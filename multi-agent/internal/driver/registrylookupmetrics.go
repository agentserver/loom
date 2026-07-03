package driver

import "sync/atomic"

// Aggregate per-process counters for driver.Lookup. Per-invocation
// samples land in the observer store's registry_lookup_samples table
// via the LookupDeps.SampleWrite hook; these counters are a debug
// convenience only (production analysis uses the table for per-run
// attribution). Spec §5.

var (
	lookupQueries atomic.Int64
	lookupHits    atomic.Int64
)

// LookupHitRate returns queries-that-yielded-any-hit ÷ total queries.
// Returns 0.0 when no queries have run this process. Queries
// suppressed by NoRegistryLookup are NOT counted in either the
// numerator or the denominator (they never ran).
func LookupHitRate() float64 {
	q := lookupQueries.Load()
	if q == 0 {
		return 0
	}
	h := lookupHits.Load()
	return float64(h) / float64(q)
}

func bumpLookupQuery() { lookupQueries.Add(1) }
func bumpLookupHit()   { lookupHits.Add(1) }

// ResetLookupMetricsForTest zeroes the counters. Test-only.
func ResetLookupMetricsForTest() {
	lookupQueries.Store(0)
	lookupHits.Store(0)
}
