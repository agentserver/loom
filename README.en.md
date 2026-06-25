# Loom

> Your loom: one local driver weaves capabilities from a fleet of self-hosted slaves into finished tasks — and spins the missing threads on the spot.

**Language**: [简体中文](README.md) · [English](README.en.md)

Loom is a set of custom agents on top of the [agentserver](https://github.com/agentserver/agentserver) platform, sharing one Go module and a common set of internal packages. The core idea: **a user runs a single local driver inside Claude Code, Codex CLI, or opencode and uses it to command a fleet of self-hosted slaves that live on different machines with different capabilities.** The driver clarifies intent, inspects capabilities, and orchestrates tasks; each slave executes locally on its own node; all telemetry flows to a standalone observer for replay and debugging. **Mixed-fleet deployment is fully supported** — each driver and slave independently picks its backend via `--agent claude|codex|opencode`; agents from all backends share one observer / workspace.

The most distinctive bit: **the fleet's capabilities are not fixed.** When none of the current slaves can satisfy a task, the driver has a slave author, run, and validate a Python MCP server on the fly, then registers it via `register_mcp`. By the next orchestration round, that new capability is already callable. The cluster therefore starts from a minimal skeleton and grows the tools it actually needs from real user tasks, instead of shipping a giant pre-baked integration catalog. The whole thing reads like weaving: warp and weft are existing capabilities from different slaves, the driver decides how they interlace into the finished task — and when a critical thread does not exist yet, it is spun on the spot on a slave and woven in.

> Naming note: the project was originally called `multi-agent` and is now **Loom**. The Go module path and the on-disk `multi-agent/` directory are kept as-is for now; the rename will be applied in a single later pass.

## Quick start

All three roles (observer → slave → driver) deploy via release-hosted
bootstrap scripts — **no repo clone required**, on any Linux host
(amd64 / arm64) or Termux/Android (aarch64). The default backend is
**Codex CLI**; pass `--agent claude` or `--agent opencode` to switch.

**Prerequisites**: `codex` CLI (`npm i -g @openai/codex`, Node ≥ 22) +
`codex login` or `export OPENAI_API_KEY=...`. Codex CLI can target any
OpenAI-compatible endpoint via `[model_providers.<name>]` in
`~/.codex/config.toml` — it does not require api.openai.com.

### 1. Observer (control plane)

```bash
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'   # both optional
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-observer.sh) \
  --name obs-prod --systemd
```

If `LOOM_API_KEY` is unset, a random key is generated and printed once
(observer's own bootstrap key).

### 2. Slave (executor)

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --systemd          # drop --systemd on Termux/Android
```

On first start the slave prints a device-code URL on stderr. Approve it
in a browser — the agentserver issues a proxy token that the slave uses
to authenticate with observer directly. **No `LOOM_API_KEY` needed.**

### 3. Driver (orchestrator)

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost
```

After bootstrap:

```bash
./driver-agent register --config ./config.yaml   # one-time device-code OAuth
cd <project-dir> && codex                         # first run prompts to trust the dir
```

Codex reads `[mcp_servers.driver]` from `.codex/config.toml` and
auto-launches `driver-agent serve-mcp`. The user can then call
`list_agents`, `submit_task`, etc. to orchestrate slaves.

### Switching backends

Pass `--agent claude` or `--agent opencode` to any bootstrap script.
Claude Code mode writes `.mcp.json` + `.claude/skills/`; opencode mode
writes `~/.config/opencode/opencode.json` + `AGENTS.md`. See
[`multi-agent/deploy/agent-backends.md`](multi-agent/deploy/agent-backends.md)
for the full comparison. Complete flag reference at
[`multi-agent/deploy/README.md`](multi-agent/deploy/README.md).

## Topology

```
                        ┌──────────────────────┐
   Claude Code / Codex  │   driver-agent       │  local, single instance (the weaver)
        / opencode ────▶│  (stdio MCP server)  │  ── workspace context + orchestration tools
                       └──────────┬───────────┘
                                  │  agentserver workspace
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
        ┌──────────┐        ┌──────────┐        ┌──────────┐
        │ slave-a  │        │ slave-b  │  …     │ slave-N  │   each node self-hosted
        │ skills:  │        │ skills:  │        │ skills:  │   different capabilities
        │ chat,mcp │◀──┐    │ chat,bash│        │ register │   and resources
        │ bash,…   │   │    │ mcp,…    │        │ _mcp,…   │
        └────┬─────┘   │    └────┬─────┘        └────┬─────┘
             │   new MCP server authored via bash + installed via register_mcp
             │             (driver-triggered dynamic-mcp loop)
             └────────── observer-server ────────────┘
                       (HTTP telemetry, deployed separately)
```

- **One driver, many slaves.** The driver sees every slave's advertised capabilities in the workspace. It can dispatch 1→1 to a single slave or build a 1→N DAG fan-out. Slaves have no implicit connection to each other — the driver is in control.
- **Everything is self-hostable.** driver / slave / observer / agentserver are plain Go binaries (or containers). They can all run on one laptop for local development, or you can scatter slaves across data centers and specialized hardware (GPU nodes, capture nodes, compression nodes…) as long as they can register into the same agentserver workspace.
- **Observer is decoupled.** driver / slave push best-effort telemetry (task / subtask / artifact events) to the observer; observer failures never fail a task.

## Build capabilities on demand (the dynamic-mcp loop)

Loom's headline feature, called out on its own:

1. The driver calls `inspect_capabilities` and finds no slave advertises the tool it needs.
2. The driver uses `bash` to have a target slave author a Python MCP server and pass smoke / acceptance tests.
3. Once it passes, the driver calls `register_mcp`; the slave persists it to `dynamic_mcp.yaml` and refreshes `CAPABILITIES.md`.
4. The next `dry_run_contract` / `submit_contract_task` schedules this new capability as a normal `skill:"mcp"` node.

Net effect: **the cluster starts from a minimal skeleton (just claude + bash) and grows the tools it actually needs from real user tasks**, instead of pre-installing a sprawling integration catalog up front. See `examples/dynamic-mcp/` for the full end-to-end walk-through.

## Other design highlights

- **Driver-first orchestration.** The user talks to the driver inside Claude Code, Codex, or opencode. The driver is both a stdio MCP server and a regular workspace agent, so it can pass the user's local file manifest through to slaves.
- **Discoverable capabilities.** On startup each slave writes its skills, MCP servers, resources, and runtime info into `journal/CAPABILITIES.md`. The driver consults `inspect_capabilities` to decide routing — this is also what makes "capabilities on demand" loop-closable.

## Four binaries

| Binary | Role | Docs |
|---|---|---|
| `cmd/driver-agent` | Local driver — Claude Code / Codex / opencode stdio MCP server, holds workspace context and orchestration tools | [cmd/driver-agent/README.md](multi-agent/cmd/driver-agent/README.md) |
| `cmd/slave-agent` | Worker — accepts tasks and runs them via claude / codex / MCP, maintains a capability journal | [cmd/slave-agent/README.md](multi-agent/cmd/slave-agent/README.md) |
| `cmd/observer-server` | Standalone HTTP observer — stores driver / slave telemetry; also hosts the userspace package registry | [cmd/observer-server/config.example.yaml](multi-agent/cmd/observer-server/config.example.yaml) |
| `cmd/mcp-userspace` | CLI for packaging a validated MCP server / skill and pushing it to observer userspace, then `install`-ing on another host | [cmd/mcp-userspace/](multi-agent/cmd/mcp-userspace/) |

### Core slave skills

- `chat` — natural-language tasks executed by the slave's embedded claude (or codex)
- `bash` — deterministic shell tasks executed by the slave's native Go executor
- `file` — stateless `read` / `write` / `stat` on slave-local paths
- `register_mcp` — install an MCP server source file that has already been authored and smoke-tested on the slave
- `permissions` — read / patch the slave's Claude Code or Codex permissions via native code

Inside `chat`, the slave-side claude/codex can call the **humanloop**
tools `ask_user` / `request_permission` to pause mid-turn — the driver
hands the question to the user sitting in Claude Code / VS Code, then
resumes the chat with the answer. See `internal/humanloop/` and
`docs/superpowers/specs/2026-05-26-humanloop-resumable-chat-design.md`.

### Driver MCP tools

The driver's tool namespace appears under `driver/` inside Claude Code. The frequently used ones:

- `inspect_capabilities` / `list_agents`
- `draft_task_contract` / `dry_run_contract` / `submit_contract_task`
- `get_task` / `wait_task` / `tail_subtasks` / `cancel_task`
- `run_slave_bash` / `register_slave_mcp` / `unregister_slave_mcp`
- `get_slave_claude_permissions` / `update_slave_claude_permissions`

Full schemas live in `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` and `skills/multiagent`.

### Python client: loom-py

If you'd rather drive the driver from a Python script or notebook
instead of opening Claude Code, use `multi-agent/python/` (PyPI name
`loom`). It wraps the driver MCP surface as a fluent workflow API —
chat / wait / `expect_or_ask` / `find_slave` / file-IO placeholders:

```python
import loom

with loom.workflow(goal="say HELLO") as wf:
    res = wf.chat("Reply with HELLO and stop.",
                  target="slave-local-prod").wait()
print(res.output)
```

Zero runtime deps; the only requirement is `driver-agent` on PATH. See
[`multi-agent/python/README.md`](multi-agent/python/README.md) and
`docs/superpowers/specs/2026-05-27-loom-python-library-design.md`.

### Userspace: reuse what you built on another host

When a driver has had a slave author a new MCP server (or you wrote a
fresh skill), you can package it with `mcp-userspace` and push to your
personal space on observer — then `install` it on another host or into
another workspace:

```bash
mcp-userspace login --url http://observer:8090 --token $TOKEN
mcp-userspace push  --slug wedding_almanac --bump-patch ./generated_mcp/wedding_almanac
mcp-userspace install --as mcp --workspace ws-work --overwrite wedding_almanac@1.0.0
```

Server-side bits live in `internal/userspace` + `internal/mcpmarket`;
the driver-side `userspace-publish` skill fires when the user says "save
this to my space".

## Repository layout

```
.
├── README.md / README.en.md          top-level docs (you're reading them)
├── skills/                           Claude Code / Codex side skills
│   ├── multiagent/                   driver tools / slave skills / task contract / orchestration
│   ├── scaffold-mcp-server/          spec.json → stdio JSON-RPC skeleton
│   ├── mcp-acceptance/               semantic acceptance gate run before register_mcp
│   └── userspace-publish/            push validated MCP / skill to personal userspace
├── docs/
│   ├── superpowers/                  design specs and execution plans
│   └── intro/                        project intro HTML site (zero-dep SVG diagrams)
└── multi-agent/                      Go module (project codename Loom; path not renamed yet)
    ├── go.mod                        module github.com/yourorg/multi-agent
    ├── cmd/
    │   ├── driver-agent/             stdio MCP + workspace agent
    │   ├── slave-agent/              worker agent
    │   ├── observer-server/          telemetry backend + userspace package registry
    │   └── mcp-userspace/            push / pull / install CLI for userspace
    ├── internal/
    │   ├── config, store, webui, tunnel, poller          shared by all
    │   ├── executor, journal, dispatch, capability(doc)  slave-side
    │   ├── driver, contract, claudeperm, progress        driver-side
    │   ├── humanloop                                     in-chat ask_user / request_permission
    │   ├── userspace, mcpmarket                          personal package registry on observer
    │   └── buildspec, observer, observerclient,
    │       observerstore, observerweb                    telemetry / build spec
    ├── pkg/transport                 reusable transport helpers
    ├── python/                       loom-py: Python fluent workflow client for driver
    ├── deploy/                       production deploy templates + bootstrap scripts
    ├── examples/
    │   ├── driver-first/             driver-first orchestration walk-through
    │   ├── dynamic-mcp/              bash → register_mcp loop
    │   ├── generic-driver/           generic driver assembling files
    │   └── image-pipeline/           multi-slave image capture + compression pipeline
    ├── dev/
    │   ├── agent-runtime/            runtime container image (numpy + default Claude Code perms)
    │   ├── configs/                  example configs for driver/slave/observer
    │   ├── compose.distributed.yaml  docker compose stack
    │   └── tmp/                      workspace helpers and e2e scratch dirs
    ├── testdata/                     fake-claude.sh / fake-planner.sh / fake-mcp-stdio
    └── tests/
        ├── contract/                 build tag: contract
        ├── runtime/                  runtime image + permission docs
        ├── smoke/                    build tag: smoke (manual, needs ANTHROPIC_API_KEY)
        ├── claude_driver/            Claude Code driver test fixtures (matmul, etc.)
        └── prod_test/                internal prod-staging bundle (gitignored)
```

## Build and test

All Go commands run from inside `multi-agent/`:

```bash
cd multi-agent
go build ./...
go vet ./...
go test ./... -race -count=1
go test -tags=contract ./tests/contract/...
go test -tags=smoke ./tests/smoke/...        # manual
```

Building a single binary:

```bash
go build -o cmd/driver-agent/driver-agent       ./cmd/driver-agent
go build -o cmd/slave-agent/slave-agent         ./cmd/slave-agent
go build -o bin/observer-server                  ./cmd/observer-server
go build -o bin/mcp-userspace                    ./cmd/mcp-userspace
```

## Self-host a stack

The fastest path is docker compose (postgres + agentserver + a pair of slaves):

```bash
cd multi-agent/dev
ANTHROPIC_API_KEY=... docker compose -f compose.distributed.yaml up --build
```

`dev/agent-runtime/Dockerfile` ships `python3-numpy` and a default `/root/.claude/settings.json` allowlist so a slave's Claude Code does not stop for permission prompts on every call. When running containers by hand or bare-metal, follow [`multi-agent/tests/runtime/README.md`](multi-agent/tests/runtime/README.md) to drop a `.claude/settings.local.json` into each slave workdir.

To self-host slaves on separate nodes:

1. On each target machine, `go build ./cmd/slave-agent` or pull the `dev/agent-runtime` image.
2. Copy `dev/configs/slave-*.example.yaml` and edit `server.url`, `discovery.display_name`, `discovery.skills`, plus any `resources` / `mcp_servers` you want to expose.
3. The first start prints a device-flow URL; complete the browser consent and credentials are written back to the yaml. The driver's `inspect_capabilities` will pick up the new slave on its next call.

The driver always runs on the user's local machine (it has to attach to Claude Code / Codex / opencode). Observer can sit next to the driver or live on its own self-hosted node.

## Observer

The observer is a standalone HTTP service independent of agentserver. driver / slave push telemetry with bearer tokens; delivery failures never fail a task.

```bash
go build -o bin/observer-server ./cmd/observer-server
cp cmd/observer-server/config.example.yaml observer.yaml
./bin/observer-server --config observer.yaml
```

Each agent's `observer:` block must match:

```yaml
observer:
  enabled: true
  url: http://observer.local:8090
  workspace_id: ws-personal          # required; observer lazy-creates
  workspace_name: "Personal Lab"      # optional; recorded on first creation
  agent_id: driver-local
  api_key: REPLACE_ME
  token_state_path: /var/lib/driver/observer-token.json
```

Views:

- `http://localhost:8090/drivers`
- `http://localhost:8090/slaves`

## Skills and design docs

The repo's `skills/` directory ships four skills, loaded by the
driver-side Claude Code / Codex:

- `multiagent` — the main skill, with reference docs for driver tools, slave skills, the task contract, and orchestration patterns
- `scaffold-mcp-server` — generates a stdio JSON-RPC skeleton from `spec.json` (re-runs preserve hand-written handlers)
- `mcp-acceptance` — the semantic acceptance gate that must pass before `register_mcp` is allowed
- `userspace-publish` — push a validated MCP / skill to your personal userspace on observer

`docs/intro/` is the project-intro HTML site (layered-stack / cycle /
related-work SVG diagrams, no JS), openable straight from `index.html`.

Design and plan documents live in `docs/superpowers/`. The most relevant recent ones:

- `specs/2026-05-09-generic-driver-agent-design.md`
- `specs/2026-05-09-dynamic-mcp-design.md`
- `specs/2026-05-13-typed-buildmcp-progress-design.md`
- `specs/2026-05-14-distributed-driver-master-contract-design.md`
- `specs/2026-05-14-observer-artifact-relay-temporary-design.md`
- `specs/2026-05-26-humanloop-resumable-chat-design.md`
- `specs/2026-05-27-loom-python-library-design.md`
- `plans/2026-05-19-bash-driven-mcp-registration.md`

The earlier design docs (`2026-04-27`, `2026-04-28`) are still useful, but their directory naming (`slave_agent/...`) predates the rename refactor and is not kept in sync automatically.
