package commanderhub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	gorilla_ws "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// forwardHubWithDB builds a Hub in cluster mode with the provided db.
// cluster.Secret is set to testSecret for HMAC signing.
const testSecret = "test-cluster-secret"
const testPeerURL = "http://peer-pod:9000"

func forwardHubWithDB(t *testing.T, db *sql.DB) *Hub {
	t.Helper()
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	h := NewHub(resolver)
	sr := newSharedRegistry(db, "http://self-pod:9000")
	h.attachSharedRegistry(sr)
	h.cluster = ClusterRuntime{
		DB:           db,
		AdvertiseURL: "http://self-pod:9000",
		Secret:       []byte(testSecret),
	}
	return h
}

// signedForwardRequest builds a signed HTTP POST request for the forward handler.
func signedForwardRequest(t *testing.T, body []byte, secret string) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	nonce, err := freshNonce()
	require.NoError(t, err)
	sig := signForward([]byte(secret), ts, nonce, body)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Forward-Ts", formatInt64(ts))
	req.Header.Set("X-Forward-Nonce", nonce)
	req.Header.Set("X-Forward-Sig", sig)
	return req
}

func formatInt64(n int64) string {
	return strings.TrimSpace(string(append([]byte(nil), intToDecimalBytes(n)...)))
}

func intToDecimalBytes(n int64) []byte {
	if n == 0 {
		return []byte("0")
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return buf[pos:]
}

// forwardWireBody marshals a forwardRequest to JSON.
func forwardWireBody(t *testing.T, req forwardRequest) []byte {
	t.Helper()
	b, err := json.Marshal(req)
	require.NoError(t, err)
	return b
}

// setupInsertNonce adds sqlmock expectations for insertNonce succeeding.
func expectNonceInsert(mock sqlmock.Sqlmock, inserted bool) {
	result := sqlmock.NewResult(0, 0)
	if inserted {
		result = sqlmock.NewResult(0, 1)
	}
	mock.ExpectExec(insertNonceSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(result)
}

// ---------------------------------------------------------------------------
// Test 0: Receiver not in shared mode → 503
// ---------------------------------------------------------------------------

func TestForwardServer_ReceiverNotSharedMode_503(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	h := NewHub(resolver) // no sharedReg, no cluster.Secret

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", strings.NewReader("{}"))
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	errMap, ok := body["error"].(map[string]any)
	require.True(t, ok, "expected error key")
	require.Equal(t, "backend_unavailable", errMap["code"])
}

// ---------------------------------------------------------------------------
// Test 2: 405 — non-POST method
// ---------------------------------------------------------------------------

func TestForwardServer_405_Method(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/commander/_internal/forward", nil)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 3: 413 — Content-Length exceeds cap
// ---------------------------------------------------------------------------

func TestForwardServer_413_ContentLength(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", strings.NewReader("{}"))
	req.ContentLength = int64(maxForwardBodySize) + 1
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 4: 400 — missing headers
// ---------------------------------------------------------------------------

func TestForwardServer_400_MissingTimestamp(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", strings.NewReader("{}"))
	// No X-Forward-Ts header
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestForwardServer_400_MissingNonce(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", strings.NewReader("{}"))
	req.Header.Set("X-Forward-Ts", "12345678")
	// No X-Forward-Nonce
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestForwardServer_400_MissingAuth(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	nonce, nerr := freshNonce()
	require.NoError(t, nerr)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", strings.NewReader("{}"))
	req.Header.Set("X-Forward-Ts", "12345678")
	req.Header.Set("X-Forward-Nonce", nonce)
	// No X-Forward-Sig → empty → not 64 hex chars
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 5: 400 — malformed headers
// ---------------------------------------------------------------------------

func TestForwardServer_400_MalformedHeader(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", strings.NewReader("{}"))
	req.Header.Set("X-Forward-Ts", "not-a-number")
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 6: 403 — timestamp drift
// ---------------------------------------------------------------------------

func TestForwardServer_403_TimestampDrift(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	// Timestamp from 10 minutes ago — outside the 60s window.
	staleTS := time.Now().Add(-10 * time.Minute).Unix()

	nonce, nerr := freshNonce()
	require.NoError(t, nerr)
	body := []byte(`{}`)
	sig := signForward([]byte(testSecret), staleTS, nonce, body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Forward-Ts", formatInt64(staleTS))
	req.Header.Set("X-Forward-Nonce", nonce)
	req.Header.Set("X-Forward-Sig", sig)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 7: 413 — body over cap after reading
// ---------------------------------------------------------------------------

func TestForwardServer_413_BodyOverCap(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	// Build a body that exceeds the cap (we'll send via io.LimitReader bypass).
	// We construct a valid TS/nonce/sig but body size > maxForwardBodySize.
	// Use an unlimited content-length so step 2 passes, but step 7 rejects.
	bigBody := bytes.Repeat([]byte("x"), maxForwardBodySize+2)
	ts := time.Now().Unix()
	nonce, nerr := freshNonce()
	require.NoError(t, nerr)
	sig := signForward([]byte(testSecret), ts, nonce, bigBody)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", bytes.NewReader(bigBody))
	req.ContentLength = -1 // unknown length — bypasses step 2
	req.Header.Set("X-Forward-Ts", formatInt64(ts))
	req.Header.Set("X-Forward-Nonce", nonce)
	req.Header.Set("X-Forward-Sig", sig)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 8: 403 — HMAC mismatch
// ---------------------------------------------------------------------------

func TestForwardServer_403_HMACMismatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d1", Command: "list_sessions",
	})
	ts := time.Now().Unix()
	nonce, nerr := freshNonce()
	require.NoError(t, nerr)
	// Sign with wrong key.
	sig := signForward([]byte("wrong-secret"), ts, nonce, body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/forward", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Forward-Ts", formatInt64(ts))
	req.Header.Set("X-Forward-Nonce", nonce)
	req.Header.Set("X-Forward-Sig", sig)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 9: 503 — nonce PG unavailable (fail closed)
// ---------------------------------------------------------------------------

func TestForwardServer_503_NoncePGUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d1", Command: "list_sessions",
	})
	req := signedForwardRequest(t, body, testSecret)
	w := httptest.NewRecorder()

	// Nonce insert returns a PG error.
	mock.ExpectExec(insertNonceSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)

	h.forwardHandler(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 10: 403 — nonce replay
// ---------------------------------------------------------------------------

func TestForwardServer_403_NonceReplay(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d1", Command: "list_sessions",
	})
	req := signedForwardRequest(t, body, testSecret)
	w := httptest.NewRecorder()

	// 0 rows affected = replay.
	expectNonceInsert(mock, false)

	h.forwardHandler(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 11: 404 — daemon not in local registry (no lookupRemote — loop prevention)
// ---------------------------------------------------------------------------

func TestForwardServer_404_DaemonNotInLocalRegistry(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "unknown-daemon", Command: "list_sessions",
	})
	req := signedForwardRequest(t, body, testSecret)
	w := httptest.NewRecorder()

	// Only expect the nonce insert — NO expectation for lookupRemoteSQL.
	expectNonceInsert(mock, true)

	h.forwardHandler(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	// Verify no unexpected SQL (especially no lookupRemoteSQL).
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 12: 426 — daemon missing file_preview_encoded_cap
// ---------------------------------------------------------------------------

func TestForwardServer_426_DaemonMissingCapability(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)

	// Register a daemon without CapabilityFilePreviewEncodedCap.
	o := owner{userID: "alice", workspaceID: "W1"}
	dc := &daemonConn{
		id:      "conn-1",
		shortID: "d1",
		owner:   o,
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}),
		hub:     h,
	}
	dc.metaMu.Lock()
	dc.capabilities = map[string]bool{
		commander.CapabilitySessions: true,
		commander.CapabilityTurn:     true,
		// No CapabilityFilePreviewEncodedCap
	}
	dc.metaMu.Unlock()
	h.reg.add(dc)

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d1", Command: "read_file",
	})
	req := signedForwardRequest(t, body, testSecret)
	w := httptest.NewRecorder()

	expectNonceInsert(mock, true)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusUpgradeRequired, w.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	errMap, _ := respBody["error"].(map[string]any)
	require.Equal(t, commander.ErrCodeDaemonUpgradeRequired, errMap["code"])
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Test 1: AcceptsValidRequest (non-streaming round-trip)
// ---------------------------------------------------------------------------

// wsPair returns a gorilla WS server-side and client-side connection over a
// loopback httptest.Server. The server-side conn is what the hub uses as
// dc.conn. The client-side conn simulates the daemon process — reads commands
// the hub writes and sends replies back. Both are registered for cleanup.
func wsPair(t *testing.T) (serverConn, clientConn *gorilla_ws.Conn) {
	t.Helper()
	upgrader := gorilla_ws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverCh := make(chan *gorilla_ws.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("server upgrade: %v", err)
			return
		}
		serverCh <- c
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	cc, _, err := gorilla_ws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	select {
	case sc := <-serverCh:
		t.Cleanup(func() { _ = sc.Close() })
		return sc, cc
	case <-time.After(2 * time.Second):
		t.Fatal("WS server upgrade timeout")
		return nil, nil
	}
}

// setupRawDaemonInHub injects a daemonConn (with shortID "d1") into h's registry.
// A goroutine on the client-side WS conn reads commands and replies with a
// list_sessions command_result.
func setupRawDaemonInHub(t *testing.T, h *Hub, o owner) {
	t.Helper()
	serverConn, clientConn := wsPair(t)

	dc := &daemonConn{
		id:      "conn-d1",
		shortID: "d1",
		owner:   o,
		conn:    serverConn, // hub writes commands here → client receives them
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}),
		hub:     nil, // hub=nil → confirmOwnership returns true
	}
	dc.metaMu.Lock()
	dc.capabilities = map[string]bool{
		commander.CapabilitySessions:              true,
		commander.CapabilityTurn:                  true,
		commander.CapabilityFilePreviewEncodedCap: true,
	}
	dc.metaMu.Unlock()
	h.reg.add(dc)

	// Simulate daemon process: reads from clientConn, sends command_result back.
	go func() {
		for {
			var env commander.Envelope
			if err := clientConn.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != "command" {
				continue
			}
			result := json.RawMessage(`{"sessions":[{"id":"s1"}]}`)
			_ = clientConn.WriteJSON(commander.Envelope{
				Type:    "command_result",
				ID:      env.ID,
				Payload: result,
			})
		}
	}()
	// Also start the hub-side read loop so routeFrame delivers replies.
	go dc.readLoop()
}

func TestForwardServer_AcceptsValidRequest(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)
	o := owner{userID: "alice", workspaceID: "W1"}
	setupRawDaemonInHub(t, h, o)

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d1", Command: "list_sessions",
	})
	req := signedForwardRequest(t, body, testSecret)
	w := httptest.NewRecorder()

	expectNonceInsert(mock, true)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())

	var fr forwardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fr))
	require.Nil(t, fr.Error)
	require.Contains(t, string(fr.Result), "s1")
}

// ---------------------------------------------------------------------------
// Test 13: Streaming round-trip
// ---------------------------------------------------------------------------

func TestForwardServer_Streaming_RoundTrip(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)
	o := owner{userID: "alice", workspaceID: "W1"}

	// Set up a daemon that emits two event frames then a command_result terminal.
	serverConn2, clientConn2 := wsPair(t)
	dc := &daemonConn{
		id:      "conn-d2",
		shortID: "d2",
		owner:   o,
		conn:    serverConn2,
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}),
		hub:     nil, // hub=nil → confirmOwnership returns true
	}
	dc.metaMu.Lock()
	dc.capabilities = map[string]bool{
		commander.CapabilitySessions: true,
		commander.CapabilityTurn:     true,
	}
	dc.metaMu.Unlock()
	h.reg.add(dc)
	go dc.readLoop()

	go func() {
		var env commander.Envelope
		if err := clientConn2.ReadJSON(&env); err != nil {
			return
		}
		if env.Type != "command" {
			return
		}
		// Send two event frames then terminal command_result.
		evtPayload, _ := json.Marshal(map[string]string{"text": "chunk1"})
		_ = clientConn2.WriteJSON(commander.Envelope{Type: "event", ID: env.ID, Payload: evtPayload})
		evtPayload2, _ := json.Marshal(map[string]string{"text": "chunk2"})
		_ = clientConn2.WriteJSON(commander.Envelope{Type: "event", ID: env.ID, Payload: evtPayload2})
		result := json.RawMessage(`{"done":true}`)
		_ = clientConn2.WriteJSON(commander.Envelope{Type: "command_result", ID: env.ID, Payload: result})
	}()

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d2",
		Command: "session_turn", Stream: true,
	})
	req := signedForwardRequest(t, body, testSecret)
	w := httptest.NewRecorder()

	expectNonceInsert(mock, true)
	h.forwardHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/octet-stream", w.Header().Get("Content-Type"))
	require.NoError(t, mock.ExpectationsWereMet())

	// Decode the stream: expect 2 events + 1 command_result.
	dec := NewEnvelopeDecoder(bytes.NewReader(w.Body.Bytes()))
	var envelopes []commander.Envelope
	for {
		env, err := dec.Decode()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		envelopes = append(envelopes, *env)
	}
	require.Len(t, envelopes, 3)
	require.Equal(t, "event", envelopes[0].Type)
	require.Equal(t, "event", envelopes[1].Type)
	require.Equal(t, "command_result", envelopes[2].Type)
}

// ---------------------------------------------------------------------------
// Test 14: Streaming — cancel propagates
// ---------------------------------------------------------------------------

func TestForwardServer_Streaming_CancelPropagates(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	h := forwardHubWithDB(t, db)
	o := owner{userID: "alice", workspaceID: "W1"}

	// Set up a daemon that sends one event then blocks waiting for context cancel.
	commandReceived := make(chan string, 1)
	serverConn3, clientConn3 := wsPair(t)
	dc := &daemonConn{
		id:      "conn-d3",
		shortID: "d3",
		owner:   o,
		conn:    serverConn3,
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}),
		hub:     nil, // hub=nil → confirmOwnership returns true
	}
	dc.metaMu.Lock()
	dc.capabilities = map[string]bool{
		commander.CapabilitySessions: true,
		commander.CapabilityTurn:     true,
	}
	dc.metaMu.Unlock()
	h.reg.add(dc)
	go dc.readLoop()

	go func() {
		var env commander.Envelope
		if err := clientConn3.ReadJSON(&env); err != nil {
			return
		}
		commandReceived <- env.ID
		// Send one event frame, then block (never sends terminal).
		evtPayload, _ := json.Marshal(map[string]string{"text": "hello"})
		_ = clientConn3.WriteJSON(commander.Envelope{Type: "event", ID: env.ID, Payload: evtPayload})
		// Block; the test will cancel the request context.
		time.Sleep(5 * time.Second)
	}()

	body := forwardWireBody(t, forwardRequest{
		UserID: "alice", WorkspaceID: "W1", DaemonID: "d3",
		Command: "session_turn", Stream: true,
	})

	// Use a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())

	expectNonceInsert(mock, true)

	req := signedForwardRequest(t, body, testSecret)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.forwardHandler(w, req)
	}()

	// Wait for the daemon to receive the command, then cancel the request.
	select {
	case <-commandReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not receive command in time")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardHandler did not return after cancel")
	}

	// The response status should be 200 (headers were written before cancel).
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Ensure fakeResolver is usable (compile check for identity import)
// ---------------------------------------------------------------------------

var _ identity.Resolver = (*fakeResolver)(nil)

// tbStreamBackend is a minimal backend that emits events for streaming tests.
type tbStreamBackend struct {
	resumeFn func(context.Context, agentbackend.SessionRef, string, executor.Sink) (executor.Result, error)
}

func (b *tbStreamBackend) Kind() agentbackend.Kind { return agentbackend.KindClaude }
func (b *tbStreamBackend) Run(context.Context, executor.Task, executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (b *tbStreamBackend) RunResume(ctx context.Context, ref agentbackend.SessionRef, ans string, sink executor.Sink) (executor.Result, error) {
	if b.resumeFn != nil {
		return b.resumeFn(ctx, ref, ans, sink)
	}
	return executor.Result{}, nil
}
func (b *tbStreamBackend) LLM() agentbackend.LLMRunner                { return nil }
func (b *tbStreamBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (b *tbStreamBackend) Detect(context.Context) error               { return nil }
func (b *tbStreamBackend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	return nil, nil
}
func (b *tbStreamBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return agentbackend.Session{}, nil, nil
}
