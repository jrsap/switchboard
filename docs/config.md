# Router configuration

The router process (`cmd/switchboard`) is configured with a single JSON file,
passed via `-config PATH` or the `SWITCHBOARD_CONFIG` environment variable
(default: `./config.json`). A complete sample lives in
[config.example.json](config.example.json); the repo root also has a minimal
`config.example.json` for quickstarts.

The router is stateless: this file is the only thing it reads. It holds the
root key and the per-child (per-org) configuration — no database access, no
business logic.

## Top-level options

| Option | Format | Meaning |
|---|---|---|
| `listen` | `host:port`, e.g. `"0.0.0.0:8080"` | Public address the router listens on. |
| `root_key` | base64 (standard encoding) of exactly 32 random bytes | Root key of the key hierarchy. Held only by the router process, never written to any DB; per-org data keys are wrapped with it and unwrapped in the loopback spawn handshake. Generate with e.g. `openssl rand -base64 32`. |
| `orgs` | array of org objects (at least one) | One entry per organization = one child process. |

## Org options (`orgs[]`)

| Option | Format | Meaning |
|---|---|---|
| `id` | `^[a-z0-9][a-z0-9-]{0,62}$` | Org id. Also the routing prefix of its API keys (`sb_<id>_<secret>`). Must be unique. |
| `name` | free text | Human-readable org name. |
| `child_addr` | `host:port`, e.g. `"127.0.0.1:9001"` | Loopback HTTP address of the org's child process. The router proxies all requests for this org there (HTTP over loopback, so children can later move to other hosts without protocol changes). |
| `storage.db` | path or DSN, e.g. `"/var/lib/switchboard/acme/org.db"` | Storage descriptor, part 1: the org's own database (providers, models, aliases, members, quotas, API key hashes, activity log). Currently a SQLite file path; Postgres DSN is the planned escape hatch. Never hardcoded elsewhere. |
| `storage.archive_root` | absolute path | Storage descriptor, part 2: directory for the org's encrypted request/response archives, separate from the org DB. |

## API key format

API keys look like `sb_<orgid>_<secret>` (e.g. `sb_acme_…`), where `<secret>`
is 32 random bytes, base64url-encoded. The `sb_<orgid>_` prefix is a routing
hint only: the router uses it to pick the child process, and the child
verifies the secret as a SHA-256 hash against its own org DB, so a valid key
presented with another org's prefix fails closed.

## Notes

- JSON has no comments, so explanations live in this document; keep
  `docs/config.example.json` valid JSON so it can be copied verbatim.
- Validation happens at load time: bad `root_key`, duplicate/invalid org
  ids, malformed `host:port` values, or missing storage fields abort startup
  with an error.
