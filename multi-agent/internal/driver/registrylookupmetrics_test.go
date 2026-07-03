package driver

import (
	"math"
	"testing"
)

func TestLookupHitRate_ZeroWhenNoQueries(t *testing.T) {
	ResetLookupMetricsForTest()
	if got := LookupHitRate(); got != 0 {
		t.Fatalf("want 0, got %v", got)
	}
}

func TestLookupHitRate_IncrementsOnHit(t *testing.T) {
	ResetLookupMetricsForTest()
	bumpLookupQuery()
	bumpLookupQuery()
	bumpLookupQuery()
	bumpLookupHit()
	bumpLookupHit()
	got := LookupHitRate()
	want := 2.0 / 3.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}
