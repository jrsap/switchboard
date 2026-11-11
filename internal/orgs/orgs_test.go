package orgs

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/globaldb"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/seal"
)

func testSQLOpen(t *testing.T) SQLOpen {
	return func(path string) (*sql.DB, error) {
		return sql.Open("sqlite", database.SQLiteDSN(path))
	}
}

func writeConfig(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	rootKey, _ := seal.NewKey()
	cfg := config.Config{
		Listen:  "127.0.0.1:18080",
		RootKey: base64.StdEncoding.EncodeToString(rootKey),
		Orgs:    nil,
	}
	path := filepath.Join(dir, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return path, rootKey
}

func openGlobal(t *testing.T, dir string) *globaldb.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(filepath.Join(dir, "global.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := globaldb.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCreateOrg(t *testing.T) {
	dir := t.TempDir()
	cfgPath, rootKey := writeConfig(t, dir)
	gdb := openGlobal(t, dir)

	org, err := Create(CreateOptions{
		ConfigPath:        cfgPath,
		DataDir:           filepath.Join(dir, "data"),
		ID:                "acme",
		Name:              "Acme Corp",
		AdminUsername:     "alice",
		AdminPasswordHash: "hash1",
		RootKey:           rootKey,
		GlobalDB:          gdb,
		OpenSQL:           testSQLOpen(t),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if org.ChildAddr == "" || org.WrappedDataKey == "" {
		t.Fatalf("incomplete org entry: %+v", org)
	}

	// The wrapped key must unwrap to a 32-byte data key with the root key.
	dataKey, err := seal.Unwrap(rootKey, org.WrappedDataKey)
	if err != nil || len(dataKey) != seal.KeySize {
		t.Fatalf("wrapped data key does not unwrap: %v", err)
	}

	// Org DB has the wrapped key and exactly the admin member.
	sqlDB, err := testSQLOpen(t)(org.Storage.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	odb, err := orgdb.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := odb.GetMeta(orgdb.MetaWrappedDataKey); err != nil || v != org.WrappedDataKey {
		t.Fatalf("org db wrapped key = %q, %v", v, err)
	}
	members, err := odb.ListMembers()
	if err != nil || len(members) != 1 || members[0].Username != "alice" || members[0].Role != "admin" {
		t.Fatalf("members = %+v, %v", members, err)
	}

	// Global user created with the org in its routing cache.
	u, err := gdb.GetUserByUsername("alice")
	if err != nil || u.PasswordHash != "hash1" || len(u.Orgs) != 1 || u.Orgs[0] != "acme" {
		t.Fatalf("global user = %+v, %v", u, err)
	}

	// Router config was updated and stays valid.
	orgs, err := List(cfgPath)
	if err != nil || len(orgs) != 1 || orgs[0].ID != "acme" {
		t.Fatalf("List = %+v, %v", orgs, err)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("updated config invalid: %v", err)
	}

	// Duplicate org id rejected.
	_, err = Create(CreateOptions{
		ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), ID: "acme", Name: "Dup",
		AdminUsername: "bob", AdminPasswordHash: "h", RootKey: rootKey, GlobalDB: gdb, OpenSQL: testSQLOpen(t),
	})
	if !errors.Is(err, ErrOrgExists) {
		t.Fatalf("duplicate: want ErrOrgExists, got %v", err)
	}

	// Second org reuses an existing global user and just extends the cache.
	org2, err := Create(CreateOptions{
		ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), ID: "beta", Name: "Beta",
		AdminUsername: "alice", AdminPasswordHash: "ignored", RootKey: rootKey, GlobalDB: gdb, OpenSQL: testSQLOpen(t),
	})
	if err != nil {
		t.Fatalf("Create beta: %v", err)
	}
	u, _ = gdb.GetUserByUsername("alice")
	if len(u.Orgs) != 2 {
		t.Fatalf("global user orgs = %v, want 2 entries", u.Orgs)
	}
	if org2.Storage.DB == org.Storage.DB {
		t.Fatal("orgs must have distinct storage")
	}
}

func TestCreateValidates(t *testing.T) {
	dir := t.TempDir()
	cfgPath, rootKey := writeConfig(t, dir)
	gdb := openGlobal(t, dir)
	base := CreateOptions{
		ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), Name: "X",
		AdminUsername: "a", AdminPasswordHash: "h", RootKey: rootKey, GlobalDB: gdb, OpenSQL: testSQLOpen(t),
	}
	for _, id := range []string{"", "Bad_ID", "-x", "a b"} {
		opts := base
		opts.ID = id
		if _, err := Create(opts); err == nil {
			t.Errorf("id %q accepted, want reject", id)
		}
	}
	opts := base
	opts.ID = "ok"
	opts.Name = ""
	if _, err := Create(opts); err == nil {
		t.Error("empty name accepted")
	}
}

func TestConfigFilePerms(t *testing.T) {
	dir := t.TempDir()
	cfgPath, _ := writeConfig(t, dir)
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config perms = %o, want 600", fi.Mode().Perm())
	}
}
