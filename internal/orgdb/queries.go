package orgdb

// queries holds the SQL per dialect (see internal/database). SQLite now,
// Postgres (pgx) as a per-org escape hatch later.
type queries struct {
	setMeta         string
	getMeta         string
	addMember       string
	getMemberByName string
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
}

var sqliteQueries = queries{
	setMeta:         `INSERT INTO org_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
	getMeta:         `SELECT value FROM org_meta WHERE key = ?`,
	addMember:       `INSERT INTO members (username, role, created_at) VALUES (?, ?, ?)`,
	getMemberByName: `SELECT id, username, role, created_at FROM members WHERE username = ?`,
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
	listModelsByProvider: `SELECT m.id, m.provider_id, p.name, m.name, m.tool_calling, m.vision, m.reasoning_levels, m.created_at
	             FROM models m JOIN providers p ON p.id = m.provider_id
	             WHERE m.provider_id = ?
	             ORDER BY m.name`,
	deleteModel: `DELETE FROM models WHERE provider_id = ? AND name = ?`,
}
