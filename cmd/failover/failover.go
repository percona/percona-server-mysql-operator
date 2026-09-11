package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
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
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const sourceLogsDir = "/var/lib/mysql/source-logs"

const (
	relayLogApplyTimeout = 10 * time.Minute
	relayLogApplyPoll    = time.Second
	jobTimeout           = 10 * time.Minute
)

var binlogMagic = []byte{0xfe, 'b', 'i', 'n'}

type database interface {
	replicaStatuser

	StopReplication(ctx context.Context) error
	FlushRelayLogs(ctx context.Context) error
	GetSourceLogPos(ctx context.Context) (db.ReplicaPosition, error)
	RelayLogPaths(ctx context.Context) (string, string, error)
	StartSQLThread(ctx context.Context) error
}

var _ database = (*db.DB)(nil)

type failoverConfig struct {
	newDatabase  func(ctx context.Context) (database, error)
	sourceURL    func(host string) string
	source       string
	stagingDir   string
	wait         bool
	applyPoll    time.Duration
	applyTimeout time.Duration
}

type flags struct {
	source      string
	stagingDir  string
	wait        bool
	waitTimeout time.Duration
	timeout     time.Duration
}

func parseFlags() flags {
	f := flags{}
	flag.StringVar(&f.source, "source", "", "Server to fetch the missing binary logs from.")
	flag.StringVar(&f.stagingDir, "staging-dir", sourceLogsDir, "Directory the fetched binary logs are staged in. Its contents are wiped first.")
	flag.BoolVar(&f.wait, "wait", true, "Wait for the applier to work through the fetched logs. With -wait=false the job returns as soon as the SQL thread is started.")
	flag.DurationVar(&f.waitTimeout, "wait-timeout", relayLogApplyTimeout, "How long to wait for the applier to work through the fetched logs. Ignored with -wait=false.")
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
		wait:         f.wait,
		applyPoll:    relayLogApplyPoll,
		applyTimeout: f.waitTimeout,
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
	stagingDir, err := stagingPath(cfg.stagingDir)
	if err != nil {
		return err
	}
	log.Printf("Fetching binary logs from %s", host)

	d, err := cfg.newDatabase(ctx)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	log.Printf("Connected to DB")

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

	sourceLogs, err := fetchLogsFromSource(ctx, stagingDir, cfg.sourceURL(host), positions.SourceLog, positions.SourcePos)
	if err != nil {
		return fmt.Errorf("fetch logs from source: %w", err)
	}
	log.Printf("Fetched %d binary log(s) from source", len(sourceLogs))

	relayLog, startPos, err := updateRelayLogs(sourceLogs, positions.RelayLog, relayLogBasename, relayLogIndex)
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

	if err := waitForRelayLogsApplied(ctx, d, relayLog, startPos, cfg.applyPoll, cfg.applyTimeout); err != nil {
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
// they are spliced into the relay log.
func stagingPath(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("-staging-dir is not set")
	}

	return dir, nil
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
func fetchLogsFromSource(ctx context.Context, stagingDir, sourceURL, binlog string, position uint64) ([]string, error) {
	type request struct {
		BinaryLog string `json:"binary_log"`
		Position  uint64 `json:"position"`
	}

	body, err := json.Marshal(request{BinaryLog: binlog, Position: position})
	if err != nil {
		return nil, err
	}

	client := &http.Client{}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sourceURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream logs from source: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	if err := os.RemoveAll(stagingDir); err != nil {
		return nil, fmt.Errorf("remove dir %s: %w", stagingDir, err)
	}
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return nil, fmt.Errorf("create dir %s: %w", stagingDir, err)
	}

	logs := make([]string, 0)

	tr := tar.NewReader(resp.Body)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		name := filepath.Base(hdr.Name)
		target := filepath.Join(stagingDir, name)

		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
		if err != nil {
			return nil, err
		}

		n, err := io.Copy(out, tr)
		if err != nil {
			out.Close() //nolint:errcheck
			return nil, err
		}
		if err := out.Close(); err != nil {
			return nil, err
		}

		log.Printf("wrote %s (%d bytes)", target, n)

		logs = append(logs, target)
	}

	if len(logs) == 0 {
		return nil, errors.New("source streamed no binary logs")
	}

	return logs, nil
}

func getLogIndex(logName string) (uint64, error) {
	s := strings.Split(logName, ".")
	return strconv.ParseUint(s[len(s)-1], 10, 64)
}

// lastClosedRelayLog returns the path of the newest relay log mysqld has already closed.
func lastClosedRelayLog(relayLogIndex, relayDir string) (string, error) {
	f, err := os.Open(relayLogIndex)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", relayLogIndex, err)
	}
	defer f.Close() //nolint:errcheck

	relayLogs := make([]string, 0)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entry := strings.TrimSpace(scanner.Text())
		if entry == "" {
			continue
		}
		relayLogs = append(relayLogs, entry)
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", relayLogIndex, err)
	}

	if len(relayLogs) < 2 {
		return "", fmt.Errorf("%s lists %d relay log(s), so no relay log is closed and none can be appended to", relayLogIndex, len(relayLogs))
	}

	return filepath.Join(relayDir, filepath.Base(relayLogs[len(relayLogs)-2])), nil
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
		magic := make([]byte, len(binlogMagic))
		if _, err := io.ReadFull(src, magic); err != nil {
			return 0, fmt.Errorf("read magic number: %w", err)
		}
		if !bytes.Equal(magic, binlogMagic) {
			return 0, fmt.Errorf("unexpected magic number %#x: not a binary log", magic)
		}
	}

	return io.Copy(dst, src)
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
func updateRelayLogs(sourceLogs []string, relayLog, relayLogBasename, relayLogIndex string) (string, uint64, error) {
	pending, err := pendingBytes(sourceLogs)
	if err != nil {
		return "", 0, err
	}

	if pending == 0 {
		log.Printf("Source has nothing beyond what the replica received; skipping the splice")
		return relayLog, 0, nil
	}

	relayDir := filepath.Dir(relayLogBasename)

	target, err := lastClosedRelayLog(relayLogIndex, relayDir)
	if err != nil {
		return "", 0, fmt.Errorf("get relay log to append to: %w", err)
	}

	applierIdx, err := getLogIndex(relayLog)
	if err != nil {
		return "", 0, fmt.Errorf("get relay log (%s) index: %w", relayLog, err)
	}
	targetIdx, err := getLogIndex(target)
	if err != nil {
		return "", 0, fmt.Errorf("get relay log (%s) index: %w", target, err)
	}
	if applierIdx > targetIdx {
		return "", 0, fmt.Errorf("applier is on %s, ahead of the newest closed relay log %s", relayLog, filepath.Base(target))
	}

	fi, err := os.Stat(target)
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", target, err)
	}
	startPos := uint64(fi.Size())

	log.Printf("updateRelayLogs sourceLogs=%d pending=%d relayLog=%s target=%s startPos=%d", len(sourceLogs), pending, relayLog, target, startPos)

	relay, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", target, err)
	}

	for i, sourceLog := range sourceLogs {
		// The first log is the source's binary log cut at the position the replica
		// had already read, so it carries no magic number. Every later one is a
		// whole file and does.
		w, err := appendLog(relay, sourceLog, i > 0)
		if err != nil {
			relay.Close() //nolint:errcheck
			return "", 0, fmt.Errorf("append %s to %s: %w", sourceLog, target, err)
		}

		log.Printf("written %d bytes from %s into %s", w, filepath.Base(sourceLog), filepath.Base(target))
	}

	return target, startPos, relay.Close()
}

type replicaStatuser interface {
	ShowReplicaStatus(ctx context.Context) (map[string]string, error)
}

// waitForRelayLogsApplied blocks until the SQL thread has worked through the relay log
func waitForRelayLogsApplied(
	ctx context.Context,
	s replicaStatuser,
	relayLog string,
	startPos uint64,
	poll, timeout time.Duration,
) error {
	targetIdx, err := getLogIndex(relayLog)
	if err != nil {
		return fmt.Errorf("get relay log (%s) index: %w", relayLog, err)
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

			currentIdx, err := getLogIndex(currentLog)
			if err != nil {
				return fmt.Errorf("get relay log (%s) index: %w", currentLog, err)
			}

			if currentLog != prevLog || currentPos != prevPos {
				log.Printf("Applying %s:%d (source %s:%s)", currentLog, currentPos,
					status["Relay_Source_Log_File"], status["Exec_Source_Log_Pos"])
			}

			drained := strings.Contains(status["Replica_SQL_Running_State"], "read all relay log")
			stable := currentLog == prevLog && currentPos == prevPos
			// Positions are per file, so startPos only means anything once the
			// applier is actually in the relay log we appended to.
			progressed := currentIdx > targetIdx || (currentIdx == targetIdx && currentPos > startPos)

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
			return fmt.Errorf("gave up after %s at %s:%d: %w", timeout, prevLog, prevPos, ctx.Err())
		case <-ticker.C:
		}
	}
}
