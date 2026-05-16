package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/progress"
)

type ProgressFunc func(ctx context.Context, phase, message string, elapsed time.Duration)

const plannerIdleTimeout = 90 * time.Second

type Planner struct {
	cfg      config.Planner
	progress ProgressFunc
}

func New(cfg config.Planner) *Planner { return &Planner{cfg: cfg} }

func (p *Planner) WithProgress(fn ProgressFunc) *Planner {
	cp := *p
	cp.progress = fn
	return &cp
}

type Node struct {
	ID            string          `json:"id"`
	TargetID      string          `json:"target_id"`
	Prompt        string          `json:"prompt"`
	SystemContext string          `json:"-"`
	BuildSpec     json.RawMessage `json:"build_spec,omitempty"`
	DependsOn     []string        `json:"depends_on,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Skill         string          `json:"skill,omitempty"`
	Optional      bool            `json:"optional,omitempty"`
}

type SubResult struct {
	NodeID, TargetID, Prompt, Status, Output, Error string
}

func (p *Planner) Route(ctx context.Context, prompt string, agents []agentsdk.AgentCard) (string, error) {
	out, err := p.runClaude(ctx, routePrompt(prompt, agents))
	if err != nil {
		return "", err
	}
	var r struct {
		TargetID string `json:"target_id"`
	}
	if json.Unmarshal([]byte(out), &r) == nil {
		return r.TargetID, nil
	}
	// Fallback: trim and treat as raw target_id
	return strings.TrimSpace(out), nil
}

func (p *Planner) Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]Node, error) {
	out, err := p.runClaude(ctx, planPrompt(prompt, agents))
	if err != nil {
		return nil, err
	}
	out = stripJSONFence(out)
	var nodes []Node
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, fmt.Errorf("plan unmarshal: %w; output: %s", err, truncate(out, 512))
	}
	return nodes, nil
}

func stripJSONFence(out string) string {
	s := strings.TrimSpace(out)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return s
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return s
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func (p *Planner) Reduce(ctx context.Context, originalPrompt string, results []SubResult) (string, error) {
	return p.runClaude(ctx, reducePrompt(originalPrompt, results))
}

func (p *Planner) runClaude(ctx context.Context, stdinPrompt string) (string, error) {
	timeout := time.Duration(p.cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	var stderrBuf strings.Builder
	var out []byte
	commandDone := make(chan struct{})
	err := progress.RunWithHeartbeat(ctx, progress.Config{
		Interval:    15 * time.Second,
		IdleTimeout: plannerIdleTimeout,
		HardTimeout: timeout,
		Message:     "planner still running",
		Emit: func(ctx context.Context, elapsed time.Duration) {
			if p.progress != nil {
				p.progress(ctx, "planning", "planner still running", elapsed)
			}
		},
	}, func(runCtx context.Context) error {
		defer close(commandDone)
		args := append([]string{"--print"}, p.cfg.ExtraArgs...)
		cmd := exec.CommandContext(runCtx, p.cfg.Bin, args...)
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
		return "", fmt.Errorf("planner exit: %v: %s", err, tail)
	}
	return strings.TrimSpace(string(out)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
