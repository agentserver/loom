package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

var (
	_ agentbackend.SessionWorker        = (*codexSessionWorker)(nil)
	_ agentbackend.HealthySessionWorker = (*codexSessionWorker)(nil)
)

type codexSessionWorker struct {
	sessionID string
	workDir   string
	healthy   atomic.Bool

	runTurn func(ctx context.Context, prompt string, emit func(appServerRPCMessage)) error
	closeFn func() error
}

func (w *codexSessionWorker) Run(ctx context.Context, prompt string, sink agentbackend.Sink) (agentbackend.Result, error) {
	agentbackend.WriteStatus(sink, agentbackend.StatusStarting, "starting codex app-server")

	var (
		mu              sync.Mutex
		text            strings.Builder
		accepted        bool
		answeringStatus bool
		notifyErr       error
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

	emit := func(msg appServerRPCMessage) {
		mu.Lock()
		defer mu.Unlock()

		switch msg.Method {
		case "turn/started":
			if !w.appServerMessageForSession(msg) {
				return
			}
			markAccepted()
		case "item/agentMessage/delta":
			var p struct {
				ThreadID string `json:"threadId"`
				Delta    string `json:"delta"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return
			}
			if p.ThreadID != w.sessionID || p.Delta == "" {
				return
			}
			markAccepted()
			sink.Write("chunk", p.Delta)
			text.WriteString(p.Delta)
		case "turn/completed":
			if !w.appServerMessageForSession(msg) {
				return
			}
			markAccepted()
		case "error":
			if !w.appServerErrorForSession(msg) {
				return
			}
			notifyErr = fmt.Errorf("app-server error: %s", strings.TrimSpace(string(msg.Params)))
		}
	}

	runErr := agentbackend.ErrSessionWorkerUnavailable
	if w.runTurn != nil {
		runErr = w.runTurn(ctx, prompt, emit)
	}

	mu.Lock()
	full := text.String()
	wasAccepted := accepted
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
	}
	if runErr != nil && !wasAccepted {
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
	return w.healthy.Load()
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
		return fmt.Errorf("codex app-server accepted execution before worker became unavailable: %v", err)
	}
	return err
}
