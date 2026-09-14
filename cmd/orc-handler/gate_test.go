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
		dir:  filepath.Join(t.TempDir(), "orc-handler"),
		ttl:  time.Minute,
		wait: 50 * time.Millisecond,
		poll: time.Millisecond,
	}
}

func TestGateEnter(t *testing.T) {
	t.Run("the first caller gets in", func(t *testing.T) {
		g := testGate(t)

		release, err := g.enter(t.Context())

		require.NoError(t, err)
		release()
	})

	t.Run("a second caller waits and gets in once the first leaves", func(t *testing.T) {
		g := testGate(t)
		g.wait = 5 * time.Second

		release, err := g.enter(t.Context())
		require.NoError(t, err)

		go func() {
			time.Sleep(50 * time.Millisecond)
			release()
		}()

		second, err := g.enter(t.Context())

		require.NoError(t, err)
		second()
	})

	t.Run("a second caller gives up once the wait is spent", func(t *testing.T) {
		g := testGate(t)

		release, err := g.enter(t.Context())
		require.NoError(t, err)
		t.Cleanup(release)

		_, err = g.enter(t.Context())

		require.ErrorIs(t, err, failoverlock.ErrLocked)
	})

	t.Run("releasing lets the next caller in", func(t *testing.T) {
		g := testGate(t)

		release, err := g.enter(t.Context())
		require.NoError(t, err)
		release()

		second, err := g.enter(t.Context())

		require.NoError(t, err)
		second()
	})
}

func TestGateHandled(t *testing.T) {
	t.Run("an unseen source is not handled", func(t *testing.T) {
		g := testGate(t)

		handled, err := g.handled(source)

		require.NoError(t, err)
		assert.False(t, handled)
	})

	t.Run("a marked source is handled", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.markHandled(source))

		handled, err := g.handled(source)

		require.NoError(t, err)
		assert.True(t, handled)
	})

	t.Run("another source is untouched by the mark", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.markHandled(source))

		handled, err := g.handled("cluster1-mysql-0.cluster1-mysql.ps-7382")

		require.NoError(t, err)
		assert.False(t, handled)
	})

	t.Run("a mark older than the ttl is stale", func(t *testing.T) {
		g := testGate(t)
		g.ttl = time.Millisecond
		require.NoError(t, g.markHandled(source))

		time.Sleep(10 * time.Millisecond)
		handled, err := g.handled(source)

		require.NoError(t, err)
		assert.False(t, handled, "a stale mark must not suppress a fresh failover")
	})

	t.Run("marking again refreshes the mark", func(t *testing.T) {
		g := testGate(t)
		g.ttl = 40 * time.Millisecond
		require.NoError(t, g.markHandled(source))

		time.Sleep(30 * time.Millisecond)
		require.NoError(t, g.markHandled(source))
		time.Sleep(20 * time.Millisecond)

		handled, err := g.handled(source)

		require.NoError(t, err)
		assert.True(t, handled)
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

			require.Error(t, g.markHandled(bad))

			_, err := g.handled(bad)
			require.Error(t, err)
		})
	}

	t.Run("a mark stays inside the gate directory", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.markHandled(source))

		entries, err := os.ReadDir(filepath.Join(g.dir, handledDir))

		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, source, entries[0].Name())
	})
}

func TestGuardedFailover(t *testing.T) {
	t.Run("runs the failover and marks the source", func(t *testing.T) {
		g := testGate(t)
		calls := 0

		err := guardedFailover(t.Context(), g, source, func(context.Context) error {
			calls++
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, calls)

		handled, err := g.handled(source)
		require.NoError(t, err)
		assert.True(t, handled)
	})

	t.Run("skips a source another attempt already handled", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.markHandled(source))
		calls := 0

		err := guardedFailover(t.Context(), g, source, func(context.Context) error {
			calls++
			return nil
		})

		require.NoError(t, err)
		assert.Zero(t, calls, "a second splice must not run for a source already handled")
	})

	t.Run("a failed failover leaves no mark", func(t *testing.T) {
		g := testGate(t)
		failure := errors.New("exec into the pod failed")

		err := guardedFailover(t.Context(), g, source, func(context.Context) error {
			return failure
		})

		require.ErrorIs(t, err, failure)

		handled, err := g.handled(source)
		require.NoError(t, err)
		assert.False(t, handled, "a transient failure must leave the next attempt free to retry")
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

		require.Error(t, guardedFailover(t.Context(), g, source, run))
		require.NoError(t, guardedFailover(t.Context(), g, source, run))

		assert.Equal(t, 2, calls)
	})

	t.Run("an attempt in flight blocks this one without running it", func(t *testing.T) {
		g := testGate(t)
		release, err := g.enter(t.Context())
		require.NoError(t, err)
		t.Cleanup(release)

		calls := 0
		err = guardedFailover(t.Context(), g, source, func(context.Context) error {
			calls++
			return nil
		})

		require.ErrorIs(t, err, failoverlock.ErrLocked)
		assert.Zero(t, calls)
	})

	t.Run("a source that is not a host name never reaches the failover", func(t *testing.T) {
		g := testGate(t)
		calls := 0

		err := guardedFailover(t.Context(), g, "../escape", func(context.Context) error {
			calls++
			return nil
		})

		require.Error(t, err)
		assert.Zero(t, calls)
	})

	t.Run("the gate is released whichever way the failover ends", func(t *testing.T) {
		g := testGate(t)

		require.Error(t, guardedFailover(t.Context(), g, source, func(context.Context) error {
			return errors.New("boom")
		}))

		release, err := g.enter(t.Context())
		require.NoError(t, err, "the gate must not stay held after a failure")
		release()
	})
}
