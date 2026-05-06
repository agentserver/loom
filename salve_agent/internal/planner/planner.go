package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/salve_agent/internal/config"
)

type Planner struct{ cfg config.Planner }

func New(cfg config.Planner) *Planner { return &Planner{cfg: cfg} }

type Node struct {
	ID        string   `json:"id"`
	TargetID  string   `json:"target_id"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"depends_on,omitempty"`
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
	var nodes []Node
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, fmt.Errorf("plan unmarshal: %w; output: %s", err, truncate(out, 512))
	}
	return nodes, nil
}

func (p *Planner) Reduce(ctx context.Context, originalPrompt string, results []SubResult) (string, error) {
	return p.runClaude(ctx, reducePrompt(originalPrompt, results))
}

func (p *Planner) runClaude(ctx context.Context, stdinPrompt string) (string, error) {
	timeout := time.Duration(p.cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{"--print"}, p.cfg.ExtraArgs...)
	cmd := exec.CommandContext(cctx, p.cfg.Bin, args...)
	cmd.Stdin = strings.NewReader(stdinPrompt)
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
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
