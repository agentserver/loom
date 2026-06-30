//go:build evaltool

package faultinject

import "errors"

// Sentinel errors returned by Store, Server, and the /inject HTTP handler.
// The /inject handler writes the sentinel's Error() string in the body so
// callers can match on text; tests prefer errors.Is over string matching.
var (
	// ErrControlPlaneMustBeLoopback is returned by NewServer when the
	// configured listen address resolves to any non-loopback IP. This is
	// a hard failure — see spec §7 (a).
	ErrControlPlaneMustBeLoopback = errors.New("faultinject: control plane must bind to loopback only")

	// ErrInjectionRunIDInvalid is returned when a /inject, /clear, or
	// /list request supplies a run_id that does not match the
	// `^[A-Za-z0-9_-]{8,128}$` regexp from spec §7 (f).
	ErrInjectionRunIDInvalid = errors.New("faultinject: run_id invalid")

	// ErrInjectionKindUnknown is returned when /inject's kind is not in
	// AllFaultKinds.
	ErrInjectionKindUnknown = errors.New("faultinject: kind unknown")

	// ErrInjectionRateLimited is returned when /inject would push a
	// (run_id, kind) pair past MaxInjectionsPerRun. See spec §7 (b).
	ErrInjectionRateLimited = errors.New("faultinject: rate limit exceeded for (run_id, kind)")

	// ErrInjectionTargetTooLong is returned when /inject's target string
	// is longer than 512 bytes.
	ErrInjectionTargetTooLong = errors.New("faultinject: target too long (>512 bytes)")

	// ErrInjectionParamsTooLarge is returned when /inject's params map
	// has more than 16 entries or any value longer than 1024 bytes.
	ErrInjectionParamsTooLarge = errors.New("faultinject: params too large (>16 entries or >1024 bytes per value)")
)
