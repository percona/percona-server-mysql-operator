package cluster

import (
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func reconcileStatefulSet(log logr.Logger, r *MySQLReconciler, cr *v2.PerconaServerForMySQL, m *mysql.MySQL) error {
	initImage, err := k8s.InitImage(r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	return ensureObject(log, r, cr, m.StatefulSet(initImage))
}

func reconcileService(log logr.Logger, r *MySQLReconciler, cr *v2.PerconaServerForMySQL, m *mysql.MySQL) error {
	return ensureObject(log, r, cr, m.Service())
}

func reconcilePrimaryService(log logr.Logger, r *MySQLReconciler, cr *v2.PerconaServerForMySQL, m *mysql.MySQL) error {
	return ensureObject(log, r, cr, m.PrimaryService())
}

func ensureObject(log logr.Logger, r *MySQLReconciler, cr *v2.PerconaServerForMySQL, obj client.Object) error {
	if err := k8s.SetControllerReference(cr, obj, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s",
			obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetName())
	}
	if err := r.createOrUpdate(log, obj); err != nil {
		return errors.Wrapf(err, "create or update %s/%s",
			obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetName())
	}

	return nil
}
