package orchestrator

import (
	"strings"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/planner"
)

func prepareBuildMCPNode(n planner.Node) (planner.Node, error) {
	if n.Kind != "build_mcp" && n.Skill != "build_mcp" {
		return n, nil
	}
	raw := strings.TrimSpace(string(n.BuildSpec))
	if raw == "" {
		raw = n.Prompt
	}
	spec, err := buildspec.ParseJSON(raw)
	if err != nil {
		return planner.Node{}, err
	}
	canonical, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		return planner.Node{}, err
	}
	n.Kind = "build_mcp"
	n.Skill = "build_mcp"
	n.Prompt = canonical
	return n, nil
}

func buildMCPSpecInvalidReplanContext(n planner.Node, err error) string {
	raw := strings.TrimSpace(string(n.BuildSpec))
	if raw == "" {
		raw = n.Prompt
	}
	if len(raw) > 800 {
		raw = raw[:800] + "..."
	}
	return "BUILD_MCP_SPEC_INVALID:\n" +
		"node_id: " + n.ID + "\n" +
		"error: " + err.Error() + "\n" +
		"bad_prompt_or_spec: " + raw + "\n" +
		"required_contract: build_mcp requires JSON with name, description, tools[].name, tools[].description, tools[].args_schema, tools[].result_description\n"
}
