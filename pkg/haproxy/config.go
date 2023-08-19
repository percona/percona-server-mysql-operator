package haproxy

import (
	"github.com/flosch/pongo2"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

const (
	CustomConfigKey  = "haproxy.cfg"
	configVolumeName = "config"
	configMountPath  = "/etc/haproxy-custom/"
)

type Configurable apiv1alpha1.PerconaServerMySQL

// GetConfigMapName returns ConfigMap name for Configurable.
func (c *Configurable) GetConfigMapName() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return Name(&cr)
}

// GetConfigMapKey returns the key for config access.
func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
}

// GetConfiguration returns HAProxy config from PerconaServerMySQL spec.
func (c *Configurable) GetConfiguration() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.Proxy.HAProxy.Configuration
}

// GetResources returns HAProxy's resource requirements from PerconaServerMySQL spec.
func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.Proxy.HAProxy.Resources
}

// ExecuteConfigurationTemplate processes a template using input and memory limit.
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
