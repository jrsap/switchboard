// Package orgs implements organization management for the admin CLI:
// creating an org (per-org DB + storage descriptor + wrapped data key +
// exactly one admin member + global user routing cache + router config
// entry) and listing orgs from the router config.
package orgs

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/globaldb"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/seal"
)

var ErrOrgExists = errors.New("orgs: org already exists")

// SQLOpen opens a database/sql handle for the given path. Supplied by the
// call site so this package stays driver-agnostic.
type SQLOpen func(path string) (*sql.DB, error)

type CreateOptions struct {
	ConfigPath        string       // router config file to update
	DataDir           string       // base directory for per-org storage
	ID                string       // org id, also the API key prefix
	Name              string       // human-readable name
	ChildAddr         string       // optional; a free loopback port is allocated if empty
	AdminUsername     string       // the exactly-one admin created with the org
	AdminPasswordHash string       // used only if the global user has to be created
	RootKey           []byte       // 32-byte root key, used to wrap the new org data key
	GlobalDB          *globaldb.DB // global users DB
	OpenSQL           SQLOpen      // opens the per-org SQLite DB
}

// Create creates a new org end to end and returns its router config entry.
func Create(opts CreateOptions) (*config.Org, error) {
	if !apikey.ValidOrgID(opts.ID) {
		return nil, fmt.Errorf("invalid org id %q: must match ^[a-z0-9][a-z0-9-]{0,62}$", opts.ID)
	}
	if opts.Name == "" {
		return nil, errors.New("org name is required")
	}
	if opts.AdminUsername == "" {
		return nil, errors.New("admin username is required")
	}
	if len(opts.RootKey) != seal.KeySize {
		return nil, fmt.Errorf("root key must be %d bytes", seal.KeySize)
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if cfg.Org(opts.ID) != nil {
		return nil, fmt.Errorf("%w: %q", ErrOrgExists, opts.ID)
	}

	childAddr := opts.ChildAddr
	if childAddr == "" {
		childAddr, err = AllocateLoopbackAddr()
		if err != nil {
			return nil, err
		}
	}

	dataKey, err := seal.NewKey()
	if err != nil {
		return nil, err
	}
	wrapped, err := seal.Wrap(opts.RootKey, dataKey)
	if err != nil {
		return nil, err
	}

	orgDir := filepath.Join(opts.DataDir, opts.ID)
	dbPath := filepath.Join(orgDir, "org.db")
	archiveRoot := filepath.Join(orgDir, "archive")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create org storage: %w", err)
	}

	sqlDB, err := opts.OpenSQL(dbPath)
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()
	odb, err := orgdb.Open(sqlDB)
	if err != nil {
		return nil, err
	}
	if err := odb.SetMeta(orgdb.MetaWrappedDataKey, wrapped); err != nil {
		return nil, fmt.Errorf("store wrapped data key: %w", err)
	}
	if _, err := odb.AddMember(opts.AdminUsername, "admin"); err != nil {
		return nil, fmt.Errorf("add admin member: %w", err)
	}

	if err := addToGlobalUser(opts.GlobalDB, opts.AdminUsername, opts.AdminPasswordHash, opts.ID); err != nil {
		return nil, err
	}

	org := config.Org{
		ID:             opts.ID,
		Name:           opts.Name,
		ChildAddr:      childAddr,
		WrappedDataKey: wrapped,
		Storage:        config.Storage{DB: dbPath, ArchiveRoot: archiveRoot},
	}
	cfg.Orgs = append(cfg.Orgs, org)
	if err := cfg.Save(opts.ConfigPath); err != nil {
		return nil, fmt.Errorf("update router config: %w", err)
	}
	return &org, nil
}

func addToGlobalUser(db *globaldb.DB, username, passwordHash, orgID string) error {
	u, err := db.GetUserByUsername(username)
	if errors.Is(err, globaldb.ErrNotFound) {
		_, err := db.CreateUser(username, passwordHash, []string{orgID})
		return err
	}
	if err != nil {
		return err
	}
	for _, o := range u.Orgs {
		if o == orgID {
			return nil
		}
	}
	return db.SetOrgs(u.ID, append(u.Orgs, orgID))
}

// List returns the orgs from the router config file.
func List(configPath string) ([]config.Org, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return cfg.Orgs, nil
}

// RootKeyFromConfig loads the root key from the router config file.
func RootKeyFromConfig(configPath string) ([]byte, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(cfg.RootKey)
	if err != nil || len(key) != seal.KeySize {
		return nil, fmt.Errorf("root_key must be a base64-encoded %d-byte key", seal.KeySize)
	}
	return key, nil
}

// AllocateLoopbackAddr reserves a free loopback port and returns it as
// host:port. There is an inherent (small) race between closing the probe
// listener and the child binding the port.
func AllocateLoopbackAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}
