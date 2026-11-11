package orgdb

// queries holds the SQL per dialect (see internal/database). SQLite now,
// Postgres (pgx) as a per-org escape hatch later.
type queries struct {
	setMeta         string
	getMeta         string
	addMember       string
	getMemberByName string
	listMembers     string
}

var sqliteQueries = queries{
	setMeta:         `INSERT INTO org_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
	getMeta:         `SELECT value FROM org_meta WHERE key = ?`,
	addMember:       `INSERT INTO members (username, role, created_at) VALUES (?, ?, ?)`,
	getMemberByName: `SELECT id, username, role, created_at FROM members WHERE username = ?`,
	listMembers:     `SELECT id, username, role, created_at FROM members ORDER BY username`,
}
