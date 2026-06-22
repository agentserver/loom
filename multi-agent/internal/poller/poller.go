package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/yourorg/multi-agent/internal/dispatch"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Dispatcher is the contract poller uses to hand a task off for execution.
// slave's *dispatch.Dispatcher and master's *orchestrator.Orchestrator both satisfy this.
type Dispatcher interface {
	Run(ctx context.Context, t executor.Task) (executor.Result, error)
}

type Config struct {
	ServerURL  string
	ProxyToken string
	IdlePoll   time.Duration // default 5s, escalates to 30s after 5 min idle
	ActivePoll time.Duration // default 0 (poll immediately after task completes)
}

type Poller struct {
	cfg  Config
	cli  *http.Client
	disp Dispatcher
	s    *store.Store
}

func New(cfg Config, d Dispatcher, s *store.Store) *Poller {
	if cfg.IdlePoll == 0 {
		cfg.IdlePoll = 5 * time.Second
	}
	return &Poller{cfg: cfg, cli: &http.Client{Timeout: 10 * time.Second}, disp: d, s: s}
}

type pollTask struct {
	TaskID         string `json:"task_id"`
	Skill          string `json:"skill"`
	Prompt         string `json:"prompt"`
	SystemContext  string `json:"system_context"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (p *Poller) Run(ctx context.Context) error {
	var idleSince time.Time // zero = not currently idle
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		p.drainPendingAcks(ctx)

		tasks, ok, err := p.poll(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(p.cfg.IdlePoll):
			}
			continue
		}
		if !ok {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			backoff := p.cfg.IdlePoll
			if time.Since(idleSince) > 5*time.Minute {
				backoff = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		idleSince = time.Time{}
		for _, t := range tasks {
			if err := ctx.Err(); err != nil {
				return nil
			}
			p.execute(ctx, t)
		}
	}
}

func (p *Poller) poll(ctx context.Context) ([]pollTask, bool, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", p.cfg.ServerURL+"/api/agent/tasks/poll", nil)
	req.Header.Set("Authorization", "Bearer "+p.cfg.ProxyToken)
	resp, err := p.cli.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		return nil, false, nil
	}
	if resp.StatusCode != 200 {
		return nil, false, fmt.Errorf("poll status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// agentserver returns a JSON array of pending tasks (possibly empty).
	// Older versions of this code (and the SDK at agentserver/pkg/agentsdk/task.go)
	// expected a single object; that decoder silently fails today, leaving the
	// task atomically marked `assigned` server-side with no agent processing it.
	var arr []pollTask
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, false, fmt.Errorf("decode poll: %w (body=%q)", err, string(body))
	}
	if len(arr) == 0 {
		return nil, false, nil
	}
	// Skill is omitted from the poll response (agentserver/internal/server/agent_tasks.go
	// pollResponse struct has no Skill field). Fall back to GET /api/agent/tasks/{id}
	// which does include it; otherwise master can't dispatch the task.
	for i := range arr {
		if arr[i].Skill == "" {
			if skill, err := p.fetchSkill(ctx, arr[i].TaskID); err == nil {
				arr[i].Skill = skill
			}
		}
	}
	return arr, true, nil
}

func (p *Poller) fetchSkill(ctx context.Context, id string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", p.cfg.ServerURL+"/api/agent/tasks/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+p.cfg.ProxyToken)
	resp, err := p.cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch skill status %d", resp.StatusCode)
	}
	var info struct {
		Skill string `json:"skill"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return info.Skill, nil
}

func (p *Poller) execute(ctx context.Context, t pollTask) {
	p.putStatus(ctx, t.TaskID, map[string]interface{}{"status": "running"})
	// Strip the driver-stamped <loom_origin> marker from SystemContext before
	// the slave's chat backend sees it (JSON-prompt skills choke on prefixes),
	// and surface the parsed parent tuple on the Task so the codex executor
	// can persist it into the loom-meta sidecar (P1 already consumes these).
	systemContext := t.SystemContext
	var parentLink agentbackend.ParentLink
	if pl, cleaned, ok := agentbackend.ParseLoomOrigin(systemContext); ok {
		parentLink = pl
		systemContext = cleaned
	}
	res, err := p.disp.Run(ctx, executor.Task{
		ID: t.TaskID, Skill: t.Skill, Prompt: t.Prompt,
		SystemContext:     systemContext,
		TimeoutSec:        t.TimeoutSeconds,
		ParentSessionID:   parentLink.SessionID,
		ParentAgentID:     parentLink.AgentID,
		ParentDisplayName: parentLink.DisplayName,
	})
	if err != nil {
		if errors.Is(err, dispatch.ErrDuplicateTaskRunning) {
			// Original run still in flight; let it publish terminal state.
			// (Do not PUT here — would clobber the in-flight state.)
			fmt.Fprintf(os.Stderr, "poller: skipping duplicate delivery for task %s (already running)\n", t.TaskID)
			return
		}
		if !p.putStatusRetry(ctx, t.TaskID, map[string]interface{}{
			"status": "failed", "failure_reason": err.Error(),
		}) {
			_ = p.s.EnqueuePendingAck(t.TaskID, "failed")
		}
		return
	}
	// agentserver's handleUpdateTaskStatus only persists `result` (json.RawMessage),
	// not `output` — same field-name mismatch the SDK works around in Complete().
	// For chat-skill results the dispatcher already populated WrappedOutput
	// with the structured kind-marker envelope; forward it verbatim so the
	// driver can read session_id from info.Result without depending on the
	// observer relay (see executor.Result.WrappedOutput).
	// For non-chat skills WrappedOutput is empty; JSON-encode the summary
	// string and send it as `result` so it round-trips.
	var resultBytes []byte
	if res.WrappedOutput != "" && json.Valid([]byte(res.WrappedOutput)) {
		resultBytes = []byte(res.WrappedOutput)
	} else {
		resultBytes, _ = json.Marshal(res.Summary)
	}
	if !p.putStatusRetry(ctx, t.TaskID, map[string]interface{}{
		"status": "completed", "result": json.RawMessage(resultBytes),
	}) {
		_ = p.s.EnqueuePendingAck(t.TaskID, "completed")
	}
}

func (p *Poller) putStatus(ctx context.Context, id string, body map[string]interface{}) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "PUT", p.cfg.ServerURL+"/api/agent/tasks/"+id+"/status", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+p.cfg.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.cli.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status update %d", resp.StatusCode)
	}
	return nil
}

func (p *Poller) putStatusRetry(ctx context.Context, id string, body map[string]interface{}) bool {
	backoffs := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i := 0; i <= len(backoffs); i++ {
		if err := p.putStatus(ctx, id, body); err == nil {
			return true
		}
		if i == len(backoffs) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoffs[i]):
		}
	}
	return false
}

func (p *Poller) drainPendingAcks(ctx context.Context) {
	pa, err := p.s.PopPendingAcks()
	if err != nil {
		return
	}
	for _, a := range pa {
		body := map[string]interface{}{"status": a.Status}
		if a.Status == "failed" {
			body["failure_reason"] = a.Reason
		} else {
			enc, _ := json.Marshal(a.Reason)
			body["result"] = json.RawMessage(enc)
		}
		if p.putStatus(ctx, a.TaskID, body) == nil {
			_ = p.s.DeletePendingAck(a.TaskID)
		}
	}
}
