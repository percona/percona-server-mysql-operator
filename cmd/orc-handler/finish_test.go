package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func TestRunFinishRejectsBadInput(t *testing.T) {
	err := runFinish(t.Context(), []string{"-uid", uid})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source flag should not be empty")
}

func TestFinish(t *testing.T) {
	t.Run("frees the source for the next recovery", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		finish(g, source, uid)

		require.NoError(t, g.claim(source, "another"))
	})

	t.Run("leaves a claim held by another recovery alone", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, "another"))

		finish(g, source, uid)

		require.ErrorIs(t, g.claim(source, uid), errClaimed)
	})

	t.Run("is harmless for a source nobody claimed", func(t *testing.T) {
		g := testGate(t)

		finish(g, source, uid)
	})
}

func TestEndLostInRecovery(t *testing.T) {
	const instance = "api/instance/cluster1-mysql-0.cluster1-mysql.ps/3306"
	const endDowntime = "api/end-downtime/cluster1-mysql-0.cluster1-mysql.ps/3306"

	downtimed := func(reason string) map[string]any {
		return map[string]any{instance: orchestrator.Instance{IsDowntimed: true, DowntimeReason: reason}}
	}

	tests := map[string]struct {
		failureType string
		bodies      map[string]any
		failed      map[string]bool
		calls       []string
		err         string
	}{
		"failed promotion": {
			failureType: "DeadMaster",
			bodies:      downtimed(orchestrator.DowntimeReasonLostInRecovery),
			calls:       []string{instance, endDowntime},
		},
		"someone else's downtime is left alone": {
			failureType: "DeadMaster",
			bodies:      downtimed(orchestrator.DowntimeReasonForcePromote),
			calls:       []string{instance},
		},
		"source without a downtime": {
			failureType: "DeadMasterAndSomeReplicas",
			bodies:      map[string]any{instance: orchestrator.Instance{}},
			calls:       []string{instance},
		},
		"successful recovery passes no failure type": {
			bodies: downtimed(orchestrator.DowntimeReasonLostInRecovery),
		},
		"downtime that does not end": {
			failureType: "DeadMaster",
			bodies:      downtimed(orchestrator.DowntimeReasonLostInRecovery),
			failed:      map[string]bool{endDowntime: true},
			calls:       []string{instance, endDowntime},
			err:         "end the downtime of cluster1-mysql-0.cluster1-mysql.ps: Cannot deduce instance",
		},
		"recovery the hook does not gate": {
			failureType: "DeadIntermediateMaster",
			bodies:      downtimed(orchestrator.DowntimeReasonLostInRecovery),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			api := &orcAPI{bodies: tt.bodies, failed: tt.failed}

			err := endLostInRecovery(t.Context(), api, &corev1.Pod{}, "cluster1-mysql-0.cluster1-mysql.ps", tt.failureType)
			if tt.err != "" {
				require.EqualError(t, err, tt.err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.calls, api.calls)
		})
	}
}
