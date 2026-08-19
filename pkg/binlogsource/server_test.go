package binlogsource

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tlsutil "github.com/percona/percona-server-mysql-operator/pkg/tls"
)

const testCertHost = "localhost"

func issueTestCerts(t *testing.T) (serverTLS, replicaTLS *tls.Config) {
	t.Helper()

	caCert, cert, key, err := tlsutil.IssueCerts([]string{testCertHost})
	require.NoError(t, err)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caCert))

	replicaTLS = &tls.Config{
		RootCAs:    pool,
		ServerName: testCertHost,
		MinVersion: tls.VersionTLS12,
	}

	return serverTLSConfig(t, cert, key), replicaTLS
}

// serverTLSConfig has to carry an RSA certificate: go-mysql takes the key
// caching_sha2_password needs from it.
func serverTLSConfig(t *testing.T, cert, key []byte) *tls.Config {
	t.Helper()

	keyPair, err := tls.X509KeyPair(cert, key)
	require.NoError(t, err)

	return &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		MinVersion:   tls.VersionTLS12,
	}
}

func testSource(t *testing.T, indexPath string) *Server {
	t.Helper()

	serverTLS, _ := issueTestCerts(t)
	srv, err := New(Config{IndexPath: indexPath, ServerID: 999, TLS: serverTLS})
	require.NoError(t, err)

	return srv
}

// A replica announces its checksum awareness with a SET and expects an OK packet.
// Answering with a result set leaves the connection out of step.
func TestChecksumAnnouncementIsAcknowledgedNotAnswered(t *testing.T) {
	srv := testSource(t, filepath.Join("testdata", "binlog.index"))

	res, err := srv.answer("SET @master_binlog_checksum = @@global.binlog_checksum, " +
		"@source_binlog_checksum = @@global.binlog_checksum")
	require.NoError(t, err)
	assert.Nil(t, res, "a SET must be acknowledged, not answered with a result set")
}

// The replica reads the value back to learn which algorithm to expect. It has to match
// the trailer on the events we synthesise, or the replica reads the first rotate event
// four bytes wrong.
func TestReportedChecksumMatchesTheServedLogs(t *testing.T) {
	index := filepath.Join("testdata", "binlog.index")
	srv := testSource(t, index)

	files, err := readIndex(index)
	require.NoError(t, err)
	sc, err := scanBinlog(files[0])
	require.NoError(t, err)

	want := "NONE"
	if sc.checksum {
		want = "CRC32"
	}

	res, err := srv.answer("SELECT @source_binlog_checksum")
	require.NoError(t, err)
	require.NotNil(t, res)

	// A server-built result set carries raw rows; the client decodes them.
	require.Len(t, res.RowDatas, 1)
	row, err := res.RowDatas[0].ParseText(res.Fields, nil)
	require.NoError(t, err)
	require.Len(t, row, 1)
	assert.Equal(t, want, string(row[0].AsString()))
}

func startTestServer(t *testing.T) (host string, port uint16, replicaTLS *tls.Config) {
	t.Helper()

	serverTLS, replicaTLS := issueTestCerts(t)
	p := serve(t, filepath.Join("testdata", "binlog.index"), serverTLS)

	return "127.0.0.1", uint16(p), replicaTLS
}

// caughtUp reports whether a replica that started at start and has since received got
// now holds everything in want.
func caughtUp(t *testing.T, start mysql.GTIDSet, got *mysql.MysqlGTIDSet, want mysql.GTIDSet) bool {
	t.Helper()

	union := start.Clone()
	require.NoError(t, union.(*mysql.MysqlGTIDSet).Update(got.String()))
	return union.Contain(want)
}

// A real go-mysql replica must be able to connect, authenticate and receive exactly
// the transactions it is missing: every one of them, and none it already has.
func TestReplicaReceivesTheDelta(t *testing.T) {
	host, port, replicaTLS := startTestServer(t)

	files, err := readIndex(filepath.Join("testdata", "binlog.index"))
	require.NoError(t, err)

	last := files[len(files)-1]
	sc, err := scanBinlog(last)
	require.NoError(t, err)
	want := sc.gtids

	// Pretend the replica stopped at the start of the last file.
	replicaSet, err := previousGTIDs(last)
	require.NoError(t, err)
	// The syncer keeps the set it is given and adds to it as events arrive, so hold on
	// to our own record of where the replica started.
	start := replicaSet.Clone()

	syncer := replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
		ServerID:  1234,
		Flavor:    "mysql",
		Host:      host,
		Port:      port,
		User:      "replication",
		Password:  "replpass",
		TLSConfig: replicaTLS,
	})
	t.Cleanup(syncer.Close)

	streamer, err := syncer.StartSyncGTID(replicaSet)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Only committed transactions count, the rule the scan applies to the files. A
	// transaction announced but never committed leaves nothing to apply.
	got := mysql.NewMysqlGTIDSet()
	var (
		pendingSID uuid.UUID
		pendingGNO int64
		pending    bool
	)
	commit := func() {
		if !pending {
			return
		}
		pending = false

		one := mysql.NewMysqlGTIDSet()
		one.AddGTID(pendingSID, pendingGNO)
		assert.False(t, start.Contain(&one), "server sent %s, which the replica already had", one.String())

		got.AddGTID(pendingSID, pendingGNO)
	}

	for !caughtUp(t, start, &got, want) {
		ev, err := streamer.GetEvent(ctx)
		if err != nil {
			require.ErrorIs(t, err, context.DeadlineExceeded, "streaming failed")
			t.Fatalf("timed out before the replica caught up: received %q, source has %q", got.String(), want.String())
		}

		switch e := ev.Event.(type) {
		case *replication.GTIDEvent:
			u, err := uuid.FromBytes(e.SID)
			require.NoError(t, err)
			pendingSID, pendingGNO, pending = u, e.GNO, true
		case *replication.XIDEvent:
			commit()
		case *replication.QueryEvent:
			if strings.ToUpper(strings.TrimSpace(string(e.Query))) != "BEGIN" {
				commit()
			}
		}
	}
}

func TestNewRejectsAConfigWithoutACertificate(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no TLS config":  {IndexPath: filepath.Join("testdata", "binlog.index")},
		"no certificate": {IndexPath: filepath.Join("testdata", "binlog.index"), TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(cfg)
			require.Error(t, err)
		})
	}
}

func TestExecutedGTIDSetIsServedAsAString(t *testing.T) {
	serverTLS, _ := issueTestCerts(t)
	srv, err := New(Config{IndexPath: filepath.Join("testdata", "binlog.index"), TLS: serverTLS})
	require.NoError(t, err)

	set, err := srv.ExecutedGTIDSet()
	require.NoError(t, err)
	assert.NotEmpty(t, set)
}
