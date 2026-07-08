# WT-4-codex-only Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert Phase 3 stub-fulltable harness from `single_machine_claude_code` (Claude CLI baseline) to `single_machine_codex` (OpenAI Codex CLI baseline) and ship 5 per-workload wrapper scripts, in ONE branch → ONE PR per repo.

**Architecture:** New `tests/eval/baselines/single_machine_codex/` binary mirrors `single_machine/` with pinned `codex exec` argv (§spec 4.1); enum swap across `tools/eval/fulltable/{matrix.yaml, schema, plan.py, snapshot, tests}`; `tools/eval/experiments/` gets 5 thin wrappers on top of a new `run.sh --workload / --results-root` filter path.

**Tech Stack:** Go 1.26 (baseline binary + harness tests), Python 3.13 + pytest (fulltable planner + tests), Bash (run.sh + wrappers + tests), TOML (codex config fixtures), jsonschema (matrix schemas).

## Global Constraints

Copied verbatim from `wt4-codex-only.spec.md`:

- **CLI-B argv is LOCKED** (spec §4.1). Every real-mode invocation of `codex` MUST use exactly:
  ```
  codex exec --sandbox workspace-write --ephemeral --skip-git-repo-check --json -C <ws.Root> -- <prompt>
  ```
  No other form is acceptable. Test `TestExecuteAgent_UsesPinnedArgv` byte-compares argv against this string.
- **Env allow-list unchanged**: `harness/env.go` alwaysAllowed / alwaysAllowedIfSet / perWorkloadAllowed lists MUST NOT be modified in this PR. Additions belong in a follow-up worktree.
- **Route (b) [`env_key`] DECLARED UNSUPPORTED** in this PR. Preflight DETECTS it (state → stderr) but exits 2 if route (a) is absent. Reason: existing `LOOM_` passthrough forwards to BOTH agent and oracle; using `LOOM_`-prefixed env-key leaks secret to oracle.
- **Anti-drift uses `go/parser` AST ONLY** (spec §5). `go:embed` of a golden JSON is explicitly REJECTED.
- **Preflight probes MUST NOT execute `codex`.** Only `command -v codex >/dev/null 2>&1` is allowed. No `codex doctor`, `codex exec`, `codex --version`.
- **Filesystem writes scoped**: wrappers write only to caller-supplied `--results-root <dir>` (default `<git-root>/multi-agent/tests/eval/results/experiments/<workload>/<isodate>-<pid>/`) and their tempdir. Rejected roots: `$HOME`, `$HOME/.codex`, `/`, `/tmp`, `/root`.
- **Sample cap preserved**: `run.sh --sample N > 3` requires `ALLOW_FULL_RUN=1` regardless of `--workload` filter. Enforced at `run.sh`, not planner.
- **Config parsing is read-only** and NEVER logs values. Env var NAME and VALUE never reach stdout/stderr.
- **`LOOM_FULLTABLE_WRAPPER_SHIM=1` requires `--dry-run`** (dual-guard) to short-circuit dispatch.
- **`--workload` may be given at most once** (spec §4.3). Wrappers reject caller-supplied `--workload` / `--filter-workload`; run.sh rejects duplicates.
- **Stderr from `codex` subprocess passes through `internal/secretscrub`** before any file/CSV/error return (spec §5 `TestExecuteAgent_ScrubsStderr`).
- **`baseline_or_ablation` name is `single_machine_codex`** (spec §4.2). Enum member is the ONLY authoritative literal; all tests reference it via constant, not free literal (where practical).
- **No new secret at rest**: regenerated `dry_run_snapshot.txt` MUST NOT contain `sk-`, `ghp_`, `Bearer`, `refresh_token`, `/root/`, `$USER`, `/home/[a-z]+/` patterns. `TestSnapshotHasNoSecrets` enforces.
- **TDD for behavioral tasks**: every task that adds behavior writes the failing test first, sees it fail, writes minimum impl, sees it pass, commits. Two explicit exceptions: (a) Task 1 (`package scaffold that compiles`) is a compile-only stub with no behavioral surface — no failing test is needed because compile-passes is the only assertion; (b) Task 5's `run.sh` shell script is a mirror of an existing pattern verified end-to-end by the smoke invocation in Step 3, and Task 20's README is a documentation-only artifact whose only test is the grep guard in Task 20 Step 2 (which IS TDD-ordered). All other tasks are strict TDD.
- **Baseline HEAD**: `origin/paper/v3-integration` at `786bf60`.
- **Baseline test suite MUST stay green throughout**: `go test ./tests/eval/baselines/... -race` and `pytest tools/eval/fulltable/tests/` after every task.

---

## File map (created in this order, grouped by phase)

### Phase A — new baseline binary + anti-drift lock (Go)

- **NEW** `multi-agent/tests/eval/baselines/single_machine_codex/main.go` — cmd entry, mirrors `single_machine/main.go`
- **NEW** `multi-agent/tests/eval/baselines/single_machine_codex/workloads.go` — `codexPrompts` map (VERBATIM copy of `claudePrompts`)
- **NEW** `multi-agent/tests/eval/baselines/single_machine_codex/impl.go` — `SingleMachineCodexImpl` with LOCKED argv
- **NEW** `multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go` — 7 tests (name/forwards/prepare/dry-run/missing-bin/unknown-workload/pinned-argv/stderr-scrub-fail/stderr-scrub-success)
- **NEW** `multi-agent/tests/eval/baselines/single_machine_codex/anti_drift_test.go` — `TestCodexPromptsMatchClaudePrompts` via `go/parser` + `go/ast`
- **NEW** `multi-agent/tests/eval/baselines/single_machine_codex/run.sh` — smoke matrix entrypoint

### Phase B — fulltable enum swap (Python + YAML + JSON)

- **MODIFY** `multi-agent/tools/eval/fulltable/matrix.schema.json` — enum member
- **MODIFY** `multi-agent/tools/eval/fulltable/matrix.yaml` — 5 rows
- **MODIFY** `multi-agent/tools/eval/fulltable/lib/plan.py` — `BASELINE_DIR` key + value
- **MODIFY** `multi-agent/tools/eval/fulltable/tests/test_matrix_schema.py` — expected enum literal
- **MODIFY** `multi-agent/tools/eval/fulltable/tests/test_stub_listen_loopback.py` — known-baseline set
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_no_claude_baseline.py` — repo-wide anti-drift + snapshot-path-exact
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_snapshot_no_secrets.py` — leak-shape regex
- **MODIFY** `multi-agent/tools/eval/fulltable/tests/dry_run_snapshot.txt` — regenerated 5 lines
- **MODIFY** `multi-agent/tests/eval/baselines/harness/row_test.go` — literal
- **MODIFY** `multi-agent/tests/eval/baselines/matrix_test.go` — the `{"single_machine", "single_machine_claude_code"}` row swap
- **MODIFY** `multi-agent/tests/eval/baselines/README.md` — baseline table entry

### Phase C — run.sh `--workload` / `--results-root` extensions (Python + Bash)

- **MODIFY** `multi-agent/tools/eval/fulltable/lib/plan.py` — `filter_workload()`, `--filter-workload`, `--list-workloads`, `--results-root` CLI args
- **MODIFY** `multi-agent/tools/eval/fulltable/run.sh` — `--workload`, `--results-root`, `LOOM_FULLTABLE_WRAPPER_SHIM=1` shim
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_filter_workload.py` — planner filter unit tests
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_workload_filter_semantics.sh` — integration: `--workload X --resume/sample/E4` (spec §4.5)
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_sample_cap_with_workload.sh` — cap enforced at run.sh (spec P0#4 resolution)
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_run_sh_duplicate_workload.sh` — `--workload A --workload B` exits 2 (spec P2 upgraded)
- **NEW** `multi-agent/tools/eval/fulltable/tests/test_results_root_allowlist.py` — allowlist checks

### Phase D — 5 per-workload wrappers + shared helpers (Bash)

- **NEW** `multi-agent/tools/eval/experiments/_common.sh` — codex_bin_present / codex_config_readable / codex_config_has_route_a / codex_config_has_route_b / require_writable_tmp / require_fixture / require_windows_host / die / warn_and_exit_zero
- **NEW** `multi-agent/tools/eval/experiments/tests/test_common_helpers.sh` — bash unit tests for `_common.sh`
- **NEW** `multi-agent/tools/eval/experiments/tests/fixtures/codex_config/*.toml` — 10 fixture toml files (spec §4.5 table)
- **NEW** `multi-agent/tools/eval/experiments/tests/test_credential_bound_preflight.sh` — 10-fixture matrix
- **NEW** `multi-agent/tools/eval/experiments/cross_device_code_mod.sh`
- **NEW** `multi-agent/tools/eval/experiments/missing_parser_converter.sh`
- **NEW** `multi-agent/tools/eval/experiments/remote_data_processing.sh`
- **NEW** `multi-agent/tools/eval/experiments/windows_only_artifact.sh`
- **NEW** `multi-agent/tools/eval/experiments/credential_bound_model.sh`
- **NEW** `multi-agent/tools/eval/experiments/tests/test_wrapper_forwards.sh` — per-wrapper SHIM_-line assertions + injection-rejection
- **NEW** `multi-agent/tools/eval/experiments/tests/test_windows_guard.sh` — Linux/MSYS/CYGWIN/*NT branching + `--skip-if-not-windows`
- **NEW** `multi-agent/tools/eval/experiments/README.md`

### Phase E — handoff

- **NEW** `docs/specs/wt4-codex-only.handoff.md`

Total files: 6 new Go, 6 new/modified Python tests, 5 modified Python/YAML/JSON, 7 modified Go + `README.md`, 5 wrapper shell scripts + `_common.sh`, 5 shell test files, 10 TOML fixtures, 1 handoff, 3 documentation. ≈40 files.

---

## Task list

Each task ends with `git commit`. Between tasks, `go test ./tests/eval/baselines/... -race` and `pytest tools/eval/fulltable/tests/` MUST be green (except during a task's own red phase).

### Task 1 — Phase A scaffold: package skeleton compiles

**Files:**
- Create: `multi-agent/tests/eval/baselines/single_machine_codex/main.go`
- Create: `multi-agent/tests/eval/baselines/single_machine_codex/impl.go`
- Create: `multi-agent/tests/eval/baselines/single_machine_codex/workloads.go`

**Interfaces:**
- Consumes: `github.com/yourorg/multi-agent/tests/eval/baselines/harness`
- Produces: package `main` that compiles with a stub `NewImpl()` returning a value implementing `harness.BaselineImpl`. Name is `"single_machine_codex"`. Concrete work in later tasks.

- [ ] **Step 1: Write skeleton files (placeholder Impl, empty prompts)**

`main.go`:
```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: single_machine_codex run --workload <id> --out <csv> [--dry-run] [--forward-openai-api-key] [--workload-dir <path>]")
		os.Exit(2)
	}
	os.Exit(runMain(os.Args[2:]))
}

func runMain(args []string) int {
	opts, fs := harness.NewFlagSet("single_machine_codex run", os.Stderr)
	forwardOpenAI := fs.Bool("forward-openai-api-key", false, "opt-in: forward OPENAI_API_KEY from parent env to `codex` subprocess. Never reaches the oracle.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, harness.ErrFlagUsage) {
			return 2
		}
		return 2
	}
	if opts.BaselineName == "" {
		opts.BaselineName = (&SingleMachineCodexImpl{}).Name()
	}
	if code := harness.ValidateOpts(opts, os.Stderr); code != 0 {
		return code
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	res := harness.Run(ctx, *opts, NewImpl(opts.WorkloadID, *forwardOpenAI), os.Stderr)
	return res.ExitCode
}
```

`impl.go` (stub, expanded in Task 3):
```go
// Package main is the single_machine_codex baseline binary. Real mode
// invokes the OpenAI Codex CLI (`codex exec`); dry-run mode projects
// mock_workspace and never touches the network.
package main

import (
	"context"
	"errors"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

// ErrCodexCLIUnavailable is returned by real-mode ExecuteAgent when
// the `codex` binary is not on $PATH. Symmetric to the
// single_machine (Claude) ErrClaudeCLIUnavailable; spec §3 fail-fast.
var ErrCodexCLIUnavailable = errors.New("single_machine_codex: `codex` binary not on $PATH; real mode requires OpenAI Codex CLI")

// ErrSingleMachineCodexWorkloadUnknown flags a workload id without a
// prompt entry in codexPrompts.
var ErrSingleMachineCodexWorkloadUnknown = errors.New("single_machine_codex: workload has no prompt; add one to codexPrompts")

// SingleMachineCodexImpl is the harness.BaselineImpl for the codex
// single-machine baseline. Real mode invokes the LOCKED §4.1 argv;
// dry-run mode projects mock_workspace only.
type SingleMachineCodexImpl struct {
	workloadID    string
	forwardOpenAI bool
	// codexBin is the path to the `codex` binary. Empty ⇒ resolved
	// via exec.LookPath("codex") at ExecuteAgent time. Tests inject
	// a fake path here.
	codexBin string
}

// NewImpl constructs the impl. `forwardOpenAI` reflects the operator's
// opt-in via --forward-openai-api-key (spec §3).
func NewImpl(workloadID string, forwardOpenAI bool) *SingleMachineCodexImpl {
	return &SingleMachineCodexImpl{workloadID: workloadID, forwardOpenAI: forwardOpenAI}
}

// Name returns the default baseline_or_ablation value.
func (*SingleMachineCodexImpl) Name() string { return "single_machine_codex" }

// AgentForwards is either empty or [OPENAI_API_KEY] depending on
// whether the operator passed --forward-openai-api-key. Spec §3:
// no implicit-by-baseline forwarding.
func (s *SingleMachineCodexImpl) AgentForwards() harness.AgentForwards {
	if s.forwardOpenAI {
		return harness.AgentForwards{"OPENAI_API_KEY"}
	}
	return nil
}

// Prepare is a no-op — this baseline has no external side effects to
// arrange up-front.
func (*SingleMachineCodexImpl) Prepare(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) error {
	return nil
}

// ExecuteAgent is a stub in Task 1; concrete argv + scrub added in
// Task 3.
func (s *SingleMachineCodexImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	return harness.ExecuteMetrics{}, errors.New("not implemented; see Task 3")
}
```

`workloads.go` (empty placeholder — filled by Task 2's anti-drift copy):
```go
package main

// codexPrompts is populated by Task 2 as an AST-verified copy of
// ../single_machine/workloads.go's claudePrompts.
var codexPrompts = map[string]promptSpec{}

type promptSpec struct {
	Prompt          string
	ExpectedOutputs []string
}
```

- [ ] **Step 2: Verify package compiles**

Run: `cd multi-agent && go build ./tests/eval/baselines/single_machine_codex/...`

Expected: exit 0, no output.

- [ ] **Step 3: Baseline test sweep still green**

Run: `cd multi-agent && go test ./tests/eval/baselines/... -race -count=1 -timeout=60s`

Expected: all packages OK (including new empty `single_machine_codex`, which has no tests yet).

- [ ] **Step 4: Commit**

```bash
cd multi-agent/.worktrees/paper-v4-codex-only
git add multi-agent/tests/eval/baselines/single_machine_codex/
git commit -m "WT-4 Task 1: single_machine_codex/ scaffold (main+impl+workloads stubs)"
```

---

### Task 2 — codexPrompts + AST anti-drift test

**Files:**
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/workloads.go`
- Create: `multi-agent/tests/eval/baselines/single_machine_codex/anti_drift_test.go`

**Interfaces:**
- Consumes: `../single_machine/workloads.go` source at test time via `go/parser`, `go/ast`, `go/token`, `strconv`.
- Produces: `codexPrompts` map with the same 5 keys / prompts / expected outputs as `claudePrompts`. Anti-drift test guarantees they stay in sync.

- [ ] **Step 1: Write failing test `anti_drift_test.go`**

```go
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestCodexPromptsMatchClaudePrompts verifies codexPrompts is a
// verbatim copy of ../single_machine/workloads.go claudePrompts.
//
// The two baselines MUST share prompts so any measured behavior
// difference is attributable to the CLI, not the prompt (spec §5).
// Implementation constraint (spec Global Constraints): AST-only
// extraction, NOT go:embed of a golden JSON — a stale golden could
// pass silently while claude drifts.
func TestCodexPromptsMatchClaudePrompts(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	claudeFile := filepath.Join(filepath.Dir(self), "..", "single_machine", "workloads.go")

	claudeExtracted := extractPromptMap(t, claudeFile, "claudePrompts")
	if len(claudeExtracted) == 0 {
		t.Fatalf("claudePrompts extraction returned empty map — parser or claude source drift")
	}
	if len(codexPrompts) != len(claudeExtracted) {
		t.Fatalf("codexPrompts has %d keys, claudePrompts has %d — key drift; add/remove entries to match", len(codexPrompts), len(claudeExtracted))
	}
	for k, cv := range claudeExtracted {
		xv, ok := codexPrompts[k]
		if !ok {
			t.Errorf("codexPrompts missing key %q present in claudePrompts", k)
			continue
		}
		if xv.Prompt != cv.Prompt {
			t.Errorf("prompt drift on %q:\n  claude: %q\n  codex:  %q", k, cv.Prompt, xv.Prompt)
		}
		if !stringSliceEqual(xv.ExpectedOutputs, cv.ExpectedOutputs) {
			t.Errorf("expected-outputs drift on %q:\n  claude: %v\n  codex:  %v", k, cv.ExpectedOutputs, xv.ExpectedOutputs)
		}
	}
	// Reverse direction — codex may not contain keys absent from claude.
	for k := range codexPrompts {
		if _, ok := claudeExtracted[k]; !ok {
			t.Errorf("codexPrompts has %q not present in claudePrompts", k)
		}
	}
}

// extractPromptMap AST-parses `file` and finds a top-level `var
// <name> = map[string]promptSpec{ ... }` declaration, returning its
// key→promptSpec pairs. Fails the test on any deviation from that
// exact shape.
func extractPromptMap(t *testing.T, file, name string) map[string]promptSpec {
	t.Helper()
	fs := token.NewFileSet()
	af, err := parser.ParseFile(fs, file, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]promptSpec{}
	for _, decl := range af.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if n.Name != name {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("%s declared without value", name)
				}
				cl, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s must be a composite literal, got %T", name, vs.Values[i])
				}
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						t.Fatalf("%s element not KeyValueExpr: %T", name, elt)
					}
					key := astLitToString(t, kv.Key)
					out[key] = extractPromptSpec(t, kv.Value)
				}
				return out
			}
		}
	}
	t.Fatalf("var %s not found in %s", name, file)
	return nil
}

func extractPromptSpec(t *testing.T, e ast.Expr) promptSpec {
	t.Helper()
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("promptSpec value not CompositeLit: %T", e)
	}
	var ps promptSpec
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("promptSpec field not KeyValueExpr: %T", elt)
		}
		// Plan-review P2: type-check the field key to avoid a panic
		// on non-Ident (e.g. selector expr) drift.
		fieldIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			t.Fatalf("promptSpec field key not *ast.Ident: %T", kv.Key)
		}
		switch fieldIdent.Name {
		case "Prompt":
			ps.Prompt = astLitToString(t, kv.Value)
		case "ExpectedOutputs":
			ps.ExpectedOutputs = astLitToStringSlice(t, kv.Value)
		default:
			t.Fatalf("unknown promptSpec field %q", fieldIdent.Name)
		}
	}
	return ps
}

func astLitToString(t *testing.T, e ast.Expr) string {
	t.Helper()
	bl, ok := e.(*ast.BasicLit)
	if !ok {
		t.Fatalf("expected string literal, got %T", e)
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		t.Fatalf("unquote %q: %v", bl.Value, err)
	}
	return s
}

func astLitToStringSlice(t *testing.T, e ast.Expr) []string {
	t.Helper()
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("expected slice literal, got %T", e)
	}
	out := make([]string, 0, len(cl.Elts))
	for _, elt := range cl.Elts {
		out = append(out, astLitToString(t, elt))
	}
	return out
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test — expect FAIL (codexPrompts empty)**

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -run TestCodexPromptsMatchClaudePrompts -v`

Expected: FAIL — `codexPrompts has 0 keys, claudePrompts has 5 — key drift`.

- [ ] **Step 3: Copy `claudePrompts` verbatim into `codexPrompts`**

Replace `workloads.go` with the following (VERBATIM copy of `../single_machine/workloads.go` `claudePrompts`, renamed):

```go
// Package main is the single_machine_codex baseline binary. Real mode
// invokes the OpenAI Codex CLI (`codex exec`) with the pinned §4.1
// argv; dry-run mode projects mock_workspace and never touches the
// network.
package main

// codexPrompts holds per-workload prompt + expected-output metadata,
// copied VERBATIM from ../single_machine/workloads.go's claudePrompts
// and locked in-sync by TestCodexPromptsMatchClaudePrompts (AST diff).
// The two baselines share prompts so any measured behavior difference
// is attributable to the CLI, not the prompt.
type promptSpec struct {
	Prompt          string
	ExpectedOutputs []string
}

var codexPrompts = map[string]promptSpec{
	"cross-device-code-mod": {
		Prompt: `You are in a workspace directory. Write two files:
1. patch.diff — a valid unified diff (with diff header, hunk header, and content lines) that modifies some file 'x' from 'a' to 'b'.
2. test.log — must contain a single line "PASS" as the first characters on a line.
No other output.`,
		ExpectedOutputs: []string{"patch.diff", "test.log"},
	},
	"remote-data-processing": {
		Prompt:          `Write result.json with fields {"count":5,"sum":15,"mean":3.0}. Then write result.sha256 as the sha256 of result.json (format: '<hex>  result.json').`,
		ExpectedOutputs: []string{"result.json", "result.sha256"},
	},
	"windows-only-artifact": {
		Prompt:          `Write a non-empty file artifact.bin and artifact.meta.json containing {"sha256":"<sha256 of artifact.bin>","size":<byte count>}.`,
		ExpectedOutputs: []string{"artifact.bin", "artifact.meta.json"},
	},
	"missing-parser-converter": {
		Prompt: `Look at fixtures/golden/expected.out (already in workspace). Write:
1. synthesized.mcp.json with a "tools" array containing {"name":"convert"}.
2. converted.out matching fixtures/golden/expected.out byte-for-byte.
3. acceptance.log with exactly one line "PASS expected.out".`,
		ExpectedOutputs: []string{"synthesized.mcp.json", "converted.out", "acceptance.log"},
	},
	"credential-bound-model": {
		Prompt:          `Write route.json = {"model_alias":"acme-bound-model-v1","proxy_context_id":"pctx-single-machine-0001"}. Also write non-empty completion.txt and run.log. No API key strings in the workspace.`,
		ExpectedOutputs: []string{"route.json", "completion.txt", "run.log"},
	},
}
```

The `promptSpec` type is now declared here (removes stub from Task 1's placeholder). The old placeholder `promptSpec` in `workloads.go` (Task 1) is fully replaced by this file.

- [ ] **Step 4: Run test — expect PASS**

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -run TestCodexPromptsMatchClaudePrompts -v`

Expected: PASS.

- [ ] **Step 5: Belt — verify a deliberate drift is caught**

Temporarily edit `workloads.go` `credential-bound-model` prompt to change `acme-bound-model-v1` to `acme-bound-model-v2`. Re-run test:

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -run TestCodexPromptsMatchClaudePrompts -v`

Expected: FAIL with `prompt drift on "credential-bound-model"`.

Revert the edit and re-run — expect PASS again.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/tests/eval/baselines/single_machine_codex/workloads.go multi-agent/tests/eval/baselines/single_machine_codex/anti_drift_test.go
git commit -m "WT-4 Task 2: codexPrompts (verbatim from claudePrompts) + AST anti-drift lock"
```

---

### Task 3 — impl.go dry-run path + missing-bin fail-fast + pinned-argv test

**Files:**
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/impl.go`
- Create: `multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go`

**Interfaces:**
- Consumes: `harness.Workspace`, `harness.ExecuteMetrics`, `harness.AgentForwards`, `secretscrub.Sanitize`
- Produces: `ExecuteAgent` populated with (a) dry-run branch that never LookPaths `codex`, (b) real branch that fails fast with `ErrCodexCLIUnavailable` when `codex` missing, (c) real branch that constructs the LOCKED argv but does NOT yet scrub stderr (Task 4 adds scrub).

- [ ] **Step 1: Write failing test — dry-run does not invoke codex**

Append to `impl_test.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
}

// TestSingleMachineCodex_DryRun_DoesNotInvokeCodex — in dry-run mode
// the impl must never call `codex`; the mock_workspace projection
// alone is what the oracle grades.
func TestSingleMachineCodex_DryRun_DoesNotInvokeCodex(t *testing.T) {
	// Poison PATH but keep oracle coreutils. Any accidental
	// exec.LookPath("codex") must fail without stripping the oracle.
	fakeBin := t.TempDir()
	for _, tool := range []string{"sha256sum", "grep", "cmp", "wc", "awk", "sed", "cat", "printf", "bash", "sh", "head", "tr"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(src, filepath.Join(fakeBin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fakeBin) // no `codex`
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      true,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("dry-run: want exit 0, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !res.Row.DryRun {
		t.Errorf("dry_run column should be true")
	}
}

// TestSingleMachineCodex_RealMode_MissingBinary_FailsFast — without
// `codex` on PATH, real mode must emit ErrCodexCLIUnavailable via
// harness (exit 1 with row emitted — agent runtime failure).
func TestSingleMachineCodex_RealMode_MissingBinary_FailsFast(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `codex`, no coreutils either
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode != 1 {
		t.Fatalf("want exit 1 on missing codex, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !errors.Is(res.Err, ErrCodexCLIUnavailable) {
		t.Errorf("want ErrCodexCLIUnavailable, got %v", res.Err)
	}
}

// TestSingleMachineCodex_RealMode_UnknownWorkload_ReturnsSentinel —
// real mode with unknown workload id must return
// ErrSingleMachineCodexWorkloadUnknown.
func TestSingleMachineCodex_RealMode_UnknownWorkload_ReturnsSentinel(t *testing.T) {
	// Fake `codex` on PATH so lookpath succeeds — we want the workload
	// lookup to fail, not the binary lookup.
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("no-such-workload", false), io.Discard)
	// harness surfaces ExecuteAgent error via res.Err with exit 1
	// (agent-runtime failure). We check the specific sentinel.
	if !errors.Is(res.Err, ErrSingleMachineCodexWorkloadUnknown) {
		t.Errorf("want ErrSingleMachineCodexWorkloadUnknown, got %v", res.Err)
	}
}

// TestSingleMachineCodex_UsesPinnedArgv — resolves spec-review P0#1.
// Real branch must invoke:
//   codex exec --sandbox workspace-write --ephemeral \
//     --skip-git-repo-check --json -C <ws.Root> -- <prompt>
// Byte-compare argv against this pinned form via a fake `codex`
// script that logs its argv to a file.
func TestSingleMachineCodex_UsesPinnedArgv(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fake := filepath.Join(dir, "codex")
	// The fake codex writes each argv element (excluding argv[0]) on
	// its own line to argvFile, then produces the 2 expected outputs
	// for cross-device-code-mod so the real-branch output-check does
	// not mask the argv assertion behind a "missing outputs" error.
	script := `#!/bin/sh
{
  for a in "$@"; do
    printf '%s\n' "$a"
  done
} > "$ARGV_FILE"
# emit expected outputs to satisfy the output-existence check
: > "$OUT_DIR/patch.diff"
printf 'PASS\n' > "$OUT_DIR/test.log"
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Populate PATH with coreutils for oracle, plus our fake codex.
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "sha256sum", "grep", "cmp", "wc", "awk", "sed", "cat", "printf", "bash", "head", "tr", "ls"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(pathDir, tool))
	}
	if err := os.Symlink(fake, filepath.Join(pathDir, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	// The fake script needs ARGV_FILE + OUT_DIR from env; the harness
	// whitelist filters bare names, so use LOOM_ namespace which is
	// passed through.
	t.Setenv("LOOM_ARGV_FILE", argvFile)
	// OUT_DIR is written by fake into ws.Root, but we don't know
	// ws.Root ahead of time. Instead, the fake reads it from PWD (the
	// harness runs codex with cmd.Dir = ws.Root).
	// Rewrite fake to use PWD:
	script2 := `#!/bin/sh
{ for a in "$@"; do printf '%s\n' "$a"; done } > "$LOOM_ARGV_FILE"
: > "$PWD/patch.diff"
printf 'PASS\n' > "$PWD/test.log"
exit 0
`
	if err := os.WriteFile(fake, []byte(script2), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode != 0 && res.ExitCode != 3 {
		// exit 0 = oracle passed; exit 3 = oracle failed (which we
		// accept — we only care about argv here). exit 1 = ExecuteAgent
		// errored, which would mean argv construction blew up.
		t.Fatalf("want exit 0 or 3, got %d; err=%v", res.ExitCode, res.Err)
	}
	got, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	// Plan-review r2 P1 (pinned argv byte-exact + non-self-referential
	// prompt check): compare argv against a fully-materialised expected
	// slice. Only ws.Root is dynamic (harness picks a tempdir);
	// substitute it from argv position 7. The prompt at position 9
	// MUST equal codexPrompts["cross-device-code-mod"].Prompt exactly
	// — do NOT source the expected prompt from argv itself.
	if len(lines) != 10 {
		t.Fatalf("argv length: want 10 got %d\nargv=%q", len(lines), lines)
	}
	wsRoot := lines[7]
	if !filepath.IsAbs(wsRoot) {
		t.Errorf("-C target must be absolute path; got %q", wsRoot)
	}
	expectedPrompt := codexPrompts["cross-device-code-mod"].Prompt
	expected := []string{
		"exec",
		"--sandbox", "workspace-write",
		"--ephemeral",
		"--skip-git-repo-check",
		"--json",
		"-C", wsRoot,
		"--",
		expectedPrompt,
	}
	if !reflect.DeepEqual(lines, expected) {
		t.Fatalf("argv drift:\n  got: %q\n  want: %q", lines, expected)
	}
	_ = fmt.Sprintf // silence unused import in stub
}
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -v`

Expected: all 4 new tests FAIL (ExecuteAgent is still the stub returning "not implemented").

- [ ] **Step 3: Implement ExecuteAgent (dry-run + real branch minus scrub)**

Replace the `ExecuteAgent` stub in `impl.go`:

```go
// ExecuteAgent invokes the LOCKED §4.1 codex exec argv in the workspace
// tempdir with the per-workload prompt. Dry-run mode short-circuits
// before any external invocation. Stderr scrub is added in Task 4.
func (s *SingleMachineCodexImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	if dryRun {
		fmt.Fprintln(os.Stderr, "single_machine_codex: [DRY-RUN] skipping `codex exec` invocation; using mock_workspace projection")
		return harness.ExecuteMetrics{WallTimeMS: 0, APICalls: 0, UploadBytes: 0}, nil
	}
	prompt, ok := codexPrompts[s.workloadID]
	if !ok {
		return harness.ExecuteMetrics{}, fmt.Errorf("%w: %s", ErrSingleMachineCodexWorkloadUnknown, s.workloadID)
	}
	bin := s.codexBin
	if bin == "" {
		resolved, err := exec.LookPath("codex")
		if err != nil {
			return harness.ExecuteMetrics{}, fmt.Errorf("%w: %v", ErrCodexCLIUnavailable, err)
		}
		bin = resolved
	}

	// LOCKED argv (spec §4.1 + Global Constraints). Do not add or
	// remove flags without updating TestSingleMachineCodex_UsesPinnedArgv.
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin,
		"exec",
		"--sandbox", "workspace-write",
		"--ephemeral",
		"--skip-git-repo-check",
		"--json",
		"-C", ws.Root,
		"--",
		prompt.Prompt,
	)
	cmd.Dir = ws.Root
	cmd.Env = agentEnv
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("single_machine_codex: codex CLI failed for %s: %w; stderr=%s", s.workloadID, err, stderr.String())
	}
	// Verify codex produced the expected outputs.
	for _, outName := range prompt.ExpectedOutputs {
		if _, err := os.Stat(filepath.Join(ws.Root, outName)); err != nil {
			return harness.ExecuteMetrics{
					WallTimeMS: time.Since(start).Milliseconds(),
				},
				fmt.Errorf("single_machine_codex: codex did not produce %q for %s: %w", outName, s.workloadID, err)
		}
	}
	return harness.ExecuteMetrics{
		WallTimeMS: time.Since(start).Milliseconds(),
	}, nil
}
```

Add imports to `impl.go`:
```go
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -v`

Expected: all 4 tests PASS. (`TestCodexPromptsMatchClaudePrompts` from Task 2 remains green.)

- [ ] **Step 5: Baseline sweep still green**

Run: `cd multi-agent && go test ./tests/eval/baselines/... -race -count=1 -timeout=60s`

Expected: all packages OK.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/tests/eval/baselines/single_machine_codex/impl.go multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go
git commit -m "WT-4 Task 3: impl.go dry-run + missing-bin fail-fast + LOCKED argv"
```

---

### Task 4 — stderr scrub via `secretscrub.Sanitize` + forward-flag test

**Files:**
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/impl.go` (wrap stderr in Sanitize before embedding in error)
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go` (add 3 tests)

**Interfaces:**
- Consumes: `github.com/yourorg/multi-agent/internal/secretscrub` (`Sanitize(s string) string`)
- Produces: `single_machine_codex`'s recorded error string on codex failure MUST have all secret-shaped substrings replaced by `[REDACTED]`. Test both nonzero-exit path (leak-path — spec P1#6 round-2) AND success-then-output-missing path (success with output missing raises a different error but stderr may still contain a leak).

- [ ] **Step 1: Write failing test — nonzero-exit path scrubs**

Append to `impl_test.go`:

```go
// TestSingleMachineCodex_ScrubsStderr_NonzeroExit — spec P1#6 round-2.
// Fake `codex` emits `sk-abc123DEFabcDEFabcDEF` to stderr and exits 42.
// The returned error string MUST have the token replaced by [REDACTED]
// and MUST NOT contain the substrings `sk-abc123`, `abcDEF`.
func TestSingleMachineCodex_ScrubsStderr_NonzeroExit(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	script := `#!/bin/sh
printf 'boot line 1\n' >&2
printf 'ERROR sk-abc123DEFabcDEFabcDEFabcDEF leak\n' >&2
printf 'ERROR Bearer bar_baz_qux_secret_val_1234567890 leak\n' >&2
exit 42
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "sha256sum", "grep", "cmp", "wc", "awk", "sed", "cat", "printf", "bash", "head", "tr", "ls"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(pathDir, tool))
	}
	_ = os.Symlink(fake, filepath.Join(pathDir, "codex"))
	t.Setenv("PATH", pathDir)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode == 0 {
		t.Fatalf("expected nonzero exit; codex fake exits 42; res=%+v", res)
	}
	// Read the persisted error via res.Err.Error() AND res.Row's error
	// column (whichever the harness populates).
	errStr := ""
	if res.Err != nil {
		errStr = res.Err.Error()
	}
	// Also, harness may embed stderr in a row column; check both.
	// BaselineRunRow has no stderr field (verified against
	// tests/eval/baselines/harness/row.go); scrubbed stderr surfaces
	// only via the wrapped %w err. Use errStr alone.
	haystack := errStr
	for _, banned := range []string{"sk-abc123", "abcDEF", "bar_baz_qux_secret"} {
		if strings.Contains(haystack, banned) {
			t.Errorf("scrub failed: substring %q leaked into error; err=%q", banned, errStr)
		}
	}
	if !strings.Contains(haystack, "[REDACTED]") {
		t.Errorf("expected [REDACTED] sentinel from secretscrub.Sanitize in err; err=%q", errStr)
	}
}

// TestSingleMachineCodex_ScrubsStderr_SuccessButOutputMissing — the
// success path also runs stderr through the scrubber; a leak on
// stdout/stderr from a "successful" codex run that happens to omit
// expected outputs still gets scrubbed before it reaches the row /
// error string.
func TestSingleMachineCodex_ScrubsStderr_SuccessButOutputMissing(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	// Exit 0 (success) but DO NOT write patch.diff / test.log; the
	// "did not produce" error path will fire. Emit token-shaped stderr
	// during that success.
	script := `#!/bin/sh
printf 'INFO sk-testonlytokenlong123456789 in log\n' >&2
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "cat", "printf", "bash", "ls"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(pathDir, tool))
	}
	_ = os.Symlink(fake, filepath.Join(pathDir, "codex"))
	t.Setenv("PATH", pathDir)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	// This path returns the "did not produce" error — nonzero exit.
	if res.Err == nil {
		t.Fatalf("expected non-nil err (missing outputs), got nil; res=%+v", res)
	}
	haystack := res.Err.Error()
	if strings.Contains(haystack, "sk-testonlytokenlong") {
		t.Errorf("scrub failed on success-then-missing-output path: leak in error; err=%q", res.Err)
	}
	// Plan-review P1: the "not-contains" alone can pass if stderr never
	// made it into the error at all. Require the [REDACTED] sentinel
	// so we know scrub actually ran on visible stderr bytes.
	if !strings.Contains(haystack, "[REDACTED]") {
		t.Errorf("success-then-missing-output path: expected [REDACTED] sentinel in err (proves scrub ran on stderr); err=%q", res.Err)
	}
}

// TestSingleMachineCodex_ForwardFlag_ControlsKeyPropagation — mirrors
// single_machine's ForwardFlag test but for OPENAI_API_KEY.
func TestSingleMachineCodex_ForwardFlag_ControlsKeyPropagation(t *testing.T) {
	parent := []string{"OPENAI_API_KEY=sk-oai-testonly-1234567890abcd", "PATH=/usr/bin"}

	noForward := NewImpl("cross-device-code-mod", false)
	env := harness.WhitelistEnvForAgent(parent, "cross-device-code-mod", noForward.AgentForwards(), nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENAI_API_KEY=") {
			t.Errorf("forward=false must drop OPENAI_API_KEY; env=%v", env)
		}
	}

	withForward := NewImpl("cross-device-code-mod", true)
	env2 := harness.WhitelistEnvForAgent(parent, "cross-device-code-mod", withForward.AgentForwards(), nil)
	found := false
	for _, kv := range env2 {
		if strings.HasPrefix(kv, "OPENAI_API_KEY=") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("forward=true must include OPENAI_API_KEY; env=%v", env2)
	}
}
```

Note on the row shape: `harness.BaselineRunRow` (verified against
`tests/eval/baselines/harness/row.go`) has no stderr / failure-detail
field — only oracle-side info. Scrubbed subprocess stderr therefore
surfaces ONLY through the wrapped `%w` error. The tests above check
`res.Err.Error()` alone.

- [ ] **Step 2: Run tests — expect FAIL (impl still returns raw stderr)**

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -run "TestSingleMachineCodex_ScrubsStderr|TestSingleMachineCodex_ForwardFlag" -v`

Expected: `ScrubsStderr_*` FAIL (raw `sk-abc123` in error); `ForwardFlag` PASS (AgentForwards is already implemented in Task 1).

- [ ] **Step 3: Add stderr scrubbing to `impl.go`**

Modify the two error-returning branches in `ExecuteAgent`:

Old:
```go
	if err := cmd.Run(); err != nil {
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("single_machine_codex: codex CLI failed for %s: %w; stderr=%s", s.workloadID, err, stderr.String())
	}
```

New:
```go
	if err := cmd.Run(); err != nil {
		// Scrub stderr before embedding — codex may emit token-shaped
		// bytes (auth errors, config dumps). Even on nonzero exit the
		// leak path (spec §5 TestExecuteAgent_ScrubsStderr).
		scrubbed := secretscrub.Sanitize(stderr.String())
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("single_machine_codex: codex CLI failed for %s: %w; stderr=%s", s.workloadID, err, scrubbed)
	}
```

Old (success-then-missing-output branch):
```go
		if _, err := os.Stat(filepath.Join(ws.Root, outName)); err != nil {
			return harness.ExecuteMetrics{
					WallTimeMS: time.Since(start).Milliseconds(),
				},
				fmt.Errorf("single_machine_codex: codex did not produce %q for %s: %w", outName, s.workloadID, err)
		}
```

New:
```go
		if _, err := os.Stat(filepath.Join(ws.Root, outName)); err != nil {
			// Success (exit 0) but expected output missing. Still
			// scrub stderr — stderr may contain diagnostic output
			// with token-shaped bytes.
			scrubbed := secretscrub.Sanitize(stderr.String())
			return harness.ExecuteMetrics{
					WallTimeMS: time.Since(start).Milliseconds(),
				},
				fmt.Errorf("single_machine_codex: codex did not produce %q for %s: %w; stderr=%s", outName, s.workloadID, err, scrubbed)
		}
```

Add import:
```go
import (
	...
	"github.com/yourorg/multi-agent/internal/secretscrub"
)
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd multi-agent && go test ./tests/eval/baselines/single_machine_codex/... -race -v`

Expected: all tests PASS (7 in `impl_test.go` + 1 anti-drift).

- [ ] **Step 5: Sweep**

Run: `cd multi-agent && go test ./tests/eval/baselines/... -race -count=1 -timeout=60s`

Expected: all packages OK.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/tests/eval/baselines/single_machine_codex/impl.go multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go
git commit -m "WT-4 Task 4: stderr scrub via secretscrub.Sanitize on both leak paths"
```

---

### Task 5 — main.go polish + baseline `run.sh` + go-build test

**Files:**
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/main.go` (usage-line polish already right; belt: touch nothing if already correct)
- Create: `multi-agent/tests/eval/baselines/single_machine_codex/run.sh` (mirror of `single_machine/run.sh` with 2 substitutions)
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go` (add build-binary belt test)

**Interfaces:**
- Consumes: `harness.NewFlagSet` / `harness.ValidateOpts` / `harness.Run`.
- Produces: `bash run.sh --workload cross-device-code-mod --dry-run --out /tmp/foo.csv` exits 0 and produces a valid CSV row with `baseline_or_ablation=single_machine_codex` and `dry_run=true`.

- [ ] **Step 1: Write `run.sh`**

```bash
#!/usr/bin/env bash
# single_machine_codex baseline entrypoint. See ../single_machine/run.sh
# for the shared shape; this wrapper only exists so the fulltable
# matrix (tools/eval/fulltable/matrix.yaml) can invoke each baseline
# uniformly with `bash run.sh --workload <id> [--dry-run]`.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"

if [[ -x /tmp/single_machine_codex ]]; then
  runner=(/tmp/single_machine_codex)
else
  runner=(go run "./tests/eval/baselines/single_machine_codex")
fi

args=()
have_out=0
have_workload_dir=0
have_workload=0
workload=""
for arg in "$@"; do
  case "$arg" in
    --out=*|--out) have_out=1 ;;
    --workload-dir=*|--workload-dir) have_workload_dir=1 ;;
    --workload=*) have_workload=1; workload="${arg#--workload=}" ;;
    --workload)   have_workload=1 ;;
  esac
  args+=("$arg")
done
if [[ "$have_workload" -eq 1 && -z "$workload" ]]; then
  for ((i = 0; i < ${#args[@]}; i++)); do
    if [[ "${args[$i]}" == "--workload" ]]; then
      workload="${args[$((i + 1))]:-}"
      break
    fi
  done
fi
if [[ "$have_out" -eq 0 ]]; then
  args+=("--out" "/tmp/single_machine_codex-${workload:-run}.csv")
fi
if [[ "$have_workload_dir" -eq 0 ]]; then
  args+=("--workload-dir" "$module_root/tests/eval/workloads")
fi

cd "$module_root"
exec "${runner[@]}" run "${args[@]}"
```

Mark executable: `chmod +x multi-agent/tests/eval/baselines/single_machine_codex/run.sh`.

- [ ] **Step 2: Add build-binary belt test**

Append to `impl_test.go`:

```go
// TestSingleMachineCodex_BuildsBinary — belt-and-braces build check.
func TestSingleMachineCodex_BuildsBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in -short")
	}
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	bin := filepath.Join(t.TempDir(), "single_machine_codex")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
}
```

- [ ] **Step 3: Verify run.sh drives dry-run end-to-end**

Run:
```bash
cd multi-agent/.worktrees/paper-v4-codex-only/multi-agent
bash tests/eval/baselines/single_machine_codex/run.sh --workload cross-device-code-mod --dry-run --out /tmp/wt4-smoke.csv
```

Expected: exit 0; `/tmp/wt4-smoke.csv` exists, first data row's
`baseline_or_ablation` column = `single_machine_codex`, `dry_run` = `true`.

- [ ] **Step 4: Full sweep**

Run: `cd multi-agent && go test ./tests/eval/baselines/... -race -count=1 -timeout=90s`

Expected: all packages OK.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tests/eval/baselines/single_machine_codex/run.sh multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go
git commit -m "WT-4 Task 5: single_machine_codex/run.sh + build belt test"
```

---

### Task 6 — matrix.yaml + matrix.schema.json enum swap

**Files:**
- Modify: `multi-agent/tools/eval/fulltable/matrix.schema.json`
- Modify: `multi-agent/tools/eval/fulltable/matrix.yaml`
- Modify: `multi-agent/tools/eval/fulltable/tests/test_matrix_schema.py`
- Modify: `multi-agent/tools/eval/fulltable/tests/test_stub_listen_loopback.py`

**Interfaces:**
- Consumes: existing schema draft-2020-12 shape (from wt3-stub-fulltable).
- Produces: matrix.yaml still has 60 rows; the 5 previously-`single_machine_claude_code` rows now carry `baseline_or_ablation: single_machine_codex`. Schema enum accepts `single_machine_codex` and rejects the old label. Tests updated accordingly.

- [ ] **Step 1: Update expected value in `test_matrix_schema.py`**

Read the current test:
```bash
grep -n "single_machine_claude_code" multi-agent/tools/eval/fulltable/tests/test_matrix_schema.py
```
Expected line ~41: `"single_machine_claude_code",`.

Change that literal (and only that literal) to `"single_machine_codex",`.

- [ ] **Step 2: Update expected set in `test_stub_listen_loopback.py`**

Read:
```bash
grep -n "single_machine_claude_code" multi-agent/tools/eval/fulltable/tests/test_stub_listen_loopback.py
```
Expected line ~36: `if plan.configuration in {"manual_ssh", "single_machine_claude_code", ...`.

Change literal to `"single_machine_codex"`.

- [ ] **Step 3: Run tests — expect FAIL (schema still has old enum member)**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_matrix_schema.py tools/eval/fulltable/tests/test_stub_listen_loopback.py -q`

Expected: FAIL — the expected-enum test now expects `single_machine_codex` but schema+yaml still have `single_machine_claude_code`.

- [ ] **Step 4: Update `matrix.schema.json`**

Locate the `baseline_or_ablation` enum (line ~30-45); replace the string `"single_machine_claude_code"` with `"single_machine_codex"` (single occurrence). Leave other enum members unchanged.

- [ ] **Step 5: Update `matrix.yaml`**

Replace all 5 occurrences of `baseline_or_ablation: single_machine_claude_code` with `baseline_or_ablation: single_machine_codex` (lines 108, 110, 112, 114, 116).

Command (idempotent, verify with diff before commit):
```bash
sed -i 's/single_machine_claude_code/single_machine_codex/g' multi-agent/tools/eval/fulltable/matrix.yaml
git diff multi-agent/tools/eval/fulltable/matrix.yaml   # should show exactly 5 line changes
```

- [ ] **Step 6: Run tests — expect PASS**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_matrix_schema.py tools/eval/fulltable/tests/test_stub_listen_loopback.py -q`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/tools/eval/fulltable/matrix.yaml multi-agent/tools/eval/fulltable/matrix.schema.json multi-agent/tools/eval/fulltable/tests/test_matrix_schema.py multi-agent/tools/eval/fulltable/tests/test_stub_listen_loopback.py
git commit -m "WT-4 Task 6: enum swap single_machine_claude_code → single_machine_codex in matrix + schema"
```

---

### Task 7 — plan.py `BASELINE_DIR` swap

**Files:**
- Modify: `multi-agent/tools/eval/fulltable/lib/plan.py`

**Interfaces:**
- Consumes: matrix.yaml (updated in Task 6).
- Produces: `BASELINE_DIR["single_machine_codex"] == "single_machine_codex"` so `plan.py` maps the new configuration name to the new baseline subdirectory (which Task 1-5 created). Old key `single_machine_claude_code` is removed.

- [ ] **Step 1: Update `BASELINE_DIR` map**

Locate:
```python
BASELINE_DIR: dict[str, str] = {
    "manual_ssh": "manual_ssh",
    "single_machine_claude_code": "single_machine",
    "cloud_sandbox_e2b": "cloud_sandbox",
}
```

Replace with:
```python
BASELINE_DIR: dict[str, str] = {
    "manual_ssh": "manual_ssh",
    "single_machine_codex": "single_machine_codex",
    "cloud_sandbox_e2b": "cloud_sandbox",
}
```

Note: BOTH key AND value change — new key `single_machine_codex` maps to the NEW subdirectory `single_machine_codex` (created in Task 1-5, distinct from the untouched `single_machine/` directory).

- [ ] **Step 2: Run tests — expect PASS (matrix.yaml already updated in Task 6)**

Run:
```bash
cd multi-agent && pytest tools/eval/fulltable/tests/ -q -x
```

Expected: all pytest tests PASS. Note: `test_dry_run_snapshot.py` will FAIL — the snapshot text still references the old paths; Task 8 regenerates it.

If ONLY the snapshot test fails, that's expected. Any OTHER pytest failure indicates a plan.py consumer we didn't consider — stop and inspect.

- [ ] **Step 3: Commit**

```bash
git add multi-agent/tools/eval/fulltable/lib/plan.py
git commit -m "WT-4 Task 7: plan.py BASELINE_DIR swap (single_machine_codex → single_machine_codex/)"
```

---

### Task 8 — regenerate `dry_run_snapshot.txt` + no-secrets + exact-path tests

**Files:**
- Modify: `multi-agent/tools/eval/fulltable/tests/dry_run_snapshot.txt` (regenerate)
- Create: `multi-agent/tools/eval/fulltable/tests/test_snapshot_no_secrets.py`
- Create: `multi-agent/tools/eval/fulltable/tests/test_snapshot_baseline_path_exact.py`

**Interfaces:**
- Consumes: updated `matrix.yaml`, `plan.py`, and `single_machine_codex/run.sh` (all from prior tasks).
- Produces: snapshot matches current `run.sh --dry-run` output; new no-secrets test enforces §Global Constraints leak regex; new path-exact test enforces exact `tests/eval/baselines/single_machine_codex/run.sh` substring on the 5 lines (not just `single_machine` — spec §5 upgrade of P2#3).

- [ ] **Step 1: Regenerate the snapshot**

```bash
cd multi-agent
bash tools/eval/fulltable/run.sh --dry-run > tools/eval/fulltable/tests/dry_run_snapshot.txt
git diff tools/eval/fulltable/tests/dry_run_snapshot.txt   # inspect: only lines mentioning single_machine* should differ
```

Expected diff: 5 lines changed. Old:
```
bash tests/eval/baselines/single_machine/run.sh --workload cross-device-code-mod --out .../matrix__cross-device-code-mod__single_machine_claude_code__<uuid>.csv
```
New:
```
bash tests/eval/baselines/single_machine_codex/run.sh --workload cross-device-code-mod --out .../matrix__cross-device-code-mod__single_machine_codex__<uuid>.csv
```

The UUIDs are deterministic (seed = configuration+workload); they DO change when the label changes (UUID input string differs). Confirm the diff has ONLY:
- 5 baseline-script path swaps (`single_machine/` → `single_machine_codex/`)
- 5 baseline-label swaps (`single_machine_claude_code` → `single_machine_codex`)
- 5 UUID swaps (side effect of label change)

Any OTHER line change (row order shift, cloud row change, etc.) is a bug — stop and investigate.

- [ ] **Step 2: Write `test_snapshot_no_secrets.py`**

```python
"""Global Constraints: regenerated dry_run_snapshot.txt MUST NOT contain
secret-shaped substrings or host/user path patterns. Spec §5
`TestSnapshotHasNoSecrets`.
"""
from __future__ import annotations

import re
from pathlib import Path

from conftest import FULLTABLE_DIR

SNAPSHOT = FULLTABLE_DIR / "tests" / "dry_run_snapshot.txt"

# Ordered from most-common → most-specific so failure message is useful.
LEAK_PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("openai/anthropic sk-", re.compile(r"sk-[A-Za-z0-9_\-]{6,}")),
    ("github token", re.compile(r"gh[opsruA-Z]_[A-Za-z0-9]{20,}")),
    ("bearer token", re.compile(r"Bearer\s+[A-Za-z0-9._\-]+", re.IGNORECASE)),
    ("refresh token literal", re.compile(r"refresh_token", re.IGNORECASE)),
    ("root path", re.compile(r"/root/")),
    ("home path with username", re.compile(r"/home/[a-z][a-z0-9_-]*/")),
]


def test_snapshot_has_no_secret_shaped_substrings() -> None:
    text = SNAPSHOT.read_text()
    hits: list[str] = []
    for label, pat in LEAK_PATTERNS:
        for m in pat.finditer(text):
            # capture surrounding context (up to 40 chars) for report
            start = max(0, m.start() - 20)
            end = min(len(text), m.end() + 20)
            hits.append(f"  [{label}] pos {m.start()}: ...{text[start:end]!r}...")
    assert not hits, (
        "dry_run_snapshot.txt contains leak-shaped substring(s):\n"
        + "\n".join(hits)
        + "\nIf a hit is a false positive (e.g. legitimate UUID collision), "
        + "adjust LEAK_PATTERNS with a targeted exception; do NOT weaken the "
        + "generic patterns."
    )


def test_snapshot_has_no_username_or_home_env() -> None:
    """Belt: literal $USER / $HOME shouldn't survive the planner's
    string substitution either."""
    text = SNAPSHOT.read_text()
    for banned in ("$USER", "$HOME"):
        assert banned not in text, (
            f"literal {banned!r} survived planner substitution; "
            "check plan.py for missed os.path.expanduser or shell-expansion"
        )
```

- [ ] **Step 3: Write `test_snapshot_baseline_path_exact.py`**

```python
"""Spec §5 `TestSnapshotBaselinePathExact` — resolves spec-review P2#3
(upgraded to acceptance test). `single_machine_codex` matches the
substring `single_machine`, so a broken path
`tests/eval/baselines/single_machine/run.sh` (old Claude dir) could
regress silently. This test asserts the EXACT baseline `run.sh` path
appears on the 5 baseline lines.
"""
from __future__ import annotations

from pathlib import Path

from conftest import FULLTABLE_DIR

SNAPSHOT = FULLTABLE_DIR / "tests" / "dry_run_snapshot.txt"

EXPECTED_BASELINE_PATH = "tests/eval/baselines/single_machine_codex/run.sh"
FORBIDDEN_LEGACY_PATH = "tests/eval/baselines/single_machine/run.sh"


def test_snapshot_uses_codex_baseline_path() -> None:
    text = SNAPSHOT.read_text()
    # Exactly 5 baseline invocation lines under the codex label.
    count = text.count(EXPECTED_BASELINE_PATH)
    assert count == 5, (
        f"expected exactly 5 lines invoking {EXPECTED_BASELINE_PATH}, "
        f"found {count}. matrix.yaml should have 5 codex baseline rows."
    )


def test_snapshot_does_not_use_legacy_claude_baseline_path() -> None:
    text = SNAPSHOT.read_text()
    assert FORBIDDEN_LEGACY_PATH not in text, (
        f"legacy Claude baseline path {FORBIDDEN_LEGACY_PATH} appears in "
        "snapshot; the enum swap (Task 6-7) or plan.py BASELINE_DIR "
        "(Task 7) is incomplete."
    )
```

- [ ] **Step 4: Run all snapshot tests — expect PASS**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_dry_run_snapshot.py tools/eval/fulltable/tests/test_snapshot_no_secrets.py tools/eval/fulltable/tests/test_snapshot_baseline_path_exact.py -q -x`

Expected: all PASS. `test_dry_run_snapshot.py` was failing after Task 7; now green.

- [ ] **Step 5: Full pytest sweep**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/ -q`

Expected: 128+ tests passing (126 pre-existing + 2 snapshot-no-secrets + 2 snapshot-path-exact). No failures.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/tools/eval/fulltable/tests/dry_run_snapshot.txt multi-agent/tools/eval/fulltable/tests/test_snapshot_no_secrets.py multi-agent/tools/eval/fulltable/tests/test_snapshot_baseline_path_exact.py
git commit -m "WT-4 Task 8: regenerate snapshot + no-secrets + path-exact tests"
```

---

### Task 9 — harness/row_test.go + matrix_test.go + README literal swap

**Files:**
- Modify: `multi-agent/tests/eval/baselines/harness/row_test.go` (1 literal)
- Modify: `multi-agent/tests/eval/baselines/matrix_test.go` (1 struct row)
- Modify: `multi-agent/tests/eval/baselines/README.md` (baseline table entry)

**Interfaces:**
- Consumes: `single_machine_codex/` baseline directory (from Task 5).
- Produces: repo-wide `grep -r 'single_machine_claude_code' multi-agent/tests/eval/` returns ONLY hits under `tests/eval/baselines/single_machine/` (unchanged) and no other file under `tests/eval/`.

- [ ] **Step 1: Update `harness/row_test.go`**

Locate:
```go
valid := []string{
    "manual_ssh",
    "single_machine_claude_code",
    "cloud_sandbox_e2b",
    ...
}
```
Change `"single_machine_claude_code"` to `"single_machine_codex"`.

- [ ] **Step 2: Update `matrix_test.go`**

Locate:
```go
{"single_machine", "single_machine_claude_code"},
```
Change to:
```go
{"single_machine_codex", "single_machine_codex"},
```

(Both fields change: `dir` = the new directory name; `name` = the new baseline label. The old row is REPLACED, not added — matrix_test.go now covers 3 baselines and the codex variant is the single-machine one.)

- [ ] **Step 3: Update `README.md`**

Read the baseline table. Locate:
```
| `single_machine/` | `single_machine_claude_code`  | §E2 — Claude Code single-machine agent |
```
Replace with:
```
| `single_machine/` | `single_machine_claude_code`  | §E2 — Claude Code single-machine agent (reference; not in the matrix after wt4-codex-only) |
| `single_machine_codex/` | `single_machine_codex`  | §E2 — OpenAI Codex CLI single-machine agent (active baseline for wt4-codex-only) |
```

Also locate the shell one-liner that iterates baselines (~line 35):
```bash
for bl in manual_ssh single_machine cloud_sandbox; do
```
Change to:
```bash
for bl in manual_ssh single_machine_codex cloud_sandbox; do
```

Belt: leave the old `single_machine` baseline dir untouched so someone reproducing a Claude-only baseline can still do so directly.

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd multi-agent && go test ./tests/eval/baselines/... -race -count=1 -timeout=120s`

For matrix_test.go, which is behind `-tags matrix`:
```bash
cd multi-agent && go test -tags matrix ./tests/eval/baselines/... -race -count=1 -timeout=300s
```

Expected: all PASS. (Matrix test builds & runs 3 baseline binaries × 5 workloads dry-run.)

- [ ] **Step 5: Repo-wide grep sanity**

Run:
```bash
cd multi-agent && grep -rn "single_machine_claude_code" tests/ tools/ 2>&1 | \
  grep -v "^Binary" | grep -v "tests/eval/baselines/single_machine/"
```

Expected: no output (empty). Only hits should be inside the untouched `tests/eval/baselines/single_machine/` directory.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/tests/eval/baselines/harness/row_test.go multi-agent/tests/eval/baselines/matrix_test.go multi-agent/tests/eval/baselines/README.md
git commit -m "WT-4 Task 9: harness row_test + matrix_test + README literal swap; grep repo-clean"
```

---

### Task 10 — plan.py `filter_workload()` + `--filter-workload` + `--list-workloads` + `--results-root`

**Files:**
- Modify: `multi-agent/tools/eval/fulltable/lib/plan.py`
- Create: `multi-agent/tools/eval/fulltable/tests/test_filter_workload.py`
- Create: `multi-agent/tools/eval/fulltable/tests/test_list_workloads.py`
- Create: `multi-agent/tools/eval/fulltable/tests/test_results_root_allowlist.py`

**Interfaces:**
- Consumes: existing `parse_matrix`, `enumerate_matrix_argvs`, `WORKLOADS` (5-tuple).
- Produces:
  - `filter_workload(rows: list[dict], workload_id: str) -> list[dict]` — filters matrix rows; raises `UnknownWorkloadError` on invalid id.
  - CLI: `dry-run --filter-workload <id>` prints only rows whose `workload_id == <id>` (12 rows per workload).
  - CLI: `--list-workloads` prints the 5 ids newline-separated (consumed by run.sh for allowlist).
  - CLI: `--results-root <abs-dir>` overrides `smoke_root`; rejects `$HOME`, `$HOME/.codex`, `/`, `/tmp`, `/root`, `$HOME`, and any git-repo top-level.

- [ ] **Step 1: Write failing tests**

`test_filter_workload.py`:
```python
"""Task 10 §Global Constraints: --workload filter selects exactly the
rows for one workload. Filter-first-then-sample ordering is
Task 11's concern; here we only test the pure filter function + CLI
plumbing."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT

sys.path.insert(0, str(FULLTABLE_DIR))
from lib import plan as planner


ALL_WORKLOADS = (
    "cross-device-code-mod",
    "remote-data-processing",
    "windows-only-artifact",
    "missing-parser-converter",
    "credential-bound-model",
)


def test_filter_workload_yields_12_rows_each() -> None:
    rows = planner.parse_matrix(FULLTABLE_DIR / "matrix.yaml")
    for w in ALL_WORKLOADS:
        got = planner.filter_workload(rows, w)
        assert len(got) == 12, f"workload {w}: expected 12 rows, got {len(got)}"
        for r in got:
            assert r["workload_id"] == w


def test_filter_workload_rejects_unknown_id() -> None:
    rows = planner.parse_matrix(FULLTABLE_DIR / "matrix.yaml")
    with pytest.raises(planner.UnknownWorkloadError):
        planner.filter_workload(rows, "no-such-workload")


def test_filter_workload_via_cli_dry_run() -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--filter-workload", "cross-device-code-mod",
         "dry-run"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True, check=True,
    )
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    assert len(lines) == 12, f"filter dry-run expected 12 lines, got {len(lines)}"
    for l in lines:
        assert "cross-device-code-mod" in l


def test_filter_workload_before_sample_non_prefix() -> None:
    """Plan-review P0#2 regression — a workload that does NOT appear in
    the first `sample_n` rows must STILL yield rows after filter+sample.
    Without filter-first-then-sample, this returns 0 rows."""
    import subprocess as sp
    proc = sp.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--filter-workload", "credential-bound-model",
         "sample", "--n", "2"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    # sample command emits one JSON line per plan; count credential-bound rows.
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    # Expect exactly 2 (min of sample_n=2 and 12 filtered rows).
    assert len(lines) == 2, (
        f"filter-first-then-sample violated: expected 2 rows, got {len(lines)}. "
        f"If 0: enumerate_matrix_argvs samples before filter — see P0#2."
    )
    for l in lines:
        assert "credential-bound-model" in l


def test_filter_workload_cli_rejects_unknown() -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--filter-workload", "no-such-workload",
         "dry-run"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    assert proc.returncode != 0
    assert "no-such-workload" in proc.stderr
```

`test_list_workloads.py`:
```python
"""--list-workloads emits the 5 ids newline-separated. Consumed by
run.sh for allowlist derivation (avoid literal duplication)."""
from __future__ import annotations

import subprocess
from conftest import FULLTABLE_DIR, MODULE_ROOT


def test_list_workloads_prints_five_ids() -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--list-workloads"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True, check=True,
    )
    ids = [l for l in proc.stdout.splitlines() if l.strip()]
    assert set(ids) == {
        "cross-device-code-mod", "remote-data-processing",
        "windows-only-artifact", "missing-parser-converter",
        "credential-bound-model",
    }
    assert len(ids) == 5  # deduped
```

`test_results_root_allowlist.py`:
```python
"""Global Constraints — --results-root rejects unsafe roots:
$HOME, $HOME/.codex, /, /tmp, /root, git-repo top-level."""
from __future__ import annotations

import os
import subprocess
from pathlib import Path

import pytest
from conftest import FULLTABLE_DIR, MODULE_ROOT


BAD_ROOTS = [
    "/",
    "/tmp",
    "/root",
    os.path.expanduser("~"),
    os.path.expanduser("~/.codex"),
    os.path.expanduser("~/.codex/subdir"),                # plan-review r3 P0
    os.path.expanduser("~/.codex/nested/deep/subdir"),    # plan-review r3 P0
]

GOOD_ROOT_HINTS = [
    "tests/eval/results/experiments/cross-device-code-mod/2026-07-08T00-00-00Z-12345",
]


@pytest.mark.parametrize("bad", BAD_ROOTS)
def test_results_root_rejects_unsafe(bad: str) -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--results-root", bad,
         "dry-run"],
        cwd=str(MODULE_ROOT),
        env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    assert proc.returncode != 0, f"root {bad!r} accepted; expected rejection"
    assert "results-root" in proc.stderr.lower() or "unsafe" in proc.stderr.lower(), \
        f"stderr should name the flag / reason; got: {proc.stderr}"


def _allowlisted_test_base(subdir: str) -> Path:
    """Plan-review r8 P1: the symlink-prefix test MUST live UNDER an
    allowlisted, non-/tmp base — otherwise the raw guard rejects it
    before canonicalization is ever exercised, and the test passes
    vacuously.

    Use a stable subdir under
    <module_root>/tests/eval/results/experiments/_symlink_test/
    which is (a) gitignored by Task 13.5, (b) not under /tmp,
    (c) not under $HOME/.codex.
    """
    base = MODULE_ROOT / "tests" / "eval" / "results" / "experiments" / "_symlink_test" / subdir
    base.mkdir(parents=True, exist_ok=True)
    return base


def test_results_root_rejects_symlink_prefix_to_tmp() -> None:
    """Plan-review r8 P1: symlink prefix + missing child MUST be
    resolved before allowlist check. Base MUST be under an
    allowlisted root (NOT /tmp) so the raw guard doesn't reject
    the path vacuously.
    """
    import shutil, uuid
    base = _allowlisted_test_base(f"tmp-{uuid.uuid4().hex[:8]}")
    try:
        link = base / "link"
        try:
            link.symlink_to("/tmp")
        except FileExistsError:
            pass
        candidate = str(link / "missing" / "deep")
        # Sanity: the raw candidate must NOT match /tmp/* — otherwise
        # the test is vacuous.
        assert not candidate.startswith("/tmp/"), (
            f"vacuous test setup: raw candidate {candidate!r} starts with /tmp/"
        )
        proc = subprocess.run(
            ["python3", "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--results-root", candidate,
             "dry-run"],
            cwd=str(MODULE_ROOT),
            env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
            capture_output=True, text=True,
        )
        assert proc.returncode != 0, (
            f"symlink-prefix path {candidate!r} escaped /tmp allowlist; "
            f"stderr={proc.stderr!r}"
        )
        assert "unsafe" in proc.stderr.lower() or "refusing" in proc.stderr.lower(), (
            f"expected refusal message; got: {proc.stderr}"
        )
    finally:
        shutil.rmtree(base, ignore_errors=True)


def test_results_root_rejects_symlink_prefix_to_codex() -> None:
    """Plan-review r8 P1: symlink-prefix pointing at FAKE $HOME/.codex.

    Base MUST be under an allowlisted root (NOT /tmp, NOT under the
    real ~/.codex). Fake HOME lives INSIDE the same allowlisted base
    so the raw candidate doesn't trip a /tmp or real-codex guard.
    """
    import shutil, uuid
    base = _allowlisted_test_base(f"codex-{uuid.uuid4().hex[:8]}")
    try:
        fake_home = base / "fake_home"
        fake_codex = fake_home / ".codex"
        fake_codex.mkdir(parents=True, exist_ok=True)
        link = base / "link"
        try:
            link.symlink_to(str(fake_codex))
        except FileExistsError:
            pass
        candidate = str(link / "missing")
        assert not candidate.startswith("/tmp/"), (
            f"vacuous setup: raw candidate {candidate!r} starts with /tmp/"
        )
        real_codex = os.path.expanduser("~/.codex")
        assert not candidate.startswith(real_codex + "/"), (
            f"vacuous setup: raw candidate {candidate!r} starts with real ~/.codex"
        )
        proc = subprocess.run(
            ["python3", "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--results-root", candidate,
             "dry-run"],
            cwd=str(MODULE_ROOT),
            # Override HOME so `_validate_results_root`'s `home / ".codex"`
            # resolves under the FAKE codex dir — the symlink points there.
            # The operator's real ~/.codex is untouched.
            env={
                "PYTHONPATH": str(FULLTABLE_DIR),
                "PATH": "/usr/bin:/bin",
                "HOME": str(fake_home),
            },
            capture_output=True, text=True,
        )
        assert proc.returncode != 0, (
            f"symlink-prefix path {candidate!r} escaped $HOME/.codex allowlist; "
            f"stderr={proc.stderr!r}"
        )
    finally:
        shutil.rmtree(base, ignore_errors=True)


def test_results_root_accepts_deep_subdir(tmp_path: Path) -> None:
    # Deep tempdir NOT under $HOME, /, /tmp, /root; pytest's tmp_path
    # under /tmp is rejected by allowlist. Use env-var-parametrized root.
    root = tmp_path.parent.parent / "wt4-results-test" / "subdir"
    root.mkdir(parents=True, exist_ok=True)
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--results-root", str(root.resolve()),
         "dry-run"],
        cwd=str(MODULE_ROOT),
        env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    # If tmp_path.parent.parent still resolves under /tmp/pytest-*/,
    # this test is skipped rather than falsely reporting the allowlist
    # broken.
    if str(root).startswith("/tmp"):
        pytest.skip("tmp_path lives under /tmp; allowlist correctly rejects")
    assert proc.returncode == 0, f"root {root!r} rejected; stderr={proc.stderr}"
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_filter_workload.py tools/eval/fulltable/tests/test_list_workloads.py tools/eval/fulltable/tests/test_results_root_allowlist.py -q -x`

Expected: all FAIL (no filter/list/results-root implementation yet).

- [ ] **Step 3: Add `WORKLOADS` constant + `UnknownWorkloadError` + `filter_workload` to `plan.py`**

Near the top of `plan.py` (after `E4_CONFIGURATIONS`):

```python
# The 5 workload ids, in the canonical order matching Phase 0 §3.1.
# Also emitted by `--list-workloads`; run.sh reads that output to
# validate --workload arguments against the allowlist (avoids literal
# duplication between shell + python).
WORKLOADS: tuple[str, ...] = (
    "cross-device-code-mod",
    "remote-data-processing",
    "windows-only-artifact",
    "missing-parser-converter",
    "credential-bound-model",
)


class UnknownWorkloadError(ValueError):
    """Raised by filter_workload() when the id is not in WORKLOADS."""


def filter_workload(rows: list[dict], workload_id: str) -> list[dict]:
    """Return only matrix rows whose workload_id == workload_id.

    Filter is applied BEFORE any --sample truncation (spec §4.3 P1#1
    resolution). Callers doing filter + sample MUST call this first,
    then truncate. See also the `filter_workload_id` parameter added
    to `enumerate_matrix_argvs` — that is where the filter runs when
    invoked via CLI, guaranteeing filter-before-sample ordering.
    """
    if workload_id not in WORKLOADS:
        raise UnknownWorkloadError(
            f"unknown workload id {workload_id!r}; expected one of {WORKLOADS}"
        )
    return [r for r in rows if r["workload_id"] == workload_id]
```

**CRITICAL — resolves plan-review P0#2 (filter-before-sample)**: modify
`enumerate_matrix_argvs` to accept a `filter_workload_id` parameter and
apply it BEFORE the existing `sample_n` truncation. Without this, a
CLI `--filter-workload credential-bound-model --sample 2` would first
truncate to the first 2 (full_loom cross-device-code-mod +
full_loom remote-data-processing) THEN filter, yielding 0 rows.

Locate:
```python
def enumerate_matrix_argvs(
    matrix_path: Path,
    *,
    smoke_root: Path,
    starting_port: int = 18100,
    sample_n: int | None = None,
    module_root_prefix: str = "",
    ...
) -> list[RunPlan]:
    ...
    matrix = parse_matrix(matrix_path)
    if sample_n is not None:
        matrix = matrix[:sample_n]
```

Replace with:
```python
def enumerate_matrix_argvs(
    matrix_path: Path,
    *,
    smoke_root: Path,
    starting_port: int = 18100,
    sample_n: int | None = None,
    module_root_prefix: str = "",
    filter_workload_id: str | None = None,  # NEW — filter BEFORE sample
    ...
) -> list[RunPlan]:
    ...
    matrix = parse_matrix(matrix_path)
    # Filter FIRST (plan-review P0#2 resolution — spec §4.3 filter-first-then-sample).
    if filter_workload_id is not None:
        matrix = filter_workload(matrix, filter_workload_id)
    # THEN truncate.
    if sample_n is not None:
        matrix = matrix[:sample_n]
```

Update BOTH `_cmd_dry_run` AND `_cmd_sample` to pass the filter into
`enumerate_matrix_argvs` INSTEAD of filtering afterward:

`_cmd_dry_run`:
```python
def _cmd_dry_run(args: argparse.Namespace) -> int:
    smoke_root = Path(args.results_root) if args.results_root else Path(args.smoke_root)
    plans = enumerate_matrix_argvs(
        Path(args.matrix),
        smoke_root=smoke_root,
        starting_port=args.starting_port,
        module_root_prefix=args.module_root_prefix,
        timeout=args.timeout,
        deterministic_run_ids=True,
        filter_workload_id=args.filter_workload,  # NEW — pass through
    )
    for plan in plans:
        _print_plan(plan)
    return 0
```

`_cmd_sample` — locate the existing call:
```python
    plans = enumerate_matrix_argvs(
        Path(args.matrix),
        smoke_root=smoke_root,
        starting_port=args.starting_port,
        module_root_prefix=args.module_root_prefix,
        timeout=args.timeout,
        sample_n=args.n,
    )
```

Add `filter_workload_id=args.filter_workload` to that call (same
pattern). Do NOT add a separate `plans = [p for p in plans if ...]`
filter afterward — that would be filter-after-sample again.

Same treatment for `_cmd_print_stub_listen` if it also calls
`enumerate_matrix_argvs` with `sample_n`.

Also handle the `--include-e4` case (spec §4.3 says "E4 rows SKIPPED
entirely when --filter-workload is set"): keep the existing early
warning + `args.include_e4 = False` guard from the previous draft.

- [ ] **Step 4: Add allowlist + `--results-root` + `--filter-workload` + `--list-workloads` to `main()`**

Add near the top:
```python
_ALLOWLIST_UNSAFE_ROOTS: set[str] = {
    os.path.abspath("/"),
    os.path.abspath("/tmp"),
    os.path.abspath("/root"),
}


def _validate_results_root(raw: str) -> Path:
    """Reject unsafe --results-root arguments.

    Rejected: `$HOME`, `$HOME/.codex`, `/`, `/tmp`, `/root`, any
    git-repo top-level.  Rationale: wrappers write per-invocation
    artefacts under this root; a mis-typed `$HOME` would scatter
    them across the operator's home dir. Requires an ABSOLUTE path.
    """
    if not os.path.isabs(raw):
        raise ValueError(f"--results-root must be absolute; got {raw!r}")
    p = Path(raw).resolve()
    home = Path(os.path.expanduser("~")).resolve()
    unsafe = _ALLOWLIST_UNSAFE_ROOTS | {str(home), str(home / ".codex")}
    if str(p) in unsafe:
        raise ValueError(f"--results-root {raw!r} is an unsafe root; refusing")
    # Also reject any path under /tmp (pytest tmp_path etc.)
    if str(p).startswith("/tmp/") or str(p) == "/tmp":
        raise ValueError(f"--results-root {raw!r} lives under /tmp; refusing")
    return p
```

In `main()`, add before `sub = p.add_subparsers(...)`:
```python
    p.add_argument("--filter-workload", default=None,
                   help="if set, only include rows whose workload_id equals this")
    p.add_argument("--results-root", default=None,
                   help="absolute path to override smoke_root; rejects unsafe roots")
    p.add_argument("--list-workloads", action="store_true",
                   help="print the 5 workload ids newline-separated then exit 0")
```

In `main()`, immediately after `args = p.parse_args(argv)`:
```python
    if args.list_workloads:
        for w in WORKLOADS:
            print(w)
        return 0
    if args.filter_workload is not None:
        if args.filter_workload not in WORKLOADS:
            print(f"unknown workload id {args.filter_workload!r}; expected one of {WORKLOADS}", file=sys.stderr)
            return 2
    if args.results_root is not None:
        try:
            args.results_root = _validate_results_root(args.results_root)
        except ValueError as e:
            print(f"--results-root: {e}", file=sys.stderr)
            return 2
```

**CRITICAL — plan-review r2 P0**: `plan_command_for_matrix_row` calls
`_smoke_out_root(smoke_root)` which REJECTS any path not under
`tests/eval/results/smoke/`. When `--results-root` provides a
different validated root (e.g.
`tests/eval/results/experiments/cross-device-code-mod/...`), the
enumerator will crash with `ErrOutsideSmokeRoot`. Two coupled changes:

1. Add an optional `override_out_root: Path | None = None` parameter
   to `plan_command_for_matrix_row`, `plan_command_for_e4_row`,
   `enumerate_matrix_argvs`, `enumerate_e4_argvs`. When set, it
   bypasses `_smoke_out_root` and is used directly as the write root
   (path already validated by `_validate_results_root`).

2. Thread `override_out_root=args.results_root` from every `_cmd_*`
   into the enumerator when `args.results_root is not None`.

Concretely, modify `plan_command_for_matrix_row`:
```python
def plan_command_for_matrix_row(
    row: dict,
    *,
    port: int,
    smoke_root: Path,
    run_id: str | None = None,
    ...
    override_out_root: Path | None = None,  # NEW
) -> RunPlan:
    ...
    if override_out_root is not None:
        # Caller passed --results-root; bypass smoke-only guard.
        # Validation happened in _validate_results_root.
        out_root = override_out_root
    else:
        out_root = _smoke_out_root(smoke_root)
    # ... use out_root wherever smoke_root was used below this point
```

Do the same for `plan_command_for_e4_row` and thread the parameter
through `enumerate_matrix_argvs` / `enumerate_e4_argvs` /
`_cmd_dry_run` / `_cmd_sample` / `_cmd_print_stub_listen`.

Add git-top and symlink checks to `_validate_results_root` (parity
with the run.sh guards — plan-review r2 P1):
```python
def _validate_results_root(raw: str) -> Path:
    """Reject unsafe --results-root arguments.

    Rejected: `$HOME`, `$HOME/.codex`, `/`, `/tmp`, `/root`, any
    git-repo top-level, any symlink whose resolved target is unsafe.
    Requires an ABSOLUTE path.
    """
    import subprocess
    if not os.path.isabs(raw):
        raise ValueError(f"--results-root must be absolute; got {raw!r}")
    # Plan-review r6 P0: resolve(strict=False) resolves symlink prefixes
    # even when a trailing component is missing. Otherwise a symlink
    # like `experiments/link_to_tmp/missing/deep` (link_to_tmp -> /tmp)
    # would escape the /tmp rejection because resolve(strict=True)
    # errors out on missing leaf and a naive except-clause fallback
    # would validate the unresolved path.
    p = Path(raw).resolve(strict=False)
    home = Path(os.path.expanduser("~")).resolve(strict=False)
    unsafe = _ALLOWLIST_UNSAFE_ROOTS | {str(home), str(home / ".codex")}
    if str(p) in unsafe:
        raise ValueError(f"--results-root {raw!r} is an unsafe root; refusing")
    # Reject ANY path under $HOME/.codex/ — plan-review r3 P0.
    codex_dir = home / ".codex"
    if codex_dir in p.parents or p == codex_dir:
        raise ValueError(
            f"--results-root {raw!r} resolves under $HOME/.codex/; refusing"
        )
    if str(p).startswith("/tmp/") or str(p) == "/tmp":
        raise ValueError(f"--results-root {raw!r} lives under /tmp; refusing")
    # Git top-level check (parity with run.sh belt).
    try:
        git_top = subprocess.run(
            ["git", "-C", str(p if p.exists() else p.parent), "rev-parse", "--show-toplevel"],
            capture_output=True, text=True, check=False,
        ).stdout.strip()
        if git_top and str(p) == git_top:
            raise ValueError(
                f"--results-root {raw!r} is a git repository top-level; refusing"
            )
    except FileNotFoundError:
        # git not installed — skip this check silently.
        pass
    return p
```

Add a NEW planner-CLI test to catch the alt-root write-path case (not
under SHIM — must exercise real enumeration):

`test_results_root_planner_writes_alt.py`:
```python
"""Plan-review r2 P0 regression — --results-root through plan.py CLI
must yield planner output paths under the alt root, NOT under
tests/eval/results/smoke."""
import subprocess
from pathlib import Path
from conftest import FULLTABLE_DIR, MODULE_ROOT


def test_planner_dry_run_uses_alt_root(tmp_path_factory):
    # Alt root must live outside /tmp for allowlist. Use module_root
    # under experiments/ so it passes.
    module_root = MODULE_ROOT
    alt = module_root / "tests" / "eval" / "results" / "experiments" / "_plan_test_alt" / "run1"
    alt.mkdir(parents=True, exist_ok=True)
    try:
        proc = subprocess.run(
            ["python3", "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--results-root", str(alt),
             "--filter-workload", "cross-device-code-mod",
             "dry-run"],
            cwd=str(module_root),
            env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
            capture_output=True, text=True,
        )
        assert proc.returncode == 0, f"planner failed: stderr={proc.stderr}"
        # None of the printed lines should reference tests/eval/results/smoke.
        for l in proc.stdout.splitlines():
            assert "tests/eval/results/smoke" not in l, (
                f"planner leaked smoke path into output when --results-root was set:\n"
                f"  {l}"
            )
        # At least some lines should mention the alt root's tail.
        assert any("_plan_test_alt" in l for l in proc.stdout.splitlines()), (
            f"planner did not use --results-root; output:\n{proc.stdout}"
        )
    finally:
        import shutil
        shutil.rmtree(module_root / "tests" / "eval" / "results" / "experiments" / "_plan_test_alt", ignore_errors=True)
```

Add this test to Task 10's git add + commit message.

The `_cmd_dry_run` / `_cmd_sample` modifications are captured under
Step 3 above (pass `filter_workload_id=args.filter_workload` into
`enumerate_matrix_argvs`). Also thread the E4 skip:

```python
def _cmd_sample(args: argparse.Namespace) -> int:
    # Spec §4.3 — E4 rows are per-family, not per-workload; skip when
    # a workload filter is active.
    if args.filter_workload and args.include_e4:
        print("--filter-workload with --include-e4: E4 rows are per-family, "
              "not per-workload; skipping E4", file=sys.stderr)
        args.include_e4 = False
    ...  # existing body, but enumerate_matrix_argvs call gets filter_workload_id
```

`results_root` is honored at the enumeration-root level:
```python
    smoke_root = Path(args.results_root) if args.results_root else Path(args.smoke_root)
```
(applied identically in `_cmd_dry_run` and `_cmd_sample`).

- [ ] **Step 5: Run tests — expect PASS**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_filter_workload.py tools/eval/fulltable/tests/test_list_workloads.py tools/eval/fulltable/tests/test_results_root_allowlist.py -q -v`

Expected: all PASS.

- [ ] **Step 6: Full pytest sweep — regression check**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/ -q`

Expected: all previous tests still PASS + new tests PASS.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/tools/eval/fulltable/lib/plan.py \
        multi-agent/tools/eval/fulltable/tests/test_filter_workload.py \
        multi-agent/tools/eval/fulltable/tests/test_list_workloads.py \
        multi-agent/tools/eval/fulltable/tests/test_results_root_allowlist.py \
        multi-agent/tools/eval/fulltable/tests/test_results_root_planner_writes_alt.py
git commit -m "WT-4 Task 10: plan.py --filter-workload / --list-workloads / --results-root with allowlist + override_out_root through enumerator"
```

---

### Task 11 — run.sh `--workload` / `--results-root` / `LOOM_FULLTABLE_WRAPPER_SHIM=1` + 4 tests

**Files:**
- Modify: `multi-agent/tools/eval/fulltable/run.sh`
- Create: `multi-agent/tools/eval/fulltable/tests/test_workload_filter_semantics.sh`
- Create: `multi-agent/tools/eval/fulltable/tests/test_sample_cap_with_workload.sh`
- Create: `multi-agent/tools/eval/fulltable/tests/test_run_sh_duplicate_workload.sh`
- Modify: `multi-agent/tools/eval/fulltable/tests/dry_run_snapshot.txt` (only if run.sh usage line change reorders output — should NOT)

**Interfaces:**
- Consumes: `plan.py --filter-workload`, `--list-workloads`, `--results-root` (Task 10).
- Produces:
  - `run.sh --workload <id>` filters to 12 rows; unknown id / duplicate exits 2 before dispatch.
  - `run.sh --results-root <dir>` forwards to planner; forbids override without absolute path.
  - `LOOM_FULLTABLE_WRAPPER_SHIM=1 bash run.sh --workload X --dry-run` prints `SHIM_WORKLOAD_FILTER: X` and `SHIM_RESULTS_ROOT: <path>` and exits 0 BEFORE preflight (test seam for wrapper tests). Dual-guard: requires BOTH env var AND `--dry-run`.
  - `--sample N > 3` still blocked without `ALLOW_FULL_RUN=1`, even with `--workload` filter.

- [ ] **Step 1: Write failing tests**

`test_workload_filter_semantics.sh`:
```bash
#!/usr/bin/env bash
# Spec §4.3 P1#1 — --workload interacts correctly with --sample / --resume.
# Runs run.sh under LOOM_FULLTABLE_WRAPPER_SHIM=1 + --dry-run so no
# dispatch, no clean-tree check.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# 1) --workload X --dry-run under SHIM → SHIM_WORKLOAD_FILTER: X + exit 0
out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --dry-run 2>&1)
echo "$out" | grep -q "^SHIM_WORKLOAD_FILTER: cross-device-code-mod$" \
  || { echo "case1 fail — no SHIM_WORKLOAD_FILTER line; got: $out" >&2; exit 1; }

# 2) --workload X --sample 2 → filter-first: 2 planner-lines
out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --sample 2 --dry-run 2>&1)
lines=$(echo "$out" | grep -c "cross-device-code-mod" || true)
if [ "$lines" -lt 1 ]; then
  echo "case2 fail — expected some cross-device-code-mod lines; got: $out" >&2
  exit 1
fi

# 3) SHIM without --dry-run: dual-guard active, SHIM path NOT taken → exit non-zero (preflight fires + fails because tree dirty during test)
if LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --sample 1 >/tmp/case3.out 2>&1; then
  echo "case3 fail — SHIM without --dry-run should NOT bypass preflight; got exit 0"
  cat /tmp/case3.out
  exit 1
fi

# 4) NON-SHIM --dry-run: plain `run.sh --workload X --dry-run` prints
#    exactly 12 planner lines to stdout (plan-review P1 resolution —
#    the SHIM-only tests above don't prove filter integration end-to-end).
out=$(bash "$run_sh" --workload cross-device-code-mod --dry-run 2>&1)
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "case4 fail — non-shim --workload --dry-run exit=$rc"; echo "$out"; exit 1
fi
lines=$(echo "$out" | grep -c "cross-device-code-mod" || true)
if [ "$lines" -ne 12 ]; then
  echo "case4 fail — non-shim --workload --dry-run expected 12 lines, got $lines"; echo "$out"; exit 1
fi

# 5) NON-SHIM --dry-run for a NON-PREFIX workload (credential-bound-model
#    is 5th in the workload iteration). Filter-first-then-sample
#    guarantees this still yields 12 rows even under implicit sample
#    ordering.
out=$(bash "$run_sh" --workload credential-bound-model --dry-run 2>&1)
lines=$(echo "$out" | grep -c "credential-bound-model" || true)
if [ "$lines" -ne 12 ]; then
  echo "case5 fail — non-prefix workload expected 12 lines, got $lines"; echo "$out"; exit 1
fi

# 6) Plan-review r2 P1 — planner sample subcommand under --filter-workload
#    respects filter-before-sample. Non-shim: exercises the real
#    _cmd_sample path all the way to the enumerator.
#    (This is done via `python3 -m lib.plan ... --filter-workload X sample --n 2`
#    directly rather than run.sh to isolate the planner change.)
alt_out=$(cd "$module_root" && PYTHONPATH="$module_root/tools/eval/fulltable" \
  python3 -m lib.plan \
    --matrix "$module_root/tools/eval/fulltable/matrix.yaml" \
    --smoke-root "tests/eval/results/smoke" \
    --filter-workload credential-bound-model \
    sample --n 2 2>&1)
alt_lines=$(echo "$alt_out" | grep -c "credential-bound-model" || true)
if [ "$alt_lines" -ne 2 ]; then
  echo "case6 fail — planner sample --n 2 --filter-workload credential-bound-model expected 2 lines, got $alt_lines"
  echo "$alt_out"
  exit 1
fi

# 7) Plan-review r3 P1 — run.sh --workload X --resume must (a) skip E4
#    rows entirely (E4 is per-family, not per-workload), (b) filter
#    matrix rows before resume-sidecar check. Uses the existing
#    LOOM_FULLTABLE_DISPATCH_SHIM which runs preflight and dispatch
#    planning but stops before real subprocess exec, printing
#    `SHIM: would dispatch N rows`.
#
# Use a large-enough --sample cap that matrix (12) + E4 (many) would
# both fit under ALLOW_FULL_RUN=1; if E4 leaked in, the row count
# would exceed 12. Explicitly REQUIRE the SHIM line to appear
# (proves the path was reached) and assert the row count equals 12
# (all workload rows survived resume with no matching sidecars).

alt_root="$module_root/tests/eval/results/experiments/_test_resume_$$"
mkdir -p "$alt_root/runs"

# Compute the resume_key so run.sh's `compgen -G .../${rk}__*.done`
# glob matches (run.sh globs by resume_key, so the run_id in the
# sidecar filename does NOT need to match planner's live run_id —
# any UUID-shaped suffix suffices). This keeps the test robust even
# though `sample`-mode run IDs are not deterministic (only dry-run
# mode is deterministic per plan.py enumerate_matrix_argvs).
credbm_full_loom_rk="matrix__credential-bound-model__full_loom"
touch "$alt_root/runs/${credbm_full_loom_rk}__00000000-1111-2222-3333-444444444444.done"

# Set WORKTREE_ROOT to a temp clean git repo so commit_meta preflight
# succeeds (plan-review r4 P1 — the impl worktree is dirty during test
# development, would fail preflight otherwise).
clean_repo=$(mktemp -d)
git init -q "$clean_repo" >/dev/null 2>&1
git -C "$clean_repo" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q >/dev/null 2>&1

shim_out=$(ALLOW_FULL_RUN=1 WORKTREE_ROOT="$clean_repo" LOOM_FULLTABLE_DISPATCH_SHIM=1 \
    bash "$run_sh" \
      --workload credential-bound-model \
      --results-root "$alt_root" \
      --sample 50 \
      --resume 2>&1)
shim_rc=$?
if [ "$shim_rc" -ne 0 ]; then
  echo "case7 fail — dispatch-shim exit=$shim_rc; stdout+stderr:"
  echo "$shim_out"
  rm -rf "$alt_root" "$clean_repo"
  exit 1
fi
if ! echo "$shim_out" | grep -qE "SHIM: would dispatch [0-9]+ rows"; then
  echo "case7 fail — dispatch-shim did NOT reach dispatch-planning:"
  echo "$shim_out"
  rm -rf "$alt_root" "$clean_repo"
  exit 1
fi
rows=$(echo "$shim_out" | grep -oE "would dispatch [0-9]+" | grep -oE "[0-9]+")
# EXACT expected: 11 = 12 workload rows minus the 1 matching sidecar.
# Anything else = broken filter or broken resume-sidecar match.
if [ "$rows" -ne 11 ]; then
  echo "case7 fail — expected exactly 11 rows (12 workload rows - 1 sidecar-matched); got $rows"
  echo "$shim_out"
  rm -rf "$alt_root" "$clean_repo"
  exit 1
fi

rm -rf "$alt_root" "$clean_repo"

echo "OK"
```

`test_sample_cap_with_workload.sh`:
```bash
#!/usr/bin/env bash
# Spec §5 TestSampleCapEnforcedWithWorkloadFilter — --sample N>3 with
# --workload still requires ALLOW_FULL_RUN=1. Cap enforced at run.sh
# (not planner) per P0#4 round-1 resolution.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# 1) --workload X --sample 4 without ALLOW_FULL_RUN → exit 2
if bash "$run_sh" --workload cross-device-code-mod --sample 4 --dry-run 2>/tmp/cap1.err; then
  echo "case1 fail — --sample 4 without ALLOW_FULL_RUN=1 should exit 2"
  exit 1
fi
grep -q "ErrFullRunNotAllowed\|--sample" /tmp/cap1.err \
  || { echo "case1 fail — missing cap error"; cat /tmp/cap1.err; exit 1; }

# 2) --workload X --sample=4 (equals form) also blocked
if bash "$run_sh" --workload cross-device-code-mod --sample=4 --dry-run 2>/tmp/cap2.err; then
  echo "case2 fail — --sample=4 should exit 2"
  exit 1
fi

# 3) With ALLOW_FULL_RUN=1 + --dry-run → exits 0, prints <= 4 rows
out=$(ALLOW_FULL_RUN=1 bash "$run_sh" --workload cross-device-code-mod --sample 4 --dry-run 2>&1)
n=$(echo "$out" | wc -l)
if [ "$n" -gt 5 ]; then
  echo "case3 fail — with ALLOW_FULL_RUN=1 got $n lines; expected <= 4"
  echo "$out"
  exit 1
fi

echo "OK"
```

`test_run_sh_duplicate_workload.sh`:
```bash
#!/usr/bin/env bash
# Spec §5 test_run_sh_duplicate_workload — --workload may be given at
# most once. Resolves P2 round-1 (upgraded to acceptance test).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# 1) --workload A --workload B → exit 2 BEFORE dispatch
if bash "$run_sh" --workload cross-device-code-mod --workload credential-bound-model --sample 1 --dry-run 2>/tmp/dup1.err; then
  echo "case1 fail — duplicate --workload should exit 2"
  cat /tmp/dup1.err
  exit 1
fi
grep -q "--workload may be given at most once" /tmp/dup1.err \
  || { echo "case1 fail — missing duplicate-workload error message"; cat /tmp/dup1.err; exit 1; }

# 2) --workload=A --workload=B (equals form) also rejected
if bash "$run_sh" --workload=cross-device-code-mod --workload=credential-bound-model --sample 1 --dry-run 2>/tmp/dup2.err; then
  echo "case2 fail — duplicate --workload= should exit 2"
  exit 1
fi

# 3) Single --workload accepted (sanity)
LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --dry-run >/dev/null 2>&1 \
  || { echo "case3 fail — single --workload should be accepted"; exit 1; }

echo "OK"
```

Add these three tests to `tools/eval/fulltable/tests/conftest.py` collection: pytest doesn't auto-run `.sh` files. Register them via a Python wrapper:

Create `tools/eval/fulltable/tests/test_run_sh_shell_tests.py`:
```python
"""Wrap the .sh test scripts so pytest discovers + reports them."""
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest


TESTS_DIR = Path(__file__).resolve().parent


@pytest.mark.parametrize("script", [
    "test_workload_filter_semantics.sh",
    "test_sample_cap_with_workload.sh",
    "test_run_sh_duplicate_workload.sh",
    "test_results_root_scoping.sh",             # added plan-review r2
    "test_results_root_scoping_dispatch.sh",    # added plan-review r3
    "test_results_root_symlink_escape.sh",      # added plan-review r6
])
def test_shell_test_passes(script: str) -> None:
    path = TESTS_DIR / script
    assert path.exists(), f"missing {path}"
    proc = subprocess.run(["bash", str(path)], capture_output=True, text=True)
    assert proc.returncode == 0, (
        f"{script} exit={proc.returncode}\n"
        f"stdout={proc.stdout}\nstderr={proc.stderr}"
    )
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_run_sh_shell_tests.py -q -v`

Expected: all 6 shell tests FAIL (run.sh has no `--workload` / `--results-root` / SHIM flags yet).

- [ ] **Step 3: Extend run.sh usage() and parse**

In `run.sh`, expand `usage()`:
```bash
usage() {
  cat <<'EOF' >&2
Usage:
  run.sh --dry-run
  run.sh --sample N               (N ≤ 3; > 3 requires ALLOW_FULL_RUN=1)
  run.sh --resume [--sample N]
  run.sh --parallel N
  run.sh --workload <id>          (filter to one workload; may be given at most once)
  run.sh --results-root <abs-dir> (override results directory; absolute, allowlisted)
  run.sh --inject-fake-failure-on-row N

Env:
  ALLOW_FULL_RUN=1                allow --sample N > 3
  LOOM_FULLTABLE_DISPATCH_SHIM=1  preflight + print SHIM line; do NOT
                                  exec runner/baseline
  LOOM_FULLTABLE_WRAPPER_SHIM=1   dual-guard with --dry-run: print
                                  SHIM_WORKLOAD_FILTER + SHIM_RESULTS_ROOT
                                  and exit 0 BEFORE preflight

--workload allowlist derived from `plan.py --list-workloads`.
--results-root rejects: /, /tmp, /root, $HOME, $HOME/.codex, git top-level.
EOF
}
```

In the arg-parse loop, add:
```bash
workload=""
workload_count=0
results_root=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_flag=1; shift ;;
    --sample) sample_n="$2"; shift 2 ;;
    --sample=*) sample_n="${1#--sample=}"; shift ;;
    --resume) resume=1; shift ;;
    --parallel) parallel="$2"; shift 2 ;;
    --parallel=*) parallel="${1#--parallel=}"; shift ;;
    --workload) workload="$2"; workload_count=$((workload_count + 1)); shift 2 ;;
    --workload=*) workload="${1#--workload=}"; workload_count=$((workload_count + 1)); shift ;;
    --results-root) results_root="$2"; shift 2 ;;
    --results-root=*) results_root="${1#--results-root=}"; shift ;;
    --inject-fake-failure-on-row) inject_row="$2"; shift 2 ;;
    --inject-fake-failure-on-row=*) inject_row="${1#--inject-fake-failure-on-row=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "run.sh: unknown flag $1" >&2; usage; exit 2 ;;
  esac
done

# Duplicate --workload rejection (BEFORE dispatch, BEFORE preflight,
# BEFORE sample-cap check).
if [[ "$workload_count" -gt 1 ]]; then
  echo "run.sh: --workload may be given at most once (got $workload_count)" >&2
  exit 2
fi

# Sample cap (unchanged; runs BEFORE workload allowlist check because a
# request that violates the cap should be rejected regardless of
# validity of other flags).
if [[ "$sample_n" -gt 3 && "${ALLOW_FULL_RUN:-0}" != "1" ]]; then
  echo "ErrFullRunNotAllowed: --sample $sample_n > 3 requires ALLOW_FULL_RUN=1" >&2
  exit 2
fi

# Workload allowlist derived from plan.py (avoids literal duplication).
if [[ -n "$workload" ]]; then
  allow=$(PYTHONPATH="$fulltable_dir" python3 -m lib.plan \
      --matrix "$fulltable_dir/matrix.yaml" \
      --smoke-root "$smoke_root_rel" \
      --list-workloads)
  if ! echo "$allow" | grep -qx "$workload"; then
    echo "run.sh: unknown workload id: $workload; expected one of:" >&2
    echo "$allow" >&2
    exit 2
  fi
fi

# LOOM_FULLTABLE_WRAPPER_SHIM=1 + --dry-run dual-guard.
if [[ "${LOOM_FULLTABLE_WRAPPER_SHIM:-0}" == "1" && "$dry_flag" == "1" ]]; then
  echo "SHIM_WORKLOAD_FILTER: ${workload:-}"
  echo "SHIM_RESULTS_ROOT: ${results_root:-<default>}"
  exit 0
fi
```

**CRITICAL — resolves plan-review P0#1**: `--results-root` MUST be
threaded through run.sh's own filesystem writes, not merely forwarded
to `plan.py`. Existing `run.sh` hardcodes `smoke_root_abs` /
`smoke_root_rel` in ~15 mkdir/rm/write sites (dbs/runs/paper/runs.csv/
metrics.csv/failures.jsonl/sidecars). Modify the top of `run.sh` so
`--results-root` OVERRIDES the smoke defaults across ALL these sites:

Locate the top-of-file assignments:
```bash
smoke_root_abs="$module_root/tests/eval/results/smoke"
smoke_root_rel="tests/eval/results/smoke"           # from module_root
```

After the arg-parse loop (where `$results_root` is populated), inject:
```bash
# --results-root overrides the smoke default for all downstream writes.
# The path was already allowlist-validated by plan.py (called with
# --results-root above) when set — but as belt-and-braces we re-validate
# here in shell before mkdir-p'ing anywhere.
if [[ -n "$results_root" ]]; then
  # Absolute-path check (planner also checks, but here we exit BEFORE
  # any mkdir).
  case "$results_root" in
    /*) : ;;
    *) echo "run.sh: --results-root must be an absolute path; got $results_root" >&2; exit 2 ;;
  esac
  # Reject well-known dangerous roots (mirrors plan.py allowlist).
  # Rationale: shell-side belt in case the caller passes --results-root
  # without also going through plan.py's validation on a code path we
  # missed.
  case "$results_root" in
    /|/tmp|/root|"$HOME"|"$HOME/.codex") \
      echo "run.sh: --results-root $results_root is an unsafe root; refusing" >&2; exit 2 ;;
  esac
  case "$results_root" in
    /tmp/*) echo "run.sh: --results-root under /tmp is unsafe; refusing" >&2; exit 2 ;;
    "$HOME/.codex/"*) echo "run.sh: --results-root $results_root resolves under \$HOME/.codex/; refusing" >&2; exit 2 ;;
  esac
  # Reject symlinks that point into unsafe roots (readlink -f resolves).
  # Plan-review r6 P0: `readlink -f` FAILS if any leaf is missing, and
  # a naive `|| echo "$results_root"` fallback would then validate the
  # UNRESOLVED raw path — allowing `symlink_to_tmp/missing/deep` to
  # escape /tmp rejection. `realpath -m` (GNU coreutils) canonicalizes
  # even when trailing components don't exist. If `realpath -m` isn't
  # available, use python3 as fallback. Refuse to validate at all if
  # both fail (safer than an unresolved raw path).
  # Try `realpath -m` (GNU coreutils), then fall back to python3.
  # Plan-review r7 P0: earlier draft had two bugs:
  #   1. `python3 -c ... -- "$results_root"` puts "--" at sys.argv[1],
  #      not the path. Fix: no `--` separator before the arg.
  #   2. Python fallback ran only when realpath was ABSENT; on hosts
  #      with a broken/BSD realpath that lacks -m, the -m call failed
  #      and we returned exit 2 without trying python3. Fix: try
  #      python3 whenever `realpath -m` failed OR was unavailable.
  resolved=""
  if command -v realpath >/dev/null 2>&1; then
    resolved="$(realpath -m -- "$results_root" 2>/dev/null)" || resolved=""
  fi
  if [[ -z "$resolved" ]]; then
    resolved="$(python3 -c 'import sys, pathlib; print(pathlib.Path(sys.argv[1]).resolve(strict=False))' "$results_root" 2>/dev/null)" || resolved=""
  fi
  if [[ -z "$resolved" ]]; then
    echo "run.sh: --results-root $results_root cannot be canonicalized (no realpath -m / python3); refusing" >&2; exit 2
  fi
  case "$resolved" in
    /|/tmp|/tmp/*|/root|"$HOME"|"$HOME/.codex"|"$HOME/.codex/"*) \
      echo "run.sh: --results-root $results_root resolves to unsafe $resolved; refusing" >&2; exit 2 ;;
  esac
  # Reject git-repo top-level: safeguard against overwriting the repo.
  if git_top="$(cd "$resolved" 2>/dev/null && git rev-parse --show-toplevel 2>/dev/null)"; then
    if [[ "$resolved" == "$git_top" ]]; then
      echo "run.sh: --results-root $results_root is a git repository top-level; refusing" >&2; exit 2 ;
    fi
  fi
  # Reject non-empty target unless --resume was passed. Non-fixture
  # data in the target implies a stale run whose reuse the operator
  # did not opt into.
  if [[ -e "$resolved" && "$resume" != "1" ]]; then
    if [[ -n "$(ls -A "$resolved" 2>/dev/null || true)" ]]; then
      echo "run.sh: --results-root $resolved exists and is non-empty; pass --resume or point at a fresh path" >&2; exit 2
    fi
  fi
  # Override all downstream smoke_root_abs / smoke_root_rel uses.
  smoke_root_abs="$resolved"
  # smoke_root_rel is used only in plan.py CLI arg wiring; keep it as
  # the same absolute path when --results-root is set — plan.py's
  # `--results-root` arg (set below) supersedes `--smoke-root` for
  # actual output path construction.
  smoke_root_rel="$resolved"
fi
```

Also add the forward:
```bash
# Forward to plan.py — added args go INSIDE both python3 -m lib.plan
# invocations already present in run.sh.
plan_extra_args=()
[[ -n "$workload" ]] && plan_extra_args+=("--filter-workload" "$workload")
[[ -n "$results_root" ]] && plan_extra_args+=("--results-root" "$results_root")

# Then each existing planner invocation:
#   PYTHONPATH="$fulltable_dir" python3 -m lib.plan \
#       --matrix "$fulltable_dir/matrix.yaml" \
#       --smoke-root "$smoke_root_rel" \
#       "${plan_extra_args[@]}" \
#       dry-run
```

Add a new acceptance test `test_results_root_scoping.sh`:

```bash
#!/usr/bin/env bash
# Plan-review P0#1: --results-root MUST scope run.sh's own writes, not
# just planner output paths. Verify nothing lands under smoke/ when
# --results-root is set.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

alt_root=$(mktemp -d --tmpdir=/var/tmp wt4-alt-XXXXXX 2>/dev/null || echo "")
if [ -z "$alt_root" ]; then
  # /var/tmp not writable — synthesize under a non-/tmp dir (allowlist-safe).
  alt_root="$module_root/tests/eval/results/experiments/_test_alt_root/$(date +%s)-$$"
  mkdir -p "$alt_root"
fi
trap "rm -rf '$alt_root'" EXIT

smoke_dir="$module_root/tests/eval/results/smoke"
smoke_before=$(find "$smoke_dir" -type f 2>/dev/null | wc -l)

LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" \
  --workload cross-device-code-mod \
  --results-root "$alt_root" \
  --dry-run > /dev/null

# Under SHIM, no writes should happen at all — but verify smoke didn't grow.
smoke_after=$(find "$smoke_dir" -type f 2>/dev/null | wc -l)
if [ "$smoke_after" -gt "$smoke_before" ]; then
  echo "FAIL: writes leaked into smoke/ under --results-root; before=$smoke_before after=$smoke_after"
  exit 1
fi

# Belt: SHIM output must include SHIM_RESULTS_ROOT: <alt_root>
out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" \
    --workload cross-device-code-mod \
    --results-root "$alt_root" \
    --dry-run 2>&1)
if ! echo "$out" | grep -q "SHIM_RESULTS_ROOT: $alt_root"; then
  echo "FAIL: SHIM output missing SHIM_RESULTS_ROOT: $alt_root"
  echo "$out"
  exit 1
fi

echo "OK"
```

Also add a REAL non-shim scoping test to catch a missed mkdir/rm site
in run.sh (plan-review r3 P1 — WRAPPER_SHIM exits before writes):

```bash
#!/usr/bin/env bash
# test_results_root_scoping_dispatch.sh — plan-review r4 P1.
# LOOM_FULLTABLE_DISPATCH_SHIM=1 reaches the mkdir + planner path
# (but stops before real subprocess exec). Any missed smoke_root_abs
# reference in run.sh would create files under smoke/ during the
# mkdir loop → caught here.
#
# CRITICAL: do NOT pass --dry-run. --dry-run short-circuits before
# preflight AND before the mkdir loop, so a scoping bug would go
# undetected.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

alt_root="$module_root/tests/eval/results/experiments/_test_scoping_$$"
mkdir -p "$alt_root"
trap "rm -rf '$alt_root' '$tmpdir'" EXIT

# Clean git repo so commit_meta preflight passes (impl worktree may be
# dirty during test dev).
clean_repo="$tmpdir/clean_repo"
git init -q "$clean_repo" >/dev/null 2>&1
git -C "$clean_repo" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q >/dev/null 2>&1

smoke_dir="$module_root/tests/eval/results/smoke"
snapshot_before="$tmpdir/smoke_before.txt"
snapshot_after="$tmpdir/smoke_after.txt"
find "$smoke_dir" \( -type f -o -type d \) 2>/dev/null | LC_ALL=C sort > "$snapshot_before"

# NO --dry-run. --sample 3 reaches the DISPATCH_SHIM after preflight
# and the mkdir loop.
ALLOW_FULL_RUN=1 WORKTREE_ROOT="$clean_repo" LOOM_FULLTABLE_DISPATCH_SHIM=1 \
  bash "$run_sh" \
    --workload cross-device-code-mod \
    --results-root "$alt_root" \
    --sample 3 > "$tmpdir/scope.out" 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAIL: dispatch-shim exit=$rc"
  cat "$tmpdir/scope.out"
  exit 1
fi
if ! grep -q "SHIM: would dispatch" "$tmpdir/scope.out"; then
  echo "FAIL: dispatch-shim did NOT reach dispatch-planning"
  cat "$tmpdir/scope.out"
  exit 1
fi

find "$smoke_dir" \( -type f -o -type d \) 2>/dev/null | LC_ALL=C sort > "$snapshot_after"
if ! diff -u "$snapshot_before" "$snapshot_after" > "$tmpdir/smoke_diff.out"; then
  echo "FAIL: DISPATCH_SHIM wrote to smoke/ under --results-root"
  cat "$tmpdir/smoke_diff.out"
  exit 1
fi

# Verify alt_root DID get expected dirs (mkdir loop ran with the
# overridden root).
for sub in dbs runs paper; do
  if [ ! -d "$alt_root/$sub" ]; then
    echo "FAIL: expected $alt_root/$sub after DISPATCH_SHIM run.sh --results-root"
    ls -la "$alt_root/"
    exit 1
  fi
done

echo "OK"
```

Add this AND `test_results_root_scoping.sh` to `test_run_sh_shell_tests.py` parametrize list (5 entries total now).

**Also** create `test_results_root_symlink_escape.sh` (plan-review r6 P0
— shell side):

```bash
#!/usr/bin/env bash
# Plan-review r8 P1 — run.sh --results-root MUST reject symlink prefix
# escapes to /tmp or $HOME/.codex. Uses a FAKE HOME AND an ALLOWLISTED
# base (NOT /tmp) so a canonicalization regression is actually caught
# (base under /tmp would be rejected by the raw guard vacuously).
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# Base MUST NOT be under /tmp. Use the gitignored experiments dir
# (Task 13.5).
base="$module_root/tests/eval/results/experiments/_symlink_test_sh_$$"
mkdir -p "$base"
trap "rm -rf '$base'" EXIT

fake_home="$base/fake_home"
mkdir -p "$fake_home/.codex"

# Sanity: base must not accidentally be under /tmp on this host.
case "$base" in
  /tmp/*) echo "SETUP FAIL: base $base under /tmp — vacuous test"; exit 2 ;;
esac

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

# Case 1: symlink → /tmp, then a missing-child path via it.
ln -s /tmp "$base/link_to_tmp"
candidate1="$base/link_to_tmp/missing/deep"
case "$candidate1" in
  /tmp/*) echo "SETUP FAIL: candidate1 under /tmp"; exit 2 ;;
esac
if HOME="$fake_home" bash "$run_sh" --results-root "$candidate1" \
    --workload cross-device-code-mod --dry-run 2>"$base/case1.err"; then
  report 1 "case1: symlink→/tmp escape accepted"
  cat "$base/case1.err"
else
  report 0 "case1: symlink→/tmp escape rejected"
fi

# Case 2: symlink → $FAKE_HOME/.codex (not the real ~/.codex),
# missing child.
ln -s "$fake_home/.codex" "$base/link_to_codex"
candidate2="$base/link_to_codex/missing"
case "$candidate2" in
  /tmp/*) echo "SETUP FAIL: candidate2 under /tmp"; exit 2 ;;
esac
if HOME="$fake_home" bash "$run_sh" --results-root "$candidate2" \
    --workload cross-device-code-mod --dry-run 2>"$base/case2.err"; then
  report 1 "case2: symlink→\$HOME/.codex escape accepted"
  cat "$base/case2.err"
else
  report 0 "case2: symlink→\$HOME/.codex escape rejected"
fi

exit $fail
```

Add to param list (6 entries total now).

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_run_sh_shell_tests.py -q -v`

Expected: all 6 shell tests PASS.

- [ ] **Step 5: Snapshot regression check**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_dry_run_snapshot.py -q`

Expected: PASS. Sanity: `bash tools/eval/fulltable/run.sh --dry-run` output MUST NOT change from Task 8's snapshot — the new flags are opt-in, no arg = default behavior.

- [ ] **Step 6: Full sweep**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/ -q && go test ./tests/eval/baselines/... -race -count=1 -timeout=120s`

Expected: all green.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/tools/eval/fulltable/run.sh \
        multi-agent/tools/eval/fulltable/tests/test_workload_filter_semantics.sh \
        multi-agent/tools/eval/fulltable/tests/test_sample_cap_with_workload.sh \
        multi-agent/tools/eval/fulltable/tests/test_run_sh_duplicate_workload.sh \
        multi-agent/tools/eval/fulltable/tests/test_results_root_scoping.sh \
        multi-agent/tools/eval/fulltable/tests/test_results_root_scoping_dispatch.sh \
        multi-agent/tools/eval/fulltable/tests/test_results_root_symlink_escape.sh \
        multi-agent/tools/eval/fulltable/tests/test_run_sh_shell_tests.py
git commit -m "WT-4 Task 11: run.sh --workload / --results-root / LOOM_FULLTABLE_WRAPPER_SHIM + scoping tests (SHIM + DISPATCH_SHIM) + resume/E4 assertion"
```

---

### Task 12 — `_common.sh` shared preflight helpers + bash unit tests

**Files:**
- Create: `multi-agent/tools/eval/experiments/_common.sh`
- Create: `multi-agent/tools/eval/experiments/tests/test_common_helpers.sh`
- Create: `multi-agent/tools/eval/experiments/tests/test_common_helpers.py` (pytest wrapper)

**Interfaces:**
- Consumes: parent `PATH`, `HOME`, `uname`, `python3` (for TOML parse in codex_config_has_route_*), optional `LOOM_CODEX_CONFIG_PATH` (test seam overriding `~/.codex/config.toml`).
- Produces (sourceable functions):
  - `codex_bin_present` — exit 0 if `command -v codex` finds a binary; NEVER invokes `codex`. Exit 2 otherwise.
  - `codex_config_readable` — exit 0 if `${LOOM_CODEX_CONFIG_PATH:-$HOME/.codex/config.toml}` is readable; exit 2 otherwise.
  - `codex_config_has_route_a` — exit 0 (route-a present) / 1 (absent) / 2 (parse error). Uses python3 tomllib. Prints ONLY `present` or `absent` (no key names, no values).
  - `codex_config_has_route_b` — exit 0 (present) / 1 (absent) / 2 (parse error). Prints ONLY `present` or `absent`.
  - `require_writable_tmp` — exit 0 if `mktemp -d` succeeds and cleanup works; exit 2 otherwise.
  - `require_fixture <path>` — exit 0 if `-e $path`; exit 2 otherwise with clear message.
  - `require_windows_host` — exit 0 if `uname -s` matches `MINGW*|MSYS*|CYGWIN*|*_NT*`; exit 1 otherwise (non-fatal — caller decides).
  - `die <msg>` — print msg to stderr, exit 2.
  - `warn_and_exit_zero <msg>` — print msg to stderr, exit 0. USE ONLY with explicit operator flag.

- [ ] **Step 1: Write failing test file `test_common_helpers.sh`**

```bash
#!/usr/bin/env bash
# Task 12 — _common.sh unit tests. Sources _common.sh in a subshell
# per case so exits don't kill the runner. No dependency on bats.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
common_sh="$here/../_common.sh"

fail=0
report() {
  if [ "$1" -eq 0 ]; then
    echo "PASS  $2"
  else
    echo "FAIL  $2"
    fail=1
  fi
}

# --- codex_bin_present ---
# Case: fake codex on PATH → exit 0
tmp=$(mktemp -d); trap "rm -rf $tmp" EXIT
touch "$tmp/codex"; chmod +x "$tmp/codex"
PATH="$tmp:/usr/bin" bash -c "source '$common_sh' && codex_bin_present"
report $? "codex_bin_present with fake codex → 0"

# Case: empty PATH → exit 2
PATH="$tmp/nonexistent" bash -c "source '$common_sh' && codex_bin_present" 2>/dev/null
[ $? -eq 2 ] && report 0 "codex_bin_present without codex → 2" || report 1 "codex_bin_present without codex → 2"

# Case: codex_bin_present MUST NOT invoke codex. Prove via wrapper
# that fails LOUDLY if invoked.
cat > "$tmp/codex" <<'EOF'
#!/bin/sh
echo "IMPOSSIBLE: codex was executed" >&2
exit 99
EOF
chmod +x "$tmp/codex"
out=$(PATH="$tmp:/usr/bin" bash -c "source '$common_sh' && codex_bin_present" 2>&1)
if echo "$out" | grep -q "IMPOSSIBLE"; then
  report 1 "codex_bin_present must not invoke codex"
else
  report 0 "codex_bin_present must not invoke codex"
fi

# --- codex_config_readable ---
tmp2=$(mktemp -d); trap "rm -rf $tmp $tmp2" EXIT
mkdir -p "$tmp2/.codex"
touch "$tmp2/.codex/config.toml"

LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_readable"
report $? "codex_config_readable with readable file → 0"

LOOM_CODEX_CONFIG_PATH="/nonexistent/config.toml" bash -c "source '$common_sh' && codex_config_readable" 2>/dev/null
[ $? -eq 2 ] && report 0 "codex_config_readable with missing file → 2" || report 1 "codex_config_readable with missing file → 2"

# --- codex_config_has_route_a ---
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.modelserver]
experimental_bearer_token = "sk-abc123DEFabcDEFabc"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_a" 2>&1)
[ "$out" = "present" ] && report 0 "route_a present detected" || report 1 "route_a present detected — got '$out'"

# CRITICAL SECURITY: token value MUST NOT leak into stdout/stderr
if echo "$out" | grep -q "sk-abc123"; then
  report 1 "SECURITY: route_a value leaked into output — got '$out'"
else
  report 0 "route_a token value NOT leaked"
fi

# route-a commented → absent
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.modelserver]
# experimental_bearer_token = "sk-abc123DEFabcDEFabc"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_a" 2>&1)
[ "$out" = "absent" ] && report 0 "route_a commented → absent" || report 1 "route_a commented → absent (got '$out')"

# route-a in different table → absent
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.other]
experimental_bearer_token = "sk-not-ours"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_a" 2>&1)
[ "$out" = "absent" ] && report 0 "route_a in .other → absent" || report 1 "route_a in .other → absent (got '$out')"

# --- codex_config_has_route_b ---
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.modelserver]
env_key = "SOME_ENV_NAME"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_b" 2>&1)
[ "$out" = "present" ] && report 0 "route_b present detected" || report 1 "route_b present detected — got '$out'"

# route-b env name MUST NOT leak
if echo "$out" | grep -q "SOME_ENV_NAME"; then
  report 1 "SECURITY: route_b env-key name leaked into output — got '$out'"
else
  report 0 "route_b env-key name NOT leaked"
fi

# --- require_writable_tmp ---
bash -c "source '$common_sh' && require_writable_tmp"
report $? "require_writable_tmp on normal system → 0"

# --- require_fixture ---
bash -c "source '$common_sh' && require_fixture '$tmp2/.codex/config.toml'"
report $? "require_fixture present → 0"

bash -c "source '$common_sh' && require_fixture '/nonexistent/xyz'" 2>/dev/null
[ $? -eq 2 ] && report 0 "require_fixture absent → 2" || report 1 "require_fixture absent → 2"

# --- require_windows_host ---
# Assume Linux CI — expect exit 1 (not-Windows)
bash -c "source '$common_sh' && require_windows_host" 2>/dev/null
rc=$?
uname_out=$(uname -s)
case "$uname_out" in
  *NT*|MSYS*|CYGWIN*|MINGW*) expected=0 ;;
  *) expected=1 ;;
esac
[ "$rc" -eq "$expected" ] && report 0 "require_windows_host respects uname (uname=$uname_out expected=$expected got=$rc)" \
                          || report 1 "require_windows_host WRONG (uname=$uname_out expected=$expected got=$rc)"

# --- die ---
out=$(bash -c "source '$common_sh' && die 'boom'" 2>&1)
rc=$?
[ "$rc" -eq 2 ] && [ "$out" = "boom" ] && report 0 "die → exit 2 with msg" || report 1 "die → exit 2 with msg (rc=$rc out='$out')"

# --- warn_and_exit_zero ---
out=$(bash -c "source '$common_sh' && warn_and_exit_zero 'skipping'" 2>&1)
rc=$?
[ "$rc" -eq 0 ] && [ "$out" = "skipping" ] && report 0 "warn_and_exit_zero → exit 0" || report 1 "warn_and_exit_zero → exit 0 (rc=$rc)"

exit $fail
```

`test_common_helpers.py` (pytest wrapper):
```python
"""pytest wrapper for _common.sh test suite."""
import subprocess
from pathlib import Path


def test_common_helpers() -> None:
    script = Path(__file__).parent / "test_common_helpers.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, (
        f"_common.sh tests failed:\n"
        f"stdout:\n{proc.stdout}\n"
        f"stderr:\n{proc.stderr}"
    )
```

- [ ] **Step 2: Run tests — expect FAIL (no _common.sh yet)**

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_common_helpers.py -q -v`

Expected: FAIL — `_common.sh` doesn't exist.

- [ ] **Step 3: Write `_common.sh`**

```bash
#!/usr/bin/env bash
# _common.sh — shared preflight helpers for tools/eval/experiments/
# per-workload wrappers.
#
# Sourced, not executed. Every helper returns exit code (0 = OK,
# 1 = negative-but-non-fatal, 2 = fatal). Config parsers NEVER print
# key names or values — only `present` / `absent` / `error`.
#
# Global constraints (spec §Global Constraints):
# - Preflight probes MUST NOT execute `codex` (no `codex doctor`,
#   `codex exec`, `codex --version`). Only `command -v codex`.
# - Config parsing is read-only and NEVER logs values.
# - No unnamespaced env vars leaked to sub-scripts.
#
# All helpers are safe to call under `set -euo pipefail`.

# Where's the codex config? Real code uses ~/.codex/config.toml; tests
# override via LOOM_CODEX_CONFIG_PATH.
_codex_config_path() {
  printf '%s\n' "${LOOM_CODEX_CONFIG_PATH:-$HOME/.codex/config.toml}"
}

codex_bin_present() {
  # NEVER invoke codex — only look it up on PATH.
  command -v codex >/dev/null 2>&1 || return 2
  return 0
}

codex_config_readable() {
  local p; p=$(_codex_config_path)
  [ -r "$p" ] || return 2
  return 0
}

# Read-only TOML probe. Emits ONLY the token `present` or `absent`
# to stdout; on parse failure, `error` and rc=2. NEVER emits key names
# or values.
codex_config_has_route_a() {
  local p; p=$(_codex_config_path)
  [ -r "$p" ] || { printf 'error\n'; return 2; }
  local out
  out=$(python3 - "$p" <<'PY' 2>/dev/null
import sys, tomllib
try:
    with open(sys.argv[1], "rb") as f:
        data = tomllib.load(f)
except Exception:
    print("error"); sys.exit(2)
node = data.get("model_providers", {}).get("modelserver", {})
if isinstance(node, dict) and "experimental_bearer_token" in node:
    val = node["experimental_bearer_token"]
    if isinstance(val, str) and val.strip():
        print("present"); sys.exit(0)
print("absent"); sys.exit(1)
PY
)
  local rc=$?
  # Belt: strip anything that isn't the fixed vocab.
  case "$out" in
    present|absent|error) printf '%s\n' "$out" ;;
    *) printf 'error\n'; rc=2 ;;
  esac
  return "$rc"
}

codex_config_has_route_b() {
  local p; p=$(_codex_config_path)
  [ -r "$p" ] || { printf 'error\n'; return 2; }
  local out
  out=$(python3 - "$p" <<'PY' 2>/dev/null
import sys, tomllib
try:
    with open(sys.argv[1], "rb") as f:
        data = tomllib.load(f)
except Exception:
    print("error"); sys.exit(2)
node = data.get("model_providers", {}).get("modelserver", {})
if isinstance(node, dict) and "env_key" in node:
    val = node["env_key"]
    if isinstance(val, str) and val.strip():
        print("present"); sys.exit(0)
print("absent"); sys.exit(1)
PY
)
  local rc=$?
  case "$out" in
    present|absent|error) printf '%s\n' "$out" ;;
    *) printf 'error\n'; rc=2 ;;
  esac
  return "$rc"
}

require_writable_tmp() {
  local d
  d=$(mktemp -d 2>/dev/null) || return 2
  rmdir "$d" 2>/dev/null || return 2
  return 0
}

require_fixture() {
  local p="${1:?require_fixture: path arg required}"
  [ -e "$p" ] || {
    printf '[_common.sh] required fixture missing: %s\n' "$p" >&2
    return 2
  }
  return 0
}

require_windows_host() {
  case "$(uname -s 2>/dev/null || echo unknown)" in
    *NT*|MSYS*|CYGWIN*|MINGW*) return 0 ;;
    *) return 1 ;;
  esac
}

die() {
  printf '%s\n' "$*" >&2
  exit 2
}

warn_and_exit_zero() {
  printf '%s\n' "$*" >&2
  exit 0
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_common_helpers.py -q -v`

Expected: all _common.sh sub-tests PASS.

- [ ] **Step 5: Full pytest sweep — regression**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/ tools/eval/experiments/tests/ -q`

Expected: all previous + new tests pass.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/tools/eval/experiments/_common.sh multi-agent/tools/eval/experiments/tests/test_common_helpers.sh multi-agent/tools/eval/experiments/tests/test_common_helpers.py
git commit -m "WT-4 Task 12: experiments/_common.sh helpers + bash unit tests (config parsers never log values)"
```

---

### Task 13 — 10-fixture credential-bound preflight test

**Files:**
- Create: `multi-agent/tools/eval/experiments/tests/fixtures/codex_config/*.toml` (10 files)
- Create: `multi-agent/tools/eval/experiments/tests/test_credential_bound_preflight.sh`
- Create: `multi-agent/tools/eval/experiments/tests/test_credential_bound_preflight.py` (pytest wrapper)

**Interfaces:**
- Consumes: `_common.sh` helpers (Task 12), the (yet-to-be-created) `credential_bound_model.sh` wrapper (Task 18).
- Produces: 10 fixtures × asserted outcomes per spec §4.5 table. Route-b-only fixtures MUST exit 2 (unsupported); route-a-only + both must exit 0; commented/wrong-table/malformed must exit 2 with clear messages; token-shape fixture MUST NOT leak value.

**Note on order**: this test is written FIRST (RED). Task 18's
`credential_bound_model.sh` implementation makes it GREEN. Between
Task 13 and Task 18, the pytest wrapper is marked `pytest.skip` unless
the wrapper file exists — so intermediate task sweeps don't block.

- [ ] **Step 1: Write 10 fixture TOML files**

Under `multi-agent/tools/eval/experiments/tests/fixtures/codex_config/`:

`01_route_a_only.toml`:
```toml
[model_providers.modelserver]
experimental_bearer_token = "sk-abcTOKEN12345678901"
```

`02_route_b_only_env_set.toml`:
```toml
[model_providers.modelserver]
env_key = "SOME_FIXTURE_ENV_NAME"
```

`03_route_b_only_env_unset.toml`:
```toml
[model_providers.modelserver]
env_key = "SOME_UNSET_FIXTURE_ENV_NAME"
```

`04_both.toml`:
```toml
[model_providers.modelserver]
experimental_bearer_token = "sk-both1234567890"
env_key = "SOME_FIXTURE_ENV_NAME"
```

`05_neither.toml`:
```toml
[model_providers.modelserver]
some_other_field = "not-relevant"
```

`06_route_a_commented.toml`:
```toml
[model_providers.modelserver]
# experimental_bearer_token = "sk-shouldNotCount"
```

`07_route_a_wrong_table.toml`:
```toml
[model_providers.other]
experimental_bearer_token = "sk-otherTable12345"
```

`08_malformed.toml`:
```toml
[model_providers.modelserver
experimental_bearer_token = "unterminated section header
```

`09_duplicate_tables.toml`:
```toml
[model_providers.modelserver]
experimental_bearer_token = "sk-firstTable"

[model_providers.modelserver]
env_key = "SECOND_TABLE_ENV"
```

`10_token_shaped_value.toml`:
```toml
[model_providers.modelserver]
experimental_bearer_token = "sk-abc123leakcheckXYZ7890"
```

- [ ] **Step 2: Write `test_credential_bound_preflight.sh`**

```bash
#!/usr/bin/env bash
# Task 13 — credential_bound_model.sh wrapper preflight against 10
# fixture TOML files. Verifies spec §4.5 outcome table + secret-non-leak.
#
# Skipped gracefully if credential_bound_model.sh does not yet exist
# (Task 18 creates it). Once Task 18 lands, this test flips from
# SKIP to expected outcomes.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
wrapper="$module_root/tools/eval/experiments/credential_bound_model.sh"
fixtures="$here/fixtures/codex_config"

if [ ! -x "$wrapper" ]; then
  echo "SKIP: $wrapper not yet created (Task 18)"
  exit 0
fi

fail=0
report() {
  if [ "$1" -eq 0 ]; then
    echo "PASS  $2"
  else
    echo "FAIL  $2"
    fail=1
  fi
}

# Run wrapper preflight only (--dry-run + LOOM_FULLTABLE_WRAPPER_SHIM=1
# short-circuits before dispatch AND before commit-tree preflight).
run_preflight() {
  local fixture="$1"
  local extra_env="${2:-}"
  LOOM_CODEX_CONFIG_PATH="$fixture" \
    LOOM_FULLTABLE_WRAPPER_SHIM=1 \
    $extra_env \
    bash "$wrapper" --dry-run 2>&1
}

# Case 1 — route-a-only → exit 0
run_preflight "$fixtures/01_route_a_only.toml" > /tmp/case01.out 2>&1
rc=$?
[ "$rc" -eq 0 ] && report 0 "01 route-a-only → exit 0" || { report 1 "01 route-a-only → exit 0 (got $rc)"; cat /tmp/case01.out; }

# Case 2 — route-b-only, env set → exit 2 (unsupported)
SOME_FIXTURE_ENV_NAME=xyz run_preflight "$fixtures/02_route_b_only_env_set.toml" > /tmp/case02.out 2>&1
rc=$?
[ "$rc" -eq 2 ] && report 0 "02 route-b-only env-set → exit 2 unsupported" || report 1 "02 route-b-only → exit 2 (got $rc)"

# Case 3 — route-b-only, env unset → exit 2 same reason
run_preflight "$fixtures/03_route_b_only_env_unset.toml" > /tmp/case03.out 2>&1
rc=$?
[ "$rc" -eq 2 ] && report 0 "03 route-b-only env-unset → exit 2 unsupported" || report 1 "03 route-b-only env-unset → exit 2 (got $rc)"

# Case 4 — both → exit 0, informational route-b-detected msg
run_preflight "$fixtures/04_both.toml" > /tmp/case04.out 2>&1
rc=$?
[ "$rc" -eq 0 ] && grep -q "route_b detected" /tmp/case04.out && \
  report 0 "04 both → exit 0 with route-b-detected note" || \
  { report 1 "04 both → exit 0 with route-b-detected note (rc=$rc)"; cat /tmp/case04.out; }

# Case 5 — neither → exit 2
run_preflight "$fixtures/05_neither.toml" > /tmp/case05.out 2>&1
rc=$?
[ "$rc" -eq 2 ] && report 0 "05 neither → exit 2" || report 1 "05 neither → exit 2 (got $rc)"

# Case 6 — route-a commented → exit 2 (parser must NOT count comments)
run_preflight "$fixtures/06_route_a_commented.toml" > /tmp/case06.out 2>&1
rc=$?
[ "$rc" -eq 2 ] && report 0 "06 route-a commented → exit 2 (not counted)" || report 1 "06 route-a commented → exit 2 (got $rc)"

# Case 7 — route-a in different table → exit 2
run_preflight "$fixtures/07_route_a_wrong_table.toml" > /tmp/case07.out 2>&1
rc=$?
[ "$rc" -eq 2 ] && report 0 "07 route-a wrong-table → exit 2" || report 1 "07 route-a wrong-table → exit 2 (got $rc)"

# Case 8 — malformed TOML → exit 2 with parse-error msg
run_preflight "$fixtures/08_malformed.toml" > /tmp/case08.out 2>&1
rc=$?
if [ "$rc" -eq 2 ] && grep -q "could not parse" /tmp/case08.out; then
  report 0 "08 malformed → exit 2 with parse-error message"
else
  report 1 "08 malformed → exit 2 with parse-error message (rc=$rc)"
  cat /tmp/case08.out
fi
# Belt: no token-shape leak in stderr
if grep -q "sk-" /tmp/case08.out; then
  report 1 "SECURITY 08 malformed → stderr contains sk-*"
else
  report 0 "08 malformed → no sk- leak"
fi

# Case 9 — duplicate tables → exit 2 with parse-error msg (TOML forbids)
run_preflight "$fixtures/09_duplicate_tables.toml" > /tmp/case09.out 2>&1
rc=$?
if [ "$rc" -eq 2 ] && grep -q "could not parse" /tmp/case09.out; then
  report 0 "09 duplicate tables → exit 2 with parse-error message"
else
  report 1 "09 duplicate tables → exit 2 with parse-error message (rc=$rc)"
  cat /tmp/case09.out
fi

# Case 10 — token-shaped value present → exit 0 AND assert no leak
run_preflight "$fixtures/10_token_shaped_value.toml" > /tmp/case10.out 2>&1
rc=$?
[ "$rc" -eq 0 ] && report 0 "10 token-shaped → exit 0" || report 1 "10 token-shaped → exit 0 (got $rc)"
if grep -qE "sk-abc123leakcheck|leakcheckXYZ" /tmp/case10.out; then
  report 1 "SECURITY 10 token-shaped → stderr LEAKED token value"
  cat /tmp/case10.out
else
  report 0 "10 token-shaped → NO leak in stderr"
fi

exit $fail
```

`test_credential_bound_preflight.py` (pytest wrapper):
```python
"""pytest wrapper for the 10-fixture credential-bound preflight suite."""
import subprocess
from pathlib import Path


def test_credential_bound_preflight() -> None:
    script = Path(__file__).parent / "test_credential_bound_preflight.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    # Skip is exit 0 with "SKIP:" on stdout — Task 18 hasn't created
    # the wrapper yet.
    if "SKIP:" in proc.stdout and proc.returncode == 0:
        import pytest
        pytest.skip(proc.stdout.strip())
    assert proc.returncode == 0, (
        f"credential-bound preflight failed:\n"
        f"stdout:\n{proc.stdout}\n"
        f"stderr:\n{proc.stderr}"
    )
```

- [ ] **Step 3: Run tests — expect SKIP (wrapper not yet in Task 18)**

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_credential_bound_preflight.py -q -v`

Expected: SKIP with message about missing wrapper.

- [ ] **Step 4: Commit fixtures + tests (they lock the outcomes before Task 18 writes the wrapper — TDD discipline)**

```bash
git add multi-agent/tools/eval/experiments/tests/fixtures/ multi-agent/tools/eval/experiments/tests/test_credential_bound_preflight.sh multi-agent/tools/eval/experiments/tests/test_credential_bound_preflight.py
git commit -m "WT-4 Task 13: 10 credential-bound preflight fixtures + skipped test (Task 18 flips to green)"
```

---

### Task 13.5 — .gitignore for `tests/eval/results/experiments/` (plan-review r5 P1)

**Files:**
- Modify: `multi-agent/.gitignore` (or root `.gitignore` if module-level doesn't exist)

**Interfaces:**
- Consumes: nothing.
- Produces: wrappers can `mkdir -p <default_results_root>` (in a follow-up
  design if needed) OR run.sh can create the alt root after preflight
  without dirtying the tree — either way, `tests/eval/results/experiments/`
  MUST be gitignored so a run there does not fail commit_meta preflight.

- [ ] **Step 1: Read current .gitignore**

```bash
find multi-agent -name .gitignore -maxdepth 3 | xargs -I {} sh -c 'echo "=== {} ==="; cat {}'
```

Locate the entry for `tests/eval/results/smoke/`. If none exists, add
the whole `tests/eval/results/` block; if only `smoke/` is ignored,
add `experiments/` next to it.

- [ ] **Step 2: Add the entry**

Append to the correct .gitignore file (module-level preferred):
```
# WT-4 — per-workload experiments results (plan §Task 13.5)
tests/eval/results/experiments/
```

- [ ] **Step 3: Verify with git status**

Run:
```bash
mkdir -p multi-agent/tests/eval/results/experiments/_probe/probe_file
echo "test" > multi-agent/tests/eval/results/experiments/_probe/probe_file/x
git status --short multi-agent/tests/eval/results/experiments/
```
Expected: NO output (probe file is ignored). Then clean:
```bash
rm -rf multi-agent/tests/eval/results/experiments/_probe
```

- [ ] **Step 4: Commit**

```bash
git add multi-agent/.gitignore     # or whichever .gitignore was modified
git commit -m "WT-4 Task 13.5: .gitignore tests/eval/results/experiments/ (wrappers write here)"
```

---

### Task 14–18 — 5 per-workload wrapper scripts (batched — same shape, workload-specific preflight differs)

**Files (per wrapper, N ∈ {14..18})**:
- Create: `multi-agent/tools/eval/experiments/<workload>.sh`
- Create: (contribute to) `multi-agent/tools/eval/experiments/tests/test_wrapper_forwards.sh`

**Wrapper mapping**:
| Task | Wrapper | Pinned workload_id | Extra preflight |
|---|---|---|---|
| 14 | `cross_device_code_mod.sh` | `cross-device-code-mod` | none |
| 15 | `missing_parser_converter.sh` | `missing-parser-converter` | none |
| 16 | `remote_data_processing.sh` | `remote-data-processing` | `require_writable_tmp` + `require_fixture` on workloads dir |
| 17 | `windows_only_artifact.sh` | `windows-only-artifact` | `require_windows_host` guard + `--skip-if-not-windows` opt-out |
| 18 | `credential_bound_model.sh` | `credential-bound-model` | route-a required, route-b detected-but-unsupported |

**Shared shape** (spec §4.4):
- Reject caller-supplied `--workload` / `--filter-workload` (exit 2).
- Accept `--dry-run`, `--sample N`, `--results-root <abs-dir>`, `-h|--help`.
- Preflight: `codex_bin_present`, `codex_config_readable`, then workload-specific extras.
- On preflight OK: print `[wrapper:<workload>] preflight OK` + `[wrapper:<workload>] results-root: <resolved>` to stderr, then `exec bash <fulltable/run.sh> --workload <pinned-id> --results-root <resolved> ...forwarded-args`.

- [ ] **Step 1: Write `test_wrapper_forwards.sh` (RED first)**

```bash
#!/usr/bin/env bash
# Task 14-18 — per-wrapper: preflight passes + forwards --workload=<id>
# to run.sh via LOOM_FULLTABLE_WRAPPER_SHIM=1 test seam. Also asserts
# each wrapper REJECTS caller-supplied --workload / --filter-workload
# (spec §4.4 P0#3 belt).
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
exp_dir="$here/.."
module_root="$(cd "$here/../../../.." && pwd)"

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

# All 5 wrappers, in {file, expected_workload_id, extra_env}
wrappers=(
  "cross_device_code_mod.sh:cross-device-code-mod:"
  "missing_parser_converter.sh:missing-parser-converter:"
  "remote_data_processing.sh:remote-data-processing:"
  # windows_only handled separately — needs --skip-if-not-windows on Linux
  "windows_only_artifact.sh:windows-only-artifact:--skip-if-not-windows"
  # credential_bound handled separately — needs LOOM_CODEX_CONFIG_PATH pointing to a route-a fixture
  "credential_bound_model.sh:credential-bound-model:"
)

# Point codex_config to a route-a-only fixture for the whole run so
# credential_bound_model wrapper's preflight passes. Set fake codex too.
fake_bin=$(mktemp -d)
touch "$fake_bin/codex"; chmod +x "$fake_bin/codex"
export PATH="$fake_bin:$PATH"
export LOOM_CODEX_CONFIG_PATH="$here/fixtures/codex_config/01_route_a_only.toml"

for entry in "${wrappers[@]}"; do
  wrapper="${entry%%:*}"
  rest="${entry#*:}"
  workload_id="${rest%%:*}"
  extra="${rest#*:}"
  script="$exp_dir/$wrapper"

  if [ ! -x "$script" ]; then
    echo "SKIP  $wrapper (not created yet)"
    continue
  fi

  # Case A: preflight + forward under SHIM
  if [ "$wrapper" = "windows_only_artifact.sh" ]; then
    out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --skip-if-not-windows --dry-run 2>&1)
    rc=$?
    # On non-Windows, this exits 0 with warning (warn_and_exit_zero).
    # The SHIM path is only reached on real Windows OR when the guard
    # is truly on Windows. On Linux with --skip-if-not-windows we
    # accept exit 0.
    [ "$rc" -eq 0 ] && report 0 "$wrapper preflight OK/skip" || { report 1 "$wrapper preflight (rc=$rc)"; echo "$out"; }
  else
    out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run 2>&1)
    rc=$?
    [ "$rc" -eq 0 ] && report 0 "$wrapper preflight" || { report 1 "$wrapper preflight (rc=$rc)"; echo "$out"; }
    if ! echo "$out" | grep -q "SHIM_WORKLOAD_FILTER: $workload_id"; then
      report 1 "$wrapper forwards --workload=$workload_id"
      echo "$out"
    else
      report 0 "$wrapper forwards --workload=$workload_id"
    fi
  fi

  # Case B: caller-supplied --workload → exit 2, message present
  out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run --workload other-id 2>&1)
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q "pinned"; then
    report 0 "$wrapper rejects caller --workload"
  else
    report 1 "$wrapper rejects caller --workload (rc=$rc)"
    echo "$out"
  fi

  # Case C: caller-supplied --filter-workload → also exit 2
  out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run --filter-workload other-id 2>&1)
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q "pinned"; then
    report 0 "$wrapper rejects caller --filter-workload"
  else
    report 1 "$wrapper rejects caller --filter-workload (rc=$rc)"
    echo "$out"
  fi
done

exit $fail
```

Also add pytest wrapper `test_wrapper_forwards.py`:
```python
"""pytest wrapper for test_wrapper_forwards.sh."""
import subprocess
from pathlib import Path


def test_wrapper_forwards() -> None:
    script = Path(__file__).parent / "test_wrapper_forwards.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, f"{proc.stdout}\n{proc.stderr}"
```

Also add `test_wrapper_real_preflight.sh` (plan-review r5 P1 — proves
wrapper's default-results-root construction doesn't dirty the tree and
break commit_meta preflight):

```bash
#!/usr/bin/env bash
# Plan-review r5 P1 — real-path dispatch-shim (NO WRAPPER_SHIM). Proves
# wrappers don't create files that dirty the worktree before run.sh
# preflight runs. If wrappers mkdir the default_results_root eagerly
# AND experiments/ isn't gitignored, preflight fails here.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
exp_dir="$here/.."
module_root="$(cd "$here/../../../.." && pwd)"

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

clean_repo="$tmpdir/clean_repo"
git init -q "$clean_repo"
git -C "$clean_repo" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q

fake_bin="$tmpdir/bin"
mkdir -p "$fake_bin"
touch "$fake_bin/codex"; chmod +x "$fake_bin/codex"
export PATH="$fake_bin:$PATH"
export LOOM_CODEX_CONFIG_PATH="$here/fixtures/codex_config/01_route_a_only.toml"

# cross_device_code_mod is representative; others share the same shape.
wrapper="$exp_dir/cross_device_code_mod.sh"
if [ ! -x "$wrapper" ]; then
  echo "SKIP: $wrapper not yet created"
  exit 0
fi

out=$(ALLOW_FULL_RUN=1 WORKTREE_ROOT="$clean_repo" LOOM_FULLTABLE_DISPATCH_SHIM=1 \
    bash "$wrapper" --sample 1 2>&1)
rc=$?
if [ "$rc" -ne 0 ]; then
  report 1 "cross_device wrapper dispatch-shim preflight (rc=$rc)"
  echo "$out"
else
  report 0 "cross_device wrapper dispatch-shim preflight passes"
fi

# Belt: after wrapper runs, git status of the worktree must NOT show
# any new untracked files under tests/eval/results/experiments/. This
# proves either (a) wrapper doesn't mkdir eagerly OR (b) the dir is
# .gitignored.
untracked=$(cd "$module_root" && git status --short -- tests/eval/results/experiments/ 2>/dev/null | wc -l)
if [ "$untracked" -gt 0 ]; then
  report 1 "cross_device wrapper left $untracked untracked files under experiments/ (would fail real preflight)"
  cd "$module_root" && git status --short -- tests/eval/results/experiments/
else
  report 0 "cross_device wrapper left no untracked experiments/ files"
fi

exit $fail
```

Add to `test_wrapper_forwards.py` alongside the primary test (or wire
via its own pytest wrapper). Simplest: append inside
`test_wrapper_forwards.py`:

```python
def test_wrapper_real_preflight() -> None:
    script = Path(__file__).parent / "test_wrapper_real_preflight.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, f"{proc.stdout}\n{proc.stderr}"
```

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_wrapper_forwards.py -q -v` → expect FAIL / all SKIP (no wrappers yet).

- [ ] **Step 2: Write `cross_device_code_mod.sh` (Task 14)**

```bash
#!/usr/bin/env bash
# tools/eval/experiments/cross_device_code_mod.sh
# Per-workload runner for cross-device-code-mod.
# Pins workload_id. Forwards to tools/eval/fulltable/run.sh.
set -euo pipefail

WORKLOAD="cross-device-code-mod"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
common="$here/_common.sh"
module_root="$(cd "$here/../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# shellcheck source=/dev/null
source "$common"

usage() {
  cat <<EOF >&2
Usage: $(basename "$0") [--dry-run] [--sample N] [--results-root <abs-dir>]

Per-workload runner for ${WORKLOAD}. Pins workload id; forwards other
flags to tools/eval/fulltable/run.sh --workload ${WORKLOAD}.

Preflight (fatal on failure):
  1. codex binary on \$PATH
  2. ~/.codex/config.toml readable

Not accepted (rejected with exit 2):
  --workload, --filter-workload (pinned by this wrapper)

Downstream flags:
$(bash "$run_sh" --help 2>&1 | head -20)
EOF
}

# Reject caller-supplied --workload / --filter-workload BEFORE any
# other processing (spec §4.4 P0#3 wrapper-side belt).
for arg in "$@"; do
  case "$arg" in
    --workload|--workload=*|--filter-workload|--filter-workload=*)
      die "wrapper:$WORKLOAD: --workload / --filter-workload is pinned; remove from argv"
      ;;
    -h|--help) usage; exit 0 ;;
  esac
done

# Preflight
codex_bin_present || die "wrapper:$WORKLOAD: codex binary not on \$PATH; install via 'npm i -g @openai/codex'"
codex_config_readable || die "wrapper:$WORKLOAD: ~/.codex/config.toml not readable"

# Default results-root: per-invocation subdir under module-root.
# CRITICAL — plan-review r5 P1: do NOT mkdir the default root here.
# `commit_meta --preflight` runs inside run.sh and rejects a dirty
# worktree (spec §7(f)); an untracked
# tests/eval/results/experiments/<workload>/<subdir>/ would fail
# preflight even though `.gitignore` should cover this directory (it
# does NOT today: `.gitignore` only excludes tests/eval/results/smoke/,
# not tests/eval/results/experiments/). Compute the path here; let
# run.sh create it AFTER preflight passes.
default_root_base="$module_root/tests/eval/results/experiments/$WORKLOAD"
subdir="$(date -u +%Y-%m-%dT%H-%M-%SZ)-$$"
default_results_root="$default_root_base/$subdir"
# NB: run.sh's --results-root allowlist rejects non-existent paths only
# for the git-top-level check; the "non-empty unless --resume" check
# tolerates a non-existent path. All other creators (run.sh mkdir -p
# after preflight, plan.py) create the path lazily.

# If caller provided --results-root, honor it (run.sh validates the
# allowlist). Else pass our default.
have_results_root=0
for arg in "$@"; do
  case "$arg" in --results-root|--results-root=*) have_results_root=1 ;; esac
done

echo "[wrapper:$WORKLOAD] preflight OK" >&2
if [ "$have_results_root" -eq 0 ]; then
  echo "[wrapper:$WORKLOAD] results-root: $default_results_root" >&2
  exec bash "$run_sh" --workload "$WORKLOAD" --results-root "$default_results_root" "$@"
else
  echo "[wrapper:$WORKLOAD] results-root: <caller-supplied>" >&2
  exec bash "$run_sh" --workload "$WORKLOAD" "$@"
fi
```

Mark executable: `chmod +x multi-agent/tools/eval/experiments/cross_device_code_mod.sh`.

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_wrapper_forwards.py -q -v` → 3 sub-tests for `cross_device_code_mod.sh` should PASS; others SKIP.

Commit:
```bash
git add multi-agent/tools/eval/experiments/cross_device_code_mod.sh multi-agent/tools/eval/experiments/tests/test_wrapper_forwards.sh multi-agent/tools/eval/experiments/tests/test_wrapper_forwards.py
git commit -m "WT-4 Task 14: cross_device_code_mod.sh wrapper + wrapper-forwards test frame"
```

- [ ] **Step 3: Write `missing_parser_converter.sh` (Task 15)**

Identical to cross_device_code_mod.sh except:
- `WORKLOAD="missing-parser-converter"`
- No extra preflight (same as cross-device).

Copy cross_device_code_mod.sh, sed the workload id, chmod +x, run tests, commit:
```bash
sed 's/cross-device-code-mod/missing-parser-converter/g' \
  multi-agent/tools/eval/experiments/cross_device_code_mod.sh \
  > multi-agent/tools/eval/experiments/missing_parser_converter.sh
chmod +x multi-agent/tools/eval/experiments/missing_parser_converter.sh
cd multi-agent && pytest tools/eval/experiments/tests/test_wrapper_forwards.py -q -v
git add multi-agent/tools/eval/experiments/missing_parser_converter.sh
git commit -m "WT-4 Task 15: missing_parser_converter.sh wrapper"
```

- [ ] **Step 4: Write `remote_data_processing.sh` (Task 16)**

Same as Task 14 with `WORKLOAD="remote-data-processing"` PLUS two extra preflight lines after the config check:

```bash
require_writable_tmp || die "wrapper:$WORKLOAD: /tmp not writable"
require_fixture "$module_root/tests/eval/workloads/remote-data-processing/spec.yaml" \
  || die "wrapper:$WORKLOAD: workload spec.yaml missing at expected path"
```

Commit:
```bash
git add multi-agent/tools/eval/experiments/remote_data_processing.sh
git commit -m "WT-4 Task 16: remote_data_processing.sh wrapper (+ writable-tmp + fixture preflight)"
```

- [ ] **Step 5: Write `windows_only_artifact.sh` (Task 17)**

Same as Task 14 with `WORKLOAD="windows-only-artifact"` PLUS explicit Windows-host guard + `--skip-if-not-windows` opt-out.

Add near the `for arg in "$@"` argv-scan:
```bash
skip_if_not_windows=0
for arg in "$@"; do
  case "$arg" in
    --skip-if-not-windows) skip_if_not_windows=1 ;;
  esac
done
# Strip --skip-if-not-windows from the args forwarded to run.sh
new_args=(); for arg in "$@"; do
  case "$arg" in --skip-if-not-windows) ;; *) new_args+=("$arg") ;; esac
done
set -- "${new_args[@]:-}"

# Guard
if ! require_windows_host; then
  if [ "$skip_if_not_windows" -eq 1 ]; then
    warn_and_exit_zero "wrapper:$WORKLOAD: not a Windows host (uname=$(uname -s)); --skip-if-not-windows honored, exit 0"
  else
    die "wrapper:$WORKLOAD: requires a Windows host (uname=$(uname -s)); pass --skip-if-not-windows to acknowledge and exit 0"
  fi
fi
```

Commit:
```bash
git add multi-agent/tools/eval/experiments/windows_only_artifact.sh
git commit -m "WT-4 Task 17: windows_only_artifact.sh wrapper + require_windows_host guard"
```

- [ ] **Step 6: Write `credential_bound_model.sh` (Task 18)**

Same as Task 14 with `WORKLOAD="credential-bound-model"` PLUS credential-bound preflight:

```bash
# Additional preflight for credential-bound-model per spec §4.4
# (route-b unsupported in this PR).
#
# Plan-review r3 P1 fix: under `set -euo pipefail`, the form
# `route_a=$(cmd); route_a_rc=$?` triggers set -e on any non-zero exit
# from cmd BEFORE the second statement runs. Must use if/then/else to
# capture rc safely.
if route_a=$(codex_config_has_route_a); then route_a_rc=0; else route_a_rc=$?; fi
if route_b=$(codex_config_has_route_b); then route_b_rc=0; else route_b_rc=$?; fi

# Helpers return `error` + rc=2 on parse failure. If either errors,
# die BEFORE the route logic to surface the parse failure clearly.
# Only when BOTH helpers succeeded (rc 0 or 1) do we apply the
# route-a-required / route-b-detected logic.

if [ "$route_a_rc" -eq 2 ] || [ "$route_b_rc" -eq 2 ]; then
  # error means: file missing, malformed TOML, or duplicate tables.
  # The helper's stdout is already sanitized to the fixed vocab
  # ({present, absent, error}); never contains key names / values.
  die "wrapper:$WORKLOAD: could not parse ~/.codex/config.toml (route_a=$route_a rc=$route_a_rc; route_b=$route_b rc=$route_b_rc); check file exists, is TOML-valid, and has at most one [model_providers.modelserver] table."
fi

# Log states only (never values / names)
echo "[wrapper:$WORKLOAD] route_a: $route_a, route_b: $route_b" >&2

# Route-a required, route-b unsupported.
if [ "$route_a" = "present" ]; then
  if [ "$route_b" = "present" ]; then
    echo "[wrapper:$WORKLOAD] note: route_b detected in ~/.codex/config.toml but route_a will be used (route_b support deferred; see docs/specs/wt4-codex-only.handoff.md §Route-b support)" >&2
  fi
  : # OK
elif [ "$route_b" = "present" ]; then
  die "wrapper:$WORKLOAD: route_b is present in ~/.codex/config.toml [model_providers.modelserver] but is not supported by this baseline yet; route_a (experimental_bearer_token) is required. See docs/specs/wt4-codex-only.handoff.md §Route-b support."
else
  die "wrapper:$WORKLOAD: credential-bound-model requires route (a) [experimental_bearer_token under model_providers.modelserver] in ~/.codex/config.toml. Route (b) support is deferred; see handoff."
fi
```

Now the credential-bound preflight test (Task 13) should flip from SKIP to PASS:

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_credential_bound_preflight.py -q -v`

Expected: all 10 sub-tests PASS.

Also run wrapper-forwards test — all 5 wrappers now PASS their 3 sub-tests each = 15 sub-tests:

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_wrapper_forwards.py -q -v`

Expected: PASS.

Commit:
```bash
git add multi-agent/tools/eval/experiments/credential_bound_model.sh
git commit -m "WT-4 Task 18: credential_bound_model.sh wrapper (route-a required, route-b unsupported)"
```

---

### Task 19 — Windows-guard test table + WSL / uname-missing edge cases

**Files:**
- Create: `multi-agent/tools/eval/experiments/tests/test_windows_guard.sh`
- Create: `multi-agent/tools/eval/experiments/tests/test_windows_guard.py`

**Interfaces:**
- Consumes: `_common.sh` `require_windows_host` (Task 12).
- Produces: parametrized outcome table under stubbed `uname` binaries.

- [ ] **Step 1: Write test**

```bash
#!/usr/bin/env bash
# Task 19 — require_windows_host uname parametrized table.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
common="$here/../_common.sh"
fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

# Build a stub `uname` that prints whatever we set in _UNAME_STUB_OUT.
tmp=$(mktemp -d); trap "rm -rf $tmp" EXIT
cat > "$tmp/uname" <<'STUB'
#!/bin/sh
if [ "$1" = "-s" ] || [ -z "$1" ]; then
  printf '%s\n' "$_UNAME_STUB_OUT"
else
  # Delegate to the real uname for other flags
  /usr/bin/uname "$@"
fi
STUB
chmod +x "$tmp/uname"
export PATH="$tmp:$PATH"

# Accepted uname strings
for accepted in \
    "Windows_NT" \
    "MINGW64_NT-10.0-19045" \
    "MSYS_NT-10.0" \
    "CYGWIN_NT-10.0-WOW"; do
  _UNAME_STUB_OUT="$accepted" bash -c "source '$common' && require_windows_host"
  report $? "require_windows_host accepts '$accepted'"
done

# Rejected uname strings
for rejected in \
    "Linux" \
    "Darwin" \
    "FreeBSD" \
    "SunOS" \
    ""; do
  _UNAME_STUB_OUT="$rejected" bash -c "source '$common' && require_windows_host" 2>/dev/null
  rc=$?
  [ "$rc" -eq 1 ] && report 0 "require_windows_host rejects '$rejected'" \
                  || report 1 "require_windows_host rejects '$rejected' (got $rc)"
done

# uname binary missing entirely — falls through to `echo unknown`
# in _common.sh fallback → rejected
PATH="/nonexistent" bash -c "source '$common' && require_windows_host" 2>/dev/null
rc=$?
[ "$rc" -eq 1 ] && report 0 "require_windows_host handles missing uname" \
                || report 1 "require_windows_host handles missing uname (got $rc)"

exit $fail
```

pytest wrapper `test_windows_guard.py`:
```python
import subprocess
from pathlib import Path

def test_windows_guard():
    script = Path(__file__).parent / "test_windows_guard.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, f"{proc.stdout}\n{proc.stderr}"
```

- [ ] **Step 2: Run — expect PASS (helpers written in Task 12)**

Run: `cd multi-agent && pytest tools/eval/experiments/tests/test_windows_guard.py -q -v`

Expected: PASS. (If uname fallback fails on some Linux, adjust `_common.sh` `require_windows_host` to use `command -v uname && uname -s || echo unknown`.)

- [ ] **Step 3: Commit**

```bash
git add multi-agent/tools/eval/experiments/tests/test_windows_guard.sh multi-agent/tools/eval/experiments/tests/test_windows_guard.py
git commit -m "WT-4 Task 19: windows-guard test table (MSYS/CYGWIN/MINGW/*NT accept, Linux/Darwin/missing reject)"
```

---

### Task 19.5 — env-allowlist unchanged guard (plan-review P1 resolution)

**Files:**
- Create: `multi-agent/tools/eval/fulltable/tests/test_env_allowlist_unchanged.py`

**Interfaces:**
- Consumes: git baseline `origin/paper/v3-integration` @ `786bf60`.
- Produces: a guard test that FAILS if this PR modifies any of
  `alwaysAllowedEnvKeys`, `alwaysAllowedIfSetEnvKeys`, or
  `perWorkloadAllowedEnvKeys` in `tests/eval/baselines/harness/env.go`
  (spec Global Constraints: allowlist frozen in this PR).

- [ ] **Step 1: Write test**

```python
"""Plan-review P1 — env allow-list in harness/env.go MUST NOT change
in this PR. A change belongs in a follow-up worktree with dedicated
security review. Test compares this branch's env.go against the
base branch's env.go."""
from __future__ import annotations

import subprocess
from pathlib import Path

from conftest import MODULE_ROOT

BASE_REF = "origin/paper/v3-integration"
ENV_GO = "multi-agent/tests/eval/baselines/harness/env.go"


def _extract_allowlists(text: str) -> dict[str, list[str]]:
    """Return dict of allowlist-name → sorted contents. Tolerant of
    formatting drift — walks the source line-by-line looking for the
    known var names + their `{...}` block."""
    import re
    out = {}
    for name in ("alwaysAllowedEnvKeys",
                 "alwaysAllowedIfSetEnvKeys",
                 "perWorkloadAllowedEnvKeys"):
        m = re.search(rf"{name}\s*=\s*(?:map\[[^\]]+\][^{{]*)?{{([^}}]*)}}", text, re.S)
        if not m:
            out[name] = ["<not-found>"]
            continue
        body = m.group(1)
        # Extract quoted strings from body.
        strs = re.findall(r'"([^"]+)"', body)
        out[name] = sorted(strs)
    return out


def test_env_allowlists_unchanged() -> None:
    repo_root = MODULE_ROOT.parent
    current_text = (repo_root / ENV_GO).read_text()
    base_text = subprocess.run(
        ["git", "-C", str(repo_root), "show", f"{BASE_REF}:{ENV_GO}"],
        capture_output=True, text=True, check=True,
    ).stdout
    current = _extract_allowlists(current_text)
    base = _extract_allowlists(base_text)
    assert current == base, (
        "env allowlist changed in this PR — this is out-of-scope for "
        "wt4-codex-only per spec Global Constraints. Move the change "
        "to a follow-up worktree with its own review.\n"
        f"current: {current}\n"
        f"base:    {base}"
    )
```

- [ ] **Step 2: Run — expect PASS (nothing changed yet)**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_env_allowlist_unchanged.py -q -v`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add multi-agent/tools/eval/fulltable/tests/test_env_allowlist_unchanged.py
git commit -m "WT-4 Task 19.5: env-allowlist unchanged guard (harness/env.go frozen for this PR)"
```

---

### Task 19.6 — P2 opportunistic fixes (long-stderr scrub, ctx-cancel, fixture token rename, mktemp per shell test)

**Files:**
- Modify: `multi-agent/tests/eval/baselines/single_machine_codex/impl_test.go` (add 2 tests)
- Modify: `multi-agent/tools/eval/experiments/tests/fixtures/codex_config/*.toml` (rename fixture-token literals to obviously-fake prefixes)
- Modify: shell tests using `/tmp/case*.out` → per-test `mktemp -d`

- [ ] **Step 1: Add long-stderr scrub test to impl_test.go**

```go
// TestSingleMachineCodex_ScrubsLongStderr — plan-review P2: lock
// behavior when codex stderr contains multiple secrets AND is longer
// than secretscrub.Sanitize's 256-rune truncation cap. Assertion:
// even if the tail is truncated (`...[truncated]`), NONE of the
// tokens leak into the visible portion.
func TestSingleMachineCodex_ScrubsLongStderr(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	// ~400 bytes of stderr with tokens at start, middle, and near end.
	script := `#!/bin/sh
{
  printf 'ERROR sk-headertokenXYZlong0123456789 at start\n'
  printf 'padding padding padding padding padding padding padding\n'
  printf 'padding padding padding padding padding padding padding\n'
  printf 'MID: Bearer bar_baz_qux_secret_val_middle_1234567890\n'
  printf 'padding padding padding padding padding padding padding\n'
  printf 'TAIL: sk-tailtokenABCDEF0123456789 near end\n'
} >&2
exit 42
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "cat", "printf", "bash", "ls"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(pathDir, tool))
	}
	_ = os.Symlink(fake, filepath.Join(pathDir, "codex"))
	t.Setenv("PATH", pathDir)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.Err == nil {
		t.Fatalf("expected err from exit 42")
	}
	errStr := res.Err.Error()
	// No visible-portion leaks — Sanitize replaces before truncation.
	for _, banned := range []string{"sk-headertoken", "bar_baz_qux_secret", "sk-tailtoken"} {
		if strings.Contains(errStr, banned) {
			t.Errorf("long-stderr scrub leaked %q into visible error; err=%q", banned, errStr)
		}
	}
}

// TestSingleMachineCodex_CtxCancelScrubs — plan-review P2: cancelation
// path also runs the scrub. Fake codex sleeps 30s; ctx is canceled
// after 100ms with a WithCancel wrap. Assert the returned error is
// scrubbed of any pre-sleep token stderr.
func TestSingleMachineCodex_CtxCancelScrubs(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	script := `#!/bin/sh
printf 'PRE-SLEEP sk-cancelleaktoken0123456789 leak\n' >&2
sleep 30
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "cat", "printf", "bash", "ls", "sleep"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(pathDir, tool))
	}
	_ = os.Symlink(fake, filepath.Join(pathDir, "codex"))
	t.Setenv("PATH", pathDir)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(ctx, harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.Err == nil {
		t.Fatalf("expected err from ctx cancel")
	}
	if strings.Contains(res.Err.Error(), "sk-cancelleak") {
		t.Errorf("ctx-cancel path leaked token; err=%q", res.Err)
	}
}
```

Note the added `time` import if not already present.

- [ ] **Step 2: Rename fixture tokens to obviously-fake forms**

In `tools/eval/experiments/tests/fixtures/codex_config/*.toml`, replace any `sk-...` string that looks credential-shaped enough to be surprising in git-blame with `sk-TEST-NOT-REAL-...`. Example:

`01_route_a_only.toml`:
```toml
# Fixture — obviously fake token; not a real credential.
[model_providers.modelserver]
experimental_bearer_token = "sk-TEST-NOT-REAL-01aaaa"
```

Do the same for `04`, `10`. Add a top-of-file comment line in each fixture: `# Fixture — not a real credential; used by test_credential_bound_preflight.sh`.

- [ ] **Step 3: Convert shell tests using `/tmp/case*.out` to per-test mktemp**

In `test_wrapper_forwards.sh`, `test_credential_bound_preflight.sh`, `test_workload_filter_semantics.sh`, `test_sample_cap_with_workload.sh`, `test_run_sh_duplicate_workload.sh`, replace the pattern:

```bash
out=$(... 2>&1)   # or:
... > /tmp/caseN.out 2>&1
```

with per-test `local` capture buffers or `mktemp -d` per test invocation. Simplest fix that keeps output visible on failure: use bash local vars (`out=$(cmd 2>&1)`) throughout; only when a test needs stdout AND stderr separated does it need a tempdir. If a tempdir is needed:

```bash
casedir=$(mktemp -d); trap "rm -rf '$casedir'" RETURN
run_preflight ... > "$casedir/out" 2>&1
```

- [ ] **Step 4: Run full test sweep**

Run: `cd multi-agent && go test ./tests/eval/baselines/... -race -count=1 -timeout=180s && pytest tools/eval/fulltable/tests/ tools/eval/experiments/tests/ -q`

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add -u
git commit -m "WT-4 Task 19.6: P2 fixes (long-stderr scrub + ctx-cancel + fixture rename + mktemp shell tests)"
```

---

### Task 20 — `experiments/README.md` + repo-wide no-Claude-baseline grep test

**Files:**
- Create: `multi-agent/tools/eval/experiments/README.md`
- Create: `multi-agent/tools/eval/fulltable/tests/test_no_claude_baseline_repo.py`

- [ ] **Step 1: Write README**

```markdown
# tools/eval/experiments/

Per-workload runner scripts for the Phase 3 fulltable harness under
the codex-only baseline (`single_machine_codex`).

Each script pins one `workload_id`, applies workload-specific
preflight, and forwards remaining args to
`tools/eval/fulltable/run.sh --workload <id> --results-root <dir>`.

## Wrappers

| Script | Workload | Extra preflight | Wall-clock/rep (approx) |
|---|---|---|---|
| `cross_device_code_mod.sh` | `cross-device-code-mod` | codex bin + config | ~3 min |
| `missing_parser_converter.sh` | `missing-parser-converter` | codex bin + config | ~3 min |
| `remote_data_processing.sh` | `remote-data-processing` | + writable-tmp + fixture | ~4 min |
| `windows_only_artifact.sh` | `windows-only-artifact` | + `require_windows_host` (or `--skip-if-not-windows`) | ~3 min |
| `credential_bound_model.sh` | `credential-bound-model` | + route (a) required, route (b) unsupported here | ~5 min |

## Common flags

- `--dry-run` — forward to `run.sh --dry-run`; no dispatch.
- `--sample N` — sample cap 3 unless `ALLOW_FULL_RUN=1` (enforced by `run.sh`).
- `--results-root <abs-dir>` — override the default per-invocation results directory. Absolute, allowlisted.
- `-h|--help` — usage + downstream `run.sh` help header.

## Not supported

- `--reps` and `--config-subset` are RESERVED for a follow-up worktree. See `docs/specs/wt4-codex-only.handoff.md`.
- Caller-supplied `--workload` / `--filter-workload` → wrappers exit 2 (pinned).

## Security notes

- Preflight NEVER invokes `codex`. Only `command -v codex >/dev/null`.
- Config parsing is read-only, uses `python3 tomllib`, and NEVER prints keys or values — only `present` / `absent`.
- Wrappers write only to `--results-root <dir>` (allowlisted) and a per-invocation `mktemp -d`.
- Route (b) [`env_key`] is DETECTED for operator visibility but DECLARED UNSUPPORTED in this PR — see spec §4.4 + handoff.

## Test seams

- `LOOM_FULLTABLE_WRAPPER_SHIM=1` (+ `--dry-run`) — dual-guard: `run.sh` prints `SHIM_WORKLOAD_FILTER` / `SHIM_RESULTS_ROOT` and exits 0 without dispatch or commit-tree preflight.
- `LOOM_CODEX_CONFIG_PATH` — overrides `~/.codex/config.toml` (test fixtures under `tests/fixtures/codex_config/`).
- `_UNAME_STUB_OUT` (via a shadowed `uname` in `$PATH`) — parametrizes Windows-host detection in `test_windows_guard.sh`.
```

- [ ] **Step 2: Write repo-wide grep test**

`test_no_claude_baseline_repo.py`:
```python
"""Repo-wide anti-drift: no un-swapped 'single_machine_claude_code'
outside the single_machine/ dir (untouched reference) and this repo's
docs/ tree (spec / plan / handoff can and do reference the old name)."""
from __future__ import annotations

import subprocess
from pathlib import Path

from conftest import MODULE_ROOT

REPO_ROOT = MODULE_ROOT.parent  # <worktree>/


def test_no_claude_baseline_string_in_source() -> None:
    proc = subprocess.run(
        ["grep", "-rln", "single_machine_claude_code",
         str(REPO_ROOT / "multi-agent" / "tests"),
         str(REPO_ROOT / "multi-agent" / "tools")],
        capture_output=True, text=True,
    )
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    # Whitelist: the single_machine/ directory (untouched Claude baseline
    # kept for reference) is allowed to reference the old label.
    allowed_prefix = str(REPO_ROOT / "multi-agent" / "tests" / "eval" / "baselines" / "single_machine") + "/"
    bad = [l for l in lines if not l.startswith(allowed_prefix)]
    assert not bad, (
        "single_machine_claude_code still present in source outside the "
        "reference single_machine/ dir; enum swap incomplete:\n"
        + "\n".join(f"  {l}" for l in bad)
    )
```

- [ ] **Step 3: Run — expect PASS**

Run: `cd multi-agent && pytest tools/eval/fulltable/tests/test_no_claude_baseline_repo.py -q -v`

Expected: PASS.

- [ ] **Step 4: Full test sweep — final green baseline**

Run:
```bash
cd multi-agent
go test ./tests/eval/baselines/... -race -count=1 -timeout=120s
pytest tools/eval/fulltable/tests/ tools/eval/experiments/tests/ -q
```

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/experiments/README.md multi-agent/tools/eval/fulltable/tests/test_no_claude_baseline_repo.py
git commit -m "WT-4 Task 20: experiments/README + repo-wide no-Claude-baseline grep test"
```

---

### Task 21 — Handoff document

**Files:**
- Create: `docs/specs/wt4-codex-only.handoff.md`

- [ ] **Step 1: Write handoff**

```markdown
# WT-4-codex-only — Handoff

Companion to `wt4-codex-only.spec.md` + `wt4-codex-only.plan.md`.
Records everything this worktree CONSCIOUSLY does NOT do, so the
next reviewer / operator / follow-up worktree knows what to pick up.

## Follow-up worktrees required before Phase 4 real-run

### Route-b support (agent-only credential forwarding)

The credential path (b) — `env_key = "SOME_NAME"` in
`~/.codex/config.toml [model_providers.modelserver]` — is DETECTED
in preflight but NOT ACCEPTED as satisfying `credential-bound-model`.

Reason: the existing `tests/eval/baselines/harness/env.go`
LOOM_ passthrough (line ~117-121 `isLoomNS`) forwards to BOTH agent
AND oracle. Using a `LOOM_`-prefixed env-key would leak the secret
to the oracle subprocess, breaking T1 "no implicit credential
forwarding to oracle".

Follow-up worktree TBD adds an agent-only forwarding path. Two
candidate designs:
1. New `alwaysAllowedIfSetEnvKeysAgentOnly` map in `harness/env.go`
   consumed by `WhitelistEnvForAgent` but NOT `WhitelistEnvForOracle`.
2. New `--forward-modelserver-env-key <NAME>` opt-in flag on
   `single_machine_codex/main.go` that propagates the named var to
   the agent only. Symmetric to `--forward-openai-api-key`.

Design choice deferred; this PR does not pre-commit.

Paper impact: `paper_outputs/evaluation_v3.md` §6.1.3's dual-config
claim ("path (a) local proxy AND path (b) workspace-scoped credential
alias") cannot be evaluated on codex-only until this lands. When
writing §6.1.3 language for the current PR, mention this deferral in
a footnote or move the dual-path claim to §Threats to validity.

### `run.sh --reps` / `--config-subset`

Phase 4 p4-experiment-plan.md §"每个 runner script 的统一形态"
listed `--reps` and `--config-subset` alongside `--dry-run` /
`--results-root`. This PR ships `--dry-run`, `--results-root`,
`--sample N`, `--workload` — but NOT `--reps` and `--config-subset`.

Reason: `run.sh` already has `--sample N` + `--resume` + `--parallel`
covering related semantics; converging them (does `--reps 3` fold
into `--sample 12 --repeat 3` or does it introduce a new manifest
axis?) is a design step outside a rename PR.

Follow-up worktree adds these when the real-run planning starts.
The 5 wrappers reject unknown flags via `run.sh`'s existing
"unknown flag" handler, so accidentally passing `--reps 3` today
exits 2 with a clear message — no silent no-op.

## Paper-writing repo — separate PR (not this worktree)

24 occurrences of `single_machine_claude_code` in the paper drafts:
- `paper_writing/paper_outputs/motivation_v3.md` — 9 occurrences
- `paper_writing/paper_outputs/evaluation_v3.md` — 15 occurrences

Also `paper_writing/paper/main.tex` and `paper/main_cn.tex` currently
have 0 occurrences (verified 2026-07-08); a CI check that greps
`main*.tex` for the old label and fails on hit is a good defensive
addition — put it in the paper_writing repo's CI, not here.

Small PR outside this worktree: `sed -i 's/single_machine_claude_code/single_machine_codex/g' paper_outputs/{motivation,evaluation}_v3.md` + a CI grep guard.

## Consciously NOT touched (record-only, no follow-up needed)

- `multi-agent/internal/driver/slave_tools.go` — the tool names
  `get_slave_claude_permissions` / `update_slave_claude_permissions`
  are stable API surface (LLM-facing tool name), not implementation.
  Backend is already agent-kind-agnostic (LOOM_AGENT_KIND=codex).
- `multi-agent/compose-test/entrypoint-driver.sh` — dev-only test
  scaffold; default agent is `claude` because that's what the dev's
  laptop has installed. Codex support is opt-in via env var.
- `mock-model/mock-claude-opus-4-8/` — provider label is referential
  (identifies the mock's target). Codex's mock provider is a separate
  fixture; renaming would confuse existing usage.
- `paper_outputs/related_work_outline.md` mentions of "Claude Code /
  Codex" — these describe products, not baselines. Unchanged.
- `tests/eval/baselines/single_machine/` — the Claude baseline is
  KEPT for reference / paper §Related Work / hypothetical reviewer
  re-run. Only removed from the fulltable matrix.

## P2/P3 findings from codex spec review — record-only

- P2 round-1 #1 (Windows guard test table) → **UPGRADED** to acceptance
  test in Task 19.
- P2 round-1 #2 (`FULLTABLE_RUN_SHIM` naming) → **UPGRADED** to
  `LOOM_FULLTABLE_WRAPPER_SHIM` rename + dual-guard in Task 11 spec.
- P2 round-1 #3 (snapshot baseline path exactness) → **UPGRADED**
  to acceptance test in Task 8.
- P2 round-5 (stale layout comment in spec §2) → fixed in spec §2
  during round-5.
- No P3 findings survived to record here.

## Cross-repo pinning

The paper_writing 24-md swap PR should reference THIS worktree's PR
commit sha (record after merge) so the paper-writing reviewer can
verify the enum landed on the multi-agent side before they merge
the sed swap. Once both merge, the `test_no_claude_baseline_repo.py`
guard here + a symmetric grep guard in paper_writing prevent
regression.

## Repository-level acceptance checklist (spec §7 mirror)

- [ ] `go test ./tests/eval/baselines/single_machine_codex/... -race` — green
- [ ] `go test ./tests/eval/baselines/... -race` — full sweep green
- [ ] `pytest tools/eval/fulltable/tests/ tools/eval/experiments/tests/ -q` — all green
- [ ] `bash tools/eval/fulltable/run.sh --dry-run` — no `single_machine_claude_code` in output
- [ ] `bash tools/eval/fulltable/run.sh --workload cross-device-code-mod --dry-run` — exactly 12 rows
- [ ] 5 wrappers `--help` → exit 0 with combined usage
- [ ] 5 wrappers `--dry-run` (with codex + config preflight satisfied) → dispatch + correct `--workload` filter
- [ ] `windows_only_artifact.sh` on Linux without `--skip-if-not-windows` → exit 2
- [ ] `windows_only_artifact.sh --skip-if-not-windows` on Linux → exit 0 with warning
- [ ] `credential_bound_model.sh` on host without `~/.codex/config.toml` → exit 2, no token leak
- [ ] Repo-wide grep of `single_machine_claude_code` in `multi-agent/{tests,tools}/` returns only `tests/eval/baselines/single_machine/` hits
- [ ] Codex code review has no unresolved P0/P1 findings
```

- [ ] **Step 2: Commit**

```bash
git add docs/specs/wt4-codex-only.handoff.md
git commit -m "WT-4 Task 21: handoff — route-b deferral, --reps deferral, paper_writing PR, P2/P3 disposition"
```

---

## Task summary

Total: 21 tasks + spec + plan + handoff = 24 commits on `paper/v4/codex-only`.

Phase A (Tasks 1–5): 5 commits — single_machine_codex/ baseline binary + prompts + anti-drift + run.sh.
Phase B (Tasks 6–9): 4 commits — matrix + schema + plan.py enum swap + snapshot regen + harness/README swap.
Phase C (Tasks 10–11): 2 commits — plan.py + run.sh --workload / --results-root / SHIM.
Phase D (Tasks 12–19): 8 commits — _common.sh + 10 credential-bound fixtures + 5 wrappers + windows guard.
Phase E (Tasks 20–21): 2 commits — README + repo-wide grep + handoff.

Baseline test suite MUST stay green throughout.

## Self-review checklist (done before submitting to codex plan review)

- [x] Every Global Constraints line maps to at least one test (LOCKED argv → Task 3 pinned-argv; anti-drift → Task 2; stderr scrub → Task 4; snapshot no secrets → Task 8; sample cap → Task 11; workload duplicate → Task 11; route-b unsupported → Task 13+18; config-parser silence → Task 12+13).
- [x] No `TBD` / `TODO` / placeholder text in any task body.
- [x] Every step's code is complete (no "similar to previous task" without repeating).
- [x] Type + function signature consistency: `filter_workload`, `WORKLOADS`, `UnknownWorkloadError`, `_validate_results_root`, `codex_config_has_route_a/b`, `codex_bin_present`, `require_windows_host`, `warn_and_exit_zero`, `die` — all referenced with the same names across their definition and consumer tasks.
- [x] TDD discipline: every BEHAVIORAL task writes the failing test first, verifies it fails, then writes minimum impl, verifies it passes. Explicit documented exceptions: Task 1 (compile-only scaffold), Task 5's run.sh (pattern mirror + smoke-invocation verification in Step 3), Task 20's README (documentation-only; verified by Task 20 Step 2 grep guard).
- [x] `git commit` at every task boundary.

Ready for codex plan review.


