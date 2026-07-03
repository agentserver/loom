# WT-2-e1e6-probes — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 2 table row
> **WT-2-e1e6-probes** (line 104).
> Branch: `paper/v3/p2-e1e6-probes`.
> Base: `origin/paper/v3-integration` HEAD = `d053897`.
> Authoritative metric list: `/root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md`
> §Metrics (lines 30–95) + §E1 Lifecycle macrobenchmark (line 116) + §E6
> Reproducibility (line 237).
> Task brief: 12 号 §D8 (line 107).

---

## 1. Task boundary & file scope

This worktree adds a **new `probes` subpackage** under
`multi-agent/tools/eval/runner/probes/` and inserts a small, additive set
of **probe call-sites** into the existing `runner.go` orchestrator. It
does NOT restructure the runner, change the CSV column set beyond
appending new columns, or alter the `RunWriter` interface signature.

Hard rules (checked in code review):

- New files live under `multi-agent/tools/eval/runner/probes/` (created
  by this worktree). `runner.go` gains **only** additive probe-call
  lines — no branching logic moves, no existing return path is
  restructured. `writer.go` gains **only** appended columns on `RunRow`
  and `CSVColumns()` (schema is append-only per §9 of
  `wt1-eval-runner-skeleton.spec.md`); no field is renamed or removed.
- The probes package imports **only** stdlib +
  `github.com/yourorg/multi-agent/internal/secretscrub`. No SQL, no HTTP,
  no `github.com/mattn/go-sqlite3` — probes write into an in-memory
  buffer that the runner drains once at the end of a run and folds into
  `RunRow`. Persistence remains WT-1-run-schema's job (via `RunWriter`).
- Zero changes to `Dispatcher`, `executor.Executor`, `humanloop`, or any
  package outside `tools/eval/runner/`. `HumanContextSelectionCount`
  reads a **counter file** that a future humanloop hook writes into the
  workspace tempdir; the probe does NOT link the humanloop package. See
  §3.4 for the file contract.
- `runner.go` orchestrator lines added or amended are enumerated in
  §5.1; any diff outside that enumeration is a code-review P0.
- Each `git commit` ends with `Co-Authored-By: Claude Opus 4.8 (1M
  context) <noreply@anthropic.com>`. **Do not** push.

### 1.1 What this worktree does NOT own

Three cross-worktree seams are called out explicitly so a reviewer can
tell "TODO here" from "TODO in the other worktree":

- **`ArtifactCorrectnessRate` full accuracy.** Depends on oracle
  producing an `artifact_correct` boolean in its JSON `metrics` block
  (§F1 oracle extension). Until that lands the probe uses the oracle
  `passed` field as a fallback and marks the metric as
  `fallback_from_passed` in the labels. See §3.5.
- **`WrongContextFailureRate` full classification.** 08 §Semantic
  capability metrics defines the collection method as "failure
  classification from logs and oracle." Full classification needs
  oracle-provided `failure_class` (§F1 oracle extension). Until that
  lands the probe uses the structural signal `passed==false AND
  selected != ground_truth` and marks the record with
  `oracle_failure_class_source=fallback_structural_only`. See §3.6.
- **`ManualSetupStepCount` / `ConfigTouchCount` sourcing.** Depends on
  the D6c deploy harness (§D6c) writing a counter file into the
  workspace. Until that lands the probe emits `nil` value + label
  `unavailable_reason=d6c_setup_harness_pending`. See §3.7.
- **D1 schema fields.** Six columns in this spec's metric set (see §6
  consumer-view audit; enumerated `HANDOFF-D1-1..6`) are NOT in the current `runs` table
  (`internal/observerstore/schema.sql` lines 194–219). This spec
  documents the additions **as a handoff to WT-1-run-schema follow-up**
  — the DDL is NOT edited here. The runner-side probe records the
  metric to the `RunRow` in-memory struct and the CSV output; the SQL
  column plumbing lands when that follow-up merges.

## 2. Package layout

```
multi-agent/tools/eval/runner/probes/
├── probes.go            Emitter type, Emit, buffered channel + single
│                        flusher goroutine, timestamp bookkeeping,
│                        secretscrub-on-label integration
├── probes_test.go       Emitter unit tests (a)-(g) coverage matrix
├── metrics.go           The 8 typed metric records + MetricKey enum;
│                        MergeIntoRow(&RunRow, drain result) helper
├── metrics_test.go      Per-metric unit test (one for each of the 8)
├── humanloop.go         Reads the workspace-tempdir counter file for
│                        HumanContextSelectionCount; never reads text.
├── humanloop_test.go    Text-not-stored guard test (d)
├── setup.go             Reads the workspace-tempdir setup counter file
│                        for ManualSetupStepCount / ConfigTouchCount
└── setup_test.go        Fallback-to-nil test (e/g)
```

Package path: `github.com/yourorg/multi-agent/tools/eval/runner/probes`.
The runner (`package main` at `tools/eval/runner`) imports this new
package.

## 3. Public API — `package probes`

### 3.1 `Emitter` (buffered, non-blocking)

```go
// Emitter buffers probe records and flushes them on Close. All Emit
// calls are non-blocking: the emitter uses a bounded channel; if the
// channel is full, Emit drops the record, increments a dropped-counter,
// and logs a single warning per drop event. This behaviour is required
// by Security §7(a) — a slow flusher must never wedge the runner.
type Emitter struct {
    // unexported: buffered channel, flusher goroutine handle, drop
    // counter, closed sentinel, mutex protecting Close idempotence.
}

// NewEmitter returns an Emitter with a bufferSize-record channel and
// starts the flusher goroutine. bufferSize <= 0 defaults to 256 (see
// §7(a) for the sizing argument).
func NewEmitter(bufferSize int, stderr io.Writer) *Emitter

// Emit records a single (metric, value, labels) tuple. Never blocks;
// never returns a non-nil error today (the signature reserves error for
// future backends). The nil-Emitter receiver is a no-op — callers can
// pass `nil` as the Emitter to disable probes without branching.
//
// Contract:
//   - value MAY be nil (metric explicitly unavailable; see §3.5, §3.7)
//   - labels values are sanitized via secretscrub.Sanitize BEFORE they
//     enter the channel (Security §7(b), §7(d))
//   - labels keys are NOT sanitized — they are compile-time literals
//     produced by this package (see §3.2 enum); a runtime-composed key
//     is an internal bug, not a data-plane concern
//   - metric name MUST be one of the eight MetricKey constants; unknown
//     names log a warning and are dropped
//   - a nil ctx is allowed (Emit does not consume ctx; the parameter is
//     kept for future backend wiring symmetry)
func (e *Emitter) Emit(ctx context.Context, metric MetricKey, value any, labels map[string]string) error

// Close stops the flusher goroutine and returns the accumulated records
// in emission order. Idempotent: subsequent calls return an empty slice
// + nil error. MUST be called before the runner assembles RunRow.
//
// Close does NOT return the flusher goroutine's own log-lines about
// dropped records; those go to the io.Writer handed to NewEmitter.
func (e *Emitter) Close() ([]Record, error)

// Record is what Close returns. Fields are read-only.
type Record struct {
    Metric          MetricKey
    Value           any               // int / int64 / float64 / bool / nil
    Labels          map[string]string // sanitized copy; never nil
    EmittedAt       time.Time         // wall-clock, for audit only
    EmittedAtMonoNs int64             // monotonic delta since Emitter start; §7(c)
}
```

### 3.2 `MetricKey` (typed enum for the 8 metrics)

```go
type MetricKey string

const (
    // E1 — Lifecycle macrobenchmark (08 §E1, line 116)
    MetricTaskSuccessRate           MetricKey = "task_success_rate"
    MetricLifecycleClosureRate      MetricKey = "lifecycle_closure_rate"
    MetricTimeToCompletion          MetricKey = "time_to_completion_ns"
    MetricHumanContextSelectionCount MetricKey = "human_context_selection_count"
    MetricWrongContextFailureRate   MetricKey = "wrong_context_failure_rate"
    MetricArtifactCorrectnessRate   MetricKey = "artifact_correctness_rate"

    // E6 — Reproducibility (08 §E6, line 237)
    MetricManualSetupStepCount      MetricKey = "manual_setup_step_count"
    MetricConfigTouchCount          MetricKey = "config_touch_count"
)

// AllMetrics returns the eight-element slice in declaration order.
// The runner uses it to iterate; changing the order is a schema
// migration for §4's CSV column layout.
func AllMetrics() []MetricKey
```

Any metric name outside this eight-item set is rejected by Emit with a
warn log + drop; test `TestEmit_RejectsUnknownMetric` covers it. This is
the compile-time safety net for the acceptance "8 metric 全有数" gate —
you cannot accidentally emit `MetricTaskSuccessRateWithTypo` and have it
silently absorb into the CSV.

### 3.3 Probe points — mapping to eight metrics

Each probe point below is invoked from `runner.go` at exactly one
enumerated site (§5.1). The value/label spec is authoritative — the
Emit call in code MUST match.

| # | Metric | Probe point | Value | Labels |
|---|---|---|---|---|
| 1 | `TaskSuccessRate` | Right after `parseOracleStdout` (runner.go step 12), before `commit_meta` collection. | `bool` (`passed`) | `oracle_exit_code=<int>`, `oracle_stdout_bytes=<int>` |
| 2 | `LifecycleClosureRate` | Same site as #1; value read from oracle metrics field `lifecycle_closed` (bool). Fallback to `passed` when oracle omits the field, with label `fallback_from_passed=true`. | `bool` | `source=oracle_metrics` \| `source=fallback_passed` |
| 3 | `TimeToCompletion` | Runner exit path, right before RunRow assembly. Value is monotonic delta captured at probe-emitter start vs runner-end (§7(c)). | `int64` (nanoseconds) | `wall_start_unix=<int>`, `wall_end_unix=<int>` |
| 4 | `HumanContextSelectionCount` | Runner exit path, right after `parseOracleStdout`. Value is the count read from the workspace counter file (§3.4). | `int` | `source=humanloop_counter_file` \| `source=absent` |
| 5 | `WrongContextFailureRate` | Runner exit path, right after commit_meta collection. Value is `bool` per §3.6 (structural signal `passed==false AND selected != ground_truth`, optionally intersected with oracle-provided `failure_class` when present). Fallback to `nil` when the selected or ground-truth field is unavailable (with `unavailable_reason=...` label). | `bool` OR `nil` | `selected=<str>`, `ground_truth=<str>`, `oracle_failure_class=<str>` (when present), `oracle_failure_class_source=oracle_metrics\|fallback_structural_only`, `unavailable_reason=<str>` (when nil) |
| 6 | `ArtifactCorrectnessRate` | Same site as #1; value read from oracle metrics `artifact_correct` (bool). Fallback to `passed` with label `fallback_from_passed=true`. See §3.5. | `bool` | `source=oracle_metrics` \| `source=fallback_passed` |
| 7 | `ManualSetupStepCount` | Runner setup-stage exit (right after `SetupWorkspace`). Value read from setup counter file (§3.7); `nil` when unavailable. | `int` OR `nil` | `source=setup_counter_file` \| `unavailable_reason=d6c_setup_harness_pending` |
| 8 | `ConfigTouchCount` | Same site as #7. | `int` OR `nil` | same as #7 |

Every value is either a scalar (int / int64 / bool / float) or `nil` —
never a string, never a slice. This constraint keeps the CSV encoder in
§4 trivial (`nilOrFormat(v)` — one function).

### 3.4 `HumanContextSelectionCount` — counter file contract

The probe reads a single file in the workspace tempdir:

```
${workspace}/.probes/humanloop.count
```

The file's entire body is one non-negative decimal integer (regex
`^[0-9]+\n?$`), representing the number of `humanloop.ask_user` /
`humanloop.request_permission` invocations during the run. **The file
MUST NOT contain the human's input text** (Security §7(d)). A future
humanloop-instrumentation worktree writes this file; today the file is
absent for every workload, and the probe records `value=0` +
`source=absent` (the observed count is legitimately zero if no probe
writer has landed yet, and marking that explicitly beats faking a nil).

Reader implementation:

```go
// countFile parses ${workspace}/.probes/humanloop.count. Absent file =>
// (0, source="absent"). Present but malformed => (0, source="malformed",
// plus a stderr warn). Present and well-formed => (n, source="humanloop_counter_file").
//
// The reader IGNORES any bytes past the first line, and hard-caps read
// at 4 KiB so a runaway writer cannot exhaust runner memory. If the cap
// trips, source="malformed" and n=0.
func countFile(path string, stderr io.Writer) (int, string)
```

Failure modes (all non-blocking):
- file absent — `n=0`, `source=absent`, no log
- file malformed — `n=0`, `source=malformed`, one stderr warn line
- file > 4 KiB — `n=0`, `source=malformed`, one stderr warn line

### 3.5 `ArtifactCorrectnessRate` — oracle metrics extension

`parseOracleStdout` today extracts `passed`, `details`, `metrics` (raw
JSON). This spec adds a second, best-effort extraction step in the
probes package: parse `metrics` as `map[string]any`, read the boolean
`artifact_correct` key. When present, that boolean IS the metric value.
When absent (today's baseline oracle for every workload), fall back to
`passed` with label `fallback_from_passed=true`. Same shape for
`LifecycleClosureRate` and its `lifecycle_closed` key.

Rationale: today's oracle contract (`wt1-eval-runner-skeleton.spec.md`
§1.3) does not mandate these boolean fields. The §F1 oracle extension
worktree will add them; until then the fallback preserves a non-nil
value while making the fallback source visible in the CSV (so an
analysis script can filter fallback vs real).

### 3.6 `WrongContextFailureRate` — selected vs ground-truth + oracle classification

08 §Semantic capability metrics (line 48) defines the metric as
"failures due to missing file/tool/OS/credential/network in chosen
context" and the collection method as "failure classification from logs
and oracle." Fully classifying the failure cause requires the oracle to
tag the failure (§F1 oracle extension work); this spec landing today
provides the selected-vs-ground-truth structural signal and reads a
best-effort oracle-side classification when available.

Value is `true` iff ALL of:
1. `passed == false` (an actual failure — a passing run cannot have
   `wrong_context_failure=true` even if selected != ground_truth,
   because "wrong context but still succeeded" is not a
   wrong-context failure per 08 §definition)
2. `selected_context != ground_truth_context`
3. AND, when the oracle provides a `failure_class` field in its JSON
   `metrics` block, `failure_class` is one of the wrong-context
   classes: `missing_file` | `missing_tool` | `wrong_os` |
   `missing_credential` | `network_unreachable`. When the oracle omits
   `failure_class` (today's baseline for every workload; §F1 has not
   yet extended the oracle contract), condition #3 is treated as
   satisfied — the structural signal from #1 + #2 is the fallback.

Labels record which conditions fired:
- `oracle_failure_class=<str>` when the oracle provided the field
- `oracle_failure_class_source=oracle_metrics` OR
  `oracle_failure_class_source=fallback_structural_only` (when
  condition #3 fell back)

Sources for the two context fields:

- `selected_context`: read from an optional file
  `${workspace}/.probes/selected_context.txt` (single-line string,
  ≤512 bytes). Writer is the routing-trace consumer (WT-1-routing-trace
  or the follow-up cmd wiring). Absent → `nil` value + label
  `unavailable_reason=no_selected_context_file`.
- `ground_truth_context`: read from the workload's `labels.json` under
  `multi-agent/tests/eval/labels/workloads/<workload_id>.labels.json`
  (§F4 file). The probe reads only the `ground_truth_context` key; any
  other keys are ignored. Absent → `nil` + label
  `unavailable_reason=no_ground_truth_labels`.

Both files' string values are sanitized via `secretscrub.Sanitize`
before being placed in a label — a routing writer that accidentally
emitted a token into the selected-context string cannot land it in the
CSV.

### 3.7 `ManualSetupStepCount` / `ConfigTouchCount` — D6c dependency

The probe reads two JSON keys from a single file:

```
${workspace}/.probes/setup.json
```

Schema (subset):
```json
{
  "manual_setup_step_count": 3,
  "config_touch_count": 5
}
```

Absent file OR missing keys → both metrics emitted with `value=nil` +
`unavailable_reason=d6c_setup_harness_pending`. When the D6c deploy
worktree lands, its harness writes this file at the start of the run
before oracle invocation. Values MUST be non-negative integers; a
non-integer or negative value yields `value=nil` +
`unavailable_reason=malformed_setup_file` and a single stderr warn.

Read is hard-capped at 4 KiB (same argument as §3.4).

## 4. CSV column additions (`writer.go`)

Appended to the end of `CSVColumns()` and `RunRow`, preserving the
existing 22-column layout. The eight metrics get either one numeric
column (scalars) or one string column carrying "" for nil (see below).

| Position | Column name | Type | Nil encoding |
|---|---|---|---|
| 23 | `probe_task_success_rate` | bool | `""` |
| 24 | `probe_lifecycle_closure_rate` | bool | `""` |
| 25 | `probe_time_to_completion_ns` | int64 | `""` |
| 26 | `probe_human_context_selection_count` | int | `""` |
| 27 | `probe_wrong_context_failure_rate` | bool | `""` |
| 28 | `probe_artifact_correctness_rate` | bool | `""` |
| 29 | `probe_manual_setup_step_count` | int | `""` |
| 30 | `probe_config_touch_count` | int | `""` |
| 31 | `probe_notes_json` | string (JSON obj) | `"{}"` |

`probe_notes_json` is a small JSON object carrying the labels that
qualify the metric values (sources, fallbacks, unavailable reasons):

```json
{
  "task_success_rate":            {"oracle_exit_code": 0, "oracle_stdout_bytes": 123},
  "lifecycle_closure_rate":       {"source": "fallback_passed"},
  "wrong_context_failure_rate":   {"source": "unavailable", "unavailable_reason": "no_selected_context_file"},
  "manual_setup_step_count":      {"source": "unavailable", "unavailable_reason": "d6c_setup_harness_pending"},
  "config_touch_count":           {"source": "unavailable", "unavailable_reason": "d6c_setup_harness_pending"}
}
```

Only metrics with non-empty labels appear in the JSON — a metric with an
integer value and no interesting labels is silent to keep the column
small. Downstream consumers MUST tolerate missing keys.

Serialisation rules:
- `bool` → `"true"` / `"false"`
- `int` / `int64` → base-10 decimal
- `nil` → empty string (matches CSV standard for missing numeric)
- `probe_notes_json` values are sanitized on the emit path (§3.1); the
  serializer only marshals them, does not re-sanitize.

`RunRow` gains eight new fields matching the columns above, plus a
`ProbeNotesJSON string` field.

### 4.1 Ordering constraint

`CSVColumns()` MUST remain append-only. This spec appends exactly nine
columns at the tail. Tests in `writer_test.go` that count columns are
updated in lockstep. A migration that reorders these columns is a
follow-up worktree's decision, not this one's.

## 5. Runner integration

### 5.1 `runner.go` diff surface (authoritative)

Only these edits are permitted. Any diff outside this list is a
code-review P0. Line numbers reference `runner.go` at base commit
`d053897`. "Currently line ~N" means the edit is inserted immediately
after the referenced line's statement completes.

**Edit 0 (imports block, currently lines 3–23).** Add exactly one line
to the existing `import (...)` block:
```go
"github.com/yourorg/multi-agent/tools/eval/runner/probes"
```

**Edit 1 (after `startedAt := time.Now()` at line 83).** Construct the
emitter and register the safety-net close:
```go
emitter := probes.NewEmitter(0 /*default buffer*/, opts.Stderr)
defer emitter.Close() // safety-net for preflight-return paths; primary Close is in Edit 6
```

**Edit 2 (after successful `SetupWorkspace` at line 134).** Emit the
two D6c-dependent setup metrics right after the workspace exists:
```go
probes.EmitSetupMetrics(ctx, emitter, ws.Root, opts.Stderr)
```
Helper defined in `probes/setup.go`.

**Edit 3 (after `parseOracleStdout` at line 203).** Emit the four
oracle-derived metrics + humanloop counter. The runner's `oracleOutput`
struct is unexported (`package main`), so we lift the fields into
`probes.OracleOutput` (defined in the probes package) at the call
site — the probes package cannot back-import the runner's `main`
package:
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

**Edit 4 (after `collectGitEmails` at line 211).** Emit
`WrongContextFailureRate`. Reuses the `oracleOutForProbes` variable
constructed at Edit 3:
```go
probes.EmitWrongContext(ctx, emitter, ws.Root,
    workloadRoot /* for labels.json lookup */,
    spec.ID, oracleOutForProbes, opts.Stderr)
```
(The struct carries both `Passed` and the parsed metrics — see §3.6
oracle `failure_class`.)

**Edit 5 (immediately before `finishedAt := time.Now()` at line 217).**
Emit `TimeToCompletion`. `startedAt` is the same variable captured by
Edit 1's sibling statement — its `time.Time` carries a monotonic
reading, per §7(c):
```go
probes.EmitTimeToCompletion(ctx, emitter, startedAt)
```

**Edit 6 (between the row-assembly block ending at line 243 and the
`opts.Writer.Insert(ctx, row)` call at line 246).** `row` exists by
line 220 and is fully populated by line 243. Drain the emitter and
merge probe records into the just-assembled row:
```go
records, _ := emitter.Close()
probes.MergeIntoRow(&row, records)
```

(Placed AFTER `row` is assembled so `MergeIntoRow` writes into an
existing struct; placed BEFORE `Writer.Insert` so persisted rows carry
the probe fields. Wall-clock `duration_ms` — computed at Edit 5's line
217 — does NOT include the drain time; that is a small,
one-shot in-memory copy — bounded by the 256-record buffer per §7(a) —
and folding it into `duration_ms` would misattribute "post-run probe
merge overhead" to the workload's own duration.)

The `emitter.Close()` in step 6 supersedes the deferred `Close()` from
step 1; the deferred one exists only so an early-return path
(`preflight`) after `NewEmitter` still stops the flusher goroutine.
`Close` is documented idempotent (§3.1) — the second call is a no-op.

No existing return path is changed. Pre-flight errors that return
before step 6 still write no CSV row (per skeleton spec §4 step 17
gate); the emitter's records are discarded on that path, which is
correct — a preflight-failed run has no oracle to measure.

### 5.2 Two-writer probe merging

`MergeIntoRow(&RunRow, []Record)` iterates records and populates the
new `RunRow` probe fields. If a metric has multiple records (a probe
was emitted twice for the same key — code bug, not data-plane concern),
the LAST record wins and a single stderr warn line is emitted. Test
`TestMergeIntoRow_LastWinsOnDuplicate` covers this.

## 6. Consumer-view reverse audit (§7(g))

Assumption: WT-1-run-schema's DDL (`runs` table, DDL at
`internal/observerstore/schema.sql` lines 194–219) is the persistence
target for these eight metrics via the WT-2-metric-extract follow-up.

| Metric | Current `runs` column | Gap | Handoff |
|---|---|---|---|
| TaskSuccessRate | `success_oracle_result` (`pass`/`fail`/`timeout`) | none — the bool maps to `pass` / non-`pass` | — |
| LifecycleClosureRate | **missing** | need nullable boolean column | HANDOFF-D1-1: add `lifecycle_closure_rate INTEGER NULL` (SQLite: 0/1/NULL). NULL preserves this spec's nil-when-unavailable discipline; downstream metric-extract MUST treat NULL as "unmeasured", NOT as `false`. |
| TimeToCompletion | `end_time - start_time` (RFC3339Nano diff) | wall-clock diff is not monotonic; §7(c) requires monotonic | HANDOFF-D1-2: add `time_to_completion_ns INTEGER NOT NULL DEFAULT 0` (**authoritative**, wall-clock columns remain for audit only — same pattern as `route_reasons.decision_duration_ns`). NOT NULL is safe here — a per-run monotonic delta is always definable once the run starts. |
| HumanContextSelectionCount | `human_intervention_count INTEGER` | column exists; name maps 1:1 | — |
| WrongContextFailureRate | derivable from `selected_context != ground_truth_context AND success_oracle_result != 'pass'` | derivable in-DB; probe-side compute is a **denormalisation** for CSV convenience | HANDOFF-D1-3: **optional** column `wrong_context_failure_rate INTEGER NULL` (nullable — mirrors the nil-when-either-context-file-absent discipline in §3.6; denormalises the SELECT for CSV convenience) |
| ArtifactCorrectnessRate | **missing** | need nullable boolean; today derivable from `passed` fallback only | HANDOFF-D1-4: add `artifact_correctness_rate INTEGER NULL`. NULL when oracle omits `artifact_correct` AND `passed` fallback disabled by future spec revision; today the probe always emits a non-nil bool (see §3.5). |
| ManualSetupStepCount | **missing** | | HANDOFF-D1-5: add `manual_setup_step_count INTEGER NULL`. NULL preserves "D6c harness has not landed → we do NOT know" — a stored `0` would silently claim "zero manual steps required" (§7(e) discipline). |
| ConfigTouchCount | **missing** | | HANDOFF-D1-6: add `config_touch_count INTEGER NULL`. Same NULL-vs-0 argument as HANDOFF-D1-5. |

Handoff notes to WT-1-run-schema follow-up:
- All six HANDOFF-D1-* additions are backwards-compatible: five are
  `INTEGER NULL` (SQLite `ALTER TABLE ADD COLUMN` populates existing
  rows as NULL), and HANDOFF-D1-2 is `INTEGER NOT NULL DEFAULT 0`
  (SQLite backfills existing rows with the default). No existing-row
  backfill script required.
- NULL semantics: metric-extract MUST distinguish NULL ("not
  measured") from `0` ("measured, none"). A `COALESCE(col, 0)` at
  the extraction layer would silently fold "D6c harness not landed"
  runs into "no manual setup needed" — that would exactly reproduce
  the §7(e) failure mode the probe is designed to prevent.
- The WT-2-metric-extract worktree consumes both the CSV columns (§4
  above) and the eventual DDL columns; until the DDL additions land,
  metric-extract reads from CSV via `evalrun-export`.
- A migration file — NOT this worktree's job — carries the ALTER TABLE
  statements. This spec documents the target shape only.

## 7. Security mitigations

All eight items are testable; each has at least one test row in the
plan's matrix. A run that violates any of (a)–(h) is a P0 bug.

### (a) `Emit` never blocks the runner

**Threat.** A future backend for `Emit` (say, an in-process SQLite
writer) does 200 ms of blocking IO per call. The runner's hot path
issues 8 Emit calls per run; sequential blocking would add 1.6 s of
latency to every workload. Worse, an emitter that wedges (deadlocked
mutex, saturated disk, network partition) blocks the runner
indefinitely and every experiment stalls.

**Mitigation.**

- `Emit` writes to a bounded `chan Record` and returns immediately.
- A single flusher goroutine drains the channel into an in-memory
  `[]Record` slice; the flusher never performs IO except stderr warn
  lines on drop events (bounded, one per drop, rate-limited to 1/second
  via a `time.Ticker` — the flusher owns the ticker so Emit remains
  non-blocking).
- Default buffer size is 256 records. Argument: the runner emits at
  most 8 records per run today, plus one order of magnitude headroom
  for future callers (label subdivisions, per-workload emissions);
  256 is 2^8 for cache-line friendliness (Go's chan implementation
  aligns to 2^N buffer sizes internally) and still bounded so a
  runaway loop cannot exhaust memory.
- Channel-full behaviour: **select with default** — on full channel,
  drop the record and bump `droppedTotal` (unexported atomic.Int64
  counter). No blocking send. Test
  `TestEmit_NonBlocking_WhenChannelFull` sends 300 records into a
  size-8 emitter with a slow flusher and asserts every `Emit` returns
  in <1 ms.
- `Close` sends a sentinel on a separate `done` channel, joins the
  flusher, and returns the drained slice. `Close` on an already-closed
  emitter is a no-op (guarded by `sync.Once`).

### (b) Labels pass through `secretscrub`

**Threat.** A future caller emits a label like
`user_input="my openai key is sk-abcdef..."` or config diff labels
carrying `.env` contents. Without sanitization, that lands in the CSV +
observer DB and stays there forever.

**Mitigation.**

- `Emit` walks `labels`, applies `secretscrub.Sanitize` to every
  **value** (keys are compile-time literals inside this package —
  a runtime-composed key is a package bug, not a data-plane exposure).
- Sanitize is called BEFORE the channel send so a slow flusher cannot
  race with the caller's mutation of the label map (defensive copy is
  free once sanitize has walked it).
- Test `TestEmit_SanitizesLabelValues` seeds a label containing a
  `sk-ABCDEFGHIJKLMNOP` shape and asserts `[REDACTED]` in the drained
  record. Also asserts `secretscrub.RedactedTotal` bumped by 1.
- The two probe-side counter files (`§3.4`, `§3.7`) do NOT contribute
  free-form text to labels — only integer values and enum-shaped source
  strings — so scanning them cannot leak. Sanitize on those labels is
  belt-and-braces.

### (c) `TimeToCompletion` is monotonic

**Threat.** Wall-clock diffs jump under NTP resync / suspend-resume —
`TimeToCompletion` can become negative or wildly inflated. This
poisons E1 macrobenchmark plots.

**Mitigation.**

- The emitter records a `start time.Time` in `NewEmitter`. `Go`'s
  `time.Time` carries a monotonic reading; `time.Since(start)` on a
  Go-1.9+ runtime returns the monotonic delta.
- `EmitTimeToCompletion` reads `time.Since(runnerStartedAt)` where
  `runnerStartedAt` is the ORIGINAL `startedAt := time.Now()` in
  `runner.go` (currently line 83). Since `startedAt` is captured by a
  `time.Now()` inside the same process, it carries a monotonic reading
  — the delta is monotonic even if the wall clock jumped.
- Wall-clock `started_at_unix` and `finished_at_unix` remain in
  `RunRow` unchanged; they land in the probe labels as
  `wall_start_unix` / `wall_end_unix` for audit only.
- Test `TestEmitTimeToCompletion_Monotonic` seeds a start time
  five seconds ago, calls the emit, and asserts value ≥ 5e9 ns and
  value < 1e10 ns (5s ± 100%). Also asserts value is `int64`, not
  `time.Duration` (JSON encoders differ on Duration).
- Explicit non-goal: cross-machine time comparison. This metric is
  per-run only; multi-machine wall-clock comparisons remain the
  operator's problem.

### (d) `HumanContextSelectionCount` does NOT store user input text

**Threat (highest severity — kickoff `安全` section).** A future
humanloop-instrumentation worktree writes the counter file; a lazy
implementation would also write `${workspace}/.probes/humanloop.log`
with question + user answer pairs. If the probe were to
opportunistically pick that up, user-entered secrets ("my API key is
sk-...") get persisted forever.

**Mitigation.**

- The probe reads EXACTLY ONE file: `${workspace}/.probes/humanloop.count`.
  The parser regex is `^[0-9]+\n?$`. Any non-decimal-digit content is
  malformed (§3.4 failure modes).
- The probe NEVER reads any other file in `.probes/` — even if a
  writer accidentally puts `humanloop.log` alongside the counter, the
  runner ignores it.
- Test `TestHumanCount_IgnoresSiblingLogFile` creates
  `.probes/humanloop.count` = "3" AND `.probes/humanloop.log` = "my
  secret sk-DEADBEEF12345678". Asserts (a) the drained record's value
  is 3, (b) the drained record's labels contain no substring from the
  log content, (c) `secretscrub.RedactedTotal` did not bump (nothing to
  sanitize because nothing was read).
- The counter-file writer's contract (documented for the future
  humanloop worktree) is "integer only, never text". This is a written
  handoff; enforcement is one-sided — the probe reader refuses to look
  at anything else.
- File size hard cap: 4 KiB. A 4 KiB file of digits is still an integer
  — the malformed path is triggered by any non-digit byte, not by size
  alone. But the size cap prevents a runaway writer from wedging the
  probe on a giant file (§3.4).

### (e) Fallback discipline for D6c-dependent metrics

**Threat.** ManualSetupStepCount / ConfigTouchCount depend on the D6c
setup harness, which has not landed. A naive implementation would emit
`0` unconditionally, making downstream analysis think "every run
required zero manual setup", which is a stronger claim than the data
supports.

**Mitigation.**

- On absent `${workspace}/.probes/setup.json`, both metrics are
  emitted with `value=nil` + label
  `unavailable_reason=d6c_setup_harness_pending`.
- The CSV serializer encodes `nil` as `""` (empty column), which
  downstream Pandas / CSV consumers correctly treat as NaN / missing.
- The label goes into `probe_notes_json` so a manual review of the
  CSV can see why the value is empty.
- Test `TestEmitSetupMetrics_AbsentFile` covers absent + expects nil
  values + expects the label. Test
  `TestEmitSetupMetrics_MalformedFile` covers malformed + expects nil
  values + expects `unavailable_reason=malformed_setup_file`.

### (f) No SQL, all parameterised persistence downstream

**Threat.** This worktree does NOT write to any DB directly. But
`RunRow` extensions (§4) are consumed by WT-1-run-schema's
`SQLWriter.Insert`, which uses `?` placeholders. This worktree MUST NOT
introduce any code path that concatenates user-controlled strings into
SQL.

**Mitigation.**

- No `database/sql` import in `probes/`. `grep -R "database/sql"
  tools/eval/runner/probes/` returns nothing (asserted by test
  `TestProbes_NoDatabaseSQLImport`, which parses the package's
  `import` block).
- Test `TestProbes_NoHTTPClient` — asserts no `net/http` import
  either. Probes are file-IO-only.

### (g) Missing D1 schema field is a handoff, not a silent failure

**Threat.** Six of the eight metrics need new columns in the `runs`
table (§6). If the runner emits them into `RunRow` fields the writer
doesn't know about, they silently disappear from the DB while
still landing in the CSV — an inconsistency between two persistence
paths.

**Mitigation.**

- The runner does NOT know about the eventual DDL. `Writer.Insert`
  today (WT-1-run-schema's `evalrun.SQLWriter`) has a fixed 24-field
  `Schema`. The runner passes eight ADDITIONAL fields into the CSV via
  `RunRow`; those fields do NOT flow into `SQLWriter.Insert` today
  because `SQLWriter.Insert` only reads the fields it knows about
  (evalrun's `Schema` struct is a separate type, not `RunRow`).
- This spec's §6 documents the DDL additions as a written handoff.
  The runner's `RunRow` gains the eight new fields, and CSV export
  carries them today. When WT-1-run-schema-follow-up lands the DDL
  ALTER, WT-2-metric-extract's copy of `RunRow → evalrun.Schema` will
  extend to pass the eight new fields into the writer.
- Test `TestRunRow_ProbeFields_PresentInCSV` — asserts the eight
  columns appear in `CSVColumns()` and a smoke run populates them (or
  `probe_notes_json` records why they're empty).

### (h) CI perf assertions are conditional

**Threat.** A perf-sensitive assertion like "Emit returns in <1 ms"
runs 3x slower on a shared CI runner; the test flakes.

**Mitigation.**

- Any Emit-latency assertion is gated on `os.Getenv("CI") == ""` —
  local dev runs assert, CI runs skip with
  `t.Skip("perf assertion skipped on CI")`.
- The functional non-blocking assertion (channel full → drop, not
  hang) uses `time.AfterFunc(500ms, ...)` as a deadlock detector,
  which fires only on a hard hang. That is safe to run on CI because
  500ms is far above scheduler jitter.
- Follows the `wt1-fault-injection.spec.md` precedent (perf gate
  skipped on CI; see commit `e7e8897` "skip 100ns perf gate on CI").

## 8. Acceptance

End-to-end smoke against workload `cross-device-code-mod` (its
`fixtures/mock_workspace/{patch.diff,test.log}` is the maintainer
self-check artifact; oracle returns pass=true):

```bash
cd multi-agent
go build -o /tmp/eval-runner ./tools/eval/runner
/tmp/eval-runner run \
    --workload cross-device-code-mod \
    --workload-dir tests/eval/workloads \
    --stub-listen 127.0.0.1:18080 \
    --out /tmp/run.csv

# 1) All eight probe columns exist in the header.
head -1 /tmp/run.csv | tr , '\n' | grep -c '^probe_' # expected: 9  (8 metrics + notes)

# 2) Data row has the two oracle-derived metrics populated.
awk -F, 'NR==2 {print $23, $24, $28}' /tmp/run.csv     # expected: true true true

# 3) Data row has the two D6c-dependent metrics empty AND their
#    probe_notes_json entries say d6c_setup_harness_pending.
awk -F, 'NR==2 {print $29, $30}' /tmp/run.csv          # expected: <empty> <empty>
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

Plus:

```bash
go test ./tools/eval/runner/probes/... ./tools/eval/runner/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l tools/eval/runner/probes tools/eval/runner/runner.go tools/eval/runner/writer.go
```

All must return clean. Any `gofmt -l` output is a P0.

## 9. Open seams for future worktrees

| Worktree | Seam this WT leaves |
|---|---|
| WT-1-run-schema follow-up (DDL ALTER) | HANDOFF-D1-{1..6}: six new columns on `runs` table per §6 table |
| WT-2-metric-extract | `RunRow` probe fields + `probe_notes_json` — the extractor reads them from CSV today, wires into evalrun.Schema once the DDL lands |
| §D6c deploy harness | `${workspace}/.probes/setup.json` writer contract per §3.7 |
| §F1 oracle extension | oracle JSON `metrics.lifecycle_closed` + `metrics.artifact_correct` + `metrics.failure_class` (§3.6) — probe reads all three automatically when present (fallbacks removed once workloads reliably emit) |
| Future humanloop-instrumentation worktree | `${workspace}/.probes/humanloop.count` writer contract per §3.4 (integer-only, ≤4 KiB) |
| Future routing-trace wiring | `${workspace}/.probes/selected_context.txt` writer per §3.6 (≤512 bytes) |
