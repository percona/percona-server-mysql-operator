package db

import (
	"bytes"
	"context"
	"fmt"
	"regexp"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	corev1 "k8s.io/api/core/v1"
)

type AdminManager struct {
	db *db
}

func NewAdminManager(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1.SystemUser, pass, host string) *AdminManager {
	return &AdminManager{db: newDB(pod, cliCmd, user, pass, host)}
}

func (m *AdminManager) SetReadOnly(ctx context.Context, readonly bool) error {
	val := "OFF"
	if readonly {
		val = "ON"
	}
	cmd := fmt.Sprintf("SET PERSIST read_only=%s", val)
	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, cmd, &outb, &errb)
	if err != nil {
		return err
	}
	return nil
}

func (m *AdminManager) SetSuperReadOnly(ctx context.Context, readonly bool) error {
	val := "OFF"
	if readonly {
		val = "ON"
	}
	cmd := fmt.Sprintf("SET PERSIST super_read_only=%s", val)
	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, cmd, &outb, &errb)
	if err != nil {
		return err
	}
	return nil
}

var (
	variableNameRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	loosePrefixRegex  = regexp.MustCompile(`^loose[-_]`)
)

// IsLooseVariable reports whether key carries the loose prefix, which tells
// mysqld to ignore the option when the server doesn't know it.
func IsLooseVariable(key string) bool {
	return loosePrefixRegex.MatchString(key)
}

func (m *AdminManager) SetGlobalVariable(ctx context.Context, key, value string) error {
	key = loosePrefixRegex.ReplaceAllString(key, "")
	if !variableNameRegex.MatchString(key) {
		return fmt.Errorf("invalid global variable name: %q", key)
	}

	var errb, outb bytes.Buffer
	cmd := fmt.Sprintf("SET GLOBAL %s=%s", key, value)
	err := m.db.exec(ctx, cmd, &outb, &errb)
	if err != nil {
		return err
	}
	return nil
}
