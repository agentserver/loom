package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yourorg/multi-agent/internal/platform"
)

// ResumeBackend is the slice of agentbackend.Backend that ChatResumeExecutor
// uses. Declared here to keep this package free of an agentbackend import
// (which would create a cycle: agentbackend already imports executor for
// Task/Sink/Result re-exports).
type ResumeBackend interface {
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
	RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
}

type ChatResumeConfig struct {
	Backend  ResumeBackend
	FlockDir string // per-session lock files live here; typically $LOOM_HOME/<agent>/humanloop/
}

// ChatResumeExecutor handles the chat_resume skill: parses the JSON prompt
// {session_id, answer, kind}, takes an exclusive flock on FlockDir/<sid>.lock
// to prevent two simultaneous resumes from racing on the same backend session
// jsonl, then delegates to Backend.RunResume.
type ChatResumeExecutor struct{ cfg ChatResumeConfig }

func NewChatResume(c ChatResumeConfig) *ChatResumeExecutor { return &ChatResumeExecutor{cfg: c} }

func (e *ChatResumeExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	var body struct {
		SessionID string `json:"session_id"`
		Answer    string `json:"answer"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(t.Prompt), &body); err != nil {
		return Result{}, fmt.Errorf("chat_resume: bad prompt JSON: %w", err)
	}
	if body.SessionID == "" || body.Answer == "" {
		return Result{}, fmt.Errorf("chat_resume: session_id and answer required")
	}

	if err := os.MkdirAll(e.cfg.FlockDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("chat_resume: mkdir %s: %w", e.cfg.FlockDir, err)
	}
	lockPath := filepath.Join(e.cfg.FlockDir, body.SessionID+".lock")
	lock, err := platform.TryLock(lockPath)
	if err != nil {
		if errors.Is(err, platform.ErrLocked) {
			return Result{}, fmt.Errorf("chat_resume: session busy (flock=%s)", lockPath)
		}
		return Result{}, fmt.Errorf("chat_resume: open lock: %w", err)
	}
	defer lock.Unlock()

	return e.cfg.Backend.RunResume(ctx, body.SessionID, body.Answer, sink)
}
