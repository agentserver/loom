// Package commander implements the daemon side of the commander-web-entry
// architecture. It speaks the WebSocket envelope schema locked in
// docs/superpowers/specs/2026-06-15-driver-daemon-design.md so the observer
// hub can implement the same contract without cross-PR negotiation.
package commander

import "encoding/json"

// SchemaVersion is the protocol version this build of the daemon speaks.
// Bump on any breaking change to envelope or payload shape.
const SchemaVersion = 1

const (
	CapabilitySessions               = "sessions"
	CapabilityTurn                   = "turn"
	CapabilityFiles                  = "files"
	CapabilityFilePreviewEncodedCap  = "file_preview_encoded_cap"
)

const MaxFilePreviewBytes int64 = 2 * 1024 * 1024

// MaxFilePreviewEncodedBytes is the maximum size in bytes of a file's Content
// field after JSON encoding. This defends against pathological files with all
// control bytes, where JSON encoding expands ~6x (each control byte becomes
// \uXXXX). A 1 MiB file of control bytes encodes to ~6 MiB, so we cap at 6 MiB
// to avoid transport issues.
const MaxFilePreviewEncodedBytes int64 = 6 * 1024 * 1024

// Envelope is the JSON shell wrapping every WebSocket frame.
//
// Daemon-to-observer types: register, heartbeat, command_result, event, error.
// Observer-to-daemon types: ack, command, ping.
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
	SchemaVersion int      `json:"schema_version"`
	Kind          string   `json:"kind"`
	AgentBin      string   `json:"agent_bin"`
	AgentWorkDir  string   `json:"agent_workdir"`
	DisplayName   string   `json:"display_name"`
	DriverVersion string   `json:"driver_version"`
	Capabilities  []string `json:"capabilities,omitempty"`
	// ShortID is the stable agent-instance id (agentserver-assigned, persisted).
	// Lets the observer resolve a parent across reconnects (daemon_id is
	// ephemeral). Empty for old daemons.
	ShortID string `json:"short_id,omitempty"`
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
// When Fresh=true, the slave handler treats ID as a client-minted
// placeholder and routes the turn to Backend.Run instead of
// RunResume, returning the real backend session ID via Result.SessionID
// (which marshalTurnResult serializes as `result.session_id`).
type SessionTurnArgs struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Fresh  bool   `json:"fresh,omitempty"`
}

// FileListArgs is the payload for command="list_files".
type FileListArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// FileEntry describes one file or directory in a list_files result.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

// FileListResult is the response payload for command="list_files".
type FileListResult struct {
	Root    string      `json:"root"`
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// FileReadArgs is the payload for command="read_file".
type FileReadArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// FileReadResult is the response payload for command="read_file".
type FileReadResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	MIME     string `json:"mime,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
	Content  string `json:"content,omitempty"`
}

// EventPayload is one streaming event in a session_turn flow.
type EventPayload struct {
	EventKind  string          `json:"event_kind"`
	Text       string          `json:"text,omitempty"`
	Extra      json.RawMessage `json:"extra,omitempty"`
	StatusCode string          `json:"status_code,omitempty"`
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
	ErrCodeInvalidRequest        = "invalid_request"
	ErrCodeInternal              = "internal"
	ErrCodeDaemonUpgradeRequired = "daemon_upgrade_required"
)
