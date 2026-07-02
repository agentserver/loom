package driver

import (
	"context"
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type unregisterSlaveMCPTool struct{ t *Tools }

func (u *unregisterSlaveMCPTool) Name() string { return "unregister_slave_mcp" }
func (u *unregisterSlaveMCPTool) Description() string {
	return "Unregister a dynamic MCP server on a slave via its unregister_mcp skill. Removes the entry from dynamic_mcp.yaml, kills its stdio subprocess, and republishes the slave card. Requires the four promotion-audit fields (see docs/specs/wt2-driver-promotion-chain-B6.spec.md)."
}
func (u *unregisterSlaveMCPTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "name":{"type":"string"},
        "if_present":{"type":"boolean"},
        "timeout_sec":{"type":"integer"},
        "promoted_by_user_id":{"type":"string"},
        "driver_thread_id":{"type":"string"},
        "promotion_reason":{"type":"string","enum":["explicit_user_request","driver_agent_inferred","batch_import","ci_seed"]},
        "candidate_source_task_id":{"type":"string"}
    },"required":["name","promoted_by_user_id","driver_thread_id","promotion_reason","candidate_source_task_id"],"additionalProperties":false}`)
}

type unregisterMCPArgs struct {
	TargetAgentID         string `json:"target_agent_id"`
	TargetDisplayName     string `json:"target_display_name"`
	Name                  string `json:"name"`
	IfPresent             bool   `json:"if_present,omitempty"`
	TimeoutSec            int    `json:"timeout_sec,omitempty"`
	PromotedByUserID      string `json:"promoted_by_user_id"`
	DriverThreadID        string `json:"driver_thread_id"`
	PromotionReason       string `json:"promotion_reason"`
	CandidateSourceTaskID string `json:"candidate_source_task_id"`
}

func (u *unregisterSlaveMCPTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args unregisterMCPArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	if args.Name == "" {
		return nil, &MCPToolError{Message: "name is required", Category: observerstore.FailContractViolation}
	}
	reason, err := promotionaudit.Parse(args.PromotionReason)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
	}
	auditPrecheck := promotionaudit.AuditFields{
		WorkspaceID:           u.t.workspaceID(),
		MCPName:               args.Name,
		Action:                promotionaudit.ActionUnregister,
		PromotedByUserID:      args.PromotedByUserID,
		DriverThreadID:        args.DriverThreadID,
		PromotionReason:       reason,
		CandidateSourceTaskID: args.CandidateSourceTaskID,
		RegistryHashAfter:     emptyBytesSHA256Hex,
	}
	if err := promotionaudit.Validate(auditPrecheck); err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
	}
	card, err := u.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "unregister_mcp") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise unregister_mcp", Category: observerstore.FailStaleCapability}
	}
	prompt, err := json.Marshal(struct {
		Name      string `json:"name"`
		IfPresent bool   `json:"if_present"`
	}{Name: args.Name, IfPresent: args.IfPresent})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	resp, err := u.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          "unregister_mcp",
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate unregister_mcp task: " + err.Error(), Category: observerstore.FailUnknown}
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
	waitResult, waitErr := u.t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
	if waitErr != nil {
		return waitResult, waitErr
	}
	hash := PublishUnregisterAndCompute(card.AgentID, args.Name)
	auditFinal := auditPrecheck
	auditFinal.RegistryHashAfter = hash
	if u.t.promoAudit != nil {
		if werr := u.t.promoAudit.Write(ctx, auditFinal); werr != nil {
			u.t.logHelperErr("promotion_audit", "write", werr)
		}
	} else {
		u.t.logHelperErr("promotion_audit", "write", errNoPromoAuditSink)
	}
	return waitResult, nil
}
