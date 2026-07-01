package commander

import (
	"encoding/json"
	"slices"
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

func TestEnvelope_RegisterCarriesCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "CapabilitySessions", got: CapabilitySessions, want: "sessions"},
		{name: "CapabilityTurn", got: CapabilityTurn, want: "turn"},
		{name: "CapabilityFiles", got: CapabilityFiles, want: "files"},
		{name: "CapabilityFilePreviewEncodedCap", got: CapabilityFilePreviewEncodedCap, want: "file_preview_encoded_cap"},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s=%q want %q", tc.name, tc.got, tc.want)
		}
	}

	in := Envelope{
		Type: "register",
		Payload: mustMarshal(t, RegisterPayload{
			SchemaVersion: SchemaVersion,
			Kind:          "codex",
			Capabilities:  []string{CapabilitySessions, CapabilityTurn, CapabilityFiles},
		}),
	}
	b, _ := json.Marshal(in)
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	var pl RegisterPayload
	if err := json.Unmarshal(out.Payload, &pl); err != nil {
		t.Fatal(err)
	}
	want := []string{CapabilitySessions, CapabilityTurn, CapabilityFiles}
	if !slices.Equal(pl.Capabilities, want) {
		t.Fatalf("capabilities=%v want %v", pl.Capabilities, want)
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

func TestEnvelope_FileCommandsRoundTrip(t *testing.T) {
	listArgs := FileListArgs{ID: "s1", Path: "internal"}
	readArgs := FileReadArgs{ID: "s1", Path: "go.mod"}
	for name, args := range map[string]any{
		"list_files": listArgs,
		"read_file":  readArgs,
	} {
		env := Envelope{
			Type: "command",
			ID:   "cmd-file",
			Payload: mustMarshal(t, CommandPayload{
				Command: name,
				Args:    mustMarshal(t, args),
			}),
		}
		b, _ := json.Marshal(env)
		var out Envelope
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		var cp CommandPayload
		if err := json.Unmarshal(out.Payload, &cp); err != nil {
			t.Fatal(err)
		}
		if cp.Command != name {
			t.Fatalf("command=%q want %q", cp.Command, name)
		}
		switch name {
		case "list_files":
			var got FileListArgs
			if err := json.Unmarshal(cp.Args, &got); err != nil {
				t.Fatal(err)
			}
			if got != listArgs {
				t.Fatalf("list args=%+v want %+v", got, listArgs)
			}
			assertJSONKeys(t, cp.Args, "id", "path")
		case "read_file":
			var got FileReadArgs
			if err := json.Unmarshal(cp.Args, &got); err != nil {
				t.Fatal(err)
			}
			if got != readArgs {
				t.Fatalf("read args=%+v want %+v", got, readArgs)
			}
			assertJSONKeys(t, cp.Args, "id", "path")
		}
	}

	listResult := FileListResult{
		Root: "/repo",
		Path: "internal",
		Entries: []FileEntry{{
			Name:    "protocol.go",
			Path:    "internal/commander/protocol.go",
			Kind:    "file",
			Size:    123,
			ModTime: "2026-06-16T10:00:00Z",
		}},
	}
	listResultJSON := mustMarshal(t, listResult)
	assertJSONKeys(t, listResultJSON, "root", "path", "entries")
	var listResultBody struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(listResultJSON, &listResultBody); err != nil {
		t.Fatal(err)
	}
	if len(listResultBody.Entries) != 1 {
		t.Fatalf("entries len=%d want 1", len(listResultBody.Entries))
	}
	assertJSONKeys(t, listResultBody.Entries[0], "name", "path", "kind", "size", "mod_time")
	var gotListResult FileListResult
	if err := json.Unmarshal(listResultJSON, &gotListResult); err != nil {
		t.Fatal(err)
	}
	if gotListResult.Root != listResult.Root ||
		gotListResult.Path != listResult.Path ||
		len(gotListResult.Entries) != 1 ||
		gotListResult.Entries[0] != listResult.Entries[0] {
		t.Fatalf("list result=%+v want %+v", gotListResult, listResult)
	}

	readResult := FileReadResult{
		Path:     "go.mod",
		Size:     456,
		MIME:     "text/plain; charset=utf-8",
		Binary:   true,
		TooLarge: true,
		Content:  "module github.com/yourorg/multi-agent",
	}
	readResultJSON := mustMarshal(t, readResult)
	assertJSONKeys(t, readResultJSON, "path", "size", "mime", "binary", "too_large", "content")
	var gotReadResult FileReadResult
	if err := json.Unmarshal(readResultJSON, &gotReadResult); err != nil {
		t.Fatal(err)
	}
	if gotReadResult != readResult {
		t.Fatalf("read result=%+v want %+v", gotReadResult, readResult)
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

func TestEventPayloadStatusCodePreservesExtra(t *testing.T) {
	extra := json.RawMessage(`{"source":"test"}`)
	payload := mustMarshal(t, EventPayload{
		EventKind:  "status",
		Text:       "starting codex",
		Extra:      extra,
		StatusCode: "starting",
	})
	assertJSONKeys(t, payload, "event_kind", "text", "extra", "status_code")

	var got EventPayload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.EventKind != "status" || got.Text != "starting codex" || got.StatusCode != "starting" || string(got.Extra) != string(extra) {
		t.Fatalf("payload=%+v extra=%s", got, got.Extra)
	}
}

// TestEnvelope_ErrorCodesEnumerated pins the spec's error codes so typos at
// call sites fail tests instead of silently becoming protocol drift.
func TestEnvelope_ErrorCodesEnumerated(t *testing.T) {
	codes := map[string]string{
		"session_not_found":       ErrCodeSessionNotFound,
		"backend_unavailable":     ErrCodeBackendUnavailable,
		"schema_version_mismatch": ErrCodeSchemaVersionMismatch,
		"invalid_request":         ErrCodeInvalidRequest,
		"internal":                ErrCodeInternal,
		"daemon_upgrade_required": ErrCodeDaemonUpgradeRequired,
	}
	for want, got := range codes {
		if got != want {
			t.Errorf("error code=%q want %q", got, want)
		}
	}
}

// TestSchemaVersion_IsOne pins that PR-2 ships at schema_version = 1.
func TestSchemaVersion_IsOne(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion=%d want 1", SchemaVersion)
	}
	if MaxFilePreviewBytes != 2*1024*1024 {
		t.Fatalf("MaxFilePreviewBytes=%d want %d", MaxFilePreviewBytes, 2*1024*1024)
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

func assertJSONKeys(t *testing.T, b []byte, wantKeys ...string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range wantKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("json keys=%v missing %q", keys(got), key)
		}
	}
	if len(got) != len(wantKeys) {
		t.Fatalf("json keys=%v want %v", keys(got), wantKeys)
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func TestRegisterPayloadShortIDRoundTrip(t *testing.T) {
	in := RegisterPayload{SchemaVersion: SchemaVersion, Kind: "codex", ShortID: "drv-1"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out RegisterPayload
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ShortID != "drv-1" {
		t.Fatalf("ShortID = %q, want drv-1", out.ShortID)
	}
}
