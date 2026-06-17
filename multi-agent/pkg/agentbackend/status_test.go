package agentbackend

import "testing"

type statusCaptureSink struct {
	events []struct {
		kind string
		text string
	}
	statuses []struct {
		code string
		text string
	}
}

func (s *statusCaptureSink) Write(kind, text string) {
	s.events = append(s.events, struct {
		kind string
		text string
	}{kind: kind, text: text})
}

func (s *statusCaptureSink) Close() {}

func (s *statusCaptureSink) WriteStatus(code, text string) {
	s.statuses = append(s.statuses, struct {
		code string
		text string
	}{code: code, text: text})
}

type plainCaptureSink struct {
	kind string
	text string
}

func (s *plainCaptureSink) Write(kind, text string) { s.kind, s.text = kind, text }
func (s *plainCaptureSink) Close()                  {}

func TestWriteStatusUsesStructuredSink(t *testing.T) {
	sink := &statusCaptureSink{}
	WriteStatus(sink, StatusStarting, "starting codex")
	if len(sink.statuses) != 1 {
		t.Fatalf("statuses=%+v", sink.statuses)
	}
	if sink.statuses[0].code != StatusStarting || sink.statuses[0].text != "starting codex" {
		t.Fatalf("status=%+v", sink.statuses[0])
	}
	if len(sink.events) != 0 {
		t.Fatalf("plain events=%+v", sink.events)
	}
}

func TestWriteStatusFallsBackToPlainStatusEvent(t *testing.T) {
	sink := &plainCaptureSink{}
	WriteStatus(sink, StatusAnswering, "codex running")
	if sink.kind != "status" || sink.text != "codex running" {
		t.Fatalf("plain status=(%q,%q)", sink.kind, sink.text)
	}
}
