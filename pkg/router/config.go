package router

import (
	"github.com/flosch/pongo2"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

type Configurable apiv1alpha1.PerconaServerMySQL

// GetConfigMapName returns the associated ConfigMap's name.
func (c *Configurable) GetConfigMapName() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return Name(&cr)
}

// GetConfigMapKey returns the stored configuration key.
func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
}

// GetConfiguration retrieves the configuration from the instance.
func (c *Configurable) GetConfiguration() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.Proxy.Router.Configuration
}

// GetResources returns the specified resource requirements.
func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.Proxy.Router.Resources
}

// ExecuteConfigurationTemplate applies a given template and memory limit.
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
