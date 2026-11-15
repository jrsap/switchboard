// Package orgs implements organization management: an org is NOT an entry in
// the router config file — an org exists iff its org DB exists at
// orgpath.DBPath(dataDir, orgID), and everything org-specific (wrapped data
// key, org name) is loaded from that database. Creating an org (admin CLI)
// means creating its org DB under the base data dir, sealing a fresh data
// key with the root key into org_meta, and adding exactly one admin member
// plus the global user routing cache. The router discovers orgs the same way
// (orgpath.Discover) and spawns one child per org; the child never reads the
// config file, it gets the base data dir and org id from the router as
// startup flags and derives its storage paths itself (see internal/orgpath).
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
	"github.com/jrsap/switchboard/internal/orgpath"
	"github.com/jrsap/switchboard/internal/seal"
)

var (
	ErrOrgExists   = errors.New("orgs: org already exists")
	ErrOrgNotFound = errors.New("orgs: org does not exist")
)

// SQLOpen opens a database/sql handle for the given path. Supplied by the
// call site so this package stays driver-agnostic.
type SQLOpen func(path string) (*sql.DB, error)

// Org is one organization: id and name plus the derived storage paths.
type Org struct {
	ID          string
	Name        string
	DB          string // orgpath.DBPath(dataDir, ID)
	ArchiveRoot string // orgpath.ArchiveRoot(dataDir, ID)
}

// OpenOrgDB opens the org DB of an existing org (ErrOrgNotFound when the org
// DB does not exist) and applies pending migrations. Used by admin tooling;
// the router reads org DBs without migrating (orgdb.ReadMeta).
func OpenOrgDB(dataDir, orgID string, openSQL SQLOpen) (*orgdb.DB, error) {
	if !orgpath.Exists(dataDir, orgID) {
		return nil, fmt.Errorf("%w: %q (no org DB at %s)", ErrOrgNotFound, orgID, orgpath.DBPath(dataDir, orgID))
	}
	sqlDB, err := openSQL(orgpath.DBPath(dataDir, orgID))
	if err != nil {
		return nil, err
	}
	odb, err := orgdb.Open(sqlDB)
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return odb, nil
}

type CreateOptions struct {
	DataDir           string       // base directory for per-org storage
	ID                string       // org id, also the API key prefix
	Name              string       // human-readable name, stored in the org DB meta
	AdminUsername     string       // the exactly-one admin created with the org
	AdminPasswordHash string       // used only if the global user has to be created
	RootKey           []byte       // 32-byte root key, used to wrap the new org data key
	GlobalDB          *globaldb.DB // global users DB
	OpenSQL           SQLOpen      // opens the per-org SQLite DB
}

// Create creates a new org end to end: <dataDir>/<id>/ with the org DB
// (wrapped data key + org name in org_meta, one admin member) and the
// archive root, plus the global user routing cache. It never touches the
// router config file. Returns the created org with its derived paths.
func Create(opts CreateOptions) (*Org, error) {
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
	if orgpath.Exists(opts.DataDir, opts.ID) {
		return nil, fmt.Errorf("%w: %q", ErrOrgExists, opts.ID)
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
	dbPath := orgpath.DBPath(opts.DataDir, opts.ID)
	archiveRoot := orgpath.ArchiveRoot(opts.DataDir, opts.ID)
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create org storage: %w", err)
	}
	// From here on the org dir exists; on failure remove it again so a
	// half-created org is not "discovered" as an existing org.
	fail := func(err error) (*Org, error) {
		os.RemoveAll(orgDir)
		return nil, err
	}

	sqlDB, err := opts.OpenSQL(dbPath)
	if err != nil {
		return fail(err)
	}
	defer sqlDB.Close()
	odb, err := orgdb.Open(sqlDB)
	if err != nil {
		return fail(err)
	}
	if err := odb.SetMeta(orgdb.MetaWrappedDataKey, wrapped); err != nil {
		return fail(fmt.Errorf("store wrapped data key: %w", err))
	}
	if err := odb.SetMeta(orgdb.MetaOrgName, opts.Name); err != nil {
		return fail(fmt.Errorf("store org name: %w", err))
	}
	if _, err := odb.AddMember(opts.AdminUsername, "admin"); err != nil {
		return fail(fmt.Errorf("add admin member: %w", err))
	}

	if err := addToGlobalUser(opts.GlobalDB, opts.AdminUsername, opts.AdminPasswordHash, opts.ID); err != nil {
		return fail(err)
	}

	return &Org{ID: opts.ID, Name: opts.Name, DB: dbPath, ArchiveRoot: archiveRoot}, nil
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

// List returns all orgs discovered under dataDir, with names loaded from
// their org DBs.
func List(dataDir string, openSQL SQLOpen) ([]*Org, error) {
	ids, err := orgpath.Discover(dataDir)
	if err != nil {
		return nil, err
	}
	var orgs []*Org
	for _, id := range ids {
		org := &Org{ID: id, DB: orgpath.DBPath(dataDir, id), ArchiveRoot: orgpath.ArchiveRoot(dataDir, id)}
		odb, err := OpenOrgDB(dataDir, id, openSQL)
		if err != nil {
			return nil, err
		}
		if name, err := odb.GetMeta(orgdb.MetaOrgName); err == nil {
			org.Name = name
		}
		odb.Close()
		orgs = append(orgs, org)
	}
	return orgs, nil
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
