package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

// TestManualSSH_DoesNotShellSSH — plan #23. Spec §7(c). Two layers:
//  1. grep over *.go for `"ssh"|"scp"|"rsync"|"sftp"` — nothing should
//     match. String-literal check catches an accidental `exec.Command("ssh"...)`.
//  2. The bashPath constant is literally "/bin/bash" — pin it.
func TestManualSSH_DoesNotShellSSH(t *testing.T) {
	if bashPath != "/bin/bash" {
		t.Errorf("bashPath drift: got %q, want /bin/bash (spec §7(c))", bashPath)
	}

	// Locate this baseline's Go source files. runtime.Caller resolves
	// against the actual filesystem so `go test` invoked from the
	// module root still finds them.
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Regexes that would indicate a shelled-out SSH-family tool.
	//   Each string literal (`"ssh"` etc.) is what an exec.Command call
	//   or os/exec.LookPath call would use.
	banned := regexp.MustCompile(`"(ssh|scp|rsync|sftp)"`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Skip *_test.go: this test itself contains the banned literal
		// as part of the pattern; only production sources are audited.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if m := banned.Find(body); m != nil {
			t.Errorf("%s contains banned SSH-family binary literal %q (spec §7(c))", e.Name(), string(m))
		}
	}
}

// TestManualSSH_HappyPath_CrossDeviceCodeMod — plan #24. Real-mode
// script writes the workload's outputs; oracle passes.
func TestManualSSH_HappyPath_CrossDeviceCodeMod(t *testing.T) {
	// Wire dependencies through the harness so this is a genuine
	// end-to-end exercise (Prepare → ExecuteAgent → oracle).
	_, self, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Join(filepath.Dir(self), "..", "..", "..", "..")
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot, "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false,
	}, NewImpl("cross-device-code-mod"), io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("real-mode run: want exit 0, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !res.Row.Passed {
		t.Errorf("oracle should have passed on manual_ssh output; details=%s", res.Row.OracleDetailsJSON)
	}
}

// TestManualSSH_DryRun_DegradesToMockWorkspace — plan #25. In dry-run,
// no bash script runs; harness mock_workspace projection is what the
// oracle grades.
func TestManualSSH_DryRun_DegradesToMockWorkspace(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Join(filepath.Dir(self), "..", "..", "..", "..")
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot, "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      true,
	}, NewImpl("cross-device-code-mod"), io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("dry-run: want exit 0, got %d; err=%v", res.ExitCode, res.Err)
	}
	if !res.Row.DryRun {
		t.Errorf("dry_run column should be true")
	}
}

// Ensure our impl compiles into a runnable binary. Not strictly a
// security test — belt-and-braces so `go build` failures surface here
// alongside the impl tests rather than only at the 15-run smoke matrix.
func TestManualSSH_BuildsBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in -short mode")
	}
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	bin := filepath.Join(t.TempDir(), "manual_ssh")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
}
