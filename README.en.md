# Loom

> Your loom: one local driver weaves capabilities from a fleet of self-hosted slaves into finished tasks — and spins the missing threads on the spot.

**Language**: [简体中文](README.md) · [English](README.en.md)

Loom is a set of custom agents on top of the [agentserver](https://github.com/agentserver/agentserver) platform, sharing one Go module and a common set of internal packages. The core idea: **a user runs a single local driver inside Claude Code or Codex CLI and uses it to command a fleet of self-hosted slaves that live on different machines with different capabilities.** The driver clarifies intent, inspects capabilities, and orchestrates tasks; each slave executes locally on its own node; all telemetry flows to a standalone observer for replay and debugging. **Mixed-fleet deployment is fully supported** — each driver and slave independently picks its backend via `--agent claude|codex`; agents from both backends share one observer / workspace.

The most distinctive bit: **the fleet's capabilities are not fixed.** When none of the current slaves can satisfy a task, the driver has a slave author, run, and validate a Python MCP server on the fly, then registers it via `register_mcp`. By the next orchestration round, that new capability is already callable. The cluster therefore starts from a minimal skeleton and grows the tools it actually needs from real user tasks, instead of shipping a giant pre-baked integration catalog. The whole thing reads like weaving: warp and weft are existing capabilities from different slaves, the driver decides how they interlace into the finished task — and when a critical thread does not exist yet, it is spun on the spot on a slave and woven in.

> Naming note: the project was originally called `multi-agent` and is now **Loom**. The Go module path and the on-disk `multi-agent/` directory are kept as-is for now; the rename will be applied in a single later pass.

## One-line deploy

All three roles bring up with a single `bash <(curl -fsSL ...)` against a
release-hosted bootstrap script — **no repo clone required**, on any Linux
host (amd64 / arm64) or Termux/Android (aarch64). Replace
`OBSERVER_HOST` / `WS_ID` / `YOUR_API_KEY` / agent names with your own
values. Append `--systemd` on slave/observer for a managed unit (needs
sudo; drop on Termux).

```bash
# observer (control plane) — random api-key is generated and printed if LOOM_API_KEY is unset
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-observer.sh) \
  --name obs-prod --systemd

# driver — Claude Code variant (default)
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
  --name driver-myhost

# driver — Codex variant (writes .codex/config.toml + AGENTS.md instead of .mcp.json)
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
  --name driver-myhost --agent codex

# slave — Claude Code variant (executor; drop --systemd on Termux/Android)
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave.sh) \
  --name slave-myhost --systemd

# slave — Codex variant (chat skill drives codex exec --json; mix freely with Claude Code slaves)
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave.sh) \
  --name slave-myhost --agent codex --systemd
```

After bootstrap, run the one-time `./driver-agent register --config ./config.yaml`
device-code OAuth on the driver host. For a Codex driver, also run `codex login`
(or `export OPENAI_API_KEY=...`) and invoke `codex` once in the project dir to
add it to the trust list. On the slave, the first start prints a device-code URL
on stderr — approve it in a browser and the slave writes the issued sandbox /
tunnel credentials back into `config.yaml`, then registers with observer.

Codex CLI accepts any OpenAI-compatible endpoint via `[model_providers.<name>]`
in `~/.codex/config.toml` (symmetric to pointing Claude Code at
`ANTHROPIC_BASE_URL=...`) — useful for self-hosted gateways. See
[`multi-agent/deploy/agent-backends.md`](multi-agent/deploy/agent-backends.md)
for the example block, container-deployment caveats (project-scoped
`.codex/config.toml` needs a trust prompt that can't fire in containers; mount
the global config instead), and `permissions`-skill JSON examples for both
backends. Full flag reference and the non-bootstrap install path live at
[`multi-agent/deploy/README.md`](multi-agent/deploy/README.md).

## Topology

```
                       ┌──────────────────────┐
   Claude Code / VS Code│   driver-agent       │  local, single instance (the weaver)
            ───────────▶│  (stdio MCP server)  │  ── workspace context + orchestration tools
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
- **Everything is self-hostable.** driver / master / slave / observer / agentserver are plain Go binaries (or containers). They can all run on one laptop for local development, or you can scatter slaves across data centers and specialized hardware (GPU nodes, capture nodes, compression nodes…) as long as they can register into the same agentserver workspace.
- **Master as a compatibility path.** The master exposes `route` / `fanout` skills that use claude as the planner / router / reducer. Use it when direct driver-to-slave orchestration is not desired.
- **Observer is decoupled.** driver / master / slave push best-effort telemetry (task / subtask / artifact events) to the observer; observer failures never fail a task.

## Build capabilities on demand (the dynamic-mcp loop)

Loom's headline feature, called out on its own:

1. The driver calls `inspect_capabilities` and finds no slave advertises the tool it needs.
2. The driver uses `bash` to have a target slave author a Python MCP server and pass smoke / acceptance tests.
3. Once it passes, the driver calls `register_mcp`; the slave persists it to `dynamic_mcp.yaml` and refreshes `CAPABILITIES.md`.
4. The next `dry_run_contract` / `submit_contract_task` schedules this new capability as a normal `skill:"mcp"` node.

Net effect: **the cluster starts from a minimal skeleton (just claude + bash) and grows the tools it actually needs from real user tasks**, instead of pre-installing a sprawling integration catalog up front. See `examples/dynamic-mcp/` for the full end-to-end walk-through.

## Other design highlights

- **Driver-first orchestration.** The user talks to the driver inside Claude Code (CLI or VS Code extension). The driver is both a stdio MCP server and a regular workspace agent, so it can pass the user's local file manifest through to master/slaves.
- **Discoverable capabilities.** On startup each slave writes its skills, MCP servers, resources, and runtime info into `journal/CAPABILITIES.md`. The driver consults `inspect_capabilities` to decide routing — this is also what makes "capabilities on demand" loop-closable.

## Four binaries

| Binary | Role | Docs |
|---|---|---|
| `cmd/driver-agent` | Local driver — Claude Code's stdio MCP server, holds workspace context and orchestration tools | [cmd/driver-agent/README.md](multi-agent/cmd/driver-agent/README.md) |
| `cmd/master-agent` | Orchestrator — uses claude as planner / router / reducer to delegate work to other workspace agents | [cmd/master-agent/README.md](multi-agent/cmd/master-agent/README.md) |
| `cmd/slave-agent` | Worker — accepts tasks and runs them via claude or MCP, maintains a capability journal | [cmd/slave-agent/README.md](multi-agent/cmd/slave-agent/README.md) |
| `cmd/observer-server` | Standalone HTTP observer — stores and displays driver / master / slave telemetry | [cmd/observer-server/config.example.yaml](multi-agent/cmd/observer-server/config.example.yaml) |

### Core slave skills

- `chat` — natural-language tasks executed by the slave's embedded claude
- `mcp` — JSON `{server, tool, args}` call dispatched to a configured MCP server
- `register_mcp` — install an MCP server source file that has already been authored and smoke-tested on the slave
- `bash` — deterministic shell tasks executed by the slave's native Go executor
- `claude_permissions` — read / patch the slave's Claude Code project permissions via the task channel (a transitional bridge)

### Driver MCP tools

The driver's tool namespace appears under `driver/` inside Claude Code. The frequently used ones:

- `inspect_capabilities` / `list_agents`
- `draft_task_contract` / `dry_run_contract` / `submit_contract_task`
- `get_task` / `wait_task` / `tail_subtasks` / `cancel_task`
- `run_slave_bash` / `register_slave_mcp`
- `get_slave_claude_permissions` / `update_slave_claude_permissions`

Full schemas live in `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` and `skills/multiagent`.

## Repository layout

```
.
├── README.md / README.en.md          top-level docs (you're reading them)
├── skills/multiagent/                Claude Code / Codex multiagent skill
├── docs/superpowers/                 design specs and execution plans
└── multi-agent/                      Go module (project codename Loom; path not renamed yet)
    ├── go.mod                        module github.com/yourorg/multi-agent
    ├── cmd/
    │   ├── driver-agent/             stdio MCP + workspace agent
    │   ├── master-agent/             orchestrator agent
    │   ├── slave-agent/              worker agent
    │   └── observer-server/          telemetry backend
    ├── internal/
    │   ├── config, store, webui, tunnel, poller          shared by all
    │   ├── executor, journal, dispatch, capability(doc)  slave-side
    │   ├── orchestrator, orchestration, planner          master-side
    │   ├── driver, contract, claudeperm, progress        driver-side
    │   └── buildspec, observer, observerclient,
    │       observerstore, observerweb                    telemetry / build spec
    ├── pkg/transport                 reusable transport helpers
    ├── examples/
    │   ├── driver-first/             driver-first orchestration walk-through
    │   ├── dynamic-mcp/              bash → register_mcp loop
    │   ├── generic-driver/           generic driver assembling files
    │   └── image-pipeline/           multi-slave image capture + compression pipeline
    ├── dev/
    │   ├── agent-runtime/            runtime container image (numpy + default Claude Code perms)
    │   ├── configs/                  example configs for driver/master/slave/observer
    │   ├── compose.distributed.yaml  docker compose stack
    │   └── tmp/                      workspace helpers and e2e scratch dirs
    ├── testdata/                     fake-claude.sh / fake-planner.sh / fake-mcp-stdio
    └── tests/
        ├── contract/                 build tag: contract
        ├── runtime/                  runtime image + permission docs
        ├── smoke/                    build tag: smoke (manual, needs ANTHROPIC_API_KEY)
        └── claude_driver/            Claude Code driver test fixtures (matmul, etc.)
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
go build -o cmd/master-agent/master-agent       ./cmd/master-agent
go build -o cmd/slave-agent/slave-agent         ./cmd/slave-agent
go build -o bin/observer-server                  ./cmd/observer-server
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

The driver always runs on the user's local machine (it has to attach to Claude Code). The master and observer can sit next to the driver or live on their own self-hosted nodes.

## Observer

The observer is a standalone HTTP service independent of agentserver. driver / master / slave push telemetry with bearer tokens; delivery failures never fail a task.

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
- `http://localhost:8090/masters`
- `http://localhost:8090/slaves`

## Skills and design docs

The multiagent skill used by Claude Code / Codex lives at the repo root in `skills/multiagent/`, with reference docs covering driver tools, slave skills, the task contract, and orchestration patterns.

Design and plan documents live in `docs/superpowers/`. The most relevant recent ones:

- `specs/2026-05-09-generic-driver-agent-design.md`
- `specs/2026-05-09-dynamic-mcp-design.md`
- `specs/2026-05-13-typed-buildmcp-progress-design.md`
- `specs/2026-05-14-distributed-driver-master-contract-design.md`
- `specs/2026-05-14-observer-artifact-relay-temporary-design.md`
- `plans/2026-05-19-bash-driven-mcp-registration.md`

The earlier slave / master design docs (`2026-04-27`, `2026-04-28`) are still useful, but their directory naming (`slave_agent/...`) predates the rename refactor and is not kept in sync automatically.
