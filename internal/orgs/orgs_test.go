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
	dataDir := filepath.Join(dir, "data")
	rootKey, _ := seal.NewKey()
	gdb := openGlobal(t, dir)

	org, err := Create(CreateOptions{
		DataDir:           dataDir,
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

	// Storage paths are derived from the data dir and the org id; the org
	// exists because its DB file exists.
	if org.DB != OrgDBPath(dataDir, "acme") || org.ArchiveRoot != OrgArchiveRoot(dataDir, "acme") {
		t.Fatalf("unexpected paths: %+v", org)
	}
	if !OrgExists(dataDir, "acme") {
		t.Fatal("org DB not created at the derived path")
	}
	ids, err := Discover(dataDir)
	if err != nil || len(ids) != 1 || ids[0] != "acme" {
		t.Fatalf("Discover = %v, %v", ids, err)
	}

	// Org DB holds the wrapped key and the org name; the wrapped key must
	// unwrap to a 32-byte data key with the root key.
	odb, err := OpenOrgDB(dataDir, "acme", testSQLOpen(t))
	if err != nil {
		t.Fatalf("OpenOrgDB: %v", err)
	}
	defer odb.Close()
	wrapped, err := odb.GetMeta(orgdb.MetaWrappedDataKey)
	if err != nil {
		t.Fatalf("GetMeta(wrapped_data_key): %v", err)
	}
	dataKey, err := seal.Unwrap(rootKey, wrapped)
	if err != nil || len(dataKey) != seal.KeySize {
		t.Fatalf("wrapped data key does not unwrap: %v", err)
	}
	if name, err := odb.GetMeta(orgdb.MetaOrgName); err != nil || name != "Acme Corp" {
		t.Fatalf("org name = %q, %v", name, err)
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

	// Duplicate org id rejected (the org DB already exists).
	_, err = Create(CreateOptions{
		DataDir: dataDir, ID: "acme", Name: "Dup",
		AdminUsername: "bob", AdminPasswordHash: "h", RootKey: rootKey, GlobalDB: gdb, OpenSQL: testSQLOpen(t),
	})
	if !errors.Is(err, ErrOrgExists) {
		t.Fatalf("duplicate: want ErrOrgExists, got %v", err)
	}

	// Second org reuses an existing global user and just extends the cache.
	org2, err := Create(CreateOptions{
		DataDir: dataDir, ID: "beta", Name: "Beta",
		AdminUsername: "alice", AdminPasswordHash: "ignored", RootKey: rootKey, GlobalDB: gdb, OpenSQL: testSQLOpen(t),
	})
	if err != nil {
		t.Fatalf("Create beta: %v", err)
	}
	u, _ = gdb.GetUserByUsername("alice")
	if len(u.Orgs) != 2 {
		t.Fatalf("global user orgs = %v, want 2 entries", u.Orgs)
	}
	if org2.DB == org.DB {
		t.Fatal("orgs must have distinct storage")
	}

	// List discovers both orgs from the data dir, with names from their DBs.
	list, err := List(dataDir, testSQLOpen(t))
	if err != nil || len(list) != 2 || list[0].ID != "acme" || list[0].Name != "Acme Corp" || list[1].ID != "beta" {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestCreateValidates(t *testing.T) {
	dir := t.TempDir()
	rootKey, _ := seal.NewKey()
	gdb := openGlobal(t, dir)
	base := CreateOptions{
		DataDir: filepath.Join(dir, "data"), Name: "X",
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

func TestDiscover(t *testing.T) {
	dir := t.TempDir()

	// Missing data dir: no orgs, no error.
	ids, err := Discover(filepath.Join(dir, "nope"))
	if err != nil || len(ids) != 0 {
		t.Fatalf("Discover(missing) = %v, %v", ids, err)
	}

	// A dir named by a valid org id containing org.db is an org.
	makeOrgDB := func(orgID string) {
		dbPath := OrgDBPath(dir, orgID)
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			t.Fatal(err)
		}
		sqlDB, err := testSQLOpen(t)(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		db, err := orgdb.Open(sqlDB)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	makeOrgDB("acme")
	makeOrgDB("beta")
	// Not orgs: invalid id dir with org.db, valid id dir without org.db, plain file.
	makeOrgDB("Bad_ID")
	if err := os.MkdirAll(filepath.Join(dir, "gamma"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "delta.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ids, err = Discover(dir)
	if err != nil || len(ids) != 2 || ids[0] != "acme" || ids[1] != "beta" {
		t.Fatalf("Discover = %v, %v", ids, err)
	}
	if OrgExists(dir, "gamma") || OrgExists(dir, "delta") || !OrgExists(dir, "acme") {
		t.Fatal("OrgExists broken")
	}
}

func TestOpenOrgDBNotFound(t *testing.T) {
	_, err := OpenOrgDB(t.TempDir(), "ghost", testSQLOpen(t))
	if !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("want ErrOrgNotFound, got %v", err)
	}
}

func TestRootKeyFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath, rootKey := writeConfig(t, dir)
	got, err := RootKeyFromConfig(cfgPath)
	if err != nil || string(got) != string(rootKey) {
		t.Fatalf("RootKeyFromConfig: %v", err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"listen":"127.0.0.1:1","root_key":"AAAA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RootKeyFromConfig(bad); err == nil {
		t.Fatal("short root key accepted")
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
