# WT-1-acceptance-golden — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 1 table row
> **WT-1-acceptance-golden** (P1).
> Branch: `paper/v3/p1-acceptance-golden`.
> Base: `origin/paper/v3-integration @ 17f2c3c` (already includes
> WT-1-ablation-registry merge).
> Consumes: `multi-agent/tests/eval/golden/<family>/acceptance/cases.jsonl`
> from Phase 0 WT-0-task-families (PR #50).
> Companion documents: `/root/paper_writing/docs/intermediate/13_workload_spec.md`
> §2.3 + §2.5, and `multi-agent/tests/eval/golden/golden_schema_test.go`
> (source of `toolAllowedFields`).

---

## 1. Task boundary & file scope

Five files are owned by this worktree; nothing else is touched.

| Path | Action |
|---|---|
| `skills/mcp-acceptance/scripts/mcp_acceptance.py` | **edit** — add `--cases <jsonl>` parsing mode + 6 security checks + ablation env bridge |
| `skills/mcp-acceptance/scripts/test_mcp_acceptance.py` | **new** — pytest suite covering the 12-row matrix in the Plan doc |
| `skills/mcp-acceptance/scripts/_echo_oracle_server.py` | **new** — fixture MCP server used by pytest + the 5-family smoke loop; reflects each case's `expected` / raises `expected_error` |
| `skills/mcp-acceptance/SKILL.md` | **edit** — append a `--cases` golden-mode section with security notes |
| `multi-agent/internal/ablation/skill_flags.go` | **new** — `init()` registers `NoAcceptanceGate` against a package-local `*bool` target, surfacing it in `ablation.Default.List()` |

Hard rules:

- **Do not modify** any `tests/eval/golden/*/acceptance/cases.jsonl` (we are
  the consumer of those files, not the producer).
- **Do not modify** any file under `multi-agent/internal/ablation/` other
  than the brand-new `skill_flags.go`. The existing
  `{doc.go,registry.go,errors.go,registry_test.go}` quartet from
  WT-1-ablation-registry stays untouched — we only add a sibling file.
- **Do not modify** `multi-agent/tests/eval/golden/golden_schema_test.go`
  (the source of truth for `toolAllowedFields` and the closed family
  set). We mirror its
  `toolAllowedFields` map verbatim in Python; the Go side stays the source
  of truth. (Verification: the pytest suite includes a one-shot syntactic
  parse of the Go map and fails loudly if the two diverge — see Plan
  matrix row `test_allowlist_in_sync_with_go_source`.)
- **No `go.mod` changes.** The new Go file imports only `package ablation`
  internals.
- **No additions to** `multi-agent/python/pyproject.toml`. The runner stays
  stdlib-only (it already is); pytest is a dev-only dependency that runs
  out of the repo's existing test toolchain.

### 1.1 Backward compatibility with the legacy case format

The runner today accepts the legacy schema documented in `SKILL.md`:

```jsonl
{"name": "...", "tool": "...", "args": {...}, "expect_contains": [...], "expect_isError": ...}
```

The Phase 0 golden case files use the §2.3 schema instead:

```jsonl
{"name": "...", "tool": "...", "input": {...}, "expected": {...}}
{"name": "...", "tool": "...", "input": {...}, "expected_error": "..."}
```

Mode selection is **automatic, per case file**:

- If **every non-comment line** in the file has `"input"` (not `"args"`)
  and exactly one of `"expected"` / `"expected_error"`, the file is
  parsed in *golden mode* (§2 below).
- If **every non-comment line** has `"args"` (the legacy contract), the
  file is parsed in *legacy mode* (existing behaviour, unchanged).
- If lines are mixed, exit 2 with
  `ValueError("cases file mixes golden ({input,expected}) and legacy ({args,expect_*}) shapes; pick one")`.
  This is intentionally strict: a half-converted file is the most likely
  source of silently-skipped assertions.

Both modes share the same `--cases <path>` flag; *there is no
`--cases-format` switch*. The auto-detect rule above is exhaustive and
deterministic, so a CLI flag would only add a way to disagree with the
file's actual contents.

---

## 2. Golden-mode `--cases` contract

### 2.1 Case schema (§13 §2.3 verbatim)

Each non-blank, non-`#`-prefixed line is one JSON object with this closed
field set:

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | ✅ | Case slug (snake_case); used for report aggregation. |
| `tool` | string | ✅ | Tool name; **only one distinct value is allowed across the whole file** (§13 §2.5 #1, also enforced by `golden_schema_test.go:TestAcceptanceCasesAreValid`). |
| `input` | object | ✅ | Passed to the MCP tool as `arguments` in `tools/call`. Each key MUST appear in the per-tool allowlist (§2.5 #3, §3 (f)). |
| `expected` | any JSON value | ⛔ exclusive with `expected_error` | On a non-error MCP response, the runner does **deep-equal JSON comparison** against the tool's structured result (see §2.4). |
| `expected_error` | string | ⛔ exclusive with `expected` | On an `isError: true` MCP response, the runner does **case-sensitive substring** matching against the joined MCP error message (§13 §2.5 #4). |

Any other top-level field is rejected (§3 (e)).

### 2.2 File-level invariants checked at load time

1. **Single tool** (§13 §2.5 #1). The runner walks all loaded cases; if
   more than one distinct `tool` value appears, exit 2 with
   `ValueError("cases file declares multiple tools: ...")`.
2. **No blank lines except trailing newline.** A line that is `""` or
   only-whitespace in the middle of the file is parser-tolerated (skipped
   with a stderr warning) for parity with `multi-agent/tests/eval/golden/golden_schema_test.go`'s
   tolerance of `strings.TrimSpace(line) == ""` — but a *non-blank,
   non-comment* line that fails to parse as JSON is exit 2.
3. **At least one case loaded.** An empty cases file (zero cases after
   skipping blanks/comments) is exit 2 — silent green on a zero-row file
   is precisely what the §3 (d) mitigation guards against.

### 2.3 Path resolution

There are **two** containment roots, not one — one for `--cases` itself
and one for in-case path-typed fields:

1. **`REPO_ROOT`** = the worktree / repo root. Derived from `__file__`:
   the runner lives at
   `<repo>/skills/mcp-acceptance/scripts/mcp_acceptance.py`, so
   `REPO_ROOT = Path(__file__).resolve().parents[3]`. The `--cases
   <path>` flag must resolve inside `REPO_ROOT`. Rationale: legacy-mode
   cases files (e.g. the shipped `example_cases.jsonl`) live in
   `skills/mcp-acceptance/scripts/`, which is a sibling of
   `multi-agent/`, not a child. Tying `--cases` to MODULE_ROOT would
   break legacy-mode backward compatibility (§1.1).
2. **`MODULE_ROOT`** = `REPO_ROOT / "multi-agent"`, the Go module root.
   Every in-case path-typed field (currently `input.path` and
   `input.policy_path`; the closed allowlist lives in the Python
   runner) must resolve inside `MODULE_ROOT`. This is the stricter
   boundary because golden fixtures all ship under
   `multi-agent/tests/eval/golden/`; the carve-out below is the only
   way to escape it.

Both roots are load-time constants; if either cannot be located on disk
the runner aborts with exit 2 (`MODULE_ROOT could not be derived from
runner path …`).

For both `--cases <path>` and every path-typed in-case field:

- If the value starts with `/tmp/` *as a strict prefix* (`len(value) > 5
  and value[:5] == "/tmp/"`), accept it verbatim and DO NOT resolve it
  against either root. This is the §13 §2.3 carve-out for negative-case
  fixtures that intentionally do not exist on disk; the MCP tool is
  expected to surface `file not found`. The strictness is intentional:
  `/tmp` alone, `/tmpfile`, `/tmp/../etc/passwd` (resolved form),
  etc. are all rejected by the containment check. The carve-out
  applies to **in-case** path fields only — `--cases` itself is not
  allowed under `/tmp/` (operator must commit the cases file to the
  repo).
- Otherwise, resolve relative to the appropriate root if the value is
  relative, or take the absolute path as-is. Then call
  `Path(...).resolve()` and assert containment under the appropriate
  root. On failure, exit 2.

The runner does **not** stat the file — it is legitimate for a case to
reference a path that the MCP tool itself will discover is missing
(that is the whole point of `expected_error: "file not found"`).
`Path.resolve()` is run with `strict=False` and we only check the
*resolved string* against the root. Pure-string comparison is adequate
because `resolve()` already collapses `..` segments.

### 2.4 Response comparison

After a non-error MCP response, the runner extracts the structured result
in this order of preference:

1. If `result.structuredContent` is present in the JSON-RPC response, use
   it as the comparison subject.
2. Otherwise, concatenate all `result.content[*].text` blocks, parse the
   joined string as JSON, and use that as the comparison subject. If the
   text is not valid JSON, the case fails with reason `response is not
   structured JSON and content text is not parseable: <err>`.

The case passes iff the comparison subject deep-equals `expected`. Deep
equality is recursive: dict keys must match exactly (no extra, none
missing) and lists are order-sensitive. Numeric tolerance and partial-key
modes (`required_keys` carve-out per family README) are explicitly **not**
implemented in this worktree — those are §13 §2.5 #5 carve-outs and live
in the per-family README; B3 v1 keeps the runner strict and consumes the
golden files as authored. (Rationale: every Phase-0 case file shipped in
PR #50 either uses strict deep-equal or has a `required_keys` field that
already shapes the `expected` object such that strict deep-equal is the
intended check.)

`expected_error` matching: on an `isError: true` response, joined
`content[*].text` is the haystack and `expected_error` is the needle.
The check is `needle in haystack` — case-sensitive substring,
non-anchored, non-regex. See §3 (c) for why this is hardcoded.

### 2.5 Exit codes (the gate contract — normative)

This table is the **single source of truth** for `mcp_acceptance.py`'s
exit code in golden mode. The terser "0/1/2 = pass/case/handshake" line
in `skills/mcp-acceptance/scripts/remote_run.py`'s docstring is a legacy
abbreviation that pre-dates this spec; it stays accurate as far as it
goes (legacy mode is unchanged, and golden mode's exit 2 still includes
handshake failure), but this table — not that docstring — defines the
behaviour for golden mode.

| Code | Trigger | Examples (non-exhaustive) |
|---|---|---|
| 0 | All cases passed AND no security/schema error occurred. | All 6 csv-profiler cases match. With `LOOM_ABLATION_NOACCEPTANCEGATE=1`, also after a successful bypass-log emission. |
| 1 | At least one case failed an *assertion*. | `expected` deep-equal mismatch; `expected_error` substring miss; `isError` flag mismatch; tool not in `tools/list`; response text not parseable as JSON; case-level `tools/call` exception (timeout, transport error). |
| 2 | Pre-flight / usage / load / validation / runtime-infra error — work never made it to per-case assertions. | `--cases <path>` outside `MODULE_ROOT` or non-existent; cases file empty, mixed-shape, or unparseable JSON; unknown top-level case field; unknown `input` field; multiple distinct `tool` values; path-traversal in `input.path` / `input.policy_path`; server handshake / `initialize` / `tools/list` failure; `MODULE_ROOT` cannot be located on disk. |

Exit 1 vs 2 lets the downstream shell wrapper distinguish "the server is
buggy" (exit 1 → fix the server) from "the cases file or environment is
malformed / hostile" (exit 2 → fix the cases or operator setup).

Per-mode exit-code parity with legacy mode: the existing legacy-mode
handshake-failure path (server crashes before responding or `tools/list`
never returns) is preserved as exit 2 in both modes. Existing legacy
mode exit-1 semantics (assertion failure) are likewise preserved.

---

## 3. Security mitigations (mandatory)

Each item below is a runtime check that the Plan doc's pytest matrix
covers by name.

### (a) `--cases` path-traversal

`--cases <path>` is operator-supplied. A hostile or sloppy operator could
pass `/etc/passwd` (absolute) or `../../etc/passwd` (relative) and rely
on a future parser to leak file contents in a JSON-parse error message.

**Mitigation:** before opening the file, run
`Path(path).resolve()` and assert containment inside `REPO_ROOT` (see
§2.3 for the two-root rationale). On failure, exit 2 with the message
`cases path outside repo root: <given> → <resolved>` — the resolved
absolute path is included so the operator can diagnose a stray symlink,
but no file *contents* are ever read or echoed.

The carve-out for `/tmp/` paths described in §2.3 applies to **in-case**
path fields, not to `--cases` itself. The cases file MUST live in the
repo. (Rationale: a stray cases file in `/tmp/` belongs to whoever wrote
it; we won't accept it.)

### (b) In-case path-traversal

Each case's `input.path` / `input.policy_path` is a perfectly normal
attacker-controlled string from the moment the cases file is opened. A
malicious cases.jsonl could embed `"path": "/etc/passwd"` and rely on the
MCP tool to dutifully read it.

**Mitigation rule (normative):** before the `tools/call` for a case
fires, the runner inspects every path-typed field in `input` and applies
this decision tree, exit-2-ing immediately on any rejection:

1. If the string starts with `/tmp/` *as a strict prefix* (i.e.
   `len(value) > 5 and value[:5] == "/tmp/"`), accept the value
   verbatim. No path-traversal check; no `.resolve()` call. This is the
   §13 §2.3 negative-case carve-out for non-existent fixtures.
2. Otherwise, compute `resolved = Path(value).resolve(strict=False)` —
   if `value` is relative it is first joined to `MODULE_ROOT`. Then
   require `resolved.is_relative_to(MODULE_ROOT)`. On failure, exit 2
   with `ValueError("input.<field> path outside module root: <given> → <resolved>")`.

**Concrete reject examples (must all exit 2):**

| Value | Why rejected |
|---|---|
| `"/etc/passwd"` | absolute, outside MODULE_ROOT |
| `"../../etc/passwd"` | relative, resolves outside MODULE_ROOT |
| `"tests/eval/golden/../../../etc/passwd"` | repo-relative-looking but resolves outside |
| `"/tmp"` | exact `/tmp` — `/tmp/` prefix check is *strict*; bare `/tmp` is not a fixture path |
| `"/tmp/../etc/passwd"` | starts with `/tmp/` but resolves outside MODULE_ROOT — the carve-out is **prefix-only**, not "anything that *resolves* under /tmp". Implementation note: the carve-out short-circuits the resolve check, so this string is *accepted* by the path layer (bullet 1 above takes the value verbatim). The MCP tool itself, when it eventually `os.path.realpath()`s and reads it, surfaces a normal `file not found` or permission error. This is acceptable because the prefix check guarantees the literal *string* never escapes `/tmp/...`; the MCP tool may not pass it to a privileged operation. If a future MCP tool *does* perform a privileged op on its `input.path` (e.g. read-as-root), that tool's own input validation is responsible — the same way it would be for any cases.jsonl-driven call. The §3 (b) check defends the runner, not every downstream tool. |
| `"/tmpfile"` | starts with `/tmp` but no trailing slash — fails the strict prefix |

**Concrete accept examples (must NOT exit 2 at the path layer):**

| Value | Why accepted |
|---|---|
| `"tests/eval/golden/csv-profiler/first-task/input/sales.csv"` | relative, resolves inside MODULE_ROOT |
| `"/tmp/does-not-exist-fb73.csv"` | matches `/tmp/` strict prefix — `expected_error: "file not found"` is the intended outcome |
| `"/root/multi-agent/tests/eval/golden/.../sales.csv"` *(absolute form of the relative one)* | absolute, resolves inside MODULE_ROOT |

The path-typed field list is closed: `{"path", "policy_path"}`. Adding a
new path-typed field requires editing both the Python runner and this
spec. The closed list is one of the quantities pinned by the pytest
case `test_path_typed_fields_locked` (Plan matrix).

### (c) `expected_error` matching is hard-coded case-sensitive substring

`expected_error` is the security contract for negative cases.

**Mitigation:** the matcher is hard-coded `needle in haystack` in
the Python source. There is no flag, no env var, no per-case override to
weaken it. A pytest case (`test_expected_error_case_sensitive`)
constructs a server response that differs only in case and verifies the
case fails.

The risk if this were ever loosened to `.lower()` or `re.search`: a
genuine bug surfacing `"File Not Found"` instead of `"file not found"`
would silently pass under a `.lower()` matcher, masking a regression in
the MCP error path. (Same class of bug as `multi-agent/tests/eval/golden/golden_schema_test.go`'s
strictness on the spec → cases tool-name pairing.)

### (d) Ablation bypass logs to stderr

When `LOOM_ABLATION_NOACCEPTANCEGATE=1` is set in the environment, the
runner skips the actual MCP handshake / `tools/call` work and exits 0 —
this is the experimental knob that asks "what does the system look like
without the acceptance gate?"

**Mitigation:** before the bypass exit, the runner emits exactly one
single line to stderr:

```text
[ablation] NoAcceptanceGate: bypassed cases=<resolved-path> count=<N>
```

where `<N>` is the number of *successfully-parsed* cases. The log line
is **mandatory** — silent green on a zero-row file is the canonical
attack against any gate-bypass switch, and the log line gives operator
audit a single grep target.

The bypass executes **after** all schema and security checks (a)–(c),
(e), (f). In particular, `LOOM_ABLATION_NOACCEPTANCEGATE=1` combined with
`--cases /etc/passwd` still exits 2, not 0 — see (a) for why this
ordering is non-negotiable.

The environment variable name is the only Python ↔ Go bridge. The Go
side registers `ablation.NoAcceptanceGate` against a package-local *bool
target (see §4 below) so `ablation.Default.List()` includes it; the
Phase-2 CLI binder (WT-2-flag-integration) is expected to convert
`--ablation NoAcceptanceGate` into both the Go target flip AND the
`LOOM_ABLATION_NOACCEPTANCEGATE=1` env export when it forks the Python
runner. **This worktree does not implement the env export from the
binder** — that wiring is the binder's job — but the Python runner
honours the env var unconditionally so the contract is in place.

### (e) Unknown top-level case fields are rejected

Without this check, an attacker who can write to a cases file could
plant a stray top-level key like `__import__`, `pickle`, or `eval`. A
future *loose* parser (e.g. someone refactoring the runner to use
`pydantic.BaseModel.parse_obj` with `extra="allow"`) might suddenly
honour that key.

**Mitigation:** at load time, every case is checked against the closed
field set `{"name", "tool", "input", "expected", "expected_error"}`. Any
extra key → exit 2 with `ValueError("unknown field in case <name>: <key>")`.
The field set is mirrored 1:1 from `multi-agent/tests/eval/golden/golden_schema_test.go`'s
`acceptanceCase` struct + `json.Decoder.DisallowUnknownFields()`.

### (f) Unknown `input` keys are rejected against the per-tool allowlist

The per-tool input-field allowlist lives in
`multi-agent/tests/eval/golden/golden_schema_test.go:toolAllowedFields`. If the Python runner accepts
keys outside that set, a cases file can quietly carry data that the MCP
tool either ignores or — worse — interprets unexpectedly (the
`base_url` redirect class). Worse still, the underlying MCP `tools/call`
contract treats unknown keys per the server's own validation, which in
practice ranges from "rejected" to "silently dropped".

**Mitigation:** the runner ships a Python copy of `toolAllowedFields`
keyed by tool name. Each case's `input` keys must be a subset of
`toolAllowedFields[tool]`. Unknown tool name → exit 2 with
`ValueError("tool <name> has no input allowlist; update toolAllowedFields in multi-agent/tests/eval/golden/golden_schema_test.go and re-sync the Python mirror")`.
Unknown input key → exit 2 with
`ValueError("input field <key> not allowed for tool <name> (allowed: <sorted list>)")`.

**Drift guard:** a pytest case
(`test_allowlist_in_sync_with_go_source`) parses
`multi-agent/tests/eval/golden/golden_schema_test.go`'s `toolAllowedFields` literal with a small regex
sweeper and asserts byte-for-byte equality with the Python mirror. Any
edit to one side without the other fails CI.

---

## 4. Go-side ablation flag registration

The runner is Python and the ablation registry is Go; they cannot share
state directly. The bridge is two halves:

- **Go half** (this worktree): `multi-agent/internal/ablation/skill_flags.go`
  is a brand-new file in the same package. Its `init()` calls

  ```go
  if err := Default.Register(NoAcceptanceGate, &noAcceptanceGate); err != nil {
      panic(fmt.Errorf("ablation: registering NoAcceptanceGate: %w", err))
  }
  ```

  against a package-private `var noAcceptanceGate bool`. Effects:
  - `ablation.Default.List()` now includes `NoAcceptanceGate`.
  - `ablation.Default.SetByName("NoAcceptanceGate", true)` flips
    `noAcceptanceGate` to true.
  - A future Phase-2 CLI binder reads `noAcceptanceGate` (via an
    exported accessor `IsNoAcceptanceGate() bool` defined in the same
    file) and exports `LOOM_ABLATION_NOACCEPTANCEGATE=1` into the
    child Python process's environment.

  Why a panic on Register error: the registry only fails Register on
  programmer error — unknown FlagName (a typo here is a compile-time
  failure because `NoAcceptanceGate` is a typed constant), nil target
  (we pass `&noAcceptanceGate`, not nil), or duplicate target. A panic
  at init-time surfaces the bug before any test runs. The existing
  ablation registry's contract (`registry.go` comment block) explicitly
  says **Register never panics**; that contract is about the *registry
  call itself* — we are panicking on our wrapping check, which is the
  documented escalation pattern (`Default.Register` returns an error;
  the caller decides what to do with it).

- **Python half** (this worktree): unconditional `os.environ.get(
  "LOOM_ABLATION_NOACCEPTANCEGATE") == "1"` check at entry to `main()`.

The two halves are intentionally loosely coupled by **just an env var
name**. The string `LOOM_ABLATION_NOACCEPTANCEGATE` appears in exactly
two places: `mcp_acceptance.py` and `skill_flags.go`. Both ship pytest
/ Go test coverage that fails if either is renamed without the other.

Why a new `skill_flags.go` file rather than adding to a future Python-skill
init pattern in `internal/ablation/`: this worktree is the first Python
skill to register an ablation flag, so it is also the first to need the
file. The file name (`skill_flags.go`) and the convention "future
Python-skill ablation flags also go here, each its own
`var <flag>Gate bool` + `init()` Register call" is documented as a
single 5-line `//` comment block at the top of the new file. We are
**not** modifying `doc.go`, `registry.go`, `errors.go`, or
`registry_test.go`; those are WT-1-ablation-registry's territory.

The Go tests `TestKnownFlagsRegistered` (new, sibling test file
`multi-agent/internal/ablation/skill_flags_test.go`) and existing
`TestKnownFlags_CopyIsolation` are both expected to pass; the new test
asserts `Default.List()` contains `NoAcceptanceGate` after package init.

---

## 5. Verification (must run, must pass)

```bash
cd multi-agent/.worktrees/p1-acceptance-golden

# Python pytest suite — full matrix
pytest skills/mcp-acceptance/scripts/ -q

# 5-family smoke loop using the echo-oracle fixture server
for fam in multi-agent/tests/eval/golden/*/acceptance/cases.jsonl; do
  python3 skills/mcp-acceptance/scripts/mcp_acceptance.py \
      --server "python3 skills/mcp-acceptance/scripts/_echo_oracle_server.py --cases $fam" \
      --cases "$fam" \
      --cwd multi-agent
done

# Mutation: change one expected value in csv-profiler and re-run → exit 1
# (covered by pytest test_cases_one_fail_exit_1)

# Ablation bypass smoke (must exit 0, write log line)
LOOM_ABLATION_NOACCEPTANCEGATE=1 python3 \
    skills/mcp-acceptance/scripts/mcp_acceptance.py \
    --server "true" \
    --cases multi-agent/tests/eval/golden/csv-profiler/acceptance/cases.jsonl

# Path-traversal smoke (must exit 2)
python3 skills/mcp-acceptance/scripts/mcp_acceptance.py \
    --server "true" --cases /etc/passwd ; test $? -eq 2

# Go side
cd multi-agent
go test ./internal/ablation/... -count=1 -race
go vet ./...
```

All four blocks must pass; the spec is rejected if any one is skipped.

---

## 6. Acceptance (todo_list row)

The todo_list row's acceptance criterion is:

> 5 个 family 的 golden 全过；故意改坏 cases 后 exit 1

This spec adds (mandatory):

- `--cases /etc/passwd` → exit 2 (security item (a))
- `--cases ../../etc/passwd` → exit 2 (security item (a), relative variant)
- `LOOM_ABLATION_NOACCEPTANCEGATE=1` → exit 0 AND stderr contains the
  documented `[ablation] NoAcceptanceGate: bypassed cases=... count=...`
  line (security item (d))
- `LOOM_ABLATION_NOACCEPTANCEGATE=1 --cases /etc/passwd` → still exit 2
  (security items (a)+(d), ordering invariant)
- Mutated cases file (one `expected` value flipped) → exit 1
- `go test ./internal/ablation/...` → all pass, including the new
  `skill_flags_test.go`

---

## 7. Open questions / explicit non-goals

- **`required_keys` partial-match mode is not implemented in v1.** Every
  Phase-0 case ships either strict deep-equal or pre-shaped `expected`
  objects. A future worktree can add it without changing the §2.5 exit
  codes if it lands the carve-out behind an explicit per-family README
  declaration (per §13 §2.5 #5). This spec deliberately defers; doing it
  here would couple us to the per-family README format which is owned by
  the Phase-0 worktree.
- **Numeric tolerance for floats** (e.g. csv-profiler's mean) is also
  deferred. The current Phase-0 case files only ship integer-valued
  `expected` numbers, so strict equality is exhaustively safe for the
  acceptance gate today. If a future case ships floats, the case file's
  author is responsible for either (a) rounding the expected value to an
  integer or (b) waiting for the partial-match worktree.
- **No `--cases-format` switch.** Auto-detection per §1.1 is exhaustive;
  a flag would only introduce a way to disagree with the file's contents.
- **No `--strict` / `--lax` mode.** Every check in §3 is mandatory. The
  ablation env var is the only documented bypass and it bypasses *only*
  the post-load MCP work, not the security pre-flight.
- **`ServerSession.send_request()` blocking-readline timeout bug is
  pre-existing and not fixed here.** The current legacy implementation
  (inherited from the pre-WT-1 mcp_acceptance.py at
  `origin/paper/v3-integration` HEAD) computes `deadline = time.monotonic()
  + timeout` and then calls a blocking `self.proc.stdout.readline()`; if
  the server never writes a line, readline blocks indefinitely and the
  deadline check after it never fires. Fixing this requires either a
  selector-driven read or a threaded read queue — a transport-layer
  refactor with its own test surface. This worktree's mandate is the
  `--cases` golden-mode + security mitigations; the transport refactor
  is deferred to a future worktree (suggested name:
  `paper/v3/pX-acceptance-transport-timeout`). The bug does not affect
  any of this worktree's verification: every test uses either the
  well-behaved echo-oracle or `--server "true"` (which exits
  immediately, hitting the `line == ""` zero-byte branch — not the
  timeout branch). A hung remote MCP server in production today would
  hang this runner indefinitely, same as before this worktree.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
