package main

import (
	"context"
	"errors"
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
	// Fresh-review P2: guard `-C` position BEFORE dereferencing lines[7]
	// as ws.Root. Without this, a future flag reorder that moves `-C`
	// away from position 6 would fail on the DeepEqual with a confusing
	// "prompt drift" diff instead of a clear "-C moved" message.
	if lines[6] != "-C" {
		t.Fatalf("-C expected at argv[6], got %q; argv=%q", lines[6], lines)
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
}

func TestSingleMachineCodex_PublicTerminalHeterogeneousDatesPrompt(t *testing.T) {
	prompt, ok := codexPrompts["public-terminal-heterogeneous-dates"]
	if !ok {
		t.Fatalf("codexPrompts missing public-terminal-heterogeneous-dates")
	}
	if !strings.Contains(prompt.Prompt, "daily_temp_sf_high.csv") {
		t.Errorf("prompt should name high-temperature CSV; prompt=%q", prompt.Prompt)
	}
	if !strings.Contains(prompt.Prompt, "daily_temp_sf_low.csv") {
		t.Errorf("prompt should name low-temperature CSV; prompt=%q", prompt.Prompt)
	}
	if !reflect.DeepEqual(prompt.ExpectedOutputs, []string{"avg_temp.txt"}) {
		t.Errorf("ExpectedOutputs = %v, want [avg_temp.txt]", prompt.ExpectedOutputs)
	}
}

func TestSingleMachineCodex_PublicTerminalHeterogeneousDatesFakeCodex(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	script := `#!/bin/sh
printf '11.428571428571429\n' > "$PWD/avg_temp.txt"
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "cat", "printf", "bash", "grep", "awk", "sed", "head", "tr", "python3"} {
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
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "public-terminal-heterogeneous-dates",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("public-terminal-heterogeneous-dates", false), io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("fake codex public task run: want exit 0, got %d; err=%v details=%s", res.ExitCode, res.Err, res.Row.OracleDetailsJSON)
	}
	if !res.Row.Passed {
		t.Errorf("oracle should pass on fake codex public task output; details=%s", res.Row.OracleDetailsJSON)
	}
}

// TestSingleMachineCodex_ScrubsStderr_NonzeroExit — spec P1#6 round-2.
// Fake `codex` emits `sk-abc123DEFabcDEFabcDEF` to stderr and exits 42.
// The returned error string MUST have the token replaced by [REDACTED]
// and MUST NOT contain the substrings `sk-abc123`, `abcDEF`.
func TestSingleMachineCodex_ScrubsStderr_NonzeroExit(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	// impl.go wraps secretscrub.Sanitize with a local Bearer pre-scrub
	// (spec §5: Bearer + sk-* + ghp_* are all stderr leak shapes that
	// MUST redact). Test all three separator styles for Bearer
	// (space / `=` / `:`) — fresh-review P1 fix on the earlier `\s+`
	// only regex.
	script := `#!/bin/sh
printf 'boot line 1\n' >&2
printf 'ERROR sk-abc123DEFabcDEFabcDEFabcDEF leak\n' >&2
printf 'ERROR ghp_abcDEFabcDEFabcDEFabcDEFabcDEFabcDEF leak\n' >&2
printf 'ERROR Bearer space_sep_secret_bar_baz_qux_1234567890 leak\n' >&2
printf 'ERROR Bearer=equals_sep_secret_bar_baz_qux_1234567890 leak\n' >&2
printf 'ERROR Bearer:colon_sep_secret_bar_baz_qux_1234567890 leak\n' >&2
exit 42
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
	_ = os.Symlink(fake, filepath.Join(pathDir, "codex"))
	t.Setenv("PATH", pathDir)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.ExitCode == 0 {
		t.Fatalf("expected nonzero exit; codex fake exits 42; res=%+v", res)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil res.Err on exit 42; res=%+v", res)
	}
	// BaselineRunRow has no stderr field; scrubbed subprocess stderr
	// surfaces only via the wrapped %w error.
	errStr := res.Err.Error()
	for _, banned := range []string{
		"sk-abc123", "ghp_abcDEF",
		"space_sep_secret", "equals_sep_secret", "colon_sep_secret",
	} {
		if strings.Contains(errStr, banned) {
			t.Errorf("scrub failed: substring %q leaked into error; err=%q", banned, errStr)
		}
	}
	if !strings.Contains(errStr, "[REDACTED]") {
		t.Errorf("expected [REDACTED] sentinel from secretscrub.Sanitize in err; err=%q", errStr)
	}
}

// TestSingleMachineCodex_ScrubsStderr_SuccessButOutputMissing — success
// path also runs stderr through the scrubber; a leak on stderr from a
// "successful" codex run that happens to omit expected outputs still
// gets scrubbed before it reaches the error string.
func TestSingleMachineCodex_ScrubsStderr_SuccessButOutputMissing(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	// Exit 0 (success) — but DELETE the mock_workspace-projected
	// expected outputs so the "did not produce" error path fires.
	// Emit token-shaped stderr during that success. Delete via bash
	// -c since the harness sets cwd=ws.Root.
	script := `#!/bin/sh
printf 'INFO sk-testonlytokenlong123456789 in log\n' >&2
rm -f "$PWD/patch.diff" "$PWD/test.log"
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	for _, tool := range []string{"sh", "cat", "printf", "bash", "ls", "rm"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("required tool %q missing from host PATH (test setup broken): %v", tool, err)
		}
		if err := os.Symlink(src, filepath.Join(pathDir, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	if err := os.Symlink(fake, filepath.Join(pathDir, "codex")); err != nil {
		t.Fatalf("symlink fake codex: %v", err)
	}
	t.Setenv("PATH", pathDir)
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod", false), io.Discard)
	if res.Err == nil {
		t.Fatalf("expected non-nil err (missing outputs), got nil; res=%+v", res)
	}
	haystack := res.Err.Error()
	if strings.Contains(haystack, "sk-testonlytokenlong") {
		t.Errorf("scrub failed on success-then-missing-output path: leak in error; err=%q", res.Err)
	}
	// Plan-review r1 P1: not-contains alone can pass if stderr never
	// made it into the error. Require [REDACTED] sentinel.
	if !strings.Contains(haystack, "[REDACTED]") {
		t.Errorf("success-then-missing-output path: expected [REDACTED] sentinel in err (proves scrub ran on stderr); err=%q", res.Err)
	}
}

// TestSingleMachineCodex_BuildsBinary — belt-and-braces build check.
func TestSingleMachineCodex_BuildsBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in -short")
	}
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	bin := filepath.Join(t.TempDir(), "single_machine_codex")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
}

// TestSingleMachineCodex_ForwardFlag_ControlsKeyPropagation — mirrors
// single_machine's ForwardFlag test but for OPENAI_API_KEY.
func TestSingleMachineCodex_ForwardFlag_ControlsKeyPropagation(t *testing.T) {
	parent := []string{"OPENAI_API_KEY=sk-oai-testonly-1234567890abcd", "PATH=/usr/bin"}

	noForward := NewImpl("cross-device-code-mod", false)
	env := harness.WhitelistEnvForAgent(parent, "cross-device-code-mod", noForward.AgentForwards(), nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENAI_API_KEY=") {
			t.Errorf("forward=false must drop OPENAI_API_KEY; env=%v", env)
		}
	}

	withForward := NewImpl("cross-device-code-mod", true)
	env2 := harness.WhitelistEnvForAgent(parent, "cross-device-code-mod", withForward.AgentForwards(), nil)
	found := false
	for _, kv := range env2 {
		if strings.HasPrefix(kv, "OPENAI_API_KEY=") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("forward=true must include OPENAI_API_KEY; env=%v", env2)
	}
}
