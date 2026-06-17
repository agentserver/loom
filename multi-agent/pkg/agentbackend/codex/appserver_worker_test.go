package codex

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type appServerWorkerTestSink struct {
	events           []appServerWorkerTestEvent
	statuses         []appServerWorkerTestStatus
	closed           bool
	writesAfterClose int
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
	if s.closed {
		s.writesAfterClose++
	}
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
		runTurn: func(_ context.Context, prompt string, emit func(appServerRPCMessage), _ func()) error {
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
		runTurn: func(context.Context, string, func(appServerRPCMessage), func()) error {
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
	if sink.closed {
		t.Fatal("sink closed before fallback")
	}
	sink.Write("chunk", "fallback can still write")
	if sink.writesAfterClose != 0 {
		t.Fatalf("writesAfterClose=%d, want 0", sink.writesAfterClose)
	}
}

func TestCodexSessionWorkerReturnsNonSentinelErrorAfterAcceptedUnavailable(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) error {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return agentbackend.ErrSessionWorkerUnavailable
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if err == nil {
		t.Fatal("Run error = nil, want non-sentinel unavailable detail")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not match ErrSessionWorkerUnavailable after accepted execution", err)
	}
	if !strings.Contains(err.Error(), agentbackend.ErrSessionWorkerUnavailable.Error()) {
		t.Fatalf("Run error = %v, want unavailable detail preserved", err)
	}
	if res.Summary != "partial" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
}

func TestCodexSessionWorkerSubmittedUnavailableDoesNotFallbackThroughCommander(t *testing.T) {
	worker := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, _ func(appServerRPCMessage), markSubmitted func()) error {
			markSubmitted()
			return agentbackend.ErrSessionWorkerUnavailable
		},
	}
	worker.healthy.Store(true)
	var fallbackCalls int
	backend := &appServerWorkerCommanderBackend{
		worker: worker,
		resumeFn: func(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error) {
			fallbackCalls++
			return agentbackend.Result{Summary: "fallback"}, nil
		},
	}
	h := &commander.Handler{Backend: backend}
	defer h.Close()

	sink := &appServerWorkerTestSink{}
	res, err := h.SessionTurn(context.Background(), "thr-1", "prompt", sink)
	if err == nil {
		t.Fatal("SessionTurn error = nil, want submitted worker error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("SessionTurn error = %v, should not match ErrSessionWorkerUnavailable after submission", err)
	}
	if !strings.Contains(err.Error(), agentbackend.ErrSessionWorkerUnavailable.Error()) {
		t.Fatalf("SessionTurn error = %v, want unavailable detail preserved", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("RunResume calls=%d, want 0", fallbackCalls)
	}
	if res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want session ID from worker", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed after submitted non-fallback error")
	}
}

func TestCodexSessionWorkerUnsubmittedUnavailableFallsBackThroughCommander(t *testing.T) {
	worker := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(context.Context, string, func(appServerRPCMessage), func()) error {
			return agentbackend.ErrSessionWorkerUnavailable
		},
	}
	worker.healthy.Store(true)
	var fallbackCalls int
	backend := &appServerWorkerCommanderBackend{
		worker: worker,
		resumeFn: func(_ context.Context, id, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
			fallbackCalls++
			if id != "thr-1" || answer != "prompt" {
				t.Fatalf("RunResume id=%q answer=%q, want thr-1/prompt", id, answer)
			}
			sink.Write("chunk", "fallback")
			sink.Close()
			return agentbackend.Result{Summary: "fallback", SessionID: id}, nil
		},
	}
	h := &commander.Handler{Backend: backend}
	defer h.Close()

	sink := &appServerWorkerTestSink{}
	res, err := h.SessionTurn(context.Background(), "thr-1", "prompt", sink)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("RunResume calls=%d, want 1", fallbackCalls)
	}
	if res.Summary != "fallback" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want fallback result", res)
	}
	if sink.writesAfterClose != 0 {
		t.Fatalf("fallback wrote after close %d times", sink.writesAfterClose)
	}
	if !equalAppServerWorkerEvents(sink.events, []appServerWorkerTestEvent{{kind: "chunk", data: "fallback"}}) {
		t.Fatalf("events=%+v, want fallback chunk", sink.events)
	}
}

func TestCodexSessionWorkerReturnsRealErrorAfterDeltaAcceptedExecution(t *testing.T) {
	runErr := errors.New("lost app-server")
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) error {
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) error {
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) error {
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
	if sink.closed {
		t.Fatal("sink closed before fallback")
	}
	if len(sink.events) != 0 {
		t.Fatalf("events=%+v, want none", sink.events)
	}
	sink.Write("chunk", "fallback can still write")
	if sink.writesAfterClose != 0 {
		t.Fatalf("writesAfterClose=%d, want 0", sink.writesAfterClose)
	}
}

func TestCodexSessionWorkerErrorNotificationAfterAcceptedExecutionReturnsRealError(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) error {
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

type appServerWorkerCommanderBackend struct {
	worker   agentbackend.SessionWorker
	resumeFn func(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error)
}

func (b *appServerWorkerCommanderBackend) Kind() agentbackend.Kind {
	return agentbackend.KindCodex
}

func (b *appServerWorkerCommanderBackend) Run(context.Context, agentbackend.Task, agentbackend.Sink) (agentbackend.Result, error) {
	return agentbackend.Result{}, nil
}

func (b *appServerWorkerCommanderBackend) RunResume(ctx context.Context, id, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	if b.resumeFn == nil {
		return agentbackend.Result{}, nil
	}
	return b.resumeFn(ctx, id, answer, sink)
}

func (b *appServerWorkerCommanderBackend) LLM() agentbackend.LLMRunner {
	return nil
}

func (b *appServerWorkerCommanderBackend) Permissions() agentbackend.PermissionsStore {
	return nil
}

func (b *appServerWorkerCommanderBackend) Detect(context.Context) error {
	return nil
}

func (b *appServerWorkerCommanderBackend) ListSessions(context.Context) ([]agentbackend.Session, error) {
	return nil, nil
}

func (b *appServerWorkerCommanderBackend) GetSession(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return agentbackend.Session{ID: "thr-1", Kind: agentbackend.KindCodex, WorkingDir: "/repo"}, nil, nil
}

func (b *appServerWorkerCommanderBackend) NewSessionWorker(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
	return b.worker, nil
}
