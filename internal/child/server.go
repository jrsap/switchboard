// Package child implements one organization's child process: it serves
// loopback HTTP, exposes a health check, and receives its unwrapped data
// key from the router in the spawn handshake before it starts serving.
package child

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/seal"
)

const (
	defaultAuthFailureLimit  = 10
	defaultAuthFailureWindow = time.Minute
)

type Server struct {
	orgID      string
	spawnToken string
	db         *orgdb.DB
	failures   *failureLimiter

	mu      sync.RWMutex
	dataKey []byte // unwrapped org data key, memory only, received via handshake
}

func New(orgID, spawnToken string, db *orgdb.DB) *Server {
	return &Server{
		orgID:      orgID,
		spawnToken: spawnToken,
		db:         db,
		failures:   newFailureLimiter(defaultAuthFailureLimit, defaultAuthFailureWindow),
	}
}

func (s *Server) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dataKey != nil
}

// DataKey returns the unwrapped org data key, or nil before the handshake.
func (s *Server) DataKey() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dataKey
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /internal/handshake", s.handshake)
	mux.HandleFunc("/", s.gate)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	status := "starting"
	if s.Ready() {
		status = "ok"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": status, "org": s.orgID})
}

type handshakeRequest struct {
	DataKey string `json:"data_key"` // base64-encoded 32-byte org data key
}

func (s *Server) handshake(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.spawnToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid spawn token", "unauthorized")
		return
	}
	var req handshakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", "invalid_request")
		return
	}
	key, err := base64.StdEncoding.DecodeString(req.DataKey)
	if err != nil || len(key) != seal.KeySize {
		writeError(w, http.StatusBadRequest, "data_key must be base64-encoded 32 bytes", "invalid_request")
		return
	}
	s.mu.Lock()
	s.dataKey = key
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "org": s.orgID})
}

// gate serves everything else: 503 until the handshake delivered the data
// key, then the org API: requests must carry a valid, non-revoked API key of
// THIS org (verified as a SHA-256 hash against the org DB with a
// constant-time comparison; a key with another org's prefix fails closed).
// Auth failures are rate-limited per key prefix and source address; only the
// key prefix is ever logged. Authenticated requests are dispatched to the
// org API endpoints.
func (s *Server) gate(w http.ResponseWriter, r *http.Request) {
	if !s.Ready() {
		writeError(w, http.StatusServiceUnavailable, "org child is starting", "server_error")
		return
	}

	auth := r.Header.Get("Authorization")
	key, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || key == auth {
		s.authFailed(w, r, "missing or malformed Authorization header")
		return
	}
	orgID, secret, ok := apikey.Parse(key)
	if !ok {
		s.authFailed(w, r, "invalid api key")
		return
	}
	source := r.RemoteAddr
	if !s.failures.allow(apikey.Prefix(orgID) + "|" + source) {
		writeError(w, http.StatusTooManyRequests, "too many auth failures; retry later", "rate_limited")
		return
	}
	if orgID != s.orgID {
		s.authFailed(w, r, "invalid api key")
		return
	}
	hash := apikey.HashSecret(secret)
	stored, err := s.db.GetAPIKeyByHash(hash)
	if err != nil {
		s.authFailed(w, r, "invalid api key")
		return
	}
	if subtle.ConstantTimeCompare([]byte(stored.SecretHash), []byte(hash)) != 1 {
		s.authFailed(w, r, "invalid api key")
		return
	}
	if stored.Revoked() {
		s.authFailed(w, r, "invalid api key")
		return
	}
	s.api(w, r)
}

// api dispatches an authenticated request to the org API endpoints.
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		s.models(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

// models serves GET /v1/models: the org's alias catalog in the OpenAI
// models response shape. No upstream calls; the alias name is the model id.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	aliases, err := s.db.ListAliases()
	if err != nil {
		log.Printf("child %s: list aliases: %v", s.orgID, err)
		writeError(w, http.StatusInternalServerError, "internal error", "server_error")
		return
	}
	data := make([]map[string]any, 0, len(aliases))
	for _, a := range aliases {
		data = append(data, map[string]any{
			"object":   "model",
			"id":       a.Name,
			"created":  a.CreatedAt.Unix(),
			"owned_by": s.orgID,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// authFailed records the failure for rate limiting, logs only the key
// prefix (never the secret) and writes a 401.
func (s *Server) authFailed(w http.ResponseWriter, r *http.Request, message string) {
	auth := r.Header.Get("Authorization")
	prefix := ""
	if key, ok := strings.CutPrefix(auth, "Bearer "); ok {
		if orgID, _, ok := apikey.Parse(key); ok {
			prefix = apikey.Prefix(orgID)
		}
	}
	s.failures.record(prefix + "|" + r.RemoteAddr)
	log.Printf("child %s: auth failed: key prefix %q from %s", s.orgID, prefix, r.RemoteAddr)
	writeError(w, http.StatusUnauthorized, message, "invalid_api_key")
}

func writeError(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": message, "type": typ},
	})
}
