package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSubprocessEnv_WhitelistedOnly — Security §7(a). Parent has secret
// API-key style env; the subprocess sees only the whitelisted keys plus the
// runner's injected AGENTSERVER_URL.
func TestSubprocessEnv_WhitelistedOnly(t *testing.T) {
	t.Parallel()

	parent := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/test",
		"OPENAI_API_KEY=sk-real-12345",
		"ANTHROPIC_API_KEY=sk-ant-9999",
		"AWS_ACCESS_KEY_ID=AKIATEST",
		"AWS_SECRET_ACCESS_KEY=secret-zzz",
		"GITHUB_TOKEN=ghp_xxx",
		"LOOM_EVAL_GIT_EMAIL_CMD=echo a@b|c@d",
		"AGENTSERVER_ROOT=/root/agentserver",
		"RANDOM_OTHER_VAR=should-not-leak",
	}
	env := WhitelistEnv(parent, "cross-device-code-mod", map[string]string{
		"AGENTSERVER_URL": "http://127.0.0.1:18080",
	})

	got := strings.Join(env, "\n")
	for _, leak := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "RANDOM_OTHER_VAR",
	} {
		if strings.Contains(got, leak+"=") {
			t.Errorf("leaked %s into child env", leak)
		}
	}
	for _, want := range []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/test",
		"AGENTSERVER_URL=http://127.0.0.1:18080",
		"LOOM_EVAL_GIT_EMAIL_CMD=echo a@b|c@d",
		"AGENTSERVER_ROOT=/root/agentserver",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing expected %q in env, got:\n%s", want, got)
		}
	}
}

// TestSubprocessEnv_PerWorkloadAllowlist — the credential-bound-model
// workload is allowed EXPECTED_MODEL_ALIAS; others must NOT inherit it
// even if the parent has it set.
func TestSubprocessEnv_PerWorkloadAllowlist(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=/usr/bin",
		"EXPECTED_MODEL_ALIAS=acme-bound-model-v1",
	}
	allowed := WhitelistEnv(parent, "credential-bound-model", nil)
	if !contains(allowed, "EXPECTED_MODEL_ALIAS=acme-bound-model-v1") {
		t.Errorf("credential-bound-model: missing EXPECTED_MODEL_ALIAS, got %v", allowed)
	}
	denied := WhitelistEnv(parent, "cross-device-code-mod", nil)
	if contains(denied, "EXPECTED_MODEL_ALIAS=acme-bound-model-v1") {
		t.Errorf("cross-device-code-mod: leaked EXPECTED_MODEL_ALIAS, got %v", denied)
	}
}

// TestSubprocessEnv_InjectedOverridesParent — when both the parent env and
// the injected map carry the same key (e.g. AGENTSERVER_URL pointing at the
// fresh stub vs a stale parent value), the injected value wins; we don't
// want a parent's leftover AGENTSERVER_URL=http://prod.example.com to leak
// to the child.
func TestSubprocessEnv_InjectedOverridesParent(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=/x",
		// AGENTSERVER_URL is not on the always-allowed list; it gets in
		// only via injection. Add LOOM_AGENTSERVER_URL to the parent as a
		// LOOM_*-style stand-in for the priority test.
		"LOOM_FORCED=parent-value",
	}
	out := WhitelistEnv(parent, "x", map[string]string{
		"LOOM_FORCED": "injected-value",
	})
	got := strings.Join(out, "\n")
	if !strings.Contains(got, "LOOM_FORCED=injected-value") {
		t.Fatalf("injection did not win: %s", got)
	}
	if strings.Contains(got, "LOOM_FORCED=parent-value") {
		t.Fatalf("parent value not displaced: %s", got)
	}
}

// TestRunSubprocess_ExecutesScript_ReturnsStdout — sanity: a tiny shell
// script's stdout reaches the caller.
func TestRunSubprocess_ExecutesScript_ReturnsStdout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("bash unavailable")
	}
	script := writeScript(t, "#!/bin/sh\nprintf 'hello\\n'\n")
	res, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     []string{script},
		Timeout: 5 * time.Second,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit=%d, want 0; stderr=%s", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(string(res.Stdout)) != "hello" {
		t.Errorf("stdout=%q", res.Stdout)
	}
}

// TestOracleOutputTooLarge_Rejected — Security §7(f). A subprocess that
// emits more than MaxOracleStdoutBytes (1 MiB) before exiting is killed
// and ErrOracleOutputTooLarge surfaces. We use a 2 MiB stream of 'x'.
func TestOracleOutputTooLarge_Rejected(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("bash unavailable")
	}
	// `yes x` emits "x\n" indefinitely; head -c 2M cuts after 2 MiB.
	// shell stops as soon as head closes the pipe.
	script := writeScript(t, "#!/bin/sh\nyes x | head -c 2097152\n")
	_, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     []string{script},
		Timeout: 30 * time.Second,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if !errors.Is(err, ErrOracleOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOracleOutputTooLarge", err)
	}
}

// TestOracleOutputBelowCap_NotRejected — a script that emits ~10 KiB then
// exits clean is fine. This guards against an overzealous cap that trips
// on healthy oracles like cross-device-code-mod whose JSON line is well
// under 1 MiB.
func TestOracleOutputBelowCap_NotRejected(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("bash unavailable")
	}
	script := writeScript(t, "#!/bin/sh\nyes x | head -c 10240\n")
	res, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     []string{script},
		Timeout: 5 * time.Second,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit=%d, want 0", res.ExitCode)
	}
}

// TestSubprocessGroup_KilledOnTimeout — Security §7(a) lifecycle. The bash
// oracle starts a child `sleep 90`; we time the runner out at 2s; both bash
// and sleep must be dead before RunSubprocess returns.
func TestSubprocessGroup_KilledOnTimeout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("process-group kill semantics tested on Linux only")
	}
	// PIDs of bash + grandchild sleep are written to a temp file so the
	// test can poll /proc afterwards.
	pidFile := filepath.Join(t.TempDir(), "pids")
	script := writeScript(t, "#!/bin/sh\nsleep 90 &\nsleep_pid=$!\necho \"$$ $sleep_pid\" > "+pidFile+"\nwait\n")

	start := time.Now()
	_, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     []string{script},
		Timeout: 2 * time.Second,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrSubprocessTimeout) {
		t.Fatalf("err = %v, want ErrSubprocessTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout took %s, runner did not kill subprocess promptly", elapsed)
	}

	// Race: the kernel may not have reaped the kids by the instant Wait
	// returns; give it 200ms.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		pids := strings.Fields(strings.TrimSpace(string(raw)))
		if len(pids) != 2 {
			t.Fatalf("pidfile shape: %q", raw)
		}
		alive := false
		for _, p := range pids {
			if procExists(t, p) {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("zombie children of oracle survived timeout kill")
}

// TestRunSubprocess_ZeroTimeoutNoEnforcement — passing Timeout=0 means "no
// deadline"; we exercise this so future callers that intentionally want an
// untimed sub-step don't get surprise SIGKILLs.
func TestRunSubprocess_ZeroTimeoutNoEnforcement(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("bash unavailable")
	}
	script := writeScript(t, "#!/bin/sh\nsleep 0.3; echo done\n")
	res, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     []string{script},
		Timeout: 0,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(res.Stdout), "done") {
		t.Errorf("stdout missing 'done': %s", res.Stdout)
	}
}

// --- helpers ---

func writeScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func procExists(t *testing.T, pid string) bool {
	t.Helper()
	n, err := atoiPID(pid)
	if err != nil || n <= 0 {
		return false
	}
	// `kill -0` semantics via syscall.Kill — ESRCH if process is gone.
	err = syscall.Kill(n, 0)
	return err == nil
}

func atoiPID(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadPID
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errBadPID = errors.New("bad pid")
