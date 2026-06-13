package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/store"
)

// funcSDK is a callback-driven SDK fake used by resume tests so individual
// cases can inspect exactly which task IDs were dispatched/awaited.
type funcSDK struct {
	mu           sync.Mutex
	agents       []agentsdk.AgentCard
	delegateFunc func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	waitFunc     func(taskID string) (*agentsdk.TaskInfo, error)

	dispatched []agentsdk.DelegateTaskRequest
	waited     []string
}

func (f *funcSDK) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}

func (f *funcSDK) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	f.dispatched = append(f.dispatched, req)
	f.mu.Unlock()
	if f.delegateFunc != nil {
		return f.delegateFunc(req)
	}
	return &agentsdk.DelegateTaskResponse{TaskID: "stub-child"}, nil
}

func (f *funcSDK) WaitForTask(_ context.Context, taskID string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	f.mu.Lock()
	f.waited = append(f.waited, taskID)
	f.mu.Unlock()
	if f.waitFunc != nil {
		return f.waitFunc(taskID)
	}
	return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed"}, nil
}

// TestOrchestrator_DuplicateRunCompletedReturnsOutput verifies that delivering
// the same parent task ID twice (driver restart, poller redelivery) returns
// the original stored output without re-running anything.
// Fixes §1.2 #5 of docs/review-2026-06-13.md.
func TestOrchestrator_DuplicateRunCompletedReturnsOutput(t *testing.T) {
	sdk := &funcSDK{}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	// Pre-seed completed parent in the store the orchestrator was given.
	_, err := o.store.InsertIfAbsent(store.Task{ID: "p-1", Skill: "fanout", Prompt: "go"})
	require.NoError(t, err)
	require.NoError(t, o.store.MarkRunning("p-1"))
	require.NoError(t, o.store.Complete("p-1", "previously-finished-summary"))

	res, err := o.Run(context.Background(), executor.Task{ID: "p-1", Skill: "fanout", Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, "previously-finished-summary", res.Summary)

	// Nothing should have been dispatched / awaited — this is a pure replay.
	require.Empty(t, sdk.dispatched, "completed task replay must not dispatch")
	require.Empty(t, sdk.waited, "completed task replay must not wait")
}

// TestOrchestrator_DuplicateRunFailedReturnsError verifies that a previously
// failed parent re-delivered to Run returns the stored error without
// re-running.
func TestOrchestrator_DuplicateRunFailedReturnsError(t *testing.T) {
	sdk := &funcSDK{}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	_, err := o.store.InsertIfAbsent(store.Task{ID: "p-failed", Skill: "fanout", Prompt: "go"})
	require.NoError(t, err)
	require.NoError(t, o.store.MarkRunning("p-failed"))
	require.NoError(t, o.store.Fail("p-failed", "earlier boom"))

	_, err = o.Run(context.Background(), executor.Task{ID: "p-failed", Skill: "fanout", Prompt: "go"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "earlier boom")
	require.Empty(t, sdk.dispatched)
}

// TestOrchestrator_ResumeFromExistingSubTasks verifies that on restart, a
// parent task with status='running' and existing sub_tasks rows is resumed:
// completed nodes are not re-dispatched, assigned nodes are awaited via
// WaitForTask, only pending nodes are newly dispatched.
func TestOrchestrator_ResumeFromExistingSubTasks(t *testing.T) {
	var (
		delegateMu  sync.Mutex
		delegatePromptToID = map[string]string{
			"do b": "child-b",
			"do c": "child-c",
		}
	)
	sdk := &funcSDK{
		agents: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegateMu.Lock()
			defer delegateMu.Unlock()
			id, ok := delegatePromptToID[req.Prompt]
			if !ok {
				id = "child-unknown"
			}
			return &agentsdk.DelegateTaskResponse{TaskID: id}, nil
		},
		waitFunc: func(taskID string) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed", Output: taskID + "-result"}, nil
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	// Seed parent in running state.
	_, err := o.store.InsertIfAbsent(store.Task{ID: "p-2", Skill: "fanout", Prompt: "x"})
	require.NoError(t, err)
	require.NoError(t, o.store.MarkRunning("p-2"))

	// Seed 3 sub_tasks: one completed, one assigned (child task in flight),
	// one pending. 'c' depends on 'a' so it's already ready on resume.
	require.NoError(t, o.store.InsertSubTasks("p-2", []store.SubTaskRow{
		{ParentID: "p-2", NodeID: "a", TargetID: "slave-a", Prompt: "do a", Status: "completed", Output: "a-result"},
		{ParentID: "p-2", NodeID: "b", TargetID: "slave-a", Prompt: "do b", Status: "assigned", ChildTaskID: "child-b"},
		{ParentID: "p-2", NodeID: "c", TargetID: "slave-a", Prompt: "do c", Status: "pending", DependsOn: []string{"a"}},
	}))

	_, err = o.Run(context.Background(), executor.Task{ID: "p-2", Skill: "fanout", Prompt: "x"})
	require.NoError(t, err)

	// Assertion 1: completed node 'a' must NOT have been re-dispatched.
	for _, req := range sdk.dispatched {
		require.NotEqual(t, "do a", req.Prompt,
			"completed node 'a' was re-dispatched; dispatched=%v", sdk.dispatched)
	}
	// Assertion 2: assigned node 'b' must have triggered WaitForTask(child-b).
	require.Contains(t, sdk.waited, "child-b",
		"assigned node 'b' must trigger WaitForTask(child-b); waited=%v", sdk.waited)
	// Assertion 3: pending node 'c' must have been dispatched.
	dispatchedPrompts := make([]string, len(sdk.dispatched))
	for i, req := range sdk.dispatched {
		dispatchedPrompts[i] = req.Prompt
	}
	require.Contains(t, dispatchedPrompts, "do c",
		"pending node 'c' must be dispatched; dispatched=%v", dispatchedPrompts)

	// Assertion 4: master_task_resumed event must have been emitted with stats.
	var sawResumed bool
	for _, ev := range obs.events {
		if ev.Type == observer.EventMasterTaskResumed && ev.TaskID == "p-2" {
			sawResumed = true
			break
		}
	}
	require.True(t, sawResumed, "expected EventMasterTaskResumed; events=%v", obs.events)
}

// TestOrchestrator_ResumeRefusesPendingMCPNode verifies that a pending node
// whose prompt looks like an MCP call ({server,tool,args}) is NOT silently
// re-dispatched as chat on resume — that would feed structured MCP args to
// an LLM and produce degraded output the user couldn't distinguish from a
// real run. Instead, resume fails fast with a clear error.
func TestOrchestrator_ResumeRefusesPendingMCPNode(t *testing.T) {
	sdk := &funcSDK{
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must NOT dispatch likely-MCP node on resume; got %+v", req)
			return nil, nil
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	_, err := o.store.InsertIfAbsent(store.Task{ID: "p-mcp", Skill: "fanout", Prompt: "x"})
	require.NoError(t, err)
	require.NoError(t, o.store.MarkRunning("p-mcp"))
	require.NoError(t, o.store.InsertSubTasks("p-mcp", []store.SubTaskRow{
		{ParentID: "p-mcp", NodeID: "n1", TargetID: "slave-a",
			Prompt: `{"server":"weather","tool":"forecast","args":{"city":"sf"}}`,
			Status: "pending"},
	}))

	_, err = o.Run(context.Background(), executor.Task{
		ID: "p-mcp", Skill: "fanout", Prompt: "x",
	})
	require.Error(t, err, "resume must fail fast for MCP-shaped pending nodes")
	require.Contains(t, err.Error(), "MCP")
	require.Empty(t, sdk.dispatched, "no MCP-shaped node should reach DelegateTask")
}
