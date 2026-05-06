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

func TestRender_NoVars(t *testing.T) {
	out, err := Render("hello world", nil)
	require.NoError(t, err)
	require.Equal(t, "hello world", out)
}

func TestRender_Substitutes(t *testing.T) {
	out, err := Render("use {{a.output}} and {{b.output}}", map[string]string{"a": "X", "b": "Y"})
	require.NoError(t, err)
	require.Equal(t, "use X and Y", out)
}

func TestRender_MissingVarErrors(t *testing.T) {
	_, err := Render("use {{ghost.output}}", map[string]string{"a": "X"})
	require.ErrorContains(t, err, "ghost")
}

func TestRender_RepeatedReferences(t *testing.T) {
	out, err := Render("{{a.output}} {{a.output}}", map[string]string{"a": "X"})
	require.NoError(t, err)
	require.Equal(t, "X X", out)
}
