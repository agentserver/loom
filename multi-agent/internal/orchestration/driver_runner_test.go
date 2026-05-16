package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/planner"
)

type fakeRunnerPlanner struct {
	nodes       []planner.Node
	summary     string
	reduceErr   error
	reduceCalls int
	gotResults  []planner.SubResult
	gotPlanArgs []agentsdk.AgentCard
}

func (f *fakeRunnerPlanner) Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]planner.Node, error) {
	f.gotPlanArgs = agents
	return f.nodes, nil
}

func (f *fakeRunnerPlanner) Reduce(ctx context.Context, prompt string, results []planner.SubResult) (string, error) {
	f.reduceCalls++
	f.gotResults = results
	if f.reduceErr != nil {
		return "", f.reduceErr
	}
	return f.summary, nil
}

type fakeRunnerSDK struct {
	cards     []agentsdk.AgentCard
	delegated []agentsdk.DelegateTaskRequest
	tasks     []agentsdk.TaskInfo
}

func (f *fakeRunnerSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.cards, nil
}

func (f *fakeRunnerSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.delegated = append(f.delegated, req)
	return &agentsdk.DelegateTaskResponse{TaskID: "child-" + req.TargetID}, nil
}

func (f *fakeRunnerSDK) GetTask(ctx context.Context, taskID string, includeOutput bool) (*agentsdk.TaskInfo, error) {
	if !includeOutput {
		return nil, errors.New("includeOutput=false")
	}
	if len(f.tasks) == 0 {
		return nil, errors.New("no fake task queued")
	}
	info := f.tasks[0]
	f.tasks = f.tasks[1:]
	info.TaskID = taskID
	return &info, nil
}

func TestDriverRunnerExecutesPlannedNodeAndReduces(t *testing.T) {
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)}},
		tasks: []agentsdk.TaskInfo{{Status: "completed", Result: json.RawMessage(`"child output"`)}},
	}
	plannerFake := &fakeRunnerPlanner{
		nodes:   []planner.Node{{ID: "n1", TargetID: "slave-a", Skill: "chat", Prompt: "do work"}},
		summary: "final answer",
	}
	runner := NewDriverRunner(plannerFake, sdk, RunnerConfig{MaxConcurrency: 2, ChildTimeoutSec: 30})

	got, err := runner.Run(context.Background(), "original prompt")

	require.NoError(t, err)
	require.Equal(t, "final answer", got.Summary)
	require.Len(t, sdk.delegated, 1)
	require.Equal(t, "slave-a", sdk.delegated[0].TargetID)
	require.Equal(t, "chat", sdk.delegated[0].Skill)
	require.Equal(t, "do work", sdk.delegated[0].Prompt)
	require.Equal(t, 30, sdk.delegated[0].TimeoutSeconds)
	require.Len(t, plannerFake.gotPlanArgs, 1)
	require.Len(t, plannerFake.gotResults, 1)
	require.Equal(t, "child output", plannerFake.gotResults[0].Output)
}

func TestDriverRunnerRendersPromptFromPriorNodeOutput(t *testing.T) {
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		tasks: []agentsdk.TaskInfo{
			{Status: "completed", Result: json.RawMessage(`{"output":"first node output"}`)},
			{Status: "completed", Output: "second node output"},
		},
	}
	runner := NewDriverRunner(&fakeRunnerPlanner{
		nodes: []planner.Node{
			{ID: "n1", TargetID: "slave-a", Prompt: "first"},
			{ID: "n2", TargetID: "slave-a", Prompt: "combine {{n1.output}}", DependsOn: []string{"n1"}},
		},
		summary: "final answer",
	}, sdk, RunnerConfig{MaxConcurrency: 1})

	_, err := runner.Run(context.Background(), "original prompt")

	require.NoError(t, err)
	require.Len(t, sdk.delegated, 2)
	require.Contains(t, sdk.delegated[1].Prompt, "first node output")
}

func TestDriverRunnerRequiredNodeFailureReturnsError(t *testing.T) {
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		tasks: []agentsdk.TaskInfo{{Status: "failed", FailureReason: "boom"}},
	}
	runner := NewDriverRunner(&fakeRunnerPlanner{
		nodes: []planner.Node{{ID: "n1", TargetID: "slave-a", Prompt: "required"}},
	}, sdk, RunnerConfig{})

	_, err := runner.Run(context.Background(), "original prompt")

	require.ErrorContains(t, err, "required node n1 failed: boom")
}

func TestDriverRunnerOptionalNodeFailureStillReduces(t *testing.T) {
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		tasks: []agentsdk.TaskInfo{{Status: "failed", FailureReason: "best effort failed"}},
	}
	plannerFake := &fakeRunnerPlanner{
		nodes:   []planner.Node{{ID: "n1", TargetID: "slave-a", Prompt: "optional", Optional: true}},
		summary: "reduced anyway",
	}
	runner := NewDriverRunner(plannerFake, sdk, RunnerConfig{})

	got, err := runner.Run(context.Background(), "original prompt")

	require.NoError(t, err)
	require.Equal(t, "reduced anyway", got.Summary)
	require.Len(t, plannerFake.gotResults, 1)
	require.Equal(t, "failed", plannerFake.gotResults[0].Status)
	require.Equal(t, "best effort failed", plannerFake.gotResults[0].Error)
}

func TestDriverRunnerOptionalFailureSkipsRequiredDownstreamAndDoesNotReduce(t *testing.T) {
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		tasks: []agentsdk.TaskInfo{{Status: "failed", FailureReason: "best effort failed"}},
	}
	plannerFake := &fakeRunnerPlanner{
		nodes: []planner.Node{
			{ID: "optional", TargetID: "slave-a", Prompt: "optional", Optional: true},
			{ID: "required", TargetID: "slave-a", Prompt: "required", DependsOn: []string{"optional"}},
		},
	}
	runner := NewDriverRunner(plannerFake, sdk, RunnerConfig{})

	_, err := runner.Run(context.Background(), "original prompt")

	require.ErrorContains(t, err, "required node required skipped: upstream optional failed/skipped")
	require.Equal(t, 0, plannerFake.reduceCalls)
}

func TestDriverRunnerReducesResultsInPlanOrder(t *testing.T) {
	const nodeCount = 20
	nodes := make([]planner.Node, 0, nodeCount)
	tasks := make([]agentsdk.TaskInfo, 0, nodeCount)
	for i := nodeCount - 1; i >= 0; i-- {
		id := "n" + string(rune('a'+i))
		nodes = append(nodes, planner.Node{ID: id, TargetID: "slave-a", Prompt: "prompt " + id})
		tasks = append(tasks, agentsdk.TaskInfo{Status: "completed", Output: "output " + id})
	}
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		tasks: tasks,
	}
	plannerFake := &fakeRunnerPlanner{nodes: nodes, summary: "ordered"}
	runner := NewDriverRunner(plannerFake, sdk, RunnerConfig{MaxConcurrency: nodeCount})

	_, err := runner.Run(context.Background(), "original prompt")

	require.NoError(t, err)
	require.Len(t, plannerFake.gotResults, nodeCount)
	for i, result := range plannerFake.gotResults {
		require.Equal(t, nodes[i].ID, result.NodeID)
		require.Equal(t, nodes[i].TargetID, result.TargetID)
		require.Equal(t, nodes[i].Prompt, result.Prompt)
	}
}

func TestDriverRunnerFallsBackWhenReduceFails(t *testing.T) {
	sdk := &fakeRunnerSDK{
		cards: []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}},
		tasks: []agentsdk.TaskInfo{{Status: "completed", Output: "preserved output"}},
	}
	runner := NewDriverRunner(&fakeRunnerPlanner{
		nodes:     []planner.Node{{ID: "n1", TargetID: "slave-a", Prompt: "work"}},
		reduceErr: errors.New("reducer unavailable"),
	}, sdk, RunnerConfig{})

	got, err := runner.Run(context.Background(), "original prompt")

	require.NoError(t, err)
	require.True(t, strings.Contains(got.Summary, "reducer unavailable"))
	require.True(t, strings.Contains(got.Summary, "preserved output"))
}
