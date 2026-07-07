package clusterset

import apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"

const (
	StatusHealthy string = "HEALTHY"
	StatusUnknown string = "UNKNOWN"
)

const (
	ClusterRolePrimary string = "PRIMARY"
	ClusterRoleReplica string = "REPLICA"
)

type Status struct {
	Clusters              apiv1.ClusterSetStatus
	DomainName            string `json:"domainName"`
	GlobalPrimaryInstance string `json:"globalPrimaryInstance"`
	PrimaryCluster        string `json:"primaryCluster"`
	Status                string `json:"status"`
	StatusText            string `json:"statusText"`
}

func (s *Status) GetPrimaryCluster() string {
	for name, cluster := range s.Clusters {
		if cluster.ClusterRole == ClusterRolePrimary {
			return name
		}
	}
	return ""
}
