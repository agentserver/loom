# slave-agent

Subordinate (worker) agent for agentserver. Receives tasks, executes via either claude (natural-language tasks) or MCP servers (`skill="mcp"` tasks), maintains a `CURRENT_STATE.md` capability journal and a persisted `CAPABILITIES.md` capability document.

See `docs/superpowers/specs/2026-04-27-slave-agent-design.md` for the full design.

## Build

From the module root (`multi-agent/`):

```bash
go build -o cmd/slave-agent/slave-agent ./cmd/slave-agent
```

## Configure

```bash
cp cmd/slave-agent/config.example.yaml cmd/slave-agent/config.yaml
# edit server.url, claude.bin, mcp_servers as needed
```

Optional control skills are advertised through `discovery.skills`:

```yaml
discovery:
  skills:
    - chat
    - mcp
    - bash
    - claude_permissions
```

`bash` enables deterministic shell execution through a native `slave-agent` executor. `claude_permissions` enables driver-side permission inspection and patching through the existing task channel. Permission patching is also native `slave-agent` Go code; it must not be implemented by asking the slave's Claude Code process to modify its own `.claude/settings.local.json`, because Claude Code may not yet have the required `Write`, `Edit`, or `Bash` permission.

This task-channel permission path is a compatibility bridge. When agentserver exposes a dedicated control channel for custom agents, permission management should move there.

## Run

```bash
cd cmd/slave-agent && ./slave-agent config.yaml
```

The first run prints a device-flow URL; visit it in a browser to register the agent. Credentials are written back to `config.yaml`.

## Capability Document

On startup the slave scans its local runtime, configured skills, configured MCP servers, generated `dynamic_mcp.yaml` servers, advertised resources, and current capability journal. It writes a hierarchical markdown document to:

```text
journal/CAPABILITIES.md
```

The document survives restarts because it is stored in the slave role directory. Startup refreshes it from the latest config and generated MCP registry. It is refreshed again when:

- a generated MCP server is built or updated by `build_mcp`;
- an MCP tool reports `capability_changed`;
- a Claude task reports a persistent capability change, such as a new skill or ordinary service.
- `claude_permissions` patches Claude Code project permissions.

The slave exposes the document through its tunneled web UI:

```text
/capabilities
```

The agent discovery card also includes `capability_doc_path: /capabilities` so drivers can discover the readable document location.

## Tests (run from module root)

```bash
go test ./...                                  # unit
go test -tags=contract ./tests/contract/...    # contract
go test -tags=smoke ./tests/smoke/...          # real claude + MCP (manual)
```

## End-to-end

From the module root:

```bash
AGENTSERVER_URL=https://agent.example.com ./cmd/slave-agent/scripts/e2e.sh
```
