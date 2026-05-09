# Generic Driver Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `cmd/driver-agent` that runs as a stdio MCP server inside Claude Code while simultaneously holding an agentserver tunnel as a regular agent, so the user's natural-language requests in Claude Code can fan out across the multi-agent workspace and reference local files/directories that master and slaves fetch on demand through a new agentserver peer-proxy route.

**Architecture:** One Go process speaks two protocols — stdio MCP up to Claude Code, agentserver tunnel down to the workspace. A per-process `FileRegistry` mints handle URLs that point at the driver's own webui; agentserver's new `/api/agent/peer/{short_id}/proxy/...` route gives master and slaves an authenticated path to those URLs. Output paths are first-class: slaves PUT result bytes back, the driver atomically writes to the local disk. The orchestrator/planner/executor packages are not touched; only the planner prompt gains one paragraph.

**Tech Stack:** Go 1.22+, agentserver SDK (`github.com/agentserver/agentserver/pkg/agentsdk`), agentserver tunnel (`internal/tunnel`), `net/http` + `chi/v5` for routing, stdio JSON-RPC for MCP, yaml.v3 for config, sqlite (existing `internal/store`), `httptest` for unit tests.

**Reference spec:** [`docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md`](../specs/2026-05-09-generic-driver-agent-design.md). Read § 1a, § 1, § 2, § 3 before starting Tasks 1–10.

**Repos involved:** Two checkouts side-by-side under `multi-agent/` (this repo) and `agentserver/` (the symlinked sibling at the worktree root). Tasks 1–3 modify `agentserver/`; Tasks 4+ modify `multi-agent/`.

**Two intentional deviations from the spec, baked into this plan:**

1. The spec's § 3 says the driver's `/files/*` middleware checks for an "HTTPStreamMeta header that the middleware checks for; absent → 401". No such middleware exists today (`multi-agent/internal/webui/server.go` only checks `Authorization` against the local proxy_token). Plan: peer-proxy handler injects `X-Agentserver-Peer-Short-ID: <requester_short_id>` before forwarding; the driver's `/files/*` middleware requires that header non-empty (only agentserver's tunneled forwarder can set it). This header doubles as the `peer_short_id` audit-log field that § 5 requires.
2. The spec's deliverables checklist mentions a `?since_seq=` parameter on `/api/sub_tasks`. That endpoint does not exist; master's webui exposes `/tasks/{id}/children` (`multi-agent/internal/webui/server.go:130`) which returns a snapshot with no monotonic seq. Adding a seq column is non-trivial and out of scope. Plan: `tail_subtasks` PeerProxies `/tasks/{master_task_id}/children` and computes diffs client-side from successive snapshots — the spec already permits this fallback in § 1.

---

## Phase A — agentserver peer-proxy (Tasks 1–3)

These tasks land in the sibling `agentserver/` checkout. They are independent of every later task and can be merged on their own — but the e2e harness (Task 17) requires them to be deployed.

### Task 1: SDK `Client.PeerProxy` helper

**Files:**
- Create: `agentserver/pkg/agentsdk/peer.go`
- Create: `agentserver/pkg/agentsdk/peer_test.go`

**Context:** All other agentsdk methods live next to each other in `agentserver/pkg/agentsdk/agents.go`; `agentAPI` (line 244) is the existing helper that fixes the JSON content-type and consumes the response body. `PeerProxy` is different: it forwards raw bodies (potentially gigabytes) and returns the raw `*http.Response` so callers can stream. So it does NOT call `agentAPI`; it builds its own request.

- [ ] **Step 1: Write the failing test**

```go
// agentserver/pkg/agentsdk/peer_test.go
package agentsdk

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPeerProxy_BuildsCorrectURLAndAuth(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("RESP"))
	}))
	defer srv.Close()

	c := NewClient(Config{ServerURL: srv.URL})
	c.SetRegistration(&Registration{ProxyToken: "tok-123"})

	resp, err := c.PeerProxy(context.Background(), "PUT",
		"slave-001", "/files/put/abc?x=1", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("PeerProxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "RESP" {
		t.Errorf("body: %q", body)
	}
	if gotMethod != "PUT" {
		t.Errorf("method: %s", gotMethod)
	}
	if gotPath != "/api/agent/peer/slave-001/proxy/files/put/abc" {
		t.Errorf("path: %s", gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("auth: %s", gotAuth)
	}
	if !bytes.Equal(gotBody, []byte("hello")) {
		t.Errorf("body forwarded: %q", gotBody)
	}
}

func TestPeerProxy_NotRegistered(t *testing.T) {
	c := NewClient(Config{ServerURL: "http://x"})
	_, err := c.PeerProxy(context.Background(), "GET", "s", "/p", nil)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agentserver && go test ./pkg/agentsdk/ -run TestPeerProxy -v`
Expected: FAIL with `undefined: (*Client).PeerProxy`.

- [ ] **Step 3: Implement `PeerProxy`**

```go
// agentserver/pkg/agentsdk/peer.go
package agentsdk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PeerProxy makes an HTTP request to another agent in the same workspace via
// the agentserver peer-proxy route. The path is the path on the target's
// webui (e.g., "/files/blob/abc..."), without the
// /api/agent/peer/{target_short_id}/proxy prefix. Authorization is added
// automatically using this client's proxy token. Returns the raw HTTP
// response; the caller is responsible for closing the body.
func (c *Client) PeerProxy(ctx context.Context, method, targetShortID, path string, body io.Reader) (*http.Response, error) {
	if c.reg == nil {
		return nil, fmt.Errorf("not registered: call Register() or SetRegistration() first")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := strings.TrimRight(c.config.ServerURL, "/") +
		"/api/agent/peer/" + targetShortID + "/proxy" + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build peer-proxy request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.reg.ProxyToken)
	return http.DefaultClient.Do(req)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agentserver && go test ./pkg/agentsdk/ -run TestPeerProxy -v`
Expected: PASS (both subtests).

- [ ] **Step 5: Commit**

```bash
cd agentserver
git add pkg/agentsdk/peer.go pkg/agentsdk/peer_test.go
git commit -m "feat(agentsdk): Client.PeerProxy helper for inter-agent HTTP via peer-proxy route"
```

---

### Task 2: peer-proxy HTTP handler

**Files:**
- Create: `agentserver/internal/server/agent_peer_proxy.go`
- Create: `agentserver/internal/server/agent_peer_proxy_test.go`

**Context:** Mirrors `agentserver/internal/sandboxproxy/tunnel.go::proxyViaTunnel` (the file we read earlier) but inside the `server` package so it can use `s.extractProxyTokenSandbox`, `s.Sandboxes.GetByShortID`, and `s.TunnelRegistry.Get`. Adds two things `proxyViaTunnel` does not: a workspace check, and the `X-Agentserver-Peer-Short-ID` header injected for the target. Also strips `Authorization` from the forwarded headers (the requester's proxy_token is consumed at the proxy boundary; the target's webui must not see it).

- [ ] **Step 1: Write the failing test**

```go
// agentserver/internal/server/agent_peer_proxy_test.go
package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The handler is exercised end-to-end via Server.Router(); we cover four cases:
// happy GET, no auth, cross-workspace, offline target. Wiring uses the existing
// test helper that builds a minimal Server with in-memory deps; if the helper
// does not yet exist for this package, it is added here.
//
// We rely on the existing fakeTunnel helper pattern in proxyViaTunnel tests;
// for a green-field test, we use httptest with a manually constructed Router.

func TestHandleAgentPeerProxy_Unauthorized(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/agent/peer/short-target/proxy/healthz", nil)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestHandleAgentPeerProxy_CrossWorkspace(t *testing.T) {
	s := newTestServer(t)
	requester := s.testInsertSandbox(t, "wsA", "short-A", "tok-A")
	_ = s.testInsertSandbox(t, "wsB", "short-B", "tok-B") // target lives in B
	r := httptest.NewRequest("GET", "/api/agent/peer/short-B/proxy/healthz", nil)
	r.Header.Set("Authorization", "Bearer "+requester.ProxyToken)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestHandleAgentPeerProxy_TargetOffline(t *testing.T) {
	s := newTestServer(t)
	requester := s.testInsertSandbox(t, "ws1", "short-A", "tok-A")
	_ = s.testInsertSandbox(t, "ws1", "short-B", "tok-B") // target, no tunnel registered
	r := httptest.NewRequest("GET", "/api/agent/peer/short-B/proxy/healthz", nil)
	r.Header.Set("Authorization", "Bearer "+requester.ProxyToken)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
}

func TestHandleAgentPeerProxy_HappyForwards(t *testing.T) {
	s := newTestServer(t)
	requester := s.testInsertSandbox(t, "ws1", "short-A", "tok-A")
	target := s.testInsertSandbox(t, "ws1", "short-B", "tok-B")

	// Register a fake tunnel for target that captures the forwarded request.
	var gotPath, gotMethod, gotPeerHdr, gotAuthHdr string
	var gotBody []byte
	s.testRegisterFakeTunnel(t, target.ID, func(meta map[string]string, body []byte) (status int, respHeaders map[string]string, respBody []byte) {
		gotPath = meta["__path__"]
		gotMethod = meta["__method__"]
		gotPeerHdr = meta["X-Agentserver-Peer-Short-Id"]
		gotAuthHdr = meta["Authorization"]
		gotBody = body
		return 200, map[string]string{"Content-Type": "text/plain"}, []byte("OK")
	})

	r := httptest.NewRequest("PUT", "/api/agent/peer/short-B/proxy/files/put/abc?x=1", strings.NewReader("payload"))
	r.Header.Set("Authorization", "Bearer "+requester.ProxyToken)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "OK" {
		t.Errorf("body: %q", got)
	}
	if gotMethod != "PUT" || gotPath != "/files/put/abc?x=1" {
		t.Errorf("forwarded method=%s path=%s", gotMethod, gotPath)
	}
	if gotPeerHdr != "short-A" {
		t.Errorf("X-Agentserver-Peer-Short-Id: %q", gotPeerHdr)
	}
	if gotAuthHdr != "" {
		t.Errorf("Authorization should be stripped, got %q", gotAuthHdr)
	}
	if string(gotBody) != "payload" {
		t.Errorf("body: %q", gotBody)
	}
}
```

- [ ] **Step 2: Add the test helpers (separate file so the test file stays focused)**

`newTestServer`, `testInsertSandbox`, and `testRegisterFakeTunnel` are not yet defined for this package. Add them in `agent_peer_proxy_testhelper_test.go`. Keep them minimal — they exist only to wire `Server.Router()` against in-memory fakes.

```go
// agentserver/internal/server/agent_peer_proxy_testhelper_test.go
package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	"github.com/agentserver/agentserver/internal/db"
	"github.com/agentserver/agentserver/internal/sbxstore"
	"github.com/agentserver/agentserver/internal/tunnel"
)

// newTestServer returns a *Server wired against an in-memory sqlite DB and a
// real *tunnel.Registry but with no auth, no oidc, no static FS. Just enough
// to exercise the peer-proxy handler.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	d, err := db.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	store := sbxstore.New(d)
	reg := tunnel.NewRegistry()
	s := &Server{
		DB:               d,
		Sandboxes:        store,
		TunnelRegistry:   reg,
	}
	return &testServer{Server: s, reg: reg}
}

type testServer struct {
	*Server
	reg *tunnel.Registry
}

// testInsertSandbox inserts a sandbox row directly so PeerProxy auth lookups succeed.
func (ts *testServer) testInsertSandbox(t *testing.T, workspaceID, shortID, proxyToken string) *db.Sandbox {
	t.Helper()
	sbxID := "sbx-" + shortID
	if err := ts.DB.InsertSandboxForTest(&db.Sandbox{
		ID:          sbxID,
		WorkspaceID: workspaceID,
		ShortID:     toNullString(shortID),
		ProxyToken:  proxyToken,
	}); err != nil {
		t.Fatalf("insert sandbox: %v", err)
	}
	// Mirror into in-memory store so Resolve() succeeds.
	if _, err := ts.Sandboxes.LoadFromDB(sbxID); err != nil {
		t.Fatalf("load sandbox: %v", err)
	}
	got, err := ts.DB.GetSandbox(sbxID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	return got
}

// testRegisterFakeTunnel wires a fake yamux-shaped responder for a sandbox.
// The handler is invoked once per OpenHTTPStream; its meta map merges
// HTTPStreamMeta.Headers with synthetic "__method__" / "__path__" entries.
func (ts *testServer) testRegisterFakeTunnel(t *testing.T, sandboxID string,
	handler func(meta map[string]string, body []byte) (status int, headers map[string]string, body []byte)) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { clientSide.Close(); serverSide.Close() })

	tn := ts.reg.RegisterRaw(context.Background(), sandboxID, clientSide)
	t.Cleanup(func() { tn.Close() })

	// Background goroutine: read meta+body from serverSide, call handler,
	// write status+headers+body back. Mirrors the agentsdk handlers.go shape.
	go fakeTunnelLoop(t, serverSide, handler)
}

func fakeTunnelLoop(t *testing.T, conn net.Conn,
	handler func(meta map[string]string, body []byte) (int, map[string]string, []byte)) {
	for {
		_, metaBytes, err := tunnel.ReadStreamHeader(conn)
		if err != nil {
			return
		}
		var meta tunnel.HTTPStreamMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return
		}
		body := make([]byte, meta.BodyLen)
		if meta.BodyLen > 0 {
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
		}
		merged := map[string]string{}
		for k, v := range meta.Headers {
			merged[k] = v
		}
		merged["__method__"] = meta.Method
		merged["__path__"] = meta.Path
		status, hdrs, respBody := handler(merged, body)
		respMeta := tunnel.HTTPResponseMeta{Status: status, Headers: hdrs}
		raw, _ := json.Marshal(respMeta)
		if err := tunnel.WriteStreamHeader(conn, tunnel.StreamTypeHTTP, raw); err != nil {
			return
		}
		conn.Write(respBody)
	}
}

func toNullString(s string) (n any) {
	type ns interface{ Set(string) }
	// db.Sandbox.ShortID is sql.NullString; encode here without importing sql twice.
	type wrap struct{ String string; Valid bool }
	return wrap{String: s, Valid: true}
}
```

> **Note on db helpers used above:** `db.OpenInMemory`, `db.InsertSandboxForTest`, and `sbxstore.Store.LoadFromDB` may not exist yet. If they don't, write a four-line addition for each — not a refactor — keeping signatures matching the names used here. If `tunnel.Registry.RegisterRaw(ctx, sandboxID, net.Conn) *Tunnel` does not exist, add it as a thin wrapper around the existing `Register` that takes a `net.Conn` instead of `*websocket.Conn` (yamux works on any net.Conn). Each missing helper is its own commit before Step 3.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd agentserver && go test ./internal/server/ -run TestHandleAgentPeerProxy -v`
Expected: FAIL — handler not registered, route returns 404.

- [ ] **Step 4: Implement the handler**

```go
// agentserver/internal/server/agent_peer_proxy.go
package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/agentserver/agentserver/internal/tunnel"
)

// handleAgentPeerProxy proxies an HTTP request from one agent to another agent
// in the same workspace via the target's agentserver tunnel.
//
// Auth: Bearer proxy_token of the requester (extractProxyTokenSandbox).
// Authorization is stripped from the forwarded headers; the target receives
// X-Agentserver-Peer-Short-ID = requester's short_id instead, both as proof
// the request came over the tunnel and as identification for audit logs.
//
// Route: ANY /api/agent/peer/{short_id}/proxy/*
func (s *Server) handleAgentPeerProxy(w http.ResponseWriter, r *http.Request) {
	requester := s.extractProxyTokenSandbox(w, r)
	if requester == nil {
		return // 401 already written
	}
	targetShort := chi.URLParam(r, "short_id")
	if targetShort == "" {
		http.Error(w, "missing target short_id", http.StatusBadRequest)
		return
	}
	target, ok := s.Sandboxes.GetByShortID(targetShort)
	if !ok {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.WorkspaceID != requester.WorkspaceID {
		http.Error(w, "cross-workspace peer access denied", http.StatusForbidden)
		return
	}
	t, ok := s.TunnelRegistry.Get(target.ID)
	if !ok {
		http.Error(w, "target offline", http.StatusBadGateway)
		return
	}

	// Read the request body (bounded by the 120s context below).
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
	}

	// Build forwarded headers: copy everything, drop Authorization, inject peer header.
	headers := make(map[string]string, len(r.Header))
	for k, vals := range r.Header {
		if len(vals) == 0 {
			continue
		}
		if http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		headers[k] = vals[0]
	}
	requesterShort := ""
	if requester.ShortID.Valid {
		requesterShort = requester.ShortID.String
	}
	headers["X-Agentserver-Peer-Short-Id"] = requesterShort

	// Strip the /api/agent/peer/{short_id}/proxy prefix; the target's webui
	// receives only the rest. RequestURI preserves the query string.
	rest := chi.URLParam(r, "*")
	forwardPath := "/" + rest
	if r.URL.RawQuery != "" {
		forwardPath += "?" + r.URL.RawQuery
	}

	meta := tunnel.HTTPStreamMeta{
		Method:  r.Method,
		Path:    forwardPath,
		Headers: headers,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	respMeta, respBody, err := t.OpenHTTPStream(ctx, meta, body)
	if err != nil {
		log.Printf("peer-proxy %s -> %s: %v", requesterShort, targetShort, err)
		http.Error(w, "tunnel proxy error", http.StatusBadGateway)
		return
	}
	defer respBody.Close()

	for k, v := range respMeta.Headers {
		w.Header().Set(k, v)
	}
	if respMeta.Status > 0 {
		w.WriteHeader(respMeta.Status)
	}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, readErr := respBody.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they fail at the next gate (route not registered yet)**

Run: `cd agentserver && go test ./internal/server/ -run TestHandleAgentPeerProxy -v`
Expected: still FAIL — handler exists but route not wired (`TestHandleAgentPeerProxy_Unauthorized` will return 404 instead of 401). The route registration is Task 3.

- [ ] **Step 6: Commit handler-only**

```bash
cd agentserver
git add internal/server/agent_peer_proxy.go internal/server/agent_peer_proxy_test.go internal/server/agent_peer_proxy_testhelper_test.go
git commit -m "feat(agentserver): handleAgentPeerProxy + tests (route registration follows)"
```

---

### Task 3: Register the peer-proxy route

**Files:**
- Modify: `agentserver/internal/server/server.go` (add one line in `Router()` next to the existing `/api/agent/*` proxy-token routes around lines 237–239)

- [ ] **Step 1: Locate the registration site**

Open `agentserver/internal/server/server.go`. Find the block:

```go
r.Get("/api/agent/discovery/agents", s.handleAgentDiscoverAgents)
r.Post("/api/agent/tasks", s.handleAgentCreateTask)
r.Get("/api/agent/tasks/{id}", s.handleAgentGetTask)
```

(Around line 237–239 in the current source. If line numbers have drifted, search for `handleAgentDiscoverAgents`.)

- [ ] **Step 2: Add the route registration directly under it**

Insert exactly:

```go
// Peer proxy: any agent can reach another agent in the same workspace via
// /api/agent/peer/{short_id}/proxy/<target webui path>. See
// agent_peer_proxy.go for auth and forwarding rules.
r.HandleFunc("/api/agent/peer/{short_id}/proxy/*", s.handleAgentPeerProxy)
```

> Note: chi's `HandleFunc` registers all methods on a single path; this matches the spec's "ANY" requirement. If chi v5 in this repo lacks a method-agnostic shortcut, use `r.Method("*", "/api/agent/peer/{short_id}/proxy/*", s.handleAgentPeerProxy)`. Do not register per-method (`r.Get`, `r.Post`, etc.) one-by-one — PUT is required and the spec keeps the door open for DELETE/PATCH later.

- [ ] **Step 3: Run the full Task 2 test suite to verify all four cases pass**

Run: `cd agentserver && go test ./internal/server/ -run TestHandleAgentPeerProxy -v`
Expected: PASS — all four subtests green (Unauthorized, CrossWorkspace, TargetOffline, HappyForwards).

- [ ] **Step 4: Run the full agentserver test suite to confirm no regressions**

Run: `cd agentserver && go test ./...`
Expected: PASS (no other package depends on routing under `/api/agent/peer/*`).

- [ ] **Step 5: Commit**

```bash
cd agentserver
git add internal/server/server.go
git commit -m "feat(agentserver): register /api/agent/peer/{short_id}/proxy route"
```

---

### Task 3.5: expose `short_id` on the discovery response

**Files:**
- Modify: `agentserver/internal/server/agent_proxy_routes.go` (extend the `cardResponse` returned by `handleAgentDiscoverAgents`)
- Modify: `agentserver/pkg/agentsdk/agents.go` (add `ShortID string` field to `AgentCard`)
- Modify: `agentserver/internal/server/agent_proxy_routes_test.go` (add a test if not yet present, or extend an existing one)

**Context:** `tail_subtasks` (Task 12) needs master's `short_id` to call `Client.PeerProxy(ctx, "GET", masterShortID, "/tasks/.../children", nil)`. Today's `handleAgentDiscoverAgents` returns the `AgentCard` rows as `cardResponse{AgentID, DisplayName, Description, AgentType, Status, Card, Version}` — with no top-level `short_id`. The opaque `Card` JSON is not guaranteed to carry it either (image-pipeline's `agentboot.PublishCard` does not set it). The fix is to look up the sandbox's `short_id` in the same handler and surface it on the response.

- [ ] **Step 1: Extend the SDK struct**

In `agentserver/pkg/agentsdk/agents.go`, after the `Version int` field on `AgentCard`:

```go
// AgentCard describes a discovered agent in the workspace.
type AgentCard struct {
	AgentID     string          `json:"agent_id"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Status      string          `json:"status"`
	Card        json.RawMessage `json:"card"`
	Version     int             `json:"version"`
	ShortID     string          `json:"short_id,omitempty"` // NEW
}
```

- [ ] **Step 2: Populate the field on the server side**

In `agentserver/internal/server/agent_proxy_routes.go::handleAgentDiscoverAgents`, change the loop that builds `result` to also fetch the sandbox's short_id. The sandbox store already exposes `s.Sandboxes.Get(sandboxID)` returning `(*Sandbox, bool)` whose `ShortID` field is the value we want.

```go
result := make([]cardResponse, len(cards))
for i, c := range cards {
	shortID := ""
	if sbx, ok := s.Sandboxes.Get(c.SandboxID); ok {
		shortID = sbx.ShortID
	}
	result[i] = cardResponse{
		AgentID:     c.SandboxID,
		DisplayName: c.DisplayName,
		Description: c.Description,
		AgentType:   c.AgentType,
		Status:      c.AgentStatus,
		Card:        c.CardJSON,
		Version:     c.Version,
		ShortID:     shortID, // NEW
	}
}
```

And add a `ShortID string` field to the local `cardResponse` struct in that handler:

```go
type cardResponse struct {
	AgentID     string          `json:"agent_id"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Status      string          `json:"status"`
	Card        json.RawMessage `json:"card"`
	Version     int             `json:"version"`
	ShortID     string          `json:"short_id,omitempty"` // NEW
}
```

- [ ] **Step 3: Add a test**

If `agentserver/internal/server/agent_proxy_routes_test.go` does not already cover discovery, add a minimal one using the test helpers from Task 2:

```go
func TestHandleAgentDiscoverAgents_IncludesShortID(t *testing.T) {
	s := newTestServer(t)
	requester := s.testInsertSandbox(t, "ws1", "short-A", "tok-A")
	_ = s.testInsertSandbox(t, "ws1", "short-B", "tok-B") // peer in same ws
	// Insert a card for short-B so DiscoverAgents returns it.
	if err := s.DB.UpsertAgentCard(&db.AgentCard{
		SandboxID: "sbx-short-B", WorkspaceID: "ws1",
		AgentType: "custom", DisplayName: "peer-b",
		CardJSON: json.RawMessage(`{}`), AgentStatus: "available",
	}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/agent/discovery/agents", nil)
	r.Header.Set("Authorization", "Bearer "+requester.ProxyToken)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var got []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range got {
		if c["display_name"] == "peer-b" && c["short_id"] == "short-B" {
			found = true
		}
	}
	if !found {
		t.Errorf("short_id missing on peer-b: %v", got)
	}
}
```

- [ ] **Step 4: Run the agentserver test suite**

Run: `cd agentserver && go test ./...`
Expected: PASS — new test green; no regressions in callers (`json:"short_id,omitempty"` keeps wire format additive).

- [ ] **Step 5: Commit**

```bash
cd agentserver
git add internal/server/agent_proxy_routes.go pkg/agentsdk/agents.go internal/server/agent_proxy_routes_test.go
git commit -m "feat(agentserver): include short_id in /api/agent/discovery/agents response"
```

---

**End of Phase A.** At this point `agentserver` has a deployable `Client.PeerProxy` SDK helper, a tested handler, the route is live, and `DiscoverAgents` exposes peer short IDs. The driver in Phase B+ does not depend on this binary being deployed for unit tests — only the Task 17 e2e does.

---

## Phase B — driver pure-function layer (Tasks 4–8)

These five tasks live entirely under `multi-agent/internal/driver/`. They are pure functions or in-memory state with file IO — no agentserver dependencies, no MCP yet. Each is fully testable with `go test`. Land them in order; later tasks reference earlier types.

### Task 4: driver `Config` (yaml schema + loader)

**Files:**
- Create: `multi-agent/internal/driver/config.go`
- Create: `multi-agent/internal/driver/config_test.go`

**Context:** The driver runs `agentsdk.NewClient` + `Connect` + tunnel registration just like every other agentboot-using agent (see `multi-agent/examples/image-pipeline/internal/agentboot/agentboot.go:18-39` for the canonical Config shape). But `internal/driver` is production code and must not import from `examples/`. So we duplicate the Server/Credentials/Discovery shape and add a new `DriverDefaults` block. The duplication is intentional and ~40 lines; refactoring image-pipeline to share `internal/driver` is out of scope.

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/driver/config_test.go
package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_FullRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  url: https://agent.example.com
  name: driver-yuzishu
credentials:
  sandbox_id: sbx-abc
  tunnel_token: ttok
  proxy_token: ptok
  workspace_id: ws1
  short_id: drv-001
discovery:
  display_name: driver-yuzishu
  description: "Local driver agent."
  skills: []
listen_addr: "127.0.0.1:0"
driver_defaults:
  target_display_name: master-prod
  task_timeout_sec: 900
  audit_log_dir: /tmp/audit
  disable_uid_check: true
  max_dir_cache_entries: 25000
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Server.URL != "https://agent.example.com" {
		t.Errorf("server.url: %s", c.Server.URL)
	}
	if c.Credentials.ShortID != "drv-001" {
		t.Errorf("short_id: %s", c.Credentials.ShortID)
	}
	if c.DriverDefaults.TargetDisplayName != "master-prod" {
		t.Errorf("target_display_name: %s", c.DriverDefaults.TargetDisplayName)
	}
	if c.DriverDefaults.TaskTimeoutSec != 900 {
		t.Errorf("task_timeout_sec: %d", c.DriverDefaults.TaskTimeoutSec)
	}
	if !c.DriverDefaults.DisableUIDCheck {
		t.Errorf("disable_uid_check: false")
	}
	if c.DriverDefaults.MaxDirCacheEntries != 25000 {
		t.Errorf("max_dir_cache_entries: %d", c.DriverDefaults.MaxDirCacheEntries)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
discovery: {display_name: drv}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.DriverDefaults.TaskTimeoutSec != 600 {
		t.Errorf("default task_timeout_sec: want 600, got %d", c.DriverDefaults.TaskTimeoutSec)
	}
	if c.DriverDefaults.MaxDirCacheEntries != 50000 {
		t.Errorf("default max_dir_cache_entries: want 50000, got %d", c.DriverDefaults.MaxDirCacheEntries)
	}
}

func TestLoadConfig_RejectsMissingRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte(`server: {url: ""}`), 0o600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for missing server.url")
	}
}

func TestSaveConfig_RoundTrip(t *testing.T) {
	c := &Config{}
	c.Server.URL = "https://a"
	c.Server.Name = "n"
	c.Discovery.DisplayName = "n"
	c.Credentials.ProxyToken = "secret"
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")
	if err := SaveConfig(path, c); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("perm: %o", st.Mode().Perm())
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if got.Credentials.ProxyToken != "secret" {
		t.Errorf("round-trip lost ProxyToken: %q", got.Credentials.ProxyToken)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestLoadConfig -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `config.go`**

```go
// multi-agent/internal/driver/config.go
package driver

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the driver-agent yaml shape. Server / Credentials / Discovery
// mirror the agentboot config used by image-pipeline agents (intentional
// duplication — internal packages must not import from examples/).
type Config struct {
	Server         ServerConfig    `yaml:"server"`
	Credentials    Credentials     `yaml:"credentials"`
	Discovery      Discovery       `yaml:"discovery"`
	ListenAddr     string          `yaml:"listen_addr"`     // local webui bind, e.g. "127.0.0.1:0"
	DriverDefaults DriverDefaults  `yaml:"driver_defaults"`
}

type ServerConfig struct {
	URL  string `yaml:"url"`
	Name string `yaml:"name"`
}

type Credentials struct {
	SandboxID   string `yaml:"sandbox_id"`
	TunnelToken string `yaml:"tunnel_token"`
	ProxyToken  string `yaml:"proxy_token"`
	WorkspaceID string `yaml:"workspace_id"`
	ShortID     string `yaml:"short_id"`
}

type Discovery struct {
	DisplayName string   `yaml:"display_name"`
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
}

type DriverDefaults struct {
	TargetDisplayName  string `yaml:"target_display_name"`
	TaskTimeoutSec     int    `yaml:"task_timeout_sec"`
	AuditLogDir        string `yaml:"audit_log_dir"`
	DisableUIDCheck    bool   `yaml:"disable_uid_check"`
	MaxDirCacheEntries int    `yaml:"max_dir_cache_entries"`
}

// LoadConfig reads + validates the yaml at path and applies DriverDefaults defaults.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.URL == "" {
		return nil, fmt.Errorf("config missing server.url")
	}
	if c.Server.Name == "" {
		return nil, fmt.Errorf("config missing server.name")
	}
	if c.Discovery.DisplayName == "" {
		return nil, fmt.Errorf("config missing discovery.display_name")
	}
	if c.DriverDefaults.TaskTimeoutSec == 0 {
		c.DriverDefaults.TaskTimeoutSec = 600
	}
	if c.DriverDefaults.MaxDirCacheEntries == 0 {
		c.DriverDefaults.MaxDirCacheEntries = 50000
	}
	return &c, nil
}

// SaveConfig writes c back to path with 0600 perms (it contains tokens).
func SaveConfig(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run TestLoadConfig -v && go test ./internal/driver/ -run TestSaveConfig -v`
Expected: PASS — all four subtests green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/config.go internal/driver/config_test.go
git commit -m "feat(driver): Config schema + Load/Save with driver_defaults defaults"
```

---

### Task 5: `audit.go` (JSONL append-only logger)

**Files:**
- Create: `multi-agent/internal/driver/audit.go`
- Create: `multi-agent/internal/driver/audit_test.go`

**Context:** Spec § 5. Lines are JSON objects, one per line, fsync after every write. Concurrent appends must preserve line atomicity (mutex-guarded; OS append + small line size makes the write itself atomic on POSIX, but the mutex makes the JSON marshal happen before the write call).

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/driver/audit_test.go
package driver

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAuditLog_LineFormat(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer a.Close()
	a.Log(AuditEvent{
		Event: "register_read", Path: "/home/me/x.csv",
		SHA256: "abc", Bytes: 123, TaskID: "",
	})
	a.Log(AuditEvent{
		Event: "fetch_blob", Path: "/home/me/x.csv",
		SHA256: "abc", Bytes: 123, TaskID: "t-9", PeerShortID: "slv-1",
	})

	f, err := os.Open(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []AuditEvent
	for scanner.Scan() {
		var e AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	if lines[0].Event != "register_read" || lines[0].SHA256 != "abc" || lines[0].TS == "" {
		t.Errorf("line 0: %+v", lines[0])
	}
	if lines[1].PeerShortID != "slv-1" || lines[1].TaskID != "t-9" {
		t.Errorf("line 1: %+v", lines[1])
	}
}

func TestAuditLog_ConcurrentLineAtomicity(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				a.Log(AuditEvent{Event: "register_read", Path: "/p", SHA256: "s"})
			}
		}(i)
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(dir, "audit.log"))
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		var e AuditEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("torn line: %q (%v)", line, err)
		}
	}
}

func TestAuditLog_AutoCreatesDir(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "audit.log")
	a, err := NewAuditLog(deep)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	a.Log(AuditEvent{Event: "x"})
	a.Close()
	if _, err := os.Stat(deep); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestAuditLog -v`
Expected: FAIL — `NewAuditLog`/`AuditEvent` undefined.

- [ ] **Step 3: Implement `audit.go`**

```go
// multi-agent/internal/driver/audit.go
package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEvent is one JSONL line in the driver's audit log.
// Empty fields are still written (omitempty would silently drop SHA256
// for events that record one — surprising during postmortem).
type AuditEvent struct {
	TS          string `json:"ts"`
	Event       string `json:"event"`           // register_read | register_read_dir | register_write | fetch_blob | fetch_dir | put_blob
	Path        string `json:"path"`
	SHA256      string `json:"sha256,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	PeerShortID string `json:"peer_short_id,omitempty"`
	Overwrite   bool   `json:"overwrite,omitempty"`
}

type AuditLog struct {
	mu sync.Mutex
	f  *os.File
}

// NewAuditLog opens path for append-only writes. Parent directories are created.
func NewAuditLog(path string) (*AuditLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &AuditLog{f: f}, nil
}

// Log marshals the event, appends it on its own line, and fsyncs.
// Errors are logged but never returned — audit failures must not block IO.
func (a *AuditLog) Log(ev AuditEvent) {
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit marshal: %v\n", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "audit write: %v\n", err)
		return
	}
	_ = a.f.Sync()
}

func (a *AuditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run TestAuditLog -v`
Expected: PASS — all three subtests green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/audit.go internal/driver/audit_test.go
git commit -m "feat(driver): JSONL audit log with concurrent-safe appends"
```

---

### Task 6: `safe_paths.go` (write-target safety guards)

**Files:**
- Create: `multi-agent/internal/driver/safe_paths.go`
- Create: `multi-agent/internal/driver/safe_paths_test.go`

**Context:** Spec § 5. Two guards combined into `assertWritableTarget`:
1. The path's parent directory must exist, must not be a symlink, and (unless `disable_uid_check`) must be owned by the driver-process uid.
2. The leaf must not already exist as a symlink (we never overwrite a symlink).

Symlink leaf-rejection on the read side (per § 1) lives here too as `assertNoSymlinkLeaf`.

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/driver/safe_paths_test.go
package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssertWritableTarget_NormalPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := AssertWritableTarget(target, false); err != nil {
		t.Errorf("normal path rejected: %v", err)
	}
}

func TestAssertWritableTarget_MissingParent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nope", "out.txt")
	if err := AssertWritableTarget(target, false); err == nil {
		t.Error("missing parent dir should be rejected")
	}
}

func TestAssertWritableTarget_SymlinkParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}
	target := filepath.Join(link, "out.txt")
	if err := AssertWritableTarget(target, false); err == nil {
		t.Error("symlink parent should be rejected")
	}
}

func TestAssertWritableTarget_LeafIsSymlink(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "out.txt")
	target := filepath.Join(dir, "real.txt")
	os.WriteFile(target, []byte("x"), 0o644)
	if err := os.Symlink(target, leaf); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}
	if err := AssertWritableTarget(leaf, false); err == nil {
		t.Error("symlink leaf should be rejected even when overwrite=false")
	}
}

func TestAssertWritableTarget_DisableUIDCheckSkipsUIDComparison(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	// We cannot easily construct a uid-mismatched parent in a portable test;
	// the assertion is that the disable_uid_check path always succeeds.
	if err := AssertWritableTarget(target, true); err != nil {
		t.Errorf("disable_uid_check should bypass uid check: %v", err)
	}
}

func TestAssertNoSymlinkLeaf_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	os.WriteFile(target, []byte("x"), 0o644)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}
	if err := AssertNoSymlinkLeaf(link); err == nil {
		t.Error("symlink leaf should be rejected")
	}
}

func TestAssertNoSymlinkLeaf_AllowsRegularFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "real.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	if err := AssertNoSymlinkLeaf(f); err != nil {
		t.Errorf("regular file rejected: %v", err)
	}
}

func TestAssertSafeRelPath_RejectsEscape(t *testing.T) {
	if err := AssertSafeRelPath("../../etc/passwd"); err == nil {
		t.Error("path escape should be rejected")
	}
	if err := AssertSafeRelPath("a/b/c"); err != nil {
		t.Errorf("clean relpath rejected: %v", err)
	}
	if err := AssertSafeRelPath("/abs/path"); err == nil {
		t.Error("absolute path should be rejected as relpath")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run "TestAssert" -v`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement `safe_paths.go`**

```go
// multi-agent/internal/driver/safe_paths.go
package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// AssertWritableTarget enforces v1 write-safety rules from spec §5:
//  1. The parent directory must exist and not be a symlink.
//  2. (Unless disableUIDCheck) the parent's owner uid must equal os.Getuid().
//  3. If the leaf exists, it must not be a symlink (we never overwrite a symlink).
// Returns a user-facing error suitable for surfacing through MCP.
func AssertWritableTarget(absPath string, disableUIDCheck bool) error {
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("write path must be absolute: %s", absPath)
	}
	parent := filepath.Dir(absPath)
	pinfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("parent dir not accessible: %s: %w", parent, err)
	}
	if pinfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("parent dir is a symlink: %s", parent)
	}
	if !pinfo.IsDir() {
		return fmt.Errorf("parent is not a directory: %s", parent)
	}
	if !disableUIDCheck {
		if st, ok := pinfo.Sys().(*syscall.Stat_t); ok {
			if int(st.Uid) != os.Getuid() {
				return fmt.Errorf("parent dir uid %d != driver uid %d: %s",
					st.Uid, os.Getuid(), parent)
			}
		}
	}
	if linfo, err := os.Lstat(absPath); err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("write target is a symlink (refusing to overwrite): %s", absPath)
		}
	}
	return nil
}

// AssertNoSymlinkLeaf rejects paths whose leaf is itself a symlink.
// Used by RegisterFile when the user passes a read path.
func AssertNoSymlinkLeaf(absPath string) error {
	info, err := os.Lstat(absPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", absPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinks not allowed: %s", absPath)
	}
	return nil
}

// AssertSafeRelPath rejects paths that are absolute or escape their root via "..".
// Used by /files/dir/{token}/blob to validate the ?path= query param.
func AssertSafeRelPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("empty rel path")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("rel path must not be absolute: %s", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("rel path escapes root: %s", rel)
	}
	return nil
}
```

> **Cross-platform note:** `syscall.Stat_t` is POSIX-only. The driver targets Linux (Claude Code on linux/macOS). If portability to Windows is ever needed, gate the uid block in a `_unix.go` build-tag file. For v1, leave it as-is — the e2e and CI run on Linux.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run "TestAssert" -v`
Expected: PASS — symlink subtests may `Skip` on filesystems without symlink support, others must PASS.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/safe_paths.go internal/driver/safe_paths_test.go
git commit -m "feat(driver): write-target safety guards (uid, symlink, relpath escape)"
```

---

### Task 7: `registry.go` (FileRegistry: read/dir/write tokens)

**Files:**
- Create: `multi-agent/internal/driver/registry.go`
- Create: `multi-agent/internal/driver/registry_test.go`

**Context:** Spec § 1, § 3. The registry holds three keyed tables:
- **Read files:** keyed by sha256 of contents (so re-registering the same file dedupes). Value: absolute path + size + mime + sha. SHA is computed at register time.
- **Read dirs:** keyed by random opaque token. Value: absolute root path + a sub-cache of (relpath → sha256, size, mtime) computed lazily by the dir handler.
- **Write paths:** keyed by random opaque token, single-use. `ConsumeWriteToken` atomically removes-and-returns; a second call returns `ok=false`.

Eviction: dir sha caches are bounded by `MaxDirCacheEntries` (drop-oldest by walk-time). Read-file table is unbounded for v1 (a typical Claude Code session references a handful of files); cap if it bites later.

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/driver/registry_test.go
package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistry_RegisterFile_ComputesSHAAndDedupes(t *testing.T) {
	r := NewFileRegistry(50000)
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	body := []byte("hello world")
	os.WriteFile(p, body, 0o644)

	sha1, size1, mime1, err := r.RegisterFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	wantHex := hex.EncodeToString(want[:])
	if sha1 != wantHex {
		t.Errorf("sha: got %s want %s", sha1, wantHex)
	}
	if size1 != int64(len(body)) {
		t.Errorf("size: %d", size1)
	}
	if mime1 == "" {
		t.Errorf("mime empty")
	}

	// Re-registering same file returns same sha; lookup works by sha.
	sha2, _, _, err := r.RegisterFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if sha1 != sha2 {
		t.Error("dedupe failed")
	}
	gotPath, ok := r.LookupBlob(sha1)
	if !ok || gotPath != p {
		t.Errorf("LookupBlob: ok=%v path=%s", ok, gotPath)
	}
}

func TestRegistry_RegisterDir_ReturnsTokenAndLookupReturnsRoot(t *testing.T) {
	r := NewFileRegistry(50000)
	dir := t.TempDir()
	tok := r.RegisterDir(dir)
	if tok == "" {
		t.Fatal("empty token")
	}
	root, ok := r.LookupDir(tok)
	if !ok || root != dir {
		t.Errorf("LookupDir: ok=%v root=%s want=%s", ok, root, dir)
	}
}

func TestRegistry_RegisterWrite_TokenIsSingleUse(t *testing.T) {
	r := NewFileRegistry(50000)
	tok := r.RegisterWrite("/tmp/out.txt", true, "task-1")
	if tok == "" {
		t.Fatal("empty token")
	}
	got, ok := r.ConsumeWriteToken(tok)
	if !ok || got.Path != "/tmp/out.txt" || !got.Overwrite || got.TaskID != "task-1" {
		t.Errorf("first consume: ok=%v got=%+v", ok, got)
	}
	if _, ok := r.ConsumeWriteToken(tok); ok {
		t.Error("second consume should fail (single-use)")
	}
}

func TestRegistry_DirSHACache_RoundTrip(t *testing.T) {
	r := NewFileRegistry(50000)
	tok := r.RegisterDir("/some/root")
	r.SetDirEntrySHA(tok, "sub/file.txt", "abc123", 100)
	sha, size, ok := r.GetDirEntrySHA(tok, "sub/file.txt")
	if !ok || sha != "abc123" || size != 100 {
		t.Errorf("cache: ok=%v sha=%s size=%d", ok, sha, size)
	}
}

func TestRegistry_PendingTask_TracksWrittenFiles(t *testing.T) {
	r := NewFileRegistry(50000)
	r.TrackTask("t-1", []string{"tok-a", "tok-b"})
	r.RecordWritten("t-1", WrittenFile{Path: "/out/a", Bytes: 10, SHA256: "x", WrittenAt: "2026-05-09T00:00:00Z"})
	written := r.WrittenFiles("t-1")
	if len(written) != 1 || written[0].Path != "/out/a" {
		t.Errorf("WrittenFiles: %+v", written)
	}
	r.ForgetTask("t-1")
	if w := r.WrittenFiles("t-1"); len(w) != 0 {
		t.Errorf("ForgetTask did not clear: %+v", w)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestRegistry -v`
Expected: FAIL — `NewFileRegistry`/`WrittenFile`/etc. undefined.

- [ ] **Step 3: Implement `registry.go`**

```go
// multi-agent/internal/driver/registry.go
package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// WriteEntry is what ConsumeWriteToken returns to the /files/put handler.
type WriteEntry struct {
	Path      string
	Overwrite bool
	TaskID    string
}

// WrittenFile is recorded when a slave successfully PUTs to a write token;
// surfaced via wait_task to Claude.
type WrittenFile struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	WrittenAt string `json:"written_at"`
}

// FileRegistry holds read/dir/write tokens for an in-flight driver process.
type FileRegistry struct {
	mu              sync.RWMutex
	blobs           map[string]string         // sha256 -> abs path
	blobMeta        map[string]blobMeta       // sha256 -> size, mime
	dirs            map[string]string         // dir token -> abs root
	writes          map[string]WriteEntry     // write token -> entry (single-use)
	dirSHA          map[string]map[string]dirEntry // dir token -> relpath -> entry
	maxDirEntries   int

	taskMu       sync.Mutex
	taskWritten  map[string][]WrittenFile  // task_id -> appended writes
}

type blobMeta struct {
	size int64
	mime string
}

type dirEntry struct {
	sha  string
	size int64
}

func NewFileRegistry(maxDirEntries int) *FileRegistry {
	if maxDirEntries <= 0 {
		maxDirEntries = 50000
	}
	return &FileRegistry{
		blobs:         map[string]string{},
		blobMeta:      map[string]blobMeta{},
		dirs:          map[string]string{},
		writes:        map[string]WriteEntry{},
		dirSHA:        map[string]map[string]dirEntry{},
		maxDirEntries: maxDirEntries,
		taskWritten:   map[string][]WrittenFile{},
	}
}

// RegisterFile reads the file, computes sha256, stores under that key, and
// returns (sha, size, mime, err). Re-registering an unchanged file is a no-op.
func (r *FileRegistry) RegisterFile(absPath string) (string, int64, string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("open %s: %w", absPath, err)
	}
	defer f.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, "", fmt.Errorf("read %s: %w", absPath, err)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	mt := mime.TypeByExtension(filepath.Ext(absPath))
	if mt == "" {
		// Probe first 512 bytes for magic-bytes-based detection.
		if _, err := f.Seek(0, 0); err == nil {
			head := make([]byte, 512)
			n, _ := f.Read(head)
			mt = http.DetectContentType(head[:n])
		}
	}
	r.mu.Lock()
	r.blobs[sum] = absPath
	r.blobMeta[sum] = blobMeta{size: size, mime: mt}
	r.mu.Unlock()
	return sum, size, mt, nil
}

func (r *FileRegistry) LookupBlob(sha string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.blobs[sha]
	return p, ok
}

func (r *FileRegistry) BlobMeta(sha string) (int64, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.blobMeta[sha]
	return m.size, m.mime, ok
}

// RegisterDir mints a random opaque token for a directory root.
func (r *FileRegistry) RegisterDir(absRoot string) string {
	tok := newToken()
	r.mu.Lock()
	r.dirs[tok] = absRoot
	r.dirSHA[tok] = map[string]dirEntry{}
	r.mu.Unlock()
	return tok
}

func (r *FileRegistry) LookupDir(tok string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.dirs[tok]
	return p, ok
}

// SetDirEntrySHA caches a (relpath -> sha,size) entry for a dir token, with
// bounded eviction (drop-arbitrary on overflow — order is not load-bearing).
func (r *FileRegistry) SetDirEntrySHA(tok, rel, sha string, size int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tab, ok := r.dirSHA[tok]
	if !ok {
		return // unknown token; caller's bug, not ours
	}
	if len(tab) >= r.maxDirEntries {
		// Drop one arbitrary entry — Go's map iteration order is randomized.
		for k := range tab {
			delete(tab, k)
			break
		}
	}
	tab[rel] = dirEntry{sha: sha, size: size}
}

func (r *FileRegistry) GetDirEntrySHA(tok, rel string) (string, int64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tab, ok := r.dirSHA[tok]
	if !ok {
		return "", 0, false
	}
	e, ok := tab[rel]
	return e.sha, e.size, ok
}

// RegisterWrite mints a single-use write token bound to (path, overwrite, task_id).
// taskID may be empty if the caller has not yet delegated.
func (r *FileRegistry) RegisterWrite(absPath string, overwrite bool, taskID string) string {
	tok := newToken()
	r.mu.Lock()
	r.writes[tok] = WriteEntry{Path: absPath, Overwrite: overwrite, TaskID: taskID}
	r.mu.Unlock()
	return tok
}

// ConsumeWriteToken atomically removes and returns the write entry.
// A second consume of the same token returns ok=false.
func (r *FileRegistry) ConsumeWriteToken(tok string) (WriteEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.writes[tok]
	if !ok {
		return WriteEntry{}, false
	}
	delete(r.writes, tok)
	return e, true
}

// TrackTask records a task_id -> write tokens association. Used by tools.go to
// later look up "what writes belong to this task" for wait_task reporting.
func (r *FileRegistry) TrackTask(taskID string, writeTokens []string) {
	// We do not need to store the tokens themselves (consume happens via the
	// /files/put handler), but reserving the slot prevents WrittenFiles from
	// returning nil for an in-flight task.
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	if _, ok := r.taskWritten[taskID]; !ok {
		r.taskWritten[taskID] = nil
	}
}

// RecordWritten appends a successful PUT to a task's written list.
// Called from the /files/put handler when it knows which task owns the token.
func (r *FileRegistry) RecordWritten(taskID string, w WrittenFile) {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	r.taskWritten[taskID] = append(r.taskWritten[taskID], w)
}

func (r *FileRegistry) WrittenFiles(taskID string) []WrittenFile {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	out := make([]WrittenFile, len(r.taskWritten[taskID]))
	copy(out, r.taskWritten[taskID])
	return out
}

func (r *FileRegistry) ForgetTask(taskID string) {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	delete(r.taskWritten, taskID)
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run TestRegistry -v`
Expected: PASS — all five subtests green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/registry.go internal/driver/registry_test.go
git commit -m "feat(driver): FileRegistry with sha-keyed blobs, dir tokens, single-use write tokens"
```

---

### Task 8: `manifest.go` (USER_FILES_MANIFEST encoder)

**Files:**
- Create: `multi-agent/internal/driver/manifest.go`
- Create: `multi-agent/internal/driver/manifest_test.go`

**Context:** Spec § 2. The encoded block is plain text with a one-line JSON payload between two literal fences. Always emitted, even with empty arrays. Paths are JSON-encoded so a filename containing `</USER_FILES_MANIFEST>` cannot inject a fake fence — the literal substring only appears as the final fence the encoder emits.

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/driver/manifest_test.go
package driver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManifest_Encode_EmptyArrays(t *testing.T) {
	m := Manifest{}
	out := m.Encode()
	if !strings.HasPrefix(out, "<USER_FILES_MANIFEST version=1>\n") {
		t.Errorf("missing opening fence: %q", out)
	}
	if !strings.HasSuffix(out, "\n</USER_FILES_MANIFEST>") {
		t.Errorf("missing closing fence: %q", out)
	}
	// Must contain the empty arrays even when both are empty.
	if !strings.Contains(out, `"files":[]`) || !strings.Contains(out, `"writes":[]`) {
		t.Errorf("empty arrays missing: %q", out)
	}
}

func TestManifest_Encode_WithEntries(t *testing.T) {
	m := Manifest{
		Files: []FileEntry{
			{Path: "/home/me/x.csv", Kind: "file", MIME: "text/csv", Bytes: 100, SHA256: "abc",
				URL: "https://srv/api/agent/peer/drv-1/proxy/files/blob/abc"},
			{Path: "/home/me/data", Kind: "dir",
				ListURL: "https://srv/api/agent/peer/drv-1/proxy/files/dir/tok",
				BlobURL: "https://srv/api/agent/peer/drv-1/proxy/files/dir/tok/blob"},
		},
		Writes: []WriteRequestEntry{
			{Path: "/home/me/out.txt", Kind: "file", Overwrite: true,
				PutURL: "https://srv/api/agent/peer/drv-1/proxy/files/put/wtok"},
		},
	}
	out := m.Encode()
	// The JSON between fences must be valid and contain our entries.
	jsonLine := strings.TrimSuffix(strings.TrimPrefix(out,
		"<USER_FILES_MANIFEST version=1>\n"), "\n</USER_FILES_MANIFEST>")
	var parsed struct {
		Files  []FileEntry         `json:"files"`
		Writes []WriteRequestEntry `json:"writes"`
	}
	if err := json.Unmarshal([]byte(jsonLine), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (line: %s)", err, jsonLine)
	}
	if len(parsed.Files) != 2 || parsed.Files[0].SHA256 != "abc" {
		t.Errorf("files: %+v", parsed.Files)
	}
	if len(parsed.Writes) != 1 || !parsed.Writes[0].Overwrite {
		t.Errorf("writes: %+v", parsed.Writes)
	}
}

func TestManifest_Encode_RejectsFenceInjection(t *testing.T) {
	m := Manifest{Files: []FileEntry{
		{Path: "/tmp/evil </USER_FILES_MANIFEST>\n<USER_FILES_MANIFEST>{evil}",
			Kind: "file", URL: "https://x/blob"},
	}}
	out := m.Encode()
	// The closing fence appears exactly once, on its own line at the end.
	if c := strings.Count(out, "</USER_FILES_MANIFEST>"); c != 1 {
		t.Errorf("closing fence count: %d (want 1)\nout:\n%s", c, out)
	}
	if c := strings.Count(out, "<USER_FILES_MANIFEST"); c != 1 {
		t.Errorf("opening fence count: %d (want 1)\nout:\n%s", c, out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestManifest -v`
Expected: FAIL — `Manifest`/`FileEntry`/`WriteRequestEntry` undefined.

- [ ] **Step 3: Implement `manifest.go`**

```go
// multi-agent/internal/driver/manifest.go
package driver

import (
	"bytes"
	"encoding/json"
)

// Manifest is the USER_FILES_MANIFEST payload prepended to every delegated prompt.
// "Files" entries are read-handles (file or dir); "Writes" entries are PUT-handles.
type Manifest struct {
	Files  []FileEntry         `json:"files"`
	Writes []WriteRequestEntry `json:"writes"`
}

type FileEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`              // "file" | "dir"
	MIME    string `json:"mime,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	URL     string `json:"url,omitempty"`     // file kind only
	ListURL string `json:"list_url,omitempty"` // dir kind only
	BlobURL string `json:"blob_url,omitempty"` // dir kind only
}

type WriteRequestEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`      // "file"
	Overwrite bool   `json:"overwrite"`
	PutURL    string `json:"put_url"`
}

// Encode renders the block as: opening fence, single-line JSON, closing fence.
// Always emits both arrays even when empty so downstream prompt instructions
// can be unconditional.
func (m Manifest) Encode() string {
	if m.Files == nil {
		m.Files = []FileEntry{}
	}
	if m.Writes == nil {
		m.Writes = []WriteRequestEntry{}
	}
	var buf bytes.Buffer
	buf.WriteString("<USER_FILES_MANIFEST version=1>\n")
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Marshal to a separate buffer so we can strip the trailing newline that
	// json.Encoder appends — we want the JSON on a single line, then our
	// own \n before the closing fence.
	var inner bytes.Buffer
	innerEnc := json.NewEncoder(&inner)
	innerEnc.SetEscapeHTML(false)
	_ = innerEnc.Encode(m)
	jsonLine := bytes.TrimRight(inner.Bytes(), "\n")
	buf.Write(jsonLine)
	buf.WriteString("\n</USER_FILES_MANIFEST>")
	return buf.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run TestManifest -v`
Expected: PASS — all three subtests green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/manifest.go internal/driver/manifest_test.go
git commit -m "feat(driver): Manifest encoder for USER_FILES_MANIFEST block"
```

---

**End of Phase B.** All five files compile, all tests pass, and the package has no external dependencies beyond `gopkg.in/yaml.v3`. Phase C builds on these to mount the HTTP handlers.

---

## Phase C — files HTTP layer + webui hook (Tasks 9–10)

These two tasks turn the in-memory `FileRegistry` into a network-reachable surface. The driver's mux sits behind the agentserver tunnel; the only requests that reach it have already been workspace-checked at the agentserver boundary (Task 2). The driver's middleware additionally enforces "this came over the tunnel" by requiring the `X-Agentserver-Peer-Short-Id` header (which Task 2 injects) to be non-empty.

### Task 9: `files_handler.go` (the four `/files/*` handlers + middleware)

**Files:**
- Create: `multi-agent/internal/driver/files_handler.go`
- Create: `multi-agent/internal/driver/files_handler_test.go`

**Context:** Spec § 3 + § 5. The handler exposes:

| Path | Method | Purpose |
|---|---|---|
| `/files/blob/{sha256}` | GET | Stream a registered file by sha |
| `/files/dir/{token}` | GET | List a registered dir; `?recursive=true` walks deep, `?prefix=...` filters |
| `/files/dir/{token}/blob` | GET | Stream one file under a registered dir; `?path=relpath` |
| `/files/put/{token}` | PUT | Atomically write bytes to a registered write target |

The middleware `WithPeerHeader` rejects requests missing `X-Agentserver-Peer-Short-Id` with 401. All four handlers expect that header to be present (set by agentserver's peer-proxy).

- [ ] **Step 1: Write the failing test (one test per handler + middleware)**

```go
// multi-agent/internal/driver/files_handler_test.go
package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: build a mux + audit + registry wired together.
func newTestHandler(t *testing.T) (*FilesHandler, *FileRegistry, *AuditLog) {
	t.Helper()
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	r := NewFileRegistry(50000)
	h := NewFilesHandler(r, a)
	return h, r, a
}

func reqWithPeer(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-Agentserver-Peer-Short-Id", "slv-test")
	return r
}

func TestFilesHandler_Blob_Streams(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "x.bin")
	body := []byte("hello world")
	os.WriteFile(p, body, 0o644)
	sha, _, _, _ := r.RegisterFile(p)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/blob/"+sha, nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body=%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "hello world" {
		t.Errorf("body: %q", w.Body.String())
	}
}

func TestFilesHandler_Blob_404(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/blob/nope", nil))
	if w.Code != 404 {
		t.Errorf("status: %d", w.Code)
	}
}

func TestFilesHandler_RequiresPeerHeader(t *testing.T) {
	h, _, _ := newTestHandler(t)
	r := httptest.NewRequest("GET", "/files/blob/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("missing-peer should be 401, got %d", w.Code)
	}
}

func TestFilesHandler_Dir_ListRecursive(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("yo"), 0o644)
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"?recursive=true", nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body=%s)", w.Code, w.Body.String())
	}
	var got struct {
		Root    string `json:"root"`
		Entries []struct {
			RelPath string `json:"relpath"`
			IsDir   bool   `json:"is_dir"`
			SHA256  string `json:"sha256"`
			Size    int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Root != dir {
		t.Errorf("root: %s", got.Root)
	}
	rels := map[string]bool{}
	for _, e := range got.Entries {
		rels[e.RelPath] = true
	}
	if !rels["a.txt"] || !rels[filepath.Join("sub", "b.txt")] {
		t.Errorf("entries missing: %+v", got.Entries)
	}
}

func TestFilesHandler_Dir_NonRecursiveOnlyTopLevel(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("yo"), 0o644)
	tok := r.RegisterDir(dir)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok, nil))
	var got struct {
		Entries []struct{ RelPath string `json:"relpath"` } `json:"entries"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	for _, e := range got.Entries {
		if strings.Contains(e.RelPath, string(filepath.Separator)) {
			t.Errorf("non-recursive returned nested entry: %s", e.RelPath)
		}
	}
}

func TestFilesHandler_DirBlob_RejectsEscape(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"/blob?path=../etc/passwd", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("escape: status %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestFilesHandler_DirBlob_StreamsHappy(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"/blob?path=a.txt", nil))
	if w.Code != 200 {
		t.Fatalf("status %d (body=%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "hi" {
		t.Errorf("body: %q", w.Body.String())
	}
}

func TestFilesHandler_Put_Atomic(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	tok := r.RegisterWrite(target, true, "task-1")
	r.TrackTask("task-1", []string{tok})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("hello world")))
	if w.Code != 200 {
		t.Fatalf("status %d (body=%s)", w.Code, w.Body.String())
	}
	got, _ := os.ReadFile(target)
	if string(got) != "hello world" {
		t.Errorf("file: %q", got)
	}
	written := r.WrittenFiles("task-1")
	if len(written) != 1 || written[0].Bytes != 11 {
		t.Errorf("WrittenFiles: %+v", written)
	}
	want := sha256.Sum256([]byte("hello world"))
	if written[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha mismatch: %s", written[0].SHA256)
	}
}

func TestFilesHandler_Put_OverwriteFalse_Conflict(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	os.WriteFile(target, []byte("existing"), 0o644)
	tok := r.RegisterWrite(target, false, "task-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("new")))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "existing" {
		t.Errorf("file overwritten: %q", got)
	}
}

func TestFilesHandler_Put_TokenSingleUse(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	tok := r.RegisterWrite(target, true, "task-1")

	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("x")))
	if w1.Code != 200 {
		t.Fatalf("first put: %d", w1.Code)
	}
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("y")))
	if w2.Code != http.StatusGone {
		t.Errorf("second put: want 410, got %d", w2.Code)
	}
}

func TestFilesHandler_Put_MissingParent(t *testing.T) {
	h, r, _ := newTestHandler(t)
	target := "/nonexistent/dir/out.txt"
	tok := r.RegisterWrite(target, true, "task-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("x")))
	if w.Code != http.StatusConflict {
		t.Errorf("missing parent: status %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestFilesHandler -v`
Expected: FAIL — `FilesHandler` / `NewFilesHandler` undefined.

- [ ] **Step 3: Implement `files_handler.go`**

```go
// multi-agent/internal/driver/files_handler.go
package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilesHandler is the http.Handler for /files/* serving the FileRegistry.
type FilesHandler struct {
	reg   *FileRegistry
	audit *AuditLog
}

func NewFilesHandler(reg *FileRegistry, audit *AuditLog) *FilesHandler {
	return &FilesHandler{reg: reg, audit: audit}
}

// ServeHTTP enforces the peer header then dispatches by path.
func (h *FilesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	peer := r.Header.Get("X-Agentserver-Peer-Short-Id")
	if peer == "" {
		http.Error(w, "missing X-Agentserver-Peer-Short-Id", http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/files/blob/"):
		h.handleBlob(w, r, peer)
	case strings.HasPrefix(r.URL.Path, "/files/dir/"):
		// /files/dir/{tok}/blob OR /files/dir/{tok}
		rest := strings.TrimPrefix(r.URL.Path, "/files/dir/")
		if i := strings.Index(rest, "/blob"); i > 0 && rest[i:] == "/blob" {
			tok := rest[:i]
			h.handleDirBlob(w, r, peer, tok)
		} else {
			h.handleDirList(w, r, peer, rest)
		}
	case strings.HasPrefix(r.URL.Path, "/files/put/"):
		h.handlePut(w, r, peer)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *FilesHandler) handleBlob(w http.ResponseWriter, r *http.Request, peer string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sha := strings.TrimPrefix(r.URL.Path, "/files/blob/")
	path, ok := h.reg.LookupBlob(sha)
	if !ok {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	if err := AssertNoSymlinkLeaf(path); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	size, mt, _ := h.reg.BlobMeta(sha)
	if mt != "" {
		w.Header().Set("Content-Type", mt)
	}
	if size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}
	written, _ := io.Copy(w, f)
	h.audit.Log(AuditEvent{
		Event: "fetch_blob", Path: path, SHA256: sha,
		Bytes: written, PeerShortID: peer,
	})
}

func (h *FilesHandler) handleDirList(w http.ResponseWriter, r *http.Request, peer, tok string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root, ok := h.reg.LookupDir(tok)
	if !ok {
		http.Error(w, "dir token not found", http.StatusNotFound)
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true"
	prefix := r.URL.Query().Get("prefix")
	if prefix != "" {
		if err := AssertSafeRelPath(prefix); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	type entry struct {
		RelPath    string `json:"relpath"`
		Size       int64  `json:"size"`
		MTime      string `json:"mtime"`
		SHA256     string `json:"sha256,omitempty"`
		IsDir      bool   `json:"is_dir"`
		IsSymlink  bool   `json:"is_symlink,omitempty"`
		Skipped    bool   `json:"skipped,omitempty"`
	}

	walkRoot := root
	if prefix != "" {
		walkRoot = filepath.Join(root, prefix)
	}
	var out []entry
	walkErr := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if path == walkRoot {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		info, _ := d.Info()
		isSym := info.Mode()&os.ModeSymlink != 0
		if isSym {
			out = append(out, entry{RelPath: rel, IsSymlink: true, Skipped: true,
				MTime: info.ModTime().UTC().Format(time.RFC3339)})
			return nil
		}
		if d.IsDir() {
			out = append(out, entry{RelPath: rel, IsDir: true,
				MTime: info.ModTime().UTC().Format(time.RFC3339)})
			if !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		// regular file
		var sha string
		var size int64 = info.Size()
		if cachedSha, _, ok := h.reg.GetDirEntrySHA(tok, rel); ok {
			sha = cachedSha
		} else {
			sha, _ = sha256OfFile(path)
			h.reg.SetDirEntrySHA(tok, rel, sha, size)
		}
		out = append(out, entry{RelPath: rel, Size: size, SHA256: sha,
			MTime: info.ModTime().UTC().Format(time.RFC3339)})
		// In non-recursive mode, skip into subdirs is handled above; the
		// walk still descends to top-level files since they are not dirs.
		_ = recursive
		return nil
	})
	if walkErr != nil {
		http.Error(w, walkErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"root":    root,
		"entries": out,
	})
	h.audit.Log(AuditEvent{Event: "fetch_dir", Path: root, PeerShortID: peer})
}

func (h *FilesHandler) handleDirBlob(w http.ResponseWriter, r *http.Request, peer, tok string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root, ok := h.reg.LookupDir(tok)
	if !ok {
		http.Error(w, "dir token not found", http.StatusNotFound)
		return
	}
	rel := r.URL.Query().Get("path")
	if err := AssertSafeRelPath(rel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := filepath.Join(root, rel)
	// Reject symlinks at every component (defense in depth).
	if !strings.HasPrefix(target+string(filepath.Separator), root+string(filepath.Separator)) &&
		target != root {
		http.Error(w, "path escapes root", http.StatusBadRequest)
		return
	}
	cur := root
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "symlink in path: "+cur, http.StatusForbidden)
			return
		}
	}
	f, err := os.Open(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	written, _ := io.Copy(w, f)
	h.audit.Log(AuditEvent{
		Event: "fetch_blob", Path: target,
		Bytes: written, PeerShortID: peer,
	})
}

func (h *FilesHandler) handlePut(w http.ResponseWriter, r *http.Request, peer string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/files/put/")
	entry, ok := h.reg.ConsumeWriteToken(tok)
	if !ok {
		http.Error(w, "token not found or already used", http.StatusGone)
		return
	}
	target := entry.Path
	parent := filepath.Dir(target)
	if _, err := os.Stat(parent); err != nil {
		http.Error(w, "parent dir missing: "+parent, http.StatusConflict)
		return
	}
	if _, err := os.Stat(target); err == nil && !entry.Overwrite {
		http.Error(w, "target exists and overwrite=false", http.StatusConflict)
		return
	}
	tmpName := fmt.Sprintf("%s.tmp.%s", target, randSuffix())
	out, err := os.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)
	written, copyErr := io.Copy(mw, r.Body)
	if copyErr != nil {
		out.Close()
		os.Remove(tmpName)
		http.Error(w, copyErr.Error(), http.StatusInternalServerError)
		return
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmpName)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out.Close()
	if err := os.Rename(tmpName, target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sha := hex.EncodeToString(hasher.Sum(nil))
	if entry.TaskID != "" {
		h.reg.RecordWritten(entry.TaskID, WrittenFile{
			Path:      target,
			Bytes:     written,
			SHA256:    sha,
			WrittenAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	h.audit.Log(AuditEvent{
		Event: "put_blob", Path: target, SHA256: sha,
		Bytes: written, TaskID: entry.TaskID, PeerShortID: peer,
		Overwrite: entry.Overwrite,
	})
	w.WriteHeader(http.StatusOK)
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func randSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run TestFilesHandler -v`
Expected: PASS — all eleven subtests green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/files_handler.go internal/driver/files_handler_test.go
git commit -m "feat(driver): /files/* HTTP handlers (blob, dir list, dir blob, atomic put)"
```

---

### Task 10: webui `SetDriverFiles` helper

**Files:**
- Modify: `multi-agent/internal/webui/server.go` (add helper near `SetMCPBridge` at line 39–45)
- Modify: `multi-agent/internal/webui/server_test.go` (add one test)

**Context:** The driver mounts `FilesHandler` into the webui mux that `cli.Connect(Handlers{HTTP: ...})` exposes over the agentserver tunnel. The existing `NewHandler` is geared for master/slave: it expects a `*store.Store` and `*config.Config` and registers `/healthz`, `/tasks`, etc. The driver does not need any of those; it needs `/files/*`.

The cleanest fit (and what the spec asks for) is a thin compositor: `SetDriverFiles(base http.Handler, files http.Handler) http.Handler` returns a handler that routes `/files/*` to `files`, everything else to `base`. The driver constructs `base` as a tiny mux with just `/healthz`, then composes.

- [ ] **Step 1: Write the failing test**

```go
// Add to multi-agent/internal/webui/server_test.go:

func TestSetDriverFiles_RoutesFilesPathToDriverHandler(t *testing.T) {
	base := http.NewServeMux()
	base.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("base-healthz"))
	})
	files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("files-hit:" + r.URL.Path))
	})
	composed := webui.SetDriverFiles(base, files)

	w1 := httptest.NewRecorder()
	composed.ServeHTTP(w1, httptest.NewRequest("GET", "/healthz", nil))
	if w1.Body.String() != "base-healthz" {
		t.Errorf("non-files path: %q", w1.Body.String())
	}
	w2 := httptest.NewRecorder()
	composed.ServeHTTP(w2, httptest.NewRequest("GET", "/files/blob/abc", nil))
	if w2.Body.String() != "files-hit:/files/blob/abc" {
		t.Errorf("files path: %q", w2.Body.String())
	}
}
```

> Imports needed in `server_test.go` if not already present: `"net/http"`, `"net/http/httptest"`, `"testing"`, and `webui "github.com/yourorg/multi-agent/internal/webui"` if the test is in `webui_test`. If the existing tests are in package `webui` (internal), drop the alias and call `SetDriverFiles` directly.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/webui/ -run TestSetDriverFiles -v`
Expected: FAIL — `SetDriverFiles` undefined.

- [ ] **Step 3: Implement the helper**

Add to `multi-agent/internal/webui/server.go`, immediately after `SetMCPBridge` (around line 45):

```go
// SetDriverFiles composes a base handler with a driver /files/* handler.
// The returned http.Handler routes any request whose path begins with
// "/files/" to filesHandler; all other requests fall through to base.
// Both handlers are responsible for their own auth/middleware.
//
// The driver-agent uses this to mount its FilesHandler alongside a tiny
// healthz-only base mux behind the agentserver tunnel.
func SetDriverFiles(base http.Handler, filesHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			filesHandler.ServeHTTP(w, r)
			return
		}
		base.ServeHTTP(w, r)
	})
}
```

> If `strings` is not already imported in `server.go`, add it to the import block. (It is already imported per the file we read in batch-1 prep.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/webui/ -run TestSetDriverFiles -v && go test ./internal/webui/ -v`
Expected: PASS — new test green; existing tests unchanged.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/webui/server.go internal/webui/server_test.go
git commit -m "feat(webui): SetDriverFiles compositor for driver /files/* sub-mux"
```

---

**End of Phase C.** The driver now has a fully tested HTTP layer. Phase D wires the MCP stdio loop and the six tools that fan out to the workspace.

---

## Phase D — MCP stdio server + the six tools (Tasks 11–12)

These are the integration glue between Claude Code (above) and the agentserver SDK (below). The MCP server in Task 11 is protocol plumbing that doesn't know about driver tools; tools are registered through a small interface. Task 12 implements the six tool handlers using a tiny `SDKClient` interface so they can be unit-tested against fakes.

### Task 11: `mcp_server.go` (stdio JSON-RPC dispatcher)

**Files:**
- Create: `multi-agent/internal/driver/mcp_server.go`
- Create: `multi-agent/internal/driver/mcp_server_test.go`

**Context:** Spec § 1 (MCP tool surface). The MCP protocol is JSON-RPC 2.0 over stdin/stdout, one message per line. Methods we implement:

| Method | Type | Purpose |
|---|---|---|
| `initialize` | request | Handshake. Echo back the client's `protocolVersion`; advertise `capabilities.tools = {}`. |
| `notifications/initialized` | notification | Client confirmation; no response. |
| `tools/list` | request | Return the registered tool schemas. |
| `tools/call` | request | Dispatch to a registered handler by name. Errors surface as JSON-RPC `-32000`. |
| `shutdown` | request | Graceful close; respond `{}`. |
| `exit` | notification | Stop the loop. |

The dispatcher does not import any driver-specific types; tools are registered through the `Tool` interface (`Name() / Description() / InputSchema() / Call()`).

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/driver/mcp_server_test.go
package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// mockTool: simplest possible Tool implementer for testing dispatch.
type mockTool struct {
	name string
	call func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

func (m *mockTool) Name() string                 { return m.name }
func (m *mockTool) Description() string          { return "mock tool" }
func (m *mockTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) Call(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return m.call(ctx, args)
}

func TestMCPServer_InitializeHandshake(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"claude-code","version":"x"}}}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(firstLine(out.String()), &resp); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out.String())
	}
	if resp.ID != 1 {
		t.Errorf("id: %d", resp.ID)
	}
	if !bytes.Contains(resp.Result, []byte(`"protocolVersion":"2024-11-05"`)) {
		t.Errorf("init result missing protocolVersion: %s", resp.Result)
	}
}

func TestMCPServer_ToolsList_ReturnsSchemas(t *testing.T) {
	tools := []Tool{
		&mockTool{name: "alpha", call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) { return nil, nil }},
		&mockTool{name: "beta", call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) { return nil, nil }},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer(tools)
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if len(resp.Result.Tools) != 2 {
		t.Errorf("tools count: %d (out=%s)", len(resp.Result.Tools), out.String())
	}
	names := map[string]bool{}
	for _, tt := range resp.Result.Tools {
		names[tt.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("names: %+v", names)
	}
}

func TestMCPServer_ToolsCall_Dispatch(t *testing.T) {
	called := false
	tool := &mockTool{
		name: "do",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			called = true
			if !bytes.Contains(args, []byte(`"x":42`)) {
				t.Errorf("args: %s", args)
			}
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"do","arguments":{"x":42}}}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{tool})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	if !called {
		t.Fatalf("tool not invoked (out=%s)", out.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Type != "text" {
		t.Fatalf("result shape: %+v", resp.Result)
	}
	if !strings.Contains(resp.Result.Content[0].Text, `"ok":true`) {
		t.Errorf("text: %s", resp.Result.Content[0].Text)
	}
}

func TestMCPServer_ToolsCall_ErrorCode(t *testing.T) {
	tool := &mockTool{
		name: "bad",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, &MCPToolError{Message: "bad input"}
		},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"bad","arguments":{}}}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{tool})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if resp.Error.Code != -32000 {
		t.Errorf("code: %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "bad input") {
		t.Errorf("msg: %s", resp.Error.Message)
	}
}

func TestMCPServer_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"weird/thing"}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if resp.Error.Code != -32601 {
		t.Errorf("code: %d", resp.Error.Code)
	}
}

func firstLine(s string) []byte {
	i := strings.Index(s, "\n")
	if i < 0 {
		return []byte(s)
	}
	return []byte(s[:i])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestMCPServer -v`
Expected: FAIL — `Tool`/`NewMCPServer`/`MCPToolError`/`WaitForLines` undefined.

- [ ] **Step 3: Implement `mcp_server.go`**

```go
// multi-agent/internal/driver/mcp_server.go
package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Tool is what the MCP server dispatches to. Each tool advertises a name,
// description, and JSON-Schema for its arguments.
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Call(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// MCPToolError lets a tool return a JSON-RPC -32000 error with a custom message.
// Any other error from Call is also surfaced as -32000.
type MCPToolError struct {
	Message string
}

func (e *MCPToolError) Error() string { return e.Message }

type MCPServer struct {
	tools     map[string]Tool
	toolOrder []string
	writeMu   sync.Mutex
	linesOut  int64
}

func NewMCPServer(tools []Tool) *MCPServer {
	s := &MCPServer{tools: map[string]Tool{}}
	for _, t := range tools {
		s.tools[t.Name()] = t
		s.toolOrder = append(s.toolOrder, t.Name())
	}
	return s
}

// jsonRPCRequest models incoming JSON-RPC 2.0 messages.
// ID is *json.RawMessage so notifications (id absent) are distinguishable.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Serve reads one JSON-RPC message per line from r and writes responses to w.
// Returns when r reaches EOF or an "exit" notification is received.
func (s *MCPServer) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // up to 16 MiB messages
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(w, json.RawMessage(`null`), -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "exit" {
			return nil
		}
		s.dispatch(w, &req)
	}
	return scanner.Err()
}

// WaitForLines is a test helper that blocks until at least n response lines
// have been written. Used so tests don't race the goroutine running Serve.
func (s *MCPServer) WaitForLines(n int) {
	for atomic.LoadInt64(&s.linesOut) < int64(n) {
		// Tight spin is fine: tests produce a handful of lines microseconds apart.
	}
}

func (s *MCPServer) dispatch(w io.Writer, req *jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "notifications/initialized":
		// No response for notifications.
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, req)
	case "shutdown":
		s.writeResult(w, req.ID, struct{}{})
	default:
		if req.ID != nil {
			s.writeError(w, *req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func (s *MCPServer) handleInitialize(w io.Writer, req *jsonRPCRequest) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	json.Unmarshal(req.Params, &p)
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "2024-11-05"
	}
	s.writeResult(w, req.ID, map[string]interface{}{
		"protocolVersion": p.ProtocolVersion,
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"serverInfo":      map[string]interface{}{"name": "driver-agent", "version": "0.1.0"},
	})
}

func (s *MCPServer) handleToolsList(w io.Writer, req *jsonRPCRequest) {
	type schema struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	out := make([]schema, 0, len(s.toolOrder))
	for _, name := range s.toolOrder {
		t := s.tools[name]
		out = append(out, schema{Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema()})
	}
	s.writeResult(w, req.ID, map[string]interface{}{"tools": out})
}

func (s *MCPServer) handleToolsCall(w io.Writer, req *jsonRPCRequest) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(w, idOrNull(req.ID), -32602, "invalid params: "+err.Error())
		return
	}
	t, ok := s.tools[p.Name]
	if !ok {
		s.writeError(w, idOrNull(req.ID), -32602, "unknown tool: "+p.Name)
		return
	}
	result, err := t.Call(context.Background(), p.Arguments)
	if err != nil {
		s.writeError(w, idOrNull(req.ID), -32000, err.Error())
		return
	}
	s.writeResult(w, req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": string(result)},
		},
	})
}

func (s *MCPServer) writeResult(w io.Writer, id *json.RawMessage, result interface{}) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: idOrNull(id), Result: result}
	s.writeLine(w, resp)
}

func (s *MCPServer) writeError(w io.Writer, id json.RawMessage, code int, msg string) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: msg}}
	s.writeLine(w, resp)
}

func (s *MCPServer) writeLine(w io.Writer, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	w.Write(b)
	w.Write([]byte("\n"))
	if f, ok := w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	atomic.AddInt64(&s.linesOut, 1)
}

func idOrNull(id *json.RawMessage) json.RawMessage {
	if id == nil {
		return json.RawMessage(`null`)
	}
	return *id
}

// fmt is used only by some tools; suppress the unused-import lint if this file
// is built without them by referencing it here as a no-op.
var _ = fmt.Sprintf
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/driver/ -run TestMCPServer -v`
Expected: PASS — five subtests green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/driver/mcp_server.go internal/driver/mcp_server_test.go
git commit -m "feat(driver): MCP stdio JSON-RPC server with Tool dispatch"
```

---

### Task 12: `tools.go` — the six MCP tools

**Files:**
- Create: `multi-agent/internal/driver/tools.go`
- Create: `multi-agent/internal/driver/tools_test.go`

**Context:** Spec § 1 (Handling input paths inside `submit_task`), § 6 (`wait_task` → `written_files`). Each tool implements the `Tool` interface from Task 11. They share state via a `Tools` struct that holds the `FileRegistry`, `AuditLog`, an `SDKClient` interface (so tests can swap in a fake), config defaults, and the driver's own `short_id` + `server.url` for minting URLs.

`SDKClient` is defined narrowly here because the driver only needs four methods. The concrete `*agentsdk.Client` satisfies it.

- [ ] **Step 1: Write the failing test (one test per tool, with a fake SDK)**

```go
// multi-agent/internal/driver/tools_test.go
package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

// fakeSDK satisfies the SDKClient interface for tests.
type fakeSDK struct {
	discoverFunc  func() ([]agentsdk.AgentCard, error)
	delegateFunc  func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	getTaskFunc   func(id string, includeOutput bool) (*agentsdk.TaskInfo, error)
	peerProxyFunc func(method, target, path string, body io.Reader) (*http.Response, error)
}

func (f *fakeSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.discoverFunc()
}
func (f *fakeSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	return f.delegateFunc(req)
}
func (f *fakeSDK) GetTask(ctx context.Context, id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
	return f.getTaskFunc(id, includeOutput)
}
func (f *fakeSDK) PeerProxy(ctx context.Context, method, target, path string, body io.Reader) (*http.Response, error) {
	return f.peerProxyFunc(method, target, path, body)
}

func newTestTools(t *testing.T, sdk SDKClient) *Tools {
	t.Helper()
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	cfg := &Config{}
	cfg.Server.URL = "https://srv.example.com"
	cfg.Credentials.ShortID = "drv-001"
	cfg.Credentials.SandboxID = "sbx-driver"
	cfg.DriverDefaults.TaskTimeoutSec = 600
	return NewTools(NewFileRegistry(50000), a, sdk, cfg)
}

func TestTool_ListAgents_FiltersSelf(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver-yuzishu"},
				{AgentID: "sbx-master", DisplayName: "master-prod",
					Card: json.RawMessage(`{"skills":["fanout"],"tools":[]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "list_agents" {
			out, err := tt.Call(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "driver-yuzishu") {
				t.Errorf("self not filtered: %s", out)
			}
			if !strings.Contains(string(out), "master-prod") {
				t.Errorf("master missing: %s", out)
			}
			return
		}
	}
	t.Fatal("list_agents tool not registered")
}

func TestTool_SubmitTask_RegistersFilesAndDelegates(t *testing.T) {
	dir := t.TempDir()
	in1 := filepath.Join(dir, "a.txt")
	in2 := filepath.Join(dir, "b.txt")
	out := filepath.Join(dir, "out.txt")
	os.WriteFile(in1, []byte("hello"), 0o644)
	os.WriteFile(in2, []byte("world"), 0o644)

	var gotPrompt string
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master-prod",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			gotPrompt = req.Prompt
			if req.TargetID != "sbx-master" {
				t.Errorf("target_id: %s", req.TargetID)
			}
			if req.Skill != "fanout" {
				t.Errorf("skill: %s", req.Skill)
			}
			if req.TimeoutSeconds != 600 {
				t.Errorf("timeout: %d", req.TimeoutSeconds)
			}
			return &agentsdk.DelegateTaskResponse{TaskID: "t-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	args := json.RawMessage(`{
        "prompt": "merge these",
        "read_paths": ["` + in1 + `", "` + in2 + `"],
        "write_paths": [{"path": "` + out + `", "overwrite": true}]
    }`)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			res, err := tt.Call(context.Background(), args)
			if err != nil {
				t.Fatalf("submit_task: %v", err)
			}
			var parsed struct {
				TaskID   string `json:"task_id"`
				Manifest struct {
					Files  []FileEntry         `json:"files"`
					Writes []WriteRequestEntry `json:"writes"`
				} `json:"manifest"`
			}
			json.Unmarshal(res, &parsed)
			if parsed.TaskID != "t-1" {
				t.Errorf("task_id: %s", parsed.TaskID)
			}
			if len(parsed.Manifest.Files) != 2 {
				t.Errorf("files: %+v", parsed.Manifest.Files)
			}
			if len(parsed.Manifest.Writes) != 1 || !parsed.Manifest.Writes[0].Overwrite {
				t.Errorf("writes: %+v", parsed.Manifest.Writes)
			}
			if !strings.Contains(gotPrompt, "<USER_FILES_MANIFEST") {
				t.Errorf("manifest not in prompt: %s", gotPrompt)
			}
			if !strings.Contains(gotPrompt, "merge these") {
				t.Errorf("user prompt not preserved: %s", gotPrompt)
			}
			return
		}
	}
	t.Fatal("submit_task tool not registered")
}

func TestTool_SubmitTask_RejectsAmbiguousTarget(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master-a", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "m2", DisplayName: "master-b", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			_, err := tt.Call(context.Background(), json.RawMessage(`{"prompt":"x"}`))
			if err == nil {
				t.Fatal("expected ambiguous-target error")
			}
			if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "candidates") {
				t.Errorf("error message: %v", err)
			}
			return
		}
	}
}

func TestTool_GetTask_ReturnsStatus(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			if id != "t-1" {
				return nil, errors.New("nope")
			}
			return &agentsdk.TaskInfo{TaskID: "t-1", Status: "completed", Output: "done"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "get_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t-1"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"status":"completed"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_WaitTask_ReturnsWrittenFiles(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Output: "hi"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.reg.RecordWritten("t-2", WrittenFile{Path: "/p", Bytes: 5, SHA256: "s"})
	for _, tt := range tools.All() {
		if tt.Name() == "wait_task" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t-2","poll_interval_sec":1,"timeout_sec":5}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"written_files"`) || !strings.Contains(string(res), `"/p"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_TailSubtasks_PeerProxiesMaster(t *testing.T) {
	called := false
	sdk := &fakeSDK{
		// tail_subtasks needs to know which agent is the master; for the fake
		// we have it discover one fanout-skilled agent with short_id "m-short".
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master-prod",
					ShortID: "m-short",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		peerProxyFunc: func(method, target, path string, body io.Reader) (*http.Response, error) {
			called = true
			if target != "m-short" {
				t.Errorf("target: %s", target)
			}
			if !strings.Contains(path, "/tasks/t-9/children") {
				t.Errorf("path: %s", path)
			}
			body2 := `[{"node_id":"n1","status":"completed","target_id":"slv","created_at":"2026-01-01T00:00:00Z"}]`
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body2)),
				Header:     http.Header{},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "tail_subtasks" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t-9","since_seq":0,"max_wait_sec":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("PeerProxy not called")
			}
			if !strings.Contains(string(res), `"events"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_CancelTask_StubReturnsNotSupported(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "running"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "cancel_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t-3"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"ok":false`) {
				t.Errorf("res: %s", res)
			}
			if !strings.Contains(string(res), "running") {
				t.Errorf("res must include current status: %s", res)
			}
			return
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/driver/ -run TestTool_ -v`
Expected: FAIL — `Tools`/`NewTools`/`SDKClient`/`SubmitTaskArgs`/etc undefined.

- [ ] **Step 3: Implement `tools.go`**

```go
// multi-agent/internal/driver/tools.go
package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

// SDKClient is the narrow agentserver SDK surface the driver tools use.
// *agentsdk.Client satisfies this interface; tests provide their own fake.
type SDKClient interface {
	DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
	DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	GetTask(ctx context.Context, taskID string, includeOutput bool) (*agentsdk.TaskInfo, error)
	PeerProxy(ctx context.Context, method, targetShortID, path string, body io.Reader) (*http.Response, error)
}

// Tools holds shared state and exposes the six MCP tools as a slice.
type Tools struct {
	reg   *FileRegistry
	audit *AuditLog
	sdk   SDKClient
	cfg   *Config
}

func NewTools(reg *FileRegistry, audit *AuditLog, sdk SDKClient, cfg *Config) *Tools {
	return &Tools{reg: reg, audit: audit, sdk: sdk, cfg: cfg}
}

// All returns the six tools in stable order.
func (t *Tools) All() []Tool {
	return []Tool{
		&listAgentsTool{t},
		&submitTaskTool{t},
		&getTaskTool{t},
		&waitTaskTool{t},
		&tailSubtasksTool{t},
		&cancelTaskTool{t},
	}
}

// peerProxyURL builds the agentserver-side URL for the driver's own /files/* endpoint.
// Slaves use this URL to fetch back through the peer-proxy route.
func (t *Tools) peerProxyURL(suffix string) string {
	return strings.TrimRight(t.cfg.Server.URL, "/") +
		"/api/agent/peer/" + t.cfg.Credentials.ShortID + "/proxy" + suffix
}

// resolveTarget picks a target agent by display_name override, config default,
// or auto-pick of the unique fanout-skilled agent.
func (t *Tools) resolveTarget(ctx context.Context, override string) (id, displayName, shortID string, err error) {
	cards, err := t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return "", "", "", &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	if override == "" {
		override = t.cfg.DriverDefaults.TargetDisplayName
	}
	if override != "" {
		for _, c := range cards {
			if c.DisplayName == override && c.AgentID != t.cfg.Credentials.SandboxID {
				return c.AgentID, c.DisplayName, cardShortID(c), nil
			}
		}
		return "", "", "", &MCPToolError{Message: "no agent named: " + override}
	}
	// Auto-pick: unique fanout-skilled agent.
	var matches []agentsdk.AgentCard
	for _, c := range cards {
		if c.AgentID == t.cfg.Credentials.SandboxID {
			continue
		}
		if hasSkill(c, "fanout") {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return "", "", "", &MCPToolError{Message: "no fanout-skilled agent available; pass target_display_name"}
	}
	if len(matches) > 1 {
		names := []string{}
		for _, m := range matches {
			names = append(names, m.DisplayName)
		}
		return "", "", "", &MCPToolError{Message: "ambiguous target: " + strings.Join(names, ", ") + " (pass target_display_name)"}
	}
	return matches[0].AgentID, matches[0].DisplayName, cardShortID(matches[0]), nil
}

func hasSkill(c agentsdk.AgentCard, want string) bool {
	var card struct {
		Skills []string `json:"skills"`
	}
	_ = json.Unmarshal(c.Card, &card)
	for _, s := range card.Skills {
		if s == want {
			return true
		}
	}
	return false
}

func cardShortID(c agentsdk.AgentCard) string {
	// Prefer the top-level ShortID added by Task 3.5.
	if c.ShortID != "" {
		return c.ShortID
	}
	// Fallback for older agentserver builds: look inside the opaque card payload.
	var card struct {
		ShortID string `json:"short_id"`
	}
	_ = json.Unmarshal(c.Card, &card)
	return card.ShortID
}

// =========================================================================
// list_agents
// =========================================================================

type listAgentsTool struct{ t *Tools }

func (l *listAgentsTool) Name() string        { return "list_agents" }
func (l *listAgentsTool) Description() string { return "List agents in the workspace (driver-self filtered out)." }
func (l *listAgentsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (l *listAgentsTool) Call(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	cards, err := l.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	type out struct {
		AgentID     string          `json:"agent_id"`
		DisplayName string          `json:"display_name"`
		ShortID     string          `json:"short_id,omitempty"`
		Skills      []string        `json:"skills"`
		Tools       []string        `json:"tools"`
		Resources   json.RawMessage `json:"resources,omitempty"`
		Description string          `json:"description,omitempty"`
	}
	results := []out{}
	for _, c := range cards {
		if c.AgentID == l.t.cfg.Credentials.SandboxID {
			continue
		}
		var card struct {
			Skills    []string        `json:"skills"`
			Tools     []string        `json:"tools"`
			Resources json.RawMessage `json:"resources"`
			ShortID   string          `json:"short_id"`
		}
		_ = json.Unmarshal(c.Card, &card)
		results = append(results, out{
			AgentID: c.AgentID, DisplayName: c.DisplayName, ShortID: card.ShortID,
			Skills: card.Skills, Tools: card.Tools, Resources: card.Resources,
			Description: c.Description,
		})
	}
	return json.Marshal(map[string]interface{}{"agents": results})
}

// =========================================================================
// submit_task
// =========================================================================

type submitTaskTool struct{ t *Tools }

func (s *submitTaskTool) Name() string        { return "submit_task" }
func (s *submitTaskTool) Description() string { return "Submit a task to a workspace agent. read_paths and write_paths are local file/dir paths the user mentioned." }
func (s *submitTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
        "type":"object",
        "properties":{
            "prompt":{"type":"string"},
            "read_paths":{"type":"array","items":{"type":"string"}},
            "write_paths":{"type":"array","items":{"type":"object","properties":{
                "path":{"type":"string"},"overwrite":{"type":"boolean"}
            },"required":["path"]}},
            "target_display_name":{"type":"string"},
            "skill":{"type":"string"},
            "timeout_sec":{"type":"integer"}
        },
        "required":["prompt"]
    }`)
}
func (s *submitTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Prompt            string `json:"prompt"`
		ReadPaths         []string `json:"read_paths"`
		WritePaths        []struct {
			Path      string `json:"path"`
			Overwrite bool   `json:"overwrite"`
		} `json:"write_paths"`
		TargetDisplayName string `json:"target_display_name"`
		Skill             string `json:"skill"`
		TimeoutSec        int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Prompt == "" {
		return nil, &MCPToolError{Message: "prompt is required"}
	}

	manifest := Manifest{}
	for _, p := range args.ReadPaths {
		absP, err := filepath.Abs(p)
		if err != nil {
			return nil, &MCPToolError{Message: "invalid path " + p + ": " + err.Error()}
		}
		info, err := os.Lstat(absP)
		if err != nil {
			return nil, &MCPToolError{Message: "stat " + absP + ": " + err.Error()}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, &MCPToolError{Message: "symlinks not allowed: " + absP}
		}
		if info.IsDir() {
			tok := s.t.reg.RegisterDir(absP)
			s.t.audit.Log(AuditEvent{Event: "register_read_dir", Path: absP})
			manifest.Files = append(manifest.Files, FileEntry{
				Path: absP, Kind: "dir",
				ListURL: s.t.peerProxyURL("/files/dir/" + tok + "?recursive=true"),
				BlobURL: s.t.peerProxyURL("/files/dir/" + tok + "/blob"),
			})
		} else {
			sha, size, mt, err := s.t.reg.RegisterFile(absP)
			if err != nil {
				return nil, &MCPToolError{Message: err.Error()}
			}
			s.t.audit.Log(AuditEvent{Event: "register_read", Path: absP, SHA256: sha, Bytes: size})
			manifest.Files = append(manifest.Files, FileEntry{
				Path: absP, Kind: "file", Bytes: size, MIME: mt, SHA256: sha,
				URL: s.t.peerProxyURL("/files/blob/" + sha),
			})
		}
	}

	var writeTokens []string
	for _, w := range args.WritePaths {
		absP, err := filepath.Abs(w.Path)
		if err != nil {
			return nil, &MCPToolError{Message: "invalid write path: " + err.Error()}
		}
		if err := AssertWritableTarget(absP, s.t.cfg.DriverDefaults.DisableUIDCheck); err != nil {
			return nil, &MCPToolError{Message: err.Error()}
		}
		tok := s.t.reg.RegisterWrite(absP, w.Overwrite, "") // task_id assigned post-delegate
		writeTokens = append(writeTokens, tok)
		s.t.audit.Log(AuditEvent{Event: "register_write", Path: absP, Overwrite: w.Overwrite})
		manifest.Writes = append(manifest.Writes, WriteRequestEntry{
			Path: absP, Kind: "file", Overwrite: w.Overwrite,
			PutURL: s.t.peerProxyURL("/files/put/" + tok),
		})
	}

	targetID, targetName, _, err := s.t.resolveTarget(ctx, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}

	skill := args.Skill
	if skill == "" {
		skill = "fanout"
	}
	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}

	finalPrompt := manifest.Encode() + "\n\n" + args.Prompt
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: targetID, Skill: skill,
		Prompt: finalPrompt, TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}

	// Re-issue write tokens with the actual task_id, so RecordWritten finds them.
	// The entries minted above had task_id=""; patch them now via the registry.
	for _, tok := range writeTokens {
		s.t.reg.RebindWriteTokenTaskID(tok, resp.TaskID)
	}
	s.t.reg.TrackTask(resp.TaskID, writeTokens)

	return json.Marshal(map[string]interface{}{
		"task_id":               resp.TaskID,
		"target_id":             targetID,
		"target_display_name":   targetName,
		"manifest":              manifest,
	})
}

// =========================================================================
// get_task
// =========================================================================

type getTaskTool struct{ t *Tools }

func (g *getTaskTool) Name() string        { return "get_task" }
func (g *getTaskTool) Description() string { return "Get current status/output of a delegated task." }
func (g *getTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "task_id":{"type":"string"},
        "include_subtasks":{"type":"boolean"}
    },"required":["task_id"]}`)
}
func (g *getTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID          string `json:"task_id"`
		IncludeSubtasks bool   `json:"include_subtasks"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	info, err := g.t.sdk.GetTask(ctx, args.TaskID, true)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return json.Marshal(map[string]interface{}{
		"status":         info.Status,
		"output":         info.Output,
		"failure_reason": info.FailureReason,
	})
}

// =========================================================================
// wait_task
// =========================================================================

type waitTaskTool struct{ t *Tools }

func (w *waitTaskTool) Name() string        { return "wait_task" }
func (w *waitTaskTool) Description() string { return "Block until a delegated task reaches a terminal status; returns written_files for any PUT-back files." }
func (w *waitTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "task_id":{"type":"string"},
        "poll_interval_sec":{"type":"integer"},
        "timeout_sec":{"type":"integer"}
    },"required":["task_id"]}`)
}
func (w *waitTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID         string `json:"task_id"`
		PollIntervalSec int   `json:"poll_interval_sec"`
		TimeoutSec     int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	if args.PollIntervalSec == 0 {
		args.PollIntervalSec = 3
	}
	if args.TimeoutSec == 0 {
		args.TimeoutSec = w.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	deadline := time.Now().Add(time.Duration(args.TimeoutSec) * time.Second)
	for {
		info, err := w.t.sdk.GetTask(ctx, args.TaskID, true)
		if err != nil {
			return nil, &MCPToolError{Message: err.Error()}
		}
		switch info.Status {
		case "completed", "failed", "cancelled":
			written := w.t.reg.WrittenFiles(args.TaskID)
			w.t.reg.ForgetTask(args.TaskID)
			return json.Marshal(map[string]interface{}{
				"status":         info.Status,
				"output":         info.Output,
				"failure_reason": info.FailureReason,
				"written_files":  written,
			})
		}
		if time.Now().After(deadline) {
			return nil, &MCPToolError{Message: "wait_task timeout after " + fmt.Sprintf("%d", args.TimeoutSec) + "s; status=" + info.Status}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(args.PollIntervalSec) * time.Second):
		}
	}
}

// =========================================================================
// tail_subtasks
// =========================================================================

type tailSubtasksTool struct{ t *Tools }

func (ts *tailSubtasksTool) Name() string        { return "tail_subtasks" }
func (ts *tailSubtasksTool) Description() string { return "Long-poll subtask events for a delegated task; returns events newer than since_seq." }
func (ts *tailSubtasksTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "task_id":{"type":"string"},
        "since_seq":{"type":"integer"},
        "max_wait_sec":{"type":"integer"}
    },"required":["task_id"]}`)
}
func (ts *tailSubtasksTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TaskID     string `json:"task_id"`
		SinceSeq   int    `json:"since_seq"`
		MaxWaitSec int    `json:"max_wait_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	if args.MaxWaitSec == 0 {
		args.MaxWaitSec = 30
	}
	if args.MaxWaitSec > 60 {
		args.MaxWaitSec = 60
	}
	// Resolve master's short_id by discovering agents.
	cards, err := ts.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	masterShort := ""
	for _, c := range cards {
		if hasSkill(c, "fanout") {
			masterShort = cardShortID(c)
			break
		}
	}
	if masterShort == "" {
		return nil, &MCPToolError{Message: "no fanout-skilled agent visible"}
	}

	deadline := time.Now().Add(time.Duration(args.MaxWaitSec) * time.Second)
	for {
		path := "/tasks/" + args.TaskID + "/children"
		resp, err := ts.t.sdk.PeerProxy(ctx, "GET", masterShort, path, nil)
		if err != nil {
			return nil, &MCPToolError{Message: "peer-proxy: " + err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Master returns []SubTaskRow; we coerce into events with synthetic seq = index.
		var rows []map[string]interface{}
		_ = json.Unmarshal(body, &rows)
		events := []map[string]interface{}{}
		for i, r := range rows {
			if i < args.SinceSeq {
				continue
			}
			events = append(events, map[string]interface{}{
				"seq":        i,
				"node_id":    r["node_id"],
				"target_id":  r["target_id"],
				"status":     r["status"],
				"created_at": r["created_at"],
			})
		}
		if len(events) > 0 || time.Now().After(deadline) {
			return json.Marshal(map[string]interface{}{
				"cursor": len(rows),
				"events": events,
			})
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// =========================================================================
// cancel_task — v1 stub
// =========================================================================

type cancelTaskTool struct{ t *Tools }

func (c *cancelTaskTool) Name() string        { return "cancel_task" }
func (c *cancelTaskTool) Description() string { return "Cancel a delegated task. v1 stub: returns current status; SDK does not yet expose proxy_token cancel." }
func (c *cancelTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`)
}
func (c *cancelTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct{ TaskID string `json:"task_id"` }
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	info, err := c.t.sdk.GetTask(ctx, args.TaskID, false)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return json.Marshal(map[string]interface{}{
		"ok":     false,
		"status": info.Status,
		"note":   "cancel_task is not implemented in v1; query get_task and wait for natural completion or timeout",
	})
}
```

- [ ] **Step 4: Add the missing `RebindWriteTokenTaskID` helper to `registry.go`**

The implementation above calls `s.t.reg.RebindWriteTokenTaskID(tok, taskID)` to attach a `task_id` to a write token after `submit_task` learns the task_id from `DelegateTask`. Append to `multi-agent/internal/driver/registry.go`:

```go
// RebindWriteTokenTaskID updates the TaskID on an existing (un-consumed)
// write token. Used by submit_task after DelegateTask returns.
func (r *FileRegistry) RebindWriteTokenTaskID(tok, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.writes[tok]
	if !ok {
		return
	}
	e.TaskID = taskID
	r.writes[tok] = e
}
```

Add a regression test in `registry_test.go`:

```go
func TestRegistry_RebindWriteTokenTaskID(t *testing.T) {
	r := NewFileRegistry(50000)
	tok := r.RegisterWrite("/p", true, "")
	r.RebindWriteTokenTaskID(tok, "task-x")
	got, ok := r.ConsumeWriteToken(tok)
	if !ok || got.TaskID != "task-x" {
		t.Errorf("rebind: ok=%v got=%+v", ok, got)
	}
}
```

- [ ] **Step 5: Run all driver tests**

Run: `cd multi-agent && go test ./internal/driver/ -v`
Expected: PASS — every test in the package green (the new tool tests, the registry rebind test, and all earlier subtests).

- [ ] **Step 6: Commit**

```bash
cd multi-agent
git add internal/driver/tools.go internal/driver/tools_test.go internal/driver/registry.go internal/driver/registry_test.go
git commit -m "feat(driver): six MCP tools (list_agents, submit_task, get_task, wait_task, tail_subtasks, cancel_task)"
```

---

**End of Phase D.** All tools are wired through the MCP dispatcher and unit-tested with a fake SDK. Phase E adds the binary entrypoint and the planner prompt paragraph.

---

## Phase E — driver binary + planner prompt paragraph (Tasks 13–14)

### Task 13: `cmd/driver-agent/main.go` — `register` and `serve-mcp` subcommands

**Files:**
- Create: `multi-agent/cmd/driver-agent/main.go`
- Create: `multi-agent/cmd/driver-agent/config.example.yaml`
- Create: `multi-agent/cmd/driver-agent/README.md`

**Context:** The binary has two subcommands. `register` does the agentserver device flow once (interactive — prints a verification URL on stderr) and saves the resulting credentials back into the yaml. `serve-mcp` is what Claude Code spawns: it loads config, ensures the tunnel is up, publishes the discovery card, mounts the webui, then runs the MCP stdio loop on stdin/stdout. When stdin closes (Claude Code exit), the tunnel is closed and the process exits 0.

No unit tests for `main.go` directly — the e2e harness in Task 17 exercises it end-to-end.

- [ ] **Step 1: Write the binary**

```go
// multi-agent/cmd/driver-agent/main.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/internal/webui"
)

const usage = `driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register   --config /path/to/driver.yaml
  driver-agent serve-mcp  --config /path/to/driver.yaml
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "register":
		runRegister(os.Args[2:])
	case "serve-mcp":
		runServe(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func runRegister(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to driver.yaml")
	fs.Parse(args)
	if *cfgPath == "" {
		die("--config required")
	}
	cfg, err := driver.LoadConfig(*cfgPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if cfg.Credentials.ProxyToken != "" {
		fmt.Fprintln(os.Stderr, "already registered (short_id="+cfg.Credentials.ShortID+"); nothing to do")
		return
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: cfg.Server.URL, Name: cfg.Server.Name})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dc, err := agentsdk.RequestDeviceCode(ctx, cfg.Server.URL)
	if err != nil {
		die("device code: " + err.Error())
	}
	fmt.Fprintf(os.Stderr, "Open this URL to register %q:\n  %s\n", cfg.Server.Name, dc.VerificationURIComplete)
	tok, err := agentsdk.PollForToken(ctx, cfg.Server.URL, dc)
	if err != nil {
		die("poll token: " + err.Error())
	}
	reg, err := cli.Register(ctx, tok.AccessToken)
	if err != nil {
		die("register: " + err.Error())
	}
	cfg.Credentials.SandboxID = reg.SandboxID
	cfg.Credentials.TunnelToken = reg.TunnelToken
	cfg.Credentials.ProxyToken = reg.ProxyToken
	cfg.Credentials.WorkspaceID = reg.WorkspaceID
	cfg.Credentials.ShortID = reg.ShortID
	if err := driver.SaveConfig(*cfgPath, cfg); err != nil {
		die("save config: " + err.Error())
	}
	fmt.Fprintln(os.Stderr, "registered as", cfg.Credentials.ShortID)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve-mcp", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to driver.yaml")
	fs.Parse(args)
	if *cfgPath == "" {
		die("--config required")
	}
	cfg, err := driver.LoadConfig(*cfgPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if cfg.Credentials.ProxyToken == "" {
		die("not registered; run `driver-agent register --config " + *cfgPath + "` first")
	}

	auditPath, err := resolveAuditPath(cfg)
	if err != nil {
		die("audit path: " + err.Error())
	}
	audit, err := driver.NewAuditLog(auditPath)
	if err != nil {
		die("audit log: " + err.Error())
	}
	defer audit.Close()
	reg := driver.NewFileRegistry(cfg.DriverDefaults.MaxDirCacheEntries)

	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: cfg.Server.URL, Name: cfg.Server.Name})
	cli.SetRegistration(&agentsdk.Registration{
		SandboxID:   cfg.Credentials.SandboxID,
		TunnelToken: cfg.Credentials.TunnelToken,
		ProxyToken:  cfg.Credentials.ProxyToken,
		WorkspaceID: cfg.Credentials.WorkspaceID,
		ShortID:     cfg.Credentials.ShortID,
	})

	files := driver.NewFilesHandler(reg, audit)
	base := http.NewServeMux()
	base.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"agent":"driver"}`))
	})
	composed := webui.SetDriverFiles(base, files)

	if err := publishCard(cfg); err != nil {
		die("publish card: " + err.Error())
	}

	tools := driver.NewTools(reg, audit, cli, cfg)
	mcp := driver.NewMCPServer(tools.All())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Tunnel goroutine — blocks on Connect, returns on disconnect / ctx cancel.
	connDone := make(chan error, 1)
	go func() {
		connDone <- cli.Connect(ctx, agentsdk.Handlers{
			HTTP: composed,
			OnConnect:    func() { fmt.Fprintln(os.Stderr, "driver: tunnel connected") },
			OnDisconnect: func(err error) { fmt.Fprintf(os.Stderr, "driver: tunnel disconnected: %v\n", err) },
		})
	}()

	// MCP loop blocks on stdin. When Claude Code closes stdin → returns nil → we exit.
	if err := mcp.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcp serve:", err)
	}
	cancel()
	<-connDone
}

func resolveAuditPath(cfg *driver.Config) (string, error) {
	dir := cfg.DriverDefaults.AuditLogDir
	if dir == "" {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(u.HomeDir, ".cache", "multi-agent", cfg.Credentials.ShortID)
	}
	return filepath.Join(dir, "audit.log"), nil
}

func publishCard(cfg *driver.Config) error {
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": cfg.Discovery.DisplayName,
		"description":  cfg.Discovery.Description,
		"agent_type":   "driver",
		"card": map[string]interface{}{
			"skills":        cfg.Discovery.Skills,
			"accepts_tasks": false,
			"has_web_ui":    false,
			"version":       "0.1.0",
		},
	})
	url := strings.TrimRight(cfg.Server.URL, "/") + "/api/agent/discovery/cards"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Credentials.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("publish card status %d", resp.StatusCode)
	}
	return nil
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver-agent:", msg)
	os.Exit(1)
}
```

- [ ] **Step 2: Write the example config**

```yaml
# multi-agent/cmd/driver-agent/config.example.yaml
server:
  url: https://agent.example.com
  name: driver-yuzishu

# Filled by `driver-agent register`. Leave blank for first run.
credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

discovery:
  display_name: driver-yuzishu
  description: "Local driver agent. Bridges Claude Code to the workspace; serves the user's local files."
  skills: []  # driver does not accept tasks

listen_addr: "127.0.0.1:0"

driver_defaults:
  target_display_name: ""        # blank = auto-pick the unique fanout-skilled agent
  task_timeout_sec: 600
  audit_log_dir: ""              # blank = ~/.cache/multi-agent/<short_id>/
  disable_uid_check: false
  max_dir_cache_entries: 50000
```

- [ ] **Step 3: Write the README**

```markdown
# driver-agent

Bridges Claude Code (CLI or VS Code extension) to a multi-agent workspace. Runs as a stdio MCP server that Claude Code spawns; in the same process, holds an agentserver tunnel as a regular workspace agent so master and slaves can fetch files the user mentions in Claude.

## First-time setup

1. Copy `config.example.yaml` to `~/.config/multi-agent/driver.yaml` and edit `server.url` + `discovery.display_name` for your deployment.
2. Run the device-flow registration once:

   ```
   driver-agent register --config ~/.config/multi-agent/driver.yaml
   ```

   Open the printed URL in a browser, complete the consent prompt; credentials are written back to the yaml file.

3. Tell Claude Code about the driver. In `~/.claude.json` (CLI) or the equivalent VS Code extension settings, add an MCP server entry:

   ```json
   {
     "mcpServers": {
       "driver": {
         "command": "/usr/local/bin/driver-agent",
         "args": ["serve-mcp", "--config", "/home/me/.config/multi-agent/driver.yaml"]
       }
     }
   }
   ```

4. Restart Claude Code. The driver's six tools (`list_agents`, `submit_task`, `get_task`, `wait_task`, `tail_subtasks`, `cancel_task`) appear under the `driver/` namespace.

## Tools

See `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` § 1 for full schemas.

## Audit log

Every file registration, fetch, and PUT is recorded as JSONL at `~/.cache/multi-agent/<short_id>/audit.log` (overridable via `driver_defaults.audit_log_dir`). Lines look like:

```json
{"ts":"...","event":"register_read","path":"/home/me/x.csv","sha256":"abc","bytes":12345}
{"ts":"...","event":"fetch_blob","path":"/home/me/x.csv","sha256":"abc","bytes":12345,"task_id":"t-9","peer_short_id":"slave-1"}
{"ts":"...","event":"put_blob","path":"/home/me/out.parquet","sha256":"def","bytes":54321,"task_id":"t-9","peer_short_id":"slave-2","overwrite":true}
```
```

- [ ] **Step 4: Verify build**

Run: `cd multi-agent && go build ./cmd/driver-agent/`
Expected: succeeds; produces `./driver-agent` binary.

- [ ] **Step 5: Verify the binary prints usage with no args**

Run: `cd multi-agent && ./driver-agent`
Expected: usage text on stderr, exit code 2. Run `./driver-agent --help` to confirm the help path.

- [ ] **Step 6: Commit**

```bash
cd multi-agent
git add cmd/driver-agent/main.go cmd/driver-agent/config.example.yaml cmd/driver-agent/README.md
git commit -m "feat(cmd/driver-agent): register + serve-mcp subcommands"
```

---

### Task 14: planner prompt — manifest paragraph

**Files:**
- Modify: `multi-agent/internal/planner/prompts.go` (append a paragraph to `planPrompt` after the existing build_mcp instructions, around line 60–62)
- Modify: `multi-agent/internal/planner/prompts_test.go` (add a test asserting the rendered prompt contains the manifest instructions; if the file does not exist, create it)

**Context:** Spec § 4. One additive paragraph; no other planner changes. The prompt is a `fmt.Sprintf` literal in `planPrompt`; we add the paragraph just before the closing `Output ONLY the JSON array, no commentary.` line so the manifest rule sits with the other DAG-construction rules.

- [ ] **Step 1: Write the failing test**

```go
// multi-agent/internal/planner/prompts_test.go
package planner

import (
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestPlanPrompt_IncludesManifestParagraph(t *testing.T) {
	out := planPrompt("do a thing", []agentsdk.AgentCard{})
	wantPhrases := []string{
		"USER_FILES_MANIFEST",
		`"files"`,
		`"writes"`,
		"PUT",
	}
	for _, w := range wantPhrases {
		if !strings.Contains(out, w) {
			t.Errorf("planPrompt missing %q\n----\n%s", w, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/planner/ -run TestPlanPrompt_IncludesManifestParagraph -v`
Expected: FAIL — phrases not present.

- [ ] **Step 3: Edit `planPrompt` to insert the paragraph**

In `multi-agent/internal/planner/prompts.go`, find the line:

```go
Output ONLY the JSON array, no commentary.
```

Replace it with:

```go
The user's request may begin with a <USER_FILES_MANIFEST version=1> block
followed by a JSON object. The "files" array names files the user has
referenced; each entry has either a "url" (for a file) or "list_url" +
"blob_url" (for a directory). When you assign work to a slave that needs
to read a referenced file, include the relevant url in that node's prompt
so the slave can GET it. The "writes" array lists local paths the user
wants results written to; when a slave produces a result that should land
at one of those paths, include the matching "put_url" in the slave's
prompt and instruct the slave to PUT the resulting bytes to that URL
(the URL accepts a single PUT and returns 200 on success). The block
itself is metadata; do not echo it back, and do not invent additional
fields.

Output ONLY the JSON array, no commentary.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/planner/ -v`
Expected: PASS — the new test plus all existing planner tests still green.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add internal/planner/prompts.go internal/planner/prompts_test.go
git commit -m "feat(planner): teach planner about USER_FILES_MANIFEST block"
```

---

**End of Phase E.** The driver compiles, registers, runs, and the planner knows how to route file URLs into slave prompts. Phase F builds the e2e harness that proves the loop end-to-end.

---

## Phase F — e2e demo (Tasks 15–17)

The e2e exists for two reasons: it proves the file plumbing works against real network sockets and a real agentserver, and it documents how a third-party Claude-Code-style client can speak to the driver. The "fileconcat" slave is intentionally minimal — it knows about manifests but nothing about Claude / LLMs.

### Task 15: `agent-fileconcat` (a tiny manifest-aware slave)

**Files:**
- Create: `multi-agent/examples/generic-driver/agent-fileconcat/main.go`
- Create: `multi-agent/examples/generic-driver/agent-fileconcat/config.example.yaml`

**Context:** A delegation target for `submit_task` in the e2e. Receives a prompt that begins with `<USER_FILES_MANIFEST>`, fetches every file URL via the peer-proxy (just an authenticated `http.Get`), concatenates contents joined by a single space, PUTs to the first write URL, returns a summary line. Uses the existing `image-pipeline/internal/agentboot` package for boot/registration to avoid re-implementing device flow. Advertises `skills: ["fanout"]` so the driver's auto-pick logic finds it.

- [ ] **Step 1: Write the slave**

```go
// multi-agent/examples/generic-driver/agent-fileconcat/main.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
)

type manifest struct {
	Files []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
		URL  string `json:"url"`
	} `json:"files"`
	Writes []struct {
		Path   string `json:"path"`
		PutURL string `json:"put_url"`
	} `json:"writes"`
}

func main() {
	cfgPath := flag.String("config", "", "path to config.yaml")
	flag.Parse()
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "--config required")
		os.Exit(2)
	}
	if err := agentboot.Run(context.Background(), *cfgPath, runTask); err != nil {
		fmt.Fprintln(os.Stderr, "fileconcat:", err)
		os.Exit(1)
	}
}

// runTask is the agentsdk.TaskHandler.
// proxyToken is read from the env (agentboot.Run plumbs it via the SDK; we
// also keep it in a global for the http calls below).
var proxyTokenEnv = os.Getenv("FILECONCAT_PROXY_TOKEN") // overridden below from cfg

func runTask(ctx context.Context, task *agentsdk.Task) error {
	m, body, err := parseManifest(task.Prompt)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("no files in manifest")
	}
	if len(m.Writes) == 0 {
		return fmt.Errorf("no write target in manifest")
	}
	pieces := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		if f.Kind != "file" {
			continue
		}
		b, err := authedGet(ctx, f.URL)
		if err != nil {
			return fmt.Errorf("get %s: %w", f.Path, err)
		}
		pieces = append(pieces, string(b))
	}
	out := []byte(strings.Join(pieces, " "))
	if err := authedPut(ctx, m.Writes[0].PutURL, out); err != nil {
		return fmt.Errorf("put %s: %w", m.Writes[0].Path, err)
	}
	_ = body // body (the user's prompt without the manifest) is unused here
	fmt.Fprintf(os.Stderr, "fileconcat: wrote %d bytes to %s\n", len(out), m.Writes[0].Path)
	return nil
}

func parseManifest(prompt string) (manifest, string, error) {
	const open = "<USER_FILES_MANIFEST"
	const close = "</USER_FILES_MANIFEST>"
	openIdx := strings.Index(prompt, open)
	if openIdx < 0 {
		return manifest{}, prompt, fmt.Errorf("no manifest")
	}
	endLine := strings.Index(prompt[openIdx:], "\n")
	if endLine < 0 {
		return manifest{}, prompt, fmt.Errorf("malformed open fence")
	}
	jsonStart := openIdx + endLine + 1
	closeIdx := strings.Index(prompt[jsonStart:], "\n"+close)
	if closeIdx < 0 {
		return manifest{}, prompt, fmt.Errorf("no close fence")
	}
	jsonLine := prompt[jsonStart : jsonStart+closeIdx]
	var m manifest
	if err := json.Unmarshal([]byte(jsonLine), &m); err != nil {
		return manifest{}, prompt, fmt.Errorf("unmarshal manifest: %w", err)
	}
	bodyStart := jsonStart + closeIdx + len("\n"+close)
	body := strings.TrimLeft(prompt[bodyStart:], "\n")
	return m, body, nil
}

func authedGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+proxyTokenEnv)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return io.ReadAll(resp.Body)
}

func authedPut(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+proxyTokenEnv)
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return nil
}
```

> **Note on `proxyTokenEnv`:** The `agentboot.Run` indirection does not currently expose the live proxy token to the task handler. The pragmatic fix for v1 e2e: the wrapper `scripts/e2e.sh` (Task 17) `export`s `FILECONCAT_PROXY_TOKEN` from the saved config before launching this binary. A future cleanup is to thread the token through `agentboot.Handlers` (one-line addition there); not in scope for this plan.

- [ ] **Step 2: Write the example config**

```yaml
# multi-agent/examples/generic-driver/agent-fileconcat/config.example.yaml
server:
  url: https://agent.example.com
  name: fileconcat-1
credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""
discovery:
  display_name: fileconcat-1
  description: "Concatenates files referenced in a USER_FILES_MANIFEST."
  skills: ["fanout"]
listen_addr: "127.0.0.1:0"
```

- [ ] **Step 3: Verify build**

Run: `cd multi-agent && go build ./examples/generic-driver/agent-fileconcat/`
Expected: succeeds.

- [ ] **Step 4: Commit**

```bash
cd multi-agent
git add examples/generic-driver/agent-fileconcat/
git commit -m "feat(examples/generic-driver): agent-fileconcat slave (manifest-aware)"
```

---

### Task 16: e2e harness (`examples/generic-driver/e2e/main.go`)

**Files:**
- Create: `multi-agent/examples/generic-driver/e2e/main.go`

**Context:** A Go program that acts as a fake Claude Code: spawns `driver-agent serve-mcp` as a subprocess, speaks JSON-RPC over its stdin/stdout, runs the spec § "E2E demo" scenario, and asserts. It is invoked both as a binary (`scripts/e2e.sh` runs the full demo) and as a `TestSmoke` `go test` target (only `tools/list`, no master/slave required).

- [ ] **Step 1: Write the e2e harness**

```go
// multi-agent/examples/generic-driver/e2e/main.go
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type rpcClient struct {
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

func newRPC(stdin io.WriteCloser, stdout io.Reader) *rpcClient {
	return &rpcClient{stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64*1024)}
}

func (c *rpcClient) call(method string, params interface{}) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	req := map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": method,
	}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // skip non-JSON or stderr leakage
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// callTool unwraps the {content:[{type:text,text:JSON}]} envelope to return
// the inner JSON of a tools/call response.
func (c *rpcClient) callTool(name string, args interface{}) (json.RawMessage, error) {
	res, err := c.call("tools/call", map[string]interface{}{
		"name": name, "arguments": args,
	})
	if err != nil {
		return nil, err
	}
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &env); err != nil {
		return nil, err
	}
	if len(env.Content) == 0 {
		return nil, fmt.Errorf("empty content")
	}
	return json.RawMessage(env.Content[0].Text), nil
}

func main() {
	bin := flag.String("driver-bin", "./driver-agent", "path to built driver-agent binary")
	cfg := flag.String("driver-config", "", "path to driver yaml")
	mode := flag.String("mode", "full", "smoke|full")
	flag.Parse()

	if *cfg == "" {
		die("--driver-config required")
	}

	cmd := exec.Command(*bin, "serve-mcp", "--config", *cfg)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		die("start driver: " + err.Error())
	}
	defer func() { _ = cmd.Process.Kill() }()

	rpc := newRPC(stdin, stdout)

	if _, err := rpc.call("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "e2e", "version": "0"},
		"capabilities":    map[string]interface{}{},
	}); err != nil {
		die("initialize: " + err.Error())
	}

	listed, err := rpc.call("tools/list", nil)
	if err != nil {
		die("tools/list: " + err.Error())
	}
	wantTools := []string{"list_agents", "submit_task", "get_task", "wait_task", "tail_subtasks", "cancel_task"}
	for _, w := range wantTools {
		if !strings.Contains(string(listed), `"name":"`+w+`"`) {
			die("tools/list missing " + w + ": " + string(listed))
		}
	}
	fmt.Println("ok: tools/list returned all six tools")

	if *mode == "smoke" {
		fmt.Println("smoke: PASS")
		return
	}

	// Full e2e: requires master + agent-fileconcat to be running.
	tmp, _ := os.MkdirTemp("", "genericdriver-e2e-")
	defer os.RemoveAll(tmp)
	in1 := filepath.Join(tmp, "in1.txt")
	in2 := filepath.Join(tmp, "in2.txt")
	out := filepath.Join(tmp, "out.txt")
	os.WriteFile(in1, []byte("hello"), 0o644)
	os.WriteFile(in2, []byte("world"), 0o644)

	subRes, err := rpc.callTool("submit_task", map[string]interface{}{
		"prompt":      "concatenate inputs and write to out.txt",
		"read_paths":  []string{in1, in2},
		"write_paths": []map[string]interface{}{{"path": out, "overwrite": true}},
	})
	if err != nil {
		die("submit_task: " + err.Error())
	}
	var sub struct {
		TaskID   string `json:"task_id"`
		Manifest struct {
			Files  []map[string]interface{} `json:"files"`
			Writes []map[string]interface{} `json:"writes"`
		} `json:"manifest"`
	}
	json.Unmarshal(subRes, &sub)
	if sub.TaskID == "" {
		die("no task_id: " + string(subRes))
	}
	if len(sub.Manifest.Files) != 2 || len(sub.Manifest.Writes) != 1 {
		die(fmt.Sprintf("manifest shape: %d files / %d writes", len(sub.Manifest.Files), len(sub.Manifest.Writes)))
	}
	fmt.Println("ok: submit_task returned", sub.TaskID)

	waitRes, err := rpc.callTool("wait_task", map[string]interface{}{
		"task_id": sub.TaskID, "poll_interval_sec": 2, "timeout_sec": 60,
	})
	if err != nil {
		die("wait_task: " + err.Error())
	}
	var w struct {
		Status       string `json:"status"`
		WrittenFiles []struct {
			Path string `json:"path"`
			Bytes int64 `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"written_files"`
	}
	json.Unmarshal(waitRes, &w)
	if w.Status != "completed" {
		die("status: " + w.Status + " (full: " + string(waitRes) + ")")
	}
	if len(w.WrittenFiles) != 1 || w.WrittenFiles[0].Bytes != 11 {
		die(fmt.Sprintf("written_files: %+v", w.WrittenFiles))
	}
	got, _ := os.ReadFile(out)
	if string(got) != "hello world" {
		die("local file content: " + string(got))
	}
	fmt.Println("ok: out.txt =", strings.TrimSpace(string(got)))

	// Audit log assertions.
	auditPath := filepath.Join(os.Getenv("HOME"), ".cache", "multi-agent",
		readShortIDFromConfig(*cfg), "audit.log")
	if alt := os.Getenv("DRIVER_AUDIT_PATH"); alt != "" {
		auditPath = alt
	}
	auditBytes, err := os.ReadFile(auditPath)
	if err != nil {
		die("read audit: " + err.Error())
	}
	for _, want := range []string{"register_read", "register_write", "fetch_blob", "put_blob"} {
		if !bytes.Contains(auditBytes, []byte(want)) {
			die("audit log missing " + want)
		}
	}
	fmt.Println("ok: audit log records all four event types")

	fmt.Println("E2E PASS")
	_ = time.Now()
	_ = rand.Read
	_ = hex.EncodeToString
}

func readShortIDFromConfig(path string) string {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "short_id:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "short_id:"))
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "e2e FAIL:", msg)
	os.Exit(1)
}
```

- [ ] **Step 2: Verify build**

Run: `cd multi-agent && go build ./examples/generic-driver/e2e/`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
cd multi-agent
git add examples/generic-driver/e2e/main.go
git commit -m "feat(examples/generic-driver): e2e harness (fake Claude Code over stdio)"
```

---

### Task 17: `scripts/e2e.sh` + README + `TestSmoke`

**Files:**
- Create: `multi-agent/examples/generic-driver/scripts/e2e.sh`
- Create: `multi-agent/examples/generic-driver/README.md`
- Create: `multi-agent/examples/generic-driver/e2e/smoke_test.go`

- [ ] **Step 1: Write the bash wrapper**

```bash
# multi-agent/examples/generic-driver/scripts/e2e.sh
#!/usr/bin/env bash
# E2E for the generic-driver demo. Requires:
#   AGENTSERVER_URL    - reachable agentserver including the new peer-proxy route
#   DRIVER_CONFIG      - path to a registered driver.yaml
#   MASTER_CONFIG      - path to a registered master-agent yaml
#   FILECONCAT_CONFIG  - path to a registered agent-fileconcat yaml
set -euo pipefail
cd "$(dirname "$0")/../../.."   # → multi-agent/

: "${AGENTSERVER_URL:?must be set}"
: "${DRIVER_CONFIG:?must be set}"
: "${MASTER_CONFIG:?must be set}"
: "${FILECONCAT_CONFIG:?must be set}"

echo "==> building binaries"
go build -o ./bin/driver-agent ./cmd/driver-agent
go build -o ./bin/master-agent ./cmd/master-agent
go build -o ./bin/agent-fileconcat ./examples/generic-driver/agent-fileconcat
go build -o ./bin/e2e ./examples/generic-driver/e2e

# Plumb the slave's proxy_token via env (see Task 15 note).
export FILECONCAT_PROXY_TOKEN
FILECONCAT_PROXY_TOKEN="$(awk '/proxy_token:/{print $2}' "$FILECONCAT_CONFIG" | tr -d '"')"
[ -n "$FILECONCAT_PROXY_TOKEN" ] || { echo "fileconcat proxy_token missing in $FILECONCAT_CONFIG"; exit 1; }

cleanup() {
  echo "==> stopping background processes"
  jobs -p | xargs -r kill 2>/dev/null || true
}
trap cleanup EXIT

echo "==> starting master"
./bin/master-agent --config "$MASTER_CONFIG" >/tmp/genericdriver-master.log 2>&1 &
MASTER_PID=$!

echo "==> starting fileconcat slave"
./bin/agent-fileconcat --config "$FILECONCAT_CONFIG" >/tmp/genericdriver-fileconcat.log 2>&1 &

# Give the agents a few seconds to register and publish cards.
sleep 5

echo "==> running e2e"
./bin/e2e --driver-bin ./bin/driver-agent --driver-config "$DRIVER_CONFIG" --mode full
```

Mark executable:

```bash
chmod +x multi-agent/examples/generic-driver/scripts/e2e.sh
```

- [ ] **Step 2: Write the README**

```markdown
# generic-driver example

End-to-end demo of the `driver-agent` from `cmd/driver-agent`. Spawns master, a `fileconcat` slave, and the driver under a fake Claude Code; round-trips two files through the workspace via the agentserver peer-proxy.

## Prerequisites

- An `agentserver/` build that includes the peer-proxy route (Tasks 1–3 of the implementation plan).
- Three registered yaml configs: one for `driver-agent`, one for `master-agent`, one for `agent-fileconcat`. Each must be `register`ed once against the same agentserver (see each binary's README).

## Run

```
AGENTSERVER_URL=https://agent.example.com \
DRIVER_CONFIG=$HOME/.config/multi-agent/driver.yaml \
MASTER_CONFIG=$HOME/.config/multi-agent/master.yaml \
FILECONCAT_CONFIG=$HOME/.config/multi-agent/fileconcat.yaml \
./scripts/e2e.sh
```

## Expected output

```
==> building binaries
==> starting master
==> starting fileconcat slave
==> running e2e
ok: tools/list returned all six tools
ok: submit_task returned t-...
ok: out.txt = hello world
ok: audit log records all four event types
E2E PASS
```

## Smoke test (no agentserver required)

```
go test ./examples/generic-driver/e2e -run TestSmoke -v
```

This boots `driver-agent serve-mcp` against a local fixture config (no real workspace needed) and verifies the MCP handshake + `tools/list` round-trip. Useful for CI.
```

- [ ] **Step 3: Write `TestSmoke`**

```go
// multi-agent/examples/generic-driver/e2e/smoke_test.go
package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSmoke launches driver-agent against a deliberately-broken config
// (server.url unreachable) but only exercises tools/list. The driver's MCP
// loop runs even when the tunnel goroutine is failing, so tools/list returns
// before the test times out.
func TestSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke spawns a child process")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "driver.yaml")
	writeFixture(t, cfg)

	// Build driver-agent into the temp dir to avoid polluting the worktree.
	bin := filepath.Join(dir, "driver-agent")
	build := exec.Command("go", "build", "-o", bin,
		"github.com/yourorg/multi-agent/cmd/driver-agent")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// Run the e2e binary in smoke mode.
	exe := exec.Command("go", "run", ".",
		"--driver-bin", bin, "--driver-config", cfg, "--mode", "smoke")
	exe.Dir = filepath.Join(repoRoot(), "examples", "generic-driver", "e2e")
	out, err := exe.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke: %v\n%s", err, out)
	}
}

func writeFixture(t *testing.T, path string) {
	t.Helper()
	const yaml = `
server: {url: "http://127.0.0.1:1", name: "smoke-driver"}
credentials:
  sandbox_id: "sbx-smoke"
  tunnel_token: "ttok"
  proxy_token: "ptok"
  workspace_id: "ws-smoke"
  short_id: "smoke"
discovery:
  display_name: smoke-driver
  description: "smoke test"
  skills: []
listen_addr: "127.0.0.1:0"
driver_defaults:
  task_timeout_sec: 5
  audit_log_dir: "` + filepath.Dir(path) + `"
  disable_uid_check: true
  max_dir_cache_entries: 100
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repoRoot() string {
	_, here, _, _ := runtime.Caller(0)
	// here = .../multi-agent/examples/generic-driver/e2e/smoke_test.go
	return filepath.Join(filepath.Dir(here), "..", "..", "..")
}
```

> **Imports needed for smoke_test.go:** add `"os"` to the import block.

- [ ] **Step 4: Run the smoke test**

Run: `cd multi-agent && go test ./examples/generic-driver/e2e -run TestSmoke -v`
Expected: PASS — driver-agent builds, boots, responds to `initialize` + `tools/list`, exits when its child stdin closes.

- [ ] **Step 5: Commit**

```bash
cd multi-agent
git add examples/generic-driver/scripts/e2e.sh examples/generic-driver/README.md examples/generic-driver/e2e/smoke_test.go
git commit -m "feat(examples/generic-driver): scripts/e2e.sh + README + TestSmoke"
```

- [ ] **Step 6 (optional verification, requires deployed agentserver): run full e2e**

Set `AGENTSERVER_URL` / `DRIVER_CONFIG` / `MASTER_CONFIG` / `FILECONCAT_CONFIG` to registered configs against an agentserver build that includes Tasks 1–3, then:

```
./multi-agent/examples/generic-driver/scripts/e2e.sh
```

Expected final line: `E2E PASS`.

---

**End of Phase F.** All deliverables in the spec's "Deliverables checklist" are now landed except the optional `?since_seq=` master endpoint (which we replaced with client-side diffing — see the deviation noted at the top of this plan).

---

## Final verification

Before marking the plan complete, run these from the repo root:

```bash
cd multi-agent && go build ./... && go vet ./... && go test ./...
cd ../agentserver && go build ./... && go vet ./... && go test ./...
```

All three commands, both repos, must pass. The full e2e (`scripts/e2e.sh`) is run separately when an agentserver build with the peer-proxy route is deployed.

---

## Self-review notes

The following gaps are deliberate and called out so reviewers do not flag them as incomplete:

1. **`cancel_task` is a stub.** Spec § 1 lists it; v1 returns "not implemented". Making it real requires either adding a proxy_token-authed `/api/agent/tasks/{id}/cancel` route on agentserver or threading the user's OAuth token through the driver. Both are larger than the scope of this plan; the stub returns the current task status so the user can at least see where it stands.
2. **`tail_subtasks` does not have a server-side `?since_seq=` parameter.** Driver does client-side diffing on `/tasks/{id}/children` snapshots. The spec's deliverable for adding `?since_seq=` to `/api/sub_tasks` was removed because that endpoint does not exist (the actual endpoint is `/tasks/{id}/children` and adding a seq column to `store.SubTaskRow` is a non-trivial schema change). Documented at the top of this plan.
3. **`agentboot.Handlers` does not surface the live proxy token to the task handler.** `agent-fileconcat` reads it from `FILECONCAT_PROXY_TOKEN` env, set by `scripts/e2e.sh` from the saved yaml. A one-line agentboot change to expose the token through `Handlers` is the correct long-term fix; out of scope here.
4. **The peer-proxy handler tests rely on `db.OpenInMemory`, `db.InsertSandboxForTest`, `sbxstore.Store.LoadFromDB`, and `tunnel.Registry.RegisterRaw`.** If any of those do not exist, Task 2 Step 2 calls them out as separate small-commit additions before the handler test is wired up.
5. **`disable_uid_check: true` is required on macOS or any system whose `os.Lstat().Sys()` does not expose `*syscall.Stat_t` with an integer Uid.** The current implementation degrades gracefully (no uid check happens if the type assertion fails) but documenting `disable_uid_check: true` for non-Linux dev machines is a README task that fits in Task 13.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-09-generic-driver-agent.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task (Tasks 1–17), review between tasks, fast iteration. Best for this plan because the tasks are mostly independent within their phase, and each task is small enough to fit in a clean subagent context.

**2. Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints for review.

**Which approach?**
