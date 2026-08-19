// Package secrets reads the system user passwords the operator mounts into every container it runs.
package secrets

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

// Get returns the password of a system user. Only known users are accepted, so the
// name can never escape the mount.
func Get(username apiv1.SystemUser) (string, error) {
	if !username.IsKnown() {
		return "", errors.Errorf("unknown system user %q", string(username))
	}
	path := filepath.Join(naming.CredsMountPath, string(username))
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}
