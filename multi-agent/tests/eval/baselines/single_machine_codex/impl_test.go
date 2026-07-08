package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
}

// TestSingleMachineCodex_DryRun_DoesNotInvokeCodex — in dry-run mode
// the impl must never call `codex`; the mock_workspace projection
// alone is what the oracle grades.
func TestSingleMachineCodex_DryRun_DoesNotInvokeCodex(t *testing.T) {
	// Poison PATH but keep oracle coreutils. Any accidental
	// exec.LookPath("codex") must fail without stripping the oracle.
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
	t.Setenv("PATH", fakeBin) // no `codex`
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

// TestSingleMachineCodex_RealMode_MissingBinary_FailsFast — without
// `codex` on PATH, real mode must emit ErrCodexCLIUnavailable via
// harness (exit 1 with row emitted — agent runtime failure).
func TestSingleMachineCodex_RealMode_MissingBinary_FailsFast(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `codex`, no coreutils either
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode != 1 {
		t.Fatalf("want exit 1 on missing codex, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !errors.Is(res.Err, ErrCodexCLIUnavailable) {
		t.Errorf("want ErrCodexCLIUnavailable, got %v", res.Err)
	}
}

// TestSingleMachineCodex_RealMode_UnknownWorkload_ReturnsSentinel —
// real mode with unknown workload id must return
// ErrSingleMachineCodexWorkloadUnknown.
func TestSingleMachineCodex_RealMode_UnknownWorkload_ReturnsSentinel(t *testing.T) {
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("no-such-workload", false), io.Discard)
	if !errors.Is(res.Err, ErrSingleMachineCodexWorkloadUnknown) {
		t.Errorf("want ErrSingleMachineCodexWorkloadUnknown, got %v", res.Err)
	}
}

// TestSingleMachineCodex_UsesPinnedArgv — spec-review P0#1. Real branch
// must invoke:
//
//	codex exec --sandbox workspace-write --ephemeral \
//	  --skip-git-repo-check --json -C <ws.Root> -- <prompt>
//
// Byte-compare argv against this pinned form via a fake `codex`
// script that logs its argv to a file. Non-self-referential:
// expected prompt is codexPrompts["cross-device-code-mod"].Prompt.
func TestSingleMachineCodex_UsesPinnedArgv(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fake := filepath.Join(dir, "codex")
	// Fake codex writes each argv element (excluding argv[0])
	// NUL-separated to LOOM_ARGV_FILE — the workload prompts contain
	// embedded newlines, so \n-separation would split each prompt
	// across many lines. It also produces the 2 expected outputs for
	// cross-device-code-mod under PWD (harness sets cwd to ws.Root)
	// so the output-existence check passes and the oracle runs.
	script := `#!/bin/sh
{ for a in "$@"; do printf '%s\0' "$a"; done } > "$LOOM_ARGV_FILE"
: > "$PWD/patch.diff"
printf 'PASS\n' > "$PWD/test.log"
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "sha256sum", "grep", "cmp", "wc", "awk", "sed", "cat", "printf", "bash", "head", "tr", "ls"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(pathDir, tool))
	}
	if err := os.Symlink(fake, filepath.Join(pathDir, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	// LOOM_-prefixed env var is preserved by harness allowlist.
	t.Setenv("LOOM_ARGV_FILE", argvFile)

	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	// We only care about argv, not oracle outcome. Accept:
	//   exit 0 = oracle passed
	//   exit 1 = oracle ran but row.Passed=false (test fixtures may
	//            not match this workload's real oracle)
	//   exit 3 = oracle errored during grading
	// Reject exit 2 (harness pre-flight / write-CSV failure) — that
	// would mean the pipeline didn't even reach ExecuteAgent.
	if res.ExitCode == 2 {
		t.Fatalf("exit 2 (harness preflight/write failure); want 0/1/3; err=%v", res.Err)
	}
	got, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv file: %v", err)
	}
	// NUL-separated (see fake codex script above). Trim the trailing
	// NUL that the last `printf '%s\0'` emits, then Split.
	lines := strings.Split(strings.TrimRight(string(got), "\x00"), "\x00")
	if len(lines) != 10 {
		t.Fatalf("argv length: want 10 got %d\nargv=%q", len(lines), lines)
	}
	wsRoot := lines[7]
	if !filepath.IsAbs(wsRoot) {
		t.Errorf("-C target must be absolute path; got %q", wsRoot)
	}
	expectedPrompt := codexPrompts["cross-device-code-mod"].Prompt
	expected := []string{
		"exec",
		"--sandbox", "workspace-write",
		"--ephemeral",
		"--skip-git-repo-check",
		"--json",
		"-C", wsRoot,
		"--",
		expectedPrompt,
	}
	if !reflect.DeepEqual(lines, expected) {
		t.Fatalf("argv drift:\n  got:  %q\n  want: %q", lines, expected)
	}
	_ = fmt.Sprintf
}
