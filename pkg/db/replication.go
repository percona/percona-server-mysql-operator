package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	defs "github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const defaultChannelName = ""

// dns1123Hostname matches RFC-1123 hostnames
var dns1123Hostname = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$`)

type ReplicationStatus int8

const (
	ReplicationStatusActive ReplicationStatus = iota
	ReplicationStatusError
	ReplicationStatusNotInitiated
	ReplicationStatusStopped
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

func NewReplicationManager(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1.SystemUser, pass, host string) *ReplicationDBManager {
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

func (m *ReplicationDBManager) ChangeReplicationSource(ctx context.Context, host, replicaPass string, port int32, sourceRetryCount, sourceConnectRetry uint32) error {
	if sourceRetryCount == 0 {
		sourceRetryCount = defs.DefaultAsyncSourceRetryCount
	}
	if sourceConnectRetry == 0 {
		sourceConnectRetry = defs.DefaultAsyncSourceConnectRetry
	}

	var errb, outb bytes.Buffer
	q := fmt.Sprintf(`
		CHANGE REPLICATION SOURCE TO
			SOURCE_USER='%s',
			SOURCE_PASSWORD='%s',
			SOURCE_HOST='%s',
			SOURCE_PORT=%d,
			SOURCE_SSL=1,
			GET_SOURCE_PUBLIC_KEY=1,
			SOURCE_CONNECTION_AUTO_FAILOVER=1,
			SOURCE_AUTO_POSITION=1,
			SOURCE_RETRY_COUNT=%d,
			SOURCE_CONNECT_RETRY=%d
		`, apiv1.UserReplication, replicaPass, host, port, sourceRetryCount, sourceConnectRetry)
	err := m.db.exec(ctx, q, &outb, &errb)
	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	return nil
}

// IsReadonly reports whether the member rejects writes.
func (m *ReplicationDBManager) IsReadonly(ctx context.Context) (bool, error) {
	rows := []*struct {
		ReadOnly int `csv:"ro"`
	}{}
	err := m.query(ctx, "SELECT @@GLOBAL.read_only OR @@GLOBAL.super_read_only AS ro", &rows)
	if err != nil {
		return false, errors.Wrap(err, "check read only")
	}
	if len(rows) == 0 {
		return false, errors.New("empty read only result")
	}
	return rows[0].ReadOnly == 1, nil
}

// SetWritable makes the member accept writes (confirms it as the primary).
func (m *ReplicationDBManager) SetWritable(ctx context.Context) error {
	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, "SET GLOBAL super_read_only=0; SET GLOBAL read_only=0", &outb, &errb)
	return errors.Wrap(err, "set writable")
}

// GetGTIDExecuted returns the member's @@GLOBAL.gtid_executed.
func (m *ReplicationDBManager) GetGTIDExecuted(ctx context.Context) (string, error) {
	rows := []*struct {
		GTIDs string `csv:"gtids"`
	}{}
	err := m.query(ctx, "SELECT COALESCE(@@GLOBAL.gtid_executed, '') AS gtids", &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", errors.Wrap(err, "get gtid_executed")
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.ReplaceAll(rows[0].GTIDs, "\\n", ""), nil
}

// GTIDSubtract returns GTIDs of set a that are not in set b.
func (m *ReplicationDBManager) GTIDSubtract(ctx context.Context, a, b string) (string, error) {
	rows := []*struct {
		Diff string `csv:"diff"`
	}{}
	q := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', '%s') AS diff", a, b)
	err := m.query(ctx, q, &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", errors.Wrap(err, "gtid_subtract")
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.ReplaceAll(rows[0].Diff, "\\n", ""), nil
}

// maxInjectableGTIDs caps how many empty transactions InjectEmptyGTIDs is
// willing to generate in one call.
const maxInjectableGTIDs = 4096

// InjectEmptyGTIDs executes an empty transaction on this member (must be the
// writable primary) for every GTID in the set — the SQL equivalent of
// Orchestrator's gtid-errant-inject-empty, usable for unjoined members too.
func (m *ReplicationDBManager) InjectEmptyGTIDs(ctx context.Context, gtidSet string) error {
	gtids, err := expandGTIDSet(gtidSet)
	if err != nil {
		return errors.Wrapf(err, "parse GTID set %q", gtidSet)
	}
	if len(gtids) == 0 {
		return nil
	}

	var sb strings.Builder
	for _, gtid := range gtids {
		fmt.Fprintf(&sb, "SET GTID_NEXT='%s'; BEGIN; COMMIT;", gtid)
	}
	sb.WriteString("SET GTID_NEXT='AUTOMATIC';")

	var errb, outb bytes.Buffer
	if err := m.db.exec(ctx, sb.String(), &outb, &errb); err != nil {
		return errors.Wrap(err, "exec inject empty GTIDs")
	}
	return nil
}

// gtidSetRe matches one uuid:intervals element of a GTID set,
// e.g. 3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5:11.
var gtidSetRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// expandGTIDSet expands "uuid:1-3:5,uuid2:7" into individual GTIDs.
func expandGTIDSet(set string) ([]string, error) {
	gtids := make([]string, 0)
	for _, element := range strings.Split(strings.TrimSpace(set), ",") {
		element = strings.TrimSpace(element)
		if element == "" {
			continue
		}
		parts := strings.Split(element, ":")
		if len(parts) < 2 || !gtidSetRe.MatchString(parts[0]) {
			return nil, errors.Errorf("malformed GTID set element %q", element)
		}
		uuid := parts[0]
		for _, interval := range parts[1:] {
			var first, last uint64
			var err error
			if from, to, found := strings.Cut(interval, "-"); found {
				if first, err = strconv.ParseUint(from, 10, 64); err != nil {
					return nil, errors.Errorf("malformed interval %q", interval)
				}
				if last, err = strconv.ParseUint(to, 10, 64); err != nil {
					return nil, errors.Errorf("malformed interval %q", interval)
				}
			} else {
				if first, err = strconv.ParseUint(interval, 10, 64); err != nil {
					return nil, errors.Errorf("malformed interval %q", interval)
				}
				last = first
			}
			if last < first {
				return nil, errors.Errorf("malformed interval %q", interval)
			}
			if uint64(len(gtids))+(last-first+1) > maxInjectableGTIDs {
				return nil, errors.Errorf("GTID set expands to more than %d transactions; resolve manually", maxInjectableGTIDs)
			}
			for n := first; n <= last; n++ {
				gtids = append(gtids, fmt.Sprintf("%s:%d", uuid, n))
			}
		}
	}
	return gtids, nil
}

// StopReplication stops both replication threads via exec inside the pod,
// so it works even when the pod is unready and has no DNS record.
func (m *ReplicationDBManager) StopReplication(ctx context.Context) error {
	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, "STOP REPLICA", &outb, &errb)
	return errors.Wrap(err, "exec STOP REPLICA")
}

// StartReplication starts both replication threads on the configured channel.
func (m *ReplicationDBManager) StartReplication(ctx context.Context) error {
	var errb, outb bytes.Buffer
	err := m.db.exec(ctx, "START REPLICA", &outb, &errb)
	return errors.Wrap(err, "exec START REPLICA")
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
	return ReplicationStatusStopped, "", nil
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
	if !dns1123Hostname.MatchString(host) {
		return MemberStateError, errors.Errorf("invalid host %q", host)
	}
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
	if len(rows) == 0 {
		return MemberStateOffline, nil
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

// CheckIfClusterMetadataDBExists reports whether the InnoDB cluster metadata
// schema is present.
func (m *ReplicationDBManager) CheckIfClusterMetadataDBExists(ctx context.Context) (bool, error) {
	rows := []*struct {
		DB string `csv:"db"`
	}{}
	err := m.query(ctx, "SELECT SCHEMA_NAME AS db FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = 'mysql_innodb_cluster_metadata'", &rows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return len(rows) > 0, nil
}

func (m *ReplicationDBManager) GetClusterSetReplicationRunning(ctx context.Context) (bool, error) {
	rows := []*struct {
		IsRunning bool `csv:"is_running"`
	}{}

	err := m.query(ctx, "SELECT EXISTS(SELECT 1 FROM replication_connection_status conn JOIN replication_applier_status appl USING (CHANNEL_NAME) WHERE conn.CHANNEL_NAME = 'clusterset_replication' AND conn.SERVICE_STATE = 'ON' AND appl.SERVICE_STATE = 'ON') as is_running", &rows)
	if err != nil {
		return false, errors.Wrap(err, "query cluster set replication running")
	}
	if len(rows) == 0 {
		return false, nil
	}

	return rows[0].IsRunning, nil
}
