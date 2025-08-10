package orchestrator

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

func (c *Component) Lables() map[string]string {
	cr := c.PerconaServerMySQL()
	return MatchLabels(cr)
}

func (c *Component) PodSpec() *apiv1alpha1.PodSpec {
	return &c.Spec.Orchestrator.PodSpec
}

func (c *Component) Object(ctx context.Context, cl client.Client) (client.Object, error) {
	cr := apiv1alpha1.PerconaServerMySQL(*c)

	initImage, err := k8s.InitImage(ctx, cl, &cr, c.PodSpec())
	if err != nil {
		return nil, errors.Wrap(err, "get init image")
	}

	return StatefulSet(&cr, initImage), nil
}
