> **本目录数据是脚手架 smoke，非论文用真数据；60 run 真跑归后续
> `paper/v3/p3-stub-fulltable-run` worktree。**

# WT-3-stub-fulltable — fixture smoke output

This directory is produced by `bash multi-agent/tools/eval/fulltable/run.sh
--sample 3 --inject-fake-failure-on-row 3` and exists solely as evidence
that the harness scaffold in `multi-agent/tools/eval/fulltable/` drives
the runner end-to-end. **Every value here is a placeholder** — the smoke
fixture skips the real agentserver-stub / observer / runner dispatch
(WT-3-stub-fulltable scope §0 is "harness-only, no 60-run") and
synthesises row-shape output so downstream aggregation can be exercised.

## Files

* `runs.csv` — 3 rows (one per smoke dispatch), header from the harness
  smoke shim; NOT the runner's 32-column CSV (`tools/eval/runner/writer.go
  CSVColumns()`).
* `metrics.csv` — deterministic fixture metrics; column list scoped to
  the paper Table 2 caption phrases (spec §5.1).
* `failures.jsonl` — 1 injected fake failure (row 3), stderr line
  `ERROR: token=sk-abc…` scrubbed via `internal/secretscrub` mirror
  (spec §7 (e)).
* `dbs/<run_id>.db` — 3 distinct zero-byte files (spec §7 (d) requires
  distinct paths, not real observer content — the follow-up run
  worktree turns these into real SQLite files by wiring the runner's
  `--observer-db` flag through).
* `runs/<resume_key>__<run_id>.done` — per-row sidecar for `--resume`.
* `paper/*_sample.{csv,json}` — output of `build_paper_tables.py
  --sample-mode` on the smoke `runs.csv` / `metrics.csv`; every
  filename ends in `_sample.*` (spec §7 (j)) so a paper writer cannot
  mistake it for real numbers.

## Regenerate

```
cd multi-agent
bash tools/eval/fulltable/run.sh --sample 3 --inject-fake-failure-on-row 3
python3 tools/eval/fulltable/build_paper_tables.py \
  --metrics tests/eval/results/smoke/metrics.csv \
  --runs    tests/eval/results/smoke/runs.csv \
  --ablation-mapping tools/eval/fulltable/ablation_mapping.yaml \
  --e4-stages        tools/eval/fulltable/e4_stages.yaml \
  --out-dir          tests/eval/results/smoke/paper/ \
  --sample-mode
```

## Handoff

Runner-side E4 `--stage {A,B,C}` flag is a handoff to the follow-up
worktree — see `docs/specs/wt3-stub-fulltable.handoff.md`.
