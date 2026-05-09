package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
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

func TestFanout_PassesNodeSkillToDelegateTask(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	o := newOrch(t, sdk, "plan_with_skill")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill,
		"orchestrator must thread Node.Skill into DelegateTask")
}

func TestFanout_BuildMCPBlocked_TriggersReplan(t *testing.T) {
	// negotiate_then_succeed: round 0 emits build n0; round 1 emits build n1;
	// round 2 emits use n2. SDK returns blocked, then tool_set, then ok.
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	blocked := `{"type":"build_mcp_blocked","url":"","meta":{"spec_name":"foo","iteration":"1","needed_packages":"requests","reason":"r"}}`
	toolSet := `{"type":"mcp_tool_set","url":"file:///x","meta":{"name":"foo","version":"1","tools":"a","iteration":"2"}}`

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
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
