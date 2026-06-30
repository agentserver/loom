package driver

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type submitContractTaskTool struct{ t *Tools }

func (s *submitContractTaskTool) Name() string { return "submit_contract_task" }

func (s *submitContractTaskTool) Description() string {
	return "Submit a validated task contract to the best matching workspace agent."
}

func (s *submitContractTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
        "type":"object",
        "properties":{
            "contract":{"type":"object"},
            "prompt":{"type":"string"},
            "target_display_name":{"type":"string"},
            "skill":{"type":"string"},
            "timeout_sec":{"type":"integer"}
        },
        "required":["contract"]
    }`)
}

func (s *submitContractTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Contract          contract.TaskContract `json:"contract"`
		Prompt            string                `json:"prompt"`
		TargetDisplayName string                `json:"target_display_name"`
		Skill             string                `json:"skill"`
		TimeoutSec        int                   `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	tc := args.Contract
	// §7 (a): EnforceContract MUST be the first call after the
	// json.Unmarshal error branch closes. Nothing — no DiscoverAgents,
	// no observer relay write, no log line, no thread bind — may run
	// between here and the json.Unmarshal-error return above. The
	// static AST test TestSubmitContractTaskHandler_FirstCallIsEnforce
	// pins this lexically; the runtime test
	// TestContractToolsEntry_SchemaEnforceBeforeDispatch pins it
	// semantically by t.Fatal'ing any unexpected side effect.
	if err := contract.EnforceContract(&tc); err != nil {
		if errors.Is(err, contract.ErrContractFormalizationDisabled) {
			return s.callNaturalLanguageFallback(ctx, tc, fallbackArgs{
				Prompt:            args.Prompt,
				TargetDisplayName: args.TargetDisplayName,
				Skill:             args.Skill,
				TimeoutSec:        args.TimeoutSec,
			})
		}
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), Category: observerstore.FailContractViolation}
	}

	// --- Pure / read-only block: allowed BEFORE the bind guard. None of
	// these are observable side effects. ---
	cards, err := s.t.sdk.DiscoverAgents(ctx) // read-only RPC #1
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	snapshot := contract.NewResourceSnapshot(cards, s.t.cfg.Credentials.SandboxID)
	snapshotBody, err := json.Marshal(snapshot)
	if err != nil {
		return nil, &MCPToolError{Message: "encode resource snapshot: " + err.Error(), Category: observerstore.FailUnknown}
	}
	report := analyzeContractCapabilities(cards, s.t.cfg.Credentials.SandboxID, tc)

	body := strings.TrimSpace(args.Prompt)
	if body == "" {
		body = tc.Intent.Goal
	}
	finalPrompt, err := contract.EncodeEnvelope(tc, body)
	if err != nil {
		return nil, &MCPToolError{Message: "encode contract envelope: " + err.Error(), Category: observerstore.FailUnknown}
	}

	// --- Compute needsBind in memory BEFORE running selectTarget. ---
	// Path A: route lands on driver_fanout AND no explicit target override.
	//   driver_fanout always spawns nested codex on the driver → always
	//   parent-link → unconditional bind.
	// Path B: resolve the skill the same way selectTarget would
	//   (mirrors contract_tools.go:175-184 defaulting). For master/fanout
	//   targets the default is "fanout"; for slave/direct it's "chat".
	//   Without targetRole we can't perfectly mirror the default — but
	//   both "fanout" and "chat" are in isParentLinkDelegation's set, so
	//   in practice every non-override Path B is parent-link. The only
	//   non-parent-link Path B is an explicit skillOverride to bash /
	//   powershell, which is exotic for contract submissions but legal.
	pathA := args.TargetDisplayName == "" && report.RecommendedRoute == routeDriverFanout
	pathBSkill := args.Skill
	if pathBSkill == "" {
		pathBSkill = "chat" // defensive default; selectTarget may upgrade to fanout
	}
	needsBind := pathA || isParentLinkDelegation(pathBSkill)

	var parentThreadID string
	if needsBind {
		pid, err := s.t.requireBoundThread()
		if err != nil {
			// thread not bound = caller skipped bind_thread; the call has
			// no parent context to attach to.
			return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailWrongContext}
		}
		parentThreadID = pid
	}

	// --- Now-and-only-now: side-effecting operations. ---
	warnings := []string{}
	if err := s.t.observerRelay().SaveResourceSnapshot(ctx, snapshotBody); err != nil {
		warnings = append(warnings, "observer save resource snapshot: "+err.Error())
	}

	// Path A — driver_fanout early return.
	if pathA {
		if s.t.contractRunner == nil {
			return nil, &MCPToolError{Message: "driver_fanout route is recommended but no driver contract runner is configured", Category: observerstore.FailStaleCapability}
		}
		marker := agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			parentThreadID,
		)
		result, err := s.t.contractRunner.Run(ctx, finalPrompt, marker)
		if err != nil {
			return nil, &MCPToolError{Message: "driver fanout: " + err.Error(), Category: observerstore.FailUnknown}
		}
		return json.Marshal(map[string]interface{}{
			"route":             routeDriverFanout,
			"summary":           result.Summary,
			"resource_snapshot": snapshot,
			"warnings":          warnings,
		})
	}

	// Path B — normal selectTarget. Note: this issues a second
	// (read-only) DiscoverAgents inside resolveTarget. Documented in the
	// spec as an acceptable duplicate; out-of-scope to refactor here.
	targetID, targetName, targetShortID, skill, route, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
	if err != nil {
		return nil, err
	}

	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}

	// If selectTarget resolved to a non-parent-link skill (e.g. an
	// explicit bash override on a slave that supports it), our needsBind
	// computation above may have been conservatively true. That's
	// acceptable — we captured the thread id but won't use it. The
	// SystemContext stays empty in that branch:
	systemContext := ""
	if isParentLinkDelegation(skill) {
		systemContext = agentbackend.BuildLoomOrigin(
			s.t.cfg.Credentials.ShortID,
			s.t.cfg.Discovery.DisplayName,
			parentThreadID, // safe even if 0 — but isParentLinkDelegation guarantees needsBind was true above
		)
	}

	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		SystemContext:  systemContext,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		// agentsdk DelegateTask wraps multiple failure modes (transport,
		// auth, version, deadline) into a string-only error today; keep
		// FailUnknown until the SDK exposes a typed error.
		return nil, &MCPToolError{Message: "delegate: " + err.Error(), Category: observerstore.FailUnknown}
	}

	// --- DelegateTask succeeded — from here helper failures degrade to
	// warnings (existing contract; do not change). ---
	var sessRef agentbackend.SessionRef
	if resp.SessionID != "" {
		sessRef = agentbackend.NewBridgeOnly("", targetShortID, resp.SessionID)
	}
	if err := s.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              s.Name(),
		Response:          resp,
		TargetID:          targetID,
		TargetDisplayName: targetName,
		ChildAgentID:      targetShortID,
		Skill:             skill,
		Wait:              false,
		TimeoutSec:        timeout,
		SessionRef:        sessRef,
	}); err != nil {
		warnings = append(warnings, "record delegated task: "+err.Error())
		s.t.logHelperErr("driver_journal", "record_delegated_task", err)
	}

	contractBody, err := json.Marshal(tc)
	if err != nil {
		return nil, &MCPToolError{Message: "encode task contract: " + err.Error(), Category: observerstore.FailUnknown}
	}
	if err := s.t.observerRelay().SaveTaskContract(ctx, resp.TaskID, tc.ConversationID, contractBody); err != nil {
		warnings = append(warnings, "observer save task contract: "+err.Error())
	}

	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"skill":               skill,
		"route":               route,
		"resource_snapshot":   snapshot,
		"warnings":            warnings,
	})
}

func (s *submitContractTaskTool) selectTarget(ctx context.Context, cards []agentsdk.AgentCard, tc contract.TaskContract, targetOverride, skillOverride string) (targetID, targetName, targetShortID, skill, route string, err error) {
	if targetOverride == "" && tc.ExecutionPolicy.Routing == contract.RoutingDirectFirst {
		matches := directContractCapabilityMatches(cards, s.t.cfg.Credentials.SandboxID, tc)
		if len(matches) == 1 {
			skill = skillOverride
			if skill == "" {
				skill = "chat"
			}
			return matches[0].AgentID, matches[0].DisplayName, cardShortID(matches[0]), skill, routeDirectSlave, nil
		}
	}
	targetID, targetName, targetShortID, targetRole, err := s.t.resolveTarget(ctx, targetOverride)
	if err != nil {
		return "", "", "", "", "", err
	}
	if targetRole == observer.RoleMaster && !tc.ExecutionPolicy.AllowsMaster() {
		return "", "", "", "", "", &MCPToolError{Message: "master fallback is not allowed by contract", Category: observerstore.FailPolicyViolation}
	}
	if !targetAllowed(targetID, tc.ExecutionPolicy.AllowedTargets) {
		return "", "", "", "", "", &MCPToolError{Message: "target is not allowed by contract: " + targetID, Category: observerstore.FailPolicyViolation}
	}
	skill = skillOverride
	if targetRole == observer.RoleMaster {
		if skill == "" {
			skill = "fanout"
		}
		return targetID, targetName, targetShortID, skill, routeMasterFanout, nil
	}
	if skill == "" {
		skill = "chat"
	}
	return targetID, targetName, targetShortID, skill, routeDirectSlave, nil
}

func directContractMatches(cards []agentsdk.AgentCard, selfID string, requiredSkills, allowedTargets []string) []agentsdk.AgentCard {
	var matches []agentsdk.AgentCard
	for _, c := range cards {
		if c.AgentID == selfID {
			continue
		}
		if c.Status != "available" {
			continue
		}
		if observerRoleForCard(c) == observer.RoleMaster {
			continue
		}
		if !targetAllowed(c.AgentID, allowedTargets) {
			continue
		}
		if !hasAllSkills(c, requiredSkills) {
			continue
		}
		matches = append(matches, c)
	}
	return matches
}

func hasAllSkills(c agentsdk.AgentCard, required []string) bool {
	for _, skill := range required {
		if !hasSkill(c, skill) {
			return false
		}
	}
	return true
}

func targetAllowed(agentID string, allowedTargets []string) bool {
	if len(allowedTargets) == 0 {
		return true
	}
	for _, allowed := range allowedTargets {
		if allowed == agentID {
			return true
		}
	}
	return false
}

// fallbackArgs is the subset of submit_contract_task's argument struct
// that the natural-language fallback needs. Unexported — used only by
// callNaturalLanguageFallback below.
type fallbackArgs struct {
	Prompt            string
	TargetDisplayName string
	Skill             string
	TimeoutSec        int
}

// callNaturalLanguageFallback implements the spec §3.2 / §4 fallback
// path taken when DisableContractEntirely is true. Logs the drop line
// exactly once, picks a non-empty body (Prompt → defaulted-from-Goal →
// hard error), runs selectTarget, and DelegateTasks the bare body
// (no envelope, no contract JSON).
//
// The route field in the response is the fixed literal
// "natural_language_fallback" so the §D eval harness can distinguish
// this path from a normal routed delegation. resource_snapshot is
// omitted — by §3.2 design we are intentionally exercising the
// no-contract code path, so persisting a snapshot would muddy the
// experiment.
//
// SystemContext is intentionally NOT populated. The ablation point of
// "no contract" is also "no parent-link metadata"; an operator who
// wants parent-link tracing under this ablation should not be using
// the ablation.
func (s *submitContractTaskTool) callNaturalLanguageFallback(
	ctx context.Context, tc contract.TaskContract, args fallbackArgs,
) (json.RawMessage, error) {
	// §3.2 + §7 (c): log line MUST contain the literal substring
	// "dropped contract body" plus "conversation=". T26 greps for these.
	log.Printf("[ablation] NoContractFormalization: dropped contract body on conversation=%s", tc.ConversationID)

	body := strings.TrimSpace(args.Prompt)
	if body == "" {
		if strings.TrimSpace(tc.Intent.Goal) == "" {
			// Neither caller-supplied prompt nor a salvageable intent.
			// Fail loudly rather than silently delegate an empty
			// string to a slave.
			return nil, &MCPToolError{
				Message:  "no prompt and no intent.goal to delegate",
				Category: observerstore.FailContractViolation,
			}
		}
		body = "(contract formalization disabled) " + tc.Intent.Goal
	}

	cards, err := s.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	targetID, targetName, _, skill, _, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
	if err != nil {
		return nil, err
	}

	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}

	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         body,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error(), Category: observerstore.FailUnknown}
	}

	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"skill":               skill,
		"route":               "natural_language_fallback",
		"warnings":            []string{},
	})
}
