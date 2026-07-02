# WT-2-driver-promotion-chain — Sub-B1: promote-candidate

**Chain position**: 4 of 4 (B6 done → B2 done → B4 done → **B1**).
Adds the promote-candidate surfacing logic that WT-2 B2's
promotion pipeline was designed to feed. Also registers the
`NoUserPromotionPath` ablation flag target that B2 hooked (via
`Deps.IsPromotionPathDisabled`); when this sub-task lands, that
predicate becomes live in production.

**Sources**:

- `/root/paper_writing/docs/final/todo_list.md` Phase 2 合流注意事项 §4.
- `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md` §B B1.
- `/root/paper_writing/docs/intermediate/11_loom_user_promoted_capability_lifecycle.md` §6 B1.
- `internal/ablation.NoUserPromotionPath` — declared in
  `internal/ablation/registry.go:22` and included in `KnownFlags()`,
  but no `*bool` target is registered. This sub-task registers it in
  a new `internal/driver/promote_candidate*.go` file.

---

## 1. Goal

Introduce the **promote-candidate signal**: when the driver observes
the SAME task-family being solved by ad-hoc script TWICE (or user
explicitly hints, or driver LLM infers), it should emit a
`promote-candidate` event AND write a row to a new
`promote_candidates` observer table. The row carries enough context
for a downstream metric (`PromotionCandidateSurfacingRate`) and for
a UI-side "want to fixate this into a proper MCP?" prompt to be
rendered.

`NoUserPromotionPath` ablation, THREE gates:
- when on, the driver DOES NOT emit candidate events (silent-open) AND
  emits ONE structured log line per suppressed emit;
- the B2 `promotion_pipeline` tool is refused at its boundary
  (predicate-based — driver-agent wires `driver.IsNoUserPromotionPath`
  into the pipeline's `Deps.IsPromotionPathDisabled`);
- driver-initiated `register_slave_mcp` calls are refused with
  `FailPolicyViolation` for every promotion_reason EXCEPT
  `explicit_user_request`. That is:
    - `driver_agent_inferred` → refused (driver decided)
    - `batch_import` → refused (driver-initiated bulk operation)
    - `ci_seed` → refused (CI-driven, not user-driven)
    - `explicit_user_request` → PASSES (represents user intent, and
      user-initiated `mcp-userspace install` also uses this reason)
  This preserves the paper's contract that
  `NoUserPromotionPath=on` measures "the world without
  driver-initiated promotion", while a real human clicking
  "register this" still works because their intent is explicit.
  Matches B6 §5 truth table row for `NoUserPromotionPath=ON` and
  §7 (c) of that spec ("USER-initiated calls STILL go through").

  This third gate is implemented in this sub-task by editing
  `internal/driver/register_mcp_tool.go`'s
  `registerSlaveMCPTool.Call` to reject early when
  `IsNoUserPromotionPath() && args.PromotionReason !=
  string(promotionaudit.ReasonExplicitUserRequest)`. The reason
  string is already regex-validated by the B6 InputSchema `enum`, so
  the guard is safe.

## 2. Data model

### 2.1 New observer table `promote_candidates`

DDL append to `internal/observerstore/schema.sql`:

```sql
-- WT-2-driver-promotion-chain B1: one row per surfaced
-- promote-candidate signal. Populated by
-- driver.SurfacePromoteCandidate; consumed by
-- PromotionCandidateSurfacingRate / PromotionAdoptionRate metrics.
-- See docs/specs/wt2-driver-promotion-chain-B1.spec.md §2.
CREATE TABLE IF NOT EXISTS promote_candidates (
    row_id            TEXT PRIMARY KEY,
    candidate_id      TEXT NOT NULL,
    family            TEXT NOT NULL,
    source_task_ids   TEXT NOT NULL DEFAULT '[]', -- JSON array of task ids
    surfaced_at       TEXT NOT NULL,
    decision          TEXT NOT NULL DEFAULT '' CHECK(decision IN ('','promoted','declined','expired')),
    decision_at       TEXT NOT NULL DEFAULT '',
    surfaced_by       TEXT NOT NULL DEFAULT '' CHECK(surfaced_by IN ('','user_hint','driver_inferred','similarity_signal')),
    workspace_id      TEXT NOT NULL DEFAULT '',
    run_id            TEXT NOT NULL DEFAULT '',
    UNIQUE(run_id, candidate_id)
);
CREATE INDEX IF NOT EXISTS idx_promote_candidates_family
    ON promote_candidates(family, surfaced_at);
CREATE INDEX IF NOT EXISTS idx_promote_candidates_candidate
    ON promote_candidates(candidate_id);
CREATE INDEX IF NOT EXISTS idx_promote_candidates_run
    ON promote_candidates(run_id, surfaced_at);
```

`UNIQUE(run_id, candidate_id)` — scoped-per-run dedup. Repeated
observations within the same run collapse via `INSERT OR IGNORE`,
BUT parallel runs (each with its own run_id) still get their own
row for the same conceptual candidate. This preserves the paper's
per-run metric attribution — a candidate that shows up in E4 Stage B
under Full Loom vs under `NoUserPromotionPath=on` needs to be
counted separately.

`run_id` is read from the same `driver.CurrentRunID()` accessor B4
introduced (populated from `LOOM_EVAL_RUN_ID` at driver-agent
startup). Rows carry run_id explicitly so
`PromotionCandidateSurfacingRate` (a Phase-3 metric that runs
alongside parallel workloads) can filter by `run_id = ?` — no
time-range ambiguity. Empty run_id is accepted for
interactive / ad-hoc sessions.

- `candidate_id` — a stable identifier derived from `run_id +
  family + sorted(source_task_ids)` (§4.1). Two observations
  WITHIN THE SAME RUN of the same family+task_ids collapse to the
  same candidate_id (dedup via `UNIQUE(run_id, candidate_id)` +
  `INSERT OR IGNORE`); two DIFFERENT runs observing the same
  family+task_ids produce DIFFERENT candidate_ids so per-run metric
  attribution stays clean.
- `source_task_ids` — JSON `["task_a","task_b",...]`; the writer
  validates each entry against the same `^[A-Za-z0-9_-]{8,128}$` regex
  used by B6 (§7 (a)).
- `surfaced_at` — RFC3339Nano UTC (monotonic per §7 (c)).
- `decision`, `decision_at` — empty on initial insert; updated later
  by a follow-up when the user decides.
- `surfaced_by` — enum recording WHY the signal fired.
- `workspace_id` — carried so D2 can filter per-workspace.

Two indexes cover the two hot access paths: by family (for
"how many candidates in this family?") and by candidate_id (for the
follow-up UPDATE that records the user's decision).

### 2.2 `CandidateSignal` struct

New file `internal/driver/promote_candidate.go`:

```go
package driver

// CandidateSignal is the input to SurfacePromoteCandidate. Callers
// construct it based on the observation that triggered the surfacing
// decision.
type CandidateSignal struct {
    Family        string      // e.g. "csv-profiler"; matches WT-1 task-family taxonomy
    SourceTaskIDs []string    // MUST have len >= 1; each matches ^[A-Za-z0-9_-]{8,128}$
    SurfacedBy    string      // enum: "user_hint" | "driver_inferred" | "similarity_signal"
    WorkspaceID   string      // may be "" for interactive / ad-hoc sessions
}
```

### 2.3 `Decision` enum

```go
type CandidateDecision string
const (
    DecisionPending  CandidateDecision = ""
    DecisionPromoted CandidateDecision = "promoted"
    DecisionDeclined CandidateDecision = "declined"
    DecisionExpired  CandidateDecision = "expired"
)
```

## 3. Public API

`internal/driver/promote_candidate.go`:

```go
// SurfacePromoteCandidate emits ONE promote-candidate event and
// writes ONE promote_candidates row (with decision='' pending). No
// return value beyond error — callers do not need to know the
// candidate_id up front; the follow-up call
// (RecordCandidateDecision) accepts either the candidate_id or the
// row_id.
//
// If NoUserPromotionPath is ON, the function short-circuits BEFORE
// any side effect (no event, no row), emits ONE log line
// `[ablation] NoUserPromotionPath: candidate suppressed
// family=<...>`, and returns nil. This is the invariant that keeps
// ablated runs measurable.
func SurfacePromoteCandidate(ctx context.Context, sig CandidateSignal) (candidateID string, err error)

// RecordCandidateDecision updates the decision + decision_at for a
// previously-surfaced candidate. decisionAt is caller-supplied for
// testability; production callers pass time.Now().UTC().
func RecordCandidateDecision(ctx context.Context, candidateID string, dec CandidateDecision, decisionAt time.Time) error

// ExpireCandidatesOlderThan sets decision='expired' for every
// pending candidate with surfaced_at older than cutoff. Callers
// invoke this from a periodic sweep; the default TTL is 24 hours
// (spec §4 (d)).
func ExpireCandidatesOlderThan(ctx context.Context, cutoff time.Time) (expired int, err error)

// IsNoUserPromotionPath reports the flag state — this is the
// production accessor B2's promotion_pipeline_tool reads.
func IsNoUserPromotionPath() bool
```

Dependencies (injected via `SetPromoteCandidateDeps`):

```go
type PromoteCandidateDeps struct {
    Writer PromoteCandidatesWriter // in observerstore
    Events observer.Sink
    Now    func() time.Time
}

type PromoteCandidatesWriter interface {
    InsertPromoteCandidate(ctx context.Context, row PromoteCandidateRow) error
    UpdatePromoteCandidateDecision(ctx context.Context, candidateID string, dec string, decisionAt string) error
    ExpirePromoteCandidates(ctx context.Context, cutoffRFC3339 string) (int, error)
}
```

## 4. Semantics

### 4.1 candidate_id derivation

`candidate_id = "cand_" + hex(sha256(run_id || '\x1F' || family || '\x1F' || sorted(source_task_ids joined by '\x1F'))))[:24]`

The run_id prefix makes candidate_id globally unique across parallel
runs even when they see the same (family, source_task_ids) —
essential because candidate_id is the join key downstream into B6's
`promotion_audit.candidate_source_task_id` (which has no run_id of
its own). Without this scoping, two parallel runs that both observe
the same family+tasks would produce the same candidate_id, and
B6-side JOIN would blend their `promotion_audit` rows across runs,
breaking `PromotionAdoptionRate` and
`TimeFromUserDecisionToRegisteredMCP` per-run attribution.

Sorting source_task_ids makes the derivation insensitive to
observation order; the fixed prefix + 24-hex suffix stays well
under the promotion_audit `candidate_source_task_id` regex bound
`^[A-Za-z0-9_-]{8,128}$`.

When run_id is empty (ad-hoc / interactive session), the hash still
succeeds — the leading empty string just becomes part of the hash
input. Multiple ad-hoc sessions with the same family + task_ids
DO collapse to the same candidate_id, which matches operator
intent for interactive replay.

`RecordCandidateDecision(candidateID, ...)` accepts candidate_id as
its key — and because candidate_id now embeds run_id, decisions are
also unambiguous across runs.

### 4.2 same-family two-ad-hoc-scripts detection

Implemented in-scope: a per-process
`familyObservationCounter` (map `family → []taskID` with mutex)
records each ad-hoc bash / powershell task the driver dispatched.
The counter is populated by a small hook in
`internal/driver/promote_candidate_detector.go`:

```go
// RecordAdHocScriptTask is called by the driver's task-completion
// path whenever a slave task with skill=bash|powershell completes.
// When a family has 2+ ad-hoc completions within 24h,
// this call automatically fires SurfacePromoteCandidate with
// SurfacedBy="similarity_signal". Second and subsequent calls
// with the same (family, source_task_ids-set) are deduped by
// candidate_id — the surfacer's insert uses INSERT OR IGNORE.
func RecordAdHocScriptTask(ctx context.Context, family, taskID, workspaceID string)
```

Callers: `internal/driver/tools.go` (submitTaskTool + related)
appends `family` to the completed task's metadata BEFORE calling
`RecordAdHocScriptTask`; a helper `familyOfTask(taskInfo)` reads
the family from the task journal / task metadata (empty family ==
skip). For B1's minimum viable observation, `family` is derived
from the first token of the task's `summary` field (e.g. "csv
processing" → family "csv"). A future WT refines this heuristic;
the paper's E4 harness sets family explicitly via
`LOOM_EVAL_TASK_FAMILY` env var.

`INSERT OR IGNORE` on the `candidate_id` primary key makes
double-firing idempotent. The `RecordAdHocScriptTask` path is
also SILENCED by `NoUserPromotionPath` per §5.

### 4.3 24-hour expiry (spec §4 (d))

`ExpireCandidatesOlderThan` is invoked by a background goroutine
started by the driver-agent main.go at process start with a 5-minute
tick. Cutoff = `time.Now().Add(-24 * time.Hour)`. Rows that were
already `promoted` / `declined` are NOT touched (only
`decision=''`).

### 4.4 event shape

Observer.Event fields:

| Field         | Value                                            |
|---------------|--------------------------------------------------|
| `Type`        | `promote_candidate`                              |
| `WorkspaceID` | sig.WorkspaceID                                  |
| `AgentRole`   | `driver`                                         |
| `Payload`     | JSON `{"family":"...", "candidate_id":"cand_...", "surfaced_by":"..."}` |

Payload deliberately EXCLUDES `source_task_ids` (which live in the
DB row) so the observer event stream stays compact.

## 5. `NoUserPromotionPath` ablation

New file `internal/driver/promote_candidate_ablation.go`:

```go
var (
    noUserPromotionPath         bool
    noUserPromotionPathInitErr  error
    noUserPromotionPathWarnOnce sync.Once
)

func IsNoUserPromotionPath() bool { return noUserPromotionPath }

func init() {
    if err := ablation.Default.Register(ablation.NoUserPromotionPath, &noUserPromotionPath); err != nil {
        noUserPromotionPathInitErr = err
        log.Printf("driver: ablation.Default.Register(NoUserPromotionPath) failed: %v — --ablation NoUserPromotionPath will not gate SurfacePromoteCandidate, promotion_pipeline, or register_slave_mcp", err)
    }
}

// surfaceInitErrorOnce is called by SurfacePromoteCandidate,
// promotion_pipeline_tool, and registerSlaveMCPTool on first entry.
// If registration failed at init, emit ONE ERROR log line so it's
// visible even when stderr was suppressed at startup — matches the
// B4 NoRegistryLookup pattern.
func surfacePromotionInitErrorOnce() {
    if noUserPromotionPathInitErr == nil {
        return
    }
    noUserPromotionPathWarnOnce.Do(func() {
        log.Printf("[error] NoUserPromotionPath ablation wiring inert (init err: %v) — driver-initiated promotion paths (SurfacePromoteCandidate, promotion_pipeline, register_slave_mcp) will run unguarded", noUserPromotionPathInitErr)
    })
}
```

Same `mustRegister`-in-spirit pattern as B4's `NoRegistryLookup`, and
the same first-use surfacing so an inert flag is loudly announced
rather than silently letting the driver run unguarded.

## 6. Wiring changes

- `cmd/driver-agent/main.go` — after `SetLookupDeps`, also call
  `driver.SetPromoteCandidateDeps` (uses the same observer-DB handle),
  and start the expiry goroutine. Also thread
  `IsPromotionPathDisabled: driver.IsNoUserPromotionPath` into the
  `promotion_pipeline_tool`'s `buildProdPipeline` (patch that method
  to accept the predicate from a package-level var
  `driver.promotionPathPredicate` that this sub-task sets to
  `IsNoUserPromotionPath`).

## 7. Security

### (a) `source_task_ids` regex + non-empty

Each entry MUST match `^[A-Za-z0-9_-]{8,128}$` (same regex B6 uses).
Empty slice → reject with `ErrEmptySourceTaskIDs`. Enforced BEFORE
the DB insert; the observer event is not emitted on rejection either
(so an invalid caller cannot pollute the event stream with a partial
signal).

### (b) `family` regex + length

Must match `^[a-z][a-z0-9_-]{0,63}$` (lowercase kebab-or-snake, up
to 64 chars). Matches the taxonomy used by WT-1's golden families.

### (c) monotonic `surfaced_at`

Callers pass `Now func() time.Time` via Deps; the pipeline writes
`Now().UTC()`. When Now uses the wall clock, DST/NTP adjustments
could cause `surfaced_at < previous surfaced_at`. Mitigated by
`ExpireCandidatesOlderThan` using cutoff-vs-row comparison (both are
wall-clock RFC3339 strings that sort lexicographically); the metric
scorer uses `MIN(surfaced_at)` per candidate_id rather than assuming
strict monotonicity. Documented so a future WT can plug a monotonic
clock without breaking downstream.

### (d) 24-hour expiry with audit

`ExpireCandidatesOlderThan` MUST log each expiry batch's row count
`[expiry] promote_candidates expired=<N> cutoff=<...>`. Silent
expiry would break the paper's `PromotionCandidateSurfacingRate`
denominator (which counts surfacings, not un-expired candidates).

### (e) `NoUserPromotionPath` silent-open logs

Ablation ON: `[ablation] NoUserPromotionPath: candidate suppressed
family=<...>` per suppressed call. Matches B4's
NoRegistryLookup silent-open-with-log pattern.

### (f) `RecordCandidateDecision` idempotency

Two updates with the same (candidate_id, decision) → second is a no-op
(second `UPDATE ... WHERE decision=''` affects 0 rows). The writer
returns nil in that case; caller learns idempotency by observing 0
rows-affected.

### (g) DB CHECK constraints on decision + surfaced_by

Belt-and-suspenders backstop for the Go-side enums.

### (h) Perf assertion conditional on -short

`TestSurfacePromoteCandidate_PerfBench_ConditionalOnShort` — 100
back-to-back surfacings complete in < 20 ms with -short skip.

### (i) No secret material in candidate_id, and run-scoped join key

`candidate_id = "cand_" + hex(sha256(run_id || family || sorted
task_ids))[:24]` per §4.1 — inputs are regex-bounded slugs, no free
text. Two properties matter:

1. **No secrets in the hash inputs.** run_id, family, task_ids are
   all slugs.
2. **run_id scoping.** Prefixing with the run_id makes candidate_id
   globally unique across parallel runs, so the downstream
   B6-join (`promotion_audit.candidate_source_task_id =
   promote_candidates.candidate_id`) cannot blend rows across
   runs — even though promotion_audit itself has no run_id column.
   The hex-hashed form is safe to log and safe to include in
   promotion_audit's `candidate_source_task_id` slot.

## 8. Test plan

| Security | Test                                                                   |
|----------|------------------------------------------------------------------------|
| §7 (a)   | `TestSurfacePromoteCandidate_RejectsEmptyTaskIDs`                      |
| §7 (a)   | `TestSurfacePromoteCandidate_RejectsMalformedTaskID`                   |
| §7 (b)   | `TestSurfacePromoteCandidate_RejectsBadFamily` (uppercase, space)      |
| §7 (c)   | `TestSurfacePromoteCandidate_SurfacedAtIsUTC`                          |
| §7 (d)   | `TestExpireCandidates_LogsRowCount`                                    |
| §7 (d)   | `TestExpireCandidates_LeavesPromotedAndDeclinedAlone`                  |
| §7 (e)   | `TestSurfacePromoteCandidate_NoUserPromotionPathAblationSuppressesAndLogs` |
| §7 (e)   | `TestSurfacePromoteCandidate_NoUserPromotionPathAblationSideEffectFree` — assert no DB insert, no event emitted |
| §7 (f)   | `TestRecordCandidateDecision_IdempotentSecondUpdate` |
| §7 (g)   | `TestSchema_PromoteCandidatesRejectsBadEnumViaCheck` |
| §7 (h)   | `TestSurfacePromoteCandidate_PerfBench_ConditionalOnShort` |
| §7 (i)   | `TestCandidateID_DerivedFromFamilyAndSortedTaskIDs` — same family + same tasks in different order → same id (within one run); different family → different id |
| §7 (i)   | `TestCandidateID_DifferentRunsProduceDifferentIDs` — SetCurrentRunID("run-a") then compute candidate_id; SetCurrentRunID("run-b") then compute candidate_id for the SAME family + task_ids; assert the two candidate_ids differ. Closes the parallel-runs join ambiguity. |
| Metric   | `TestSurfacePromoteCandidate_WritesRowAndEvent` — happy path emits both |
| B2 link  | `TestPromotionPipeline_UsesLiveNoUserPromotionPathPredicate` — with the flag on, promotion_pipeline_tool refuses (integration test) |
| Detector | `TestRecordAdHocScriptTask_FiresCandidateAfterSecondFamily` — call twice with same family + different task_ids → one candidate row appears (INSERT OR IGNORE handles the second attempt at same candidate_id). |
| Detector | `TestRecordAdHocScriptTask_UnderNoUserPromotionPath_NoCandidate` — ablation on: 5 calls → 0 candidate rows AND 5 suppression log lines. |
| B6 link  | `TestRegisterSlaveMCP_UnderNoUserPromotionPath_RefusesAllExceptExplicitUser` — with the flag on, register_slave_mcp with promotion_reason ∈ {driver_agent_inferred, batch_import, ci_seed} each returns FailPolicyViolation; same call with promotion_reason=explicit_user_request succeeds. |
| Init     | `TestSurfacePromoteCandidate_InitErrorSurfacedOnFirstCall` — inject noUserPromotionPathInitErr; call surfacePromotionInitErrorOnce (or a Lookup call route) twice; assert exactly one `[error] NoUserPromotionPath ablation wiring inert` log line. |
| Run     | `TestSurfacePromoteCandidate_WritesRunIDFromCurrentRunID` — SetCurrentRunID("run-xyz01234"), fire candidate, assert row.run_id="run-xyz01234". |

## 9. Files touched

Created:

- `multi-agent/internal/driver/promote_candidate.go`
- `multi-agent/internal/driver/promote_candidate_test.go`
- `multi-agent/internal/driver/promote_candidate_ablation.go`
- `multi-agent/internal/observerstore/promote_candidates_writer.go`
- `multi-agent/internal/observerstore/promote_candidates_writer_test.go`
- `docs/specs/wt2-driver-promotion-chain-B1.{spec,plan}.md`

Modified:

- `multi-agent/internal/observerstore/schema.sql` — append
  `promote_candidates` DDL.
- `multi-agent/cmd/driver-agent/main.go` — wire
  `SetPromoteCandidateDeps` + expiry goroutine + wire
  `IsNoUserPromotionPath` into the pipeline predicate.
- `multi-agent/internal/driver/promotion_pipeline_tool.go` —
  read predicate from a package-level var
  `promotionPathPredicate func() bool` (set by main.go); default nil
  means "unwired" (matches B2 §5).

## 10. What this spec explicitly does NOT do

- Does NOT emit the UI-side prompt (e.g. "you may want to fixate
  this?"). That's a claude-code / driver-orchestrator concern; B1
  produces the SIGNAL, the surface renders it.
- Does NOT wire `PromotionCandidateSurfacingRate` into the D1 `runs`
  writer. That's a follow-up (WT-2-metric-extract) — D2 reads
  `promote_candidates` directly by run_id per §2.
- Does NOT modify B4 code paths. Modifies B6's
  register_slave_mcp for the third `NoUserPromotionPath` gate (§1)
  and B2's promotion_pipeline_tool for the predicate wire-up (§6).

(The "same family × 2 ad-hoc scripts" heuristic IS in-scope — see
§4.2. The family-of-task derivation is a minimum-viable
first-token-of-summary rule; a smarter classifier is deferred.)
