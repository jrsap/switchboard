// Package globaldb manages the global database. It holds users only — no
// relationships or org data. Org membership on the user record is a
// denormalized routing cache; each org DB is authoritative for its members.
//
// The driver is opened at the call site (see internal/database) and passed
// in already initialized; this package only applies its own migrations and
// runs the user queries.
package globaldb

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jrsap/switchboard/internal/database"
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

// Open wraps an already-open *sql.DB and applies the pending global DB
// migrations. Closing the DB closes the underlying *sql.DB.
func Open(sqlDB *sql.DB) (*DB, error) {
	if err := database.Migrate(sqlDB, migrationsFS, "migrations"); err != nil {
		return nil, fmt.Errorf("globaldb: migrate: %w", err)
	}
	return &DB{sql: sqlDB, q: sqliteQueries}, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
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
