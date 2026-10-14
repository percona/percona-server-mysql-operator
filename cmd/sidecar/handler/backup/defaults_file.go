package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func defaultsFilePaths(conf *xb.BackupConfig) []string {
	if conf == nil {
		return nil
	}

	defaultsFileValue := conf.ContainerOptions.GetArgs().GetXtrabackupFlagValue("--defaults-file")
	if defaultsFileValue == "" {
		return nil
	}

	defaultsFiles := make([]string, 0, 2)
	if hasCustomConfig() {
		defaultsFiles = append(defaultsFiles, mysql.CustomMyCnfPath)
	}
	defaultsFiles = append(defaultsFiles, defaultsFileValue)
	return defaultsFiles
}

// preflightDefaultsFiles validates each configuration file directly using xtrabackup.
// This is necessary because xtrabackup can exit with code 0 even when files
// referenced by !include directives are invalid or do not exist.
func preflightDefaultsFiles(ctx context.Context, conf *xb.BackupConfig) error {
	for _, path := range defaultsFilePaths(conf) {
		path, err := normalizeDefaultsFilePath(path)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "xtrabackup", "--defaults-file="+path, "--print-defaults")
		cmd.Env = envs(*conf)
		// Leave stdout unset to discard printed options, which may contain secrets.
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "preflight defaults file %q: %s", path, strings.TrimSpace(stderr.String()))
		}
	}
	return nil
}

// generateDefaultsFile creates a temporary merged defaults file when the user specifies --defaults-file.
// It includes spec.mysql.configuration, when present, followed by the user's file.
func generateDefaultsFile(conf *xb.BackupConfig) (string, error) {
	defaultsFiles := defaultsFilePaths(conf)
	if len(defaultsFiles) == 0 {
		return "", nil
	}

	generatedPath, err := createMergedDefaultsFile(defaultsFiles...)
	if err != nil {
		return "", errors.Wrap(err, "create defaults file")
	}

	return generatedPath, nil
}

func normalizeDefaultsFilePath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", errors.Wrap(err, "get absolute defaults file path")
	}
	if strings.ContainsAny(path, "\r\n") {
		return "", errors.New("defaults file path contains a newline")
	}
	return path, nil
}

func createMergedDefaultsFile(paths ...string) (path string, err error) {
	for i := range paths {
		paths[i], err = normalizeDefaultsFilePath(paths[i])
		if err != nil {
			return "", err
		}
	}

	file, err := os.CreateTemp("", "xtrabackup-defaults-*.cnf")
	if err != nil {
		return "", errors.Wrap(err, "create merged defaults file")
	}
	defer func() {
		if err != nil {
			file.Close()    //nolint:errcheck
			os.Remove(path) //nolint:errcheck
		}
	}()

	path = file.Name()
	for _, includePath := range paths {
		if _, err = fmt.Fprintf(file, "!include %s\n", includePath); err != nil {
			return path, errors.Wrap(err, "write merged defaults file")
		}
	}
	if err = file.Close(); err != nil {
		return path, errors.Wrap(err, "close merged defaults file")
	}

	return path, nil
}
