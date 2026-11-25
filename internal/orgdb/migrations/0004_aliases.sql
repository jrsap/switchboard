CREATE TABLE aliases (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Alias name is what clients send as the "model" field and what
    -- GET /v1/models exposes, so it follows the model name charset.
    name       TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

-- Ordered failover list of one alias. position is explicit and unique per
-- alias; an alias cannot list the same provider/model twice. Entries must
-- always reference existing providers and models: deleting a provider or
-- model still referenced by an alias entry is rejected (ON DELETE RESTRICT),
-- so the invariant holds at all times.
CREATE TABLE alias_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    alias_id    INTEGER NOT NULL REFERENCES aliases(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL CHECK (position >= 0),
    provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    model_id    INTEGER NOT NULL REFERENCES models(id) ON DELETE RESTRICT,
    UNIQUE (alias_id, position),
    UNIQUE (alias_id, provider_id, model_id)
);
