# Task1 Redesign Gap Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the `public-terminal-heterogeneous-dates` strict-partition pilot so its report explicitly tracks the redesign document's fair-comparison requirements and adds the next missing baseline/probe.

**Architecture:** Keep the current Bash container-smoke runner as the experiment orchestrator. Add one deterministic `cloud_sandbox_context_injection` baseline, add wrong-context probes for local strict contexts, and generate a redesign-alignment table so the report separates completed, partial, and missing requirements.

**Tech Stack:** Bash, Python standard library, pytest contract tests, existing workload oracle.

---

### Task 1: Contract Tests For Redesign Alignment

**Files:**
- Modify: `tools/eval/container_smoke/test_strict_partition_contract.py`

- [ ] **Step 1: Write failing tests**
  - Assert the strict runner declares `cloud_sandbox_context_injection`.
  - Assert the strict runner writes `context_injection_manifest.json`.
  - Assert the strict runner records `wrong_context_failure`.
  - Assert the report generator writes a `redesign_alignment` section and mentions `FullPCS_reuse_stage` as not yet run.

- [ ] **Step 2: Run focused pytest**
  - Run: `pytest tools/eval/container_smoke/test_strict_partition_contract.py -q`
  - Expected: fail because the runner does not yet implement the cloud baseline/alignment report.

### Task 2: Wrong-Context Probe And Cloud Context Injection Baseline

**Files:**
- Modify: `tools/eval/container_smoke/run_heterogeneous_dates_strict_partition_experiment.sh`

- [ ] **Step 1: Add wrong-context probe helper**
  - The helper runs a command against the wrong context and records `wrong_context_probe.json`.
  - A successful probe means the wrong-context attempt failed before producing `avg_temp.txt`.

- [ ] **Step 2: Add `cloud_sandbox_context_injection`**
  - The sandbox starts without raw CSV files.
  - The runner writes `context_injection_manifest.json` showing explicit raw-data injection from `data_context` to `cloud_sandbox_context`.
  - The sandbox computes `avg_temp.txt`, then the runner fetches only that final file into the driver/final workspace.

- [ ] **Step 3: Extend metadata**
  - Metadata includes `wrong_context_failure`.
  - Metadata includes `context_injection_steps` for cloud sandbox.
  - Metadata keeps `raw_csv_in_driver_workspace=false`.

### Task 3: Redesign-Aware Report

**Files:**
- Modify: `tools/eval/container_smoke/run_heterogeneous_dates_strict_partition_experiment.sh`

- [ ] **Step 1: Add redesign alignment data to JSON**
  - `redesign_alignment` includes status for manual SSH, single-machine Codex SSH, cloud sandbox injection, Full PCS strict, wrong-context, per-agent token accounting, and reuse stage.

- [ ] **Step 2: Add report tables**
  - Table B includes `cloud_sandbox_context_injection`.
  - A separate alignment table states `done`, `partial`, or `missing`.
  - `FullPCS_reuse_stage` is explicitly `missing/not_run`, not silently omitted.

### Task 4: Verification And Experiment Rerun

**Files:**
- Output: `/root/paper_writing/paper_outputs/public_benchmark_task1_baselines/strict_partition_report_cn.md`
- Output: `/root/paper_writing/paper_outputs/public_benchmark_task1_baselines/strict_partition_results.json`

- [ ] **Step 1: Run tests**
  - `pytest tools/eval/container_smoke/test_strict_partition_contract.py tools/eval/container_smoke/test_driver_mcp_codex_harness_contract.py tools/eval/container_smoke/test_driver_autonomous_contract.py -q`
  - `go test ./tests/eval/baselines/manual_ssh ./tests/eval/baselines/single_machine ./tests/eval/baselines/single_machine_codex ./tests/eval/baselines/cloud_sandbox ./tests/eval -count=1`

- [ ] **Step 2: Rerun experiment**
  - `bash tools/eval/container_smoke/run_heterogeneous_dates_strict_partition_experiment.sh /root/paper_writing/paper_outputs/public_benchmark_task1_baselines/strict_partition`

- [ ] **Step 3: Verify outputs**
  - Confirm all run configurations either pass or are explicitly marked `missing/not_run`.
  - Confirm no strict configuration leaks raw CSVs into driver workspace.
