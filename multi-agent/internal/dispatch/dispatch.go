package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// ErrDuplicateTaskRunning is returned by Run when the same task ID is
// delivered while another executor is still running it. Pollers should
// treat this as "do not PUT status; the original Run will publish its
// own terminal state when it finishes". Callers MUST check for this
// before treating a nil-Result/nil-error as a successful completion.
var ErrDuplicateTaskRunning = errors.New("dispatch: duplicate task delivery; original run still in progress")

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
	inserted, err := d.store.InsertIfAbsent(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt})
	if err != nil {
		return executor.Result{}, err
	}
	if !inserted {
		return d.replayExistingTask(t)
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
	// the body and bash/mcp/register_mcp/unregister_mcp can json.Unmarshal it cleanly. Only
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
			// Surface the wrapped marker on the result so the poller can
			// forward it as the agentserver `result` field. Only when the
			// envelope actually carries info downstream needs (session id
			// for reverse parent link, or awaiting_user question for resume)
			// — otherwise leave the wire format as the raw summary so we
			// don't break consumers that expect a string there (orchestrator
			// taskOutput, contract test, agentserver clients). #24 P2 review.
			if res.AwaitingUser != nil || res.SessionID != "" {
				res.WrappedOutput = stored
			}
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

// replayExistingTask is called when InsertIfAbsent sees a duplicate task ID.
// The caller (poller re-delivery, driver restart) must get a sensible result
// without spawning a second executor (otherwise chat skills launch a 2nd
// claude subprocess — see MEMORY jetson_outage_modes mode B beyond the
// acquireInstanceLock fix).
//
// Semantics:
//   - completed → surface stored output (chat skill output is a JSON wrapper;
//     forward as-is — the driver-side wait_task already unwraps it via
//     unwrapKindMarker).
//   - failed    → surface stored error.
//   - other (running/assigned) → another executor is still running; return
//     ErrDuplicateTaskRunning so the poller no-ops instead of PUTting an
//     empty completed status that would clobber the in-flight executor's
//     real result.
func (d *Dispatcher) replayExistingTask(t executor.Task) (executor.Result, error) {
	row, _, err := d.store.GetTaskWithChunks(t.ID)
	if err != nil {
		return executor.Result{}, fmt.Errorf("replay task %s: %w", t.ID, err)
	}
	switch row.Status {
	case "completed":
		// For chat skills row.Output is the kind-marker JSON envelope (see
		// the wrapping block in Run above). Replay MUST produce the same
		// executor.Result the normal Run path would have produced for the
		// same stored output — otherwise the poller's wire-encoding logic
		// produces different bytes on the duplicate-delivery path than on
		// the original path. WireResultFromStoredOutput encodes the rule:
		//
		//   awaiting_user OR (final && session_id != "") → forward raw via
		//     WrappedOutput; Summary is irrelevant for those because the
		//     poller picks WrappedOutput first.
		//   final with empty session_id → no WrappedOutput; Summary is the
		//     UNWRAPPED summary string (the same value Run sent as
		//     res.Summary), so the poller's raw-summary fallback wire-
		//     encodes it identically.
		//   non-envelope (non-chat skills, or anything that wasn't wrapped)
		//     → no WrappedOutput; Summary is row.Output verbatim.
		//
		// Bug history: an earlier round set Summary = row.Output for chat
		// envelopes too, which let the poller JSON-encode the envelope
		// TEXT into a string like "{\"kind\":\"final\"...}" instead of
		// sending "ok". #24 P2 review 5.
		isChatSkill := t.Skill == "" || t.Skill == "chat" || t.Skill == "chat_resume"
		if isChatSkill {
			raw, payload := agentbackend.WireResultFromStoredOutput(row.Output)
			if raw {
				return executor.Result{Summary: row.Output, WrappedOutput: row.Output}, nil
			}
			return executor.Result{Summary: payload}, nil
		}
		return executor.Result{Summary: row.Output}, nil
	case "failed":
		if row.Error == "" {
			return executor.Result{}, fmt.Errorf("task %s previously failed", t.ID)
		}
		return executor.Result{}, errors.New(row.Error)
	default: // assigned, running
		return executor.Result{}, ErrDuplicateTaskRunning
	}
}
