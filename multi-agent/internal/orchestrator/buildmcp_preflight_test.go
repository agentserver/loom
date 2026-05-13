package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/planner"
)

func TestPrepareBuildMCPNode_UsesStructuredBuildSpec(t *testing.T) {
	raw := json.RawMessage(`{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}`)
	node := planner.Node{ID: "n0", Kind: "build_mcp", Skill: "build_mcp", BuildSpec: raw}

	got, err := prepareBuildMCPNode(node)

	require.NoError(t, err)
	require.Equal(t, "build_mcp", got.Skill)
	require.NotEmpty(t, got.Prompt)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}],"hints":"","allowed_packages":[],"compose_servers":[],"version":1,"iteration":1,"max_iterations":3}`, got.Prompt)
	require.Contains(t, got.SystemContext, buildspec.LegacyHashFromJSON(string(raw)))
}

func TestPrepareBuildMCPNode_UsesLegacyPromptJSON(t *testing.T) {
	node := planner.Node{
		ID: "n0", Kind: "build_mcp", Skill: "build_mcp",
		Prompt: `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}`,
	}

	got, err := prepareBuildMCPNode(node)

	require.NoError(t, err)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}],"hints":"","allowed_packages":[],"compose_servers":[],"version":1,"iteration":1,"max_iterations":3}`, got.Prompt)
	require.Contains(t, got.SystemContext, buildspec.LegacyHashFromJSON(node.Prompt))
}

func TestPrepareBuildMCPNode_RejectsNaturalLanguage(t *testing.T) {
	node := planner.Node{ID: "n0", Kind: "build_mcp", Skill: "build_mcp", Prompt: "build a server"}

	_, err := prepareBuildMCPNode(node)

	require.Error(t, err)
	require.ErrorContains(t, err, "malformed build_mcp spec")
}
