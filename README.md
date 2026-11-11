# Switchboard

An OpenAI API compatible switchboard server. It defines models and model aliases and routes
them to different upstream servers, trying each configured upstream in order until one
succeeds (failover on connection errors, timeouts, 429 and 5xx).

## Architecture

- **Router process** (`cmd/switchboard`): listens on the public port and demultiplexes
  requests to one child process per organization over loopback HTTP. It is stateless —
  no business logic, no database access; it holds only the root key and the per-org
  child configuration (see `config.example.json`).
- **Global database** (`internal/globaldb`): users only. Org membership on the user
  record is a denormalized routing cache; each org DB is authoritative for its members.
  SQLite via `modernc.org/sqlite` (pure Go), migrations are embedded numbered `.sql`
  files applied by a hand-rolled runner.
- **Admin CLI** (`cmd/switchboard-admin`): configures users in the global database
  (orgs / providers / models come later, against per-org DBs).

Backend is Go, stdlib + `golang.org/x` only; `modernc.org/sqlite` is the single approved
exception. Frontend (later): plain HTML/CSS, no JS libraries, no SPA.

## Quickstart

```bash
go build ./...
cp config.example.json config.json   # set root_key (base64 32 bytes) and orgs

# manage users in the global DB
./switchboard-admin -db data/global.db user add -username alice -org acme
./switchboard-admin -db data/global.db user list

# run the router
./switchboard -config config.json
```

API keys are structured as `sb_<orgid>_<secret>`; the org prefix is only a routing hint,
the org child verifies the secret hash against its own database.

## Tests

```bash
go test ./...
```
