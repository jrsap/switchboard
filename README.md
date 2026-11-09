# Switchboard

An OpenAI API compatible switchboard server. It defines models and model aliases and routes
them to different upstream servers, trying each configured upstream in order until one
succeeds (failover on connection errors, timeouts, 429 and 5xx).

- `GET /v1/models` and `POST /v1/chat/completions` are supported (streaming included).
- Multiple users, each with multiple API keys. API keys are scoped to an organization.
- Users belong to organizations as `member` or `admin`; a user can be in multiple orgs.
- Every proxied request is logged (full request/response headers and bodies, upstream used,
  status, latency, token usage) and each user can review their own activity in the web UI.
- Organization admins can set usage quotas (requests / prompt tokens / completion tokens per
  day, sliding 24h window) and a model/alias allowlist from the web UI.

Built with FastAPI and requests. Persistence uses SQLAlchemy (SQLite by default).

## Quickstart

```bash
pip install -r requirements.txt
cp config.example.yaml config.yaml   # edit upstreams/keys
python -m switchboard.cli init-db
python -m switchboard.cli create-org --name Acme
python -m switchboard.cli create-user --username alice --org Acme --role admin
python -m switchboard.cli create-key --username alice --org Acme --name default
uvicorn switchboard.main:app --host 127.0.0.1 --port 8000
```

Then:

```bash
curl http://127.0.0.1:8000/v1/models -H "Authorization: Bearer sk-..."
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Authorization: Bearer sk-..." -H "Content-Type: application/json" \
  -d '{"model": "big", "messages": [{"role": "user", "content": "hello"}]}'
```

Web UI: open http://127.0.0.1:8000/login and sign in with the username/password created
above. `Activity` shows your logged requests with full headers and bodies; `Organizations`
shows your orgs and, for admins, the quota/allowlist settings.

## Configuration

`config.yaml` (path overridable via `SWITCHBOARD_CONFIG_PATH`):

- `upstreams`: named upstream servers with `base_url` and optional `api_key`
  (environment variables are expanded, e.g. `${MISTRAL_API_KEY}`).
- `models`: a model name maps to an ordered list of `{upstream, model}` targets. The
  upstream `model` is what gets sent to that provider, so a single model can be served by
  multiple providers under different names.
- `aliases`: a generic name (e.g. `big`, `fast`) maps to an ordered list of model names,
  which are flattened into targets. Aliases and models are both listed by `/v1/models`.

Server settings (env vars with `SWITCHBOARD_` prefix or a `.env` file):

- `SWITCHBOARD_DATABASE_URL` (default `sqlite:///./switchboard.db`)
- `SWITCHBOARD_SECRET_KEY` (used to sign web session cookies; set this in production)
- `SWITCHBOARD_CONFIG_PATH` (default `config.yaml`)
- `SWITCHBOARD_HOST`, `SWITCHBOARD_PORT` (defaults `127.0.0.1`, `8000`)
- `SWITCHBOARD_QUOTA_WINDOW_HOURS` (default `24`)

## Routing and failover

For `POST /v1/chat/completions`, the requested `model` is resolved to an ordered list of
targets. Each target is tried in order; the request body is forwarded with `model` rewritten
to the target's upstream model name and `Authorization: Bearer <upstream api_key>` added.
Connection errors, timeouts, HTTP 429 and HTTP 5xx trigger failover to the next target; other
4xx responses are passed through to the client. If every target fails, the API returns 502.
`stream: true` requests are proxied as SSE and the reassembled stream is logged.

## CLI

```bash
python -m switchboard.cli init-db
python -m switchboard.cli create-org --name Acme
python -m switchboard.cli create-user --username alice --org Acme --role admin   # prompts for password
python -m switchboard.cli create-key --username alice --org Acme --name default  # prints the key once
python -m switchboard.cli list-keys --username alice
```

## PR preview environments

PRs targeting `main` get a check run on a self-hosted runner in the private network: it is
green only if the test suite passes and the app bootstraps and answers `/healthz`,
`/v1/models` and `/v1/chat/completions`. On success a per-PR instance is left running and its
LAN/VPN-only URL (plus throwaway web UI and API credentials) is posted as a PR comment; the
instance is torn down when the PR closes. See [docs/pr-preview.md](docs/pr-preview.md) for
runner setup, branch protection and security notes.

## Tests

```bash
pip install -r requirements-dev.txt
pytest
```

## Running in a container

```bash
podman build -t switchboard .        # or: docker build -t switchboard .
cp config.example.yaml config.yaml   # edit upstreams/keys
mkdir -p data
podman run --rm -p 8000:8000 \
  -v "$PWD/config.yaml:/app/config.yaml:Z" \
  -v "$PWD/data:/data:Z" \
  -e MISTRAL_API_KEY=... -e OPENAI_API_KEY=... \
  switchboard
```

The image ships with `config.example.yaml` copied to `/app/config.yaml` and stores the
SQLite database at `/data/switchboard.db`, so mount `/data` to persist it and mount your own
`config.yaml` to override upstreams and keys. Upstream API keys can also be passed purely as
environment variables (e.g. `-e MISTRAL_API_KEY=...`), since `${VAR}` references in the config
are expanded. To manage orgs/users/keys with the CLI inside the container, override the
default command:

```bash
podman run --rm \
  -v "$PWD/config.yaml:/app/config.yaml:Z" \
  -v "$PWD/data:/data:Z" \
  switchboard python -m switchboard.cli create-org --name Acme
```

The tests spin up stub upstream servers on 127.0.0.1 and cover model listing, chat
completions, failover, quotas, allowlists, streaming, request logging and the web UI.

## Layout

```
switchboard/
  settings.py   env-based server settings
  config.py     YAML loading of upstreams / models / aliases
  db.py         engine/session helpers
  models.py     ORM: organizations, users, memberships, api keys, request logs
  security.py   password hashing, api key hashing, session tokens
  routing.py    model/alias -> ordered upstream targets
  proxy.py      upstream HTTP calls and failover (requests)
  policy.py     org allowlist + quota enforcement
  api.py        /v1/models and /v1/chat/completions
  web.py        login, activity viewer, org admin UI
  templates/    web UI templates
  cli.py        org/user/api-key management
tests/          pytest suite with stub upstream servers
```
