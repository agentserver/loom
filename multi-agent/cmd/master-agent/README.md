# master-agent

Master (orchestrator) agent for agentserver. Pure scheduling: receives tasks, uses claude as planner / router / reducer to delegate work to other agents in the workspace via `SDK.DelegateTask`, then aggregates results.

Two skills:
- `route` — 1→1 LLM-routed delegation
- `fanout` — 1→N DAG with `{{nX.output}}` template substitution between nodes

See `docs/superpowers/specs/2026-04-28-master-agent-design.md` for the full design.

## Build

From the module root (`multi-agent/`):

```bash
go build -o cmd/master-agent/master-agent ./cmd/master-agent
```

## Configure

```bash
cp cmd/master-agent/config.example.yaml cmd/master-agent/config.yaml
# edit server.url, planner.bin, fanout policies
```

## Run

```bash
cd cmd/master-agent && ./master-agent config.yaml
```

The first run prints a device-flow URL; visit it in a browser to register. Credentials are written back to `config.yaml`.

## Tests (run from module root)

```bash
go test ./...                                  # unit (covers orchestrator, planner, dag)
go test -tags=contract ./tests/contract/...    # contract (incl. DelegateTask shape)
```

## End-to-end

From the module root, with at least one or two salve-agents already registered to the same workspace:

```bash
AGENTSERVER_URL=https://agent.example.com ./cmd/master-agent/scripts/e2e.sh
```
