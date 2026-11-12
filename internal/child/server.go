// Package child implements one organization's child process: it serves
// loopback HTTP, exposes a health check, and receives its unwrapped data
// key from the router in the spawn handshake before it starts serving.
package child

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/seal"
)

type Server struct {
	orgID      string
	spawnToken string
	db         *orgdb.DB

	mu      sync.RWMutex
	dataKey []byte // unwrapped org data key, memory only, received via handshake
}

func New(orgID, spawnToken string, db *orgdb.DB) *Server {
	return &Server{orgID: orgID, spawnToken: spawnToken, db: db}
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
// key, then 404 (the org API endpoints land here later).
func (s *Server) gate(w http.ResponseWriter, r *http.Request) {
	if !s.Ready() {
		writeError(w, http.StatusServiceUnavailable, "org child is starting", "server_error")
		return
	}
	writeError(w, http.StatusNotFound, "not found", "not_found")
}

func writeError(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": message, "type": typ},
	})
}
