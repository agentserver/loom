# Slave Bash and Claude Permissions Design

## Goal

Let a Claude Code driver execute explicit Bash scripts on selected slave agents, and let the driver inspect or adjust each slave's Claude Code project permissions in real time without relying on shared local filesystems.

## Context

The current system already supports driver-to-slave task delegation through agentserver and slave task dispatch by `skill`. It also exposes each slave's local HTTP handler through agentserver peer proxy using the slave card's `short_id`. Recent E2E testing showed that Python scripts can be transferred and executed only after the slave's Claude Code workdir grants the right tools, for example `Bash(python3 *)`, `Bash(curl *)`, `Read`, and `Write`.

## Design Choices

### Recommended Approach: Two Surfaces

1. Add a first-class slave `bash` skill for deterministic shell execution.
2. Add peer-proxied slave HTTP endpoints for Claude Code permissions.
3. Add driver MCP tools that wrap both surfaces for Claude Code users.

This keeps command execution inside the existing task lifecycle, so task status, logs, observer events, timeout handling, and agentserver ownership still work. Permission mutation is a control-plane operation and belongs on the slave HTTP surface rather than inside ordinary tasks.

### Alternatives Considered

- Run Bash through the existing `chat` Claude executor only.
  This is flexible but nondeterministic and still depends on Claude Code's Bash permissions before execution can happen.

- Expose Bash as peer-proxy HTTP only.
  This avoids task polling latency but bypasses the task store and observer model, making E2E results harder to inspect.

- Modify local files from driver.
  This is rejected because driver, master, and slaves run on different machines and must not share a local filesystem in the design.

## Feature 1: Slave Bash Skill

Slaves may advertise `bash` in `discovery.skills`. When present, `slave-agent` registers `routes["bash"]` with a new deterministic executor.

The task prompt for `skill="bash"` is JSON:

```json
{
  "script": "python3 - <<'PY'\nprint(2 + 3)\nPY",
  "timeout_sec": 60,
  "env": {
    "EXAMPLE": "value"
  }
}
```

`script` is required. `timeout_sec` is optional and is capped by the task timeout already enforced by `dispatch.Dispatcher`. `env` is optional and merged over the process environment.

The executor runs `/bin/bash -lc <script>` in `claude.workdir` if set, otherwise the slave process working directory. It creates the workdir if missing. Output summary is JSON:

```json
{
  "exit_code": 0,
  "stdout": "5\n",
  "stderr": "",
  "workdir": "/e2e/slave-a"
}
```

Non-zero exit returns the same JSON summary and an error, so the task is marked failed but the driver can still inspect stdout/stderr through task output where available.

## Feature 2: Slave Claude Permission API

Each slave exposes authenticated HTTP endpoints through its existing web UI handler:

- `GET /claude/permissions`
- `PATCH /claude/permissions`

Both require `Authorization: Bearer <slave proxy_token>`, matching `/bridge/call`.

The implementation reads and writes:

```text
<claude.workdir>/.claude/settings.local.json
```

If `claude.workdir` is empty, it uses the slave process working directory. The file schema is Claude Code's project settings shape:

```json
{
  "permissions": {
    "allow": ["Read", "Write", "Bash(python3 *)"],
    "deny": []
  }
}
```

`GET` returns:

```json
{
  "path": "/e2e/slave-a/.claude/settings.local.json",
  "allow": ["Read", "Write", "Bash(python3 *)"],
  "deny": []
}
```

`PATCH` accepts direct list operations:

```json
{
  "allow_add": ["Bash(curl *)"],
  "allow_remove": ["Bash(python *)"],
  "deny_add": [],
  "deny_remove": []
}
```

It also accepts convenience presets:

```json
{
  "allow_presets": ["python", "pip", "curl", "file_write"]
}
```

Preset expansion:

- `python`: `Bash(python *)`, `Bash(python3 *)`
- `pip`: `Bash(pip *)`, `Bash(pip3 *)`, `Bash(python -m pip *)`, `Bash(python3 -m pip *)`
- `curl`: `Bash(curl *)`
- `file_read`: `Read`
- `file_write`: `Write`, `Edit`, `Read`

The patch operation is idempotent, sorts resulting lists for stable diffs, preserves unknown top-level settings fields, writes atomically, and returns the updated response shape.

After a successful patch, `slave-agent` refreshes its persisted `CAPABILITIES.md` with reason `claude permission update` and republishes its discovery card. Capability documents should include a small "Claude Code Permissions" section listing current allow and deny entries.

## Feature 3: Driver MCP Tools

Driver exposes three new MCP tools.

### `run_slave_bash`

Arguments:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "target_agent_id": "",
  "script": "python3 script.py",
  "env": {},
  "timeout_sec": 120,
  "wait": true
}
```

Behavior:

1. Resolve the target by `target_agent_id` or `target_display_name`.
2. Require the target agent to be available and advertise `bash`.
3. Submit a task with `skill="bash"` and JSON prompt.
4. If `wait` is true or omitted, poll until terminal status and return task status plus parsed Bash result.
5. If `wait` is false, return the task id immediately.

### `get_slave_claude_permissions`

Arguments:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "target_agent_id": ""
}
```

Behavior:

1. Resolve target and read its `short_id` from the discovery card.
2. Call `GET /claude/permissions` through `SDKClient.PeerProxy`.
3. Return the slave response unchanged.

### `update_slave_claude_permissions`

Arguments:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "allow_presets": ["python", "curl", "file_write"],
  "allow_add": [],
  "allow_remove": [],
  "deny_add": [],
  "deny_remove": []
}
```

Behavior:

1. Resolve target and `short_id`.
2. Call `PATCH /claude/permissions` through peer proxy.
3. Return the updated permission response unchanged.

## Error Handling

- Missing `script` returns MCP error for driver tool and executor error for direct slave tasks.
- Unknown target, unavailable target, or missing `short_id` returns MCP error.
- `run_slave_bash` rejects targets that do not advertise `bash`.
- Peer proxy non-2xx responses are returned as MCP errors with status and response body.
- Permission JSON parse errors are returned as HTTP 500 on the slave endpoint, because they indicate corrupted local settings.
- Invalid patch JSON returns HTTP 400.

## Security Boundaries

This feature is intentionally powerful. It does not make Bash safe; it makes Bash explicit, auditable, and opt-in.

- Slaves must opt in by advertising `bash`.
- Permission mutation is authenticated with the existing agent proxy token and only reachable through the agentserver peer proxy.
- No direct filesystem dependency exists between driver and slave.
- The permission API only edits the slave's Claude Code project settings file, not arbitrary paths.
- The Bash executor does not run unless the task skill is exactly `bash`.

Out of scope for this iteration:

- Shell sandboxing, seccomp, network policy, or command allowlists for the new deterministic Bash executor.
- Package installation policy beyond Claude Code permission entries.
- Interactive commands or streaming stdin after task start.
- Windows slave support.

## Testing Requirements

Unit tests:

- `executor.BashExecutor` runs a script and returns structured stdout/stderr.
- `executor.BashExecutor` returns structured output on non-zero exit.
- Permission store reads missing settings as empty and writes valid Claude Code JSON.
- Permission patch operations are idempotent, sorted, and preserve unknown settings fields.
- Web UI permission endpoints enforce bearer auth and read/patch settings.
- Driver tools resolve targets, require `bash`, delegate `skill="bash"`, wait for completion, and proxy permission reads/patches.

Integration tests:

- `slave-agent` only registers `bash` route when `discovery.skills` includes `bash`.
- Capability document includes Claude Code permission entries after startup and after patch.

Manual online E2E:

1. Start driver and two slaves in separate persistent directories.
2. Use driver MCP `update_slave_claude_permissions` to add `python`, `curl`, and `file_write` presets.
3. Use driver MCP `run_slave_bash` on both slaves to execute Python matrix multiplication scripts.
4. Confirm both task outputs contain matching result hashes.
