#!/usr/bin/env python3
"""Fixture stdio MCP server that replays the `expected` / `expected_error`
side of a cases.jsonl. Used exclusively by the WT-1-acceptance-golden
test matrix and the 5-family smoke loop — it lets the runner be
exercised end-to-end without the real per-tool MCP servers (which live
under generated_mcp/ and are owned by Phase 2).

Behaviour:

- Load the cases.jsonl named by --cases.
- Advertise exactly one tool: the (single) value of `case["tool"]`
  across the file (file invariant from §13 §2.5 #1; matches what the
  runner enforces).
- On each tools/call, find the case whose `input` deep-equals the
  incoming `arguments` and reply with either:
    * `{"structuredContent": <expected>, "content": [], "isError": false}`
      if the case has `expected`, or
    * `{"content": [{"type": "text", "text": <expected_error>}], "isError": true}`
      if the case has `expected_error`.
- On no match: reply isError=true with text
  "echo-oracle: no matching case for arguments=<json>". The runner
  surfaces this as a normal case failure.

Fixture-only switch:

- `--force-error-text <str>`: ignore the loaded cases entirely and
  reply isError=true with the given text for every tools/call. Used
  exclusively by `test_expected_error_case_sensitive` to assert that
  the runner's `expected_error` matching is case-sensitive substring.
  This flag is intentionally undocumented in SKILL.md; real workloads
  should never use it.

This file is not part of the published skill surface; it lives in
scripts/ next to mcp_acceptance.py for proximity. Filename starts with
"_" so future automation that scans scripts/*.py treats it as private.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any


def load_cases(path: Path) -> list[dict[str, Any]]:
    """Load cases.jsonl with the same tolerance as the runner.

    No security checks — this fixture server runs against cases the
    runner has already approved.
    """
    text = path.read_text(encoding="utf-8")
    cases: list[dict[str, Any]] = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        cases.append(json.loads(line))
    return cases


def reply_result(rid: int | str | None, result: dict[str, Any]) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": rid, "result": result}


def reply_error(rid: int | str | None, code: int, message: str) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": rid, "error": {"code": code, "message": message}}


def match_case(cases: list[dict[str, Any]], arguments: Any) -> dict[str, Any] | None:
    for c in cases:
        if c.get("input") == arguments:
            return c
    return None


def build_tool_call_response(
    rid: Any,
    matched: dict[str, Any] | None,
    arguments: Any,
    force_error_text: str | None,
) -> dict[str, Any]:
    if force_error_text is not None:
        # Fixture override — ignore cases entirely.
        return reply_result(rid, {
            "content": [{"type": "text", "text": force_error_text}],
            "isError": True,
        })

    if matched is None:
        return reply_result(rid, {
            "content": [{
                "type": "text",
                "text": (
                    "echo-oracle: no matching case for arguments="
                    + json.dumps(arguments, sort_keys=True, ensure_ascii=False)
                ),
            }],
            "isError": True,
        })

    if "expected_error" in matched:
        return reply_result(rid, {
            "content": [{"type": "text", "text": str(matched["expected_error"])}],
            "isError": True,
        })

    # Reply with structuredContent for the runner's preferred extraction
    # path. We also emit a text serialization so a runner that fell back
    # to the text path (e.g. a future MCP server downgrade) would see the
    # same JSON.
    expected = matched.get("expected")
    return reply_result(rid, {
        "content": [{
            "type": "text",
            "text": json.dumps(expected, sort_keys=True, ensure_ascii=False),
        }],
        "structuredContent": expected,
        "isError": False,
    })


def serve(cases: list[dict[str, Any]], tool_name: str,
          force_error_text: str | None) -> None:
    out = sys.stdout
    for raw in sys.stdin:
        line = raw.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            # Drop malformed; a real client would never send this.
            continue

        method = msg.get("method")
        rid = msg.get("id")

        if method == "initialize":
            out.write(json.dumps(reply_result(rid, {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "echo-oracle", "version": "0.1"},
            })) + "\n")
            out.flush()
            continue

        if method == "notifications/initialized":
            # Notification — no response.
            continue

        if method == "tools/list":
            out.write(json.dumps(reply_result(rid, {
                "tools": [{
                    "name": tool_name,
                    "description": "echo-oracle replay tool (fixture only)",
                    "inputSchema": {"type": "object"},
                }],
            })) + "\n")
            out.flush()
            continue

        if method == "tools/call":
            params = msg.get("params") or {}
            arguments = params.get("arguments")
            matched = match_case(cases, arguments)
            out.write(json.dumps(build_tool_call_response(
                rid, matched, arguments, force_error_text,
            )) + "\n")
            out.flush()
            continue

        out.write(json.dumps(reply_error(rid, -32601, f"method not found: {method}")) + "\n")
        out.flush()


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__ or "")
    p.add_argument("--cases", required=True, help="path to cases.jsonl to replay")
    p.add_argument("--force-error-text", default=None,
                   help="fixture-only: ignore cases and reply isError=true with this text on every tools/call")
    args = p.parse_args()

    cases_path = Path(args.cases).resolve()
    cases = load_cases(cases_path)
    if not cases:
        print(f"echo-oracle: no cases loaded from {cases_path}", file=sys.stderr)
        return 2

    tools = {c.get("tool") for c in cases if c.get("tool")}
    if len(tools) != 1:
        print(f"echo-oracle: cases must declare exactly one tool, got {sorted(tools)}",
              file=sys.stderr)
        return 2
    tool_name = next(iter(tools))

    serve(cases, tool_name, args.force_error_text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
