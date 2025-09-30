package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func (h *Handler) deleteBackupHandler(w http.ResponseWriter, req *http.Request) {
	log := logf.Log.WithName("handlerBackup").WithName("deleteBackup")

	ns, err := h.getNamespaceFunc()
	if err != nil {
		log.Error(err, "failed to detect namespace")
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}

	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}

	backupName := path[2]
	log = log.WithValues("namespace", ns, "name", backupName)
	data, err := io.ReadAll(req.Body)
	if err != nil {
		log.Error(err, "failed to read request data")
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer req.Body.Close() //nolint:errcheck

	backupConf := xb.BackupConfig{}
	if err = json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	exists, err := h.backupExists(req.Context(), &backupConf)
	if err != nil {
		log.Error(err, "failed to check if backup exists")
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if !exists {
		log.Info("Backup doesn't exist during deletion", "destination", backupConf.Destination, "storage", backupConf.Type)
		return
	}

	if err := h.deleteBackupFunc(req.Context(), &backupConf, backupName); err != nil {
		log.Error(err, "failed to delete backup")
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Info("Backup deleted successfully", "destination", backupConf.Destination, "storage", backupConf.Type)
}

func deleteBackup(ctx context.Context, cfg *xb.BackupConfig, backupName string) error {
	log := logf.Log.WithName("deleteBackup")

	logWriter := io.Writer(os.Stderr)
	if backupName != "" {
		backupLog, err := os.OpenFile(
			filepath.Join(mysql.BackupLogDir, backupName+".log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			return errors.Wrap(err, "failed to open log file")
		}
		defer backupLog.Close() //nolint:errcheck
		logWriter = io.MultiWriter(backupLog, os.Stderr)
	}
	xbcloud := exec.CommandContext(ctx, "xbcloud", xb.XBCloudArgs(xb.XBCloudActionDelete, cfg)...)
	xbcloud.Env = envs(*cfg)
	xbcloudErr, err := xbcloud.StderrPipe()
	if err != nil {
		return errors.Wrap(err, "xbcloud stderr pipe failed")
	}
	defer xbcloudErr.Close() //nolint:errcheck
	log.Info(
		"Deleting Backup",
		"destination", cfg.Destination,
		"storage", cfg.Type,
		"xbcloudCmd", sanitizeCmd(xbcloud),
	)
	if err := xbcloud.Start(); err != nil {
		return errors.Wrap(err, "failed to start xbcloud")
	}

	if _, err := io.Copy(logWriter, xbcloudErr); err != nil {
		return errors.Wrap(err, "failed to copy xbcloud stderr")
	}

	if err := xbcloud.Wait(); err != nil {
		return errors.Wrap(err, "failed waiting for xbcloud to finish")
	}
	return nil
}
