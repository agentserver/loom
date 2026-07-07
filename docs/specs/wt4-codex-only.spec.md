# WT-4-codex-only — Spec

> Phase 4 §"Phase 4 交付物 = 5 个 per-workload 运行脚本 + 3 个环境改造 worktree"
> (docs/final/prompts/p4-experiment-plan.md v1.1 in the paper_writing repo).
> Worktree `paper/v4/codex-only` at
> `/root/multi-agent/.worktrees/paper-v4-codex-only`.
> Baseline: `origin/paper/v3-integration` HEAD `786bf60` (verified Phase 3
> full harness merged: PR #77 stub-fulltable + PR #79 prod-multidevice +
> PR #76 mini-case + WT-2-baselines).
> Scope (this repo only):
> `multi-agent/tests/eval/baselines/single_machine_codex/` (NEW) +
> `multi-agent/tools/eval/fulltable/{matrix.yaml, matrix.schema.json,
> lib/plan.py, tests/*, build_paper_tables.py}` (SWAP claude→codex enum) +
> `multi-agent/tests/eval/baselines/harness/row_test.go` (SWAP enum) +
> `multi-agent/tools/eval/experiments/` (NEW, 5 per-workload runners) +
> `multi-agent/tools/eval/fulltable/{run.sh, lib/plan.py}` (small
> `--workload` filter extension so per-workload runners are thin wrappers) +
> `docs/specs/wt4-codex-only.{spec,plan,handoff}.md`.
> **Not touched here** (goes in separate paper_writing PR): 24 markdown
> occurrences of `single_machine_claude_code` in
> `paper_outputs/{motivation,evaluation}_v3.md` + zero-hit sanity on
> `paper/main*.tex`. That PR is documented in §11.
> Nothing else moves.

## 0. Scope declaration — codex-only environment change, NO 60-run trigger

**This worktree bundles the 8-step p4 plan into ONE branch → ONE PR.** The
original p4 plan carved 8 worktrees (3 environment + 5 runner). The user's
2026-07-07 decision was to collapse the 8 into 1 per-repo PR because the
total diff is ~600 lines, the logic is tightly coupled around one rename,
and Phase 3 merged the fulltable harness so the swap is a simple enum
change plus one new binary plus five thin wrappers. This spec captures
that consolidated scope.

**What this worktree ships:**
- ONE new baseline binary `single_machine_codex/` (main+impl+workloads+
  run+tests), semantically parallel to `single_machine/` but invoking
  `codex exec` instead of `claude -p`.
- ENUM SWAP across `tools/eval/fulltable/` (matrix.yaml, schema, plan.py,
  build_paper_tables.py, dry_run_snapshot.txt, tests) replacing 5 rows +
  1 schema entry + 1 plan.py map key + 5 snapshot lines +
  2 test occurrences with `single_machine_codex`.
- `tests/eval/baselines/harness/row_test.go` enum swap (1 occurrence).
- SMALL EXTENSION to `tools/eval/fulltable/run.sh` +
  `tools/eval/fulltable/lib/plan.py` adding a `--workload <id>` filter so
  wrappers subset rows by workload without duplicating orchestration.
- FIVE per-workload wrapper scripts under `tools/eval/experiments/`, each
  pinning its `workload_id` and applying workload-specific preflight
  before forwarding to `fulltable/run.sh --workload <id>`.
- Handoff document listing everything this worktree consciously does NOT
  do (compose-test entrypoint, slave_tools.go tool-name rename,
  paper_outputs md swap, `mock-claude-opus-4-8` provider label,
  Windows-executor real run).

**What this worktree does NOT do:**
- Does NOT run the 60-row matrix. `--dry-run` and per-workload script
  self-test only. Real runs remain in a downstream `paper/v4/run-*-real`
  worktree per p4 §Non-goals.
- Does NOT delete `tests/eval/baselines/single_machine/` (the Claude
  variant). It stays as reference for the paper's §Related Work and as a
  fallback baseline should a reviewer request it. Only the fulltable
  matrix stops enumerating it.
- Does NOT edit `internal/driver/slave_tools.go` (`get/update_slave_claude_permissions`
  tool names are backend-agnostic already — the token "claude" in the
  tool name is stable API surface, not implementation).
- Does NOT touch `compose-test/entrypoint-driver.sh`, mock-model
  provider labels, or paper `related_work_outline.md` references. Those
  are p2/p3 conflicts categorized `record only` in p4 v1.
- Does NOT extend `run.sh` beyond `--workload <id>` filter and
  `--results-root <dir>` (see §4.3 for both). Historical draft deferred
  `--results-root`; spec-review P0#2 pointed out the wrapper security
  guarantee ("writes only under caller-supplied results-root") cannot
  be honored without `run.sh` support, so `--results-root` is now
  in-scope. `--reps` and `--config-subset` remain deferred — see
  §Handoff §Runner-flag deferral.
- `--reps` / `--config-subset` deferral is a **conscious partial
  fulfillment** of p4 §"每个 runner script 的统一形态". Documented in
  §Success criteria acceptance item 12 and the handoff doc. Reviewer
  can push back on this if the paper's methodology section requires
  the flags in this cycle.

## 1. Purpose

Ship the codex-only environment so the Phase 3 harness can execute the
60-row matrix with `codex exec` as the single-machine coding-agent
baseline (replacing `claude -p`), and give each of the 5 workloads its
own thin wrapper script with the security-critical preflight
(codex binary presence, `~/.codex/config.toml` readable, Windows-host
guard for windows-only-artifact, dual-config validation for
credential-bound-model). Enable a subsequent `paper/v4/run-*-real`
worktree to invoke `bash tools/eval/experiments/<workload>.sh --dry-run`
per workload and — once real-run infrastructure is added — trigger real
runs one workload at a time without touching the harness.

## 2. Module layout

```
multi-agent/tests/eval/baselines/single_machine_codex/       (NEW)
├── main.go                         cmd entry; mirrors single_machine/main.go
├── impl.go                         BaselineImpl; §4.1 pinned `codex exec` argv for real, dry-run projects mock_workspace
├── impl_test.go                    dry-run + LookPath + missing-workload tests
├── workloads.go                    codexPrompts map — VERBATIM copy of claudePrompts (same 5 workload ids, same prompts)
└── run.sh                          smoke matrix entrypoint, mirrors single_machine/run.sh

multi-agent/tools/eval/fulltable/                            (MODIFIED IN PLACE)
├── matrix.yaml                     5× `baseline_or_ablation: single_machine_claude_code` → `single_machine_codex`
├── matrix.schema.json              enum member swap
├── lib/plan.py                     BASELINE_DIR key rename `"single_machine_claude_code": "single_machine"` → `"single_machine_codex": "single_machine_codex"`
├── build_paper_tables.py           any residual `single_machine_claude_code` literal → `single_machine_codex`
├── tests/dry_run_snapshot.txt      5 snapshot lines updated to `single_machine_codex` + new baseline `run.sh` path
├── tests/test_matrix_schema.py     enum-member expectation swap
├── tests/test_stub_listen_loopback.py  known-baseline set swap
├── tests/test_dry_run_snapshot.py  no code change (compares regenerated snapshot)
├── run.sh                          NEW: `--workload <id>` filter (subset by workload_id before dispatch)
└── lib/plan.py                     NEW: `filter_workload(rows, workload_id)` helper + tests

multi-agent/tests/eval/baselines/harness/row_test.go         (MODIFIED)
    literal `"single_machine_claude_code"` → `"single_machine_codex"`

multi-agent/tools/eval/experiments/                          (NEW)
├── README.md                       overview + preflight table + safety notes
├── _common.sh                      shared preflight helpers (codex_bin_present, codex_config_readable, log-and-exit helpers)
├── _common.bats                    tests for _common.sh (bats-core if available; fallback to bash smoke)
├── cross_device_code_mod.sh        pin workload_id=cross-device-code-mod
├── missing_parser_converter.sh     pin workload_id=missing-parser-converter
├── remote_data_processing.sh       pin workload_id=remote-data-processing; +check /tmp writable + fixture present
├── windows_only_artifact.sh        pin workload_id=windows-only-artifact; +Windows-host guard (exit 0 with warning on non-Windows)
├── credential_bound_model.sh       pin workload_id=credential-bound-model; +validate ~/.codex/config.toml has route (a) [experimental_bearer_token]; detect but reject route (b) as unsupported here (see §4.4)
├── tests/test_common_helpers.sh    bash unit test for _common.sh helpers (no bats dep)
├── tests/test_wrapper_forwards.sh  each wrapper --dry-run forwards to fulltable/run.sh with the correct --workload
└── tests/fixtures/                 minimal ~/.codex/config.toml samples (route a only, route b only, both, neither) — for wrapper preflight tests

docs/specs/
├── wt4-codex-only.spec.md          THIS
├── wt4-codex-only.plan.md          Stage 2
└── wt4-codex-only.handoff.md       Stage 3+; what this worktree consciously skips
```

## 3. Non-negotiables (from p4 §"决策上下文" + §"Non-goals" + safety)

**CLI-B semantics**: real-mode `single_machine_codex` MUST invoke the
`codex exec` argv pinned in §4.1 (positional prompt, `--sandbox
workspace-write`, `--ephemeral`, `--skip-git-repo-check`, `--json`,
`-C ws.Root`, `--` separator). No other argv is acceptable — the older
draft's `codex exec -p <prompt>` was WRONG (`-p` is `--profile`, not
`--prompt`). Dry-run mode MUST NOT invoke `codex`. Missing `codex` on
$PATH in real mode MUST fail fast with `ErrCodexCLIUnavailable`,
symmetric to the existing `ErrClaudeCLIUnavailable` pattern.

**Environment allow-list unchanged**: `single_machine_codex` MUST reuse
the existing `harness.WhitelistEnvForAgent` contract exactly. NO new
alwaysAllowed / alwaysAllowedIfSet / perWorkload entries. `codex exec`
inherits only the same env as `claude -p` did — `PATH`, `HOME`, `LANG`,
`LC_ALL`, `TZ`, `USER`, LOOM_* namespace, plus `AGENTSERVER_ROOT`,
`MODELSERVER_ROOT`, `APP_ROOT`, `MOCK_MODEL_URL` when set, plus
`EXPECTED_MODEL_ALIAS` for credential-bound-model. If codex needs
additional inheritance (e.g. `CODEX_HOME`, `XDG_CONFIG_HOME`), it MUST
be added to `alwaysAllowedIfSetEnvKeys` in a follow-up worktree with
its own review — NOT hidden in the baseline binary.

**No agent-forwards implicit expansion**: the existing pattern for
`--forward-anthropic-api-key` MUST be mirrored as
`--forward-openai-api-key` in `single_machine_codex/main.go` (opt-in,
default off, populates `AgentForwards`). Baseline choice MUST NOT
implicitly forward any credential.

**No new secret at rest**: dry-run snapshot MUST NOT contain any live
credential material. The regenerated snapshot only contains baseline
`run.sh` paths + workload ids + deterministic UUIDs (already true;
just verify).

**Preflight failure is loud**: every per-workload wrapper MUST exit
non-zero with a clear stderr message on ANY preflight failure. Silent
"not applicable, skipping" is only allowed for `windows_only_artifact.sh`
on non-Windows hosts, and only when `--skip-if-not-windows` is passed
(default is fail with exit 2). Rationale: cron/CI runs must see a
missed workload as a failure, not as a green skip.

**Credential-bound preflight is read-only**: validating
`~/.codex/config.toml` MUST use a read-only parser (python `tomllib` in
a subprocess or bash `grep`/`awk` for the two well-known keys). The
wrapper MUST NOT edit, copy, or log the contents of the file. Only the
presence or absence of `experimental_bearer_token` (route a) and
`env_key` (route b) under the `modelserver` provider block is reported.
The actual token/env-key VALUES must never appear in wrapper stdout,
stderr, or logs — even in `--dry-run` mode. Rationale: wrappers may be
run interactively over shared screens.

**Preflight probes MUST NOT execute codex**: `codex_bin_present` MAY
call `command -v codex >/dev/null 2>&1` but MUST NOT invoke `codex`,
`codex exec`, or `codex doctor`. Reason: `codex` may spawn network
calls, mutate `~/.codex/`, or emit telemetry; a wrapper preflight is
not the place for that.

**Filesystem writes are scoped**: per-workload wrappers MUST write only
to (a) a caller-supplied `--results-root` directory (default:
`multi-agent/tests/eval/results/experiments/<workload>/`) and (b) their
per-invocation tempdir. NO writes to `$HOME`, `~/.codex/`, `/tmp/`
outside a `mktemp -d` handle, or anywhere in the tracked repo tree
outside the results root.

**Zero-hit sanity on tex is enforced by test**: the plan includes a
`tests/test_no_claude_baseline_in_tex.py` (or shell equivalent) that
greps `paper/main.tex` + `paper/main_cn.tex` for
`single_machine_claude_code` and asserts count==0. Applies only when
those files exist (they live in the paper_writing repo — the check
skips gracefully in this repo).

**Sample cap preserved**: `run.sh --sample N > 3` still requires
`ALLOW_FULL_RUN=1`. The `--workload` filter extension MUST NOT bypass
this. Verified by a new test.

## 4. Component-level requirements

### 4.1 `tests/eval/baselines/single_machine_codex/`

**main.go** (mirror of single_machine/main.go with 3 diffs):
- Usage line replaces `claude` with `codex` and
  `--forward-anthropic-api-key` with `--forward-openai-api-key`.
- Default `opts.BaselineName` returns `"single_machine_codex"`.
- Constructor call is `NewImpl(opts.WorkloadID, *forwardOpenAI)`.

**impl.go** — new sentinel errors:
- `ErrCodexCLIUnavailable = errors.New("single_machine_codex: `codex` binary not on $PATH; real mode requires OpenAI Codex CLI")`
- `ErrSingleMachineCodexWorkloadUnknown = errors.New("single_machine_codex: workload has no prompt; add one to codexPrompts")`

Struct rename: `SingleMachineCodexImpl` (field `codexBin` instead of
`claudeBin`). `Name()` returns `"single_machine_codex"`.
`AgentForwards()` returns `[]string{"OPENAI_API_KEY"}` iff
`forwardOpenAI` set, else nil. `ExecuteAgent` dry-run branch prints
`single_machine_codex: [DRY-RUN] skipping `codex exec` invocation; using mock_workspace projection`
and returns the same zero-metrics struct. Real branch invokes the
LOCKED argv below (no other form is acceptable):

**Exact argv (LOCKED — resolves spec-review P0#1)**: real branch
invokes

```
cmd := exec.CommandContext(
    ctx, bin,
    "exec",
    "--sandbox", "workspace-write",       // sandbox_write path — writes only under primary workspace + explicit --add-dir
    "--ephemeral",                        // no session persistence under ~/.codex/sessions
    "--skip-git-repo-check",              // ws.Root is a tempdir, not a git repo
    "--json",                             // stable structured stream to stderr for scrubbing
    "-C", ws.Root,                        // primary working root (writable per workspace-write sandbox)
    "--",                                 // end-of-options guard against prompt strings that look like flags
    prompt.Prompt,                        // POSITIONAL (verified via `codex exec --help`: [PROMPT])
)
```

Argv notes (verified 2026-07-07 against `codex-cli 0.142.5`):
- `[PROMPT]` is positional; `-p` is `--profile` (config profile
  selector), NOT prompt. Spec-review P0#1 caught the prior draft's
  `-p <prompt>` error.
- `-s|--sandbox <SANDBOX_MODE>` accepts `read-only`,
  `workspace-write`, `danger-full-access`. `workspace-write` scopes
  writes to `-C <DIR>` plus any `--add-dir`; we pass neither
  `--add-dir` nor rely on `~/.codex/config.toml` default policy.
- `--ephemeral` disables session-file persistence to
  `$CODEX_HOME/sessions/`. Combined with `--sandbox workspace-write`
  bounded to `ws.Root`, this satisfies §3 "Filesystem writes are
  scoped" for the codex subprocess.
- `--` separator prevents an attacker-controlled prompt from being
  interpreted as a flag (defense-in-depth; workload prompts are static
  compiled-in strings today but the guard is cheap).
- `--json` emits JSONL events (not free-form). Every event line goes
  through `internal/secretscrub` before being written to any file or
  stderr the harness records (resolves P1#6 — runtime codex stderr
  scrubbing).
- We deliberately do NOT pass `--dangerously-bypass-approvals-and-sandbox`,
  do NOT pass `-c sandbox_mode=...` (would override `--sandbox`
  precedence unpredictably), and do NOT enable any tool that would
  reach the network without an operator-set config profile.

**Rationale for `workspace-write`**: the 5 workload prompts explicitly
ask the agent to write files (`Write two files:...`, `Write result.json`).
`read-only` would guarantee 0/5 outputs and defeat the baseline's whole
point (measure "what does a single-shot coding agent do?"). We accept
that workspace-write allows codex to write anywhere under `ws.Root`
(the workspace tempdir), matching how `claude -p` behaves today.

**Argv change requires the exact string `--sandbox workspace-write`
in impl.go and a `TestExecuteAgent_UsesPinnedArgv` (fake `codex` binary
that records argv to a file, impl asserts the file contents byte-for-byte).**

**impl_test.go**:
- `TestName_ReturnsSingleMachineCodex` — struct.Name() equality.
- `TestAgentForwards_OptInGated` — nil when flag off, `[OPENAI_API_KEY]`
  when on.
- `TestPrepare_NoOp` — Prepare returns nil for both dry-run and real.
- `TestExecuteAgent_DryRun_SkipsBinary` — dry-run branch does not
  invoke `LookPath("codex")`; use a fake `codexBin` set to a
  guaranteed-nonexistent path to prove LookPath is not called.
- `TestExecuteAgent_RealMode_MissingBinary_ReturnsSentinel` — real
  mode with PATH cleared returns `ErrCodexCLIUnavailable`.
- `TestExecuteAgent_UnknownWorkload_ReturnsSentinel` — pass unknown
  workload id, expect `ErrSingleMachineCodexWorkloadUnknown`.
- `TestCodexPromptsMatchClaudePrompts` — read
  `../single_machine/workloads.go` at test time via `go/parser` +
  `go/ast` (AST extraction ONLY — `go:embed` of a golden JSON is
  explicitly REJECTED because a stale golden could pass while claude's
  workloads.go drifts). Assert `codexPrompts` and `claudePrompts` have
  identical keys + identical `Prompt` strings + identical
  `ExpectedOutputs`. This is the anti-drift lock — the two baselines
  MUST use the same prompts so any measured behavior difference is
  attributable to the CLI, not the prompt. Failure includes a per-key
  diff so an implementer can immediately see what drifted.

**workloads.go**: VERBATIM copy of `single_machine/workloads.go`
`claudePrompts` renamed to `codexPrompts`. The anti-drift test above
locks this to the source.

**run.sh**: mirror of `single_machine/run.sh` with two substitutions:
- fallback binary `/tmp/single_machine` → `/tmp/single_machine_codex`
- `go run "./tests/eval/baselines/single_machine"` →
  `go run "./tests/eval/baselines/single_machine_codex"`

### 4.2 `tools/eval/fulltable/` enum swap

Exactly the 11 hits found by
`grep -rn single_machine_claude_code multi-agent/tools/eval/fulltable`:

- `matrix.schema.json:36` enum member: `"single_machine_claude_code"` →
  `"single_machine_codex"`.
- `matrix.yaml:108,110,112,114,116` → `single_machine_codex`.
- `lib/plan.py:57` BASELINE_DIR map:
  ```python
  "single_machine_claude_code": "single_machine",
  ```
  →
  ```python
  "single_machine_codex": "single_machine_codex",
  ```
  Note both key AND value change (new key + new subdirectory name).
- `tests/test_matrix_schema.py:41` expected enum members set.
- `tests/test_stub_listen_loopback.py:36` known-baseline set.
- `tests/dry_run_snapshot.txt:51-55` regenerated by rerunning the
  planner (5 lines with `single_machine/run.sh` → `single_machine_codex/run.sh`).

Plus one hit outside fulltable:
- `tests/eval/baselines/harness/row_test.go:12` expected label.

The `build_paper_tables.py` file has no `single_machine_claude_code`
literal today but a defensive grep at test time verifies zero
residual hits repo-wide.

### 4.3 `run.sh` + `plan.py` `--workload <id>` and `--results-root <dir>`

**Motivation**: p4 §"per-workload runner 的统一形态" specifies that each
wrapper "把 workload 名 pin 死 + 传剩下的 flag 给 fulltable run.sh". The
existing `run.sh` has no `--workload` filter — it always enumerates all
60 matrix rows and always writes under `multi-agent/tests/eval/results/smoke/`.
Two extensions are required in this PR:

**`--workload <id>` filter** (resolves p4 wrapper requirement):
- New flag `--workload <id>` — SINGLE value; wrappers pin exactly one.
- run.sh MUST reject a repeated / second `--workload` with exit 2
  ("run.sh: --workload may be given at most once"). This closes
  spec-review P0#3 (caller cannot override the wrapper-pinned id by
  appending a second `--workload` after it).
- Unknown workload id → `run.sh: unknown workload id: <id>; expected one of {5 ids}` exit 2 BEFORE any dispatch (validation against the 5-id allowlist derived from plan.py's WORKLOADS constant, sourced via a `plan.py --list-workloads` mode to avoid duplication).
- Passed through to plan.py CLI as `--filter-workload <id>`.
- `usage()` block gets the new line.
- Interaction with existing flags (spec-review P1#1):
  - `--workload X --resume` — resume matrix scope is filtered to
    workload X first, then the completed-sidecar skip applies. E4
    scope is NOT filtered (E4 is by family, not workload); when
    `--workload` is set, E4 rows are SKIPPED entirely for this
    invocation. Documented in run.sh usage and a test.
  - `--workload X --sample N` — sample-first-then-filter would drop
    all sampled rows if none match X. Order MUST be filter-first-then-sample.
    Verified by test.
  - `--workload X --sample N > 3` — still requires `ALLOW_FULL_RUN=1`.
    Explicit test asserts exit 2 without the env var (resolves
    spec-review P0#4 — test at the run.sh level, not planner).
  - `--workload X --parallel N` — no interaction; parallel budget
    applies to the filtered row set.

**`--results-root <dir>` scope override** (resolves spec-review P0#2):
- New flag `--results-root <dir>`. Default: unchanged (existing
  `tests/eval/results/smoke/`).
- When set, wrapper-derived value overrides the hard-coded smoke root.
- The overridden root MUST be an absolute path; relative paths exit 2.
- The path MUST NOT resolve inside `$HOME/.codex/` (grep of realpath),
  MUST NOT be `/`, `/tmp`, `/root`, `$HOME`, or any git-repo top-level
  (checked via `git rev-parse --show-toplevel` when applicable).
- The path MUST NOT already contain non-fixture data (fail with exit 2
  and message listing conflicting files) UNLESS `--resume` is set.

**plan.py diffs**:
- New function `filter_workload(rows: list[PlannedRow], workload_id: str) -> list[PlannedRow]`.
- CLI arg `--filter-workload` (default None; None = no filter, existing
  behavior).
- CLI arg `--list-workloads` (prints WORKLOADS newline-separated; used
  by run.sh for allowlist derivation to avoid literal duplication).
- CLI arg `--results-root` (default None = preserve existing behavior;
  when set, planned CSV paths are rooted here).
- `filter_workload` raises `UnknownWorkloadError` if the id is not in
  the WORKLOADS constant.
- Tests: filter yields exactly N rows where N = count of
  matrix rows with that workload_id (currently 12 per workload —
  full_loom + 8 ablations + 3 baselines) AFTER the enum swap.

**FULLTABLE_RUN_SHIM env var** (test seam; addresses P2#2 rename):
- Renamed to `LOOM_FULLTABLE_WRAPPER_SHIM=1` (namespaced under LOOM_,
  consistent with `LOOM_FULLTABLE_DISPATCH_SHIM`).
- When set AND `--dry-run` is set (both required), run.sh prints
  `SHIM_WORKLOAD_FILTER: <id>` and `SHIM_RESULTS_ROOT: <path>` and
  exits 0. Without `--dry-run` OR without the env var, dispatch
  proceeds normally.
- Rationale: dual-guard prevents an operator env accident from silently
  skipping preflight during a real run.

### 4.4 `tools/eval/experiments/` — 5 wrappers + shared

**_common.sh** exports functions:
- `codex_bin_present` → returns 0/2 (2 on missing). NO codex invocation.
- `codex_config_readable` → tests `-r "$HOME/.codex/config.toml"`.
- `codex_config_has_route_a` → uses python3 `-c 'import tomllib; ...'`
  or fallback awk to check `[model_providers.modelserver]` block has
  `experimental_bearer_token`. Prints ONLY `route_a: present|absent`.
- `codex_config_has_route_b` → same for `env_key`.
- `require_writable_tmp` → `mktemp -d --dry-run` (or `mktemp -d` +
  immediate `rmdir`).
- `require_fixture` <path> — checks fixture exists.
- `require_windows_host` — `case "$(uname -s)" in *NT*|MSYS*|CYGWIN*) return 0;; *) return 1;; esac`.
- `die` <msg> → echo to stderr, exit 2.
- `warn_and_exit_zero` <msg> → echo to stderr, exit 0 (ONLY used by
  windows wrapper with explicit `--skip-if-not-windows` flag).

**cross_device_code_mod.sh** — no workload-specific preflight beyond
codex bin + config readable.

**missing_parser_converter.sh** — same.

**remote_data_processing.sh** — additionally requires:
- `require_writable_tmp`
- `require_fixture` on
  `multi-agent/tests/eval/workloads/remote-data-processing/fixtures/`
  (list of files TBD in plan; probably just directory existence).

**windows_only_artifact.sh** — additionally requires either:
- `require_windows_host` returns 0, OR
- `--skip-if-not-windows` flag passed → `warn_and_exit_zero`.
Default (no flag, non-Windows) is `die "windows-only-artifact requires a Windows host; pass --skip-if-not-windows to acknowledge"`.

**credential_bound_model.sh** — additionally requires:
- `codex_config_readable`
- `codex_config_has_route_a` reports **present**. **This PR only
  supports route (a).** Route (b) is declared unsupported and MUST
  cause an explicit fail (see below).

**Route (b) declared unsupported in this PR** (resolves spec-review
P0 round-3 — the earlier round-2 attempt to auto-forward via a
`LOOM_`-prefixed env-key was rejected because the existing harness
LOOM_ passthrough forwards to BOTH agent AND oracle
(`env.go:filterAndInject`), which would break T1 "no implicit
credential forwarding to oracle"; using a bare name and asking the
operator to `--forward-openai-api-key` conflates two credentials into
one flag). The correct route-b design needs a dedicated agent-only
credential forwarding path in `harness/env.go` and is out-of-scope for
a rename PR. Concretely:

- Preflight reports `route_a: <present|absent>` AND `route_b: <present|absent|not-supported-in-this-pr>`.
  Route-b **presence** in `~/.codex/config.toml` is DETECTED (so the
  operator learns their config has both) but is NOT ACCEPTED as
  satisfying the preflight. The `env_key` NAME is never printed and
  its VALUE is never read.
- If `route_a` absent AND `route_b` present: exit 2 with
  `route_b is present in ~/.codex/config.toml [model_providers.modelserver] but is not supported by this baseline yet; route_a (experimental_bearer_token) is required. See docs/specs/wt4-codex-only.handoff.md §Route-b support.`
- If both absent: exit 2 with
  `credential-bound-model requires route (a) [experimental_bearer_token under model_providers.modelserver] in ~/.codex/config.toml. Route (b) support is deferred; see handoff.`
- If both present: proceed (route-a is what will be exercised); print
  informational note that route-b is detected but route-a will be
  used.
- Test fixture set from §4.5 remains 10 files — the ones covering
  `route-b-only`, `route-b + env-unset`, `route-b different table`,
  `route-b commented`, etc. all now assert the same route_b `absent`
  or `not-supported-in-this-pr` outcome and non-zero exit when
  route_a is absent.

**Follow-up scope** (in handoff): a dedicated worktree adds an
agent-only forward path — either a new
`alwaysAllowedIfSetEnvKeysAgentOnly` map in `harness/env.go`, or a new
`--forward-modelserver-env-key <NAME>` opt-in flag on
`single_machine_codex/main.go` that only propagates the named var to
the agent (never the oracle). Whichever design lands there, this PR
does not pre-commit to it.

**All 5 wrappers**:
- Accept `--dry-run`, `--sample N`, `--results-root <dir>` (default
  `<git-root>/multi-agent/tests/eval/results/experiments/<workload>/<isodate>-<pid>/`
  — per-invocation subdir avoids concurrent-wrapper race, resolving
  T6 / spec-review adjacent), `-h|--help`, and forward to
  `fulltable/run.sh --workload <id> --results-root <dir>`.
- **Reject caller-supplied `--workload` / `--filter-workload`**
  (resolves spec-review P0#3): if either appears in wrapper argv, exit 2
  with `wrapper:<id>: --workload / --filter-workload is pinned; remove from argv`.
  This is a wrapper-side belt; run.sh's "at most once" enforcement is
  the suspenders.
- Print `[wrapper:<workload>] preflight OK` to stderr before dispatch.
- Print `[wrapper:<workload>] results-root: <resolved-path>` to stderr.
- On `--help`: print own usage AND `run.sh --help` header via
  `run.sh --help 2>&1 | head -20`.

### 4.5 Tests for wrappers

- `tests/test_common_helpers.sh` — plain bash test (no bats
  dependency). Sources `_common.sh` and asserts each helper's exit
  code + stderr under mocked env (fake `HOME` with fixture toml files,
  `PATH` with and without a fake `codex` binary via `mktemp -d`).
- `tests/test_wrapper_forwards.sh` — per wrapper:
  1. Run with `--dry-run` and `LOOM_FULLTABLE_WRAPPER_SHIM=1` (see §4.3
     — makes `run.sh` print `SHIM_WORKLOAD_FILTER: <id>` +
     `SHIM_RESULTS_ROOT: <path>` and exit 0 without dispatching).
  2. Assert wrapper stdout contains `SHIM_WORKLOAD_FILTER: <expected id>`.
  3. Assert exit 0.
  4. **Also**: run wrapper with an EXTRA `--workload other-id` in argv;
     assert exit 2 + stderr contains `--workload / --filter-workload is pinned`.
     Same test for `--filter-workload`.
- `tests/test_credential_bound_preflight.sh` — **10 fixture toml files**
  (resolves spec-review P1#4 — expanded fixture set; round-2 typo fix;
  round-4 route-b-unsupported update):

  Each fixture is exercised against the wrapper's preflight. The
  expected outcome depends ONLY on route-a presence, since route-b is
  declared unsupported in this PR (§4.4). The fixtures still cover
  route-b permutations to prove the parser detects them correctly
  (state field goes to stderr) and to lock the detection logic in
  place for the follow-up worktree that adds route-b support.

  | # | fixture | route_a | route_b (detected) | expected wrapper outcome |
  |---|---|---|---|---|
  | 1 | route-a-only | present | absent | preflight OK, exit 0 |
  | 2 | route-b-only, env-set | absent | present | exit 2 "route_b present but unsupported; route_a required" |
  | 3 | route-b-only, env-unset | absent | present | same as #2 (env-var state does not matter — route-b is not accepted either way) |
  | 4 | both | present | present | preflight OK with informational "route_b detected, will use route_a", exit 0 |
  | 5 | neither | absent | absent | exit 2 "both absent" |
  | 6 | route-a commented (`# experimental_bearer_token = "..."`) | absent | absent | exit 2; parser must NOT count commented keys |
  | 7 | `experimental_bearer_token` under `[model_providers.other]` | absent | absent | exit 2; parser only reads `modelserver` table |
  | 8 | malformed TOML | n/a | n/a | exit 2 with `malformed TOML` message; NO token-shaped substring in stderr |
  | 9 | duplicate `[model_providers.modelserver]` tables | n/a | n/a | exit 2 with clear parser error (TOML forbids duplicates) |
  | 10 | token-shaped value `experimental_bearer_token = "sk-abc..."` | present | absent | preflight OK, exit 0, AND assert `sk-abc` never appears in stderr/stdout |

  Fixtures live under `tests/fixtures/codex_config/`. The wrapper's
  `route_a: <state>, route_b: <state>` stderr line uses the state
  vocabulary `{present, absent}` — route-b's `env-set` /
  `env-unset` distinction is NOT surfaced in this PR since route-b
  is unsupported (surfacing it would misleadingly imply usability).
- `tests/test_snapshot_no_secrets.py` (Python; in
  `tools/eval/fulltable/tests/`) — resolves spec-review P1#5. Reads
  `tests/dry_run_snapshot.txt` after the enum swap and asserts none of
  these patterns match: `sk-[A-Za-z0-9]{6,}`, `ghp_[A-Za-z0-9]+`,
  `Bearer [A-Za-z0-9._-]+`, `refresh_token`, `/root/`, `$USER`,
  `/home/[a-z]+/`. Fails with a message that identifies the offending
  line if any hit lands.
- `tests/test_run_sh_duplicate_workload.sh` — resolves spec-review P2#1
  (round 2, upgraded to acceptance test). Invokes
  `bash tools/eval/fulltable/run.sh --workload cross-device-code-mod --workload credential-bound-model --sample 3`
  directly (not via wrapper) with `ALLOW_FULL_RUN` unset. Expect
  exit 2 with stderr containing `--workload may be given at most once`
  BEFORE any dispatch. Also tests `--workload=A --workload=B` variant.
- `tests/test_sample_cap_with_workload.sh` — resolves spec-review P0#4.
  Invokes
  `bash tools/eval/fulltable/run.sh --workload cross-device-code-mod --sample 4`
  AND the `--sample=4` variant AND same with a stray extra
  `--workload other-id`. All three MUST exit 2 with ALLOW_FULL_RUN
  unset; the sample-cap message MUST fire BEFORE the extra-workload
  message (order-independent hit — both messages may be present, but
  exit must be 2 and no dispatch may occur).
- `tests/test_workload_filter_semantics.sh` — resolves spec-review P1#1.
  Under `LOOM_FULLTABLE_WRAPPER_SHIM=1 --dry-run`:
  - `--workload X` alone → SHIM prints filter=X, expected-row-count=12.
  - `--workload X --sample 2` → filter=X, expected-row-count=2 (filter-first).
  - `--workload X --resume` → filter=X, E4 skipped, matrix rows subset.

The `LOOM_FULLTABLE_WRAPPER_SHIM` env var is new and namespaced. Adding
it MUST be documented in run.sh usage() block. It is CI-safe (no
dispatch, no network) and parallel to the existing
`LOOM_FULLTABLE_DISPATCH_SHIM=1`. Rationale for a second shim:
`LOOM_FULLTABLE_DISPATCH_SHIM=1` still runs the commit_meta preflight
and requires a clean tree; `LOOM_FULLTABLE_WRAPPER_SHIM=1` short-circuits
BEFORE preflight so wrapper tests can run in a dirty worktree without
preflight false-positives. **Both `--dry-run` AND the env var are
required to activate** (dual guard, resolves spec-review P2#2).

### 4.6 README

- `tools/eval/experiments/README.md` — overview, per-wrapper preflight
  table, security notes (no token logging, read-only config parse),
  known-not-supported flags list (`--reps`, `--config-subset`
  documented as reserved for the follow-up worktree).

## 5. Anti-drift and safety tests (extra to §4.5)

- `TestCodexPromptsMatchClaudePrompts` (Go; in `single_machine_codex/`)
  — **AST-parse** `../single_machine/workloads.go` at test time using
  `go/parser` + `go/ast` and diff the `claudePrompts` map declaration
  against `codexPrompts` in the current package. Compare KEYS +
  `Prompt` strings + `ExpectedOutputs` slices. Failure message:
  "codex prompts drifted from claude prompts; the two baselines must
  share prompts for the CLI-difference measurement to be valid — see
  line diff." (Resolves spec-review P1#2 — a `go:embed` golden could
  go stale independently; source AST comparison cannot.)
- `TestExecuteAgent_UsesPinnedArgv` (Go; in `single_machine_codex/`)
  — resolves spec-review P0#1 test hook. Sets `codexBin` to a shell
  script that writes its argv to a temp file, calls ExecuteAgent, then
  reads and byte-compares argv against the pinned string from §4.1.
- `TestExecuteAgent_ScrubsStderr` (Go; in `single_machine_codex/`) —
  resolves spec-review P1#6 (round 2). Fake `codex` emits
  `sk-abc123DEFabcDEFabcDEF` and `Bearer bar_baz_qux_secret_val` to
  stderr AND exits with code 42 (nonzero — this is the leak path a
  passing test never exercises). Assert the returned error's `Error()`
  string and any harness-persisted representation of that error
  contains the scrubbed sentinel from `internal/secretscrub` (e.g.
  `[REDACTED]`) and does NOT contain `sk-abc123`, `abcDEF`, or
  `bar_baz_qux_secret_val` as substrings. Test uses `testing.T.Cleanup`
  to restore any tempdir state. A SECOND variant tests the success
  path (exit 0, token-shaped stderr) to prove scrubbing runs on both
  branches, not only errors.
- `TestNoClaudeBaselineInMatrix` (Python; in
  `tools/eval/fulltable/tests/`) — grep matrix.yaml + schema + plan.py
  for `single_machine_claude_code`; assert count == 0. This locks the
  swap.
- `TestNoClaudeBaselineInHarnessRowTest` (Python or Go) — grep
  `tests/eval/baselines/harness/row_test.go` **and**
  `tests/eval/baselines/matrix_test.go` **and**
  `tests/eval/baselines/README.md` for `single_machine_claude_code`;
  assert count == 0 in the first two, and equals-1-inside-a-strike-through
  or is not present in README (§10 updates README to list the codex
  variant). (Broader-than-original — surfaced by pre-review grep sweep.)
- `TestSnapshotBaselinePathExact` (Python) — resolves spec-review P2#3.
  Not "does path contain single_machine" (`single_machine_codex`
  matches that substring), but exact literal
  `tests/eval/baselines/single_machine_codex/run.sh` on the 5 snapshot
  lines. Regression-proof.
- `TestSnapshotHasNoSecrets` (Python) — see §4.5, listed here too so
  the acceptance checklist is one-stop.

## 6. Rollback

The swap is enum-only in matrix + plan.py + tests. Rollback = revert
the branch. Both `single_machine/` and `single_machine_codex/` exist
side-by-side after this PR, so a follow-up PR that flips matrix.yaml
back to `single_machine_claude_code` restores the Claude baseline in
the matrix without deleting the codex baseline.

**Complete rollback file list (resolves spec-review P1#7)**, in order:

1. `multi-agent/tools/eval/fulltable/matrix.yaml` — 5 rows
   `single_machine_codex` → `single_machine_claude_code`.
2. `multi-agent/tools/eval/fulltable/matrix.schema.json` — enum member.
3. `multi-agent/tools/eval/fulltable/lib/plan.py` — BASELINE_DIR key
   and value.
4. `multi-agent/tools/eval/fulltable/tests/dry_run_snapshot.txt` — 5
   snapshot lines re-generated by rerunning planner.
5. `multi-agent/tools/eval/fulltable/tests/test_matrix_schema.py`.
6. `multi-agent/tools/eval/fulltable/tests/test_stub_listen_loopback.py`.
7. `multi-agent/tools/eval/fulltable/tests/test_no_claude_baseline_*.py`
   — delete (or invert to allow `single_machine_claude_code`).
8. `multi-agent/tools/eval/fulltable/tests/test_snapshot_baseline_path_exact.py`
   — literal string flip.
9. `multi-agent/tests/eval/baselines/harness/row_test.go` — literal.
10. `multi-agent/tests/eval/baselines/matrix_test.go` — the
    `{"single_machine", "single_machine_claude_code"}` row (may need
    to be added back if this PR deleted it — check plan.md §Task 7).
11. `multi-agent/tests/eval/baselines/README.md` — baseline table entry.
12. **Leave `tests/eval/baselines/single_machine_codex/` intact** — it
    causes no harm; deletion is optional cleanup.
13. `docs/specs/wt4-codex-only.{spec,plan,handoff}.md` — mark
    superseded in a rollback commit, do not delete.

## 7. Success criteria / acceptance

1. `go test ./tests/eval/baselines/single_machine_codex/... -race` all
   green.
2. `go test ./tests/eval/baselines/... -race` (whole baselines tree)
   remains green.
3. `pytest tools/eval/fulltable/tests/` — 126 previously-passing tests
   still pass + all new tests pass. Anti-drift tests (§5) pass.
4. `bash tools/eval/fulltable/run.sh --dry-run` — 60 dispatched-would-be
   rows, no `single_machine_claude_code` in output.
5. `bash tools/eval/fulltable/run.sh --workload cross-device-code-mod
   --dry-run` — exactly 12 dispatched-would-be rows.
6. Each of 5 wrappers: `bash tools/eval/experiments/<workload>.sh
   --help` → prints usage + `run.sh` header; exit 0.
7. Each of 5 wrappers: `bash tools/eval/experiments/<workload>.sh
   --dry-run` (with codex + config preflight satisfied on dev host)
   → prints `preflight OK` + dispatches with correct
   `--workload <id>` filter.
8. `bash tools/eval/experiments/windows_only_artifact.sh --dry-run`
   on Linux → exit 2 with "requires Windows host" message.
9. `bash tools/eval/experiments/windows_only_artifact.sh
   --skip-if-not-windows --dry-run` on Linux → exit 0 with warning.
10. `bash tools/eval/experiments/credential_bound_model.sh --dry-run`
    without `~/.codex/config.toml` → exit 2 with route-a/b message.
    None of the token values appear in stderr.
11. Repo-wide `grep -rn single_machine_claude_code multi-agent/` (this
    PR's repo scope) returns 0 matches EXCEPT under
    `tests/eval/baselines/single_machine/` (untouched) and the docs/
    tree (spec/plan/handoff).
12. `docs/specs/wt4-codex-only.handoff.md` enumerates each item
    consciously skipped (paper_writing 24-md swap, slave_tools.go,
    compose-test entrypoint, mock-model provider label,
    Windows-executor real run, --reps / --config-subset in run.sh —
    with an explicit note that Phase 4 p4 §"每个 runner script 的统一
    形态" listed `--reps` and `--config-subset`; this PR ships the
    other 3 flags plus `--results-root` and defers those two with a
    short deferral rationale). Resolves spec-review P1#7's
    under-scope concern by making the deferral explicit rather than
    silent.
13. Reviewer (codex) has no unresolved P0 or P1 findings; P2/P3
    recorded in `docs/specs/wt4-codex-only.handoff.md` §Known-issues
    with a short reason for deferral.
14. Runtime `codex exec` stderr passes through `internal/secretscrub`
    before any file/CSV write (resolves spec-review P1#6). Test
    `TestExecuteAgent_ScrubsStderr` proves it with a fake `codex`
    that prints token-shaped bytes.
15. `LOOM_FULLTABLE_WRAPPER_SHIM=1` (renamed from `FULLTABLE_RUN_SHIM`)
    requires `--dry-run` to activate (resolves spec-review P2#2).
16. Anti-drift test uses `go/parser` AST extraction, not `go:embed`
    golden (resolves spec-review P1#2).
17. Credential-bound route (b) is DETECTED (state reported to stderr
    for operator visibility) but NOT SUPPORTED in this PR (resolves
    spec-review P1#3 round-3 P0 — using `LOOM_` prefix would leak the
    secret to the oracle subprocess via the existing LOOM_ passthrough).
    Route-b-only configs exit 2 requiring route-a. env var NAME and
    VALUE never reach stdout / stderr. Follow-up worktree adds
    agent-only forwarding path — see handoff.

## 8. Sequencing / dependencies

Single branch, in this order:

1. Task 0 — scaffolding: create `single_machine_codex/` dir + empty
   files; test infra passes green (nothing yet).
2. Task 1 — copy prompts + anti-drift test (RED then GREEN).
3. Task 2 — impl.go + impl_test.go (dry-run branch first, then real
   branch behind LookPath).
4. Task 3 — main.go + run.sh; go build passes.
5. Task 4 — matrix.yaml + schema enum swap; test_matrix_schema
   still green.
6. Task 5 — plan.py BASELINE_DIR swap; existing planner tests still
   green.
7. Task 6 — regenerate dry_run_snapshot.txt (write once, then lock).
8. Task 7 — harness/row_test.go enum swap.
9. Task 8 — plan.py filter_workload + run.sh --workload flag +
   tests.
10. Task 9 — _common.sh + its tests.
11. Task 10 — 5 wrappers + wrapper tests + credential fixtures + tests.
12. Task 11 — experiments/README.md + repo-wide grep sanity test.
13. Task 12 — handoff doc.

Every task ends with `git commit`. Between tasks, `go test ./...` and
`pytest tools/eval/fulltable/tests/` must be green. Codex review
happens after all tasks land on the branch (i.e. code review is on the
full diff, not per-task).

## 9. Threat model / security review checklist

For codex to specifically audit at the review stage:

- **T1 — env leak via subprocess**: does `single_machine_codex` inherit
  any secret from parent env that `single_machine` (claude) did not?
  Expected answer: no, both use `harness.WhitelistEnvForAgent` with
  identical workload id; ONLY `OPENAI_API_KEY` differs from
  `ANTHROPIC_API_KEY` and both require explicit `--forward-*` opt-in.
- **T2 — codex config exfiltration**: does any wrapper log the contents
  of `~/.codex/config.toml`? Expected: no; only route-presence booleans
  reach stderr.
- **T3 — write outside sandbox**: real-mode `codex exec` — what's the
  smallest sandbox that still yields workload output-parity? The impl
  test enumerates. Default policy declared in §4.1 rationale.
- **T4 — snapshot leak**: does `dry_run_snapshot.txt` post-swap contain
  any credential or hostname or username? Grep the regenerated file
  for `sk-`, `ghp_`, `Bearer`, `$USER`, `$HOME`, `/root`, and any
  route-key regex — assert zero.
- **T5 — wrapper flag injection**: what if a caller passes
  `--workload "; rm -rf ~"`? Bash-word-splitting hazard. The wrapper
  MUST pass workload id as a single positional arg to run.sh, and
  run.sh MUST validate against the 5-id allowlist before shelling out.
- **T6 — race on results-root**: two wrappers running concurrently
  writing to the same default results-root. Each wrapper MUST include
  a per-invocation subdir (e.g. `<workload>/<timestamp>-<pid>/`) OR
  reject if the target subdir exists non-empty.
- **T7 — bypassing sample cap**: covered by §5's
  `TestSampleCapEnforcedWithWorkloadFilter`.

## 10. Handoff (Stage 3 artifact, listed here for spec completeness)

The `wt4-codex-only.handoff.md` file (Stage 3) will enumerate:
- paper_writing PR pending: 24 md swap.
- `internal/driver/slave_tools.go`: tool names unchanged (backend-agnostic).
- `compose-test/entrypoint-driver.sh`: default `claude` unchanged (dev-only).
- `mock-model/mock-claude-opus-4-8`: provider name unchanged (referential).
- `paper_outputs/related_work_outline.md`: Claude Code / Codex product
  references unchanged (they describe products, not baselines).
- `run.sh --reps` / `--config-subset`: reserved; not implemented here.
- Windows-executor real run: reserved for Phase 5 prod-multidevice.
- **Route-b support** (deferred to follow-up worktree): the credential
  path (b) — `env_key = "SOME_NAME"` in
  `~/.codex/config.toml [model_providers.modelserver]` — is DETECTED
  in preflight but NOT ACCEPTED as satisfying the credential-bound
  workload in this PR. Reason: the existing harness LOOM_ passthrough
  forwards to BOTH agent AND oracle (see
  `tests/eval/baselines/harness/env.go:filterAndInject isLoomNS` +
  `WhitelistEnvForOracle` sharing the same base wanted set), which
  would break T1 "no implicit credential forwarding to oracle".
  Follow-up worktree adds an agent-only forwarding path — either a
  new `alwaysAllowedIfSetEnvKeysAgentOnly` slice in `harness/env.go`
  or a `--forward-modelserver-env-key <NAME>` opt-in flag on
  `single_machine_codex/main.go`. §6.5's paper claim about "dual codex
  config path (a) local proxy AND path (b) workspace-scoped credential
  alias" (from `paper_outputs/evaluation_v3.md` §6.1.3) requires route
  (b) support before that paper claim can be measured on codex-only.
  Explicit follow-up commitment: this handoff is the reason the paper
  claim is NOT verified in this PR's dry-run outputs.
- P2/P3 spec-review findings recorded as known-issues:
  - **P2#1** — Windows guard: `_common.sh` will initially handle
    `MINGW*|MSYS*|CYGWIN*|*NT*`; a broader table (WSL detection,
    missing `uname`) is a follow-up.
  - **P2#3** — snapshot baseline path exactness: resolved by
    `TestSnapshotBaselinePathExact` in this PR (upgraded from P2 to
    an acceptance test).
  - No P3 findings recorded from this review round.

## 11. Paper draft (paper_writing repo) — separate small PR

Not touched in this worktree because it lives in another repo. The
follow-up PR does:

- `paper_outputs/motivation_v3.md`: 9 s/single_machine_claude_code/single_machine_codex/g
- `paper_outputs/evaluation_v3.md`: 15 same
- `paper/main.tex` + `paper/main_cn.tex`: 0 hits expected today; add a
  CI check `! grep -l single_machine_claude_code paper/main*.tex` to
  fail future edits that would re-introduce the string.

Cross-repo linkage: the handoff doc records the exact commit of THIS
worktree that closes those docs' expectations, so the paper-writing
reviewer can pin their sed-swap PR to it.

---

**Ready for codex spec review.**
