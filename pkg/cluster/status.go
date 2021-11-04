package cluster

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

type component interface {
	GetSize() int32
	MatchLabels() map[string]string
}

func (r *MySQLReconciler) updateStatus(cr *v2.PerconaServerForMySQL) error {
	m := mysql.New(cr)
	mysqlStatus, err := r.appStatus(cr, m)
	if err != nil {
		return errors.Wrap(err, "get MySQL status")
	}
	cr.Status.MySQL = mysqlStatus

	o := orchestrator.New(cr)
	orcStatus, err := r.appStatus(cr, o)
	if err != nil {
		return errors.Wrap(err, "get Orchestrator status")
	}
	cr.Status.Orchestrator = orcStatus

	return r.writeStatus(cr)
}

func (r *MySQLReconciler) appStatus(cr *v2.PerconaServerForMySQL, app component) (v2.StatefulAppStatus, error) {
	status := v2.StatefulAppStatus{
		Size:  app.GetSize(),
		State: v2.StateInitializing,
	}

	podList, err := r.podListByLabel(app.MatchLabels())
	if err != nil {
		return status, errors.Wrap(err, "get pod list")
	}

	for _, pod := range podList.Items {
		if k8s.IsPodReady(pod) {
			status.Ready++
		}
	}

	if status.Ready == status.Size {
		status.State = v2.StateReady
	}

	return status, nil
}

func (r *MySQLReconciler) writeStatus(cr *v2.PerconaServerForMySQL) error {
	err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &v2.PerconaServerForMySQL{}

		err := r.Client.Get(context.TODO(), types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, c)
		if err != nil {
			return err
		}

		c.Status = cr.Status

		return r.Client.Status().Update(context.TODO(), c)
	})

	return errors.Wrap(err, "write status")
}
