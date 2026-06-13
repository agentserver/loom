package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type ProgressFunc func(ctx context.Context, phase, message string, elapsed time.Duration)

type Planner struct {
	cfg      config.Planner
	llm      agentbackend.LLMRunner
	progress ProgressFunc
}

func New(cfg config.Planner, llm agentbackend.LLMRunner) *Planner {
	return &Planner{cfg: cfg, llm: llm}
}

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
	out, err := p.runLLM(ctx, routePrompt(prompt, agents, ""))
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
	out, err := p.runLLM(ctx, planPrompt(prompt, agents, ""))
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
	return p.runLLM(ctx, reducePrompt(originalPrompt, results))
}

func (p *Planner) runLLM(ctx context.Context, stdinPrompt string) (string, error) {
	timeout := time.Duration(p.cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := p.llm.Run(runCtx, stdinPrompt)
	if err != nil && runCtx.Err() != nil {
		// Context expired due to our timeout — normalize to a recognizable message.
		return "", fmt.Errorf("planner timeout after %s", timeout)
	}
	return out, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
