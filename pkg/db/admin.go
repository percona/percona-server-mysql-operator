package db

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

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

func (m *AdminManager) SetGlobalVariables(ctx context.Context, keyValues ...string) error {
	if len(keyValues) == 0 {
		return nil
	}

	if len(keyValues)%2 != 0 {
		return fmt.Errorf("keyValues must be in pairs")
	}

	kv := make(map[string]string)
	for i := 0; i < len(keyValues); i += 2 {
		key := keyValues[i]
		value := keyValues[i+1]
		kv[key] = value
	}

	assignments := make([]string, 0, len(kv))
	for _, key := range slices.Sorted(maps.Keys(kv)) {
		assignments = append(assignments, fmt.Sprintf("GLOBAL %s=%s", key, kv[key]))
	}

	var errb, outb bytes.Buffer
	cmd := "SET " + strings.Join(assignments, ", ")
	err := m.db.exec(ctx, cmd, &outb, &errb)
	if err != nil {
		return err
	}
	return nil
}
