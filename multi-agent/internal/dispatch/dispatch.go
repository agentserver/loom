package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	if err := d.store.Complete(t.ID, res.Summary); err != nil {
		return res, err
	}
	d.emit(observer.Event{
		Type:    observer.EventSlaveTaskCompleted,
		TaskID:  t.ID,
		Summary: summary,
		Status:  "completed",
		Payload: observerPayload(map[string]string{"output": res.Summary}),
	})

	if res.CapabilityChange != "" {
		if jerr := d.journal.Record(ctx, t, res); jerr != nil {
			// logged, but does not fail the task
			_ = jerr
		}
	}
	return res, nil
}
