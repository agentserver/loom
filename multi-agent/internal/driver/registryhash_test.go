package driver

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const emptyBytesSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestComputeRegistryHash_EmptyNamesEqualsSHA256OfEmptyBytes(t *testing.T) {
	got := ComputeRegistryHash(nil, func(string) string { return "" })
	if got != emptyBytesSHA256 {
		t.Fatalf("empty registry hash: got %q want %q", got, emptyBytesSHA256)
	}
	got2 := ComputeRegistryHash([]string{}, func(string) string { return "" })
	if got2 != emptyBytesSHA256 {
		t.Fatalf("empty slice hash: got %q want %q", got2, emptyBytesSHA256)
	}
}

func TestComputeRegistryHash_OrderIndependent(t *testing.T) {
	desc := func(n string) string { return "h-" + n }
	a := ComputeRegistryHash([]string{"x", "y", "z"}, desc)
	b := ComputeRegistryHash([]string{"z", "x", "y"}, desc)
	if a != b {
		t.Fatalf("order-dependent: %q vs %q", a, b)
	}
}

func TestComputeRegistryHash_ChangesOnDescriptorEdit(t *testing.T) {
	names := []string{"tool_a"}
	h1 := ComputeRegistryHash(names, func(string) string { return "v1" })
	h2 := ComputeRegistryHash(names, func(string) string { return "v2" })
	if h1 == h2 {
		t.Fatalf("descriptor edit did not change hash: %q", h1)
	}
}

func TestComputeRegistryHash_ChangesOnNameEdit(t *testing.T) {
	desc := func(string) string { return "v" }
	h1 := ComputeRegistryHash([]string{"tool_a"}, desc)
	h2 := ComputeRegistryHash([]string{"tool_b"}, desc)
	if h1 == h2 {
		t.Fatalf("name edit did not change hash: %q", h1)
	}
}

func TestComputeRegistryHash_DeterministicAcrossRuns(t *testing.T) {
	desc := func(n string) string { return "spec-" + n }
	names := []string{"alpha", "beta"}
	h1 := ComputeRegistryHash(names, desc)
	h2 := ComputeRegistryHash(names, desc)
	if h1 != h2 {
		t.Fatalf("non-deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("hash length = %d, want 64", len(h1))
	}
}

func TestLastRegistryHash_ZeroValueReturnsEmptyHash(t *testing.T) {
	resetRegistryForTest()
	got := LastRegistryHash()
	if got != emptyBytesSHA256 {
		t.Fatalf("zero-value LastRegistryHash: got %q want %q", got, emptyBytesSHA256)
	}
	// Must never return "".
	if got == "" {
		t.Fatal("LastRegistryHash returned empty string")
	}
}

func TestSetLastRegistryHash_Publishes(t *testing.T) {
	resetRegistryForTest()
	SetLastRegistryHash("abc123")
	if got := LastRegistryHash(); got != "abc123" {
		t.Fatalf("got %q want %q", got, "abc123")
	}
}

func TestLastRegistryHash_ConcurrentReadWrite(t *testing.T) {
	resetRegistryForTest()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 8 readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := LastRegistryHash()
				if got == "" {
					t.Errorf("LastRegistryHash returned empty during race")
					return
				}
			}
		}()
	}
	// 4 writers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				select {
				case <-stop:
					return
				default:
				}
				SetLastRegistryHash("h" + strings.Repeat("x", 63))
			}
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestDriverRegistryView_UndercountsUntilFirstRegister — spec §7 (j).
// (1) fresh view is empty for any given slave, (2) after two register
// calls the hash reflects both, (3) no code path in registryhash.go
// touches network / slave file.
func TestDriverRegistryView_UndercountsUntilFirstRegister(t *testing.T) {
	resetRegistryForTest()

	// (1) fresh view: LastRegistryHash is the empty-bytes sha256.
	if LastRegistryHash() != emptyBytesSHA256 {
		t.Fatalf("fresh view should yield empty-bytes hash")
	}

	// (2) two register calls, hash reflects them.
	noteRegister("slave-a", "tool_x", "spec-hash-x")
	names, desc := snapshotAll()
	h1 := ComputeRegistryHash(names, desc)
	SetLastRegistryHash(h1)

	noteRegister("slave-a", "tool_y", "spec-hash-y")
	names, desc = snapshotAll()
	h2 := ComputeRegistryHash(names, desc)
	SetLastRegistryHash(h2)

	if h1 == emptyBytesSHA256 || h2 == emptyBytesSHA256 {
		t.Fatalf("hash still empty after register")
	}
	if h1 == h2 {
		t.Fatalf("hash unchanged between two registers: %q", h1)
	}

	// (2b) unregister returns to h1.
	noteUnregister("slave-a", "tool_y")
	names, desc = snapshotAll()
	h1again := ComputeRegistryHash(names, desc)
	if h1again != h1 {
		t.Fatalf("unregister did not restore h1: got %q want %q", h1again, h1)
	}

	// (3) source-level guard: registryhash.go MUST NOT contain
	// network/filesystem calls that would signal a remote read.
	src, err := os.ReadFile(filepath.Join("registryhash.go"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, bad := range []string{
		"net.", "http.", "os.Open(", "os.ReadFile(", "ioutil.", "exec.Command",
	} {
		if strings.Contains(body, bad) {
			t.Errorf("registryhash.go must not contain %q — see §7 (j)", bad)
		}
	}
}

// TestAudit_PerfBench_ConditionalOnShortOrCI — spec §7 (h). The
// compute path runs unconditionally (so -race still exercises it) but
// the wall-clock threshold is only enforced without -short.
func TestAudit_PerfBench_ConditionalOnShortOrCI(t *testing.T) {
	const N = 1000
	names := make([]string, N)
	specs := make(map[string]string, N)
	for i := 0; i < N; i++ {
		names[i] = "tool_" + strings.Repeat("x", 4) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
		specs[names[i]] = strings.Repeat("s", 32)
	}
	desc := func(n string) string { return specs[n] }

	// Run once unconditionally — keeps the compute path in the -race net.
	_ = ComputeRegistryHash(names, desc)

	if testing.Short() {
		t.Skip("short mode; wall-clock threshold skipped per spec §7 (h)")
	}

	start := time.Now()
	_ = ComputeRegistryHash(names, desc)
	elapsed := time.Since(start)
	if elapsed > 5*time.Millisecond {
		t.Fatalf("ComputeRegistryHash on %d names took %v > 5ms", N, elapsed)
	}
}

// TestDriverConstructor_NeverProducesAPIKeyShapedThreadID — spec §7 (e).
// Asserts the driver-side thread-id constructor never emits an "sk-"
// prefix given a realistic set of inputs.
func TestDriverConstructor_NeverProducesAPIKeyShapedThreadID(t *testing.T) {
	inputs := []struct {
		codexThreadID string
		want          string // expected passes deriveDriverThreadID
	}{
		{"codex_thread_abcdefghij", ""},
		{"thread-01H8ABCDE", ""},
		{"driver-1", ""},
		{"", ""},
	}
	for _, tc := range inputs {
		got := DeriveDriverThreadID(tc.codexThreadID)
		if strings.HasPrefix(got, "sk-") {
			t.Errorf("DeriveDriverThreadID(%q) = %q — must not be sk- prefixed", tc.codexThreadID, got)
		}
	}
}
