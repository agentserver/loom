package planner

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/capability"
)

// planMaxBodyBytes caps each wrapUserContent body. Picked at 64 KiB
// because modern LLM context windows are 200K+ tokens, leaving room
// for system prompt + agents JSON + any retry feedback. Malicious
// or accidentally enormous user/slave text is truncated with a
// length-disclosing marker so the LLM knows it didn't see the tail.
// Fixes §1.5 #20 of docs/review-2026-06-13.md.
const planMaxBodyBytes = 64 * 1024 // 64 KiB

// tagName returns the bare element name from a tag string that may
// carry attributes, e.g. `sub_output node="n1" target="a"` -> `sub_output`.
// Closing tags don't carry attributes in our boundary scheme, and the
// escape needs to match on the bare element name so both `</sub_output>`
// and `</sub_output foo="bar">` are neutralized.
func tagName(tag string) string {
	if i := strings.IndexByte(tag, ' '); i >= 0 {
		return tag[:i]
	}
	return tag
}

// escapeBoundaryTags neutralizes both opening and closing forms of
// the wrapper tag inside body by inserting U+200D (ZERO WIDTH JOINER)
// after '<' (and after '</'). The LLM tokenizer sees the same visual
// sequence but downstream-style boundary scanners — and the LLM's own
// pattern recognition for the matching opener it just saw — won't
// treat the result as a real tag. We escape on the bare element name
// so any attribute-bearing variant is caught too. Strategy from §1.5
// spec decision record.
//
// Note: the match against "<name" / "</name" is a prefix match, so a
// body legitimately containing e.g. "<user_task_extra>" is mangled
// when wrapping with tag "user_task". This is a deliberate conservative
// trade-off — over-escaping is safe (the LLM still reads the visually
// identical text fine) and under-escaping would allow boundary
// breakout, which is the threat this function exists to defeat.
func escapeBoundaryTags(tag, body string) string {
	name := tagName(tag)
	// Escape closing form first ("</name") so we don't double-escape its
	// '<'. Then escape opening form ("<name" but not "</name").
	closer := "</" + name
	if strings.Contains(body, closer) {
		body = strings.ReplaceAll(body, closer, "<‍/"+name)
	}
	opener := "<" + name
	if strings.Contains(body, opener) {
		// Insert ZWJ right after '<' so "<name" becomes "<‍name".
		body = strings.ReplaceAll(body, opener, "<‍"+name)
	}
	return body
}

// wrapUserContent wraps untrusted text in <tag>...</tag> for use
// inside an LLM prompt. It (a) escapes both opening and closing forms
// of tag's element name in the body so injected boundary tags can't
// break out, and (b) truncates bodies over planMaxBodyBytes with a
// length-disclosing marker. Use this for any string the user or a
// slave can influence.
//
// The closing tag emitted around the body uses only the bare element
// name (so `tag` can carry attributes like `sub_output node="n1"`
// without producing an illegal `</sub_output node="n1">` closer).
//
// The result always ends in a newline so adjacent wrapped sections
// don't visually run together when concatenated.
// Fixes §1.5 #20 of docs/review-2026-06-13.md.
func wrapUserContent(tag, body string) string {
	original := len(body)
	if original > planMaxBodyBytes {
		// Truncate to a rune boundary so we don't leave a broken UTF-8
		// sequence at the cut point. Walk backwards from the cap until
		// we find a valid utf8.RuneStart byte.
		cut := planMaxBodyBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + fmt.Sprintf("\n...[truncated; original %d bytes]", original)
	}
	body = escapeBoundaryTags(tag, body)
	return "<" + tag + ">\n" + body + "\n</" + tagName(tag) + ">\n"
}

// routePrompt builds the LLM prompt for single-agent routing.
// taskPrompt and agentsJSON are wrapped in untrusted-content
// boundaries (§1.5 #20). feedback is appended as a
// <previous_attempt_error> block when non-empty, used by the
// retry loop in Plan/Route (§1.5 #21) to feed parse / validation
// errors back to the LLM for self-repair.
func routePrompt(taskPrompt string, agents []agentsdk.AgentCard, feedback string) string {
	var fb string
	if feedback != "" {
		fb = wrapUserContent("previous_attempt_error", feedback)
	}
	return fmt.Sprintf(`You are a task router. Given a task and a list of available agents, pick the single agent best suited to handle this task.

Output exactly one line of JSON: {"target_id":"<agent_id>"}.
If no agent is suitable, output {"target_id":""}.

%s%s%s`,
		wrapUserContent("user_task", taskPrompt),
		wrapUserContent("available_agents", agentsJSON(agents)),
		fb,
	)
}

// planPrompt builds the LLM prompt for DAG decomposition.
// See routePrompt for the boundary + feedback semantics.
func planPrompt(taskPrompt string, agents []agentsdk.AgentCard, feedback string) string {
	var fb string
	if feedback != "" {
		fb = wrapUserContent("previous_attempt_error", feedback)
	}
	body := `You are a task decomposer. Break the following task into a DAG of 1 to 20 sub-tasks. Output a JSON array; each element has:
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

`
	return body + wrapUserContent("user_task", taskPrompt) + wrapUserContent("available_agents", agentsJSON(agents)) + fb
}

// reducePrompt builds the LLM prompt for reducing sub-task results
// into a final answer. Every node-controlled string (Prompt, Output,
// Error) is wrapped with attribute-bearing boundary tags so a
// malicious slave can't inject pseudo-tags to steer the reducer.
// See §1.5 #20.
//
// Unlike routePrompt / planPrompt, reduce has no retry loop and so
// takes no feedback parameter: sub-task outputs are facts produced by
// the executing slaves, not LLM choices to validate against, so there
// is nothing for the reducer to "re-attempt" against parser feedback.
func reducePrompt(originalPrompt string, results []SubResult) string {
	var sb strings.Builder
	sb.WriteString("Sub-tasks (with status and output):\n\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("--- node %s [target=%s status=%s] ---\n", r.NodeID, r.TargetID, r.Status))
		sb.WriteString(wrapUserContent(fmt.Sprintf(`sub_prompt node="%s"`, r.NodeID), r.Prompt))
		if r.Status == "completed" {
			sb.WriteString(wrapUserContent(
				fmt.Sprintf(`sub_output node="%s" target="%s"`, r.NodeID, r.TargetID),
				r.Output,
			))
		} else if r.Error != "" {
			sb.WriteString(wrapUserContent(fmt.Sprintf(`sub_error node="%s"`, r.NodeID), r.Error))
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

%s
%s`,
		writeGuidance,
		wrapUserContent("original_task", originalPrompt),
		sb.String(),
	)
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
