package mysql

import (
	"strconv"
	"strings"

	"github.com/flosch/pongo2"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

type Configurable apiv1alpha1.PerconaServerMySQL

func (c *Configurable) GetConfigMapName() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return ConfigMapName(&cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
}

func (c *Configurable) GetConfiguration() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.MySQL.Configuration
}

func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.MySQL.Resources
}

func (c *Configurable) ExecuteConfigurationTemplate(input string, memory *resource.Quantity) (string, error) {
	tmpl, err := pongo2.FromString(input)
	if err != nil {
		return "", errors.Wrap(err, "parse template")
	}
	result, err := tmpl.Execute(pongo2.Context{"containerMemoryLimit": memory.Value()})
	if err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	return result, nil
}

func GetAutoTuneParams(cr *apiv1alpha1.PerconaServerMySQL, q *resource.Quantity) (string, error) {
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
