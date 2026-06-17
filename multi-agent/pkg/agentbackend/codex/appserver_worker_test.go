package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/humanloop"
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

func TestAppServerManagerSendsInitializeResumeAndTurnWithContext(t *testing.T) {
	cfg := agentbackend.Config{
		Kind:    agentbackend.KindCodex,
		Bin:     "codex",
		WorkDir: "/manager-cwd",
	}
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
		writeFakeAppServerNotification(t, w, "turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`)
		writeFakeAppServerNotification(t, w, "item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"ok"}`)
		writeFakeAppServerNotification(t, w, "turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
		return true
	})
	m := newAppServerManager(cfg, []string{"LOOM_TEST_ENV=present"})
	m.starter = fake.starter
	backend := New(cfg, []string{"LOOM_TEST_ENV=present"})
	backend.exec.binSelf = "/path/to/driver-agent"
	wb := &workerBackend{Backend: backend, manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/session-cwd",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	sink := &appServerWorkerTestSink{}
	res, err := worker.Run(context.Background(), "continue", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "ok" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want summary ok and session thr-1", res)
	}

	reqs := fake.takeRequests(t, 4)
	wantMethods := []string{"initialize", "initialized", "thread/resume", "turn/start"}
	for i, want := range wantMethods {
		if reqs[i].Method != want {
			t.Fatalf("request[%d].method=%q, want %q", i, reqs[i].Method, want)
		}
	}
	assertInitializeRequest(t, reqs[0])
	assertInitializedNotification(t, reqs[1])
	assertThreadResumeRequest(t, reqs[2], "thr-1", "/session-cwd", "/path/to/driver-agent", "5")
	assertTurnStartRequest(t, reqs[3], "thr-1", "/session-cwd", "User answered: continue")
}

func TestAppServerManagerLearnsTurnIDFromDelta(t *testing.T) {
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerResult(t, w, *req.ID, map[string]any{"turn": map[string]any{"status": "running"}})
		writeFakeAppServerNotification(t, w, "item/agentMessage/delta", `{"threadId":"other","turnId":"turn-x","itemId":"other","delta":"ignored"}`)
		writeFakeAppServerNotification(t, w, "item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"ok"}`)
		writeFakeAppServerNotification(t, w, "item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-2","itemId":"stale","delta":"stale"}`)
		writeFakeAppServerNotification(t, w, "turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
		return true
	})
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), appServerRPCTestTimeout)
	defer cancel()
	sink := &appServerWorkerTestSink{}
	res, err := worker.Run(ctx, "continue", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "ok" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want summary ok and session thr-1", res)
	}
	if got := sink.events; len(got) != 1 || got[0] != (appServerWorkerTestEvent{kind: "chunk", data: "ok"}) {
		t.Fatalf("events=%+v, want only ok chunk", got)
	}
}

func TestAppServerManagerUsesManagerOwnedLifecycleContext(t *testing.T) {
	fake := newFakeAppServerTransport(t, nil)
	m := newAppServerManager(agentbackend.Config{Bin: "codex", WorkDir: "/repo"}, nil)
	var starterCtx context.Context
	var starterCalls atomic.Int32
	m.starter = func(ctx context.Context, cfg agentbackend.Config, env []string) (*appServerConnection, error) {
		starterCalls.Add(1)
		starterCtx = ctx
		return fake.starter(ctx, cfg, env)
	}

	startupCtx, cancelStartup := context.WithCancel(context.Background())
	if err := m.ensure(startupCtx); err != nil {
		t.Fatal(err)
	}
	generation := m.generation
	cancelStartup()

	if err := starterCtx.Err(); err != nil {
		t.Fatalf("starter lifecycle context error after startup context cancel = %v, want nil", err)
	}
	if !m.healthy(generation) {
		t.Fatal("manager unhealthy after startup context cancellation")
	}

	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-starterCtx.Done():
	case <-time.After(appServerRPCTestTimeout):
		t.Fatal("manager close did not cancel app-server lifecycle context")
	}
	if m.healthy(generation) {
		t.Fatal("manager healthy after close")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if got := fake.closeCount.Load(); got != 1 {
		t.Fatalf("transport close count = %d, want idempotent close count 1", got)
	}

	if err := m.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := starterCalls.Load(); got != 2 {
		t.Fatalf("starter calls = %d, want restart after close", got)
	}
	_ = m.close()
}

func TestAppServerManagerCloseUnblocksInitializeStartup(t *testing.T) {
	initSeen := make(chan struct{})
	var closeTransport func() error
	m := newAppServerManager(agentbackend.Config{Bin: "codex", WorkDir: "/repo"}, nil)
	m.starter = func(context.Context, agentbackend.Config, []string) (*appServerConnection, error) {
		clientReader, serverWriter := io.Pipe()
		serverReader, clientWriter := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			sc := bufio.NewScanner(serverReader)
			for sc.Scan() {
				var req appServerRPCMessage
				if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
					return
				}
				if req.Method == "initialize" {
					close(initSeen)
					continue
				}
				if req.ID != nil {
					writeFakeAppServerResult(nil, serverWriter, *req.ID, fakeAppServerResultFor(req.Method))
				}
			}
		}()
		closeFn := func() error {
			_ = clientReader.Close()
			_ = clientWriter.Close()
			_ = serverReader.Close()
			_ = serverWriter.Close()
			select {
			case <-done:
			case <-time.After(appServerRPCTestTimeout):
			}
			return nil
		}
		closeTransport = closeFn
		return &appServerConnection{
			rpc:   newAppServerRPC(clientReader, clientWriter),
			close: closeFn,
		}, nil
	}

	ensureDone := make(chan error, 1)
	go func() { ensureDone <- m.ensure(context.Background()) }()
	receiveWithin(t, initSeen, "initialize request")

	closeDone := make(chan error, 1)
	go func() { closeDone <- m.close() }()
	select {
	case err := <-ensureDone:
		if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
			t.Fatalf("ensure error = %v, want ErrSessionWorkerUnavailable", err)
		}
	case <-time.After(500 * time.Millisecond):
		if closeTransport != nil {
			_ = closeTransport()
		}
		t.Fatal("manager close did not unblock ensure stuck in initialize")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		if closeTransport != nil {
			_ = closeTransport()
		}
		t.Fatal("manager close did not return after unblocking startup")
	}
}

func TestAppServerManagerCloseInvalidatesWorkerHealth(t *testing.T) {
	fake := newFakeAppServerTransport(t, nil)
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	healthyWorker, ok := worker.(agentbackend.HealthySessionWorker)
	if !ok {
		t.Fatalf("worker does not implement HealthySessionWorker")
	}
	if !healthyWorker.Healthy() {
		t.Fatal("worker unhealthy before manager close")
	}

	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if healthyWorker.Healthy() {
		t.Fatal("worker healthy after manager close")
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerProcessArgsIncludeExtraArgs(t *testing.T) {
	cfg := agentbackend.Config{
		ExtraArgs: []string{"--profile", "loom-test", "-c", "model=gpt-5-codex"},
	}
	got := appServerProcessArgs(cfg)
	want := []string{"app-server", "--listen", "stdio://", "--profile", "loom-test", "-c", "model=gpt-5-codex"}
	if !equalStringSlices(got, want) {
		t.Fatalf("args=%#v, want %#v", got, want)
	}
}

func TestAppServerManagerEnsureUnavailableAndClosesTransportWhenInitializeFails(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method == "initialize" && req.ID != nil {
			writeFakeAppServerError(t, w, *req.ID, "init rejected")
			return true
		}
		return false
	})
	m := newAppServerManager(agentbackend.Config{Bin: "codex", WorkDir: "/repo"}, nil)
	m.starter = fake.starter

	err := m.ensure(context.Background())
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("ensure error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if got := fake.closeCount.Load(); got == 0 {
		t.Fatal("transport close count = 0, want cleanup on initialize failure")
	}
}

func TestCodexWorkerBackendNewSessionWorkerUnavailableWhenResumeRejectsConfig(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method == "thread/resume" && req.ID != nil {
			writeFakeAppServerError(t, w, *req.ID, "bad mcp config")
			return true
		}
		return false
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("NewSessionWorker error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if worker != nil {
		t.Fatalf("worker=%#v, want nil", worker)
	}
	reqs := fake.takeRequests(t, 3)
	for i, want := range []string{"initialize", "initialized", "thread/resume"} {
		if reqs[i].Method != want {
			t.Fatalf("request[%d].method=%q, want %q", i, reqs[i].Method, want)
		}
	}
	select {
	case req := <-fake.requests:
		t.Fatalf("unexpected request after rejected resume: %s", req.Method)
	default:
	}
}

func TestAppServerWorkerTurnStartSubmissionPreventsFallbackOnLostResponse(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method == "turn/start" {
			if closer, ok := w.(io.Closer); ok {
				_ = closer.Close()
			}
			return true
		}
		return false
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	_, err = worker.Run(context.Background(), "continue", &appServerWorkerTestSink{})
	if err == nil {
		t.Fatal("Run error = nil, want lost response error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not allow fallback after turn/start may have been written", err)
	}
}

func TestAppServerWorkerTurnStartMethodNotFoundReturnsUnavailableForFallback(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerErrorCode(t, w, *req.ID, -32601, "Method not found")
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	sink := &appServerWorkerTestSink{}
	res, err := worker.Run(context.Background(), "continue", sink)
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if res != (agentbackend.Result{}) {
		t.Fatalf("result=%+v, want zero result", res)
	}
	if sink.closed {
		t.Fatal("sink closed; fallback must be able to reuse it")
	}
}

func TestAppServerWorkerTurnStartMethodNotFoundFallsBackThroughCommander(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerErrorCode(t, w, *req.ID, -32601, "Method not found")
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fallbackCalls int
	backend := &appServerWorkerCommanderBackend{
		worker: worker,
		resumeFn: func(_ context.Context, id, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
			fallbackCalls++
			if id != "thr-1" || answer != "continue" {
				t.Fatalf("RunResume id=%q answer=%q, want thr-1/continue", id, answer)
			}
			sink.Write("chunk", "fallback")
			sink.Close()
			return agentbackend.Result{Summary: "fallback", SessionID: id}, nil
		},
	}
	h := &commander.Handler{Backend: backend}
	defer h.Close()

	sink := &appServerWorkerTestSink{}
	res, err := h.SessionTurn(context.Background(), "thr-1", "continue", sink)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("RunResume calls=%d, want 1", fallbackCalls)
	}
	if res.Summary != "fallback" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want fallback result", res)
	}
	if !equalAppServerWorkerEvents(sink.events, []appServerWorkerTestEvent{{kind: "chunk", data: "fallback"}}) {
		t.Fatalf("events=%+v, want fallback chunk", sink.events)
	}
	for _, status := range sink.statuses {
		if strings.Contains(status.text, "may have executed") || strings.Contains(status.text, "falling back") {
			t.Fatalf("status=%+v, want no duplicate-execution/fallback warning", status)
		}
	}
}

func TestAppServerWorkerTurnStartOtherRPCErrorDoesNotFallback(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerErrorCode(t, w, *req.ID, -32000, "turn rejected after submission")
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	sink := &appServerWorkerTestSink{}
	res, err := worker.Run(context.Background(), "continue", sink)
	if err == nil {
		t.Fatal("Run error = nil, want non-fallback protocol error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not allow fallback for non--32601 turn/start error", err)
	}
	if !strings.Contains(err.Error(), "turn rejected after submission") {
		t.Fatalf("Run error = %v, want rejection detail", err)
	}
	if res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want worker result with session ID", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed after non-fallback error")
	}
}

func TestAppServerWorkerSubmittedContextCancelInvalidatesManager(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
		writeFakeAppServerNotification(nil, w, "turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`)
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	codexWorker := worker.(*codexSessionWorker)
	generation := codexWorker.generation

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		_, err := worker.Run(ctx, "continue", &appServerWorkerTestSink{})
		runDone <- err
	}()
	fake.takeRequests(t, 4)
	cancel()
	err = receiveWithin(t, runDone, "canceled run")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if m.healthy(generation) {
		t.Fatal("manager remains healthy after submitted turn context cancellation")
	}
	if codexWorker.Healthy() {
		t.Fatal("worker remains healthy after manager invalidated by submitted cancellation")
	}
}

func TestAppServerWorkerReturnsAwaitingUserFromHumanloopIPC(t *testing.T) {
	var endpoint atomic.Value
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		switch req.Method {
		case "thread/resume":
			ep := endpointFromThreadResumeRequest(t, req)
			endpoint.Store(humanloop.EndpointArg(ep))
			return false
		case "turn/start":
			writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
			raw, ok := endpoint.Load().(string)
			if !ok {
				t.Error("turn/start before thread/resume endpoint captured")
				return true
			}
			ep, err := humanloop.ParseEndpointArg(raw)
			if err != nil {
				t.Errorf("ParseEndpointArg: %v", err)
				return true
			}
			c, err := humanloop.DialIPC(ep)
			if err != nil {
				t.Errorf("DialIPC: %v", err)
				return true
			}
			defer c.Close()
			_ = c.Send(humanloop.Payload{
				Kind:     "ask_user",
				Question: "pick?",
				Options:  []string{"a", "b"},
			})
			time.Sleep(10 * time.Millisecond)
			writeFakeAppServerNotification(t, w, "turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
			return true
		}
		return false
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	res, err := worker.Run(context.Background(), "continue", &appServerWorkerTestSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AwaitingUser == nil {
		t.Fatal("AwaitingUser = nil, want ask_user payload")
	}
	if res.AwaitingUser.Kind != "ask_user" || res.AwaitingUser.Question != "pick?" || len(res.AwaitingUser.Options) != 2 {
		t.Fatalf("AwaitingUser=%+v, want ask_user pick? options", res.AwaitingUser)
	}
}

func TestAppServerWorkerWaitsForTerminalEventBeforeReturningAwaitingUser(t *testing.T) {
	var endpoint atomic.Value
	payloadSent := make(chan struct{})
	allowCompletion := make(chan struct{})
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		switch req.Method {
		case "thread/resume":
			ep := endpointFromThreadResumeRequest(t, req)
			endpoint.Store(humanloop.EndpointArg(ep))
			return false
		case "turn/start":
			writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
			raw, ok := endpoint.Load().(string)
			if !ok {
				t.Error("turn/start before thread/resume endpoint captured")
				return true
			}
			ep, err := humanloop.ParseEndpointArg(raw)
			if err != nil {
				t.Errorf("ParseEndpointArg: %v", err)
				return true
			}
			c, err := humanloop.DialIPC(ep)
			if err != nil {
				t.Errorf("DialIPC: %v", err)
				return true
			}
			_ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "wait?"})
			_ = c.Close()
			close(payloadSent)
			<-allowCompletion
			writeFakeAppServerNotification(t, w, "turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
			return true
		}
		return false
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	type runResult struct {
		res agentbackend.Result
		err error
	}
	runDone := make(chan runResult, 1)
	go func() {
		res, err := worker.Run(context.Background(), "continue", &appServerWorkerTestSink{})
		runDone <- runResult{res: res, err: err}
	}()
	receiveWithin(t, payloadSent, "humanloop payload send")
	select {
	case got := <-runDone:
		close(allowCompletion)
		t.Fatalf("Run returned before terminal event: result=%+v err=%v", got.res, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowCompletion)
	got := receiveWithin(t, runDone, "run result after completion")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.res.AwaitingUser == nil || got.res.AwaitingUser.Question != "wait?" {
		t.Fatalf("AwaitingUser=%+v, want remembered wait? payload", got.res.AwaitingUser)
	}
}

func TestAppServerTerminalEventDrainsQueuedHumanloopPayload(t *testing.T) {
	payloads := newAppServerHumanloopPayloadBuffer(1)
	payloads.beginTurn()
	defer payloads.endTurn()
	payloads.deliver(humanloop.Payload{Kind: "ask_user", Question: "ready at terminal"})
	terminal := appServerWorkerNotification("turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
	terminalCh := make(chan appServerRPCMessage, 1)
	terminalCh <- terminal

	msg := receiveWithin(t, terminalCh, "queued terminal notification")
	if !appServerNotificationRelevantToTurn(appServerNotificationMetaFor(msg), "thr-1", "turn-1") {
		t.Fatal("terminal notification is not relevant to turn")
	}

	got := appServerRememberQueuedHumanloopPayload(payloads.C(), nil)
	if got == nil || got.Question != "ready at terminal" {
		t.Fatalf("AwaitingUser=%+v, want queued terminal-time payload", got)
	}
	select {
	case stale := <-payloads.C():
		t.Fatalf("payload remained queued after terminal drain: %+v", stale)
	default:
	}
}

func TestAppServerWorkerCompletionWithoutHumanloopPayloadDoesNotWaitGracePeriod(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
		writeFakeAppServerNotification(t, w, "turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	runDone := make(chan error, 1)
	go func() {
		res, err := worker.Run(context.Background(), "continue", &appServerWorkerTestSink{})
		if err == nil && res.AwaitingUser != nil {
			err = fmt.Errorf("AwaitingUser=%+v, want nil", res.AwaitingUser)
		}
		runDone <- err
	}()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Millisecond):
		t.Fatal("Run waited for humanloop grace period after terminal completion")
	}
}

func TestAppServerWorkerDropsHumanloopPayloadAfterCompletedTurn(t *testing.T) {
	var endpoint atomic.Value
	var turnStarts atomic.Int32
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		switch req.Method {
		case "thread/resume":
			ep := endpointFromThreadResumeRequest(t, req)
			endpoint.Store(humanloop.EndpointArg(ep))
			return false
		case "turn/start":
			turnID := fmt.Sprintf("turn-%d", turnStarts.Add(1))
			writeFakeAppServerResult(t, w, *req.ID, map[string]any{"turn": map[string]any{"id": turnID, "status": "running"}})
			writeFakeAppServerNotification(t, w, "turn/completed", fmt.Sprintf(`{"threadId":"thr-1","turn":{"id":%q,"status":"completed"}}`, turnID))
			return true
		}
		return false
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	res, err := worker.Run(context.Background(), "first", &appServerWorkerTestSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AwaitingUser != nil {
		t.Fatalf("first AwaitingUser=%+v, want nil", res.AwaitingUser)
	}
	raw, ok := endpoint.Load().(string)
	if !ok {
		t.Fatal("thread/resume endpoint was not captured")
	}
	ep, err := humanloop.ParseEndpointArg(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err := humanloop.DialIPC(ep)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(humanloop.Payload{Kind: "ask_user", Question: "stale"}); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	time.Sleep(20 * time.Millisecond)

	res, err = worker.Run(context.Background(), "second", &appServerWorkerTestSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AwaitingUser != nil {
		t.Fatalf("second AwaitingUser=%+v, want stale payload dropped", res.AwaitingUser)
	}
}

func TestAppServerWorkerRejectsInboundServerRequestAndFailsTurn(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
		writeFakeAppServerRequest(t, w, "77", "approval/request", `{"threadId":"thr-1","turnId":"turn-1"}`)
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), appServerRPCTestTimeout)
	defer cancel()
	_, err = worker.Run(ctx, "continue", &appServerWorkerTestSink{})
	if err == nil {
		t.Fatal("Run error = nil, want unsupported inbound request error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not allow fallback after unsupported inbound request", err)
	}
	if !strings.Contains(err.Error(), "approval/request") {
		t.Fatalf("Run error = %v, want approval/request detail", err)
	}

	reqs := fake.takeRequests(t, 5)
	resp := reqs[4]
	if resp.ID == nil || strings.TrimSpace(string(*resp.ID)) != "77" {
		t.Fatalf("response id=%v, want 77", resp.ID)
	}
	if resp.Method != "" {
		t.Fatalf("response method=%q, want empty", resp.Method)
	}
	if resp.Error == nil || resp.Error.Code != -32601 || !strings.Contains(resp.Error.Message, "approval/request") {
		t.Fatalf("response error=%+v, want unsupported approval/request error", resp.Error)
	}
}

func TestAppServerWorkerIgnoresRetryableErrorNotification(t *testing.T) {
	fake := newFakeAppServerTransport(t, func(req appServerRPCMessage, w io.Writer) bool {
		if req.Method != "turn/start" {
			return false
		}
		writeFakeAppServerResult(t, w, *req.ID, fakeAppServerResultFor(req.Method))
		writeFakeAppServerNotification(t, w, "error", `{"threadId":"thr-1","turnId":"turn-1","message":"transient","willRetry":true}`)
		writeFakeAppServerNotification(t, w, "item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"ok"}`)
		writeFakeAppServerNotification(t, w, "turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`)
		return true
	})
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thr-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	res, err := worker.Run(context.Background(), "continue", &appServerWorkerTestSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "ok" {
		t.Fatalf("summary=%q, want ok", res.Summary)
	}
}

func TestCodexSessionWorkerStreamsDeltasAndCapability(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	var gotPrompt string
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, prompt string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			gotPrompt = prompt
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"hello"}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"stale-turn","itemId":"old","delta":"stale"}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"other","turnId":"turn-x","itemId":"i2","delta":"ignored"}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"\n=== CAPABILITY ===\nnew cap"}`))
			emit(appServerWorkerNotification("turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`))
			return appServerTurnResult{}, nil
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
		runTurn: func(context.Context, string, func(appServerRPCMessage), func()) (appServerTurnResult, error) {
			return appServerTurnResult{}, runErr
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return appServerTurnResult{}, agentbackend.ErrSessionWorkerUnavailable
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

func TestCodexSessionWorkerTurnStartedAlonePreventsUnavailableFallback(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			return appServerTurnResult{}, agentbackend.ErrSessionWorkerUnavailable
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if err == nil {
		t.Fatal("Run error = nil, want non-sentinel unavailable detail")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not match ErrSessionWorkerUnavailable after turn/started", err)
	}
	if !strings.Contains(err.Error(), agentbackend.ErrSessionWorkerUnavailable.Error()) {
		t.Fatalf("Run error = %v, want unavailable detail preserved", err)
	}
	if res.Summary != "" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want empty summary for session thr-1", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
}

func TestCodexSessionWorkerDeltaAlonePreventsUnavailableFallback(t *testing.T) {
	sink := &appServerWorkerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return appServerTurnResult{}, agentbackend.ErrSessionWorkerUnavailable
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if err == nil {
		t.Fatal("Run error = nil, want non-sentinel unavailable detail")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("Run error = %v, should not match ErrSessionWorkerUnavailable after delta", err)
	}
	if !strings.Contains(err.Error(), agentbackend.ErrSessionWorkerUnavailable.Error()) {
		t.Fatalf("Run error = %v, want unavailable detail preserved", err)
	}
	if res.Summary != "partial" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v, want partial summary for session thr-1", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
}

func TestCodexSessionWorkerSubmittedUnavailableDoesNotFallbackThroughCommander(t *testing.T) {
	worker := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(_ context.Context, _ string, _ func(appServerRPCMessage), markSubmitted func()) (appServerTurnResult, error) {
			markSubmitted()
			return appServerTurnResult{}, agentbackend.ErrSessionWorkerUnavailable
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
		runTurn: func(context.Context, string, func(appServerRPCMessage), func()) (appServerTurnResult, error) {
			return appServerTurnResult{}, agentbackend.ErrSessionWorkerUnavailable
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return appServerTurnResult{}, runErr
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"other","turn":{"id":"turn-x","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"other","turnId":"turn-x","itemId":"i1","delta":"ignored"}`))
			emit(appServerWorkerNotification("turn/completed", `{"threadId":"other","turn":{"id":"turn-x","status":"completed"}}`))
			return appServerTurnResult{}, runErr
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("error", `{"threadId":"thr-1","message":"turn rejected"}`))
			return appServerTurnResult{}, nil
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
		runTurn: func(_ context.Context, _ string, emit func(appServerRPCMessage), _ func()) (appServerTurnResult, error) {
			emit(appServerWorkerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			emit(appServerWorkerNotification("error", `{"threadId":"thr-1","message":"lost worker"}`))
			return appServerTurnResult{}, nil
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

func TestAppServerNotificationRouterDeliversTerminalNotificationWhenBuffered(t *testing.T) {
	r := newAppServerNotificationRouter()
	sub := r.subscribe("thr-1")

	r.dispatch(appServerWorkerNotification("turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`))

	msg := receiveWithin(t, sub.ch, "terminal notification")
	if msg.Method != "turn/completed" {
		t.Fatalf("method=%q, want turn/completed", msg.Method)
	}
}

func TestAppServerNotificationRouterClosesSubscriptionOnOverflow(t *testing.T) {
	r := newAppServerNotificationRouter()
	sub := r.subscribe("thr-1")
	for i := 0; i < cap(sub.ch); i++ {
		r.dispatch(appServerWorkerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"x"}`))
	}

	r.dispatch(appServerWorkerNotification("turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`))

	for i := 0; i < cap(sub.ch); i++ {
		receiveWithin(t, sub.ch, "buffered notification")
	}
	select {
	case _, ok := <-sub.ch:
		if ok {
			t.Fatal("subscription channel still open after overflow")
		}
	case <-time.After(appServerRPCTestTimeout):
		t.Fatal("subscription channel not closed after overflow")
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

func TestWorkerBackendCloseClosesAppServerManager(t *testing.T) {
	fake := newFakeAppServerTransport(t, nil)
	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: "/repo"}
	m := newAppServerManager(cfg, nil)
	m.starter = fake.starter
	wb := &workerBackend{Backend: New(cfg, nil), manager: m}

	if err := m.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	generation := m.generation
	if !m.healthy(generation) {
		t.Fatal("manager unhealthy before backend close")
	}

	if err := wb.Close(); err != nil {
		t.Fatal(err)
	}
	if m.healthy(generation) {
		t.Fatal("manager healthy after backend close")
	}
	if got := fake.closeCount.Load(); got != 1 {
		t.Fatalf("transport close count=%d want 1", got)
	}
}

func appServerWorkerNotification(method string, params string) appServerRPCMessage {
	return appServerRPCMessage{Method: method, Params: json.RawMessage(params)}
}

type fakeAppServerTransport struct {
	requests   chan appServerRPCMessage
	closeCount atomic.Int32
	handler    func(appServerRPCMessage, io.Writer) bool
}

func newFakeAppServerTransport(t *testing.T, handler func(appServerRPCMessage, io.Writer) bool) *fakeAppServerTransport {
	t.Helper()
	return &fakeAppServerTransport{
		requests: make(chan appServerRPCMessage, 16),
		handler:  handler,
	}
}

func (f *fakeAppServerTransport) starter(context.Context, agentbackend.Config, []string) (*appServerConnection, error) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(serverReader)
		for sc.Scan() {
			var req appServerRPCMessage
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				return
			}
			f.requests <- req
			handled := false
			if f.handler != nil {
				handled = f.handler(req, serverWriter)
			}
			if req.ID == nil || handled {
				continue
			}
			select {
			case <-done:
				return
			default:
			}
			if req.Method == "initialize" || req.Method == "thread/resume" || req.Method == "turn/start" {
				writeFakeAppServerResult(nil, serverWriter, *req.ID, fakeAppServerResultFor(req.Method))
			}
		}
	}()
	closeFn := func() error {
		f.closeCount.Add(1)
		_ = clientReader.Close()
		_ = clientWriter.Close()
		_ = serverReader.Close()
		_ = serverWriter.Close()
		select {
		case <-done:
		case <-time.After(appServerRPCTestTimeout):
		}
		return nil
	}
	return &appServerConnection{
		rpc:   newAppServerRPC(clientReader, clientWriter),
		close: closeFn,
	}, nil
}

func (f *fakeAppServerTransport) takeRequests(t *testing.T, n int) []appServerRPCMessage {
	t.Helper()
	got := make([]appServerRPCMessage, 0, n)
	for len(got) < n {
		select {
		case req := <-f.requests:
			got = append(got, req)
		case <-time.After(appServerRPCTestTimeout):
			t.Fatalf("timed out waiting for request %d/%d", len(got)+1, n)
		}
	}
	return got
}

func fakeAppServerResultFor(method string) any {
	switch method {
	case "turn/start":
		return map[string]any{"turn": map[string]any{"id": "turn-1", "status": "running"}}
	case "thread/resume":
		return map[string]any{"thread": map[string]any{"id": "thr-1"}}
	default:
		return map[string]any{}
	}
}

func writeFakeAppServerResult(t *testing.T, w io.Writer, id json.RawMessage, result any) {
	if t != nil {
		t.Helper()
	}
	body, err := json.Marshal(appServerRPCMessage{ID: &id, Result: mustMarshalRaw(result)})
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		return
	}
	_, err = fmt.Fprintln(w, string(body))
	if err != nil && t != nil {
		t.Fatal(err)
	}
}

func writeFakeAppServerError(t *testing.T, w io.Writer, id json.RawMessage, message string) {
	t.Helper()
	writeFakeAppServerErrorCode(t, w, id, -32000, message)
}

func writeFakeAppServerErrorCode(t *testing.T, w io.Writer, id json.RawMessage, code int, message string) {
	t.Helper()
	body, err := json.Marshal(appServerRPCMessage{ID: &id, Error: &appServerError{Code: code, Message: message}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(w, string(body)); err != nil {
		t.Fatal(err)
	}
}

func writeFakeAppServerNotification(t *testing.T, w io.Writer, method string, params string) {
	if t != nil {
		t.Helper()
	}
	body, err := json.Marshal(appServerWorkerNotification(method, params))
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := fmt.Fprintln(w, string(body)); err != nil {
		if t != nil {
			t.Fatal(err)
		}
	}
}

func writeFakeAppServerRequest(t *testing.T, w io.Writer, id, method, params string) {
	t.Helper()
	rawID := json.RawMessage(id)
	body, err := json.Marshal(appServerRPCMessage{
		ID:     &rawID,
		Method: method,
		Params: json.RawMessage(params),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(w, string(body)); err != nil {
		t.Fatal(err)
	}
}

func mustMarshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func assertInitializeRequest(t *testing.T, req appServerRPCMessage) {
	t.Helper()
	if req.ID == nil {
		t.Fatal("initialize missing request id")
	}
	var params struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"clientInfo"`
		Capabilities struct {
			ExperimentalAPI bool `json:"experimentalApi"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ClientInfo.Name != "loom_driver_daemon" ||
		params.ClientInfo.Title != "Loom Driver Daemon" ||
		params.ClientInfo.Version != "v0.0.0" ||
		!params.Capabilities.ExperimentalAPI {
		t.Fatalf("initialize params=%s", string(req.Params))
	}
}

func assertInitializedNotification(t *testing.T, req appServerRPCMessage) {
	t.Helper()
	if req.ID != nil {
		t.Fatalf("initialized id=%s, want omitted", string(*req.ID))
	}
	if strings.TrimSpace(string(req.Params)) != "{}" {
		t.Fatalf("initialized params=%s, want {}", string(req.Params))
	}
}

func assertThreadResumeRequest(t *testing.T, req appServerRPCMessage, threadID, cwd, command, maxQuestions string) {
	t.Helper()
	if req.ID == nil {
		t.Fatal("thread/resume missing request id")
	}
	var params struct {
		ThreadID string `json:"threadId"`
		CWD      string `json:"cwd"`
		Config   struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != threadID || params.CWD != cwd {
		t.Fatalf("thread/resume threadID=%q cwd=%q, want %q %q", params.ThreadID, params.CWD, threadID, cwd)
	}
	got := params.Config.MCPServers["loom_humanloop"]
	if got.Command != command {
		t.Fatalf("mcp command=%q, want %q", got.Command, command)
	}
	if len(got.Args) != 3 || got.Args[0] != "humanloop-mcp" || got.Args[2] != maxQuestions {
		t.Fatalf("mcp args=%#v, want [humanloop-mcp ENDPOINT %s]", got.Args, maxQuestions)
	}
	if _, err := humanloop.ParseEndpointArg(got.Args[1]); err != nil {
		t.Fatalf("humanloop endpoint arg: %v", err)
	}
}

func endpointFromThreadResumeRequest(t *testing.T, req appServerRPCMessage) humanloop.Endpoint {
	t.Helper()
	var params struct {
		Config struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	args := params.Config.MCPServers["loom_humanloop"].Args
	if len(args) < 2 {
		t.Fatalf("mcp args=%#v, want endpoint arg", args)
	}
	ep, err := humanloop.ParseEndpointArg(args[1])
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func assertTurnStartRequest(t *testing.T, req appServerRPCMessage, threadID, cwd, text string) {
	t.Helper()
	if req.ID == nil {
		t.Fatal("turn/start missing request id")
	}
	var params struct {
		ThreadID string `json:"threadId"`
		CWD      string `json:"cwd"`
		Input    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != threadID || params.CWD != cwd {
		t.Fatalf("turn/start threadID=%q cwd=%q, want %q %q", params.ThreadID, params.CWD, threadID, cwd)
	}
	if len(params.Input) != 1 || params.Input[0].Type != "text" || params.Input[0].Text != text {
		t.Fatalf("turn/start input=%+v, want text %q", params.Input, text)
	}
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

func equalStringSlices(a, b []string) bool {
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
