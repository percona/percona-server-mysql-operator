package failover

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogsource"
	tlsutil "github.com/percona/percona-server-mysql-operator/pkg/tls"
)

var testIndexPath = filepath.Join("..", "..", "..", "..", "pkg", "binlogsource", "testdata", "binlog.index")

// writeCertDir writes the certificate mount the handler reads in the pod. The binlog
// source needs an RSA certificate for caching_sha2_password.
func writeCertDir(t *testing.T) string {
	t.Helper()

	_, cert, key, err := tlsutil.IssueCerts([]string{"localhost"})
	if err != nil {
		t.Fatalf("issue certs: %v", err)
	}

	dir := t.TempDir()
	for name, data := range map[string][]byte{"tls.crt": cert, "tls.key": key} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// newTestHandler points the handler at fixtures and an ephemeral port, so it can run
// outside a pod and alongside itself.
func newTestHandler(t *testing.T, tlsDir string) http.Handler {
	t.Helper()

	return newHandler(&failoverHandler{
		indexPath:  testIndexPath,
		listenAddr: "127.0.0.1:0",
		tlsDir:     tlsDir,
		getSecret:  func(apiv1.SystemUser) (string, error) { return "replpass", nil },
	})
}

func sourceIsListening(t *testing.T, port int32) bool {
	t.Helper()

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		return false
	}
	c.Close() //nolint:errcheck
	return true
}

func TestStartSourceReportsTheExecutedSet(t *testing.T) {
	h := newTestHandler(t, writeCertDir(t))

	req := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rr.Code, rr.Body)
	}

	var resp SourceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExecutedGTIDSet == "" {
		t.Error("want a non-empty executed set")
	}
	if resp.Port == 0 {
		t.Fatal("want the port the source listens on")
	}
	if !sourceIsListening(t, resp.Port) {
		t.Errorf("nothing is listening on the reported port %d", resp.Port)
	}
}

func TestStartSourceIsIdempotent(t *testing.T) {
	h := newTestHandler(t, writeCertDir(t))

	var first SourceResponse
	for i := range 2 {
		req := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("call %d: want 200, got %d (%s)", i, rr.Code, rr.Body)
		}

		var resp SourceResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("call %d: unmarshal: %v", i, err)
		}
		if i == 0 {
			first = resp
			continue
		}
		if resp.Port != first.Port {
			t.Errorf("want the same port on both calls, got %d then %d", first.Port, resp.Port)
		}
	}
}

func TestStopSourceReleasesThePort(t *testing.T) {
	h := newTestHandler(t, writeCertDir(t))

	post := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != http.StatusOK {
		t.Fatalf("start: want 200, got %d (%s)", rr.Code, rr.Body)
	}

	var started SourceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(started.Port)))

	del := httptest.NewRequest(http.MethodDelete, "/failover/source", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, del)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop: want 200, got %d (%s)", rr.Code, rr.Body)
	}

	// DELETE answers only once the source is down, so the port is free by now.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port is still bound after the source was stopped: %v", err)
	}
	ln.Close() //nolint:errcheck

	post = httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != http.StatusOK {
		t.Fatalf("restart: want 200, got %d (%s)", rr.Code, rr.Body)
	}
}

// A replica connected when the source is stopped must not keep the port alive: it sits
// in a read that only closing the connection ends, and the operator retries a failover
// on the same fixed port.
func TestStopSourceDrainsAConnectedReplica(t *testing.T) {
	h := newTestHandler(t, writeCertDir(t))

	post := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != http.StatusOK {
		t.Fatalf("start: want 200, got %d (%s)", rr.Code, rr.Body)
	}

	var started SourceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(started.Port)))

	// Connected and saying nothing.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial the source: %v", err)
	}
	defer c.Close() //nolint:errcheck

	stopped := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/failover/source", nil))
		stopped <- rec.Code
	}()

	select {
	case code := <-stopped:
		if code != http.StatusOK {
			t.Fatalf("stop: want 200, got %d", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stopping the source never finished with a replica connected")
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port is still bound after the source was stopped: %v", err)
	}
	ln.Close() //nolint:errcheck
}

// sourceTLSConfig reads the mount the operator provides in the sidecar, so it has to
// keep working against the file names the cluster secret uses.
func TestSourceTLSConfigLoadsTheClusterCertificate(t *testing.T) {
	cfg, err := sourceTLSConfig(writeCertDir(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("want one certificate, got %d", len(cfg.Certificates))
	}

	// binlogsource needs the certificate to carry an RSA key.
	if _, err := binlogsource.New(binlogsource.Config{
		IndexPath: testIndexPath,
		TLS:       cfg,
	}); err != nil {
		t.Errorf("binlog source rejected the loaded config: %v", err)
	}
}

func TestSourceTLSConfigFailsWithoutAMount(t *testing.T) {
	if _, err := sourceTLSConfig(t.TempDir()); err == nil {
		t.Error("want an error when the TLS mount is absent")
	}
}

// A missing certificate used to panic inside go-mysql and sever the connection without
// a status, rather than reporting an error.
func TestSourceFailsWithoutACertificate(t *testing.T) {
	// An empty directory: the mount is there, the certificate is not.
	h := newTestHandler(t, t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d (%s)", rr.Code, rr.Body)
	}
}
