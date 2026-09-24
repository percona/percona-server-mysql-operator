package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	failoverlock "github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

func TestPodName(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{
		Name: "cluster1", Namespace: "ps",
	}

	tests := map[string]struct {
		host string
		want string
	}{
		"the hostname orchestrator reports": {
			host: "cluster1-mysql-0.cluster1-mysql.ps",
			want: "cluster1-mysql-0",
		},
		"without the namespace": {
			host: "cluster1-mysql-0.cluster1-mysql",
			want: "cluster1-mysql-0",
		},
		"a bare pod name": {
			host: "cluster1-mysql-0",
			want: "cluster1-mysql-0",
		},
		"a host of another cluster is left alone": {
			host: "cluster2-mysql-0.cluster2-mysql.ps2",
			want: "cluster2-mysql-0.cluster2-mysql.ps2",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, podName(cr, tt.host))
		})
	}
}

func TestRunFailoverSkipsNonFailoverRecoveries(t *testing.T) {
	tests := []string{
		"NoWriteableMasterStructureWarning",
		"MasterSingleReplicaNotReplicating",
		"UnreachableMaster",
		"",
	}

	for _, failureType := range tests {
		t.Run(failureType, func(t *testing.T) {
			err := runFailover(t.Context(), []string{
				"-source", "cluster1-mysql-0.cluster1-mysql.ps",
				"-failure-type", failureType,
			})

			require.NoError(t, err)
		})
	}
}

// A planned takeover synthesizes a DeadMaster recovery on a primary that is
// alive, frozen and already caught up with, so there is nothing to splice.
func TestRunFailoverSkipsPlannedTakeovers(t *testing.T) {
	tests := []string{
		"graceful-master-takeover",
		"force-master-takeover",
	}

	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			err := runFailover(context.Background(), []string{
				"-source", "cluster1-mysql-0.cluster1-mysql.ps",
				"-failure-type", "DeadMaster",
				"-command", command,
			})

			require.NoError(t, err)
		})
	}
}

func TestRunFailoverRejectsBadInput(t *testing.T) {
	tests := map[string]struct {
		args []string
		want string
	}{
		"no source": {
			args: []string{"-failure-type", "DeadMaster"},
			want: "source flag should not be empty",
		},
		"a non-positive timeout": {
			args: []string{"-source", source, "-failure-type", "DeadMaster", "-timeout", "0s"},
			want: "timeout must be positive",
		},
		"an unknown policy": {
			args: []string{"-source", source, "-failure-type", "DeadMaster", "-on-timeout", "Force"},
			want: "on-timeout must be Abort or ForceWithPossibleDataLoss",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := runFailover(context.Background(), tt.args)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestBudget(t *testing.T) {
	const timeout = time.Hour

	t.Run("the first attempt gets the whole timeout", func(t *testing.T) {
		g := testGate(t)

		remaining, _, err := budget(g, source, timeout)

		require.NoError(t, err)
		assert.InDelta(t, timeout, remaining, float64(time.Minute))
	})

	t.Run("only the first attempt starts the clock", func(t *testing.T) {
		g := testGate(t)

		_, started, err := budget(g, source, timeout)
		require.NoError(t, err)
		assert.True(t, started)

		_, started, err = budget(g, source, timeout)
		require.NoError(t, err)
		assert.False(t, started)
	})

	t.Run("later attempts get what is left", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, 40*time.Minute)

		remaining, _, err := budget(g, source, timeout)

		require.NoError(t, err)
		assert.InDelta(t, 20*time.Minute, remaining, float64(time.Minute))
	})

	t.Run("a spent budget goes negative", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, 2*timeout)

		remaining, _, err := budget(g, source, timeout)

		require.NoError(t, err)
		assert.Negative(t, remaining)
	})

	// Orchestrator retries a recovery every RecoveryPeriodBlockSeconds, and the
	// attempts that leave a cluster stuck are the ones that fail in under a
	// second. A per-attempt timeout would never fire for them.
	t.Run("fast failures still spend the budget", func(t *testing.T) {
		g := testGate(t)

		for range 5 {
			remaining, _, err := budget(g, source, timeout)
			require.NoError(t, err)
			require.Positive(t, remaining)
		}

		backdateSeen(t, g, source, 2*timeout)

		remaining, _, err := budget(g, source, timeout)
		require.NoError(t, err)
		assert.Negative(t, remaining)
	})

	t.Run("a timeout longer than the idle bound still runs out", func(t *testing.T) {
		const timeout = 3 * seenIdle
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, timeout+time.Hour)

		remaining, _, err := budget(g, source, timeout)

		require.NoError(t, err)
		assert.Negative(t, remaining)
	})

	t.Run("an attempt longer than the idle bound still spends the budget", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		// The attempt started 2*seenIdle ago and is only now on its way out.
		backdateSeen(t, g, source, 2*seenIdle)
		require.NoError(t, backdate(t, g, seenDir, source, 2*seenIdle))

		require.NoError(t, g.refreshSeen(source))

		remaining, _, err := budget(g, source, timeout)
		require.NoError(t, err)
		assert.Negative(t, remaining)
	})

	t.Run("an idle mark is replaced, not just ignored", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		require.NoError(t, backdate(t, g, seenDir, source, seenIdle+time.Minute))

		remaining, _, err := budget(g, source, timeout)
		require.NoError(t, err)
		assert.InDelta(t, timeout, remaining, float64(time.Minute))

		// The clock has to start on this attempt, not the next one.
		at, ok, err := g.firstSeen(source)
		require.NoError(t, err)
		require.True(t, ok)
		assert.WithinDuration(t, time.Now(), at, time.Minute)
	})

	t.Run("sources are timed apart", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, 2*timeout)

		remaining, _, err := budget(g, "other-host", timeout)

		require.NoError(t, err)
		assert.Positive(t, remaining)
	})
}

// timedOut needs a cluster to talk to for anything beyond the abort path, and
// there is none in a unit test. What can be pinned down here is that aborting
// stays the outcome that needs no cluster at all, and that it reports why.
func TestTimedOutAborts(t *testing.T) {
	g := testGate(t)

	err := timedOut(context.Background(), g, source, "", time.Hour, apiv1.FailoverPolicyAbort)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not recover the transactions stranded on "+source)
	assert.Contains(t, err.Error(), "within 1h0m0s")
}

// The attempt that hands the whole budget to the worker still has to probe
// the source and acknowledge the recovery once the worker is done.
func TestHookTimeoutOutlastsTheBudget(t *testing.T) {
	timeout := time.Hour

	assert.GreaterOrEqual(t, hookTimeout(timeout), timeout+sourcePodWait+probeTimeout+ackWait)
}

func TestIsSourceRecovered(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"no error":                     {err: nil},
		"an ordinary failure":          {err: errors.New("exec failed, stdout: , stderr: ERROR: connection refused")},
		"a failure naming the outcome": {err: errors.New("exec failed, stdout: " + failoverlock.ResultSourceRecovered + "\n, stderr: "), want: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSourceRecovered(tt.err))
		})
	}
}
