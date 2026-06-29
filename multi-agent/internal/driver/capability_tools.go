package driver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
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
			return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
		}
	}
	cards, err := i.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	snapshot := contract.NewResourceSnapshot(cards, i.t.cfg.Credentials.SandboxID)
	warnings := []string{}
	if args.SaveSnapshot == nil || *args.SaveSnapshot {
		body, err := json.Marshal(snapshot)
		if err != nil {
			return nil, &MCPToolError{Message: "encode resource snapshot: " + err.Error(), Category: observerstore.FailUnknown}
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
		MaxConcurrency  int                    `json:"max_concurrency"`
		AllowedTargets  []string               `json:"allowed_targets"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	if strings.TrimSpace(args.Goal) == "" {
		return nil, &MCPToolError{Message: "goal is required", Category: observerstore.FailContractViolation}
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

const (
	routeDirectSlave  = "direct_slave"
	routeDriverFanout = "driver_fanout"
	routeMasterFanout = "master_fanout"
	routeBlocked      = "blocked"
)

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
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	tc := args.Contract
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	cards, err := d.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	report := analyzeContractCapabilities(cards, d.t.cfg.Credentials.SandboxID, tc)
	return json.Marshal(report)
}

type dryRunReport struct {
	Runnable              bool            `json:"runnable"`
	RecommendedRoute      string          `json:"recommended_route"`
	RecommendedTargetID   string          `json:"recommended_target_id,omitempty"`
	RecommendedTargetName string          `json:"recommended_target_display_name,omitempty"`
	RecommendedSkill      string          `json:"recommended_skill,omitempty"`
	SatisfiedTools        []string        `json:"satisfied_tools"`
	MissingTools          []string        `json:"missing_tools"`
	MissingSkills         []string        `json:"missing_skills"`
	MissingResources      json.RawMessage `json:"missing_resources,omitempty"`
	Reasons               []string        `json:"reasons"`
}

func analyzeContractCapabilities(cards []agentsdk.AgentCard, selfID string, tc contract.TaskContract) dryRunReport {
	snapshot := contract.NewResourceSnapshot(cards, selfID)
	report := dryRunReport{RecommendedRoute: routeBlocked}
	report.SatisfiedTools, report.MissingTools = matchRequiredTools(snapshot.Agents, tc.CapabilityRequirements.Tools, tc.ExecutionPolicy.AllowedTargets)
	report.MissingSkills = missingRequiredSkills(snapshot.Agents, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
	if len(report.MissingSkills) > 0 {
		report.Reasons = append(report.Reasons, "required skills are missing or unavailable")
		return report
	}
	if !resourcesAvailable(snapshot.Agents, tc.CapabilityRequirements.Resources, tc.ExecutionPolicy.AllowedTargets) {
		report.MissingResources = append(json.RawMessage(nil), tc.CapabilityRequirements.Resources...)
		report.Reasons = append(report.Reasons, "required resources are missing or unavailable")
		return report
	}

	direct := directContractCapabilityMatches(cards, selfID, tc)
	if tc.ExecutionPolicy.Routing == contract.RoutingDirectFirst && len(direct) == 1 && len(report.MissingTools) == 0 {
		report.Runnable = true
		report.RecommendedRoute = routeDirectSlave
		report.RecommendedTargetID = direct[0].AgentID
		report.RecommendedTargetName = direct[0].DisplayName
		report.RecommendedSkill = "chat"
		report.Reasons = append(report.Reasons, "single direct slave satisfies required skills and tools")
		return report
	}

	master := firstAllowedMaster(snapshot.Agents, tc)
	if tc.ExecutionPolicy.Routing == contract.RoutingMasterOnly {
		if master.AgentID != "" && len(report.MissingTools) == 0 {
			report.Runnable = true
			report.RecommendedRoute = routeMasterFanout
			report.RecommendedTargetID = master.AgentID
			report.RecommendedTargetName = master.DisplayName
			report.RecommendedSkill = "fanout"
			report.Reasons = append(report.Reasons, "master can orchestrate with currently advertised tools")
			return report
		}
	} else if tc.ExecutionPolicy.Routing == contract.RoutingDirectFirst {
		if len(report.MissingTools) == 0 {
			report.Runnable = true
			report.RecommendedRoute = routeDriverFanout
			report.RecommendedSkill = "fanout"
			report.Reasons = append(report.Reasons, "driver can orchestrate with currently advertised tools")
			return report
		}
	}
	if len(report.MissingTools) > 0 {
		report.Reasons = append(report.Reasons, "required tools are missing or unavailable")
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

func cardSatisfiesResources(card agentsdk.AgentCard, required json.RawMessage) bool {
	if resourcesRequirementEmpty(required) {
		return true
	}
	var inner struct {
		Resources json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(card.Card, &inner); err != nil {
		return false
	}
	return resourceJSONContains(inner.Resources, required)
}

func directContractCapabilityMatches(cards []agentsdk.AgentCard, selfID string, tc contract.TaskContract) []agentsdk.AgentCard {
	candidates := directContractMatches(cards, selfID, tc.CapabilityRequirements.Skills, tc.ExecutionPolicy.AllowedTargets)
	matches := make([]agentsdk.AgentCard, 0, len(candidates))
	for _, candidate := range candidates {
		if cardSatisfiesTools(candidate, tc.CapabilityRequirements.Tools) && cardSatisfiesResources(candidate, tc.CapabilityRequirements.Resources) {
			matches = append(matches, candidate)
		}
	}
	return matches
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

func matchRequiredTools(agents []contract.ResourceAgent, required []string, allowedTargets []string) ([]string, []string) {
	satisfied := []string{}
	missing := []string{}
	for _, req := range required {
		if requiredToolAvailable(agents, req, allowedTargets) {
			satisfied = append(satisfied, req)
		} else {
			missing = append(missing, req)
		}
	}
	return satisfied, missing
}

func requiredToolAvailable(agents []contract.ResourceAgent, req string, allowedTargets []string) bool {
	for _, agent := range agents {
		if agent.Status != "available" || !targetAllowed(agent.AgentID, allowedTargets) {
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

func resourcesAvailable(agents []contract.ResourceAgent, required json.RawMessage, allowedTargets []string) bool {
	if resourcesRequirementEmpty(required) {
		return true
	}
	for _, agent := range agents {
		if agent.Status != "available" || !targetAllowed(agent.AgentID, allowedTargets) {
			continue
		}
		if resourceJSONContains(agent.Resources, required) {
			return true
		}
	}
	return false
}

func resourcesRequirementEmpty(required json.RawMessage) bool {
	if len(required) == 0 {
		return true
	}
	var value interface{}
	if err := decodeJSONValue(required, &value); err != nil {
		return false
	}
	if value == nil {
		return true
	}
	object, ok := value.(map[string]interface{})
	return ok && len(object) == 0
}

func resourceJSONContains(candidate, required json.RawMessage) bool {
	if resourcesRequirementEmpty(required) {
		return true
	}
	if len(candidate) == 0 {
		return false
	}
	var candidateValue interface{}
	if err := decodeJSONValue(candidate, &candidateValue); err != nil {
		return false
	}
	var requiredValue interface{}
	if err := decodeJSONValue(required, &requiredValue); err != nil {
		return false
	}
	return jsonSubset(candidateValue, requiredValue)
}

func decodeJSONValue(raw json.RawMessage, dst interface{}) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode(dst)
}

func jsonSubset(candidate, required interface{}) bool {
	switch req := required.(type) {
	case map[string]interface{}:
		cand, ok := candidate.(map[string]interface{})
		if !ok {
			return false
		}
		for key, reqValue := range req {
			candValue, ok := cand[key]
			if !ok || !jsonSubset(candValue, reqValue) {
				return false
			}
		}
		return true
	case []interface{}:
		cand, ok := candidate.([]interface{})
		if !ok {
			return false
		}
		for _, reqValue := range req {
			found := false
			for _, candValue := range cand {
				if jsonSubset(candValue, reqValue) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case json.Number:
		cand, ok := candidate.(json.Number)
		return ok && jsonNumbersEqual(cand, req)
	default:
		return reflect.DeepEqual(candidate, required)
	}
}

func jsonNumbersEqual(candidate, required json.Number) bool {
	candidateRat, ok := new(big.Rat).SetString(candidate.String())
	if !ok {
		return false
	}
	requiredRat, ok := new(big.Rat).SetString(required.String())
	if !ok {
		return false
	}
	return candidateRat.Cmp(requiredRat) == 0
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
