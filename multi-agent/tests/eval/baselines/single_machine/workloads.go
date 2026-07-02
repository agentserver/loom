// Package main is the single_machine_claude_code baseline binary. See
// docs/specs/wt2-baselines.spec.md §E2 — single-machine coding agent
// baseline (Claude Code CLI). Real mode invokes the `claude` binary;
// dry-run mode projects mock_workspace and never touches the network.
package main

// claudePrompts holds per-workload prompt + expected-output metadata.
// The prompt is what would be handed to the Claude Code CLI in a real
// interactive run; the ExpectedOutputs list is checked (in real mode)
// to confirm the CLI actually produced them before the oracle grades
// the workspace.
//
// The prompts here are intentionally spare — no chain-of-thought
// scaffolding, no example — because the E2 baseline's whole point is
// "what does a single-shot coding agent do without capability discovery,
// typed contracts, or a slave fleet?" A verbose prompt would defeat the
// comparison.
type promptSpec struct {
	Prompt          string
	ExpectedOutputs []string
}

var claudePrompts = map[string]promptSpec{
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
}
