package main

// codexPrompts is populated by Task 2 as an AST-verified copy of
// ../single_machine/workloads.go's claudePrompts.
var codexPrompts = map[string]promptSpec{}

type promptSpec struct {
	Prompt          string
	ExpectedOutputs []string
}
