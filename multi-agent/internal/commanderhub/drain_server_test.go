package commanderhub

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

// ---------------------------------------------------------------------------
// Drain auth helpers
// ---------------------------------------------------------------------------

// drainHubWithDB builds a Hub in cluster (shared) mode with the provided db.
// cluster.Secret is set to the given secret for HMAC signing.
func drainHubWithDB(t *testing.T, db *sql.DB, secret []byte) *Hub {
	t.Helper()
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	h := NewHub(resolver)
	sr := newSharedRegistry(db, "http://self-pod:9000")
	h.attachSharedRegistry(sr)
	h.cluster = ClusterRuntime{
		DB:           db,
		AdvertiseURL: "http://self-pod:9000",
		Secret:       secret,
	}
	return h
}

// signedDrainRequest builds a signed HTTP POST request for the drain handler.
func signedDrainRequest(t *testing.T, body []byte, secret []byte) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	nonce, err := freshNonce()
	require.NoError(t, err)
	sig := signDrain(secret, ts, nonce, body)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/drain", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.5:12345" // non-loopback so auth is required
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Forward-Ts", formatInt64(ts))
	req.Header.Set("X-Forward-Nonce", nonce)
	req.Header.Set("X-Forward-Sig", sig)
	return req
}

// TestIsLoopbackRemoteAddr_Loopback tests that loopback addresses are recognized.
func TestIsLoopbackRemoteAddr_Loopback(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:12345", true},
		{"127.1.1.1:8080", true},
		{"[::1]:8080", true}, // IPv6 loopback in brackets (standard format)
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := isLoopbackRemoteAddr(tt.addr)
			require.Equal(t, tt.want, got, "loopback check for %s", tt.addr)
		})
	}
}

// TestIsLoopbackRemoteAddr_NonLoopback tests that non-loopback addresses are rejected.
func TestIsLoopbackRemoteAddr_NonLoopback(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"10.0.0.5:12345", false},
		{"192.168.1.1:12345", false},
		{"8.8.8.8:443", false},
		{"example.com:80", false},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := isLoopbackRemoteAddr(tt.addr)
			require.Equal(t, tt.want, got, "loopback check for %s should return %v", tt.addr, tt.want)
		})
	}
}

// TestDrainHandler_LoopbackBypass tests that loopback requests succeed without HMAC.
func TestDrainHandler_LoopbackBypass(t *testing.T) {
	// This test verifies that isLoopbackRemoteAddr is called correctly.
	// A full integration test would require mocking daemon connections.
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/drain", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h := NewHub(&fakeResolver{mu: map[string]identity.Identity{}})

	// Should succeed (even without HMAC) because loopback.
	h.drainHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code, "loopback drain should return 200 OK")
}

// TestDrainHandler_NonLoopbackRequiresAuth tests that non-loopback requests
// are rejected when the hub is not in cluster mode (sharedReg == nil).
// The expected status is 503 — consistent with forwardHandler step-0 guard —
// because the drain endpoint can only be authenticated in cluster mode.
func TestDrainHandler_NonLoopbackRequiresAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/drain", nil)
	req.RemoteAddr = "10.0.0.5:12345"

	w := httptest.NewRecorder()
	h := NewHub(&fakeResolver{mu: map[string]identity.Identity{}})

	// Should fail because non-loopback and not in cluster mode (sharedReg == nil).
	// Returns 503 (backend_unavailable) matching forwardHandler step-0.
	h.drainHandler(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "non-loopback without cluster mode must return 503")
}

// TestDrainHandler_MethodNotAllowed tests that invalid methods are rejected.
func TestDrainHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/commander/_internal/drain", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h := NewHub(&fakeResolver{mu: map[string]identity.Identity{}})

	h.drainHandler(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code, "DELETE should return 405")
}

// TestDrainHandler_GetMethodAllowed tests that GET method is allowed.
func TestDrainHandler_GetMethodAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/commander/_internal/drain", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h := NewHub(&fakeResolver{mu: map[string]identity.Identity{}})

	h.drainHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code, "GET from loopback should return 200 OK")
}

// ---------------------------------------------------------------------------
// Fix #1: drain nonce insert + domain separation tests
// ---------------------------------------------------------------------------

// TestDrain_NonceReplay_Rejected verifies that a second drain with the same
// nonce is rejected with 403 (replay detection via insertNonce).
func TestDrain_NonceReplay_Rejected(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	secret := []byte("drain-secret")
	h := drainHubWithDB(t, db, secret)
	body := []byte(`{}`)

	// Simulate replay: nonce already in DB (0 rows affected).
	mock.ExpectExec(insertNonceSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	req := signedDrainRequest(t, body, secret)
	w := httptest.NewRecorder()
	h.drainHandler(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, "replayed drain nonce must be rejected with 403")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDrain_ReplayForwardRequest_Rejected verifies that a request signed with
// signForward (purpose="forward") is rejected at the drain endpoint because
// verifyDrain uses purpose="drain" — preventing cross-endpoint replay.
func TestDrain_ReplayForwardRequest_Rejected(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	secret := []byte("shared-secret")
	h := drainHubWithDB(t, db, secret)
	body := []byte(`{}`)

	// Sign with "forward" purpose — should NOT validate at /drain.
	ts := time.Now().Unix()
	nonce, nerr := freshNonce()
	require.NoError(t, nerr)
	sig := signForward(secret, ts, nonce, body) // wrong purpose

	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/drain", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.5:12345"
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Forward-Ts", formatInt64(ts))
	req.Header.Set("X-Forward-Nonce", nonce)
	req.Header.Set("X-Forward-Sig", sig)

	w := httptest.NewRecorder()
	h.drainHandler(w, req)

	// HMAC mismatch due to purpose prefix → 403 before any nonce insert.
	require.Equal(t, http.StatusForbidden, w.Code, "forward-signed request must be rejected at /drain (purpose mismatch)")
	// No nonce insert expectation means sqlmock would fail if insertNonce was called.
}

// TestDrain_NilDB_503 verifies that a Hub with sharedReg set but db == nil
// returns 503 (not a panic) when a non-loopback drain request arrives.
// This is the C5 follow-up guard that mirrors forwardHandler step 0.
func TestDrain_NilDB_503(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	h := NewHub(resolver)
	// Attach a sharedRegistry whose db field is explicitly nil.
	sr := &sharedRegistry{db: nil, advertiseURL: "http://self-pod:9000"}
	h.attachSharedRegistry(sr)
	h.cluster = ClusterRuntime{
		Secret:       []byte("some-secret"),
		AdvertiseURL: "http://self-pod:9000",
	}

	// Non-loopback request → verifyDrainAuth is called → must hit the nil-DB guard.
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/drain", nil)
	req.RemoteAddr = "10.0.0.5:12345"

	w := httptest.NewRecorder()
	// Must not panic.
	h.drainHandler(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "nil DB must return 503 not panic")
}

// TestDrain_NoncePGError_503 verifies that when insertNonce returns a PG error,
// the drain endpoint responds with 503 (fail closed).
func TestDrain_NoncePGError_503(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	secret := []byte("drain-secret")
	h := drainHubWithDB(t, db, secret)
	body := []byte(`{}`)

	// PG error on nonce insert → fail closed.
	mock.ExpectExec(insertNonceSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)

	req := signedDrainRequest(t, body, secret)
	w := httptest.NewRecorder()
	h.drainHandler(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "PG error must return 503 (fail closed)")
	require.NoError(t, mock.ExpectationsWereMet())
}
