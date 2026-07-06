# tools/eval/fulltable — WT-3-stub-fulltable harness

Reproducible one-command harness for the 5 workload × 12 configuration
= 60-run E1–E5 matrix (12 号 §E1–§E5, 08 号 §Table 2 + §Figure 1–4) on
the stub path (12 号 §J.1), plus the aggregation to Paper Table 2 /
Figure 1–4 data.

**Spec:** `docs/specs/wt3-stub-fulltable.spec.md`
**Plan:** `docs/specs/wt3-stub-fulltable.plan.md`
**Handoff:** `docs/specs/wt3-stub-fulltable.handoff.md`

## What this ships (harness only, no 60-run)

* `matrix.yaml` — 60 rows (spec §3.1 × §3.2)
* `ablation_mapping.yaml` — 8 rows (spec §3.3)
* `e4_stages.yaml` — 240 rows (spec §4.4)
* `run.sh` — orchestrator (dry-run / sample / resume / parallel)
* `build_paper_tables.py` — 7 sample paper artifact writers
* `lib/*.py` — planning, port pool, commit_meta, failure_scrub,
  stage_e4, aggregate, paper_tables
* `tests/` — pytest suite (structural + safety + scrub)
* A fixture smoke at `../../tests/eval/results/smoke/` — 3 runs,
  synthesised row shape only (not real observer/runner output). The
  smoke's README carries the spec §7 (j) blockquote warning
  verbatim so the paper writer cannot mistake it for real numbers.

## What this does NOT ship

* Any real invocation of the 60-run matrix or the 240-row E4
  manifest. That is the follow-up worktree `paper/v3/p3-stub-
  fulltable-run`.
* Any file under `multi-agent/tests/eval/results/{runs,metrics,paper,
  dbs,failures}.*` at a non-smoke/ path.
* Any change to `multi-agent/tools/eval/runner/`,
  `multi-agent/internal/`, or any Phase 0/1/2 code.

## How to run in this worktree

```
# Dry-run — prints 60 CLI lines, no subprocess.
bash multi-agent/tools/eval/fulltable/run.sh --dry-run

# Fixture smoke — 3 runs, one injected failure. Preflight refuses to
# dispatch from a dirty tree.
bash multi-agent/tools/eval/fulltable/run.sh --sample 3 \
    --inject-fake-failure-on-row 3

# Sample paper tables from the smoke output.
python3 multi-agent/tools/eval/fulltable/build_paper_tables.py \
    --metrics multi-agent/tests/eval/results/smoke/metrics.csv \
    --runs    multi-agent/tests/eval/results/smoke/runs.csv \
    --ablation-mapping multi-agent/tools/eval/fulltable/ablation_mapping.yaml \
    --e4-stages        multi-agent/tools/eval/fulltable/e4_stages.yaml \
    --out-dir          multi-agent/tests/eval/results/smoke/paper/ \
    --sample-mode
```

`--sample N` with N > 3 is rejected with `ErrFullRunNotAllowed` unless
`ALLOW_FULL_RUN=1` is set (spec §7 (k)). The follow-up run worktree
sets that fuse explicitly; this worktree does not.

## How the follow-up run worktree runs it

**For the run worktree only** (do NOT copy-paste into this worktree):

```
ALLOW_FULL_RUN=1 bash multi-agent/tools/eval/fulltable/run.sh \
    --sample 60 --parallel 4
```

The run worktree also implements the Stage A/B/C runner flag per the
handoff at `docs/specs/wt3-stub-fulltable.handoff.md`, then extends
`run.sh` to dispatch the 240 E4 rows against it.

## Files, at a glance

* `matrix.yaml`, `matrix.schema.json`
* `ablation_mapping.yaml`, `ablation_mapping.schema.json`
* `e4_stages.yaml`, `e4_stages.schema.json`
* `lib/plan.py` — per-row CLI construction, loopback guard,
  cloud-dry-run guard, resume keys
* `lib/portpool.py` — loopback port allocator (18100+)
* `lib/commit_meta.py` — preflight + post-run verify
* `lib/failure_scrub.py` — internal/secretscrub mirror
* `lib/stage_e4.py` — E4 three-stage long-form aggregator
* `lib/aggregate.py` — per-configuration + per-flag means
* `lib/paper_tables.py` — 7 paper artifact writers
* `run.sh` — bash orchestrator
* `build_paper_tables.py` — argparse CLI (5 required args + `--sample-mode`)

## Handoff to the run worktree

See `docs/specs/wt3-stub-fulltable.handoff.md` for the one
paragraph describing the runner-side `--stage {A,B,C}` flag the
follow-up worktree must add before it can dispatch E4 rows.
