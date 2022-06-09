package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/pkg/errors"
)

var log = logf.Log.WithName("sidecar")

func main() {
	opts := zap.Options{Development: true}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mux := http.NewServeMux()

	mux.HandleFunc("/health/", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "OK")
	})
	mux.HandleFunc("/backup/", backupHandler)
	mux.HandleFunc("/logs/", logHandler)

	log.Info("starting http server")
	log.Error(http.ListenAndServe(":6033", mux), "http server failed")
}

func getNamespace() (string, error) {
	ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", errors.Wrap(err, "read namespace file")
	}

	return string(ns), nil
}

func xtrabackupArgs() []string {
	return []string{
		"--backup",
		"--stream=xbstream",
		fmt.Sprintf("--user=%s", os.Getenv("BACKUP_USER")),
		fmt.Sprintf("--password=%s", os.Getenv("BACKUP_PASSWORD")),
	}
}

func backupHandler(w http.ResponseWriter, req *http.Request) {
	ns, err := getNamespace()
	if err != nil {
		http.Error(w, "failed to detect namespace", http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "HTTP server does not support streaming!", http.StatusInternalServerError)
		return
	}

	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}

	backupName := path[2]
	logFile, err := os.Create(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		http.Error(w, "failed to create log file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Connection", "keep-alive")

	xtrabackup := exec.Command("xtrabackup", xtrabackupArgs()...)

	stdout, err := xtrabackup.StdoutPipe()
	if err != nil {
		log.Error(err, "xtrabackup stdout pipe failed")
		http.Error(w, "xtrabackup failed", http.StatusInternalServerError)
		return
	}

	stderr, err := xtrabackup.StderrPipe()
	if err != nil {
		log.Error(err, "xtrabackup stderr pipe failed")
		http.Error(w, "xtrabackup failed", http.StatusInternalServerError)
		return
	}

	log.Info("Backup starting", "name", backupName, "namespace", ns)

	if err := xtrabackup.Start(); err != nil {
		log.Error(err, "failed to start xtrabackup command")
		http.Error(w, "xtrabackup failed", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(w, stdout); err != nil {
		log.Error(err, "failed to copy stdout")
		http.Error(w, "buffer copy failed", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(logFile, stderr); err != nil {
		log.Error(err, "failed to copy stderr")
		http.Error(w, "log copy failed", http.StatusInternalServerError)
		return
	}

	if err := xtrabackup.Wait(); err != nil {
		log.Error(err, "failed waiting for xtrabackup to finish")
		http.Error(w, "xtrabackup failed", http.StatusInternalServerError)
		return
	}

	log.Info("Backup finished successfully", "name", backupName, "namespace", ns)

	flusher.Flush()
}

func logHandler(w http.ResponseWriter, req *http.Request) {
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
	defer logFile.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Connection", "keep-alive")

	buf := bufio.NewScanner(logFile)
	for buf.Scan() {
		fmt.Fprintln(w, buf.Text())
	}
	if err := buf.Err(); err != nil {
		http.Error(w, "failed to scan log", http.StatusInternalServerError)
		return
	}
}
