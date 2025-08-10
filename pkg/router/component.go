package router

import (
	"context"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

type Component apiv1alpha1.PerconaServerMySQL

func (c *Component) Name() string {
	cr := c.PerconaServerMySQL()
	return Name(cr)
}

func (c *Component) PerconaServerMySQL() *apiv1alpha1.PerconaServerMySQL {
	cr := apiv1alpha1.PerconaServerMySQL(*c)
	return &cr
}

func (c *Component) Labels() map[string]string {
	cr := c.PerconaServerMySQL()
	return MatchLabels(cr)
}

func (c *Component) PodSpec() *apiv1alpha1.PodSpec {
	return &c.Spec.Proxy.Router.PodSpec
}

func (c *Component) Object(ctx context.Context, cl client.Client) (client.Object, error) {
	cr := c.PerconaServerMySQL()

	initImage, err := k8s.InitImage(ctx, cl, cr, c.PodSpec())
	if err != nil {
		return nil, errors.Wrap(err, "get init image")
	}

	configurable := Configurable(*cr)
	configHash, err := k8s.CustomConfigHash(ctx, cl, cr, &configurable)
	if err != nil {
		return nil, errors.Wrapf(err, "get custom config hash")
	}

	tlsHash, err := k8s.GetTLSHash(ctx, cl, cr)
	if err != nil {
		return nil, errors.Wrapf(err, "get tls hash")
	}

	return Deployment(cr, initImage, configHash, tlsHash), nil
}
