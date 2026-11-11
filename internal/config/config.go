// Package config loads and validates the router configuration. The router
// is stateless: it holds only the root key and the per-child (per-org)
// configuration. It never touches a database.
package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/jrsap/switchboard/internal/apikey"
)

// Storage is the per-org storage descriptor: where the org child keeps its
// database and its encrypted archives. Never hardcoded elsewhere.
type Storage struct {
	DB          string `json:"db"`
	ArchiveRoot string `json:"archive_root"`
}

// Org is the router-side configuration for one child process.
type Org struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ChildAddr string  `json:"child_addr"` // loopback HTTP address of the child process
	Storage   Storage `json:"storage"`
}

type Config struct {
	Listen  string `json:"listen"`
	RootKey string `json:"root_key"` // base64-encoded 32-byte root key, held only by the router
	Orgs    []Org  `json:"orgs"`
}

var listenRe = regexp.MustCompile(`^[^:]+:\d{1,5}$`)

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if !listenRe.MatchString(c.Listen) {
		return fmt.Errorf("listen must be host:port, got %q", c.Listen)
	}
	rootKey, err := base64.StdEncoding.DecodeString(c.RootKey)
	if err != nil || len(rootKey) != 32 {
		return fmt.Errorf("root_key must be a base64-encoded 32-byte key")
	}
	if len(c.Orgs) == 0 {
		return fmt.Errorf("at least one org is required")
	}
	seen := make(map[string]bool, len(c.Orgs))
	for i := range c.Orgs {
		org := &c.Orgs[i]
		if !apikey.ValidOrgID(org.ID) {
			return fmt.Errorf("org %d: invalid id %q", i, org.ID)
		}
		if seen[org.ID] {
			return fmt.Errorf("duplicate org id %q", org.ID)
		}
		seen[org.ID] = true
		if org.Name == "" {
			return fmt.Errorf("org %q: name is required", org.ID)
		}
		if !listenRe.MatchString(org.ChildAddr) {
			return fmt.Errorf("org %q: child_addr must be host:port, got %q", org.ID, org.ChildAddr)
		}
		if org.Storage.DB == "" {
			return fmt.Errorf("org %q: storage.db is required", org.ID)
		}
		if org.Storage.ArchiveRoot == "" {
			return fmt.Errorf("org %q: storage.archive_root is required", org.ID)
		}
	}
	return nil
}

// Org returns the org with the given id, or nil.
func (c *Config) Org(id string) *Org {
	for i := range c.Orgs {
		if c.Orgs[i].ID == id {
			return &c.Orgs[i]
		}
	}
	return nil
}
