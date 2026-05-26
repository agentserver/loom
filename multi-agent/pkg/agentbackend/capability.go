package agentbackend

import "strings"

// SplitCapability extracts the trailing "=== CAPABILITY ===" epilogue.
// Returns the summary (everything before the marker) and the change description
// (the trimmed text after the marker), with "NO_CAPABILITY_CHANGE" normalized
// to empty.
func SplitCapability(s string) (summary, change string) {
	const sep = "=== CAPABILITY ==="
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return strings.TrimSpace(s), ""
	}
	summary = strings.TrimSpace(s[:i])
	change = strings.TrimSpace(s[i+len(sep):])
	if change == "NO_CAPABILITY_CHANGE" {
		change = ""
	}
	return
}

// CapabilityEpilogue is the system-prompt suffix that asks the agent to print
// the marker + a one-paragraph capability change description (or
// NO_CAPABILITY_CHANGE) on completion.
const CapabilityEpilogue = "\n\nWhen you finish, append a line `=== CAPABILITY ===` then 1-3 lines describing any persistent capability change to yourself. If none, write `NO_CAPABILITY_CHANGE`.\n\nYou have two tools, `ask_user` and `request_permission`, that pause the chat to ask the human at the driver. Only call them when (a) you are genuinely uncertain how to proceed, (b) guessing wrong has a non-trivial cost (loss of work, irreversible side-effects, or wasted spend), and (c) the human can answer in one or two sentences. Otherwise: decide yourself and explain the assumption in your final summary. You have a small budget of questions per task; spend them carefully.\n\n`request_permission` is advisory only — answering \"approve\" does NOT grant you new abilities; the human must run `update_slave_claude_permissions` or `register_slave_mcp` separately to actually elevate."
