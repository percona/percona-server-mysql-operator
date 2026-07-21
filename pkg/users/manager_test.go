package users

import (
	"testing"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestUpsertUserQueries(t *testing.T) {
	tests := []struct {
		name     string
		user     *apiv1.User
		expected []string
	}{
		{
			name: "no grants, no with grant option",
			user: &apiv1.User{
				Name:  "app",
				Hosts: []string{"%"},
			},
			expected: []string{
				"CREATE USER IF NOT EXISTS 'app'@'%' IDENTIFIED BY ?",
			},
		},
		{
			name: "grants without with grant option",
			user: &apiv1.User{
				Name:   "app",
				Hosts:  []string{"%"},
				Grants: []string{"SELECT", "INSERT"},
			},
			expected: []string{
				"CREATE USER IF NOT EXISTS 'app'@'%' IDENTIFIED BY ?",
				"GRANT SELECT,INSERT ON *.* TO 'app'@'%'",
			},
		},
		{
			name: "grants with with grant option",
			user: &apiv1.User{
				Name:            "app",
				Hosts:           []string{"%"},
				Grants:          []string{"SELECT", "INSERT"},
				WithGrantOption: true,
			},
			expected: []string{
				"CREATE USER IF NOT EXISTS 'app'@'%' IDENTIFIED BY ?",
				"GRANT SELECT,INSERT ON *.* TO 'app'@'%' WITH GRANT OPTION",
			},
		},
		{
			name: "grants scoped to dbs with with grant option",
			user: &apiv1.User{
				Name:            "app",
				Hosts:           []string{"%"},
				DBs:             []string{"db1", "db2"},
				Grants:          []string{"SELECT"},
				WithGrantOption: true,
			},
			expected: []string{
				"CREATE DATABASE IF NOT EXISTS `db1`",
				"CREATE DATABASE IF NOT EXISTS `db2`",
				"CREATE USER IF NOT EXISTS 'app'@'%' IDENTIFIED BY ?",
				"GRANT SELECT ON `db1`.* TO 'app'@'%' WITH GRANT OPTION",
				"GRANT SELECT ON `db2`.* TO 'app'@'%' WITH GRANT OPTION",
			},
		},
		{
			name: "grants scoped to dbs without with grant option",
			user: &apiv1.User{
				Name:   "app",
				Hosts:  []string{"%"},
				DBs:    []string{"db1"},
				Grants: []string{"SELECT"},
			},
			expected: []string{
				"CREATE DATABASE IF NOT EXISTS `db1`",
				"CREATE USER IF NOT EXISTS 'app'@'%' IDENTIFIED BY ?",
				"GRANT SELECT ON `db1`.* TO 'app'@'%'",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, upsertUserQueries(tt.user))
		})
	}
}
