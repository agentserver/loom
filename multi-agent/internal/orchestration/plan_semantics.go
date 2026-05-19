package orchestration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/planner"
)

type planValidationError struct {
	err error
}

func (e planValidationError) Error() string { return e.err.Error() }
func (e planValidationError) Unwrap() error { return e.err }

func preparePlanForDispatch(nodes []planner.Node, agents []agentsdk.AgentCard) ([]planner.Node, error) {
	if err := Validate(nodes); err != nil {
		return nil, planValidationError{err: err}
	}
	prepared := make([]planner.Node, len(nodes))
	copy(prepared, nodes)
	for i := range prepared {
		node, err := prepareNodeForDispatch(prepared[i], agents)
		if err != nil {
			return nil, planValidationError{err: fmt.Errorf("node %s: %w", prepared[i].ID, err)}
		}
		prepared[i] = node
	}
	return prepared, nil
}

func prepareNodeForDispatch(n planner.Node, agents []agentsdk.AgentCard) (planner.Node, error) {
	if n.TargetID == "" {
		return n, fmt.Errorf("target_id required")
	}
	caps, ok := findAgentCapabilities(agents, n.TargetID)
	if !ok {
		return n, fmt.Errorf("unknown target_id %q", n.TargetID)
	}
	if err := validateAdvertisedSkill(n, caps); err != nil {
		return n, err
	}
	switch {
	case n.Skill == "build_mcp" || n.Kind == "build_mcp":
		if n.Skill != "build_mcp" {
			return n, fmt.Errorf("build_mcp node must set skill %q", "build_mcp")
		}
		prompt, err := canonicalBuildMCPPrompt(n)
		if err != nil {
			return n, err
		}
		n.Prompt = prompt
		return n, nil
	case n.Skill == "mcp":
		return n, validateMCPPrompt(n.Prompt, caps, !containsTemplate(n.Prompt))
	default:
		return n, nil
	}
}

func validateRenderedNodePrompt(n planner.Node, rendered string, agents []agentsdk.AgentCard) error {
	caps, ok := findAgentCapabilities(agents, n.TargetID)
	if !ok {
		return planValidationError{err: fmt.Errorf("node %s: unknown target_id %q", n.ID, n.TargetID)}
	}
	if n.Skill == "mcp" {
		if err := validateMCPPrompt(rendered, caps, true); err != nil {
			return planValidationError{err: fmt.Errorf("node %s: %w", n.ID, err)}
		}
	}
	return nil
}

func canonicalBuildMCPPrompt(n planner.Node) (string, error) {
	raw := strings.TrimSpace(string(n.BuildSpec))
	if raw == "" {
		raw = strings.TrimSpace(n.Prompt)
	}
	if raw == "" {
		return "", fmt.Errorf("build_mcp requires build_spec or legacy JSON prompt")
	}
	spec, err := buildspec.ParseJSON(raw)
	if err != nil {
		return "", fmt.Errorf("invalid build_spec: %w", err)
	}
	canonical, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		return "", fmt.Errorf("invalid build_spec: %w", err)
	}
	return canonical, nil
}

type agentCapabilities struct {
	skillsKnown bool
	skills      map[string]bool
	mcpTools    []capability.MCPToolDescriptor
}

func findAgentCapabilities(agents []agentsdk.AgentCard, targetID string) (agentCapabilities, bool) {
	for _, agent := range agents {
		if agent.AgentID != targetID {
			continue
		}
		return parseAgentCapabilities(agent.Card), true
	}
	return agentCapabilities{}, false
}

func parseAgentCapabilities(card json.RawMessage) agentCapabilities {
	caps := agentCapabilities{skills: map[string]bool{}}
	if len(card) == 0 || string(card) == "null" {
		return caps
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(card, &raw); err != nil {
		return caps
	}
	if skillsRaw, ok := raw["skills"]; ok {
		caps.skillsKnown = true
		var skills []string
		_ = json.Unmarshal(skillsRaw, &skills)
		for _, skill := range skills {
			caps.skills[skill] = true
		}
	}
	if toolsRaw, ok := raw["mcp_tools"]; ok {
		_ = json.Unmarshal(toolsRaw, &caps.mcpTools)
	}
	return caps
}

func validateAdvertisedSkill(n planner.Node, caps agentCapabilities) error {
	if n.Skill == "" || !caps.skillsKnown {
		return nil
	}
	if caps.skills[n.Skill] {
		return nil
	}
	return fmt.Errorf("target %q does not advertise skill %q", n.TargetID, n.Skill)
}

type mcpPlanPrompt struct {
	Server string                 `json:"server"`
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
}

func validateMCPPrompt(raw string, caps agentCapabilities, validateArgs bool) error {
	parseRaw := raw
	if containsTemplate(raw) {
		parseRaw = templateExpr.ReplaceAllString(raw, "null")
	}
	var prompt mcpPlanPrompt
	if err := json.Unmarshal([]byte(parseRaw), &prompt); err != nil {
		return fmt.Errorf("mcp prompt must be JSON: %w", err)
	}
	if prompt.Server == "" || prompt.Tool == "" {
		return fmt.Errorf("mcp prompt missing server or tool")
	}
	tool, ok := capability.FindTool(caps.mcpTools, prompt.Server, prompt.Tool)
	if !ok {
		return fmt.Errorf("target does not advertise mcp tool %s/%s", prompt.Server, prompt.Tool)
	}
	if validateArgs {
		if err := validateSchema(tool.InputSchema, prompt.Args, "args"); err != nil {
			return err
		}
	}
	return nil
}

var templateExpr = regexp.MustCompile(`\{\{[^{}]+\}\}`)

func containsTemplate(s string) bool {
	return templateExpr.MatchString(s)
}

type simpleSchema struct {
	Type                 interface{}                `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	Items                json.RawMessage            `json:"items"`
	AdditionalProperties interface{}                `json:"additionalProperties"`
}

func validateSchema(schemaRaw json.RawMessage, value interface{}, path string) error {
	if len(strings.TrimSpace(string(schemaRaw))) == 0 {
		return nil
	}
	var schema simpleSchema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return fmt.Errorf("invalid input_schema for %s: %w", path, err)
	}
	if !schemaTypeAllows(schema.Type, value) {
		return fmt.Errorf("%s has wrong type", path)
	}
	if schemaWantsObject(schema) {
		obj, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s has wrong type", path)
		}
		for _, required := range schema.Required {
			if _, ok := obj[required]; !ok {
				return fmt.Errorf("%s missing required property %q", path, required)
			}
		}
		if disallowAdditional(schema.AdditionalProperties) {
			for key := range obj {
				if _, ok := schema.Properties[key]; !ok {
					return fmt.Errorf("%s unexpected property %q", path, key)
				}
			}
		}
		for key, childSchema := range schema.Properties {
			child, ok := obj[key]
			if !ok {
				continue
			}
			if err := validateSchema(childSchema, child, path+"."+key); err != nil {
				return err
			}
		}
	}
	if schemaWantsArray(schema) && len(schema.Items) > 0 {
		arr, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("%s has wrong type", path)
		}
		for i, item := range arr {
			if err := validateSchema(schema.Items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaWantsObject(schema simpleSchema) bool {
	types := schemaTypeNames(schema.Type)
	if types["object"] {
		return true
	}
	return len(types) == 0 && (len(schema.Properties) > 0 || len(schema.Required) > 0 || schema.AdditionalProperties != nil)
}

func schemaWantsArray(schema simpleSchema) bool {
	return schemaTypeNames(schema.Type)["array"]
}

func schemaTypeAllows(raw interface{}, value interface{}) bool {
	types := schemaTypeNames(raw)
	if len(types) == 0 {
		return true
	}
	for typ := range types {
		if jsonValueMatchesType(value, typ) {
			return true
		}
	}
	return false
}

func schemaTypeNames(raw interface{}) map[string]bool {
	out := map[string]bool{}
	switch v := raw.(type) {
	case string:
		out[v] = true
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func jsonValueMatchesType(value interface{}, typ string) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "array":
		_, ok := value.([]interface{})
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && n == float64(int64(n))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func disallowAdditional(raw interface{}) bool {
	v, ok := raw.(bool)
	return ok && !v
}
