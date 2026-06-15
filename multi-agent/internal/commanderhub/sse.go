package commanderhub

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/yourorg/multi-agent/internal/commander"
)

// sseWriter converts inbound daemon Envelopes to SSE event lines on an HTTP
// response. Content-Type/Cache-Control headers are set on construction. The
// browser reads this with fetch + ReadableStream (not EventSource — POST body).
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	s := &sseWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		s.flusher = f
	}
	return s
}

// writeEnvelope emits one daemon frame:
//   event            → "event: <event_kind>\ndata: {"text":...}"   (decoded)
//   command_result   → "event: done\ndata: <payload verbatim>"      (forwarded)
//   error            → "event: error\ndata: <payload verbatim>"     (forwarded)
// command_result/error payloads are produced by the daemon (PR-2) — observer
// forwards them as-is, so it never imports marshalTurnResult (PR-2 frozen).
func (s *sseWriter) writeEnvelope(env commander.Envelope) {
	switch env.Type {
	case "event":
		var ep commander.EventPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			return
		}
		data, _ := json.Marshal(map[string]string{"text": ep.Text})
		s.emit(ep.EventKind, data)
	case "command_result":
		s.emit("done", env.Payload)
	case "error":
		s.emit("error", env.Payload)
	}
}

// emitError emits an observer-synthesized SSE error (disconnect/timeout), not a
// daemon frame.
func (s *sseWriter) emitError(code, message string) {
	data, _ := json.Marshal(commander.ErrorPayload{Code: code, Message: message})
	s.emit("error", data)
}

func (s *sseWriter) emit(event string, data []byte) {
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
