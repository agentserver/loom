# WT-3-stub-fulltable — Plan

> Companion to `wt3-stub-fulltable.spec.md`. TDD order **within each
> numbered task**: write the failing test first, then the minimal impl
> to pass, then the next test.
>
> This worktree is **harness-only, no 60-run** (spec §0). Every step
> below either produces the scaffold, the ≤ 3-run fixture smoke, or
> the aggregation to paper-table sample structure. **No step in this
> plan runs the 60-row matrix or the 240-row E4 manifest.** No step in
> this plan touches `multi-agent/tools/eval/runner/*.go`,
> `multi-agent/internal/*`, or any Phase 0/1/2 code — if a step
> requires such a change, stop and file a handoff paragraph in
> `docs/specs/wt3-stub-fulltable.handoff.md`.

## 0. Baseline HEAD

`origin/paper/v3-integration` at `1185e99`.

## 1. Files (created in this order)

All new content is under
`multi-agent/tools/eval/fulltable/` and `multi-agent/tests/eval/results/smoke/`;
handoff sits alongside the spec at
`docs/specs/wt3-stub-fulltable.handoff.md`. Order groups files by TDD
phase, not by directory.

### Phase A — schemas + planners (pure functions, no I/O beyond the yaml/json input)

0. `lib/__init__.py` and `tests/__init__.py`
   — empty package markers so both packages import as
   `tools.eval.fulltable.lib.*` and `tools.eval.fulltable.tests.*`
   (spec §2 layout).
1. `matrix.schema.json`
   — jsonschema describing the 60-row matrix. Fields: `workload_id`
   ∈ 5-value enum from spec §3.1; `baseline_or_ablation` ∈ 12-value
   enum from spec §3.2; optional `dry_run: bool` (default false,
   forced true for `cloud_sandbox_e2b`). Uses draft 2020-12.
2. `matrix.yaml`
   — 60 rows, deterministic order **configuration outer ×
   workload inner** (spec §4.2: "row order = §3.2 configuration
   order × §3.1 workload order"). So the first 5 rows are all
   `full_loom` × 5 workloads (in §3.1 order), the next 5 are
   `NoCapabilityDiscovery` × 5 workloads, etc. `--sample 3` therefore
   picks the first 3 `full_loom` rows. `cloud_sandbox_e2b` rows all
   have `dry_run: true` set.
3. `ablation_mapping.schema.json` + `ablation_mapping.yaml`
   — 8 rows as spec §3.3.
4. `e4_stages.schema.json` + `e4_stages.yaml`
   — 240 rows = 5 × 3 × 4 × 4 as spec §4.4.
5. `lib/plan.py`
   — pure functions: `parse_matrix`, `parse_e4_stages`,
   `plan_command_for_matrix_row`, `plan_command_for_e4_row`,
   `resume_key_for_matrix_row`, `resume_key_for_e4_row`.
   No subprocess; no fs writes.
6. `lib/portpool.py`
   — pure functions plus one syscall (`socket.bind`) with a mockable
   seam; supports contiguous-range allocation starting 18100.
7. `lib/commit_meta.py`
   — pure functions plus two subprocess calls
   (`git rev-parse HEAD`, `git status --porcelain
   --untracked-files=all`) with a mockable `_git` seam.
8. `lib/failure_scrub.py`
   — pure secretscrub regex (mirrors internal/secretscrub Go patterns)
   + `tail_lines(text, n=200)`; test injects `sk-abc…` and asserts
   redaction.
9. `lib/stage_e4.py`
   — pure joins + aggregation on the (family, stage, task_id,
   configuration) 4-tuple; no I/O.
10. `lib/aggregate.py`
    — pure aggregation of per-run rows into per-configuration means /
    p50s; consumes an in-memory list, no fs.
11. `lib/paper_tables.py`
    — pure writers for the 7 sample files (§5); consume dicts, write
    to a caller-supplied `Path`.

### Phase B — CLIs (thin wrappers around Phase A)

12. `build_paper_tables.py`
    — argparse CLI with the 5 required inputs from spec §5; calls
    into Phase A modules.
13. `run.sh`
    — bash orchestrator. Modes: `--dry-run`, `--sample N`,
    `--resume`, `--parallel N`. Hard cap on `--sample N > 3` unless
    `ALLOW_FULL_RUN=1`. Delegates planning to `lib/plan.py` (via
    `python3 -m lib.plan …`), delegates dispatch to `eval-runner` /
    `tests/eval/baselines/<dir>/run.sh`.

### Phase C — fixture smoke

14. `tests/eval/results/smoke/README.md`
    — smoke-only warning (spec §7 (j) exact string).
15. Run `bash tools/eval/fulltable/run.sh --sample 3` — this actually
    starts stub + runner for 3 rows, produces `smoke/runs.csv`,
    `smoke/metrics.csv`, `smoke/failures.jsonl`, `smoke/dbs/*.db`.
16. Run `python tools/eval/fulltable/build_paper_tables.py
    --sample-mode …` — produces the 7 `smoke/paper/*_sample.*` files.
17. Commit smoke output. (Yes, we do commit smoke fixtures; they are
    evidence the scaffold works, and the schema tests parse them.)

### Phase D — handoff + README

18. `docs/specs/wt3-stub-fulltable.handoff.md`
    — one-paragraph runner-flag handoff for E4 `--stage` (spec §4.4).
    Contains the literal string `"Stage A/B/C flag handoff to runner"`
    for the test to grep.
19. `multi-agent/tools/eval/fulltable/README.md`
    — usage doc; explains the three run modes; explains that the
    60/240 dispatch belongs to `paper/v3/p3-stub-fulltable-run`;
    lists the exact `ALLOW_FULL_RUN=1` command the run worktree will
    use (in a fenced block clearly labelled "for the run worktree
    only"); does NOT include a `nohup` example.

## 2. Test matrix

Each row maps to at least one spec §7 (a)–(k) item AND to a Phase A/B
step above. The tests live under
`multi-agent/tools/eval/fulltable/tests/`.

| Test file (Python) | What it verifies | Spec § | Files under test |
|---|---|---|---|
| `test_matrix_schema.py` | matrix.yaml is exactly 60 rows; every workload_id ∈ §3.1 enum; every baseline_or_ablation ∈ §3.2 enum; a `NoAccetpanceGate` typo in the YAML rejects; a `cloud_sandbox_e2b` row without `dry_run: true` rejects | §3.1 + §3.2 + §7 (c) + §7 (g) | matrix.yaml, matrix.schema.json |
| `test_ablation_mapping.py` | ablation_mapping.yaml is exactly 8 rows; each `flag` ∈ 8-value enum; each `difficulty` ∈ {难点一, 难点二, 难点三}; each `target_metrics` entry ∈ `eval_metrics.metrics.ALL_METRICS`; `test_yaml_matches_spec_table` parses the spec §3.3 markdown mapping table (backtick-metric extraction plus the `NoDryRun` parenthetical proxy expansion rule) and asserts each YAML entry equals what the spec table says | §3.3 + §7 (i) | ablation_mapping.yaml, spec.md |
| `test_e4_stages_yaml.py` | e4_stages.yaml is exactly 240 rows; the set of `(family, stage, task_id, configuration)` tuples equals `product(FAMILIES, STAGES, TASKS, CONFIGURATIONS)`; a per-family `acceptance` entry rejects; a row with `configuration: manual_ssh` rejects | §4.4 + §7 (g) | e4_stages.yaml, e4_stages.schema.json |
| `test_stub_listen_loopback.py` | `plan.py.build_stub_listen(port)` returns `127.0.0.1:<port>` only; parsing a matrix row that somehow contained `0.0.0.0` rejects; every planned CLI string starts with `--stub-listen 127.0.0.1:` | §7 (a) | plan.py, matrix.yaml |
| `test_port_pool.py` | `portpool.assign_port` allocates from the reserved range 18100+; a `EADDRINUSE` fixture on port 18100 causes the pool to advance to 18101 and succeed; two concurrent `assign_port` calls in the same process return different ports | §7 (b) | portpool.py |
| `test_cloud_baseline_dry_run.py` | For every `cloud_sandbox_e2b` row in matrix.yaml, the planned CLI contains `--dry-run`; hand-crafted row without `dry_run: true` causes `plan.py` to raise `ErrCloudDryRunMissing` | §7 (c) | plan.py, matrix.yaml |
| `test_per_run_observer_db.py` | Fixture smoke (or a synthesised 3-row dispatch) produces 3 distinct files under `smoke/dbs/`; each `<run_id>.db` exists after the smoke run; simulating two dispatches to the SAME `<run_id>.db` path raises a fatal error (not silent last-writer-wins) | §7 (d) | plan.py, run.sh |
| `test_failure_scrub.py` | Feeding stderr `ERROR: sk-abcdefghij0123456789xxxx` into `failure_scrub.scrub_and_tail(text)` produces a scrubbed string that no longer contains `sk-abc…`; a 500-line input produces a result of exactly 200 lines; `smoke/failures.jsonl` (from the smoke run) contains a redacted `sk-` entry | §7 (e) | failure_scrub.py, smoke fixture |
| `test_commit_meta.py` | Four cases parametrised via a tempfile git repo: (a) staged-dirty → exit 2 / `ErrDirtyWorktree`; (b) unstaged-dirty → same; (c) untracked-not-ignored → same; (d) untracked-but-gitignored → passes; happy path (clean tree) → returns `loom_commit == git rev-parse HEAD`. Also covers `verify_runs_loom_commit(runs_csv_path, expected_commit)` — a helper that reads a runs.csv and asserts every row's `loom_commit` cell equals the given expected commit; raises `ErrLoomCommitMismatch` on any drift. | §7 (f) | commit_meta.py |
| `test_commit_meta_preflight.py` | Integration test wiring `run.sh` to `commit_meta.py`. Sets up a tempfile git worktree with the fulltable harness copied in. Parametrised cases mirror spec §7 (f) exactly: (a) staged-dirty → exit 2 + stderr `ErrDirtyWorktree`; (b) unstaged-dirty → same; (c) untracked-not-gitignored → same; (d) untracked-but-gitignored → **passes** preflight. To keep the CI test suite subprocess-free per spec §7 (h), the test sets an env var `LOOM_FULLTABLE_DISPATCH_SHIM=1` that `run.sh` recognises: when set, `run.sh` runs the commit_meta preflight normally (so the dirty cases still exit 2), then instead of exec'ing `eval-runner` / baseline `run.sh`, it prints a single line `SHIM: would dispatch <n> rows` to stdout and exits 0. Case (d)'s happy-path assertion is: exit 0 and stdout contains that line, proving preflight passed and dispatch planning was reached — no real runner / stub / observer subprocess is ever started. | §7 (f) | run.sh + commit_meta.py |
| `test_dry_run_snapshot.py` | Running `bash tools/eval/fulltable/run.sh --dry-run` prints exactly 60 CLI lines (baseline rows use the baseline `run.sh` prefix, other rows use `eval-runner run`); each cloud row line contains `--dry-run`; snapshot diff against a checked-in golden file at `tests/dry_run_snapshot.txt` | §7 (h) + §4.1 | run.sh, plan.py |
| `test_paper_table2_sample.py` | `smoke/paper/table2_sample.csv` header is exactly `workload_id,configuration,TaskSuccessRate,TimeToCompletion,HumanContextSelectionCount,WrongContextFailureRate,LifecycleClosureRate,run_count`; every metric column name ∈ `ALL_METRICS`; `pd.read_csv` round-trips into a DataFrame with the same column list | §5 + §7 (i) | build_paper_tables.py |
| `test_paper_table2_ablation.py` | `smoke/paper/table2_ablation_sample.csv` has exactly 8 rows; header equals **exactly** `["ablation_flag", "difficulty_group"] + first_appearance_union(row.target_metrics for row in ablation_mapping.yaml)` (order = first-appearance walk over the 8 YAML entries in file order; per spec §3.4 "column-order rule"); every metric column ∈ `ALL_METRICS`; `difficulty_group` cell values ∈ {难点一, 难点二, 难点三} | §3.4 + §5 + §7 (i) | build_paper_tables.py |
| `test_paper_figure_samples.py` | For `figure1_data_sample.json`: `configurations` list = 9-entry set `{full_loom} ∪ ABLATIONS`; each series sub-dict's key set = that same 9-entry set. For `figure2_data_sample.json`: `fault_types` == parsed `AllFaultKinds` from `faultinject/kinds.go` (order-sensitive). For `figure3_data_sample.json`: `families` = sorted `os.listdir("multi-agent/tests/eval/golden/")` (excluding non-family entries); each stage's `metric` and `subkey` strings match spec §5 rationale; Figure-3 rows aggregated from `e4_stages_sample.csv` under filter `configuration == "full_loom"` and count 20 per stage. For `figure4_data_sample.json`: `components` list equals **exactly** `["DriverPlanningOverhead", "TaskDispatchLatency", "TunnelOverhead", "ObserverOverhead", "ModelProxyOverhead"]` (order-sensitive); `extras` **keys equal exactly** `{"ArtifactTransferThroughput", "RoutingLatencyP50P95"}`; `extras.ArtifactTransferThroughput.artifact_size_bytes` equals `[1024, 1048576, 104857600]`; `extras.RoutingLatencyP50P95.scale_points` has all 3 axes `contexts`/`tools_per_context`/`artifact_size_bytes` each matching 08 号 §E5 line 209–212 verbatim | §5 + §7 (i) | build_paper_tables.py |
| `test_smoke_readme_warning.py` | `smoke/README.md` contains the spec §7 (j) blockquote **verbatim** across two source lines: `> **本目录数据是脚手架 smoke，非论文用真数据；60 run 真跑归后续` and `> \`paper/v3/p3-stub-fulltable-run\` worktree。**` (including the `> ` Markdown blockquote prefixes, the `**` bold delimiters, and the backticks around the worktree name; the two lines are joined by a `\n`). Additionally, every filename under `smoke/paper/` matches `*_sample.{csv,json}` (no non-sample paper artefact exists). | §7 (j) | smoke/README.md, smoke/paper/ |
| `test_hard_cap_full_run.py` | `bash run.sh --sample 4` without env exits 2 with stderr containing `ErrFullRunNotAllowed`; `ALLOW_FULL_RUN=1 bash run.sh --sample 4 --dry-run` exits 0 and prints ≤ 4 CLI lines (`--dry-run` overrides real dispatch so nothing runs); `bash run.sh --sample 60 --dry-run` (no env) still exits 2 (the gate fires before `--dry-run` short-circuits) | §7 (k) + §4.3 | run.sh |
| `test_stage_e4_handoff.py` | `docs/specs/wt3-stub-fulltable.handoff.md` exists at exactly that repo-relative path; file contains the literal string `Stage A/B/C flag handoff to runner`; file is discoverable by grepping `smoke/README.md` for the handoff filename | §4.4 | handoff.md, smoke/README.md |
| `test_resume_sidecar.py` | `basename.rsplit("__", 1)` on both `matrix__<w>__<ba>__<uuid>.done` and `e4__<f>__<s>__<t>__<c>__<uuid>.done` recovers the correct `resume_key` on the left; `run.sh --resume` skips rows from either manifest whose `resume_key` matches a pre-populated sidecar; a stale `smoke/runs/<resume_key>__*.csv` from a failed attempt is deleted before retry; two fixtures parametrise the two prefixes | §4.5 | plan.py, run.sh |
| `test_baseline_or_ablation_source.py` | Neither `run.sh` nor `plan.py` accepts a CLI flag that could substitute the `baseline_or_ablation` value of a matrix row (asserted by parsing `run.sh --help` and by attempting an unknown flag like `--configuration NoAcceptanceGate` which must exit non-zero); each planned CLI's `--ablation …` argument value comes verbatim from a matrix row; a hand-crafted matrix row with `NoAccetpanceGate` still rejects at schema time (double defence per §7 (g)) | §7 (g) | run.sh, plan.py |
| `test_build_paper_tables_missing_arg.py` | `python build_paper_tables.py --metrics … --runs … --ablation-mapping … --out-dir … --sample-mode` (omitting `--e4-stages`) exits non-zero and stderr names `--e4-stages`; each of the other 4 required args individually omitted also exits non-zero with a naming stderr | §5 | build_paper_tables.py |

**Coverage check.** Each spec §7 item (a)–(k) maps to at least one test:
(a) → test_stub_listen_loopback; (b) → test_port_pool;
(c) → test_cloud_baseline_dry_run; (d) → test_per_run_observer_db;
(e) → test_failure_scrub;
(f) → test_commit_meta + test_commit_meta_preflight;
(g) → test_matrix_schema + test_e4_stages_yaml
      + test_baseline_or_ablation_source;
(h) → test_dry_run_snapshot; (i) → test_paper_*;
(j) → test_smoke_readme_warning; (k) → test_hard_cap_full_run.
Additional spec §4.5 (resume) → test_resume_sidecar; spec §5 required
CLI args → test_build_paper_tables_missing_arg. **Total: 19 tests.**

## 3. Step-by-step task list (TDD order)

Every step is: write test → run and see it fail → write minimal impl
→ see it pass → move on.

### Step 1 — matrix.schema.json + matrix.yaml + test_matrix_schema.py

1.1 Write `test_matrix_schema.py` with 6 sub-tests:
    `test_60_rows`, `test_workload_enum`, `test_baseline_or_ablation_enum`,
    `test_typo_rejected`, `test_cloud_rows_dry_run_true`,
    `test_no_duplicate_rows`.
1.2 Run: fails (no matrix.yaml, no schema).
1.3 Write `matrix.schema.json`.
1.4 Write `matrix.yaml` with all 60 rows.
1.5 Run: passes.
1.6 Commit: `feat(fulltable): matrix.yaml + schema (60 rows)`.

### Step 2 — ablation_mapping.{schema.json,yaml} + test

2.1 Write `test_ablation_mapping.py` with 5 sub-tests:
    `test_8_rows`, `test_flag_enum`, `test_difficulty_enum`,
    `test_target_metrics_in_ALL_METRICS`, `test_yaml_matches_spec_table`.
2.2 Run: fails.
2.3 Write `ablation_mapping.schema.json`.
2.4 Write `ablation_mapping.yaml` matching spec §3.3.
2.5 Run: passes.
2.6 Commit: `feat(fulltable): ablation_mapping.yaml (8 rows)`.

### Step 3 — e4_stages.{schema.json,yaml} + test

3.1 Write `test_e4_stages_yaml.py` with 4 sub-tests:
    `test_240_rows`, `test_full_cartesian_coverage`,
    `test_acceptance_entry_rejected`, `test_bad_configuration_rejected`.
3.2 Run: fails.
3.3 Write schema + yaml.
3.4 Run: passes.
3.5 Commit: `feat(fulltable): e4_stages.yaml (240 rows)`.

### Step 4 — lib/plan.py + test_stub_listen_loopback.py + test_cloud_baseline_dry_run.py

4.1 Write both tests (both fail).
4.2 Implement `plan.py` with `build_stub_listen`,
    `plan_command_for_matrix_row`, `resume_key_for_matrix_row`,
    `plan_command_for_e4_row`, `resume_key_for_e4_row`.
    Rejection of `0.0.0.0` at `build_stub_listen`.
    Rejection of cloud row without `dry_run: true` at
    `plan_command_for_matrix_row`.
4.3 Run: passes.
4.4 Commit: `feat(fulltable): lib/plan.py + loopback + cloud dry-run`.

### Step 5 — lib/portpool.py + test_port_pool.py

5.1 Write test with fixture that binds 18100 to force retry.
5.2 Implement `portpool.py` (single-process singleton pool starting
    at 18100; `try_bind` uses `SO_REUSEADDR=0` so the second bind
    fails cleanly).
5.3 Run: passes.
5.4 Commit: `feat(fulltable): lib/portpool.py`.

### Step 6 — lib/commit_meta.py + test_commit_meta.py

6.1 Write test with 4 parametrised cases using `tmp_path` +
    `subprocess.run(["git", "init"], cwd=tmp_path)`.
6.2 Implement `commit_meta.py`: `assert_clean_and_get_head(repo_dir)`
    returns `loom_commit` string or raises `ErrDirtyWorktree`.
6.3 Run: passes.
6.4 Commit: `feat(fulltable): lib/commit_meta.py`.

### Step 7 — lib/failure_scrub.py + test_failure_scrub.py

7.1 Write test with 3 sub-tests:
    `test_redact_sk_token`, `test_tail_200`,
    `test_redact_bearer_header`.
7.2 Implement `failure_scrub.py`: regex set mirrors the Go
    secretscrub patterns
    (`sk-[A-Za-z0-9]{20,}`, `gh[a-z]_[A-Za-z0-9]{20,}`,
    `[Bb]earer\s+[A-Za-z0-9_-]+`); `tail_lines(text, n=200)` returns
    the last N lines.
7.3 Run: passes.
7.4 Commit: `feat(fulltable): lib/failure_scrub.py`.

### Step 8 — lib/stage_e4.py + test_stage_e4_join.py

8.1 Write test asserting `stage_e4.join(runs, e4_manifest)` produces
    a long-form table keyed on the 4-tuple `(family, stage, task_id,
    configuration)` with the header from spec §4.4; missing runs
    produce blank cells; `TaskSuccessRate` never blank for Stage-A
    `full_loom` rows.
8.2 Implement `stage_e4.py`.
8.3 Run: passes.
8.4 Commit: `feat(fulltable): lib/stage_e4.py`.

### Step 9 — lib/aggregate.py + lib/paper_tables.py + build_paper_tables.py + test_paper_table2_sample.py + test_paper_table2_ablation.py + test_paper_figure_samples.py

9.1 Write all three paper-sample tests. Each expects a
    `--sample-mode` invocation on fake input to produce a specific
    header/shape.
9.2 Implement `aggregate.py` (numeric aggregation over per-run
    rows).
9.3 Implement `paper_tables.py` writer functions.
9.4 Implement `build_paper_tables.py` argparse CLI (5 required args,
    `--sample-mode` flag).
9.5 Fixture: pre-canned `metrics.csv` + `runs.csv` under
    `tests/fixtures/fake/` for the tests to point at.
9.6 Run: passes.
9.7 Commit: `feat(fulltable): aggregation + paper table writers`.

### Step 10 — run.sh + test_dry_run_snapshot.py + test_hard_cap_full_run.py + test_commit_meta_preflight.py

10.1 Write three bash-facing tests. `test_dry_run_snapshot.py` compares
     stdout to `tests/dry_run_snapshot.txt` (golden file created
     next to the test). `test_hard_cap_full_run.py` per §2 test
     matrix. `test_commit_meta_preflight.py` sets up a temp git
     worktree and parametrises **all four §7 (f) cases** exactly:
       (a) staged-dirty → `bash run.sh --sample 1` exits 2 with
           stderr `ErrDirtyWorktree`, no `.db` file lands in
           `smoke/dbs/`;
       (b) unstaged-dirty → same;
       (c) untracked-not-gitignored → same;
       (d) untracked-but-gitignored → runs with
           `LOOM_FULLTABLE_DISPATCH_SHIM=1 bash run.sh --sample 1`,
           expects exit 0 with stdout containing
           `SHIM: would dispatch 1 rows`, and asserts no `.db`
           file exists (dispatch shim short-circuited).
10.2 Implement `run.sh` — pure bash; delegates to
     `python3 -m lib.plan …` for planning. Modes: `--dry-run`,
     `--sample N`, `--resume`, `--parallel N`; hard cap on `N > 3`
     unless `ALLOW_FULL_RUN=1`; `--sample N` invokes `eval-runner`
     or `baselines/<dir>/run.sh` per row. **Before any real
     dispatch** (i.e. any invocation that is not `--dry-run`),
     `run.sh` calls `python3 -m lib.commit_meta` which runs the
     spec §7 (f) check and exits 2 on any dirty state.
     **Dispatch shim.** When `LOOM_FULLTABLE_DISPATCH_SHIM=1` is set
     in the environment, `run.sh` runs the commit_meta preflight
     unchanged (so dirty trees still exit 2), then instead of
     exec'ing `eval-runner` or a baseline `run.sh`, prints one line
     `SHIM: would dispatch <N> rows` to stdout and exits 0. This is
     the test seam consumed by `test_commit_meta_preflight.py` (§2
     test matrix) and by any future CI-safe integration test — it
     lets clean-tree happy-path preflight coverage run without
     starting a real runner / stub / observer subprocess, matching
     spec §7 (h)'s "CI: subprocess-free" constraint.
10.3 Generate the golden `tests/dry_run_snapshot.txt` by running
     `bash run.sh --dry-run` once, hand-inspecting it against
     matrix.yaml, then committing.
10.4 Run: passes.
10.5 Commit: `feat(fulltable): run.sh + dry-run + hard cap + commit_meta preflight`.

### Step 11 — fixture smoke (real 3-run dispatch)

**CWD for every command in Steps 11 and 12 is
`/root/multi-agent/.worktrees/p3-stub-fulltable/multi-agent/`** — i.e.
the Go module root, one level below the worktree root. All paths in
the commands below are relative to that CWD.

**Clean-tree discipline.** Every change made in Step 11 that would
otherwise dirty the tree at dispatch time MUST be committed **before**
`run.sh --sample 3` runs, because `run.sh` calls the §7 (f) commit_meta
preflight and refuses to dispatch from a dirty worktree (see Step
10.2 above). Concretely: the README + inject-hook + smoke directory
skeleton are all committed in sub-steps 11.1–11.3 (in a single commit
before dispatch), and only 11.4 actually invokes `run.sh --sample 3`.
`.gitignore` entries for the smoke output files are staged in 11.1
so the smoke run's generated output does NOT re-dirty the tree until
the operator explicitly commits it in 11.6.

11.1 `mkdir -p tests/eval/results/smoke/{dbs,runs,paper}`. Create
     `tests/eval/results/smoke/.gitignore` with these smoke-local
     patterns (paths are relative to the file per gitignore
     semantics; leading `/` anchors to the smoke/ dir):

     ```
     # Ignore smoke outputs by default; force-add via `git add -f`
     # after generation (see Step 11.6 + 12.3).
     /runs.csv
     /metrics.csv
     /failures.jsonl
     /dbs/
     /runs/
     /paper/
     ```

     `README.md` and `.gitignore` itself remain tracked (they are not
     matched by any pattern above). This ensures Step 11.4's and
     Step 12.1's generated smoke outputs do not dirty the tree
     between dispatch and the deliberate `git add -f` commits in
     11.6 and 12.3.
11.2 Write `tests/eval/results/smoke/README.md`. Its first two
     content lines MUST be the spec §7 (j) blockquote verbatim
     (including the `> ` Markdown blockquote prefix, `**bold**`
     delimiters, and backticks around the worktree name):

     ```
     > **本目录数据是脚手架 smoke，非论文用真数据；60 run 真跑归后续
     > `paper/v3/p3-stub-fulltable-run` worktree。**
     ```

     Additional narrative below is fine and encouraged (why this
     directory exists, how to regenerate, link to spec + handoff).
11.3 Add a **fixture-inject failure hook**: `lib/plan.py` accepts a
     `--inject-fake-failure-on-row N` flag that stamps a stderr line
     `ERROR: token=sk-abcdefghij0123456789xxxx` into the runner's
     wrapping stderr; the harness's failure recording picks it up
     into `smoke/failures.jsonl` via the failure_scrub pipeline.
11.3b **Commit** 11.1 + 11.2 + 11.3 in one commit:
      `feat(fulltable): smoke output dir skeleton + README + inject-hook`.
      Verify `git status --porcelain --untracked-files=all` is empty
      afterwards (i.e. the preflight would pass).
11.4 Run: `bash tools/eval/fulltable/run.sh --sample 3
     --inject-fake-failure-on-row 3`. Because 11.3b just brought the
     tree clean, the commit_meta preflight passes and dispatch runs.
11.5 Verify: `tests/eval/results/smoke/runs.csv` has 3 rows;
     `tests/eval/results/smoke/dbs/` has 3 distinct `.db` files;
     `tests/eval/results/smoke/failures.jsonl` has 1 entry whose
     `tail_stderr` does NOT contain `sk-abc…` and IS ≤ 200 lines.
     **Additionally**, `run.sh` itself calls the spec §7 (f) step-3
     post-run check by invoking `python3 -m lib.commit_meta
     --verify-runs tests/eval/results/smoke/runs.csv --expected
     $(git rev-parse HEAD)` after the smoke completes; the check
     asserts every row's `loom_commit` cell equals the harness-side
     `rev-parse HEAD` captured at preflight time (raises
     `ErrLoomCommitMismatch` otherwise). This step happens inside
     `run.sh`, so a drift between the runner's stamped commit and
     the harness's expected commit fails Step 11.4 outright, not
     silently in downstream aggregation.
11.6 Commit smoke output. Because the .gitignore added in 11.1
     ignores the output files by default, this commit must
     explicitly `git add -f tests/eval/results/smoke/{runs.csv,
     metrics.csv,failures.jsonl,dbs/,paper/}` and commit:
     `feat(fulltable): fixture smoke output (3 runs)`.

### Step 12 — sample paper tables (real aggregation on smoke output)

CWD identical to Step 11 (`multi-agent/`).

12.1 Run:

     ```
     python tools/eval/fulltable/build_paper_tables.py \
       --metrics tests/eval/results/smoke/metrics.csv \
       --runs    tests/eval/results/smoke/runs.csv \
       --ablation-mapping tools/eval/fulltable/ablation_mapping.yaml \
       --e4-stages        tools/eval/fulltable/e4_stages.yaml \
       --out-dir          tests/eval/results/smoke/paper/ \
       --sample-mode
     ```

12.2 Verify: 7 sample files exist under
     `tests/eval/results/smoke/paper/`; all tests from Step 9 pass
     on real output.
12.3 Commit paper sample outputs. Because Step 11.1's `.gitignore`
     ignores `/paper/`, this commit must explicitly force-add:
     `git add -f tests/eval/results/smoke/paper/*_sample.csv
     tests/eval/results/smoke/paper/*_sample.json`; then commit
     `feat(fulltable): sample paper tables from smoke`.

### Step 13 — Verify remaining safety tests pass on real smoke output

13.1 Run all Python tests:
     `cd multi-agent && python -m pytest tools/eval/fulltable/tests/ -v`.
13.2 Verify all 21 tests pass (19 in the test matrix + Step 8's
     stage_e4_join test + Step 11's implicit smoke-shape check).
13.3 Fix any drift.
13.4 Commit: `test(fulltable): all safety and structural tests green`.

### Step 14 — Handoff paragraph + README

14.1 Write `docs/specs/wt3-stub-fulltable.handoff.md`. Must contain
     literal string `Stage A/B/C flag handoff to runner`.
14.2 Write `multi-agent/tools/eval/fulltable/README.md`. Sections:
     - "What this ships" (harness + smoke, not real 60/240 dispatch)
     - "How to run in this worktree" (only `--dry-run`, `--sample 3`)
     - "How the follow-up run worktree runs it"
       (single fenced block, exactly the CLI invocation with
        `ALLOW_FULL_RUN=1`; NO `nohup`, NO `--parallel 8` in a copy-
        pasteable line without visible context)
     - "Files, at a glance" (5 yaml/json + 3 lib subdirs + 3 CLI
       entry points)
     - "Handoff to the run worktree" (link to
       `docs/specs/wt3-stub-fulltable.handoff.md`)
14.3 Add a test line to `test_stage_e4_handoff.py` that greps
     `smoke/README.md` for the handoff filename.
14.4 Run: all tests still pass.
14.5 Commit: `docs(fulltable): handoff + README`.

## 4. Non-tasks (explicitly forbidden in this plan)

* Any **real** invocation of `run.sh --sample N` with `N > 3` (i.e.
  one that would actually dispatch runs). The reject-path test
  (Step 10.4) is required; the dry-run accept-path test
  `ALLOW_FULL_RUN=1 bash run.sh --sample 4 --dry-run` **is
  permitted** — `--dry-run` short-circuits before any subprocess
  starts, so nothing gets dispatched — and is required as the
  `test_hard_cap_full_run.py` accept-path assertion.
* Any invocation of `nohup … run.sh &` or `run.sh --parallel 8` on a
  real 60/240 dispatch.
* Any real cloud API call. `cloud_sandbox_e2b` rows stay dry-run.
* Any edit to `multi-agent/tools/eval/runner/*.go` or
  `multi-agent/internal/*` or `multi-agent/tests/eval/baselines/*`
  or `multi-agent/tools/eval/metrics/*` (the Phase 2 code). If Phase A
  needs a hook the runner does not expose, stop and write a handoff
  paragraph.
* Any file under `multi-agent/tests/eval/results/{runs,metrics,paper,
  dbs,failures}.*` at a non-`smoke/` path. That path is the run
  worktree's output location.

## 5. Verify (final gate before commit + push-refusal)

```bash
cd multi-agent
# Python
python -m pytest tools/eval/fulltable/tests/ -v

# Dry-run
bash tools/eval/fulltable/run.sh --dry-run > /tmp/dryrun.txt
diff -u tools/eval/fulltable/tests/dry_run_snapshot.txt /tmp/dryrun.txt

# Hard cap (reject path only)
bash tools/eval/fulltable/run.sh --sample 4 || echo "expected exit 2: $?"

# Smoke output shape
ls tests/eval/results/smoke/
cat tests/eval/results/smoke/README.md | head -3
ls tests/eval/results/smoke/paper/ | grep _sample

# Scope check — nothing landed outside the allowed prefix
git diff --stat origin/paper/v3-integration -- \
  ':(exclude)multi-agent/tools/eval/fulltable/' \
  ':(exclude)multi-agent/tests/eval/results/smoke/' \
  ':(exclude)docs/specs/wt3-stub-fulltable.*.md'
# Expected: empty output. Any line = scope violation.

# Handoff exists
test -f ../docs/specs/wt3-stub-fulltable.handoff.md
```

If any check fails, do NOT commit; fix and re-verify.

## 6. Commit convention

* One commit per Step (Steps 1–14 above).
* Every commit message body includes the trailer
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
* Do NOT `git push`. The user reviews the local branch before push.
