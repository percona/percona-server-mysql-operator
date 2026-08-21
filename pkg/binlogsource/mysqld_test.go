package binlogsource

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	tlsutil "github.com/percona/percona-server-mysql-operator/pkg/tls"
)

// These helpers run a real mysqld, because that is the only replica that keeps
// a relay log. A go-mysql replica reads events straight off the wire, so it
// cannot see anything the relay log writer rejects.

const defaultMySQLImage = "percona/percona-server:8.4"

// Where the replica container finds the certificates, mirroring the operator.
const replicaTLSDir = "/etc/mysql/mysql-tls-secret"

// The containers share the host network namespace, so the replica reaches the
// source on the loopback address. A name rather than 127.0.0.1 keeps the
// certificate's SAN meaningful, and it must not be "localhost": the client
// library the IO thread uses would take that as a request for a unix socket.
const sourceHost = "binlog-source"

func mysqlImage() string {
	if img := os.Getenv("TEST_MYSQL_IMAGE"); img != "" {
		return img
	}
	return defaultMySQLImage
}

func requireDocker(t *testing.T) {
	t.Helper()

	if err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		t.Skipf("docker is not available: %v", err)
	}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()

	out, err := exec.Command("docker", args...).CombinedOutput()
	require.NoErrorf(t, err, "docker %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// writeCertDir writes a certificate the source serves and the replica trusts,
// and returns the TLS config for the source.
func writeCertDir(t *testing.T, dir string, hosts ...string) *tls.Config {
	t.Helper()

	caCert, cert, key, err := tlsutil.IssueCerts(hosts)
	require.NoError(t, err)

	for name, data := range map[string][]byte{"ca.crt": caCert, "tls.crt": cert, "tls.key": key} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
	}

	keyPair, err := tls.X509KeyPair(cert, key)
	require.NoError(t, err)

	return &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		MinVersion:   tls.VersionTLS12,
	}
}

type mysqld struct {
	t  *testing.T
	id string
	db *sql.DB
}

// startMySQLD boots a mysqld with GTIDs and binary logging on. Every mount is
// passed to docker as a -v argument.
//
// The container shares the host network namespace because the docker bridge
// cannot always reach a port on the host, and the source listens on the host.
// That makes the mysqld port host-wide, so each one gets its own.
func startMySQLD(t *testing.T, serverID int, mounts ...string) *mysqld {
	t.Helper()

	port := freePort(t)
	args := []string{
		"run", "-d", "--rm",
		"--network", "host",
		"--add-host", sourceHost + ":127.0.0.1",
		"-e", "MYSQL_ROOT_PASSWORD=root",
	}
	for _, m := range mounts {
		args = append(args, "-v", m)
	}
	args = append(args, mysqlImage(),
		fmt.Sprintf("--server-id=%d", serverID),
		fmt.Sprintf("--port=%d", port),
		// The X plugin would claim a second host-wide port.
		"--mysqlx=0",
		"--gtid-mode=ON",
		"--enforce-gtid-consistency=ON",
		"--log-bin=binlog",
		"--log-bin-index=binlog.index",
		"--log-replica-updates=ON",
		// Named after the channel, not the host, which under a shared network
		// namespace is whatever the machine is called.
		"--relay-log=relay-bin",
		"--relay-log-index=relay-bin.index",
	)

	m := &mysqld{t: t, id: docker(t, args...)}
	t.Cleanup(func() {
		if t.Failed() {
			m.logReplication()
		}
		exec.Command("docker", "rm", "-f", m.id).Run() //nolint:errcheck
	})

	db, err := sql.Open("mysql", fmt.Sprintf("root:root@tcp(127.0.0.1:%d)/", port))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() }) //nolint:errcheck
	m.db = db

	waitFor(t, 2*time.Minute, "mysqld to accept connections", func() bool {
		return db.Ping() == nil
	})

	return m
}

// logReplication reports what mysqld itself made of the stream, which says far
// more about a rejected event than SHOW REPLICA STATUS does. Startup chatter is
// left out.
func (m *mysqld) logReplication() {
	out, err := exec.Command("docker", "logs", m.id).CombinedOutput()
	if err != nil {
		m.t.Logf("mysqld %s: cannot read log: %v", m.id[:12], err)
		return
	}

	var kept []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "[Repl]") || strings.Contains(line, "[ERROR]") {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return
	}
	m.t.Logf("mysqld %s replication log:\n%s", m.id[:12], strings.Join(kept, "\n"))
}

func (m *mysqld) exec(query string, args ...any) {
	m.t.Helper()

	_, err := m.db.Exec(query, args...)
	require.NoErrorf(m.t, err, "exec %s", query)
}

func (m *mysqld) str(query string) string {
	m.t.Helper()

	var v string
	require.NoErrorf(m.t, m.db.QueryRow(query).Scan(&v), "query %s", query)
	return v
}

// rows returns each row keyed by column name, so the helpers keep working on
// server versions that add columns.
func (m *mysqld) rows(query string) []map[string]string {
	m.t.Helper()

	rows, err := m.db.Query(query)
	require.NoErrorf(m.t, err, "query %s", query)
	defer rows.Close() //nolint:errcheck

	cols, err := rows.Columns()
	require.NoError(m.t, err)

	var out []map[string]string
	for rows.Next() {
		cells := make([]sql.RawBytes, len(cols))
		dest := make([]any, len(cols))
		for i := range cells {
			dest[i] = &cells[i]
		}
		require.NoError(m.t, rows.Scan(dest...))

		row := make(map[string]string, len(cols))
		for i, c := range cols {
			row[c] = string(cells[i])
		}
		out = append(out, row)
	}
	require.NoError(m.t, rows.Err())

	return out
}

func (m *mysqld) replicaStatus(channel string) map[string]string {
	m.t.Helper()

	rows := m.rows("SHOW REPLICA STATUS FOR CHANNEL '" + channel + "'")
	require.Lenf(m.t, rows, 1, "no replica status for channel %q", channel)
	return rows[0]
}

// heartbeatsReceived is how many heartbeats the replica has accepted on a
// channel, which is the only direct evidence that the source is sending them.
func (m *mysqld) heartbeatsReceived(channel string) int {
	m.t.Helper()

	n, err := strconv.Atoi(m.str(
		"SELECT COUNT_RECEIVED_HEARTBEATS FROM performance_schema.replication_connection_status" +
			" WHERE CHANNEL_NAME = '" + channel + "'"))
	require.NoError(m.t, err)

	return n
}

// copyBinlogs copies the binary logs and their index out of a running mysqld,
// so the newest file is still open and its tail may be torn: the state a
// failover source is always in.
func (m *mysqld) copyBinlogs(dir string) {
	m.t.Helper()

	var names []string
	for _, row := range m.rows("SHOW BINARY LOGS") {
		names = append(names, row["Log_name"])
	}
	require.NotEmpty(m.t, names, "mysqld has no binary logs")

	for _, name := range append(names, "binlog.index") {
		docker(m.t, "cp", m.id+":/var/lib/mysql/"+name, filepath.Join(dir, name))
	}
}

// freePort asks the kernel for a port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close() //nolint:errcheck

	return ln.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
