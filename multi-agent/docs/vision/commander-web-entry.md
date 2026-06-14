# 星池指挥官 web entry — 架构 vision

- **日期**：2026-06-14
- **来源**：`/root/pr_review/personal-compute-network.html` 的 vision（「一个入口连接所有工作现场」）；对比当前 Loom 架构后的 3-PR 实施蓝图
- **状态**：long-living architecture doc。每个具体 PR 的 design spec 单写在 `docs/superpowers/specs/`，本文档作为它们的上层契约。
- **不在范围**：master 路径（[[master_path_frozen]]）；agentserver 端改造；ModelServer。

## 1. 现状盘点

| 组件 | 形态 | 与 vision 的差距 |
|---|---|---|
| **driver-agent** | stdio MCP 服务，**短命**（codex/claude/opencode 的子进程） | 没法被网页消费；进程关 → driver 关 |
| **observer-server** (`39.104.x:8090`) | 已是**全局**的（所有 driver/slave 推事件过去），有 dashboard / `/api/tasks/{id}/progress` / token + workspace 鉴权 | 只读：网页能看，不能下指令 |
| **identity 链路** (`internal/identity/agentserver/resolver.go`) | observer 已能调 agentserver `/api/agent/whoami` 验 token、拿 subject | 现在仅事件上推走这条；下行命令通道未做 |
| **agentserver** (`agent.cs.ac.cn`) | 中心 tunnel + device-code OAuth + workspace 注册 | 不暴露面向终端用户的任务下发 |
| **星池指挥官 app** | **不存在** | 整块缺位 |

## 2. 真正缺的两件

1. **driver 要长跑** —— 网页要随时下指令，必须有常驻进程接
2. **observer 加 control plane** —— 接受网页发的指令并转发给指定的长跑 driver

其余组件全部就位。observer 是全局、有 dashboard、有 agentserver token validation；driver 已经走过 device-code OAuth 拿到带 user identity 的 token。

## 3. 总体设计

```
            ┌─────────────────────────────────────────────────┐
            │            Web (/commander)                      │
            │   sessions 列表    发新指令到 session            │
            └─────────────┬──────────────┬───────────────────┘
                          │ agentserver  │ HTTPS
                          │ OAuth token  │
                          ▼              ▼
              ┌────────────────────────────────────────────────┐
              │      observer-server (云端 39.104.x:8090)       │
              │  ‧ 新增 reverse proxy: /api/commander/...       │
              │  ‧ 新增 WebSocket /daemon-link ← daemon 长连接 │
              │  ‧ 用 identity.Resolver 验所有 token (agentserver) │
              │  ‧ 已有 /api/events / dashboard 不变            │
              └─────────────────────┬──────────────────────────┘
                                   │ WebSocket
                                   │ (daemon 出口连接，绕 NAT)
                                   │ Auth: Bearer <ProxyToken>
                                   ▼
            ┌──────────────────────────────────────────────┐
            │ driver-agent daemon (长跑)                    │
            │ ‧ 同 config.yaml；复用 cfg.Credentials.ProxyToken │
            │ ‧ 启动注册 → WS 长连 observer                  │
            │ ‧ session map（in-memory v1）+ slave_id 映射  │
            │ ‧ 收 observer 命令 → 调 backend 的            │
            │   ListSessions / GetSession / RunResume       │
            │ ‧ 与现有 serve-mcp 并存（不互冲）             │
            └─────────────────────┬───────────────────────┘
                                  │ 现有 agentsdk tunnel
                                  ▼
                              [slaves]
```

### 3.1 Transport：WebSocket（daemon 出口）

- daemon 在 NAT 后，observer 公网 → 只能 daemon 主动出口连
- 一旦 WS 建立，observer 可双向：接事件、推命令

### 3.2 Token / identity 模型

**关键决策**：daemon → observer 用 `cfg.Credentials.ProxyToken`（device-code OAuth 后 agentserver 颁的），**不**用 `observer.api_key`。

| 角色 | token 源 | observer 验证路径 |
|---|---|---|
| Web 用户 | agentserver device-code OAuth | `identity.Resolver` → agentserver `/api/agent/whoami` → 拿 subject |
| daemon → observer (WS) | driver register 时 agentserver 颁的 `ProxyToken` | 同上 |
| driver/slave 推事件（现有） | 同 ProxyToken | 同上 |
| 旧 `observer.api_key` | shared secret | 退化为「v1 兼容 fallback」，新功能不依赖 |

含义：
- 「我的 daemon」自动 = WS 连接 token 的 subject 与当前 web 用户 subject 一致
- 多用户共享一个 observer，自然按 subject 隔离 session 视图
- 不需要新增 token 分发流程，复用 driver register 已走过的 OAuth

### 3.3 Session 模型

- **session 的权威源是 backend CLI 自己的持久化**（claude `~/.claude/projects/.../*.jsonl`、codex 的 thread storage、opencode 的 session dir）
- daemon **不存** session 内容，按需从 backend 拉
- daemon 维护的 in-memory map 仅是「这台 daemon 启动以来已知的 session_id 列表」（v1，重启清空）
- web 用户能枚举 backend 文件夹里的全部 session（不只是 daemon 自己 spawn 的）—— 由 `Backend.ListSessions()` 提供

### 3.4 三个 backend 的 session 存储

| Backend | Session 存储位置 | 列表能力 |
|---|---|---|
| claude | `~/.claude/projects/<encoded_workdir>/{sessionId}.jsonl` | 扫目录 |
| codex | codex thread storage（implementer 需调研，可能 `~/.codex/threads/` 或 sqlite db） | 扫 |
| opencode | opencode session 目录（implementer 调研，可能 `~/.local/share/opencode/sessions/` 或类似） | 扫 |

每个 backend 的具体路径在 PR-1 调研时确定并写进各自 backend 的 `ListSessions` 实现。

## 4. 三 PR 递进

### PR-1: Backend 接口扩 ListSessions / GetSession + 3 backend 实现

- `pkg/agentbackend/backend.go` 加：
  ```go
  type Session struct {
      ID         string
      Kind       Kind
      WorkingDir string
      StartedAt  time.Time
      Updated    time.Time
      LastUserMsg string  // optional preview
      LastAssistantMsg string  // optional preview
  }
  type Backend interface {
      // ... 现有 ...
      ListSessions(ctx context.Context) ([]Session, error)
      GetSession(ctx context.Context, id string) (Session, []SessionMessage, error)
  }
  type SessionMessage struct {
      Role string // user / assistant / tool / ...
      Text string
      Ts   time.Time
  }
  ```
- 3 backend 各实现（读自己的 session 目录）
- 单元测试覆盖；用 fake session 文件 fixture
- **不动** daemon / observer / web

**指定 spec**：`docs/superpowers/specs/2026-06-14-backend-sessions-design.md`

### PR-2: driver-agent daemon 模式 + WebSocket out + HTTP API

- 新子命令 `driver-agent serve-daemon --config <X>`
- 启动时 dial WS 到 `observer.url + /daemon-link`，`Authorization: Bearer <ProxyToken>`
- 本地暴露 HTTP API（127.0.0.1 默认）：
  - `GET /sessions` → 调 `backend.ListSessions()`
  - `GET /sessions/{id}` → 调 `backend.GetSession(id)`
  - `POST /sessions/{id}/turn` → 调 `backend.RunResume(id, prompt, sink)`
- WS 上推：daemon 启动注册 + heartbeat
- WS 下推：observer 把网页 POST 转化的 command 推下来 → daemon 处理 → 结果回上
- 与 `serve-mcp` 共用同 config.yaml，不互冲
- 单 host e2e（observer-local + 1 daemon）

**指定 spec**：v2 阶段写

### PR-3: observer `/commander` UI + reverse proxy + WebSocket hub + e2e

- observerweb 加路由 `/commander/*`（独立页面，不并入现有 dashboard）
- observer 加 WS endpoint `/daemon-link`：维护 in-memory `subject → []daemon WS` 映射
- 反代 API：网页 `GET /api/commander/sessions` → 找 user 的所有 daemon → 并发问 → 聚合
- 网页 `POST /api/commander/sessions/{daemon_id}/{session_id}/turn` → 路由到那个 daemon
- 前端：极简（vanilla JS 即可，对齐现有 dashboard 风格）；session list + 单 session chat view + 发新 turn
- 跨 host e2e（2 个 daemon + 1 observer，验证 subject filtering）

**指定 spec**：v3 阶段写

## 5. 反范围 / 反目标

- **不动 master 路径**（cmd/master-agent/, internal/orchestrator/, internal/orchestration/）
- **不改 agentserver**：复用现有 OAuth + whoami；不要求 agentserver 加新 API
- **不引入新 binary**：daemon 是 `driver-agent` 的新子命令，不是新 binary
- **不持久化 daemon session map** v1（重启清空 in-memory 部分；backend session 文件本身一直在）
- **不实现网页端的 humanloop 批准 UI**：v1 用户从 web 发指令进入 awaiting_user 时显示「请到 CLI 端继续」；v2 再做
- **不动 stdio MCP `serve-mcp` 模式**：长跑 daemon 是新增模式，不替换
- **不重写事件上报路径**：现有 driver/slave → observer event push 不动；commander 是新增的下行通道

## 6. 决策记录

| 决策 | 备选 | 选择理由 |
|---|---|---|
| 一个入口路径 (a) Loom 侧 daemon | (b) agentserver 侧任务总线 | agentserver 不在本仓；(a) Loom 自包含 |
| daemon 住 driver-agent 新子命令 | 独立 binary | driver 业务逻辑 (submit_task / wait_task / chat_resume) 直接复用；不增加部署单元 |
| 多 host 聚合走 (Z) observer reverse proxy | (X) observer 存 session / (Y) 网页直连 N daemon | 网页只面一个 endpoint；session 数据不重复；可加 subject filtering |
| Web 用户 auth 复用 agentserver OAuth | 本地 token / 不鉴权 | 已有 identity.Resolver；与 daemon 同身份体系 |
| daemon → observer token 用 `ProxyToken` | `observer.api_key` / 单独 daemon token | ProxyToken 带 user identity；observer 验证路径现成 |
| daemon ↔ observer transport WS | SSE / long-poll | NAT-friendly + 双向；既能事件上推也能命令下推 |
| Session 权威源 = backend CLI 文件夹 | daemon 自己存 / observer 存 | 复用 backend 已有 session 持久化；不增 schema |
| 3 backend 各实现 `ListSessions` | daemon 统一扫文件系统 | backend 才知道自己的 session 存储位置 + 解码格式 |
| 3-PR 切分 | 一个大 PR | 每 PR 都可单独 merge / review；PR-1 直接是 backend feature |

## 7. 实施顺序总览

```
PR-1  Backend.ListSessions + 3 impl           ← 当前
        ↓
PR-2  driver-agent serve-daemon + WS + HTTP API
        ↓
PR-3  observer /commander UI + reverse proxy + e2e
```

PR-1 是基础（pure interface + impl，无 transport / UI），可以独立 merge 并被未来 PR-2 直接用。

## 8. Open questions（implementer 答）

| 问题 | 何时拍 |
|---|---|
| codex 的 session 实际存哪个目录、文件格式？ | PR-1 调研 |
| opencode 的 session 实际存哪个目录、文件格式？ | PR-1 调研（已知 ~/.local/share/opencode/auth.json 是 auth；session 可能在另一目录） |
| claude session jsonl 已 known 但需要确认 schema 稳定 | PR-1 调研 |
| daemon WS 重连退避策略 | PR-2 |
| /commander 前端用啥（vanilla / preact / 其他） | PR-3 |
