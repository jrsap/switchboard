package globaldb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/database"
)

func openTestSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(openTestSQL(t, database.SQLiteDSN(filepath.Join(t.TempDir(), "global.db"))))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrationsAreApplied(t *testing.T) {
	db, err := Open(openTestSQL(t, database.SQLiteDSN(":memory:")))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least one applied migration")
	}
	// Re-running the migrations must be idempotent.
	if err := database.Migrate(db.sql, migrationsFS, "migrations"); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 applied migration, got %d", n)
	}
}

func TestUserCRUD(t *testing.T) {
	db := openTestDB(t)

	u, err := db.CreateUser("alice", "hash1", []string{"acme", "beta"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 || u.Username != "alice" || len(u.Orgs) != 2 || u.CreatedAt.IsZero() {
		t.Fatalf("unexpected user: %+v", u)
	}

	byName, err := db.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if byName.ID != u.ID || byName.PasswordHash != "hash1" {
		t.Fatalf("unexpected user: %+v", byName)
	}

	byID, err := db.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if byID.Username != "alice" {
		t.Fatalf("unexpected user: %+v", byID)
	}

	if _, err := db.CreateUser("alice", "hash2", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate username: want ErrConflict, got %v", err)
	}

	if err := db.SetPassword(u.ID, "hash3"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if err := db.SetOrgs(u.ID, []string{"gamma"}); err != nil {
		t.Fatalf("SetOrgs: %v", err)
	}
	updated, err := db.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if updated.PasswordHash != "hash3" || len(updated.Orgs) != 1 || updated.Orgs[0] != "gamma" {
		t.Fatalf("unexpected updated user: %+v", updated)
	}

	users, err := db.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("ListUsers: want 1 user, got %d", len(users))
	}

	if err := db.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := db.GetUser(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser after delete: want ErrNotFound, got %v", err)
	}
	if err := db.DeleteUser(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteUser again: want ErrNotFound, got %v", err)
	}
}

func TestNotFound(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.GetUserByUsername("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := db.SetPassword(999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := db.SetOrgs(999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
