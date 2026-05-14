package contract

import (
	"encoding/json"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func NewResourceSnapshot(cards []agentsdk.AgentCard, selfID string) ResourceSnapshot {
	out := ResourceSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Agents:      make([]ResourceAgent, 0, len(cards)),
	}
	for _, c := range cards {
		if c.AgentID == selfID {
			continue
		}
		var inner struct {
			Skills    []string        `json:"skills"`
			Tools     []string        `json:"tools"`
			MCPTools  json.RawMessage `json:"mcp_tools"`
			Resources json.RawMessage `json:"resources"`
			ShortID   string          `json:"short_id"`
		}
		_ = json.Unmarshal(c.Card, &inner)
		out.Agents = append(out.Agents, ResourceAgent{
			AgentID:     c.AgentID,
			ShortID:     inner.ShortID,
			DisplayName: c.DisplayName,
			Description: c.Description,
			Status:      c.Status,
			Skills:      inner.Skills,
			Tools:       inner.Tools,
			MCPTools:    inner.MCPTools,
			Resources:   inner.Resources,
		})
	}
	return out
}
