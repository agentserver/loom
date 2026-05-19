# Slave Skills

Slaves advertise skills through their discovery card. The driver and planner should dispatch by `skill`:

- `chat`: natural-language Claude Code task.
- `mcp`: direct call to a configured or generated MCP server.
- `register_mcp`: register a pre-built MCP server file (paired with bash to generate and validate).
- `bash`: run explicit Bash through native slave-agent code.
- `claude_permissions`: read or patch Claude Code permissions through native slave-agent code.

## `chat`

Prompt is natural language. Use for work that needs Claude Code reasoning or file editing inside the slave workspace. Do not ask chat to call MCP when a direct `skill:"mcp"` JSON call is available.

## `mcp`

Prompt must be JSON:

```json
{
  "server": "server_name",
  "tool": "tool_name",
  "args": {"key": "value"}
}
```

Rules:

- `server` must be registered on the target slave.
- `tool` should come from the target's `mcp_tools`.
- `args` must match the tool `input_schema`.
- Driver fanout validates server/tool/schema before dispatch.

## `register_mcp`

Prompt is JSON:

```json
{
  "spec": {
    "name": "row_stats",
    "description": "Compute statistics for rows",
    "version": 1,
    "tools": [
      {
        "name": "summarize_rows",
        "description": "Summarize numeric row values",
        "args_schema": {"type": "object", "properties": {"rows": {"type":"array"}}, "required":["rows"], "additionalProperties": false},
        "result_description": "JSON summary statistics"
      }
    ],
    "allowed_packages": []
  },
  "source_path": "generated_mcp/row_stats/v1.py"
}
```

Validation:

- `spec.name` matches `[a-z][a-z0-9_]{0,31}`.
- `spec.tools` has at least one entry; each tool needs name, description, valid JSON `args_schema`, and `result_description`.
- `source_path` is relative to the slave workdir and must not escape it.
- Python source is syntax-checked, imports are checked against `spec.allowed_packages`, then smoke-launched once (`tools/list`).
- On success the file is registered in the MCP runtime and persisted to `dynamic_mcp.yaml`; the slave's capability card is republished.

## Bash → register_mcp workflow

The driver Claude should construct MCP servers by combining `bash` and `register_mcp`:

1. Use `bash` (or `run_slave_bash`) to write `generated_mcp/<name>/v1.py` on the slave, optionally importing or shelling out to existing services already on the slave.
2. Inside the same bash task, run real acceptance cases against the file (e.g. `python3 generated_mcp/<name>/v1.py < tests.jsonl`) and iterate on the source until outputs match expectations.
3. Submit `register_mcp` (or `register_slave_mcp` from the driver) with the spec and `source_path`. Registration only does structural checks + `tools/list` smoke; acceptance is the previous step's job.

The structured spec exists so capability publishing has clean metadata; it is not a generation contract.

## `bash`

Prompt is JSON:

```json
{
  "script": "set -euo pipefail\npython3 script.py",
  "timeout_sec": 60,
  "env": {"KEY": "value"}
}
```

Use only for explicit commands the driver has decided are appropriate. If Claude Code permissions block a command, use permission tools first.

## `claude_permissions`

Implemented by slave-agent native Go code, not by slave Claude Code.

Read prompt:

```json
{"op": "get"}
```

Patch prompt:

```json
{
  "op": "patch",
  "allow_presets": ["python", "curl", "file_write"],
  "allow_add": ["Bash(pip install *)"],
  "allow_remove": [],
  "deny_add": [],
  "deny_remove": []
}
```

This is a compatibility bridge over the task channel. Future work should use a dedicated agentserver peer/control channel.

## Capability Document

On startup the slave scans runtime, skills, MCP servers, generated MCP, resources, and capability journal, then writes `journal/CAPABILITIES.md`. It is exposed at `/capabilities` through the tunneled web UI and advertised as `capability_doc_path`.
