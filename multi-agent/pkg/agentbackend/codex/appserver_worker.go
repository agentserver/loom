package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	executorpkg "github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

var (
	_ agentbackend.SessionWorker        = (*codexSessionWorker)(nil)
	_ agentbackend.HealthySessionWorker = (*codexSessionWorker)(nil)
)

type appServerTurnResult struct {
	AwaitingUser  *executorpkg.AskUserPayload
	AllowFallback bool
}

type codexSessionWorker struct {
	sessionID  string
	workDir    string
	healthy    atomic.Bool
	generation int64
	healthyFn  func() bool

	// runTurn must call markSubmitted once the turn/start request may have
	// reached app-server; after that, Run must not allow RunResume fallback.
	runTurn func(ctx context.Context, prompt string, emit func(appServerRPCMessage), markSubmitted func()) (appServerTurnResult, error)
	closeFn func() error
}

func (w *codexSessionWorker) Run(ctx context.Context, prompt string, sink agentbackend.Sink) (agentbackend.Result, error) {
	agentbackend.WriteStatus(sink, agentbackend.StatusStarting, "starting codex app-server")

	var (
		mu              sync.Mutex
		text            strings.Builder
		submitted       bool
		accepted        bool
		answeringStatus bool
		notifyErr       error
		activeTurnID    string
	)

	markAccepted := func() {
		if !accepted {
			accepted = true
		}
		if !answeringStatus {
			answeringStatus = true
			agentbackend.WriteStatus(sink, agentbackend.StatusAnswering, "codex app-server running")
		}
	}

	markSubmitted := func() {
		mu.Lock()
		defer mu.Unlock()
		submitted = true
	}

	emit := func(msg appServerRPCMessage) {
		mu.Lock()
		defer mu.Unlock()

		switch msg.Method {
		case "turn/started":
			meta := appServerNotificationMetaFor(msg)
			if meta.ThreadID != w.sessionID || meta.TurnID == "" {
				return
			}
			if activeTurnID == "" {
				activeTurnID = meta.TurnID
			}
			if meta.TurnID != activeTurnID {
				return
			}
			markAccepted()
		case "item/agentMessage/delta":
			var p struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Delta    string `json:"delta"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return
			}
			if p.ThreadID != w.sessionID || p.Delta == "" || p.TurnID == "" {
				return
			}
			if activeTurnID == "" {
				activeTurnID = p.TurnID
			}
			if p.TurnID != activeTurnID {
				return
			}
			markAccepted()
			sink.Write("chunk", p.Delta)
			text.WriteString(p.Delta)
		case "turn/completed":
			meta := appServerNotificationMetaFor(msg)
			if meta.ThreadID != w.sessionID || meta.TurnID == "" {
				return
			}
			if activeTurnID == "" || meta.TurnID != activeTurnID {
				return
			}
			markAccepted()
		case "error":
			meta := appServerNotificationMetaFor(msg)
			if meta.ThreadID != "" && meta.ThreadID != w.sessionID {
				return
			}
			if meta.TurnID != "" {
				if activeTurnID == "" || meta.TurnID != activeTurnID {
					return
				}
			}
			if !w.appServerErrorForSession(msg) {
				return
			}
			if appServerErrorWillRetry(msg) {
				return
			}
			notifyErr = fmt.Errorf("app-server error: %s", strings.TrimSpace(string(msg.Params)))
		}
	}

	runErr := agentbackend.ErrSessionWorkerUnavailable
	var turnResult appServerTurnResult
	if w.runTurn != nil {
		turnResult, runErr = w.runTurn(ctx, prompt, emit, markSubmitted)
	}

	mu.Lock()
	full := text.String()
	unsafeForFallback := accepted || turnResult.AwaitingUser != nil || (submitted && !turnResult.AllowFallback)
	if runErr == nil {
		runErr = notifyErr
	}
	mu.Unlock()

	summary, change := agentbackend.SplitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	result := agentbackend.Result{
		Summary:          summary,
		CapabilityChange: change,
		SessionID:        w.sessionID,
		AwaitingUser:     turnResult.AwaitingUser,
	}
	if runErr != nil && !unsafeForFallback {
		return agentbackend.Result{}, agentbackend.ErrSessionWorkerUnavailable
	}
	if runErr != nil {
		sink.Close()
		return result, nonFallbackWorkerRunError(runErr)
	}
	sink.Close()
	return result, nil
}

func (w *codexSessionWorker) Close() error {
	w.healthy.Store(false)
	if w.closeFn != nil {
		return w.closeFn()
	}
	return nil
}

func (w *codexSessionWorker) Healthy() bool {
	if !w.healthy.Load() {
		return false
	}
	if w.healthyFn != nil && !w.healthyFn() {
		w.healthy.Store(false)
		return false
	}
	return true
}

func (w *codexSessionWorker) appServerMessageForSession(msg appServerRPCMessage) bool {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return false
	}
	return p.ThreadID == w.sessionID
}

func (w *codexSessionWorker) appServerErrorForSession(msg appServerRPCMessage) bool {
	if len(msg.Params) == 0 {
		return true
	}
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return true
	}
	return p.ThreadID == "" || p.ThreadID == w.sessionID
}

func nonFallbackWorkerRunError(err error) error {
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		return fmt.Errorf("codex app-server turn may have executed before worker became unavailable: %v", err)
	}
	return err
}

type appServerNotificationMeta struct {
	ThreadID string
	TurnID   string
}

func appServerNotificationMetaFor(msg appServerRPCMessage) appServerNotificationMeta {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return appServerNotificationMeta{}
	}
	turnID := p.TurnID
	if turnID == "" {
		turnID = p.Turn.ID
	}
	return appServerNotificationMeta{ThreadID: p.ThreadID, TurnID: turnID}
}

func appServerErrorWillRetry(msg appServerRPCMessage) bool {
	var p struct {
		WillRetry bool `json:"willRetry"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return false
	}
	return p.WillRetry
}
