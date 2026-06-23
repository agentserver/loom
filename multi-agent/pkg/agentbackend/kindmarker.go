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
//     callers must unwrap via UnwrapKindMarker and forward the inner
//     summary string. See WireResultFromStoredOutput for the full rule.
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

// WireResultFromStoredOutput converts a stored task output (row.Output /
// a.Reason from store.PopPendingAcks) into the exact value the slave's
// normal completion path would have sent as the agentserver `result`
// field. Three cases, in order:
//
//  1. ShouldForwardEnvelopeRaw(stored) → forwardRaw=true; the caller must
//     send the stored bytes as a raw JSON object (json.RawMessage).
//  2. The envelope is a final marker with empty session_id (or the
//     envelope has no inner summary for any reason) → forwardRaw=false;
//     the caller must wire-encode the unwrapped summary as a JSON string.
//     This is the case dispatch.Run handles by sending res.Summary plain;
//     ack/replay paths must produce the same wire shape.
//  3. stored is not an envelope (bash/file/MCP raw output, including
//     outputs that happen to be valid JSON text) → forwardRaw=false;
//     the caller wire-encodes stored as a JSON string.
//
// In every non-raw case payload is the string the caller should pass to
// json.Marshal. This keeps the wire-shape contract in one place: every
// consumer that turns a stored output into an HTTP `result` field uses
// this helper, and the three execution paths (Run, replay, ack drain)
// produce identical bytes for the same logical task.
func WireResultFromStoredOutput(stored string) (forwardRaw bool, payload string) {
	if ShouldForwardEnvelopeRaw(stored) {
		return true, stored
	}
	// At this point stored is one of:
	//   (a) a final envelope with empty session_id — must unwrap to the
	//       inner summary string so the wire shape matches what Run sent
	//       (Run only stamps WrappedOutput when session_id is non-empty);
	//   (b) not an envelope at all — bash/file/MCP raw output, possibly
	//       valid JSON text. Pass through verbatim.
	// Re-parse for kind so we correctly handle the empty-summary final
	// case (unwrap to "" rather than falling through to envelope text).
	var kw struct {
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stored), &kw); err == nil && kw.Kind == "final" {
		return false, kw.Summary
	}
	return false, stored
}
