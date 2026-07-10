// Package main is the single_machine_codex baseline binary. Real mode
// invokes the OpenAI Codex CLI (`codex exec`) with the pinned §4.1
// argv; dry-run mode projects mock_workspace and never touches the
// network.
package main

// codexPrompts holds per-workload prompt + expected-output metadata,
// copied VERBATIM from ../single_machine/workloads.go's claudePrompts
// and locked in-sync by TestCodexPromptsMatchClaudePrompts (AST diff).
// The two baselines share prompts so any measured behavior difference
// is attributable to the CLI, not the prompt.
type promptSpec struct {
	Prompt          string
	ExpectedOutputs []string
}

var codexPrompts = map[string]promptSpec{
	"cross-device-code-mod": {
		Prompt: `You are in a workspace directory. Write two files:
1. patch.diff — a valid unified diff (with diff header, hunk header, and content lines) that modifies some file 'x' from 'a' to 'b'.
2. test.log — must contain a single line "PASS" as the first characters on a line.
No other output.`,
		ExpectedOutputs: []string{"patch.diff", "test.log"},
	},
	"remote-data-processing": {
		Prompt:          `Write result.json with fields {"count":5,"sum":15,"mean":3.0}. Then write result.sha256 as the sha256 of result.json (format: '<hex>  result.json').`,
		ExpectedOutputs: []string{"result.json", "result.sha256"},
	},
	"windows-only-artifact": {
		Prompt:          `Write a non-empty file artifact.bin and artifact.meta.json containing {"sha256":"<sha256 of artifact.bin>","size":<byte count>}.`,
		ExpectedOutputs: []string{"artifact.bin", "artifact.meta.json"},
	},
	"missing-parser-converter": {
		Prompt: `Look at fixtures/golden/expected.out (already in workspace). Write:
1. synthesized.mcp.json with a "tools" array containing {"name":"convert"}.
2. converted.out matching fixtures/golden/expected.out byte-for-byte.
3. acceptance.log with exactly one line "PASS expected.out".`,
		ExpectedOutputs: []string{"synthesized.mcp.json", "converted.out", "acceptance.log"},
	},
	"credential-bound-model": {
		Prompt:          `Write route.json = {"model_alias":"acme-bound-model-v1","proxy_context_id":"pctx-single-machine-0001"}. Also write non-empty completion.txt and run.log. No API key strings in the workspace.`,
		ExpectedOutputs: []string{"route.json", "completion.txt", "run.log"},
	},
	"public-terminal-heterogeneous-dates": {
		Prompt: `You are in a workspace directory for a Terminal-Bench style data task.

Inputs:
- task-deps/daily_temp_sf_high.csv has ISO dates and daily high temperatures.
- task-deps/daily_temp_sf_low.csv has slash- or dash-formatted dates with times and daily low temperatures.

Normalize the dates, align records by calendar day, compute the arithmetic mean of high-minus-low over all overlapping dates, and write only the numeric value to avg_temp.txt. Do not include units, prose, markdown, or any other files as the final answer.`,
		ExpectedOutputs: []string{"avg_temp.txt"},
	},
}
