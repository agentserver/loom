package driver

import "sync/atomic"

// current_run_id.go publishes the run_id the driver is currently
// executing under. Set by the eval-runner at run start; read by
// registry_lookup.go to key each sample row with a run identifier.
// See docs/specs/wt2-driver-promotion-chain-B4.spec.md §5.

var currentRunID atomic.Pointer[string]

// SetCurrentRunID publishes runID as the current eval run. Called
// exactly once per driver process by the eval-runner harness before
// any Lookup could fire. Safe for concurrent read/write.
func SetCurrentRunID(runID string) {
	// Copy to a new string so the caller cannot mutate our storage
	// via a shared pointer.
	s := runID
	currentRunID.Store(&s)
}

// CurrentRunID returns the currently-published run_id, or "" when
// none has been set (ad-hoc / interactive driver sessions).
func CurrentRunID() string {
	if p := currentRunID.Load(); p != nil {
		return *p
	}
	return ""
}

// resetCurrentRunIDForTest clears the value. Test-only.
func resetCurrentRunIDForTest() {
	currentRunID.Store(nil)
}
