package orchestration

import (
	"context"
	"encoding/json"
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
		return RunnerResult{}, fmt.Errorf("discover agents: %w", err)
	}
	nodes, err := r.planner.Plan(ctx, prompt, agents)
	if err != nil {
		return RunnerResult{}, fmt.Errorf("planner plan: %w", err)
	}
	if err := Validate(nodes); err != nil {
		return RunnerResult{}, fmt.Errorf("invalid plan: %w", err)
	}

	sched := NewScheduler(nodes, r.cfg.MaxConcurrency)
	outputs := make(map[string]string, len(nodes))
	renderedPrompts := make(map[string]string, len(nodes))
	nodeByID := make(map[string]planner.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	for !sched.Done() {
		ready := sched.Ready()
		if len(ready) == 0 {
			return RunnerResult{}, fmt.Errorf("driver runner stalled")
		}
		for _, n := range ready {
			sched.MarkDispatched(n.ID)
			result, err := r.runNode(ctx, n, outputs)
			if err != nil {
				return RunnerResult{}, err
			}
			sched.Report(result.NodeID, result.Status, result.Output, result.Error)
			renderedPrompts[result.NodeID] = result.Prompt
			if result.Status == "completed" {
				outputs[result.NodeID] = result.Output
				continue
			}
			alreadyFinished := finishedIDs(sched.AllFinished())
			sched.MarkDownstreamSkipped(result.NodeID)
			skipped := newlySkipped(sched.AllFinished(), alreadyFinished)
			if !n.Optional {
				return RunnerResult{}, fmt.Errorf("required node %s %s: %s", result.NodeID, result.Status, result.Error)
			}
			for _, skippedNode := range skipped {
				if !nodeByID[skippedNode.NodeID].Optional {
					return RunnerResult{}, fmt.Errorf("required node %s skipped: %s", skippedNode.NodeID, skippedNode.Error)
				}
			}
		}
	}

	results := reducerResults(nodes, sched.AllFinished(), renderedPrompts)
	summary, err := r.planner.Reduce(ctx, prompt, results)
	if err != nil {
		return RunnerResult{Summary: FallbackReduceSummary(results, err)}, nil
	}
	return RunnerResult{Summary: summary}, nil
}

func (r *DriverRunner) runNode(ctx context.Context, n planner.Node, outputs map[string]string) (planner.SubResult, error) {
	rendered, err := Render(n.Prompt, outputs)
	if err != nil {
		return planner.SubResult{}, err
	}
	resp, err := r.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       n.TargetID,
		Prompt:         rendered,
		Skill:          n.Skill,
		SystemContext:  n.SystemContext,
		TimeoutSeconds: r.cfg.ChildTimeoutSec,
	})
	if err != nil {
		return planner.SubResult{}, fmt.Errorf("delegate node %s: %w", n.ID, err)
	}
	info, err := r.sdk.GetTask(ctx, resp.TaskID, true)
	if err != nil {
		return planner.SubResult{}, fmt.Errorf("get task for node %s: %w", n.ID, err)
	}
	status := ""
	errorMessage := ""
	if info != nil {
		status = info.Status
		errorMessage = info.FailureReason
	}
	output := runnerTaskOutput(info)
	if status != "completed" && errorMessage == "" {
		errorMessage = status
	}
	return planner.SubResult{
		NodeID:   n.ID,
		TargetID: n.TargetID,
		Prompt:   rendered,
		Status:   status,
		Output:   output,
		Error:    errorMessage,
	}, nil
}

func reducerResults(nodes []planner.Node, finished []FinishedNode, renderedPrompts map[string]string) []planner.SubResult {
	finishedByID := make(map[string]FinishedNode, len(finished))
	for _, f := range finished {
		finishedByID[f.NodeID] = f
	}
	results := make([]planner.SubResult, 0, len(finished))
	for _, n := range nodes {
		f, ok := finishedByID[n.ID]
		if !ok {
			continue
		}
		prompt := n.Prompt
		if rendered, ok := renderedPrompts[n.ID]; ok {
			prompt = rendered
		}
		results = append(results, planner.SubResult{
			NodeID:   n.ID,
			TargetID: n.TargetID,
			Prompt:   prompt,
			Status:   f.Status,
			Output:   f.Output,
			Error:    f.Error,
		})
	}
	return results
}

func finishedIDs(finished []FinishedNode) map[string]bool {
	ids := make(map[string]bool, len(finished))
	for _, f := range finished {
		ids[f.NodeID] = true
	}
	return ids
}

func newlySkipped(finished []FinishedNode, alreadyFinished map[string]bool) []FinishedNode {
	var out []FinishedNode
	for _, f := range finished {
		if f.Status == "skipped" && !alreadyFinished[f.NodeID] {
			out = append(out, f)
		}
	}
	return out
}

func runnerTaskOutput(info *agentsdk.TaskInfo) string {
	if info == nil {
		return ""
	}
	if info.Output != "" {
		return info.Output
	}
	if len(info.Result) == 0 {
		return ""
	}
	var obj struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(info.Result, &obj); err == nil && obj.Output != "" {
		return obj.Output
	}
	var raw string
	if err := json.Unmarshal(info.Result, &raw); err == nil {
		return raw
	}
	return ""
}
