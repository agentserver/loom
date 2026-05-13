# Typed BuildMCP Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `build_mcp` a typed master/slave protocol and surface structured progress for master planning, slave build work, observer views, and driver wait/status responses.

**Architecture:** Add a shared `internal/buildspec` package and validate every `build_mcp` planner node inside master before dispatching to slave. Add observer progress event types and a small progress helper so planner/build phases can emit non-terminal progress while existing completed/failed events remain terminal authority. Extend observer aggregates and driver tools so callers can distinguish latest progress from final output.

**Tech Stack:** Go, SQLite via `modernc.org/sqlite`, existing `observerclient`, existing `agentsdk`, existing Go test suite with `testify/require`.

---

## File Map

- Create `multi-agent/internal/buildspec/buildspec.go`: shared `Spec`/`ToolSpec`, validation, normalization, canonical JSON.
- Create `multi-agent/internal/buildspec/buildspec_test.go`: unit tests for spec validation and canonicalization.
- Modify `multi-agent/internal/executor/buildmcp.go`: replace local build spec validation with shared buildspec aliases, emit build progress events.
- Modify `multi-agent/internal/executor/buildmcp_test.go`: verify progress events and preserve JSON-only executor behavior.
- Modify `multi-agent/internal/planner/planner.go`: add `BuildSpec json.RawMessage` to `Node`.
- Modify `multi-agent/internal/planner/prompts.go`: prefer `build_spec` field in planner instructions.
- Create `multi-agent/internal/orchestrator/buildmcp_preflight.go`: master-side preflight/repair helpers for `build_mcp` nodes.
- Create `multi-agent/internal/orchestrator/buildmcp_preflight_test.go`: focused tests for preflight behavior.
- Modify `multi-agent/internal/orchestrator/fanout.go`: call preflight before dispatch; emit planning/build-spec progress; add planning hard timeout config path.
- Modify `multi-agent/testdata/fake-planner.sh`: add fake planner modes for valid structured spec, malformed natural language spec, and repaired spec.
- Modify `multi-agent/internal/observer/event.go`: add progress event constants and helper predicates.
- Create `multi-agent/internal/progress/progress.go`: reusable ticker/heartbeat helper for long-running operations.
- Create `multi-agent/internal/progress/progress_test.go`: unit tests for heartbeat, hard timeout, and idle timeout behavior.
- Modify `multi-agent/internal/observerstore/schema.sql`: add latest progress columns to `tasks` and `subtasks`.
- Modify `multi-agent/internal/observerstore/store.go`: migrate progress columns, aggregate progress events, expose latest progress and `is_final`.
- Modify `multi-agent/internal/observerstore/store_test.go`: progress persistence and terminal-state separation tests.
- Modify `multi-agent/internal/observerweb/server.go`: render latest progress in task tables and preserve event timeline.
- Modify `multi-agent/internal/observerweb/server_test.go`: API includes progress fields; progress events do not overwrite status.
- Create `multi-agent/internal/driver/observer_progress.go`: query observer events/tasks for latest progress.
- Modify `multi-agent/internal/driver/tools.go`: include `latest_progress`, `latest_progress_phase`, `latest_progress_at`, `is_final`, and `final_output` in `get_task`/`wait_task`.
- Modify `multi-agent/internal/driver/tools_test.go`: driver status/wait progress response tests.
- Modify `multi-agent/cmd/master-agent/config.yaml` and `multi-agent/cmd/slave-agent/config.yaml`: update existing timeout values after code supports progress reporting.

---

### Task 1: Shared BuildSpec Contract

**Files:**
- Create: `multi-agent/internal/buildspec/buildspec.go`
- Create: `multi-agent/internal/buildspec/buildspec_test.go`

- [ ] **Step 1: Write failing tests for valid, normalized, and invalid specs**

Create `internal/buildspec/buildspec_test.go`:

```go
package buildspec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseJSON_NormalizesDefaultsAndLists(t *testing.T) {
	raw := `{
		"name":" lesson_builder ",
		"description":"build lesson docs",
		"tools":[{"name":"render","description":"render doc","args_schema":{"type":"object"},"result_description":"doc path"}],
		"allowed_packages":["python-docx","python-docx"],
		"compose_servers":["textbook","textbook"]
	}`

	spec, err := ParseJSON(raw)
	require.NoError(t, err)
	require.Equal(t, "lesson_builder", spec.Name)
	require.Equal(t, 1, spec.Version)
	require.Equal(t, 1, spec.Iteration)
	require.Equal(t, 3, spec.MaxIterations)
	require.Equal(t, []string{"python-docx"}, spec.AllowedPackages)
	require.Equal(t, []string{"textbook"}, spec.ComposeServers)

	canonical, err := MarshalCanonical(spec)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"name":"lesson_builder",
		"description":"build lesson docs",
		"tools":[{"name":"render","description":"render doc","args_schema":{"type":"object"},"result_description":"doc path"}],
		"hints":"",
		"allowed_packages":["python-docx"],
		"compose_servers":["textbook"],
		"version":1,
		"iteration":1,
		"max_iterations":3
	}`, canonical)
}

func TestParseJSON_RejectsNaturalLanguage(t *testing.T) {
	_, err := ParseJSON("please build a reusable MCP server")
	require.Error(t, err)
	require.ErrorContains(t, err, "malformed build_mcp spec")
}

func TestValidate_RejectsInvalidNameAndMissingTools(t *testing.T) {
	err := Validate(Spec{Name: "Bad-Name", Version: 1, Iteration: 1, MaxIterations: 3})
	require.ErrorContains(t, err, "invalid name")

	err = Validate(Spec{Name: "ok", Version: 1, Iteration: 1, MaxIterations: 3})
	require.ErrorContains(t, err, "tools must have at least 1 entry")
}

func TestValidate_RequiresPriorPathForUpgrade(t *testing.T) {
	spec := Spec{
		Name: "lesson_builder", Description: "d", Version: 2, Iteration: 1, MaxIterations: 3,
		Tools: []ToolSpec{{Name: "render", Description: "d", ArgsSchema: json.RawMessage(`{"type":"object"}`), ResultDescription: "r"}},
	}
	err := Validate(spec)
	require.ErrorContains(t, err, "prior_path required")
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run:

```bash
go test ./internal/buildspec
```

Expected: FAIL because `internal/buildspec` does not exist.

- [ ] **Step 3: Implement shared build spec types and validation**

Create `internal/buildspec/buildspec.go`:

```go
package buildspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type Spec struct {
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Tools             []ToolSpec `json:"tools"`
	Hints             string     `json:"hints"`
	AllowedPackages   []string   `json:"allowed_packages"`
	ComposeServers    []string   `json:"compose_servers"`
	Version           int        `json:"version"`
	Iteration         int        `json:"iteration"`
	MaxIterations     int        `json:"max_iterations"`
	PriorPath         string     `json:"prior_path,omitempty"`
	PatchInstructions string     `json:"patch_instructions,omitempty"`
}

type ToolSpec struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ArgsSchema        json.RawMessage `json:"args_schema"`
	ResultDescription string          `json:"result_description"`
}

func ParseJSON(raw string) (Spec, error) {
	var spec Spec
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("malformed build_mcp spec: %w", err)
	}
	spec = Normalize(spec)
	if err := Validate(spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func Normalize(spec Spec) Spec {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Hints = strings.TrimSpace(spec.Hints)
	spec.PriorPath = strings.TrimSpace(spec.PriorPath)
	spec.PatchInstructions = strings.TrimSpace(spec.PatchInstructions)
	if spec.Version == 0 {
		spec.Version = 1
	}
	if spec.Iteration == 0 {
		spec.Iteration = 1
	}
	if spec.MaxIterations == 0 {
		spec.MaxIterations = 3
	}
	spec.AllowedPackages = cleanList(spec.AllowedPackages)
	spec.ComposeServers = cleanList(spec.ComposeServers)
	for i := range spec.Tools {
		spec.Tools[i].Name = strings.TrimSpace(spec.Tools[i].Name)
		spec.Tools[i].Description = strings.TrimSpace(spec.Tools[i].Description)
		spec.Tools[i].ResultDescription = strings.TrimSpace(spec.Tools[i].ResultDescription)
	}
	return spec
}

func Validate(spec Spec) error {
	if !validName.MatchString(spec.Name) {
		return fmt.Errorf("invalid name %q (must match [a-z][a-z0-9_]{0,31})", spec.Name)
	}
	if spec.Version < 1 {
		return fmt.Errorf("version must be >=1")
	}
	if spec.Iteration < 1 {
		return fmt.Errorf("iteration must be >=1")
	}
	if spec.MaxIterations < 1 {
		return fmt.Errorf("max_iterations must be >=1")
	}
	if spec.Version >= 2 && spec.PriorPath == "" {
		return fmt.Errorf("prior_path required for version>=2")
	}
	if len(spec.Tools) == 0 {
		return fmt.Errorf("tools must have at least 1 entry")
	}
	for i, tool := range spec.Tools {
		if tool.Name == "" {
			return fmt.Errorf("tools[%d].name required", i)
		}
		if tool.Description == "" {
			return fmt.Errorf("tools[%d].description required", i)
		}
		if len(bytes.TrimSpace(tool.ArgsSchema)) == 0 {
			return fmt.Errorf("tools[%d].args_schema required", i)
		}
		if !json.Valid(tool.ArgsSchema) {
			return fmt.Errorf("tools[%d].args_schema must be valid JSON", i)
		}
		if tool.ResultDescription == "" {
			return fmt.Errorf("tools[%d].result_description required", i)
		}
	}
	return nil
}

func MarshalCanonical(spec Spec) (string, error) {
	spec = Normalize(spec)
	if err := Validate(spec); err != nil {
		return "", err
	}
	out, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func cleanList(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run buildspec tests**

Run:

```bash
go test ./internal/buildspec
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/buildspec/buildspec.go internal/buildspec/buildspec_test.go
git commit -m "feat: add typed build mcp spec"
```

---

### Task 2: Reuse BuildSpec in BuildMCPExecutor

**Files:**
- Modify: `multi-agent/internal/executor/buildmcp.go`
- Modify: `multi-agent/internal/executor/buildmcp_test.go`

- [ ] **Step 1: Write failing executor test for shared validation error shape**

Add to `internal/executor/buildmcp_test.go`:

```go
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
```

- [ ] **Step 2: Run focused executor test**

Run:

```bash
go test ./internal/executor -run TestBuildMCP_RejectsMalformedSpecBeforeExecution
```

Expected: PASS. This locks the JSON-only executor contract before the shared-spec refactor.

- [ ] **Step 3: Replace local parse/validation with shared package**

In `internal/executor/buildmcp.go`:

1. import `github.com/yourorg/multi-agent/internal/buildspec`.
2. replace the local struct definitions with aliases so the rest of the file can be migrated in small steps:

```go
type buildSpec = buildspec.Spec
type buildToolSpec = buildspec.ToolSpec
```

Remove the local `validName` variable and the `regexp` import because validation now lives in `internal/buildspec`.

3. replace the start of `Run` with:

```go
func (e *BuildMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	spec, err := buildspec.ParseJSON(t.Prompt)
	if err != nil {
		return Result{}, fmt.Errorf("buildmcp: malformed spec: %w", err)
	}
	canonical, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		return Result{}, fmt.Errorf("buildmcp: invalid spec: %w", err)
	}
	specHash := computeSpecHashFromCanonical(canonical)
```

4. replace `computeSpecHash(spec buildSpec)` with:

```go
func computeSpecHashFromCanonical(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}
```

5. update helper signatures from `buildSpec` to `buildspec.Spec`.

- [ ] **Step 4: Run executor buildmcp tests**

Run:

```bash
go test ./internal/executor -run BuildMCP
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/executor/buildmcp.go internal/executor/buildmcp_test.go
git commit -m "refactor: use shared build mcp spec"
```

---

### Task 3: Planner Node BuildSpec Field and Prompt Contract

**Files:**
- Modify: `multi-agent/internal/planner/planner.go`
- Modify: `multi-agent/internal/planner/prompts.go`
- Modify: `multi-agent/internal/planner/planner_test.go`

- [ ] **Step 1: Add failing planner parse test**

Add to `internal/planner/planner_test.go`:

```go
func TestPlanParsesBuildSpecField(t *testing.T) {
	t.Setenv("FAKE_PLANNER_MODE", "plan_build_spec_field")
	p := New(config.Planner{Bin: fakePlannerPath(t), TimeoutSec: 5})

	nodes, err := p.Plan(context.Background(), "build reusable tool", demoAgents)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "build_mcp", nodes[0].Kind)
	require.Equal(t, "build_mcp", nodes[0].Skill)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}`, string(nodes[0].BuildSpec))
}
```

- [ ] **Step 2: Add fake planner mode**

Modify `testdata/fake-planner.sh` and add this case:

```bash
  plan_build_spec_field)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","build_spec":{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}}
]
EOF
    ;;
```

- [ ] **Step 3: Run the focused planner test and verify failure**

Run:

```bash
go test ./internal/planner -run TestPlanParsesBuildSpecField
```

Expected: FAIL because `Node.BuildSpec` does not exist.

- [ ] **Step 4: Add field and update prompt instructions**

Modify `internal/planner/planner.go`:

```go
type Node struct {
	ID        string          `json:"id"`
	TargetID  string          `json:"target_id"`
	Prompt    string          `json:"prompt"`
	BuildSpec json.RawMessage `json:"build_spec,omitempty"`
	DependsOn []string        `json:"depends_on,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Skill     string          `json:"skill,omitempty"`
	Optional  bool            `json:"optional,omitempty"`
}
```

Modify `internal/planner/prompts.go` so `build_mcp` instructions say:

```text
2. If no agent lists the needed tool but at least one agent has skill "build_mcp" and resources matching the requirement, emit a sub-task with "kind":"build_mcp", "skill":"build_mcp", and a structured "build_spec" object. Do not put natural language in "prompt" for build_mcp nodes. The master will validate "build_spec" before dispatch.
```

Keep legacy `prompt` JSON examples as "accepted for compatibility, not preferred".

- [ ] **Step 5: Run planner tests**

Run:

```bash
go test ./internal/planner
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/planner/planner.go internal/planner/prompts.go internal/planner/planner_test.go testdata/fake-planner.sh
git commit -m "feat: add structured build spec planner nodes"
```

---

### Task 4: Master BuildMCP Preflight

**Files:**
- Create: `multi-agent/internal/orchestrator/buildmcp_preflight.go`
- Create: `multi-agent/internal/orchestrator/buildmcp_preflight_test.go`
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Modify: `multi-agent/testdata/fake-planner.sh`

- [ ] **Step 1: Write preflight unit tests**

Create `internal/orchestrator/buildmcp_preflight_test.go`:

```go
package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/planner"
)

func TestPrepareBuildMCPNode_UsesStructuredBuildSpec(t *testing.T) {
	raw := json.RawMessage(`{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}`)
	node := planner.Node{ID: "n0", Kind: "build_mcp", Skill: "build_mcp", BuildSpec: raw}

	got, err := prepareBuildMCPNode(node)

	require.NoError(t, err)
	require.Equal(t, "build_mcp", got.Skill)
	require.NotEmpty(t, got.Prompt)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}],"hints":"","allowed_packages":[],"compose_servers":[],"version":1,"iteration":1,"max_iterations":3}`, got.Prompt)
}

func TestPrepareBuildMCPNode_UsesLegacyPromptJSON(t *testing.T) {
	node := planner.Node{
		ID: "n0", Kind: "build_mcp", Skill: "build_mcp",
		Prompt: `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}`,
	}

	got, err := prepareBuildMCPNode(node)

	require.NoError(t, err)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}],"hints":"","allowed_packages":[],"compose_servers":[],"version":1,"iteration":1,"max_iterations":3}`, got.Prompt)
}

func TestPrepareBuildMCPNode_RejectsNaturalLanguage(t *testing.T) {
	node := planner.Node{ID: "n0", Kind: "build_mcp", Skill: "build_mcp", Prompt: "build a server"}

	_, err := prepareBuildMCPNode(node)

	require.Error(t, err)
	require.ErrorContains(t, err, "malformed build_mcp spec")
}
```

- [ ] **Step 2: Run preflight tests and verify failure**

Run:

```bash
go test ./internal/orchestrator -run PrepareBuildMCPNode
```

Expected: FAIL because helper does not exist.

- [ ] **Step 3: Implement preflight helper**

Create `internal/orchestrator/buildmcp_preflight.go`:

```go
package orchestrator

import (
	"strings"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/planner"
)

func prepareBuildMCPNode(n planner.Node) (planner.Node, error) {
	if n.Kind != "build_mcp" && n.Skill != "build_mcp" {
		return n, nil
	}
	raw := strings.TrimSpace(string(n.BuildSpec))
	if raw == "" {
		raw = n.Prompt
	}
	spec, err := buildspec.ParseJSON(raw)
	if err != nil {
		return planner.Node{}, err
	}
	canonical, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		return planner.Node{}, err
	}
	n.Kind = "build_mcp"
	n.Skill = "build_mcp"
	n.Prompt = canonical
	return n, nil
}
```

- [ ] **Step 4: Add fanout test proving invalid spec does not dispatch**

Add to `internal/orchestrator/fanout_test.go`:

```go
func TestFanout_BuildMCPSpecPreflightRejectsBeforeDispatch(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available", Card: json.RawMessage(`{"skills":["build_mcp"]}`)}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_build_mcp_bad_text", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "build reusable server"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "build_mcp")
	require.Empty(t, sdk.dispatched)
}
```

Add fake planner mode:

```bash
  plan_build_mcp_bad_text)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","prompt":"build a reusable server"}
]
EOF
    ;;
```

- [ ] **Step 5: Call preflight in fanout before validation/dispatch**

In `internal/orchestrator/fanout.go`, inside the scheduling block just before `prompt := renderPrompt(n.Prompt, ...)` or before `validateMCPCall`, add:

```go
if n.Kind == "build_mcp" || n.Skill == "build_mcp" {
	prepared, err := prepareBuildMCPNode(n)
	if err != nil {
		sched.MarkDispatched(n.ID)
		inFlight++
		go func(n planner.Node, err error) {
			doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: "build_mcp spec preflight: " + err.Error()}}
		}(n, err)
		continue
	}
	n = prepared
}
```

Keep this first version fail-fast. The repair/replan loop is added in Task 5 so this task stays focused.

- [ ] **Step 6: Run orchestrator tests**

Run:

```bash
go test ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/orchestrator/buildmcp_preflight.go internal/orchestrator/buildmcp_preflight_test.go internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go testdata/fake-planner.sh
git commit -m "feat: preflight build mcp specs before dispatch"
```

---

### Task 5: Replan on Malformed BuildMCP Spec

**Files:**
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Modify: `multi-agent/internal/orchestrator/fanout_test.go`
- Modify: `multi-agent/testdata/fake-planner.sh`
- Modify: `multi-agent/internal/observer/event.go`

- [ ] **Step 1: Add observer event constant for spec validation failure**

Modify `internal/observer/event.go`:

```go
EventMasterBuildMCPValidationFailed = "master_build_mcp_validation_failed"
```

- [ ] **Step 2: Add failing replan test**

Add to `internal/orchestrator/fanout_test.go`:

```go
func TestFanout_BuildMCPSpecValidationReplans(t *testing.T) {
	t.Setenv("FAKE_PLANNER_ROUND_FILE", filepath.Join(t.TempDir(), "round"))
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available", Card: json.RawMessage(`{"skills":["build_mcp"]}`)}},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: `{"type":"mcp_tool_set","meta":{"name":"foo","tools":"render"}}`}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_build_mcp_repair", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "build reusable server"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}],"hints":"","allowed_packages":[],"compose_servers":[],"version":1,"iteration":1,"max_iterations":3}`, sdk.dispatched[0].Prompt)
	require.NotEmpty(t, eventsOfType(obs.events, observer.EventMasterBuildMCPValidationFailed))
}
```

Add fake planner mode:

```bash
  plan_build_mcp_repair)
    rf="${FAKE_PLANNER_ROUND_FILE:-/tmp/_fpround}"
    r=$(cat "$rf" 2>/dev/null || echo 0)
    case "$r" in
      0) cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","prompt":"build a reusable server"}
]
EOF
         ;;
      1) cat <<'EOF'
[
  {"id":"n1","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","build_spec":{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}}
]
EOF
         ;;
      *) echo "REDUCED";;
    esac
    echo $((r+1)) > "$rf"
    ;;
```

- [ ] **Step 3: Implement replan context helper**

Add to `internal/orchestrator/buildmcp_preflight.go`:

```go
func buildMCPSpecInvalidReplanContext(n planner.Node, err error) string {
	raw := strings.TrimSpace(string(n.BuildSpec))
	if raw == "" {
		raw = n.Prompt
	}
	if len(raw) > 800 {
		raw = raw[:800] + "..."
	}
	return "BUILD_MCP_SPEC_INVALID:\n" +
		"node_id: " + n.ID + "\n" +
		"error: " + err.Error() + "\n" +
		"bad_prompt_or_spec: " + raw + "\n" +
		"required_contract: build_mcp requires JSON with name, description, tools[].name, tools[].description, tools[].args_schema, tools[].result_description\n"
}
```

- [ ] **Step 4: Replace fail-fast preflight branch with replan**

In `internal/orchestrator/fanout.go`, when `prepareBuildMCPNode` fails:

```go
o.emit(observer.Event{
	Type:      observer.EventMasterBuildMCPValidationFailed,
	TaskID:    t.ID,
	SubtaskID: n.ID,
	Status:    "failed",
	Payload: observerPayload(map[string]interface{}{
		"error":    err.Error(),
		"required": !n.Optional,
		"prompt":   n.Prompt,
	}),
})
validationReplans++
if validationReplans >= maxBuildIterations {
	sched.MarkDispatched(n.ID)
	inFlight++
	go func(n planner.Node, err error) {
		doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: "build_mcp spec preflight: " + err.Error()}}
	}(n, err)
	continue
}
ctx2 := t.Prompt + "\n\n" + buildMCPSpecInvalidReplanContext(n, err)
newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
if perr != nil {
	cancelAll()
	return executor.Result{}, fmt.Errorf("replan after build_mcp spec validation failure: %w", perr)
}
if err := Validate(newPlan); err != nil {
	cancelAll()
	return executor.Result{}, fmt.Errorf("invalid build_mcp spec validation replan: %w", err)
}
newPlan = renamePlanIDs(newPlan, n.ID)
if err := sched.Append(newPlan); err != nil {
	cancelAll()
	return executor.Result{}, fmt.Errorf("append build_mcp spec validation replan: %w", err)
}
allNodes = append(allNodes, newPlan...)
for _, appended := range newPlan {
	optionalByID[appended.ID] = appended.Optional
}
appendSubTaskRows(o.store, t.ID, newPlan)
for _, skipped := range sched.MarkSuperseded(n.ID, "superseded by build_mcp spec validation replan") {
	optionalByID[skipped.NodeID] = true
	recordSkippedDone(skipped)
}
continue
```

- [ ] **Step 5: Run orchestrator tests**

Run:

```bash
go test ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/buildmcp_preflight.go internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go internal/observer/event.go testdata/fake-planner.sh
git commit -m "feat: replan malformed build mcp specs"
```

---

### Task 6: Progress Event Types and Helper

**Files:**
- Modify: `multi-agent/internal/observer/event.go`
- Create: `multi-agent/internal/progress/progress.go`
- Create: `multi-agent/internal/progress/progress_test.go`

- [ ] **Step 1: Add progress helper tests**

Create `internal/progress/progress_test.go`:

```go
package progress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunWithHeartbeatEmitsProgressUntilFunctionReturns(t *testing.T) {
	var messages []string
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    10 * time.Millisecond,
		HardTimeout: time.Second,
		Message:     "still running",
		Emit: func(_ context.Context, elapsed time.Duration) {
			messages = append(messages, elapsed.String())
		},
	}, func(ctx context.Context) error {
		time.Sleep(25 * time.Millisecond)
		return nil
	})

	require.NoError(t, err)
	require.NotEmpty(t, messages)
}

func TestRunWithHeartbeatHardTimeout(t *testing.T) {
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    10 * time.Millisecond,
		HardTimeout: 20 * time.Millisecond,
		Message:     "still running",
		Emit:        func(context.Context, time.Duration) {},
	}, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "hard timeout")
}

func TestRunWithHeartbeatIdleTimeout(t *testing.T) {
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    time.Hour,
		IdleTimeout: 25 * time.Millisecond,
		HardTimeout: time.Second,
		Message:     "still running",
		Emit:        func(context.Context, time.Duration) {},
	}, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "idle timeout")
}

func TestRunWithHeartbeatReturnsFunctionError(t *testing.T) {
	want := errors.New("boom")
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    time.Hour,
		HardTimeout: time.Second,
		Message:     "still running",
		Emit:        func(context.Context, time.Duration) {},
	}, func(ctx context.Context) error {
		return want
	})

	require.ErrorIs(t, err, want)
}
```

- [ ] **Step 2: Run progress tests and verify failure**

Run:

```bash
go test ./internal/progress
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement progress helper**

Create `internal/progress/progress.go`:

```go
package progress

import (
	"context"
	"fmt"
	"time"
)

type Config struct {
	Interval    time.Duration
	IdleTimeout time.Duration
	HardTimeout time.Duration
	Message     string
	Emit        func(context.Context, time.Duration)
}

func RunWithHeartbeat(parent context.Context, cfg Config, fn func(context.Context) error) error {
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}
	ctx := parent
	cancel := func() {}
	if cfg.HardTimeout > 0 {
		ctx, cancel = context.WithTimeout(parent, cfg.HardTimeout)
	}
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- fn(ctx)
	}()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	var idle <-chan time.Time
	var idleTimer *time.Timer
	if cfg.IdleTimeout > 0 {
		idleTimer = time.NewTimer(cfg.IdleTimeout)
		defer idleTimer.Stop()
		idle = idleTimer.C
	}

	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			if cfg.Emit != nil {
				cfg.Emit(ctx, time.Since(start))
			}
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(cfg.IdleTimeout)
			}
		case <-idle:
			cancel()
			return fmt.Errorf("idle timeout after %s", cfg.IdleTimeout)
		case <-ctx.Done():
			if cfg.HardTimeout > 0 && parent.Err() == nil {
				return fmt.Errorf("hard timeout after %s", cfg.HardTimeout)
			}
			return ctx.Err()
		}
	}
}
```

- [ ] **Step 4: Add observer progress constants**

Modify `internal/observer/event.go`:

```go
EventMasterPlanningProgress = "master_planning_progress"
EventMasterPlanningCompleted = "master_planning_completed"
EventSlaveTaskProgress = "slave_task_progress"
EventSlaveBuildMCPProgress = "slave_build_mcp_progress"
```

Add helper:

```go
func IsProgressEvent(eventType string) bool {
	switch eventType {
	case EventMasterPlanningProgress, EventMasterPlanningCompleted, EventSlaveTaskProgress, EventSlaveBuildMCPProgress:
		return true
	default:
		return false
	}
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./internal/progress ./internal/observerstore ./internal/observerweb
```

Expected: PASS. Observer packages should still ignore new event constants.

- [ ] **Step 6: Commit**

```bash
git add internal/progress/progress.go internal/progress/progress_test.go internal/observer/event.go
git commit -m "feat: add progress event primitives"
```

---

### Task 7: Master Planning Progress

**Files:**
- Modify: `multi-agent/internal/planner/planner.go`
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Modify: `multi-agent/internal/orchestrator/fanout_test.go`

- [ ] **Step 1: Introduce planner progress callback**

Modify `internal/planner/planner.go`:

```go
type ProgressFunc func(ctx context.Context, phase, message string, elapsed time.Duration)

type Planner struct {
	cfg      config.Planner
	progress ProgressFunc
}

func New(cfg config.Planner) *Planner { return &Planner{cfg: cfg} }

func (p *Planner) WithProgress(fn ProgressFunc) *Planner {
	cp := *p
	cp.progress = fn
	return &cp
}
```

- [ ] **Step 2: Wrap `runClaude` with heartbeat**

In `runClaude`, replace direct `cmd.Output()` with a goroutine plus `progress.RunWithHeartbeat`. The implementation should preserve stdout/stderr behavior:

```go
var out []byte
err := progress.RunWithHeartbeat(ctx, progress.Config{
	Interval:    15 * time.Second,
	HardTimeout: timeout,
	Message:     "planner still running",
	Emit: func(ctx context.Context, elapsed time.Duration) {
		if p.progress != nil {
			p.progress(ctx, "planning", "planner still running", elapsed)
		}
	},
}, func(runCtx context.Context) error {
	args := append([]string{"--print"}, p.cfg.ExtraArgs...)
	cmd := exec.CommandContext(runCtx, p.cfg.Bin, args...)
	cmd.Stdin = strings.NewReader(stdinPrompt)
	cmd.Stderr = &stderrBuf
	var err error
	out, err = cmd.Output()
	return err
})
```

Keep existing error mapping:

```go
if err != nil {
	if strings.Contains(err.Error(), "hard timeout") {
		return "", fmt.Errorf("planner timeout after %s", timeout)
	}
	...
}
```

- [ ] **Step 3: Emit master planning progress in orchestrator**

In `internal/orchestrator/fanout.go`, before calling `o.planner.Plan`, derive a planner with progress:

```go
planningProgress := func(ctx context.Context, phase, message string, elapsed time.Duration) {
	o.emit(observer.Event{
		Type:   observer.EventMasterPlanningProgress,
		TaskID: t.ID,
		Status: "running",
		Payload: observerPayload(map[string]interface{}{
			"phase":      phase,
			"message":    message,
			"elapsed_ms": elapsed.Milliseconds(),
			"is_final":   false,
		}),
	})
}
plan, err := o.planner.WithProgress(planningProgress).Plan(ctx, t.Prompt, agents)
```

After successful plan creation, emit:

```go
o.emit(observer.Event{
	Type:   observer.EventMasterPlanningCompleted,
	TaskID: t.ID,
	Status: "completed",
	Payload: observerPayload(map[string]interface{}{
		"phase": "planning",
		"message": "planning completed",
		"is_final": false,
		"node_count": len(plan),
	}),
})
```

Apply the same wrapper to replan calls inside fanout.

- [ ] **Step 4: Add orchestrator test for planning completed event**

Add to `internal/orchestrator/fanout_test.go`:

```go
func TestFanout_EmitsPlanningCompleted(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.NotEmpty(t, eventsOfType(obs.events, observer.EventMasterPlanningCompleted))
}
```

This test uses the existing `plan_chain` fake mode and only verifies the non-terminal planning-completed progress event. Heartbeat timing is covered by `internal/progress` tests so this test does not sleep.

- [ ] **Step 5: Run planner and orchestrator tests**

Run:

```bash
go test ./internal/planner ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/planner/planner.go internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go
git commit -m "feat: emit master planning progress"
```

---

### Task 8: Slave BuildMCP Progress

**Files:**
- Modify: `multi-agent/internal/executor/buildmcp.go`
- Modify: `multi-agent/internal/executor/buildmcp_test.go`

- [ ] **Step 1: Add failing progress event test**

Add to `internal/executor/buildmcp_test.go`:

```go
func TestBuildMCP_EmitsProgressEvents(t *testing.T) {
	os.Setenv("FAKE_BUILD_CLAUDE_MODE", "ok")
	defer os.Unsetenv("FAKE_BUILD_CLAUDE_MODE")
	obs := &fakeObserver{}
	be, _ := newBuildMCPForTestWithObserver(t, obs)
	defer be.MCPExec.Close()

	spec := map[string]interface{}{
		"name": "foo", "description": "d",
		"tools": []map[string]interface{}{{"name": "foo", "description": "d", "args_schema": map[string]interface{}{"type": "object"}, "result_description": "r"}},
		"allowed_packages": []string{}, "version": 1, "iteration": 1, "max_iterations": 3,
	}
	specBytes, _ := json.Marshal(spec)

	_, err := be.Run(context.Background(), Task{ID: "tx", Skill: "build_mcp", Prompt: string(specBytes), TimeoutSec: 30}, &nopSink{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	progress := []observer.Event{}
	for _, ev := range obs.events {
		if ev.Type == observer.EventSlaveBuildMCPProgress {
			progress = append(progress, ev)
		}
	}
	if len(progress) == 0 {
		t.Fatalf("expected build progress events, got %+v", obs.events)
	}
}
```

- [ ] **Step 2: Run focused test and verify failure**

Run:

```bash
go test ./internal/executor -run TestBuildMCP_EmitsProgressEvents
```

Expected: FAIL because no progress events are emitted.

- [ ] **Step 3: Add build progress emitter**

In `internal/executor/buildmcp.go`, add:

```go
func (e *BuildMCPExecutor) progress(t Task, phase, message string, extra map[string]interface{}) {
	payload := map[string]interface{}{
		"phase":    phase,
		"message":  message,
		"is_final": false,
	}
	for k, v := range extra {
		payload[k] = v
	}
	e.emit(observer.Event{
		Type:   observer.EventSlaveBuildMCPProgress,
		TaskID: t.ID,
		Status: "running",
		Payload: buildMCPObserverPayload(payload),
	})
}
```

Call it at each explicit phase:

```go
e.progress(t, "parse_spec", "parsed build_mcp spec", map[string]interface{}{"name": spec.Name})
e.progress(t, "reuse", "reusing existing generated MCP server", map[string]interface{}{"name": spec.Name})
e.progress(t, "generate", "generating MCP server source", map[string]interface{}{"name": spec.Name})
e.progress(t, "validate", "validating generated source", map[string]interface{}{"name": spec.Name})
e.progress(t, "smoke_launch", "smoke launching generated MCP server", map[string]interface{}{"name": spec.Name})
e.progress(t, "register", "registering generated MCP server", map[string]interface{}{"name": spec.Name})
e.progress(t, "republish", "republishing slave capability card", map[string]interface{}{"name": spec.Name})
```

- [ ] **Step 4: Wrap Claude generation with heartbeat**

Change `invokeClaude` signature:

```go
func (e *BuildMCPExecutor) invokeClaude(ctx context.Context, t Task, spec buildspec.Spec, priorCode string) (string, error)
```

Inside it, use `progress.RunWithHeartbeat` with:

```go
Interval: 20 * time.Second
HardTimeout: 0
Emit: func(ctx context.Context, elapsed time.Duration) {
	e.progress(t, "generate", "generating MCP server source", map[string]interface{}{
		"name": spec.Name,
		"elapsed_ms": elapsed.Milliseconds(),
	})
}
```

Use `HardTimeout: 0` here deliberately. `BuildMCPExecutor` already receives a task context with the dispatcher timeout applied, so the heartbeat wrapper should not create a second deadline.

- [ ] **Step 5: Run executor tests**

Run:

```bash
go test ./internal/executor -run BuildMCP
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/executor/buildmcp.go internal/executor/buildmcp_test.go
git commit -m "feat: emit build mcp progress"
```

---

### Task 9: Observer Progress Aggregation

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql`
- Modify: `multi-agent/internal/observerstore/store.go`
- Modify: `multi-agent/internal/observerstore/store_test.go`

- [ ] **Step 1: Add failing observerstore progress test**

Add to `internal/observerstore/store_test.go`:

```go
func TestProgressEventsUpdateLatestProgressWithoutTerminalState(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing", Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningProgress, TaskID: "mt1", Status: "running",
		Payload: json.RawMessage(`{"phase":"planning","message":"planner still running","is_final":false}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "assigned", tasks[0].Status)
	require.Equal(t, "planner still running", tasks[0].LatestProgress)
	require.Equal(t, "planning", tasks[0].LatestProgressPhase)
	require.False(t, tasks[0].IsFinal)
	require.Empty(t, tasks[0].FinalOutput)
}

func TestTerminalEventSetsIsFinalAndFinalOutput(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing", Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterTaskCompleted, TaskID: "mt1", Status: "completed",
		Payload: json.RawMessage(`{"output":"done"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.True(t, tasks[0].IsFinal)
	require.Equal(t, "done", tasks[0].FinalOutput)
}
```

- [ ] **Step 2: Run observerstore tests and verify failure**

Run:

```bash
go test ./internal/observerstore -run 'ProgressEvents|TerminalEvent'
```

Expected: FAIL because fields do not exist.

- [ ] **Step 3: Extend schema and migrations**

Modify `internal/observerstore/schema.sql` `tasks` table:

```sql
latest_progress TEXT,
latest_progress_phase TEXT,
latest_progress_at TEXT,
final_output TEXT,
is_final INTEGER NOT NULL DEFAULT 0,
```

Modify `subtasks` table with these columns:

```sql
latest_progress TEXT,
latest_progress_phase TEXT,
latest_progress_at TEXT,
```

Modify `ensureColumns` in `store.go`:

```go
for _, stmt := range []string{
	`ALTER TABLE tasks ADD COLUMN latest_progress TEXT`,
	`ALTER TABLE tasks ADD COLUMN latest_progress_phase TEXT`,
	`ALTER TABLE tasks ADD COLUMN latest_progress_at TEXT`,
	`ALTER TABLE tasks ADD COLUMN final_output TEXT`,
	`ALTER TABLE tasks ADD COLUMN is_final INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE subtasks ADD COLUMN latest_progress TEXT`,
	`ALTER TABLE subtasks ADD COLUMN latest_progress_phase TEXT`,
	`ALTER TABLE subtasks ADD COLUMN latest_progress_at TEXT`,
} {
	if _, err := db.Exec(stmt); err != nil && !isDuplicateColumn(err) {
		return err
	}
}
```

- [ ] **Step 4: Extend view structs and queries**

Modify `TaskView`:

```go
LatestProgress      string `json:"latest_progress"`
LatestProgressPhase string `json:"latest_progress_phase"`
LatestProgressAt    string `json:"latest_progress_at"`
FinalOutput         string `json:"final_output"`
IsFinal             bool   `json:"is_final"`
```

Modify `SubtaskView`:

```go
LatestProgress      string `json:"latest_progress"`
LatestProgressPhase string `json:"latest_progress_phase"`
LatestProgressAt    string `json:"latest_progress_at"`
```

Update `ListTasks` select/scan to include the new columns and convert `is_final` integer to bool.

- [ ] **Step 5: Aggregate progress events**

Modify `applyAggregate`:

```go
if observer.IsProgressEvent(ev.Type) {
	return updateLatestProgress(tx, ev)
}
```

Add helper:

```go
func updateLatestProgress(tx *sql.Tx, ev observer.Event) error {
	phase, message := progressPayload(ev.Payload)
	if message == "" {
		message = ev.Summary
	}
	if phase == "" {
		phase = ev.Type
	}
	if ev.SubtaskID != "" || ev.ChildTaskID != "" || ev.ParentTaskID != "" {
		parentTaskID := ev.TaskID
		if ev.ParentTaskID != "" {
			parentTaskID = ev.ParentTaskID
		}
		subtaskID := ev.SubtaskID
		if subtaskID == "" {
			subtaskID = ev.ChildTaskID
		}
		_, err := tx.Exec(`UPDATE subtasks SET latest_progress=?, latest_progress_phase=?, latest_progress_at=?, updated_at=? WHERE workspace_id=? AND parent_task_id=? AND subtask_id=?`,
			message, phase, ev.TS, ev.TS, ev.WorkspaceID, parentTaskID, subtaskID)
		return err
	}
	_, err := tx.Exec(`UPDATE tasks SET latest_progress=?, latest_progress_phase=?, latest_progress_at=?, updated_at=? WHERE workspace_id=? AND task_id=?`,
		message, phase, ev.TS, ev.TS, ev.WorkspaceID, ev.TaskID)
	return err
}

func progressPayload(raw json.RawMessage) (phase, message string) {
	var p struct {
		Phase   string `json:"phase"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Phase, p.Message
}
```

- [ ] **Step 6: Mark terminal task states final**

Modify `updateTaskStatus` so completed/failed/cancelled set `is_final=1`; extract final output from payload if present:

```go
final := ev.Status == "completed" || ev.Status == "failed" || ev.Status == "cancelled"
finalOutput := ""
if final {
	var p struct{ Output string `json:"output"` }
	_ = json.Unmarshal(ev.Payload, &p)
	finalOutput = p.Output
}
_, err := tx.Exec(`UPDATE tasks SET status=?, final_output=CASE WHEN ? THEN ? ELSE final_output END, is_final=CASE WHEN ? THEN 1 ELSE is_final END, updated_at=? WHERE workspace_id=? AND task_id=?`,
	ev.Status, final, finalOutput, final, ev.TS, ev.WorkspaceID, ev.TaskID)
```

- [ ] **Step 7: Run observerstore tests**

Run:

```bash
go test ./internal/observerstore
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/observerstore/schema.sql internal/observerstore/store.go internal/observerstore/store_test.go
git commit -m "feat: aggregate task progress events"
```

---

### Task 10: Observer Web Progress Display

**Files:**
- Modify: `multi-agent/internal/observerweb/server.go`
- Modify: `multi-agent/internal/observerweb/server_test.go`

- [ ] **Step 1: Add API test for progress fields**

Add to `internal/observerweb/server_test.go`:

```go
func TestAPITasksIncludesLatestProgress(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "build", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningProgress, TaskID: "t1", Status: "running",
		Payload: json.RawMessage(`{"phase":"planning","message":"planner still running","is_final":false}`),
	}))

	h := New(st)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var tasks []observerstore.TaskView
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&tasks))
	require.Equal(t, "planner still running", tasks[0].LatestProgress)
	require.False(t, tasks[0].IsFinal)
}
```

- [ ] **Step 2: Add progress display to templates**

In `internal/observerweb/server.go`, add a compact progress line in task and slave tables:

```html
{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}
```

For subtask rows:

```html
{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}
```

- [ ] **Step 3: Run observerweb tests**

Run:

```bash
go test ./internal/observerweb
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/observerweb/server.go internal/observerweb/server_test.go
git commit -m "feat: show observer progress"
```

---

### Task 11: Driver Progress Responses

**Files:**
- Create: `multi-agent/internal/driver/observer_progress.go`
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Create observer progress client tests**

Add tests to `internal/driver/tools_test.go` using `httptest.Server`:

```go
func TestGetTaskIncludesObserverProgress(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			require.Equal(t, "t1", id)
			require.True(t, includeOutput)
			return &agentsdk.TaskInfo{TaskID: "t1", Status: "running"}, nil
		},
	}
	obsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/tasks", r.URL.Path)
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{{
			"task_id": "t1", "status": "running",
			"latest_progress": "planner still running",
			"latest_progress_phase": "planning",
			"latest_progress_at": "2026-05-13T00:00:00Z",
			"is_final": false,
		}})
	}))
	defer obsServer.Close()
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.URL = obsServer.URL

	out, err := (&getTaskTool{tools}).Call(context.Background(), json.RawMessage(`{"task_id":"t1"}`))

	require.NoError(t, err)
	require.JSONEq(t, `{"status":"running","output":"","failure_reason":"","latest_progress":"planner still running","latest_progress_phase":"planning","latest_progress_at":"2026-05-13T00:00:00Z","is_final":false,"final_output":""}`, string(out))
}
```

Modify the import block in `internal/driver/tools_test.go` to include:

```go
"net/http/httptest"
```

- [ ] **Step 2: Implement observer progress client**

Create `internal/driver/observer_progress.go`:

```go
package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type taskProgress struct {
	LatestProgress      string `json:"latest_progress"`
	LatestProgressPhase string `json:"latest_progress_phase"`
	LatestProgressAt    string `json:"latest_progress_at"`
	FinalOutput         string `json:"final_output"`
	IsFinal             bool   `json:"is_final"`
}

func (t *Tools) observerProgress(ctx context.Context, taskID string) taskProgress {
	if t.cfg == nil || t.cfg.Observer.URL == "" {
		return taskProgress{}
	}
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(t.cfg.Observer.URL, "/")+"/api/tasks", nil)
	if err != nil {
		return taskProgress{}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return taskProgress{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return taskProgress{}
	}
	var tasks []struct {
		TaskID string `json:"task_id"`
		taskProgress
	}
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return taskProgress{}
	}
	for _, task := range tasks {
		if task.TaskID == taskID {
			return task.taskProgress
		}
	}
	return taskProgress{}
}
```

- [ ] **Step 3: Extend `get_task` response**

In `internal/driver/tools.go`, after `GetTask`:

```go
progress := g.t.observerProgress(ctx, taskID)
return json.Marshal(map[string]interface{}{
	"status":                info.Status,
	"output":                info.Output,
	"failure_reason":        info.FailureReason,
	"latest_progress":       progress.LatestProgress,
	"latest_progress_phase": progress.LatestProgressPhase,
	"latest_progress_at":    progress.LatestProgressAt,
	"final_output":          progress.FinalOutput,
	"is_final":              progress.IsFinal || info.Status == "completed" || info.Status == "failed" || info.Status == "cancelled",
})
```

- [ ] **Step 4: Extend `wait_task` terminal response**

In terminal branch of `wait_task`, include the same progress fields:

```go
progress := w.t.observerProgress(ctx, taskID)
return json.Marshal(map[string]interface{}{
	"status":                info.Status,
	"output":                info.Output,
	"failure_reason":        info.FailureReason,
	"written_files":         written,
	"latest_progress":       progress.LatestProgress,
	"latest_progress_phase": progress.LatestProgressPhase,
	"latest_progress_at":    progress.LatestProgressAt,
	"final_output":          firstNonEmpty(progress.FinalOutput, info.Output),
	"is_final":              true,
})
```

Add helper:

```go
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
```

- [ ] **Step 5: Run driver tests**

Run:

```bash
go test ./internal/driver
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/driver/observer_progress.go internal/driver/tools.go internal/driver/tools_test.go
git commit -m "feat: return task progress to driver"
```

---

### Task 12: Config Defaults and Full Verification

**Files:**
- Modify: `multi-agent/cmd/master-agent/config.yaml`
- Modify: `multi-agent/cmd/slave-agent/config.yaml`

- [ ] **Step 1: Update master planner timeout default**

Modify `cmd/master-agent/config.yaml`:

```yaml
planner:
    bin: claude
    timeout_sec: 300
    extra_args: []
```

- [ ] **Step 2: Keep slave executor timeout practical**

Update `cmd/slave-agent/config.yaml`:

```yaml
fanout:
    subtask_defaults:
        timeout_sec: 900
```

Progress intervals remain code constants from Tasks 7 and 8 in this plan, so no new YAML fields or config struct changes are part of this task.

- [ ] **Step 3: Run full Go test suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Build binaries**

Run:

```bash
go build ./cmd/master-agent ./cmd/slave-agent ./cmd/driver-agent ./cmd/observer-server
```

Expected: PASS and binaries are produced in the current directory or default Go build output behavior.

- [ ] **Step 5: Manual smoke test**

Start observer, master, slave, and driver:

```bash
scripts/observer.sh restart
scripts/agents.sh restart
scripts/observer.sh status
scripts/agents.sh status
```

Submit a reusable-MCP task through the currently configured driver MCP client, then inspect observer output:

```bash
curl -s http://127.0.0.1:8090/api/tasks
```

Expected:

- `/api/tasks` includes `latest_progress` during planning/build.
- invalid natural language `build_mcp` is repaired or fails in master before slave dispatch.
- valid `build_spec` reaches slave as JSON.
- `driver get_task` shows progress with `is_final=false` while running.
- terminal result sets `is_final=true`.

- [ ] **Step 6: Commit**

```bash
git add cmd/master-agent/config.yaml cmd/slave-agent/config.yaml
git commit -m "chore: tune agent timeouts for progress flow"
```

---

## Final Verification

Run:

```bash
go test ./...
go build ./cmd/master-agent ./cmd/slave-agent ./cmd/driver-agent ./cmd/observer-server
```

Expected:

- all tests pass.
- all four commands build.
- no unrelated files are modified.

Check worktree:

```bash
git status --short
```

Expected:

- only intentional tracked changes, or clean after commits.
