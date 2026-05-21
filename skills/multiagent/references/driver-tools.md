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
      "skills": ["chat", "mcp", "register_mcp", "bash", "claude_permissions"],
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
  "recommended_route": "driver_fanout",
  "recommended_target_id": "",
  "recommended_target_display_name": "",
  "recommended_skill": "fanout",
  "satisfied_tools": [],
  "missing_tools": [],
  "missing_skills": [],
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

### `register_slave_mcp`

Input:

```json
{
  "target_agent_id": "optional",
  "target_display_name": "slave-a",
  "spec": {
    "name": "row_stats",
    "description": "Compute statistics for rows",
    "version": 1,
    "tools": [
      {
        "name": "summarize_rows",
        "description": "Summarize numeric row values",
        "args_schema": {"type":"object","properties":{"rows":{"type":"array"}},"required":["rows"]},
        "result_description": "JSON summary"
      }
    ],
    "allowed_packages": []
  },
  "source_path": "generated_mcp/row_stats/v1.py",
  "timeout_sec": 60
}
```

Requires target skill `register_mcp`. Delegates `skill:"register_mcp"` with the JSON above. Use after a bash task has written and validated the source.

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

## Slave File Tools

In-band file I/O against a slave that advertises `file`. Bytes flow through the driver's `FileRegistry` (sha256-keyed cache under `logs/file-cache/`), so payloads never need to live in the LLM context as tool arguments or results. Slave-side semantics are documented in `slave-skills.md` under `file`.

Prefer these over `run_slave_bash "cat ..."` / base64-in-bash payloads whenever you actually want bytes (uploading an MCP server source, pulling back a log, copying between slaves). Prefer the PUT-manifest path (`submit_task.read_paths`/`write_paths`) for large artifacts that should live in observer storage.

### `read_slave_file`

Input:

```json
{
  "target_agent_id": "optional",
  "target_display_name": "slave-a",
  "path": "data/in.csv",
  "offset": 0,
  "length": 65536,
  "encoding": "utf-8",
  "inline_max_bytes": 65536
}
```

- `path` resolves against the slave's `claude.workdir` if relative; absolute paths are used as-is.
- `encoding`: `"utf-8"` (default) or `"base64"` for binary-safe transfer.
- `offset` / `length` chunk large files (slave caps a single read at 8 MiB).
- `inline_max_bytes` controls whether `content` is returned inline; bytes are always cached.

Result:

```json
{
  "slave_path": "data/in.csv",
  "size": 41,
  "sha256": "f38d...",
  "blob_handle": "sha256:f38d...",
  "cache_path": "logs/file-cache/f38d...",
  "encoding": "utf-8",
  "content": "...optional inline...",
  "eof": true
}
```

`blob_handle` is the stable reference. Pass it back into `write_slave_file` as `source_blob` to push the same bytes to another slave without re-fetching. `cache_path` is a driver-local file you can hand to the `Read` tool when you actually need to inspect the bytes.

### `write_slave_file`

Input — exactly one of `content` / `source_blob` / `source_path` is required:

```json
{
  "target_display_name": "slave-a",
  "path": "generated_mcp/foo/v1.py",
  "mode": "overwrite",
  "mkdir": true,
  "encoding": "utf-8",
  "content": "...inline string..."
}
```

```json
{
  "target_display_name": "slave-b",
  "path": "data/copy.bin",
  "source_blob": "sha256:f38d..."
}
```

```json
{
  "target_display_name": "slave-a",
  "path": "generated_mcp/foo/v1.py",
  "source_path": "/abs/driver/path/v1.py"
}
```

- `mode`: `overwrite` | `append` | `create_new` | `patch`. `offset` is only valid with `patch`.
- `source_path` (driver-local absolute path) gets registered in `FileRegistry` on the way through, so the resulting handle is available for subsequent fanout.
- `source_blob` must reference a handle the driver already knows (returned by a prior `read_slave_file` or `write_slave_file source_path`).

Result: `{slave_path, bytes_written, mode, source}`.

### `stat_slave_file`

Input:

```json
{"target_display_name": "slave-a", "path": "generated_mcp/foo/v1.py"}
```

Result: `{slave_path, exists, size?, mode?, is_dir?, mtime?}`. Missing paths return `exists:false` — not an error — so it's cheap to probe before writing.

### Cross-slave copy pattern

```text
read_slave_file(target=A, path=src)        -> {blob_handle: H, ...}
write_slave_file(target=B, path=dst, source_blob=H)
```

The bytes round-trip through the driver's `FileRegistry`, never through chat or `run_slave_bash`. Use this instead of routing through observer artifacts when both endpoints are slaves currently advertising `file` and the payload fits comfortably in driver-local cache.
