package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
)

// SDKDelegator is the slice of agentsdk.Client we use, expressed as an interface
// so tests can supply an in-memory fake.
type SDKDelegator interface {
	DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
	DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	WaitForTask(ctx context.Context, taskID string, pollInterval time.Duration) (*agentsdk.TaskInfo, error)
}

type ObserverSink interface {
	Emit(observer.Event)
}

type Orchestrator struct {
	store    *store.Store
	planner  *planner.Planner
	sdk      SDKDelegator
	cfg      config.Fanout
	selfID   string
	observer ObserverSink
}

func New(s *store.Store, p *planner.Planner, sdk SDKDelegator, cfg config.Fanout, selfID string, obs ObserverSink) *Orchestrator {
	return &Orchestrator{store: s, planner: p, sdk: sdk, cfg: cfg, selfID: selfID, observer: obs}
}

func (o *Orchestrator) emit(ev observer.Event) {
	if o.observer != nil {
		o.observer.Emit(ev)
	}
}

func observerPayload(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Run satisfies poller.Dispatcher.
func (o *Orchestrator) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	summary := observer.SummarizePrompt(t.Prompt, 80)
	if err := o.store.Insert(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt}); err != nil {
		return executor.Result{}, err
	}
	if err := o.store.MarkRunning(t.ID); err != nil {
		return executor.Result{}, err
	}
	o.emit(observer.Event{
		Type:    observer.EventMasterTaskReceived,
		TaskID:  t.ID,
		Summary: summary,
		Status:  "running",
	})

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
		o.emit(observer.Event{
			Type:    observer.EventMasterTaskFailed,
			TaskID:  t.ID,
			Summary: summary,
			Status:  "failed",
			Payload: observerPayload(map[string]string{"error": err.Error()}),
		})
		return executor.Result{}, err
	}
	if cerr := o.store.Complete(t.ID, res.Summary); cerr != nil {
		o.emit(observer.Event{
			Type:    observer.EventMasterTaskFailed,
			TaskID:  t.ID,
			Summary: summary,
			Status:  "failed",
			Payload: observerPayload(map[string]string{"error": cerr.Error()}),
		})
		return res, cerr
	}
	o.emit(observer.Event{
		Type:    observer.EventMasterTaskCompleted,
		TaskID:  t.ID,
		Summary: summary,
		Status:  "completed",
		Payload: observerPayload(map[string]string{"output": res.Summary}),
	})
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
