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

### Avoiding LLM Token Waste: Out-of-Band by Default

A naive design routes file bytes through the driver's Claude Code context — every `read_slave_file` result becomes an MCP tool result the LLM sees, and every `write_slave_file` requires `content` as a tool-call argument. For small text this is fine; for large or binary files it burns the LLM context window unnecessarily.

Crucially, **task-channel bytes do not enter the LLM by themselves**. The task result returns to driver-side Go code first; only what the MCP tool's `Call` chooses to put in its JSON return reaches the LLM. So byte-bypass can be done entirely in the driver tool layer without changing the slave wire protocol.

The driver already operates a sha256-keyed blob channel for user files: `internal/driver/files_handler.go` exposes `/files/blob/{sha}` over the agentserver peer-proxy, with `FileRegistry` managing registrations and an audit log. The file skill **reuses this existing channel**: after `read_slave_file` pulls bytes via the task channel, the driver writes them to a local cache file and registers it in `FileRegistry`. Other slaves can then fetch the same content directly via `/files/blob/{sha}` without going through the driver Claude. The LLM only ever sees `{size, sha256, cache_path, blob_handle}` — ~200 bytes regardless of file size.

A small inline window remains for genuinely small files (`inline_max_bytes`, default 4 KiB): the tool result also includes a `content` field when the file is small and the encoding is UTF-8, so the LLM can read tiny configs without a second `Read` round-trip.

For `write_slave_file`, the three source modes mirror this:

- `content` — small inline string the LLM provides (default cap 4 KiB).
- `source_blob` — a sha256 handle returned by a prior `read_slave_file` or by user-file registration. The driver looks up the local path in `FileRegistry`, reads bytes itself, and ships them through the task channel. **The LLM never sees the bytes.**
- `source_path` — driver-local absolute path. The driver registers it in `FileRegistry` (so it gets a sha) and proceeds as in `source_blob`.

This composes with the existing dataflow: a slave-A → slave-B file transfer becomes "driver issues `read_slave_file(A)`, driver issues `write_slave_file(B, source_blob=sha)`" — both tool returns are small handles, and slave-B can fetch the blob through the peer-proxy without ever putting bytes in the LLM's context.

### Alternatives Considered

- **Three separate skills (`file_read`, `file_write`, `file_stat`).** Lets a slave grant read but not write. Rejected because (a) the cluster has no current need for that granularity, (b) it triples the size of `discovery.skills` for what is one capability, and (c) it diverges from the `bash` precedent (one skill, op-shaped JSON for `script`/`env`/`timeout`).
- **Keep using `bash` with `cat`/`tee`.** Rejected: not binary-safe, requires Claude Code Bash permissions, no structured offset/length, return shape mixes file bytes with shell framing.
- **Slave-local MCP file server.** Rejected for the same reason `claude_permissions` is native Go: bootstrapping an MCP call may itself depend on Claude Code permissions the operator has not yet granted.
- **All bytes inline through the LLM.** Rejected: burns tokens linearly with file size for no LLM benefit, and breaks binary content unless every consumer is forced to deal with base64 in context.
- **Slave-side `/files/*` blob server for direct slave↔slave transfers.** Possible future work; deferred. The driver-cached approach already enables slave-A → driver-blob → slave-B fetches via the existing peer-proxy; a direct slave↔slave path only matters once the driver becomes a bandwidth bottleneck. YAGNI for now.

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

In `internal/driver/slave_file_tools.go`, add three tools modeled on `runSlaveBashTool` for target resolution and task delegation, but with extra logic in `Call` to keep bytes out of the LLM context.

All three reuse `resolveAvailableAgent` and gate on `hasSkill(card, "file")`, matching `run_slave_bash`. They use the existing `t.sdk.DelegateTask` + `t.waitDelegatedTask` plumbing for transport. They share access to the driver's `FileRegistry` and `AuditLog` (passed through `Tools` like the existing `/files/*` handler does).

### `read_slave_file`

Input:

```json
{
  "target_agent_id": "...",
  "target_display_name": "...",
  "path": "data/in.csv",
  "offset": 0,
  "length": 65536,
  "encoding": "utf-8",
  "inline_max_bytes": 4096
}
```

`inline_max_bytes` is optional, default **4096**. Set to `0` to suppress inline content entirely.

Behavior:

1. Delegate `{"op":"read", path, offset, length, encoding}` to the target slave.
2. Receive the slave's structured result (path, bytes, encoding, content, eof) in driver Go code — not yet in the LLM context.
3. Decode `content` to raw bytes (UTF-8 passthrough or base64 decode).
4. Write the bytes to a driver-local cache file at `<cache_root>/file-cache/<sha256>` and register it in `FileRegistry` (`reg.RegisterFile`). The cache root follows the existing driver convention: `driver_defaults.audit_log_dir` if set, otherwise `~/.cache/multi-agent/<short_id>/`. Audit-log the registration as `register_read` with `peer_short_id` = the slave's short id, mirroring the user-file flow.
5. Return to the LLM:

   ```json
   {
     "task_id": "t-9",
     "target_display_name": "slave-a-...",
     "slave_path": "/abs/on/slave/data/in.csv",
     "size": 12345678,
     "encoding": "utf-8",
     "sha256": "abc...",
     "blob_handle": "sha256:abc...",
     "cache_path": "/home/me/.cache/multi-agent/d3f/file-cache/abc...",
     "eof": true,
     "content": "..."
   }
   ```

   `content` is included only when the returned byte count is `<= inline_max_bytes` AND the encoding is `utf-8`. Otherwise the field is omitted (binary, or too large). The LLM can read the bytes by calling Claude Code's native `Read` tool on `cache_path`, or hand `blob_handle` to a subsequent `write_slave_file` on another slave.

The 8 MiB cap on the slave side still applies — the driver tool reflects the slave's error verbatim if the caller asks for more than that in one go.

### `write_slave_file`

Input — exactly one of `content`, `source_blob`, `source_path` must be set:

```json
{
  "target_agent_id": "...",
  "target_display_name": "...",
  "path": "data/out.txt",
  "content": "hello\n",
  "source_blob": "sha256:abc...",
  "source_path": "/home/me/local.bin",
  "encoding": "utf-8",
  "mode": "overwrite",
  "mkdir": true,
  "offset": 0
}
```

Schema validation:

- Exactly one of `content` / `source_blob` / `source_path` is set; tool rejects the call otherwise.
- `content` inline is capped at the driver's `inline_max_bytes` (default 4 KiB) to prevent the LLM from being asked to carry large payloads as tool arguments. Larger writes must go through `source_blob` or `source_path`.
- `offset` is set iff `mode == "patch"`.

Behavior by source:

- **`content`**: tool forwards the slave-side prompt `{"op":"write", path, content, encoding, mode, mkdir, offset?}` directly.
- **`source_path`**: tool reads the local file (registers it in `FileRegistry` so it gets a sha256), then proceeds as `source_blob`.
- **`source_blob`**: tool looks up the absolute path in `FileRegistry.LookupBlob(sha)`, reads bytes, base64-encodes them (regardless of caller-specified `encoding` — binary is always safe), and sends the slave-side prompt with `encoding="base64"` and the base64 content. The slave-side executor decodes as usual.

The slave-side wire protocol is unchanged.

Return to the LLM:

```json
{
  "task_id": "t-10",
  "target_display_name": "slave-b-...",
  "slave_path": "/abs/on/slave/data/out.txt",
  "bytes_written": 4096,
  "mode": "patch",
  "offset": 65536,
  "source": "source_blob:sha256:abc..."
}
```

`source` echoes which input mode was used (`"content"`, `"source_blob:<sha>"`, or `"source_path:<abs>"`) so the LLM can chain operations without re-asking.

### `stat_slave_file`

Input:

```json
{
  "target_agent_id": "...",
  "target_display_name": "...",
  "path": "data/out.txt"
}
```

Delegates `{"op":"stat", path}`. Return is the slave's stat result as-is, with `task_id` and `target_display_name` added. Stat results are small; no caching layer.

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
- `multi-agent/internal/driver/slave_file_tools.go` — three MCP tool types (`readSlaveFileTool`, `writeSlaveFileTool`, `statSlaveFileTool`). `readSlaveFileTool` also writes to driver cache and registers in `FileRegistry`; `writeSlaveFileTool` dispatches by source mode (`content` / `source_blob` / `source_path`).
- `multi-agent/internal/driver/slave_file_tools_test.go` — driver tool tests.

### Driver Cache Path

Resolved once at driver startup (or lazily on first use):

- If `driver_defaults.audit_log_dir` is set, cache root is `<audit_log_dir>/file-cache/`.
- Otherwise, cache root is `~/.cache/multi-agent/<short_id>/file-cache/`.

Files inside the cache root are named by their sha256 (no extension). Re-fetching the same `(slave, path, content)` is idempotent because `FileRegistry.RegisterFile` is sha-keyed and the writer skips re-registration when the target file already exists.

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
- `write_slave_file` rejects when zero or more than one of `content`/`source_blob`/`source_path` is set.
- `write_slave_file` rejects `content` larger than `inline_max_bytes` cap.
- `read_slave_file` writes the bytes to the cache root, registers in `FileRegistry`, and includes `cache_path` + `blob_handle` in the LLM-facing return.
- `read_slave_file` includes inline `content` when size ≤ `inline_max_bytes` and `encoding=utf-8`; omits it when size > cap; omits it for base64 even if small.
- `read_slave_file` logs a `register_read` audit event with the slave's short id.
- `write_slave_file` with `source_blob` looks up the blob in `FileRegistry`, base64-encodes the bytes, and sends `encoding=base64` to the slave regardless of caller `encoding`.
- `write_slave_file` with `source_path` registers the local file and proceeds as `source_blob`.
- `write_slave_file` with `source_blob` errors clearly when the sha is not in `FileRegistry`.

`cmd/slave-agent/main_test.go`:

- `hasSkill(["chat","file"], "file") == true`, `hasSkill(["chat"], "file") == false`.

## Out of Scope

- **Directory ops** (`mkdir` standalone, `ls`, `rm`, `mv`). Can be added later as new ops; not required for read/write/stat flows.
- **Streaming / chunked transport.** The 8 MiB cap plus `offset`/`length` on read and `patch`+`offset` on write give callers explicit chunking — adequate for the current need. A future streaming op can be added if profile shows excessive task-channel overhead.
- **Per-path ACLs on the slave.** Trust model is "advertise the skill or don't"; same as `bash`.
- **Optimistic concurrency** (e.g. `if_etag`/`if_mtime` on write). The stateless executor leaves coordination to the orchestrator; can be revisited if conflict scenarios appear in practice.
- **Slave-side `/files/*` blob server.** Direct slave↔slave blob transport (bypassing the driver cache) is deferred. The driver-cached path already produces a `blob_handle` that other slaves can fetch via the existing peer-proxy `/files/blob/{sha}` — driver bandwidth is the only thing in the loop, which is acceptable until profiling says otherwise.
- **Cache eviction policy.** The driver cache grows monotonically for the lifetime of the driver process; sha-keyed naming means dedup is automatic but stale entries are not pruned. Operators can clear the cache root manually. A real LRU/TTL policy can be added when cache size becomes a problem.
