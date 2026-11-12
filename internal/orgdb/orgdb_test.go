package orgdb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/database"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(filepath.Join(t.TempDir(), "org.db")))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := Open(sqlDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func TestMeta(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.GetMeta(MetaWrappedDataKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := db.SetMeta(MetaWrappedDataKey, "wrapped123"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	v, err := db.GetMeta(MetaWrappedDataKey)
	if err != nil || v != "wrapped123" {
		t.Fatalf("GetMeta = %q, %v", v, err)
	}
	if err := db.SetMeta(MetaWrappedDataKey, "wrapped456"); err != nil {
		t.Fatalf("SetMeta overwrite: %v", err)
	}
	if v, _ := db.GetMeta(MetaWrappedDataKey); v != "wrapped456" {
		t.Fatalf("want overwrite, got %q", v)
	}
}

func TestMembers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	admin, err := db.AddMember("alice", "admin")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if admin.Role != "admin" || admin.CreatedAt.IsZero() {
		t.Fatalf("unexpected member: %+v", admin)
	}
	if _, err := db.AddMember("bob", "member"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := db.AddMember("alice", "member"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate member: want ErrConflict, got %v", err)
	}
	if _, err := db.AddMember("carol", "superuser"); err == nil {
		t.Fatal("invalid role accepted")
	}
	members, err := db.ListMembers()
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 || members[0].Username != "alice" || members[1].Username != "bob" {
		t.Fatalf("unexpected members: %+v", members)
	}
	if _, err := db.GetMemberByUsername("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
