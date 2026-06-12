# Driver Task Journal Design

**Status:** Draft (2026-06-12)
**Scope:** `driver-agent` MCP task delegation paths, local recovery metadata, MCP recovery tool, docs, and prod_test validation.
**Non-scope:** agentserver idempotent task creation, remote task ownership changes, observer authorization fixes, and cancellation semantics for already-created tasks.

## Background

Issue `agentserver/loom#7` exposed two separate driver MCP problems:

1. Long `tools/call` handling blocked later MCP requests when the MCP server processed one request at a time.
2. Shell helpers waited synchronously by default, so an outer MCP client timeout could hide the `task_id` from the caller.

The current branch already addresses those two symptoms by running driver MCP `tools/call` requests concurrently and making shell helpers default to `wait=false`.

There is still a recovery gap: after driver receives a successful `DelegateTaskResponse`, the delegated task can keep running even if the MCP response is never delivered to the caller. This can happen with an outer client timeout, an interrupted stdio session, or an explicit `wait:true` call that blocks after task creation. If the `task_id` is only kept in the in-flight response, users cannot reliably discover the created task later.

## Goals

1. Persist every driver-created delegated `task_id` locally as soon as driver receives it from agentserver.
2. Record before any wait loop, observer update, write-token rebinding, result marshalling, or MCP response write.
3. Cover all driver MCP tools that call `DelegateTask`, not only shell helpers.
4. Make recent recorded task IDs recoverable through a driver MCP tool.
5. Store the journal beside the existing audit log so deployment and prod_test operators know where to look.
6. Surface journal write failures explicitly, with the created `task_id` in the error message.
7. Keep the implementation append-only, small, and testable.

## Non-Goals

- This does not solve the distributed case where agentserver creates a task but the HTTP response containing `task_id` never reaches driver. That needs agentserver support, such as idempotent `client_request_id` task creation and lookup.
- This does not guarantee recovery if the driver process is killed in the tiny window between receiving the HTTP response and appending the local record.
- This does not cancel orphaned tasks. It gives operators and MCP clients the task IDs needed to inspect or cancel them.
- This does not replace the audit log. The audit log remains file-transfer oriented; this feature adds a task-specific journal.

## Approaches Considered

### Option A: Record only shell helper task IDs

This is the smallest patch and targets the original symptom. It is incomplete because `submit_task`, `resume_task`, file tools, permission tools, contract submission, and dynamic MCP registration also create delegated tasks that can outlive a timed-out MCP call.

### Option B: Reuse `audit.log`

This avoids another file, but it mixes file access events with task recovery state. The audit logger currently swallows write errors by design, which is acceptable for postmortem audit but not for the task recovery path.

### Option C: Add a dedicated task journal

Add a `driver-tasks.jsonl` file, append one record per successful delegation, fsync each line, and expose recent records via `list_driver_tasks`. This keeps recovery data focused, lets journal failures be returned to the caller, and mirrors the existing audit log storage model. This is the recommended design.

## Architecture

Add a `TaskJournal` under `internal/driver`:

```go
type TaskJournal struct {
    // append-only JSONL file with mutex-protected writes
}
```

`cmd/driver-agent` opens it at startup beside `audit.log`:

```text
<driver_defaults.audit_log_dir>/driver-tasks.jsonl
```

If `driver_defaults.audit_log_dir` is unset, the existing audit directory default is reused:

```text
~/.cache/multi-agent/<driver_short_id>/driver-tasks.jsonl
```

`Tools` gets an optional journal reference. Production startup sets it. Tests can opt in through the existing test helper. If a journal is configured, every successful `DelegateTask` response is recorded immediately through a shared helper.

## Journal Schema

Each line is one JSON object:

```json
{
  "ts": "2026-06-12T03:30:00.000000000Z",
  "event": "delegate_task",
  "tool": "run_slave_powershell",
  "task_id": "task-abc",
  "session_id": "session-xyz",
  "target_id": "agent-123",
  "target_display_name": "slave-local-prod",
  "skill": "powershell",
  "status": "submitted",
  "wait": true,
  "timeout_sec": 600
}
```

Fields:

- `ts`: UTC RFC3339Nano timestamp assigned by the driver when writing.
- `event`: fixed value `delegate_task` for forward-compatible filtering.
- `tool`: driver MCP tool that created the task.
- `task_id`: agentserver task ID.
- `session_id`: optional session returned by agentserver.
- `target_id`: target agent ID.
- `target_display_name`: best-known target display name.
- `skill`: delegated skill.
- `status`: status returned in the delegate response, if present.
- `wait`: whether the tool was going to wait synchronously after creation.
- `timeout_sec`: timeout passed to the delegated task or wait path.

No prompts, script bodies, environment variables, file contents, tokens, or credentials are written to the journal.

## Write Timing

For every call site:

```go
resp, err := sdk.DelegateTask(ctx, req)
if err != nil {
    return ...
}
if err := tools.recordDelegatedTask(...); err != nil {
    return taskCreatedButRecordFailed(resp.TaskID, err)
}
```

The record must happen before:

- `waitDelegatedTask`
- observer relay writes
- `reg.RebindWriteTokenTaskID`
- `reg.TrackTask`
- `observer.Emit`
- `json.Marshal` of the MCP result

This ordering is the core fix. A long `wait:true` call can time out externally, but the task ID is already durable before the wait begins.

## MCP Recovery Tool

Add `list_driver_tasks`:

```json
{
  "limit": 50,
  "task_id": "task-abc"
}
```

Behavior:

- Reads `driver-tasks.jsonl`.
- Returns newest records first.
- Defaults to `limit=50`.
- Caps `limit` at `500`.
- If `task_id` is provided, filters to matching records.
- Returns the journal path so an operator can inspect the file directly.
- Skips malformed JSONL lines and returns `warnings` so one partial write or
  manual edit does not break recovery for older valid task IDs.

Example result:

```json
{
  "journal_path": "/home/agent/.cache/multi-agent/drv-001/driver-tasks.jsonl",
  "warnings": [],
  "tasks": [
    {
      "ts": "2026-06-12T03:30:00Z",
      "event": "delegate_task",
      "tool": "run_slave_bash",
      "task_id": "task-abc",
      "target_id": "agent-123",
      "target_display_name": "slave-local-prod",
      "skill": "bash",
      "status": "submitted",
      "wait": true,
      "timeout_sec": 600
    }
  ]
}
```

If no journal is configured, the tool returns an empty task list and an empty path. Production driver-agent always configures the journal.

## Error Handling

Opening the journal fails startup, just like opening `audit.log`.

Append failures are returned as MCP tool errors. The error message includes the created `task_id`:

```text
task task-abc was created but driver failed to record it in driver-tasks.jsonl: <error>
```

This avoids silently continuing with no durable recovery record. It also gives the caller the task ID if the MCP response is still delivered.

## Covered Delegation Paths

The first implementation records these driver-created tasks:

- `submit_task`
- `submit_contract_task`
- `resume_task`
- `run_slave_bash`
- `run_slave_powershell`
- `run_slave_shell`
- `register_slave_mcp`
- `unregister_slave_mcp`
- `get_slave_claude_permissions`
- `update_slave_claude_permissions`
- `read_slave_file`
- `write_slave_file`
- `stat_slave_file`

Any future driver tool that calls `DelegateTask` must call the shared record helper immediately after success.

## Testing

Unit tests cover:

- JSONL append, fsync-compatible file creation, and newest-first reads.
- `list_driver_tasks` filtering and limit behavior.
- shell helper `wait:true` records before entering `GetTask` wait polling.
- `submit_task` records before observer/write-token side effects.
- representative wait-only helpers record before waiting.
- `cmd/driver-agent` resolves `driver-tasks.jsonl` beside `audit.log`.

Regression verification:

```bash
go test ./internal/driver -run 'TaskJournal|ListDriverTasks|RecordsDelegatedTask' -count=1
go test ./cmd/driver-agent -run 'DriverLocal|TaskJournal' -count=1
go test ./...
```

Prod validation should submit a long `run_slave_bash` or `run_slave_powershell` with `wait:true`, interrupt or timeout the MCP client after submission, then verify `list_driver_tasks` and `driver-tasks.jsonl` contain the `task_id`.

## Known Remaining Gap

This is a local durability fix, not a distributed transaction. If agentserver creates a task but the driver never receives the delegate response, driver cannot record the unknown `task_id`. The next layer should add an idempotent client-generated request ID to `DelegateTaskRequest`, persist it on agentserver, and let driver recover by request ID after reconnect.
