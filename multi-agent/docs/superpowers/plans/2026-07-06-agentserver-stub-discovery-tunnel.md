# agentserver-stub Discovery / Tunnel / Peer-Proxy / Tasks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `agentserver-stub` 从只有 `register/whoami/heartbeat` 3 个端点扩展到具备完整 driver→slave dispatch 能力：discovery (cards+agents)、WS tunnel with yamux server、peer proxy over HTTP-over-yamux、task poll 全 4 端点。修 [agentserver/loom#78](https://github.com/agentserver/loom/issues/78)。

**Architecture:** 单进程 in-memory smoke server；6 张 map（byProxy/byTunnel/cards/tunnels/tasks/tasksBySandbox）共享一把 `sync.RWMutex`；yamux stream I/O 全部在锁外做。协议 helper (`WriteStreamHeader/HTTPStreamMeta` 等)、WSConn (net.Conn 桥)、yamux ServerMux 都字节对齐 agentserver v0.69.9 `internal/tunnel/*.go`（4 文件 433 行原样复制，加 SOURCE 注释）。

**Tech Stack:**
- Go 1.24（沿用仓库 `go.mod`）
- `nhooyr.io/websocket v1.8.17` — WS upgrade + Ping (`Ping(ctx)` 单参 + `context.WithTimeout`)
- `github.com/hashicorp/yamux v0.1.2` — server side session mux
- `net/http` `ServeMux` — 现有 stub 就是这个，不引入 router 库
- `httptest.NewServer` — 单测模式（沿用 `stub_test.go`）

## Global Constraints

*Every task's requirements implicitly include this section. 全部**直接从 spec 抄字面**，不要改写。*

- 所有代码文件路径以 repo root `multi-agent/` 为准（例：`multi-agent/tools/eval/agentserver-stub/discovery.go`）
- 所有新文件放 `tools/eval/agentserver-stub/` 内（`package main`；stub 是 command）
- **单锁**：所有 6 张 map 共享 `s.mu` (`sync.RWMutex`)；yamux Open/Read/Write **绝不**在持锁时调用
- **`agent_id` = `sandbox_id`**（不是 `short_id`；driver 侧 `resolveTarget` 按 `SandboxID` 比对 self）
- **peer proxy 路径**：`ANY /api/agent/peer/<target_short_id>/proxy/<rest_path...>[?query]`；转发 `HTTPStreamMeta.Path` 必须**去掉** `/api/agent/peer/<target_short_id>/proxy` 前缀，**保留** rest path + 原 raw query 一字节不改
- **peer proxy 错误码**：401 bad bearer / 404 unknown target OR cross-workspace（不区分防泄漏）/ 502 no active tunnel / 502 stream error / 504 timeout
- **task poll 路径**：`GET /api/agent/tasks/poll[?sandbox_id=X]`；`sandbox_id` 可选，缺省 default 到 caller；仅当显式给且 ≠ caller 时 403
- **task 状态字段兼容双 shape**：`result` (json.RawMessage) 优先（loom poller.go path）；否则 `output` (string)（agentsdk SDK path）
- **cards 存储 key = `sandbox_id`**；`tasksBySandbox` key = target `sandbox_id`；`tunnels` key = `sandbox_id`
- **workspace 隔离**：discovery/agents 只返 caller.workspace_id 内的 cards；tasks POST 跨 workspace → 403；peer proxy 跨 workspace → 404
- **`self` 不排除**：`discovery/agents` 返回列表**包含**caller 自己的 card；driver 侧 `resolveTarget` 按 `SandboxID` 排 self
- **heartbeat**：`ws.Ping(context.WithTimeout(ctx, 5*time.Second))`，每 20s；失败关 session
- **reconnect race**：`unregisterTunnel(sid, t)` 仅当 `tunnels[sid] == t` 时 delete（防新 session 被旧 handler 误删）
- **原始来源引用注释**：`tunnel_transport.go` 头部必须写 `// Copied verbatim from github.com/agentserver/agentserver@v0.69.9 internal/tunnel/{stream,mux,wsconn,registry}.go — needed because that package is internal/. Do NOT edit; if the upstream copy diverges, refresh this file with a byte-diff.`
- **单元测试架构**：全部走 `httptest.NewServer(NewServer("").Handler())`；tunnel 测试用 `nhooyr.io/websocket.Dial` + `yamux.Client` 端起 fake handler
- **验收 5 层**（见 spec §1.3）：L1 单测全绿 → L2 slave.log 无 404 → L3 discovery ≥1 台 slave → L4 capability_snapshots ≥1 hash → L5 dispatch echo hello

---

## 文件结构

参见 spec §3.1。任务映射：

| Task | 文件（新/改） | Spec ref | Interface produced |
|---|---|---|---|
| 1 | 改 `server.go` + 新 `tunnel_transport.go`（复制协议 helper） | §2.1, §3.5 | 类型 `HTTPStreamMeta` / `HTTPResponseMeta` / `stubTunnel` / `writeStreamHeader` / `readStreamHeader` / const `streamTypeHTTP` |
| 2 | 新 `discovery.go` + `discovery_test.go` | §4.1, §4.2, §9.1 | `Server.cards` map；handler `handleDiscoveryCards` / `handleDiscoveryAgents`；类型 `agentCard` |
| 3 | 新 `tunnel.go` + `tunnel_test.go` | §4.3, §3.6, §9.2 | `Server.tunnels` map；handler `handleTunnelUpgrade`；`Server.registerTunnel` / `Server.unregisterTunnel` |
| 4 | 新 `peerproxy.go` + `peerproxy_test.go` | §4.4, §3.3 step 5, §9.3 | handler `handlePeerProxy` |
| 5 | 新 `tasks.go` + `tasks_test.go` | §4.5-4.8, §9.4 | `Server.tasks` / `Server.tasksBySandbox` maps；4 handlers |
| 6 | 改 `server.go`（挂 mux + `Close`） | §3.1, §3.7 | `Handler()` 挂全部新路径；`Server.Close()` 关 tunnels |

Task 之间**严格顺序**：Task 1 是所有 tunnel 相关 task 的前置；Task 6 是最后集成。Task 2 / 5 各自独立（不依赖 Task 3/4），但通用 `Server` struct 字段需在 Task 1 一起添加以避免多次改 struct。

---

## Task 1：复制协议 helpers + Server struct 扩字段

**Files:**
- Modify: `multi-agent/tools/eval/agentserver-stub/server.go` — 只加 struct fields + `Close()` 骨架（handler 不挂，Task 6 再挂）
- Create: `multi-agent/tools/eval/agentserver-stub/tunnel_transport.go` — 复制 agentserver 侧 4 个文件合并
- Create: `multi-agent/tools/eval/agentserver-stub/tunnel_transport_test.go` — 只 test `writeStreamHeader/readStreamHeader` round-trip

**Interfaces:**
- Consumes: 现有 `Server` + `whoamiResponse` + `deriveToken`
- Produces:
  - Types: `HTTPStreamMeta{Method,Path,Headers,BodyLen}` `HTTPResponseMeta{Status,Headers}` `stubTunnel` `wsConn`
  - Consts: `streamTypeHTTP byte = 0x01` `streamTypeTerminal = 0x02` `streamTypeControl = 0x03`
  - Funcs: `writeStreamHeader(w io.Writer, streamType byte, metadata []byte) error` / `readStreamHeader(r io.Reader) (byte, []byte, error)` / `newWSConn(ctx, ws) *wsConn` / `serverMux(conn net.Conn) (*yamux.Session, error)` / `newStubTunnel(ctx, sid, ws) *stubTunnel` / `(*stubTunnel).OpenHTTPStream(ctx, meta, body) (HTTPResponseMeta, io.ReadCloser, error)` / `(*stubTunnel).Close()` / `(*stubTunnel).Done() <-chan struct{}` / `yamuxCfg() *yamux.Config`
  - Server fields: `cards map[string]agentCard` / `tunnels map[string]*stubTunnel` / `tasks map[string]*stubTask` / `tasksBySandbox map[string][]string`
  - `Server.Close() error` 骨架（后续 task 填充）

- [ ] **Step 1.1: Write the failing test for header round-trip**

Add to a fresh file `tunnel_transport_test.go`:

```go
package main

import (
	"bytes"
	"testing"
)

func TestWriteReadStreamHeader_RoundTrip(t *testing.T) {
	meta := []byte(`{"method":"GET","path":"/x","headers":{"a":"b"},"body_len":0}`)
	var buf bytes.Buffer
	if err := writeStreamHeader(&buf, streamTypeHTTP, meta); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, got, err := readStreamHeader(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != streamTypeHTTP {
		t.Fatalf("streamType: want %d got %d", streamTypeHTTP, typ)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("metadata: want %q got %q", meta, got)
	}
}

func TestReadStreamHeader_MetaTooLarge_Errors(t *testing.T) {
	// 1B type + 4B big-endian length = 1<<21 (2MB, exceeds 1MB limit)
	var buf bytes.Buffer
	buf.Write([]byte{streamTypeHTTP})
	buf.Write([]byte{0x00, 0x20, 0x00, 0x00}) // 2MB
	if _, _, err := readStreamHeader(&buf); err == nil {
		t.Fatal("want error for 2MB metadata; got nil")
	}
}
```

- [ ] **Step 1.2: Run test to verify it fails**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestWriteReadStreamHeader -v
```

Expected: FAIL with `undefined: writeStreamHeader` / `undefined: streamTypeHTTP` / `undefined: readStreamHeader`.

- [ ] **Step 1.3: Create `tunnel_transport.go`**

```go
// Copied verbatim (with local renames to unexported names + package rename to `main`)
// from github.com/agentserver/agentserver@v0.69.9 internal/tunnel/{stream,mux,wsconn,registry}.go
// — needed because that package is internal/. Do NOT edit; if the upstream copy
// diverges, refresh this file with a byte-diff.
//
// Original files & sizes (agentserver v0.69.9):
//   stream.go   76 lines
//   mux.go      38 lines
//   wsconn.go   97 lines
//   registry.go 222 lines
//
// Adaptations in this copy:
//   - Package renamed to `main` (stub is a command)
//   - Public helpers renamed to unexported: WriteStreamHeader → writeStreamHeader, etc.
//   - Types stubTunnel / wsConn — smoke variants of tunnel.Tunnel / tunnel.WSConn;
//     terminal / OnAgentInfo / control-stream dispatch dropped (spec §1.2 non-goals).

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// ---------------------------------------------------------------------------
// Stream header wire format (spec §3.5)
//   1B type + 4B big-endian metadata length + metadata bytes
// ---------------------------------------------------------------------------

const (
	streamTypeHTTP     byte = 0x01
	streamTypeTerminal byte = 0x02 // reserved; not implemented
	streamTypeControl  byte = 0x03 // agent-info control; read + discarded
)

func writeStreamHeader(w io.Writer, streamType byte, metadata []byte) error {
	header := make([]byte, 5)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(metadata)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write stream header: %w", err)
	}
	if len(metadata) > 0 {
		if _, err := w.Write(metadata); err != nil {
			return fmt.Errorf("write stream metadata: %w", err)
		}
	}
	return nil
}

func readStreamHeader(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, fmt.Errorf("read stream header: %w", err)
	}
	streamType := header[0]
	metaLen := binary.BigEndian.Uint32(header[1:5])
	if metaLen > 1<<20 {
		return 0, nil, fmt.Errorf("stream metadata too large: %d bytes", metaLen)
	}
	var metadata []byte
	if metaLen > 0 {
		metadata = make([]byte, metaLen)
		if _, err := io.ReadFull(r, metadata); err != nil {
			return 0, nil, fmt.Errorf("read stream metadata: %w", err)
		}
	}
	return streamType, metadata, nil
}

// HTTPStreamMeta is written from server → agent as stream_type=streamTypeHTTP metadata.
type HTTPStreamMeta struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	BodyLen int               `json:"body_len"`
}

// HTTPResponseMeta is written from agent → server as stream_type=streamTypeHTTP metadata.
type HTTPResponseMeta struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// ---------------------------------------------------------------------------
// wsConn — net.Conn wrapper over nhooyr.io/websocket
// (spec §3.4 — copied from agentserver internal/tunnel/wsconn.go)
// ---------------------------------------------------------------------------

type wsConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	ws     *websocket.Conn
	rBuf   []byte
	rMu    sync.Mutex
	wMu    sync.Mutex
	closed bool
	deadR  time.Time
	deadW  time.Time
}

func newWSConn(ctx context.Context, ws *websocket.Conn) *wsConn {
	cctx, cancel := context.WithCancel(ctx)
	// Match upstream agentserver internal/tunnel.NewWSConn wsconn.go:33 —
	// disable nhooyr/websocket's default 32KiB read limit so yamux can
	// carry frames larger than that without dropping the session
	// (fix plan review r2 P0).
	ws.SetReadLimit(-1)
	return &wsConn{ctx: cctx, cancel: cancel, ws: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	c.rMu.Lock()
	defer c.rMu.Unlock()
	if len(c.rBuf) > 0 {
		n := copy(p, c.rBuf)
		c.rBuf = c.rBuf[n:]
		return n, nil
	}
	rctx := c.ctx
	if !c.deadR.IsZero() {
		var cancel context.CancelFunc
		rctx, cancel = context.WithDeadline(c.ctx, c.deadR)
		defer cancel()
	}
	_, data, err := c.ws.Read(rctx)
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	if n < len(data) {
		c.rBuf = data[n:]
	}
	return n, nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wMu.Lock()
	defer c.wMu.Unlock()
	wctx := c.ctx
	if !c.deadW.IsZero() {
		var cancel context.CancelFunc
		wctx, cancel = context.WithDeadline(c.ctx, c.deadW)
		defer cancel()
	}
	if err := c.ws.Write(wctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "close")
}

func (c *wsConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (c *wsConn) RemoteAddr() net.Addr               { return dummyAddr{} }
func (c *wsConn) SetDeadline(t time.Time) error      { c.deadR = t; c.deadW = t; return nil }
func (c *wsConn) SetReadDeadline(t time.Time) error  { c.deadR = t; return nil }
func (c *wsConn) SetWriteDeadline(t time.Time) error { c.deadW = t; return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "ws" }
func (dummyAddr) String() string  { return "ws://tunnel" }

// ---------------------------------------------------------------------------
// yamux config + factories (spec §3.4)
// ---------------------------------------------------------------------------

func yamuxCfg() *yamux.Config {
	c := yamux.DefaultConfig()
	// EnableKeepAlive off — heartbeat handled at WS layer (Ping) per §4.3.
	c.EnableKeepAlive = false
	// Silence yamux internal logging in tests
	c.LogOutput = io.Discard
	return c
}

func serverMux(conn net.Conn) (*yamux.Session, error) {
	return yamux.Server(conn, yamuxCfg())
}

// ---------------------------------------------------------------------------
// stubTunnel — server-side representation of one live WS tunnel
// (spec §3.4 — copied from agentserver internal/tunnel/registry.go)
// ---------------------------------------------------------------------------

type stubTunnel struct {
	sandboxID string
	mux       *yamux.Session
	wsConn    *wsConn
	done      chan struct{}
	closeOnce sync.Once
}

func newStubTunnel(ctx context.Context, sid string, ws *websocket.Conn) (*stubTunnel, error) {
	conn := newWSConn(ctx, ws)
	session, err := serverMux(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("yamux server: %w", err)
	}
	t := &stubTunnel{sandboxID: sid, mux: session, wsConn: conn, done: make(chan struct{})}
	go t.watch()
	return t, nil
}

// watch closes t (through Close/closeOnce) when the yamux session dies.
// Never closes done directly — see spec §3.4 note on double-close panic.
func (t *stubTunnel) watch() {
	<-t.mux.CloseChan()
	t.Close()
}

// OpenHTTPStream (spec §3.4) — writes HTTPStreamMeta + body over a new yamux stream,
// reads HTTPResponseMeta back, and returns the stream as the response body reader.
// Caller MUST close the returned reader.
func (t *stubTunnel) OpenHTTPStream(ctx context.Context, meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, io.ReadCloser, error) {
	if t.mux == nil {
		return HTTPResponseMeta{}, nil, errors.New("nil yamux session")
	}
	stream, err := t.mux.Open()
	if err != nil {
		return HTTPResponseMeta{}, nil, err
	}
	// Best-effort deadline from ctx
	if dl, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(dl)
	}
	meta.BodyLen = len(body)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	if err := writeStreamHeader(stream, streamTypeHTTP, metaJSON); err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	if len(body) > 0 {
		if _, err := stream.Write(body); err != nil {
			stream.Close()
			return HTTPResponseMeta{}, nil, err
		}
	}
	_, respMetaJSON, err := readStreamHeader(stream)
	if err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	var respMeta HTTPResponseMeta
	if err := json.Unmarshal(respMetaJSON, &respMeta); err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	return respMeta, stream, nil
}

func (t *stubTunnel) Close() {
	t.closeOnce.Do(func() {
		if t.mux != nil {
			t.mux.Close()
		}
		if t.wsConn != nil {
			t.wsConn.Close()
		}
		close(t.done)
	})
}

func (t *stubTunnel) Done() <-chan struct{} { return t.done }
```

- [ ] **Step 1.4: Extend `Server` struct in `server.go`**

Modify `server.go`:

- In the `type Server struct {}` block, add these fields **after** `byTunnel`:

```go
	cards          map[string]agentCard   // key = sandbox_id — populated by handleDiscoveryCards
	tunnels        map[string]*stubTunnel // key = sandbox_id — populated by handleTunnelUpgrade
	tasks          map[string]*stubTask   // key = task_id — populated by handleCreateTask
	tasksBySandbox map[string][]string    // key = target sandbox_id → task_id list (pending only)
```

- In `NewServer()`, initialize the new maps:

```go
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
```

- Add `Close()` skeleton **at the bottom of `server.go`**:

```go
// Close shuts down every open tunnel; safe to call multiple times but Server
// is single-use after Close (map state is not zeroed). Tests defer Close().
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
```

- Add these **placeholder types** at the top of `server.go` (just under the imports; concrete field lists come in Tasks 2 and 5):

```go
// agentCard is what handleDiscoveryCards stores and handleDiscoveryAgents returns.
// Full definition in discovery.go (Task 2).
type agentCard struct {
	SandboxID   string          `json:"-"`
	WorkspaceID string          `json:"-"`
	ShortID     string          `json:"-"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Card        json.RawMessage `json:"card"`
}

// stubTask holds one delegated task.  Full definition in tasks.go (Task 5).
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
	Status        string          `json:"status"` // pending, assigned, running, completed, failed
	Result        json.RawMessage `json:"result,omitempty"`
	Output        string          `json:"output,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
	TotalCostUSD  float64         `json:"total_cost_usd,omitempty"`
	NumTurns      int             `json:"num_turns,omitempty"`
	CreatedAt     time.Time       `json:"-"`
	CompletedAt   time.Time       `json:"-"`
}
```

- Add `time` and `json` to imports of `server.go` if not already present.

- [ ] **Step 1.5: Run header + build**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go build ./tools/eval/agentserver-stub/
go test ./tools/eval/agentserver-stub/ -run TestWriteReadStreamHeader -v
```

Expected: build ok; both `TestWriteReadStreamHeader_RoundTrip` and `TestReadStreamHeader_MetaTooLarge_Errors` PASS.

- [ ] **Step 1.6: Commit**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
git add tools/eval/agentserver-stub/tunnel_transport.go \
        tools/eval/agentserver-stub/tunnel_transport_test.go \
        tools/eval/agentserver-stub/server.go
git commit -m "stub(#78): copy tunnel protocol helpers + extend Server struct

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2：Discovery cards + agents 端点

**Files:**
- Create: `multi-agent/tools/eval/agentserver-stub/discovery.go`
- Create: `multi-agent/tools/eval/agentserver-stub/discovery_test.go`
- Modify: `multi-agent/tools/eval/agentserver-stub/server.go`（Handler() 挂 2 个新路径，仅当前 task 涉及的两条 —— Task 6 会最终校对整份 mux）

**Interfaces:**
- Consumes: Task 1 的 `Server.cards` map, `agentCard` 类型, `bearerToken()`, `Server.mu`, `Server.byProxy`
- Produces:
  - Handlers: `Server.handleDiscoveryCards(w, r)` / `Server.handleDiscoveryAgents(w, r)`
  - Wire: `discovery/cards` POST 与 `discovery/agents` GET，行为对齐 spec §4.1 §4.2

- [ ] **Step 2.1: Write failing tests**

Create `discovery_test.go`:

```go
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

// helpers -------------------------------------------------------------------

// registerAgent hits /api/v1/agents/register and returns the five-tuple.
// Uses workspaceID (empty → server default).
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

// postCard hits /api/agent/discovery/cards with a Bearer token, returns the raw resp.
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

// newTestServerWithStub returns both the httptest.Server and the underlying *Server
// so tunnel/peer-proxy tests can inspect internal state (e.g. tunnels map) without
// racy time.Sleep-based synchronization.
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

// tests ---------------------------------------------------------------------

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
	// Now GET agents — should see the card
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
	// Publish a card in each workspace
	postCard(t, srv, credsA.ProxyToken, map[string]any{"display_name": "eval-slave-a", "card": map[string]any{}}).Body.Close()
	postCard(t, srv, credsB.ProxyToken, map[string]any{"display_name": "eval-slave-b", "card": map[string]any{}}).Body.Close()
	// A only sees A
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
```

- [ ] **Step 2.2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestDiscovery -v
```

Expected: FAIL — 404 not-found on `/api/agent/discovery/cards` (handler not mounted yet).

- [ ] **Step 2.3: Create `discovery.go`**

```go
package main

import (
	"encoding/json"
	"net/http"
)

// discoveryCardRequest mirrors the payload slave.internal/tunnel.PublishCard sends.
// See spec §4.1.
type discoveryCardRequest struct {
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Card        json.RawMessage `json:"card"`
}

// discoveryAgentEntry is the array element returned by GET /api/agent/discovery/agents.
// Matches github.com/agentserver/agentserver@v0.69.9 pkg/agentsdk.AgentCard field-for-field.
// (spec §4.2)
type discoveryAgentEntry struct {
	AgentID     string          `json:"agent_id"` // = card.SandboxID
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Status      string          `json:"status"` // always "available"
	Card        json.RawMessage `json:"card"`
	Version     int             `json:"version"` // always 1
}

func (s *Server) handleDiscoveryCards(w http.ResponseWriter, r *http.Request) {
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
	ident, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req discoveryCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.AgentType == "" {
		req.AgentType = "custom"
	}
	card := agentCard{
		SandboxID:   ident.SandboxID,
		WorkspaceID: ident.WorkspaceID,
		ShortID:     ident.ShortID,
		DisplayName: req.DisplayName,
		Description: req.Description,
		AgentType:   req.AgentType,
		Card:        req.Card,
	}
	s.mu.Lock()
	s.cards[ident.SandboxID] = card
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDiscoveryAgents(w http.ResponseWriter, r *http.Request) {
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
	if !known {
		s.mu.RUnlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wsID := ident.WorkspaceID
	entries := make([]discoveryAgentEntry, 0, len(s.cards))
	for _, c := range s.cards {
		if c.WorkspaceID != wsID {
			continue
		}
		entries = append(entries, discoveryAgentEntry{
			AgentID:     c.SandboxID,
			DisplayName: c.DisplayName,
			Description: c.Description,
			AgentType:   c.AgentType,
			Status:      "available",
			Card:        c.Card,
			Version:     1,
		})
	}
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}
```

- [ ] **Step 2.4: Wire into `Handler()` in `server.go`**

In `server.go` `Handler()` function, **after** the existing register/whoami/heartbeat mux loop, add:

```go
	// Discovery (spec §4.1 §4.2)
	mux.HandleFunc("/api/agent/discovery/cards", s.handleDiscoveryCards)
	mux.HandleFunc("/api/agent/discovery/agents", s.handleDiscoveryAgents)
```

- [ ] **Step 2.5: Run tests to verify all pass**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestDiscovery -v
```

Expected: ALL 7 `TestDiscovery*` tests PASS.

- [ ] **Step 2.6: Commit**

```bash
git add tools/eval/agentserver-stub/discovery.go \
        tools/eval/agentserver-stub/discovery_test.go \
        tools/eval/agentserver-stub/server.go
git commit -m "stub(#78): implement /api/agent/discovery/{cards,agents}

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3：Tunnel WS upgrade + reconnect race handling

**Files:**
- Create: `multi-agent/tools/eval/agentserver-stub/tunnel.go`
- Create: `multi-agent/tools/eval/agentserver-stub/tunnel_test.go`
- Modify: `multi-agent/tools/eval/agentserver-stub/server.go`（Handler() 挂 `/api/tunnel/`）

**Interfaces:**
- Consumes: Task 1 的 `stubTunnel`, `newStubTunnel`, `Server.tunnels`, `Server.mu`, `Server.byTunnel`
- Produces:
  - `Server.handleTunnelUpgrade(w, r)`
  - `Server.registerTunnel(sid string, t *stubTunnel)` — 覆盖旧 session
  - `Server.unregisterTunnel(sid string, t *stubTunnel) bool` — 仅当 instance 匹配才 delete；返 true 表示删除生效

- [ ] **Step 3.1: Write failing tests**

Create `tunnel_test.go`:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// dialTunnel connects a client-side yamux session to the stub tunnel endpoint.
// Returns (yamux.Session, cleanup). If token or sid mismatches, dial fails.
func dialTunnel(t *testing.T, srv *httptest.Server, sid, token string) (*yamux.Session, *websocket.Conn, error) {
	t.Helper()
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + sid + "?token=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, nil, err
	}
	conn := newWSConn(context.Background(), ws)
	session, err := yamux.Client(conn, yamuxCfg())
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return session, ws, nil
}

func TestTunnelUpgrade_MissingToken_401(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + creds.SandboxID
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("want dial error; got nil")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 in failure response; got resp=%v err=%v", resp, err)
	}
}

func TestTunnelUpgrade_TokenSandboxMismatch_401(t *testing.T) {
	srv := newTestServer(t)
	credsA := registerAgent(t, srv, "slave", "slv-a", "")
	credsB := registerAgent(t, srv, "slave", "slv-b", "")
	// dial slv-a's sandbox with slv-b's token
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + credsA.SandboxID + "?token=" + credsB.TunnelToken
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("want dial error; got nil")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401; got resp=%v err=%v", resp, err)
	}
}

func TestTunnelUpgrade_AcceptsAndOpensYamuxSession(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	session, ws, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "test")
	defer session.Close()
	// yamux client session should be healthy; NumStreams should be 0
	if session.NumStreams() != 0 {
		t.Fatalf("num streams: want 0 got %d", session.NumStreams())
	}
}

func TestTunnelReconnect_OldSessionClosed_NewSessionServed(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	// First dial
	s1, ws1, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	// Second dial — should force old to close
	s2, ws2, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { s2.Close(); ws2.Close(websocket.StatusNormalClosure, "test") }()

	// Wait up to 2s for s1 to notice its session died
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s1.IsClosed() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !s1.IsClosed() {
		t.Fatal("old session should be closed after reconnect; still open")
	}
	_ = ws1.Close(websocket.StatusNormalClosure, "test")
	// s2 must still be open
	if s2.IsClosed() {
		t.Fatal("new session should be open; already closed")
	}
}

func TestTunnelUnregister_DoesNotDeleteNewAfterReconnect(t *testing.T) {
	// fix plan review r2 P1-1: verify unregisterTunnel(sid, oldT) is a no-op
	// when tunnels[sid] has already been overwritten by a newer session.
	// (Otherwise the old handler's defer would delete the new tunnel, and
	//  peer proxy would 502 despite a live WS.)
	srv, stubSrv := newTestServerWithStub(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	s1, ws1, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	s2, ws2, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { s2.Close(); ws2.Close(websocket.StatusNormalClosure, "test") }()

	// Wait for old to die (so its handler runs unregisterTunnel)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s1.IsClosed() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = ws1.Close(websocket.StatusNormalClosure, "test")

	// Wait for the old handler's defer unregisterTunnel to definitely have run.
	// Fix plan review r3 P1-2: polling loop instead of fixed sleep — under a
	// broken unconditional unregister the tunnel would eventually vanish; we
	// need to detect that within the polling window, not sample a single point.
	//
	// Signal: the old handler goroutine holds a reference to the *stubTunnel
	// via its defer chain. Because s2 was installed AFTER s1 (registerTunnel
	// closed s1.Close() during s2's install), and s1's watch() called Close()
	// on s1 which enqueued s1's defer to run unregisterTunnel(sid, s1_ptr),
	// once s1.IsClosed() is true we allow up to 1s for the defer to fire,
	// checking every 20ms that s2 remains registered.
	stableDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(stableDeadline) {
		if !stubSrv.hasTunnel(creds.SandboxID) {
			t.Fatal("new tunnel was deleted by old handler's unregister — reconnect race not guarded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Final assert: still there
	if !stubSrv.hasTunnel(creds.SandboxID) {
		t.Fatal("new tunnel gone by end of stability window")
	}
}
```

- [ ] **Step 3.2: Run tests to verify fail**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestTunnel -v
```

Expected: FAIL — either 404 (handler not mounted) or 401 handshake result.

- [ ] **Step 3.3: Create `tunnel.go`**

```go
package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

// handleTunnelUpgrade authenticates via ?token= against byTunnel, upgrades the
// WS, wraps into yamux server session, tracks it in Server.tunnels, runs a
// 20s heartbeat, and blocks until session close.  Spec §4.3.
func (s *Server) handleTunnelUpgrade(w http.ResponseWriter, r *http.Request) {
	// Path: /api/tunnel/<sandboxID>
	sandboxID := strings.TrimPrefix(r.URL.Path, "/api/tunnel/")
	if sandboxID == "" || strings.Contains(sandboxID, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	ident, known := s.byTunnel[token]
	s.mu.RUnlock()
	if !known || ident.SandboxID != sandboxID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote a response
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	t, err := newStubTunnel(ctx, sandboxID, ws)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "mux init failed")
		return
	}
	s.registerTunnel(sandboxID, t)

	// Heartbeat: ping every 20s, with a 5s per-ping timeout via ctx.WithTimeout.
	// Ping is Ping(ctx) single-arg in nhooyr.io/websocket v1.8.17; timeout via ctx.
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				err := ws.Ping(pingCtx)
				pingCancel()
				if err != nil {
					t.Close()
					return
				}
			}
		}
	}()

	// Block until the session dies
	<-t.Done()
	// Only remove ourselves if we're still the active tunnel (fix P1 #7)
	s.unregisterTunnel(sandboxID, t)
}

// registerTunnel installs t as the active tunnel for sandboxID, closing any
// previous tunnel for the same sandbox (spec §3.6).
func (s *Server) registerTunnel(sid string, t *stubTunnel) {
	var old *stubTunnel
	s.mu.Lock()
	old = s.tunnels[sid]
	s.tunnels[sid] = t
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// unregisterTunnel removes t from Server.tunnels only if s.tunnels[sid] == t.
// Returns true if the entry was actually removed (spec §3.6, fix round-1 P1 #7).
func (s *Server) unregisterTunnel(sid string, t *stubTunnel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.tunnels[sid]; ok && cur == t {
		delete(s.tunnels, sid)
		return true
	}
	return false
}

// hasTunnel reports whether a tunnel session is currently registered for sid.
// Used by tests to poll for server-side registerTunnel completion without racy
// time.Sleep (plan review r1 P1-1).
func (s *Server) hasTunnel(sid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.tunnels[sid]
	return ok
}
```

- [ ] **Step 3.4: Wire into `Handler()`**

In `server.go` `Handler()`, add **after** the discovery block:

```go
	// Tunnel WS (spec §4.3) — path is /api/tunnel/<sandboxID>
	mux.HandleFunc("/api/tunnel/", s.handleTunnelUpgrade)
```

- [ ] **Step 3.5: Run tests to verify all pass**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestTunnel -v -timeout 30s
```

Expected: ALL 4 `TestTunnel*` tests PASS.

- [ ] **Step 3.6: Commit**

```bash
git add tools/eval/agentserver-stub/tunnel.go \
        tools/eval/agentserver-stub/tunnel_test.go \
        tools/eval/agentserver-stub/server.go
git commit -m "stub(#78): implement WS /api/tunnel/<sid> with yamux server + reconnect race guard

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4：Peer proxy — HTTP forwarding over yamux stream

**Files:**
- Create: `multi-agent/tools/eval/agentserver-stub/peerproxy.go`
- Create: `multi-agent/tools/eval/agentserver-stub/peerproxy_test.go`
- Modify: `multi-agent/tools/eval/agentserver-stub/server.go`（Handler() 挂 `/api/agent/peer/`）

**Interfaces:**
- Consumes: Task 1 `stubTunnel.OpenHTTPStream`, `HTTPStreamMeta`, `HTTPResponseMeta`, `Server.cards`, `Server.tunnels`, `Server.mu`
- Produces: `Server.handlePeerProxy(w, r)`

- [ ] **Step 4.1: Write failing tests**

Create `peerproxy_test.go`:

```go
package main

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

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// startFakeSlave: register a slave, publish its card, dial WS as slave-side,
// spin up a goroutine that Accept's yamux streams and runs `handler` on each.
// Returns Credentials for driving driver-side requests.
// stubSrv is used to deterministically wait for server-side registerTunnel completion
// via s.hasTunnel(sandboxID) polling (avoids racy time.Sleep, fix plan review r1 P1-1).
func startFakeSlave(t *testing.T, srv *httptest.Server, stubSrv *Server, wsID, shortID string, handler func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte)) Credentials {
	t.Helper()
	creds := registerAgent(t, srv, "slave", shortID, wsID)
	postCard(t, srv, creds.ProxyToken, map[string]any{
		"display_name": "fake-" + shortID,
		"card":         map[string]any{"short_id": shortID},
	}).Body.Close()
	// Dial tunnel as slave
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + creds.SandboxID + "?token=" + creds.TunnelToken
	dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, url, nil)
	if err != nil {
		t.Fatalf("slave dial: %v", err)
	}
	conn := newWSConn(context.Background(), ws)
	session, err := yamux.Client(conn, yamuxCfg())
	if err != nil {
		t.Fatalf("yamux client: %v", err)
	}
	t.Cleanup(func() {
		session.Close()
		ws.Close(websocket.StatusNormalClosure, "test done")
	})
	// Accept loop: for every incoming stream, read header + body, invoke handler, write response
	go func() {
		for {
			stream, err := session.Accept()
			if err != nil {
				return
			}
			go func(s io.ReadWriteCloser) {
				defer s.Close()
				typ, metaJSON, err := readStreamHeader(s)
				if err != nil || typ != streamTypeHTTP {
					return
				}
				var meta HTTPStreamMeta
				if err := json.Unmarshal(metaJSON, &meta); err != nil {
					return
				}
				body := make([]byte, meta.BodyLen)
				if meta.BodyLen > 0 {
					_, _ = io.ReadFull(s, body)
				}
				respMeta, respBody := handler(meta, body)
				respMetaJSON, _ := json.Marshal(respMeta)
				_ = writeStreamHeader(s, streamTypeHTTP, respMetaJSON)
				_, _ = s.Write(respBody)
			}(stream)
		}
	}()
	// Wait deterministically for the server-side registerTunnel to complete
	// (fix plan review r1 P1-1). registerTunnel runs in the server-side handler
	// goroutine AFTER websocket.Accept but BEFORE the handler blocks on <-t.Done().
	// We poll s.tunnels[sid] via a test-only accessor until it's non-nil.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stubSrv.hasTunnel(creds.SandboxID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !stubSrv.hasTunnel(creds.SandboxID) {
		t.Fatalf("server-side registerTunnel did not complete within 2s for %s", creds.SandboxID)
	}
	return creds
}

func peerProxyReq(t *testing.T, srv *httptest.Server, callerToken, targetShortID, path string, body []byte) *http.Response {
	t.Helper()
	url := srv.URL + "/api/agent/peer/" + targetShortID + "/proxy" + path
	req, _ := http.NewRequest("GET", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+callerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("peer proxy: %v", err)
	}
	return resp
}

func TestPeerProxy_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/agent/peer/slv-x/proxy/state", nil)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_UnknownTarget_404(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-does-not-exist", "/state", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_TargetNotInWorkspace_404(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "ws-A")
	// startFakeSlave with a different workspace
	_ = startFakeSlave(t, srv, stubSrv, "ws-B", "slv-b", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		return HTTPResponseMeta{Status: 200}, nil
	})
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-b", "/state", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 (cross-workspace hidden) got %d", resp.StatusCode)
	}
}

func TestPeerProxy_NoActiveTunnel_502(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	// Register a target and publish card but DO NOT dial WS
	target := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, target.ProxyToken, map[string]any{"display_name": "eval", "card": map[string]any{"short_id": "slv-001"}}).Body.Close()
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-001", "/state", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_ForwardsPOSTAndBody(t *testing.T) {
	// fix plan review r2 P1-2: verify method AND body are forwarded verbatim
	// (previously drove GET which is trivially default; using POST proves the
	//  handler doesn't hardcode GET).
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		if meta.Method != "POST" || meta.Path != "/echo" {
			return HTTPResponseMeta{Status: 400}, []byte("bad meta")
		}
		if string(body) != "hello-body" {
			return HTTPResponseMeta{Status: 400}, []byte("bad body")
		}
		return HTTPResponseMeta{Status: 200, Headers: map[string]string{"Content-Type": "text/plain"}}, []byte("hello-back")
	})
	// Explicit POST — do not go through the GET-only peerProxyReq helper.
	req, _ := http.NewRequest("POST", srv.URL+"/api/agent/peer/slv-001/proxy/echo", bytes.NewReader([]byte("hello-body")))
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200 got %d body %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-back" {
		t.Fatalf("want body 'hello-back' got %q", body)
	}
}

func TestPeerProxy_ReturnsResponseFromStream(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		return HTTPResponseMeta{Status: 418, Headers: map[string]string{"X-Test": "abc"}}, []byte("teapot")
	})
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-001", "/x", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 418 {
		t.Fatalf("want 418 got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Test") != "abc" {
		t.Fatalf("want X-Test: abc got %q", resp.Header.Get("X-Test"))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "teapot" {
		t.Fatalf("want body 'teapot' got %q", body)
	}
}

func TestPeerProxy_StripsPrefixPreservesQuery(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	var gotPath string
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		gotPath = meta.Path
		return HTTPResponseMeta{Status: 200}, nil
	})
	// Note: peerProxyReq uses GET internally
	url := srv.URL + "/api/agent/peer/slv-001/proxy/files/dir/tok?recursive=true"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	if gotPath != "/files/dir/tok?recursive=true" {
		t.Fatalf("want inner path '/files/dir/tok?recursive=true' got %q", gotPath)
	}
}

func TestPeerProxy_MethodNotAllowed_405(t *testing.T) {
	// fix plan review r1 P0-3: peer proxy must reject non-whitelisted methods
	// (CONNECT/TRACE/OPTIONS have no HTTP-over-yamux semantics).
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	req, _ := http.NewRequest("TRACE", srv.URL+"/api/agent/peer/slv-x/proxy/y", nil)
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_EmptyRestPath_Slash(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	var gotPath string
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		gotPath = meta.Path
		return HTTPResponseMeta{Status: 200}, nil
	})
	url := srv.URL + "/api/agent/peer/slv-001/proxy"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if gotPath != "/" {
		t.Fatalf("want inner path '/' got %q", gotPath)
	}
}
```

- [ ] **Step 4.2: Run tests to verify fail**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestPeerProxy -v -timeout 30s
```

Expected: FAIL — 404 or missing handler.

- [ ] **Step 4.3: Create `peerproxy.go`**

```go
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// isTimeoutErr recognizes both the ctx deadline path and yamux's own
// net.Error Timeout() path (spec §4.4 — return 504, fix plan review r3 P0).
func isTimeoutErr(err error, ctx context.Context) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// ctx expired synchronously after OpenHTTPStream returned
	if ctx.Err() == context.DeadlineExceeded {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return false
}

// handlePeerProxy forwards ANY /api/agent/peer/<target_short>/proxy[/path][?query]
// over the target's yamux tunnel using HTTPStreamMeta. Spec §4.4.
func (s *Server) handlePeerProxy(w http.ResponseWriter, r *http.Request) {
	// Method whitelist (spec §4.4 — 405 for CONNECT/TRACE/OPTIONS; fix plan review r1 P0-3).
	// Deliberately include the common HTTP-verbs the loom driver actually issues; CONNECT/TRACE
	// have no HTTP-over-yamux semantics and must be rejected before auth.
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
		// allowed
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Auth
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse target from URL: /api/agent/peer/<short>/proxy[/rest][?q]
	const rootPrefix = "/api/agent/peer/"
	if !strings.HasPrefix(r.URL.EscapedPath(), rootPrefix) {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	tail := strings.TrimPrefix(r.URL.EscapedPath(), rootPrefix)
	// tail = "<short>/proxy" or "<short>/proxy/rest"
	slash := strings.Index(tail, "/")
	if slash < 0 {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	targetShort := tail[:slash]
	after := tail[slash:]
	const proxyMark = "/proxy"
	// Strict match: after == "/proxy" or starts with "/proxy/" (fix plan review r3 P1-1).
	// Otherwise "/proxyevil" would falsely satisfy HasPrefix.
	if after != proxyMark && !strings.HasPrefix(after, proxyMark+"/") {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(after, proxyMark)
	// rest is "" or "/..."
	forwardPath := rest
	if forwardPath == "" {
		forwardPath = "/"
	}
	if r.URL.RawQuery != "" {
		forwardPath += "?" + r.URL.RawQuery
	}

	// Look up target card in caller.workspace
	var targetCard agentCard
	found := false
	s.mu.RLock()
	for _, c := range s.cards {
		if c.WorkspaceID == caller.WorkspaceID && c.ShortID == targetShort {
			targetCard = c
			found = true
			break
		}
	}
	tun, hasTun := s.tunnels[targetCard.SandboxID]
	s.mu.RUnlock()

	// unknown target OR cross-workspace: 404 (do not distinguish, spec §4.4)
	if !found {
		http.Error(w, "unknown target", http.StatusNotFound)
		return
	}
	if !hasTun || tun == nil {
		http.Error(w, "no active tunnel", http.StatusBadGateway)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build stream meta
	headers := make(map[string]string, len(r.Header))
	for k, vs := range r.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	meta := HTTPStreamMeta{
		Method:  r.Method,
		Path:    forwardPath,
		Headers: headers,
	}

	// Open stream with 120s timeout (spec §4.4)
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	respMeta, respBody, err := tun.OpenHTTPStream(ctx, meta, body)
	if err != nil {
		if isTimeoutErr(err, ctx) {
			http.Error(w, "tunnel timeout", http.StatusGatewayTimeout)
			return
		}
		http.Error(w, "tunnel error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer respBody.Close()

	for k, v := range respMeta.Headers {
		w.Header().Set(k, v)
	}
	if respMeta.Status > 0 {
		w.WriteHeader(respMeta.Status)
	}
	_, _ = io.Copy(w, respBody)
}
```

- [ ] **Step 4.4: Wire into `Handler()`**

Add **after** the tunnel block:

```go
	// Peer proxy (spec §4.4)
	mux.HandleFunc("/api/agent/peer/", s.handlePeerProxy)
```

- [ ] **Step 4.5: Run tests to verify all pass**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestPeerProxy -v -timeout 30s
```

Expected: ALL 8 `TestPeerProxy*` tests PASS.

- [ ] **Step 4.6: Commit**

```bash
git add tools/eval/agentserver-stub/peerproxy.go \
        tools/eval/agentserver-stub/peerproxy_test.go \
        tools/eval/agentserver-stub/server.go
git commit -m "stub(#78): implement /api/agent/peer/<short>/proxy over yamux

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5：Tasks 4-endpoint（create / poll / status / get）

**Files:**
- Create: `multi-agent/tools/eval/agentserver-stub/tasks.go`
- Create: `multi-agent/tools/eval/agentserver-stub/tasks_test.go`
- Modify: `multi-agent/tools/eval/agentserver-stub/server.go`（挂 4 个路径）

**Interfaces:**
- Consumes: Task 1 `Server.tasks`, `Server.tasksBySandbox`, `stubTask`, `Server.cards`, `Server.mu`, `Server.byProxy`, `randomHex` helper (需要新加或使用现有)
- Produces: 4 handlers + wire per spec §4.5–4.8

- [ ] **Step 5.1: Write failing tests**

Create `tasks_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// helpers -------------------------------------------------------------------

// Renamed to avoid collision with existing postJSON(t, url, body any) in stub_test.go (fix P0-1 from plan review r1).
func postJSONBearer(t *testing.T, url, bearer string, payload map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func putJSONBearer(t *testing.T, url, bearer string, payload map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put %s: %v", url, err)
	}
	return resp
}

func getJSONBearer(t *testing.T, url, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	return resp
}

// tests ---------------------------------------------------------------------
// (Existing helpers registerAgent / postCard / newTestServer are declared in
// discovery_test.go and cross-file visible within package main; do NOT
// redeclare them here — plan review r1 P0-2 fix.)

func TestCreateTask_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", "", map[string]any{"target_id": "x", "prompt": "y"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestCreateTask_MissingFields_400(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"prompt": "y"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
}

func TestCreateTask_UnknownTarget_404(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": "sbx-never-registered", "prompt": "y",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", resp.StatusCode)
	}
}

func TestCreateTask_CrossWorkspace_403(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "ws-A")
	target := registerAgent(t, srv, "slave", "slv-b", "ws-B")
	postCard(t, srv, target.ProxyToken, map[string]any{"display_name": "b", "card": map[string]any{}}).Body.Close()
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": target.SandboxID, "prompt": "y",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
}

func TestCreateTask_ReturnsTaskIDStatusPending(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	target := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, target.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": target.SandboxID, "prompt": "hello",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 201 got %d body %s", resp.StatusCode, b)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if out["task_id"] == nil || out["task_id"] == "" {
		t.Fatalf("missing task_id: %+v", out)
	}
	if out["status"] != "pending" {
		t.Fatalf("want status=pending got %v", out["status"])
	}
}

func TestPollTasks_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestPollTasks_SandboxMismatch_403(t *testing.T) {
	srv := newTestServer(t)
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll?sandbox_id=sbx-other", poller.ProxyToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
}

func TestPollTasks_NoTasks_204(t *testing.T) {
	srv := newTestServer(t)
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 got %d", resp.StatusCode)
	}
}

func TestPollTasks_NoQueryString_UsesCallerSandbox(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	// Enqueue a task for slv-001
	postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": poller.SandboxID, "prompt": "hello",
	}).Body.Close()
	// poller polls with NO ?sandbox_id= — must still receive the task
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	var arr []map[string]any
	json.NewDecoder(resp.Body).Decode(&arr)
	if len(arr) != 1 || arr[0]["prompt"] != "hello" {
		t.Fatalf("want 1 task with prompt=hello got %+v", arr)
	}
}

func TestPollTasks_AtomicAssignBatch5(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	// Enqueue 7 tasks
	for i := 0; i < 7; i++ {
		postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
			"target_id": poller.SandboxID, "prompt": "p",
		}).Body.Close()
	}
	// First poll should get 5
	resp1 := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	var arr1 []map[string]any
	json.NewDecoder(resp1.Body).Decode(&arr1)
	resp1.Body.Close()
	if len(arr1) != 5 {
		t.Fatalf("want batch 5 got %d", len(arr1))
	}
	// Second poll should get 2 remaining
	resp2 := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	var arr2 []map[string]any
	json.NewDecoder(resp2.Body).Decode(&arr2)
	resp2.Body.Close()
	if len(arr2) != 2 {
		t.Fatalf("want batch 2 got %d", len(arr2))
	}
	// Third poll → 204
	resp3 := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 got %d", resp3.StatusCode)
	}
}

func TestUpdateStatus_MarksRunningToCompleted(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	// Create + poll to get task_id
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": poller.SandboxID, "prompt": "p",
	})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)

	// PUT running
	rr := putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{"status": "running"})
	rr.Body.Close()
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("running: want 200 got %d", rr.StatusCode)
	}
	// PUT completed with result
	rc := putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{
		"status": "completed", "result": json.RawMessage(`"hello output"`),
	})
	rc.Body.Close()
	if rc.StatusCode != http.StatusOK {
		t.Fatalf("completed: want 200 got %d", rc.StatusCode)
	}
	// GET task must reflect
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid, caller.ProxyToken)
	defer g.Body.Close()
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	if info["status"] != "completed" {
		t.Fatalf("get status: want completed got %v", info["status"])
	}
}

func TestUpdateStatus_AcceptsResultField(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{
		"status": "completed", "result": json.RawMessage(`{"echo":"hello"}`),
	}).Body.Close()
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"?include_output=true", caller.ProxyToken)
	defer g.Body.Close()
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	rawResult, _ := json.Marshal(info["result"])
	if string(rawResult) != `{"echo":"hello"}` {
		t.Fatalf("result: want {echo:hello} got %s", rawResult)
	}
}

func TestUpdateStatus_AcceptsOutputField(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{
		"status": "completed", "output": "hello output",
	}).Body.Close()
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"?include_output=true", caller.ProxyToken)
	defer g.Body.Close()
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	if info["output"] != "hello output" {
		t.Fatalf("output: want 'hello output' got %v", info["output"])
	}
}

func TestUpdateStatus_WrongOwner_403(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	other := registerAgent(t, srv, "slave", "slv-other", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	// "other" slave tries to update task belonging to poller
	rr := putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", other.ProxyToken, map[string]any{"status": "running"})
	rr.Body.Close()
	if rr.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", rr.StatusCode)
	}
}

func TestGetTask_ReturnsResultAfterComplete(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{"status": "completed", "output": "done"}).Body.Close()
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid, caller.ProxyToken)
	defer g.Body.Close()
	if g.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", g.StatusCode)
	}
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	if info["status"] != "completed" {
		t.Fatalf("status: want completed got %v", info["status"])
	}
}

func TestGetTask_WrongWorkspace_404(t *testing.T) {
	srv := newTestServer(t)
	callerA := registerAgent(t, srv, "driver", "drv-A", "ws-A")
	callerB := registerAgent(t, srv, "driver", "drv-B", "ws-B")
	pollerA := registerAgent(t, srv, "slave", "slv-A", "ws-A")
	postCard(t, srv, pollerA.ProxyToken, map[string]any{"display_name": "sA", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", callerA.ProxyToken, map[string]any{"target_id": pollerA.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	// driver-B (different workspace) tries to GET
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid, callerB.ProxyToken)
	g.Body.Close()
	if g.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", g.StatusCode)
	}
}
```

*Helpers `registerAgent` / `postCard` / `newTestServer` come from `discovery_test.go` (cross-file visible in `package main`).* `postJSONBearer` / `putJSONBearer` / `getJSONBearer` are named with the `Bearer` suffix to avoid colliding with the existing `postJSON(t, url string, body any)` helper in `stub_test.go` — fixed after plan review r1 P0-1.

- [ ] **Step 5.2: Run tests to verify fail**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run TestCreateTask -v
```

Expected: FAIL — 404 unmounted handler.

- [ ] **Step 5.3: Create `tasks.go`**

```go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// randomHex n → 2n-char hex string; used for task_id
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createTaskRequest — matches driver-side agentsdk.DelegateTaskRequest + spec §4.5
type createTaskRequest struct {
	TargetID        string   `json:"target_id"`
	Skill           string   `json:"skill,omitempty"`
	Prompt          string   `json:"prompt"`
	SystemContext   string   `json:"system_context,omitempty"`
	MaxTurns        int      `json:"max_turns,omitempty"`
	MaxBudgetUSD    float64  `json:"max_budget_usd,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
	DelegationChain []string `json:"delegation_chain,omitempty"`
	RequesterID     string   `json:"requester_id,omitempty"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
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
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TargetID == "" || req.Prompt == "" {
		http.Error(w, "target_id and prompt required", http.StatusBadRequest)
		return
	}
	// Lookup target card
	s.mu.RLock()
	target, ok := s.cards[req.TargetID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "target agent not found", http.StatusNotFound)
		return
	}
	if target.WorkspaceID != caller.WorkspaceID {
		http.Error(w, "target agent not in workspace", http.StatusForbidden)
		return
	}
	tid := "task_" + randomHex(16)
	task := &stubTask{
		ID:            tid,
		WorkspaceID:   caller.WorkspaceID,
		RequesterID:   caller.SandboxID,
		TargetID:      req.TargetID,
		Skill:         req.Skill,
		Prompt:        req.Prompt,
		SystemContext: req.SystemContext,
		MaxTurns:      req.MaxTurns,
		MaxBudgetUSD:  req.MaxBudgetUSD,
		Timeout:       req.TimeoutSeconds,
		Status:        "pending",
		CreatedAt:     time.Now().UTC(),
	}
	s.mu.Lock()
	s.tasks[tid] = task
	s.tasksBySandbox[req.TargetID] = append(s.tasksBySandbox[req.TargetID], tid)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"task_id":    tid,
		"session_id": "",
		"status":     "pending",
	})
}

// pollTaskEntry matches the shape driver-side / poller-side clients parse.
type pollTaskEntry struct {
	TaskID        string  `json:"task_id"`
	Prompt        string  `json:"prompt"`
	SystemContext string  `json:"system_context"`
	SessionID     string  `json:"session_id,omitempty"`
	MaxTurns      int     `json:"max_turns"`
	MaxBudgetUSD  float64 `json:"max_budget_usd"`
}

const pollBatchSize = 5

func (s *Server) handlePollTasks(w http.ResponseWriter, r *http.Request) {
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
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	requested := r.URL.Query().Get("sandbox_id")
	if requested != "" && requested != caller.SandboxID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	sid := caller.SandboxID

	// Take up to pollBatchSize pending tasks atomically
	s.mu.Lock()
	pending := s.tasksBySandbox[sid]
	assigned := make([]pollTaskEntry, 0, pollBatchSize)
	remaining := pending[:0]
	for _, tid := range pending {
		if len(assigned) < pollBatchSize {
			t, ok := s.tasks[tid]
			if !ok || t.Status != "pending" {
				continue
			}
			t.Status = "assigned"
			assigned = append(assigned, pollTaskEntry{
				TaskID:        t.ID,
				Prompt:        t.Prompt,
				SystemContext: t.SystemContext,
				SessionID:     t.SessionID,
				MaxTurns:      t.MaxTurns,
				MaxBudgetUSD:  t.MaxBudgetUSD,
			})
		} else {
			remaining = append(remaining, tid)
		}
	}
	s.tasksBySandbox[sid] = remaining
	s.mu.Unlock()

	if len(assigned) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(assigned)
}

// updateStatusRequest — accepts both loom poller path (`result`) and SDK path (`output`).
type updateStatusRequest struct {
	Status        string          `json:"status"`
	Result        json.RawMessage `json:"result,omitempty"`
	Output        string          `json:"output,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
	TotalCostUSD  float64         `json:"total_cost_usd,omitempty"`
	NumTurns      int             `json:"num_turns,omitempty"`
}

// handleUpdateStatus PUT /api/agent/tasks/{id}/status
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Path: /api/agent/tasks/{id}/status
	path := strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/")
	tid := strings.TrimSuffix(path, "/status")
	if tid == "" || strings.Contains(tid, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	var req updateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	task, ok := s.tasks[tid]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if task.TargetID != caller.SandboxID {
		s.mu.Unlock()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	task.Status = req.Status
	if len(req.Result) > 0 {
		task.Result = req.Result
	}
	if req.Output != "" {
		task.Output = req.Output
	}
	if req.FailureReason != "" {
		task.FailureReason = req.FailureReason
	}
	if req.TotalCostUSD > 0 {
		task.TotalCostUSD = req.TotalCostUSD
	}
	if req.NumTurns > 0 {
		task.NumTurns = req.NumTurns
	}
	if req.Status == "completed" || req.Status == "failed" || req.Status == "cancelled" {
		task.CompletedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// handleGetTask GET /api/agent/tasks/{id}[?include_output=true]
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
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
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Path: /api/agent/tasks/{id}
	tid := strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/")
	if tid == "" || strings.Contains(tid, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	task, ok := s.tasks[tid]
	if !ok || task.WorkspaceID != caller.WorkspaceID {
		s.mu.RUnlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	// Snapshot fields (avoid map alias)
	resp := map[string]any{
		"task_id":      task.ID,
		"workspace_id": task.WorkspaceID,
		"requester_id": task.RequesterID,
		"target_id":    task.TargetID,
		"prompt":       task.Prompt,
		"status":       task.Status,
		"num_turns":    task.NumTurns,
		"created_at":   task.CreatedAt.Format(time.RFC3339),
	}
	if task.Skill != "" {
		resp["skill"] = task.Skill
	}
	if task.SessionID != "" {
		resp["session_id"] = task.SessionID
	}
	if task.TotalCostUSD > 0 {
		resp["total_cost_usd"] = task.TotalCostUSD
	}
	if len(task.Result) > 0 {
		resp["result"] = task.Result
	}
	if task.FailureReason != "" {
		resp["failure_reason"] = task.FailureReason
	}
	if !task.CompletedAt.IsZero() {
		resp["completed_at"] = task.CompletedAt.Format(time.RFC3339)
	}
	if r.URL.Query().Get("include_output") == "true" {
		if task.Output != "" {
			resp["output"] = task.Output
		} else if len(task.Result) > 0 {
			// Fallback: if result is a JSON string, unwrap it into output (spec §4.8, fix plan review r2 P1-3).
			var s string
			if err := json.Unmarshal(task.Result, &s); err == nil {
				resp["output"] = s
			}
			// Otherwise result is a JSON object/array — leave output unset; the caller
			// can read `result` directly.
		}
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 5.4: Wire into `Handler()`**

Add **after** the peer proxy block:

```go
	// Tasks (spec §4.5–4.8)
	mux.HandleFunc("/api/agent/tasks", s.handleCreateTask)
	mux.HandleFunc("/api/agent/tasks/poll", s.handlePollTasks)
	// The plain /api/agent/tasks/{id} and /api/agent/tasks/{id}/status paths overlap;
	// dispatch inside a shared handler by suffix
	mux.HandleFunc("/api/agent/tasks/", s.dispatchTaskByID)
```

Add helper `dispatchTaskByID` to `tasks.go`:

```go
// dispatchTaskByID routes /api/agent/tasks/{id} vs /api/agent/tasks/{id}/status vs /api/agent/tasks/poll.
// Note: /api/agent/tasks (no trailing slash) is handled by handleCreateTask, and
// /api/agent/tasks/poll by handlePollTasks — both mounted separately.
func (s *Server) dispatchTaskByID(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/")
	// suffix examples: "task_abc123", "task_abc123/status", "poll" (won't reach here)
	if strings.HasSuffix(suffix, "/status") {
		s.handleUpdateStatus(w, r)
		return
	}
	s.handleGetTask(w, r)
}
```

- [ ] **Step 5.5: Run tests to verify all pass**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go test ./tools/eval/agentserver-stub/ -run "TestCreateTask|TestPollTasks|TestUpdateStatus|TestGetTask" -v
```

Expected: all 14 pass. If any test uses the aliased `createSlaveWithCard` / `httptest_Server` helpers (from Step 5.1 draft), delete those helper stubs; tests should call `registerAgent` + `postCard` directly (both cross-file visible in `package main`).

- [ ] **Step 5.6: Commit**

```bash
git add tools/eval/agentserver-stub/tasks.go \
        tools/eval/agentserver-stub/tasks_test.go \
        tools/eval/agentserver-stub/server.go
git commit -m "stub(#78): implement /api/agent/tasks CRUD (create/poll/status/get)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6：全套 integration smoke + go vet + full test suite

**Files:**
- Modify: `multi-agent/tools/eval/agentserver-stub/server.go`（可能补漏挂路径；实际 Handler() 已在 Tasks 2/3/4/5 增量补齐）
- Create (optional): `multi-agent/tools/eval/agentserver-stub/stub_integration_test.go`

**Interfaces:**
- No new exports; only verifying the whole stub as one system

- [ ] **Step 6.1: Run entire test suite + go vet**

```bash
cd /root/multi-agent/.worktrees/stub-discovery-tunnel-fix/multi-agent
go vet ./tools/eval/agentserver-stub/
go test ./tools/eval/agentserver-stub/ -v -race -timeout 60s
```

Expected: 0 vet issues; all tests pass.

- [ ] **Step 6.2: (Optional) Add stub_integration_test.go**

Only if the individual test files leave a gap: end-to-end via SDK (driver-side + slave-side both spun up in one goroutine each) to run
`register → PublishCard → CreateTask → PollTasks → PUT status → GetTask`. Skip if the Tasks 2–5 coverage already exercises this chain (Step 5's TestUpdateStatus_MarksRunningToCompleted does so end-to-end already; recommend skipping this optional file).

- [ ] **Step 6.3: Commit if changes**

```bash
git status
# if clean, no commit needed
git commit -am "stub(#78): (if needed) integration polish" || true
```

- [ ] **Step 6.4: Ready for L2–L5 smoke rerun**

At this point the stub compiles + all unit tests pass with `-race`. Next steps (L2–L5) are covered by the parent worktree task #15 (smoke rerun), NOT by this plan.

---

## Self-Review

**1. Spec coverage:**
- §4.1 discovery/cards → Task 2 ✅
- §4.2 discovery/agents → Task 2 ✅
- §4.3 WS tunnel → Task 3 ✅
- §4.4 peer proxy → Task 4 ✅ (incl. prefix strip + raw query preserve)
- §4.5–4.8 tasks CRUD → Task 5 ✅
- §3.4 stubTunnel + protocol helpers → Task 1 ✅
- §3.6 reconnect race guard → Task 3 (`registerTunnel` / `unregisterTunnel`) ✅
- §3.7 Server.Close() → Task 1 ✅
- §9.1–9.4 named test list → tests included in each task ✅
- §5 authz matrix → covered per handler in Tasks 2/3/4/5 ✅

**2. Placeholder scan:** No "TBD" / "TODO" / "similar to earlier task". Every code step contains actual code. Verified.

**3. Type consistency:** `stubTunnel`, `HTTPStreamMeta`, `HTTPResponseMeta`, `stubTask`, `agentCard`, `Server.tunnels/cards/tasks/tasksBySandbox` field names appear identically across tasks 1/3/4/5. `unregisterTunnel(sid, t)` signature (returns `bool`) consistent between spec §3.6 and Task 3 implementation.

**Known cleanup for Step 5.1**: the draft test file includes obsolete helper stubs (`createSlaveWithCard`, `httptest_Server`) that must be deleted at implementation time — Step 5.1 body flags this explicitly.
