package executor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

type noopSink struct{}

func (noopSink) Write(eventType, data string) {}
func (noopSink) Close()                       {}

func TestBashExecutorRunsScriptAndReturnsStructuredOutput(t *testing.T) {
	workdir := t.TempDir()
	exec := NewBashExecutor(BashConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		ID:     "task-1",
		Skill:  "bash",
		Prompt: `{"script":"pwd\nprintf 'hello stdout\\n'\nprintf 'hello stderr\\n' >&2","timeout_sec":5}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got BashResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary is not BashResult JSON: %v\n%s", err, res.Summary)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0; result=%+v", got.ExitCode, got)
	}
	if got.Stdout != workdir+"\nhello stdout\n" {
		t.Fatalf("stdout = %q", got.Stdout)
	}
	if got.Stderr != "hello stderr\n" {
		t.Fatalf("stderr = %q", got.Stderr)
	}
	if got.WorkDir != workdir {
		t.Fatalf("workdir = %q", got.WorkDir)
	}
}

func TestBashExecutorFailsOnNonZeroExitWithResult(t *testing.T) {
	exec := NewBashExecutor(BashConfig{WorkDir: t.TempDir()})
	res, err := exec.Run(context.Background(), Task{
		ID:     "task-1",
		Skill:  "bash",
		Prompt: `{"script":"echo before; echo bad >&2; exit 7","timeout_sec":5}`,
	}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want non-zero exit error")
	}
	var got BashResult
	if jsonErr := json.Unmarshal([]byte(res.Summary), &got); jsonErr != nil {
		t.Fatalf("summary is not BashResult JSON: %v\n%s", jsonErr, res.Summary)
	}
	if got.ExitCode != 7 || got.Stdout != "before\n" || got.Stderr != "bad\n" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestBashExecutorRejectsMissingScript(t *testing.T) {
	exec := NewBashExecutor(BashConfig{WorkDir: t.TempDir()})
	_, err := exec.Run(context.Background(), Task{Prompt: `{}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want missing script error")
	}
}

func TestBashExecutorCreatesWorkDir(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "nested", "work")
	exec := NewBashExecutor(BashConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{Prompt: `{"script":"pwd"}`}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got BashResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stdout != workdir+"\n" {
		t.Fatalf("stdout = %q, want pwd output", got.Stdout)
	}
}
