package mysql

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/pkg/errors"
)

const DefaultChannelName = "default-channel"

type ReplicationStatus int8

const (
	ReplicationStatusActive ReplicationStatus = iota
	ReplicationStatusError
	ReplicationStatusNotInitiated
)

type Database struct {
	db *sql.DB
}

type ReplicationChannel struct {
	Name   string
	Host   string
	Port   int
	Weight int
}

func NewConnection(user, pass, host string, port int32) (Database, error) {
	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/mysql?interpolateParams=true", user, pass, host, port)
	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return Database{}, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return Database{}, errors.Wrap(err, "ping database")
	}

	return Database{db: db}, nil
}

func (d *Database) AddReplicationSource(channel ReplicationChannel) error {
	rows, err := d.db.Query("SELECT 1 FROM replication_asynchronous_connection_failover WHERE channel_name = ?", DefaultChannelName)
	if err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return errors.Wrap(err, "check existing channel")
	}
	defer rows.Close()

	_, err = d.db.Exec(
		"SELECT asynchronous_connection_failover_add_source(?, ?, ?, null, ?)",
		channel.Name, channel.Host, channel.Port, channel.Weight,
	)
	return errors.Wrap(err, "add replication source")
}

func (d *Database) StartReplication(channel ReplicationChannel, replicaPass string) error {
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
                FOR CHANNEL ?
        `, v2.USERS_SECRET_KEY_REPLICATION, replicaPass, channel.Host, channel.Port, channel.Name)
	if err != nil {
		return errors.Wrapf(err, "change replication source for channel %s", channel.Name)
	}

	_, err = d.db.Exec("START REPLICA FOR CHANNEL ?", channel.Name)
	return errors.Wrapf(err, "start replication for channel %s", channel.Name)
}

func (p *Database) ReplicationStatus(channelName string) (ReplicationStatus, error) {
	rows, err := p.db.Query(`SHOW REPLICA STATUS FOR CHANNEL ?`, channelName)
	if err != nil {
		if strings.HasSuffix(err.Error(), "does not exist.") || errors.Is(err, sql.ErrNoRows) {
			return ReplicationStatusNotInitiated, nil
		}
		return ReplicationStatusError, errors.Wrap(err, "get current replica status")
	}

	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return ReplicationStatusError, errors.Wrap(err, "get columns")
	}
	vals := make([]interface{}, len(cols))
	for i := range cols {
		vals[i] = new(sql.RawBytes)
	}

	for rows.Next() {
		err = rows.Scan(vals...)
		if err != nil {
			return ReplicationStatusError, errors.Wrap(err, "scan replication status")
		}
	}

	IORunning := string(*vals[10].(*sql.RawBytes))
	SQLRunning := string(*vals[11].(*sql.RawBytes))
	LastErrNo := string(*vals[18].(*sql.RawBytes))
	if IORunning == "Yes" && SQLRunning == "Yes" {
		return ReplicationStatusActive, nil
	}

	if IORunning == "No" && SQLRunning == "No" && LastErrNo == "0" {
		return ReplicationStatusNotInitiated, nil
	}

	return ReplicationStatusError, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}
