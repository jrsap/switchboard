package child

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/seal"
)

// readyServer returns a test server past the spawn handshake plus a valid
// API key created in its org DB.
func readyServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	srv := newTestServer(t)
	h := srv.Handler()
	key, _ := seal.NewKey()
	body := `{"data_key":"` + base64.StdEncoding.EncodeToString(key) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/handshake", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-spawn-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("handshake: %d %s", rr.Code, rr.Body.String())
	}
	apiKey, err := apikey.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, _ := apikey.Parse(apiKey)
	member, err := srv.db.AddMember("alice", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.AddAPIKey(member.ID, "test", apikey.HashSecret(secret)); err != nil {
		t.Fatal(err)
	}
	return srv, h, apiKey
}

func requestWithKey(method, path, key string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	return req
}

func TestValidKeyAuthenticates(t *testing.T) {
	_, h, key := readyServer(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", key))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("valid key: %d %s, want 404 (authenticated, endpoint not implemented yet)", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rr.Body.String(), "not_found") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestWrongOrgKeyFailsClosed(t *testing.T) {
	_, h, key := readyServer(t)
	other, err := apikey.New("beta")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{other, "sb_nope_totallyvalidsecret"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", k))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("key %q: %d, want 401", apikey.Prefix("beta"), rr.Code)
		}
	}
	// The valid acme key still works after the wrong-org failures.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", key))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("valid key after wrong-org failures: %d, want 404", rr.Code)
	}
}

func TestUnknownAndMalformedKeysRejected(t *testing.T) {
	_, h, _ := readyServer(t)
	for name, auth := range map[string]string{
		"no header":     "",
		"wrong scheme":  "Basic sb_acme_x",
		"bad format":    "Bearer not-a-key",
		"empty secret":  "Bearer sb_acme_",
		"wrong secret":  "Bearer sb_acme_" + strings.Repeat("x", 43),
		"bearer prefix": "sb_acme_secret",
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rr.Code)
		}
	}
}

func TestRevokedKeyRejected(t *testing.T) {
	srv, h, key := readyServer(t)
	keys, err := srv.db.ListAPIKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListAPIKeys: %v (%d)", err, len(keys))
	}
	if err := srv.db.RevokeAPIKey(keys[0].ID); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", key))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key: %d, want 401", rr.Code)
	}
}

func TestAuthFailureRateLimit(t *testing.T) {
	srv, h, validKey := readyServer(t)
	srv.failures = newFailureLimiter(3, time.Minute)
	badKey := "sb_acme_" + strings.Repeat("y", 43)
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", badKey))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d, want 401", i, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", badKey))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("after limit: %d, want 429", rr.Code)
	}
	// Even the valid key is throttled from the same source.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", validKey))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("valid key from limited source: %d, want 429", rr.Code)
	}
	// A different source is not affected.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+validKey)
	req.RemoteAddr = "192.0.2.9:9999"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("valid key from other source: %d, want 404", rr.Code)
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := orgdb.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	member, err := db.AddMember("alice", "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, err := apikey.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, _ := apikey.Parse(key)
	k, err := db.AddAPIKey(member.ID, "ci", apikey.HashSecret(secret))
	if err != nil {
		t.Fatal(err)
	}
	if k.SecretHash == secret || k.SecretHash == "" || k.Revoked() {
		t.Fatalf("stored key must hold only a hash: %+v", k)
	}
	got, err := db.GetAPIKeyByHash(apikey.HashSecret(secret))
	if err != nil || got.ID != k.ID || got.Username != "alice" || got.Label != "ci" {
		t.Fatalf("GetAPIKeyByHash = %+v, %v", got, err)
	}
	if _, err := db.GetAPIKeyByHash(apikey.HashSecret("wrong-secret")); err == nil {
		t.Fatal("wrong secret hash accepted")
	}
	if _, err := db.AddAPIKey(member.ID, "dup", apikey.HashSecret(secret)); err == nil {
		t.Fatal("duplicate secret accepted")
	}
}
