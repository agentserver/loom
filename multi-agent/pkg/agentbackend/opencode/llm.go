package opencode

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// llmRunner implements agentbackend.LLMRunner by spawning
// `opencode run --dangerously-skip-permissions --format=json -`
// and returning the trimmed stdout. Used by the planner and by
// journal capability-merge calls (see internal/journal after PR #18).
//
// --format=json emits an nd-JSON event stream; this LLMRunner only
// cares about the final assistant text, which appears as the last
// significant frame. We trim trailing whitespace and return — callers
// that need structured events go through Backend.Run instead.
//
// --dangerously-skip-permissions is injected by default because the
// slave is unattended; operator overrides go in cfg.ExtraArgs.
type llmRunner struct {
	cfg agentbackend.Config
	env []string
}

func newLLM(cfg agentbackend.Config, env []string) *llmRunner {
	return &llmRunner{cfg: cfg, env: env}
}

func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
	args := []string{"run", "--dangerously-skip-permissions", "--format=json", "-"}
	if len(r.cfg.ExtraArgs) > 0 {
		args = append(args, r.cfg.ExtraArgs...)
	}
	cmd := exec.CommandContext(ctx, r.cfg.Bin, args...)
	cmd.Env = append(cmd.Environ(), r.env...)
	cmd.Stdin = strings.NewReader(stdinPrompt)
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()
	if err != nil {
		tail := stderrBuf.String()
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
		}
		return "", fmt.Errorf("opencode llm exit: %v: %s", err, tail)
	}
	return strings.TrimSpace(string(out)), nil
}
