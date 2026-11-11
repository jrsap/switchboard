package globaldb

// queries holds the SQL per dialect. SQLite is the default; Postgres (pgx)
// is the per-org escape hatch later.
type queries struct {
	createUser        string
	getUserByID       string
	getUserByUsername string
	listUsers         string
	deleteUser        string
	updatePassword    string
	updateOrgs        string
}

var sqliteQueries = queries{
	createUser: `INSERT INTO users (username, password_hash, orgs, created_at, updated_at)
	             VALUES (?, ?, ?, ?, ?)`,
	getUserByID: `SELECT id, username, password_hash, orgs, created_at, updated_at
	              FROM users WHERE id = ?`,
	getUserByUsername: `SELECT id, username, password_hash, orgs, created_at, updated_at
	              FROM users WHERE username = ?`,
	listUsers: `SELECT id, username, password_hash, orgs, created_at, updated_at
	            FROM users ORDER BY username`,
	deleteUser:     `DELETE FROM users WHERE id = ?`,
	updatePassword: `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
	updateOrgs:     `UPDATE users SET orgs = ?, updated_at = ? WHERE id = ?`,
}
