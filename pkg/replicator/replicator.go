package replicator

import (
	"database/sql"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
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
	ChangeReplicationSource(host, replicaPass string, port int32) error
	StartReplication(host, replicaPass string, port int32) error
	StopReplication() error
	ResetReplication() error
	ReplicationStatus() (ReplicationStatus, string, error)
	EnableSuperReadonly() error
	IsReadonly() (bool, error)
	ReportHost() (string, error)
	Close() error
	CloneInProgress() (bool, error)
	NeedsClone(donor string, port int32) (bool, error)
	Clone(donor, user, pass string, port int32) error
	IsReplica() (bool, error)
	DumbQuery() error
	SetSemiSyncSource(enabled bool) error
	SetSemiSyncSize(size int) error
	GetGlobal(variable string) (interface{}, error)
	SetGlobal(variable, value interface{}) error
	ChangeGroupReplicationPassword(replicaPass string) error
	StartGroupReplication(password string) error
	StopGroupReplication() error
	GetGroupReplicationPrimary() (string, error)
	GetGroupReplicationReplicas() ([]string, error)
	GetMemberState(host string) (MemberState, error)
	GetGroupReplicationMembers() ([]string, error)
	CheckIfDatabaseExists(name string) (bool, error)
	CheckIfInPrimaryPartition() (bool, error)
}

type dbImpl struct{ db *sql.DB }

func NewReplicator(user apiv1alpha1.SystemUser, pass, host string, port int32) (Replicator, error) {
	config := mysql.NewConfig()

	config.User = string(user)
	config.Passwd = pass
	config.Net = "tcp"
	config.Addr = fmt.Sprintf("%s:%d", host, port)
	config.DBName = "performance_schema"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           "20s",
		"readTimeout":       "20s",
		"writeTimeout":      "20s",
		"tls":               "preferred",
	}

	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping database")
	}

	return &dbImpl{db}, nil
}

func (d *dbImpl) ChangeReplicationSource(host, replicaPass string, port int32) error {
	// TODO: Make retries configurable
	_, err := d.db.Exec(`
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

func (d *dbImpl) StartReplication(host, replicaPass string, port int32) error {
	if err := d.ChangeReplicationSource(host, replicaPass, port); err != nil {
		return errors.Wrap(err, "change replication source")
	}

	_, err := d.db.Exec("START REPLICA")
	return errors.Wrap(err, "start replication")
}

func (d *dbImpl) StopReplication() error {
	_, err := d.db.Exec("STOP REPLICA")
	return errors.Wrap(err, "stop replication")
}

func (d *dbImpl) ResetReplication() error {
	_, err := d.db.Exec("RESET REPLICA ALL")
	return errors.Wrap(err, "reset replication")
}

func (d *dbImpl) ReplicationStatus() (ReplicationStatus, string, error) {
	row := d.db.QueryRow(`
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

func (d *dbImpl) IsReplica() (bool, error) {
	status, _, err := d.ReplicationStatus()
	return status == ReplicationStatusActive, errors.Wrap(err, "get replication status")
}

func (d *dbImpl) EnableSuperReadonly() error {
	_, err := d.db.Exec("SET GLOBAL SUPER_READ_ONLY=1")
	return errors.Wrap(err, "set global super_read_only param to 1")
}

func (d *dbImpl) IsReadonly() (bool, error) {
	var readonly int
	err := d.db.QueryRow("select @@read_only and @@super_read_only").Scan(&readonly)
	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

func (d *dbImpl) ReportHost() (string, error) {
	var reportHost string
	err := d.db.QueryRow("select @@report_host").Scan(&reportHost)
	return reportHost, errors.Wrap(err, "select report_host param")
}

func (d *dbImpl) Close() error {
	return d.db.Close()
}

func (d *dbImpl) CloneInProgress() (bool, error) {
	rows, err := d.db.Query("SELECT STATE FROM clone_status")
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

func (d *dbImpl) NeedsClone(donor string, port int32) (bool, error) {
	rows, err := d.db.Query("SELECT SOURCE, STATE FROM clone_status")
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

func (d *dbImpl) Clone(donor, user, pass string, port int32) error {
	_, err := d.db.Exec("SET GLOBAL clone_valid_donor_list=?", fmt.Sprintf("%s:%d", donor, port))
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	_, err = d.db.Exec("CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?", user, donor, port, pass)

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

func (d *dbImpl) DumbQuery() error {
	_, err := d.db.Exec("SELECT 1")
	return errors.Wrap(err, "SELECT 1")
}

func (d *dbImpl) SetSemiSyncSource(enabled bool) error {
	_, err := d.db.Exec("SET GLOBAL rpl_semi_sync_master_enabled=?", enabled)
	return errors.Wrap(err, "set rpl_semi_sync_master_enabled")
}

func (d *dbImpl) SetSemiSyncSize(size int) error {
	_, err := d.db.Exec("SET GLOBAL rpl_semi_sync_master_wait_for_slave_count=?", size)
	return errors.Wrap(err, "set rpl_semi_sync_master_wait_for_slave_count")
}

func (d *dbImpl) GetGlobal(variable string) (interface{}, error) {
	// TODO: check how to do this without being vulnerable to injection
	var value interface{}
	err := d.db.QueryRow(fmt.Sprintf("SELECT @@%s", variable)).Scan(&value)
	return value, errors.Wrapf(err, "SELECT @@%s", variable)
}

func (d *dbImpl) SetGlobal(variable, value interface{}) error {
	_, err := d.db.Exec(fmt.Sprintf("SET GLOBAL %s=?", variable), value)
	return errors.Wrapf(err, "SET GLOBAL %s=%s", variable, value)
}

func (d *dbImpl) StartGroupReplication(password string) error {
	_, err := d.db.Exec("START GROUP_REPLICATION USER=?, PASSWORD=?", apiv1alpha1.UserReplication, password)

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

func (d *dbImpl) StopGroupReplication() error {
	_, err := d.db.Exec("STOP GROUP_REPLICATION")
	return errors.Wrap(err, "stop group replication")
}

func (d *dbImpl) ChangeGroupReplicationPassword(replicaPass string) error {
	_, err := d.db.Exec(`
            CHANGE REPLICATION SOURCE TO
                SOURCE_USER=?,
                SOURCE_PASSWORD=?
            FOR CHANNEL 'group_replication_recovery'
        `, apiv1alpha1.UserReplication, replicaPass)
	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	return nil
}

func (d *dbImpl) GetGroupReplicationPrimary() (string, error) {
	var host string

	err := d.db.QueryRow("SELECT MEMBER_HOST FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'").Scan(&host)
	if err != nil {
		return "", errors.Wrap(err, "query primary member")
	}

	return host, nil
}

func (d *dbImpl) GetGroupReplicationReplicas() ([]string, error) {
	replicas := make([]string, 0)

	rows, err := d.db.Query("SELECT MEMBER_HOST FROM replication_group_members WHERE MEMBER_ROLE='SECONDARY' AND MEMBER_STATE='ONLINE'")
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

func (d *dbImpl) GetMemberState(host string) (MemberState, error) {
	var state MemberState

	err := d.db.QueryRow("SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_HOST=?", host).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MemberStateOffline, nil
		}
		return MemberStateError, errors.Wrap(err, "query member state")
	}

	return state, nil
}

func (d *dbImpl) GetGroupReplicationMembers() ([]string, error) {
	members := make([]string, 0)

	rows, err := d.db.Query("SELECT MEMBER_HOST FROM replication_group_members")
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

func (d *dbImpl) CheckIfDatabaseExists(name string) (bool, error) {
	var db string

	err := d.db.QueryRow("SHOW DATABASES LIKE ?", name).Scan(&db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (d *dbImpl) CheckIfInPrimaryPartition() (bool, error) {
	var in bool

	err := d.db.QueryRow(`
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
