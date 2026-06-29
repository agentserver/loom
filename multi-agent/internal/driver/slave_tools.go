package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type runSlaveBashTool struct{ t *Tools }

func (r *runSlaveBashTool) Name() string { return "run_slave_bash" }
func (r *runSlaveBashTool) Description() string {
	return "Run an explicit Bash script on a selected slave that advertises a Bash command interface. Returns task_id immediately by default; pass wait:true to block for completion."
}
func (r *runSlaveBashTool) InputSchema() json.RawMessage {
	return shellToolInputSchema()
}
func (r *runSlaveBashTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	args, err := parseShellToolArgs(raw)
	if err != nil {
		return nil, err
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	parsed := parseAgentCard(card)
	if !parsed.SupportsExplicitShell("bash") {
		if parsed.SupportsExplicitShell("powershell") {
			return nil, &MCPToolError{Message: "target " + card.DisplayName + " has no Bash command interface; use run_slave_powershell", Category: observerstore.FailStaleCapability}
		}
		if parsed.HasSkill("bash") {
			return nil, &MCPToolError{Message: "target " + card.DisplayName + " has no Bash command interface", Category: observerstore.FailStaleCapability}
		}
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise bash", Category: observerstore.FailStaleCapability}
	}
	return r.t.delegateShellTask(ctx, card, r.Name(), "bash", args)
}

type runSlavePowerShellTool struct{ t *Tools }

func (r *runSlavePowerShellTool) Name() string { return "run_slave_powershell" }
func (r *runSlavePowerShellTool) Description() string {
	return "Run an explicit PowerShell script on a selected slave that advertises a PowerShell command interface. Returns task_id immediately by default; pass wait:true to block for completion."
}
func (r *runSlavePowerShellTool) InputSchema() json.RawMessage {
	return shellToolInputSchema()
}
func (r *runSlavePowerShellTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	args, err := parseShellToolArgs(raw)
	if err != nil {
		return nil, err
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	parsed := parseAgentCard(card)
	if !parsed.SupportsExplicitShell("powershell") {
		if parsed.HasSkill("powershell") {
			return nil, &MCPToolError{Message: "target " + card.DisplayName + " has no PowerShell command interface", Category: observerstore.FailStaleCapability}
		}
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise powershell", Category: observerstore.FailStaleCapability}
	}
	return r.t.delegateShellTask(ctx, card, r.Name(), "powershell", args)
}

type runSlaveShellTool struct{ t *Tools }

func (r *runSlaveShellTool) Name() string { return "run_slave_shell" }
func (r *runSlaveShellTool) Description() string {
	return "Run a script on a selected slave using its default advertised shell command interface. Returns task_id immediately by default; pass wait:true to block for completion."
}
func (r *runSlaveShellTool) InputSchema() json.RawMessage {
	return shellToolInputSchema()
}
func (r *runSlaveShellTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	args, err := parseShellToolArgs(raw)
	if err != nil {
		return nil, err
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	parsed := parseAgentCard(card)
	if commandInterface := parsed.DefaultCommandInterface(); commandInterface.Kind != "" {
		switch commandInterface.Kind {
		case "bash", "powershell":
			if !parsed.SupportsExplicitShell(commandInterface.Kind) {
				return nil, &MCPToolError{Message: "target " + card.DisplayName + " default " + commandInterface.Kind + " command interface is not supported by advertised skills", Category: observerstore.FailStaleCapability}
			}
			return r.t.delegateShellTask(ctx, card, r.Name(), commandInterface.Kind, args)
		default:
			return nil, &MCPToolError{Message: "target " + card.DisplayName + " default command interface kind " + commandInterface.Kind + " is unsupported; expected bash or powershell", Category: observerstore.FailStaleCapability}
		}
	}
	if len(parsed.CommandInterfaces) == 0 && parsed.HasSkill("bash") {
		return r.t.delegateShellTask(ctx, card, r.Name(), "bash", args)
	}
	if parsed.SupportsExplicitShell("powershell") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " has no default shell command interface; use run_slave_powershell", Category: observerstore.FailStaleCapability}
	}
	return nil, &MCPToolError{Message: "target " + card.DisplayName + " has no supported shell command interface", Category: observerstore.FailStaleCapability}
}

type shellToolArgs struct {
	TargetAgentID     string            `json:"target_agent_id"`
	TargetDisplayName string            `json:"target_display_name"`
	Script            string            `json:"script"`
	Env               map[string]string `json:"env,omitempty"`
	TimeoutSec        int               `json:"timeout_sec,omitempty"`
	Wait              *bool             `json:"wait,omitempty"`
}

func shellToolInputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "script":{"type":"string"},
        "env":{"type":"object","additionalProperties":{"type":"string"}},
        "timeout_sec":{"type":"integer"},
        "wait":{"type":"boolean","description":"When true, block until the delegated task completes. Defaults to false and returns task_id immediately."}
    },"required":["script"],"additionalProperties":false}`)
}

func parseShellToolArgs(raw json.RawMessage) (shellToolArgs, error) {
	var args shellToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return shellToolArgs{}, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	if args.Script == "" {
		return shellToolArgs{}, &MCPToolError{Message: "script is required", Category: observerstore.FailContractViolation}
	}
	return args, nil
}

func (t *Tools) delegateShellTask(ctx context.Context, card agentsdk.AgentCard, toolName, skill string, args shellToolArgs) (json.RawMessage, error) {
	prompt, err := json.Marshal(struct {
		Script     string            `json:"script"`
		TimeoutSec int               `json:"timeout_sec,omitempty"`
		Env        map[string]string `json:"env,omitempty"`
	}{Script: args.Script, TimeoutSec: args.TimeoutSec, Env: args.Env})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	resp, err := t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          skill,
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate " + skill + " task: " + err.Error(), Category: observerstore.FailUnknown}
	}
	wait := false
	if args.Wait != nil {
		wait = *args.Wait
	}
	// DelegateTask succeeded — degrade journal append failure to a log entry
	// so we still return task_id (wait=false) or wait (wait=true). See
	// §1.1 #1 of the 2026-06-13 review.
	var sessRef agentbackend.SessionRef
	if resp.SessionID != "" {
		sessRef = agentbackend.NewBridgeOnly("", cardShortID(card), resp.SessionID)
	}
	if err := t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              toolName,
		Response:          resp,
		TargetID:          card.AgentID,
		TargetDisplayName: card.DisplayName,
		Skill:             skill,
		Wait:              wait,
		TimeoutSec:        args.TimeoutSec,
		SessionRef:        sessRef,
	}); err != nil {
		t.logHelperErr("driver_journal", "record_delegated_task", err)
	}
	if !wait {
		return json.Marshal(map[string]interface{}{
			"task_id":             resp.TaskID,
			"target_id":           card.AgentID,
			"target_display_name": card.DisplayName,
			"skill":               skill,
			"status":              resp.Status,
		})
	}
	return t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
}

type getSlaveClaudePermissionsTool struct{ t *Tools }

func (g *getSlaveClaudePermissionsTool) Name() string { return "get_slave_claude_permissions" }
func (g *getSlaveClaudePermissionsTool) Description() string {
	return "Read Claude Code permissions from a selected slave through the task-channel claude_permissions skill."
}
func (g *getSlaveClaudePermissionsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"}
    },"additionalProperties":false}`)
}
func (g *getSlaveClaudePermissionsTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	return g.t.delegatePermissionTask(ctx, g.Name(), args.TargetAgentID, args.TargetDisplayName, `{"op":"get"}`)
}

type updateSlaveClaudePermissionsTool struct{ t *Tools }

func (u *updateSlaveClaudePermissionsTool) Name() string {
	return "update_slave_claude_permissions"
}
func (u *updateSlaveClaudePermissionsTool) Description() string {
	return "Patch Claude Code permissions on a selected slave through the task-channel claude_permissions skill."
}
func (u *updateSlaveClaudePermissionsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "allow_presets":{"type":"array","items":{"type":"string"}},
        "allow_add":{"type":"array","items":{"type":"string"}},
        "allow_remove":{"type":"array","items":{"type":"string"}},
        "deny_add":{"type":"array","items":{"type":"string"}},
        "deny_remove":{"type":"array","items":{"type":"string"}}
    },"additionalProperties":false}`)
}
func (u *updateSlaveClaudePermissionsTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string   `json:"target_agent_id"`
		TargetDisplayName string   `json:"target_display_name"`
		AllowPresets      []string `json:"allow_presets,omitempty"`
		AllowAdd          []string `json:"allow_add,omitempty"`
		AllowRemove       []string `json:"allow_remove,omitempty"`
		DenyAdd           []string `json:"deny_add,omitempty"`
		DenyRemove        []string `json:"deny_remove,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	prompt, err := json.Marshal(struct {
		Op          string   `json:"op"`
		Presets     []string `json:"presets,omitempty"`
		AllowAdd    []string `json:"allow_add,omitempty"`
		AllowRemove []string `json:"allow_remove,omitempty"`
		DenyAdd     []string `json:"deny_add,omitempty"`
		DenyRemove  []string `json:"deny_remove,omitempty"`
	}{
		Op:          "patch",
		Presets:     args.AllowPresets,
		AllowAdd:    args.AllowAdd,
		AllowRemove: args.AllowRemove,
		DenyAdd:     args.DenyAdd,
		DenyRemove:  args.DenyRemove,
	})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	return u.t.delegatePermissionTask(ctx, u.Name(), args.TargetAgentID, args.TargetDisplayName, string(prompt))
}

func (t *Tools) delegatePermissionTask(ctx context.Context, toolName, targetAgentID, targetDisplayName, prompt string) (json.RawMessage, error) {
	card, err := t.resolveAvailableAgent(ctx, targetAgentID, targetDisplayName)
	if err != nil {
		return nil, err
	}
	skill := ""
	switch {
	case hasSkill(card, "permissions"):
		skill = "permissions"
	case hasSkill(card, "claude_permissions"):
		skill = "claude_permissions"
	default:
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise permissions or claude_permissions", Category: observerstore.FailStaleCapability}
	}
	resp, err := t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID,
		Skill:    skill,
		Prompt:   prompt,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate " + skill + " task: " + err.Error(), Category: observerstore.FailUnknown}
	}
	// DelegateTask succeeded — degrade journal append failure to a log entry
	// so we still wait on the permission task. See §1.1 #1 of the
	// 2026-06-13 review.
	var permSessRef agentbackend.SessionRef
	if resp.SessionID != "" {
		permSessRef = agentbackend.NewBridgeOnly("", cardShortID(card), resp.SessionID)
	}
	if err := t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              toolName,
		Response:          resp,
		TargetID:          card.AgentID,
		TargetDisplayName: card.DisplayName,
		Skill:             skill,
		Wait:              true,
		SessionRef:        permSessRef,
	}); err != nil {
		t.logHelperErr("driver_journal", "record_delegated_task", err)
	}
	return t.waitDelegatedTask(ctx, resp.TaskID, 0)
}

func (t *Tools) resolveAvailableAgent(ctx context.Context, targetAgentID, targetDisplayName string) (agentsdk.AgentCard, error) {
	if targetAgentID == "" && targetDisplayName == "" {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "target_agent_id or target_display_name is required", Category: observerstore.FailContractViolation}
	}
	cards, err := t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	var unavailable bool
	var matches []agentsdk.AgentCard
	for _, card := range cards {
		if t.cfg != nil && card.AgentID == t.cfg.Credentials.SandboxID {
			continue
		}
		if targetAgentID != "" && card.AgentID != targetAgentID {
			continue
		}
		if targetAgentID == "" && card.DisplayName != targetDisplayName {
			continue
		}
		if !agentAvailable(card) {
			unavailable = true
			continue
		}
		matches = append(matches, card)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	target := targetAgentID
	if target == "" {
		target = targetDisplayName
	}
	if unavailable {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "agent " + target + " is not available", Category: observerstore.FailSlaveDisconnect}
	}
	if len(matches) == 0 {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "no agent found: " + target, Category: observerstore.FailWrongContext}
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match.DisplayName)
	}
	return agentsdk.AgentCard{}, &MCPToolError{Message: "ambiguous target: " + fmt.Sprint(names), Category: observerstore.FailWrongContext}
}

func (t *Tools) waitDelegatedTask(ctx context.Context, taskID string, timeoutSec int) (json.RawMessage, error) {
	if timeoutSec <= 0 && t.cfg != nil {
		timeoutSec = t.cfg.DriverDefaults.TaskTimeoutSec
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		info, err := t.sdk.GetTask(ctx, taskID, true)
		if err != nil {
			return nil, &MCPToolError{Message: "get task " + taskID + ": " + err.Error(), Category: observerstore.FailUnknown}
		}
		if isTerminalStatus(info.Status) {
			isAwaiting, unwrappedOutput, question := unwrapResultMarker(info)
			if isAwaiting && info.Status == "completed" {
				return marshalDelegatedAwaitingUser(taskID, info, question)
			}
			output := unwrappedOutput
			if info.Status != "completed" {
				// Slave-side failure: info.Status is "failed" or "cancelled"
				// (per isTerminalStatus). info.FailureReason is an opaque
				// human-readable string the slave executor produced; bucketing
				// it would require parsing it. Leave FailUnknown until the
				// slave protocol returns a typed category alongside the reason.
				return nil, &MCPToolError{Message: "task " + taskID + " " + info.Status + ": " + firstNonEmpty(info.FailureReason, output), Category: observerstore.FailUnknown}
			}
			return marshalDelegatedTaskOutput(taskID, info, output)
		}
		if time.Now().After(deadline) {
			return nil, &MCPToolError{Message: "task " + taskID + " timeout after " + fmt.Sprintf("%d", timeoutSec) + "s; status=" + info.Status, Category: observerstore.FailTimeout}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func marshalDelegatedAwaitingUser(taskID string, info *agentsdk.TaskInfo, question json.RawMessage) (json.RawMessage, error) {
	// session_id is the backend-native id from the slave's kind marker (may be
	// empty when no marker was emitted). bridge_session_id is the agentserver
	// task-bridge id. Matches the wait_task / get_task response contract from
	// tools.go so resume_task → waitDelegatedTask cannot reintroduce the
	// bridge/backend confusion #29 removed.
	markerSessionID := sessionIDFromMarker(info.Output, string(info.Result))
	return json.Marshal(struct {
		TaskID          string          `json:"task_id"`
		Status          string          `json:"status"`
		IsFinal         bool            `json:"is_final"`
		SessionID       string          `json:"session_id,omitempty"`
		BridgeSessionID string          `json:"bridge_session_id,omitempty"`
		CurrentTaskID   string          `json:"current_task_id"`
		TargetID        string          `json:"target_id"`
		Question        json.RawMessage `json:"question"`
	}{
		TaskID:          firstNonEmpty(info.TaskID, taskID),
		Status:          "awaiting_user",
		IsFinal:         false,
		SessionID:       markerSessionID,
		BridgeSessionID: info.SessionID,
		CurrentTaskID:   firstNonEmpty(info.TaskID, taskID),
		TargetID:        info.TargetID,
		Question:        question,
	})
}

func marshalDelegatedTaskOutput(taskID string, info *agentsdk.TaskInfo, output string) (json.RawMessage, error) {
	result := map[string]interface{}{
		"task_id":        firstNonEmpty(info.TaskID, taskID),
		"status":         info.Status,
		"output":         output,
		"failure_reason": info.FailureReason,
	}
	if json.Valid([]byte(output)) {
		var parsed interface{}
		if err := json.Unmarshal([]byte(output), &parsed); err == nil {
			result["result"] = parsed
		}
	}
	return json.Marshal(result)
}
