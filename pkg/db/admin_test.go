package db

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	clientcmdmock "github.com/percona/percona-server-mysql-operator/pkg/clientcmd/mock"
)

func TestAdminManagerGetGlobalVariable(t *testing.T) {
	const (
		pass = "configurator-password"
		host = "cluster1-mysql-0.cluster1-mysql.ns"
	)

	pod := &corev1.Pod{Name: "cluster1-mysql-0", Namespace: "ns"}

	// headerless, so stdout carries the value alone
	mysqlCmd := func(stmt string) []string {
		return []string{
			"mysql",
			"--database", "performance_schema",
			"-p" + pass,
			"-u", string(apiv1.UserConfigurator),
			"-h", host,
			"--skip-column-names",
			"-e", stmt,
		}
	}

	tests := map[string]struct {
		key        string
		stmt       string // statement the read is expected to run; empty means it never reaches mysql
		stdout     string
		execErr    error
		want       string
		wantErrMsg string
	}{
		"value is read back": {
			key:    "innodb_buffer_pool_chunk_size",
			stmt:   "SELECT @@GLOBAL.innodb_buffer_pool_chunk_size",
			stdout: "268435456\n",
			want:   "268435456",
		},
		// the config carries the prefix, mysqld does not know it
		"loose prefix is stripped": {
			key:    "loose_group_replication_consistency",
			stmt:   "SELECT @@GLOBAL.group_replication_consistency",
			stdout: "EVENTUAL\n",
			want:   "EVENTUAL",
		},
		"empty value": {
			key:    "init_connect",
			stmt:   "SELECT @@GLOBAL.init_connect",
			stdout: "\n",
			want:   "",
		},
		// a name reaches mysql unquoted, so anything that is not one is refused
		"injected statement is refused": {
			key:        "max_connections; DROP DATABASE mysql",
			wantErrMsg: `invalid global variable name: "max_connections; DROP DATABASE mysql"`,
		},
		"empty name is refused": {
			key:        "",
			wantErrMsg: `invalid global variable name: ""`,
		},
		"mysql error is returned": {
			key:        "max_connections",
			stmt:       "SELECT @@GLOBAL.max_connections",
			execErr:    errors.New("command terminated with exit code 1"),
			wantErrMsg: "command terminated with exit code 1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// the mock fails the test on any call not set up here
			cliCmd := clientcmdmock.NewClient(t)
			if tt.stmt != "" {
				cliCmd.On("Exec",
					mock.Anything,
					pod,
					"mysql",
					mysqlCmd(tt.stmt),
					mock.Anything, // stdin
					mock.Anything, // stdout
					mock.Anything, // stderr
					false,         // tty
				).Return(tt.execErr).Once().Run(func(args mock.Arguments) {
					_, _ = args.Get(5).(io.Writer).Write([]byte(tt.stdout))
				})
			}

			m := NewAdminManager(pod, cliCmd, apiv1.UserConfigurator, pass, host)

			got, err := m.GetGlobalVariable(t.Context(), tt.key)
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
