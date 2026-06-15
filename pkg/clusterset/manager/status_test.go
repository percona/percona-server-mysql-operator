package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
)

func TestClusterGlobalStatus(t *testing.T) {
	tests := []struct {
		name        string
		role        string
		reachable   bool
		inPartition bool
		primaryAddr string
		replRunning bool
		want        string
	}{
		{
			name:      "unreachable cluster is unknown",
			role:      clusterset.ClusterRolePrimary,
			reachable: false,
			want:      clusterset.GlobalStatusUnknown,
		},
		{
			name:        "primary with quorum and online primary is ok",
			role:        clusterset.ClusterRolePrimary,
			reachable:   true,
			inPartition: true,
			primaryAddr: "primary-0:3306",
			want:        clusterset.GlobalStatusOK,
		},
		{
			name:        "primary without quorum is not ok",
			role:        clusterset.ClusterRolePrimary,
			reachable:   true,
			inPartition: false,
			primaryAddr: "",
			want:        clusterset.GlobalStatusNotOK,
		},
		{
			name:        "reachable cluster with no online primary is not ok",
			role:        clusterset.ClusterRolePrimary,
			reachable:   true,
			inPartition: true,
			primaryAddr: "",
			want:        clusterset.GlobalStatusNotOK,
		},
		{
			name:        "replica replicating is ok",
			role:        clusterset.ClusterRoleReplica,
			reachable:   true,
			inPartition: true,
			primaryAddr: "replica-0:3306",
			replRunning: true,
			want:        clusterset.GlobalStatusOK,
		},
		{
			name:        "replica not replicating is ok-not-replicating",
			role:        clusterset.ClusterRoleReplica,
			reachable:   true,
			inPartition: true,
			primaryAddr: "replica-0:3306",
			replRunning: false,
			want:        clusterset.GlobalStatusNotReplicating,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clusterGlobalStatus(tt.role, tt.reachable, tt.inPartition, tt.primaryAddr, tt.replRunning)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClusterSetGlobalStatus(t *testing.T) {
	tests := []struct {
		name          string
		primaryStatus string
		numOK         int
		numTotal      int
		wantStatus    string
		wantText      string
	}{
		{
			name:          "all clusters ok is healthy",
			primaryStatus: "OK",
			numOK:         3,
			numTotal:      3,
			wantStatus:    "HEALTHY",
			wantText:      "All Clusters available.",
		},
		{
			name:          "primary ok with one replica issue is available, singular",
			primaryStatus: "OK",
			numOK:         2,
			numTotal:      3,
			wantStatus:    "AVAILABLE",
			wantText:      "Primary Cluster available, there are issues with a Replica cluster.",
		},
		{
			name:          "primary ok with multiple replica issues is available, plural",
			primaryStatus: "OK",
			numOK:         1,
			numTotal:      3,
			wantStatus:    "AVAILABLE",
			wantText:      "Primary Cluster available, there are issues with 2 Replica Clusters.",
		},
		{
			name:          "primary not ok is unavailable",
			primaryStatus: "NOT_OK",
			numOK:         0,
			numTotal:      3,
			wantStatus:    "UNAVAILABLE",
			wantText:      "Primary Cluster is not available. ClusterSet availability may be restored by restoring the Primary Cluster or failover.",
		},
		{
			name:          "primary unknown is unavailable",
			primaryStatus: "UNKNOWN",
			numOK:         0,
			numTotal:      3,
			wantStatus:    "UNAVAILABLE",
			wantText:      "Primary Cluster is not reachable, assuming it to be unavailable.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, text := clusterSetGlobalStatus(tt.primaryStatus, tt.numOK, tt.numTotal)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantText, text)
		})
	}
}
