package cluster

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (r *MySQLReconciler) reconcileMySQL(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	m := mysql.New(cr)

	cfg := m.Configuration()
	cm := m.ConfigMap(cfg)
	if err := k8s.SetControllerReference(cr, cm, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", cm.Kind, cm.Name)
	}
	if err := r.Client.Create(context.TODO(), cm); err != nil && !k8serrors.IsAlreadyExists(err) {
		return errors.Wrapf(err, "create %s/%s", cm.Kind, cm.Name)
	}

	sfs := m.StatefulSet()

	initImage, err := k8s.InitImage(r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}
	sfs.Spec.Template.Spec.InitContainers = m.InitContainers(initImage)

	if err := k8s.SetControllerReference(cr, sfs, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", sfs.Kind, sfs.Name)
	}
	if err := r.createOrUpdate(log, sfs); err != nil {
		return errors.Wrapf(err, "create or update %s/%s", sfs.Kind, sfs.Name)
	}

	svc := m.Service()
	if err := k8s.SetControllerReference(cr, svc, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", svc.Kind, svc.Name)
	}
	if err := r.createOrUpdate(log, svc); err != nil {
		return errors.Wrapf(err, "create or update %s/%s", svc.Kind, svc.Name)
	}

	primarySvc := m.PrimaryService()
	if err := k8s.SetControllerReference(cr, primarySvc, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", svc.Kind, svc.Name)
	}
	if err := r.createOrUpdate(log, primarySvc); err != nil {
		return errors.Wrapf(err, "create or update %s/%s", svc.Kind, svc.Name)
	}

	return nil
}
