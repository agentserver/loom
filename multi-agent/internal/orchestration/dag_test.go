package orchestration

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/planner"
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

func TestRender_JSONFieldPath(t *testing.T) {
	out, err := Render(`{"rows":{{csv.output.rows}},"policy":{{policy.output.policy}}}`, map[string]string{
		"csv":    `{"rows":[{"order_id":"1001","amount":128.5}],"summary":{"row_count":1}}`,
		"policy": `{"policy":{"rules":[{"id":"R1","text":"ok"}]}}`,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"rows":[{"order_id":"1001","amount":128.5}],"policy":{"rules":[{"id":"R1","text":"ok"}]}}`, out)
}

func TestRender_JSONNestedFieldPath(t *testing.T) {
	out, err := Render(`count={{csv.output.summary.row_count}}`, map[string]string{
		"csv": `{"summary":{"row_count":5}}`,
	})
	require.NoError(t, err)
	require.Equal(t, "count=5", out)
}

func TestRender_JSONFieldPathErrorsWhenMissing(t *testing.T) {
	_, err := Render(`{{csv.output.missing}}`, map[string]string{
		"csv": `{"rows":[]}`,
	})
	require.ErrorContains(t, err, "csv.output.missing")
}

func TestScheduler_LinearChain(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p"},
		{ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
		{ID: "c", TargetID: "z", Prompt: "p", DependsOn: []string{"b"}},
	}
	s := NewScheduler(nodes, 4)
	require.False(t, s.Done())

	ready := s.Ready()
	require.Len(t, ready, 1)
	require.Equal(t, "a", ready[0].ID)

	s.MarkDispatched("a")
	s.Report("a", "completed", "out-a", "")
	require.False(t, s.Done())

	ready = s.Ready()
	require.Len(t, ready, 1)
	require.Equal(t, "b", ready[0].ID)

	s.MarkDispatched("b")
	s.Report("b", "completed", "out-b", "")
	s.MarkDispatched("c")
	s.Report("c", "completed", "out-c", "")
	require.True(t, s.Done())

	fin := s.AllFinished()
	require.Len(t, fin, 3)
}

func TestScheduler_DiamondParallel(t *testing.T) {
	nodes := []planner.Node{
		{ID: "n1", TargetID: "a", Prompt: "p"},
		{ID: "n2", TargetID: "b", Prompt: "p", DependsOn: []string{"n1"}},
		{ID: "n3", TargetID: "c", Prompt: "p", DependsOn: []string{"n1"}},
		{ID: "n4", TargetID: "d", Prompt: "p", DependsOn: []string{"n2", "n3"}},
	}
	s := NewScheduler(nodes, 4)
	require.Equal(t, []string{"n1"}, idsOf(s.Ready()))
	s.MarkDispatched("n1")
	s.Report("n1", "completed", "x", "")
	ready := s.Ready()
	require.ElementsMatch(t, []string{"n2", "n3"}, idsOf(ready))
}

func TestScheduler_MaxConcurrencyLimits(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p"},
		{ID: "b", TargetID: "y", Prompt: "p"},
		{ID: "c", TargetID: "z", Prompt: "p"},
	}
	s := NewScheduler(nodes, 2)
	require.Len(t, s.Ready(), 2)
	s.MarkDispatched(s.Ready()[0].ID)
	s.MarkDispatched(s.Ready()[0].ID)
	require.Len(t, s.Ready(), 0)
}

func TestScheduler_DownstreamSkippedOnFailure(t *testing.T) {
	nodes := []planner.Node{
		{ID: "a", TargetID: "x", Prompt: "p"},
		{ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
		{ID: "c", TargetID: "z", Prompt: "p", DependsOn: []string{"b"}},
	}
	s := NewScheduler(nodes, 4)
	s.MarkDispatched("a")
	s.Report("a", "failed", "", "boom")
	s.MarkDownstreamSkipped("a")
	require.True(t, s.Done())

	fin := s.AllFinished()
	statuses := map[string]string{}
	for _, f := range fin {
		statuses[f.NodeID] = f.Status
	}
	require.Equal(t, "failed", statuses["a"])
	require.Equal(t, "skipped", statuses["b"])
	require.Equal(t, "skipped", statuses["c"])
}

func idsOf(ns []planner.Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

func TestScheduler_Append(t *testing.T) {
	initial := []planner.Node{
		{ID: "n0", TargetID: "a", Prompt: "p0"},
	}
	s := NewScheduler(initial, 4)
	r := s.Ready()
	if len(r) != 1 || r[0].ID != "n0" {
		t.Fatalf("initial Ready = %v", r)
	}
	s.MarkDispatched("n0")
	s.Report("n0", "completed", "out0", "")

	// Append two new nodes; n1 depends on n0 (already complete), n2 on n1.
	more := []planner.Node{
		{ID: "n1", TargetID: "b", Prompt: "p1", DependsOn: []string{"n0"}},
		{ID: "n2", TargetID: "c", Prompt: "p2", DependsOn: []string{"n1"}},
	}
	if err := s.Append(more); err != nil {
		t.Fatal(err)
	}
	r = s.Ready()
	if len(r) != 1 || r[0].ID != "n1" {
		t.Fatalf("after append Ready = %v", r)
	}
}

func TestScheduler_Append_RejectsDuplicateID(t *testing.T) {
	s := NewScheduler([]planner.Node{{ID: "n0", TargetID: "a", Prompt: "p"}}, 4)
	err := s.Append([]planner.Node{{ID: "n0", TargetID: "b", Prompt: "p"}})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}
