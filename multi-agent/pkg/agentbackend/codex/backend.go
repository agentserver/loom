package codex

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Backend is the Codex implementation of agentbackend.Backend.
type Backend struct {
	cfg  agentbackend.CodexConfig
	exec *executor
	perm *Store
	llm  *llmRunner
}

// New returns a fully-assembled Codex Backend. (Replaces the throwaway
// `New(...) *executor` stub from Task 11 — the executor_test.go calls
// still resolve to this new symbol because the signature returns a type
// that implements agentbackend.Backend; the test only calls .Run on it,
// which both types satisfy with the same shape.)
func New(cfg agentbackend.CodexConfig, env []string) *Backend {
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
func (b *Backend) LLM() agentbackend.LLMRunner          { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore { return b.perm }
func (b *Backend) Detect(ctx context.Context) error      { return detect(ctx, b.cfg.Bin) }

func init() {
	agentbackend.RegisterBuilder(agentbackend.KindCodex, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg.Codex, env), nil
	})
}
