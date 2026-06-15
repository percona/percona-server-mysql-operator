package manager

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
)

// fakeConn is a hand-rolled fake clusterConn returning canned values.
type fakeConn struct {
	domain      string
	members     []memberMeta
	grPrimary   string
	inPartition bool
	replRunning bool
	closed      bool
	queryErr    error // returned by the live-state reads when set
}

func (f *fakeConn) DomainName(context.Context) (string, error)    { return f.domain, nil }
func (f *fakeConn) Members(context.Context) ([]memberMeta, error) { return f.members, nil }
func (f *fakeConn) GRPrimary(context.Context) (string, error)     { return f.grPrimary, f.queryErr }
func (f *fakeConn) InPrimaryPartition(context.Context) (bool, error) {
	return f.inPartition, f.queryErr
}
func (f *fakeConn) ClusterSetReplicationRunning(context.Context) (bool, error) {
	return f.replRunning, f.queryErr
}
func (f *fakeConn) Close() error               { f.closed = true; return nil }
func (f *fakeConn) Ping(context.Context) error { return f.queryErr }

// fakeConnector maps a cluster's first endpoint host to a fakeConn. A host with
// no entry is treated as unreachable.
type fakeConnector struct {
	conns map[string]*fakeConn
}

func (c *fakeConnector) connect(_ context.Context, endpoints []apiv1.ClusterSetClusterEndpoint, _ string) (clusterConn, error) {
	if len(endpoints) == 0 {
		return nil, errors.Wrap(mysqlsh.ErrEndpointUnreachable, "no endpoints")
	}
	conn, ok := c.conns[endpoints[0].Host]
	if !ok {
		return nil, errors.Wrap(mysqlsh.ErrEndpointUnreachable, "unreachable")
	}
	return conn, nil
}

func endpoints(host string) []apiv1.ClusterSetClusterEndpoint {
	return []apiv1.ClusterSetClusterEndpoint{{Host: host}}
}

func pcsWith(primary string, names ...string) *apiv1.PerconaServerMySQLClusterSet {
	pcs := &apiv1.PerconaServerMySQLClusterSet{}
	pcs.Spec.PrimaryCluster = primary
	for _, n := range names {
		pcs.Spec.Clusters = append(pcs.Spec.Clusters, apiv1.ClusterSetCluster{
			InnoDBClusterName: n,
			Endpoints:         endpoints(n + "-host"),
		})
	}
	return pcs
}

func TestComputeStatus_Healthy(t *testing.T) {
	pcs := pcsWith("cluster1", "cluster1", "cluster2", "cluster3")

	members := []memberMeta{
		{Name: "cluster1", Role: clusterset.ClusterRolePrimary},
		{Name: "cluster2", Role: clusterset.ClusterRoleReplica},
		{Name: "cluster3", Role: clusterset.ClusterRoleReplica},
	}
	conns := map[string]*fakeConn{
		"cluster1-host": {domain: "mydomain", members: members, grPrimary: "cluster1-0:3306", inPartition: true},
		"cluster2-host": {members: members, grPrimary: "cluster2-0:3306", inPartition: true, replRunning: true},
		"cluster3-host": {members: members, grPrimary: "cluster3-0:3306", inPartition: true, replRunning: true},
	}

	status, err := computeStatus(context.Background(), pcs, "pass", &fakeConnector{conns: conns})
	require.NoError(t, err)

	assert.Equal(t, "mydomain", status.DomainName)
	assert.Equal(t, "cluster1", status.PrimaryCluster)
	assert.Equal(t, "cluster1-0:3306", status.GlobalPrimaryInstance)
	assert.Equal(t, clusterset.StatusHealthy, status.Status)
	assert.Equal(t, "All Clusters available.", status.StatusText)

	require.Len(t, status.Clusters, 3)
	assert.Equal(t, clusterset.ClusterRolePrimary, status.Clusters["cluster1"].ClusterRole)
	assert.Equal(t, clusterset.GlobalStatusOK, status.Clusters["cluster1"].GlobalStatus)
	assert.Equal(t, "cluster1-0:3306", status.Clusters["cluster1"].Primary)
	assert.Equal(t, clusterset.GlobalStatusOK, status.Clusters["cluster2"].GlobalStatus)
	assert.Equal(t, clusterset.GlobalStatusOK, status.Clusters["cluster3"].GlobalStatus)
}

func TestComputeStatus_AvailableReplicaNotReplicating(t *testing.T) {
	pcs := pcsWith("cluster1", "cluster1", "cluster2")

	members := []memberMeta{
		{Name: "cluster1", Role: clusterset.ClusterRolePrimary},
		{Name: "cluster2", Role: clusterset.ClusterRoleReplica},
	}
	conns := map[string]*fakeConn{
		"cluster1-host": {domain: "mydomain", members: members, grPrimary: "cluster1-0:3306", inPartition: true},
		"cluster2-host": {members: members, grPrimary: "cluster2-0:3306", inPartition: true, replRunning: false},
	}

	status, err := computeStatus(context.Background(), pcs, "pass", &fakeConnector{conns: conns})
	require.NoError(t, err)

	assert.Equal(t, clusterset.StatusAvailable, status.Status)
	assert.Equal(t, "Primary Cluster available, there are issues with a Replica cluster.", status.StatusText)
	assert.Equal(t, clusterset.GlobalStatusNotReplicating, status.Clusters["cluster2"].GlobalStatus)
}

func TestComputeStatus_UnreachableReplicaIsUnknown(t *testing.T) {
	pcs := pcsWith("cluster1", "cluster1", "cluster2")

	members := []memberMeta{
		{Name: "cluster1", Role: clusterset.ClusterRolePrimary},
		{Name: "cluster2", Role: clusterset.ClusterRoleReplica},
	}
	conns := map[string]*fakeConn{
		"cluster1-host": {domain: "mydomain", members: members, grPrimary: "cluster1-0:3306", inPartition: true},
		// cluster2-host intentionally absent -> unreachable
	}

	status, err := computeStatus(context.Background(), pcs, "pass", &fakeConnector{conns: conns})
	require.NoError(t, err)

	assert.Equal(t, clusterset.StatusAvailable, status.Status)
	assert.Equal(t, clusterset.GlobalStatusUnknown, status.Clusters["cluster2"].GlobalStatus)
	assert.Equal(t, "", status.Clusters["cluster2"].Primary)
}

func TestComputeStatus_PrimaryUnreachableIsUnavailable(t *testing.T) {
	pcs := pcsWith("cluster1", "cluster1", "cluster2")

	// Entry connection (to the primary cluster) is unreachable -> overall error.
	status, err := computeStatus(context.Background(), pcs, "pass", &fakeConnector{conns: map[string]*fakeConn{}})
	require.Error(t, err)
	assert.ErrorIs(t, err, mysqlsh.ErrEndpointUnreachable)
	assert.Empty(t, status.Clusters)
}

func TestComputeStatus_MemberQueryErrorIsUnknown(t *testing.T) {
	pcs := pcsWith("cluster1", "cluster1", "cluster2")

	members := []memberMeta{
		{Name: "cluster1", Role: clusterset.ClusterRolePrimary},
		{Name: "cluster2", Role: clusterset.ClusterRoleReplica},
	}
	conns := map[string]*fakeConn{
		"cluster1-host": {domain: "mydomain", members: members, grPrimary: "cluster1-0:3306", inPartition: true},
		// cluster2 is reachable but its live-state queries fail.
		"cluster2-host": {members: members, queryErr: errors.New("query failed")},
	}

	status, err := computeStatus(context.Background(), pcs, "pass", &fakeConnector{conns: conns})
	require.NoError(t, err)

	assert.Equal(t, clusterset.StatusAvailable, status.Status)
	assert.Equal(t, clusterset.GlobalStatusUnknown, status.Clusters["cluster2"].GlobalStatus)
	assert.Equal(t, "", status.Clusters["cluster2"].Primary)
}

func TestComputeStatus_Switchover(t *testing.T) {
	// CR still designates cluster1 as primary, but metadata reports cluster2 as PRIMARY.
	pcs := pcsWith("cluster1", "cluster1", "cluster2")

	members := []memberMeta{
		{Name: "cluster1", Role: clusterset.ClusterRoleReplica},
		{Name: "cluster2", Role: clusterset.ClusterRolePrimary},
	}
	conns := map[string]*fakeConn{
		"cluster1-host": {domain: "mydomain", members: members, grPrimary: "cluster1-0:3306", inPartition: true, replRunning: true},
		"cluster2-host": {members: members, grPrimary: "cluster2-0:3306", inPartition: true},
	}

	status, err := computeStatus(context.Background(), pcs, "pass", &fakeConnector{conns: conns})
	require.NoError(t, err)

	assert.Equal(t, "cluster2", status.PrimaryCluster)
	assert.Equal(t, "cluster2-0:3306", status.GlobalPrimaryInstance)
	assert.Equal(t, clusterset.StatusHealthy, status.Status)
}
