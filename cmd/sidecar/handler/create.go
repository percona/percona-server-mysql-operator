package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

func (h *handlerBackup) createBackupHandler(w http.ResponseWriter, req *http.Request) {
	log := logf.Log.WithName("sidecar").WithName("create backup")

	if !h.status.TryRunBackup() {
		log.Info("backup is already running", "host", req.RemoteAddr)
		http.Error(w, "backup is already running", http.StatusConflict)
		return
	}
	defer h.status.DoneBackup()

	ns, err := h.getNamespaceFunc()
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
	defer req.Body.Close() //nolint:errcheck

	backupConf := xb.BackupConfig{}
	if err := json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}

	h.status.SetBackupConfig(backupConf)
	defer func() {
		h.status.RemoveBackupConfig()
	}()
	log.Info("Checking if backup exists")
	exists, err := h.backupExists(req.Context(), &backupConf)
	if err != nil {
		log.Error(err, "failed to check if backup exists")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}
	if exists {
		log.Info("Backup exists. Deleting backup")
		if err := h.deleteBackupFunc(req.Context(), &backupConf, backupName); err != nil {
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
	defer xbOut.Close() //nolint:errcheck

	xbErr, err := xtrabackup.StderrPipe()
	if err != nil {
		log.Error(err, "xtrabackup stderr pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbErr.Close() //nolint:errcheck

	backupLog, err := os.Create(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		log.Error(err, "failed to create log file")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer backupLog.Close() //nolint:errcheck
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
	defer xbcloudErr.Close() //nolint:errcheck

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

		if getClusterType() == apiv1.ClusterTypeAsync {
			// --safe-slave-backup stops SQL thread but it's not started
			// if xtrabackup command fails
			log.Info("starting replication SQL thread")
			startReplicaSQLThread(req.Context()) // nolint:errcheck
		}

		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	if err := h.checkBackupMD5Size(req.Context(), &backupConf); err != nil {
		log.Error(err, "check backup md5 file size")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	log.Info("Backup finished successfully", "destination", backupConf.Destination, "storage", backupConf.Type)
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

func getClusterType() apiv1.ClusterType {
	cType, ok := os.LookupEnv("CLUSTER_TYPE")
	if !ok {
		return apiv1.ClusterTypeGR
	}

	return apiv1.ClusterType(cType)
}

func (h *handlerBackup) checkBackupMD5Size(ctx context.Context, cfg *xb.BackupConfig) error {
	// xbcloud doesn't create md5 file for azure
	if cfg.Type == apiv1.BackupStorageAzure {
		return nil
	}

	opts, err := storage.GetOptionsFromBackupConfig(cfg)
	if err != nil {
		return errors.Wrap(err, "get options from backup config")
	}
	storageClient, err := h.newStorageFunc(ctx, opts)
	if err != nil {
		return errors.Wrap(err, "new storage")
	}
	r, err := storageClient.GetObject(ctx, cfg.Destination+".md5")
	if err != nil {
		return errors.Wrap(err, "get object")
	}
	defer r.Close() //nolint:errcheck
	data, err := io.ReadAll(r)
	if err != nil {
		return errors.Wrap(err, "read all")
	}

	// Q: what value we should use here?
	// size of the `demand-backup` test md5 file is 4575
	minSize := 3000
	if len(data) < minSize {
		return errors.Errorf("backup was finished unsuccessful: small md5 size: %d: expected to be >= %d", len(data), minSize)
	}
	return nil
}

func startReplicaSQLThread(ctx context.Context) error {
	log := logf.Log.WithName("startReplicaSQLThread")

	backupUser := apiv1.UserXtraBackup

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
