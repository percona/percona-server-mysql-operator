package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sync/errgroup"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

var log = logf.Log.WithName("sidecar")
var sensitiveFlags = regexp.MustCompile("--password=(.*)|--.*-access-key=(.*)|--.*secret-key=(.*)")

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

func getSecret(username apiv1alpha1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

func sanitizeCmd(cmd *exec.Cmd) string {
	c := []string{cmd.Path}

	for _, arg := range cmd.Args[1:] {
		c = append(c, sensitiveFlags.ReplaceAllString(arg, ""))
	}

	return strings.Join(c, " ")
}

func getNamespace() (string, error) {
	ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", errors.Wrap(err, "read namespace file")
	}

	return string(ns), nil
}

func xtrabackupArgs(user, pass string) []string {
	return []string{
		"--backup",
		"--stream=xbstream",
		"--safe-slave-backup",
		"--slave-info",
		"--target-dir=/backup/",
		fmt.Sprintf("--user=%s", user),
		fmt.Sprintf("--password=%s", pass),
	}
}

func backupHandler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodPost:
		createBackupHandler(w, req)
	case http.MethodDelete:
		deleteBackupHandler(w, req)
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}

func deleteBackupHandler(w http.ResponseWriter, req *http.Request) {
	ns, err := getNamespace()
	if err != nil {
		log.Error(err, "failed to detect namespace")
		http.Error(w, "backup failed", http.StatusInternalServerError)
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
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	backupConf := xb.BackupConfig{}
	if err = json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}

	backupLog, err := os.OpenFile(filepath.Join(mysql.BackupLogDir, backupName+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Error(err, "failed to open log file")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer backupLog.Close()
	logWriter := io.MultiWriter(backupLog, os.Stderr)

	xbcloud := exec.CommandContext(req.Context(), "xbcloud", xb.XBCloudArgs(xb.XBCloudActionDelete, &backupConf)...)
	xbcloudErr, err := xbcloud.StderrPipe()
	if err != nil {
		log.Error(err, "xbcloud stderr pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbcloudErr.Close()
	log.Info(
		"Deleting Backup",
		"destination", backupConf.Destination,
		"storage", backupConf.Type,
		"xbcloudCmd", sanitizeCmd(xbcloud),
	)
	if err := xbcloud.Start(); err != nil {
		log.Error(err, "failed to start xbcloud")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(logWriter, xbcloudErr); err != nil {
		log.Error(err, "failed to copy xbcloud stderr")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	if err := xbcloud.Wait(); err != nil {
		log.Error(err, "failed waiting for xbcloud to finish")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	log.Info("Backup deleted successfully", "destination", backupConf.Destination, "storage", backupConf.Type)
}

func createBackupHandler(w http.ResponseWriter, req *http.Request) {
	ns, err := getNamespace()
	if err != nil {
		log.Error(err, "failed to detect namespace")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}
	backupName := path[2]
	log = log.WithValues("namespace", ns, "name", backupName)

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Error(err, "failed to read request data")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	backupConf := xb.BackupConfig{}
	if err := json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Connection", "keep-alive")

	backupUser := apiv1alpha1.UserXtraBackup
	backupPass, err := getSecret(backupUser)
	if err != nil {
		log.Error(err, "failed to get backup password")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	xtrabackup := exec.Command("xtrabackup", xtrabackupArgs(string(backupUser), backupPass)...)

	xbOut, err := xtrabackup.StdoutPipe()
	if err != nil {
		log.Error(err, "xtrabackup stdout pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbOut.Close()

	xbErr, err := xtrabackup.StderrPipe()
	if err != nil {
		log.Error(err, "xtrabackup stderr pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbErr.Close()

	backupLog, err := os.Create(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		log.Error(err, "failed to create log file")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer backupLog.Close()
	logWriter := io.MultiWriter(backupLog, os.Stderr)

	xbcloud := exec.Command("xbcloud", xb.XBCloudArgs(xb.XBCloudActionPut, &backupConf)...)
	xbcloud.Stdin = xbOut

	xbcloudErr, err := xbcloud.StderrPipe()
	if err != nil {
		log.Error(err, "xbcloud stderr pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbcloudErr.Close()

	log.Info(
		"Backup starting",
		"destination", backupConf.Destination,
		"storage", backupConf.Type,
		"xtrabackupCmd", sanitizeCmd(xtrabackup),
		"xbcloudCmd", sanitizeCmd(xbcloud),
	)

	g := new(errgroup.Group)
	g.Go(func() error {
		if err := xbcloud.Start(); err != nil {
			log.Error(err, "failed to start xbcloud")
			return err
		}

		if _, err := io.Copy(logWriter, xbcloudErr); err != nil {
			log.Error(err, "failed to copy xbcloud stderr")
			return err
		}

		if err := xbcloud.Wait(); err != nil {
			log.Error(err, "failed waiting for xbcloud to finish")
			return err
		}
		return nil
	})

	g.Go(func() error {
		if err := xtrabackup.Start(); err != nil {
			log.Error(err, "failed to start xtrabackup command")
			return err
		}

		if _, err := io.Copy(logWriter, xbErr); err != nil {
			log.Error(err, "failed to copy xtrabackup stderr")
			return err
		}

		if err := xtrabackup.Wait(); err != nil {
			log.Error(err, "failed waiting for xtrabackup to finish")
			return err
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	log.Info("Backup finished successfully", "destination", backupConf.Destination, "storage", backupConf.Type)
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
