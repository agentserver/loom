# Driver-First Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move routing and simple orchestration decisions into the driver while preserving master fanout compatibility.

**Architecture:** Implement this incrementally. First make driver dry-run produce explicit route recommendations, then make submit follow those recommendations for direct slave and explicit master paths. After that, extract master fanout primitives into reusable orchestration packages before adding driver-managed fanout.

**Tech Stack:** Go, `github.com/agentserver/agentserver/pkg/agentsdk`, existing `internal/driver`, `internal/contract`, `internal/orchestrator`, `internal/planner`, observer artifact APIs, and Go test.

---

## File Structure

- Modify `multi-agent/internal/driver/capability_tools.go`: add route recommendation fields and route analysis helpers.
- Modify `multi-agent/internal/driver/contract_tools.go`: make `submit_contract_task` use route recommendations and return the chosen route.
- Modify `multi-agent/internal/driver/tools_test.go`: add focused tests for route recommendation and submit behavior.
- Modify `multi-agent/examples/dynamic-mcp/scenario_test.go`: assert driver clarification fixture includes the route recommendation.
- Modify `multi-agent/examples/dynamic-mcp/scenarios/driver-clarify-contract/expected/dry_run_result.json`: include expected `recommended_route`.
- Create `multi-agent/internal/orchestration/dag.go`: move scheduler, finished-node type, DAG validation, and template rendering from `internal/orchestrator/dag.go`.
- Modify `multi-agent/internal/orchestrator/dag.go`: reduce to compatibility wrappers or remove after imports migrate.
- Modify `multi-agent/internal/orchestrator/*.go`: update imports and references from local DAG helpers to `internal/orchestration`.
- Create `multi-agent/internal/orchestration/summary.go`: move reducer fallback summary into a shared helper.
- Create `multi-agent/internal/orchestration/driver_runner.go`: add a driver-facing fanout runner after shared primitives exist.
- Create `multi-agent/internal/orchestration/driver_runner_test.go`: test direct DAG execution with fake planner and fake SDK.
- Modify `multi-agent/internal/driver/contract_tools.go`: add `driver_fanout` execution path once `driver_runner.go` exists.
- Create `multi-agent/examples/driver-first/README.md`: document direct slave, driver fanout, and master fanout scenarios.

## Route Names

Use these exact route strings in driver JSON outputs:

```go
const (
	routeDirectSlave  = "direct_slave"
	routeDriverFanout = "driver_fanout"
	routeMasterFanout = "master_fanout"
	routeBlocked      = "blocked"
)
```

Keep these as driver-level route recommendation constants. Do not add them to `contract.ExecutionPolicy.Routing`; contract routing remains `direct_first` or `master_only`.

---

### Task 1: Add `recommended_route` To Dry Run

**Files:**
- Modify: `multi-agent/internal/driver/capability_tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`
- Modify: `multi-agent/examples/dynamic-mcp/scenario_test.go`
- Modify: `multi-agent/examples/dynamic-mcp/scenarios/driver-clarify-contract/expected/dry_run_result.json`

- [ ] **Step 1: Write failing dry-run route tests**

Add these tests to `multi-agent/internal/driver/tools_test.go` near the existing dry-run tests:

```go
func TestTool_DryRunContractRecommendsDirectSlaveRoute(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("dry_run_contract must not delegate")
			return nil, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"recommended_route":"direct_slave"`)
	require.Contains(t, string(out), `"recommended_target_id":"slave-a"`)
	require.Contains(t, string(out), `"recommended_skill":"chat"`)
}

func TestTool_DryRunContractRecommendsDriverFanoutForBuildableMissingTool(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "builder", DisplayName: "builder", Status: "available", Card: json.RawMessage(`{"skills":["build_mcp"],"resources":{"tags":["python3"]}}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.ExecutionPolicy.AllowBuildMCP = true
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"runnable":true`)
	require.Contains(t, string(out), `"requires_build_mcp":true`)
	require.Contains(t, string(out), `"recommended_route":"driver_fanout"`)
	require.Contains(t, string(out), `"recommended_skill":"fanout"`)
}

func TestTool_DryRunContractRecommendsMasterFanoutForMasterOnly(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "s1", DisplayName: "slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"recommended_route":"master_fanout"`)
	require.Contains(t, string(out), `"recommended_target_id":"m1"`)
	require.Contains(t, string(out), `"recommended_skill":"fanout"`)
}

func TestTool_DryRunContractReportsBlockedRoute(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "s1", DisplayName: "slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"missing/tool"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"runnable":false`)
	require.Contains(t, string(out), `"recommended_route":"blocked"`)
	require.Contains(t, string(out), `"missing_tools":["missing/tool"]`)
}
```

- [ ] **Step 2: Run route tests and verify they fail**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver -run 'TestTool_DryRunContractRecommends|TestTool_DryRunContractReportsBlockedRoute'
```

Expected: FAIL because `recommended_route` is not present in dry-run JSON.

- [ ] **Step 3: Add route fields and constants**

In `multi-agent/internal/driver/capability_tools.go`, add the constants after `type dryRunContractTool struct{ t *Tools }`:

```go
const (
	routeDirectSlave  = "direct_slave"
	routeDriverFanout = "driver_fanout"
	routeMasterFanout = "master_fanout"
	routeBlocked      = "blocked"
)
```

Add this field to `dryRunReport`:

```go
RecommendedRoute string `json:"recommended_route"`
```

- [ ] **Step 4: Implement dry-run route selection**

Replace `analyzeContractCapabilities` in `multi-agent/internal/driver/capability_tools.go` with this implementation:

```go
func analyzeContractCapabilities(cards []agentsdk.AgentCard, selfID string, tc contract.TaskContract) dryRunReport {
	snapshot := contract.NewResourceSnapshot(cards, selfID)
	report := dryRunReport{RecommendedRoute: routeBlocked}
	report.SatisfiedTools, report.MissingTools = matchRequiredTools(snapshot.Agents, tc.CapabilityRequirements.Tools)
	report.MissingSkills = missingRequiredSkills(snapshot.Agents, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
	buildTargets := candidateAgentsWithSkill(snapshot.Agents, "build_mcp", tc.ExecutionPolicy.AllowedTargets)
	report.CandidateBuildTargets = buildTargets
	if len(report.MissingSkills) > 0 {
		report.Reasons = append(report.Reasons, "required skills are missing or unavailable")
		return report
	}

	direct := directContractMatches(cards, selfID, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
	if tc.ExecutionPolicy.Routing == contract.RoutingDirectFirst && len(direct) == 1 && len(report.MissingTools) == 0 && cardSatisfiesTools(direct[0], tc.CapabilityRequirements.Tools) {
		report.Runnable = true
		report.RecommendedRoute = routeDirectSlave
		report.RecommendedTargetID = direct[0].AgentID
		report.RecommendedTargetName = direct[0].DisplayName
		report.RecommendedSkill = "chat"
		report.Reasons = append(report.Reasons, "single direct slave satisfies required skills and tools")
		return report
	}

	master := firstAllowedMaster(snapshot.Agents, tc)
	if tc.ExecutionPolicy.Routing == contract.RoutingMasterOnly {
		if master.AgentID == "" {
			report.Reasons = append(report.Reasons, "no allowed available master with fanout skill is visible")
			return report
		}
		if len(report.MissingTools) > 0 && (!tc.ExecutionPolicy.AllowBuildMCP || len(buildTargets) == 0) {
			report.Reasons = append(report.Reasons, "master route requested but required tools are missing and cannot be built")
			return report
		}
		report.Runnable = true
		report.RequiresBuildMCP = len(report.MissingTools) > 0
		report.RecommendedRoute = routeMasterFanout
		report.RecommendedTargetID = master.AgentID
		report.RecommendedTargetName = master.DisplayName
		report.RecommendedSkill = "fanout"
		report.Reasons = append(report.Reasons, "contract requires master fanout route")
		return report
	}

	if len(report.MissingTools) == 0 {
		report.Runnable = true
		report.RecommendedRoute = routeDriverFanout
		report.RecommendedSkill = "fanout"
		report.Reasons = append(report.Reasons, "driver can orchestrate with currently advertised tools")
		return report
	}
	if tc.ExecutionPolicy.AllowBuildMCP && len(buildTargets) > 0 {
		report.Runnable = true
		report.RequiresBuildMCP = true
		report.RecommendedRoute = routeDriverFanout
		report.RecommendedSkill = "fanout"
		report.Reasons = append(report.Reasons, "missing tools can be built by available build_mcp slaves during driver orchestration")
		return report
	}
	if !tc.ExecutionPolicy.AllowBuildMCP {
		report.Reasons = append(report.Reasons, "required tools are missing and allow_build_mcp is false")
	} else {
		report.Reasons = append(report.Reasons, "required tools are missing and no allowed build_mcp slave is available")
	}
	return report
}
```

- [ ] **Step 5: Update dynamic fixture expected dry run**

In `multi-agent/examples/dynamic-mcp/scenarios/driver-clarify-contract/expected/dry_run_result.json`, add:

```json
"recommended_route": "driver_fanout",
```

Keep `recommended_target_id` and `recommended_skill` only if the implementation still returns them. For driver fanout, `recommended_target_id` should be omitted because the driver is the orchestrator.

- [ ] **Step 6: Update scenario test struct**

In `multi-agent/examples/dynamic-mcp/scenario_test.go`, add the field to the local dry-run struct:

```go
RecommendedRoute string `json:"recommended_route"`
```

Change assertions to:

```go
require.Equal(t, "driver_fanout", dryRun.RecommendedRoute)
require.Equal(t, "fanout", dryRun.RecommendedSkill)
```

Remove the assertion that `RecommendedTargetID` equals `master-online-e2e` if the fixture no longer expects a target for driver fanout.

- [ ] **Step 7: Run driver and dynamic-mcp tests**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver ./examples/dynamic-mcp
```

Expected: PASS.

- [ ] **Step 8: Commit Task 1**

```bash
git add multi-agent/internal/driver/capability_tools.go multi-agent/internal/driver/tools_test.go multi-agent/examples/dynamic-mcp/scenario_test.go multi-agent/examples/dynamic-mcp/scenarios/driver-clarify-contract/expected/dry_run_result.json
git commit -m "Add driver route recommendations"
```

---

### Task 2: Make `submit_contract_task` Return And Honor Route Decisions

**Files:**
- Modify: `multi-agent/internal/driver/contract_tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Write failing submit route tests**

Add these tests to `multi-agent/internal/driver/tools_test.go` near submit contract tests:

```go
func TestSubmitContractTaskReturnsDirectSlaveRoute(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"direct_slave"`)
	require.Equal(t, "slave-a", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
}

func TestSubmitContractTaskReturnsMasterFanoutRoute(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"master_fanout"`)
	require.Equal(t, "m1", lastDelegate.TargetID)
	require.Equal(t, "fanout", lastDelegate.Skill)
}
```

- [ ] **Step 2: Run submit route tests and verify they fail**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver -run 'TestSubmitContractTaskReturns.*Route'
```

Expected: FAIL because submit output does not include `route`.

- [ ] **Step 3: Add selected route to target selection**

In `multi-agent/internal/driver/contract_tools.go`, change `selectTarget` signature to:

```go
func (s *submitContractTaskTool) selectTarget(ctx context.Context, cards []agentsdk.AgentCard, tc contract.TaskContract, targetOverride, skillOverride string) (string, string, string, string, error)
```

Return `routeDirectSlave` for the direct match branch and `routeMasterFanout` for the master fallback branch.

- [ ] **Step 4: Update caller and JSON response**

In `Call`, change target selection to:

```go
targetID, targetName, skill, route, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
```

Add this field to the returned map:

```go
"route": route,
```

- [ ] **Step 5: Keep driver fanout explicit but not executed yet**

If `analyzeContractCapabilities(cards, s.t.cfg.Credentials.SandboxID, tc).RecommendedRoute == routeDriverFanout`, return this error until Task 5 implements it:

```go
return nil, &MCPToolError{Message: "driver_fanout route is recommended but driver-managed fanout is not implemented yet"}
```

Place that check after resource snapshot save and before master fallback. This prevents silently sending driver-first tasks to master.

- [ ] **Step 6: Run driver tests**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver
```

Expected: PASS.

- [ ] **Step 7: Commit Task 2**

```bash
git add multi-agent/internal/driver/contract_tools.go multi-agent/internal/driver/tools_test.go
git commit -m "Report submit contract route decisions"
```

---

### Task 3: Extract Shared DAG And Rendering Primitives

**Files:**
- Create: `multi-agent/internal/orchestration/dag.go`
- Modify: `multi-agent/internal/orchestrator/dag.go`
- Modify: `multi-agent/internal/orchestrator/dag_test.go`
- Modify: `multi-agent/internal/orchestrator/fanout.go`

- [ ] **Step 1: Create shared package test**

Create `multi-agent/internal/orchestration/dag_test.go` with tests copied from the existing `internal/orchestrator/dag_test.go`, but change the package to:

```go
package orchestration
```

Keep tests for `Validate`, `NewScheduler`, `Append`, `Render`, and JSON path rendering.

- [ ] **Step 2: Run shared package tests and verify they fail**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/orchestration
```

Expected: FAIL because `internal/orchestration` does not exist or does not define the required symbols.

- [ ] **Step 3: Move DAG implementation**

Create `multi-agent/internal/orchestration/dag.go` by moving the full current contents of `multi-agent/internal/orchestrator/dag.go` and changing the package line to:

```go
package orchestration
```

Keep the existing imports, constants, `FinishedNode`, `Scheduler`, `Validate`, `Render`, and helpers.

- [ ] **Step 4: Add orchestrator compatibility aliases**

Replace `multi-agent/internal/orchestrator/dag.go` with:

```go
package orchestrator

import (
	"github.com/yourorg/multi-agent/internal/orchestration"
	"github.com/yourorg/multi-agent/internal/planner"
)

const MaxNodes = orchestration.MaxNodes

type FinishedNode = orchestration.FinishedNode
type Scheduler = orchestration.Scheduler

func Validate(nodes []planner.Node) error {
	return orchestration.Validate(nodes)
}

func NewScheduler(nodes []planner.Node, maxConc int) *Scheduler {
	return orchestration.NewScheduler(nodes, maxConc)
}

func Render(template string, outputs map[string]string) (string, error) {
	return orchestration.Render(template, outputs)
}
```

- [ ] **Step 5: Run orchestrator and orchestration tests**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/orchestration ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

```bash
git add multi-agent/internal/orchestration/dag.go multi-agent/internal/orchestration/dag_test.go multi-agent/internal/orchestrator/dag.go
git commit -m "Extract shared orchestration DAG primitives"
```

---

### Task 4: Extract Shared Summary Helper

**Files:**
- Create: `multi-agent/internal/orchestration/summary.go`
- Create: `multi-agent/internal/orchestration/summary_test.go`
- Modify: `multi-agent/internal/orchestrator/fanout.go`

- [ ] **Step 1: Write failing summary test**

Create `multi-agent/internal/orchestration/summary_test.go`:

```go
package orchestration

import (
	"errors"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/planner"
)

func TestFallbackReduceSummaryPreservesCompletedOutputs(t *testing.T) {
	got := FallbackReduceSummary([]planner.SubResult{
		{NodeID: "n1", Status: "completed", Output: "analysis output"},
		{NodeID: "n2", Status: "failed", Error: "tool failed"},
	}, errors.New("reducer unavailable"))

	for _, want := range []string{
		"# Task Summary",
		"Reducer failed: reducer unavailable",
		"analysis output",
		"Error: tool failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Run summary test and verify it fails**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/orchestration -run TestFallbackReduceSummaryPreservesCompletedOutputs
```

Expected: FAIL because `FallbackReduceSummary` is not defined.

- [ ] **Step 3: Move fallback summary implementation**

Create `multi-agent/internal/orchestration/summary.go`:

```go
package orchestration

import (
	"strings"

	"github.com/yourorg/multi-agent/internal/planner"
)

func FallbackReduceSummary(results []planner.SubResult, reduceErr error) string {
	var sb strings.Builder
	sb.WriteString("# Task Summary\n\n")
	sb.WriteString("Reducer failed: ")
	sb.WriteString(reduceErr.Error())
	sb.WriteString("\n\n")
	sb.WriteString("Completed sub-task outputs are preserved below.\n")
	for _, r := range results {
		sb.WriteString("\n## ")
		sb.WriteString(r.NodeID)
		sb.WriteString(" (")
		sb.WriteString(r.Status)
		sb.WriteString(")\n\n")
		switch {
		case r.Status == "completed" && r.Output != "":
			sb.WriteString(TruncateForSummary(r.Output, 3000))
		case r.Error != "":
			sb.WriteString("Error: ")
			sb.WriteString(r.Error)
		default:
			sb.WriteString("No output.")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func TruncateForSummary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n\n[truncated]\n"
}
```

- [ ] **Step 4: Use shared summary from master fanout**

In `multi-agent/internal/orchestrator/fanout.go`, add import:

```go
"github.com/yourorg/multi-agent/internal/orchestration"
```

Replace:

```go
summary = fallbackReduceSummary(results, rerr)
```

with:

```go
summary = orchestration.FallbackReduceSummary(results, rerr)
```

Delete local `fallbackReduceSummary` and `truncateForSummary` from `fanout.go`.

- [ ] **Step 5: Run tests**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/orchestration ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 6: Commit Task 4**

```bash
git add multi-agent/internal/orchestration/summary.go multi-agent/internal/orchestration/summary_test.go multi-agent/internal/orchestrator/fanout.go
git commit -m "Extract shared orchestration summary helper"
```

---

### Task 5: Add Minimal Driver-Managed Fanout Runner

**Files:**
- Create: `multi-agent/internal/orchestration/driver_runner.go`
- Create: `multi-agent/internal/orchestration/driver_runner_test.go`
- Modify: `multi-agent/internal/driver/contract_tools.go`

- [ ] **Step 1: Define narrow runner interfaces and test**

Create `multi-agent/internal/orchestration/driver_runner_test.go` with:

```go
package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/planner"
)

type fakeRunnerPlanner struct {
	nodes   []planner.Node
	summary string
}

func (f fakeRunnerPlanner) Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]planner.Node, error) {
	return f.nodes, nil
}

func (f fakeRunnerPlanner) Reduce(ctx context.Context, prompt string, results []planner.SubResult) (string, error) {
	return f.summary, nil
}

type fakeRunnerSDK struct {
	cards     []agentsdk.AgentCard
	delegated []agentsdk.DelegateTaskRequest
}

func (f *fakeRunnerSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.cards, nil
}

func (f *fakeRunnerSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.delegated = append(f.delegated, req)
	return &agentsdk.DelegateTaskResponse{TaskID: "child-1"}, nil
}

func (f *fakeRunnerSDK) GetTask(ctx context.Context, taskID string, includeOutput bool) (*agentsdk.TaskInfo, error) {
	return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed", Result: json.RawMessage(`"child output"`)}, nil
}

func TestDriverRunnerExecutesPlannedNodeAndReduces(t *testing.T) {
	sdk := &fakeRunnerSDK{cards: []agentsdk.AgentCard{
		{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
	}}
	runner := NewDriverRunner(fakeRunnerPlanner{
		nodes: []planner.Node{{ID: "n1", TargetID: "slave-a", Skill: "chat", Prompt: "do work"}},
		summary: "final answer",
	}, sdk, RunnerConfig{MaxConcurrency: 2, ChildTimeoutSec: 30})

	got, err := runner.Run(context.Background(), "original prompt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Summary != "final answer" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if len(sdk.delegated) != 1 || sdk.delegated[0].TargetID != "slave-a" {
		t.Fatalf("delegated = %#v", sdk.delegated)
	}
}
```

- [ ] **Step 2: Run runner test and verify it fails**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/orchestration -run TestDriverRunnerExecutesPlannedNodeAndReduces
```

Expected: FAIL because `NewDriverRunner`, `RunnerConfig`, and result types are not defined.

- [ ] **Step 3: Implement minimal runner**

Create `multi-agent/internal/orchestration/driver_runner.go`:

```go
package orchestration

import (
	"context"
	"fmt"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/planner"
)

type RunnerPlanner interface {
	Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]planner.Node, error)
	Reduce(ctx context.Context, prompt string, results []planner.SubResult) (string, error)
}

type RunnerSDK interface {
	DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
	DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	GetTask(ctx context.Context, taskID string, includeOutput bool) (*agentsdk.TaskInfo, error)
}

type RunnerConfig struct {
	MaxConcurrency  int
	ChildTimeoutSec int
}

type RunnerResult struct {
	Summary string
}

type DriverRunner struct {
	planner RunnerPlanner
	sdk     RunnerSDK
	cfg     RunnerConfig
}

func NewDriverRunner(p RunnerPlanner, sdk RunnerSDK, cfg RunnerConfig) *DriverRunner {
	return &DriverRunner{planner: p, sdk: sdk, cfg: cfg}
}

func (r *DriverRunner) Run(ctx context.Context, prompt string) (RunnerResult, error) {
	agents, err := r.sdk.DiscoverAgents(ctx)
	if err != nil {
		return RunnerResult{}, err
	}
	nodes, err := r.planner.Plan(ctx, prompt, agents)
	if err != nil {
		return RunnerResult{}, err
	}
	if err := Validate(nodes); err != nil {
		return RunnerResult{}, err
	}
	sched := NewScheduler(nodes, r.cfg.MaxConcurrency)
	outputs := map[string]string{}
	results := []planner.SubResult{}
	for !sched.Done() {
		ready := sched.Ready()
		if len(ready) == 0 {
			return RunnerResult{}, fmt.Errorf("driver runner stalled")
		}
		for _, n := range ready {
			rendered, err := Render(n.Prompt, outputs)
			if err != nil {
				return RunnerResult{}, err
			}
			sched.MarkDispatched(n.ID)
			resp, err := r.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
				TargetID:       n.TargetID,
				Skill:          n.Skill,
				Prompt:         rendered,
				SystemContext:  n.SystemContext,
				TimeoutSeconds: r.cfg.ChildTimeoutSec,
			})
			if err != nil {
				return RunnerResult{}, err
			}
			info, err := r.sdk.GetTask(ctx, resp.TaskID, true)
			if err != nil {
				return RunnerResult{}, err
			}
			output := info.Output
			if output == "" && len(info.Result) > 0 {
				output = string(info.Result)
			}
			status := info.Status
			errMsg := ""
			if status != "completed" {
				errMsg = info.FailureReason
				if errMsg == "" {
					errMsg = status
				}
			}
			sched.Report(n.ID, status, output, errMsg)
			if status == "completed" {
				outputs[n.ID] = output
			}
			results = append(results, planner.SubResult{NodeID: n.ID, TargetID: n.TargetID, Prompt: rendered, Status: status, Output: output, Error: errMsg})
			if status != "completed" && !n.Optional {
				return RunnerResult{}, fmt.Errorf("required node %s %s: %s", n.ID, status, errMsg)
			}
		}
	}
	summary, err := r.planner.Reduce(ctx, prompt, results)
	if err != nil {
		summary = FallbackReduceSummary(results, err)
	}
	return RunnerResult{Summary: summary}, nil
}
```

- [ ] **Step 4: Run runner test**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/orchestration
```

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

```bash
git add multi-agent/internal/orchestration/driver_runner.go multi-agent/internal/orchestration/driver_runner_test.go
git commit -m "Add minimal driver orchestration runner"
```

---

### Task 6: Wire Driver Fanout Into `submit_contract_task`

**Files:**
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/contract_tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Add fake contract runner test support**

In `multi-agent/internal/driver/tools_test.go`, add this helper near `fakeObserver`:

```go
type fakeContractRunner struct {
	prompt string
	result orchestration.RunnerResult
	err    error
}

func (f *fakeContractRunner) Run(ctx context.Context, prompt string) (orchestration.RunnerResult, error) {
	f.prompt = prompt
	return f.result, f.err
}
```

Add this import to the same file:

```go
"github.com/yourorg/multi-agent/internal/orchestration"
```

- [ ] **Step 2: Write failing submit driver fanout test**

Add to `multi-agent/internal/driver/tools_test.go`:

```go
func TestSubmitContractTaskUsesDriverFanoutWhenRecommended(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "builder", DisplayName: "builder", Status: "available", Card: json.RawMessage(`{"skills":["build_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("submit_contract_task should use the configured driver runner for driver_fanout")
			return nil, nil
		},
	}
	runner := &fakeContractRunner{result: orchestration.RunnerResult{Summary: "driver summary"}}
	tools := newTestTools(t, sdk)
	tools.SetContractRunner(runner)

	tc := testTaskContract()
	tc.ExecutionPolicy.AllowBuildMCP = true
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc, "prompt": "analyze refunds"})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"driver_fanout"`)
	require.Contains(t, string(out), `"summary":"driver summary"`)
	require.Contains(t, runner.prompt, contract.EnvelopeStart)
}
```

- [ ] **Step 3: Run submit driver fanout test and verify it fails**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver -run TestSubmitContractTaskUsesDriverFanoutWhenRecommended
```

Expected: FAIL because driver fanout currently returns the explicit not-implemented error from Task 2.

- [ ] **Step 4: Add runner field to `Tools`**

In `multi-agent/internal/driver/tools.go`, add:

```go
type ContractRunner interface {
	Run(ctx context.Context, prompt string) (orchestration.RunnerResult, error)
}
```

Add a field to `Tools`:

```go
contractRunner ContractRunner
```

Leave `contractRunner` nil in `NewTools` for this task. Add this setter so tests and later command wiring can inject the runner explicitly:

```go
func (t *Tools) SetContractRunner(r ContractRunner) {
	t.contractRunner = r
}
```

- [ ] **Step 5: Execute driver fanout route**

In `submit_contract_task.Call`, when `report.RecommendedRoute == routeDriverFanout`, run:

```go
if s.t.contractRunner == nil {
	return nil, &MCPToolError{Message: "driver_fanout route is recommended but no driver contract runner is configured"}
}
runnerResult, err := s.t.contractRunner.Run(ctx, finalPrompt)
if err != nil {
	return nil, &MCPToolError{Message: "driver fanout: " + err.Error()}
}
return json.Marshal(map[string]interface{}{
	"route":             routeDriverFanout,
	"summary":           runnerResult.Summary,
	"resource_snapshot": snapshot,
	"warnings":          warnings,
})
```

- [ ] **Step 6: Run driver tests**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver
```

Expected: PASS.

- [ ] **Step 7: Commit Task 6**

```bash
git add multi-agent/internal/driver/tools.go multi-agent/internal/driver/contract_tools.go multi-agent/internal/driver/tools_test.go
git commit -m "Wire driver fanout route into contract submit"
```

---

### Task 7: Preserve Master Compatibility

**Files:**
- Modify: `multi-agent/internal/driver/tools_test.go`
- Modify: `multi-agent/examples/dynamic-mcp/README.md`
- Modify: `multi-agent/examples/image-pipeline/README.md`

- [ ] **Step 1: Add regression test for explicit master route**

Add to `multi-agent/internal/driver/tools_test.go`:

```go
func TestSubmitContractTaskMasterOnlyStillDelegatesToMasterFanout(t *testing.T) {
	var last agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "s1", DisplayName: "slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			last = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-master"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"master_fanout"`)
	require.Equal(t, "m1", last.TargetID)
	require.Equal(t, "fanout", last.Skill)
	require.Contains(t, last.Prompt, contract.EnvelopeStart)
}
```

- [ ] **Step 2: Run regression test**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./internal/driver -run TestSubmitContractTaskMasterOnlyStillDelegatesToMasterFanout
```

Expected: PASS after Tasks 2 and 6.

- [ ] **Step 3: Update example docs**

In `multi-agent/examples/dynamic-mcp/README.md` and `multi-agent/examples/image-pipeline/README.md`, add a short compatibility note:

```markdown
### Routing Note

Driver-first orchestration is the default for clarified contract workflows.
Set `execution_policy.routing` to `master_only` when this example should keep
using the master `fanout` executor for compatibility or batch-style execution.
```

- [ ] **Step 4: Run docs-adjacent tests**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./examples/dynamic-mcp ./examples/image-pipeline/...
```

Expected: PASS.

- [ ] **Step 5: Commit Task 7**

```bash
git add multi-agent/internal/driver/tools_test.go multi-agent/examples/dynamic-mcp/README.md multi-agent/examples/image-pipeline/README.md
git commit -m "Preserve explicit master fanout route"
```

---

### Task 8: Add Driver-First Example Documentation

**Files:**
- Create: `multi-agent/examples/driver-first/README.md`

- [ ] **Step 1: Create example README**

Create `multi-agent/examples/driver-first/README.md`:

```markdown
# Driver-First Orchestration Example

This example documents the intended driver-first contract flow.

## Direct Slave

Use this when exactly one slave satisfies the required skills and MCP tools.

Expected dry-run route:

```json
{
  "recommended_route": "direct_slave",
  "recommended_skill": "chat"
}
```

## Driver Fanout

Use this when the driver needs to coordinate multiple slaves, call tools across
slaves, build MCP services, or pause for follow-up clarification.

Expected dry-run route:

```json
{
  "recommended_route": "driver_fanout",
  "recommended_skill": "fanout"
}
```

## Master Fanout

Use this when compatibility or operator policy requires the remote master.

Expected dry-run route:

```json
{
  "recommended_route": "master_fanout",
  "recommended_skill": "fanout"
}
```

## Artifact Transport

Distributed driver, master, and slave deployments must exchange user files,
generated MCP code, and final reports through observer artifacts. Do not rely on
shared local filesystem paths for cross-machine execution.
```
```

- [ ] **Step 2: Verify markdown file exists**

Run:

```bash
test -f multi-agent/examples/driver-first/README.md
```

Expected: exit code 0.

- [ ] **Step 3: Commit Task 8**

```bash
git add multi-agent/examples/driver-first/README.md
git commit -m "Document driver-first orchestration examples"
```

---

### Task 9: Full Verification

**Files:**
- No planned source changes.

- [ ] **Step 1: Run full Go test suite**

Run:

```bash
/root/.local/go-toolchain/go/bin/go test -count=1 ./...
```

Expected: PASS for all packages.

- [ ] **Step 2: Inspect final git history**

Run:

```bash
git log --oneline --decorate -8
```

Expected: recent commits show each completed task in order on branch `driver-first-orchestration`.

- [ ] **Step 3: Inspect worktree status**

Run:

```bash
git status --short --branch
```

Expected: clean working tree on `driver-first-orchestration`.

---

## Spec Coverage Review

- Driver close to user: covered by route recommendation, driver fanout route, and later clarification checkpoints.
- Direct driver-to-slave: covered by Tasks 1, 2, and 7.
- Driver-managed multi-slave DAG: covered by Tasks 3, 4, 5, and 6.
- Master compatibility: covered by Tasks 2 and 7.
- Observer artifact transport: preserved in existing master path; driver fanout runner starts minimal and must not introduce shared filesystem assumptions.
- Recorded snapshots and contracts: preserved in `submit_contract_task`; route is added to response for auditability.
- `dry_run_contract` side-effect free: explicitly tested in Task 1.
- Future agentserver-native support: documented in the spec; no implementation task required here.

## Deferred Work After This Plan

- Full interactive clarification checkpoints for `build_mcp_blocked`, invalid MCP args, and uncertain reducer output.
- Container E2E with separate driver, master, and slave containers.
- Driver-managed observer artifact GET/PUT parity with all master fanout behavior.
- Durable generated MCP metadata audit records beyond current observer events.
