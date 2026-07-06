# `motivation-e2e` trace fixtures

These files are **mock trace inputs** used by the four collectors in
`tests/eval/motivation/collect_*.py` for scaffold self-check only. They are
NOT real observer / harness output.

## `route_reasons.sqlite`

Mock observer database with a single `route_reasons` table:
```
CREATE TABLE route_reasons(
    workload_id TEXT NOT NULL,
    slave_id TEXT,
    capability_snapshot_hash TEXT
);
```
Populated with rows for `workload_id = 'motivation-e2e'`. Number of distinct
`slave_id` values is the `raw_value` for `contexts_count`.

## `steps.log`

Transcript file — **not an executable script**. Each line is one command a
human would have typed if they were manually SSHing between laptop / server.
The `manual_ssh` baseline (see `tests/eval/baselines/manual_ssh/`) never
actually runs these commands; it uses local bash-only fake scripts to
produce the same artifacts. The transcript is authored so its
`^(ssh|scp|rsync|mkdir|cd) ` line count equals the
`ManualSetupStepCount` cell for `workload_id = 'motivation-e2e'`,
`baseline_or_ablation = 'manual_ssh'` in the co-authored `runs.csv`.

If you edit one, edit the other to match; `collect_manual_steps.py` will
exit 2 `ErrManualStepsMismatch` otherwise (see spec §3.4).

## `runs.csv`

Minimal main-experiment-shape CSV with columns `workload_id`,
`baseline_or_ablation`, `wrong_ctx`, `ManualSetupStepCount`. Includes at
least the row that `collect_manual_steps.py` reads back.

## `e4_stages.csv`

E4 three-stage timing CSV with columns `workload_id`, `family`, `stage`,
`stage_start`, `stage_end`. Used by `collect_reuse_time_savings.py`.

## Regenerating

The fixtures are hand-authored; there is no generator. If you need to change
any value, edit the file directly and update `dry_run_smoke.sh` output
expectations if the aggregate would change.
