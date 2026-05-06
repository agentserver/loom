package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/salve_agent/internal/config"
	"github.com/yourorg/salve_agent/internal/executor"
	"github.com/yourorg/salve_agent/internal/planner"
	"github.com/yourorg/salve_agent/internal/store"
)

// SDKDelegator is the slice of agentsdk.Client we use, expressed as an interface
// so tests can supply an in-memory fake.
type SDKDelegator interface {
	DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
	DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	WaitForTask(ctx context.Context, taskID string, pollInterval time.Duration) (*agentsdk.TaskInfo, error)
}

type Orchestrator struct {
	store   *store.Store
	planner *planner.Planner
	sdk     SDKDelegator
	cfg     config.Fanout
	selfID  string
}

func New(s *store.Store, p *planner.Planner, sdk SDKDelegator, cfg config.Fanout, selfID string) *Orchestrator {
	return &Orchestrator{store: s, planner: p, sdk: sdk, cfg: cfg, selfID: selfID}
}

// Run satisfies poller.Dispatcher.
func (o *Orchestrator) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	if err := o.store.Insert(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt}); err != nil {
		return executor.Result{}, err
	}
	if err := o.store.MarkRunning(t.ID); err != nil {
		return executor.Result{}, err
	}

	var (
		res executor.Result
		err error
	)
	switch t.Skill {
	case "route":
		res, err = o.runRoute(ctx, t)
	case "fanout":
		res, err = o.runFanout(ctx, t)
	default:
		err = fmt.Errorf("unknown skill: %q", t.Skill)
	}
	if err != nil {
		_ = o.store.Fail(t.ID, err.Error())
		return executor.Result{}, err
	}
	if cerr := o.store.Complete(t.ID, res.Summary); cerr != nil {
		return res, cerr
	}
	return res, nil
}

// discoverFiltered returns DiscoverAgents minus self, only "available" agents.
func (o *Orchestrator) discoverFiltered(ctx context.Context) ([]agentsdk.AgentCard, error) {
	all, err := o.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	out := make([]agentsdk.AgentCard, 0, len(all))
	for _, a := range all {
		if a.AgentID == o.selfID {
			continue
		}
		if a.Status != "available" {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (o *Orchestrator) policyForSkill(skill string) string {
	if p, ok := o.cfg.PolicyBySkill[skill]; ok {
		return p
	}
	if o.cfg.DefaultPolicy != "" {
		return o.cfg.DefaultPolicy
	}
	return "best_effort"
}

// Stub (replaced in Task 12)
func (o *Orchestrator) runFanout(ctx context.Context, t executor.Task) (executor.Result, error) {
	return executor.Result{}, fmt.Errorf("runFanout not implemented")
}
