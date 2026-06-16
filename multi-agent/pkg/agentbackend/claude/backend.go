package claude

import (
	"context"
	"errors"

	"github.com/yourorg/multi-agent/internal/sessioncache"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Backend is the Claude implementation of agentbackend.Backend.
type Backend struct {
	cfg  agentbackend.Config
	env  []string
	exec *executor
	perm *Store
	llm  *llmRunner
	list *sessioncache.FileCache
}

// New creates a Backend wired with an executor, permissions store, and LLM runner.
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "claude"
	}
	return &Backend{
		cfg:  cfg,
		env:  env,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
		list: sessioncache.NewFileCache(),
	}
}

// Kind implements agentbackend.Backend.
func (b *Backend) Kind() Kind { return agentbackend.KindClaude }

// Run implements agentbackend.Backend.
func (b *Backend) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	return b.exec.Run(ctx, t, sink)
}

// RunResume implements agentbackend.Backend.
func (b *Backend) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	workDir, err := b.resumeWorkDir(ctx, sessionID)
	if err != nil {
		return agentbackend.Result{}, err
	}
	return b.executorForWorkDir(workDir).RunResume(ctx, sessionID, answer, sink)
}

// LLM implements agentbackend.Backend.
func (b *Backend) LLM() agentbackend.LLMRunner { return b.llm }

// Permissions implements agentbackend.Backend.
func (b *Backend) Permissions() agentbackend.PermissionsStore { return b.perm }

// Detect implements agentbackend.Backend.
func (b *Backend) Detect(ctx context.Context) error { return detect(ctx, b.cfg.Bin) }

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

// Kind is a local alias so package-internal code can reference it without the full path.
type Kind = agentbackend.Kind

func init() {
	agentbackend.RegisterBuilder(agentbackend.KindClaude, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
