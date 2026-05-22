package clusterset

import (
	batchv1 "k8s.io/api/batch/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func BootstrapJob(pcs *apiv1.PerconaServerMySQLClusterSet) *batchv1.Job {
	return &batchv1.Job{}
}
