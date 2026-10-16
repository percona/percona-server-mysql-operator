package orchestrator

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	restclient "k8s.io/client-go/rest"
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

func TestIsPlannedTakeover(t *testing.T) {
	tests := map[string]struct {
		command string
		want    bool
	}{
		"a switchover demotes a live primary": {
			command: "graceful-master-takeover",
			want:    true,
		},
		"a forced takeover also demotes a live primary": {
			command: "force-master-takeover",
			want:    true,
		},
		"a forced failover still means the primary is gone": {
			command: "force-master-failover",
			want:    false,
		},
		"a failure orchestrator detected on its own": {
			command: "",
			want:    false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPlannedTakeover(tt.command))
		})
	}
}

func TestAckRecoveryEndpoint(t *testing.T) {
	endpoint := ackRecoveryEndpoint("1790096871059981569:ad6d704a", "orc-handler: recovery finished")

	assert.Equal(t, "api/ack-recovery/uid/1790096871059981569:ad6d704a?comment=orc-handler%3A+recovery+finished", endpoint)
}

// orcAnswer answers every orchestrator API call with body
type orcAnswer string

func (a orcAnswer) Exec(_ context.Context, _ *corev1.Pod, _ string, _ []string, _ io.Reader, stdout, _ io.Writer, _ bool) error {
	_, err := io.WriteString(stdout, string(a))
	return err
}

func (orcAnswer) REST() restclient.Interface { return nil }
func (orcAnswer) Config() *restclient.Config { return nil }

func TestGetInstance(t *testing.T) {
	t.Run("instance", func(t *testing.T) {
		answer := orcAnswer(`{"Key":{"Hostname":"mysql-0","Port":3306},"IsDowntimed":true,"DowntimeReason":"lost-in-recovery"}`)

		instance, err := GetInstance(t.Context(), answer, &corev1.Pod{}, "mysql-0", 3306)

		require.NoError(t, err)
		assert.Equal(t, InstanceKey{Hostname: "mysql-0", Port: 3306}, instance.Key)
		assert.True(t, instance.IsDowntimed)
		assert.Equal(t, "lost-in-recovery", instance.DowntimeReason)
	})

	t.Run("error answer", func(t *testing.T) {
		answer := orcAnswer(`{"Code":"ERROR","Message":"Cannot read instance: mysql-0:3306"}`)

		instance, err := GetInstance(t.Context(), answer, &corev1.Pod{}, "mysql-0", 3306)

		require.EqualError(t, err, "Cannot read instance: mysql-0:3306")
		assert.Nil(t, instance)
	})

	t.Run("empty answer", func(t *testing.T) {
		instance, err := GetInstance(t.Context(), orcAnswer(""), &corev1.Pod{}, "mysql-0", 3306)

		require.ErrorIs(t, err, ErrEmptyResponse)
		assert.Nil(t, instance)
	})
}
