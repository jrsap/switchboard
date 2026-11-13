// Package orgdb manages one organization's own database, written only by
// that org's child process: providers, models, aliases, members,
// permissions, quotas, API key hashes and the activity log. At this stage
// org meta (the wrapped data key), members, providers and upstream models
// exist; aliases, quotas, keys and the activity log come later.
//
// Like globaldb, the driver is opened at the call site; this package
// applies its own embedded migrations and runs the queries.
package orgdb

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

var (
	providerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	modelNameRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]{0,127}$`)
	levelRe        = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)
)

const maxReasoningLevels = 16

// Provider is a row of the providers table: one upstream OpenAI-compatible
// server. APIKey is the upstream key sealed with the org data key
// (seal.Encrypt, base64(nonce||ct)) — this package stores and returns the
// sealed value only; only the org child (which holds the unwrapped data key
// from the handshake) can open it. Empty string means the upstream needs
// no key.
type Provider struct {
	ID        int64
	Name      string
	BaseURL   string
	APIKey    string // sealed at rest; never plaintext
	CreatedAt time.Time
}

func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (db *DB) scanProvider(row interface{ Scan(...any) error }) (*Provider, error) {
	var p Provider
	var createdAt int64
	err := row.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKey, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.CreatedAt = time.Unix(0, createdAt).UTC()
	return &p, nil
}

// AddProvider inserts an upstream provider. apiKeySealed is the already
// sealed upstream key (seal.Encrypt with the org data key), or "".
func (db *DB) AddProvider(name, baseURL, apiKeySealed string) (*Provider, error) {
	if !providerNameRe.MatchString(name) {
		return nil, fmt.Errorf("orgdb: invalid provider name %q: 1-63 chars of [a-zA-Z0-9_.-], starting alphanumeric", name)
	}
	if !validBaseURL(baseURL) {
		return nil, fmt.Errorf("orgdb: invalid base_url %q: must be an http(s) URL with a host", baseURL)
	}
	res, err := db.sql.Exec(db.q.addProvider, name, baseURL, apiKeySealed, time.Now().UnixNano())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("orgdb: provider %q: %w", name, ErrConflict)
		}
		return nil, err
	}
	if _, err := res.LastInsertId(); err != nil {
		return nil, err
	}
	return db.GetProviderByName(name)
}

func (db *DB) GetProviderByID(id int64) (*Provider, error) {
	return db.scanProvider(db.sql.QueryRow(db.q.getProviderByID, id))
}

func (db *DB) GetProviderByName(name string) (*Provider, error) {
	return db.scanProvider(db.sql.QueryRow(db.q.getProviderByName, name))
}

func (db *DB) ListProviders() ([]*Provider, error) {
	rows, err := db.sql.Query(db.q.listProviders)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var providers []*Provider
	for rows.Next() {
		p, err := db.scanProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// DeleteProvider removes a provider and, via ON DELETE CASCADE, its models.
func (db *DB) DeleteProvider(id int64) error {
	res, err := db.sql.Exec(db.q.deleteProvider, id)
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

// Model is one upstream model of a provider. ToolCalling and Vision are
// manual capability checkboxes (no probing yet); ReasoningLevels is the
// user-specified list of supported reasoning levels (e.g. ["low","high"]).
type Model struct {
	ID              int64
	ProviderID      int64
	ProviderName    string
	Name            string
	ToolCalling     bool
	Vision          bool
	ReasoningLevels []string
	CreatedAt       time.Time
}

func encodeLevels(levels []string) (string, error) {
	if levels == nil {
		levels = []string{}
	}
	raw, err := json.Marshal(levels)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// NormalizeReasoningLevels validates, dedupes (order preserved) and caps a
// user-specified reasoning level list.
func NormalizeReasoningLevels(levels []string) ([]string, error) {
	seen := make(map[string]bool, len(levels))
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		if !levelRe.MatchString(l) {
			return nil, fmt.Errorf("orgdb: invalid reasoning level %q: 1-32 chars of [a-zA-Z0-9_-], starting alphanumeric", l)
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
		if len(out) > maxReasoningLevels {
			return nil, fmt.Errorf("orgdb: at most %d reasoning levels", maxReasoningLevels)
		}
	}
	return out, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (db *DB) scanModel(row interface{ Scan(...any) error }) (*Model, error) {
	var m Model
	var toolCalling, vision int64
	var levelsJSON string
	var createdAt int64
	err := row.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.Name, &toolCalling, &vision, &levelsJSON, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(levelsJSON), &m.ReasoningLevels); err != nil {
		return nil, fmt.Errorf("orgdb: decode reasoning_levels for model %d: %w", m.ID, err)
	}
	if m.ReasoningLevels == nil {
		m.ReasoningLevels = []string{}
	}
	m.ToolCalling = toolCalling != 0
	m.Vision = vision != 0
	m.CreatedAt = time.Unix(0, createdAt).UTC()
	return &m, nil
}

// AddModel inserts an upstream model under an existing provider.
func (db *DB) AddModel(providerID int64, name string, toolCalling, vision bool, reasoningLevels []string) (*Model, error) {
	if _, err := db.GetProviderByID(providerID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("orgdb: provider id %d: %w", providerID, ErrNotFound)
		}
		return nil, err
	}
	if !modelNameRe.MatchString(name) {
		return nil, fmt.Errorf("orgdb: invalid model name %q: 1-128 chars of [a-zA-Z0-9_./:-], starting alphanumeric", name)
	}
	levels, err := NormalizeReasoningLevels(reasoningLevels)
	if err != nil {
		return nil, err
	}
	levelsJSON, err := encodeLevels(levels)
	if err != nil {
		return nil, err
	}
	_, err = db.sql.Exec(db.q.addModel, providerID, name, boolToInt(toolCalling), boolToInt(vision), levelsJSON, time.Now().UnixNano())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("orgdb: model %q on provider %d: %w", name, providerID, ErrConflict)
		}
		return nil, err
	}
	return db.GetModel(providerID, name)
}

func (db *DB) GetModel(providerID int64, name string) (*Model, error) {
	return db.scanModel(db.sql.QueryRow(db.q.getModel, providerID, name))
}

// ListModels returns all upstream models of the org, ordered by provider
// then model name.
func (db *DB) ListModels() ([]*Model, error) {
	return db.listModels(db.q.listModels)
}

// ListModelsByProvider returns the upstream models of one provider.
func (db *DB) ListModelsByProvider(providerID int64) ([]*Model, error) {
	return db.listModels(db.q.listModelsByProvider, providerID)
}

func (db *DB) listModels(query string, args ...any) ([]*Model, error) {
	rows, err := db.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []*Model
	for rows.Next() {
		m, err := db.scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

// DeleteModel removes one upstream model by provider and name.
func (db *DB) DeleteModel(providerID int64, name string) error {
	res, err := db.sql.Exec(db.q.deleteModel, providerID, name)
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
