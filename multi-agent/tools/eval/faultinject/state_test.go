//go:build evaltool

package faultinject

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	at := time.Unix(1735689600, 0).UTC() // fixed 2025-01-01 for reproducibility
	s.now = func() time.Time { return at }
	return s
}

// F3: Add/List/Lookup happy path — 3 injects round-trip in order; Lookup
// returns the first directive in injection order for that kind.
func TestStore_AddListLookup(t *testing.T) {
	s := newTestStore(t)
	const runID = "run-abc12345"
	for i, kind := range []FaultKind{FaultMissingFile, FaultMissingFile, FaultStaleCapability} {
		d, err := s.Add(runID, kind, "tgt", map[string]string{"i": "v"})
		if err != nil {
			t.Fatalf("Add[%d]: unexpected error: %v", i, err)
		}
		if d.Seq != i+1 {
			t.Errorf("Add[%d].Seq = %d, want %d", i, d.Seq, i+1)
		}
		if d.Kind != kind {
			t.Errorf("Add[%d].Kind = %q, want %q", i, d.Kind, kind)
		}
	}
	got, err := s.List(runID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List len = %d, want 3", len(got))
	}
	wantKinds := []FaultKind{FaultMissingFile, FaultMissingFile, FaultStaleCapability}
	for i, d := range got {
		if d.Kind != wantKinds[i] {
			t.Errorf("List[%d].Kind = %q, want %q", i, d.Kind, wantKinds[i])
		}
		if d.Seq != i+1 {
			t.Errorf("List[%d].Seq = %d, want %d", i, d.Seq, i+1)
		}
		if d.Params == nil {
			t.Errorf("List[%d].Params is nil; want non-nil empty-or-populated map", i)
		}
	}
	first, ok := s.Lookup(runID, FaultMissingFile)
	if !ok {
		t.Fatalf("Lookup: want hit for FaultMissingFile")
	}
	if first.Seq != 1 {
		t.Errorf("Lookup first.Seq = %d, want 1 (injection order)", first.Seq)
	}
	if _, ok := s.Lookup(runID, FaultWrongOSVersion); ok {
		t.Errorf("Lookup: want miss for un-injected kind")
	}
	// Mutating the returned slice must not corrupt store state.
	got[0].Kind = "tampered"
	got2, _ := s.List(runID)
	if got2[0].Kind != FaultMissingFile {
		t.Errorf("List result was not a copy: got2[0].Kind = %q", got2[0].Kind)
	}
}

// F4: Clear resets the per-(run, kind) rate counter; injection past
// the prior limit succeeds. The per-run Seq counter, however, is
// LIFETIME-MONOTONIC across clear cycles — see TestStore_SeqMonotonicAcrossClear.
func TestStore_ClearResetsCounter(t *testing.T) {
	s := newTestStore(t)
	const runID = "run-clearcounter"
	for i := 0; i < MaxInjectionsPerRun; i++ {
		if _, err := s.Add(runID, FaultMissingFile, "", nil); err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
	}
	// 101st must be rejected.
	if _, err := s.Add(runID, FaultMissingFile, "", nil); !errors.Is(err, ErrInjectionRateLimited) {
		t.Fatalf("Add[101]: err = %v, want ErrInjectionRateLimited", err)
	}
	n, err := s.Clear(runID)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n != MaxInjectionsPerRun {
		t.Errorf("Clear count = %d, want %d", n, MaxInjectionsPerRun)
	}
	// After clear, the rate counter is 0 and the next inject succeeds.
	post, err := s.Add(runID, FaultMissingFile, "", nil)
	if err != nil {
		t.Fatalf("Add after Clear: %v", err)
	}
	// List sees only the fresh inject — but its Seq must be
	// MaxInjectionsPerRun+1, not 1 (lifetime-monotonic, see fix for
	// PR #54 round-2 reviewer P1).
	if post.Seq != MaxInjectionsPerRun+1 {
		t.Errorf("post-clear Seq = %d, want %d (lifetime-monotonic)", post.Seq, MaxInjectionsPerRun+1)
	}
	got, _ := s.List(runID)
	if len(got) != 1 || got[0].Seq != post.Seq {
		t.Errorf("post-clear List = %v; want 1 directive with Seq=%d", got, post.Seq)
	}
}

// New (round-2 reviewer P1): seq must be lifetime-monotonic per run_id.
// Audit log uses (run_id, seq) as a discriminator; if Clear reset seq,
// inject → clear → inject would emit two audit lines with the same key.
func TestStore_SeqMonotonicAcrossClear(t *testing.T) {
	s := newTestStore(t)
	const runID = "run-seqmonotonic"
	const cycles = 10
	const perCycle = 5
	seenSeq := make(map[int]bool)
	for c := 0; c < cycles; c++ {
		for i := 0; i < perCycle; i++ {
			d, err := s.Add(runID, FaultMissingFile, "", nil)
			if err != nil {
				t.Fatalf("cycle %d add %d: %v", c, i, err)
			}
			if seenSeq[d.Seq] {
				t.Fatalf("Seq %d appeared twice (cycle %d) — must be lifetime-monotonic", d.Seq, c)
			}
			seenSeq[d.Seq] = true
		}
		if _, err := s.Clear(runID); err != nil {
			t.Fatalf("cycle %d clear: %v", c, err)
		}
	}
	if got := len(seenSeq); got != cycles*perCycle {
		t.Errorf("distinct seq values = %d, want %d", got, cycles*perCycle)
	}
}

// F5: Per-(run, kind) rate limit. 100 same-kind inserts succeed; 101st is
// rejected. A different kind for the same run is unaffected.
func TestStore_RateLimitPerRunPerKind(t *testing.T) {
	s := newTestStore(t)
	const runID = "run-ratelimit"
	for i := 0; i < MaxInjectionsPerRun; i++ {
		if _, err := s.Add(runID, FaultDriverRestart, "", nil); err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
	}
	if _, err := s.Add(runID, FaultDriverRestart, "", nil); !errors.Is(err, ErrInjectionRateLimited) {
		t.Fatalf("Add[101] same kind: err = %v, want ErrInjectionRateLimited", err)
	}
	// Different kind, same run — accepted.
	if _, err := s.Add(runID, FaultMissingFile, "", nil); err != nil {
		t.Fatalf("Add different kind, same run: %v", err)
	}
	// Different run, same kind — accepted (counter is per-run-per-kind).
	if _, err := s.Add("run-other987", FaultDriverRestart, "", nil); err != nil {
		t.Fatalf("Add same kind, different run: %v", err)
	}
}

// F6: ValidateRunID accepts only ^[A-Za-z0-9_-]{8,128}$.
func TestStore_ValidateRunID_Matrix(t *testing.T) {
	cases := []struct {
		in   string
		want bool // true = valid
	}{
		{"", false},
		{"abc", false},                    // too short (3)
		{"abcdefg", false},                // too short (7)
		{"abcdefgh", true},                // exactly 8
		{strings.Repeat("a", 128), true},  // exactly 128
		{strings.Repeat("a", 129), false}, // 129
		{"with/slash", false},             // bad char
		{"with space", false},             // bad char
		{"with.dot", false},               // bad char (dot not allowed)
		{"good_id-12", true},
		{"GoodID_-09az", true},
		{"\nabcdefgh", false}, // leading newline
		{"abcdefgh\n", false}, // trailing newline
		{"  abcdefgh", false}, // leading spaces
	}
	for _, c := range cases {
		err := ValidateRunID(c.in)
		got := err == nil
		if got != c.want {
			t.Errorf("ValidateRunID(%q) ok=%v (err=%v), want ok=%v", c.in, got, err, c.want)
		}
		if !got && !errors.Is(err, ErrInjectionRunIDInvalid) {
			t.Errorf("ValidateRunID(%q) err = %v; want errors.Is(ErrInjectionRunIDInvalid)", c.in, err)
		}
	}
}

// F7: Sentinel cred is the exact literal mandated by spec §7 (c).
func TestStore_SentinelFakeCred_ByteEquality(t *testing.T) {
	const want = "FAKE_CRED_FOR_INJECTION_DO_NOT_USE"
	if SentinelFakeCred != want {
		t.Fatalf("SentinelFakeCred = %q, want %q (must match spec §7 (c) literal)", SentinelFakeCred, want)
	}
	// Defense in depth: also byte-compare to catch any non-ASCII look-alike.
	if len(SentinelFakeCred) != len(want) {
		t.Fatalf("SentinelFakeCred len = %d, want %d", len(SentinelFakeCred), len(want))
	}
	for i := 0; i < len(want); i++ {
		if SentinelFakeCred[i] != want[i] {
			t.Fatalf("SentinelFakeCred[%d] = %q (%d), want %q (%d)", i, string(SentinelFakeCred[i]), SentinelFakeCred[i], string(want[i]), want[i])
		}
	}
}

// F17/F18 coverage live in server_test.go (the size limits are validation
// at the HTTP layer, not the Store layer — Store accepts any params).

// Extra: Add rejects unknown kinds at the store layer too so callers that
// bypass HTTP still cannot register garbage.
func TestStore_AddRejectsUnknownKind(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("run-runid01", "made_up_kind", "", nil); !errors.Is(err, ErrInjectionKindUnknown) {
		t.Fatalf("Add unknown kind: err = %v, want ErrInjectionKindUnknown", err)
	}
}

// Extra: Add validates run_id.
func TestStore_AddValidatesRunID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("bad/id", FaultMissingFile, "", nil); !errors.Is(err, ErrInjectionRunIDInvalid) {
		t.Fatalf("Add bad runID: err = %v, want ErrInjectionRunIDInvalid", err)
	}
}

// Extra: List/Clear validate run_id.
func TestStore_ListClearValidateRunID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.List("bad"); !errors.Is(err, ErrInjectionRunIDInvalid) {
		t.Errorf("List bad runID: err = %v, want ErrInjectionRunIDInvalid", err)
	}
	if _, err := s.Clear("bad"); !errors.Is(err, ErrInjectionRunIDInvalid) {
		t.Errorf("Clear bad runID: err = %v, want ErrInjectionRunIDInvalid", err)
	}
}

// Extra: Store is safe for concurrent Add from many goroutines.
func TestStore_ConcurrentAdd_NoLostUpdates(t *testing.T) {
	s := newTestStore(t)
	const runID = "run-concurrent01"
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = s.Add(runID, FaultMissingFile, "", nil)
		}()
	}
	wg.Wait()
	got, _ := s.List(runID)
	if len(got) != N {
		t.Fatalf("concurrent Add: got %d directives, want %d", len(got), N)
	}
	// Seq values must be a permutation of 1..N (each unique).
	seen := make(map[int]bool, N)
	for _, d := range got {
		if d.Seq < 1 || d.Seq > N {
			t.Errorf("Seq = %d, out of range [1,%d]", d.Seq, N)
		}
		if seen[d.Seq] {
			t.Errorf("duplicate Seq = %d", d.Seq)
		}
		seen[d.Seq] = true
	}
}
