# Switchboard

An OpenAI API compatible switchboard server. It defines models and model aliases and routes
them to different upstream servers, trying each configured upstream in order until one
succeeds (failover on connection errors, timeouts, 429 and 5xx).

## Architecture

- **Router process** (`cmd/switchboard`): holds only the root key and the base data
  dir from the config file — orgs are **not** in the config. An org exists iff its
  org DB exists at `<data_dir>/<orgid>/org.db`; the router discovers orgs that way,
  loads each org's wrapped data key from its org DB meta, allocates a loopback
  address, spawns one child per org, hands each its unwrapped data key over a
  loopback handshake, then listens on the public port and demultiplexes requests to
  the children over loopback HTTP. No business logic.
- **Org child process** (`cmd/switchboard-org`): never reads the config file — the
  router passes it everything it needs as startup flags (`-org`, `-addr`, `-data-dir`,
  `-spawn-token`) plus the unwrapped data key in the `/internal/handshake` request.
  From the org id and the base data dir it derives its own storage paths
  (`<data-dir>/<org>/org.db`, `<data-dir>/<org>/archive/`, see `internal/orgpath`).
  It serves loopback HTTP per org — `/healthz`, the handshake, and (later) the org
  API — backed by its own org DB.
- **Global database** (`internal/globaldb`): users only. Org membership on the user
  record is a denormalized routing cache; each org DB is authoritative for its members.
- **Per-org database** (`internal/orgdb`): written only by the org's child. SQLite via
  `modernc.org/sqlite` (pure Go), migrations are embedded numbered `.sql` files applied
  by the shared hand-rolled runner in `internal/database`.
- **Admin CLI** (`cmd/switchboard-admin`): user management in the global DB, plus
  `org add` / `org list` (an org is created with exactly one admin user) and
  per-org `provider` / `model` commands (providers with sealed upstream API keys,
  upstream models with manual tool-calling/vision checkboxes and a user-specified
  reasoning-level list), written to the org DB located via the data dir
  (`<data_dir>/<org>/org.db`).
- **Key hierarchy** (`internal/seal`): root key held only by the router; per-org data
  keys stored wrapped (XChaCha20-Poly1305) in the org DB and handed unwrapped to the
  child in the spawn handshake; request bodies encrypted per org before storage.

Backend is Go, stdlib + `golang.org/x` only; `modernc.org/sqlite` is the single approved
exception. Frontend (later): plain HTML/CSS, no JS libraries, no SPA.

Router configuration is a single JSON file with only global settings (listen, root
key, child binary, global_db/data_dir defaults) — see [docs/config.md](docs/config.md)
for every option and value format, and [docs/config.example.json](docs/config.example.json)
for a complete sample. Orgs are discovered from their per-org databases under
`data_dir`, not from the config file.

## Quickstart

```bash
go build ./...
cp config.example.json config.json   # set root_key (base64 32 bytes); global_db / data_dir are CLI defaults

# create an org: creates <data-dir>/acme/ (org DB with wrapped data key + org name,
# archive dir, one admin user). The config file is NOT modified; the router
# discovers the org from its org DB. Restart the router to spawn its child.
./switchboard-admin org add -id acme -name "Acme Corp" -admin-username alice
./switchboard-admin org list

# manage users in the global DB
./switchboard-admin user list

# configure providers and upstream models for an org (stored in the org DB)
./switchboard-admin provider add -org acme -name openai -base-url https://api.openai.com/v1 -api-key sk-...
./switchboard-admin model add -org acme -provider openai -name gpt-4o -tool-calling -vision -reasoning-level low -reasoning-level high
./switchboard-admin model list -org acme

# define the public model names the org's API exposes (GET /v1/models): each alias maps to an
# ordered failover list of provider/model entries, tried in order until one succeeds
./switchboard-admin alias add -org acme -name big -entry openai/gpt-4o
./switchboard-admin alias list -org acme

# manage org API keys (sb_<orgid>_<secret>): only a SHA-256 hash of the secret
# is stored; `key add` prints the full key exactly once
./switchboard-admin key add -org acme -username alice -label ci
./switchboard-admin key list -org acme
./switchboard-admin key revoke -org acme -id 1

# run the router; it spawns one switchboard-org child per org
./switchboard -config config.json
```

API keys are structured as `sb_<orgid>_<secret>`; the org prefix is only a routing hint,
the org child verifies the secret hash against its own database.

## Tests

```bash
go test ./...
```
