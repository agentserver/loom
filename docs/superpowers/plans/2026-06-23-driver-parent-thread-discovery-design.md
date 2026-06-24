# Driver Parent-Thread Binding + Sidecar-Confirms-Exec Origin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make slave codex sessions visibly nest under their originating driver session in the Commander UI by giving driver-agent an explicit `bind_thread` MCP tool fed by a deterministic discovery script, and stop classifying every `codex exec` rollout as `agent_task` unless a loom-meta sidecar confirms it.

**Architecture:** Two orthogonal fixes folded into PR #31:

- **Q1 — explicit parent thread binding.** driver-agent gains one mutable field (`parentThread atomic.Pointer[string]`) and one MCP tool `bind_thread`. **Parent-link** submissions (`submit_task` / `submit_contract_task` with skill ∈ {`""`, `chat`, `chat_resume`, `fanout`, `fanout_strict`, `route`}, plus `resume_task` which is always parent-link) refuse to operate until the field is set, and stamp the captured thread id into the `loom_origin` SystemContext. Non-parent-link skills (bash, powershell, slave-file, etc.) are unaffected. The previous implicit `loomOriginMarker()` reader is **deleted** so race-free capture-and-pass is the only pattern. Discovery moves entirely into the multiagent skill (`scripts/discover-thread.sh` + an inline heredoc in `SKILL.md`).
- **Q2 — sidecar-confirms-exec origin.** `applyCodexSessionMeta` no longer blanket-promotes exec-mode rollouts to `SessionOriginAgentTask`. `applyLoomMeta` now requires BOTH a valid sidecar (`origin=agent_task`) AND an exec-mode rollout flag (OR-accumulated across the rollout's `session_meta` records) before upgrading to `agent_task`. Codex-native `subagent` classification stays authoritative.

**Tech Stack:** Go 1.22+ (`internal/driver`, `pkg/agentbackend/codex`), bash (skills/multiagent), bats-shape shell tests, existing observer relay + agentsdk + codex executor; no new Go dependencies.

## Reviewer Findings — Resolution Log

This section is appended after the first review round so subsequent readers can audit how the plan was fixed without diffing.

- **P1 — `scanCodexSession` signature.** Real signature is `func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult` (`multi-agent/pkg/agentbackend/codex/sessions.go:246`); returns value, not pointer; `base` is already a parameter. Task 14's File Structure entry, function-skeleton, and the `TestScanCodexSession_MultiSessionMeta_ExecFlagAccumulates` call site all corrected. Only `applyLoomMeta` gains a new parameter; `scanCodexSession`'s signature stays as today.
- **P1 — `submit_task` side-effect test was vacuous.** The original test passed no `read_paths`/`write_paths` so the manifest / audit / observer-write registration code never ran; the registry-empty assertion would pass even with a misplaced guard. Patched (Task 4 Step 1) to supply a real read_path AND write_path, then assert (a) `FileRegistry` blobs/dirs/writes/observerArtifacts all zero AND (b) the audit-log file did not grow. The `snapshotForTest` helper now returns the real bucket counts (`blobs`, `dirs`, `writes`, `observerArtifacts` — see `multi-agent/internal/driver/registry.go:32`), not a fabricated `byToken` field that doesn't exist.
- **P1 — `submit_contract_task` observer test would false-pass.** `NewObserverRelay` returns nil unless `cfg.Observer.Enabled = true`, `cfg.Observer.URL` set, AND a non-nil `TokenSource` is passed (see `multi-agent/internal/driver/observer_relay.go:44`). The default `newTestTools(t, sdk)` passes `obs = nil` ⇒ `toTokenSource(nil) = nil` ⇒ relay is nil ⇒ `SaveResourceSnapshot` is a no-op regardless of ordering. Patched (Task 6 Step 1) to use `newTestToolsWithObserver(t, sdk, &fakeObserver{})` (so `toTokenSource(obs)` returns the `fakeObserver.Token()` source) AND set `cfg.Observer.Enabled = true` AND assert `tools.relay != nil` so the test fails loudly if a future refactor breaks the wiring.
- **P2 — bash exit-code captures.** `err=$(cmd 2>&1 >/dev/null || true); rc=$?` captures `true`'s exit code (0), not the script's. Cases 2, 3, 4, and 10 in `discover-thread_test.sh` (Task 10) all rewritten to `set +e; cmd ...; rc=$?; set -e` so `$?` is the script's real exit code. Stderr capture moved to a temp file where needed.
- **P3 — manual smoke for all-fail.** `CODEX_HOME=$(mktemp -d) unset CODEX_THREAD_ID` is two separate statements; `CODEX_HOME` is NOT carried into the next line's script invocation. Replaced with `env -u CODEX_THREAD_ID CODEX_HOME="$(mktemp -d)" skills/multiagent/scripts/discover-thread.sh` so both env knobs apply to the same one command (Task 9 Step 3).
- **Open question — agentserver v0.48.1 → v0.69.9 bump.** Worktree has an uncommitted `go.mod`/`go.sum` bump unrelated to #24's Q1/Q2 scope. Added "Pre-existing Worktree State (Triage)" section below: recommended path is `git checkout -- multi-agent/go.mod multi-agent/go.sum` and land the bump as a separate PR. Operator can override.

### Second-round fixes

- **P2 — Task 8 was a wrapper-call smoke, not a stdio MCP round-trip.** The previous version called `toolByName(...).Call(...)` directly, bypassing `NewMCPServer`, the JSON-RPC `tools/call` envelope, the `result.content[0].text` shape, the `-32000` error wrapper, and the per-call concurrent goroutine — exactly the layers spec line 733 demanded coverage for. Rewrote (Task 8 Step 1) to spin up `NewMCPServer(tools.All()).Serve(ctx, inR, outW)` over an `io.Pipe` pair, write newline-delimited JSON-RPC, parse `mcpEnvelope{Result|Error}`, decode `Result.Content[0].Text` into `BindResult`. Now exercises `mcp_server.go:91, :220-249`. Test cleanly tears down by closing stdin and asserting `Serve` returns within 5s.
- **P2 — `writeSidecarForTest` did not exist in the codebase.** Replaced every reference in Task 13 and Task 14 with the real production helper `writeLoomMeta(base string, m loomMeta) error` (`multi-agent/pkg/agentbackend/codex/loommeta.go:37`) wrapped in `require.NoError(t, ...)`. `writeLoomMeta` silently no-ops on malformed input (`m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID == ""`), so every plan fixture now carries all four required fields — otherwise the helper returns nil but writes nothing and a downstream `applyLoomMeta` would read a missing sidecar.

### Third-round fixes

- **P2 — Task 13 sidecar base path.** `TestGetSession_CodexExecWithSidecar…` uses the `setTestHome(t, home)` pattern with the default-CodexHome resolution path: rollouts go to `home/.codex/sessions/…` and `EffectiveCodexHome` returns `filepath.Join(home, ".codex")` (`codexenv.go:28`). The previous plan wrote `writeLoomMeta(base, …)` without saying what `base` should be — an implementer reading "base" as `home` would put the sidecar at `home/loom-meta/…` while `applyLoomMeta` reads from `home/.codex/loom-meta/…`, and the assertion would pass falsely (because the gate-2 fail-closed returns User, matching the pre-fix behavior, only if the test asserts AgentTask — which it does — it would FAIL silently for the wrong reason). Pseudo-shape rewritten to declare `codexHome := filepath.Join(home, ".codex")` and call `writeLoomMeta(codexHome, …)`. Added an explicit "two pitfalls" note distinguishing this case from the sibling `TestListSessionsMergesLoomMetaSidecar` shape (which sets `cfg.CodexHome = home` and writes both rollout + sidecar directly under `home`).
- **P3 — Task 8 substring assertion was loose.** Substring matches `Contains(sc, "loom_origin")` + `Contains(sc, uuid)` would still pass if the marker's JSON shape drifted to a stale schema. Replaced with `agentbackend.ParseLoomOrigin(sc)` (`pkg/agentbackend/loomorigin.go:57`), asserting `ok == true`, `parent.SessionID == boundID`, `parent.AgentID == "drv-int"`, `parent.DisplayName == "int-driver"` — pins the full wire shape per spec line 735.

### Fourth-round fixes

- **P1 — Path B parent-link unbind was uncovered.** The previous matrix entry "covered via existing chat-route tests once migrated" was wrong: migrated tests add `BindThread` to satisfy the new guard, so they can't prove the unbound-fails contract for Path B. Added `TestSubmitContractTaskPathB_ChatSkill_FailsWithoutBindThread` to Task 7: single chat slave → route falls into Path B `selectTarget` (not driver_fanout) → no `BindThread` → asserts the actionable bind error, `DelegateTask` never called, AND zero observer requests (with full relay wiring per the T6 `_NoObserverSideEffect` pattern so the assertion is not vacuous).
- **P2 — Task 13 intermediate-state breakage understated.** Task 13 alone breaks three more sidecar-merge tests beyond the renamed `…WithSidecar…`: `TestListSessionsMergesLoomMetaSidecar` (`:348`), `TestListCacheInvalidatedBySidecarRewrite` (`:447`), `TestListSessionsLoomMetaMergesPartialParentLink` (`:737`). The legacy `applyLoomMeta` gate `sess.Origin != AgentTask` (`sessions.go:345`) blocks the merge once Task 13 stops promoting exec rollouts to `AgentTask`. Added an Intermediate-state warning at the top of Task 13 listing two options (skip-and-unskip vs atomic Q2 commit), a per-test breakage table, an explicit Step 6 that adds `t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")` to the three sidecar tests, and a Step 8 commit message that documents the temporary skips. Task 14 Step 2 rewritten to un-skip all four tests in lockstep (with a `grep` invariant asserting zero "Task 14" skip markers survive the commit) so PR #31 ships green. `TestListSessionsSidecarDoesNotRelabelUserSession` (`:390`) stays unskipped — verified that gate-2 fail-closed keeps it green throughout.

### Fifth-round fixes

- **P2 — Task 13 / Task 14 skip-marker drift.** The renamed `TestGetSession_CodexExecWithSidecar…` test in Task 13 Step 2 used a different marker string (`"…applyLoomMeta exec-mode gate"`) than the three sidecar-merge tests in Task 13 Step 6 (`"…applyLoomMeta sidecar+exec gate"`). Task 14 Step 2's grep matched only the latter, so the renamed test's skip would have survived into the Q2 commit — silently dropping coverage of the exec-rollout-with-sidecar happy path from PR #31. Unified all four skip markers on the single string `"enabled by Task 14 — applyLoomMeta sidecar+exec gate"`. Task 14 Step 2's `grep -n 'enabled by Task 14 — applyLoomMeta sidecar+exec gate' …` now hits exactly 4 lines (one per test) and the post-commit invariant (`grep | wc -l == 0`) catches any straggler.

## Pre-existing Worktree State (Triage)

The worktree contained an uncommitted bump of `multi-agent/go.mod` from `github.com/agentserver/agentserver v0.48.1 → v0.69.9`. The operator chose option (2): land the bump as Task 0 of this plan so PR #31's commit story stays coherent and the entire test surface validates against the new version before Q1/Q2 changes start.

**Resolution (Task 0, see below):** keep the bump, run `go mod tidy && go test ./...` to prove the worktree is green at the new version, refresh the stale "version selected by agentserver v0.48.1" comment in `go.mod`, commit as `chore(deps): bump agentserver v0.48.1 → v0.69.9`. Global Constraints amended to allow the bump as a transitive maintenance update.

## Global Constraints

- **No new Go dependencies for the Q1/Q2 fixes themselves.** Earlier drafts proposed `github.com/shirou/gopsutil/v3`; with `bind_thread` the driver does not introspect processes from Go. Note: PR #31 also carries an agentserver version bump (`v0.48.1 → v0.69.9`) landed by Task 0 — that bump is a transitive maintenance update needed for the worktree to build cleanly; it does NOT add new direct dependencies or new APIs consumed by Q1/Q2 code, and full `go test ./...` passes at the bumped version before any Q1/Q2 work starts.
- **No upstream codex changes.** Both fixes work against the codex binary as it ships today.
- **Backend-native naming.** In new code use "Codex thread_id" or "backend-native session id"; reserve `cse_…` for agentserver bridge ids (per #29). The `thread_id` parameter on `bind_thread` carries the backend-native semantic and is validated against `^[A-Za-z0-9._-]{1,128}$` so non-UUID opaque ids (other backends, tests) are accepted.
- **Fail explicitly, never silently mis-attribute.** Unbound parent-link submissions return an actionable error before any observable side effect (no token registration, no audit row, no observer artifact, no `SaveResourceSnapshot`). Non-parent-link submissions continue working without bind.
- **No `cwd`-based heuristics, no "newest thread" guesses.** Discovery uses only signals that uniquely identify the parent codex process: `$CODEX_THREAD_ID` env in the shell-tool subprocess, or `/proc/<ppid>/fd` exact-UUID match under a canonicalized `$CODEX_HOME/sessions/` prefix on Linux. Multiple candidates → exit 2 with the candidate list; no candidate → exit 1; the SKILL.md instructs the model to ask the user for `/status` thread_id.
- **Platform support, v1.** Linux: both discovery paths. macOS / WSL / Git Bash: env path works, `/proc` no-ops, manual `/status` fallback. Native Windows (cmd / PowerShell, no bash): script is **not invoked**; SKILL.md instructs the model to skip Step 1 and ask the user directly. A `.ps1` sibling is an explicit follow-up, not in this plan.
- **`SKILL.md` ⇄ `discover-thread.sh` are byte-identical.** The CI gate `skill_md_inline_in_sync_test.sh` extracts the heredoc body from `SKILL.md` and diffs it against the script file; any drift fails CI.
- **Capture-and-pass parent thread id.** Every tool call MUST `Load` the bound thread id exactly once at the top (via `requireBoundThread()`) and pass it through to `BuildLoomOrigin`. A second `Load` mid-call would race with a concurrent `bind_thread` invocation (the MCP server executes tools concurrently — see `internal/driver/mcp_server.go:79,237`).
- **Pre-P1 historical exec rollouts shift from AgentTask → User.** Accepted semantic correction; called out in Q2's classification matrix.
- **Three commits, all in PR #31.** `feat(driver): bind_thread MCP tool + submit/resume/contract guards (#24)`, `feat(skills): discover-thread.sh + SKILL.md init step for multiagent (#24)`, `fix(codex): loom-meta sidecar confirms agent_task only for exec rollouts (#24)`. Each commit ships its own tests.

## File Structure

### Created

- `multi-agent/internal/driver/bind_thread_tool.go` — `bindThreadTool` MCP wrapper. Tiny file (one struct + four methods) so the Go-API `BindThread` (in `tools.go`) and the MCP-wire surface stay in lockstep. Pairs with `tools_bind_thread_test.go`.
- `multi-agent/internal/driver/tools_bind_thread_test.go` — unit tests for `BindThread` (validator, idempotency, replace) AND the mandatory non-gated `All()`-registration test that catches the "method exists but tool not wired into MCP" failure mode.
- `multi-agent/internal/driver/bind_thread_integration_test.go` — env-gated (`LOOM_BIND_THREAD_INTEGRATION=1`) stdio MCP round-trip.
- `skills/multiagent/scripts/discover-thread.sh` — canonical discovery script. Single bash file; the source of truth that the SKILL.md heredoc is diffed against.
- `skills/multiagent/scripts/discover-thread_test.sh` — bats-shape bash tests (CI-runnable as plain shell). Linux-only `/proc` cases skip elsewhere.
- `skills/multiagent/scripts/skill_md_inline_in_sync_test.sh` — drift gate that asserts the inline heredoc body matches the script byte-for-byte.

### Modified

- `multi-agent/internal/driver/tools.go` — add `parentThread atomic.Pointer[string]` to `Tools`, add `BindResult`, `BindThread`, `requireBoundThread`; **delete** `loomOriginMarker()`; register `&bindThreadTool{t}` in `All()`; in `submitTaskTool.Call` move the bind guard ahead of all side effects and use captured `parentThreadID` in the `BuildLoomOrigin` call; in `resumeTaskTool.Call` add the same unconditional guard and replace the `r.t.loomOriginMarker()` call site.
- `multi-agent/internal/driver/contract_tools.go` — reorder `submitContractTaskTool.Call`: keep pure / read-only steps before the guard, move `observerRelay().SaveResourceSnapshot(...)` to **after** it, guard both Path A (driver_fanout early return) and Path B (selectTarget); build `loom_origin` inline from `parentThreadID`.
- `multi-agent/internal/driver/tools_test.go` — migrate ~6 loom-origin tests from `writeLoomOriginCurrentFile` to `BindThread`; delete the helper; add narrowed-guard semantic tests; add the `DriverFanoutFailWithoutBind_NoObserverSideEffect` test that records observer requests.
- `multi-agent/pkg/agentbackend/codex/sessions.go` — `applyCodexSessionMeta` drops the `codex_exec || source=exec → AgentTask` branch and defaults to `User`. `applyLoomMeta` grows a `rolloutIsExec bool` parameter and the two gates (Subagent priority, exec-mode requirement). `scanCodexSession` declares `var rolloutIsExec bool` outside the read loop and OR-accumulates inside the `session_meta` case.
- `multi-agent/pkg/agentbackend/codex/sessions_test.go` — rename / fix the codex_exec assertion test, add five new tests covering matrix rows + the OR-accumulator semantics. Three existing sidecar-merge tests (`TestListSessionsMergesLoomMetaSidecar` `:348`, `TestListCacheInvalidatedBySidecarRewrite` `:447`, `TestListSessionsLoomMetaMergesPartialParentLink` `:737`) are TEMPORARILY skipped in Task 13's commit and un-skipped in Task 14's commit (the new `applyLoomMeta` exec-mode gate restores the sidecar-merge path). `TestListSessionsSidecarDoesNotRelabelUserSession` (`:390`) stays green throughout — no skip needed. Final post-Task-14 invariant: zero `t.Skip` lines mentioning "Task 14" survive.
- `skills/multiagent/SKILL.md` — insert a new `## Initialization` section before `## Default Workflow` containing the inline heredoc, the native-Windows manual-fallback note, and the `/resume` re-bind guidance.
- `multi-agent/tests/prod_test/` driver-side skill instructions — call `bind_thread` first (becomes part of the live e2e fixture).

### Deleted

- `multi-agent/internal/driver/tools.go::loomOriginMarker()` — the method is fully removed; both callers (`tools.go:571`, `tools.go:1229`, `contract_tools.go:79`, `contract_tools.go:102`) migrate to inline `agentbackend.BuildLoomOrigin(...)` with the locally-captured `parentThreadID`.
- `multi-agent/internal/driver/tools_test.go::writeLoomOriginCurrentFile` — removed after all five callers migrate to `BindThread`.

### Out of scope (explicit follow-ups, not in this plan)

- `skills/multiagent/scripts/discover-thread.ps1` — native-Windows discovery; tracked as a separate follow-up.
- Removing `codex.ReadCurrentSession` entirely — after this plan it has no driver-side callers, but the slave executor still uses `writeCurrentSession`; the dead-read cleanup is a separate small PR.
- Refactoring `selectTarget` to accept the already-discovered `cards` slice (avoiding the second `DiscoverAgents` RPC). Acknowledged duplicate read-only call; not in this PR's scope.

---

### Task 1: BindThread core — `Tools` field, validator, and direct-call API

This task lands the Go-only surface that unit tests drive directly. The MCP wrapper comes in Task 2; splitting them lets us prove the semantics (validator, idempotency, replace) without going through JSON-RPC.

**Files:**
- Modify: `multi-agent/internal/driver/tools.go` (struct field + new methods + new imports)
- Create: `multi-agent/internal/driver/tools_bind_thread_test.go`

**Interfaces:**
- Consumes: nothing — this is the foundational layer.
- Produces:
  - `type BindResult struct { Bound bool; ThreadID, AgentID, DisplayName string }` (JSON tags `bound`, `thread_id`, `agent_id`, `display_name`)
  - `func (t *Tools) BindThread(ctx context.Context, threadID string) (BindResult, error)`
  - `validThreadIDPattern = "^[A-Za-z0-9._-]{1,128}$"` exported as a package-level constant (so the schema string in Task 2 and the error message stay in lockstep).
  - `Tools.parentThread atomic.Pointer[string]` field. `*Tools` callers do NOT touch this directly — read via `requireBoundThread()` (Task 3).

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/internal/driver/tools_bind_thread_test.go`:

```go
package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBindThread_SetsAndStores verifies a valid call mutates parentThread and
// returns the expected BindResult (carrying the configured agent_id + display_name).
func TestBindThread_SetsAndStores(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "" /*codexHome*/, "drv-1", "prod-driver")
	res, err := tools.BindThread(context.Background(), "019ef3bd-42c8-7731-85b7-7177ae747389")
	require.NoError(t, err)
	require.Equal(t, BindResult{
		Bound:       true,
		ThreadID:    "019ef3bd-42c8-7731-85b7-7177ae747389",
		AgentID:     "drv-1",
		DisplayName: "prod-driver",
	}, res)
	p := tools.parentThread.Load()
	require.NotNil(t, p)
	require.Equal(t, "019ef3bd-42c8-7731-85b7-7177ae747389", *p)
}

// TestBindThread_RejectsInvalidFormat covers every shape the validator must
// reject. None of these may mutate parentThread.
func TestBindThread_RejectsInvalidFormat(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"empty", ""},
		{"whitespace_only", "   "},
		{"contains_dollar", "thread$id"},
		{"contains_brace_open", "thread{id"},
		{"contains_brace_close", "thread}id"},
		{"contains_slash", "thread/id"},
		{"contains_newline", "thr\nid"},
		{"unexpanded_placeholder", "${CODEX_THREAD_ID}"},
		{"too_long", strings.Repeat("a", 129)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-X", "driver-X")
			res, err := tools.BindThread(context.Background(), tc.in)
			require.Error(t, err, "input %q must be rejected", tc.in)
			require.Contains(t, err.Error(), "invalid thread_id format")
			require.Equal(t, BindResult{}, res)
			require.Nil(t, tools.parentThread.Load(), "parentThread must not be set on rejection")
		})
	}
}

// TestBindThread_Idempotent_SameValue calls with the same id twice; the second
// call must succeed and not change observable state.
func TestBindThread_Idempotent_SameValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	_, err = tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	require.Equal(t, "thr-1", *tools.parentThread.Load())
}

// TestBindThread_Replaces_DifferentValue codifies the /resume semantic: a
// later bind_thread with a NEW value replaces the previously-bound id.
func TestBindThread_Replaces_DifferentValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	_, err = tools.BindThread(context.Background(), "thr-2")
	require.NoError(t, err)
	require.Equal(t, "thr-2", *tools.parentThread.Load())
}

// TestBindResult_JSONTags pins the wire-shape the MCP wrapper relies on.
func TestBindResult_JSONTags(t *testing.T) {
	b, err := json.Marshal(BindResult{Bound: true, ThreadID: "t", AgentID: "a", DisplayName: "d"})
	require.NoError(t, err)
	require.JSONEq(t, `{"bound":true,"thread_id":"t","agent_id":"a","display_name":"d"}`, string(b))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestBindThread|TestBindResult' -v`
Expected: FAIL — `tools.BindThread undefined`, `BindResult undefined`, `tools.parentThread undefined`.

- [ ] **Step 3: Add the field, type, and method to `tools.go`**

At the top of `multi-agent/internal/driver/tools.go`, extend the import block (add `regexp`, `sync/atomic`):

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/orchestration"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	"github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)
```

Add the field to the `Tools` struct (placed alphabetically near the existing single-purpose fields):

```go
type Tools struct {
	reg            *FileRegistry
	audit          *AuditLog
	taskJournal    *TaskJournal
	sdk            SDKClient
	cfg            *Config
	observer       ObserverSink
	relay          *ObserverRelay
	contractRunner ContractRunner
	parentThread   atomic.Pointer[string] // nil = not yet bound; set by BindThread
}
```

Add the validator constant, the `BindResult` type, and the `BindThread` method. Place this block immediately AFTER the existing `isParentLinkDelegation` (currently `tools.go:67`) and BEFORE the old `loomOriginMarker` (which Task 3 deletes):

```go
// validThreadIDPattern accepts opaque backend-native session ids:
//   - Codex emits UUIDv7 (e.g. 019ef3bd-42c8-7731-85b7-7177ae747389)
//   - Other backends or tests may pass non-UUID strings (e.g. "thr-parent")
// Rejects unexpanded placeholders (`${VAR}`), shell metachars, whitespace,
// newlines, slashes, and anything longer than 128 chars — values that would
// persist as broken links in loom-meta sidecars otherwise.
const validThreadIDPattern = `^[A-Za-z0-9._-]{1,128}$`

var validThreadID = regexp.MustCompile(validThreadIDPattern)

// BindResult is the structured response returned to MCP callers and direct
// Go callers. Marshaled by bindThreadTool.Call (see bind_thread_tool.go).
type BindResult struct {
	Bound       bool   `json:"bound"`
	ThreadID    string `json:"thread_id"`
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
}

// BindThread validates and stores the parent thread id. Tests call this
// directly; the MCP-facing bindThreadTool wraps it so the wire surface and
// direct-call surface stay in lockstep. ctx is reserved for future audit-log
// hooks; not used today (Store is non-blocking and never errors).
func (t *Tools) BindThread(_ context.Context, threadID string) (BindResult, error) {
	id := strings.TrimSpace(threadID)
	if !validThreadID.MatchString(id) {
		return BindResult{}, fmt.Errorf("invalid thread_id format: must match %s", validThreadIDPattern)
	}
	t.parentThread.Store(&id)
	return BindResult{
		Bound:       true,
		ThreadID:    id,
		AgentID:     t.cfg.Credentials.ShortID,
		DisplayName: t.cfg.Discovery.DisplayName,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestBindThread|TestBindResult' -v`
Expected: PASS (5 subtests under `TestBindThread_RejectsInvalidFormat` + 4 top-level tests).

- [ ] **Step 5: Run the full driver test suite as a regression guard**

Run: `cd multi-agent && go test ./internal/driver/...`
Expected: PASS for every previously-passing test. The new field is unused elsewhere and `loomOriginMarker` still exists (Task 3 deletes it), so nothing should regress.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/driver/tools.go \
        multi-agent/internal/driver/tools_bind_thread_test.go
git commit -m "feat(driver): add BindThread core (validator + parentThread field) (#24)"
```

### Task 2: `bindThreadTool` MCP wrapper + `All()` registration

This task makes `bind_thread` reachable from codex over the wire. The split (`BindThread` Go method in Task 1; `bindThreadTool` JSON-RPC wrapper here) keeps semantics and serialization testable in isolation. The mandatory **non-gated** registration test catches the failure mode where `BindThread` compiles but the wrapper is missing from `Tools.All()` — without it the env-gated integration test in Task 8 wouldn't catch the regression on routine CI runs.

**Files:**
- Create: `multi-agent/internal/driver/bind_thread_tool.go`
- Modify: `multi-agent/internal/driver/tools.go` (one new line in `All()`)
- Modify: `multi-agent/internal/driver/tools_bind_thread_test.go` (append registration + Call tests)

**Interfaces:**
- Consumes:
  - `Tool` interface from `internal/driver/mcp_server.go:18` (methods `Name() string`, `Description() string`, `InputSchema() json.RawMessage`, `Call(ctx, json.RawMessage) (json.RawMessage, error)`).
  - `MCPToolError` from `internal/driver/mcp_server.go:27` for wrapping non-MCP errors into JSON-RPC `-32000`.
  - `Tools.BindThread`, `BindResult`, `validThreadIDPattern` from Task 1.
- Produces: a registered tool whose `Name()` returns `"bind_thread"`, ready for Tasks 4/5/6 (their guards rely on the model being able to call it).

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/internal/driver/tools_bind_thread_test.go`:

```go
// TestBindThreadTool_RegisteredInAll is the non-gated regression for the
// "method exists but tool not wired into MCP" failure mode. If All() drops
// the registration, tools/list won't surface bind_thread and codex can't
// call it; tests that go through Tools.BindThread directly wouldn't notice.
func TestBindThreadTool_RegisteredInAll(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "" /*home*/, "drv-1", "prod-driver")

	var found Tool
	for _, tool := range tools.All() {
		if tool.Name() == "bind_thread" {
			found = tool
			break
		}
	}
	require.NotNil(t, found, "bind_thread missing from Tools.All() — wrapper not registered")

	// Schema sanity: must require thread_id.
	var schema struct {
		Required   []string                          `json:"required"`
		Properties map[string]map[string]interface{} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(found.InputSchema(), &schema))
	require.Contains(t, schema.Required, "thread_id")
	require.Contains(t, schema.Properties, "thread_id")

	// Round-trip Call: valid UUIDv7 must produce a BindResult-shaped response.
	raw, err := found.Call(context.Background(),
		json.RawMessage(`{"thread_id":"019ef3bd-42c8-7731-85b7-7177ae747389"}`))
	require.NoError(t, err)
	var got BindResult
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, BindResult{
		Bound:       true,
		ThreadID:    "019ef3bd-42c8-7731-85b7-7177ae747389",
		AgentID:     "drv-1",
		DisplayName: "prod-driver",
	}, got)
}

// TestBindThreadTool_Call_InvalidJSON exercises the json.Unmarshal error
// path: the wrapper must convert it into an MCPToolError so codex sees a
// clean JSON-RPC -32000 instead of a generic Go error string.
func TestBindThreadTool_Call_InvalidJSON(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	tool := findToolByName(t, tools, "bind_thread")
	_, err := tool.Call(context.Background(), json.RawMessage(`{not json}`))
	require.Error(t, err)
	var mcpErr *MCPToolError
	require.ErrorAs(t, err, &mcpErr, "wrapper must return *MCPToolError")
	require.Contains(t, mcpErr.Message, "invalid args")
}

// TestBindThreadTool_Call_ValidatorRejection ensures validator errors from
// the underlying BindThread are also rewrapped as MCPToolError.
func TestBindThreadTool_Call_ValidatorRejection(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	tool := findToolByName(t, tools, "bind_thread")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"thread_id":"${VAR}"}`))
	require.Error(t, err)
	var mcpErr *MCPToolError
	require.ErrorAs(t, err, &mcpErr)
	require.Contains(t, mcpErr.Message, "invalid thread_id format")
}

// findToolByName is a tiny local helper; if tools_test.go already exposes
// toolByName(...) just call that instead and delete this stub. Inlined here
// so Task 2 doesn't accumulate cross-file refactor noise.
func findToolByName(t *testing.T, tools *Tools, name string) Tool {
	t.Helper()
	for _, tool := range tools.All() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not registered", name)
	return nil
}
```

NOTE: `internal/driver/tools_test.go:109` already exports `submitContractToolForTest` and a similar `toolByName` helper. If `toolByName` exists, replace `findToolByName(t, tools, "bind_thread")` with `toolByName(t, tools, "bind_thread")` and delete the local stub before committing.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestBindThreadTool' -v`
Expected: FAIL — `bind_thread` not in `All()`; `bind_thread_tool.go` missing.

- [ ] **Step 3: Create `multi-agent/internal/driver/bind_thread_tool.go`**

```go
package driver

import (
	"context"
	"encoding/json"
	"fmt"
)

// bindThreadTool exposes Tools.BindThread over MCP. The Tools-level method
// owns validation and state; this wrapper only handles JSON unmarshalling
// and MCPToolError translation so wire errors are JSON-RPC -32000 instead
// of generic Go strings.
type bindThreadTool struct{ t *Tools }

func (b *bindThreadTool) Name() string { return "bind_thread" }

func (b *bindThreadTool) Description() string {
	return "Bind the driver MCP to its parent Codex thread_id. Must be " +
		"called once per session before any parent-link submission " +
		"(submit_task / submit_contract_task with skill chat / chat_resume / " +
		"fanout / fanout_strict / route, or resume_task). The multiagent " +
		"skill runs the discover-thread.sh script and passes its output here. " +
		"Calling with a new thread_id replaces the bound value (supports " +
		"/resume mid-session)."
}

func (b *bindThreadTool) InputSchema() json.RawMessage {
	// Schema string interpolates validThreadIDPattern (Task 1) so the wire
	// pattern and the validator stay in lockstep — no copy-paste drift.
	return json.RawMessage(fmt.Sprintf(`{
        "type":"object",
        "properties":{
            "thread_id":{
                "type":"string",
                "description":"Backend-native thread/session id of the parent agent. Codex's value is a UUIDv7 like 019ef3bd-42c8-7731-85b7-7177ae747389; the validator accepts any opaque id matching %s, so other backends or tests can pass non-UUID strings (e.g. 'thr-parent') as well.",
                "pattern":%q
            }
        },
        "required":["thread_id"]
    }`, validThreadIDPattern, validThreadIDPattern))
}

func (b *bindThreadTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	result, err := b.t.BindThread(ctx, args.ThreadID)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return json.Marshal(result)
}
```

- [ ] **Step 4: Register the tool in `Tools.All()`**

In `multi-agent/internal/driver/tools.go` modify the `All()` slice (currently `tools.go:176-200`). Place `&bindThreadTool{t}` directly before `&submitTaskTool{t}` so the bind tool sits next to the submission tools it guards (discoverability when reading `tools/list`):

```go
func (t *Tools) All() []Tool {
	return []Tool{
		&listAgentsTool{t},
		&listDriverTasksTool{t},
		&inspectCapabilitiesTool{t},
		&runSlaveBashTool{t},
		&runSlavePowerShellTool{t},
		&runSlaveShellTool{t},
		&registerSlaveMCPTool{t},
		&unregisterSlaveMCPTool{t},
		&getSlaveClaudePermissionsTool{t},
		&updateSlaveClaudePermissionsTool{t},
		&readSlaveFileTool{t},
		&writeSlaveFileTool{t},
		&statSlaveFileTool{t},
		&draftTaskContractTool{t},
		&dryRunContractTool{t},
		&bindThreadTool{t}, // ← NEW: must be registered for codex tools/list to see it
		&submitTaskTool{t},
		&submitContractTaskTool{t},
		&getTaskTool{t},
		&waitTaskTool{t},
		&resumeTaskTool{t},
		&tailSubtasksTool{t},
		&cancelTaskTool{t},
	}
}
```

- [ ] **Step 5: Run the targeted tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestBindThreadTool' -v`
Expected: PASS for `_RegisteredInAll`, `_Call_InvalidJSON`, `_Call_ValidatorRejection`.

- [ ] **Step 6: Run the full driver test suite**

Run: `cd multi-agent && go test ./internal/driver/...`
Expected: PASS. The new tool only adds an entry to `All()`; nothing else depends on the order.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/driver/bind_thread_tool.go \
        multi-agent/internal/driver/tools.go \
        multi-agent/internal/driver/tools_bind_thread_test.go
git commit -m "feat(driver): expose bind_thread as MCP tool + registration test (#24)"
```

### Task 3: `requireBoundThread` helper + delete `loomOriginMarker()`

This task does two coupled changes that MUST happen together: add the capture helper that subsequent tasks call, and delete the implicit-Load `loomOriginMarker()` method that would otherwise let a developer accidentally re-introduce the race. After this task the codebase will not compile until Tasks 4/5/6 migrate the call sites — that's intentional, and the steps below explicitly route through "make it red" → "make it green" so the compile error is visible to the reviewer.

**Files:**
- Modify: `multi-agent/internal/driver/tools.go` (add `requireBoundThread`, delete `loomOriginMarker`)
- Modify: `multi-agent/internal/driver/tools.go` (`tools.go:571`) — replace `loomOriginMarker()` call in `submitTaskTool.Call`
- Modify: `multi-agent/internal/driver/tools.go` (`tools.go:1229`) — replace `loomOriginMarker()` call in `resumeTaskTool.Call`
- Modify: `multi-agent/internal/driver/contract_tools.go` (`contract_tools.go:79`, `:102`) — replace both `loomOriginMarker()` calls in `submitContractTaskTool.Call`
- Modify: `multi-agent/internal/driver/tools_test.go` — temporarily seed every existing loom-origin test that called `writeLoomOriginCurrentFile` with a `BindThread` call too (Task 4 finishes the migration by deleting the helper)
- Add: `multi-agent/internal/driver/tools_bind_thread_test.go` — `TestRequireBoundThread_*`

**Interfaces:**
- Consumes: `Tools.parentThread`, `Tools.cfg.Credentials.ShortID`, `Tools.cfg.Discovery.DisplayName` (Task 1); `agentbackend.BuildLoomOrigin(shortID, displayName, sessionID string) string` (existing, `pkg/agentbackend/loomorigin.go`).
- Produces:
  - `func (t *Tools) requireBoundThread() (string, error)` — returns the captured thread id or a long actionable error message.
  - `loomOriginMarker()` no longer exists; every call site builds the marker inline with the captured id.

The capture-and-pass invariant: **one `Load` per tool call, near the top, value threaded through to the `BuildLoomOrigin` call site as a local variable.** Re-loading later would race with a concurrent `bind_thread` and could stamp a different parent on the in-flight call than the bind guard validated.

- [ ] **Step 1: Write the failing tests for `requireBoundThread`**

Append to `multi-agent/internal/driver/tools_bind_thread_test.go`:

```go
// TestRequireBoundThread_UnboundReturnsActionableError covers the path the
// model will see when the skill skipped Step 1 of Initialization.
func TestRequireBoundThread_UnboundReturnsActionableError(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	id, err := tools.requireBoundThread()
	require.Error(t, err)
	require.Empty(t, id)
	// The message MUST mention bind_thread AND the script — the model
	// uses both keywords to recover.
	require.Contains(t, err.Error(), "bind_thread")
	require.Contains(t, err.Error(), "discover-thread.sh")
}

// TestRequireBoundThread_ReturnsCapturedValue is the success path.
func TestRequireBoundThread_ReturnsCapturedValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	id, err := tools.requireBoundThread()
	require.NoError(t, err)
	require.Equal(t, "thr-1", id)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run TestRequireBoundThread -v`
Expected: FAIL — `requireBoundThread undefined`.

- [ ] **Step 3: Add `requireBoundThread` and delete `loomOriginMarker` in `tools.go`**

Locate the existing `loomOriginMarker()` (currently `multi-agent/internal/driver/tools.go:76-82`). Replace the whole method with `requireBoundThread`:

```go
// requireBoundThread returns the parent codex thread id captured by a prior
// bind_thread MCP call. Caller MUST invoke this exactly once at the top of
// a tool call, BEFORE any side effects, then thread the returned id through
// to BuildLoomOrigin as a local variable.
//
// A second Load mid-call would race with a concurrent bind_thread (MCP
// server handles requests concurrently — internal/driver/mcp_server.go:79,237).
// The capture-and-pass pattern guarantees a single tool invocation sees one
// consistent thread id even if the model rebinds mid-flight.
//
// The error message is intentionally long: when the model sees it, it should
// know both the failure mode AND the recovery (run discover-thread.sh and
// call bind_thread). Keep both keywords ("bind_thread", "discover-thread.sh")
// in the message — the multiagent SKILL.md tells the model to look for them.
func (t *Tools) requireBoundThread() (string, error) {
	p := t.parentThread.Load()
	if p == nil {
		return "", fmt.Errorf(
			"driver not bound to a codex thread; call `bind_thread` first " +
				"(the multiagent skill runs scripts/discover-thread.sh as " +
				"its first init step)")
	}
	return *p, nil
}
```

`loomOriginMarker()` is now **deleted**. The `codex.EffectiveCodexHome`/`codex.ReadCurrentSession` imports inside it are no longer used by `tools.go`; if `go vet` complains about unused imports, prune the bare `"github.com/yourorg/multi-agent/pkg/agentbackend/codex"` import too. (Re-check after Task 4/5/6 because they might still need it for unrelated reasons — most likely not.)

- [ ] **Step 4: Make all four old call sites compile by inlining `BuildLoomOrigin`**

The build is now red — four places reference the deleted method. Patch them to use the captured `parentThreadID` pattern. Tasks 4/5/6 add the FULL guard machinery (early fail-fast, side-effect reordering, contract-route plumbing); this step is the minimum required to restore compilation. Each call site becomes:

In `multi-agent/internal/driver/tools.go::submitTaskTool.Call` (currently `:569-575`):

```go
// Before:
//   systemContext := ""
//   if isParentLinkDelegation(skill) {
//       if m := s.t.loomOriginMarker(); m != "" {
//           systemContext = m
//       }
//   }
// After (temporary — Task 4 will move the requireBoundThread call to the
// top of Call() and fail-fast there; this preserves the LEGACY best-effort
// semantics just long enough to keep the tree compiling):
systemContext := ""
if isParentLinkDelegation(skill) {
	if pid, err := s.t.requireBoundThread(); err == nil {
		systemContext = agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			pid,
		)
	}
}
```

In `multi-agent/internal/driver/tools.go::resumeTaskTool.Call` (currently `:1229`):

```go
// Before:
//   SystemContext: r.t.loomOriginMarker(),
// After (temporary):
sysCtx := ""
if pid, err := r.t.requireBoundThread(); err == nil {
	sysCtx = agentbackend.BuildLoomOrigin(
		r.t.cfg.Credentials.ShortID,
		r.t.cfg.Discovery.DisplayName,
		pid,
	)
}
// ... use sysCtx in the DelegateTask call:
resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
	TargetID:       info.TargetID,
	Skill:          "chat_resume",
	Prompt:         string(body),
	SystemContext:  sysCtx,
	TimeoutSeconds: timeout,
})
```

In `multi-agent/internal/driver/contract_tools.go` (currently `:79` Path A driver_fanout and `:102` Path B):

```go
// Path A (line 79) — Before:
//   result, err := s.t.contractRunner.Run(ctx, finalPrompt, s.t.loomOriginMarker())
// After (temporary):
marker := ""
if pid, err := s.t.requireBoundThread(); err == nil {
	marker = agentbackend.BuildLoomOrigin(
		s.t.cfg.Credentials.ShortID,
		s.t.cfg.Discovery.DisplayName,
		pid,
	)
}
result, err := s.t.contractRunner.Run(ctx, finalPrompt, marker)

// Path B (line 102) — Before:
//   systemContext := ""
//   if isParentLinkDelegation(skill) {
//       if m := s.t.loomOriginMarker(); m != "" {
//           systemContext = m
//       }
//   }
// After (temporary):
systemContext := ""
if isParentLinkDelegation(skill) {
	if pid, err := s.t.requireBoundThread(); err == nil {
		systemContext = agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			pid,
		)
	}
}
```

The temporary `err == nil` shape preserves the OLD best-effort semantics (no bind → empty marker, not an error). Tasks 4/5/6 will replace these with **fail-fast** guards at the top of each Call(); doing both in one task would intertwine reordering with the deletion and make review harder.

- [ ] **Step 5: Bridge the existing loom-origin tests so they don't go red**

The five existing tests at `multi-agent/internal/driver/tools_test.go:2265,2316,2375,2408,2706` all call `writeLoomOriginCurrentFile` and expect a marker stamped. With `loomOriginMarker` gone they currently produce an empty marker (because `parentThread` is nil). Add a `BindThread` call to each so they keep passing under the temporary best-effort shape from Step 4. The thread id passed to `BindThread` MUST equal the value the test currently writes to the CODEX_HOME marker file — that's the value the existing assertion compares against.

```go
// Pattern: at every site that calls writeLoomOriginCurrentFile(t, "thr-X"),
// add:
//   _, err := tools.BindThread(context.Background(), "thr-X")
//   require.NoError(t, err)
// IMMEDIATELY AFTER newLoomTestTools(...). The writeLoomOriginCurrentFile
// call stays for now — Task 4 removes the helper and the call.
```

Exact sites:
- `tools_test.go:2271-2272` — `TestSubmitTaskDefaultStampsLoomOrigin` → `BindThread(ctx, markerSessID)`
- `tools_test.go:2317` — `TestSubmitTaskChatStampsLoomOrigin` → `BindThread(ctx, "thr-chat")`
- `tools_test.go:2347` — `TestSubmitTaskBashDoesNotStampLoomOrigin` → `BindThread(ctx, "thr-bash")` (still asserts marker absent because bash is not parent-link; the bind just keeps the test stable)
- `tools_test.go:2376` — `TestSubmitContractTaskStampsLoomOrigin` → `BindThread(ctx, "thr-contract")`
- `tools_test.go:2409` — `TestResumeTaskStampsLoomOrigin` → `BindThread(ctx, "thr-resume")`
- `tools_test.go:2706` — `TestSubmitContractTaskDriverFanoutCarriesLoomOrigin` → `BindThread(ctx, "thr-fanout")`

Also patch any other existing test that currently routes through a parent-link skill (default `fanout`, `chat`, etc.) without `writeLoomOriginCurrentFile`. The temporary best-effort shape means those still pass (empty SystemContext) — Task 7 does the full audit; here we only need the tree green.

- [ ] **Step 6: Build, vet, and run tests**

Run: `cd multi-agent && go build ./... && go vet ./... && go test ./internal/driver/...`
Expected: PASS. Compilation succeeds (`loomOriginMarker` calls all gone), `go vet` clean (no unused imports), every previously-passing test in `internal/driver/` still passes.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/driver/tools.go \
        multi-agent/internal/driver/contract_tools.go \
        multi-agent/internal/driver/tools_test.go \
        multi-agent/internal/driver/tools_bind_thread_test.go
git commit -m "refactor(driver): replace loomOriginMarker with requireBoundThread capture (#24)"
```

### Task 4: `submit_task` fail-fast guard + capture-and-pass

Replace the temporary best-effort shape from Task 3 with the production fail-fast guard. The guard MUST run before any observable side effect (token registration, write registration, observer artifact creation, audit log, observer write upload) so an unbound parent-link submission leaks nothing. Non-parent-link skills (`bash`, `powershell`, `run_slave_shell`, etc.) skip the guard entirely — they don't stamp `loom_origin` and don't need a bound thread.

**Files:**
- Modify: `multi-agent/internal/driver/tools.go::submitTaskTool.Call` (currently `:435-`); move guard to top, replace temporary best-effort block from Task 3 with the captured-id pass-through.
- Modify: `multi-agent/internal/driver/tools_test.go` — finish migrating the loom-origin tests: delete the `writeLoomOriginCurrentFile` calls left over from Task 3, then delete the helper function itself. Add new narrowed-guard tests.

**Interfaces:**
- Consumes: `requireBoundThread()` (Task 3), `isParentLinkDelegation(skill string) bool` (`tools.go:67`), `agentbackend.BuildLoomOrigin` (existing).
- Produces: an unbound `submit_task` call with skill ∈ parent-link set returns `&MCPToolError{Message: "driver not bound to a codex thread; ..."}` and registers nothing in `FileRegistry`, writes nothing to `AuditLog`, creates no observer artifact.

- [ ] **Step 1: Write the new failing tests**

Add to `multi-agent/internal/driver/tools_test.go` (next to the existing loom-origin tests around `:2265`):

```go
// TestSubmitTask_ChatSkill_FailsWithoutBindThread codifies the fail-fast
// guard: a parent-link submission with read_paths AND write_paths
// supplied returns the actionable error before any token / audit / observer
// side effect. We MUST supply paths here — without them the registration /
// audit / observer-artifact code wouldn't run anyway and the assertion
// would be vacuous (a misplaced guard would still pass). Picking a real
// readable file and a writable parent-dir target makes the assertion meaningful.
func TestSubmitTask_ChatSkill_FailsWithoutBindThread(t *testing.T) {
	delegated := false
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave", DisplayName: "slave", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "should-not-reach"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*home*/, "drv-1", "d1")
	// NO BindThread call — guard must reject.

	// Build a real read_path (a temp file) and a write_path (target under
	// the configured WorkDir). Both routes through the manifest / audit /
	// observer-write registration code, so any misplaced guard would leave
	// observable state and fail the assertion below.
	readPath := filepath.Join(t.TempDir(), "input.txt")
	require.NoError(t, os.WriteFile(readPath, []byte("hi"), 0o644))
	writePath := filepath.Join(tools.cfg.DriverDefaults.WorkDir, "out.txt")

	auditSizeBefore, err := os.Stat(filepath.Join(tools.cfg.DriverDefaults.AuditLogDir, "audit.log"))
	require.NoError(t, err)

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"prompt":              "hi",
		"skill":               "chat",
		"target_display_name": "slave",
		"read_paths":          []string{readPath},
		"write_paths":         []map[string]interface{}{{"path": writePath, "overwrite": true}},
	})
	_, err = toolByName(t, tools, "submit_task").Call(context.Background(), argsJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.False(t, delegated, "DelegateTask must NOT be called when bind missing")

	// 1) FileRegistry must contain no blob, no write token, no observer
	//    artifact. snapshotForTest below returns the union of all live maps.
	snap := tools.reg.snapshotForTest()
	require.Equal(t, 0, snap.Blobs, "no blobs registered")
	require.Equal(t, 0, snap.Writes, "no write tokens registered")
	require.Equal(t, 0, snap.ObserverArtifacts, "no observer artifacts registered")

	// 2) AuditLog file must not have grown (no register_read /
	//    register_write entries appended).
	auditSizeAfter, err := os.Stat(filepath.Join(tools.cfg.DriverDefaults.AuditLogDir, "audit.log"))
	require.NoError(t, err)
	require.Equal(t, auditSizeBefore.Size(), auditSizeAfter.Size(),
		"audit log must not grow when bind guard short-circuits")
}

// TestSubmitTask_BashSkill_SucceedsWithoutBindThread codifies the narrowed-
// guard semantic: bash is not parent-link, so submit_task runs without bind
// AND systemContext stays empty (not stamped with a stale marker).
func TestSubmitTask_BashSkill_SucceedsWithoutBindThread(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-bash", DisplayName: "slave-bash", Status: "available",
					Card: json.RawMessage(`{"skills":["bash"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-bash"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "", "drv-1", "d1")
	// NO BindThread call.

	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"run","skill":"bash","target_display_name":"slave-bash"}`))
	require.NoError(t, err)
	require.Equal(t, "bash", captured.Skill)
	require.Empty(t, captured.SystemContext,
		"bash submissions must NOT carry a loom_origin marker even after Q1")
}

// TestSubmitTask_FanoutDefault_FailsWithoutBindThread covers the empty-skill
// case (defaults to fanout, which IS parent-link).
func TestSubmitTask_FanoutDefault_FailsWithoutBindThread(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m", DisplayName: "master", Status: "available",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}, "", "drv-1", "d1")
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"hi"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
}
```

The audit-log file-size delta + the registry snapshot together cover the spec's "no token / no audit row / no observer artifact" contract. The audit-log assertion is direct because `newTestTools` writes the audit file under a known `cfg.DriverDefaults.AuditLogDir` (see `tools_test.go:80`).

Add a `snapshotForTest` helper to `multi-agent/internal/driver/registry.go`. The real `FileRegistry` has separate maps for `blobs`, `dirs`, `writes`, `observerArtifacts` (see `registry.go:32`) — return counts for each so the test asserts each surface independently:

```go
// FileRegistrySnapshot is a count-of-each-bucket view used by tests to
// assert "no side effects" without touching internal fields.
type FileRegistrySnapshot struct {
	Blobs             int
	Dirs              int
	Writes            int
	ObserverArtifacts int
}

// snapshotForTest returns bucket counts for the live registry. Test-only;
// kept lowercase to stay package-private.
func (r *FileRegistry) snapshotForTest() FileRegistrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return FileRegistrySnapshot{
		Blobs:             len(r.blobs),
		Dirs:              len(r.dirs),
		Writes:            len(r.writes),
		ObserverArtifacts: len(r.observerArtifacts),
	}
}
```

If the implementation finds a similar accessor already exists (none today — verified against `multi-agent/internal/driver/registry.go:32` field set: `blobs`, `blobMeta`, `dirs`, `writes`, `dirSHA`, `observerArtifacts`), reuse it and adjust the assertion shape.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestSubmitTask_.*WithoutBindThread|TestSubmitTask_BashSkill_SucceedsWithoutBindThread|TestSubmitTask_FanoutDefault_FailsWithoutBindThread' -v`

Expected: FAIL — under Task 3's best-effort shape the bash/chat tests pass but `TestSubmitTask_ChatSkill_FailsWithoutBindThread` and the fanout default test expect an error that doesn't happen yet.

- [ ] **Step 3: Move the guard to the top of `submitTaskTool.Call`**

In `multi-agent/internal/driver/tools.go::submitTaskTool.Call`, after the existing skill defaulting + `jsonPromptSkill` check (currently around `:454-461`) and BEFORE the `manifest := Manifest{}` block (`:463`), insert the capture-and-fail-fast guard. Then at the existing `systemContext` block (currently `:569-575`, patched temporarily in Task 3), use the captured `parentThreadID`:

```go
func (s *submitTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Prompt     string   `json:"prompt"`
		ReadPaths  []string `json:"read_paths"`
		WritePaths []struct {
			Path      string `json:"path"`
			Overwrite bool   `json:"overwrite"`
		} `json:"write_paths"`
		TargetDisplayName string `json:"target_display_name"`
		Skill             string `json:"skill"`
		TimeoutSec        int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Prompt == "" {
		return nil, &MCPToolError{Message: "prompt is required"}
	}

	skill := args.Skill
	if skill == "" {
		skill = "fanout"
	}
	if jsonPromptSkill(skill) && (len(args.ReadPaths) > 0 || len(args.WritePaths) > 0) {
		return nil, &MCPToolError{
			Message: "skill " + skill +
				" takes JSON-only prompts; read_paths/write_paths cannot be conveyed",
		}
	}

	// === NEW: capture-and-fail-fast guard ===
	// Runs BEFORE manifest construction, RegisterFile, RegisterWrite,
	// observer artifact creation, AuditLog writes. An unbound parent-link
	// submission must leave NO trace.
	var parentThreadID string
	if isParentLinkDelegation(skill) {
		pid, err := s.t.requireBoundThread()
		if err != nil {
			return nil, &MCPToolError{Message: err.Error()}
		}
		parentThreadID = pid
	}
	// === END guard ===

	manifest := Manifest{}
	// ... [unchanged manifest / token / observer / target-resolution block] ...

	// At the systemContext site (was tools.go:569 plus Task 3's temporary
	// best-effort shape), replace with the captured-id pass-through:
	systemContext := ""
	if isParentLinkDelegation(skill) {
		systemContext = agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			parentThreadID, // captured at top; never re-Loaded
		)
	}

	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		SystemContext:  systemContext,
		TimeoutSeconds: timeout,
	})
	// ... [rest unchanged] ...
}
```

Note: the empty-marker branch from the legacy code (`if m := s.t.loomOriginMarker(); m != ""`) is gone — `BuildLoomOrigin` with a non-empty `parentThreadID` always returns a parseable marker, and the guard guarantees `parentThreadID` is non-empty whenever `isParentLinkDelegation(skill)` is true.

- [ ] **Step 4: Finish migrating the loom-origin tests**

For each of the five loom-origin tests in `multi-agent/internal/driver/tools_test.go` that Task 3 added a `BindThread` call to, **delete the `writeLoomOriginCurrentFile` call and the `home` variable** — the file-based marker is no longer read by anything:

```go
// Before (Task 3 shape):
//   home := writeLoomOriginCurrentFile(t, markerSessID)
//   ...
//   tools := newLoomTestTools(t, sdk, home, shortID, displayName)
//   _, err := tools.BindThread(context.Background(), markerSessID)
//   require.NoError(t, err)
// After:
//   tools := newLoomTestTools(t, sdk, "" /*codexHome unused*/, shortID, displayName)
//   _, err := tools.BindThread(context.Background(), markerSessID)
//   require.NoError(t, err)
```

For `TestSubmitTaskBashDoesNotStampLoomOrigin` (`:2346`): delete the `writeLoomOriginCurrentFile` call AND the `BindThread` call. Bash is not parent-link, so the guard doesn't fire and binding is irrelevant. The assertion "marker absent" is now a structural guarantee, not a P2-marker absence.

After all five callers migrate, delete `writeLoomOriginCurrentFile` (the helper function at `:2240`):

```go
// delete this entire function (no callers remain):
// func writeLoomOriginCurrentFile(t *testing.T, sessionID string) string { ... }
```

- [ ] **Step 5: Run the targeted tests**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestSubmitTask' -v`
Expected: PASS for every `TestSubmitTask...` test, including the new `_FailsWithoutBindThread` cases and the migrated loom-origin tests.

- [ ] **Step 6: Run the full driver suite**

Run: `cd multi-agent && go test ./internal/driver/...`
Expected: PASS. If a non-loom test that exercises a parent-link skill now fails with "driver not bound", note its name — Task 7 covers the audit; you can either fix it inline (one-line `BindThread` addition) or defer to Task 7's bulk pass. Inline fix is fine for low-noise migrations.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/driver/tools.go \
        multi-agent/internal/driver/tools_test.go \
        multi-agent/internal/driver/registry.go
git commit -m "feat(driver): submit_task fail-fast bind guard (#24)"
```

### Task 5: `resume_task` unconditional guard

`resume_task` is **always** parent-link: every resume produces a `chat_resume` delegation, which spawns a nested slave codex session that needs the parent thread id. The guard is therefore unconditional (no `isParentLinkDelegation` branch). All the same capture-and-pass invariants from Task 4 apply.

**Files:**
- Modify: `multi-agent/internal/driver/tools.go::resumeTaskTool.Call` (currently `:1156-` ending around `:1240`).
- Modify: `multi-agent/internal/driver/tools_test.go::TestResumeTaskStampsLoomOrigin` (already migrated in Tasks 3-4; add the new negative test).

**Interfaces:**
- Consumes: `requireBoundThread()` (Task 3), `agentbackend.BuildLoomOrigin` (existing).
- Produces: an unbound `resume_task` call returns the same `"driver not bound to a codex thread"` error before calling `sdk.GetTask` or `sdk.DelegateTask`.

The guard is placed BEFORE the existing `sdk.GetTask` call so unbound resumes don't even consume an RPC round-trip. `GetTask` is read-only so the leak risk is low, but skipping it keeps the failure shape identical to `submit_task`.

- [ ] **Step 1: Write the new failing test**

Append to `multi-agent/internal/driver/tools_test.go` next to the existing `TestResumeTaskStampsLoomOrigin`:

```go
// TestResumeTask_FailsWithoutBindThread codifies that resume_task is always
// parent-link and refuses to operate when the driver is unbound, without
// even querying agentserver for the prior task.
func TestResumeTask_FailsWithoutBindThread(t *testing.T) {
	getCalled := false
	delegated := false
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			getCalled = true
			return nil, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return nil, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*home*/, "drv-1", "d1")
	// NO BindThread call.

	_, err := toolByName(t, tools, "resume_task").Call(context.Background(),
		json.RawMessage(`{"last_task_id":"T-1","answer":"yes"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.False(t, getCalled, "GetTask must not be called when bind missing")
	require.False(t, delegated, "DelegateTask must not be called when bind missing")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestResumeTask_FailsWithoutBindThread -v`
Expected: FAIL — under Task 3's best-effort shape the call succeeds (or fails further downstream with a different message).

- [ ] **Step 3: Add the unconditional guard at the top of `resumeTaskTool.Call`**

In `multi-agent/internal/driver/tools.go::resumeTaskTool.Call`, after the args validation block (currently around `:1162-1168`) and BEFORE the `sdk.GetTask` call (`:1169`), insert the guard. Then at the temporary best-effort shape from Task 3 (around `:1229`), use the captured `parentThreadID`:

```go
func (r *resumeTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		LastTaskID string `json:"last_task_id"`
		Answer     string `json:"answer"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.LastTaskID == "" || args.Answer == "" {
		return nil, &MCPToolError{Message: "last_task_id and answer are required"}
	}

	// === NEW: unconditional bind guard.
	// resume_task is always parent-link (every resume produces a
	// chat_resume delegation that spawns a nested slave codex session).
	// Runs BEFORE GetTask so unbound resumes don't even consume an RPC.
	parentThreadID, err := r.t.requireBoundThread()
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	// === END guard ===

	info, err := r.t.sdk.GetTask(ctx, args.LastTaskID, true)
	if err != nil {
		return nil, &MCPToolError{Message: "get_task: " + err.Error()}
	}
	// ... [unchanged kw / sessionID resolution block] ...

	// At the DelegateTask call (was tools.go:1229 plus Task 3's temporary
	// best-effort shape), use the captured parentThreadID:
	systemContext := agentbackend.BuildLoomOrigin(
		r.t.cfg.Credentials.ShortID,
		r.t.cfg.Discovery.DisplayName,
		parentThreadID,
	)
	resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       info.TargetID,
		Skill:          "chat_resume",
		Prompt:         string(body),
		SystemContext:  systemContext,
		TimeoutSeconds: timeout,
	})
	// ... [rest unchanged] ...
}
```

Note that `parentThreadID` is captured at the TOP of `Call` and used MUCH later — between the capture and the use, the call performs a `GetTask` RPC, observer-progress lookups, etc. The capture-and-pass invariant guarantees that even if a concurrent `bind_thread` runs mid-RPC and replaces `parentThread`, this invocation stamps the value the guard validated against.

- [ ] **Step 4: Run the targeted tests**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestResumeTask' -v`
Expected: PASS — `TestResumeTaskStampsLoomOrigin` (migrated in Task 3) and the new `TestResumeTask_FailsWithoutBindThread` both pass.

- [ ] **Step 5: Run the full driver suite**

Run: `cd multi-agent && go test ./internal/driver/...`
Expected: PASS. Other resume-touching tests (e.g. resume bridge-id vs slave-thread-id tests) MAY now require `BindThread` — Task 7 audits the rest, but if a test breaks here with the bind-not-set error, add a single-line `BindThread` call to its setup and move on.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/driver/tools.go \
        multi-agent/internal/driver/tools_test.go
git commit -m "feat(driver): resume_task unconditional bind guard (#24)"
```

### Task 6: `submit_contract_task` reorder + dual-path guard

`submitContractTaskTool.Call` is the trickiest of the three: it has a pre-existing side effect (`observerRelay().SaveResourceSnapshot(...)`) that lives BEFORE any bind-aware code, and it has TWO routing paths (Path A `driver_fanout` early return; Path B `selectTarget` + delegate) that each need the guard. The reorder must keep all pure / read-only work above the guard (so the snapshot has the same content) but move every observable mutation below it.

**Files:**
- Modify: `multi-agent/internal/driver/contract_tools.go::submitContractTaskTool.Call` (currently lines 35-149).
- Modify: `multi-agent/internal/driver/tools_test.go::TestSubmitContractTaskStampsLoomOrigin` (already migrated in Tasks 3-4 to `BindThread`; verify it still asserts `p.SessionID == "thr-contract"`).
- Modify: `multi-agent/internal/driver/tools_test.go::TestSubmitContractTaskUsesDriverFanoutWhenRecommended` (`:285`) — add `BindThread` to setup.
- Modify: `multi-agent/internal/driver/tools_test.go::TestSubmitContractTaskDriverFanoutRequiresConfiguredRunner` (`:322`) — add `BindThread` so guard doesn't fire BEFORE the no-runner check.
- Modify: `multi-agent/internal/driver/tools_test.go::TestSubmitContractTaskDriverFanoutCarriesLoomOrigin` (`:2706`) — verify it still asserts marker fields after the migration; add `p.SessionID == "thr-fanout"` if not already present.
- Add: `multi-agent/internal/driver/tools_test.go::TestSubmitContractTaskDriverFanoutFailsWithoutBind` and `..._NoObserverSideEffect`.

**Interfaces:**
- Consumes: `requireBoundThread()` (Task 3), `isParentLinkDelegation` (existing), `agentbackend.BuildLoomOrigin`.
- Produces: Path A and Path B both refuse to operate when unbound, BEFORE `SaveResourceSnapshot` runs. The `httptest`-recorded observer endpoint must observe ZERO `POST /api/resource-snapshots` after a failed call.

The route-vs-skill check for `needsBind`: Path A always needs bind (contractRunner spawns nested codex on the driver); Path B inherits the SubmitTask narrowed rule (parent-link skills only). The spec recommends Path B Option (a) — compute the resolved skill from the contract + skillOverride **before** calling `selectTarget`, mirroring the defaulting logic in `selectTarget` at `contract_tools.go:175-184`. This keeps the guard at the top and avoids duplicating route logic; pick Option (a). If it turns out infeasible during implementation (e.g. defaulting logic depends on `targetRole` which isn't known until after the second `DiscoverAgents` RPC), fall back to Option (b) — call `selectTarget` first then guard. Either way, `SaveResourceSnapshot` MUST move below the guard.

- [ ] **Step 1: Write the new failing tests**

Append to `multi-agent/internal/driver/tools_test.go`:

```go
// TestSubmitContractTaskDriverFanoutFailsWithoutBind exercises Path A: two
// slaves match → route = driver_fanout → guard must fire before the
// contractRunner.Run call. No BindThread setup.
func TestSubmitContractTaskDriverFanoutFailsWithoutBind(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	tools := newTestTools(t, sdk)
	tools.SetContractRunner(&fakeContractRunner{result: orchestration.RunnerResult{Summary: "x"}})
	// NO BindThread call.

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
}

// TestSubmitContractTaskDriverFanoutFailWithoutBind_NoObserverSideEffect is
// the side-effect guard: a recording httptest observer must see ZERO
// requests when the bind guard short-circuits. Catches an implementation
// that leaves SaveResourceSnapshot in its pre-guard position
// (contract_tools.go:62 today).
//
// IMPORTANT — observer wiring. `NewObserverRelay` returns nil unless
// (a) cfg.Observer.Enabled = true, (b) cfg.Observer.URL is set, AND
// (c) a non-nil TokenSource is passed (see observer_relay.go:44). The
// default newTestTools uses obs=nil, which makes the relay nil and any
// SaveResourceSnapshot a no-op — without these three knobs the test would
// pass even with the bug present. We construct an observer that satisfies
// all three, then point the relay at the recording server.
func TestSubmitContractTaskDriverFanoutFailWithoutBind_NoObserverSideEffect(t *testing.T) {
	var observerReqs int64
	observerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&observerReqs, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer observerSrv.Close()

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
	}
	// Use newTestToolsWithObserver with a fakeObserver that is non-nil AND
	// satisfies TokenSource (fakeObserver.Token() returns "fake-token"
	// today — see tools_test.go:80). This makes toTokenSource(obs) non-nil.
	obs := &fakeObserver{}
	tools := newTestToolsWithObserver(t, sdk, obs)
	tools.SetContractRunner(&fakeContractRunner{})
	// Wire the relay against the recording server. Both Enabled and URL
	// are mandatory per observer_relay.go:44.
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerSrv.URL
	tools.relay = NewObserverRelay(tools.cfg, toTokenSource(obs))
	require.NotNil(t, tools.relay,
		"relay must be non-nil — without this the SaveResourceSnapshot call is a no-op and the test is vacuous")
	// NO BindThread call.

	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.EqualValues(t, 0, atomic.LoadInt64(&observerReqs),
		"observer must see ZERO requests when bind guard short-circuits")
}
```

Add `"net/http"`, `"net/http/httptest"`, `"sync/atomic"` to the test imports if not already present.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestSubmitContractTaskDriverFanoutFails' -v`
Expected: FAIL — under Task 3's best-effort shape both pass-through (no error) OR the side-effect test sees one observer request (the leak this test exists to catch).

- [ ] **Step 3: Reorder `submitContractTaskTool.Call`**

Open `multi-agent/internal/driver/contract_tools.go`. Replace the body of `Call` from the `if err := json.Unmarshal(...)` line (`:43`) through the end of the function with the reordered version below. The structure is:

1. Pure read-only / in-memory work above the guard.
2. Guard.
3. Then-and-only-then: `SaveResourceSnapshot`, Path A or Path B.

```go
func (s *submitContractTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Contract          contract.TaskContract `json:"contract"`
		Prompt            string                `json:"prompt"`
		TargetDisplayName string                `json:"target_display_name"`
		Skill             string                `json:"skill"`
		TimeoutSec        int                   `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	tc := args.Contract
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error()}
	}

	// --- Pure / read-only block: allowed BEFORE the bind guard. None of
	// these are observable side effects. ---
	cards, err := s.t.sdk.DiscoverAgents(ctx) // read-only RPC #1
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	snapshot := contract.NewResourceSnapshot(cards, s.t.cfg.Credentials.SandboxID)
	snapshotBody, err := json.Marshal(snapshot)
	if err != nil {
		return nil, &MCPToolError{Message: "encode resource snapshot: " + err.Error()}
	}
	report := analyzeContractCapabilities(cards, s.t.cfg.Credentials.SandboxID, tc)

	body := strings.TrimSpace(args.Prompt)
	if body == "" {
		body = tc.Intent.Goal
	}
	finalPrompt, err := contract.EncodeEnvelope(tc, body)
	if err != nil {
		return nil, &MCPToolError{Message: "encode contract envelope: " + err.Error()}
	}

	// --- Compute needsBind in memory BEFORE running selectTarget. ---
	// Path A: route lands on driver_fanout AND no explicit target override.
	//   driver_fanout always spawns nested codex on the driver → always
	//   parent-link → unconditional bind.
	// Path B: resolve the skill the same way selectTarget would
	//   (mirrors contract_tools.go:175-184 defaulting). For master/fanout
	//   targets the default is "fanout"; for slave/direct it's "chat".
	//   Without targetRole we can't perfectly mirror the default — but
	//   both "fanout" and "chat" are in isParentLinkDelegation's set, so
	//   in practice every non-override Path B is parent-link. The only
	//   non-parent-link Path B is an explicit skillOverride to bash /
	//   powershell, which is exotic for contract submissions but legal.
	pathA := args.TargetDisplayName == "" && report.RecommendedRoute == routeDriverFanout
	pathBSkill := args.Skill
	if pathBSkill == "" {
		pathBSkill = "chat" // defensive default; selectTarget may upgrade to fanout
	}
	needsBind := pathA || isParentLinkDelegation(pathBSkill)

	var parentThreadID string
	if needsBind {
		pid, err := s.t.requireBoundThread()
		if err != nil {
			return nil, &MCPToolError{Message: err.Error()}
		}
		parentThreadID = pid
	}

	// --- Now-and-only-now: side-effecting operations. ---
	warnings := []string{}
	if err := s.t.observerRelay().SaveResourceSnapshot(ctx, snapshotBody); err != nil {
		warnings = append(warnings, "observer save resource snapshot: "+err.Error())
	}

	// Path A — driver_fanout early return.
	if pathA {
		if s.t.contractRunner == nil {
			return nil, &MCPToolError{Message: "driver_fanout route is recommended but no driver contract runner is configured"}
		}
		marker := agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			parentThreadID,
		)
		result, err := s.t.contractRunner.Run(ctx, finalPrompt, marker)
		if err != nil {
			return nil, &MCPToolError{Message: "driver fanout: " + err.Error()}
		}
		return json.Marshal(map[string]interface{}{
			"route":             routeDriverFanout,
			"summary":           result.Summary,
			"resource_snapshot": snapshot,
			"warnings":          warnings,
		})
	}

	// Path B — normal selectTarget. Note: this issues a second
	// (read-only) DiscoverAgents inside resolveTarget. Documented in the
	// spec as an acceptable duplicate; out-of-scope to refactor here.
	targetID, targetName, targetShortID, skill, route, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
	if err != nil {
		return nil, err
	}

	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}

	// If selectTarget resolved to a non-parent-link skill (e.g. an
	// explicit bash override on a slave that supports it), our needsBind
	// computation above may have been conservatively true. That's
	// acceptable — we captured the thread id but won't use it. The
	// SystemContext stays empty in that branch:
	systemContext := ""
	if isParentLinkDelegation(skill) {
		systemContext = agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			parentThreadID, // safe even if 0 — but isParentLinkDelegation guarantees needsBind was true above
		)
	}

	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		SystemContext:  systemContext,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}

	// --- DelegateTask succeeded — from here helper failures degrade to
	// warnings (existing contract; do not change). ---
	if err := s.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              s.Name(),
		Response:          resp,
		TargetID:          targetID,
		TargetDisplayName: targetName,
		ChildAgentID:      targetShortID,
		Skill:             skill,
		Wait:              false,
		TimeoutSec:        timeout,
	}); err != nil {
		warnings = append(warnings, "record delegated task: "+err.Error())
		s.t.logHelperErr("driver_journal", "record_delegated_task", err)
	}

	contractBody, err := json.Marshal(tc)
	if err != nil {
		return nil, &MCPToolError{Message: "encode task contract: " + err.Error()}
	}
	if err := s.t.observerRelay().SaveTaskContract(ctx, resp.TaskID, tc.ConversationID, contractBody); err != nil {
		warnings = append(warnings, "observer save task contract: "+err.Error())
	}

	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"skill":               skill,
		"route":               route,
		"resource_snapshot":   snapshot,
		"warnings":            warnings,
	})
}
```

CORNER CASE: if `pathBSkill` happens to be a non-parent-link skill (`bash`, `powershell`, etc. — exotic for contracts but legal), `needsBind` may end up false and `parentThreadID` empty. The Path B `systemContext` block correctly skips stamping in that case (its own `isParentLinkDelegation(skill)` check after `selectTarget` resolves the final skill). This means an unbound contract submission with an explicit `bash` skill override succeeds — matching the SubmitTask narrowed-guard semantic.

If during implementation Option (a)'s in-memory skill default conflicts with `selectTarget`'s `targetRole`-aware defaulting in a corner case the tests catch, switch to Option (b): call `selectTarget` first to resolve `(skill, targetRole, route)`, then run the guard with the resolved skill — `SaveResourceSnapshot` still moves below the guard either way.

- [ ] **Step 4: Migrate the existing driver-fanout tests**

In `multi-agent/internal/driver/tools_test.go`:

- `TestSubmitContractTaskUsesDriverFanoutWhenRecommended` (`:285`): add `tools.BindThread(context.Background(), "thr-fanout-uses")` immediately after `tools := newTestTools(t, sdk)`. No assertion change.
- `TestSubmitContractTaskDriverFanoutRequiresConfiguredRunner` (`:322`): same pattern. Without bind, the bind guard would fire BEFORE the no-runner check, so the test would assert the wrong error string.
- `TestSubmitContractTaskDriverFanoutCarriesLoomOrigin` (`:2706`): already migrated in Tasks 3-4. ADD an assertion `require.Equal(t, "thr-fanout", p.SessionID)` if missing — this verifies the captured thread id reaches the contract runner via `BuildLoomOrigin`.

- [ ] **Step 5: Run the targeted tests**

Run: `cd multi-agent && go test ./internal/driver/ -run 'TestSubmitContractTask' -v`
Expected: PASS for every `TestSubmitContractTask...` — happy paths, both new `_FailsWithoutBind` tests, the driver_fanout migrations, and the side-effect-free guard test.

- [ ] **Step 6: Run the full driver suite**

Run: `cd multi-agent && go test ./internal/driver/...`
Expected: PASS. If any other contract-touching test breaks with the bind error, add a one-line `BindThread` call to its setup (those candidates are part of Task 7).

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/driver/contract_tools.go \
        multi-agent/internal/driver/tools_test.go
git commit -m "feat(driver): submit_contract_task dual-path bind guard + side-effect reorder (#24)"
```

### Task 7: Bulk test audit for parent-link skills (non-loom tests)

The narrowed `isParentLinkDelegation` guard means most existing `SubmitTask`/`ResumeTask`/`SubmitContractTask` tests are unaffected (bash/powershell skills short-circuit the guard). But ~6-10 tests across `tools_test.go` exercise `chat`/`fanout`/`route` skills WITHOUT a `writeLoomOriginCurrentFile` setup; under Tasks 4-6 those will now fail with the bind error. This task does the systematic audit and adds a one-line `BindThread` to each.

**Files:**
- Modify: `multi-agent/internal/driver/tools_test.go` (one-liner additions; no logic changes).

**Interfaces:**
- Consumes: `Tools.BindThread` (Task 1).
- Produces: full driver test suite green with no `"driver not bound"` errors leaking from parent-link skill tests; explicit positive/negative narrowed-guard semantic tests for each tool.

- [ ] **Step 1: List candidate tests**

Run from the repo root:

```bash
cd multi-agent
rg -nE 'func TestSubmit(Task|ContractTask)|func TestResumeTask' internal/driver/tools_test.go | sort -k1,1
```

This yields ~30 function-start lines. For each:

```bash
# Inspect the args body to find the skill string.
sed -n '<START>,<END>p' internal/driver/tools_test.go | rg '"skill"|Skill:'
```

For every test whose call site sets `"skill":"chat"`, `"skill":"chat_resume"`, `"skill":"fanout"`, `"skill":"fanout_strict"`, `"skill":"route"`, OR omits `"skill"` entirely (default `fanout`), AND does NOT already call `BindThread`, add one line to the setup:

```go
// Right after newTestTools or newLoomTestTools:
_, err := tools.BindThread(context.Background(), "thr-<test-slug>")
require.NoError(t, err)
```

Pick `<test-slug>` to match the test's intent (e.g. `thr-chat-default`, `thr-route-direct`). The exact value doesn't matter unless the test asserts marker contents — in which case use the value the assertion expects.

Candidate hot spots from spec / inspection (verify locally):
- `tools_test.go:1045` — likely needs bind
- `tools_test.go:1911` — likely needs bind
- `tools_test.go:2622` — likely needs bind

These line numbers are from the spec's reviewer hint; treat them as starting points and run the audit yourself.

- [ ] **Step 2: Run the full suite and triage failures**

```bash
cd multi-agent
go test ./internal/driver/... 2>&1 | tee /tmp/driver-test.log
rg 'FAIL|driver not bound to a codex thread' /tmp/driver-test.log
```

For each FAIL line that points to a test calling a parent-link skill, add the `BindThread` line. For each FAIL line that does NOT mention "driver not bound", investigate separately — it's not a Task 7 fix.

- [ ] **Step 3: Add the explicit narrowed-guard semantic tests**

Some are already added in Tasks 4-6. Verify the matrix below is fully covered; add any missing:

| Tool | Parent-link skill | Non-parent-link skill |
|---|---|---|
| `submit_task` | `_ChatSkill_FailsWithoutBindThread` (T4) | `_BashSkill_SucceedsWithoutBindThread` (T4) |
| `submit_task` | `_FanoutDefault_FailsWithoutBindThread` (T4) | — |
| `resume_task` | `_FailsWithoutBindThread` (T5) | n/a (resume is always parent-link) |
| `submit_contract_task` Path A | `_DriverFanoutFailsWithoutBind` (T6) | n/a (Path A always parent-link) |
| `submit_contract_task` Path B | `_ChatSkill_FailsWithoutBindThread` (NEW, here) — see below | `_BashSkillOverride_SucceedsWithoutBind` (NEW, here) |

The migrated chat-route tests all ADD a `BindThread` call to satisfy the new guard — they prove the happy path stays green, but they cannot prove the unbound-fails contract for Path B (no migrated test ever runs without a bind). Add a dedicated negative test below.

Add the Path B parent-link FAILURE test (mirrors `_DriverFanoutFailsWithoutBind` from T6 but routes via Path B):

```go
// TestSubmitContractTaskPathB_ChatSkill_FailsWithoutBindThread is the
// Path B mirror of T6's _DriverFanoutFailsWithoutBind. A single
// chat-capable slave makes the contract route via selectTarget (Path B)
// rather than driver_fanout. Without bind_thread the guard MUST fire
// before SaveResourceSnapshot, before DelegateTask, and before any
// observer side effect.
//
// Observer wiring uses the same enable-relay pattern as
// _NoObserverSideEffect (T6) so the assertion is meaningful — without
// Enabled/URL/TokenSource all set, NewObserverRelay returns nil and the
// SaveResourceSnapshot call is a vacuous no-op (see observer_relay.go:44).
func TestSubmitContractTaskPathB_ChatSkill_FailsWithoutBindThread(t *testing.T) {
	var observerReqs int64
	observerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&observerReqs, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer observerSrv.Close()

	delegated := false
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			// Exactly ONE chat slave → route falls through driver_fanout
			// (which needs >=2 matches) and into Path B selectTarget.
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-chat", DisplayName: "slave-chat", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "should-not-reach"}, nil
		},
	}

	obs := &fakeObserver{}
	tools := newTestToolsWithObserver(t, sdk, obs)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerSrv.URL
	tools.relay = NewObserverRelay(tools.cfg, toTokenSource(obs))
	require.NotNil(t, tools.relay, "relay must be non-nil — otherwise observer assertion is vacuous")
	// NO BindThread call — Path B with chat skill must reject.

	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = []string{"chat"}
	raw, err := json.Marshal(map[string]interface{}{
		"contract":            tc,
		"skill":               "chat",
		"target_display_name": "slave-chat",
	})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.False(t, delegated, "DelegateTask must NOT be called when bind missing")
	require.EqualValues(t, 0, atomic.LoadInt64(&observerReqs),
		"observer must see ZERO requests when Path B bind guard short-circuits")
}
```

Add the Path B negative SUCCESS test (bash override, no bind required):

```go
// TestSubmitContractTaskPathB_BashSkillOverride_SucceedsWithoutBind covers
// the exotic corner case: a contract submitted with an explicit bash skill
// override, no target_display_name, single slave matching the contract.
// Path B's resolved skill is bash → not parent-link → no bind required →
// no SystemContext stamping.
func TestSubmitContractTaskPathB_BashSkillOverride_SucceedsWithoutBind(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-bash", DisplayName: "slave-bash", Status: "available",
					Card: json.RawMessage(`{"skills":["bash"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-b"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = []string{"bash"}
	raw, err := json.Marshal(map[string]interface{}{
		"contract":            tc,
		"skill":               "bash",
		"target_display_name": "slave-bash",
	})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, "bash", captured.Skill)
	require.Empty(t, captured.SystemContext)
}
```

NOTE: if the Path B needsBind computation in Task 6 conservatively bound `pathBSkill` to a parent-link default and this test fails with "driver not bound", refine the Step 3 logic in Task 6 — that's a sign Option (b) (call `selectTarget` first) is the cleaner choice for this codepath. Either fix is acceptable; tighten the guard so it matches the post-`selectTarget` skill.

- [ ] **Step 4: Run the full suite, expect green**

```bash
cd multi-agent
go test ./internal/driver/...
```

Expected: PASS.

- [ ] **Step 5: Sanity-check the whole repo**

Run: `cd multi-agent && go test ./...`
Expected: PASS. Tasks 4-7 only touch `internal/driver/`, but a non-driver package may depend on `loomOriginMarker` indirectly via the bridged Tools struct — surface and fix here if so.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/driver/tools_test.go
git commit -m "test(driver): audit non-loom tests for narrowed parent-link bind (#24)"
```

### Task 8: `bind_thread` integration test (env-gated)

End-to-end stdio MCP round-trip. Boots `NewMCPServer(tools.All()).Serve(ctx, stdinReader, stdoutWriter)`, sends newline-delimited JSON-RPC `tools/call` requests for `bind_thread` then `submit_task`, and parses MCP's `result.content[0].text` envelope (see `multi-agent/internal/driver/mcp_server.go:243-249`). Env-gated because it spins up the full server; the non-gated `_RegisteredInAll` test from Task 2 is the cheap CI regression guard.

This is the difference between a tool-wrapper smoke (already covered in Task 2) and a wire-level smoke: only the MCP path exercises `dispatch`, `handleToolsCall`, the `-32000` error envelope, the `content[0].text` shape, and the concurrent goroutine spawned per call (`mcp_server.go:91, :237`).

**Files:**
- Create: `multi-agent/internal/driver/bind_thread_integration_test.go`

**Interfaces:**
- Consumes: `NewMCPServer([]Tool) *MCPServer` (`mcp_server.go:41`), `(*MCPServer).Serve(ctx, io.Reader, io.Writer) error` (`:91`), `(*MCPServer).WaitForLines(n int)` test helper (`:164`), fake `SDKClient`.
- Produces: a smoke test asserting (a) tools/call for `submit_task` BEFORE `bind_thread` returns a JSON-RPC -32000 error whose message contains "driver not bound", (b) tools/call for `bind_thread` returns a `result.content[0].text` whose JSON decodes to a `BindResult{Bound:true, ThreadID:"019…", AgentID, DisplayName}`, (c) tools/call for `submit_task` after bind succeeds and the captured `DelegateTaskRequest.SystemContext` carries `loom_origin` with `parent_session_id == bound id`.

- [ ] **Step 1: Write the test**

Create `multi-agent/internal/driver/bind_thread_integration_test.go`:

```go
package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// mcpEnvelope mirrors the result shape mcp_server.go writes for tools/call:
//
//	{"jsonrpc":"2.0","id":<id>,"result":{"content":[{"type":"text","text":"<json>"}]}}
//
// or on error:
//
//	{"jsonrpc":"2.0","id":<id>,"error":{"code":-32000,"message":"..."}}
type mcpEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// TestIntegration_DriverBindFlow drives a real MCP server over an in-memory
// stdio pair and asserts the bind / stamp / unbound-fails-cleanly contract
// at the wire level. Env-gated to keep routine CI fast; the wire is the
// reason this test exists (the Tool-wrapper layer is already covered by
// TestBindThreadTool_RegisteredInAll in Task 2).
func TestIntegration_DriverBindFlow(t *testing.T) {
	if os.Getenv("LOOM_BIND_THREAD_INTEGRATION") != "1" {
		t.Skip("set LOOM_BIND_THREAD_INTEGRATION=1 to run")
	}

	var (
		muCap    sync.Mutex
		captured agentsdk.DelegateTaskRequest
	)
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave", DisplayName: "slave", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			muCap.Lock()
			captured = req
			muCap.Unlock()
			return &agentsdk.DelegateTaskResponse{TaskID: "task-i1"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*home*/, "drv-int", "int-driver")

	// Stdio pair. We write JSON-RPC requests to inW (server reads from inR);
	// the server writes responses to outW (we read from outR). Close inW
	// when done to let Serve exit cleanly.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewMCPServer(tools.All())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx, inR, outW)
		_ = outW.Close()
	}()
	scanner := bufio.NewScanner(outR)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	readEnvelope := func(t *testing.T) mcpEnvelope {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("scanner: no line (err=%v)", scanner.Err())
		}
		var env mcpEnvelope
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &env))
		return env
	}
	writeReq := func(t *testing.T, line string) {
		t.Helper()
		_, err := io.WriteString(inW, line+"\n")
		require.NoError(t, err)
	}

	// 1) tools/call submit_task BEFORE bind_thread → JSON-RPC -32000.
	writeReq(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_task","arguments":{"prompt":"hi","skill":"chat","target_display_name":"slave"}}}`)
	env := readEnvelope(t)
	require.NotNil(t, env.Error, "expected JSON-RPC error, got result=%v", env.Result)
	require.Equal(t, -32000, env.Error.Code)
	require.Contains(t, env.Error.Message, "driver not bound to a codex thread")
	require.Nil(t, env.Result)

	// 2) tools/call bind_thread → result.content[0].text is a BindResult JSON.
	writeReq(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bind_thread","arguments":{"thread_id":"019ef3bd-42c8-7731-85b7-7177ae747389"}}}`)
	env = readEnvelope(t)
	require.Nil(t, env.Error)
	require.NotNil(t, env.Result)
	require.Len(t, env.Result.Content, 1)
	require.Equal(t, "text", env.Result.Content[0].Type)
	var bres BindResult
	require.NoError(t, json.Unmarshal([]byte(env.Result.Content[0].Text), &bres))
	require.True(t, bres.Bound)
	require.Equal(t, "019ef3bd-42c8-7731-85b7-7177ae747389", bres.ThreadID)
	require.Equal(t, "drv-int", bres.AgentID)
	require.Equal(t, "int-driver", bres.DisplayName)

	// 3) tools/call submit_task → result.content[0].text decodes to the
	//    DelegateTask response; captured request carries a parseable
	//    loom_origin marker pointing at the bound thread.
	const boundID = "019ef3bd-42c8-7731-85b7-7177ae747389"
	writeReq(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_task","arguments":{"prompt":"hi","skill":"chat","target_display_name":"slave"}}}`)
	env = readEnvelope(t)
	require.Nil(t, env.Error)
	require.NotNil(t, env.Result)
	require.Len(t, env.Result.Content, 1)
	require.Contains(t, env.Result.Content[0].Text, `"task_id":"task-i1"`)
	muCap.Lock()
	sc := captured.SystemContext
	muCap.Unlock()
	// Parse — substring match would still pass if the marker's JSON shape
	// drifted to a stale schema. ParseLoomOrigin enforces the wire-shape
	// contract: it returns ok=false if `{"loom_origin":{...}}` is missing
	// or malformed (pkg/agentbackend/loomorigin.go:57).
	parent, _, ok := agentbackend.ParseLoomOrigin(sc)
	require.True(t, ok, "SystemContext must contain a parseable loom_origin marker, got %q", sc)
	require.Equal(t, boundID, parent.SessionID,
		"parent_session_id MUST equal the bound thread id")
	require.Equal(t, "drv-int", parent.AgentID)
	require.Equal(t, "int-driver", parent.DisplayName)

	// Cleanly stop Serve: close stdin → reader goroutine exits with EOF.
	require.NoError(t, inW.Close())
	select {
	case err := <-serveErr:
		// Serve returns ctx.Err() (nil) on clean EOF; any other error fails.
		if err != nil && err != context.Canceled {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin EOF")
	}
}
```

- [ ] **Step 2: Run it with the gate set**

```bash
cd multi-agent
LOOM_BIND_THREAD_INTEGRATION=1 go test ./internal/driver/ -run TestIntegration_DriverBindFlow -v
```

Expected: PASS.

- [ ] **Step 3: Confirm the gate works**

```bash
cd multi-agent
go test ./internal/driver/ -run TestIntegration_DriverBindFlow -v
```

Expected: `--- SKIP: TestIntegration_DriverBindFlow`.

- [ ] **Step 4: Commit**

```bash
git add multi-agent/internal/driver/bind_thread_integration_test.go
git commit -m "test(driver): env-gated bind_thread MCP integration (#24)"
```

### Task 9: `discover-thread.sh` script

The canonical discovery script. Single bash file, no host-specific deps beyond bash + sed + awk + readlink + standard POSIX. The Linux `/proc` source is `[[ -d /proc/$PPID/fd ]]`-gated so macOS / WSL / Git Bash hosts fall through to the actionable stderr block.

**Files:**
- Create: `skills/multiagent/scripts/discover-thread.sh`

**Interfaces:**
- Consumes: env `CODEX_THREAD_ID`, env `CODEX_HOME`, `/proc/$PPID/fd/*` symlinks on Linux.
- Produces: stdout = UUID on success (exit 0); stderr = actionable message on failure (exit 1 = no source resolved; exit 2 = multi-candidate invariant violation, stderr lists candidates).

The script body MUST match the version inlined in `SKILL.md` byte-for-byte (Task 11). Task 12 ships the CI gate that enforces this.

- [ ] **Step 1: Create the file**

Create `skills/multiagent/scripts/discover-thread.sh`:

```bash
#!/usr/bin/env bash
# discover-thread.sh — print the current codex thread_id to stdout, or
# exit non-zero with an explanation on stderr. Deterministic; no heuristics.
#
# Signals allowed:
#   1. $CODEX_THREAD_ID (codex itself wrote it, or a forwarder did)
#   2. /proc/<ppid>/fd open rollout file under $CODEX_HOME/sessions/
#      with exact UUID suffix (Linux)
#
# Cwd-based or "newest thread" sqlite lookup is INTENTIONALLY OMITTED —
# the same cwd routinely hosts multiple codex threads, and "newest" can
# silently mis-attribute to the wrong one.
#
# /proc/<ppid>/fd matches MUST be:
#   - under $CODEX_HOME/sessions/ (canonicalized prefix)
#   - basename ends with -<UUID>.jsonl
# Across the parent-chain walk we collect UNIQUE thread_id candidates;
# exactly 1 → emit it; 0 → fall to manual fallback; >1 → fail loud and
# print the candidates so operators can see the invariant violation.

set -euo pipefail

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
valid() { [[ "$1" =~ $UUID_RE ]]; }

# Source 1: env
if [[ -n "${CODEX_THREAD_ID:-}" ]] && valid "$CODEX_THREAD_ID"; then
    echo "$CODEX_THREAD_ID"
    exit 0
fi

# Source 2: Linux /proc/<pid>/fd walk
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
# Canonicalize so symlinked CODEX_HOME doesn't false-negative the prefix
# check (gopsutil-side fd readlinks return realpath).
SESSIONS_ROOT=$(cd "$CODEX_HOME/sessions" 2>/dev/null && pwd -P || true)

if [[ -n "$SESSIONS_ROOT" && -d "/proc/$PPID/fd" ]]; then
    declare -A seen=()
    pid=$PPID
    for _ in 1 2 3 4 5; do
        [[ "$pid" -gt 1 ]] || break
        if [[ -d "/proc/$pid/fd" ]]; then
            for fd in /proc/$pid/fd/*; do
                target=$(readlink -f "$fd" 2>/dev/null) || continue
                # Reject anything not under sessions root
                [[ "$target" == "$SESSIONS_ROOT"/* ]] || continue
                # Extract trailing UUID (validate strict shape)
                cand=$(printf '%s\n' "$target" | sed -nE \
                    's|.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$|\1|p')
                if [[ -n "$cand" ]] && valid "$cand"; then
                    seen["$cand"]=1
                fi
            done
        fi
        pid=$(awk '/^PPid:/{print $2}' "/proc/$pid/status" 2>/dev/null || echo 1)
    done

    case "${#seen[@]}" in
        1)
            for k in "${!seen[@]}"; do echo "$k"; done
            exit 0
            ;;
        0)
            : # fall through to manual-fallback message below
            ;;
        *)
            {
                echo "discover-thread: parent codex appears to hold MORE THAN ONE"
                echo "open rollout fd under $SESSIONS_ROOT. Refusing to guess."
                echo "Candidate thread_ids:"
                for k in "${!seen[@]}"; do echo "  - $k"; done
                echo "Next: use /status in codex to identify the correct id"
                echo "and call driver.bind_thread(thread_id=...) directly."
            } >&2
            exit 2
            ;;
    esac
fi

# All sources failed — explicit fail with operator-actionable message.
{
    echo "discover-thread: could not determine the parent codex thread_id."
    echo ""
    echo "Tried:"
    echo "  1. \$CODEX_THREAD_ID env (unset or malformed)"
    if [[ -d "/proc/$PPID/fd" ]]; then
        if [[ -z "${SESSIONS_ROOT:-}" ]]; then
            echo "  2. /proc/<ppid>/fd scan (skipped — \$CODEX_HOME/sessions missing)"
        else
            echo "  2. /proc/<ppid>/fd scan under $SESSIONS_ROOT (no match)"
        fi
    else
        echo "  2. /proc/<ppid>/fd scan (skipped — not on Linux)"
    fi
    echo ""
    echo "Next steps:"
    echo "  - In your codex session, type /status. Find the line"
    echo "    starting with 'thread_id:' and copy the UUID."
    echo "  - Then call driver.bind_thread(thread_id=<that uuid>) manually."
} >&2
exit 1
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x skills/multiagent/scripts/discover-thread.sh
```

- [ ] **Step 3: Manual smoke test (Linux)**

```bash
# env path
CODEX_THREAD_ID=019ef3bd-42c8-7731-85b7-7177ae747389 \
    skills/multiagent/scripts/discover-thread.sh
# stdout: 019ef3bd-42c8-7731-85b7-7177ae747389
# exit 0

# all-fail path (point CODEX_HOME at an empty dir to neutralize /proc walk).
# `env -u VAR cmd...` unsets VAR for that one invocation; this is the
# portable way to drop CODEX_THREAD_ID just for the script while still
# passing CODEX_HOME. The earlier-draft `CODEX_HOME=... unset ...` form
# would NOT pass CODEX_HOME to the next line — that's a sequence of two
# statements, not a single env-prefixed command.
env -u CODEX_THREAD_ID CODEX_HOME="$(mktemp -d)" \
    skills/multiagent/scripts/discover-thread.sh
echo "exit=$?"
# stderr: operator-actionable block ; exit=1
```

- [ ] **Step 4: shellcheck**

```bash
shellcheck skills/multiagent/scripts/discover-thread.sh
```

Expected: no warnings (or only `SC2034` / advisory; gating warnings is a follow-up). Fix actionable issues inline if any appear.

- [ ] **Step 5: Commit**

```bash
git add skills/multiagent/scripts/discover-thread.sh
git commit -m "feat(skills): discover-thread.sh — deterministic codex thread_id discovery (#24)"
```

### Task 10: `discover-thread.sh` bash test suite

Tests every exit code branch. The parent-holds-fd trick exercises Source 2 without trying to set the read-only `$PPID` — the test harness shell opens the rollout fd and then runs the script as its CHILD so the script's `$PPID` points at the harness, where `/proc/$PPID/fd/9` holds the rollout link.

**Files:**
- Create: `skills/multiagent/scripts/discover-thread_test.sh`

**Interfaces:**
- Consumes: `discover-thread.sh` (Task 9). Standard POSIX bash + sed + awk.
- Produces: an executable test script returning exit 0 on success, exit 1 on any failed case, with `ok N - <name>` / `not ok N - <name>` TAP-shaped output for CI parsing.

The fixtures use a Codex-shaped UUIDv7 (`019ef3bd-42c8-7731-85b7-7177ae747389`) so tests double as documentation. The test-isolation invariant (CODEX_HOME pointing at a directory without `sessions/`) MUST be applied to every env-fall-through case; otherwise stray rollout fds from the outer CI runner's codex session would let the script "succeed" via Source 2 and never exercise the intended exit-1 branch.

- [ ] **Step 1: Create the test file**

Create `skills/multiagent/scripts/discover-thread_test.sh`:

```bash
#!/usr/bin/env bash
# discover-thread_test.sh — bash tests for discover-thread.sh.
# TAP-shaped output: "1..<N>" header, "ok N - <name>" / "not ok N - <name>".
# Linux-only /proc tests skip on macOS / WSL-without-/proc / etc.

set -u
TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPT="$TEST_DIR/discover-thread.sh"

UUID_OK='019ef3bd-42c8-7731-85b7-7177ae747389'
UUID_OK2='019ef3bd-42c8-7731-85b7-7177ae747390'

passed=0
failed=0
n=0
ok()      { n=$((n+1)); passed=$((passed+1)); echo "ok $n - $1"; }
not_ok()  { n=$((n+1)); failed=$((failed+1)); echo "not ok $n - $1"; }
skip()    { n=$((n+1)); echo "ok $n - $1 # SKIP $2"; }

assert_exit() {
    local label=$1 expected=$2 actual=$3
    if [[ "$expected" == "$actual" ]]; then
        ok "$label (exit=$expected)"
    else
        not_ok "$label (expected exit=$expected, got=$actual)"
    fi
}

assert_stdout() {
    local label=$1 expected=$2 actual=$3
    if [[ "$expected" == "$actual" ]]; then
        ok "$label (stdout matches)"
    else
        not_ok "$label (expected stdout=$expected, got=$actual)"
    fi
}

# 1) Source 1 (env): valid UUID returns immediately.
tmpdir=$(mktemp -d)
out=$(CODEX_THREAD_ID="$UUID_OK" CODEX_HOME="$tmpdir" "$SCRIPT" 2>/dev/null)
rc=$?
assert_exit "env_valid_uuid_returns_immediately" 0 "$rc"
assert_stdout "env_valid_uuid_returns_immediately stdout" "$UUID_OK" "$out"
rm -rf "$tmpdir"

# 2) Source 1: invalid format falls through; CODEX_HOME points at empty dir
#    so the /proc walk does NOT match anything from the outer runner.
#    Disable set -e for the command since we expect non-zero. Capture
#    stderr to a file rather than `|| true` so we get the SCRIPT's exit
#    code, not `true`'s.
tmpdir=$(mktemp -d)
errfile=$(mktemp)
set +e
CODEX_THREAD_ID='thr-from-env' CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>"$errfile"
rc=$?
set -e
assert_exit "env_invalid_format_falls_through" 1 "$rc"
rm -rf "$tmpdir" "$errfile"

# 3) Source 1: unexpanded placeholder.
tmpdir=$(mktemp -d)
set +e
CODEX_THREAD_ID='${CODEX_THREAD_ID}' CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>/dev/null
rc=$?
set -e
assert_exit "env_unexpanded_placeholder_falls_through" 1 "$rc"
rm -rf "$tmpdir"

# 4) Source 1: whitespace-only.
tmpdir=$(mktemp -d)
set +e
CODEX_THREAD_ID='   ' CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>/dev/null
rc=$?
set -e
assert_exit "env_whitespace_falls_through" 1 "$rc"
rm -rf "$tmpdir"

# 5) Source 2 (Linux only): single /proc fd match returns UUID. Uses the
#    parent-holds-fd, script-runs-as-child topology — the harness shell
#    opens the rollout on fd 9 and `bash "$SCRIPT"` runs as its child, so
#    the script's $PPID points at the harness.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions/2026/06/23"
    rollout="$tmpdir/sessions/2026/06/23/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    : > "$rollout"
    out=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" ROLLOUT_PATH="$rollout" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$ROLLOUT_PATH"
              bash "$SCRIPT"
          ')
    rc=$?
    assert_exit "proc_fd_single_match_returns_uuid" 0 "$rc"
    assert_stdout "proc_fd_single_match_returns_uuid stdout" "$UUID_OK" "$out"
    rm -rf "$tmpdir"
else
    skip "proc_fd_single_match_returns_uuid" "no /proc on this host"
fi

# 6) Source 2: rollout path OUTSIDE $CODEX_HOME/sessions/ → not picked up.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    elsewhere=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    bad="$elsewhere/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    : > "$bad"
    rc=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" ROLLOUT_PATH="$bad" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$ROLLOUT_PATH"
              bash "$SCRIPT" >/dev/null 2>&1
              echo $?
          ')
    assert_exit "proc_fd_rejects_path_outside_sessions_root" 1 "$rc"
    rm -rf "$tmpdir" "$elsewhere"
else
    skip "proc_fd_rejects_path_outside_sessions_root" "no /proc on this host"
fi

# 7) Source 2: non-UUID basename ignored.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    bad="$tmpdir/sessions/rollout-2026-06-23T17-08-20-not-a-uuid.jsonl"
    : > "$bad"
    rc=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" ROLLOUT_PATH="$bad" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$ROLLOUT_PATH"
              bash "$SCRIPT" >/dev/null 2>&1
              echo $?
          ')
    assert_exit "proc_fd_rejects_non_uuid_basename" 1 "$rc"
    rm -rf "$tmpdir"
else
    skip "proc_fd_rejects_non_uuid_basename" "no /proc on this host"
fi

# 8) Source 2: multi-candidate → exit 2, stderr lists both.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    r1="$tmpdir/sessions/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    r2="$tmpdir/sessions/rollout-2026-06-23T17-08-21-$UUID_OK2.jsonl"
    : > "$r1"
    : > "$r2"
    output=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" R1="$r1" R2="$r2" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$R1"
              exec 8< "$R2"
              bash "$SCRIPT" 2>&1 >/dev/null
              echo "rc=$?"
          ')
    rc=$(echo "$output" | sed -nE 's/^rc=([0-9]+)$/\1/p' | tail -n 1)
    assert_exit "proc_fd_multi_candidate_fails_loud" 2 "$rc"
    case "$output" in
        *"$UUID_OK"*"$UUID_OK2"*|*"$UUID_OK2"*"$UUID_OK"*) ok "proc_fd_multi_candidate_lists_both_in_stderr";;
        *) not_ok "proc_fd_multi_candidate_lists_both_in_stderr";;
    esac
    rm -rf "$tmpdir"
else
    skip "proc_fd_multi_candidate_fails_loud" "no /proc on this host"
    skip "proc_fd_multi_candidate_lists_both_in_stderr" "no /proc on this host"
fi

# 9) Source 2: same UUID opened twice → dedupes to single candidate.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    r1="$tmpdir/sessions/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    : > "$r1"
    out=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" R1="$r1" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$R1"
              exec 8< "$R1"
              bash "$SCRIPT"
          ')
    rc=$?
    assert_exit "proc_fd_dedupe_same_uuid_across_walk" 0 "$rc"
    assert_stdout "proc_fd_dedupe_same_uuid_across_walk stdout" "$UUID_OK" "$out"
    rm -rf "$tmpdir"
else
    skip "proc_fd_dedupe_same_uuid_across_walk" "no /proc on this host"
fi

# 10) All sources fail → exit 1 with operator-actionable stderr.
#     Capture stderr to a temp file and the script's exit code via $?
#     (NOT via `|| true` — that would mask the true exit code as 0).
tmpdir=$(mktemp -d)
errfile=$(mktemp)
set +e
env -u CODEX_THREAD_ID CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>"$errfile"
rc=$?
set -e
err=$(cat "$errfile")
assert_exit "all_sources_fail_explicit_message" 1 "$rc"
case "$err" in
    *"/status"*"thread_id"*) ok "all_sources_fail_mentions_status";;
    *) not_ok "all_sources_fail_mentions_status";;
esac
rm -rf "$tmpdir" "$errfile"

# TAP header (printed last because we computed n dynamically).
echo "1..$n"
[[ "$failed" -eq 0 ]]
```

- [ ] **Step 2: Run the suite**

```bash
chmod +x skills/multiagent/scripts/discover-thread_test.sh
skills/multiagent/scripts/discover-thread_test.sh
echo "exit=$?"
```

Expected: TAP lines `ok 1 …` through `ok N …`, final line `1..N`, exit 0. On macOS / non-Linux you'll see `# SKIP no /proc on this host` for tests 5-9.

- [ ] **Step 3: Wire into CI**

Add an invocation alongside the existing skills tests in CI. If a Makefile already has a `test-skills` target, append the command; otherwise add a one-line GitHub Actions step:

```yaml
- name: discover-thread.sh tests
  run: skills/multiagent/scripts/discover-thread_test.sh
```

- [ ] **Step 4: Commit**

```bash
git add skills/multiagent/scripts/discover-thread_test.sh
# also include any Makefile / CI workflow change
git commit -m "test(skills): bash tests for discover-thread.sh exit codes (#24)"
```

### Task 11: SKILL.md Initialization section + inline heredoc

Add a new `## Initialization (REQUIRED before any submit_task)` section to `skills/multiagent/SKILL.md` before the existing `## Default Workflow`. The heredoc body MUST be byte-identical to `skills/multiagent/scripts/discover-thread.sh` — Task 12 ships the CI drift gate that enforces this.

**Files:**
- Modify: `skills/multiagent/SKILL.md`

**Interfaces:**
- Consumes: `skills/multiagent/scripts/discover-thread.sh` (Task 9), `driver.bind_thread` MCP tool (Tasks 1-2).
- Produces: an Initialization section the model reads and executes once per session before any parent-link submission.

The quoted heredoc form `bash <<'DISCOVER_EOF' ... DISCOVER_EOF` is mandatory:

- Single-quoted regex literals inside the script (e.g. `UUID_RE='^[0-9a-fA-F]...$'`) collide with single quotes around a `bash -c '...'` argument; the heredoc form sidesteps this.
- The leading `'DISCOVER_EOF'` quoting disables variable / command expansion inside the body — required for the CI drift check (`$CODEX_HOME` would otherwise be expanded by the shell before the script ever runs).

- [ ] **Step 1: Insert the section**

Open `skills/multiagent/SKILL.md`. After the front-matter and the existing `# Multiagent` / `## Core Rule` blocks, and BEFORE `## Default Workflow`, insert:

````markdown
## Initialization (REQUIRED before any submit_task)

You MUST bind the driver to its parent codex thread once per session,
BEFORE calling any parent-link skill (`submit_task` / `resume_task` /
`submit_contract_task` with skill ∈ {`chat`, `chat_resume`, `fanout`,
`fanout_strict`, `route`} — i.e. anything that spawns a nested codex on a
slave). Bash-only / powershell-only submissions don't need this.

**Step 1** — run this discovery script as a shell tool call. **Pipe the
script body into bash via a quoted heredoc** (`bash <<'DISCOVER_EOF' ...
DISCOVER_EOF`) rather than `bash -c '...'`. The script contains
single-quoted regex literals (`UUID_RE='^[0-9a-fA-F]{8}-...$'`) that would
conflict with single quotes around a `-c` argument. The quoted heredoc
form (`<<'DISCOVER_EOF'`) disables variable / command expansion AND
preserves the script byte-for-byte, satisfying the CI drift check.

**Critical**: do NOT wrap the invocation in `THREAD_ID=$(...)` or any other
shell-variable assignment. Shell-tool calls do not persist environment
between invocations — a variable set in one tool call vanishes before the
next. The script must print the thread_id to stdout so the model sees it
directly in the tool-call response. The model then copies that value
verbatim into the `bind_thread` MCP call.

```
bash <<'DISCOVER_EOF'
#!/usr/bin/env bash
# discover-thread.sh — print the current codex thread_id to stdout, or
# exit non-zero with an explanation on stderr. Deterministic; no heuristics.
#
# Signals allowed:
#   1. $CODEX_THREAD_ID (codex itself wrote it, or a forwarder did)
#   2. /proc/<ppid>/fd open rollout file under $CODEX_HOME/sessions/
#      with exact UUID suffix (Linux)
#
# Cwd-based or "newest thread" sqlite lookup is INTENTIONALLY OMITTED —
# the same cwd routinely hosts multiple codex threads, and "newest" can
# silently mis-attribute to the wrong one.
#
# /proc/<ppid>/fd matches MUST be:
#   - under $CODEX_HOME/sessions/ (canonicalized prefix)
#   - basename ends with -<UUID>.jsonl
# Across the parent-chain walk we collect UNIQUE thread_id candidates;
# exactly 1 → emit it; 0 → fall to manual fallback; >1 → fail loud and
# print the candidates so operators can see the invariant violation.

set -euo pipefail

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
valid() { [[ "$1" =~ $UUID_RE ]]; }

# Source 1: env
if [[ -n "${CODEX_THREAD_ID:-}" ]] && valid "$CODEX_THREAD_ID"; then
    echo "$CODEX_THREAD_ID"
    exit 0
fi

# Source 2: Linux /proc/<pid>/fd walk
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
# Canonicalize so symlinked CODEX_HOME doesn't false-negative the prefix
# check (gopsutil-side fd readlinks return realpath).
SESSIONS_ROOT=$(cd "$CODEX_HOME/sessions" 2>/dev/null && pwd -P || true)

if [[ -n "$SESSIONS_ROOT" && -d "/proc/$PPID/fd" ]]; then
    declare -A seen=()
    pid=$PPID
    for _ in 1 2 3 4 5; do
        [[ "$pid" -gt 1 ]] || break
        if [[ -d "/proc/$pid/fd" ]]; then
            for fd in /proc/$pid/fd/*; do
                target=$(readlink -f "$fd" 2>/dev/null) || continue
                # Reject anything not under sessions root
                [[ "$target" == "$SESSIONS_ROOT"/* ]] || continue
                # Extract trailing UUID (validate strict shape)
                cand=$(printf '%s\n' "$target" | sed -nE \
                    's|.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$|\1|p')
                if [[ -n "$cand" ]] && valid "$cand"; then
                    seen["$cand"]=1
                fi
            done
        fi
        pid=$(awk '/^PPid:/{print $2}' "/proc/$pid/status" 2>/dev/null || echo 1)
    done

    case "${#seen[@]}" in
        1)
            for k in "${!seen[@]}"; do echo "$k"; done
            exit 0
            ;;
        0)
            : # fall through to manual-fallback message below
            ;;
        *)
            {
                echo "discover-thread: parent codex appears to hold MORE THAN ONE"
                echo "open rollout fd under $SESSIONS_ROOT. Refusing to guess."
                echo "Candidate thread_ids:"
                for k in "${!seen[@]}"; do echo "  - $k"; done
                echo "Next: use /status in codex to identify the correct id"
                echo "and call driver.bind_thread(thread_id=...) directly."
            } >&2
            exit 2
            ;;
    esac
fi

# All sources failed — explicit fail with operator-actionable message.
{
    echo "discover-thread: could not determine the parent codex thread_id."
    echo ""
    echo "Tried:"
    echo "  1. \$CODEX_THREAD_ID env (unset or malformed)"
    if [[ -d "/proc/$PPID/fd" ]]; then
        if [[ -z "${SESSIONS_ROOT:-}" ]]; then
            echo "  2. /proc/<ppid>/fd scan (skipped — \$CODEX_HOME/sessions missing)"
        else
            echo "  2. /proc/<ppid>/fd scan under $SESSIONS_ROOT (no match)"
        fi
    else
        echo "  2. /proc/<ppid>/fd scan (skipped — not on Linux)"
    fi
    echo ""
    echo "Next steps:"
    echo "  - In your codex session, type /status. Find the line"
    echo "    starting with 'thread_id:' and copy the UUID."
    echo "  - Then call driver.bind_thread(thread_id=<that uuid>) manually."
} >&2
exit 1
DISCOVER_EOF
```

It prints the thread_id to stdout, or exits non-zero with an actionable
message on stderr. Exit codes: 0 = success, 1 = no source resolved,
2 = multi-candidate invariant violation.

**Step 2** — read the UUID from Step 1's stdout, then call `bind_thread`
with that value as a literal argument. Expect `{ "bound": true,
"thread_id": "..." }` in the response:

```
driver.bind_thread(thread_id="<paste the UUID from Step 1's stdout here>")
```

If Step 1 exits non-zero (typical on macOS / Windows without
`CODEX_THREAD_ID` set, or in containers without /proc access), or if
running on native Windows (cmd / PowerShell, no bash — skip Step 1
entirely), ask the user:

> "I need your current Codex thread id to set up parent linkage.
>  In your codex session, run `/status` and paste the UUID after
>  `thread_id:` here."

Then call `bind_thread` with that value.

If you skip this step entirely, every parent-link submission will fail
with: "driver not bound to a codex thread; call `bind_thread` first ..."

**Re-binding mid-session** (e.g. after `/resume` switches to a different
codex thread) is supported: just re-run discovery and call `bind_thread`
again — the new value replaces the old one.

[script]: scripts/discover-thread.sh
````

The body between `bash <<'DISCOVER_EOF'` and `DISCOVER_EOF` (inclusive of the shebang line, exclusive of the markdown fence) MUST equal `cat skills/multiagent/scripts/discover-thread.sh`. Copy the file content verbatim — do not edit during paste.

- [ ] **Step 2: Verify byte-identity manually**

```bash
diff -u skills/multiagent/scripts/discover-thread.sh \
    <(awk '/^## Initialization/{p=1} p && /<<\047DISCOVER_EOF\047/{flag=1; next} flag && /^DISCOVER_EOF$/{exit} flag' skills/multiagent/SKILL.md)
```

Expected: empty output (no diff). If anything shows up, fix the SKILL.md heredoc to match the script file exactly.

- [ ] **Step 3: Render check**

```bash
grep -n '## Initialization' skills/multiagent/SKILL.md
grep -n '## Default Workflow' skills/multiagent/SKILL.md
```

Expected: Initialization appears BEFORE Default Workflow.

- [ ] **Step 4: Commit**

```bash
git add skills/multiagent/SKILL.md
git commit -m "docs(skills): SKILL.md Initialization section with inline discover-thread heredoc (#24)"
```

### Task 12: SKILL.md ⇄ discover-thread.sh byte-identical CI gate

Standalone CI script that extracts the heredoc body from SKILL.md and diffs it against the canonical script file. Any drift fails the test.

**Files:**
- Create: `skills/multiagent/scripts/skill_md_inline_in_sync_test.sh`

**Interfaces:**
- Consumes: `SKILL.md` Initialization section heredoc (Task 11), `discover-thread.sh` (Task 9).
- Produces: a CI gate. Exit 0 on byte-identical; exit 1 with a unified diff on stderr otherwise.

- [ ] **Step 1: Create the test**

Create `skills/multiagent/scripts/skill_md_inline_in_sync_test.sh`:

```bash
#!/usr/bin/env bash
# skill_md_inline_in_sync_test.sh — assert the inlined heredoc body in
# SKILL.md is byte-identical to scripts/discover-thread.sh. Without this
# gate the inline copy can silently drift and the model would run a stale
# script.

set -euo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SKILL_DIR="$(cd "$TEST_DIR/.." && pwd -P)"
SCRIPT_FILE="$TEST_DIR/discover-thread.sh"
SKILL_MD="$SKILL_DIR/SKILL.md"

if [[ ! -f "$SCRIPT_FILE" ]]; then
    echo "skill_md_inline_in_sync_test: $SCRIPT_FILE missing" >&2
    exit 1
fi
if [[ ! -f "$SKILL_MD" ]]; then
    echo "skill_md_inline_in_sync_test: $SKILL_MD missing" >&2
    exit 1
fi

# Extract the heredoc body. Awk state machine:
#   p=1 after "## Initialization" header
#   flag=1 after the opening `bash <<'DISCOVER_EOF'`
#   exit when we hit the closing `DISCOVER_EOF` line.
inlined=$(awk '
    /^## Initialization/ {p=1; next}
    p && /<<\047DISCOVER_EOF\047/ {flag=1; next}
    flag && /^DISCOVER_EOF$/ {exit}
    flag {print}
' "$SKILL_MD")

if [[ -z "$inlined" ]]; then
    echo "skill_md_inline_in_sync_test: could not extract heredoc body from $SKILL_MD" >&2
    echo "Expected a 'bash <<'\''DISCOVER_EOF'\''' block inside the '## Initialization' section." >&2
    exit 1
fi

# Compare byte-for-byte. Use diff for a readable failure message.
if ! diff -u "$SCRIPT_FILE" <(printf '%s\n' "$inlined") >/tmp/skill-drift.$$.diff 2>&1; then
    {
        echo "skill_md_inline_in_sync_test: FAILED — SKILL.md heredoc body has drifted from $SCRIPT_FILE."
        echo "Either copy the canonical script into the heredoc, or update the script and re-paste."
        echo "---"
        cat /tmp/skill-drift.$$.diff
    } >&2
    rm -f /tmp/skill-drift.$$.diff
    exit 1
fi

rm -f /tmp/skill-drift.$$.diff
echo "ok - SKILL.md heredoc matches discover-thread.sh"
```

- [ ] **Step 2: Run it**

```bash
chmod +x skills/multiagent/scripts/skill_md_inline_in_sync_test.sh
skills/multiagent/scripts/skill_md_inline_in_sync_test.sh
echo "exit=$?"
```

Expected: `ok - SKILL.md heredoc matches discover-thread.sh`, exit 0. If it fails, fix the SKILL.md heredoc per the diff before continuing.

- [ ] **Step 3: Negative-path verification**

Temporarily mutate the script to verify the gate fires:

```bash
echo "# DRIFT" >> skills/multiagent/scripts/discover-thread.sh
skills/multiagent/scripts/skill_md_inline_in_sync_test.sh
echo "exit=$?"   # expect non-zero
# Undo
git checkout -- skills/multiagent/scripts/discover-thread.sh
```

Expected: failure exit with diff output. Then revert.

- [ ] **Step 4: Wire into CI**

Append to the same CI step (or Makefile target) as Task 10's discover-thread tests:

```yaml
- name: SKILL.md inline heredoc drift check
  run: skills/multiagent/scripts/skill_md_inline_in_sync_test.sh
```

- [ ] **Step 5: Commit**

```bash
git add skills/multiagent/scripts/skill_md_inline_in_sync_test.sh
# also include CI workflow change if separate
git commit -m "test(skills): assert SKILL.md heredoc matches discover-thread.sh (#24)"
```

### Task 13: Q2 — `applyCodexSessionMeta` removes the codex_exec branch

This task is the first half of Q2's classifier change. `applyCodexSessionMeta` currently blanket-maps `originator=codex_exec || source=exec → SessionOriginAgentTask`. The fix: drop that branch and default to `User`. Sidecar-driven upgrades to `AgentTask` move to `applyLoomMeta` (Task 14).

This task by itself shifts pre-P1 historical exec rollouts (those without a sidecar) from AgentTask → User. That's the accepted semantic correction the spec calls out; the fixture update below makes it visible.

**Intermediate-state warning.** Task 13 ALONE breaks every sidecar-dependent test, NOT just the `TestGetSession_CodexExecManifest…` rename. The reason: the legacy `applyLoomMeta` still gates the merge on `sess.Origin == AgentTask` (`sessions.go:345-347`), and after Task 13 nothing pre-classifies exec rollouts as `AgentTask`, so sidecar fixtures stop being applied. Task 14 fixes both — it replaces the `Origin == AgentTask` precondition with the explicit `rolloutIsExec` gate AND un-skips all the tests Task 13 skipped here.

Two integration options — pick one before starting:

- **Option A (recommended) — split commits, skip-and-unskip.** Task 13 commits the classifier change with an explicit `t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")` on each affected test. Task 14 removes every skip in the same commit. Reviewers get a clean "delete branch / add gate" diff at the cost of one commit landing with skips. The skips MUST be removed in Task 14 to keep PR #31's three-commit story honest — never let a skip survive into merge.
- **Option B — atomic Q2 commit.** Combine Tasks 13 and 14 into one commit (`fix(codex): loom-meta sidecar confirms agent_task only for exec rollouts (#24)`). No skips needed; the diff is larger but every intermediate state is green. Pick this if reviewers prefer atomic functional changes over staged decomposition.

If Option A is chosen (default), the **exhaustive list** of affected tests in `multi-agent/pkg/agentbackend/codex/sessions_test.go` that Task 13 MUST skip and Task 14 MUST un-skip:

| Line | Test | Why it breaks under Task 13 alone | Resolution |
|---|---|---|---|
| `:305` | `TestGetSession_CodexExecManifestIsAgentTaskAndTitleSkipsManifest` | Asserts `AgentTask` for exec rollout without sidecar; new default is `User` | Renamed `…WithSidecar…` + sidecar fixture + skip; un-skipped in T14 |
| `:348` | `TestListSessionsMergesLoomMetaSidecar` | Sidecar fixture present, but `applyLoomMeta`'s `Origin == AgentTask` gate fails because the rollout no longer pre-classifies | Skip in T13; un-skip in T14 (Gate 2 now allows merge via `rolloutIsExec`) |
| `:447` | `TestListCacheInvalidatedBySidecarRewrite` | Same sidecar-merge dependency; cache test relies on sidecar merge producing observable parent fields | Skip in T13; un-skip in T14 |
| `:737` | `TestListSessionsLoomMetaMergesPartialParentLink` | Same sidecar-merge dependency; verifies per-field merge under exec rollout | Skip in T13; un-skip in T14 |
| `:390` | `TestListSessionsSidecarDoesNotRelabelUserSession` | **No skip needed.** Test asserts the sidecar does NOT upgrade an interactive session; under Task 13 alone the same outcome holds (`Origin == AgentTask` gate fails → no merge → stays `User`). Stays green throughout. |

If a previously-unmentioned test references `originator: "codex_exec"` AND asserts `AgentTask` AND merges a sidecar, treat it identically — add to the skip list here, un-skip it in Task 14.

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/sessions.go::applyCodexSessionMeta` (currently `:326-348`)
- Modify: `multi-agent/pkg/agentbackend/codex/sessions_test.go` — rename / update `TestGetSession_CodexExecManifestIsAgentTaskAndTitleSkipsManifest` (`:305`); add `TestApplyCodexSessionMeta_ExecRunWithoutSidecarIsUser`; skip the three sidecar-merge tests listed above

**Interfaces:**
- Consumes: existing `codexMetaPayload`, `agentbackend.SessionOriginUser/Subagent/AgentTask` consts.
- Produces: a classifier that:
  - keeps the existing `thread_source=subagent` / `parent_thread_id` / `agent_nickname` branch (which sets `SessionOriginSubagent`) unchanged,
  - removes the `originator=codex_exec || source=exec` branch entirely,
  - defaults `sess.Origin` to `User` when nothing else classified it.

Why split from Task 14: Task 13 is a deletion + a one-line default. Reviewing it on its own confirms the spec's "accepted semantic shift" is intentional. Task 14 then adds the new sidecar-gated upgrade path.

- [ ] **Step 1: Add the new positive/negative tests**

Append to `multi-agent/pkg/agentbackend/codex/sessions_test.go`:

```go
// TestApplyCodexSessionMeta_ExecRunWithoutSidecarIsUser codifies Q2's
// accepted semantic shift: a codex_exec rollout WITHOUT a loom-meta
// sidecar is no longer classified as AgentTask. Defaults to User.
func TestApplyCodexSessionMeta_ExecRunWithoutSidecarIsUser(t *testing.T) {
	sess := &agentbackend.Session{}
	applyCodexSessionMeta(sess, codexMetaPayload{
		Originator: "codex_exec",
		Source:     codexMetaSource{Kind: "exec"},
	})
	require.Equal(t, agentbackend.SessionOriginUser, sess.Origin,
		"exec-mode rollout without sidecar must default to User now")
}

// TestApplyCodexSessionMeta_SubagentClassificationUnchanged ensures the
// codex-native subagent branch keeps its priority — it must NOT be
// downgraded by the new default.
func TestApplyCodexSessionMeta_SubagentClassificationUnchanged(t *testing.T) {
	sess := &agentbackend.Session{}
	applyCodexSessionMeta(sess, codexMetaPayload{
		ThreadSource:   "subagent",
		ParentThreadID: "parent-thr",
		AgentNickname:  "Lover",
	})
	require.Equal(t, agentbackend.SessionOriginSubagent, sess.Origin)
	require.Equal(t, "parent-thr", sess.ParentID)
	require.Equal(t, "Lover", sess.AgentName)
}
```

- [ ] **Step 2: Update / rename the existing exec-mode assertion test**

Locate `TestGetSession_CodexExecManifestIsAgentTaskAndTitleSkipsManifest` (`multi-agent/pkg/agentbackend/codex/sessions_test.go:305`). Its current premise — that an exec rollout WITHOUT a sidecar is `AgentTask` — is exactly what Q2 inverts. Two updates:

(a) Add a loom-meta sidecar fixture so the test still asserts `AgentTask` (Task 14 will make this work via the new gate); rename to `TestGetSession_CodexExecWithSidecarIsAgentTaskAndTitleSkipsManifest`.

(b) Until Task 14 plumbs `rolloutIsExec` into `applyLoomMeta`, this test will FAIL even with the sidecar (gate 2 from Task 14 isn't in place yet). Mark it `t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")` temporarily, with a TODO removing the skip in Task 14.

Pseudo-shape:

```go
func TestGetSession_CodexExecWithSidecarIsAgentTaskAndTitleSkipsManifest(t *testing.T) {
	t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")
	home := t.TempDir()
	setTestHome(t, home)
	// IMPORTANT: this fixture uses setTestHome + the default-CodexHome
	// resolution path. The rollout is written to home/.codex/sessions/...,
	// and EffectiveCodexHome(cfg, env) returns filepath.Join(home, ".codex")
	// when cfg.CodexHome is empty (codexenv.go:28). The sidecar must live
	// under the SAME base — otherwise applyLoomMeta will read
	// home/.codex/loom-meta/<id>.json and the sidecar at home/loom-meta/...
	// will be invisible to it.
	codexHome := filepath.Join(home, ".codex")
	dir := filepath.Join(codexHome, "sessions", "2026", "06", "17")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	sessionID := "abababab-1111-2222-3333-cdcdcdcdcdcd"
	// ... existing rollout body write unchanged, into `dir` above ...

	// NEW — sidecar lives under codexHome, NOT under home:
	require.NoError(t, writeLoomMeta(codexHome, loomMeta{
		Schema:    loomMetaSchema,
		Kind:      "codex",
		Origin:    "agent_task",
		SessionID: sessionID,
	}))

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	// ... existing ListSessions / GetSession assertions unchanged ...
}
```

Two pitfalls the implementer MUST avoid:

1. **Sidecar base.** `writeLoomMeta(base, ...)` writes to `<base>/loom-meta/<sessionID>.json`. When the rollout uses the default `setTestHome` pattern (no `cfg.CodexHome` override), the effective base is `filepath.Join(home, ".codex")`, NOT `home`. Always compute `codexHome := filepath.Join(home, ".codex")` and pass it. The sibling test `TestListSessionsMergesLoomMetaSidecar` (`sessions_test.go:349`) takes a different shape — it sets `cfg.CodexHome = home` and writes the rollout under `home/sessions/...` AND the sidecar under `home` — so `writeLoomMeta(home, …)` is correct there. Pick the base that matches the rollout location.
2. **`writeLoomMeta` silently no-ops on malformed input.** Required-field gate is `m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID == ""` (`loommeta.go:37`). Wrap every call in `require.NoError(t, …)` AND fill all four fields — otherwise the helper returns nil, writes nothing, and the test reads a missing sidecar.

There is no `writeSidecarForTest` in the codebase; do NOT introduce one. Use `writeLoomMeta` directly.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd multi-agent && go test ./pkg/agentbackend/codex/ -run 'TestApplyCodexSessionMeta_ExecRunWithoutSidecarIsUser|TestApplyCodexSessionMeta_SubagentClassificationUnchanged' -v`
Expected: FAIL on the user-default test (current code returns AgentTask). Subagent test passes.

- [ ] **Step 4: Delete the codex_exec branch**

In `multi-agent/pkg/agentbackend/codex/sessions.go::applyCodexSessionMeta`, delete the `if p.Originator == "codex_exec" || p.Source.Kind == "exec"` block:

```go
func applyCodexSessionMeta(sess *agentbackend.Session, p codexMetaPayload) {
	spawn := p.Source.Subagent.ThreadSpawn
	parentID := firstNonEmpty(p.ParentThreadID, spawn.ParentThreadID)
	if p.ThreadSource == "subagent" || parentID != "" || p.AgentNickname != "" || spawn.AgentNickname != "" {
		sess.Origin = agentbackend.SessionOriginSubagent
		sess.ParentID = parentID
		sess.AgentName = firstNonEmpty(p.AgentNickname, spawn.AgentNickname)
		sess.AgentRole = firstNonEmpty(p.AgentRole, spawn.AgentRole)
		return
	}
	// REMOVED in Q2: codex_exec / source==exec → SessionOriginAgentTask.
	// agent_task is now granted exclusively by applyLoomMeta when a sidecar
	// confirms an exec-mode rollout originated from a loom agent (Task 14).
	if sess.Origin == "" {
		sess.Origin = agentbackend.SessionOriginUser
	}
}
```

- [ ] **Step 5: Run the targeted tests**

Run: `cd multi-agent && go test ./pkg/agentbackend/codex/ -run 'TestApplyCodexSessionMeta' -v`
Expected: PASS for both new tests. The renamed `TestGetSession_CodexExecWithSidecar…` is `SKIP` (enabled by Task 14).

- [ ] **Step 6: Add the temporary skips to the three sidecar-merge tests**

Per the Intermediate-state warning at the top of Task 13, add `t.Skip(...)` at the top of each of these three test functions. Every skip uses the SAME message string so the un-skip pass in Task 14 is a single `grep | sed` (or manual find-and-delete):

```go
// :348 — TestListSessionsMergesLoomMetaSidecar
func TestListSessionsMergesLoomMetaSidecar(t *testing.T) {
	t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")
	// ... existing body unchanged ...
}

// :447 — TestListCacheInvalidatedBySidecarRewrite
func TestListCacheInvalidatedBySidecarRewrite(t *testing.T) {
	t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")
	// ... existing body unchanged ...
}

// :737 — TestListSessionsLoomMetaMergesPartialParentLink
func TestListSessionsLoomMetaMergesPartialParentLink(t *testing.T) {
	t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")
	// ... existing body unchanged ...
}
```

Do NOT skip `TestListSessionsSidecarDoesNotRelabelUserSession` (`:390`) — under Task 13 alone it still passes (sidecar merge fails → session stays User → matches assertion).

If Option B (atomic commit) was chosen, skip this step entirely and combine the Task 14 commit message into one.

- [ ] **Step 7: Run the full codex suite**

Run: `cd multi-agent && go test ./pkg/agentbackend/codex/...`
Expected: PASS. The skips swallow the three known-failing sidecar tests; the renamed `…WithSidecar…` is also skipped; the surviving `TestListSessionsSidecarDoesNotRelabelUserSession` stays green. If ANY other test fails:

- Check whether it references `originator: "codex_exec"` AND asserts `AgentTask` AND merges a sidecar.
- If yes → add to the skip list AND to Task 14's un-skip list (keep the lists in lockstep).
- If no → it's an unexpected regression; debug.

Document any test you skipped beyond the three listed above in the commit message body so Task 14 knows what else to un-skip.

- [ ] **Step 8: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/sessions.go \
        multi-agent/pkg/agentbackend/codex/sessions_test.go
git commit -m "fix(codex): drop exec-mode blanket promotion to AgentTask (#24)

Pre-P1 historical exec rollouts (no loom-meta sidecar) now classify as
User. Sidecar-driven AgentTask upgrade follows in the next commit; four
sidecar-merge tests are temporarily skipped with marker message
'enabled by Task 14 — applyLoomMeta sidecar+exec gate' and un-skipped
in the next commit (PR #31 ships them green)."
```

### Task 14: Q2 — `applyLoomMeta` gates + `rolloutIsExec` plumbing

This task adds the two gates `applyLoomMeta` needs to safely upgrade exec rollouts to `AgentTask` and threads the `rolloutIsExec` boolean from the read loop.

- **Gate 1** preserves the codex-native subagent classification — a sidecar cannot demote or relabel a session already marked `SessionOriginSubagent`.
- **Gate 2** restricts the upgrade to exec-mode rollouts only — interactive sessions can never be promoted by a stale or corrupted sidecar.

The boolean is OR-accumulated across the loop so a later `session_meta` record that lacks exec fields cannot demote an earlier `true`.

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/sessions.go::applyLoomMeta` (currently `:350-379`)
- Modify: `multi-agent/pkg/agentbackend/codex/sessions.go::scanCodexSession` (real signature: `func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult` — returns value, not pointer; `base` is already a parameter and feeds `applyLoomMeta`)
- Modify: `multi-agent/pkg/agentbackend/codex/sessions_test.go` — un-skip the renamed test from Task 13; add the matrix-row tests and the OR-accumulator test.

**Interfaces:**
- Consumes: `loomMeta` (existing in `loommeta.go`), `agentbackend.Session*` consts.
- Produces: `applyLoomMeta(base string, sess *agentbackend.Session, rolloutIsExec bool)` — note the new third parameter.

This task changes the **`applyLoomMeta`** signature (adds `rolloutIsExec bool`); every caller must be updated in the same commit. There's exactly one caller — inside `scanCodexSession`. `scanCodexSession`'s own signature stays as today (`func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult`); the `rolloutIsExec` boolean is a function-local declared just before the read loop, not a parameter.

- [ ] **Step 1: Write the new matrix-row + OR-accumulator tests**

Append to `multi-agent/pkg/agentbackend/codex/sessions_test.go`:

```go
// TestApplyLoomMeta_PreservesSubagentClassification — Gate 1: a sidecar
// must never demote or relabel a codex-native subagent.
func TestApplyLoomMeta_PreservesSubagentClassification(t *testing.T) {
	base := t.TempDir()
	sess := &agentbackend.Session{
		ID:     "sess-1",
		Origin: agentbackend.SessionOriginSubagent,
	}
	require.NoError(t, writeLoomMeta(base, loomMeta{
		Schema: loomMetaSchema, Kind: "codex",
		Origin: "agent_task", SessionID: "sess-1",
		ParentAgentID: "drv-1", ParentDisplayName: "Driver",
		ParentSessionID: "thr-X",
	}))
	applyLoomMeta(base, sess, true /*rolloutIsExec*/)
	require.Equal(t, agentbackend.SessionOriginSubagent, sess.Origin)
}

// TestApplyLoomMeta_RequiresExecModeRollout — Gate 2: an interactive
// (non-exec) rollout must not be upgraded even if a sidecar is present.
func TestApplyLoomMeta_RequiresExecModeRollout(t *testing.T) {
	base := t.TempDir()
	sess := &agentbackend.Session{
		ID:     "sess-1",
		Origin: agentbackend.SessionOriginUser,
	}
	require.NoError(t, writeLoomMeta(base, loomMeta{
		Schema: loomMetaSchema, Kind: "codex",
		Origin: "agent_task", SessionID: "sess-1",
		ParentSessionID: "thr-X",
	}))
	applyLoomMeta(base, sess, false /*rolloutIsExec*/)
	require.Equal(t, agentbackend.SessionOriginUser, sess.Origin,
		"non-exec rollouts must not be upgraded to AgentTask")
}

// TestApplyLoomMeta_UpgradesExecModeWithSidecar — happy path.
func TestApplyLoomMeta_UpgradesExecModeWithSidecar(t *testing.T) {
	base := t.TempDir()
	sess := &agentbackend.Session{
		ID:     "sess-1",
		Origin: agentbackend.SessionOriginUser,
	}
	require.NoError(t, writeLoomMeta(base, loomMeta{
		Schema: loomMetaSchema, Kind: "codex",
		Origin: "agent_task", SessionID: "sess-1",
		ParentSessionID: "thr-X", ParentAgentID: "drv-1", ParentDisplayName: "Driver",
	}))
	applyLoomMeta(base, sess, true /*rolloutIsExec*/)
	require.Equal(t, agentbackend.SessionOriginAgentTask, sess.Origin)
	require.Equal(t, "thr-X", sess.ParentID)
	require.Equal(t, "drv-1", sess.ParentAgentID)
	require.Equal(t, "Driver", sess.ParentDisplayName)
}

// TestScanCodexSession_MultiSessionMeta_ExecFlagAccumulates — when a
// rollout contains multiple session_meta records, OR-accumulator ensures
// a later record without exec fields cannot demote an earlier true.
func TestScanCodexSession_MultiSessionMeta_ExecFlagAccumulates(t *testing.T) {
	base := t.TempDir()
	sessID := "sess-multi"
	// Write a rollout file with TWO session_meta records: first has
	// originator=codex_exec, second has none.
	rollout := filepath.Join(base, "sessions/2026/06/23/rollout-2026-06-23T17-08-20-"+sessID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(rollout), 0o755))
	require.NoError(t, os.WriteFile(rollout, []byte(strings.Join([]string{
		`{"timestamp":"2026-06-23T17:08:20Z","type":"session_meta","payload":{"id":"`+sessID+`","originator":"codex_exec","source":{"kind":"exec"}}}`,
		`{"timestamp":"2026-06-23T17:08:21Z","type":"session_meta","payload":{"id":"`+sessID+`"}}`,
	}, "\n")+"\n"), 0o644))
	require.NoError(t, writeLoomMeta(base, loomMeta{
		Schema: loomMetaSchema, Kind: "codex",
		Origin: "agent_task", SessionID: sessID,
	}))

	res := scanCodexSession(rollout, sessID, false /*withMessages*/, base)
	require.Equal(t, agentbackend.SessionOriginAgentTask, res.session.Origin,
		"rolloutIsExec must remain true after a later session_meta without exec fields")
}
```

- [ ] **Step 2: Un-skip every test Task 13 marked**

Delete the `t.Skip("enabled by Task 14 — applyLoomMeta sidecar+exec gate")` line from each of the four tests Task 13 skipped:

```bash
# Find them all in one shot. The marker message is identical on purpose.
cd multi-agent
grep -n 'enabled by Task 14 — applyLoomMeta sidecar+exec gate' \
    pkg/agentbackend/codex/sessions_test.go
```

Expected hits (four total — the three sidecar-merge tests plus the renamed `…WithSidecar…`). Delete each skip line. Mandatory list:

1. `TestGetSession_CodexExecWithSidecarIsAgentTaskAndTitleSkipsManifest`
2. `TestListSessionsMergesLoomMetaSidecar` (`:348`)
3. `TestListCacheInvalidatedBySidecarRewrite` (`:447`)
4. `TestListSessionsLoomMetaMergesPartialParentLink` (`:737`)

PLUS any test the implementer added to the list in Task 13 Step 7 (commit message body documents extras). Final post-Task-14 invariant: `grep -n 'enabled by Task 14' pkg/agentbackend/codex/sessions_test.go` returns zero hits. If a hit survives the commit, PR #31 ships with a permanent skip — that fails the spec's "every commit ships its own tests" contract.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd multi-agent && go test ./pkg/agentbackend/codex/ -run 'TestApplyLoomMeta_|TestScanCodexSession_MultiSessionMeta|TestGetSession_CodexExecWithSidecar' -v`
Expected: FAIL — `applyLoomMeta` signature mismatch and the rollout-tests fail (current code uses the old signature and doesn't honor exec-mode gating).

- [ ] **Step 4: Patch `applyLoomMeta`**

In `multi-agent/pkg/agentbackend/codex/sessions.go`, replace `applyLoomMeta`:

```go
// applyLoomMeta upgrades sess.Origin to AgentTask iff:
//   - a valid loom-meta sidecar exists for sess.ID,
//   - sess is not already classified as Subagent (Gate 1 — codex-native
//     subagent priority, enshrined by TestListSessionsSidecarDoesNotRelabelUserSession),
//   - the rollout is exec-mode (Gate 2 — prevents stray sidecars from
//     upgrading interactive sessions).
//
// Parent-link fields (ParentID, ParentAgentID, ParentDisplayName) merge
// independently — each is only filled when empty so codex-native subagent
// ParentID set by applyCodexSessionMeta is never overwritten.
func applyLoomMeta(base string, sess *agentbackend.Session, rolloutIsExec bool) {
	if base == "" || sess == nil {
		return
	}
	m, ok := readLoomMeta(base, sess.ID)
	if !ok {
		return
	}
	if m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID != sess.ID {
		return
	}
	// Gate 1: codex-native subagent classification is authoritative.
	if sess.Origin == agentbackend.SessionOriginSubagent {
		return
	}
	// Gate 2: only exec-mode rollouts are eligible for sidecar upgrade.
	if !rolloutIsExec {
		return
	}

	sess.Origin = agentbackend.SessionOriginAgentTask
	if m.ParentSessionID != "" && sess.ParentID == "" {
		sess.ParentID = m.ParentSessionID
	}
	if m.ParentAgentID != "" && sess.ParentAgentID == "" {
		sess.ParentAgentID = m.ParentAgentID
	}
	if m.ParentDisplayName != "" && sess.ParentDisplayName == "" {
		sess.ParentDisplayName = m.ParentDisplayName
	}
}
```

- [ ] **Step 5: Plumb `rolloutIsExec` through `scanCodexSession`**

In `scanCodexSession` (currently `multi-agent/pkg/agentbackend/codex/sessions.go:246-377`), keep the existing signature `func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult` — `base` is already a parameter and is what feeds `applyLoomMeta`. Declare `rolloutIsExec` as a function-local just before the read loop, OR-accumulate inside the `session_meta` case, and pass it into `applyLoomMeta` after the loop:

```go
func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult {
	res := codexScanResult{session: agentbackend.Session{
		ID:     fallbackID,
		Kind:   agentbackend.KindCodex,
		Origin: agentbackend.SessionOriginUser,
	}}

	f, err := os.Open(path)
	if err != nil {
		return res
	}
	defer f.Close()

	var rolloutIsExec bool   // ← NEW: OR-accumulated across all session_meta records
	var lastAssistantText string
	rd := bufio.NewReader(f)
	for {
		line, readErr := sessionjsonl.ReadLine(rd, sessionjsonl.MaxLineBytes)
		if len(line) > 0 {
			var ln codexLine
			if err := json.Unmarshal(line, &ln); err != nil {
				continue
			}
			ts := parseTimestamp(ln.Timestamp)
			switch ln.Type {
			case "session_meta":
				var p codexMetaPayload
				if err := json.Unmarshal(ln.Payload, &p); err != nil {
					continue
				}
				if p.ID != "" {
					res.session.ID = p.ID
				}
				if p.Cwd != "" {
					res.session.WorkingDir = p.Cwd
				}
				// NEW: OR-accumulate; a later record without exec fields
				// must not be able to demote an earlier true.
				rolloutIsExec = rolloutIsExec || p.Originator == "codex_exec" || p.Source.Kind == "exec"
				applyCodexSessionMeta(&res.session, p)
				if res.session.StartedAt.IsZero() && !ts.IsZero() {
					res.session.StartedAt = ts
				}
			case "user_input":
				// ... [unchanged]
			case "model_output":
				// ... [unchanged]
			case "response_item":
				// ... [unchanged]
			}
		}
		if readErr != nil {
			break
		}
	}
	if lastAssistantText != "" {
		res.session.Preview = truncatePreview(lastAssistantText)
	}
	applyLoomMeta(base, &res.session, rolloutIsExec) // ← signature change
	return res
}
```

- [ ] **Step 6: Run the targeted tests**

Run: `cd multi-agent && go test ./pkg/agentbackend/codex/ -run 'TestApplyLoomMeta_|TestScanCodexSession_MultiSessionMeta|TestGetSession_CodexExecWithSidecar' -v`
Expected: PASS.

- [ ] **Step 7: Run the full codex package suite**

Run: `cd multi-agent && go test ./pkg/agentbackend/codex/...`
Expected: PASS. In particular:
- `TestListSessionsSidecarDoesNotRelabelUserSession` must STAY green — gate 2 explicitly upholds it.
- `TestListSessionsLoomMetaMergesPartialParentLink` must STAY green — the per-field merge logic is unchanged for exec-mode rollouts.

- [ ] **Step 8: Run the full repo suite**

Run: `cd multi-agent && go test ./...`
Expected: PASS.

- [ ] **Step 9: Verify the classification matrix manually**

The spec's matrix (excerpt):

| Rollout signal | Sidecar? | After |
|---|---|---|
| `thread_source=subagent` / `parent_thread_id` set | n/a | Subagent (Gate 1) |
| `source=cli` (interactive) | absent | User |
| `source=cli` (interactive) | present (stale/corrupted) | User (Gate 2 blocks) |
| `source=exec, originator=codex_exec` | absent | **User** (Q2 fix) |
| `source=exec, originator=codex_exec` | present (origin=agent_task) | AgentTask + parent fields merged |
| Pre-P1 historical exec rollouts | absent | **User** |

Tests cover rows 1, 3, 4 (Tasks 13-14), 5 (Tasks 13-14), 6 (Task 13). Spot-check by skimming `TestListSessionsSidecarDoesNotRelabelUserSession` for row 3 and the existing partial-merge tests for row 5; add fixture rows if any are missing.

- [ ] **Step 10: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/sessions.go \
        multi-agent/pkg/agentbackend/codex/sessions_test.go
git commit -m "fix(codex): loom-meta sidecar confirms agent_task only for exec rollouts (#24)

Adds Gate 1 (Subagent priority preserved) and Gate 2 (exec-mode required)
to applyLoomMeta. OR-accumulates rolloutIsExec across session_meta records
in scanCodexSession to handle multi-record rollouts."
```

### Task 15: E2E fixture update + PR commit shaping

Final integration check using the existing PR #31 `prod_test` stack. Verifies the three acceptance criteria end-to-end against live driver + slave + Commander UI. Also reshapes the local commit log so the three-commit story lands cleanly in PR #31.

**Files:**
- Modify: driver-side skill instructions used by `multi-agent/tests/prod_test/` (the exact file depends on how the prod_test stack distributes the multiagent skill — likely a copy of `skills/multiagent/SKILL.md` or a thin wrapper around it). Identify with:
  ```bash
  rg -n "Default Workflow|## Core Rule" multi-agent/tests/prod_test/
  ```
- Optionally extend any e2e harness (`multi-agent/tests/prod_test/`) with assertions on driver origin + slave parent_session_id.

**Interfaces:**
- Consumes: every prior task — bind_thread + discover-thread + SKILL.md init + sidecar gates.
- Produces: e2e green; PR #31 has exactly three Q1/Q2 commits.

- [ ] **Step 1: Update the prod_test driver-side skill instructions**

If `multi-agent/tests/prod_test/` ships its own copy of `SKILL.md` (or a tailored variant), apply the Task 11 Initialization section to it. If it loads the skill from `skills/multiagent/`, no change is needed — the canonical SKILL.md already has the section.

- [ ] **Step 2: Run the prod_test e2e flow**

Bring up the prod_test stack per its existing README:

```bash
cd multi-agent/tests/prod_test
./run.sh   # or whatever the stack's entry point is
```

Drive the scenario: `driver codex exec → submit_task slave-A → submit_task slave-B → wait_task`. The driver-side codex MUST call `bind_thread` first (via the Initialization step) before either submit_task.

- [ ] **Step 3: Verify the three acceptance criteria**

1. **Driver session origin = `user`** (Q2 acceptance). Inspect the driver's session metadata via Commander or directly:
   ```bash
   # whatever query the existing prod_test uses to fetch a session by id;
   # the field is `origin` and must equal "user"
   ```
2. **Both slave sessions' sidecars contain a non-empty `parent_session_id`** equal to the driver's current thread_id (Q1 acceptance). Find the sidecar files:
   ```bash
   ls ~/.codex/loom-meta/    # or per-slave CODEX_HOME path
   ```
   Each `.json` sidecar for an `agent_task` slave session must have `parent_session_id` set to the same UUID the driver bound.
3. **Commander UI shows the two slave sessions nested under the driver session, default-collapsed.** Open the Commander UI and visually confirm.

Capture screenshots / dumps for the PR description.

- [ ] **Step 4: Tear down and re-test without bind_thread (negative)**

Restart the prod_test stack but instruct the driver-side codex skill to SKIP the Initialization step. Submit a `submit_task` with `skill="chat"`. Expected: actionable error `"driver not bound to a codex thread; call bind_thread first ..."`. The error must surface in the driver-side conversation transcript so the model can recover.

- [ ] **Step 5: Reshape commits for PR #31**

Tasks 1-8 produced multiple driver-side commits; the spec asks for ONE commit per concern (driver, skill, codex). Use interactive rebase:

```bash
git rebase -i HEAD~<N>   # N = number of commits since branching from PR #31's base
```

Squash into exactly three:

```
feat(driver): bind_thread MCP tool + submit/resume/contract guards (#24)
feat(skills): discover-thread.sh + SKILL.md init step for multiagent (#24)
fix(codex): loom-meta sidecar confirms agent_task only for exec rollouts (#24)
```

Each commit must include its own tests. Verify:

```bash
git log --oneline HEAD~3..HEAD
# Expect exactly the three messages above.
git show --stat HEAD~2   # driver: tools.go, contract_tools.go, *_test.go, bind_thread*.go
git show --stat HEAD~1   # skills: discover-thread.sh, *_test.sh, SKILL.md
git show --stat HEAD     # codex: sessions.go, sessions_test.go
```

If a file lands in the wrong commit (e.g. a SKILL.md change ended up in the codex commit), redo the rebase with `--keep-base` and `edit`/`reword`/`fixup` operations until each commit is concern-pure.

- [ ] **Step 6: Final repo-wide test sweep**

```bash
cd multi-agent
go test ./...
cd ..
shellcheck skills/multiagent/scripts/discover-thread.sh
skills/multiagent/scripts/discover-thread_test.sh
skills/multiagent/scripts/skill_md_inline_in_sync_test.sh
```

Expected: every command exits 0.

- [ ] **Step 7: Push and open / update PR #31**

```bash
git push --force-with-lease origin commander-parent-link-p3
gh pr view 31 --json url | jq -r .url
```

If the PR already exists, update its description with:
- The three acceptance criteria (Step 3 results, screenshots).
- The Q2 classification matrix table from the spec.
- A note about the pre-P1 historical exec-rollout semantic shift (User instead of AgentTask without sidecar).

- [ ] **Step 8: Hand off for review**

Tag reviewers per the spec's open-questions list. Flag two items for explicit reviewer attention:

1. The pre-P1 semantic shift in row 6 of Q2's matrix.
2. The `loomOriginMarker` deletion makes `codex.ReadCurrentSession` driver-side dead — cleanup follow-up tracked separately, not in this PR.

---

## Self-Review

Skim the spec against this plan one more time. Coverage:

- Q1 — bind_thread tool, validator, idempotency, replace → Tasks 1, 2.
- Q1 — capture-and-pass invariant + delete loomOriginMarker → Task 3.
- Q1 — submit_task / resume_task / submit_contract_task guards (fail-fast, side-effect ordering, dual-path) → Tasks 4, 5, 6.
- Q1 — bulk test audit (narrowed guard, ~53 candidates) → Task 7.
- Q1 — env-gated MCP integration → Task 8.
- Q1 — discover-thread.sh (env + /proc, multi-candidate exit 2, canonicalized prefix, no cwd heuristic) → Task 9.
- Q1 — bash test suite (env isolation invariant, parent-holds-fd technique) → Task 10.
- Q1 — SKILL.md inline heredoc + native-Windows fallback + /resume re-bind → Task 11.
- Q1 — SKILL.md ⇄ script CI drift gate → Task 12.
- Q2 — applyCodexSessionMeta drops codex_exec branch → Task 13.
- Q2 — applyLoomMeta gates + rolloutIsExec OR-accumulator + sessions_test matrix → Task 14.
- E2E acceptance + commit shaping → Task 15.

No placeholders, no "TBD". Function signatures consistent across tasks:
- `BindThread(ctx, threadID) (BindResult, error)` — Tasks 1, 2, 8.
- `requireBoundThread() (string, error)` — Tasks 3, 4, 5, 6.
- `applyLoomMeta(base, sess, rolloutIsExec)` — Task 14; called from `scanCodexSession` in Task 14.
- `validThreadIDPattern` (exported const) — Task 1; consumed by `bindThreadTool.InputSchema()` in Task 2.

No spec requirement is unimplemented.
