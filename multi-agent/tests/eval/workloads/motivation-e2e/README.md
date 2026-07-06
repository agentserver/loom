# `motivation-e2e` workload — scope

**This is a scaffold self-check fixture.** It is NOT part of the
`p3-stub-fulltable` 5-workload set. It exists so the
`tests/eval/motivation/` chain (aggregate → gen_provenance → replace_intro
dual-target dry-run patcher) can be smoke-tested end-to-end without waiting
on the main experiment.

The four canonical motivation numbers that end up in
`paper_outputs/introduction_v3.md` §motivation and `paper_outputs/motivation_v3.md`
come from `p3-stub-fulltable-run` main-experiment metric directory, aggregated
by `p3-mini-case-run` (see `docs/specs/wt3-mini-case.spec.md` §1 唯一数据源
约定). This workload never produces paper-visible numbers.

For the transcript fixture format (`fixtures/traces/steps.log`), see
`fixtures/traces/README.md`.
