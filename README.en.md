# multi-agent

**Language**: [简体中文](README.md) · [English](README.en.md)

A set of custom agents on top of the [agentserver](https://github.com/agentserver/agentserver) platform, sharing one Go module and a common set of internal packages. The core idea: **a user runs a single local driver inside Claude Code and uses it to command a fleet of self-hosted slaves that live on different machines with different capabilities.** The driver clarifies intent, inspects capabilities, and orchestrates tasks; each slave executes locally on its own node; all telemetry flows to a standalone observer for replay and debugging.

## Topology

```
                       ┌──────────────────────┐
   Claude Code / VS Code│   driver-agent       │  local, user-side, single instance
            ───────────▶│  (stdio MCP server)  │  ── workspace context + orchestration tools
                       └──────────┬───────────┘
                                  │  agentserver workspace
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
        ┌──────────┐        ┌──────────┐        ┌──────────┐
        │ slave-a  │        │ slave-b  │  …     │ slave-N  │   each node self-hosted
        │ skills:  │        │ skills:  │        │ skills:  │   different capabilities
        │ chat,mcp │        │ chat,bash│        │ register │   and resources
        │ bash,…   │        │ mcp,…    │        │ _mcp,…   │
        └────┬─────┘        └────┬─────┘        └────┬─────┘
             └────────── observer-server ────────────┘
                       (HTTP telemetry, deployed separately)
```

- **One driver, many slaves.** The driver sees every slave's advertised capabilities in the workspace. It can dispatch 1→1 to a single slave or build a 1→N DAG fan-out. Slaves have no implicit connection to each other — the driver is in control.
- **Everything is self-hostable.** driver / master / slave / observer / agentserver are plain Go binaries (or containers). They can all run on one laptop for local development, or you can scatter slaves across data centers and specialized hardware (GPU nodes, capture nodes, compression nodes…) as long as they can register into the same agentserver workspace.
- **Master as a compatibility path.** The master exposes `route` / `fanout` skills that use claude as the planner / router / reducer. Use it when direct driver-to-slave orchestration is not desired.
- **Observer is decoupled.** driver / master / slave push best-effort telemetry (task / subtask / artifact events) to the observer; observer failures never fail a task.

## Design highlights

- **Driver-first orchestration.** The user talks to the driver inside Claude Code (CLI or VS Code extension). The driver is both a stdio MCP server and a regular workspace agent, so it can pass the user's local file manifest through to master/slaves.
- **Discoverable and extensible capabilities.** On startup each slave writes its skills, MCP servers, resources, and runtime info into `journal/CAPABILITIES.md`. The driver consults `inspect_capabilities` to decide routing.
- **Build new capabilities on demand.** When no slave can fulfill a task, the driver can use `bash` to author and validate a Python MCP server on a slave, then call `register_mcp` to install it. Capabilities refresh and orchestration continues. This is the dynamic-mcp loop.

## Four binaries

| Binary | Role | Docs |
|---|---|---|
| `cmd/driver-agent` | Local driver — Claude Code's stdio MCP server, holds workspace context and orchestration tools | [cmd/driver-agent/README.md](cmd/driver-agent/README.md) |
| `cmd/master-agent` | Orchestrator — uses claude as planner / router / reducer to delegate work to other workspace agents | [cmd/master-agent/README.md](cmd/master-agent/README.md) |
| `cmd/slave-agent` | Worker — accepts tasks and runs them via claude or MCP, maintains a capability journal | [cmd/slave-agent/README.md](cmd/slave-agent/README.md) |
| `cmd/observer-server` | Standalone HTTP observer — stores and displays driver / master / slave telemetry | [cmd/observer-server/config.example.yaml](cmd/observer-server/config.example.yaml) |

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

Full schemas live in `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` and `../skills/multiagent`.

## Repository layout

```
multi-agent/
├── go.mod                       module github.com/yourorg/multi-agent
├── cmd/
│   ├── driver-agent/            stdio MCP + workspace agent
│   ├── master-agent/            orchestrator agent
│   ├── slave-agent/             worker agent
│   └── observer-server/         telemetry backend
├── internal/
│   ├── config, store, webui, tunnel, poller          shared by all
│   ├── executor, journal, dispatch, capability(doc)  slave-side
│   ├── orchestrator, orchestration, planner          master-side
│   ├── driver, contract, claudeperm, progress        driver-side
│   ├── buildspec, observer, observerclient,
│   │   observerstore, observerweb                    telemetry / build spec
├── pkg/transport                reusable transport helpers
├── examples/
│   ├── driver-first/            driver-first orchestration walk-through
│   ├── dynamic-mcp/             bash → register_mcp loop
│   ├── generic-driver/          generic driver assembling files
│   └── image-pipeline/          multi-slave image capture + compression pipeline
├── dev/
│   ├── agent-runtime/           runtime container image (numpy + default Claude Code perms)
│   ├── configs/                 example configs for driver/master/slave/observer
│   ├── compose.distributed.yaml docker compose stack
│   └── tmp/                     workspace helpers and e2e scratch dirs
├── testdata/                    fake-claude.sh / fake-planner.sh / fake-mcp-stdio
└── tests/
    ├── contract/                build tag: contract
    ├── runtime/                 runtime image + permission docs
    ├── smoke/                   build tag: smoke (manual, needs ANTHROPIC_API_KEY)
    └── claude_driver/           Claude Code driver test fixtures (matmul, etc.)
```

## Build and test

```bash
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
cd dev
ANTHROPIC_API_KEY=... docker compose -f compose.distributed.yaml up --build
```

`dev/agent-runtime/Dockerfile` ships `python3-numpy` and a default `/root/.claude/settings.json` allowlist so a slave's Claude Code does not stop for permission prompts on every call. When running containers by hand or bare-metal, follow [`tests/runtime/README.md`](tests/runtime/README.md) to drop a `.claude/settings.local.json` into each slave workdir.

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
  url: https://observer.example.com
  workspace_id: ws-local
  agent_id: driver-local
  token: driver-token
```

Views:

- `http://localhost:8090/drivers`
- `http://localhost:8090/masters`
- `http://localhost:8090/slaves`

## Skills and design docs

The multiagent skill used by Claude Code / Codex lives at the repo root in `../skills/multiagent/`, with reference docs covering driver tools, slave skills, the task contract, and orchestration patterns.

Design and plan documents live in `../docs/superpowers/`. The most relevant recent ones:

- `specs/2026-05-09-generic-driver-agent-design.md`
- `specs/2026-05-09-dynamic-mcp-design.md`
- `specs/2026-05-13-typed-buildmcp-progress-design.md`
- `specs/2026-05-14-distributed-driver-master-contract-design.md`
- `specs/2026-05-14-observer-artifact-relay-temporary-design.md`
- `plans/2026-05-19-bash-driven-mcp-registration.md`

The earlier slave / master design docs (`2026-04-27`, `2026-04-28`) are still useful, but their directory naming (`slave_agent/...`) predates the rename refactor and is not kept in sync automatically.
