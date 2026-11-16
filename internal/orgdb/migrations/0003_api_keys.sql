CREATE TABLE api_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id  INTEGER NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    -- Optional user-facing label, e.g. "ci".
    label      TEXT NOT NULL DEFAULT '',
    -- SHA-256 hex of the secret (apikey.HashSecret). The plaintext secret
    -- is never stored; it is printed exactly once by the admin CLI.
    secret_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    -- NULL while the key is active; set on revoke. Revoked keys stay in the
    -- table (audit) and fail closed at auth time.
    revoked_at INTEGER
);
