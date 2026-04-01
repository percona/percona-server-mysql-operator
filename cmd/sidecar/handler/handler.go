package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/handler/backup"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func Backup() http.Handler {
	return new(backup.Handler)
}

func LogsHandlerFunc(w http.ResponseWriter, req *http.Request) {
	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}

	backupName := path[2]
	logFile, err := os.Open(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		http.Error(w, "failed to open log file", http.StatusInternalServerError)
		return
	}
	defer logFile.Close() //nolint:errcheck

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Connection", "keep-alive")

	buf := bufio.NewScanner(logFile)
	for buf.Scan() {
		if _, err := fmt.Fprintln(w, buf.Text()); err != nil {
			http.Error(w, "failed to scan log", http.StatusInternalServerError)
			return
		}
	}
	if err := buf.Err(); err != nil {
		http.Error(w, "failed to scan log", http.StatusInternalServerError)
		return
	}
}

func GetCheckpointInfoFunc(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
		return
	}

	log := logf.Log.WithName("GetCheckpointInfo")

	defer req.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(req.Body)
	if err != nil {
		log.Error(err, "failed to read request body")
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	backupConf := xb.BackupConfig{}
	if err := json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "failed to unmarshal backup config", http.StatusBadRequest)
		return
	}

	xbcloud := exec.CommandContext(req.Context(), "xbcloud", backupConf.XbcloudGetArgs("xtrabackup_checkpoints")...)
	xbOut, err := xbcloud.StdoutPipe()
	if err != nil {
		log.Error(err, "failed to create stdout pipe")
		http.Error(w, "failed to create stdout pipe", http.StatusInternalServerError)
		return
	}
	defer xbOut.Close() //nolint:errcheck

	xbErr, err := xbcloud.StderrPipe()
	if err != nil {
		log.Error(err, "failed to create stderr pipe")
		http.Error(w, "failed to create stderr pipe", http.StatusInternalServerError)
		return
	}
	defer xbErr.Close() //nolint:errcheck

	if err := xbcloud.Start(); err != nil {
		log.Error(err, "failed to start xbcloud")
		http.Error(w, "failed to start xbcloud", http.StatusInternalServerError)
		return
	}

	go func() {
		io.Copy(os.Stderr, xbErr) //nolint:errcheck
	}()

	info := xb.CheckpointInfo{}
	if err := info.ParseFrom(xbOut); err != nil {
		log.Error(err, "failed to read checkpoint info")
		http.Error(w, "failed to read checkpoint info", http.StatusInternalServerError)
		return
	}

	if err := xbcloud.Wait(); err != nil {
		log.Error(err, "xbcloud command failed")
		http.Error(w, "xbcloud command failed", http.StatusInternalServerError)
		return
	}

	infoB, err := json.Marshal(info)
	if err != nil {
		log.Error(err, "failed to marshal checkpoint info")
		http.Error(w, "failed to marshal checkpoint info", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err = w.Write(infoB); err != nil {
		log.Error(err, "failed to write response")
	}
}
