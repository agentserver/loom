package ablation

import (
	"errors"
	"testing"
)

// TestSkillFlags_NoAcceptanceGate_Registered confirms that this file's
// init() successfully wired NoAcceptanceGate into Default. Default.List()
// must include the flag, and SetByName must flip the package-private
// target observable via IsNoAcceptanceGate.
//
// Test isolation note: noAcceptanceGate is process-wide mutable state,
// shared with the WT-1-ablation-registry suite which uses
// NewRegistry()-based local Registry objects (so they do not collide
// with Default). The setbyname_flips_target subtest writes through
// Default and therefore cannot run with t.Parallel; the present_in_list
// subtest is a pure read and may. We re-set the gate to false at
// Cleanup time so the test never leaves Default with a sticky `true`
// for any subsequent test runner pass — but a panic or hard-exit
// between the SetByName(true) and the Cleanup would leak the value,
// which is the unavoidable price of testing process-wide state. The
// registry's documented "pre-run-only mutation" contract (registry.go
// Registry godoc) is the long-term mitigation: production binders
// flip flags once during CLI parsing, not from goroutines.
func TestSkillFlags_NoAcceptanceGate_Registered(t *testing.T) {
	t.Run("present_in_default_list", func(t *testing.T) {
		t.Parallel()
		found := false
		for _, fn := range Default.List() {
			if fn == NoAcceptanceGate {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Default.List() missing NoAcceptanceGate; got %v", Default.List())
		}
	})

	t.Run("setbyname_flips_target", func(t *testing.T) {
		// NOT parallel: mutates process-wide Default state. If a
		// future maintainer adds another writer to Default, they must
		// also wrap that writer in an explicit sync (e.g. a test-only
		// sync.Mutex declared at file scope and acquired by every
		// non-parallel writer subtest in this package). Today there
		// is exactly one writer — this subtest — so the no-mutex
		// stance is safe but fragile.
		prev := IsNoAcceptanceGate()
		t.Cleanup(func() {
			// Best-effort restore. If this Cleanup itself errors,
			// surface it as a normal test failure rather than masking
			// the leak — a stuck-on flag will then break the next
			// run's present_in_list assertion (or any future test that
			// reads the gate), making the leak visible.
			if err := Default.SetByName(string(NoAcceptanceGate), prev); err != nil {
				t.Errorf("cleanup restore failed; gate may leak: %v", err)
			}
		})

		if err := Default.SetByName(string(NoAcceptanceGate), true); err != nil {
			t.Fatalf("SetByName: %v", err)
		}
		if !IsNoAcceptanceGate() {
			t.Errorf("IsNoAcceptanceGate after SetByName(true): want true, got false")
		}

		if err := Default.SetByName(string(NoAcceptanceGate), false); err != nil {
			t.Fatalf("SetByName(false): %v", err)
		}
		if IsNoAcceptanceGate() {
			t.Errorf("IsNoAcceptanceGate after SetByName(false): want false, got true")
		}
	})
}

// TestSkillFlags_MustRegister_PanicsOnError exercises the production
// mustRegister wrapper directly against a fresh local Registry so the
// process-wide Default stays unmodified. We deliberately re-register
// the same name to provoke ErrAlreadyRegistered, then assert the
// panic value wraps that sentinel.
//
// Calling the real mustRegister (rather than an inline mirror) means
// a regression like `_ = reg.Register(...)` or `return` instead of
// `panic` would now make this test fail loudly. The earlier shape
// (inline mirror) was caught by PR #57 review as a test-gap.
func TestSkillFlags_MustRegister_PanicsOnError(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from mustRegister, got none")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panic value not an error: %T %v", r, r)
		}
		if !errors.Is(err, ErrAlreadyRegistered) {
			t.Errorf("panic err: want errors.Is ErrAlreadyRegistered, got %v", err)
		}
	}()

	// First Register against a fresh local Registry: must succeed.
	reg := NewRegistry()
	var b1, b2 bool
	mustRegister(reg, NoAcceptanceGate, &b1)
	// Second Register with same name + different target: must panic
	// with ErrAlreadyRegistered (per registry contract — name
	// duplicate beats target duplicate).
	mustRegister(reg, NoAcceptanceGate, &b2)
	t.Fatal("mustRegister did not panic; expected ErrAlreadyRegistered")
}
