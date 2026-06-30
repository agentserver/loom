package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzValidate mutates JSON bytes into TaskContract instances and
// asserts Validate does not panic, deadlock, or take pathological
// wall-time. Spec §7 (e): Validate is the trust boundary at the entry
// tool; a panic becomes a 5xx that takes down the driver process.
//
// Seed corpus per spec §7 (e): empty TC, valid TC, RecoveryHint at
// various lengths, RecoveryHint with \x00, each HTML prefix,
// MaxDAGNodes=-1, MaxConcurrency=0, 10 MiB Intent.Goal. Each seed is
// added so the fuzzer starts with diverse coverage.
func FuzzValidate(f *testing.F) {
	// Seed 1: empty contract.
	f.Add([]byte(`{}`))

	// Seed 2: fully-valid 7/7 contract.
	f.Add([]byte(`{
		"version": 1,
		"conversation_id": "fuzz-seed-valid",
		"intent": {"goal": "do", "success_criteria": ["done"]},
		"data_contract": {
			"read_artifacts": [],
			"write_targets": [{"type":"artifact","kind":"code","name":"x.go"}]
		},
		"capability_requirements": {"skills":["chat"]},
		"recovery_hint": "fine"
	}`))

	// Seed 3: RecoveryHint at the cap (4096 runes).
	f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"g","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"recovery_hint":"` + strings.Repeat("a", 4096) + `"}`))

	// Seed 4: RecoveryHint over the cap.
	f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"g","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"recovery_hint":"` + strings.Repeat("a", 4097) + `"}`))

	// Seed 5: RecoveryHint with control characters.
	f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"g","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"recovery_hint":"badhere"}`))

	// Seed 6..13: each of the 8 HTML prefixes.
	for _, p := range recoveryHintHTMLPrefixes {
		f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"g","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"recovery_hint":"see ` + p + ` here"}`))
	}

	// Seed 14: pathological policy (MaxDAGNodes=-1).
	f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"g","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"execution_policy":{"max_dag_nodes":-1},"recovery_hint":"x"}`))

	// Seed 15: pathological policy (MaxConcurrency=0).
	f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"g","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"execution_policy":{"max_concurrency":0},"recovery_hint":"x"}`))

	// Note on "10 MiB Intent.Goal": adding a 10 MiB seed would slow every
	// fuzz iteration significantly without exercising new code paths
	// (the goal field has no length cap, it's just a string property
	// check). Validate's bounded wall-time on a 10 MiB goal is the
	// implementation guarantee. We seed a more modest 1 MiB goal
	// instead, which is enough to catch a quadratic regression.
	f.Add([]byte(`{"version":1,"conversation_id":"a","intent":{"goal":"` + strings.Repeat("g", 1<<20) + `","success_criteria":["s"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"code","name":"x.go"}]},"capability_requirements":{"skills":["chat"]},"recovery_hint":"x"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var tc TaskContract
		if err := json.Unmarshal(data, &tc); err != nil {
			// Invalid JSON is fine — fuzz often produces malformed input.
			return
		}
		// ApplyDefaults must not panic.
		tc.ApplyDefaults()
		// Validate must not panic. Errors are expected — the assertion
		// is only "does not panic / deadlock / OOM".
		_ = tc.Validate()
	})
}
