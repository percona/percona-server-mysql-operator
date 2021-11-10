package mysql

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/pkg/errors"
)

const DefaultChannelName = ""

type ReplicationStatus int8

const (
	ReplicationStatusActive ReplicationStatus = iota
	ReplicationStatusError
	ReplicationStatusNotInitiated
)

type Replicator interface {
	StartReplication(host, replicaPass string, port int32) error
	ReplicationStatus() (ReplicationStatus, string, error)
	EnableReadonly() error
	IsReadonly() (bool, error)
	ReportHost() (string, error)
	Close() error
	CloneInProgress() (bool, error)
	NeedsClone(donor string, port int32) (bool, error)
	Clone(donor, user, pass string, port int32) error
}

type dbImpl sql.DB

func NewReplicator(user, pass, host string, port int32) (Replicator, error) {
	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/performance_schema?interpolateParams=true", user, pass, host, port)
	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping database")
	}

	return (*dbImpl)(db), nil
}

func (d *dbImpl) StartReplication(host, replicaPass string, port int32) error {
	// TODO: Make retries configurable
	_, err := (*sql.DB)(d).Exec(`
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
        `, v2.USERS_SECRET_KEY_REPLICATION, replicaPass, host, port)
	if err != nil {
		return errors.Wrap(err, "change replication source to")
	}

	_, err = (*sql.DB)(d).Exec("START REPLICA")
	return errors.Wrap(err, "start replication")
}

func (d *dbImpl) ReplicationStatus() (ReplicationStatus, string, error) {
	row := (*sql.DB)(d).QueryRow(`
        SELECT
            replication_connection_status.SERVICE_STATE,
            replication_applier_status.SERVICE_STATE,
            HOST
        FROM replication_connection_status
        JOIN replication_connection_configuration
            ON replication_connection_status.channel_name = replication_connection_configuration.channel_name
        JOIN replication_applier_status
            ON replication_connection_status.channel_name = replication_applier_status.channel_name
        WHERE replication_connection_status.channel_name = ?
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

func (d *dbImpl) EnableReadonly() error {
	_, err := (*sql.DB)(d).Exec("SET GLOBAL READ_ONLY=1")
	return errors.Wrap(err, "set global read_only param to 1")
}

func (d *dbImpl) IsReadonly() (bool, error) {
	var readonly int
	err := (*sql.DB)(d).QueryRow("select @@read_only").Scan(&readonly)
	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

func (d *dbImpl) ReportHost() (string, error) {
	var reportHost string
	err := (*sql.DB)(d).QueryRow("select @@report_host").Scan(&reportHost)
	return reportHost, errors.Wrap(err, "select report_host param")
}

func (d *dbImpl) Close() error {
	return (*sql.DB)(d).Close()
}

func (d *dbImpl) CloneInProgress() (bool, error) {
	rows, err := (*sql.DB)(d).Query("SELECT STATE FROM clone_status")
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
	rows, err := (*sql.DB)(d).Query("SELECT SOURCE, STATE FROM clone_status")
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
	_, err := (*sql.DB)(d).Exec("SET GLOBAL clone_valid_donor_list=?", fmt.Sprintf("%s:%d", donor, port))
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	_, err = (*sql.DB)(d).Exec("CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?", user, donor, port, pass)
	if err != nil {
		return errors.Wrap(err, "clone instance")
	}

	return nil
}
