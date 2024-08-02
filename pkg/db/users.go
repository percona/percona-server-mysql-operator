package db

import (
	"bytes"
	"context"
	"fmt"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"strings"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type UserManager struct {
	db *db
}

func NewUserManager(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1alpha1.SystemUser, pass, host string) *UserManager {
	return &UserManager{db: newDB(pod, cliCmd, user, pass, host)}
}

// UpdateUserPasswords updates user passwords but retains the current password using Dual Password feature of MySQL 8
func (m *UserManager) UpdateUserPasswords(ctx context.Context, users []mysql.User) error {
	for _, user := range users {
		for _, host := range user.Hosts {
			q := fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s' RETAIN CURRENT PASSWORD", user.Username, host, escapePass(user.Password))
			var errb, outb bytes.Buffer
			err := m.db.exec(ctx, q, &outb, &errb)
			if err != nil {
				return errors.Wrap(err, "alter user")
			}
		}
	}

	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, "FLUSH PRIVILEGES", &outb, &errb)
	if err != nil {
		return errors.Wrap(err, "flush privileges")
	}

	return nil
}

// DiscardOldPasswords discards old passwords of givens users
func (m *UserManager) DiscardOldPasswords(ctx context.Context, users []mysql.User) error {
	for _, user := range users {
		for _, host := range user.Hosts {
			q := fmt.Sprintf("ALTER USER '%s'@'%s' DISCARD OLD PASSWORD", user.Username, host)
			var errb, outb bytes.Buffer
			err := m.db.exec(ctx, q, &outb, &errb)
			if err != nil {
				return errors.Wrap(err, "discard old password")
			}
		}
	}

	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, "FLUSH PRIVILEGES", &outb, &errb)
	if err != nil {
		return errors.Wrap(err, "flush privileges")
	}

	return nil
}

func escapePass(pass string) string {
	s := strings.ReplaceAll(pass, `'`, `\'`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `\`, `\\`)
	return s
}
