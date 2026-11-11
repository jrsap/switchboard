// Package router implements the main router process: it listens on the
// public port and demultiplexes requests to one child process per org over
// loopback HTTP. It is deliberately tiny and stateless: no business logic,
// no database access — only the root key and per-child configuration.
package router

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/config"
)

type Server struct {
	rootKey []byte // held only here; later handed to children in the spawn handshake
	proxies map[string]*httputil.ReverseProxy
}

func New(cfg *config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rootKey, err := base64.StdEncoding.DecodeString(cfg.RootKey)
	if err != nil || len(rootKey) != 32 {
		return nil, errInvalidRootKey
	}
	s := &Server{
		rootKey: rootKey,
		proxies: make(map[string]*httputil.ReverseProxy, len(cfg.Orgs)),
	}
	for i := range cfg.Orgs {
		org := &cfg.Orgs[i]
		target := &url.URL{Scheme: "http", Host: org.ChildAddr}
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("org %s: upstream error: %v", org.ID, err)
			writeError(w, http.StatusBadGateway, "upstream unavailable", "upstream_error")
		}
		s.proxies[org.ID] = rp
	}
	return s, nil
}

var errInvalidRootKey = &configError{"root_key must be a base64-encoded 32-byte key"}

type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

// Handler returns the root HTTP handler: /healthz plus the demultiplexer.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", s.route)
	return mux
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	key, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || key == auth {
		writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header", "invalid_api_key")
		return
	}
	orgID, _, ok := apikey.Parse(key)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid api key", "invalid_api_key")
		return
	}
	rp, ok := s.proxies[orgID]
	if !ok {
		// Fail closed: a key with an unknown org prefix is rejected here;
		// the child would also reject it against its own org DB.
		writeError(w, http.StatusUnauthorized, "invalid api key", "invalid_api_key")
		return
	}
	rp.ServeHTTP(w, r)
}

func writeError(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": message, "type": typ},
	})
}
