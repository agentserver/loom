package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type llmRunner struct {
	cfg agentbackend.Config
	env []string
}

func newLLM(cfg agentbackend.Config, env []string) *llmRunner {
	return &llmRunner{cfg: cfg, env: env}
}

func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
	args := []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-"}
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
		return "", fmt.Errorf("codex llm exit: %v: %s", err, tail)
	}
	return strings.TrimSpace(string(out)), nil
}
