package mysql

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/flosch/pongo2"
	"github.com/go-ini/ini"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/config"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

type Configurable apiv1.PerconaServerMySQL

func (c *Configurable) GetConfigMapName() string {
	cr := apiv1.PerconaServerMySQL(*c)
	return ConfigMapName(&cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
}

func (c *Configurable) GetConfiguration() string {
	cr := apiv1.PerconaServerMySQL(*c)
	return cr.Spec.MySQL.Configuration
}

func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1.PerconaServerMySQL(*c)
	return cr.Spec.MySQL.Resources
}

func (c *Configurable) ExecuteConfigurationTemplate(input string, memory *resource.Quantity) (string, error) {
	set, err := util.SandboxedTemplateSet()
	if err != nil {
		return "", errors.Wrap(err, "create sandboxed template set")
	}
	tmpl, err := set.FromString(input)
	if err != nil {
		return "", errors.Wrap(err, "parse template")
	}
	result, err := tmpl.Execute(pongo2.Context{"containerMemoryLimit": memory.Value()})
	if err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	return result, nil
}

func GetAutoTuneParams(cr *apiv1.PerconaServerMySQL, q *resource.Quantity) (string, error) {
	autotuneParams := ""

	poolSize := q.Value() * int64(50) / int64(100)
	instances := int64(1)                 // default value
	chunkSize := int64(1024 * 1024 * 128) // default value

	// Adjust innodb_buffer_pool_chunk_size
	// If innodb_buffer_pool_size is bigger than 1Gi, innodb_buffer_pool_instances is set to 8.
	// By default, innodb_buffer_pool_chunk_size is 128Mi and innodb_buffer_pool_size needs to be
	// multiple of innodb_buffer_pool_chunk_size * innodb_buffer_pool_instances.
	// More info: https://dev.mysql.com/doc/refman/8.0/en/innodb-buffer-pool-resize.html
	if poolSize > int64(1073741824) {
		instances = 8
		chunkSize = poolSize / instances
		// innodb_buffer_pool_chunk_size can be increased or decreased in units of 1Mi (1048576 bytes).
		// That's why we should strip redundant bytes
		chunkSize -= chunkSize % (1048576)
		poolSize = instances * chunkSize
	} else if poolSize%(instances*chunkSize) != 0 {
		// Buffer pool size must always
		// be equal to or a multiple of innodb_buffer_pool_chunk_size * innodb_buffer_pool_instances.
		// If not, this value will be adjusted
		poolSize += (instances * chunkSize) - poolSize%(instances*chunkSize)
	}

	conf := cr.Spec.MySQL.Configuration
	if !strings.Contains(conf, "innodb_buffer_pool_size") {
		poolSizeVal := strconv.FormatInt(poolSize, 10)
		autotuneParams += "\ninnodb_buffer_pool_size=" + poolSizeVal

		if !strings.Contains(conf, "innodb_buffer_pool_chunk_size") {
			chunkSizeVal := strconv.FormatInt(chunkSize, 10)
			autotuneParams += "\ninnodb_buffer_pool_chunk_size=" + chunkSizeVal
		}
	}

	if !strings.Contains(conf, "max_connections") {
		divider := int64(12582880)
		if q.Value() < divider {
			return "", errors.New("not enough memory set in requests. Must be >= 12Mi")
		}
		maxConnSize := q.Value() / divider
		maxConnSizeVal := strconv.FormatInt(maxConnSize, 10)
		autotuneParams += "\nmax_connections=" + maxConnSizeVal
	}

	return autotuneParams, nil
}

func ReconcileConfigMap(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
) error {
	cfg := Configurable(*cr)
	cmName := cfg.GetConfigMapName()
	nn := types.NamespacedName{Name: cmName, Namespace: cr.Namespace}
	currCm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, nn, currCm); err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrapf(err, "get ConfigMap/%s", cmName)
	}
	if cfg.GetConfiguration() == "" {
		if err := cl.Get(ctx, nn, currCm); err != nil {
			if !k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		// ConfigMap exists and is created by the user, not the operator
		if !metav1.IsControlledBy(currCm, cr) {
			if currCm.Data["my.cnf"] == "" {
				return errors.New("Failed to update config map. Please use my.cnf as a config name. Only in this case config map will be applied to the cluster")
			}
			return nil
		}

		if err := cl.Delete(ctx, currCm); err != nil {
			return errors.Wrapf(err, "delete ConfigMaps/%s", cmName)
		}
		return nil
	}

	var memory *resource.Quantity
	if res := cfg.GetResources(); res.Size() > 0 {
		if _, ok := res.Requests[corev1.ResourceMemory]; ok {
			memory = res.Requests.Memory()
		}
		if _, ok := res.Limits[corev1.ResourceMemory]; ok {
			memory = res.Limits.Memory()
		}
	}

	configuration := cfg.GetConfiguration()
	if memory != nil {
		var err error
		configuration, err = cfg.ExecuteConfigurationTemplate(cfg.GetConfiguration(), memory)
		if err != nil {
			return errors.Wrap(err, "execute configuration template")
		}
	} else if strings.Contains(configuration, "{{") {
		return errors.New("resources.limits[memory] or resources.requests[memory] should be specified for template usage in configuration")
	}

	cm := k8s.ConfigMap(cr, cmName, cfg.GetConfigMapKey(), configuration, naming.ComponentDatabase)
	if !k8s.EqualConfigMaps(currCm, cm) {
		if err := k8s.EnsureObject(ctx, cl, cr, cm, cl.Scheme()); err != nil {
			return errors.Wrapf(err, "ensure ConfigMap/%s", cmName)
		}
	}
	return nil
}

func GetConfig(
	ctx context.Context,
	cl client.Reader,
	cr *apiv1.PerconaServerMySQL,
) (ini.Section, error) {
	configurable := Configurable(*cr)
	cmName := configurable.GetConfigMapName()
	nn := types.NamespacedName{Name: cmName, Namespace: cr.Namespace}
	cm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, nn, cm); err != nil {
		return ini.Section{}, client.IgnoreNotFound(err)
	}

	data := cm.Data[configurable.GetConfigMapKey()]
	section, err := config.ParseSection(io.NopCloser(strings.NewReader(data)), "mysqld")
	if err != nil {
		return ini.Section{}, errors.Wrap(err, "parse config section")
	}
	for _, k := range section.Keys() {
		k.SetValue(sanitizeConfigValue(k.Value()))
	}
	return *section, nil
}

func sanitizeConfigValue(value string) string {
	_, err := strconv.ParseFloat(value, 64)
	if err == nil {
		return value
	}
	return fmt.Sprintf("'%s'", value)
}
