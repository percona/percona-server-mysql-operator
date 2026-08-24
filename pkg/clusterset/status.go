package clusterset

import (
	"fmt"
	"strings"
	"time"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

const (
	StatusHealthy string = "HEALTHY"
	StatusUnknown string = "UNKNOWN"
)

const (
	ClusterRolePrimary string = "PRIMARY"
	ClusterRoleReplica string = "REPLICA"
)

type ClusterStatuses map[string]ClusterStatus

func (cs ClusterStatuses) IntoAPI() apiv1.ClusterSetClusterStatuses {
	result := make(apiv1.ClusterSetClusterStatuses, len(cs))
	for name, status := range cs {
		result[name] = apiv1.ClusterSetClusterStatus{
			ClusterRole:           status.ClusterRole,
			GlobalStatus:          status.GlobalStatus,
			Primary:               status.Primary,
			ReplicationLagSeconds: status.getPrimaryMemberReplicationLagSeconds(),
		}
	}
	return result
}

type Status struct {
	Clusters              ClusterStatuses `json:"clusters"`
	DomainName            string          `json:"domainName"`
	GlobalPrimaryInstance string          `json:"globalPrimaryInstance"`
	PrimaryCluster        string          `json:"primaryCluster"`
	Status                string          `json:"status"`
	StatusText            string          `json:"statusText"`
}

type ClusterStatus struct {
	ClusterRole  string                    `json:"clusterRole"`
	GlobalStatus string                    `json:"globalStatus"`
	Primary      string                    `json:"primary"`
	Topology     map[string]TopologyStatus `json:"topology"`
}

func (cs ClusterStatus) getPrimaryMemberReplicationLagSeconds() *int64 {
	// Primary cluster, no lag expected as this is source
	if cs.ClusterRole == ClusterRolePrimary {
		return nil
	}
	for _, topo := range cs.Topology {
		// Only consider the primary member of the replica cluster for lag
		if topo.MemberRole != ClusterRolePrimary {
			continue
		}

		lagDurationStr := topo.ReplicationLagFromOriginalSource
		if lagDurationStr == "" {
			return nil
		}
		return new(int64(mysqlTimediffToDuration(lagDurationStr).Seconds()))
	}
	return nil
}

func mysqlTimediffToDuration(timediff string) time.Duration {
	parts := strings.Split(timediff, ":")
	if len(parts) != 3 {
		return 0
	}

	hours := parts[0]
	minutes := parts[1]
	seconds := parts[2]
	durationString := fmt.Sprintf("%sh%sm%ss", hours, minutes, seconds)
	duration, err := time.ParseDuration(durationString)
	if err != nil {
		return 0
	}
	return duration
}

type TopologyStatus struct {
	Status                            string `json:"status"`
	MemberRole                        string `json:"memberRole"`
	MemberState                       string `json:"memberState"`
	ReplicationLagFromImmediateSource string `json:"replicationLagFromImmediateSource"`
	ReplicationLagFromOriginalSource  string `json:"replicationLagFromOriginalSource"`
}

func (s *Status) GetPrimaryCluster() string {
	for name, cluster := range s.Clusters {
		if cluster.ClusterRole == ClusterRolePrimary {
			return name
		}
	}
	return ""
}
