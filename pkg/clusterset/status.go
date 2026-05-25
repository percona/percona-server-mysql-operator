package clusterset

import apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"

const (
	StatusHealthy string = "HEALTHY"
)

const (
	ClusterRolePrimary   string = "PRIMARY"
	ClusterRoleSECONDARY string = "REPLICA"
)

type Status struct {
	Clusters              apiv1.ClusterSetStatus
	DomainName            string `json:"domainName"`
	GlobalPrimaryInstance string `json:"globalPrimaryInstance"`
	PrimaryCluster        string `json:"primaryCluster"`
	Status                string `json:"status"`
	StatusText            string `json:"statusText"`
}
