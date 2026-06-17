package codex

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type appServerWorkerTestSink struct {
	events   []appServerWorkerTestEvent
	statuses []appServerWorkerTestStatus
	closed   bool
}

type appServerWorkerTestEvent struct {
	kind string
	data string
}

type appServerWorkerTestStatus struct {
	code string
	text string
}

func (s *appServerWorkerTestSink) Write(kind, data string) {
	s.events = append(s.events, appServerWorkerTestEvent{kind: kind, data: data})
}

func (s *appServerWorkerTestSink) Close() {
	s.closed = true
}

func (s *appServerWorkerTestSink) WriteStatus(code, text string) {
	s.statuses = append(s.statuses, appServerWorkerTestStatus{code: code, text: text})
}

func TestCodexFactoryDefaultDoesNotExposeSessionWorkerBackend(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindCodex,
		Bin:     "codex",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(agentbackend.SessionWorkerBackend); ok {
		t.Fatalf("default codex backend implements SessionWorkerBackend; want plain backend")
	}
}

func TestCodexFactoryAppServerModeExposesSessionWorkerBackend(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(agentbackend.SessionWorkerBackend); !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
	}
}

func TestCodexWorkerBackendNewSessionWorkerUnavailableWhenManagerUnavailable(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        filepath.Join(t.TempDir(), "missing-codex"),
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerBackend, ok := b.(agentbackend.SessionWorkerBackend)
	if !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
	}

	worker, err := workerBackend.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thread-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: t.TempDir(),
	})
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("NewSessionWorker error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if worker != nil {
		t.Fatalf("NewSessionWorker returned worker %#v, want nil", worker)
	}
}

func TestCodexWorkerBackendNewSessionWorkerUnavailableWhenSessionIDEmpty(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerBackend, ok := b.(agentbackend.SessionWorkerBackend)
	if !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
	}

	worker, err := workerBackend.NewSessionWorker(context.Background(), agentbackend.Session{
		Kind:       agentbackend.KindCodex,
		WorkingDir: t.TempDir(),
	})
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("NewSessionWorker error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if worker != nil {
		t.Fatalf("NewSessionWorker returned worker %#v, want nil", worker)
	}
}

func TestCodexSessionWorkerStreamsDeltasAndCapability(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	var gotPrompt string
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, prompt string, emit func(appServerRPCMessage)) error {
			gotPrompt = prompt
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"hello"}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"other","turnId":"turn-x","itemId":"i2","delta":"ignored"}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"\n=== CAPABILITY ===\nnew cap"}`))
			emit(appServerWorkerNotification("turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`))
			return nil
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "prompt" {
		t.Fatalf("prompt = %q, want prompt", gotPrompt)
	}
	if res.Summary != "hello" || res.CapabilityChange != "new cap" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	wantEvents := []appServerWorkerTestEvent{
		{kind: "chunk", data: "hello"},
		{kind: "chunk", data: "\n=== CAPABILITY ===\nnew cap"},
		{kind: "capability", data: "new cap"},
	}
	if !equalAppServerWorkerEvents(sink.events, wantEvents) {
		t.Fatalf("events=%+v, want %+v", sink.events, wantEvents)
	}
	wantStatuses := []appServerWorkerTestStatus{
		{code: agentbackend.StatusStarting, text: "starting codex app-server"},
		{code: agentbackend.StatusAnswering, text: "codex app-server running"},
	}
	if !equalAppServerWorkerStatuses(sink.statuses, wantStatuses) {
		t.Fatalf("statuses=%+v, want %+v", sink.statuses, wantStatuses)
	}
}

func TestCodexSessionWorkerReturnsUnavailableBeforeAcceptedExecution(t *testing.T) {
	runErr := errors.New("transport closed")
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(context.Context, string, func(appServerRPCMessage)) error {
			return runErr
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if errors.Is(err, runErr) {
		t.Fatalf("Run error = %v, should not expose pre-acceptance run error", err)
	}
	if res != (agentbackend.Result{}) {
		t.Fatalf("result=%+v, want zero result", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
}

func TestCodexSessionWorkerReturnsRealErrorAfterDeltaAcceptedExecution(t *testing.T) {
	runErr := errors.New("lost app-server")
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage)) error {
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return runErr
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if !errors.Is(err, runErr) {
		t.Fatalf("Run error = %v, want %v", err, runErr)
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not allow fallback after accepted execution", err)
	}
	if res.Summary != "partial" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
}

func TestCodexSessionWorkerIgnoresOtherSessionNotificationsBeforeFallback(t *testing.T) {
	runErr := errors.New("wrong thread only")
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage)) error {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"other","turn":{"id":"turn-x","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"other","turnId":"turn-x","itemId":"i1","delta":"ignored"}`))
			emit(appServerWorkerNotification("turn/completed", `{"threadId":"other","turn":{"id":"turn-x","status":"completed"}}`))
			return runErr
		},
	}

	_, err := w.Run(context.Background(), "prompt", sink)
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("events=%+v, want none", sink.events)
	}
	wantStatuses := []appServerWorkerTestStatus{
		{code: agentbackend.StatusStarting, text: "starting codex app-server"},
	}
	if !equalAppServerWorkerStatuses(sink.statuses, wantStatuses) {
		t.Fatalf("statuses=%+v, want %+v", sink.statuses, wantStatuses)
	}
}

func TestCodexSessionWorkerErrorNotificationBeforeAcceptedExecutionReturnsUnavailable(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage)) error {
			emit(appServerWorkerNotification("error", `{"threadId":"thr-1","message":"turn rejected"}`))
			return nil
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if res != (agentbackend.Result{}) {
		t.Fatalf("result=%+v, want zero result", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	if len(sink.events) != 0 {
		t.Fatalf("events=%+v, want none", sink.events)
	}
}

func TestCodexSessionWorkerErrorNotificationAfterAcceptedExecutionReturnsRealError(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage)) error {
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			emit(appServerWorkerNotification("error", `{"threadId":"thr-1","message":"lost worker"}`))
			return nil
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if err == nil {
		t.Fatal("Run error = nil, want app-server error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not allow fallback after accepted execution", err)
	}
	if !strings.Contains(err.Error(), "lost worker") {
		t.Fatalf("Run error = %v, want lost worker detail", err)
	}
	if res.Summary != "partial" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
}

func TestCodexSessionWorkerHealthyAndClose(t *testing.T) {
	closeErr := errors.New("close failed")
	closed := false
	w := &codexSessionWorker{
		closeFn: func() error {
			closed = true
			return closeErr
		},
	}
	w.healthy.Store(true)

	if !w.Healthy() {
		t.Fatal("Healthy() = false, want true before Close")
	}
	err := w.Close()
	if !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want %v", err, closeErr)
	}
	if !closed {
		t.Fatal("closeFn was not called")
	}
	if w.Healthy() {
		t.Fatal("Healthy() = true, want false after Close")
	}
}

func appServerWorkerNotification(method string, params string) appServerRPCMessage {
	return appServerRPCMessage{Method: method, Params: json.RawMessage(params)}
}

func equalAppServerWorkerEvents(a, b []appServerWorkerTestEvent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalAppServerWorkerStatuses(a, b []appServerWorkerTestStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
