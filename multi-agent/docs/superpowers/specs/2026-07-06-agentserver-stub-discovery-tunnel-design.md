# Design — agentserver-stub 补齐 discovery + tunnel + peer proxy + tasks 端点（完整 dispatch scope）

**Date**: 2026-07-06
**Owner**: paper/v3/stub-discovery-tunnel-fix worktree
**Base**: multi-agent `origin/paper/v3-integration` @ `1748802`
**Fixes**: [agentserver/loom#78](https://github.com/agentserver/loom/issues/78)
**Related evidence**: paper_writing `docs/intermediate/15_smoke_e2e_2slave_finding.md`
**Revision**: v2 — 应 codex round-1 审核补齐 6 P0 + 2 P1；scope 扩到"完整 dispatch"（新增 task poll 全套 + yamux HTTPStreamMeta 协议）。

---

## 1. 目标与非目标

### 1.1 目标

修 issue #78 —— 让 `deploy/linux/deploy.sh --stub` 起的栈能在无 OAuth、纯 loopback 环境下真正跑通完整 driver→slave dispatch，包括真数据面 HTTP-over-yamux：
- driver 侧 `inspect_capabilities` / `dry_run_contract` / `submit_contract_task` / `wait_task` 都不再 404
- slave 侧 `internal/tunnel` 的 `PublishCard` + WS 反连接得上并保活
- driver 通过 `PeerProxy` (`/api/agent/peer/<sid>/proxy/<path>`) 打到 slave 的 HTTP handler，数据面走 yamux + `HTTPStreamMeta` 协议
- driver 侧 `sdk.DelegateTask` → stub 记 task → slave 侧 `internal/poller` GET poll → 拿到 → dispatcher.Run 跑 → PUT status（`result` 字段）→ driver GetTask 拉回结果
- Phase 3 三个"harness-only"worktree 具备真跑基础

### 1.2 非目标（明确不做）

| 项 | 原因 |
|---|---|
| Device-code OAuth (`/api/oauth2/device/*`) | stub 自设计起就不做；five-tuple 由 `register` 直接签发 |
| Mailbox `/api/agent/mailbox/*` | 未被 driver/slave dispatch 路径调用 |
| Task cancel (`POST /api/tasks/{id}/cancel`) | smoke 不用；`AgentSDKClient.CancelTask` 未在本次链路里 |
| 多进程 / 集群 / 持久化 | stub 是 single-process in-memory smoke 工具 |
| 通用 `/api/agent/discovery/interactions` 或 `/api/workspaces/{wid}/...` 管理面 | driver/slave 不调 |
| `session_id` 复用逻辑（chat_resume） | poll 返 `session_id` 字段为空即可 |
| Terminal stream (`StreamTypeTerminal`) | 仅 HTTP stream 需要（driver 只用 PeerProxy） |
| Slave 端 tunnel 数据面接收侧 | slave 自己实现；stub 只作 server 侧 |
| 真实 rate-limit / TLS / auth token 反注入等生产语义 | smoke loopback 无威胁面 |

### 1.3 验收 = 5 层依次卡

| # | 层 | 命令 / 观测点 | 通过标准 |
|---|---|---|---|
| L1 | Go 单测 | `cd tools/eval/agentserver-stub && go test ./...` | 全绿；具名覆盖详见 §11.1 |
| L2 | stack 起来后 slave 日志 | `deploy.sh --stub --loom-home /tmp/loom-smoke && tail -f $LOOM_HOME/logs/slave.log`（≥30s 观察） | **无** `404 page not found`、无 `tunnel disconnected: ...got 404` 指数退避。首行应出现 `tunnel connected` / `publish card: 200` 类正字段 |
| L3 | discovery | codex exec 调 `smoke-driver.inspect_capabilities` | 返 ≥1 台 slave（`deploy.sh --stub` 默认起 `eval-slave` / `slv-eval-001`；若手工加起 `eval-slave-node` 则 2 台。**验收判据：`.slaves \| length` ≥ 1 且 `.slaves[0].display_name == "eval-slave"`**） |
| L4 | dry-run + observer 写入 | codex 调 `dry_run_contract`（传 discovered slave 的 snapshot）+ `sqlite3 $LOOM_HOME/observer/observer.db 'SELECT count(distinct hash) FROM capability_snapshots'` | count ≥ 1（若加起 slave-node 且各调一次 dry_run_contract 则 count == 2） |
| L5 | dispatch 数据面 | codex 调 `submit_contract_task` 显式带 `target_display_name=eval-slave`，skill=`bash`，prompt=`echo hello`；随后 `wait_task` 或 `get_task`；等待 ≤60s | 返回状态 `completed` 且 `result` / `output` 字段包含 `hello` |

**若 L5 撞出非 stub 侧 bug**（如 codex bin 找不到、planner 需要额外 wire、observer schema drift 等），本 PR 到 L4 收尾并落一个 follow-up issue；不吞 scope。stub 侧代码质量以 L1/L2 通过 + `go test` 全绿为闸。

---

## 2. 现状（stub 已有基础）

`tools/eval/agentserver-stub/server.go` @ base `1748802`（180 行 single-file）：

- `Server` 已有两张 in-memory map：
  - `byProxy: map[proxy_token]whoamiResponse`（`register` 时写、`whoami/heartbeat` 时读）
  - `byTunnel: map[tunnel_token]whoamiResponse`（`register` 时写；注释说"kept for future"—— 本设计正好用它）
- `whoamiResponse{user_id, workspace_id, workspace_name, sandbox_id, short_id, role}` — 身份字段现成
- `deriveToken(secret, ...)` HMAC 派生 - 纯函数
- Handler 已挂 `/api/v1/agents/{register,whoami,heartbeat}` + legacy alias `/api/agent/{...}`
- `bearerToken(header)` helper 已有
- 测试 `stub_test.go` 走 `httptest.NewServer` 模式

**依赖已在 go.mod**（无需 `go get`）：
- `nhooyr.io/websocket v1.8.17`
- `github.com/hashicorp/yamux v0.1.2`
- `github.com/agentserver/agentserver v0.69.9`（其 `pkg/agentsdk` 已用来定 wire 契约参考）

### 2.1 复用 vs 复制协议 helper 的抉择

真 agentserver 的 `internal/tunnel/{stream,mux,wsconn,registry}.go`（合计 433 行）是 `internal/`——不能外部 import。**选择：复制到 stub 内部** `tunnel_transport.go`（合并 4 文件为一个新文件），加原始来源注释 + 字节级对齐 `HTTPStreamMeta` / `StreamHeader` wire 格式，保证 slave 侧 `internal/tunnel` (loom 侧的) 反向解 stream 时**字节对齐**。**不做行为变更**——只是"搬进 stub 目录避免 internal 屏障"。

---

## 3. 架构

### 3.1 文件拆分

| 新/改文件 | 内容 | 行数估计 |
|---|---|---|
| `server.go` (改) | 新增字段到 `Server` struct；`Handler()` 挂新路径；`Close()` 关 tunnels | ~+60 |
| `tunnel_transport.go` (新) | 复制 agentserver `internal/tunnel/*.go` 精简版：`WriteStreamHeader` / `ReadStreamHeader` / `HTTPStreamMeta` / `HTTPResponseMeta` / `ServerMux` / `WSConn`（`net.Conn` 桥）/ `Tunnel` 类型 + `OpenHTTPStream`；带 SOURCE 注释 | ~440 |
| `discovery.go` (新) | `handleDiscoveryCards` (POST) + `handleDiscoveryAgents` (GET) + `agentCard` 内部结构 + `Server.cards` map ops | ~140 |
| `tunnel.go` (新) | `handleTunnelUpgrade` WS handler（用 tunnel_transport.go 类型 + `Server.tunnels` map + heartbeat pong） | ~150 |
| `peerproxy.go` (新) | `handlePeerProxy` (ANY `/api/agent/peer/<short_id>/proxy/<rest>`) — 反查 sandbox_id → tunnel → `OpenHTTPStream` → 双向 pipe | ~180 |
| `tasks.go` (新) | 4 端点：`handleCreateTask` / `handlePollTasks` / `handleUpdateTaskStatus` / `handleGetTask` + `Server.tasks` map | ~230 |
| `discovery_test.go` / `tunnel_test.go` / `peerproxy_test.go` / `tasks_test.go` (新) | 单测按文件对齐 | ~600 合计 |
| `stub_test.go` (不动) | 现有 test 只测 register/whoami/heartbeat | 0 |

### 3.2 Server 新增字段

```go
type Server struct {
    // 现有
    secret           string
    defaultWorkspace string
    mu               sync.RWMutex
    byProxy          map[string]whoamiResponse
    byTunnel         map[string]whoamiResponse

    // 新增
    cards   map[string]agentCard      // key = sandbox_id
    tunnels map[string]*stubTunnel    // key = sandbox_id; value = active server-side tunnel session
    tasks   map[string]*stubTask      // key = task_id
    tasksBySandbox map[string][]string // key = target sandbox_id; value = task_id list（poll 用；pending 状态下才排队）
}
```

**并发**：所有 6 张 map 共享 `s.mu` (RWMutex)——单锁保序、便审。热路径全部 <10µs 内的 nano 操作。yamux session 本身 concurrent-safe，可在锁外 Open/Read/Write。

### 3.3 数据流（全链路）

```
1) slave.tunnel.PublishCard()
     POST /api/agent/discovery/cards + Bearer proxy_token
     → discovery.go: handleDiscoveryCards
     → byProxy[bearer] → identity
     → cards[identity.sandbox_id] = agentCard{...identity + body}
     → 200

2) slave.agentsdk.Connect() → WS /api/tunnel/<sandbox_id>?token=<tunnel_token>
     → tunnel.go: handleTunnelUpgrade
     → byTunnel[qs.token] → identity；identity.sandbox_id == path.sandbox_id → 401 otherwise
     → websocket.Accept → wrap WSConn → ServerMux(yamux) → &stubTunnel{...}
     → tunnels[sandbox_id] = t（reconnect 时 replace，旧 t.Close()）
     → start heartbeat goroutine（30s Ping）
     → block on t.Done() ...
     → on exit: unregister(sandbox_id, t)（仅 tunnels[sid]==t 时 delete）
       (fix P1 #7 reconnect race)

3) driver.inspect_capabilities → sdk.DiscoverAgents()
     GET /api/agent/discovery/agents + Bearer proxy_token
     → discovery.go: handleDiscoveryAgents
     → byProxy[bearer] → self.workspace_id
     → iterate cards, filter by workspace_id, project to []AgentCard
       • agent_id = card.sandbox_id  (fix P0 #3)
       • status = "available"（不看 tunnel 活跃：discovery 是 dry-run 前 pre-req，WS 可能还没接）
     → 200 JSON array

4) driver.dry_run_contract（内含 sdk.DiscoverAgents）
     无 stub 侧新接口——复用 3.

5) driver.PeerProxy(targetShortID, path, body)
     打 /api/agent/peer/<targetShortID>/proxy/<path> + Bearer proxy_token  (fix P0 #1)
     → peerproxy.go: handlePeerProxy
     → byProxy[bearer] → caller.workspace_id
     → cards 里查满足（card.workspace_id == caller.workspace_id AND card.short_id == targetShortID）的那 1 张
       (missing → **404 "unknown target"** — 目标不在 workspace 或压根未 PublishCard；不区分以免泄漏跨 workspace 存在性；fix P1 #8)
     → tunnels[target.sandbox_id] → *stubTunnel
       (missing → 502 "no active tunnel" — target 存在但 WS 未连)
     → 构造 forwarded path (fix round-3 P1-2)：
        forwardedPath := 去掉 request URL 的 `/api/agent/peer/<targetShortID>/proxy` 前缀，保留剩余 raw path
        forwardedRawQuery := 保留 request URL 的原始 raw query（不 unmarshal 再 marshal）
        forwardedPath 应通过 `r.URL.RequestURI()` 语义组装，等价于 `<path>[?<query>]`
     → tunnel.OpenHTTPStream(HTTPStreamMeta{Method, Path: forwardedPath+("?"+forwardedRawQuery if rawQuery else ""), Headers, BodyLen}, body)  (fix P0 #2)
     → 收 HTTPResponseMeta + body reader → 写回 caller ResponseWriter

6) driver.DelegateTask (submit_contract_task)
     POST /api/agent/tasks + Bearer proxy_token + JSON{target_id, prompt, skill, system_context, timeout_seconds}  (fix P0 #4)
     → tasks.go: handleCreateTask
     → byProxy[bearer] → requester.workspace_id
     → cards[req.TargetID] must exist AND target.workspace_id == requester.workspace_id
       (missing → 404 "target agent not found")
       (跨 workspace → 403 "target agent not in workspace"; 对齐真 agentserver §agent_tasks.go:63)
     → new task_id = "task_" + hex(16)
     → tasks[task_id] = &stubTask{workspace_id, requester_id, target_id, prompt, skill, ...status="pending"}
     → tasksBySandbox[target_id] = append(..., task_id)
     → 201 JSON{task_id, session_id="", status="pending"}

7) slave.poller.poll() → GET /api/agent/tasks/poll + Bearer proxy_token（loom 侧不带 sandbox_id 查询串；真 agentserver 也 default 到 caller sandbox）
     → tasks.go: handlePollTasks
     → byProxy[bearer] → poller.sandbox_id
     → 若 qs 里带了 sandbox_id 且不匹配 → 403；否则 default 到 caller.sandbox_id
     → tasksBySandbox[caller.sandbox_id] → 取 pending 里的一批（≤5）
     → for each: mark status="assigned"
     → 返回 200 JSON array of {task_id, prompt, system_context, session_id, max_turns, max_budget_usd}
     → 若空 → 204 No Content

8) slave.poller.execute() → dispatcher.Run() → putStatus PUT /api/agent/tasks/{id}/status + JSON{status:"running"}
     → tasks.go: handleUpdateTaskStatus
     → byProxy[bearer] → agent.sandbox_id；task.target_id == sandbox_id (else 403)
     → task.status = "running"
     → 200

9) slave 完成 → putStatus PUT /api/agent/tasks/{id}/status + JSON{status:"completed", result:<json.RawMessage>}
     → 同 8), 但更新 status/result/completed_at
     → 200

10) driver.wait_task / get_task → GET /api/agent/tasks/{id}?include_output=true + Bearer proxy_token
     → tasks.go: handleGetTask
     → byProxy[bearer] → caller.workspace_id
     → tasks[id] must exist AND task.workspace_id == caller.workspace_id (else 404)
     → 返回 200 JSON{task_id, workspace_id, requester_id, target_id, prompt, status, num_turns, created_at, result?, session_id?, skill?, failure_reason?, completed_at?}
```

### 3.4 stubTunnel 类型（核心，参考真 agentserver `internal/tunnel/registry.go`）

```go
type stubTunnel struct {
    sandboxID string
    mux       *yamux.Session
    wsConn    *wsConn
    done      chan struct{}
    closeOnce sync.Once
}

func newStubTunnel(ctx context.Context, sid string, ws *websocket.Conn) *stubTunnel {
    conn := newWSConn(ctx, ws)  // net.Conn wrapper
    session, err := yamux.Server(conn, yamuxCfg())  // yamux.DefaultConfig() with EnableKeepAlive=false
    if err != nil { conn.Close(); ... }
    t := &stubTunnel{sandboxID: sid, mux: session, wsConn: conn, done: make(chan struct{})}
    go t.watch()  // <-session.CloseChan() → close(done)
    return t
}

func (t *stubTunnel) OpenHTTPStream(ctx context.Context, meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, io.ReadCloser, error) {
    stream, err := t.mux.Open()
    if err != nil { return HTTPResponseMeta{}, nil, err }
    meta.BodyLen = len(body)
    metaJSON, _ := json.Marshal(meta)
    if err := writeStreamHeader(stream, streamTypeHTTP, metaJSON); err != nil {
        stream.Close(); return HTTPResponseMeta{}, nil, err
    }
    if len(body) > 0 { if _, err := stream.Write(body); err != nil {...} }
    _, respMetaJSON, err := readStreamHeader(stream)
    if err != nil { stream.Close(); return HTTPResponseMeta{}, nil, err }
    var respMeta HTTPResponseMeta
    _ = json.Unmarshal(respMetaJSON, &respMeta)
    return respMeta, stream, nil  // caller closes stream
}

func (t *stubTunnel) Close() { t.closeOnce.Do(func() { t.mux.Close(); t.wsConn.Close(); close(t.done) }) }
func (t *stubTunnel) Done() <-chan struct{} { return t.done }

// watch 只观察 mux CloseChan，不直接 close(t.done)——真正的 close 由 Close() 走 closeOnce 保护，
// 防 P1 fix (double-close panic on reconnect): registerTunnel(new) → old.Close() → close(done)
// → old.watch() wake up → 若 watch() 再 close(done) 就 panic。所以 watch 只调 Close()，不裸操 done。
func (t *stubTunnel) watch() {
    <-t.mux.CloseChan()
    t.Close()  // 走 closeOnce，重入安全
}
```

### 3.5 Wire 协议常量（tunnel_transport.go 复制）

```go
const (
    streamTypeHTTP     byte = 0x01  // HTTP proxy request (server → agent)
    streamTypeTerminal byte = 0x02  // 不实现，仅占位常量
    streamTypeControl  byte = 0x03  // agent info；stub 目前接收后忽略
)

type HTTPStreamMeta struct {
    Method  string            `json:"method"`
    Path    string            `json:"path"`
    Headers map[string]string `json:"headers"`
    BodyLen int               `json:"body_len"`
}

type HTTPResponseMeta struct {
    Status  int               `json:"status"`
    Headers map[string]string `json:"headers"`
}

// wire format: 1B type + 4B big-endian metaLen + metaLen bytes JSON
```

### 3.6 Reconnect race（fix P1 #7）

```go
func (s *Server) registerTunnel(sid string, t *stubTunnel) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if old, ok := s.tunnels[sid]; ok {
        old.Close()  // 旧 session 显式关，避免泄露
    }
    s.tunnels[sid] = t
}

func (s *Server) unregisterTunnel(sid string, t *stubTunnel) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    if existing, ok := s.tunnels[sid]; ok && existing == t {
        delete(s.tunnels, sid)
        return true
    }
    return false  // 已被 reconnect 覆盖，不 delete
}
```

### 3.7 Close()（防泄漏）

`Server.Close()` 遍历 `tunnels` 逐个 `Close()`，`tasks` 无需清理。tests 里 `defer srv.Close()`。

---

## 4. Wire 契约（stub 侧全部承诺）

### 4.1 `POST /api/agent/discovery/cards`
- Header: `Authorization: Bearer <proxy_token>`（401 if missing/unknown）
- Body: 完全对齐 slave 侧 `internal/tunnel/tunnel.go:PublishCard` 的 JSON（`display_name` / `description` / `agent_type` / `card`）
- 200 空体；400 body 坏 JSON；401；405

### 4.2 `GET /api/agent/discovery/agents`
- Header: `Authorization: Bearer <proxy_token>`
- Response 200 JSON array of `AgentCard`（对齐 `agentsdk.AgentCard{agent_id, display_name, description, agent_type, status, card, version}`）
- 过滤：同 `workspace_id`；`agent_id` = card.sandbox_id；`status` 固定 `"available"`；`version` 固定 1
- self 不排除（driver 侧 `resolveTarget` 自己按 `SandboxID` 排 self）
- 401；405

### 4.3 `WS /api/tunnel/<sandbox_id>?token=<tunnel_token>`
- 401 前置：`byTunnel[qs.token]` 反查得 identity；identity.sandbox_id == path.sandbox_id
- Upgrade：`websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})`（对齐真 agentserver `sandboxproxy/tunnel.go:44`）
- 之后：wrap `wsConn` → `yamux.Server()` → 存 `tunnels[sandbox_id]`
- Heartbeat：每 20s 用 `context.WithTimeout(ctx, 5*time.Second)` 包一个 pingCtx，调 `ws.Ping(pingCtx)`（`nhooyr.io/websocket v1.8.17` 的 `Conn.Ping` 只收 ctx 一个参数，超时由 ctx 承载；`conn.go:210`）；Ping 失败关 session
- 阻塞直到 `session.CloseChan()` 就绪；退出前 `unregisterTunnel(sid, t)`

### 4.4 `ANY /api/agent/peer/<target_short_id>/proxy/<rest_path...>[?query]`
- Header: `Authorization: Bearer <proxy_token>`（caller 的）
- Body: 任意；Content-Length 决定 stub 读多少（默认全读进内存 - smoke 场景 <1MB 无压力；spec §8 note）
- **Path 转发语义（fix round-3 P1-2）**：stub 打进 tunnel 的 `HTTPStreamMeta.Path` 必须**去掉** `/api/agent/peer/<target_short_id>/proxy` 前缀，**保留** rest path + 原 raw query string 一字节不改。例：
  - 请求 `GET /api/agent/peer/slv-001/proxy/files/dir/tok123?recursive=true HTTP/1.1`
  - 转发 `Path` = `/files/dir/tok123?recursive=true`
  - 若原路径无 rest path（只 `/api/agent/peer/<short>/proxy`），转发 `Path` = `/`
  - 实现：`raw := r.URL.EscapedPath()`；`prefix := "/api/agent/peer/"+targetShortID+"/proxy"`；`suffix := strings.TrimPrefix(raw, prefix); if suffix == "" { suffix = "/" }`；`if r.URL.RawQuery != "" { suffix += "?"+r.URL.RawQuery }`
- **错误码统一（fix round-2 P1 #4）**：
  - 401 bearer 缺失/未知
  - 405 不合法 method（如 CONNECT/TRACE — 常见 method 全放）
  - **404** target 不在 caller.workspace 或未 PublishCard（**不区分**，防跨 workspace 存在性泄漏）
  - **502** target 存在但 WS 未连（"no active tunnel"）
  - **502** yamux stream Open/write/read 失败（"tunnel error: ..."）
  - **504** context deadline / timeout
- 成功：write status + headers + body 从 yamux stream

### 4.5 `POST /api/agent/tasks`
- Header: Bearer proxy_token
- Body JSON: `{target_id, skill?, prompt, system_context?, max_turns?, max_budget_usd?, timeout_seconds?, delegation_chain?, requester_id?}`
- 400 缺 `target_id`/`prompt`；401；404 target 不存在；403 跨 workspace
- 201 JSON: `{task_id, session_id:"", status:"pending"}`  ← 对齐 driver 侧 `agentsdk.DelegateTaskResponse`

### 4.6 `GET /api/agent/tasks/poll[?sandbox_id=<sbx>]`
- Header: Bearer proxy_token
- **`sandbox_id` 查询串可选**：loom 侧 `internal/poller/poller.go:98` 打的 URL 无 query；真 agentserver `sandboxproxy` 也 default 到 caller sandbox（对齐 v0.69.9 `agent_tasks.go:handlePollTasks:280`）。仅当 caller 显式传了 `?sandbox_id=X` **且** `X != caller.sandbox_id` 时返 403
- 401；403（跨 sandbox）
- 204 无 pending；200 JSON array（≤5 pending，全部同批标为 `assigned`）
- 单元素形状：`{task_id, prompt, system_context, session_id, max_turns, max_budget_usd}`

### 4.7 `PUT /api/agent/tasks/{id}/status`
- Header: Bearer proxy_token
- Body JSON: `{status: "running"|"completed"|"failed"|"cancelled", result?, output?, failure_reason?, total_cost_usd?, num_turns?}`
- 401；403 task.target_id != caller.sandbox_id；404 task 不存在
- 200 空体
- **实现细节**：poller 上报时用 `result: json.RawMessage`（poller.go:207）；SDK Complete 用 `output: string`（sdk task.go:19）。stub 同时接受两种：
  - `result` 非空 → 存 `resultJSON`
  - 否则 `output` 非空 → 存 `output`
  - 均无 → status 转但 result/output 保持空

### 4.8 `GET /api/agent/tasks/{id}?include_output=true`
- Header: Bearer proxy_token
- 401；404 task 不存在；404 task.workspace_id != caller.workspace_id
- 200 JSON: `{task_id, workspace_id, requester_id, target_id, prompt, status, num_turns, created_at, session_id?, skill?, total_cost_usd?, result?, output?, failure_reason?, completed_at?}`
- **`include_output=true` 时**：若原 PUT 带了 `output` 字段，写 `output`；若带 `result` 且是 string-JSON，可 unmarshal 后填 `output`；否则忽略。smoke 只要 `result` 或 `output` 里能读到 `hello` 即验收通过

---

## 5. 授权矩阵

| 端点 | Bearer 期望 | 反查 map | 额外校验 | 错误 |
|---|---|---|---|---|
| POST `/api/agent/discovery/cards` | proxy_token | byProxy | — | 401 |
| GET `/api/agent/discovery/agents` | proxy_token | byProxy | — | 401 |
| WS `/api/tunnel/<sid>` | tunnel_token (qs) | byTunnel | identity.sandbox_id == path.sid | 401 (pre-upgrade) |
| ANY `/api/agent/peer/<short>/proxy/...` | proxy_token | byProxy | target card in same workspace | 401 / 404 (unknown target OR cross-workspace，不区分) / 502 (no tunnel / stream err) / 504 (timeout) |
| POST `/api/agent/tasks` | proxy_token | byProxy | target card in same workspace | 401 / 400 / 404 / 403 |
| GET `/api/agent/tasks/poll` | proxy_token | byProxy | qs.sandbox_id 若显式给则须 == self；无 qs 则 default 到 self | 401 / 403（仅当显式给错）|
| PUT `/api/agent/tasks/<id>/status` | proxy_token | byProxy | task.target_id == self | 401 / 403 / 404 |
| GET `/api/agent/tasks/<id>` | proxy_token | byProxy | task.workspace_id == self | 401 / 404 |

---

## 6. Concurrency 模型

- **单 `s.mu` RWMutex** 覆盖 6 map（byProxy / byTunnel / cards / tunnels / tasks / tasksBySandbox）
- 热路径读用 RLock；写路径（register / PublishCard / WS attach/detach / CreateTask / poll assign / status update）用 Lock
- Yamux session 自身 concurrent-safe（`session.Open()` 可并发）—— **绝不在 s.mu 内**做 `OpenHTTPStream` 或 stream I/O，避免死锁
- WS handler 阻塞等 `Done()` 时**不持锁**；attach/detach 那 2 个 nano 内持锁
- `handlePeerProxy` 流程：持锁快查 `cards` + `tunnels` → release lock → 无锁 stream I/O → 无锁写 response

---

## 7. 错误分类

| 类型 | HTTP | 何时 |
|---|---|---|
| Missing/bad bearer | 401 | 所有 authenticated 端点 |
| Method 不对 | 405 | 所有端点 |
| Body 坏 JSON | 400 | discovery/cards, tasks POST, tasks PUT |
| target 不存在 | 404 | peer proxy (合并跨 workspace), tasks POST/GET |
| 跨 workspace | 403 (tasks — 对齐真 agentserver §agent_tasks.go:63) / 404 (peer proxy — 防存在性泄漏) | 两处语义不同，故意分离 |
| Poll qs 不匹配 self | 403 | tasks/poll |
| PUT status task 不属于 caller | 403 | tasks/status |
| Target tunnel 未连 | 502 "no active tunnel" | peer proxy |
| yamux stream Open 失败 | 502 | peer proxy |
| Timeout（context deadline） | 504 | peer proxy |

---

## 8. Limitation & follow-ups（不阻断本 PR）

1. **cards 无 TTL / 无 heartbeat 关联**：slave 死了 stub 不知道，`discovery/agents` 仍返其 card
2. **peer proxy body 全读入内存**：目前不流式（smoke 无大 body；产品语义 v2 迭代）
3. **peer proxy 无并发限流**：单 tunnel `session.Open()` 内部上限 256 stream
4. **WS 重连不清 card**：`cards[sid]` 保留，`tunnels[sid]` 被覆盖；无 race，但语义 note
5. **`chat_resume` 会话延续**：poll 返 `session_id` 恒空；无法测试真 chat 上下文延续 — 本 PR 只跑 `bash` skill 避免此路径
6. **Task 无 GC**：`tasks` map 只增；smoke 短寿无 pressure；产品化 v2

---

## 9. 测试矩阵（阶段 3 落地时 minimum bar）

### 9.1 `discovery_test.go`
- `TestDiscoveryCards_MissingBearer_401`
- `TestDiscoveryCards_UnknownBearer_401`
- `TestDiscoveryCards_BadJSON_400`
- `TestDiscoveryCards_StoresAgentCard`（POST → 200 → 再 GET agents 能看到）
- `TestDiscoveryAgents_MissingBearer_401`
- `TestDiscoveryAgents_ReturnsCardsSameWorkspaceOnly`（2 slave 不同 workspace，driver 只看到 self workspace 的）
- `TestDiscoveryAgents_AgentIDIsSandboxID`（**explicit** 覆 fix P0 #3）
- `TestDiscoveryAgents_StatusAlwaysAvailable`

### 9.2 `tunnel_test.go`
- `TestTunnelUpgrade_MissingToken_401`
- `TestTunnelUpgrade_TokenSandboxMismatch_401`
- `TestTunnelUpgrade_AcceptsAndOpensYamuxSession`（websocket dial → yamux ClientMux 能成功创建）
- `TestTunnelReconnect_OldSessionClosed_NewSessionServed`（**explicit** 覆 fix P1 #7）
- `TestTunnelUnregister_DoesNotDeleteNewAfterReconnect`（**explicit** race scenario）

### 9.3 `peerproxy_test.go`
- `TestPeerProxy_MissingBearer_401`
- `TestPeerProxy_UnknownTarget_404`（对齐 §4.4 统一码）
- `TestPeerProxy_TargetNotInWorkspace_404`（**explicit** 覆 fix P1 #8；返 404 而非 403 防存在性泄漏）
- `TestPeerProxy_NoActiveTunnel_502`（target 存在但 WS 未连）
- `TestPeerProxy_ForwardsGETAndBody`（起一个 fake yamux client 侧 handler，driver 侧 POST /api/agent/peer/<sid>/proxy/echo, body 应被转发到 stream 上，读到期望的 HTTPStreamMeta + body）
- `TestPeerProxy_ReturnsResponseFromStream`（fake handler 写 HTTPResponseMeta + body → HTTP response 侧读到相同 status + body）
- `TestPeerProxy_StripsPrefixPreservesQuery`（**explicit** 覆 round-3 P1-2：GET /api/agent/peer/slv-001/proxy/files/dir/tok?recursive=true → 内层 stream 读到 `HTTPStreamMeta.Path == "/files/dir/tok?recursive=true"`）
- `TestPeerProxy_EmptyRestPath_Slash`（GET /api/agent/peer/slv-001/proxy → 转发 `Path == "/"`）

### 9.4 `tasks_test.go`
- `TestCreateTask_MissingBearer_401`
- `TestCreateTask_MissingFields_400`
- `TestCreateTask_UnknownTarget_404`
- `TestCreateTask_CrossWorkspace_403`（**对齐真 agentserver**）
- `TestCreateTask_ReturnsTaskIDStatusPending`
- `TestPollTasks_MissingBearer_401`
- `TestPollTasks_SandboxMismatch_403`
- `TestPollTasks_NoTasks_204`
- `TestPollTasks_AtomicAssignBatch5`
- `TestUpdateStatus_MarksRunningToCompleted`
- `TestUpdateStatus_AcceptsResultField`（poller.go path）
- `TestUpdateStatus_AcceptsOutputField`（SDK path）
- `TestPollTasks_NoQueryString_UsesCallerSandbox`（**explicit** 覆 round-3 P1-1 与 loom `internal/poller/poller.go:98` 真调路径一致）
- `TestUpdateStatus_WrongOwner_403`
- `TestGetTask_ReturnsResultAfterComplete`
- `TestGetTask_WrongWorkspace_404`

### 9.5 集成 `stub_integration_test.go`（新，可选加）
- 模拟"driver + slave" 双 SDK 走 register → PublishCard → discovery → CreateTask → poll → PUT status → GetTask 全链路，用 fake slave-side stream handler 完成 HTTP-over-yamux echo

---

## 10. 与真 agentserver 的兼容性 note

本 stub 是 wire-shape-compatible-only，非行为兼容。已知差异（不算 bug，是 scope）：

- `handleCreateTask` 真 agentserver 会 lookup DB sandbox；stub 仅认 in-memory `cards` 里 register 过的 target。若 driver 拿 sandbox_id 但 stub 没 card，返 404
- `session_id` 在 stub 里永远为空——slave 侧代码若强依赖非空 session_id 会 fail；本 PR 只跑 `bash` skill 规避此路径
- 真 agentserver 有 workspace-scoped 变体 `POST /api/workspaces/{wid}/tasks`；stub **不**实现，因为 driver 侧 `sdk.DelegateTask` 只用 `/api/agent/tasks` 那条路径
- 真 agentserver 在 `handleUpdateTaskStatus` 里做 event stream；stub 仅存最后一次 status/result；`include_output=true` 直接读那份 result

---

## 11. 三阶段循环审的执行计划

- **阶段 1（本文）** — spec 交 codex 审 → parse P0/P1 → 修 → 再审 → 至零 P0/P1
- **阶段 2** — 基于最终 spec 写 plan.md（`superpowers:writing-plans` 交付格式，含 TDD test list、file-by-file 顺序）→ 同 codex session (`resume --last`) 审 → 至零 P0/P1
- **阶段 3** — 按 plan 落 code + 单测 → 同 codex session 审 → 至零 P0/P1；P2 累积成 follow-up
- 每阶段用 `codex exec --output-schema` JSON 返回 `[{severity,category,summary,file,line,failure_scenario}]`
- 单阶段防死循环：>=5 轮不收敛，停下问用户
- 阶段间共享上下文靠 `codex exec resume <session_id>`（同一 session `019f3732-2518-74c1-a3b3-9fbdbbe4974b`）

## 12. Revision log

- **v1 (2026-07-06 initial)** — 初稿：仅 discovery + tunnel（peer proxy 到 upgrade）+ L5 dispatch 期望
- **v4 (2026-07-06 after codex round 3)** — 应审核补 2 P1（0 P0）：
  - r3 P1-1 poll 无 query 路径缺 explicit 正测 — 加 `TestPollTasks_NoQueryString_UsesCallerSandbox`（§9.4）
  - r3 P1-2 peer proxy Path 转发语义未固化 — §4.4 与 §3.3 step 5 明写"去掉前缀保留 raw query"，加 §9.3 `TestPeerProxy_StripsPrefixPreservesQuery` + `TestPeerProxy_EmptyRestPath_Slash`

- **v3 (2026-07-06 after codex round 2)** — 应审核补 2 P0 + 2 P1：
  - r2 P0-1 poll 无 `?sandbox_id=` — loom poller 不带 query；stub 改为可选 param + default self（§3.3 step 7 / §4.6 / §5）
  - r2 P0-2 `ws.Ping(ctx)` 单参 — 改为 `context.WithTimeout` 包裹（§4.3）
  - r2 P1-1 watch()/Close() 双关 done 会 panic — watch 改为不裸 close, 走 t.Close() closeOnce (§3.4)
  - r2 P1-2 peer proxy 错误码 spec ↔ test 不一致 — 统一 unknown target/跨 workspace 都 404, no-tunnel 502, stream err 502, timeout 504 (§3.3 step 5 / §4.4 / §5 / §9.3)

- **v2 (2026-07-06 after codex round 1)** — 应审核补齐 6 P0 + 2 P1：
  - #1 peer proxy 路径 `/api/agent/peer/<target>/proxy/<path>`
  - #2 复制 agentserver `internal/tunnel/*.go` 实现 `HTTPStreamMeta` 协议
  - #3 agent_id = sandbox_id
  - #4 补齐 task poll 全套 4 端点（scope 扩到完整 dispatch）
  - #5 L3 改成 ≥1 台（deploy.sh 默认 1 台）
  - #6 L4 sqlite 路径写为 `$LOOM_HOME/observer/observer.db`
  - #7 tunnel reconnect race — 仿 agentserver `Unregister(sid, t)` 只有匹配才删
  - #8 加显式跨 workspace peer proxy 测试
