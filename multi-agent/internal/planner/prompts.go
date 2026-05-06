package planner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func routePrompt(taskPrompt string, agents []agentsdk.AgentCard) string {
	return fmt.Sprintf(`You are a task router. Given a task and a list of available agents, pick the single agent best suited to handle this task.

Output exactly one line of JSON: {"target_id":"<agent_id>"}.
If no agent is suitable, output {"target_id":""}.

Task:
%s

Available agents:
%s
`, taskPrompt, agentsJSON(agents))
}

func planPrompt(taskPrompt string, agents []agentsdk.AgentCard) string {
	return fmt.Sprintf(`You are a task decomposer. Break the following task into a DAG of 1 to 20 sub-tasks. Output a JSON array; each element has:
  - "id": short unique node name (e.g. "n1")
  - "target_id": from the available agents list (the agent_id field)
  - "prompt": the sub-task text. May reference an upstream node's output via {{X.output}}, where X must appear in depends_on.
  - "depends_on": array of upstream node ids; empty for root nodes.

Constraints: no cycles. At least one root. Prefer a clear convergence (one or few sink nodes).

Output ONLY the JSON array, no commentary.

Task:
%s

Available agents:
%s
`, taskPrompt, agentsJSON(agents))
}

func reducePrompt(originalPrompt string, results []SubResult) string {
	var sb strings.Builder
	sb.WriteString("Sub-tasks (with status and output):\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("\n--- node %s [target=%s status=%s] ---\nprompt: %s\n", r.NodeID, r.TargetID, r.Status, r.Prompt))
		if r.Status == "completed" {
			sb.WriteString("output: " + r.Output + "\n")
		} else if r.Error != "" {
			sb.WriteString("error: " + r.Error + "\n")
		}
	}
	return fmt.Sprintf(`You are a task reducer. Given the original task and the outputs of the sub-tasks that ran on its behalf, produce a final answer to the original task.

If some sub-tasks failed or were skipped, mention which ones explicitly so the caller knows what data is missing.

Original task:
%s

%s
`, originalPrompt, sb.String())
}

func agentsJSON(agents []agentsdk.AgentCard) string {
	type lite struct {
		AgentID, DisplayName, Description, Status string
	}
	out := make([]lite, len(agents))
	for i, a := range agents {
		out[i] = lite{a.AgentID, a.DisplayName, a.Description, a.Status}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}
