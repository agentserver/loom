package commander

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type fakeBackend struct {
	listFn   func(ctx context.Context) ([]agentbackend.Session, error)
	getFn    func(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error)
}

func (f *fakeBackend) Kind() agentbackend.Kind { return agentbackend.KindClaude }
func (f *fakeBackend) Run(_ context.Context, _ executor.Task, _ executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *fakeBackend) RunResume(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
	if f.resumeFn == nil {
		return executor.Result{}, nil
	}
	return f.resumeFn(ctx, id, answer, sink)
}
func (f *fakeBackend) LLM() agentbackend.LLMRunner                { return nil }
func (f *fakeBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (f *fakeBackend) Detect(_ context.Context) error             { return nil }
func (f *fakeBackend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx)
}
func (f *fakeBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	if f.getFn == nil {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	return f.getFn(ctx, id)
}

type captureSink struct {
	events []captured
	closed bool
}

type captured struct {
	kind string
	data string
}

func (c *captureSink) Write(kind, data string) {
	c.events = append(c.events, captured{kind: kind, data: data})
}

func (c *captureSink) Close() { c.closed = true }

// TestHandler_ListSessionsForwards pins that Handler.ListSessions returns
// whatever the Backend returns, with no filtering or mutation.
func TestHandler_ListSessionsForwards(t *testing.T) {
	want := []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude}}
	h := &Handler{Backend: &fakeBackend{
		listFn: func(_ context.Context) ([]agentbackend.Session, error) { return want, nil },
	}}
	got, err := h.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("got=%+v", got)
	}
}

// TestHandler_GetSessionForwards pins the descriptor plus message slice flow.
func TestHandler_GetSessionForwards(t *testing.T) {
	wantS := agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude}
	wantM := []agentbackend.SessionMessage{{Role: "user", Text: "hi"}}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			if id != "s1" {
				t.Errorf("id=%q", id)
			}
			return wantS, wantM, nil
		},
	}}
	s, m, err := h.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s1" || len(m) != 1 || m[0].Text != "hi" {
		t.Fatalf("s=%+v m=%+v", s, m)
	}
}

// TestHandler_GetSessionPropagatesErrSessionNotFound pins that the PR-1
// sentinel survives so transports can emit session_not_found.
func TestHandler_GetSessionPropagatesErrSessionNotFound(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
		},
	}}
	_, _, err := h.GetSession(context.Background(), "missing")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v", err)
	}
}

// TestHandler_SessionTurnStreamsAndReturns pins that the sink receives each
// RunResume event in order and the final Result is returned to the caller.
func TestHandler_SessionTurnStreamsAndReturns(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
			if id != "s1" {
				t.Errorf("id=%q", id)
			}
			if !strings.Contains(answer, "do thing") {
				t.Errorf("answer=%q", answer)
			}
			sink.Write("chunk", "step one")
			sink.Write("chunk", "step two")
			sink.Write("capability", "added foo")
			sink.Close()
			return executor.Result{Summary: "done", SessionID: id}, nil
		},
	}}
	sink := &captureSink{}
	res, err := h.SessionTurn(context.Background(), "s1", "do thing", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "done" {
		t.Errorf("summary=%q", res.Summary)
	}
	if !sink.closed {
		t.Errorf("sink not closed")
	}
	if len(sink.events) != 3 || sink.events[0].kind != "chunk" || sink.events[2].kind != "capability" {
		t.Errorf("events=%+v", sink.events)
	}
}

// TestHandler_SessionTurnRespectsContextCancel pins that handler does not
// swallow context errors, which daemon shutdown relies on.
func TestHandler_SessionTurnRespectsContextCancel(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(ctx context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			select {
			case <-ctx.Done():
				return executor.Result{}, ctx.Err()
			case <-time.After(5 * time.Second):
				t.Errorf("backend not cancelled")
				return executor.Result{}, nil
			}
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.SessionTurn(ctx, "s1", "p", &captureSink{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
