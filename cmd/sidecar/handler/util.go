package handler

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func getSecret(username apiv1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
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

func sanitizeCmd(cmd *exec.Cmd) string {
	sensitiveFlags := regexp.MustCompile("--password=(.*)|--.*-access-key=(.*)|--.*secret-key=(.*)")
	c := []string{cmd.Path}

	for _, arg := range cmd.Args[1:] {
		c = append(c, sensitiveFlags.ReplaceAllString(arg, ""))
	}

	return strings.Join(c, " ")
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
