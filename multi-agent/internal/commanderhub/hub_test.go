package commanderhub

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

	sqlmock "github.com/DATA-DOG/go-sqlmock"
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

func TestHub_RegisterCapabilitiesAndLastSeenVisibleInRegistry(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL:        wsURL,
		ProxyToken: "tok-alice",
		Register: commander.RegisterPayload{
			SchemaVersion: commander.SchemaVersion,
			Kind:          "codex",
			DisplayName:   "prod-codex",
			Capabilities:  []string{"", commander.CapabilityFiles},
		},
		Handler:        &commander.Handler{},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	var info DaemonInfo
	waitFor(t, func() bool {
		infos := hub.reg.daemons(owner{userID: "alice", workspaceID: "W1"})
		if len(infos) != 1 {
			return false
		}
		info = infos[0]
		return info.LastSeenAt != "" &&
			containsString(info.Capabilities, commander.CapabilityFiles) &&
			containsString(info.Capabilities, commander.CapabilitySessions) &&
			containsString(info.Capabilities, commander.CapabilityTurn)
	}, time.Second, "daemon metadata visible")

	require.Equal(t, "prod-codex", info.DisplayName)
	require.Equal(t, "codex", info.Kind)
	_, err := time.Parse(time.RFC3339Nano, info.LastSeenAt)
	require.NoError(t, err)

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
	hub.reg.add(&daemonConn{id: "a1", shortID: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
	hub.reg.add(&daemonConn{id: "b1", shortID: "b1", owner: owner{"bob", "W1"}, displayName: "bob-laptop", kind: "codex"})

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

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// TestNewDaemonID_128BitHexLength: newDaemonID returns a 32-char hex string
// (16 bytes × 2 hex chars/byte = 32).
func TestNewDaemonID_128BitHexLength(t *testing.T) {
	id, err := newDaemonID()
	require.NoError(t, err)
	require.Len(t, id, 32, "expected 32-char hex string for 16-byte (128-bit) random ID")
}

// TestNewDaemonID_DistinctAcrossCalls: two back-to-back calls must produce
// different IDs (probability of collision is 2^-128, i.e., astronomically low).
func TestNewDaemonID_DistinctAcrossCalls(t *testing.T) {
	id1, err := newDaemonID()
	require.NoError(t, err)
	id2, err := newDaemonID()
	require.NoError(t, err)
	require.NotEqual(t, id1, id2, "two newDaemonID calls must produce distinct IDs")
}

// TestServeHTTP_ClusterMode_RequiresShortID: when a sharedRegistry is attached
// and the daemon registers with an empty ShortID, the hub must refuse the WS
// with an invalid_request error envelope.
func TestServeHTTP_ClusterMode_RequiresShortID(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	// Attach a shared registry backed by a sqlmock DB.  No SQL expectations
	// are set because admission must be refused before any DB call.
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	hub.attachSharedRegistry(ClusterRuntime{DB: db, AdvertiseURL: "http://pod-a:8091"}, newSharedRegistry(db, "http://pod-a:8091"), nil, nil)

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, wsDialHeader("tok-alice"))
	require.NoError(t, err)
	defer conn.Close()

	// Register with empty ShortID.
	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "no-short-id",
		ShortID:       "",
	})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload}))

	// Expect an error envelope with invalid_request code.
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, json.Unmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeInvalidRequest, ep.Code)

	// No DB interactions should have occurred.
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestServeHTTP_ClusterMode_RejectsWhitespaceShortID: when a sharedRegistry is
// attached and the daemon registers with a whitespace-only ShortID ("   "), the
// hub must refuse the WS with an invalid_request error envelope.
func TestServeHTTP_ClusterMode_RejectsWhitespaceShortID(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	// Attach a shared registry backed by a sqlmock DB. No SQL expectations
	// are set because admission must be refused before any DB call.
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	hub.attachSharedRegistry(ClusterRuntime{DB: db, AdvertiseURL: "http://pod-a:8091"}, newSharedRegistry(db, "http://pod-a:8091"), nil, nil)

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, wsDialHeader("tok-alice"))
	require.NoError(t, err)
	defer conn.Close()

	// Register with a whitespace-only ShortID.
	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "whitespace-short-id",
		ShortID:       "   ",
	})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload}))

	// Expect an error envelope with invalid_request code.
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, json.Unmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeInvalidRequest, ep.Code)

	// No DB interactions should have occurred.
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestServeHTTP_ClusterMode_RefusesWSOnUpsertFailure: when connectUpsert
// returns an error, the hub must refuse the WS with a backend_unavailable
// error envelope and NOT add the conn to the local registry.
func TestServeHTTP_ClusterMode_RefusesWSOnUpsertFailure(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	hub.attachSharedRegistry(ClusterRuntime{DB: db, AdvertiseURL: "http://pod-a:8091"}, newSharedRegistry(db, "http://pod-a:8091"), nil, nil)

	// Make connectUpsert fail.
	mock.ExpectExec(connectUpsertSQL).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	conn, _, dialErr := websocket.DefaultDialer.DialContext(context.Background(), wsURL, wsDialHeader("tok-alice"))
	require.NoError(t, dialErr)
	defer conn.Close()

	// Register with a valid ShortID.
	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "alice-mac",
		ShortID:       "agent-A",
	})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload}))

	// Expect a backend_unavailable error envelope.
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, json.Unmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeBackendUnavailable, ep.Code)

	// The local registry must remain empty — daemon was refused before add.
	o := owner{userID: "alice", workspaceID: "W1"}
	require.Empty(t, hub.reg.daemons(o), "local registry must be empty after upsert failure")

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestNextCmdID_SinglePod_ByteExactLegacy tests that single-pod mode returns
// base36 sequences without pod prefix (bit-exact v0.0.9 behavior).
func TestNextCmdID_SinglePod_ByteExactLegacy(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)
	// Ensure sharedReg is nil (single-pod mode).
	require.Nil(t, hub.sharedReg)

	// First 5 calls should return "1", "2", "3", "4", "5" exactly.
	expectedSeqs := []string{"1", "2", "3", "4", "5"}
	for i, expected := range expectedSeqs {
		got := hub.nextCmdID()
		require.Equal(t, expected, got, "call %d: expected base36 %q but got %q", i+1, expected, got)
	}
}

// TestNextCmdID_SharedMode_PodPrefix tests that shared mode includes a 4-hex
// pod prefix derived from the advertiseURL.
func TestNextCmdID_SharedMode_PodPrefix(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	// Set up shared mode with a known advertiseURL.
	advertiseURL := "http://10.0.0.42:8091"
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	_ = mock // unused in this test

	hub.sharedReg = newSharedRegistry(db, advertiseURL)

	// First call should return <4hex>-1.
	firstID := hub.nextCmdID()
	parts := strings.Split(firstID, "-")
	require.Len(t, parts, 2, "shared mode ID should have format <hash>-<seq>")

	podHash := parts[0]
	seqPart := parts[1]

	// Pod hash should be exactly 4 hex characters.
	require.Len(t, podHash, 4, "pod hash should be 4 hex chars")
	for _, c := range podHash {
		require.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
			"pod hash should contain only hex chars, got %c", c)
	}

	// Sequence part should be "1".
	require.Equal(t, "1", seqPart, "first sequence should be 1")

	// Second call should have the same pod hash but sequence "2".
	secondID := hub.nextCmdID()
	parts2 := strings.Split(secondID, "-")
	require.Len(t, parts2, 2)
	require.Equal(t, podHash, parts2[0], "pod hash should be consistent")
	require.Equal(t, "2", parts2[1], "second sequence should be 2")
}

// TestHub_RouteFrame_UpdatesTurnsBackend verifies that routeFrame calls
// turns.updateFromEnvelope when the pending entry carries session_turn metadata.
// This is the MAJOR-5 fix: envelopes must reach the cross-pod turn store.
func TestHub_RouteFrame_UpdatesTurnsBackend(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	// Swap in a spy turn store that records updateFromEnvelope calls.
	spy := &spyTurnStore{}
	hub.turns = spy

	o := owner{userID: "alice", workspaceID: "W1"}
	dc := &daemonConn{
		id:      "dc1",
		shortID: "agent-A",
		owner:   o,
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}),
		hub:     hub,
	}

	// Register a pending entry with session_turn metadata.
	pe := dc.registerPending("cmd-1", true, "agent-A", "sess-1", "session_turn")
	consumer := pe.ch

	// Route a status=answering event.
	ep, _ := json.Marshal(commander.EventPayload{EventKind: "status", StatusCode: "answering"})
	env := commander.Envelope{Type: "event", ID: "cmd-1", Payload: ep}
	dc.routeFrame(env)

	// Consume the frame so the channel doesn't block.
	select {
	case <-consumer:
	case <-time.After(time.Second):
		t.Fatal("no frame delivered to consumer")
	}

	// The spy must have seen at least one updateFromEnvelope call with the correct key.
	require.Eventually(t, func() bool {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		return spy.updateCount > 0
	}, time.Second, 10*time.Millisecond, "updateFromEnvelope must be called")

	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.Equal(t, "alice", spy.lastKey.owner.userID)
	require.Equal(t, "agent-A", spy.lastKey.shortID)
	require.Equal(t, "sess-1", spy.lastKey.sessionID)
}

// spyTurnStore records updateFromEnvelope calls for TestHub_RouteFrame_UpdatesTurnsBackend.
type spyTurnStore struct {
	mu          sync.Mutex
	updateCount int
	lastKey     turnKey
	memTurnStore
}

func (s *spyTurnStore) updateFromEnvelope(ctx context.Context, key turnKey, command string, env commander.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCount++
	s.lastKey = key
	return nil
}
