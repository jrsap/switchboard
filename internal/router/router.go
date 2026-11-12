// Package router implements the main router process: it listens on the
// public port and demultiplexes requests to one child process per org over
// loopback HTTP. It is deliberately tiny and stateless: no business logic,
// no database access — only the root key and per-child configuration.
//
// At startup the router spawns one child process per org (network link +
// spawn + storage descriptor), waits for its health check, and hands it the
// unwrapped org data key over the loopback handshake.
package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/seal"
)

const (
	healthPollInterval = 100 * time.Millisecond
	healthWaitTimeout  = 10 * time.Second
	httpClientTimeout  = 2 * time.Second
)

type Server struct {
	cfg     *config.Config
	rootKey []byte // held only here; unwrapped per-org at spawn, never stored
	binary  string

	mu       sync.RWMutex
	proxies  map[string]*httputil.ReverseProxy
	ready    map[string]bool
	children map[string]*exec.Cmd
	client   *http.Client
}

func New(cfg *config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Orgs) == 0 {
		return nil, errors.New("router: at least one org is required")
	}
	rootKey, err := base64.StdEncoding.DecodeString(cfg.RootKey)
	if err != nil || len(rootKey) != seal.KeySize {
		return nil, fmt.Errorf("root_key must be a base64-encoded %d-byte key", seal.KeySize)
	}
	binary := cfg.ChildBinary
	if binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		binary = filepath.Join(filepath.Dir(exe), "switchboard-org")
	}
	s := &Server{
		cfg:      cfg,
		rootKey:  rootKey,
		binary:   binary,
		proxies:  make(map[string]*httputil.ReverseProxy, len(cfg.Orgs)),
		ready:    make(map[string]bool, len(cfg.Orgs)),
		children: map[string]*exec.Cmd{},
		client:   &http.Client{Timeout: httpClientTimeout},
	}
	for i := range cfg.Orgs {
		org := &cfg.Orgs[i]
		target := &url.URL{Scheme: "http", Host: org.ChildAddr}
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("router: org %s: upstream error: %v", org.ID, err)
			writeError(w, http.StatusBadGateway, "upstream unavailable", "upstream_error")
		}
		s.proxies[org.ID] = rp
	}
	return s, nil
}

// StartChildren spawns one child process per org and performs the loopback
// spawn handshake (unwrapped data key). An org is proxied only once ready.
func (s *Server) StartChildren(ctx context.Context) error {
	for i := range s.cfg.Orgs {
		org := &s.cfg.Orgs[i]
		if err := s.startChild(ctx, org); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) startChild(ctx context.Context, org *config.Org) error {
	dataKey, err := seal.Unwrap(s.rootKey, org.WrappedDataKey)
	if err != nil {
		return fmt.Errorf("router: org %s: unwrap data key: %w", org.ID, err)
	}

	healthy, ready := s.probe(ctx, org.ChildAddr)
	if healthy && ready {
		log.Printf("router: org %s: child already running and ready at %s, not spawning", org.ID, org.ChildAddr)
		s.setReady(org.ID, true)
		return nil
	}

	token, err := seal.NewKey()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, s.binary,
		"-org", org.ID,
		"-addr", org.ChildAddr,
		"-db", org.Storage.DB,
		"-archive-root", org.Storage.ArchiveRoot,
		"-spawn-token", base64.StdEncoding.EncodeToString(token),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("router: org %s: spawn child: %w", org.ID, err)
	}
	s.mu.Lock()
	s.children[org.ID] = cmd
	s.mu.Unlock()
	log.Printf("router: org %s: spawned child (pid %d) at %s", org.ID, cmd.Process.Pid, org.ChildAddr)

	if err := s.waitHealthy(ctx, org.ChildAddr); err != nil {
		return fmt.Errorf("router: org %s: %w", org.ID, err)
	}
	if err := s.handshake(ctx, org.ChildAddr, token, dataKey); err != nil {
		return fmt.Errorf("router: org %s: handshake: %w", org.ID, err)
	}
	s.setReady(org.ID, true)
	log.Printf("router: org %s: child ready", org.ID)
	return nil
}

// StopChildren kills all spawned child processes.
func (s *Server) StopChildren() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for orgID, cmd := range s.children {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
		log.Printf("router: org %s: child stopped", orgID)
	}
}

func (s *Server) setReady(orgID string, ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready[orgID] = ready
}

func (s *Server) isReady(orgID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready[orgID]
}

type healthResponse struct {
	Status string `json:"status"`
	Org    string `json:"org"`
}

// probe checks whether a child is already listening and ready.
func (s *Server) probe(ctx context.Context, addr string) (healthy, ready bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return false, false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true, false
	}
	var h healthResponse
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return true, false
	}
	return true, h.Status == "ok"
}

func (s *Server) waitHealthy(ctx context.Context, addr string) error {
	deadline := time.Now().Add(healthWaitTimeout)
	for {
		if healthy, _ := s.probe(ctx, addr); healthy {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("child did not become healthy in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPollInterval):
		}
	}
}

// handshake delivers the unwrapped org data key to the child over loopback.
func (s *Server) handshake(ctx context.Context, addr string, token, dataKey []byte) error {
	body, err := json.Marshal(map[string]string{
		"data_key": base64.StdEncoding.EncodeToString(dataKey),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/internal/handshake", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("child returned %s", resp.Status)
	}
	return nil
}

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
	if !s.isReady(orgID) {
		writeError(w, http.StatusServiceUnavailable, "org is starting", "server_error")
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
