# driver-clarify-contract

Fixture-level E2E example for driver-side requirement clarification before
submitting work to a master.

## Story

The user starts with a vague business request: analyze refund risk and produce a
report. The driver LLM does not submit this directly. It first inspects visible
workspace capabilities, drafts a task contract, asks targeted clarification
questions, and dry-runs the contract against the current resource snapshot.

The visible resources include:

- `master-online-e2e`: a master with `fanout`.
- `analytics-slave`: a slave with an existing `csv_profiler/profile_orders_csv`
  MCP tool.
- `builder-slave`: a slave with `build_mcp`.

The dry run shows that CSV profiling is already available, but
`refund_policy_checker/evaluate_rows` is missing. Because the contract allows
`build_mcp`, the task is runnable through the master and requires one MCP build
during orchestration.

## What This Example Tests

- Driver-side capability inspection includes structured `mcp_tools`.
- Driver-side contract drafting produces clarification questions before submit.
- Driver-side dry-run is side-effect free and reports missing tools plus build
  candidates instead of triggering MCP construction itself.
