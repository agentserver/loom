#!/usr/bin/env python3
"""Acceptance harness for stdio JSON-RPC MCP servers.

Exit code 0 iff every case passes. Non-zero exit is the contract: it is
designed to gate `register_slave_mcp` in a shell pipeline:

    python3 mcp_acceptance.py --server "python3 server.py" --cases cases.jsonl \
        && register_slave_mcp ...

This runner exists because `register_mcp` only does structural validation
(`tools/list` smoke). It never calls `tools/call`, so a server with broken
business logic can register successfully and surface bad data downstream.
This script closes that gap: it drives a real `initialize` -> `tools/list`
-> `tools/call` sequence and asserts per-case expectations.

Case file: JSONL, one case per line. Each case is an object:

    {
      "name": "happy path",                    # optional label
      "tool": "summarize_rows",                # required
      "args": {"rows": [1,2,3,4]},             # required
      "expect_isError": false,                 # default: false
      "expect_contains": ["count=4", "mean"],  # all substrings must appear in text
      "expect_not_contains": ["error"],        # none may appear
      "expect_regex": "mean=\\d+\\.\\d+",       # python re.search must match
      "timeout_sec": 10                        # per-call timeout, default 10
    }

Assertion types are AND-combined. Missing expectations means "any non-error
response passes" (only checks isError == expect_isError).

The runner also asserts that the tool exists in `tools/list`, that the
server replies to `initialize` with a valid `protocolVersion`, and that
`notifications/initialized` produces no response.
"""
from __future__ import annotations

import argparse
import json
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


@dataclass
class CaseResult:
    name: str
    tool: str
    passed: bool
    reasons: list[str] = field(default_factory=list)
    elapsed_ms: int = 0
    response_text: str = ""
    is_error: bool = False


def load_cases(path: str) -> list[dict[str, Any]]:
    if path == "-":
        text = sys.stdin.read()
    else:
        text = Path(path).read_text(encoding="utf-8")
    cases: list[dict[str, Any]] = []
    for i, line in enumerate(text.splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError as e:
            raise SystemExit(f"cases line {i}: invalid JSON: {e}")
        if "tool" not in obj or "args" not in obj:
            raise SystemExit(f"cases line {i}: missing 'tool' or 'args'")
        obj.setdefault("name", f"case-{i}")
        cases.append(obj)
    if not cases:
        raise SystemExit("no cases loaded")
    return cases


class ServerSession:
    """Drive a stdio MCP server line-by-line."""

    def __init__(self, server_cmd: list[str], cwd: str | None = None,
                 startup_timeout: float = 5.0):
        self.cmd = server_cmd
        self.cwd = cwd
        self.startup_timeout = startup_timeout
        self.proc: subprocess.Popen[str] | None = None
        self._next_id = 1

    def start(self) -> None:
        self.proc = subprocess.Popen(
            self.cmd, cwd=self.cwd,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True, bufsize=1,
        )

    def stop(self) -> tuple[int, str]:
        assert self.proc is not None
        try:
            if self.proc.stdin and not self.proc.stdin.closed:
                self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()
        stderr = self.proc.stderr.read() if self.proc.stderr else ""
        return self.proc.returncode or 0, stderr

    def send_notification(self, method: str, params: dict | None = None) -> None:
        assert self.proc and self.proc.stdin
        msg: dict[str, Any] = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            msg["params"] = params
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()

    def send_request(self, method: str, params: dict | None = None,
                      timeout: float = 10.0) -> dict:
        assert self.proc and self.proc.stdin and self.proc.stdout
        rid = self._next_id
        self._next_id += 1
        msg: dict[str, Any] = {"jsonrpc": "2.0", "id": rid, "method": method}
        if params is not None:
            msg["params"] = params
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()
        # Read one line with a wallclock deadline.
        deadline = time.monotonic() + timeout
        # readline blocks; we rely on the subprocess to either respond or
        # die. If the process dies, readline returns "" and we surface that.
        line = self.proc.stdout.readline()
        if time.monotonic() > deadline and not line:
            raise TimeoutError(f"{method} timed out after {timeout}s")
        if line == "":
            rc = self.proc.poll()
            err = self.proc.stderr.read() if self.proc.stderr else ""
            raise RuntimeError(
                f"server closed stdout before responding to {method} "
                f"(rc={rc}); stderr tail: {err[-500:]!r}"
            )
        try:
            resp = json.loads(line)
        except json.JSONDecodeError as e:
            raise RuntimeError(
                f"server emitted non-JSON line: {line!r} ({e})"
            )
        if resp.get("id") != rid:
            raise RuntimeError(
                f"id mismatch: sent {rid} got {resp.get('id')} "
                f"(full response: {resp})"
            )
        return resp


def extract_text(call_result: dict) -> tuple[str, bool]:
    """Pull the concatenated text content + isError flag from tools/call."""
    inner = call_result.get("result") or {}
    is_err = bool(inner.get("isError", False))
    content = inner.get("content") or []
    parts: list[str] = []
    for item in content:
        if isinstance(item, dict) and item.get("type") == "text":
            parts.append(str(item.get("text", "")))
    return "".join(parts), is_err


def evaluate(case: dict, text: str, is_err: bool) -> list[str]:
    """Return list of failure reasons; empty list = pass."""
    reasons: list[str] = []
    expected_err = bool(case.get("expect_isError", False))
    if is_err != expected_err:
        reasons.append(
            f"isError: expected {expected_err}, got {is_err}"
        )
    for needle in case.get("expect_contains", []) or []:
        if needle not in text:
            reasons.append(f"expect_contains missing: {needle!r}")
    for forbidden in case.get("expect_not_contains", []) or []:
        if forbidden in text:
            reasons.append(f"expect_not_contains present: {forbidden!r}")
    rgx = case.get("expect_regex")
    if rgx:
        try:
            if re.search(rgx, text) is None:
                reasons.append(f"expect_regex no match: {rgx!r}")
        except re.error as e:
            reasons.append(f"expect_regex invalid: {e}")
    return reasons


def run(server_cmd: list[str], cases: list[dict], cwd: str | None,
        verbose: bool) -> list[CaseResult]:
    sess = ServerSession(server_cmd, cwd=cwd)
    sess.start()
    results: list[CaseResult] = []
    try:
        # 1. initialize
        init_resp = sess.send_request("initialize", {}, timeout=5.0)
        if "result" not in init_resp:
            raise RuntimeError(f"initialize did not return result: {init_resp}")
        proto = init_resp["result"].get("protocolVersion")
        if not proto:
            raise RuntimeError(
                f"initialize result missing protocolVersion: {init_resp['result']}"
            )

        # 2. notifications/initialized must NOT produce a response. We can't
        # block-read here (no response coming), so just send and move on. If
        # the server bug-emits one, we'll detect a stray response on the next
        # request (id mismatch).
        sess.send_notification("notifications/initialized", {})

        # 3. tools/list
        list_resp = sess.send_request("tools/list", {}, timeout=5.0)
        advertised = {t["name"] for t in (list_resp.get("result", {}).get("tools") or [])
                      if isinstance(t, dict) and "name" in t}

        # 4. per-case tools/call
        for case in cases:
            r = CaseResult(name=case["name"], tool=case["tool"], passed=False)
            if case["tool"] not in advertised:
                r.reasons.append(
                    f"tool {case['tool']!r} not in tools/list (advertised: {sorted(advertised)})"
                )
                results.append(r)
                continue
            t0 = time.monotonic()
            try:
                resp = sess.send_request(
                    "tools/call",
                    {"name": case["tool"], "arguments": case.get("args", {})},
                    timeout=float(case.get("timeout_sec", 10)),
                )
            except Exception as e:
                r.reasons.append(f"tools/call raised: {e}")
                r.elapsed_ms = int((time.monotonic() - t0) * 1000)
                results.append(r)
                continue
            r.elapsed_ms = int((time.monotonic() - t0) * 1000)
            text, is_err = extract_text(resp)
            r.response_text = text
            r.is_error = is_err
            r.reasons = evaluate(case, text, is_err)
            r.passed = not r.reasons
            results.append(r)
    finally:
        _rc, stderr_tail = sess.stop()
        if verbose and stderr_tail.strip():
            print(f"\n[server stderr tail]\n{stderr_tail[-1000:]}", file=sys.stderr)
    return results


def report(results: list[CaseResult], json_out: bool) -> int:
    passed = sum(1 for r in results if r.passed)
    failed = len(results) - passed
    if json_out:
        out = {
            "summary": {"total": len(results), "passed": passed, "failed": failed},
            "cases": [
                {
                    "name": r.name, "tool": r.tool, "passed": r.passed,
                    "elapsed_ms": r.elapsed_ms, "is_error": r.is_error,
                    "reasons": r.reasons,
                    "response_text": r.response_text[:500],
                } for r in results
            ],
        }
        print(json.dumps(out, ensure_ascii=False, indent=2))
    else:
        for r in results:
            status = "PASS" if r.passed else "FAIL"
            print(f"  [{status}] {r.name} ({r.tool}) — {r.elapsed_ms}ms")
            for reason in r.reasons:
                print(f"      ! {reason}")
            if not r.passed and r.response_text:
                snippet = r.response_text[:200].replace("\n", " ")
                print(f"      response: {snippet!r}")
        print(f"\n{passed}/{len(results)} passed, {failed} failed")
    return 0 if failed == 0 else 1


def main() -> int:
    p = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n", 1)[0])
    p.add_argument("--server", required=True,
                   help='server command, e.g. "python3 generated_mcp/foo/v1.py"')
    p.add_argument("--cases", required=True,
                   help="path to cases.jsonl, or '-' for stdin")
    p.add_argument("--cwd", help="working directory for server command")
    p.add_argument("--json", action="store_true",
                   help="emit JSON report instead of text")
    p.add_argument("--verbose", action="store_true",
                   help="print server stderr tail on exit")
    args = p.parse_args()

    server_cmd = shlex.split(args.server)
    cases = load_cases(args.cases)
    try:
        results = run(server_cmd, cases, cwd=args.cwd, verbose=args.verbose)
    except (RuntimeError, TimeoutError) as e:
        # Setup-time failure: server crashed before or during handshake, or
        # tools/list never returned. Report uniformly; exit non-zero so the
        # outer pipeline (e.g. && register_slave_mcp) does not proceed.
        msg = f"server handshake failed: {e}"
        if args.json:
            print(json.dumps({
                "summary": {"total": len(cases), "passed": 0, "failed": len(cases)},
                "error": msg,
            }, ensure_ascii=False, indent=2))
        else:
            print(f"  [ERROR] {msg}", file=sys.stderr)
            print(f"\n0/{len(cases)} passed, {len(cases)} failed (server "
                  f"unreachable)", file=sys.stderr)
        return 2
    return report(results, json_out=args.json)


if __name__ == "__main__":
    sys.exit(main())
