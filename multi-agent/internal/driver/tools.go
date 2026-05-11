package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/observer"
)

// SDKClient is the narrow agentserver SDK surface the driver tools use.
// *agentsdk.Client satisfies this interface; tests provide their own fake.
type SDKClient interface {
	DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
	DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	GetTask(ctx context.Context, taskID string, includeOutput bool) (*agentsdk.TaskInfo, error)
	PeerProxy(ctx context.Context, method, targetShortID, path string, body io.Reader) (*http.Response, error)
}

type ObserverSink interface {
	Emit(observer.Event)
}

// Tools holds shared state and exposes the six MCP tools as a slice.
type Tools struct {
	reg      *FileRegistry
	audit    *AuditLog
	sdk      SDKClient
	cfg      *Config
	observer ObserverSink
}

// NewTools constructs a Tools bundle.
func NewTools(reg *FileRegistry, audit *AuditLog, sdk SDKClient, cfg *Config, obs ObserverSink) *Tools {
	return &Tools{reg: reg, audit: audit, sdk: sdk, cfg: cfg, observer: obs}
}

func (t *Tools) emit(ev observer.Event) {
	if t.observer != nil {
		t.observer.Emit(ev)
	}
}

// All returns the six tools in stable order.
func (t *Tools) All() []Tool {
	return []Tool{
		&listAgentsTool{t},
		&submitTaskTool{t},
		&getTaskTool{t},
		&waitTaskTool{t},
		&tailSubtasksTool{t},
		&cancelTaskTool{t},
	}
}

// peerProxyURL builds the agentserver-side URL for the driver's own /files/* endpoint.
func (t *Tools) peerProxyURL(suffix string) string {
	return strings.TrimRight(t.cfg.Server.URL, "/") +
		"/api/agent/peer/" + t.cfg.Credentials.ShortID + "/proxy" + suffix
}

// resolveTarget picks a target agent by display_name override, config default,
// or auto-pick of the unique fanout-skilled agent.
func (t *Tools) resolveTarget(ctx context.Context, override string) (id, displayName, shortID, role string, err error) {
	cards, err := t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return "", "", "", "", &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	if override == "" {
		override = t.cfg.DriverDefaults.TargetDisplayName
	}
	if override != "" {
		for _, c := range cards {
			if c.DisplayName == override && c.AgentID != t.cfg.Credentials.SandboxID {
				return c.AgentID, c.DisplayName, cardShortID(c), observerRoleForCard(c), nil
			}
		}
		return "", "", "", "", &MCPToolError{Message: "no agent named: " + override}
	}
	var matches []agentsdk.AgentCard
	for _, c := range cards {
		if c.AgentID == t.cfg.Credentials.SandboxID {
			continue
		}
		if hasSkill(c, "fanout") {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return "", "", "", "", &MCPToolError{Message: "no fanout-skilled agent available; pass target_display_name"}
	}
	if len(matches) > 1 {
		names := []string{}
		for _, m := range matches {
			names = append(names, m.DisplayName)
		}
		return "", "", "", "", &MCPToolError{Message: "ambiguous target: " + strings.Join(names, ", ") + " (pass target_display_name)"}
	}
	return matches[0].AgentID, matches[0].DisplayName, cardShortID(matches[0]), observerRoleForCard(matches[0]), nil
}

func hasSkill(c agentsdk.AgentCard, want string) bool {
	var card struct {
		Skills []string `json:"skills"`
	}
	_ = json.Unmarshal(c.Card, &card)
	for _, s := range card.Skills {
		if s == want {
			return true
		}
	}
	return false
}

func observerRoleForCard(c agentsdk.AgentCard) string {
	if hasSkill(c, "fanout") || hasSkill(c, "route") || hasSkill(c, "fanout_strict") {
		return observer.RoleMaster
	}
	return observer.RoleSlave
}

func cardShortID(c agentsdk.AgentCard) string {
	// Prefer the top-level ShortID added by Task 3.5.
	if c.ShortID != "" {
		return c.ShortID
	}
	// Fallback for older agentserver builds.
	var card struct {
		ShortID string `json:"short_id"`
	}
	_ = json.Unmarshal(c.Card, &card)
	return card.ShortID
}

// =========================================================================
// list_agents
// =========================================================================

type listAgentsTool struct{ t *Tools }

func (l *listAgentsTool) Name() string { return "list_agents" }
func (l *listAgentsTool) Description() string {
	return "List agents in the workspace (driver-self filtered out)."
}
func (l *listAgentsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (l *listAgentsTool) Call(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	cards, err := l.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	type out struct {
		AgentID     string          `json:"agent_id"`
		DisplayName string          `json:"display_name"`
		ShortID     string          `json:"short_id,omitempty"`
		Skills      []string        `json:"skills"`
		Tools       []string        `json:"tools"`
		Resources   json.RawMessage `json:"resources,omitempty"`
		Description string          `json:"description,omitempty"`
	}
	results := []out{}
	for _, c := range cards {
		if c.AgentID == l.t.cfg.Credentials.SandboxID {
			continue
		}
		var card struct {
			Skills    []string        `json:"skills"`
			Tools     []string        `json:"tools"`
			Resources json.RawMessage `json:"resources"`
			ShortID   string          `json:"short_id"`
		}
		_ = json.Unmarshal(c.Card, &card)
		shortID := c.ShortID
		if shortID == "" {
			shortID = card.ShortID
		}
		results = append(results, out{
			AgentID: c.AgentID, DisplayName: c.DisplayName, ShortID: shortID,
			Skills: card.Skills, Tools: card.Tools, Resources: card.Resources,
			Description: c.Description,
		})
	}
	return json.Marshal(map[string]interface{}{"agents": results})
}

// =========================================================================
// submit_task
// =========================================================================

type submitTaskTool struct{ t *Tools }

func (s *submitTaskTool) Name() string { return "submit_task" }
func (s *submitTaskTool) Description() string {
	return "Submit a task to a workspace agent. read_paths and write_paths are local file/dir paths the user mentioned."
}
func (s *submitTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
        "type":"object",
        "properties":{
            "prompt":{"type":"string"},
            "read_paths":{"type":"array","items":{"type":"string"}},
            "write_paths":{"type":"array","items":{"type":"object","properties":{
                "path":{"type":"string"},"overwrite":{"type":"boolean"}
            },"required":["path"]}},
            "target_display_name":{"type":"string"},
            "skill":{"type":"string"},
            "timeout_sec":{"type":"integer"}
        },
        "required":["prompt"]
    }`)
}
func (s *submitTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Prompt     string   `json:"prompt"`
		ReadPaths  []string `json:"read_paths"`
		WritePaths []struct {
			Path      string `json:"path"`
			Overwrite bool   `json:"overwrite"`
		} `json:"write_paths"`
		TargetDisplayName string `json:"target_display_name"`
		Skill             string `json:"skill"`
		TimeoutSec        int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Prompt == "" {
		return nil, &MCPToolError{Message: "prompt is required"}
	}

	manifest := Manifest{}
	for _, p := range args.ReadPaths {
		absP, err := filepath.Abs(p)
		if err != nil {
			return nil, &MCPToolError{Message: "invalid path " + p + ": " + err.Error()}
		}
		info, err := os.Lstat(absP)
		if err != nil {
			return nil, &MCPToolError{Message: "stat " + absP + ": " + err.Error()}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, &MCPToolError{Message: "symlinks not allowed: " + absP}
		}
		if info.IsDir() {
			tok := s.t.reg.RegisterDir(absP)
			s.t.audit.Log(AuditEvent{Event: "register_read_dir", Path: absP})
			manifest.Files = append(manifest.Files, FileEntry{
				Path:    absP,
				Kind:    "dir",
				ListURL: s.t.peerProxyURL("/files/dir/" + tok + "?recursive=true"),
				BlobURL: s.t.peerProxyURL("/files/dir/" + tok + "/blob"),
			})
		} else {
			sha, size, mt, err := s.t.reg.RegisterFile(absP)
			if err != nil {
				return nil, &MCPToolError{Message: err.Error()}
			}
			s.t.audit.Log(AuditEvent{Event: "register_read", Path: absP, SHA256: sha, Bytes: size})
			manifest.Files = append(manifest.Files, FileEntry{
				Path:   absP,
				Kind:   "file",
				Bytes:  size,
				MIME:   mt,
				SHA256: sha,
				URL:    s.t.peerProxyURL("/files/blob/" + sha),
			})
		}
	}

	var writeTokens []string
	for _, w := range args.WritePaths {
		absP, err := filepath.Abs(w.Path)
		if err != nil {
			return nil, &MCPToolError{Message: "invalid write path: " + err.Error()}
		}
		if err := AssertWritableTarget(absP, s.t.cfg.DriverDefaults.DisableUIDCheck); err != nil {
			return nil, &MCPToolError{Message: err.Error()}
		}
		tok := s.t.reg.RegisterWrite(absP, w.Overwrite, "")
		writeTokens = append(writeTokens, tok)
		s.t.audit.Log(AuditEvent{Event: "register_write", Path: absP, Overwrite: w.Overwrite})
		manifest.Writes = append(manifest.Writes, WriteRequestEntry{
			Path:      absP,
			Kind:      "file",
			Overwrite: w.Overwrite,
			PutURL:    s.t.peerProxyURL("/files/put/" + tok),
		})
	}

	targetID, targetName, _, targetRole, err := s.t.resolveTarget(ctx, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}

	skill := args.Skill
	if skill == "" {
		skill = "fanout"
	}
	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}

	finalPrompt := manifest.Encode() + "\n\n" + args.Prompt
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}
	s.t.emit(observer.Event{
		Type:          observer.EventDriverTaskSubmitted,
		TaskID:        resp.TaskID,
		Summary:       observer.SummarizePrompt(args.Prompt, 80),
		Status:        "assigned",
		TargetAgentID: targetID,
		TargetRole:    targetRole,
	})

	for _, tok := range writeTokens {
		s.t.reg.RebindWriteTokenTaskID(tok, resp.TaskID)
	}
	s.t.reg.TrackTask(resp.TaskID, writeTokens)

	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"manifest":            manifest,
	})
}

// =========================================================================
// get_task
// =========================================================================

type getTaskTool struct{ t *Tools }

func (g *getTaskTool) Name() string        { return "get_task" }
func (g *getTaskTool) Description() string { return "Get current status/output of a delegated task." }
func (g *getTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "task_id":{"type":"string"},
        "include_subtasks":{"type":"boolean"}
    },"required":["task_id"]}`)
}
func (g *getTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID          string `json:"task_id"`
		IncludeSubtasks bool   `json:"include_subtasks"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	info, err := g.t.sdk.GetTask(ctx, args.TaskID, true)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	taskID := info.TaskID
	if taskID == "" {
		taskID = args.TaskID
	}
	g.t.emit(observer.Event{
		Type:   observer.EventDriverTaskStatus,
		TaskID: taskID,
		Status: info.Status,
	})
	return json.Marshal(map[string]interface{}{
		"status":         info.Status,
		"output":         info.Output,
		"failure_reason": info.FailureReason,
	})
}

// =========================================================================
// wait_task
// =========================================================================

type waitTaskTool struct{ t *Tools }

func (w *waitTaskTool) Name() string { return "wait_task" }
func (w *waitTaskTool) Description() string {
	return "Block until a delegated task reaches a terminal status; returns written_files for any PUT-back files."
}
func (w *waitTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "task_id":{"type":"string"},
        "poll_interval_sec":{"type":"integer"},
        "timeout_sec":{"type":"integer"}
    },"required":["task_id"]}`)
}
func (w *waitTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID          string `json:"task_id"`
		PollIntervalSec int    `json:"poll_interval_sec"`
		TimeoutSec      int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	if args.PollIntervalSec == 0 {
		args.PollIntervalSec = 3
	}
	if args.TimeoutSec == 0 {
		args.TimeoutSec = w.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	deadline := time.Now().Add(time.Duration(args.TimeoutSec) * time.Second)
	for {
		info, err := w.t.sdk.GetTask(ctx, args.TaskID, true)
		if err != nil {
			return nil, &MCPToolError{Message: err.Error()}
		}
		switch info.Status {
		case "completed", "failed", "cancelled":
			taskID := info.TaskID
			if taskID == "" {
				taskID = args.TaskID
			}
			w.t.emit(observer.Event{
				Type:   observer.EventDriverTaskStatus,
				TaskID: taskID,
				Status: info.Status,
			})
			written := w.t.reg.WrittenFiles(args.TaskID)
			w.t.reg.ForgetTask(args.TaskID)
			return json.Marshal(map[string]interface{}{
				"status":         info.Status,
				"output":         info.Output,
				"failure_reason": info.FailureReason,
				"written_files":  written,
			})
		}
		if time.Now().After(deadline) {
			return nil, &MCPToolError{Message: "wait_task timeout after " + fmt.Sprintf("%d", args.TimeoutSec) + "s; status=" + info.Status}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(args.PollIntervalSec) * time.Second):
		}
	}
}

// =========================================================================
// tail_subtasks
// =========================================================================

type tailSubtasksTool struct{ t *Tools }

func (ts *tailSubtasksTool) Name() string { return "tail_subtasks" }
func (ts *tailSubtasksTool) Description() string {
	return "Long-poll subtask events for a delegated task; returns events newer than since_seq."
}
func (ts *tailSubtasksTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "task_id":{"type":"string"},
        "since_seq":{"type":"integer"},
        "max_wait_sec":{"type":"integer"},
        "master_display_name":{"type":"string"}
    },"required":["task_id"]}`)
}
func (ts *tailSubtasksTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID            string `json:"task_id"`
		SinceSeq          int    `json:"since_seq"`
		MaxWaitSec        int    `json:"max_wait_sec"`
		MasterDisplayName string `json:"master_display_name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	if args.MaxWaitSec == 0 {
		args.MaxWaitSec = 30
	}
	if args.MaxWaitSec > 60 {
		args.MaxWaitSec = 60
	}
	cards, err := ts.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}

	// Resolve master with the same auto-pick / ambiguity rules as submit_task.
	override := args.MasterDisplayName
	if override == "" {
		override = ts.t.cfg.DriverDefaults.TargetDisplayName
	}
	masterShort := ""
	if override != "" {
		for _, c := range cards {
			if c.DisplayName == override && c.AgentID != ts.t.cfg.Credentials.SandboxID {
				masterShort = cardShortID(c)
				break
			}
		}
		if masterShort == "" {
			return nil, &MCPToolError{Message: "no agent named: " + override}
		}
	} else {
		var matches []agentsdk.AgentCard
		for _, c := range cards {
			if c.AgentID == ts.t.cfg.Credentials.SandboxID {
				continue
			}
			if hasSkill(c, "fanout") {
				matches = append(matches, c)
			}
		}
		if len(matches) == 0 {
			return nil, &MCPToolError{Message: "no fanout-skilled agent visible; pass master_display_name"}
		}
		if len(matches) > 1 {
			names := []string{}
			for _, m := range matches {
				names = append(names, m.DisplayName)
			}
			return nil, &MCPToolError{Message: "ambiguous master: " + strings.Join(names, ", ") + " (pass master_display_name)"}
		}
		masterShort = cardShortID(matches[0])
	}

	deadline := time.Now().Add(time.Duration(args.MaxWaitSec) * time.Second)
	for {
		path := "/tasks/" + args.TaskID + "/children"
		resp, err := ts.t.sdk.PeerProxy(ctx, "GET", masterShort, path, nil)
		if err != nil {
			return nil, &MCPToolError{Message: "peer-proxy: " + err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var rows []map[string]interface{}
		_ = json.Unmarshal(body, &rows)
		events := []map[string]interface{}{}
		for i, r := range rows {
			if i < args.SinceSeq {
				continue
			}
			events = append(events, map[string]interface{}{
				"seq":        i,
				"node_id":    r["node_id"],
				"target_id":  r["target_id"],
				"status":     r["status"],
				"created_at": r["created_at"],
			})
		}
		if len(events) > 0 || time.Now().After(deadline) {
			return json.Marshal(map[string]interface{}{
				"cursor": len(rows),
				"events": events,
			})
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// =========================================================================
// cancel_task — v1 stub
// =========================================================================

type cancelTaskTool struct{ t *Tools }

func (c *cancelTaskTool) Name() string { return "cancel_task" }
func (c *cancelTaskTool) Description() string {
	return "Cancel a delegated task. v1 stub: returns current status; SDK does not yet expose proxy_token cancel."
}
func (c *cancelTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`)
}
func (c *cancelTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	info, err := c.t.sdk.GetTask(ctx, args.TaskID, false)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return json.Marshal(map[string]interface{}{
		"ok":     false,
		"status": info.Status,
		"note":   "cancel_task is not implemented in v1; query get_task and wait for natural completion or timeout",
	})
}
