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
  - "skill": which executor on the slave should handle it (see below)
  - "kind": optional special node kind (currently only "build_mcp")
  - "prompt": the sub-task text. May reference an upstream node's output via {{X.output}}, where X must appear in depends_on.
  - "depends_on": array of upstream node ids; empty for root nodes.

Constraints: no cycles. At least one root. Prefer a clear convergence (one or few sink nodes).

Example node with kind: "build_mcp" (first phase):
  {kind: "build_mcp", skill: "build_mcp", ...spec JSON...}
Example node with skill: "mcp" (use existing tool):
  {skill: "mcp", ...tool call...}

Each agent card lists "skills", "tools" (a flattened list of MCP tool names the agent currently exposes), and "resources" (free-form hardware/runtime info: cpu, gpu, memory_gb, devices, tags). When deciding the DAG:

1. If the work needs a tool that some agent already lists in "tools", emit a node targeting that agent. Set "skill": "mcp" and write the prompt as JSON {"server":"<server-name>","tool":"<tool-name>","args":{...}} so the slave's mcp executor handles it directly. Omit "kind".

2. If no agent lists the needed tool but at least one agent has skill "build_mcp" and resources matching the requirement, you MAY emit a sub-task with "kind": "build_mcp" AND "skill": "build_mcp". The prompt MUST be a JSON spec:
       {"name":"<lower_snake>", "description":"...",
        "tools":[{"name":"...","description":"...","args_schema":{...},
                  "result_description":"..."}],
        "hints":"...", "allowed_packages":["..."], "compose_servers":[],
        "version":1, "iteration":1, "max_iterations":3}
   Emit ONLY the build node — do NOT also emit the use nodes in this plan. The orchestrator schedules the build, then automatically calls you again with the agent's updated "tools" list and you plan the use phase then.

3. If a build_mcp sub-task returns an output handle of type "build_mcp_blocked", I will call you again with the blocked output appended to the original task prompt under the marker "BUILD_MCP_BLOCKED: ...". You may:
   (a) emit a new build_mcp node with expanded allowed_packages, or
   (b) emit a new build_mcp node with revised hints/spec, or
   (c) abandon — emit a single chat-skill node that explains the failure to the user.
   Iteration is bounded at 3 globally; after that I fail the master task.

4. Match resources sensibly. A camera-required tool goes to a slave with devices:[camera]. Heavy compute goes to one with gpu. Use the resource fields literally — they are not a fixed schema.

5. For ordinary chat sub-tasks, omit "skill" (or set it to ""); the slave dispatches to its claude executor.

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
		AgentID     string                 `json:"agent_id"`
		DisplayName string                 `json:"display_name"`
		Description string                 `json:"description"`
		Status      string                 `json:"status"`
		Tools       []string               `json:"tools,omitempty"`
		Resources   map[string]interface{} `json:"resources,omitempty"`
	}
	out := make([]lite, len(agents))
	for i, a := range agents {
		out[i] = lite{
			AgentID:     a.AgentID,
			DisplayName: a.DisplayName,
			Description: a.Description,
			Status:      a.Status,
		}
		if len(a.Card) > 0 {
			var inner struct {
				Tools     []string               `json:"tools"`
				Resources map[string]interface{} `json:"resources"`
			}
			_ = json.Unmarshal(a.Card, &inner)
			out[i].Tools = inner.Tools
			out[i].Resources = inner.Resources
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}
