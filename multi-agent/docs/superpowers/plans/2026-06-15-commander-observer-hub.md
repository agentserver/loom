# observer /commander(WS hub + reverse proxy + UI)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 observer 侧实现 commander-web-entry 的 PR-3:`/daemon-link` WebSocket hub(按 `(UserID, WorkspaceID)` 隔离的长连注册表)+ `/api/commander/*` reverse proxy(device-flow 登录 + cookie 鉴权 + 扇出/路由命令 + SSE)+ `/commander` 网页,让用户从浏览器指挥自己名下的长跑 daemon。

**Architecture:** 新包 `internal/commanderhub/`,observerweb 注入并挂路由。Hub 复用 PR-2 的 `internal/commander` 信封类型作 schema 单一来源;daemon(PR-2)零改动。observer 当 device client 走 agentserver device-code OAuth,颁 httpOnly cookie;`/api/commander/*` 接受 cookie 或 Bearer。e2e 直接复用 `commander.WSClient` 当「真 daemon」。

**Tech Stack:** Go stdlib + `github.com/gorilla/websocket`(已是依赖)+ `github.com/agentserver/agentserver/pkg/agentsdk`(device flow)+ 现有 `internal/identity`、`internal/observerweb`、`internal/commander`(PR-2)。

**Spec:** `docs/superpowers/specs/2026-06-15-commander-observer-hub-design.md`(已 commit)。本 plan 只写 HOW;WHY 与契约看 spec。

**Branch:** `commander-observer-hub`(已从 `worktree-driver-daemon` 分出)。Worktree:`/root/multi-agent/.claude/worktrees/driver-daemon/multi-agent/`。所有路径相对该 worktree 根。

**Master-path freeze:** 改动只限 `internal/commanderhub/`(新)、`internal/observerweb/server.go`、`cmd/observer-server/main.go`(`observerWebOptions` +1 字段)。验证:
```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**' 'internal/commander/**' 'cmd/driver-agent/**'
# expected: empty(commander/driver-agent 是 PR-2 冻结面,本 PR 不碰)
```

**Conventions(全 plan 适用):**
- 每个任务:TDD(先写失败测试 → 跑红 → 最小实现 → 跑绿 → commit)。
- 跑测统一 `cd /root/multi-agent/.claude/worktrees/driver-daemon/multi-agent && go test ./internal/commanderhub/ -count=1 -race`(逐 task 可加 `-run <Name>`)。
- commit message 用 `feat(commanderhub): ...` / `test(commanderhub): ...` / `docs(commanderhub): ...`,尾随 `Co-Authored-By: Claude <noreply@anthropic.com>`。
- 复用 PR-2 信封:`commander.Envelope`/`RegisterPayload`/`CommandPayload`/`EventPayload`/`ErrorPayload`/`SchemaVersion`/`ErrCode*`。observer **不**调 `marshalTurnResult` —— `command_result`/`error` 帧的 payload 原样转发到 SSE,只有 `event` 帧需解码 `EventPayload` 取 `event_kind`+`text`。故 PR-2 完全冻结。

---

## File Structure

| 文件 | 状态 | 责任 |
|---|---|---|
| `internal/commanderhub/registry.go` | 新 | `owner`、`DaemonInfo`、`daemonConn`(全字段,供全包用)、`registry`(owner→daemonID→conn 增删查/快照) |
| `internal/commanderhub/hub.go` | 新 | `Hub`、`NewHub`、`/api/daemon-link` handler(bearer→resolve→upgrade→register/ack/schema 校验)、`daemonConn` 的写/读/pending 机制、`bearerToken` util |
| `internal/commanderhub/auth.go` | 新 | device flow(`agentsdk`)、in-memory session 存储、`commanderIdentity`(cookie 或 bearer)、login/poll/logout handler |
| `internal/commanderhub/proxy.go` | 新 | `Hub.SendCommand` / `SendCommandStream` / `fanOutSessions`(并发+超时+fail-open) |
| `internal/commanderhub/sse.go` | 新 | `Envelope` → SSE 行(`event` 帧解码,其余转发) |
| `internal/commanderhub/http.go` | 新 | `/api/commander/*` 路由 handler(daemons/sessions/turn) |
| `internal/commanderhub/web.go` | 新 | `go:embed` 静态 + `/commander` 页面路由 |
| `internal/commanderhub/*_test.go` | 新 | 每 task 对应测试 + e2e |
| `internal/observerweb/server.go` | 改 | `Options` 加 `AgentserverURL`;构造 Hub+Authenticator 并挂路由 |
| `cmd/observer-server/main.go` | 改 1 行 | `observerWebOptions` 里填 `AgentserverURL: cfg.Identity.Agentserver.URL` |

---

## Phase 1 — Registry + Hub core

### Task 1: registry(隔离键 + 注册表 CRUD)

**Files:**
- Create: `internal/commanderhub/registry.go`
- Create: `internal/commanderhub/registry_test.go`

- [ ] **Step 1.1: 写失败测试 `registry_test.go`**

```go
package commanderhub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistry_AddLookupRemove(t *testing.T) {
	r := newRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	dc := &daemonConn{id: "d1", owner: o, displayName: "mac", kind: "claude", driverVersion: "v1"}

	r.add(dc)

	got, ok := r.lookup(o, "d1")
	require.True(t, ok)
	require.Equal(t, "mac", got.displayName)

	// 跨用户不可见
	_, ok = r.lookup(owner{userID: "bob", workspaceID: "W1"}, "d1")
	require.False(t, ok)

	// 同用户跨 workspace 不可见
	_, ok = r.lookup(owner{userID: "alice", workspaceID: "W2"}, "d1")
	require.False(t, ok)
}

func TestRegistry_DaemonsSnapshot(t *testing.T) {
	r := newRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	r.add(&daemonConn{id: "d1", owner: o, displayName: "mac", kind: "claude", driverVersion: "v1"})
	r.add(&daemonConn{id: "d2", owner: o, displayName: "linux", kind: "codex", driverVersion: "v2"})

	infos := r.daemons(o)
	require.Len(t, infos, 2)
	got := map[string]DaemonInfo{}
	for _, di := range infos {
		got[di.DaemonID] = di
	}
	require.Equal(t, "claude", got["d1"].Kind)
	require.Equal(t, "codex", got["d2"].Kind)

	// 别人的 owner 快照为空
	require.Empty(t, r.daemons(owner{userID: "bob", workspaceID: "W1"}))
}

func TestRegistry_RemoveCleansEmptyOwner(t *testing.T) {
	r := newRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	r.add(&daemonConn{id: "d1", owner: o})
	r.remove(o, "d1")

	_, ok := r.lookup(o, "d1")
	require.False(t, ok)
	require.Empty(t, r.daemons(o))
}
```

- [ ] **Step 1.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestRegistry -count=1
```
Expected: build error(undefined `newRegistry`/`owner`/`daemonConn`/`DaemonInfo`)。

- [ ] **Step 1.3: 实现 `registry.go`**

```go
// Package commanderhub implements the observer side of commander-web-entry
// (PR-3): the /daemon-link WebSocket hub, the /api/commander reverse proxy,
// and the /commander web page. See
// docs/superpowers/specs/2026-06-15-commander-observer-hub-design.md.
package commanderhub

import (
	"sync"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/internal/commander"
)

// owner is the isolation key: daemons under the same (UserID, WorkspaceID)
// are mutually visible; cross-owner is invisible + unreachable (404).
type owner struct {
	userID      string
	workspaceID string
}

// DaemonInfo is the JSON snapshot of an online daemon, returned to the web.
type DaemonInfo struct {
	DaemonID      string `json:"daemon_id"`
	DisplayName   string `json:"display_name"`
	Kind          string `json:"kind"`
	DriverVersion string `json:"driver_version"`
}

// daemonConn is one live daemon WebSocket link. Defined here (not hub.go) so
// the registry can be unit-tested without a real upgrade. Fields used by the
// WS read/write/pending machinery are populated in hub.go / proxy.go.
type daemonConn struct {
	id            string
	owner         owner
	displayName   string
	kind          string
	driverVersion string

	conn      *websocket.Conn
	writeMu   sync.Mutex // serializes conn.WriteJSON / WriteControl
	pendingMu sync.Mutex // guards pending map
	pending   map[string]chan commander.Envelope
	done      chan struct{} // closed when the read loop exits
	hub       *Hub
}

func (dc *daemonConn) info() DaemonInfo {
	return DaemonInfo{
		DaemonID:      dc.id,
		DisplayName:   dc.displayName,
		Kind:          dc.kind,
		DriverVersion: dc.driverVersion,
	}
}

// registry maps owner → daemonID → *daemonConn. All methods are goroutine-safe.
type registry struct {
	mu    sync.Mutex
	conns map[owner]map[string]*daemonConn
}

func newRegistry() *registry {
	return &registry{conns: make(map[owner]map[string]*daemonConn)}
}

// add indexes dc by its own owner + id. dc.id and dc.owner must be set.
func (r *registry) add(dc *daemonConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[dc.owner]
	if m == nil {
		m = make(map[string]*daemonConn)
		r.conns[dc.owner] = m
	}
	m[dc.id] = dc
}

func (r *registry) remove(o owner, daemonID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	delete(m, daemonID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

func (r *registry) lookup(o owner, daemonID string) (*daemonConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dc := r.conns[o][daemonID] // map[o] nil → zero-value map lookup returns nil,false-pattern
	return dc, dc != nil
}

func (r *registry) daemons(o owner) []DaemonInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	out := make([]DaemonInfo, 0, len(m))
	for _, dc := range m {
		out = append(out, dc.info())
	}
	return out
}
```

- [ ] **Step 1.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestRegistry -count=1 -race
```
Expected: 3 tests PASS。

- [ ] **Step 1.5: commit**

```bash
git add internal/commanderhub/registry.go internal/commanderhub/registry_test.go
git commit -m "feat(commanderhub): owner-keyed daemon registry (PR-3 Task 1)

internal/commanderhub/registry.go: owner{UserID,WorkspaceID} isolation key,
DaemonInfo snapshot type, daemonConn struct (full fields, shared across the
package), and a goroutine-safe registry (add/remove/lookup/daemons).

Cross-user and same-user-cross-workspace lookups return false → drives the
404 isolation boundary. Pure data + map ops, no WS yet.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: hub core(`/api/daemon-link` upgrade + register/ack + schema + read loop)

**Files:**
- Create: `internal/commanderhub/hub.go`
- Create: `internal/commanderhub/hub_test.go`

> 测试复用 PR-2 的 `commander.WSClient` 当「真 daemon」:起一个 `httptest.Server` 挂 `Hub.ServeHTTP`,让 `WSClient` 拨过去,验证 register/ack/schema/401。需要一个假 `identity.Resolver` 把测试 token 映射到 `(UserID, WorkspaceID)`。

- [ ] **Step 2.1: 在 `hub_test.go` 写公共假 resolver + 失败测试**

```go
package commanderhub

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
)

// fakeResolver maps a bearer token to a fixed Identity. Tests use it to drive
// subject/workspace isolation without a real agentserver whoami round-trip.
type fakeResolver struct {
	mu   map[string]identity.Identity // token → identity
	err  error
}

func (f *fakeResolver) Resolve(_ context.Context, token string) (identity.Identity, error) {
	if f.err != nil {
		return identity.Identity{}, f.err
	}
	if ident, ok := f.mu[token]; ok {
		return ident, nil
	}
	return identity.Identity{}, identity.ErrInvalid
}

func newHubServer(t *testing.T, resolver identity.Resolver) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewHub(resolver))
}

func wsClient(t *testing.T, srv *httptest.Server, token string) *commander.WSClient {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	return commander.NewWSClient(commander.WSConfig{
		URL:            wsURL,
		ProxyToken:     token,
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "test-daemon"},
		Handler:        &commander.Handler{}, // not exercised in hub tests
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
}

// waitFor calls cond every 10ms until it returns true or the timeout fires.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitFor: %s within %v", msg, timeout)
}

// TestHub_AcksRegisterAndAdmitsDaemon: daemon dials with a valid token → hub
// resolves (alice, W1), admits the daemon, sends ack. Linked flips true on the
// WSClient only after ack (PR-2 contract).
func TestHub_AcksRegisterAndAdmitsDaemon(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	srv := newHubServer(t, resolver)
	defer srv.Close()

	c := wsClient(t, srv, "tok-alice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

waitFor linked
waitFor(t, func() bool { return c.Linked() }, time.Second, "WSClient linked")
	require.True(t, c.Linked(), "daemon only links after observer ack")

	cancel()
	<-errCh
}

// TestHub_401OnUnknownToken: unknown token → resolver ErrInvalid → 401, no
// upgrade. WSClient treats 401 as terminal ErrObserverUnauthorized (PR-2).
func TestHub_401OnUnknownToken(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}} // nothing resolves
	srv := newHubServer(t, resolver)
	defer srv.Close()

	c := wsClient(t, srv, "bogus")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Run(ctx)
	require.Error(t, err)
	require.True(t, errors.Is(err, commander.ErrObserverUnauthorized),
		"want ErrObserverUnauthorized, got %v", err)
}

// TestHub_SchemaMismatchRejected: daemon sends wrong schema_version → hub writes
// error{schema_version_mismatch} and closes; WSClient treats it as terminal.
func TestHub_SchemaMismatchRejected(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	srv := newHubServer(t, resolver)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	hdr := wsDialHeader("tok-alice")
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, hdr)
	require.NoError(t, err)
	defer conn.Close()

	// Send a register with the wrong schema version.
	reg, _ := jsonMarshal(commander.RegisterPayload{SchemaVersion: commander.SchemaVersion + 999, Kind: "claude"})
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: reg}))

	// Expect an error envelope then close.
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, jsonUnmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeSchemaVersionMismatch, ep.Code)
}

// TestHub_DaemonsListsOnlyOwnOwner: two daemons under different owners register;
// registry.daemons(alice) sees only alice's.
func TestHub_DaemonsListsOnlyOwnOwner(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
		"tok-bob":   {UserID: "bob", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)

	// Simulate two admitted daemons by adding directly (admission path tested
	// above; here we test the registry snapshot an HTTP handler would call).
	hub.reg.add(&daemonConn{id: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
	hub.reg.add(&daemonConn{id: "b1", owner: owner{"bob", "W1"}, displayName: "bob-laptop", kind: "codex"})

	infos := hub.reg.daemons(owner{"alice", "W1"})
	require.Len(t, infos, 1)
	require.Equal(t, "a1", infos[0].DaemonID)
}
```

`hub_test.go` 顶部 import `encoding/json` 与 `net/http`,末尾放 3 个测试内 helper(直接包 stdlib,不留别名层):

```go
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func wsDialHeader(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}
```

- [ ] **Step 2.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestHub -count=1
```
Expected: build error(undefined `NewHub`/`Hub`/helpers)。

- [ ] **Step 2.3: 实现 `hub.go` + 测试 helpers `hub_helpers_test.go`**

`internal/commanderhub/hub.go`:

```go
package commanderhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
)

const (
	wsReadLimit = 1 << 20 // 1 MiB inbound cap (mirrors PR-2 daemon side)
	wsWriteWait = 5 * time.Second
)

// Hub owns the /daemon-link WebSocket endpoint and the owner-keyed registry of
// live daemon connections.
type Hub struct {
	resolver identity.Resolver
	upgrader websocket.Upgrader
	reg      *registry
	cmdSeq   atomic.Int64 // generates per-command IDs (see proxy.go)
}

// NewHub builds a Hub backed by resolver for bearer-token → Identity resolution.
func NewHub(resolver identity.Resolver) *Hub {
	return &Hub{
		resolver: resolver,
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		reg:      newRegistry(),
	}
}

// ServeHTTP implements GET /api/daemon-link.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	ident, err := h.resolver.Resolve(r.Context(), tok)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response.
	}
	conn.SetReadLimit(wsReadLimit)

	dc := &daemonConn{
		id:      newDaemonID(),
		owner:   o,
		conn:    conn,
		pending: make(map[string]chan commander.Envelope),
		done:    make(chan struct{}),
		hub:     h,
	}

	// First frame must be register; validate schema before admitting.
	reg, err := readFrame(conn)
	if err != nil {
		conn.Close()
		return
	}
	if reg.Type != "register" {
		conn.Close()
		return
	}
	var rp commander.RegisterPayload
	if err := json.Unmarshal(reg.Payload, &rp); err != nil {
		conn.Close()
		return
	}
	if rp.SchemaVersion != commander.SchemaVersion {
		_ = dc.writeEnvelope(errorEnvelope(0, "", commander.ErrCodeSchemaVersionMismatch, "schema version mismatch"))
		_ = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(wsWriteWait))
		conn.Close()
		return
	}
	dc.displayName = rp.DisplayName
	dc.kind = rp.Kind
	dc.driverVersion = rp.DriverVersion

	h.reg.add(dc)
	defer h.reg.remove(o, dc.id)
	defer close(dc.done)
	defer dc.failAllPending()

	// Ack: PR-2 WSClient only flips linked=true on receipt.
	if err := dc.writeEnvelope(commander.Envelope{Type: "ack"}); err != nil {
		return
	}

	dc.readLoop()
}

// --- daemonConn WS mechanics ---

func readFrame(conn *websocket.Conn) (commander.Envelope, error) {
	var env commander.Envelope
	return env, conn.ReadJSON(&env)
}

func (dc *daemonConn) writeEnvelope(env commander.Envelope) error {
	dc.writeMu.Lock()
	defer dc.writeMu.Unlock()
	_ = dc.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return dc.conn.WriteJSON(env)
}

// registerPending reserves a reply channel for cmdID. Caller owns lifecycle via
// removePending (terminal) or failAllPending (disconnect).
func (dc *daemonConn) registerPending(cmdID string) chan commander.Envelope {
	ch := make(chan commander.Envelope, 16)
	dc.pendingMu.Lock()
	dc.pending[cmdID] = ch
	dc.pendingMu.Unlock()
	return ch
}

func (dc *daemonConn) removePending(cmdID string) {
	dc.pendingMu.Lock()
	ch, ok := dc.pending[cmdID]
	if ok {
		delete(dc.pending, cmdID)
	}
	dc.pendingMu.Unlock()
	if ok {
		close(ch)
	}
}

// failAllPending closes every pending channel so SendCommand/Stream callers
// unblock with a disconnect error. Called on read-loop exit.
func (dc *daemonConn) failAllPending() {
	dc.pendingMu.Lock()
	olds := dc.pending
	dc.pending = make(map[string]chan commander.Envelope)
	dc.pendingMu.Unlock()
	for _, ch := range olds {
		close(ch)
	}
}

// readLoop drains inbound frames and routes each to its pending reply channel
// by frame.ID. Returns on read error / close → ServeHTTP's defers clean up.
func (dc *daemonConn) readLoop() {
	for {
		env, err := readFrame(dc.conn)
		if err != nil {
			return
		}
		dc.routeFrame(env)
	}
}

func (dc *daemonConn) routeFrame(env commander.Envelope) {
	if env.ID == "" {
		return // ack / heartbeat / unsolicited: nothing to correlate
	}
	dc.pendingMu.Lock()
	ch := dc.pending[env.ID]
	dc.pendingMu.Unlock()
	if ch == nil {
		return // unknown id (stale/late): drop
	}
	terminal := env.Type == "command_result" || env.Type == "error"
	if !sendOrDrop(ch, env, terminal, dc.done) {
		return
	}
	if terminal {
		dc.removePending(env.ID)
	}
}

// nextCmdID returns a hub-unique command id (used by proxy.go).
func (h *Hub) nextCmdID() string {
	return strconv.FormatInt(h.cmdSeq.Add(1), 36)
}

// --- shared utils (bearerToken also used by auth.go) ---

func bearerToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	return tok, tok != ""
}

func newDaemonID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func errorEnvelope(_ int64, id, code, message string) commander.Envelope {
	payload, _ := json.Marshal(commander.ErrorPayload{Code: code, Message: message})
	return commander.Envelope{Type: "error", ID: id, Payload: payload}
}
```

`hub.go` 的 import 块含 `"strconv"`(供 `nextCmdID` 用)。`sendOrDrop` 也在 `hub.go`:

```go
// sendOrDrop delivers env to ch. Non-terminal events are dropped if the buffer
// is full (avoid blocking the read loop on a slow consumer). Terminal frames
// force through (blocking on dc.done as escape). Returns false if dropped.
func sendOrDrop(ch chan commander.Envelope, env commander.Envelope, terminal bool, done <-chan struct{}) bool {
	select {
	case ch <- env:
		return true
	default:
	}
	if !terminal {
		return false
	}
	select {
	case ch <- env:
		return true
	case <-done:
		return false
	}
}
```

（`hub_test.go` 末尾的 3 个 helper 已在 Step 2.1 定义;本步只新增 `hub.go`,无新增测试文件。）

- [ ] **Step 2.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestHub -count=1 -race
go test ./internal/commanderhub/ -run TestRegistry -count=1 -race
go vet ./internal/commanderhub/
```
Expected: 4 hub tests + 3 registry tests PASS;vet clean。

- [ ] **Step 2.5: 跑一次 daemon 零改动回归**

```bash
git diff --stat master..HEAD -- 'internal/commander/**' 'cmd/driver-agent/**'
# expected: empty(PR-2 冻结面未碰)
```

- [ ] **Step 2.6: commit**

```bash
git add internal/commanderhub/hub.go internal/commanderhub/hub_test.go
git commit -m "feat(commanderhub): /daemon-link hub upgrade + register/ack + read loop (PR-3 Task 2)

internal/commanderhub/hub.go: Hub{resolver,upgrader,registry} with
ServeHTTP on /api/daemon-link — bearer→resolve→(UserID,WorkspaceID),
upgrade, read register, schema_version check (mismatch → error frame + close),
assign daemon_id, register, send ack, then read-loop routing inbound frames to
per-cmdID pending channels.

daemonConn WS mechanics: writeEnvelope (writeMu-serialized, write deadline),
registerPending/removePending/failAllPending, readLoop+routeFrame with
drop-on-full backpressure for events. bearerToken util shared with auth.go.

Tests reuse commander.WSClient as the real daemon: ack drives Linked()=true,
unknown token → 401 → ErrObserverUnauthorized, schema mismatch → terminal
error frame. PR-2 commander/driver-agent untouched.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 2 — Web 登录(device flow + observer session cookie)

> agentserver 只有 device-code OAuth(`agentsdk.RequestDeviceCode` + `agentsdk.PollForToken`,`dc.VerificationURIComplete`、`tok.AccessToken` 已从 driver-agent 用法确认)。observer 当 device client;成功后颁 httpOnly cookie,token 不进 JS。
> 为可测,把 device flow 抽成 `deviceFlow` 接口;生产用 `agentsdkDeviceFlow`(包 agentsdk),测试用 fake。这样 `auth_test` 不依赖 agentsdk 的 HTTP 路径。

### Task 3: auth.go(device flow + session + commanderIdentity + login/poll/logout)

**Files:**
- Create: `internal/commanderhub/auth.go`
- Create: `internal/commanderhub/auth_test.go`

- [ ] **Step 3.1: 写失败测试 `auth_test.go`**

```go
package commanderhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

// fakeDeviceFlow hands out a fixed verify URL and blocks PollToken until
// approve() is called, then returns a token the resolver can map.
type fakeDeviceFlow struct {
	mu       sync.Mutex
	approved chan struct{}
	token    string
}

func newFakeDeviceFlow(token string) *fakeDeviceFlow {
	return &fakeDeviceFlow{approved: make(chan struct{}), token: token}
}

func (f *fakeDeviceFlow) RequestCode(context.Context) (DeviceCode, error) {
	return DeviceCode{Code: "dc", VerificationURIComplete: "https://agent/verify?user_code=XX", ExpiresIn: 5 * time.Minute}, nil
}

func (f *fakeDeviceFlow) PollToken(ctx context.Context, _ DeviceCode) (string, error) {
	select {
	case <-f.approved:
		return f.token, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (f *fakeDeviceFlow) approve() { close(f.approved) }

func newAuth(t *testing.T, resolver identity.Resolver, flow deviceFlow) *Authenticator {
	t.Helper()
	return newAuthenticatorWithFlow(resolver, flow)
}

// TestAuth_LoginPollPendingThenApproved: login returns verify URL + login_id;
// poll is pending until approve, then returns ok and sets the httpOnly cookie.
func TestAuth_LoginPollPendingThenApproved(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	flow := newFakeDeviceFlow("tok-alice")
	a := newAuth(t, resolver, flow)

	// POST /login
	req := httptest.NewRequest(http.MethodPost, "/api/commander/login", nil)
	rec := httptest.NewRecorder()
	a.ServeLogin(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var lr struct {
		VerificationURIComplete string `json:"verification_uri_complete"`
		LoginID                 string `json:"login_id"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &lr))
	require.Equal(t, "https://agent/verify?user_code=XX", lr.VerificationURIComplete)
	require.NotEmpty(t, lr.LoginID)

	// poll before approval → pending
	rec2 := httptest.NewRecorder()
	a.ServeLoginPoll(rec2, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Contains(t, rec2.Body.String(), `"pending"`)
	require.Empty(t, rec2.Result().Cookies())

	// approve on the agentserver side → observer's poller gets the token
	flow.approve()
	require.Eventually(t, func() bool {
		rec3 := httptest.NewRecorder()
		a.ServeLoginPoll(rec3, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
		return rec3.Code == http.StatusOK && len(rec3.Result().Cookies()) == 1
	}, time.Second, 10*time.Millisecond, "poll never completed after approval")

	rec3 := httptest.NewRecorder()
	a.ServeLoginPoll(rec3, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	cookies := rec3.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, sessionCookieName, cookies[0].Name)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
}

// TestAuth_CommanderIdentityCookieOrBearer: cookie session → cached identity;
// no cookie but bearer → resolve; neither → false.
func TestAuth_CommanderIdentityCookieOrBearer(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	a := newAuth(t, resolver, newFakeDeviceFlow("tok-alice"))

	// seed a session directly
	sessID := a.putSession("tok-alice", identity.Identity{UserID: "alice", WorkspaceID: "W1"})

	// cookie path
	req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessID})
	ident, ok := a.CommanderIdentity(req)
	require.True(t, ok)
	require.Equal(t, "alice", ident.UserID)

	// bearer path (no cookie)
	req2 := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req2.Header.Set("Authorization", "Bearer tok-alice")
	ident2, ok2 := a.CommanderIdentity(req2)
	require.True(t, ok2)
	require.Equal(t, "alice", ident2.UserID)

	// neither
	req3 := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	_, ok3 := a.CommanderIdentity(req3)
	require.False(t, ok3)
}

// TestAuth_LogoutClearsSession: logout deletes the session and expires the cookie.
func TestAuth_LogoutClearsSession(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	a := newAuth(t, resolver, newFakeDeviceFlow("x"))
	sessID := a.putSession("tok", identity.Identity{UserID: "alice", WorkspaceID: "W1"})

	req := httptest.NewRequest(http.MethodPost, "/api/commander/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessID})
	rec := httptest.NewRecorder()
	a.ServeLogout(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// session gone → cookie no longer authenticates
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessID})
	_, ok := a.CommanderIdentity(req2)
	require.False(t, ok)
}
```

- [ ] **Step 3.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestAuth -count=1
```
Expected: build error(undefined `Authenticator`/`DeviceCode`/`deviceFlow`/`CommanderIdentity`/...)。

- [ ] **Step 3.3: 实现 `auth.go`**

```go
package commanderhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"

	"github.com/yourorg/multi-agent/internal/identity"
)

const (
	sessionCookieName = "commander_sess"
	sessionTTL        = 12 * time.Hour
	loginTTL          = 10 * time.Minute
)

// DeviceCode is the observer-internal view of an agentserver device-authorization
// response. Code is the server-side secret handed to PollToken.
type DeviceCode struct {
	Code                    string
	VerificationURIComplete string
	ExpiresIn               time.Duration
}

// deviceFlow is the seam between Authenticator and the OAuth grant. Production
// uses agentsdkDeviceFlow; tests inject a fake.
type deviceFlow interface {
	RequestCode(ctx context.Context) (DeviceCode, error)
	PollToken(ctx context.Context, code DeviceCode) (string, error)
}

// agentsdkDeviceFlow wraps the real agentserver device-code endpoints.
type agentsdkDeviceFlow struct{ serverURL string }

func (f agentsdkDeviceFlow) RequestCode(ctx context.Context) (DeviceCode, error) {
	dc, err := agentsdk.RequestDeviceCode(ctx, f.serverURL)
	if err != nil {
		return DeviceCode{}, err
	}
	return DeviceCode{
		Code:                    dc.DeviceCode, // confirm field name in agentsdk.DeviceCode
		VerificationURIComplete: dc.VerificationURIComplete,
		ExpiresIn:               time.Duration(dc.ExpiresIn) * time.Second, // confirm ExpiresIn (seconds)
	}, nil
}

func (f agentsdkDeviceFlow) PollToken(ctx context.Context, code DeviceCode) (string, error) {
	// PollForToken wants the agentsdk DeviceCode; rebuild the minimal shape it reads.
	dc := agentsdk.DeviceCode{DeviceCode: code.Code, ExpiresIn: int(code.ExpiresIn / time.Second)}
	tok, err := agentsdk.PollForToken(ctx, f.serverURL, dc)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

type session struct {
	token     string
	identity  identity.Identity
	expiresAt time.Time
}

type loginState struct {
	code      DeviceCode
	sessionID string // set when PollToken succeeds
	failed    bool
	done      bool
}

// Authenticator drives the web login (device flow) and owns the cookie→token
// session store. CommanderIdentity is the auth check used by /api/commander/*.
type Authenticator struct {
	resolver identity.Resolver
	flow     deviceFlow

	sessMu  sync.Mutex
	sessions map[string]*session

	loginMu sync.Mutex
	logins  map[string]*loginState
}

// NewAuthenticator builds an Authenticator backed by the real agentserver
// device flow at agentserverURL. Used by observerweb wiring (Task 8).
func NewAuthenticator(resolver identity.Resolver, agentserverURL string) *Authenticator {
	return newAuthenticatorWithFlow(resolver, agentsdkDeviceFlow{serverURL: agentserverURL})
}

// newAuthenticatorWithFlow lets tests inject a fake deviceFlow.
func newAuthenticatorWithFlow(resolver identity.Resolver, flow deviceFlow) *Authenticator {
	return &Authenticator{
		resolver: resolver,
		flow:     flow,
		sessions: make(map[string]*session),
		logins:   make(map[string]*loginState),
	}
}

// CommanderIdentity authenticates a /api/commander/* request: cookie session
// first (cached identity), then Authorization: Bearer (resolve), else false.
func (a *Authenticator) CommanderIdentity(r *http.Request) (identity.Identity, bool) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		a.sessMu.Lock()
		s := a.sessions[c.Value]
		a.sessMu.Unlock()
		if s != nil && time.Now().Before(s.expiresAt) {
			return s.identity, true
		}
	}
	if tok, ok := bearerToken(r.Header.Get("Authorization")); ok {
		ident, err := a.resolver.Resolve(r.Context(), tok)
		if err == nil {
			return ident, true
		}
	}
	return identity.Identity{}, false
}

// putSession is a test helper that seeds a session and returns its id.
func (a *Authenticator) putSession(token string, ident identity.Identity) string {
	sid := randomID()
	a.sessMu.Lock()
	a.sessions[sid] = &session{token: token, identity: ident, expiresAt: time.Now().Add(sessionTTL)}
	a.sessMu.Unlock()
	return sid
}

// ServeLogin: POST /api/commander/login → starts device flow, returns verify URL.
func (a *Authenticator) ServeLogin(w http.ResponseWriter, r *http.Request) {
	dc, err := a.flow.RequestCode(r.Context())
	if err != nil {
		http.Error(w, "device flow: "+err.Error(), http.StatusBadGateway)
		return
	}
	lid := randomID()
	a.loginMu.Lock()
	a.logins[lid] = &loginState{code: dc}
	a.loginMu.Unlock()

	go a.pollLogin(lid, dc)

	writeJSON(w, map[string]any{
		"verification_uri_complete": dc.VerificationURIComplete,
		"login_id":                  lid,
		"expires_in":                int(dc.ExpiresIn / time.Second),
	})
}

func (a *Authenticator) pollLogin(lid string, dc DeviceCode) {
	ctx, cancel := context.WithTimeout(context.Background(), dc.ExpiresIn)
	defer cancel()
	tok, err := a.flow.PollToken(ctx, dc)
	a.loginMu.Lock()
	st := a.logins[lid]
	a.loginMu.Unlock()
	if st == nil {
		return
	}
	if err != nil {
		a.loginMu.Lock()
		st.failed = true
		a.loginMu.Unlock()
		return
	}
	ident, err := a.resolver.Resolve(ctx, tok)
	if err != nil {
		a.loginMu.Lock()
		st.failed = true
		a.loginMu.Unlock()
		return
	}
	sid := a.putSession(tok, ident)
	a.loginMu.Lock()
	st.sessionID = sid
	st.done = true
	a.loginMu.Unlock()
}

// ServeLoginPoll: GET /api/commander/login/poll?id=<login_id>.
func (a *Authenticator) ServeLoginPoll(w http.ResponseWriter, r *http.Request) {
	lid := r.URL.Query().Get("id")
	a.loginMu.Lock()
	st := a.logins[lid]
	a.loginMu.Unlock()
	if st == nil {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	if st.failed {
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	if !st.done {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: st.sessionID,
		Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL / time.Second),
	})
	writeJSON(w, map[string]any{"status": "ok"})
}

// ServeLogout: POST /api/commander/logout.
func (a *Authenticator) ServeLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.sessMu.Lock()
		delete(a.sessions, c.Value)
		a.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"status": "ok"})
}

// --- shared helpers (writeJSON also used by http.go in Phase 3) ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

> **Pre-flight 在此落地(实现时确认 agentsdk 字段名):** `agentsdk.RequestDeviceCode` 返回类型里的 `DeviceCode` 字段(用于 PollToken)与 `ExpiresIn`(秒,int)。`VerificationURIComplete` 已确认。若 agentsdk 的 `DeviceCode` 结构体字段名不同,改 `agentsdkDeviceFlow.RequestCode`/`PollToken` 里的映射即可 —— 测试用 `fakeDeviceFlow`,不受影响。

- [ ] **Step 3.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestAuth -count=1 -race
go vet ./internal/commanderhub/
```
Expected: 3 auth tests PASS(含 race)。若 agentsdk 未在本机 module cache,`agentsdkDeviceFlow` 的编译依赖 agentsdk —— driver-agent 已 import agentsdk 且 go.mod 有该依赖,故本仓可编译;若 cache 缺失先 `go mod download`。

- [ ] **Step 3.5: commit**

```bash
git add internal/commanderhub/auth.go internal/commanderhub/auth_test.go
git commit -m "feat(commanderhub): device-flow login + httpOnly session cookie (PR-3 Task 3)

internal/commanderhub/auth.go: Authenticator drives agentserver device-code
OAuth via a deviceFlow seam (agentsdkDeviceFlow in prod, fake in tests),
issues an httpOnly/Secure/SameSite=Lax cookie mapping to a cached Identity.
CommanderIdentity authenticates /api/commander/* via cookie first, then
Bearer fallback (for curl/scripts). Login spawns a background poller; the
browser polls /login/poll until the cookie is set.

Token never reaches browser JS. Daemon side (PR-2) unchanged.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 3 — Proxy + SSE + HTTP routes

### Task 4: proxy.go(SendCommand / SendCommandStream / FanOutSessions)

**Files:**
- Create: `internal/commanderhub/proxy.go`
- Create: `internal/commanderhub/proxy_test.go`(含共享 `tbBackend` 假 backend,后续 sse/http/e2e 测试复用)

> 命令关联机制已在 Task 2 的 `daemonConn`(registerPending/removePending/failAllPending/routeFrame)落地。本任务实现 Hub 面向 HTTP 层的三个入口。routeFrame 在收到 `command_result`/`error` 终止帧时会 removePending(关 chan);消费方据此退出,自身 `defer removePending` 为幂等 no-op。

- [ ] **Step 4.1: 写失败测试 `proxy_test.go`**

```go
package commanderhub

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// tbBackend is a minimal agentbackend.Backend fake for commanderhub tests. The
// WSClient (real daemon) is wired to it; listFn/getFn/resumeFn drive behavior.
type tbBackend struct {
	listFn   func(context.Context) ([]agentbackend.Session, error)
	getFn    func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(context.Context, string, string, executor.Sink) (executor.Result, error)
}

func (b *tbBackend) Kind() agentbackend.Kind                         { return agentbackend.KindClaude }
func (b *tbBackend) Run(context.Context, executor.Task, executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (b *tbBackend) RunResume(ctx context.Context, id, ans string, sink executor.Sink) (executor.Result, error) {
	if b.resumeFn != nil {
		return b.resumeFn(ctx, id, ans, sink)
	}
	return executor.Result{}, nil
}
func (b *tbBackend) LLM() agentbackend.LLMRunner                { return nil }
func (b *tbBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (b *tbBackend) Detect(context.Context) error               { return nil }
func (b *tbBackend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	if b.listFn != nil {
		return b.listFn(ctx)
	}
	return nil, nil
}
func (b *tbBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	if b.getFn != nil {
		return b.getFn(ctx, id)
	}
	return agentbackend.Session{}, nil, nil
}

// dialFakeDaemon stands up a Hub server, dials a real WSClient (the "daemon")
// with the given backend + token, waits for ack, and returns the owner + the
// daemonID the hub assigned (read back via Daemons()).
func dialFakeDaemon(t *testing.T, resolver *fakeResolver, token string, backend agentbackend.Backend) (*Hub, *httptest.Server, owner, func()) {
	t.Helper()
	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL: wsURL, ProxyToken: token,
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "tester"},
		Handler:        &commander.Handler{Backend: backend},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	ident, _ := resolver.Resolve(ctx, token)
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}
	waitFor(t, func() bool { return c.Linked() }, time.Second, "daemon linked")
	cleanup := func() { cancel(); <-errCh; srv.Close() }
	return hub, srv, o, cleanup
}

// TestProxy_SendCommandListSessions: SendCommand(list_sessions) round-trips to
// the daemon and returns the backend's sessions payload.
func TestProxy_SendCommandListSessions(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1"}, {ID: "s2"}}, nil
		},
	})
	defer cleanup()

	di := hub.reg.daemons(o)
	require.Len(t, di, 1)
	payload, err := hub.SendCommand(context.Background(), o, di[0].DaemonID, "list_sessions", nil)
	require.NoError(t, err)
	require.Contains(t, string(payload), "s1")
	require.Contains(t, string(payload), "s2")
}

// TestProxy_SendCommandCrossOwner404: looking up another owner's daemon fails.
func TestProxy_SendCommandCrossOwner404(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, _, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{})
	defer cleanup()

	_, err := hub.SendCommand(context.Background(), owner{"bob", "W1"}, "any", "list_sessions", nil)
	require.ErrorIs(t, err, ErrDaemonNotFound)
}

// TestProxy_SendCommandStreamTurn: session_turn streams events then a terminal
// command_result on the returned channel.
func TestProxy_SendCommandStreamTurn(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			sink.Write("chunk", "one")
			sink.Write("chunk", "two")
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
	})
	defer cleanup()

	di := hub.reg.daemons(o)
	ch, err := hub.SendCommandStream(context.Background(), o, di[0].DaemonID, "session_turn",
		jsonRaw(t, commander.SessionTurnArgs{ID: "s1", Prompt: "go"}))
	require.NoError(t, err)

	var events, results int
	for env := range ch {
		if env.Type == "event" {
			events++
		}
		if env.Type == "command_result" {
			results++
		}
	}
	require.Equal(t, 2, events)
	require.Equal(t, 1, results)
}

// TestProxy_FanOutSessionsFailOpen: two daemons, one slow (timeout) → status
// timeout, the other ok; neither blocks the other.
func TestProxy_FanOutSessionsFailOpen(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "fast"}}, nil
		},
	})
	defer cleanup()
	// register a second "daemon" entry under same owner that will never answer
	// (no real conn) → SendCommand hits ErrDaemonGone/timeout quickly via done.
	hub.reg.add(&daemonConn{id: "ghost", owner: o, done: closedDone(), pending: map[string]chan commander.Envelope{}}})

	res := hub.FanOutSessions(context.Background(), o)
	byID := map[string]DaemonSessions{}
	for _, r := range res {
		byID[r.DaemonID] = r
	}
	require.Equal(t, "ok", byID[hub.reg.daemons(o)[0].DaemonID].Status) // real daemon answered
	require.Contains(t, []string{"error", "disconnected", "timeout"}, byID["ghost"].Status)
}
```

> 上面用到的两个小 helper 补进 `proxy_test.go`:
```go
func jsonRaw(t *testing.T, v any) []byte { t.Helper(); b, err := jsonMarshal(v); require.NoError(t, err); return b }
func closedDone() chan struct{}         { c := make(chan struct{}); close(c); return c }
```

- [ ] **Step 4.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestProxy -count=1
```
Expected: build error(undefined `Hub.SendCommand`/`SendCommandStream`/`FanOutSessions`/`ErrDaemonNotFound`/`DaemonSessions`)。

- [ ] **Step 4.3: 实现 `proxy.go`**

```go
package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
)

var (
	ErrDaemonNotFound = errors.New("commanderhub: daemon not found for this owner")
	ErrDaemonGone     = errors.New("commanderhub: daemon disconnected")
)

const (
	defaultCmdTimeout  = 10 * time.Second
	defaultTurnTimeout = 30 * time.Second
)

// SendCommand runs a non-streaming command (list_sessions / get_session) on one
// daemon and returns the command_result payload. ErrDaemonNotFound → caller 404.
func (h *Hub) SendCommand(ctx context.Context, o owner, daemonID, command string, args json.RawMessage) (json.RawMessage, error) {
	dc, ok := h.reg.lookup(o, daemonID)
	if !ok {
		return nil, ErrDaemonNotFound
	}
	select {
	case <-dc.done:
		return nil, ErrDaemonGone
	default:
	}
	cmdID := h.nextCmdID()
	ch := dc.registerPending(cmdID)
	defer dc.removePending(cmdID)
	if err := dc.writeEnvelope(commandEnvelope(cmdID, command, args)); err != nil {
		return nil, ErrDaemonGone
	}
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return nil, ErrDaemonGone
			}
			switch env.Type {
			case "error":
				return nil, fmt.Errorf("daemon error: %s", errorCode(env.Payload))
			case "command_result":
				return env.Payload, nil
			}
			// event: ignore for non-streaming commands
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-dc.done:
			return nil, ErrDaemonGone
		}
	}
}

// SendCommandStream runs a streaming command (session_turn). Events and the
// terminal command_result/error are forwarded on the returned channel, which is
// closed when the turn ends or the daemon/ctx is done.
func (h *Hub) SendCommandStream(ctx context.Context, o owner, daemonID, command string, args json.RawMessage) (<-chan commander.Envelope, error) {
	dc, ok := h.reg.lookup(o, daemonID)
	if !ok {
		return nil, ErrDaemonNotFound
	}
	select {
	case <-dc.done:
		return nil, ErrDaemonGone
	default:
	}
	cmdID := h.nextCmdID()
	ch := dc.registerPending(cmdID)
	if err := dc.writeEnvelope(commandEnvelope(cmdID, command, args)); err != nil {
		dc.removePending(cmdID)
		return nil, ErrDaemonGone
	}
	out := make(chan commander.Envelope, 16)
	go func() {
		defer close(out)
		defer dc.removePending(cmdID)
		for {
			select {
			case env, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- env:
				case <-ctx.Done():
					return
				case <-dc.done:
					return
				}
				if env.Type == "command_result" || env.Type == "error" {
					return
				}
			case <-ctx.Done():
				return
			case <-dc.done:
				return
			}
		}
	}()
	return out, nil
}

// DaemonSessions is one row of the fan-out GET /sessions result.
type DaemonSessions struct {
	DaemonID    string              `json:"daemon_id"`
	DisplayName string              `json:"display_name"`
	Kind        string              `json:"kind"`
	Status      string              `json:"status"` // ok|timeout|error|disconnected
	Error       string              `json:"error,omitempty"`
	Sessions    []map[string]any    `json:"sessions,omitempty"`
}

// FanOutSessions concurrently asks every online daemon of this owner for its
// sessions, each under defaultCmdTimeout. Slow/dead daemons surface a per-row
// status and do not block the rest (fail-open).
func (h *Hub) FanOutSessions(ctx context.Context, o owner) []DaemonSessions {
	snapshot := h.reg.daemons(o)
	results := make([]DaemonSessions, len(snapshot))
	var wg sync.WaitGroup
	for i := range snapshot {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			results[i] = h.oneDaemonSessions(ctx, o, snapshot[i])
		}()
	}
	wg.Wait()
	return results
}

func (h *Hub) oneDaemonSessions(ctx context.Context, o owner, di DaemonInfo) DaemonSessions {
	row := DaemonSessions{DaemonID: di.DaemonID, DisplayName: di.DisplayName, Kind: di.Kind}
	cctx, cancel := context.WithTimeout(ctx, defaultCmdTimeout)
	defer cancel()
	payload, err := h.SendCommand(cctx, o, di.DaemonID, "list_sessions", nil)
	switch {
	case errors.Is(err, ErrDaemonNotFound):
		row.Status = "disconnected"
	case errors.Is(err, context.DeadlineExceeded):
		row.Status, row.Error = "timeout", "context deadline exceeded"
	case err != nil:
		row.Status, row.Error = "error", err.Error()
	default:
		row.Status = "ok"
		var body struct {
			Sessions []map[string]any `json:"sessions"`
		}
		_ = json.Unmarshal(payload, &body)
		row.Sessions = body.Sessions
	}
	return row
}

// --- helpers ---

func commandEnvelope(cmdID, command string, args json.RawMessage) commander.Envelope {
	payload, _ := json.Marshal(commander.CommandPayload{Command: command, Args: args})
	return commander.Envelope{Type: "command", ID: cmdID, Payload: payload}
}

func errorCode(payload []byte) string {
	var ep commander.ErrorPayload
	_ = json.Unmarshal(payload, &ep)
	return ep.Code
}
```

- [ ] **Step 4.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestProxy -count=1 -race
go vet ./internal/commanderhub/
```
Expected: 4 proxy tests PASS。`TestProxy_FanOutSessionsFailOpen` 的 `ghost` daemon(无 conn)会立刻因 `dc.done` 已关闭返回 `ErrDaemonGone` → status `error` 或 `disconnected`。

- [ ] **Step 4.5: commit**

```bash
git add internal/commanderhub/proxy.go internal/commanderhub/proxy_test.go
git commit -m "feat(commanderhub): command proxy + fail-open session fan-out (PR-3 Task 4)

internal/commanderhub/proxy.go: Hub.SendCommand (non-streaming) and
SendCommandStream (session_turn) drive the per-cmdID pending channels from
Task 2 and surface ErrDaemonNotFound (→404) / ErrDaemonGone (→SSE error).
FanOutSessions asks every online daemon of an owner concurrently under a
per-daemon timeout; slow/dead rows get status timeout/error/disconnected
without blocking the rest.

proxy_test reuses commander.WSClient as the real daemon over a tbBackend fake.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: sse.go(Envelope → SSE 行)

**Files:**
- Create: `internal/commanderhub/sse.go`
- Create: `internal/commanderhub/sse_test.go`

- [ ] **Step 5.1: 写失败测试 `sse_test.go`**

```go
package commanderhub

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

func TestSSE_EventChunkDecoded(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	payload, _ := jsonMarshal(commander.EventPayload{EventKind: "chunk", Text: "hello"})
	s.writeEnvelope(commander.Envelope{Type: "event", ID: "c1", Payload: payload})

	body := rec.Body.String()
	require.Contains(t, body, "event: chunk\n")
	require.Contains(t, body, `data: {"text":"hello"}`)
}

func TestSSE_CommandResultForwardedAsDone(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	// daemon already marshalled this; observer forwards verbatim (no marshalTurnResult).
	s.writeEnvelope(commander.Envelope{Type: "command_result", ID: "c1", Payload: []byte(`{"result":{"summary":"done"}}`)})
	require.Contains(t, rec.Body.String(), "event: done\n")
	require.Contains(t, rec.Body.String(), `data: {"result":{"summary":"done"}}`)
}

func TestSSE_ErrorForwardedAndObserverSynthesized(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	payload, _ := jsonMarshal(commander.ErrorPayload{Code: "session_not_found", Message: "nope"})
	s.writeEnvelope(commander.Envelope{Type: "error", ID: "c1", Payload: payload})
	require.Contains(t, rec.Body.String(), "event: error\n")
	require.Contains(t, rec.Body.String(), "session_not_found")

	// observer-synthesized (not from daemon):
	rec2 := httptest.NewRecorder()
	s2 := newSSEWriter(rec2)
	s2.emitError("timeout", "30s no terminal frame")
	require.True(t, strings.Contains(rec2.Body.String(), "event: error\n"), rec2.Body.String())
	require.Contains(t, rec2.Body.String(), "timeout")
}
```

- [ ] **Step 5.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestSSE -count=1
```
Expected: build error(undefined `newSSEWriter`)。

- [ ] **Step 5.3: 实现 `sse.go`**

```go
package commanderhub

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/yourorg/multi-agent/internal/commander"
)

// sseWriter converts inbound daemon Envelopes to SSE event lines on an HTTP
// response. Content-Type/Cache-Control headers are set on construction. The
// browser reads this with fetch + ReadableStream (not EventSource — POST body).
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	s := &sseWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		s.flusher = f
	}
	return s
}

// writeEnvelope emits one daemon frame:
//   event            → "event: <event_kind>\ndata: {"text":...}"   (decoded)
//   command_result   → "event: done\ndata: <payload verbatim>"      (forwarded)
//   error            → "event: error\ndata: <payload verbatim>"     (forwarded)
// command_result/error payloads are produced by the daemon (PR-2) — observer
// forwards them as-is, so it never imports marshalTurnResult (PR-2 frozen).
func (s *sseWriter) writeEnvelope(env commander.Envelope) {
	switch env.Type {
	case "event":
		var ep commander.EventPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			return
		}
		data, _ := json.Marshal(map[string]string{"text": ep.Text})
		s.emit(ep.EventKind, data)
	case "command_result":
		s.emit("done", env.Payload)
	case "error":
		s.emit("error", env.Payload)
	}
}

// emitError emits an observer-synthesized SSE error (disconnect/timeout), not a
// daemon frame.
func (s *sseWriter) emitError(code, message string) {
	data, _ := json.Marshal(commander.ErrorPayload{Code: code, Message: message})
	s.emit("error", data)
}

func (s *sseWriter) emit(event string, data []byte) {
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
```

- [ ] **Step 5.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestSSE -count=1 -race
go vet ./internal/commanderhub/
```
Expected: 3 SSE tests PASS。

- [ ] **Step 5.5: commit**

```bash
git add internal/commanderhub/sse.go internal/commanderhub/sse_test.go
git commit -m "feat(commanderhub): daemon envelope → SSE adapter (PR-3 Task 5)

internal/commanderhub/sse.go: sseWriter converts inbound commander.Envelopes to
SSE event lines — event frames decoded to {\"text\":...} with event_kind as the
SSE event name; command_result/error payloads forwarded verbatim (so observer
never imports PR-2 marshalTurnResult). emitError for observer-synthesized
disconnect/timeout. Headers + per-write flush for real-time browser streaming.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: http.go(`/api/commander/*` routes)

**Files:**
- Create: `internal/commanderhub/http.go`
- Create: `internal/commanderhub/http_test.go`

> `Mount(mux, hub, auth)` 把所有 commander 路由挂到 observerweb 共享 mux(含 auth 三条 + daemon 路由)。鉴权统一走 `Authenticator.CommanderIdentity`(cookie 或 bearer)。

- [ ] **Step 6.1: 写失败测试 `http_test.go`**

```go
package commanderhub

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// commanderSetup wires Hub+Auth on a shared mux with one real daemon (WSClient)
// and returns the server, hub, auth, the daemon's owner, and a ready session
// cookie for that owner's token.
func commanderSetup(t *testing.T, resolver *fakeResolver, token string, backend agentbackend.Backend) (*httptest.Server, *Hub, *Authenticator, owner, *http.Cookie, func()) {
	t.Helper()
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, newFakeDeviceFlow(token))
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	srv := httptest.NewServer(mux)

	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL: wsURL, ProxyToken: token,
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "tester"},
		Handler:        &commander.Handler{Backend: backend},
		HeartbeatInt:   10 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	ident, _ := resolver.Resolve(ctx, token)
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}
	waitFor(t, func() bool { return c.Linked() }, time.Second, "daemon linked")

	sessID := auth.putSession(token, ident)
	cookie := &http.Cookie{Name: sessionCookieName, Value: sessID}
	cleanup := func() { cancel(); <-errCh; srv.Close() }
	return srv, hub, auth, o, cookie, cleanup
}

// TestHTTP_DaemonsUnauthorizedWithoutCookie: no cookie/bearer → 401.
func TestHTTP_DaemonsUnauthorizedWithoutCookie(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, _, _, _, _, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{})
	defer cleanup()

	resp, err := http.Get(srv.URL + "/api/commander/daemons")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHTTP_DaemonsReturnsOwn: with the session cookie, /daemons lists this
// owner's daemon.
func TestHTTP_DaemonsReturnsOwn(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, _, _, _, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{})
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "tester") // DisplayName from register
}

// TestHTTP_TurnStreamsSSE: POST .../turn returns text/event-stream with chunk
// events and a terminal done event. daemonID is read from the hub registry.
func TestHTTP_TurnStreamsSSE(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			sink.Write("chunk", "hello")
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"), resp.Header.Get("Content-Type"))

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "event: chunk")
	require.Contains(t, joined, "hello")
	require.Contains(t, joined, "event: done")
}
```

- [ ] **Step 6.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestHTTP -count=1
```
Expected: build error(undefined `Mount` — http.go not created yet; `commanderSetup`/`tbBackend`/`fakeResolver`/`waitFor` are already defined in this package's earlier test files)。

- [ ] **Step 6.3: 实现 `http.go`**

```go
package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/commander"
)

// Mount registers all commander routes on the observerweb shared mux. hub may be
// nil (commander disabled) — auth/login routes still mount.
func Mount(mux *http.ServeMux, hub *Hub, auth *Authenticator) {
	mux.HandleFunc("/api/commander/login", auth.ServeLogin)
	mux.HandleFunc("/api/commander/login/poll", auth.ServeLoginPoll)
	mux.HandleFunc("/api/commander/logout", auth.ServeLogout)
	if hub == nil {
		return
	}
	ch := &commanderHandlers{hub: hub, auth: auth}
	mux.HandleFunc("/api/commander/daemons", ch.daemons)
	mux.HandleFunc("/api/commander/sessions", ch.sessionsFanout)
	mux.HandleFunc("/api/commander/daemons/", ch.daemonScoped)
}

type commanderHandlers struct {
	hub  *Hub
	auth *Authenticator
}

func (ch *commanderHandlers) ownerOf(w http.ResponseWriter, r *http.Request) (owner, bool) {
	ident, ok := ch.auth.CommanderIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return owner{}, false
	}
	return owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}, true
}

func (ch *commanderHandlers) daemons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"daemons": ch.hub.reg.daemons(o)})
}

func (ch *commanderHandlers) sessionsFanout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"daemons": ch.hub.FanOutSessions(r.Context(), o)})
}

// daemonScoped routes /api/commander/daemons/{id}/sessions[/{sid}[/turn]].
func (ch *commanderHandlers) daemonScoped(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/commander/daemons/")
	id, rest2, _ := strings.Cut(rest, "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case rest2 == "sessions":
		ch.listSessions(w, r, id)
	case strings.HasPrefix(rest2, "sessions/"):
		sub := strings.TrimPrefix(rest2, "sessions/")
		sid, tail, _ := strings.Cut(sub, "/")
		switch {
		case sid == "":
			http.NotFound(w, r)
		case tail == "turn":
			ch.turn(w, r, id, sid)
		case tail == "":
			ch.getSession(w, r, id, sid)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (ch *commanderHandlers) listSessions(w http.ResponseWriter, r *http.Request, daemonID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	payload, err := ch.hub.SendCommand(r.Context(), o, daemonID, "list_sessions", nil)
	if errors.Is(err, ErrDaemonNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (ch *commanderHandlers) getSession(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	args, _ := json.Marshal(commander.GetSessionArgs{ID: sid})
	payload, err := ch.hub.SendCommand(r.Context(), o, daemonID, "get_session", args)
	if errors.Is(err, ErrDaemonNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (ch *commanderHandlers) turn(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	args, _ := json.Marshal(commander.SessionTurnArgs{ID: sid, Prompt: body.Prompt})
	ctx, cancel := context.WithTimeout(r.Context(), defaultTurnTimeout)
	defer cancel()

	chunkCh, err := ch.hub.SendCommandStream(ctx, o, daemonID, "session_turn", args)
	if errors.Is(err, ErrDaemonNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sse := newSSEWriter(w)
	terminal := false
	for env := range chunkCh {
		sse.writeEnvelope(env)
		if env.Type == "command_result" || env.Type == "error" {
			terminal = true
		}
	}
	if !terminal {
		// stream closed without a terminal frame: timeout vs disconnect
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			sse.emitError("timeout", "no terminal frame within timeout")
		} else {
			sse.emitError(commander.ErrCodeBackendUnavailable, "daemon disconnected")
		}
	}
}
```

- [ ] **Step 6.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestHTTP -count=1 -race
go vet ./internal/commanderhub/
```
Expected: 3 http tests PASS。

- [ ] **Step 6.5: 全包回归**

```bash
go test ./internal/commanderhub/ -count=1 -race
```
Expected: registry + hub + auth + proxy + sse + http 全绿。

- [ ] **Step 6.6: commit**

```bash
git add internal/commanderhub/http.go internal/commanderhub/http_test.go
git commit -m "feat(commanderhub): /api/commander/* routes + Mount (PR-3 Task 6)

internal/commanderhub/http.go: Mount registers login/poll/logout + daemon
routes on the shared mux. GET /daemons, GET /sessions (fan-out), GET
/daemons/{id}/sessions, GET /daemons/{id}/sessions/{sid}, POST
/daemons/{id}/sessions/{sid}/turn (SSE). All non-login routes authenticate via
CommanderIdentity (cookie or bearer); cross-owner daemon id → 404. Turn handler
synthesizes timeout/disconnect SSE errors when the stream ends without a
terminal frame.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 4 — Web UI(`/commander`,go:embed + vanilla JS)

### Task 7: web.go + 嵌入静态资源

**Files:**
- Create: `internal/commanderhub/web.go`
- Create: `internal/commanderhub/assets/index.html`
- Create: `internal/commanderhub/assets/app.js`
- Create: `internal/commanderhub/assets/style.css`
- Create: `internal/commanderhub/web_test.go`

> 极简单页,无构建。浏览器 fetch 全部 `credentials:'include'`(发 cookie)。turn 用 `fetch`+`ReadableStream` 读 SSE(非 `EventSource`)。token 全程不进 JS(observer device flow + httpOnly cookie)。

- [ ] **Step 7.1: 写失败测试 `web_test.go`**

```go
package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeb_CommanderPageAndAssets(t *testing.T) {
	mux := http.NewServeMux()
	MountWeb(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// /commander → HTML
	resp, err := http.Get(srv.URL + "/commander")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	require.True(t, strings.Contains(string(body[:n]), "commander"), "index references app.js")

	// assets served
	for _, p := range []string{"/commander/app.js", "/commander/style.css"} {
		resp, err := http.Get(srv.URL + p)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, p)
		resp.Body.Close()
	}

	// unknown path under /commander/ → 404 (no stray fileserver catch-all)
	resp, err = http.Get(srv.URL + "/commander/nope")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}
```

- [ ] **Step 7.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestWeb -count=1
```
Expected: build error(undefined `MountWeb`;embed 目录不存在 → 编译失败)。

- [ ] **Step 7.3: 建 `assets/index.html`**

```html
<!doctype html>
<html lang="zh">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>commander</title>
  <link rel="stylesheet" href="/commander/style.css" />
</head>
<body>
  <header><h1>commander</h1><div id="auth"></div></header>
  <main id="app"></main>
  <script src="/commander/app.js"></script>
</body>
</html>
```

- [ ] **Step 7.4: 建 `assets/style.css`**

```css
:root { font-family: system-ui, sans-serif; --muted: #666; }
body { margin: 0; }
header { display: flex; justify-content: space-between; align-items: center;
         padding: .6rem 1rem; border-bottom: 1px solid #ddd; }
main { padding: 1rem; }
ul.daemons, ul.sessions { list-style: none; padding: 0; cursor: pointer; }
ul.daemons li, ul.sessions li { padding: .4rem .2rem; border-bottom: 1px solid #eee; }
.chat { border: 1px solid #ddd; padding: .5rem; height: 60vh; overflow: auto; }
.msg.user { color: #005; } .msg.assistant { color: #050; } .muted { color: var(--muted); }
textarea { width: 100%; box-sizing: border-box; }
```

- [ ] **Step 7.5: 建 `assets/app.js`**

```javascript
// commander /commander page. All API calls use the observer's httpOnly session
// cookie (credentials:'include'). No token is ever stored in JS.
const app = document.getElementById("app");
const auth = document.getElementById("auth");

async function api(path, opts = {}) {
  const res = await fetch(path, { credentials: "include", ...opts });
  if (res.status === 401) { showLogin(); throw new Error("unauthorized"); }
  return res;
}

function showLogin() {
  auth.innerHTML = "";
  const btn = document.createElement("button");
  btn.textContent = "用 agentserver 登录";
  btn.onclick = startLogin;
  auth.appendChild(btn);
  app.innerHTML = '<p class="muted">请先登录。</p>';
}

async function startLogin() {
  const r = await api("/api/commander/login", { method: "POST" });
  const { verification_uri_complete, login_id } = await r.json();
  window.open(verification_uri_complete, "_blank"); // user approves on agentserver
  const poll = async () => {
    const pr = await api("/api/commander/login/poll?id=" + encodeURIComponent(login_id));
    if (pr.status === 401) { app.innerHTML = '<p class="muted">登录失败,重试。</p>'; return; }
    const body = await pr.json();
    if (body.status === "ok") { await whoami(); return; }
    setTimeout(poll, 1500); // pending → keep polling
  };
  poll();
}

async function whoami() {
  // After login the cookie is set; load the daemon list to confirm + render.
  auth.innerHTML = '<button id="logout">登出</button>';
  document.getElementById("logout").onclick = async () => {
    await api("/api/commander/logout", { method: "POST" }); showLogin();
  };
  await showDaemons();
}

async function showDaemons() {
  const r = await api("/api/commander/daemons");
  const { daemons } = await r.json();
  app.innerHTML = '<h2>daemons</h2><ul class="daemons"></ul>';
  const ul = app.querySelector("ul.daemons");
  if (!daemons || daemons.length === 0) {
    ul.innerHTML = '<li class="muted">没有在线 daemon(先在机器上跑 driver-agent serve-daemon)。</li>';
    return;
  }
  for (const d of daemons) {
    const li = document.createElement("li");
    li.textContent = `${d.display_name || d.daemon_id} (${d.kind})`;
    li.onclick = () => showSessions(d.daemon_id, d.display_name);
    ul.appendChild(li);
  }
}

async function showSessions(daemonID, name) {
  const r = await api(`/api/commander/daemons/${daemonID}/sessions`);
  const { sessions } = await r.json();
  app.innerHTML = `<h2>${name} · sessions</h2><button id="back">← daemons</button><ul class="sessions"></ul>`;
  document.getElementById("back").onclick = showDaemons;
  const ul = app.querySelector("ul.sessions");
  if (!sessions || sessions.length === 0) {
    ul.innerHTML = '<li class="muted">这台 daemon 暂无 session。</li>'; return;
  }
  for (const s of sessions) {
    const li = document.createElement("li");
    li.textContent = `${s.id}  ${(s.last_user_msg || "").slice(0, 60)}`;
    li.onclick = () => showChat(daemonID, s.id);
    ul.appendChild(li);
  }
}

async function showChat(daemonID, sid) {
  const r = await api(`/api/commander/daemons/${daemonID}/sessions/${sid}`);
  const { session, messages } = await r.json();
  app.innerHTML = `<h2>${session && session.id || sid}</h2>
    <button id="back">← sessions</button>
    <div class="chat" id="chat"></div>
    <textarea id="prompt" rows="2" placeholder="发一轮 turn…"></textarea>
    <button id="send">发送</button>`;
  document.getElementById("back").onclick = () => showSessions(daemonID, session && session.working_dir || "");
  const chat = document.getElementById("chat");
  (messages || []).forEach(renderMsg);
  document.getElementById("send").onclick = () => sendTurn(daemonID, sid);

  function renderMsg(m) {
    const div = document.createElement("div");
    div.className = "msg " + (m.role || "");
    div.textContent = m.text || "";
    chat.appendChild(div);
  }
  chat.scrollTop = chat.scrollHeight;
}

async function sendTurn(daemonID, sid) {
  const prompt = document.getElementById("prompt").value.trim();
  if (!prompt) return;
  const chat = document.getElementById("chat");
  const div = document.createElement("div");
  div.className = "msg user"; div.textContent = prompt;
  chat.appendChild(div);
  const assistant = document.createElement("div");
  assistant.className = "msg assistant"; chat.appendChild(assistant);
  document.getElementById("prompt").value = "";

  // POST + stream SSE via fetch ReadableStream (EventSource can't POST).
  const res = await fetch(`/api/commander/daemons/${daemonID}/sessions/${sid}/turn`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ prompt }),
  });
  if (!res.ok) { assistant.textContent = "错误: " + res.status; return; }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, idx); buf = buf.slice(idx + 2);
      handleSSE(block, assistant);
    }
  }
  chat.scrollTop = chat.scrollHeight;
}

function handleSSE(block, assistant) {
  let event = "message";
  for (const line of block.split("\n")) {
    if (line.startsWith("event: ")) event = line.slice(7);
    else if (line.startsWith("data: ")) {
      let text = "";
      try { text = JSON.parse(line.slice(6)).text || ""; } catch {
        // done/error payloads carry result/error objects, not {text}
        if (event === "error") text = "[error] " + line.slice(6);
      }
      if (event === "chunk" && text) assistant.textContent += text;
      if (event === "done") {
        try { const r = JSON.parse(line.slice(6)); if (r.result && r.result.awaiting_user) {
          assistant.textContent += "\n(需审批:请到 CLI 端继续)";
        } } catch {}
      }
    }
  }
}

// boot: try /daemons to see if already authed (cookie present); else show login.
(async function boot() {
  try {
    const r = await fetch("/api/commander/daemons", { credentials: "include" });
    if (r.ok) { await whoami(); } else { showLogin(); }
  } catch { showLogin(); }
})();
```

- [ ] **Step 7.6: 实现 `web.go`**

```go
package commanderhub

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/index.html assets/app.js assets/style.css
var assetsFS embed.FS

// MountWeb registers GET /commander (the page) and its static assets.
func MountWeb(mux *http.ServeMux) {
	mux.HandleFunc("/commander", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/commander" {
			http.NotFound(w, r)
			return
		}
		data, err := assetsFS.ReadFile("assets/index.html")
		if err != nil {
			http.Error(w, "index unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	sub, _ := fs.Sub(assetsFS, "assets")
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/commander/app.js", fileServer)
	mux.Handle("/commander/style.css", fileServer)
}
```

- [ ] **Step 7.7: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestWeb -count=1 -race
go vet ./internal/commanderhub/
```
Expected: web test PASS(embed 成功;页面/asset 200;未知路径 404)。

- [ ] **Step 7.8: commit**

```bash
git add internal/commanderhub/web.go internal/commanderhub/web_test.go internal/commanderhub/assets/
git commit -m "feat(commanderhub): /commander page + embedded vanilla assets (PR-3 Task 7)

internal/commanderhub/web.go + assets/: go:embed single-page app (index.html,
app.js, style.css). Vanilla JS: device-flow login (opens agentserver verify tab,
polls /login/poll until cookie set), daemon list → session list → chat view,
streaming turn over fetch+ReadableStream SSE, awaiting_user → 'go to CLI'.

All fetches use credentials:'include'; no token ever reaches JS. No build step.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 5 — Wiring + e2e + 自审

### Task 8: MountAll + observerweb 接线 + main +1 字段

**Files:**
- Create: `internal/commanderhub/wiring.go`
- Create: `internal/commanderhub/wiring_test.go`
- Modify: `internal/observerweb/server.go`(Options 加字段;`NewWithResolverOptions` 调 `MountAll`)
- Modify: `cmd/observer-server/main.go`(`observerWebOptions` 加 1 行)

- [ ] **Step 8.1: 写失败测试 `wiring_test.go`**

```go
package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

// TestMountAll_RegistersAllSurfaces: MountAll wires daemon-link + commander API
// + /commander page. API routes require auth (401); the page is public.
func TestMountAll_RegistersAllSurfaces(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	mux := http.NewServeMux()
	MountAll(mux, resolver, "https://agent.example/")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// page is public
	resp, err := http.Get(srv.URL + "/commander")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// commander API requires auth
	resp, err = http.Get(srv.URL + "/api/commander/daemons")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// bearer works (no cookie needed for scripts/curl)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
```

- [ ] **Step 8.2: 跑红**

```bash
go test ./internal/commanderhub/ -run TestMountAll -count=1
```
Expected: build error(undefined `MountAll`)。

- [ ] **Step 8.3: 实现 `wiring.go`**

```go
package commanderhub

import (
	"net/http"

	"github.com/yourorg/multi-agent/internal/identity"
)

// MountAll wires the full commander surface onto mux: the daemon WebSocket
// endpoint, the /api/commander/* reverse proxy + auth, and the /commander page.
// One call from observerweb.NewWithResolverOptions.
func MountAll(mux *http.ServeMux, resolver identity.Resolver, agentserverURL string) {
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, agentserverURL)
	mux.Handle("/api/daemon-link", hub) // hub.ServeHTTP upgrades the daemon WS
	Mount(mux, hub, auth)               // /api/commander/* + login/poll/logout
	MountWeb(mux)                       // /commander page + assets
}
```

- [ ] **Step 8.4: 跑绿**

```bash
go test ./internal/commanderhub/ -run TestMountAll -count=1 -race
```
Expected: PASS。

- [ ] **Step 8.5: observerweb 接线**

`internal/observerweb/server.go`:

(a) `Options` struct(server.go:44)加字段:
```go
type Options struct {
	TelemetryRateLimit  RateLimitConfig
	MaxEventBodyBytes   int64
	Objects             objectstore.Store
	DisableObjectProxy  bool
	MaxObjectProxyBytes int64
	RegisterDisabled    bool
	AgentserverURL      string // PR-3: enables /commander surface (empty = disabled)
}
```

(b) import 块加:
```go
	"github.com/yourorg/multi-agent/internal/commanderhub"
```

(c) `NewWithResolverOptions` 末尾(返回 mux 之前)加:
```go
	if opts.AgentserverURL != "" {
		commanderhub.MountAll(mux, resolver, opts.AgentserverURL)
	}
```
> 具体插入点:`NewWithResolverOptions` 内 `mux` 构造完、return 之前。若该函数把 mux 包了中间件(如 logging/recovery),MountAll 要挂在**最内层裸 mux**上(路由匹配在最内层),否则中间件会拦截 `/api/daemon-link` 的 Upgrade。实现时确认 mux 与中间件包裹顺序。

- [ ] **Step 8.6: main.go `observerWebOptions` +1 字段**

`cmd/observer-server/main.go` 的 `observerWebOptions(cfg, objects)` 返回值里加:
```go
		AgentserverURL: strings.TrimSpace(cfg.Identity.Agentserver.URL),
```
(`strings` 已在 main.go import — 第 581/621 行已用 `strings.TrimSpace`/`TrimRight`。)这是 main.go 唯一改动:1 行。`NewWithResolverOptions(...)` 调用处不变。

- [ ] **Step 8.7: 全量构建 + vet**

```bash
go build ./...
go vet ./...
go test ./internal/commanderhub/ ./internal/observerweb/ -count=1 -race
# observer-server 二进制能编出来(验 main 零断裂):
CGO_ENABLED=0 go build -o /tmp/observer-pr3 ./cmd/observer-server
```
Expected: build/vet 通过;commanderhub 全绿;observerweb 既有测试不回归。

- [ ] **Step 8.8: master-path + daemon 冻结面回归**

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**' 'internal/commander/**' 'cmd/driver-agent/**'
# expected: empty
```

- [ ] **Step 8.9: commit**

```bash
git add internal/commanderhub/wiring.go internal/commanderhub/wiring_test.go internal/observerweb/server.go cmd/observer-server/main.go
git commit -m "feat(commanderhub): wire /commander into observerweb (PR-3 Task 8)

commanderhub.MountAll(mux, resolver, agentserverURL) mounts the daemon WS
endpoint, the /api/commander/* reverse proxy + auth, and the /commander page in
one call. observerweb.Options gains AgentserverURL; NewWithResolverOptions
calls MountAll when set. main.go adds exactly one line (AgentserverURL from
cfg.Identity.Agentserver.URL). PR-2/daemon/master untouched.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 9: 跨用户×workspace e2e

**Files:**
- Create: `internal/commanderhub/e2e_test.go`

> 4-token 网格 `tok-AW`(alice/W)、`tok-AW2`(alice/W2)、`tok-BW`(bob/W)、`tok-BW2`(bob/W2),各起一个真 `commander.WSClient`。验证:**同用户跨 workspace 隔离**(tok-AW 看不到 alice/W2 的 daemon)、**跨用户隔离**(alice 看不到 bob)、**扇出**、**turn SSE**、**跨 owner 404**。用 Bearer 直连(/api/commander/* 接受 bearer,免去 device flow)。

- [ ] **Step 9.1: 写 e2e 测试 `e2e_test.go`**

```go
package commanderhub

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
)

func TestE2E_SubjectAndWorkspaceIsolation(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-AW":  {UserID: "alice", WorkspaceID: "W"},
		"tok-AW2": {UserID: "alice", WorkspaceID: "W2"},
		"tok-BW":  {UserID: "bob", WorkspaceID: "W"},
		"tok-BW2": {UserID: "bob", WorkspaceID: "W2"},
	}}
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, "https://unused/") // bearer path; device flow not exercised
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	mux.Handle("/api/daemon-link", hub)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Dial four real daemons, each under its own (user, workspace).
	mkDaemon := func(token, display string, backend agentbackend.Backend) func() {
		wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
		c := commander.NewWSClient(commander.WSConfig{
			URL: wsURL, ProxyToken: token,
			Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: display},
			Handler:        &commander.Handler{Backend: backend},
			HeartbeatInt:   10 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		})
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- c.Run(ctx) }()
		waitFor(t, func() bool { return c.Linked() }, time.Second, display+" linked")
		return func() { cancel(); <-errCh }
	}

	cleanup := sync.WaitGroup{}
	cleanAll := []func(){}
	addDaemon := func(token, display string, b agentbackend.Backend) {
		cleanup.Add(1)
		cl := mkDaemon(token, display, b)
		cleanAll = append(cleanAll, func() { cl(); cleanup.Done() })
	}
	addDaemon("tok-AW", "alice-W", &tbBackend{})
	addDaemon("tok-AW2", "alice-W2", &tbBackend{})
	addDaemon("tok-BW", "bob-W", &tbBackend{})
	addDaemon("tok-BW2", "bob-W2", &tbBackend{})
	defer func() { for _, c := range cleanAll { c() }; cleanup.Wait() }()

	bearerGet := func(token, path string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	// alice/W sees exactly her W daemon, not her W2 daemon, not bob's.
	resp, body := bearerGet("tok-AW", "/api/commander/daemons")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "alice-W")
	require.NotContains(t, body, "alice-W2") // same user, other workspace
	require.NotContains(t, body, "bob-")     // other user

	// alice/W2 sees only her W2 daemon.
	_, body = bearerGet("tok-AW2", "/api/commander/daemons")
	require.Contains(t, body, "alice-W2")
	require.NotContains(t, body, "alice-W\"") // not the W one (substring guard)

	// bob sees only bob.
	_, body = bearerGet("tok-BW", "/api/commander/daemons")
	require.Contains(t, body, "bob-W")
	require.NotContains(t, body, "alice-")

	// Cross-owner daemon id → 404. Grab bob-W's id via the registry, hit as alice.
	bobW := hub.reg.daemons(owner{"bob", "W"})
	require.Len(t, bobW, 1)
	resp, _ = bearerGet("tok-AW", "/api/commander/daemons/"+bobW[0].DaemonID+"/sessions")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestE2e_TurnSSEBearer(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-AW": {UserID: "alice", WorkspaceID: "W"},
	}}
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, "https://unused/")
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	mux.Handle("/api/daemon-link", hub)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL: wsURL, ProxyToken: "tok-AW",
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "alice-W"},
		Handler: &commander.Handler{Backend: &tbBackend{
			resumeFn: func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
				sink.Write("chunk", "hi from daemon")
				sink.Close()
				return executor.Result{Summary: "done"}, nil
			},
		}},
		HeartbeatInt: 10 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() { cancel(); <-errCh }()
	waitFor(t, func() bool { return c.Linked() }, time.Second, "linked")

	daemonID := hub.reg.daemons(owner{"alice", "W"})[0].DaemonID
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/turn",
		strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Authorization", "Bearer tok-AW")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))

	var lines []string
	for sc := bufio.NewScanner(resp.Body); sc.Scan(); {
		lines = append(lines, sc.Text())
	}
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "event: chunk")
	require.Contains(t, joined, "hi from daemon")
	require.Contains(t, joined, "event: done")
}
```

- [ ] **Step 9.2: 跑 e2e**

```bash
go test ./internal/commanderhub/ -run 'TestE2E_|TestE2e_' -count=1 -race -v
```
Expected: 2 e2e tests PASS。`-race` 下无 data race(注册表/pending/写串行正确)。

- [ ] **Step 9.3: 全包 + 全仓回归**

```bash
go test ./internal/commanderhub/ -count=1 -race
go test ./... -count=1            # 确认未碰其它包
go vet ./...
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**' 'internal/commander/**' 'cmd/driver-agent/**'
# expected: empty
```

- [ ] **Step 9.4: commit**

```bash
git add internal/commanderhub/e2e_test.go
git commit -m "test(commanderhub): cross-user×workspace e2e (PR-3 Task 9)

Four real commander.WSClient daemons across (alice/bob) × (W/W2) against one
observer. Asserts same-user-cross-workspace and cross-user invisibility, the
fan-out list, cross-owner daemon id → 404, and a bearer-authed streaming turn
(chunk + done). Uses Bearer (accepted by /api/commander/*) to bypass device
flow, which is unit-tested separately in auth_test.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Self-review(plan 作者自检,落地前跑一遍)

- [ ] **Spec 覆盖**:逐条对照 spec——hub/registry(Task 1-2)、device-flow+cookie 鉴权(Task 3)、proxy+fanout(Task 4)、SSE(Task 5)、HTTP 路由(Task 6)、UI(Task 7)、wiring(Task 8)、e2e(Task 9)。`marshalTurnResult` 不导出(observer 转发 payload)✓;daemon 零改动 ✓;`(UserID,WorkspaceID)` 双键 ✓;fail-open ✓;SSE 形态对齐 PR-2 ✓。
- [ ] **占位符扫描**:无 `TODO/placeholder/实现时再改` 残留 —— `commanderSetup` 已返回 `*Hub`(6 返回值)、Task 2 测试 helper 已内联 stdlib、`strconv.FormatInt` 直用。
- [ ] **类型/签名一致**:`NewAuthenticator(resolver, agentserverURL)`(prod,Task 3/8)vs `newAuthenticatorWithFlow`(tests)✓;`Mount`/`MountWeb`/`MountAll` ✓;`Hub.SendCommand/SendCommandStream/FanOutSessions` 签名 ✓;`commander.Envelope`/`CommandPayload`/`EventPayload`/`ErrorPayload`/`ErrCode*` 复用 ✓。
- [ ] **agentsdk 字段确认**:`agentsdkDeviceFlow` 里 `dc.DeviceCode`/`dc.ExpiresIn`/`dc.VerificationURIComplete`/`tok.AccessToken` —— `VerificationURIComplete`/`AccessToken` 已确认;`DeviceCode`/`ExpiresIn` 在实现 Task 3 时 `go doc` 核对一次,字段名不同则改映射(测试用 fake 不受影响)。
- [ ] **observerweb 中间件顺序**:Task 8 Step 8.5(c)的 MountAll 必须挂在最内层裸 mux(在 Upgrade 之前),否则中间件吃掉 `/api/daemon-link`。

## Verification(end-to-end,人工一次)

```bash
# 1. 全绿
go test ./... -race -count=1 && go vet ./...
# 2. 冻结面零改动
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**' 'internal/commander/**' 'cmd/driver-agent/**'
# 3. 跑起 observer-server + 一个真 driver-agent serve-daemon,浏览器打开 /commander,
#    device-flow 登录 → 看到 daemon → 选 session → 发 turn 实时看 chunk
CGO_ENABLED=0 go build ./cmd/observer-server ./cmd/driver-agent
```

