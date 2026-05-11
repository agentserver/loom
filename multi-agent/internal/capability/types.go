package capability

import "encoding/json"

type MCPToolDescriptor struct {
	Server            string          `json:"server" yaml:"server"`
	Name              string          `json:"name" yaml:"name"`
	Description       string          `json:"description,omitempty" yaml:"description,omitempty"`
	InputSchema       json.RawMessage `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`
	ResultDescription string          `json:"result_description,omitempty" yaml:"result_description,omitempty"`
}

func FlatNames(tools []MCPToolDescriptor) []string {
	seen := make(map[string]struct{}, len(tools))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		if _, ok := seen[tool.Name]; ok {
			continue
		}
		seen[tool.Name] = struct{}{}
		names = append(names, tool.Name)
	}
	return names
}

func WithServer(server string, tools []MCPToolDescriptor) []MCPToolDescriptor {
	out := make([]MCPToolDescriptor, len(tools))
	for i, tool := range tools {
		out[i] = tool
		if out[i].Server == "" {
			out[i].Server = server
		}
	}
	return out
}

func ExtractFromAgentCard(card json.RawMessage) ([]MCPToolDescriptor, []string) {
	if len(card) == 0 || string(card) == "null" {
		return nil, nil
	}

	var parsed struct {
		MCPTools []MCPToolDescriptor `json:"mcp_tools"`
		Tools    []string            `json:"tools"`
	}
	if err := json.Unmarshal(card, &parsed); err != nil {
		return nil, nil
	}
	return parsed.MCPTools, parsed.Tools
}

func FindTool(tools []MCPToolDescriptor, server, name string) (MCPToolDescriptor, bool) {
	for _, tool := range tools {
		if tool.Server == server && tool.Name == name {
			return tool, true
		}
	}
	return MCPToolDescriptor{}, false
}
