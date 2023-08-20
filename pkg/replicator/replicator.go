package replicator

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

const DefaultChannelName = ""

type ReplicationStatus int8

var ErrRestartAfterClone error = errors.New("Error 3707: Restart server failed (mysqld is not managed by supervisor process).")

const (
	ReplicationStatusActive ReplicationStatus = iota
	ReplicationStatusError
	ReplicationStatusNotInitiated
)

type MemberState string

const (
	MemberStateOnline      MemberState = "ONLINE"
	MemberStateRecovering  MemberState = "RECOVERING"
	MemberStateOffline     MemberState = "OFFLINE"
	MemberStateError       MemberState = "ERROR"
	MemberStateUnreachable MemberState = "UNREACHABLE"
)

var ErrGroupReplicationNotReady = errors.New("Error 3092: The server is not configured properly to be an active member of the group.")

type Replicator interface {
	ChangeReplicationSource(ctx context.Context, host, replicaPass string, port int32) error
	StartReplication(ctx context.Context, host, replicaPass string, port int32) error
	StopReplication(ctx context.Context) error
	ResetReplication(ctx context.Context) error
	ReplicationStatus(ctx context.Context) (ReplicationStatus, string, error)
	EnableSuperReadonly(ctx context.Context) error
	IsReadonly(ctx context.Context) (bool, error)
	ReportHost(ctx context.Context) (string, error)
	Close() error
	CloneInProgress(ctx context.Context) (bool, error)
	NeedsClone(ctx context.Context, donor string, port int32) (bool, error)
	Clone(ctx context.Context, donor, user, pass string, port int32) error
	IsReplica(ctx context.Context) (bool, error)
	DumbQuery(ctx context.Context) error
	GetGlobal(ctx context.Context, variable string) (interface{}, error)
	SetGlobal(ctx context.Context, variable, value interface{}) error
	StartGroupReplication(ctx context.Context, password string) error
	StopGroupReplication(ctx context.Context) error
	GetGroupReplicationPrimary(ctx context.Context) (string, error)
	GetGroupReplicationReplicas(ctx context.Context) ([]string, error)
	GetMemberState(ctx context.Context, host string) (MemberState, error)
	GetGroupReplicationMembers(ctx context.Context) ([]string, error)
	CheckIfDatabaseExists(ctx context.Context, name string) (bool, error)
	CheckIfInPrimaryPartition(ctx context.Context) (bool, error)
	CheckIfPrimaryUnreachable(ctx context.Context) (bool, error)
}

type dbImpl struct{ db *sql.DB }

// NewReplicator creates a new Replicator instance to manage replication operations.
func NewReplicator(ctx context.Context, user apiv1alpha1.SystemUser, pass, host string, port int32) (Replicator, error) {
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
		return nil, errors.Wrap(err, "ping database")
	}

	return &dbImpl{db}, nil
}

// ChangeReplicationSource changes the replication source for the database.
func (d *dbImpl) ChangeReplicationSource(ctx context.Context, host, replicaPass string, port int32) error {
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

	return nil
}

// StartReplication starts the database replication process.
func (d *dbImpl) StartReplication(ctx context.Context, host, replicaPass string, port int32) error {
	if err := d.ChangeReplicationSource(ctx, host, replicaPass, port); err != nil {
		return errors.Wrap(err, "change replication source")
	}

	_, err := d.db.ExecContext(ctx, "START REPLICA")
	return errors.Wrap(err, "start replication")
}

// StopReplication stops the database replication process.
func (d *dbImpl) StopReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "STOP REPLICA")
	return errors.Wrap(err, "stop replication")
}

// ResetReplication resets the replication configuration.
func (d *dbImpl) ResetReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "RESET REPLICA ALL")
	return errors.Wrap(err, "reset replication")
}

// ReplicationStatus retrieves the replication status of the database.
func (d *dbImpl) ReplicationStatus(ctx context.Context) (ReplicationStatus, string, error) {
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
        `, DefaultChannelName)

	var ioState, sqlState, host string
	if err := row.Scan(&ioState, &sqlState, &host); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReplicationStatusNotInitiated, "", nil
		}
		return ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	}

	if ioState == "ON" && sqlState == "ON" {
		return ReplicationStatusActive, host, nil
	}

	return ReplicationStatusNotInitiated, "", nil
}

// IsReplica checks if the database is acting as a replica.
func (d *dbImpl) IsReplica(ctx context.Context) (bool, error) {
	status, _, err := d.ReplicationStatus(ctx)
	return status == ReplicationStatusActive, errors.Wrap(err, "get replication status")
}

// EnableSuperReadonly sets the global super_read_only parameter to 1.
func (d *dbImpl) EnableSuperReadonly(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL SUPER_READ_ONLY=1")
	return errors.Wrap(err, "set global super_read_only param to 1")
}

// IsReadonly checks if the database is in read-only mode.
func (d *dbImpl) IsReadonly(ctx context.Context) (bool, error) {
	var readonly int
	err := d.db.QueryRowContext(ctx, "select @@read_only and @@super_read_only").Scan(&readonly)
	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

// ReportHost retrieves the report_host parameter value.
func (d *dbImpl) ReportHost(ctx context.Context) (string, error) {
	var reportHost string
	err := d.db.QueryRowContext(ctx, "select @@report_host").Scan(&reportHost)
	return reportHost, errors.Wrap(err, "select report_host param")
}

// Close closes the database connection.
func (d *dbImpl) Close() error {
	return d.db.Close()
}

// CloneInProgress checks if a database clone operation is in progress.
func (d *dbImpl) CloneInProgress(ctx context.Context) (bool, error) {
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

// NeedsClone checks if the database needs to be cloned from a donor.
func (d *dbImpl) NeedsClone(ctx context.Context, donor string, port int32) (bool, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT SOURCE, STATE FROM clone_status")
	if err != nil {
		return false, errors.Wrap(err, "fetch clone status")
	}
	defer rows.Close()

	for rows.Next() {
		var source, state string
		if err := rows.Scan(&source, &state); err != nil {
			return false, errors.Wrap(err, "scan rows")
		}
		if source == fmt.Sprintf("%s:%d", donor, port) && state == "Completed" {
			return false, nil
		}
	}

	return true, nil
}

// Clone initiates the cloning process for the database.
func (d *dbImpl) Clone(ctx context.Context, donor, user, pass string, port int32) error {
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

// DumbQuery executes a simple query to check database connectivity.
func (d *dbImpl) DumbQuery(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SELECT 1")
	return errors.Wrap(err, "SELECT 1")
}

// GetGlobal retrieves the value of a global database variable.
func (d *dbImpl) GetGlobal(ctx context.Context, variable string) (interface{}, error) {
	// TODO: check how to do this without being vulnerable to injection
	var value interface{}
	err := d.db.QueryRowContext(ctx, fmt.Sprintf("SELECT @@%s", variable)).Scan(&value)
	return value, errors.Wrapf(err, "SELECT @@%s", variable)
}

// SetGlobal sets the value of a global database variable.
func (d *dbImpl) SetGlobal(ctx context.Context, variable, value interface{}) error {
	_, err := d.db.ExecContext(ctx, fmt.Sprintf("SET GLOBAL %s=?", variable), value)
	return errors.Wrapf(err, "SET GLOBAL %s=%s", variable, value)
}

// StartGroupReplication starts the group replication process.
func (d *dbImpl) StartGroupReplication(ctx context.Context, password string) error {
	_, err := d.db.ExecContext(ctx, "START GROUP_REPLICATION USER=?, PASSWORD=?", apiv1alpha1.UserReplication, password)

	mErr, ok := err.(*mysql.MySQLError)
	if !ok {
		return errors.Wrap(err, "start group replication")
	}

	// Error 3092: The server is not configured properly to be an active member of the group.
	if mErr.Number == uint16(3092) {
		return ErrGroupReplicationNotReady
	}

	return errors.Wrap(err, "start group replication")
}

// StopGroupReplication stops the group replication process.
func (d *dbImpl) StopGroupReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "STOP GROUP_REPLICATION")
	return errors.Wrap(err, "stop group replication")
}

// GetGroupReplicationPrimary retrieves the primary host in the group replication.
func (d *dbImpl) GetGroupReplicationPrimary(ctx context.Context) (string, error) {
	var host string

	err := d.db.QueryRowContext(ctx, "SELECT MEMBER_HOST FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'").Scan(&host)
	if err != nil {
		return "", errors.Wrap(err, "query primary member")
	}

	return host, nil
}

// GetGroupReplicationReplicas retrieves the list of secondary hosts in the group replication.
func (d *dbImpl) GetGroupReplicationReplicas(ctx context.Context) ([]string, error) {
	replicas := make([]string, 0)

	rows, err := d.db.QueryContext(ctx, "SELECT MEMBER_HOST FROM replication_group_members WHERE MEMBER_ROLE='SECONDARY' AND MEMBER_STATE='ONLINE'")
	if err != nil {
		return nil, errors.Wrap(err, "query replicas")
	}
	defer rows.Close()

	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, errors.Wrap(err, "scan rows")
		}

		replicas = append(replicas, host)
	}

	return replicas, nil
}

// GetMemberState retrieves the replication state of a member in the group replication.
func (d *dbImpl) GetMemberState(ctx context.Context, host string) (MemberState, error) {
	var state MemberState

	err := d.db.QueryRowContext(ctx, "SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_HOST=?", host).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MemberStateOffline, nil
		}
		return MemberStateError, errors.Wrap(err, "query member state")
	}

	return state, nil
}

// GetGroupReplicationMembers retrieves the list of all members in the group replication.
func (d *dbImpl) GetGroupReplicationMembers(ctx context.Context) ([]string, error) {
	members := make([]string, 0)

	rows, err := d.db.QueryContext(ctx, "SELECT MEMBER_HOST FROM replication_group_members")
	if err != nil {
		return nil, errors.Wrap(err, "query members")
	}
	defer rows.Close()

	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, errors.Wrap(err, "scan rows")
		}

		members = append(members, host)
	}

	return members, nil
}

// CheckIfDatabaseExists checks if a database with the given name exists.
func (d *dbImpl) CheckIfDatabaseExists(ctx context.Context, name string) (bool, error) {
	var db string

	err := d.db.QueryRowContext(ctx, "SHOW DATABASES LIKE ?", name).Scan(&db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// CheckIfInPrimaryPartition checks if the database is in the primary partition.
func (d *dbImpl) CheckIfInPrimaryPartition(ctx context.Context) (bool, error) {
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

// CheckIfPrimaryUnreachable checks if the primary member in group replication is unreachable.
func (d *dbImpl) CheckIfPrimaryUnreachable(ctx context.Context) (bool, error) {
	var state string

	err := d.db.QueryRowContext(ctx, `
	SELECT
		MEMBER_STATE
	FROM
		performance_schema.replication_group_members
	WHERE
		MEMBER_ROLE = 'PRIMARY'
	`).Scan(&state)
	if err != nil {
		return false, err
	}

	return state == string(innodbcluster.MemberStateUnreachable), nil
}
