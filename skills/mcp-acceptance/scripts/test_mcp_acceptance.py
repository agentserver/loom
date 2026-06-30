"""Pytest matrix for mcp_acceptance.py --cases golden-mode.

Covers spec §3 (a)-(f) security mitigations, §13 §2.5 invariants, the
todo_list acceptance criteria, and Go/Python drift guards.

See docs/specs/wt1-acceptance-golden.plan.md §2 for the row-by-row map.
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

from conftest import (
    FAMILIES,
    GOLDEN_ROOT,
    MODULE_ROOT,
    ORACLE,
    PYTEST_TMP,
    REPO_ROOT,
    RUNNER,
)


# ---- helpers ---------------------------------------------------------


def _run_runner(cases_path: Path | str, *,
                server_cmd: str | None = None,
                env_extra: dict[str, str] | None = None,
                ) -> subprocess.CompletedProcess:
    """Invoke mcp_acceptance.py and capture exit/stdout/stderr."""
    if server_cmd is None:
        server_cmd = f"{sys.executable} {ORACLE} --cases {cases_path}"
    env = os.environ.copy()
    if env_extra:
        env.update(env_extra)
    return subprocess.run(
        [sys.executable, str(RUNNER),
         "--server", server_cmd,
         "--cases", str(cases_path)],
        env=env,
        capture_output=True,
        text=True,
        timeout=60,
        cwd=str(REPO_ROOT),
    )


def _golden_case(tool: str, **overrides) -> dict:
    """Build a syntactically-valid golden case with caller overrides."""
    base = {
        "name": "t",
        "tool": tool,
        "input": {"path": "/tmp/x.csv"},
        "expected_error": "y",
    }
    base.update(overrides)
    return base


# ---- 5-family happy path ---------------------------------------------


@pytest.mark.parametrize("family", FAMILIES)
def test_cases_happy_path_5_families(family: str) -> None:
    cases = GOLDEN_ROOT / family / "acceptance" / "cases.jsonl"
    r = _run_runner(cases)
    assert r.returncode == 0, (
        f"{family} expected exit 0, got {r.returncode}\n"
        f"stdout:\n{r.stdout}\nstderr:\n{r.stderr}"
    )


# ---- mutation: one wrong expected → exit 1 ---------------------------


def test_cases_one_fail_exit_1(synth_cases) -> None:
    src = GOLDEN_ROOT / "csv-profiler" / "acceptance" / "cases.jsonl"
    raw = src.read_text(encoding="utf-8").splitlines()
    mutated_lines: list[dict] = []
    for line in raw:
        line = line.strip()
        if not line:
            continue
        obj = json.loads(line)
        # Flip the first happy_path case's rows count.
        if obj.get("name") == "happy_path_small_sales":
            obj["expected"]["rows"] = 999
        mutated_lines.append(obj)
    mutated = synth_cases(mutated_lines, suffix="-mut")

    # IMPORTANT: oracle uses the ORIGINAL cases (the ground truth);
    # runner uses the MUTATED cases (the assertions). Mismatch -> exit 1.
    oracle_cmd = f"{sys.executable} {ORACLE} --cases {src}"
    r = _run_runner(mutated, server_cmd=oracle_cmd)
    assert r.returncode == 1, (
        f"expected exit 1 (case mismatch), got {r.returncode}\n"
        f"stdout:\n{r.stdout}\nstderr:\n{r.stderr}"
    )
    assert "happy_path_small_sales" in r.stdout, r.stdout
    assert "deep-equal mismatch" in r.stdout or "FAIL" in r.stdout, r.stdout


# ---- §3 (a) --cases path traversal -----------------------------------


def test_cases_path_outside_module_root_exit_2() -> None:
    r = _run_runner("/etc/passwd", server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "cases path outside repo root" in r.stderr, r.stderr


def test_cases_relative_traversal_exit_2() -> None:
    r = _run_runner("../../etc/passwd", server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "cases path outside repo root" in r.stderr, r.stderr


# ---- §3 (b) input.path traversal + /tmp carve-out --------------------


def test_input_path_traversal_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "bad", "tool": "csv_profile",
         "input": {"path": "/etc/passwd"},
         "expected_error": "x"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "input.path path outside module root" in r.stderr, r.stderr


def test_input_path_tmp_allowed_for_negative(synth_cases) -> None:
    # Echo-oracle's no-match branch returns isError=true with text
    # 'echo-oracle: no matching case for arguments=...'. We use that
    # text as the substring needle so the case passes; the point of
    # this test is that the path layer ACCEPTS /tmp/... and lets the
    # case reach the tools/call.
    cases = synth_cases([
        {"name": "tmp_neg", "tool": "csv_profile",
         "input": {"path": "/tmp/does-not-exist-fb73.csv"},
         "expected_error": "no matching case"},
    ])
    r = _run_runner(cases)
    assert r.returncode == 0, (
        f"expected /tmp/... carve-out → exit 0, got {r.returncode}\n"
        f"stdout:\n{r.stdout}\nstderr:\n{r.stderr}"
    )


def test_tmp_carveout_strict_prefix(synth_cases) -> None:
    """/tmpfile and bare /tmp must NOT carve out — both exit 2."""
    for bad_value in ("/tmpfile", "/tmp"):
        cases = synth_cases([
            {"name": "bad", "tool": "csv_profile",
             "input": {"path": bad_value},
             "expected_error": "x"},
        ], suffix=f"-strict-{bad_value.replace('/', '_')}")
        r = _run_runner(cases, server_cmd="true")
        assert r.returncode == 2, (
            f"path {bad_value!r}: expected exit 2, got {r.returncode}\n"
            f"stderr:\n{r.stderr}"
        )
        assert "input.path path outside module root" in r.stderr, (
            f"path {bad_value!r} stderr:\n{r.stderr}"
        )


# ---- §3 (e) unknown top-level field ----------------------------------


def test_unknown_case_field_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "x", "tool": "csv_profile",
         "input": {"path": "/tmp/x"},
         "expected_error": "y",
         "__import__": "os"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "unknown field in case" in r.stderr, r.stderr
    assert "__import__" in r.stderr, r.stderr


# ---- §3 (f) unknown input field --------------------------------------


def test_unknown_input_field_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "x", "tool": "csv_profile",
         "input": {"path": "/tmp/x", "base_url": "evil"},
         "expected_error": "y"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "input field 'base_url' not allowed for tool 'csv_profile'" in r.stderr, r.stderr


# ---- §3 (b) defensive: empty path bypass (PR #57 review) -------------


def test_empty_input_path_rejected(synth_cases) -> None:
    """An empty input.path slips past _assert_inside (Path("") resolves
    to MODULE_ROOT); reject it at load time."""
    cases = synth_cases([
        {"name": "blank", "tool": "csv_profile",
         "input": {"path": ""}, "expected_error": "x"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "input.path must be a non-empty string" in r.stderr, r.stderr


def test_whitespace_input_path_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "ws", "tool": "csv_profile",
         "input": {"path": "   "}, "expected_error": "x"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "input.path must be a non-empty string" in r.stderr, r.stderr


# ---- §3 (c) defensive: empty expected_error (PR #57 review) ----------


def test_empty_expected_error_rejected(synth_cases) -> None:
    """Empty expected_error matches every isError response via `"" in X`;
    reject at load time to preserve the negative-case contract."""
    cases = synth_cases([
        {"name": "empty_err", "tool": "csv_profile",
         "input": {"path": "/tmp/x.csv"}, "expected_error": ""},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "expected_error must be a non-empty string" in r.stderr, r.stderr


def test_non_string_expected_error_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "bad_type", "tool": "csv_profile",
         "input": {"path": "/tmp/x.csv"}, "expected_error": 42},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "expected_error must be a string" in r.stderr, r.stderr


@pytest.mark.parametrize("blank", ["\n", "\t", " ", "   ", "\r\n"])
def test_whitespace_only_expected_error_rejected(synth_cases, blank: str) -> None:
    """Round-2 PR #57 review: \"\\n\" in any multi-line error == True,
    \" \" in any multi-word error == True — same silent-green class as
    empty-string. Reject all whitespace-only needles."""
    cases = synth_cases([
        {"name": "ws", "tool": "csv_profile",
         "input": {"path": "/tmp/x.csv"}, "expected_error": blank},
    ], suffix=f"-ws-{repr(blank)}")
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (
        f"blank={blank!r} expected exit 2, got {r.returncode}\nstderr:\n{r.stderr}"
    )
    assert "expected_error must be a non-empty string" in r.stderr, r.stderr


# ---- Per-file invariants (mirror golden_schema_test.go) --------------


def test_duplicate_case_names_rejected(synth_cases) -> None:
    """Mirrors golden_schema_test.go:TestAcceptanceCasesAreValid's
    per-file name uniqueness check."""
    cases = synth_cases([
        {"name": "same", "tool": "csv_profile",
         "input": {"path": "/tmp/a"}, "expected_error": "x"},
        {"name": "same", "tool": "csv_profile",
         "input": {"path": "/tmp/b"}, "expected_error": "y"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "duplicate case name" in r.stderr, r.stderr
    assert "'same'" in r.stderr, r.stderr


def test_empty_case_name_rejected(synth_cases) -> None:
    """Mirrors Go schema test's `if c.Name == ""` check."""
    cases = synth_cases([
        {"name": "", "tool": "csv_profile",
         "input": {"path": "/tmp/x"}, "expected_error": "y"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "'name' must be a non-empty string" in r.stderr, r.stderr


def test_whitespace_case_name_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "  ", "tool": "csv_profile",
         "input": {"path": "/tmp/x"}, "expected_error": "y"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "'name' must be a non-empty string" in r.stderr, r.stderr


# ---- §3 (c) case-sensitive substring ---------------------------------


def test_expected_error_case_sensitive(synth_cases) -> None:
    cases = synth_cases([
        {"name": "miscased", "tool": "csv_profile",
         "input": {"path": "/tmp/x.csv"},
         "expected_error": "file not found"},
    ])
    forced_text = "File Not Found"
    server_cmd = f'{sys.executable} {ORACLE} --cases {cases} --force-error-text "{forced_text}"'
    r = _run_runner(cases, server_cmd=server_cmd)
    assert r.returncode == 1, (
        f"expected exit 1 (substring miss), got {r.returncode}\n"
        f"stdout:\n{r.stdout}\nstderr:\n{r.stderr}"
    )
    assert "expect_error miss" in r.stdout, r.stdout
    assert "file not found" in r.stdout and "File Not Found" in r.stdout, r.stdout


# ---- §3 (d) ablation bypass logs to stderr ---------------------------


def test_ablation_bypass_logs_to_stderr() -> None:
    cases = GOLDEN_ROOT / "csv-profiler" / "acceptance" / "cases.jsonl"
    r = _run_runner(
        cases,
        server_cmd="true",  # would otherwise be exit 2 (handshake)
        env_extra={"LOOM_ABLATION_NOACCEPTANCEGATE": "1"},
    )
    assert r.returncode == 0, (
        f"expected ablation bypass → exit 0, got {r.returncode}\n"
        f"stderr:\n{r.stderr}"
    )
    # Spec §3 (d) requires exactly one matching log line; we also
    # assert that no other non-blank stderr lines slip in alongside it
    # (silent green is the attack, and stuffing extra noise around the
    # log would defeat operator grep).
    log_lines = [ln for ln in r.stderr.splitlines() if ln.strip()]
    assert log_lines, "expected at least one stderr log line"
    matched = [ln for ln in log_lines if re.match(
        r"^\[ablation\] NoAcceptanceGate: bypassed cases=.+ count=\d+$", ln
    )]
    assert len(matched) == 1, (
        f"expected exactly one matching log line, got {len(matched)};\n"
        f"stderr:\n{r.stderr}"
    )
    assert len(log_lines) == 1, (
        f"expected the matching log line to be the ONLY non-blank stderr "
        f"line, got {len(log_lines)} total:\n{r.stderr}"
    )
    assert "cases=" in matched[0] and "count=6" in matched[0], matched[0]


def test_ablation_bypass_still_checks_path() -> None:
    """Spec §6: ablation+absolute-path traversal still exit 2."""
    r = _run_runner(
        "/etc/passwd",
        server_cmd="true",
        env_extra={"LOOM_ABLATION_NOACCEPTANCEGATE": "1"},
    )
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "cases path outside repo root" in r.stderr, r.stderr


def test_ablation_bypass_still_checks_relative_path() -> None:
    """Spec §6: ablation+relative-path traversal also exit 2."""
    r = _run_runner(
        "../../etc/passwd",
        server_cmd="true",
        env_extra={"LOOM_ABLATION_NOACCEPTANCEGATE": "1"},
    )
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "cases path outside repo root" in r.stderr, r.stderr


# ---- §13 §2.5 #1 single-tool invariant ------------------------------


def test_multi_tool_in_cases_rejected(synth_cases) -> None:
    cases = synth_cases([
        {"name": "a", "tool": "csv_profile",
         "input": {"path": "/tmp/a"}, "expected_error": "x"},
        {"name": "b", "tool": "parse_access_log",
         "input": {"path": "/tmp/b", "format": "nginx-combined"},
         "expected_error": "x"},
    ])
    r = _run_runner(cases, server_cmd="true")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "multiple tools" in r.stderr, r.stderr


# ---- Drift guards ----------------------------------------------------


def _load_runner_module():
    """Load mcp_acceptance.py as a module for in-process introspection.

    Python 3.14's @dataclass decorator introspects sys.modules during
    class construction, so we MUST register the module in sys.modules
    before calling exec_module, otherwise dataclass raises
    AttributeError: 'NoneType' object has no attribute '__dict__'.
    """
    import importlib.util
    name = "mcp_acceptance_mod"
    spec = importlib.util.spec_from_file_location(name, RUNNER)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    try:
        spec.loader.exec_module(mod)
    except Exception:
        sys.modules.pop(name, None)
        raise
    return mod


def test_path_typed_fields_locked() -> None:
    """Spec §3 (b) closed list."""
    mod = _load_runner_module()
    assert mod.PATH_TYPED_INPUT_FIELDS == frozenset({"path", "policy_path"})


def test_allowlist_in_sync_with_go_source() -> None:
    """The Python TOOL_ALLOWED_FIELDS must match the Go toolAllowedFields literal."""
    # Parse the Go map by hand-rolled brace-balanced scan. Regex with
    # nested {...} blocks is unreliable; we instead locate the opening
    # `{` after `map[string]map[string]bool` and walk forward counting
    # braces (after stripping // comments, /* */ block comments, "..."
    # interpreted strings, and `...` raw strings) to find the matching
    # closing `}`. The inner per-tool blocks are then extracted with a
    # simpler regex on the balanced body.
    #
    # Why every literal type: a future maintainer could plausibly add
    # a comment with `{` characters in it, or a raw-string description
    # field, or a /* */ block. Any of those, if not skipped, throws off
    # the brace-counter and the test silently mis-parses the map.
    go_path = MODULE_ROOT / "tests" / "eval" / "golden" / "golden_schema_test.go"
    text = go_path.read_text(encoding="utf-8")
    anchor = "toolAllowedFields := map[string]map[string]bool{"
    start = text.find(anchor)
    assert start != -1, f"could not locate {anchor!r} in golden_schema_test.go"
    open_brace = start + len(anchor) - 1  # index of the leading `{`
    depth = 0
    end = -1
    i = open_brace
    while i < len(text):
        c = text[i]
        # Skip // line comments.
        if c == "/" and i + 1 < len(text) and text[i + 1] == "/":
            nl = text.find("\n", i)
            if nl == -1:
                break
            i = nl + 1
            continue
        # Skip /* ... */ block comments.
        if c == "/" and i + 1 < len(text) and text[i + 1] == "*":
            close = text.find("*/", i + 2)
            if close == -1:
                break
            i = close + 2
            continue
        # Skip "..." interpreted string literals (no embedded newlines;
        # handle escapes).
        if c == '"':
            j = i + 1
            while j < len(text) and text[j] != '"':
                if text[j] == "\\":
                    j += 2
                else:
                    j += 1
            i = j + 1
            continue
        # Skip `...` raw string literals (may span newlines, no escapes).
        if c == "`":
            close = text.find("`", i + 1)
            if close == -1:
                break
            i = close + 1
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                end = i
                break
        i += 1
    assert end != -1, "no matching closing brace for toolAllowedFields"
    body = text[open_brace + 1:end]

    # Match per-tool blocks: "tool_name": { ... },
    block_re = re.compile(
        r'"(?P<tool>[^"]+)"\s*:\s*\{(?P<inner>[^{}]*)\}',
        re.DOTALL,
    )
    field_re = re.compile(r'"(?P<key>[^"]+)"\s*:\s*true')
    go_map: dict[str, set[str]] = {}
    for match in block_re.finditer(body):
        tool = match.group("tool")
        # Strip // comments before extracting keys.
        inner_no_comments = re.sub(r"//[^\n]*", "", match.group("inner"))
        keys = {km.group("key") for km in field_re.finditer(inner_no_comments)}
        go_map[tool] = keys

    mod = _load_runner_module()
    py_map = {tool: set(fields) for tool, fields in mod.TOOL_ALLOWED_FIELDS.items()}

    assert py_map == go_map, (
        f"TOOL_ALLOWED_FIELDS drift between mcp_acceptance.py and "
        f"golden_schema_test.go:\n  python: {py_map}\n  go: {go_map}"
    )


# ---- Legacy-mode regression ------------------------------------------


def test_legacy_mode_still_works(tmp_path: Path) -> None:
    """example_cases.jsonl runs through the legacy code path."""
    example = REPO_ROOT / "skills" / "mcp-acceptance" / "scripts" / "example_cases.jsonl"
    if not example.exists():
        pytest.skip("example_cases.jsonl absent; legacy fixture not packaged")
    # Build a tiny stdio server that satisfies whatever the example file
    # asserts. example_cases.jsonl ships with the skill — read it to
    # discover the tool name and minimal echo behaviour. We inline a
    # generic legacy-mode echo server here rather than shipping another
    # fixture file.
    text = example.read_text(encoding="utf-8")
    first = next(json.loads(ln) for ln in text.splitlines()
                 if ln.strip() and not ln.strip().startswith("#"))
    tool_name = first["tool"]

    legacy_server = tmp_path / "legacy_server.py"
    legacy_server.write_text(f'''
import json, sys
def emit(rid, result): sys.stdout.write(json.dumps({{"jsonrpc":"2.0","id":rid,"result":result}}) + "\\n"); sys.stdout.flush()
for line in sys.stdin:
    if not line.strip(): continue
    msg = json.loads(line); m = msg.get("method"); rid = msg.get("id")
    if m == "initialize":
        emit(rid, {{"protocolVersion":"2024-11-05","capabilities":{{"tools":{{}}}},"serverInfo":{{"name":"legacy-stub","version":"0.1"}}}})
    elif m == "notifications/initialized":
        continue
    elif m == "tools/list":
        emit(rid, {{"tools":[{{"name":"{tool_name}","description":"x","inputSchema":{{"type":"object"}}}}]}})
    elif m == "tools/call":
        # Always return some text so expect_contains can match on at least one needle.
        args = (msg.get("params") or {{}}).get("arguments") or {{}}
        emit(rid, {{"content":[{{"type":"text","text":json.dumps(args)}}],"isError":False}})
''')
    # The legacy example may have specific expect_contains values; if so,
    # an arbitrary echo won't satisfy them, and the test would degenerate
    # into asserting exit-1. That's still useful — it proves legacy mode
    # is reached (no GoldenLoadError). We accept either 0 or 1, but
    # reject 2 (legacy mode must not raise GoldenLoadError).
    r = subprocess.run(
        [sys.executable, str(RUNNER),
         "--server", f"{sys.executable} {legacy_server}",
         "--cases", str(example)],
        capture_output=True, text=True, timeout=60, cwd=str(REPO_ROOT),
    )
    # Hard exit-code pin: must be 0 or 1, never 2 or higher. exit 2 =
    # GoldenLoadError fired (mode-detect regression); >1 = handshake or
    # other infra error. exit 1 is expected because the echo server
    # cannot satisfy the file's specific expect_contains needles.
    assert r.returncode in (0, 1), (
        f"legacy mode produced exit {r.returncode}; expected 0 or 1.\n"
        f"stderr:\n{r.stderr}\nstdout:\n{r.stdout}"
    )
    # The legacy reporter prints each case name; confirm the loader
    # actually ran cases and the report path is alive. A future
    # regression that silently classified the file as golden (or as
    # zero cases) would not print any of these names.
    for expected_name in ("four-row sum", "empty rows", "missing rows raises"):
        assert expected_name in r.stdout, (
            f"legacy mode did not name case {expected_name!r} in stdout; "
            f"loader may have mis-classified the file.\n"
            f"stdout:\n{r.stdout}\nstderr:\n{r.stderr}"
        )
