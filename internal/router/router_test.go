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

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/child"
	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/orgpath"
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
	dbPath := orgpath.DBPath(dataDir, orgID)
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

// makeKey creates a member and a valid API key in an org DB and returns the
// full key.
func makeKey(t *testing.T, db *orgdb.DB, orgID, username string) string {
	t.Helper()
	member, err := db.AddMember(username, "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, err := apikey.New(orgID)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, _ := apikey.Parse(key)
	if _, err := db.AddAPIKey(member.ID, "e2e", apikey.HashSecret(secret)); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestKeysVerifiedAgainstOrgDB is the e2e auth path: a valid key is
// demultiplexed by the router and authenticated by the child against its own
// org DB (404 = past auth, no org API endpoints yet); a key of another org
// routed to the wrong child fails closed (401); a revoked key is rejected
// (401); repeated failures hit the child's rate limit (429).
func TestKeysVerifiedAgainstOrgDB(t *testing.T) {
	dataDir := t.TempDir()
	wrapped := base64.StdEncoding.EncodeToString(make([]byte, 72))
	makeOrg(t, dataDir, "acme", wrapped)
	makeOrg(t, dataDir, "beta", wrapped)
	srv, err := New(testConfig(dataDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serve := func(orgID string) *orgdb.DB {
		sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(":memory:"))
		if err != nil {
			t.Fatal(err)
		}
		db, err := orgdb.Open(sqlDB)
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(child.New(orgID, "spawn-token-"+orgID, db).Handler())
		t.Cleanup(func() { ts.Close(); sqlDB.Close() })
		// Deliver the data key like the router does at spawn, so the child
		// serves its org API instead of 503.
		dataKey, _ := seal.NewKey()
		body := `{"data_key":"` + base64.StdEncoding.EncodeToString(dataKey) + `"}`
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/internal/handshake", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer spawn-token-"+orgID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("handshake with %s child: %v %v", orgID, err, resp)
		}
		resp.Body.Close()
		pointAt(srv, orgID, strings.TrimPrefix(ts.URL, "http://"))
		srv.setReady(orgID, true)
		return db
	}
	acmeDB := serve("acme")
	betaDB := serve("beta")

	acmeKey := makeKey(t, acmeDB, "acme", "alice")
	betaKey := makeKey(t, betaDB, "beta", "bob")

	do := func(key string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		return rr.Code
	}

	// Valid key: through the router, past child auth, into the org API.
	if code := do(acmeKey); code != http.StatusNotFound {
		t.Fatalf("valid acme key: %d, want 404 (past auth)", code)
	}
	if code := do(betaKey); code != http.StatusNotFound {
		t.Fatalf("valid beta key: %d, want 404 (past auth)", code)
	}

	// Wrong-org key: the router demultiplexes by prefix, the child that
	// receives it rejects it against its own org DB (fail closed).
	if code := do(acmeKey[:len("sb_acme")] + "_notasecret"); code != http.StatusUnauthorized {
		t.Fatalf("bad acme secret: %d, want 401", code)
	}

	// Revoked key: rejected by the child that owns it.
	keys, err := acmeDB.ListAPIKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if err := acmeDB.RevokeAPIKey(keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if code := do(acmeKey); code != http.StatusUnauthorized {
		t.Fatalf("revoked key: %d, want 401", code)
	}

	// Repeated auth failures through the router trip the child's rate limit
	// (limit is 10 failures per key prefix and source within a minute; the
	// two failures above already count).
	for i := 0; i < 8; i++ {
		if code := do(acmeKey[:len("sb_acme")] + "_wrongsecret"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d, want 401", i, code)
		}
	}
	if code := do(acmeKey[:len("sb_acme")] + "_wrongsecret"); code != http.StatusTooManyRequests {
		t.Fatalf("after repeated failures: %d, want 429", code)
	}
	// The other org's child is unaffected.
	if code := do(betaKey); code != http.StatusNotFound {
		t.Fatalf("beta child after acme failures: %d, want 404", code)
	}
}
