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

// CreateClusterSetUser creates the clusterset user with the same grants as
// ps-entrypoint.sh. The entrypoint creates users only on initial datadir
// initialization, so clusters created by operator versions older than 1.2.0
// don't have this user. It is safe to call if the user already exists.
func (m *UserManager) CreateClusterSetUser(ctx context.Context, pass string, status *apiv1.PerconaServerMySQLStatus) error {
	dynamicPrivs := "BACKUP_ADMIN, CLONE_ADMIN, CONNECTION_ADMIN, GROUP_REPLICATION_ADMIN, REPLICATION_SLAVE_ADMIN, REPLICATION_APPLIER, PERSIST_RO_VARIABLES_ADMIN, ROLE_ADMIN, SESSION_VARIABLES_ADMIN, SYSTEM_VARIABLES_ADMIN"
	if status.CompareMySQLVersion("9.7") >= 0 {
		dynamicPrivs += ", TRANSACTION_GTID_TAG"
	}
	queries := []string{
		fmt.Sprintf("CREATE USER IF NOT EXISTS 'clusterset'@'%%' IDENTIFIED BY '%s' PASSWORD EXPIRE NEVER", escapePass(pass)),
		"GRANT SELECT, RELOAD, SHUTDOWN, PROCESS, FILE, REPLICATION SLAVE, REPLICATION CLIENT, CREATE USER, EXECUTE ON *.* TO 'clusterset'@'%' WITH GRANT OPTION",
		fmt.Sprintf("GRANT %s ON *.* TO 'clusterset'@'%%' WITH GRANT OPTION", dynamicPrivs),
		"GRANT INSERT, UPDATE, DELETE ON mysql.* TO 'clusterset'@'%' WITH GRANT OPTION",
		"GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata.* TO 'clusterset'@'%' WITH GRANT OPTION",
		"GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata_bkp.* TO 'clusterset'@'%' WITH GRANT OPTION",
		"GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata_previous.* TO 'clusterset'@'%' WITH GRANT OPTION",
	}

	var errb, outb bytes.Buffer
	if err := m.db.exec(ctx, strings.Join(queries, "; "), &outb, &errb); err != nil {
		return errors.Wrap(err, "create clusterset user")
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
