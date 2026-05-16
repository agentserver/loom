package driver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

type inspectCapabilitiesTool struct{ t *Tools }

func (i *inspectCapabilitiesTool) Name() string { return "inspect_capabilities" }

func (i *inspectCapabilitiesTool) Description() string {
	return "Inspect current workspace capabilities, including skills, resources, and structured MCP tools."
}

func (i *inspectCapabilitiesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"save_snapshot":{"type":"boolean"}},"additionalProperties":false}`)
}

func (i *inspectCapabilitiesTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		SaveSnapshot *bool `json:"save_snapshot"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
		}
	}
	cards, err := i.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	snapshot := contract.NewResourceSnapshot(cards, i.t.cfg.Credentials.SandboxID)
	warnings := []string{}
	if args.SaveSnapshot == nil || *args.SaveSnapshot {
		body, err := json.Marshal(snapshot)
		if err != nil {
			return nil, &MCPToolError{Message: "encode resource snapshot: " + err.Error()}
		}
		if err := i.t.observerRelay().SaveResourceSnapshot(ctx, body); err != nil {
			warnings = append(warnings, "observer save resource snapshot: "+err.Error())
		}
	}
	return json.Marshal(map[string]interface{}{
		"resource_snapshot": snapshot,
		"masters":           filterResourceAgents(snapshot.Agents, true),
		"slaves":            filterResourceAgents(snapshot.Agents, false),
		"mcp_tools":         flattenSnapshotMCPTools(snapshot.Agents),
		"warnings":          warnings,
	})
}

type draftTaskContractTool struct{ t *Tools }

func (d *draftTaskContractTool) Name() string { return "draft_task_contract" }

func (d *draftTaskContractTool) Description() string {
	return "Draft a task contract and clarification questions from a business goal and capability requirements."
}

func (d *draftTaskContractTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"goal":{"type":"string"},
			"business_context":{"type":"string"},
			"success_criteria":{"type":"array","items":{"type":"string"}},
			"write_targets":{"type":"array","items":{"type":"object","properties":{"type":{"type":"string"},"kind":{"type":"string"},"name":{"type":"string"}}}},
			"required_skills":{"type":"array","items":{"type":"string"}},
			"required_tools":{"type":"array","items":{"type":"string"}},
			"resources":{"type":"object"},
			"routing":{"type":"string"},
			"allow_build_mcp":{"type":"boolean"},
			"max_concurrency":{"type":"integer"},
			"allowed_targets":{"type":"array","items":{"type":"string"}}
		},
		"required":["goal"]
	}`)
}

func (d *draftTaskContractTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Goal            string                 `json:"goal"`
		BusinessContext string                 `json:"business_context"`
		SuccessCriteria []string               `json:"success_criteria"`
		WriteTargets    []contract.WriteTarget `json:"write_targets"`
		RequiredSkills  []string               `json:"required_skills"`
		RequiredTools   []string               `json:"required_tools"`
		Resources       json.RawMessage        `json:"resources"`
		Routing         string                 `json:"routing"`
		AllowBuildMCP   bool                   `json:"allow_build_mcp"`
		MaxConcurrency  int                    `json:"max_concurrency"`
		AllowedTargets  []string               `json:"allowed_targets"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if strings.TrimSpace(args.Goal) == "" {
		return nil, &MCPToolError{Message: "goal is required"}
	}
	if len(args.SuccessCriteria) == 0 {
		args.SuccessCriteria = []string{"The requested result is produced as an artifact."}
	}
	if len(args.WriteTargets) == 0 {
		args.WriteTargets = []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "result.md"}}
	}
	for idx := range args.WriteTargets {
		if args.WriteTargets[idx].Type == "" {
			args.WriteTargets[idx].Type = contract.WriteTargetArtifact
		}
	}
	routing := args.Routing
	if routing == "" {
		routing = contract.RoutingMasterOnly
	}
	tc := contract.TaskContract{
		Version:        contract.Version,
		ConversationID: "draft-" + randomHex(8),
		Intent: contract.IntentSpec{
			Goal:            args.Goal,
			BusinessContext: args.BusinessContext,
			SuccessCriteria: args.SuccessCriteria,
		},
		DataContract: contract.DataContract{WriteTargets: args.WriteTargets},
		ExecutionPolicy: contract.ExecutionPolicy{
			Routing:        routing,
			AllowBuildMCP:  args.AllowBuildMCP,
			AllowedTargets: args.AllowedTargets,
		},
		CapabilityRequirements: contract.CapabilityRequirements{
			Skills:    args.RequiredSkills,
			Tools:     args.RequiredTools,
			Resources: args.Resources,
		},
	}
	if args.MaxConcurrency > 0 {
		tc.ExecutionPolicy.MaxConcurrency = contract.Int(args.MaxConcurrency)
	}
	tc.ApplyDefaults()
	questions := clarificationQuestions(tc)
	return json.Marshal(map[string]interface{}{
		"contract":                tc,
		"clarification_questions": questions,
	})
}

type dryRunContractTool struct{ t *Tools }

func (d *dryRunContractTool) Name() string { return "dry_run_contract" }

func (d *dryRunContractTool) Description() string {
	return "Validate a task contract against currently visible agents, skills, resources, and MCP tools without submitting work."
}

func (d *dryRunContractTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"contract":{"type":"object"}},"required":["contract"]}`)
}

func (d *dryRunContractTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Contract contract.TaskContract `json:"contract"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	tc := args.Contract
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error()}
	}
	cards, err := d.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	report := analyzeContractCapabilities(cards, d.t.cfg.Credentials.SandboxID, tc)
	return json.Marshal(report)
}

type dryRunReport struct {
	Runnable              bool                     `json:"runnable"`
	RequiresBuildMCP      bool                     `json:"requires_build_mcp"`
	RecommendedTargetID   string                   `json:"recommended_target_id,omitempty"`
	RecommendedTargetName string                   `json:"recommended_target_display_name,omitempty"`
	RecommendedSkill      string                   `json:"recommended_skill,omitempty"`
	SatisfiedTools        []string                 `json:"satisfied_tools"`
	MissingTools          []string                 `json:"missing_tools"`
	MissingSkills         []string                 `json:"missing_skills"`
	CandidateBuildTargets []contract.ResourceAgent `json:"candidate_build_targets,omitempty"`
	Reasons               []string                 `json:"reasons"`
}

func analyzeContractCapabilities(cards []agentsdk.AgentCard, selfID string, tc contract.TaskContract) dryRunReport {
	snapshot := contract.NewResourceSnapshot(cards, selfID)
	report := dryRunReport{}
	report.SatisfiedTools, report.MissingTools = matchRequiredTools(snapshot.Agents, tc.CapabilityRequirements.Tools)
	report.MissingSkills = missingRequiredSkills(snapshot.Agents, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
	buildTargets := candidateAgentsWithSkill(snapshot.Agents, "build_mcp", tc.ExecutionPolicy.AllowedTargets)
	report.CandidateBuildTargets = buildTargets
	if len(report.MissingSkills) > 0 {
		report.Reasons = append(report.Reasons, "required skills are missing or unavailable")
		return report
	}

	direct := directContractMatches(cards, selfID, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
	if tc.ExecutionPolicy.Routing == contract.RoutingDirectFirst && len(direct) == 1 && len(report.MissingTools) == 0 && cardSatisfiesTools(direct[0], tc.CapabilityRequirements.Tools) {
		report.Runnable = true
		report.RecommendedTargetID = direct[0].AgentID
		report.RecommendedTargetName = direct[0].DisplayName
		report.RecommendedSkill = "chat"
		report.Reasons = append(report.Reasons, "single direct slave satisfies required skills and tools")
		return report
	}

	master := firstAllowedMaster(snapshot.Agents, tc)
	if master.AgentID != "" && len(report.MissingTools) == 0 {
		report.Runnable = true
		report.RecommendedTargetID = master.AgentID
		report.RecommendedTargetName = master.DisplayName
		report.RecommendedSkill = "fanout"
		report.Reasons = append(report.Reasons, "master can orchestrate with currently advertised tools")
		return report
	}
	if len(report.MissingTools) > 0 && tc.ExecutionPolicy.AllowBuildMCP && len(buildTargets) > 0 && master.AgentID != "" {
		report.Runnable = true
		report.RequiresBuildMCP = true
		report.RecommendedTargetID = master.AgentID
		report.RecommendedTargetName = master.DisplayName
		report.RecommendedSkill = "fanout"
		report.Reasons = append(report.Reasons, "missing tools can be built by available build_mcp slaves during master orchestration")
		return report
	}
	if len(report.MissingTools) > 0 {
		if !tc.ExecutionPolicy.AllowBuildMCP {
			report.Reasons = append(report.Reasons, "required tools are missing and allow_build_mcp is false")
		} else if len(buildTargets) == 0 {
			report.Reasons = append(report.Reasons, "required tools are missing and no allowed build_mcp slave is available")
		}
	}
	if master.AgentID == "" && tc.ExecutionPolicy.AllowsMaster() {
		report.Reasons = append(report.Reasons, "no allowed available master with fanout skill is visible")
	}
	return report
}

func cardSatisfiesTools(card agentsdk.AgentCard, required []string) bool {
	tools, flat := capability.ExtractFromAgentCard(card.Card)
	for _, req := range required {
		found := false
		for _, name := range flat {
			if name == req {
				found = true
				break
			}
		}
		for _, tool := range tools {
			if tool.Name == req || qualifiedToolName(tool) == req {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func filterResourceAgents(agents []contract.ResourceAgent, masters bool) []contract.ResourceAgent {
	out := []contract.ResourceAgent{}
	for _, a := range agents {
		isMaster := containsString(a.Skills, "fanout") || containsString(a.Skills, "route")
		if isMaster == masters {
			out = append(out, a)
		}
	}
	return out
}

func flattenSnapshotMCPTools(agents []contract.ResourceAgent) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, agent := range agents {
		var tools []capability.MCPToolDescriptor
		_ = json.Unmarshal(agent.MCPTools, &tools)
		for _, tool := range tools {
			out = append(out, map[string]interface{}{
				"agent_id":            agent.AgentID,
				"agent_display_name":  agent.DisplayName,
				"server":              tool.Server,
				"name":                tool.Name,
				"description":         tool.Description,
				"input_schema":        tool.InputSchema,
				"result_description":  tool.ResultDescription,
				"qualified_tool_name": qualifiedToolName(tool),
			})
		}
	}
	return out
}

func matchRequiredTools(agents []contract.ResourceAgent, required []string) ([]string, []string) {
	satisfied := []string{}
	missing := []string{}
	for _, req := range required {
		if requiredToolAvailable(agents, req) {
			satisfied = append(satisfied, req)
		} else {
			missing = append(missing, req)
		}
	}
	return satisfied, missing
}

func requiredToolAvailable(agents []contract.ResourceAgent, req string) bool {
	for _, agent := range agents {
		if agent.Status != "available" {
			continue
		}
		for _, flat := range agent.Tools {
			if flat == req {
				return true
			}
		}
		var tools []capability.MCPToolDescriptor
		_ = json.Unmarshal(agent.MCPTools, &tools)
		for _, tool := range tools {
			if tool.Name == req || qualifiedToolName(tool) == req {
				return true
			}
		}
	}
	return false
}

func missingRequiredSkills(agents []contract.ResourceAgent, required, allowedTargets []string) []string {
	missing := []string{}
	for _, skill := range required {
		if len(candidateAgentsWithSkill(agents, skill, allowedTargets)) == 0 {
			missing = append(missing, skill)
		}
	}
	return missing
}

func candidateAgentsWithSkill(agents []contract.ResourceAgent, skill string, allowedTargets []string) []contract.ResourceAgent {
	out := []contract.ResourceAgent{}
	for _, agent := range agents {
		if agent.Status != "available" || !targetAllowed(agent.AgentID, allowedTargets) {
			continue
		}
		if containsString(agent.Skills, skill) {
			out = append(out, agent)
		}
	}
	return out
}

func firstAllowedMaster(agents []contract.ResourceAgent, tc contract.TaskContract) contract.ResourceAgent {
	if !tc.ExecutionPolicy.AllowsMaster() {
		return contract.ResourceAgent{}
	}
	for _, agent := range agents {
		if agent.Status != "available" || !targetAllowed(agent.AgentID, tc.ExecutionPolicy.AllowedTargets) {
			continue
		}
		if containsString(agent.Skills, "fanout") {
			return agent
		}
	}
	return contract.ResourceAgent{}
}

func clarificationQuestions(tc contract.TaskContract) []string {
	questions := []string{}
	if len(tc.DataContract.ReadArtifacts) == 0 {
		questions = append(questions, "Which input files or artifact IDs should be read?")
	}
	if len(tc.CapabilityRequirements.Tools) == 0 && len(tc.CapabilityRequirements.Skills) == 0 {
		questions = append(questions, "Which existing tools or skills are required, and may missing tools be built as MCP services?")
	}
	if len(tc.Intent.SuccessCriteria) == 1 && tc.Intent.SuccessCriteria[0] == "The requested result is produced as an artifact." {
		questions = append(questions, "What exact checks should define a successful result?")
	}
	return questions
}

func qualifiedToolName(tool capability.MCPToolDescriptor) string {
	if tool.Server == "" {
		return tool.Name
	}
	return tool.Server + "/" + tool.Name
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(buf)
}
