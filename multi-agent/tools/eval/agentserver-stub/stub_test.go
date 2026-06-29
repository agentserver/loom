package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// helper: spin up the stub on an httptest server and return the URL + cleanup.
func newTestStub(t *testing.T) (string, func()) {
	t.Helper()
	srv := NewServer("ws-eval-auto")
	ts := httptest.NewServer(srv.Handler())
	return ts.URL, ts.Close
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func decodeCreds(t *testing.T, resp *http.Response) Credentials {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var c Credentials
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return c
}

func TestIssue_ReturnsFiveTupleCredentials(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	resp := postJSON(t, url+"/api/v1/agents/register", map[string]string{
		"role":     "driver",
		"short_id": "drv-001",
	})
	c := decodeCreds(t, resp)

	if c.SandboxID == "" {
		t.Error("sandbox_id is empty")
	}
	if c.TunnelToken == "" {
		t.Error("tunnel_token is empty")
	}
	if c.ProxyToken == "" {
		t.Error("proxy_token is empty")
	}
	if c.WorkspaceID == "" {
		t.Error("workspace_id is empty")
	}
	if c.ShortID != "drv-001" {
		t.Errorf("short_id = %q, want drv-001", c.ShortID)
	}
}

func TestRegister_ReturnsConsistentTokens(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	body := map[string]string{"role": "slave-a", "short_id": "slv-a-001"}
	first := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register", body))
	second := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register", body))

	if first != second {
		t.Errorf("idempotency broken:\n  first  = %+v\n  second = %+v", first, second)
	}

	// Different short_id must yield different tokens.
	other := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register",
		map[string]string{"role": "slave-a", "short_id": "slv-a-002"}))
	if other.ProxyToken == first.ProxyToken {
		t.Errorf("different short_id produced same proxy_token: %s", other.ProxyToken)
	}
	if other.SandboxID == first.SandboxID {
		t.Errorf("different short_id produced same sandbox_id: %s", other.SandboxID)
	}
}

func TestWhoami_RoundTrips(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	creds := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register",
		map[string]string{"role": "observer", "short_id": "obs-001"}))

	req, _ := http.NewRequest(http.MethodGet, url+"/api/v1/agents/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+creds.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("whoami status %d: %s", resp.StatusCode, body)
	}
	var who struct {
		UserID        string `json:"user_id"`
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceName string `json:"workspace_name"`
		SandboxID     string `json:"sandbox_id"`
		ShortID       string `json:"short_id"`
		Role          string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if who.ShortID != creds.ShortID {
		t.Errorf("short_id = %q, want %q", who.ShortID, creds.ShortID)
	}
	if who.WorkspaceID != creds.WorkspaceID {
		t.Errorf("workspace_id = %q, want %q", who.WorkspaceID, creds.WorkspaceID)
	}
	if who.Role != "observer" {
		t.Errorf("role = %q, want observer", who.Role)
	}
	if who.UserID == "" {
		t.Error("user_id empty — resolver requires it")
	}
	if who.SandboxID != creds.SandboxID {
		t.Errorf("sandbox_id = %q, want %q", who.SandboxID, creds.SandboxID)
	}

	// Unknown token → 401.
	req2, _ := http.NewRequest(http.MethodGet, url+"/api/v1/agents/whoami", nil)
	req2.Header.Set("Authorization", "Bearer not-a-real-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("whoami(unknown): %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown bearer status = %d, want 401", resp2.StatusCode)
	}
}

func TestHeartbeat_AlwaysAccepts(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	creds := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register",
		map[string]string{"role": "slave-b", "short_id": "slv-b-001"}))

	req, _ := http.NewRequest(http.MethodPost, url+"/api/v1/agents/heartbeat",
		strings.NewReader(`{"anything": "goes", "ts": 1234567}`))
	req.Header.Set("Authorization", "Bearer "+creds.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("heartbeat status = %d, want 204", resp.StatusCode)
	}

	// Bogus payload still 204 for a known token.
	req2, _ := http.NewRequest(http.MethodPost, url+"/api/v1/agents/heartbeat",
		strings.NewReader("not-even-json"))
	req2.Header.Set("Authorization", "Bearer "+creds.ProxyToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("heartbeat(garbage): %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("garbage-body status = %d, want 204", resp2.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	resp, err := http.Get(url + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK {
		t.Error(`expected {"ok": true}`)
	}
}

func TestLegacyAndV1PathsAreIdentical(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	creds := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register",
		map[string]string{"role": "driver", "short_id": "drv-alias"}))

	get := func(path string) []byte {
		req, _ := http.NewRequest(http.MethodGet, url+path, nil)
		req.Header.Set("Authorization", "Bearer "+creds.ProxyToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		return b
	}

	v1 := get("/api/v1/agents/whoami")
	legacy := get("/api/agent/whoami")
	if !bytes.Equal(v1, legacy) {
		t.Errorf("alias mismatch:\n  v1     = %s\n  legacy = %s", v1, legacy)
	}
}

// TestLegacyAliasRegisterAndHeartbeat exercises the legacy /api/agent/* prefix
// for register and heartbeat (whoami is covered above). The driver/slave/
// observer binaries hardcode the legacy prefix, so a regression here would
// break them silently.
func TestLegacyAliasRegisterAndHeartbeat(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	// Register via legacy alias.
	legacyCreds := decodeCreds(t, postJSON(t, url+"/api/agent/register",
		map[string]string{"role": "slave-a", "short_id": "legacy-001"}))
	// Same body via v1 must produce identical credentials (HMAC determinism +
	// shared handler).
	v1Creds := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register",
		map[string]string{"role": "slave-a", "short_id": "legacy-001"}))
	if legacyCreds != v1Creds {
		t.Errorf("legacy/v1 register mismatch:\n  legacy = %+v\n  v1     = %+v", legacyCreds, v1Creds)
	}

	// Heartbeat via legacy alias.
	req, _ := http.NewRequest(http.MethodPost, url+"/api/agent/heartbeat",
		strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+legacyCreds.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("legacy heartbeat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("legacy heartbeat status = %d, want 204", resp.StatusCode)
	}
}

// TestErrorStatusCodes locks in the status codes the design doc promises for
// malformed input (405 wrong method, 400 bad JSON / missing fields, 401 missing
// or bad bearer). Without these, an upstream resolver could misread a server
// bug as a transient network error.
func TestErrorStatusCodes(t *testing.T) {
	url, stop := newTestStub(t)
	defer stop()

	// Issue one good token for the auth-required cases.
	good := decodeCreds(t, postJSON(t, url+"/api/v1/agents/register",
		map[string]string{"role": "driver", "short_id": "drv-err"}))

	do := func(method, path, auth, body string) int {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, url+path, r)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	cases := []struct {
		name   string
		method string
		path   string
		auth   string
		body   string
		want   int
	}{
		{"register-wrong-method", http.MethodGet, "/api/v1/agents/register", "", "", http.StatusMethodNotAllowed},
		{"whoami-wrong-method", http.MethodPost, "/api/v1/agents/whoami", "Bearer " + good.ProxyToken, "", http.StatusMethodNotAllowed},
		{"heartbeat-wrong-method", http.MethodGet, "/api/v1/agents/heartbeat", "Bearer " + good.ProxyToken, "", http.StatusMethodNotAllowed},
		{"healthz-wrong-method", http.MethodPost, "/healthz", "", "", http.StatusMethodNotAllowed},
		{"register-bad-json", http.MethodPost, "/api/v1/agents/register", "", "{not-json", http.StatusBadRequest},
		{"register-missing-role", http.MethodPost, "/api/v1/agents/register", "", `{"short_id":"x"}`, http.StatusBadRequest},
		{"register-missing-short-id", http.MethodPost, "/api/v1/agents/register", "", `{"role":"driver"}`, http.StatusBadRequest},
		{"whoami-no-bearer", http.MethodGet, "/api/v1/agents/whoami", "", "", http.StatusUnauthorized},
		{"whoami-empty-bearer", http.MethodGet, "/api/v1/agents/whoami", "Bearer ", "", http.StatusUnauthorized},
		{"whoami-non-bearer-scheme", http.MethodGet, "/api/v1/agents/whoami", "Basic dXNlcjpwYXNz", "", http.StatusUnauthorized},
		{"whoami-unknown-token", http.MethodGet, "/api/v1/agents/whoami", "Bearer made-up-token", "", http.StatusUnauthorized},
		{"heartbeat-no-bearer", http.MethodPost, "/api/v1/agents/heartbeat", "", `{}`, http.StatusUnauthorized},
		{"heartbeat-unknown-token", http.MethodPost, "/api/v1/agents/heartbeat", "Bearer made-up", `{}`, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := do(tc.method, tc.path, tc.auth, tc.body)
			if got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}
