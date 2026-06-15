# driver-agent serve-daemon — 设计文档

- **日期**：2026-06-15
- **范围**：
  - 新子命令 `driver-agent serve-daemon --config <X>`
  - 长跑进程：拨 WebSocket 出口到 observer，注册自己作为 commander-link daemon
  - 同进程暴露本地 HTTP API（127.0.0.1，仅本机网页 + debug 用）
  - 收到 observer 推下来的命令 → 调 PR-1 提供的 `Backend.ListSessions/GetSession/RunResume`，结果回上
  - **WS message envelope schema lock**（PR-3 observer-side hub 直接按此契约实现）
- **上层 context**：[[docs/vision/commander-web-entry.md]] 第 4 节 PR-2；建在 PR-1（`Backend.ListSessions`/`GetSession`）已落地的基础上
- **不在范围**：
  - observer 端 reverse proxy + WS hub + web UI（PR-3）
  - master 路径（[[master_path_frozen]]）
  - 多 kind on 同 daemon（每 daemon 一 kind，由 `cfg.Agent.Kind` 决定；多 kind = 跑多 daemon）
  - 持久化 daemon-side state（in-memory 即可；进程重启清空，session 文件自己在 backend 那持续）
  - daemon → observer 之外的 transport（不做 SSE / 长轮询 fallback）

## 背景

vision 第 3 节确定的整体：daemon 长跑，绕 NAT 用 daemon 主动出 WS 连 observer，observer 收 web 用户指令后推 WS 给对应 daemon 执行。PR-1 已经让 `Backend` 能枚举/读 session；PR-2 把这层 read + RunResume 接进 daemon HTTP + WS 上。

「本机网页仅限测试和 debug」——因此 HTTP server 默认 bind `127.0.0.1`，操作员可 curl 调试，**不**用作生产入口。

## 目标不变量

1. **后台长跑**：daemon 进程不依赖 stdio；启动后保持 WS 连接与 HTTP listener，直到 SIGTERM/SIGINT
2. **WS 主动出口**：daemon 拨 `observer.url` 的 `/api/daemon-link`（exact path TBD by PR-3, this spec proposes path），Bearer `cfg.Credentials.ProxyToken`
3. **断线自动重连**：observer 不可达时 daemon 不退；指数退避重连，最大 30 s 间隔
4. **HTTP API 本机绑定**：默认 `127.0.0.1:0`（端口由 OS 分配并打印到 stderr）；可通过 `--listen 127.0.0.1:9099` 显式指定。**不允许 bind 0.0.0.0** 除非显式 `--listen 0.0.0.0:N` 且打印 warning
5. **HTTP / WS 共用 handler**：两个 transport 调同一组 session-handler 函数；HTTP 路径名与 WS envelope `kind` 字段一一对应
6. **协议契约 lock**：WS envelope schema 由本 spec 定义；PR-3 实现 hub 时按此对接
7. **流式事件**：`RunResume` 通过 `Sink` 输出 chunk/capability event；daemon 转发为 WS `event` 消息流，HTTP 路径用 SSE 同样格式
8. **与 `serve-mcp` 并存**：同一 `config.yaml` 启动 daemon 与 serve-mcp 互不冲突；agentsdk tunnel 共享 `cfg.Credentials.TunnelToken` 在两进程内独立维持
9. **Master 不动**：仅 `cmd/driver-agent/`、`internal/driver/`（如需 daemon 子包）、新 `internal/commander/` 子包

## 整体结构

```
                          observer (云端)
                              │  WS  /api/daemon-link
                              │  Bearer ProxyToken
                              ▼
            ┌──────────────────────────────────────┐
            │ driver-agent serve-daemon (long-lived)│
            │                                       │
            │ ┌────────────────────────────────┐   │
            │ │  WS client (out-bound dial)    │◄──┼── ws envelope
            │ │  reconnect w/ backoff          │   │
            │ └──────────────┬─────────────────┘   │
            │                │                     │
            │ ┌──────────────▼─────────────────┐   │
            │ │  command router                │   │
            │ │  list_sessions  | get_session   │   │
            │ │  session_turn (streams events)  │   │
            │ └──────────────┬─────────────────┘   │
            │                │                     │
            │     reuse PR-1 Backend interface    │
            │                │                     │
            │ ┌──────────────▼─────────────────┐   │
            │ │  HTTP listener (127.0.0.1)     │◄──┼── curl/local-web
            │ │  GET  /sessions                │   │
            │ │  GET  /sessions/{id}           │   │
            │ │  POST /sessions/{id}/turn (SSE)│   │
            │ │  GET  /healthz                 │   │
            │ └────────────────────────────────┘   │
            └──────────────────────────────────────┘
```

## 文件结构

| 文件 | 状态 | 责任 |
|---|---|---|
| `cmd/driver-agent/main.go` | Modify | 加 `serve-daemon` case 进 switch；加 `runServeDaemon(args []string)` |
| `cmd/driver-agent/README.md` | Modify | 加 `serve-daemon` 子命令使用 |
| `internal/commander/protocol.go` | Create | WS envelope structs + JSON tags + version const |
| `internal/commander/protocol_test.go` | Create | encode/decode round-trip 测试 |
| `internal/commander/daemon.go` | Create | `Daemon` struct + `Run(ctx) error`；包含 WS client + HTTP server + handler |
| `internal/commander/daemon_test.go` | Create | in-memory WS server fixture; daemon registers + responds to commands |
| `internal/commander/handler.go` | Create | shared command handlers (list/get/turn) — 两 transport 都调 |
| `internal/commander/handler_test.go` | Create | 用 fake Backend 验 handler 转发到 Backend 方法 |
| `internal/commander/wsclient.go` | Create | gorilla WS client with reconnect + heartbeat |
| `internal/commander/wsclient_test.go` | Create | 模拟 server 验 reconnect + heartbeat |
| `internal/commander/http.go` | Create | HTTP routes + SSE for streaming turns |
| `internal/commander/http_test.go` | Create | httptest 验 4 个路由 |
| `internal/driver/config.go` | Modify | 加 `Daemon` config 段（listen addr, ws path, reconnect knobs） |
| `internal/driver/config_test.go` | Modify | yaml round-trip 含 daemon 段 |
| `go.mod` / `go.sum` | Modify | 加 `github.com/gorilla/websocket`（pure Go） |

## WS envelope schema lock

所有 WS 消息均为单行 JSON（newline-delimited frame OK，gorilla/websocket text frames 用 Marshal/Unmarshal 直接收发）。

### 公共信封

```go
type Envelope struct {
    // Type 是 message kind。Daemon→Observer：register / heartbeat / ack /
    // command_result / event / error。Observer→Daemon：command / ping.
    Type string `json:"type"`

    // ID 让 daemon 把 reply / event 关联回原 command。Observer 发
    // command 时生成；daemon 在 result/event/ack/error 里回填。
    // register / heartbeat / ping 不需要 ID。
    ID string `json:"id,omitempty"`

    // Payload 是具体 message body。JSON 用 raw 保留延迟反序列化。
    Payload json.RawMessage `json:"payload,omitempty"`
}
```

### Daemon → Observer

#### `register`（连接建立后立即发，仅一次）

```json
{
  "type": "register",
  "payload": {
    "schema_version": 1,
    "kind": "claude" | "codex" | "opencode",
    "agent_bin": "/usr/bin/claude",
    "agent_workdir": "/home/me/proj",
    "display_name": "office-mac",
    "driver_version": "v0.1.2"
  }
}
```

Observer 拒绝 schema 不匹配，daemon 收到 `error` envelope 后退出。

#### `heartbeat`（每 30 s 发，无 payload）

```json
{ "type": "heartbeat" }
```

Observer 用 heartbeat 判断 daemon 是否在线；30 s 未收即视为离线。

#### `command_result`（响应非流式 command）

```json
{
  "type": "command_result",
  "id": "<original-command-id>",
  "payload": { /* command-specific result body */ }
}
```

#### `event`（流式 command 的中间事件，e.g. session_turn 流出 chunk）

```json
{
  "type": "event",
  "id": "<original-command-id>",
  "payload": {
    "event_kind": "chunk" | "capability" | "awaiting_user",
    "text": "...",
    "extra": { /* event-kind specific */ }
  }
}
```

#### `error`（任何 daemon-side failure 都用 envelope 而非关 WS）

```json
{
  "type": "error",
  "id": "<command-id, if relevant>",
  "payload": {
    "code": "session_not_found" | "backend_unavailable" | "schema_version_mismatch" | "internal",
    "message": "human-readable"
  }
}
```

### Observer → Daemon

#### `command`

```json
{
  "type": "command",
  "id": "cmd-<uuid>",
  "payload": {
    "command": "list_sessions" | "get_session" | "session_turn",
    "args": { /* command-specific */ }
  }
}
```

具体 args:

- `list_sessions`：`{}`（空）→ daemon 回 `command_result` payload `{ "sessions": [Session...] }`
- `get_session`：`{ "id": "<sess-id>" }` → daemon 回 `command_result` payload `{ "session": Session, "messages": [SessionMessage...] }`
- `session_turn`：`{ "id": "<sess-id>", "prompt": "user new turn text" }` → daemon 流出 0..N 个 `event` envelope，最后一个 `command_result` payload `{ "result": Result }`（agentbackend.Result 的 marshallable subset）

#### `ping`（observer 主动 keep-alive，可选）

```json
{ "type": "ping" }
```

Daemon 收到 `ping` 立刻回 `heartbeat`（用于观测端检测 daemon RTT；可不实现，与 daemon 主动 heartbeat 互补）。

### Schema version

`schema_version: 1`。后续 breaking change 升 2；observer 与 daemon 必须双向支持当前协议版本，否则拒绝注册并报 `schema_version_mismatch`。

## HTTP API（本机 debug）

所有 HTTP API 请求都必须带：

```
Authorization: Bearer <cfg.Credentials.ProxyToken>
```

这不是生产入口，只用于 curl/debug；即便绑定在 `127.0.0.1`，bearer 也用于挡住浏览器 CSRF /
DNS rebinding 页面和本机其他用户的误用。

| 路径 | 方法 | 响应 |
|---|---|---|
| `/healthz` | GET | `200 OK\nlinked: true|false\nuptime: ...` |
| `/sessions` | GET | JSON `{ "sessions": [Session...] }` — 等价于 `command:list_sessions` |
| `/sessions/{id}` | GET | JSON `{ "session": Session, "messages": [SessionMessage...] }` — 等价于 `command:get_session` |
| `/sessions/{id}/turn` | POST | SSE: 每个 event 对应一个 WS `event` envelope；最终一个 `event: done` 包含 final Result |

POST body：`{ "prompt": "user new turn text" }`

SSE event stream：

```
event: chunk
data: {"text": "..."}

event: chunk
data: {"text": "..."}

event: capability
data: {"text": "..."}

event: done
data: {"result": {...}}

event: error
data: {"code": "session_not_found" | "backend_unavailable" | "internal", "message": "..."}
```

HTTP handlers 和 WS handlers 共用 `internal/commander/handler.go` 里的同一组函数；transport 适配只在 envelope 转换 + sink 写出层。

## driver config 新增

```yaml
# internal/driver/config.go DriverDefaults 段下加 daemon 子段（可选）：
daemon:
  listen: "127.0.0.1:0"      # default; "" = same as default
  ws_path: "/api/daemon-link" # default; must match observer (PR-3)
  reconnect:
    initial_backoff_ms: 1000
    max_backoff_ms: 30000
    heartbeat_interval_sec: 30
```

LoadConfig 给所有字段填合理默认；`listen=""` 时用 `"127.0.0.1:0"`；`ws_path=""` 用 `/api/daemon-link`。

## 启动流程

```
driver-agent serve-daemon --config /path/to/driver.yaml

  1. LoadConfig (复用 PR #18 的现有 loader)
  2. agentbackend.New(Config{Kind: cfg.Agent.Kind, ...})
  3. http.Listen(cfg.Daemon.Listen) → 拿实际端口 print to stderr
  4. spawn WS client goroutine:
     - dial cfg.Observer.URL + cfg.Daemon.WSPath with Bearer ProxyToken
     - send register envelope
     - loop: recv command → dispatch handler → send result/event back
     - on disconnect: backoff + redial
  5. block on SIGTERM/SIGINT → graceful shutdown (close WS, drain HTTP, exit 0)
```

## Sink 适配（流式事件转 WS event）

`agentbackend.Sink` 是现有接口（`internal/executor.Sink`）：

```go
type Sink interface {
    Write(kind, text string)
    Close()
}
```

新的 sink 实现 `wsSink` / `sseSink` 把 Write 调用转成对应 transport 的 event envelope。`handler.RunTurn` 接 sink interface，handler 内调 `backend.RunResume(ctx, id, prompt, sink)` 后 sink 自动 stream。

## 测试策略

### `internal/commander/protocol_test.go`

- `TestEnvelope_JSONRoundTrip` — 每种 envelope 类型 encode→decode 字段不丢
- `TestEnvelope_UnknownType` — 拒绝未知 type 时返 `schema_version_mismatch`-class 错误

### `internal/commander/handler_test.go`

- fake Backend implementing `Backend` interface — list/get/runresume mockable
- `TestHandler_ListSessions_ForwardsToBackend`
- `TestHandler_GetSession_ReturnsErrSessionNotFound_AsErrEnvelope`
- `TestHandler_SessionTurn_StreamsSinkWrites`

### `internal/commander/wsclient_test.go`

- in-process `httptest.NewServer` + `gorilla/websocket` upgrader
- `TestWSClient_DialsAndRegisters`
- `TestWSClient_ReconnectsOnDrop` —— close server, daemon retries with backoff, eventually re-registers
- `TestWSClient_HeartbeatEvery30s` —— fake clock or smaller test interval
- `TestWSClient_RejectsOnSchemaMismatch` —— server replies error envelope, daemon returns from Run

### `internal/commander/http_test.go`

- `httptest.NewServer` wrapping daemon HTTP
- `TestHTTP_HealthzOK`
- `TestHTTP_GETSessions_ReturnsBackendResult`
- `TestHTTP_GETSession_404OnUnknown`
- `TestHTTP_POSTTurn_StreamsSSE` — content-type `text/event-stream`，多个 event lines

### `internal/commander/daemon_test.go`

- end-to-end: daemon 启动 → 连 fake observer (httptest WS) → 发 command → 收 result；同时 HTTP 调 `/sessions` 也返同一数据
- `TestDaemon_GracefulShutdownClosesBothTransports`

### `cmd/driver-agent/main_test.go`

- `TestServeDaemon_ParseFlags` —— `--config`, `--listen` 默认值正确

回归：`go test ./... -race -count=1`

## 兼容性

| 变更 | 影响 |
|---|---|
| 加新子命令 `serve-daemon` | 加，不动现有 `register` / `serve-mcp` |
| 加 `gorilla/websocket` 依赖 | pure-Go，CGO_ENABLED=0 无影响 |
| `driver.Config` 加 `Daemon` 段 | 新可选字段；旧 YAML 缺则 LoadConfig 填默认 |
| 不改 Backend 接口 | PR-1 已稳；本 PR 只消费 |
| 不动 `serve-mcp` | 现有 stdio 模式 100% 不变 |

## 决策记录

| 决策 | 备选 | 选择理由 |
|---|---|---|
| 子命令 `serve-daemon` | 加 `--daemon` flag 到 `serve-mcp` | 子命令显式，方便 systemd unit + log; serve-mcp 行为不变 |
| WS protocol = JSON over text frames | gRPC / protobuf / msgpack | 简单；不增依赖（gorilla 自带）；schema lock 也容易在 doc 里写出来 |
| HTTP API + WS 双 transport，handler 共用 | 只 WS 或只 HTTP | 用户答「HTTP 仅 debug」—— 单 handler 两 transport 满足；无重复实现 |
| `127.0.0.1:0` 默认 listen | `127.0.0.1:9099` 固定 | 同一 host 多 daemon 不冲突；端口由 OS 给，无 conflict |
| WS 由 daemon 主动出口 | observer 反向连 | NAT-friendly；vision 第 3.1 节已锁 |
| 重连指数退避 1s→30s | 固定 5s / 无退避 | 抗瞬态网络问题；不给 observer 端无限制重连压力 |
| Heartbeat 30s | 60s / 10s | 30s 够检 NAT 半小时 timeout；不浪费带宽 |
| 流式事件 = WS `event` envelope | 一次性返回 final result | RunResume 已 sink-based；流式自然映射 + UI 实时性 |
| schema_version = 1 hard-coded | 协商 | v1 简洁；后续 break 必升 2 + daemon/observer 双向支持表 |
| Session 数据不缓存在 daemon | in-memory cache | vision 第 3.3 节已锁；每次 list/get 直接调 Backend 读 |
| 每 daemon 一 kind | 多 kind | 一 host 多 kind 跑多 daemon；保持配置极简；vision 第 4 节已隐含 |
| 不为 daemon 单独 auth；复用 ProxyToken | 单独 OAuth 流 | user 上次反馈「先试 daemon 与 driver 一套 token」 |

## 反目标 / 反范围

- 不实现 observer 端 WS hub / 反代（PR-3）
- 不实现 web UI（PR-3）
- 不动 session 写路径（仅消费 Backend.RunResume）
- 不持久化 daemon-side 任何 session 数据（in-memory only）
- 不实现 multi-kind 单 daemon
- 不引入 gRPC / protobuf
- 不动 master 路径
- 不动 `serve-mcp` / `register`
- 不实现 daemon-to-daemon discovery（observer 是 hub，daemon 之间不互联）
- 不实现 daemon hot-reload config（重启 daemon 即可）

## 风险

1. **observer 端 PR-3 还没实现**，WS endpoint `/api/daemon-link` 不存在；daemon 启动会循环 reconnect 失败。**Mitigation**：spec 锁了 envelope；PR-3 直接对照实现，端到端可联调
2. **gorilla/websocket** 是 sunsetting maintenance；upstream 推荐 `nhooyr.io/websocket`。**Decision**：gorilla 仍稳定且广泛使用，PR-2 用 gorilla；如未来需迁移 nhooyr，handler 层 transport-abstracted
3. **HTTP API bind 0.0.0.0**：unsafe by default 已规避（127.0.0.1 默认 + warning on 0.0.0.0）；但仍有 footgun
4. **多 daemon 同 host 同 kind**：register payload 含 `display_name`，observer 用 display_name + kind + subject 去重；user 责任不重名
5. **流式 turn 跨 reconnect**：如果 daemon 在 turn 进行中 WS 断了，重连后 observer 无法续 stream（command id 失效）。v1 接受这个 — turn 失败用户重试。v2 可加 turn resume token

## Implementer pre-flight checklist

1. **确认 `gorilla/websocket` 不在现有 go.mod**：`grep gorilla go.mod` 看；若无，`go get github.com/gorilla/websocket`
2. **跑 PR-1 已 merge 的所有 sessions test**：本地 `go test ./pkg/agentbackend/... -race -count=1` 应全绿
3. **确认 `cfg.Credentials.ProxyToken` 是 daemon 启动时一定存在**：driver-agent register 子命令产物；spec 假设 daemon 启动前已 register 过；implementer 在 `serve-daemon` 启动校验空值 → fail-fast with 提示「先跑 register」
4. **grep `127.0.0.1` 全仓**确认无 hard-coded 冲突端口
