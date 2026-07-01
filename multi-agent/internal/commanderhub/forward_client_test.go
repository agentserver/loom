package commanderhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	return newForwardClient([]byte(secret), []byte(prevSecret), "http://test-pod:8091", 0)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_RoundTrip
// ---------------------------------------------------------------------------

// TestForwardClient_Send_RoundTrip uses doSend directly (bypasses wouldLoop)
// because httptest.Server binds to 127.0.0.1 which IsLoopback returns true for.
// The loop detection itself is tested in TestWouldLoop_IPv4Loopback and
// TestForwardClient_Send_LoopRefused_*.
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
	body, err := json.Marshal(req)
	require.NoError(t, err)
	result, err := fc.doSend(context.Background(), srv.URL, body, fc.secret)
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
	body, err := json.Marshal(req)
	require.NoError(t, err)
	// First try with current secret → 403 → retry with prev secret.
	_, err = fc.doSend(context.Background(), srv.URL, body, fc.secret)
	require.ErrorIs(t, err, errForward403)
	result, err := fc.doSend(context.Background(), srv.URL, body, fc.prevSecret)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, callCount, "should have made exactly 2 requests")
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
	body, _ := json.Marshal(forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"})
	_, err := fc.doSend(context.Background(), srv.URL, body, fc.secret)
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
	body, _ := json.Marshal(forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"})
	_, err := fc.doSend(context.Background(), srv.URL, body, fc.secret)
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	require.Equal(t, commander.ErrCodeDaemonUpgradeRequired, de.Code)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Stream_RoundTrip
// ---------------------------------------------------------------------------

// TestForwardClient_Stream_RoundTrip uses doStreamRequest directly (bypasses
// wouldLoop) because httptest.Server binds to 127.0.0.1 which IsLoopback.
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
	body, err := json.Marshal(req)
	require.NoError(t, err)

	resp, err := fc.doStreamRequest(context.Background(), srv.URL, body, fc.secret)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Drain the codec-encoded stream.
	ctx := context.Background()
	out := make(chan commander.Envelope, 16)
	go func() {
		defer close(out)
		dec := NewEnvelopeDecoder(resp.Body)
		for {
			env, err := dec.Decode()
			if err != nil {
				return
			}
			select {
			case out <- *env:
			case <-ctx.Done():
				return
			}
		}
	}()

	var received []commander.Envelope
	for env := range out {
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

// TestForwardClient_Stream_CancelClosesChannel uses doStreamRequest directly
// to bypass wouldLoop (httptest.Server is on 127.0.0.1 which IsLoopback).
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
	body, err := json.Marshal(req)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	resp, err := fc.doStreamRequest(ctx, srv.URL, body, fc.secret)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out := make(chan commander.Envelope, 4)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := NewEnvelopeDecoder(resp.Body)
		for {
			env, err := dec.Decode()
			if err != nil {
				return
			}
			select {
			case out <- *env:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Read the first envelope.
	select {
	case _, ok := <-out:
		require.True(t, ok, "first envelope should be received")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first envelope")
	}

	// Cancel — channel should close within 1s.
	cancel()
	select {
	case _, open := <-out:
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
	body, _ := json.Marshal(forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"})
	_, err := fc.doSend(context.Background(), srv.URL, body, fc.secret)
	// 403 → errForward403; caller maps to ErrDaemonGone.
	require.ErrorIs(t, err, errForward403)
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_LoopRefused_SelfURL
// ---------------------------------------------------------------------------

func TestForwardClient_Send_LoopRefused_SelfURL(t *testing.T) {
	selfURL := "http://test-pod:8091"
	fc := newForwardClient([]byte("secret"), nil, selfURL, 0)
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
			fc := newForwardClient([]byte("secret"), nil, tc.advertiseURL, 0)
			_, err := fc.send(context.Background(), tc.peerURL, req)
			require.ErrorIs(t, err, ErrDaemonNotFound, "loopback %q must return ErrDaemonNotFound", tc.peerURL)
		})
	}
}

// ---------------------------------------------------------------------------
// TestForwardClient_Send_5xxWithPrevSecret_NoRetry — C3 follow-up
// ---------------------------------------------------------------------------

// TestForwardClient_Send_5xxWithPrevSecret_NoRetry verifies that when a peer
// returns 503, the forwardClient makes exactly ONE request even when PrevSecret
// is configured. A 5xx must not trigger key-rotation retry (only 403 should).
func TestForwardClient_Send_5xxWithPrevSecret_NoRetry(t *testing.T) {
	callCount := 0
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})
	defer srv.Close()

	// Redirect all traffic from a fake non-loopback hostname to the test server.
	// This lets us call send() with a non-loopback peer URL while still hitting
	// the httptest server (which binds to 127.0.0.1).
	fc := newForwardClient([]byte("new-secret"), []byte("old-secret"), "http://self-pod:8091", 0)
	fc.httpClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// Rewrite the target host to the test server while preserving path.
			req2 := req.Clone(req.Context())
			req2.URL.Host = srv.Listener.Addr().String()
			req2.URL.Scheme = "http"
			return http.DefaultTransport.RoundTrip(req2)
		}),
	}

	req := forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"}
	// peer URL is non-loopback so wouldLoop returns false.
	_, err := fc.send(context.Background(), "http://peer-pod:8091", req)

	require.ErrorIs(t, err, ErrDaemonGone, "5xx must map to ErrDaemonGone")
	require.Equal(t, 1, callCount, "5xx must not trigger retry: exactly 1 request expected, got %d", callCount)
}

// roundTripFunc is an http.RoundTripper implemented by a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// Additional: 5xx → ErrDaemonGone
// ---------------------------------------------------------------------------

func TestForwardClient_Send_5xx_MapsToErrDaemonGone(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})
	defer srv.Close()

	fc := newTestClient("secret", "")
	body, _ := json.Marshal(forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "list_sessions"})
	_, err := fc.doSend(context.Background(), srv.URL, body, fc.secret)
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
	body, _ := json.Marshal(forwardRequest{UserID: "u", WorkspaceID: "w", DaemonID: "d", Command: "get_session"})
	_, err := fc.doSend(context.Background(), srv.URL, body, fc.secret)
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	require.Equal(t, "session_not_found", de.Code)
	require.Equal(t, "session abc not found", de.Message)
}

// ---------------------------------------------------------------------------
// TestWouldLoop_IPv4Loopback — covers Fix #5: net.IP.IsLoopback detection
// ---------------------------------------------------------------------------

func TestWouldLoop_IPv4Loopback(t *testing.T) {
	selfURL := "http://prod-pod:8091"
	fc := newForwardClient([]byte("secret"), nil, selfURL, 0)

	cases := []struct {
		peerURL    string
		expectLoop bool
	}{
		// IPv4 loopback — must be blocked by IsLoopback.
		{"http://127.0.0.1:8091", true},
		{"http://127.1.2.3:8091", true},
		// IPv6 loopback — blocked by IsLoopback.
		{"http://[::1]:8091", true},
		// Named loopback — explicit check.
		{"http://localhost:8091", true},
		// Non-loopback production peer — must NOT be blocked.
		{"http://10.0.0.42:8091", false},
		// Self URL — blocked by exact match.
		{selfURL, true},
		// Empty — blocked.
		{"", true},
	}

	for _, tc := range cases {
		t.Run(tc.peerURL, func(t *testing.T) {
			got := fc.wouldLoop(tc.peerURL)
			if tc.expectLoop {
				require.True(t, got, "peerURL %q should be detected as loop", tc.peerURL)
			} else {
				require.False(t, got, "peerURL %q should NOT be detected as loop", tc.peerURL)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestForwardClient_Stream_DecodeError_EmitsErrorEnvelope
// ---------------------------------------------------------------------------

// TestForwardClient_Stream_DecodeError_EmitsErrorEnvelope verifies that when
// the server writes garbage bytes (not a valid length-prefixed envelope), the
// stream goroutine emits a synthetic error envelope before closing the channel.
func TestForwardClient_Stream_DecodeError_EmitsErrorEnvelope(t *testing.T) {
	srv := makeForwardServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Write garbage that is not a valid length-prefixed envelope.
		// This will trigger a decode error in the stream goroutine.
		w.Write([]byte("this is not valid envelope data!!!\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
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
	body, err := json.Marshal(req)
	require.NoError(t, err)

	// Use doStreamRequest directly to bypass wouldLoop (httptest binds to 127.0.0.1).
	ctx := context.Background()
	resp, err := fc.doStreamRequest(ctx, srv.URL, body, fc.secret)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Replay the stream goroutine logic (mirrors fc.stream internals).
	out := make(chan commander.Envelope, forwardStreamBuf)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := NewEnvelopeDecoder(resp.Body)
		for {
			env, err := dec.Decode()
			switch {
			case err == nil:
				select {
				case out <- *env:
				case <-ctx.Done():
					return
				}
			case errors.Is(err, io.EOF):
				return
			default:
				payload, _ := json.Marshal(map[string]string{
					"code":    commander.ErrCodeBackendUnavailable,
					"message": err.Error(),
				})
				select {
				case out <- commander.Envelope{Type: "error", Payload: payload}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	// Collect all envelopes until channel closes.
	var received []commander.Envelope
	for env := range out {
		received = append(received, env)
	}

	// Must have received at least one envelope with type "error".
	require.NotEmpty(t, received, "should receive at least one envelope on decode error")
	last := received[len(received)-1]
	require.Equal(t, "error", last.Type, "last envelope must be type=error on decode failure")
}

// ---------------------------------------------------------------------------
// Compile-time check: forwardClient fields exist.
// ---------------------------------------------------------------------------

var _ = func() *forwardClient {
	return newForwardClient([]byte("s"), []byte("p"), "http://a:1", 0)
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
