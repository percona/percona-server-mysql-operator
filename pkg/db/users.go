package db

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type UserManager struct {
	db *db
}

func NewUserManager(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1.SystemUser, pass, host string) *UserManager {
	return &UserManager{db: newDB(pod, cliCmd, user, pass, host)}
}

// UpdateUserPasswords updates user passwords but retains the current password using Dual Password feature of MySQL 8.
// An account that already has a secondary password keeps it: it was retained by an earlier attempt of this change,
// and retaining again would replace it with the new password, leaving nothing that still accepts the old one.
func (m *UserManager) UpdateUserPasswords(ctx context.Context, users []mysql.User) error {
	retained, err := m.accountsWithSecondaryPassword(ctx)
	if err != nil {
		return errors.Wrap(err, "get accounts with a secondary password")
	}

	for _, user := range users {
		for _, host := range user.Hosts {
			q := fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY %s", user.Username, host, mysql.QuoteLiteral(user.Password))
			if _, ok := retained[string(user.Username)+"@"+host]; !ok {
				q += " RETAIN CURRENT PASSWORD"
			}
			var errb, outb bytes.Buffer
			err := m.db.exec(ctx, q, &outb, &errb)
			if err != nil {
				return errors.Wrap(err, "alter user")
			}
		}
	}

	return nil
}

// accountsWithSecondaryPassword returns the 'user@host' accounts that hold a secondary password.
func (m *UserManager) accountsWithSecondaryPassword(ctx context.Context) (map[string]struct{}, error) {
	var errb, outb bytes.Buffer
	q := "SELECT CONCAT(User, '@', Host) FROM mysql.user WHERE JSON_CONTAINS_PATH(User_attributes, 'one', '$.additional_password')"
	if err := m.db.execValues(ctx, q, &outb, &errb); err != nil {
		return nil, err
	}

	accounts := make(map[string]struct{})
	for line := range strings.Lines(outb.String()) {
		if line = strings.TrimSpace(line); line != "" {
			accounts[line] = struct{}{}
		}
	}

	return accounts, nil
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

	return nil
}
