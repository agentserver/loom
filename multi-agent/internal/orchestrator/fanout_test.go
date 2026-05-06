package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/executor"
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
	require.NoError(t, err) // best_effort: parent still completed (reducer summarizes failure)
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
