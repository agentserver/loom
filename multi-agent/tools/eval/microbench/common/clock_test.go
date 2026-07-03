package common

import (
	"testing"
	"time"
)

// Test #32 — common.Now() returns a Time carrying a monotonic reading.
func TestClock_NowHasMonotonicReading(t *testing.T) {
	got := Now()
	if !HasMonotonicReading(got) {
		t.Errorf("Now() did not preserve monotonic reading (round-trip drift)")
	}
	// Negative case: a time built from Unix()/nano has no monotonic reading.
	naked := time.Unix(0, 1_700_000_000_000_000_000)
	if HasMonotonicReading(naked) {
		t.Errorf("time.Unix() should not carry a monotonic reading")
	}
}
