package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/store"
)

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file is .../multi-agent/internal/executor/buildmcp_test.go
	// root is two dirs up from internal/executor/
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../.."))
	if err != nil {
		t.Fatalf("projectRoot: %v", err)
	}
	return root
}

func fakeBuildClaude(t *testing.T) string {
	t.Helper()
	root := projectRoot(t)
	return filepath.Join(root, "testdata", "fake-build-claude.sh")
}

func newBuildMCPForTest(t *testing.T) (*BuildMCPExecutor, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	cardRepub := func(ctx context.Context) error { return nil }
	be := NewBuildMCPExecutor(BuildMCPConfig{
		WorkDir:   work,
		ClaudeBin: fakeBuildClaude(t),
		MCPExec:   mcpExec,
		Republish: cardRepub,
	})
	return be, work
}

func TestBuildMCP_HappyPath(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "ok")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, work := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	spec := map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{
			{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"},
		},
		"hints":            "",
		"allowed_packages": []string{},
		"version":          1,
		"iteration":        1,
		"max_iterations":   3,
	}
	specBytes, _ := json.Marshal(spec)
	sink := &nopSink{}
	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Summary, `"type":"mcp_tool_set"`) {
		t.Fatalf("expected mcp_tool_set handle, got %q", res.Summary)
	}
	src, err := os.ReadFile(filepath.Join(work, "generated_mcp", "foo", "v1.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(src), "# -*- coding: utf-8 -*-\n# AUTO-GENERATED") {
		t.Fatalf("missing header:\n%s", string(src[:80]))
	}
	dy, _ := os.ReadFile(filepath.Join(work, "dynamic_mcp.yaml"))
	if !strings.Contains(string(dy), "foo:") {
		t.Fatalf("dynamic_mcp.yaml missing entry:\n%s", string(dy))
	}
}

func TestBuildMCP_BadImport_ReturnsBlocked(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "bad_import")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, _ := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	specBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools":            []map[string]interface{}{{"name": "x", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"}},
		"allowed_packages": []string{}, "version": 1, "iteration": 1, "max_iterations": 3,
	})
	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, `"type":"build_mcp_blocked"`) {
		t.Fatalf("expected build_mcp_blocked, got %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "requests_html") {
		t.Fatalf("expected blocked handle to mention requests_html, got %q", res.Summary)
	}
}

func TestBuildMCP_BadSyntax_ReturnsBlocked(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "bad_syntax")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, _ := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	specBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools":            []map[string]interface{}{{"name": "x", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"}},
		"allowed_packages": []string{}, "version": 1, "iteration": 1, "max_iterations": 3,
	})
	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, `"type":"build_mcp_blocked"`) || !strings.Contains(res.Summary, "validate_syntax") {
		t.Fatalf("unexpected: %q", res.Summary)
	}
}

func TestBuildMCP_MalformedSpec_ReturnsErr(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "ok")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, _ := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	_, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: "not-json"}, &nopSink{})
	if err == nil {
		t.Fatal("expected error for malformed spec")
	}
}

type nopSink struct{}

func (*nopSink) Write(string, string) {}
func (*nopSink) Close()               {}

// Avoid unused-import warning in case store isn't otherwise used.
var _ = store.SubTaskRow{}
