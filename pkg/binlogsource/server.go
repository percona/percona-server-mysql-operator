package binlogsource

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const serverVersion = "8.4.0"

type Config struct {
	IndexPath string
	User      string
	Password  string
	ServerID  uint32
	TLS       *tls.Config
}

// Server serves binary log files on disk to a replica over the MySQL replication protocol
type Server struct {
	srv *server.Server

	cfg  Config
	uuid string

	// index is the list of logs to serve, read once.
	index func() ([]string, error)

	// scans caches one pass over each binary log. The set the source advertises and
	// the offset it serves a log to both come out of that one pass, so the stream and
	// the set are the same snapshot by construction. A source only ever serves a
	// fenced primary, so no answer can change once read.
	mu    sync.Mutex
	scans map[string]func() (*binlogScan, error)
}

// New returns a server serving the binary logs listed in cfg.IndexPath.
//
// caching_sha2_password needs an RSA key to encrypt the password on the wire and
// go-mysql takes it from the TLS certificate, so a config without one is rejected here
// rather than left to panic inside go-mysql.
func New(cfg Config) (*Server, error) {
	if cfg.TLS == nil || len(cfg.TLS.Certificates) == 0 {
		return nil, errors.New("TLS config with at least one certificate is required")
	}

	if cfg.ServerID == 0 {
		cfg.ServerID = 1
	}

	s := &Server{
		cfg:   cfg,
		uuid:  uuid.NewString(),
		srv:   server.NewServer(serverVersion, mysql.DEFAULT_COLLATION_ID, mysql.AUTH_CACHING_SHA2_PASSWORD, nil, cfg.TLS),
		scans: make(map[string]func() (*binlogScan, error)),
	}
	s.index = sync.OnceValues(func() ([]string, error) { return readIndex(cfg.IndexPath) })

	return s, nil
}

// Serve accepts replicas on ln until ctx is cancelled or the listener fails.
//
// It does not return until the listener is closed and every connection it accepted has
// gone, so a caller that waits for it can listen on the same address again. The
// operator relies on that when it retries a failover on the source's fixed port.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	// A context of Serve's own, so the goroutines below end when Serve does and not
	// only when the caller's context is cancelled.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Closing the listener is the only thing that unblocks Accept.
	go func() {
		<-ctx.Done()
		ln.Close() //nolint:errcheck
	}()

	var conns sync.WaitGroup
	err := s.accept(ctx, ln, &conns)

	// Cancelled first, so the connections are torn down rather than waited on until
	// the replicas holding them hang up of their own accord. Closed here as well as
	// above, since a failed Accept leaves the goroutine no reason to have run yet.
	cancel()
	ln.Close() //nolint:errcheck
	conns.Wait()

	return err
}

// accept hands every connection to a goroutine of its own, counted in conns so that
// Serve can wait for them.
func (s *Server) accept(ctx context.Context, ln net.Listener, conns *sync.WaitGroup) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.Wrap(err, "accept")
		}

		conns.Go(func() {
			s.handle(ctx, c)
		})
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	defer c.Close() //nolint:errcheck

	log := logf.FromContext(ctx).WithName("binlogsource").WithValues("replica", c.RemoteAddr().String())
	log.Info("replica connected")

	// A context of this connection's own, so the dump and the heartbeats that follow
	// it end when the connection does and not only when the whole source is torn down.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The dump this connection goes on to serve says which replica it is serving.
	ctx = logf.IntoContext(ctx, log)

	// Closing the connection is what unblocks HandleCommand: it sits in a read waiting
	// for the replica's next command, which a cancelled context does nothing to.
	go func() {
		<-ctx.Done()
		c.Close() //nolint:errcheck
	}()

	conn, err := s.srv.NewConn(c, s.cfg.User, s.cfg.Password, newHandler(ctx, s))
	if err != nil {
		log.Error(err, "failed to handshake with the replica")
		return
	}

	for ctx.Err() == nil {
		if err := conn.HandleCommand(); err != nil {
			if hungUp(ctx, err) {
				log.Info("replica disconnected", "reason", err.Error())
				return
			}
			log.Error(err, "failed to handle command")
			return
		}

		// A replica that quits leaves the connection closed behind it, and another
		// command read off it only ever fails.
		if conn.Closed() {
			log.Info("replica disconnected")
			return
		}
	}
}

// hungUp reports whether an error is the replica going away or the source being torn
// down, rather than a failure of the source. Both are ordinary: the operator drops the
// source once the new primary has what it needs.
func hungUp(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, mysql.ErrBadConn) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF)
}

// scanOf returns the pass over one binary log a dump reads its bounds from, reading
// the file the first time it is asked for. A log is scanned once however many dumps
// serve it, so a replica that reconnects does not have every file read again.
func (s *Server) scanOf(file string) (*binlogScan, error) {
	s.mu.Lock()
	scan, ok := s.scans[file]
	if !ok {
		scan = sync.OnceValues(func() (*binlogScan, error) { return scanBinlog(file) })
		s.scans[file] = scan
	}
	s.mu.Unlock()

	// Called outside the lock: a scan reads a whole file, and two dumps serving
	// different logs have no reason to wait for one another.
	return scan()
}

// newestScan is the pass over the newest binary log, which is where the set the source
// advertises comes from.
func (s *Server) newestScan() (*binlogScan, error) {
	files, err := s.index()
	if err != nil {
		return nil, err
	}
	return s.scanOf(files[len(files)-1])
}

func (s *Server) ExecutedGTIDSet() (string, error) {
	sc, err := s.newestScan()
	if err != nil {
		return "", err
	}
	return sc.gtids.String(), nil
}

func (s *Server) answer(query string) (*mysql.Result, error) {
	q := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(query, ";")))
	// A replica asks for these either way round, and the two spellings mean the same
	// thing below, so only one has to be matched on.
	q = strings.ReplaceAll(q, "@@global.", "@@")

	switch {
	// A SET is never a request for a value, and some of them name variables the cases
	// below match on.
	case strings.HasPrefix(q, "set "):
		return nil, nil
	case strings.Contains(q, "@@server_id"), strings.Contains(q, "'server_id'"):
		return oneRow("SERVER_ID", strconv.FormatUint(uint64(s.cfg.ServerID), 10))
	case strings.Contains(q, "@@server_uuid"):
		return oneRow("SERVER_UUID", s.uuid)
	case strings.Contains(q, "@@gtid_mode"):
		return oneRow("GTID_MODE", "ON")
	case strings.Contains(q, "@@gtid_executed"):
		set, err := s.ExecutedGTIDSet()
		if err != nil {
			return nil, err
		}
		return oneRow("GTID_EXECUTED", set)
	case strings.Contains(q, "version()"), strings.Contains(q, "@@version"):
		return oneRow("VERSION()", serverVersion)
	case strings.Contains(q, "unix_timestamp"):
		return oneRow("UNIX_TIMESTAMP()", "0")
	case strings.Contains(q, "binlog_checksum"):
		// A replica sizes the trailer it expects by this answer, and the events it
		// gets carry whatever the log's format description event says. A constant here
		// would be four bytes out on any log written with binlog_checksum=NONE.
		sc, err := s.newestScan()
		if err != nil {
			return nil, err
		}
		if !sc.checksum {
			return oneRow("BINLOG_CHECKSUM", "NONE")
		}
		return oneRow("BINLOG_CHECKSUM", "CRC32")
	}
	return nil, nil
}

func oneRow(name, value string) (*mysql.Result, error) {
	rs, err := mysql.BuildSimpleResultset([]string{name}, [][]any{{value}}, false)
	if err != nil {
		return nil, err
	}
	return mysql.NewResult(rs), nil
}
