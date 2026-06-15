package manager

import (
	"context"
	"fmt"

	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
)

// memberMeta describes a member cluster of the ClusterSet as recorded in the
// mysql_innodb_cluster_metadata schema.
type memberMeta struct {
	Name string
	Role string // clusterset.ClusterRolePrimary / ClusterRoleReplica
}

// clusterConn is a connection to a single MySQL instance of the ClusterSet. It
// can read both the replicated metadata schema and the instance's local live
// group-replication / replication state.
type clusterConn interface {
	DomainName(ctx context.Context) (string, error)
	Members(ctx context.Context) ([]memberMeta, error)
	// GRPrimary returns the "host:port" of the ONLINE group-replication PRIMARY
	// member, or "" if there is none.
	GRPrimary(ctx context.Context) (string, error)
	InPrimaryPartition(ctx context.Context) (bool, error)
	ClusterSetReplicationRunning(ctx context.Context) (bool, error)
	Close() error
}

// connector opens a clusterConn to the first reachable endpoint.
type connector interface {
	connect(ctx context.Context, endpoints []apiv1.ClusterSetClusterEndpoint, pass string) (clusterConn, error)
}

// computeStatus reproduces dba.getCluster().getClusterSet().status() for the
// fields the operator consumes. It reads the replicated metadata schema from a
// reachable instance of the CR's primary cluster, then connects to each member
// cluster to read its live state.
func computeStatus(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, pass string, c connector) (clusterset.Status, error) {
	status := clusterset.Status{Clusters: apiv1.ClusterSetStatus{}}

	primaryCluster := pcs.PrimaryCluster()
	if primaryCluster == nil {
		return status, errors.New("primary cluster not found")
	}

	entry, err := c.connect(ctx, primaryCluster.Endpoints, pass)
	if err != nil {
		return status, errors.Wrap(err, "connect to primary cluster")
	}
	defer entry.Close()

	domain, err := entry.DomainName(ctx)
	if err != nil {
		return status, errors.Wrap(err, "get domain name")
	}
	status.DomainName = domain

	members, err := entry.Members(ctx)
	if err != nil {
		return status, errors.Wrap(err, "get clusterset members")
	}

	var primaryStatus string
	numOK := 0
	for _, member := range members {
		primaryAddr, globalStatus := memberStatus(ctx, pcs, pass, c, member)

		status.Clusters[member.Name] = apiv1.ClusterSetClusterStatus{
			ClusterRole:  member.Role,
			GlobalStatus: globalStatus,
			Primary:      primaryAddr,
		}

		if globalStatus == clusterset.GlobalStatusOK {
			numOK++
		}
		if member.Role == clusterset.ClusterRolePrimary {
			status.PrimaryCluster = member.Name
			status.GlobalPrimaryInstance = primaryAddr
			primaryStatus = globalStatus
		}
	}

	status.Status, status.StatusText = clusterSetGlobalStatus(primaryStatus, numOK, len(members))

	return status, nil
}

// memberStatus connects to a member cluster and derives its primary instance
// address and collapsed global status. An unreachable cluster degrades to
// UNKNOWN rather than failing the whole status call.
func memberStatus(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, pass string, c connector, member memberMeta) (primaryAddr, globalStatus string) {
	crCluster := pcs.GetCluster(member.Name)
	if crCluster == nil {
		return "", clusterset.GlobalStatusUnknown
	}

	conn, err := c.connect(ctx, crCluster.Endpoints, pass)
	if err != nil {
		return "", clusterset.GlobalStatusUnknown
	}
	defer conn.Close()

	primaryAddr, err = conn.GRPrimary(ctx)
	if err != nil {
		return "", clusterset.GlobalStatusUnknown
	}

	inPartition, err := conn.InPrimaryPartition(ctx)
	if err != nil {
		return "", clusterset.GlobalStatusUnknown
	}

	replRunning := false
	if member.Role == clusterset.ClusterRoleReplica {
		replRunning, err = conn.ClusterSetReplicationRunning(ctx)
		if err != nil {
			return "", clusterset.GlobalStatusUnknown
		}
	}

	return primaryAddr, clusterGlobalStatus(member.Role, true, inPartition, primaryAddr, replRunning)
}

// clusterSetGlobalStatus derives the overall ClusterSet status string and human
// readable status text from the primary cluster's global status and the number
// of OK clusters in the set. It mirrors the logic of MySQL Shell's
// cluster_set_status() (modules/adminapi/cluster_set/status.cc), restricted to
// the collapsed per-cluster global statuses the operator tracks.
func clusterSetGlobalStatus(primaryStatus string, numOK, numTotal int) (status string, statusText string) {
	switch primaryStatus {
	case clusterset.GlobalStatusOK:
		if numOK >= numTotal {
			return clusterset.StatusHealthy, "All Clusters available."
		}
		issues := numTotal - numOK
		if issues == 1 {
			return clusterset.StatusAvailable, "Primary Cluster available, there are issues with a Replica cluster."
		}
		return clusterset.StatusAvailable, fmt.Sprintf("Primary Cluster available, there are issues with %d Replica Clusters.", issues)
	case clusterset.GlobalStatusUnknown:
		return clusterset.StatusUnavailable, "Primary Cluster is not reachable, assuming it to be unavailable."
	default:
		return clusterset.StatusUnavailable, "Primary Cluster is not available. ClusterSet availability may be restored by restoring the Primary Cluster or failover."
	}
}

// clusterGlobalStatus derives the collapsed global status of a single member
// cluster.
func clusterGlobalStatus(role string, reachable, inPartition bool, primaryAddr string, replRunning bool) string {
	if !reachable {
		return clusterset.GlobalStatusUnknown
	}
	if !inPartition || primaryAddr == "" {
		return clusterset.GlobalStatusNotOK
	}
	if role == clusterset.ClusterRoleReplica && !replRunning {
		return clusterset.GlobalStatusNotReplicating
	}
	return clusterset.GlobalStatusOK
}
