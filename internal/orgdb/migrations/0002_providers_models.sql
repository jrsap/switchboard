CREATE TABLE providers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    base_url   TEXT NOT NULL,
    -- Upstream API key sealed with the org data key (seal.Encrypt,
    -- base64(nonce || ct)); empty string if the upstream needs no key.
    -- Never decrypted here; only the org child (handshake key) can open it.
    api_key    TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE models (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id   INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    -- Manual capability flags (checkboxes): 0/1, no probing yet.
    tool_calling  INTEGER NOT NULL DEFAULT 0,
    vision        INTEGER NOT NULL DEFAULT 0,
    -- JSON array of user-specified reasoning levels, e.g. ["low","high"].
    reasoning_levels TEXT NOT NULL DEFAULT '[]',
    created_at    INTEGER NOT NULL,
    UNIQUE (provider_id, name)
);
