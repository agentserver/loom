package executor

import (
	"context"
	"database/sql"
	"errors"
	"expvar"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

type fakeExec struct {
	called bool
	result Result
	err    error
}

func (f *fakeExec) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	f.called = true
	return f.result, f.err
}

type probeNopSinkT struct{}

func (probeNopSinkT) Write(string, string) {}
func (probeNopSinkT) Close()               {}

func openTestObserverExec(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "obs.db")
	st, err := observerstore.OpenSQLite(p)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

// Test #18 — happy path: result verbatim, one span row.
func TestMeasureDispatch_WrapsInnerExecutor(t *testing.T) {
	db := openTestObserverExec(t)
	SetDispatchProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetDispatchProbeWriter(nil) })

	inner := &fakeExec{result: Result{Summary: "ok"}}
	wrapped := MeasureDispatch(inner, func(t Task) string { return "conv-abcdefgh" })

	res, err := wrapped.Run(context.Background(), Task{ID: "t-1"}, probeNopSinkT{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary != "ok" {
		t.Errorf("result not propagated: %+v", res)
	}
	if !inner.called {
		t.Errorf("inner.Run was not called")
	}
	var kind string
	if err := db.QueryRow(`SELECT probe_kind FROM probe_events`).Scan(&kind); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if kind != string(observerstore.KindTaskDispatch) {
		t.Errorf("kind mismatch: %q", kind)
	}
}

// Test #19 — inner returns error: wrapper still writes span, propagates
// error verbatim.
func TestMeasureDispatch_InnerReturnsError_StillEmitsSpan(t *testing.T) {
	db := openTestObserverExec(t)
	SetDispatchProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetDispatchProbeWriter(nil) })

	innerErr := errors.New("boom")
	inner := &fakeExec{err: innerErr}
	wrapped := MeasureDispatch(inner, func(t Task) string { return "conv-abcdefgh" })

	_, err := wrapped.Run(context.Background(), Task{ID: "t-1"}, probeNopSinkT{})
	if !errors.Is(err, innerErr) {
		t.Fatalf("err not propagated: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 span row after error, got %d", n)
	}
}

// Test #20 — nil convIDFn defaults to Task.ID.
func TestMeasureDispatch_NilConvIDFn_UsesTaskID(t *testing.T) {
	db := openTestObserverExec(t)
	SetDispatchProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetDispatchProbeWriter(nil) })

	inner := &fakeExec{}
	wrapped := MeasureDispatch(inner, nil)

	if _, err := wrapped.Run(context.Background(), Task{ID: "conv-task9876"}, probeNopSinkT{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var conv string
	if err := db.QueryRow(`SELECT conversation_id FROM probe_events`).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	if conv != "conv-task9876" {
		t.Errorf("convID mismatch: %q", conv)
	}
}

// Test #21 — convIDFn returns invalid: span skipped, inner still runs,
// wrapper does NOT propagate a probe-side error.
func TestMeasureDispatch_ConvIDFnReturnsInvalid_SpanSkippedNoError(t *testing.T) {
	db := openTestObserverExec(t)
	SetDispatchProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetDispatchProbeWriter(nil) })

	before := probeIDRejectedTotalValue()
	inner := &fakeExec{}
	wrapped := MeasureDispatch(inner, func(t Task) string { return "bad id" })

	_, err := wrapped.Run(context.Background(), Task{ID: "t-1"}, probeNopSinkT{})
	if err != nil {
		t.Fatalf("wrapper propagated a probe error: %v", err)
	}
	if !inner.called {
		t.Errorf("inner should still run when convID is invalid")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no row should be written, got %d", n)
	}
	if got := probeIDRejectedTotalValue(); got <= before {
		t.Errorf("probe_id_rejected_total not bumped (before=%d got=%d)", before, got)
	}
}

// probeIDRejectedTotalValue reads the expvar counter set by
// observerstore.StartSpan on regex rejection. Reads via expvar's
// well-known name — no need to import observerstore internals.
func probeIDRejectedTotalValue() int64 {
	v := expvar.Get("probe_id_rejected_total")
	if v == nil {
		return 0
	}
	n, err := strconv.ParseInt(v.String(), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Test #22 — structural: MeasureDispatch signature has no time.Time.
func TestMeasureDispatch_SignatureNoTimeArg(t *testing.T) {
	tt := reflect.TypeOf(time.Time{})
	fnT := reflect.TypeOf(MeasureDispatch)
	for i := 0; i < fnT.NumIn(); i++ {
		if fnT.In(i) == tt {
			t.Errorf("MeasureDispatch param #%d is time.Time", i)
		}
	}
	// Also check Executor.Run on the wrapped instance.
	wrapped := MeasureDispatch(&fakeExec{}, nil)
	runM, ok := reflect.TypeOf(wrapped).MethodByName("Run")
	if !ok {
		return
	}
	mt := runM.Func.Type()
	for i := 0; i < mt.NumIn(); i++ {
		if mt.In(i) == tt {
			t.Errorf("wrapped.Run param #%d is time.Time", i)
		}
	}
}
