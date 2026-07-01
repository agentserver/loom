package commanderhub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
)

// TestHub_Close_DrainsLocalDaemons verifies that Hub.Close:
//  1. Causes in-flight daemon WebSocket connections to be closed (dc.done fires).
//  2. New WS upgrade attempts after Close return 503.
//  3. Goroutine count does not leak (delta between before/after is small).
func TestHub_Close_DrainsLocalDaemons(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	// Snapshot goroutine count before daemon connect.
	runtime.GC()
	goroutinesBefore := runtime.NumGoroutine()

	// Dial the daemon WS manually so we can observe its close.
	hdr := wsDialHeader("tok-alice")
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, hdr)
	require.NoError(t, err, "dial daemon WS")

	// Send register frame.
	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "test-daemon",
	})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload}))

	// Wait for the ack frame (confirms daemon is fully admitted).
	var ack commander.Envelope
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "ack", ack.Type, "expected ack after register")

	// Verify daemon is in the local registry.
	o := owner{userID: "alice", workspaceID: "W1"}
	waitFor(t, func() bool {
		return len(hub.reg.daemons(o)) == 1
	}, time.Second, "daemon visible in local registry")

	// Call Close with a 3-second deadline.
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = hub.Close(closeCtx)
	require.NoError(t, err, "hub.Close should not return an error")

	// The WS connection should be closed from the server side.
	// drainAllLocalDaemons sends an observer_draining event then closes the conn.
	// Read until we get an error (may consume the observer_draining event first).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var closedByServer bool
	for i := 0; i < 10; i++ {
		var dummy commander.Envelope
		if err := conn.ReadJSON(&dummy); err != nil {
			closedByServer = true
			break
		}
	}
	require.True(t, closedByServer, "expected WS to be closed by server after hub.Close")

	// After Close, the local registry should be empty (daemon defers ran).
	waitFor(t, func() bool {
		return len(hub.reg.daemons(o)) == 0
	}, time.Second, "local registry cleared after Close")

	// New WS upgrade attempts must return 503 (draining).
	conn2, resp, dialErr := websocket.DefaultDialer.DialContext(context.Background(), wsURL, hdr)
	if conn2 != nil {
		conn2.Close()
	}
	// Either the dial fails with a non-101 (including 503) or the response code is 503.
	if dialErr == nil {
		t.Fatal("expected dial to fail after hub.Close, but it succeeded")
	}
	if resp != nil {
		require.Equal(t, 503, resp.StatusCode, "expected 503 after hub is draining")
	}

	// Goroutine leak check: allow a small window for defers to complete.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	goroutinesAfter := runtime.NumGoroutine()
	delta := goroutinesAfter - goroutinesBefore
	// Allow up to 5 extra goroutines (test runtime overhead, GC goroutines, etc.).
	require.LessOrEqual(t, delta, 5,
		"goroutine leak: before=%d after=%d delta=%d", goroutinesBefore, goroutinesAfter, delta)
}
