//go:build evaltool

package faultinject

import (
	"reflect"
	"testing"
)

// F1: AllFaultKinds must contain exactly the 8 declared kinds, in
// declaration order, with no duplicates and no extras.
func TestKinds_AllEightDeclared(t *testing.T) {
	want := []FaultKind{
		FaultMissingFile,
		FaultStaleCapability,
		FaultWrongOSVersion,
		FaultForbiddenCred,
		FaultSlaveDisconnect,
		FaultDriverRestart,
		FaultModelRouteFailure,
		FaultDuplicatePickup,
	}
	if len(AllFaultKinds) != 8 {
		t.Fatalf("AllFaultKinds: want 8 entries, got %d (%v)", len(AllFaultKinds), AllFaultKinds)
	}
	if !reflect.DeepEqual(AllFaultKinds, want) {
		t.Fatalf("AllFaultKinds order/contents mismatch:\n got=%v\nwant=%v", AllFaultKinds, want)
	}
	seen := make(map[FaultKind]struct{}, len(AllFaultKinds))
	for _, k := range AllFaultKinds {
		if _, dup := seen[k]; dup {
			t.Fatalf("AllFaultKinds: duplicate entry %q", k)
		}
		seen[k] = struct{}{}
	}
}

// F2: Every kind in AllFaultKinds is recognised by IsKnownKind; arbitrary
// strings (including ones similar to real kinds) are not.
func TestKinds_IsKnownRoundTrip(t *testing.T) {
	for _, k := range AllFaultKinds {
		if !IsKnownKind(k) {
			t.Errorf("IsKnownKind(%q) = false; want true (kind is in AllFaultKinds)", k)
		}
	}
	for _, bogus := range []FaultKind{
		"",
		"made_up",
		"MISSING_FILE",         // wrong case
		"missing_file ",        // trailing space
		" missing_file",        // leading space
		"missing-file",         // wrong separator
		"forbidden_credential", // close to real, but not equal
	} {
		if IsKnownKind(bogus) {
			t.Errorf("IsKnownKind(%q) = true; want false", bogus)
		}
	}
}
