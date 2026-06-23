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

// ShouldForwardEnvelopeRaw reports whether a stored chat-skill envelope
// string carries enough downstream-relevant info to justify forwarding it
// to agentserver as a raw JSON object (envelope shape) instead of as a
// JSON-encoded string. Used by the slave's pending-ack drain and the
// dispatcher's completed-task replay path to match the same contract the
// normal Run path establishes via executor.Result.WrappedOutput:
//
//   - kind == "awaiting_user": always forward raw (driver needs the
//     question payload + session_id for resume).
//   - kind == "final" && session_id != "": forward raw so the reverse
//     parent-link reads child session_id from info.Result without
//     depending on the observer relay (#24 P2).
//   - kind == "final" && session_id == "": treat as a plain summary —
//     downstream has no use for an empty session id, and the wire shape
//     must match what the normal path produces (raw summary string) so
//     consumers like the orchestrator's taskOutput parser and the
//     agentserver contract test see the same thing on every code path.
//
// This is intentionally narrower than json.Valid: bash/file/MCP outputs
// that happen to be valid JSON would otherwise be misclassified and
// arrive at agentserver as decoded objects/arrays.
func ShouldForwardEnvelopeRaw(s string) bool {
	if s == "" {
		return false
	}
	var kw struct {
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(s), &kw); err != nil {
		return false
	}
	switch kw.Kind {
	case "awaiting_user":
		return true
	case "final":
		return kw.SessionID != ""
	default:
		return false
	}
}
