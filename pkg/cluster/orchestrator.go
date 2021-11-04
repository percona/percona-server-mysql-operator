package cluster

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator"
	orclient "github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator/client"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (r *MySQLReconciler) reconcileOrchestrator(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	o := orchestrator.New(cr)

	svc := o.Service()
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

func (r *MySQLReconciler) discoverMySQL(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	o := orchestrator.New(cr)
	orc := orclient.New(o.APIHost())

	m := mysql.New(cr)

	for i := 0; i < int(m.Size); i++ {
		mysqlHost := fmt.Sprintf("%s-%d.%s.%s", m.Name(), i, m.ServiceName(), m.Namespace())
		_, err := orc.Discover(mysqlHost, mysql.DefaultPort)
		if err != nil {
			return errors.Wrapf(err, "discover host %s", mysqlHost)
		}
	}

	return nil
}
