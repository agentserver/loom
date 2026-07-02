# WT-2-metric-extract — Spec

> Scope: a new Python package `multi-agent/tools/eval/metrics/` (module
> `eval_metrics`) that reads an observer SQLite DB — primarily the
> `runs` table (WT-1-run-schema, PR #56) plus companion tables
> (`route_reasons`, `capability_snapshots{,_usages}`, `task_contracts`,
> `writes`, `events`) — and emits one paper-metric row (CSV single row
> or JSON single object; **not JSON-Lines** — see §3.3) per `runs`-row
> selection. Consumers: 08 号 Figures 1–4 / Tables 1–3
> (`docs/intermediate/08_evaluation_plan_v3.md`) and Phase 3 paper
> back-fill.
>
> Out of scope: no Go code changes (**this worktree touches only
> `multi-agent/tools/eval/metrics/`**); no new observer tables; no
> upstream data source implementation — every metric this spec lists is
> emitted from data sources that **already** landed (12 号 §D1 / §A / §C /
> Phase 1 close-out memo §4), and metrics whose data source has NOT yet
> landed are still emitted as columns but populated with `null` and a
> `"upstream data missing"` note (see §7 (g)).

## 1. Background

Phase 1 landed the per-run D1 schema (`internal/evalrun/{schema,writer}.go`
+ `cmd/evalrun-export/` + observer `runs` table, PR #56) and three
companion trace tables (`route_reasons` PR #55, `capability_snapshots`
+ `capability_snapshot_usages` PR #61). `cmd/evalrun-export`, however,
only serialises the raw 24-column `runs` rows — it does not compute any
metric. The 08 号 evaluation tables (see the exhaustive per-experiment
metric list at 08:36–92, restated in §2 below) require aggregations
across `runs` and joins onto `route_reasons` / `task_contracts` /
`writes` / `events`. Phase 1 close-out memo §5
(`docs/intermediate/14_phase1_closeout.md`) explicitly hands this
extraction work to WT-2-metric-extract.

12 号 §D2 (`docs/intermediate/12_loom_development_tasks_for_v3.md` line
99) is the authoritative task-line:

> **Metric 提取脚本**：observer trace → 论文表里**全部 metric** 的
> CSV/JSON（包括 §A/B/C/D 列出的所有具名 metric）| E1–E6 | 🔴 |
> P0 | `tools/eval/metrics/*` Python

The word "全部 metric" is load-bearing. §2 below enumerates every named
metric across 12 号 §A/§B/§C/§D and 08 号 §Lifecycle / §Semantic /
§Contracted / §User-promoted / §Overhead — 36 metrics in total — and
the extractor MUST emit a header column for each, even when the
underlying data source has not yet been instrumented.

## 2. Metric catalog (authoritative — 39 metrics)

Order is preserved in CSV headers and JSON `metrics`-object keys;
renaming or reordering this list is a spec change, not an implementation
change. `null` handling per §7 (f). "Provenance" cites the observer
table/column or fixture-event type the extractor reads.

Cross-check anchor: this catalog is the union of every metric name
appearing in 08 号 §Metrics tables (08:32–92), 08 号 §Experiments
metrics-lines (E1: 08:116, E2: 08:141, E3: 08:163, E4: 08:198, E5:
08:219, E6: 08:237), and 12 号 §A/§B/§C/§D metric one-liners (12:41,
12:44, 12:74, 12:87, 12:89, 12:105, 12:106, 12:107). If a future
audit finds a named metric not in this catalog, that is a P0 spec bug.

### 2.1 Lifecycle (9 — from 12 号 §D8 / §A5 + 08 号 §Lifecycle / E1 / E3)

| # | Metric | Numerator | Denominator | Provenance |
|---|---|---|---|---|
| 1 | `TaskSuccessRate` | `count(runs where success_oracle_result = 'pass')` | `count(runs in selection)` | `runs.success_oracle_result` |
| 2 | `LifecycleClosureRate` | `count(runs where success_oracle_result = 'pass' AND task_contract_hash != '' AND artifact_hashes != '[]' AND observer_trace_path != '')` | `count(runs in selection)` | `runs.{success_oracle_result, task_contract_hash, artifact_hashes, observer_trace_path}` |
| 3 | `TimeToCompletion` | reported as a **structured object** with keys `p50_seconds`, `p95_seconds`, `mean_seconds`, `count` — computed from `(end_time − start_time)` across the selection; excludes rows where either timestamp is empty. | — | `runs.{start_time, end_time}` |
| 4 | `HumanContextSelectionCount` | `sum(runs.human_intervention_count)` | — | `runs.human_intervention_count` (WT-1-run-schema §D1) |
| 5 | `WrongContextFailureRate` | `count(runs where failure_category IN ('wrong-context','missing-file','wrong-version'))` | `count(runs in selection)` | `runs.failure_category` (11-value taxonomy per PR #61 / D4) |
| 6 | `ArtifactCorrectnessRate` | `count(runs where success_oracle_result = 'pass' AND artifact_hashes != '[]')` | `count(runs where artifact_hashes != '[]')` | `runs.{success_oracle_result, artifact_hashes}` (oracle-side truth deferred to 12号 §D8; see §7 (g)) |
| 7 | `ManualSetupStepCount` | `sum(runs.manual_setup_step_count)` if column present, else `null` + `"upstream data missing"` | — | 12号 §D8 owns the writer; not yet landed → emit `null` per §7 (g) |
| 8 | `ConfigTouchCount` | `sum(runs.config_touch_count)` if column present, else `null` + `"upstream data missing"` | — | same as above |
| 9 | `StateContinuityRate` | `count(runs where success_oracle_result = 'pass' AND artifact_hashes != '[]' AND task_contract_hash != '' AND observer_trace_path != '')` restricted to runs whose `failure_category` was `slave-disconnect` OR `driver-restart` at any point in their lifecycle (join to `events` table) | `count(runs where the run was interrupted at least once)` — proxy today: `count(runs where failure_category IN ('slave-disconnect','driver-restart') OR the run's task appears in >1 events with status='resumed')` | Named in 08:39 + 12:44 (§A5); today observer has no `resumed` event type → emit `null` + `"upstream data missing"` per §7 (g) until 12号 §A6 lands the write_id dedup + resume audit. |

**Note on data source status.** Columns `manual_setup_step_count` and
`config_touch_count` are 08号 §Overhead / §E6 fields; they are NOT in
the 24-column `runs` DDL landed by PR #56 (see `runs` DDL in
`multi-agent/internal/observerstore/schema.sql:194-220`). The extractor
therefore emits them as `null` today; when 12号 §D8 / §D6c adds the
columns, no spec change is needed — the extractor SHALL detect column
presence via `PRAGMA table_info(runs)` and switch from `null` to `sum(...)`.
`StateContinuityRate` is 08:39 / 12:44 (owner: 12号 §A5 driver task
journal, ALREADY landed; but the eval-runner-side join to `events`
for interruption detection is 12号 §A6 P1, not yet landed).

### 2.2 Contracted (7 — from 12 号 §A + 08 号 §Contracted / E3) — metrics #10..#16

| # | Metric | Numerator | Denominator | Provenance |
|---|---|---|---|---|
| 10 | `ContractCompleteness` | `sum(present_field_count)` over all contracts | **fixed constant `7 × count(task_contracts in selection)`** — the 7 lifecycle fields per 12号 §A2; **NOT 08号's 8-field count** (§A2 pins the denominator). | `task_contracts.body` — parsed to count the 7 lifecycle fields (intent.goal / intent.success_criteria / data_contract.read_artifacts / data_contract.write_targets / capability_requirements / execution_policy / recovery_hint) per `internal/contract/completeness.go:36-44` |
| 11 | `PreExecutionFaultCatchRate` | `count(runs where baseline_or_ablation != 'NoDryRun' AND failure_category = 'policy-violation')` | `count(runs where run was fault-injected)` — proxy: `count(runs where experiment_id = 'E3')`, since only E3 injects faults | `runs.{baseline_or_ablation, failure_category, experiment_id}` |
| 12 | `ContractViolationRate` | `count(runs where failure_category = 'contract-violation')` | `count(runs where task_contract_hash != '')` | `runs.{failure_category, task_contract_hash}` |
| 13 | `MissingArtifactDetectionRate` | `count(runs where failure_category = 'missing-file' AND success_oracle_result != 'pass')` | `count(runs where experiment_id = 'E3')` | `runs.{failure_category, success_oracle_result, experiment_id}` |
| 14 | `PolicyViolationPreventionRate` | data source not landed (12号 §A3 P1, not this worktree) → `null` + `"upstream data missing"` | — | Requires `dry_run_blocks` table (12号 §A3); not yet in observer schema. §7 (g). |
| 15 | `RecoverySuccessRate` | `count(runs where failure_category IN ('slave-disconnect','driver-restart','timeout') AND success_oracle_result = 'pass')` | `count(runs where failure_category IN ('slave-disconnect','driver-restart','timeout'))` | `runs.{failure_category, success_oracle_result}` |
| 16 | `DuplicateSideEffectRate` | data source not landed (12号 §A6 P1, write_id dedup table missing) → `null` + `"upstream data missing"` | — | Requires observer `write_id` dedup table (12号 §A6). §7 (g). |

### 2.3 User-promoted (11 — from 12 号 §B + 08 号 §User-promoted / E4) — metrics #17..#27

All 11 emit `null` + `"upstream data missing"` at spec time — the
promotion-chain writers (12号 §B1/B2/B4/B6, WT-2-driver-promotion-chain
worktree) have not landed. The columns are still emitted so downstream
pandas readers do not silently drop schema when B chain merges. Numerator
/ denominator strings are recorded now so back-fill is a one-line change.

| # | Metric | Numerator / denominator | Data source (once landed) |
|---|---|---|---|
| 17 | `PromotionCandidateSurfacingRate` | `count(events where type='promote_candidate') / count(runs where workload requires promotion)` | 12号 §B1 event stream |
| 18 | `UserInitiatedSynthesisSuccessRate` | `count(events where type='register_slave_mcp' AND acceptance='pass') / count(events where type='user_scaffold_start')` | 12号 §B2 |
| 19 | `ValidationFalseAcceptRate` | `count(runs where acceptance_result='pass' AND oracle_result='fail') / count(runs where acceptance_result='pass')` | 12号 §B3 (`mcp-acceptance --cases`; PR #57 landed the golden, not the observer event) |
| 20 | `TimeFromUserDecisionToRegisteredMCP` | structured `{p50_seconds,p95_seconds,mean_seconds,count}` over `(register_ts − user_decision_ts)` per B6 audit row | 12号 §B6 |
| 21 | `RegistryLookupHitRate` | `count(events where type='registry_lookup' AND result='hit') / count(events where type='registry_lookup')` | 12号 §B4 |
| 22 | `CapabilityReuseRate` | `count(runs where reused_mcp_hash != '') / count(runs in same capability family)` | 12号 §B4 + `runs.dynamic_mcp_registry_hash` |
| 23 | `RepeatedGenerationRate` | `count(events where type='user_scaffold_start' with existing valid MCP in registry) / count(repeated capability-family tasks)` | 12号 §B4 |
| 24 | `PromotionAdoptionRate` | `count(events where type='register_slave_mcp') / count(events where type='promote_candidate')` | 12号 §B1+§B2 |
| 25 | `AdHocScriptTaskShare` | `count(runs where no promotion event fired) / count(runs)` | 12号 §B1 (absence signal) |
| 26 | `GeneratedCapabilityDefectRate` | `count(runs where success_oracle_result='fail' AND selected_capability_source='user-promoted') / count(runs where selected_capability_source='user-promoted')` | 12号 §B4 + oracle |
| 27 | `ReuseSpeedup` | `avg(TimeToCompletion for stage='A') / avg(TimeToCompletion for stage='C')` over same capability family | 12号 §B (family + stage columns not yet in `runs`) |

### 2.4 Semantic (3 — from 12 号 §C2 + 08 号 §Semantic / E2) — metrics #28..#30

| # | Metric | Numerator | Denominator | Provenance |
|---|---|---|---|---|
| 28 | `RoutingAccuracy` | `count(runs where selected_context = ground_truth_context AND ground_truth_context != '')` | `count(runs where ground_truth_context != '')` — non-empty gates ground-truth availability | `runs.{selected_context, ground_truth_context}` |
| 29 | `CapabilityRecall` | data source: capability snapshot + ground-truth (12号 §F4) — landed for snapshot (PR #61), NOT for ground-truth requirement labels → `null` + `"upstream data missing"` | — | §7 (g) |
| 30 | `CapabilityPrecision` | data source: capability smoke tests — no observer schema field yet → `null` + `"upstream data missing"` | — | §7 (g) |

### 2.5 Overhead (9 — from 12 号 §D7 / §C4 / §D6c + 08 号 §Overhead / E5 / E6)

The `route_reasons` table (PR #55) supplies `RoutingLatencyP50P95`
directly; the other overhead metrics require probe writers that 12号
§D7 (WT-2-overhead-probes worktree) or 12号 §D6c / §C4 own. The
extractor emits all 9 columns; only #37 is populated today.

| # | Metric | Numerator / value | Denominator | Provenance |
|---|---|---|---|---|
| 31 | `DriverPlanningOverhead` | data source not landed (12号 §D7) → `null` | — | §7 (g) |
| 32 | `TaskDispatchLatency` | data source not landed (12号 §D7) → `null` | — | §7 (g) |
| 33 | `TunnelOverhead` | data source not landed (12号 §D7) → `null` | — | §7 (g) |
| 34 | `ArtifactTransferThroughput` | data source not landed (12号 §D7) → `null` | — | §7 (g) |
| 35 | `ObserverOverhead` | data source not landed (12号 §D7) → `null` | — | §7 (g) |
| 36 | `ModelProxyOverhead` | data source not landed (12号 §D7) → `null` | — | §7 (g) |
| 37 | `RoutingLatencyP50P95` | structured `{p50_ns, p95_ns, count}` over `route_reasons.decision_duration_ns` where the row's `decision_started_at` falls within `[runs.start_time, runs.end_time]` for at least one run in the selection (there is NO direct FK between `runs` and `route_reasons` — the join is on the time window, since `runs` has no `conversation_id` column per the 24-column DDL) | — | `route_reasons.decision_duration_ns` + `route_reasons.decision_started_at` (PR #55); `runs.{start_time, end_time}` |
| 38 | `TimeToFirstTask` | data source not landed (12号 §C4 / §D6c) → `null` | — | Named in 08:90 + 08:237 + 12:89 + 12:105; requires deploy-time timestamp not currently in schema. §7 (g). |
| 39 | `SetupFailureRate` | data source not landed (12号 §C4 / §D6c) → `null` | — | Named in 08:237 + 12:89 + 12:105; requires deploy harness event stream not currently in schema. §7 (g). |

## 3. CLI

Single subcommand `extract`; the top-level command exists so future
`report` / `sanity` subcommands can slot in without breaking flag
parsing.

```
eval-metrics extract \
  --observer-db <path>            # required; SQLite file (see §7 (a)–(b))
  --runs-filter '<sql-fragment>'  # optional; WHERE fragment referencing whitelisted columns only (§7 (c))
  --format csv|json               # required; no default — silent-typo hazard
  --out <path>                    # optional; default stdout (§7 (d))
  --metric-set full|lifecycle|contracted|user-promoted|semantic|overhead   # default full (§7 (e))
```

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Success |
| 2 | Any input-validation rejection (§7 (b)–(e)) or DB open failure |
| 3 | I/O failure writing `--out` |

`stderr` is used for warnings (missing upstream data source, per §7 (g))
and error messages. `stdout` is reserved for CSV/JSON output when
`--out` is omitted.

### 3.1 `--runs-filter` semantics

`--runs-filter` is a **WHERE-clause fragment** — the extractor prepends
`WHERE ` (or `AND ` after its own filter) and passes it to sqlite3's
prepared-statement compiler. **No user text is ever string-concatenated
into SQL** (§7 (c)). Only the columns in the whitelist below may be
referenced; any other identifier → exit 2 `ErrRunsFilterFieldNotAllowed`.

Whitelist (5 columns, matching 12号 §D2 filter axes):

- `run_id`, `workload_id`, `claim_id`, `experiment_id`, `baseline_or_ablation`

The parser accepts equality (`col = 'lit'` / `col = ?`), `IN (...)`,
`LIKE 'pat'`, and their `AND`/`OR` combinations. `;`, comments (`--`,
`/*`), subqueries (`SELECT`, `WITH`), and DDL/DML keywords
(`UPDATE`/`DELETE`/`INSERT`/`DROP`/`ALTER`/`ATTACH`/`PRAGMA`) trigger
rejection before parse — belt-and-suspenders against sqlparse edge
cases.

### 3.2 `--metric-set` selection

`full` emits all 36 columns from §2. The five subset values narrow to
§2.1 / §2.2 / §2.3 / §2.4 / §2.5 respectively. Any other value → exit
2 with the message `unknown metric set: <val> (allowed: full, lifecycle, contracted, user-promoted, semantic, overhead)` (§7 (e)).

### 3.3 Output shape

**Aggregation model.** The extractor produces **one output "record" per
invocation** (not per `runs` row), because the paper metrics are
cohort-level ratios / percentiles. `--runs-filter` narrows the cohort;
a single invocation always produces exactly one record — including
when the cohort is empty (in which case count-metrics are 0 and
ratio-metrics are `null` per §7 (f)). To produce Table 2's per-baseline
breakdown, the caller invokes the extractor once per
`baseline_or_ablation` value and concatenates the resulting single-row
CSVs.

**CSV.** Row 1 is the header: column 1 = `metric_set`, column 2 =
`row_count` (the number of `runs` rows that matched the selection),
columns 3..N = metric names in §2 order. Structured metrics
(`TimeToCompletion`, `TimeFromUserDecisionToRegisteredMCP`,
`RoutingLatencyP50P95`) flatten into sub-columns using `<metric>.<key>`
naming (e.g. `TimeToCompletion.p50_seconds`). Row 2 is the single data
row for this invocation. There is NO third row.

The `_notes` companion is written as an extra final column named
`_notes` whose value is a semicolon-separated list of
`<metric>: <reason>` pairs, one per metric that returned `null` with
a `"upstream data missing"` reason (§7 (g)).

**JSON.** Single JSON object (not JSON-Lines) with keys:

- `metric_set`: the resolved value of `--metric-set`;
- `row_count`: number of `runs` rows in the selection (integer, may be 0);
- `metrics`: an object mapping metric-name → scalar or nested object;
- `notes`: a map from metric-name → status string, always emitted
  (even when empty `{}`), so downstream consumers can rely on the key
  being present.

**Empty-DB / zero-selection contract.** A cohort of zero `runs` rows
still produces a valid one-record output:

- CSV: header row + exactly one data row where `row_count = 0`,
  count-metrics (`HumanContextSelectionCount`, …) = `0`, ratio-metrics
  = empty cell (JSON `null`), structured metrics = empty flattened
  sub-columns. The acceptance criterion in §5.1 refers to this shape
  as "header + one data row with row_count=0"; earlier drafts said
  "zero data rows" — that phrasing was ambiguous and is corrected
  here.
- JSON: `{"metric_set": "full", "row_count": 0, "metrics": {...all
  count metrics: 0, all ratio/structured metrics: null}, "notes":
  {...one entry per metric whose upstream is missing}}`.

## 4. Golden fixture traces

Three self-contained SQLite fixtures live in
`multi-agent/tools/eval/metrics/tests/fixtures/`; each is built by a
`build_fixture_<N>.py` helper checked in beside the resulting `.db` so
the DB can be rebuilt deterministically. Each fixture contains one
`runs`-DDL-only DB plus 5–10 rows across `runs` / `route_reasons` /
`task_contracts` (whichever the fixture exercises). Hand-computed
expected values live in `expected_<N>.json` and are asserted directly
by pytest.

### 4.1 Fixture 1 — Lifecycle (§2.1)

10 runs, all `experiment_id='E1'`, `baseline_or_ablation='FullLoom'`.

| run # | success | end−start (s) | human_intervention_count | failure_category | artifact_hashes | task_contract_hash | observer_trace_path |
|---|---|---|---|---|---|---|---|
| 1  | pass    | 12.0 | 0 | ''             | `["a1"]`       | `h1` | `/t/1` |
| 2  | pass    | 18.0 | 1 | ''             | `["a2"]`       | `h2` | `/t/2` |
| 3  | fail    | 30.0 | 2 | wrong-context  | `[]`           | `h3` | `/t/3` |
| 4  | pass    | 10.0 | 0 | ''             | `["a4"]`       | `h4` | `/t/4` |
| 5  | fail    | 60.0 | 3 | missing-file   | `[]`           | ''   | `/t/5` |
| 6  | pass    | 20.0 | 0 | ''             | `["a6","a6b"]` | `h6` | `/t/6` |
| 7  | timeout | 90.0 | 1 | timeout        | `[]`           | `h7` | `/t/7` |
| 8  | pass    | 14.0 | 0 | ''             | `["a8"]`       | `h8` | `/t/8` |
| 9  | fail    | 22.0 | 1 | wrong-version  | `[]`           | `h9` | `/t/9` |
| 10 | pass    | 16.0 | 0 | ''             | `["a10"]`      | `h10`| `/t/10` |

Hand-computed values:

- `TaskSuccessRate = 6/10 = 0.6`
- `LifecycleClosureRate = 6/10 = 0.6` (same 6 rows: pass AND contract_hash non-empty AND artifacts non-empty AND trace non-empty)
- `TimeToCompletion.mean_seconds = (12+18+30+10+60+20+90+14+22+16)/10 = 29.2`
- `TimeToCompletion.count = 10`
- `TimeToCompletion.p50_seconds = 19.0` (linear-interp median of the sorted list [10,12,14,16,18,20,22,30,60,90]; per numpy convention, midpoint of the two middle values 18 and 20)
- `TimeToCompletion.p95_seconds = 76.5` (linear-interp 95th percentile; between the two top values 60 and 90 at fractional index 8.55 → 60 + 0.55·(90-60) = 76.5)
- `HumanContextSelectionCount = 0+1+2+0+3+0+1+0+1+0 = 8`
- `WrongContextFailureRate = 3/10 = 0.3` (rows 3, 5, 9)
- `ArtifactCorrectnessRate = 6/6 = 1.0` (all 6 rows with non-empty artifacts also passed; denominator = 6)
- `ManualSetupStepCount = null` (column absent per §2.1 note)
- `ConfigTouchCount = null` (same)
- `StateContinuityRate = null` (upstream missing per §2.1 note — no
  `events.status='resumed'` schema yet)

### 4.2 Fixture 2 — Contracted (§2.2)

6 runs, `experiment_id='E3'`, mix of `baseline_or_ablation` values;
plus 6 `task_contracts` rows with the JSON bodies below.

| run # | success | failure_category | baseline_or_ablation | task_contract_hash |
|---|---|---|---|---|
| 1 | pass    | ''                  | FullLoom     | h1 |
| 2 | fail    | contract-violation  | FullLoom     | h2 |
| 3 | fail    | missing-file        | FullLoom     | h3 |
| 4 | pass    | ''                  | NoDryRun     | h4 |
| 5 | fail    | policy-violation    | FullLoom     | h5 |
| 6 | pass    | ''                  | NoTypedContracts | '' |

`task_contracts.body` field-presence bitmaps (7 lifecycle fields, per
12号 §A2):

| contract | intent.goal | intent.success_criteria | read_artifacts | write_targets | capability_requirements | execution_policy | recovery_hint | present_count |
|---|---|---|---|---|---|---|---|---|
| h1 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | 7 |
| h2 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✗ | 6 |
| h3 | ✓ | ✓ | ✓ | ✗ | ✓ | ✓ | ✓ | 6 |
| h4 | ✓ | ✓ | ✓ | ✓ | ✗ | ✓ | ✓ | 6 |
| h5 | ✓ | ✗ | ✓ | ✓ | ✓ | ✓ | ✓ | 6 |
| h6 | ✓ | ✓ | ✓ | ✓ | ✓ | ✗ | ✓ | 6 |

Hand-computed values:

- `ContractCompleteness = (7+6+6+6+6+6) / (7 * 6) = 37 / 42 ≈ 0.8809523809523809`
- `PreExecutionFaultCatchRate = 1 / 6 ≈ 0.16666666666666666` (numerator: 1 row where `baseline_or_ablation != 'NoDryRun' AND failure_category = 'policy-violation'`, i.e. row 5; denominator: 6 rows with `experiment_id='E3'`)
- `ContractViolationRate = 1 / 5 = 0.2` (numerator: 1 row with `failure_category='contract-violation'`, i.e. row 2; denominator: 5 rows with `task_contract_hash != ''`, i.e. rows 1–5)
- `MissingArtifactDetectionRate = 1 / 6 ≈ 0.16666666666666666` (row 3; denom = 6 E3 runs)
- `PolicyViolationPreventionRate = null` (§2.2 row 13, upstream missing)
- `RecoverySuccessRate = null` (no rows with failure_category IN slave-disconnect/driver-restart/timeout → denominator=0 → null per §7 (f))
- `DuplicateSideEffectRate = null` (§2.2 row 15, upstream missing)

### 4.3 Fixture 3 — Semantic + Overhead + User-promoted (§2.3–§2.5)

5 runs (`experiment_id='E2'`), 4 `route_reasons` rows, no
`task_contracts`. Establishes the two populated metrics
(`RoutingAccuracy`, `RoutingLatencyP50P95`) and asserts that all 11
user-promoted metrics + 5 overhead metrics + 2 semantic metrics emit as
`null` with the `"upstream data missing"` note.

| run # | selected_context | ground_truth_context |
|---|---|---|
| 1 | slave-A | slave-A |
| 2 | slave-A | slave-B |
| 3 | slave-B | slave-B |
| 4 | slave-A | slave-A |
| 5 | slave-C | ''        |

`route_reasons.decision_duration_ns` values keyed to the 5 runs'
conversation_ids: `[500_000, 1_000_000, 2_000_000, 4_000_000]` (only 4
decisions total; run #5 never dispatched).

Hand-computed values:

- `RoutingAccuracy = 3 / 4 = 0.75` (rows 1, 3, 4 match; row 2 is wrong; row 5 excluded — ground_truth empty)
- `CapabilityRecall = null` (§2.4 row 28)
- `CapabilityPrecision = null` (§2.4 row 29)
- `RoutingLatencyP50P95 = {p50_ns: 1_500_000, p95_ns: 3_700_000, count: 4}` (numpy linear-interp on sorted [500k, 1M, 2M, 4M]; median = midpoint of 1M and 2M = 1.5M; 95th percentile at fractional index 2.85 → 2M + 0.85·(4M−2M) = 3.7M)
- All 11 user-promoted metrics (#17..#27) + 6 non-routing E5 overhead
  metrics (#31..#36) + 2 E6 onboarding metrics (#38 `TimeToFirstTask`,
  #39 `SetupFailureRate`) = `null` with `"upstream data missing"`
  note. Also 2 semantic metrics (#29 `CapabilityRecall`, #30
  `CapabilityPrecision`), 3 contracted metrics if the fixture bothered
  to add contracts (which it does not — the fixture has no
  `task_contracts` rows, so #10 `ContractCompleteness` = `null` per
  denominator=0). Total assertions on `null` metrics in fixture 3:
  11 + 6 + 2 + 2 = **21 non-routing metrics assertion is `null`**,
  plus lifecycle-null metrics (`ManualSetupStepCount`,
  `ConfigTouchCount`, `StateContinuityRate`) = 3, and contract-null
  metrics inferred by empty `task_contracts` = 7 (all §2.2 rows
  either `null`-upstream or denominator=0). Fixture 3 exercises the
  complete `null` matrix.

### 4.4 Empty-DB fixture (implicit)

An in-memory DB with the WT-1-run-schema DDL applied but zero rows.
Expected output per §3.3 empty-DB contract:

- CSV: header row + **exactly one data row** with `metric_set=full`,
  `row_count=0`, all count metrics = `0`, all ratio and structured
  metrics = empty cells, `_notes` populated for every metric whose
  upstream is missing.
- JSON: `{"metric_set":"full","row_count":0,"metrics":{...all count
  metrics: 0, all ratio metrics: null...},"notes":{...upstream-missing
  entries...}}`.

Confirms §5 acceptance criterion 1.

## 5. Acceptance criteria

1. `eval-metrics extract --observer-db <empty.db> --format csv` prints
   a header line whose column set is `metric_set, row_count, <the 39
   metric names in §2 order, with structured metrics flattened to
   <metric>.<subkey> sub-columns>, _notes`, followed by exactly one
   data row where `row_count=0`, count-metrics are `0`, ratio and
   structured metric cells are empty, and `_notes` is populated per
   §3.3 empty-DB contract.
2. Each of the three golden fixtures produces the hand-computed values
   in §4 to full float64 precision — no rounding, no lossy formatting.
3. `--metric-set lifecycle` on fixture 1 emits only the §2.1 columns
   (plus flattened sub-columns of `TimeToCompletion`) plus the leading
   `metric_set` / `row_count` columns.
4. Every metric whose data source has NOT landed produces `null` in the
   value cell AND an entry in the `notes` map / stderr warning with the
   literal string `"upstream data missing"`.
5. All security items §7 (a)–(h) have at least one passing pytest test
   (matrix in the plan doc).

## 6. Non-goals

- No new Go code, no new observer tables, no changes to
  `internal/evalrun/schema.go`. If a metric requires a schema change,
  the extractor emits `null` today; a follow-up worktree adds the column
  and this extractor gains one line.
- No plotting / figure generation — that is Phase 3 paper back-fill,
  consuming this CSV.
- No streaming: the full selection is loaded into a pandas / stdlib
  buffer; upper bound on rows is ~10⁴ (one row per workload×baseline
  cell across E1–E6), which fits comfortably.
- No incremental / cache mode: every invocation re-reads `runs` from
  scratch. The DB is small (24 columns × 10⁴ rows ≈ single-digit MiB).

## 7. Security

The worst-case failure mode this section guards against is
**(c) SQL injection via `--runs-filter`** — an unescaped fragment such
as `--runs-filter "1=1; UPDATE runs SET success_oracle_result='pass'"`
would silently rewrite every row in the paper's evidence base. The
mitigation stack is defense-in-depth: read-only DB open (§a), prepared
statements only (§c), field whitelist parse (§c), plus SQL-keyword
denylist as a belt.

### (a) SQLite opened read-only

All DB connections go through a single helper that constructs a URI of
the form `file:<abs-path>?mode=ro&immutable=1` and passes
`uri=True` to `sqlite3.connect`. Immediately after connect, the helper
issues `PRAGMA query_only = ON;` — belt against a future change that
inverts the URI accidentally. Test: attempted `UPDATE runs SET ... `
raises `sqlite3.OperationalError` mentioning `attempt to write a
readonly database`.

The observer schema.sql is NEVER re-applied by this tool; the tool
assumes the DB was populated by observer / eval-runner. Applying DDL to
a read-only connection would fail loudly.

### (b) `--observer-db` path validation

Two checks, in order:

1. **Realpath scrub**: resolve with `pathlib.Path(...).expanduser().resolve(strict=True)`
   (strict=True forces existence — non-existent → immediate `FileNotFoundError`
   → exit 2). Reject if the resolved path descends from any of `/etc/`,
   `/proc/`, `/sys/`, `/dev/`. Sample-and-hold: the resolved path is
   the one opened; symlink swap between check and open is impossible
   because we pass the resolved string to `sqlite3.connect`, never the
   original.
2. **Magic-bytes probe**: open with `open(path, 'rb')`, read exactly 16
   bytes, verify `header == b'SQLite format 3\x00'` (the standard
   SQLite header per <https://www.sqlite.org/fileformat.html>). Reject
   otherwise. This runs BEFORE `sqlite3.connect` — sqlite3's own error
   on a non-DB file is a generic "file is not a database" that can be
   confused with permission errors; the explicit probe gives a clean
   exit-2 with `"not a SQLite database"`.

### (c) `--runs-filter` — prepared statements + field whitelist

Every DB read is a `cursor.execute(sql, params)` call — user text NEVER
enters the SQL string. `--runs-filter` is compiled through the
following pipeline:

1. **Denylist scan** (pre-parse): reject if the raw fragment contains
   any of the tokens `;`, `--`, `/*`, `*/`, `SELECT`, `WITH`, `UPDATE`,
   `DELETE`, `INSERT`, `DROP`, `ALTER`, `ATTACH`, `DETACH`, `PRAGMA`,
   `CREATE`, `REPLACE`, `TRIGGER`, `INDEX`, `TRANSACTION`, `BEGIN`,
   `COMMIT`, `ROLLBACK`, `VACUUM`, `LOAD_EXTENSION`. Case-insensitive.
   This is the belt — even if the AST parser (step 3) has a
   look-alike-Unicode bypass, this scan catches literal SQL.
2. **Parametric extraction**: replace every string literal (`'...'`)
   with a `?` placeholder and pull the literal out to a positional
   parameter list. Numeric literals stay inline (they cannot inject).
3. **AST parse** (using `sqlglot` — pure-Python, no C extension, no
   network I/O): parse the placeholder-substituted fragment as a
   standalone expression with the sqlite dialect. Walk every
   `Column` node in the AST; if any referenced column name is not in
   the whitelist below → exit 2 `ErrRunsFilterFieldNotAllowed`. If parse
   fails → exit 2 with the parse error.
4. **Wrap and bind**: prepend `WHERE ` and interpolate the sanitized
   AST-string into `SELECT ... FROM runs WHERE <sanitized>`; parameters
   from step 2 flow through to `cursor.execute(sql, params)` unchanged.

Column whitelist (5 columns, per §3.1): `run_id`, `workload_id`,
`claim_id`, `experiment_id`, `baseline_or_ablation`.

Rejected examples (all → exit 2):

- `1=1; UPDATE runs SET success_oracle_result='pass'` — caught by
  denylist (`;`, `UPDATE`).
- `run_id='x' OR EXISTS (SELECT 1 FROM sqlite_master)` — caught by
  denylist (`SELECT`) and by whitelist (`sqlite_master`).
- `secret_col = 'x'` — caught by whitelist (column `secret_col`).
- `run_id = 'x' -- ignore` — caught by denylist (`--`).

Accepted examples (all → prepared with `run_id = ?` binding):

- `run_id = 'run-abc'`
- `experiment_id IN ('E1','E3')`
- `baseline_or_ablation = 'FullLoom' AND workload_id LIKE 'code-mod-%'`

### (d) `--out` path validation + CSV formula-injection escape

`--out` path is validated by the same rules as `--observer-db` (§b,
sans magic-bytes check). Additionally: reject if parent directory does
not exist (do NOT `mkdir -p` — the tool has no business creating
directories under paths it did not choose). Refuse to overwrite by
default; `--out` may only point at a non-existent file or `/dev/stdout`
/ `/dev/null`.

CSV cells are escaped identically to `cmd/evalrun-export` (spec §7 (e)
of WT-1-run-schema, `main.go:283`): if the cell's first byte is one of
`=`, `+`, `-`, `@`, `\t`, `\r`, `\n`, prefix with `'`. This covers the
CWE-1236 formula-injection surface plus the two cell-content-shifter
control chars. JSON output is unaffected — reviewers never feed JSON
through a spreadsheet formula engine.

### (e) `--metric-set` enum enforcement

Value MUST be one of `{full, lifecycle, contracted, user-promoted,
semantic, overhead}`. Any other value → exit 2 immediately with the
allowed-list printed. **Do NOT default to `full` on unrecognized
input** — that would let a typo (`--metric-set liflecycle`) silently
emit the full 36-column set while the operator thinks they got a
subset, corrupting the paper's cohort attribution.

### (f) Denominator = 0 → `null` (never NaN / inf / 0)

Any metric whose denominator evaluates to 0 emits the JSON literal
`null` (CSV: empty cell). **Never** `NaN`, `inf`, or `0`. Rationale:

- `NaN` in CSV: Excel converts to `#NUM!`; pandas parses as `float NaN`
  but downstream `.astype(int)` silently coerces to 0; R's `read.csv`
  parses as `NA`; Julia's `CSV.jl` parses as `missing`. Cross-tool
  ambiguity around a paper's metric column is unacceptable.
- `0` is worse: a real "0% success rate" is a valid metric value; if
  denominator=0 also produced 0, a reviewer cannot distinguish "0 runs
  attempted" from "0 out of 40 succeeded". Both are meaningful, and
  they must be visibly different.
- Empty CSV cell + JSON `null` is the least-ambiguous choice: pandas
  reads as `NaN` (correctly flagged as missing), R as `NA`, jq as
  `null`. Reviewers of the CSV see an empty cell.

The `_notes` column / JSON `notes` map records which metrics returned
`null` and why. When the reason is `"upstream data missing"` (§g) the
metric's note is emitted whether the denominator is 0 or not — the
consumer needs to know they're seeing a null-by-schema, not
null-by-empty-cohort.

### (g) Consumer-view reverse audit

Every metric in §2 MUST have a fixture in §4 that either (a) computes a
non-null value the pytest matrix asserts on, or (b) is explicitly
recorded in the fixture-4 all-null contract. There is NO third category
"no fixture" — every metric name is exercised. When a metric returns
`null` because the underlying observer table/column has not been
populated yet, the extractor emits its column with `null` value AND
writes a one-line stderr warning of the form:

```
[eval-metrics] warn: metric <name> returned null: upstream data missing (owner: 12号 §<section>)
```

The `--out` file's companion `notes` map (JSON) or `_notes` companion
column (CSV) records the same. Reviewers of the CSV can eyeball the
`_notes` column; automated pipelines can parse the JSON `notes` map.

### (h) CI-conditional perf assertions

Any pytest test that asserts wall-clock timing (e.g. "extract of 10k
rows completes in < 2 s") is marked `@pytest.mark.perf` and skipped by
default. `CI=true` or `-m perf` enables. Rationale: shared CI runners
have unpredictable spare capacity; a hard timing assertion would flap
red without indicating a real regression.

## 8. Files this worktree creates or modifies

Created (all under `multi-agent/tools/eval/metrics/`):

- `pyproject.toml` — `[project] name = "eval-metrics"`, entry point
  `eval-metrics = "eval_metrics.cli:main"`; runtime deps `sqlglot >= 20`;
  dev deps `pytest`, `pytest-cov`.
- `eval_metrics/__init__.py`
- `eval_metrics/__main__.py` — enables `python -m eval_metrics extract ...`
- `eval_metrics/cli.py` — argparse, exit codes, dispatch to `extract`
- `eval_metrics/db.py` — read-only open helper (§7 (a)–(b))
- `eval_metrics/filter.py` — `--runs-filter` sanitizer (§7 (c))
- `eval_metrics/paths.py` — `--observer-db` / `--out` path validators (§7 (b), (d))
- `eval_metrics/csv_out.py` — CSV serializer + formula-injection escape (§7 (d))
- `eval_metrics/json_out.py` — JSON serializer
- `eval_metrics/metrics/__init__.py` — registry of the 36 metrics
- `eval_metrics/metrics/lifecycle.py` — §2.1 (8 metrics)
- `eval_metrics/metrics/contracted.py` — §2.2 (7 metrics)
- `eval_metrics/metrics/user_promoted.py` — §2.3 (11 metrics, all null today)
- `eval_metrics/metrics/semantic.py` — §2.4 (3 metrics)
- `eval_metrics/metrics/overhead.py` — §2.5 (7 metrics)
- `tests/conftest.py`
- `tests/fixtures/build_fixture_1.py` + `fixture_1.db` + `expected_1.json`
- `tests/fixtures/build_fixture_2.py` + `fixture_2.db` + `expected_2.json`
- `tests/fixtures/build_fixture_3.py` + `fixture_3.db` + `expected_3.json`
- `tests/test_cli.py`, `tests/test_filter.py`, `tests/test_paths.py`,
  `tests/test_csv_out.py`, `tests/test_metrics_lifecycle.py`,
  `tests/test_metrics_contracted.py`, `tests/test_metrics_semantic_overhead.py`,
  `tests/test_metrics_null_contract.py`, `tests/test_security_denylist.py`,
  `tests/test_db_readonly.py`, `tests/test_empty_db.py`

Modified: none. This worktree adds files only. If any file outside
`multi-agent/tools/eval/metrics/` is touched — including any `.go` file
— that is a spec violation and the reviewer should reject the diff.

## 9. Change record

- 2026-07-02 (initial): spec written against 12号 §D2 (line 99), 12号
  §A/§B/§C/§D metric lists, 08号 §Metrics / §Experiments / §Data
  collection schema (08:32–288), Phase 1 close-out memo §5 handoff
  (14:111–125). Denominator for `ContractCompleteness` pinned at 7 per
  12号 §A2 (not 08号's 8-field count).
- 2026-07-02 (round 2, Codex P0 fixes):
  - Added missing metrics: `StateContinuityRate` (#9, 08:39 / 12:44
    §A5), `TimeToFirstTask` (#38, 08:90 / 12:89 / 12:105),
    `SetupFailureRate` (#39, 08:237 / 12:89 / 12:105). Catalog count
    36 → 39.
  - Fixed scope-line contradiction: output is CSV single row or JSON
    single object, NOT JSON-Lines.
  - Fixed aggregation-vs-empty-DB contradiction: §3.3 now specifies
    one output record per invocation including empty cohort; §5.1 +
    §4.4 aligned to "header + one data row with row_count=0".
  - Fixed `RoutingLatencyP50P95` provenance: `runs` has no
    `conversation_id` column; join to `route_reasons` is on the time
    window `[runs.start_time, runs.end_time]`, using
    `route_reasons.decision_started_at`.
  - Fixed fixture 3 null-count arithmetic (was "5 non-routing overhead
    metrics"; is now "6 non-routing E5 + 2 E6 onboarding = 8 overhead
    metrics all null").
