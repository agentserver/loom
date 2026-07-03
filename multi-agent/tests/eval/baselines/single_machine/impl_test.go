package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
}

// TestSingleMachine_DryRun_DoesNotInvokeClaude — plan #26. In dry-run
// mode the impl must never call `claude`; the mock_workspace projection
// alone is what the oracle grades.
func TestSingleMachine_DryRun_DoesNotInvokeClaude(t *testing.T) {
	// Poison PATH but keep the oracle's dependencies (sha256sum, grep,
	// cmp, wc, awk, sed) available: symlink coreutils dirs so any
	// accidental `exec.LookPath("claude")` still fails without stripping
	// the oracle of its POSIX toolbox.
	fakeBin := t.TempDir()
	for _, tool := range []string{"sha256sum", "grep", "cmp", "wc", "awk", "sed", "cat", "printf", "bash", "sh", "head", "tr"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(src, filepath.Join(fakeBin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fakeBin) // no `claude`
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      true,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("dry-run: want exit 0, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !res.Row.DryRun {
		t.Errorf("dry_run column should be true")
	}
}

// TestSingleMachine_RealMode_MissingClaude_FailsFast — plan #27.
// Without `claude` on PATH, real mode must emit ErrClaudeCLIUnavailable
// via harness (exit 1 with row emitted — agent runtime failure).
func TestSingleMachine_RealMode_MissingClaude_FailsFast(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `claude`, no coreutils either
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	// Exit 1 (ExecuteAgent runtime failure with partial row): impl
	// wraps ErrClaudeCLIUnavailable inside a %w chain; unwrap check.
	if res.ExitCode != 1 {
		t.Fatalf("want exit 1 on missing claude, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !errors.Is(res.Err, ErrClaudeCLIUnavailable) {
		t.Errorf("want ErrClaudeCLIUnavailable, got %v", res.Err)
	}
}

// TestSingleMachine_ForwardFlag_ControlsKeyPropagation — plan #27b.
// AgentForwards() gates ANTHROPIC_API_KEY propagation via harness's
// WhitelistEnvForAgent; validate both branches without invoking claude.
func TestSingleMachine_ForwardFlag_ControlsKeyPropagation(t *testing.T) {
	parent := []string{"ANTHROPIC_API_KEY=sk-ant-testonly-12345678901234567890", "PATH=/usr/bin"}

	noForward := NewImpl("cross-device-code-mod", false)
	env := harness.WhitelistEnvForAgent(parent, "cross-device-code-mod", noForward.AgentForwards(), nil)
	for _, kv := range env {
		if len(kv) > len("ANTHROPIC_API_KEY=") && kv[:len("ANTHROPIC_API_KEY=")] == "ANTHROPIC_API_KEY=" {
			t.Errorf("forward=false must drop ANTHROPIC_API_KEY; env=%v", env)
		}
	}

	withForward := NewImpl("cross-device-code-mod", true)
	env2 := harness.WhitelistEnvForAgent(parent, "cross-device-code-mod", withForward.AgentForwards(), nil)
	found := false
	for _, kv := range env2 {
		if len(kv) > len("ANTHROPIC_API_KEY=") && kv[:len("ANTHROPIC_API_KEY=")] == "ANTHROPIC_API_KEY=" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("forward=true must include ANTHROPIC_API_KEY; env=%v", env2)
	}
}

// TestSingleMachine_BuildsBinary — belt-and-braces build check.
func TestSingleMachine_BuildsBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in -short")
	}
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	bin := filepath.Join(t.TempDir(), "single_machine")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
}
