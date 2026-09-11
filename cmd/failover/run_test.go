package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
)

type fakeDatabase struct {
	*fakeStatuser

	ops []string

	positions    db.ReplicaPosition
	positionsErr error

	basename string
	index    string
	pathsErr error

	stopErr  error
	flushErr error
	startErr error
}

var _ database = (*fakeDatabase)(nil)

func (f *fakeDatabase) StopReplication(context.Context) error {
	f.ops = append(f.ops, "StopReplication")
	return f.stopErr
}

func (f *fakeDatabase) FlushRelayLogs(context.Context) error {
	f.ops = append(f.ops, "FlushRelayLogs")
	return f.flushErr
}

func (f *fakeDatabase) GetSourceLogPos(context.Context) (db.ReplicaPosition, error) {
	f.ops = append(f.ops, "GetSourceLogPos")
	return f.positions, f.positionsErr
}

func (f *fakeDatabase) RelayLogPaths(context.Context) (string, string, error) {
	f.ops = append(f.ops, "RelayLogPaths")
	return f.basename, f.index, f.pathsErr
}

func (f *fakeDatabase) StartSQLThread(context.Context) error {
	f.ops = append(f.ops, "StartSQLThread")
	return f.startErr
}

type jobFixture struct {
	src   sourceLayout
	relay relayLayout
	fake  *fakeDatabase
	cfg   failoverConfig
}

func newJobFixture(t *testing.T) *jobFixture {
	t.Helper()

	src := newSourceLayout(t)
	relay := newRelayLayout(t)

	fake := &fakeDatabase{
		fakeStatuser: &fakeStatuser{
			statuses: []map[string]string{applying("relay-bin.000002", 500, drainedState)},
		},
		positions: db.ReplicaPosition{
			SourceHost: "mysql-0.mysql",
			SourceLog:  "binlog.000004",
			SourcePos:  src.position,
			RelayLog:   "relay-bin.000002",
			RelayPos:   13,
		},
		basename: relay.basename,
		index:    relay.index,
	}

	return &jobFixture{
		src:   src,
		relay: relay,
		fake:  fake,
		cfg: failoverConfig{
			newDatabase:  func(context.Context) (database, error) { return fake, nil },
			sourceURL:    func(string) string { return src.url },
			source:       "mysql-1.mysql",
			wait:         true,
			stagingDir:   filepath.Join(t.TempDir(), "source-logs"),
			applyPoll:    time.Millisecond,
			applyTimeout: time.Second,
		},
	}
}

func TestRun(t *testing.T) {
	// The statement order matters
	wantOps := []string{
		"StopReplication",
		"FlushRelayLogs",
		"GetSourceLogPos",
		"RelayLogPaths",
		"StartSQLThread",
	}

	t.Run("stops replication, splices the missing logs and waits for the applier", func(t *testing.T) {
		j := newJobFixture(t)
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, wantOps, j.fake.ops)
		assert.GreaterOrEqual(t, j.fake.calls, 2, "the applier must be polled to a stable position")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"four-tail"+"whole-five"+"whole-six", string(after))
		j.relay.assertUntouched(t)
	})

	t.Run("-wait=false returns once the applier is started", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.wait = false
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, wantOps, j.fake.ops)
		assert.Zero(t, j.fake.calls, "the applier must not be polled")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"four-tail"+"whole-five"+"whole-six", string(after),
			"the splice must land even when the job does not wait for it")
	})

	t.Run("-wait=false ignores the wait timeout", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.wait = false
		j.cfg.applyTimeout = 0

		require.NoError(t, run(t.Context(), j.cfg))
	})

	t.Run("a caught-up replica succeeds without touching the relay logs", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.positions.SourceLog = "binlog.000006"
		j.fake.positions.SourcePos = uint64(len(magic + "whole-six"))
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, wantOps, j.fake.ops)
		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		j.relay.assertUntouched(t)
	})

	t.Run("a missing source host stops nothing", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.source = ""

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "-source is not set")
		assert.Empty(t, j.fake.ops, "no statement may be issued without the input")
	})

	t.Run("a non-positive wait timeout stops nothing", func(t *testing.T) {
		for _, timeout := range []time.Duration{0, -time.Second} {
			j := newJobFixture(t)
			j.cfg.applyTimeout = timeout

			err := run(t.Context(), j.cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "-wait-timeout must be positive")
			assert.Empty(t, j.fake.ops, "no statement may be issued without the input")
		}
	})

	t.Run("a missing staging dir stops nothing", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.stagingDir = "  "

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "-staging-dir is not set")
		assert.Empty(t, j.fake.ops, "no statement may be issued without the input")
	})

	t.Run("connecting to the database fails", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.newDatabase = func(context.Context) (database, error) {
			return nil, errors.New("dial tcp: connection refused")
		}

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "connect to database")
		assert.Empty(t, j.fake.ops)
	})

	failures := []struct {
		name    string
		breakIt func(t *testing.T, j *jobFixture)
		wantErr string
		wantOps []string
	}{
		{
			name:    "STOP REPLICA fails",
			breakIt: func(_ *testing.T, j *jobFixture) { j.fake.stopErr = errors.New("access denied") },
			wantErr: "stop replica",
			wantOps: []string{"StopReplication"},
		},
		{
			name:    "FLUSH RELAY LOGS fails",
			breakIt: func(_ *testing.T, j *jobFixture) { j.fake.flushErr = errors.New("access denied") },
			wantErr: "flush relay logs",
			wantOps: []string{"StopReplication", "FlushRelayLogs"},
		},
		{
			name:    "the replica position is unreadable",
			breakIt: func(_ *testing.T, j *jobFixture) { j.fake.positionsErr = errors.New("not a replica") },
			wantErr: "get replica positions",
			wantOps: []string{"StopReplication", "FlushRelayLogs", "GetSourceLogPos"},
		},
		{
			name:    "the relay log paths are unreadable",
			breakIt: func(_ *testing.T, j *jobFixture) { j.fake.pathsErr = errors.New("relay logging is off") },
			wantErr: "get relay log paths",
			wantOps: []string{"StopReplication", "FlushRelayLogs", "GetSourceLogPos", "RelayLogPaths"},
		},
		{
			name: "the source refuses to stream",
			breakIt: func(_ *testing.T, j *jobFixture) {
				j.fake.positions.SourceLog = "binlog.000001" // purged from the source
			},
			wantErr: "fetch logs from source",
			wantOps: []string{"StopReplication", "FlushRelayLogs", "GetSourceLogPos", "RelayLogPaths"},
		},
		{
			name: "there is nowhere to splice",
			breakIt: func(t *testing.T, j *jobFixture) {
				writeIndex(t, j.relay.index, "./relay-bin.000003")
			},
			wantErr: "update relay logs",
			wantOps: []string{"StopReplication", "FlushRelayLogs", "GetSourceLogPos", "RelayLogPaths"},
		},
	}

	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			j := newJobFixture(t)
			tt.breakIt(t, j)

			err := run(t.Context(), j.cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, tt.wantOps, j.fake.ops)
			assert.NotContains(t, j.fake.ops, "StartSQLThread")
		})
	}

	t.Run("START REPLICA SQL_THREAD fails", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.startErr = errors.New("access denied")

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start SQL_THREAD")
		assert.Equal(t, wantOps, j.fake.ops)
		assert.Equal(t, 0, j.fake.calls, "no point polling an applier that never started")
	})

	t.Run("the applier never drains the splice", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.statuses = []map[string]string{applying("relay-bin.000002", 4, drainedState)}
		j.cfg.applyTimeout = 50 * time.Millisecond

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "apply relay logs")
		assert.Equal(t, wantOps, j.fake.ops)
	})

	t.Run("the applier reports a failure", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.statuses = []map[string]string{{
			"Replica_SQL_Running": "Yes",
			"Last_SQL_Error":      "Error 1062: Duplicate entry",
		}}

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "apply relay logs")
		assert.Contains(t, err.Error(), "Duplicate entry")
	})
}

func TestParseFlags(t *testing.T) {
	origFlags, origArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = origFlags, origArgs })

	flag.CommandLine = flag.NewFlagSet(origArgs[0], flag.ContinueOnError)
	os.Args = []string{"failover", "-source", "mysql-1.mysql"}

	f := parseFlags()

	assert.Equal(t, "mysql-1.mysql", f.source)
	assert.True(t, f.wait, "the job waits for the applier unless told not to")
	assert.Equal(t, sourceLogsDir, f.stagingDir, "the staging dir defaults to the path in the container")
	assert.Equal(t, relayLogApplyTimeout, f.waitTimeout)
}

func TestProductionConfig(t *testing.T) {
	cfg := config(flags{source: "mysql-1.mysql", stagingDir: "/tmp/source-logs", wait: true, waitTimeout: 5 * time.Minute})

	require.NotNil(t, cfg.newDatabase)
	require.NotNil(t, cfg.sourceURL)
	assert.Equal(t, sourceStreamURL("mysql-1.mysql"), cfg.sourceURL("mysql-1.mysql"))
	assert.Equal(t, "mysql-1.mysql", cfg.source)
	assert.Equal(t, "/tmp/source-logs", cfg.stagingDir)
	assert.True(t, cfg.wait)
	assert.Equal(t, relayLogApplyPoll, cfg.applyPoll)
	assert.Equal(t, 5*time.Minute, cfg.applyTimeout)
	assert.Positive(t, cfg.applyPoll)
	assert.Greater(t, cfg.applyTimeout, cfg.applyPoll)
}
