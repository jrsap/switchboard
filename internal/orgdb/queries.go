package orgdb

// queries holds the SQL per dialect (see internal/database). SQLite now,
// Postgres (pgx) as a per-org escape hatch later.
type queries struct {
	setMeta         string
	getMeta         string
	addMember       string
	getMemberByName string
	getMemberByID   string
	listMembers     string

	addProvider          string
	getProviderByID      string
	getProviderByName    string
	listProviders        string
	deleteProvider       string
	addModel             string
	getModel             string
	listModels           string
	listModelsByProvider string
	deleteModel          string

	addAPIKey       string
	getAPIKeyByHash string
	listAPIKeys     string
	revokeAPIKey    string
}

var sqliteQueries = queries{
	setMeta:         `INSERT INTO org_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
	getMeta:         `SELECT value FROM org_meta WHERE key = ?`,
	addMember:       `INSERT INTO members (username, role, created_at) VALUES (?, ?, ?)`,
	getMemberByName: `SELECT id, username, role, created_at FROM members WHERE username = ?`,
	getMemberByID:   `SELECT id, username, role, created_at FROM members WHERE id = ?`,
	listMembers:     `SELECT id, username, role, created_at FROM members ORDER BY username`,

	addProvider:       `INSERT INTO providers (name, base_url, api_key, created_at) VALUES (?, ?, ?, ?)`,
	getProviderByID:   `SELECT id, name, base_url, api_key, created_at FROM providers WHERE id = ?`,
	getProviderByName: `SELECT id, name, base_url, api_key, created_at FROM providers WHERE name = ?`,
	listProviders:     `SELECT id, name, base_url, api_key, created_at FROM providers ORDER BY name`,
	deleteProvider:    `DELETE FROM providers WHERE id = ?`,

	addModel: `INSERT INTO models (provider_id, name, tool_calling, vision, reasoning_levels, created_at)
	           VALUES (?, ?, ?, ?, ?, ?)`,
	getModel: `SELECT m.id, m.provider_id, p.name, m.name, m.tool_calling, m.vision, m.reasoning_levels, m.created_at
	           FROM models m JOIN providers p ON p.id = m.provider_id
	           WHERE m.provider_id = ? AND m.name = ?`,
	listModels: `SELECT m.id, m.provider_id, p.name, m.name, m.tool_calling, m.vision, m.reasoning_levels, m.created_at
	             FROM models m JOIN providers p ON p.id = m.provider_id
	             ORDER BY p.name, m.name`,
	addAPIKey: `INSERT INTO api_keys (member_id, label, secret_hash, created_at) VALUES (?, ?, ?, ?)`,
	getAPIKeyByHash: `SELECT k.id, k.member_id, m.username, k.label, k.secret_hash, k.created_at, k.revoked_at
	                   FROM api_keys k JOIN members m ON m.id = k.member_id
	                   WHERE k.secret_hash = ?`,
	listAPIKeys: `SELECT k.id, k.member_id, m.username, k.label, k.secret_hash, k.created_at, k.revoked_at
	                   FROM api_keys k JOIN members m ON m.id = k.member_id
	                   ORDER BY k.created_at, k.id`,
	revokeAPIKey: `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,

	listModelsByProvider: `SELECT m.id, m.provider_id, p.name, m.name, m.tool_calling, m.vision, m.reasoning_levels, m.created_at
	             FROM models m JOIN providers p ON p.id = m.provider_id
	             WHERE m.provider_id = ?
	             ORDER BY m.name`,
	deleteModel: `DELETE FROM models WHERE provider_id = ? AND name = ?`,
}
