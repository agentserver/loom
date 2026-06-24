package driver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observer"
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
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	tc := args.Contract
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error()}
	}

	cards, err := s.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	snapshot := contract.NewResourceSnapshot(cards, s.t.cfg.Credentials.SandboxID)
	warnings := []string{}
	snapshotBody, err := json.Marshal(snapshot)
	if err != nil {
		return nil, &MCPToolError{Message: "encode resource snapshot: " + err.Error()}
	}
	if err := s.t.observerRelay().SaveResourceSnapshot(ctx, snapshotBody); err != nil {
		warnings = append(warnings, "observer save resource snapshot: "+err.Error())
	}

	report := analyzeContractCapabilities(cards, s.t.cfg.Credentials.SandboxID, tc)
	body := strings.TrimSpace(args.Prompt)
	if body == "" {
		body = tc.Intent.Goal
	}
	finalPrompt, err := contract.EncodeEnvelope(tc, body)
	if err != nil {
		return nil, &MCPToolError{Message: "encode contract envelope: " + err.Error()}
	}
	if args.TargetDisplayName == "" && report.RecommendedRoute == routeDriverFanout {
		if s.t.contractRunner == nil {
			return nil, &MCPToolError{Message: "driver_fanout route is recommended but no driver contract runner is configured"}
		}
		// Path A (driver_fanout) — Before:
		//   result, err := s.t.contractRunner.Run(ctx, finalPrompt, s.t.loomOriginMarker())
		// After (temporary):
		marker := ""
		if pid, err := s.t.requireBoundThread(); err == nil {
			marker = agentbackend.BuildLoomOrigin(
				s.t.cfg.Credentials.ShortID,
				s.t.cfg.Discovery.DisplayName,
				pid,
			)
		}
		result, err := s.t.contractRunner.Run(ctx, finalPrompt, marker)
		if err != nil {
			return nil, &MCPToolError{Message: "driver fanout: " + err.Error()}
		}
		return json.Marshal(map[string]interface{}{
			"route":             routeDriverFanout,
			"summary":           result.Summary,
			"resource_snapshot": snapshot,
			"warnings":          warnings,
		})
	}

	targetID, targetName, targetShortID, skill, route, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
	if err != nil {
		return nil, err
	}

	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	// Path B — Before:
	//   systemContext := ""
	//   if isParentLinkDelegation(skill) {
	//       if m := s.t.loomOriginMarker(); m != "" {
	//           systemContext = m
	//       }
	//   }
	// After (temporary):
	systemContext := ""
	if isParentLinkDelegation(skill) {
		if pid, err := s.t.requireBoundThread(); err == nil {
			systemContext = agentbackend.BuildLoomOrigin(
				s.t.cfg.Credentials.ShortID,
				s.t.cfg.Discovery.DisplayName,
				pid,
			)
		}
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		SystemContext:  systemContext,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}
	// DelegateTask succeeded — slave is already running. From here on, any
	// helper failure degrades to a warning rather than an error so Claude
	// always learns the task_id. See §1.1 #1 of docs/review-2026-06-13.md.
	if err := s.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              s.Name(),
		Response:          resp,
		TargetID:          targetID,
		TargetDisplayName: targetName,
		ChildAgentID:      targetShortID,
		Skill:             skill,
		Wait:              false,
		TimeoutSec:        timeout,
	}); err != nil {
		warnings = append(warnings, "record delegated task: "+err.Error())
		s.t.logHelperErr("driver_journal", "record_delegated_task", err)
	}

	contractBody, err := json.Marshal(tc)
	if err != nil {
		return nil, &MCPToolError{Message: "encode task contract: " + err.Error()}
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
		return "", "", "", "", "", &MCPToolError{Message: "master fallback is not allowed by contract"}
	}
	if !targetAllowed(targetID, tc.ExecutionPolicy.AllowedTargets) {
		return "", "", "", "", "", &MCPToolError{Message: "target is not allowed by contract: " + targetID}
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
