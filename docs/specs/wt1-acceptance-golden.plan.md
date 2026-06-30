# WT-1-acceptance-golden — Plan

> Companion to `wt1-acceptance-golden.spec.md`. Branch:
> `paper/v3/p1-acceptance-golden`. Base: `origin/paper/v3-integration @
> 17f2c3c`.

This plan converts the spec into a concrete test matrix + implementation
order. The runner uses an in-repo fixture MCP server
(`skills/mcp-acceptance/scripts/_echo_oracle_server.py`) so the pytest
suite is hermetic — no network, no compose, no real `generated_mcp/`
servers.

---

## 1. Implementation order

1. **Go side first** — `multi-agent/internal/ablation/skill_flags.go` +
   `skill_flags_test.go`. Smallest diff; lands the ablation flag in
   `Default.List()`. Verify with `go test ./internal/ablation/... -count=1
   -race`.
2. **Echo-oracle fixture server** —
   `skills/mcp-acceptance/scripts/_echo_oracle_server.py`. Tiny stdio
   MCP server that loads a cases.jsonl, advertises the (single) tool,
   and replies `expected` for the matching case or raises `expected_error`.
   The 5-family smoke loop uses this; no real MCP work is exercised.
3. **Python runner edits** — `mcp_acceptance.py`. Add `TOOL_ALLOWED_FIELDS`
   module constant, `MODULE_ROOT` constant, golden-mode loader,
   path-traversal helpers, ablation env-var bypass, deep-equal +
   case-sensitive substring response evaluator. Keep legacy mode
   bit-identical.
4. **Pytest suite** —
   `skills/mcp-acceptance/scripts/test_mcp_acceptance.py`. 14 tests
   (12 from the test matrix + 2 invariants — see §2 below).
5. **SKILL.md docs** — append golden-mode section (§3 of the spec
   reproduced as user-facing prose).
6. **Verification** — `pytest`, 5-family smoke, mutation smoke, ablation
   smoke, `go test`, `go vet`.

Doing the Go side first means we can run `go test ./internal/ablation/`
the moment that file lands and rule out a registry contract bug before
spending pytest time. The Python side is independent so any
late-discovered Go fix can land without re-running pytest.

**Verification gating (normative):** do **NOT** run `pytest
skills/mcp-acceptance/scripts/ -q` until step 3 (runner edits) has
landed. The test matrix references constants (`TOOL_ALLOWED_FIELDS`,
`PATH_TYPED_INPUT_FIELDS`, `MODULE_ROOT`) and the golden-mode loader
that only exist after step 3; running pytest earlier would produce
import errors that look like real failures. The only verification
permitted between steps 2 and 3 is `python3
skills/mcp-acceptance/scripts/_echo_oracle_server.py --help` (does the
fixture launch cleanly?) and `go test ./internal/ablation/...` (does
the Go half pass standalone?). The full matrix runs once after step 4
(pytest suite landed), and only then.

---

## 2. Test matrix (pytest)

All tests live in
`skills/mcp-acceptance/scripts/test_mcp_acceptance.py` and are invoked
via `pytest skills/mcp-acceptance/scripts/ -q`. Each row maps to one
`def test_*` function with the exact name in column 1.

| Test function | What it verifies | Security item |
|---|---|---|
| `test_cases_happy_path_5_families` | All 5 family golden cases.jsonl run to completion against the echo-oracle server with exit 0. | base — todo_list acceptance |
| `test_cases_one_fail_exit_1` | After programmatically flipping one `expected` value in a temp copy of csv-profiler/cases.jsonl, the runner exits 1 (not 2). Confirms an assertion failure is exit 1, not a pre-flight error. | base — todo_list acceptance |
| `test_cases_path_outside_module_root_exit_2` | `--cases /etc/passwd` → exit 2; stderr message contains `cases path outside module root`. | (a) |
| `test_cases_relative_traversal_exit_2` | `--cases ../../etc/passwd` (passed verbatim, so resolve() handles the `..`) → exit 2. | (a) |
| `test_input_path_traversal_rejected` | A synthetic cases.jsonl with `"input": {"path": "/etc/passwd"}` → exit 2; stderr contains `input.path path outside module root`. The MCP server is never started (the pre-flight runs before `subprocess.Popen`). | (b) |
| `test_input_path_tmp_allowed_for_negative` | A synthetic cases.jsonl with `"input": {"path": "/tmp/does-not-exist-fb73.csv"}` + `"expected_error": "file not found"` passes when the echo-oracle reports "file not found". Confirms the `/tmp/` strict-prefix carve-out lets the case reach the tool call. | (b) carve-out |
| `test_tmp_carveout_strict_prefix` | Synthetic case with `"input": {"path": "/tmpfile"}` → exit 2. Confirms `/tmpfile` is rejected (no trailing slash → no carve-out). Synthetic case with `"input": {"path": "/tmp"}` → exit 2 (bare `/tmp` is not a fixture path). | (b) carve-out edge |
| `test_unknown_case_field_rejected` | Synthetic case `{"name": "x", "tool": "csv_profile", "input": {"path": "/tmp/x"}, "expected_error": "y", "__import__": "os"}` → exit 2; stderr contains `unknown field in case` and `__import__`. | (e) |
| `test_unknown_input_field_rejected` | Synthetic case `{"name": "x", "tool": "csv_profile", "input": {"path": "/tmp/x", "base_url": "evil"}, "expected_error": "y"}` → exit 2; stderr contains `input field "base_url" not allowed for tool "csv_profile"`. (csv_profile only allows `path`.) | (f) |
| `test_expected_error_case_sensitive` | Build a synthetic cases.jsonl in the per-session `_pytest_tmp/` dir with one case `{"name": "miscased", "tool": "csv_profile", "input": {"path": "/tmp/x.csv"}, "expected_error": "file not found"}`. Spawn the echo-oracle with `--force-error-text "File Not Found"` (a fixture-only flag, see §2.2). The oracle then unconditionally returns `isError: true, content: [{text: "File Not Found"}]` for every `tools/call`. Runner exits 1 with reasons line containing `expect_error miss: needle="file not found" in haystack="File Not Found"`. Confirms no `.lower()` / regex / fuzzy collapse. | (c) |
| `test_ablation_bypass_logs_to_stderr` | `LOOM_ABLATION_NOACCEPTANCEGATE=1` with a real cases path + a fake `--server "true"` (which would otherwise be exit 2 on handshake) → exit 0, stderr matches regex `^\[ablation\] NoAcceptanceGate: bypassed cases=.+ count=\d+$` (single line). | (d) |
| `test_ablation_bypass_still_checks_path` | `LOOM_ABLATION_NOACCEPTANCEGATE=1 --cases /etc/passwd` → still exit 2 (path check precedes bypass). | (a)+(d) ordering |
| `test_ablation_bypass_still_checks_relative_path` | `LOOM_ABLATION_NOACCEPTANCEGATE=1 --cases ../../etc/passwd` → still exit 2. Covers the relative-traversal variant of the ablation ordering invariant called out in spec §6 — the absolute-path case alone would let a `if path.startswith("/"): check_absolute()` regression silently pass. | (a)+(d) ordering, relative variant |
| `test_multi_tool_in_cases_rejected` | Synthetic cases.jsonl with two distinct `tool` values → exit 2; stderr contains `cases file declares multiple tools`. | §13 §2.5 #1 |
| `test_allowlist_in_sync_with_go_source` | Parse `multi-agent/tests/eval/golden/golden_schema_test.go`'s `toolAllowedFields` literal with a small regex sweeper; assert byte-for-byte equality with `TOOL_ALLOWED_FIELDS` in `mcp_acceptance.py`. Drift guard. | (f) drift |
| `test_path_typed_fields_locked` | Assert `PATH_TYPED_INPUT_FIELDS` constant in `mcp_acceptance.py` equals exactly `{"path", "policy_path"}`. Spec §3 (b) closed-list invariant. | (b) lock |
| `test_legacy_mode_still_works` | Pass `skills/mcp-acceptance/scripts/example_cases.jsonl` (legacy `args` / `expect_*`) — exit 0 against a tiny `_echo_args_server.py` stub. Confirms backward compatibility. | regression |

**Test count: 17** (the prompt sketched 12; we have 17 because: three are essential drift guards — `test_allowlist_in_sync_with_go_source`, `test_path_typed_fields_locked`, `test_legacy_mode_still_works`; one is a carve-out edge case `test_tmp_carveout_strict_prefix`; and one is the relative-variant of the ablation ordering invariant called out in spec §6, `test_ablation_bypass_still_checks_relative_path`. None are removable; each defends a distinct invariant from the spec.)

### 2.1 Fixture helpers

A `conftest.py` (also new, also under `scripts/`) provides:

- `MODULE_ROOT` resolved from `tests/eval/golden/` upward.
- `synth_cases(tmp_path, lines: list[dict]) -> Path` — write a cases.jsonl
  inside `tmp_path` but **also inside `MODULE_ROOT`** (we create
  `multi-agent/tests/eval/golden/_pytest_tmp/` per-session, symlinked
  into `tmp_path`). Required so synthetic cases survive the `--cases`
  path-traversal check while staying inside the test's tmpdir for
  cleanup.

  Actually — simpler approach: the synth helper writes to
  `MODULE_ROOT / "tests/eval/golden/_pytest_tmp" / f"{test_id}.jsonl"`
  and the conftest fixture uses pytest's autouse cleanup to remove the
  `_pytest_tmp` dir at session teardown. No symlinks. This keeps the
  drift guard `TestNoUnexpectedFamilies` happy because the dir starts
  with `_` (per `golden_schema_test.go` line 130:
  `if strings.HasPrefix(e.Name(), "_") { continue }`).

- `echo_oracle(tool: str, cases: list[dict]) -> subprocess.Popen-able
  command` — returns the argv list that spawns the fixture server with
  the right `--cases` flag.

### 2.2 Echo-oracle MCP server design

`_echo_oracle_server.py` is a tiny stdio JSON-RPC MCP server (~120
lines). Reads its own `--cases <path>` on startup (a copy of whatever
cases file the test/smoke uses), advertises a single tool whose name is
the cases file's single `tool` value, and on each `tools/call`:

1. Find the matching case by deep-equaling the incoming `arguments`
   against each loaded case's `input`. (Deterministic; one match per
   case input.)
2. If the case has `expected`: return
   `{"content": [], "structuredContent": <expected>, "isError": false}`.
3. If the case has `expected_error`: return
   `{"content": [{"type": "text", "text": <expected_error>}], "isError": true}`.
4. If no matching case is found: return `isError: true` with text
   `"echo-oracle: no matching case for arguments=<json>"`.

The oracle also accepts a **fixture-only** `--force-error-text <str>`
flag: when set, it ignores its loaded cases and unconditionally returns
`{"isError": true, "content": [{"type": "text", "text": <str>}]}` for
every `tools/call`. This flag exists exclusively to drive
`test_expected_error_case_sensitive` and is not documented in
SKILL.md (no real workload should use it).

The echo-oracle never calls out to the real MCP tool implementations, so
the 5-family smoke loop validates the *runner* end-to-end without
needing the actual `csv_profile` / `parse_access_log` / etc. servers
from Phase 2.

### 2.3 5-family smoke (driven from a pytest test)

`test_cases_happy_path_5_families` is parametrised over the 5 family
directories. For each family it:

1. Spawns the echo-oracle server with `--cases <fam>/acceptance/cases.jsonl`.
2. Runs the runner with the same `--cases` path.
3. Asserts exit 0.

This is the in-pytest realisation of the spec §5 shell smoke loop.

### 2.4 Mutation test

`test_cases_one_fail_exit_1` copies `csv-profiler/acceptance/cases.jsonl`
into the per-session `_pytest_tmp/` dir, flips the first `"rows": 5` to
`"rows": 999`, and runs the runner. Asserts exit code is exactly 1 (not
2), and stderr/stdout contains a fail-reason mentioning the mismatch.

---

## 3. Verification commands (must all pass)

```bash
cd <repo-root>  # e.g. /root/multi-agent/.worktrees/p1-acceptance-golden

# Python pytest suite — 33 tests (17 + 16 added by PR #57 review rounds)
pytest skills/mcp-acceptance/scripts/ -q

# 5-family golden smoke (driven by pytest above, but also runnable
# standalone for shell-level reproducibility):
for f in multi-agent/tests/eval/golden/*/acceptance/cases.jsonl; do
  python3 skills/mcp-acceptance/scripts/mcp_acceptance.py \
      --server "python3 skills/mcp-acceptance/scripts/_echo_oracle_server.py --cases $f" \
      --cases "$f" || { echo "FAIL: $f"; exit 1; }
done

# Path-traversal smoke
python3 skills/mcp-acceptance/scripts/mcp_acceptance.py \
    --server "true" --cases /etc/passwd ; test $? -eq 2 || exit 1

# Ablation bypass smoke
LOOM_ABLATION_NOACCEPTANCEGATE=1 python3 \
    skills/mcp-acceptance/scripts/mcp_acceptance.py \
    --server "true" \
    --cases multi-agent/tests/eval/golden/csv-profiler/acceptance/cases.jsonl \
    2>&1 | grep -q "^\[ablation\] NoAcceptanceGate: bypassed cases=" || exit 1

# Go side
cd multi-agent
go test ./internal/ablation/... -count=1 -race
go vet ./...
```

---

## 4. Subagent / Codex review checklist

Three Codex review rounds, one per stage. Each uses
`--output-last-message /tmp/codex-wt1-acceptance-golden.lastmsg`.

| Stage | Codex prompt outline | P0 trigger |
|---|---|---|
| Spec | "Review against todo_list row, §2.3/§2.5, scaffold, golden_schema_test.go, ablation registry contracts." | Spec contradicts §2.3/§2.5, omits any §3 mitigation, allows `--cases /etc/passwd`, or proposes a Go-side change that breaks ablation registry contracts. |
| Plan | "Review plan against spec." | Test matrix misses any §3 (a)–(f) mitigation or any §13 §2.5 invariant; missing drift guard. |
| Code | "Review diff." | Path traversal accepted; unknown field passes through; `expected_error` matched loosely; ablation silent bypass; legacy-mode regression. |

The plan review's P0 trigger is the strictest because the test matrix is
the security contract's executable form — any missing row means the
corresponding mitigation has zero CI coverage.

---

## 5. Open risks

- **Symlink races inside `_pytest_tmp/`:** because the per-session temp
  dir lives inside `MODULE_ROOT`, a concurrently-running `pytest
  -p xdist -n auto` could race on test-case filename collisions. The
  conftest uses `tmp_path` (pytest's per-test tmpdir) hashed into the
  filename, so two test workers never write the same file. Single-test
  re-runs use the same hash → deterministic clobber by the second
  worker, which is the desired pytest semantics. (Verification: each
  synth helper accepts `tmp_path` as the first arg; the hash is
  `hashlib.sha1(str(tmp_path).encode()).hexdigest()[:12]`.)
- **`MODULE_ROOT` resolution at install time:** if someone moves
  `mcp_acceptance.py` outside `skills/mcp-acceptance/scripts/`, the
  `Path(__file__).resolve().parents[3] / "multi-agent"` derivation
  breaks. Mitigation: the runner aborts at exit 2 with
  `RuntimeError("MODULE_ROOT not found: ...")` (spec §2.3). Test
  `test_module_root_must_exist` is **not** in the matrix above because
  the test setup itself depends on MODULE_ROOT existing; the failure
  mode is obvious (every test crashes at import time) so a dedicated
  guard would be tautological.
- **Subprocess cleanup on test failure:** the existing runner's
  `ServerSession.stop()` already does best-effort kill + wait. The
  echo-oracle inherits the same lifecycle (it reads stdin / writes
  stdout exactly like the legacy code path). No new cleanup risk.

---

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
