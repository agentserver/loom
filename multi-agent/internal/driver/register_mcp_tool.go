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

// registerCoreArgs is the input to registerCore, the shared logic
// invoked by both the tool-boundary (registerSlaveMCPTool.Call) and
// the B2 pipeline (Stage 3). It carries a pre-normalised, pre-validated
// spec + audit fields — the caller has already run promotionaudit.Validate.
type registerCoreArgs struct {
	TargetAgentID     string
	TargetDisplayName string
	Spec              buildspec.Spec // MUST be already-normalised
	SourcePath        string
	TimeoutSec        int
}

// registerCoreResult carries what the caller needs to write an audit
// row and construct the tool response. `RegistryHash` is the post-
// register value published to `driver.LastRegistryHash()`.
type registerCoreResult struct {
	WaitResult   json.RawMessage
	RegistryHash string
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
		RegistryHashAfter:     emptyBytesSHA256Hex,
	}
	if err := promotionaudit.Validate(auditPrecheck); err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
	}
	// Invoke the shared core; it does NOT write an audit row.
	coreArgs := registerCoreArgs{
		TargetAgentID:     args.TargetAgentID,
		TargetDisplayName: args.TargetDisplayName,
		Spec:              spec,
		SourcePath:        args.SourcePath,
		TimeoutSec:        args.TimeoutSec,
	}
	result, waitErr := r.t.registerCore(ctx, coreArgs, r.Name())
	if waitErr != nil {
		return result.WaitResult, waitErr
	}
	// Slave task succeeded → write the tool-boundary audit row.
	auditFinal := auditPrecheck
	auditFinal.RegistryHashAfter = result.RegistryHash
	if r.t.promoAudit != nil {
		if werr := r.t.promoAudit.Write(ctx, auditFinal); werr != nil {
			r.t.logHelperErr("promotion_audit", "write", werr)
		}
	} else {
		r.t.logHelperErr("promotion_audit", "write", errNoPromoAuditSink)
	}
	return result.WaitResult, nil
}

// registerCore is the shared register logic invoked by BOTH the
// tool-boundary Call above AND the B2 promotion pipeline (stage 3).
// It does NOT write an audit row — that is the caller's job so B2 can
// wrap it in the per-stage audit row shape without double-audit.
//
// callerToolName is used ONLY for the task-journal entry and for
// helper-error logs.
func (t *Tools) registerCore(ctx context.Context, args registerCoreArgs, callerToolName string) (registerCoreResult, error) {
	card, err := t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return registerCoreResult{}, err
	}
	if !hasSkill(card, "register_mcp") {
		return registerCoreResult{}, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise register_mcp", Category: observerstore.FailStaleCapability}
	}
	prompt, err := json.Marshal(struct {
		Spec       buildspec.Spec `json:"spec"`
		SourcePath string         `json:"source_path"`
	}{Spec: args.Spec, SourcePath: args.SourcePath})
	if err != nil {
		return registerCoreResult{}, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	resp, err := t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          "register_mcp",
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return registerCoreResult{}, &MCPToolError{Message: "delegate register_mcp task: " + err.Error(), Category: observerstore.FailUnknown}
	}
	var sessRef agentbackend.SessionRef
	if resp.SessionID != "" {
		sessRef = agentbackend.NewBridgeOnly("", cardShortID(card), resp.SessionID)
	}
	if err := t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              callerToolName,
		Response:          resp,
		TargetID:          card.AgentID,
		TargetDisplayName: card.DisplayName,
		Skill:             "register_mcp",
		Wait:              true,
		TimeoutSec:        args.TimeoutSec,
		SessionRef:        sessRef,
	}); err != nil {
		t.logHelperErr("driver_journal", "record_delegated_task", err)
	}
	waitResult, waitErr := t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
	if waitErr != nil {
		return registerCoreResult{WaitResult: waitResult}, waitErr
	}
	specHash := computeSpecHash(args.Spec)
	hash := PublishRegisterAndCompute(card.AgentID, args.Spec.Name, specHash)
	return registerCoreResult{WaitResult: waitResult, RegistryHash: hash}, nil
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
