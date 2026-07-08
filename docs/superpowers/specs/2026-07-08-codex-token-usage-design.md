# Codex Token Usage Design

## Goal

Collect per-run Codex CLI input and output token counts in the evaluation runner, so the existing `TokenUsage` metric can report real values from `runs.model_input_tokens` and `runs.model_output_tokens`.

## Scope

This change supports Codex CLI only. It does not normalize usage from Claude, OpenHands, E2B, or other agents. If no Codex usage JSONL is supplied, token fields remain zero.

## Interface

Add a runner CLI flag and matching `Opts` field:

```text
--codex-usage-jsonl <path>
```

The file is parsed after the workload oracle runs and before `RunRow` assembly. The parser reads Codex CLI JSONL line by line, ignores lines without usage data, and sums usage records.

Accepted usage keys:

- input: `input_tokens`, `prompt_tokens`
- output: `output_tokens`, `completion_tokens`

The parser accepts `usage` objects at the top level and under common wrappers such as `event`, `response`, and `message`.

## Persistence

Append two numeric fields to runner CSV:

- `model_input_tokens`
- `model_output_tokens`

Append two integer columns to `runs`:

- `model_input_tokens INTEGER NOT NULL DEFAULT 0`
- `model_output_tokens INTEGER NOT NULL DEFAULT 0`

The existing Python metric extractor already probes for these two columns and sums them, so no metric API change is needed.

## Errors

An unreadable or malformed supplied JSONL file is a preflight-style runner error with exit code 2. Individual JSONL lines without usage are ignored. Negative token counts are rejected.

## Testing

Use TDD:

1. Add parser tests for Codex JSONL usage summing and ignored non-usage lines.
2. Add runner/CSV tests for appended columns and row serialization.
3. Add evalrun DB roundtrip and schema drift tests for the new columns.
4. Add a Python metric test showing `TokenUsage` returns real sums when the columns exist.
