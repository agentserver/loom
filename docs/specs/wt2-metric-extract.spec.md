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
> upstream data source implementation. Metrics whose data source
> **has** landed AND whose cohort-projection join keys exist in
> today's schema are populated with real values; metrics whose data
> source has **not** yet landed (12 号 §A3 / §A6 / §B / §C4 / §D6c /
> §D7 / §D8, Phase 1 close-out memo §5), **or** whose upstream is
> landed but whose per-run join key is missing (§A2 landed the
> `task_contracts` writer + 7-field bitmap logic, but the per-run→
> contract join key is not in today's schema — see §2.2 row 10), are
> still emitted as columns but populated with `null` and a
> `"upstream data missing"` note (see §7 (g)). The per-metric
> provenance table in §2 marks which category each metric falls in
> today.

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
§Contracted / §User-promoted / §Overhead — 40 metrics in total — and
the extractor MUST emit a header column for each, even when the
underlying data source has not yet been instrumented.

## 2. Metric catalog (authoritative — 40 metrics)

Order is preserved in CSV headers and JSON `metrics`-object keys;
renaming or reordering this list is a spec change, not an implementation
change. `null` handling per §7 (f). "Provenance" cites the observer
table/column or fixture-event type the extractor reads.

Cross-check anchor: this catalog is the union of every metric name
appearing in 08 号 §Metrics tables (08:32–92), 08 号 §Experiments
metrics-lines (E1: 08:116, E2: 08:141, E3: 08:163, E4: 08:198 + Stage
A/B/C metrics at 08:184-186, E5: 08:219, E6: 08:237), and 12 号
§A/§B/§C/§D metric one-liners (12:41, 12:44, 12:74, 12:87, 12:89,
12:105, 12:106, 12:107), plus `HumanEditCount` from 08:185 (E4 Stage
A metric — the 08 §User-promoted table at 08:65-78 does not list it
as a named row, but the E4 experiment body does). If a future audit
finds a named metric not in this catalog, that is a P0 spec bug.

### 2.1 Lifecycle (9 — from 12 号 §D8 / §A5 + 08 号 §Lifecycle / E1 / E3)

**Metric-set membership.** Each metric row in §2.1..§2.5 has an
implicit `metric_set` tag equal to its section (`lifecycle` for §2.1
rows, `contracted` for §2.2, etc.) UNLESS the row's "Metric-sets"
column below overrides it. Two lifecycle-listed metrics belong to
BOTH `lifecycle` AND `overhead` because 08号 files them under both
groupings — `ManualSetupStepCount` and `ConfigTouchCount` are named
in 08号 §Overhead (08:87 heading, `ManualSetupStepCount` at 08:91;
`ConfigTouchCount` at 08:237 as E6 metric) AND cited by 12号
§D8 for E1/E6 lifecycle probes:

| # | Metric-set membership |
|---|---|
| 4 (`HumanContextSelectionCount`) | `lifecycle`, `semantic` |
| 5 (`WrongContextFailureRate`) | `lifecycle`, `semantic` |
| 7 | `lifecycle`, `overhead` |
| 8 | `lifecycle`, `overhead` |
| 22 (`CapabilityReuseRate`) | `lifecycle`, `user-promoted` |
| 23 (`RepeatedGenerationRate`) | `lifecycle`, `user-promoted` |

- `--metric-set overhead` emits #7 and #8 in addition to the §2.5 rows.
- `--metric-set lifecycle` emits #22 and #23 in addition to the §2.1 rows (08:37-38 files them under both §Lifecycle and §User-promoted).
- `--metric-set semantic` emits #4 and #5 in addition to the §2.4 rows (08:140 E2 metrics list `HumanContextSelectionCount` and `WrongContextFailureRate` as Semantic-routing metrics too).

All other metrics remain in exactly one metric-set.

| # | Metric | Numerator | Denominator | Provenance |
|---|---|---|---|---|
| 1 | `TaskSuccessRate` | `count(runs where success_oracle_result = 'pass')` | `count(runs in selection)` | `runs.success_oracle_result` |
| 2 | `LifecycleClosureRate` | `count(runs where success_oracle_result = 'pass' AND capability_snapshot_hash != '' AND task_contract_hash != '' AND artifact_hashes != '[]' AND observer_trace_path != '')` — the five columns map to 08:36's chain "raw-context (`capability_snapshot_hash`) → capability (same) → contract (`task_contract_hash`) → artifact/telemetry (`artifact_hashes` + `observer_trace_path`) → reusable-capability WHEN NEEDED". The reusable-capability leg is 08:36's "when needed" branch — a task that never touches a reusable capability still closes the lifecycle; the extractor does not require `dynamic_mcp_registry_hash != ''` here because that would over-restrict tasks that never engaged the promotion path. See 12号 §B `CapabilityReuseRate` (§2.3 row 22) for the reuse-side metric. | `count(runs in selection)` | `runs.{success_oracle_result, capability_snapshot_hash, task_contract_hash, artifact_hashes, observer_trace_path}` |
| 3 | `TimeToCompletion` | reported as a **structured object** with keys `p50_seconds`, `p95_seconds`, `mean_seconds`, `count` — computed from `(end_time − start_time)` across the selection; excludes rows where either timestamp is empty. | — | `runs.{start_time, end_time}` |
| 4 | `HumanContextSelectionCount` | `sum(runs.human_intervention_count)` | — | `runs.human_intervention_count` (WT-1-run-schema §D1) |
| 5 | `WrongContextFailureRate` | `count(runs where failure_category IN ('wrong-context','missing-file','wrong-version','forbidden-cred','stale-capability'))` — the five D4 taxonomy tags that map to 08:48's "missing file/tool/OS/credential/network in chosen context" (`forbidden-cred` covers credential; `stale-capability` covers network / OS reachability drift; the three tool/file/version tags are direct) | `count(runs in selection)` | `runs.failure_category` (11-value taxonomy per PR #61 / D4) |
| 6 | `ArtifactCorrectnessRate` | `count(runs where success_oracle_result = 'pass' AND artifact_hashes != '[]')` | `count(runs where artifact_hashes != '[]')` | `runs.{success_oracle_result, artifact_hashes}` (oracle-side truth deferred to 12号 §D8; see §7 (g)) |
| 7 | `ManualSetupStepCount` | `sum(runs.manual_setup_step_count)` if column present, else `null` + `"upstream data missing"` | — | 12号 §D8 owns the writer; not yet landed → emit `null` per §7 (g) |
| 8 | `ConfigTouchCount` | `sum(runs.config_touch_count)` if column present, else `null` + `"upstream data missing"` | — | same as above |
| 9 | `StateContinuityRate` | data source not landed: 08:39 defines the numerator as "# tasks whose artifacts/contracts/events can be **traced across driver/slave/restart**"; the driver task journal (12号 §A5) has landed and provides the artifact/contract IDs, but there is no observer join today that tests trace-continuity across a driver/slave restart (12号 §A6 owns the resume audit + write_id dedup that would supply the "restart" side of the trace). → `null` + `"upstream data missing"` | — | Named in 08:39 + 12:44 (§A5). §7 (g). A future worktree that adds the §A6 resume audit will populate this metric without spec change. |

**Note on data source status.** Columns `manual_setup_step_count` and
`config_touch_count` are 08号 §Overhead / §E6 fields; they are NOT in
the 24-column `runs` DDL landed by PR #56 (see `runs` DDL in
`multi-agent/internal/observerstore/schema.sql:194-220`). The extractor
therefore emits them as `null` today; when 12号 §D8 / §D6c adds the
columns, no spec change is needed — the extractor SHALL detect column
presence via `PRAGMA table_info(runs)` and switch from `null` to `sum(...)`.
`StateContinuityRate` (§2.1 #9) is 08:39 / 12:44 — the driver task
journal (§A5) has landed, but the "traced across driver/slave/restart"
join requires 12号 §A6 (resume audit + write_id dedup), which has
not — the metric emits `null` today.

### 2.2 Contracted (7 — from 12 号 §A + 08 号 §Contracted / E3) — metrics #10..#16

| # | Metric | Numerator | Denominator | Provenance |
|---|---|---|---|---|
| 10 | `ContractCompleteness` | data source cohort-attribution not landed: while `task_contracts.body` is populated by PR #52 and the 7-field bitmap logic is in `internal/contract/completeness.go`, projecting the runs-cohort (from `--runs-filter`) onto `task_contracts` requires a join key that today's schema does not provide — `runs` has no `conversation_id` and `task_contracts` has no `run_id`/hash. Any per-baseline aggregation would silently include contracts from other baselines and lie. → `null` + `"upstream data missing"` (owner: 12号 §D1 follow-up to add a per-run→contract join key). | — | Requires a per-run→contract join key not yet in schema. §7 (g). **Uncohorted diagnostic** available via a future subcommand (out of scope for this worktree); this row emits `null` in the cohort output. |
| 11 | `PreExecutionFaultCatchRate` | data source not landed: while `runs.failure_category` can carry the `policy-violation` tag (D4 taxonomy, PR #61), only 12号 §A3 (dry-run pre-exec validator) attributes a caught fault to a pre-execution block — that worktree has NOT landed → `null` + `"upstream data missing"` | — | Requires 12号 §A3 validator + `dry_run_blocks` event stream; not yet in observer schema. §7 (g). |
| 12 | `ContractViolationRate` | data source not landed: needs the runtime `contract_violations` audit view (12号 §A4, P1); the `failure_category='contract-violation'` tag exists in D4 taxonomy but nothing writes it yet → `null` + `"upstream data missing"` | — | Requires 12号 §A4 audit. §7 (g). |
| 13 | `MissingArtifactDetectionRate` | data source not landed: needs the artifact-oracle/dry-run detection event (12号 §A3); the `failure_category='missing-file'` tag exists but attribution to a detected-vs-undetected fault is A3's job → `null` + `"upstream data missing"` | — | Requires 12号 §A3. §7 (g). |
| 14 | `PolicyViolationPreventionRate` | data source not landed (12号 §A3 P1, not this worktree) → `null` + `"upstream data missing"` | — | Requires `dry_run_blocks` table (12号 §A3); not yet in observer schema. §7 (g). |
| 15 | `RecoverySuccessRate` | data source not landed: 08:60 defines the numerator as "# interrupted tasks that resume or fail safely / # injected failures". A run's `failure_category` reflects its **terminal** state — a recovered run's `failure_category` is `''` (it passed) and its `success_oracle_result` is `'pass'`, so `runs` alone cannot distinguish "recovered from an interruption" from "never interrupted". This attribution requires 12号 §A6 write_id dedup + resume-event stream. → `null` + `"upstream data missing"` | — | Requires 12号 §A6. §7 (g). |
| 16 | `DuplicateSideEffectRate` | data source not landed (12号 §A6 P1, write_id dedup table missing) → `null` + `"upstream data missing"` | — | Requires observer `write_id` dedup table (12号 §A6). §7 (g). |

### 2.3 User-promoted (12 — from 12 号 §B + 08 号 §User-promoted / E4) — metrics #17..#27, #40

All 12 emit `null` + `"upstream data missing"` at spec time — the
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
| 21 | `RegistryLookupHitRate` | 08:70 defines this as "# new tasks for which driver's pre-prompt lookup found an applicable registered MCP / # new tasks" — the denominator is `count(new tasks)`, NOT `count(lookup events)`, so a missing lookup instrumentation manifests as a low ratio (many task rows, few hit events), not a denominator-zero. Extractor: `count(events where type='registry_lookup' AND result='hit') / count(runs in selection)` — the denominator uses `runs` (per 08:70's "new tasks") rather than the events table, avoiding the "denominator=0 hides missing instrumentation" trap. | 12号 §B4 + `runs` (denominator) |
| 22 | `CapabilityReuseRate` | `count(runs where reused_mcp_hash != '') / count(runs in same capability family)` | 12号 §B4 + `runs.dynamic_mcp_registry_hash` |
| 23 | `RepeatedGenerationRate` | `count(events where type='user_scaffold_start' with existing valid MCP in registry) / count(repeated capability-family tasks)` | 12号 §B4 |
| 24 | `PromotionAdoptionRate` | `count(events where type='register_slave_mcp') / count(events where type='promote_candidate')` | 12号 §B1+§B2 |
| 25 | `AdHocScriptTaskShare` | `count(runs where no promotion event fired) / count(runs)` | 12号 §B1 (absence signal) |
| 26 | `GeneratedCapabilityDefectRate` | 08:76 defines this as "# reuse attempts failing due to a registered tool bug / # reuse attempts" — the numerator is scoped to `failure_category='registered-tool-defect'` (a not-yet-landed D4 taxonomy tag; today's 11-value taxonomy has no equivalent), NOT "any failed run using a user-promoted capability" (that would conflate workload-side failures with tool bugs). Extractor: `count(runs where selected_capability_source='user-promoted' AND failure_category='registered-tool-defect') / count(runs where selected_capability_source='user-promoted' AND was_reuse_attempt=true)`. | 12号 §B4 + oracle + a future §D4 tag |
| 27 | `ReuseSpeedup` | `avg(TimeToCompletion for stage='A') / avg(TimeToCompletion for stage='C')` over same capability family | 12号 §B (family + stage columns not yet in `runs`) |
| 40 | `HumanEditCount` | 08:185 (E4 Stage A baseline metric) — number of manual edits the operator makes to a Stage-A ad-hoc script before it works. **Not** the same as `HumanContextSelectionCount` (#4), which counts context-selection interventions. Extractor: `sum(runs.human_edit_count)` if the column is present in `PRAGMA table_info(runs)`, else `null` + `"upstream data missing"`. | Requires a new `runs.human_edit_count` column; owner: 12号 §D8 follow-up (currently focused on §D8's named metrics only). §7 (g). |

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

The parser accepts equality (`col = 'lit'`), `IN (lit, ...)`,
`LIKE 'pat'`, and their `AND`/`OR` combinations. The extractor is
the ONLY source of `?` placeholders — the placeholder-substitution
step below replaces literals with `?` internally; users cannot supply
`?` on the CLI. `;`, comments (`--`, `/*`), subqueries (`SELECT`,
`WITH`), and DDL/DML keywords (`UPDATE`/`DELETE`/`INSERT`/`DROP`/
`ALTER`/`ATTACH`/`PRAGMA`) trigger rejection before parse —
belt-and-suspenders against sqlparse edge cases.

### 3.2 `--metric-set` selection

`full` emits all 40 metrics from §2 (with structured metrics flattened
into sub-columns, so the on-wire column count is larger — see §3.3).
The five subset values each emit the section's rows PLUS any metric
whose §2.1 Metric-set membership table (or an equivalent cross-list
note in §2.2..§2.5) explicitly names the subset:

| Subset value | Rows emitted |
|---|---|
| `lifecycle` | §2.1 rows #1..#9 + #22 `CapabilityReuseRate` + #23 `RepeatedGenerationRate` (cross-listed per §2.1 membership table) |
| `contracted` | §2.2 rows #10..#16 |
| `user-promoted` | §2.3 rows #17..#27 |
| `semantic` | §2.4 rows #28..#30 + #4 `HumanContextSelectionCount` + #5 `WrongContextFailureRate` (cross-listed per §2.1 membership table; 08:140) |
| `overhead` | §2.5 rows #31..#39 + #7 `ManualSetupStepCount` + #8 `ConfigTouchCount` (cross-listed per §2.1 membership table) |

Any value not in the six-value enum above → exit 2 with the message
`unknown metric set: <val> (allowed: full, lifecycle, contracted,
user-promoted, semantic, overhead)` (§7 (e)).

### 3.3 Output shape

**Aggregation model.** The extractor produces **one output "record" per
invocation** (not per `runs` row), because the paper metrics are
cohort-level ratios / percentiles. `--runs-filter` narrows the cohort;
a single invocation always produces exactly one record — including
when the cohort is empty (in which case count-metrics with landed
upstream data sources emit `0`, count-metrics with unlanded upstream
emit `null`, and ratio-metrics emit `null` per §7 (f)). To produce
Table 2's per-baseline breakdown, the caller invokes the extractor
once per `baseline_or_ablation` value and concatenates the resulting
single-row CSVs.

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
a reason from the closed set:

- `"upstream data missing"` — metric's upstream data source (12号 §A3/§A4/§A6/§B/§C4/§D6c/§D7/§D8) or per-run cohort-projection join key has not landed today (§7 (g))
- `"denominator zero"` — denominator evaluated to 0 for this cohort (§7 (f))

Every `null` metric MUST land in exactly one of these two categories
and get exactly one `_notes` entry; the two categories are
mutually exclusive because a metric whose upstream is not landed
never reaches the denominator computation.

**JSON.** Single JSON object (not JSON-Lines) with keys:

- `metric_set`: the resolved value of `--metric-set`;
- `row_count`: number of `runs` rows in the selection (integer, may be 0);
- `metrics`: an object mapping metric-name → scalar or nested object;
- `notes`: a map from metric-name → status string, always emitted
  (even when empty `{}`), so downstream consumers can rely on the key
  being present.

**Empty-DB / zero-selection contract.** A cohort of zero `runs` rows
still produces a valid one-record output:

- CSV: header row + exactly one data row where `row_count = 0`.
  Count-metrics whose data source HAS landed (`HumanContextSelectionCount`
  from `runs.human_intervention_count`) = `0`. Count-metrics whose
  data source has NOT landed = **empty cell** (JSON `null`),
  identical to ratio-metric denominator-0 treatment — the operator
  must not read "0 manual setup steps" as evidence when in fact the
  probe never fired. Ratio-metrics = empty cell. Structured metrics
  = empty flattened sub-columns.
- JSON: `{"metric_set": "full", "row_count": 0, "metrics": {
  HumanContextSelectionCount: 0, TaskSuccessRate: null,
  ManualSetupStepCount: null, ...}, "notes":
  {...one entry per metric whose upstream is missing}}`.

The distinction between "count metric with real 0" and "count
metric with unavailable data" is exactly the §7 (f) rationale
against emitting `0` for missing data — it applies to count metrics
just as it does to ratio metrics.

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
Every row has `capability_snapshot_hash = 'cs-<run#>'` (non-empty on
all 10) — this is stated once here so the fixture table below does
not need to carry the column.

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
- `LifecycleClosureRate = 6/10 = 0.6` (same 6 rows: pass AND
  capability_snapshot_hash non-empty (universally true in fixture 1)
  AND contract_hash non-empty AND artifacts non-empty AND trace
  non-empty)
- `TimeToCompletion.mean_seconds = (12+18+30+10+60+20+90+14+22+16)/10 = 29.2`
- `TimeToCompletion.count = 10`
- `TimeToCompletion.p50_seconds = 19.0` (linear-interp median of the sorted list [10,12,14,16,18,20,22,30,60,90]; per numpy convention, midpoint of the two middle values 18 and 20)
- `TimeToCompletion.p95_seconds = 76.5` (linear-interp 95th percentile; between the two top values 60 and 90 at fractional index 8.55 → 60 + 0.55·(90-60) = 76.5)
- `HumanContextSelectionCount = 0+1+2+0+3+0+1+0+1+0 = 8`
- `WrongContextFailureRate = 3/10 = 0.3` (rows 3, 5, 9)
- `ArtifactCorrectnessRate = 6/6 = 1.0` (all 6 rows with non-empty artifacts also passed; denominator = 6)
- `ManualSetupStepCount = null` (column absent per §2.1 note)
- `ConfigTouchCount = null` (same)
- `StateContinuityRate = null` (§2.1 row 9, upstream missing —
  12号 §A6 resume audit not landed; the paper's "traced across
  driver/slave/restart" numerator requires a resume-event join
  this worktree cannot provide today)

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

All seven §2.2 metrics assert to `null` in fixture 2:

- `ContractCompleteness = null` (§2.2 row 10, cohort-attribution
  join key missing — the per-run→contract join is not in today's
  schema; the fixture's 6 contracts cannot be legally attributed
  to the 6-run cohort)
- `PreExecutionFaultCatchRate = null` (§2.2 row 11, upstream missing — 12号 §A3 not landed)
- `ContractViolationRate = null` (§2.2 row 12, upstream missing — 12号 §A4 not landed)
- `MissingArtifactDetectionRate = null` (§2.2 row 13, upstream missing — 12号 §A3 not landed)
- `PolicyViolationPreventionRate = null` (§2.2 row 14, upstream missing)
- `RecoverySuccessRate = null` (§2.2 row 15, upstream missing — 12号 §A6 not landed)
- `DuplicateSideEffectRate = null` (§2.2 row 16, upstream missing)

**Why keep fixture 2 at all if every §2.2 metric is null today?**
Fixture 2 exercises the FIXTURE-BUILDER (populates `task_contracts`
correctly) and the null-metric contract for §2.2 — both must
continue to work once 12号 §A3/§A4/§A6 land and metrics #10..#16
flip to real values. The hand-computed `ContractCompleteness =
37/42` and the 6 flag combinations remain here as the reference
values a future §A3/§D1 follow-up worktree will assert on when it
enables cohort-projected computation. They are NOT asserted by
this worktree's tests; each metric asserts `null` today.

### 4.3 Fixture 3 — Semantic + Overhead + User-promoted (§2.3–§2.5)

5 runs (`experiment_id='E2'`), 4 `route_reasons` rows, no
`task_contracts`. The fixture's purpose is to exercise the two
routing metrics (`RoutingAccuracy` and `RoutingLatencyP50P95`) plus
the full null-matrix for user-promoted / non-routing overhead /
capability-graph metrics whose upstream sources have not landed.

Each of the 5 runs has fully populated lifecycle columns
(`success_oracle_result`, `start_time`, `end_time`,
`artifact_hashes`, `human_intervention_count`, `failure_category`,
`observer_trace_path`) so §2.1 metrics compute deterministically —
those hand-computed values appear alongside the routing values in
the "Hand-computed values" block below. The `task_contract_hash`
column is empty on every row, and there are no `task_contracts` rows;
`ContractCompleteness` (§2.2 #10) nulls for the same
cohort-attribution-missing reason spelled out in §2.2 row 10 (the
per-run→contract join key is absent regardless of whether
`task_contracts` is populated).

| run # | selected_context | ground_truth_context |
|---|---|---|
| 1 | slave-A | slave-A |
| 2 | slave-A | slave-B |
| 3 | slave-B | slave-B |
| 4 | slave-A | slave-A |
| 5 | slave-C | ''        |

The full per-run lifecycle-column matrix is (every row has
`capability_snapshot_hash = ''` — no capability snapshot recorded —
so `LifecycleClosureRate` correctly evaluates to 0/5 under the
5-column predicate in §2.1 row 2):

| run # | success | end−start (s) | human | failure_category | artifact_hashes | task_contract_hash | observer_trace_path |
|---|---|---|---|---|---|---|---|
| 1 | pass | 20.0 | 0 | ''             | `["ar1"]` | '' | `/t/e2-1` |
| 2 | fail | 25.0 | 1 | wrong-context  | `[]`      | '' | `/t/e2-2` |
| 3 | pass | 15.0 | 0 | ''             | `["ar3"]` | '' | `/t/e2-3` |
| 4 | pass | 10.0 | 0 | ''             | `["ar4"]` | '' | `/t/e2-4` |
| 5 | fail | 40.0 | 2 | missing-file   | `[]`      | '' | `/t/e2-5` |

`route_reasons` rows: 4 decisions total (run #5 never dispatched).
Since `runs` has no `conversation_id` column (see §2.5 #37 provenance
clause on the time-window join), the fixture pins the join via
timestamps:

| decision # | decision_started_at | decision_duration_ns | conversation_id |
|---|---|---|---|
| 1 | 2026-07-02T10:00:05Z | 500_000     | conv-1 |
| 2 | 2026-07-02T10:01:05Z | 1_000_000   | conv-2 |
| 3 | 2026-07-02T10:02:05Z | 2_000_000   | conv-3 |
| 4 | 2026-07-02T10:03:05Z | 4_000_000   | conv-4 |

And the 4 dispatched runs' `[start_time, end_time]` windows contain
exactly the above decision timestamps in order (run 5 has no
overlapping decision). The join filter is
`WHERE route_reasons.decision_started_at BETWEEN run.start_time AND
run.end_time`, restated in §2.5 #37.

Hand-computed values (per-metric, in §2 order — 39 total):

Populated metrics:

- `TaskSuccessRate = 3/5 = 0.6` (§2.1 #1)
- `LifecycleClosureRate = 0/5 = 0.0` (§2.1 #2; no rows have
  task_contract_hash non-empty AND no rows have
  capability_snapshot_hash non-empty either — the 5-column AND fails
  on every row)
- `TimeToCompletion = {mean_seconds: 22.0, p50_seconds: 20.0,
  p95_seconds: 37.0, count: 5}` (§2.1 #3; sorted [10, 15, 20, 25,
  40]; median = 20 (middle element); p95 at fractional index 3.8 →
  25 + 0.8·(40−25) = 37)
- `HumanContextSelectionCount = 3` (§2.1 #4; 0+1+0+0+2)
- `WrongContextFailureRate = 2/5 = 0.4` (§2.1 #5; rows 2 and 5)
- `ArtifactCorrectnessRate = 3/3 = 1.0` (§2.1 #6; 3 rows with
  non-empty artifact_hashes all passed)
- `RoutingAccuracy = 3/4 = 0.75` (§2.4 #28; rows 1, 3, 4 match;
  row 2 wrong; row 5 excluded — ground_truth empty)
- `RoutingLatencyP50P95 = {p50_ns: 1_500_000, p95_ns: 3_700_000,
  count: 4}` (§2.5 #37; numpy linear-interp on sorted
  [500k, 1M, 2M, 4M]; median = midpoint = 1.5M; p95 at fractional
  index 2.85 → 2M + 0.85·(4M−2M) = 3.7M)

Null metrics (32 total = 40 catalog − 8 populated above):

- §2.1: `ManualSetupStepCount` (#7), `ConfigTouchCount` (#8),
  `StateContinuityRate` (#9) — 3 nulls
- §2.2: all 7 contracted metrics — `ContractCompleteness` (#10)
  via cohort-attribution missing; #11..#16 via upstream-missing.
  **7 nulls**
- §2.3: all 11 user-promoted metrics (#17..#27) — 11 nulls
- §2.4: `CapabilityRecall` (#29), `CapabilityPrecision` (#30) —
  2 nulls
- §2.5: 6 non-routing E5 overhead metrics (#31..#36) plus 2 E6
  onboarding metrics (#38, #39) — 8 nulls

Total: 3 + 7 + 11 + 2 + 8 = **31 null cells**. Populated 8 + null
32 = 40 metrics.

### 4.4 Empty-DB fixture (implicit)

An in-memory DB with the **full** observer schema DDL from
`multi-agent/internal/observerstore/schema.sql` applied but zero rows
in any table. Applying only the WT-1-run-schema DDL would leave
`route_reasons`, `task_contracts`, `capability_snapshots`, and other
tables absent, and §2.5 #37 `RoutingLatencyP50P95`'s query to
`route_reasons` would fail with `no such table`. The extractor's
`db.py` helper therefore treats a **missing companion table** as
`null` + `"upstream data missing"` (schema not applied — an
operational error), and an **empty companion table** (schema
applied but zero rows) as `null` + `"denominator zero"` (data
source landed, cohort just has no rows). This asymmetry matters
for `RoutingLatencyP50P95` in particular: its upstream is landed
today (PR #55), so an empty `route_reasons` in a real observer DB
means "no dispatch decisions occurred", which is denominator-zero
semantics — different from "schema was never applied", which is a
setup bug. The empty-DB fixture applies the whole schema so this
branch is exercised as denominator-zero for #37; a separate
missing-table integration test in the plan exercises the
upstream-missing branch.
Expected output per §3.3 empty-DB contract:

- CSV: header row + **exactly one data row** with `metric_set=full`,
  `row_count=0`; count-metrics with landed upstream emit `0`,
  count-metrics with unlanded upstream emit empty cell (JSON
  `null`), all ratio and structured metrics = empty cells,
  `_notes` populated for every metric whose upstream is missing.
- JSON: `{"metric_set":"full","row_count":0,"metrics":{count-metric
  with landed upstream: 0, count-metric with unlanded upstream: null,
  ratio-metric: null, ...},"notes":{...upstream-missing
  entries...}}`.

Confirms §5 acceptance criterion 1.

## 5. Acceptance criteria

1. `eval-metrics extract --observer-db <empty.db> --format csv` prints
   a header line whose column set is `metric_set, row_count, <the 40
   metric names in §2 order, with structured metrics flattened to
   <metric>.<subkey> sub-columns>, _notes`, followed by exactly one
   data row where `row_count=0`; count-metrics with landed upstream
   are `0`, count-metrics with unlanded upstream are empty cells,
   ratio and structured metric cells are empty, and `_notes` is
   populated per §3.3 empty-DB contract.
2. Each of the three golden fixtures produces the hand-computed values
   in §4 to full float64 precision — no rounding, no lossy formatting.
3. `--metric-set lifecycle` on fixture 1 emits exactly the columns
   per the §3.2 subset table for `lifecycle` (i.e. §2.1 rows #1..#9
   plus §2.3 rows #22 and #23 as cross-listed members, with structured
   metrics flattened) plus the leading `metric_set` / `row_count`
   columns and the trailing `_notes` column.
4. Every metric whose data source has NOT landed **OR whose
   per-run cohort-projection join key is missing in today's schema
   (§2.2 row 10 `ContractCompleteness` is the canonical case)**
   produces `null` in the value cell AND an entry in the `notes` map /
   stderr warning with the literal string `"upstream data missing"`.
   Every metric whose upstream IS landed but whose denominator
   evaluates to 0 for the current cohort produces `null` AND the
   literal string `"denominator zero"`. These two are the entire
   closed set of `_notes` reasons.
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
   → exit 2 — this is the correct policy for `--observer-db` because
   an unreadable/nonexistent input DB is always an error; the `--out`
   path validation in §(d) below uses a **different** policy that
   requires the target to NOT yet exist). Reject if the resolved path
   descends from any of `/etc/`, `/proc/`, `/sys/`, `/dev/`.
   Sample-and-hold: the resolved path is
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
   the whitelist below → exit 2 `ErrRunsFilterFieldNotAllowed`. If
   parse fails → exit 2 with the parse error. **Additionally** (this
   guards against the `run_id = 'x' OR 1=1` broadening attack):
   walk every `Predicate` / `Comparison` / `In` / `Like` /
   `Between` / `Is` node in the AST; each such node MUST reference
   at least one column from the whitelist. A predicate that
   references only literals on both sides (e.g. `1 = 1`, `'a' =
   'a'`) → exit 2 `ErrRunsFilterTautology`. This closes the
   OR-broadening surface — `run_id = 'x' OR 1 = 1` is rejected
   because `1 = 1` fails the "must reference a column" test even
   though `1` alone is not a Column node.
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
- `run_id = 'x' OR 1 = 1` — caught by tautology rule (the
  `1 = 1` predicate references only literals). This is the
  broadening-attack case.
- `1 = 1` alone — same tautology rule.
- `run_id = workload_id` — accepted **only if both columns are
  in the whitelist** (they are); a filter like
  `run_id = capability_snapshot_hash` is rejected because the
  right-hand column is not in the whitelist.

Accepted examples (all → prepared with `run_id = ?` binding):

- `run_id = 'run-abc'`
- `experiment_id IN ('E1','E3')`
- `baseline_or_ablation = 'FullLoom' AND workload_id LIKE 'code-mod-%'`

### (d) `--out` path validation + CSV formula-injection escape

`--out` is a **write target**, so its path-validation policy differs
from §(b)'s read-target policy. Rules, in order:

1. **Parent-only realpath**: `parent = pathlib.Path(--out).expanduser().parent.resolve(strict=True)`
   (parent MUST exist — do NOT `mkdir -p`; the tool has no business
   creating directories under paths it did not choose). Reject if the
   resolved parent descends from `/etc/`, `/proc/`, `/sys/`, `/dev/`.
   The `strict=True` applies to the **parent**, not the target file
   itself — an `--out` pointing at a not-yet-created file inside an
   existing parent directory is the normal case.
2. **Overwrite refusal**: if the target file already exists, exit 2
   with `ErrOutFileExists`. There is no `--force` flag; callers who
   want to overwrite must delete the target first (an explicit,
   auditable act).
3. **Symlink refusal on the target itself**: if the target exists as a
   dangling symlink or a symlink pointing anywhere, exit 2 with
   `ErrOutIsSymlink`. This is checked AFTER (1) and BEFORE (4).
4. **Atomic exclusive-nofollow create anchored on the resolved parent**:
   open the target relative to a directory file descriptor of the
   resolved parent, using `dir_fd`:

   ```python
   dir_fd = os.open(str(resolved_parent), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
   try:
       out_fd = os.open(
           os.path.basename(target),
           os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
           0o600,
           dir_fd=dir_fd,
       )
   finally:
       os.close(dir_fd)
   ```

   Using `dir_fd` binds the open to the parent directory that step 1
   resolved; a subsequent rename of the resolved parent to a
   symlink pointing elsewhere cannot re-target the open, because
   the kernel opens `basename(target)` **within the fd that already
   points at the resolved parent's inode**. `O_EXCL` refuses to open
   if the target basename exists inside that fd (redundant with (2),
   but survives a race between (2) and (4)); `O_NOFOLLOW` refuses
   to follow a symlink at the last path component; mode `0o600`
   prevents world-readable output. The parent-directory `O_DIRECTORY
   | O_NOFOLLOW` open closes the parent-side TOCTOU too. If either
   open fails, exit 2 with the errno name and no path bytes beyond
   what the operator supplied. This anchored-`O_CREAT|O_EXCL|O_NOFOLLOW`
   sequence is what actually closes the TOCTOU window; (2) and (3)
   are the belt to its suspenders.

**`/dev/stdout` / `/dev/null` are NOT supported** as `--out` values
(they would trip the `/dev/` reject in step 1). The `stdout` sink is
addressed by **omitting `--out`** entirely; there is no other way to
send output to a terminal. This paragraph corrects the round-2 draft
that mentioned `/dev/stdout` — that would have conflicted with step 1
above.

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
emit the full 40-metric set while the operator thinks they got a
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
`null` and why, using exactly the two-value closed set defined in
§3.3: `"upstream data missing"` or `"denominator zero"`. Upstream
missing takes precedence — if a metric's upstream is not landed, the
extractor emits that note and never even evaluates the denominator,
so `"denominator zero"` cannot mask a schema gap.

### (g) Consumer-view reverse audit

Every metric in §2 MUST have a fixture in §4 that either (a) computes a
non-null value the pytest matrix asserts on, or (b) is explicitly
recorded in the fixture-4 all-null contract. There is NO third category
"no fixture" — every metric name is exercised. When a metric returns
`null`, the extractor emits its column with `null` value AND writes a
one-line stderr warning + a `_notes` / `notes` entry using one of the
two closed-set reasons from §3.3:

```
[eval-metrics] warn: metric <name> returned null: upstream data missing (owner: 12号 §<section>)
[eval-metrics] warn: metric <name> returned null: denominator zero
```

Note-emission is unconditional: EVERY `null` metric — whether from
upstream-missing OR denominator-zero — gets a `_notes` / `notes`
entry and a stderr warning. Missing a note for a `null` cell is a
spec violation. Reviewers of the CSV can eyeball the `_notes`
column; automated pipelines can parse the JSON `notes` map.

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
- `eval_metrics/metrics/__init__.py` — registry of the 40 metrics
- `eval_metrics/metrics/lifecycle.py` — §2.1 (9 metrics)
- `eval_metrics/metrics/contracted.py` — §2.2 (7 metrics)
- `eval_metrics/metrics/user_promoted.py` — §2.3 (12 metrics, all null today)
- `eval_metrics/metrics/semantic.py` — §2.4 (3 metrics)
- `eval_metrics/metrics/overhead.py` — §2.5 (9 metrics)
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
- 2026-07-02 (round 3, Codex P1 fixes):
  - Scope §1: reworded to acknowledge some data sources have NOT
    landed (§A3 / §A6 / §B / §C4 / §D6c / §D7 / §D8), instead of the
    round-1 "every metric uses landed data sources" claim.
  - Metric counts: fixed 36 → 39 everywhere; §8 file manifest
    line-counts corrected (lifecycle 8 → 9; overhead 7 → 9).
  - §7 (d) `--out` policy rewritten: parent must exist (not target
    itself), overwrite refused, symlink refused, no `/dev/stdout`
    (which conflicted with the `/dev/` reject); §7 (b) note added
    to disambiguate the two policies.
  - Fixture 3 `route_reasons` join keys rewritten to time-window
    timestamps (matches §2.5 #37 join rule; removed dead
    conversation-id keying).
  - Fixture 3 null-metric total reconciled: introduces the number
    once (31 cells), removes the per-paragraph re-derivation that
    contradicted itself.
  - §2.2 #10 `ContractCompleteness` cohort-projection clause added,
    including the fallback stderr warning when the per-run→contract
    join key is missing in the current schema.
- 2026-07-02 (round 4, Codex P1 fixes):
  - §2.2 #11 `PreExecutionFaultCatchRate`, #12 `ContractViolationRate`,
    #13 `MissingArtifactDetectionRate` reclassified as
    upstream-missing (12号 §A3 / §A4 not landed); their fixture 2
    hand-computed values changed to `null`; a paragraph clarifies
    that fixture 2's failure_category tags exercise only the
    §2.1 #5 and §2.2 #15 filters.
  - §2.1 #9 `StateContinuityRate` denominator changed to
    `count(runs in selection)` (matches 08:39 "# tasks");
    numerator remains a closed-form AND over four `runs` columns;
    metric is now POPULATED today (not upstream-missing) because
    all required columns already exist.
  - Fixture 1 hand-computed value for `StateContinuityRate` updated
    to 6/10 = 0.6.
  - Fixture 3 rewritten with full per-run lifecycle columns; total
    metric-cell arithmetic now sums explicitly to 9 populated + 30
    null = 39.
  - §7 (d) closing paragraph corrected: "36-column" → "39-metric".
  - Remaining stray "36" references either removed or corrected.
- 2026-07-02 (round 5, Codex P1 fixes — first batch):
  - §2.1 header adds "Metric-set membership" table making #7
    `ManualSetupStepCount` / #8 `ConfigTouchCount` members of BOTH
    `lifecycle` AND `overhead` metric-sets (they're §Overhead in 08
    but §D8 lifecycle probes in 12).
  - §2.2 #10 `ContractCompleteness` fallback removed — the metric
    now emits `null` + upstream-missing when the per-run→contract
    join key is missing, instead of silently aggregating over ALL
    rows and lying.
  - §2.2 #15 `RecoverySuccessRate` reclassified as upstream-missing
    (12号 §A6 not landed): `runs.failure_category` reflects
    terminal state only, so `runs`-alone cannot distinguish
    recovered from never-interrupted.
  - §2.1 #9 `StateContinuityRate` reclassified as upstream-missing
    (12号 §A6 not landed): 08:39 numerator explicitly requires
    "traced across driver/slave/restart", which needs a resume
    join today's schema does not have.
  - §3.3 empty-DB contract: count-metrics with landed upstream = 0;
    count-metrics with unlanded upstream = null (not 0). §5.1
    aligned.
  - §3.1 `--runs-filter` grammar removed `col = ?` — placeholders
    are internally generated, not user-supplied.
  - Fixture 1 `StateContinuityRate = null`; fixture 2 all §2.2
    metrics = null (including #10 `ContractCompleteness`); fixture
    3 arithmetic: 8 populated + 31 null = 39.
- 2026-07-02 (round 6, Codex P1 fixes):
  - §2.1 #5 `WrongContextFailureRate` numerator widened to include
    `forbidden-cred` (credential) and `stale-capability` (network/OS
    drift) per 08:48.
  - §2.1 #2 `LifecycleClosureRate` numerator adds
    `capability_snapshot_hash != ''` per 08:36 lifecycle chain.
  - §2.1 Metric-set membership table: `CapabilityReuseRate` (#22)
    and `RepeatedGenerationRate` (#23) added — 08:37-38 files them
    under both §Lifecycle and §User-promoted.
  - §3.3 aggregation and empty-DB paragraphs harmonized on the same
    contract: count with landed upstream = 0; count with unlanded
    upstream = null; ratio always null when denominator=0 or
    upstream missing.
  - Fixture 3 `ContractCompleteness` null-reason wording aligned
    with §2.2 row 10 (cohort-attribution missing).
- 2026-07-02 (round 7, Codex P1 fixes — first batch):
  - §3.2 `--metric-set` subset table added; §5.1 acceptance criterion
    #3 updated to reference the subset table so the two sections no
    longer disagree about cross-listed metrics.
  - §7 (c) AST parse step 3 tightened: every Comparison / In / Like /
    Between / Is node MUST reference at least one whitelisted column,
    closing the `run_id = 'x' OR 1 = 1` OR-broadening attack that
    would otherwise pass Column-only whitelisting.
- 2026-07-03 (round 8, Codex P1 fixes):
  - §2.1 metric-set membership table cross-lists #4 `HumanContextSelectionCount`
    and #5 `WrongContextFailureRate` into `semantic` (08:140 E2 metrics list).
    §3.2 subset table + §5.1 acceptance criterion updated to match.
  - Scope §1 out-of-scope paragraph carves out the
    `ContractCompleteness` case (upstream landed, cohort join key
    missing → null), so lines 16-17 no longer conflict with §2.2 row 10.
  - §7 (d) `--out` step 4 added: atomic
    `O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW` open (mode `0o600`) is what
    actually closes the TOCTOU window; the parent-realpath / overwrite-
    refusal / symlink-refusal checks are the belt to its suspenders.
- 2026-07-03 (round 9, Codex P1 fixes — first batch):
  - §3.3 `_notes` reason set expanded to two-value closed set
    (`"upstream data missing"` OR `"denominator zero"`) so every
    `null` gets exactly one categorized reason; §5.1 acceptance
    criterion #4 updated to enforce both; §7 (f) closing paragraph
    aligned.
  - §4.4 empty-DB fixture rewritten to apply the FULL observer
    schema (was: only WT-1-run-schema DDL — would `no such table`
    on `route_reasons`); missing-table branch documented as "same
    as empty table → null + upstream-missing".
  - §7 (d) step 4 rewritten to anchor the atomic
    `O_CREAT|O_EXCL|O_NOFOLLOW` open on a `dir_fd` opened
    `O_DIRECTORY|O_NOFOLLOW` from the resolved parent, closing
    the parent-side TOCTOU that a plain path-based open leaves
    open.
  - §2.1 note: `ManualSetupStepCount` cite is 08:91,
    `ConfigTouchCount` cite is 08:237 (E6 metrics list) — the
    round-8 change collapsed both into 08:91 by mistake.
- 2026-07-03 (round 10, Codex P0 + P1 fixes):
  - P0: `HumanEditCount` added as metric #40 (08:185 / 11号 §6);
    catalog count 39 → 40. §2.3 header updated to 12 metrics.
  - P1: §7 (g) reverse audit paragraph emits stderr warnings and
    `_notes` entries for BOTH `"upstream data missing"` AND
    `"denominator zero"` — every null gets a note regardless of
    reason.
  - P1: §4.4 empty-DB fixture clarifies missing-table (upstream
    missing, setup bug) vs empty-table (denominator zero, real
    empty cohort) semantics; `RoutingLatencyP50P95` on the
    fixture asserts `denominator zero`, not upstream missing.
  - P1: `RegistryLookupHitRate` denominator changed to
    `count(runs in selection)` per 08:70 (was
    `count(events where type='registry_lookup')`, which would
    hide missing lookup instrumentation as denominator-zero).
  - P1: `GeneratedCapabilityDefectRate` numerator narrowed to
    `failure_category='registered-tool-defect' AND was_reuse_attempt=true`
    per 08:76 (was any-failed-run-using-user-promoted-capability).
