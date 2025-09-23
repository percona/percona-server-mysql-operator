package handler

import (
	"encoding/json"
	"net/http"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func (h *handlerBackup) getBackup(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close() //nolint:errcheck
	log := logf.Log.WithName("sidecar").WithName("get backup")

	if !h.status.isRunning.Load() {
		http.Error(w, "backup is not running", http.StatusNotFound)
		return
	}

	data, err := json.Marshal(h.status.GetBackupConfig())
	if err != nil {
		log.Error(err, "failed to marshal data")
		http.Error(w, "get backup failed", http.StatusInternalServerError)
		return
	}

	_, err = w.Write(data)
	if err != nil {
		log.Error(err, "failed to write data")
		http.Error(w, "get backup failed", http.StatusInternalServerError)
		return
	}
}
