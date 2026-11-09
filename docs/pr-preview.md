# PR preview environments (self-hosted)

A machine in your private network runs a [self-hosted GitHub Actions runner][runner].
For every PR targeting `main`, the `PR preview` workflow (`.github/workflows/pr-preview.yml`)
runs on that machine and:

1. checks out the PR head and runs the pytest suite,
2. boots the app with a fresh SQLite DB and a mock upstream, seeds a throwaway
   org/user/API key, and smoke-tests `/healthz`, `GET /v1/models` and
   `POST /v1/chat/completions` (plain and streaming),
3. if all of that passes, leaves the instance running and posts a comment on the PR
   with the preview URL, web UI credentials and API key,
4. when the PR is closed, stops the instance and removes its state.

The check run is named **`PR preview / Bootstrap check and preview deploy`** and is green
exactly when the PR bootstraps and runs correctly — add it as a required status check in
branch protection (`Settings > Branches > Branch protection > Require status checks to pass`,
`Require branches to be up to date` recommended). The existing GitHub-hosted `CI` workflow
keeps running as before for all PRs, including forks.

Preview URLs look like `http://<host>:<port>/` where `<port>` is in 9000–9099 (one per PR)
and `<host>` is the repo variable `PREVIEW_ADVERTISED_HOST` if set (e.g. `switchboard-preview.lan`
with a wildcard DNS record pointing at the runner), otherwise the runner's first LAN IP.
The URL is reachable only from your private network (VPN/LAN) — firewall the runner so ports
9000–9199 are not exposed beyond it.

## Runner setup

On the machine that will host previews (Ubuntu/Debian example, run as a dedicated user):

```bash
mkdir ~/actions-runner && cd ~/actions-runner
curl -o actions-runner.tar.gz -L https://github.com/actions/runner/releases/download/v2.320.0/actions-runner-linux-x64-2.320.0.tar.gz
tar xzf actions-runner.tar.gz
./config.sh --url https://github.com/jrsap/switchboard --labels self-hosted,preview
sudo ./svc.sh install && sudo ./svc.sh start
```

(`./config.sh` asks for a runner registration token from
`Settings > Actions > Runners > New runner`.) The runner needs `python3`, `curl` and outbound
HTTPS to `github.com` only. Keep it persistent (not `--ephemeral`) so the service survives reboots.

Optionally set these in the runner's environment (`/etc/environment` or the systemd unit) to
customize behavior — none of them are GitHub secrets:

- `PREVIEW_STATE_ROOT` — where per-PR state lives (default `~/switchboard-previews`)
- `PREVIEW_ADVERTISED_HOST` is a **repo variable** (`Settings > Secrets and variables > Actions >
  Variables`), not an env var
- `PREVIEW_REAL_UPSTREAMS=1` — prepend the real mistral/openai upstreams to the preview config.
  Their keys are read from the runner's local environment (`MISTRAL_API_KEY`, `OPENAI_API_KEY`)
  via `${...}` expansion in the generated `config.yaml`; they are never sent to GitHub. By
  default previews use only the mock upstream, so no provider quota is spent and no real
  credentials leave the machine.
- `PREVIEW_APP_PORT_RANGE` / `PREVIEW_MOCK_PORT_RANGE` — port ranges (default `9000-9099` /
  `9100-9199`)

## Security notes

- The `pull_request` event never exposes secrets to fork PRs, and this workflow uses no
  secrets at all (preview credentials are generated per PR on the runner).
- The preview job is skipped for fork PRs unless someone with write access adds the
  **`preview-deploy`** label — do not add it blindly, because the job executes PR code on a
  machine that can reach your private network.
- Preview instances listen on `0.0.0.0` so the LAN/VPN can reach them; restrict the machine's
  firewall to the private network interfaces for ports 9000–9199.
- To exercise failover/502 behavior in a preview, restart its mock upstream with a forced
  failure: `MOCK_UPSTREAM_FAIL_STATUS=500` in the runner env, then re-run the workflow.

## Operating the previews

```bash
# list running previews
ls ~/switchboard-previews
# inspect one (ports, credentials, logs, pids)
cat ~/switchboard-previews/pr-7/info.env
# manual teardown (automatic on PR close)
bash scripts/preview_env.sh down 7
```

Each `up` on an already-deployed PR stops the old instance first, so pushes to a PR simply
redeploy it (same ports, fresh database).

[runner]: https://docs.github.com/en/actions/hosting-your-own-runners/about-self-hosted-runners
