package failover

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/helpers"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogsource"
)

// BinlogSourcePort is deliberately not 3306: Orchestrator polls 3306 and must
// never discover the source as a live instance.
const BinlogSourcePort int32 = 33065

const tlsMountPath = "/etc/mysql/mysql-tls-secret"

type SourceResponse struct {
	ExecutedGTIDSet string `json:"executedGtidSet"`
	Port            int32  `json:"port"`
}

type failoverHandler struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	port   int32

	getSecret func(apiv1.SystemUser) (string, error)
	listen    func() (net.Listener, error)
	sourceTLS func() (*tls.Config, error)
}

func Handler() http.Handler {
	return newHandler(&failoverHandler{})
}

func newHandler(h *failoverHandler) http.Handler {
	h.init()

	mux := http.NewServeMux()
	mux.HandleFunc("/failover/fence", h.fence)
	mux.HandleFunc("/failover/source", h.source)

	return mux
}

func (h *failoverHandler) init() {
	if h.getSecret == nil {
		h.getSecret = helpers.GetSecret
	}
	if h.listen == nil {
		h.listen = func() (net.Listener, error) {
			return net.Listen("tcp", ":"+strconv.Itoa(int(BinlogSourcePort)))
		}
	}
	if h.sourceTLS == nil {
		h.sourceTLS = sourceTLSConfig
	}
}

func fenceFile() string {
	if p := os.Getenv("FAILOVER_FENCE_FILE"); p != "" {
		return p
	}
	return "/var/lib/mysql/fenced"
}

func binlogIndex() string {
	if p := os.Getenv("FAILOVER_BINLOG_INDEX"); p != "" {
		return p
	}
	return "/var/lib/mysql/binlog.index"
}

// sourceTLSConfig loads the cluster certificate the replica already trusts,
// since mysqld serves the same one.
func sourceTLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(tlsMountPath, "tls.crt"),
		filepath.Join(tlsMountPath, "tls.key"),
	)
	if err != nil {
		return nil, errors.Wrap(err, "load key pair")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func (h *failoverHandler) fence(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		f, err := os.OpenFile(fenceFile(), os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.Close() //nolint:errcheck
	case http.MethodDelete:
		if err := os.Remove(fenceFile()); err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *failoverHandler) source(w http.ResponseWriter, r *http.Request) {
	log := logf.Log.WithName("failover-source")

	switch r.Method {
	case http.MethodPost:
		pass, err := h.getSecret(apiv1.UserReplication)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tlsConfig, err := h.sourceTLS()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		srv, err := binlogsource.New(binlogsource.Config{
			IndexPath: binlogIndex(),
			User:      string(apiv1.UserReplication),
			Password:  pass,
			TLS:       tlsConfig,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		set, err := srv.ExecutedGTIDSet()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		h.mu.Lock()
		defer h.mu.Unlock()
		if h.cancel == nil {
			ln, err := h.listen()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			addr, ok := ln.Addr().(*net.TCPAddr)
			if !ok {
				ln.Close() //nolint:errcheck
				http.Error(w, "binlog source listener is not TCP", http.StatusInternalServerError)
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			h.cancel = cancel
			h.port = int32(addr.Port)
			go func() {
				if err := srv.Serve(ctx, ln); err != nil {
					log.Error(err, "binlog source stopped")
				}
			}()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SourceResponse{ //nolint:errcheck
			ExecutedGTIDSet: set,
			Port:            h.port,
		})
	case http.MethodDelete:
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.cancel != nil {
			h.cancel()
			h.cancel = nil
			h.port = 0
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
