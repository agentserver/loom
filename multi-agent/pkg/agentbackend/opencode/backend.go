package opencode

import (
	"context"
	"fmt"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Backend is the opencode implementation of agentbackend.Backend.
// Mirrors pkg/agentbackend/codex/backend.go in shape — opencode is also
// invoked as `<bin> <subcmd> --flag …` and reads PROMPT from stdin.
//
// Backend-specific fields (humanloop MCP injection mechanism, event
// schema) live in executor.go.
type Backend struct {
	cfg  agentbackend.Config
	exec *executor
	perm *Store
	llm  *llmRunner
}

// New returns a fully-assembled opencode Backend with executor / permissions
// store / LLM runner wired. Defaults cfg.Bin to "opencode" when empty (the
// npm install target). See pkg/agentbackend/opencode/backend_test.go.
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "opencode"
	}
	return &Backend{
		cfg:  cfg,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}

func (b *Backend) Kind() agentbackend.Kind { return agentbackend.KindOpencode }

func (b *Backend) Run(ctx context.Context, t agentbackend.Task, s agentbackend.Sink) (agentbackend.Result, error) {
	return b.exec.Run(ctx, t, s)
}

func (b *Backend) RunResume(ctx context.Context, ref agentbackend.SessionRef, answer string, s agentbackend.Sink) (agentbackend.Result, error) {
	if !ref.HasBackend() {
		return agentbackend.Result{}, fmt.Errorf("opencode.Backend.RunResume: SessionRef has no backend id (Bridge=%q); cannot resume", ref.Bridge)
	}
	workDir, err := b.resumeWorkDir(ctx, ref.Backend)
	if err != nil {
		return agentbackend.Result{}, err
	}
	return b.executorForWorkDir(workDir).RunResume(ctx, ref, answer, s)
}

func (b *Backend) LLM() agentbackend.LLMRunner                { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore { return b.perm }
func (b *Backend) Detect(ctx context.Context) error           { return detect(ctx, b.cfg.Bin) }

func (b *Backend) resumeWorkDir(ctx context.Context, sessionID string) (string, error) {
	workDir, ok, err := b.sessionWorkingDir(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if !ok || workDir == "" {
		return b.cfg.WorkDir, nil
	}
	return workDir, nil
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

// init registers the opencode builder with the agentbackend registry. The
// builder runs only when this package is imported; CLI mains
// (cmd/{driver,slave}-agent/main.go) add the side-effect import.
func init() {
	agentbackend.RegisterBuilder(agentbackend.KindOpencode, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
