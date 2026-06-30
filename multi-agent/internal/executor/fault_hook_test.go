package executor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// D1 (executor mirror): default noop returns nil for every HookPoint.
func TestFaultHook_NoopByDefault(t *testing.T) {
	prev := SetHook(nil)
	defer SetHook(prev)
	for _, hp := range []HookPoint{HookPointExecutorFileOpen, HookPointExecutorCredResolve} {
		if err := InjectIfActive(context.Background(), "run-noopexec", hp, nil); err != nil {
			t.Errorf("InjectIfActive(%q) = %v, want nil (noop)", hp, err)
		}
	}
}

// D2 (executor mirror): SetHook returns previous; nil restores noop.
func TestFaultHook_SetHookReturnsPrevious(t *testing.T) {
	prev := SetHook(nil)
	defer SetHook(prev)

	want := errors.New("hook fired")
	h1 := Hook(func(_ context.Context, _ string, _ HookPoint, _ map[string]string) error { return want })
	if old := SetHook(h1); old != nil {
		t.Errorf("SetHook(h1) returned %v, want nil", old)
	}
	called := false
	h2 := Hook(func(_ context.Context, _ string, _ HookPoint, _ map[string]string) error {
		called = true
		return nil
	})
	got := SetHook(h2)
	if got == nil {
		t.Fatalf("SetHook(h2) returned nil, want h1")
	}
	if err := got(context.Background(), "", HookPointExecutorFileOpen, nil); !errors.Is(err, want) {
		t.Errorf("returned previous hook did not behave like h1: err=%v", err)
	}
	_ = InjectIfActive(context.Background(), "run-execset01", HookPointExecutorFileOpen, nil)
	if !called {
		t.Errorf("after SetHook(h2), InjectIfActive did not call h2")
	}
	_ = SetHook(nil)
	if err := InjectIfActive(context.Background(), "run-execset02", HookPointExecutorFileOpen, nil); err != nil {
		t.Errorf("after SetHook(nil): InjectIfActive = %v, want nil", err)
	}
}

// D3 (executor mirror): hook errors propagate.
func TestFaultHook_HookErrorPropagates(t *testing.T) {
	prev := SetHook(nil)
	defer SetHook(prev)
	want := errors.New("synthetic exec fault")
	SetHook(func(_ context.Context, runID string, hp HookPoint, _ map[string]string) error {
		if runID == "run-execprop" && hp == HookPointExecutorCredResolve {
			return want
		}
		return nil
	})
	got := InjectIfActive(context.Background(), "run-execprop", HookPointExecutorCredResolve, nil)
	if !errors.Is(got, want) {
		t.Fatalf("InjectIfActive err = %v, want %v", got, want)
	}
}

// D4 (executor mirror): all HookPoints non-empty and unique.
func TestFaultHook_HookPointsEnumerated(t *testing.T) {
	want := []HookPoint{HookPointExecutorFileOpen, HookPointExecutorCredResolve}
	seen := make(map[HookPoint]bool, len(want))
	for _, hp := range want {
		if string(hp) == "" {
			t.Errorf("HookPoint constant is empty")
		}
		if seen[hp] {
			t.Errorf("HookPoint %q duplicate", hp)
		}
		seen[hp] = true
	}
}

// D5 (executor mirror): internal/executor does not depend on faultinject.
func TestFaultHook_NoFaultInjectImport(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/yourorg/multi-agent/internal/executor")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "tools/eval/faultinject") {
			t.Errorf("internal/executor depends on faultinject:\n%s", line)
		}
	}
}

// D6 (executor mirror): bench under 100 ns/op.
func TestBench_FastPathUnder100ns(t *testing.T) {
	prev := SetHook(nil)
	defer SetHook(prev)
	res := testing.Benchmark(BenchmarkFaultHook_NoopFastPath)
	t.Logf("BenchmarkFaultHook_NoopFastPath: %s", res.String())
	const limitNs = 100
	if res.NsPerOp() >= limitNs {
		t.Fatalf("noop fast path %d ns/op exceeds limit %d ns/op", res.NsPerOp(), limitNs)
	}
}

func BenchmarkFaultHook_NoopFastPath(b *testing.B) {
	prev := SetHook(nil)
	defer SetHook(prev)
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		_ = InjectIfActive(ctx, "run-benchexec01", HookPointExecutorFileOpen, nil)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == "/dev/null" {
		t.Fatalf("no go.mod found")
	}
	return strings.TrimSuffix(mod, "/go.mod")
}
