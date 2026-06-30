package commanderhub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/commander"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeForwardServer returns an httptest.Server that serves
// /api/commander/_internal/forward. The given handler func is invoked for
// each request. The caller is responsible for closing the server.
func makeForwardServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/commander/_internal/forward", h)
	return httptest.NewServer(mux)
}

// okJSONResponse writes a forwardResponse with a JSON result payload.
func okJSONResponse(w http.ResponseWriter, result any) {
	raw, _ := json.Marshal(result)
	fr := forwardResponse{Result: raw}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fr)
}

// errorJSONResponse writes a forwardResponse with an application-level error.
func errorJSONResponse(w http.ResponseWriter, code, message string) {
	fr := forwardResponse{Error: &forwardRespErr{Code: code, Message: message}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fr)
}

// writeStreamEnvelopes writes a sequence of Envelopes using the length-prefix codec.
func writeStreamEnvelopes(w io.Writer, envs ...commander.Envelope) error {
	enc := NewEnvelopeEncoder(w)
	for _, e := range envs {
		if err := enc.Encode(&e); err != nil {
			return err
		}
	}
	return nil
}

// newTestClient creates a forwardClient pointing at self=http://test-pod:8091.
func newTestClient(secret, prevSecret string) *forwardClient {
	return newForwardClient([]byte(secret), []byte(prevSecret), "http://test-pod:8091")
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_RoundTrip
// ---------------------------------------------------------------------------

func TestForwardClient_Send_RoundTrip(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NotEmpty(t, r.Header.Get("X-Forward-Ts"))
		require.NotEmpty(t, r.Header.Get("X-Forward-Nonce"))
		require.NotEmpty(t, r.Header.Get("X-Forward-Sig"))

		okJSONResponse(w, map[string]string{"sessions": "[]"})
	})
	defer srv.Close()

	fc := newTestClient("secret1", "")
	req := forwardRequest{
		UserID:      "u1",
		WorkspaceID: "w1",
		DaemonID:    "d1",
		Command:     "list_sessions",
	}
	result, err := fc.send(context.Background(), srv.URL, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	// The result is a JSON object; just confirm it decoded.
	var out map[string]string
	require.NoError(t, json.Unmarshal(result, &out))
	require.Equal(t, "[]", out["sessions"])
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_RetryOnPrevSecret
// ---------------------------------------------------------------------------

func TestForwardClient_Send_RetryOnPrevSecret(t *testing.T) {
	callCount := 0
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First attempt: reject with 403.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Second attempt (with prevSecret): succeed.
		okJSONResponse(w, map[string]string{"ok": "true"})
	})
	defer srv.Close()

	fc := newTestClient("new-secret", "old-secret")
	req := forwardRequest{
		UserID:      "u1",
		WorkspaceID: "w1",
		DaemonID:    "d1",
		Command:     "list_sessions",
	}
	result, err := fc.send(context.Background(), srv.URL, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, callCount, "should have retried exactly once")
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_404_MapsToErrDaemonNotFound
// ---------------------------------------------------------------------------

func TestForwardClient_Send_404_MapsToErrDaemonNotFound(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}
	_, err := fc.send(context.Background(), srv.URL, req)
	require.ErrorIs(t, err, ErrDaemonNotFound)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_426_MapsToDaemonUpgradeRequired
// ---------------------------------------------------------------------------

func TestForwardClient_Send_426_MapsToDaemonUpgradeRequired(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}
	_, err := fc.send(context.Background(), srv.URL, req)
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	require.Equal(t, commander.ErrCodeDaemonUpgradeRequired, de.Code)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Stream_RoundTrip
// ---------------------------------------------------------------------------

func TestForwardClient_Stream_RoundTrip(t *testing.T) {
	envs := []commander.Envelope{
		{Type: "event", ID: "1"},
		{Type: "command_result", ID: "1"},
	}

	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request is marked as streaming.
		var fr forwardRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&fr))
		require.True(t, fr.Stream, "stream field must be true")

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Write envelopes using the codec.
		_ = writeStreamEnvelopes(w, envs...)
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	req := forwardRequest{
		UserID:      "u",
		WorkspaceID: "w",
		DaemonID:    "d",
		Command:     "session_turn",
		Stream:      true,
	}
	ch, err := fc.stream(context.Background(), srv.URL, req)
	require.NoError(t, err)
	require.NotNil(t, ch)

	var received []commander.Envelope
	for env := range ch {
		received = append(received, env)
	}
	require.Len(t, received, 2, "should receive 2 envelopes")
	require.Equal(t, "event", received[0].Type)
	require.Equal(t, "command_result", received[1].Type)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_OversizedBody_Rejected
// ---------------------------------------------------------------------------

func TestForwardClient_Send_OversizedBody_Rejected(t *testing.T) {
	// Build a request with > 1.5 MiB args payload.
	bigArgs, _ := json.Marshal(strings.Repeat("x", maxForwardBodySize+1))
	req := forwardRequest{
		UserID:      "u",
		WorkspaceID: "w",
		DaemonID:    "d",
		Command:     "session_turn",
		Args:        bigArgs,
	}
	// Confirm that marshalling yields a large body.
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	require.Greater(t, len(raw), maxForwardBodySize, "test setup: body must exceed limit")

	// The client should refuse without dialing.
	fc := newTestClient("secret", "")
	// Use a URL that will never be dialed.
	_, sendErr := fc.send(context.Background(), "http://unreachable-pod:9999", req)
	require.Error(t, sendErr)
	require.Contains(t, sendErr.Error(), "too large")
}

// ---------------------------------------------------------------------------
// TestForwardClient_Stream_CancelClosesChannel
// ---------------------------------------------------------------------------

func TestForwardClient_Stream_CancelClosesChannel(t *testing.T) {
	// Server streams slowly — but we cancel before it finishes.
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Write one envelope then block.
		env := commander.Envelope{Type: "event", ID: "1"}
		_ = writeStreamEnvelopes(w, env)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Block until the client disconnects.
		<-r.Context().Done()
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	req := forwardRequest{
		UserID:      "u",
		WorkspaceID: "w",
		DaemonID:    "d",
		Command:     "session_turn",
		Stream:      true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := fc.stream(ctx, srv.URL, req)
	require.NoError(t, err)

	// Read the first envelope.
	select {
	case _, ok := <-ch:
		require.True(t, ok, "first envelope should be received")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first envelope")
	}

	// Cancel — channel should close within 1s.
	cancel()
	select {
	case _, open := <-ch:
		require.False(t, open, "channel must be closed after cancel")
	case <-time.After(1 * time.Second):
		t.Fatal("channel did not close within 1s after context cancel")
	}
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_NeitherSecretMatches_Errors
// ---------------------------------------------------------------------------

func TestForwardClient_Send_NeitherSecretMatches_Errors(t *testing.T) {
	// Server always returns 403 (wrong secret, no prev).
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	defer srv.Close()

	fc := newTestClient("wrong-secret", "") // no prevSecret
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}
	_, err := fc.send(context.Background(), srv.URL, req)
	require.ErrorIs(t, err, ErrDaemonGone)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_LoopRefused_SelfURL
// ---------------------------------------------------------------------------

func TestForwardClient_Send_LoopRefused_SelfURL(t *testing.T) {
	selfURL := "http://test-pod:8091"
	fc := newForwardClient([]byte("secret"), nil, selfURL)
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}

	// Should refuse to forward to self.
	_, err := fc.send(context.Background(), selfURL, req)
	require.ErrorIs(t, err, ErrDaemonNotFound, "self URL must return ErrDaemonNotFound")
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_LoopRefused_LoopbackURL
// ---------------------------------------------------------------------------

func TestForwardClient_Send_LoopRefused_LoopbackURL(t *testing.T) {
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}

	cases := []struct {
		name         string
		advertiseURL string // self
		peerURL      string
	}{
		// 127.0.0.1: self is also on 127.0.0.1 — caught by loopback self-match.
		{"127.0.0.1", "http://127.0.0.1:8091", "http://127.0.0.1:8091"},
		// localhost: named loopback — always blocked regardless of self.
		{"localhost", "http://real-pod:8091", "http://localhost:8091"},
		// [::1]: named loopback — always blocked regardless of self.
		{"[::1]", "http://real-pod:8091", "http://[::1]:8091"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newForwardClient([]byte("secret"), nil, tc.advertiseURL)
			_, err := fc.send(context.Background(), tc.peerURL, req)
			require.ErrorIs(t, err, ErrDaemonNotFound, "loopback %q must return ErrDaemonNotFound", tc.peerURL)
		})
	}
}

// ---------------------------------------------------------------------------
// Additional: 5xx → ErrDaemonGone
// ---------------------------------------------------------------------------

func TestForwardClient_Send_5xx_MapsToErrDaemonGone(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}
	_, err := fc.send(context.Background(), srv.URL, req)
	require.ErrorIs(t, err, ErrDaemonGone)
}

// ---------------------------------------------------------------------------
// Additional: application-level error in 200 body → *DaemonError
// ---------------------------------------------------------------------------

func TestForwardClient_Send_AppError_ReturnsDaemonError(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		errorJSONResponse(w, "session_not_found", "session abc not found")
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "get_session"}
	_, err := fc.send(context.Background(), srv.URL, req)
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	require.Equal(t, "session_not_found", de.Code)
	require.Equal(t, "session abc not found", de.Message)
}

// ---------------------------------------------------------------------------
// Compile-time check: forwardClient fields exist.
// ---------------------------------------------------------------------------

var _ = func() *forwardClient {
	return newForwardClient([]byte("s"), []byte("p"), "http://a:1")
}

// Compile-time: Hub has forwardCli field (accessed via struct literal, not nil deref).
func _hubHasForwardCli() {
	var h Hub
	_ = h.forwardCli
}

// Compile-time: forwardHMACTimestampWindow constant exists.
var _ = forwardHMACTimestampWindow

// Compile-time: forwardNonceHexLen constant exists.
var _ = forwardNonceHexLen

// Compile-time: bytes.NewReader is imported (prevents unused import warning).
var _ = bytes.NewReader
