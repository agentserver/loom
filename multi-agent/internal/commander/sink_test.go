package commander

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestSSESink_WriteEmitsEventDataPair pins the SSE wire format: each
// Sink.Write produces "event: <kind>\ndata: <json>\n\n".
func TestSSESink_WriteEmitsEventDataPair(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSESink(rec)
	s.Write("chunk", "hello")
	s.Close()
	body := rec.Body.String()
	if !strings.Contains(body, "event: chunk\n") {
		t.Errorf("missing event line: %q", body)
	}
	if !strings.Contains(body, `data: {"text":"hello"}`) {
		t.Errorf("missing data line: %q", body)
	}
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), "}") {
		t.Errorf("body should end with closed json: %q", body)
	}
}

func TestSSESink_EmitDoneWritesResult(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSESink(rec)
	s.EmitDone([]byte(`{"result":{"summary":"ok"}}`))
	body := rec.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Errorf("missing done event: %q", body)
	}
	if !strings.Contains(body, `data: {"result":{"summary":"ok"}}`) {
		t.Errorf("missing done payload: %q", body)
	}
}

type fakeFlushWriter struct {
	bytes.Buffer
	flushed int
}

func (f *fakeFlushWriter) Flush() { f.flushed++ }

func TestSSESink_FlushedOnEachWrite(t *testing.T) {
	f := &fakeFlushWriter{}
	s := newSSESink(f)
	s.Write("chunk", "x")
	s.Write("chunk", "y")
	s.Close()
	if f.flushed < 2 {
		t.Errorf("flushed=%d want >=2", f.flushed)
	}
}

func TestSSESinkWriteStatusIncludesStatusCode(t *testing.T) {
	var b strings.Builder
	sink := newSSESink(&b)
	sink.WriteStatus(agentbackend.StatusAnswering, "codex running")
	got := b.String()
	if !strings.Contains(got, "event: status") || !strings.Contains(got, `"status_code":"answering"`) {
		t.Fatalf("sse=%s", got)
	}
}

// TestWSSink_WriteForwardsAsEventEnvelope pins that each sink Write produces
// an event envelope with the right id and payload.
func TestWSSink_WriteForwardsAsEventEnvelope(t *testing.T) {
	var got []Envelope
	send := func(e Envelope) error {
		got = append(got, e)
		return nil
	}
	s := newWSSink("cmd-1", send)
	s.Write("chunk", "hello")
	s.Write("capability", "added foo")
	s.Close()

	if len(got) != 2 {
		t.Fatalf("got %d envelopes", len(got))
	}
	if got[0].Type != "event" || got[0].ID != "cmd-1" {
		t.Errorf("envelope[0]=%+v", got[0])
	}
	var p1 EventPayload
	if err := json.Unmarshal(got[0].Payload, &p1); err != nil {
		t.Fatal(err)
	}
	if p1.EventKind != "chunk" || p1.Text != "hello" {
		t.Errorf("payload[0]=%+v", p1)
	}
}

func TestWSSinkWriteStatusIncludesStatusCode(t *testing.T) {
	var sent Envelope
	sink := newWSSink("cmd-1", func(env Envelope) error {
		sent = env
		return nil
	})
	sink.WriteStatus(agentbackend.StatusStarting, "starting codex")

	if sent.Type != "event" || sent.ID != "cmd-1" {
		t.Fatalf("envelope=%+v", sent)
	}
	var payload EventPayload
	if err := json.Unmarshal(sent.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.EventKind != "status" || payload.Text != "starting codex" || payload.StatusCode != agentbackend.StatusStarting {
		t.Fatalf("payload=%+v", payload)
	}
}
