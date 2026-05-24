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
const CapabilityEpilogue = "\n\nWhen you finish, append a line `=== CAPABILITY ===` then 1-3 lines describing any persistent capability change to yourself. If none, write `NO_CAPABILITY_CHANGE`."
