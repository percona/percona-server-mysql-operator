package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

const defaultChannelName = ""

var ErrRestartAfterClone = errors.New("Error 3707: Restart server failed (mysqld is not managed by supervisor process).")

type ReplicationStatus int8

type DB struct {
	db *sql.DB
}

func NewDatabase(ctx context.Context, user apiv1alpha1.SystemUser, pass, host string, port int32) (*DB, error) {
	config := mysql.NewConfig()

	config.User = string(user)
	config.Passwd = pass
	config.Net = "tcp"
	config.Addr = fmt.Sprintf("%s:%d", host, port)
	config.DBName = "performance_schema"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           "10s",
		"readTimeout":       "10s",
		"writeTimeout":      "10s",
		"tls":               "preferred",
	}

	db, err := sql.Open("mysql", config.FormatDSN())
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

func (d *DB) ReplicationStatus(ctx context.Context) (replicator.ReplicationStatus, string, error) {
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
			return replicator.ReplicationStatusNotInitiated, "", nil
		}
		return replicator.ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	}

	if ioState == "ON" && sqlState == "ON" {
		return replicator.ReplicationStatusActive, host, nil
	}

	return replicator.ReplicationStatusNotInitiated, "", nil
}

func (d *DB) IsReplica(ctx context.Context) (bool, error) {
	status, _, err := d.ReplicationStatus(ctx)
	return status == replicator.ReplicationStatusActive, errors.Wrap(err, "get replication status")
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

func (d *DB) Clone(ctx context.Context, donor, user, pass string, port int32) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL clone_valid_donor_list=?", fmt.Sprintf("%s:%d", donor, port))
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	_, err = d.db.ExecContext(ctx, "CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?", user, donor, port, pass)

	mErr, ok := err.(*mysql.MySQLError)
	if !ok {
		return errors.Wrap(err, "clone instance")
	}

	// Error 3707: Restart server failed (mysqld is not managed by supervisor process).
	if mErr.Number == uint16(3707) {
		return ErrRestartAfterClone
	}

	return nil
}

func (d *DB) DumbQuery(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SELECT 1")
	return errors.Wrap(err, "SELECT 1")
}

func (d *DB) GetMemberState(ctx context.Context, host string) (replicator.MemberState, error) {
	var state replicator.MemberState

	err := d.db.QueryRowContext(ctx, "SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_HOST=?", host).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return replicator.MemberStateOffline, nil
		}
		return replicator.MemberStateError, errors.Wrap(err, "query member state")
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
