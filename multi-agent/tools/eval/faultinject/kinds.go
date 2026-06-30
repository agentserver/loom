//go:build evaltool

package faultinject

import "time"

// FaultKind is the wire-format identifier of a single injectable fault.
// The string literal values are part of the /inject HTTP contract and the
// audit-log schema; treat them as stable (append-only) for the lifetime of
// the harness. Adding a kind requires (a) declaring a constant here, (b)
// appending it to AllFaultKinds in declaration order, and (c) wiring a
// bridge implementation in hookbridge.go.
type FaultKind string

const (
	FaultMissingFile       FaultKind = "missing_file"
	FaultStaleCapability   FaultKind = "stale_capability"
	FaultWrongOSVersion    FaultKind = "wrong_os_version"
	FaultForbiddenCred     FaultKind = "forbidden_cred"
	FaultSlaveDisconnect   FaultKind = "slave_disconnect"
	FaultDriverRestart     FaultKind = "driver_restart"
	FaultModelRouteFailure FaultKind = "model_route_failure"
	FaultDuplicatePickup   FaultKind = "duplicate_pickup"
)

// AllFaultKinds is the canonical, declaration-order list of the 8 injectable
// fault kinds. The /list endpoint, integration tests, and IsKnownKind all
// read from this slice; keep it in lockstep with the constants above.
var AllFaultKinds = []FaultKind{
	FaultMissingFile,
	FaultStaleCapability,
	FaultWrongOSVersion,
	FaultForbiddenCred,
	FaultSlaveDisconnect,
	FaultDriverRestart,
	FaultModelRouteFailure,
	FaultDuplicatePickup,
}

// knownKindSet is the set-form of AllFaultKinds, computed once. IsKnownKind
// reads from it so /inject validation is O(1).
var knownKindSet = func() map[FaultKind]struct{} {
	s := make(map[FaultKind]struct{}, len(AllFaultKinds))
	for _, k := range AllFaultKinds {
		s[k] = struct{}{}
	}
	return s
}()

// IsKnownKind reports whether k is one of the 8 declared fault kinds.
// Arbitrary strings (including superficially similar values like wrong-case
// or trailing whitespace) return false.
func IsKnownKind(k FaultKind) bool {
	_, ok := knownKindSet[k]
	return ok
}

// FaultDirective is one active fault registered with the control plane.
// Seq is monotonic per run_id (1-based); At is the server-side UTC
// timestamp at the moment of registration. Params is always non-nil but
// may be empty.
type FaultDirective struct {
	Kind   FaultKind         `json:"kind"`
	Target string            `json:"target,omitempty"`
	Params map[string]string `json:"params"`
	Seq    int               `json:"seq"`
	At     time.Time         `json:"at"`
}
