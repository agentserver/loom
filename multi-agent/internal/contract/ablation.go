package contract

import (
	"fmt"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// DisableSchemaEnforce gates §2.2 required-field enforcement plus §2.4
// recovery_hint content checks. Wired to ablation.NoTypedContracts.
//
// Per the WT-1-ablation-registry spec §4 "set once before workload
// start, never flipped mid-run" pattern: reads on the hot path
// (EnforceContract / Validate) are UNSYNCHRONIZED. Tests that toggle the
// flag MUST go through the plan §4 `withAblationFlag` helper so the
// restoration is t.Cleanup-anchored and the flag never leaks between
// tests. The package itself does not synchronise reads.
var DisableSchemaEnforce bool

// DisableContractEntirely gates whether the entry tool parses the
// contract at all. Wired to ablation.NoContractFormalization.
//
// When true, the submit_contract_task handler returns
// ErrContractFormalizationDisabled from EnforceContract and falls back
// to natural-language delegation (see contract_tools.go's
// callNaturalLanguageFallback per spec §3.2 / §4).
var DisableContractEntirely bool

func init() {
	if err := ablation.Default.Register(
		ablation.NoTypedContracts, &DisableSchemaEnforce,
	); err != nil {
		// Unreachable in a production binary that links this package once;
		// surface loudly so a misconfigured test setup (duplicate
		// link / wrong canonical name) fails immediately rather than
		// silently losing the registration.
		panic(fmt.Sprintf("contract: register NoTypedContracts: %v", err))
	}
	if err := ablation.Default.Register(
		ablation.NoContractFormalization, &DisableContractEntirely,
	); err != nil {
		panic(fmt.Sprintf("contract: register NoContractFormalization: %v", err))
	}
}
