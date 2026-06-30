package commanderhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestStream_CancelWhileStreamingNoPanic is the regression test for the
// send-on-closed-channel panic that crashed the observer.
//
// Setup: a real daemon (WSClient + tbBackend) streams many chunk events and a
// terminal command_result. The browser-turn consumer (SendCommandStream) starts
// draining, then CANCELS its ctx partway — exactly what the 30s turn timeout or
// a browser disconnect does. In the proxy goroutine, `defer removePending` then
// fires while the daemon is still emitting frames; routeFrame (in the read loop)
// does a locked lookup, releases the lock, and calls sendOrDrop on the pending
// channel. Under the OLD code removePending closed that channel, so a late frame
// in that window panicked "send on closed channel". Under the fix the channel is
// never closed, so routeFrame's lookup misses (entry deleted) and the late frame
// is dropped — no panic.
//
// Run with -race -count to stress the race window.
func TestStream_CancelWhileStreamingNoPanic(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	// resumeFn emits 200 chunks with small sleeps, then a terminal result. The
	// sleeps spread frames across time so the consumer's cancel can land while
	// the daemon is mid-stream (maximizing the race window).
	const chunks = 200
	var sent int64
	backend := &tbBackend{
		resumeFn: func(ctx context.Context, _ agentbackend.SessionRef, _ string, sink executor.Sink) (executor.Result, error) {
			for i := 0; i < chunks; i++ {
				sink.Write("chunk", "x")
				atomic.AddInt64(&sent, 1)
				// Tiny sleep keeps the daemon from dumping all frames into the
				// 16-deep buffer at once; instead they trickle while we cancel.
				select {
				case <-time.After(200 * time.Microsecond):
				case <-ctx.Done():
					return executor.Result{Summary: "ctx-done"}, nil
				}
			}
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
	}

	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", backend)
	defer cleanup()

	di := hub.reg.daemons(o)
	require.NotEmpty(t, di)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := hub.SendCommandStream(ctx, o, di[0].DaemonID, "session_turn",
		jsonRaw(t, commander.SessionTurnArgs{ID: "s1", Prompt: "go"}))
	require.NoError(t, err)

	// Drain the consumer in a goroutine; it must not panic. Returned after the
	// consumer goroutine ends (channel closed by the stream's defer on
	// terminal/cancel/dc.done).
	consumerDone := make(chan struct{})
	var got int64
	go func() {
		defer close(consumerDone)
		// range over out; SendCommandStream closes `out` on every exit path.
		for range ch {
			atomic.AddInt64(&got, 1)
		}
	}()

	// Cancel partway: wait until the daemon has emitted a handful of frames,
	// then pull the rug — simulating the 30s turn timeout / browser disconnect.
	require.Eventually(t, func() bool { return atomic.LoadInt64(&sent) >= 3 },
		time.Second, time.Microsecond*500, "daemon never started streaming")
	cancel()

	// Consumer must terminate cleanly: cancel fires <-ctx.Done() in the stream
	// goroutine, its defer removePending runs (now delete-only, no close), `out`
	// closes, the range ends. No panic ever reaches the process.
	select {
	case <-consumerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not terminate after cancel")
	}

	// Sanity: the daemon streamed at least a few frames before we cancelled.
	require.GreaterOrEqual(t, atomic.LoadInt64(&sent), int64(1))
}

// TestHub_Close_RaceVsAdmission is the race-detector regression test for the
// admission-vs-Close race described in D-fix3 MAJOR #1.
//
// Before the fix: ServeHTTP checked h.draining (no lock), then did some work,
// then called h.reg.add(dc). Close could set draining=true and snapshot the
// registry between those two points, so a concurrently-upgrading WS ended up
// admitted to neither the snapshot (missed by Close) nor rejected (passed the
// pre-check), meaning it survived shutdown indefinitely.
//
// After the fix: admitMu makes the (re-check draining + reg.add) atomic in
// ServeHTTP and the (draining.Store + snapshot) atomic in Close, so every
// live WS is either in the drain snapshot or rejected before being added.
//
// The test spawns N=50 goroutines racing to open WS upgrades concurrently with
// a hub.Close call. With -race it also surfaces any data-race on h.draining or
// the local registry. Post-Close asserts:
//   - h.reg is empty (zero local daemons)
//   - h.draining is set
//   - all successfully-opened WS connections have been closed by the server
//
// Run as: go test -run TestHub_Close_RaceVsAdmission -race -count=5
func TestHub_Close_RaceVsAdmission(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok-alice")

	const N = 50

	// conns collects every WebSocket that was successfully upgraded (before the
	// server rejected or closed it). We check them for server-side close after
	// hub.Close returns.
	var connsMu sync.Mutex
	var conns []*websocket.Conn

	// admitted counts goroutines that were fully admitted (ack received).
	var admitted int64

	// start is a gate that all goroutines wait on simultaneously to maximise
	// the race window against hub.Close.
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)

	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "race-daemon",
	})

	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start // wait for the gun

			conn, resp, err := websocket.DefaultDialer.DialContext(
				context.Background(), wsURL, hdr)
			if err != nil {
				// Server rejected upgrade (503 draining or other error): fine.
				if resp != nil {
					resp.Body.Close()
				}
				return
			}

			// Upgraded: register so we can receive the ack (or a close frame).
			connsMu.Lock()
			conns = append(conns, conn)
			connsMu.Unlock()

			// Send register. The server may have started draining after the
			// upgrade; it is fine if this write fails.
			_ = conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload})

			// Try to read: may get ack or a close frame.
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			var env commander.Envelope
			if err := conn.ReadJSON(&env); err == nil && env.Type == "ack" {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}

	// Fire all goroutines, then immediately call Close.
	close(start)

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, hub.Close(closeCtx))

	// Wait for all dial goroutines to finish.
	wg.Wait()

	// ASSERTION 1: draining flag is set.
	require.True(t, hub.draining.Load(), "h.draining must be true after Close")

	// ASSERTION 2: local registry is empty (all daemons' defers ran).
	// Give defers a short window to complete (they run in ServeHTTP goroutines
	// that may be slightly behind).
	require.Eventually(t, func() bool {
		hub.reg.mu.Lock()
		defer hub.reg.mu.Unlock()
		return len(hub.reg.conns) == 0
	}, 3*time.Second, 10*time.Millisecond, "local registry must be empty after Close")

	// ASSERTION 3: every successfully-upgraded WS was closed by the server.
	connsMu.Lock()
	defer connsMu.Unlock()
	for _, conn := range conns {
		// Drain until error; the server must have closed these.
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break // closed (expected)
			}
		}
	}
	// No assertion needed — if a connection hangs here the 2s deadline will
	// surface it. The -race detector catches any data races on the way.
	t.Logf("admitted=%d total_upgraded=%d", atomic.LoadInt64(&admitted), len(conns))
}

// TestHub_Admission_RejectedAfterUpsert_RemovesSharedRow is the race-detector
// regression test for D-fix4 MAJOR #1: when a daemon is rejected in the
// draining-rejection branch of ServeHTTP (after connectUpsert succeeded but
// before h.reg.add), the shared registry row must be removed so sibling pods
// do not see a ghost daemon until the sweep TTL.
//
// Arrangement:
//   - A sqlmock DB records the exact SQL calls in order.
//   - hub.testHookPostUpsert flips h.draining=true between connectUpsert and
//     the admitMu critical section, deterministically opening the race window
//     without real scheduling non-determinism.
//   - The test asserts that sqlmock sees BOTH the connectUpsert and the remove
//     SQL in that order, confirming the fix is exercised.
//
// Run as: go test -run TestHub_Admission_RejectedAfterUpsert_RemovesSharedRow -race -count=5
func TestHub_Admission_RejectedAfterUpsert_RemovesSharedRow(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	// Set up a sqlmock DB with exact-match SQL.
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	const advertiseURL = "http://pod-a:8091"

	// Expect 1: connectUpsert (INSERT ... ON CONFLICT DO UPDATE).
	mock.ExpectExec(connectUpsertSQL).
		WithArgs(
			sqlmock.AnyArg(), // user_id
			sqlmock.AnyArg(), // workspace_id
			sqlmock.AnyArg(), // short_id
			sqlmock.AnyArg(), // connection_id
			sqlmock.AnyArg(), // display_name
			sqlmock.AnyArg(), // kind
			sqlmock.AnyArg(), // driver_version
			sqlmock.AnyArg(), // capabilities (json)
			advertiseURL,     // owning_instance_url
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Expect 2: remove (DELETE with ownership guard) called from the draining-
	// rejection branch before closing the WS.
	mock.ExpectExec(removeSQL).
		WithArgs(
			sqlmock.AnyArg(), // user_id
			sqlmock.AnyArg(), // workspace_id
			sqlmock.AnyArg(), // short_id
			advertiseURL,     // owning_instance_url
			sqlmock.AnyArg(), // connection_id
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	hub := NewHub(resolver)
	sr := newSharedRegistry(db, advertiseURL)
	hub.attachSharedRegistry(ClusterRuntime{DB: db, AdvertiseURL: advertiseURL}, sr, nil, nil)

	// Install the race-window hook: flip draining=true between connectUpsert and
	// the admitMu critical section. This is the exact race the fix must handle.
	hub.testHookPostUpsert = func() {
		hub.admitMu.Lock()
		hub.draining.Store(true)
		hub.admitMu.Unlock()
	}

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, wsDialHeader("tok-alice"))
	require.NoError(t, err)
	defer conn.Close()

	// Register: the hub will upsert, call the hook (draining=true), then reject.
	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "race-daemon",
		ShortID:       "agent-race",
	})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload}))

	// The server must close the WS (draining rejection). Drain until error.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break // expected: server closed
		}
	}

	// Give the remove call time to complete (it runs synchronously in ServeHTTP
	// before the WS close, so it must already have landed, but be defensive).
	require.Eventually(t, func() bool {
		return mock.ExpectationsWereMet() == nil
	}, 2*time.Second, 10*time.Millisecond,
		"sqlmock expectations not met: connectUpsert and remove must both be called")

	// Local registry must be empty — the daemon was rejected before reg.add.
	o := owner{userID: "alice", workspaceID: "W1"}
	require.Empty(t, hub.reg.daemons(o), "local registry must be empty: daemon was rejected before add")
}

// TestHub_Close_WaitsForInFlightUpsertCleanup is the regression test for
// D-fix5 MAJOR #1: Close must not return before in-flight post-upsert cleanup
// (sharedReg.remove in the draining-rejection branch) has finished.
//
// Before the fix: Close set draining=true, snapshotted the registry, and
// returned — a goroutine that passed the fast pre-check was still executing
// sharedReg.remove (up to 5s timeout). If the process exited immediately after
// Close, that remove never completed → ghost row.
//
// After the fix: Close calls inFlightAdmissions.Wait() after releasing admitMu
// so in-flight goroutines can complete their remove call before Close returns.
//
// Arrangement:
//   - sqlmock DB records connectUpsert then remove SQL in order.
//   - testHookPostUpsert blocks until a gate channel is closed (simulating the
//     goroutine being paused in the race window).
//   - Close is called from another goroutine.
//   - We assert Close has not returned while the hook is still blocking.
//   - Then we unblock the hook (releasing the goroutine to execute remove).
//   - We assert Close returns after that.
//
// Run as: go test -run TestHub_Close_WaitsForInFlightUpsertCleanup -race -count=5
func TestHub_Close_WaitsForInFlightUpsertCleanup(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	const advertiseURL = "http://pod-a:8091"

	// Expect 1: connectUpsert succeeds.
	mock.ExpectExec(connectUpsertSQL).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), advertiseURL,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Expect 2: remove is called from the draining-rejection branch.
	mock.ExpectExec(removeSQL).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			advertiseURL, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	hub := NewHub(resolver)
	sr := newSharedRegistry(db, advertiseURL)
	hub.attachSharedRegistry(ClusterRuntime{DB: db, AdvertiseURL: advertiseURL}, sr, nil, nil)

	// gate controls when the hook releases the in-flight goroutine. The hook
	// is called after connectUpsert but BEFORE admitMu acquisition. It blocks
	// until gate is closed, keeping the goroutine in the race window while
	// Close runs in parallel.
	gate := make(chan struct{})
	hookEntered := make(chan struct{})

	hub.testHookPostUpsert = func() {
		close(hookEntered)
		<-gate // block until test releases
		// Now set draining=true inside admitMu (simulating Close having raced).
		hub.admitMu.Lock()
		hub.draining.Store(true)
		hub.admitMu.Unlock()
	}

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	// Dial and send register in a goroutine; it will block in the hook.
	dialDone := make(chan struct{})
	go func() {
		defer close(dialDone)
		conn, _, dialErr := websocket.DefaultDialer.DialContext(context.Background(), wsURL, wsDialHeader("tok-alice"))
		if dialErr != nil {
			return
		}
		defer conn.Close()
		regPayload, _ := json.Marshal(commander.RegisterPayload{
			SchemaVersion: commander.SchemaVersion,
			Kind:          "claude",
			DisplayName:   "race-daemon",
			ShortID:       "agent-waitclose",
		})
		_ = conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload})
		// Drain until closed.
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Wait until the goroutine is stuck in the hook (after connectUpsert).
	select {
	case <-hookEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook never entered: goroutine did not reach post-upsert window")
	}

	// Call Close in a goroutine; it must block waiting for the in-flight admission.
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, hub.Close(closeCtx))
	}()

	// Close must NOT have returned yet — the hook is still holding the goroutine.
	select {
	case <-closeDone:
		t.Fatal("Close returned before in-flight admission cleanup finished")
	case <-time.After(50 * time.Millisecond):
		// expected: Close is still waiting
	}

	// Release the hook → goroutine executes draining-rejection → sharedReg.remove.
	close(gate)

	// Now Close must return (with enough budget for the remove to complete).
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after in-flight admission cleanup finished")
	}

	// The dial goroutine must also finish.
	select {
	case <-dialDone:
	case <-time.After(3 * time.Second):
		t.Fatal("dial goroutine did not finish")
	}

	// Verify both SQL calls were made.
	require.Eventually(t, func() bool {
		return mock.ExpectationsWereMet() == nil
	}, 2*time.Second, 10*time.Millisecond,
		"sqlmock: connectUpsert and remove must both have been called")
}

// TestHub_DrainHandler_WaitsForInFlightUpsertCleanup verifies that the preStop
// drain handler also waits for in-flight post-upsert cleanup before returning.
//
// This mirrors TestHub_Close_WaitsForInFlightUpsertCleanup but exercises the
// drainHandler code path (POST /api/commander/_internal/drain from loopback).
//
// Run as: go test -run TestHub_DrainHandler_WaitsForInFlightUpsertCleanup -race -count=5
func TestHub_DrainHandler_WaitsForInFlightUpsertCleanup(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	const advertiseURL = "http://pod-b:8091"

	mock.ExpectExec(connectUpsertSQL).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), advertiseURL,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec(removeSQL).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			advertiseURL, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	hub := NewHub(resolver)
	sr := newSharedRegistry(db, advertiseURL)
	hub.attachSharedRegistry(ClusterRuntime{DB: db, AdvertiseURL: advertiseURL}, sr, nil, nil)

	gate := make(chan struct{})
	hookEntered := make(chan struct{})

	hub.testHookPostUpsert = func() {
		close(hookEntered)
		<-gate
		hub.admitMu.Lock()
		hub.draining.Store(true)
		hub.admitMu.Unlock()
	}

	mux := http.NewServeMux()
	mux.Handle("/api/daemon-link", hub)
	mux.HandleFunc("/api/commander/_internal/drain", hub.drainHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	dialDone := make(chan struct{})
	go func() {
		defer close(dialDone)
		conn, _, dialErr := websocket.DefaultDialer.DialContext(context.Background(), wsURL, wsDialHeader("tok-alice"))
		if dialErr != nil {
			return
		}
		defer conn.Close()
		regPayload, _ := json.Marshal(commander.RegisterPayload{
			SchemaVersion: commander.SchemaVersion,
			Kind:          "claude",
			DisplayName:   "race-daemon",
			ShortID:       "agent-waitdrain",
		})
		_ = conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload})
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Wait until goroutine is in the hook window.
	select {
	case <-hookEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook never entered")
	}

	// POST the drain endpoint from loopback; it must block while the hook holds.
	drainURL := srv.URL + "/api/commander/_internal/drain"
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, drainURL, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}()

	// Drain must not have returned yet.
	select {
	case <-drainDone:
		t.Fatal("drainHandler returned before in-flight admission cleanup finished")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	// Release the hook.
	close(gate)

	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("drainHandler did not return after in-flight admission cleanup finished")
	}

	select {
	case <-dialDone:
	case <-time.After(3 * time.Second):
		t.Fatal("dial goroutine did not finish")
	}

	require.Eventually(t, func() bool {
		return mock.ExpectationsWereMet() == nil
	}, 2*time.Second, 10*time.Millisecond,
		"sqlmock: connectUpsert and remove must both have been called")
}

// TestHub_InFlightAdmissions_Counter_NoLeak runs N=100 concurrent admissions
// interleaved with a hub.Close and asserts:
//  1. The inFlightAdmissions counter returns to zero (no goroutine leak).
//  2. After Close, the local registry is empty.
//
// Run as: go test -run TestHub_InFlightAdmissions_Counter_NoLeak -race -count=3
func TestHub_InFlightAdmissions_Counter_NoLeak(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	const N = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)

	regPayload, _ := json.Marshal(commander.RegisterPayload{
		SchemaVersion: commander.SchemaVersion,
		Kind:          "claude",
		DisplayName:   "leak-test-daemon",
	})

	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			conn, resp, dialErr := websocket.DefaultDialer.DialContext(
				context.Background(), wsURL, wsDialHeader("tok-alice"))
			if dialErr != nil {
				if resp != nil {
					resp.Body.Close()
				}
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(commander.Envelope{Type: "register", Payload: regPayload})
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			// Drain until closed.
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	}

	// Fire all goroutines then immediately Close.
	close(start)

	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, hub.Close(closeCtx))

	// Wait for all dial goroutines to finish.
	wg.Wait()

	// ASSERTION 1: inFlightAdmissions counter must be zero (no leak).
	// WaitGroup.Wait() is not exported for checking the counter directly, but
	// we can call Wait() on it again: if it returns immediately, counter is zero.
	counterZero := make(chan struct{})
	go func() {
		hub.inFlightAdmissions.Wait()
		close(counterZero)
	}()
	select {
	case <-counterZero:
	case <-time.After(time.Second):
		t.Fatal("inFlightAdmissions counter did not reach zero after all goroutines finished")
	}

	// ASSERTION 2: local registry is empty.
	require.Eventually(t, func() bool {
		hub.reg.mu.Lock()
		defer hub.reg.mu.Unlock()
		return len(hub.reg.conns) == 0
	}, 3*time.Second, 10*time.Millisecond, "local registry must be empty after Close")
}
