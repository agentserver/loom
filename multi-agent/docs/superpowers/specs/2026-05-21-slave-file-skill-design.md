# Slave File Skill Design

## Goal

Add a first-class, stateless `file` skill on slaves for deterministic file I/O (read, write, stat), plus matching driver MCP tools. The skill complements `bash`: structured, binary-safe, and free of shell permission setup, so drivers and planners can move file contents around without writing ad-hoc `cat`/`tee`/`base64` scripts.

## Context

Slaves today advertise capabilities like `chat`, `mcp`, `bash`, `register_mcp`, and `claude_permissions`. `bash` (see `2026-05-18-slave-bash-permissions-design.md`) is the existing native-Go executor pattern: the slave declares the skill in `discovery.skills`, `slave-agent/main.go` registers a Go executor in `routes[<skill>]`, and the prompt is JSON. The driver exposes a thin MCP tool (`run_slave_bash`) that delegates a task with `skill="bash"` and waits for completion.

File I/O is currently possible only by running `cat` / `tee` / `base64` through `bash`. That works but:

- Depends on the slave having `Bash(cat *)`, `Bash(tee *)`, etc. granted.
- Mixes file bytes with shell stdout framing, which is fragile for binaries.
- Has no structured offset/length, so chunked transfer of large files needs hand-rolled `dd` invocations.
- Returns an unstructured shape (`{exit_code, stdout, stderr}`) instead of a typed result.

A dedicated `file` skill removes those frictions while preserving the same trust model (a slave that advertises `file` is choosing to expose file I/O; there is no extra sandboxing beyond what `bash` already permits).

## Design Choices

### Recommended Approach: Single `file` Skill, Op-Routed JSON Prompt

One skill named `file`, advertised in `discovery.skills`. The prompt is JSON with an `op` discriminator (`read` | `write` | `stat`). The executor is a Go type in `internal/executor/file.go` that holds no per-call state — each `Run` parses, performs the I/O, and returns.

Driver side exposes three MCP tools that each delegate a `skill="file"` task with the right `op` baked in: `read_slave_file`, `write_slave_file`, `stat_slave_file`. The reason for three driver tools but one slave skill: LLM tool selection is sharper when each tool has a specific name and schema, but `discovery.skills` should not multiply (it shapes capability advertising, planner dispatch, and permission gating).

### Alternatives Considered

- **Three separate skills (`file_read`, `file_write`, `file_stat`).** Lets a slave grant read but not write. Rejected because (a) the cluster has no current need for that granularity, (b) it triples the size of `discovery.skills` for what is one capability, and (c) it diverges from the `bash` precedent (one skill, op-shaped JSON for `script`/`env`/`timeout`).
- **Keep using `bash` with `cat`/`tee`.** Rejected: not binary-safe, requires Claude Code Bash permissions, no structured offset/length, return shape mixes file bytes with shell framing.
- **Slave-local MCP file server.** Rejected for the same reason `claude_permissions` is native Go: bootstrapping an MCP call may itself depend on Claude Code permissions the operator has not yet granted.

## Skill Protocol

Prompt is JSON; the executor switches on `op`.

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

- `path` — required. Relative paths resolve against `claude.workdir` (falling back to the slave process cwd, matching `bash`). Absolute paths are used as-is.
- `offset` — optional, byte offset. Default 0.
- `length` — optional, max bytes to return. Default: read to EOF.
- `encoding` — optional, `"utf-8"` (default) or `"base64"`. `utf-8` rejects invalid UTF-8 (caller should switch to base64); `base64` is the binary-safe path.

Hard cap: a single `read` returns at most **8 MiB** of payload. Exceeding the cap (either because the file is larger and no `length` was set, or because `length` itself exceeds the cap) is an error — the caller is expected to chunk by raising `offset`. The cap protects the slave from accidental OOM on a multi-GB file.

Result:

```json
{
  "path": "/abs/data/in.csv",
  "bytes": 1234,
  "encoding": "utf-8",
  "content": "...",
  "eof": true
}
```

`eof` is `true` when the read reached end-of-file (so the caller knows there is nothing more to fetch at higher offsets).

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

- `path` — required, same resolution as `read`.
- `content` — required. UTF-8 string when `encoding` is `utf-8`; base64-decoded to bytes when `encoding` is `base64`.
- `encoding` — optional, `"utf-8"` (default) or `"base64"`.
- `mkdir` — optional, default `false`. If `true`, parent directories are created with `0o755`.
- `mode` — optional, default `"overwrite"`. One of:

  | mode | Behavior | Uses `offset`? |
  |---|---|---|
  | `overwrite` | Truncate file to 0, write from byte 0. Creates file if missing. | No (must be omitted or 0) |
  | `append` | Open `O_APPEND`, write at current EOF. Creates file if missing. | No (must be omitted or 0) |
  | `create_new` | Open `O_EXCL`, error if file exists. Write from byte 0. | No (must be omitted or 0) |
  | `patch` | Open `O_WRONLY`, write at `offset` without truncating. Creates file if missing. If `offset > size`, the gap is sparse / zero-filled (matches `pwrite` semantics). | **Required** |

  Schema validation rejects `offset` on the three non-patch modes to keep the contract unambiguous.

Result:

```json
{
  "path": "/abs/data/out.txt",
  "bytes_written": 4096,
  "mode": "patch",
  "offset": 65536
}
```

`offset` in the result echoes the input for `patch`; omitted for the other modes.

### `op: "stat"`

```json
{
  "op": "stat",
  "path": "data/out.txt"
}
```

Result when the path exists:

```json
{
  "path": "/abs/data/out.txt",
  "exists": true,
  "size": 65540,
  "mode": "0644",
  "is_dir": false,
  "mtime": "2026-05-21T10:30:00Z"
}
```

Result when the path does not exist:

```json
{
  "path": "/abs/data/out.txt",
  "exists": false
}
```

`stat` deliberately does not error on `ENOENT` — its whole purpose is letting the caller decide "should I write this file?" without having to distinguish a real error from absence. Other errors (permission denied, IO error) still surface as task failures.

## Trust Model

A slave that advertises `file` is granting the same trust surface as `bash`: any path the slave-agent OS user can access is reachable. No path-escape sandbox, no allowlist. Rationale: `bash` already permits arbitrary `cat`/`tee` against any path; adding path checks to `file` would be both weaker than what `bash` already allows and inconsistent. Operators control exposure through `discovery.skills` (don't advertise `file` if you don't want it) and through the OS user's filesystem permissions.

## Driver MCP Tools

In `internal/driver/slave_file_tools.go`, add three tools modeled exactly on `runSlaveBashTool`:

- `read_slave_file(target_agent_id?, target_display_name?, path, offset?, length?, encoding?)` — delegates `skill="file"`, prompt `{"op":"read", ...}`, wait=true by default. Schema mirrors the slave-side read fields.
- `write_slave_file(target_agent_id?, target_display_name?, path, content, encoding?, mode?, mkdir?, offset?)` — delegates `{"op":"write", ...}`. Schema validates that `offset` is set iff `mode == "patch"`.
- `stat_slave_file(target_agent_id?, target_display_name?, path)` — delegates `{"op":"stat", ...}`.

All three reuse `resolveAvailableAgent` and gate on `hasSkill(card, "file")`, matching `run_slave_bash`'s patterns. Wait semantics, timeout defaults, and result wrapping (`marshalDelegatedTaskOutput`) come from the existing helpers — no new control flow.

## Stateless Guarantees

The executor never holds open file handles, buffers, or session state across calls:

- Each `Run` opens the file with `os.OpenFile`, performs one `ReadAt` / `WriteAt` (or `ReadFile` / `WriteFile` for whole-file ops), then closes inside the same call via `defer`.
- No in-memory cache of file contents, paths, or stat results.
- Concurrency safety is delegated to the OS filesystem — two concurrent `patch` writes at overlapping offsets behave exactly as POSIX `pwrite` does.

The result: the executor can be invoked from any dispatch thread, in any order, with no coordination, and the only persistent state is the filesystem itself.

## Implementation Outline

### Files Added

- `multi-agent/internal/executor/file.go` — `FileExecutor` and its `Run` implementation. Pure I/O code, no goroutines, no caches.
- `multi-agent/internal/executor/file_test.go` — unit tests (see below).
- `multi-agent/internal/driver/slave_file_tools.go` — three MCP tool types (`readSlaveFileTool`, `writeSlaveFileTool`, `statSlaveFileTool`), each ~25 LOC, all delegating through `t.sdk.DelegateTask` like `runSlaveBashTool`.
- `multi-agent/internal/driver/slave_file_tools_test.go` — driver tool tests.

### Files Modified

- `multi-agent/cmd/slave-agent/main.go` — add `if hasSkill(cfg.Discovery.Skills, "file") { routes["file"] = executor.NewFileExecutor(executor.FileConfig{WorkDir: cfg.Claude.WorkDir}) }` next to the existing `bash` registration.
- `multi-agent/cmd/slave-agent/main_test.go` — extend `hasSkill` test cases to cover `"file"`.
- `multi-agent/cmd/slave-agent/config.example.yaml` — add commented `# - file` entry next to `# - bash`.
- `multi-agent/cmd/slave-agent/README.md` — mention `file` in the skill list.
- `multi-agent/internal/driver/tools.go` (or wherever the tool registry lives) — register the three new tools.
- `multi-agent/cmd/driver-agent/README.md` — add the three tools under the slave control helpers section, with example payloads.
- `skills/multiagent/references/slave-skills.md` — add a `file` section parallel to the `bash` section, documenting the three ops and modes.

### Tests

`internal/executor/file_test.go`:

- `read` whole file, with offset, with length, with both, base64 round-trip, invalid UTF-8 rejected when `encoding=utf-8`, file-not-found error, 8 MiB cap enforced.
- `write` overwrite (creates and replaces), append (creates and appends), `create_new` errors on existing file, `patch` at offset 0, `patch` past EOF (zero-fill), `patch` rejects when `offset` is set with non-patch mode, `mkdir` creates missing parents, base64 input round-trips bytes.
- `stat` on existing file, on missing file (returns `exists:false`, no error), on directory (`is_dir:true`).
- Relative path resolves against configured `WorkDir`; absolute path used as-is.

`internal/driver/slave_file_tools_test.go` (modeled on `slave_tools_test.go`):

- Each tool errors when target lacks `file` skill.
- Each tool errors on missing required fields.
- `write_slave_file` rejects `offset` with non-patch mode (schema-level).
- `wait=true` path returns the slave's structured result; `wait=false` path returns task id and status.

`cmd/slave-agent/main_test.go`:

- `hasSkill(["chat","file"], "file") == true`, `hasSkill(["chat"], "file") == false`.

## Out of Scope

- **Directory ops** (`mkdir` standalone, `ls`, `rm`, `mv`). Can be added later as new ops; not required for read/write/stat flows.
- **Streaming / chunked transport.** The 8 MiB cap plus `offset`/`length` on read and `patch`+`offset` on write give callers explicit chunking — adequate for the current need. A future streaming op can be added if profile shows excessive task-channel overhead.
- **Per-path ACLs on the slave.** Trust model is "advertise the skill or don't"; same as `bash`.
- **Optimistic concurrency** (e.g. `if_etag`/`if_mtime` on write). The stateless executor leaves coordination to the orchestrator; can be revisited if conflict scenarios appear in practice.
