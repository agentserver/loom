package commanderhub

import (
	"context"
	"encoding/json"
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

// TestDrain_BlocksFutureAdmissions is the regression test for D-fix3 MAJOR #2:
// the /drain endpoint must set h.draining=true (under admitMu) so that any WS
// upgrade attempt arriving after drain completes is rejected with 503.
//
// Before the fix: drainHandler called drainAllLocalDaemons but never set
// h.draining, so daemons could immediately reconnect to the same terminating
// pod while the k8s preStop hook was still running — defeating the drain.
//
// After the fix: drainHandler acquires admitMu, stores draining=true, releases
// admitMu, then calls drainAllLocalDaemons. Any subsequent WS upgrade attempt
// either sees draining=true in the ServeHTTP pre-check (fast path) or sees it
// after acquiring admitMu (slow path), and is rejected with 503.
//
// Test sequence:
//  1. Stand up a hub; connect a daemon and wait for the ack.
//  2. POST loopback drain → 200 OK; wait for the existing daemon's WS to close.
//  3. Attempt a new WS upgrade → must get 503.
//
// Run as: go test -run TestDrain_BlocksFutureAdmissions -race -count=5
func TestDrain_BlocksFutureAdmissions(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	hub := NewHub(resolver)

	// Use a custom mux so we can test both /api/daemon-link and the drain path.
	mux := http.NewServeMux()
	mux.Handle("/api/daemon-link", hub)
	mux.HandleFunc("/api/commander/_internal/drain", hub.drainHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok-alice")

	// --- Step 1: connect a daemon and wait for ack ---
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, hdr)
	require.NoError(t, err, "initial daemon dial must succeed")
	t.Cleanup(func() { conn.Close() })

	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "pre-drain-daemon",
	})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload}))

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack commander.Envelope
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "ack", ack.Type, "must receive ack before proceeding")

	// Confirm daemon is visible in local registry.
	o := owner{userID: "alice", workspaceID: "W1"}
	require.Eventually(t, func() bool {
		return len(hub.reg.daemons(o)) == 1
	}, time.Second, 10*time.Millisecond, "daemon must appear in local registry")

	// --- Step 2: POST loopback drain ---
	drainURL := srv.URL + "/api/commander/_internal/drain"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, drainURL, nil)
	require.NoError(t, err)
	// The httptest.Server sets RemoteAddr in responses, but for our outgoing
	// client request we need the server to see a loopback RemoteAddr.
	// httptest.Server's listener binds to 127.0.0.1 and the client dials
	// 127.0.0.1, so RemoteAddr on the server side is always 127.x — loopback
	// bypass applies automatically.
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "drain endpoint must return 200 OK")

	// draining flag must be set immediately after drain returns.
	require.True(t, hub.draining.Load(), "h.draining must be true after drain endpoint")

	// Wait for the existing WS to be closed by the server (drainAllLocalDaemons
	// sends observer_draining + conn.Close).
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var closedByServer bool
	for i := 0; i < 10; i++ {
		var dummy commander.Envelope
		if err := conn.ReadJSON(&dummy); err != nil {
			closedByServer = true
			break
		}
	}
	require.True(t, closedByServer, "existing daemon WS must be closed by drain")

	// --- Step 3: subsequent upgrade must be rejected with 503 ---
	conn2, resp2, dialErr := websocket.DefaultDialer.DialContext(context.Background(), wsURL, hdr)
	if conn2 != nil {
		conn2.Close()
	}
	if resp2 != nil {
		defer resp2.Body.Close()
	}
	// Upgrade must fail: either dialErr is non-nil (server sent non-101) or
	// the status code is explicitly 503.
	if dialErr == nil {
		t.Fatal("expected WS upgrade to be rejected after drain, but it succeeded")
	}
	if resp2 != nil {
		require.Equal(t, http.StatusServiceUnavailable, resp2.StatusCode,
			"post-drain WS upgrade must return 503")
	}
}
