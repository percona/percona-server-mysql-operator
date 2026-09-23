package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	failoverlock "github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

const source = "cluster1-mysql-1.cluster1-mysql.ps-7382"

func testGate(t *testing.T) *gate {
	t.Helper()

	return &gate{
		dir:       filepath.Join(t.TempDir(), "orc-handler"),
		claimIdle: time.Minute,
		beat:      time.Millisecond,
		idle:      seenIdle,
	}
}

const uid = "3b0f4d2e-recovery"

func TestGateEnter(t *testing.T) {
	t.Run("the first caller gets in", func(t *testing.T) {
		g := testGate(t)

		release, err := g.enter()

		require.NoError(t, err)
		release()
	})

	t.Run("a second caller is refused at once", func(t *testing.T) {
		g := testGate(t)

		release, err := g.enter()
		require.NoError(t, err)
		t.Cleanup(release)

		_, err = g.enter()

		require.ErrorIs(t, err, failoverlock.ErrLocked)
	})

	t.Run("releasing lets the next caller in", func(t *testing.T) {
		g := testGate(t)

		release, err := g.enter()
		require.NoError(t, err)
		release()

		second, err := g.enter()

		require.NoError(t, err)
		second()
	})
}

func TestGateClaim(t *testing.T) {
	t.Run("an unclaimed source is claimed", func(t *testing.T) {
		g := testGate(t)

		require.NoError(t, g.claim(source, uid))

		holder, _, ok, err := g.holder(source)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, uid, holder)
	})

	t.Run("a claimed source is refused to another recovery", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		err := g.claim(source, "another")

		require.ErrorIs(t, err, errClaimed)
		assert.Contains(t, err.Error(), uid, "the refusal must name the holder")
	})

	t.Run("a claimed source is refused to the same recovery too", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		require.ErrorIs(t, g.claim(source, uid), errClaimed)
	})

	t.Run("another source is untouched by the claim", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		require.NoError(t, g.claim("cluster1-mysql-0.cluster1-mysql.ps-7382", "another"))
	})

	t.Run("an idle claim is taken over", func(t *testing.T) {
		g := testGate(t)
		g.claimIdle = time.Millisecond
		require.NoError(t, g.claim(source, uid))

		time.Sleep(10 * time.Millisecond)

		require.NoError(t, g.claim(source, "another"), "a recovery that lost its post hook must not hold the source forever")

		holder, _, _, err := g.holder(source)
		require.NoError(t, err)
		assert.Equal(t, "another", holder)
	})

	t.Run("keeping the claim stops it going idle", func(t *testing.T) {
		g := testGate(t)
		g.claimIdle = 40 * time.Millisecond
		require.NoError(t, g.claim(source, uid))

		stop := g.keepClaim(t.Context(), source)
		t.Cleanup(stop)

		time.Sleep(100 * time.Millisecond)

		require.ErrorIs(t, g.claim(source, "another"), errClaimed)
	})

	t.Run("stopping the keeper waits for the last beat", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		stop := g.keepClaim(t.Context(), source)
		time.Sleep(10 * time.Millisecond)
		stop()

		released, err := g.release(source, uid)
		require.NoError(t, err)
		require.True(t, released)

		_, _, ok, err := g.holder(source)
		require.NoError(t, err)
		assert.False(t, ok, "no beat may bring the claim back after the release")
	})

	t.Run("the gate held by another caller refuses the claim", func(t *testing.T) {
		g := testGate(t)
		release, err := g.enter()
		require.NoError(t, err)
		t.Cleanup(release)

		require.ErrorIs(t, g.claim(source, uid), errClaimed)
	})

	t.Run("a claim without a uid can be released without one", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, ""))

		released, err := g.release(source, "")

		require.NoError(t, err)
		assert.True(t, released)
	})

	names := map[string]string{
		"empty":            "",
		"a path separator": "../../etc/passwd",
		"a bare dot":       ".",
		"a double dot":     "..",
		"only separators":  "///",
	}

	for name, bad := range names {
		t.Run("refuses a source name that is "+name, func(t *testing.T) {
			g := testGate(t)

			require.Error(t, g.claim(bad, uid))

			_, err := g.release(bad, uid)
			require.Error(t, err)
		})
	}

	t.Run("a claim stays inside the gate directory", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		entries, err := os.ReadDir(filepath.Join(g.dir, claimDir))

		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, source, entries[0].Name())
	})
}

func TestGateRelease(t *testing.T) {
	t.Run("the holder releases its claim", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		released, err := g.release(source, uid)

		require.NoError(t, err)
		assert.True(t, released)
		require.NoError(t, g.claim(source, "another"), "a released source is free again")
	})

	t.Run("another recovery cannot release the claim", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		released, err := g.release(source, "another")

		require.NoError(t, err)
		assert.False(t, released)
		require.ErrorIs(t, g.claim(source, "another"), errClaimed, "the claim must survive a stranger's release")
	})

	t.Run("releasing an unclaimed source is nothing", func(t *testing.T) {
		g := testGate(t)

		released, err := g.release(source, uid)

		require.NoError(t, err)
		assert.False(t, released)
	})
}

func TestGuardedFailover(t *testing.T) {
	t.Run("runs the failover and keeps the claim for the post hook", func(t *testing.T) {
		g := testGate(t)
		calls := 0

		err := guardedFailover(t.Context(), g, source, uid, func(context.Context) error {
			calls++
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, calls)

		require.ErrorIs(t, g.claim(source, "another"), errClaimed, "the source stays claimed until the recovery is finished")
	})

	t.Run("refuses a source another recovery holds without running", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, "another"))
		calls := 0

		err := guardedFailover(t.Context(), g, source, uid, func(context.Context) error {
			calls++
			return nil
		})

		require.ErrorIs(t, err, errClaimed)
		assert.Zero(t, calls, "a second splice must not run while the first recovery is in flight")
	})

	t.Run("a failed failover releases the claim", func(t *testing.T) {
		g := testGate(t)
		failure := errors.New("exec into the pod failed")

		err := guardedFailover(t.Context(), g, source, uid, func(context.Context) error {
			return failure
		})

		require.ErrorIs(t, err, failure)
		require.NoError(t, g.claim(source, "another"), "a transient failure must leave the next attempt free to retry")
	})

	t.Run("the next attempt retries after a failure", func(t *testing.T) {
		g := testGate(t)
		calls := 0

		run := func(context.Context) error {
			calls++
			if calls == 1 {
				return errors.New("connection refused")
			}
			return nil
		}

		require.Error(t, guardedFailover(t.Context(), g, source, "first", run))
		require.NoError(t, guardedFailover(t.Context(), g, source, "second", run))

		assert.Equal(t, 2, calls)
	})

	t.Run("the claim is kept alive while the failover runs", func(t *testing.T) {
		g := testGate(t)
		g.claimIdle = 40 * time.Millisecond

		err := guardedFailover(t.Context(), g, source, uid, func(context.Context) error {
			time.Sleep(100 * time.Millisecond)
			return nil
		})

		require.NoError(t, err)
		require.ErrorIs(t, g.claim(source, "another"), errClaimed, "a long failover must not lose its claim")
	})

	t.Run("a source that is not a host name never reaches the failover", func(t *testing.T) {
		g := testGate(t)
		calls := 0

		err := guardedFailover(t.Context(), g, "../escape", uid, func(context.Context) error {
			calls++
			return nil
		})

		require.Error(t, err)
		assert.Zero(t, calls)
	})

	t.Run("the gate lock is released whichever way the failover ends", func(t *testing.T) {
		g := testGate(t)

		require.Error(t, guardedFailover(t.Context(), g, source, uid, func(context.Context) error {
			return errors.New("boom")
		}))

		release, err := g.enter()
		require.NoError(t, err, "the gate must not stay held after a failure")
		release()
	})
}

func TestGateFirstSeen(t *testing.T) {
	t.Run("nothing is being timed to start with", func(t *testing.T) {
		g := testGate(t)

		_, ok, err := g.firstSeen(source)

		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("the first attempt starts the clock", func(t *testing.T) {
		g := testGate(t)

		markSeen(t, g, source)
		at, ok, err := g.firstSeen(source)

		require.NoError(t, err)
		require.True(t, ok)
		assert.WithinDuration(t, time.Now(), at, time.Minute)
	})

	t.Run("later attempts keep counting from the first", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, time.Hour)

		// A mark that moved forward would hand every retry the full timeout
		// again, so the budget would never run out.
		markSeen(t, g, source)

		first, ok, err := g.firstSeen(source)
		require.NoError(t, err)
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(-time.Hour), first, time.Minute, "markSeen must not restart the clock")
	})

	t.Run("attempts keep the mark from going idle", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, 2*seenIdle)

		_, ok, err := g.firstSeen(source)

		require.NoError(t, err)
		assert.True(t, ok, "a failure that is still being reported is still being timed")
	})

	t.Run("a finished recovery stops the clock", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		require.NoError(t, g.claim(source, uid))

		finish(g, source, uid)

		_, ok, err := g.firstSeen(source)
		require.NoError(t, err)
		assert.False(t, ok, "the next failure has to start with the full timeout")
	})

	t.Run("a promotion clears every clock", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		markSeen(t, g, "other-host")

		require.NoError(t, g.clearAllSeen())

		for _, host := range []string{source, "other-host"} {
			_, ok, err := g.firstSeen(host)
			require.NoError(t, err)
			assert.Falsef(t, ok, "%s is still being timed", host)
		}
	})

	t.Run("clearing is idempotent", func(t *testing.T) {
		g := testGate(t)

		require.NoError(t, g.clearSeen(source))
		require.NoError(t, g.clearAllSeen())
	})

	t.Run("an idle mark is ignored", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		require.NoError(t, backdate(t, g, seenDir, source, seenIdle+time.Minute))

		_, ok, err := g.firstSeen(source)

		require.NoError(t, err)
		assert.False(t, ok, "a cluster repaired by hand must not leave the next failure out of time")
	})

	t.Run("a mark this binary did not write is ignored", func(t *testing.T) {
		g := testGate(t)
		path, err := g.markPath(seenDir, source)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("not a time"), 0o644))

		_, ok, err := g.firstSeen(source)

		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("a source that is not a host name is refused", func(t *testing.T) {
		g := testGate(t)

		for _, bad := range []string{"", ".", "..", "a/b"} {
			_, err := g.markSeen(bad)
			require.Error(t, err, "markSeen(%q)", bad)

			_, _, err = g.firstSeen(bad)
			require.Error(t, err, "firstSeen(%q)", bad)
		}
	})
}

func TestGateRefreshSeen(t *testing.T) {
	t.Run("bumps the mark without restarting the clock", func(t *testing.T) {
		g := testGate(t)
		markSeen(t, g, source)
		backdateSeen(t, g, source, time.Hour)
		require.NoError(t, backdate(t, g, seenDir, source, 2*seenIdle))

		require.NoError(t, g.refreshSeen(source))

		first, ok, err := g.firstSeen(source)
		require.NoError(t, err)
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(-time.Hour), first, time.Minute)
	})

	t.Run("leaves a missing mark missing", func(t *testing.T) {
		g := testGate(t)

		require.NoError(t, g.refreshSeen(source))

		_, ok, err := g.firstSeen(source)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestGateNotifyOnce(t *testing.T) {
	t.Run("the first caller reports, the next stays quiet", func(t *testing.T) {
		g := testGate(t)

		first, err := g.notifyOnce(source)
		require.NoError(t, err)
		assert.True(t, first)

		// Orchestrator retries a blocked recovery every few seconds.
		second, err := g.notifyOnce(source)
		require.NoError(t, err)
		assert.False(t, second)
	})

	t.Run("it reports again once the interval is spent", func(t *testing.T) {
		g := testGate(t)
		_, err := g.notifyOnce(source)
		require.NoError(t, err)

		require.NoError(t, backdate(t, g, notifiedDir, source, notifyInterval+time.Minute))

		again, err := g.notifyOnce(source)
		require.NoError(t, err)
		assert.True(t, again)
	})

	t.Run("sources are tracked apart", func(t *testing.T) {
		g := testGate(t)
		_, err := g.notifyOnce(source)
		require.NoError(t, err)

		other, err := g.notifyOnce("other-host")
		require.NoError(t, err)
		assert.True(t, other)
	})
}

// backdate ages a mark so a test does not have to wait for the real interval.
func markSeen(t *testing.T, g *gate, source string) {
	t.Helper()

	_, err := g.markSeen(source)
	require.NoError(t, err)
}

// backdateSeen moves the first attempt at source into the past while leaving
// the last one where it is, the shape of a failure that keeps being reported.
func backdateSeen(t *testing.T, g *gate, source string, by time.Duration) {
	t.Helper()

	path, err := g.markPath(seenDir, source)
	require.NoError(t, err)

	first := time.Now().Add(-by).Format(time.RFC3339Nano)
	require.NoError(t, os.WriteFile(path, []byte(first), 0o644))
}

func backdate(t *testing.T, g *gate, dir, host string, by time.Duration) error {
	t.Helper()

	path, err := g.markPath(dir, host)
	require.NoError(t, err)

	at := time.Now().Add(-by)

	return os.Chtimes(path, at, at)
}
