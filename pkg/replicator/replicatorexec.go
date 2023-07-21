package replicator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/gocarina/gocsv"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

var sensitiveRegexp = regexp.MustCompile(":.*@")

type dbImplExec struct {
	client *clientcmd.Client
	obj    client.Object
	user   apiv1alpha1.SystemUser
	pass   string
	host   string
}

func NewReplicatorExec(obj client.Object, user apiv1alpha1.SystemUser, pass, host string) (Replicator, error) {
	c, err := clientcmd.NewClient()
	if err != nil {
		return nil, err
	}

	return &dbImplExec{client: c, obj: obj, user: user, pass: pass, host: host}, nil
}

func (d *dbImplExec) exec(stm string, stdout, stderr *bytes.Buffer) error {
	cmd := []string{"mysql", "--database", "performance_schema", fmt.Sprintf("-p%s", d.pass), "-u", string(d.user), "-h", d.host, "-e", stm}

	err := d.client.Exec(context.TODO(), d.obj, "mysql", cmd, nil, stdout, stderr, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(stdout.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(stderr.String(), ":*****@")
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, sout, serr)
	}

	if strings.Contains(stderr.String(), "ERROR") {
		return fmt.Errorf("sql error: %s", stderr)
	}

	return nil
}

func (d *dbImplExec) query(query string, out interface{}) error {
	var errb, outb bytes.Buffer
	err := d.exec(query, &outb, &errb)
	if err != nil {
		return err
	}

	if !strings.Contains(errb.String(), "ERROR") && outb.Len() == 0 {
		return sql.ErrNoRows
	}

	csv := csv.NewReader(bytes.NewReader(outb.Bytes()))
	csv.Comma = '\t'

	if err = gocsv.UnmarshalCSV(csv, out); err != nil {
		return err
	}

	return nil
}

func (d *dbImplExec) ChangeReplicationSource(host, replicaPass string, port int32) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf(`
		CHANGE REPLICATION SOURCE TO
			SOURCE_USER='%s',
			SOURCE_PASSWORD='%s',
			SOURCE_HOST='%s',
			SOURCE_PORT=%d,
			SOURCE_SSL=1,
			SOURCE_CONNECTION_AUTO_FAILOVER=1,
			SOURCE_AUTO_POSITION=1,
			SOURCE_RETRY_COUNT=3,
			SOURCE_CONNECT_RETRY=60
		`, apiv1alpha1.UserReplication, replicaPass, host, port)
	err := d.exec(q, &outb, &errb)

	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	return nil
}

func (d *dbImplExec) StartReplication(host, replicaPass string, port int32) error {
	if err := d.ChangeReplicationSource(host, replicaPass, port); err != nil {
		return errors.Wrap(err, "change replication source")
	}

	var errb, outb bytes.Buffer
	err := d.exec("START REPLICA", &outb, &errb)
	return errors.Wrap(err, "start replication")
}

func (d *dbImplExec) StopReplication() error {
	var errb, outb bytes.Buffer
	err := d.exec("STOP REPLICA", &outb, &errb)
	return errors.Wrap(err, "stop replication")
}

func (d *dbImplExec) ResetReplication() error {
	var errb, outb bytes.Buffer
	err := d.exec("RESET REPLICA ALL", &outb, &errb)
	return errors.Wrap(err, "reset replication")

}

func (d *dbImplExec) ReplicationStatus() (ReplicationStatus, string, error) {
	rows := []*struct {
		IoState  string `csv:"conn_state"`
		SqlState string `csv:"applier_state"`
		Host     string `csv:"host"`
	}{}

	q := fmt.Sprintf(`
        SELECT
	    connection_status.SERVICE_STATE as conn_state,
	    applier_status.SERVICE_STATE as applier_state,
            HOST as host
        FROM replication_connection_status connection_status
        JOIN replication_connection_configuration connection_configuration
            ON connection_status.channel_name = connection_configuration.channel_name
        JOIN replication_applier_status applier_status
            ON connection_status.channel_name = applier_status.channel_name
        WHERE connection_status.channel_name = '%s'
		`, DefaultChannelName)
	err := d.query(q, &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReplicationStatusNotInitiated, "", nil
		}
		return ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	}

	if rows[0].IoState == "ON" && rows[0].SqlState == "ON" {
		return ReplicationStatusActive, rows[0].Host, nil
	}

	return ReplicationStatusNotInitiated, "", err
}

func (d *dbImplExec) IsReplica() (bool, error) {
	status, _, err := d.ReplicationStatus()
	return status == ReplicationStatusActive, errors.Wrap(err, "get replication status")
}

func (d *dbImplExec) EnableSuperReadonly() error {
	var errb, outb bytes.Buffer
	err := d.exec("SET GLOBAL SUPER_READ_ONLY=1", &outb, &errb)
	return errors.Wrap(err, "set global super_read_only param to 1")
}

func (d *dbImplExec) IsReadonly() (bool, error) {
	rows := []*struct {
		Readonly int `csv:"readonly"`
	}{}

	err := d.query("select @@read_only and @@super_read_only as readonly", &rows)
	if err != nil {
		return false, err
	}

	return rows[0].Readonly == 1, nil
}

func (d *dbImplExec) ReportHost() (string, error) {
	rows := []*struct {
		Host string `csv:"host"`
	}{}

	err := d.query("select @@report_host as host", &rows)
	if err != nil {
		return "", err
	}

	return rows[0].Host, nil
}

func (d *dbImplExec) Close() error {
	return nil
}

func (d *dbImplExec) CloneInProgress() (bool, error) {
	rows := []*struct {
		State string `csv:"state"`
	}{}
	err := d.query("SELECT STATE FROM clone_status as state", &rows)
	if err != nil {
		return false, errors.Wrap(err, "fetch clone status")
	}

	for _, row := range rows {
		if row.State != "Completed" && row.State != "Failed" {
			return true, nil
		}
	}

	return false, nil
}

func (d *dbImplExec) NeedsClone(donor string, port int32) (bool, error) {
	rows := []*struct {
		Source string `csv:"source"`
		State  string `csv:"state"`
	}{}
	err := d.query("SELECT SOURCE as source, STATE as state FROM clone_status", &rows)
	if err != nil {
		return false, errors.Wrap(err, "fetch clone status")
	}

	for _, row := range rows {
		if row.Source == fmt.Sprintf("%s:%d", donor, port) && row.State == "Completed" {
			return false, nil
		}
	}

	return true, nil
}

func (d *dbImplExec) Clone(donor, user, pass string, port int32) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL clone_valid_donor_list='%s'", fmt.Sprintf("%s:%d", donor, port))
	err := d.exec(q, &outb, &errb)
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	q = fmt.Sprintf("CLONE INSTANCE FROM %s@%s:%d IDENTIFIED BY %s", user, donor, port, pass)
	err = d.exec(q, &outb, &errb)

	if strings.Contains(errb.String(), "ERROR") {
		return errors.Wrap(err, "clone instance")
	}

	// Error 3707: Restart server failed (mysqld is not managed by supervisor process).
	if strings.Contains(errb.String(), "3707") {
		return ErrRestartAfterClone
	}

	return nil
}

func (d *dbImplExec) DumbQuery() error {
	var errb, outb bytes.Buffer
	err := d.exec("SELECT 1", &outb, &errb)

	return errors.Wrap(err, "SELECT 1")
}

func (d *dbImplExec) SetSemiSyncSource(enabled bool) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL rpl_semi_sync_master_enabled=%t", enabled)
	err := d.exec(q, &outb, &errb)
	return errors.Wrap(err, "set rpl_semi_sync_master_enabled")
}

func (d *dbImplExec) SetSemiSyncSize(size int) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL rpl_semi_sync_master_wait_for_slave_count=%d", size)
	err := d.exec(q, &outb, &errb)
	return errors.Wrap(err, "set rpl_semi_sync_master_wait_for_slave_count")
}

func (d *dbImplExec) GetGlobal(variable string) (interface{}, error) {
	rows := []*struct {
		Val interface{} `csv:"val"`
	}{}

	// TODO: check how to do this without being vulnerable to injection
	err := d.query(fmt.Sprintf("SELECT @@%s as val", variable), &rows)
	if err != nil {
		return nil, errors.Wrapf(err, "SELECT @@%s", variable)
	}

	return rows[0].Val, nil
}

func (d *dbImplExec) SetGlobal(variable, value interface{}) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("SET GLOBAL %s=%s", variable, value)
	err := d.exec(q, &outb, &errb)
	if err != nil {
		return errors.Wrapf(err, "SET GLOBAL %s=%s", variable, value)

	}
	return nil
}

func (d *dbImplExec) StartGroupReplication(password string) error {
	var errb, outb bytes.Buffer
	q := fmt.Sprintf("START GROUP_REPLICATION USER='%s', PASSWORD='%s'", apiv1alpha1.UserReplication, password)
	err := d.exec(q, &outb, &errb)

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
	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	return nil
}

func (d *dbImplExec) GetGroupReplicationPrimary() (string, error) {
	rows := []*struct {
		Host string `csv:"host"`
	}{}

	err := d.query("SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'", &rows)
	if err != nil {
		return "", errors.Wrap(err, "query primary member")
	}

	return rows[0].Host, nil
}

func (d *dbImplExec) GetGroupReplicationReplicas() ([]string, error) {
	rows := []*struct {
		Host string `csv:"host"`
	}{}

	err := d.query("SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='SECONDARY' AND MEMBER_STATE='ONLINE'", &rows)
	if err != nil {
		if err == sql.ErrNoRows {
			return []string{}, nil
		}
		return nil, errors.Wrap(err, "query replicas")
	}

	replicas := make([]string, 0)
	for _, row := range rows {
		replicas = append(replicas, row.Host)
	}

	return replicas, nil
}

func (d *dbImplExec) GetMemberState(host string) (MemberState, error) {
	rows := []*struct {
		State MemberState `csv:"state"`
	}{}
	q := fmt.Sprintf(`SELECT MEMBER_STATE as state FROM replication_group_members WHERE MEMBER_HOST='%s'`, host)
	err := d.query(q, &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MemberStateOffline, nil
		}
		return MemberStateError, errors.Wrap(err, "query member state")
	}

	return rows[0].State, nil
}

func (d *dbImplExec) GetGroupReplicationMembers() ([]string, error) {
	rows := []*struct {
		Member string `csv:"member"`
	}{}

	err := d.query("SELECT MEMBER_HOST as member FROM replication_group_members", &rows)
	if err != nil {
		return nil, errors.Wrap(err, "query members")
	}

	members := make([]string, 0)
	for _, row := range rows {
		members = append(members, row.Member)
	}

	return members, nil
}

func (d *dbImplExec) CheckIfDatabaseExists(name string) (bool, error) {
	rows := []*struct {
		DB string `csv:"db"`
	}{}

	q := fmt.Sprintf("SELECT SCHEMA_NAME AS db FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME LIKE '%s'", name)
	err := d.query(q, &rows)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// TODO: finish implementation
func (d *dbImplExec) CheckIfInPrimaryPartition() (bool, error) {
	rows := []*struct {
		In bool `csv:"in"`
	}{}

	err := d.query(`
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
		) as in
	FROM
		performance_schema.replication_group_members
		JOIN performance_schema.replication_group_member_stats USING(member_id)
	WHERE
		member_id = @@glob, &outb, &errba
	`, &rows)

	if err != nil {
		return false, err
	}

	return rows[0].In, nil
}

func (d *dbImplExec) ShowReplicas(ctx context.Context) ([]string, error) {
	rows := []*struct {
		ServerID    string `csv:"Server_Id"`
		Host        string `csv:"Host"`
		Port        int    `csv:"Port"`
		SourceID    string `csv:"Source_Id"`
		ReplicaUUID string `csv:"Replica_UUID"`
	}{}
	replicas := make([]string, 0)

	err := d.query("SHOW REPLICAS", &rows)
	if err != nil {
		if err == sql.ErrNoRows {
			return replicas, nil
		}
		return nil, errors.Wrap(err, "query replicas")
	}

	for _, row := range rows {
		replicas = append(replicas, row.Host)
	}

	return replicas, nil
}

func (d *dbImplExec) ShowReplicaStatus(ctx context.Context) (map[string]string, error) {
	rows := []*struct {
		SourceHost string `csv:"Source_Host"`
		IoRunning  string `csv:"Replica_IO_Running"`
		SqlRunning string `csv:"Replica_SQL_Running"`
	}{}
	err := d.query("SHOW REPLICA STATUS", &rows)
	if err != nil {
		if err == sql.ErrNoRows {
			return make(map[string]string), nil
		}
		return nil, err
	}
	if len(rows) > 1 {
		return nil, errors.New("more than one replica status returned")
	}
	if len(rows) == 0 {
		return make(map[string]string), nil
	}
	status := map[string]string{
		"Source_Host":         rows[0].SourceHost,
		"Replica_IO_Running":  rows[0].IoRunning,
		"Replica_SQL_Running": rows[0].SqlRunning,
	}

	return status, nil
}
