package replicator

//import (
//	"bytes"
//	"context"
//	"database/sql"
//	"encoding/csv"
//	"fmt"
//	"regexp"
//	"strings"
//
//	"github.com/gocarina/gocsv"
//	"github.com/pkg/errors"
//	corev1 "k8s.io/api/core/v1"
//
//	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
//	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
//)
//
//var sensitiveRegexp = regexp.MustCompile(":.*@")
//
//const DefaultChannelName = ""
//
//type ReplicationStatus int8
//
//var ErrRestartAfterClone = errors.New("Error 3707: Restart server failed (mysqld is not managed by supervisor process).")
//
//const (
//	ReplicationStatusActive ReplicationStatus = iota
//	ReplicationStatusError
//	ReplicationStatusNotInitiated
//)
//
//type MemberState string
//
//const (
//	MemberStateOnline  MemberState = "ONLINE"
//	MemberStateOffline MemberState = "OFFLINE"
//	MemberStateError   MemberState = "ERROR"
//)
//
//type Replicator interface {
//	ChangeReplicationSource(ctx context.Context, host, replicaPass string, port int32) error
//	ReplicationStatus(ctx context.Context) (ReplicationStatus, string, error)
//	GetGroupReplicationPrimary(ctx context.Context) (string, error)
//	GetGroupReplicationReplicas(ctx context.Context) ([]string, error)
//	GetMemberState(ctx context.Context, host string) (MemberState, error)
//}
//type db struct {
//	client clientcmd.Client
//	pod    *corev1.Pod
//	user   apiv1alpha1.SystemUser
//	pass   string
//	host   string
//}
//
//func NewReplicator(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1alpha1.SystemUser, pass, host string) (Replicator, error) {
//	return &db{client: cliCmd, pod: pod, user: user, pass: pass, host: host}, nil
//}
//
//func (d *db) exec(ctx context.Context, stm string, stdout, stderr *bytes.Buffer) error {
//	cmd := []string{"mysql", "--database", "performance_schema", fmt.Sprintf("-p%s", d.pass), "-u", string(d.user), "-h", d.host, "-e", stm}
//
//	err := d.client.Exec(ctx, d.pod, "mysql", cmd, nil, stdout, stderr, false)
//	if err != nil {
//		sout := sensitiveRegexp.ReplaceAllString(stdout.String(), ":*****@")
//		serr := sensitiveRegexp.ReplaceAllString(stderr.String(), ":*****@")
//		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, sout, serr)
//	}
//
//	if strings.Contains(stderr.String(), "ERROR") {
//		return fmt.Errorf("sql error: %s", stderr)
//	}
//
//	return nil
//}
//
//func (d *db) query(ctx context.Context, query string, out interface{}) error {
//	var errb, outb bytes.Buffer
//	err := d.exec(ctx, query, &outb, &errb)
//	if err != nil {
//		return err
//	}
//
//	if !strings.Contains(errb.String(), "ERROR") && outb.Len() == 0 {
//		return sql.ErrNoRows
//	}
//
//	r := csv.NewReader(bytes.NewReader(outb.Bytes()))
//	r.Comma = '\t'
//
//	if err = gocsv.UnmarshalCSV(r, out); err != nil {
//		return err
//	}
//
//	return nil
//}
//
//func (d *db) ChangeReplicationSource(ctx context.Context, host, replicaPass string, port int32) error {
//	var errb, outb bytes.Buffer
//	q := fmt.Sprintf(`
//		CHANGE REPLICATION SOURCE TO
//			SOURCE_USER='%s',
//			SOURCE_PASSWORD='%s',
//			SOURCE_HOST='%s',
//			SOURCE_PORT=%d,
//			SOURCE_SSL=1,
//			SOURCE_CONNECTION_AUTO_FAILOVER=1,
//			SOURCE_AUTO_POSITION=1,
//			SOURCE_RETRY_COUNT=3,
//			SOURCE_CONNECT_RETRY=60
//		`, apiv1alpha1.UserReplication, replicaPass, host, port)
//	err := d.exec(ctx, q, &outb, &errb)
//
//	if err != nil {
//		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
//	}
//
//	return nil
//}
//
//func (d *db) ReplicationStatus(ctx context.Context) (ReplicationStatus, string, error) {
//	rows := []*struct {
//		IoState  string `csv:"conn_state"`
//		SqlState string `csv:"applier_state"`
//		Host     string `csv:"host"`
//	}{}
//
//	q := fmt.Sprintf(`
//        SELECT
//	    connection_status.SERVICE_STATE as conn_state,
//	    applier_status.SERVICE_STATE as applier_state,
//            HOST as host
//        FROM replication_connection_status connection_status
//        JOIN replication_connection_configuration connection_configuration
//            ON connection_status.channel_name = connection_configuration.channel_name
//        JOIN replication_applier_status applier_status
//            ON connection_status.channel_name = applier_status.channel_name
//        WHERE connection_status.channel_name = '%s'
//		`, DefaultChannelName)
//	err := d.query(ctx, q, &rows)
//	if err != nil {
//		if errors.Is(err, sql.ErrNoRows) {
//			return ReplicationStatusNotInitiated, "", nil
//		}
//		return ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
//	}
//
//	if rows[0].IoState == "ON" && rows[0].SqlState == "ON" {
//		return ReplicationStatusActive, rows[0].Host, nil
//	}
//
//	return ReplicationStatusNotInitiated, "", err
//}
//
//func (d *db) GetGroupReplicationPrimary(ctx context.Context) (string, error) {
//	rows := []*struct {
//		Host string `csv:"host"`
//	}{}
//
//	err := d.query(ctx, "SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'", &rows)
//	if err != nil {
//		return "", errors.Wrap(err, "query primary member")
//	}
//
//	return rows[0].Host, nil
//}
//
//// TODO: finish implementation
//func (d *db) GetGroupReplicationReplicas(ctx context.Context) ([]string, error) {
//	rows := []*struct {
//		Host string `csv:"host"`
//	}{}
//
//	err := d.query(ctx, "SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='SECONDARY' AND MEMBER_STATE='ONLINE'", &rows)
//	if err != nil {
//		return nil, errors.Wrap(err, "query replicas")
//	}
//
//	replicas := make([]string, 0)
//	for _, row := range rows {
//		replicas = append(replicas, row.Host)
//	}
//
//	return replicas, nil
//}
//
//func (d *db) GetMemberState(ctx context.Context, host string) (MemberState, error) {
//	rows := []*struct {
//		State MemberState `csv:"state"`
//	}{}
//	q := fmt.Sprintf(`SELECT MEMBER_STATE as state FROM replication_group_members WHERE MEMBER_HOST='%s'`, host)
//	err := d.query(ctx, q, &rows)
//	if err != nil {
//		if errors.Is(err, sql.ErrNoRows) {
//			return MemberStateOffline, nil
//		}
//		return MemberStateError, errors.Wrap(err, "query member state")
//	}
//
//	return rows[0].State, nil
//}
