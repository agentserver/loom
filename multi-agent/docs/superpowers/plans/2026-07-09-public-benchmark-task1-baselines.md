# Public Benchmark Task 1 Baselines Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add baseline support and a runnable report path for the first public-task-derived benchmark task, `public-terminal-heterogeneous-dates`.

**Architecture:** Reuse the existing baseline harness. Add per-workload script/prompt/plan entries for the public Terminal-Bench task, plus a cloud baseline `--container-codex` mode that substitutes Docker-isolated Codex for E2B in this pilot.

**Tech Stack:** Go baseline binaries, bash wrappers, Docker for optional container Codex, Codex CLI, existing workload oracle.

---

### Task 1: Add Public Task To Baseline Workload Tables

**Files:**
- Modify: `tests/eval/baselines/manual_ssh/workloads.go`
- Modify: `tests/eval/baselines/single_machine_codex/workloads.go`
- Modify: `tests/eval/baselines/cloud_sandbox/workloads.go`
- Test: `tests/eval/baselines/manual_ssh/impl_test.go`
- Test: `tests/eval/baselines/single_machine_codex/impl_test.go`
- Test: `tests/eval/baselines/cloud_sandbox/impl_test.go`

- [ ] **Step 1: Write failing tests**

Add assertions that each baseline has an entry for `public-terminal-heterogeneous-dates`, and add a real `manual_ssh` harness pass test for the public task.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
go test ./tests/eval/baselines/manual_ssh ./tests/eval/baselines/single_machine_codex ./tests/eval/baselines/cloud_sandbox -run 'PublicTerminal|Heterogeneous' -count=1
```

Expected: FAIL because the workload entries are missing.

- [ ] **Step 3: Add workload entries**

Add scripts/prompts that read the two CSV files and write `avg_temp.txt` only.

- [ ] **Step 4: Verify tests pass**

Run the same command and expect PASS.

### Task 2: Add Container Codex Mode To Cloud Baseline

**Files:**
- Modify: `tests/eval/baselines/cloud_sandbox/main.go`
- Modify: `tests/eval/baselines/cloud_sandbox/impl.go`
- Modify: `tests/eval/baselines/cloud_sandbox/run.sh`
- Test: `tests/eval/baselines/cloud_sandbox/impl_test.go`

- [ ] **Step 1: Write failing tests**

Add a test that injects a fake Docker binary, enables container Codex mode, and verifies the cloud baseline runs `docker run` with the workspace mounted and validates `avg_temp.txt`.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
go test ./tests/eval/baselines/cloud_sandbox -run 'ContainerCodex|PublicTerminal' -count=1
```

Expected: FAIL because container mode is not implemented.

- [ ] **Step 3: Implement container mode**

Add `--container-codex`, `--container-codex-image`, and `--container-codex-bin` flags. In this mode, skip E2B client calls and execute Docker with a mounted workspace.

- [ ] **Step 4: Verify tests pass**

Run the same command and expect PASS.

### Task 3: Run Baseline Pilot And Write Report

**Files:**
- Create runtime outputs under `/root/paper_writing/paper_outputs/public_benchmark_task1_baselines/`

- [ ] **Step 1: Run unit and contract tests**

Run:

```bash
go test ./tests/eval/baselines/... -count=1
go test ./tests/eval -count=1
```

- [ ] **Step 2: Run baseline commands**

Run `manual_ssh` real mode, `single_machine_codex` real mode, and `cloud_sandbox --container-codex` if Docker is available.

- [ ] **Step 3: Write report**

Write a Markdown report with command lines, CSV rows, pass/fail status, runtime, and any skipped baseline reason.
