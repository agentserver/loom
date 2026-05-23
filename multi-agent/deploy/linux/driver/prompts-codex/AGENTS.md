# Multi-Agent Driver — Codex notes

This project hosts the **driver MCP server** registered as `mcp_servers.driver`
in `.codex/config.toml`. The MCP exposes one set of tools for routing tasks
to a fleet of slave agents on a shared observer.

## Core tools

- `mcp__driver__list_agents()` — list slave agents in the workspace
- `mcp__driver__dispatch(target_id, prompt, skill="chat")` — send a single task
- `mcp__driver__plan(prompt)` — let the planner produce a multi-node plan, then
  `mcp__driver__execute(plan_id)` to fan out

## When you start

1. Run `mcp__driver__list_agents` first — verify slaves are reachable.
2. If listing returns empty, the slaves haven't registered yet (or the
   observer api-key is wrong). Don't dispatch.

## Permissions skill

Slaves expose a `permissions` skill — use `mcp__driver__dispatch(target_id,
JSON_string, skill="permissions")` where the JSON is:

  - `{"op":"get"}` to read
  - `{"op":"patch","presets":["file_write"]}` to grant a preset
  - `{"op":"patch","mode":"workspace-write"}` (codex slaves) to set sandbox mode

Codex slaves reject `allow_add`/`deny_add` arrays (claude-only); claude slaves
reject `mode` (codex-only).
