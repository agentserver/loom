# WT-2-overhead-probes — Plan

> Drives [wt2-overhead-probes.spec.md](wt2-overhead-probes.spec.md). Strict
> TDD; every test maps to a spec section, a Security mitigation (a–h), or
> the "no dispatch changes" file-domain gate. Assumes cwd =
> `.worktrees/p2-overhead-probes/multi-agent/` (the Go module root).

## Global constraints (from spec)

- **File domain**: `tools/eval/microbench/`, `tools/eval/scale_sweep/`, plus
  the three new `*_timing.go` files in `internal/orchestrator/`,
  `internal/executor/`, `internal/observerstore/`, plus one appended DDL
  block on `internal/observerstore/schema.sql`, plus one 1-line call-site
  wrap in `internal/orchestrator/fanout.go`. **NOTHING under
  `internal/dispatch/`.** `git diff origin/paper/v3-integration --
  internal/dispatch/` MUST print nothing at end of Task 8.
- **Diff floor on `fanout.go`**: exactly one hunk touching ≤ 3 lines (the
  `MeasurePlanning` wrap + a paired closing brace/blank line at most).
  Test 8 enforces this.
- **Go version**: 1.26.x (module declares `go 1.26.2`).
- **Test invocation**:
  `go test ./tools/eval/microbench/... ./tools/eval/scale_sweep/...
     ./internal/orchestrator/... ./internal/executor/...
     ./internal/observerstore/... -count=1 -shuffle=on -race`
- **Lint**: `go vet ./...` + `gofmt -l tools/eval/microbench
  tools/eval/scale_sweep internal/orchestrator/planning_timing.go
  internal/executor/task_dispatch_timing.go
  internal/observerstore/probe_events_writer.go`.
- Every `git commit` ends with `Co-Authored-By: Claude Opus 4.8 (1M context)
  <noreply@anthropic.com>`. **Do NOT push.**

## File map

| Path | Action | Responsibility |
|---|---|---|
| `internal/observerstore/schema.sql` | **MODIFY (APPEND)** | Append `probe_events` DDL + `idx_probe_events_kind_conv` at end of file. |
| `internal/observerstore/probe_events_writer.go` | **CREATE** | `Kind` enum, `ProbeSpanHandle` (unexported time fields), `ProbeEventWriter` interface (StartSpan / IsNoop), `NewProbeEventWriter`, unexported `probeEventRow`, unexported `insertRow`, `deriveEventID`, `noopProbeWriter`, atomic-value `writerBox` glue, `IsNoopProbeWriter` helper for cmd-side fail-loud. |
| `internal/observerstore/probe_events_writer_test.go` | **CREATE** | Tests #1–#12 (writer, spans, forgery, timestamp ban, SQL injection, EventID uniqueness, ON CONFLICT). |
| `internal/orchestrator/planning_timing.go` | **CREATE** | `MeasurePlanning[T any]` generic + package-level `atomic.Value` for the `ProbeEventWriter`, `SetPlanningProbeWriter`, `IsPlanningProbeWriterNoop`. |
| `internal/orchestrator/planning_timing_test.go` | **CREATE** | Tests #13–#17. |
| `internal/orchestrator/fanout.go` | **MODIFY (1 hunk, ≤3 lines)** | Wrap `planWithProgress(...)` call at line ~250 in `MeasurePlanning`. |
| `internal/executor/task_dispatch_timing.go` | **CREATE** | `MeasureDispatch(inner Executor, convIDFn) Executor` + package-level probe writer glue. |
| `internal/executor/task_dispatch_timing_test.go` | **CREATE** | Tests #18–#22. |
| `tools/eval/microbench/common/warmup.go` | **CREATE** | `BenchConfig`, `Sample`, `RunBench`, `P50`, `P95`, `ErrWarmupTooShort`. |
| `tools/eval/microbench/common/warmup_test.go` | **CREATE** | Tests #23–#28. |
| `tools/eval/microbench/common/csvsink.go` | **CREATE** | Formula-injection-safe `WriteRow`. |
| `tools/eval/microbench/common/csvsink_test.go` | **CREATE** | Tests #29–#30 + golden fixture `testdata/csv_golden.csv`. |
| `tools/eval/microbench/common/idvalid.go` | **CREATE** | `ValidConversationID` + `expvar` `probe_id_rejected_total`. |
| `tools/eval/microbench/common/idvalid_test.go` | **CREATE** | Test #31. |
| `tools/eval/microbench/common/clock.go` | **CREATE** | Documented monotonic-only clock accessor `now() time.Time` (single indirection so a future clock-injection test can stub it — but the stub is TEST-ONLY, exposed via `_test.go` build tag). |
| `tools/eval/microbench/common/clock_test.go` | **CREATE** | Test #32 (monotonic reading present). |
| `tools/eval/microbench/tunnel_bench/main.go` | **CREATE** | Ping + transfer bench, `--dry-run`. |
| `tools/eval/microbench/tunnel_bench/main_test.go` | **CREATE** | Tests #33–#37 (no-disk-write, dry-run, 100 MiB in-memory, CI-guarded perf assertion, invalid warmup rejection). |
| `tools/eval/microbench/observer_bench/main.go` | **CREATE** | NoObserver on/off cell bench. |
| `tools/eval/microbench/observer_bench/main_test.go` | **CREATE** | Tests #38–#40. |
| `tools/eval/microbench/proxy_bench/main.go` | **CREATE** | Proxy vs direct bench (in-process `httptest`). |
| `tools/eval/microbench/proxy_bench/main_test.go` | **CREATE** | Tests #41–#43. |
| `tools/eval/scale_sweep/plan.go` | **CREATE** | `ScaleAxes`, `Expand`. |
| `tools/eval/scale_sweep/plan_test.go` | **CREATE** | Tests #44–#46. |
| `tools/eval/scale_sweep/sweep/main.go` | **CREATE** | CLI, CSV assembly, `route_reasons` query, `--dry-run`, `--force-full` CI guard. |
| `tools/eval/scale_sweep/sweep/main_test.go` | **CREATE** | Tests #47–#52. |
| `tools/eval/scale_sweep/testdata/fixture.sqlite` (+ `fixture.golden.csv`, `build_fixture.go`) | **CREATE** | Deterministic acceptance fixture. |
| `tools/eval/microbench/perftests_guard_test.go` | **CREATE** | Test #55 (`TestPerfTestsUseCIGuard`) — package-level `go/parser` walk over `microbench/`/`scale_sweep/` `_test.go` files. Lives in the `microbench` root so `go test ./tools/eval/microbench/...` executes it exactly once. |

## Test matrix (each row: test → spec/security anchor)

### Writer (`internal/observerstore/probe_events_writer_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 1 | `TestOpenSQLite_HasProbeEventsTable` | After `OpenSQLite`, `SELECT name FROM sqlite_master WHERE name='probe_events'` returns one row; column set matches §4.2 exactly (including `wallclock_delta_ms INTEGER NOT NULL DEFAULT 0`). | §4.2 |
| 2 | `TestStartSpan_RejectsUnknownKind` | `w.StartSpan(Kind("banana"), "conv-abcdefgh")` returns error, no rows written, no `time.Now()` call (uses a `nowStub` in test build that panics if called). | §3.3.C validation |
| 3 | `TestStartSpan_RejectsInvalidConvID` | `w.StartSpan(KindDriverPlanning, "no")` (< 8 chars) returns error, bumps `probe_id_rejected_total`, no row. | Security (c) |
| 4 | `TestSpanEndToEnd_RoundTrip` | `h, _ := w.StartSpan(KindDriverPlanning, "conv-abc12345"); h.End(ctx)` writes exactly one row with correct `probe_kind`, `duration_ns > 0`, `conversation_id` matches, `event_id` is 32 hex chars. | §3.3.C, §4.3 |
| 5 | `TestInsertRow_ONCONFLICT_DoNothing` | Test-only injector writes two rows with identical `eventID`; second returns nil, table has 1 row. | §4.3 idempotency |
| 6 | `TestNoCallerTimestampAtBoundary` | `reflect.TypeOf(*ProbeEventWriter).Method(...)` — every exported method's `In(i)` and every exported field of `ProbeSpanHandle` — asserts NONE is `reflect.TypeOf(time.Time{})`. Also verifies `MeasurePlanning` / `MeasureDispatch` function signatures (imports orchestrator + executor via test-only file). | Security (b) |
| 7 | `TestInsertRow_RejectsNegativeDuration` | Test-only injector builds `probeEventRow{durationNs: -1}` (via same package); `insertRow` returns error, no row. | §3.3.C guard |
| 8 | `TestStartSpan_SQLInjectionInConvID_RejectedByRegex` | `w.StartSpan(KindDriverPlanning, "abc'; DROP TABLE probe_events; --")` returns error via regex gate BEFORE reaching SQL; table still exists, row count unchanged. | Security (c) + (e) |
| 9 | `TestWriter_ParameterizedSQL` | Test-only injector allowlists a payload matching the regex (`"conv-DROP-TB1"`) and asserts the string is stored verbatim in `conversation_id` (proves `?` placeholder is doing its job, not string interpolation). | Security (e) |
| 10 | `TestDeriveEventID_UniquePerCall` | 10 000 spans with same convID + same kind in tight loop → 10 000 distinct `event_id`s. | §4.3 |
| 11 | `TestSetProbeWriter_AtomicValueNoPanic` | Cycle `SetProbeWriter(noop) → SetProbeWriter(real) → SetProbeWriter(nil)` 100 times concurrently from 8 goroutines; no panic; `IsNoopProbeWriter()` reports the last-installed state. | §3.2 (mirrors WT-1 §2.2) |
| 12 | `TestIsNoopProbeWriter_TrueByDefault` | Fresh package: `IsNoopProbeWriter() == true`; after `SetProbeWriter(real)`: `== false`; after `SetProbeWriter(nil)`: `== true`. | Security (d)/(h) cmd-side hook |

### Orchestrator planning (`internal/orchestrator/planning_timing_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 13 | `TestMeasurePlanning_HappyPath` | Bind a capture-writer, call `MeasurePlanning(ctx, "conv-abc12345", fn)`; exactly one span row with `kind = driver_planning`, `duration_ns ≈ fn's sleep`. | §3.3.A |
| 14 | `TestMeasurePlanning_PanicRecovery_StillWritesSpan` | `fn` panics; `MeasurePlanning` re-panics **after** `defer h.End(ctx)` fires; capture writer received exactly one row. | §3.3.A defer coverage |
| 15 | `TestMeasurePlanning_NoopWriter_ZeroAllocationsOverhead` | Default noop; call 100 000 times; `testing.Benchmark` reports `AllocsPerOp == 0`. Guarded by `if testing.Short() { t.Skip("perf bench") }` — perf-shape but correctness verifier for the noop fast-path. | Security (h) |
| 16 | `TestMeasurePlanning_RejectsInvalidConvID` | `convID = "bad id"` → span not written, `probe_id_rejected_total` incremented, `fn` still runs and its return value propagates. | Security (c) |
| 17 | `TestMeasurePlanning_SignatureNoTimeArg` | Reflect-check: `MeasurePlanning[T]`'s reified signature (via `reflect.TypeOf(MeasurePlanning[int])`) has three params `(context.Context, string, func() (int, error))` — none is `time.Time`. | Security (b) |

### Executor dispatch (`internal/executor/task_dispatch_timing_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 18 | `TestMeasureDispatch_WrapsInnerExecutor` | `MeasureDispatch(fakeExec, tIDFn)`; `.Run(ctx, task)` returns `fakeExec`'s result verbatim, one span row emitted with `kind = task_dispatch`. | §3.3.B |
| 19 | `TestMeasureDispatch_InnerReturnsError_StillEmitsSpan` | Inner exec returns `errors.New("boom")`; wrapper returns same error, capture writer got one row. | §3.3.B defer coverage |
| 20 | `TestMeasureDispatch_NilConvIDFn_UsesTaskID` | `MeasureDispatch(inner, nil)`; passed task with `ID = "conv-task9876"`; row's `conversation_id == "conv-task9876"`. | §3.3.B default |
| 21 | `TestMeasureDispatch_ConvIDFnReturnsInvalid_SpanSkippedNoError` | Fn returns `"bad id"`; inner runs and returns nil; wrapper returns nil (no error propagation); zero rows, `probe_id_rejected_total` bumped. | Security (c) |
| 22 | `TestMeasureDispatch_SignatureNoTimeArg` | Reflect check on wrapper's `Run` and constructor `MeasureDispatch`; no `time.Time` params or fields. | Security (b) |

### Microbench common (`tools/eval/microbench/common/*_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 23 | `TestRunBench_RejectsShortWarmup` | `cfg.Warmup = 99` → `RunBench` returns `ErrWarmupTooShort`, step closure invoked 0 times. | Security (a) |
| 24 | `TestRunBench_WarmupSamplesExcluded` | `Warmup=100, Samples=1`, step returns `Sample{DurationNs: iteration_index * 1e6}`; asserts `samples[0].DurationNs == 100_000_000`. | Security (a) |
| 25 | `TestRunBench_DeterministicUnderSeed` | Two `RunBench(cfg, step)` calls with identical `cfg.Seed` yield byte-identical `[]Sample`. | Security (a) same-subject rule |
| 26 | `TestP50P95_TableDriven` | Golden cases: empty slice → 0 + log line; single sample; 100 samples where p50=50th value, p95=95th (nearest-rank). | §5.1 percentile helper |
| 27 | `TestRunBench_StepErrorPropagates` | Step returns `errors.New("x")` at iter 50 (post-warmup); `RunBench` returns that error with iter number in message; partial samples slice discarded. | §5.1 error surface |
| 28 | `TestRunBench_MinSamplesEnforced` | `cfg.Samples = 499` → returns `ErrSamplesTooShort`; 500 accepted. | §5.1 floor |
| 29 | `TestCSVSink_EscapesInjection` | Six-case table: cells starting with `=`, `+`, `-`, `@`, `\t`, `\r` all get `'` prefix in the written row. | Security (f) |
| 30 | `TestCSVSink_LeavesBenignCellsUnchanged` | Cells starting with `a`, `1`, `_`, `.`, `#` are written verbatim; cells containing `,` / `"` / `\n` follow RFC 4180 quoting. Also asserts `common/testdata/csv_golden.csv` byte-matches expected output. | Security (f) |
| 31 | `TestValidConversationID_TableDriven` | Positive: `"abcdefgh"` (min), `"a".repeat(128)` (max), `"conv_abc-123"`. Negative: 7-char, 129-char, `""`, `"conv abc"`, `"conv;abc"`, `"conv.abc"`, `"conv/abc"`. | Security (c) |
| 32 | `TestClock_NowHasMonotonicReading` | `now := common.now(); reflect.ValueOf(now).FieldByName("ext")` inspection is fragile; instead `now.Round(0)` differs from raw `now` iff monotonic reading is present (Go stdlib documented behavior). Table-driven. | Security (b) |

### Tunnel bench (`tools/eval/microbench/tunnel_bench/main_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 33 | `TestTunnelBench_DryRun_NoNetworkNoDisk` | `main(["--dry-run"])` exits 0; hooked `net.Listen` counter is 0; `t.TempDir()` byte-total unchanged before/after. | §5.2 dry-run + Security (d) |
| 34 | `TestTunnelBench_NoDiskWrites_100MiB` | Full non-dry run against `httptest.NewServer` at size=100 MiB, warmup=100, samples=500; `sumTreeBytes(t.TempDir())` before == after (0). Uses `TMPDIR=t.TempDir()`. | Security (d) |
| 35 | `TestTunnelBench_LatencyPerf_HasCIGuard` | Static analysis: `go/parser` scan of `main_test.go` asserts the function body of any `Test.*(Latency|Throughput|Perf).*` matches regex `testing\.Short\(\)` OR `os\.Getenv\("CI"\)`. Catches drift. | Security (h) |
| 36 | `TestTunnelBench_WarmupBelow100Rejected` | `main(["--warmup", "99"])` exits non-zero with stderr containing `warmup must be ≥ 100`. | Security (a) CLI floor |
| 37 | `TestTunnelBench_SeedThreaded` | Two runs with `--seed 42 --dry-run --print-plan` produce byte-identical stdout; two runs with different seeds differ in the payload-permutation section only. | Security (a) same-subject |

### Observer bench (`tools/eval/microbench/observer_bench/main_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 38 | `TestObserverBench_TwoRounds_NoObserverOnOff` | Full run with `--tmp-sqlite t.TempDir()/o.db`; parses stdout CSV; asserts both a "NoObserver=true" and "NoObserver=false" row exist and `off_p50_ns > on_p50_ns` (correctness gate: writer cost is > 0, not a perf bound). | §5.2 |
| 39 | `TestObserverBench_DryRun_NoSQLiteOpen` | `--dry-run` exits 0; hooked `os.Create` counter is 0; no `.db` file in `t.TempDir()`. | §5.2 |
| 40 | `TestObserverBench_ReadsAblationRegistry` | Test injects `ablation.CurrentBool = func(k) bool { return k == observerNoObserverKey }`; asserts bench sees `NoObserver = true` on the matching round. | §5.2 registry-only |

### Proxy bench (`tools/eval/microbench/proxy_bench/main_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 41 | `TestProxyBench_LocalHttpstestOnly` | Default flags start two `httptest.NewServer`s; no outbound socket; test wraps `net.Dial` with an assertion that `RemoteAddr().String()` starts with `127.0.0.1`. | §5.2 + Security (b) safety |
| 42 | `TestProxyBench_DryRun_PrintsURLPair` | `--dry-run` stdout contains `proxy_url=http://127.0.0.1:` and `direct_url=http://127.0.0.1:`; exits 0. | §5.2 |
| 43 | `TestProxyBench_SamePromptBothSides` | Both round trips receive the same prompt bytes (asserted via a request-body capture in the fake server); if the prompt differs, comparison is invalid. | Security (a) same-subject |

### Scale sweep (`tools/eval/scale_sweep/*_test.go`)

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 44 | `TestExpand_DefaultCount` | `Expand(ScaleAxes{})` → 45 points (5 × 3 × 3). | §5.3 |
| 45 | `TestExpand_Lex_Order` | Result is stable across runs (lexicographic sort). | §5.3 |
| 46 | `TestExpand_DefaultAxes_Match08E5ScalePoints` | Assert `ScaleAxes{}.Contexts == []int{1,2,4,8,16}`, `ScaleAxes{}.ToolsPerContext == []int{10,50,100}`, and `ScaleAxes{}.ArtifactSizes == []int64{1<<10, 1<<20, 100<<20}` — the exact §E5 axis triple, not just the count or the 100 MiB tail. | §E5 scale points (08 号 lines 213–217) |
| 47 | `TestSweep_DryRun_NoExec` | `sweep --dry-run --contexts 1,2 --tools-per-context 10 --artifact-size 1KiB` exits 0; hooks `exec.CommandContext` — assert 0 invocations. | Security (h) CI-safe smoke |
| 48 | `TestSweep_CSVHeader_MatchesSpec` | First line of `--out` file matches §4.6 header byte-for-byte. | §4.6 |
| 49 | `TestSweep_FromFixture_GoldenDiff` | `sweep --from-fixture testdata/fixture.sqlite --out /tmp/out.csv --dry-bench` (skips bench, reads probe rows + route_reasons from fixture SQLite); `diff -u testdata/fixture.golden.csv /tmp/out.csv` is empty. | §7 acceptance |
| 50 | `TestSweep_CIFullMatrixRefused` | `CI=1 sweep --contexts 1,2,4,8,16 ...` (no `--force-full`) exits non-zero with `refusing to run full matrix in CI without --force-full`. | Security (h) |
| 51 | `TestSweep_SentinelConvID_ValidatesAtStartup` | Test injects an axis value that yields a >128-char sentinel; sweep exits non-zero with `sentinel conversation_id fails ValidConversationID`. | Security (c) startup gate |
| 52 | `TestSweep_RoutingLatencyFromRouteReasons` | Fixture contains 100 `route_reasons` rows with `decision_duration_ns` = 1..100 ms; assert output CSV `routing_latency_p50_ns == 50_000_000` and `routing_latency_p95_ns == 95_000_000`. Uses in-memory Go percentile — proves this WT does NOT re-implement dispatch. | §4.4 no-dispatch-injection |

### Cross-cutting

| # | Test | Verifies | Anchor |
|---|---|---|---|
| 53 | `TestDispatchDirUntouched` | `git diff --name-only origin/paper/v3-integration -- internal/dispatch/` executed via `os/exec` returns empty stdout. **This test uses `testing.Short() || os.Getenv("CI") != ""` to skip only if `git` is unavailable — it runs in CI too when a `git` binary exists.** | §2 file-domain guard |
| 54 | `TestFanoutDiffAtMost3Lines` | `git diff --stat origin/paper/v3-integration -- internal/orchestrator/fanout.go` returns `+3 -0` or smaller. Skips only when `git` unavailable. | Global constraint |
| 55 | `TestPerfTestsUseCIGuard` | Package-level `go/parser` walk over `microbench/`/`scale_sweep/` `_test.go`; every `Test.*(Perf|Latency|Throughput).*` function body contains the literal `testing.Short()` OR `os.Getenv("CI")`. Fails on drift. | Security (h) |
| 56 | `TestCSVGolden_MatchesMetricExtractGolden` | Documented as an INTEGRATION-MERGE audit in spec §8; NOT run in this WT's `go test`. This row exists in the matrix so reviewers see the follow-up gate is on the checklist. | Security (f) cross-WT |

**Total: 55 executable + 1 documented merge-time audit.** The distribution:
Security (a) → 4 tests (23, 24, 25, 36); Security (b) → 4 tests (6, 17, 22,
32); Security (c) → 5 tests (3, 8, 16, 21, 31, 51); Security (d) → 2 tests
(33, 34); Security (e) → 2 tests (8, 9); Security (f) → 2 tests (29, 30) + 1
merge audit (56); Security (g) covered by §4.5 SELECT + Test 49 golden;
Security (h) → 5 tests (35, 47, 50, 55, 15).

## Task order (each task is one commit)

### Task 1 — DDL + writer package (probe_events_writer.go + schema.sql)

**Files created**: `internal/observerstore/probe_events_writer.go`,
`internal/observerstore/probe_events_writer_test.go`. **Modified**:
`internal/observerstore/schema.sql` (append only). Tests: #1–#12.

TDD steps:

- [ ] 1.1 Write `probe_events_writer_test.go` skeleton with `TestOpenSQLite_HasProbeEventsTable` (RED: table missing).
- [ ] 1.2 Append DDL to `schema.sql`; re-run: GREEN.
- [ ] 1.3 Write `TestNoCallerTimestampAtBoundary` (RED: no types exist).
- [ ] 1.4 Author minimal `Kind`, `ProbeSpanHandle`, `ProbeEventWriter`, `NewProbeEventWriter` — all timestamps unexported. GREEN.
- [ ] 1.5 Write validation tests (#2, #3, #7, #8, #9). GREEN one-by-one.
- [ ] 1.6 Write `TestSpanEndToEnd_RoundTrip`, `TestInsertRow_ONCONFLICT_DoNothing`, `TestDeriveEventID_UniquePerCall`. GREEN.
- [ ] 1.7 Write `TestSetProbeWriter_AtomicValueNoPanic`, `TestIsNoopProbeWriter_TrueByDefault`. Add `atomic.Value` writer + `SetProbeWriter` glue. GREEN.
- [ ] 1.8 `go test ./internal/observerstore/... -race -shuffle=on -count=1` + `go vet ./...`. Commit.

### Task 2 — Orchestrator planning timing

**Files**: `internal/orchestrator/planning_timing.go`,
`internal/orchestrator/planning_timing_test.go`; **modify**
`internal/orchestrator/fanout.go` (1 hunk).

- [ ] 2.1 Write tests #13, #17 (RED — file missing).
- [ ] 2.2 Author `MeasurePlanning[T]` + package `SetPlanningProbeWriter`/`IsPlanningProbeWriterNoop` glue that binds an `observerstore.ProbeEventWriter`. GREEN.
- [ ] 2.3 Write tests #14, #15, #16. GREEN.
- [ ] 2.4 Edit `fanout.go`: wrap `plan, err := planWithProgress(...)` in `MeasurePlanning`. Ensure diff is ≤ 3 lines.
- [ ] 2.5 Write `TestFanoutDiffAtMost3Lines` (#54). Run: must pass.
- [ ] 2.6 `go test ./internal/orchestrator/... ./internal/observerstore/... -race -shuffle=on`. Commit.

### Task 3 — Executor dispatch timing

**Files**: `internal/executor/task_dispatch_timing.go` +
`_test.go`. **NO edits to any existing executor `.go` file.**

- [ ] 3.1 Tests #18, #22 (RED). Author `MeasureDispatch`. GREEN.
- [ ] 3.2 Tests #19, #20, #21. GREEN.
- [ ] 3.3 `git diff --stat` on `internal/executor/` shows only the two new files.
- [ ] 3.4 `go test ./internal/executor/... -race -shuffle=on`. Commit.

### Task 4 — Microbench common

**Files**: `tools/eval/microbench/common/{warmup,csvsink,idvalid,clock}.go`
+ tests + `testdata/csv_golden.csv`.

- [ ] 4.1 Write `warmup_test.go` #23–#28. RED. Author `BenchConfig`, `RunBench`, `P50`, `P95`, `ErrWarmupTooShort`, `ErrSamplesTooShort`. GREEN.
- [ ] 4.2 `csvsink_test.go` #29, #30 + golden fixture. RED → GREEN.
- [ ] 4.3 `idvalid_test.go` #31. RED → GREEN.
- [ ] 4.4 `clock_test.go` #32. RED → GREEN.
- [ ] 4.5 `go test ./tools/eval/microbench/common/...`. Commit.

### Task 5 — Tunnel bench

**Files**: `tools/eval/microbench/tunnel_bench/{main,main_test}.go`.

- [ ] 5.1 Tests #33, #34, #36, #37 RED. Author `main.go` with in-memory reader (`bytes.NewReader` of a `[100<<20]byte` pre-allocated only for the 100 MiB point; sub-100-MiB reuses a bounded pool). GREEN.
- [ ] 5.2 `TestTunnelBench_LatencyPerf_HasCIGuard` (#35). GREEN by construction.
- [ ] 5.3 Manual smoke: `go run ./tools/eval/microbench/tunnel_bench --dry-run`.
- [ ] 5.4 `go test` + commit.

### Task 6 — Observer bench + Proxy bench

**Files**: `observer_bench/{main,main_test}.go`,
`proxy_bench/{main,main_test}.go`.

- [ ] 6.1 Observer bench tests #38–#40. Author main. GREEN.
- [ ] 6.2 Proxy bench tests #41–#43. Author main. GREEN.
- [ ] 6.3 Manual smoke: `--dry-run` on both.
- [ ] 6.4 Commit.

### Task 7 — Scale sweep

**Files**: `tools/eval/scale_sweep/{plan,plan_test}.go`,
`tools/eval/scale_sweep/sweep/{main,main_test}.go`, fixture assets.

- [ ] 7.1 `plan_test.go` #44–#46 RED. Author `Expand`. GREEN.
- [ ] 7.2 Author `build_fixture.go` (a `main` gated by `//go:build ignore` build tag). Run it once by hand:  `go run -tags ignore ./tools/eval/scale_sweep/testdata/build_fixture.go` → produces `fixture.sqlite` and `fixture.golden.csv` (both committed).
- [ ] 7.3 Sweep tests #47–#52 RED. Author `sweep/main.go`. GREEN.
- [ ] 7.4 `go run ./tools/eval/scale_sweep/sweep --contexts 1,2 --tools-per-context 10 --artifact-size 1KiB --dry-run`. Manual smoke.
- [ ] 7.5 Commit.

### Task 8 — Cross-cutting guards + final acceptance

**Files**: `internal/observerstore/probe_events_writer_test.go` (append
#53), `tools/eval/microbench/perftests_guard_test.go` (#55).

- [ ] 8.1 Add #53 (`TestDispatchDirUntouched`) — run: must pass (`git diff` empty).
- [ ] 8.2 Add #55 (`TestPerfTestsUseCIGuard`) walking `microbench/`/`scale_sweep/` — must pass.
- [ ] 8.3 Full suite: `go test ./tools/eval/microbench/... ./tools/eval/scale_sweep/... ./internal/orchestrator/... ./internal/executor/... ./internal/observerstore/... -count=1 -shuffle=on -race`.
- [ ] 8.4 `go vet ./...`; `gofmt -l` empty for the six spec-cited paths.
- [ ] 8.5 Manual invariants:
  - `git diff origin/paper/v3-integration -- internal/dispatch/` → empty.
  - `git diff --stat origin/paper/v3-integration -- internal/orchestrator/fanout.go` → `+3 -0` or smaller.
  - `CI=1 ./bin/tunnel_bench --dry-run` → exit 0.
  - `CI=1 ./bin/sweep --contexts 1,2 --tools-per-context 10 --artifact-size 1KiB --dry-run` → exit 0.
- [ ] 8.6 Commit + `git log --oneline` shows 8 commits with the `Co-Authored-By` trailer.

## Follow-ups (out of scope for this WT)

- **cmd-side wiring** for the three writers (documented in spec §3.2 and
  §8). The `IsNoopProbeWriter()` helpers exported here are the seam.
- **postgres schema.sql mirror** of `probe_events` (spec §8).
- **csvsink cross-worktree byte-golden** at Phase 2 integration merge time
  (spec §8; test #56 in this plan is the audit anchor).
