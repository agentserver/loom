package commanderhub

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestSSE_EventChunkDecoded(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	payload, _ := jsonMarshal(commander.EventPayload{EventKind: "chunk", Text: "hello"})
	s.writeEnvelope(commander.Envelope{Type: "event", ID: "c1", Payload: payload})

	body := rec.Body.String()
	require.Contains(t, body, "event: chunk\n")
	require.Contains(t, body, `data: {"text":"hello"}`)
}

func TestSSE_StatusPreservesStatusCodeAndExtra(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	extra := json.RawMessage(`{"source":"daemon"}`)
	payload, _ := jsonMarshal(commander.EventPayload{
		EventKind:  "status",
		Text:       "codex running",
		Extra:      extra,
		StatusCode: agentbackend.StatusAnswering,
	})
	s.writeEnvelope(commander.Envelope{Type: "event", ID: "c1", Payload: payload})

	body := rec.Body.String()
	require.Contains(t, body, "event: status\n")
	require.Contains(t, body, `"text":"codex running"`)
	require.Contains(t, body, `"status_code":"answering"`)
	require.Contains(t, body, `"extra":{"source":"daemon"}`)
}

func TestSSE_CommandResultForwardedAsDone(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	// daemon already marshalled this; observer forwards verbatim (no marshalTurnResult).
	s.writeEnvelope(commander.Envelope{Type: "command_result", ID: "c1", Payload: []byte(`{"result":{"summary":"done"}}`)})
	require.Contains(t, rec.Body.String(), "event: done\n")
	require.Contains(t, rec.Body.String(), `data: {"result":{"summary":"done"}}`)
}

func TestSSE_ErrorForwardedAndObserverSynthesized(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	payload, _ := jsonMarshal(commander.ErrorPayload{Code: "session_not_found", Message: "nope"})
	s.writeEnvelope(commander.Envelope{Type: "error", ID: "c1", Payload: payload})
	require.Contains(t, rec.Body.String(), "event: error\n")
	require.Contains(t, rec.Body.String(), "session_not_found")

	// observer-synthesized (not from daemon):
	rec2 := httptest.NewRecorder()
	s2 := newSSEWriter(rec2)
	s2.emitError("timeout", "30s no terminal frame")
	require.True(t, strings.Contains(rec2.Body.String(), "event: error\n"), rec2.Body.String())
	require.Contains(t, rec2.Body.String(), "timeout")
}
