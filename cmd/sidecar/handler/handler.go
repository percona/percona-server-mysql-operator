package handler

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/handler/backup"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
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
