package driver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observer"
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

	targetID, targetName, skill, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
	if err != nil {
		return nil, err
	}

	body := strings.TrimSpace(args.Prompt)
	if body == "" {
		body = tc.Intent.Goal
	}
	finalPrompt, err := contract.EncodeEnvelope(tc, body)
	if err != nil {
		return nil, &MCPToolError{Message: "encode contract envelope: " + err.Error()}
	}
	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
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
		"resource_snapshot":   snapshot,
		"warnings":            warnings,
	})
}

func (s *submitContractTaskTool) selectTarget(ctx context.Context, cards []agentsdk.AgentCard, tc contract.TaskContract, targetOverride, skillOverride string) (string, string, string, error) {
	if targetOverride == "" && tc.ExecutionPolicy.Routing == contract.RoutingDirectFirst {
		matches := directContractMatches(cards, s.t.cfg.Credentials.SandboxID, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
		if len(matches) == 1 {
			skill := skillOverride
			if skill == "" {
				skill = "chat"
			}
			return matches[0].AgentID, matches[0].DisplayName, skill, nil
		}
	}
	targetID, targetName, _, targetRole, err := s.t.resolveTarget(ctx, targetOverride)
	if err != nil {
		return "", "", "", err
	}
	if targetRole == observer.RoleMaster && !tc.ExecutionPolicy.AllowsMaster() {
		return "", "", "", &MCPToolError{Message: "master fallback is not allowed by contract"}
	}
	if !targetAllowed(targetID, tc.ExecutionPolicy.AllowedTargets) {
		return "", "", "", &MCPToolError{Message: "target is not allowed by contract: " + targetID}
	}
	skill := skillOverride
	if skill == "" {
		skill = "fanout"
	}
	return targetID, targetName, skill, nil
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
