# salve-agent

Subordinate (worker) agent for agentserver. Receives tasks, executes via either claude (natural-language tasks) or MCP servers (`skill="mcp"` tasks), maintains a `CURRENT_STATE.md` capability journal.

See `docs/superpowers/specs/2026-04-27-salve-agent-design.md` for the full design.

## Build

From the module root (`multi-agent/`):

```bash
go build -o cmd/salve-agent/salve-agent ./cmd/salve-agent
```

## Configure

```bash
cp cmd/salve-agent/config.example.yaml cmd/salve-agent/config.yaml
# edit server.url, claude.bin, mcp_servers as needed
```

## Run

```bash
cd cmd/salve-agent && ./salve-agent config.yaml
```

The first run prints a device-flow URL; visit it in a browser to register the agent. Credentials are written back to `config.yaml`.

## Tests (run from module root)

```bash
go test ./...                                  # unit
go test -tags=contract ./tests/contract/...    # contract
go test -tags=smoke ./tests/smoke/...          # real claude + MCP (manual)
```

## End-to-end

From the module root:

```bash
AGENTSERVER_URL=https://agent.example.com ./cmd/salve-agent/scripts/e2e.sh
```
