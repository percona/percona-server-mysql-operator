package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

var (
	log            = logf.Log.WithName("sidecar")
	sensitiveFlags = regexp.MustCompile("--password=(.*)|--.*-access-key=(.*)|--.*secret-key=(.*)")
)

var status Status

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

func startServer() *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/health/", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "OK")
	})
	mux.HandleFunc("/backup/", backupHandler)
	mux.HandleFunc("/logs/", logHandler)

	srv := &http.Server{Addr: ":" + strconv.Itoa(mysql.SidecarHTTPPort), Handler: mux}

	go func() {
		log.Info("starting http server")
		// always returns error. ErrServerClosed on graceful close
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Error(err, "http server failed")
		}
	}()

	return srv
}

func main() {
	opts := zap.Options{Development: true}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	srv := startServer()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop

	log.Info("received interrupt signal, shutting down http server")

	// TODO: should this timeout use terminationGracePeriodSeconds?
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error(err, "graceful shutdown failed")
		os.Exit(1)
	}

	os.Exit(0)
}

func getSecret(username apiv1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := os.ReadFile(path)
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

func xtrabackupArgs(user, pass string, conf *xb.BackupConfig) []string {
	args := []string{
		"--backup",
		"--stream=xbstream",
		"--safe-slave-backup",
		"--slave-info",
		"--target-dir=/backup/",
		fmt.Sprintf("--user=%s", user),
		fmt.Sprintf("--password=%s", pass),
	}
	if conf != nil && conf.ContainerOptions != nil {
		args = append(args, conf.ContainerOptions.Args.Xtrabackup...)
	}
	return args
}

func backupHandler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		getBackupHandler(w, req)
	case http.MethodPost:
		createBackupHandler(w, req)
	case http.MethodDelete:
		deleteBackupHandler(w, req)
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}

func getBackupHandler(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	if !status.isRunning.Load() {
		http.Error(w, "backup is not running", http.StatusNotFound)
		return
	}

	data, err := json.Marshal(status.GetBackupConfig())
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

func deleteBackupHandler(w http.ResponseWriter, req *http.Request) {
	ns, err := getNamespace()
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
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	backupConf := xb.BackupConfig{}
	if err = json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}

	if err := deleteBackup(req.Context(), &backupConf, backupName); err != nil {
		log.Error(err, "failed to delete backup")
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}

	log.Info("Backup deleted successfully", "destination", backupConf.Destination, "storage", backupConf.Type)
}

func deleteBackup(ctx context.Context, cfg *xb.BackupConfig, backupName string) error {
	logWriter := io.Writer(os.Stderr)
	if backupName != "" {
		backupLog, err := os.OpenFile(
			filepath.Join(mysql.BackupLogDir, backupName+".log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			return errors.Wrap(err, "failed to open log file")
		}
		defer backupLog.Close()
		logWriter = io.MultiWriter(backupLog, os.Stderr)
	}
	xbcloud := exec.CommandContext(ctx, "xbcloud", xb.XBCloudArgs(xb.XBCloudActionDelete, cfg)...)
	xbcloud.Env = envs(*cfg)
	xbcloudErr, err := xbcloud.StderrPipe()
	if err != nil {
		return errors.Wrap(err, "xbcloud stderr pipe failed")
	}
	defer xbcloudErr.Close()
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

func backupExists(ctx context.Context, cfg *xb.BackupConfig) (bool, error) {
	opts, err := storage.GetOptionsFromBackupConfig(cfg)
	if err != nil {
		return false, errors.Wrap(err, "get options from backup config")
	}
	storage, err := storage.NewClient(ctx, opts)
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

func checkBackupMD5Size(ctx context.Context, cfg *xb.BackupConfig) error {
	// xbcloud doesn't create md5 file for azure
	if cfg.Type == apiv1.BackupStorageAzure {
		return nil
	}

	opts, err := storage.GetOptionsFromBackupConfig(cfg)
	if err != nil {
		return errors.Wrap(err, "get options from backup config")
	}
	storage, err := storage.NewClient(ctx, opts)
	if err != nil {
		return errors.Wrap(err, "new storage")
	}
	r, err := storage.GetObject(ctx, cfg.Destination+".md5")
	if err != nil {
		return errors.Wrap(err, "get object")
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return errors.Wrap(err, "read all")
	}

	// Q: what value we should use here?
	// size of the `demand-backup` test md5 file is 4575
	if len(data) < 3000 {
		return errors.Errorf("backup was finished unsuccessful: md5 size: %d", len(data))
	}
	return nil
}

func startReplicaSQLThread(ctx context.Context) error {
	backupUser := apiv1alpha1.UserXtraBackup

	backupPass, err := getSecret(backupUser)
	if err != nil {
		return errors.Wrap(err, "get password")
	}

	startSQL := "START REPLICA SQL_THREAD"
	cmd := exec.CommandContext(ctx, "mysql", "-u", string(backupUser), "-p", "-e", startSQL)
	cmd.Stdin = strings.NewReader(backupPass + "\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Error(err, "failed to start SQL thread", "output", string(out))
		return errors.Wrap(err, startSQL)
	}

	return nil
}

func createBackupHandler(w http.ResponseWriter, req *http.Request) {
	if !status.TryRunBackup() {
		log.Info("backup is already running", "host", req.RemoteAddr)
		http.Error(w, "backup is already running", http.StatusConflict)
		return
	}
	defer status.DoneBackup()

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
	if err := json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}

	status.SetBackupConfig(backupConf)
	defer func() {
		status.RemoveBackupConfig()
	}()
	log.Info("Checking if backup exists")
	exists, err := backupExists(req.Context(), &backupConf)
	if err != nil {
		log.Error(err, "failed to check if backup exists")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}
	if exists {
		log.Info("Backup exists. Deleting backup")
		if err := deleteBackup(req.Context(), &backupConf, backupName); err != nil {
			log.Error(err, "failed to delete existing backup")
			http.Error(w, "backup failed", http.StatusBadRequest)
			return
		}
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Connection", "keep-alive")

	backupUser := apiv1.UserXtraBackup
	backupPass, err := getSecret(backupUser)
	if err != nil {
		log.Error(err, "failed to get backup password")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	g, gCtx := errgroup.WithContext(req.Context())

	xtrabackup := exec.CommandContext(gCtx, "xtrabackup", xtrabackupArgs(string(backupUser), backupPass, &backupConf)...)
	xtrabackup.Env = envs(backupConf)

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

	xbcloud := exec.CommandContext(gCtx, "xbcloud", xb.XBCloudArgs(xb.XBCloudActionPut, &backupConf)...)
	xbcloud.Env = envs(backupConf)
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
			log.Error(err, "failed to wait for xtrabackup to finish")
			return err
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		log.Error(err, "backup failed")

		// --safe-slave-backup stops SQL thread but it's not started
		// if xtrabackup command fails
		log.Info("starting replication SQL thread")
		startReplicaSQLThread(req.Context())

		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	if err := checkBackupMD5Size(req.Context(), &backupConf); err != nil {
		log.Error(err, "check backup md5 file size")
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

func envs(cfg xb.BackupConfig) []string {
	envs := os.Environ()
	if cfg.ContainerOptions != nil {
		for _, env := range cfg.ContainerOptions.Env {
			envs = append(envs, fmt.Sprintf("%s=%s", env.Name, env.Value))
		}
	}
	return envs
}
