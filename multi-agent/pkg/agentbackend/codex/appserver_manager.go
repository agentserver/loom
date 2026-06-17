package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	executorpkg "github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

var (
	_ agentbackend.Backend              = (*workerBackend)(nil)
	_ agentbackend.SessionWorkerBackend = (*workerBackend)(nil)
)

const appServerHumanloopCompletionGrace = 25 * time.Millisecond

type workerBackend struct {
	*Backend
	manager *appServerManager
}

func (b *workerBackend) Close() error {
	if b == nil || b.manager == nil {
		return nil
	}
	return b.manager.close()
}

func (b *workerBackend) NewSessionWorker(ctx context.Context, session agentbackend.Session) (agentbackend.SessionWorker, error) {
	if session.ID == "" || b.manager == nil || b.Backend == nil || b.exec == nil {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	if err := b.manager.ensure(ctx); err != nil {
		if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
			return nil, err
		}
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	workDir := session.WorkingDir
	if workDir == "" {
		workDir = b.cfg.WorkDir
	}
	sockDir, err := os.MkdirTemp("", "loom-codex-appserver-humanloop-")
	if err != nil {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	srv, ep, err := humanloop.ListenIPC(sockDir)
	if err != nil {
		_ = os.RemoveAll(sockDir)
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	payloads := make(chan humanloop.Payload, 16)
	receiveDone := make(chan struct{})
	go func() {
		defer close(receiveDone)
		for {
			p, err := srv.Receive()
			if err != nil {
				return
			}
			select {
			case payloads <- p:
			default:
			}
		}
	}()

	resumeParams := appServerThreadResumeParams{
		ThreadID: session.ID,
		CWD:      workDir,
		Config: appServerConfig{
			MCPServers: map[string]appServerMCPServer{
				"loom_humanloop": {
					Command: b.exec.binSelf,
					Args: []string{
						"humanloop-mcp",
						humanloop.EndpointArg(ep),
						strconv.Itoa(b.exec.maxQuestions),
					},
				},
			},
		},
	}
	generation, err := b.manager.resumeThread(ctx, resumeParams)
	if err != nil {
		_ = srv.Close()
		<-receiveDone
		_ = os.RemoveAll(sockDir)
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}

	var closeOnce sync.Once
	closeFn := func() error {
		var closeErr error
		closeOnce.Do(func() {
			closeErr = srv.Close()
			<-receiveDone
			if err := os.RemoveAll(sockDir); closeErr == nil {
				closeErr = err
			}
		})
		return closeErr
	}

	w := &codexSessionWorker{
		sessionID:  session.ID,
		workDir:    workDir,
		generation: generation,
		healthyFn: func() bool {
			return b.manager.healthy(generation)
		},
		closeFn: closeFn,
	}
	w.runTurn = func(ctx context.Context, prompt string, emit func(appServerRPCMessage), markSubmitted func()) (appServerTurnResult, error) {
		return b.runAppServerTurn(ctx, w, prompt, payloads, emit, markSubmitted)
	}
	w.healthy.Store(true)
	return w, nil
}

func (b *workerBackend) runAppServerTurn(
	ctx context.Context,
	w *codexSessionWorker,
	prompt string,
	payloads <-chan humanloop.Payload,
	emit func(appServerRPCMessage),
	markSubmitted func(),
) (appServerTurnResult, error) {
	drainHumanloopPayloads(payloads)

	sub, err := b.manager.subscribe(w.sessionID, w.generation)
	if err != nil {
		return appServerTurnResult{}, err
	}
	defer sub.close()

	params := appServerTurnStartParams{
		ThreadID: w.sessionID,
		CWD:      w.workDir,
		Input: []appServerTurnInput{{
			Type: "text",
			Text: "User answered: " + prompt,
		}},
	}
	startResult, err := b.manager.startTurn(ctx, w.generation, params, markSubmitted)
	turnID := startResult.Turn.ID
	if turnID != "" {
		emit(appServerSyntheticTurnStarted(w.sessionID, turnID, startResult.Turn.Status))
	}
	if err != nil {
		if appServerMethodNotFound(err, "turn/start") {
			return appServerTurnResult{AllowFallback: true}, agentbackend.ErrSessionWorkerUnavailable
		}
		return appServerTurnResult{}, err
	}

	var result appServerTurnResult
	for {
		select {
		case p := <-payloads:
			result.AwaitingUser = humanloopPayloadToAwaitingUser(p)
			return result, nil
		case msg, ok := <-sub.ch:
			if !ok {
				return result, agentbackend.ErrSessionWorkerUnavailable
			}
			meta := appServerNotificationMetaFor(msg)
			if msg.Method == "turn/started" && meta.ThreadID == w.sessionID && meta.TurnID != "" && turnID == "" {
				turnID = meta.TurnID
			}
			emit(msg)
			if msg.Method == "error" && !appServerErrorWillRetry(msg) && appServerNotificationRelevantToTurn(meta, w.sessionID, turnID) {
				return result, nil
			}
			if msg.Method == "turn/completed" && appServerNotificationRelevantToTurn(meta, w.sessionID, turnID) {
				if result.AwaitingUser == nil {
					result.AwaitingUser = waitForHumanloopPayload(ctx, payloads, appServerHumanloopCompletionGrace)
				}
				return result, nil
			}
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

func drainHumanloopPayloads(payloads <-chan humanloop.Payload) {
	for {
		select {
		case <-payloads:
		default:
			return
		}
	}
}

func waitForHumanloopPayload(ctx context.Context, payloads <-chan humanloop.Payload, grace time.Duration) *executorpkg.AskUserPayload {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case p := <-payloads:
		return humanloopPayloadToAwaitingUser(p)
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return nil
	}
}

func humanloopPayloadToAwaitingUser(p humanloop.Payload) *executorpkg.AskUserPayload {
	return &executorpkg.AskUserPayload{
		Kind:     p.Kind,
		Question: p.Question,
		Options:  p.Options,
		Context:  p.Context,
		Intent:   p.Intent,
		Target:   p.Target,
		Reason:   p.Reason,
	}
}

func appServerSyntheticTurnStarted(threadID, turnID, status string) appServerRPCMessage {
	if status == "" {
		status = "running"
	}
	params, _ := json.Marshal(map[string]any{
		"threadId": threadID,
		"turn": map[string]any{
			"id":     turnID,
			"status": status,
		},
	})
	return appServerRPCMessage{Method: "turn/started", Params: params}
}

func handleUnsupportedAppServerRequest(rpc *appServerRPC, router *appServerNotificationRouter, msg appServerRPCMessage) {
	resp := appServerUnsupportedRequestResponse(msg)
	if router != nil {
		router.dispatch(appServerUnsupportedRequestNotification(msg, resp.Error.Message))
	}
	if rpc != nil && msg.ID != nil {
		_ = rpc.writeMessage(resp)
	}
}

func appServerUnsupportedRequestNotification(msg appServerRPCMessage, message string) appServerRPCMessage {
	meta := appServerNotificationMetaFor(msg)
	params, _ := json.Marshal(map[string]any{
		"threadId":  meta.ThreadID,
		"turnId":    meta.TurnID,
		"message":   message,
		"willRetry": false,
	})
	return appServerRPCMessage{Method: "error", Params: params}
}

func appServerMethodNotFound(err error, method string) bool {
	var rpcErr *appServerRPCError
	return errors.As(err, &rpcErr) && rpcErr.Method == method && rpcErr.Code == -32601
}

func appServerNotificationRelevantToTurn(meta appServerNotificationMeta, threadID, turnID string) bool {
	if meta.ThreadID != "" && meta.ThreadID != threadID {
		return false
	}
	if meta.TurnID == "" {
		return true
	}
	return turnID != "" && meta.TurnID == turnID
}

type appServerManager struct {
	cfg agentbackend.Config
	env []string

	mu         sync.Mutex
	started    bool
	rpc        *appServerRPC
	cmd        *exec.Cmd
	conn       *appServerConnection
	router     *appServerNotificationRouter
	generation int64
	starter    appServerStarter

	lifecycleCancel context.CancelFunc
}

func newAppServerManager(cfg agentbackend.Config, env []string) *appServerManager {
	return &appServerManager{
		cfg:     cfg,
		env:     append([]string(nil), env...),
		starter: startAppServerProcess,
	}
}

func (m *appServerManager) ensure(ctx context.Context) error {
	m.mu.Lock()
	if m.started && m.rpc != nil && m.rpc.terminalError() == nil {
		m.mu.Unlock()
		return nil
	}
	if m.started {
		conn := m.clearLocked()
		m.mu.Unlock()
		_ = closeAppServerConnection(conn)
		return m.ensure(ctx)
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return agentbackend.ErrSessionWorkerUnavailable
	}

	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	conn, err := m.starter(lifecycleCtx, m.cfg, m.env)
	if err != nil {
		lifecycleCancel()
		m.mu.Unlock()
		return agentbackend.ErrSessionWorkerUnavailable
	}
	if conn == nil || conn.rpc == nil {
		lifecycleCancel()
		m.mu.Unlock()
		_ = closeAppServerConnection(conn)
		return agentbackend.ErrSessionWorkerUnavailable
	}

	router := newAppServerNotificationRouter()
	conn.rpc.onNotification = router.dispatch
	conn.rpc.onRequest = func(msg appServerRPCMessage) {
		handleUnsupportedAppServerRequest(conn.rpc, router, msg)
	}
	readCtx, cancelRead := context.WithCancel(context.Background())
	baseClose := conn.close
	var closeOnce sync.Once
	var closeErr error
	conn.close = func() error {
		closeOnce.Do(func() {
			cancelRead()
			router.close()
			lifecycleCancel()
			if baseClose != nil {
				closeErr = baseClose()
			}
		})
		return closeErr
	}
	go func() {
		_ = conn.rpc.readLoop(readCtx)
		router.close()
	}()

	m.started = true
	m.rpc = conn.rpc
	m.conn = conn
	m.cmd = conn.cmd
	m.router = router
	m.lifecycleCancel = lifecycleCancel
	m.generation++
	rpc := m.rpc

	if err := ctx.Err(); err != nil {
		conn := m.clearLocked()
		m.mu.Unlock()
		_ = closeAppServerConnection(conn)
		return agentbackend.ErrSessionWorkerUnavailable
	}
	if err := rpc.call(ctx, "initialize", initializeParams(), nil); err != nil {
		conn := m.clearLocked()
		m.mu.Unlock()
		_ = closeAppServerConnection(conn)
		return agentbackend.ErrSessionWorkerUnavailable
	}
	if err := ctx.Err(); err != nil {
		conn := m.clearLocked()
		m.mu.Unlock()
		_ = closeAppServerConnection(conn)
		return agentbackend.ErrSessionWorkerUnavailable
	}
	if err := rpc.notify("initialized", map[string]any{}); err != nil {
		conn := m.clearLocked()
		m.mu.Unlock()
		_ = closeAppServerConnection(conn)
		return agentbackend.ErrSessionWorkerUnavailable
	}
	m.mu.Unlock()
	return nil
}

func (m *appServerManager) close() error {
	m.mu.Lock()
	conn := m.clearLocked()
	m.mu.Unlock()
	return closeAppServerConnection(conn)
}

func (m *appServerManager) clearLocked() *appServerConnection {
	conn := m.conn
	if m.router != nil {
		m.router.close()
	}
	if m.lifecycleCancel != nil {
		m.lifecycleCancel()
	}
	m.started = false
	m.rpc = nil
	m.cmd = nil
	m.conn = nil
	m.router = nil
	m.lifecycleCancel = nil
	m.generation++
	return conn
}

func (m *appServerManager) resumeThread(ctx context.Context, params appServerThreadResumeParams) (int64, error) {
	m.mu.Lock()
	rpc := m.rpc
	generation := m.generation
	m.mu.Unlock()
	if rpc == nil || rpc.terminalError() != nil {
		return 0, agentbackend.ErrSessionWorkerUnavailable
	}
	if err := rpc.call(ctx, "thread/resume", params, nil); err != nil {
		return 0, agentbackend.ErrSessionWorkerUnavailable
	}
	return generation, nil
}

func (m *appServerManager) startTurn(ctx context.Context, generation int64, params appServerTurnStartParams, markSubmitted func()) (appServerTurnStartResult, error) {
	m.mu.Lock()
	rpc := m.rpc
	if !m.started || m.generation != generation || rpc == nil || rpc.terminalError() != nil {
		m.mu.Unlock()
		return appServerTurnStartResult{}, agentbackend.ErrSessionWorkerUnavailable
	}
	m.mu.Unlock()

	var result appServerTurnStartResult
	err := rpc.callWithWriteHook(ctx, "turn/start", params, &result, markSubmitted)
	return result, err
}

func (m *appServerManager) subscribe(threadID string, generation int64) (*appServerSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.generation != generation || m.rpc == nil || m.router == nil || m.rpc.terminalError() != nil {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	return m.router.subscribe(threadID), nil
}

func (m *appServerManager) healthy(generation int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started &&
		m.generation == generation &&
		m.rpc != nil &&
		m.rpc.terminalError() == nil
}

func initializeParams() map[string]any {
	return map[string]any{
		"clientInfo": map[string]any{
			"name":    "loom_driver_daemon",
			"title":   "Loom Driver Daemon",
			"version": "v0.0.0",
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}
}

type appServerConfig struct {
	MCPServers map[string]appServerMCPServer `json:"mcp_servers"`
}

type appServerMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type appServerThreadResumeParams struct {
	ThreadID string          `json:"threadId"`
	CWD      string          `json:"cwd"`
	Config   appServerConfig `json:"config"`
}

type appServerTurnInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type appServerTurnStartParams struct {
	ThreadID string               `json:"threadId"`
	CWD      string               `json:"cwd"`
	Input    []appServerTurnInput `json:"input"`
}

type appServerTurnStartResult struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

type appServerStarter func(context.Context, agentbackend.Config, []string) (*appServerConnection, error)

type appServerConnection struct {
	rpc   *appServerRPC
	cmd   *exec.Cmd
	close func() error
}

func appServerProcessArgs(cfg agentbackend.Config) []string {
	args := []string{"app-server", "--listen", "stdio://"}
	return append(args, cfg.ExtraArgs...)
}

func startAppServerProcess(ctx context.Context, cfg agentbackend.Config, env []string) (*appServerConnection, error) {
	cmd := exec.CommandContext(ctx, cfg.Bin, appServerProcessArgs(cfg)...)
	cmd.Dir = cfg.WorkDir
	cmd.Env = append(cmd.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	var closeOnce sync.Once
	var closeErr error
	closeFn := func() error {
		closeOnce.Do(func() {
			_ = stdin.Close()
			_ = stdout.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			closeErr = <-waitDone
		})
		return closeErr
	}
	return &appServerConnection{
		rpc:   newAppServerRPC(stdout, stdin),
		cmd:   cmd,
		close: closeFn,
	}, nil
}

func closeAppServerConnection(conn *appServerConnection) error {
	if conn == nil || conn.close == nil {
		return nil
	}
	return conn.close()
}

type appServerNotificationRouter struct {
	mu     sync.Mutex
	closed bool
	subs   map[string]map[*appServerSubscription]struct{}
}

type appServerSubscription struct {
	threadID string
	ch       chan appServerRPCMessage
	router   *appServerNotificationRouter
}

func newAppServerNotificationRouter() *appServerNotificationRouter {
	return &appServerNotificationRouter{
		subs: make(map[string]map[*appServerSubscription]struct{}),
	}
}

func (r *appServerNotificationRouter) subscribe(threadID string) *appServerSubscription {
	sub := &appServerSubscription{
		threadID: threadID,
		ch:       make(chan appServerRPCMessage, 128),
		router:   r,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		close(sub.ch)
		return sub
	}
	if r.subs[threadID] == nil {
		r.subs[threadID] = make(map[*appServerSubscription]struct{})
	}
	r.subs[threadID][sub] = struct{}{}
	return sub
}

func (s *appServerSubscription) close() {
	if s == nil || s.router == nil {
		return
	}
	s.router.unsubscribe(s)
}

func (r *appServerNotificationRouter) unsubscribe(sub *appServerSubscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	subs := r.subs[sub.threadID]
	if subs == nil {
		return
	}
	if _, ok := subs[sub]; !ok {
		return
	}
	delete(subs, sub)
	close(sub.ch)
	if len(subs) == 0 {
		delete(r.subs, sub.threadID)
	}
}

func (r *appServerNotificationRouter) dispatch(msg appServerRPCMessage) {
	threadID := appServerNotificationThreadID(msg)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if threadID == "" && msg.Method == "error" {
		for _, subs := range r.subs {
			for sub := range subs {
				r.dispatchToSubscriptionLocked(sub, msg)
			}
		}
	} else {
		for sub := range r.subs[threadID] {
			r.dispatchToSubscriptionLocked(sub, msg)
		}
	}
}

func (r *appServerNotificationRouter) dispatchToSubscriptionLocked(sub *appServerSubscription, msg appServerRPCMessage) {
	select {
	case sub.ch <- msg:
	default:
		r.closeSubscriptionLocked(sub)
	}
}

func (r *appServerNotificationRouter) closeSubscriptionLocked(sub *appServerSubscription) {
	subs := r.subs[sub.threadID]
	if subs == nil {
		return
	}
	if _, ok := subs[sub]; !ok {
		return
	}
	delete(subs, sub)
	close(sub.ch)
	if len(subs) == 0 {
		delete(r.subs, sub.threadID)
	}
}

func (r *appServerNotificationRouter) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for threadID, subs := range r.subs {
		for sub := range subs {
			close(sub.ch)
		}
		delete(r.subs, threadID)
	}
}

func appServerNotificationThreadID(msg appServerRPCMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return ""
	}
	return p.ThreadID
}
