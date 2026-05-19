package planner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/capability"
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
  - "kind": optional special node kind
  - "prompt": the sub-task text. May reference an upstream node's output via {{X.output}}, where X must appear in depends_on.
    Template references also support JSON field paths like {{n1.output.rows}} or {{n2.output.policy.rules}} for extracting nested fields from upstream JSON outputs.
  - "depends_on": array of upstream node ids; empty for root nodes.
  - "optional": optional boolean. Nodes are required by default; set optional:true only when the original user request can still succeed without that node.

Constraints: no cycles. At least one root. Prefer a clear convergence (one or few sink nodes).

Example node with skill: "mcp" (use existing tool):
  {skill: "mcp", ...tool call...}

Each agent card lists "skills", "tools" (a flattened legacy list of MCP tool names), "mcp_tools" (structured MCP tools with server, name, description, input_schema, and result_description), and "resources" (free-form hardware/runtime info: cpu, gpu, memory_gb, devices, tags). When deciding the DAG:

1. If the work needs a tool that some agent already lists in "mcp_tools", prefer/use "mcp_tools" for MCP planning and emit a node targeting that agent. Set "skill": "mcp" and write the prompt as JSON {"server":"<server>","tool":"<tool>","args":{...}} so the slave's mcp executor handles it directly. Omit "kind". The args object MUST conform to that tool's input_schema. You must not invent arguments outside input_schema. If a needed argument is absent from input_schema, evolve/build MCP instead of calling the existing tool with extra args. When a schema expects a nested value from an upstream JSON output, use JSON field paths in the MCP JSON prompt, e.g. "rows":{{n1.output.rows}} instead of passing the whole {{n1.output}} when only its rows array is required. Do not turn direct MCP tool calls into ordinary chat prompts asking a slave to call MCP; direct MCP calls must stay skill:"mcp" JSON nodes. Use legacy "tools" only when a valid server and argument contract is otherwise known from the task context; otherwise build/evolve an MCP tool or use ordinary chat instead of inventing server names or args.

2. Match resources sensibly. A camera-required tool goes to a slave with devices:[camera]. Heavy compute goes to one with gpu. Use the resource fields literally — they are not a fixed schema.

3. For ordinary chat sub-tasks, omit "skill" (or set it to ""); the slave dispatches to its claude executor.

4. Nodes are required by default. Set "optional": true only when the original user request can still succeed without that node.

The user's request may begin with a <USER_FILES_MANIFEST version=1> block
followed by a JSON object. The "files" array names files the user has
referenced; each entry has either a "url" (for a file) or "list_url" +
"blob_url" (for a directory). When you assign work to a slave that needs
to read a referenced file, include the relevant url in that node's prompt
so the slave can GET it. The "writes" array lists local paths the user
wants results written to; when a slave produces a result that should land
at one of those paths, include the matching "put_url" in the slave's
prompt and instruct the slave to PUT the resulting bytes to that URL
(the URL accepts a single PUT and returns 200 on success). The block
itself is metadata; do not echo it back, and do not invent additional
fields.

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
	writeGuidance := ""
	if strings.Contains(originalPrompt, "<USER_FILES_MANIFEST") && strings.Contains(originalPrompt, `"writes"`) {
		writeGuidance = `
The original task includes manifest write targets. Do not call curl, do not perform HTTP PUT, and do not ask for approval to write files. The master process will write your returned final answer to the requested destination after reduction. Do not claim the upload failed or is still required.
`
	}

	return fmt.Sprintf(`You are a task reducer. Given the original task and the outputs of the sub-tasks that ran on its behalf, produce a final answer to the original task.

If some sub-tasks failed or were skipped, mention which ones explicitly so the caller knows what data is missing.
%s

Original task:
%s

%s
`, writeGuidance, originalPrompt, sb.String())
}

func agentsJSON(agents []agentsdk.AgentCard) string {
	type lite struct {
		AgentID     string                         `json:"agent_id"`
		DisplayName string                         `json:"display_name"`
		Description string                         `json:"description"`
		Status      string                         `json:"status"`
		Skills      []string                       `json:"skills,omitempty"`
		Tools       []string                       `json:"tools,omitempty"`
		MCPTools    []capability.MCPToolDescriptor `json:"mcp_tools,omitempty"`
		Resources   map[string]interface{}         `json:"resources,omitempty"`
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
				Skills    []string                       `json:"skills"`
				Tools     []string                       `json:"tools"`
				MCPTools  []capability.MCPToolDescriptor `json:"mcp_tools"`
				Resources map[string]interface{}         `json:"resources"`
			}
			_ = json.Unmarshal(a.Card, &inner)
			out[i].Skills = inner.Skills
			out[i].Tools = inner.Tools
			out[i].MCPTools = inner.MCPTools
			out[i].Resources = inner.Resources
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}
