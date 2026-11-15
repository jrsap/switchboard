// Package config loads and validates the global router configuration file.
// The file holds only global settings: where the router listens, the root
// key, and the base directory for per-org storage.
//
// Organizations are NOT configured in this file. An org exists iff its org
// database exists at <data_dir>/<orgid>/org.db (see internal/orgs); the
// router discovers orgs that way and loads the per-org config (wrapped data
// key, org name) from the org DB itself. The org child process never reads
// this file — the router passes it everything via command-line flags and
// the loopback spawn handshake.
package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// DefaultDataDir is the base directory for per-org storage when the config
// sets no data_dir.
const DefaultDataDir = "data"

type Config struct {
	Listen  string `json:"listen"`
	RootKey string `json:"root_key"` // base64-encoded 32-byte root key, held only by the router process
	// ChildBinary is the org child process executable the router spawns.
	// Defaults to "switchboard-org" next to the router executable.
	ChildBinary string `json:"child_binary,omitempty"`
	// GlobalDB is NOT used by the router (no business logic); it is the
	// default for the admin CLI's -db. CLI precedence: -db flag >
	// SWITCHBOARD_GLOBAL_DB env > global_db > ./switchboard-global.db.
	GlobalDB string `json:"global_db,omitempty"`
	// DataDir is the base directory for per-org storage; the router derives
	// each org DB path as <data_dir>/<orgid>/org.db. The router itself only
	// reads org DBs here (discovery + wrapped data key); all org business
	// data is written by the org child. CLI precedence for -data-dir:
	// flag > SWITCHBOARD_DATA_DIR env > data_dir > ./data.
	DataDir string `json:"data_dir,omitempty"`
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
	return nil
}

// DataDirOrDefault returns DataDir, or DefaultDataDir when unset.
func (c *Config) DataDirOrDefault() string {
	if c.DataDir == "" {
		return DefaultDataDir
	}
	return c.DataDir
}

// Save writes the config back to path as indented JSON with 0600 perms.
func (c *Config) Save(path string) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}
