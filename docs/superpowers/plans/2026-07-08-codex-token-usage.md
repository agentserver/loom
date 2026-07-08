# Codex Token Usage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-run Codex CLI input/output token collection to the eval runner, CSV output, SQLite `runs` schema, and existing `TokenUsage` metric path.

**Architecture:** The runner accepts `--codex-usage-jsonl`, parses a Codex CLI JSONL file into a small `CodexTokenUsage` value, and stores those totals on `RunRow`. CSV and SQLite schemas append `model_input_tokens` and `model_output_tokens`; the Python metric extractor already consumes those column names.

**Tech Stack:** Go eval runner and evalrun SQLite writer, SQLite schema SQL, Python pytest metric tests.

---

### Task 1: Codex Usage Parser

**Files:**
- Create: `multi-agent/tools/eval/runner/codex_usage.go`
- Test: `multi-agent/tools/eval/runner/codex_usage_test.go`

- [ ] **Step 1: Write failing parser tests**

Add tests that call `ParseCodexUsageJSONL(path)` on JSONL fixtures. Cover summed usage records, alias fields, wrapper objects, ignored non-usage records, malformed JSON, unreadable files, and negative token counts.

- [ ] **Step 2: Run red test**

Run: `go test ./tools/eval/runner -run 'TestParseCodexUsageJSONL'`

Expected: FAIL because `ParseCodexUsageJSONL` and `CodexTokenUsage` do not exist.

- [ ] **Step 3: Implement minimal parser**

Implement `CodexTokenUsage`, `ParseCodexUsageJSONL`, and helper functions in `codex_usage.go`. Read line by line, unmarshal to `map[string]any`, find usage maps at top level and under `event`, `response`, and `message`, sum non-negative integer values, and return an error for malformed JSON or negative values.

- [ ] **Step 4: Run green test**

Run: `go test ./tools/eval/runner -run 'TestParseCodexUsageJSONL'`

Expected: PASS.

### Task 2: Runner CSV Columns

**Files:**
- Modify: `multi-agent/tools/eval/runner/writer.go`
- Modify: `multi-agent/tools/eval/runner/writer_test.go`

- [ ] **Step 1: Write failing CSV tests**

Update frozen column tests to expect appended `model_input_tokens` and `model_output_tokens`. Add a row roundtrip assertion that a `RunRow` with token values serializes those values into the new columns.

- [ ] **Step 2: Run red test**

Run: `go test ./tools/eval/runner -run 'TestCSVColumns|TestRunRow_CodexTokenUsage_RoundTripCSV'`

Expected: FAIL because the columns and fields do not exist.

- [ ] **Step 3: Implement CSV fields**

Add `ModelInputTokens int` and `ModelOutputTokens int` to `RunRow`, append column names to `CSVColumns`, and append values in `rowAsCSVRecord`.

- [ ] **Step 4: Run green test**

Run: `go test ./tools/eval/runner -run 'TestCSVColumns|TestRunRow_CodexTokenUsage_RoundTripCSV'`

Expected: PASS.

### Task 3: Runner Flag and Row Assembly

**Files:**
- Modify: `multi-agent/tools/eval/runner/main.go`
- Modify: `multi-agent/tools/eval/runner/runner.go`
- Test: `multi-agent/tools/eval/runner/runner_test.go`

- [ ] **Step 1: Write failing runner test**

Add a test that supplies `Opts.CodexUsageJSONL` with a JSONL fixture and asserts `res.Row.ModelInputTokens` and `res.Row.ModelOutputTokens` contain the parsed totals.

- [ ] **Step 2: Run red test**

Run: `go test ./tools/eval/runner -run 'TestRun_RecordsCodexUsageJSONL'`

Expected: FAIL because `Opts.CodexUsageJSONL` is not wired.

- [ ] **Step 3: Implement flag and row wiring**

Add `CodexUsageJSONL string` to `Opts`, add `--codex-usage-jsonl` in `main.go`, parse usage in `Run` before row assembly, return exit code 2 on parse errors, and stamp totals into `RunRow`.

- [ ] **Step 4: Run green test**

Run: `go test ./tools/eval/runner -run 'TestRun_RecordsCodexUsageJSONL'`

Expected: PASS.

### Task 4: SQLite Runs Schema

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql`
- Modify: `multi-agent/internal/evalrun/schema.go`
- Modify: `multi-agent/internal/evalrun/writer.go`
- Modify: `multi-agent/internal/evalrun/writer_test.go`
- Test: `multi-agent/internal/evalrun/schema_test.go`

- [ ] **Step 1: Write failing DB tests**

Update roundtrip tests to insert and read token counts. Update schema drift expectations to require the two appended columns and detect missing token columns.

- [ ] **Step 2: Run red test**

Run: `go test ./internal/evalrun`

Expected: FAIL because schema, expected columns, insert SQL, and `Schema` do not include token fields.

- [ ] **Step 3: Implement DB schema changes**

Append the two integer columns to `schema.sql`, `Schema`, `expectedColumns`, and `insertSQL`. Pass `s.ModelInputTokens` and `s.ModelOutputTokens` to `ExecContext`.

- [ ] **Step 4: Run green test**

Run: `go test ./internal/evalrun`

Expected: PASS.

### Task 5: Metric Extraction Verification

**Files:**
- Modify: `multi-agent/tools/eval/metrics/tests/test_metrics_user_promoted.py`

- [ ] **Step 1: Write failing metric test**

Add a test that creates a temp DB from a fixture schema with `model_input_tokens` and `model_output_tokens`, inserts two selected runs, and asserts `TokenUsage` returns summed input/output plus count.

- [ ] **Step 2: Run red test**

Run: `python -m pytest tests/test_metrics_user_promoted.py`

Expected: FAIL until the fixture creates the new columns or inserts valid rows with the new schema.

- [ ] **Step 3: Make metric fixture use new columns**

Adjust the test fixture setup so the DB has both token columns and the inserted rows have known values.

- [ ] **Step 4: Run green test**

Run: `python -m pytest tests/test_metrics_user_promoted.py`

Expected: PASS.

### Task 6: Final Verification

**Files:**
- All changed files above.

- [ ] **Step 1: Run focused Go tests**

Run: `go test ./tools/eval/runner ./internal/evalrun`

Expected: PASS.

- [ ] **Step 2: Run metric tests**

Run from `multi-agent/tools/eval/metrics`: `python -m pytest tests/test_metrics_user_promoted.py`

Expected: PASS.

- [ ] **Step 3: Inspect worktree**

Run: `git status --short`

Expected: only intentional files changed.
