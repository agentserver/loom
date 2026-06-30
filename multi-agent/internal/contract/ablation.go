package contract

import (
	"fmt"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// DisableSchemaEnforce gates §2.2 required-field enforcement plus §2.4
// recovery_hint content checks. Wired to ablation.NoTypedContracts.
//
// CONCURRENCY CONTRACT (per WT-1-ablation-registry spec §4):
// "set once before workload start, never flipped mid-run". Reads on the
// hot path (EnforceContract / Validate) are intentionally unsynchronized
// — `go test -race` WILL report a data race if any caller writes to
// this var concurrently with an in-flight EnforceContract / Validate
// invocation, and that race is real and undefined-behavior, not a false
// positive. Phase-2 WT-2-flag-integration's CLI binder is expected to
// set this exactly once during process startup, before any agent goroutine
// begins servicing requests; any future runtime-mutation surface
// (HTTP admin endpoint, signal handler, etc.) MUST first move the
// storage to an `atomic.Bool` and update Validate's read to a typed
// atomic load. Until then, runtime mutation is not supported.
//
// Tests that toggle the flag MUST go through `withAblationFlag` (the
// t.Cleanup-anchored helper in completeness_test.go) so the restoration
// is reliable and the flag never leaks across tests.
var DisableSchemaEnforce bool

// DisableContractEntirely gates whether the entry tool parses the
// contract at all. Wired to ablation.NoContractFormalization.
//
// When true, the submit_contract_task handler returns
// ErrContractFormalizationDisabled from EnforceContract and falls back
// to natural-language delegation (see contract_tools.go's
// callNaturalLanguageFallback per spec §3.2 / §4).
//
// Same concurrency contract as DisableSchemaEnforce above — set once
// at startup, never at runtime, no implicit atomic load on the hot path.
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
