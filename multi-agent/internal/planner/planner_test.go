package planner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/config"
)

func fakePlanner(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-planner.sh"))
	require.NoError(t, err)
	return p
}

func newPlanner(t *testing.T, mode string) *Planner {
	return New(config.Planner{
		Bin:        fakePlanner(t),
		TimeoutSec: 5,
		ExtraArgs:  []string{},
	})
}

func withMode(t *testing.T, mode string, fn func()) {
	t.Helper()
	t.Setenv("FAKE_PLANNER_MODE", mode)
	fn()
}

var demoAgents = []agentsdk.AgentCard{
	{AgentID: "agent-a", DisplayName: "A", Status: "available"},
	{AgentID: "agent-b", DisplayName: "B", Status: "available"},
}

func TestRoute_PicksTarget(t *testing.T) {
	withMode(t, "route_a", func() {
		p := newPlanner(t, "route_a")
		target, err := p.Route(context.Background(), "do thing", demoAgents)
		require.NoError(t, err)
		require.Equal(t, "agent-a", target)
	})
}

func TestRoute_EmptyMeansNoCandidate(t *testing.T) {
	withMode(t, "route_empty", func() {
		p := newPlanner(t, "route_empty")
		target, err := p.Route(context.Background(), "do thing", demoAgents)
		require.NoError(t, err)
		require.Equal(t, "", target)
	})
}

func TestPlan_DiamondParsedCorrectly(t *testing.T) {
	withMode(t, "plan_diamond", func() {
		p := newPlanner(t, "plan_diamond")
		nodes, err := p.Plan(context.Background(), "x", demoAgents)
		require.NoError(t, err)
		require.Len(t, nodes, 4)
		require.Equal(t, "n1", nodes[0].ID)
		require.Equal(t, []string{"n1"}, nodes[1].DependsOn)
		require.Equal(t, []string{"n2", "n3"}, nodes[3].DependsOn)
	})
}

func TestPlan_InvalidJSONErrors(t *testing.T) {
	withMode(t, "plan_invalid_json", func() {
		p := newPlanner(t, "plan_invalid_json")
		_, err := p.Plan(context.Background(), "x", demoAgents)
		require.ErrorContains(t, err, "plan unmarshal")
	})
}

func TestReduce_ReturnsClaudeOutput(t *testing.T) {
	withMode(t, "reduce_ok", func() {
		p := newPlanner(t, "reduce_ok")
		out, err := p.Reduce(context.Background(), "task", []SubResult{
			{NodeID: "n1", Status: "completed", Output: "x"},
		})
		require.NoError(t, err)
		require.Equal(t, "REDUCED OUTPUT", out)
	})
}

func TestRunClaude_Exit1(t *testing.T) {
	withMode(t, "exit1", func() {
		p := newPlanner(t, "exit1")
		_, err := p.Route(context.Background(), "x", demoAgents)
		require.ErrorContains(t, err, "boom")
	})
}

func TestRunClaude_Timeout(t *testing.T) {
	withMode(t, "sleep", func() {
		t.Setenv("FAKE_PLANNER_SLEEP", "10")
		p := New(config.Planner{Bin: fakePlanner(t), TimeoutSec: 1})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := p.Route(ctx, "x", demoAgents)
		require.ErrorContains(t, err, "timeout")
	})
}

func TestPlan_DecodeNodeKindAndSkill(t *testing.T) {
	jsonSrc := `[
	  {"id":"n0","target_id":"a","kind":"build_mcp","skill":"build_mcp","prompt":"spec"},
	  {"id":"n1","target_id":"b","skill":"mcp","prompt":"call","depends_on":["n0"],"optional":true},
	  {"id":"n2","target_id":"c","prompt":"chat","depends_on":["n1"]}
	]`
	var nodes []Node
	if err := json.Unmarshal([]byte(jsonSrc), &nodes); err != nil {
		t.Fatal(err)
	}
	if nodes[0].Kind != "build_mcp" || nodes[0].Skill != "build_mcp" {
		t.Fatalf("n0 = %+v", nodes[0])
	}
	if nodes[1].Kind != "" || nodes[1].Skill != "mcp" {
		t.Fatalf("n1 = %+v", nodes[1])
	}
	if !nodes[1].Optional {
		t.Fatalf("n1 optional = false, want true")
	}
	if nodes[2].Kind != "" || nodes[2].Skill != "" {
		t.Fatalf("n2 = %+v", nodes[2])
	}
}

func TestPlanPrompt_MentionsBuildMCPAndKindSkill(t *testing.T) {
	p := planPrompt("do something", []agentsdk.AgentCard{
		{AgentID: "a1", DisplayName: "x", Description: "y"},
	})
	for _, want := range []string{
		"build_mcp",         // skill name appears
		`kind: "build_mcp"`, // node attribute documented
		`skill: "mcp"`,      // phase-2 use-node guidance
		"BUILD_MCP_BLOCKED", // negotiation marker
		"resources",         // resources keyword referenced
		"tools",             // tools field referenced
	} {
		if !strings.Contains(p, want) {
			t.Errorf("planPrompt missing %q", want)
		}
	}
}

func TestAgentsJSON_IncludesToolsAndResources(t *testing.T) {
	cards := []agentsdk.AgentCard{
		{
			AgentID:     "a1",
			DisplayName: "n1",
			Description: "d",
			Card:        []byte(`{"tools":["echo","raise"],"resources":{"devices":["camera"]}}`),
		},
	}
	out := agentsJSON(cards)
	for _, want := range []string{"echo", "raise", "camera"} {
		if !strings.Contains(out, want) {
			t.Errorf("agentsJSON missing %q in:\n%s", want, out)
		}
	}
}

func TestAgentsJSON_IncludesStructuredMCPTools(t *testing.T) {
	cards := []agentsdk.AgentCard{
		{
			AgentID:     "a1",
			DisplayName: "n1",
			Description: "d",
			Card: []byte(`{
				"tools":["render"],
				"mcp_tools":[{
					"server":"vision",
					"name":"render",
					"description":"Render an image",
					"input_schema":{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]},
					"result_description":"image url"
				}]
			}`),
		},
	}

	out := agentsJSON(cards)
	for _, want := range []string{
		`"mcp_tools"`,
		`"server": "vision"`,
		`"name": "render"`,
		`"input_schema"`,
		`"prompt"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agentsJSON missing %q in:\n%s", want, out)
		}
	}
}
