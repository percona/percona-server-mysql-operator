package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
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
	objectKeys      []string
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
		pitrForce     string
		db            *fakeDB
		newDB         func(ctx context.Context, params db.DBParams) (Database, error)
		newS3         func(*fakeStorage) newStorageFn
		getSecret     func(apiv1.SystemUser) (string, error)
		applyErr      error
		expectedError string
		checkApply    func(t *testing.T, call applyCall)
		checkObjects  func(t *testing.T, objects []binlogSource)
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
				// Should not contain --include-gtids for date mode
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
				// No --exclude-gtids when GTID_EXECUTED is empty
				for _, arg := range call.mysqlbinlogArgs {
					assert.False(t, strings.HasPrefix(arg, "--exclude-gtids"), "should not have --exclude-gtids when GTID_EXECUTED is empty")
				}
			},
		},
		"unencrypted binlog is not decrypted when later binlogs are encrypted": {
			entries: []binlogserver.BinlogEntry{
				{Name: "binlog.000001", URI: "s3://mybucket/binlogs/binlog.000001"},
				{
					Name: "binlog.000002",
					URI:  "s3://mybucket/binlogs/binlog.000002",
					Encryption: &binlogserver.Encryption{
						FileKeyEnvelope: &binlogserver.FileKeyEnvelope{KekID: "alpha"},
						FileDataEnvelope: &binlogserver.FileDataEnvelope{
							Cipher: "AES-256-CTR",
						},
					},
				},
			},
			pitrType: "gtid",
			pitrGTID: "aaaaaaaa-0000-0000-0000-000000000001:1-10",
			db:       &fakeDB{getGTIDExecutedResult: "aaaaaaaa-0000-0000-0000-000000000001:1-5"},
			checkObjects: func(t *testing.T, objects []binlogSource) {
				require.Len(t, objects, 2)

				reader, err := objects[0].decrypt(io.NopCloser(strings.NewReader("plain binlog")))
				require.NoError(t, err)
				defer reader.Close() //nolint:errcheck

				data, err := io.ReadAll(reader)
				require.NoError(t, err)
				assert.Equal(t, "plain binlog", string(data))
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
			t.Setenv("PITR_FORCE", tc.pitrForce)
			t.Setenv("S3_BUCKET", bucket)
			t.Setenv("KEYRING_PATH", "")

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
			apply := func(_ context.Context, objects []binlogSource, _ getObjectFn, mysqlbinlogArgs []string, mysqlArgs []string, _ string) error {
				var objectKeys []string
				for _, obj := range objects {
					objectKeys = append(objectKeys, obj.objectKey)
				}
				captured = applyCall{
					objectKeys:      objectKeys,
					mysqlbinlogArgs: mysqlbinlogArgs,
					mysqlArgs:       mysqlArgs,
				}
				if tc.checkObjects != nil {
					tc.checkObjects(t, objects)
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

func TestDecryptingReader(t *testing.T) {
	plaintext := append([]byte{}, binlogMagic...)
	plaintext = append(plaintext, []byte("events")...)

	ciphertext, entry, keyring := encryptBinlogForTest(t, plaintext)
	reader, err := decryptingReader(io.NopCloser(bytes.NewReader(ciphertext)), entry, keyring)
	require.NoError(t, err)
	defer reader.Close() //nolint:errcheck

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func encryptBinlogForTest(t *testing.T, plaintext []byte) ([]byte, binlogserver.BinlogEntry, *binlogserver.Keyring) {
	t.Helper()

	kek := bytes.Repeat([]byte{0x11}, 32)
	fileKey := bytes.Repeat([]byte{0x22}, 32)
	fileKeyBlock, err := aes.NewCipher(kek)
	require.NoError(t, err)

	wrappedFileKey := make([]byte, len(fileKey))
	for i := 0; i < len(fileKey); i += fileKeyBlock.BlockSize() {
		fileKeyBlock.Encrypt(wrappedFileKey[i:i+fileKeyBlock.BlockSize()], fileKey[i:i+fileKeyBlock.BlockSize()])
	}

	dataIV := bytes.Repeat([]byte{0x33}, aes.BlockSize)
	dataBlock, err := aes.NewCipher(fileKey)
	require.NoError(t, err)
	stream := cipher.NewCTR(dataBlock, dataIV)
	ciphertext := make([]byte, len(plaintext))
	stream.XORKeyStream(ciphertext, plaintext)

	entry := binlogserver.BinlogEntry{
		Name: "binlog.000001",
		Encryption: &binlogserver.Encryption{
			FileKeyEnvelope: &binlogserver.FileKeyEnvelope{
				KekID:   "alpha",
				DataHex: hex.EncodeToString(wrappedFileKey),
			},
			FileDataEnvelope: &binlogserver.FileDataEnvelope{
				Cipher: "AES-256-CTR",
				IVHex:  hex.EncodeToString(dataIV),
			},
		},
	}

	keyring := &binlogserver.Keyring{
		Version: 1,
		Keys: []binlogserver.Key{
			{
				Id:      "alpha",
				Cipher:  "AES-256-ECB",
				DataHex: hex.EncodeToString(kek),
			},
		},
	}

	return ciphertext, entry, keyring
}
