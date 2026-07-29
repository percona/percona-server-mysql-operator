package mysql

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/flosch/pongo2"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/config"
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

func GetConfig(
	ctx context.Context,
	cl client.Reader,
	cr *apiv1.PerconaServerMySQL,
) (config.Section, error) {
	configurable := Configurable(*cr)
	cmName := configurable.GetConfigMapName()
	nn := types.NamespacedName{Name: cmName, Namespace: cr.Namespace}
	cm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, nn, cm); err != nil {
		return config.EmptySection, client.IgnoreNotFound(err)
	}

	data := cm.Data[configurable.GetConfigMapKey()]
	section, err := config.ParseSection(io.NopCloser(strings.NewReader(data)), "mysqld")
	if err != nil {
		return config.EmptySection, errors.Wrap(err, "parse config section")
	}

	result := config.Section{Section: *section}
	return result, nil
}

func GetLastAppliedConfig(
	sts *appsv1.StatefulSet,
) (config.Section, error) {
	val, ok := sts.GetAnnotations()[naming.AnnotationLastAppliedConfig.String()]
	if !ok {
		return config.EmptySection, nil
	}

	result, err := config.NewSection(config.NewSectionOpts{})
	if err != nil {
		return config.EmptySection, errors.Wrap(err, "create section")
	}

	result.FromJSON(io.NopCloser(strings.NewReader(val)), "mysqld")
	return *result, nil
}

func FormatConfigValue(value string) string {
	value = strings.TrimSpace(value)

	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return value
	}

	if expanded, ok := expandByteSuffix(value); ok {
		return expanded
	}

	switch strings.ToLower(value) {
	case "true", "on":
		return "ON"
	case "false", "off":
		return "OFF"
	}

	return fmt.Sprintf("'%s'", value)
}

func expandByteSuffix(value string) (string, bool) {
	// Quantity parses a bare "Gi" as zero, so require a leading digit rather
	// than turning a mangled value into a silent 0.
	if value == "" || value[0] < '0' || value[0] > '9' {
		return "", false
	}

	q, err := resource.ParseQuantity(strings.ToUpper(value) + "i")
	if err != nil {
		return "", false
	}

	return strconv.FormatInt(q.Value(), 10), true
}
