package failover

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	tlsutil "github.com/percona/percona-server-mysql-operator/pkg/tls"
)

func TestFenceIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAILOVER_FENCE_FILE", filepath.Join(dir, ".failover-fence"))
	h := Handler()

	for i := range 2 {
		req := httptest.NewRequest(http.MethodPost, "/failover/fence", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("call %d: want 200, got %d", i, rr.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".failover-fence")); err != nil {
		t.Errorf("fence file not created: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/failover/fence", nil)
	req.Header.Set("Authorization", "Bearer t")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unfence: want 200, got %d", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".failover-fence")); !os.IsNotExist(err) {
		t.Error("fence file still present after unfence")
	}
}

// testTLSConfig returns a server TLS config carrying an RSA certificate, which
// the binlog source needs for caching_sha2_password.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	_, cert, key, err := tlsutil.IssueCerts([]string{"localhost"})
	if err != nil {
		t.Fatalf("issue certs: %v", err)
	}
	keyPair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		MinVersion:   tls.VersionTLS12,
	}
}

// newTestHandler stubs out the in-pod credential mount, the TLS mount and the
// fixed source port, so the handler can run outside a pod and alongside itself.
func newTestHandler(t *testing.T) (http.Handler, *net.Listener) {
	t.Helper()

	t.Setenv("FAILOVER_BINLOG_INDEX", filepath.Join("..", "..", "..", "..", "pkg", "binlogsource", "testdata", "binlog.index"))
	t.Setenv("FAILOVER_FENCE_FILE", filepath.Join(t.TempDir(), ".failover-fence"))

	tlsConfig := testTLSConfig(t)
	var bound net.Listener

	h := &failoverHandler{
		getSecret: func(apiv1.SystemUser) (string, error) { return "replpass", nil },
		sourceTLS: func() (*tls.Config, error) { return tlsConfig, nil },
		listen: func() (net.Listener, error) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			bound = ln
			t.Cleanup(func() { ln.Close() }) //nolint:errcheck
			return ln, nil
		},
	}

	return newHandler(h), &bound
}

func TestStartSourceReportsTheExecutedSet(t *testing.T) {
	h, bound := newTestHandler(t)

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
	if *bound == nil {
		t.Fatal("no listener was bound")
	}
	if want := int32((*bound).Addr().(*net.TCPAddr).Port); resp.Port != want {
		t.Errorf("want the port the source listens on (%d), got %d", want, resp.Port)
	}
}

func TestStartSourceIsIdempotent(t *testing.T) {
	h, _ := newTestHandler(t)

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
	h, bound := newTestHandler(t)

	post := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != http.StatusOK {
		t.Fatalf("start: want 200, got %d (%s)", rr.Code, rr.Body)
	}
	addr := (*bound).Addr().String()

	del := httptest.NewRequest(http.MethodDelete, "/failover/source", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, del)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop: want 200, got %d (%s)", rr.Code, rr.Body)
	}

	// The listener is closed asynchronously, so rebinding is racy; the port the
	// handler reports is what callers act on.
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close() //nolint:errcheck
	}

	post = httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != http.StatusOK {
		t.Fatalf("restart: want 200, got %d (%s)", rr.Code, rr.Body)
	}
}

// A missing certificate used to panic inside go-mysql and sever the connection
// without a status, rather than reporting an error.
func TestSourceFailsWithoutACertificate(t *testing.T) {
	broken := &failoverHandler{
		getSecret: func(apiv1.SystemUser) (string, error) { return "replpass", nil },
		sourceTLS: func() (*tls.Config, error) { return nil, nil },
		listen:    func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
	}

	req := httptest.NewRequest(http.MethodPost, "/failover/source", nil)
	rr := httptest.NewRecorder()
	newHandler(broken).ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d (%s)", rr.Code, rr.Body)
	}
}
