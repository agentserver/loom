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

4. Restart Claude Code. The driver's six tools (`list_agents`, `submit_task`, `get_task`, `wait_task`, `tail_subtasks`, `cancel_task`) appear under the `driver/` namespace.

## Tools

See `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` § 1 for full schemas.

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
