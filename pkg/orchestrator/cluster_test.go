package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func member(host, cluster string, valid bool) *Instance {
	return &Instance{
		Key:              InstanceKey{Hostname: host, Port: 3306},
		ClusterName:      cluster,
		IsLastCheckValid: valid,
	}
}

func TestLiveCluster(t *testing.T) {
	const hint = "cluster1.ns"

	tests := map[string]struct {
		instances []*Instance
		want      string
		wantSplit bool
	}{
		"a single cluster": {
			instances: []*Instance{
				member("mysql-0", "mysql-0:3306", true),
				member("mysql-1", "mysql-0:3306", true),
				member("mysql-2", "mysql-0:3306", true),
			},
			want: "mysql-0:3306",
		},
		"a cluster with a dead primary is still live": {
			instances: []*Instance{
				member("mysql-0", "mysql-0:3306", false),
				member("mysql-1", "mysql-0:3306", true),
			},
			want: "mysql-0:3306",
		},
		"the dead primary a failover left behind is ignored": {
			instances: []*Instance{
				member("mysql-0", "mysql-0:3306", false),
				member("mysql-1", "mysql-1:3306", true),
				member("mysql-2", "mysql-1:3306", true),
			},
			want: "mysql-1:3306",
		},
		"an orphaned primary splits the topology": {
			instances: []*Instance{
				member("mysql-0", "mysql-0:3306", true),
				member("mysql-1", "mysql-0:3306", true),
				member("mysql-2", "mysql-2:3306", true),
			},
			wantSplit: true,
		},
		"an orphan splits the topology even when the primary is dead": {
			instances: []*Instance{
				member("mysql-0", "mysql-0:3306", false),
				member("mysql-1", "mysql-0:3306", true),
				member("mysql-2", "mysql-2:3306", true),
			},
			wantSplit: true,
		},
		"nothing reachable falls back to the hint": {
			instances: []*Instance{
				member("mysql-0", "mysql-0:3306", false),
				member("mysql-1", "mysql-1:3306", false),
			},
			want: hint,
		},
		"nothing discovered falls back to the hint": {
			want: hint,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := liveCluster(tt.instances, hint)
			if tt.wantSplit {
				assert.ErrorIs(t, err, ErrSplitTopology)
				assert.ErrorContains(t, err, "mysql-0:3306, mysql-2:3306")
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
