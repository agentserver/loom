package orchestration

import (
	"errors"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/planner"
)

func TestFallbackReduceSummaryPreservesCompletedOutputs(t *testing.T) {
	results := []planner.SubResult{
		{NodeID: "node-1", Status: "completed", Output: "completed output"},
		{NodeID: "node-2", Status: "failed", Error: "failed error"},
	}

	summary := FallbackReduceSummary(results, errors.New("reducer unavailable"))

	for _, want := range []string{
		"# Task Summary",
		"Reducer failed: reducer unavailable",
		"completed output",
		"failed error",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}
