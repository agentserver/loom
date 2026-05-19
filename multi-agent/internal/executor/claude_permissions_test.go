package executor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestClaudePermissionsExecutorGetsAndPatches(t *testing.T) {
	workdir := t.TempDir()
	refreshCalled := false
	exec := NewClaudePermissionsExecutor(ClaudePermissionsConfig{
		WorkDir: workdir,
		Refresh: func(ctx context.Context, reason string) error {
			refreshCalled = reason == "claude permission update"
			return nil
		},
	})

	patchRes, err := exec.Run(context.Background(), Task{
		ID:     "task-1",
		Skill:  "claude_permissions",
		Prompt: `{"op":"patch","allow_add":["Bash(python3 *)","Bash(curl *)"],"deny_add":["Bash(rm *)"]}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if !refreshCalled {
		t.Fatal("refresh was not called")
	}
	var patched struct {
		Path  string   `json:"path"`
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal([]byte(patchRes.Summary), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Path != filepath.Join(workdir, ".claude", "settings.local.json") {
		t.Fatalf("path=%q", patched.Path)
	}

	getRes, err := exec.Run(context.Background(), Task{
		ID:     "task-2",
		Skill:  "claude_permissions",
		Prompt: `{"op":"get"}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	var got struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal([]byte(getRes.Summary), &got); err != nil {
		t.Fatal(err)
	}
	if !sameStrings(got.Allow, []string{"Bash(curl *)", "Bash(python3 *)"}) {
		t.Fatalf("allow=%q", got.Allow)
	}
	if !sameStrings(got.Deny, []string{"Bash(rm *)"}) {
		t.Fatalf("deny=%q", got.Deny)
	}
}

func TestClaudePermissionsExecutorRejectsInvalidOp(t *testing.T) {
	exec := NewClaudePermissionsExecutor(ClaudePermissionsConfig{WorkDir: t.TempDir()})
	_, err := exec.Run(context.Background(), Task{Prompt: `{"op":"delete"}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want invalid op error")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
