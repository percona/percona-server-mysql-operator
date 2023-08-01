package router

import (
	"github.com/flosch/pongo2"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

type Configurable apiv1alpha1.PerconaServerMySQL

func (c *Configurable) GetConfigMapName() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return Name(&cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
}

func (c *Configurable) GetConfiguration() string {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.Proxy.Router.Configuration
}

func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return cr.Spec.Proxy.Router.Resources
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
