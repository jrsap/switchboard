package child

import (
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/seal"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(filepath.Join(t.TempDir(), "org.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := orgdb.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	return New("acme", "test-spawn-token", db)
}

func TestHealthzBeforeAndAfterHandshake(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"starting"`) {
		t.Fatalf("before handshake: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("gated endpoint before handshake: %d, want 503", rr.Code)
	}

	key, _ := seal.NewKey()
	body := `{"data_key":"` + base64.StdEncoding.EncodeToString(key) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/handshake", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-spawn-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("handshake: %d %s", rr.Code, rr.Body.String())
	}
	if !srv.Ready() {
		t.Fatal("server not ready after handshake")
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(rr.Body.String(), `"ok"`) {
		t.Fatalf("after handshake: %s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("gated endpoint after handshake: %d, want 404", rr.Code)
	}
}

func TestHandshakeRejectsBadTokenAndBadKey(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()
	key, _ := seal.NewKey()
	goodBody := `{"data_key":"` + base64.StdEncoding.EncodeToString(key) + `"}`

	for _, auth := range []string{"", "Bearer wrong", "Basic test-spawn-token"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/internal/handshake", strings.NewReader(goodBody))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q: %d, want 401", auth, rr.Code)
		}
	}
	if srv.Ready() {
		t.Fatal("server must not be ready after rejected handshakes")
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/handshake", strings.NewReader(`{"data_key":"not-base64!!"}`))
	req.Header.Set("Authorization", "Bearer test-spawn-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad key: %d, want 400", rr.Code)
	}
}
