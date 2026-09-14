package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestIsReplicationStopExpected(t *testing.T) {
	// Keeps isBackupRunning from reaching for the sidecar.
	t.Setenv(naming.EnvBackupsEnabled, "false")

	t.Run("a failover holding the lock is expected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := failover.Lock(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })

		expected, err := isReplicationStopExpected(context.Background(), path)

		require.NoError(t, err)
		assert.True(t, expected)
	})

	t.Run("no failover and no backup is not expected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		expected, err := isReplicationStopExpected(context.Background(), path)

		require.NoError(t, err)
		assert.False(t, expected)
	})

	t.Run("a released lock is not expected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failover.lock")

		f, err := failover.Lock(path)
		require.NoError(t, err)
		require.NoError(t, f.Close())

		expected, err := isReplicationStopExpected(context.Background(), path)

		require.NoError(t, err)
		assert.False(t, expected)
	})
}
