# WT-2-overhead-probes — Spec

> Scope: instrument three in-process latency spans (driver planning, agentserver
> task dispatch, observer write) and ship three microbench binaries + one scale
> sweep harness that together produce p50/p95 for seven overhead metrics feeding
> paper §E5 Figure 4 (Overhead breakdown). Adds one observer table
> (`probe_events`) and new `tools/eval/microbench/` + `tools/eval/scale_sweep/`
> trees. No changes to routing/dispatch code (RoutingLatencyP50P95 is read from
> the existing `route_reasons` table that WT-1-routing-trace already populates).
>
> Out of scope: `internal/dispatch/*` (owned by WT-1-routing-trace); ablation
> flag *registration* (owned by WT-1-ablation-registry — this WT only *reads*
> `NoObserver` via the shared registry); Figure 4 rendering (owned by
> WT-2-metric-extract §D2 CSV → paper pipeline).

## 1. Background

Paper §E5 (`/root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md`
lines 200–224) commits seven overhead metrics:

| Metric | Data source |
|---|---|
| `DriverPlanningOverhead` | in-process span around `planner.Plan()` inside `orchestrator.runFanout` |
| `TaskDispatchLatency` | agentserver-side span from "task envelope received" to "executor.Run entered" |
| `TunnelOverhead` | microbench: ping + small transfer round-trip |
| `ArtifactTransferThroughput` | same microbench: variable-size payload bytes/sec |
| `ObserverOverhead` | microbench: same workload with `NoObserver` off vs on |
| `ModelProxyOverhead` | microbench: same prompt via proxy vs direct provider endpoint |
| `RoutingLatencyP50P95` | SELECT `decision_duration_ns` from existing `route_reasons` (Phase 1 WT-1-routing-trace) |

The scale sweep runs
`contexts ∈ {1,2,4,8,16} × tools/context ∈ {10,50,100} × artifact_size ∈ {1 KiB, 1 MiB, 100 MiB}`
(§E5 "Scale points", lines 213–217) and reports p50/p95 for all seven above.

Overlap with sibling worktrees is bounded:

* **WT-1-routing-trace** owns `internal/dispatch/*` and the `route_reasons`
  table. This WT reads that table via a documented SELECT (§4.4) and MUST NOT
  touch `internal/dispatch/`.
* **WT-2-credential-workload** (§D3/C3) also measures `ModelProxyOverhead`, but
  as a **workload-integrated** number (real credential-bound workload, path (a)
  vs (b) of `.codex/config.toml`). This WT measures the **naked microbench**
  number (same fixed prompt, proxy vs direct, no workload). Both feed
  §E5 but from different angles; the paper cites both (workload for realism,
  microbench for headroom). CSV column naming (§4.6) disambiguates the two:
  `model_proxy_overhead_bench_ns` here vs `model_proxy_overhead_workload_ns` in
  the credential worktree.
* **WT-2-metric-extract** (§D2) reads `probe_events` + `route_reasons` and
  emits Figure 4's CSV. This spec's §4.5 "Consumer-view SELECT" is the
  canonical query template metric-extract MUST use so field naming does not
  drift.

## 2. File domain

```
multi-agent/
  tools/eval/microbench/
    common/
      warmup.go           # shared warm-up + sampler + p50/p95 helpers
      warmup_test.go
      csvsink.go          # formula-injection-safe CSV writer (see §6 (f))
      csvsink_test.go
      idvalid.go          # conversation_id regex validation (§6 (c))
      idvalid_test.go
      clock.go            # monotonic-clock helpers + tests
      clock_test.go
    tunnel_bench/
      main.go             # TunnelOverhead + ArtifactTransferThroughput
      main_test.go
    observer_bench/
      main.go             # ObserverOverhead (NoObserver on/off cell)
      main_test.go
    proxy_bench/
      main.go             # ModelProxyOverhead (proxy vs direct)
      main_test.go
  tools/eval/scale_sweep/
    sweep/
      main.go             # CLI over the cross-product; emits Figure-4 CSV
      main_test.go
    plan.go               # contexts × tools/context × artifact_size expansion
    plan_test.go
  internal/orchestrator/
    planning_timing.go        # NEW file, driver DriverPlanningOverhead span
    planning_timing_test.go
  internal/executor/
    task_dispatch_timing.go   # NEW file, agentserver TaskDispatchLatency span
    task_dispatch_timing_test.go
  internal/observerstore/
    probe_events_writer.go        # NEW file, ObserverOverhead span + writer
    probe_events_writer_test.go
    schema.sql                    # AMENDED (append §4.2 DDL only)
  docs/specs/
    wt2-overhead-probes.spec.md   # this file
    wt2-overhead-probes.plan.md   # produced in stage 2
```

**Files this WT must NOT touch**:

* Anything under `internal/dispatch/` (WT-1-routing-trace territory; `git
  diff origin/paper/v3-integration -- internal/dispatch/` MUST be empty).
* Existing `.go` files in `internal/orchestrator/`, `internal/executor/`,
  `internal/observerstore/` — each timestamp injection lands in a **new** file
  named `*_timing.go` alongside its host package. Existing function signatures
  are unchanged; injection points call into the new-file helpers only.

**Rationale for the three injection files' package siting**:

* `internal/orchestrator/planning_timing.go` — `orchestrator.runFanout` owns
  the planning call site (fanout.go:230 `planWithProgress`). Placing the
  helper in-package lets it wrap `o.planner.Plan(...)` without exporting
  additional planner internals.
* `internal/executor/task_dispatch_timing.go` — agentserver's task-dispatch
  ingress in this codebase is `executor.Executor.Run` (slave-agent's dispatch
  entry). This WT installs a `MeasureDispatch` wrapper the slave-agent cmd can
  layer over its executor map at boot (cmd-side wiring is a documented
  follow-up, mirroring the WT-1-routing-trace `SetWriter` pattern; see §3.2
  "wiring boundary"). The helper file adds no imports to `executor.go`.
* `internal/observerstore/probe_events_writer.go` — the observer-side probe
  (`ObserverOverhead` span) measures the wall-time cost of ONE `WriteEvent`
  call under the `NoObserver`-off condition; the microbench `observer_bench`
  drives it directly against a temp SQLite file.

## 3. Timestamp injection contract

### 3.1 Universal rules (mirror WT-1-routing-trace §6 (c) / (f))

1. **Monotonic-only.** Every `span_start_at` / `span_end_at` is taken via a
   local call to `time.Now()` inside the process that owns the span. The
   package MUST NOT expose a constructor that accepts a caller-supplied
   `time.Time`. The duration written to `probe_events.duration_ns` is
   `end.Sub(start).Nanoseconds()` while both values still carry Go's monotonic
   reading, exactly as WT-1-routing-trace does for `decision_duration_ns`.
2. **RFC3339 strings are audit-only.** `span_start_at` and `span_end_at`
   columns are RFC3339Nano for human debugging; `duration_ns` is the
   authoritative latency value consumers read.
3. **`conversation_id` validated at boundary.** Any injection helper that
   accepts a `conversationID` argument MUST call
   `microbench_common.ValidConversationID(id)` (regex + length; §6 (c)) before
   it emits a span. Invalid IDs → no row written + one `expvar`
   counter (`probe_id_rejected_total`) bumped + one `log.Printf` line. Never
   panic; this is a probe, not business logic.
4. **Optional cross-machine clock skew field.** `probe_events` carries an
   OPTIONAL `wallclock_delta_ms INTEGER NOT NULL DEFAULT 0` column (§4.2). When
   a span crosses machines (e.g., a future agentserver-remote injection),
   the boundary helper stamps this with `abs(remote_wall - local_wall)` at
   handshake time. In this WT all three injection points are same-process, so
   the field stays `0`. The column is present now so metric-extract does not
   need a schema-change follow-up when remote spans arrive.

### 3.2 Wiring boundary — same shape as WT-1-routing-trace

Following the pattern established by WT-1-routing-trace §2.2 / §6 (d):

* Each injection helper (`planning_timing.go`, `task_dispatch_timing.go`,
  `probe_events_writer.go`) exposes a `SetProbeWriter(ProbeWriter)` +
  `probeWriter()` pair backed by an `atomic.Value` holding a fixed
  `probeWriterBox{ w ProbeWriter }` wrapper — this dodges `atomic.Value`'s
  same-concrete-type invariant when the noop is swapped for a real writer.
  Default writer is `noopProbeWriter{}` (returns nil).
* Each package exports `IsNoopProbeWriter() bool` so the cmd-side wiring can
  `log.Fatal` when it forgot to install a real writer (again, mirrors WT-1's
  `dispatch.IsNoopWriter()` guard).
* **cmd-side wiring is explicitly out of scope for this WT** (same rationale
  WT-1 used for `cmd/slave-agent/main.go`). This WT ships the interfaces, the
  in-process SQL writer, the schema migration, and the injection helpers.
  Bridging to `cmd/driver-agent`, `cmd/slave-agent`, and `cmd/observer-server`
  lands in a follow-up wiring PR. Until then:
  * The three microbench binaries drive the writers directly against a temp
    SQLite file, so their p50/p95 outputs are fully covered without wiring.
  * The scale sweep harness runs against microbench binaries + a
    metric-extract-shaped SELECT against `route_reasons`, so it also does not
    need cmd wiring.
  * The three injection files land dark in production; `IsNoopProbeWriter()`
    stays `true` until the follow-up. This is acceptable because the
    microbench numbers (which run in a controlled harness where wiring IS
    installed inside the bench `main.go`) are the paper-visible values;
    production spans just add audit coverage over time.

### 3.3 Three injection sites — precise contract

**A. `internal/orchestrator/planning_timing.go` (DriverPlanningOverhead)**

Exports `MeasurePlanning[T any](ctx, convID string, fn func() (T, error)) (T, error)`
that opens a span via the shared `observerstore.ProbeEventWriter` bound
in this file (`planningWriter observerstore.ProbeEventWriter` behind an
`atomic.Value`; default `noopProbeWriter{}`) — `StartSpan(KindDriverPlanning,
convID)` runs BEFORE `fn`, then `defer h.End(ctx)` covers every return
path including panic-recovery. The helper does NOT touch `time.Now()`
directly (the writer side owns the clock).

Boundary contract: `MeasurePlanning`'s signature is
`(ctx context.Context, convID string, fn func() (T, error))` — three
parameters, none of them `time.Time`. The reflect-based drift test in §6
(b) covers this.

`orchestrator/fanout.go` (existing file, one-line change) wraps its planning
call:

```go
// existing:
plan, err := planWithProgress(ctx, t.Prompt, agents)
// becomes:
plan, err := MeasurePlanning(ctx, dec.ConversationID, func() ([]planner.Node, error) {
    return planWithProgress(ctx, t.Prompt, agents)
})
```

**⚠️ file-domain reconciliation.** The task brief mandates "new `*_timing.go`
files, no changes to existing signatures." Wrapping the call in fanout.go is a
1-line **call-site** change (no signature change) — but it *is* an edit to an
existing file, which the brief also warns against. The trade-off is: without
the call-site wrap, the injection file is dead code and the metric is unfed
in production. The mitigation adopted here: the wrap is a single-line diff
that does not alter fanout.go's control flow, error semantics, or any other
call site; a `go vet ./...` on the diff confirms no signature drift. The
plan-stage test matrix (§7 of the plan) asserts fanout.go's diff hunks total
≤ 3 lines (the wrap plus one blank-line adjustment).

If the follow-up wiring PR is not landed at eval time, the wrap still writes
via `noopProbeWriter{}` (zero cost, no rows), so the change is safe to ship
dark.

**B. `internal/executor/task_dispatch_timing.go` (TaskDispatchLatency)**

Exports `MeasureDispatch(inner executor.Executor, convIDFn func(executor.Task) string) executor.Executor`
that wraps an `Executor`. The wrapper's `Run` opens a span via the
shared `observerstore.ProbeEventWriter` bound in this file (same
`atomic.Value` pattern as A), then `defer h.End(ctx)` covers every
return path. No `time.Time` fields cross the boundary.

`convIDFn` is caller-provided (defaults to `func(t) string { return t.ID }`) so
existing executors need no interface change. **No edits to
`internal/executor/*.go` existing files.** The follow-up wiring PR (again,
cmd-side, out of this WT) does the actual `executor = MeasureDispatch(executor,
...)` composition at slave-agent boot. Until then this file is used only by
`observer_bench` and `tunnel_bench` from `tools/eval/microbench/`.

**C. `internal/observerstore/probe_events_writer.go` (ObserverOverhead)**

The writer boundary MUST NOT accept caller-supplied timestamps (§6 (b)).
Following WT-1-routing-trace §2.1's seed pattern, `probe_events` uses a
`SpanHandle`-based API where `time.Now()` is stamped exclusively inside
`StartSpan` and `End` (both of which live in this file). Cross-package
callers (orchestrator, executor) get the handle; they can populate no
time fields on it because all time fields are unexported.

Exported API:

```go
// Kind enumerates the three probe span kinds; string-based enum keeps
// the DDL happy but Kind is a distinct type so a bare "typo string"
// cannot be passed at the call site.
type Kind string
const (
    KindDriverPlanning Kind = "driver_planning"
    KindTaskDispatch   Kind = "task_dispatch"
    KindObserverWrite  Kind = "observer_write"
)

// ProbeSpanHandle is opaque outside this package: all fields are
// unexported, so a caller in another package cannot forge a
// (kind, convID, startedAt, nonce) tuple via struct literal. The only
// way to produce a non-zero handle is via ProbeEventWriter.StartSpan.
type ProbeSpanHandle struct {
    kind    Kind
    convID  string
    started time.Time      // monotonic-reading preserved
    nonce   uint64         // process-local atomic counter, ensures unique event_id
    writer  ProbeEventWriter
}

// ProbeEventWriter is the observerstore-side interface every probe uses.
// It exposes only two verbs, StartSpan and — through the returned handle —
// End. The internal insertRow(ctx, ...) method is unexported so only
// (*ProbeSpanHandle).End can drive the DB write.
type ProbeEventWriter interface {
    StartSpan(kind Kind, convID string) (*ProbeSpanHandle, error)
    IsNoop() bool  // mirrors WT-1's IsNoopWriter; cmd-side fail-loud helper
}

func NewProbeEventWriter(db *sql.DB) ProbeEventWriter { ... }

// End closes the span, deriving span_end_at = time.Now() *inside this
// function* and computing duration_ns from the retained monotonic
// reading of h.started. It is a no-op on the noop writer.
func (h *ProbeSpanHandle) End(ctx context.Context) error {
    end := time.Now()
    return h.writer.insertRow(ctx, probeEventRow{
        eventID:    deriveEventID(h.convID, h.kind, h.started, h.nonce),
        kind:       h.kind,
        convID:     h.convID,
        startedAt:  h.started,
        endedAt:    end,
        durationNs: end.Sub(h.started).Nanoseconds(),
    })
}
```

**Unexported row + insertRow** (no exported struct with time fields):

```go
type probeEventRow struct {
    eventID          string
    kind             Kind
    convID           string
    startedAt        time.Time  // stamped in StartSpan; monotonic
    endedAt          time.Time  // stamped in End; monotonic
    durationNs       int64
    wallclockDeltaMs int64      // reserved; 0 for same-process spans
}

// insertRow is unexported; only ProbeSpanHandle.End reaches it, so the
// caller-timestamp ban is enforced structurally: there is no exported
// path that lets external code hand a time.Time to the writer.
func (w *probeEventWriter) insertRow(ctx context.Context, r probeEventRow) error { ... }
```

**`StartSpan` validation** (defense-in-depth, all fail-closed):

* `kind` must be one of the three declared `Kind` constants — any other
  value → returns `fmt.Errorf("probe_events: unknown kind %q", kind)`,
  no allocation, no `time.Now()` call.
* `convID` must pass `ValidConversationID` (§6 (c)) — invalid →
  returns `fmt.Errorf("probe_events: invalid conversation_id")` and
  bumps `probe_id_rejected_total`.
* Only after both pass does `StartSpan` call `time.Now()` and populate
  the seed fields. Handle's `writer` field points back at the
  `ProbeEventWriter` receiver so `End` can call `insertRow` without an
  extra parameter.

**`insertRow` validation** (belt-and-suspenders):

* `r.durationNs >= 0` — negative → return error, do not write. Guards
  the theoretical case of a refactor losing the monotonic reading on
  `h.started`. Test `TestInsertRow_RejectsNegativeDuration` uses a
  test-only injector to build a row with `endedAt = startedAt - 1ns`
  and asserts the writer rejects it.
* Statement is a single `db.ExecContext(ctx, "INSERT ... VALUES(?,?,...)
  ON CONFLICT(event_id) DO NOTHING", ...)` — see §6 (e).

**Test contract asserting the timestamp ban structurally**
(`TestNoCallerTimestampAtBoundary`):

* Uses `reflect.TypeOf((*ProbeEventWriter)(nil)).Elem()` and iterates
  every exported method + every exported field of every exported struct
  in the package; asserts NONE has a parameter or field of type
  `time.Time`. This catches drift where a future PR "conveniently" adds
  a `WriteProbeEvent(row ProbeEventRow)` shortcut with an exported
  time-typed field.

Style otherwise matches `route_reasons_writer.go`: `?` placeholders,
SQLite DDL applied via the embedded schema.sql (§4.2), `ON
CONFLICT(event_id) DO NOTHING` for idempotency, postgres path deferred
to a follow-up (same rationale WT-1-routing-trace used at
`internal/observerstore/route_reasons_writer.go:47`).

ObserverOverhead itself is measured by
`tools/eval/microbench/observer_bench` calling `w.StartSpan(...); h.End(ctx)`
in a tight loop with the `NoObserver` ablation flag flipped between
rounds (§5.2).

## 4. DDL and consumer contract

### 4.1 Placement

Appended at the **end** of `internal/observerstore/schema.sql`, after the
`capability_snapshot_usages` block. This keeps merge conflicts with
run-schema and capability-snapshot worktrees on file-tail order only.

### 4.2 SQL

```sql
-- WT-2-overhead-probes: in-process latency spans (driver planning,
-- agentserver task dispatch, observer write). Consumed by
-- DriverPlanningOverhead / TaskDispatchLatency / ObserverOverhead p50/p95
-- via the WT-2-metric-extract SELECT in §4.5 of the spec.
CREATE TABLE IF NOT EXISTS probe_events (
    event_id            TEXT PRIMARY KEY,
    probe_kind          TEXT NOT NULL,     -- 'driver_planning' | 'task_dispatch' | 'observer_write'
    conversation_id     TEXT NOT NULL,     -- validated ^[A-Za-z0-9_-]{8,128}$ before insert
    span_start_at       TEXT NOT NULL,     -- RFC3339Nano, audit only
    span_end_at         TEXT NOT NULL,     -- RFC3339Nano, audit only
    duration_ns         INTEGER NOT NULL,  -- AUTHORITATIVE latency; end.Sub(start) with monotonic
    wallclock_delta_ms  INTEGER NOT NULL DEFAULT 0  -- cross-machine skew; 0 for same-process spans
);
CREATE INDEX IF NOT EXISTS idx_probe_events_kind_conv
    ON probe_events(probe_kind, conversation_id, span_start_at);
```

### 4.3 EventID derivation

`event_id` is derived server-side (never accepted from callers), following
WT-1-routing-trace §6 (f) exactly. `deriveEventID` is unexported and
called only from `(*ProbeSpanHandle).End`:

```
sum := sha256.Sum256([]byte(
    convID + "|" + string(kind) + "|" +
    strconv.FormatInt(start.UnixNano(), 10) + "|" +
    strconv.FormatUint(nonce, 10),
))
eventID = hex.EncodeToString(sum[:16])   // 32 hex chars
```

`nonce` is a process-local `atomic.Uint64` counter starting at 1, seeded
into the handle by `StartSpan`, so two spans landing on the same
nanosecond in the same conv/kind still produce distinct `event_id`s.
The seed fields (`convID`, `kind`, `started`, `nonce`) all live in the
`ProbeSpanHandle`'s unexported fields; callers in other packages cannot
forge one via struct literal. Test `TestDeriveEventID_UniquePerCall`
constructs 10 000 handles with the same conv/kind in a tight loop and
asserts `len(distinct IDs) == 10000`.

### 4.4 RoutingLatencyP50P95 comes from `route_reasons`

`RoutingLatencyP50P95` is NOT inserted into `probe_events`. It is read from
the existing `route_reasons.decision_duration_ns` column (WT-1-routing-trace
already populates this). The scale sweep harness (§5.3) computes it via:

```sql
SELECT decision_duration_ns FROM route_reasons ORDER BY decision_started_at;
```

then percentile-selects in Go. This matches the todo_list Phase 2 constraint
"本 worktree 不注入 dispatch."

### 4.5 Consumer-view SELECT template (canonical for §D2 metric-extract)

The following SELECT is the contract metric-extract MUST implement so field
naming does not drift between this WT and the D2 CSV output. It is included
here so both this spec and the metric-extract spec share one source of truth.

```sql
-- DriverPlanningOverhead p50/p95 (example; same shape for the other two spans).
-- Percentile computation happens Go-side after ordering; SQLite does not have
-- a native percentile function. The ORDER BY makes streaming through the
-- result set safe (no post-fetch sort needed).
SELECT duration_ns
FROM probe_events
WHERE probe_kind = 'driver_planning'
ORDER BY duration_ns ASC;
```

Placeholder `?` for `probe_kind`; metric-extract binds one of the three
literals per query. The `idx_probe_events_kind_conv` index makes the WHERE
clause O(log n) even at 10 M rows.

### 4.6 CSV field names (canonical for `tools/eval/scale_sweep/`)

The sweep harness emits one row per scale point with the following fields
(header locked; metric-extract keys off these names):

```
contexts,tools_per_context,artifact_size_bytes,
driver_planning_overhead_p50_ns,driver_planning_overhead_p95_ns,
task_dispatch_latency_p50_ns,task_dispatch_latency_p95_ns,
tunnel_overhead_p50_ns,tunnel_overhead_p95_ns,
artifact_transfer_throughput_p50_bps,artifact_transfer_throughput_p95_bps,
observer_overhead_p50_ns,observer_overhead_p95_ns,
model_proxy_overhead_bench_p50_ns,model_proxy_overhead_bench_p95_ns,
routing_latency_p50_ns,routing_latency_p95_ns,
samples,warmup_iters,seed
```

`_bench_` in `model_proxy_overhead_bench_*` disambiguates against
WT-2-credential-workload's `model_proxy_overhead_workload_*` columns.

## 5. Microbench harnesses

### 5.1 Common contract (`tools/eval/microbench/common/`)

Every microbench binary shares the same skeleton:

```go
type BenchConfig struct {
    Warmup   int    // ≥ 100; enforced; see §6 (a)
    Samples  int    // ≥ 500; sampled after warmup
    Seed     int64  // seed for prompt/payload permutation
    DryRun   bool   // --dry-run
    OutCSV   string // sink path (absolute or "-" for stdout)
    ProbeDB  string // optional; sqlite path to write probe_events rows
}

type Sample struct { DurationNs int64; Bytes int64 } // Bytes = 0 for latency-only benches

// RunBench takes a step closure. It runs cfg.Warmup iterations
// (throwing away the samples), then cfg.Samples iterations recording
// (DurationNs, Bytes) each. Returns the sample slice; caller computes
// p50/p95.
func RunBench(cfg BenchConfig, step func() (Sample, error)) ([]Sample, error)
```

**Warm-up enforcement**: if `cfg.Warmup < 100`, `RunBench` returns
`ErrWarmupTooShort` and writes nothing. Test
`TestRunBench_RejectsShortWarmup` covers.

**Warm-up sampling isolation** (audit-3-caliber correctness check): the
sampler `[]Sample` slice is allocated at `len == cfg.Samples` exactly, and
the recording index only starts incrementing *after* warm-up completes. Test
`TestRunBench_WarmupSamplesExcluded` runs `Warmup=100, Samples=1` with a
step function that returns `DurationNs = iteration_index * 1e6` (so warm-up
samples are 0..99 ms and the sampled iteration is 100 ms), then asserts
`samples[0].DurationNs == 100_000_000` — proving no warm-up sample leaked
into the p50 window.

**Percentile helpers**: `func P50(samples []Sample) int64` and
`func P95(samples []Sample) int64` operate on a slice of `DurationNs`
values; both sort in-place (nearest-rank method) and are covered by
table-driven tests including the empty-input case (returns 0 + logs
"empty sample slice"; never panics).

**Same-subject / same-seed rule (§6 (a))**: the seed is threaded into every
step closure so payload permutation, prompt selection, and any randomness
inside the bench is deterministic given `--seed`. Two runs of the same
binary with the same seed MUST produce byte-identical CSV modulo timestamp
columns. Test `TestRunBench_DeterministicUnderSeed`.

### 5.2 Three bench binaries

**`tunnel_bench/main.go`** — computes `TunnelOverhead` (latency-only ping)
AND `ArtifactTransferThroughput` (bytes/s over sizes {1 KiB, 1 MiB, 100 MiB}).

* Ping mode: RTT of a 32-byte HTTP GET against a local `httptest.NewServer`
  (or a real observer HTTPS endpoint if `--target` is set). Recorded as
  `DurationNs`.
* Transfer mode: `io.CopyN(io.Discard, req.Body, size)` recorded as
  `(DurationNs, size)`.
* **100 MiB payload is memory-only** (§6 (d)) — served from a
  `bytes.NewReader` wrapping a pre-allocated `[]byte` (or `io.LimitReader`
  over `/dev/zero` on Linux), NEVER staged to a tmpfile.
* `--dry-run`: prints the planned config table (targets × sizes × iters) and
  exits 0 without opening any socket.

**`observer_bench/main.go`** — computes `ObserverOverhead` (span-write cost).

* Opens a temp SQLite file, creates a `ProbeEventWriter` against it.
* Two rounds under the same seed: round A with `NoObserver = true` (skip
  writer; step returns 0 duration), round B with `NoObserver = false`
  (real write). Difference is the overhead.
* The `NoObserver` value is read via `ablation.CurrentBool(NoObserver)`
  from the shared registry landed by WT-1-ablation-registry (this WT does
  NOT register a new flag).
* `--dry-run`: prints round plan + skips SQLite open.

**`proxy_bench/main.go`** — computes `ModelProxyOverhead` (naked proxy vs
direct).

* Two round-trips of the SAME prompt against two endpoints: proxy URL vs
  direct-provider URL. Endpoints are `--proxy-url` and `--direct-url`
  flags; both default to `http://127.0.0.1:0` (a `httptest.NewServer` the
  bench itself spins up, echoing after a configurable synthetic delay via
  `--fake-delay-ms`). This makes CI runs deterministic without real API
  keys.
* Overhead is the p50/p95 difference of the two `DurationNs` distributions.
* `--dry-run`: prints URL pair + round count and exits.

### 5.3 Scale sweep harness (`tools/eval/scale_sweep/`)

`plan.go` expands the cross-product:

```go
type ScaleAxes struct {
    Contexts        []int   // default {1,2,4,8,16}
    ToolsPerContext []int   // default {10,50,100}
    ArtifactSizes   []int64 // default {1<<10, 1<<20, 100<<20}
}
type ScalePoint struct{ Contexts, ToolsPerContext int; ArtifactSizeBytes int64 }
func Expand(a ScaleAxes) []ScalePoint    // returns 5*3*3 = 45 points by default
```

Comma-separated CLI flags override the defaults. `Expand` sorts the points
lexicographically for stable CSV output; test `TestExpand_DefaultCount`
asserts `len == 45`.

`sweep/main.go`:

1. For each scale point, invoke `tunnel_bench`, `observer_bench`,
   `proxy_bench` via `os/exec` with `--warmup 100 --samples 500 --seed
   <axis-derived>` and collect their emitted CSVs.
2. Additionally query `probe_events` (spans emitted during that point's
   run, tagged by a per-point `conversation_id` sentinel like
   `sweep-c1-t10-1024`) for `driver_planning` / `task_dispatch` /
   `observer_write` p50/p95, and `route_reasons` for
   `routing_latency_p50_ns` / `_p95_ns`.
3. Write one row per point to `--out <path>` using the §4.6 header.

**`--dry-run`**: prints the point plan and the exact `os/exec` command lines
that would run (with args), then exits 0. Zero network / disk activity.

**Point budget**: at 45 points × 3 benches × (100 warm-up + 500 sample) iters,
a single sweep is ~67 500 real iterations per bench. Wall-clock guidance is
non-binding (documented in the sweep README), and the sweep is designed to be
resumable via `--only "1,4"` (comma-separated ScalePoint indices).

## 6. Security (mandatory, mirrors WT-1-routing-trace §6)

### (a) Warm-up ≥ 100 iterations, same subject / seed / payload

Cold-start latency (JIT, connection pooling, disk-cache warm) can be
100–1000× the steady-state number. If the first sample lands in the p50/p95
window, the paper's headline overhead number is a lie. Mitigation:

* `common.RunBench` rejects `cfg.Warmup < 100` with `ErrWarmupTooShort`.
  Test `TestRunBench_RejectsShortWarmup`.
* Warm-up samples are discarded — the sampler slice's index is only
  incremented after the warm-up loop exits. Test
  `TestRunBench_WarmupSamplesExcluded` (see §5.1).
* All three binaries default `--warmup 100 --samples 500` and refuse to
  proceed with values below the floor. Test asserts CLI parse rejects
  `--warmup 99`.
* The same `--seed` value yields byte-identical payload/prompt/permutation
  across runs. Test `TestRunBench_DeterministicUnderSeed`.

### (b) Local monotonic clock only; caller timestamps rejected

All `span_start_at` / `span_end_at` come from `time.Now()` inside the
process that owns the span. The API is structured so cross-package
callers cannot supply a `time.Time` at all: `ProbeSpanHandle` has
unexported `started` / `nonce` / `writer` fields (§3.3.C), populated
exclusively inside `StartSpan`; `End` stamps `time.Now()` locally; the
row struct that reaches `insertRow` is unexported.
`MeasurePlanning` / `MeasureDispatch` follow the same pattern — the
public helpers take a closure (`fn func() (T, error)`) or an executor;
they do NOT take a `time.Time`.

Test `TestNoCallerTimestampAtBoundary` (in
`internal/observerstore/probe_events_writer_test.go`) uses
`reflect.TypeOf(...)` to walk every exported method of
`ProbeEventWriter`, every exported field of `ProbeSpanHandle`, and the
`MeasurePlanning` / `MeasureDispatch` function signatures, asserting
NONE has a parameter or field of type `time.Time`. This is a
spec-driven contract test that catches drift where a future PR
"conveniently" adds a shortcut with a `time.Time` argument or field.

The optional `wallclock_delta_ms` column exists for future cross-machine
spans (stays 0 in this WT since all three injection points are
same-process).

### (c) `probe_events.conversation_id` length + regex gate

`common.ValidConversationID(s string) bool` returns true iff:

```go
var convIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
```

Enforced at THREE boundaries:

1. `MeasurePlanning` / `MeasureDispatch` — invalid ID → skip write + bump
   `probe_id_rejected_total` + `log.Printf("probe: rejected conv_id %q", id)`.
2. `WriteProbeEvent` — invalid ID → return non-nil error, do not touch DB.
3. `sweep/main.go` — when composing per-point sentinel IDs, the sweep
   asserts `ValidConversationID(sentinel) == true` at startup and refuses
   to run otherwise (prevents a future default-value change from silently
   losing all sweep rows to boundary rejection).

Tests: `TestValidConversationID_TableDriven` (all boundary lengths + all
disallowed chars); `TestWriteProbeEvent_RejectsInvalidConvID`;
`TestMeasurePlanning_SkipsOnInvalidConvID` (uses a captureWriter + asserts
zero rows on `"a b"` and other malformed IDs).

### (d) 100 MiB artifact stays in memory (no disk staging)

The transfer bench MUST serve the 100 MiB payload from a
`bytes.NewReader(preAllocated[100<<20])` or `io.LimitReader(&zeroReader{},
100<<20)`. No `os.CreateTemp`, no `io.Copy(fileWriter, ...)`, no
`/tmp/*` writes anywhere in the transfer path. Test
`TestTunnelBench_NoDiskWrites` verifies via two invariants:

1. Bench-side: an interface-satisfying `fs.FS` stub with all writes
   plumbed through it; the test uses `testfs.WriteDenyingFS` (a helper
   that returns `syscall.EACCES` on any `Create`/`OpenFile(...O_WRONLY)`)
   and asserts the bench completes without error.
2. Environment invariant: the test snapshots `du -sb $TMPDIR` before and
   after the bench (via `os.Stat` walk of `TMPDIR`) and asserts the two
   totals are equal. `TMPDIR` defaults to `t.TempDir()` per test — a
   guaranteed-clean directory whose disk delta MUST be 0 for the transfer
   bench. Any temp file written under `TMPDIR` will fail this second
   assertion even if the first is bypassed. The `du -sb`-equivalent walk
   is a Go helper `sumTreeBytes(t.TempDir())`; no external `du` binary is
   invoked (Windows CI compat).

### (e) SQL parameterization

Every `WriteProbeEvent` INSERT uses `?` placeholders exclusively. No
`fmt.Sprintf`, no `strings.Replace`, no `text/template` on any
user-influenced field. Static analysis: the writer's single `ExecContext`
call is the only path that writes to `probe_events`. Test
`TestWriteProbeEvent_RejectsSQLInjectionInConvID` (feeds a
`"'; DROP TABLE probe_events; --"` string, asserts the write is rejected
by the regex gate before it reaches SQL — belt AND suspenders).

### (f) CSV formula-injection escape

Excel-family spreadsheets execute cell contents starting with `=`, `+`,
`-`, `@`, `\t`, or `\r` as formulas. Since our CSVs feed metric-extract
which the paper author may open in Excel, the sink MUST prefix any cell
whose first byte is one of `{=, +, -, @, \t, \r}` with a single quote
`'`. Cells starting with any other byte are written verbatim.

`common/csvsink.go` exposes `func WriteRow(w io.Writer, cells []string)
error` that applies the escape rule per cell + quotes cells containing
`,` / `"` / `\n` per RFC 4180. Test `TestCSVSink_EscapesInjection`
covers each of the six trigger prefixes; test
`TestCSVSink_LeavesBenignCellsUnchanged` covers the negative case.

This WT's csvsink is **inlined**, not shared with WT-2-metric-extract's
D2 escape helper — the two worktrees may land in parallel and cross-
package imports would tangle merge order. The two implementations share
a common test file `common/csvsink_test.go` whose golden output MUST
match metric-extract's golden byte-for-byte; a mismatch is a spec
violation caught in Phase 2 integration merge (documented as a merge-time
audit in this spec's §8).

### (g) Consumer-view reverse audit — worked SELECT

Assume WT-2-metric-extract wants `DriverPlanningOverhead` p50/p95 from a
run's observer database. The following SELECT (already the §4.5 canonical
form) suffices; no `probe_events` schema change is required to make
metric-extract's job possible:

```sql
SELECT duration_ns
FROM probe_events
WHERE probe_kind = ?
  AND conversation_id BETWEEN ? AND ?   -- optional run-scope filter
ORDER BY duration_ns ASC;
```

Metric-extract binds `probe_kind = 'driver_planning'`, streams the result
set, and applies the nearest-rank p50/p95 in Python (same formula as
`common.P50`/`P95` here — the plan-stage test matrix includes a
cross-language equivalence check). The consumer-view is thus fully served
by columns this WT ships (`probe_kind`, `duration_ns`, `conversation_id`);
no follow-up schema PR is required.

### (h) CI-conditionalized perf assertions (Phase 1 PR #59 lesson)

Every test that asserts a latency numerical bound (as opposed to a
correctness bound like "the sampled iteration wasn't a warm-up
iteration") MUST guard with:

```go
if testing.Short() || os.Getenv("CI") != "" {
    t.Skip("perf assertion skipped in short/CI mode")
}
```

Correctness tests (warm-up rejection, regex gate, no-disk-write invariant,
CSV escape, seed determinism, empty-sample edge case) run on every
invocation — those are cheap and deterministic. Only tests that measure
absolute wall-clock or throughput thresholds are gated. Test
`TestPerfTestsUseCIGuard` scans the `microbench/`/`scale_sweep/`
`_test.go` files with a shallow `go/parser` walk and asserts every test
whose name matches `^Test.*Perf|Latency|Throughput.*$` contains the
literal `testing.Short()` OR `os.Getenv("CI")` in its function body — a
structural check that prevents drift.

Additionally the sweep harness itself refuses to run its full 45-point
matrix under `CI != ""` unless `--force-full` is passed; the CI smoke
target invokes `sweep --dry-run --contexts 1,2 --tools-per-context 10
--artifact-size 1KiB`, exercising the plan / CLI / CSV-header code paths
without any actual bench execution.

## 7. Acceptance

* All seven overhead metrics produce a p50 and p95 value on a fixture run
  (`tools/eval/scale_sweep/testdata/fixture.sqlite` shipped with the WT;
  contains synthetic `probe_events` rows and `route_reasons` rows so
  `sweep --from-fixture` produces a fully-populated CSV without touching
  the network). Golden CSV `testdata/fixture.golden.csv` diffs clean.
* `go test ./tools/eval/microbench/... ./tools/eval/scale_sweep/...
  ./internal/orchestrator/... ./internal/executor/...
  ./internal/observerstore/... -count=1 -shuffle=on -race` passes.
* `go vet ./...` clean; `gofmt -l` empty.
* `git diff origin/paper/v3-integration -- internal/dispatch/` prints
  nothing (§2 file-domain guard).
* CI-smoke commands both exit 0 under `CI=1`:
  * `./tools/eval/microbench/tunnel_bench --dry-run`
  * `./tools/eval/scale_sweep/sweep --contexts 1,2 --tools-per-context 10 --artifact-size 1KiB --dry-run`

## 8. Open issues / follow-ups

* **cmd-side wiring** for the three probe writers lives in a follow-up PR
  (same story as WT-1-routing-trace's slave-agent wiring). The
  `IsNoopProbeWriter()` helper this WT exports is what that PR uses to
  `log.Fatal` if it forgets to install a writer.
* **postgres schema.sql mirror** of `probe_events` is deferred to a
  follow-up per the same rationale as
  `internal/observerstore/postgres/schema.sql:180-201` for `route_reasons`.
  The follow-up must ship a pg-native `WriteProbeEvent` (`$N` placeholders,
  no `?`).
* **csvsink cross-worktree byte-golden**: at Phase 2 integration merge time,
  the maintainer runs `diff -u tools/eval/microbench/common/testdata/csv_golden.csv
  tools/eval/metrics/testdata/csv_golden.csv` and asserts empty output.
  Any drift means one of the two escape implementations skipped a trigger
  prefix. This audit is documented here (spec §8) and in the plan (§7).
