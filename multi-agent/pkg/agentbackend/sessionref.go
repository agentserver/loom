package agentbackend

import "fmt"

// SessionRef is a typed reference to an agent conversation. It distinguishes
// the backend-native session id (what the CLI actually persists and resumes
// against) from the agentserver task bridge id (what agentserver uses for
// task SSE/proxy). The compiler enforces the distinction at use sites that
// previously took bare strings.
//
// Backend MUST be set for any operation that reaches a backend (RunResume,
// chat_resume delegation, sidecar writes, cross-daemon nesting). Bridge is
// optional and only meaningful inside the driver/agentsdk seam.
//
// SessionRef does NOT carry its own JSON marshaler. JSON I/O happens at the
// containing-struct level (TaskJournal.Record, response builders), which
// flatten Backend → "session_id" and Bridge → "bridge_session_id" into the
// parent JSON object using explicit fields rather than a nested object.
// See internal/driver/task_journal.go for the canonical example.
//
// Driver-side construction sites legitimately leave Kind and AgentID empty
// (no backend-kind source on the driver). Slave-side wraps populate Kind
// via the Backend interface's Kind() method.
type SessionRef struct {
	Backend string // backend-native conversation id (codex thread uuid, claude session uuid, opencode session id)
	Bridge  string // agentserver task bridge id (cse_<uuid>); optional; non-empty only when wrapped from agentsdk
	Kind    Kind   // codex / claude / opencode; matches the backend kind that owns Backend
	AgentID string // sandbox short_id of the agent that holds this session
}

// IsZero reports whether the ref carries no usable id.
func (r SessionRef) IsZero() bool { return r.Backend == "" && r.Bridge == "" }

// HasBackend reports whether Backend is set (the field required for resume/nesting).
func (r SessionRef) HasBackend() bool { return r.Backend != "" }

// String renders a compact, log-safe representation. Backend takes priority; bridge is parenthesized.
func (r SessionRef) String() string {
	switch {
	case r.Backend != "" && r.Bridge != "":
		return fmt.Sprintf("SessionRef{backend=%s (bridge=%s)}", r.Backend, r.Bridge)
	case r.Backend != "":
		return fmt.Sprintf("SessionRef{backend=%s}", r.Backend)
	case r.Bridge != "":
		return fmt.Sprintf("SessionRef{bridge=%s}", r.Bridge)
	default:
		return "SessionRef{}"
	}
}

// NewBackend builds a ref from a known backend-native id (kind marker,
// loom_origin marker, executor.Result, sidecar read, slave's own session id).
// Panics if backendID is empty — this is for internal invariant enforcement
// where the caller has already validated the id. For external/user-input
// paths (e.g. commander.Handler.SessionTurn) callers MUST validate first
// and return an error themselves, not catch the panic.
//
// agentID may be empty when no cross-agent identity is needed; kind may be
// empty when no backend-kind source is available (driver-side construction).
func NewBackend(kind Kind, agentID, backendID string) SessionRef {
	if backendID == "" {
		panic("agentbackend.NewBackend: empty backendID")
	}
	return SessionRef{Backend: backendID, Kind: kind, AgentID: agentID}
}

// NewBridgeOnly wraps an agentsdk response that has only the bridge id.
// Used at the driver↔agentsdk seam; downstream code that needs Backend
// must error if it sees !HasBackend(). Panics if bridgeID is empty.
func NewBridgeOnly(kind Kind, agentID, bridgeID string) SessionRef {
	if bridgeID == "" {
		panic("agentbackend.NewBridgeOnly: empty bridgeID")
	}
	return SessionRef{Bridge: bridgeID, Kind: kind, AgentID: agentID}
}

// WithBackend returns a copy of r with Backend filled. The only legitimate
// pairing path: take a bridge-only ref returned by NewBridgeOnly, look up
// the backend id (e.g. from the slave's kind marker), and pair them.
// Panics if backendID == "" OR r.Bridge == "" OR r.Backend != "".
func (r SessionRef) WithBackend(backendID string) SessionRef {
	if backendID == "" {
		panic("agentbackend.SessionRef.WithBackend: empty backendID")
	}
	if r.Bridge == "" {
		panic("agentbackend.SessionRef.WithBackend: base has no Bridge; only meaningful on bridge-only refs")
	}
	if r.Backend != "" {
		panic("agentbackend.SessionRef.WithBackend: base already has Backend; refuse to overwrite")
	}
	r.Backend = backendID
	return r
}
