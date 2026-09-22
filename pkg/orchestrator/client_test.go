package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsMasterFailover(t *testing.T) {
	tests := map[string]struct {
		analysis string
		want     bool
	}{
		"a dead primary": {
			analysis: "DeadMaster",
			want:     true,
		},
		"a dead primary that took some replicas with it": {
			analysis: "DeadMasterAndSomeReplicas",
			want:     true,
		},
		"a read_only primary is fixed in place": {
			analysis: "NoWriteableMasterStructureWarning",
			want:     false,
		},
		"an unreachable primary is only re-read": {
			analysis: "UnreachableMaster",
			want:     false,
		},
		"a replica that stopped replicating": {
			analysis: "MasterSingleReplicaNotReplicating",
			want:     false,
		},
		"an intermediate primary keeps the topology's primary": {
			analysis: "DeadIntermediateMaster",
			want:     false,
		},
		"a dead primary with no replicas has nothing to promote": {
			analysis: "DeadMasterWithoutReplicas",
			want:     false,
		},
		"no analysis at all": {
			analysis: "",
			want:     false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsMasterFailover(tt.analysis))
		})
	}
}
