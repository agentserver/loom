package executor

// WT-2-overhead-probes: agentserver task-dispatch latency span
// (§3.3.B of wt2-overhead-probes.spec.md). MeasureDispatch wraps an
// Executor and stamps a probe_events span around every Run call. The
// wrapper does NOT change the Executor interface (Run signature is
// untouched) — cmd-side wiring simply substitutes
// `exec = MeasureDispatch(exec, tIDFn)` at slave-agent boot.
//
// All timestamps are stamped inside observerstore's StartSpan/End so
// this package does not touch time.Now() directly; the caller-timestamp
// ban (spec §6 (b)) is enforced structurally by the writer.

import (
	"context"
	"sync/atomic"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

type dispatchWriterBox struct {
	w observerstore.ProbeEventWriter
}

var activeDispatchWriter atomic.Value // holds dispatchWriterBox

func init() {
	activeDispatchWriter.Store(dispatchWriterBox{w: observerstore.CurrentProbeWriter()})
}

// SetDispatchProbeWriter installs w as the writer MeasureDispatch uses.
// Passing nil reverts to observerstore's current writer.
func SetDispatchProbeWriter(w observerstore.ProbeEventWriter) {
	if w == nil {
		w = observerstore.CurrentProbeWriter()
	}
	activeDispatchWriter.Store(dispatchWriterBox{w: w})
}

// IsDispatchProbeWriterNoop mirrors observerstore.IsNoopProbeWriter for
// the dispatch injection point — cmd-side wiring calls it to log.Fatal
// if it forgot to install a real writer.
func IsDispatchProbeWriterNoop() bool {
	return activeDispatchWriter.Load().(dispatchWriterBox).w.IsNoop()
}

func currentDispatchWriter() observerstore.ProbeEventWriter {
	return activeDispatchWriter.Load().(dispatchWriterBox).w
}

// ConvIDFunc extracts a conversation_id from a Task at dispatch time.
// It returns the empty string when the task carries no meaningful id;
// the caller (MeasureDispatch) then falls back to Task.ID. If the
// resulting id fails the observerstore regex, the span is dropped and
// the wrapped executor still runs.
type ConvIDFunc func(Task) string

// measuredExecutor wraps an inner Executor. Its Run enters a span before
// delegating and defers End so error / panic paths still emit a row.
type measuredExecutor struct {
	inner     Executor
	convIDFor ConvIDFunc
}

// MeasureDispatch wraps inner in a probe_events span of kind
// KindTaskDispatch. convIDFn is called once per Run; if nil, the default
// is Task.ID.
//
// Signature: (Executor, ConvIDFunc) → Executor — no time.Time anywhere,
// per §6 (b).
func MeasureDispatch(inner Executor, convIDFn ConvIDFunc) Executor {
	if inner == nil {
		return nil
	}
	if convIDFn == nil {
		convIDFn = func(t Task) string { return t.ID }
	}
	return &measuredExecutor{inner: inner, convIDFor: convIDFn}
}

func (m *measuredExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	w := currentDispatchWriter()
	convID := m.convIDFor(t)
	h, _ := w.StartSpan(observerstore.KindTaskDispatch, convID)
	// h == nil when the writer rejected (invalid convID / unknown kind);
	// End tolerates nil so this is safe.
	defer func() {
		if h != nil {
			_ = h.End(ctx)
		}
	}()
	return m.inner.Run(ctx, t, sink)
}
