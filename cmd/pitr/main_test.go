package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

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

type fakeDB struct {
	getGTIDExecutedResult string
	getGTIDExecutedErr    error
	calls                 []string
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

type applyCall struct {
	binlogPaths     []string
	mysqlbinlogArgs []string
	mysqlArgs       []string
}

func TestRun(t *testing.T) {
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
			entries:  defaultEntries,
			pitrType: "gtid",
			pitrGTID: "uuid:1",
			db:       &fakeDB{getGTIDExecutedResult: "uuid:1-5"},
			newS3: func(fake *fakeStorage) newStorageFn {
				fake.getErr = errors.New("download failed")
				return func(_ context.Context, _, _, _, _, _, _ string, _ bool) (storage.Storage, error) {
					return fake, nil
				}
			},
			expectedError: "download binlog",
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
				assert.Len(t, call.binlogPaths, 2)
				assert.Contains(t, call.mysqlbinlogArgs, "--disable-log-bin")
				assert.Contains(t, call.mysqlbinlogArgs, "--exclude-gtids=aaaaaaaa-0000-0000-0000-000000000001:1-5")
				assert.Contains(t, call.mysqlbinlogArgs, "--include-gtids=aaaaaaaa-0000-0000-0000-000000000001:1-10")
				assert.NotContains(t, call.mysqlbinlogArgs, "--stop-datetime")
			},
		},
		"date mode success": {
			entries:  defaultEntries,
			pitrType: "date",
			pitrDate: "2024-01-15 12:00:00",
			db:       &fakeDB{getGTIDExecutedResult: "bbbbbbbb-0000-0000-0000-000000000002:1-5"},
			checkApply: func(t *testing.T, call applyCall) {
				assert.Len(t, call.binlogPaths, 2)
				assert.Contains(t, call.mysqlbinlogArgs, "--disable-log-bin")
				assert.Contains(t, call.mysqlbinlogArgs, "--exclude-gtids=bbbbbbbb-0000-0000-0000-000000000002:1-5")
				assert.Contains(t, call.mysqlbinlogArgs, "--stop-datetime=2024-01-15 12:00:00")
				// Should not contain --include-gtids for date mode
				for _, arg := range call.mysqlbinlogArgs {
					assert.False(t, strings.HasPrefix(arg, "--include-gtids"), "date mode should not have --include-gtids")
				}
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
				// No --exclude-gtids when GTID_EXECUTED is empty
				for _, arg := range call.mysqlbinlogArgs {
					assert.False(t, strings.HasPrefix(arg, "--exclude-gtids"), "should not have --exclude-gtids when GTID_EXECUTED is empty")
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// Set up binlogs file.
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
			apply := func(_ context.Context, binlogPaths []string, mysqlbinlogArgs []string, mysqlArgs []string, _ string) error {
				captured = applyCall{
					binlogPaths:     binlogPaths,
					mysqlbinlogArgs: mysqlbinlogArgs,
					mysqlArgs:       mysqlArgs,
				}
				return tc.applyErr
			}

			err := run(t.Context(), newS3, newDB, getSecret, apply)

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
