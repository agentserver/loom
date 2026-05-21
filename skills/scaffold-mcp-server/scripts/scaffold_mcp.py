#!/usr/bin/env python3
"""Scaffold a stdio JSON-RPC MCP server for the slave register_mcp pipeline.

Single source of truth: the spec JSON used here is the SAME shape that
register_mcp consumes. We translate spec.tools[*].args_schema (snake_case,
register's field) into the generated TOOLS list's inputSchema (camelCase,
MCP protocol's field). That removes the args_schema <-> inputSchema sync
bug that bites every hand-written server.

Generated code is stdlib-only (json, sys, traceback) so it runs on the
default slave Python 3.10/3.11 without pip installs.

Usage:
    python3 scaffold_mcp.py --spec spec.json --out generated_mcp/<name>/v1.py
    python3 scaffold_mcp.py --spec spec.json --out -      # stdout
    python3 scaffold_mcp.py --spec - --out path           # spec via stdin
    python3 scaffold_mcp.py --regenerate path             # rewrite protocol
                                                          # region in place,
                                                          # keep handler bodies

Region markers (preserved across --regenerate):
    # @@scaffold:proto:start ... # @@scaffold:proto:end
        owned by scaffold; do not hand-edit; regenerate to refresh.
    # @@scaffold:business:start <tool_name> ... # @@scaffold:business:end
        owned by you; scaffold never overwrites between these markers.

Spec format (subset of register_mcp Spec):
{
  "name": "row_stats",
  "description": "...",
  "version": 1,
  "tools": [
    {
      "name": "summarize_rows",
      "description": "...",
      "args_schema": {"type":"object","properties":{...},"required":[...]},
      "result_description": "..."
    }
  ],
  "allowed_packages": []
}
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any


PROTO_START = "# @@scaffold:proto:start"
PROTO_END = "# @@scaffold:proto:end"
BUSINESS_START_PREFIX = "# @@scaffold:business:start "
BUSINESS_END = "# @@scaffold:business:end"

NAME_RE = re.compile(r"^[a-z][a-z0-9_]{0,31}$")


def load_spec(path: str) -> dict[str, Any]:
    if path == "-":
        spec = json.load(sys.stdin)
    else:
        spec = json.loads(Path(path).read_text(encoding="utf-8"))
    validate_spec(spec)
    return spec


def validate_spec(spec: dict[str, Any]) -> None:
    if not isinstance(spec, dict):
        raise SystemExit("spec must be a JSON object")
    name = spec.get("name")
    if not isinstance(name, str) or not NAME_RE.match(name):
        raise SystemExit(
            f"spec.name {name!r} must match [a-z][a-z0-9_]{{0,31}}"
        )
    tools = spec.get("tools")
    if not isinstance(tools, list) or not tools:
        raise SystemExit("spec.tools must be a non-empty array")
    seen = set()
    for i, t in enumerate(tools):
        if not isinstance(t, dict):
            raise SystemExit(f"spec.tools[{i}] must be an object")
        tn = t.get("name")
        if not isinstance(tn, str) or not re.match(r"^[a-zA-Z_][\w]*$", tn):
            raise SystemExit(
                f"spec.tools[{i}].name {tn!r} must be a python identifier"
            )
        if tn in seen:
            raise SystemExit(f"duplicate tool name {tn!r}")
        seen.add(tn)
        if not isinstance(t.get("description", ""), str):
            raise SystemExit(f"spec.tools[{i}].description must be a string")
        sch = t.get("args_schema") or t.get("inputSchema")
        if not isinstance(sch, dict):
            raise SystemExit(
                f"spec.tools[{i}].args_schema must be a JSON object"
            )
        if sch.get("type") != "object":
            raise SystemExit(
                f"spec.tools[{i}].args_schema.type must be \"object\""
            )


def render(spec: dict[str, Any], existing_bodies: dict[str, str] | None = None) -> str:
    """Render a complete server. existing_bodies keeps user code on regenerate."""
    bodies = existing_bodies or {}
    name = spec["name"]
    # NOTE: register_mcp requires integer version (e.g. 1). The scaffolder
    # accepts either int or string but emits json.dumps(version), so an int
    # spec stays an int Python literal in the generated source.
    version = spec.get("version", 1)
    server_desc = spec.get("description", "")
    tools = spec["tools"]

    # TOOLS list: translate args_schema -> inputSchema once, here.
    tools_decl: list[dict[str, Any]] = []
    for t in tools:
        sch = t.get("args_schema") or t.get("inputSchema") or {}
        tools_decl.append({
            "name": t["name"],
            "description": t.get("description", ""),
            "inputSchema": sch,
        })
    # JSON is parsed at startup so booleans/nulls stay valid (avoids the
    # "false is not defined" trap when embedding JSON as Python literals).
    tools_json_str = json.dumps(tools_decl, ensure_ascii=False, indent=2)
    # Triple-quoted raw string; protect any """ inside descriptions.
    if '"""' in tools_json_str:
        raise SystemExit('tool description/schema contains """; please remove')

    header = (
        f'#!/usr/bin/env python3\n'
        f'"""{name} MCP server (stdio JSON-RPC 2.0, stdlib-only).\n\n'
        f'Generated by scaffold-mcp-server. Hand-edit ONLY inside\n'
        f'`# @@scaffold:business:start <tool>` ... `# @@scaffold:business:end`\n'
        f'blocks; the protocol region is owned by the scaffolder and will be\n'
        f'rewritten by --regenerate.\n\n'
        f'Server: {name}\n'
        f'Description: {server_desc}\n'
        f'"""\n'
    )

    proto = f'''{PROTO_START}
import json
import sys
import traceback

PROTOCOL_VERSION = "2024-11-05"
SERVER_NAME = {json.dumps(name)}
SERVER_VERSION = {json.dumps(version)}

_TOOLS_JSON = r"""
{tools_json_str}
"""
TOOLS = json.loads(_TOOLS_JSON)
{PROTO_END}
'''

    # Tool dispatch table
    dispatch_lines = [f"    {json.dumps(t['name'])}: _handle_{t['name']},"
                       for t in tools]
    dispatch = "\n".join(dispatch_lines)

    # Per-tool handler skeletons, preserving any existing body
    handler_blocks = []
    for t in tools:
        tn = t["name"]
        sch = t.get("args_schema") or t.get("inputSchema") or {}
        required = sch.get("required") or []
        props = sch.get("properties") or {}
        # Build a friendly docstring listing the schema.
        arg_doc = []
        for k, v in props.items():
            req = " (required)" if k in required else ""
            arg_doc.append(f"        {k}{req}: {v.get('description', v.get('type', ''))}")
        doc = (
            f'    """{t.get("description","")}\n\n'
            f'    Args (validated by MCP client against inputSchema):\n'
            + ("\n".join(arg_doc) if arg_doc else "        (none)")
            + f'\n\n    Returns: str — markdown/plain text shown to the user.\n'
            f'\n    {t.get("result_description","")}\n'
            f'    """\n'
        )
        body = bodies.get(tn)
        if body is None:
            body = (
                f'    # TODO: implement {tn}\n'
                f'    raise NotImplementedError({json.dumps(tn + " not implemented")})\n'
            )
        else:
            # Ensure trailing newline so the end marker sits on its own line.
            if not body.endswith("\n"):
                body += "\n"
        handler_blocks.append(
            f'def _handle_{tn}(args: dict) -> str:\n'
            f'{doc}'
            f'    {BUSINESS_START_PREFIX}{tn}\n'
            f'{body}'
            f'    {BUSINESS_END}\n'
        )

    handlers = "\n\n".join(handler_blocks)

    dispatch_block = f'''{PROTO_START}
DISPATCH = {{
{dispatch}
}}


def _result_text(text: str) -> dict:
    return {{"content": [{{"type": "text", "text": text}}], "isError": False}}


def _result_error(msg: str) -> dict:
    return {{"content": [{{"type": "text", "text": msg}}], "isError": True}}


def handle_rpc(req: dict):
    method = req.get("method")
    rid = req.get("id")
    params = req.get("params") or {{}}
    if method == "initialize":
        return {{"jsonrpc": "2.0", "id": rid, "result": {{
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {{"tools": {{"listChanged": False}}}},
            "serverInfo": {{"name": SERVER_NAME, "version": SERVER_VERSION}},
        }}}}
    if method == "notifications/initialized":
        # Notifications MUST NOT return a response.
        return None
    if method == "tools/list":
        return {{"jsonrpc": "2.0", "id": rid, "result": {{"tools": TOOLS}}}}
    if method == "tools/call":
        tool = params.get("name")
        fn = DISPATCH.get(tool)
        if fn is None:
            return {{"jsonrpc": "2.0", "id": rid,
                    "result": _result_error(f"unknown tool: {{tool}}")}}
        try:
            text = fn(params.get("arguments") or {{}})
            return {{"jsonrpc": "2.0", "id": rid, "result": _result_text(text)}}
        except Exception as e:
            return {{"jsonrpc": "2.0", "id": rid, "result": _result_error(
                f"error: {{e}}\\n{{traceback.format_exc()}}"
            )}}
    if method == "ping":
        return {{"jsonrpc": "2.0", "id": rid, "result": {{}}}}
    return {{"jsonrpc": "2.0", "id": rid,
            "error": {{"code": -32601, "message": f"method not found: {{method}}"}}}}


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception:
            # Malformed input is ignored; never crash the loop.
            continue
        try:
            resp = handle_rpc(req)
        except Exception as e:
            resp = {{"jsonrpc": "2.0", "id": req.get("id"), "error": {{
                "code": -32000, "message": str(e),
                "data": {{"trace": traceback.format_exc()}},
            }}}}
        if resp is not None:
            sys.stdout.write(json.dumps(resp, ensure_ascii=False) + "\\n")
            sys.stdout.flush()


if __name__ == "__main__":
    main()
{PROTO_END}
'''

    return header + "\n" + proto + "\n\n" + handlers + "\n\n" + dispatch_block


def parse_existing(text: str) -> dict[str, str]:
    """Extract handler bodies from a previously generated file."""
    bodies: dict[str, str] = {}
    i = 0
    lines = text.splitlines(keepends=True)
    while i < len(lines):
        line = lines[i]
        stripped = line.lstrip()
        if stripped.startswith(BUSINESS_START_PREFIX):
            tool = stripped[len(BUSINESS_START_PREFIX):].strip()
            j = i + 1
            buf: list[str] = []
            while j < len(lines):
                inner = lines[j].lstrip()
                if inner.startswith(BUSINESS_END):
                    break
                buf.append(lines[j])
                j += 1
            bodies[tool] = "".join(buf)
            i = j + 1
            continue
        i += 1
    return bodies


def cmd_generate(args: argparse.Namespace) -> int:
    spec = load_spec(args.spec)
    existing: dict[str, str] = {}
    if args.out != "-" and Path(args.out).exists():
        try:
            existing = parse_existing(Path(args.out).read_text(encoding="utf-8"))
        except Exception:
            existing = {}
    output = render(spec, existing_bodies=existing)
    if args.out == "-":
        sys.stdout.write(output)
    else:
        out_path = Path(args.out)
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(output, encoding="utf-8")
        print(f"wrote {out_path} ({len(output)} bytes, "
              f"{len(spec['tools'])} tools, preserved={len(existing)})",
              file=sys.stderr)
    return 0


def cmd_regenerate(args: argparse.Namespace) -> int:
    path = Path(args.target)
    if not path.exists():
        raise SystemExit(f"{path} does not exist")
    text = path.read_text(encoding="utf-8")
    bodies = parse_existing(text)
    # Spec for regeneration must be supplied separately; we cannot recover
    # descriptions from generated code reliably.
    if not args.spec:
        raise SystemExit("--regenerate requires --spec to know tool metadata")
    spec = load_spec(args.spec)
    out = render(spec, existing_bodies=bodies)
    path.write_text(out, encoding="utf-8")
    print(f"regenerated {path} (preserved {len(bodies)} handler bodies)",
          file=sys.stderr)
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n", 1)[0])
    p.add_argument("--spec", help="path to spec.json, or '-' for stdin")
    p.add_argument("--out", default="-",
                   help="output path, or '-' for stdout (default)")
    p.add_argument("--regenerate", dest="target",
                   help="rewrite proto region in target file, preserve "
                        "handler bodies; requires --spec")
    args = p.parse_args()
    if args.target:
        return cmd_regenerate(args)
    if not args.spec:
        p.error("--spec is required (or use --regenerate)")
    return cmd_generate(args)


if __name__ == "__main__":
    sys.exit(main())
