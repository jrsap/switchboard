# Switchboard

An OpenAI API compatible switchboard server. It defines models and model aliases and routes
them to different upstream servers, trying each configured upstream in order until one
succeeds (failover on connection errors, timeouts, 429 and 5xx).

## Architecture

- **Router process** (`cmd/switchboard`): spawns one child process per organization,
  hands each its unwrapped data key over a loopback handshake, then listens on the
  public port and demultiplexes requests to the children over loopback HTTP. It is
  stateless — no business logic, no database access; it holds only the root key and
  the per-org child configuration (see `config.example.json`).
- **Org child process** (`cmd/switchboard-org`): serves loopback HTTP per org —
  `/healthz`, the `/internal/handshake` key handoff, and (later) the org API — backed
  by its own org DB.
- **Global database** (`internal/globaldb`): users only. Org membership on the user
  record is a denormalized routing cache; each org DB is authoritative for its members.
- **Per-org database** (`internal/orgdb`): written only by the org's child. SQLite via
  `modernc.org/sqlite` (pure Go), migrations are embedded numbered `.sql` files applied
  by the shared hand-rolled runner in `internal/database`.
- **Admin CLI** (`cmd/switchboard-admin`): user management in the global DB, plus
  `org add` / `org list` (an org is created with exactly one admin user).
- **Key hierarchy** (`internal/seal`): root key held only by the router; per-org data
  keys stored wrapped (XChaCha20-Poly1305) in the org DB and handed unwrapped to the
  child in the spawn handshake; request bodies encrypted per org before storage.

Backend is Go, stdlib + `golang.org/x` only; `modernc.org/sqlite` is the single approved
exception. Frontend (later): plain HTML/CSS, no JS libraries, no SPA.

Router configuration is a single JSON file — see [docs/config.md](docs/config.md) for every
option and value format, and [docs/config.example.json](docs/config.example.json) for a
complete sample.

## Quickstart

```bash
go build ./...
cp config.example.json config.json   # set root_key (base64 32 bytes); global_db / data_dir are CLI defaults

# create an org (creates its storage, org DB, wrapped data key, and one admin user)
./switchboard-admin org add -id acme -name "Acme Corp" -admin-username alice
./switchboard-admin org list

# manage users in the global DB
./switchboard-admin user list

# run the router; it spawns one switchboard-org child per org
./switchboard -config config.json
```

API keys are structured as `sb_<orgid>_<secret>`; the org prefix is only a routing hint,
the org child verifies the secret hash against its own database.

## Tests

```bash
go test ./...
```
