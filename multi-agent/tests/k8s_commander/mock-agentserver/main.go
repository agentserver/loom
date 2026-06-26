// Mock agentserver for commander state-persistence e2e.
//
// Implements the subset of agentserver endpoints observer-server +
// commanderhub.Authenticator touch during a login flow:
//
//   - POST /api/oauth2/device/auth      → returns a DeviceAuthResponse
//   - POST /api/oauth2/token            → first call: authorization_pending; subsequent: token
//   - GET  /api/agent/whoami            → 401 for unknown bearer; 200 for "fixture-proxy"
//
// The OAuth id_token returned is an unsigned JWT (alg=none) carrying
// {sub, workspace_id, workspace_role, exp} — enough for
// identityFromIDToken to parse. This matches what the real device flow
// would return.
//
// Behavior is deliberately deterministic so the e2e test can assert on
// specific identity values: user_id="alice-mock", workspace_id="W-mock".
//
// Auto-approves devices after a configurable delay (default 0s) so the
// flow completes without manual intervention.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	mockUserID        = "alice-mock"
	mockWorkspaceID   = "W-mock"
	mockWorkspaceRole = "member"
	mockSandboxID     = "sb-mock"
	mockUserCode      = "MOCK-CODE"
	deviceCodeTTL     = 5 * time.Minute
	idTokenTTL        = 1 * time.Hour
)

func main() {
	addr := os.Getenv("MOCK_LISTEN")
	if addr == "" {
		addr = ":18080"
	}
	publicURL := os.Getenv("MOCK_PUBLIC_URL")
	if publicURL == "" {
		publicURL = "http://localhost" + addr
	}

	m := newMockServer(publicURL)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/device/auth", m.handleDeviceAuth)
	mux.HandleFunc("/api/oauth2/token", m.handleToken)
	mux.HandleFunc("/api/agent/whoami", m.handleWhoami)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	log.Printf("mock-agentserver listening on %s (public %s)", addr, publicURL)
	log.Fatal(http.ListenAndServe(addr, mux))
}

type mockServer struct {
	publicURL string

	mu      sync.Mutex
	devices map[string]*deviceState // device_code → state
}

type deviceState struct {
	createdAt time.Time
	consumed  bool // first /token call → pending; second+ → ok
}

func newMockServer(publicURL string) *mockServer {
	return &mockServer{
		publicURL: publicURL,
		devices:   map[string]*deviceState{},
	}
}

func (m *mockServer) handleDeviceAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dc := fmt.Sprintf("dc-%d", time.Now().UnixNano())
	m.mu.Lock()
	m.devices[dc] = &deviceState{createdAt: time.Now()}
	m.mu.Unlock()

	resp := map[string]any{
		"device_code":               dc,
		"user_code":                 mockUserCode,
		"verification_uri":          m.publicURL + "/verify",
		"verification_uri_complete": m.publicURL + "/verify?user_code=" + mockUserCode,
		"expires_in":                int(deviceCodeTTL / time.Second),
		"interval":                  5,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	dc := r.PostFormValue("device_code")
	if dc == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	m.mu.Lock()
	st, ok := m.devices[dc]
	if !ok {
		m.mu.Unlock()
		writeOAuthError(w, http.StatusBadRequest, "expired_token")
		return
	}
	if time.Since(st.createdAt) > deviceCodeTTL {
		delete(m.devices, dc)
		m.mu.Unlock()
		writeOAuthError(w, http.StatusBadRequest, "expired_token")
		return
	}
	first := !st.consumed
	st.consumed = true
	m.mu.Unlock()

	if first {
		// Mimic agentserver: a freshly-issued device code is "pending" on
		// its very first poll, then auto-approves on subsequent polls.
		// This guarantees the e2e test exercises both [C2 pending] and
		// [C1 ready] paths without any manual approval step.
		writeOAuthError(w, http.StatusBadRequest, "authorization_pending")
		return
	}

	// Build an unsigned JWT with the required claims.
	header := base64URLJSON(map[string]string{"alg": "none", "typ": "JWT"})
	payload := base64URLJSON(map[string]any{
		"sub":            mockUserID,
		"workspace_id":   mockWorkspaceID,
		"workspace_role": mockWorkspaceRole,
		"exp":            time.Now().Add(idTokenTTL).Unix(),
	})
	idToken := header + "." + payload + "."

	resp := map[string]any{
		"access_token": "mock-access-" + dc,
		"id_token":     idToken,
		"token_type":   "Bearer",
		"expires_in":   int(idTokenTTL / time.Second),
		"scope":        "openid profile agent:register",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockServer) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Observer's startup probe sends "observer-startup-probe-invalid-token"
	// expecting 401. Real production whoami also 401s on unknown tokens.
	if tok != "fixture-proxy" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp := map[string]any{
		"user_id":        mockUserID,
		"workspace_id":   mockWorkspaceID,
		"workspace_name": "mock-workspace",
		"sandbox_id":     mockSandboxID,
		"role":           "driver",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func base64URLJSON(v any) string {
	b, _ := json.Marshal(v)
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}
