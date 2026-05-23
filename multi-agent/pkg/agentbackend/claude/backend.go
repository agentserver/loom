package claude

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Backend is the Claude implementation of agentbackend.Backend.
type Backend struct {
	cfg  agentbackend.ClaudeConfig
	env  []string
	exec *executor
	perm *Store
	llm  *llmRunner
}

// New creates a Backend wired with an executor, permissions store, and LLM runner.
func New(cfg agentbackend.ClaudeConfig, env []string) *Backend {
	return &Backend{
		cfg:  cfg,
		env:  env,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}

// Kind implements agentbackend.Backend.
func (b *Backend) Kind() Kind { return agentbackend.KindClaude }

// Run implements agentbackend.Backend.
func (b *Backend) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	return b.exec.Run(ctx, t, sink)
}

// LLM implements agentbackend.Backend.
func (b *Backend) LLM() agentbackend.LLMRunner { return b.llm }

// Permissions implements agentbackend.Backend.
func (b *Backend) Permissions() agentbackend.PermissionsStore { return b.perm }

// Detect implements agentbackend.Backend.
func (b *Backend) Detect(ctx context.Context) error { return detect(ctx, b.cfg.Bin) }

// Kind is a local alias so package-internal code can reference it without the full path.
type Kind = agentbackend.Kind

func init() {
	agentbackend.RegisterBuilder(agentbackend.KindClaude, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg.Claude, env), nil
	})
}
