package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
)

// fakeSDKQueue lets each child task return a queued (status, output) pair keyed by request order.
type fakeSDKQueue struct {
	mu         sync.Mutex
	agents     []agentsdk.AgentCard
	nextID     int
	queue      []agentsdk.TaskInfo
	dispatched []agentsdk.DelegateTaskRequest
}

func (f *fakeSDKQueue) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}
func (f *fakeSDKQueue) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, req)
	f.nextID++
	return &agentsdk.DelegateTaskResponse{TaskID: fmt.Sprintf("c%d", f.nextID)}, nil
}
func (f *fakeSDKQueue) WaitForTask(_ context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return &agentsdk.TaskInfo{TaskID: id, Status: "failed"}, nil
	}
	info := f.queue[0]
	f.queue = f.queue[1:]
	info.TaskID = id
	return &info, nil
}

type cancelAwareSDK struct {
	mu         sync.Mutex
	agents     []agentsdk.AgentCard
	dispatched []agentsdk.DelegateTaskRequest
}

func (f *cancelAwareSDK) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}

func (f *cancelAwareSDK) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, req)
	return &agentsdk.DelegateTaskResponse{TaskID: req.Prompt}, nil
}

func (f *cancelAwareSDK) WaitForTask(ctx context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	if id == "fail" {
		return &agentsdk.TaskInfo{TaskID: id, Status: "failed", FailureReason: "boom"}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type nonCooperativeSDK struct {
	mu         sync.Mutex
	agents     []agentsdk.AgentCard
	dispatched []agentsdk.DelegateTaskRequest
}

func (f *nonCooperativeSDK) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}

func (f *nonCooperativeSDK) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, req)
	return &agentsdk.DelegateTaskResponse{TaskID: req.Prompt}, nil
}

func (f *nonCooperativeSDK) WaitForTask(_ context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	if id == "fail" {
		return &agentsdk.TaskInfo{TaskID: id, Status: "failed", FailureReason: "boom"}, nil
	}
	select {}
}

func TestFanout_HappyDiamond(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
			{AgentID: "agent-c", Status: "available"},
			{AgentID: "agent-d", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: "out-1"},
			{Status: "completed", Output: "out-2"},
			{Status: "completed", Output: "out-3"},
			{Status: "completed", Output: "out-4"},
		},
	}
	o := newOrch(t, sdk, "plan_diamond")
	res, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	_ = res
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 4)
}

func TestFanout_BestEffortPartialFailure(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed"},
		},
	}
	o := newOrch(t, sdk, "plan_chain")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required node a failed")
	// Only "a" was dispatched; "b" was skipped.
	require.Len(t, sdk.dispatched, 1)
}

func TestFanout_AllOrNothingFailsImmediately(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed"},
		},
	}
	o := newOrch(t, sdk, "plan_chain")
	o.cfg.PolicyBySkill = map[string]string{"fanout": "all_or_nothing"}

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
}

func TestFanout_AllOrNothingEmitsTerminalEventsForInFlightSiblings(t *testing.T) {
	sdk := &cancelAwareSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_parallel", obs)
	o.cfg.PolicyBySkill = map[string]string{"fanout": "all_or_nothing"}

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
	require.Len(t, sdk.dispatched, 2)

	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	require.Len(t, done, 2)
	seen := map[string]string{}
	for _, ev := range done {
		seen[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "failed", seen["fail"])
	require.Equal(t, "failed", seen["slow"])
}

func TestFanout_RequiredFailureDrainBoundedForNonCooperativeSibling(t *testing.T) {
	sdk := &nonCooperativeSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
	}
	o := newOrch(t, sdk, "plan_parallel")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := o.Run(ctx, executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "required node fail failed")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Run did not return after required failure and context cancellation")
	}
}

func TestFanout_PassesNodeSkillToDelegateTask(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithTool(t, "x", "y")},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	o := newOrch(t, sdk, "plan_with_skill")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill,
		"orchestrator must thread Node.Skill into DelegateTask")
}

func TestFanout_InvalidMCPArgsRejectedBeforeDispatch(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_mcp_invalid_arg", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown argument put_url_128")
	require.Len(t, sdk.dispatched, 0)

	validationFailed := firstEventOfType(t, obs.events, observer.EventMasterMCPCallValidationFailed)
	require.Equal(t, "p", validationFailed.TaskID)
	require.Equal(t, "n0", validationFailed.SubtaskID)
	require.Equal(t, "agent-a", validationFailed.TargetAgentID)
	require.Equal(t, observer.RoleSlave, validationFailed.TargetRole)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(validationFailed.Payload, &payload))
	require.Contains(t, payload["validation_error"], "unknown argument put_url_128")
	require.Equal(t, true, payload["required"])
	require.NotEmpty(t, payload["prompt"])
}

func TestFanout_InvalidMCPArgsTriggersBoundedReplan(t *testing.T) {
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_mcp_validation_replan", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill)
	require.JSONEq(t, `{"server":"srv","tool":"render","args":{"n":7}}`, sdk.dispatched[0].Prompt)

	validationFailed := eventsOfType(obs.events, observer.EventMasterMCPCallValidationFailed)
	require.Len(t, validationFailed, 1)
	require.Equal(t, "n0", validationFailed[0].SubtaskID)
	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	statusByNode := map[string]string{}
	for _, ev := range done {
		statusByNode[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "skipped", statusByNode["n0"])
	require.Equal(t, "completed", statusByNode["n0_n1"])
}

func TestFanout_ValidMCPArgsDispatchesOnce(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	o := newOrch(t, sdk, "plan_mcp_valid")

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill)
}

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

func TestFanout_RequiredFailureFailsParentUnderBestEffort(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed", FailureReason: "boom"},
			{Status: "completed", Output: "ok"},
		},
	}
	o := newOrch(t, sdk, "plan_optional_failure")

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "required node")
}

func TestFanout_RequiredFailureMarksDownstreamSkipped(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed", FailureReason: "boom"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	statusByNode := map[string]string{}
	for _, ev := range done {
		statusByNode[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "failed", statusByNode["a"])
	require.Equal(t, "skipped", statusByNode["b"])

	requiredFailed := eventsOfType(obs.events, observer.EventMasterRequiredNodeFailed)
	require.Len(t, requiredFailed, 2)
	require.Equal(t, "a", requiredFailed[0].SubtaskID)
	require.Equal(t, "b", requiredFailed[1].SubtaskID)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(requiredFailed[1].Payload, &payload))
	require.Equal(t, true, payload["required"])
	require.Equal(t, "b", payload["node_id"])
	require.Equal(t, "skipped", payload["status"])
}

func TestFanout_OptionalFailureReducedUnderBestEffort(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: "ok"},
			{Status: "failed", FailureReason: "optional boom"},
		},
	}
	o := newOrch(t, sdk, "plan_optional_failure")

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 2)
}

func TestFanout_BuildMCPBlocked_TriggersReplan(t *testing.T) {
	// negotiate_then_succeed: round 0 emits build n0; round 1 emits build n1;
	// round 2 emits use n2. SDK returns blocked, then tool_set, then ok.
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	blocked := `{"type":"build_mcp_blocked","url":"","meta":{"spec_name":"foo","iteration":"1","needed_packages":"requests","reason":"r"}}`
	toolSet := `{"type":"mcp_tool_set","url":"file:///x","meta":{"name":"foo","version":"1","tools":"a","iteration":"2"}}`

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithTool(t, "foo", "a")},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: blocked}, // n0
			{Status: "completed", Output: toolSet}, // n1
			{Status: "completed", Output: "ok"},    // n2 (use)
		},
	}
	o := newOrch(t, sdk, "negotiate_then_succeed")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 3, "n0 build, n1 build, n2 use")
}

func TestFanout_BuildMCPBlocked_HitsIterationCap(t *testing.T) {
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	blocked := `{"type":"build_mcp_blocked","url":"","meta":{"spec_name":"foo","iteration":"X","needed_packages":"y","reason":"r"}}`

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: blocked},
			{Status: "completed", Output: blocked},
			{Status: "completed", Output: blocked},
			{Status: "completed", Output: blocked},
		},
	}
	o := newOrch(t, sdk, "negotiate_forever")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exhausted")
	// 3 build_mcp dispatches before giving up.
	require.Len(t, sdk.dispatched, 3)
}

func TestFanout_EmitsPlanDispatchAndDoneEvents(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithTool(t, "x", "y")},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_with_skill", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "build something"})
	require.NoError(t, err)

	planCreated := firstEventOfType(t, obs.events, observer.EventMasterPlanCreated)
	require.Equal(t, "p", planCreated.TaskID)
	var planPayload map[string][]string
	require.NoError(t, json.Unmarshal(planCreated.Payload, &planPayload))
	require.Equal(t, []string{"n0"}, planPayload["node_ids"])

	dispatched := eventsOfType(obs.events, observer.EventMasterSubtaskDispatched)
	require.Len(t, dispatched, 1)
	require.Equal(t, "p", dispatched[0].TaskID)
	require.Equal(t, "n0", dispatched[0].SubtaskID)
	require.Equal(t, "c1", dispatched[0].ChildTaskID)
	require.Equal(t, "agent-a", dispatched[0].TargetAgentID)
	require.Equal(t, observer.RoleSlave, dispatched[0].TargetRole)
	require.Equal(t, "assigned", dispatched[0].Status)
	require.Equal(t, "build something", dispatched[0].Summary)
	require.Equal(t, "y", dispatched[0].SubtaskSummary)

	done := firstEventOfType(t, obs.events, observer.EventMasterSubtaskDone)
	require.Equal(t, "p", done.TaskID)
	require.Equal(t, "n0", done.SubtaskID)
	require.Equal(t, "c1", done.ChildTaskID)
	require.Equal(t, "completed", done.Status)
	var donePayload map[string]string
	require.NoError(t, json.Unmarshal(done.Payload, &donePayload))
	require.Equal(t, "ok", donePayload["output"])
}

func TestFanout_EmitsMCPReplanForHandleOutputs(t *testing.T) {
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	blocked := `{"type":"build_mcp_blocked","url":"","meta":{"spec_name":"foo","iteration":"1","needed_packages":"requests","reason":"r"}}`
	toolSet := `{"type":"mcp_tool_set","url":"file:///x","meta":{"name":"foo","version":"1","tools":"a","iteration":"2"}}`

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithTool(t, "foo", "a")},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: blocked},
			{Status: "completed", Output: toolSet},
			{Status: "completed", Output: "ok"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "negotiate_then_succeed", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err)

	replans := eventsOfType(obs.events, observer.EventMasterMCPReplan)
	require.Len(t, replans, 2)
	require.Equal(t, "build_mcp_blocked", replans[0].Status)
	require.Equal(t, "foo", replans[0].MCPServerName)
	require.Equal(t, "n0", replans[0].SubtaskID)
	require.Equal(t, "c1", replans[0].ChildTaskID)
	var blockedPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(replans[0].Payload, &blockedPayload))
	require.Equal(t, "build_mcp_blocked", blockedPayload["type"])

	require.Equal(t, "mcp_tool_set", replans[1].Status)
	require.Equal(t, "foo", replans[1].MCPServerName)
	require.Equal(t, "n0_n1", replans[1].SubtaskID)
	require.Equal(t, "c2", replans[1].ChildTaskID)
}

func eventsOfType(events []observer.Event, typ string) []observer.Event {
	var out []observer.Event
	for _, ev := range events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func firstEventOfType(t *testing.T, events []observer.Event, typ string) observer.Event {
	t.Helper()
	matches := eventsOfType(events, typ)
	require.NotEmpty(t, matches, "event type %s not emitted", typ)
	return matches[0]
}

func agentWithRenderTool(t *testing.T) agentsdk.AgentCard {
	t.Helper()
	return agentWithTool(t, "srv", "render")
}

func agentWithTool(t *testing.T, server, tool string) agentsdk.AgentCard {
	t.Helper()
	card := json.RawMessage(`{
		"mcp_tools":[{
			"server":` + strconv.Quote(server) + `,
			"name":` + strconv.Quote(tool) + `,
			"input_schema":{
				"type":"object",
				"properties":{"n":{"type":"number"}},
				"required":[]
			}
		}]
	}`)
	return agentsdk.AgentCard{AgentID: "agent-a", Status: "available", Card: card}
}
