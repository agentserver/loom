# WT-3-stub-fulltable — Handoff to `paper/v3/p3-stub-fulltable-run`

**Stage A/B/C flag handoff to runner.** The E4 three-stage manifest
(`multi-agent/tools/eval/fulltable/e4_stages.yaml`, 240 rows keyed on
`(family, stage, task_id, configuration)` per spec §4.4) exists on the
harness side only. `multi-agent/tools/eval/runner/main.go` at base
HEAD `1185e99` exposes no `--stage {A,B,C}` CLI flag, so this
worktree's `run.sh` cannot pass the stage into the runner and this
worktree's smoke output does **not** exercise real E4 three-stage
dispatch — `smoke/paper/e4_stages_sample.csv` is filled from a
deterministic fixture in `lib/stage_e4.py` only for column-shape
verification. The follow-up run worktree
(`paper/v3/p3-stub-fulltable-run`) MUST (a) add `--stage {A,B,C}` to
`cmd/eval-runner run` and thread it through to a new `runs.stage`
CSV column; (b) extend the ablation-vs-baseline stamping so
`runs.stage` participates in the resume key alongside
`(family, task_id, configuration)`; (c) wire `run.sh` to pass
`--stage` per row when dispatching from `e4_stages.yaml`; and (d)
update the aggregation join in `lib/stage_e4.py` to read
`runs.stage` from the runner output instead of the fixture-filled
placeholder. Once those four steps land, the harness in this
worktree consumes the real 240-row E4 output unchanged — no other
harness code needs to move.
