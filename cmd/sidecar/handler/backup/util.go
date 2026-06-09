package backup

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/pkg/errors"

	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

// backupNameRE matches RFC 1123 subdomain names, the format Kubernetes
// resource names follow.
var backupNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// ErrInvalidBackupName is returned by ValidateBackupName for any input
// that is not a valid RFC 1123 subdomain.
var ErrInvalidBackupName = errors.New("invalid backup name")

// ValidateBackupName rejects values that could traverse outside of the
// backup log directory when joined into a file path. backupName is taken
// from the HTTP request URL, so untrusted input reaches filepath.Join.
func ValidateBackupName(backupName string) error {
	if backupName == "" {
		return errors.Wrap(ErrInvalidBackupName, "empty")
	}
	if len(backupName) > 253 {
		return errors.Wrapf(ErrInvalidBackupName, "exceeds 253 characters: %d", len(backupName))
	}
	if !backupNameRE.MatchString(backupName) {
		return errors.Wrapf(ErrInvalidBackupName, "%q is not a valid RFC 1123 subdomain", backupName)
	}
	return nil
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
