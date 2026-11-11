// Package orgdb manages one organization's own database, written only by
// that org's child process: providers, models, aliases, members,
// permissions, quotas, API key hashes and the activity log. At this stage
// only org meta (the wrapped data key) and members exist.
//
// Like globaldb, the driver is opened at the call site; this package
// applies its own embedded migrations and runs the queries.
package orgdb

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jrsap/switchboard/internal/database"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	ErrNotFound = errors.New("orgdb: not found")
	ErrConflict = errors.New("orgdb: conflict")
)

// Meta keys stored in org_meta.
const MetaWrappedDataKey = "wrapped_data_key"

type DB struct {
	sql *sql.DB
	q   queries
}

// Open wraps an already-open *sql.DB and applies the pending org DB
// migrations. Closing the DB closes the underlying *sql.DB.
func Open(sqlDB *sql.DB) (*DB, error) {
	if err := database.Migrate(sqlDB, migrationsFS, "migrations"); err != nil {
		return nil, fmt.Errorf("orgdb: migrate: %w", err)
	}
	return &DB{sql: sqlDB, q: sqliteQueries}, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

func (db *DB) SetMeta(key, value string) error {
	_, err := db.sql.Exec(db.q.setMeta, key, value)
	return err
}

func (db *DB) GetMeta(key string) (string, error) {
	var value string
	err := db.sql.QueryRow(db.q.getMeta, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}

// Member is a row of the members table. The org DB is authoritative for
// membership; the global DB only keeps a denormalized routing cache.
type Member struct {
	ID        int64
	Username  string
	Role      string // "admin" or "member"
	CreatedAt time.Time
}

func (db *DB) scanMember(row interface{ Scan(...any) error }) (*Member, error) {
	var m Member
	var createdAt int64
	err := row.Scan(&m.ID, &m.Username, &m.Role, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.CreatedAt = time.Unix(0, createdAt).UTC()
	return &m, nil
}

func (db *DB) AddMember(username, role string) (*Member, error) {
	if role != "admin" && role != "member" {
		return nil, fmt.Errorf("orgdb: invalid role %q", role)
	}
	res, err := db.sql.Exec(db.q.addMember, username, role, time.Now().UnixNano())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("orgdb: member %q: %w", username, ErrConflict)
		}
		return nil, err
	}
	if _, err := res.LastInsertId(); err != nil {
		return nil, err
	}
	return db.GetMemberByUsername(username)
}

func (db *DB) GetMemberByUsername(username string) (*Member, error) {
	return db.scanMember(db.sql.QueryRow(db.q.getMemberByName, username))
}

func (db *DB) ListMembers() ([]*Member, error) {
	rows, err := db.sql.Query(db.q.listMembers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []*Member
	for rows.Next() {
		m, err := db.scanMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
