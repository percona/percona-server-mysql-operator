package cluster

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (r *MySQLReconciler) reconcileOrchestrator(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	o := orchestrator.New(cr)

	usersSecret := &corev1.Secret{}
	if err := r.Client.Get(context.TODO(),
		types.NamespacedName{Name: cr.Spec.SecretsName, Namespace: cr.Namespace},
		usersSecret,
	); err != nil {
		return errors.Wrapf(err, "get %s/%s", usersSecret.Kind, cr.Spec.SecretsName)
	}

	orcPass := string(usersSecret.Data[v2.USERS_SECRET_KEY_ORCHESTRATOR])
	secret := o.Secret(v2.USERS_SECRET_KEY_ORCHESTRATOR, orcPass)

	if err := k8s.SetControllerReference(cr, secret, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", secret.Kind, secret.Name)
	}

	if err := r.createOrUpdate(log, secret); err != nil {
		return errors.Wrapf(err, "create or update %s/%s", secret.Kind, secret.Name)
	}

	svc := o.Service(cr)

	if err := k8s.SetControllerReference(cr, svc, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", svc.Kind, svc.Name)
	}

	if err := r.createOrUpdate(log, svc); err != nil {
		return errors.Wrapf(err, "create or update %s/%s", svc.Kind, svc.Name)
	}

	sfs := o.StatefulSet()

	if err := k8s.SetControllerReference(cr, sfs, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", sfs.Kind, sfs.Name)
	}

	if err := r.createOrUpdate(log, sfs); err != nil {
		return errors.Wrapf(err, "create or update %s/%s", sfs.Kind, sfs.Name)
	}

	return nil
}
