// Package globaldb manages the global database. It holds users only — no
// relationships or org data. Org membership on the user record is a
// denormalized routing cache; each org DB is authoritative for its members.
package globaldb

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	ErrNotFound = errors.New("globaldb: not found")
	ErrConflict = errors.New("globaldb: conflict")
)

type DB struct {
	sql *sql.DB
	q   queries
}

// Open opens (creating if necessary) the global SQLite database at path and
// applies all pending embedded migrations. Use ":memory:" for tests.
func Open(path string) (*DB, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path +
			"?_pragma=busy_timeout(5000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=foreign_keys(ON)"
	} else {
		dsn = "file::memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("globaldb: open %s: %w", path, err)
	}
	db := &DB{sql: sqlDB, q: sqliteQueries}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("globaldb: migrate %s: %w", path, err)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

// migrate is a small hand-rolled migration runner: embedded numbered .sql
// files are applied in order, tracked in schema_migrations.
func (db *DB) migrate() error {
	if _, err := db.sql.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
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
		err = db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied)
		if err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		stmt, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := db.sql.Begin()
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

// User is a row of the global users table. Orgs is the denormalized routing
// cache; the per-org DBs are authoritative for membership.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Orgs         []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (db *DB) scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var orgsJSON string
	var createdAt, updatedAt int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &orgsJSON, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(orgsJSON), &u.Orgs); err != nil {
		return nil, fmt.Errorf("globaldb: decode orgs for user %d: %w", u.ID, err)
	}
	if u.Orgs == nil {
		u.Orgs = []string{}
	}
	u.CreatedAt = time.Unix(0, createdAt).UTC()
	u.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return &u, nil
}

func encodeOrgs(orgs []string) (string, error) {
	if orgs == nil {
		orgs = []string{}
	}
	raw, err := json.Marshal(orgs)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (db *DB) CreateUser(username, passwordHash string, orgs []string) (*User, error) {
	orgsJSON, err := encodeOrgs(orgs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixNano()
	res, err := db.sql.Exec(db.q.createUser, username, passwordHash, orgsJSON, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("globaldb: username %q: %w", username, ErrConflict)
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.GetUser(id)
}

func (db *DB) GetUser(id int64) (*User, error) {
	return db.scanUser(db.sql.QueryRow(db.q.getUserByID, id))
}

func (db *DB) GetUserByUsername(username string) (*User, error) {
	return db.scanUser(db.sql.QueryRow(db.q.getUserByUsername, username))
}

func (db *DB) ListUsers() ([]*User, error) {
	rows, err := db.sql.Query(db.q.listUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u, err := db.scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (db *DB) DeleteUser(id int64) error {
	res, err := db.sql.Exec(db.q.deleteUser, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) SetPassword(id int64, passwordHash string) error {
	res, err := db.sql.Exec(db.q.updatePassword, passwordHash, time.Now().UnixNano(), id)
	if err != nil {
		return err
	}
	return rowsAffected(res, ErrNotFound)
}

func (db *DB) SetOrgs(id int64, orgs []string) error {
	orgsJSON, err := encodeOrgs(orgs)
	if err != nil {
		return err
	}
	res, err := db.sql.Exec(db.q.updateOrgs, orgsJSON, time.Now().UnixNano(), id)
	if err != nil {
		return err
	}
	return rowsAffected(res, ErrNotFound)
}

func rowsAffected(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}
