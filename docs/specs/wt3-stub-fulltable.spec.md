# WT-3-stub-fulltable — Spec

> Phase 3 §7 (12 号 §H step 7 + §J.1 stub path + §E1–E5 + §D1/D2/D3 harness).
> Worktree `paper/v3/p3-stub-fulltable`.
> Baseline: `origin/paper/v3-integration` HEAD `1185e99` at spec time.
> Scope:
> `multi-agent/tools/eval/fulltable/` (NEW) +
> `multi-agent/tests/eval/results/smoke/` (NEW, fixture smoke only) +
> `docs/specs/wt3-stub-fulltable.spec.md` (this file) +
> `docs/specs/wt3-stub-fulltable.plan.md` (Stage 2) +
> `docs/specs/wt3-stub-fulltable.handoff.md` (E4-stage runner-flag handoff, §4.4).
> Nothing else moves.

## 0. Scope declaration — **harness only, no 60-run**

**This worktree ships the scaffolding to run the 60-run E1–E5 matrix on the
Phase 0/1/2 stub path. It does NOT run the 60-run matrix itself.** The
matrix has wall-clock 4–8 h even at `--parallel 8` (todo_list.md line 129
estimate) and hits the model-gateway; triggering it here mixes "harness
was wrong / has to re-run" and "config was wrong / has to re-run" into one
long round-trip. To keep the review checkpoint clean:

* **This worktree produces**: `matrix.yaml` (60 rows), `ablation_mapping.yaml`
  (8 rows), `run.sh` (dry-run + `--sample N≤3` fixture smoke + `--resume`),
  `build_paper_tables.py` (aggregation → paper table structure samples),
  README, tests. Plus a **fixture smoke** — 3 runs into
  `tests/eval/results/smoke/*` — as evidence that the scaffold actually
  drives the runner end-to-end.
* **This worktree does NOT produce**: any file under
  `multi-agent/tests/eval/results/{runs,metrics,paper,dbs,failures}.*` at
  non-smoke locations. The 60-run real data lives in a subsequent worktree,
  provisionally named **`paper/v3/p3-stub-fulltable-run`**, which the user
  will trigger separately after reviewing and merging this scaffold.
* **This worktree does NOT change** any file under
  `multi-agent/tools/eval/runner/`, `multi-agent/internal/`, or any other
  Phase 0/1/2 code. If a needed capability is missing (e.g. the runner
  has no `--stage {A,B,C}` flag for E4), this spec records a **handoff**
  (§4.4) and the follow-up worktree does the runner change; this spec
  does not silently work around it.
* **The aggregation structure defined here is the single source of truth
  for the paper §6.5 ablation table.** The follow-up 60-run worktree
  populates the numbers into the same schema — no ad-hoc re-shaping in
  the paper-writing phase. This is why the aggregation is fully specified
  and tested with fake data now.

Everything below applies inside that scope. Where "60 run" appears, it
describes what the *scaffold enables*, not what this worktree executes.

## 1. Purpose

Ship a reproducible, one-command harness that can drive the full
`5 workload × 12 configuration = 60 run` E1–E5 matrix (12 号 §E1–§E5,
08 号 §Table 2 + §Figure 1–4) on the stub path (12 号 §J.1) — plus the
aggregation to Paper Table 2 / Figure 1–4 data. The scaffold + a
3-run fixture smoke is what this worktree delivers; the real 60 run is
executed by a downstream worktree per §0.

## 2. Module layout

```
multi-agent/tools/eval/fulltable/                (NEW)
├── README.md                       usage + hand-off to real-run worktree
├── matrix.yaml                     60 (workload, configuration) rows
├── matrix.schema.json              jsonschema for matrix.yaml
├── ablation_mapping.yaml           8 rows: flag → difficulty → target metrics
├── ablation_mapping.schema.json    jsonschema for ablation_mapping.yaml
├── e4_stages.yaml                  E4 family × stage A/B/C × task manifest (§4.4)
├── e4_stages.schema.json           jsonschema for e4_stages.yaml
├── run.sh                          orchestrator; dry-run / --sample N / --resume
├── lib/
│   ├── __init__.py
│   ├── plan.py                     matrix → per-run CLI planning
│   ├── portpool.py                 loopback port allocator + retry
│   ├── commit_meta.py              clean-tree + rev-parse assertion
│   ├── failure_scrub.py            secretscrub wrapper + 200-line tail
│   ├── stage_e4.py                 Stage A/B/C annotation (matrix-side; runner
│   │                               flag is a handoff — §4.4)
│   ├── aggregate.py                fake-safe metric aggregation across runs
│   └── paper_tables.py             paper Table 2 / Figure 1–4 sample writer
├── build_paper_tables.py           CLI: metrics.csv + runs.csv → smoke/paper/*_sample.*
└── tests/
    ├── test_matrix_schema.py       60 rows; enum; typo reject
    ├── test_ablation_mapping.py    8 rows; source-of-truth check vs spec table
    ├── test_stub_listen_loopback.py    only 127.0.0.1 allowed
    ├── test_port_pool.py           conflict detection + retry
    ├── test_cloud_baseline_dry_run.py  cloud_sandbox_e2b rows are always --dry-run
    ├── test_per_run_observer_db.py smoke: 3 distinct DB files, no lock races
    ├── test_failure_scrub.py       sk-xxx redacted, ≤200 lines
    ├── test_commit_meta.py         dirty tree → exit 2
    ├── test_dry_run_snapshot.py    ci-friendly; no subprocess
    ├── test_paper_table2_sample.py table2_sample.csv header vs 08 号 §Table 2
    ├── test_paper_table2_ablation.py table2_ablation_sample.csv 8 rows + cols
    ├── test_smoke_readme_warning.py smoke README + _sample suffix
    ├── test_hard_cap_full_run.py   --sample 4 without env exits 2
    ├── test_stage_e4_handoff.py    docstring names the handoff issue
    └── test_e4_stages_yaml.py      e4_stages.yaml schema + family-match to golden/

multi-agent/tests/eval/results/smoke/           (NEW — smoke output only)
├── README.md                       hard warning: NOT paper data
├── runs.csv                        3 rows (3 runs)
├── metrics.csv                     3-run aggregated metrics
├── failures.jsonl                  1 injected fake failure (redacted)
├── dbs/                            per-run observer sqlite
│   ├── <run_id_1>.db
│   ├── <run_id_2>.db
│   └── <run_id_3>.db
└── paper/
    ├── table2_sample.csv           header aligned to 08 号 §Table 2
    ├── table2_ablation_sample.csv  8 rows (one per ablation flag)
    ├── figure1_data_sample.json    routing accuracy & wrong-context
    ├── figure2_data_sample.json    contract fault injection
    ├── figure3_data_sample.json    E4 three-stage (stage A/B/C)
    ├── figure4_data_sample.json    overhead breakdown
    └── e4_stages_sample.csv        E4 three-stage long-form table
```

Every `smoke/paper/*_sample.*` filename **must** end in `_sample.{csv,json}`
(§7 (j)); no non-suffixed paper artifact is produced by this worktree.

## 3. Matrix — 5 workloads × 12 configurations = 60 runs

### 3.1 Workload id enumeration (5)

The five directory names under `multi-agent/tests/eval/workloads/`
(Phase 0 WT-0-workloads, 12 号 §F1, 13 号 §1):

```
cross-device-code-mod
remote-data-processing
windows-only-artifact
missing-parser-converter
credential-bound-model
```

`matrix.yaml` MUST contain the full Cartesian product of these 5
workload ids and the 12 configuration values in §3.2 — no missing pair,
no duplicate, no unknown workload id. The schema test rejects each of
these three violations independently.

### 3.2 Configuration enumeration (12 = 1 + 8 + 3)

The `baseline_or_ablation` value per row is drawn from this fixed
enumeration (12 号 §E5 + Phase 2 WT-2-flag-integration §2.3 for ablation
CLI naming; Phase 2 WT-2-baselines §7 for baseline naming):

```
# Full Loom (1) — matches runner DefaultBaselineName = "full_loom"
# (multi-agent/tools/eval/runner/flags.go:43); referred to in prose as
# "Full Loom" but the on-wire baseline_or_ablation value is always
# lowercase-underscored.
full_loom

# 8 ablation flags (mirrors internal/ablation/registry.go FlagName consts;
# 12 号 §E5)
NoCapabilityDiscovery
NoTypedContracts
NoDryRun
NoContractFormalization
NoUserPromotionPath
NoAcceptanceGate
NoRegistryLookup
NoObserver

# 3 baselines (mirrors tests/eval/baselines/*/impl Name() and
# tests/eval/baselines/matrix_test.go `baselines` slice — Phase 2
# WT-2-baselines README §Baselines)
manual_ssh
single_machine_claude_code
cloud_sandbox_e2b
```

Rows for the `full_loom` configuration are dispatched with **no**
`--ablation` flag and **no** `--baseline-name` override — the runner's
own default stamps `runs.baseline_or_ablation = "full_loom"` (Phase 2
`ComputeBaselineOrAblation`). Rows for an ablation configuration pass
`--ablation <name>` and no `--baseline-name`. Rows for a baseline
configuration invoke that baseline's own binary
(`tests/eval/baselines/<dir>/run.sh`) instead of `eval-runner`;
the baseline's `impl.Name()` stamps the CSV — matches WT-2-baselines §7.

`baseline_or_ablation` values in `matrix.yaml` MUST come from this
enumeration verbatim; the schema test rejects a typo like
`NoAccetpanceGate` (§7 (g)). The harness reads only `matrix.yaml` for
this field; there is no CLI override that could substitute a value the
schema never saw (§7 (g)).

### 3.3 Ablation ↔ difficulty ↔ target-metric mapping

This mapping is copied verbatim from `paper_outputs/evaluation_v3.md`
§6.5.1 (line 233–244). It defines how the aggregated metrics are grouped
into the paper's §6.5 ablation table so that "which flag maps to which
paper §" is fixed at spec time and cannot drift during paper writing.

| ablation flag             | 大纲难点 | 目标 metric (预期方向) |
|---|---|---|
| `NoCapabilityDiscovery`   | 难点一 | `RoutingAccuracy` ↓ / `WrongContextFailureRate` ↑ / `PreExecutionFaultCatchRate` ↓ |
| `NoObserver`              | 难点一 | `PromotionCandidateSurfacingRate` ↓ / `ContractViolationRate` 归因困难 |
| `NoTypedContracts`        | 难点二 | `ContractCompleteness` ↓ / `ContractViolationRate` ↑ / `PolicyViolationPreventionRate` ↓ |
| `NoContractFormalization` | 难点二 | `MissingArtifactDetectionRate` ↓ / `DuplicateSideEffectRate` ↑ |
| `NoDryRun`                | 难点二 | `PreExecutionFaultCatchRate` ↓ / 下游 side-effect ↑ (proxied by `DuplicateSideEffectRate` ↑ + `PolicyViolationPreventionRate` ↓) |
| `NoUserPromotionPath`     | 难点三 | `CapabilityReuseRate` ↓ / `RepeatedGenerationRate` ↑ / `ReuseSpeedup` → 1 |
| `NoAcceptanceGate`        | 难点三 | `ValidationFalseAcceptRate` ↑ |
| `NoRegistryLookup`        | 难点三 | `RegistryLookupHitRate` → 0 / `RepeatedGenerationRate` ↑ |

This table is normative. It is also mirrored to
`ablation_mapping.yaml` as a machine-readable single source of truth:

```yaml
# multi-agent/tools/eval/fulltable/ablation_mapping.yaml
- flag: NoCapabilityDiscovery
  difficulty: 难点一
  target_metrics: [RoutingAccuracy, WrongContextFailureRate, PreExecutionFaultCatchRate]
- flag: NoObserver
  difficulty: 难点一
  target_metrics: [PromotionCandidateSurfacingRate, ContractViolationRate]
- flag: NoTypedContracts
  difficulty: 难点二
  target_metrics: [ContractCompleteness, ContractViolationRate, PolicyViolationPreventionRate]
- flag: NoContractFormalization
  difficulty: 难点二
  target_metrics: [MissingArtifactDetectionRate, DuplicateSideEffectRate]
- flag: NoDryRun
  difficulty: 难点二
  # "下游 side-effect ↑" in the source table is proxied by the two
  # downstream metrics below (evaluation_v3.md §6.5.1 line 241;
  # 08 号 §Metrics defines these two as the on-wire side-effect
  # proxies — DuplicateSideEffectRate for duplicate writes,
  # PolicyViolationPreventionRate for policy-blocked writes).
  target_metrics: [PreExecutionFaultCatchRate, DuplicateSideEffectRate, PolicyViolationPreventionRate]
- flag: NoUserPromotionPath
  difficulty: 难点三
  target_metrics: [CapabilityReuseRate, RepeatedGenerationRate, ReuseSpeedup]
- flag: NoAcceptanceGate
  difficulty: 难点三
  target_metrics: [ValidationFalseAcceptRate]
- flag: NoRegistryLookup
  difficulty: 难点三
  target_metrics: [RegistryLookupHitRate, RepeatedGenerationRate]
```

`ablation_mapping.schema.json` MUST enforce:

* list of exactly 8 entries;
* each `flag` ∈ the 8 ablation names from §3.2 (matrix.yaml ablation
  configuration enumeration);
* each `difficulty` ∈ {`难点一`, `难点二`, `难点三`};
* each `target_metrics` element is a member of
  `eval_metrics.metrics.ALL_METRICS` (the canonical 41-entry catalog
  in `multi-agent/tools/eval/metrics/eval_metrics/metrics/__init__.py`
  — this is the single source of truth for metric names, not
  `_OWNER_HINTS` which is only a stderr-warning hint map);
* a companion `test_ablation_mapping.py::test_yaml_matches_spec_table`
  parses the mapping table in **this spec file** (§3.3) — pulling the
  flag → difficulty → backtick-quoted metric-name list out of each
  row — and asserts, for each of the 8 flags, that the YAML entry's
  `flag`, `difficulty`, and `target_metrics` are exactly the values
  extracted from the spec table. Two exceptions the parser recognises:
  (i) prose fragments like "下游 side-effect ↑" that are not backtick-
  wrapped metric names are dropped from the extracted set; (ii) any
  parenthetical `(proxied by \`Foo\` + \`Bar\`)` extends the extracted
  set by the parenthetical metric names. The test enumerates these
  rules explicitly so a future spec-table edit that forgets to update
  the YAML fails.

`build_paper_tables.py --ablation-mapping tools/eval/fulltable/ablation_mapping.yaml`
consumes this YAML as its **only** grouping source.

### 3.4 `table2_ablation_sample.csv` — long-form layout

The output format is **one row per ablation flag** (8 rows), columns:

```
ablation_flag, difficulty_group, <metric_1>, <metric_2>, ..., <metric_M>
```

where `<metric_i>` iterates over the **union** of `target_metrics` across
all 8 mapping entries (order = first-appearance in the YAML above; cells
for flags that don't target that metric are blank). `difficulty_group`
is a **column** whose value ∈ {`难点一`, `难点二`, `难点三`}, not a
header-level pivot; this keeps the output as pandas-friendly long form
so downstream re-shaping (facet grid, groupby) works without another
step. `test_paper_table2_ablation.py` asserts the row count, the
column-order rule, and that every `<metric_i>` name exists in
`eval_metrics.metrics`.

## 4. Harness `run.sh`

`run.sh` is a bash script; per-run planning + aggregation live in Python
under `lib/`. It supports three modes.

### 4.1 `--dry-run` (default in this worktree; safe on CI)

Read `matrix.yaml`; for each of the 60 rows, `printf` the fully
expanded command that would execute. Row → command-type mapping per
§3.2:

* `full_loom` rows → `eval-runner run --workload <W> --stub-listen
  127.0.0.1:<port> --out … --timeout 3600s [...]` (no `--ablation`,
  no `--baseline-name` override; runner defaults stamp `full_loom`).
* Any of the 8 ablation-name rows → same as above plus
  `--ablation <name>` (name copied verbatim from matrix row, no
  CLI-side splicing).
* `manual_ssh`, `single_machine_claude_code`, `cloud_sandbox_e2b`
  rows → `bash tests/eval/baselines/<dir>/run.sh --workload <W>
  --out … [--dry-run for cloud_sandbox_e2b]` (see §7 (c)).

No subprocess is started. No file is written outside the printed
plan on stdout.

### 4.2 `--sample N` (fixture smoke; N ≤ 3 here)

Actually invoke `eval-runner` for the first N rows of `matrix.yaml`
(row order = §3.2 configuration order × §3.1 workload order,
deterministic). Each run gets:

* a fresh workspace tempdir (already handled by the runner);
* a unique loopback port from the pool (`lib/portpool.py`) for
  `--stub-listen 127.0.0.1:<port>` (§7 (a) + (b));
* a per-run observer SQLite at `tests/eval/results/smoke/dbs/<run_id>.db`
  (§7 (d));
* the runner's own `--timeout` flag (the actual CLI name in
  `multi-agent/tools/eval/runner/main.go:63`) set to `3600s` normally,
  but the fixture smoke uses `60s` per row (workloads have a real
  `timeout_seconds` inside spec.yaml; the harness passes the smaller
  value to the runner via `--timeout 60s` on smoke paths only, which
  overrides `spec.timeout_seconds`);
* one deliberate `sk-xxx`-tainted stderr line injected into the third
  smoke run via a fixture, to exercise the failure-scrub path (§7 (e)).

The smoke output roots under `tests/eval/results/smoke/`; no
non-`smoke/` `results/*` path is ever written. This is enforced by
`plan.py`'s output-path builder rejecting any path that does not start
with `tests/eval/results/smoke/`.

### 4.3 Hard cap — `--sample N > 3` requires `ALLOW_FULL_RUN=1`

`run.sh` rejects `--sample N` with `N > 3` unless the environment
variable `ALLOW_FULL_RUN=1` is set. On rejection: prints
`ErrFullRunNotAllowed: --sample N=4 exceeds smoke cap 3; set ALLOW_FULL_RUN=1`
to stderr and exits with code 2. This is a manual-safety fuse per §7 (k)
so that a careless paste in the follow-up worktree's setup notes does
not fire a 60-run job before it is intended. The follow-up worktree
sets `ALLOW_FULL_RUN=1` explicitly.

### 4.4 E4 three-stage handoff

The runner today has **no** `--stage {A,B,C}` CLI flag (verified against
`multi-agent/tools/eval/runner/main.go` at base HEAD `1185e99`). This
worktree therefore expresses Stage A/B/C on the **harness** side only,
in a **separate manifest** that does not perturb the 60-row `matrix.yaml`:

* `matrix.yaml` remains the 5 × 12 = 60-row product from §3.1/§3.2;
  Stage A/B/C annotation lives in a **sibling** file
  `multi-agent/tools/eval/fulltable/e4_stages.yaml` whose rows are
  drawn from a fixed enumeration:
  * **family** ∈ the 5 directory names under
    `multi-agent/tests/eval/golden/` **excluding** the non-family
    entries `acceptance`, `_shared`, and any `README.md` (13 号 §2
    reserves those as per-family sub-dirs / shared-fixture dirs);
  * **stage** ∈ {`A`, `B`, `C`};
  * **task_id** ∈ the fixed 4-tuple `{first-task, reuse-1, reuse-2,
    reuse-3}` (13 号 §2.4 line 490–494);
  * **configuration** ∈ the 4-tuple `{full_loom, NoUserPromotionPath,
    NoAcceptanceGate, NoRegistryLookup}` — Full Loom to populate the
    stage-headline Figure 3 bars, plus the three E4-specific
    ablations 08 号 E4 §Compare (line 194–197) names as the E4
    reference set (they are the only ablations whose §6.5.1 target
    metrics are E4-family metrics: `CapabilityReuseRate`,
    `RepeatedGenerationRate`, `ReuseSpeedup`, `ValidationFalseAcceptRate`,
    `RegistryLookupHitRate`). The other 5 ablations (`NoCapabilityDiscovery`,
    `NoObserver`, `NoTypedContracts`, `NoContractFormalization`,
    `NoDryRun`) target E1/E2/E3 metrics only and are aggregated
    exclusively from `matrix.yaml` runs; they do NOT appear in
    `e4_stages.yaml`. The 3 non-Loom baselines (`manual_ssh`, ...)
    likewise stay in matrix-scope only.

  Total rows: 5 families × 3 stages × 4 tasks × 4 configurations =
  **240 exactly**. Schema:
  `- family: <name>; stage: A|B|C; task_id: <task>; configuration: <name>`.
  Test asserts (a) row count = 240, (b) `configuration` ∈ the 4-tuple
  above, (c) no row uses a family/task_id/configuration outside this
  enumeration, (d) **full Cartesian coverage**: the set of
  `(family, stage, task_id, configuration)` tuples parsed from the
  YAML equals the exact `product(FAMILIES, STAGES, TASKS,
  CONFIGURATIONS)` set — every one of the 240 combinations appears
  exactly once, no duplicates. A per-family `acceptance` entry
  appearing in `e4_stages.yaml` fails the schema test.

  **The follow-up run worktree** will dispatch these 240 E4 rows in
  addition to the 60 matrix rows (total 300 planned dispatches). The
  wall-clock, parallelism, and model-gateway quota for that combined
  set are **the run worktree's responsibility to compute afresh** —
  this worktree does not project a multiplier off the todo_list's
  60-row estimate because per-run cost per configuration
  (`full_loom` vs `NoUserPromotionPath` vs baseline) varies
  meaningfully and the todo_list number is a single-configuration
  average. This worktree only **samples** ≤ 3 dispatches into the
  smoke fixture, so the true total doesn't need estimating here.
* `matrix.yaml` never carries a `stage` column; the two files are joined
  at aggregation time by `lib/stage_e4.py` on the 4-tuple
  `(family, stage, task_id, configuration)`. The E4 rows aggregated
  into `e4_stages_sample.csv` come exclusively from `e4_stages.yaml`,
  not from `matrix.yaml`. Because `configuration` participates in the
  join key, Full Loom rows and E4-ablation rows cannot merge.
* `e4_stages.yaml` is only exercised by aggregation, not by
  dispatched runs in **this** worktree (the smoke run does not
  actually execute E4 three-stage — it fills the sample with fixture
  numbers so column shape is verified). The 60-run follow-up worktree
  wires `--stage` into the runner (see the handoff paragraph below)
  and then dispatches E4 rows keyed by `e4_stages.yaml`.
* `lib/stage_e4.py` reads `e4_stages.yaml`, joins on the corresponding
  aggregated metrics per row, and produces `smoke/paper/e4_stages_sample.csv`.
  Column list (order):

  ```
  stage, family, task_id, configuration,
  TaskSuccessRate, TimeToCompletion, HumanEditCount, TokenUsage,
  RegistryLookupHitRate, CapabilityReuseRate, ReuseSpeedup,
  RepeatedGenerationRate, AdHocScriptTaskShare,
  GeneratedCapabilityDefectRate,
  PromotionCandidateSurfacingRate, UserInitiatedSynthesisSuccessRate,
  ValidationFalseAcceptRate, PromotionAdoptionRate,
  TimeFromUserDecisionToRegisteredMCP
  ```

  `configuration` participates in the key so the 240-row long-form
  table stays uniquely indexable on `(stage, family, task_id,
  configuration)`. `TaskSuccessRate` is required by 11 号 §4 Stage A
  (line 90); every stage collects it. Cells for metrics that don't
  apply to a given (stage, configuration) combination stay blank.
  Test asserts (a) every metric name ∈ `ALL_METRICS`, (b) the
  Stage-A `full_loom` row for the smoke fixture has a non-blank
  `TaskSuccessRate` cell, (c) the 4-tuple `(stage, family, task_id,
  configuration)` is unique across all rows (no duplicates).
* This worktree writes a **handoff issue file** at repo-root path
  `docs/specs/wt3-stub-fulltable.handoff.md` (relative to the git
  worktree root, i.e. same directory as this spec file). It is one
  paragraph explaining what change the follow-up run worktree needs in
  the runner (add `--stage {A,B,C}` flag that stamps a new `runs.stage`
  column). The handoff file is discoverable by grepping the smoke
  README and by `test_stage_e4_handoff.py`, which asserts the file
  exists at that exact path and contains the literal string
  `"Stage A/B/C flag handoff to runner"`.
* **This worktree does not verify** the Stage-C trend (`ReuseSpeedup > 1`)
  or Stage-B latency; only the structural aggregation is exercised. The
  fixture smoke fills those cells with placeholder numbers; the test
  asserts column presence, not value magnitudes.

### 4.5 `--resume`

`--resume` uses a **stable key** — not the runner's UUIDv4 `run_id`.
UUIDs are freshly generated on every plan invocation, so they cannot
map a completed row back to its planned row. Two key shapes, one per
source manifest:

```
# Matrix rows (matrix.yaml, 60 rows)
matrix_resume_key = f"matrix__{workload_id}__{baseline_or_ablation}"

# E4 rows (e4_stages.yaml, 240 rows) — task_id + configuration keep
# the sibling tasks and per-configuration variants distinct.
e4_resume_key    = f"e4__{family}__{stage}__{task_id}__{configuration}"
```

Both shapes share the same `__` separator and the same rsplit-parsing
rule below. The `matrix__` / `e4__` prefix disambiguates the two
scopes at glob time.

**Resume skip rule.** `run.sh --resume` skips **any** planned row,
whether from `matrix.yaml` or from `e4_stages.yaml`, whose computed
`resume_key` appears in `sorted(glob("smoke/runs/*.done"))` — the
harness computes the row's key at plan time using its manifest kind
(matrix or e4), globs the sidecar directory once at start, and
short-circuits both dispatch loops. Test parametrises both prefixes:
one fixture pre-populates a `matrix__…done` file and asserts the
matching matrix row is skipped; a second pre-populates an `e4__…done`
file and asserts the matching E4 row is skipped; both leave the other
scope's rows untouched.

Each completed run writes a **sidecar** file
`tests/eval/results/smoke/runs/<resume_key>__<run_id>.done` on
success — a zero-byte marker whose basename recovery uses
`basename.rsplit("__", 1)` (Python) to split into exactly two parts:
the left is the full `resume_key` (which itself contains two `__`
separators — that's fine because rsplit with maxsplit=1 only splits
the last `__`), and the right is `<run_id>.done`.
This keeps the runner's `run_id` (UUIDv4) traceable back to the row
in `runs.csv` for operators, while `resume_key` is what `--resume`
actually indexes on. `run.sh --resume` globs `smoke/runs/*.done` at
startup and skips **any** planned row (from either `matrix.yaml` or
`e4_stages.yaml`) whose parsed `resume_key` is present in the set —
covering both `matrix__…` and `e4__…` prefixes uniformly. Partial /
failed runs leave no `.done` file, so they are retried on the next
`--resume` invocation; the harness deletes any stale
`smoke/runs/<resume_key>__*.csv` from the failed attempt before
retrying (so `--out` refuse-if-exists guard doesn't fire).

`--resume` here **only** consults the `smoke/` directory; the
follow-up worktree will point it at `tests/eval/results/` instead
(the resume-key mechanism is unchanged, only the base directory).

### 4.6 Parallelism scaffold

`run.sh --parallel N` accepts N ≥ 1; harness default is N=1 in this
worktree (fixture smoke sequential, easier to inspect on failure). The
plan.py planner enumerates all 60 runs and dispatches them; the actual
concurrency knob is a semaphore in `run.sh`. The follow-up worktree may
raise N to 4–8; nothing about this scaffold is single-threaded by
design. The port pool (§7 (b)) makes parallel dispatch safe.

## 5. Aggregation — `build_paper_tables.py`

CLI:

```
build_paper_tables.py
  --metrics             tests/eval/results/smoke/metrics.csv
  --runs                tests/eval/results/smoke/runs.csv
  --ablation-mapping    tools/eval/fulltable/ablation_mapping.yaml
  --e4-stages           tools/eval/fulltable/e4_stages.yaml
  --out-dir             tests/eval/results/smoke/paper/
  --sample-mode         (adds _sample suffix to every output file basename)
```

All five inputs are required (no defaults) — a silent-typo fuse mirroring
the eval-metrics CLI's `--format required` rule. Test asserts a missing
`--e4-stages` argument exits 2 with a clear error naming the missing
flag.

Outputs (in `--sample-mode`; the follow-up worktree drops `--sample-mode`
to produce non-suffixed paper artefacts):

1. **`table2_sample.csv`** — Paper Table 2 "Macrobenchmark: Full Loom vs
   baselines on success, time, interventions, wrong context, lifecycle
   closure" (08 号 line 244). Columns are drawn verbatim from the
   `eval_metrics.metrics.ALL_METRICS` catalog by name; the caption's
   five phrases map to metric names as follows:

   | Caption phrase        | Metric name (from ALL_METRICS)  |
   |-----------------------|---------------------------------|
   | success               | `TaskSuccessRate`               |
   | time                  | `TimeToCompletion`              |
   | interventions         | `HumanContextSelectionCount`    |
   | wrong context         | `WrongContextFailureRate`       |
   | lifecycle closure     | `LifecycleClosureRate`          |

   Column list (exact order — this is the header the test greps for):

   ```
   workload_id, configuration, TaskSuccessRate, TimeToCompletion,
   HumanContextSelectionCount, WrongContextFailureRate,
   LifecycleClosureRate, run_count
   ```

   One row per (workload_id × configuration) cell — 60 rows in the
   60-run case; 3 rows for the smoke fixture (the smoke skips cells
   the sample-run subset didn't touch). Test asserts the header
   sequence character-for-character against this list and asserts
   every metric-name column ∈ `ALL_METRICS`.

2. **`table2_ablation_sample.csv`** — 8-row long-form ablation table per
   §3.4. Columns:
   `ablation_flag, difficulty_group, <metric_1>, …, <metric_M>` where the
   metric union is exactly the union of `target_metrics` across the 8
   `ablation_mapping.yaml` entries.

3. **`figure1_data_sample.json`** — Paper Figure 1 "Semantic routing:
   routing accuracy and wrong-context failures under Full Loom /
   ablations" (08 号 line 245). Shape:
   `{"configurations": [...], "series": {"RoutingAccuracy": {conf: value},
   "WrongContextFailureRate": {conf: value}}}`. The `configurations`
   set is exactly `full_loom` + the 8 ablation flags (9 entries; the
   3 baseline names are excluded because the caption is explicitly
   "Full Loom / ablations", not "baselines"). Test asserts (a) the
   top-level `configurations` list equals the 9-entry set
   `{"full_loom"} ∪ ABLATIONS` (order = enumeration in §3.2 minus the
   3 baseline entries), (b) each `series` sub-dict's key set equals
   that same 9-entry set exactly — no missing ablation, no extra
   baseline — so a Figure 1 that quietly drops `NoObserver` fails
   the test just as loudly as one that includes `manual_ssh`.

4. **`figure2_data_sample.json`** — Paper Figure 2 "Contract fault
   injection: pre-execution catches and contract violations by fault
   type" (08 号 line 246). Shape:

   ```json
   {"fault_types": [...8 wire values...],
    "series": {"PreExecutionFaultCatchRate": {ft: value},
               "ContractViolationRate":      {ft: value}}}
   ```

   The `fault_types` list is drawn **verbatim** from
   `multi-agent/tools/eval/faultinject/kinds.go` `AllFaultKinds`
   (declaration order, 8 entries):

   ```
   missing_file, stale_capability, wrong_os_version, forbidden_cred,
   slave_disconnect, driver_restart, model_route_failure,
   duplicate_pickup
   ```

   The test asserts the list equals `AllFaultKinds` (order-sensitive)
   by reading the Go source at test time — drift caught immediately.

5. **`figure3_data_sample.json`** — Paper Figure 3 "User-promoted
   capability lifecycle closure" three-stage overlay (08 号 line 247;
   11 号 §4 line 90/95/100). Shape binds each stage bar to the
   specific metric the caption promises:

   ```json
   {
     "families": ["api-wrapper-for-local-service", "csv-profiler",
                  "image-metadata-extractor", "log-parser",
                  "refund-policy-checker"],
     "stage_A": {
       "metric": "TimeToCompletion",
       "subkey": "p50_seconds",
       "unit":   "seconds",
       "values": {family: value}
     },
     "stage_B": {
       "metric": "TimeFromUserDecisionToRegisteredMCP",
       "subkey": "p50_seconds",
       "unit":   "seconds",
       "values": {family: value}
     },
     "stage_C": {
       "metric": "ReuseSpeedup",
       "subkey": "ratio",
       "unit":   "ratio (Stage A p50_seconds / Stage C p50_seconds)",
       "values": {family: value}
     },
     "AdHocScriptTaskShare": {
       "subkey": "share_0_to_1",
       "values": {family: value_in_0_to_1}
     }
   }
   ```

   Rationale: Stage-A "ad-hoc baseline cost" ← `TimeToCompletion.p50_seconds`
   (11 号 §4 line 90); Stage-B "user-decision-to-registered cost" ←
   `TimeFromUserDecisionToRegisteredMCP.p50_seconds` (11 号 §4 line 94);
   Stage-C "reuse amortization" ← `ReuseSpeedup.ratio` (11 号 §4 line 100);
   `AdHocScriptTaskShare.share_0_to_1` overlay (08 号 line 247 caption).
   The family enum uses the exact golden directory names from
   `multi-agent/tests/eval/golden/` — Phase 0 WT-0-task-families
   fixed `api-wrapper-for-local-service` as the full family name
   (13 号 §2.4 line 163).

   **Configuration filter.** Figure 3 by construction is a full_loom
   comparison across stages (08 号 line 247 caption: "Stage-A ad-hoc
   baseline cost、Stage-B user-decision-to-registered 升格成本、Stage-C
   reuse 摊销速度三段对照" — no ablation dimension). The aggregator
   filters `e4_stages_sample.csv` to `configuration == "full_loom"`
   before feeding Figure 3. The E4-ablation rows
   (`NoUserPromotionPath` / `NoAcceptanceGate` / `NoRegistryLookup`)
   remain in `e4_stages_sample.csv` and flow through the
   `table2_ablation_sample.csv` §6.5 ablation column set — that is
   where the paper attributes each ablation's E4 metric delta. Test
   asserts (a) every Figure 3 `values` map covers all 5 families,
   (b) the aggregator's Figure 3 filter is exactly
   `configuration == "full_loom"`, and (c) the input rows selected
   for each stage number exactly 20 (5 families × 4 tasks) after the
   filter.

   Test also asserts (a) the exact `metric` **and** `subkey` string per
   stage, (b) every family key ∈ the sorted list of directory names
   under `multi-agent/tests/eval/golden/`, (c) each `metric` value ∈
   `ALL_METRICS`, and (d) `families` array is identical to the sorted
   golden-directory list (drift caught immediately).

6. **`figure4_data_sample.json`** — Paper Figure 4 "Overhead breakdown:
   driver / AgentServer / tunnel / observer / ModelServer/App proxy"
   (08 号 line 248 + §E5 Overhead microbenchmarks + 12 号 §D7). Shape
   covers the seven §D7 overhead metrics; the first five are the
   Figure-4 caption bars, the last two are §D7's extra structured
   overhead metrics that the paper writer will decide whether to plot
   or drop into an appendix (kept in the sample so column shape is
   ready either way):

   ```json
   {
     "components": ["DriverPlanningOverhead", "TaskDispatchLatency",
                    "TunnelOverhead", "ObserverOverhead",
                    "ModelProxyOverhead"],
     "series": {
       "p50_ms": {comp: value},
       "p95_ms": {comp: value}
     },
     "extras": {
       "ArtifactTransferThroughput": {
         "MiBps_p50": v, "MiBps_p95": v,
         "artifact_size_bytes": [1024, 1048576, 104857600]
       },
       "RoutingLatencyP50P95": {
         "p50_ms": v, "p95_ms": v,
         "scale_points": {
           "contexts":            [1, 2, 4, 8, 16],
           "tools_per_context":   [10, 50, 100],
           "artifact_size_bytes": [1024, 1048576, 104857600]
         }
       }
     }
   }
   ```

   The three scale axes (contexts × tools/context × artifact size)
   match 08 号 §E5 Scale points verbatim (line 209–212) and 12 号 §D7.
   Component list mirrors §E5 Overhead microbenchmarks + `ALL_METRICS`
   `overhead` section names (numbers 31–37). Test asserts (a) every
   name in `components` ∪ `extras` keys ∈ `ALL_METRICS`, and (b) the
   three scale-axis arrays match 08 号 §E5 exactly.

7. **`e4_stages_sample.csv`** — per §4.4.

**Empty / null policy.** When a metric returns null (upstream data
missing, denominator zero — 08 号 §Metrics + `eval_metrics` §7 (g)) the
sample fills the cell with a blank string, matches the aggregation
behaviour of the follow-up worktree; nothing is silently zeroed.

**Reverse consumer view (§7 (i)).** Every sample file passes a round-trip
`pd.read_csv` / `json.load` test that yields a DataFrame or dict with
column / key names identical to the paper caption in 08 号 §Table 2 /
Figure 1–4. This is why the sample step is done here even though the
follow-up worktree will replace the values.

## 6. Deliverables

* `matrix.yaml` — 60 rows.
* `ablation_mapping.yaml` — 8 rows.
* `e4_stages.yaml` — 5 families × 3 stages × 4 tasks × 4 configurations
  = 240 rows (§4.4).
* `matrix.schema.json`, `ablation_mapping.schema.json`,
  `e4_stages.schema.json` — jsonschema files.
* `run.sh` — orchestrator.
* `lib/` — planning, port pool, commit-meta, failure-scrub, stage-E4,
  aggregation, paper-table writers.
* `build_paper_tables.py` — aggregation CLI.
* `tests/` — 14 tests, one per §7 line + §3–§5 structural.
* `README.md` — usage + hand-off block for the run worktree.
* `docs/specs/wt3-stub-fulltable.handoff.md` — one-paragraph runner-flag
  handoff for E4 stage column (path exactly as in §0 scope and §4.4).
* `tests/eval/results/smoke/*` — fixture smoke output (3 runs), plus
  `smoke/README.md` with the smoke-only warning (§7 (j)).

Nothing else is written. Nothing under
`tests/eval/results/{runs,metrics,paper,dbs,failures}.*` is created.

## 7. Security & correctness posture

Each item (a)–(k) has one test in §8 / plan §Test matrix (1:1 mapping).

### (a) Stub bind must be loopback

The harness generates `--stub-listen 127.0.0.1:<port>` **only**; it
rejects `0.0.0.0`, `[::]`, or any non-loopback address at plan time.
Rationale: the agentserver-stub signs bearer tokens with no OAuth
challenge; a stub bound to `0.0.0.0` on a shared host lets any local
network peer pull a fully-authorised token five-tuple. Test:
`test_stub_listen_loopback.py` — asserting `assign_port` refuses
`0.0.0.0` and any external IP; assert the built CLI string in every
matrix row starts with `--stub-listen 127.0.0.1:`.

### (b) Port pool with conflict detection

Before allocating a port, `portpool.py` `try_bind(port)` calls
`socket.socket(); s.bind(("127.0.0.1", port)); s.close()`. On
`OSError(EADDRINUSE)` it advances to the next port; the pool is a
contiguous range starting at `18100` reserved to this worktree (chosen
so it does not overlap the runner's default `18080` or the Phase 2
stub tests). Rationale: parallel smoke runs must not collide on the
same loopback port when `--parallel > 1`; the smoke exercises the
retry with a synthetic `already-bound` fixture. Test:
`test_port_pool.py`.

### (c) Cloud baseline forced dry-run

Every `cloud_sandbox_e2b` row in `matrix.yaml` has `dry_run: true` set;
`plan.py` refuses to emit a plan for a cloud row where `dry_run` is
false; the runner's baseline CLI is invoked with `--dry-run`. Rationale:
the cloud baseline is the only configuration that would dial an
external service in the follow-up 60-run; the stub-fulltable scope
explicitly does not burn any cloud quota, and even the follow-up
worktree stays dry-run for `cloud_sandbox_e2b` (per Phase 2 WT-2-
baselines §7(c) — real E2B is reserved for `WT-3-prod-multidevice`).
Test: `test_cloud_baseline_dry_run.py`.

### (d) Per-run observer SQLite

Each smoke run writes to `tests/eval/results/smoke/dbs/<run_id>.db`.
`<run_id>` is a UUIDv4 generated by `plan.py` (or the runner's own
default) at plan time; the file is created by the runner. Rationale:
SQLite serialises writers on a per-file basis and holds a fcntl lock;
two parallel runs against one file get `SQLITE_BUSY` at random,
which corrupts audit trails and drops rows. Test:
`test_per_run_observer_db.py` — asserts each of 3 smoke runs opened a
distinct file, and that concurrent write attempts against a shared
file surface as a fatal error (guard is present) rather than silent
last-writer-wins.

### (e) Failure record: secretscrub + tail 200 lines

When a runner exits non-zero, the harness records a JSONL row in
`smoke/failures.jsonl` with:

```json
{"run_id": "...", "configuration": "...", "workload_id": "...",
 "exit_code": N, "tail_stderr": ["line-1", ..., "line-200"]}
```

`tail_stderr` is the last ≤ 200 lines of stderr passed through
`internal/secretscrub` (already used by the runner for its own row
redaction) via a small Python shim that calls the Go binary or
re-implements the same regex set (spec: pattern set = `sk-[A-Za-z0-9]{20,}`,
`gh[a-z]_[A-Za-z0-9]{20,}`, bearer-token headers). The fixture smoke
injects a stderr containing `sk-abcdefghij0123456789xxxx`; the test
asserts (1) the string appears redacted as `sk-REDACTED` (or similar),
(2) the array is at most 200 elements. Test: `test_failure_scrub.py`.

### (f) commit_meta clean-tree assertion

Before dispatching any smoke run, `commit_meta.py`:

1. runs `git rev-parse HEAD` — records `loom_commit`;
2. runs `git status --porcelain --untracked-files=all` — if output is
   non-empty (ANY staged, unstaged, or untracked-and-not-gitignored
   path) harness exits 2 with `ErrDirtyWorktree` and prints the first
   20 offending paths. Rationale: a `git diff --quiet` pair would miss
   an untracked `.env` or a stray shell script that the runner might
   pick up at exec time — untracked-not-ignored is exactly the class
   of change that produces a "run works on my machine only" artifact.
3. asserts the runner's `runs.loom_commit` equals the harness-side
   `rev-parse HEAD`.

The runner already collects commit_meta and stamps `dirty` when tree
is dirty (`multi-agent/tools/eval/runner/runner.go:788 collectCommitMeta`);
the harness cross-checks. Rationale: mixing a dirty tree into a paper
run wrecks reproducibility; the runner-side check is post-hoc, the
harness-side check is pre-flight. Test: `test_commit_meta.py`
parametrises three cases — staged-dirty, unstaged-dirty, and
untracked-not-gitignored — each asserted to fail with `ErrDirtyWorktree`;
a fourth case (untracked-but-in-`.gitignore`) is asserted to pass.

### (g) `baseline_or_ablation` value comes from matrix, not CLI

The harness never accepts a CLI flag overriding the
`baseline_or_ablation` column of a row; the schema test rejects a typo
in the matrix. Rationale: a typo `NoAccetpanceGate` (transposed 'p'
and 't') is silently treated by the runner as "no ablation matching
this name → run with all flags off" and produces an ambiguous row.
Two defences: (1) schema validation of `matrix.yaml` enum; (2) harness
does not construct the `--ablation` list itself, it copies the string
from the parsed matrix row through `plan.py` which validates each
name against `internal/ablation/registry.go`'s `FlagName` list (via a
hard-coded 8-name Python whitelist that mirrors §3.2). Test:
`test_matrix_schema.py::test_ablation_typo_rejected`.

### (h) CI posture

The 3-run smoke is NOT wired to CI (it needs the agentserver-stub +
runner + observer to start real subprocesses, which is unreliable on
GitHub Actions runners). What CI runs from this worktree:

* `pytest tools/eval/fulltable/tests/` (the tests that don't need
  subprocess);
* `bash tools/eval/fulltable/run.sh --dry-run` snapshot compare.

Rationale: CI catches config drift on every PR without opening the
"flaky-because-subprocess" door. Test: `test_dry_run_snapshot.py`.

### (i) Reverse consumer view

Every `smoke/paper/*_sample.*` file is loaded back with
`pd.read_csv` / `json.load` in the corresponding test; the frames /
dicts are asserted to have exactly the column list / key set the paper
caption in 08 号 §Table 2 / §Figure 1–4 uses. Rationale: if the
follow-up worktree drops in real numbers and the columns don't match
the paper, the reviewer only finds out during writing. Test:
`test_paper_table2_sample.py`, `test_paper_table2_ablation.py`, plus
per-figure tests.

### (j) Smoke-only declaration

`tests/eval/results/smoke/README.md` contains, exactly:

> **本目录数据是脚手架 smoke，非论文用真数据；60 run 真跑归后续
> `paper/v3/p3-stub-fulltable-run` worktree。**

The test greps for this exact string. Every paper artefact under
`smoke/paper/` has a filename ending in `_sample.{csv,json}` (§2). The
combination — content warning + suffix — makes it structurally hard for
a reader or a paper-writing tool to pick up smoke data as if it were
the paper's real number. Test: `test_smoke_readme_warning.py`.

### (k) Hard cap on `--sample N`

`run.sh --sample N`: `N > 3` requires `ALLOW_FULL_RUN=1` in env,
otherwise exit 2 with `ErrFullRunNotAllowed`. `N ≤ 3` runs unconditionally.
Rationale: a stray `run.sh --sample 60` copied from README lore would
otherwise take 4–8 h and burn model gateway; the `ALLOW_FULL_RUN` fuse
is set once, by the run worktree, on purpose. This worktree's tests
verify the reject path; the accept path (`ALLOW_FULL_RUN=1 --sample 4
--dry-run`) is exercised in `--dry-run` mode only to confirm the
argument parses. Test: `test_hard_cap_full_run.py`.

## 8. Acceptance

* Codex clean on spec / plan / code (P0/P1 = 0).
* `matrix.yaml` exactly 60 rows; schema test passes on every configuration
  enum value.
* `ablation_mapping.yaml` exactly 8 rows; schema test passes;
  semantically identical to §3.3 mapping table under the parser rules
  defined at the end of §3.3 (backtick-metric-only extraction with
  proxied-expansion for `NoDryRun`'s parenthetical).
* `run.sh --dry-run` prints 60 CLIs; snapshot test passes.
* `run.sh --sample 4` exits 2 without `ALLOW_FULL_RUN`.
* `run.sh --sample 3` produces `tests/eval/results/smoke/` structure with 3
  runs.csv rows, 3 dbs, ≥1 failure.jsonl row (fixture inject), and one
  `sk-*`-redacted stderr tail; `smoke/README.md` warning line matches.
* `build_paper_tables.py --sample-mode` produces the 7 sample files;
  every column/key aligns to 08 号 §Table 2 / §Figure 1–4.
* No file created outside the scope in §6.
* No diff touches `multi-agent/tools/eval/runner/`,
  `multi-agent/internal/`, or any Phase 0/1/2 code.
* Commit trailer:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
* Not pushed.

## 9. Non-goals (explicitly)

* Real 60-run production. Goes to `paper/v3/p3-stub-fulltable-run`.
* Any real cloud API call. `cloud_sandbox_e2b` stays dry-run through
  both this worktree AND the follow-up run worktree (real E2B is
  `WT-3-prod-multidevice`).
* Any prod-multidevice / real-OAuth path. That's `WT-3-prod-multidevice`.
* Any change to the runner CLI, ablation registry, or the metric
  extractor. E4 `--stage` runner flag is a handoff (§4.4).
* Populating the paper text with numbers. Numbers are the follow-up run
  worktree's output; column shape is this worktree's output.

## 10. References

* todo_list.md line 129 — WT-3-stub-fulltable row.
* 08 号 §Metrics, §Data collection schema, §Table 2, §Figure 1–4
  (line 30–248).
* 11 号 §4 — E4 three-stage (Stage A ad-hoc / Stage B user-promoted /
  Stage C reuse) semantics.
* 12 号 §E1–§E5 (baseline + ablation), §H step 7 (execution order),
  §J.1 (stub auto path).
* 13 号 §1 (workload id enumeration), §2 (E4 families + acceptance
  golden), §3 (ground-truth labels).
* Phase 2 WT-2-flag-integration §2.3 (`--ablation` CLI, ablation
  FlagName consts in `internal/ablation/registry.go`).
* Phase 2 WT-2-baselines §7 (baseline naming + `baseline_or_ablation`
  regex + cloud-dry-run posture).
* Phase 2 WT-2-metric-extract §Metrics catalog (`eval_metrics.metrics`).
* `paper_outputs/evaluation_v3.md` §6.5.1 — normative ablation ↔ 难点
  ↔ target-metric mapping.
