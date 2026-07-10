# Task1 Strict Partition Experiment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and run a strict cross-context version of the public Terminal-Bench `heterogeneous-dates` pilot and write a Chinese result report.

**Architecture:** Add a focused container-smoke runner that provisions data and compute contexts separately, runs cross-context baselines, then runs the existing driver MCP/PCS path in strict partition mode. The runner writes machine-readable JSON plus a Markdown report under `paper_outputs/public_benchmark_task1_baselines`.

**Tech Stack:** Bash, Docker, OpenSSH client/server where available, Codex CLI, existing Go driver/slave/observer binaries, pytest contract tests.

---

### Task 1: Strict Runner Contract

**Files:**
- Create: `tools/eval/container_smoke/test_strict_partition_contract.py`
- Create: `tools/eval/container_smoke/run_heterogeneous_dates_strict_partition_experiment.sh`

- [ ] **Step 1: Write the failing test**
  - Assert the runner defines `manual_ssh_cross_context`, `single_machine_codex_ssh`, and `FullPCS_strict_partition`.
  - Assert driver prompt/source setup does not copy raw CSVs into the driver workspace.
  - Assert the runner writes `strict_partition_report_cn.md`, `strict_partition_results.json`, and all-agent token evidence.

- [ ] **Step 2: Run the focused pytest**
  - Run: `pytest tools/eval/container_smoke/test_strict_partition_contract.py -q`
  - Expected: fail because the runner does not exist yet.

- [ ] **Step 3: Implement minimal runner skeleton**
  - Add a Bash runner with the required function names, output files, and strict partition comments.

- [ ] **Step 4: Re-run focused pytest**
  - Expected: pass.

### Task 2: Cross-Context Baselines

**Files:**
- Modify: `tools/eval/container_smoke/run_heterogeneous_dates_strict_partition_experiment.sh`

- [ ] **Step 1: Add manual cross-context baseline**
  - Data context owns raw CSVs.
  - Compute context receives only `aligned_temperatures.csv`.
  - Driver receives only `avg_temp.txt`.
  - Count SSH/SCP-style steps in `manual_steps_ssh`.

- [ ] **Step 2: Add single-machine Codex SSH baseline**
  - Codex prompt may use only `ssh-data` and `ssh-compute` helper commands exposed in PATH.
  - Local driver workspace must not contain raw CSV files.
  - Record Codex token usage from session JSONL when available.

### Task 3: Strict Full PCS Run

**Files:**
- Modify: `tools/eval/container_smoke/run_heterogeneous_dates_driver_mcp_smoke.sh`
- Modify: `tools/eval/container_smoke/test_driver_mcp_codex_harness_contract.py`

- [ ] **Step 1: Add strict partition mode to existing driver MCP runner**
  - `STRICT_PARTITION=1` copies raw CSVs into `smoke-data-slave/workspace/input`.
  - Driver workspace receives only `task.md`.
  - Guided prompt instructs driver to use data slave and compute slave without local raw inputs.

- [ ] **Step 2: Add contract test for strict mode**
  - Assert `STRICT_PARTITION` exists.
  - Assert strict prompt states raw CSVs are not in driver workspace.

### Task 4: Run and Report

**Files:**
- Output: `/root/paper_writing/paper_outputs/public_benchmark_task1_baselines/strict_partition_report_cn.md`
- Output: `/root/paper_writing/paper_outputs/public_benchmark_task1_baselines/strict_partition_results.json`

- [ ] **Step 1: Run Go/pytest verification**
  - `pytest tools/eval/container_smoke/test_strict_partition_contract.py tools/eval/container_smoke/test_driver_mcp_codex_harness_contract.py -q`
  - `go test ./tests/eval/baselines/manual_ssh ./tests/eval/baselines/single_machine ./tests/eval/baselines/single_machine_codex ./tests/eval/baselines/cloud_sandbox ./tests/eval -count=1`

- [ ] **Step 2: Run strict experiment**
  - `bash tools/eval/container_smoke/run_heterogeneous_dates_strict_partition_experiment.sh /root/paper_writing/paper_outputs/public_benchmark_task1_baselines/strict_partition`

- [ ] **Step 3: Summarize results**
  - Report pass/fail, wall time, manual steps, human intervention, observer events, and all-agent tokens.
