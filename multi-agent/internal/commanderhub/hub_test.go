package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
)

// fakeResolver maps a bearer token to a fixed Identity. Tests use it to drive
// subject/workspace isolation without a real agentserver whoami round-trip.
type fakeResolver struct {
	mu  map[string]identity.Identity // token → identity
	err error
}

func (f *fakeResolver) Resolve(_ context.Context, token string) (identity.Identity, error) {
	if f.err != nil {
		return identity.Identity{}, f.err
	}
	if ident, ok := f.mu[token]; ok {
		return ident, nil
	}
	return identity.Identity{}, identity.ErrInvalid
}

func newHubServer(t *testing.T, resolver identity.Resolver) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewHub(resolver))
}

func wsClient(t *testing.T, srv *httptest.Server, token string) *commander.WSClient {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	return commander.NewWSClient(commander.WSConfig{
		URL:            wsURL,
		ProxyToken:     token,
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "test-daemon"},
		Handler:        &commander.Handler{}, // not exercised in hub tests
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
}

// waitFor calls cond every 10ms until it returns true or the timeout fires.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitFor: %s within %v", msg, timeout)
}

// TestHub_AcksRegisterAndAdmitsDaemon: daemon dials with a valid token → hub
// resolves (alice, W1), admits the daemon, sends ack. Linked flips true on the
// WSClient only after ack (PR-2 contract).
func TestHub_AcksRegisterAndAdmitsDaemon(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	srv := newHubServer(t, resolver)
	defer srv.Close()

	c := wsClient(t, srv, "tok-alice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	waitFor(t, func() bool { return c.Linked() }, time.Second, "WSClient linked")
	require.True(t, c.Linked(), "daemon only links after observer ack")

	cancel()
	<-errCh
}

// TestHub_401OnUnknownToken: unknown token → resolver ErrInvalid → 401, no
// upgrade. WSClient treats 401 as terminal ErrObserverUnauthorized (PR-2).
func TestHub_401OnUnknownToken(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}} // nothing resolves
	srv := newHubServer(t, resolver)
	defer srv.Close()

	c := wsClient(t, srv, "bogus")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Run(ctx)
	require.Error(t, err)
	require.True(t, errors.Is(err, commander.ErrObserverUnauthorized),
		"want ErrObserverUnauthorized, got %v", err)
}

// TestHub_SchemaMismatchRejected: daemon sends wrong schema_version → hub writes
// error{schema_version_mismatch} and closes; WSClient treats it as terminal.
func TestHub_SchemaMismatchRejected(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	srv := newHubServer(t, resolver)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	hdr := wsDialHeader("tok-alice")
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, hdr)
	require.NoError(t, err)
	defer conn.Close()

	// Send a register with the wrong schema version.
	reg, _ := jsonMarshal(commander.RegisterPayload{SchemaVersion: commander.SchemaVersion + 999, Kind: "claude"})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: reg}))

	// Expect an error envelope then close.
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, jsonUnmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeSchemaVersionMismatch, ep.Code)
}

// TestHub_DaemonsListsOnlyOwnOwner: two daemons under different owners register;
// registry.daemons(alice) sees only alice's.
func TestHub_DaemonsListsOnlyOwnOwner(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
		"tok-bob":   {UserID: "bob", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	// Simulate two admitted daemons by adding directly (admission path tested
	// above; here we test the registry snapshot an HTTP handler would call).
	hub.reg.add(&daemonConn{id: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
	hub.reg.add(&daemonConn{id: "b1", owner: owner{"bob", "W1"}, displayName: "bob-laptop", kind: "codex"})

	infos := hub.reg.daemons(owner{"alice", "W1"})
	require.Len(t, infos, 1)
	require.Equal(t, "a1", infos[0].DaemonID)
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func wsDialHeader(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}
