package contract

import (
	"bytes"
	"strings"
	"sync/atomic"
)

// completenessDenominator is the fixed denominator for
// ContractCompletenessEvent.CompletenessRatio. Per spec §1.1 + §2.6 this
// is intentionally a constant equal to 7 (the number of lifecycle fields
// in §2.2 table rows 1..7); the two trace fields (version,
// conversation_id) are required for Validate to return nil but are NOT
// counted in the completeness ratio.
//
// Exported via the package-level identifier so the §6 T30 regression test
// can pin this value — if a future commit changes the denominator, that
// test fails before any ratio math runs.
const completenessDenominator = 7

// Lifecycle field names emitted in ContractCompletenessEvent.PresentFields
// in §2.2 table order. The exact strings are part of the wire contract
// with downstream eval consumers (§D2).
const (
	lifecycleIntentGoal             = "intent.goal"
	lifecycleSuccessCriteria        = "intent.success_criteria"
	lifecycleReadArtifacts          = "data_contract.read_artifacts"
	lifecycleWriteTargets           = "data_contract.write_targets"
	lifecycleCapabilityRequirements = "capability_requirements"
	lifecycleExecutionPolicy        = "execution_policy"
	lifecycleRecoveryHint           = "recovery_hint"
)

// lifecycleFieldOrder is the canonical §2.2 ordering. Tests assert
// PresentFields appears in this order.
var lifecycleFieldOrder = []string{
	lifecycleIntentGoal,
	lifecycleSuccessCriteria,
	lifecycleReadArtifacts,
	lifecycleWriteTargets,
	lifecycleCapabilityRequirements,
	lifecycleExecutionPolicy,
	lifecycleRecoveryHint,
}

// ContractCompletenessEvent is the per-contract telemetry payload emitted
// once per successful Validate() call. Per spec §7 (d), the payload
// contains ONLY the bitmap and the derived ratio — never the contract
// body. Downstream §D2 aggregates these to a percentile.
type ContractCompletenessEvent struct {
	ConversationID    string   `json:"conversation_id"`
	PresentFields     []string `json:"present_fields"`
	CompletenessRatio float64  `json:"completeness_ratio"`
}

// EventSink receives one ContractCompletenessEvent per successful
// Validate() call. Implementations MUST NOT block on network or disk I/O
// on the calling goroutine; the typical implementation is a channel send
// into an observer-owned goroutine.
type EventSink interface {
	EmitContractCompleteness(ContractCompletenessEvent)
}

// sinkHolder wraps an EventSink so atomic.Pointer has a concrete type to
// store (interfaces themselves are not directly storable in
// atomic.Pointer in Go 1.21+ without a wrapper).
type sinkHolder struct{ sink EventSink }

var currentSinkPtr atomic.Pointer[sinkHolder]

// RegisterCompletenessSink atomically swaps the package-level sink and
// returns the previous one. Pass nil to revert to the silent default.
// Tests typically use RegisterCompletenessSink in pairs with t.Cleanup.
//
// Concurrency: safe to call from multiple goroutines; concurrent
// Validate() calls see either the old or the new sink, never a torn
// pointer. The `freshSink` test helper (plan §4) uses this with
// t.Cleanup to guarantee tests don't leak sink state.
func RegisterCompletenessSink(sink EventSink) EventSink {
	newH := &sinkHolder{sink: sink}
	old := currentSinkPtr.Swap(newH)
	if old == nil {
		return nil
	}
	return old.sink
}

// currentSink returns the registered sink or nil. Called on the hot
// path of Validate(); a nil return is a no-op.
func currentSink() EventSink {
	h := currentSinkPtr.Load()
	if h == nil {
		return nil
	}
	return h.sink
}

// emitCompleteness fires a ContractCompletenessEvent at the registered
// sink if one is present. No-op when the sink is nil. The event is
// computed from `tc` exactly as observed (no "would-have-passed"
// reasoning; see spec §3.1).
func emitCompleteness(tc TaskContract) {
	sink := currentSink()
	if sink == nil {
		return
	}
	present := presentFields(tc)
	sink.EmitContractCompleteness(ContractCompletenessEvent{
		ConversationID:    tc.ConversationID,
		PresentFields:     present,
		CompletenessRatio: float64(len(present)) / float64(completenessDenominator),
	})
}

// presentFields returns the §2.2 lifecycle field names that are
// considered present on tc per the §2.6 bitmap-counting rule:
//
//   - slice-typed fields: present iff non-nil (length 0 is fine — an
//     explicit empty slice IS a declaration; see §2.2.1);
//   - struct-typed fields (capability_requirements, execution_policy):
//     present iff at least one sub-field is non-zero;
//   - string-typed fields (intent.goal, recovery_hint): present iff
//     strings.TrimSpace(...) != "".
//
// Returned in §2.2 table order, so a downstream serializer's bitmap is
// deterministic.
func presentFields(tc TaskContract) []string {
	var out []string
	if strings.TrimSpace(tc.Intent.Goal) != "" {
		out = append(out, lifecycleIntentGoal)
	}
	if tc.Intent.SuccessCriteria != nil && hasNonEmptyEntry(tc.Intent.SuccessCriteria) {
		out = append(out, lifecycleSuccessCriteria)
	}
	if tc.DataContract.ReadArtifacts != nil {
		out = append(out, lifecycleReadArtifacts)
	}
	// WriteTargets parallels SuccessCriteria as a "must have at least one
	// entry" field (§2.6 carve-out): an empty write_targets slice is a
	// meaningless output declaration (the task produces nothing observable)
	// rather than a "considered the outputs; there are none" statement, so
	// the bitmap counts it as absent. This matches collectMissing's
	// `len == 0` rejection rule so the validity and completeness signals
	// agree. The asymmetry vs ReadArtifacts is intentional and parallels
	// the SuccessCriteria carve-out: outputs and oracles are productive
	// fields; inputs can legitimately be empty.
	if len(tc.DataContract.WriteTargets) > 0 {
		out = append(out, lifecycleWriteTargets)
	}
	if capabilityRequirementsPresent(tc.CapabilityRequirements) {
		out = append(out, lifecycleCapabilityRequirements)
	}
	if executionPolicyPresent(tc.ExecutionPolicy) {
		out = append(out, lifecycleExecutionPolicy)
	}
	if strings.TrimSpace(tc.RecoveryHint) != "" {
		out = append(out, lifecycleRecoveryHint)
	}
	return out
}

func hasNonEmptyEntry(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func capabilityRequirementsPresent(cr CapabilityRequirements) bool {
	// Present iff ANY sub-field is meaningfully declared.
	//
	// Slices (Skills, Tools): non-nil-vs-nil is the test, per §2.2.1.
	// An explicit `Skills: []string{}` declares "I considered the
	// skills; there are none required" — a valid declaration for an
	// opaque/generic task.
	//
	// Resources (json.RawMessage = []byte): the nil-vs-non-nil test
	// alone is NOT sufficient because Go's encoding/json decodes the
	// literals `"resources": null` and `"resources": {}` to non-nil
	// `[]byte("null")` and `[]byte("{}")` respectively — neither is a
	// declaration of capability requirements. We treat both as
	// equivalent to absence: the operator who writes either of them
	// did not say "I considered resources; here is the (possibly
	// empty) declaration". See PR #52 round-2 review P1-2 for the
	// real-world bypass this guards against.
	return cr.Skills != nil || cr.Tools != nil || resourcesDeclared(cr.Resources)
}

// resourcesDeclared returns true iff the RawMessage is non-nil AND its
// trimmed content is not the literal token `null`. Empty object `{}` is
// counted as a (trivial) declaration because the operator typed
// something — they're saying "there is an object here, just empty".
// JSON `null`, however, is the JSON way to say "no value", which is
// semantically identical to the field being absent.
func resourcesDeclared(r []byte) bool {
	if len(r) == 0 {
		return false
	}
	return !bytes.Equal(bytes.TrimSpace(r), []byte("null"))
}

func executionPolicyPresent(p ExecutionPolicy) bool {
	// Present iff at least one of the enum-string sub-fields is non-empty.
	// After ApplyDefaults this is always true; before ApplyDefaults a
	// caller who passed a zero-value struct trips the missing check.
	return p.Routing != "" || p.CodePersistence != "" || p.ExposeCodeToUser != "" || p.WriteMode != ""
}
