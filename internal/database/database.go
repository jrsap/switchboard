// Package database holds the generic database plumbing shared by all
// switchboard databases (the global DB and, later, the per-org DBs):
// DSN construction and the hand-rolled migration runner. Driver imports
// live at the call sites, not here, so this package stays driver-agnostic.
package database

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SQLiteDSN builds a DSN for modernc.org/sqlite (driver name "sqlite")
// with sane pragmas. Use ":memory:" for tests.
func SQLiteDSN(path string) string {
	if path == ":memory:" {
		return "file::memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	}
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)"
}

// Migrate applies the numbered .sql files in dir of fsys in filename order,
// tracking applied versions in the schema_migrations table. Hand-rolled on
// purpose: no third-party migration tool. File names look like
// "0001_users.sql"; the part before the first "_" must be numeric.
func Migrate(sqlDB *sql.DB, fsys fs.FS, dir string) error {
	if _, err := sqlDB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".sql")
		versionStr, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			return fmt.Errorf("migration file %q: version must be numeric", e.Name())
		}
		var applied int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		stmt, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return err
		}
		tx, err := sqlDB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(stmt)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().UnixNano()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
