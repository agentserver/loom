# Slave Skills

Slaves advertise skills through their discovery card. The driver and planner should dispatch by `skill`:

- `chat`: natural-language Claude Code task.
- `mcp`: direct call to a configured or generated MCP server.
- `build_mcp`: generate and register a reusable MCP server.
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

## `build_mcp`

Prompt is a build spec JSON object. Planner nodes may use `build_spec`; driver fanout canonicalizes it into the prompt before dispatch.

Minimal spec:

```json
{
  "name": "row_stats",
  "description": "Compute statistics for rows",
  "tools": [
    {
      "name": "summarize_rows",
      "description": "Summarize numeric row values",
      "args_schema": {
        "type": "object",
        "properties": {"rows": {"type": "array"}},
        "required": ["rows"],
        "additionalProperties": false
      },
      "result_description": "JSON summary statistics"
    }
  ],
  "hints": "Use only the standard library unless allowed packages include more.",
  "allowed_packages": [],
  "compose_servers": [],
  "version": 1,
  "iteration": 1,
  "max_iterations": 3
}
```

Validation:

- `name` matches `[a-z][a-z0-9_]{0,31}`.
- `tools` has at least one entry.
- Each tool needs `name`, `description`, valid JSON `args_schema`, and `result_description`.
- `version >= 2` requires `prior_path`.
- Generated Python is syntax checked, imports are checked against `allowed_packages`, then smoke-launched.
- Successful builds persist under the slave role directory and update `dynamic_mcp.yaml`, the agent card, and capability docs.

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
