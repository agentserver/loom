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
