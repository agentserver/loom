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
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/orchestration"
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

type ContractRunner interface {
	Run(ctx context.Context, prompt string) (orchestration.RunnerResult, error)
}

// Tools holds shared state and exposes the six MCP tools as a slice.
type Tools struct {
	reg            *FileRegistry
	audit          *AuditLog
	taskJournal    *TaskJournal
	sdk            SDKClient
	cfg            *Config
	observer       ObserverSink
	relay          *ObserverRelay
	contractRunner ContractRunner
}

// NewTools constructs a Tools bundle.
func NewTools(reg *FileRegistry, audit *AuditLog, sdk SDKClient, cfg *Config, obs ObserverSink) *Tools {
	return &Tools{reg: reg, audit: audit, sdk: sdk, cfg: cfg, observer: obs, relay: NewObserverRelay(cfg, toTokenSource(obs))}
}

func (t *Tools) SetTaskJournal(j *TaskJournal) {
	t.taskJournal = j
}

func (t *Tools) SetContractRunner(r ContractRunner) {
	t.contractRunner = r
}

type delegatedTaskRecord struct {
	Tool              string
	Response          *agentsdk.DelegateTaskResponse
	TargetID          string
	TargetDisplayName string
	Skill             string
	Wait              bool
	TimeoutSec        int
}

func (t *Tools) recordDelegatedTask(rec delegatedTaskRecord) error {
	if t.taskJournal == nil || rec.Response == nil || rec.Response.TaskID == "" {
		return nil
	}
	if err := t.taskJournal.Append(TaskRecord{
		Tool:              rec.Tool,
		TaskID:            rec.Response.TaskID,
		SessionID:         rec.Response.SessionID,
		TargetID:          rec.TargetID,
		TargetDisplayName: rec.TargetDisplayName,
		Skill:             rec.Skill,
		Status:            rec.Response.Status,
		Wait:              rec.Wait,
		TimeoutSec:        rec.TimeoutSec,
	}); err != nil {
		return &MCPToolError{Message: fmt.Sprintf("task %s was created but driver failed to record it in driver-tasks.jsonl: %v", rec.Response.TaskID, err)}
	}
	return nil
}

func (t *Tools) emit(ev observer.Event) {
	if t.observer != nil {
		t.observer.Emit(ev)
	}
}

// All returns the driver MCP tools in stable order.
func (t *Tools) All() []Tool {
	return []Tool{
		&listAgentsTool{t},
		&listDriverTasksTool{t},
		&inspectCapabilitiesTool{t},
		&runSlaveBashTool{t},
		&runSlavePowerShellTool{t},
		&runSlaveShellTool{t},
		&registerSlaveMCPTool{t},
		&unregisterSlaveMCPTool{t},
		// Permission tools use task delegation until agentserver exposes a dedicated control channel.
		&getSlaveClaudePermissionsTool{t},
		&updateSlaveClaudePermissionsTool{t},
		&readSlaveFileTool{t},
		&writeSlaveFileTool{t},
		&statSlaveFileTool{t},
		&draftTaskContractTool{t},
		&dryRunContractTool{t},
		&submitTaskTool{t},
		&submitContractTaskTool{t},
		&getTaskTool{t},
		&waitTaskTool{t},
		&resumeTaskTool{t},
		&tailSubtasksTool{t},
		&cancelTaskTool{t},
	}
}

// peerProxyURL builds the agentserver-side URL for the driver's own /files/* endpoint.
func (t *Tools) peerProxyURL(suffix string) string {
	return strings.TrimRight(t.cfg.Server.URL, "/") +
		"/api/agent/peer/" + t.cfg.Credentials.ShortID + "/proxy" + suffix
}

func (t *Tools) useObserverRelay() bool {
	return t.cfg != nil && t.cfg.DriverDefaults.ArtifactTransport == ArtifactTransportObserverLazy
}

func (t *Tools) observerRelay() *ObserverRelay {
	if t.relay == nil {
		t.relay = NewObserverRelay(t.cfg, toTokenSource(t.observer))
	}
	return t.relay
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
		unavailable := false
		for _, c := range cards {
			if c.DisplayName == override && c.AgentID != t.cfg.Credentials.SandboxID {
				if !agentAvailable(c) {
					unavailable = true
					continue
				}
				return c.AgentID, c.DisplayName, cardShortID(c), observerRoleForCard(c), nil
			}
		}
		if unavailable {
			return "", "", "", "", &MCPToolError{Message: "agent named " + override + " is not available"}
		}
		return "", "", "", "", &MCPToolError{Message: "no agent named: " + override}
	}
	var matches []agentsdk.AgentCard
	for _, c := range cards {
		if c.AgentID == t.cfg.Credentials.SandboxID {
			continue
		}
		if !agentAvailable(c) {
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

func agentAvailable(c agentsdk.AgentCard) bool {
	return c.Status == "available"
}

// jsonPromptSkill reports whether the named skill's slave-side executor
// json.Unmarshals t.Prompt directly and therefore cannot tolerate any
// preamble (USER_FILES_MANIFEST, TASK_CONTRACT envelope, etc.). For these
// skills submit_task forwards the caller's prompt verbatim.
func jsonPromptSkill(skill string) bool {
	switch skill {
	case "mcp", "bash", "powershell", "register_mcp", "unregister_mcp", "claude_permissions", "permissions", "file", "chat_resume":
		return true
	}
	return false
}

func hasSkill(c agentsdk.AgentCard, want string) bool {
	return parseAgentCard(c).HasSkill(want)
}

func observerRoleForCard(c agentsdk.AgentCard) string {
	if hasSkill(c, "fanout") || hasSkill(c, "route") || hasSkill(c, "fanout_strict") {
		return observer.RoleMaster
	}
	return observer.RoleSlave
}

func listAgentRoleForCard(c agentsdk.AgentCard) string {
	switch c.AgentType {
	case observer.RoleDriver, observer.RoleMaster, observer.RoleSlave:
		return c.AgentType
	default:
		return observerRoleForCard(c)
	}
}

func cardShortID(c agentsdk.AgentCard) string {
	// agentserver v0.40.0 does not expose short_id as a top-level AgentCard
	// field. Agents that need peer-proxy addressing publish it inside card.
	return parseAgentCard(c).ShortID
}

// =========================================================================
// list_agents
// =========================================================================

type listAgentsTool struct{ t *Tools }

func (l *listAgentsTool) Name() string { return "list_agents" }
func (l *listAgentsTool) Description() string {
	return "List agents in the workspace with role and status; returns available agents unless include_unavailable is true (driver-self filtered out)."
}
func (l *listAgentsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"include_unavailable":{"type":"boolean"}},"additionalProperties":false}`)
}
func (l *listAgentsTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		IncludeUnavailable bool `json:"include_unavailable"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
		}
	}
	cards, err := l.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	type out struct {
		AgentID           string                          `json:"agent_id"`
		DisplayName       string                          `json:"display_name"`
		Status            string                          `json:"status"`
		Role              string                          `json:"role"`
		ShortID           string                          `json:"short_id,omitempty"`
		Skills            []string                        `json:"skills"`
		Tools             []string                        `json:"tools"`
		MCPTools          json.RawMessage                 `json:"mcp_tools,omitempty"`
		Resources         json.RawMessage                 `json:"resources,omitempty"`
		Platform          *commandiface.Platform          `json:"platform,omitempty"`
		CommandInterfaces []commandiface.CommandInterface `json:"command_interfaces,omitempty"`
		Description       string                          `json:"description,omitempty"`
	}
	results := []out{}
	for _, c := range cards {
		if c.AgentID == l.t.cfg.Credentials.SandboxID {
			continue
		}
		if !args.IncludeUnavailable && !agentAvailable(c) {
			continue
		}
		card := parseAgentCard(c)
		var platform *commandiface.Platform
		if card.Platform != (commandiface.Platform{}) {
			platform = &card.Platform
		}
		results = append(results, out{
			AgentID: c.AgentID, DisplayName: c.DisplayName, Status: c.Status, Role: listAgentRoleForCard(c), ShortID: card.ShortID,
			Skills: card.Skills, Tools: card.Tools, MCPTools: card.MCPTools, Resources: card.Resources,
			Platform: platform, CommandInterfaces: card.CommandInterfaces, Description: c.Description,
		})
	}
	return json.Marshal(map[string]interface{}{"agents": results})
}

// =========================================================================
// list_driver_tasks
// =========================================================================

type listDriverTasksTool struct{ t *Tools }

func (l *listDriverTasksTool) Name() string { return "list_driver_tasks" }
func (l *listDriverTasksTool) Description() string {
	return "List locally recorded driver-created delegated task IDs for recovery after MCP client timeouts or interrupts."
}
func (l *listDriverTasksTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"},"task_id":{"type":"string"}},"additionalProperties":false}`)
}
func (l *listDriverTasksTool) Call(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Limit  int    `json:"limit,omitempty"`
		TaskID string `json:"task_id,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
		}
	}
	if l.t.taskJournal == nil {
		return json.Marshal(map[string]interface{}{"journal_path": "", "tasks": []TaskRecord{}})
	}
	records, err := l.t.taskJournal.Recent(args.Limit, args.TaskID)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return json.Marshal(map[string]interface{}{"journal_path": l.t.taskJournal.Path(), "tasks": records})
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

	skill := args.Skill
	if skill == "" {
		skill = "fanout"
	}
	if jsonPromptSkill(skill) && (len(args.ReadPaths) > 0 || len(args.WritePaths) > 0) {
		return nil, &MCPToolError{Message: "skill " + skill + " takes JSON-only prompts; read_paths/write_paths cannot be conveyed"}
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
			if s.t.useObserverRelay() {
				return nil, &MCPToolError{Message: "observer_lazy directory read_paths are not implemented yet; use file paths or artifact_transport=peer_proxy"}
			}
			tok := s.t.reg.RegisterDir(absP)
			s.t.audit.Log(AuditEvent{Event: "register_read_dir", Path: absP})
			entry := FileEntry{
				Path:    absP,
				Kind:    "dir",
				ListURL: s.t.peerProxyURL("/files/dir/" + tok + "?recursive=true"),
				BlobURL: s.t.peerProxyURL("/files/dir/" + tok + "/blob"),
			}
			manifest.Files = append(manifest.Files, entry)
		} else {
			sha, size, mt, err := s.t.reg.RegisterFile(absP)
			if err != nil {
				return nil, &MCPToolError{Message: err.Error()}
			}
			s.t.audit.Log(AuditEvent{Event: "register_read", Path: absP, SHA256: sha, Bytes: size})
			entry := FileEntry{
				Path:   absP,
				Kind:   "file",
				Bytes:  size,
				MIME:   mt,
				SHA256: sha,
				URL:    s.t.peerProxyURL("/files/blob/" + sha),
			}
			if s.t.useObserverRelay() {
				relayResp, err := s.t.observerRelay().RegisterArtifact(ctx, observerArtifactCreate{
					Path: absP, Kind: "file", MIME: mt, Bytes: size, SHA256: sha, Mode: "lazy",
				})
				if err != nil {
					return nil, &MCPToolError{Message: "observer register file: " + err.Error()}
				}
				s.t.reg.RegisterObserverArtifact(relayResp.ArtifactID, absP, "file")
				entry.URL = relayResp.URL
			}
			manifest.Files = append(manifest.Files, entry)
		}
	}

	var writeTokens []string
	var observerWriteIDs []string
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
		putURL := s.t.peerProxyURL("/files/put/" + tok)
		if s.t.useObserverRelay() {
			relayResp, err := s.t.observerRelay().CreateWrite(ctx, observerWriteCreate{
				TaskID: "__pending__", Path: absP, Overwrite: w.Overwrite,
			})
			if err != nil {
				return nil, &MCPToolError{Message: "observer create write: " + err.Error()}
			}
			observerWriteIDs = append(observerWriteIDs, relayResp.WriteID)
			putURL = relayResp.PutURL
		}
		manifest.Writes = append(manifest.Writes, WriteRequestEntry{
			Path:      absP,
			Kind:      "file",
			Overwrite: w.Overwrite,
			PutURL:    putURL,
		})
	}

	targetID, targetName, _, targetRole, err := s.t.resolveTarget(ctx, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}

	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}

	// JSON-prompt skills parse t.Prompt with json.Unmarshal in the slave
	// executor; a USER_FILES_MANIFEST prefix breaks them with
	// "invalid character '<'". For those skills send the caller's prompt
	// verbatim; the early guard above already rejected any read/write paths
	// that would have needed to live in the manifest.
	var finalPrompt string
	if jsonPromptSkill(skill) {
		finalPrompt = args.Prompt
	} else {
		finalPrompt = manifest.Encode() + "\n\n" + args.Prompt
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}
	if err := s.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              s.Name(),
		Response:          resp,
		TargetID:          targetID,
		TargetDisplayName: targetName,
		Skill:             skill,
		Wait:              false,
		TimeoutSec:        timeout,
	}); err != nil {
		return nil, err
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
	for _, writeID := range observerWriteIDs {
		if err := s.t.observerRelay().UpdateWriteTask(ctx, writeID, resp.TaskID); err != nil {
			return nil, &MCPToolError{Message: "observer update write task: " + err.Error()}
		}
	}
	s.t.reg.TrackTask(resp.TaskID, writeTokens)

	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"session_id":          resp.SessionID,
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
	// Prefer observer-recorded FinalOutput because it carries the dispatch's
	// wrapped marker verbatim; agentserver's TaskInfo.Output may be just the
	// assistant text streamed before a pause, which doesn't carry the marker.
	progress := g.t.observerProgress(ctx, taskID)
	var isAwaiting bool
	var unwrappedOutput string
	var question json.RawMessage
	if a, s, q := unwrapKindMarker(progress.FinalOutput); a || s != "" {
		isAwaiting, unwrappedOutput, question = a, s, q
	} else {
		isAwaiting, unwrappedOutput, question = unwrapResultMarker(info)
	}
	markerSessionID := sessionIDFromMarker(progress.FinalOutput, info.Output, string(info.Result))
	if isAwaiting {
		g.t.emit(observer.Event{
			Type:   observer.EventDriverTaskStatus,
			TaskID: taskID,
			Status: "awaiting_user",
		})
		return json.Marshal(struct {
			Status        string          `json:"status"`
			IsFinal       bool            `json:"is_final"`
			SessionID     string          `json:"session_id"`
			CurrentTaskID string          `json:"current_task_id"`
			TargetID      string          `json:"target_id"`
			Question      json.RawMessage `json:"question"`
		}{
			Status:        "awaiting_user",
			IsFinal:       false,
			SessionID:     firstNonEmpty(info.SessionID, markerSessionID),
			CurrentTaskID: taskID,
			TargetID:      info.TargetID,
			Question:      question,
		})
	}
	g.t.emit(observer.Event{
		Type:   observer.EventDriverTaskStatus,
		TaskID: taskID,
		Status: info.Status,
	})
	output := unwrappedOutput
	finalOutput := progress.FinalOutput
	if finalOutput == "" && isTerminalStatus(info.Status) {
		finalOutput = output
	}
	return json.Marshal(struct {
		Status              string `json:"status"`
		Output              string `json:"output"`
		FailureReason       string `json:"failure_reason"`
		LatestProgress      string `json:"latest_progress"`
		LatestProgressPhase string `json:"latest_progress_phase"`
		LatestProgressAt    string `json:"latest_progress_at"`
		FinalOutput         string `json:"final_output"`
		IsFinal             bool   `json:"is_final"`
	}{
		Status:              info.Status,
		Output:              output,
		FailureReason:       info.FailureReason,
		LatestProgress:      progress.LatestProgress,
		LatestProgressPhase: progress.LatestProgressPhase,
		LatestProgressAt:    progress.LatestProgressAt,
		FinalOutput:         finalOutput,
		IsFinal:             progress.IsFinal || isTerminalStatus(info.Status),
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
			// Prefer observer-recorded FinalOutput because it carries the
			// dispatch's wrapped marker verbatim; agentserver's TaskInfo.Output
			// may be just the assistant text streamed before a pause.
			progress := w.t.observerProgress(ctx, taskID)
			var isAwaiting bool
			var unwrappedOutput string
			var question json.RawMessage
			if a, s, q := unwrapKindMarker(progress.FinalOutput); a || s != "" {
				isAwaiting, unwrappedOutput, question = a, s, q
			} else {
				isAwaiting, unwrappedOutput, question = unwrapResultMarker(info)
			}
			if isAwaiting && info.Status == "completed" {
				w.t.emit(observer.Event{
					Type:   observer.EventDriverTaskStatus,
					TaskID: taskID,
					Status: "awaiting_user",
				})
				return json.Marshal(struct {
					Status        string          `json:"status"`
					IsFinal       bool            `json:"is_final"`
					SessionID     string          `json:"session_id"`
					CurrentTaskID string          `json:"current_task_id"`
					TargetID      string          `json:"target_id"`
					Question      json.RawMessage `json:"question"`
				}{
					Status:        "awaiting_user",
					IsFinal:       false,
					SessionID:     firstNonEmpty(info.SessionID, sessionIDFromMarker(info.Output, string(info.Result), progress.FinalOutput)),
					CurrentTaskID: taskID,
					TargetID:      info.TargetID,
					Question:      question,
				})
			}
			w.t.emit(observer.Event{
				Type:   observer.EventDriverTaskStatus,
				TaskID: taskID,
				Status: info.Status,
			})
			if w.t.useObserverRelay() {
				if _, err := w.t.observerRelay().SyncWrites(ctx, taskID, w.t.cfg.DriverDefaults.DisableUIDCheck, w.t.reg); err != nil {
					return nil, &MCPToolError{Message: "observer sync writes: " + err.Error()}
				}
			}
			written := w.t.reg.WrittenFiles(args.TaskID)
			w.t.reg.ForgetTask(args.TaskID)
			output := unwrappedOutput
			return json.Marshal(struct {
				Status              string        `json:"status"`
				Output              string        `json:"output"`
				FailureReason       string        `json:"failure_reason"`
				LatestProgress      string        `json:"latest_progress"`
				LatestProgressPhase string        `json:"latest_progress_phase"`
				LatestProgressAt    string        `json:"latest_progress_at"`
				FinalOutput         string        `json:"final_output"`
				IsFinal             bool          `json:"is_final"`
				WrittenFiles        []WrittenFile `json:"written_files"`
			}{
				Status:              info.Status,
				Output:              output,
				FailureReason:       info.FailureReason,
				LatestProgress:      progress.LatestProgress,
				LatestProgressPhase: progress.LatestProgressPhase,
				LatestProgressAt:    progress.LatestProgressAt,
				FinalOutput:         firstNonEmpty(progress.FinalOutput, output),
				IsFinal:             true,
				WrittenFiles:        written,
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

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// sessionIDFromMarker extracts a session_id from the first candidate string
// that parses as a kind marker with a non-empty session_id. Callers pass
// info.Output, info.Result, progress.FinalOutput (in that priority order).
func sessionIDFromMarker(candidates ...string) string {
	for _, s := range candidates {
		if s == "" {
			continue
		}
		var kw struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(s), &kw); err == nil && kw.SessionID != "" {
			return kw.SessionID
		}
	}
	return ""
}

// unwrapKindMarker parses a chat-skill {kind:...} marker out of a string.
// Returns (isAwaiting, finalSummary, questionRaw) where:
//   - isAwaiting=true when the marker is {"kind":"awaiting_user", ...}
//   - finalSummary is the inner summary when {"kind":"final", "summary":"..."}
//   - returns (false, "", nil) when the string is not a recognised marker
//     (legacy non-chat skills, or empty)
func unwrapKindMarker(s string) (isAwaiting bool, finalSummary string, question json.RawMessage) {
	if s == "" {
		return false, "", nil
	}
	var kw struct {
		Kind     string          `json:"kind"`
		Summary  string          `json:"summary"`
		Question json.RawMessage `json:"question"`
	}
	if err := json.Unmarshal([]byte(s), &kw); err != nil {
		return false, "", nil
	}
	switch kw.Kind {
	case "awaiting_user":
		return true, "", kw.Question
	case "final":
		return false, kw.Summary, nil
	default:
		return false, "", nil
	}
}

// unwrapResultMarker checks TaskInfo for a chat-skill kind marker. It tries
// info.Output first (where the slave writes via task.Complete), then
// info.Result (legacy). Caller is responsible for also trying observer's
// progress.FinalOutput when neither field contains the marker — see
// wait_task / get_task.
func unwrapResultMarker(info *agentsdk.TaskInfo) (isAwaiting bool, output string, question json.RawMessage) {
	if info == nil {
		return false, "", nil
	}
	if info.Output != "" {
		if a, s, q := unwrapKindMarker(info.Output); a || s != "" {
			return a, s, q
		}
	}
	if len(info.Result) > 0 {
		if a, s, q := unwrapKindMarker(string(info.Result)); a || s != "" {
			return a, s, q
		}
	}
	return false, sdkTaskOutput(info), nil
}

func sdkTaskOutput(info *agentsdk.TaskInfo) string {
	if info == nil {
		return ""
	}
	if info.Output != "" {
		return info.Output
	}
	if len(info.Result) == 0 {
		return ""
	}
	var obj struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(info.Result, &obj); err == nil && obj.Output != "" {
		return obj.Output
	}
	var raw string
	if err := json.Unmarshal(info.Result, &raw); err == nil {
		return raw
	}
	return ""
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

// =========================================================================
// resume_task
// =========================================================================

type resumeTaskTool struct{ t *Tools }

func (r *resumeTaskTool) Name() string { return "resume_task" }
func (r *resumeTaskTool) Description() string {
	return "Resume a paused chat: pass the last_task_id (from wait_task's awaiting_user) and the user's answer. Returns the next wait_task-shaped result (completed, or another awaiting_user for multi-round questions, or failed)."
}
func (r *resumeTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "last_task_id":{"type":"string"},
        "answer":{"type":"string"},
        "timeout_sec":{"type":"integer"}
    },"required":["last_task_id","answer"]}`)
}
func (r *resumeTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		LastTaskID string `json:"last_task_id"`
		Answer     string `json:"answer"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.LastTaskID == "" || args.Answer == "" {
		return nil, &MCPToolError{Message: "last_task_id and answer are required"}
	}
	info, err := r.t.sdk.GetTask(ctx, args.LastTaskID, true)
	if err != nil {
		return nil, &MCPToolError{Message: "get_task: " + err.Error()}
	}
	// Validate: status == completed AND kind == awaiting_user. The marker
	// can live in info.Output (string), info.Result (legacy json.RawMessage),
	// or — when the slave reports via observer-only path — in observer's
	// recorded final output. Try all three.
	var kw struct {
		Kind     string `json:"kind"`
		Question struct {
			Kind string `json:"kind"`
		} `json:"question"`
		SessionID string `json:"session_id"`
	}
	for _, candidate := range []string{info.Output, string(info.Result)} {
		if candidate == "" {
			continue
		}
		if err := json.Unmarshal([]byte(candidate), &kw); err == nil && kw.Kind != "" {
			break
		}
	}
	if kw.Kind != "awaiting_user" {
		// Fall back to observer-recorded final output.
		prog := r.t.observerProgress(ctx, args.LastTaskID)
		if prog.FinalOutput != "" {
			_ = json.Unmarshal([]byte(prog.FinalOutput), &kw)
		}
	}
	if info.Status != "completed" || kw.Kind != "awaiting_user" {
		return nil, &MCPToolError{Message: fmt.Sprintf(
			"not awaiting_user; status=%s, kind=%s", info.Status, kw.Kind)}
	}
	sessionID := firstNonEmpty(info.SessionID, kw.SessionID)
	if sessionID == "" {
		return nil, &MCPToolError{Message: "missing session_id; cannot resume"}
	}

	body, _ := json.Marshal(map[string]string{
		"session_id": sessionID,
		"answer":     args.Answer,
		"kind":       kw.Question.Kind,
	})
	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = r.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	if timeout == 0 {
		timeout = 600
	}

	resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       info.TargetID,
		Skill:          "chat_resume",
		Prompt:         string(body),
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate chat_resume: " + err.Error()}
	}
	if err := r.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:       r.Name(),
		Response:   resp,
		TargetID:   info.TargetID,
		Skill:      "chat_resume",
		Wait:       true,
		TimeoutSec: timeout,
	}); err != nil {
		return nil, err
	}

	return r.t.waitDelegatedTask(ctx, resp.TaskID, timeout)
}

// toTokenSource adapts an ObserverSink (the 1-method Emit interface) into
// the broader TokenSource the relay needs. The concrete *observerclient.Client
// satisfies both; consumers like tests may pass a sink that does not — in
// which case the relay degrades to nil (no relay calls).
func toTokenSource(s ObserverSink) TokenSource {
	if s == nil {
		return nil
	}
	if ts, ok := s.(TokenSource); ok {
		return ts
	}
	return nil
}
