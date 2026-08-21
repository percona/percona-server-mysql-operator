package binlogsource

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	replicationUser     = "replication"
	replicationPassword = "replpass"
	failoverChannel     = "failover"
)

// serve starts the source and returns the port it listens on. The replica
// shares the host network namespace, so loopback is enough.
func serve(t *testing.T, indexPath string, sourceTLS *tls.Config) int {
	t.Helper()

	srv, err := New(Config{
		IndexPath: indexPath,
		User:      replicationUser,
		Password:  replicationPassword,
		ServerID:  999,
		TLS:       sourceTLS,
	})
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, ln) //nolint:errcheck

	return ln.Addr().(*net.TCPAddr).Port
}

// binlogFixture fills a mysqld with transactions spread over several binary
// logs, copies them out and returns the directory and the GTID set they hold.
func binlogFixture(t *testing.T, source *mysqld) (dir, gtidSet string) {
	t.Helper()

	source.exec("CREATE DATABASE app")
	source.exec("CREATE TABLE app.t (id INT PRIMARY KEY, v VARCHAR(32))")
	source.exec("INSERT INTO app.t VALUES (1, 'a'), (2, 'b')")
	// Rotate so the stream has to cross a file boundary.
	source.exec("FLUSH BINARY LOGS")
	source.exec("INSERT INTO app.t VALUES (3, 'c')")
	source.exec("FLUSH BINARY LOGS")
	source.exec("INSERT INTO app.t VALUES (4, 'd')")
	source.exec("UPDATE app.t SET v = 'dd' WHERE id = 4")

	dir = t.TempDir()
	source.copyBinlogs(dir)

	return dir, source.str("SELECT @@global.gtid_executed")
}

func startReplication(t *testing.T, replica *mysqld, port int) {
	t.Helper()

	// A short timeout so the test finds out quickly if the source lets the
	// connection go quiet, and a heartbeat period well inside it.
	replica.exec("SET GLOBAL replica_net_timeout = 4")

	replica.exec(fmt.Sprintf(`CHANGE REPLICATION SOURCE TO
		SOURCE_HOST = '%s',
		SOURCE_PORT = %d,
		SOURCE_USER = '%s',
		SOURCE_PASSWORD = '%s',
		SOURCE_SSL = 1,
		SOURCE_SSL_CA = '%[5]s/ca.crt',
		SOURCE_SSL_CERT = '%[5]s/tls.crt',
		SOURCE_SSL_KEY = '%[5]s/tls.key',
		SOURCE_AUTO_POSITION = 1,
		SOURCE_HEARTBEAT_PERIOD = 1
		FOR CHANNEL '%[6]s'`,
		sourceHost, port, replicationUser, replicationPassword, replicaTLSDir, failoverChannel))
	replica.exec("START REPLICA FOR CHANNEL '" + failoverChannel + "'")
}

// waitForReplica blocks until the replica has executed everything in want, and
// fails as soon as either replication thread reports an error.
func waitForReplica(t *testing.T, replica *mysqld, want string) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	for {
		st := replica.replicaStatus(failoverChannel)
		require.Equalf(t, "0", st["Last_IO_Errno"], "replica IO thread failed: %s", st["Last_IO_Error"])
		require.Equalf(t, "0", st["Last_SQL_Errno"], "replica SQL thread failed: %s", st["Last_SQL_Error"])

		if replica.str("SELECT GTID_SUBSET('"+want+"', @@global.gtid_executed)") == "1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("replica did not catch up: has %q, want %q",
				replica.str("SELECT @@global.gtid_executed"), want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// A real mysqld replica must apply every transaction the source serves. It
// keeps a relay log, so it rejects a stream that a bare protocol client would
// happily read.
func TestRealReplicaAppliesEverythingTheSourceServes(t *testing.T) {
	requireDocker(t)

	certDir := t.TempDir()
	sourceTLS := writeCertDir(t, certDir, sourceHost, "localhost")

	source := startMySQLD(t, 1001)
	replica := startMySQLD(t, 1002, certDir+":"+replicaTLSDir+":ro")

	dir, want := binlogFixture(t, source)
	port := serve(t, filepath.Join(dir, "binlog.index"), sourceTLS)

	startReplication(t, replica, port)
	waitForReplica(t, replica, want)

	require.Equal(t, "dd", replica.str("SELECT v FROM app.t WHERE id = 4"))

	// Nothing more will ever be written to the source's logs, so the connection
	// now goes quiet. Heartbeats have to keep it alive, or the replica times it
	// out and reconnects in a loop while the operator is still waiting.
	waitFor(t, time.Minute, "the replica to receive a heartbeat", func() bool {
		return replica.heartbeatsReceived(failoverChannel) > 0
	})

	st := replica.replicaStatus(failoverChannel)
	assert.Equal(t, "Yes", st["Replica_IO_Running"])
	assert.Equal(t, "0", st["Last_IO_Errno"], st["Last_IO_Error"])
}
