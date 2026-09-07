package db

import (
	"database/sql"
	"errors"
	"math"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestDBParams_setDefaults(t *testing.T) {
	tests := []struct {
		name     string
		params   DBParams
		expected DBParams
	}{
		{
			name: "all defaults",
			params: DBParams{
				User: apiv1.UserOperator,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                33062,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    3,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "custom values",
			params: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 300,
				SourceConnectRetry:  60,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 300,
				SourceRetryCount:    3,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "zero port gets default",
			params: DBParams{
				User: apiv1.UserOperator,
				Port: 0,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                33062,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    3,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "zero clone timeout gets default",
			params: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 0,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    3,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "zero source retry count gets default",
			params: DBParams{
				User:             apiv1.UserOperator,
				Port:             3306,
				SourceRetryCount: 0,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    3,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "custom source retry count",
			params: DBParams{
				User:             apiv1.UserOperator,
				Port:             3306,
				SourceRetryCount: 7,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    7,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "zero source connect retry gets default",
			params: DBParams{
				User:               apiv1.UserOperator,
				Port:               3306,
				SourceConnectRetry: 0,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    3,
				SourceConnectRetry:  60,
			},
		},
		{
			name: "custom source connect retry",
			params: DBParams{
				User:               apiv1.UserOperator,
				Port:               3306,
				SourceConnectRetry: 120,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
				SourceRetryCount:    3,
				SourceConnectRetry:  120,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.params.setDefaults()
			assert.Equal(t, tt.expected, tt.params)
		})
	}
}

func TestDBParams_DSN(t *testing.T) {
	params := DBParams{
		User:                apiv1.UserOperator,
		Pass:                "testpass",
		Host:                "localhost",
		Port:                3306,
		ReadTimeoutSeconds:  31,
		CloneTimeoutSeconds: 300,
	}

	dsn := params.DSN()

	assert.Contains(t, dsn, "operator")
	assert.Contains(t, dsn, "testpass")
	assert.Contains(t, dsn, "localhost:3306")
	assert.Contains(t, dsn, "performance_schema")
	assert.Contains(t, dsn, "readTimeout=31s")
	assert.Contains(t, dsn, "timeout=10s")
	assert.Contains(t, dsn, "writeTimeout=31s")
}

func newMockDB(t *testing.T) (*DB, sqlmock.Sqlmock) {
	t.Helper()

	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	t.Cleanup(func() {
		assert.NoError(t, mock.ExpectationsWereMet())
		mockDB.Close() //nolint:errcheck
	})

	return &DB{db: mockDB}, mock
}

// replicaStatusColumns lists the SHOW REPLICA STATUS columns the failover code
// actually reads.
var replicaStatusColumns = []string{
	"Source_Host", "Source_Log_File", "Read_Source_Log_Pos",
	"Relay_Log_File", "Relay_Log_Pos", "Executed_Gtid_Set",
	"Last_SQL_Error", "Replica_SQL_Running", "Replica_SQL_Running_State",
}

func TestShowReplicaStatus(t *testing.T) {
	t.Run("returns the row as a column map", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(
			sqlmock.NewRows(replicaStatusColumns).AddRow(
				"mysql-1.mysql", "binlog.000004", "157",
				"relay-bin.000002", "4711", "uuid:1-10",
				"", "Yes", "Replica has read all relay log; waiting for more updates"))

		status, err := d.ShowReplicaStatus(t.Context())

		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"Source_Host":               "mysql-1.mysql",
			"Source_Log_File":           "binlog.000004",
			"Read_Source_Log_Pos":       "157",
			"Relay_Log_File":            "relay-bin.000002",
			"Relay_Log_Pos":             "4711",
			"Executed_Gtid_Set":         "uuid:1-10",
			"Last_SQL_Error":            "",
			"Replica_SQL_Running":       "Yes",
			"Replica_SQL_Running_State": "Replica has read all relay log; waiting for more updates",
		}, status)
	})

	t.Run("NULL columns become empty strings", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(
			sqlmock.NewRows([]string{"Source_Host", "Last_SQL_Error", "Executed_Gtid_Set"}).
				AddRow("mysql-1.mysql", nil, nil))

		status, err := d.ShowReplicaStatus(t.Context())

		require.NoError(t, err)
		assert.Equal(t, "", status["Last_SQL_Error"])
		assert.Equal(t, "", status["Executed_Gtid_Set"])
	})

	t.Run("preserves long and multi-line values", func(t *testing.T) {
		gtid := "uuid-a:1-100000,\nuuid-b:1-200000"
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(
			sqlmock.NewRows([]string{"Executed_Gtid_Set"}).AddRow(gtid))

		status, err := d.ShowReplicaStatus(t.Context())

		require.NoError(t, err)
		assert.Equal(t, gtid, status["Executed_Gtid_Set"])
	})

	t.Run("not a replica", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(
			sqlmock.NewRows(replicaStatusColumns))

		status, err := d.ShowReplicaStatus(t.Context())

		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.Nil(t, status)
	})

	t.Run("query fails", func(t *testing.T) {
		queryErr := errors.New("connection refused")
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnError(queryErr)

		_, err := d.ShowReplicaStatus(t.Context())

		require.ErrorIs(t, err, queryErr)
	})

	t.Run("row error", func(t *testing.T) {
		rowErr := errors.New("lost connection mid-read")
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(
			sqlmock.NewRows([]string{"Source_Host"}).AddRow("mysql-1.mysql").RowError(0, rowErr))

		_, err := d.ShowReplicaStatus(t.Context())

		require.Error(t, err)
	})
}

func TestGetSourceLogPos(t *testing.T) {
	fullRow := func(readPos, relayPos string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"Source_Host", "Source_Log_File", "Read_Source_Log_Pos",
			"Relay_Log_File", "Relay_Log_Pos", "Executed_Gtid_Set",
		}).AddRow("mysql-1.mysql", "binlog.000004", readPos, "relay-bin.000002", relayPos, "uuid:1-10")
	}

	tests := []struct {
		name     string
		rows     *sqlmock.Rows
		expected ReplicaPosition
		wantErr  string
	}{
		{
			name: "all fields parsed",
			rows: fullRow("157", "4711"),
			expected: ReplicaPosition{
				SourceHost:   "mysql-1.mysql",
				SourceLog:    "binlog.000004",
				SourcePos:    157,
				RelayLog:     "relay-bin.000002",
				RelayPos:     4711,
				GTIDExecuted: "uuid:1-10",
			},
		},
		{
			name: "max uint64 position is not truncated",
			rows: fullRow("18446744073709551615", "4711"),
			expected: ReplicaPosition{
				SourceHost:   "mysql-1.mysql",
				SourceLog:    "binlog.000004",
				SourcePos:    math.MaxUint64,
				RelayLog:     "relay-bin.000002",
				RelayPos:     4711,
				GTIDExecuted: "uuid:1-10",
			},
		},
		{
			name:    "unparseable Read_Source_Log_Pos",
			rows:    fullRow("abc", "4711"),
			wantErr: "parse Read_Source_Log_Pos",
		},
		{
			name:    "negative Read_Source_Log_Pos",
			rows:    fullRow("-1", "4711"),
			wantErr: "parse Read_Source_Log_Pos",
		},
		{
			name:    "unparseable Relay_Log_Pos",
			rows:    fullRow("157", ""),
			wantErr: "parse Relay_Log_Pos",
		},
		{
			// A MySQL variant that doesn't report the column must error rather
			// than silently claim position 0.
			name: "Read_Source_Log_Pos column absent",
			rows: sqlmock.NewRows([]string{"Source_Host", "Relay_Log_Pos"}).
				AddRow("mysql-1.mysql", "4711"),
			wantErr: "parse Read_Source_Log_Pos",
		},
		{
			name:    "not a replica",
			rows:    sqlmock.NewRows([]string{"Source_Host"}),
			wantErr: "show replica status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, mock := newMockDB(t)
			mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(tt.rows)

			positions, err := d.GetSourceLogPos(t.Context())

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, positions)
		})
	}

	t.Run("no rows keeps sql.ErrNoRows in the chain", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(sqlmock.NewRows([]string{"Source_Host"}))

		_, err := d.GetSourceLogPos(t.Context())

		require.ErrorIs(t, err, sql.ErrNoRows)
	})
}

func TestRelayLogPaths(t *testing.T) {
	const query = "select @@relay_log_basename, @@relay_log_index"

	tests := []struct {
		name         string
		basename     any
		index        any
		wantBasename string
		wantIndex    string
		wantErr      string
	}{
		{
			name:         "both set",
			basename:     "/var/lib/mysql/relay-bin",
			index:        "/var/log/mysql/relay-bin.index",
			wantBasename: "/var/lib/mysql/relay-bin",
			wantIndex:    "/var/log/mysql/relay-bin.index",
		},
		{
			name:         "empty index is derived from the basename",
			basename:     "/var/lib/mysql/relay-bin",
			index:        "",
			wantBasename: "/var/lib/mysql/relay-bin",
			wantIndex:    "/var/lib/mysql/relay-bin.index",
		},
		{
			name:         "NULL index is derived from the basename",
			basename:     "/var/lib/mysql/relay-bin",
			index:        nil,
			wantBasename: "/var/lib/mysql/relay-bin",
			wantIndex:    "/var/lib/mysql/relay-bin.index",
		},
		{
			name:     "empty basename",
			basename: "",
			index:    "/var/lib/mysql/relay-bin.index",
			wantErr:  "relay_log_basename is empty",
		},
		{
			name:     "NULL basename",
			basename: nil,
			index:    nil,
			wantErr:  "relay_log_basename is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, mock := newMockDB(t)
			mock.ExpectQuery(regexp.QuoteMeta(query)).WillReturnRows(
				sqlmock.NewRows([]string{"@@relay_log_basename", "@@relay_log_index"}).
					AddRow(tt.basename, tt.index))

			basename, index, err := d.RelayLogPaths(t.Context())

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, basename)
				assert.Empty(t, index)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantBasename, basename)
			assert.Equal(t, tt.wantIndex, index)
		})
	}

	t.Run("query fails", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta(query)).WillReturnError(errors.New("connection refused"))

		basename, index, err := d.RelayLogPaths(t.Context())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "select relay log params")
		assert.Empty(t, basename)
		assert.Empty(t, index)
	})

	t.Run("relay logging is off", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta(query)).WillReturnRows(
			sqlmock.NewRows([]string{"@@relay_log_basename", "@@relay_log_index"}))

		_, _, err := d.RelayLogPaths(t.Context())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "select relay log params")
	})
}

func TestStartSQLThread(t *testing.T) {
	t.Run("executes START REPLICA SQL_THREAD", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta("START REPLICA SQL_THREAD")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		require.NoError(t, d.StartSQLThread(t.Context()))
	})

	t.Run("exec fails", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta("START REPLICA SQL_THREAD")).
			WillReturnError(errors.New("server has gone away"))

		err := d.StartSQLThread(t.Context())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start SQL_THREAD")
	})
}

func TestFlushRelayLogs(t *testing.T) {
	t.Run("executes FLUSH RELAY LOGS", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta("FLUSH RELAY LOGS")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		require.NoError(t, d.FlushRelayLogs(t.Context()))
	})

	t.Run("exec fails", func(t *testing.T) {
		d, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta("FLUSH RELAY LOGS")).
			WillReturnError(errors.New("server has gone away"))

		err := d.FlushRelayLogs(t.Context())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "flush relay logs")
	})
}
