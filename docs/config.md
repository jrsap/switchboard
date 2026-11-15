# Router configuration

The router process (`cmd/switchboard`) is configured with a single JSON file,
passed via `-config PATH` or the `SWITCHBOARD_CONFIG` environment variable
(default: `./config.json`). A complete sample lives in
[config.example.json](config.example.json); the repo root also has a minimal
`config.example.json` for quickstarts.

The config file holds **only global settings** — listen address, root key,
child binary, and the base directory for per-org storage. **Organizations are
not configured in this file.** An org exists iff its org database exists at
`<data_dir>/<orgid>/org.db`; the router discovers orgs that way and loads all
org-specific configuration from the org DB itself. The org child process
never reads (or even knows about) this config file: the router passes it
everything via command-line flags and the loopback handshake.

## Top-level options

| Option | Format | Meaning |
|---|---|---|
| `listen` | `host:port`, e.g. `"0.0.0.0:8080"` | Public address the router listens on. |
| `root_key` | base64 (standard encoding) of exactly 32 random bytes | Root key of the key hierarchy. Held only by the router process, never written to any DB; per-org data keys are wrapped with it and unwrapped in the loopback spawn handshake. Generate with e.g. `openssl rand -base64 32`. |
| `child_binary` | path to an executable, optional | The org child process the router spawns per org. Defaults to `switchboard-org` next to the router executable. |
| `global_db` | path, optional | Path to the global users DB. **Not used by the router** — it is the default for the admin CLI's `-db`, so tooling has a single config file. CLI precedence: `-db` flag > `SWITCHBOARD_GLOBAL_DB` env > `global_db` > `./switchboard-global.db`. |
| `data_dir` | path, optional | Base directory for per-org storage, same for all orgs. Each org DB path is derived as `<data_dir>/<orgid>/org.db` and its archive root as `<data_dir>/<orgid>/archive/`. The router uses it to discover orgs (and reads only the org DB meta); the admin CLI uses it as default for `-data-dir` (flag > `SWITCHBOARD_DATA_DIR` env > `data_dir` > `./data`). |

## Org discovery and per-org storage

- An org **exists iff its org DB file exists** at `<data_dir>/<orgid>/org.db`
  (`orgid` must match `^[a-z0-9][a-z0-9-]{0,62}$`). If the file does not
  exist, the org does not exist and API keys with that prefix fail closed at
  the router (401).
- The org DB is the org's own config store. Its `org_meta` table holds:
  - `wrapped_data_key`: the org data key sealed with the root key
    (base64 of `nonce(24) || ciphertext+tag`, XChaCha20-Poly1305). The router
    reads it at startup (read-only, no migrations) and unwraps it at spawn.
    Created automatically by `switchboard-admin org add` — do not hand-edit.
  - `org_name`: human-readable org name.
- Storage layout per org, derived from the org id — never hardcoded elsewhere:
  - `<data_dir>/<orgid>/org.db` — the org's own database (providers, models,
    aliases, members, quotas, API key hashes, activity log). Currently a
    SQLite file; Postgres DSN is the planned per-org escape hatch.
  - `<data_dir>/<orgid>/archive/` — directory for the org's encrypted
    request/response archives, separate from the org DB.
- The router allocates a fresh loopback `host:port` for each child at spawn
  time; child addresses are not stored anywhere.

## Process lifecycle

On startup the router scans `data_dir` for org DBs (`<orgid>/org.db`). Per
discovered org it: reads the wrapped data key from the org DB meta, allocates
a free loopback address, spawns `child_binary` with `-org`, `-addr`, `-db`,
`-archive-root` and a random `-spawn-token` (all the org information the
child needs comes from these flags — the child never opens the config file),
polls `GET http://<child_addr>/healthz` until the child is up, then
`POST /internal/handshake` (Bearer = spawn token) with the unwrapped org data
key. Only then does the router proxy requests for the org; before that it
answers `503`. The child keeps the data key in memory only. New orgs (new org
DBs under `data_dir`) are picked up on the next router start. The router ↔
child contract is just network link + spawn + storage paths — the scalability
escape hatch. (Running each child under its own OS user is a deployment
concern, not yet enforced in code.)

## Managing orgs

```bash
switchboard-admin -config config.json -db data/global.db org add -id acme -name "Acme Corp" -admin-username alice
switchboard-admin -config config.json org list
```

`org add` creates the per-org storage (`<data-dir>/<id>/org.db` + `archive/`),
seals a fresh data key with the root key, stores the wrapped key and the org
name in the org DB meta, creates exactly one `admin` member (creating the
global user if needed and updating its org routing cache). It does **not**
touch the router config file — the org is discovered from its org DB.
Restart the router to spawn the new org's child.

## Providers and models (per org)

Providers and their upstream models live in the per-org DB, located via the
data dir (`<data-dir>/<org>/org.db`). The admin CLI unwraps the org data key
(root key from the config unwrapping the wrapped key from the org DB meta) to
seal secrets:

```bash
switchboard-admin provider add -org acme -name openai -base-url https://api.openai.com/v1 -api-key sk-...
switchboard-admin provider list -org acme
switchboard-admin model add -org acme -provider openai -name gpt-4o -tool-calling -vision -reasoning-level low -reasoning-level high
switchboard-admin model list -org acme [-provider openai]
switchboard-admin model delete -org acme -provider openai -name gpt-4o
switchboard-admin provider delete -org acme -name openai   # cascades to its models
```

- The upstream API key is sealed with the org data key (`seal.Encrypt`,
  XChaCha20-Poly1305) before it is stored, so plaintext never hits disk; only
  the org child can open it after the spawn handshake. An empty `-api-key`
  means the upstream needs no key (e.g. a local server).
- `-tool-calling` / `-vision` are manual checkboxes on the model (no probing
  yet); `-reasoning-level` is repeatable and sets the model's user-specified
  list of supported reasoning levels (stored as a JSON array).
- Model autodiscovery (listing/probing the upstream) comes later; for now
  models are added manually.

## API key format

API keys look like `sb_<orgid>_<secret>` (e.g. `sb_acme_…`), where `<secret>`
is 32 random bytes, base64url-encoded. The `sb_<orgid>_` prefix is a routing
hint only: the router uses it to pick the child process, and the child
verifies the secret as a SHA-256 hash against its own org DB, so a valid key
presented with another org's prefix fails closed.

## Notes

- JSON has no comments, so explanations live in this document; keep
  `docs/config.example.json` valid JSON so it can be copied verbatim.
- Validation happens at load time: bad `root_key` or a malformed `listen`
  aborts startup with an error.
