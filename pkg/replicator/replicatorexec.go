package replicator

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

type dbImplExec struct {
	client *clientcmd.Client
	pod    *corev1.Pod
	user   apiv1alpha1.SystemUser
	pass   string
	host   string
}

func NewReplicatorExec(pod *corev1.Pod, user apiv1alpha1.SystemUser, pass, host string) (Replicator, error) {
	// config := mysql.NewConfig()

	// config.User = string(user)
	// config.Passwd = pass
	// config.Net = "tcp"
	// config.Addr = fmt.Sprintf("%s:%d", host, port)
	// config.DBName = "performance_schema"
	// config.Params = map[string]string{
	// 	"interpolateParams": "true",
	// 	"timeout":           "20s",
	// 	"readTimeout":       "20s",
	// 	"writeTimeout":      "20s",
	// 	"tls":               "preferred",
	// }

	// db, err := sql.Open("mysql", config.FormatDSN())
	// if err != nil {
	// 	return nil, errors.Wrap(err, "connect to MySQL")
	// }

	// if err := db.Ping(); err != nil {
	// 	return nil, errors.Wrap(err, "ping database")
	// }
	c, err := clientcmd.NewClient()
	if err != nil {
		return nil, err
	}

	return &dbImplExec{client: c, pod: pod, user: user, pass: pass, host: host}, nil
}

func (d *dbImplExec) exec(query string, stdout, stderr io.Writer) error {
	cmd := []string{"mysql", "--database", "performance_schema", fmt.Sprintf("-p%s", d.pass), "-u", string(d.user), "-h", d.host, "-e", query}

	println("exec() ================")
	println(query)
	println("----------------------------------")
	println(fmt.Sprintf("%v", cmd))
	println("exec() ================")

	err := d.client.Exec(context.TODO(), d.pod, "mysql", cmd, nil, stdout, stderr, false)
	if err != nil {
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, stdout, stderr)
	}

	return nil
}

func (d *dbImplExec) ChangeReplicationSource(host, replicaPass string, port int32) error {
	var errb, outb bytes.Buffer
	err := d.exec(`
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
        `, &outb, &errb)

	println("ChangeReplicationSource() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("ChangeReplicationSource() ----------------------------------")

	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	// TODO: marshall buffers

	return nil
}

func (d *dbImplExec) StartReplication(host, replicaPass string, port int32) error {
	if err := d.ChangeReplicationSource(host, replicaPass, port); err != nil {
		return errors.Wrap(err, "change replication source")
	}

	var errb, outb bytes.Buffer
	err := d.exec("START REPLICA", &outb, &errb)

	println("StartReplication() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("StartReplication() ----------------------------------")

	return errors.Wrap(err, "start replication")
}

func (d *dbImplExec) StopReplication() error {
	var errb, outb bytes.Buffer
	err := d.exec("STOP REPLICA", &outb, &errb)

	println("StopReplication() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("StopReplication() ----------------------------------")

	return errors.Wrap(err, "stop replication")
}

func (d *dbImplExec) ResetReplication() error {
	var errb, outb bytes.Buffer
	err := d.exec("RESET REPLICA ALL", &outb, &errb)

	println("ResetReplication() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("ResetReplication() ----------------------------------")

	return errors.Wrap(err, "reset replication")

}

func (d *dbImplExec) ReplicationStatus() (ReplicationStatus, string, error) {
	var errb, outb bytes.Buffer

	q := fmt.Sprintf(`
		SELECT
		connection_status.SERVICE_STATE,
		applier_status.SERVICE_STATE,
			HOST
		FROM replication_connection_status connection_status
		JOIN replication_connection_configuration connection_configuration
			ON connection_status.channel_name = connection_configuration.channel_name
		JOIN replication_applier_status applier_status
			ON connection_status.channel_name = applier_status.channel_name
		WHERE connection_status.channel_name = %s 
		`, DefaultChannelName)

	err := d.exec(q, &outb, &errb)

	println("ReplicationStatus() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("ReplicationStatus() ----------------------------------")

	return ReplicationStatusActive, "", err
	// var ioState, sqlState, host string
	// if err := row.Scan(&ioState, &sqlState, &host); err != nil {
	// 	if errors.Is(err, sql.ErrNoRows) {
	// 		return ReplicationStatusNotInitiated, "", nil
	// 	}
	// 	return ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	// }

	// if ioState == "ON" && sqlState == "ON" {
	// 	return ReplicationStatusActive, host, nil
	// }

	// return ReplicationStatusNotInitiated, "", nil
}

func (d *dbImplExec) IsReplica() (bool, error) {
	status, _, err := d.ReplicationStatus()
	return status == ReplicationStatusActive, errors.Wrap(err, "get replication status")
}

func (d *dbImplExec) EnableSuperReadonly() error {
	var errb, outb bytes.Buffer
	err := d.exec("SET GLOBAL SUPER_READ_ONLY=1", &outb, &errb)

	println("EnableSuperReadonly() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("EnableSuperReadonly() ----------------------------------")

	return errors.Wrap(err, "set global super_read_only param to 1")
}

func (d *dbImplExec) IsReadonly() (bool, error) {
	var readonly int

	var errb, outb bytes.Buffer

	err := d.exec("select @@read_only and @@super_read_only", &outb, &errb)
	println("IsReadonly() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("IsReadonly() ----------------------------------")

	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

func (d *dbImplExec) ReportHost() (string, error) {
	var reportHost string

	var errb, outb bytes.Buffer
	err := d.exec("select @@report_host", &outb, &errb)
	println("ReportHost() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("ReportHost() ----------------------------------")

	return reportHost, errors.Wrap(err, "select report_host param")
}

func (d *dbImplExec) Close() error {
	return nil
}

func (d *dbImplExec) CloneInProgress() (bool, error) {
	var errb, outb bytes.Buffer
	err := d.exec("SELECT STATE FROM clone_status", &outb, &errb)
	println("CloneInProgress() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("CloneInProgress() ----------------------------------")
	if err != nil {
		return false, errors.Wrap(err, "fetch clone status")
	}

	// for rows.Next() {
	// 	var state string
	// 	if err := rows.Scan(&state); err != nil {
	// 		return false, errors.Wrap(err, "scan rows")
	// 	}

	// 	if state != "Completed" && state != "Failed" {
	// 		return true, nil
	// 	}
	// }

	return false, nil
}

func (d *dbImplExec) NeedsClone(donor string, port int32) (bool, error) {
	var errb, outb bytes.Buffer
	err := d.exec("SELECT SOURCE, STATE FROM clone_status", &outb, &errb)
	println("NeedsClone() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("NeedsClone() ----------------------------------")
	if err != nil {
		return false, errors.Wrap(err, "fetch clone status")
	}
	// defer rows.Close()

	// for rows.Next() {
	// 	var source, state string
	// 	if err := rows.Scan(&source, &state); err != nil {
	// 		return false, errors.Wrap(err, "scan rows")
	// 	}
	// 	if source == fmt.Sprintf("%s:%d", donor, port) && state == "Completed" {
	// 		return false, nil
	// 	}
	// }

	return true, nil
}

func (d *dbImplExec) Clone(donor, user, pass string, port int32) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL clone_valid_donor_list=%s", fmt.Sprintf("%s:%d", donor, port))
	err := d.exec(q, &outb, &errb)
	println("Clone() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("Clone() ----------------------------------")
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	q = fmt.Sprintf("CLONE INSTANCE FROM %s@%s:%d IDENTIFIED BY %s", user, donor, port, pass)
	err = d.exec(q, &outb, &errb)
	println("Clone() CLONE INSTANCE FROM----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("Clone() CLONE INSTANCE FROM----------------------------------")

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

func (d *dbImplExec) DumbQuery() error {
	var errb, outb bytes.Buffer
	err := d.exec("SELECT 1", &outb, &errb)
	println("DumbQuery() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("DumbQuery() ----------------------------------")
	return errors.Wrap(err, "SELECT 1")
}

func (d *dbImplExec) SetSemiSyncSource(enabled bool) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL rpl_semi_sync_master_enabled=%t", enabled)
	err := d.exec(q, &outb, &errb)
	println("SetSemySyncSource() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("SetSemySyncSource() ----------------------------------")
	return errors.Wrap(err, "set rpl_semi_sync_master_enabled")
}

func (d *dbImplExec) SetSemiSyncSize(size int) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL rpl_semi_sync_master_wait_for_slave_count=%d", size)
	err := d.exec(q, &outb, &errb)
	println("SetSemiSyncSize() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("SetSemiSyncSize() ----------------------------------")
	return errors.Wrap(err, "set rpl_semi_sync_master_wait_for_slave_count")
}

func (d *dbImplExec) GetGlobal(variable string) (interface{}, error) {
	// TODO: check how to do this without being vulnerable to injection
	var value interface{}
	var errb, outb bytes.Buffer
	err := d.exec(fmt.Sprintf("SELECT @@%s", variable), &outb, &errb)
	println("GetGlobal() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("GetGlobal() ----------------------------------")
	return value, errors.Wrapf(err, "SELECT @@%s", variable)
}

func (d *dbImplExec) SetGlobal(variable, value interface{}) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL %s=%s", variable, value)
	err := d.exec(q, &outb, &errb)
	println("SetGlobal() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("SetGlobal() ----------------------------------")
	return errors.Wrapf(err, "SET GLOBAL %s=%s", variable, value)
}

func (d *dbImplExec) StartGroupReplication(password string) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("START GROUP_REPLICATION USER=%s, PASSWORD=%s", apiv1alpha1.UserReplication, password)
	err := d.exec(q, &outb, &errb)
	println("StartGroupReplication() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("StartGroupReplication() ----------------------------------")

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

func (d *dbImplExec) StopGroupReplication() error {
	var errb, outb bytes.Buffer
	err := d.exec("STOP GROUP_REPLICATION", &outb, &errb)
	println("StopGroupReplication() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("StopGroupReplication() ----------------------------------")
	return errors.Wrap(err, "stop group replication")
}

func (d *dbImplExec) ChangeGroupReplicationPassword(replicaPass string) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf(`
            CHANGE REPLICATION SOURCE TO
                SOURCE_USER='%s',
                SOURCE_PASSWORD='%s'
            FOR CHANNEL 'group_replication_recovery'
        `, apiv1alpha1.UserReplication, replicaPass)

	err := d.exec(q, &outb, &errb)

	println("ChangeGroupReplicationPassword() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("ChangeGroupReplicationPassword() ----------------------------------")
	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	return nil
}

func (d *dbImplExec) GetGroupReplicationPrimary() (string, error) {
	var host string

	var errb, outb bytes.Buffer
	err := d.exec("SELECT MEMBER_HOST FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'", &outb, &errb)
	println("GetGroupReplicationPrimary() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("GetGroupReplicationPrimary() ----------------------------------")
	if err != nil {
		return "", errors.Wrap(err, "query primary member")
	}

	return host, nil
}

func (d *dbImplExec) GetGroupReplicationReplicas() ([]string, error) {
	replicas := make([]string, 0)

	var errb, outb bytes.Buffer
	err := d.exec("SELECT MEMBER_HOST FROM replication_group_members WHERE MEMBER_ROLE='SECONDARY' AND MEMBER_STATE='ONLINE'", &outb, &errb)
	println("GetGroupReplicationReplicas() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("GetGroupReplicationReplicas() ----------------------------------")
	if err != nil {
		return nil, errors.Wrap(err, "query replicas")
	}
	// defer rows.Close()

	// for rows.Next() {
	// 	var host string
	// 	if err := rows.Scan(&host); err != nil {
	// 		return nil, errors.Wrap(err, "scan rows")
	// 	}

	// 	replicas = append(replicas, host)
	// }

	return replicas, nil
}

func (d *dbImplExec) GetMemberState(host string) (MemberState, error) {
	var state MemberState

	var errb, outb bytes.Buffer
	q := fmt.Sprintf(`SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_HOST='%s'`, host)
	err := d.exec(q, &outb, &errb)
	println("GetMemberState() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("GetMemberState() ----------------------------------")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MemberStateOffline, nil
		}
		return MemberStateError, errors.Wrap(err, "query member state")
	}

	return state, nil
}

func (d *dbImplExec) GetGroupReplicationMembers() ([]string, error) {
	members := make([]string, 0)

	var errb, outb bytes.Buffer
	err := d.exec("SELECT MEMBER_HOST FROM replication_group_members", &outb, &errb)
	println("GetGroupReplicationMembers() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("GetGroupReplicationMembers() ----------------------------------")
	if err != nil {
		return nil, errors.Wrap(err, "query members")
	}
	// defer rows.Close()

	// for rows.Next() {
	// 	var host string
	// 	if err := rows.Scan(&host); err != nil {
	// 		return nil, errors.Wrap(err, "scan rows")
	// 	}

	// 	members = append(members, host)
	// }

	return members, nil
}

func (d *dbImplExec) CheckIfDatabaseExists(name string) (bool, error) {
	// var db string

	var outb, errb bytes.Buffer
	err := d.exec(fmt.Sprintf("SHOW DATABASES LIKE %s", name), &outb, &errb)
	println("CheckIfDatabaseExists() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("CheckIfDatabaseExists() ----------------------------------")

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (d *dbImplExec) CheckIfInPrimaryPartition() (bool, error) {
	var in bool

	var outb, errb bytes.Buffer
	err := d.exec(`
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
		member_id = @@glob, &outb, &errba
	`, &outb, &errb)

	println("CheckIfInPrimaryPartition() ----------------------------------")
	println(outb.String())
	println("----------------------------------")
	println(errb.String())
	println("CheckIfInPrimaryPartition() ----------------------------------")

	if err != nil {
		return false, err
	}

	return in, nil
}
