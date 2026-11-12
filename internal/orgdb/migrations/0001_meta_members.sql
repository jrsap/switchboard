CREATE TABLE org_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE members (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    created_at INTEGER NOT NULL
);
