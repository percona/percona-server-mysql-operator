package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
	fakestorage "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage/fake"
)

type fakeStorageClient struct {
	storage.Storage
}

func (c *fakeStorageClient) ListObjects(_ context.Context, destination string) ([]string, error) {
	if destination == "non-existing-backup" {
		return nil, nil
	}
	switch destination {
	case "non-existing-backup":
		return nil, nil
	case "existing-backup":
		return []string{"some-dest/backup1", "some-dest/backup2"}, nil
	default:
		return nil, errors.New("fake list objects error")
	}
}

func TestHandlerDelete(t *testing.T) {
	cfg := xb.BackupConfig{
		Type: apiv1.BackupStorageS3,
		S3: xb.BackupConfigS3{
			Bucket:       "bucket",
			Region:       "region",
			EndpointURL:  "http://endpoint.local",
			StorageClass: "storage-class",
			AccessKey:    "some-key",
			SecretKey:    "secret-key",
		},
	}

	t.Run("backup exists", func(t *testing.T) {
		backupDeleted := false
		h := &Handler{
			newStorageFunc: func(ctx context.Context, opts storage.Options) (storage.Storage, error) {
				defaultFakeClient, err := fakestorage.NewFakeClient(ctx, opts)
				if err != nil {
					return nil, err
				}
				return &fakeStorageClient{Storage: defaultFakeClient}, nil
			},
			getNamespaceFunc: func() (string, error) {
				return "test-namespace", nil
			},
			deleteBackupFunc: func(ctx context.Context, cfg *xb.BackupConfig, backupName string) error {
				backupDeleted = true
				return nil
			},
		}
		cfg := cfg
		cfg.Destination = "existing-backup"
		body, err := json.Marshal(cfg)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/backup/name", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		h.deleteBackupHandler(w, req)

		res := w.Result()
		defer res.Body.Close() //nolint:errcheck

		resBody, err := io.ReadAll(res.Body)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, res.StatusCode)
		assert.Equal(t, "", string(resBody))
		assert.Equal(t, true, backupDeleted)
	})

	t.Run("backup doesn't exist", func(t *testing.T) {
		backupDeleted := false
		h := &Handler{
			newStorageFunc: func(ctx context.Context, opts storage.Options) (storage.Storage, error) {
				defaultFakeClient, err := fakestorage.NewFakeClient(ctx, opts)
				if err != nil {
					return nil, err
				}
				return &fakeStorageClient{Storage: defaultFakeClient}, nil
			},
			getNamespaceFunc: func() (string, error) {
				return "test-namespace", nil
			},
			deleteBackupFunc: func(ctx context.Context, cfg *xb.BackupConfig, backupName string) error {
				backupDeleted = true
				return nil
			},
		}
		cfg := cfg
		cfg.Destination = "non-existing-backup"
		body, err := json.Marshal(cfg)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/backup/name", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		h.deleteBackupHandler(w, req)

		res := w.Result()
		defer res.Body.Close() //nolint:errcheck

		resBody, err := io.ReadAll(res.Body)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, res.StatusCode)
		assert.Equal(t, "", string(resBody))
		assert.Equal(t, false, backupDeleted)
	})
}
