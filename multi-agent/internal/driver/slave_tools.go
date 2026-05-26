package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

type runSlaveBashTool struct{ t *Tools }

func (r *runSlaveBashTool) Name() string { return "run_slave_bash" }
func (r *runSlaveBashTool) Description() string {
	return "Run an explicit Bash script on a selected slave that advertises the bash skill."
}
func (r *runSlaveBashTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "script":{"type":"string"},
        "env":{"type":"object","additionalProperties":{"type":"string"}},
        "timeout_sec":{"type":"integer"},
        "wait":{"type":"boolean"}
    },"required":["script"],"additionalProperties":false}`)
}
func (r *runSlaveBashTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string            `json:"target_agent_id"`
		TargetDisplayName string            `json:"target_display_name"`
		Script            string            `json:"script"`
		Env               map[string]string `json:"env,omitempty"`
		TimeoutSec        int               `json:"timeout_sec,omitempty"`
		Wait              *bool             `json:"wait,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Script == "" {
		return nil, &MCPToolError{Message: "script is required"}
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "bash") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise bash"}
	}
	prompt, err := json.Marshal(struct {
		Script     string            `json:"script"`
		TimeoutSec int               `json:"timeout_sec,omitempty"`
		Env        map[string]string `json:"env,omitempty"`
	}{Script: args.Script, TimeoutSec: args.TimeoutSec, Env: args.Env})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          "bash",
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate bash task: " + err.Error()}
	}
	wait := true
	if args.Wait != nil {
		wait = *args.Wait
	}
	if !wait {
		return json.Marshal(map[string]interface{}{
			"task_id":             resp.TaskID,
			"target_id":           card.AgentID,
			"target_display_name": card.DisplayName,
			"skill":               "bash",
			"status":              resp.Status,
		})
	}
	return r.t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
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
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	return g.t.delegatePermissionTask(ctx, args.TargetAgentID, args.TargetDisplayName, `{"op":"get"}`)
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
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	prompt, err := json.Marshal(struct {
		Op           string   `json:"op"`
		AllowPresets []string `json:"allow_presets,omitempty"`
		AllowAdd     []string `json:"allow_add,omitempty"`
		AllowRemove  []string `json:"allow_remove,omitempty"`
		DenyAdd      []string `json:"deny_add,omitempty"`
		DenyRemove   []string `json:"deny_remove,omitempty"`
	}{
		Op:           "patch",
		AllowPresets: args.AllowPresets,
		AllowAdd:     args.AllowAdd,
		AllowRemove:  args.AllowRemove,
		DenyAdd:      args.DenyAdd,
		DenyRemove:   args.DenyRemove,
	})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return u.t.delegatePermissionTask(ctx, args.TargetAgentID, args.TargetDisplayName, string(prompt))
}

func (t *Tools) delegatePermissionTask(ctx context.Context, targetAgentID, targetDisplayName, prompt string) (json.RawMessage, error) {
	card, err := t.resolveAvailableAgent(ctx, targetAgentID, targetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "claude_permissions") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise claude_permissions"}
	}
	resp, err := t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID,
		Skill:    "claude_permissions",
		Prompt:   prompt,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate claude_permissions task: " + err.Error()}
	}
	return t.waitDelegatedTask(ctx, resp.TaskID, 0)
}

func (t *Tools) resolveAvailableAgent(ctx context.Context, targetAgentID, targetDisplayName string) (agentsdk.AgentCard, error) {
	if targetAgentID == "" && targetDisplayName == "" {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "target_agent_id or target_display_name is required"}
	}
	cards, err := t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "discover agents: " + err.Error()}
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
		return agentsdk.AgentCard{}, &MCPToolError{Message: "agent " + target + " is not available"}
	}
	if len(matches) == 0 {
		return agentsdk.AgentCard{}, &MCPToolError{Message: "no agent found: " + target}
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match.DisplayName)
	}
	return agentsdk.AgentCard{}, &MCPToolError{Message: "ambiguous target: " + fmt.Sprint(names)}
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
			return nil, &MCPToolError{Message: "get task " + taskID + ": " + err.Error()}
		}
		if isTerminalStatus(info.Status) {
			isAwaiting, unwrappedOutput, question := unwrapResultMarker(info)
			if isAwaiting && info.Status == "completed" {
				return marshalDelegatedAwaitingUser(taskID, info, question)
			}
			output := unwrappedOutput
			if info.Status != "completed" {
				return nil, &MCPToolError{Message: "task " + taskID + " " + info.Status + ": " + firstNonEmpty(info.FailureReason, output)}
			}
			return marshalDelegatedTaskOutput(taskID, info, output)
		}
		if time.Now().After(deadline) {
			return nil, &MCPToolError{Message: "task " + taskID + " timeout after " + fmt.Sprintf("%d", timeoutSec) + "s; status=" + info.Status}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func marshalDelegatedAwaitingUser(taskID string, info *agentsdk.TaskInfo, question json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(struct {
		TaskID        string          `json:"task_id"`
		Status        string          `json:"status"`
		IsFinal       bool            `json:"is_final"`
		SessionID     string          `json:"session_id"`
		CurrentTaskID string          `json:"current_task_id"`
		TargetID      string          `json:"target_id"`
		Question      json.RawMessage `json:"question"`
	}{
		TaskID:        firstNonEmpty(info.TaskID, taskID),
		Status:        "awaiting_user",
		IsFinal:       false,
		SessionID:     info.SessionID,
		CurrentTaskID: firstNonEmpty(info.TaskID, taskID),
		TargetID:      info.TargetID,
		Question:      question,
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
