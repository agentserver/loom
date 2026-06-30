//go:build evaltool

package faultinject

import (
	"fmt"
	"regexp"
	"sync"
	"time"
)

// MaxInjectionsPerRun caps how many fault directives a single (run_id, kind)
// pair can register before the control plane rejects further /inject calls
// with ErrInjectionRateLimited. See spec §7 (b).
const MaxInjectionsPerRun = 100

// SentinelFakeCred is the literal value returned by the FaultForbiddenCred
// hook bridge. Per spec §7 (c), this MUST be a hardcoded fake — never a
// real-looking credential — so downstream secret scanners (trufflehog and
// friends) trivially recognise it and ops engineers seeing it in logs
// immediately know it originated from the injector.
const SentinelFakeCred = "FAKE_CRED_FOR_INJECTION_DO_NOT_USE"

// RunIDRegexp is the canonical pattern for run_id strings. ValidateRunID
// is the recommended entry point; this is exported only so callers that
// need to construct runtime help messages can render the pattern.
var RunIDRegexp = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// ValidateRunID returns nil iff s matches RunIDRegexp. A bad value
// returns an error wrapping ErrInjectionRunIDInvalid.
func ValidateRunID(s string) error {
	if !RunIDRegexp.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrInjectionRunIDInvalid, s)
	}
	return nil
}

// Store is the in-memory registry of active fault directives, keyed by
// run_id. Reads (Lookup, List) and writes (Add, Clear) are concurrency-
// safe via a single mutex; per-run-per-kind rate counters live inside the
// same critical section so Add's read-modify-write is atomic.
//
// A Store has no external persistence and no /clear-all endpoint — the
// only retraction path is Clear(runID). Server constructors create one
// Store per process; tests construct a fresh Store per test.
type Store struct {
	mu         sync.Mutex
	perRun     map[string][]FaultDirective
	perRunKind map[string]map[FaultKind]int
	seq        map[string]int
	now        func() time.Time // injectable for tests
}

// NewStore returns an empty Store using time.Now for timestamps.
func NewStore() *Store {
	return &Store{
		perRun:     make(map[string][]FaultDirective),
		perRunKind: make(map[string]map[FaultKind]int),
		seq:        make(map[string]int),
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Add registers a new fault directive for (runID, kind). It validates
// runID and kind, increments the per-(run_id, kind) counter, and assigns
// a monotonic per-run Seq. Returns the registered directive on success.
//
// Errors:
//   - ErrInjectionRunIDInvalid: runID does not match RunIDRegexp
//   - ErrInjectionKindUnknown:  kind is not in AllFaultKinds
//   - ErrInjectionRateLimited:  (runID, kind) is already at MaxInjectionsPerRun
func (s *Store) Add(runID string, kind FaultKind, target string, params map[string]string) (FaultDirective, error) {
	if err := ValidateRunID(runID); err != nil {
		return FaultDirective{}, err
	}
	if !IsKnownKind(kind) {
		return FaultDirective{}, fmt.Errorf("%w: %q", ErrInjectionKindUnknown, kind)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.perRunKind[runID] == nil {
		s.perRunKind[runID] = make(map[FaultKind]int)
	}
	if s.perRunKind[runID][kind] >= MaxInjectionsPerRun {
		return FaultDirective{}, fmt.Errorf("%w: run_id=%q kind=%q already at %d", ErrInjectionRateLimited, runID, kind, MaxInjectionsPerRun)
	}

	s.seq[runID]++
	// Defensive copy of params so the caller cannot mutate stored state.
	storedParams := make(map[string]string, len(params))
	for k, v := range params {
		storedParams[k] = v
	}

	d := FaultDirective{
		Kind:   kind,
		Target: target,
		Params: storedParams,
		Seq:    s.seq[runID],
		At:     s.now(),
	}
	s.perRun[runID] = append(s.perRun[runID], d)
	s.perRunKind[runID][kind]++
	return d, nil
}

// Clear drops every directive for runID and resets the rate counters for
// the run. Returns the number of directives that were cleared and nil on
// success, or (0, ErrInjectionRunIDInvalid) for a malformed runID.
//
// Clearing an unknown-but-well-formed runID is a no-op and returns
// (0, nil).
func (s *Store) Clear(runID string) (int, error) {
	if err := ValidateRunID(runID); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.perRun[runID])
	delete(s.perRun, runID)
	delete(s.perRunKind, runID)
	delete(s.seq, runID)
	return n, nil
}

// List returns a copy of the directives for runID in injection order.
// Returns (nil, ErrInjectionRunIDInvalid) for a malformed runID;
// an unknown-but-well-formed runID returns (empty-slice, nil).
func (s *Store) List(runID string) ([]FaultDirective, error) {
	if err := ValidateRunID(runID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.perRun[runID]
	out := make([]FaultDirective, len(src))
	for i, d := range src {
		copyParams := make(map[string]string, len(d.Params))
		for k, v := range d.Params {
			copyParams[k] = v
		}
		out[i] = FaultDirective{
			Kind:   d.Kind,
			Target: d.Target,
			Params: copyParams,
			Seq:    d.Seq,
			At:     d.At,
		}
	}
	return out, nil
}

// Lookup returns the first directive for (runID, kind) in injection order
// and reports whether a match was found. It does not consume the
// directive — by design, an active fault stays armed until /clear is
// called, so re-reads during a test repeatedly fire the same hook.
//
// Lookup does NOT validate runID; it is on the hot path of every
// InjectIfActive call. A garbage runID simply returns (zero, false).
func (s *Store) Lookup(runID string, kind FaultKind) (FaultDirective, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.perRun[runID] {
		if d.Kind == kind {
			copyParams := make(map[string]string, len(d.Params))
			for k, v := range d.Params {
				copyParams[k] = v
			}
			out := FaultDirective{
				Kind:   d.Kind,
				Target: d.Target,
				Params: copyParams,
				Seq:    d.Seq,
				At:     d.At,
			}
			return out, true
		}
	}
	return FaultDirective{}, false
}
