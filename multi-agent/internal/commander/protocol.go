// Package commander implements the daemon side of the commander-web-entry
// architecture. It speaks the WebSocket envelope schema locked in
// docs/superpowers/specs/2026-06-15-driver-daemon-design.md so the observer
// hub can implement the same contract without cross-PR negotiation.
package commander

import "encoding/json"

// SchemaVersion is the protocol version this build of the daemon speaks.
// Bump on any breaking change to envelope or payload shape.
const SchemaVersion = 1

// Envelope is the JSON shell wrapping every WebSocket frame.
//
// Daemon-to-observer types: register, heartbeat, command_result, event, error.
// Observer-to-daemon types: command, ping.
type Envelope struct {
	// Type names the frame kind; routing dispatches on this.
	Type string `json:"type"`
	// ID correlates command frames with command_result/event frames.
	ID string `json:"id,omitempty"`
	// Payload is the type-specific body.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RegisterPayload is the first frame the daemon sends after the WS upgrade.
type RegisterPayload struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	AgentBin      string `json:"agent_bin"`
	AgentWorkDir  string `json:"agent_workdir"`
	DisplayName   string `json:"display_name"`
	DriverVersion string `json:"driver_version"`
}

// CommandPayload describes an observer-to-daemon command.
type CommandPayload struct {
	Command string          `json:"command"`
	Args    json.RawMessage `json:"args,omitempty"`
}

// GetSessionArgs is the payload for command="get_session".
type GetSessionArgs struct {
	ID string `json:"id"`
}

// SessionTurnArgs is the payload for command="session_turn".
type SessionTurnArgs struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

// EventPayload is one streaming event in a session_turn flow.
type EventPayload struct {
	EventKind string          `json:"event_kind"`
	Text      string          `json:"text,omitempty"`
	Extra     json.RawMessage `json:"extra,omitempty"`
}

// ErrorPayload is the body of an error envelope.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	ErrCodeSessionNotFound       = "session_not_found"
	ErrCodeBackendUnavailable    = "backend_unavailable"
	ErrCodeSchemaVersionMismatch = "schema_version_mismatch"
	ErrCodeInternal              = "internal"
)
