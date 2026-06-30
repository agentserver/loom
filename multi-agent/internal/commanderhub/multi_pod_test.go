package commanderhub

// multi_pod_test.go — integration tests exercising the full shared-registry
// path with two in-process Hub instances sharing a real Postgres database.
//
// Env-gated: set OBSERVER_POSTGRES_TEST_DSN to run these tests.
// Without the DSN they t.Skip immediately.
//
// Each fake pod uses:
//   - A distinct non-loopback "advertise URL" (http://pod-a.internal /
//     http://pod-b.internal) stored in Postgres.
//   - A real httptest.Server on 127.0.0.1 for the internal mux.
//   - A custom http.Transport that routes pod-X.internal → the real
//     httptest.Server, bypassing wouldLoop's loopback check.
//
// This design keeps all network I/O in-process without requiring real DNS
// or special network privileges.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

// ---------------------------------------------------------------------------
// Env-gated setup helpers
// ---------------------------------------------------------------------------

const multiPodDSNEnv = "OBSERVER_POSTGRES_TEST_DSN"

func requirePG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(multiPodDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run multi-pod integration tests", multiPodDSNEnv)
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err, "sql.Open pgx")
	require.NoError(t, db.PingContext(context.Background()), "ping postgres")
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// migrateAll runs the combined schema SQL from authstore so all commander_*
// tables exist. Uses CREATE TABLE IF NOT EXISTS, so idempotent.
func migrateAll(t *testing.T, db *sql.DB) {
	t.Helper()
	require.NoError(t, authstore.MigratePostgres(db), "MigratePostgres")
}

func cleanupTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, tbl := range []string{
		"commander_daemons",
		"commander_turns",
		"commander_forward_nonces",
		"commander_telemetry_buckets",
		"commander_identity_revocations",
	} {
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+tbl); err != nil {
			t.Logf("truncate %s: %v (table may not exist)", tbl, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Fake-pod wiring
// ---------------------------------------------------------------------------

// fakePod is a full in-process Hub + httptest.Server pair that mimics a
// deployed observer pod. Two fakePods sharing the same *sql.DB simulate a
// two-pod cluster.
type fakePod struct {
	name        string
	db          *sql.DB
	sr          *sharedRegistry
	fc          *forwardClient
	hub         *Hub
	internalSrv *httptest.Server
	advertiseURL string // fake (non-loopback) URL stored in Postgres
}

// newFakePod constructs a Hub in cluster mode with a custom HTTP transport
// that routes advertiseURL → actual httptest.Server, bypassing loopback detection.
//
//   - advertiseURL must be a non-loopback fake URL (e.g. "http://pod-a.internal").
//   - secret / prevSecret are the HMAC keys for forward/drain auth.
func newFakePod(t *testing.T, db *sql.DB, name string, advertiseURL string, secret, prevSecret []byte) *fakePod {
	t.Helper()

	// 1. Start internal mux + httptest.Server first to get the real listen addr.
	internalMux := http.NewServeMux()
	internalSrv := httptest.NewServer(internalMux)
	t.Cleanup(internalSrv.Close)

	// 2. Build shared registry with the fake advertise URL.
	sr := newSharedRegistry(db, advertiseURL)
	// Tighten timings so tests aren't slow:
	sr.heartbeatEvery = 200 * time.Millisecond
	sr.sweepEvery = 100 * time.Millisecond
	sr.onlineTTL = 10 * time.Second
	sr.deleteAfter = 30 * time.Second
	sr.nonceTTL = 30 * time.Second

	// 3. Build forward client: its advertiseURL is the fake URL (for loop
	//    detection), but its http.Client uses a transport that dials the real
	//    httptest.Server for any host matching the name pattern "*.internal".
	fc := newForwardClient(secret, prevSecret, advertiseURL, 0)
	// Replace the transport so *.internal hostnames reach real test servers.
	fc.httpClient.Transport = newFakeClusterTransport()

	// 4. Build Hub in cluster mode.
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	hub := NewHub(resolver)
	cluster := ClusterRuntime{
		DB:           db,
		AdvertiseURL: advertiseURL,
		Secret:       secret,
		PrevSecret:   prevSecret,
	}
	turns := newPGTurnStore(db)
	hub.attachSharedRegistry(cluster, sr, fc, turns)

	// 5. Mount internal endpoints on the internal mux.
	internalMux.HandleFunc("/api/commander/_internal/forward", hub.forwardHandler)
	internalMux.HandleFunc("/api/commander/_internal/drain", hub.drainHandler)

	pod := &fakePod{
		name:         name,
		db:           db,
		sr:           sr,
		fc:           fc,
		hub:          hub,
		internalSrv:  internalSrv,
		advertiseURL: advertiseURL,
	}

	// 6. Register this pod's fake hostname → real server mapping.
	registerFakeHost(t, advertiseURL, internalSrv.URL)

	return pod
}

// daemonOwner is the default owner used across multi-pod tests.
var multiPodOwner = owner{userID: "mp-user", workspaceID: "mp-ws"}

// addLocalDaemon adds a daemonConn to pod's local registry and inserts its
// row into Postgres (simulating a WebSocket daemon connect). The returned
// daemonConn has a real WebSocket conn via newOwnershipTestDaemonConn so
// the heartbeat goroutine can close it.
//
// A background goroutine is started that watches for WS-connection closure and
// then calls sr.remove + reg.removeIf, mirroring the deferred cleanup that the
// real handleDaemonLink read-loop performs. This means drain tests can trigger
// removal via normal WS close (as in production) rather than manual removeDaemon calls.
func addLocalDaemon(t *testing.T, pod *fakePod, shortID string, caps ...string) *daemonConn {
	t.Helper()
	dc := newOwnershipTestDaemonConn(t, shortID+"-conn", shortID, multiPodOwner)
	dc.shortID = shortID
	dc.displayName = shortID + "-display"
	dc.kind = "claude"
	dc.driverVersion = "1.0.0"
	dc.hub = pod.hub

	capMap := map[string]bool{
		commander.CapabilitySessions: true,
		commander.CapabilityTurn:     true,
	}
	for _, c := range caps {
		capMap[c] = true
	}
	dc.metaMu.Lock()
	dc.capabilities = capMap
	dc.lastSeenAt = time.Now().UTC()
	dc.metaMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, pod.sr.connectUpsert(ctx, dc), "connectUpsert")

	pod.hub.reg.add(dc)

	// Start background goroutine that mirrors the real read-loop's deferred cleanup:
	// when dc.conn is closed (e.g. by drainAllLocalDaemons), remove the daemon from
	// both the local registry and the shared Postgres registry.
	go func() {
		// The gorilla Conn's ReadMessage will return an error immediately once the
		// server-side connection is closed. We use this as the close signal.
		for {
			if _, _, err := dc.conn.ReadMessage(); err != nil {
				// Connection closed — run the deferred cleanup.
				routingID := dc.routingID()
				pod.hub.reg.removeIf(dc.owner, routingID, func(existing *daemonConn) bool {
					return existing.id == dc.id
				})
				removeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = pod.sr.remove(removeCtx, dc.owner, dc.shortID, dc.id)
				cancel()
				return
			}
		}
	}()

	return dc
}

// removeDaemon removes a daemonConn from both local and shared registry.
func removeDaemon(t *testing.T, pod *fakePod, dc *daemonConn) {
	t.Helper()
	routingID := dc.routingID()
	pod.hub.reg.removeIf(multiPodOwner, routingID, func(existing *daemonConn) bool {
		return existing.id == dc.id
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = pod.sr.remove(ctx, multiPodOwner, dc.shortID, dc.id)
}

// ---------------------------------------------------------------------------
// Fake cluster transport
// ---------------------------------------------------------------------------

// fakeClusterTransport routes "pod-X.internal" hostnames to real httptest
// servers registered via registerFakeHost. This lets forwardClient.send POST
// to "http://pod-a.internal/..." which actually hits the 127.0.0.1 httptest
// server without triggering wouldLoop's loopback check.

var (
	fakeHostsMu sync.RWMutex
	fakeHosts   = map[string]string{} // "pod-a.internal:80" → "127.0.0.1:PORT"
)

func registerFakeHost(t *testing.T, advertiseURL, realServerURL string) {
	t.Helper()
	// advertiseURL e.g. "http://pod-a.internal"
	// realServerURL e.g. "http://127.0.0.1:12345"
	fakeHost := hostPort(advertiseURL)
	realAddr := hostPort(realServerURL)

	fakeHostsMu.Lock()
	fakeHosts[fakeHost] = realAddr
	fakeHostsMu.Unlock()

	t.Cleanup(func() {
		fakeHostsMu.Lock()
		delete(fakeHosts, fakeHost)
		fakeHostsMu.Unlock()
	})
}

// hostPort extracts "host:port" from a URL, defaulting to port 80/443.
func hostPort(rawURL string) string {
	rawURL = strings.TrimPrefix(rawURL, "http://")
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.SplitN(rawURL, "/", 2)[0]
	if !strings.Contains(rawURL, ":") {
		rawURL += ":80"
	}
	return rawURL
}

// newFakeClusterTransport returns an http.RoundTripper that resolves
// fake hostnames to real httptest servers before dialing.
func newFakeClusterTransport() http.RoundTripper {
	base := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			fakeHostsMu.RLock()
			real, ok := fakeHosts[addr]
			fakeHostsMu.RUnlock()
			if ok {
				addr = real
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		DisableKeepAlives: true, // test isolation
	}
	return base
}

// ---------------------------------------------------------------------------
// Test 1: DaemonRegistration_VisibleFromBothPods
// ---------------------------------------------------------------------------

func TestMultiPod_DaemonRegistration_VisibleFromBothPods(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-1")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Pod A registers daemon "abc".
	addLocalDaemon(t, podA, "abc")

	// Pod B's listAll should immediately include "abc" (read from Postgres).
	ctx := context.Background()
	daemons, err := podB.sr.listAll(ctx, multiPodOwner)
	require.NoError(t, err)
	require.Len(t, daemons, 1, "pod B must see pod A's daemon via shared registry")
	require.Equal(t, "abc", daemons[0].ShortID)
}

// ---------------------------------------------------------------------------
// Test 2: RegistrySweep_RemovesStaleDaemon
// ---------------------------------------------------------------------------

func TestMultiPod_RegistrySweep_RemovesStaleDaemon(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-2")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Insert a daemon row with last_seen_at very far in the past (stale).
	ctx := context.Background()
	_, err := db.ExecContext(ctx,
		`INSERT INTO commander_daemons
		 (user_id, workspace_id, short_id, connection_id, display_name, kind,
		  driver_version, capabilities, owning_instance_url, last_seen_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, now() - interval '10 minutes', now() - interval '10 minutes')
		 ON CONFLICT (user_id, workspace_id, short_id) DO UPDATE
		 SET last_seen_at = now() - interval '10 minutes'`,
		multiPodOwner.userID, multiPodOwner.workspaceID, "stale-abc",
		"stale-conn-id", "stale daemon", "claude", "1.0.0", `["sessions"]`,
		podA.advertiseURL)
	require.NoError(t, err)

	// Confirm the stale daemon row exists in raw SQL but is NOT visible via
	// listAll (which filters by onlineTTL — 10 minutes ago is outside the
	// default onlineTTL window, so the production filter correctly hides it).
	rawRows, err := db.QueryContext(ctx,
		`SELECT short_id FROM commander_daemons WHERE user_id=$1 AND workspace_id=$2`,
		multiPodOwner.userID, multiPodOwner.workspaceID)
	require.NoError(t, err)
	var rawIDs []string
	for rawRows.Next() {
		var sid string
		require.NoError(t, rawRows.Scan(&sid))
		rawIDs = append(rawIDs, sid)
	}
	require.NoError(t, rawRows.Err())
	rawRows.Close()
	require.Contains(t, rawIDs, "stale-abc", "stale row must exist in raw SQL before sweep")

	initial, err := podB.sr.listAll(ctx, multiPodOwner)
	require.NoError(t, err)
	require.Empty(t, initial, "stale daemon must NOT be visible via listAll (outside onlineTTL filter)")

	// Override deleteAfter to be very short so the stale row qualifies.
	podB.sr.deleteAfter = 5 * time.Minute

	// Pod B's sweep removes it.
	podB.sr.runSweepOnce(ctx)

	// After sweep, the daemon should be gone from Postgres.
	remaining, err := db.QueryContext(ctx,
		`SELECT short_id FROM commander_daemons WHERE user_id=$1 AND workspace_id=$2`,
		multiPodOwner.userID, multiPodOwner.workspaceID)
	require.NoError(t, err)
	defer remaining.Close()
	var rows []string
	for remaining.Next() {
		var sid string
		require.NoError(t, remaining.Scan(&sid))
		rows = append(rows, sid)
	}
	require.NoError(t, remaining.Err())
	require.Empty(t, rows, "stale daemon must be removed by sweep")
}

// ---------------------------------------------------------------------------
// Test 3: OwnershipFailover_NewClaimWins
// ---------------------------------------------------------------------------

func TestMultiPod_OwnershipFailover_NewClaimWins(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-3")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Pod A registers daemon "x".
	dcA := addLocalDaemon(t, podA, "x")

	// Pod B now "steals" ownership by doing a connectUpsert with a different conn-id.
	dcB := newOwnershipTestDaemonConn(t, "x-conn-B", "x", multiPodOwner)
	dcB.shortID = "x"
	dcB.displayName = "x-display"
	dcB.kind = "claude"
	dcB.driverVersion = "1.0.0"
	dcB.hub = podB.hub
	dcB.metaMu.Lock()
	dcB.capabilities = map[string]bool{commander.CapabilitySessions: true}
	dcB.lastSeenAt = time.Now().UTC()
	dcB.metaMu.Unlock()

	ctx := context.Background()
	require.NoError(t, podB.sr.connectUpsert(ctx, dcB), "pod B steal ownership via connectUpsert")

	// Pod A runs heartbeatUpsert — it should see 0 rows (ownership lost).
	stillOwn, err := podA.sr.heartbeatUpsert(ctx, dcA)
	require.NoError(t, err)
	require.False(t, stillOwn, "pod A must lose ownership after pod B's connectUpsert")

	// Pod A's runHeartbeatOnce should set ownershipLost and close the conn.
	keepGoing := podA.sr.runHeartbeatOnce(ctx, dcA)
	require.False(t, keepGoing, "runHeartbeatOnce must return false on ownership loss")
	require.True(t, dcA.ownershipLost.Load(), "ownershipLost must be sticky-true")
}

// ---------------------------------------------------------------------------
// Test 4: ForwardFromBToA_RoundTrips
// ---------------------------------------------------------------------------

func TestMultiPod_ForwardFromBToA_RoundTrips(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-4")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Pod A holds daemon "abc". We need a real WebSocket daemon behind it to
	// complete the round trip. We simulate sendCommandToLocal by registering a
	// fake command handler via a real WS connection.
	dcA := addLocalDaemon(t, podA, "abc")

	// Spin up a goroutine that plays the daemon role: reads the command
	// envelope from the pending entry and writes back a command_result.
	go func() {
		// Wait for a pending entry to appear.
		deadline := time.Now().Add(5 * time.Second)
		var cmdID string
		for time.Now().Before(deadline) {
			dcA.pendingMu.Lock()
			for id := range dcA.pending {
				cmdID = id
			}
			dcA.pendingMu.Unlock()
			if cmdID != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if cmdID == "" {
			return
		}
		// Route a command_result back. For command_result, Payload is the raw
		// JSON result (a sessions list in this case).
		payload, _ := json.Marshal(map[string]string{"sessions": "[]"})
		dcA.routeFrame(commander.Envelope{
			Type:    "command_result",
			ID:      cmdID,
			Payload: payload,
		})
	}()

	// Pod B does lookupRemote — should find pod-a.internal as the owner.
	ctx := context.Background()
	peerURL, _, found, err := podB.sr.lookupRemote(ctx, multiPodOwner, "abc")
	require.NoError(t, err)
	require.True(t, found, "pod B must find abc via shared registry")
	require.Equal(t, podA.advertiseURL, peerURL)

	// Pod B forwards a list_sessions command to pod A.
	result, err := podB.fc.send(ctx, peerURL, forwardRequest{
		UserID:      multiPodOwner.userID,
		WorkspaceID: multiPodOwner.workspaceID,
		DaemonID:    "abc",
		Command:     "list_sessions",
	})
	require.NoError(t, err, "forward from B to A must succeed")
	require.NotNil(t, result, "result payload must be non-nil")
}

// ---------------------------------------------------------------------------
// Test 5: ForwardWithRevokedSecret_FailsClosed
// ---------------------------------------------------------------------------

func TestMultiPod_ForwardWithRevokedSecret_FailsClosed(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	rightSecret := []byte("right-secret")
	wrongSecret := []byte("wrong-secret")

	// Pod A uses rightSecret; pod B uses wrongSecret (simulating revocation).
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", rightSecret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", wrongSecret, nil)

	addLocalDaemon(t, podA, "abc")

	ctx := context.Background()
	peerURL, _, found, err := podB.sr.lookupRemote(ctx, multiPodOwner, "abc")
	require.NoError(t, err)
	require.True(t, found)

	// Pod B has no prevSecret either, so all keys exhausted → ErrDaemonGone.
	_, sendErr := podB.fc.send(ctx, peerURL, forwardRequest{
		UserID:      multiPodOwner.userID,
		WorkspaceID: multiPodOwner.workspaceID,
		DaemonID:    "abc",
		Command:     "list_sessions",
	})
	require.ErrorIs(t, sendErr, ErrDaemonGone, "wrong secret must return ErrDaemonGone (fail closed)")
}

// ---------------------------------------------------------------------------
// Test 6: ForwardWithRotatedSecret_RetriesWithPrev
// ---------------------------------------------------------------------------

func TestMultiPod_ForwardWithRotatedSecret_RetriesWithPrev(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret1 := []byte("secret-old")
	secret2 := []byte("secret-new")

	// Pod A: current=secret2, prev=secret1 (accepts both).
	// Pod B: current=secret1 only (has not rotated yet).
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret2, secret1)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret1, nil)

	// Pod A's daemon.
	dcA := addLocalDaemon(t, podA, "abc")

	// Daemon goroutine — echo back a result.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		var cmdID string
		for time.Now().Before(deadline) {
			dcA.pendingMu.Lock()
			for id := range dcA.pending {
				cmdID = id
			}
			dcA.pendingMu.Unlock()
			if cmdID != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if cmdID == "" {
			return
		}
		payload, _ := json.Marshal(map[string]string{"ok": "true"})
		dcA.routeFrame(commander.Envelope{
			Type:    "command_result",
			ID:      cmdID,
			Payload: payload,
		})
	}()

	ctx := context.Background()
	// Pod B signs with secret1; pod A verifies first with secret2 (fail),
	// then falls back to prevSecret=secret1 (success).
	peerURL, _, found, err := podB.sr.lookupRemote(ctx, multiPodOwner, "abc")
	require.NoError(t, err)
	require.True(t, found)

	result, err := podB.fc.send(ctx, peerURL, forwardRequest{
		UserID:      multiPodOwner.userID,
		WorkspaceID: multiPodOwner.workspaceID,
		DaemonID:    "abc",
		Command:     "list_sessions",
	})
	require.NoError(t, err, "pod B signing with old secret must succeed when pod A accepts prev")
	require.NotNil(t, result)
}

// ---------------------------------------------------------------------------
// Test 7: TurnState_VisibleFromBothPods
// ---------------------------------------------------------------------------

func TestMultiPod_TurnState_VisibleFromBothPods(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-7")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	ctx := context.Background()
	key := turnKey{
		owner:     multiPodOwner,
		shortID:   "x",
		sessionID: "s1",
	}

	// Pod A begins a turn.
	started, err := podA.hub.turns.begin(ctx, key)
	require.NoError(t, err)
	require.True(t, started, "turn must begin on pod A")

	// Pod B reads the same turn state.
	snap, err := podB.hub.turns.get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, turnStateQueued, snap.State, "pod B must see the turn state set by pod A")
	require.True(t, snap.InFlight, "InFlight must be true in queued state")
}

// ---------------------------------------------------------------------------
// Test 8: TurnState_ConcurrentBegin_OneWins
// ---------------------------------------------------------------------------

func TestMultiPod_TurnState_ConcurrentBegin_OneWins(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-8")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	ctx := context.Background()
	key := turnKey{
		owner:     multiPodOwner,
		shortID:   "x",
		sessionID: "concurrent-s",
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)

	for i, pod := range []*fakePod{podA, podB} {
		wg.Add(1)
		i, pod := i, pod
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = pod.hub.turns.begin(ctx, key)
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err, "begin must not return an error")
	}

	wins := 0
	for _, won := range results {
		if won {
			wins++
		}
	}
	require.Equal(t, 1, wins, "exactly one pod must win the concurrent begin")
}

// ---------------------------------------------------------------------------
// Test 9: DrainOnShutdown_FlushesDaemons
// ---------------------------------------------------------------------------

func TestMultiPod_DrainOnShutdown_FlushesDaemons(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-9")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)

	// Pod A registers 2 daemons.
	dc1 := addLocalDaemon(t, podA, "daemon-1")
	dc2 := addLocalDaemon(t, podA, "daemon-2")
	// Keep them in scope to prevent GC.
	_ = dc1
	_ = dc2

	// Confirm 2 rows exist in Postgres.
	ctx := context.Background()
	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM commander_daemons WHERE user_id=$1 AND workspace_id=$2`,
		multiPodOwner.userID, multiPodOwner.workspaceID).Scan(&count))
	require.Equal(t, 2, count, "2 daemons must be registered before drain")

	// Pod A's drainHandler is accessible via loopback (no HMAC needed).
	resp, err := http.Post(podA.internalSrv.URL+"/api/commander/_internal/drain", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "drain must succeed")

	// After drain, the WS connections are closed. The background goroutines
	// started by addLocalDaemon (mirroring the real read-loop deferred cleanup)
	// detect the close and call sr.remove. Poll until both rows disappear
	// (or timeout), exercising the real WS-defer cleanup path rather than
	// manually calling removeDaemon.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM commander_daemons WHERE user_id=$1 AND workspace_id=$2`,
			multiPodOwner.userID, multiPodOwner.workspaceID).Scan(&count))
		if count == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, 0, count, "shared registry must have 0 rows for pod A's daemons after WS-close cleanup")
}

// ---------------------------------------------------------------------------
// Test 10: NonceReplay_FailsClosed
// ---------------------------------------------------------------------------

func TestMultiPod_NonceReplay_FailsClosed(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-10")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	addLocalDaemon(t, podA, "abc")

	ctx := context.Background()
	peerURL, _, found, err := podB.sr.lookupRemote(ctx, multiPodOwner, "abc")
	require.NoError(t, err)
	require.True(t, found)

	// Build and send the first request using doSend directly to capture the nonce.
	// We need to replay the exact same signed request to trigger nonce rejection.
	body, _ := json.Marshal(forwardRequest{
		UserID:      multiPodOwner.userID,
		WorkspaceID: multiPodOwner.workspaceID,
		DaemonID:    "abc",
		Command:     "list_sessions",
	})

	ts := time.Now().Unix()
	nonce, err := freshNonce()
	require.NoError(t, err)
	sig := signForward(secret, ts, nonce, body)

	endpoint := strings.TrimRight(peerURL, "/") + "/api/commander/_internal/forward"

	// Build the signed request.
	buildReq := func() *http.Request {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forward-Ts", fmt.Sprintf("%d", ts))
		req.Header.Set("X-Forward-Nonce", nonce)
		req.Header.Set("X-Forward-Sig", sig)
		return req
	}

	// First request — will go through (daemon goroutine not needed since we only
	// care about nonce insertion, not command execution; daemon might return 404
	// if the local lookup fails after nonce insertion, but the nonce IS inserted).
	resp1, err := podB.fc.httpClient.Do(buildReq())
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	// The first request may succeed or fail for the command itself, but the
	// nonce must have been inserted.

	// Second request with the same nonce must be rejected (replay).
	resp2, err := podB.fc.httpClient.Do(buildReq())
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	require.Equal(t, http.StatusForbidden, resp2.StatusCode,
		"replay with same nonce must return 403")
}

// ---------------------------------------------------------------------------
// Helpers used across test files
// ---------------------------------------------------------------------------

// assertEventually retries cond every 20ms until it returns true or timeout.
// Reports the failure message via t.Fatal if the deadline is reached.
func assertEventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	assert.Eventually(t, cond, timeout, 20*time.Millisecond, msg)
}
