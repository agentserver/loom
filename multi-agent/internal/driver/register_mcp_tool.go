package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type registerSlaveMCPTool struct{ t *Tools }

func (r *registerSlaveMCPTool) Name() string { return "register_slave_mcp" }
func (r *registerSlaveMCPTool) Description() string {
	return "Register a pre-built MCP server file on a slave via its register_mcp skill. Use after a bash task has written the source and validated it locally. Requires the four promotion-audit fields (promoted_by_user_id / driver_thread_id / promotion_reason / candidate_source_task_id) that WT-2 B6 introduced — see docs/specs/wt2-driver-promotion-chain-B6.spec.md."
}
func (r *registerSlaveMCPTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "spec":{"type":"object"},
        "source_path":{"type":"string"},
        "timeout_sec":{"type":"integer"},
        "promoted_by_user_id":{"type":"string"},
        "driver_thread_id":{"type":"string"},
        "promotion_reason":{"type":"string","enum":["explicit_user_request","driver_agent_inferred","batch_import","ci_seed"]},
        "candidate_source_task_id":{"type":"string"}
    },"required":["spec","source_path","promoted_by_user_id","driver_thread_id","promotion_reason","candidate_source_task_id"],"additionalProperties":false}`)
}

type registerMCPArgs struct {
	TargetAgentID         string         `json:"target_agent_id"`
	TargetDisplayName     string         `json:"target_display_name"`
	Spec                  buildspec.Spec `json:"spec"`
	SourcePath            string         `json:"source_path"`
	TimeoutSec            int            `json:"timeout_sec,omitempty"`
	PromotedByUserID      string         `json:"promoted_by_user_id"`
	DriverThreadID        string         `json:"driver_thread_id"`
	PromotionReason       string         `json:"promotion_reason"`
	CandidateSourceTaskID string         `json:"candidate_source_task_id"`
}

func (r *registerSlaveMCPTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args registerMCPArgs
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
	// Build the audit fields BEFORE the delegate task so a malformed
	// audit input aborts without any slave-side side-effects (§7 (a)).
	// We don't have WorkspaceID / RegistryHashAfter yet — we set the
	// hash after the slave completes and workspace is filled from cfg.
	reason, err := promotionaudit.Parse(args.PromotionReason)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
	}
	auditPrecheck := promotionaudit.AuditFields{
		WorkspaceID:           r.t.workspaceID(),
		MCPName:               spec.Name,
		Action:                promotionaudit.ActionRegister,
		PromotedByUserID:      args.PromotedByUserID,
		DriverThreadID:        args.DriverThreadID,
		PromotionReason:       reason,
		CandidateSourceTaskID: args.CandidateSourceTaskID,
		// RegistryHashAfter is set post-delegate; use the empty-bytes
		// sha256 hex placeholder so the precheck Validate passes (any
		// 64-hex is accepted).
		RegistryHashAfter: emptyBytesSHA256Hex,
	}
	if err := promotionaudit.Validate(auditPrecheck); err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
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
	waitResult, waitErr := r.t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
	if waitErr != nil {
		return waitResult, waitErr
	}
	// Slave task succeeded → update per-slave view + compute new hash
	// + write audit row (spec §3.4). Failure to write audit degrades
	// to a helper-error log; the register itself is still successful.
	specHash := computeSpecHash(spec)
	hash := PublishRegisterAndCompute(card.AgentID, spec.Name, specHash)
	auditFinal := auditPrecheck
	auditFinal.RegistryHashAfter = hash
	if r.t.promoAudit != nil {
		if werr := r.t.promoAudit.Write(ctx, auditFinal); werr != nil {
			r.t.logHelperErr("promotion_audit", "write", werr)
		}
	} else {
		r.t.logHelperErr("promotion_audit", "write", errNoPromoAuditSink)
	}
	return waitResult, nil
}

// computeSpecHash returns the sha256 hex of the canonical JSON of spec.
// Returns "" on marshal error — the caller uses the return as
// descHash(name) for ComputeRegistryHash, and an empty descHash still
// produces a well-formed 64-hex registry hash (just carries less
// content information for that entry).
func computeSpecHash(spec buildspec.Spec) string {
	canon, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:])
}

// emptyBytesSHA256Hex is a compile-time constant so the register-tool
// precheck can pre-fill RegistryHashAfter without importing the driver
// package back into itself.
const emptyBytesSHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// errNoPromoAuditSink is the sentinel logged when the driver was
// constructed without a promoAudit writer. Kept as a package variable
// so tests can errors.Is against it if a downstream helper needs to
// branch on the shape.
var errNoPromoAuditSink = &missingSinkError{name: "promotion_audit"}

type missingSinkError struct{ name string }

func (e *missingSinkError) Error() string { return e.name + " sink not wired on Tools" }
