package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

// generateDefaultsFile creates a temporary merged defaults file when the user specifies --defaults-file
// It includes the configuration from `spec.mysql.configuration`, when present, followed by the file specified in the --defaults-file.
func generateDefaultsFile(conf *xb.BackupConfig) (string, error) {
	if conf == nil {
		return "", nil
	}

	defaultsFileValue := conf.ContainerOptions.GetArgs().GetXtrabackupFlagValue("--defaults-file")
	if defaultsFileValue == "" {
		return "", nil
	}

	defaultsFiles := make([]string, 0, 2)
	if hasCustomConfig() {
		defaultsFiles = append(defaultsFiles, mysql.CustomMyCnfPath)
	}
	defaultsFiles = append(defaultsFiles, defaultsFileValue)

	generatedPath, err := createMergedDefaultsFile(defaultsFiles...)
	if err != nil {
		return "", errors.Wrap(err, "create defaults file")
	}

	return generatedPath, nil
}

func createMergedDefaultsFile(paths ...string) (path string, err error) {
	for i := range paths {
		paths[i], err = filepath.Abs(paths[i])
		if err != nil {
			return "", errors.Wrap(err, "get absolute defaults file path")
		}
		if strings.ContainsAny(paths[i], "\r\n") {
			return "", errors.New("defaults file path contains a newline")
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
