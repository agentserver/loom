---
name: scaffold-mcp-server
description: Use when building a stdio MCP server to register on a slave via register_mcp — generates the JSON-RPC protocol skeleton from a spec so you only hand-write business logic; safe to re-run (handler bodies preserved across regenerate).
---

# Scaffold MCP Server

## Overview

Stamp out a stdio JSON-RPC 2.0 MCP server from the same `spec.json` you will feed to `register_mcp`. The scaffolder owns the protocol region (initialize / notifications/initialized / tools/list / tools/call / error routing / main loop / stderr logging); you only fill in tool handler bodies between sentinel markers.

Two non-obvious wins:

1. **Single source of truth eliminates the `args_schema` ↔ `inputSchema` bug.** The spec uses `args_schema` (register_mcp's field); the scaffolder translates it to `inputSchema` (MCP protocol's field) in the generated TOOLS list. You cannot get them out of sync.
2. **Re-running scaffold is safe.** `--regenerate` rewrites only the protocol region; everything between `# @@scaffold:business:start <tool>` and `# @@scaffold:business:end` is preserved. Change schema, re-run, keep your code.

Stdlib-only output (json + sys + traceback). Runs on the default slave Python 3.10/3.11 without `pip install`.

## When to Use

- Building a new MCP server to ship via `register_mcp` / `register_slave_mcp`.
- Adding a tool to an existing scaffolded server (re-run with updated spec).
- Changing a tool's schema or description after edits to spec (re-run, body preserved).

When NOT to use:
- The slave can satisfy the task with an existing MCP. Don't generate a new server when you can call an existing tool.
- You need FastMCP / `mcp` SDK features (sampling, roots, elicitation, resources beyond tools). Scaffold output is tools-only by design.

## Quick Reference

```bash
# First time: write spec.json, scaffold, fill in handler, validate
python3 skills/scaffold-mcp-server/scripts/scaffold_mcp.py \
    --spec generated_mcp/my_tool/spec.json \
    --out generated_mcp/my_tool/v1.py
# ...edit handler body between business:start / business:end markers...

# Schema or description changed? Re-run scaffold; bodies stay.
python3 skills/scaffold-mcp-server/scripts/scaffold_mcp.py \
    --spec generated_mcp/my_tool/spec.json \
    --out generated_mcp/my_tool/v1.py

# Spec via stdin (for one-off shell pipelines)
echo '{"name":"foo",...}' | python3 .../scaffold_mcp.py --spec - --out server.py
```

## Spec Format

Subset of register_mcp's Spec — same file feeds both tools.

```json
{
  "name": "row_stats",
  "description": "Numeric row statistics",
  "version": 1,
  "tools": [
    {
      "name": "summarize_rows",
      "description": "Compute count/sum/mean of numeric rows.",
      "args_schema": {
        "type": "object",
        "properties": {"rows": {"type":"array","items":{"type":"number"}}},
        "required": ["rows"],
        "additionalProperties": false
      },
      "result_description": "Markdown table with count/sum/mean"
    }
  ],
  "allowed_packages": []
}
```

Validation rules: `name` matches `[a-z][a-z0-9_]{0,31}`; each tool's `name` is a Python identifier; `args_schema.type == "object"`; tool names unique; **`version` must be an integer** (e.g. `1`) — register_mcp rejects string versions like `"0.1.0"`.

## Generated File Structure

```python
#!/usr/bin/env python3
"""<server_name> MCP server (stdio JSON-RPC 2.0, stdlib-only)."""

# @@scaffold:proto:start
# ... imports, PROTOCOL_VERSION, TOOLS (json.loads of _TOOLS_JSON) ...
# @@scaffold:proto:end


def _handle_summarize_rows(args: dict) -> str:
    """<docstring built from spec, with arg list>"""
    # @@scaffold:business:start summarize_rows
    # TODO: implement summarize_rows
    raise NotImplementedError("summarize_rows not implemented")
    # @@scaffold:business:end


# @@scaffold:proto:start
# ... DISPATCH table, handle_rpc, main loop ...
# @@scaffold:proto:end
```

**Region rules:**

- Inside `# @@scaffold:proto:start ... # @@scaffold:proto:end`: scaffolder owns it. Never hand-edit. Regenerate to refresh.
- Inside `# @@scaffold:business:start <tool> ... # @@scaffold:business:end`: you own it. Scaffold preserves it byte-for-byte on regenerate.

## What the Protocol Region Gets Right (so you don't have to)

- `initialize` → returns `{protocolVersion, capabilities:{tools:{listChanged:false}}, serverInfo}`.
- `notifications/initialized` → returns **None** (no response on the wire). Easy to forget; biggest source of "server appears hung" bugs.
- `tools/list` → returns `TOOLS` (already in MCP camelCase `inputSchema` shape).
- `tools/call` → dispatches to `_handle_<tool>`, wraps result as `{content:[{type:"text",text:...}], isError:false}`. Exceptions become `isError:true` with traceback in `text`.
- Unknown tool → `isError:true` with clear message (not a JSON-RPC error).
- Unknown method → JSON-RPC `error:{code:-32601}`.
- Handler exception → JSON-RPC `error:{code:-32000, data:{trace}}` only at the outermost level (per-tool errors are content errors, not protocol errors).
- Malformed input line → silently skipped (loop never crashes).
- `stdout.flush()` after every response (otherwise pipe-buffered stdio hangs the caller).

## Workflow With register_mcp

1. Write `spec.json`.
2. `scaffold_mcp.py --spec spec.json --out generated_mcp/<name>/v1.py`.
3. Edit handler bodies between business markers.
4. Validate with `mcp-acceptance` (see that skill) — **do not skip; register_mcp does not check semantics**.
5. `register_slave_mcp` with the same `spec.json` and `source_path`.

## Common Mistakes

| Mistake | Fix |
|---|---|
| Hand-editing inside `# @@scaffold:proto:*` markers | Move logic to a `_handle_*` body or import-time top-level (which scaffold ignores). |
| Forgetting to re-run scaffold after editing `spec.json` | Always re-run; safe because handler bodies are preserved. |
| Different `inputSchema` in source vs `args_schema` in spec | Don't write `inputSchema` by hand — the scaffolder is the only writer. |
| Adding `import requests` to handler body | Slave default Python has no `requests`. Use `urllib.request` from stdlib, or declare in `spec.allowed_packages`. |
| Printing to stdout in handler | stdout is the protocol channel. Use `print(..., file=sys.stderr)` or `sys.stderr.write`. |

## Related

- `mcp-acceptance` — codified acceptance harness. ALWAYS run before `register_slave_mcp`.
- `multiagent` references `slave-skills.md` — full register_mcp spec.
