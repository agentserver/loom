# Slave Skills

Slaves advertise skills through their discovery card. The driver and planner should dispatch by `skill`:

- `chat`: natural-language Claude Code task.
- `chat_resume`: driver-only continuation of a paused `chat` task (see below).
- `mcp`: direct call to a configured or generated MCP server.
- `register_mcp`: register a pre-built MCP server file after source generation
  and validation through the target's advertised shell interface.
- `bash`: run explicit Bash through native slave-agent code. On Windows this
  is advertised only when real Bash is detected, such as Git Bash or WSL.
- `powershell`: run explicit PowerShell through native slave-agent code.
- `file`: stateless file read/write/stat through a native slave-agent executor.
- `permissions`: read or patch Claude Code permissions through native
  slave-agent code. `claude_permissions` is a legacy alias.

## `chat`

Prompt is natural language. Use for work that needs Claude Code reasoning or file editing inside the slave workspace. Do not ask chat to call MCP when a direct `skill:"mcp"` JSON call is available.

### humanloop MCP server (auto-injected into all chat backends)

Every `chat` and `chat_resume` invocation runs with the `loom_humanloop`
MCP server attached. The server exposes two tools to the model:

- `ask_user(question, options?, context?)` — pause and ask the human
- `request_permission(intent, target, reason?)` — pause and request approval

When the model calls either, the slave executor stashes the payload, kills
the backend gracefully, and finalises the task with
`result.kind="awaiting_user"`. The driver's `wait_task` / `get_task`
recognises that marker and surfaces it as `status:"awaiting_user"` to the
caller.

Per-task question quota defaults to 5; beyond that the tool returns
`{"status":"refused"}` to the model without pausing.

## `chat_resume`

JSON-prompt skill. **Driver-only:** the driver's `resume_task` tool
delegates to this; do not call directly from `submit_task` unless you know
what you're doing.

Prompt shape:

```json
{ "session_id": "S-uuid", "answer": "...", "kind": "ask_user|request_permission" }
```

Slave re-runs the chat backend with `claude --resume <S>` (or `codex exec
resume <S>`), feeding `"User answered: <answer>"` as the next user turn.
A per-session flock at `$LOOM_HOME/<agent>/humanloop/<session>.lock`
prevents concurrent resumes; the loser fails with `session busy`.

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

## Shell validation → register_mcp workflow

The driver Claude should construct MCP servers using the two dedicated skills,
writing source with file tools when possible and validating with the target's
advertised shell interface (`run_slave_shell`, `run_slave_powershell`, or
`run_slave_bash`):

1. **`scaffold-mcp-server`** — generate `generated_mcp/<name>/v1.py` from `spec.json`. The scaffolder owns the JSON-RPC protocol region; you only fill in handler bodies. `args_schema` from the spec is translated to `inputSchema` in the generated TOOLS list, so the two fields stay in sync by construction.
2. Hand-edit handlers between `# @@scaffold:business:start <tool>` / `# @@scaffold:business:end` markers. Re-running scaffold (e.g. after `spec.json` changes) preserves these bodies.
3. **`mcp-acceptance`** — drive `initialize → tools/list → tools/call` against `cases.jsonl`. Exit 0 = safe; gate registration: `mcp_acceptance.py ... && register_slave_mcp ...`. Exit 1 = case failure; exit 2 = server unreachable.
4. Submit `register_mcp` / `register_slave_mcp` with the same `spec.json` and `source_path`. Registration only does structural checks + `tools/list` smoke; semantic correctness is the acceptance step's job.

The structured spec exists so capability publishing has clean metadata AND as input to the scaffolder; it is not a generation contract for one-shot LLM emission.

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

## `powershell`

Prompt is JSON:

```json
{
  "script": "$PSVersionTable.PSVersion.ToString()\nGet-ChildItem -Path .",
  "timeout_sec": 60,
  "env": {"KEY": "value"}
}
```

Use for Windows-native commands and scripts. Prefer PowerShell cmdlets and
Windows paths when targeting Windows slaves. Do not wrap PowerShell in
`run_slave_bash`; Bash is available on Windows only when the slave has
detected a real Bash implementation such as Git Bash or WSL.

## Default shell selection

`shell` is a driver-side convenience helper, not a slave skill. The driver's
`run_slave_shell` tool reads the target card's default `command_interfaces`
entry and delegates to `skill:"powershell"` or `skill:"bash"`. Windows slaves
default to PowerShell; Unix-like slaves usually default to Bash.

## `file`

Stateless file I/O through a native `slave-agent` Go executor. Advertised by adding `file` to `discovery.skills`. The prompt is JSON with an `op` discriminator. Same trust model as `bash`: a slave that advertises `file` is granting access to any path its OS user can reach.

### `op: "read"`

```json
{
  "op": "read",
  "path": "data/in.csv",
  "offset": 0,
  "length": 65536,
  "encoding": "utf-8"
}
```

- `path` resolves against `claude.workdir` if relative; absolute paths used as-is.
- `encoding`: `"utf-8"` (default; rejects invalid UTF-8) or `"base64"` (binary-safe).
- `offset` / `length` optional; reads to EOF if `length` unset.
- Hard cap: one read returns ≤ 8 MiB. Chunk by raising `offset`.

Result: `{path, bytes, encoding, content, eof}`.

### `op: "write"`

```json
{
  "op": "write",
  "path": "data/out.txt",
  "content": "hello\n",
  "encoding": "utf-8",
  "mode": "overwrite",
  "mkdir": true,
  "offset": 0
}
```

Modes: `overwrite` (truncate+write), `append` (`O_APPEND`), `create_new` (`O_EXCL`, errors if file exists), `patch` (writes at `offset` without truncating; zero-fills if `offset > size`). `offset` is rejected on non-patch modes.

Result: `{path, bytes_written, mode, offset?}`.

### `op: "stat"`

```json
{"op":"stat","path":"data/out.txt"}
```

Returns `{path, exists, size?, mode?, is_dir?, mtime?}`. Missing paths return `exists:false` (not an error) so callers can probe "should I write here?" cheaply.

### Driver-side tools

The driver exposes `read_slave_file`, `write_slave_file`, and `stat_slave_file`. They keep bytes out of the LLM context: `read_slave_file` caches in the driver's `FileRegistry` and returns a `sha256` / `blob_handle` / `cache_path`; `write_slave_file` accepts `source_blob` (a prior handle) or `source_path` (a driver-local path) so the LLM never carries large payloads as tool arguments.

Full schemas, the cross-slave copy pattern, and when to prefer these over the PUT-manifest path live in `driver-tools.md` ("Slave File Tools") and `orchestration-patterns.md` ("File Transfer").

## `permissions`

Implemented by slave-agent native Go code, not by slave Claude Code.
`claude_permissions` remains available as a backward-compatible alias.

Read prompt:

```json
{"op": "get"}
```

Patch prompt:

```json
{
  "op": "patch",
  "presets": ["python", "curl", "file_write"],
  "allow_add": ["Bash(pip install *)"],
  "allow_remove": [],
  "deny_add": [],
  "deny_remove": []
}
```

This is a compatibility bridge over the task channel. Future work should use a dedicated agentserver peer/control channel.

## Capability Document

On startup the slave scans runtime, skills, MCP servers, generated MCP, resources, and capability journal, then writes `journal/CAPABILITIES.md`. It is exposed at `/capabilities` through the tunneled web UI and advertised as `capability_doc_path`.

## Discovery Card Metadata

Slave discovery cards include platform and command interface metadata when
the slave-agent runtime supports it:

```json
{
  "platform": {"os": "windows", "arch": "amd64"},
  "command_interfaces": [
    {"skill": "powershell", "kind": "powershell", "command": "powershell.exe", "default": true}
  ]
}
```

Use `platform.os` and `command_interfaces` to choose shell helpers. If a
legacy card omits these fields, fall back to the advertised skills: `bash`
means a real Bash executor and `powershell` means a PowerShell executor.
