package cluster

import (
	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	v2 "github.com/percona/percona-mysql/api/v2"
	"github.com/percona/percona-mysql/pkg/database/orchestrator"
	"github.com/percona/percona-mysql/pkg/k8s"
)

func (r *MySQLReconciler) reconcileOrchestrator(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	o := orchestrator.New(cr)
	sfs := o.StatefulSet()

	if err := k8s.SetControllerReference(cr, sfs, r.Scheme); err != nil {
		return errors.Wrap(err, "set controller reference")
	}

	if err := r.createOrUpdate(sfs); err != nil {
		return errors.Wrap(err, "create or update mysql statefulset")
	}

	return nil
}
