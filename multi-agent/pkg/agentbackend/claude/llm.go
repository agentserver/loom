package claude

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/progress"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

const llmIdleTimeout = 90 * time.Second

type llmRunner struct {
	cfg agentbackend.Config
	env []string
}

func newLLM(cfg agentbackend.Config, env []string) *llmRunner {
	return &llmRunner{cfg: cfg, env: env}
}

// Run honors a deadline carried in ctx; if none, defaults to 60s.
func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
	var stderrBuf strings.Builder
	var out []byte
	commandDone := make(chan struct{})
	timeout := 60 * time.Second
	if d, ok := ctx.Deadline(); ok {
		if rem := time.Until(d); rem > 0 {
			timeout = rem
		}
	}
	err := progress.RunWithHeartbeat(ctx, progress.Config{
		Interval:    15 * time.Second,
		IdleTimeout: llmIdleTimeout,
		HardTimeout: timeout,
		Message:     "claude llm still running",
	}, func(runCtx context.Context) error {
		defer close(commandDone)
		args := append([]string{"--print"}, r.cfg.ExtraArgs...)
		cmd := exec.CommandContext(runCtx, r.cfg.Bin, args...)
		cmd.Env = append(cmd.Environ(), r.env...)
		cmd.Stdin = strings.NewReader(stdinPrompt)
		cmd.Stderr = &stderrBuf
		var err error
		out, err = cmd.Output()
		return err
	})
	<-commandDone
	if err != nil {
		if strings.Contains(err.Error(), "hard timeout") {
			return "", fmt.Errorf("planner timeout after %s", timeout)
		}
		tail := stderrBuf.String()
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
		}
		return "", fmt.Errorf("claude llm exit: %v: %s", err, tail)
	}
	return strings.TrimSpace(string(out)), nil
}
