package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/store"
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

		t, ok, err := p.poll(ctx)
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
		p.execute(ctx, t)
	}
}

func (p *Poller) poll(ctx context.Context) (pollTask, bool, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", p.cfg.ServerURL+"/api/agent/tasks/poll", nil)
	req.Header.Set("Authorization", "Bearer "+p.cfg.ProxyToken)
	resp, err := p.cli.Do(req)
	if err != nil {
		return pollTask{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		return pollTask{}, false, nil
	}
	if resp.StatusCode != 200 {
		return pollTask{}, false, fmt.Errorf("poll status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// agentserver returns a JSON array of pending tasks (possibly empty).
	// Older versions of this code (and the SDK at agentserver/pkg/agentsdk/task.go)
	// expected a single object; that decoder silently fails today, leaving the
	// task atomically marked `assigned` server-side with no agent processing it.
	var arr []pollTask
	if err := json.Unmarshal(body, &arr); err != nil {
		return pollTask{}, false, fmt.Errorf("decode poll: %w (body=%q)", err, string(body))
	}
	if len(arr) == 0 {
		return pollTask{}, false, nil
	}
	return arr[0], true, nil
}

func (p *Poller) execute(ctx context.Context, t pollTask) {
	p.putStatus(ctx, t.TaskID, map[string]interface{}{"status": "running"})
	res, err := p.disp.Run(ctx, executor.Task{
		ID: t.TaskID, Skill: t.Skill, Prompt: t.Prompt,
		SystemContext: t.SystemContext, TimeoutSec: t.TimeoutSeconds,
	})
	if err != nil {
		if !p.putStatusRetry(ctx, t.TaskID, map[string]interface{}{
			"status": "failed", "failure_reason": err.Error(),
		}) {
			_ = p.s.EnqueuePendingAck(t.TaskID, "failed")
		}
		return
	}
	if !p.putStatusRetry(ctx, t.TaskID, map[string]interface{}{
		"status": "completed", "output": res.Summary,
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
			body["output"] = a.Reason
		}
		if p.putStatus(ctx, a.TaskID, body) == nil {
			_ = p.s.DeletePendingAck(a.TaskID)
		}
	}
}
