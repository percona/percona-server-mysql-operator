package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

type fakeStorage struct {
	objects map[string]string // key -> content
	getErr  error
}

func (f *fakeStorage) GetObject(_ context.Context, objectName string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	content, ok := f.objects[objectName]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeStorage) PutObject(_ context.Context, _ string, _ io.Reader, _ int64) error { return nil }
func (f *fakeStorage) ListObjects(_ context.Context, _ string) ([]string, error)         { return nil, nil }
func (f *fakeStorage) DeleteObject(_ context.Context, _ string) error                    { return nil }
func (f *fakeStorage) SetPrefix(_ string)                                                {}
func (f *fakeStorage) GetPrefix() string                                                 { return "" }

// fakeDB records method calls and returns configured errors.
type fakeDB struct {
	changeRelayErr        error
	startUntilErr         error
	waitErr               error
	stopErr               error
	resetErr              error
	setGTIDNextErr        error
	getGTIDExecutedErr    error
	getGTIDExecutedResult string
	calls                 []string
	startUntilGTID        string
	channels              []string
}

func (f *fakeDB) ChangeReplicationSourceRelay(_ context.Context, _ string, _ int, channel string) error {
	f.calls = append(f.calls, "ChangeReplicationSourceRelay")
	f.channels = append(f.channels, channel)
	return f.changeRelayErr
}

func (f *fakeDB) StartReplicaUntilGTID(_ context.Context, gtid string, channel string) error {
	f.calls = append(f.calls, "StartReplicaUntilGTID")
	f.startUntilGTID = gtid
	f.channels = append(f.channels, channel)
	return f.startUntilErr
}

func (f *fakeDB) WaitReplicaSQLThreadStop(_ context.Context, _ time.Duration, channel string) error {
	f.calls = append(f.calls, "WaitReplicaSQLThreadStop")
	f.channels = append(f.channels, channel)
	return f.waitErr
}

func (f *fakeDB) StopReplication(_ context.Context, channel string) error {
	f.calls = append(f.calls, "StopReplication")
	f.channels = append(f.channels, channel)
	return f.stopErr
}

func (f *fakeDB) ResetReplication(_ context.Context, channel string) error {
	f.calls = append(f.calls, "ResetReplication")
	f.channels = append(f.channels, channel)
	return f.resetErr
}

func (f *fakeDB) SetGTIDNextAutomatic(_ context.Context) error {
	f.calls = append(f.calls, "SetGTIDNextAutomatic")
	return f.setGTIDNextErr
}

func (f *fakeDB) GetGTIDExecuted(_ context.Context) (string, error) {
	f.calls = append(f.calls, "GetGTIDExecuted")
	return f.getGTIDExecutedResult, f.getGTIDExecutedErr
}

func (f *fakeDB) Close() error { return nil }

func writeBinlogsFile(t *testing.T, entries []binlogserver.BinlogEntry) string {
	t.Helper()
	data, err := json.Marshal(entries)
	require.NoError(t, err)
	f, err := os.CreateTemp(t.TempDir(), "binlogs-*.json")
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

func TestRunApply(t *testing.T) {
	bucket := "mybucket"
	defaultEntries := []binlogserver.BinlogEntry{
		{URI: "s3://mybucket/binlogs/binlog.000001"},
		{URI: "s3://mybucket/binlogs/binlog.000002"},
	}
	defaultS3 := func(fake *fakeStorage) newStorageFn {
		fake.objects = map[string]string{
			"binlogs/binlog.000001": "binlogdata1",
			"binlogs/binlog.000002": "binlogdata2",
		}
		return func(_ context.Context, _, _, _, _, _, _ string, _ bool) (storage.Storage, error) {
			return fake, nil
		}
	}
	allDBCalls := []string{
		"GetGTIDExecuted",
		"ChangeReplicationSourceRelay",
		"StartReplicaUntilGTID",
		"WaitReplicaSQLThreadStop",
		"StopReplication",
		"ResetReplication",
		"GetGTIDExecuted",
		"SetGTIDNextAutomatic",
	}

	tests := map[string]struct {
		entries           []binlogserver.BinlogEntry
		rawContent        string
		omitBinlogsPath   bool
		pitrType          string
		pitrGTID          string
		pitrDate          string
		db                *fakeDB
		newDB             func(ctx context.Context, params db.DBParams) (Database, error)
		newS3             func(*fakeStorage) newStorageFn
		getSecret         func(apiv1.SystemUser) (string, error)
		getGTID           func(string, string) (string, error)
		expectedError     string
		expectedFuncCalls []string
		expectedUDID      string
		checkRelayLogs    bool
	}{
		"missing BINLOGS_PATH": {
			omitBinlogsPath: true,
			expectedError:   "BINLOGS_PATH",
		},
		"invalid JSON in binlogs file": {
			rawContent:    "not-json",
			expectedError: "parse binlogs json",
		},
		"empty binlog entries": {
			entries:       []binlogserver.BinlogEntry{},
			expectedError: "no binlog entries found",
		},
		"get secret error": {
			entries:       defaultEntries,
			pitrType:      "gtid",
			pitrGTID:      "uuid:1",
			getSecret:     func(apiv1.SystemUser) (string, error) { return "", errors.New("secret not found") },
			expectedError: "get operator password",
		},
		"DB connect error": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "uuid:1",
			newDB: func(_ context.Context, _ db.DBParams) (Database, error) {
				return nil, errors.New("connection refused")
			},
			expectedError: "connect to MySQL",
		},
		"GetGTIDExecuted error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{getGTIDExecutedErr: errors.New("query failed")},
			expectedError:     "get current GTID_EXECUTED",
			expectedFuncCalls: []string{"GetGTIDExecuted"},
		},
		"change replication source relay error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{changeRelayErr: errors.New("relay error")},
			expectedError:     "change replication source",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay"},
		},
		"S3 client creation error": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "uuid:1",
			db:       &fakeDB{},
			newS3: func(_ *fakeStorage) newStorageFn {
				return func(_ context.Context, _, _, _, _, _, _ string, _ bool) (storage.Storage, error) {
					return nil, errors.New("s3 unavailable")
				}
			},
			expectedError:     "create S3 client",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay"},
		},
		"S3 download error": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "uuid:1",
			db:       &fakeDB{},
			newS3: func(fake *fakeStorage) newStorageFn {
				fake.getErr = errors.New("download failed")
				return func(_ context.Context, _, _, _, _, _, _ string, _ bool) (storage.Storage, error) {
					return fake, nil
				}
			},
			expectedError:     "download binlog",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay"},
		},
		"start replica until GTID error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{startUntilErr: errors.New("start error")},
			expectedError:     "start replica until GTID",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay", "StartReplicaUntilGTID"},
		},
		"wait replica stop error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{waitErr: errors.New("wait error")},
			expectedError:     "wait for replication",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay", "StartReplicaUntilGTID", "WaitReplicaSQLThreadStop"},
		},
		"stop replication error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{stopErr: errors.New("stop error")},
			expectedError:     "stop replication",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay", "StartReplicaUntilGTID", "WaitReplicaSQLThreadStop", "StopReplication"},
		},
		"reset replication error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{resetErr: errors.New("reset error")},
			expectedError:     "reset replication",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay", "StartReplicaUntilGTID", "WaitReplicaSQLThreadStop", "StopReplication", "ResetReplication"},
		},
		"set GTID_NEXT error": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "uuid:1",
			db:                &fakeDB{setGTIDNextErr: errors.New("gtid error")},
			expectedError:     "set GTID_NEXT to AUTOMATIC",
			expectedFuncCalls: allDBCalls,
		},
		"GTID mode success": {
			entries:           defaultEntries,
			pitrType:          "gtid",
			pitrGTID:          "aaaaaaaa-0000-0000-0000-000000000001:1-10",
			db:                &fakeDB{},
			expectedFuncCalls: allDBCalls,
			expectedUDID:      "aaaaaaaa-0000-0000-0000-000000000001:1-10",
			checkRelayLogs:    true,
		},
		"date mode success": {
			entries:  defaultEntries,
			pitrType: "date",
			pitrDate: "2024-01-15 12:00:00",
			db:       &fakeDB{},
			getGTID: func(_ string, datetime string) (string, error) {
				assert.Equal(t, "2024-01-15 12:00:00", datetime)
				return "bbbbbbbb-0000-0000-0000-000000000002:1-5", nil
			},
			expectedFuncCalls: allDBCalls,
			expectedUDID:      "bbbbbbbb-0000-0000-0000-000000000002:1-5",
			checkRelayLogs:    true,
		},
		"date mode getGTID error": {
			entries:           defaultEntries,
			pitrType:          "date",
			pitrDate:          "2024-01-15 12:00:00",
			db:                &fakeDB{},
			getGTID:           func(string, string) (string, error) { return "", errors.New("mysqlbinlog failed") },
			expectedError:     "get latest GTID for date",
			expectedFuncCalls: []string{"GetGTIDExecuted", "ChangeReplicationSourceRelay"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var binlogsPath string
			if tc.rawContent != "" {
				f, err := os.CreateTemp(t.TempDir(), "binlogs-*.json")
				require.NoError(t, err)
				_, err = f.WriteString(tc.rawContent)
				require.NoError(t, err)
				require.NoError(t, f.Close())
				binlogsPath = f.Name()
			} else if tc.entries != nil {
				binlogsPath = writeBinlogsFile(t, tc.entries)
			}

			if tc.omitBinlogsPath {
				t.Setenv("BINLOGS_PATH", "")
			} else {
				t.Setenv("BINLOGS_PATH", binlogsPath)
			}
			t.Setenv("PITR_TYPE", tc.pitrType)
			t.Setenv("PITR_GTID", tc.pitrGTID)
			t.Setenv("PITR_DATE", tc.pitrDate)
			t.Setenv("S3_BUCKET", bucket)

			fakeDatabase := tc.db

			newDB := tc.newDB
			if newDB == nil {
				newDB = func(_ context.Context, _ db.DBParams) (Database, error) {
					return fakeDatabase, nil
				}
			}

			getSecret := tc.getSecret
			if getSecret == nil {
				getSecret = func(apiv1.SystemUser) (string, error) { return "testpass", nil }
			}

			getGTID := tc.getGTID
			if getGTID == nil {
				getGTID = func(string, string) (string, error) {
					t.Fatal("getGTIDByDatetime should not be called")
					return "", nil
				}
			}

			fake := &fakeStorage{}
			var newS3 newStorageFn
			if tc.newS3 != nil {
				newS3 = tc.newS3(fake)
			} else {
				newS3 = defaultS3(fake)
			}

			mysqlDir := t.TempDir()
			err := runApply(t.Context(), newS3, newDB, getSecret, getGTID, mysqlDir)

			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
			} else {
				require.NoError(t, err)
			}

			if tc.expectedFuncCalls != nil && fakeDatabase != nil {
				assert.Equal(t, tc.expectedFuncCalls, fakeDatabase.calls)
				for i, ch := range fakeDatabase.channels {
					assert.Equalf(t, "pitr", ch, "channel-aware call #%d should target the pitr channel", i)
				}
			}
			if tc.expectedUDID != "" && fakeDatabase != nil {
				assert.Equal(t, tc.expectedUDID, fakeDatabase.startUntilGTID)
			}
			if tc.checkRelayLogs {
				hostname, err := os.Hostname()
				require.NoError(t, err)

				indexPath := filepath.Join(mysqlDir, hostname+"-relay-bin.index")
				indexData, err := os.ReadFile(indexPath)
				require.NoError(t, err, "relay log index file must exist")

				indexContent := string(indexData)
				assert.Contains(t, indexContent, hostname+"-relay-bin.000001")
				assert.Contains(t, indexContent, hostname+"-relay-bin.000002")

				for i, wantContent := range []string{"binlogdata1", "binlogdata2"} {
					relayLog := filepath.Join(mysqlDir, fmt.Sprintf("%s-relay-bin.%06d", hostname, i+1))
					data, err := os.ReadFile(relayLog)
					require.NoErrorf(t, err, "relay log %d must exist", i+1)
					assert.Equalf(t, wantContent, string(data), "relay log %d content mismatch", i+1)
				}
			}
		})
	}
}

type applyCall struct {
	objectKeys      []string
	mysqlbinlogArgs []string
	mysqlArgs       []string
}

func TestRunReplay(t *testing.T) {
	bucket := "mybucket"

	defaultEntries := []binlogserver.BinlogEntry{
		{URI: "s3://mybucket/binlogs/binlog.000001"},
		{URI: "s3://mybucket/binlogs/binlog.000002"},
	}

	defaultS3 := func(fake *fakeStorage) newStorageFn {
		fake.objects = map[string]string{
			"binlogs/binlog.000001": "binlogdata1",
			"binlogs/binlog.000002": "binlogdata2",
		}
		return func(_ context.Context, _, _, _, _, _, _ string, _ bool) (storage.Storage, error) {
			return fake, nil
		}
	}

	tests := map[string]struct {
		entries       []binlogserver.BinlogEntry
		rawContent    string
		pitrType      string
		pitrGTID      string
		pitrDate      string
		pitrForce     string
		db            *fakeDB
		newDB         func(ctx context.Context, params db.DBParams) (Database, error)
		newS3         func(*fakeStorage) newStorageFn
		getSecret     func(apiv1.SystemUser) (string, error)
		applyErr      error
		expectedError string
		checkApply    func(t *testing.T, call applyCall)
	}{
		"missing BINLOGS_PATH": {
			expectedError: "BINLOGS_PATH",
		},
		"invalid JSON in binlogs file": {
			rawContent:    "not-json",
			expectedError: "parse binlogs json",
		},
		"empty binlog entries": {
			entries:       []binlogserver.BinlogEntry{},
			expectedError: "no binlog entries found",
		},
		"get secret error": {
			entries:       defaultEntries,
			pitrType:      "gtid",
			pitrGTID:      "uuid:1",
			getSecret:     func(apiv1.SystemUser) (string, error) { return "", errors.New("secret not found") },
			expectedError: "get operator password",
		},
		"DB connect error": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "uuid:1",
			newDB: func(_ context.Context, _ db.DBParams) (Database, error) {
				return nil, errors.New("connection refused")
			},
			expectedError: "connect to MySQL",
		},
		"GetGTIDExecuted error": {
			entries:       defaultEntries,
			pitrType:      "gtid",
			pitrGTID:      "uuid:1",
			db:            &fakeDB{getGTIDExecutedErr: errors.New("query failed")},
			expectedError: "get GTID_EXECUTED",
		},
		"S3 client creation error": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "uuid:1",
			db:       &fakeDB{getGTIDExecutedResult: "uuid:1-5"},
			newS3: func(_ *fakeStorage) newStorageFn {
				return func(_ context.Context, _, _, _, _, _, _ string, _ bool) (storage.Storage, error) {
					return nil, errors.New("s3 unavailable")
				}
			},
			expectedError: "create S3 client",
		},
		"S3 download error": {
			entries:       defaultEntries,
			pitrType:      "gtid",
			pitrGTID:      "uuid:1",
			db:            &fakeDB{getGTIDExecutedResult: "uuid:1-5"},
			applyErr:      errors.New("fetch binlog binlogs/binlog.000001: download failed"),
			expectedError: "apply binlogs",
		},
		"unknown PITR type": {
			entries:       defaultEntries,
			pitrType:      "unknown",
			db:            &fakeDB{getGTIDExecutedResult: "uuid:1-5"},
			expectedError: "unknown PITR_TYPE",
		},
		"apply error": {
			entries:       defaultEntries,
			pitrType:      "gtid",
			pitrGTID:      "uuid:1-10",
			db:            &fakeDB{getGTIDExecutedResult: "uuid:1-5"},
			applyErr:      errors.New("mysql failed"),
			expectedError: "apply binlogs",
		},
		"GTID mode success": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "aaaaaaaa-0000-0000-0000-000000000001:1-10",
			db:       &fakeDB{getGTIDExecutedResult: "aaaaaaaa-0000-0000-0000-000000000001:1-5"},
			checkApply: func(t *testing.T, call applyCall) {
				assert.Len(t, call.objectKeys, 2)
				assert.Contains(t, call.mysqlbinlogArgs, "--disable-log-bin")
				assert.Contains(t, call.mysqlbinlogArgs, "--exclude-gtids=aaaaaaaa-0000-0000-0000-000000000001:1-5")
				assert.Contains(t, call.mysqlbinlogArgs, "--include-gtids=aaaaaaaa-0000-0000-0000-000000000001:1-10")
				assert.NotContains(t, call.mysqlbinlogArgs, "--stop-datetime")
				assert.NotContains(t, call.mysqlArgs, "--force")
			},
		},
		"GTID mode with force": {
			entries:   defaultEntries,
			pitrType:  "gtid",
			pitrGTID:  "aaaaaaaa-0000-0000-0000-000000000001:1-10",
			pitrForce: "true",
			db:        &fakeDB{getGTIDExecutedResult: "aaaaaaaa-0000-0000-0000-000000000001:1-5"},
			checkApply: func(t *testing.T, call applyCall) {
				assert.Contains(t, call.mysqlArgs, "--force")
			},
		},
		"date mode success": {
			entries:  defaultEntries,
			pitrType: "date",
			pitrDate: "2024-01-15 12:00:00",
			db:       &fakeDB{getGTIDExecutedResult: "bbbbbbbb-0000-0000-0000-000000000002:1-5"},
			checkApply: func(t *testing.T, call applyCall) {
				assert.Len(t, call.objectKeys, 2)
				assert.Contains(t, call.mysqlbinlogArgs, "--disable-log-bin")
				assert.Contains(t, call.mysqlbinlogArgs, "--exclude-gtids=bbbbbbbb-0000-0000-0000-000000000002:1-5")
				assert.Contains(t, call.mysqlbinlogArgs, "--stop-datetime=2024-01-15 12:00:00")
				for _, arg := range call.mysqlbinlogArgs {
					assert.False(t, strings.HasPrefix(arg, "--include-gtids"), "date mode should not have --include-gtids")
				}
				assert.NotContains(t, call.mysqlArgs, "--force")
			},
		},
		"date mode with force": {
			entries:   defaultEntries,
			pitrType:  "date",
			pitrDate:  "2024-01-15 12:00:00",
			pitrForce: "true",
			db:        &fakeDB{getGTIDExecutedResult: "bbbbbbbb-0000-0000-0000-000000000002:1-5"},
			checkApply: func(t *testing.T, call applyCall) {
				assert.Contains(t, call.mysqlArgs, "--force")
			},
		},
		"empty GTID_EXECUTED": {
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "aaaaaaaa-0000-0000-0000-000000000001:1-10",
			db:       &fakeDB{getGTIDExecutedResult: ""},
			checkApply: func(t *testing.T, call applyCall) {
				assert.Contains(t, call.mysqlbinlogArgs, "--disable-log-bin")
				assert.Contains(t, call.mysqlbinlogArgs, "--include-gtids=aaaaaaaa-0000-0000-0000-000000000001:1-10")
				for _, arg := range call.mysqlbinlogArgs {
					assert.False(t, strings.HasPrefix(arg, "--exclude-gtids"), "should not have --exclude-gtids when GTID_EXECUTED is empty")
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var binlogsPath string
			if tc.rawContent != "" {
				f, err := os.CreateTemp(t.TempDir(), "binlogs-*.json")
				require.NoError(t, err)
				_, err = f.WriteString(tc.rawContent)
				require.NoError(t, err)
				require.NoError(t, f.Close())
				binlogsPath = f.Name()
			} else if tc.entries != nil {
				binlogsPath = writeBinlogsFile(t, tc.entries)
			}

			if binlogsPath != "" {
				t.Setenv("BINLOGS_PATH", binlogsPath)
			} else {
				t.Setenv("BINLOGS_PATH", "")
			}
			t.Setenv("PITR_TYPE", tc.pitrType)
			t.Setenv("PITR_GTID", tc.pitrGTID)
			t.Setenv("PITR_DATE", tc.pitrDate)
			t.Setenv("PITR_FORCE", tc.pitrForce)
			t.Setenv("S3_BUCKET", bucket)

			fakeDatabase := tc.db

			newDB := tc.newDB
			if newDB == nil {
				newDB = func(_ context.Context, _ db.DBParams) (Database, error) {
					return fakeDatabase, nil
				}
			}

			getSecret := tc.getSecret
			if getSecret == nil {
				getSecret = func(apiv1.SystemUser) (string, error) { return "testpass", nil }
			}

			fake := &fakeStorage{}
			var newS3 newStorageFn
			if tc.newS3 != nil {
				newS3 = tc.newS3(fake)
			} else {
				newS3 = defaultS3(fake)
			}

			var captured applyCall
			apply := func(_ context.Context, objectKeys []string, _ getObjectFn, mysqlbinlogArgs []string, mysqlArgs []string, _ string) error {
				captured = applyCall{
					objectKeys:      objectKeys,
					mysqlbinlogArgs: mysqlbinlogArgs,
					mysqlArgs:       mysqlArgs,
				}
				return tc.applyErr
			}

			err := runReplay(t.Context(), newS3, newDB, getSecret, apply)

			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
				return
			}
			require.NoError(t, err)
			if tc.checkApply != nil {
				tc.checkApply(t, captured)
			}
		})
	}
}
