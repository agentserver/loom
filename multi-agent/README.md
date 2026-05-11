# multi-agent

Two custom agents for the agentserver platform, sharing one Go module and a common set of internal packages.

| Binary | Role | Docs |
|---|---|---|
| `cmd/driver-agent` | Local driver — submits tasks and serves local workspace context | [cmd/driver-agent/README.md](cmd/driver-agent/README.md) |
| `cmd/slave-agent` | Subordinate worker — accepts tasks and runs them via claude or MCP | [cmd/slave-agent/README.md](cmd/slave-agent/README.md) |
| `cmd/master-agent` | Orchestrator — uses claude as planner/router/reducer to delegate work to other agents | [cmd/master-agent/README.md](cmd/master-agent/README.md) |
| `cmd/observer-server` | Standalone HTTP observer — stores and displays driver/master/slave telemetry | [cmd/observer-server/config.example.yaml](cmd/observer-server/config.example.yaml) |

## Layout

```
multi-agent/
├── go.mod                 (module github.com/yourorg/multi-agent)
├── cmd/
│   ├── slave-agent/       binary + per-binary docs/config/scripts
│   └── master-agent/      binary + per-binary docs/config/scripts
├── internal/
│   ├── config, store, webui, tunnel, poller   shared by both
│   ├── executor, journal, dispatch            slave-only (master does not import)
│   └── orchestrator, planner                  master-only (slave does not import)
├── testdata/              fake-claude.sh, fake-planner.sh, fake-mcp-stdio/
└── tests/
    ├── contract/          build tag: contract
    └── smoke/             build tag: smoke (manual; needs ANTHROPIC_API_KEY)
```

## Build everything

```bash
go build ./...
```

## Observer Server

The observer is a separate HTTP service. It does not call `agent.cs.ac.cn`; driver, master, and slave push best-effort telemetry to it with bearer tokens.

Build:

```bash
go build -o bin/observer-server ./cmd/observer-server
```

Run:

```bash
cp cmd/observer-server/config.example.yaml observer.yaml
./bin/observer-server --config observer.yaml
```

For a public deployment, run it on a host with persistent storage for `observer.db` and put HTTPS in front of the listen address. Then configure each agent with a matching `observer:` block:

```yaml
observer:
  enabled: true
  url: https://observer.example.com
  workspace_id: ws-local
  agent_id: driver-local
  token: driver-token
```

Use `master-local` / `master-token` for the master and `slave-local` / `slave-token` for the slave, matching `cmd/observer-server/config.example.yaml`. Agent task execution is not failed if observer delivery fails.

Views:

- `http://localhost:8090/drivers`
- `http://localhost:8090/masters`
- `http://localhost:8090/slaves`

## Test everything

```bash
go vet ./...
go test ./... -race -count=1
go test -tags=contract ./tests/contract/...
go test -tags=smoke ./tests/smoke/...        # manual only
```

## Design docs

Living at the repo root (one level up):

- `../docs/superpowers/specs/2026-04-27-slave-agent-design.md`
- `../docs/superpowers/plans/2026-04-28-slave-agent.md`
- `../docs/superpowers/specs/2026-04-28-master-agent-design.md`
- `../docs/superpowers/plans/2026-04-28-master-agent.md`

Note: spec/plan documents reference earlier path layouts (`slave_agent/...`); they are historical and not auto-updated by the rename refactor.
