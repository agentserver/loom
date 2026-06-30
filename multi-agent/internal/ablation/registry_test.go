package ablation

import (
	"errors"
	"sort"
	"sync"
	"testing"
)

func TestKnownFlags_CopyIsolation(t *testing.T) {
	got := KnownFlags()
	if len(got) != 8 {
		t.Fatalf("KnownFlags(): want length 8, got %d (%v)", len(got), got)
	}
	if got[0] != NoCapabilityDiscovery {
		t.Errorf("KnownFlags()[0]: want %q, got %q", NoCapabilityDiscovery, got[0])
	}

	// Mutate the returned slice; a subsequent call must be unaffected.
	got[0] = FlagName("CORRUPTED")
	again := KnownFlags()
	if again[0] != NoCapabilityDiscovery {
		t.Errorf("KnownFlags()[0] after caller mutation: want %q, got %q (caller mutation leaked into registry)", NoCapabilityDiscovery, again[0])
	}
}

func TestRegister_Success(t *testing.T) {
	r := NewRegistry()
	var b bool
	if err := r.Register(NoCapabilityDiscovery, &b); err != nil {
		t.Fatalf("Register: want nil, got %v", err)
	}
}

func TestRegister_NilTarget_ErrNilTarget(t *testing.T) {
	r := NewRegistry()
	err := r.Register(NoObserver, nil)
	if !errors.Is(err, ErrNilTarget) {
		t.Errorf("Register(NoObserver, nil): want errors.Is ErrNilTarget, got %v", err)
	}
}

func TestRegister_UnknownFlag_ErrUnknownFlag(t *testing.T) {
	r := NewRegistry()
	var b bool
	err := r.Register(FlagName("NoBogus"), &b)
	if !errors.Is(err, ErrUnknownFlag) {
		t.Errorf("Register(NoBogus, &b): want errors.Is ErrUnknownFlag, got %v", err)
	}
}

func TestRegister_Duplicate_ErrAlreadyRegistered(t *testing.T) {
	r := NewRegistry()
	var first, second bool
	if err := r.Register(NoDryRun, &first); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(NoDryRun, &second)
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register: want errors.Is ErrAlreadyRegistered, got %v", err)
	}
	// The wired-target behaviour (first vs second) is covered end-to-end
	// in TestSetByName_DuplicateRegisterDoesNotOverwrite (step 4), which
	// requires a working SetByName.
	_ = first
	_ = second
}

// TestSetByName_Unknown_ErrUnknownFlag is the spec §7 (b) headline
// regression test. A naive SetByName that only checks the registered-
// targets map would return ErrNotRegistered for an unknown name —
// "loud", but the WRONG sentinel, which lets the CLI binder misclassify
// a typo as "package not linked" diagnostic noise. The correct
// behaviour is ErrUnknownFlag (the name is not in canonicalSet at all).
func TestSetByName_Unknown_ErrUnknownFlag(t *testing.T) {
	r := NewRegistry()
	err := r.SetByName("NoTpedContracts", true) // typo of NoTypedContracts
	if !errors.Is(err, ErrUnknownFlag) {
		t.Errorf("SetByName(NoTpedContracts, true): want errors.Is ErrUnknownFlag, got %v", err)
	}
}

func TestSetByName_NotRegistered_ErrNotRegistered(t *testing.T) {
	r := NewRegistry()
	err := r.SetByName(string(NoObserver), true)
	if !errors.Is(err, ErrNotRegistered) {
		t.Errorf("SetByName(NoObserver, true) on empty registry: want errors.Is ErrNotRegistered, got %v", err)
	}
}

func TestSetByName_Flips(t *testing.T) {
	r := NewRegistry()
	var b bool
	if err := r.Register(NoCapabilityDiscovery, &b); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, want := range []bool{true, false, true} {
		if err := r.SetByName(string(NoCapabilityDiscovery), want); err != nil {
			t.Fatalf("SetByName(_, %v): %v", want, err)
		}
		if b != want {
			t.Errorf("after SetByName(%v): *target = %v, want %v", want, b, want)
		}
	}
}

func TestSetByName_DuplicateRegisterDoesNotOverwrite(t *testing.T) {
	r := NewRegistry()
	var first, second bool
	if err := r.Register(NoDryRun, &first); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(NoDryRun, &second); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register: want ErrAlreadyRegistered, got %v", err)
	}
	if err := r.SetByName(string(NoDryRun), true); err != nil {
		t.Fatalf("SetByName: %v", err)
	}
	if !first {
		t.Errorf("first target after SetByName: want true, got false (duplicate Register overwrote the wired target)")
	}
	if second {
		t.Errorf("second target after SetByName: want false (was never wired), got true")
	}
}

func TestList_Stable(t *testing.T) {
	r := NewRegistry()
	// Register an out-of-canonical-order subset to exercise the sort.
	var a, b, c bool
	if err := r.Register(NoObserver, &a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(NoDryRun, &b); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(NoAcceptanceGate, &c); err != nil {
		t.Fatal(err)
	}

	want := []FlagName{NoAcceptanceGate, NoDryRun, NoObserver} // ascending string order
	got := r.List()
	if len(got) != len(want) {
		t.Fatalf("List len: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List[%d]: want %q, got %q (full slice: %v)", i, want[i], got[i], got)
		}
	}

	// Stability: a second call returns an element-equal slice.
	again := r.List()
	for i := range again {
		if again[i] != got[i] {
			t.Errorf("second List[%d]: want %q (stable), got %q", i, got[i], again[i])
		}
	}
}

// TestConcurrent_Register_Race: 8 goroutines, each registering one of the
// 8 known flags concurrently. With -race, an unprotected map would either
// trip the race detector or leave the registry inconsistent (final List()
// size != 8). The test is also a smoke check for "Register doesn't panic
// under concurrent use".
func TestConcurrent_Register_Race(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	flags := KnownFlags()
	targets := make([]bool, len(flags))

	var wg sync.WaitGroup
	wg.Add(len(flags))
	for i := range flags {
		i := i
		go func() {
			defer wg.Done()
			if err := r.Register(flags[i], &targets[i]); err != nil {
				t.Errorf("Register(%q): %v", flags[i], err)
			}
		}()
	}
	wg.Wait()

	if got := r.List(); len(got) != len(flags) {
		t.Errorf("after concurrent Register: List len = %d, want %d (registry corrupted)", len(got), len(flags))
	}
}

// TestConcurrent_SetByName_Race: many goroutines hammer SetByName on the
// same flag. The final *target value is intentionally non-deterministic —
// the test asserts only that no race / panic occurs.
func TestConcurrent_SetByName_Race(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	var b bool
	if err := r.Register(NoObserver, &b); err != nil {
		t.Fatal(err)
	}

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			if err := r.SetByName(string(NoObserver), i%2 == 0); err != nil {
				t.Errorf("SetByName: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestConcurrent_RegisterSetList_Race: mixed Register / SetByName / List
// workload. Validates that the mutex spans every method touching the map,
// not just Register. Asserts that every List() snapshot is in ascending
// order (catches sort-after-unlock vs sort-before-unlock regressions).
func TestConcurrent_RegisterSetList_Race(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	// Pre-register half the canonical flags so SetByName has something to
	// flip; leave the other half for concurrent Register.
	flags := KnownFlags()
	half := len(flags) / 2
	preTargets := make([]bool, half)
	for i := 0; i < half; i++ {
		if err := r.Register(flags[i], &preTargets[i]); err != nil {
			t.Fatal(err)
		}
	}
	postTargets := make([]bool, len(flags)-half)

	var wg sync.WaitGroup

	// Registers for the second half.
	for i := half; i < len(flags); i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			if err := r.Register(flags[i], &postTargets[i-half]); err != nil {
				t.Errorf("Register(%q): %v", flags[i], err)
			}
		}()
	}

	// SetByName on the first half.
	for i := 0; i < half; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			if err := r.SetByName(string(flags[i]), true); err != nil {
				t.Errorf("SetByName(%q): %v", flags[i], err)
			}
		}()
	}

	// List in a small loop, checking sortedness each time.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 32; i++ {
			snap := r.List()
			if !sort.SliceIsSorted(snap, func(i, j int) bool { return snap[i] < snap[j] }) {
				t.Errorf("List() returned unsorted slice: %v", snap)
				return
			}
		}
	}()

	wg.Wait()

	final := r.List()
	if len(final) != len(flags) {
		t.Errorf("after mixed workload: List len = %d, want %d", len(final), len(flags))
	}
}

// TestDefault_IsRegistryAndIndependent guards against an accidental
// refactor of Default into a per-call constructor or a shared instance
// with NewRegistry. The identity check captures Default into a local so
// the comparison is not folded by the compiler.
func TestDefault_IsRegistryAndIndependent(t *testing.T) {
	if Default == nil {
		t.Fatal("Default is nil; expected process-wide registry singleton")
	}
	first := Default
	second := Default
	if first != second {
		t.Errorf("Default identity not stable across reads (%p vs %p)", first, second)
	}
	if NewRegistry() == Default {
		t.Errorf("NewRegistry() returned the Default singleton; expected a fresh instance")
	}
}

// TestZeroValueRegistry_DoesNotPanic exercises a Registry value created
// without NewRegistry. The spec's API contract says Register never
// panics, so the implementation must lazy-init its internal map.
// SetByName on the same zero-value Registry (before any Register) must
// return ErrNotRegistered, not panic on a nil map read.
func TestZeroValueRegistry_DoesNotPanic(t *testing.T) {
	var r Registry
	var b bool
	if err := r.Register(NoCapabilityDiscovery, &b); err != nil {
		t.Fatalf("Register on zero-value Registry: %v", err)
	}
	if err := r.SetByName(string(NoCapabilityDiscovery), true); err != nil {
		t.Fatalf("SetByName after Register on zero-value Registry: %v", err)
	}
	if !b {
		t.Errorf("after SetByName(true), *target = false; want true")
	}

	var empty Registry
	if err := empty.SetByName(string(NoObserver), true); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("SetByName on zero-value Registry (no Register): want ErrNotRegistered, got %v", err)
	}
	if got := empty.List(); len(got) != 0 {
		t.Errorf("List on zero-value Registry: want empty, got %v", got)
	}
}

// TestSentinels_AreDistinct is a non-RED regression smoke test: two
// distinct errors.New values are always distinct, so this can't
// meaningfully fail now. It exists to fail loudly if a future refactor
// accidentally writes `var ErrFoo = ErrBar` (e.g. while consolidating
// error declarations), which would silently merge two error categories.
func TestSentinels_AreDistinct(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrUnknownFlag", ErrUnknownFlag},
		{"ErrNilTarget", ErrNilTarget},
		{"ErrAlreadyRegistered", ErrAlreadyRegistered},
		{"ErrNotRegistered", ErrNotRegistered},
	}
	for i, a := range sentinels {
		if !errors.Is(a.err, a.err) {
			t.Errorf("%s does not match itself via errors.Is", a.name)
		}
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a.err, b.err) {
				t.Errorf("%s matches %s via errors.Is (sentinel aliasing regression)", a.name, b.name)
			}
		}
	}
}
