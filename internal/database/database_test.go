package database

import (
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", SQLiteDSN(":memory:"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

func tableExists(t *testing.T, sqlDB *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	return n == 1
}

func TestMigrateAppliesInOrderAndIsIdempotent(t *testing.T) {
	sqlDB := openMem(t)
	fsys := fstest.MapFS{
		"migrations/0002_second.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE t2 (id INTEGER PRIMARY KEY);`)},
		"migrations/0001_first.sql":  &fstest.MapFile{Data: []byte(`CREATE TABLE t1 (id INTEGER PRIMARY KEY);`)},
		"migrations/notes.txt":       &fstest.MapFile{Data: []byte(`not a migration`)},
	}
	if err := Migrate(sqlDB, fsys, "migrations"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !tableExists(t, sqlDB, "t1") || !tableExists(t, sqlDB, "t2") {
		t.Fatal("expected t1 and t2 to exist")
	}
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", n)
	}
	if err := Migrate(sqlDB, fsys, "migrations"); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected still 2 applied migrations, got %d", n)
	}
}

func TestMigrateRejectsBadFileNameAndBadSQL(t *testing.T) {
	sqlDB := openMem(t)
	badName := fstest.MapFS{"migrations/users.sql": &fstest.MapFile{Data: []byte(`SELECT 1;`)}}
	if err := Migrate(sqlDB, badName, "migrations"); err == nil {
		t.Fatal("expected error for non-numeric version")
	}
	badSQL := fstest.MapFS{"migrations/0001_broken.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE;`)}}
	if err := Migrate(sqlDB, badSQL, "migrations"); err == nil {
		t.Fatal("expected error for broken SQL")
	}
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("failed migration must not be recorded, got %d applied", n)
	}
}

func TestSQLiteDSN(t *testing.T) {
	if got := SQLiteDSN(":memory:"); got != "file::memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)" {
		t.Fatalf("memory DSN = %q", got)
	}
	got := SQLiteDSN("/data/global.db")
	for _, want := range []string{"file:/data/global.db", "busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(ON)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("DSN %q missing %q", got, want)
		}
	}
}
