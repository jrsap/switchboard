package router

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/child"
	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/orgs"
	"github.com/jrsap/switchboard/internal/seal"
)

func newChildServer(t *testing.T, orgID, token string) *child.Server {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := orgdb.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	return child.New(orgID, token, db)
}

// makeOrg creates an org DB at its derived path under dataDir, with
// wrappedKey in its meta (empty = no meta row).
func makeOrg(t *testing.T, dataDir, orgID, wrappedKey string) {
	t.Helper()
	dbPath := orgs.OrgDBPath(dataDir, orgID)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	db, err := orgdb.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if wrappedKey != "" {
		if err := db.SetMeta(orgdb.MetaWrappedDataKey, wrappedKey); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
}

func testConfig(dataDir string) *config.Config {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return &config.Config{
		Listen:  "127.0.0.1:0",
		RootKey: rootKey,
		DataDir: dataDir,
	}
}

// pointAt retargets an org's reverse proxy at addr (tests only; New
// allocates a fresh loopback addr per org).
func pointAt(srv *Server, orgID, addr string) {
	target := &url.URL{Scheme: "http", Host: addr}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, http.StatusBadGateway, "upstream unavailable", "upstream_error")
	}
	srv.proxies[orgID] = rp
}

func TestNewDiscoversOrgsFromDataDir(t *testing.T) {
	dataDir := t.TempDir()
	wrapped := base64.StdEncoding.EncodeToString(make([]byte, 72))
	makeOrg(t, dataDir, "beta", wrapped)
	makeOrg(t, dataDir, "acme", wrapped)

	srv, err := New(testConfig(dataDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ids := srv.OrgIDs()
	if len(ids) != 2 || ids[0] != "acme" || ids[1] != "beta" {
		t.Fatalf("OrgIDs = %v", ids)
	}

	// An org DB without a wrapped data key in its meta fails at New.
	makeOrg(t, dataDir, "broken", "")
	if _, err := New(testConfig(dataDir)); err == nil {
		t.Fatal("org DB without wrapped data key accepted")
	}

	// No orgs at all is fine; a missing data dir is fine too.
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "nope")} {
		srv, err := New(testConfig(dir))
		if err != nil || len(srv.OrgIDs()) != 0 {
			t.Fatalf("dataDir %s: orgs=%v err=%v", dir, srv.OrgIDs(), err)
		}
	}
}

func TestRoutesToOrgChild(t *testing.T) {
	dataDir := t.TempDir()
	makeOrg(t, dataDir, "acme", base64.StdEncoding.EncodeToString(make([]byte, 72)))

	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Child", "acme")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(r.Method + " " + r.URL.Path + " " + string(body)))
	}))
	defer child.Close()

	srv, err := New(testConfig(dataDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pointAt(srv, "acme", strings.TrimPrefix(child.URL, "http://"))
	srv.setReady("acme", true)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"big"}`))
	req.Header.Set("Authorization", "Bearer sb_acme_somesecret")
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Child") != "acme" {
		t.Fatalf("expected request to reach acme child, got %q", rr.Header().Get("X-Child"))
	}
	if !strings.Contains(rr.Body.String(), "POST /v1/chat/completions") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestRejectsBadKeys(t *testing.T) {
	dataDir := t.TempDir()
	makeOrg(t, dataDir, "acme", base64.StdEncoding.EncodeToString(make([]byte, 72)))

	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("child must not be reached")
	}))
	defer child.Close()
	srv, err := New(testConfig(dataDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := map[string]string{
		"no header":           "",
		"wrong scheme":        "Basic sb_acme_x",
		"no prefix":           "Bearer acme_x",
		"unknown org":         "Bearer sb_other_x", // no org DB for "other"
		"empty secret":        "Bearer sb_acme_",
		"bad org id":          "Bearer sb_ACME_x",
		"no secret separator": "Bearer sb_acme",
	}
	for name, auth := range cases {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rr.Code)
		}
	}
}

func TestHealthz(t *testing.T) {
	srv, err := New(testConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rr.Code)
	}
}

func TestUpstreamErrorIs502(t *testing.T) {
	dataDir := t.TempDir()
	makeOrg(t, dataDir, "acme", base64.StdEncoding.EncodeToString(make([]byte, 72)))
	srv, err := New(testConfig(dataDir)) // allocated child addr: nothing listening
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.setReady("acme", true)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sb_acme_x")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}

func TestOrgNotReadyIs503(t *testing.T) {
	dataDir := t.TempDir()
	makeOrg(t, dataDir, "acme", base64.StdEncoding.EncodeToString(make([]byte, 72)))
	srv, err := New(testConfig(dataDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sb_acme_x")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandshakeAgainstChild(t *testing.T) {
	// The loopback spawn handshake contract, exercised against a real
	// child handler over HTTP.
	key, _ := seal.NewKey()
	token, _ := seal.NewKey()
	srv := newChildServer(t, "acme", base64.StdEncoding.EncodeToString(token))
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	addr := strings.TrimPrefix(httpSrv.URL, "http://")

	r, err := New(testConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.waitHealthy(context.Background(), addr); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	if err := r.handshake(context.Background(), addr, token, key); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !srv.Ready() {
		t.Fatal("child not ready after handshake")
	}
	if got := srv.DataKey(); !bytes.Equal(got, key) {
		t.Fatal("child received wrong data key")
	}
	// Wrong token must fail.
	bad, _ := seal.NewKey()
	if err := r.handshake(context.Background(), addr, bad, key); err == nil {
		t.Fatal("handshake with wrong token accepted")
	}
}
