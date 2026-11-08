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
- Upstream servers are polled through their `GET /models` endpoint (periodically in the
  background and on demand when a request is routed to them) to track which servers and
  models are up, including model capabilities such as max context size and supported
  modalities. The `Status` page in the web UI shows all of this.

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
above. `Activity` shows your logged requests with full headers and bodies; `Status` shows
upstream server and model availability with capabilities; `Organizations` shows your orgs
and, for admins, the quota/allowlist settings.

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
- `SWITCHBOARD_HEALTH_POLL_INTERVAL_SECONDS` (default `60`; `0` disables the periodic poller)
- `SWITCHBOARD_HEALTH_POLL_TIMEOUT_SECONDS` (default `5`, per-upstream poll timeout)
- `SWITCHBOARD_HEALTH_STALE_AFTER_SECONDS` (default `30`; when a chat request is routed to an
  upstream whose last poll is older than this, a background re-poll is triggered; `0` re-polls
  on every routed request)

## Routing and failover

For `POST /v1/chat/completions`, the requested `model` is resolved to an ordered list of
targets. Each target is tried in order; the request body is forwarded with `model` rewritten
to the target's upstream model name and `Authorization: Bearer <upstream api_key>` added.
Connection errors, timeouts, HTTP 429 and HTTP 5xx trigger failover to the next target; other
4xx responses are passed through to the client. If every target fails, the API returns 502.
`stream: true` requests are proxied as SSE and the reassembled stream is logged.

## Upstream health polling and the status dashboard

Each configured upstream is polled at `GET <base_url>/models` (with its `api_key` as a bearer
token when configured). Results are stored in the `upstream_status` table and shown on the
`Status` web page, which lists every server (up/down, last checked, latency, error, model
count) and every model/alias with its targets, whether the server currently lists the upstream
model, and the model's capabilities.

Polling happens three ways:

- periodically, from a background thread started at server startup
  (`SWITCHBOARD_HEALTH_POLL_INTERVAL_SECONDS`);
- on demand, when a chat completion is routed to an upstream whose last poll is stale
  (`SWITCHBOARD_HEALTH_STALE_AFTER_SECONDS`) — the re-poll runs in a background thread so it
  never blocks the request;
- manually, via the "Refresh now" button on the `Status` page (`POST /status/refresh`).

Model capabilities are extracted from each entry of the upstream's model list. Common field
names are normalized: `max_context_length` / `max_model_len` / `context_length` /
`max_position_embeddings` become `max_context_length`, and `supported_modalities` /
`modalities` / `input_modalities`+`output_modalities` become `modalities`. Any other fields
returned by the upstream (e.g. `supported_parameters`, `owned_by`) are kept as-is.

## CLI

```bash
python -m switchboard.cli init-db
python -m switchboard.cli create-org --name Acme
python -m switchboard.cli create-user --username alice --org Acme --role admin   # prompts for password
python -m switchboard.cli create-key --username alice --org Acme --name default  # prints the key once
python -m switchboard.cli list-keys --username alice
```

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
  health.py     upstream /models polling, capability extraction, status persistence
  policy.py     org allowlist + quota enforcement
  api.py        /v1/models and /v1/chat/completions
  web.py        login, activity viewer, status dashboard, org admin UI
  templates/    web UI templates
  cli.py        org/user/api-key management
tests/          pytest suite with stub upstream servers
```
