package mysql

import (
	"database/sql"
	"fmt"
	"strings"

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

type Database struct {
	db *sql.DB
}

func NewConnection(user, pass, host string, port int32) (*Database, error) {
	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/mysql?interpolateParams=true", user, pass, host, port)
	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping database")
	}

	return &Database{db: db}, nil
}

func (d *Database) StartReplication(host, replicaPass string, port int32) error {
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
        `, v2.USERS_SECRET_KEY_REPLICATION, replicaPass, host, port)
	if err != nil {
		return errors.Wrap(err, "change replication source to")
	}

	_, err = d.db.Exec("START REPLICA")
	return errors.Wrap(err, "start replication")
}

func (d *Database) ReplicationStatus() (ReplicationStatus, string, error) {
	rows, err := d.db.Query("SHOW REPLICA STATUS")
	if err != nil {
		if strings.HasSuffix(err.Error(), "does not exist.") || errors.Is(err, sql.ErrNoRows) {
			return ReplicationStatusNotInitiated, "", nil
		}
		return ReplicationStatusError, "", errors.Wrap(err, "get current replica status")
	}

	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return ReplicationStatusError, "", errors.Wrap(err, "get columns")
	}
	vals := make([]interface{}, len(cols))
	for i := range cols {
		vals[i] = new(sql.RawBytes)
	}

	for rows.Next() {
		err = rows.Scan(vals...)
		if err != nil {
			return ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
		}
	}

	SourceHost := string(*vals[1].(*sql.RawBytes))
	IORunning := string(*vals[10].(*sql.RawBytes))
	SQLRunning := string(*vals[11].(*sql.RawBytes))
	if IORunning == "Yes" && SQLRunning == "Yes" {
		return ReplicationStatusActive, SourceHost, nil
	}

	return ReplicationStatusNotInitiated, "", nil
}

func (p *Database) EnableReadonly() error {
	_, err := p.db.Exec("SET GLOBAL READ_ONLY=1")
	return errors.Wrap(err, "set global read_only param to 1")
}

func (d *Database) IsReadonly() (bool, error) {
	var readonly int
	err := d.db.QueryRow("select @@read_only").Scan(&readonly)
	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) CloneInProgress() (bool, error) {
	rows, err := d.db.Query("SELECT STATE FROM performance_schema.clone_status")
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

func (d *Database) NeedsClone(donor string, port int32) (bool, error) {
	rows, err := d.db.Query("SELECT SOURCE, STATE FROM performance_schema.clone_status")
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

func (d *Database) Clone(donor, user, pass string, port int32) error {
	_, err := d.db.Exec("SET GLOBAL clone_valid_donor_list=?", fmt.Sprintf("%s:%d", donor, port))
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	_, err = d.db.Exec("CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?", user, donor, port, pass)
	if err != nil {
		return errors.Wrap(err, "clone instance")
	}

	return nil
}
