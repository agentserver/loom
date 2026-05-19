# Bash-Driven MCP Registration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the slave's one-shot `build_mcp` skill (Claude generates Python in a single call, only structural validation) with a thin native `register_mcp` skill that takes a pre-written Python file and a spec, validates/smokes/registers it. Generation moves to the bash skill, where the driver Claude can iterate against real test cases and reuse existing slave services.

**Architecture:**
- New native slave executor `register_mcp` (Go, not Claude): inputs `{spec_json, source_path}` → AST + import allowlist check → smoke-launch `tools/list` → `RegisterStdio` + `dynamic_mcp.yaml` upsert + capability card republish. Same persistence infra as today, generation step removed.
- Driver Claude workflow: `bash` → write `generated_mcp/<name>/v1.py`, optionally run real test cases, then `register_mcp` to publish it.
- Old `build_mcp` skill, executor, planner emission, orchestrator preflight/replan, `allow_build_mcp` contract field, and `BuildMCPExecutor` are all removed. No backward compatibility shims.

**Tech Stack:** Go 1.26, existing `internal/executor` patterns (`bash.go`, `mcp.go`, `claude_permissions.go`), existing `internal/buildspec` types for the spec input, Python 3 for the smoke-launched servers.

---

## File Structure

**Create:**
- `multi-agent/internal/executor/dynamicmcp.go` — package-level helpers extracted from `buildmcp.go`: `DynamicEntry` struct, `ReadDynamicYAML(path)`, `UpsertDynamicYAML(path, entry)`, `LookupDynamicEntry(path, name)`. Removes the methods-on-`BuildMCPExecutor` indirection so the new executor can reuse them without depending on `BuildMCPExecutor`.
- `multi-agent/internal/executor/registermcp.go` — `RegisterMCPExecutor`: the new native skill. ~150 lines.
- `multi-agent/internal/executor/registermcp_test.go` — unit tests: happy path, bad spec, bad source (syntax/import), smoke launch failure, idempotent re-register, observer events.
- `multi-agent/internal/driver/register_mcp_tool.go` — `register_slave_mcp` MCP tool: thin wrapper that does `submit_task` to a slave's `register_mcp` skill (mirrors `run_slave_bash`).
- `multi-agent/internal/driver/register_mcp_tool_test.go` — tests.

**Modify:**
- `multi-agent/cmd/slave-agent/main.go:144-156` — wire `register_mcp` route, remove `build_mcp` wiring.
- `multi-agent/cmd/slave-agent/config.example.yaml:29-34` — advertise `register_mcp` (default-on); remove `build_mcp` comment.
- `multi-agent/cmd/slave-agent/e2e_test.go:80` — replace `build_mcp` with `register_mcp` in test skill list.
- `multi-agent/internal/contract/types.go` — remove `AllowBuildMCP` field from `ExecutionPolicy`.
- `multi-agent/internal/contract/validate.go:79` — remove the `build_mcp` skill string-check.
- `multi-agent/internal/orchestrator/contract_policy.go:35-42, 67-74` — remove the `build_mcp` policy gates.
- `multi-agent/internal/orchestrator/fanout.go:518-535, 670-720` — remove the `build_mcp_blocked` replan branch and `iterCount` tracking.
- `multi-agent/internal/orchestration/plan_semantics.go:50-60` — remove `build_mcp` node kind handling.
- `multi-agent/internal/planner/prompts.go:31, 40-49` — remove `build_mcp` emission instructions.
- `multi-agent/internal/driver/capability_tools.go:82, 419` — remove `allow_build_mcp` schema field and `candidateAgentsWithSkill(agents, "build_mcp", ...)` block.
- `multi-agent/internal/driver/contract_tools.go` — remove any `AllowBuildMCP`/`build_mcp` references surfaced by the type removal.
- `multi-agent/internal/driver/tools.go` — register `register_slave_mcp` MCP tool.
- `skills/multiagent/SKILL.md:21, 30, 44` — remove `allow_build_mcp` lines; add `register_mcp` to the route guidance.
- `skills/multiagent/references/slave-skills.md:7, 35-74` — replace the entire `build_mcp` section with a `register_mcp` section + the bash-driven workflow template.
- `skills/multiagent/references/task-contract.md:38, 80, 96-104` — remove `allow_build_mcp` references and the "Build MCP Policy" section.
- `skills/multiagent/references/driver-tools.md:55-65, 174-219` — drop `allow_build_mcp` from `draft_task_contract` schema; add `register_slave_mcp` to "Slave Control Helpers".

**Delete:**
- `multi-agent/internal/executor/buildmcp.go` — entire file.
- `multi-agent/internal/executor/buildmcp_test.go` — entire file.
- `multi-agent/internal/orchestrator/buildmcp_preflight.go` — entire file.
- `multi-agent/internal/orchestrator/buildmcp_preflight_test.go` — entire file.

---

### Task 1: Extract dynamic_mcp.yaml helpers to package-level

Pure refactor. `dynamicEntry`, `dynamicFile`, and the methods `yamlPath`, `lookupExistingEntry`, `readDynamicYAML`, `upsertDynamicYAML` currently live on `*BuildMCPExecutor` in `internal/executor/buildmcp.go:622-729`. Move them to a new file as package-level functions so they outlive `BuildMCPExecutor`.

**Files:**
- Create: `multi-agent/internal/executor/dynamicmcp.go`
- Modify: `multi-agent/internal/executor/buildmcp.go` (replace method calls with package-level calls; do not change behavior)

- [ ] **Step 1: Create the new file with the moved helpers**

```go
// multi-agent/internal/executor/dynamicmcp.go
package executor

import (
	"os"
	"path/filepath"

	"github.com/yourorg/multi-agent/internal/capability"
	"gopkg.in/yaml.v3"
)

type DynamicEntry struct {
	Name      string                         `yaml:"-"`
	Transport string                         `yaml:"transport"`
	Command   string                         `yaml:"command"`
	Args      []string                       `yaml:"args"`
	Version   int                            `yaml:"version"`
	CreatedAt string                         `yaml:"created_at"`
	SpecHash  string                         `yaml:"spec_hash"`
	Tools     []capability.MCPToolDescriptor `yaml:"tools,omitempty"`
}

func (d *DynamicEntry) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Transport string    `yaml:"transport"`
		Command   string    `yaml:"command"`
		Args      []string  `yaml:"args"`
		Version   int       `yaml:"version"`
		CreatedAt string    `yaml:"created_at"`
		SpecHash  string    `yaml:"spec_hash"`
		Tools     yaml.Node `yaml:"tools"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	d.Transport = raw.Transport
	d.Command = raw.Command
	d.Args = raw.Args
	d.Version = raw.Version
	d.CreatedAt = raw.CreatedAt
	d.SpecHash = raw.SpecHash
	if raw.Tools.Kind == 0 {
		return nil
	}
	var descriptors []capability.MCPToolDescriptor
	if err := raw.Tools.Decode(&descriptors); err == nil {
		d.Tools = descriptors
		return nil
	}
	var names []string
	if err := raw.Tools.Decode(&names); err != nil {
		return err
	}
	d.Tools = make([]capability.MCPToolDescriptor, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		d.Tools = append(d.Tools, capability.MCPToolDescriptor{Name: name})
	}
	return nil
}

type DynamicFile struct {
	Servers map[string]DynamicEntry `yaml:"servers"`
}

func DynamicYAMLPath(workDir string) string {
	return filepath.Join(workDir, "dynamic_mcp.yaml")
}

func ReadDynamicYAML(path string) (DynamicFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DynamicFile{Servers: map[string]DynamicEntry{}}, nil
		}
		return DynamicFile{}, err
	}
	var df DynamicFile
	if err := yaml.Unmarshal(b, &df); err != nil {
		return DynamicFile{}, err
	}
	if df.Servers == nil {
		df.Servers = map[string]DynamicEntry{}
	}
	for name, entry := range df.Servers {
		entry.Name = name
		entry.Tools = capability.WithServer(name, entry.Tools)
		df.Servers[name] = entry
	}
	return df, nil
}

func LookupDynamicEntry(path, name string) (DynamicEntry, bool) {
	df, err := ReadDynamicYAML(path)
	if err != nil {
		return DynamicEntry{}, false
	}
	d, ok := df.Servers[name]
	return d, ok
}

func UpsertDynamicYAML(path string, entry DynamicEntry) error {
	df, err := ReadDynamicYAML(path)
	if err != nil {
		return err
	}
	df.Servers[entry.Name] = entry
	out, err := yaml.Marshal(df)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 2: Delete the now-duplicate type+method definitions from `buildmcp.go`**

Remove the following ranges from `multi-agent/internal/executor/buildmcp.go`:
- The `dynamicEntry` struct + its `UnmarshalYAML` method (lines 622-672)
- The `dynamicFile` struct (lines 674-676)
- The methods `yamlPath`, `lookupExistingEntry`, `readDynamicYAML`, `upsertDynamicYAML` (lines 678-729)

Replace the in-file call sites with the new package-level functions:
- `e.lookupExistingEntry(spec.Name)` → `LookupDynamicEntry(DynamicYAMLPath(e.cfg.WorkDir), spec.Name)` — both call sites in `Run()`.
- `e.upsertDynamicYAML(dynamicEntry{...})` → `UpsertDynamicYAML(DynamicYAMLPath(e.cfg.WorkDir), DynamicEntry{...})` — single call site in `Run()`.
- Any other reference to the lowercase `dynamicEntry` / `dynamicFile` types becomes the exported `DynamicEntry` / `DynamicFile` (search and update).

- [ ] **Step 3: Update `buildmcp_test.go` references**

`internal/executor/buildmcp_test.go:153-157` uses `dynamicFile` and `df.Servers["foo"]`. Replace `dynamicFile` with `DynamicFile`. Re-run later in this task.

- [ ] **Step 4: Build to catch missed call sites**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./...`
Expected: clean build, no output.

- [ ] **Step 5: Run all executor tests as the safety net**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/executor/... -count=1`
Expected: PASS (existing build_mcp tests still pass with the refactored helpers; this proves the move is behavior-preserving).

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/executor/dynamicmcp.go multi-agent/internal/executor/buildmcp.go multi-agent/internal/executor/buildmcp_test.go
git commit -m "refactor: extract dynamic_mcp.yaml helpers to package-level"
```

---

### Task 2: Add `RegisterMCPExecutor` with full tests

The new executor is structurally similar to the trailing half of `BuildMCPExecutor.Run` (the validate-and-persist part). It does NOT call Claude. Input is JSON: `{spec, source_path}`. `spec` follows `buildspec.Spec` so capability publishing has the same metadata fields. `source_path` is relative to slave `WorkDir`.

**Files:**
- Create: `multi-agent/internal/executor/registermcp.go`
- Create: `multi-agent/internal/executor/registermcp_test.go`

- [ ] **Step 1: Write the failing happy-path test**

```go
// multi-agent/internal/executor/registermcp_test.go
package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/capability"
)

const minimalMCPSource = `import sys, json
def main():
    for line in sys.stdin:
        try:
            req = json.loads(line)
        except Exception:
            continue
        method = req.get("method", "")
        rid = req.get("id", 0)
        if method == "tools/list":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"tools":[{"name":"echo"}]}}), flush=True)
        elif method == "tools/call":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"result":"ok","capability_changed":False}}), flush=True)
        else:
            print(json.dumps({"jsonrpc":"2.0","id":rid,"error":{"message":"unknown"}}), flush=True)
if __name__ == "__main__":
    main()
`

func newRegisterMCPForTest(t *testing.T) (*RegisterMCPExecutor, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	repub := func(ctx context.Context) error { return nil }
	r := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir:   work,
		MCPExec:   mcpExec,
		Republish: repub,
	})
	return r, work
}

func writeSource(t *testing.T, workDir, rel, body string) {
	t.Helper()
	full := filepath.Join(workDir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

func TestRegisterMCP_HappyPath(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)

	prompt := `{
		"spec": {
			"name": "echo",
			"description": "Echo tool",
			"version": 1,
			"tools": [{"name":"echo","description":"echo","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages": []
		},
		"source_path": "generated_mcp/echo/v1.py"
	}`
	res, err := r.Run(context.Background(), Task{ID: "t1", Skill: "register_mcp", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	require.Contains(t, res.Summary, `"type":"mcp_tool_set"`)

	// dynamic_mcp.yaml has the entry
	df, err := ReadDynamicYAML(DynamicYAMLPath(work))
	require.NoError(t, err)
	require.Contains(t, df.Servers, "echo")
	require.Equal(t, "stdio", df.Servers["echo"].Transport)
	require.Equal(t, "python3", df.Servers["echo"].Command)

	// MCPExecutor knows about the server
	require.Contains(t, r.MCPExec.Servers(), "echo")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/executor/ -run TestRegisterMCP_HappyPath -count=1 -v`
Expected: FAIL with `undefined: RegisterMCPExecutor` / `undefined: NewRegisterMCPExecutor` / `undefined: RegisterMCPConfig`.

- [ ] **Step 3: Implement `RegisterMCPExecutor`**

```go
// multi-agent/internal/executor/registermcp.go
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
)

type RegisterMCPConfig struct {
	WorkDir   string
	MCPExec   *MCPExecutor
	Republish func(ctx context.Context) error
	Observer  Observer
}

type RegisterMCPExecutor struct {
	cfg     RegisterMCPConfig
	MCPExec *MCPExecutor
}

func NewRegisterMCPExecutor(cfg RegisterMCPConfig) *RegisterMCPExecutor {
	return &RegisterMCPExecutor{cfg: cfg, MCPExec: cfg.MCPExec}
}

type registerMCPPrompt struct {
	Spec       buildspec.Spec `json:"spec"`
	SourcePath string         `json:"source_path"`
}

func (e *RegisterMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var p registerMCPPrompt
	if err := json.Unmarshal([]byte(t.Prompt), &p); err != nil {
		return Result{}, fmt.Errorf("register_mcp prompt must be JSON: %w", err)
	}
	if err := buildspec.Validate(p.Spec); err != nil {
		return Result{}, fmt.Errorf("register_mcp: invalid spec: %w", err)
	}
	if p.SourcePath == "" {
		return Result{}, fmt.Errorf("register_mcp: source_path is required")
	}
	relPath := p.SourcePath
	absPath := filepath.Join(e.cfg.WorkDir, relPath)
	if !strings.HasPrefix(filepath.Clean(absPath), filepath.Clean(e.cfg.WorkDir)+string(filepath.Separator)) {
		return Result{}, fmt.Errorf("register_mcp: source_path escapes workdir: %s", relPath)
	}
	src, err := os.ReadFile(absPath)
	if err != nil {
		return Result{}, fmt.Errorf("register_mcp: read source: %w", err)
	}
	if err := validatePythonSyntax(string(src)); err != nil {
		return Result{}, fmt.Errorf("register_mcp: syntax: %w", err)
	}
	if bad, _ := ValidateImports(string(src), p.Spec.AllowedPackages); len(bad) > 0 {
		return Result{}, fmt.Errorf("register_mcp: disallowed imports: %s", strings.Join(bad, ","))
	}
	observed, err := SmokeLaunchPython(ctx, absPath, 3*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("register_mcp: smoke: %w", err)
	}
	tools := mergeMCPToolDescriptors(p.Spec, observed)

	mcpCfg := MCPServerCfg{Transport: "stdio", Command: "python3", Args: []string{absPath}}
	if err := e.cfg.MCPExec.RegisterStdio(p.Spec.Name, mcpCfg); err != nil {
		return Result{}, fmt.Errorf("register_mcp: register: %w", err)
	}
	entry := DynamicEntry{
		Name: p.Spec.Name, Transport: "stdio", Command: "python3",
		Args: []string{relPath}, Version: p.Spec.Version,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Tools:     tools,
	}
	if err := UpsertDynamicYAML(DynamicYAMLPath(e.cfg.WorkDir), entry); err != nil {
		return Result{}, fmt.Errorf("register_mcp: persist: %w", err)
	}
	if e.cfg.Republish != nil {
		if err := e.cfg.Republish(ctx); err != nil {
			sink.Write("warn", fmt.Sprintf("register_mcp: republish: %v", err))
		}
	}
	if e.cfg.Observer != nil {
		toolNames := capability.FlatNames(tools)
		e.cfg.Observer.Emit(observer.Event{
			Type:          observer.EventMCPServerCreated,
			TaskID:        t.ID,
			MCPServerName: p.Spec.Name,
			MCPTools:      toolNames,
			Status:        "completed",
		})
	}
	handle := handleJSON{
		Type: "mcp_tool_set",
		URL:  "file://" + absPath,
		Meta: map[string]string{
			"name":    p.Spec.Name,
			"version": strconv.Itoa(p.Spec.Version),
			"tools":   strings.Join(capability.FlatNames(tools), ","),
		},
	}
	return Result{Summary: handle.Marshal()}, nil
}
```

- [ ] **Step 4: Run the happy-path test to verify it passes**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/executor/ -run TestRegisterMCP_HappyPath -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Add the failure-mode tests**

Append to `registermcp_test.go`:

```go
func TestRegisterMCP_RejectsNonJSONPrompt(t *testing.T) {
	r, _ := newRegisterMCPForTest(t)
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: "not json"}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be JSON")
}

func TestRegisterMCP_RejectsInvalidSpec(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/x/v1.py", minimalMCPSource)
	prompt := `{"spec":{"name":"X bad"},"source_path":"generated_mcp/x/v1.py"}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid spec")
}

func TestRegisterMCP_RejectsBadSyntax(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/echo/v1.py", "def broken(:\n    pass\n")
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "syntax")
}

func TestRegisterMCP_RejectsDisallowedImport(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	src := "import requests_html\n" + minimalMCPSource
	writeSource(t, work, "generated_mcp/echo/v1.py", src)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "disallowed imports")
}

func TestRegisterMCP_RejectsSourcePathEscape(t *testing.T) {
	r, _ := newRegisterMCPForTest(t)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"../etc/passwd"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workdir")
}

func TestRegisterMCP_IdempotentReRegister(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t1", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	// Second call with same spec + same file: must succeed (overwrite is fine).
	_, err = r.Run(context.Background(), Task{ID: "t2", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
}

func TestRegisterMCP_RepublishCalled(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	called := 0
	r := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec,
		Republish: func(ctx context.Context) error { called++; return nil },
	})
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	require.Equal(t, 1, called)
}

func TestRegisterMCP_ObserverEmitsCreated(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	obs := &fakeObserver{}
	r := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec, Observer: obs,
	})
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	_, ok := observerEventOfType(obs.events, observer.EventMCPServerCreated)
	require.True(t, ok, "expected EventMCPServerCreated")
}
```

The helper `observerEventOfType` and `nopSink` are reused from `buildmcp_test.go` (same package). The helper `fakeObserver` is defined there too. These remain visible until Task 9.

- [ ] **Step 6: Run all register_mcp tests**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/executor/ -run TestRegisterMCP -count=1 -v`
Expected: 7 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/executor/registermcp.go multi-agent/internal/executor/registermcp_test.go
git commit -m "feat: add register_mcp slave executor"
```

---

### Task 3: Wire `register_mcp` route in slave-agent main; drop `build_mcp` wiring

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go:144-156`

- [ ] **Step 1: Replace the build_mcp wiring block**

In `cmd/slave-agent/main.go`, replace lines 144-156 (the `if hasSkill(cfg.Discovery.Skills, "build_mcp") { ... }` block) with:

```go
		if hasSkill(cfg.Discovery.Skills, "register_mcp") {
			routes["register_mcp"] = executor.NewRegisterMCPExecutor(executor.RegisterMCPConfig{
				WorkDir: workdir,
				MCPExec: mcpExec,
				Observer: obs,
				Republish: func(ctx context.Context) error {
					refreshCapabilities(ctx, "register_mcp registered or updated MCP server")
					return tn.PublishCard(ctx)
				},
			})
		}
```

- [ ] **Step 2: Build**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add multi-agent/cmd/slave-agent/main.go
git commit -m "feat(slave): wire register_mcp skill, drop build_mcp"
```

---

### Task 4: Update slave config example and e2e test skill list

**Files:**
- Modify: `multi-agent/cmd/slave-agent/config.example.yaml:29-34`
- Modify: `multi-agent/cmd/slave-agent/e2e_test.go:80`

- [ ] **Step 1: Update config example**

In `cmd/slave-agent/config.example.yaml`, replace the discovery skills block with:

```yaml
discovery:
  display_name: slave_agent
  description: General-purpose task executor with claude + MCP
  skills:
    - chat
    - mcp
    # - register_mcp  # opt-in: register pre-built MCP server files; pair with bash skill for generation
    # - bash  # opt-in deterministic shell execution for trusted workspaces
    # - claude_permissions  # opt-in task-channel Claude Code permission management
```

- [ ] **Step 2: Update e2e test skill list**

In `cmd/slave-agent/e2e_test.go:80`, change `skills: [chat, mcp, build_mcp]` to `skills: [chat, mcp, register_mcp]`.

- [ ] **Step 3: Build (no test run yet — many other call sites still reference build_mcp; full suite green at Task 11)**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./...`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add multi-agent/cmd/slave-agent/config.example.yaml multi-agent/cmd/slave-agent/e2e_test.go
git commit -m "feat(slave): advertise register_mcp in default config"
```

---

### Task 5: Add `register_slave_mcp` driver MCP tool

Mirrors `run_slave_bash` (`multi-agent/internal/driver/tools.go`): a thin helper that drives `submit_task` with `skill:"register_mcp"`. Lets driver Claude register an MCP file without hand-rolling the `submit_task` JSON.

**Files:**
- Create: `multi-agent/internal/driver/register_mcp_tool.go`
- Create: `multi-agent/internal/driver/register_mcp_tool_test.go`
- Modify: `multi-agent/internal/driver/tools.go` (register the new tool in the tool list)

- [ ] **Step 1: Inspect the existing `run_slave_bash` pattern as a reference**

Read `multi-agent/internal/driver/tools.go` and locate the `runSlaveBashTool` registration. The new tool follows the same shape: `Name()`, `Description()`, `InputSchema()`, `Call()`.

- [ ] **Step 2: Write a failing test for the new tool**

```go
// multi-agent/internal/driver/register_mcp_tool_test.go
package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestRegisterSlaveMCP_DelegatesAsRegisterMCPSkill(t *testing.T) {
	fakeSDK := &fakeAgentSDK{
		discover: []agentsdk.AgentCard{
			{AgentID: "s1", DisplayName: "slave", Status: "available",
				Card: json.RawMessage(`{"skills":["chat","register_mcp"]}`)},
		},
		delegateResp: agentsdk.DelegateTaskResponse{TaskID: "delegated-1"},
	}
	tools := newToolsForTest(t, fakeSDK)
	tool := &registerSlaveMCPTool{t: tools}
	args := json.RawMessage(`{
		"target_display_name": "slave",
		"spec": {"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path": "generated_mcp/echo/v1.py"
	}`)
	out, err := tool.Call(context.Background(), args)
	require.NoError(t, err)

	require.Equal(t, "register_mcp", fakeSDK.lastDelegate.Skill)
	require.Equal(t, "s1", fakeSDK.lastDelegate.TargetID)
	var prompt registerMCPPrompt
	require.NoError(t, json.Unmarshal([]byte(fakeSDK.lastDelegate.Prompt), &prompt))
	require.Equal(t, "echo", prompt.Spec.Name)
	require.Equal(t, "generated_mcp/echo/v1.py", prompt.SourcePath)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(out, &resp))
	require.Equal(t, "delegated-1", resp["task_id"])
}
```

The helpers `newToolsForTest`, `fakeAgentSDK`, and `lastDelegate` exist in `multi-agent/internal/driver/tools_test.go` — reuse them. If `registerMCPPrompt` is not exported, define a local struct in the test instead:

```go
type registerMCPPrompt struct {
	Spec       json.RawMessage `json:"spec"`
	SourcePath string          `json:"source_path"`
}
```

then assert on `prompt.SourcePath` and round-trip-unmarshal `prompt.Spec` to read the name.

- [ ] **Step 3: Run the test to verify it fails**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/driver/ -run TestRegisterSlaveMCP_DelegatesAsRegisterMCPSkill -count=1 -v`
Expected: FAIL — `undefined: registerSlaveMCPTool`.

- [ ] **Step 4: Implement the tool**

```go
// multi-agent/internal/driver/register_mcp_tool.go
package driver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/buildspec"
)

type registerSlaveMCPTool struct{ t *Tools }

func (s *registerSlaveMCPTool) Name() string { return "register_slave_mcp" }

func (s *registerSlaveMCPTool) Description() string {
	return "Register a pre-built MCP server file on a slave via its register_mcp skill. Use after a bash task has written the source and validated it locally."
}

func (s *registerSlaveMCPTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
        "type":"object",
        "properties":{
            "target_agent_id":{"type":"string"},
            "target_display_name":{"type":"string"},
            "spec":{"type":"object"},
            "source_path":{"type":"string"},
            "timeout_sec":{"type":"integer"}
        },
        "required":["spec","source_path"]
    }`)
}

func (s *registerSlaveMCPTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string          `json:"target_agent_id"`
		TargetDisplayName string          `json:"target_display_name"`
		Spec              buildspec.Spec  `json:"spec"`
		SourcePath        string          `json:"source_path"`
		TimeoutSec        int             `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.SourcePath == "" {
		return nil, &MCPToolError{Message: "source_path is required"}
	}
	if err := buildspec.Validate(args.Spec); err != nil {
		return nil, &MCPToolError{Message: "invalid spec: " + err.Error()}
	}
	targetID, _, _, _, err := s.t.resolveTarget(ctx, firstNonEmpty(args.TargetAgentID, args.TargetDisplayName))
	if err != nil {
		return nil, err
	}
	prompt := struct {
		Spec       buildspec.Spec `json:"spec"`
		SourcePath string         `json:"source_path"`
	}{Spec: args.Spec, SourcePath: args.SourcePath}
	body, err := json.Marshal(prompt)
	if err != nil {
		return nil, &MCPToolError{Message: "encode prompt: " + err.Error()}
	}
	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          "register_mcp",
		Prompt:         string(body),
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}
	return json.Marshal(map[string]any{
		"task_id":   resp.TaskID,
		"target_id": targetID,
		"skill":     "register_mcp",
	})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
```

If `firstNonEmpty` already exists elsewhere in the package, drop the local copy and use the existing one. If `resolveTarget`'s signature differs from what is shown, adapt the call site to match — refer to `run_slave_bash`'s usage as the ground truth.

- [ ] **Step 5: Register the new tool**

In `multi-agent/internal/driver/tools.go`, locate where `runSlaveBashTool` is appended to the tool list (search for `&runSlaveBashTool{`). Add the parallel registration:

```go
		&registerSlaveMCPTool{t: t},
```

- [ ] **Step 6: Run the test**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/driver/ -run TestRegisterSlaveMCP_DelegatesAsRegisterMCPSkill -count=1 -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/driver/register_mcp_tool.go multi-agent/internal/driver/register_mcp_tool_test.go multi-agent/internal/driver/tools.go
git commit -m "feat(driver): add register_slave_mcp MCP tool"
```

---

### Task 6: Drop `build_mcp` instructions from planner prompts

If the planner keeps telling Claude to emit `kind: build_mcp` nodes, but no slave advertises that skill anymore, every plan that needs MCP construction will fail dry-run. Strip the instruction so the planner instead emits a `chat`/`bash` node that calls `register_slave_mcp`.

**Files:**
- Modify: `multi-agent/internal/planner/prompts.go:31, 40-49`

- [ ] **Step 1: Read the current planner prompt**

Run: `sed -n '20,70p' multi-agent/internal/planner/prompts.go`
Expected: see the section describing `kind: build_mcp` examples and the rule "If no agent lists the needed tool but at least one agent has skill 'build_mcp' …".

- [ ] **Step 2: Remove the build_mcp emission instructions**

Edit `multi-agent/internal/planner/prompts.go`:
- Line 31: Remove the parenthetical `(currently only "build_mcp")`. Result: `- "kind": optional special node kind`.
- Lines 40-41 (the `Example node with kind: "build_mcp"` block, both the heading line and the JSON example): delete the example entirely.
- Line 49 (the rule "If no agent lists the needed tool but at least one agent has skill 'build_mcp' …"): delete this rule entirely.

After this, no `build_mcp` string remains in the planner prompts.

- [ ] **Step 3: Verify with grep**

Run: `grep -n "build_mcp" multi-agent/internal/planner/prompts.go`
Expected: no output.

- [ ] **Step 4: Run planner tests**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/planner/... -count=1`
Expected: PASS. (Planner tests use fake-planner.sh stub output; they're not sensitive to the prompt text.)

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/planner/prompts.go
git commit -m "feat(planner): stop emitting build_mcp nodes"
```

---

### Task 7: Remove `AllowBuildMCP` from contract types and validation

The `allow_build_mcp` flag in `ExecutionPolicy` has no remaining effect.

**Files:**
- Modify: `multi-agent/internal/contract/types.go` (remove `AllowBuildMCP` field)
- Modify: `multi-agent/internal/contract/validate.go:79` (remove the `build_mcp` skill string-check)
- Modify: `multi-agent/internal/contract/contract_test.go` (drop any case referencing `AllowBuildMCP`)

- [ ] **Step 1: Find every reference**

Run: `grep -rn "AllowBuildMCP\|allow_build_mcp" multi-agent/`
Expected: see hits in `contract/types.go`, `contract/validate.go`, `contract/contract_test.go`, possibly `driver/capability_tools.go`, `driver/contract_tools.go`, and `multiagent` skill docs. Plan addresses non-test, non-doc sites in this task; docs/driver get handled in their own tasks.

- [ ] **Step 2: Remove the field from the struct**

In `multi-agent/internal/contract/types.go`, find the `ExecutionPolicy` struct, and delete the `AllowBuildMCP` field (and its JSON tag).

- [ ] **Step 3: Drop the contract validation rule**

In `multi-agent/internal/contract/validate.go`, locate the block around line 79 (`if s == "build_mcp" {`) and remove the entire `if` (likely a skill-name check that rejects `build_mcp` unless `AllowBuildMCP` is true). If the surrounding loop becomes trivial, simplify it accordingly.

- [ ] **Step 4: Update contract tests**

Run: `grep -n "AllowBuildMCP\|allow_build_mcp" multi-agent/internal/contract/contract_test.go`
For each hit, remove the assertion/setter line. Tests that asserted "allow_build_mcp=false rejects build_mcp skill" should be deleted entirely — that behavior is gone.

- [ ] **Step 5: Run contract tests**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/contract/... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/contract/types.go multi-agent/internal/contract/validate.go multi-agent/internal/contract/contract_test.go
git commit -m "feat(contract): remove allow_build_mcp policy field"
```

---

### Task 8: Remove orchestrator `build_mcp` paths

Three things to delete: the preflight file, the contract policy gates, and the fanout replan branch.

**Files:**
- Delete: `multi-agent/internal/orchestrator/buildmcp_preflight.go`
- Delete: `multi-agent/internal/orchestrator/buildmcp_preflight_test.go`
- Modify: `multi-agent/internal/orchestrator/contract_policy.go:35-42, 67-74`
- Modify: `multi-agent/internal/orchestrator/fanout.go:518-535, 670-720`
- Modify: `multi-agent/internal/orchestration/plan_semantics.go:50-60`
- Modify: any `internal/orchestrator/fanout_test.go` cases that exercise the replan loop

- [ ] **Step 1: Delete the preflight files**

```bash
rm multi-agent/internal/orchestrator/buildmcp_preflight.go multi-agent/internal/orchestrator/buildmcp_preflight_test.go
```

- [ ] **Step 2: Remove contract_policy gates**

In `multi-agent/internal/orchestrator/contract_policy.go`, delete the two blocks that look like:

```go
	if !policy.AllowBuildMCP {
		for _, n := range nodes {
			if n.Skill == "build_mcp" || n.Kind == "build_mcp" {
				return fmt.Errorf("build_mcp node %s rejected by contract policy", n.ID)
			}
		}
	}
```

Both occur — one in `ValidateWithContractPolicy` (~line 35-42), one in `ValidateAppendWithContractPolicy` (~line 67-74). Drop both.

- [ ] **Step 3: Remove fanout replan branch**

In `multi-agent/internal/orchestrator/fanout.go`:
- Delete the `iterCount := map[string]int{}` declaration (search for it; likely above line 600).
- Delete the entire `case "build_mcp_blocked":` branch in the output-handle switch (lines 686–720 region).
- Delete the `if n.Kind == "build_mcp" || n.Skill == "build_mcp" {…}` block around line 518-535 (this is plan validation rejection / canonicalization; gone now).
- Delete the `const maxBuildIterations = 3` (line ~24) and the comment block above it.

If the switch no longer has any cases, simplify or remove the `switch hType` wrapper.

- [ ] **Step 4: Remove plan_semantics handling**

In `multi-agent/internal/orchestration/plan_semantics.go`, delete the `case n.Skill == "build_mcp" || n.Kind == "build_mcp":` branch (lines 50-60ish). If the surrounding switch is now empty, remove it.

- [ ] **Step 5: Drop matching tests**

Run: `grep -rn "build_mcp\|BuildMCP\|maxBuildIterations\|build_mcp_blocked" multi-agent/internal/orchestrator/ multi-agent/internal/orchestration/`
For each hit in test files: delete the entire test function. Examples to remove from `fanout_test.go`: tests for `plan_build_mcp_repair`, the `iterCount`-based tests, the `blocked` constants around lines 757-925.

- [ ] **Step 6: Build to find missed call sites**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./...`
Expected: clean build. If errors, fix and re-run.

- [ ] **Step 7: Run orchestrator + orchestration tests**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/orchestrator/... ./internal/orchestration/... -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add multi-agent/internal/orchestrator/ multi-agent/internal/orchestration/
git commit -m "feat(orchestrator): remove build_mcp preflight and replan loop"
```

---

### Task 9: Remove the `BuildMCPExecutor`

After Task 8, nothing references `BuildMCPExecutor`. Delete it — but first re-home three helpers that `registermcp.go` depends on (`mergeMCPToolDescriptors`, `handleJSON` + its `Marshal` method, `validatePythonSyntax`).

**Files:**
- Create: `multi-agent/internal/executor/mcpsource.go` (the three helpers, package-level)
- Delete: `multi-agent/internal/executor/buildmcp.go`
- Delete: `multi-agent/internal/executor/buildmcp_test.go`
- Delete: `multi-agent/testdata/fake-build-claude.sh` (no remaining consumer)

- [ ] **Step 1: Re-home the three shared helpers**

Create `multi-agent/internal/executor/mcpsource.go` with the helpers copied verbatim from their current location in `buildmcp.go` (use `grep -n "func mergeMCPToolDescriptors\|type handleJSON\|func.*handleJSON.*Marshal\|func validatePythonSyntax" internal/executor/buildmcp.go` to locate; today they're at lines 244, 334, 342, 460):

```go
// multi-agent/internal/executor/mcpsource.go
package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yourorg/multi-agent/internal/capability"
)

type handleJSON struct {
	Type string            `json:"type"`
	URL  string            `json:"url"`
	Meta map[string]string `json:"meta,omitempty"`
}

func (h handleJSON) Marshal() string {
	b, _ := json.Marshal(h)
	return string(b)
}

func mergeMCPToolDescriptors(spec buildSpec, observed []capability.MCPToolDescriptor) []capability.MCPToolDescriptor {
	// COPY VERBATIM from buildmcp.go (current lines 244-297).
}

func validatePythonSyntax(src string) error {
	cmd := exec.Command("python3", "-c", "import ast,sys; ast.parse(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(src)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ast.parse failed: %v: %s", err, errBuf.String())
	}
	return nil
}
```

For `mergeMCPToolDescriptors`, do not retype it from memory — open `buildmcp.go`, copy the function body verbatim. Same for `handleJSON`'s exact field set if it differs from what's shown here.

The type alias `type buildSpec = buildspec.Spec` (currently line 83 of buildmcp.go) is also needed in this file's compile context. Add `type buildSpec = buildspec.Spec` to `mcpsource.go` (with the matching `"github.com/yourorg/multi-agent/internal/buildspec"` import). After Task 9 step 3 deletes buildmcp.go, this becomes the sole definition.

- [ ] **Step 2: Delete the helpers from `buildmcp.go`**

Remove the four helper definitions (`mergeMCPToolDescriptors`, `handleJSON`, the `Marshal` method, `validatePythonSyntax`) and the `type buildSpec = ...` alias from `buildmcp.go`. Verify build still compiles before moving on:

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./internal/executor/...`
Expected: clean.

- [ ] **Step 3: Confirm no remaining references**

Run: `grep -rn "BuildMCPExecutor\|NewBuildMCPExecutor\|BuildMCPConfig\|fake-build-claude" multi-agent/`
Expected: only hits in `buildmcp.go` and `buildmcp_test.go` and `testdata/fake-build-claude.sh`. If anything else shows up, stop and adjust prior tasks.

- [ ] **Step 4: Re-home helpers still needed by tests**

`registermcp_test.go` (Task 2) reuses `fakeObserver`, `observerEventOfType`, and `nopSink` defined in `buildmcp_test.go`. Move just those three definitions to a new sibling file `multi-agent/internal/executor/testhelpers_test.go`:

```go
// multi-agent/internal/executor/testhelpers_test.go
package executor

import "github.com/yourorg/multi-agent/internal/observer"

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

type nopSink struct{}

func (n *nopSink) Write(eventType, data string) {}
func (n *nopSink) Close()                       {}
```

(If `nopSink` or another helper has subtly different fields in the original, copy verbatim — open the file and check before transcribing.)

- [ ] **Step 5: Delete the files**

```bash
rm multi-agent/internal/executor/buildmcp.go
rm multi-agent/internal/executor/buildmcp_test.go
rm multi-agent/testdata/fake-build-claude.sh
```

- [ ] **Step 6: Build + run executor tests**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./... && go test ./internal/executor/... -count=1`
Expected: PASS, no missing types.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/executor/ multi-agent/testdata/
git commit -m "feat: remove BuildMCPExecutor"
```

---

### Task 10: Remove `allow_build_mcp` from driver tools and update skill docs

**Files:**
- Modify: `multi-agent/internal/driver/capability_tools.go:82, 419`
- Modify: `multi-agent/internal/driver/contract_tools.go` (any `AllowBuildMCP` references surfaced by the type removal)
- Modify: `multi-agent/internal/driver/tools_test.go` (drop test cases that exercise `allow_build_mcp`, `build_mcp` candidate enumeration)
- Modify: `skills/multiagent/SKILL.md`
- Modify: `skills/multiagent/references/slave-skills.md`
- Modify: `skills/multiagent/references/task-contract.md`
- Modify: `skills/multiagent/references/driver-tools.md`

- [ ] **Step 1: Strip `allow_build_mcp` from `draft_task_contract` schema**

In `multi-agent/internal/driver/capability_tools.go` around line 82, the `draft_task_contract` `InputSchema` has `"allow_build_mcp":{"type":"boolean"}`. Remove that property entry (and trailing comma if needed for valid JSON). Also remove the Go args struct field reading it and any subsequent assignment to `contract.ExecutionPolicy.AllowBuildMCP` (gone after Task 7 anyway — go build will surface this).

- [ ] **Step 2: Drop `candidateAgentsWithSkill(agents, "build_mcp", …)` block**

At `capability_tools.go:419`, locate the `candidates := candidateAgentsWithSkill(agents, "build_mcp", allowedTargets)` line and the surrounding block that computes `candidate_build_targets` / `requires_build_mcp`. The whole block goes. In the `dry_run_contract` result, drop `requires_build_mcp` and `candidate_build_targets` fields entirely (they're always false/empty now). Adjust the result struct/marshalling accordingly.

- [ ] **Step 3: Build and adjust test fixtures**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./...`
Expected: errors from `tools_test.go` and `contract_tools.go` referencing removed fields. Fix each:
- Delete `tools_test.go` cases that pass `"allow_build_mcp":true` or assert on `requires_build_mcp`/`candidate_build_targets`/`build_mcp` candidate listings (lines 232, 268, 348, 623, 788, 811, 856 — verify each against current source).
- Adjust `contract_tools.go` if it sets `AllowBuildMCP` on a struct it builds.

Re-run build until clean.

- [ ] **Step 4: Update `skills/multiagent/SKILL.md`**

Apply these line edits (one Edit call per change):
- Line 21 (mentions `allow_build_mcp`): rewrite the sentence to read `4. Call `dry_run_contract`. If blocked, ask for missing permission/capability or pair the bash skill with `register_mcp` to add a new MCP server.`
- Line 30 (route choice list): change `driver_fanout` description to drop the "generated MCP" phrase if present.
- Line 44 (common mistakes): replace `Setting allow_build_mcp: true without requiring generated code to persist as artifacts.` with `Calling register_mcp without first validating the generated source via bash (smoke + acceptance).`

- [ ] **Step 5: Rewrite `references/slave-skills.md` build_mcp section**

In `skills/multiagent/references/slave-skills.md`:
- Line 7 (the skills enumeration): replace `- build_mcp: generate and register a reusable MCP server.` with `- register_mcp: register a pre-built MCP server file (paired with bash to generate and validate).`
- Lines 35-74 (the entire `## build_mcp` section): replace with:

```markdown
## `register_mcp`

Prompt is JSON:

```json
{
  "spec": {
    "name": "row_stats",
    "description": "Compute statistics for rows",
    "version": 1,
    "tools": [
      {
        "name": "summarize_rows",
        "description": "Summarize numeric row values",
        "args_schema": {"type": "object", "properties": {"rows": {"type":"array"}}, "required":["rows"], "additionalProperties": false},
        "result_description": "JSON summary statistics"
      }
    ],
    "allowed_packages": []
  },
  "source_path": "generated_mcp/row_stats/v1.py"
}
```

Validation:

- `spec.name` matches `[a-z][a-z0-9_]{0,31}`.
- `spec.tools` has at least one entry; each tool needs name, description, valid JSON `args_schema`, and `result_description`.
- `source_path` is relative to the slave workdir and must not escape it.
- Python source is syntax-checked, imports are checked against `spec.allowed_packages`, then smoke-launched once (`tools/list`).
- On success the file is registered in the MCP runtime and persisted to `dynamic_mcp.yaml`; the slave's capability card is republished.

## Bash → register_mcp workflow

The driver Claude should construct MCP servers by combining `bash` and `register_mcp`:

1. Use `bash` (or `run_slave_bash`) to write `generated_mcp/<name>/v1.py` on the slave, optionally importing or shelling out to existing services already on the slave.
2. Inside the same bash task, run real acceptance cases against the file (e.g. `python3 generated_mcp/<name>/v1.py < tests.jsonl`) and iterate on the source until outputs match expectations.
3. Submit `register_mcp` (or `register_slave_mcp` from the driver) with the spec and `source_path`. Registration only does structural checks + `tools/list` smoke; acceptance is the previous step's job.

The structured spec exists so capability publishing has clean metadata; it is not a generation contract.
```

- [ ] **Step 6: Update `references/task-contract.md`**

In `skills/multiagent/references/task-contract.md`:
- Line 38 (the `"allow_build_mcp": false,` example field): delete the line.
- Line 80 (the `If allow_build_mcp:false, do not require build_mcp.` validation rule): delete.
- Lines 96-104 (the entire `## Build MCP Policy` section): delete.

- [ ] **Step 7: Update `references/driver-tools.md`**

In `skills/multiagent/references/driver-tools.md`:
- Lines 55-65 area (`draft_task_contract` input example): delete the `"allow_build_mcp": false,` line.
- Lines 174-219 area (Slave Control Helpers): add a new section after `run_slave_bash`:

```markdown
### `register_slave_mcp`

Input:

```json
{
  "target_agent_id": "optional",
  "target_display_name": "slave-a",
  "spec": {
    "name": "row_stats",
    "description": "Compute statistics for rows",
    "version": 1,
    "tools": [
      {
        "name": "summarize_rows",
        "description": "Summarize numeric row values",
        "args_schema": {"type":"object","properties":{"rows":{"type":"array"}},"required":["rows"]},
        "result_description": "JSON summary"
      }
    ],
    "allowed_packages": []
  },
  "source_path": "generated_mcp/row_stats/v1.py",
  "timeout_sec": 60
}
```

Requires target skill `register_mcp`. Delegates `skill:"register_mcp"` with the JSON above. Use after a bash task has written and validated the source.
```

- [ ] **Step 8: Verify no stray references remain**

Run: `grep -rn "build_mcp\|allow_build_mcp\|BuildMCP" multi-agent/ skills/`
Expected: zero hits.

- [ ] **Step 9: Commit**

```bash
git add multi-agent/internal/driver/ skills/multiagent/
git commit -m "feat(driver,docs): remove allow_build_mcp; document register_mcp workflow"
```

---

### Task 11: Full verification

- [ ] **Step 1: Vet**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go vet ./...`
Expected: clean.

- [ ] **Step 2: Build**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go build ./...`
Expected: clean.

- [ ] **Step 3: Full internal test suite**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./internal/... -count=1 -timeout 180s`
Expected: every package PASS.

- [ ] **Step 4: cmd package tests**

Run: `PATH=/root/.local/go-toolchain/go/bin:$PATH go test ./cmd/... -count=1 -timeout 180s`
Expected: PASS.

- [ ] **Step 5: Grep for orphans one more time**

Run: `grep -rn "build_mcp\|BuildMCP\|allow_build_mcp" multi-agent/ skills/ ../docs/superpowers/specs/ 2>/dev/null | grep -v ../docs/superpowers/plans/`
Expected: only doc/spec history references in `docs/superpowers/specs/` (intentional history; not touched by this plan).

- [ ] **Step 6: Final commit (if any lint/format drift)**

```bash
git status
# if clean, no action; if format/lint diff: 
git add -A
git commit -m "chore: cleanup after build_mcp removal"
```

---

## Follow-ups (out of scope for this plan)

- Acceptance test harness inside `register_mcp` itself (let the spec declare `acceptance_cases` so registration runs `tools/call` with sample inputs before persisting). Today bash owns acceptance; later it could move closer to registration.
- The original `dynamic-mcp-design.md` spec mentions a 3-iteration replan; that's gone. If the project wants a "register failed → driver retries with different source" loop, it now lives in the driver Claude's bash plan, not the orchestrator.
- The `compose_servers` and `hints` fields on `buildspec.Spec` are now metadata only. Either repurpose them in `register_mcp` or remove from the type.
