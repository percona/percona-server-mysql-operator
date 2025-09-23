package handler

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

type Status struct {
	isRunning         atomic.Bool
	currentBackupConf *xb.BackupConfig

	mu sync.Mutex
}

func (s *Status) TryRunBackup() bool {
	return s.isRunning.CompareAndSwap(false, true)
}

func (s *Status) DoneBackup() {
	s.isRunning.Store(false)
}

func (s *Status) SetBackupConfig(conf xb.BackupConfig) {
	s.mu.Lock()
	s.currentBackupConf = &conf
	s.mu.Unlock()
}

func (s *Status) RemoveBackupConfig() {
	s.mu.Lock()
	s.currentBackupConf = nil
	s.mu.Unlock()
}

func (s *Status) GetBackupConfig() *xb.BackupConfig {
	s.mu.Lock()
	cfg := *s.currentBackupConf
	s.mu.Unlock()
	return &cfg
}

type handlerBackup struct {
	status Status

	newStorageFunc   storage.NewClientFunc
	getNamespaceFunc func() (string, error)
	deleteBackupFunc func(ctx context.Context, cfg *xb.BackupConfig, backupName string) error
}

func NewBackupHandler() http.Handler {
	return &handlerBackup{
		newStorageFunc: storage.NewClient,
		getNamespaceFunc: func() (string, error) {
			ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
			if err != nil {
				return "", errors.Wrap(err, "read namespace file")
			}

			return string(ns), nil
		},
		deleteBackupFunc: deleteBackup,
	}
}

func (h *handlerBackup) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		h.getBackup(w, req)
	case http.MethodPost:
		h.createBackup(w, req)
	case http.MethodDelete:
		h.deleteBackup(w, req)
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}

func (h *handlerBackup) backupExists(ctx context.Context, cfg *xb.BackupConfig) (bool, error) {
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
