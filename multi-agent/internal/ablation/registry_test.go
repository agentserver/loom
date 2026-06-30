package ablation

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKnownFlags_CopyIsolation(t *testing.T) {
	t.Parallel()
	got := KnownFlags()

	// Spec §2.2 + §6 acceptance item 4: the 8 known flags must be returned
	// in the documented declaration order. A weaker test (length-only or
	// element-0-only) would let a maintainer alphabetise the slice and
	// silently break consumers that index into it (e.g. CLI binders).
	want := []FlagName{
		NoCapabilityDiscovery,
		NoTypedContracts,
		NoDryRun,
		NoContractFormalization,
		NoUserPromotionPath,
		NoAcceptanceGate,
		NoRegistryLookup,
		NoObserver,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KnownFlags() order:\n  want %v\n  got  %v", want, got)
	}

	// Mutate the returned slice; a subsequent call must be unaffected
	// (spec §4: callers may modify the returned slice; the package
	// canonical source must not share-mutate).
	got[0] = FlagName("CORRUPTED")
	again := KnownFlags()
	if !reflect.DeepEqual(again, want) {
		t.Errorf("KnownFlags() after caller mutation:\n  want %v\n  got  %v (caller mutation leaked into registry)", want, again)
	}
}

func TestRegister_Success(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	var b bool
	if err := r.Register(NoCapabilityDiscovery, &b); err != nil {
		t.Fatalf("Register: want nil, got %v", err)
	}
}

func TestRegister_NilTarget_ErrNilTarget(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	err := r.Register(NoObserver, nil)
	if !errors.Is(err, ErrNilTarget) {
		t.Errorf("Register(NoObserver, nil): want errors.Is ErrNilTarget, got %v", err)
	}
}

func TestRegister_UnknownFlag_ErrUnknownFlag(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	var b bool
	err := r.Register(FlagName("NoBogus"), &b)
	if !errors.Is(err, ErrUnknownFlag) {
		t.Errorf("Register(NoBogus, &b): want errors.Is ErrUnknownFlag, got %v", err)
	}
}

func TestRegister_Duplicate_ErrAlreadyRegistered(t *testing.T) {
	t.Parallel()
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
	// in TestSetByName_DuplicateRegisterDoesNotOverwrite, which requires
	// a working SetByName (plan §4 step 5).
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
	t.Parallel()
	r := NewRegistry()
	err := r.SetByName("NoTpedContracts", true) // typo of NoTypedContracts
	if !errors.Is(err, ErrUnknownFlag) {
		t.Errorf("SetByName(NoTpedContracts, true): want errors.Is ErrUnknownFlag, got %v", err)
	}
}

func TestSetByName_NotRegistered_ErrNotRegistered(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	err := r.SetByName(string(NoObserver), true)
	if !errors.Is(err, ErrNotRegistered) {
		t.Errorf("SetByName(NoObserver, true) on empty registry: want errors.Is ErrNotRegistered, got %v", err)
	}
}

func TestSetByName_Flips(t *testing.T) {
	t.Parallel()
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

// TestRegister_SameTargetUnderTwoNames_Rejected guards spec §7 (c) against
// a target-aliasing failure that's distinct from name-aliasing: a downstream
// package can copy-paste a Register line and forget to swap the target
// pointer, ending up with two flag names wired to the same *bool. CLI
// `--ablation NoX` and `--ablation NoY` would then flip the SAME toggle
// silently, which is exactly the "two flags secretly share an owner"
// failure mode §7 (c) exists to prevent.
func TestRegister_SameTargetUnderTwoNames_Rejected(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	var b bool
	if err := r.Register(NoCapabilityDiscovery, &b); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(NoObserver, &b)
	if !errors.Is(err, ErrTargetAlreadyRegistered) {
		t.Fatalf("Register(NoObserver, &b) when &b is already registered under NoCapabilityDiscovery: want errors.Is ErrTargetAlreadyRegistered, got %v", err)
	}
	// The second flag must NOT have been wired — SetByName on it returns
	// ErrNotRegistered, proving the rejection wasn't a soft "warn-and-store".
	if err := r.SetByName(string(NoObserver), true); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("SetByName(NoObserver) after rejected duplicate-target Register: want ErrNotRegistered, got %v", err)
	}
	if b {
		t.Errorf("*b after rejected duplicate-target Register + SetByName(NoObserver): want false, got true")
	}
}

// TestRegister_SamePairTwice_ErrAlreadyRegistered pins the documented
// sentinel precedence: an idempotent re-Register with the SAME name and
// the SAME *bool resolves to ErrAlreadyRegistered (name-duplicate beats
// target-duplicate). This makes the failure mode obvious for the
// copy-paste-the-whole-line variant of the §7 (c) bug; without this
// pinning, a future implementation could choose to check target-aliasing
// first and the same caller would see a different sentinel.
func TestRegister_SamePairTwice_ErrAlreadyRegistered(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	var b bool
	if err := r.Register(NoCapabilityDiscovery, &b); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(NoCapabilityDiscovery, &b)
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("Register(NoCapabilityDiscovery, &b) twice: want errors.Is ErrAlreadyRegistered, got %v", err)
	}
	// Today this assertion is trivially true (the sentinels are distinct
	// bare errors.New values, so errors.Is(ErrAlreadyRegistered,
	// ErrTargetAlreadyRegistered) is necessarily false). The check earns
	// its keep once spec §2.5's %w-wrapping rule is exercised by a
	// future enrichment that could otherwise produce a composite error
	// satisfying both sentinels at once — flagging the precedence as
	// part of the public contract rather than an artefact of bare
	// returns.
	if errors.Is(err, ErrTargetAlreadyRegistered) {
		t.Errorf("Register of identical (name, target): also matched ErrTargetAlreadyRegistered (precedence rule violated under future %%w wrapping)")
	}
}

func TestSetByName_DuplicateRegisterDoesNotOverwrite(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
// same flag. The interleaved final value is non-deterministic; the test
// checks (1) the race detector and panics, AND (2) that SetByName
// actually writes through *target — guards against a future refactor
// that moves the deref outside the lock and silently drops the write on
// a non-happy path.
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

	// Final deterministic write — must be observed. If SetByName silently
	// dropped the deref this would fail. Cheap and pins the write-through
	// contract under the lock.
	if err := r.SetByName(string(NoObserver), true); err != nil {
		t.Fatalf("final SetByName: %v", err)
	}
	if !b {
		t.Errorf("after final SetByName(NoObserver, true): *target = false; SetByName dropped the write")
	}
	if err := r.SetByName(string(NoObserver), false); err != nil {
		t.Fatalf("final SetByName false: %v", err)
	}
	if b {
		t.Errorf("after final SetByName(NoObserver, false): *target = true; SetByName dropped the write")
	}
}

// TestConcurrent_RegisterSetList_Race: mixed Register / SetByName / List
// workload. Validates that the mutex spans every method touching the map
// (a List() that read the map outside the lock would trip -race here).
// A "ready" gate + wall-clock budget on the List goroutine ensures it
// actually runs concurrently with the Register/SetByName goroutines —
// without it, List could drain its loop before the workers wake up and
// the test would pass against a totally unlocked List().
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
	var ready sync.WaitGroup
	// Worker goroutines are blocked on `start` until the main goroutine
	// releases them, AFTER the List goroutine is already spinning. This
	// removes the early-finish race where List drains before workers
	// start.
	start := make(chan struct{})
	workers := len(flags) // half Registers + half SetByNames
	ready.Add(workers)

	// Registers for the second half.
	for i := half; i < len(flags); i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
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
			ready.Done()
			<-start
			if err := r.SetByName(string(flags[i]), true); err != nil {
				t.Errorf("SetByName(%q): %v", flags[i], err)
			}
		}()
	}

	// List goroutine: runs for a small wall-clock budget. Tracks the
	// largest snapshot size observed; if it never sees a growth from the
	// pre-registered `half` (i.e. it never observed any concurrent
	// Register landing), the test failed to actually exercise concurrency
	// and we fail loudly rather than passing silently.
	//
	// runtime.Gosched() yields after each iteration so the scheduler can
	// run the worker goroutines under GOMAXPROCS=1 (some CI runners /
	// resource-throttled containers). Without it, this tight loop can
	// dominate the single P for the whole 50ms budget and the maxSeen
	// self-check would flake to false-FAIL.
	wg.Add(1)
	var maxSeen int64
	go func() {
		defer wg.Done()
		<-start
		deadline := time.Now().Add(50 * time.Millisecond)
		for time.Now().Before(deadline) {
			snap := r.List()
			if n := int64(len(snap)); n > atomic.LoadInt64(&maxSeen) {
				atomic.StoreInt64(&maxSeen, n)
			}
			runtime.Gosched()
		}
	}()

	// Wait until every worker is parked on `start`, then release them
	// simultaneously with the List goroutine.
	ready.Wait()
	close(start)
	wg.Wait()

	final := r.List()
	if len(final) != len(flags) {
		t.Errorf("after mixed workload: List len = %d, want %d", len(final), len(flags))
	}
	// At least one List() snapshot must have observed the registry
	// growing past its pre-registered size, i.e. List ran concurrently
	// with at least one Register. If maxSeen == half, the List goroutine
	// finished before any concurrent Register landed (or the registry was
	// frozen), and the test wasn't exercising the contract it claims to.
	if seen := atomic.LoadInt64(&maxSeen); seen <= int64(half) {
		t.Errorf("List goroutine never observed a concurrent Register landing (maxSeen=%d, want > half=%d); test did not actually exercise concurrent List vs Register", seen, half)
	}
}

// TestDefault_IsRegistryAndIndependent guards against an accidental
// refactor of Default into a per-call constructor or a shared instance
// with NewRegistry. The identity check captures Default into a local so
// the comparison is not folded by the compiler.
func TestDefault_IsRegistryAndIndependent(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestSentinels_AreDistinct guards against an accidental `var ErrFoo =
// ErrBar` aliasing in a future consolidation refactor, which would
// silently merge two error categories. Also asserts each sentinel's
// Error() string starts with the "ablation: " package prefix — catches a
// copy-paste error like `errors.New("")` or a sentinel pulled in from a
// neighbouring package.
func TestSentinels_AreDistinct(t *testing.T) {
	t.Parallel()
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrUnknownFlag", ErrUnknownFlag},
		{"ErrNilTarget", ErrNilTarget},
		{"ErrAlreadyRegistered", ErrAlreadyRegistered},
		{"ErrNotRegistered", ErrNotRegistered},
		{"ErrTargetAlreadyRegistered", ErrTargetAlreadyRegistered},
	}
	for i, a := range sentinels {
		if !strings.HasPrefix(a.err.Error(), "ablation: ") {
			t.Errorf("%s.Error() = %q; want prefix \"ablation: \"", a.name, a.err.Error())
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
