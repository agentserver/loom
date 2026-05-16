package orchestration

import (
	"strings"

	"github.com/yourorg/multi-agent/internal/planner"
)

func FallbackReduceSummary(results []planner.SubResult, reduceErr error) string {
	var sb strings.Builder
	sb.WriteString("# Task Summary\n\n")
	sb.WriteString("Reducer failed: ")
	sb.WriteString(reduceErr.Error())
	sb.WriteString("\n\n")
	sb.WriteString("Completed sub-task outputs are preserved below.\n")
	for _, r := range results {
		sb.WriteString("\n## ")
		sb.WriteString(r.NodeID)
		sb.WriteString(" (")
		sb.WriteString(r.Status)
		sb.WriteString(")\n\n")
		switch {
		case r.Status == "completed" && r.Output != "":
			sb.WriteString(TruncateForSummary(r.Output, 3000))
		case r.Error != "":
			sb.WriteString("Error: ")
			sb.WriteString(r.Error)
		default:
			sb.WriteString("No output.")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func TruncateForSummary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n\n[truncated]\n"
}
