package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

const defaultChannelName = ""

type ReplicationStatus int8

const (
	ReplicationStatusActive ReplicationStatus = iota
	ReplicationStatusError
	ReplicationStatusNotInitiated
)

type MemberState string

const (
	MemberStateOnline  MemberState = "ONLINE"
	MemberStateOffline MemberState = "OFFLINE"
	MemberStateError   MemberState = "ERROR"
)

type ReplicationDBManager struct {
	db *db
}

func NewReplicationManager(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1alpha1.SystemUser, pass, host string) *ReplicationDBManager {
	return &ReplicationDBManager{db: newDB(pod, cliCmd, user, pass, host)}
}

func (m *ReplicationDBManager) query(ctx context.Context, query string, out interface{}) error {
	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, query, &outb, &errb)
	if err != nil {
		return err
	}

	if !strings.Contains(errb.String(), "ERROR") && outb.Len() == 0 {
		return sql.ErrNoRows
	}

	r := csv.NewReader(bytes.NewReader(outb.Bytes()))
	r.Comma = '\t'

	if err = gocsv.UnmarshalCSV(r, out); err != nil {
		return err
	}

	return nil
}

func (m *ReplicationDBManager) ChangeReplicationSource(ctx context.Context, host, replicaPass string, port int32) error {
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
	err := m.db.exec(ctx, q, &outb, &errb)

	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	return nil
}

func (m *ReplicationDBManager) ReplicationStatus(ctx context.Context) (ReplicationStatus, string, error) {
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
		`, defaultChannelName)
	err := m.query(ctx, q, &rows)
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

func (m *ReplicationDBManager) GetGroupReplicationPrimary(ctx context.Context) (string, error) {
	rows := []*struct {
		Host string `csv:"host"`
	}{}

	err := m.query(ctx, "SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'", &rows)
	if err != nil {
		return "", errors.Wrap(err, "query primary member")
	}

	return rows[0].Host, nil
}

// TODO: finish implementation
func (m *ReplicationDBManager) GetGroupReplicationReplicas(ctx context.Context) ([]string, error) {
	rows := []*struct {
		Host string `csv:"host"`
	}{}

	err := m.query(ctx, "SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='SECONDARY' AND MEMBER_STATE='ONLINE'", &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, errors.Wrap(err, "query replicas")
	}

	replicas := make([]string, 0)
	for _, row := range rows {
		replicas = append(replicas, row.Host)
	}

	return replicas, nil
}

func (m *ReplicationDBManager) GetMemberState(ctx context.Context, host string) (MemberState, error) {
	rows := []*struct {
		State MemberState `csv:"state"`
	}{}
	q := fmt.Sprintf(`SELECT MEMBER_STATE as state FROM replication_group_members WHERE MEMBER_HOST='%s'`, host)
	err := m.query(ctx, q, &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MemberStateOffline, nil
		}
		return MemberStateError, errors.Wrap(err, "query member state")
	}

	return rows[0].State, nil
}

func (m *ReplicationDBManager) GetGroupReplicationMembers(ctx context.Context) ([]innodbcluster.Member, error) {
	rows := []*struct {
		Member string `csv:"member"`
		State  string `csv:"state"`
	}{}

	err := m.query(ctx, "SELECT MEMBER_HOST as member, MEMBER_STATE as state FROM replication_group_members", &rows)
	if err != nil {
		return nil, errors.Wrap(err, "query members")
	}

	members := make([]innodbcluster.Member, 0)
	for _, row := range rows {
		state := innodbcluster.MemberState(row.State)
		members = append(members, innodbcluster.Member{Address: row.Member, MemberState: state})
	}

	return members, nil
}

func (m *ReplicationDBManager) CheckIfDatabaseExists(ctx context.Context, name string) (bool, error) {
	rows := []*struct {
		DB string `csv:"db"`
	}{}
	q := fmt.Sprintf("SELECT SCHEMA_NAME AS db FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME LIKE '%s'", name)
	err := m.query(ctx, q, &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
