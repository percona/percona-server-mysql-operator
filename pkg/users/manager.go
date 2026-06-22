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
	DropUser(ctx context.Context, user *apiv1.User) error
	RevokeStaleGrants(ctx context.Context, user *apiv1.User) error
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
		return nil, errors.Wrap(err, "cannot connect to any host")
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
		query = append(query, (fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", db)))
	}

	for _, host := range user.Hosts {
		query = append(query, (fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY ?", escapeIdentifier(user.Name), escapeIdentifier(host))))

		if len(user.Grants) > 0 {
			withGrantOption := "WITH GRANT OPTION"
			grants := strings.Join(user.Grants, ",")
			if len(user.DBs) > 0 {
				for _, db := range user.DBs {
					q := fmt.Sprintf("GRANT %s ON %s.* TO '%s'@'%s' %s", grants, db, escapeIdentifier(user.Name), escapeIdentifier(host), withGrantOption)
					query = append(query, q)
				}
			} else {
				q := fmt.Sprintf("GRANT %s ON *.* TO '%s'@'%s' %s", grants, escapeIdentifier(user.Name), escapeIdentifier(host), withGrantOption)
				query = append(query, q)
			}
		}
	}

	for _, q := range query {
		_, err := m.db.ExecContext(ctx, q, pass)
		if err != nil {
			return errors.Wrapf(err, "upsert user %s", user.Name)
		}
	}

	return nil
}

func dropUserStatements(name string, hosts []string) []string {
	var query []string
	for _, host := range hosts {
		query = append(query, fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s'", escapeIdentifier(name), escapeIdentifier(host)))
	}
	return query
}

func (m *manager) DropUser(ctx context.Context, user *apiv1.User) error {
	hosts := user.Hosts
	if len(hosts) == 0 {
		// No hosts specified: drop every account that exists for this name so
		// removing a user from the CR cleans up all of its host accounts.
		discovered, err := m.userHosts(ctx, user.Name)
		if err != nil {
			return errors.Wrapf(err, "get hosts for user %s", user.Name)
		}
		hosts = discovered
	}

	for _, q := range dropUserStatements(user.Name, hosts) {
		if _, err := m.db.ExecContext(ctx, q); err != nil {
			return errors.Wrapf(err, "drop user %s", user.Name)
		}
	}
	return nil
}

// userHosts returns every host for which an account with the given name exists.
func (m *manager) userHosts(ctx context.Context, name string) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT Host FROM mysql.user WHERE User = ?", name)
	if err != nil {
		return nil, errors.Wrap(err, "query user hosts")
	}
	defer rows.Close()

	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, errors.Wrap(err, "scan user host")
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate user hosts")
	}

	return hosts, nil
}
