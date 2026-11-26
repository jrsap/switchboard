// Package orgdb manages one organization's own database, written only by
// that org's child process: providers, models, aliases, members,
// permissions, quotas, API key hashes and the activity log. At this stage
// org meta (the wrapped data key), members, providers, upstream models,
// API keys and aliases exist; quotas and the activity log come later.
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

// Meta keys stored in org_meta. The org DB is the org's own config store:
// the router and the admin CLI discover an org from the existence of its DB
// file and load these values from it (the config file holds no org entries).
const (
	MetaWrappedDataKey = "wrapped_data_key"
	MetaOrgName        = "org_name"
)

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

// ReadMeta reads one org_meta value from an already-open, already-migrated
// org DB without applying migrations. For discovery paths that must not
// write to the org DB (the router process reading the wrapped data key).
func ReadMeta(sqlDB *sql.DB, key string) (string, error) {
	var value string
	err := sqlDB.QueryRow(`SELECT value FROM org_meta WHERE key = ?`, key).Scan(&value)
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

func (db *DB) GetMemberByID(id int64) (*Member, error) {
	return db.scanMember(db.sql.QueryRow(db.q.getMemberByID, id))
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

// APIKey is a row of the api_keys table. Only the SHA-256 hash of the
// secret is stored (secret_hash); the plaintext is printed exactly once by
// the admin CLI at creation and never persisted. RevokedAt is nil while
// the key is active.
type APIKey struct {
	ID         int64
	MemberID   int64
	Username   string
	Label      string
	SecretHash string
	CreatedAt  time.Time
	RevokedAt  *time.Time
}

func (k *APIKey) Revoked() bool {
	return k.RevokedAt != nil
}

func (db *DB) scanAPIKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var createdAt int64
	var revokedAt sql.NullInt64
	err := row.Scan(&k.ID, &k.MemberID, &k.Username, &k.Label, &k.SecretHash, &createdAt, &revokedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	k.CreatedAt = time.Unix(0, createdAt).UTC()
	if revokedAt.Valid {
		t := time.Unix(0, revokedAt.Int64).UTC()
		k.RevokedAt = &t
	}
	return &k, nil
}

// AddAPIKey inserts a key owned by an existing member. secretHash is the
// already-computed SHA-256 hex of the secret (apikey.HashSecret); this
// package never sees the plaintext.
func (db *DB) AddAPIKey(memberID int64, label, secretHash string) (*APIKey, error) {
	if secretHash == "" {
		return nil, errors.New("orgdb: secret hash must not be empty")
	}
	if _, err := db.GetMemberByID(memberID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("orgdb: member id %d: %w", memberID, ErrNotFound)
		}
		return nil, err
	}
	res, err := db.sql.Exec(db.q.addAPIKey, memberID, label, secretHash, time.Now().UnixNano())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("orgdb: api key: %w", ErrConflict)
		}
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return nil, fmt.Errorf("orgdb: member id %d: %w", memberID, ErrNotFound)
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.GetAPIKeyByID(id)
}

func (db *DB) GetAPIKeyByID(id int64) (*APIKey, error) {
	row := db.sql.QueryRow(`SELECT k.id, k.member_id, m.username, k.label, k.secret_hash, k.created_at, k.revoked_at
	                        FROM api_keys k JOIN members m ON m.id = k.member_id WHERE k.id = ?`, id)
	return db.scanAPIKey(row)
}

// GetAPIKeyByHash looks a key up by the SHA-256 hex of its secret. Used by
// the child's auth middleware; the caller still does the constant-time
// comparison of the hashes.
func (db *DB) GetAPIKeyByHash(secretHash string) (*APIKey, error) {
	return db.scanAPIKey(db.sql.QueryRow(db.q.getAPIKeyByHash, secretHash))
}

func (db *DB) ListAPIKeys() ([]*APIKey, error) {
	rows, err := db.sql.Query(db.q.listAPIKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*APIKey
	for rows.Next() {
		k, err := db.scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// RevokeAPIKey marks a key revoked; it stays in the table and fails closed
// at auth time. Revoking an already-revoked key returns ErrNotFound.
func (db *DB) RevokeAPIKey(id int64) error {
	res, err := db.sql.Exec(db.q.revokeAPIKey, time.Now().UnixNano(), id)
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
// A provider still referenced by an alias entry cannot be deleted
// (ON DELETE RESTRICT on alias_entries); that surfaces as ErrConflict.
func (db *DB) DeleteProvider(id int64) error {
	res, err := db.sql.Exec(db.q.deleteProvider, id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("orgdb: provider id %d is still referenced by an alias entry: %w", id, ErrConflict)
		}
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

// DeleteModel removes one upstream model by provider and name. A model
// still referenced by an alias entry cannot be deleted (ON DELETE RESTRICT
// on alias_entries); that surfaces as ErrConflict.
func (db *DB) DeleteModel(providerID int64, name string) error {
	res, err := db.sql.Exec(db.q.deleteModel, providerID, name)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("orgdb: model %q on provider %d is still referenced by an alias entry: %w", name, providerID, ErrConflict)
		}
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

// AliasEntry is one entry of an alias's ordered failover list: an upstream
// model of an upstream provider, referenced as "provider/model".
type AliasEntry struct {
	Position     int
	ProviderID   int64
	ProviderName string
	ModelID      int64
	ModelName    string
}

// String returns the entry in its "provider/model" form.
func (e *AliasEntry) String() string {
	return e.ProviderName + "/" + e.ModelName
}

// Alias is what the public API exposes as a model: a name mapped to an
// ordered list of upstream provider/model entries, tried in order until
// one succeeds (failover happens in the proxy, later).
type Alias struct {
	ID        int64
	Name      string
	Entries   []*AliasEntry
	CreatedAt time.Time
}

// ParseAliasEntry splits an alias entry in "provider/model" form. The cut
// is at the first "/" because provider names cannot contain "/" but model
// names can.
func ParseAliasEntry(entry string) (provider, model string, err error) {
	provider, model, ok := strings.Cut(entry, "/")
	if !ok || provider == "" || model == "" {
		return "", "", fmt.Errorf("orgdb: invalid alias entry %q: want \"provider/model\"", entry)
	}
	return provider, model, nil
}

func (db *DB) scanAlias(row interface{ Scan(...any) error }) (*Alias, error) {
	var a Alias
	var createdAt int64
	err := row.Scan(&a.ID, &a.Name, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.CreatedAt = time.Unix(0, createdAt).UTC()
	return &a, nil
}

func (db *DB) scanAliasEntry(row interface{ Scan(...any) error }) (*AliasEntry, error) {
	var e AliasEntry
	err := row.Scan(&e.Position, &e.ProviderID, &e.ProviderName, &e.ModelID, &e.ModelName)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (db *DB) listEntries(aliasID int64) ([]*AliasEntry, error) {
	rows, err := db.sql.Query(db.q.listAliasEntries, aliasID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*AliasEntry
	for rows.Next() {
		e, err := db.scanAliasEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (db *DB) withEntries(a *Alias) (*Alias, error) {
	entries, err := db.listEntries(a.ID)
	if err != nil {
		return nil, err
	}
	a.Entries = entries
	return a, nil
}

// AddAlias creates an alias mapping name to the ordered list of entries
// ("provider/model" strings, order preserved as failover order). Every
// entry must reference an existing provider and upstream model; duplicate
// entries and an empty entry list are rejected. The alias and its entries
// are inserted in one transaction.
func (db *DB) AddAlias(name string, entries []string) (*Alias, error) {
	if !modelNameRe.MatchString(name) {
		return nil, fmt.Errorf("orgdb: invalid alias name %q: 1-128 chars of [a-zA-Z0-9_./:-], starting alphanumeric", name)
	}
	if len(entries) == 0 {
		return nil, errors.New("orgdb: alias needs at least one entry")
	}
	type resolved struct {
		providerID, modelID int64
	}
	resolvedEntries := make([]resolved, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		providerName, modelName, err := ParseAliasEntry(entry)
		if err != nil {
			return nil, err
		}
		if seen[entry] {
			return nil, fmt.Errorf("orgdb: duplicate alias entry %q", entry)
		}
		seen[entry] = true
		p, err := db.GetProviderByName(providerName)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("orgdb: alias entry %q: provider %q: %w", entry, providerName, ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		m, err := db.GetModel(p.ID, modelName)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("orgdb: alias entry %q: model %q on provider %q: %w", entry, modelName, providerName, ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		resolvedEntries = append(resolvedEntries, resolved{p.ID, m.ID})
	}

	tx, err := db.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(db.q.addAlias, name, time.Now().UnixNano())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("orgdb: alias %q: %w", name, ErrConflict)
		}
		return nil, err
	}
	aliasID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for pos, e := range resolvedEntries {
		if _, err := tx.Exec(db.q.addAliasEntry, aliasID, pos, e.providerID, e.modelID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetAliasByName(name)
}

func (db *DB) GetAliasByName(name string) (*Alias, error) {
	a, err := db.scanAlias(db.sql.QueryRow(db.q.getAliasByName, name))
	if err != nil {
		return nil, err
	}
	return db.withEntries(a)
}

func (db *DB) GetAliasByID(id int64) (*Alias, error) {
	a, err := db.scanAlias(db.sql.QueryRow(db.q.getAliasByID, id))
	if err != nil {
		return nil, err
	}
	return db.withEntries(a)
}

// ListAliases returns all aliases of the org ordered by name, each with its
// entries in failover (position) order.
func (db *DB) ListAliases() ([]*Alias, error) {
	rows, err := db.sql.Query(db.q.listAliases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aliases []*Alias
	for rows.Next() {
		a, err := db.scanAlias(rows)
		if err != nil {
			return nil, err
		}
		aliases = append(aliases, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, a := range aliases {
		if _, err := db.withEntries(a); err != nil {
			return nil, err
		}
	}
	return aliases, nil
}

// DeleteAlias removes an alias and, via ON DELETE CASCADE, its entries.
func (db *DB) DeleteAlias(name string) error {
	res, err := db.sql.Exec(db.q.deleteAlias, name)
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
