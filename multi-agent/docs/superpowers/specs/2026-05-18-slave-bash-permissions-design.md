# Slave Bash and Claude Permissions Design

## Goal

Let a Claude Code driver execute explicit Bash scripts on selected slave agents, and let the driver inspect or adjust each slave's Claude Code project permissions without relying on shared local filesystems.

## Context

The current system already supports driver-to-slave task delegation through agentserver and slave task dispatch by `skill`. Current agentserver does not expose a usable `/api/agent/peer/{short_id}/proxy` route for custom agent HTTP handlers, so permission management must use the existing task channel for this iteration. Recent E2E testing showed that Python scripts can be transferred and executed only after the slave's Claude Code workdir grants the right tools, for example `Bash(python3 *)`, `Bash(curl *)`, `Read`, and `Write`.

## Design Choices

### Recommended Approach: Task Channel Now, Dedicated Control Channel Later

1. Add a first-class slave `bash` skill for deterministic shell execution.
2. Add a first-class slave `claude_permissions` skill for Claude Code permission reads and patches, implemented by the `slave-agent` Go process rather than by the slave's Claude Code process.
3. Add driver MCP tools that wrap both task skills for Claude Code users.
4. Record that permission management should move to a dedicated agentserver control channel when one exists.

This keeps both command execution and permission mutation inside the existing task lifecycle, so task status, logs, observer events, timeout handling, and agentserver ownership still work today. Permission mutation is still conceptually a control-plane operation; using `skill="claude_permissions"` is an intentional compatibility bridge until agentserver provides a special peer/control channel.

The required execution boundary is:

```text
driver Claude Code
  -> driver MCP tool
  -> agentserver task channel
  -> slave-agent native Go executor for skill="claude_permissions"
  -> <claude.workdir>/.claude/settings.local.json
```

The implementation must not route `claude_permissions` to the slave's Claude Code chat executor. Claude Code may lack `Write`, `Edit`, or `Bash` permission before this operation, so asking it to edit its own permission file creates a bootstrap failure.

### Alternatives Considered

- Run Bash through the existing `chat` Claude executor only.
  This is flexible but nondeterministic and still depends on Claude Code's Bash permissions before execution can happen.

- Expose permission APIs through peer-proxy HTTP now.
  This would be cleaner architecturally, but current agentserver does not expose the required custom-agent peer proxy route. The feature must not depend on unpublished agentserver changes.

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

## Feature 2: Slave Claude Permission Task Skill

Slaves may advertise `claude_permissions` in `discovery.skills`. When present, `slave-agent` registers `routes["claude_permissions"]` with a deterministic permission executor.

This permission executor is native to `slave-agent`: it is ordinary Go code in the slave control process, not a prompt sent to the slave's Claude Code runtime and not a slave-local MCP server requirement. A slave-local MCP server may be added later as a convenience surface, but it must not be the only path for permission bootstrap because invoking that MCP server may itself depend on Claude Code permissions.

The task prompt for `skill="claude_permissions"` is JSON. A read request is:

```json
{
  "op": "get"
}
```

A patch request is:

```json
{
  "op": "patch",
  "allow_presets": ["python", "curl", "file_write"],
  "allow_add": ["Bash(python3 *)"],
  "allow_remove": [],
  "deny_add": [],
  "deny_remove": []
}
```

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

The `get` operation returns:

```json
{
  "path": "/e2e/slave-a/.claude/settings.local.json",
  "allow": ["Read", "Write", "Bash(python3 *)"],
  "deny": []
}
```

The `patch` operation accepts direct list operations:

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

The task-channel implementation is not the long-term ideal. Future work should replace the `claude_permissions` task skill with a special agentserver control channel or peer proxy endpoint that can reach a slave's local control plane without consuming ordinary task capacity.

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

1. Resolve target by id or display name.
2. Require the target agent to be available and advertise `claude_permissions`.
3. Submit a task with `skill="claude_permissions"` and prompt `{"op":"get"}`.
4. Wait for terminal status and return the permission JSON result.

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

1. Resolve target by id or display name.
2. Require the target agent to be available and advertise `claude_permissions`.
3. Submit a task with `skill="claude_permissions"` and a patch prompt.
4. Wait for terminal status and return the updated permission JSON result.

## Error Handling

- Missing `script` returns MCP error for driver tool and executor error for direct slave tasks.
- Unknown target or unavailable target returns MCP error.
- `run_slave_bash` rejects targets that do not advertise `bash`.
- Permission tools reject targets that do not advertise `claude_permissions`.
- Permission task failures are returned as MCP errors with task id, status, and failure reason.
- Permission JSON parse errors fail the permission task, because they indicate corrupted local settings.
- Invalid patch JSON fails the permission task.

## Security Boundaries

This feature is intentionally powerful. It does not make Bash safe; it makes Bash explicit, auditable, and opt-in.

- Slaves must opt in by advertising `bash`.
- Slaves must opt in to permission management by advertising `claude_permissions`.
- Permission mutation is routed through agentserver task delegation and inherits existing workspace/task authorization.
- No direct filesystem dependency exists between driver and slave.
- The permission executor only edits the slave's Claude Code project settings file, not arbitrary paths.
- The Bash executor does not run unless the task skill is exactly `bash`.
- Future work should move permission management to a special control channel rather than ordinary tasks once agentserver supports it.

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
- Permission executor reads and patches settings through `skill="claude_permissions"`.
- Driver tools resolve targets, require `bash` or `claude_permissions`, delegate the correct skill, and wait for completion.

Integration tests:

- `slave-agent` only registers `bash` route when `discovery.skills` includes `bash`.
- `slave-agent` only registers `claude_permissions` route when `discovery.skills` includes `claude_permissions`.
- Capability document includes Claude Code permission entries after startup and after patch.

Manual online E2E:

1. Start driver and two slaves in separate persistent directories.
2. Use driver MCP `update_slave_claude_permissions` to add `python`, `curl`, and `file_write` presets.
3. Use driver MCP `run_slave_bash` on both slaves to execute Python matrix multiplication scripts.
4. Confirm both task outputs contain matching result hashes.
