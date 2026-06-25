package driver

import (
	"context"
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type unregisterSlaveMCPTool struct{ t *Tools }

func (u *unregisterSlaveMCPTool) Name() string { return "unregister_slave_mcp" }
func (u *unregisterSlaveMCPTool) Description() string {
	return "Unregister a dynamic MCP server on a slave via its unregister_mcp skill. Removes the entry from dynamic_mcp.yaml, kills its stdio subprocess, and republishes the slave card."
}
func (u *unregisterSlaveMCPTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "name":{"type":"string"},
        "if_present":{"type":"boolean"},
        "timeout_sec":{"type":"integer"}
    },"required":["name"],"additionalProperties":false}`)
}

func (u *unregisterSlaveMCPTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
		Name              string `json:"name"`
		IfPresent         bool   `json:"if_present,omitempty"`
		TimeoutSec        int    `json:"timeout_sec,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Name == "" {
		return nil, &MCPToolError{Message: "name is required"}
	}
	card, err := u.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "unregister_mcp") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise unregister_mcp"}
	}
	prompt, err := json.Marshal(struct {
		Name      string `json:"name"`
		IfPresent bool   `json:"if_present"`
	}{Name: args.Name, IfPresent: args.IfPresent})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	resp, err := u.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          "unregister_mcp",
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate unregister_mcp task: " + err.Error()}
	}
	// DelegateTask succeeded — degrade journal append failure to a log entry
	// so we still wait on the slave task. See §1.1 #1 of the 2026-06-13 review.
	var sessRef agentbackend.SessionRef
	if resp.SessionID != "" {
		sessRef = agentbackend.NewBridgeOnly("", cardShortID(card), resp.SessionID)
	}
	if err := u.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              u.Name(),
		Response:          resp,
		TargetID:          card.AgentID,
		TargetDisplayName: card.DisplayName,
		Skill:             "unregister_mcp",
		Wait:              true,
		TimeoutSec:        args.TimeoutSec,
		SessionRef:        sessRef,
	}); err != nil {
		u.t.logHelperErr("driver_journal", "record_delegated_task", err)
	}
	return u.t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
}
