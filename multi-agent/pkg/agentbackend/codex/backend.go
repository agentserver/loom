package codex

import (
	"context"
	"errors"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Backend is the Codex implementation of agentbackend.Backend.
type Backend struct {
	cfg  agentbackend.Config
	exec *executor
	perm *Store
	llm  *llmRunner
}

// New returns a fully-assembled Codex Backend. (Replaces the throwaway
// `New(...) *executor` stub from Task 11 — the executor_test.go calls
// still resolve to this new symbol because the signature returns a type
// that implements agentbackend.Backend; the test only calls .Run on it,
// which both types satisfy with the same shape.)
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "codex"
	}
	return &Backend{
		cfg:  cfg,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}

func (b *Backend) Kind() agentbackend.Kind { return agentbackend.KindCodex }
func (b *Backend) Run(ctx context.Context, t agentbackend.Task, s agentbackend.Sink) (agentbackend.Result, error) {
	return b.exec.Run(ctx, t, s)
}
func (b *Backend) RunResume(ctx context.Context, sessionID, answer string, s agentbackend.Sink) (agentbackend.Result, error) {
	workDir, err := b.resumeWorkDir(ctx, sessionID)
	if err != nil {
		return agentbackend.Result{}, err
	}
	return b.executorForWorkDir(workDir).RunResume(ctx, sessionID, answer, s)
}
func (b *Backend) LLM() agentbackend.LLMRunner                { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore { return b.perm }
func (b *Backend) Detect(ctx context.Context) error           { return detect(ctx, b.cfg.Bin) }

func (b *Backend) resumeWorkDir(ctx context.Context, sessionID string) (string, error) {
	sess, _, err := b.GetSession(ctx, sessionID)
	if err == nil {
		if sess.WorkingDir != "" {
			return sess.WorkingDir, nil
		}
		return b.cfg.WorkDir, nil
	}
	if errors.Is(err, agentbackend.ErrSessionNotFound) {
		return b.cfg.WorkDir, nil
	}
	return "", err
}

func (b *Backend) executorForWorkDir(workDir string) *executor {
	if workDir == "" || workDir == b.cfg.WorkDir {
		return b.exec
	}
	cfg := b.cfg
	cfg.WorkDir = workDir
	exec := *b.exec
	exec.cfg = cfg
	return &exec
}

func init() {
	agentbackend.RegisterBuilder(agentbackend.KindCodex, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
