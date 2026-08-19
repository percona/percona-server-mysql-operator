package failover

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/secrets"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogsource"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type SourceResponse struct {
	ExecutedGTIDSet string `json:"executedGtidSet"`
	Port            int32  `json:"port"`
}

// runningSource is the source currently serving the binary logs. A nil one is the
// single "not running" state, so starting and stopping cannot leave a stale cancel
// behind a cleared server.
type runningSource struct {
	srv    *binlogsource.Server
	port   int32
	cancel context.CancelFunc

	// done is closed once Serve has returned, which is once the port is free.
	done chan struct{}
}

type failoverHandler struct {
	mu      sync.Mutex
	current *runningSource

	indexPath  string
	listenAddr string
	tlsDir     string

	getSecret func(apiv1.SystemUser) (string, error)
}

func Handler() http.Handler {
	return newHandler(&failoverHandler{
		indexPath:  filepath.Join(mysql.DataMountPath, "binlog.index"),
		listenAddr: ":" + strconv.Itoa(int(mysql.BinlogSourcePort)),
		tlsDir:     mysql.TLSMountPath,
		getSecret:  secrets.Get,
	})
}

func newHandler(h *failoverHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/failover/source", h.source)

	return mux
}

// sourceTLSConfig loads the cluster certificate the replica already trusts, since
// mysqld serves the same one.
func sourceTLSConfig(dir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(dir, "tls.crt"),
		filepath.Join(dir, "tls.key"),
	)
	if err != nil {
		return nil, errors.Wrap(err, "load key pair")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func (h *failoverHandler) source(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.startSource(w)
	case http.MethodDelete:
		h.stopSource(w)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// startSource starts the binlog source if it is not already running and reports the
// transactions it holds. The operator polls this, so a repeat call answers from the
// running source rather than building a second one.
func (h *failoverHandler) startSource(w http.ResponseWriter) {
	log := logf.Log.WithName("failover-source")

	src, started, err := h.running()
	if err != nil {
		log.Error(err, "failed to start the binlog source")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	set, err := src.srv.ExecutedGTIDSet()
	if err != nil {
		log.Error(err, "failed to read the transactions the binary logs hold", "index", h.indexPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if started {
		// The set the operator picks a new primary from, and the one the source goes on
		// to serve it.
		log.Info("binlog source started", "port", src.port, "gtidSet", set)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SourceResponse{ //nolint:errcheck
		ExecutedGTIDSet: set,
		Port:            src.port,
	})
}

// running returns the source serving the binary logs, starting one on the first call.
// The second return value says whether this call is the one that started it.
func (h *failoverHandler) running() (*runningSource, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.current != nil {
		return h.current, false, nil
	}

	pass, err := h.getSecret(apiv1.UserReplication)
	if err != nil {
		return nil, false, err
	}
	tlsConfig, err := sourceTLSConfig(h.tlsDir)
	if err != nil {
		return nil, false, err
	}
	srv, err := binlogsource.New(binlogsource.Config{
		IndexPath: h.indexPath,
		User:      string(apiv1.UserReplication),
		Password:  pass,
		TLS:       tlsConfig,
	})
	if err != nil {
		return nil, false, err
	}

	ln, err := net.Listen("tcp", h.listenAddr)
	if err != nil {
		return nil, false, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close() //nolint:errcheck
		return nil, false, errors.New("binlog source listener is not TCP")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.current = &runningSource{srv: srv, port: int32(addr.Port), cancel: cancel, done: done}

	log := logf.Log.WithName("failover-source")
	go func() {
		defer close(done)
		if err := srv.Serve(ctx, ln); err != nil {
			log.Error(err, "binlog source failed")
		}
	}()

	return h.current, true, nil
}

// stopSource stops the source and answers only once it is down. The operator retries a
// failover on the same fixed port, so a 200 that arrives while the listener is still
// open buys it an EADDRINUSE. The lock is held across the wait on purpose: a start that
// arrives meanwhile belongs after the teardown, not beside it.
func (h *failoverHandler) stopSource(w http.ResponseWriter) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.current != nil {
		h.current.cancel()
		<-h.current.done
		h.current = nil
		logf.Log.WithName("failover-source").Info("binlog source stopped")
	}
	w.WriteHeader(http.StatusOK)
}
