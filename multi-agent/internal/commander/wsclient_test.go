package commander

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type fakeObserver struct {
	t          *testing.T
	upgrader   websocket.Upgrader
	mu         sync.Mutex
	conns      []*websocket.Conn
	received   []Envelope
	bearer     string
	rejectAuth bool
}

func newFakeObserver(t *testing.T) *fakeObserver {
	return &fakeObserver{t: t, upgrader: websocket.Upgrader{}}
}

func (f *fakeObserver) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.bearer = r.Header.Get("Authorization")
		rejectAuth := f.rejectAuth
		f.mu.Unlock()
		if rejectAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		for {
			var env Envelope
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
			f.mu.Lock()
			f.received = append(f.received, env)
			f.mu.Unlock()
		}
	})
}

func (f *fakeObserver) Send(env Envelope) error {
	f.mu.Lock()
	if len(f.conns) == 0 {
		f.mu.Unlock()
		return nil
	}
	conn := f.conns[len(f.conns)-1]
	f.mu.Unlock()
	return conn.WriteJSON(env)
}

func (f *fakeObserver) closeAll() {
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (f *fakeObserver) frames() []Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Envelope(nil), f.received...)
}

func (f *fakeObserver) authHeader() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bearer
}

func (f *fakeObserver) registerCount() int {
	count := 0
	for _, env := range f.frames() {
		if env.Type == "register" {
			count++
		}
	}
	return count
}

func observerWSURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
}

func TestWSClient_DialsAndRegisters(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()

	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "token-abc",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude", DisplayName: "test"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	waitFor(t, func() bool { return fo.registerCount() >= 1 }, time.Second)
	if got := fo.authHeader(); got != "Bearer token-abc" {
		t.Errorf("auth header=%q", got)
	}
	frames := fo.frames()
	if frames[0].Type != "register" {
		t.Fatalf("first frame=%+v", frames[0])
	}
	cancel()
	fo.closeAll()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestWSClient_DispatchesCommandAndReturnsResult(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:        observerWSURL(srv),
		ProxyToken: "t",
		Register:   RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler: &Handler{Backend: &fakeBackend{
			listFn: func(_ context.Context) ([]agentbackend.Session, error) {
				return []agentbackend.Session{{ID: "s1"}}, nil
			},
		}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	waitFor(t, func() bool { return fo.registerCount() >= 1 }, time.Second)
	if err := fo.Send(Envelope{
		Type:    "command",
		ID:      "cmd-1",
		Payload: jsonRaw(t, CommandPayload{Command: "list_sessions"}),
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		for _, env := range fo.frames() {
			if env.Type != "command_result" || env.ID != "cmd-1" {
				continue
			}
			var body struct {
				Sessions []agentbackend.Session `json:"sessions"`
			}
			if err := json.Unmarshal(env.Payload, &body); err != nil {
				return false
			}
			return len(body.Sessions) == 1 && body.Sessions[0].ID == "s1"
		}
		return false
	}, time.Second)
}

func TestWSClient_TurnCommandStreamsEventsAndResult(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:        observerWSURL(srv),
		ProxyToken: "t",
		Register:   RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler: &Handler{Backend: &fakeBackend{
			resumeFn: func(_ context.Context, id, prompt string, sink executor.Sink) (executor.Result, error) {
				if id != "s1" || prompt != "do" {
					t.Errorf("id=%q prompt=%q", id, prompt)
				}
				sink.Write("chunk", "one")
				sink.Write("chunk", "two")
				sink.Close()
				return executor.Result{Summary: "done", SessionID: "s1"}, nil
			},
		}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	waitFor(t, func() bool { return fo.registerCount() >= 1 }, time.Second)
	if err := fo.Send(Envelope{
		Type: "command",
		ID:   "turn-1",
		Payload: jsonRaw(t, CommandPayload{
			Command: "session_turn",
			Args:    jsonRaw(t, SessionTurnArgs{ID: "s1", Prompt: "do"}),
		}),
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		eventCount := 0
		sawResult := false
		for _, env := range fo.frames() {
			if env.ID != "turn-1" {
				continue
			}
			if env.Type == "event" {
				eventCount++
			}
			if env.Type == "command_result" {
				sawResult = strings.Contains(string(env.Payload), `"result":`) &&
					strings.Contains(string(env.Payload), `"summary":"done"`)
			}
		}
		return eventCount >= 2 && sawResult
	}, 2*time.Second)
}

func TestWSClient_Heartbeat(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "t",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   20 * time.Millisecond,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	waitFor(t, func() bool {
		for _, env := range fo.frames() {
			if env.Type == "heartbeat" {
				return true
			}
		}
		return false
	}, time.Second)
}

func TestWSClient_ReconnectsOnDrop(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "t",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	waitFor(t, func() bool { return fo.registerCount() >= 1 }, time.Second)
	fo.closeAll()
	waitFor(t, func() bool { return fo.registerCount() >= 2 }, 2*time.Second)
}

func TestWSClient_RejectsUnauthorized(t *testing.T) {
	fo := newFakeObserver(t)
	fo.rejectAuth = true
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "bad",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)
	for _, env := range fo.frames() {
		if env.Type == "register" {
			t.Fatalf("registered despite 401")
		}
	}
}

func TestWSClient_StopsOnSchemaMismatch(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "t",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	waitFor(t, func() bool { return fo.registerCount() >= 1 }, time.Second)
	if err := fo.Send(Envelope{
		Type:    "error",
		Payload: jsonRaw(t, ErrorPayload{Code: ErrCodeSchemaVersionMismatch, Message: "upgrade"}),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrSchemaVersionMismatch) {
			t.Fatalf("err=%v want ErrSchemaVersionMismatch", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on schema mismatch")
	}
}

func jsonRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
