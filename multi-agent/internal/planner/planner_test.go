package planner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	claudebe "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
)

func fakePlanner(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-planner.sh"))
	require.NoError(t, err)
	return p
}

func newPlanner(t *testing.T, mode string) *Planner {
	fakeBin := fakePlanner(t)
	cfg := agentbackend.ClaudeConfig{Bin: fakeBin}
	backend := claudebe.New(cfg, nil)
	return New(config.Planner{TimeoutSec: 5}, backend.LLM())
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

func TestPlanParsesBuildSpecField(t *testing.T) {
	t.Setenv("FAKE_PLANNER_MODE", "plan_build_spec_field")
	backend := claudebe.New(agentbackend.ClaudeConfig{Bin: fakePlanner(t)}, nil)
	p := New(config.Planner{TimeoutSec: 5}, backend.LLM())

	nodes, err := p.Plan(context.Background(), "build reusable tool", demoAgents)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "register_mcp", nodes[0].Kind)
	require.Equal(t, "register_mcp", nodes[0].Skill)
	require.JSONEq(t, `{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}`, string(nodes[0].BuildSpec))
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

func TestStripJSONFence(t *testing.T) {
	got := stripJSONFence("```json\n[{\"id\":\"n1\"}]\n```")
	if got != `[{"id":"n1"}]` {
		t.Fatalf("got %q", got)
	}
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
		backend := claudebe.New(agentbackend.ClaudeConfig{Bin: fakePlanner(t)}, nil)
		p := New(config.Planner{TimeoutSec: 1}, backend.LLM())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := p.Route(ctx, "x", demoAgents)
		require.ErrorContains(t, err, "timeout")
	})
}

func TestRunClaude_CancelWaitsForCommandBeforeReadingStderr(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "late-stderr.sh")
	started := filepath.Join(dir, "started")
	err := os.WriteFile(bin, []byte(`#!/usr/bin/env bash
touch "$RUN_CLAUDE_STARTED"
(sleep 0.25; echo late-stderr >&2) &
wait
`), 0o755)
	require.NoError(t, err)

	t.Setenv("RUN_CLAUDE_STARTED", started)
	backend := claudebe.New(agentbackend.ClaudeConfig{Bin: bin}, nil)
	p := New(config.Planner{TimeoutSec: 5}, backend.LLM())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := p.Route(ctx, "x", demoAgents)
		errCh <- err
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, time.Second, 10*time.Millisecond)

	start := time.Now()
	cancel()
	err = <-errCh
	require.Error(t, err)
	require.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
}

func TestPlannerIdleTimeoutIsNinetySeconds(t *testing.T) {
	require.Equal(t, 90*time.Second, plannerIdleTimeout)
}

func TestPlan_DecodeNodeKindAndSkill(t *testing.T) {
	jsonSrc := `[
	  {"id":"n0","target_id":"a","kind":"register_mcp","skill":"register_mcp","prompt":"spec"},
	  {"id":"n1","target_id":"b","skill":"mcp","prompt":"call","depends_on":["n0"],"optional":true},
	  {"id":"n2","target_id":"c","prompt":"chat","depends_on":["n1"]}
	]`
	var nodes []Node
	if err := json.Unmarshal([]byte(jsonSrc), &nodes); err != nil {
		t.Fatal(err)
	}
	if nodes[0].Kind != "register_mcp" || nodes[0].Skill != "register_mcp" {
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

func TestPlanPrompt_MentionsKindAndMCPSkill(t *testing.T) {
	p := planPrompt("do something", []agentsdk.AgentCard{
		{AgentID: "a1", DisplayName: "x", Description: "y"},
	})
	for _, want := range []string{
		`"kind"`,       // node attribute documented
		`skill: "mcp"`, // phase-2 use-node guidance
		"resources",    // resources keyword referenced
		"tools",        // tools field referenced
	} {
		if !strings.Contains(p, want) {
			t.Errorf("planPrompt missing %q", want)
		}
	}
	for _, absent := range []string{
		"BUILD_MCP_BLOCKED",
	} {
		if strings.Contains(p, absent) {
			t.Errorf("planPrompt should not mention %q", absent)
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
