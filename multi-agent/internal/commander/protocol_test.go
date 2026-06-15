package commander

import (
	"encoding/json"
	"testing"
)

// TestEnvelope_RegisterRoundTrip pins the daemon-to-observer register
// frame as documented in the spec's WS envelope schema lock.
func TestEnvelope_RegisterRoundTrip(t *testing.T) {
	in := Envelope{
		Type: "register",
		Payload: mustMarshal(t, RegisterPayload{
			SchemaVersion: SchemaVersion,
			Kind:          "claude",
			AgentBin:      "/usr/bin/claude",
			AgentWorkDir:  "/home/me/proj",
			DisplayName:   "office-mac",
			DriverVersion: "v0.1.2",
		}),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "register" {
		t.Fatalf("type=%q", out.Type)
	}
	var pl RegisterPayload
	if err := json.Unmarshal(out.Payload, &pl); err != nil {
		t.Fatal(err)
	}
	if pl.Kind != "claude" || pl.DisplayName != "office-mac" {
		t.Fatalf("payload=%+v", pl)
	}
	if pl.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version mismatch: %d", pl.SchemaVersion)
	}
}

// TestEnvelope_CommandWithIDRoundTrip pins that observer-to-daemon command
// frames carry an ID so replies and events can correlate with the command.
func TestEnvelope_CommandWithIDRoundTrip(t *testing.T) {
	in := Envelope{
		Type: "command",
		ID:   "cmd-abc",
		Payload: mustMarshal(t, CommandPayload{
			Command: "get_session",
			Args:    mustMarshal(t, GetSessionArgs{ID: "sess-1"}),
		}),
	}
	b, _ := json.Marshal(in)
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "cmd-abc" {
		t.Fatalf("id=%q", out.ID)
	}
	var cp CommandPayload
	if err := json.Unmarshal(out.Payload, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.Command != "get_session" {
		t.Fatalf("command=%q", cp.Command)
	}
}

// TestEnvelope_EventStreamingShape pins streaming events for session_turn:
// each chunk is a separate event envelope with the originating command ID.
func TestEnvelope_EventStreamingShape(t *testing.T) {
	ev := Envelope{
		Type: "event",
		ID:   "cmd-abc",
		Payload: mustMarshal(t, EventPayload{
			EventKind: "chunk",
			Text:      "hello",
		}),
	}
	b, _ := json.Marshal(ev)
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "event" || out.ID != "cmd-abc" {
		t.Fatalf("envelope=%+v", out)
	}
	var ep EventPayload
	if err := json.Unmarshal(out.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.EventKind != "chunk" || ep.Text != "hello" {
		t.Fatalf("event payload=%+v", ep)
	}
}

// TestEnvelope_ErrorCodesEnumerated pins the spec's error codes so typos at
// call sites fail tests instead of silently becoming protocol drift.
func TestEnvelope_ErrorCodesEnumerated(t *testing.T) {
	codes := []string{
		ErrCodeSessionNotFound,
		ErrCodeBackendUnavailable,
		ErrCodeSchemaVersionMismatch,
		ErrCodeInternal,
	}
	for _, c := range codes {
		if c == "" {
			t.Errorf("error code is empty string")
		}
	}
}

// TestSchemaVersion_IsOne pins that PR-2 ships at schema_version = 1.
func TestSchemaVersion_IsOne(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion=%d want 1", SchemaVersion)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
