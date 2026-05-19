# Driver MCP Tools

The driver runs as a stdio MCP server inside Claude Code and also registers as an agentserver workspace agent. Prefer contract tools for business tasks; use direct helpers for explicit low-level control.

## Capability Discovery

### `list_agents`

Input:

```json
{}
```

Returns visible agents excluding driver self:

```json
{
  "agents": [
    {
      "agent_id": "sandbox-id",
      "display_name": "slave-a",
      "short_id": "abc123",
      "skills": ["chat", "mcp", "build_mcp", "bash", "claude_permissions"],
      "tools": ["legacy_tool_name"],
      "mcp_tools": [{"server": "srv", "name": "tool", "input_schema": {}}],
      "resources": {"tags": ["python3"]}
    }
  ]
}
```

### `inspect_capabilities`

Input:

```json
{"save_snapshot": true}
```

Returns a resource snapshot plus visible agents, slaves, flattened `mcp_tools`, and `warnings`. Use it before drafting contracts.

## Contract Tools

### `draft_task_contract`

Input:

```json
{
  "goal": "Compare two matrix multiplication implementations on two slaves",
  "business_context": "Need reproducible evidence",
  "success_criteria": ["Both scripts run", "Results match"],
  "write_targets": [{"type": "artifact", "kind": "document", "name": "report.md"}],
  "required_skills": ["bash"],
  "required_tools": [],
  "resources": {"tags": ["python3"]},
  "routing": "direct_first",
  "allow_build_mcp": false,
  "max_concurrency": 2,
  "allowed_targets": []
}
```

Returns `contract` and `clarification_questions`.

### `dry_run_contract`

Input:

```json
{"contract": {"version": 1}}
```

Returns:

```json
{
  "runnable": true,
  "requires_build_mcp": false,
  "recommended_route": "driver_fanout",
  "recommended_target_id": "",
  "recommended_target_display_name": "",
  "recommended_skill": "fanout",
  "satisfied_tools": [],
  "missing_tools": [],
  "missing_skills": [],
  "candidate_build_targets": [],
  "reasons": ["driver can orchestrate with currently advertised tools"]
}
```

Never submits work and never creates MCP servers.

### `submit_contract_task`

Input:

```json
{
  "contract": {"version": 1},
  "prompt": "Optional execution detail; defaults to contract.intent.goal",
  "target_display_name": "optional explicit target",
  "skill": "optional skill override",
  "timeout_sec": 1200
}
```

Behavior:

- Encodes the contract envelope and saves resource/contract snapshots through observer when configured.
- If `recommended_route` is `driver_fanout` and no target override is supplied, the driver runs its own DAG runner.
- Otherwise delegates to a direct slave according to target and contract policy.

## Direct Task Tools

### `submit_task`

Use for simple ad hoc direct tasks. Prefer `submit_contract_task` for coordinated work. Input:

```json
{
  "prompt": "Do the task",
  "read_paths": ["/absolute/local/input.csv"],
  "write_paths": [{"path": "/absolute/local/out.md", "overwrite": true}],
  "target_display_name": "slave-a",
  "skill": "fanout",
  "timeout_sec": 1200
}
```

`read_paths` and `write_paths` are driver-local paths. The driver converts them into file manifests and observer or peer-proxy URLs for remote agents.

### `get_task`

Input:

```json
{"task_id": "task-id", "include_subtasks": true}
```

Returns status, output, latest progress, final output, and whether the task is final.

### `wait_task`

Input:

```json
{"task_id": "task-id", "poll_interval_sec": 3, "timeout_sec": 1200}
```

Blocks until terminal status and returns `written_files` for PUT-back outputs.

### `tail_subtasks`

Input:

```json
{"task_id": "task-id", "since_seq": 0, "max_wait_sec": 30}
```

Long-polls subtask rows and returns `{cursor, events}` for a delegated task.

### `cancel_task`

Input:

```json
{"task_id": "task-id"}
```

Currently a v1 stub: returns current status and notes that cancel is not implemented.

## Slave Control Helpers

### `run_slave_bash`

Input:

```json
{
  "target_agent_id": "optional",
  "target_display_name": "slave-a",
  "script": "python3 - <<'PY'\nprint('ok')\nPY",
  "env": {"KEY": "value"},
  "timeout_sec": 60,
  "wait": true
}
```

Requires target skill `bash`. Delegates `skill:"bash"` with JSON prompt.

### `get_slave_claude_permissions`

Input:

```json
{"target_agent_id": "optional", "target_display_name": "slave-a"}
```

Requires target skill `claude_permissions`. Delegates prompt `{"op":"get"}`.

### `update_slave_claude_permissions`

Input:

```json
{
  "target_display_name": "slave-a",
  "allow_presets": ["python", "curl", "file_write"],
  "allow_add": ["Bash(python3 *)"],
  "allow_remove": [],
  "deny_add": [],
  "deny_remove": []
}
```

Requires target skill `claude_permissions`. Uses the task channel today; future design should move this to a dedicated agentserver control channel.
