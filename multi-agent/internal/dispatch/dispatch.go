package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/store"
)

type JournalRecorder interface {
	Record(ctx context.Context, t executor.Task, r executor.Result) error
}

type ObserverSink interface {
	Emit(observer.Event)
}

type Dispatcher struct {
	routes   map[string]executor.Executor
	journal  JournalRecorder
	store    *store.Store
	observer ObserverSink
}

func New(routes map[string]executor.Executor, j JournalRecorder, s *store.Store, obs ObserverSink) *Dispatcher {
	return &Dispatcher{routes: routes, journal: j, store: s, observer: obs}
}

func (d *Dispatcher) emit(ev observer.Event) {
	if d.observer == nil {
		return
	}
	defer func() { _ = recover() }()
	d.observer.Emit(ev)
}

func observerPayload(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	summary := observer.SummarizePrompt(t.Prompt, 80)
	if err := d.store.Insert(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt}); err != nil {
		return executor.Result{}, err
	}
	if err := d.store.MarkRunning(t.ID); err != nil {
		return executor.Result{}, err
	}
	d.emit(observer.Event{
		Type:    observer.EventSlaveTaskStarted,
		TaskID:  t.ID,
		Summary: summary,
		Status:  "running",
	})

	// Strip TASK_CONTRACT envelope before executor dispatch so chat sees only
	// the body and bash/mcp/register_mcp can json.Unmarshal it cleanly. Only
	// master orchestrator needs the decoded contract; slave executors don't.
	if _, body, ok, err := contract.DecodeEnvelope(t.Prompt); err != nil {
		_ = d.store.Fail(t.ID, err.Error())
		d.emit(observer.Event{
			Type:    observer.EventSlaveTaskFailed,
			TaskID:  t.ID,
			Summary: summary,
			Status:  "failed",
			Payload: observerPayload(map[string]string{"error": err.Error()}),
		})
		return executor.Result{}, err
	} else if ok {
		t.Prompt = body
	}

	exec, ok := d.routes[t.Skill]
	if !ok {
		exec = d.routes[""]
	}
	if exec == nil {
		err := fmt.Errorf("no executor for skill %q", t.Skill)
		_ = d.store.Fail(t.ID, err.Error())
		d.emit(observer.Event{
			Type:    observer.EventSlaveTaskFailed,
			TaskID:  t.ID,
			Summary: summary,
			Status:  "failed",
			Payload: observerPayload(map[string]string{"error": err.Error()}),
		})
		return executor.Result{}, err
	}

	runCtx := ctx
	if t.TimeoutSec > 0 {
		var tcancel context.CancelFunc
		runCtx, tcancel = context.WithTimeout(ctx, time.Duration(t.TimeoutSec)*time.Second)
		defer tcancel()
	} else {
		var tcancel context.CancelFunc
		runCtx, tcancel = context.WithTimeout(ctx, 300*time.Second) // default 5 min
		defer tcancel()
	}
	sink := d.store.ChunkSink(t.ID)
	res, err := exec.Run(runCtx, t, sink)
	if err != nil {
		_ = d.store.Fail(t.ID, err.Error())
		d.emit(observer.Event{
			Type:    observer.EventSlaveTaskFailed,
			TaskID:  t.ID,
			Summary: summary,
			Status:  "failed",
			Payload: observerPayload(map[string]string{"error": err.Error()}),
		})
		return executor.Result{}, err
	}
	// For chat / chat_resume only, wrap the result in a structured marker so
	// the driver can distinguish "final" from "awaiting_user" without parsing
	// the summary text. See spec §3.4.
	stored := res.Summary
	if t.Skill == "" || t.Skill == "chat" || t.Skill == "chat_resume" {
		var wrapper any
		if res.AwaitingUser != nil {
			wrapper = map[string]any{
				"kind":       "awaiting_user",
				"session_id": res.SessionID,
				"question":   res.AwaitingUser,
			}
		} else {
			wrapper = map[string]any{
				"kind":       "final",
				"summary":    res.Summary,
				"session_id": res.SessionID,
			}
		}
		if b, jerr := json.Marshal(wrapper); jerr == nil {
			stored = string(b)
		}
	}
	if err := d.store.Complete(t.ID, stored); err != nil {
		return res, err
	}
	d.emit(observer.Event{
		Type:    observer.EventSlaveTaskCompleted,
		TaskID:  t.ID,
		Summary: summary,
		Status:  "completed",
		Payload: observerPayload(map[string]string{"output": stored}),
	})

	if res.CapabilityChange != "" {
		if jerr := d.journal.Record(ctx, t, res); jerr != nil {
			// logged, but does not fail the task
			_ = jerr
		}
	}
	return res, nil
}
