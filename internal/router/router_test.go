package router

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jrsap/switchboard/internal/config"
)

func testConfig(childAddr string) *config.Config {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return &config.Config{
		Listen:  "127.0.0.1:0",
		RootKey: rootKey,
		Orgs: []config.Org{{
			ID:        "acme",
			Name:      "Acme",
			ChildAddr: childAddr,
			Storage:   config.Storage{DB: "/tmp/acme.db", ArchiveRoot: "/tmp/acme/archive"},
		}},
	}
}

func TestRoutesToOrgChild(t *testing.T) {
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Child", "acme")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(r.Method + " " + r.URL.Path + " " + string(body)))
	}))
	defer child.Close()

	// child_addr is host:port; strip the scheme from the test server URL.
	addr := strings.TrimPrefix(child.URL, "http://")
	srv, err := New(testConfig(addr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("child must not be reached")
	}))
	defer child.Close()
	addr := strings.TrimPrefix(child.URL, "http://")
	srv, err := New(testConfig(addr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := map[string]string{
		"no header":           "",
		"wrong scheme":        "Basic sb_acme_x",
		"no prefix":           "Bearer acme_x",
		"unknown org":         "Bearer sb_other_x",
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
	srv, err := New(testConfig("127.0.0.1:1"))
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
	srv, err := New(testConfig("127.0.0.1:1")) // nothing listening
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sb_acme_x")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}
