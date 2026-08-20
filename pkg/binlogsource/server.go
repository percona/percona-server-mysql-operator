package binlogsource

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/google/uuid"
	"github.com/pkg/errors"
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
	server.EmptyReplicationHandler

	_server *server.Server

	cfg  Config
	uuid string

	mu    sync.Mutex
	conns []*server.Conn
}

// New returns a server serving the binary logs listed in cfg.IndexPath.
//
// caching_sha2_password needs an RSA key to encrypt the password on the wire and
// go-mysql takes it from the TLS certificate, so a config without one is rejected
// here: go-mysql itself panics on it.
func New(cfg Config) (*Server, error) {
	if cfg.TLS == nil || len(cfg.TLS.Certificates) == 0 {
		return nil, errors.New("TLS config with at least one certificate is required")
	}

	if cfg.ServerID == 0 {
		cfg.ServerID = 1
	}

	return &Server{
		cfg:     cfg,
		uuid:    uuid.NewString(),
		_server: server.NewServer(serverVersion, mysql.DEFAULT_COLLATION_ID, mysql.AUTH_CACHING_SHA2_PASSWORD, nil, cfg.TLS),
	}, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close() //nolint:errcheck
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.Wrap(err, "accept")
		}
		go s.handle(ctx, c)
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	defer c.Close() //nolint:errcheck

	conn, err := s._server.NewConn(c, s.cfg.User, s.cfg.Password, Handler{server: s})
	if err != nil {
		log.Printf("ERROR: failed to handle connection: %v", err)
		return
	}

	for ctx.Err() == nil {
		if err := conn.HandleCommand(); err != nil {
			log.Printf("ERROR: failed to handle command: %v", err)
			return
		}
	}
}

func (s *Server) ExecutedGTIDSet() (string, error) {
	idx, err := ReadIndex(s.cfg.IndexPath)
	if err != nil {
		return "", err
	}
	set, err := ExecutedGTIDs(idx)
	if err != nil {
		return "", err
	}
	return set.String(), nil
}

func (s *Server) answer(query string) (*mysql.Result, error) {
	q := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(query, ";")))

	switch {
	case strings.Contains(q, "@@global.server_id"), strings.Contains(q, "'server_id'"):
		return oneRow("SERVER_ID", strconv.FormatUint(uint64(s.cfg.ServerID), 10))
	case strings.Contains(q, "@@global.server_uuid"), strings.Contains(q, "@@server_uuid"):
		return oneRow("SERVER_UUID", s.uuid)
	case strings.Contains(q, "@@global.gtid_mode"), strings.Contains(q, "@@gtid_mode"):
		return oneRow("GTID_MODE", "ON")
	case strings.Contains(q, "@@global.gtid_executed"), strings.Contains(q, "@@gtid_executed"):
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
		return oneRow("BINLOG_CHECKSUM", "CRC32")
	case strings.HasPrefix(q, "set "):
		return nil, nil
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
