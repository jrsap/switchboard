// Package router implements the main router process: it listens on the
// public port and demultiplexes requests to one child process per org over
// loopback HTTP.
//
// Orgs are discovered, not configured: an org exists iff its org DB exists
// at <data_dir>/<orgid>/org.db (see internal/orgs). At startup the router
// scans the data dir, reads each org's wrapped data key from its org DB
// meta (the only thing it reads from org DBs — no business logic), allocates
// a loopback address, spawns one child per org with everything it needs as
// command-line flags (-org, -addr, -db, -archive-root, -spawn-token), waits
// for its health check, and hands it the unwrapped org data key over the
// loopback handshake. The child process never reads the router config file.
package router

import (
	"context"
	"database/sql"
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/orgs"
	"github.com/jrsap/switchboard/internal/seal"
)

const (
	healthPollInterval = 100 * time.Millisecond
	healthWaitTimeout  = 10 * time.Second
	httpClientTimeout  = 2 * time.Second
)

// orgChild is one discovered org plus the runtime child state.
type orgChild struct {
	ID             string
	DBPath         string // derived: <data_dir>/<id>/org.db
	ArchiveRoot    string // derived: <data_dir>/<id>/archive
	ChildAddr      string // allocated at router startup
	wrappedDataKey string // loaded from the org DB meta
}

type Server struct {
	cfg     *config.Config
	rootKey []byte // held only here; unwrapped per-org at spawn, never stored
	binary  string
	dataDir string

	orgs    map[string]*orgChild              // discovered orgs, immutable after New
	proxies map[string]*httputil.ReverseProxy // immutable after New

	mu       sync.RWMutex
	ready    map[string]bool
	children map[string]*exec.Cmd
	client   *http.Client
}

func New(cfg *config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
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
	dataDir := cfg.DataDirOrDefault()
	ids, err := orgs.Discover(dataDir)
	if err != nil {
		return nil, fmt.Errorf("router: discover orgs in %s: %w", dataDir, err)
	}
	s := &Server{
		cfg:      cfg,
		rootKey:  rootKey,
		binary:   binary,
		dataDir:  dataDir,
		orgs:     make(map[string]*orgChild, len(ids)),
		proxies:  make(map[string]*httputil.ReverseProxy, len(ids)),
		ready:    make(map[string]bool, len(ids)),
		children: map[string]*exec.Cmd{},
		client:   &http.Client{Timeout: httpClientTimeout},
	}
	for _, id := range ids {
		dbPath := orgs.OrgDBPath(dataDir, id)
		wrapped, err := readWrappedDataKey(dbPath)
		if err != nil {
			return nil, fmt.Errorf("router: org %s: read org DB %s: %w", id, dbPath, err)
		}
		addr, err := orgs.AllocateLoopbackAddr()
		if err != nil {
			return nil, fmt.Errorf("router: org %s: allocate child addr: %w", id, err)
		}
		oc := &orgChild{
			ID:             id,
			DBPath:         dbPath,
			ArchiveRoot:    orgs.OrgArchiveRoot(dataDir, id),
			ChildAddr:      addr,
			wrappedDataKey: wrapped,
		}
		s.orgs[id] = oc
		target := &url.URL{Scheme: "http", Host: addr}
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("router: org %s: upstream error: %v", id, err)
			writeError(w, http.StatusBadGateway, "upstream unavailable", "upstream_error")
		}
		s.proxies[id] = rp
	}
	return s, nil
}

// readWrappedDataKey loads the wrapped data key from an existing org DB
// without applying migrations (the org DB is written only by its child).
func readWrappedDataKey(dbPath string) (string, error) {
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(dbPath))
	if err != nil {
		return "", err
	}
	defer sqlDB.Close()
	return orgdb.ReadMeta(sqlDB, orgdb.MetaWrappedDataKey)
}

// OrgIDs returns the discovered org ids, sorted.
func (s *Server) OrgIDs() []string {
	ids := make([]string, 0, len(s.orgs))
	for id := range s.orgs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// StartChildren spawns one child process per discovered org and performs the
// loopback spawn handshake (unwrapped data key). An org is proxied only once
// ready. New orgs (new org DBs) are picked up on the next router start.
func (s *Server) StartChildren(ctx context.Context) error {
	for _, id := range s.OrgIDs() {
		if err := s.startChild(ctx, s.orgs[id]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) startChild(ctx context.Context, oc *orgChild) error {
	dataKey, err := seal.Unwrap(s.rootKey, oc.wrappedDataKey)
	if err != nil {
		return fmt.Errorf("router: org %s: unwrap data key: %w", oc.ID, err)
	}

	token, err := seal.NewKey()
	if err != nil {
		return err
	}
	// The child gets everything from the main process here: org id, loopback
	// addr, org DB path, archive root and spawn token as flags; the data key
	// comes in the handshake below. It never reads the config file.
	cmd := exec.CommandContext(ctx, s.binary,
		"-org", oc.ID,
		"-addr", oc.ChildAddr,
		"-db", oc.DBPath,
		"-archive-root", oc.ArchiveRoot,
		"-spawn-token", base64.StdEncoding.EncodeToString(token),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("router: org %s: spawn child: %w", oc.ID, err)
	}
	s.mu.Lock()
	s.children[oc.ID] = cmd
	s.mu.Unlock()
	log.Printf("router: org %s: spawned child (pid %d) at %s (db %s)", oc.ID, cmd.Process.Pid, oc.ChildAddr, oc.DBPath)

	if err := s.waitHealthy(ctx, oc.ChildAddr); err != nil {
		return fmt.Errorf("router: org %s: %w", oc.ID, err)
	}
	if err := s.handshake(ctx, oc.ChildAddr, token, dataKey); err != nil {
		return fmt.Errorf("router: org %s: handshake: %w", oc.ID, err)
	}
	s.setReady(oc.ID, true)
	log.Printf("router: org %s: child ready", oc.ID)
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

// probe checks whether something is listening on addr and reports its
// readiness.
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
	if _, known := s.orgs[orgID]; !known {
		// Fail closed: an org without an org DB does not exist, and a key
		// with an unknown org prefix is rejected here; the child would also
		// reject it against its own org DB.
		writeError(w, http.StatusUnauthorized, "invalid api key", "invalid_api_key")
		return
	}
	if !s.isReady(orgID) {
		writeError(w, http.StatusServiceUnavailable, "org is starting", "server_error")
		return
	}
	s.proxies[orgID].ServeHTTP(w, r)
}

func writeError(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": message, "type": typ},
	})
}
