# Dynamic MCP Server Creation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an autonomous build-and-register loop so that when a master task needs a tool no agent advertises, the planner can dispatch a `build_mcp` sub-task to a slave with matching resources; the slave authors a Python MCP server via claude, validates and hot-registers it, and the orchestrator re-plans phase-2 with the new tool visible (3-iteration negotiation cap).

**Architecture:** Slaves declare hardware/runtime `resources:` in their config, advertised on the agent card alongside a flat `tools[]` list. Planner sees both. New `BuildMCPExecutor` (skill `build_mcp`) on the slave runs claude → validates imports → smoke-launches → hot-registers via new `MCPExecutor.RegisterStdio`. Orchestrator gains a small phase-boundary in `runFanout` that, on `build_mcp_blocked` or `mcp_tool_set` handle outputs, re-calls the planner and appends new nodes to the running scheduler via new `Scheduler.Append`. `Node.Skill` is threaded through `DelegateTask` so phase-2 use-nodes route directly to the slave's `mcp` executor.

**Tech Stack:** Go 1.22+ (existing module), `github.com/agentserver/agentserver/pkg/agentsdk`, `gopkg.in/yaml.v3`, Python 3.11 (only on slaves with `build_mcp` skill), stdlib `crypto/sha256`, stdlib `os/exec`. Reuses existing `internal/executor.NewClaudeExecutor` invocation conventions and the in-process `pkg/transport.Handle` JSON convention.

**Spec:** `docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md`

---

## File structure

| File | Purpose |
|---|---|
| `multi-agent/internal/config/config.go` | + `Resources`, `CPUSpec`, `GPUSpec` types |
| `multi-agent/internal/config/config_test.go` | + Resources yaml round-trip case |
| `multi-agent/internal/planner/planner.go` | + `Node.Kind`, `Node.Skill` fields |
| `multi-agent/internal/planner/planner_test.go` | + JSON round-trip including new fields |
| `multi-agent/internal/executor/mcp.go` | + `RegisterStdio`, `ListTools` |
| `multi-agent/internal/executor/mcp_test.go` | + replace-semantics + ListTools |
| `multi-agent/testdata/fake-mcp-stdio/main.go` | + handle `tools/list` method |
| `multi-agent/internal/executor/importsallowlist.go` | NEW. Stdlib name set + `Validate(src, allowed)` |
| `multi-agent/internal/executor/importsallowlist_test.go` | NEW |
| `multi-agent/internal/executor/pythonsmoke.go` | NEW. Spawn python3, send `tools/list`, await response |
| `multi-agent/internal/executor/pythonsmoke_test.go` | NEW |
| `multi-agent/internal/executor/buildmcp.go` | NEW. `BuildMCPExecutor` with `Run` |
| `multi-agent/internal/executor/buildmcp_test.go` | NEW |
| `multi-agent/testdata/fake-build-claude.sh` | NEW. Emits a deterministic minimal Python MCP server |
| `multi-agent/internal/tunnel/tunnel.go` | `PublishCard` collects tools via `ListTools` and includes `Resources` |
| `multi-agent/internal/tunnel/tunnel_test.go` | + assertion that posted body includes tools[] + resources |
| `multi-agent/internal/planner/prompts.go` | Extend `agentsJSON` (tools+resources) and `planPrompt` (build_mcp instructions) |
| `multi-agent/internal/planner/prompts_test.go` | + assertion the planPrompt contains the new instructions |
| `multi-agent/internal/orchestrator/dag.go` | + `Scheduler.Append([]planner.Node)` |
| `multi-agent/internal/orchestrator/dag_test.go` | + Append case |
| `multi-agent/internal/orchestrator/fanout.go` | Pass `Skill: n.Skill` to DelegateTask; phase-boundary handler for blocked/tool_set |
| `multi-agent/internal/orchestrator/fanout_test.go` | + negotiation loop + max-iter + phase-2 replan |
| `multi-agent/internal/webui/server.go` | + `/bridge/call` POST handler |
| `multi-agent/internal/webui/server_test.go` | + bridge handler test |
| `multi-agent/cmd/slave-agent/main.go` | Wire `build_mcp` route + load `dynamic_mcp.yaml` + pass Resources |
| `multi-agent/cmd/slave-agent/config.example.yaml` | + `resources:` block doc |
| `multi-agent/examples/dynamic-mcp/README.md` | NEW |
| `multi-agent/examples/dynamic-mcp/agent-builder/main.go` | NEW (thin wrapper) |
| `multi-agent/examples/dynamic-mcp/agent-builder/config.example.yaml` | NEW |
| `multi-agent/examples/dynamic-mcp/e2e-driver/main.go` | NEW |
| `multi-agent/examples/dynamic-mcp/scripts/e2e.sh` | NEW |

All paths absolute from `/mnt/c/Users/DELL/multi-agent`. Module root is `/mnt/c/Users/DELL/multi-agent/multi-agent`. All `go` commands run from the module root.

---

## Task 1: Resources struct in config

**Files:**
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/internal/config/config_test.go`

- [ ] **Step 1: Read existing test file structure**

```bash
grep -n "func Test\|TestLoad\|t.TempDir" /mnt/c/Users/DELL/multi-agent/multi-agent/internal/config/config_test.go | head -10
```
Note the existing test naming pattern; add a new test function below.

- [ ] **Step 2: Write the failing test**

Append to `multi-agent/internal/config/config_test.go`:

```go
func TestLoad_ResourcesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	src := `
server: {url: "http://x", name: "s"}
resources:
  cpu:
    cores: 8
    arch: x86_64
  gpu:
    count: 1
    model: "RTX 4090"
    vram_gb: 24
  memory_gb: 32
  devices: [camera, gpio]
  tags: [photogrammetry]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Resources == nil {
		t.Fatal("Resources nil")
	}
	if c.Resources.CPU == nil || c.Resources.CPU.Cores != 8 || c.Resources.CPU.Arch != "x86_64" {
		t.Fatalf("CPU mismatch: %+v", c.Resources.CPU)
	}
	if c.Resources.GPU == nil || c.Resources.GPU.Count != 1 || c.Resources.GPU.Model != "RTX 4090" || c.Resources.GPU.VRAMGB != 24 {
		t.Fatalf("GPU mismatch: %+v", c.Resources.GPU)
	}
	if c.Resources.MemoryGB != 32 {
		t.Fatalf("MemoryGB = %d", c.Resources.MemoryGB)
	}
	if !reflect.DeepEqual(c.Resources.Devices, []string{"camera", "gpio"}) {
		t.Fatalf("Devices = %v", c.Resources.Devices)
	}
	if !reflect.DeepEqual(c.Resources.Tags, []string{"photogrammetry"}) {
		t.Fatalf("Tags = %v", c.Resources.Tags)
	}
}

func TestLoad_ResourcesAbsentIsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte(`server: {url: "u", name: "n"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Resources != nil {
		t.Fatalf("expected nil Resources when not declared, got %+v", c.Resources)
	}
}
```

If `reflect` and/or `filepath` and/or `os` are not yet imported in the test file, add them to the import block.

- [ ] **Step 3: Run test to verify it fails**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/config/
```
Expected: build failure (`Resources` undefined).

- [ ] **Step 4: Add Resources types to `multi-agent/internal/config/config.go`**

Add to the `Config` struct (after `Fanout`):

```go
	Resources *Resources `yaml:"resources,omitempty"`
```

Add new types at the end of the file:

```go
type Resources struct {
	CPU      *CPUSpec `yaml:"cpu,omitempty"       json:"cpu,omitempty"`
	GPU      *GPUSpec `yaml:"gpu,omitempty"       json:"gpu,omitempty"`
	MemoryGB int      `yaml:"memory_gb,omitempty" json:"memory_gb,omitempty"`
	Devices  []string `yaml:"devices,omitempty"   json:"devices,omitempty"`
	Tags     []string `yaml:"tags,omitempty"      json:"tags,omitempty"`
}

type CPUSpec struct {
	Cores int    `yaml:"cores"          json:"cores"`
	Arch  string `yaml:"arch,omitempty" json:"arch,omitempty"`
}

type GPUSpec struct {
	Count  int    `yaml:"count"             json:"count"`
	Model  string `yaml:"model,omitempty"   json:"model,omitempty"`
	VRAMGB int    `yaml:"vram_gb,omitempty" json:"vram_gb,omitempty"`
}
```

- [ ] **Step 5: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/config/
```
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/config/
git commit -m "$(cat <<'EOF'
feat(config): add Resources block (cpu, gpu, memory, devices, tags)

Optional yaml block on slave configs that advertises hardware /
runtime resources. Verbatim pass-through to the agent card so the
planner can match a build_mcp request to an agent with the right
hardware. All fields optional.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: planner.Node.Kind + Skill fields

**Files:**
- Modify: `multi-agent/internal/planner/planner.go`
- Modify: `multi-agent/internal/planner/planner_test.go`

- [ ] **Step 1: Inspect existing Node struct**

```bash
grep -n "type Node\|^}" /mnt/c/Users/DELL/multi-agent/multi-agent/internal/planner/planner.go | head -10
```

- [ ] **Step 2: Write the failing test**

Append to `multi-agent/internal/planner/planner_test.go`:

```go
func TestPlan_DecodeNodeKindAndSkill(t *testing.T) {
	jsonSrc := `[
	  {"id":"n0","target_id":"a","kind":"build_mcp","skill":"build_mcp","prompt":"spec"},
	  {"id":"n1","target_id":"b","skill":"mcp","prompt":"call","depends_on":["n0"]},
	  {"id":"n2","target_id":"c","prompt":"chat","depends_on":["n1"]}
	]`
	var nodes []Node
	if err := json.Unmarshal([]byte(jsonSrc), &nodes); err != nil {
		t.Fatal(err)
	}
	if nodes[0].Kind != "build_mcp" || nodes[0].Skill != "build_mcp" {
		t.Fatalf("n0 = %+v", nodes[0])
	}
	if nodes[1].Kind != "" || nodes[1].Skill != "mcp" {
		t.Fatalf("n1 = %+v", nodes[1])
	}
	if nodes[2].Kind != "" || nodes[2].Skill != "" {
		t.Fatalf("n2 = %+v", nodes[2])
	}
}
```

If `encoding/json` is not yet imported in the test file, add it.

- [ ] **Step 3: Run test to verify it fails**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/planner/ -run NodeKind
```
Expected: failure on `Kind` / `Skill` field unknown.

- [ ] **Step 4: Add fields to Node**

Locate the existing `type Node struct { ... }` in `multi-agent/internal/planner/planner.go` and append the two new fields:

```go
type Node struct {
	ID        string   `json:"id"`
	TargetID  string   `json:"target_id"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"depends_on,omitempty"`
	Kind      string   `json:"kind,omitempty"`  // "" or "build_mcp"
	Skill     string   `json:"skill,omitempty"` // "" → slave's default exec; "mcp" → mcpExec; "build_mcp" → BuildMCPExecutor
}
```

(If the existing struct already has additional fields, preserve them.)

- [ ] **Step 5: Run all planner tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/planner/
```
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/planner/
git commit -m "$(cat <<'EOF'
feat(planner): add Node.Kind and Node.Skill fields

Kind="build_mcp" marks a phase-1 build node so the orchestrator can
trigger phase-2 replanning after it completes. Skill is threaded
through to DelegateTask so a node can target the slave's mcp
executor (skill="mcp"), build_mcp executor, or default claude
("").

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: MCPExecutor.RegisterStdio + ListTools (+ extend fake-mcp-stdio)

**Files:**
- Modify: `multi-agent/internal/executor/mcp.go`
- Modify: `multi-agent/internal/executor/mcp_test.go`
- Modify: `multi-agent/testdata/fake-mcp-stdio/main.go`

- [ ] **Step 1: Extend fake-mcp-stdio to handle `tools/list`**

In `multi-agent/testdata/fake-mcp-stdio/main.go`, change the dispatch switch to inspect `r.Method`:

```go
		out := resp{JSONRPC: "2.0", ID: r.ID}
		switch r.Method {
		case "tools/list":
			out.Result = map[string]interface{}{
				"tools": []map[string]string{
					{"name": "echo"},
					{"name": "raise"},
					{"name": "boom"},
				},
			}
		case "tools/call":
			var p toolCallParams
			_ = json.Unmarshal(r.Params, &p)
			switch p.Name {
			case "echo":
				out.Result = toolResult{Result: p.Arguments, CapabilityChanged: false}
			case "raise":
				out.Result = toolResult{Result: "raised", CapabilityChanged: true, ChangeHint: "did the thing"}
			case "boom":
				out.Error = map[string]string{"message": "intentional failure"}
			default:
				out.Error = map[string]string{"message": "unknown tool"}
			}
		default:
			out.Error = map[string]string{"message": "unknown method"}
		}
```

(Replace the existing `switch p.Name { ... }` block accordingly.)

- [ ] **Step 2: Write failing tests**

Append to `multi-agent/internal/executor/mcp_test.go`:

```go
func TestMCPExecutor_ListTools(t *testing.T) {
	bin := buildFakeMCPStdio(t) // existing helper used elsewhere in mcp_test.go;
	// if no helper exists, build inline:  bin := filepath.Join(t.TempDir(), "fake")
	// then exec.Command("go","build","-o",bin,"./testdata/fake-mcp-stdio").Run()
	cfg := MCPServerCfg{Transport: "stdio", Command: bin}
	e := NewMCPExecutor(map[string]MCPServerCfg{"x": cfg})
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := e.ListTools(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"echo": true, "raise": true, "boom": true}
	if len(tools) != 3 {
		t.Fatalf("got %v", tools)
	}
	for _, n := range tools {
		if !want[n] {
			t.Fatalf("unexpected tool name %q", n)
		}
	}
}

func TestMCPExecutor_RegisterStdioReplaces(t *testing.T) {
	bin := buildFakeMCPStdio(t)
	e := NewMCPExecutor(map[string]MCPServerCfg{})
	defer e.Close()

	if err := e.RegisterStdio("x", MCPServerCfg{Transport: "stdio", Command: bin}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.ListTools(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	// Re-register with the same command (forces process replace).
	if err := e.RegisterStdio("x", MCPServerCfg{Transport: "stdio", Command: bin}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ListTools(ctx, "x"); err != nil {
		t.Fatalf("post-reregister ListTools: %v", err)
	}
}

func TestMCPExecutor_RegisterStdioRejectsHTTP(t *testing.T) {
	e := NewMCPExecutor(map[string]MCPServerCfg{})
	defer e.Close()
	err := e.RegisterStdio("x", MCPServerCfg{Transport: "http", URL: "http://x"})
	if err == nil {
		t.Fatal("expected error for non-stdio transport")
	}
}
```

If `buildFakeMCPStdio` doesn't already exist in the test file, add this helper at the top of the file (before the first test):

```go
func buildFakeMCPStdio(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-mcp-stdio")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fake-mcp-stdio")
	cmd.Dir = projectRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake-mcp-stdio: %v: %s", err, out)
	}
	return bin
}

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for d := wd; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("project root not found")
	return ""
}
```

(If a similar helper already exists with a different name in the file, reuse it instead of adding this one.)

- [ ] **Step 3: Run tests to confirm they fail (RegisterStdio + ListTools undefined)**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run "RegisterStdio|ListTools"
```
Expected: build failure.

- [ ] **Step 4: Add `RegisterStdio` and `ListTools` to `multi-agent/internal/executor/mcp.go`**

Append to the file:

```go
// RegisterStdio adds (or replaces) a stdio MCP server entry at runtime.
// If a server with this name already exists, its subprocess is killed and
// removed before the new entry is installed; subsequent calls will spawn
// the new subprocess on demand. Only "stdio" transport is accepted.
func (e *MCPExecutor) RegisterStdio(name string, cfg MCPServerCfg) error {
	if cfg.Transport != "stdio" {
		return fmt.Errorf("RegisterStdio: transport must be stdio, got %q", cfg.Transport)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, ok := e.stdios[name]; ok {
		old.kill()
		delete(e.stdios, name)
	}
	e.cfg[name] = cfg
	return nil
}

// ListTools returns the tool names exposed by the named server, by issuing
// one tools/list JSON-RPC call. The server must be registered (in cfg) at
// call time. Spawns the subprocess if it is not yet running.
func (e *MCPExecutor) ListTools(ctx context.Context, name string) ([]string, error) {
	e.mu.Lock()
	cfg, ok := e.cfg[name]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown mcp server: %s", name)
	}
	if cfg.Transport != "stdio" {
		return nil, fmt.Errorf("ListTools: only stdio supported, got %q", cfg.Transport)
	}
	c, err := e.connStdio(name, cfg)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := atomic.AddInt64(&c.nextID, 1)
	req := map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": "tools/list",
		"params": map[string]interface{}{},
	}
	line, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		c.kill()
		e.mu.Lock()
		delete(e.stdios, name)
		e.mu.Unlock()
		return nil, fmt.Errorf("mcp stdio write: %w", err)
	}
	type doneT struct {
		b   []byte
		err error
	}
	ch := make(chan doneT, 1)
	go func() {
		b, rerr := c.stdout.ReadBytes('\n')
		ch <- doneT{b, rerr}
	}()
	select {
	case <-ctx.Done():
		c.kill()
		e.mu.Lock()
		delete(e.stdios, name)
		e.mu.Unlock()
		return nil, fmt.Errorf("timeout")
	case d := <-ch:
		if d.err != nil {
			return nil, fmt.Errorf("mcp stdio read: %w", d.err)
		}
		var raw struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(d.b, &raw); err != nil {
			return nil, fmt.Errorf("mcp parse: %w", err)
		}
		if raw.Error != nil {
			return nil, fmt.Errorf("%s", raw.Error.Message)
		}
		out := make([]string, 0, len(raw.Result.Tools))
		for _, t := range raw.Result.Tools {
			out = append(out, t.Name)
		}
		return out, nil
	}
}
```

- [ ] **Step 5: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/
```
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/executor/ multi-agent/testdata/fake-mcp-stdio/
git commit -m "$(cat <<'EOF'
feat(executor/mcp): RegisterStdio + ListTools

RegisterStdio is the hot-add path used by BuildMCPExecutor after a
new generated server is validated. Replaces the prior subprocess if
the same name was already registered (so version bumps work).

ListTools issues one tools/list JSON-RPC call against an active
stdio server; tunnel.PublishCard uses it to enumerate the agent
card's tools[] field.

Extends testdata/fake-mcp-stdio to dispatch on JSON-RPC `method`
(was only handling tools/call).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Python imports allow-list validator

**Files:**
- Create: `multi-agent/internal/executor/importsallowlist.go`
- Create: `multi-agent/internal/executor/importsallowlist_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/internal/executor/importsallowlist_test.go`:

```go
package executor

import (
	"reflect"
	"testing"
)

func TestImportsValidate_AllStdlib(t *testing.T) {
	src := "import json\nimport os\nfrom urllib.parse import quote\n"
	bad, err := ValidateImports(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("unexpected disallowed: %v", bad)
	}
}

func TestImportsValidate_AllowedPackage(t *testing.T) {
	src := "import requests\nfrom PIL import Image\n"
	bad, err := ValidateImports(src, []string{"requests", "pillow"})
	if err != nil {
		t.Fatal(err)
	}
	// Note: top-level package name is what we check; "from PIL import" means
	// top-level "PIL", which the allow-list resolves via lowercase to "pillow".
	if len(bad) != 0 {
		t.Fatalf("unexpected disallowed: %v", bad)
	}
}

func TestImportsValidate_Disallowed(t *testing.T) {
	src := "import json\nimport requests_html\nimport notapackage\n"
	bad, err := ValidateImports(src, []string{"requests"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"notapackage", "requests_html"}
	if !reflect.DeepEqual(bad, want) {
		t.Fatalf("bad = %v, want %v", bad, want)
	}
}

func TestImportsValidate_FromImportSubmodule(t *testing.T) {
	src := "from os.path import join\n"
	bad, _ := ValidateImports(src, nil)
	if len(bad) != 0 {
		t.Fatalf("unexpected disallowed for stdlib submodule: %v", bad)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run ImportsValidate
```
Expected: `ValidateImports` undefined.

- [ ] **Step 3: Write the implementation**

Create `multi-agent/internal/executor/importsallowlist.go`:

```go
package executor

import (
	"regexp"
	"sort"
	"strings"
)

// ValidateImports parses src for top-level Python `import X` and
// `from X[.Y] import Z` statements, extracts each top-level module name,
// and returns those that are neither in pythonStdlib nor in allowed.
// allowed values are matched against module names with package-name
// equivalence: e.g., the import "PIL" is satisfied by allowed entry
// "pillow" (the wheel name), the import "yaml" by "pyyaml", etc.
//
// Returns the disallowed module names sorted alphabetically, or empty
// if the source is acceptable. The bool error return is reserved for
// future syntax-level rejection — currently always nil.
func ValidateImports(src string, allowed []string) ([]string, error) {
	allowSet := make(map[string]bool, len(allowed)+len(packageAliases))
	for _, a := range allowed {
		allowSet[strings.ToLower(a)] = true
		// Map wheel name back to import name(s) via packageAliases.
		for impName, wheel := range packageAliases {
			if strings.EqualFold(wheel, a) {
				allowSet[strings.ToLower(impName)] = true
			}
		}
	}
	mods := extractTopLevelModules(src)
	bad := []string{}
	for m := range mods {
		lm := strings.ToLower(m)
		if pythonStdlib[lm] {
			continue
		}
		if allowSet[lm] {
			continue
		}
		bad = append(bad, m)
	}
	sort.Strings(bad)
	return bad, nil
}

var importRe = regexp.MustCompile(`(?m)^\s*(?:import\s+([A-Za-z_][\w.]*)|from\s+([A-Za-z_][\w.]*)\s+import\s)`)

func extractTopLevelModules(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range importRe.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		// Top-level package only.
		if i := strings.Index(name, "."); i > 0 {
			name = name[:i]
		}
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// packageAliases maps Python import names to their pip/wheel package names
// where the two differ. Used so that allowed_packages in a build_mcp spec
// can use the wheel name (more familiar) while we still allow the
// corresponding import.
var packageAliases = map[string]string{
	"PIL":           "pillow",
	"yaml":          "pyyaml",
	"cv2":           "opencv-python",
	"sklearn":       "scikit-learn",
	"bs4":           "beautifulsoup4",
	"dateutil":      "python-dateutil",
	"google":        "google-api-python-client",
	"OpenSSL":       "pyopenssl",
}

// pythonStdlib lists Python 3.11 standard-library top-level module names
// (lowercased). Generated from https://docs.python.org/3.11/py-modindex.html.
// Kept in source so the validator has no runtime dependency.
var pythonStdlib = func() map[string]bool {
	names := []string{
		"__future__", "_thread", "abc", "aifc", "argparse", "array", "ast",
		"asynchat", "asyncio", "asyncore", "atexit", "audioop", "base64",
		"bdb", "binascii", "bisect", "builtins", "bz2", "calendar", "cgi",
		"cgitb", "chunk", "cmath", "cmd", "code", "codecs", "codeop",
		"collections", "colorsys", "compileall", "concurrent", "configparser",
		"contextlib", "contextvars", "copy", "copyreg", "crypt", "csv",
		"ctypes", "curses", "dataclasses", "datetime", "dbm", "decimal",
		"difflib", "dis", "distutils", "doctest", "email", "encodings",
		"ensurepip", "enum", "errno", "faulthandler", "fcntl", "filecmp",
		"fileinput", "fnmatch", "fractions", "ftplib", "functools", "gc",
		"genericpath", "getopt", "getpass", "gettext", "glob", "graphlib",
		"grp", "gzip", "hashlib", "heapq", "hmac", "html", "http", "idlelib",
		"imaplib", "imghdr", "imp", "importlib", "inspect", "io",
		"ipaddress", "itertools", "json", "keyword", "lib2to3", "linecache",
		"locale", "logging", "lzma", "mailbox", "mailcap", "marshal", "math",
		"mimetypes", "mmap", "modulefinder", "msilib", "msvcrt", "multiprocessing",
		"netrc", "nis", "nntplib", "numbers", "operator", "optparse", "os",
		"ossaudiodev", "pathlib", "pdb", "pickle", "pickletools", "pipes",
		"pkgutil", "platform", "plistlib", "poplib", "posix", "posixpath",
		"pprint", "profile", "pstats", "pty", "pwd", "py_compile", "pyclbr",
		"pydoc", "pydoc_data", "queue", "quopri", "random", "re", "readline",
		"reprlib", "resource", "rlcompleter", "runpy", "sched", "secrets",
		"select", "selectors", "shelve", "shlex", "shutil", "signal", "site",
		"smtpd", "smtplib", "sndhdr", "socket", "socketserver", "spwd",
		"sqlite3", "sre_compile", "sre_constants", "sre_parse", "ssl", "stat",
		"statistics", "string", "stringprep", "struct", "subprocess", "sunau",
		"symtable", "sys", "sysconfig", "syslog", "tabnanny", "tarfile",
		"telnetlib", "tempfile", "termios", "test", "textwrap", "threading",
		"time", "timeit", "tkinter", "token", "tokenize", "tomllib", "trace",
		"traceback", "tracemalloc", "tty", "turtle", "turtledemo", "types",
		"typing", "unicodedata", "unittest", "urllib", "uu", "uuid", "venv",
		"warnings", "wave", "weakref", "webbrowser", "winreg", "winsound",
		"wsgiref", "xdrlib", "xml", "xmlrpc", "zipapp", "zipfile", "zipimport",
		"zlib", "zoneinfo",
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[strings.ToLower(n)] = true
	}
	return m
}()
```

- [ ] **Step 4: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run ImportsValidate
```
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/executor/importsallowlist.go multi-agent/internal/executor/importsallowlist_test.go
git commit -m "$(cat <<'EOF'
feat(executor): Python imports allow-list validator

ValidateImports parses top-level `import` and `from X import` lines
out of a Python source string, extracts each top-level package name,
and returns those that are neither in the embedded Python 3.11
stdlib set nor in the caller's allowlist. Maps a few common
import-vs-wheel naming differences (PIL→pillow, yaml→pyyaml, etc.).

Used by BuildMCPExecutor to gate generated MCP servers; if the LLM
imports something not on the spec's allowed_packages list, the build
returns build_mcp_blocked so master can decide whether to expand the
allow-list and try again.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Python smoke-launch helper

**Files:**
- Create: `multi-agent/internal/executor/pythonsmoke.go`
- Create: `multi-agent/internal/executor/pythonsmoke_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/internal/executor/pythonsmoke_test.go`:

```go
package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSmokeLaunch_OK(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/list":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"tools":[{"name":"foo"}]}}), flush=True)
`
	path := filepath.Join(dir, "ok.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, err := SmokeLaunchPython(context.Background(), path, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0] != "foo" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestSmokeLaunch_NoResponse(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `
import sys, time
time.sleep(10)
` // never responds
	path := filepath.Join(dir, "hang.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SmokeLaunchPython(context.Background(), path, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestSmokeLaunch_PythonCrash(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `raise RuntimeError("intentional")`
	path := filepath.Join(dir, "boom.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SmokeLaunchPython(context.Background(), path, 2*time.Second)
	if err == nil {
		t.Fatal("expected error from crashing script")
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run SmokeLaunch
```
Expected: `SmokeLaunchPython` undefined.

- [ ] **Step 3: Write the implementation**

Create `multi-agent/internal/executor/pythonsmoke.go`:

```go
package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// SmokeLaunchPython spawns `python3 path`, sends one tools/list JSON-RPC
// request to its stdin, and waits up to timeout for a single line of stdout
// that decodes as a valid response. Returns the tool name list. Always
// kills the subprocess before returning.
func SmokeLaunchPython(ctx context.Context, path string, timeout time.Duration) ([]string, error) {
	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "python3", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	if _, err := stdin.Write(req); err != nil {
		return nil, fmt.Errorf("smoke write: %w", err)
	}
	_ = stdin.Close()

	type doneT struct {
		line []byte
		err  error
	}
	ch := make(chan doneT, 1)
	go func() {
		r := bufio.NewReaderSize(stdout, 1<<20)
		line, err := r.ReadBytes('\n')
		ch <- doneT{line, err}
	}()
	select {
	case <-subCtx.Done():
		return nil, fmt.Errorf("smoke timeout after %s", timeout)
	case d := <-ch:
		if d.err != nil {
			return nil, fmt.Errorf("smoke read: %w", d.err)
		}
		var resp struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(d.line, &resp); err != nil {
			return nil, fmt.Errorf("smoke parse: %w (body=%q)", err, string(d.line))
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("smoke server error: %s", resp.Error.Message)
		}
		out := make([]string, 0, len(resp.Result.Tools))
		for _, t := range resp.Result.Tools {
			out = append(out, t.Name)
		}
		return out, nil
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run SmokeLaunch
```
Expected: `ok` (with one or more skipped if python3 is absent).

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/executor/pythonsmoke.go multi-agent/internal/executor/pythonsmoke_test.go
git commit -m "$(cat <<'EOF'
feat(executor): SmokeLaunchPython helper

Spawns `python3 <path>`, sends a single tools/list JSON-RPC request,
waits up to a deadline for one valid response line, then kills the
subprocess. Used by BuildMCPExecutor to confirm a freshly written
generated MCP server can at least start and speak the protocol
before it gets registered for real task traffic.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: tunnel.PublishCard publishes Tools[] + Resources

**Files:**
- Modify: `multi-agent/internal/tunnel/tunnel.go`
- Modify: `multi-agent/internal/tunnel/tunnel_test.go`

- [ ] **Step 1: Inspect existing PublishCard**

```bash
sed -n '78,115p' /mnt/c/Users/DELL/multi-agent/multi-agent/internal/tunnel/tunnel.go
```

- [ ] **Step 2: Write the failing test**

Append to `multi-agent/internal/tunnel/tunnel_test.go`:

```go
func TestPublishCard_IncludesToolsAndResources(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Server:      config.Server{URL: srv.URL, Name: "n"},
		Credentials: config.Credentials{ProxyToken: "ptoken"},
		Discovery:   config.Discovery{DisplayName: "dn", Description: "d", Skills: []string{"chat", "build_mcp"}},
		Resources:   &config.Resources{Devices: []string{"camera"}, Tags: []string{"x"}},
	}
	tn := NewWithDeps(cfg, "/tmp/none", nil, Deps{})
	tn.SetTools([]string{"echo", "raise"})
	if err := tn.PublishCard(context.Background()); err != nil {
		t.Fatal(err)
	}
	card, _ := got["card"].(map[string]interface{})
	if card == nil {
		t.Fatalf("missing card: %v", got)
	}
	tools, _ := card["tools"].([]interface{})
	if len(tools) != 2 || tools[0] != "echo" || tools[1] != "raise" {
		t.Fatalf("tools = %v", tools)
	}
	res, _ := card["resources"].(map[string]interface{})
	if res == nil {
		t.Fatalf("missing resources")
	}
	devs, _ := res["devices"].([]interface{})
	if len(devs) != 1 || devs[0] != "camera" {
		t.Fatalf("devices = %v", devs)
	}
}
```

If `httptest`, `io`, `json`, `context` aren't imported in the test file, add them.

- [ ] **Step 3: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/tunnel/ -run PublishCard_IncludesToolsAndResources
```
Expected: `SetTools` undefined or `tools`/`resources` not in card body.

- [ ] **Step 4: Modify `multi-agent/internal/tunnel/tunnel.go`**

Add a `tools []string` field to the `Tunnel` struct (and an exported setter):

```go
type Tunnel struct {
	cfg     *config.Config
	cfgPath string
	http    http.Handler
	deps    Deps
	sdk     *agentsdk.Client
	tools   []string  // NEW: published in PublishCard
}

// SetTools sets the flattened MCP tool name list to include in the next
// PublishCard call. Safe to call before or after EnsureRegistered.
func (t *Tunnel) SetTools(tools []string) {
	t.tools = append([]string{}, tools...)
}
```

Replace the body-construction inside `PublishCard`:

```go
func (t *Tunnel) PublishCard(ctx context.Context) error {
	cardBody := map[string]interface{}{
		"skills":        t.cfg.Discovery.Skills,
		"tools":         t.tools, // may be empty slice; that's fine
		"accepts_tasks": true,
		"has_web_ui":    true,
		"version":       "0.1.0",
	}
	if t.cfg.Resources != nil {
		cardBody["resources"] = t.cfg.Resources
	}
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": t.cfg.Discovery.DisplayName,
		"description":  t.cfg.Discovery.Description,
		"agent_type":   "custom",
		"card":         cardBody,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.cfg.Server.URL+"/api/agent/discovery/cards", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.cfg.Credentials.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("publish card: status %d", resp.StatusCode)
	}
	return nil
}
```

If `t.tools` is `nil` at the time of marshaling, the resulting JSON will encode it as `null`. To ensure an empty array instead (so consumers don't have to handle null), initialize in `New`/`NewWithDeps`:

In `New`:
```go
return &Tunnel{cfg: cfg, cfgPath: cfgPath, http: h, tools: []string{}}
```
And similarly in `NewWithDeps`.

- [ ] **Step 5: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/tunnel/
```
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/tunnel/
git commit -m "$(cat <<'EOF'
feat(tunnel): publish card includes tools[] and resources

PublishCard now serializes the slave's flattened MCP tool name list
(set by the slave-agent main via SetTools, populated from
mcpExec.ListTools across both static and dynamic mcp_servers) and
the optional Resources block. The planner uses both to decide
whether to issue a build_mcp request and which slave should fulfill
it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Planner prompt extension

**Files:**
- Modify: `multi-agent/internal/planner/prompts.go`
- Modify: `multi-agent/internal/planner/prompts_test.go` (or `planner_test.go`, whichever houses prompt assertions)

- [ ] **Step 1: Inspect existing prompts and tests**

```bash
cat /mnt/c/Users/DELL/multi-agent/multi-agent/internal/planner/prompts.go
ls /mnt/c/Users/DELL/multi-agent/multi-agent/internal/planner/
```

- [ ] **Step 2: Write the failing test**

Append to the appropriate planner test file (create `multi-agent/internal/planner/prompts_test.go` if it doesn't exist):

```go
package planner

import (
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestPlanPrompt_MentionsBuildMCPAndKindSkill(t *testing.T) {
	p := planPrompt("do something", []agentsdk.AgentCard{
		{AgentID: "a1", DisplayName: "x", Description: "y"},
	})
	for _, want := range []string{
		"build_mcp",          // skill name appears
		`kind: "build_mcp"`,  // node attribute documented
		`skill: "mcp"`,       // phase-2 use-node guidance
		"BUILD_MCP_BLOCKED",  // negotiation marker
		"resources",          // resources keyword referenced
		"tools",              // tools field referenced
	} {
		if !strings.Contains(p, want) {
			t.Errorf("planPrompt missing %q", want)
		}
	}
}

func TestAgentsJSON_IncludesToolsAndResources(t *testing.T) {
	cards := []agentsdk.AgentCard{
		{
			AgentID:     "a1",
			DisplayName: "n1",
			Description: "d",
			Card:        []byte(`{"tools":["echo","raise"],"resources":{"devices":["camera"]}}`),
		},
	}
	out := agentsJSON(cards)
	for _, want := range []string{"echo", "raise", "camera"} {
		if !strings.Contains(out, want) {
			t.Errorf("agentsJSON missing %q in:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 3: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/planner/ -run "PlanPrompt_MentionsBuildMCP|AgentsJSON_IncludesTools"
```
Expected: missing strings / agentsJSON not exposing card content.

- [ ] **Step 4: Modify `multi-agent/internal/planner/prompts.go`**

Replace `agentsJSON` with a version that flattens each card's inner JSON:

```go
func agentsJSON(agents []agentsdk.AgentCard) string {
	type lite struct {
		AgentID     string                 `json:"agent_id"`
		DisplayName string                 `json:"display_name"`
		Description string                 `json:"description"`
		Status      string                 `json:"status"`
		Tools       []string               `json:"tools,omitempty"`
		Resources   map[string]interface{} `json:"resources,omitempty"`
	}
	out := make([]lite, len(agents))
	for i, a := range agents {
		out[i] = lite{
			AgentID:     a.AgentID,
			DisplayName: a.DisplayName,
			Description: a.Description,
			Status:      a.Status,
		}
		if len(a.Card) > 0 {
			var inner struct {
				Tools     []string               `json:"tools"`
				Resources map[string]interface{} `json:"resources"`
			}
			_ = json.Unmarshal(a.Card, &inner)
			out[i].Tools = inner.Tools
			out[i].Resources = inner.Resources
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}
```

Replace `planPrompt` to add the build_mcp guidance block AFTER the existing instructions:

```go
func planPrompt(taskPrompt string, agents []agentsdk.AgentCard) string {
	return fmt.Sprintf(`You are a task decomposer. Break the following task into a DAG of 1 to 20 sub-tasks. Output a JSON array; each element has:
  - "id": short unique node name (e.g. "n1")
  - "target_id": from the available agents list (the agent_id field)
  - "skill": which executor on the slave should handle it (see below)
  - "kind": optional special node kind (currently only "build_mcp")
  - "prompt": the sub-task text. May reference an upstream node's output via {{X.output}}, where X must appear in depends_on.
  - "depends_on": array of upstream node ids; empty for root nodes.

Constraints: no cycles. At least one root. Prefer a clear convergence (one or few sink nodes).

Each agent card lists "skills", "tools" (a flattened list of MCP tool names the agent currently exposes), and "resources" (free-form hardware/runtime info: cpu, gpu, memory_gb, devices, tags). When deciding the DAG:

1. If the work needs a tool that some agent already lists in "tools", emit a node targeting that agent. Set "skill": "mcp" and write the prompt as JSON {"server":"<server-name>","tool":"<tool-name>","args":{...}} so the slave's mcp executor handles it directly. Omit "kind".

2. If no agent lists the needed tool but at least one agent has skill "build_mcp" and resources matching the requirement, you MAY emit a sub-task with "kind": "build_mcp" AND "skill": "build_mcp". The prompt MUST be a JSON spec:
       {"name":"<lower_snake>", "description":"...",
        "tools":[{"name":"...","description":"...","args_schema":{...},
                  "result_description":"..."}],
        "hints":"...", "allowed_packages":["..."], "compose_servers":[],
        "version":1, "iteration":1, "max_iterations":3}
   Emit ONLY the build node — do NOT also emit the use nodes in this plan. The orchestrator schedules the build, then automatically calls you again with the agent's updated "tools" list and you plan the use phase then.

3. If a build_mcp sub-task returns an output handle of type "build_mcp_blocked", I will call you again with the blocked output appended to the original task prompt under the marker "BUILD_MCP_BLOCKED: ...". You may:
   (a) emit a new build_mcp node with expanded allowed_packages, or
   (b) emit a new build_mcp node with revised hints/spec, or
   (c) abandon — emit a single chat-skill node that explains the failure to the user.
   Iteration is bounded at 3 globally; after that I fail the master task.

4. Match resources sensibly. A camera-required tool goes to a slave with devices:[camera]. Heavy compute goes to one with gpu. Use the resource fields literally — they are not a fixed schema.

5. For ordinary chat sub-tasks, omit "skill" (or set it to ""); the slave dispatches to its claude executor.

Output ONLY the JSON array, no commentary.

Task:
%s

Available agents:
%s
`, taskPrompt, agentsJSON(agents))
}
```

(If the existing `routePrompt` and `reducePrompt` reference `agentsJSON`, they continue to work because the function's signature is unchanged.)

- [ ] **Step 5: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/planner/
```
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/planner/
git commit -m "$(cat <<'EOF'
feat(planner): prompt teaches build_mcp + threads tools/resources

agentsJSON now flattens each agent card's tools[] and resources
into the lite view that goes into the planner prompt. planPrompt
documents the build_mcp two-phase pattern, the BUILD_MCP_BLOCKED
negotiation marker, the kind/skill node attributes, and the
resources matching guidance.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Orchestrator — Skill threading + Scheduler.Append

**Files:**
- Modify: `multi-agent/internal/orchestrator/dag.go`
- Modify: `multi-agent/internal/orchestrator/dag_test.go`
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Modify: `multi-agent/internal/orchestrator/fanout_test.go`

- [ ] **Step 1: Write a failing test for Scheduler.Append**

Append to `multi-agent/internal/orchestrator/dag_test.go`:

```go
func TestScheduler_Append(t *testing.T) {
	initial := []planner.Node{
		{ID: "n0", TargetID: "a", Prompt: "p0"},
	}
	s := NewScheduler(initial, 4)
	r := s.Ready()
	if len(r) != 1 || r[0].ID != "n0" {
		t.Fatalf("initial Ready = %v", r)
	}
	s.MarkDispatched("n0")
	s.Report("n0", "completed", "out0", "")

	// Append two new nodes; one depends on n0, one on the other.
	more := []planner.Node{
		{ID: "n1", TargetID: "b", Prompt: "p1", DependsOn: []string{"n0"}},
		{ID: "n2", TargetID: "c", Prompt: "p2", DependsOn: []string{"n1"}},
	}
	if err := s.Append(more); err != nil {
		t.Fatal(err)
	}
	r = s.Ready()
	if len(r) != 1 || r[0].ID != "n1" {
		t.Fatalf("after append Ready = %v", r)
	}
}

func TestScheduler_Append_RejectsDuplicateID(t *testing.T) {
	s := NewScheduler([]planner.Node{{ID: "n0", TargetID: "a", Prompt: "p"}}, 4)
	err := s.Append([]planner.Node{{ID: "n0", TargetID: "b", Prompt: "p"}})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/orchestrator/ -run Scheduler_Append
```
Expected: `Append` undefined.

- [ ] **Step 3: Implement Append**

Append to `multi-agent/internal/orchestrator/dag.go`:

```go
// Append adds new nodes to a running scheduler. Used by runFanout's
// phase-boundary handler when re-planning after a build_mcp completes.
//
// Caller is responsible for unique node ids; Append errors if any new node
// shares an id with an existing one. depends_on may reference either
// already-appended ids or pre-existing ids (including completed ones).
func (s *Scheduler) Append(nodes []planner.Node) error {
	for _, n := range nodes {
		if _, exists := s.nodeByID[n.ID]; exists {
			return fmt.Errorf("Scheduler.Append: duplicate id %q", n.ID)
		}
	}
	for _, n := range nodes {
		s.nodes = append(s.nodes, n)
		s.nodeByID[n.ID] = n
		s.pending[n.ID] = true
		for _, d := range n.DependsOn {
			s.rev[d] = append(s.rev[d], n.ID)
		}
	}
	return nil
}
```

(`fmt` may need to be added to the file's imports.)

- [ ] **Step 4: Run dag tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/orchestrator/ -run Scheduler
```
Expected: `ok`.

- [ ] **Step 5: Write a failing test for Skill threading**

Append to `multi-agent/internal/orchestrator/fanout_test.go`. (If the file doesn't yet exist, create it with the package + imports needed by the existing tests; check existing tests first to see the test fixtures.)

```go
func TestFanout_PassesNodeSkillToDelegateTask(t *testing.T) {
	// Use whatever fake SDK is already in fanout_test.go to capture the
	// DelegateTask requests the orchestrator emits. If no fake exists, build
	// a minimal one matching SDKDelegator (3 methods).

	// Single-node DAG with skill="mcp"
	plan := []planner.Node{
		{ID: "n0", TargetID: "tgt", Prompt: `{"server":"x","tool":"y"}`, Skill: "mcp"},
	}
	fakeSDK := newCapturingSDK([]agentsdk.AgentCard{{AgentID: "tgt", Status: "available"}}, plan)
	o := buildOrchestratorWithFakes(t, fakeSDK)

	res, err := o.runFanout(context.Background(), executor.Task{ID: "t1", Skill: "fanout", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	if len(fakeSDK.delegateCalls) != 1 {
		t.Fatalf("got %d DelegateTask calls", len(fakeSDK.delegateCalls))
	}
	got := fakeSDK.delegateCalls[0]
	if got.Skill != "mcp" {
		t.Fatalf("Skill = %q, want %q", got.Skill, "mcp")
	}
}
```

If `newCapturingSDK` and `buildOrchestratorWithFakes` aren't already in the test file, you may need to introduce them. Look at how existing fanout tests construct an Orchestrator + fake SDK and follow that pattern. The fake SDK's `DelegateTask` should capture requests to `delegateCalls []agentsdk.DelegateTaskRequest` and immediately return a status that lets WaitForTask resolve to "completed" with empty output. The fake planner should return `plan` from `Plan()`.

- [ ] **Step 6: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/orchestrator/ -run PassesNodeSkill
```
Expected: failure showing `Skill=""` because orchestrator currently doesn't thread the field.

- [ ] **Step 7: Modify `runFanout` in fanout.go to pass `Skill: n.Skill`**

Locate the existing `o.sdk.DelegateTask(...)` call in the dispatched goroutine. Add `Skill: n.Skill,` to the request:

```go
resp, err := o.sdk.DelegateTask(fanoutCtx, agentsdk.DelegateTaskRequest{
	TargetID:       n.TargetID,
	Skill:          n.Skill, // NEW
	Prompt:         prompt,
	TimeoutSeconds: o.cfg.SubTaskDefaults.TimeoutSec,
})
```

- [ ] **Step 8: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/orchestrator/
```
Expected: `ok`.

- [ ] **Step 9: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/orchestrator/
git commit -m "$(cat <<'EOF'
feat(orchestrator): thread Node.Skill into DelegateTask + Scheduler.Append

Threading Skill closes the gap that forced examples/image-pipeline to
bypass slave-agent entirely: a planner-emitted node with Skill:"mcp"
now lands on the slave's mcp executor instead of falling through to
claude. Scheduler.Append lets the upcoming phase-boundary handler
splice newly-planned nodes into a running DAG.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Orchestrator — phase boundary + negotiation loop

**Files:**
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Modify: `multi-agent/internal/orchestrator/fanout_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/internal/orchestrator/fanout_test.go`:

```go
func TestFanout_BuildMCPBlocked_TriggersReplan(t *testing.T) {
	// Round 1: planner emits one build_mcp node. Build returns blocked handle.
	// Round 2: planner emits a new build_mcp node (iter:2). This time the build
	//          returns mcp_tool_set. Then planner emits a use node.
	//          (Phase 2 may also emit zero nodes; for this test, return one.)

	blockedHandle := `{"type":"build_mcp_blocked","url":"","meta":{"spec_name":"foo","iteration":"1","needed_packages":"requests","reason":"r"}}`
	toolHandle := `{"type":"mcp_tool_set","url":"file:///x","meta":{"name":"foo","version":"1","tools":"a","slave_id":"sid","iteration":"2"}}`

	round := 0
	plans := [][]planner.Node{
		{ /* round 0 */ {ID: "n0", TargetID: "a", Prompt: "spec1", Kind: "build_mcp", Skill: "build_mcp"}},
		{ /* round 1 (replan after blocked) */ {ID: "n1", TargetID: "a", Prompt: "spec2", Kind: "build_mcp", Skill: "build_mcp"}},
		{ /* round 2 (replan after tool_set) */ {ID: "n2", TargetID: "a", Prompt: `{"server":"foo","tool":"a"}`, Skill: "mcp"}},
	}
	fakePlanner := newFakePlanner(func(prompt string) []planner.Node {
		out := plans[round]
		round++
		return out
	})

	// Fake SDK: n0 returns blockedHandle, n1 returns toolHandle, n2 returns "ok".
	outputs := map[string]string{"n0": blockedHandle, "n1": toolHandle, "n2": "ok"}
	fakeSDK := newCapturingSDKWithOutputs(
		[]agentsdk.AgentCard{{AgentID: "a", Status: "available"}},
		outputs,
	)

	o := buildOrchestratorWithFakesPlanner(t, fakeSDK, fakePlanner)
	res, err := o.runFanout(context.Background(), executor.Task{ID: "t", Skill: "fanout", Prompt: "do"})
	if err != nil {
		t.Fatalf("runFanout: %v", err)
	}
	_ = res
	if got := len(fakeSDK.delegateCalls); got != 3 {
		t.Fatalf("DelegateTask calls = %d, want 3 (n0, n1, n2)", got)
	}
}

func TestFanout_BuildMCPBlocked_HitsIterationCap(t *testing.T) {
	blockedHandle := `{"type":"build_mcp_blocked","url":"","meta":{"spec_name":"foo","iteration":"X","needed_packages":"y","reason":"r"}}`

	// Planner keeps emitting build_mcp nodes; never converges.
	round := 0
	fakePlanner := newFakePlanner(func(prompt string) []planner.Node {
		round++
		return []planner.Node{{ID: fmt.Sprintf("n%d", round), TargetID: "a", Prompt: "spec", Kind: "build_mcp", Skill: "build_mcp"}}
	})
	outputs := func(taskID string) string { return blockedHandle }
	fakeSDK := newCapturingSDKWithDynamicOutputs(
		[]agentsdk.AgentCard{{AgentID: "a", Status: "available"}}, outputs,
	)

	o := buildOrchestratorWithFakesPlanner(t, fakeSDK, fakePlanner)
	_, err := o.runFanout(context.Background(), executor.Task{ID: "t", Skill: "fanout", Prompt: "do"})
	if err == nil {
		t.Fatal("expected iteration-cap failure")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 build_mcp nodes were dispatched before giving up.
	if got := len(fakeSDK.delegateCalls); got != 3 {
		t.Fatalf("delegate calls = %d, want 3", got)
	}
}
```

The exact helper names (`newFakePlanner`, `newCapturingSDKWithOutputs`, `newCapturingSDKWithDynamicOutputs`, `buildOrchestratorWithFakesPlanner`) reuse / extend whatever fixtures already exist in `fanout_test.go`. Adapt to the existing patterns; the key behavior to provide is (a) a planner that returns the next plan in a sequence and (b) an SDK whose WaitForTask returns the configured output string keyed by the dispatched node's id.

- [ ] **Step 2: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/orchestrator/ -run BuildMCPBlocked
```
Expected: failure (no replan, only one DelegateTask call).

- [ ] **Step 3: Modify `multi-agent/internal/orchestrator/fanout.go`**

Add a package-level constant near the top:

```go
// maxBuildIterations bounds how many build_mcp_blocked → replan cycles the
// orchestrator will tolerate per spec name before failing the master task.
// Hardcoded for v1; documented as a future-extension in
// docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md.
const maxBuildIterations = 3
```

Add an import for `github.com/yourorg/multi-agent/pkg/transport` if not present.

In `runFanout`, locate the block that handles a completed sub-task. After the existing `outputs[d.NodeID] = d.Output` and store-update / SSE-emit code, add:

```go
		if h, ok := transport.ParseHandle(d.Output); ok {
			switch h.Type {
			case "build_mcp_blocked":
				specName := h.Meta["spec_name"]
				iterCount[specName]++
				if iterCount[specName] > maxBuildIterations {
					cancelAll()
					return executor.Result{}, fmt.Errorf(
						"build_mcp '%s' exhausted %d iterations; last need=%q reason=%q",
						specName, maxBuildIterations,
						h.Meta["needed_packages"], h.Meta["reason"])
				}
				ctx2 := t.Prompt + "\n\nBUILD_MCP_BLOCKED: " + d.Output
				newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
				if perr != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("replan after blocked: %w", perr)
				}
				if err := Validate(newPlan); err != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("invalid replan: %w", err)
				}
				if err := sched.Append(newPlan); err != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("append replan: %w", err)
				}
				appendSubTaskRows(o.store, t.ID, newPlan)

			case "mcp_tool_set":
				freshAgents, derr := o.discoverFiltered(fanoutCtx)
				if derr == nil {
					agents = freshAgents
				}
				ctx2 := t.Prompt + "\n\nBUILT: " + d.Output
				newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
				if perr != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("replan after build: %w", perr)
				}
				if err := Validate(newPlan); err != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("invalid post-build plan: %w", err)
				}
				if err := sched.Append(newPlan); err != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("append post-build plan: %w", err)
				}
				appendSubTaskRows(o.store, t.ID, newPlan)
			}
		}
```

`iterCount` is declared once at the top of `runFanout` (alongside `outputs`):

```go
iterCount := map[string]int{}
```

`appendSubTaskRows` is a small helper added in the same file:

```go
func appendSubTaskRows(s *store.Store, parentID string, nodes []planner.Node) {
	rows := make([]store.SubTaskRow, len(nodes))
	for i, n := range nodes {
		rows[i] = store.SubTaskRow{
			ParentID: parentID, NodeID: n.ID, TargetID: n.TargetID,
			Prompt: n.Prompt, DependsOn: n.DependsOn, Status: "pending",
		}
	}
	_ = s.InsertSubTasks(parentID, rows)
}
```

- [ ] **Step 4: Run all orchestrator tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/orchestrator/
```
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/orchestrator/
git commit -m "$(cat <<'EOF'
feat(orchestrator): build_mcp phase boundary + 3-iter negotiation loop

When a sub-task completes with output type "build_mcp_blocked", the
orchestrator increments a per-spec-name iteration counter, calls the
planner again with the blocked output appended (under marker
BUILD_MCP_BLOCKED:), and appends the resulting nodes to the running
scheduler. After 3 iterations per spec it gives up and fails the
master task.

When a sub-task completes with output type "mcp_tool_set", the
orchestrator re-discovers agents (so the new tool is visible) and
calls the planner with the build summary appended (marker BUILT:).
The new nodes typically include "use" nodes that depend on the now-
registered MCP tool.

Both paths reuse Scheduler.Append (Task 8) and a small
appendSubTaskRows helper for store persistence.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: webui /bridge/call handler

**Files:**
- Modify: `multi-agent/internal/webui/server.go`
- Modify: `multi-agent/internal/webui/server_test.go`

- [ ] **Step 1: Inspect existing webui mux**

```bash
sed -n '20,40p' /mnt/c/Users/DELL/multi-agent/multi-agent/internal/webui/server.go
```

- [ ] **Step 2: Write the failing test**

Append to `multi-agent/internal/webui/server_test.go`:

```go
func TestBridgeCall_DispatchesToMCPExecutor(t *testing.T) {
	// fakeMCP returns a canned response for any call.
	fake := &fakeMCPDispatcher{response: json.RawMessage(`{"result":"hello","capability_changed":false}`)}
	cfg := &config.Config{Credentials: config.Credentials{ProxyToken: "tok"}}
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	h := NewHandler(s, t.TempDir(), cfg)
	SetMCPBridge(h, fake) // exposed setter

	srv := httptest.NewServer(h)
	defer srv.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"server": "x", "tool": "echo", "args": map[string]interface{}{"a": 1},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/bridge/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, got)
	}
	var got map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["result"] != "hello" {
		t.Fatalf("got %+v", got)
	}
	if fake.lastServer != "x" || fake.lastTool != "echo" {
		t.Fatalf("dispatcher saw server=%q tool=%q", fake.lastServer, fake.lastTool)
	}
}

func TestBridgeCall_RejectsBadAuth(t *testing.T) {
	cfg := &config.Config{Credentials: config.Credentials{ProxyToken: "tok"}}
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	h := NewHandler(s, t.TempDir(), cfg)
	SetMCPBridge(h, &fakeMCPDispatcher{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/bridge/call", bytes.NewReader([]byte(`{"server":"x","tool":"y"}`)))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

type fakeMCPDispatcher struct {
	lastServer, lastTool string
	lastArgs             map[string]interface{}
	response             json.RawMessage
	err                  error
}

func (f *fakeMCPDispatcher) CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (json.RawMessage, error) {
	f.lastServer, f.lastTool, f.lastArgs = server, tool, args
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}
```

(Add missing imports: `bytes`, `context`, `encoding/json`, `filepath`, `io`, `net/http`, `net/http/httptest`, `path/filepath`, plus the local `config` and `store` packages.)

- [ ] **Step 3: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/webui/ -run BridgeCall
```
Expected: `SetMCPBridge` undefined.

- [ ] **Step 4: Modify `multi-agent/internal/webui/server.go`**

Add an interface and setter at the top:

```go
// MCPBridge is implemented by *executor.MCPExecutor (and fakes in tests).
// The webui's /bridge/call handler calls CallTool to forward generated-
// MCP-server requests back into the slave's existing MCP executor.
type MCPBridge interface {
	CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (json.RawMessage, error)
}

// SetMCPBridge attaches an MCP dispatcher to a Handler. Slave-agent
// main calls this after constructing the Handler.
func SetMCPBridge(h http.Handler, b MCPBridge) {
	if hh, ok := h.(*muxWithBridge); ok {
		hh.bridge = b
	}
}

type muxWithBridge struct {
	http.Handler
	bridge       MCPBridge
	cfg          *config.Config
}
```

Refactor `NewHandler` to wrap the existing mux:

```go
func NewHandler(s *store.Store, journalDir string, cfg *config.Config) http.Handler {
	h := &Handler{s: s, journalDir: journalDir, cfg: cfg, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/state", h.state)
	mux.HandleFunc("/tasks", h.listTasks)
	mux.HandleFunc("/tasks/", h.taskRouter)
	mux.HandleFunc("/", h.dashboard)

	wrap := &muxWithBridge{Handler: mux, cfg: cfg}
	mux.HandleFunc("/bridge/call", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+cfg.Credentials.ProxyToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Server string                 `json:"server"`
			Tool   string                 `json:"tool"`
			Args   map[string]interface{} `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if wrap.bridge == nil {
			http.Error(w, "bridge not configured", http.StatusInternalServerError)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		raw, err := wrap.bridge.CallTool(ctx, req.Server, req.Tool, req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	})
	return wrap
}
```

Add corresponding imports (`context`, `encoding/json`).

- [ ] **Step 5: Add `CallTool` to `MCPExecutor` (`internal/executor/mcp.go`)**

This makes `*MCPExecutor` satisfy `webui.MCPBridge` indirectly — it cannot be a structural import (cycle), but the slave-agent main wires them up.

```go
// CallTool is the adapter used by the webui's /bridge/call handler to let
// generated MCP servers re-enter the slave's MCP executor (compose existing
// tools). It builds the standard mcp prompt JSON and calls Run on a
// throwaway Task whose output is the tool's raw result.
func (e *MCPExecutor) CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (json.RawMessage, error) {
	cfg, ok := e.cfg[server]
	if !ok {
		return nil, fmt.Errorf("unknown mcp server: %s", server)
	}
	switch cfg.Transport {
	case "stdio":
		return e.callStdio(ctx, server, cfg, tool, args)
	case "http":
		return e.callHTTP(ctx, cfg, tool, args)
	default:
		return nil, fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
}
```

- [ ] **Step 6: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/webui/ ./internal/executor/
```
Expected: `ok`.

- [ ] **Step 7: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/webui/ multi-agent/internal/executor/mcp.go
git commit -m "$(cat <<'EOF'
feat(webui,executor): /bridge/call POST + MCPExecutor.CallTool

Adds a loopback HTTP bridge so generated MCP servers can compose the
slave's existing MCP servers. Bearer auth is the slave's
proxy_token, matching how the slave already authenticates to
agentserver. SetMCPBridge wires *executor.MCPExecutor into the
webui after both are constructed (avoiding an import cycle).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: BuildMCPExecutor

**Files:**
- Create: `multi-agent/internal/executor/buildmcp.go`
- Create: `multi-agent/internal/executor/buildmcp_test.go`
- Create: `multi-agent/testdata/fake-build-claude.sh`

- [ ] **Step 1: Create `multi-agent/testdata/fake-build-claude.sh`**

```bash
#!/usr/bin/env bash
# Fake claude that emits a deterministic minimal Python MCP server,
# wrapped in the stream-json envelope that ClaudeExecutor expects.
#
# Behavior knobs (env):
#   FAKE_BUILD_CLAUDE_MODE = ok | bad_import | bad_syntax | crash
set -euo pipefail
mode="${FAKE_BUILD_CLAUDE_MODE:-ok}"

emit_text() {
  # Print a stream-json line carrying $1 as assistant text content.
  python3 -c '
import json, sys
text = sys.argv[1]
print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":text}]}}))
print(json.dumps({"type":"result","subtype":"success"}))
' "$1"
}

case "$mode" in
  ok)
    emit_text '# placeholder header replaced by build pipeline
import sys, json
def main():
    for line in sys.stdin:
        try:
            req = json.loads(line)
        except Exception:
            continue
        method = req.get("method", "")
        rid = req.get("id", 0)
        if method == "tools/list":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"tools":[{"name":"foo"}]}}), flush=True)
        elif method == "tools/call":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"result":"called","capability_changed":False}}), flush=True)
        else:
            print(json.dumps({"jsonrpc":"2.0","id":rid,"error":{"message":"unknown method"}}), flush=True)
if __name__ == "__main__":
    main()
'
    ;;
  bad_import)
    emit_text 'import requests_html
import sys
'
    ;;
  bad_syntax)
    emit_text 'def broken(:
    pass
'
    ;;
  crash)
    echo "boom" >&2
    exit 1
    ;;
  *)
    echo "unknown FAKE_BUILD_CLAUDE_MODE: $mode" >&2
    exit 2
    ;;
esac
```

Make it executable:

```bash
chmod +x /mnt/c/Users/DELL/multi-agent/multi-agent/testdata/fake-build-claude.sh
```

- [ ] **Step 2: Write the failing tests**

Create `multi-agent/internal/executor/buildmcp_test.go`:

```go
package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/store"
)

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
		"hints":             "",
		"allowed_packages":  []string{},
		"version":           1,
		"iteration":         1,
		"max_iterations":    3,
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
	// File on disk with header.
	src, err := os.ReadFile(filepath.Join(work, "generated_mcp", "foo", "v1.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(src), "# -*- coding: utf-8 -*-\n# AUTO-GENERATED") {
		t.Fatalf("missing header:\n%s", string(src[:80]))
	}
	// dynamic_mcp.yaml has the entry.
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
func (*nopSink) Close()                {}

// Avoid unused-import warning in case store isn't otherwise used.
var _ = store.SubTaskRow{}
```

- [ ] **Step 3: Run to confirm failure**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run BuildMCP
```
Expected: `BuildMCPExecutor` undefined.

- [ ] **Step 4: Implement `multi-agent/internal/executor/buildmcp.go`**

```go
package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BuildMCPConfig wires BuildMCPExecutor to its slave-side dependencies.
type BuildMCPConfig struct {
	WorkDir   string                                 // slave's cwd; generated_mcp/ + dynamic_mcp.yaml live here
	ClaudeBin string                                 // path to claude CLI (or fake during tests)
	MCPExec   *MCPExecutor                           // for RegisterStdio after a successful build
	Republish func(ctx context.Context) error        // tunnel.PublishCard hook; called after successful register
}

type BuildMCPExecutor struct {
	cfg BuildMCPConfig

	// Exposed for tests.
	MCPExec *MCPExecutor
}

func NewBuildMCPExecutor(cfg BuildMCPConfig) *BuildMCPExecutor {
	return &BuildMCPExecutor{cfg: cfg, MCPExec: cfg.MCPExec}
}

type buildSpec struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Tools            []buildToolSpec `json:"tools"`
	Hints            string          `json:"hints"`
	AllowedPackages  []string        `json:"allowed_packages"`
	ComposeServers   []string        `json:"compose_servers"`
	Version          int             `json:"version"`
	PriorPath        string          `json:"prior_path"`
	PatchInstructions string         `json:"patch_instructions"`
	Iteration        int             `json:"iteration"`
	MaxIterations    int             `json:"max_iterations"`
}

type buildToolSpec struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ArgsSchema        json.RawMessage `json:"args_schema"`
	ResultDescription string          `json:"result_description"`
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func (e *BuildMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var spec buildSpec
	if err := json.Unmarshal([]byte(t.Prompt), &spec); err != nil {
		return Result{}, fmt.Errorf("buildmcp: malformed spec: %w", err)
	}
	if !validName.MatchString(spec.Name) {
		return Result{}, fmt.Errorf("buildmcp: invalid name %q (must match [a-z][a-z0-9_]{0,31})", spec.Name)
	}
	if len(spec.Tools) == 0 {
		return Result{}, fmt.Errorf("buildmcp: spec.tools must have at least 1 entry")
	}
	if spec.Version < 1 {
		return Result{}, fmt.Errorf("buildmcp: spec.version must be >=1")
	}
	if spec.Version >= 2 && spec.PriorPath == "" {
		return Result{}, fmt.Errorf("buildmcp: prior_path required for version>=2")
	}

	specHash := computeSpecHash(spec)

	// Idempotency: if dynamic_mcp.yaml has a matching entry that points at an
	// existing file, short-circuit.
	if existing, ok := e.lookupExistingEntry(spec.Name); ok && existing.SpecHash == specHash && existing.Version == spec.Version {
		if _, err := os.Stat(filepath.Join(e.cfg.WorkDir, existing.Args[0])); err == nil {
			return Result{Summary: e.successHandle(spec, existing.Args[0], existing.Tools, 0).Marshal()}, nil
		}
	}

	priorCode := ""
	if spec.PriorPath != "" {
		b, err := os.ReadFile(filepath.Join(e.cfg.WorkDir, spec.PriorPath))
		if err != nil {
			return Result{}, fmt.Errorf("buildmcp: read prior_path: %w", err)
		}
		priorCode = string(b)
	}

	src, err := e.invokeClaude(ctx, spec, priorCode)
	if err != nil {
		return Result{Summary: blockedHandle(spec, "", "", "claude_invocation", err.Error())}, nil
	}
	src = stripFencesAndJunk(src)

	// Validate syntax.
	if err := validatePythonSyntax(src); err != nil {
		return Result{Summary: blockedHandle(spec, "", "", "validate_syntax", err.Error())}, nil
	}
	// Validate imports.
	bad, _ := ValidateImports(src, spec.AllowedPackages)
	if len(bad) > 0 {
		return Result{Summary: blockedHandle(spec, strings.Join(bad, ","),
			"claude used "+strings.Join(bad, ", ")+" not in allowed_packages",
			"validate_imports", "")}, nil
	}
	// Write file with header.
	relPath := filepath.Join("generated_mcp", spec.Name, fmt.Sprintf("v%d.py", spec.Version))
	absPath := filepath.Join(e.cfg.WorkDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return Result{}, err
	}
	header := fmt.Sprintf(`# -*- coding: utf-8 -*-
# AUTO-GENERATED by multi-agent build_mcp at %s
# spec.name=%s  version=%d  iteration=%d
# spec_hash=sha256:%s  prior_path=%s
# DO NOT HAND-EDIT. To evolve this file, send another build_mcp task
# with version=N+1, prior_path=this file path, and patch_instructions=...
#
# This file is reserved for framework-generated MCP servers.
# Hand-written MCP servers belong elsewhere (referenced via config.yaml).

`, time.Now().UTC().Format(time.RFC3339), spec.Name, spec.Version, spec.Iteration, specHash, spec.PriorPath)
	full := []byte(header + src)
	if err := os.WriteFile(absPath, full, 0o644); err != nil {
		return Result{}, err
	}
	// Smoke launch.
	tools, err := SmokeLaunchPython(ctx, absPath, 3*time.Second)
	if err != nil {
		_ = os.Remove(absPath)
		return Result{Summary: blockedHandle(spec, "", err.Error(), "smoke_launch", "")}, nil
	}
	// Register.
	cfg := MCPServerCfg{Transport: "stdio", Command: "python3", Args: []string{absPath}}
	if err := e.cfg.MCPExec.RegisterStdio(spec.Name, cfg); err != nil {
		return Result{}, fmt.Errorf("buildmcp: register: %w", err)
	}
	// Persist to dynamic_mcp.yaml.
	if err := e.upsertDynamicYAML(dynamicEntry{
		Name: spec.Name, Transport: "stdio", Command: "python3",
		Args: []string{relPath}, Version: spec.Version,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SpecHash:  specHash, Tools: tools,
	}); err != nil {
		return Result{}, fmt.Errorf("buildmcp: persist yaml: %w", err)
	}
	// Re-publish card.
	if e.cfg.Republish != nil {
		if err := e.cfg.Republish(ctx); err != nil {
			// Non-fatal; log via the sink.
			sink.Write("warn", fmt.Sprintf("republish card: %v", err))
		}
	}
	return Result{Summary: e.successHandle(spec, relPath, tools, len(strings.Split(src, "\n"))).Marshal()}, nil
}

// successHandle builds the mcp_tool_set output handle.
func (e *BuildMCPExecutor) successHandle(spec buildSpec, relPath string, tools []string, lineCount int) handle {
	return handle{
		Type: "mcp_tool_set",
		URL:  "file://" + filepath.Join(e.cfg.WorkDir, relPath),
		Meta: map[string]string{
			"name":      spec.Name,
			"version":   strconv.Itoa(spec.Version),
			"tools":     strings.Join(tools, ","),
			"slave_id":  os.Getenv("SLAVE_SANDBOX_ID"), // best-effort; main may set
			"lines":     strconv.Itoa(lineCount),
			"deps":      strings.Join(spec.AllowedPackages, ","),
			"iteration": strconv.Itoa(spec.Iteration),
		},
	}
}

// blockedHandle builds the build_mcp_blocked output handle JSON string.
func blockedHandle(spec buildSpec, neededPackages, reason, stage, claudeErr string) string {
	if reason == "" && claudeErr != "" {
		reason = claudeErr
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	h := handle{
		Type: "build_mcp_blocked",
		URL:  "",
		Meta: map[string]string{
			"spec_name":       spec.Name,
			"iteration":       strconv.Itoa(spec.Iteration),
			"needed_packages": neededPackages,
			"reason":          reason,
			"stage":           stage,
		},
	}
	return h.Marshal()
}

// handle is a local mirror of pkg/transport.Handle to avoid an import cycle
// between internal/executor and pkg/transport. Field names match.
type handle struct {
	Type  string            `json:"type"`
	URL   string            `json:"url"`
	Bytes int64             `json:"bytes,omitempty"`
	MIME  string            `json:"mime,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

func (h handle) Marshal() string {
	b, _ := json.Marshal(h)
	return string(b)
}

// invokeClaude shells out to the configured claude bin with the build prompt
// on stdin, returning the assembled assistant text.
func (e *BuildMCPExecutor) invokeClaude(ctx context.Context, spec buildSpec, priorCode string) (string, error) {
	args := []string{"--print", "--output-format=stream-json"}
	cmd := exec.CommandContext(ctx, e.cfg.ClaudeBin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() {
		defer stdin.Close()
		io.WriteString(stdin, buildSystemPrompt+"\n\n")
		io.WriteString(stdin, "SPEC:\n")
		b, _ := json.MarshalIndent(spec, "", "  ")
		io.WriteString(stdin, string(b))
		if priorCode != "" {
			io.WriteString(stdin, "\n\nPRIOR CODE (revise according to patch_instructions):\n")
			io.WriteString(stdin, priorCode)
		}
		io.WriteString(stdin, "\n\nRespond with python source only. No markdown, no commentary.")
	}()
	var assembled strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Type != "assistant" {
			continue
		}
		for _, c := range msg.Message.Content {
			if c.Type == "text" {
				assembled.WriteString(c.Text)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("claude exit: %v: %s", err, stderrBuf.String())
	}
	return assembled.String(), nil
}

const buildSystemPrompt = `You are generating a Python MCP (Model Context Protocol) stdio server.
Wire format:
- Read JSON-RPC requests one per line on stdin: {"jsonrpc":"2.0","id":<int>,"method":"tools/list" | "tools/call","params":{...}}
- Write JSON responses one per line to stdout: {"jsonrpc":"2.0","id":<int>,"result":{...}} or {"jsonrpc":"2.0","id":<int>,"error":{"message":"..."}}
- For "tools/list" return {"result":{"tools":[{"name":"X"},...]}}
- For "tools/call" with params {"name":"X","arguments":{...}}, dispatch on name; return {"result":{"result":<your value>,"capability_changed":false}}
- Flush after every response.

Constraints:
- Output ONLY a complete Python file. No markdown fences. No commentary. No explanation.
- The file must be a runnable program (use 'if __name__ == "__main__":' guard).
- Limit imports to the packages declared in the spec's allowed_packages plus the Python standard library.
`

// stripFencesAndJunk removes leading markdown fences and any commentary
// before the first import or # statement.
func stripFencesAndJunk(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// drop first line and trailing fence
		if i := strings.Index(s, "\n"); i > 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSuffix(s, "```\n")
	}
	return strings.TrimSpace(s) + "\n"
}

// validatePythonSyntax shells out to `python3 -c 'ast.parse(sys.stdin.read())'`.
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

func computeSpecHash(spec buildSpec) string {
	// Canonical JSON via Marshal (Go's encoding/json sorts map keys deterministically
	// since Go 1.12). For struct fields, declaration order is canonical enough for our use.
	b, _ := json.Marshal(spec)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// dynamicEntry is the on-disk record persisted in dynamic_mcp.yaml.
type dynamicEntry struct {
	Name      string   `yaml:"-"`
	Transport string   `yaml:"transport"`
	Command   string   `yaml:"command"`
	Args      []string `yaml:"args"`
	Version   int      `yaml:"version"`
	CreatedAt string   `yaml:"created_at"`
	SpecHash  string   `yaml:"spec_hash"`
	Tools     []string `yaml:"tools,omitempty"`
}

type dynamicFile struct {
	Servers map[string]dynamicEntry `yaml:"servers"`
}

func (e *BuildMCPExecutor) yamlPath() string {
	return filepath.Join(e.cfg.WorkDir, "dynamic_mcp.yaml")
}

func (e *BuildMCPExecutor) lookupExistingEntry(name string) (dynamicEntry, bool) {
	df, err := e.readDynamicYAML()
	if err != nil {
		return dynamicEntry{}, false
	}
	d, ok := df.Servers[name]
	return d, ok
}

func (e *BuildMCPExecutor) readDynamicYAML() (dynamicFile, error) {
	b, err := os.ReadFile(e.yamlPath())
	if err != nil {
		if os.IsNotExist(err) {
			return dynamicFile{Servers: map[string]dynamicEntry{}}, nil
		}
		return dynamicFile{}, err
	}
	var df dynamicFile
	if err := yaml.Unmarshal(b, &df); err != nil {
		return dynamicFile{}, err
	}
	if df.Servers == nil {
		df.Servers = map[string]dynamicEntry{}
	}
	return df, nil
}

func (e *BuildMCPExecutor) upsertDynamicYAML(entry dynamicEntry) error {
	df, err := e.readDynamicYAML()
	if err != nil {
		return err
	}
	df.Servers[entry.Name] = entry
	out, err := yaml.Marshal(df)
	if err != nil {
		return err
	}
	tmp := e.yamlPath() + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.yamlPath())
}
```

- [ ] **Step 5: Run tests**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go test ./internal/executor/ -run BuildMCP
```
Expected: `ok` (with skips if python3 absent).

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/internal/executor/buildmcp.go multi-agent/internal/executor/buildmcp_test.go multi-agent/testdata/fake-build-claude.sh
git commit -m "$(cat <<'EOF'
feat(executor): BuildMCPExecutor

The slave-side workhorse for the dynamic-mcp feature. Parses a spec
JSON from the task prompt, optionally loads prior code, invokes
claude (real or fake) with a Python MCP-server skeleton in the
system prompt, validates the response (syntax + imports against
allowed_packages), smoke-launches via tools/list, registers via
mcpExec.RegisterStdio, persists to dynamic_mcp.yaml, and re-publishes
the agent card. Soft failures (LLM mistakes) return a
build_mcp_blocked handle so the orchestrator can re-call the
planner; hard failures (malformed spec, OS errors) return a Go
error to fail the sub-task normally.

testdata/fake-build-claude.sh provides 4 modes (ok, bad_import,
bad_syntax, crash) for the unit tests.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Wire build_mcp into slave-agent

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Modify: `multi-agent/cmd/slave-agent/config.example.yaml`

- [ ] **Step 1: Inspect existing slave-agent main**

```bash
cat /mnt/c/Users/DELL/multi-agent/multi-agent/cmd/slave-agent/main.go
```

- [ ] **Step 2: Modify `multi-agent/cmd/slave-agent/main.go`**

Add helpers and wiring. The exact diff depends on the existing file; the additions are:

After loading config and constructing `mcpExec`, load `dynamic_mcp.yaml` if present and merge:

```go
	// Merge dynamic_mcp.yaml entries into mcpExec config.
	if df, err := loadDynamicMCP("dynamic_mcp.yaml"); err == nil {
		for name, entry := range df.Servers {
			mcpCfg[name] = executor.MCPServerCfg{
				Transport: entry.Transport, Command: entry.Command, Args: entry.Args,
			}
		}
	}
```

Add `loadDynamicMCP` to `main.go` (or a new small file under `cmd/slave-agent`):

```go
type dynamicMCPFile struct {
	Servers map[string]struct {
		Transport string   `yaml:"transport"`
		Command   string   `yaml:"command"`
		Args      []string `yaml:"args"`
	} `yaml:"servers"`
}

func loadDynamicMCP(path string) (*dynamicMCPFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var df dynamicMCPFile
	if err := yaml.Unmarshal(b, &df); err != nil {
		return nil, err
	}
	if df.Servers == nil {
		df.Servers = map[string]struct {
			Transport string   `yaml:"transport"`
			Command   string   `yaml:"command"`
			Args      []string `yaml:"args"`
		}{}
	}
	return &df, nil
}
```

Wire BuildMCPExecutor into the routes if `discovery.skills` includes `build_mcp`:

```go
	routes := map[string]executor.Executor{
		"mcp": mcpExec,
		"":    claudeExec,
	}
	hasBuildMCP := false
	for _, s := range cfg.Discovery.Skills {
		if s == "build_mcp" {
			hasBuildMCP = true
			break
		}
	}
	if hasBuildMCP {
		workdir, _ := os.Getwd()
		buildExec := executor.NewBuildMCPExecutor(executor.BuildMCPConfig{
			WorkDir:   workdir,
			ClaudeBin: cfg.Claude.Bin,
			MCPExec:   mcpExec,
			Republish: func(ctx context.Context) error { return tn.PublishCard(ctx) },
		})
		routes["build_mcp"] = buildExec
	}
```

After `tn := tunnel.New(...)`, call `SetMCPBridge` and seed initial tools:

```go
	webui.SetMCPBridge(ui, mcpExec)

	// Initial tools enumeration: best-effort. Spawn each stdio server briefly
	// to call tools/list; skip those that fail.
	initTools := []string{}
	enumCtx, enumCancel := context.WithTimeout(ctx, 10*time.Second)
	for name, sc := range mcpCfg {
		if sc.Transport != "stdio" {
			continue
		}
		if names, err := mcpExec.ListTools(enumCtx, name); err == nil {
			initTools = append(initTools, names...)
		}
	}
	enumCancel()
	tn.SetTools(initTools)
```

Add necessary imports: `gopkg.in/yaml.v3`, `os`, `time` (if not present).

- [ ] **Step 3: Modify `multi-agent/cmd/slave-agent/config.example.yaml`**

Append at the end (or at an appropriate place after `discovery:`):

```yaml
# Optional: hardware / runtime resources advertised on the agent card.
# All fields optional. The planner uses these literally to pick a slave for
# build_mcp tasks (e.g., a "needs camera" tool goes to a slave that
# advertises devices: [camera]).
resources:
  cpu:
    cores: 8
    arch: x86_64
  gpu:
    count: 0
  memory_gb: 16
  devices: []
  tags: []
```

- [ ] **Step 4: Build everything**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go build ./... && go vet ./... && go test ./...
```
Expected: all green. (`go test` may skip python3-dependent tests if python3 isn't on PATH — that's OK.)

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/cmd/slave-agent/
git commit -m "$(cat <<'EOF'
feat(slave-agent): wire build_mcp + dynamic_mcp.yaml + initial tools

- Loads dynamic_mcp.yaml on startup and merges its server entries
  into the MCP executor's config.
- If discovery.skills includes "build_mcp", constructs a
  BuildMCPExecutor (with the slave's claude bin, mcpExec, and a
  republish callback into the tunnel) and routes that skill to it.
- Wires the *MCPExecutor into the webui via SetMCPBridge so the
  /bridge/call loopback endpoint works.
- Enumerates each static stdio MCP server's tools/list and seeds
  the agent card via tunnel.SetTools.
- config.example.yaml documents the optional resources: block.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Demo + e2e (`examples/dynamic-mcp/`)

**Files:**
- Create: `multi-agent/examples/dynamic-mcp/README.md`
- Create: `multi-agent/examples/dynamic-mcp/agent-builder/main.go`
- Create: `multi-agent/examples/dynamic-mcp/agent-builder/config.example.yaml`
- Create: `multi-agent/examples/dynamic-mcp/e2e-driver/main.go`
- Create: `multi-agent/examples/dynamic-mcp/scripts/e2e.sh`

- [ ] **Step 1: Create `agent-builder/main.go`**

The builder is just a normal `cmd/slave-agent` invoked with a config that declares the `build_mcp` skill. To avoid duplicating main code, this binary's main is a thin wrapper that calls the slave-agent's run entry. If `cmd/slave-agent/main.go`'s `run` is not exported, the simplest path is to have this binary import and shell out the same logic via a small re-export. Alternatively, just point the e2e script at `~/.config/multi-agent-e2e/bin/slave-agent` directly.

For simplicity: skip a separate binary — the e2e script uses the existing `cmd/slave-agent` binary with the builder's config:

Create `multi-agent/examples/dynamic-mcp/agent-builder/config.example.yaml`:

```yaml
server:
  url: https://agent.example.com
  name: agent-dynmcp-builder

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  short_id: ""

claude:
  bin: claude

mcp_servers: {}

discovery:
  display_name: dynmcp-builder
  description: |
    Builder agent. Has skill build_mcp; can author Python MCP servers
    on demand. Resources tagged "crypto" and python3-capable.
  skills:
    - chat
    - mcp
    - build_mcp

resources:
  cpu:
    cores: 4
  memory_gb: 8
  tags: [crypto, python3]
```

(No `agent-builder/main.go` is needed — the e2e script uses the existing slave-agent binary with this config.)

- [ ] **Step 2: Create `e2e-driver/main.go`**

```go
// e2e-driver for the dynamic-mcp example.
//
// Submits a fanout task to master that requires a sha256-image-parity tool
// no agent currently has. Asserts that:
//   - master task completes
//   - reducer output mentions the hash and "even" or "odd"
//   - generated_mcp/image_hash/v1.py exists in the builder's workdir with
//     the AUTO-GENERATED header
//   - dynamic_mcp.yaml has the entry
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
)

func main() {
	cfg := flag.String("config", "", "driver agent config (must have credentials)")
	target := flag.String("target-display-name", "master-dynmcp", "master display_name to discover")
	expect := flag.String("expect-agents", "dynmcp-builder", "comma-separated display_names that must also be visible")
	prompt := flag.String("prompt", `Compute SHA-256 of the image at https://example.com/foo.png and tell me whether the last hex digit is even or odd. No agent in this workspace currently has a sha256+parity tool, but the dynmcp-builder agent has skill build_mcp. Build the tool first.`, "task prompt")
	timeout := flag.Duration("timeout", 600*time.Second, "overall driver timeout")
	builderDir := flag.String("builder-dir", "", "path to builder agent's working directory (for file-existence assertions)")
	flag.Parse()

	if *cfg == "" {
		log.Fatal("--config required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := agentboot.LoadConfig(*cfg)
	if err != nil {
		log.Fatalf("driver: load config: %v", err)
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: c.Server.URL, Name: c.Server.Name})
	if c.Credentials.ProxyToken == "" {
		log.Fatal("driver: config has no credentials; register first")
	}
	cli.SetRegistration(&agentsdk.Registration{
		SandboxID:   c.Credentials.SandboxID,
		TunnelToken: c.Credentials.TunnelToken,
		ProxyToken:  c.Credentials.ProxyToken,
		WorkspaceID: c.Credentials.WorkspaceID,
		ShortID:     c.Credentials.ShortID,
	})

	want := append(strings.Split(*expect, ","), *target)
	masterID, err := waitForAgents(ctx, cli, want, *target, 60*time.Second)
	if err != nil {
		log.Fatalf("driver: discover: %v", err)
	}
	fmt.Printf("driver: master agent_id=%s\n", masterID)

	resp, err := cli.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       masterID,
		Skill:          "fanout",
		Prompt:         *prompt,
		TimeoutSeconds: int((*timeout - 60*time.Second).Seconds()),
	})
	if err != nil {
		log.Fatalf("driver: delegate: %v", err)
	}
	fmt.Printf("driver: submitted task_id=%s\n", resp.TaskID)

	info, err := cli.WaitForTask(ctx, resp.TaskID, 3*time.Second)
	if err != nil {
		log.Fatalf("driver: wait: %v", err)
	}
	if info.Status != "completed" {
		log.Fatalf("driver: master task status=%s reason=%s", info.Status, info.FailureReason)
	}
	fmt.Printf("driver: master output (len=%d): %s\n", len(info.Output), truncate(info.Output, 400))

	hexRe := regexp.MustCompile(`[0-9a-fA-F]{64}`)
	if !hexRe.MatchString(info.Output) {
		log.Fatalf("driver: reducer output missing 64-hex hash")
	}
	if !strings.Contains(strings.ToLower(info.Output), "even") && !strings.Contains(strings.ToLower(info.Output), "odd") {
		log.Fatalf("driver: reducer output missing parity word")
	}

	if *builderDir != "" {
		genFile := filepath.Join(*builderDir, "generated_mcp", "image_hash", "v1.py")
		b, err := os.ReadFile(genFile)
		if err != nil {
			log.Fatalf("driver: expected generated file %s: %v", genFile, err)
		}
		if !strings.HasPrefix(string(b), "# -*- coding: utf-8 -*-\n# AUTO-GENERATED") {
			log.Fatalf("driver: %s missing AUTO-GENERATED header", genFile)
		}
		dy, err := os.ReadFile(filepath.Join(*builderDir, "dynamic_mcp.yaml"))
		if err != nil {
			log.Fatalf("driver: expected dynamic_mcp.yaml: %v", err)
		}
		if !strings.Contains(string(dy), "image_hash:") {
			log.Fatalf("driver: dynamic_mcp.yaml missing image_hash entry")
		}
	}

	fmt.Println("OK dynamic-mcp e2e")
}

func waitForAgents(ctx context.Context, cli *agentsdk.Client, displayNames []string, target string, deadline time.Duration) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		cards, err := cli.DiscoverAgents(dctx)
		if err == nil {
			present := make(map[string]string)
			for _, c := range cards {
				present[c.DisplayName] = c.AgentID
			}
			missing := []string{}
			for _, n := range displayNames {
				if _, ok := present[n]; !ok {
					missing = append(missing, n)
				}
			}
			if len(missing) == 0 {
				return present[target], nil
			}
			fmt.Printf("driver: waiting for agents: %v\n", missing)
		} else {
			fmt.Printf("driver: discover error (will retry): %v\n", err)
		}
		select {
		case <-dctx.Done():
			return "", fmt.Errorf("timeout waiting for agents %v", displayNames)
		case <-tk.C:
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
```

- [ ] **Step 3: Create `scripts/e2e.sh`**

```bash
#!/usr/bin/env bash
# Live end-to-end test for the dynamic-mcp feature.
#
# Prerequisites (one-time):
#   - AGENTSERVER_URL set, agentserver reachable
#   - claude on PATH (logged in)
#   - go and python3 on PATH
#   - Three pre-registered config files (with credentials filled in by prior
#     interactive device-flow registration), paths supplied via env:
#       MASTER_CONFIG    config.yaml for cmd/master-agent
#       BUILDER_CONFIG   config.yaml for cmd/slave-agent (with build_mcp skill)
#       DRIVER_CONFIG    config.yaml for examples/dynamic-mcp/e2e-driver
#
# See README.md in this directory for first-time setup.
set -euo pipefail

require_env() {
  for v in AGENTSERVER_URL MASTER_CONFIG BUILDER_CONFIG DRIVER_CONFIG; do
    if [ -z "${!v:-}" ]; then
      echo "missing required env: $v" >&2
      exit 2
    fi
  done
  for cmd in go claude python3; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "missing required command: $cmd" >&2
      exit 2
    fi
  done
}
require_env

script_dir=$(cd "$(dirname "$0")" && pwd)
module_root=$(cd "$script_dir/../../.." && pwd)
cd "$module_root"

work=$(mktemp -d)
echo "work dir: $work"
pids=()
cleanup() {
  for p in "${pids[@]:-}"; do
    [ -n "$p" ] && kill "$p" 2>/dev/null || true
  done
  # Preserve generated_mcp + dynamic_mcp.yaml in the BUILDER's persistent dir
  # for inspection across runs; the README documents how to clean.
  rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/bin"
echo "building binaries..."
go build -o "$work/bin/master-agent"   ./cmd/master-agent
go build -o "$work/bin/slave-agent"    ./cmd/slave-agent
go build -o "$work/bin/e2e-driver"     ./examples/dynamic-mcp/e2e-driver

# Builder runs from the BUILDER_CONFIG's parent dir so generated_mcp/ persists.
builder_dir=$(dirname "$BUILDER_CONFIG")
master_dir=$(dirname "$MASTER_CONFIG")

echo "launching builder slave-agent (cwd=$builder_dir)..."
( cd "$builder_dir" && "$work/bin/slave-agent" "$(basename "$BUILDER_CONFIG")" ) > "$work/builder.log" 2>&1 &
pids+=($!)

echo "launching master-agent (cwd=$master_dir)..."
( cd "$master_dir" && "$work/bin/master-agent" "$(basename "$MASTER_CONFIG")" ) > "$work/master.log" 2>&1 &
pids+=($!)

echo "running e2e driver..."
set +e
"$work/bin/e2e-driver" \
  --config "$DRIVER_CONFIG" \
  --target-display-name master-dynmcp \
  --expect-agents dynmcp-builder \
  --builder-dir "$builder_dir" \
  --timeout 600s
status=$?
set -e

if [ "$status" -ne 0 ]; then
  echo "e2e FAILED. Logs:" >&2
  for log in master.log builder.log; do
    echo "--- $work/$log ---" >&2
    tail -n 80 "$work/$log" >&2 || true
  done
  exit "$status"
fi

echo "OK dynamic-mcp e2e"
```

Make executable:

```bash
chmod +x /mnt/c/Users/DELL/multi-agent/multi-agent/examples/dynamic-mcp/scripts/e2e.sh
```

- [ ] **Step 4: Create `README.md`**

```markdown
# dynamic-mcp

End-to-end example for the autonomous build-and-register loop.

## Scenario

The master receives a task that needs a tool no agent in the workspace
advertises (compute SHA-256 of an image and report parity of last hex digit).
Master's planner sees that no agent has a `sha256_parity` tool, but the
`dynmcp-builder` agent has skill `build_mcp` and resources tagged
`[crypto, python3]`. It emits a `kind: "build_mcp"` node first; the builder
authors a Python MCP server via claude, validates and registers it, and the
orchestrator re-plans phase 2 with the new tool visible. Phase 2 emits a
`skill: "mcp"` use node that calls the freshly-registered tool. The reducer
summarizes.

If the first build attempt is blocked (e.g., claude wrote `import requests`
but the spec only allowed stdlib), the orchestrator re-calls the planner
with `BUILD_MCP_BLOCKED:` context appended; the planner can expand
`allowed_packages`, revise the spec, or abandon. Iteration is bounded at 3.

## Layout

- `agent-builder/config.example.yaml` — slave-agent config with `build_mcp` skill + `resources:` advertising `tags: [crypto, python3]`
- `e2e-driver/main.go` — Go binary that DelegateTasks the master and asserts the reducer output + checks the generated file landed on disk with the AUTO-GENERATED header
- `scripts/e2e.sh` — bash wrapper that builds, launches master + builder slave, runs the driver, and reports

## First-time setup

Three pre-registered configs needed (master + builder + driver). Same flow as
`examples/image-pipeline/README.md`: copy `config.example.yaml`, set
`server.url`, run the binary once interactively to complete device-flow login,
the binary writes credentials back. Repeat for each of the three.

For master: use `cmd/master-agent/config.example.yaml` with
`discovery.display_name: master-dynmcp`.

For builder: use `examples/dynamic-mcp/agent-builder/config.example.yaml`.
**Important:** the builder's config file should live in a stable directory
because `generated_mcp/` and `dynamic_mcp.yaml` are written next to it. A
fresh tmp dir on every run defeats persistence (and forces a fresh build).

## Running

```bash
export AGENTSERVER_URL=https://your-agentserver
# No ANTHROPIC_API_KEY needed if local claude CLI is logged in.
export MASTER_CONFIG=/persistent/path/master/config.yaml
export BUILDER_CONFIG=/persistent/path/builder/config.yaml
export DRIVER_CONFIG=/persistent/path/driver/config.yaml

cd multi-agent/   # module root
./examples/dynamic-mcp/scripts/e2e.sh
```

Expected last line: `OK dynamic-mcp e2e`. On failure the script tails master
and builder logs.

## Cleaning up generated state

Re-running the e2e with the same builder config reuses the generated server
(idempotent — `spec_hash` matches). To force a rebuild from scratch:

```bash
rm -rf $(dirname "$BUILDER_CONFIG")/generated_mcp
rm -f $(dirname "$BUILDER_CONFIG")/dynamic_mcp.yaml
```

## Caveats

- Generated Python runs as a subprocess of the builder; **it is not
  sandboxed**. The framework's defenses are (a) the strict import allow-list
  and (b) the smoke-launch precondition. A determined attacker controlling
  claude's output could still cause harm. Future work: real sandboxing.
- The 3-iteration negotiation cap is hardcoded. To raise it, edit
  `internal/orchestrator/fanout.go`'s `maxBuildIterations` constant and rebuild.
- `generated_mcp/` should be git-ignored in any real deployment so generated
  code doesn't accidentally get committed.
```

- [ ] **Step 5: Verify everything builds + script parses**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go build ./... && go vet ./...
bash -n /mnt/c/Users/DELL/multi-agent/multi-agent/examples/dynamic-mcp/scripts/e2e.sh
git -C /mnt/c/Users/DELL/multi-agent ls-files --stage multi-agent/examples/dynamic-mcp/scripts/e2e.sh 2>/dev/null
```
The last command might show nothing (file untracked) — that's fine; the chmod above set the bit. Verify by `ls -la`.

- [ ] **Step 6: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/dynamic-mcp/
git commit -m "$(cat <<'EOF'
feat(examples/dynamic-mcp): demo + e2e harness

Builder slave config (skill build_mcp + resources tagged crypto),
e2e-driver Go binary that delegates an "image hash + parity" task
to master, and a bash orchestrator that builds + launches +
asserts. README documents the per-builder persistent-dir
requirement and how to wipe generated state for a fresh build.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: Final verification

- [ ] **Step 1: Build / vet / test the whole module**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent && go build ./... && go vet ./... && go test ./...
```
Expected: zero errors; every package `ok` (with `[no test files]` for binaries) or skipped if python3 absent.

- [ ] **Step 2: Search for placeholders / leakage in the new code**

```bash
grep -rn "TBD\|TODO\|salve\|FIXME" \
  multi-agent/internal/{config,executor,planner,orchestrator,tunnel,webui} \
  multi-agent/cmd/slave-agent \
  multi-agent/examples/dynamic-mcp \
  multi-agent/testdata/fake-build-claude.sh \
  || echo "clean"
```
Expected: `clean`.

- [ ] **Step 3: Verify spec deliverables checklist**

Open `docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md`. Check off each item in the "Deliverables checklist" section against what landed. If any item didn't land, surface it now (likely a missed config field or test).

- [ ] **Step 4: No commit if everything is green**

If steps 1-3 all pass without changes, this task ends without a new commit.

---

## Self-review

Spec coverage cross-check:

- ✅ Resources block in slave config → Task 1
- ✅ AgentCard tools[] + resources via PublishCard + ListTools → Tasks 3, 6
- ✅ planner.Node.Kind + Skill → Task 2
- ✅ planner prompt extension (build_mcp instructions, tools/resources visible, BUILD_MCP_BLOCKED marker) → Task 7
- ✅ BuildMCPExecutor input/output contract (mcp_tool_set / build_mcp_blocked handles, spec hash idempotency, version, prior_path, iteration) → Task 11
- ✅ Import allow-list with stdlib set → Task 4
- ✅ Smoke-launch via tools/list → Task 5
- ✅ MCPExecutor.RegisterStdio + ListTools → Task 3
- ✅ generated_mcp/<name>/v<n>.py with AUTO-GENERATED header + dynamic_mcp.yaml round-trip → Task 11 (write/persist) + Task 12 (load on startup)
- ✅ webui /bridge/call loopback for compose_servers → Task 10
- ✅ orchestrator phase boundary + 3-iter cap + Scheduler.Append + DelegateTask Skill threading → Tasks 8, 9
- ✅ slave wiring (route, dynamic load, initial tools enumeration, SetMCPBridge, Republish callback) → Task 12
- ✅ Demo + e2e (builder config, e2e-driver, scripts/e2e.sh, README) → Task 13

Type / symbol consistency:

- `transport.Handle` field names (`Type`, `URL`, `Bytes`, `MIME`, `Meta`) reused from `pkg/transport`. Task 11 mirrors them locally to avoid an import cycle (executor → transport would tangle the framework boundary).
- `MCPExecutor.RegisterStdio(name string, cfg MCPServerCfg) error` — defined Task 3, called Task 11 step 4.
- `MCPExecutor.ListTools(ctx, name) ([]string, error)` — defined Task 3, called Task 12 step 2 + Task 6 (in slave-agent main, not in PublishCard itself).
- `MCPExecutor.CallTool(ctx, server, tool, args) (json.RawMessage, error)` — defined Task 10, used by webui handler in same task.
- `BuildMCPExecutor.Run(ctx, Task, Sink) (Result, error)` — matches `executor.Executor` interface; routed by slave dispatch Task 12.
- `Scheduler.Append([]planner.Node) error` — defined Task 8, called Task 9 in fanout.go.
- `tunnel.Tunnel.SetTools(tools []string)` — defined Task 6, called Task 12.
- `webui.SetMCPBridge(http.Handler, MCPBridge)` + `MCPBridge.CallTool` — defined Task 10, called Task 12.
- `BuildMCPConfig.Republish func(context.Context) error` — defined Task 11, supplied by Task 12 (`func(ctx) error { return tn.PublishCard(ctx) }`).

`maxBuildIterations = 3` is hardcoded in `internal/orchestrator/fanout.go` (Task 9), documented in spec and README.

`packageAliases` (PIL→pillow, yaml→pyyaml, etc.) is the single source of truth for import-vs-wheel naming; Task 4 only.

No placeholders found in the plan during writing. No "similar to Task N" references — every code-bearing step has its full code.
