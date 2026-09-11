package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/handler"
)

type sourceLayout struct {
	dir      string
	url      string
	position uint64
}

func newSourceLayout(t *testing.T) sourceLayout {
	t.Helper()

	dir := t.TempDir()
	binlogFile(t, dir, "binlog.000004", "four-head"+"four-tail")
	binlogFile(t, dir, "binlog.000005", "whole-five")
	binlogFile(t, dir, "binlog.000006", "whole-six")
	writeIndex(t, filepath.Join(dir, "binlog.index"),
		"./binlog.000004", "./binlog.000005", "./binlog.000006")

	srv := httptest.NewServer(&handler.FailoverHandler{DataDir: dir})
	t.Cleanup(srv.Close)

	return sourceLayout{dir: dir, url: srv.URL, position: uint64(len(magic + "four-head"))}
}

func newStagingDir(t *testing.T) string {
	t.Helper()

	staging := filepath.Join(t.TempDir(), "source-logs")
	require.NoError(t, os.MkdirAll(staging, 0o755))
	writeFile(t, staging, "binlog.999999", []byte("STALE"))

	return staging
}

func TestFailoverRoundTrip(t *testing.T) {
	t.Run("splices the missing binary logs into the relay log", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)
		staging := newStagingDir(t)

		before, err := os.ReadFile(relay.target)
		require.NoError(t, err)

		logs, err := fetchLogsFromSource(t.Context(), staging, src.url, "binlog.000004", src.position)
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(staging, "binlog.000004"),
			filepath.Join(staging, "binlog.000005"),
			filepath.Join(staging, "binlog.000006"),
		}, logs)

		staged, err := os.ReadDir(staging)
		require.NoError(t, err)
		assert.Len(t, staged, 3, "the leftover from the earlier run must be gone")

		target, startPos, err := updateRelayLogs(logs, "relay-bin.000002", relay.basename, relay.index)
		require.NoError(t, err)
		assert.Equal(t, relay.target, target)
		assert.Equal(t, uint64(len(before)), startPos)

		after, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"four-tail"+"whole-five"+"whole-six", string(after))
		assert.Equal(t, 1, bytes.Count(after, binlogMagic))
		relay.assertUntouched(t)
	})

	t.Run("the replica had read the whole first log", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)

		before, err := os.ReadFile(relay.target)
		require.NoError(t, err)

		whole := uint64(len(magic + "four-head" + "four-tail"))
		logs, err := fetchLogsFromSource(t.Context(), newStagingDir(t), src.url, "binlog.000004", whole)
		require.NoError(t, err)
		require.Len(t, logs, 3)

		empty, err := os.ReadFile(logs[0])
		require.NoError(t, err)
		assert.Empty(t, empty, "the first log contributes nothing")

		target, _, err := updateRelayLogs(logs, "relay-bin.000002", relay.basename, relay.index)
		require.NoError(t, err)

		after, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"whole-five"+"whole-six", string(after))
		assert.Equal(t, 1, bytes.Count(after, binlogMagic))
	})

	t.Run("only the last log is missing", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)

		before, err := os.ReadFile(relay.target)
		require.NoError(t, err)

		logs, err := fetchLogsFromSource(t.Context(), newStagingDir(t), src.url,
			"binlog.000006", uint64(len(magic)))
		require.NoError(t, err)
		require.Len(t, logs, 1)

		target, _, err := updateRelayLogs(logs, "relay-bin.000002", relay.basename, relay.index)
		require.NoError(t, err)

		after, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"whole-six", string(after))
		assert.Equal(t, 1, bytes.Count(after, binlogMagic))
	})

	t.Run("an already caught-up replica splices nothing", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)

		before, err := os.ReadFile(relay.target)
		require.NoError(t, err)

		end := uint64(len(magic + "whole-six"))
		logs, err := fetchLogsFromSource(t.Context(), newStagingDir(t), src.url, "binlog.000006", end)
		require.NoError(t, err)
		require.Len(t, logs, 1)

		target, startPos, err := updateRelayLogs(logs, "relay-bin.000002", relay.basename, relay.index)

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000002", target, "the applier stays in the log it is already in")
		assert.Equal(t, uint64(0), startPos)

		after, err := os.ReadFile(relay.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		relay.assertUntouched(t)
	})

	t.Run("a caught-up replica whose relay logs were purged", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)
		writeIndex(t, relay.index, "./relay-bin.000003")

		end := uint64(len(magic + "whole-six"))
		logs, err := fetchLogsFromSource(t.Context(), newStagingDir(t), src.url, "binlog.000006", end)
		require.NoError(t, err)

		target, startPos, err := updateRelayLogs(logs, "relay-bin.000003", relay.basename, relay.index)

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000003", target)
		assert.Equal(t, uint64(0), startPos)
	})

	t.Run("a purged replica that is behind the source refuses", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)
		writeIndex(t, relay.index, "./relay-bin.000003")

		logs, err := fetchLogsFromSource(t.Context(), newStagingDir(t), src.url, "binlog.000004", src.position)
		require.NoError(t, err)

		_, _, err = updateRelayLogs(logs, "relay-bin.000003", relay.basename, relay.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "so no relay log is closed and none can be appended to")
	})

	t.Run("a source failing mid-stream splices nothing", func(t *testing.T) {
		src := newSourceLayout(t)
		relay := newRelayLayout(t)
		require.NoError(t, os.Remove(filepath.Join(src.dir, "binlog.000006")))

		before, err := os.ReadFile(relay.target)
		require.NoError(t, err)

		_, err = fetchLogsFromSource(t.Context(), newStagingDir(t), src.url, "binlog.000004", src.position)

		require.Error(t, err)
		after, err := os.ReadFile(relay.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		relay.assertUntouched(t)
	})

	refusals := []struct {
		name     string
		binlog   string
		position uint64
		wantErr  string
	}{
		{
			name:     "a position past the end of the source log",
			binlog:   "binlog.000004",
			position: 99999,
			wantErr:  "unexpected status: 400",
		},
		{
			name:     "the requested log was purged from the source",
			binlog:   "binlog.000001",
			position: 4,
			wantErr:  "unexpected status: 404",
		},
	}

	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			src := newSourceLayout(t)
			relay := newRelayLayout(t)
			staging := newStagingDir(t)

			_, err := fetchLogsFromSource(t.Context(), staging, src.url, tt.binlog, tt.position)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			leftover, err := os.ReadFile(filepath.Join(staging, "binlog.999999"))
			require.NoError(t, err)
			assert.Equal(t, "STALE", string(leftover))
			relay.assertUntouched(t)
		})
	}
}
