package backup

import (
	"context"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"

	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

type Handler struct {
	status status

	newStorageFunc   storage.NewClientFunc
	getNamespaceFunc func() (string, error)
	deleteBackupFunc func(ctx context.Context, cfg *xb.BackupConfig, backupName string) error
}

func (h *Handler) init() {
	if h.deleteBackupFunc == nil {
		h.deleteBackupFunc = deleteBackup
	}
	if h.newStorageFunc == nil {
		h.newStorageFunc = storage.NewClient
	}
	if h.getNamespaceFunc == nil {
		h.getNamespaceFunc = func() (string, error) {
			ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
			if err != nil {
				return "", errors.Wrap(err, "read namespace file")
			}

			return string(ns), nil
		}
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h.init()

	switch req.Method {
	case http.MethodGet:
		h.getBackupHandler(w, req)
	case http.MethodPost:
		h.createBackupHandler(w, req)
	case http.MethodDelete:
		h.deleteBackupHandler(w, req)
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) backupExists(ctx context.Context, cfg *xb.BackupConfig) (bool, error) {
	opts, err := storage.GetOptionsFromBackupConfig(cfg)
	if err != nil {
		return false, errors.Wrap(err, "get options from backup config")
	}
	storage, err := h.newStorageFunc(ctx, opts)
	if err != nil {
		return false, errors.Wrap(err, "new storage")
	}
	objects, err := storage.ListObjects(ctx, cfg.Destination)
	if err != nil {
		return false, errors.Wrap(err, "list objects")
	}
	if len(objects) == 0 {
		return false, nil
	}
	return true, nil
}

type status struct {
	isRunning         atomic.Bool
	currentBackupConf *xb.BackupConfig

	mu sync.Mutex
}

func (s *status) tryRunBackup() bool {
	return s.isRunning.CompareAndSwap(false, true)
}

func (s *status) doneBackup() {
	s.isRunning.Store(false)
}

func (s *status) setBackupConfig(conf xb.BackupConfig) {
	s.mu.Lock()
	s.currentBackupConf = &conf
	s.mu.Unlock()
}

func (s *status) removeBackupConfig() {
	s.mu.Lock()
	s.currentBackupConf = nil
	s.mu.Unlock()
}

func (s *status) getBackupConfig() *xb.BackupConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentBackupConf
}
