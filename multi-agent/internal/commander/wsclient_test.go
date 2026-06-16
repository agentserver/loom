package commander

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type fakeObserver struct {
	t                        *testing.T
	upgrader                 websocket.Upgrader
	mu                       sync.Mutex
	writeMu                  sync.Mutex
	conns                    []*websocket.Conn
	received                 []Envelope
	bearer                   string
	rejectAuth               bool
	sendAck                  bool
	stopReadingAfterRegister bool
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
			sendAck := f.sendAck && env.Type == "register"
			f.mu.Unlock()
			if sendAck {
				f.writeMu.Lock()
				_ = conn.WriteJSON(Envelope{Type: "ack"})
				f.writeMu.Unlock()
			}
			if f.stopReadingAfterRegister && env.Type == "register" {
				return
			}
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
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
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

func TestWSClient_LinkedRequiresObserverAck(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "token-abc",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	waitFor(t, func() bool { return fo.registerCount() >= 1 }, time.Second)
	if c.Linked() {
		t.Fatal("Linked should remain false until observer ack")
	}
	cancel()
	fo.closeAll()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	fo = newFakeObserver(t)
	fo.sendAck = true
	srv = httptest.NewServer(fo.handler())
	defer srv.Close()
	c = NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "token-abc",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel = context.WithCancel(context.Background())
	errCh = make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitFor(t, c.Linked, time.Second)
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
		sawStatus := false
		sawResult := false
		for _, env := range fo.frames() {
			if env.ID != "turn-1" {
				continue
			}
			if env.Type == "event" {
				eventCount++
				var ep EventPayload
				if err := json.Unmarshal(env.Payload, &ep); err == nil && ep.EventKind == "status" {
					sawStatus = true
				}
			}
			if env.Type == "command_result" {
				sawResult = strings.Contains(string(env.Payload), `"result":`) &&
					strings.Contains(string(env.Payload), `"summary":"done"`)
			}
		}
		return eventCount >= 3 && sawStatus && sawResult
	}, 2*time.Second)
}

func TestWSClient_CancelsTurnWhenConnectionDrops(t *testing.T) {
	turnStarted := make(chan struct{})
	turnCanceled := make(chan struct{})
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:        observerWSURL(srv),
		ProxyToken: "t",
		Register:   RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler: &Handler{Backend: &fakeBackend{
			resumeFn: func(ctx context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
				close(turnStarted)
				<-ctx.Done()
				close(turnCanceled)
				return executor.Result{}, ctx.Err()
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
		ID:   "turn-cancel",
		Payload: jsonRaw(t, CommandPayload{
			Command: "session_turn",
			Args:    jsonRaw(t, SessionTurnArgs{ID: "s1", Prompt: "do"}),
		}),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		select {
		case <-turnStarted:
			return true
		default:
			return false
		}
	}, time.Second)
	fo.closeAll()
	waitFor(t, func() bool {
		select {
		case <-turnCanceled:
			return true
		default:
			return false
		}
	}, time.Second)
}

func TestWSClient_SessionTurnSameSessionSerialized(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var calls atomic.Int32

	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:        observerWSURL(srv),
		ProxyToken: "t",
		Register:   RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler: &Handler{Backend: &fakeBackend{
			resumeFn: func(_ context.Context, id, _ string, _ executor.Sink) (executor.Result, error) {
				if id != "same-session" {
					t.Errorf("id=%q", id)
				}
				switch calls.Add(1) {
				case 1:
					close(firstStarted)
					<-releaseFirst
				case 2:
					close(secondStarted)
				default:
					t.Errorf("unexpected extra call")
				}
				return executor.Result{Summary: "ok", SessionID: id}, nil
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
	for _, id := range []string{"turn-1", "turn-2"} {
		if err := fo.Send(Envelope{
			Type: "command",
			ID:   id,
			Payload: jsonRaw(t, CommandPayload{
				Command: "session_turn",
				Args:    jsonRaw(t, SessionTurnArgs{ID: "same-session", Prompt: id}),
			}),
		}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			select {
			case <-firstStarted:
				return true
			default:
				return false
			}
		}, time.Second)
	}

	select {
	case <-secondStarted:
		t.Fatal("second turn started before first same-session turn completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	waitFor(t, func() bool {
		select {
		case <-secondStarted:
			return true
		default:
			return false
		}
	}, time.Second)
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

func TestWSClient_ReconnectsWhenPeerStopsAnsweringControlFrames(t *testing.T) {
	fo := newFakeObserver(t)
	fo.sendAck = true
	fo.stopReadingAfterRegister = true
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:            observerWSURL(srv),
		ProxyToken:     "t",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   20 * time.Millisecond,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	waitFor(t, func() bool { return fo.registerCount() >= 2 }, time.Second)
}

func TestWSClient_ReconnectsAfterOversizedFrame(t *testing.T) {
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
		MaxBackoff:     20 * time.Millisecond,
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
	_ = fo.Send(Envelope{
		Type: "command",
		ID:   "too-large",
		Payload: jsonRaw(t, struct {
			Command string          `json:"command"`
			Args    json.RawMessage `json:"args"`
			Padding string          `json:"padding"`
		}{
			Command: "list_sessions",
			Args:    json.RawMessage(`{}`),
			Padding: strings.Repeat("x", 2*1024*1024),
		}),
	})
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Run(ctx)
	if !errors.Is(err, ErrObserverUnauthorized) {
		t.Fatalf("err=%v want ErrObserverUnauthorized", err)
	}
	for _, env := range fo.frames() {
		if env.Type == "register" {
			t.Fatalf("registered despite 401")
		}
	}
}

func TestWSClient_TurnLockReleasedAfterLastWaiter(t *testing.T) {
	c := NewWSClient(WSConfig{})
	firstUnlock := c.lockTurn("s1")
	secondLocked := make(chan struct{})
	releaseSecond := make(chan struct{})
	secondDone := make(chan struct{})

	go func() {
		unlock := c.lockTurn("s1")
		close(secondLocked)
		<-releaseSecond
		unlock()
		close(secondDone)
	}()

	select {
	case <-secondLocked:
		t.Fatal("second turn acquired lock before first unlock")
	case <-time.After(50 * time.Millisecond):
	}
	firstUnlock()
	waitFor(t, func() bool {
		select {
		case <-secondLocked:
			return true
		default:
			return false
		}
	}, time.Second)

	c.turnMu.Lock()
	during := len(c.turnLocks)
	c.turnMu.Unlock()
	if during != 1 {
		t.Fatalf("turnLocks len while waiter holds lock=%d want 1", during)
	}

	close(releaseSecond)
	waitFor(t, func() bool {
		select {
		case <-secondDone:
			return true
		default:
			return false
		}
	}, time.Second)
	c.turnMu.Lock()
	got := len(c.turnLocks)
	c.turnMu.Unlock()
	if got != 0 {
		t.Fatalf("turnLocks len after final unlock=%d want 0", got)
	}
}

func TestReconnectBackoffResetsAfterStableConnection(t *testing.T) {
	got := nextReconnectBackoff(200*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond, 2*time.Second, time.Second)
	if got != 10*time.Millisecond {
		t.Fatalf("stable connection backoff=%v want 10ms", got)
	}
	got = nextReconnectBackoff(10*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond, 100*time.Millisecond, time.Second)
	if got != 20*time.Millisecond {
		t.Fatalf("short connection backoff=%v want 20ms", got)
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
