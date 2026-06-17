package commander

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type fakeBackend struct {
	listFn   func(ctx context.Context) ([]agentbackend.Session, error)
	getFn    func(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error)
	workerFn func(ctx context.Context, sess agentbackend.Session) (agentbackend.SessionWorker, error)
}

func (f *fakeBackend) Kind() agentbackend.Kind { return agentbackend.KindClaude }
func (f *fakeBackend) Run(_ context.Context, _ executor.Task, _ executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *fakeBackend) RunResume(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
	if f.resumeFn == nil {
		return executor.Result{}, nil
	}
	return f.resumeFn(ctx, id, answer, sink)
}
func (f *fakeBackend) LLM() agentbackend.LLMRunner                { return nil }
func (f *fakeBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (f *fakeBackend) Detect(_ context.Context) error             { return nil }
func (f *fakeBackend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx)
}
func (f *fakeBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	if f.getFn == nil {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	return f.getFn(ctx, id)
}
func (f *fakeBackend) NewSessionWorker(ctx context.Context, sess agentbackend.Session) (agentbackend.SessionWorker, error) {
	if f.workerFn == nil {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	return f.workerFn(ctx, sess)
}

type closingBackend struct {
	*fakeBackend
	closeFn func() error
}

func (b *closingBackend) Close() error {
	if b.closeFn == nil {
		return nil
	}
	return b.closeFn()
}

type resumeOnlyBackend struct {
	getFn    func(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error)
}

func (b *resumeOnlyBackend) Kind() agentbackend.Kind { return agentbackend.KindCodex }
func (b *resumeOnlyBackend) Run(context.Context, executor.Task, executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (b *resumeOnlyBackend) RunResume(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
	return b.resumeFn(ctx, id, answer, sink)
}
func (b *resumeOnlyBackend) LLM() agentbackend.LLMRunner                { return nil }
func (b *resumeOnlyBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (b *resumeOnlyBackend) Detect(context.Context) error               { return nil }
func (b *resumeOnlyBackend) ListSessions(context.Context) ([]agentbackend.Session, error) {
	return nil, nil
}
func (b *resumeOnlyBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return b.getFn(ctx, id)
}

type fakeSessionWorker struct {
	mu      sync.Mutex
	turns   []string
	closed  bool
	healthy bool
	runFn   func(ctx context.Context, prompt string, sink executor.Sink) (executor.Result, error)
}

func (w *fakeSessionWorker) Run(ctx context.Context, prompt string, sink executor.Sink) (executor.Result, error) {
	if w.runFn != nil {
		return w.runFn(ctx, prompt, sink)
	}
	w.mu.Lock()
	w.turns = append(w.turns, prompt)
	w.mu.Unlock()
	sink.Write("chunk", prompt)
	return executor.Result{Summary: "worker", SessionID: "s1"}, nil
}

func (w *fakeSessionWorker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *fakeSessionWorker) Healthy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.healthy
}

type captureSink struct {
	events           []captured
	closed           bool
	writesAfterClose int
}

type captured struct {
	kind string
	data string
}

func (c *captureSink) Write(kind, data string) {
	if c.closed {
		c.writesAfterClose++
	}
	c.events = append(c.events, captured{kind: kind, data: data})
}

func (c *captureSink) Close() { c.closed = true }

// TestHandler_ListSessionsForwards pins that Handler.ListSessions returns
// whatever the Backend returns, with no filtering or mutation.
func TestHandler_ListSessionsForwards(t *testing.T) {
	want := []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude}}
	h := &Handler{Backend: &fakeBackend{
		listFn: func(_ context.Context) ([]agentbackend.Session, error) { return want, nil },
	}}
	got, err := h.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("got=%+v", got)
	}
}

// TestHandler_GetSessionForwards pins the descriptor plus message slice flow.
func TestHandler_GetSessionForwards(t *testing.T) {
	wantS := agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude}
	wantM := []agentbackend.SessionMessage{{Role: "user", Text: "hi"}}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			if id != "s1" {
				t.Errorf("id=%q", id)
			}
			return wantS, wantM, nil
		},
	}}
	s, m, err := h.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s1" || len(m) != 1 || m[0].Text != "hi" {
		t.Fatalf("s=%+v m=%+v", s, m)
	}
}

// TestHandler_GetSessionPropagatesErrSessionNotFound pins that the PR-1
// sentinel survives so transports can emit session_not_found.
func TestHandler_GetSessionPropagatesErrSessionNotFound(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
		},
	}}
	_, _, err := h.GetSession(context.Background(), "missing")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v", err)
	}
}

// TestHandler_SessionTurnStreamsAndReturns pins that the sink receives each
// RunResume event in order and the final Result is returned to the caller.
func TestHandler_SessionTurnStreamsAndReturns(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
			if id != "s1" {
				t.Errorf("id=%q", id)
			}
			if !strings.Contains(answer, "do thing") {
				t.Errorf("answer=%q", answer)
			}
			sink.Write("chunk", "step one")
			sink.Write("chunk", "step two")
			sink.Write("capability", "added foo")
			sink.Close()
			return executor.Result{Summary: "done", SessionID: id}, nil
		},
	}}
	sink := &captureSink{}
	res, err := h.SessionTurn(context.Background(), "s1", "do thing", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "done" {
		t.Errorf("summary=%q", res.Summary)
	}
	if !sink.closed {
		t.Errorf("sink not closed")
	}
	if len(sink.events) != 3 || sink.events[0].kind != "chunk" || sink.events[2].kind != "capability" {
		t.Errorf("events=%+v", sink.events)
	}
}

func TestHandler_OffModeBackendDoesNotFetchSessionBeforeResume(t *testing.T) {
	var getCalls atomic.Int32
	var resumeCalls atomic.Int32
	h := &Handler{Backend: &resumeOnlyBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			getCalls.Add(1)
			return agentbackend.Session{}, nil, errors.New("unexpected GetSession")
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			resumeCalls.Add(1)
			return executor.Result{Summary: "fallback"}, nil
		},
	}}

	_, err := h.SessionTurn(context.Background(), "s1", "prompt", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}
	if got := getCalls.Load(); got != 0 {
		t.Fatalf("GetSession calls=%d want 0", got)
	}
	if got := resumeCalls.Load(); got != 1 {
		t.Fatalf("RunResume calls=%d want 1", got)
	}
}

// TestHandler_SessionTurnRespectsContextCancel pins that handler does not
// swallow context errors, which daemon shutdown relies on.
func TestHandler_SessionTurnRespectsContextCancel(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(ctx context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			select {
			case <-ctx.Done():
				return executor.Result{}, ctx.Err()
			case <-time.After(5 * time.Second):
				t.Errorf("backend not cancelled")
				return executor.Result{}, nil
			}
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.SessionTurn(ctx, "s1", "p", &captureSink{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestHandler_SessionTurnReusesHotWorkerAndReportsActive(t *testing.T) {
	worker := &fakeSessionWorker{healthy: true}
	var created atomic.Int32
	var fallback atomic.Int32
	h := &Handler{Backend: &fakeBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}}, nil
		},
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			created.Add(1)
			return worker, nil
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			fallback.Add(1)
			return executor.Result{Summary: "fallback"}, nil
		},
	}}
	defer h.Close()

	for _, prompt := range []string{"first", "second"} {
		if _, err := h.SessionTurn(context.Background(), "s1", prompt, &captureSink{}); err != nil {
			t.Fatal(err)
		}
	}

	if got := created.Load(); got != 1 {
		t.Fatalf("worker creates=%d want 1", got)
	}
	if got := fallback.Load(); got != 0 {
		t.Fatalf("fallback calls=%d want 0", got)
	}
	worker.mu.Lock()
	if got := append([]string(nil), worker.turns...); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("worker turns=%v", got)
	}
	worker.mu.Unlock()

	sessions, err := h.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !sessions[0].ActiveWorker {
		t.Fatalf("sessions=%+v want active worker marker", sessions)
	}
}

func TestHandler_SessionTurnEvictsLeastRecentlyUsedWorker(t *testing.T) {
	workers := make(map[string]*fakeSessionWorker)
	h := &Handler{
		Backend: &fakeBackend{
			listFn: func(context.Context) ([]agentbackend.Session, error) {
				return []agentbackend.Session{
					{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo/s1"},
					{ID: "s2", Kind: agentbackend.KindClaude, WorkingDir: "/repo/s2"},
					{ID: "s3", Kind: agentbackend.KindClaude, WorkingDir: "/repo/s3"},
				}, nil
			},
			getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
				return agentbackend.Session{ID: id, Kind: agentbackend.KindClaude, WorkingDir: "/repo/" + id}, nil, nil
			},
			workerFn: func(_ context.Context, sess agentbackend.Session) (agentbackend.SessionWorker, error) {
				w := &fakeSessionWorker{healthy: true}
				workers[sess.ID] = w
				return w, nil
			},
		},
		WorkerMax:         2,
		WorkerIdleTimeout: time.Hour,
	}
	defer h.Close()

	for _, id := range []string{"s1", "s2", "s1", "s3"} {
		if _, err := h.SessionTurn(context.Background(), id, "go", &captureSink{}); err != nil {
			t.Fatal(err)
		}
	}

	if !workers["s2"].closed {
		t.Fatalf("s2 worker was least recently used and should be closed")
	}
	if workers["s1"].closed || workers["s3"].closed {
		t.Fatalf("workers closed unexpectedly: s1=%v s3=%v", workers["s1"].closed, workers["s3"].closed)
	}
	sessions, err := h.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]bool{}
	for _, sess := range sessions {
		active[sess.ID] = sess.ActiveWorker
	}
	if !active["s1"] || active["s2"] || !active["s3"] {
		t.Fatalf("active=%v want s1/s3 active, s2 inactive", active)
	}
}

func TestHandler_SessionTurnEvictsIdleWorker(t *testing.T) {
	workers := make(map[string]*fakeSessionWorker)
	h := &Handler{
		Backend: &fakeBackend{
			listFn: func(context.Context) ([]agentbackend.Session, error) {
				return []agentbackend.Session{
					{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo/s1"},
					{ID: "s2", Kind: agentbackend.KindClaude, WorkingDir: "/repo/s2"},
				}, nil
			},
			getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
				return agentbackend.Session{ID: id, Kind: agentbackend.KindClaude, WorkingDir: "/repo/" + id}, nil, nil
			},
			workerFn: func(_ context.Context, sess agentbackend.Session) (agentbackend.SessionWorker, error) {
				w := &fakeSessionWorker{healthy: true}
				workers[sess.ID] = w
				return w, nil
			},
		},
		WorkerMax:         10,
		WorkerIdleTimeout: time.Nanosecond,
	}
	defer h.Close()

	if _, err := h.SessionTurn(context.Background(), "s1", "go", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := h.SessionTurn(context.Background(), "s2", "go", &captureSink{}); err != nil {
		t.Fatal(err)
	}

	if !workers["s1"].closed {
		t.Fatalf("idle s1 worker should be closed")
	}
}

func TestHandler_SessionTurnFallsBackWhenWorkerUnavailable(t *testing.T) {
	var fallback atomic.Int32
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return nil, agentbackend.ErrSessionWorkerUnavailable
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			fallback.Add(1)
			return executor.Result{Summary: "fallback", SessionID: "s1"}, nil
		},
	}}
	defer h.Close()

	res, err := h.SessionTurn(context.Background(), "s1", "go", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "fallback" || fallback.Load() != 1 {
		t.Fatalf("result=%+v fallback=%d", res, fallback.Load())
	}
}

func TestHandler_SessionTurnFallsBackWhenCachedWorkerUnhealthy(t *testing.T) {
	worker := &fakeSessionWorker{healthy: true}
	var fallback atomic.Int32
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return worker, nil
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			fallback.Add(1)
			return executor.Result{Summary: "fallback", SessionID: "s1"}, nil
		},
	}}
	defer h.Close()

	if _, err := h.SessionTurn(context.Background(), "s1", "warm", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	worker.mu.Lock()
	worker.healthy = false
	worker.mu.Unlock()
	res, err := h.SessionTurn(context.Background(), "s1", "again", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}

	if res.Summary != "fallback" || fallback.Load() != 1 {
		t.Fatalf("result=%+v fallback=%d", res, fallback.Load())
	}
	worker.mu.Lock()
	closed := worker.closed
	worker.mu.Unlock()
	if !closed {
		t.Fatalf("unhealthy worker should be closed")
	}
}

func TestHandler_SessionTurnFallsBackWhenHotWorkerRunUnavailable(t *testing.T) {
	worker := &fakeSessionWorker{healthy: true}
	var runCalls atomic.Int32
	worker.runFn = func(_ context.Context, prompt string, sink executor.Sink) (executor.Result, error) {
		switch runCalls.Add(1) {
		case 1:
			sink.Write("chunk", "warm")
			return executor.Result{Summary: "warm", SessionID: "s1"}, nil
		case 2:
			return executor.Result{}, agentbackend.ErrSessionWorkerUnavailable
		default:
			t.Fatalf("unexpected worker run for prompt %q", prompt)
			return executor.Result{}, nil
		}
	}
	var fallback atomic.Int32
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return worker, nil
		},
		resumeFn: func(_ context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
			fallback.Add(1)
			if id != "s1" || answer != "again" {
				t.Fatalf("RunResume id=%q answer=%q, want s1/again", id, answer)
			}
			sink.Write("chunk", "fallback")
			return executor.Result{Summary: "fallback", SessionID: "s1"}, nil
		},
	}}
	defer h.Close()

	if _, err := h.SessionTurn(context.Background(), "s1", "warm", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	res, err := h.SessionTurn(context.Background(), "s1", "again", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "fallback" || fallback.Load() != 1 {
		t.Fatalf("result=%+v fallback=%d", res, fallback.Load())
	}
	if sink.writesAfterClose != 0 {
		t.Fatalf("fallback wrote after close %d times", sink.writesAfterClose)
	}
	if len(sink.events) != 1 || sink.events[0] != (captured{kind: "chunk", data: "fallback"}) {
		t.Fatalf("sink events=%+v, want fallback chunk", sink.events)
	}
	worker.mu.Lock()
	closed := worker.closed
	worker.mu.Unlock()
	if !closed {
		t.Fatalf("unavailable worker should be removed and closed")
	}
}

func TestHandler_SessionTurnDoesNotFallbackAfterWorkerRunError(t *testing.T) {
	runErr := errors.New("worker failed after starting")
	worker := &fakeSessionWorker{healthy: true}
	worker.runFn = func(_ context.Context, prompt string, sink executor.Sink) (executor.Result, error) {
		sink.Write("chunk", "partial "+prompt)
		return executor.Result{}, runErr
	}
	var fallback atomic.Int32
	h := &Handler{Backend: &fakeBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}}, nil
		},
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return worker, nil
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			fallback.Add(1)
			return executor.Result{Summary: "fallback"}, nil
		},
	}}
	defer h.Close()

	sink := &captureSink{}
	_, err := h.SessionTurn(context.Background(), "s1", "go", sink)
	if !errors.Is(err, runErr) {
		t.Fatalf("err=%v want %v", err, runErr)
	}
	if got := fallback.Load(); got != 0 {
		t.Fatalf("fallback calls=%d want 0 after worker.Run started", got)
	}
	if len(sink.events) != 1 || sink.events[0].kind != "chunk" || sink.events[0].data != "partial go" {
		t.Fatalf("sink events=%+v want only partial worker output", sink.events)
	}
	worker.mu.Lock()
	closed := worker.closed
	worker.mu.Unlock()
	if !closed {
		t.Fatalf("failed worker should be removed and closed")
	}
	sessions, err := h.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ActiveWorker {
		t.Fatalf("sessions=%+v want failed worker inactive", sessions)
	}
}

func TestHandler_CloseWaitsForInFlightWorkerRun(t *testing.T) {
	worker := &closeObservingWorker{
		runStarted: make(chan struct{}),
		releaseRun: make(chan struct{}),
		closeDone:  make(chan struct{}),
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return worker, nil
		},
	}}

	turnDone := make(chan error, 1)
	go func() {
		_, err := h.SessionTurn(context.Background(), "s1", "go", &captureSink{})
		turnDone <- err
	}()
	select {
	case <-worker.runStarted:
	case <-time.After(time.Second):
		t.Fatal("worker run did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- h.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while worker.Run was still in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(worker.releaseRun)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after worker.Run finished")
	}
	if worker.closedDuringRun.Load() {
		t.Fatal("worker.Close ran concurrently with worker.Run")
	}
	select {
	case <-worker.closeDone:
	case <-time.After(time.Second):
		t.Fatal("worker.Close was not called after run completed")
	}
}

func TestHandler_CloseClosesBackendBeforeWaitingForActiveWorker(t *testing.T) {
	runStarted := make(chan struct{})
	releaseRun := make(chan struct{})
	backendClosed := make(chan struct{})
	var closeOnce sync.Once
	worker := &fakeSessionWorker{healthy: true}
	worker.runFn = func(context.Context, string, executor.Sink) (executor.Result, error) {
		close(runStarted)
		<-releaseRun
		return executor.Result{Summary: "done", SessionID: "s1"}, nil
	}
	h := &Handler{Backend: &closingBackend{
		fakeBackend: &fakeBackend{
			getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
				return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
			},
			workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
				return worker, nil
			},
		},
		closeFn: func() error {
			closeOnce.Do(func() {
				close(backendClosed)
				close(releaseRun)
			})
			return nil
		},
	}}

	turnDone := make(chan error, 1)
	go func() {
		_, err := h.SessionTurn(context.Background(), "s1", "go", &captureSink{})
		turnDone <- err
	}()
	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("worker run did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- h.Close() }()
	select {
	case <-backendClosed:
	case <-time.After(200 * time.Millisecond):
		closeOnce.Do(func() { close(releaseRun) })
		t.Fatal("Handler.Close waited for active worker before calling backend Close")
	}
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Handler.Close did not return after backend Close unblocked worker")
	}
}

func TestHandler_WorkerCacheLazyInitNoRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		worker := &fakeSessionWorker{healthy: true}
		h := &Handler{Backend: &fakeBackend{
			listFn: func(context.Context) ([]agentbackend.Session, error) {
				return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}}, nil
			},
			getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
				return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
			},
			workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
				return worker, nil
			},
		}}
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = h.ListSessions(context.Background())
		}()
		go func() {
			defer wg.Done()
			_, _ = h.SessionTurn(context.Background(), "s1", "go", &captureSink{})
		}()
		go func() {
			defer wg.Done()
			_ = h.Close()
		}()
		wg.Wait()
	}
}

func TestHandler_CloseBeforeFirstTurnDisablesWorkerCache(t *testing.T) {
	var fallback atomic.Int32
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			t.Fatal("worker should not be created after Handler.Close")
			return nil, agentbackend.ErrSessionWorkerUnavailable
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			fallback.Add(1)
			return executor.Result{Summary: "fallback"}, nil
		},
	}}

	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SessionTurn(context.Background(), "s1", "go", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	if fallback.Load() != 1 {
		t.Fatalf("fallback calls=%d want 1", fallback.Load())
	}
	if h.workerCache.Load() != nil {
		t.Fatal("worker cache should not be created after Handler.Close")
	}
}

func TestHandler_CloseClosesBackendWithoutWorkerCache(t *testing.T) {
	var backendClosed atomic.Int32
	h := &Handler{Backend: &closingBackend{
		fakeBackend: &fakeBackend{},
		closeFn: func() error {
			backendClosed.Add(1)
			return nil
		},
	}}

	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if got := backendClosed.Load(); got != 1 {
		t.Fatalf("backend Close calls=%d want 1", got)
	}
	if h.workerCache.Load() != nil {
		t.Fatal("worker cache should not be created by Close")
	}
}

func TestHandler_WorkerMaxNegativeDisablesWorkerCache(t *testing.T) {
	var fallback atomic.Int32
	h := &Handler{
		Backend: &fakeBackend{
			getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
				return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
			},
			workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
				t.Fatal("worker should not be created when WorkerMax is negative")
				return nil, agentbackend.ErrSessionWorkerUnavailable
			},
			resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
				fallback.Add(1)
				return executor.Result{Summary: "fallback"}, nil
			},
		},
		WorkerMax: -1,
	}

	if _, err := h.SessionTurn(context.Background(), "s1", "go", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	if fallback.Load() != 1 {
		t.Fatalf("fallback calls=%d want 1", fallback.Load())
	}
	if h.workerCache.Load() != nil {
		t.Fatal("worker cache should not be created when WorkerMax is negative")
	}
}

func TestHandler_SessionTurnSerializesSameSession(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var calls atomic.Int32
	worker := &fakeSessionWorker{healthy: true}
	worker.runFn = func(_ context.Context, prompt string, _ executor.Sink) (executor.Result, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			close(secondStarted)
		default:
			t.Errorf("unexpected call for prompt %q", prompt)
		}
		return executor.Result{Summary: "ok", SessionID: "s1"}, nil
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/repo"}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return worker, nil
		},
	}}
	defer h.Close()

	errCh := make(chan error, 2)
	go func() {
		_, err := h.SessionTurn(context.Background(), "s1", "first", &captureSink{})
		errCh <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first turn did not start")
	}
	go func() {
		_, err := h.SessionTurn(context.Background(), "s1", "second", &captureSink{})
		errCh <- err
	}()
	select {
	case <-secondStarted:
		t.Fatal("second same-session turn started before first completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

type closeObservingWorker struct {
	running         atomic.Bool
	closedDuringRun atomic.Bool
	runStarted      chan struct{}
	releaseRun      chan struct{}
	closeDone       chan struct{}
}

func (w *closeObservingWorker) Run(context.Context, string, executor.Sink) (executor.Result, error) {
	w.running.Store(true)
	close(w.runStarted)
	<-w.releaseRun
	w.running.Store(false)
	return executor.Result{Summary: "done", SessionID: "s1"}, nil
}

func (w *closeObservingWorker) Close() error {
	if w.running.Load() {
		w.closedDuringRun.Store(true)
	}
	close(w.closeDone)
	return nil
}

func (w *closeObservingWorker) Healthy() bool { return true }
