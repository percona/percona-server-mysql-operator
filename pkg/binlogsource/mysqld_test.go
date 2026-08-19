package binlogsource

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"

	tlsutil "github.com/percona/percona-server-mysql-operator/pkg/tls"
)

// These helpers run a real mysqld, because that is the only replica that keeps a relay
// log. A go-mysql replica reads events straight off the wire, so it cannot see anything
// the relay log writer rejects.

const defaultMySQLImage = "percona/percona-server:8.4"

// Where the replica container finds the certificates: the mount the operator gives
// mysqld, so the test moves with it.
var replicaTLSDir = mysql.TLSMountPath

// The containers share the host network namespace, so the replica reaches the source
// on loopback. A name keeps the certificate's SAN meaningful, and it must not be
// "localhost": the client library the IO thread uses would read that as a unix socket.
const sourceHost = "binlog-source"

func mysqlImage() string {
	if img := os.Getenv("TEST_MYSQL_IMAGE"); img != "" {
		return img
	}
	return defaultMySQLImage
}

// writeCertDir writes a certificate the source serves and the replica trusts into a
// directory mysqld can read, and returns the TLS config for the source.
func writeCertDir(t *testing.T, dir string, hosts ...string) *tls.Config {
	t.Helper()

	caCert, cert, key, err := tlsutil.IssueCerts(hosts)
	require.NoError(t, err)

	for name, data := range map[string][]byte{"ca.crt": caCert, "tls.crt": cert, "tls.key": key} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
	}

	return serverTLSConfig(t, cert, key)
}

type mysqld struct {
	t   *testing.T
	ctr *testcontainers.DockerContainer
	db  *sql.DB
}

// startMySQLD boots a mysqld with GTIDs and binary logging on. Every bind is given in
// the host:container[:options] form docker expects.
//
// The container shares the host network namespace because the docker bridge cannot
// always reach a port on the host, and the source listens on the host. That makes the
// mysqld port host-wide, so each one gets its own.
func startMySQLD(t *testing.T, serverID int, binds ...string) *mysqld {
	t.Helper()

	ctx := context.Background()
	port := freePort(t)
	dsn := fmt.Sprintf("root:root@tcp(127.0.0.1:%d)/", port)

	ctr, err := testcontainers.Run(ctx, mysqlImage(),
		testcontainers.WithEnv(map[string]string{"MYSQL_ROOT_PASSWORD": "root"}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.NetworkMode = "host"
			hc.ExtraHosts = []string{sourceHost + ":127.0.0.1"}
			hc.Binds = binds
		}),
		testcontainers.WithCmd(
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
		),
		// The port is host-wide, so the daemon host and the mapped port the strategy
		// hands over say nothing the DSN does not already have.
		testcontainers.WithWaitStrategy(
			wait.ForSQL(strconv.Itoa(port), "mysql", func(string, network.Port) string { return dsn }).
				WithStartupTimeout(2*time.Minute),
		),
	)
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)

	m := &mysqld{t: t, ctr: ctr}
	t.Cleanup(func() {
		if t.Failed() {
			m.logReplication()
		}
	})

	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() }) //nolint:errcheck
	// The fixtures run statements that only mean anything on the connection before
	// them: an XA branch belongs to one session, and binlog_transaction_compression is
	// read at commit from the session that commits.
	db.SetMaxOpenConns(1)
	m.db = db

	return m
}

// logReplication reports what mysqld itself made of the stream, which says far more
// about a rejected event than SHOW REPLICA STATUS does.
func (m *mysqld) logReplication() {
	id := m.ctr.GetContainerID()[:12]

	logs, err := m.ctr.Logs(context.Background())
	if err != nil {
		m.t.Logf("mysqld %s: cannot read log: %v", id, err)
		return
	}
	defer logs.Close() //nolint:errcheck

	out, err := io.ReadAll(logs)
	if err != nil {
		m.t.Logf("mysqld %s: cannot read log: %v", id, err)
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
	m.t.Logf("mysqld %s replication log:\n%s", id, strings.Join(kept, "\n"))
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

// rows returns each row keyed by column name, so the helpers keep working on server
// versions that add columns.
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

// heartbeatsReceived is how many heartbeats the replica has accepted on a channel,
// the only direct evidence that the source is sending them.
func (m *mysqld) heartbeatsReceived(channel string) int {
	m.t.Helper()

	n, err := strconv.Atoi(m.str(
		"SELECT COUNT_RECEIVED_HEARTBEATS FROM performance_schema.replication_connection_status" +
			" WHERE CHANNEL_NAME = '" + channel + "'"))
	require.NoError(m.t, err)

	return n
}

// copyBinlogs copies the binary logs and their index out of a running mysqld, so the
// newest file is still open and its tail may be torn: the state a failover source is
// always in.
func (m *mysqld) copyBinlogs(dir string) {
	m.t.Helper()

	var names []string
	for _, row := range m.rows("SHOW BINARY LOGS") {
		names = append(names, row["Log_name"])
	}
	require.NotEmpty(m.t, names, "mysqld has no binary logs")

	for _, name := range append(names, "binlog.index") {
		m.copyOut("/var/lib/mysql/"+name, filepath.Join(dir, name))
	}
}

func (m *mysqld) copyOut(src, dst string) {
	m.t.Helper()

	r, err := m.ctr.CopyFileFromContainer(context.Background(), src)
	require.NoErrorf(m.t, err, "copy %s out of the container", src)
	defer r.Close() //nolint:errcheck

	f, err := os.Create(dst)
	require.NoError(m.t, err)
	defer f.Close() //nolint:errcheck

	_, err = io.Copy(f, r)
	require.NoErrorf(m.t, err, "write %s", dst)
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close() //nolint:errcheck

	return ln.Addr().(*net.TCPAddr).Port
}
