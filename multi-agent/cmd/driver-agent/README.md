# driver-agent

Bridges Claude Code (CLI or VS Code extension) to a multi-agent workspace. Runs as a stdio MCP server that Claude Code spawns; in the same process, holds an agentserver tunnel as a regular workspace agent so master and slaves can fetch files the user mentions in Claude.

## First-time setup

1. Copy `config.example.yaml` to `~/.config/multi-agent/driver.yaml` and edit `server.url` + `discovery.display_name` for your deployment.
2. Run the device-flow registration once:

   ```
   driver-agent register --config ~/.config/multi-agent/driver.yaml
   ```

   Open the printed URL in a browser, complete the consent prompt; credentials are written back to the yaml file.

3. Tell Claude Code about the driver. In `~/.claude.json` (CLI) or the equivalent VS Code extension settings, add an MCP server entry:

   ```json
   {
     "mcpServers": {
       "driver": {
         "command": "/usr/local/bin/driver-agent",
         "args": ["serve-mcp", "--config", "/home/me/.config/multi-agent/driver.yaml"]
       }
     }
   }
   ```

4. Restart Claude Code. The driver's tools appear under the `driver/` namespace.

## serve-daemon

`serve-daemon` is the long-lived commander web entry mode. It reuses the same
driver config and registration token, dials the observer WebSocket endpoint, and
serves a loopback HTTP debug API:

```
driver-agent serve-daemon --config ~/.config/multi-agent/driver.yaml [--listen host:port]
```

Run `driver-agent register --config ...` first; the daemon uses
`credentials.proxy_token` as its WebSocket bearer token.

Use an `https://` observer URL outside loopback/debug deployments. `http://`
observer URLs are converted to `ws://`, which sends `credentials.proxy_token`
without TLS.

The HTTP API requires `Authorization: Bearer <credentials.proxy_token>` on every
request. This prevents browser CSRF and DNS-rebinding pages from driving the
local agent just because they can reach a loopback port.

By default the HTTP debug API binds `127.0.0.1:0` so multiple daemons can run on
one host without port conflicts. Use `--listen 127.0.0.1:9099` only when a
stable curl/debug port is required:

```
curl -H "Authorization: Bearer $(yq -r .credentials.proxy_token ~/.config/multi-agent/driver.yaml)" \
  http://127.0.0.1:9099/sessions
```

Binding `0.0.0.0` prints a warning because the bearer-protected debug API still
becomes reachable from the network.

`serve-daemon` coexists with `serve-mcp`: same config file, different transport.
Release builds should inject the daemon version with
`-ldflags "-X main.driverVersion=vX.Y.Z"` so observer can report the actual
driver build in register payloads.

## Tools

See `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` § 1 for full schemas.

Slave control helpers:

- `run_slave_bash`: sends explicit Bash code to a slave that advertises `bash`.
- `get_slave_claude_permissions`: reads a slave's Claude Code project permissions.
- `update_slave_claude_permissions`: patches a slave's Claude Code project permissions.
- `read_slave_file`: reads a path on a slave that advertises `file`. Bytes are cached in the driver's blob store and returned as a `sha256` handle plus `cache_path`; inline `content` is included only when ≤ 4 KiB and utf-8.
- `write_slave_file`: writes to a path on a slave that advertises `file`. Pass exactly one of `content` (inline, ≤ 4 KiB), `source_blob` (a sha256 returned from a prior tool), or `source_path` (a driver-local absolute path).
- `stat_slave_file`: stats a path on a slave that advertises `file`. Returns `exists:false` for missing paths rather than erroring.

Shell helpers return a `task_id` immediately by default. Pass `"wait": true`
only when you intentionally want the MCP call to block until completion; for
long commands, keep the default and poll with `wait_task`.

If a long helper call is interrupted or an outer MCP client times out,
use `list_driver_tasks` to recover recently created task IDs. The driver also
appends each delegated task to `driver-tasks.jsonl` as soon as agentserver
returns the `task_id`, before waiting for completion.

Permission tools currently submit ordinary tasks with `skill="claude_permissions"` because the current agentserver does not expose the needed custom-agent peer/control proxy. The task is handled by native Go code in `slave-agent`; it is not a request for the slave's Claude Code process to edit its own permissions. Future work should migrate this control operation to a dedicated agentserver control channel.

Example permission patch:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "allow_presets": ["python", "curl", "file_write"]
}
```

Example Bash execution:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "script": "python3 - <<'PY'\nprint('ok')\nPY",
  "timeout_sec": 60,
  "wait": true
}
```

Example chained slave-A → slave-B file transfer (LLM never carries bytes):

```json
{"target_display_name":"slave-a","path":"data/big.bin","encoding":"base64"}
```
→ returns `{"sha256":"abc...","blob_handle":"sha256:abc...","cache_path":"..."}`

```json
{"target_display_name":"slave-b","path":"incoming/big.bin","source_blob":"sha256:abc...","mkdir":true}
```

## Audit log

Every file registration, fetch, and PUT is recorded as JSONL at `~/.cache/multi-agent/<short_id>/audit.log` (overridable via `driver_defaults.audit_log_dir`). Lines look like:

```json
{"ts":"...","event":"register_read","path":"/home/me/x.csv","sha256":"abc","bytes":12345}
{"ts":"...","event":"fetch_blob","path":"/home/me/x.csv","sha256":"abc","bytes":12345,"task_id":"t-9","peer_short_id":"slave-1"}
{"ts":"...","event":"put_blob","path":"/home/me/out.parquet","sha256":"def","bytes":54321,"task_id":"t-9","peer_short_id":"slave-2","overwrite":true}
```

## Task journal

Every driver-created delegated task is recorded as JSONL at
`~/.cache/multi-agent/<short_id>/driver-tasks.jsonl` beside `audit.log`
(overridable via `driver_defaults.audit_log_dir`). Each line records metadata
such as `tool`, `task_id`, `target_id`, `target_display_name`, `skill`,
`status`, `wait`, and `timeout_sec`.

The journal does not store prompts, scripts, environment variables, file
contents, tokens, or credentials.

Use the MCP tool `list_driver_tasks` to read recent records:

```json
{"limit": 20}
```

Filter to one task ID when needed:

```json
{"task_id": "task-abc"}
```

## Notes

- The driver uses a tunnel-only request path: `/files/*` requests must come over the agentserver peer-proxy. Direct HTTP to a local port is rejected (`X-Agentserver-Peer-Short-Id` header required).
- `disable_uid_check: true` may be necessary on macOS or systems with unusual filesystem ownership; defaults are Linux-friendly.
