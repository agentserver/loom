package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// helper: locate this repo's real workloads dir. Tests run from the
// package dir, so multi-agent/ is 3 levels up.
func workloadsDir(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// oracle_test.go → harness → baselines → eval → tests → multi-agent
	root := filepath.Join(filepath.Dir(self), "..", "..", "..", "..")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(abs, "tests", "eval", "workloads")
}

// TestRunOracle_HappyPath_CrossDeviceCodeMod — plan #18. Runs the real
// oracle from the cross-device-code-mod workload against the maintainer
// mock_workspace and confirms pass=true.
func TestRunOracle_HappyPath_CrossDeviceCodeMod(t *testing.T) {
	wl := filepath.Join(workloadsDir(t), "cross-device-code-mod")
	fixtures := filepath.Join(wl, "fixtures")
	ws, err := SetupWorkspace(fixtures, false)
	if err != nil {
		t.Fatalf("SetupWorkspace: %v", err)
	}
	defer ws.Cleanup()

	oracle := filepath.Join(wl, "oracle.sh")
	res, err := RunOracle(context.Background(), oracle, ws.Root, WhitelistEnvForOracle(os.Environ(), "cross-device-code-mod", nil), 30*time.Second)
	if err != nil {
		t.Fatalf("RunOracle: %v; stderr=%s", err, string(res.Stderr))
	}
	if !res.Passed {
		t.Errorf("expected passed=true; got details=%s stderr=%s", res.DetailsJSON, string(res.Stderr))
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit 0, got %d", res.ExitCode)
	}
	if res.DetailsJSON == "" || res.MetricsJSON == "" {
		t.Errorf("details/metrics should be present; got %q / %q", res.DetailsJSON, res.MetricsJSON)
	}
}

// TestRunOracle_RejectsOversizedStdout — plan #19. A hostile oracle that
// spews > 1 MiB must be killed and reported via ErrOracleOutputTooLarge.
func TestRunOracle_RejectsOversizedStdout(t *testing.T) {
	dir := t.TempDir()
	oracle := filepath.Join(dir, "oracle.sh")
	// Print ~2 MiB of `x`s.
	script := "#!/usr/bin/env bash\nhead -c 2097152 /dev/zero | tr '\\0' 'x'\n"
	if err := os.WriteFile(oracle, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := RunOracle(context.Background(), oracle, dir, os.Environ(), 10*time.Second)
	if !errors.Is(err, ErrOracleOutputTooLarge) {
		t.Fatalf("want ErrOracleOutputTooLarge, got %v", err)
	}
}

// TestRunOracle_MalformedFirstLine — plan #20. A non-JSON first line
// surfaces as ErrOracleStdoutNotJSON; the caller marks the row failed
// rather than aborting the whole run.
func TestRunOracle_MalformedFirstLine(t *testing.T) {
	dir := t.TempDir()
	oracle := filepath.Join(dir, "oracle.sh")
	script := "#!/usr/bin/env bash\necho not json\nexit 1\n"
	if err := os.WriteFile(oracle, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := RunOracle(context.Background(), oracle, dir, os.Environ(), 10*time.Second)
	if !errors.Is(err, ErrOracleStdoutNotJSON) {
		t.Fatalf("want ErrOracleStdoutNotJSON, got %v", err)
	}
	if res.ExitCode != 1 {
		t.Errorf("exit code lost; want 1 got %d", res.ExitCode)
	}
}

// TestRunOracle_RejectsExtraStdoutLine — plan #20b. 13 号 §1.3 forbids
// any second stdout line. Even a blank second line trips the guard.
// Two sub-cases: blank second line, and non-blank second line.
func TestRunOracle_RejectsExtraStdoutLine(t *testing.T) {
	t.Run("blank_second_line", func(t *testing.T) {
		dir := t.TempDir()
		oracle := filepath.Join(dir, "oracle.sh")
		script := "#!/usr/bin/env bash\nprintf '%s\\n\\n' '{\"passed\":true,\"details\":{},\"metrics\":{}}'\n"
		if err := os.WriteFile(oracle, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := RunOracle(context.Background(), oracle, dir, os.Environ(), 10*time.Second)
		if !errors.Is(err, ErrOracleStdoutHasExtraLines) {
			t.Fatalf("want ErrOracleStdoutHasExtraLines, got %v", err)
		}
		if res.Passed {
			// The extra-line rule overrides a valid Passed value —
			// caller must not report success on a contract-violating
			// oracle. RunOracle keeps res.Passed as parsed, but the
			// caller (harness.Run) forces it to false on this error.
			// The oracle_test.go layer just asserts the error surface;
			// harness_test.go can round-trip the full flip.
		}
	})
	t.Run("nonblank_second_line", func(t *testing.T) {
		dir := t.TempDir()
		oracle := filepath.Join(dir, "oracle.sh")
		script := "#!/usr/bin/env bash\nprintf '%s\\ndebug: extra line\\n' '{\"passed\":true,\"details\":{},\"metrics\":{}}'\n"
		if err := os.WriteFile(oracle, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := RunOracle(context.Background(), oracle, dir, os.Environ(), 10*time.Second)
		if !errors.Is(err, ErrOracleStdoutHasExtraLines) {
			t.Fatalf("want ErrOracleStdoutHasExtraLines, got %v", err)
		}
	})
	t.Run("single_line_with_terminator_is_fine", func(t *testing.T) {
		// A well-behaved oracle: one line + one '\n'. No error.
		dir := t.TempDir()
		oracle := filepath.Join(dir, "oracle.sh")
		script := "#!/usr/bin/env bash\nprintf '%s\\n' '{\"passed\":true,\"details\":{},\"metrics\":{}}'\n"
		if err := os.WriteFile(oracle, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := RunOracle(context.Background(), oracle, dir, os.Environ(), 10*time.Second)
		if err != nil {
			t.Fatalf("well-formed single-line oracle should succeed; got %v", err)
		}
		if !res.Passed {
			t.Errorf("expected passed=true")
		}
	})
}

// TestRunOracle_RejectsRelativeScript — safety valve: callers must
// pre-resolve the oracle path before RunOracle.
func TestRunOracle_RejectsRelativeScript(t *testing.T) {
	_, err := RunOracle(context.Background(), "oracle.sh", "/tmp", nil, time.Second)
	if err == nil {
		t.Fatal("expected error on relative oracle path")
	}
}
