# deploy/linux — one-key stack bring-up (Linux)

`deploy.sh` starts the four-process eval stack — `agentserver-stub`
(stub mode only), `observer-server`, `slave-agent`, `driver-agent
serve-daemon` — in a single invocation, waits for readiness, and
emits a `machine_topology` JSON blob suitable for the eval-runner's
D1 `runs.machine_topology` column.

- **Spec**: [`docs/specs/wt2-deploy-scripts.spec.md`](../../../docs/specs/wt2-deploy-scripts.spec.md)
- **Plan**: [`docs/specs/wt2-deploy-scripts.plan.md`](../../../docs/specs/wt2-deploy-scripts.plan.md)
- **Boundary**: this script owns the *orchestrator*. It never modifies
  the per-component installers (`observer/install.sh`, `slave/install.sh`,
  `driver/install.sh`); it invokes them as subprocesses.

---

## Quick start

```bash
# Prereqs (one-off): jq (present on most distros), curl, yq (Go yq —
# `pacman -S go-yq` / `snap install yq` / `brew install yq` — install-
# only in stub mode; prod mode reads the yaml natively).
cd multi-agent

# Build every binary the stack needs with the arch-suffixed names the
# per-component installers expect (this repo's install scripts look for
# e.g. driver-agent.linux-amd64, not driver-agent).
ARCH=$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)
for cmd in driver-agent slave-agent observer-server; do
  CGO_ENABLED=0 go build -o deploy/linux/bin/${cmd}.linux-${ARCH} ./cmd/${cmd}
done
go build -o deploy/linux/bin/agentserver-stub ./tools/eval/agentserver-stub

# Stub mode (default): brings up all 4 processes against the local
# agentserver-stub — no OAuth device flow.
bash deploy/linux/deploy.sh --stub

# Same, but plan-only (no side effects):
bash deploy/linux/deploy.sh --stub --dry-run
```

Successful `--stub` exits 0 with:
- `~/.loom/eval-deploy/.pids/{agentserver-stub,observer,slave,driver}.pid`
  (mode `0600`, each holding a live PID)
- Machine topology JSON as the final line of stdout, validating against
  [`../topology_schema.json`](../topology_schema.json).

## Teardown

```bash
bash deploy/linux/deploy.sh --shutdown
# reads $LOOM_HOME/.pids/*.pid, SIGTERM → 5s grace → SIGKILL, cleans .pids
```

## CLI reference

```
deploy.sh [--stub | --prod | --mode {stub|prod}] \
          [--observer-port 18091] [--driver-port 18092] [--slave-port 18093] \
          [--stub-port 18080] [--loom-home DIR] [--bin-dir DIR] \
          [--dry-run] [--allow-model-key-passthrough] \
          [--topology-out PATH]

deploy.sh --shutdown [--loom-home DIR]
```

- `--stub` (default) — bring up against agentserver-stub on
  `127.0.0.1:$STUB_PORT`. Signs its own 5-tuple credentials via
  `agentserver-stub issue`; no OAuth. Stub is HTTP-only (no yamux
  tunnel plane, no `/api/agent/tasks/poll`), so this mode delivers
  *process liveness* — not end-to-end driver-to-slave task dispatch,
  which needs prod agentserver or a future stub extension.
- `--prod` — bring up against pre-registered `$LOOM_HOME` (the
  operator ran `driver-agent register` / `slave-agent` first-run
  device-code login previously, per
  [`../../tests/prod_test/E2E_RUNBOOK.md`](../../tests/prod_test/E2E_RUNBOOK.md)).
  deploy.sh does NOT invoke install.sh in prod mode — the per-component
  installers unconditionally re-render `config.yaml` from a blank
  template, which would clobber `credentials.proxy_token`.
- `--dry-run` — print the resolved plan as JSON, exit 0. No processes
  spawned, no files written. Argv values matching common secret flags
  (`--api-key`, `--token`, `--secret`, `--password`, `--bearer`) are
  replaced with `<REDACTED>`; env values in the printed
  `planned_env_whitelist` are keys-only (values never printed).
- `--allow-model-key-passthrough` — prod-only opt-in for propagating
  `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` from the parent env into the
  driver+slave subprocesses (the pre-existing `codex` / `claude` CLIs
  they fork need these in most prod setups; see
  [`../../tests/prod_test/E2E_RUNBOOK.md:80`](../../tests/prod_test/E2E_RUNBOOK.md)).
  Off by default; passing the flag emits a WARN line per key. Passing
  it with `--stub` is a preflight error (stub has no model plane).

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. All 4 (stub mode) / 3 (prod mode) readiness gates passed; PID files written. |
| `2` | Preflight failure — bad flag, bad port, missing prereq (jq, yq, or a required binary), prod-mode config incomplete. |
| `3` | Sub-installer (`observer/install.sh` etc.) returned non-zero. |
| `4` | Readiness gate timeout (default 30 s; override via `LOOM_DEPLOY_READY_TIMEOUT_SEC`). |
| `5` | Topology emit failed (SHA-256 unavailable, JSON assembly error). |

## Security notes (mirrors spec §7)

- **(a) stub loopback-only** — the `agentserver-stub` argv is
  hard-coded to `--listen 127.0.0.1:$STUB_PORT`. `--stub-port` is
  configurable; the *host* is not. No env override channel
  (`LOOM_STUB_LISTEN` is not read; T7 grep asserts).
- **(b) OAuth stays on disk** — prod mode never rewrites any file
  under `$LOOM_HOME`, so a previously registered `credentials.proxy_token`
  can never be clobbered by this script.
- **(c) fail-fast** — first executable line is `set -euo pipefail`.
- **(d) hostname redacted** — `machine_topology` `host` field is
  `sha256(hostname)[:8]` (or `<hash>@<hash>` for `alice@corp-laptop`
  style). `$USER` is never emitted.
- **(e) port validation** — every port must be in `[1024, 65535]`
  and not in the blacklist `{22, 23, 25, 53, 80, 110, 143, 443, 465,
  587, 993, 995, 3389, 5432, 6379, 8080, 8443}`; observer/driver/
  slave/stub ports are pairwise distinct.
- **(g) env whitelist** — subprocesses inherit only
  `{PATH, HOME, LANG, LC_ALL, TZ, USER}` unconditionally + optional
  `{AGENTSERVER_ROOT, MODELSERVER_ROOT, APP_ROOT, MOCK_MODEL_URL}` +
  any `LOOM_*` variable + the model-key exception above. Everything
  else — including `AWS_*`, `GITHUB_TOKEN`, `DOCKER_CONFIG`,
  `NPM_TOKEN` — dropped.
- **(h) `--dry-run` redaction** — see the argv/env rules above; every
  test in the T8 family asserts that a distinctive fixture key placed
  in the parent env does not appear anywhere in `--dry-run` output.

## Tests

```bash
# bats-core (fallback: bash multi-agent/deploy/_bats_shim.sh <file>)
bats multi-agent/deploy/linux/deploy_test.bats
bats multi-agent/deploy/test_machine_topology.bats
bats multi-agent/deploy/test_topology_schema.bats
bats multi-agent/deploy/test_template_parity.bats
```

## Windows counterpart

See [`../windows/README.md`](../windows/README.md) for the PowerShell
equivalent (`deploy.ps1`). The behaviour is identical modulo Windows-
native cmdlets for TCP probing / topology collection.
