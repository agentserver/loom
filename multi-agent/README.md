# multi-agent

Two custom agents for the agentserver platform, sharing one Go module and a common set of internal packages.

| Binary | Role | Docs |
|---|---|---|
| `cmd/slave-agent` | Subordinate worker — accepts tasks and runs them via claude or MCP | [cmd/slave-agent/README.md](cmd/slave-agent/README.md) |
| `cmd/master-agent` | Orchestrator — uses claude as planner/router/reducer to delegate work to other agents | [cmd/master-agent/README.md](cmd/master-agent/README.md) |

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
