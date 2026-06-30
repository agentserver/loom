package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

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

// TestDrainHandler_NonLoopbackRequiresAuth tests that non-loopback requires auth.
func TestDrainHandler_NonLoopbackRequiresAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/commander/_internal/drain", nil)
	req.RemoteAddr = "10.0.0.5:12345"

	w := httptest.NewRecorder()
	h := NewHub(&fakeResolver{mu: map[string]identity.Identity{}})

	// Should fail because non-loopback and no HMAC.
	h.drainHandler(w, req)
	require.Equal(t, http.StatusForbidden, w.Code, "non-loopback without HMAC should return 403")
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
