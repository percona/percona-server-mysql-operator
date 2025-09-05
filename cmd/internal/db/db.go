package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	defs "github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const defaultChannelName = ""

var ErrRestartAfterClone = errors.New("Error 3707: Restart server failed (mysqld is not managed by supervisor process).")

type ReplicationStatus int8

type DB struct {
	db *sql.DB
}

type DBParams struct {
	User apiv1alpha1.SystemUser
	Pass string
	Host string
	Port int32

	ReadTimeoutSeconds  uint32
	CloneTimeoutSeconds uint32
}

func (p *DBParams) setDefaults() {
	if p.Port == 0 {
		p.Port = defs.DefaultAdminPort
	}

	if p.ReadTimeoutSeconds == 0 {
		p.ReadTimeoutSeconds = 3600 // 1 hour for long-running operations like clone
	}

	if p.CloneTimeoutSeconds == 0 {
		p.CloneTimeoutSeconds = 3600 // 1 hour for clone operations (large databases can take time)
	}
}

func (p *DBParams) DSN() string {
	p.setDefaults()

	config := mysql.NewConfig()

	config.User = string(p.User)
	config.Passwd = p.Pass
	config.Net = "tcp"
	config.Addr = fmt.Sprintf("%s:%d", p.Host, p.Port)
	config.DBName = "performance_schema"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           "10s",
		"readTimeout":       fmt.Sprintf("%ds", p.ReadTimeoutSeconds),
		"writeTimeout":      fmt.Sprintf("%ds", p.ReadTimeoutSeconds), // Use same timeout for write operations
		"tls":               "preferred",
	}

	return config.FormatDSN()
}

func NewDatabase(ctx context.Context, params DBParams) (*DB, error) {
	db, err := sql.Open("mysql", params.DSN())
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Wrap(err, "ping DB")
	}

	return &DB{db}, nil
}

func (d *DB) StartReplication(ctx context.Context, host, replicaPass string, port int32) error {
	// TODO: Make retries configurable
	_, err := d.db.ExecContext(ctx, `
            CHANGE REPLICATION SOURCE TO
                SOURCE_USER=?,
                SOURCE_PASSWORD=?,
                SOURCE_HOST=?,
                SOURCE_PORT=?,
                SOURCE_SSL=1,
                SOURCE_CONNECTION_AUTO_FAILOVER=1,
                SOURCE_AUTO_POSITION=1,
                SOURCE_RETRY_COUNT=3,
                SOURCE_CONNECT_RETRY=60
        `, apiv1alpha1.UserReplication, replicaPass, host, port)
	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	_, err = d.db.ExecContext(ctx, "START REPLICA")
	return errors.Wrap(err, "start replication")
}

func (d *DB) StopReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "STOP REPLICA")
	return errors.Wrap(err, "stop replication")
}

func (d *DB) ResetReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "RESET REPLICA ALL")
	return errors.Wrap(err, "reset replication")
}

func (d *DB) ReplicationStatus(ctx context.Context) (db.ReplicationStatus, string, error) {
	row := d.db.QueryRowContext(ctx, `
        SELECT
	    connection_status.SERVICE_STATE,
	    applier_status.SERVICE_STATE,
            HOST
        FROM replication_connection_status connection_status
        JOIN replication_connection_configuration connection_configuration
            ON connection_status.channel_name = connection_configuration.channel_name
        JOIN replication_applier_status applier_status
            ON connection_status.channel_name = applier_status.channel_name
        WHERE connection_status.channel_name = ?
        `, defaultChannelName)

	var ioState, sqlState, host string
	if err := row.Scan(&ioState, &sqlState, &host); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.ReplicationStatusNotInitiated, "", nil
		}
		return db.ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	}

	if ioState == "ON" && sqlState == "ON" {
		return db.ReplicationStatusActive, host, nil
	}

	return db.ReplicationStatusNotInitiated, "", nil
}

func (d *DB) IsReplica(ctx context.Context) (bool, error) {
	status, _, err := d.ReplicationStatus(ctx)
	return status == db.ReplicationStatusActive, errors.Wrap(err, "get replication status")
}

func (d *DB) DisableSuperReadonly(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL SUPER_READ_ONLY=0")
	return errors.Wrap(err, "set global super_read_only param to 0")
}

func (d *DB) IsReadonly(ctx context.Context) (bool, error) {
	var readonly int
	err := d.db.QueryRowContext(ctx, "select @@read_only and @@super_read_only").Scan(&readonly)
	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

func (d *DB) ReportHost(ctx context.Context) (string, error) {
	var reportHost string
	err := d.db.QueryRowContext(ctx, "select @@report_host").Scan(&reportHost)
	return reportHost, errors.Wrap(err, "select report_host param")
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) CloneInProgress(ctx context.Context) (bool, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT STATE FROM clone_status")
	if err != nil {
		return false, errors.Wrap(err, "fetch clone status")
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return false, errors.Wrap(err, "scan rows")
		}

		if state != "Completed" && state != "Failed" {
			return true, nil
		}
	}

	return false, nil
}

// getCloneStatus returns the current clone status
func (d *DB) getCloneStatus(ctx context.Context) (string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT STATE FROM clone_status")
	if err != nil {
		return "", errors.Wrap(err, "fetch clone status")
	}
	defer func() { _ = rows.Close() }()

	if rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return "", errors.Wrap(err, "scan rows")
		}
		return state, nil
	}

	return "", errors.New("no clone status found")
}

// getCloneStatusDetails returns detailed clone status information for debugging
func (d *DB) getCloneStatusDetails(ctx context.Context) (map[string]interface{}, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT STATE, BEGIN_TIME, END_TIME, SOURCE, DESTINATION, ERROR_NO, ERROR_MESSAGE FROM clone_status")
	if err != nil {
		return nil, errors.Wrap(err, "fetch clone status details")
	}
	defer func() { _ = rows.Close() }()

	details := make(map[string]interface{})
	if rows.Next() {
		var state, beginTime, endTime, source, destination, errorNo, errorMessage sql.NullString
		if err := rows.Scan(&state, &beginTime, &endTime, &source, &destination, &errorNo, &errorMessage); err != nil {
			return nil, errors.Wrap(err, "scan clone status details")
		}

		details["state"] = state.String
		details["begin_time"] = beginTime.String
		details["end_time"] = endTime.String
		details["source"] = source.String
		details["destination"] = destination.String
		details["error_no"] = errorNo.String
		details["error_message"] = errorMessage.String
	}

	return details, nil
}

func (d *DB) Clone(ctx context.Context, donor, user, pass string, port int32, cloneTimeoutSeconds uint32) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL clone_valid_donor_list=?", fmt.Sprintf("%s:%d", donor, port))
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	// Use the original context if no timeout is specified, otherwise create a timeout context
	var cloneCtx context.Context
	var cancel context.CancelFunc

	if cloneTimeoutSeconds > 0 {
		cloneCtx, cancel = context.WithTimeout(ctx, time.Duration(cloneTimeoutSeconds)*time.Second)
		defer cancel()
	} else {
		cloneCtx = ctx // Use original context without timeout (original behavior)
	}

	_, err = d.db.ExecContext(cloneCtx, "CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?", user, donor, port, pass)

	// Check for MySQL errors first
	if err != nil {
		mErr, ok := err.(*mysql.MySQLError)
		if !ok {
			return errors.Wrap(err, "clone instance")
		}

		// Error 3707: Restart server failed (mysqld is not managed by supervisor process).
		if mErr.Number == uint16(3707) {
			return ErrRestartAfterClone
		}

		// Error 1317: Query execution was interrupted (often due to timeout)
		if mErr.Number == uint16(1317) {
			return errors.Wrapf(err, "clone instance was interrupted (likely due to timeout) - MySQL error %d: %s", mErr.Number, mErr.Message)
		}

		// Error 1160: Got an error reading communication packets
		if mErr.Number == uint16(1160) {
			return errors.Wrapf(err, "clone instance communication error - MySQL error %d: %s", mErr.Number, mErr.Message)
		}

		// Return other MySQL errors
		return errors.Wrapf(err, "clone instance failed with MySQL error %d: %s", mErr.Number, mErr.Message)
	}

	// If no error from the clone command, check the clone status to ensure it completed successfully
	cloneStatus, err := d.getCloneStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "check clone status after operation")
	}

	if cloneStatus != "Completed" {
		// Get detailed clone status for better error reporting
		details, detailErr := d.getCloneStatusDetails(ctx)
		if detailErr != nil {
			return errors.Errorf("clone operation did not complete successfully, status: %s (failed to get details: %v)", cloneStatus, detailErr)
		}

		errorMsg := fmt.Sprintf("clone operation did not complete successfully, status: %s", cloneStatus)
		if errorNo, ok := details["error_no"].(string); ok && errorNo != "" {
			errorMsg += fmt.Sprintf(", error_no: %s", errorNo)
		}
		if errorMessage, ok := details["error_message"].(string); ok && errorMessage != "" {
			errorMsg += fmt.Sprintf(", error_message: %s", errorMessage)
		}

		return errors.New(errorMsg)
	}

	return nil
}

func (d *DB) DumbQuery(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SELECT 1")
	return errors.Wrap(err, "SELECT 1")
}

func (d *DB) GetMemberState(ctx context.Context, host string) (db.MemberState, error) {
	var state db.MemberState

	err := d.db.QueryRowContext(ctx, "SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_HOST=?", host).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.MemberStateOffline, nil
		}
		return db.MemberStateError, errors.Wrap(err, "query member state")
	}

	return state, nil
}

func (d *DB) CheckIfInPrimaryPartition(ctx context.Context) (bool, error) {
	var in bool

	err := d.db.QueryRowContext(ctx, `
	SELECT
		MEMBER_STATE = 'ONLINE'
		AND (
			(
				SELECT
					COUNT(*)
				FROM
					performance_schema.replication_group_members
				WHERE
					MEMBER_STATE NOT IN ('ONLINE', 'RECOVERING')
			) >= (
				(
					SELECT
						COUNT(*)
					FROM
						performance_schema.replication_group_members
				) / 2
			) = 0
		)
	FROM
		performance_schema.replication_group_members
		JOIN performance_schema.replication_group_member_stats USING(member_id)
	WHERE
		member_id = @@global.server_uuid;
	`).Scan(&in)
	if err != nil {
		return false, err
	}

	return in, nil
}

func (d *DB) EnableSuperReadonly(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL SUPER_READ_ONLY=1")
	return errors.Wrap(err, "set global super_read_only param to 1")
}
