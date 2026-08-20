package helpers

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func GetSecret(username apiv1.SystemUser) (string, error) {
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
