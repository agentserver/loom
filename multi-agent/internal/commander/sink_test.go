package commander

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
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
