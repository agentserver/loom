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

## Tools

See `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` § 1 for full schemas.

Slave control helpers:

- `run_slave_bash`: sends explicit Bash code to a slave that advertises `bash`.
- `get_slave_claude_permissions`: reads a slave's Claude Code project permissions.
- `update_slave_claude_permissions`: patches a slave's Claude Code project permissions.
- `read_slave_file`: reads a path on a slave that advertises `file`. Bytes are cached in the driver's blob store and returned as a `sha256` handle plus `cache_path`; inline `content` is included only when ≤ 4 KiB and utf-8.
- `write_slave_file`: writes to a path on a slave that advertises `file`. Pass exactly one of `content` (inline, ≤ 4 KiB), `source_blob` (a sha256 returned from a prior tool), or `source_path` (a driver-local absolute path).
- `stat_slave_file`: stats a path on a slave that advertises `file`. Returns `exists:false` for missing paths rather than erroring.

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
  "timeout_sec": 60
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

## Notes

- The driver uses a tunnel-only request path: `/files/*` requests must come over the agentserver peer-proxy. Direct HTTP to a local port is rejected (`X-Agentserver-Peer-Short-Id` header required).
- `disable_uid_check: true` may be necessary on macOS or systems with unusual filesystem ownership; defaults are Linux-friendly.
