package db

import (
	"bytes"
	"context"
	"fmt"

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
