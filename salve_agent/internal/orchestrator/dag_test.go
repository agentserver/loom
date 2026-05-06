package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/salve_agent/internal/planner"
)

func TestValidate_OK(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p"},
		{ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
	}
	require.NoError(t, Validate(nodes))
}

func TestValidate_Empty(t *testing.T) {
	require.ErrorContains(t, Validate(nil), "plan empty")
}

func TestValidate_DuplicateID(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p"},
		{ID: "a", TargetID: "y", Prompt: "p"},
	}
	require.ErrorContains(t, Validate(nodes), "duplicate")
}

func TestValidate_DanglingDep(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p", DependsOn: []string{"ghost"}},
	}
	require.ErrorContains(t, Validate(nodes), "dangling dep")
}

func TestValidate_Cycle(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p", DependsOn: []string{"b"}},
		{ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
	}
	require.ErrorContains(t, Validate(nodes), "cycle")
}

func TestValidate_TooLarge(t *testing.T) {
	var nodes []planner.Node
	for i := 0; i < 101; i++ {
		nodes = append(nodes, planner.Node{ID: string(rune('a' + i)), TargetID: "x", Prompt: "p"})
	}
	require.ErrorContains(t, Validate(nodes), "plan too large")
}
