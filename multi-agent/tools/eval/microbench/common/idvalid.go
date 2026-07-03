package common

// Conversation-ID validation used by the microbench + scale sweep harnesses
// before they emit a probe_events row. The regex + character class MUST
// match observerstore.probeConvIDRE exactly — any drift means a bench
// happily generates spans that the observerstore boundary silently
// rejects (spec §6 (c)).

import (
	"expvar"
	"regexp"
)

// ConvIDRegex is the exact regex from spec §6 (c). Exported so the sweep
// harness can format an operator-friendly diagnostic that includes it.
var ConvIDRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// probeIDRejectedTotal is the microbench-side counter. The observerstore
// package registers its own counter of the same name; expvar.NewInt would
// panic on double-registration, so we look up first.
var probeIDRejectedTotal = func() *expvar.Int {
	if v := expvar.Get("probe_id_rejected_total"); v != nil {
		if iv, ok := v.(*expvar.Int); ok {
			return iv
		}
	}
	return expvar.NewInt("probe_id_rejected_total")
}()

// ValidConversationID returns true iff s matches spec §6 (c)'s regex.
// A false result bumps probe_id_rejected_total for operator diagnostics.
func ValidConversationID(s string) bool {
	if !ConvIDRegex.MatchString(s) {
		probeIDRejectedTotal.Add(1)
		return false
	}
	return true
}
