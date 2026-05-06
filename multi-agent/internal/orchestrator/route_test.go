package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
)

type fakeSDK struct {
	agents       []agentsdk.AgentCard
	delegateResp *agentsdk.DelegateTaskResponse
	delegateErr  error
	waitInfo     *agentsdk.TaskInfo
	waitErr      error

	delegatedReqs []agentsdk.DelegateTaskRequest
}

func (f *fakeSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}
func (f *fakeSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.delegatedReqs = append(f.delegatedReqs, req)
	return f.delegateResp, f.delegateErr
}
func (f *fakeSDK) WaitForTask(ctx context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	return f.waitInfo, f.waitErr
}

func newOrch(t *testing.T, sdk SDKDelegator, mode string) *Orchestrator {
	t.Helper()
	t.Setenv("FAKE_PLANNER_MODE", mode)
	p := planner.New(config.Planner{Bin: fakePlannerForOrch(t), TimeoutSec: 5})
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id")
}

func fakePlannerForOrch(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fake-planner.sh")
	require.NoError(t, err)
	return p
}

func TestRoute_HappyPath(t *testing.T) {
	sdk := &fakeSDK{
		agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "completed", Output: "child output"},
	}
	o := newOrch(t, sdk, "route_a")

	res, err := o.Run(context.Background(), executor.Task{ID: "p1", Skill: "route", Prompt: "do thing"})
	require.NoError(t, err)
	require.Equal(t, "child output", res.Summary)
	require.Len(t, sdk.delegatedReqs, 1)
	require.Equal(t, "agent-a", sdk.delegatedReqs[0].TargetID)
	require.Equal(t, "do thing", sdk.delegatedReqs[0].Prompt)
}

func TestRoute_NoCandidate(t *testing.T) {
	sdk := &fakeSDK{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
	}
	o := newOrch(t, sdk, "route_empty")

	_, err := o.Run(context.Background(), executor.Task{ID: "p2", Skill: "route", Prompt: "x"})
	require.ErrorContains(t, err, "no candidate")
	require.Empty(t, sdk.delegatedReqs)
}

func TestRoute_ChildFails(t *testing.T) {
	sdk := &fakeSDK{
		agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "failed"},
	}
	o := newOrch(t, sdk, "route_a")
	_, err := o.Run(context.Background(), executor.Task{ID: "p3", Skill: "route", Prompt: "x"})
	require.ErrorContains(t, err, "child")
}

func TestRoute_DelegateError(t *testing.T) {
	sdk := &fakeSDK{
		agents:      []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateErr: errors.New("boom"),
	}
	o := newOrch(t, sdk, "route_a")
	_, err := o.Run(context.Background(), executor.Task{ID: "p4", Skill: "route", Prompt: "x"})
	require.ErrorContains(t, err, "boom")
}

func TestRoute_FiltersSelf(t *testing.T) {
	sdk := &fakeSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "self-id", Status: "available"},
			{AgentID: "agent-a", Status: "available"},
		},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "c"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "c", Status: "completed", Output: "ok"},
	}
	o := newOrch(t, sdk, "route_a")
	_, err := o.Run(context.Background(), executor.Task{ID: "p5", Skill: "route", Prompt: "x"})
	require.NoError(t, err)
	for _, r := range sdk.delegatedReqs {
		require.NotEqual(t, "self-id", r.TargetID)
	}
}
