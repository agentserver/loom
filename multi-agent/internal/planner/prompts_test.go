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
		`Use legacy "tools" only when a valid server and argument contract is otherwise known from the task context`,
		"instead of inventing server names or args",
	}
	for _, w := range wantPhrases {
		if !strings.Contains(out, w) {
			t.Errorf("planPrompt missing %q\n----\n%s", w, out)
		}
	}
}

func TestPlanPrompt_IncludesJSONFieldPathGuidanceForMCPArgs(t *testing.T) {
	out := planPrompt("evaluate rows", []agentsdk.AgentCard{})
	wantPhrases := []string{
		"{{n1.output.rows}}",
		"JSON field paths",
		"instead of passing the whole {{n1.output}}",
		"Do not turn direct MCP tool calls into ordinary chat prompts",
	}
	for _, w := range wantPhrases {
		if !strings.Contains(out, w) {
			t.Errorf("planPrompt missing %q\n----\n%s", w, out)
		}
	}
}

func TestReducePrompt_ClarifiesManifestWritesAreHandledByMaster(t *testing.T) {
	out := reducePrompt(`<USER_FILES_MANIFEST version=1>
{"writes":[{"put_url":"http://observer.local/api/writes/wr_1"}]}
</USER_FILES_MANIFEST>

write report`, []SubResult{{NodeID: "n1", Status: "completed", Output: "data"}})
	wantPhrases := []string{
		"Do not call curl",
		"do not perform HTTP PUT",
		"master process will write",
		"Do not claim the upload failed",
	}
	for _, w := range wantPhrases {
		if !strings.Contains(out, w) {
			t.Errorf("reducePrompt missing %q\n----\n%s", w, out)
		}
	}
}
