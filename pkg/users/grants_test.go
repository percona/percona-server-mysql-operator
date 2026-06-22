package users

import (
	"testing"

	"github.com/stretchr/testify/assert"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestStaleGrantRevokes(t *testing.T) {
	tests := map[string]struct {
		user   *apiv1.User
		host   string
		actual []grant
		want   []string
	}{
		"no drift returns nothing": {
			user: &apiv1.User{Name: "u", Grants: []string{"SELECT"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "USAGE", grantable: false},
				{db: "", privilege: "SELECT", grantable: true},
			},
			want: nil,
		},
		"usage is never revoked": {
			user: &apiv1.User{Name: "u"},
			host: "%",
			actual: []grant{
				{db: "", privilege: "USAGE", grantable: false},
			},
			want: nil,
		},
		"global privilege removed from grants": {
			user: &apiv1.User{Name: "u", Grants: []string{"SELECT"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "USAGE", grantable: false},
				{db: "", privilege: "SELECT", grantable: true},
				{db: "", privilege: "INSERT", grantable: true},
			},
			// SELECT still desired so grant option on *.* stays.
			want: []string{
				"REVOKE INSERT ON *.* FROM 'u'@'%'",
			},
		},
		"multiple global privileges removed are grouped and sorted": {
			user: &apiv1.User{Name: "u", Grants: []string{"SELECT"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "UPDATE", grantable: true},
				{db: "", privilege: "INSERT", grantable: true},
				{db: "", privilege: "SELECT", grantable: true},
			},
			want: []string{
				"REVOKE INSERT, UPDATE ON *.* FROM 'u'@'%'",
			},
		},
		"database removed revokes all privileges and grant option on it": {
			user: &apiv1.User{Name: "u", Grants: []string{"SELECT"}, DBs: []string{"db1"}},
			host: "%",
			actual: []grant{
				{db: "db1", privilege: "SELECT", grantable: true},
				{db: "db2", privilege: "SELECT", grantable: true},
			},
			want: []string{
				"REVOKE SELECT ON db2.* FROM 'u'@'%'",
				"REVOKE GRANT OPTION ON db2.* FROM 'u'@'%'",
			},
		},
		"all privileges desired keeps every privilege on a desired scope": {
			user: &apiv1.User{Name: "u", Grants: []string{"ALL"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "USAGE", grantable: false},
				{db: "", privilege: "SELECT", grantable: true},
				{db: "", privilege: "INSERT", grantable: true},
				{db: "", privilege: "DELETE", grantable: true},
			},
			want: nil,
		},
		"all privileges on a removed scope are revoked": {
			user: &apiv1.User{Name: "u", Grants: []string{"ALL"}, DBs: []string{"db1"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "SELECT", grantable: true},
				{db: "", privilege: "INSERT", grantable: true},
				{db: "db1", privilege: "SELECT", grantable: true},
				{db: "db1", privilege: "INSERT", grantable: true},
			},
			want: []string{
				"REVOKE INSERT, SELECT ON *.* FROM 'u'@'%'",
				"REVOKE GRANT OPTION ON *.* FROM 'u'@'%'",
			},
		},
		"switching from global to database scope revokes the global grant": {
			user: &apiv1.User{Name: "u", Grants: []string{"SELECT"}, DBs: []string{"db1"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "SELECT", grantable: true},
				{db: "db1", privilege: "SELECT", grantable: true},
			},
			want: []string{
				"REVOKE SELECT ON *.* FROM 'u'@'%'",
				"REVOKE GRANT OPTION ON *.* FROM 'u'@'%'",
			},
		},
		"removing all grants revokes everything on a still-present database": {
			user: &apiv1.User{Name: "u", DBs: []string{"db1"}},
			host: "%",
			actual: []grant{
				{db: "db1", privilege: "SELECT", grantable: true},
				{db: "db1", privilege: "INSERT", grantable: true},
			},
			want: []string{
				"REVOKE INSERT, SELECT ON db1.* FROM 'u'@'%'",
				"REVOKE GRANT OPTION ON db1.* FROM 'u'@'%'",
			},
		},
		"privilege names are matched case-insensitively": {
			user: &apiv1.User{Name: "u", Grants: []string{"select", "insert"}},
			host: "%",
			actual: []grant{
				{db: "", privilege: "SELECT", grantable: true},
				{db: "", privilege: "INSERT", grantable: true},
				{db: "", privilege: "UPDATE", grantable: true},
			},
			want: []string{
				"REVOKE UPDATE ON *.* FROM 'u'@'%'",
			},
		},
		"host is interpolated into the statement": {
			user: &apiv1.User{Name: "u", Grants: []string{"SELECT"}},
			host: "10.0.0.1",
			actual: []grant{
				{db: "", privilege: "SELECT", grantable: true},
				{db: "", privilege: "INSERT", grantable: true},
			},
			want: []string{
				"REVOKE INSERT ON *.* FROM 'u'@'10.0.0.1'",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := staleGrantRevokes(tt.user, tt.host, tt.actual)
			assert.Equal(t, tt.want, got)
		})
	}
}
