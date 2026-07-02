package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/ablation"
	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
	"github.com/yourorg/multi-agent/internal/promotionpipeline"
)

// promotionPipelineTool wires the internal/promotionpipeline package
// as an MCP tool on the driver. See spec
// docs/specs/wt2-driver-promotion-chain-B2.spec.md §2.1 / §3.2.
type promotionPipelineTool struct {
	t *Tools
	// pipelineOverride is used by tests to inject a pre-built
	// Pipeline (with mocked Deps). Nil in production.
	pipelineOverride atomic.Pointer[promotionpipeline.Pipeline]
}

func (p *promotionPipelineTool) Name() string { return "promotion_pipeline" }
func (p *promotionPipelineTool) Description() string {
	return "Run the scaffold → acceptance → register pipeline as one MCP tool call, emitting per-stage audit rows and observer events. Acceptance failure hard-rejects register. --dry-run-register skips the final register step but still writes audit + events. Requires the four B6 audit fields."
}
func (p *promotionPipelineTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "spec":{"type":"object"},
        "cases_path":{"type":"string"},
        "dry_run_register":{"type":"boolean"},
        "timeout_sec":{"type":"integer"},
        "promoted_by_user_id":{"type":"string"},
        "driver_thread_id":{"type":"string"},
        "promotion_reason":{"type":"string","enum":["explicit_user_request","driver_agent_inferred","batch_import","ci_seed"]},
        "candidate_source_task_id":{"type":"string"}
    },"required":["spec","cases_path","promoted_by_user_id","driver_thread_id","promotion_reason","candidate_source_task_id"],"additionalProperties":false}`)
}

type promotionPipelineArgs struct {
	TargetAgentID         string         `json:"target_agent_id"`
	TargetDisplayName     string         `json:"target_display_name"`
	Spec                  buildspec.Spec `json:"spec"`
	CasesPath             string         `json:"cases_path"`
	DryRunRegister        bool           `json:"dry_run_register,omitempty"`
	TimeoutSec            int            `json:"timeout_sec,omitempty"`
	PromotedByUserID      string         `json:"promoted_by_user_id"`
	DriverThreadID        string         `json:"driver_thread_id"`
	PromotionReason       string         `json:"promotion_reason"`
	CandidateSourceTaskID string         `json:"candidate_source_task_id"`
}

func (pt *promotionPipelineTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args promotionPipelineArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	spec := buildspec.Normalize(args.Spec)
	if err := buildspec.Validate(spec); err != nil {
		return nil, &MCPToolError{Message: "invalid spec: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	reason, err := promotionaudit.Parse(args.PromotionReason)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
	}
	// Precheck audit fields once at the tool boundary; the pipeline
	// re-validates internally per stage row.
	precheck := promotionaudit.AuditFields{
		WorkspaceID:           pt.t.workspaceID(),
		MCPName:               spec.Name,
		Action:                promotionaudit.ActionRegister,
		PromotedByUserID:      args.PromotedByUserID,
		DriverThreadID:        args.DriverThreadID,
		PromotionReason:       reason,
		CandidateSourceTaskID: args.CandidateSourceTaskID,
		RegistryHashAfter:     emptyBytesSHA256Hex,
	}
	if err := promotionaudit.Validate(precheck); err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailContractViolation}
	}

	pipe := pt.pipelineOverride.Load()
	if pipe == nil {
		var err error
		pipe, err = pt.buildProdPipeline()
		if err != nil {
			return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
		}
	}

	req := promotionpipeline.Request{
		Spec:                  spec,
		CasesPath:             args.CasesPath,
		DryRunRegister:        args.DryRunRegister,
		TimeoutSec:            args.TimeoutSec,
		SlaveAgentID:          args.TargetAgentID,
		SlaveDisplayName:      args.TargetDisplayName,
		WorkspaceID:           pt.t.workspaceID(),
		PromotedByUserID:      args.PromotedByUserID,
		DriverThreadID:        args.DriverThreadID,
		PromotionReason:       reason,
		CandidateSourceTaskID: args.CandidateSourceTaskID,
	}
	outcomes, runErr := pipe.Run(ctx, req)
	if runErr != nil {
		switch runErr {
		case promotionpipeline.ErrPromotionPathDisabled:
			return nil, &MCPToolError{Message: runErr.Error(), Category: observerstore.FailPolicyViolation}
		case promotionpipeline.ErrInvalidCasesPath:
			return nil, &MCPToolError{Message: runErr.Error(), Category: observerstore.FailContractViolation}
		default:
			return nil, &MCPToolError{Message: runErr.Error(), Category: observerstore.FailContractViolation}
		}
	}
	// Serialise the outcomes for the tool response.
	respBody, err := json.Marshal(struct {
		Outcomes promotionpipeline.StageOutcomes `json:"outcomes"`
		Spec     string                          `json:"spec_name"`
	}{Outcomes: outcomes.PopulateErrorMsgs(), Spec: spec.Name})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	return respBody, nil
}

// buildProdPipeline constructs the production Pipeline from Tools.
// Predicates: IsPromotionPathDisabled=nil (B1 not landed),
// IsAcceptanceGateDisabled=ablation.IsNoAcceptanceGate.
func (pt *promotionPipelineTool) buildProdPipeline() (*promotionpipeline.Pipeline, error) {
	deps := promotionpipeline.Deps{
		Delegate: pt.delegateAdapter,
		RegisterCall: func(ctx context.Context, spec buildspec.Spec, sourcePath, tid, tdn string, ts int) (string, error) {
			result, err := pt.t.registerCore(ctx, registerCoreArgs{
				TargetAgentID:     tid,
				TargetDisplayName: tdn,
				Spec:              spec,
				SourcePath:        sourcePath,
				TimeoutSec:        ts,
			}, "promotion_pipeline")
			if err != nil {
				return "", err
			}
			return result.RegistryHash, nil
		},
		AuditWrite:               pt.auditWriteAdapter,
		EventEmit:                pt.eventEmitAdapter,
		// WT-2 B1: NoUserPromotionPath predicate is now wired
		// (was nil at B2's commit boundary).
		IsPromotionPathDisabled:  IsNoUserPromotionPath,
		IsAcceptanceGateDisabled: ablation.IsNoAcceptanceGate,
	}
	return promotionpipeline.New(deps)
}

// delegateAdapter turns a Deps.Delegate call into
// tools.sdk.DelegateTask + waitDelegatedTask, returning the
// slave-side Result body as a string.
func (pt *promotionPipelineTool) delegateAdapter(ctx context.Context, targetAgentID, targetDisplayName, skill, prompt string, timeoutSec int) (string, error) {
	card, err := pt.t.resolveAvailableAgent(ctx, targetAgentID, targetDisplayName)
	if err != nil {
		return "", err
	}
	resp, err := pt.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          skill,
		Prompt:         prompt,
		TimeoutSeconds: timeoutSec,
	})
	if err != nil {
		return "", fmt.Errorf("delegate %s: %w", skill, err)
	}
	waitOut, waitErr := pt.t.waitDelegatedTask(ctx, resp.TaskID, timeoutSec)
	if waitErr != nil {
		return string(waitOut), waitErr
	}
	return string(waitOut), nil
}

// auditWriteAdapter forwards to the Tools' promoAudit writer, or logs
// and no-ops if the writer is nil (matches the existing degrade
// pattern from B6).
func (pt *promotionPipelineTool) auditWriteAdapter(ctx context.Context, f promotionaudit.AuditFields) error {
	if pt.t.promoAudit == nil {
		pt.t.logHelperErr("promotion_audit", "write", errNoPromoAuditSink)
		return nil
	}
	return pt.t.promoAudit.Write(ctx, f)
}

// eventEmitAdapter forwards to the observer sink, or drops silently
// if nil (matches the existing observer-optional pattern). The
// pipeline package emits stage-transition events via this adapter so
// downstream consumers see the promotion pipeline as first-class in
// the observer trace.
func (pt *promotionPipelineTool) eventEmitAdapter(ev observer.Event) {
	if pt.t.observer == nil {
		return
	}
	pt.t.observer.Emit(ev)
}
