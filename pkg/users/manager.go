package users

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/pkg/errors"
)

type manager struct {
	db *sql.DB
}

type Manager interface {
	UpsertUser(ctx context.Context, user *apiv1.User, pass string) error
	AlterUser(ctx context.Context, user *apiv1.User, pass string) error
	Close() error
}

func NewManager(
	addr, user, pass string, timeout int32,
) (*manager, error) {
	timeoutStr := fmt.Sprintf("%ds", timeout)
	config := mysql.NewConfig()
	config.User = user
	config.Passwd = pass
	config.Net = "tcp"
	config.Addr = addr
	config.DBName = "mysql"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           timeoutStr,
		"readTimeout":       timeoutStr,
		"writeTimeout":      timeoutStr,
		"tls":               "preferred",
	}

	mysqlDB, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, errors.Wrap(err, "open mysql connection")
	}

	return &manager{
		db: mysqlDB,
	}, nil
}

func (m *manager) Close() error {
	return m.db.Close()
}

func escapeIdentifier(identifier string) string {
	return strings.ReplaceAll(identifier, "'", "''")
}

func (m *manager) AlterUser(ctx context.Context, user *apiv1.User, pass string) error {
	query := make([]string, 0)
	if len(user.Hosts) > 0 {
		for _, host := range user.Hosts {
			query = append(query, fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY ?", escapeIdentifier(user.Name), escapeIdentifier(host)))
		}
	} else {
		query = append(query, fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY ?", escapeIdentifier(user.Name)))
	}

	for _, q := range query {
		_, err := m.db.ExecContext(ctx, q, pass)
		if err != nil {
			return errors.Wrapf(err, "alter user %s", user.Name)
		}
	}

	return nil
}

func (m *manager) UpsertUser(ctx context.Context, user *apiv1.User, pass string) error {
	query := make([]string, 0)

	for _, db := range user.DBs {
		query = append(query, (fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", db)))
	}

	for _, host := range user.Hosts {
		query = append(query, (fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY ?", escapeIdentifier(user.Name), escapeIdentifier(host))))

		if len(user.Grants) > 0 {
			withGrantOption := "WITH GRANT OPTION"
			grants := strings.Join(user.Grants, ",")
			if len(user.DBs) > 0 {
				for _, db := range user.DBs {
					q := fmt.Sprintf("GRANT %s ON `%s`.* TO '%s'@'%s' %s", grants, db, escapeIdentifier(user.Name), escapeIdentifier(host), withGrantOption)
					query = append(query, q)
				}
			} else {
				q := fmt.Sprintf("GRANT %s ON *.* TO '%s'@'%s' %s", grants, escapeIdentifier(user.Name), escapeIdentifier(host), withGrantOption)
				query = append(query, q)
			}
		}
	}

	for _, q := range query {
		args := []any{}
		if strings.Contains(q, "?") {
			args = append(args, pass)
		}

		_, err := m.db.ExecContext(ctx, q, args...)
		if err != nil {
			return errors.Wrapf(err, "exec %s for user %s", q, user.Name)
		}
		continue

	}

	return nil
}
