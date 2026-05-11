package planner

import (
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestPlanPrompt_IncludesManifestParagraph(t *testing.T) {
	out := planPrompt("do a thing", []agentsdk.AgentCard{})
	wantPhrases := []string{
		"USER_FILES_MANIFEST",
		`"files"`,
		`"writes"`,
		"PUT",
	}
	for _, w := range wantPhrases {
		if !strings.Contains(out, w) {
			t.Errorf("planPrompt missing %q\n----\n%s", w, out)
		}
	}
}

func TestPlanPrompt_IncludesStructuredMCPGuidance(t *testing.T) {
	out := planPrompt("use the renderer", []agentsdk.AgentCard{
		{
			AgentID:     "renderer",
			DisplayName: "Renderer",
			Status:      "available",
			Card: []byte(`{
				"tools":["render"],
				"mcp_tools":[{
					"server":"vision",
					"name":"render",
					"input_schema":{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}
				}]
			}`),
		},
	})
	wantPhrases := []string{
		"mcp_tools",
		"input_schema",
		"must not invent arguments",
		"optional",
		`"server": "vision"`,
		`"name": "render"`,
		`{"server":"<server>","tool":"<tool>","args":{...}}`,
	}
	for _, w := range wantPhrases {
		if !strings.Contains(out, w) {
			t.Errorf("planPrompt missing %q\n----\n%s", w, out)
		}
	}
}
