package orchestrator

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
	return Labels(cr)
}

func (c *Component) MatchLabels() map[string]string {
	cr := c.PerconaServerMySQL()
	return MatchLabels(cr)
}

func (c *Component) PodSpec() *apiv1alpha1.PodSpec {
	return &c.Spec.Orchestrator.PodSpec
}

func (c *Component) Object(ctx context.Context, cl client.Client) (client.Object, error) {
	cr := c.PerconaServerMySQL()

	initImage, err := k8s.InitImage(ctx, cl, cr, c.PodSpec())
	if err != nil {
		return nil, errors.Wrap(err, "get init image")
	}

	configMap := &corev1.ConfigMap{}
	configMapName := client.ObjectKey{Name: ConfigMapName(cr), Namespace: cr.Namespace}
	configHash := ""
	if err := cl.Get(ctx, configMapName, configMap); err == nil {
		configHash, err = k8s.ObjectHash(configMap)
		if err != nil {
			return nil, errors.Wrap(err, "calculate config map hash")
		}
	} else if !k8serrors.IsNotFound(err) {
		return nil, errors.Wrap(err, "get config map")
	}

	tlsHash, err := k8s.GetTLSHash(ctx, cl, cr)
	if err != nil {
		return nil, errors.Wrapf(err, "get tls hash")
	}

	return StatefulSet(cr, initImage, configHash, tlsHash), nil
}
