package commander

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type flushable interface {
	Flush()
}

// sseSink adapts executor.Sink writes to SSE event lines.
type sseSink struct {
	w       io.Writer
	f       flushable
	written bool
}

func newSSESink(w io.Writer) *sseSink {
	s := &sseSink{w: w}
	if f, ok := w.(flushable); ok {
		s.f = f
	}
	return s
}

func (s *sseSink) Write(eventType, data string) {
	payload, _ := json.Marshal(map[string]string{"text": data})
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, payload)
	s.written = true
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *sseSink) WriteStatus(statusCode, text string) {
	payload, _ := json.Marshal(struct {
		Text       string `json:"text,omitempty"`
		StatusCode string `json:"status_code,omitempty"`
	}{Text: text, StatusCode: statusCode})
	fmt.Fprintf(s.w, "event: status\ndata: %s\n\n", payload)
	s.written = true
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *sseSink) Close() {}

func (s *sseSink) EmitDone(body []byte) {
	fmt.Fprintf(s.w, "event: done\ndata: %s\n\n", body)
	s.written = true
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *sseSink) EmitError(code, message string) {
	payload, _ := json.Marshal(ErrorPayload{Code: code, Message: message})
	fmt.Fprintf(s.w, "event: error\ndata: %s\n\n", payload)
	s.written = true
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *sseSink) Written() bool { return s.written }

var _ executor.Sink = (*sseSink)(nil)
var _ agentbackend.StatusSink = (*sseSink)(nil)

// wsSink adapts executor.Sink writes to event envelopes sent by WSClient.
type wsSink struct {
	cmdID string
	send  func(Envelope) error
}

func newWSSink(cmdID string, send func(Envelope) error) *wsSink {
	return &wsSink{cmdID: cmdID, send: send}
}

func (s *wsSink) Write(eventType, data string) {
	payload, _ := json.Marshal(EventPayload{EventKind: eventType, Text: data})
	_ = s.send(Envelope{Type: "event", ID: s.cmdID, Payload: payload})
}

func (s *wsSink) WriteStatus(statusCode, text string) {
	payload, _ := json.Marshal(EventPayload{EventKind: "status", Text: text, StatusCode: statusCode})
	_ = s.send(Envelope{Type: "event", ID: s.cmdID, Payload: payload})
}

func (s *wsSink) Close() {}

var _ executor.Sink = (*wsSink)(nil)
var _ agentbackend.StatusSink = (*wsSink)(nil)
