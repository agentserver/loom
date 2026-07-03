package common

// Package-local monotonic clock accessor. Direct callers should use
// time.Now(); this single indirection exists so tests can assert
// monotonic-reading presence (spec §6 (b)) without needing to poke at
// runtime internals.

import "time"

// Now returns the current time with Go's stdlib monotonic reading
// preserved. Callers in the microbench harness use time.Now() directly;
// this wrapper is here for the drift test in clock_test.go.
func Now() time.Time { return time.Now() }

// HasMonotonicReading returns true iff t was constructed with Go's
// monotonic clock (i.e., via time.Now()). Documented Go behaviour:
// t.Round(0) strips the monotonic reading, so a Time with monotonic
// reading differs from its .Round(0) form only in the hidden monotonic
// component. Comparing them with .Equal (which ignores monotonic) is
// always true; comparing with == (which considers the whole struct)
// differs iff a monotonic reading is present.
func HasMonotonicReading(t time.Time) bool {
	return t != t.Round(0)
}
