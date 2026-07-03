package orchestrator

// WT-2-overhead-probes: driver planning latency span (§3.3.A of
// wt2-overhead-probes.spec.md). MeasurePlanning wraps orchestrator.Plan's
// call site in fanout.go without changing any existing signature; the
// span timestamps are stamped inside observerstore's StartSpan/End, so
// the caller-timestamp ban (spec §6 (b)) is enforced structurally.
//
// Wiring: SetPlanningProbeWriter installs a real writer at slave-agent
// boot (follow-up cmd wiring; see spec §3.2 boundary discussion). Until
// then this file writes into a noop and adds essentially zero cost.

import (
	"context"
	"sync/atomic"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// planningWriterBox is the fixed concrete type held by the atomic.Value
// so swapping noop → real → noop doesn't trip the "same concrete type"
// invariant (WT-1-routing-trace §2.2 template).
type planningWriterBox struct {
	w observerstore.ProbeEventWriter
}

var activePlanningWriter atomic.Value // holds planningWriterBox

// planningNoop is the default: it inherits observerstore's noop semantics
// (StartSpan validates but produces no row); we prefer using observerstore's
// noop directly so validation errors surface even without wiring.
//
// The initial Store binds observerstore.CurrentProbeWriter() at init time —
// which is itself the noop unless the observerstore package was already
// initialised with a real writer, in which case we inherit that writer.
func init() {
	activePlanningWriter.Store(planningWriterBox{w: observerstore.CurrentProbeWriter()})
}

// SetPlanningProbeWriter installs w as the writer MeasurePlanning uses.
// Passing nil reverts to observerstore's current writer (which is the noop
// unless the observerstore-level SetProbeWriter was called).
func SetPlanningProbeWriter(w observerstore.ProbeEventWriter) {
	if w == nil {
		w = observerstore.CurrentProbeWriter()
	}
	activePlanningWriter.Store(planningWriterBox{w: w})
}

// IsPlanningProbeWriterNoop reports whether the currently installed
// planning writer is the noop. Cmd-side wiring uses this in main to
// log.Fatal if it forgot to install a real writer.
func IsPlanningProbeWriterNoop() bool {
	return activePlanningWriter.Load().(planningWriterBox).w.IsNoop()
}

func currentPlanningWriter() observerstore.ProbeEventWriter {
	return activePlanningWriter.Load().(planningWriterBox).w
}

// MeasurePlanning wraps fn in a probe_events span of kind
// KindDriverPlanning. The signature is deliberately (ctx, convID, fn) —
// three parameters, none of them time.Time — so the reflect-based drift
// test in observerstore §6 (b) passes.
//
// Behaviour:
//   - Opens the span BEFORE fn runs; defers End so panic-recovery still
//     writes the span.
//   - When the writer is noop or convID fails validation, the span is
//     dropped (with the observerstore expvar counter bumped for invalid
//     IDs), and fn still runs. fn's return values are always propagated.
//   - No allocation on the noop hot path (StartSpan returns nil handle
//     when the writer rejects; End on nil handle is a no-op).
func MeasurePlanning[T any](ctx context.Context, convID string, fn func() (T, error)) (T, error) {
	w := currentPlanningWriter()
	h, _ := w.StartSpan(observerstore.KindDriverPlanning, convID)
	// h may be nil if the writer rejected validation; End tolerates nil.
	defer func() {
		if h != nil {
			_ = h.End(ctx)
		}
	}()
	return fn()
}
