package agentbackend

import "encoding/json"

// UnwrapKindMarker parses a chat-skill kind-marker envelope out of a string.
// Mirrors internal/driver.unwrapKindMarker so the master orchestrator + the
// driver runner can extract the inner summary from a chat child's task
// result without depending on internal/driver. The slave's dispatcher wraps
// completed chat results into {"kind":"final","summary":..., "session_id":...}
// (see internal/dispatch.Run), and the slave poller forwards that envelope
// verbatim as agentserver `result` whenever the session id is present
// (see internal/poller.execute). Code that reduces child outputs MUST
// peel the envelope, or every chat child's summary disappears into "".
//
// Returns (isAwaiting, finalSummary, questionRaw):
//   - isAwaiting=true when kind=="awaiting_user"; finalSummary=""
//   - finalSummary set when kind=="final"
//   - all zero values when the string is not a recognised envelope (raw
//     summary text, JSON output from a non-chat skill, empty, etc.)
func UnwrapKindMarker(s string) (isAwaiting bool, finalSummary string, question json.RawMessage) {
	if s == "" {
		return false, "", nil
	}
	var kw struct {
		Kind     string          `json:"kind"`
		Summary  string          `json:"summary"`
		Question json.RawMessage `json:"question"`
	}
	if err := json.Unmarshal([]byte(s), &kw); err != nil {
		return false, "", nil
	}
	switch kw.Kind {
	case "awaiting_user":
		return true, "", kw.Question
	case "final":
		return false, kw.Summary, nil
	default:
		return false, "", nil
	}
}

// IsKindMarkerEnvelope reports whether s is specifically a chat-skill
// kind-marker envelope (kind ∈ {"final","awaiting_user"}). The slave's
// poller uses this to decide whether to forward agentserver `result` as a
// raw JSON object (envelope, downstream sessionIDFromMarker / unwrap
// reads it as JSON) or as a JSON-encoded string (any other output,
// including non-chat skill outputs that happen to be valid JSON — those
// must round-trip as strings to match the normal-path semantics).
//
// Note: this is intentionally NOT json.Valid — bash/file/MCP outputs that
// happen to be valid JSON would otherwise be misclassified as envelopes
// and arrive at agentserver as decoded objects/arrays, which downstream
// consumers wouldn't recognise. The contract is precisely "is this the
// envelope shape dispatch.Run produces for chat skills".
func IsKindMarkerEnvelope(s string) bool {
	if s == "" {
		return false
	}
	var kw struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(s), &kw); err != nil {
		return false
	}
	return kw.Kind == "final" || kw.Kind == "awaiting_user"
}
