# Compute Packaging Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make dynamic MCP capabilities structured, validated before dispatch, and observable so master/slave execution becomes a reliable Phase 0/1 compute packaging baseline.

**Architecture:** Add a small `internal/capability` package for MCP tool descriptors and JSON-schema-lite validation. Thread descriptors through executor smoke/list, dynamic MCP persistence, tunnel publication, planner context, master fanout validation, and observer evidence. Preserve the old flat `tools[]` field for compatibility while adding structured `mcp_tools[]`.

**Tech Stack:** Go 1.x, SQLite, YAML via existing config dependencies, agentserver `agentsdk`, existing observer client/store/web packages, shell-based fake planner tests.

---

## File Structure

- Create `multi-agent/internal/capability/types.go`: shared MCP tool descriptor type, flatten helpers, agent-card extraction helpers.
- Create `multi-agent/internal/capability/schema_validate.go`: minimal object-schema validator for MCP call args.
- Create `multi-agent/internal/capability/schema_validate_test.go`: validation coverage for required fields, unknown fields, and primitive JSON types.
- Modify `multi-agent/internal/executor/pythonsmoke.go`: return descriptors from `tools/list`.
- Modify `multi-agent/internal/executor/mcp.go`: make `ListTools` return descriptors and accept `inputSchema` / `input_schema`.
- Modify `multi-agent/internal/executor/buildmcp.go`: persist descriptors, emit descriptors, and update generated-server prompt.
- Modify `multi-agent/internal/tunnel/tunnel.go`: publish both `tools[]` and `mcp_tools[]`.
- Modify `multi-agent/cmd/slave-agent/main.go`: enumerate descriptors from MCP servers, seed flat + structured card fields, and republish both.
- Modify `multi-agent/internal/planner/planner.go`: add `Optional` to `Node`.
- Modify `multi-agent/internal/planner/prompts.go`: pass structured `mcp_tools` into planner context and instruct schema-conformant MCP calls.
- Modify `multi-agent/internal/orchestrator/fanout.go`: validate MCP call nodes before dispatch and enforce required-node failure semantics.
- Modify `multi-agent/internal/observer/event.go`: add structured descriptor field and validation event constants.
- Modify `multi-agent/internal/observerstore/schema.sql` and `store.go`: persist structured descriptor and validation evidence.
- Modify `multi-agent/internal/observerweb/server.go`: expose stored validation evidence through existing API responses.
- Modify tests beside each changed package.

## Task 1: Capability Types and Schema Validator

**Files:**
- Create: `multi-agent/internal/capability/types.go`
- Create: `multi-agent/internal/capability/schema_validate.go`
- Create: `multi-agent/internal/capability/schema_validate_test.go`

- [ ] **Step 1: Write descriptor and validation tests**

Create `multi-agent/internal/capability/schema_validate_test.go`:

```go
package capability

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestValidateArgsAcceptsValidObject(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"},"output_dir":{"type":"string"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "output_dir": "/tmp/out"})
	require.NoError(t, err)
}

func TestValidateArgsRejectsMissingRequired(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required argument n")
}

func TestValidateArgsRejectsUnknownArgumentByDefault(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "put_url_128": "http://x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown argument put_url_128")
}

func TestValidateArgsAllowsAdditionalPropertiesWhenExplicit(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":true}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "extra": "ok"})
	require.NoError(t, err)
}

func TestValidateArgsRejectsWrongPrimitiveType(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"},"enabled":{"type":"boolean"}},"required":["n","enabled"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": "7", "enabled": true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument n must be integer")
}

func TestExtractFromAgentCardSupportsStructuredAndFlatTools(t *testing.T) {
	card := json.RawMessage(`{
		"tools":["legacy"],
		"mcp_tools":[{"server":"srv","name":"render","input_schema":{"type":"object"}}]
	}`)
	desc, flat := ExtractFromAgentCard(card)
	require.Equal(t, []string{"legacy"}, flat)
	require.Len(t, desc, 1)
	require.Equal(t, "srv", desc[0].Server)
	require.Equal(t, "render", desc[0].Name)
}
```

- [ ] **Step 2: Run capability tests and confirm they fail**

Run:

```bash
cd /mnt/c/users/dell/multi-agent/.claude/worktrees/http-task-observer/multi-agent
go test ./internal/capability
```

Expected: package or symbols are missing.

- [ ] **Step 3: Implement capability types**

Create `multi-agent/internal/capability/types.go`:

```go
package capability

import "encoding/json"

type MCPToolDescriptor struct {
	Server            string          `json:"server" yaml:"server"`
	Name              string          `json:"name" yaml:"name"`
	Description       string          `json:"description,omitempty" yaml:"description,omitempty"`
	InputSchema       json.RawMessage `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`
	ResultDescription string          `json:"result_description,omitempty" yaml:"result_description,omitempty"`
}

func FlatNames(tools []MCPToolDescriptor) []string {
	out := make([]string, 0, len(tools))
	seen := map[string]bool{}
	for _, t := range tools {
		if t.Name == "" || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t.Name)
	}
	return out
}

func WithServer(server string, tools []MCPToolDescriptor) []MCPToolDescriptor {
	out := make([]MCPToolDescriptor, 0, len(tools))
	for _, t := range tools {
		if t.Server == "" {
			t.Server = server
		}
		out = append(out, t)
	}
	return out
}

func ExtractFromAgentCard(card json.RawMessage) ([]MCPToolDescriptor, []string) {
	var inner struct {
		MCPTools []MCPToolDescriptor `json:"mcp_tools"`
		Tools    []string            `json:"tools"`
	}
	_ = json.Unmarshal(card, &inner)
	return inner.MCPTools, inner.Tools
}

func FindTool(tools []MCPToolDescriptor, server, name string) (MCPToolDescriptor, bool) {
	for _, t := range tools {
		if t.Server == server && t.Name == name {
			return t, true
		}
	}
	return MCPToolDescriptor{}, false
}
```

- [ ] **Step 4: Implement JSON-schema-lite validation**

Create `multi-agent/internal/capability/schema_validate.go`:

```go
package capability

import (
	"encoding/json"
	"fmt"
	"math"
)

type objectSchema struct {
	Type                 string                    `json:"type"`
	Properties           map[string]propertySchema `json:"properties"`
	Required             []string                  `json:"required"`
	AdditionalProperties interface{}               `json:"additionalProperties"`
}

type propertySchema struct {
	Type string `json:"type"`
}

func ValidateArgs(schema json.RawMessage, args map[string]interface{}) error {
	if len(schema) == 0 || string(schema) == "null" {
		return nil
	}
	var s objectSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("invalid input_schema: %w", err)
	}
	if s.Type != "" && s.Type != "object" {
		return fmt.Errorf("input_schema top-level type must be object, got %q", s.Type)
	}
	for _, name := range s.Required {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("missing required argument %s", name)
		}
	}
	allowExtra := false
	if b, ok := s.AdditionalProperties.(bool); ok {
		allowExtra = b
	} else if m, ok := s.AdditionalProperties.(map[string]interface{}); ok && len(m) > 0 {
		allowExtra = true
	}
	for name, val := range args {
		prop, known := s.Properties[name]
		if !known {
			if allowExtra {
				continue
			}
			return fmt.Errorf("unknown argument %s", name)
		}
		if err := validatePrimitive(name, prop.Type, val); err != nil {
			return err
		}
	}
	return nil
}

func validatePrimitive(name, typ string, val interface{}) error {
	switch typ {
	case "", "null":
		return nil
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("argument %s must be string", name)
		}
	case "number":
		if _, ok := val.(float64); !ok {
			return fmt.Errorf("argument %s must be number", name)
		}
	case "integer":
		n, ok := val.(float64)
		if !ok || math.Trunc(n) != n {
			return fmt.Errorf("argument %s must be integer", name)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("argument %s must be boolean", name)
		}
	case "object":
		if _, ok := val.(map[string]interface{}); !ok {
			return fmt.Errorf("argument %s must be object", name)
		}
	case "array":
		if _, ok := val.([]interface{}); !ok {
			return fmt.Errorf("argument %s must be array", name)
		}
	}
	return nil
}
```

- [ ] **Step 5: Run capability tests**

Run:

```bash
cd /mnt/c/users/dell/multi-agent/.claude/worktrees/http-task-observer/multi-agent
go test ./internal/capability
```

Expected: `ok github.com/yourorg/multi-agent/internal/capability`.

- [ ] **Step 6: Commit capability package**

Run:

```bash
git add internal/capability
git commit -m "feat(capability): add MCP tool descriptors"
```

## Task 2: Executor Tool Descriptor Enumeration

**Files:**
- Modify: `multi-agent/internal/executor/pythonsmoke.go`
- Modify: `multi-agent/internal/executor/pythonsmoke_test.go`
- Modify: `multi-agent/internal/executor/mcp.go`
- Modify: `multi-agent/internal/executor/mcp_test.go`

- [ ] **Step 1: Update smoke tests for descriptors**

In `multi-agent/internal/executor/pythonsmoke_test.go`, change the fake Python response to include descriptor fields and assert them:

```go
if req.get("method") == "tools/list":
    print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"tools":[{"name":"foo","description":"Foo tool","inputSchema":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}}]}}), flush=True)
```

Then assert:

```go
tools, err := SmokeLaunchPython(context.Background(), path, 3*time.Second)
require.NoError(t, err)
require.Len(t, tools, 1)
require.Equal(t, "foo", tools[0].Name)
require.Equal(t, "Foo tool", tools[0].Description)
require.JSONEq(t, `{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`, string(tools[0].InputSchema))
```

Add a second test for name-only compatibility:

```go
func TestSmokeLaunchPythonNameOnlyTool(t *testing.T) {
	// Build a Python script that returns {"tools":[{"name":"foo"}]}.
	// Assert SmokeLaunchPython returns one descriptor with Name "foo" and empty InputSchema.
}
```

- [ ] **Step 2: Run smoke tests and confirm they fail**

Run:

```bash
go test ./internal/executor -run 'TestSmokeLaunchPython' -count=1
```

Expected: compile failure because `SmokeLaunchPython` still returns `[]string`.

- [ ] **Step 3: Update SmokeLaunchPython**

In `multi-agent/internal/executor/pythonsmoke.go`, import capability:

```go
import "github.com/yourorg/multi-agent/internal/capability"
```

Change signature:

```go
func SmokeLaunchPython(ctx context.Context, path string, timeout time.Duration) ([]capability.MCPToolDescriptor, error)
```

Use this parse struct:

```go
var resp struct {
	Result struct {
		Tools []struct {
			Name              string          `json:"name"`
			Description       string          `json:"description"`
			InputSchema       json.RawMessage `json:"inputSchema"`
			InputSchemaSnake  json.RawMessage `json:"input_schema"`
			ResultDescription string          `json:"result_description"`
		} `json:"tools"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}
```

Convert to descriptors:

```go
out := make([]capability.MCPToolDescriptor, 0, len(resp.Result.Tools))
for _, t := range resp.Result.Tools {
	schema := t.InputSchema
	if len(schema) == 0 {
		schema = t.InputSchemaSnake
	}
	out = append(out, capability.MCPToolDescriptor{
		Name:              t.Name,
		Description:       t.Description,
		InputSchema:       schema,
		ResultDescription: t.ResultDescription,
	})
}
return out, nil
```

- [ ] **Step 4: Update MCP ListTools tests**

In `multi-agent/internal/executor/mcp_test.go`, update `TestMCPExecutor_ListTools` to assert descriptors instead of strings:

```go
tools, err := e.ListTools(ctx, "x")
require.NoError(t, err)
require.Len(t, tools, 3)
require.Equal(t, "x", tools[0].Server)
require.NotEmpty(t, tools[0].Name)
```

Add a test response using `input_schema` and assert it is parsed:

```go
require.JSONEq(t, `{"type":"object"}`, string(tools[0].InputSchema))
```

- [ ] **Step 5: Update MCPExecutor.ListTools**

In `multi-agent/internal/executor/mcp.go`, import capability and change:

```go
func (e *MCPExecutor) ListTools(ctx context.Context, name string) ([]capability.MCPToolDescriptor, error)
```

Parse `description`, `inputSchema`, `input_schema`, and `result_description`, set `Server: name`, and return descriptors.

- [ ] **Step 6: Run executor tests**

Run:

```bash
go test ./internal/executor -count=1
```

Expected: executor package passes.

- [ ] **Step 7: Commit executor enumeration**

Run:

```bash
git add internal/executor/pythonsmoke.go internal/executor/pythonsmoke_test.go internal/executor/mcp.go internal/executor/mcp_test.go
git commit -m "feat(executor): enumerate structured MCP tools"
```

## Task 3: Dynamic MCP Persists and Emits Descriptors

**Files:**
- Modify: `multi-agent/internal/executor/buildmcp.go`
- Modify: `multi-agent/internal/executor/buildmcp_test.go`

- [ ] **Step 1: Add build MCP tests for descriptor persistence**

In `multi-agent/internal/executor/buildmcp_test.go`, add assertions to the successful build test:

```go
df, err := be.readDynamicYAML()
require.NoError(t, err)
entry := df.Servers["calc"]
require.Len(t, entry.Tools, 1)
require.Equal(t, "calc", entry.Tools[0].Server)
require.Equal(t, "add", entry.Tools[0].Name)
require.JSONEq(t, `{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`, string(entry.Tools[0].InputSchema))
```

Add a compatibility test that writes old YAML:

```yaml
servers:
  legacy:
    transport: stdio
    command: python3
    args: ["legacy.py"]
    version: 1
    created_at: "2026-05-11T00:00:00Z"
    spec_hash: "abc"
    tools: ["echo"]
```

Then assert `readDynamicYAML()` converts `echo` into `MCPToolDescriptor{Name:"echo", Server:"legacy"}`.

- [ ] **Step 2: Run buildmcp tests and confirm they fail**

Run:

```bash
go test ./internal/executor -run 'TestBuildMCP|TestDynamic' -count=1
```

Expected: compile failures around `dynamicEntry.Tools []string`.

- [ ] **Step 3: Update dynamicEntry**

In `multi-agent/internal/executor/buildmcp.go`, import capability and change:

```go
Tools []capability.MCPToolDescriptor `yaml:"tools,omitempty"`
```

Update `successHandle` signature:

```go
func (e *BuildMCPExecutor) successHandle(spec buildSpec, relPath string, tools []capability.MCPToolDescriptor, lineCount int) handleJSON
```

Set the success handle `tools` meta to flat names:

```go
"tools": strings.Join(capability.FlatNames(tools), ","),
```

- [ ] **Step 4: Preserve spec schema after smoke launch**

After `SmokeLaunchPython`, merge smoke names with the build spec descriptors:

```go
declared := make([]capability.MCPToolDescriptor, 0, len(spec.Tools))
for _, t := range spec.Tools {
	declared = append(declared, capability.MCPToolDescriptor{
		Server:            spec.Name,
		Name:              t.Name,
		Description:       t.Description,
		InputSchema:       t.ArgsSchema,
		ResultDescription: t.ResultDescription,
	})
}
tools = mergeDescriptors(spec.Name, declared, tools)
```

Add helper:

```go
func mergeDescriptors(server string, declared, observed []capability.MCPToolDescriptor) []capability.MCPToolDescriptor {
	byName := map[string]capability.MCPToolDescriptor{}
	for _, t := range declared {
		if t.Server == "" {
			t.Server = server
		}
		byName[t.Name] = t
	}
	for _, t := range observed {
		if t.Server == "" {
			t.Server = server
		}
		if existing, ok := byName[t.Name]; ok {
			if len(existing.InputSchema) == 0 {
				existing.InputSchema = t.InputSchema
			}
			if existing.Description == "" {
				existing.Description = t.Description
			}
			if existing.ResultDescription == "" {
				existing.ResultDescription = t.ResultDescription
			}
			byName[t.Name] = existing
			continue
		}
		byName[t.Name] = t
	}
	out := make([]capability.MCPToolDescriptor, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	return out
}
```

- [ ] **Step 5: Update observer event emission**

Change `Event.MCPTools` usage in build success to flat names for compatibility, and add descriptor JSON to payload:

```go
e.emit(observer.Event{
	Type:          observer.EventMCPServerCreated,
	TaskID:        t.ID,
	MCPServerName: spec.Name,
	MCPTools:      capability.FlatNames(tools),
	Status:        "completed",
	Payload: buildMCPObserverPayload(map[string]interface{}{
		"mcp_tool_descriptors": tools,
	}),
})
```

- [ ] **Step 6: Update generated server system prompt**

In `buildSystemPrompt`, replace the tools/list line with:

```text
- For "tools/list" return {"result":{"tools":[{"name":"X","description":"...","inputSchema":{...}}]}}. inputSchema must match the SPEC tools[].args_schema for that tool.
```

- [ ] **Step 7: Run buildmcp tests**

Run:

```bash
go test ./internal/executor -run 'TestBuildMCP|TestDynamic|TestSmokeLaunchPython|TestMCPExecutor_ListTools' -count=1
```

Expected: tests pass.

- [ ] **Step 8: Commit dynamic MCP descriptors**

Run:

```bash
git add internal/executor/buildmcp.go internal/executor/buildmcp_test.go
git commit -m "feat(buildmcp): persist MCP tool descriptors"
```

## Task 4: Publish Structured Tool Descriptors on Agent Cards

**Files:**
- Modify: `multi-agent/internal/tunnel/tunnel.go`
- Modify: `multi-agent/internal/tunnel/tunnel_test.go`
- Modify: `multi-agent/cmd/slave-agent/main.go`

- [ ] **Step 1: Extend tunnel tests**

In `multi-agent/internal/tunnel/tunnel_test.go`, update the card publish test:

```go
tn.SetTools([]string{"echo", "raise"})
tn.SetMCPTools([]capability.MCPToolDescriptor{{
	Server:      "demo",
	Name:        "echo",
	Description: "Echo input",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
}})
```

Assert:

```go
mcpTools, _ := card["mcp_tools"].([]interface{})
require.Len(t, mcpTools, 1)
first := mcpTools[0].(map[string]interface{})
require.Equal(t, "demo", first["server"])
require.Equal(t, "echo", first["name"])
```

- [ ] **Step 2: Run tunnel tests and confirm they fail**

Run:

```bash
go test ./internal/tunnel -count=1
```

Expected: `SetMCPTools` is undefined.

- [ ] **Step 3: Implement tunnel descriptor publication**

In `multi-agent/internal/tunnel/tunnel.go`, import capability and add field:

```go
mcpTools []capability.MCPToolDescriptor
```

Add method:

```go
func (t *Tunnel) SetMCPTools(tools []capability.MCPToolDescriptor) {
	t.mcpTools = append([]capability.MCPToolDescriptor{}, tools...)
}
```

Initialize in `NewWithDeps`:

```go
return &Tunnel{cfg: cfg, cfgPath: cfgPath, http: h, deps: deps, tools: []string{}, mcpTools: []capability.MCPToolDescriptor{}}
```

Add to `cardBody`:

```go
"mcp_tools": t.mcpTools,
```

- [ ] **Step 4: Update slave enumeration**

In `multi-agent/cmd/slave-agent/main.go`, replace each enumeration block with descriptor accumulation:

```go
allDesc := []capability.MCPToolDescriptor{}
for _, name := range mcpExec.Servers() {
	if tools, err := mcpExec.ListTools(enumCtx, name); err == nil {
		allDesc = append(allDesc, tools...)
	}
}
tn.SetMCPTools(allDesc)
tn.SetTools(capability.FlatNames(allDesc))
```

Apply this in both the `Republish` callback and initial publish path.

- [ ] **Step 5: Run tunnel and slave compile tests**

Run:

```bash
go test ./internal/tunnel ./cmd/slave-agent -count=1
```

Expected: tests pass or `cmd/slave-agent` reports no test files after compiling.

- [ ] **Step 6: Commit card publication**

Run:

```bash
git add internal/tunnel/tunnel.go internal/tunnel/tunnel_test.go cmd/slave-agent/main.go
git commit -m "feat(tunnel): publish structured MCP tools"
```

## Task 5: Planner Context and Node Optionality

**Files:**
- Modify: `multi-agent/internal/planner/planner.go`
- Modify: `multi-agent/internal/planner/prompts.go`
- Modify: `multi-agent/internal/planner/planner_test.go`
- Modify: `multi-agent/internal/planner/prompts_test.go`

- [ ] **Step 1: Add planner tests**

In `multi-agent/internal/planner/planner_test.go`, extend node unmarshal test:

```go
out := `[{"id":"n1","target_id":"a","skill":"mcp","prompt":"{}","optional":true}]`
var nodes []Node
require.NoError(t, json.Unmarshal([]byte(out), &nodes))
require.True(t, nodes[0].Optional)
```

In `multi-agent/internal/planner/prompts_test.go`, add expectations:

```go
p := planPrompt("do", []agentsdk.AgentCard{{
	AgentID: "a",
	Card: []byte(`{"skills":["mcp"],"tools":["render"],"mcp_tools":[{"server":"srv","name":"render","input_schema":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}}]}`),
}})
for _, want := range []string{"mcp_tools", "input_schema", "must not invent arguments", "optional"} {
	require.Contains(t, p, want)
}
require.Contains(t, p, `"server": "srv"`)
```

- [ ] **Step 2: Run planner tests and confirm they fail**

Run:

```bash
go test ./internal/planner -count=1
```

Expected: prompt expectations fail and `Node.Optional` is absent.

- [ ] **Step 3: Add Optional to Node**

In `multi-agent/internal/planner/planner.go`:

```go
Optional bool `json:"optional,omitempty"`
```

- [ ] **Step 4: Update agentsJSON**

In `multi-agent/internal/planner/prompts.go`, import capability and include `MCPTools`:

```go
type lite struct {
	AgentID     string                           `json:"agent_id"`
	DisplayName string                           `json:"display_name"`
	Description string                           `json:"description"`
	Status      string                           `json:"status"`
	Skills      []string                         `json:"skills,omitempty"`
	Tools       []string                         `json:"tools,omitempty"`
	MCPTools    []capability.MCPToolDescriptor   `json:"mcp_tools,omitempty"`
	Resources   map[string]interface{}           `json:"resources,omitempty"`
}
```

Read from card:

```go
var inner struct {
	Skills    []string                       `json:"skills"`
	Tools     []string                       `json:"tools"`
	MCPTools  []capability.MCPToolDescriptor `json:"mcp_tools"`
	Resources map[string]interface{}         `json:"resources"`
}
```

- [ ] **Step 5: Update planPrompt instructions**

Replace the current flat-tools wording with:

```text
Each agent card lists "skills", legacy "tools", structured "mcp_tools", and "resources".
Prefer "mcp_tools" for MCP planning. For skill "mcp", the prompt MUST be JSON {"server":"<server>","tool":"<tool>","args":{...}} and args MUST conform to that tool's input_schema. Do not invent arguments outside input_schema. If the needed argument is not in the schema, emit a build_mcp evolution node instead of calling the tool with extra args.
Nodes are required by default. Set "optional": true only when the original user request can still succeed without that node.
```

- [ ] **Step 6: Run planner tests**

Run:

```bash
go test ./internal/planner -count=1
```

Expected: planner package passes.

- [ ] **Step 7: Commit planner changes**

Run:

```bash
git add internal/planner/planner.go internal/planner/prompts.go internal/planner/planner_test.go internal/planner/prompts_test.go
git commit -m "feat(planner): include structured MCP capabilities"
```

## Task 6: Master MCP Validation and Required Failure Semantics

**Files:**
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Modify: `multi-agent/internal/orchestrator/fanout_test.go`
- Modify: `multi-agent/testdata/fake-planner.sh`

- [ ] **Step 1: Extend fake planner cases**

In `multi-agent/testdata/fake-planner.sh`, add cases:

```bash
case "$MODE" in
  plan_mcp_valid)
    echo '[{"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7}}"}]'
    exit 0
    ;;
  plan_mcp_invalid_arg)
    echo '[{"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7,\"put_url_128\":\"x\"}}"}]'
    exit 0
    ;;
  plan_optional_failure)
    echo '[{"id":"required","target_id":"agent-a","prompt":"ok"},{"id":"optional","target_id":"agent-b","prompt":"fail","optional":true}]'
    exit 0
    ;;
esac
```

- [ ] **Step 2: Add fanout validation tests**

In `multi-agent/internal/orchestrator/fanout_test.go`, add:

```go
func TestFanout_RejectsInvalidMCPArgsBeforeDispatch(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{
			AgentID: "agent-a",
			Status:  "available",
			Card: json.RawMessage(`{"skills":["mcp"],"tools":["render"],"mcp_tools":[{"server":"srv","name":"render","input_schema":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}}]}`),
		}},
		queue: []agentsdk.TaskInfo{{Status: "completed", Output: "unexpected"}},
	}
	o := newOrch(t, sdk, "plan_mcp_invalid_arg")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown argument put_url_128")
	require.Len(t, sdk.dispatched, 0)
}

func TestFanout_AllowsValidMCPArgs(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{
			AgentID: "agent-a",
			Status:  "available",
			Card: json.RawMessage(`{"skills":["mcp"],"tools":["render"],"mcp_tools":[{"server":"srv","name":"render","input_schema":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}}]}`),
		}},
		queue: []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	o := newOrch(t, sdk, "plan_mcp_valid")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
}

func TestFanout_RequiredFailureFailsParentUnderBestEffort(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}, {AgentID: "agent-b", Status: "available"}},
		queue:  []agentsdk.TaskInfo{{Status: "failed", FailureReason: "boom"}},
	}
	o := newOrch(t, sdk, "plan_chain")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required node")
}
```

- [ ] **Step 3: Run fanout tests and confirm they fail**

Run:

```bash
go test ./internal/orchestrator -run 'TestFanout_RejectsInvalidMCPArgsBeforeDispatch|TestFanout_AllowsValidMCPArgs|TestFanout_RequiredFailureFailsParentUnderBestEffort' -count=1
```

Expected: invalid MCP call dispatches today and required failure remains best-effort.

- [ ] **Step 4: Implement MCP node validation helper**

In `multi-agent/internal/orchestrator/fanout.go`, import capability and add helper:

```go
type mcpCallPrompt struct {
	Server string                 `json:"server"`
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
}

func validateMCPNode(n planner.Node, agents []agentsdk.AgentCard, prompt string) error {
	if n.Skill != "mcp" {
		return nil
	}
	var p mcpCallPrompt
	if err := json.Unmarshal([]byte(prompt), &p); err != nil {
		return fmt.Errorf("mcp prompt must be JSON: %w", err)
	}
	if p.Server == "" || p.Tool == "" {
		return fmt.Errorf("mcp prompt missing server or tool")
	}
	if p.Args == nil {
		p.Args = map[string]interface{}{}
	}
	for _, a := range agents {
		if a.AgentID != n.TargetID {
			continue
		}
		descs, flat := capability.ExtractFromAgentCard(a.Card)
		if desc, ok := capability.FindTool(descs, p.Server, p.Tool); ok {
			return capability.ValidateArgs(desc.InputSchema, p.Args)
		}
		for _, name := range flat {
			if name == p.Tool {
				return nil
			}
		}
		return fmt.Errorf("target %s does not expose mcp tool %s/%s", n.TargetID, p.Server, p.Tool)
	}
	return fmt.Errorf("target agent %s not found for mcp call", n.TargetID)
}
```

- [ ] **Step 5: Call validation before dispatch**

In the ready-node loop in `runFanout`, after rendering prompt and before `dispatched(n, prompt)`:

```go
if err := validateMCPNode(n, agents, prompt); err != nil {
	sched.MarkDispatched(n.ID)
	inFlight++
	go func(n planner.Node, e error) {
		doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: e.Error()}}
	}(n, err)
	continue
}
```

This makes invalid calls enter the same failure path without reaching `DelegateTask`.

- [ ] **Step 6: Enforce required-node failures**

Build an optional map after `allNodes` is initialized and whenever replans are appended:

```go
optionalByID := map[string]bool{}
for _, n := range allNodes {
	optionalByID[n.ID] = n.Optional
}
```

After append:

```go
for _, n := range newPlan {
	optionalByID[n.ID] = n.Optional
}
```

In the non-completed branch, before `best_effort` skip logic:

```go
if !optionalByID[d.NodeID] {
	cancelAll()
	for inFlight > 0 {
		drained := <-doneCh
		inFlight--
		sched.Report(drained.NodeID, drained.Status, drained.Output, drained.Error)
		recordTerminalDone(drained)
	}
	return executor.Result{}, fmt.Errorf("required node %s %s: %s", d.NodeID, d.Status, d.Error)
}
```

Keep `all_or_nothing` behavior for optional failures if configured; `all_or_nothing` remains stricter than optional.

- [ ] **Step 7: Update old best-effort test**

Change `TestFanout_BestEffortPartialFailure` to use `plan_optional_failure` or update its expectation to required failure. Keep one explicit optional test:

```go
func TestFanout_OptionalFailureCanBeReducedUnderBestEffort(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}, {AgentID: "agent-b", Status: "available"}},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: "ok"},
			{Status: "failed", FailureReason: "optional boom"},
		},
	}
	o := newOrch(t, sdk, "plan_optional_failure")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 2)
}
```

- [ ] **Step 8: Run orchestrator tests**

Run:

```bash
go test ./internal/orchestrator -count=1
```

Expected: orchestrator package passes.

- [ ] **Step 9: Commit fanout validation**

Run:

```bash
git add internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go testdata/fake-planner.sh
git commit -m "feat(orchestrator): validate MCP calls before dispatch"
```

## Task 7: Observer Evidence for Validation and Descriptors

**Files:**
- Modify: `multi-agent/internal/observer/event.go`
- Modify: `multi-agent/internal/observerstore/schema.sql`
- Modify: `multi-agent/internal/observerstore/store.go`
- Modify: `multi-agent/internal/observerstore/store_test.go`
- Modify: `multi-agent/internal/observerweb/server.go`
- Modify: `multi-agent/internal/observerweb/server_test.go`

- [ ] **Step 1: Add observer tests for descriptor and validation payloads**

In `multi-agent/internal/observerstore/store_test.go`, add a test that ingests:

```go
observer.Event{
	Type:          observer.EventMCPServerCreated,
	WorkspaceID:   "ws",
	AgentID:       "slave-a",
	AgentRole:     observer.RoleSlave,
	TaskID:        "child-1",
	MCPServerName: "srv",
	MCPTools:      []string{"render"},
	Payload: json.RawMessage(`{"mcp_tool_descriptors":[{"server":"srv","name":"render","input_schema":{"type":"object"}}]}`),
}
```

Assert `mcp_servers.tools` stores descriptor JSON or the API model returns descriptors.

Add a second event:

```go
observer.Event{
	Type:        observer.EventMasterMCPCallValidationFailed,
	WorkspaceID: "ws",
	AgentID:     "master-a",
	AgentRole:   observer.RoleMaster,
	TaskID:      "parent-1",
	SubtaskID:   "n0",
	Status:      "failed",
	Payload: json.RawMessage(`{"validation_error":"unknown argument put_url_128","required":true}`),
}
```

Assert it is persisted in `events` and visible through the task detail API.

- [ ] **Step 2: Run observer tests and confirm they fail**

Run:

```bash
go test ./internal/observer ./internal/observerstore ./internal/observerweb -count=1
```

Expected: missing event constant and missing descriptor handling.

- [ ] **Step 3: Extend observer event model**

In `multi-agent/internal/observer/event.go`, add:

```go
EventMasterMCPCallValidationFailed = "master_mcp_call_validation_failed"
EventMasterRequiredNodeFailed      = "master_required_node_failed"
```

Add descriptor field:

```go
MCPToolDescriptors []capability.MCPToolDescriptor `json:"mcp_tool_descriptors,omitempty"`
```

Import capability.

- [ ] **Step 4: Store descriptors in mcp_servers**

In `multi-agent/internal/observerstore/schema.sql`, add a nullable column to `mcp_servers`:

```sql
tool_descriptors TEXT
```

If this repo uses explicit migrations only through schema recreation in tests, update schema and store together. If existing local DBs must be upgraded, add an `ALTER TABLE` guard in store initialization:

```go
_, _ = db.Exec(`ALTER TABLE mcp_servers ADD COLUMN tool_descriptors TEXT`)
```

- [ ] **Step 5: Update store event ingestion**

In `upsertMCPServer`, derive descriptors:

```go
descriptorBytes := []byte("null")
if len(ev.MCPToolDescriptors) > 0 {
	descriptorBytes, _ = json.Marshal(ev.MCPToolDescriptors)
} else if len(ev.Payload) > 0 {
	var payload struct {
		MCPToolDescriptors []capability.MCPToolDescriptor `json:"mcp_tool_descriptors"`
	}
	if json.Unmarshal(ev.Payload, &payload) == nil && len(payload.MCPToolDescriptors) > 0 {
		descriptorBytes, _ = json.Marshal(payload.MCPToolDescriptors)
	}
}
```

Include `tool_descriptors` in insert/update.

- [ ] **Step 6: Emit validation failure events from fanout**

In Task 6 validation failure branch, emit:

```go
o.emit(observer.Event{
	Type:           observer.EventMasterMCPCallValidationFailed,
	TaskID:         t.ID,
	SubtaskID:      n.ID,
	SubtaskSummary: observer.SummarizePrompt(prompt, 80),
	Status:         "failed",
	TargetAgentID:  n.TargetID,
	TargetRole:     observer.RoleSlave,
	Payload: observerPayload(map[string]interface{}{
		"validation_error": err.Error(),
		"required":         !n.Optional,
		"prompt":           prompt,
	}),
})
```

For required-node failure, emit `EventMasterRequiredNodeFailed` before returning.

- [ ] **Step 7: Update observer web response**

In `multi-agent/internal/observerweb/server.go`, include `tool_descriptors` in MCP server response structs and task detail payloads. Use `json.RawMessage` to avoid lossy conversions:

```go
ToolDescriptors json.RawMessage `json:"tool_descriptors,omitempty"`
```

- [ ] **Step 8: Run observer tests**

Run:

```bash
go test ./internal/observer ./internal/observerstore ./internal/observerweb -count=1
```

Expected: observer packages pass.

- [ ] **Step 9: Commit observer evidence**

Run:

```bash
git add internal/observer/event.go internal/observerstore/schema.sql internal/observerstore/store.go internal/observerstore/store_test.go internal/observerweb/server.go internal/observerweb/server_test.go internal/orchestrator/fanout.go
git commit -m "feat(observer): record MCP validation evidence"
```

## Task 8: Full Test Suite and Local End-to-End Verification

**Files:**
- Modify only if failures reveal integration gaps.
- Use existing generated configs and binaries after rebuild.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
cd /mnt/c/users/dell/multi-agent/.claude/worktrees/http-task-observer/multi-agent
go test ./internal/capability ./internal/executor ./internal/tunnel ./internal/planner ./internal/orchestrator ./internal/observer ./internal/observerstore ./internal/observerweb -count=1
```

Expected: all listed packages pass.

- [ ] **Step 2: Run broader repository tests**

Run:

```bash
go test ./...
```

Expected: all packages pass. If external contract tests require an unavailable live server, record the exact failing package and rerun the deterministic package list from Step 1.

- [ ] **Step 3: Rebuild binaries**

Run:

```bash
go build -o bin/observer-server ./cmd/observer-server
go build -o bin/driver-agent ./cmd/driver-agent
go build -o bin/master-agent ./cmd/master-agent
go build -o bin/slave-agent ./cmd/slave-agent
```

Expected: all four binaries are rebuilt.

- [ ] **Step 4: Stop current local agent processes**

The user has approved stopping current `driver-agent`, `master-agent`, and `slave-agent` during end-to-end testing. Use targeted process names:

```bash
pkill -f '/driver-agent|driver-agent'
pkill -f '/master-agent|master-agent'
pkill -f '/slave-agent|slave-agent'
```

Expected: commands may return non-zero if a process is absent. Continue if no matching process exists.

- [ ] **Step 5: Start observer, master, slave, and driver**

Use the existing registered workspace configs. Do not trigger manual device-code confirmation unless credentials are missing from config.

Run each in a separate terminal or background supervisor:

```bash
cd /mnt/c/users/dell/multi-agent/.claude/worktrees/http-task-observer/multi-agent
./bin/observer-server -config observer.yaml
./bin/master-agent -config cmd/master-agent/config.yaml
./bin/slave-agent -config cmd/slave-agent/config.yaml
./bin/driver-agent -config cmd/driver-agent/config.yaml
```

Expected:

- Observer listens on its configured HTTP address.
- Master publishes a card.
- Slave publishes a card with `mcp_tools`.
- Driver starts without device-code flow because workspace credentials already exist.

- [ ] **Step 6: Run an end-to-end dynamic MCP task**

Submit a generic reusable-capability task through the existing driver MCP flow. The task must request a reusable tool, then use it once. Avoid depending on the previous image/compression example. Use a simple deterministic capability such as a reusable text normalizer:

```text
构建一个可反复使用的文本规范化 MCP 能力：输入 text 和 mode，mode 支持 lower 和 trim。构建完成后调用它处理 "  Hello WORLD  "，返回处理结果。
```

Expected:

- Master first plans `build_mcp`.
- Slave creates an MCP server and publishes a descriptor with `input_schema`.
- Master replans and calls `skill=mcp` with only schema-declared args.
- Observer shows MCP creation, replan, subtask dispatch, and completion.

- [ ] **Step 7: Verify invalid MCP args are blocked**

Use fake planner tests for deterministic proof. For a live run, inspect observer events and confirm no slave task is dispatched for a validation-failed MCP call. Deterministic command:

```bash
go test ./internal/orchestrator -run TestFanout_RejectsInvalidMCPArgsBeforeDispatch -count=1 -v
```

Expected: test passes and dispatched request count is zero.

- [ ] **Step 8: Query observer API or DB**

Use the configured observer DB path. If it is `observer.db`:

```bash
sqlite3 observer.db "select type,status,mcp_server_name,payload from events where type in ('mcp_server_created','master_mcp_replan','master_mcp_call_validation_failed','master_required_node_failed') order by ts desc limit 20;"
```

Expected:

- `mcp_server_created` payload includes `mcp_tool_descriptors`.
- `master_mcp_replan` records build/use boundary.
- Validation failures, if any, include `validation_error`.

- [ ] **Step 9: Commit verification-only fixes if needed**

If Step 1 through Step 8 reveal integration fixes, commit them separately:

```bash
git add <changed-files>
git commit -m "test: verify compute packaging repair e2e"
```

If no files changed, do not create a commit.

## Self-Review Checklist

- Spec coverage:
  - Structured MCP descriptors: Tasks 1-4.
  - Dynamic MCP schema preservation: Tasks 2-4.
  - Planner schema-aware context: Task 5.
  - Master-side validation: Task 6.
  - Required/optional failure semantics: Task 6.
  - Observer provenance: Task 7.
  - End-to-end process and user-approved process stopping: Task 8.
- Type consistency:
  - Shared descriptor name is `capability.MCPToolDescriptor`.
  - Published structured card field is `mcp_tools`.
  - Schema field is `input_schema`; parser also accepts `inputSchema`.
  - Planner optional field is `Optional bool` with JSON key `optional`.
  - Validation event is `master_mcp_call_validation_failed`.
- Verification:
  - Each implementation task has a failing-test step, a passing-test step, and a commit step.
