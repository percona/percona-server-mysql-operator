package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockSplice(t *testing.T) {
	t.Run("creates the lock file and holds it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := lockSplice(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() }) //nolint:errcheck

		assert.FileExists(t, path)
	})

	t.Run("a second holder is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		first, err := lockSplice(path)
		require.NoError(t, err)
		t.Cleanup(func() { first.Close() }) //nolint:errcheck

		_, err = lockSplice(path)
		require.ErrorIs(t, err, errLocked)
	})

	t.Run("closing releases it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		first, err := lockSplice(path)
		require.NoError(t, err)
		require.NoError(t, first.Close())

		second, err := lockSplice(path)
		require.NoError(t, err)
		t.Cleanup(func() { second.Close() }) //nolint:errcheck
	})

	t.Run("a leftover file with no holder is acquirable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")
		require.NoError(t, os.WriteFile(path, []byte("stale"), 0o644))

		f, err := lockSplice(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() }) //nolint:errcheck
	})

	t.Run("an unusable path is an error, not a false lock", func(t *testing.T) {
		_, err := lockSplice(filepath.Join(t.TempDir(), "missing-dir", "failover.lock"))

		require.Error(t, err)
		assert.NotErrorIs(t, err, errLocked)
	})
}
