#!/usr/bin/env bash
# Manage per-PR preview environments on the self-hosted runner machine.
#
#   preview_env.sh up   <pr-number> <repo-dir>   start (or restart) the preview for a PR
#   preview_env.sh down <pr-number>              stop the preview and remove its state
#
# State lives in ${PREVIEW_STATE_ROOT:-$HOME/switchboard-previews}/pr-<number>/
# with its own SQLite DB, config, logs, pid files and allocated ports.
# App ports are allocated from 9000-9099, mock upstream ports from 9100-9199
# (override with PREVIEW_APP_PORT_RANGE / PREVIEW_MOCK_PORT_RANGE, e.g. "9000-9099").
#
# Set PREVIEW_REAL_UPSTREAMS=1 in the runner environment to prepend the real
# mistral/openai upstreams (keys read from the runner's local environment via
# ${MISTRAL_API_KEY} / ${OPENAI_API_KEY} expansion in config.yaml; never stored
# in GitHub). By default previews use only the mock upstream.
set -euo pipefail

STATE_ROOT="${PREVIEW_STATE_ROOT:-$HOME/switchboard-previews}"
APP_RANGE="${PREVIEW_APP_PORT_RANGE:-9000-9099}"
MOCK_RANGE="${PREVIEW_MOCK_PORT_RANGE:-9100-9199}"

port_free() {
  python3 - "$1" <<'PY'
import socket, sys
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    try:
        s.bind(("0.0.0.0", int(sys.argv[1])))
    except OSError:
        sys.exit(1)
PY
}

alloc_port() { # $1=range "lo-hi"  $2=portfile -> echoes port
  local lo hi p
  lo="${1%%-*}"; hi="${1##*-}"
  if [[ -f "$2" ]]; then
    p=$(cat "$2")
    if port_free "$p"; then echo "$p"; return 0; fi
  fi
  p=$(python3 - "$lo" "$hi" <<'PY'
import socket, sys
lo, hi = int(sys.argv[1]), int(sys.argv[2])
for port in range(lo, hi + 1):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        try:
            s.bind(("0.0.0.0", port))
        except OSError:
            continue
        print(port)
        break
else:
    sys.exit("no free port in range")
PY
  )
  echo "$p" > "$2"
  echo "$p"
}

stop_pidfile() { # $1=pidfile
  [[ -f "$1" ]] || return 0
  local p
  p=$(cat "$1")
  kill "$p" 2>/dev/null || true
  for _ in 1 2 3 4 5; do
    kill -0 "$p" 2>/dev/null || break
    sleep 1
  done
  kill -9 "$p" 2>/dev/null || true
  rm -f "$1"
}

wait_http() { # $1=url $2=logfile-on-failure
  local i
  for i in $(seq 1 60); do
    if python3 -c "import urllib.request; urllib.request.urlopen('$1', timeout=2)" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for $1" >&2
  [[ -f "$2" ]] && tail -n 40 "$2" >&2
  return 1
}

down() {
  local pr="$1"
  local dir="$STATE_ROOT/pr-$pr"
  stop_pidfile "$dir/app.pid"
  stop_pidfile "$dir/mock.pid"
  rm -rf "$dir"
  echo "preview for PR #$pr torn down"
}

up() {
  local pr="$1" repo="$2"
  local dir="$STATE_ROOT/pr-$pr"
  mkdir -p "$dir"

  if [[ -f "$dir/app.pid" || -f "$dir/mock.pid" ]]; then
    down "$pr"
    mkdir -p "$dir"
  fi

  local app_port mock_port
  app_port=$(alloc_port "$APP_RANGE" "$dir/app.port")
  mock_port=$(alloc_port "$MOCK_RANGE" "$dir/mock.port")

  {
    echo "upstreams:"
    echo "  - name: mock"
    echo "    base_url: http://127.0.0.1:${mock_port}/v1"
    if [[ "${PREVIEW_REAL_UPSTREAMS:-0}" == "1" ]]; then
      echo "  - name: mistral"
      echo "    base_url: https://api.mistral.ai/v1"
      echo "    api_key: \${MISTRAL_API_KEY}"
      echo "  - name: openai"
      echo "    base_url: https://api.openai.com/v1"
      echo "    api_key: \${OPENAI_API_KEY}"
    fi
    echo "models:"
    echo "  mock-model:"
    echo "    - upstream: mock"
    echo "      model: mock-model"
    if [[ "${PREVIEW_REAL_UPSTREAMS:-0}" == "1" ]]; then
      echo "  mistral-large-4:"
      echo "    - upstream: mistral"
      echo "      model: mistral-large-latest"
      echo "  gpt-4o:"
      echo "    - upstream: openai"
      echo "      model: gpt-4o"
    fi
    echo "aliases:"
    if [[ "${PREVIEW_REAL_UPSTREAMS:-0}" == "1" ]]; then
      echo "  big:"
      echo "    - mistral-large-4"
      echo "    - gpt-4o"
      echo "    - mock-model"
    else
      echo "  big:"
      echo "    - mock-model"
    fi
    echo "  fast:"
    echo "    - mock-model"
  } > "$dir/config.yaml"

  export SWITCHBOARD_DATABASE_URL="sqlite:///$dir/switchboard.db"
  export SWITCHBOARD_SECRET_KEY
  SWITCHBOARD_SECRET_KEY=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
  export SWITCHBOARD_CONFIG_PATH="$dir/config.yaml"

  cd "$repo"
  python3 -m switchboard.cli init-db
  python3 -m switchboard.cli create-org --name Preview
  local web_password api_key key_out
  web_password=$(python3 -c 'import secrets; print(secrets.token_urlsafe(12))')
  python3 -m switchboard.cli create-user --username reviewer --password "$web_password" --org Preview --role admin
  key_out=$(python3 -m switchboard.cli create-key --username reviewer --org Preview --name default)
  api_key=$(printf '%s\n' "$key_out" | sed -n 's/^plaintext key (shown once): //p')
  [[ -n "$api_key" ]] || { echo "could not parse api key from CLI output:" >&2; printf '%s\n' "$key_out" >&2; exit 1; }

  {
    echo "PR=$pr"
    echo "APP_PORT=$app_port"
    echo "MOCK_PORT=$mock_port"
    echo "API_KEY=$api_key"
    echo "WEB_USERNAME=reviewer"
    echo "WEB_PASSWORD=$web_password"
  } > "$dir/info.env"
  chmod 600 "$dir/info.env"

  setsid nohup python3 "$repo/scripts/mock_upstream.py" "$mock_port" > "$dir/mock.log" 2>&1 &
  echo $! > "$dir/mock.pid"
  setsid nohup python3 -m uvicorn switchboard.main:app --host 0.0.0.0 --port "$app_port" > "$dir/app.log" 2>&1 &
  echo $! > "$dir/app.pid"

  if ! wait_http "http://127.0.0.1:$mock_port/healthz" "$dir/mock.log"; then
    stop_pidfile "$dir/app.pid"; stop_pidfile "$dir/mock.pid"; exit 1
  fi
  if ! wait_http "http://127.0.0.1:$app_port/healthz" "$dir/app.log"; then
    stop_pidfile "$dir/app.pid"; stop_pidfile "$dir/mock.pid"; exit 1
  fi

  echo "preview for PR #$pr up: http://127.0.0.1:$app_port (mock upstream on $mock_port, state in $dir)"
}

case "${1:-}" in
  up) up "$2" "$3" ;;
  down) down "$2" ;;
  *) echo "usage: $0 up|down <pr-number> [repo-dir]" >&2; exit 2 ;;
esac
