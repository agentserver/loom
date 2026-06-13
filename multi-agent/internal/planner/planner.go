package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

const planMaxAttempts = 3 // 1 initial + 2 retries; see §1.5 #21 decision record

var (
	// errPlanParse signals the LLM output did not parse as JSON; the
	// retry loop feeds the parse error back to the LLM for self-repair.
	errPlanParse = errors.New("plan parse")
	// errPlanValidate signals one or more nodes referenced an unknown
	// target_id; retry feeds the offending ids and the permitted set back.
	errPlanValidate = errors.New("plan validate")
)

// agentIDSet returns the set of agent_ids available for a given
// planning call. Used by validatePlanNodes to white-list target_id.
// Cards with an empty AgentID are skipped: otherwise "" would land
// in the permitted set and defeat Route's `target_id:""` "no
// suitable agent" sentinel by making it look like a valid pick.
func agentIDSet(agents []agentsdk.AgentCard) map[string]struct{} {
	set := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if a.AgentID != "" {
			set[a.AgentID] = struct{}{}
		}
	}
	return set
}

// validatePlanNodes returns nil if every node's target_id is in
// permitted. Otherwise returns a descriptive error naming every
// offending node and listing the permitted set, so the retry loop
// can give the LLM enough info to self-repair on the next attempt.
func validatePlanNodes(nodes []Node, permitted map[string]struct{}) error {
	var bad []string
	for _, n := range nodes {
		if _, ok := permitted[n.TargetID]; !ok {
			bad = append(bad, fmt.Sprintf(`%s→%q`, n.ID, n.TargetID))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	allowed := make([]string, 0, len(permitted))
	for id := range permitted {
		allowed = append(allowed, id)
	}
	sort.Strings(allowed) // deterministic feedback to LLM + stable error messages
	return fmt.Errorf("unknown target_id(s): [%s]; permitted: [%s]",
		strings.Join(bad, ", "),
		strings.Join(allowed, ", "),
	)
}

// Route asks the LLM to pick a single agent. Returns "" when no
// agent is suitable. Retries up to planMaxAttempts on parse failure
// or unknown target_id (empty target_id is a legitimate response,
// not a validation failure). Deletes the old TrimSpace-as-id
// fallback that turned any LLM freeform text into a dispatch target.
// Fixes §1.5 #21 of docs/review-2026-06-13.md.
func (p *Planner) Route(ctx context.Context, prompt string, agents []agentsdk.AgentCard) (string, error) {
	permitted := agentIDSet(agents)
	var lastErr error
	var feedback string
	for attempt := 1; attempt <= planMaxAttempts; attempt++ {
		out, err := p.runLLM(ctx, routePrompt(prompt, agents, feedback))
		if err != nil {
			return "", err
		}
		var r struct {
			TargetID string `json:"target_id"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			// Route output is a one-liner ({"target_id":"..."}); 256 bytes
			// is plenty to show the LLM what its previous attempt produced.
			lastErr = fmt.Errorf("%w: %v; output: %s", errPlanParse, err, truncate(out, 256))
			feedback = fmt.Sprintf(
				`Your previous response failed to parse: %v. Return EXACTLY one line of JSON: {"target_id":"<agent_id>"} or {"target_id":""}.`,
				err,
			)
			continue
		}
		if r.TargetID == "" {
			return "", nil // "no suitable agent" — legitimate contract
		}
		if _, ok := permitted[r.TargetID]; !ok {
			lastErr = fmt.Errorf(`%w: target_id %q not in available agents`, errPlanValidate, r.TargetID)
			feedback = fmt.Sprintf(
				`Your previous response chose target_id %q which is not in the available agents list. Pick one of the listed agent_id values or use "" if none is suitable.`,
				r.TargetID,
			)
			continue
		}
		return r.TargetID, nil
	}
	return "", fmt.Errorf("planner: route rejected after %d attempts; last error: %w", planMaxAttempts, lastErr)
}

// Plan asks the LLM to decompose a task into a DAG. It runs up to
// planMaxAttempts attempts, retrying with structured feedback on
// parse failure or unknown target_id. Returns the last error
// wrapped with the attempt count after the cap.
// Fixes §1.5 #21 of docs/review-2026-06-13.md.
func (p *Planner) Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]Node, error) {
	permitted := agentIDSet(agents)
	var lastErr error
	var feedback string
	for attempt := 1; attempt <= planMaxAttempts; attempt++ {
		out, err := p.runLLM(ctx, planPrompt(prompt, agents, feedback))
		if err != nil {
			return nil, err // LLM transport / timeout — don't retry here
		}
		out = stripJSONFence(out)
		var nodes []Node
		if err := json.Unmarshal([]byte(out), &nodes); err != nil {
			// Plan output is a JSON array of nodes; 512 bytes captures
			// enough structural context (vs Route's 256) for the LLM to
			// see where its previous attempt went off the rails.
			lastErr = fmt.Errorf("%w: %v; output: %s", errPlanParse, err, truncate(out, 512))
			feedback = fmt.Sprintf(
				"Your previous response failed to parse as JSON: %v. The output was:\n%s\nReturn ONLY a valid JSON array as instructed.",
				err, truncate(out, 512),
			)
			continue
		}
		if err := validatePlanNodes(nodes, permitted); err != nil {
			lastErr = fmt.Errorf("%w: %v", errPlanValidate, err)
			feedback = fmt.Sprintf(
				"Your previous response contained invalid target_id values: %v. Return ONLY a JSON array where every target_id is one of the available agents listed above.",
				err,
			)
			continue
		}
		return nodes, nil
	}
	return nil, fmt.Errorf("planner: plan rejected after %d attempts; last error: %w", planMaxAttempts, lastErr)
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
