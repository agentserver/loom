package driver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// D1: With no hook installed, InjectIfActive returns nil for every
// declared HookPoint. Production binaries take this fast path forever.
func TestFaultHook_NoopByDefault(t *testing.T) {
	// Defensive: ensure we start with a clean slate (other tests in this
	// package may install + restore).
	prev := SetHook(nil)
	defer SetHook(prev)
	for _, hp := range []HookPoint{
		HookPointDriverPickup,
		HookPointDriverCapabilityRead,
		HookPointDriverCredResolve,
		HookPointDriverModelRoute,
		HookPointDriverMainLoop,
		HookPointSlaveHeartbeat,
	} {
		if err := InjectIfActive(context.Background(), "run-noopdef", hp, nil); err != nil {
			t.Errorf("InjectIfActive(%q) = %v, want nil (noop)", hp, err)
		}
	}
}

// D2: SetHook returns the previously installed hook; restoring nil
// returns to the noop fast path.
func TestFaultHook_SetHookReturnsPrevious(t *testing.T) {
	prev := SetHook(nil) // baseline
	defer SetHook(prev)

	want := errors.New("hook fired")
	h1 := Hook(func(_ context.Context, _ string, _ HookPoint, _ map[string]string) error { return want })
	if old := SetHook(h1); old != nil {
		t.Errorf("SetHook(h1) returned %v, want nil (start state)", old)
	}
	// Install h2; expect h1 returned.
	h2Called := false
	h2 := Hook(func(_ context.Context, _ string, _ HookPoint, _ map[string]string) error {
		h2Called = true
		return nil
	})
	got := SetHook(h2)
	// We cannot compare function values directly; verify by invoking.
	if got == nil {
		t.Fatalf("SetHook(h2) returned nil, want h1")
	}
	if err := got(context.Background(), "", HookPointDriverPickup, nil); !errors.Is(err, want) {
		t.Errorf("returned previous hook did not behave like h1: err=%v", err)
	}
	// Active hook is now h2.
	_ = InjectIfActive(context.Background(), "run-sethook01", HookPointDriverPickup, nil)
	if !h2Called {
		t.Errorf("after SetHook(h2), InjectIfActive did not call h2")
	}
	// Restore nil and ensure noop.
	_ = SetHook(nil)
	if err := InjectIfActive(context.Background(), "run-sethook02", HookPointDriverPickup, nil); err != nil {
		t.Errorf("after SetHook(nil): InjectIfActive = %v, want nil", err)
	}
}

// D3: Hook errors propagate verbatim through InjectIfActive.
func TestFaultHook_HookErrorPropagates(t *testing.T) {
	prev := SetHook(nil)
	defer SetHook(prev)

	want := errors.New("synthetic fault")
	SetHook(func(_ context.Context, runID string, hp HookPoint, _ map[string]string) error {
		if runID == "run-propagate" && hp == HookPointDriverModelRoute {
			return want
		}
		return nil
	})
	got := InjectIfActive(context.Background(), "run-propagate", HookPointDriverModelRoute, nil)
	if !errors.Is(got, want) {
		t.Fatalf("InjectIfActive err = %v, want %v", got, want)
	}
	// Other (runID, HookPoint) combinations: noop returns nil.
	if err := InjectIfActive(context.Background(), "run-other001", HookPointDriverPickup, nil); err != nil {
		t.Errorf("other combo err = %v, want nil", err)
	}
}

// D4: Every declared HookPoint constant is non-empty and unique within
// the package.
func TestFaultHook_HookPointsEnumerated(t *testing.T) {
	want := []HookPoint{
		HookPointDriverPickup,
		HookPointDriverCapabilityRead,
		HookPointDriverCredResolve,
		HookPointDriverModelRoute,
		HookPointDriverMainLoop,
		HookPointSlaveHeartbeat,
	}
	seen := make(map[HookPoint]bool, len(want))
	for _, hp := range want {
		if string(hp) == "" {
			t.Errorf("HookPoint constant is empty")
		}
		if seen[hp] {
			t.Errorf("HookPoint %q declared more than once in test list", hp)
		}
		seen[hp] = true
	}
}

// D5: The production internal/driver package does not depend on
// tools/eval/faultinject. `go list -deps` over the package must not
// list the faultinject path.
func TestFaultHook_NoFaultInjectImport(t *testing.T) {
	// Run from module root so the package path is unambiguous.
	cmd := exec.Command("go", "list", "-deps", "github.com/yourorg/multi-agent/internal/driver")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "tools/eval/faultinject") {
			t.Errorf("internal/driver depends on faultinject:\n%s", line)
		}
	}
}

// D6: Fast-path benchmark. The noop path must complete in under 100ns/op
// on the dev workstation (spec §6.1 perf gate).
//
// Skipped on GitHub-hosted CI runners: their shared burstable CPUs cannot
// sustainably beat 100 ns/op even for a trivial atomic-load path, and
// the `-race` build (used in CI) multiplies per-op cost by ~5x. The perf
// gate's purpose — catch regressions from a real dev workstation — is
// preserved locally; CI's job is coverage + correctness, not perf.
func TestBench_FastPathUnder100ns(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "" {
		t.Skip("perf gate is dev-workstation only; CI runners can't sustain 100 ns/op under -race")
	}
	prev := SetHook(nil)
	defer SetHook(prev)
	res := testing.Benchmark(BenchmarkFaultHook_NoopFastPath)
	t.Logf("BenchmarkFaultHook_NoopFastPath: %s", res.String())
	const limitNs = 100
	if res.NsPerOp() >= limitNs {
		t.Fatalf("noop fast path %d ns/op exceeds limit %d ns/op", res.NsPerOp(), limitNs)
	}
}

// Benchmark exported so TestBench_FastPathUnder100ns can call it via
// testing.Benchmark, and so the canonical `go test -bench` invocation
// from plan §5 picks it up.
func BenchmarkFaultHook_NoopFastPath(b *testing.B) {
	prev := SetHook(nil)
	defer SetHook(prev)
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		_ = InjectIfActive(ctx, "run-benchmark01", HookPointDriverPickup, nil)
	}
}

// moduleRoot walks upward from this test's package directory looking
// for go.mod so `go list` runs in the right module. Uses filepath.Dir
// so `GOMOD` on Windows (backslash separators) resolves correctly —
// `strings.TrimSuffix(mod, "/go.mod")` didn't match `\go.mod` and
// yielded a malformed cwd that broke CI's windows job.
func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == "/dev/null" || mod == "NUL" {
		t.Fatalf("no go.mod found from test cwd")
	}
	return filepath.Dir(mod)
}
