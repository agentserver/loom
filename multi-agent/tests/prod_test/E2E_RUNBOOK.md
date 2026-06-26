# Prod E2E Runbook (host mode)

How to set up, run, and tear down the prod e2e environment against the real `agent.cs.ac.cn` agentserver. This document covers **infrastructure only** — specific test targets live in the test scripts themselves (`driver_mcp_e2e.py`, `commander-live.spec.ts`, etc.).

> **For commander multi-pod / state-persistence verification, this runbook is the WRONG tool** — host mode runs a single observer process at `:18091`, so the production "two pods, different state" failure mode cannot reproduce here. Use `tests/k8s_commander/` (minikube + 3-replica observer + mock agentserver) for that surface. The two suites are complementary; see [§ Commander multi-pod e2e (k8s)](#commander-multi-pod-e2e-k8s) below.

The `tests/prod_test/` tree (except whitelisted files) is `.gitignore`-d: it holds OAuth credentials, sqlite state, and locally-rebuilt binaries. **The actual configs live in this directory in the canonical repo checkout (`/root/multi-agent/multi-agent/tests/prod_test/` in the dev environment) and are NOT visible from worktrees.**

> **After each successful or failed run, update this file in-place** with whatever drifted: short_ids, port numbers, model/provider settings, config paths, new gotchas. The file is meant to stay current with the actual setup state.

## Topology (host mode — single observer process)

```
                      ┌────────────────────────┐
                      │  agent.cs.ac.cn (prod) │
                      └──┬──────────┬──────────┘
                         │tunnel    │whoami
        ┌────────────────┴───┐   ┌──┴────────────────┐
        │ slave-A daemon     │   │ observer (host)   │
        │ :18093             │   │ :18091            │
        └──────┬─────────────┘   └──────┬────────────┘
        ┌──────┴─────────────┐          │
        │ slave-B daemon     │──────────┘ daemon-link WS
        │ :18094             │           ws://:18091
        └────────────────────┘
                                       ┌────────────────────┐
                                       │ driver daemon      │
                                       │ :18092             │
                                       │ (host)             │
                                       └──────┬─────────────┘
                                              │ stdio JSON-RPC
                                       ┌──────┴─────────────┐
                                       │ driver MCP         │
                                       │ (forked by codex)  │
                                       └────────────────────┘
```

| Component       | Process                          | Listen        | Config                                    |
| --------------- | -------------------------------- | ------------- | ----------------------------------------- |
| observer        | `observer-server -config ...`    | `:18091`      | `.commander-manual/observer.yaml`         |
| driver daemon   | `driver-agent serve-daemon`      | `:18092`      | `driver-codex-local/config.yaml`          |
| driver MCP      | forked by `codex` CLI            | stdio         | `driver-codex-local/.codex/config.toml`   |
| slave A daemon  | `slave-agent`                    | `:18093`      | `slave-codex-local/config.yaml`           |
| slave B daemon  | `slave-agent`                    | `:18094`      | `slave-codex-local-b/config.yaml`         |

All four agents MUST share the same `workspace_id` (currently `96bd3120-…` / "嘿嘿"). A wrong-workspace slave shows up in agentserver but is invisible to driver's `list_agents`.

## Prereqs

- `OPENAI_API_KEY` env var set (driver MCP startup checks for it).
- `codex` CLI in `$PATH` (used to fork the driver MCP server).
- A local modelserver proxy reachable from this host — see "Model provider" below.
- Linux/amd64 host. `bin/*.linux-amd64` rebuilt from current branch HEAD.

## Automated runner (`run_e2e.sh`)

The recommended way to run e2e tests. Automates Steps 0–5 below and invokes the test scripts:

```bash
# Full run (rebuild binaries + all registered test scripts):
./tests/prod_test/run_e2e.sh

# Skip binary rebuild (when binaries are already current):
./tests/prod_test/run_e2e.sh --no-rebuild

# Selective:
./tests/prod_test/run_e2e.sh --skip-playwright   # skip Playwright tests
./tests/prod_test/run_e2e.sh --skip-mcp           # skip driver MCP test
```

Set `WORKTREE_ROOT` if running from a worktree (defaults to `../../` relative to the script).

The script uses `trap cleanup EXIT` to guarantee teardown even on failure. Pre-flight token checks treat HTTP 403 as a warning (sandbox may recover after tunnel reconnects), but HTTP 401 is fatal.

Test scripts invoked by the runner:
- **`driver_mcp_e2e.py`** — Python 3 stdio client for driver MCP (define assertions there)
- **Playwright live config** — `internal/commanderhub/webapp/playwright.live.config.ts` (specs under `src/e2e/`)

To add a new test target, add it as a phase in `run_e2e.sh` and put assertions in the test script itself.

## Manual steps (when not using `run_e2e.sh`)

### Step 0 — Rebuild binaries from branch HEAD

```bash
cd <multi-agent module root>           # e.g. .claude/worktrees/<branch>/multi-agent
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /root/multi-agent/multi-agent/tests/prod_test/bin/driver-agent.linux-amd64 \
    ./cmd/driver-agent
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /root/multi-agent/multi-agent/tests/prod_test/bin/slave-agent.linux-amd64 \
    ./cmd/slave-agent
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /root/multi-agent/multi-agent/tests/prod_test/bin/observer-server.linux-amd64 \
    ./cmd/observer-server
```

If `Text file busy` — a previous instance is still holding the binary; kill its PID first.

### Step 1 — Pre-flight checks

From `/root/multi-agent/multi-agent/tests/prod_test/`:

```bash
# Stale procs from a previous session?
ps -ef | grep -E 'slave-agent|driver-agent|observer-server' | grep -v grep
# If any belong to a prior session, kill them BEFORE starting fresh ones.

# Stale locks left from a killed process?
rm -f slave-agent.lock slave-codex-local/slave-agent.lock slave-codex-local-b/slave-agent.lock

# Token health (driver, slave A, slave B):
for cfg in driver-codex-local/config.yaml slave-codex-local/config.yaml slave-codex-local-b/config.yaml; do
    TOK=$(grep proxy_token "$cfg" | awk '{print $2}')
    echo -n "$cfg: "
    curl -sS -o /dev/null -w "HTTP %{http_code}\n" -H "Authorization: Bearer $TOK" https://agent.cs.ac.cn/api/agent/whoami
done
```

Expected: all three return `HTTP 200`.

- **HTTP 403 = sandbox `forbidden`** — agentserver flipped the sandbox out of `running` state. See the re-register section.
- **HTTP 401 = token invalid** — credentials were revoked or wiped. Re-register.

### Step 2 — Start services (order matters)

```bash
cd /root/multi-agent/multi-agent/tests/prod_test

# observer first — listen for daemon-link before anyone dials it
cd .commander-manual
nohup ../bin/observer-server.linux-amd64 -config observer.yaml > logs/observer.log 2>&1 &
disown
sleep 2
cd ..

# slaves — each one's tunnel goroutine registers the sandbox; the commander
# daemon goroutine waits on tunnel-ready (PR #32 fix) before dialing observer
cd slave-codex-local
rm -f slave-agent.lock
nohup ../bin/slave-agent.linux-amd64 ./config.yaml > logs/slave-agent.log 2>&1 &
disown
cd ../slave-codex-local-b
rm -f slave-agent.lock
nohup ../bin/slave-agent.linux-amd64 ./config.yaml > logs/slave-agent.log 2>&1 &
disown
cd ..

# driver daemon last — needs observer up
cd driver-codex-local
nohup ../bin/driver-agent.linux-amd64 serve-daemon --config ./config.yaml --listen 127.0.0.1:18092 > logs/driver-daemon.log 2>&1 &
disown
cd ..

sleep 6
# Verify all four ports listening
for port in 18091 18092 18093 18094; do
    ss -tlnp "sport = :$port" | grep -q LISTEN && echo ":$port OK" || echo ":$port DOWN"
done
# Verify each slave log shows BOTH "tunnel connected" AND "commander daemon ready"
for d in slave-codex-local slave-codex-local-b; do
    echo "=== $d ==="
    tail -8 "$d/logs/slave-agent.log"
done
```

Healthy log shape per slave (PR #32 ordering fix):

```
agentsdk: connecting to https://agent.cs.ac.cn
agentsdk: tunnel connected (sandbox: <uuid>)
slave-agent: commander daemon ready http=http://127.0.0.1:1809x
```

If `observer unauthorized: status 401` appears, the slave-daemon-startup race is back; the gate in `cmd/slave-agent/main.go::run()` is missing or broken.

### Step 3 — Run tests

Run your test scripts against the running environment. Two common approaches:

**Via `codex exec` (exercises the real codex thread):**

```bash
cd driver-codex-local
CODEX_HOME=$PWD/.codex codex exec --cd $PWD "<your prompt>"
```

The codex CLI forks `driver-agent serve-mcp` per the `[mcp_servers.driver]` block in `.codex/config.toml`. `driver-agent serve-mcp --config` MUST point at the SAME yaml the daemon reads (`driver-codex-local/config.yaml`) so `short_id` matches on both legs — without this, `loom_origin` parent links break.

**Via direct stdio client (faster, deterministic, no codex token spend):**

Use `driver_mcp_e2e.py` or write a custom client:
- spawn `driver-agent serve-mcp --config .../driver-codex-local/config.yaml`
- wire format: **newline-delimited JSON-RPC** (NOT LSP Content-Length); write `json + "\n"`, split stdout on `\n`
- Note: `submit_task` only accepts `target_display_name`. Passing `target_short_id` silently falls back to `driver_defaults.target_display_name` (= slave A) — verified via the tool's inputSchema.

### Step 4 — Tear down

```bash
ps -ef | grep -E 'slave-agent|driver-agent|observer-server' | grep -v grep
kill <pids>
# locks are best-effort cleaned up on next start
```

## Re-registering agents (OAuth device flow)

When `whoami` returns 403/401, the sandbox token can't be revived. Re-register:

**Important:** Start observer FIRST, then register agents and keep them all running. Killing a registered agent immediately makes its sandbox go `forbidden` — tokens cannot be reused after that.

**For slaves:**

```bash
cd slave-codex-local       # or slave-codex-local-b
cp config.yaml config.yaml.bak-pre-reregister-$(date +%s)
python3 -c "
import yaml
with open('config.yaml') as f: c = yaml.safe_load(f)
c['credentials'] = {'sandbox_id':'','tunnel_token':'','proxy_token':'','workspace_id':'','short_id':''}
with open('config.yaml','w') as f: yaml.dump(c, f, default_flow_style=False, sort_keys=False)
"
rm -f slave-agent.lock
nohup ../bin/slave-agent.linux-amd64 ./config.yaml > logs/slave-agent.log 2>&1 &
disown
sleep 4
grep -A1 'Open this URL' logs/slave-agent.log
```

**For the driver:** (uses a separate `register` subcommand)

```bash
cd driver-codex-local
# clear creds same as above, then:
../bin/driver-agent.linux-amd64 register --config ./config.yaml
# After approval, start the daemon separately:
nohup ../bin/driver-agent.linux-amd64 serve-daemon --config ./config.yaml --listen 127.0.0.1:18092 > logs/driver-daemon.log 2>&1 &
```

Open the printed URL in a browser, complete the consent prompt.

**Browser workspace pitfall:** the OAuth consent screen defaults to whatever workspace your account has open. Confirm the workspace is `96bd3120-…` ("嘿嘿") — NOT a different workspace. A wrong-workspace agent registers fine but is invisible to the driver. After approval, double-check:

```bash
grep workspace_id config.yaml
# must match driver-codex-local/config.yaml's workspace_id
```

## Model provider

Both slaves and the driver run the codex CLI to do agent work. The CLI reads `model_providers.<name>` from their per-agent `.codex/config.toml`. As of 2026-06-25 the working setup is:

```toml
model_provider = "modelserver"
model = "glm-5.2"
model_reasoning_effort = "xhigh"

[model_providers.modelserver]
name = "modelserver"
base_url = "http://127.0.0.1:53452/v1"
experimental_bearer_token = "<token>"
wire_api = "responses"
```

- Pointing `base_url` at the upstream `https://code.ai.cs.ac.cn/v1` with `model = "gpt-5.5"` (the historical default) hits capacity errors during peak hours and the e2e fails with `ERROR: Selected model is at capacity`.
- glm-5.2 is served via the local proxy and is reliable.
- The local proxy uses `experimental_bearer_token`, NOT `env_key = "OPENAI_API_KEY"`. Setting `env_key` against the local proxy silently sends the wrong bearer.

All three per-agent `.codex/config.toml` files (`driver-codex-local/.codex/`, `slave-codex-local/.codex/`, `slave-codex-local-b/.codex/`) need the same provider block — drift here causes one agent to work and the next to fail mysteriously.

## Current agent state (2026-06-26)

| Agent | short_id | workspace_id |
| --- | --- | --- |
| driver | `9ve6sfzf` | `96bd3120-a725-44d9-a047-a75ed89af3ed` |
| slave-A | `82arwg4f` | `96bd3120-a725-44d9-a047-a75ed89af3ed` |
| slave-B | `53et0rx4` | `96bd3120-a725-44d9-a047-a75ed89af3ed` |

## Common failure modes

| Symptom | Likely cause | Fix |
| ------- | ------------ | --- |
| Slave exits seconds after start with `observer unauthorized: status 401` | slave-daemon-startup race regressed (daemon dialed observer before tunnel registered sandbox) | Verify `cmd/slave-agent/main.go::run()` waits on `tn.Ready()` before `daemon.Run` (PR #32 fix); rebuild binary |
| Slave runs but commander UI doesn't show it | `daemon.auto_start: false` in slave's config.yaml | Flip to `true`, restart |
| `wait_task` reports `failed` with `codex exit: exit status 1` and no output | Per-agent `.codex/config.toml` points at a model the proxy can't route | Align to working model + provider (see above) |
| `submit_task` succeeds but always lands on slave-A regardless of `target_short_id` | Used `target_short_id` arg (doesn't exist in inputSchema); driver silently falls back to `driver_defaults.target_display_name` | Use `target_display_name` |
| `whoami` returns 403 | sandbox state went `offline` or `forbidden` on agentserver | Re-register (no other fix; this is the issue #290 / sandbox-token-forbidden behavior) |
| Killing a slave makes its sandbox immediately `forbidden` | agentserver detects tunnel disconnect and flips sandbox state | Don't kill agents between registration and test completion; re-register if needed |
| Driver MCP `tools/call` reports `user cancelled MCP tool call` | Upstream model request was 429 / capacity / network-cancelled mid-tool-call; codex relabels it as user-cancel | Retry; if persistent, swap models per the provider section |
| `list_agents` only returns one slave even though both daemons are healthy | Second slave's PublishCard hasn't propagated yet (race with first invocation) or the second slave's last reconnect cycle re-published stale card | Wait 30-60s and retry; if persistent, restart the missing slave to trigger a fresh PublishCard |
| Commander login intermittently `HTTP 404` in production but never in host e2e | Multi-pod observer-server with in-memory commander state — host e2e runs ONE observer process so it can't reproduce | Use `tests/k8s_commander/` to reproduce; the fix landed via authstore.Store (PR #37) |

## Commander multi-pod e2e (k8s)

Host-mode e2e above runs a **single** observer process at `:18091`. That is fine for exercising driver/slave behavior against the real agentserver, but it CANNOT reproduce or regress-test failure modes that only appear when commander state must be shared across observer-server replicas — most prominently the `登录失败: HTTP 404` symptom that motivated `internal/commanderhub/authstore/` (PR #37).

`tests/k8s_commander/` covers exactly that surface. It stands up the production-shaped topology in a local minikube cluster:

```
                +-----------------------+
                | mock-agentserver (1×) |
                +-----------+-----------+
                            |
                +-----------+-----------+
                | observer (3×, ClusterIP, NO sessionAffinity) |
                +-----------+-----------+
                            |
                      postgres (1×)
```

**The mock agentserver is intentional** — this suite isolates the multi-pod cross-replica state contracts (login_id consistency, cookie hash storage, advisory-lock cap) from device-flow + sandbox + tunnel concerns that host-mode e2e already covers against the real agentserver. Don't try to point it at `agent.cs.ac.cn`; that's host-mode's job.

### When to run which

| Surface to verify | Use | Why |
| --- | --- | --- |
| Driver MCP tool semantics, slave registration, tunnel/sandbox lifecycle, real codex calls | host e2e (`tests/prod_test/run_e2e.sh`) | Needs the real agentserver + a real OAuth-issued sandbox |
| Commander login pollable from any pod, cookie usable across pods, cross-pod logout, cap under concurrency, `pg_advisory_xact_lock` actually serializing | k8s e2e (`tests/k8s_commander/run_e2e.sh`) | Needs ≥2 observer pods sharing one Postgres |
| Unit + tx-level contracts of `authstore.Store` | `go test ./internal/commanderhub/...` (and `OBSERVER_POSTGRES_TEST_DSN=... go test ...` for the postgres conformance suite) | Cheap and DSN-gated |

### Run

Prereqs: `docker`, `minikube` ≥ 1.38, `kubectl`. See `tests/k8s_commander/README.md` for the canonical instructions; quick version:

```bash
# One-time per host:
minikube start --driver=docker --force --cpus=4 --memory=6g --kubernetes-version=v1.31.4

# Build images inside minikube's docker daemon (no registry push):
eval $(minikube docker-env)
docker build -f cmd/observer-server/Dockerfile -t observer-server:e2e .

# Build the mock agentserver — its Go file lives at
# tests/k8s_commander/mock-agentserver/main.go but is built against the
# repo's go.mod, so build the binary then bake into the image:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
    -o tests/k8s_commander/mock-agentserver/mock-agentserver \
    ./tests/k8s_commander/mock-agentserver
docker build -t mock-agentserver:e2e tests/k8s_commander/mock-agentserver/

# Apply + run:
kubectl apply -f tests/k8s_commander/manifests.yaml
kubectl -n commander-e2e wait --for=condition=Ready pod -l app=observer-server --timeout=180s
./tests/k8s_commander/run_e2e.sh
```

### What each step proves (and what it would have looked like pre-fix)

| Step | Asserted property | Pre-fix behavior |
| --- | --- | --- |
| 1 | POST /login on pod A → 200 + login_id | 200 (same) |
| 2 | GET /poll on **pod B** → 200 pending | **404 "unknown login"** — the production bug |
| 3 | [C1] inline Set-Cookie 200 ok via Service round-robin | First wrong-pod /poll 404'd |
| 4 | Cookie authenticates on **every pod** (cross-pod `GetSession` via shared DB) | Only the issuing pod accepted it |
| 5 | Logout on one pod invalidates cookie **everywhere** | Other pods kept accepting it |
| 6 | 1100 concurrent POST /login: 200 count never exceeds `MaxActiveLogins=1024`, AND some 429s observed | Per-pod 64-cap allowed 3× upstream amplification |

### Re-running

The k8s e2e leaves commander state in Postgres between runs (Step 6 in particular fills the cap). Either wait `loginTTL = 10min` for the sweeper, or:

```bash
kubectl -n commander-e2e exec deploy/postgres -- \
  psql -U observer -d observer -c 'TRUNCATE commander_logins, commander_sessions'
```

### Teardown

```bash
kubectl delete namespace commander-e2e        # keep minikube
# or
minikube delete                                 # destroy the cluster too
```

### Common k8s e2e failure modes

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `observer-server` pod stuck in `Init:0/1` for >30s | Postgres still starting (DB connection retries log every 2s in `wait-and-migrate` init) | Wait — the init container retries forever; this is expected for ~30-40s |
| Step 6 reports `200=N, 429=0` with N << 1024 | port-forward connection churn dropped requests before they hit the cap | Restart minikube; if persistent, lower `xargs -P` concurrency in `run_e2e.sh` |
| Step 6 reports `200 > 1024` | `pg_advisory_xact_lock` not actually serializing — implementation regression | Check `internal/commanderhub/authstore/postgres.go::ReserveLogin` advisory-lock call is intact |
| `userspace object store is required when store.driver is postgres` in observer logs | configmap missing `object_store.driver: memory` block | Reapply `manifests.yaml`; the `memory` driver is required because the commander e2e never exercises the object proxy but the userspace init path still requires one |
| `minikube start` warns about root + docker driver | Running as root | Pass `--force`; safe in a dev VM |
| Step 2 → 404 unexpectedly | Reverted to in-memory state (rebuild without authstore wiring) | Re-run `eval $(minikube docker-env) && docker build -t observer-server:e2e .` from this branch's HEAD |

## When you finish a run

**Edit this file** to capture what you learned:

- Did short_ids, sandbox UUIDs, or workspace_ids change? Update the "Current agent state" table.
- Did you hit a NEW failure mode? Add it to the table.
- Did a fix in the codebase remove an old gotcha? Cross it off — don't leave stale warnings.
- Did the model provider change? Update the "Model provider" section.

The file is one of the few persistent artifacts that survives session/context loss. Memory entries `[[prod-e2e-setup]]` and `[[slave-daemon-startup-race]]` summarize from this file, not the other way around.

For the **k8s commander e2e** section above:

- Did the manifest defaults drift (image tag, postgres image, resource requests)? Update the section + `tests/k8s_commander/README.md`.
- Did a new "what step N proves" assertion get added to `run_e2e.sh`? Update the step table here.
- Did a new failure mode bite you under minikube? Add a row to the k8s failure-modes table.
