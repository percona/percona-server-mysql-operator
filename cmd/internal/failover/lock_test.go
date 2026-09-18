package failover

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock(t *testing.T) {
	t.Run("creates the lock file and holds it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })

		assert.FileExists(t, path)
	})

	t.Run("a second holder is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		first, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { first.Close() })

		_, err = Lock(path)
		require.ErrorIs(t, err, ErrLocked)
	})

	t.Run("closing releases it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		first, err := Lock(path)
		require.NoError(t, err)
		require.NoError(t, first.Close())

		second, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { second.Close() })
	})

	t.Run("a leftover file with no holder is acquirable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		require.NoError(t, os.WriteFile(path, []byte("stale"), 0o644))

		f, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
	})

	t.Run("an unusable path is an error, not a false lock", func(t *testing.T) {
		_, err := Lock(filepath.Join(t.TempDir(), "missing-dir", "failover.lock"))

		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrLocked)
	})
}

func TestInProgress(t *testing.T) {
	t.Run("a held lock is a failover in progress", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })

		inProgress, err := InProgress(path)
		require.NoError(t, err)
		assert.True(t, inProgress)
	})

	t.Run("no lock file means no failover", func(t *testing.T) {
		inProgress, err := InProgress(filepath.Join(t.TempDir(), "failover.lock"))

		require.NoError(t, err)
		assert.False(t, inProgress)
	})

	t.Run("a leftover file with no holder means no failover", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		require.NoError(t, os.WriteFile(path, nil, 0o644))

		inProgress, err := InProgress(path)

		require.NoError(t, err)
		assert.False(t, inProgress)
	})

	t.Run("releasing ends the failover", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := Lock(path)
		require.NoError(t, err)
		require.NoError(t, f.Close())

		inProgress, err := InProgress(path)
		require.NoError(t, err)
		assert.False(t, inProgress)
	})

	t.Run("probing does not create the lock file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		_, err := InProgress(path)
		require.NoError(t, err)

		assert.NoFileExists(t, path)
	})

	t.Run("probing leaves the lock acquirable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		require.NoError(t, os.WriteFile(path, nil, 0o644))

		_, err := InProgress(path)
		require.NoError(t, err)

		f, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
	})

	t.Run("probing does not disturb the holder", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })

		_, err = InProgress(path)
		require.NoError(t, err)

		_, err = Lock(path)
		require.ErrorIs(t, err, ErrLocked)
	})

	t.Run("an unusable path is an error, not a silent no", func(t *testing.T) {
		inProgress, err := InProgress(filepath.Join(t.TempDir(), strings.Repeat("a", 300)))

		require.Error(t, err)
		assert.False(t, inProgress)
	})
}

func TestLockWait(t *testing.T) {
	t.Run("acquires a free lock without waiting", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := LockWait(t.Context(), path, time.Second, time.Millisecond)

		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
		assert.FileExists(t, path)
	})

	t.Run("waits for the holder to release", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		held, err := Lock(path)
		require.NoError(t, err)

		go func() {
			time.Sleep(50 * time.Millisecond)
			held.Close() //nolint:errcheck
		}()

		f, err := LockWait(t.Context(), path, 5*time.Second, time.Millisecond)

		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
	})

	t.Run("gives up once the wait is spent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		held, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { held.Close() })

		_, err = LockWait(t.Context(), path, 20*time.Millisecond, time.Millisecond)

		require.ErrorIs(t, err, ErrLocked)
	})

	t.Run("a cancelled context stops the wait", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		held, err := Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { held.Close() })

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = LockWait(ctx, path, time.Hour, time.Millisecond)

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("an unusable path fails without waiting", func(t *testing.T) {
		start := time.Now()

		_, err := LockWait(t.Context(), filepath.Join(t.TempDir(), "missing-dir", "l"), time.Hour, time.Millisecond)

		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrLocked)
		assert.Less(t, time.Since(start), time.Second, "a broken path must not be retried until the wait is spent")
	})
}
