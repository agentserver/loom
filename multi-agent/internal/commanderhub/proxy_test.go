package commanderhub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// tbBackend is a minimal agentbackend.Backend fake for commanderhub tests. The
// WSClient (real daemon) is wired to it; listFn/getFn/resumeFn drive behavior.
type tbBackend struct {
	listFn   func(context.Context) ([]agentbackend.Session, error)
	getFn    func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(context.Context, string, string, executor.Sink) (executor.Result, error)
}

func (b *tbBackend) Kind() agentbackend.Kind { return agentbackend.KindClaude }
func (b *tbBackend) Run(context.Context, executor.Task, executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (b *tbBackend) RunResume(ctx context.Context, id, ans string, sink executor.Sink) (executor.Result, error) {
	if b.resumeFn != nil {
		return b.resumeFn(ctx, id, ans, sink)
	}
	return executor.Result{}, nil
}
func (b *tbBackend) LLM() agentbackend.LLMRunner                { return nil }
func (b *tbBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (b *tbBackend) Detect(context.Context) error               { return nil }
func (b *tbBackend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	if b.listFn != nil {
		return b.listFn(ctx)
	}
	return nil, nil
}
func (b *tbBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	if b.getFn != nil {
		return b.getFn(ctx, id)
	}
	return agentbackend.Session{}, nil, nil
}

// dialFakeDaemon stands up a Hub server, dials a real WSClient (the "daemon")
// with the given backend + token, waits for ack, and returns the hub, server,
// the owner, and a cleanup func. The assigned daemonID is read back via
// Daemons() by the caller.
func dialFakeDaemon(t *testing.T, resolver *fakeResolver, token string, backend agentbackend.Backend) (*Hub, *httptest.Server, owner, func()) {
	t.Helper()
	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL:        wsURL,
		ProxyToken: token,
		Register:   commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "tester"},
		Handler:    &commander.Handler{Backend: backend},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	ident, _ := resolver.Resolve(ctx, token)
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}
	waitFor(t, func() bool { return c.Linked() }, time.Second, "daemon linked")
	cleanup := func() { cancel(); <-errCh; srv.Close() }
	return hub, srv, o, cleanup
}

// TestProxy_SendCommandListSessions: SendCommand(list_sessions) round-trips to
// the daemon and returns the backend's sessions payload.
func TestProxy_SendCommandListSessions(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1"}, {ID: "s2"}}, nil
		},
	})
	defer cleanup()

	di := hub.reg.daemons(o)
	require.Len(t, di, 1)
	payload, err := hub.SendCommand(context.Background(), o, di[0].DaemonID, "list_sessions", nil)
	require.NoError(t, err)
	require.Contains(t, string(payload), "s1")
	require.Contains(t, string(payload), "s2")
}

// TestProxy_SendCommandCrossOwner404: looking up another owner's daemon fails.
func TestProxy_SendCommandCrossOwner404(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, _, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{})
	defer cleanup()

	_, err := hub.SendCommand(context.Background(), owner{"bob", "W1"}, "any", "list_sessions", nil)
	require.ErrorIs(t, err, ErrDaemonNotFound)
}

// TestProxy_SendCommandStreamTurn: session_turn streams events then a terminal
// command_result on the returned channel.
func TestProxy_SendCommandStreamTurn(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			sink.Write("chunk", "one")
			sink.Write("chunk", "two")
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
	})
	defer cleanup()

	di := hub.reg.daemons(o)
	ch, err := hub.SendCommandStream(context.Background(), o, di[0].DaemonID, "session_turn",
		jsonRaw(t, commander.SessionTurnArgs{ID: "s1", Prompt: "go"}))
	require.NoError(t, err)

	var events, results int
	for env := range ch {
		if env.Type == "event" {
			events++
		}
		if env.Type == "command_result" {
			results++
		}
	}
	require.Equal(t, 2, events)
	require.Equal(t, 1, results)
}

// TestProxy_FanOutSessionsFailOpen: two daemons, one slow (never answers) →
// status timeout/error/disconnected, the other ok; neither blocks the other.
func TestProxy_FanOutSessionsFailOpen(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "fast"}}, nil
		},
	})
	defer cleanup()
	// register a second "daemon" entry under same owner that will never answer
	// (no real conn) → SendCommand hits ErrDaemonGone quickly via the pre-check
	// on the already-closed done chan.
	hub.reg.add(&daemonConn{id: "ghost", owner: o, done: closedDone(), pending: map[string]*pendingEntry{}})

	res := hub.FanOutSessions(context.Background(), o)
	byID := map[string]DaemonSessions{}
	for _, r := range res {
		byID[r.DaemonID] = r
	}
	// the real daemon answered
	realID := hub.reg.daemons(o)
	var realFound bool
	for _, info := range realID {
		if byID[info.DaemonID].Status == "ok" {
			realFound = true
		}
	}
	require.True(t, realFound, "real daemon should report ok")
	require.Contains(t, []string{"error", "disconnected", "timeout"}, byID["ghost"].Status)
}

// --- helpers ---

func jsonRaw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func closedDone() chan struct{} { c := make(chan struct{}); close(c); return c }
