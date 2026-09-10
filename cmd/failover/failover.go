package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/handler"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const (
	// stagingMarker marks the staging directory as this job's own. Emptying the
	// directory is a recursive delete, so one without the marker is one we did not
	// create and must not touch.
	stagingMarker = ".failover-staging"
	sourceLogsDir = "/var/lib/mysql/source-logs"
	lockPath      = "/var/lib/mysql/failover.lock"
)

const (
	relayLogApplyTimeout = 1 * time.Hour
	relayLogApplyPoll    = time.Second
	sourceFetchTimeout   = 2 * time.Minute
	jobTimeout           = 6 * time.Hour
)

var errRelayApplyTimeout = fmt.Errorf("timeout while waiting for relay log apply")

var binlogMagic = []byte{0xfe, 'b', 'i', 'n'}

type database interface {
	replicaStatuser

	StopReplication(ctx context.Context) error
	FlushRelayLogs(ctx context.Context) error
	GetSourceLogPos(ctx context.Context) (db.ReplicaPosition, error)
	RelayLogPaths(ctx context.Context) (string, string, error)
	StartSQLThread(ctx context.Context) error
	Close() error
}

var _ database = (*db.DB)(nil)

type failoverConfig struct {
	newDatabase  func(ctx context.Context) (database, error)
	sourceURL    func(host string) string
	source       string
	stagingDir   string
	lockPath     string
	wait         bool
	applyPoll    time.Duration
	applyTimeout time.Duration
	fetchTimeout time.Duration
}

type flags struct {
	source       string
	stagingDir   string
	wait         bool
	waitTimeout  time.Duration
	fetchTimeout time.Duration
	timeout      time.Duration
}

func parseFlags() flags {
	f := flags{}
	flag.StringVar(&f.source, "source", "", "Server to fetch the missing binary logs from.")
	flag.StringVar(&f.stagingDir, "staging-dir", sourceLogsDir, "Directory the fetched binary logs are staged in. Its contents are wiped first, so it must be empty or a directory an earlier run created.")
	flag.BoolVar(&f.wait, "wait", true, "Wait for the applier to work through the fetched logs. With -wait=false the job returns as soon as the SQL thread is started.")
	flag.DurationVar(&f.waitTimeout, "wait-timeout", relayLogApplyTimeout, "How long to wait for the applier to work through the fetched logs. Ignored with -wait=false.")
	flag.DurationVar(&f.fetchTimeout, "fetch-timeout", sourceFetchTimeout, "How long the source has to stream the binary logs, headers and body included.")
	flag.DurationVar(&f.timeout, "timeout", jobTimeout, "How long the whole job may take, fetching the logs from the source included. Zero or less means no limit.")
	flag.Parse()

	return f
}

func config(f flags) failoverConfig {
	return failoverConfig{
		newDatabase:  func(ctx context.Context) (database, error) { return connectToDB(ctx) },
		sourceURL:    sourceStreamURL,
		source:       f.source,
		stagingDir:   f.stagingDir,
		lockPath:     lockPath,
		wait:         f.wait,
		applyPoll:    relayLogApplyPoll,
		applyTimeout: f.waitTimeout,
		fetchTimeout: f.fetchTimeout,
	}
}

func main() {
	f := parseFlags()

	// Nothing else bounds the fetch, and the job runs from a failover hook that
	// holds up the promotion for as long as it takes.
	ctx := context.Background()
	if f.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}

	if err := run(ctx, config(f)); err != nil {
		log.Fatalf("ERROR: %v", err)
	}

	log.Printf("DONE.")
}

func run(ctx context.Context, cfg failoverConfig) error {
	// Read the input before touching replication: failing after STOP REPLICA
	// would leave the replica stopped for nothing.
	host, err := sourceHost(cfg.source)
	if err != nil {
		return err
	}
	if cfg.wait && cfg.applyTimeout <= 0 {
		return fmt.Errorf("-wait-timeout must be positive, got %s", cfg.applyTimeout)
	}
	if cfg.fetchTimeout <= 0 {
		return fmt.Errorf("-fetch-timeout must be positive, got %s", cfg.fetchTimeout)
	}

	stagingDir, err := stagingPath(cfg.stagingDir)
	if err != nil {
		return err
	}
	if err := checkStagingDir(stagingDir); err != nil {
		return err
	}

	lock, err := lockSplice(cfg.lockPath)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck

	log.Printf("Fetching binary logs from %s", host)

	d, err := cfg.newDatabase(ctx)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	log.Printf("Connected to DB")

	defer func() {
		if err := d.Close(); err != nil {
			log.Printf("ERROR: failed to close database connection: %v", err)
		}
	}()

	if _, err := d.ShowReplicaStatus(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("Not a replica, nothing to apply")
			return nil
		}

		return fmt.Errorf("show replica status: %w", err)
	}

	if err := d.StopReplication(ctx); err != nil {
		return fmt.Errorf("stop replica: %w", err)
	}
	log.Printf("Stopped replication")

	if err := d.FlushRelayLogs(ctx); err != nil {
		return fmt.Errorf("flush relay logs: %w", err)
	}
	log.Printf("Flushed relay logs")

	positions, err := d.GetSourceLogPos(ctx)
	if err != nil {
		return fmt.Errorf("get replica positions: %w", err)
	}
	log.Printf("Replica positions: SourceHost=%s SourceLog=%s SourcePos=%d RelayLog=%s RelayLogPos=%d GTIDExecuted=%s",
		positions.SourceHost, positions.SourceLog, positions.SourcePos, positions.RelayLog, positions.RelayPos, positions.GTIDExecuted)

	relayLogBasename, relayLogIndex, err := d.RelayLogPaths(ctx)
	if err != nil {
		return fmt.Errorf("get relay log paths: %w", err)
	}
	log.Printf("Relay log paths: basename=%s index=%s", relayLogBasename, relayLogIndex)

	relayLogs, err := readRelayIndex(relayLogIndex, filepath.Dir(relayLogBasename))
	if err != nil {
		return fmt.Errorf("read relay log index: %w", err)
	}
	log.Printf("Relay index lists %d relay log(s)", len(relayLogs))

	sourceLogs, err := fetchLogsFromSource(ctx, stagingDir, cfg.sourceURL(host), positions.SourceLog, positions.SourcePos, cfg.fetchTimeout)
	if err != nil {
		return fmt.Errorf("fetch logs from source: %w", err)
	}
	log.Printf("Fetched %d binary log(s) from source", len(sourceLogs))

	relayLog, startPos, err := updateRelayLogs(sourceLogs, positions.RelayLog, relayLogs)
	if err != nil {
		return fmt.Errorf("update relay logs: %w", err)
	}

	if err := d.StartSQLThread(ctx); err != nil {
		return fmt.Errorf("start SQL_THREAD: %w", err)
	}
	log.Printf("Started SQL_THREAD")

	if !cfg.wait {
		log.Printf("Leaving the applier to work through %s on its own", filepath.Base(relayLog))
		return nil
	}

	if err := waitForRelayLogsApplied(ctx, d, relayLogs, relayLog, startPos, cfg.applyPoll, cfg.applyTimeout); err != nil {
		if errors.Is(err, errRelayApplyTimeout) && ctx.Err() == nil {
			log.Printf("Timed out while waiting for applier, it will continue in the server anyway")
			return nil
		}
		return fmt.Errorf("apply relay logs: %w", err)
	}

	return nil
}

func sourceHost(source string) (string, error) {
	host := strings.TrimSpace(source)
	if host == "" {
		return "", errors.New("-source is not set")
	}

	return host, nil
}

// stagingPath is where the binary logs fetched from the source are staged before
// they are spliced into the relay log. Its contents are deleted before every
// fetch, so a path that could hold anything mysqld owns is rejected outright.
func stagingPath(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("-staging-dir is not set")
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("-staging-dir %s is not an absolute path", dir)
	}

	dir = filepath.Clean(dir)
	if dir == string(filepath.Separator) {
		return "", errors.New("-staging-dir must not be the filesystem root")
	}
	if dir == mysql.DataMountPath || strings.HasPrefix(mysql.DataMountPath, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("-staging-dir %s holds the MySQL data directory %s", dir, mysql.DataMountPath)
	}

	return dir, nil
}

// checkStagingDir refuses a directory this job did not create.
func checkStagingDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}

	if len(entries) == 0 {
		return nil
	}
	for _, entry := range entries {
		if entry.Name() == stagingMarker {
			return nil
		}
	}

	return fmt.Errorf("%s has no %s marker: it is not a staging directory this job may wipe", dir, stagingMarker)
}

// prepareStagingDir empties the staging directory and marks it as this job's own.
func prepareStagingDir(dir string) error {
	if err := checkStagingDir(dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove dir %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	marker := filepath.Join(dir, stagingMarker)
	if err := os.WriteFile(marker, nil, 0644); err != nil {
		return fmt.Errorf("create %s: %w", marker, err)
	}

	return nil
}

func sourceStreamURL(host string) string {
	return fmt.Sprintf("http://%s/failover/stream",
		net.JoinHostPort(host, strconv.Itoa(mysql.SidecarHTTPPort)))
}

func connectToDB(ctx context.Context) (*db.DB, error) {
	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return nil, fmt.Errorf("get %s password: %w", apiv1.UserOperator, err)
	}

	podHostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("get hostname: %w", err)
	}

	podIP, err := utils.GetPodIP(podHostname)
	if err != nil {
		return nil, fmt.Errorf("get pod IP: %w", err)
	}

	params := db.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: podIP,
	}

	return db.NewDatabase(ctx, params)
}

// fetchLogsFromSource stages the binary logs the source streams into stagingDir
// and returns their paths in the order they arrived, which is rotation order.
func fetchLogsFromSource(ctx context.Context, stagingDir, sourceURL, binlog string, position uint64, timeout time.Duration) ([]string, error) {
	body, err := json.Marshal(handler.StreamConfig{BinaryLog: binlog, Position: int64(position)})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sourceURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream logs from source: %w", stalled(err, timeout))
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	if err := prepareStagingDir(stagingDir); err != nil {
		return nil, err
	}

	logs := make([]string, 0)

	tr := tar.NewReader(resp.Body)

	copyBinlog := func(path string, hdr *tar.Header) error {
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
		if err != nil {
			return err
		}
		defer out.Close() //nolint:errcheck

		n, err := io.CopyN(out, tr, hdr.Size)
		if err != nil {
			return err
		}

		log.Printf("wrote %s (%d bytes)", path, n)

		return nil
	}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, stalled(err, timeout)
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		name := filepath.Base(hdr.Name)
		target := filepath.Join(stagingDir, name)

		if err := copyBinlog(target, hdr); err != nil {
			return nil, stalled(err, timeout)
		}

		logs = append(logs, target)
	}

	if len(logs) == 0 {
		return nil, errors.New("source streamed no binary logs")
	}

	return logs, nil
}

func stalled(err error, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("source did not finish streaming within %s: %w", timeout, err)
	}

	return err
}

// relayIndex holds the relay logs the index file lists, in the order mysqld rotated them.
type relayIndex []string

// readRelayIndex reads the relay log index as paths in relayDir.
func readRelayIndex(relayLogIndex, relayDir string) (relayIndex, error) {
	f, err := os.Open(relayLogIndex)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", relayLogIndex, err)
	}
	defer f.Close() //nolint:errcheck

	relayLogs := make(relayIndex, 0)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entry := strings.TrimSpace(scanner.Text())
		if entry == "" {
			continue
		}
		relayLogs = append(relayLogs, filepath.Join(relayDir, filepath.Base(entry)))
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", relayLogIndex, err)
	}

	return relayLogs, nil
}

// place is how far along the index relayLog sits, or -1 when the index does not list it.
func (r relayIndex) place(relayLog string) int {
	relayLog = strings.TrimSpace(relayLog)
	if relayLog == "" {
		return -1
	}

	name := filepath.Base(relayLog)
	for i, entry := range r {
		if filepath.Base(entry) == name {
			return i
		}
	}

	return -1
}

// lastClosed returns the newest relay log mysqld has already closed
func (r relayIndex) lastClosed() (string, error) {
	if len(r) < 2 {
		return "", fmt.Errorf("the index lists %d relay log(s), so no relay log is closed and none can be appended to", len(r))
	}

	return r[len(r)-2], nil
}

// readMagic consumes the binary log magic number a relay log may only carry at
// offset 0.
func readMagic(src io.Reader) error {
	magic := make([]byte, len(binlogMagic))
	if _, err := io.ReadFull(src, magic); err != nil {
		return fmt.Errorf("read magic number: %w", err)
	}
	if !bytes.Equal(magic, binlogMagic) {
		return fmt.Errorf("unexpected magic number %#x: not a binary log", magic)
	}

	return nil
}

// appendLog copies srcPath into dst, dropping the binary log magic number a relay
// log may only carry at offset 0.
func appendLog(dst io.Writer, srcPath string, stripMagic bool) (int64, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer src.Close() //nolint:errcheck

	if stripMagic {
		if err := readMagic(src); err != nil {
			return 0, err
		}
	}

	return io.Copy(dst, src)
}

// validateSourceLogs checks every log that must carry a magic number before a
// single byte reaches the relay log, so the splice cannot fail halfway through
// on input we could have rejected up front.
func validateSourceLogs(sourceLogs []string) error {
	for i, sourceLog := range sourceLogs {
		// The first log is the source's binary log cut at the position the replica
		// had already read, so it carries no magic number. Every later one is a
		// whole file and does.
		if i == 0 {
			continue
		}

		src, err := os.Open(sourceLog)
		if err != nil {
			return err
		}

		err = readMagic(src)
		src.Close() //nolint:errcheck

		if err != nil {
			return fmt.Errorf("check %s: %w", sourceLog, err)
		}
	}

	return nil
}

// pendingBytes is how much the source actually streamed. The source cuts the
// first log at the position the replica had already read, so a replica holding
// everything the source has gets that one log with nothing in it.
func pendingBytes(sourceLogs []string) (uint64, error) {
	var total uint64

	for _, sourceLog := range sourceLogs {
		fi, err := os.Stat(sourceLog)
		if err != nil {
			return 0, fmt.Errorf("stat %s: %w", sourceLog, err)
		}
		total += uint64(fi.Size())
	}

	return total, nil
}

// updateRelayLogs appends the binary logs fetched from the source to the newest
// relay log mysqld has already closed.
func updateRelayLogs(sourceLogs []string, relayLog string, relayLogs relayIndex) (string, uint64, error) {
	pending, err := pendingBytes(sourceLogs)
	if err != nil {
		return "", 0, err
	}

	if pending == 0 {
		log.Printf("Source has nothing beyond what the replica received; skipping the splice")
		return relayLog, 0, nil
	}

	if err := validateSourceLogs(sourceLogs); err != nil {
		return "", 0, err
	}

	target, err := relayLogs.lastClosed()
	if err != nil {
		return "", 0, fmt.Errorf("get relay log to append to: %w", err)
	}

	applierPlace := relayLogs.place(relayLog)
	if applierPlace < 0 {
		return "", 0, fmt.Errorf("the index does not list the applier's relay log %q", relayLog)
	}
	if applierPlace > relayLogs.place(target) {
		return "", 0, fmt.Errorf("applier is on %s, ahead of the newest closed relay log %s", relayLog, filepath.Base(target))
	}

	fi, err := os.Stat(target)
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", target, err)
	}
	startPos := uint64(fi.Size())

	log.Printf("updateRelayLogs sourceLogs=%d pending=%d relayLog=%s target=%s startPos=%d", len(sourceLogs), pending, relayLog, target, startPos)

	if err := spliceInto(target, startPos, sourceLogs); err != nil {
		return "", 0, err
	}

	return target, startPos, nil
}

// spliceInto appends every source log to the relay log, leaving it at the length
// it had before if any of them fails. mysqld reads a half-written append as a
// corrupt event and the SQL thread never gets past it, so a failed splice must
// leave nothing behind for the next attempt to trip over.
func spliceInto(target string, startPos uint64, sourceLogs []string) error {
	relay, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}

	for i, sourceLog := range sourceLogs {
		// A receiver precedes every source rotation with an artificial Rotate event
		// naming the new file. Without it the applier keeps reporting the previous
		// file's name while taking the new file's positions, so its source
		// coordinates go backwards — and orchestrator ranks promotion candidates by
		// exactly those coordinates.
		if i > 0 {
			if err := appendRotate(relay, sourceLog); err != nil {
				return errors.Join(
					fmt.Errorf("append Rotate event for %s: %w", filepath.Base(sourceLog), err),
					rollbackSplice(relay, target, startPos),
				)
			}
		}

		// The first log is the source's binary log cut at the position the replica
		// had already read, so it carries no magic number. Every later one is a
		// whole file and does.
		w, err := appendLog(relay, sourceLog, i > 0)
		if err != nil {
			return errors.Join(
				fmt.Errorf("append %s to %s: %w", sourceLog, target, err),
				rollbackSplice(relay, target, startPos),
			)
		}

		log.Printf("written %d bytes from %s into %s", w, filepath.Base(sourceLog), filepath.Base(target))
	}

	return relay.Close()
}

func rollbackSplice(relay *os.File, target string, startPos uint64) error {
	defer relay.Close() //nolint:errcheck

	if err := relay.Truncate(int64(startPos)); err != nil {
		return fmt.Errorf("truncate %s back to %d: %w", target, startPos, err)
	}

	return nil
}

type replicaStatuser interface {
	ShowReplicaStatus(ctx context.Context) (map[string]string, error)
}

// waitForRelayLogsApplied blocks until the SQL thread has worked through the relay log
func waitForRelayLogsApplied(
	ctx context.Context,
	s replicaStatuser,
	relayLogs relayIndex,
	relayLog string,
	startPos uint64,
	poll, timeout time.Duration,
) error {
	targetPlace := relayLogs.place(relayLog)
	if targetPlace < 0 {
		return fmt.Errorf("the index does not list the relay log %q the applier must work through", relayLog)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	var prevLog string
	var prevPos uint64

	for {
		status, err := s.ShowReplicaStatus(ctx)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				log.Printf("Replication channel is gone; the instance was already promoted")
				return nil
			}

			return fmt.Errorf("show replica status: %w", err)
		}

		if sqlErr := status["Last_SQL_Error"]; sqlErr != "" {
			return fmt.Errorf("SQL_THREAD failed: %s", sqlErr)
		}
		if status["Replica_SQL_Running"] != "Yes" {
			return errors.New("SQL_THREAD is not running")
		}

		// Empty until the applier has initialized, so keep waiting rather than
		// failing to parse it.
		currentLog := status["Relay_Log_File"]
		if currentLog != "" {
			currentPos, err := strconv.ParseUint(status["Relay_Log_Pos"], 10, 64)
			if err != nil {
				return fmt.Errorf("parse Relay_Log_Pos: %w", err)
			}

			currentPlace := relayLogs.place(currentLog)
			if currentPlace < 0 {
				return fmt.Errorf("the applier is on %s, which the index does not list", currentLog)
			}

			if currentLog != prevLog || currentPos != prevPos {
				log.Printf("Applying %s:%d (source %s:%s)", currentLog, currentPos,
					status["Relay_Source_Log_File"], status["Exec_Source_Log_Pos"])
			}

			drained := strings.Contains(status["Replica_SQL_Running_State"], "read all relay log")
			stable := currentLog == prevLog && currentPos == prevPos
			// Positions are per file, so startPos only means anything once the
			// applier is actually in the relay log we appended to.
			progressed := currentPlace > targetPlace || (currentPlace == targetPlace && currentPos > startPos)

			if drained && stable && progressed {
				log.Printf("Applied all relay logs: %s:%d source=%s:%s GTIDExecuted=%s",
					currentLog, currentPos, status["Relay_Source_Log_File"],
					status["Exec_Source_Log_Pos"], status["Executed_Gtid_Set"])
				return nil
			}

			prevLog, prevPos = currentLog, currentPos
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("gave up after %s at %s:%d: %w: %w", timeout, prevLog, prevPos, errRelayApplyTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}
