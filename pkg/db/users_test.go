package db

import (
	"io"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	clientcmdmock "github.com/percona/percona-server-mysql-operator/pkg/clientcmd/mock"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func TestUserManagerUpdateUserPasswords(t *testing.T) {
	const (
		pass = "operator-old"
		host = "cluster1-mysql-0.cluster1-mysql.ns"
	)

	pod := &corev1.Pod{Name: "cluster1-mysql-0", Namespace: "ns"}

	mysqlCmd := func(stmt string, args ...string) []string {
		cmd := []string{
			"mysql",
			"--database", "performance_schema",
			"-p" + pass,
			"-u", string(apiv1.UserOperator),
			"-h", host,
		}
		cmd = append(cmd, args...)
		return append(cmd, "-e", stmt)
	}
	accountsQuery := "SELECT CONCAT(User, '@', Host) FROM mysql.user WHERE JSON_CONTAINS_PATH(User_attributes, 'one', '$.additional_password')"

	users := []mysql.User{
		{Username: apiv1.UserOperator, Hosts: []string{"%"}, Password: "operator-new"},
		{Username: apiv1.UserRoot, Hosts: []string{"%", "localhost"}, Password: "root-new"},
	}

	tests := map[string]struct {
		accounts string
		stmts    []string
	}{
		"no account holds a secondary password": {
			stmts: []string{
				"ALTER USER 'operator'@'%' IDENTIFIED BY 'operator-new' RETAIN CURRENT PASSWORD",
				"ALTER USER 'root'@'%' IDENTIFIED BY 'root-new' RETAIN CURRENT PASSWORD",
				"ALTER USER 'root'@'localhost' IDENTIFIED BY 'root-new' RETAIN CURRENT PASSWORD",
				"FLUSH PRIVILEGES",
			},
		},
		// a repeated attempt must keep the password the first attempt retained
		"accounts that already hold a secondary password keep it": {
			accounts: "operator@%\nroot@localhost\n",
			stmts: []string{
				"ALTER USER 'operator'@'%' IDENTIFIED BY 'operator-new'",
				"ALTER USER 'root'@'%' IDENTIFIED BY 'root-new' RETAIN CURRENT PASSWORD",
				"ALTER USER 'root'@'localhost' IDENTIFIED BY 'root-new'",
				"FLUSH PRIVILEGES",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// the mock fails the test on any call not set up here
			cliCmd := clientcmdmock.NewClient(t)
			cliCmd.On("Exec", mock.Anything, pod, "mysql", mysqlCmd(accountsQuery, "--skip-column-names"),
				mock.Anything, mock.Anything, mock.Anything, false,
			).Return(nil).Once().Run(func(args mock.Arguments) {
				_, _ = args.Get(5).(io.Writer).Write([]byte(tt.accounts))
			})
			for _, stmt := range tt.stmts {
				cliCmd.On("Exec", mock.Anything, pod, "mysql", mysqlCmd(stmt),
					mock.Anything, mock.Anything, mock.Anything, false,
				).Return(nil).Once()
			}

			m := NewUserManager(pod, cliCmd, apiv1.UserOperator, pass, host)
			require.NoError(t, m.UpdateUserPasswords(t.Context(), users))
		})
	}
}
