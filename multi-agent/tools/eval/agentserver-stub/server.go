package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Credentials is the five-tuple returned by register/issue. Field order matches
// tests/prod_test/E2E_RUNBOOK.md `credentials:` block so eval-runner can paste
// it straight into driver/slave/observer YAML.
type Credentials struct {
	SandboxID   string `json:"sandbox_id"`
	TunnelToken string `json:"tunnel_token"`
	ProxyToken  string `json:"proxy_token"`
	WorkspaceID string `json:"workspace_id"`
	ShortID     string `json:"short_id"`
}

// whoamiResponse matches the JSON shape parsed by
// internal/identity/agentserver/resolver.go. user_id, workspace_id, sandbox_id
// and role are all required there — empty values would make the observer
// startup probe fail.
type whoamiResponse struct {
	UserID        string `json:"user_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	SandboxID     string `json:"sandbox_id"`
	ShortID       string `json:"short_id"`
	Role          string `json:"role"`
}

// registerRequest is what /api/v1/agents/register accepts. workspace_id is
// optional — empty means "use the server default".
type registerRequest struct {
	Role        string `json:"role"`
	ShortID     string `json:"short_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// agentCard is what handleDiscoveryCards stores and handleDiscoveryAgents returns.
// Full definition owned by discovery.go.
type agentCard struct {
	SandboxID   string          `json:"-"`
	WorkspaceID string          `json:"-"`
	ShortID     string          `json:"-"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Card        json.RawMessage `json:"card"`
}

// stubTask holds one delegated task.  Full definition owned by tasks.go.
type stubTask struct {
	ID            string          `json:"task_id"`
	WorkspaceID   string          `json:"workspace_id"`
	RequesterID   string          `json:"requester_id"`
	TargetID      string          `json:"target_id"`
	Skill         string          `json:"skill,omitempty"`
	Prompt        string          `json:"prompt"`
	SystemContext string          `json:"system_context,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	MaxTurns      int             `json:"max_turns,omitempty"`
	MaxBudgetUSD  float64         `json:"max_budget_usd,omitempty"`
	Timeout       int             `json:"timeout_seconds,omitempty"`
	Status        string          `json:"status"`
	Result        json.RawMessage `json:"result,omitempty"`
	Output        string          `json:"output,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
	TotalCostUSD  float64         `json:"total_cost_usd,omitempty"`
	NumTurns      int             `json:"num_turns,omitempty"`
	CreatedAt     time.Time       `json:"-"`
	CompletedAt   time.Time       `json:"-"`
}

// Server is the stripped-down agentserver. It is single-process, in-memory,
// and ⚠️  NOT FOR PRODUCTION — see README.md.
type Server struct {
	secret           string
	defaultWorkspace string

	mu             sync.RWMutex
	byProxy        map[string]whoamiResponse // proxy_token -> identity
	byTunnel       map[string]whoamiResponse // tunnel_token -> identity
	cards          map[string]agentCard      // sandbox_id -> agentCard (discovery.go)
	tunnels        map[string]*stubTunnel    // sandbox_id -> active tunnel (tunnel.go)
	tasks          map[string]*stubTask      // task_id -> task (tasks.go)
	tasksBySandbox map[string][]string       // target sandbox_id -> pending task_ids
}

// NewServer creates a fresh stub server with a freshly generated HMAC secret.
// workspaceDefault is the workspace_id assigned when register/issue does not
// supply one; pass "" or "auto" to get the canonical "ws-eval-auto".
func NewServer(workspaceDefault string) *Server {
	if workspaceDefault == "" || workspaceDefault == "auto" {
		workspaceDefault = "ws-eval-auto"
	}
	return &Server{
		secret:           NewSecret(),
		defaultWorkspace: workspaceDefault,
		byProxy:          map[string]whoamiResponse{},
		byTunnel:         map[string]whoamiResponse{},
		cards:            map[string]agentCard{},
		tunnels:          map[string]*stubTunnel{},
		tasks:            map[string]*stubTask{},
		tasksBySandbox:   map[string][]string{},
	}
}

// Close shuts down every open tunnel; safe to call multiple times but Server
// is single-use after Close. Tests defer Close().
func (s *Server) Close() error {
	s.mu.Lock()
	tunnels := make([]*stubTunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		tunnels = append(tunnels, t)
	}
	s.mu.Unlock()
	for _, t := range tunnels {
		t.Close()
	}
	return nil
}

// Handler returns the HTTP handler mounting every endpoint on both the spec
// path (/api/v1/agents/...) and the legacy alias (/api/agent/...). Both paths
// are served by identical handlers; see design doc 2026-06-29.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)

	for _, prefix := range []string{"/api/v1/agents", "/api/agent"} {
		mux.HandleFunc(prefix+"/register", s.handleRegister)
		mux.HandleFunc(prefix+"/whoami", s.handleWhoami)
		mux.HandleFunc(prefix+"/heartbeat", s.handleHeartbeat)
	}

	// Discovery (spec §4.1 §4.2)
	mux.HandleFunc("/api/agent/discovery/cards", s.handleDiscoveryCards)
	mux.HandleFunc("/api/agent/discovery/agents", s.handleDiscoveryAgents)

	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Role == "" || req.ShortID == "" {
		http.Error(w, "role and short_id are required", http.StatusBadRequest)
		return
	}
	// Mirror the NewServer normalization: an empty workspace_id, or the
	// sentinel "auto", both mean "use the server default". Without this branch
	// a caller running `agentserver-stub issue --workspace-id auto` would slot
	// the literal "auto" string into credentials, lookup, and whoami.
	if req.WorkspaceID == "" || req.WorkspaceID == "auto" {
		req.WorkspaceID = s.defaultWorkspace
	}

	creds, ident := s.issue(req.Role, req.ShortID, req.WorkspaceID)

	s.mu.Lock()
	s.byProxy[creds.ProxyToken] = ident
	s.byTunnel[creds.TunnelToken] = ident
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(creds)
}

// issue derives the five-tuple and matching identity for (role, short_id,
// workspace_id). Pure function — same inputs ⇒ identical outputs.
func (s *Server) issue(role, shortID, workspaceID string) (Credentials, whoamiResponse) {
	creds := Credentials{
		SandboxID:   "sbx-" + deriveToken(s.secret, "sandbox", role, shortID, workspaceID),
		TunnelToken: "ttok-" + deriveToken(s.secret, "tunnel", role, shortID, workspaceID),
		ProxyToken:  "ptok-" + deriveToken(s.secret, "proxy", role, shortID, workspaceID),
		WorkspaceID: workspaceID,
		ShortID:     shortID,
	}
	ident := whoamiResponse{
		UserID:        "eval-user",
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceID,
		SandboxID:     creds.SandboxID,
		ShortID:       shortID,
		Role:          role,
	}
	return creds, ident
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	ident, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ident)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	_, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Drain and discard. No state mutation — see design "Limitations".
	_, _ = io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusNoContent)
}

func bearerToken(authHeader string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	return token, token != ""
}
