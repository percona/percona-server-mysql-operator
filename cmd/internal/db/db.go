package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	defs "github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const (
	defaultChannelName = ""

	errCodeRestartFailed            = 3707
	errCodeQueryInterrupted         = 1317
	errCodeReadCommunicationPackets = 1160

	cloneStateCompleted = "Completed"
	cloneStateFailed    = "Failed"
)

var ErrRestartAfterClone = errors.New("Error 3707: Restart server failed (mysqld is not managed by supervisor process).")

type ReplicationStatus int8

type DB struct {
	db     *sql.DB
	params DBParams
}

// cloneReadTimeoutSeconds is the read timeout for the dedicated clone connection.
// CLONE INSTANCE sends nothing on the wire until it finishes, so the normal
// read timeout would abort a long clone; this is set effectively "no deadline"
// (7 days, matching the startup-probe backstop) instead - the stall watchdog and
// context bound the operation.
const cloneReadTimeoutSeconds = 7 * 24 * 60 * 60

type DBParams struct {
	User apiv1.SystemUser
	Pass string
	Host string
	Port int32

	ReadTimeoutSeconds  uint32
	CloneTimeoutSeconds uint32
	SourceRetryCount    uint32
	SourceConnectRetry  uint32
}

func (p *DBParams) setDefaults() {
	if p.Port == 0 {
		p.Port = defs.DefaultAdminPort
	}

	if p.ReadTimeoutSeconds == 0 {
		p.ReadTimeoutSeconds = defs.DefaultReadTimeoutSecondsSeconds // 1 hour for long-running operations like clone
	}

	if p.CloneTimeoutSeconds == 0 {
		p.CloneTimeoutSeconds = defs.DefaultCloneTimeoutSeconds // generous default; large databases can take hours to clone
	}

	if p.SourceRetryCount == 0 {
		p.SourceRetryCount = defs.DefaultAsyncSourceRetryCount
	}
	if p.SourceConnectRetry == 0 {
		p.SourceConnectRetry = defs.DefaultAsyncSourceConnectRetry
	}
}

func (p *DBParams) DSN() string {
	p.setDefaults()

	config := mysql.NewConfig()

	config.User = string(p.User)
	config.Passwd = p.Pass
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	config.DBName = "performance_schema"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           "10s",
		"readTimeout":       fmt.Sprintf("%ds", p.ReadTimeoutSeconds),
		"writeTimeout":      fmt.Sprintf("%ds", p.ReadTimeoutSeconds), // Use same timeout for write operations
		"tls":               "preferred",
	}

	return config.FormatDSN()
}

func NewDatabase(ctx context.Context, params DBParams) (*DB, error) {
	// DSN() applies defaults to params (pointer receiver), so read it back after.
	dsn := params.DSN()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Wrap(err, "ping DB")
	}

	return &DB{db: db, params: params}, nil
}

func (d *DB) StartReplication(ctx context.Context, host, replicaPass string, port int32, sourceRetryCount, sourceConnectRetry uint32) error {
	if sourceRetryCount == 0 {
		sourceRetryCount = defs.DefaultAsyncSourceRetryCount
	}
	if sourceConnectRetry == 0 {
		sourceConnectRetry = defs.DefaultAsyncSourceConnectRetry
	}

	_, err := d.db.ExecContext(ctx, `
            CHANGE REPLICATION SOURCE TO
                SOURCE_USER=?,
                SOURCE_PASSWORD=?,
                SOURCE_HOST=?,
                SOURCE_PORT=?,
                SOURCE_SSL=1,
                SOURCE_CONNECTION_AUTO_FAILOVER=1,
                SOURCE_AUTO_POSITION=1,
                SOURCE_RETRY_COUNT=?,
                SOURCE_CONNECT_RETRY=?
        `, apiv1.UserReplication, replicaPass, host, port, sourceRetryCount, sourceConnectRetry)
	if err != nil {
		return errors.Wrap(err, "exec CHANGE REPLICATION SOURCE TO")
	}

	_, err = d.db.ExecContext(ctx, "START REPLICA")
	return errors.Wrap(err, "start replication")
}

func (d *DB) StopReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "STOP REPLICA")
	return errors.Wrap(err, "stop replication")
}

func (d *DB) ResetReplication(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "RESET REPLICA ALL")
	return errors.Wrap(err, "reset replication")
}

func (d *DB) ReplicationStatus(ctx context.Context) (db.ReplicationStatus, string, error) {
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
        `, defaultChannelName)

	var ioState, sqlState, host string
	if err := row.Scan(&ioState, &sqlState, &host); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.ReplicationStatusNotInitiated, "", nil
		}
		return db.ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	}

	if ioState == "ON" && sqlState == "ON" {
		return db.ReplicationStatusActive, host, nil
	}

	return db.ReplicationStatusStopped, "", nil
}

func (d *DB) IsReplica(ctx context.Context) (bool, error) {
	status, _, err := d.ReplicationStatus(ctx)
	return status == db.ReplicationStatusActive, errors.Wrap(err, "get replication status")
}

func (d *DB) DisableSuperReadonly(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL SUPER_READ_ONLY=0")
	return errors.Wrap(err, "set global super_read_only param to 0")
}

func (d *DB) IsReadonly(ctx context.Context) (bool, error) {
	var readonly int
	err := d.db.QueryRowContext(ctx, "select @@read_only and @@super_read_only").Scan(&readonly)
	return readonly == 1, errors.Wrap(err, "select global read_only param")
}

func (d *DB) ReportHost(ctx context.Context) (string, error) {
	var reportHost string
	err := d.db.QueryRowContext(ctx, "select @@report_host").Scan(&reportHost)
	return reportHost, errors.Wrap(err, "select report_host param")
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) CloneInProgress(ctx context.Context) (bool, error) {
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

		if state != cloneStateCompleted && state != cloneStateFailed {
			return true, nil
		}
	}

	return false, nil
}

// getCloneStatus returns the current clone status
func (d *DB) getCloneStatus(ctx context.Context) (string, error) {
	log := logf.FromContext(ctx)
	rows, err := d.db.QueryContext(ctx, "SELECT STATE FROM clone_status")
	if err != nil {
		return "", errors.Wrap(err, "fetch clone status")
	}
	defer func() {
		err := rows.Close()
		if err != nil {
			log.Error(err, "close rows while getting clone status")
		}
	}()

	if rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return "", errors.Wrap(err, "scan rows")
		}
		return state, nil
	}

	return "", errors.New("no clone status found")
}

// getCloneStatusDetails returns detailed clone status information for debugging
func (d *DB) getCloneStatusDetails(ctx context.Context) (map[string]any, error) {
	log := logf.FromContext(ctx)
	rows, err := d.db.QueryContext(ctx, "SELECT STATE, BEGIN_TIME, END_TIME, SOURCE, DESTINATION, ERROR_NO, ERROR_MESSAGE FROM clone_status")
	if err != nil {
		return nil, errors.Wrap(err, "fetch clone status details")
	}
	defer func() {
		err := rows.Close()
		if err != nil {
			log.Error(err, "close rows while getting clone status details")
		}
	}()

	details := make(map[string]any)
	if rows.Next() {
		var state, beginTime, endTime, source, destination, errorNo, errorMessage sql.NullString
		if err := rows.Scan(&state, &beginTime, &endTime, &source, &destination, &errorNo, &errorMessage); err != nil {
			return nil, errors.Wrap(err, "scan clone status details")
		}

		details["state"] = state.String
		details["begin_time"] = beginTime.String
		details["end_time"] = endTime.String
		details["source"] = source.String
		details["destination"] = destination.String
		details["error_no"] = errorNo.String
		details["error_message"] = errorMessage.String
	}

	return details, nil
}

func (d *DB) Clone(ctx context.Context, donor, user, pass string, port int32, cloneTimeoutSeconds, stallTimeoutSeconds uint32) error {
	// CLONE INSTANCE is silent on the wire until it completes, so running it on
	// the normal connection would trip that connection's read timeout on any
	// clone longer than ReadTimeoutSeconds (1h by default). Use a dedicated
	// connection with an effectively unlimited read deadline; the stall watchdog
	// and context bound the operation instead.
	cloneParams := d.params
	cloneParams.ReadTimeoutSeconds = cloneReadTimeoutSeconds
	cloneDB, err := sql.Open("mysql", cloneParams.DSN())
	if err != nil {
		return errors.Wrap(err, "open clone connection")
	}
	defer func() { _ = cloneDB.Close() }()

	_, err = cloneDB.ExecContext(ctx, "SET GLOBAL clone_valid_donor_list=?", fmt.Sprintf("%s:%d", donor, port))
	if err != nil {
		return errors.Wrap(err, "set clone_valid_donor_list")
	}

	// cloneCtx controls how long CLONE INSTANCE may run. We make it cancelable so
	// the stall watchdog below can stop a clone that has stopped making progress.
	// If cloneTimeoutSeconds > 0, we also add a fixed overall deadline on top.
	cloneCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if cloneTimeoutSeconds > 0 {
		var timeoutCancel context.CancelFunc
		cloneCtx, timeoutCancel = context.WithTimeout(cloneCtx, time.Duration(cloneTimeoutSeconds)*time.Second)
		defer timeoutCancel()
	}

	// Watch the clone's progress and abort it if it transfers no bytes for
	// stallTimeoutSeconds. This lets a slow-but-progressing clone run as long as
	// it needs, while a genuinely hung clone is stopped (and then retried when
	// the container restarts) instead of hanging forever.
	if stallTimeoutSeconds > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go d.watchCloneProgress(ctx, cancel, time.Duration(stallTimeoutSeconds)*time.Second, stop, d.cloneProgress)
	}

	_, err = cloneDB.ExecContext(cloneCtx, "CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?", user, donor, port, pass)
	if err != nil {
		mErr, ok := err.(*mysql.MySQLError)
		if !ok {
			return errors.Wrap(err, "clone instance")
		}

		switch mErr.Number {
		case errCodeRestartFailed:
			return ErrRestartAfterClone
		case errCodeQueryInterrupted:
			return errors.Wrapf(err, "clone instance was interrupted (likely due to timeout) - MySQL error %d: %s", mErr.Number, mErr.Message)
		case errCodeReadCommunicationPackets:
			return errors.Wrapf(err, "clone instance communication error - MySQL error %d: %s", mErr.Number, mErr.Message)
		default:
			return errors.Wrapf(err, "clone instance failed with MySQL error %d: %s", mErr.Number, mErr.Message)
		}
	}

	cloneStatus, err := d.getCloneStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "check clone status after operation")
	}

	if cloneStatus != cloneStateCompleted {
		// Get detailed clone status for better error reporting
		details, detailErr := d.getCloneStatusDetails(ctx)
		if detailErr != nil {
			return errors.Errorf("clone operation did not complete successfully, status: %s (failed to get details: %v)", cloneStatus, detailErr)
		}

		errorMsg := fmt.Sprintf("clone operation did not complete successfully, status: %s", cloneStatus)
		if errorNo, ok := details["error_no"].(string); ok && errorNo != "" {
			errorMsg += fmt.Sprintf(", error_no: %s", errorNo)
		}
		if errorMessage, ok := details["error_message"].(string); ok && errorMessage != "" {
			errorMsg += fmt.Sprintf(", error_message: %s", errorMessage)
		}

		return errors.New(errorMsg)
	}

	return nil
}

// cloneProgress reports how far the running clone has gotten, read from the
// recipient's performance_schema.clone_progress over a separate pooled
// connection so it works while the main connection is blocked in CLONE INSTANCE.
// It returns both the bytes moved (grows only during DATA/PAGE/REDO copy) and
// the count of finished stages, so byte-less stages like DROP DATA, FILE SYNC
// and RECOVERY still register as progress instead of looking like a stall.
func (d *DB) cloneProgress(ctx context.Context) (bytes, completedStages int64, err error) {
	var data, network, completed sql.NullInt64
	err = d.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(DATA), 0), COALESCE(SUM(NETWORK), 0), COALESCE(SUM(STATE = 'Completed'), 0) FROM clone_progress").
		Scan(&data, &network, &completed)
	if err != nil {
		return 0, 0, err
	}
	return data.Int64 + network.Int64, completed.Int64, nil
}

// watchCloneProgress polls the clone's progress and calls cancel to stop the
// clone if it does not advance for the whole stall duration. Progress is either
// more bytes moved or another stage finished, so byte-less tail stages (FILE
// SYNC, RECOVERY, ...) are not mistaken for a stall. Short outages (the mysqld
// restart at the end of a clone, a transient error) are tolerated, but if the
// clone cannot be observed at all for a whole stall duration - so we can neither
// confirm progress nor a stall - it is stopped too, rather than waiting forever.
// cloneProgressPollInterval is how often the watchdog samples progress. It is a
// var (not a const) so tests can shrink it.
var cloneProgressPollInterval = 30 * time.Second

// cloneProgressFunc reports (bytesTransferred, completedStages). It is injected
// so the watchdog loop can be tested without a live clone.
type cloneProgressFunc func(ctx context.Context) (bytes, completedStages int64, err error)

func (d *DB) watchCloneProgress(ctx context.Context, cancel context.CancelFunc, stall time.Duration, stop <-chan struct{}, progress cloneProgressFunc) {
	log := logf.FromContext(ctx)

	ticker := time.NewTicker(cloneProgressPollInterval)
	defer ticker.Stop()

	now := time.Now()
	lastBytes := int64(-1)
	lastStages := int64(-1)
	lastProgress := now // last time bytes moved or a stage finished
	lastMeasured := now // last time we successfully read progress

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			qCtx, qCancel := context.WithTimeout(ctx, 10*time.Second)
			bytes, stages, err := progress(qCtx)
			qCancel()
			if err != nil {
				// Could not read progress this tick (the mysqld restart at the end
				// of a clone, or a transient error). Tolerate short outages, but if
				// we cannot observe the clone at all for a whole stall duration,
				// stop it instead of waiting forever: a healthy clone keeps its
				// recipient reachable, so persistent unreadability is itself a
				// failure.
				if time.Since(lastMeasured) >= stall {
					log.Info("clone progress could not be measured, aborting", "stall", stall.String())
					cancel()
					return
				}
				continue
			}
			lastMeasured = time.Now()

			if lastBytes < 0 || bytes > lastBytes || stages > lastStages {
				lastBytes = bytes
				lastStages = stages
				lastProgress = time.Now()
				continue
			}

			if time.Since(lastProgress) >= stall {
				log.Info("clone made no progress, aborting", "stall", stall.String(), "bytesTransferred", bytes, "completedStages", stages)
				cancel()
				return
			}
		}
	}
}

func (d *DB) DumbQuery(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SELECT 1")
	return errors.Wrap(err, "SELECT 1")
}

func (d *DB) GetMemberState(ctx context.Context, host string) (db.MemberState, error) {
	var state db.MemberState

	err := d.db.QueryRowContext(ctx, "SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_HOST=?", host).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.MemberStateOffline, nil
		}
		return db.MemberStateError, errors.Wrap(err, "query member state")
	}

	return state, nil
}

func (d *DB) GetSelfState(ctx context.Context) (db.MemberState, error) {
	var state db.MemberState

	err := d.db.QueryRowContext(ctx, "SELECT MEMBER_STATE FROM replication_group_members WHERE MEMBER_ID = @@global.server_uuid").Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.MemberStateOffline, nil
		}
		return db.MemberStateError, errors.Wrap(err, "query member state")
	}

	return state, nil
}

func (d *DB) IsGRConfigured(ctx context.Context) (bool, error) {
	var groupName sql.NullString

	err := d.db.QueryRowContext(ctx, "SELECT @@global.group_replication_group_name").Scan(&groupName)
	if err != nil {
		return false, errors.Wrap(err, "query group replication group name")
	}

	return groupName.Valid && groupName.String != "", nil
}

func (d *DB) CheckIfInPrimaryPartition(ctx context.Context) (bool, error) {
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

func (d *DB) EnableSuperReadonly(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "SET GLOBAL SUPER_READ_ONLY=1")
	return errors.Wrap(err, "set global super_read_only param to 1")
}

func (d *DB) GetGTIDExecuted(ctx context.Context) (string, error) {
	var gtid string
	err := d.db.QueryRowContext(ctx, "SELECT @@GTID_EXECUTED").Scan(&gtid)
	return gtid, errors.Wrap(err, "get GTID_EXECUTED")
}
