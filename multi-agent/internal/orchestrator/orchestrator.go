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

type ArtifactResolver interface {
	GetArtifact(ctx context.Context, rawURL string) ([]byte, string, error)
	PutWrite(ctx context.Context, rawURL string, content []byte, mime string) error
}

type ArtifactURLAuthorizer interface {
	AuthorizeArtifactURL(rawURL string) (string, bool)
}

type Orchestrator struct {
	store     *store.Store
	planner   *planner.Planner
	sdk       SDKDelegator
	cfg       config.Fanout
	selfID    string
	observer  ObserverSink
	artifacts ArtifactResolver
}

func New(s *store.Store, p *planner.Planner, sdk SDKDelegator, cfg config.Fanout, selfID string, obs ObserverSink) *Orchestrator {
	return &Orchestrator{store: s, planner: p, sdk: sdk, cfg: cfg, selfID: selfID, observer: obs}
}

func (o *Orchestrator) SetArtifactResolver(r ArtifactResolver) *Orchestrator {
	o.artifacts = r
	return o
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
	inserted, err := o.store.InsertIfAbsent(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt})
	if err != nil {
		return executor.Result{}, err
	}
	if !inserted {
		// Parent task already exists (driver restart, poller redelivery).
		// Replay terminal state or resume from sub_tasks rows.
		return o.resumeOrReplay(ctx, t, summary)
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

// resumeOrReplay is called when InsertIfAbsent sees the parent task ID
// already exists (driver restart, poller redelivery). Returns the stored
// result for terminal tasks and continues a fanout DAG from sub_tasks rows
// for in-flight ones.
func (o *Orchestrator) resumeOrReplay(ctx context.Context, t executor.Task, summary string) (executor.Result, error) {
	row, _, err := o.store.GetTaskWithChunks(t.ID)
	if err != nil {
		return executor.Result{}, fmt.Errorf("resume %s: %w", t.ID, err)
	}
	switch row.Status {
	case "completed":
		return executor.Result{Summary: row.Output}, nil
	case "failed":
		if row.Error == "" {
			return executor.Result{}, fmt.Errorf("task %s previously failed", t.ID)
		}
		return executor.Result{}, fmt.Errorf("%s", row.Error)
	}
	// running / assigned: try to resume from sub_tasks
	rows, lerr := o.store.ListSubTasks(t.ID)
	if lerr != nil {
		return executor.Result{}, fmt.Errorf("list sub_tasks %s: %w", t.ID, lerr)
	}
	if len(rows) == 0 {
		// No DAG state yet — fall through to a fresh plan on the same task id
		// (the first attempt died before InsertSubTasks). We need MarkRunning
		// to be a no-op since the row is already in running/assigned state.
		o.emit(observer.Event{
			Type:    observer.EventMasterTaskResumed,
			TaskID:  t.ID,
			Summary: summary,
			Status:  "running",
			Payload: observerPayload(map[string]int{}),
		})
		switch t.Skill {
		case "fanout":
			res, runErr := o.runFanout(ctx, t)
			return o.finalizeRun(t, summary, res, runErr)
		case "route":
			res, runErr := o.runRoute(ctx, t)
			return o.finalizeRun(t, summary, res, runErr)
		default:
			return executor.Result{}, fmt.Errorf("resume %s: unknown skill %q", t.ID, t.Skill)
		}
	}
	if t.Skill != "fanout" {
		return executor.Result{}, fmt.Errorf("resume %s: only fanout supported (skill=%s)", t.ID, t.Skill)
	}
	o.emit(observer.Event{
		Type:    observer.EventMasterTaskResumed,
		TaskID:  t.ID,
		Summary: summary,
		Status:  "running",
		Payload: observerPayload(resumeStats(rows)),
	})
	res, runErr := o.runFanoutResume(ctx, t, rows)
	return o.finalizeRun(t, summary, res, runErr)
}

// finalizeRun persists the terminal state and emits the corresponding
// observer event. It mirrors the tail of Run() but is reused by the
// resume path which can't share Run()'s suffix (already past
// MarkRunning + the EventMasterTaskReceived emit).
func (o *Orchestrator) finalizeRun(t executor.Task, summary string, res executor.Result, err error) (executor.Result, error) {
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

func resumeStats(rows []store.SubTaskRow) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Status]++
	}
	return out
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
