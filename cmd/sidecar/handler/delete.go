package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func (h *handlerBackup) deleteBackup(w http.ResponseWriter, req *http.Request) {
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
	defer req.Body.Close()

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
