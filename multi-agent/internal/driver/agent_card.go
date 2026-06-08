package driver

import (
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/commandiface"
)

type parsedAgentCard struct {
	Skills            []string
	Tools             []string
	MCPTools          json.RawMessage
	Resources         json.RawMessage
	ShortID           string
	Platform          commandiface.Platform
	CommandInterfaces []commandiface.CommandInterface
}

func parseAgentCard(c agentsdk.AgentCard) parsedAgentCard {
	var parsed struct {
		Skills            []string                        `json:"skills"`
		Tools             []string                        `json:"tools"`
		MCPTools          json.RawMessage                 `json:"mcp_tools"`
		Resources         json.RawMessage                 `json:"resources"`
		ShortID           string                          `json:"short_id"`
		Platform          commandiface.Platform           `json:"platform"`
		CommandInterfaces []commandiface.CommandInterface `json:"command_interfaces"`
	}
	_ = json.Unmarshal(c.Card, &parsed)
	return parsedAgentCard{
		Skills:            parsed.Skills,
		Tools:             parsed.Tools,
		MCPTools:          parsed.MCPTools,
		Resources:         parsed.Resources,
		ShortID:           parsed.ShortID,
		Platform:          parsed.Platform,
		CommandInterfaces: parsed.CommandInterfaces,
	}
}

func (c parsedAgentCard) HasSkill(want string) bool {
	for _, skill := range c.Skills {
		if skill == want {
			return true
		}
	}
	return false
}

func (c parsedAgentCard) HasCommandKind(kind string) bool {
	for _, commandInterface := range c.CommandInterfaces {
		if commandInterface.Kind == kind {
			return true
		}
	}
	return false
}

func (c parsedAgentCard) DefaultCommandInterface() commandiface.CommandInterface {
	for _, commandInterface := range c.CommandInterfaces {
		if commandInterface.Default {
			return commandInterface
		}
	}
	if len(c.CommandInterfaces) > 0 {
		return c.CommandInterfaces[0]
	}
	return commandiface.CommandInterface{}
}

func (c parsedAgentCard) SupportsExplicitShell(kind string) bool {
	switch kind {
	case "bash", "powershell":
		return c.HasSkill(kind) && (len(c.CommandInterfaces) == 0 || c.HasCommandKind(kind))
	default:
		return false
	}
}
