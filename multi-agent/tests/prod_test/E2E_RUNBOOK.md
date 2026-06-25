# Prod E2E Runbook (host mode)

Step-by-step for running the two-slave + driver-daemon + driver-mcp + observer e2e against the real `agent.cs.ac.cn` agentserver.

The `tests/prod_test/` tree (except this file and `README.md`) is `.gitignore`-d: it holds OAuth credentials, sqlite state, and locally-rebuilt binaries. **The actual configs live in this directory in the canonical repo checkout (`/root/multi-agent/multi-agent/tests/prod_test/` in the dev environment) and are NOT visible from worktrees.**

> **After each successful or failed run, update this file in-place** with whatever drifted: short_ids, port numbers, model/provider settings, config paths, new gotchas. The file is meant to stay current with the actual setup state.

## Topology

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

## Step 0 — Rebuild binaries from branch HEAD

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

## Step 1 — Pre-flight checks

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

## Step 2 — Start services (order matters)

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

## Step 3 — Trigger driver MCP

Two ways:

### 3a. Via `codex exec` (interactive-ish, exercises the real codex thread)

```bash
cd driver-codex-local
CODEX_HOME=$PWD/.codex codex exec --cd $PWD "<your prompt>"
```

The codex CLI forks `driver-agent serve-mcp` per the `[mcp_servers.driver]` block in `.codex/config.toml`. `driver-agent serve-mcp --config` MUST point at the SAME yaml the daemon reads (`driver-codex-local/config.yaml`) so `short_id` matches on both legs — without this, `loom_origin` parent links break.

### 3b. Direct stdio client (faster, deterministic, no codex token spend)

`/tmp/driver_mcp_e2e.py` (one-off helper, regenerate from this snippet):

- spawn `driver-agent serve-mcp --config .../driver-codex-local/config.yaml`
- wire format: **newline-delimited JSON-RPC** (NOT LSP Content-Length); write `json + "\n"`, split stdout on `\n`
- `initialize` → `tools/list` → `bind_thread{thread_id: <uuid>}` → `submit_task{target_display_name: "slave-codex-local", skill: "chat", prompt: "..."}` → `wait_task{task_id}`

Note `submit_task` only accepts `target_display_name`. Passing `target_short_id` silently falls back to `driver_defaults.target_display_name` (= slave A) — verified via the tool's inputSchema.

## Step 4 — Verify success

A successful `wait_task` response on a chat task:

```json
{
  "status": "completed",
  "session_id": "<UUID, the backend-native id from kind marker>",
  "bridge_session_id": "<cse_…, if agentserver minted one>",
  "output": "<slave's reply>",
  "final_output": "...",
  "is_final": true
}
```

Key invariant from #29: `session_id` is a backend-native UUID, **not** an agentserver bridge `cse_*`. If you see `cse_…` as `session_id`, the bridge/backend split regressed.

Both branches of `wait_task` / `get_task` (awaiting_user AND final/completed) MUST emit `session_id` + `bridge_session_id` (omitempty). Same goes for the awaiting_user branch returned through `waitDelegatedTask` (used by `resume_task` / `register_mcp` / `slave_file_tools`).

## Step 5 — Tear down

```bash
ps -ef | grep -E 'slave-agent|driver-agent|observer-server' | grep -v grep
kill <pids>
# locks are best-effort cleaned up on next start
```

## Re-registering a slave (OAuth device flow)

When `whoami` returns 403/401 for a slave, agentserver lost the sandbox and the token can't be revived. Re-register:

```bash
cd slave-codex-local       # or slave-codex-local-b
# Save the old creds first, then clear them so EnsureRegistered takes the device-flow path
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
# tail the log for the device-flow URL:
sleep 4
grep -A1 'Open this URL' logs/slave-agent.log
```

Open the printed URL in a browser, complete the consent prompt.

**Browser workspace pitfall:** the OAuth consent screen defaults to whatever workspace your account has open. Confirm the workspace is `96bd3120-…` ("嘿嘿") — NOT `6f55e9fe-…` ("prod-test/slave"). A wrong-workspace slave registers fine but is invisible to the driver. After approval, double-check:

```bash
grep workspace_id config.yaml
# must match driver-codex-local/config.yaml's workspace_id
```

If it doesn't match, kill the slave, clear creds again, and re-approve in the right workspace.

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

## Common failure modes

| Symptom | Likely cause | Fix |
| ------- | ------------ | --- |
| Slave exits seconds after start with `observer unauthorized: status 401` | slave-daemon-startup race regressed (daemon dialed observer before tunnel registered sandbox) | Verify `cmd/slave-agent/main.go::run()` waits on `tn.Ready()` before `daemon.Run` (PR #32 fix); rebuild binary |
| Slave runs but commander UI doesn't show it | `daemon.auto_start: false` in slave's config.yaml | Flip to `true`, restart |
| `wait_task` reports `failed` with `codex exit: exit status 1` and no output | Per-agent `.codex/config.toml` points at a model the proxy can't route | Align to working model + provider (see above) |
| `submit_task` succeeds but always lands on slave-A regardless of `target_short_id` | Used `target_short_id` arg (doesn't exist in inputSchema); driver silently falls back to `driver_defaults.target_display_name` | Use `target_display_name` |
| `whoami` returns 403 | sandbox state went `offline` or `forbidden` on agentserver | Re-register (no other fix; this is the issue #290 / sandbox-token-forbidden behavior) |
| Driver MCP `tools/call` reports `user cancelled MCP tool call` | Upstream model request was 429 / capacity / network-cancelled mid-tool-call; codex relabels it as user-cancel | Retry; if persistent, swap models per the provider section |
| `list_agents` only returns one slave even though both daemons are healthy | Second slave's PublishCard hasn't propagated yet (race with first invocation) or the second slave's last reconnect cycle re-published stale card | Wait 30-60s and retry; if persistent, restart the missing slave to trigger a fresh PublishCard |

## Automated runner (`run_e2e.sh`)

A unified script that automates Steps 0–5 plus the Playwright live e2e:

```bash
# Full run (rebuild binaries + driver MCP test + Playwright live e2e):
./tests/prod_test/run_e2e.sh

# Skip binary rebuild (when binaries are already current):
./tests/prod_test/run_e2e.sh --no-rebuild

# Run only driver MCP test:
./tests/prod_test/run_e2e.sh --skip-playwright

# Run only Playwright live e2e:
./tests/prod_test/run_e2e.sh --skip-mcp
```

Set `WORKTREE_ROOT` if running from a worktree (defaults to `../../` relative to the script).

The script uses `trap cleanup EXIT` to guarantee teardown even on failure. Pre-flight token checks treat HTTP 403 as a warning (sandbox may recover after tunnel reconnects), but HTTP 401 is fatal.

**`driver_mcp_e2e.py`** — Python 3 stdio client exercising `initialize → tools/list → bind_thread → submit_task → wait_task → exit`. Asserts the #29 invariant: `session_id` is a backend-native UUID, NOT `cse_*`.

### 2026-06-25 run results

| Test | Result | Key observations |
| --- | --- | --- |
| Driver MCP (submit_task + wait_task) | PASS | 23 tools, session_id=`019efef2-dcef-7790-...` (UUID v7), output=`E2E_OK` |
| Playwright fresh-id rebind | PASS | 5.0s test time, OAuth cookie cached for reuse |

**Short IDs after re-registration:**
- driver: `q6u3f1qr`
- slave-A: `pwqo9ro8`
- slave-B: `53et0rx4`

**Gotcha discovered:** Killing a slave process makes its sandbox go `forbidden` on agentserver immediately. Tokens cannot be reused after that — must re-register. When doing manual registration of multiple agents, start observer first and keep all agents running; don't kill one to register the next.

## When you finish a run

**Edit this file** to capture what you learned:

- Did short_ids, sandbox UUIDs, or workspace_ids change? Update the relevant rows above (they drift across re-registrations).
- Did you hit a NEW failure mode? Add it to the table.
- Did a fix in the codebase remove an old gotcha? Cross it off — don't leave stale warnings.
- Did the model provider change? Update the "Model provider" section.
- Did the driver MCP tool schema change (new tool, renamed argument)? Update Step 3b.

The file is one of the few persistent artifacts that survives session/context loss. Memory entries `[[prod-e2e-setup]]` and `[[slave-daemon-startup-race]]` summarize from this file, not the other way around.
