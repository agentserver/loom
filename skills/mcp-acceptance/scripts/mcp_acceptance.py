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

Two case-file shapes are accepted; the runner auto-detects per file.

Legacy shape (pre-WT-1-acceptance-golden, kept for backward compat):

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

Golden shape (Phase-0 task-families output, §13 §2.3, B3 contract):

    {
      "name": "happy_path",          # required, snake_case
      "tool": "csv_profile",         # required, file invariant: one tool per file
      "input": {"path": "..."},      # required, keys must be in TOOL_ALLOWED_FIELDS
      "expected": {"rows": 5}        # OR expected_error; exactly one
    }

Mode selection per file:
  - All lines have "input" and exactly one of "expected"/"expected_error" -> golden
  - All lines have "args" (legacy)                                       -> legacy
  - Mixed                                                                -> exit 2

Golden mode adds 6 security checks (spec §3 (a)-(f)): --cases path
traversal, in-case input.path traversal (with /tmp/ strict-prefix
carve-out for negative-case fixtures per §13 §2.3), case-sensitive
expected_error substring matching, ablation env bypass logging, unknown
top-level field rejection, and unknown input field rejection against
a per-tool allowlist mirrored from
multi-agent/tests/eval/golden/golden_schema_test.go:toolAllowedFields.

The runner also asserts that the tool exists in `tools/list`, that the
server replies to `initialize` with a valid `protocolVersion`, and that
`notifications/initialized` produces no response.

Ablation: when `LOOM_ABLATION_NOACCEPTANCEGATE=1` is set in the env,
golden-mode runs do all security pre-flight checks, then skip the MCP
work entirely and exit 0 with a single stderr log line. Phase-2 CLI
binder (WT-2-flag-integration) is expected to export this var when
`--ablation NoAcceptanceGate` is passed; see
multi-agent/internal/ablation/skill_flags.go.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


# ----------------------------------------------------------------------
# Golden-mode constants (spec §2.3, §3 (b), §3 (f))
# ----------------------------------------------------------------------

# REPO_ROOT / MODULE_ROOT are derived from __file__ (not $PWD) so the
# runner is moveable: the only invariant is the runner lives at
#   <repo>/skills/mcp-acceptance/scripts/mcp_acceptance.py
# REPO_ROOT contains --cases (so legacy-mode files under skills/
# continue to work); MODULE_ROOT contains in-case input.path fields
# (the stricter §13 §2.3 boundary).
_RUNNER_PATH = Path(__file__).resolve()
try:
    REPO_ROOT = _RUNNER_PATH.parents[3].resolve()
    MODULE_ROOT = (REPO_ROOT / "multi-agent").resolve()
except IndexError:  # pragma: no cover — only fires if file is moved out of skills/
    REPO_ROOT = None  # type: ignore[assignment]
    MODULE_ROOT = None  # type: ignore[assignment]

# PATH_TYPED_INPUT_FIELDS is the closed set of input keys whose values
# are paths and therefore require the §3 (b) traversal check. The test
# `test_path_typed_fields_locked` pins this set.
PATH_TYPED_INPUT_FIELDS: frozenset[str] = frozenset({"path", "policy_path"})

# TOOL_ALLOWED_FIELDS mirrors
# multi-agent/tests/eval/golden/golden_schema_test.go:toolAllowedFields.
# A pytest case (test_allowlist_in_sync_with_go_source) parses the Go
# source and asserts byte-equal sync. Adding a key here without editing
# the Go side (or vice-versa) fails CI.
TOOL_ALLOWED_FIELDS: dict[str, frozenset[str]] = {
    "csv_profile": frozenset({"path"}),
    "parse_access_log": frozenset({"path", "format"}),
    "check_refund_eligibility": frozenset({"order", "policy_path"}),
    "image_metadata": frozenset({"path"}),
    "local_echo_call": frozenset({"method", "body", "headers", "base_url"}),
}

# GOLDEN_TOP_LEVEL_FIELDS is the closed set of top-level keys allowed
# in a golden-mode case line (spec §3 (e)).
GOLDEN_TOP_LEVEL_FIELDS: frozenset[str] = frozenset({
    "name", "tool", "input", "expected", "expected_error",
})

# Environment variable name for the ablation bypass (spec §3 (d)).
# Hardcoded twice in the codebase — once here and once in
# multi-agent/internal/ablation/skill_flags.go — so renaming one without
# the other breaks both tests immediately.
ABLATION_ENV_VAR = "LOOM_ABLATION_NOACCEPTANCEGATE"


class GoldenLoadError(SystemExit):
    """Pre-flight / load-time error in golden mode. Exit code 2.

    A subclass of SystemExit so that a plain `raise GoldenLoadError(msg)`
    surfaces at argparse-level with the right exit code, but `isinstance`
    still works for the pytest suite.
    """

    def __init__(self, message: str):
        # Write to stderr so the message is visible even when --json is
        # passed; argparse-style errors normally go to stderr.
        print(f"mcp_acceptance: {message}", file=sys.stderr)
        super().__init__(2)


@dataclass
class CaseResult:
    name: str
    tool: str
    passed: bool
    reasons: list[str] = field(default_factory=list)
    elapsed_ms: int = 0
    response_text: str = ""
    is_error: bool = False


# ----------------------------------------------------------------------
# Path-traversal helpers (spec §3 (a) and §3 (b))
# ----------------------------------------------------------------------

def _is_tmp_carveout(value: str) -> bool:
    """True iff `value` matches the §13 §2.3 strict /tmp/ prefix.

    The carve-out lets negative-case fixtures point at intentionally
    non-existent paths (e.g. /tmp/does-not-exist-fb73.csv) without the
    runner rejecting them as out-of-tree. Strict prefix means
    `len(value) > 5 and value[:5] == "/tmp/"`. Bare /tmp, /tmpfile,
    and anything else does NOT match.
    """
    return len(value) > 5 and value[:5] == "/tmp/"


def _assert_inside(value: str, field_label: str, root: Path,
                   root_label: str) -> None:
    """Resolve `value` and assert it is inside `root`. Exit 2 on miss.

    Skips the resolve check for /tmp/ strict-prefix values per §13 §2.3
    (the carve-out applies to both --cases and in-case fields — the
    spec §3 (a) carve-out exclusion for --cases is enforced separately
    by _resolve_cases_path before this is called).

    Relative paths are resolved against `root`; absolute paths are
    taken as-is then resolved. `field_label` is used in the error
    message; `root_label` names the boundary (e.g. "repo root",
    "module root").
    """
    if root is None:
        raise GoldenLoadError(
            f"{root_label} could not be derived from runner path "
            f"({_RUNNER_PATH}); is the file outside skills/mcp-acceptance/scripts/?"
        )
    if _is_tmp_carveout(value):
        return
    candidate = Path(value)
    if not candidate.is_absolute():
        candidate = root / candidate
    resolved = candidate.resolve()
    if not _is_within(resolved, root):
        raise GoldenLoadError(
            f"{field_label} path outside {root_label}: {value} -> {resolved}"
        )


def _is_within(path: Path, root: Path) -> bool:
    """True iff `path` is `root` or lives below it.

    Implemented with os.path.commonpath rather than is_relative_to
    because the latter requires Python 3.9+ AND is sensitive to
    case-folding quirks on macOS. commonpath returns the root only when
    `path` is genuinely below it.
    """
    try:
        return os.path.commonpath([str(path), str(root)]) == str(root)
    except ValueError:
        # Different drives on Windows, or path empties; treat as outside.
        return False


# ----------------------------------------------------------------------
# Case loading — golden mode + legacy mode (spec §1.1 auto-detect)
# ----------------------------------------------------------------------


def _resolve_cases_path(path: str) -> Path:
    """Validate --cases <path> and return the resolved Path.

    Special-cases "-" for stdin — supported in BOTH legacy and golden
    mode. Stdin payloads still go through all §3 (a)-(f) security
    checks (mode-detect, allowlist, in-case input.path traversal); only
    the §3 (a) --cases-resolves-inside-REPO_ROOT check is skipped
    because there is no on-disk anchor to resolve against. The ablation
    bypass log surfaces `cases=-` so the audit trail makes the stdin
    origin explicit.

    Per spec §2.3 / §3 (a), --cases must resolve inside REPO_ROOT (the
    worktree dir), not MODULE_ROOT — legacy fixtures like
    skills/mcp-acceptance/scripts/example_cases.jsonl live alongside
    multi-agent/, not under it.
    """
    if path == "-":
        return Path("-")
    # /tmp/ carve-out does NOT apply to --cases — the cases file must
    # live in the repo. See spec §3 (a) "carve-out exception".
    if _is_tmp_carveout(path):
        raise GoldenLoadError(
            f"cases path under /tmp/ is not allowed for --cases: {path}"
        )
    _assert_inside(path, "cases", REPO_ROOT, "repo root")
    if REPO_ROOT is None:
        # Defensive: _assert_inside already raised, but keep the path
        # return type honest.
        raise GoldenLoadError("REPO_ROOT not derivable")
    p = Path(path)
    if not p.is_absolute():
        p = REPO_ROOT / p
    return p.resolve()


def _classify_line(obj: dict[str, Any]) -> str:
    """Return 'golden' | 'legacy' | 'incomplete_golden' | 'mixed'.

    'incomplete_golden' means the line uses the golden `input` field but
    is missing both `expected` and `expected_error`. It is still routed
    through the golden loader (which then raises the targeted
    "missing one of {expected, expected_error}" error) — so downstream
    error messages tell operators what to fix instead of the misleading
    "mixes golden and legacy" wording. PR #57 round-3 review caught
    the previous behaviour.
    """
    has_input = "input" in obj
    has_args = "args" in obj
    has_expected = "expected" in obj or "expected_error" in obj
    if has_input and has_expected and not has_args:
        return "golden"
    if has_input and not has_expected and not has_args:
        return "incomplete_golden"
    if has_args and not has_input and not has_expected:
        return "legacy"
    return "mixed"


def load_cases(path: str) -> tuple[list[dict[str, Any]], str, Path | None]:
    """Load and validate a cases file. Returns (cases, mode, resolved_path).

    mode is "golden" or "legacy". For legacy stdin ("-"), resolved_path
    is None.

    Raises GoldenLoadError (exit 2) on any pre-flight violation:
      - --cases path outside MODULE_ROOT (§3 (a))
      - mixed-shape file (§1.1)
      - empty / no cases (§2.2 #3)
      - multiple distinct `tool` values in golden mode (§2.2 #1)
      - unknown top-level case field in golden mode (§3 (e))
      - unknown input field in golden mode (§3 (f))
      - input.path / input.policy_path outside MODULE_ROOT (§3 (b))
    """
    resolved: Path | None
    if path == "-":
        text = sys.stdin.read()
        resolved = None
    else:
        resolved = _resolve_cases_path(path)
        try:
            text = resolved.read_text(encoding="utf-8")
        except OSError as e:
            raise GoldenLoadError(f"cases file unreadable: {resolved}: {e}")

    parsed: list[tuple[int, dict[str, Any], str]] = []
    for i, raw in enumerate(text.splitlines(), 1):
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        try:
            obj = json.loads(stripped)
        except json.JSONDecodeError as e:
            raise GoldenLoadError(f"cases line {i}: invalid JSON: {e}")
        if not isinstance(obj, dict):
            raise GoldenLoadError(f"cases line {i}: case must be a JSON object")
        parsed.append((i, obj, _classify_line(obj)))

    if not parsed:
        raise GoldenLoadError(
            f"no cases loaded from {path} "
            "(empty file, all lines blank, or all lines comments)"
        )

    shapes = {shape for _, _, shape in parsed}
    # "incomplete_golden" lines share the golden mode's downstream
    # validator (which raises a targeted "missing one of {expected,
    # expected_error}" error). Merge them into "golden" for mode
    # detection so we don't mis-report an incomplete file as
    # "mixes shapes".
    if shapes == {"golden", "incomplete_golden"} or shapes == {"incomplete_golden"}:
        shapes = {"golden"}
    if "mixed" in shapes or len(shapes) > 1:
        # If any line is "mixed" (neither pure golden nor pure legacy),
        # or if golden and legacy lines are both present, refuse.
        raise GoldenLoadError(
            "cases file mixes golden ({input, expected|expected_error}) "
            "and legacy ({args, expect_*}) shapes; pick one. "
            f"Per-line shapes: {[(i, s) for i, _, s in parsed]}"
        )

    mode = next(iter(shapes))
    cases: list[dict[str, Any]] = []
    if mode == "legacy":
        for i, obj, _ in parsed:
            if "tool" not in obj or "args" not in obj:
                # Reachable only via a future legacy-mode extension that
                # adds new top-level keys; today _classify_line already
                # rejected this as "mixed". Belt-and-braces.
                raise GoldenLoadError(f"cases line {i}: missing 'tool' or 'args'")
            obj.setdefault("name", f"case-{i}")
            cases.append(obj)
        return cases, mode, resolved

    # mode == "golden": apply §3 (a)..(f) checks.
    tools_seen: set[str] = set()
    for i, obj, _ in parsed:
        # §3 (e): closed top-level field set.
        unknown_top = set(obj.keys()) - GOLDEN_TOP_LEVEL_FIELDS
        if unknown_top:
            raise GoldenLoadError(
                f"unknown field in case {obj.get('name', f'line-{i}')!r}: "
                f"{sorted(unknown_top)}"
            )
        # Required fields.
        for required in ("name", "tool", "input"):
            if required not in obj:
                raise GoldenLoadError(f"cases line {i}: missing required field {required!r}")
        # name must be a non-empty string — mirrors the Go schema test
        # (golden_schema_test.go:TestAcceptanceCasesAreValid). PR #57
        # round-2 review caught the Python loader silently accepting
        # `"name": ""`, which would render as `[PASS]  (csv_profile)`
        # in reports — confusing for human review.
        if not isinstance(obj["name"], str) or not obj["name"].strip():
            raise GoldenLoadError(
                f"cases line {i}: 'name' must be a non-empty string"
            )
        # tool must be a non-empty string. PR #57 round-3 review caught
        # two failure modes: (1) a non-hashable value (dict/list) crashed
        # with a bare TypeError at `tools_seen.add(tool_name)` → exit 1,
        # violating the "pre-flight → exit 2" contract; (2) a hashable
        # non-string (bool/int/float/None) leaked past the type check
        # and hit the TOOL_ALLOWED_FIELDS lookup, producing a misleading
        # "tool True has no input allowlist; update toolAllowedFields..."
        # message that asks the maintainer to register a bad-type value.
        if not isinstance(obj["tool"], str) or not obj["tool"].strip():
            raise GoldenLoadError(
                f"cases line {i}: 'tool' must be a non-empty string, "
                f"got {type(obj['tool']).__name__}"
            )
        if "expected" not in obj and "expected_error" not in obj:
            raise GoldenLoadError(
                f"cases line {i}: exactly one of {{expected, expected_error}} required"
            )
        if "expected" in obj and "expected_error" in obj:
            raise GoldenLoadError(
                f"cases line {i}: only one of {{expected, expected_error}} allowed"
            )
        # §3 (c) defensive: empty / whitespace-only expected_error would
        # substring-match nearly every isError response (`"" in
        # anything == True`; `"\n" in any-multi-line-traceback == True`;
        # `" " in any-multi-word-error == True`), giving silent green
        # on any unrelated tool error. Reject at load time. The first
        # version of this check only rejected `== ""`; the round-2 PR
        # #57 review caught that `"\n"` against a Python traceback
        # still bypassed the §3 (c) contract. Use the same
        # not value.strip() pattern as the input.path check below for
        # symmetry.
        if "expected_error" in obj:
            if not isinstance(obj["expected_error"], str):
                raise GoldenLoadError(
                    f"cases line {i}: expected_error must be a string, "
                    f"got {type(obj['expected_error']).__name__}"
                )
            if not obj["expected_error"].strip():
                raise GoldenLoadError(
                    f"cases line {i}: expected_error must be a non-empty "
                    "string (whitespace-only would substring-match almost "
                    "any error message)"
                )
        if not isinstance(obj["input"], dict):
            raise GoldenLoadError(f"cases line {i}: 'input' must be a JSON object")

        tool_name = obj["tool"]
        tools_seen.add(tool_name)

        # §3 (f): per-tool input-field allowlist.
        if tool_name not in TOOL_ALLOWED_FIELDS:
            raise GoldenLoadError(
                f"tool {tool_name!r} has no input allowlist; update toolAllowedFields "
                "in multi-agent/tests/eval/golden/golden_schema_test.go and "
                "re-sync the Python mirror in mcp_acceptance.py"
            )
        allowed = TOOL_ALLOWED_FIELDS[tool_name]
        for key in obj["input"]:
            if key not in allowed:
                raise GoldenLoadError(
                    f"input field {key!r} not allowed for tool {tool_name!r} "
                    f"(allowed: {sorted(allowed)})"
                )

        # §3 (b): in-case path-traversal check on PATH_TYPED_INPUT_FIELDS.
        for path_field in PATH_TYPED_INPUT_FIELDS:
            if path_field not in obj["input"]:
                continue
            value = obj["input"][path_field]
            if not isinstance(value, str):
                # A non-string value is rejected here rather than silently
                # ignored: a future MCP tool that accepts e.g. a list of
                # paths must update both the allowlist AND this loop.
                raise GoldenLoadError(
                    f"input.{path_field} must be a string, got {type(value).__name__}"
                )
            # §3 (b) defensive: an empty / whitespace-only path silently
            # joins to MODULE_ROOT (Path("") resolves to ".") and slips
            # past _assert_inside. The MCP tool then sees "" and its
            # tool-specific behaviour (list CWD, default file, etc.)
            # leaks. Reject at load time. Discovered by PR #57 review.
            if not value.strip():
                raise GoldenLoadError(
                    f"input.{path_field} must be a non-empty string "
                    "(blank paths bypass the traversal check)"
                )
            _assert_inside(value, f"input.{path_field}", MODULE_ROOT, "module root")

        cases.append(obj)

    # §2.2 #1 + §13 §2.5 #1: one tool per file.
    if len(tools_seen) != 1:
        raise GoldenLoadError(
            f"cases file declares multiple tools: {sorted(tools_seen)} "
            "(§13 §2.5 #1: one family, one tool)"
        )

    # Mirror golden_schema_test.go's per-file name-uniqueness check
    # (TestAcceptanceCasesAreValid). Without this, a duplicate name
    # would produce two PASS / FAIL lines under the same label,
    # defeating report aggregation. PR #57 round-2 review caught the
    # Python loader silently accepting duplicates.
    seen_names: dict[str, int] = {}
    for c in cases:
        seen_names[c["name"]] = seen_names.get(c["name"], 0) + 1
    dupes = sorted(n for n, count in seen_names.items() if count > 1)
    if dupes:
        raise GoldenLoadError(
            f"duplicate case name(s) in cases file: {dupes}"
        )

    return cases, mode, resolved


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


def extract_golden_response(call_result: dict) -> tuple[Any, bool, list[str]]:
    """Extract (subject, is_error, reasons) for a golden-mode comparison.

    Subject is the value the runner deep-equals against `expected` (or
    None when is_error=True). Reasons is a list of pre-comparison
    failures; non-empty reasons means the case has already failed
    before we touch `expected`.

    Preference order (spec §2.4):
      1. result.structuredContent if present.
      2. Concatenated content[*].text parsed as JSON.

    On is_error=True, subject is the joined text (so the substring
    matcher in evaluate_golden can search it).
    """
    inner = call_result.get("result") or {}
    is_err = bool(inner.get("isError", False))
    content = inner.get("content") or []

    text_parts: list[str] = []
    for item in content:
        if isinstance(item, dict) and item.get("type") == "text":
            text_parts.append(str(item.get("text", "")))
    joined_text = "".join(text_parts)

    if is_err:
        return joined_text, True, []

    if "structuredContent" in inner:
        return inner["structuredContent"], False, []

    if not joined_text.strip():
        return None, False, [
            "response has no structuredContent and no text content"
        ]
    try:
        return json.loads(joined_text), False, []
    except json.JSONDecodeError as e:
        return None, False, [
            f"response is not structured JSON and content text is not parseable: {e}"
        ]


def evaluate_golden(case: dict, subject: Any, is_err: bool) -> list[str]:
    """Return list of failure reasons for a golden-mode case; empty = pass.

    Deep-equal for `expected` (§2.4); case-sensitive substring for
    `expected_error` (§3 (c)). The matcher is hardcoded; there is no
    flag, no env var, no per-case override to weaken it.
    """
    reasons: list[str] = []
    if "expected_error" in case:
        needle = str(case["expected_error"])
        if not is_err:
            reasons.append(
                f"expected_error set ({needle!r}) but response was not isError"
            )
            return reasons
        haystack = subject if isinstance(subject, str) else json.dumps(subject)
        # Case-sensitive substring — see spec §3 (c). DO NOT change.
        if needle not in haystack:
            reasons.append(
                f"expect_error miss: needle={needle!r} in haystack={haystack!r}"
            )
        return reasons

    # case has "expected" (load_cases enforces exactly one).
    expected = case["expected"]
    if is_err:
        reasons.append(
            f"expected non-error response but got isError=True; "
            f"server text: {subject!r}"
        )
        return reasons
    if subject != expected:
        reasons.append(
            f"expected deep-equal mismatch:\n  expected: {json.dumps(expected, sort_keys=True)}"
            f"\n  got:      {json.dumps(subject, sort_keys=True, default=str)}"
        )
    return reasons


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
        verbose: bool, mode: str = "legacy") -> list[CaseResult]:
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
            # Golden mode passes `input`; legacy mode passes `args`.
            arguments = case["input"] if mode == "golden" else case.get("args", {})
            t0 = time.monotonic()
            try:
                resp = sess.send_request(
                    "tools/call",
                    {"name": case["tool"], "arguments": arguments},
                    timeout=float(case.get("timeout_sec", 10)),
                )
            except Exception as e:
                r.reasons.append(f"tools/call raised: {e}")
                r.elapsed_ms = int((time.monotonic() - t0) * 1000)
                results.append(r)
                continue
            r.elapsed_ms = int((time.monotonic() - t0) * 1000)
            if mode == "golden":
                subject, is_err, extract_reasons = extract_golden_response(resp)
                r.is_error = is_err
                # response_text is kept for the human-readable report; we
                # serialize the comparison subject for golden mode so a
                # failing case shows the actual data, not just '[object]'.
                r.response_text = (
                    subject if isinstance(subject, str)
                    else json.dumps(subject, sort_keys=True, default=str)
                )
                if extract_reasons:
                    r.reasons = extract_reasons
                else:
                    r.reasons = evaluate_golden(case, subject, is_err)
            else:
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

    # load_cases performs ALL §3 (a)..(f) security checks before
    # returning. If anything fails, it raises GoldenLoadError which is a
    # SystemExit(2) subclass — argparse-style exit, no recursion into
    # the MCP layer. This guarantees the §3 (d) ordering invariant:
    # ablation bypass is checked AFTER security checks, so combinations
    # like LOOM_ABLATION_NOACCEPTANCEGATE=1 --cases /etc/passwd still
    # exit 2, not 0.
    cases, mode, resolved_cases_path = load_cases(args.cases)

    # §3 (d) ablation bypass — only meaningful for golden mode. Legacy
    # mode is unchanged. The env var is documented in spec §3 (d) and
    # bridged from Go via internal/ablation/skill_flags.go.
    if mode == "golden" and os.environ.get(ABLATION_ENV_VAR) == "1":
        # Mandatory single-line stderr log; silent green is the attack.
        cases_label = str(resolved_cases_path) if resolved_cases_path else "-"
        print(
            f"[ablation] NoAcceptanceGate: bypassed cases={cases_label} count={len(cases)}",
            file=sys.stderr,
        )
        return 0

    server_cmd = shlex.split(args.server)
    try:
        results = run(server_cmd, cases, cwd=args.cwd, verbose=args.verbose, mode=mode)
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
