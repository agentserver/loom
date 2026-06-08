package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPowerShellExecutorRejectsMissingScript(t *testing.T) {
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: t.TempDir(), Bin: "unused"})
	_, err := exec.Run(context.Background(), Task{Prompt: `{}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want missing script error")
	}
	if !strings.Contains(err.Error(), "powershell script is required") {
		t.Fatalf("error = %q, want missing script message", err.Error())
	}
}

func TestPowerShellCommandArgs(t *testing.T) {
	script := "Write-Output 'hello'"
	got := powerShellArgs(script)
	wantCommand := "& { Write-Output 'hello' }; if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }"
	want := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", wantCommand}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("powerShellArgs() = %#v, want %#v", got, want)
	}
}

func TestPowerShellExecutorRunsScriptWhenAvailable(t *testing.T) {
	bin := findPowerShellForTest(t)
	workdir := t.TempDir()
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: workdir, Bin: bin})
	prompt := mustJSON(t, PowerShellRequest{
		Script: `[Console]::Out.Write($PWD.Path + [Environment]::NewLine); [Console]::Out.Write($env:PS_TEST_VALUE + [Environment]::NewLine); [Console]::Error.Write("hello stderr" + [Environment]::NewLine)`,
		Env: map[string]string{
			"PS_TEST_VALUE": "hello stdout",
		},
		TimeoutSec: 5,
	})

	res, err := exec.Run(context.Background(), Task{
		ID:     "task-1",
		Skill:  "powershell",
		Prompt: prompt,
	}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got PowerShellResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary is not PowerShellResult JSON: %v\n%s", err, res.Summary)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0; result=%+v", got.ExitCode, got)
	}
	if normalizeNewlines(got.Stdout) != workdir+"\nhello stdout\n" {
		t.Fatalf("stdout = %q", got.Stdout)
	}
	if normalizeNewlines(got.Stderr) != "hello stderr\n" {
		t.Fatalf("stderr = %q", got.Stderr)
	}
	if got.WorkDir != workdir {
		t.Fatalf("workdir = %q, want %q", got.WorkDir, workdir)
	}
}

func TestPowerShellExecutorCreatesWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell helper is POSIX-only")
	}
	workdir := filepath.Join(t.TempDir(), "nested", "work")
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: workdir, Bin: fakePowerShellBin(t, `#!/bin/sh
if [ "$1" != "-NoProfile" ] || [ "$2" != "-ExecutionPolicy" ] || [ "$3" != "Bypass" ] || [ "$4" != "-Command" ]; then
	exit 64
fi
pwd
`)})
	res, err := exec.Run(context.Background(), Task{Prompt: `{"script":"ignored"}`}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got PowerShellResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stdout != workdir+"\n" {
		t.Fatalf("stdout = %q, want workdir output", got.Stdout)
	}
	if got.WorkDir != workdir {
		t.Fatalf("workdir = %q, want %q", got.WorkDir, workdir)
	}
}

func TestPowerShellExecutorPreservesNonZeroExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell helper is POSIX-only")
	}
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: t.TempDir(), Bin: fakePowerShellBin(t, `#!/bin/sh
exit 7
`)})
	res, err := exec.Run(context.Background(), Task{Prompt: `{"script":"ignored"}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want non-zero exit error")
	}
	if !strings.Contains(err.Error(), "powershell exit code 7") {
		t.Fatalf("error = %q, want exit code 7 message", err.Error())
	}
	var got PowerShellResult
	if jsonErr := json.Unmarshal([]byte(res.Summary), &got); jsonErr != nil {
		t.Fatalf("summary is not PowerShellResult JSON: %v\n%s", jsonErr, res.Summary)
	}
	if got.ExitCode != 7 {
		t.Fatalf("exit_code = %d, want 7; result=%+v", got.ExitCode, got)
	}
}

func TestPowerShellExecutorTimeoutReturnsStructuredSummary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell helper is POSIX-only")
	}
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: t.TempDir(), Bin: fakePowerShellBin(t, `#!/bin/sh
while :; do
	:
done
`)})
	res, err := exec.Run(context.Background(), Task{Prompt: `{"script":"ignored","timeout_sec":1}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want timeout error")
	}
	if !strings.Contains(err.Error(), "powershell timeout") {
		t.Fatalf("error = %q, want timeout message", err.Error())
	}
	var got PowerShellResult
	if jsonErr := json.Unmarshal([]byte(res.Summary), &got); jsonErr != nil {
		t.Fatalf("summary is not PowerShellResult JSON: %v\n%s", jsonErr, res.Summary)
	}
	if got.WorkDir == "" {
		t.Fatalf("workdir missing from result: %+v", got)
	}
}

func TestPowerShellExecutorReportsStartupError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-powershell")
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: t.TempDir(), Bin: missing})
	res, err := exec.Run(context.Background(), Task{Prompt: `{"script":"ignored"}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want startup error")
	}
	if !strings.Contains(err.Error(), "powershell start:") {
		t.Fatalf("error = %q, want startup diagnostic", err.Error())
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %q, want missing binary path %q", err.Error(), missing)
	}
	var got PowerShellResult
	if jsonErr := json.Unmarshal([]byte(res.Summary), &got); jsonErr != nil {
		t.Fatalf("summary is not PowerShellResult JSON: %v\n%s", jsonErr, res.Summary)
	}
}

func findPowerShellForTest(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path
		}
	}
	if runtime.GOOS == "windows" {
		path, err := exec.LookPath("powershell.exe")
		if err == nil {
			return path
		}
	}
	t.Skip("neither pwsh nor powershell exists")
	return ""
}

func fakePowerShellBin(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-powershell")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
