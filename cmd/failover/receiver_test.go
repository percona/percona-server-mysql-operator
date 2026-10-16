package main

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

// gaveUp is a channel whose receiver spent its connect retries while the source was down
func gaveUp(host string) map[string]string {
	return map[string]string{
		"Source_Host":         host,
		"Replica_IO_Running":  "No",
		"Replica_SQL_Running": "Yes",
	}
}

func TestStartReceiver(t *testing.T) {
	newFixture := func(t *testing.T, status map[string]string) *jobFixture {
		j := newJobFixture(t)
		j.cfg.startReceiver = true
		j.cfg.source = "mysql-0.mysql"
		j.fake.statuses = []map[string]string{status}

		return j
	}

	t.Run("starts a receiver that gave up on the source", func(t *testing.T) {
		j := newFixture(t, gaveUp("mysql-0.mysql"))

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, []string{"StartIOThread"}, j.fake.ops)
		assert.Equal(t, 1, j.fake.closed, "the connection must be closed")
	})

	t.Run("a receiver still retrying is left alone", func(t *testing.T) {
		status := gaveUp("mysql-0.mysql")
		status["Replica_IO_Running"] = "Connecting"
		j := newFixture(t, status)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops)
	})

	t.Run("a connected receiver is left alone", func(t *testing.T) {
		status := gaveUp("mysql-0.mysql")
		status["Replica_IO_Running"] = "Yes"
		j := newFixture(t, status)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops)
	})

	t.Run("a channel to another source is left alone", func(t *testing.T) {
		j := newFixture(t, gaveUp("mysql-2.mysql"))

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops)
	})

	t.Run("an instance with no channel is left alone", func(t *testing.T) {
		j := newFixture(t, nil)
		j.fake.err = sql.ErrNoRows

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops)
	})

	t.Run("a splice in flight is not interrupted", func(t *testing.T) {
		j := newFixture(t, gaveUp("mysql-0.mysql"))

		held, err := failover.Lock(j.cfg.lockPath)
		require.NoError(t, err)
		defer held.Close() //nolint:errcheck

		require.ErrorIs(t, run(t.Context(), j.cfg), failover.ErrLocked)
		assert.Empty(t, j.fake.ops, "a receiver the splice stopped must stay stopped")
	})

	t.Run("nothing is fetched and no relay log is touched", func(t *testing.T) {
		j := newFixture(t, gaveUp("mysql-0.mysql"))
		j.cfg.sourceURL = func(string) string {
			t.Error("the source must not be contacted")
			return ""
		}

		require.NoError(t, run(t.Context(), j.cfg))

		j.relay.assertUntouched(t)
	})

	t.Run("a missing source is rejected", func(t *testing.T) {
		j := newFixture(t, gaveUp("mysql-0.mysql"))
		j.cfg.source = " "

		require.EqualError(t, run(t.Context(), j.cfg), "-source is not set")
		assert.Empty(t, j.fake.ops)
	})

	t.Run("a failed start surfaces", func(t *testing.T) {
		j := newFixture(t, gaveUp("mysql-0.mysql"))
		j.fake.ioErr = errors.New("boom")

		require.EqualError(t, run(t.Context(), j.cfg), "start IO_THREAD: boom")
		assert.Equal(t, []string{"StartIOThread"}, j.fake.ops)
	})

	t.Run("a status read that fails for another reason surfaces", func(t *testing.T) {
		j := newFixture(t, nil)
		j.fake.err = errors.New("boom")

		require.EqualError(t, run(t.Context(), j.cfg), "show replica status: boom")
		assert.Empty(t, j.fake.ops)
	})
}
