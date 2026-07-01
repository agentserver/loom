---
name: mcp-acceptance
description: Use BEFORE calling register_slave_mcp or register_mcp to validate a stdio MCP server's tools/call semantics — drives initialize→tools/list→tools/call against a case file and exits non-zero on any failure, gating registration in a shell pipeline. Also serves as the B3 acceptance gate for Phase-0 task-family golden cases (`--cases multi-agent/tests/eval/golden/<family>/acceptance/cases.jsonl`).
---

# MCP Acceptance

## Overview

`register_mcp` only does structural validation: `ast.parse`, import-allowlist check, and a `tools/list` smoke launch. It **never calls `tools/call`**, so a server with broken business logic (wrong math, missing field, crashes on edge case) can register successfully and surface bad data downstream. This skill closes that gap.

The runner takes a server command and a `cases.jsonl` file, runs the full MCP handshake (`initialize` → `notifications/initialized` → `tools/list` → per-case `tools/call`), asserts per-case expectations, and exits non-zero on any failure with a structured per-case diff.

**Exit code is the contract:** designed to gate registration in a shell pipeline.

```bash
python3 mcp_acceptance.py --server "python3 v1.py" --cases cases.jsonl \
    && register_slave_mcp ...
```

## When to Use

- **Always** before `register_slave_mcp` / `register_mcp` on a newly written or modified server.
- After editing a handler body in a scaffolded server (re-run cases).
- After bumping a tool's `args_schema` (extend cases to cover the new fields).
- Investigating a "tools/list smoke passed but slave returns garbage" report — the acceptance run will reproduce the failure.

When NOT to use:
- Pure protocol-conformance testing of someone else's MCP server (use MCP Inspector instead — it has interactive UI).
- Load / concurrency testing — this is a one-shot per-case sequential runner.

## Quick Reference

```bash
# Gate registration on acceptance pass
python3 .../mcp_acceptance.py --server "python3 v1.py" --cases cases.jsonl \
    && register_slave_mcp ...

# Server needs working directory (e.g. reads sibling files)
python3 .../mcp_acceptance.py --server "python3 v1.py" --cwd generated_mcp/foo --cases cases.jsonl

# Cases via stdin (for shell pipelines)
echo '{"tool":"...","args":{...},"expect_contains":["..."]}' \
    | python3 .../mcp_acceptance.py --server "python3 v1.py" --cases -

# JSON output for programmatic gating
python3 .../mcp_acceptance.py --server "..." --cases ... --json
```

## Exit Codes

| Code | Meaning |
|---|---|
| 0 | All cases passed. Safe to proceed to `register_slave_mcp`. |
| 1 | At least one case failed (assertion mismatch). |
| 2 | Server handshake failed (crash on startup, no response, malformed JSON). |

Exit 1 vs 2 lets shell wrappers distinguish "server has bugs" from "server isn't even runnable."

## Case Format (JSONL)

Two shapes are accepted; the runner auto-detects per file.

### Legacy shape (driver acceptance, ad-hoc cases)

One case per non-empty, non-`#` line:

```jsonl
{"name": "happy path", "tool": "summarize_rows", "args": {"rows": [1,2,3]}, "expect_contains": ["count=3", "mean=2.000"]}
{"name": "empty input", "tool": "summarize_rows", "args": {"rows": []}, "expect_contains": ["count=0"]}
{"name": "missing required arg", "tool": "summarize_rows", "args": {}, "expect_isError": true, "expect_contains": ["error"]}
{"name": "rejects negatives via regex", "tool": "summarize_rows", "args": {"rows": [-1]}, "expect_regex": "(negative|invalid)"}
```

Required fields:
- `tool` (string)
- `args` (object — passed verbatim as `arguments` in `tools/call`)

Optional fields (all AND-combined):
- `name` (string, label for output; defaults to `case-N`)
- `expect_isError` (bool, default `false`)
- `expect_contains` (list[str], all substrings must appear in joined `text` content)
- `expect_not_contains` (list[str], none may appear)
- `expect_regex` (str, `re.search` must match)
- `timeout_sec` (number, per-call timeout, default 10)

No expectations → only asserts `isError == expect_isError`.

### Golden shape (Phase-0 task families, B3 acceptance gate)

The `multi-agent/tests/eval/golden/<family>/acceptance/cases.jsonl` files
ship a stricter contract that pairs each case with either a
deep-equal `expected` value or a case-sensitive substring
`expected_error`:

```jsonl
{"name": "happy_path_small_sales", "tool": "csv_profile", "input": {"path": "tests/eval/golden/csv-profiler/first-task/input/sales.csv"}, "expected": {"rows": 5, "cols": 3}}
{"name": "error_missing_file", "tool": "csv_profile", "input": {"path": "/tmp/does-not-exist-fb73.csv"}, "expected_error": "file not found"}
```

Required fields per case:
- `name` (string, snake_case slug)
- `tool` (string — **all cases in the file MUST share one value**; per §13 §2.5 #1)
- `input` (object — keys must appear in the per-tool allowlist mirrored
  from `multi-agent/tests/eval/golden/golden_schema_test.go:toolAllowedFields`)
- **exactly one** of `expected` (any JSON value, deep-equal) or
  `expected_error` (string, case-sensitive substring matched against
  the MCP error message)

Any other top-level key → exit 2. See **Security** below.

### Mode selection

The runner auto-detects per file:

- All lines have `input` and exactly one of `expected` / `expected_error`
  → golden mode.
- All lines have `args` → legacy mode.
- Mixed → exit 2.

There is **no `--cases-format` switch** — the auto-detect rule is
exhaustive and a flag would only let the operator disagree with the
file's actual contents.

## What the Runner Also Asserts (free)

- `initialize` returns a result with a non-empty `protocolVersion`.
- `notifications/initialized` produces no response (a stray response surfaces as an id mismatch on the next request).
- Each case's `tool` is listed in `tools/list`. Missing tool fails the case without dispatching.
- Server stdout lines parse as JSON-RPC 2.0 responses.

## Workflow

1. Write or edit a server (typically with `scaffold-mcp-server`).
2. Write `cases.jsonl` covering: 1 happy path, 1+ edge case, 1+ error path.
3. `python3 mcp_acceptance.py --server "python3 v1.py" --cases cases.jsonl`
4. Iterate until exit 0.
5. **Only then** call `register_slave_mcp`.

## Running on a Slave (recommended)

A server's environment is the slave's environment (Python version, `allowed_packages`, network reachability), so validate there — not on the driver. Use `remote_run.py` to bundle the runner + cases + server source into a single bash payload, then pass it to `run_slave_bash`:

```bash
# 1. Driver-side: emit payload (base64-embeds runner + cases + server)
python3 skills/mcp-acceptance/scripts/remote_run.py \
    --cases cases.jsonl \
    --server-cmd "python3 /tmp/mcpa/server.py" \
    --source generated_mcp/foo/v1.py:/tmp/mcpa/server.py \
    > /tmp/payload.sh

# 2. Driver Claude passes the script content to run_slave_bash, e.g.
#    run_slave_bash(target_display_name="slave-a", script=<contents of payload.sh>, wait=true)
#    Exit code propagates: 0 = pass, 1 = case failed, 2 = server unreachable.
#    If the MCP client times out or is interrupted, call list_driver_tasks to
#    recover the delegated task_id before re-running the acceptance task.

# 3. Only on success:
register_slave_mcp --spec spec.json --source_path generated_mcp/foo/v1.py
```

Flags:
- `--source SRC:DEST` (repeatable) — upload a file from driver to absolute path on slave.
- `--workdir PATH` (default `/tmp/mcpa`) — slave scratch dir; cleaned by `trap` on exit.
- `--keep` — skip cleanup so you can inspect the workdir if a case failed.
- `--runner PATH` — override the embedded runner (default: the canonical copy alongside `remote_run.py`).

### Alternative: file-tools path (no base64 embedding)

When the slave advertises `file`, you can skip the bundled payload entirely:

```text
write_slave_file(target=slave-a, path="/tmp/mcpa/server.py",  source_path="generated_mcp/foo/v1.py")
write_slave_file(target=slave-a, path="/tmp/mcpa/cases.jsonl", source_path="generated_mcp/foo/cases.jsonl")
write_slave_file(target=slave-a, path="/tmp/mcpa/runner.py",  source_path="skills/mcp-acceptance/scripts/mcp_acceptance.py")
run_slave_bash(target=slave-a, script="python3 /tmp/mcpa/runner.py --server 'python3 /tmp/mcpa/server.py' --cases /tmp/mcpa/cases.jsonl", wait=true)
# exit code propagates: 0 = pass, 1 = case failed, 2 = server unreachable.
```

Tradeoffs vs `remote_run.py`:

| | `remote_run.py` (Option A) | file-tools (Option B) |
|---|---|---|
| Cleanup | automatic `trap` on exit | manual; survives for `read_slave_file`-based debug |
| Payload shape | one base64 shell blob | three plain file writes + one bash call |
| Re-running with edits | rebuild & re-ship payload | re-`write_slave_file` only the changed file |
| Inspect server source after run | `--keep` then `read_slave_file` | always available, no flag |
| Shell-pipeline gating | exit code from one command | exit code from final `run_slave_bash` |

Pick A for CI-like one-shot validation. Pick B when iterating on cases or expecting a failure you'll want to dig into.

## Writing Good Cases

| Cover | Why |
|---|---|
| One happy path with exact `expect_contains` | Catches calc/formatting regressions. |
| Empty / boundary input (`[]`, `0`, very large, unicode) | Catches `ZeroDivisionError`, `IndexError`, encoding bugs. |
| Schema-required field missing | Confirms `tools/call` wrapper turns exceptions into `isError:true`. |
| External-source case (when applicable) with a network sentinel | Catches "works on dev, fails behind slave's firewall". |
| Regex over numeric output | Tolerant to formatting drift; brittle exact-match is OK for stable enums. |

## Common Mistakes

| Mistake | Fix |
|---|---|
| Running `register_slave_mcp` without acceptance | `&&` gate; non-zero exit blocks it. |
| Single happy-path case only | Add at least one error case (`expect_isError: true`); confirms the error path is wired. |
| `expect_contains: ["error"]` matching ANY response | Combine with `expect_isError: true`; otherwise a success containing "no errors" passes. |
| Treating exit 2 (handshake) as exit 1 (case failure) | Distinguish in wrappers — exit 2 means fix the server, not the cases. |
| Forgetting `--cwd` for servers that load sibling files | Pass `--cwd` to the directory where the server expects relative paths. |

## Security (golden mode only)

Golden mode adds 6 mandatory checks. None are bypassable except item (d)
below, and even (d) cannot bypass (a)–(c), (e), (f).

| # | Check | What it prevents |
|---|---|---|
| (a) | `--cases <path>` must resolve inside the repo (worktree) root. | `--cases /etc/passwd` leaking file metadata via parse errors. |
| (b) | Each case's `input.path` / `input.policy_path` must resolve inside `multi-agent/`. Strict `/tmp/` prefix is the negative-case carve-out (§13 §2.3). | A malicious cases file from making the MCP tool read `/etc/passwd`. |
| (c) | `expected_error` matching is **case-sensitive substring**, hardcoded. No flag, no env var, no per-case override. | Genuine bugs surfacing `"File Not Found"` (vs the expected `"file not found"`) silently passing under a loosened matcher. |
| (d) | When `LOOM_ABLATION_NOACCEPTANCEGATE=1` is set, the runner bypasses the MCP work AND emits exactly one stderr line: `[ablation] NoAcceptanceGate: bypassed cases=<path> count=<N>`. | Silent green on a zero-row cases file; auditable trail for ablation runs. The line is **mandatory**. |
| (e) | Unknown top-level case fields → exit 2. The closed set is `{name, tool, input, expected, expected_error}`. | An attacker planting `__import__`, `pickle`, etc. that a future loose parser might honour. |
| (f) | Each `input` key must appear in the per-tool allowlist mirrored from `multi-agent/tests/eval/golden/golden_schema_test.go:toolAllowedFields`. | The "negative_service_down case adds base_url but local_echo_call has no base_url parameter" drift class. |

### Ablation bridge (Go ↔ Python)

The `LOOM_ABLATION_NOACCEPTANCEGATE` env var is the only Python ↔ Go
bridge. On the Go side, `multi-agent/internal/ablation/skill_flags.go`
registers `NoAcceptanceGate` against a package-private `*bool` target
so `ablation.Default.List()` is exhaustive. The Phase-2 CLI binder
(WT-2-flag-integration) is expected to convert `--ablation
NoAcceptanceGate` into both the Go target flip and the
`LOOM_ABLATION_NOACCEPTANCEGATE=1` env export when it forks this
Python runner.

This skill does NOT implement the binder — only honours the env var.

## Exit Codes (golden mode)

| Code | Trigger |
|---|---|
| 0 | All cases passed AND no security/schema error occurred. Also: ablation bypass success. |
| 1 | At least one case failed an assertion (deep-equal mismatch, `expected_error` substring miss, `isError` flag mismatch, tool not in `tools/list`, response not parseable as JSON, `tools/call` exception). |
| 2 | Pre-flight error — `--cases` outside repo root, cases file empty/mixed/unparseable, unknown top-level field, unknown input field, multiple distinct tools, path-traversal in `input.path` / `input.policy_path`, server handshake / `initialize` / `tools/list` failure. |

The legacy mode's `0 = pass / 1 = case failed / 2 = handshake failed`
contract is preserved unchanged.

## Related

- `scaffold-mcp-server` — Generates the protocol skeleton; pair these two skills end-to-end.
- `multiagent` references `slave-skills.md` — `register_mcp` validation scope and what it does NOT check.
- Memory `registermcp-reliability` — Background on why this skill exists.
- `docs/specs/wt1-acceptance-golden.spec.md` — Golden mode design + security mitigations.
- `multi-agent/internal/ablation/skill_flags.go` — Go side of the `NoAcceptanceGate` ablation flag.
