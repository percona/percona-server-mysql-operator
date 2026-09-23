package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type fakeDatabase struct {
	*fakeStatuser

	ops []string

	positions    db.ReplicaPosition
	positionsErr error

	basename string
	index    string
	pathsErr error

	stopErr   error
	resetErr  error
	flushErr  error
	startErr  error
	ioErr     error
	gtidErr   error
	gtidMu    sync.Mutex
	gtid      string
	gtidAhead string

	closed   int
	closeErr error
}

var _ database = (*fakeDatabase)(nil)

func (f *fakeDatabase) StopReplication(context.Context) error {
	f.ops = append(f.ops, "StopReplication")
	return f.stopErr
}

func (f *fakeDatabase) ResetReplication(context.Context) error {
	f.ops = append(f.ops, "ResetReplication")
	return f.resetErr
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

// Close stays out of ops: run closes the connection on every path, so recording
// it would say nothing about the statement order the other ops pin down.
func (f *fakeDatabase) Close() error {
	f.closed++
	return f.closeErr
}

func (f *fakeDatabase) StartIOThread(context.Context) error {
	f.ops = append(f.ops, "StartIOThread")
	return f.ioErr
}

// The GTID reads run on the source watch's goroutine, so they stay off ops.
func (f *fakeDatabase) GetGTIDExecuted(context.Context) (string, error) {
	f.gtidMu.Lock()
	defer f.gtidMu.Unlock()

	return f.gtid, f.gtidErr
}

func (f *fakeDatabase) GTIDSubtract(_ context.Context, _, _ string) (string, error) {
	f.gtidMu.Lock()
	defer f.gtidMu.Unlock()

	return f.gtidAhead, f.gtidErr
}

type jobFixture struct {
	src    sourceLayout
	relay  relayLayout
	fake   *fakeDatabase
	source *fakeSourceConn
	cfg    failoverConfig
}

func newJobFixture(t *testing.T) *jobFixture {
	t.Helper()

	src := newSourceLayout(t)
	relay := newRelayLayout(t)
	// The source stays dead unless a test brings it back, which is what every
	// case written before the stand-down existed assumes.
	source := &fakeSourceConn{}

	// run's pre-check reads the source host straight off the status map.
	status := applying("relay-bin.000002", 500, drainedState)
	status["Source_Host"] = "mysql-0.mysql"

	fake := &fakeDatabase{
		fakeStatuser: &fakeStatuser{
			statuses: []map[string]string{status},
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
		src:    src,
		relay:  relay,
		fake:   fake,
		source: source,
		cfg: failoverConfig{
			newDatabase:   func(context.Context) (database, error) { return fake, nil },
			newSourceDB:   source.connect,
			sourceURL:     func(string) string { return src.url },
			source:        "mysql-1.mysql",
			wait:          true,
			stagingDir:    filepath.Join(t.TempDir(), "source-logs"),
			logDir:        t.TempDir(),
			lockPath:      filepath.Join(t.TempDir(), "failover.lock"),
			applyPoll:     time.Millisecond,
			fetchTimeout:  testFetchTimeout,
			sourcePoll:    time.Millisecond,
			sourceTimeout: time.Second,
			receiverWait:  50 * time.Millisecond,
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
		assert.Equal(t, 1, j.fake.closed, "the connection must be closed")
		assert.GreaterOrEqual(t, j.fake.calls, 2, "the applier must be polled to a stable position")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+binlogEvent("four-tail")+binlogEvent("whole-five")+binlogEvent("whole-six"), string(after))
		j.relay.assertUntouched(t)
	})

	t.Run("an instance with no replication channel is left alone", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.err = sql.ErrNoRows
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Empty(t, j.fake.ops, "replication must not be touched")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		j.relay.assertUntouched(t)

		released, err := failover.Lock(j.cfg.lockPath)
		require.NoError(t, err, "the early exit must release the splice lock")
		t.Cleanup(func() { released.Close() }) //nolint:errcheck
	})

	t.Run("an unreadable status stops the splice", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.err = errors.New("connection lost")

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "show replica status")
		assert.Empty(t, j.fake.ops, "replication must not be touched when the status is unreadable")
	})

	t.Run("-wait=false returns once the applier is started", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.wait = false
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, wantOps, j.fake.ops)
		assert.Equal(t, 1, j.fake.calls, "only the pre-check reads the status; the applier must not be polled")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+binlogEvent("four-tail")+binlogEvent("whole-five")+binlogEvent("whole-six"), string(after),
			"the splice must land even when the job does not wait for it")
	})

	t.Run("-wait=false returns as soon as the applier is started", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.wait = false

		require.NoError(t, run(t.Context(), j.cfg))
	})

	t.Run("a caught-up replica succeeds without touching the relay logs", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.positions.SourceLog = "binlog.000006"
		j.fake.positions.SourcePos = uint64(len(magic + binlogEvent("whole-six")))
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

	t.Run("a non-positive fetch timeout stops nothing", func(t *testing.T) {
		for _, timeout := range []time.Duration{0, -time.Second} {
			j := newJobFixture(t)
			j.cfg.fetchTimeout = timeout

			err := run(t.Context(), j.cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "-fetch-timeout must be positive")
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

	t.Run("a staging dir the job does not own stops nothing", func(t *testing.T) {
		j := newJobFixture(t)
		staging := t.TempDir()
		writeFile(t, staging, "ibdata1", []byte("DATA"))
		j.cfg.stagingDir = staging

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "it is not a staging directory this job may wipe")
		assert.Empty(t, j.fake.ops, "no statement may be issued without the input")
		assert.FileExists(t, filepath.Join(staging, "ibdata1"), "nothing may be deleted")
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
			assert.Contains(t, jobLog(t, j.cfg.logDir), "ERROR: "+tt.wantErr, "the error must reach the log file")
		})
	}

	t.Run("a partial event at the end of the newest log is dropped", func(t *testing.T) {
		j := newJobFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(j.src.dir, "binlog.000006"),
			[]byte(magic+binlogEvent("whole-six")+tornEvent("half-six")), 0o644))
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		require.NoError(t, run(t.Context(), j.cfg))

		assert.Equal(t, wantOps, j.fake.ops)

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+binlogEvent("four-tail")+binlogEvent("whole-five")+binlogEvent("whole-six"),
			string(after), "everything up to the last whole event must still be spliced")
		j.relay.assertUntouched(t)
	})

	t.Run("a partial event in an earlier log stops the splice", func(t *testing.T) {
		j := newJobFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(j.src.dir, "binlog.000005"),
			[]byte(magic+binlogEvent("whole-five")+tornEvent("half-five")), 0o644))
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		err = run(t.Context(), j.cfg)

		require.ErrorIs(t, err, errTornBinlog)
		assert.NotContains(t, j.fake.ops, "StartSQLThread")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, before, after, "a gap in the middle of the stream may not reach the relay log")
		j.relay.assertUntouched(t)
	})

	t.Run("START REPLICA SQL_THREAD fails", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.startErr = errors.New("access denied")

		err := run(t.Context(), j.cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start SQL_THREAD")
		assert.Equal(t, wantOps, j.fake.ops)
		assert.Equal(t, 1, j.fake.calls, "only the pre-check; no point polling an applier that never started")
	})

	t.Run("an applier that never drains the splice stops the promotion", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.statuses = []map[string]string{applying("relay-bin.000002", 4, drainedState)}
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		// The drain has no budget of its own: it runs until the job's deadline.
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		err = run(ctx, j.cfg)

		require.ErrorIs(t, err, errRelayApplyTimeout,
			"a candidate that never caught up must not be promoted")
		assert.Equal(t, wantOps, j.fake.ops)

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.NotEqual(t, before, after, "giving up on the wait must not undo the splice")
	})

	t.Run("a job torn down mid-wait does not report success", func(t *testing.T) {
		j := newJobFixture(t)
		j.fake.statuses = []map[string]string{applying("relay-bin.000002", 4, busyState)}

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		// Call 1 is run's own pre-check, so cancel once the wait loop is polling.
		j.fake.onCall = func(calls int) {
			if calls >= 3 {
				cancel()
			}
		}

		err := run(ctx, j.cfg)

		require.Error(t, err, "a cancelled job must not exit 0")
		assert.Contains(t, err.Error(), "apply relay logs")
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("a splice already in progress stops this one", func(t *testing.T) {
		j := newJobFixture(t)
		held, err := failover.Lock(j.cfg.lockPath)
		require.NoError(t, err)
		t.Cleanup(func() { held.Close() }) //nolint:errcheck

		err = run(t.Context(), j.cfg)

		require.ErrorIs(t, err, failover.ErrLocked)
		assert.Empty(t, j.fake.ops, "replication must not be touched by a losing run")
		j.relay.assertUntouched(t)
	})

	t.Run("a source that is already back stops the failover before it starts", func(t *testing.T) {
		j := newJobFixture(t)
		j.source.up = []bool{true}
		j.fake.statuses[0]["Replica_IO_Running"] = "Yes"
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		err = run(t.Context(), j.cfg)

		require.ErrorIs(t, err, errSourceRecovered)
		assert.Empty(t, j.fake.ops, "replication must not be touched when there is nothing to fail over")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		j.relay.assertUntouched(t)

		released, err := failover.Lock(j.cfg.lockPath)
		require.NoError(t, err, "standing down must release the splice lock")
		t.Cleanup(func() { released.Close() })
	})

	t.Run("a probe stands down when the source is back", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.probe = true
		j.source.up = []bool{true}
		j.fake.statuses[0]["Replica_IO_Running"] = "Yes"

		err := run(t.Context(), j.cfg)

		require.ErrorIs(t, err, errSourceRecovered)
		assert.Empty(t, j.fake.ops)
	})

	t.Run("a probe touches nothing when the source is still dead", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.probe = true

		err := run(t.Context(), j.cfg)

		require.NoError(t, err)
		assert.Empty(t, j.fake.ops, "a probe must not stop replication")
		j.relay.assertUntouched(t)
	})

	t.Run("a probe after a completed splice hands the replica back", func(t *testing.T) {
		j := newJobFixture(t)
		j.cfg.probe = true
		j.source.up = []bool{true}
		// What a splice leaves behind: the applier drained, the receiver down.
		j.fake.statuses[0]["Replica_IO_Running"] = "No"

		err := run(t.Context(), j.cfg)

		require.ErrorIs(t, err, errSourceRecovered)
		assert.Equal(t, []string{"StartIOThread"}, j.fake.ops,
			"the receiver the splice left down has to be started again")
		j.relay.assertUntouched(t)
	})

	t.Run("a receiver a failed attempt left down is started again", func(t *testing.T) {
		j := newJobFixture(t)
		j.source.up = []bool{true}
		j.fake.statuses[0]["Replica_IO_Running"] = "No"

		err := run(t.Context(), j.cfg)

		require.ErrorIs(t, err, errSourceRecovered)
		assert.Equal(t, []string{"StartIOThread"}, j.fake.ops,
			"the replica may not be left stopped just because we are standing down")
	})

	t.Run("a source that comes back mid-drain gets the replica handed back", func(t *testing.T) {
		j := newJobFixture(t)
		// The pre-check dial finds it still dead; every dial after that answers.
		j.source.up = []bool{false, true}
		j.fake.statuses = []map[string]string{{
			"Replica_SQL_Running":       "Yes",
			"Replica_SQL_Running_State": busyState,
			"Relay_Log_File":            "relay-bin.000002",
			"Relay_Log_Pos":             "4",
			"Replica_IO_Running":        "Yes",
			"Source_Host":               "mysql-0.mysql",
		}}
		before, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)

		err = run(t.Context(), j.cfg)

		require.ErrorIs(t, err, errSourceRecovered, "an applier that never drained must not be promoted")
		assert.Equal(t, append(slices.Clone(wantOps), "StartIOThread"), j.fake.ops,
			"the stand-down has to happen after the splice, not instead of it")

		after, err := os.ReadFile(j.relay.target)
		require.NoError(t, err)
		assert.NotEqual(t, before, after, "the splice stays for the applier to work through")
	})

	t.Run("a source missing transactions we hold is promoted past", func(t *testing.T) {
		j := newJobFixture(t)
		j.source.up = []bool{true}
		j.fake.gtidAhead = "b7b097e0-1111-1111-1111-111111111111:50147-123853"

		require.NoError(t, run(t.Context(), j.cfg),
			"only a promotion keeps transactions the source lost")

		assert.Equal(t, wantOps, j.fake.ops)

		dials, _ := j.source.count()
		assert.Positive(t, dials, "the source has to be probed for this to mean anything")
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
	assert.False(t, f.probe, "the job fetches unless told only to look")
	assert.Equal(t, sourceLogsDir, f.stagingDir, "the staging dir defaults to the path in the container")
	assert.Equal(t, sourceFetchTimeout, f.fetchTimeout)
	assert.Equal(t, jobTimeout, f.timeout, "the job bounds the fetch and the drain together")
}

func TestProductionConfig(t *testing.T) {
	cfg := config(flags{source: "mysql-1.mysql", stagingDir: "/tmp/source-logs", wait: true, probe: true,
		fetchTimeout: 2 * time.Minute, timeout: 5 * time.Minute})

	require.NotNil(t, cfg.newDatabase)
	require.NotNil(t, cfg.sourceURL)
	assert.Equal(t, sourceStreamURL("mysql-1.mysql"), cfg.sourceURL("mysql-1.mysql"))
	assert.Equal(t, "mysql-1.mysql", cfg.source)
	assert.Equal(t, "/tmp/source-logs", cfg.stagingDir)
	assert.Equal(t, mysql.DataMountPath, cfg.logDir)
	assert.Equal(t, failover.LockPath, cfg.lockPath)
	assert.True(t, cfg.wait)
	assert.True(t, cfg.probe)
	require.NotNil(t, cfg.newSourceDB)
	assert.Equal(t, sourceProbePoll, cfg.sourcePoll)
	assert.Equal(t, sourceProbeTimeout, cfg.sourceTimeout)
	assert.Equal(t, receiverWait, cfg.receiverWait)
	assert.Positive(t, cfg.sourcePoll)
	assert.Equal(t, relayLogApplyPoll, cfg.applyPoll)
	assert.Equal(t, 2*time.Minute, cfg.fetchTimeout)
	assert.Positive(t, cfg.applyPoll)
}

// jobLog returns the log file the one run in dir wrote.
func jobLog(t *testing.T, dir string) string {
	t.Helper()

	logs, err := filepath.Glob(filepath.Join(dir, "failover.*.log"))
	require.NoError(t, err)
	require.Len(t, logs, 1)

	b, err := os.ReadFile(logs[0])
	require.NoError(t, err)

	return string(b)
}
