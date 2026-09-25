package main

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

// detached is the channel orchestrator leaves on a promoted master when its own
// RESET SLAVE ALL did not go through: the source host carries orchestrator's
// detach marker, so the receiver can never resolve it again.
func detached(host string) map[string]string {
	return map[string]string{
		"Source_Host":         "//" + host,
		"Replica_IO_Running":  "No",
		"Replica_SQL_Running": "Yes",
	}
}

func TestClearSource(t *testing.T) {
	t.Run("drops the channel the promotion left behind", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.statuses = []map[string]string{detached("mysql-0.mysql")}

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, []string{"StopReplication", "ResetReplication"}, j.fake.ops)
		assert.Equal(t, 1, j.fake.closed, "the connection must be closed")
	})

	t.Run("an instance with no channel is left alone", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.err = sql.ErrNoRows

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops, "replication must not be touched")
	})

	t.Run("an instance whose receiver is connected is left alone", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.statuses = []map[string]string{{
			"Source_Host":         "mysql-0.mysql",
			"Replica_IO_Running":  "Yes",
			"Replica_SQL_Running": "Yes",
		}}

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops, "a relay log being filled must not be thrown away")
	})

	t.Run("nothing is fetched and no relay log is touched", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.statuses = []map[string]string{detached("mysql-0.mysql")}
		j.cfg.sourceURL = func(string) string {
			t.Error("the source must not be contacted")
			return ""
		}

		require.NoError(t, run(t.Context(), j.cfg))

		j.relay.assertUntouched(t)
	})

	t.Run("a splice in flight is not interrupted", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.statuses = []map[string]string{detached("mysql-0.mysql")}

		held, err := failover.Lock(j.cfg.lockPath)
		require.NoError(t, err)
		defer held.Close() //nolint:errcheck

		require.ErrorIs(t, run(t.Context(), j.cfg), failover.ErrLocked)
		assert.Empty(t, j.fake.ops, "the relay log being filled must not be thrown away")
	})

	t.Run("a failed stop surfaces", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.statuses = []map[string]string{detached("mysql-0.mysql")}
		j.fake.stopErr = errors.New("boom")

		require.ErrorContains(t, run(t.Context(), j.cfg), "boom")
		assert.Equal(t, []string{"StopReplication"}, j.fake.ops)
	})

	t.Run("a failed reset surfaces", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.statuses = []map[string]string{detached("mysql-0.mysql")}
		j.fake.resetErr = errors.New("boom")

		require.ErrorContains(t, run(t.Context(), j.cfg), "boom")
		assert.Equal(t, []string{"StopReplication", "ResetReplication"}, j.fake.ops)
	})

	t.Run("a status read that fails for another reason surfaces", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.clear = true
		j.fake.err = errors.New("boom")

		require.ErrorContains(t, run(t.Context(), j.cfg), "boom")
		assert.Empty(t, j.fake.ops)
	})
}
