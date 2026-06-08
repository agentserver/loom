package driver

import (
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/commandiface"
)

func TestAgentCardParsesPlatformAndCommandInterfaces(t *testing.T) {
	parsed := parseAgentCard(agentsdk.AgentCard{Card: json.RawMessage(`{
		"skills":["bash","powershell","fanout"],
		"tools":["submit_task"],
		"mcp_tools":[{"server":"fs","name":"read_file"}],
		"resources":[{"uri":"file:///tmp/a.txt"}],
		"short_id":"slv-123",
		"platform":{"os":"windows","arch":"amd64"},
		"command_interfaces":[
			{"skill":"powershell","kind":"powershell","command":"pwsh","default":true},
			{"skill":"bash","kind":"bash","command":"bash"}
		]
	}`)})

	require.Equal(t, []string{"bash", "powershell", "fanout"}, parsed.Skills)
	require.Equal(t, []string{"submit_task"}, parsed.Tools)
	require.JSONEq(t, `[{"server":"fs","name":"read_file"}]`, string(parsed.MCPTools))
	require.JSONEq(t, `[{"uri":"file:///tmp/a.txt"}]`, string(parsed.Resources))
	require.Equal(t, "slv-123", parsed.ShortID)
	require.Equal(t, commandiface.Platform{OS: "windows", Arch: "amd64"}, parsed.Platform)
	require.Equal(t, []commandiface.CommandInterface{
		{Skill: "powershell", Kind: "powershell", Command: "pwsh", Default: true},
		{Skill: "bash", Kind: "bash", Command: "bash"},
	}, parsed.CommandInterfaces)
	require.True(t, parsed.HasSkill("powershell"))
	require.True(t, parsed.HasCommandKind("bash"))
	require.Equal(t, commandiface.CommandInterface{Skill: "powershell", Kind: "powershell", Command: "pwsh", Default: true}, parsed.DefaultCommandInterface())
	require.True(t, parsed.SupportsExplicitShell("powershell"))
	require.True(t, parsed.SupportsExplicitShell("bash"))
}

func TestAgentCardLegacyBashFallbackWithoutCommandInterfaces(t *testing.T) {
	parsed := parseAgentCard(agentsdk.AgentCard{Card: json.RawMessage(`{"skills":["bash"],"short_id":"legacy"}`)})

	require.Equal(t, "legacy", parsed.ShortID)
	require.True(t, parsed.HasSkill("bash"))
	require.False(t, parsed.HasCommandKind("bash"))
	require.Equal(t, commandiface.CommandInterface{}, parsed.DefaultCommandInterface())
	require.True(t, parsed.SupportsExplicitShell("bash"))
	require.False(t, parsed.SupportsExplicitShell("powershell"))
}
