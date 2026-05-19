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
	"time"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/store"
	"gopkg.in/yaml.v3"
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

type fakeObserver struct {
	events []observer.Event
}

func (f *fakeObserver) Emit(ev observer.Event) {
	f.events = append(f.events, ev)
}

func observerEventOfType(events []observer.Event, eventType string) (observer.Event, bool) {
	for _, ev := range events {
		if ev.Type == eventType {
			return ev, true
		}
	}
	return observer.Event{}, false
}

func progressPayloads(t *testing.T, events []observer.Event) []map[string]interface{} {
	t.Helper()
	payloads := []map[string]interface{}{}
	for _, ev := range events {
		if ev.Type != observer.EventSlaveBuildMCPProgress {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("progress payload unmarshal: %v", err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func progressPhases(payloads []map[string]interface{}) []string {
	phases := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		phase, _ := payload["phase"].(string)
		phases = append(phases, phase)
	}
	return phases
}

func assertPhaseSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	next := 0
	for _, phase := range got {
		if next < len(want) && phase == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("progress phases = %+v, want subsequence %+v", got, want)
	}
}

func newBuildMCPForTest(t *testing.T) (*BuildMCPExecutor, string) {
	return newBuildMCPForTestWithObserver(t, nil)
}

func newBuildMCPForTestWithObserver(t *testing.T, obs Observer) (*BuildMCPExecutor, string) {
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
		Observer:  obs,
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
	var df DynamicFile
	if err := yaml.Unmarshal(dy, &df); err != nil {
		t.Fatalf("dynamic_mcp.yaml unmarshal: %v", err)
	}
	entry := df.Servers["foo"]
	if len(entry.Tools) != 1 {
		t.Fatalf("tools = %+v", entry.Tools)
	}
	tool := entry.Tools[0]
	if tool.Server != "foo" || tool.Name != "foo" || tool.Description != "d" || tool.ResultDescription != "r" {
		t.Fatalf("unexpected tool descriptor: %+v", tool)
	}
	if string(tool.InputSchema) != `{"type":"object"}` {
		t.Fatalf("input schema = %s", tool.InputSchema)
	}
	var handle handleJSON
	if err := json.Unmarshal([]byte(res.Summary), &handle); err != nil {
		t.Fatalf("summary unmarshal: %v", err)
	}
	if handle.Meta["tools"] != "foo" {
		t.Fatalf("success handle tools meta = %q", handle.Meta["tools"])
	}
}

func TestBuildMCP_EmitsObserverCreatedAfterPersist(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "ok")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	obs := &fakeObserver{}
	be, _ := newBuildMCPForTestWithObserver(t, obs)
	defer be.MCPExec.Close()

	spec := map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{
			{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"},
		},
		"allowed_packages": []string{},
		"version":          1,
		"iteration":        1,
		"max_iterations":   3,
	}
	specBytes, _ := json.Marshal(spec)

	_, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, ok := observerEventOfType(obs.events, observer.EventMCPServerCreated)
	if !ok {
		t.Fatalf("expected created event, got %+v", obs.events)
	}
	if ev.TaskID != "tx" || ev.MCPServerName != "foo" || ev.Status != "completed" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if len(ev.MCPTools) == 0 || ev.MCPTools[0] != "foo" {
		t.Fatalf("unexpected mcp tools: %+v", ev.MCPTools)
	}
	var payload struct {
		MCPToolDescriptors []capability.MCPToolDescriptor `json:"mcp_tool_descriptors"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if len(payload.MCPToolDescriptors) != 1 {
		t.Fatalf("payload descriptors: %+v", payload.MCPToolDescriptors)
	}
	tool := payload.MCPToolDescriptors[0]
	if tool.Server != "foo" || tool.Name != "foo" || tool.Description != "d" || string(tool.InputSchema) != `{"type":"object"}` {
		t.Fatalf("unexpected payload descriptor: %+v", tool)
	}
}

func TestBuildMCP_EmitsProgressEvents(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "ok")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	obs := &fakeObserver{}
	be, _ := newBuildMCPForTestWithObserver(t, obs)
	defer be.MCPExec.Close()

	spec := map[string]interface{}{
		"name": "foo", "description": "d",
		"tools":            []map[string]interface{}{{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"}},
		"allowed_packages": []string{}, "version": 1, "iteration": 1, "max_iterations": 3,
	}
	specBytes, _ := json.Marshal(spec)

	_, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	payloads := progressPayloads(t, obs.events)
	if len(payloads) == 0 {
		t.Fatalf("expected build progress events, got %+v", obs.events)
	}
	assertPhaseSubsequence(t, progressPhases(payloads), []string{
		"parse_spec",
		"generate",
		"validate",
		"smoke_launch",
		"register",
		"republish",
	})
	for _, payload := range payloads {
		if payload["message"] == "" || payload["name"] != "foo" || payload["is_final"] != false {
			t.Fatalf("unexpected progress payload: %+v", payload)
		}
	}
	if obs.events[len(obs.events)-1].Type != observer.EventMCPServerCreated {
		t.Fatalf("expected terminal created event last, got events %+v", obs.events)
	}
	for i, ev := range obs.events {
		if ev.Type == observer.EventMCPServerCreated {
			for _, later := range obs.events[i+1:] {
				if later.Type == observer.EventSlaveBuildMCPProgress {
					t.Fatalf("progress emitted after terminal success: events %+v", obs.events)
				}
			}
			return
		}
	}
	t.Fatalf("expected created event, got %+v", obs.events)
}

func TestBuildMCP_InvokeClaudeWaitsForCommandDrainAfterCancellation(t *testing.T) {
	be, _ := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	started := filepath.Join(t.TempDir(), "started")
	claude := filepath.Join(t.TempDir(), "fake-claude-drain.sh")
	script := `#!/usr/bin/env bash
set -euo pipefail
(sleep 0.25) &
: > "$FAKE_BUILD_CLAUDE_STARTED"
sleep 1
`
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	be.cfg.ClaudeBin = claude
	t.Setenv("FAKE_BUILD_CLAUDE_STARTED", started)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := be.invokeClaude(ctx, Task{ID: "tx"}, buildSpec{Name: "foo"}, "")
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake claude did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelAt := time.Now()
	cancel()
	err := <-done
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(cancelAt); elapsed < 200*time.Millisecond {
		t.Fatalf("invokeClaude returned before command stdout path drained: elapsed %s", elapsed)
	}
}

func TestBuildMCP_ReadDynamicYAMLConvertsOldToolNamesToDescriptors(t *testing.T) {
	be, work := newBuildMCPForTest(t)
	defer be.MCPExec.Close()
	oldYAML := []byte(`servers:
  legacy:
    transport: stdio
    command: python3
    args:
      - generated_mcp/legacy/v1.py
    version: 1
    tools:
      - echo
`)
	if err := os.WriteFile(filepath.Join(work, "dynamic_mcp.yaml"), oldYAML, 0o600); err != nil {
		t.Fatalf("write old yaml: %v", err)
	}

	df, err := ReadDynamicYAML(DynamicYAMLPath(work))

	if err != nil {
		t.Fatalf("ReadDynamicYAML: %v", err)
	}
	tools := df.Servers["legacy"].Tools
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Server != "legacy" || tools[0].Name != "echo" {
		t.Fatalf("converted tool = %+v", tools[0])
	}
}

func TestBuildMCP_ReusesExistingEntryWithLegacySpecHash(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "crash")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, work := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	specBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{
			{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"},
		},
		"hints":            "",
		"allowed_packages": []string{},
		"version":          1,
		"iteration":        1,
		"max_iterations":   3,
	})
	legacyHash := oldBuildMCPSpecHashForTest(t, string(specBytes))
	relPath := filepath.Join("generated_mcp", "foo", "v1.py")
	absPath := filepath.Join(work, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("mkdir generated mcp: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("print('existing')\n"), 0o600); err != nil {
		t.Fatalf("write generated mcp: %v", err)
	}
	dy := []byte(`servers:
  foo:
    transport: stdio
    command: python3
    args:
      - generated_mcp/foo/v1.py
    version: 1
    created_at: "2026-05-13T00:00:00Z"
    spec_hash: ` + legacyHash + `
    tools:
      - foo
`)
	if err := os.WriteFile(filepath.Join(work, "dynamic_mcp.yaml"), dy, 0o600); err != nil {
		t.Fatalf("write dynamic yaml: %v", err)
	}

	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Summary, `"type":"mcp_tool_set"`) {
		t.Fatalf("expected existing mcp_tool_set handle, got %q", res.Summary)
	}
	if strings.Contains(res.Summary, `"type":"build_mcp_blocked"`) {
		t.Fatalf("expected reuse to bypass Claude crash, got %q", res.Summary)
	}
}

func TestBuildMCP_ReusesExistingEntryWithLegacyRawHashAfterCanonicalPrompt(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "crash")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, work := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	rawSpecBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{
			{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"},
		},
		"allowed_packages": []string{},
	})
	legacyHash := oldBuildMCPSpecHashForTest(t, string(rawSpecBytes))
	canonicalPrompt := canonicalBuildSpecPromptForTest(t, string(rawSpecBytes))
	relPath := filepath.Join("generated_mcp", "foo", "v1.py")
	absPath := filepath.Join(work, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("mkdir generated mcp: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("print('existing')\n"), 0o600); err != nil {
		t.Fatalf("write generated mcp: %v", err)
	}
	dy := []byte(`servers:
  foo:
    transport: stdio
    command: python3
    args:
      - generated_mcp/foo/v1.py
    version: 1
    created_at: "2026-05-13T00:00:00Z"
    spec_hash: ` + legacyHash + `
    tools:
      - foo
`)
	if err := os.WriteFile(filepath.Join(work, "dynamic_mcp.yaml"), dy, 0o600); err != nil {
		t.Fatalf("write dynamic yaml: %v", err)
	}

	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: canonicalPrompt, TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Summary, `"type":"mcp_tool_set"`) {
		t.Fatalf("expected existing mcp_tool_set handle, got %q", res.Summary)
	}
	if strings.Contains(res.Summary, `"type":"build_mcp_blocked"`) {
		t.Fatalf("expected reuse to bypass Claude crash, got %q", res.Summary)
	}
}

func TestBuildMCP_ReusesExistingEntryWithLegacyUnsortedListHashAfterCanonicalPrompt(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "crash")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, work := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	rawSpecBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{
			{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"},
		},
		"hints":            "",
		"allowed_packages": []string{"zpkg", "apkg"},
		"compose_servers":  []string{"zserver", "aserver"},
		"version":          1,
		"iteration":        1,
		"max_iterations":   3,
	})
	legacyHash := oldBuildMCPSpecHashForTest(t, string(rawSpecBytes))
	canonicalPrompt := canonicalBuildSpecPromptForTest(t, string(rawSpecBytes))
	relPath := filepath.Join("generated_mcp", "foo", "v1.py")
	absPath := filepath.Join(work, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("mkdir generated mcp: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("print('existing')\n"), 0o600); err != nil {
		t.Fatalf("write generated mcp: %v", err)
	}
	dy := []byte(`servers:
  foo:
    transport: stdio
    command: python3
    args:
      - generated_mcp/foo/v1.py
    version: 1
    created_at: "2026-05-13T00:00:00Z"
    spec_hash: ` + legacyHash + `
    tools:
      - foo
`)
	if err := os.WriteFile(filepath.Join(work, "dynamic_mcp.yaml"), dy, 0o600); err != nil {
		t.Fatalf("write dynamic yaml: %v", err)
	}

	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: canonicalPrompt, TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Summary, `"type":"mcp_tool_set"`) {
		t.Fatalf("expected existing mcp_tool_set handle, got %q", res.Summary)
	}
	if strings.Contains(res.Summary, `"type":"build_mcp_blocked"`) {
		t.Fatalf("expected reuse to bypass Claude crash, got %q", res.Summary)
	}
}

func TestBuildMCP_ReusesExistingEntryWithLargeLegacyUnsortedListHashFromSystemContext(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "crash")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	be, work := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	rawSpecBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{
			{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"},
		},
		"hints": "",
		"allowed_packages": []string{
			"zpkg", "ypkg", "xpkg", "wpkg", "vpkg", "upkg", "apkg",
		},
		"compose_servers": []string{
			"zserver", "yserver", "xserver", "wserver", "vserver", "userver", "aserver",
		},
		"version":        1,
		"iteration":      1,
		"max_iterations": 3,
	})
	legacyHash := oldBuildMCPSpecHashForTest(t, string(rawSpecBytes))
	canonicalPrompt := canonicalBuildSpecPromptForTest(t, string(rawSpecBytes))
	relPath := filepath.Join("generated_mcp", "foo", "v1.py")
	absPath := filepath.Join(work, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("mkdir generated mcp: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("print('existing')\n"), 0o600); err != nil {
		t.Fatalf("write generated mcp: %v", err)
	}
	dy := []byte(`servers:
  foo:
    transport: stdio
    command: python3
    args:
      - generated_mcp/foo/v1.py
    version: 1
    created_at: "2026-05-13T00:00:00Z"
    spec_hash: ` + legacyHash + `
    tools:
      - foo
`)
	if err := os.WriteFile(filepath.Join(work, "dynamic_mcp.yaml"), dy, 0o600); err != nil {
		t.Fatalf("write dynamic yaml: %v", err)
	}

	systemContext := `{"build_mcp_legacy_spec_hashes":["` + legacyHash + `"]}`
	res, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: canonicalPrompt, SystemContext: systemContext, TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Summary, `"type":"mcp_tool_set"`) {
		t.Fatalf("expected existing mcp_tool_set handle, got %q", res.Summary)
	}
	if strings.Contains(res.Summary, `"type":"build_mcp_blocked"`) {
		t.Fatalf("expected reuse to bypass Claude crash, got %q", res.Summary)
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

func TestBuildMCP_EmitsObserverBlockedWithPayload(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "bad_import")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	obs := &fakeObserver{}
	be, _ := newBuildMCPForTestWithObserver(t, obs)
	defer be.MCPExec.Close()

	specBytes, _ := json.Marshal(map[string]interface{}{
		"name": "foo", "description": "d",
		"tools":            []map[string]interface{}{{"name": "x", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"}},
		"allowed_packages": []string{}, "version": 1, "iteration": 2, "max_iterations": 3,
	})

	_, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatal(err)
	}
	ev, ok := observerEventOfType(obs.events, observer.EventMCPServerBlocked)
	if !ok {
		t.Fatalf("expected blocked event, got %+v", obs.events)
	}
	if ev.TaskID != "tx" || ev.MCPServerName != "foo" || ev.Status != "blocked" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["stage"] != "validate_imports" || payload["needed_packages"] != "requests_html" || payload["iteration"].(float64) != 2 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if !strings.Contains(payload["reason"].(string), "not in allowed_packages") {
		t.Fatalf("unexpected reason: %+v", payload)
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

func TestBuildMCP_RejectsMalformedSpecBeforeExecution(t *testing.T) {
	be, _ := newBuildMCPForTest(t)
	defer be.MCPExec.Close()

	_, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: "please build a tool"}, &nopSink{})

	if err == nil {
		t.Fatal("expected malformed spec error")
	}
	if !strings.Contains(err.Error(), "buildmcp: malformed spec") {
		t.Fatalf("error = %v", err)
	}
}

type nopSink struct{}

func (*nopSink) Write(string, string) {}
func (*nopSink) Close()               {}

// Avoid unused-import warning in case store isn't otherwise used.
var _ = store.SubTaskRow{}

func oldBuildMCPSpecHashForTest(t *testing.T, raw string) string {
	t.Helper()
	var spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Tools       []struct {
			Name              string          `json:"name"`
			Description       string          `json:"description"`
			ArgsSchema        json.RawMessage `json:"args_schema"`
			ResultDescription string          `json:"result_description"`
		} `json:"tools"`
		Hints             string   `json:"hints"`
		AllowedPackages   []string `json:"allowed_packages"`
		ComposeServers    []string `json:"compose_servers"`
		Version           int      `json:"version"`
		PriorPath         string   `json:"prior_path"`
		PatchInstructions string   `json:"patch_instructions"`
		Iteration         int      `json:"iteration"`
		MaxIterations     int      `json:"max_iterations"`
	}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("unmarshal old build spec: %v", err)
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal old build spec: %v", err)
	}
	return computeSpecHashFromCanonical(string(b))
}

func canonicalBuildSpecPromptForTest(t *testing.T, raw string) string {
	t.Helper()
	spec, err := buildspec.ParseJSON(raw)
	if err != nil {
		t.Fatalf("parse build spec: %v", err)
	}
	canonical, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		t.Fatalf("marshal canonical build spec: %v", err)
	}
	return canonical
}
