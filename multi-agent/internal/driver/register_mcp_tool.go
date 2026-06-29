package driver

import (
	"context"
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type registerSlaveMCPTool struct{ t *Tools }

func (r *registerSlaveMCPTool) Name() string { return "register_slave_mcp" }
func (r *registerSlaveMCPTool) Description() string {
	return "Register a pre-built MCP server file on a slave via its register_mcp skill. Use after a bash task has written the source and validated it locally."
}
func (r *registerSlaveMCPTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "spec":{"type":"object"},
        "source_path":{"type":"string"},
        "timeout_sec":{"type":"integer"}
    },"required":["spec","source_path"],"additionalProperties":false}`)
}

func (r *registerSlaveMCPTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string         `json:"target_agent_id"`
		TargetDisplayName string         `json:"target_display_name"`
		Spec              buildspec.Spec `json:"spec"`
		SourcePath        string         `json:"source_path"`
		TimeoutSec        int            `json:"timeout_sec,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	if args.SourcePath == "" {
		return nil, &MCPToolError{Message: "source_path is required", Category: observerstore.FailContractViolation}
	}
	spec := buildspec.Normalize(args.Spec)
	if err := buildspec.Validate(spec); err != nil {
		return nil, &MCPToolError{Message: "invalid spec: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "register_mcp") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise register_mcp", Category: observerstore.FailStaleCapability}
	}
	prompt, err := json.Marshal(struct {
		Spec       buildspec.Spec `json:"spec"`
		SourcePath string         `json:"source_path"`
	}{Spec: spec, SourcePath: args.SourcePath})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          "register_mcp",
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate register_mcp task: " + err.Error(), Category: observerstore.FailUnknown}
	}
	// DelegateTask succeeded — degrade journal append failure to a log entry
	// so we still wait on the slave task. See §1.1 #1 of the 2026-06-13 review.
	var sessRef agentbackend.SessionRef
	if resp.SessionID != "" {
		sessRef = agentbackend.NewBridgeOnly("", cardShortID(card), resp.SessionID)
	}
	if err := r.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              r.Name(),
		Response:          resp,
		TargetID:          card.AgentID,
		TargetDisplayName: card.DisplayName,
		Skill:             "register_mcp",
		Wait:              true,
		TimeoutSec:        args.TimeoutSec,
		SessionRef:        sessRef,
	}); err != nil {
		r.t.logHelperErr("driver_journal", "record_delegated_task", err)
	}
	return r.t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
}
