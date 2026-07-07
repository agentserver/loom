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

// -- shared test helpers (cross-file visible in package main) --------------

func registerAgent(t *testing.T, srv *httptest.Server, role, shortID, wsID string) Credentials {
	t.Helper()
	body, _ := json.Marshal(registerRequest{Role: role, ShortID: shortID, WorkspaceID: wsID})
	resp, err := http.Post(srv.URL+"/api/v1/agents/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register: status %d: %s", resp.StatusCode, b)
	}
	var creds Credentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		t.Fatalf("decode creds: %v", err)
	}
	return creds
}

func postCard(t *testing.T, srv *httptest.Server, bearer string, payload map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", srv.URL+"/api/agent/discovery/cards", bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post card: %v", err)
	}
	return resp
}

func getAgents(t *testing.T, srv *httptest.Server, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/agent/discovery/agents", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get agents: %v", err)
	}
	return resp
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newTestServerWithStub(t)
	return srv
}

// newTestServerWithStub returns both handles for tests that need to inspect
// internal Server state (e.g. tunnel readiness polling).
func newTestServerWithStub(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	s := NewServer("")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		s.Close()
		srv.Close()
	})
	return srv, s
}

// -- discovery tests ------------------------------------------------------

func TestDiscoveryCards_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := postCard(t, srv, "", map[string]any{"display_name": "x"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestDiscoveryCards_UnknownBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := postCard(t, srv, "ptok-not-a-real-token", map[string]any{"display_name": "x"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestDiscoveryCards_BadJSON_400(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	req, _ := http.NewRequest("POST", srv.URL+"/api/agent/discovery/cards", strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer "+creds.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
}

func TestDiscoveryCards_StoresAgentCard(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	resp := postCard(t, srv, creds.ProxyToken, map[string]any{
		"display_name": "eval-slave",
		"description":  "linux slave",
		"agent_type":   "custom",
		"card":         map[string]any{"skills": []string{"chat", "bash"}, "short_id": "slv-001"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("post card: status %d body %s", resp.StatusCode, b)
	}
	got := getAgents(t, srv, creds.ProxyToken)
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get agents: status %d", got.StatusCode)
	}
	var cards []map[string]any
	if err := json.NewDecoder(got.Body).Decode(&cards); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("want 1 card got %d", len(cards))
	}
	if cards[0]["display_name"] != "eval-slave" {
		t.Fatalf("display_name: want eval-slave got %v", cards[0]["display_name"])
	}
}

func TestDiscoveryAgents_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := getAgents(t, srv, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestDiscoveryAgents_ReturnsCardsSameWorkspaceOnly(t *testing.T) {
	srv := newTestServer(t)
	credsA := registerAgent(t, srv, "slave", "slv-a", "ws-A")
	credsB := registerAgent(t, srv, "slave", "slv-b", "ws-B")
	postCard(t, srv, credsA.ProxyToken, map[string]any{"display_name": "eval-slave-a", "card": map[string]any{}}).Body.Close()
	postCard(t, srv, credsB.ProxyToken, map[string]any{"display_name": "eval-slave-b", "card": map[string]any{}}).Body.Close()
	resp := getAgents(t, srv, credsA.ProxyToken)
	defer resp.Body.Close()
	var cards []map[string]any
	json.NewDecoder(resp.Body).Decode(&cards)
	if len(cards) != 1 || cards[0]["display_name"] != "eval-slave-a" {
		t.Fatalf("workspace A leaks: %+v", cards)
	}
}

func TestDiscoveryAgents_AgentIDIsSandboxID(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, creds.ProxyToken, map[string]any{"display_name": "eval-slave", "card": map[string]any{}}).Body.Close()
	resp := getAgents(t, srv, creds.ProxyToken)
	defer resp.Body.Close()
	var cards []map[string]any
	json.NewDecoder(resp.Body).Decode(&cards)
	if len(cards) != 1 {
		t.Fatalf("want 1 card got %d", len(cards))
	}
	if cards[0]["agent_id"] != creds.SandboxID {
		t.Fatalf("agent_id: want %q (sandbox_id) got %v", creds.SandboxID, cards[0]["agent_id"])
	}
}

func TestDiscoveryAgents_StatusAlwaysAvailable(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, creds.ProxyToken, map[string]any{"display_name": "eval-slave", "card": map[string]any{}}).Body.Close()
	resp := getAgents(t, srv, creds.ProxyToken)
	defer resp.Body.Close()
	var cards []map[string]any
	json.NewDecoder(resp.Body).Decode(&cards)
	if cards[0]["status"] != "available" {
		t.Fatalf("status: want 'available' got %v", cards[0]["status"])
	}
}
