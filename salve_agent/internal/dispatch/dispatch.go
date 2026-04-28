package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/yourorg/salve_agent/internal/executor"
	"github.com/yourorg/salve_agent/internal/store"
)

type JournalRecorder interface {
	Record(ctx context.Context, t executor.Task, r executor.Result) error
}

type Dispatcher struct {
	routes  map[string]executor.Executor
	journal JournalRecorder
	store   *store.Store
}

func New(routes map[string]executor.Executor, j JournalRecorder, s *store.Store) *Dispatcher {
	return &Dispatcher{routes: routes, journal: j, store: s}
}

func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	if err := d.store.Insert(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt}); err != nil {
		return executor.Result{}, err
	}
	if err := d.store.MarkRunning(t.ID); err != nil {
		return executor.Result{}, err
	}

	exec, ok := d.routes[t.Skill]
	if !ok {
		exec = d.routes[""]
	}
	if exec == nil {
		err := fmt.Errorf("no executor for skill %q", t.Skill)
		_ = d.store.Fail(t.ID, err.Error())
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
		return executor.Result{}, err
	}
	if err := d.store.Complete(t.ID, res.Summary); err != nil {
		return res, err
	}

	if res.CapabilityChange != "" {
		if jerr := d.journal.Record(ctx, t, res); jerr != nil {
			// logged, but does not fail the task
			_ = jerr
		}
	}
	return res, nil
}
