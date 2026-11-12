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
| `child_binary` | path to an executable, optional | The org child process the router spawns per org. Defaults to `switchboard-org` next to the router executable. |
| `global_db` | path, optional | Path to the global users DB. **Not used by the router** (it stays stateless, no DB access) — it is the default for the admin CLI's `-db`, so tooling has a single config file. CLI precedence: `-db` flag > `SWITCHBOARD_GLOBAL_DB` env > `global_db` > `./switchboard-global.db`. |
| `data_dir` | path, optional | Base directory for per-org storage (`<data_dir>/<orgid>/…`). Also CLI-only. Precedence: `-data-dir` flag > `SWITCHBOARD_DATA_DIR` env > `data_dir` > `./data`. |
| `orgs` | array of org objects (at least one at router startup; the file may start empty and be filled by `org add`) | One entry per organization = one child process. |

## Org options (`orgs[]`)

| Option | Format | Meaning |
|---|---|---|
| `id` | `^[a-z0-9][a-z0-9-]{0,62}$` | Org id. Also the routing prefix of its API keys (`sb_<id>_<secret>`). Must be unique. |
| `name` | free text | Human-readable org name. |
| `child_addr` | `host:port`, e.g. `"127.0.0.1:9001"` | Loopback HTTP address of the org's child process. The router spawns the child here and proxies all requests for this org there (HTTP over loopback, so children can later move to other hosts without protocol changes). |
| `wrapped_data_key` | base64 of `nonce(24) || ciphertext+tag` sealing a 32-byte org data key with the root key (XChaCha20-Poly1305) | The org's data key at rest, sealed with the root key. The router unwraps it at spawn and hands the plaintext key to the child in the loopback handshake (`POST /internal/handshake`); the same wrapped value is stored in the org DB. Created automatically by `switchboard-admin org add` — do not hand-edit. |
| `storage.db` | path or DSN, e.g. `"/var/lib/switchboard/acme/org.db"` | Storage descriptor, part 1: the org's own database (providers, models, aliases, members, quotas, API key hashes, activity log). Currently a SQLite file path; Postgres DSN is the planned escape hatch. Never hardcoded elsewhere. |
| `storage.archive_root` | absolute path | Storage descriptor, part 2: directory for the org's encrypted request/response archives, separate from the org DB. |

## Process lifecycle

On startup the router, per org: spawns `child_binary` with `-org`, `-addr`, `-db`,
`-archive-root` and a random `-spawn-token`, polls `GET http://<child_addr>/healthz`
until the child is up, then `POST /internal/handshake` (Bearer = spawn token) with the
unwrapped org data key. Only then does the router proxy requests for the org; before
that it answers `503`. The child keeps the data key in memory only. The router ↔ child
contract is just network link + spawn + storage descriptor — the scalability escape
hatch. (Running each child under its own OS user is a deployment concern, not yet
enforced in code.)

## Managing orgs

```bash
switchboard-admin -config config.json -db data/global.db org add -id acme -name "Acme Corp" -admin-username alice
switchboard-admin -config config.json org list
```

`org add` creates the per-org storage (`<data-dir>/<id>/org.db` + `archive/`), seals a
fresh data key with the root key, stores the wrapped key in the org DB, creates exactly
one `admin` member (creating the global user if needed and updating its org routing
cache), and appends the org to the router config. Restart the router to spawn the new
child.

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
