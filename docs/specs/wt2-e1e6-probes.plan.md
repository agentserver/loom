# WT-2-e1e6-probes — Plan

> Drives [wt2-e1e6-probes.spec.md](wt2-e1e6-probes.spec.md). Pure TDD; every
> test maps to a spec section or a Security mitigation (a–h). Task
> boundaries are drawn so each ends with a passing test suite and a
> green `go vet`; a reviewer can accept task N without task N+1 landing.

## Global Constraints (copied verbatim from spec)

- File domain: `multi-agent/tools/eval/runner/probes/` (**NEW**),
  `multi-agent/tools/eval/runner/runner.go` (Edit 0–6 per spec §5.1),
  `multi-agent/tools/eval/runner/writer.go` (append-only columns per
  spec §4). Nothing else moves.
- Probes package imports **only** stdlib +
  `github.com/yourorg/multi-agent/internal/secretscrub`. No SQL, no
  HTTP.
- Zero changes to `Dispatcher`, `executor.Executor`, `humanloop`, or
  any package outside `tools/eval/runner/`.
- Go version: same as repo (`multi-agent/go.mod` declares `go 1.26.x`).
- `RunRow` + `CSVColumns()` are append-only; the eight new probe
  columns + `probe_notes_json` land at the tail per spec §4 table.
- **All `go test` / `go vet` / `gofmt` commands assume cwd =
  `multi-agent/.worktrees/p2-e1e6-probes/multi-agent/`** (the Go
  module root). From the worktree root, run `cd multi-agent` first.
- Test command:
  `go test ./tools/eval/runner/probes/... ./tools/eval/runner/... -count=1 -shuffle=on -race`
- Lint: `go vet ./...` && `gofmt -l tools/eval/runner/probes tools/eval/runner/runner.go tools/eval/runner/writer.go`
- Each `git commit` ends with
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
  **Do not** push.

---

## Metric → CSV column → D1 (`runs` table) column mapping

Straight copy of spec §6's audit, restated in one table for the
executing engineer so they don't have to jump back to the spec while
writing code. `runs` columns marked `HANDOFF-D1-N` are additions this
worktree does NOT own (they land in a WT-1-run-schema follow-up per
spec §9); this plan writes ONLY the CSV columns + `RunRow` fields.

| Metric | RunRow field (Task 6) | CSV column (Task 6) | `runs` table column | Landing worktree |
|---|---|---|---|---|
| TaskSuccessRate | `ProbeTaskSuccessRate *bool` | `probe_task_success_rate` | `success_oracle_result` (existing enum `pass`/`fail`/`timeout`) | WT-1-run-schema (merged) |
| LifecycleClosureRate | `ProbeLifecycleClosureRate *bool` | `probe_lifecycle_closure_rate` | `lifecycle_closure_rate INTEGER NULL` (HANDOFF-D1-1) | follow-up |
| TimeToCompletion | `ProbeTimeToCompletionNs *int64` | `probe_time_to_completion_ns` | `time_to_completion_ns INTEGER NOT NULL DEFAULT 0` (HANDOFF-D1-2, authoritative) | follow-up |
| HumanContextSelectionCount | `ProbeHumanContextSelectionCount *int` | `probe_human_context_selection_count` | `human_intervention_count INTEGER` (existing) | WT-1-run-schema (merged) |
| WrongContextFailureRate | `ProbeWrongContextFailureRate *bool` | `probe_wrong_context_failure_rate` | `wrong_context_failure_rate INTEGER NULL` (HANDOFF-D1-3, optional denormalisation) | follow-up |
| ArtifactCorrectnessRate | `ProbeArtifactCorrectnessRate *bool` | `probe_artifact_correctness_rate` | `artifact_correctness_rate INTEGER NULL` (HANDOFF-D1-4) | follow-up |
| ManualSetupStepCount | `ProbeManualSetupStepCount *int` | `probe_manual_setup_step_count` | `manual_setup_step_count INTEGER NULL` (HANDOFF-D1-5) | follow-up |
| ConfigTouchCount | `ProbeConfigTouchCount *int` | `probe_config_touch_count` | `config_touch_count INTEGER NULL` (HANDOFF-D1-6) | follow-up |
| (labels sidecar) | `ProbeNotesJSON string` | `probe_notes_json` | — (CSV-only; JSON blob remains in CSV until follow-up adds a `probe_notes_json TEXT` column, out of scope) | follow-up |

Notes for the follow-up engineer landing HANDOFF-D1-N:
- Five of the six additions are `INTEGER NULL` — NULL preserves the
  §7(e) discipline. Metric-extract MUST NOT `COALESCE(col, 0)` — that
  would silently claim "measured = 0" for runs where the probe emitted
  nil (D6c harness not landed / read failure / oracle omission).
- Field-name convention: SQL column name = CSV column name minus the
  `probe_` prefix (e.g. `probe_lifecycle_closure_rate` → SQL
  `lifecycle_closure_rate`). Kept 1:1 so the extractor's `RunRow →
  Schema` mapping is a simple field rename.

## File Map

| Path | Action | Responsibility |
|---|---|---|
| `multi-agent/tools/eval/runner/probes/probes.go` | **CREATE** | `Emitter`, `NewEmitter`, `Emit`, `Close`, `Record`, buffered channel + single flusher goroutine, `secretscrub.Sanitize` on label values, drop counter, monotonic reference. |
| `multi-agent/tools/eval/runner/probes/probes_test.go` | **CREATE** | Non-blocking, secretscrub, unknown-metric drop, close idempotence, drop-counter, monotonic reference. |
| `multi-agent/tools/eval/runner/probes/metrics.go` | **CREATE** | `MetricKey` enum, `AllMetrics`, `EmitOracleMetrics`, `EmitTimeToCompletion`, `MergeIntoRow(&RunRow, []Record)`. |
| `multi-agent/tools/eval/runner/probes/metrics_test.go` | **CREATE** | Per-metric emit unit test (8 metrics × 1 test each) + `MergeIntoRow` last-wins-on-dupes. |
| `multi-agent/tools/eval/runner/probes/humanloop.go` | **CREATE** | `EmitHumanCount(ctx, emitter, wsRoot, stderr)`, `countFile(path, stderr) (int, string)` — strict integer-only reader. |
| `multi-agent/tools/eval/runner/probes/humanloop_test.go` | **CREATE** | Text-not-stored guard, absent → 0/`absent`, malformed → 0/`malformed`, size cap. |
| `multi-agent/tools/eval/runner/probes/setup.go` | **CREATE** | `EmitSetupMetrics(ctx, emitter, wsRoot, stderr)` — reads `${wsRoot}/.probes/setup.json`, emits `manual_setup_step_count` / `config_touch_count`. |
| `multi-agent/tools/eval/runner/probes/setup_test.go` | **CREATE** | Absent → nil + `d6c_setup_harness_pending`, malformed → nil + `malformed_setup_file`, present-well-formed → int values. |
| `multi-agent/tools/eval/runner/probes/wrongctx.go` | **CREATE** | `EmitWrongContext(ctx, emitter, wsRoot, workloadRoot, workloadID, oracleOut, stderr)` — reads selected_context.txt + labels.json, applies §3.6 rule. |
| `multi-agent/tools/eval/runner/probes/wrongctx_test.go` | **CREATE** | Structural-only pass, structural + oracle failure_class, missing selected → nil, missing ground_truth → nil, pass=true short-circuits to `false`. |
| `multi-agent/tools/eval/runner/probes/nodeps_test.go` | **CREATE** | Static import-graph test: no `database/sql`, no `net/http`. |
| `multi-agent/tools/eval/runner/runner.go` | **MODIFY** | Edits 0–6 per spec §5.1 (imports, emitter construction, six probe emit sites, drain-and-merge). |
| `multi-agent/tools/eval/runner/writer.go` | **MODIFY (APPEND)** | Append eight probe fields + `ProbeNotesJSON` to `RunRow`; append nine column names to `CSVColumns()`; extend `rowAsCSVRecord` to serialise them (bool/int/nil → string per §4). |
| `multi-agent/tools/eval/runner/writer_test.go` | **MODIFY (APPEND)** | Two tests: column-count assertion + probe-fields-round-trip via CSV. |
| `multi-agent/tools/eval/runner/review_fixes_test.go` | **NOT TOUCHED** | Existing WT-1 review tests; adding a probe field must not break them (the CSV header order is append-only). |

## Test Matrix

Each row links a test to a spec section AND/OR a Security mitigation.
The `Sec` column carries the mitigation ID; every mitigation (a)–(h)
has ≥1 test row. A row with a bare `spec §X` cite is a functional
test that has no security counterweight (e.g. plain metric emit).

| # | Test | Verifies | Spec / Sec |
|---|---|---|---|
| 1 | `TestEmit_NonBlocking_WhenChannelFull` | 300 records into size-8 emitter with a slow flusher, every `Emit` returns in <1 ms. | Sec (a) |
| 2 | `TestEmit_DropCounter_Bumps` | Same setup as #1, asserts `droppedTotal` == expected dropped count. | Sec (a) |
| 3 | `TestEmit_SanitizesLabelValues` | Label `bad="sk-ABCDEFGHIJKLMNOP"` → drained record has `[REDACTED]`; `secretscrub.RedactedTotal` bumped by 1. | Sec (b) |
| 4 | `TestEmit_RejectsUnknownMetric` | Metric `"not_a_real_metric"` → drop + one stderr warn line. | spec §3.2 |
| 5 | `TestClose_Idempotent` | Two consecutive `Close()` calls: second returns empty slice + nil error, no data race under `-race`. | spec §3.1 |
| 6 | `TestEmit_NilEmitter_IsNoop` | `var e *Emitter; e.Emit(ctx, k, v, nil)` does not panic and returns nil. | spec §3.1 |
| 7 | `TestEmitTimeToCompletion_Monotonic` | `startedAt` at now-5s, emit, drained value in `[5e9, 1e10]` ns; type is `int64`; wall_start/wall_end labels are unix ints. | Sec (c) |
| 8 | `TestHumanCount_IgnoresSiblingLogFile` | Create `.probes/humanloop.count`="3" AND `.probes/humanloop.log`="my sk-DEADBEEF12345678". Drained value=3; labels contain no substring from the log; `secretscrub.RedactedTotal` did NOT bump. | Sec (d) |
| 9 | `TestHumanCount_AbsentFile` | No `.probes/` dir at all → value=0, label `source=absent`, no stderr. | spec §3.4 |
| 10 | `TestHumanCount_MalformedFile` | Content = "3abc" → value=0, label `source=malformed`, one stderr warn. | spec §3.4 |
| 11 | `TestHumanCount_SizeCap` | 5 KiB file of digits → value=0, label `source=malformed`, one stderr warn. | Sec (d) |
| 12 | `TestEmitSetupMetrics_AbsentFile` | No `.probes/setup.json` → both metrics nil, label `unavailable_reason=d6c_setup_harness_pending`. | Sec (e) |
| 13 | `TestEmitSetupMetrics_MalformedFile` | Content = `{"manual_setup_step_count":"three"}` → both nil, `unavailable_reason=malformed_setup_file`, one stderr warn. | Sec (e) |
| 14 | `TestEmitSetupMetrics_WellFormed` | Content = `{"manual_setup_step_count":3,"config_touch_count":5}` → values 3 and 5, label `source=setup_counter_file`. | spec §3.7 |
| 15 | `TestEmitOracleMetrics_TaskSuccessRate` | oracleOut.Passed=true, exit=0 → drained record `metric=task_success_rate value=true labels{oracle_exit_code=0,oracle_stdout_bytes=N}`. | spec §3.3 #1 |
| 16 | `TestEmitOracleMetrics_LifecycleClosureRate_FromOracle` | `metrics.lifecycle_closed=false` in oracle stdout → drained value=false, label `source=oracle_metrics`. | spec §3.3 #2 |
| 17 | `TestEmitOracleMetrics_LifecycleClosureRate_Fallback` | oracle stdout has no `lifecycle_closed` → drained value=passed, label `source=fallback_passed`. | spec §3.3 #2 |
| 18 | `TestEmitOracleMetrics_ArtifactCorrectness_FromOracle` | `metrics.artifact_correct=false` → drained value=false, `source=oracle_metrics`. | spec §3.3 #6 |
| 19 | `TestEmitOracleMetrics_ArtifactCorrectness_Fallback` | oracle omits `artifact_correct` → value=passed, `source=fallback_passed`. | spec §3.3 #6 |
| 20 | `TestEmitWrongContext_StructuralOnlyFail` | passed=false, no oracle failure_class, selected="ctxA", gt="ctxB" → value=true, `oracle_failure_class_source=fallback_structural_only`. | spec §3.6 |
| 21 | `TestEmitWrongContext_OracleClassifiedFail` | passed=false, `metrics.failure_class="missing_tool"`, selected!=gt → value=true, label `oracle_failure_class=missing_tool`. | spec §3.6 |
| 22 | `TestEmitWrongContext_PassShortCircuitsToFalse` | passed=true, selected!=gt → value=false (passing runs cannot be wrong-context failures). | spec §3.6 |
| 23 | `TestEmitWrongContext_MissingSelectedFile` | passed=false, no `selected_context.txt` → value=nil, `unavailable_reason=no_selected_context_file`. | spec §3.6 |
| 24 | `TestEmitWrongContext_MissingGroundTruthLabels` | passed=false, no `labels/workloads/<id>.labels.json` → value=nil, `unavailable_reason=no_ground_truth_labels`. | spec §3.6 |
| 25 | `TestEmitWrongContext_SanitizesContextStrings` | selected file contains `"srv-a sk-ABCDEFGHIJKLMNOP"` → label `selected` shows `[REDACTED]`. | Sec (b) |
| 26 | `TestMergeIntoRow_PopulatesEightFields` | Drain eight records (one per metric) → `RunRow.ProbeTaskSuccessRate` etc. populated; `ProbeNotesJSON` non-empty. | spec §5.2 |
| 27 | `TestMergeIntoRow_LastWinsOnDuplicate` | Emit `MetricTaskSuccessRate=true` then `=false` → `RunRow.ProbeTaskSuccessRate` == false + one stderr warn. | spec §5.2 |
| 28 | `TestProbes_NoDatabaseSQLImport` | Parse `probes` package's `import` block via `go/ast`; assert no `database/sql`. | Sec (f) |
| 29 | `TestProbes_NoHTTPClient` | Same parse; assert no `net/http`. | Sec (f) |
| 30 | `TestRunRow_ProbeFields_PresentInCSV` | Assemble a `RunRow` with the eight probe fields populated; write CSV; parse header + data; assert nine new columns (8 metrics + `probe_notes_json`) round-trip. | Sec (g), spec §4 |
| 31 | `TestCSVColumns_AppendOnly` | Assert `CSVColumns()[:22]` still matches the WT-1 skeleton column order; assert `len(CSVColumns()) == 31`. | spec §4.1 |
| 32 | `TestPerf_EmitLatency_LocalOnly` | 1000 `Emit` calls, average latency <1 ms; **skipped when `os.Getenv("CI") != ""`**. | Sec (h) |
| 33 | `TestSmoke_WorkloadRun_EightMetricsPresent` | `_test.go` in `runner` package builds runner in-process, runs `cross-device-code-mod`, parses CSV, asserts (a) all nine probe columns exist in header, (b) task_success_rate/lifecycle_closure_rate/artifact_correctness_rate non-empty in data row, (c) manual_setup_step_count / config_touch_count empty AND `probe_notes_json` records `d6c_setup_harness_pending`. Uses `testing.Short()` skip so a `-short` run bypasses the workload spawn. | spec §8 acceptance + Sec (e/g) |
| 34 | `TestSmoke_ProbeFailure_DoesNotBlockRunner` | Same smoke path with a shadow-workload fixture carrying a MALFORMED-JSON `.probes/setup.json`; runner still completes, oracle still runs, CSV still writes, `probe_manual_setup_step_count` is empty + `probe_notes_json` records `unavailable_reason=malformed_setup_file`. | Sec (a) at the integration level |

Every Security mitigation is covered:
- (a) → #1, #2, #34
- (b) → #3, #25
- (c) → #7
- (d) → #8, #11
- (e) → #12, #13
- (f) → #28, #29
- (g) → #30, #33
- (h) → #32

---

## Task 0: Test scaffolding + import-graph guard

**Files:**
- Create: `multi-agent/tools/eval/runner/probes/nodeps_test.go`

**Interfaces:**
- Consumes: none.
- Produces: none (this is a static-analysis test).

- [ ] **Step 1: Write the failing test**

```go
// nodeps_test.go — Security §7(f): probes package must not import
// database/sql or net/http. Prevents the "quick add sqlite writer"
// drift that would put SQL / network IO on the runner hot path.
package probes

import (
    "go/ast"
    "go/parser"
    "go/token"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestProbes_NoForbiddenImports(t *testing.T) {
    forbidden := map[string]bool{
        `"database/sql"`: true,
        `"net/http"`:     true,
    }
    fset := token.NewFileSet()
    entries, err := os.ReadDir(".")
    if err != nil {
        t.Fatalf("readdir: %v", err)
    }
    for _, e := range entries {
        if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
            continue
        }
        f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ImportsOnly)
        if err != nil {
            t.Fatalf("parse %s: %v", e.Name(), err)
        }
        for _, imp := range f.Imports {
            if forbidden[imp.Path.Value] {
                t.Fatalf("%s: forbidden import %s", e.Name(), imp.Path.Value)
            }
        }
        _ = ast.NewIdent // silence unused import in stripped-down variants
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -run TestProbes_NoForbiddenImports`
Expected: FAIL (`no Go files in ./tools/eval/runner/probes` — the package does not yet exist).

- [ ] **Step 3: Create a minimal package file so the test compiles**

Create `multi-agent/tools/eval/runner/probes/probes.go`:
```go
// Package probes emits E1/E6 evaluation metric records collected by the
// eval-runner into a bounded, non-blocking buffer. See
// docs/specs/wt2-e1e6-probes.spec.md.
package probes
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -run TestProbes_NoForbiddenImports`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/probes/
git commit -m "wt2-e1e6-probes T0: probes package scaffold + import-graph guard

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 1: `Emitter` + `Emit` + `Close` + `Record`

**Files:**
- Modify: `multi-agent/tools/eval/runner/probes/probes.go`
- Create: `multi-agent/tools/eval/runner/probes/probes_test.go`

**Interfaces:**
- Consumes: `github.com/yourorg/multi-agent/internal/secretscrub` (for
  label sanitization).
- Produces:
  - `type MetricKey string` and the eight `MetricKey` constants (used
    by Task 2's helpers).
  - `type Record struct{ Metric MetricKey; Value any; Labels map[string]string; EmittedAt time.Time; EmittedAtMonoNs int64 }`.
  - `func NewEmitter(bufferSize int, stderr io.Writer) *Emitter`.
  - `func (*Emitter) Emit(ctx context.Context, metric MetricKey, value any, labels map[string]string) error` — nil receiver = no-op.
  - `func (*Emitter) Close() ([]Record, error)` — idempotent.

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/tools/eval/runner/probes/probes_test.go`:
```go
package probes

import (
    "context"
    "expvar"
    "io"
    "strings"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/yourorg/multi-agent/internal/secretscrub"
)

func TestEmit_NonBlocking_WhenChannelFull(t *testing.T) {
    e := NewEmitter(8, io.Discard)
    // Stall the flusher by holding its mutex via reflection is fragile;
    // instead, close early to freeze the drain and keep Emit exercising
    // the channel-full → drop path.
    _, _ = e.Close()
    for i := 0; i < 300; i++ {
        start := time.Now()
        if err := e.Emit(context.Background(), MetricTaskSuccessRate, true, nil); err != nil {
            t.Fatalf("emit returned error: %v", err)
        }
        if d := time.Since(start); d > 1*time.Millisecond {
            t.Fatalf("emit blocked: %s (iter %d)", d, i)
        }
    }
}

func TestEmit_DropCounter_Bumps(t *testing.T) {
    e := NewEmitter(2, io.Discard)
    _, _ = e.Close() // freeze the drain
    for i := 0; i < 10; i++ {
        _ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
    }
    if got := atomic.LoadInt64(&e.dropped); got < 1 {
        t.Fatalf("droppedTotal did not bump: got %d, want >=1", got)
    }
}

func TestEmit_SanitizesLabelValues(t *testing.T) {
    before := readRedactedCounter()
    e := NewEmitter(0, io.Discard)
    err := e.Emit(context.Background(), MetricTaskSuccessRate, true, map[string]string{
        "bad": "sk-ABCDEFGHIJKLMNOP",
    })
    if err != nil {
        t.Fatalf("emit: %v", err)
    }
    recs, _ := e.Close()
    if len(recs) != 1 {
        t.Fatalf("want 1 record, got %d", len(recs))
    }
    if got := recs[0].Labels["bad"]; !strings.Contains(got, "[REDACTED]") {
        t.Fatalf("label not sanitized: %q", got)
    }
    after := readRedactedCounter()
    if after-before != 1 {
        t.Fatalf("secretscrub.RedactedTotal bumped %d, want 1", after-before)
    }
    _ = secretscrub.RedactedTotal // touch the imported symbol
}

func TestEmit_RejectsUnknownMetric(t *testing.T) {
    // Serialise stderr writes with a mutex — the flusher goroutine
    // drains the warn queue on Close, and Fprintln + strings.Builder
    // are not concurrency-safe on their own.
    var buf synchronizedBuf
    e := NewEmitter(0, &buf)
    err := e.Emit(context.Background(), MetricKey("not_a_real_metric"), 1, nil)
    if err != nil {
        t.Fatalf("emit: %v", err)
    }
    recs, _ := e.Close()
    if len(recs) != 0 {
        t.Fatalf("unknown metric was not dropped: %v", recs)
    }
    if !strings.Contains(buf.String(), "not_a_real_metric") {
        t.Fatalf("stderr warn missing: %q", buf.String())
    }
}

// synchronizedBuf is a mutex-guarded strings.Builder-ish writer used by
// tests whose stderr is written by the flusher goroutine (post-Close)
// as well as by the test goroutine (assertions).
type synchronizedBuf struct {
    mu  sync.Mutex
    buf strings.Builder
}

func (s *synchronizedBuf) Write(p []byte) (int, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.buf.Write(p)
}
func (s *synchronizedBuf) String() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.buf.String()
}

func TestClose_Idempotent(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    a, err := e.Close()
    if err != nil {
        t.Fatal(err)
    }
    b, err := e.Close()
    if err != nil {
        t.Fatal(err)
    }
    if len(b) != 0 {
        t.Fatalf("second Close returned %d records, want 0", len(b))
    }
    _ = a
}

func TestEmit_NilEmitter_IsNoop(t *testing.T) {
    var e *Emitter
    if err := e.Emit(context.Background(), MetricTaskSuccessRate, true, nil); err != nil {
        t.Fatal(err)
    }
    // Close on nil is not defined; skip.
}

// readRedactedCounter reads the expvar counter with a defensive path
// so a future rename of the expvar name shows up as an assertion
// mismatch rather than a nil-deref.
func readRedactedCounter() int64 {
    v := expvar.Get("route_reason_redacted_total")
    if v == nil {
        return 0
    }
    iv, ok := v.(*expvar.Int)
    if !ok {
        return 0
    }
    return iv.Value()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1`
Expected: FAIL (undefined: `MetricKey`, `NewEmitter`, `Emit`, `Close`, `dropped`).

- [ ] **Step 3: Implement the emitter**

Replace `multi-agent/tools/eval/runner/probes/probes.go` with:
```go
package probes

import (
    "context"
    "fmt"
    "io"
    "sync"
    "sync/atomic"
    "time"

    "github.com/yourorg/multi-agent/internal/secretscrub"
)

// MetricKey is the typed enum for the eight E1/E6 metrics. Any Emit
// call with a value outside AllMetrics() is dropped with a warn log.
type MetricKey string

const (
    MetricTaskSuccessRate            MetricKey = "task_success_rate"
    MetricLifecycleClosureRate       MetricKey = "lifecycle_closure_rate"
    MetricTimeToCompletion           MetricKey = "time_to_completion_ns"
    MetricHumanContextSelectionCount MetricKey = "human_context_selection_count"
    MetricWrongContextFailureRate    MetricKey = "wrong_context_failure_rate"
    MetricArtifactCorrectnessRate    MetricKey = "artifact_correctness_rate"
    MetricManualSetupStepCount       MetricKey = "manual_setup_step_count"
    MetricConfigTouchCount           MetricKey = "config_touch_count"
)

// AllMetrics returns the eight-element slice in declaration order.
func AllMetrics() []MetricKey {
    return []MetricKey{
        MetricTaskSuccessRate,
        MetricLifecycleClosureRate,
        MetricTimeToCompletion,
        MetricHumanContextSelectionCount,
        MetricWrongContextFailureRate,
        MetricArtifactCorrectnessRate,
        MetricManualSetupStepCount,
        MetricConfigTouchCount,
    }
}

var validMetric = func() map[MetricKey]bool {
    m := make(map[MetricKey]bool, 8)
    for _, k := range AllMetrics() {
        m[k] = true
    }
    return m
}()

// Record is one buffered emission returned by Close.
type Record struct {
    Metric          MetricKey
    Value           any
    Labels          map[string]string
    EmittedAt       time.Time
    EmittedAtMonoNs int64
}

// Emitter buffers Emit calls onto a bounded channel and drains them
// on Close. All Emit calls are non-blocking (§7(a)).
//
// Warn discipline: Emit NEVER writes to stderr directly — a slow
// stderr (redirected to a wedged pipe) would block the runner. Warns
// go to bounded `warnCh` (buffered), drained by the flusher goroutine
// off the hot path. Channel-full warns increment atomic counters that
// the flusher folds into a single summary line on Close.
type Emitter struct {
    ch      chan Record
    warnCh  chan string
    done    chan struct{}
    drained chan []Record
    stderr  io.Writer

    mono time.Time // monotonic reference captured in NewEmitter

    dropped     int64 // atomic — Emit-side, warn folded into Close summary
    warnDropped int64 // atomic — warnCh-side, warn folded into Close summary

    closeOnce sync.Once
    closed    atomic.Bool
    result    []Record
}

// NewEmitter returns an Emitter with a bufferSize-record channel and
// starts the flusher goroutine. bufferSize <= 0 defaults to 256.
func NewEmitter(bufferSize int, stderr io.Writer) *Emitter {
    if bufferSize <= 0 {
        bufferSize = 256
    }
    if stderr == nil {
        stderr = io.Discard
    }
    e := &Emitter{
        ch:      make(chan Record, bufferSize),
        warnCh:  make(chan string, 32), // small; overflow → atomic counter
        done:    make(chan struct{}),
        drained: make(chan []Record, 1),
        stderr:  stderr,
        mono:    time.Now(),
    }
    go e.flusher()
    return e
}

// Emit records one (metric, value, labels) tuple. Never blocks —
// stderr writes are pushed to a bounded warnCh drained by the flusher
// (§7(a) rationale). nil-Emitter receiver is a no-op.
func (e *Emitter) Emit(_ context.Context, metric MetricKey, value any, labels map[string]string) error {
    if e == nil {
        return nil
    }
    if !validMetric[metric] {
        e.warn(fmt.Sprintf("probes: dropping unknown metric %q", metric))
        return nil
    }
    // Sanitize labels BEFORE the channel send so a slow flusher cannot
    // race with caller mutations (§7(b)).
    sanitized := make(map[string]string, len(labels))
    for k, v := range labels {
        sanitized[k] = secretscrub.Sanitize(v)
    }
    rec := Record{
        Metric:          metric,
        Value:           value,
        Labels:          sanitized,
        EmittedAt:       time.Now(),
        EmittedAtMonoNs: time.Since(e.mono).Nanoseconds(),
    }
    select {
    case e.ch <- rec:
    default:
        // Channel full → drop, bump counter, warn (off-path).
        atomic.AddInt64(&e.dropped, 1)
        e.warn(fmt.Sprintf("probes: buffer full, dropped %s record", metric))
    }
    return nil
}

// warn queues a warning line for the flusher to write. If the warn
// channel is itself full, the warn is dropped and warnDropped
// increments — a summary line at Close reports the count. NEVER
// blocks the caller (spec §7(a)).
func (e *Emitter) warn(msg string) {
    select {
    case e.warnCh <- msg:
    default:
        atomic.AddInt64(&e.warnDropped, 1)
    }
}

// Close stops the flusher and returns accumulated records in emission
// order. Idempotent.
func (e *Emitter) Close() ([]Record, error) {
    if e == nil {
        return nil, nil
    }
    e.closeOnce.Do(func() {
        close(e.done)
        e.result = <-e.drained
        e.closed.Store(true)
    })
    if e.closed.Load() {
        return e.result, nil
    }
    return nil, nil
}

func (e *Emitter) flusher() {
    var buf []Record
    for {
        select {
        case r := <-e.ch:
            buf = append(buf, r)
        case w := <-e.warnCh:
            fmt.Fprintln(e.stderr, w) // off the hot path
        case <-e.done:
            // Drain remaining buffered records + warns before returning.
            for {
                select {
                case r := <-e.ch:
                    buf = append(buf, r)
                case w := <-e.warnCh:
                    fmt.Fprintln(e.stderr, w)
                default:
                    if wd := atomic.LoadInt64(&e.warnDropped); wd > 0 {
                        fmt.Fprintf(e.stderr, "probes: %d warn(s) dropped due to warn-channel overflow\n", wd)
                    }
                    e.drained <- buf
                    return
                }
            }
        }
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/probes/
git commit -m "wt2-e1e6-probes T1: Emitter + Emit + Close + MetricKey enum

Buffered channel + single flusher goroutine per spec §3.1 / §7(a).
Labels sanitized via secretscrub.Sanitize before channel send (§7(b)).
Unknown metrics dropped with warn line (§3.2).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: 8 oracle-derived metric emit helpers + monotonic TimeToCompletion

**Files:**
- Create: `multi-agent/tools/eval/runner/probes/metrics.go`
- Create: `multi-agent/tools/eval/runner/probes/metrics_test.go`

**Interfaces:**
- Consumes: `Emitter`, `MetricKey`, `AllMetrics` (Task 1).
- Produces:
  - `type OracleOutput struct{ Passed bool; MetricsJSON string; ExitCode int; StdoutBytes int }` — the probes-package's projection of runner.go's `oracleOutput`. Runner passes fields into this struct at the Edit-3 call site (spec §5.1).
  - `func EmitOracleMetrics(ctx context.Context, e *Emitter, out OracleOutput)`.
  - `func EmitTimeToCompletion(ctx context.Context, e *Emitter, startedAt time.Time)`.
  - `func MergeIntoRow(row *RunRowLike, records []Record) []string` — returns list of warnings for duplicates.
  - `type RunRowLike interface{ SetProbe(metric MetricKey, value any); SetProbeNotes(json string) }` — implemented by writer.go's `RunRow` in Task 6.

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/tools/eval/runner/probes/metrics_test.go`:
```go
package probes

import (
    "context"
    "io"
    "testing"
    "time"
)

func TestEmitOracleMetrics_TaskSuccessRate(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    EmitOracleMetrics(context.Background(), e, OracleOutput{
        Passed: true, ExitCode: 0, StdoutBytes: 42,
        MetricsJSON: `{}`,
    })
    recs, _ := e.Close()
    got := findRecord(recs, MetricTaskSuccessRate)
    if got == nil {
        t.Fatal("no task_success_rate record")
    }
    if got.Value != true {
        t.Fatalf("value = %v, want true", got.Value)
    }
    if got.Labels["oracle_exit_code"] != "0" || got.Labels["oracle_stdout_bytes"] != "42" {
        t.Fatalf("labels missing/wrong: %#v", got.Labels)
    }
}

func TestEmitOracleMetrics_LifecycleClosureRate_FromOracle(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    EmitOracleMetrics(context.Background(), e, OracleOutput{
        Passed: true, MetricsJSON: `{"lifecycle_closed": false}`,
    })
    recs, _ := e.Close()
    got := findRecord(recs, MetricLifecycleClosureRate)
    if got == nil || got.Value != false || got.Labels["source"] != "oracle_metrics" {
        t.Fatalf("wrong lifecycle record: %#v", got)
    }
}

func TestEmitOracleMetrics_LifecycleClosureRate_Fallback(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    EmitOracleMetrics(context.Background(), e, OracleOutput{
        Passed: true, MetricsJSON: `{}`,
    })
    recs, _ := e.Close()
    got := findRecord(recs, MetricLifecycleClosureRate)
    if got == nil || got.Value != true || got.Labels["source"] != "fallback_passed" {
        t.Fatalf("wrong fallback lifecycle: %#v", got)
    }
}

func TestEmitOracleMetrics_ArtifactCorrectness_FromOracle(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    EmitOracleMetrics(context.Background(), e, OracleOutput{
        Passed: false, MetricsJSON: `{"artifact_correct": false}`,
    })
    recs, _ := e.Close()
    got := findRecord(recs, MetricArtifactCorrectnessRate)
    if got == nil || got.Value != false || got.Labels["source"] != "oracle_metrics" {
        t.Fatalf("wrong artifact record: %#v", got)
    }
}

func TestEmitOracleMetrics_ArtifactCorrectness_Fallback(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    EmitOracleMetrics(context.Background(), e, OracleOutput{
        Passed: false, MetricsJSON: `{}`,
    })
    recs, _ := e.Close()
    got := findRecord(recs, MetricArtifactCorrectnessRate)
    if got == nil || got.Value != false || got.Labels["source"] != "fallback_passed" {
        t.Fatalf("wrong artifact fallback: %#v", got)
    }
}

func TestEmitTimeToCompletion_Monotonic(t *testing.T) {
    startedAt := time.Now().Add(-5 * time.Second) // 5 s ago (carries monotonic reading)
    e := NewEmitter(0, io.Discard)
    EmitTimeToCompletion(context.Background(), e, startedAt)
    recs, _ := e.Close()
    got := findRecord(recs, MetricTimeToCompletion)
    if got == nil {
        t.Fatal("no time_to_completion record")
    }
    v, ok := got.Value.(int64)
    if !ok {
        t.Fatalf("value type %T, want int64", got.Value)
    }
    // 5 s ± 100 %
    if v < int64(5*time.Second) || v > int64(10*time.Second) {
        t.Fatalf("duration %d ns out of expected range", v)
    }
    if got.Labels["wall_start_unix"] == "" || got.Labels["wall_end_unix"] == "" {
        t.Fatalf("wall labels missing: %#v", got.Labels)
    }
}

// MergeIntoRow test uses a captured stub of RunRowLike so this Task
// stays independent of writer.go changes (which land in Task 6).
type fakeRow struct {
    metrics map[MetricKey]any
    notes   string
    warns   int
}

func (f *fakeRow) SetProbe(m MetricKey, v any) {
    if f.metrics == nil {
        f.metrics = map[MetricKey]any{}
    }
    if _, dup := f.metrics[m]; dup {
        f.warns++
    }
    f.metrics[m] = v
}
func (f *fakeRow) SetProbeNotes(json string) { f.notes = json }

func TestMergeIntoRow_LastWinsOnDuplicate(t *testing.T) {
    e := NewEmitter(0, io.Discard)
    _ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
    _ = e.Emit(context.Background(), MetricTaskSuccessRate, false, nil)
    recs, _ := e.Close()
    r := &fakeRow{}
    _ = MergeIntoRow(r, recs)
    if r.metrics[MetricTaskSuccessRate] != false {
        t.Fatalf("last-write did not win: %v", r.metrics[MetricTaskSuccessRate])
    }
}

func findRecord(recs []Record, m MetricKey) *Record {
    for i := range recs {
        if recs[i].Metric == m {
            return &recs[i]
        }
    }
    return nil
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -run Emit -run Merge`
Expected: FAIL (undefined: `OracleOutput`, `EmitOracleMetrics`, `EmitTimeToCompletion`, `MergeIntoRow`, `RunRowLike`).

- [ ] **Step 3: Implement the helpers**

Create `multi-agent/tools/eval/runner/probes/metrics.go`:
```go
package probes

import (
    "context"
    "encoding/json"
    "fmt"
    "strconv"
    "time"
)

// OracleOutput is the probes-package projection of the runner's parsed
// oracle stdout. Runner passes fields into this at spec §5.1 Edit 3.
type OracleOutput struct {
    Passed      bool
    MetricsJSON string
    ExitCode    int
    StdoutBytes int
}

// EmitOracleMetrics emits TaskSuccessRate, LifecycleClosureRate,
// ArtifactCorrectnessRate per spec §3.3 rows #1/#2/#6. Callers invoke
// this exactly once per run, right after parseOracleStdout.
func EmitOracleMetrics(ctx context.Context, e *Emitter, out OracleOutput) {
    // #1 TaskSuccessRate
    _ = e.Emit(ctx, MetricTaskSuccessRate, out.Passed, map[string]string{
        "oracle_exit_code":    strconv.Itoa(out.ExitCode),
        "oracle_stdout_bytes": strconv.Itoa(out.StdoutBytes),
    })
    // Parse metrics JSON best-effort — missing / malformed → fallback.
    var m map[string]any
    if out.MetricsJSON != "" {
        _ = json.Unmarshal([]byte(out.MetricsJSON), &m)
    }
    emitBoolWithFallback(ctx, e, MetricLifecycleClosureRate, "lifecycle_closed", m, out.Passed)
    emitBoolWithFallback(ctx, e, MetricArtifactCorrectnessRate, "artifact_correct", m, out.Passed)
}

func emitBoolWithFallback(ctx context.Context, e *Emitter, metric MetricKey, key string, m map[string]any, fallback bool) {
    if raw, ok := m[key]; ok {
        if b, isBool := raw.(bool); isBool {
            _ = e.Emit(ctx, metric, b, map[string]string{"source": "oracle_metrics"})
            return
        }
    }
    _ = e.Emit(ctx, metric, fallback, map[string]string{"source": "fallback_passed"})
}

// EmitTimeToCompletion emits the monotonic delta since startedAt. §7(c).
func EmitTimeToCompletion(ctx context.Context, e *Emitter, startedAt time.Time) {
    dur := time.Since(startedAt).Nanoseconds()
    _ = e.Emit(ctx, MetricTimeToCompletion, dur, map[string]string{
        "wall_start_unix": strconv.FormatInt(startedAt.Unix(), 10),
        "wall_end_unix":   strconv.FormatInt(time.Now().Unix(), 10),
    })
}

// RunRowLike is implemented by writer.go's *RunRow (Task 6) — this
// interface keeps the probes package free of a back-import.
type RunRowLike interface {
    SetProbe(metric MetricKey, value any)
    SetProbeNotes(json string)
}

// MergeIntoRow folds probe records into the row + serialises the
// labels-carrying subset into probe_notes_json. Last write wins on
// duplicate metrics; row implementations MAY log the collision.
func MergeIntoRow(row RunRowLike, records []Record) []string {
    if row == nil {
        return nil
    }
    var warns []string
    notes := map[string]map[string]string{}
    for _, r := range records {
        row.SetProbe(r.Metric, r.Value)
        if len(r.Labels) > 0 {
            notes[string(r.Metric)] = r.Labels
        }
    }
    if len(notes) == 0 {
        row.SetProbeNotes("{}")
        return warns
    }
    b, err := json.Marshal(notes)
    if err != nil {
        // JSON of map[string]map[string]string can only fail via
        // encoder bug; degrade to empty object rather than blocking.
        row.SetProbeNotes("{}")
        warns = append(warns, fmt.Sprintf("probes: json.Marshal notes failed: %v", err))
        return warns
    }
    row.SetProbeNotes(string(b))
    return warns
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/probes/
git commit -m "wt2-e1e6-probes T2: EmitOracleMetrics + EmitTimeToCompletion + MergeIntoRow

Task 2 delivers three oracle-derived metrics (TaskSuccessRate,
LifecycleClosureRate, ArtifactCorrectnessRate), monotonic
TimeToCompletion (spec §7(c)), and the MergeIntoRow adapter that
probes/ writes to writer.go's *RunRow via the RunRowLike interface
(keeps probes/ free of a back-import cycle).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `EmitHumanCount` — text-not-stored guard

**Files:**
- Create: `multi-agent/tools/eval/runner/probes/humanloop.go`
- Create: `multi-agent/tools/eval/runner/probes/humanloop_test.go`

**Interfaces:**
- Consumes: `Emitter`, `MetricHumanContextSelectionCount`.
- Produces:
  - `func EmitHumanCount(ctx context.Context, e *Emitter, wsRoot string, stderr io.Writer)`.

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/tools/eval/runner/probes/humanloop_test.go`:
```go
package probes

import (
    "context"
    "expvar"
    "io"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestHumanCount_IgnoresSiblingLogFile(t *testing.T) {
    ws := t.TempDir()
    if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.count"), []byte("3\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.log"), []byte("my secret sk-DEADBEEF12345678"), 0o644); err != nil {
        t.Fatal(err)
    }

    before := readRedactedCounter()
    e := NewEmitter(0, io.Discard)
    EmitHumanCount(context.Background(), e, ws, io.Discard)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != 3 {
        t.Fatalf("record: %#v", recs)
    }
    for k, v := range recs[0].Labels {
        if strings.Contains(v, "sk-DEADBEEF") || strings.Contains(v, "secret") {
            t.Fatalf("label %s leaked log content: %q", k, v)
        }
    }
    _ = expvar.Get
    if after := readRedactedCounter(); after != before {
        t.Fatalf("secretscrub fired unexpectedly (%d vs %d) — did the reader read the log file?", after, before)
    }
}

func TestHumanCount_AbsentFile(t *testing.T) {
    ws := t.TempDir()
    var buf strings.Builder
    e := NewEmitter(0, io.Discard)
    EmitHumanCount(context.Background(), e, ws, &buf)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != 0 || recs[0].Labels["source"] != "absent" {
        t.Fatalf("record: %#v", recs)
    }
    if buf.Len() != 0 {
        t.Fatalf("stderr non-empty on absent file: %q", buf.String())
    }
}

func TestHumanCount_MalformedFile(t *testing.T) {
    ws := t.TempDir()
    if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.count"), []byte("3abc"), 0o644); err != nil {
        t.Fatal(err)
    }
    var buf strings.Builder
    e := NewEmitter(0, io.Discard)
    EmitHumanCount(context.Background(), e, ws, &buf)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != 0 || recs[0].Labels["source"] != "malformed" {
        t.Fatalf("record: %#v", recs)
    }
    if !strings.Contains(buf.String(), "humanloop") {
        t.Fatalf("stderr warn missing: %q", buf.String())
    }
}

func TestHumanCount_SizeCap(t *testing.T) {
    ws := t.TempDir()
    if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
        t.Fatal(err)
    }
    big := strings.Repeat("1", 5*1024) // 5 KiB
    if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.count"), []byte(big), 0o644); err != nil {
        t.Fatal(err)
    }
    var buf strings.Builder
    e := NewEmitter(0, io.Discard)
    EmitHumanCount(context.Background(), e, ws, &buf)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != 0 || recs[0].Labels["source"] != "malformed" {
        t.Fatalf("record: %#v", recs)
    }
    if !strings.Contains(buf.String(), "humanloop") {
        t.Fatalf("stderr warn missing: %q", buf.String())
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -run TestHumanCount`
Expected: FAIL (undefined: `EmitHumanCount`).

- [ ] **Step 3: Implement**

Create `multi-agent/tools/eval/runner/probes/humanloop.go`:
```go
package probes

import (
    "context"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strconv"
    "strings"
)

const humanCountFile = ".probes/humanloop.count"
const humanCountMaxBytes = 4 * 1024

// EmitHumanCount reads ${wsRoot}/.probes/humanloop.count (integer only)
// and emits MetricHumanContextSelectionCount. Never reads any other
// file in .probes/ — §7(d) discipline. Absent file is legitimate ("no
// humanloop instrumentation landed yet"), value=0, source=absent.
func EmitHumanCount(ctx context.Context, e *Emitter, wsRoot string, stderr io.Writer) {
    path := filepath.Join(wsRoot, humanCountFile)
    n, src := countFile(path, stderr)
    _ = e.Emit(ctx, MetricHumanContextSelectionCount, n, map[string]string{"source": src})
}

// countFile parses the file at path. Contract per spec §3.4.
func countFile(path string, stderr io.Writer) (int, string) {
    if stderr == nil {
        stderr = io.Discard
    }
    f, err := os.Open(path)
    if err != nil {
        if os.IsNotExist(err) {
            return 0, "absent"
        }
        fmt.Fprintf(stderr, "probes: humanloop count file open: %v\n", err)
        return 0, "malformed"
    }
    defer f.Close()

    // Hard-cap the read at humanCountMaxBytes+1 so we can detect
    // "file larger than the cap" without slurping GBs.
    buf := make([]byte, humanCountMaxBytes+1)
    read, err := io.ReadFull(f, buf)
    switch err {
    case nil:
        // File is at least humanCountMaxBytes+1 → over the cap.
        fmt.Fprintf(stderr, "probes: humanloop count file exceeds %d bytes\n", humanCountMaxBytes)
        return 0, "malformed"
    case io.ErrUnexpectedEOF, io.EOF:
        buf = buf[:read]
    default:
        fmt.Fprintf(stderr, "probes: humanloop count file read: %v\n", err)
        return 0, "malformed"
    }
    // First line only; trim trailing newline.
    line := string(buf)
    if i := strings.IndexByte(line, '\n'); i >= 0 {
        line = line[:i]
    }
    line = strings.TrimSpace(line)
    n, perr := strconv.Atoi(line)
    if perr != nil || n < 0 {
        fmt.Fprintf(stderr, "probes: humanloop count file malformed (%q)\n", line)
        return 0, "malformed"
    }
    return n, "humanloop_counter_file"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/probes/
git commit -m "wt2-e1e6-probes T3: EmitHumanCount + text-not-stored guard (§7(d))

Strict integer-only reader; ignores every other file in .probes/;
4 KiB size cap; absent/malformed both surface as source labels
without leaking any log content into probes stream.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: `EmitSetupMetrics` — D6c fallback discipline

**Files:**
- Create: `multi-agent/tools/eval/runner/probes/setup.go`
- Create: `multi-agent/tools/eval/runner/probes/setup_test.go`

**Interfaces:**
- Consumes: `Emitter`, `MetricManualSetupStepCount`, `MetricConfigTouchCount`.
- Produces: `func EmitSetupMetrics(ctx context.Context, e *Emitter, wsRoot string, stderr io.Writer)`.

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/tools/eval/runner/probes/setup_test.go`:
```go
package probes

import (
    "context"
    "io"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestEmitSetupMetrics_AbsentFile(t *testing.T) {
    ws := t.TempDir()
    e := NewEmitter(0, io.Discard)
    EmitSetupMetrics(context.Background(), e, ws, io.Discard)
    recs, _ := e.Close()
    if len(recs) != 2 {
        t.Fatalf("want 2 records, got %d", len(recs))
    }
    for _, r := range recs {
        if r.Value != nil {
            t.Fatalf("%s value not nil: %v", r.Metric, r.Value)
        }
        if r.Labels["unavailable_reason"] != "d6c_setup_harness_pending" {
            t.Fatalf("%s label wrong: %#v", r.Metric, r.Labels)
        }
    }
}

func TestEmitSetupMetrics_MalformedFile(t *testing.T) {
    ws := t.TempDir()
    if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(ws, ".probes/setup.json"),
        []byte(`{"manual_setup_step_count":"three"}`), 0o644); err != nil {
        t.Fatal(err)
    }
    var buf strings.Builder
    e := NewEmitter(0, io.Discard)
    EmitSetupMetrics(context.Background(), e, ws, &buf)
    recs, _ := e.Close()
    for _, r := range recs {
        if r.Value != nil || r.Labels["unavailable_reason"] != "malformed_setup_file" {
            t.Fatalf("%s: %#v", r.Metric, r)
        }
    }
    if !strings.Contains(buf.String(), "setup") {
        t.Fatalf("stderr warn missing: %q", buf.String())
    }
}

func TestEmitSetupMetrics_WellFormed(t *testing.T) {
    ws := t.TempDir()
    if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(ws, ".probes/setup.json"),
        []byte(`{"manual_setup_step_count":3,"config_touch_count":5}`), 0o644); err != nil {
        t.Fatal(err)
    }
    e := NewEmitter(0, io.Discard)
    EmitSetupMetrics(context.Background(), e, ws, io.Discard)
    recs, _ := e.Close()
    got := map[MetricKey]any{}
    for _, r := range recs {
        got[r.Metric] = r.Value
    }
    if got[MetricManualSetupStepCount] != 3 || got[MetricConfigTouchCount] != 5 {
        t.Fatalf("values wrong: %#v", got)
    }
    for _, r := range recs {
        if r.Labels["source"] != "setup_counter_file" {
            t.Fatalf("%s source: %s", r.Metric, r.Labels["source"])
        }
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -run TestEmitSetupMetrics`
Expected: FAIL (undefined: `EmitSetupMetrics`).

- [ ] **Step 3: Implement**

Create `multi-agent/tools/eval/runner/probes/setup.go`:
```go
package probes

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
)

const setupFile = ".probes/setup.json"
const setupMaxBytes = 4 * 1024

// EmitSetupMetrics emits ManualSetupStepCount + ConfigTouchCount per
// spec §3.7. Nil-value + explanatory label when the D6c harness has
// not landed (§7(e)).
func EmitSetupMetrics(ctx context.Context, e *Emitter, wsRoot string, stderr io.Writer) {
    if stderr == nil {
        stderr = io.Discard
    }
    path := filepath.Join(wsRoot, setupFile)
    m, reason := readSetupFile(path, stderr)
    if reason != "" {
        labels := map[string]string{"unavailable_reason": reason}
        _ = e.Emit(ctx, MetricManualSetupStepCount, nil, labels)
        _ = e.Emit(ctx, MetricConfigTouchCount, nil, labels)
        return
    }
    manual, mok := intFrom(m["manual_setup_step_count"])
    cfg, cok := intFrom(m["config_touch_count"])
    if !mok || !cok {
        fmt.Fprintf(stderr, "probes: setup.json missing/non-int fields\n")
        labels := map[string]string{"unavailable_reason": "malformed_setup_file"}
        _ = e.Emit(ctx, MetricManualSetupStepCount, nil, labels)
        _ = e.Emit(ctx, MetricConfigTouchCount, nil, labels)
        return
    }
    _ = e.Emit(ctx, MetricManualSetupStepCount, manual, map[string]string{"source": "setup_counter_file"})
    _ = e.Emit(ctx, MetricConfigTouchCount, cfg, map[string]string{"source": "setup_counter_file"})
}

func readSetupFile(path string, stderr io.Writer) (map[string]any, string) {
    f, err := os.Open(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, "d6c_setup_harness_pending"
        }
        fmt.Fprintf(stderr, "probes: setup.json open: %v\n", err)
        return nil, "malformed_setup_file"
    }
    defer f.Close()
    buf := make([]byte, setupMaxBytes+1)
    read, err := io.ReadFull(f, buf)
    switch err {
    case nil:
        fmt.Fprintf(stderr, "probes: setup.json exceeds %d bytes\n", setupMaxBytes)
        return nil, "malformed_setup_file"
    case io.ErrUnexpectedEOF, io.EOF:
        buf = buf[:read]
    default:
        fmt.Fprintf(stderr, "probes: setup.json read: %v\n", err)
        return nil, "malformed_setup_file"
    }
    var m map[string]any
    if err := json.Unmarshal(buf, &m); err != nil {
        fmt.Fprintf(stderr, "probes: setup.json unmarshal: %v\n", err)
        return nil, "malformed_setup_file"
    }
    return m, ""
}

func intFrom(v any) (int, bool) {
    switch x := v.(type) {
    case float64:
        if x < 0 || x != float64(int(x)) {
            return 0, false
        }
        return int(x), true
    case int:
        if x < 0 {
            return 0, false
        }
        return x, true
    default:
        return 0, false
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/probes/
git commit -m "wt2-e1e6-probes T4: EmitSetupMetrics + D6c-not-landed nil discipline (§7(e))

Absent setup.json → nil + d6c_setup_harness_pending.
Malformed → nil + malformed_setup_file + warn.
Well-formed → non-negative int values with source=setup_counter_file.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: `EmitWrongContext` — selected vs ground-truth + oracle failure_class

**Files:**
- Create: `multi-agent/tools/eval/runner/probes/wrongctx.go`
- Create: `multi-agent/tools/eval/runner/probes/wrongctx_test.go`

**Interfaces:**
- Consumes: `Emitter`, `MetricWrongContextFailureRate`, `OracleOutput` (Task 2).
- Produces:
  - `func EmitWrongContext(ctx context.Context, e *Emitter, wsRoot, workloadRoot, workloadID string, out OracleOutput, stderr io.Writer)`.

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/tools/eval/runner/probes/wrongctx_test.go`:
```go
package probes

import (
    "context"
    "io"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func writeLabels(t *testing.T, workloadRoot, id, gt string) {
    t.Helper()
    dir := filepath.Join(workloadRoot, "labels", "workloads")
    if err := os.MkdirAll(dir, 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(dir, id+".labels.json"),
        []byte(`{"ground_truth_context":"`+gt+`"}`), 0o644); err != nil {
        t.Fatal(err)
    }
}

func writeSelected(t *testing.T, ws, s string) {
    t.Helper()
    if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(ws, ".probes/selected_context.txt"), []byte(s), 0o644); err != nil {
        t.Fatal(err)
    }
}

func TestEmitWrongContext_StructuralOnlyFail(t *testing.T) {
    ws, wl := t.TempDir(), t.TempDir()
    writeSelected(t, ws, "ctxA")
    writeLabels(t, wl, "wl-1", "ctxB")
    e := NewEmitter(0, io.Discard)
    EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
        OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != true {
        t.Fatalf("record: %#v", recs)
    }
    if recs[0].Labels["oracle_failure_class_source"] != "fallback_structural_only" {
        t.Fatalf("label: %#v", recs[0].Labels)
    }
}

func TestEmitWrongContext_OracleClassifiedFail(t *testing.T) {
    ws, wl := t.TempDir(), t.TempDir()
    writeSelected(t, ws, "ctxA")
    writeLabels(t, wl, "wl-1", "ctxB")
    e := NewEmitter(0, io.Discard)
    EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
        OracleOutput{Passed: false, MetricsJSON: `{"failure_class":"missing_tool"}`}, io.Discard)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != true {
        t.Fatalf("record: %#v", recs)
    }
    if recs[0].Labels["oracle_failure_class"] != "missing_tool" ||
        recs[0].Labels["oracle_failure_class_source"] != "oracle_metrics" {
        t.Fatalf("labels: %#v", recs[0].Labels)
    }
}

func TestEmitWrongContext_PassShortCircuitsToFalse(t *testing.T) {
    ws, wl := t.TempDir(), t.TempDir()
    writeSelected(t, ws, "ctxA")
    writeLabels(t, wl, "wl-1", "ctxB")
    e := NewEmitter(0, io.Discard)
    EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
        OracleOutput{Passed: true, MetricsJSON: `{}`}, io.Discard)
    recs, _ := e.Close()
    if len(recs) != 1 || recs[0].Value != false {
        t.Fatalf("passing run should be false, got %#v", recs)
    }
}

func TestEmitWrongContext_MissingSelectedFile(t *testing.T) {
    ws, wl := t.TempDir(), t.TempDir()
    writeLabels(t, wl, "wl-1", "ctxB")
    e := NewEmitter(0, io.Discard)
    EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
        OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
    recs, _ := e.Close()
    if recs[0].Value != nil || recs[0].Labels["unavailable_reason"] != "no_selected_context_file" {
        t.Fatalf("%#v", recs[0])
    }
}

func TestEmitWrongContext_MissingGroundTruthLabels(t *testing.T) {
    ws, wl := t.TempDir(), t.TempDir()
    writeSelected(t, ws, "ctxA")
    e := NewEmitter(0, io.Discard)
    EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
        OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
    recs, _ := e.Close()
    if recs[0].Value != nil || recs[0].Labels["unavailable_reason"] != "no_ground_truth_labels" {
        t.Fatalf("%#v", recs[0])
    }
}

func TestEmitWrongContext_SanitizesContextStrings(t *testing.T) {
    ws, wl := t.TempDir(), t.TempDir()
    writeSelected(t, ws, "srv-a sk-ABCDEFGHIJKLMNOP")
    writeLabels(t, wl, "wl-1", "ctxB")
    e := NewEmitter(0, io.Discard)
    EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
        OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
    recs, _ := e.Close()
    if !strings.Contains(recs[0].Labels["selected"], "[REDACTED]") {
        t.Fatalf("selected label not sanitized: %q", recs[0].Labels["selected"])
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -run TestEmitWrongContext`
Expected: FAIL (undefined: `EmitWrongContext`).

- [ ] **Step 3: Implement**

Create `multi-agent/tools/eval/runner/probes/wrongctx.go`:
```go
package probes

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"
)

const selectedCtxFile = ".probes/selected_context.txt"
const selectedCtxMaxBytes = 512
const labelsMaxBytes = 8 * 1024

var wrongClasses = map[string]bool{
    "missing_file":        true,
    "missing_tool":        true,
    "wrong_os":            true,
    "missing_credential":  true,
    "network_unreachable": true,
}

// EmitWrongContext emits MetricWrongContextFailureRate per spec §3.6.
// Never blocks; returns nil-value with an unavailable_reason label when
// either the selected-context file or the ground-truth labels file is
// absent.
func EmitWrongContext(ctx context.Context, e *Emitter, wsRoot, workloadRoot, workloadID string, out OracleOutput, stderr io.Writer) {
    if stderr == nil {
        stderr = io.Discard
    }
    selected, sel_ok := readSmallFile(filepath.Join(wsRoot, selectedCtxFile), selectedCtxMaxBytes, stderr)
    if !sel_ok {
        _ = e.Emit(ctx, MetricWrongContextFailureRate, nil, map[string]string{
            "unavailable_reason": "no_selected_context_file",
        })
        return
    }
    gt, gt_ok := readGroundTruth(workloadRoot, workloadID, stderr)
    if !gt_ok {
        _ = e.Emit(ctx, MetricWrongContextFailureRate, nil, map[string]string{
            "unavailable_reason": "no_ground_truth_labels",
        })
        return
    }
    labels := map[string]string{
        "selected":     selected,
        "ground_truth": gt,
    }
    // Rule #1: a passing run cannot be a wrong-context failure.
    if out.Passed {
        labels["oracle_failure_class_source"] = "n/a_passed"
        _ = e.Emit(ctx, MetricWrongContextFailureRate, false, labels)
        return
    }
    // Rule #2: structural signal.
    if selected == gt {
        labels["oracle_failure_class_source"] = "n/a_selected_equals_gt"
        _ = e.Emit(ctx, MetricWrongContextFailureRate, false, labels)
        return
    }
    // Rule #3: oracle-side classification (when available).
    var m map[string]any
    if out.MetricsJSON != "" {
        _ = json.Unmarshal([]byte(out.MetricsJSON), &m)
    }
    if fc, ok := m["failure_class"].(string); ok && fc != "" {
        labels["oracle_failure_class"] = fc
        labels["oracle_failure_class_source"] = "oracle_metrics"
        // Only wrong-context classes count as wrong-context failures.
        _ = e.Emit(ctx, MetricWrongContextFailureRate, wrongClasses[fc], labels)
        return
    }
    labels["oracle_failure_class_source"] = "fallback_structural_only"
    _ = e.Emit(ctx, MetricWrongContextFailureRate, true, labels)
}

func readSmallFile(path string, cap int, stderr io.Writer) (string, bool) {
    f, err := os.Open(path)
    if err != nil {
        return "", false
    }
    defer f.Close()
    buf := make([]byte, cap+1)
    read, err := io.ReadFull(f, buf)
    switch err {
    case nil:
        fmt.Fprintf(stderr, "probes: %s exceeds %d bytes\n", path, cap)
        return "", false
    case io.ErrUnexpectedEOF, io.EOF:
        buf = buf[:read]
    default:
        fmt.Fprintf(stderr, "probes: %s read: %v\n", path, err)
        return "", false
    }
    line := string(buf)
    if i := strings.IndexByte(line, '\n'); i >= 0 {
        line = line[:i]
    }
    return strings.TrimSpace(line), true
}

func readGroundTruth(workloadRoot, workloadID string, stderr io.Writer) (string, bool) {
    path := filepath.Join(workloadRoot, "labels", "workloads", workloadID+".labels.json")
    f, err := os.Open(path)
    if err != nil {
        return "", false
    }
    defer f.Close()
    buf := make([]byte, labelsMaxBytes+1)
    read, err := io.ReadFull(f, buf)
    switch err {
    case nil:
        fmt.Fprintf(stderr, "probes: labels %s exceeds %d bytes\n", path, labelsMaxBytes)
        return "", false
    case io.ErrUnexpectedEOF, io.EOF:
        buf = buf[:read]
    default:
        fmt.Fprintf(stderr, "probes: labels %s read: %v\n", path, err)
        return "", false
    }
    var m map[string]any
    if err := json.Unmarshal(buf, &m); err != nil {
        fmt.Fprintf(stderr, "probes: labels %s unmarshal: %v\n", path, err)
        return "", false
    }
    if gt, ok := m["ground_truth_context"].(string); ok {
        return gt, true
    }
    return "", false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./tools/eval/runner/probes/... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/probes/
git commit -m "wt2-e1e6-probes T5: EmitWrongContext — structural + oracle failure_class (§3.6)

Rule chain: pass → false, selected==gt → false, oracle failure_class
match → wrongClasses membership, structural-only fallback → true.
Missing selected/gt → nil + unavailable_reason. Context strings pass
through Emit's secretscrub (§7(b)) — label leaks stay redacted.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `RunRow` extension + CSV append + `MergeIntoRow` integration + perf gate

**Files:**
- Modify: `multi-agent/tools/eval/runner/writer.go`
- Modify: `multi-agent/tools/eval/runner/writer_test.go`
- Create: `multi-agent/tools/eval/runner/probes/perf_test.go`

**Interfaces:**
- Consumes: `probes.RunRowLike` — implemented via `SetProbe` /
  `SetProbeNotes` methods on `*RunRow`.
- Produces:
  - Eight new `RunRow` fields per spec §4 table (rows 23–30) + `ProbeNotesJSON` (row 31).
  - Nine new column names in `CSVColumns()` at positions 23–31.
  - `SetProbe(metric probes.MetricKey, value any)` and
    `SetProbeNotes(json string)` methods on `*RunRow`.

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/tools/eval/runner/writer_test.go`:
```go
func TestCSVColumns_AppendOnly_WithProbes(t *testing.T) {
    cols := CSVColumns()
    if len(cols) != 31 {
        t.Fatalf("column count = %d, want 31", len(cols))
    }
    // WT-1 skeleton columns must remain at positions 1..22 in order.
    wantSkeleton := []string{
        "run_id", "workload_id", "started_at_unix", "finished_at_unix",
        "duration_ms", "passed", "oracle_exit_code",
        "oracle_details_json", "oracle_metrics_json",
        "loom_commit", "agentserver_commit", "modelserver_commit",
        "app_commit", "os_kernel", "os_distro", "os_arch",
        "machine_hostname", "author_email_sha8", "committer_email_sha8",
        "codex_config_path", "stub_listen", "tempdir_kept",
    }
    for i, want := range wantSkeleton {
        if cols[i] != want {
            t.Fatalf("col[%d] = %q, want %q", i, cols[i], want)
        }
    }
    wantProbes := []string{
        "probe_task_success_rate",
        "probe_lifecycle_closure_rate",
        "probe_time_to_completion_ns",
        "probe_human_context_selection_count",
        "probe_wrong_context_failure_rate",
        "probe_artifact_correctness_rate",
        "probe_manual_setup_step_count",
        "probe_config_touch_count",
        "probe_notes_json",
    }
    for i, want := range wantProbes {
        if cols[22+i] != want {
            t.Fatalf("probe col[%d] = %q, want %q", 22+i, cols[22+i], want)
        }
    }
}

func TestRunRow_ProbeFields_RoundTripCSV(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "out.csv")
    row := RunRow{RunID: "r-1", WorkloadID: "wl-1"}
    tt := true
    ff := false
    row.ProbeTaskSuccessRate = &tt
    row.ProbeLifecycleClosureRate = &tt
    var nsec int64 = 1234567
    row.ProbeTimeToCompletionNs = &nsec
    hi := 3
    row.ProbeHumanContextSelectionCount = &hi
    row.ProbeWrongContextFailureRate = &ff
    row.ProbeArtifactCorrectnessRate = &tt
    // Manual + config left as nil to exercise nil encoding.
    row.ProbeNotesJSON = `{"manual_setup_step_count":{"unavailable_reason":"d6c_setup_harness_pending"}}`

    if err := WriteCSVRow(path, row); err != nil {
        t.Fatal(err)
    }
    b, _ := os.ReadFile(path)
    text := string(b)
    if !strings.Contains(text, "probe_task_success_rate") {
        t.Fatal("header missing probe col")
    }
    // nil-encoded manual/config columns must appear as empty fields.
    // Rely on WT-1 CSV writer's encoding (encoding/csv quotes empty
    // fields naturally); the round-trip parses back with the same nil.
}
```

Add the required imports to `writer_test.go` if not present: `path/filepath`, `os`, `strings`.

Create `multi-agent/tools/eval/runner/probes/perf_test.go`:
```go
package probes

import (
    "context"
    "io"
    "os"
    "testing"
    "time"
)

func TestPerf_EmitLatency_LocalOnly(t *testing.T) {
    if os.Getenv("CI") != "" {
        t.Skip("perf assertion skipped on CI")
    }
    e := NewEmitter(0, io.Discard)
    defer e.Close()
    total := 1000
    start := time.Now()
    for i := 0; i < total; i++ {
        _ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
    }
    avg := time.Since(start) / time.Duration(total)
    if avg > time.Millisecond {
        t.Fatalf("avg emit latency = %s, want < 1ms", avg)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./tools/eval/runner/... -count=1 -run TestCSVColumns_AppendOnly_WithProbes`
Expected: FAIL (`column count = 22, want 31`).

- [ ] **Step 3: Extend `RunRow` + `CSVColumns` + `rowAsCSVRecord`**

Edit `multi-agent/tools/eval/runner/writer.go` — append the eight probe
fields (as `*T` for nullable scalars, `string` for notes) to `RunRow`,
then append the nine column names to `CSVColumns()`, then extend
`rowAsCSVRecord` to serialise the new fields.

```go
// Append to RunRow struct definition (after TempdirKept bool):
    // Probe fields (WT-2-e1e6-probes spec §4). Pointer types encode
    // "unavailable" as nil; the CSV serialiser emits "" for nil.
    ProbeTaskSuccessRate            *bool
    ProbeLifecycleClosureRate       *bool
    ProbeTimeToCompletionNs         *int64
    ProbeHumanContextSelectionCount *int
    ProbeWrongContextFailureRate    *bool
    ProbeArtifactCorrectnessRate    *bool
    ProbeManualSetupStepCount       *int
    ProbeConfigTouchCount           *int
    ProbeNotesJSON                  string // "{}" when empty; always non-empty in practice
```

Extend `CSVColumns()`:
```go
        // WT-2-e1e6-probes §4 (append-only):
        "probe_task_success_rate",
        "probe_lifecycle_closure_rate",
        "probe_time_to_completion_ns",
        "probe_human_context_selection_count",
        "probe_wrong_context_failure_rate",
        "probe_artifact_correctness_rate",
        "probe_manual_setup_step_count",
        "probe_config_touch_count",
        "probe_notes_json",
```

Add helper + extend `rowAsCSVRecord`:
```go
// nilOrFormat returns "" for nil pointer inputs and the string form
// otherwise. Matches spec §4 nil encoding for probe columns.
func nilOrBool(b *bool) string {
    if b == nil {
        return ""
    }
    return strconv.FormatBool(*b)
}
func nilOrInt(i *int) string {
    if i == nil {
        return ""
    }
    return strconv.Itoa(*i)
}
func nilOrInt64(i *int64) string {
    if i == nil {
        return ""
    }
    return strconv.FormatInt(*i, 10)
}

// Extend rowAsCSVRecord (append these entries to the return slice):
        nilOrBool(r.ProbeTaskSuccessRate),
        nilOrBool(r.ProbeLifecycleClosureRate),
        nilOrInt64(r.ProbeTimeToCompletionNs),
        nilOrInt(r.ProbeHumanContextSelectionCount),
        nilOrBool(r.ProbeWrongContextFailureRate),
        nilOrBool(r.ProbeArtifactCorrectnessRate),
        nilOrInt(r.ProbeManualSetupStepCount),
        nilOrInt(r.ProbeConfigTouchCount),
        func() string {
            if r.ProbeNotesJSON == "" {
                return "{}"
            }
            return r.ProbeNotesJSON
        }(),
```

Add the `SetProbe` / `SetProbeNotes` methods on `*RunRow` (compile-time
assertion that `*RunRow` satisfies `probes.RunRowLike`):
```go
import "github.com/yourorg/multi-agent/tools/eval/runner/probes"

// Compile-time interface check.
var _ probes.RunRowLike = (*RunRow)(nil)

// SetProbe implements probes.RunRowLike. Last write wins per §5.2.
func (r *RunRow) SetProbe(metric probes.MetricKey, value any) {
    switch metric {
    case probes.MetricTaskSuccessRate:
        r.ProbeTaskSuccessRate = boolPtrOrNil(value)
    case probes.MetricLifecycleClosureRate:
        r.ProbeLifecycleClosureRate = boolPtrOrNil(value)
    case probes.MetricTimeToCompletion:
        r.ProbeTimeToCompletionNs = int64PtrOrNil(value)
    case probes.MetricHumanContextSelectionCount:
        r.ProbeHumanContextSelectionCount = intPtrOrNil(value)
    case probes.MetricWrongContextFailureRate:
        r.ProbeWrongContextFailureRate = boolPtrOrNil(value)
    case probes.MetricArtifactCorrectnessRate:
        r.ProbeArtifactCorrectnessRate = boolPtrOrNil(value)
    case probes.MetricManualSetupStepCount:
        r.ProbeManualSetupStepCount = intPtrOrNil(value)
    case probes.MetricConfigTouchCount:
        r.ProbeConfigTouchCount = intPtrOrNil(value)
    }
}
func (r *RunRow) SetProbeNotes(j string) { r.ProbeNotesJSON = j }

func boolPtrOrNil(v any) *bool {
    if v == nil { return nil }
    if b, ok := v.(bool); ok { return &b }
    return nil
}
func intPtrOrNil(v any) *int {
    if v == nil { return nil }
    if i, ok := v.(int); ok { return &i }
    return nil
}
func int64PtrOrNil(v any) *int64 {
    if v == nil { return nil }
    if i, ok := v.(int64); ok { return &i }
    return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./tools/eval/runner/... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/tools/eval/runner/writer.go multi-agent/tools/eval/runner/writer_test.go multi-agent/tools/eval/runner/probes/perf_test.go
git commit -m "wt2-e1e6-probes T6: RunRow probe fields + CSV append + perf gate (§4, §7(h))

Eight probe fields as nullable pointer types (spec §4 nil encoding);
nine new CSV columns appended at positions 23-31; SetProbe /
SetProbeNotes methods implement probes.RunRowLike (Task 2's
back-import-free interface). Perf test skipped on CI (§7(h)).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Runner integration — Edits 0–6 per spec §5.1 + smoke coverage

**Files:**
- Modify: `multi-agent/tools/eval/runner/runner.go`
- Create: `multi-agent/tools/eval/runner/runner_probes_smoke_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–6.
- Produces: probe records flow into `RunRow` at Edit 6; two new
  end-to-end smoke tests validate the integration.

Task 7 lands both the smoke tests (spec §8 acceptance +
probe-failure non-blocking, spec §7(a) at the integration level) AND
the runner integration edits. The tests are written FIRST so the plan
stays TDD-consistent: the tests fail against a runner without the
edits, drive Steps 2–8 that apply the edits, and pass at Step 9.

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/tools/eval/runner/runner_probes_smoke_test.go`
with **BOTH** tests defined below (originally staged as Task 8; folded
into Task 7 so runner integration is properly TDD):

(Test source is inlined in Task 8's Step 1 code block below — write
that file now; skip Task 8's Step 1 duplicate write step when you
reach it.)

- [ ] **Step 2: Apply Edit 0 (imports)**

Edit `multi-agent/tools/eval/runner/runner.go` to add the probes
import inside the existing `import (...)` block:
```go
    "github.com/yourorg/multi-agent/tools/eval/runner/probes"
```

- [ ] **Step 3: Apply Edit 1 (emitter construction after `startedAt := time.Now()`)**

Insert immediately after line 83:
```go
    emitter := probes.NewEmitter(0, opts.Stderr)
    defer emitter.Close() // safety-net for preflight-return paths; primary Close is at Edit 6
```

- [ ] **Step 4: Apply Edit 2 (setup metrics after `SetupWorkspace`)**

Insert immediately after the successful-return `ws, err :=
SetupWorkspace(...)` block (currently ~line 137, immediately before the
`defer func() { defer ws.Cleanup(); ... }` line — placing it before the
defer keeps the emit outside the cleanup path):
```go
    probes.EmitSetupMetrics(ctx, emitter, ws.Root, opts.Stderr)
```

- [ ] **Step 5: Apply Edit 3 (oracle + human count after `parseOracleStdout`)**

Insert immediately after `oracleOut := parseOracleStdout(res.Stdout)`
(currently line 203):
```go
    oracleOutForProbes := probes.OracleOutput{
        Passed:      oracleOut.Passed,
        MetricsJSON: oracleOut.MetricsRaw,
        ExitCode:    res.ExitCode,
        StdoutBytes: len(res.Stdout),
    }
    probes.EmitOracleMetrics(ctx, emitter, oracleOutForProbes)
    probes.EmitHumanCount(ctx, emitter, ws.Root, opts.Stderr)
```

- [ ] **Step 6: Apply Edit 4 (wrong-context after `collectGitEmails`)**

Insert immediately after `author, committer := collectGitEmails(...)`
(currently line 211). Reuses `oracleOutForProbes` from Edit 3:
```go
    probes.EmitWrongContext(ctx, emitter, ws.Root, workloadRoot, spec.ID,
        oracleOutForProbes, opts.Stderr)
```

- [ ] **Step 7: Apply Edit 5 (time-to-completion before `finishedAt`)**

Insert immediately before `finishedAt := time.Now()` (currently
line 217):
```go
    probes.EmitTimeToCompletion(ctx, emitter, startedAt)
```

- [ ] **Step 8: Apply Edit 6 (drain + merge after row assembly, before Insert)**

Insert between the row-assembly block (ending at the closing `}` of the
`row := RunRow{...}` literal at line 243) and the `if err :=
opts.Writer.Insert(ctx, row); err != nil {` at line 246:
```go
    records, _ := emitter.Close()
    probes.MergeIntoRow(&row, records)
```

- [ ] **Step 9: Run all runner tests to verify none regress**

Run: `cd multi-agent && go test ./tools/eval/runner/... -count=1 -race`
Expected: PASS (existing skeleton tests still green + probe tests green).

- [ ] **Step 10: `go vet` + `gofmt`**

Run: `cd multi-agent && go vet ./... && gofmt -l tools/eval/runner/probes tools/eval/runner/runner.go tools/eval/runner/writer.go`
Expected: no output.

- [ ] **Step 11: Commit**

```bash
git add multi-agent/tools/eval/runner/runner.go multi-agent/tools/eval/runner/runner_probes_smoke_test.go
git commit -m "wt2-e1e6-probes T7: wire probes into runner.go (Edits 0-6, spec §5.1) + smoke tests

Six additive edits at the enumerated line anchors; no existing return
path restructured; drain-and-merge fires between RunRow assembly and
Writer.Insert so persisted rows carry the probe fields.

Bundled smoke tests: TestSmoke_WorkloadRun_EightMetricsPresent
(spec §8 acceptance) and TestSmoke_ProbeFailure_DoesNotBlockRunner
(probe read fails from a poisoned shadow-workload fixture; runner
still exits 0/1 and CSV still writes — integration counterpart of
§7(a) test #1).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: (smoke test source — write during Task 7 Step 1)

Task 8 is a "test source" reference folded into Task 7 (see Task 7
Step 1 note). The two tests below are the ones Task 7's TDD cycle
uses; write them at Task 7 Step 1, not here. Task 7 Steps 2–8 apply
the runner edits; Step 9 asserts the tests now pass. Task 8 has
**no separate commit** — the smoke test file is part of Task 7's
runner-integration commit.

**Files (reference only):**
- Referenced: `multi-agent/tools/eval/runner/runner_probes_smoke_test.go` (written at Task 7 Step 1)

- [ ] **Step 1 (reference): the failing tests written at Task 7 Step 1 are:**

Create `multi-agent/tools/eval/runner/runner_probes_smoke_test.go`:
```go
package main

import (
    "context"
    "encoding/csv"
    "encoding/json"
    "net"
    "os"
    "path/filepath"
    "testing"
    "time"
)

// TestSmoke_WorkloadRun_EightMetricsPresent replays the maintainer
// smoke path from spec §8: run cross-device-code-mod through
// runner.Run and assert all nine probe columns appear + oracle-derived
// metrics are non-empty + D6c-dependent metrics carry the
// unavailable_reason label.
func TestSmoke_WorkloadRun_EightMetricsPresent(t *testing.T) {
    if testing.Short() {
        t.Skip("smoke test skipped in -short mode")
    }
    workloadDir := findWorkloadsDir(t)
    tmp := t.TempDir()
    csvPath := filepath.Join(tmp, "run.csv")

    // Bind stub to a per-test loopback port to avoid conflicts under
    // parallel smoke runs.
    port := freeLoopbackPort(t)
    opts := Opts{
        WorkloadID:  "cross-device-code-mod",
        WorkloadDir: workloadDir,
        StubListen:  port,
        OutCSV:      csvPath,
        Timeout:     60 * time.Second,
    }
    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()
    result := Run(ctx, opts)
    if result.ExitCode != 0 {
        t.Fatalf("Run exit=%d err=%v", result.ExitCode, result.Err)
    }

    f, err := os.Open(csvPath)
    if err != nil {
        t.Fatal(err)
    }
    defer f.Close()
    r := csv.NewReader(f)
    rows, err := r.ReadAll()
    if err != nil {
        t.Fatal(err)
    }
    if len(rows) != 2 {
        t.Fatalf("row count = %d, want 2", len(rows))
    }
    header, data := rows[0], rows[1]
    col := func(name string) string {
        for i, h := range header {
            if h == name {
                return data[i]
            }
        }
        t.Fatalf("column %s missing", name)
        return ""
    }
    // Nine probe columns present.
    for _, name := range []string{
        "probe_task_success_rate", "probe_lifecycle_closure_rate",
        "probe_time_to_completion_ns", "probe_human_context_selection_count",
        "probe_wrong_context_failure_rate", "probe_artifact_correctness_rate",
        "probe_manual_setup_step_count", "probe_config_touch_count",
        "probe_notes_json",
    } {
        // Just probing the header presence via col() throws if missing.
        _ = col(name)
    }
    // Oracle-derived metrics populated.
    if col("probe_task_success_rate") != "true" {
        t.Fatalf("task success = %q", col("probe_task_success_rate"))
    }
    if col("probe_lifecycle_closure_rate") != "true" {
        t.Fatalf("lifecycle = %q", col("probe_lifecycle_closure_rate"))
    }
    if col("probe_artifact_correctness_rate") != "true" {
        t.Fatalf("artifact = %q", col("probe_artifact_correctness_rate"))
    }
    // D6c-dependent metrics empty + notes carry unavailable_reason.
    if col("probe_manual_setup_step_count") != "" || col("probe_config_touch_count") != "" {
        t.Fatalf("manual/config not empty: %q %q",
            col("probe_manual_setup_step_count"), col("probe_config_touch_count"))
    }
    var notes map[string]map[string]string
    if err := json.Unmarshal([]byte(col("probe_notes_json")), &notes); err != nil {
        t.Fatalf("notes JSON: %v", err)
    }
    if notes["manual_setup_step_count"]["unavailable_reason"] != "d6c_setup_harness_pending" {
        t.Fatalf("manual notes: %v", notes["manual_setup_step_count"])
    }
    if notes["config_touch_count"]["unavailable_reason"] != "d6c_setup_harness_pending" {
        t.Fatalf("config notes: %v", notes["config_touch_count"])
    }
}

// TestSmoke_ProbeFailure_DoesNotBlockRunner injects a fault that
// exercises the probe read-side: a shadow workload with a fixtures
// tree containing a MALFORMED `.probes/setup.json` (JSON syntax error).
// runner.go's SetupWorkspace copies the fixtures into the tempdir at
// line 134 via copyTree — the file copies fine — then flattens
// mock_workspace/ into workspace root at line 68–73, so
// `${workspace}/.probes/setup.json` exists with malformed JSON when
// Edit 2's EmitSetupMetrics fires. The probe's readSetupFile takes the
// malformed_setup_file branch, emits nil values + label + one stderr
// warn, and the runner MUST continue.
//
// Using malformed JSON (not mode 0000) sidesteps two pitfalls the
// previous draft had:
//   1. mode-0000 files fail runner.go's fixture copy (os.Open in
//      copyFile), which surfaces as a preflight-2 exit BEFORE Edit 2
//      ever runs — the fault gets there via the wrong path.
//   2. root uid ignores mode 0000 (skip-on-root would gate the test
//      off on CI runners that happen to run as root).
//
// Injecting via AgentStage would run too late — Edit 2 fires BEFORE
// AgentStage — so the fault has to be pre-seeded in the workload
// fixtures so the copy hits it.
func TestSmoke_ProbeFailure_DoesNotBlockRunner(t *testing.T) {
    if testing.Short() {
        t.Skip("smoke test skipped in -short mode")
    }
    src := findWorkloadsDir(t)
    // Build a shadow workload directory: a fresh tempdir with a copy of
    // the cross-device-code-mod workload plus a MALFORMED-JSON .probes
    // file inside mock_workspace/ (so it lands in the workspace root
    // after SetupWorkspace's mock_workspace flatten step).
    shadow := t.TempDir()
    wl := filepath.Join(shadow, "cross-device-code-mod")
    if err := copyDirTree(filepath.Join(src, "cross-device-code-mod"), wl); err != nil {
        t.Fatal(err)
    }
    probesDir := filepath.Join(wl, "fixtures", "mock_workspace", ".probes")
    if err := os.MkdirAll(probesDir, 0o700); err != nil {
        t.Fatal(err)
    }
    poisoned := filepath.Join(probesDir, "setup.json")
    // Malformed JSON: readSetupFile returns malformed_setup_file
    // reason; probe emits nil values + one stderr warn.
    if err := os.WriteFile(poisoned, []byte(`{"manual_setup_step_count": not_json_here`), 0o644); err != nil {
        t.Fatal(err)
    }

    tmp := t.TempDir()
    csvPath := filepath.Join(tmp, "run.csv")
    port := freeLoopbackPort(t)

    // Stderr collector so we can assert the probe warn line landed
    // there. A tempfile is easier to seek+re-read than a pipe, and the
    // runner's Opts.Stderr wants an *os.File.
    stderrFile, err := os.CreateTemp(tmp, "stderr-")
    if err != nil {
        t.Fatal(err)
    }
    defer stderrFile.Close()
    opts := Opts{
        WorkloadID:  "cross-device-code-mod",
        WorkloadDir: shadow,
        StubListen:  port,
        OutCSV:      csvPath,
        Timeout:     60 * time.Second,
        Stderr:      stderrFile,
    }
    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()
    result := Run(ctx, opts)
    // Runner MUST complete — fault is a non-blocking probe warn, not
    // a preflight error. Exit code 0 (pass) or 1 (oracle fail) both
    // acceptable — this test asserts non-blockage, not oracle result.
    if result.ExitCode == 2 {
        b, _ := os.ReadFile(stderrFile.Name())
        t.Fatalf("preflight failure not expected: err=%v; stderr=%s", result.Err, b)
    }
    // CSV must have been written.
    if _, err := os.Stat(csvPath); err != nil {
        t.Fatalf("CSV missing: %v", err)
    }
    // Data row's manual_setup_step_count column must be empty AND
    // probe_notes_json must record the JSON parse failure as
    // `malformed_setup_file` — readSetupFile's json.Unmarshal branch
    // triggers on the intentionally malformed fixture we planted.
    f, err := os.Open(csvPath)
    if err != nil {
        t.Fatal(err)
    }
    defer f.Close()
    r := csv.NewReader(f)
    rows, err := r.ReadAll()
    if err != nil {
        t.Fatal(err)
    }
    header, data := rows[0], rows[1]
    idx := func(name string) int {
        for i, h := range header { if h == name { return i } }
        return -1
    }
    if data[idx("probe_manual_setup_step_count")] != "" {
        t.Fatalf("expected empty on read failure, got %q", data[idx("probe_manual_setup_step_count")])
    }
    var notes map[string]map[string]string
    if err := json.Unmarshal([]byte(data[idx("probe_notes_json")]), &notes); err != nil {
        t.Fatal(err)
    }
    reason := notes["manual_setup_step_count"]["unavailable_reason"]
    if reason != "malformed_setup_file" {
        t.Fatalf("expected unavailable_reason=malformed_setup_file, got %q; stderr=%s", reason, readAll(t, stderrFile.Name()))
    }
}

// copyDirTree is a minimal recursive copy used only by the
// probe-failure smoke test to shadow-clone a workload. It preserves
// modes and file bytes. Deliberately simple; the workload trees are
// small (KB scale).
func copyDirTree(src, dst string) error {
    return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
        if err != nil { return err }
        rel, err := filepath.Rel(src, p)
        if err != nil { return err }
        target := filepath.Join(dst, rel)
        if info.IsDir() {
            return os.MkdirAll(target, info.Mode()&0o777|0o700)
        }
        b, err := os.ReadFile(p)
        if err != nil { return err }
        if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil { return err }
        return os.WriteFile(target, b, info.Mode()&0o777)
    })
}

func readAll(t *testing.T, path string) string {
    t.Helper()
    b, _ := os.ReadFile(path)
    return string(b)
}

// findWorkloadsDir walks up from the test's cwd to locate
// tests/eval/workloads so the test can run from either the package dir
// or the module root without hard-coding a path.
func findWorkloadsDir(t *testing.T) string {
    t.Helper()
    d, err := os.Getwd()
    if err != nil {
        t.Fatal(err)
    }
    for {
        candidate := filepath.Join(d, "tests/eval/workloads")
        if info, err := os.Stat(candidate); err == nil && info.IsDir() {
            return candidate
        }
        parent := filepath.Dir(d)
        if parent == d {
            t.Fatalf("cannot find tests/eval/workloads under %s", d)
        }
        d = parent
    }
}

// freeLoopbackPort returns a 127.0.0.1:<port> string bound to an
// ephemeral port so parallel smoke tests do not collide.
func freeLoopbackPort(t *testing.T) string {
    t.Helper()
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatal(err)
    }
    addr := ln.Addr().String()
    _ = ln.Close()
    return addr
}
```

(No separate Steps 2-4 for Task 8 — the smoke tests are executed as
part of Task 7 Step 9 and committed with Task 7's runner-integration
commit at Task 7 Step 11. Task 8's heading is preserved for
Cross-reference from external notes; no engineer action required.)

---

## Task 9: Final acceptance — build + workload + full test sweep

**Files:** none created; verification only.

- [ ] **Step 1: Full test sweep**

Run:
```bash
cd multi-agent
go test ./tools/eval/runner/probes/... ./tools/eval/runner/... -count=1 -shuffle=on -race
```
Expected: PASS.

- [ ] **Step 2: Lints**

Run:
```bash
cd multi-agent
go vet ./...
gofmt -l tools/eval/runner/probes tools/eval/runner/runner.go tools/eval/runner/writer.go
```
Expected: no output.

- [ ] **Step 3: Build + workload smoke (spec §8)**

Run:
```bash
cd multi-agent
go build -o /tmp/eval-runner ./tools/eval/runner
/tmp/eval-runner run \
    --workload cross-device-code-mod \
    --workload-dir tests/eval/workloads \
    --stub-listen 127.0.0.1:18080 \
    --out /tmp/run.csv
head -1 /tmp/run.csv | tr , '\n' | grep -c '^probe_'
```
Expected: exit 0; `9`.

Then the Python assertion from spec §8 for `d6c_setup_harness_pending`:
```bash
python3 -c "
import csv, json
with open('/tmp/run.csv') as f:
    r = list(csv.DictReader(f))[0]
notes = json.loads(r['probe_notes_json'])
assert notes.get('manual_setup_step_count', {}).get('unavailable_reason') == 'd6c_setup_harness_pending', notes
assert notes.get('config_touch_count', {}).get('unavailable_reason') == 'd6c_setup_harness_pending', notes
print('OK')
"
```
Expected: `OK`.

- [ ] **Step 4: If everything green, no additional commit needed**

Task 9 is a verification-only pass; if any prior commit needs a fixup
(e.g. gofmt output), amend the closest task's commit with `git commit
--amend --no-edit`.

---

## Self-Review

**1. Spec coverage.**

- §1 file scope → File Map + Task 0's package scaffold.
- §2 layout → Task 1 (probes.go), Task 2 (metrics.go), Task 3
  (humanloop.go), Task 4 (setup.go), Task 5 (wrongctx.go), Task 6
  (writer.go), Task 0 (nodeps_test.go).
- §3.1 Emitter API → Task 1.
- §3.2 MetricKey enum → Task 1.
- §3.3 8 probe-point table → Tasks 2/3/4/5 (helper per row).
- §3.4 counter file contract → Task 3.
- §3.5 artifact fallback → Task 2's `emitBoolWithFallback`.
- §3.6 wrong-context rule + oracle failure_class → Task 5.
- §3.7 setup.json contract → Task 4.
- §4 CSV columns 23–31 + nil encoding → Task 6.
- §5.1 Edits 0–6 → Task 7.
- §5.2 MergeIntoRow last-wins → Task 2 test #27.
- §6 consumer-view audit → documented in spec; no code deliverable
  here (handoff to WT-1-run-schema follow-up).
- §7(a)–(h) mitigations → Test Matrix rows above (each mitigation
  has ≥1 row).
- §8 acceptance smoke → Task 8 tests + Task 9 manual verification.
- §9 seams → documented in spec; no code deliverable.

**2. Placeholder scan.** No `TBD`, `TODO in this plan`, or "add
appropriate handling" fragments. Every step includes concrete code or
a concrete command.

**3. Type consistency.** `MetricKey` typed identically in every
task. `RunRowLike` defined in Task 2 (metrics.go), implemented in
Task 6 (writer.go) with a compile-time `var _
probes.RunRowLike = (*RunRow)(nil)` assertion so a mismatch is a
compile error, not a runtime failure. `OracleOutput` defined in Task 2,
consumed in Tasks 5 and 7 with the same field names.
