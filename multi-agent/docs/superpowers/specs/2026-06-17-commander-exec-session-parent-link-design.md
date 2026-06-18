# Commander: 链接 driver/slave exec session 到来源 session — 设计文档

- **Issue**: agentserver/loom#24 — *Commander: link driver/slave exec sessions back to originating workspace session*
- **日期**: 2026-06-17
- **状态**: design spec, 待 user review → writing-plans
- **来源**: `/commander` 现状、`docs/superpowers/specs/2026-06-16-commander-ui-redesign-design.md`、`2026-06-14-backend-sessions-design.md`、`2026-06-15-commander-observer-hub-design.md`、`2026-06-15-driver-daemon-design.md`、`2026-06-12-driver-task-journal-design.md`、全栈审查 `multi-agent/docs/review-2026-06-13.md`

## Context（为什么做这件事）

Commander 现在已经能区分直连 user session、本地 subagent、以及 `codex_exec` agent task，并能把本地 subagent 按来源 session 嵌套。但 driver / slave 创建的 `codex_exec` session **不带 `parent_id`**，所以在 session 树里显示成断开的根行——即使从 workspace 视角看，这个远程任务是某个 driver session 派发出去的。Issue #24 要求给这些远程 exec session 显式的父级链路，让 Commander 默认把它们嵌在来源 session 下，保住跨实例的因果结构。

**目标产出**：driver 的交互 codex session 能清晰"拥有"它派发出去的远程任务（在本地 daemon 或 slave daemon 上），且从任一端都能导航到另一端，并展示 slave/driver 的 display name。

## 关键概念：是"哪个 agent 实例启动了 session"，不是"哪台机器"

**`ShortID` 是 per-agent-instance 的，不是 per-machine 的**：

- 一台机器上可跑多个 agent（多个 slave，或 driver+slave），每个各自向 agentserver `Register`，**各自得到不同的 `ShortID`**（`agentserver@v0.48.1/internal/shortid/shortid.go`：8 字符 crypto/rand；`internal/db/migrations/001_initial.sql:100`：全局唯一索引 `idx_sandboxes_short_id`；`internal/server/agent_register.go:94-102`：collision 重试）。
- 因此 **`machine_id` 是错的抽象**。正确的关联单元是 **agent 实例（= 一个 daemon / 一个 driver 或 slave 进程）**，它的稳定唯一 id 是 **`ShortID`**（下称 `agent_id`），给人看的标签是 **`display_name`**。

**隔离机制 = 每个 agent 实例用自己的 `CODEX_HOME`**（见决策 §隔离）。codex 只写自己的 `CODEX_HOME/sessions`，scanner 也只扫自己的，于是：

- **归属天然隐式**：daemon 扫自己 `CODEX_HOME` 即拥有其内全部 session —— 不需要把 owner 写进 sidecar，不需要 observer 去重。
- 一机多 agent 不再互相污染 session 列表，也不会重复上报。
- 跨实例的 **父级链路** 仍必须显式：codex 不知道是谁派发了它，父级信息只能由 sidecar 在创建时写入（沿用下方方案）。

## 现状（带 file:line）

- **Session 模型已支持嵌套**：`agentbackend.Session` 有 `Origin`（`user`/`subagent`/`agent_task`/`unknown`）和 `ParentID` + `AgentName`/`AgentRole`（`pkg/agentbackend/backend.go:111-122`）。
- **Codex scanner** 读 rollout 文件 `<codex_home>/sessions/<yyyy>/<mm>/<dd>/rollout-<iso>-<thread-uuid>.jsonl`，首行是 `session_meta`。当前 `sessionsRoot()` 用 `$HOME/.codex/sessions`（`pkg/agentbackend/codex/sessions.go:34-40`），**未读 `CODEX_HOME`** —— 需改。`applyCodexSessionMeta`（`sessions.go:313-330`）**只**为 codex 原生 subagent（从 `parent_thread_id`）设 `ParentID`；对 `originator == "codex_exec"` 只设 `Origin = agent_task`，**从不设 `ParentID`**。**这是核心缺口。**
- **Codex 启动**由 backend executor 负责：`runWithArgv`（`pkg/agentbackend/codex/executor.go:107`）spawn `codex exec --json …`，`cmd.Env = append(cmd.Environ(), e.env…)`（`executor.go:128`）—— 这是注入 `CODEX_HOME` 给 codex 子进程的点。捕获新 thread id（`sessionID = ev.ThreadID`，`executor.go:191`），也是写 sidecar 的注入点。
- **slave 新建 codex exec session** 走 `routes[""] = backendExecutor{backend}` → `backend.Run(ctx, Task{…, SystemContext})`（`cmd/slave-agent/main.go:242-245`）。`agentbackend.New(Config{…}, nil)`（`slave-agent/main.go:188`、`driver-agent/main.go:271`）第二参 `env` 当前为 nil —— 注入点。
- **Commander tree row** 已带 `Origin`/`ParentID`/`AgentName`/`AgentRole`（`internal/commanderhub/tree.go:26-44`，`sessionRowFromBackend:84-106`）。
- **前端嵌套是另一个缺口**：`DaemonSessionTree.tsx` 的 `buildSessionNodes` **只**嵌 `origin === 'subagent' && parent_id`，且**只在单个 daemon 分组内**（`DaemonSessionTree.tsx:15-34`）。`agent_task` 行从不嵌套；当前结构下跨 daemon 嵌套不可能。
- **派发契约是外部且冻结的**：`agentsdk.DelegateTaskRequest`（module `github.com/agentserver/agentserver`）带 `Prompt`、`SystemContext`、`DelegationChain []string`。observer-hub spec 禁止改 agentserver，所以来源链路必须搭在已有的自由字段（`SystemContext`）上。
- **稳定的 agent 实例 id 已存在但未暴露给 Commander**：
  - `Credentials.ShortID`（`internal/config/config.go:53`）—— `Register` 时由 agentserver 分配（`internal/tunnel/tunnel.go:117`），**持久化到 config 文件**（`tunnel.go:113-119`），已被用作 `Observer.AgentID`、缓存目录 key、peer 路由身份（`/api/agent/peer/<short_id>/proxy`）。**选作 `agent_id`**。
  - `cfg.Discovery.DisplayName` —— 操作员配置、持久化、稳定但**非唯一**；作为给人看的 `display_name` 标签，**不作 key**。
  - `daemonID` 是**短暂的**（observer 每次 WS 连接分配，重连即变），**不能**当 key。

## 决策（已与用户确认）

- **隔离 = 每个 agent 实例用自己的 `CODEX_HOME`**（默认 `$LOOM_HOME/<short_id>/.codex`，可配；fallback `$HOME/.codex`）。codex session、sidecar 都落在该实例私有目录 → **归属隐式、无需去重**（见 §8）。
- **`agent_id` = `ShortID`**（per-agent-instance、稳定、唯一；**非** machine 维度）。标签用 `display_name`。
- **持久化 = sidecar 文件**（`$CODEX_HOME/loom-meta/<thread_id>.json`），codex executor 在捕获 thread id 后写，scanner 合并。与 codex 版本解耦。
- **跨 daemon 嵌套 = 全量**（issue 验收标准"nests under parent by default"），按 `(parent_agent_id, parent_session_id)` 解析 parent。
- **传播载体 = `SystemContext`** 结构化标记，不改 agentserver。
- **双向链路**：driver 派发时带 `(parent_agent_id, parent_session_id, parent display_name)`；slave 把父级 + 自己的 `(child_agent_id, child_session_id)` 记进 sidecar；slave 在输出 marker 里返回 child session；driver 把 child 记进自己的 task journal。两端都持有完整 tuple。

## 设计

### 链路 tuple

```
(parent_agent_id, parent_session_id) ↔ (child_agent_id, child_session_id)
+ parent/child 各自的 display_name（标签，非 key）
```

- `agent_id` = 该 agent 实例（daemon）的 `ShortID`。
- **归属**：隐式 —— child 归属于它所在 `CODEX_HOME` 的 owner agent（= 扫它的那个 daemon）。无需 sidecar 记 owner。
- **父级**：显式 —— `parent_agent_id`/`parent_session_id` 由 sidecar 记（codex 自己不知道父级）。
- `display_name` 仅为给人看的标签；**解析一律用 `agent_id`**。

### 与 codex app-server hot worker 的关系（issue-23，已落 master）

master 已合入 codex app-server hot worker（`pkg/agentbackend/codex/appserver_manager.go` + `agentbackend.SessionWorkerBackend`/`HealthySessionWorker`，`pkg/agentbackend/backend.go:154-169`；`agentbackend.Config.WorkerMode`，`pkg/agentbackend/config.go:17`；`Handler.SessionTurn` 现在先查 `SessionWorkerBackend` → `NewSessionWorker`，否则 `RunResume`，`internal/commander/handler.go:85-105,137`）。本 spec 与它**正交、不冲突**：

- **app-server worker 只"恢复"已存在的交互 session**（`thread/resume`，复用热 worker），**不创建** `codex_exec` agent_task session。issue #24 要链接的 agent_task session 仍由 `Backend.Run` → executor `codex exec`（`pkg/agentbackend/codex/executor.go:107`）创建 —— 本 spec 的 sidecar 写入点不变。
- **parent（交互）session** 经 app-server 或 exec-resume 跑都行，其 codex thread id 就是 `parent_session_id` —— 传播链路不受影响。
- **CODEX_HOME 一致性**：app-server design 的"Runtime context parity"已要求 worker 继承与 exec 路径相同的 `CODEX_HOME`，并把 `CODEX_HOME` 纳入 worker context fingerprint。`newAppServerManager(cfg, env)`（`appserver_manager.go:426`）与 exec executor 共用 `env` —— 所以把 `CODEX_HOME` 注入 `agentbackend.New(cfg, env)` 会自动同时到达 exec 与 app-server 两条路径，**天然一致**。

### 1. 每个 agent 实例用自己的 CODEX_HOME（隔离前提）

- 解析 `CODEX_HOME`：优先 `cfg.Agent.CodexHome`（新可选配置，沿用 `agentbackend.Config` 这个 flat carrier —— 已有 `WorkerMode` 字段先例，`pkg/agentbackend/config.go:17`），否则 `$LOOM_HOME/<short_id>/.codex`（复用 `~/.cache/multi-agent/<short_id>/` 约定，见 `internal/driver/slave_file_tools.go:36`），再否则 `$HOME/.codex`（向后兼容老部署）。
- **三处必须一致**：① daemon 进程 env（让 scanner 的 `sessionsRoot()` 读到同一目录）；② codex exec 子进程 env（`e.env` 注入 `CODEX_HOME=…`，`executor.go:128`）；③ codex app-server 子进程 env（同 `env`，经 `newAppServerManager(cfg, env)`，`appserver_manager.go:426`）—— 让 exec 与 app-server 两路径都写同一目录、scanner 也读同一目录。
- `agentbackend.New(cfg, env)` 的 `env` 当前为 nil —— 改为注入 `CODEX_HOME`（driver `driver-agent/main.go:271`、slave `slave-agent/main.go:188`）；该 env 同时喂给 exec executor 与 app-server manager，无需额外接线。
- 这条要求写进 driver/slave 部署文档（`deploy/`、prod_test config）。
- 注意：`worker_mode` 默认 `off`，绝大多数 agent_task session 走 exec；本 spec 不依赖 app-server，但要求两条路径共用 `CODEX_HOME`，避免 app-server 开启后隔离失效。

### 2. 把 agent 实例身份暴露给 Commander

把 `ShortID`（=`agent_id`）与 `DisplayName` 贯通：

- `commander.RegisterPayload`（`internal/commander/protocol.go:35`）→ 加 `ShortID string`。`DisplayName` 已在。
- `commanderhub.daemonConn` + `DaemonInfo`（`internal/commanderhub/registry.go:23,38`）→ 加 `ShortID`；从 `RegisterPayload` 填（`hub.go:111-113`）。
- observer 维护 `agent_id(ShortID) → 当前 daemonConn` 映射：重连后 daemon_id 变、ShortID 不变，映射按 ShortID 重建；供按 `agent_id` 解析 parent、回查 display_name。
- `SessionRow`（`internal/commanderhub/tree.go:26`）→ 加 `OwnerAgentID`（daemon 上报时填自己的 ShortID，**不**来自 Session）、`ParentAgentID`（来自 Session）、`OwnerDisplayName?`/`ParentDisplayName?`（observer 回填：在线走 registry，离线走 sidecar denormalized）。
- 前端 `SessionRow` 类型（`internal/commanderhub/webapp/src/api/types.ts:10`）→ 加对应字段。

**不需要 bump `commander.SchemaVersion`**：`ShortID` 是 additive；observer 容忍老 daemon 缺该字段（空 = 该 daemon 不能当链路端点）。

### 3. Session 模型：带上 parent 的 agent

扩展 `agentbackend.Session`（`pkg/agentbackend/backend.go:94`）：

- `ParentAgentID string` —— 拥有 `ParentID`（父 session）的 agent 实例的 `ShortID`。`ParentID` 为空时为空。让 observer 把 parent 解析到具体 daemon（daemon_id 短暂，agent_id 稳定）。来自 sidecar。

保留 `ParentID` = 父级 **session** id（语义不变）。**不**在 Session 上加 `OwnerAgentID` —— 归属隐式（由扫它的 daemon 决定），`SessionRow.OwnerAgentID` 在 daemon 上报时填自身 ShortID 即可。

### 4. 传播：driver → slave（正向链路）

- driver 在一轮 turn 中**知道自己当前的 codex thread id**（codex backend executor 从 `thread.started` 捕获）与自己的 `agent_id`（`ShortID`）/`display_name`。把这三者接进 MCP tool-call 上下文：driver 跑 codex turn 时，在 `Tools` handler 上设一个 current-session 值（或经 context 传），turn 结束清掉。
- 在 `submit_task`/shell/contract 派发 handler（`internal/driver/tools.go:511`、`contract_tools.go:100`、`slave_tools.go:150` 等）里，给 `DelegateTaskRequest.SystemContext` 打结构化、带 escape 的标记：
  `<loom_origin agent="<driver ShortID>" name="<driver display_name>" session="<当前 codex thread_id>" />`。
  - 放在任何已有 context 之前；用 boundary 标签包裹并 escape，避免和 `USER_FILES_MANIFEST` 或 prompt 内容混淆（沿用 `codex/sessions.go:446` 的 `stripCodexInjectedUserPrefix` 纪律）。
- 复用 `executor.Task` 带解析后的链路。给 `executor.Task`（`internal/executor/executor.go:5`）加：
  `ParentSessionID string`、`ParentAgentID string`、`ParentDisplayName string`。
- slave 派发桥（`cmd/slave-agent`/`internal/dispatch` 里从 delegated `agentsdk.Task` 构建 `Task` 的那层）：把 `<loom_origin …/>` 从 `SystemContext` 解析出来填进新 `Task` 字段，并**剥离标记**使其不进 codex prompt 正文（维持 `tools.go:506-510` 已有的 JSON-prompt skill 保护）。

### 5. Sidecar 持久化（可恢复的父级记录）

codex executor 捕获新 `sessionID`（`executor.go:191`）后，写一个 sidecar：

- 路径：`$CODEX_HOME/loom-meta/<thread_id>.json`（与 codex session 同根、天然 per-agent 隔离）。
- 内容：
  ```json
  {
    "schema": 1,
    "session_id": "<thread_id>",
    "parent_session_id": "<来源 thread_id，或空>",
    "parent_agent_id": "<来源 ShortID，或空>",
    "parent_display_name": "<来源 display_name，或空>",
    "origin": "agent_task",
    "kind": "codex",
    "created_at": "<RFC3339Nano，由调用方传入，热路径不用 Date.now>"
  }
  ```
  - `parent_*` 来自 `Task` 字段（driver 场景为本机当前 turn；slave 场景为 `<loom_origin>` 解析结果）。
  - `parent_display_name` 故意 denormalized：parent daemon 离线时仍能给人显示标签。
  - 不写 `owner_*`：归属由 `CODEX_HOME` 隐式决定。
- 写入 best-effort：失败降级为"无 parent 链路"（session 照常列出），绝不阻塞 codex run。
- **清理**：一个小 reaper 裁掉 N 天前的条目，以及 thread id 已无 rollout 对应的孤儿文件（scanner 已枚举存活 thread id，可顺带驱动）。

### 6. Scanner：为 agent_task session 恢复 parent

`pkg/agentbackend/codex/sessions.go`：

- `sessionsRoot()` 改为**优先读 `CODEX_HOME`**，否则 `$HOME/.codex/sessions`（向后兼容）。
- `scanCodexSession` 构出 descriptor 后，合并同目录 `loom-meta/<thread_id>.json`：有则设 `Origin = SessionOriginAgentTask`（若未设），从 sidecar 填 `ParentID`/`ParentAgentID`（仅非空时）。
- `applyCodexSessionMeta`（`sessions.go:313`）保留原有 codex 原生 subagent 分支（`parent_thread_id`）——sidecar 是 `codex_exec` 的另一个 additive 来源。
- sidecar 合并放 List/Get 共用路径；现有 `b.list`/`Prune` 文件缓存（`sessions.go:82,91`）以 rollout (path,size,mtime) 为 key，**把 sidecar mtime 也并入 cache key**，sidecar 重写即让该 row 失效。

### 7. 反向链路：slave 结果 → driver journal

- slave 的 chat 输出本就带 `session_id` kind marker，driver 经 `sessionIDFromMarker`（`internal/driver/tools.go:619-636,821-833,1101-1140`）解析。**扩展该 marker 同时带 `agent_id`**（slave 的 ShortID）——不过 driver 通常已从目标 `AgentCard`（`resolveTarget` 返回 `shortID`，`tools.go:165-211`）知道它。
- 扩展 `driver-tasks.jsonl` 记录（`internal/driver/tools.go:79` 的 `delegatedTaskRecord`），加 `child_session_id` + `child_agent_id`（= slave ShortID），在 task 结果 marker 到达时写入。这就是反向链路：一个 driver session 的 children 索引，持久在 driver 本地磁盘，与任何 daemon_id 无关。

### 8. Observer + 前端：全量跨 daemon 嵌套（无需去重）

因 §1 已用 per-agent `CODEX_HOME` 隔离，每个 session 只被**唯一**一个 daemon（其 `CODEX_HOME` owner）扫到并上报，**不存在重复上报**，observer 无需去重。

Observer 侧（`internal/commanderhub/tree.go`）：

- 跨 daemon 嵌套需要前端建一个**全局** parent 索引：
  - key `(owner_agent_id, session_id)` → session node，跨树内**所有** daemon（`owner_agent_id` = 该 row 的归属 daemon ShortID）。
  - 带 `parent_id` + `parent_agent_id` 的 child 挂到 `(parent_agent_id, parent_id)` 这个 key 下，即便 parent 在另一个 daemon 分组。
- 渲染规则：远程 child 嵌在其 **parent** session 下（主位置），带 `remote` badge 标明 parent 侧/child 侧的 display_name（如 `remote · on slave-02`）。为避免重复行，child **从其归属 daemon 的根列表里省略**（它归在 parent 下）；归属 daemon 的 `session_count` 仍计入它。

前端（`DaemonSessionTree.tsx` + `api/types.ts`）：

- `SessionRow` 加 `owner_agent_id`、`parent_agent_id`、`owner_display_name?`、`parent_display_name?`。
- 用跨 daemon builder 替换"单 daemon 内"的 `buildSessionNodes`：聚合所有 session，建全局 `(owner_agent_id, session_id)` map，再把 `origin === 'subagent' || origin === 'agent_task'`（两者都）且带 `parent_id` 的 child 嵌到解析出的 parent node 下，默认折叠（对齐现有 subagent UX）。
- 离线/缺失 parent：若没有节点匹配 `(parent_agent_id, parent_id)`（parent daemon 离线或 session 被裁），把 child 当普通根行渲染并附 `parent offline`（带 parent display_name）灰色注记，而不是丢掉。
- Badge：远程 `agent_task` 行显示 `remote task · on <owner display_name>`；本地 subagent 保留现有 `subagent · <name>`。

### 分阶段（3 PR，对齐本仓 PR 节奏）

1. **P1 — 后端记录 + scanner + 隔离。** `Session.ParentAgentID`；codex executor 写 sidecar + 注入 `CODEX_HOME`；codex scanner 读 `CODEX_HOME` 并合并 sidecar；reaper；`Task` parent 字段；带 fixture 的单测。（无 UI、无传播；测试可手填 sidecar。）
2. **P2 — 传播 + agent_id 贯通。** register→DaemonInfo→SessionRow 带 `ShortID`/`DisplayName`；driver 当前 session-id 接线 + 派发时打 `<loom_origin>`；slave 解析进 `Task`；反向 marker + `driver-tasks.jsonl` child 字段；部署文档写明 per-agent `CODEX_HOME`。端到端：driver 的 `codex exec` 与被派发的 slave `codex exec` 都带上 `parent_id`/`parent_agent_id`。
3. **P3 — Commander 嵌套。** observer 全局 parent 索引；前端跨 daemon `buildSessionNodes` 重写、`remote`/`parent offline` badge（带 display_name）、默认折叠嵌套；Playwright 视觉校验。

### 关键文件

- `pkg/agentbackend/backend.go` — 加 `ParentAgentID`。（注：`SessionWorkerBackend`/`HealthySessionWorker` 已在 `:154-169`，本 spec 不动它。）
- `pkg/agentbackend/config.go` — `agentbackend.Config` 加 `CodexHome`（与已有 `WorkerMode` 同一 flat carrier，`:17`）。
- `pkg/agentbackend/codex/executor.go` — 注入 `CODEX_HOME` 进 `e.env`；`sessionID` 捕获后写 sidecar（约 `:191`）；从 `Task` 字段读 parent。
- `pkg/agentbackend/codex/appserver_manager.go` — 复用同 `env`（`newAppServerManager(cfg, env)`，`:426`），确保 app-server 路径与 exec 路径写同一 `CODEX_HOME`（隔离一致性）；本 spec 不改其协议逻辑。
- `pkg/agentbackend/codex/sessions.go` — `sessionsRoot()` 读 `CODEX_HOME`；List/Get 合并 sidecar；cache key 纳入 sidecar mtime；新增 `loom-meta` reader + reaper。
- `internal/executor/executor.go` — `Task` 加 `ParentSessionID`/`ParentAgentID`/`ParentDisplayName`。
- `internal/commander/protocol.go` — `RegisterPayload` 加 `ShortID`。
- `internal/commanderhub/registry.go` + `hub.go` + `tree.go` — 贯通 `ShortID`；`agent_id → daemonConn` 映射；全局 parent 索引；`SessionRow.OwnerAgentID` 上报时填自身 ShortID。
- `internal/commanderhub/webapp/src/api/types.ts` + `components/DaemonSessionTree.tsx` — 跨 daemon 嵌套 + badge（display_name）。
- `internal/driver/tools.go`（含 `contract_tools.go`、`slave_tools.go`、`register_mcp_tool.go`、`slave_file_tools.go`）— current-session 接线 + `<loom_origin>` 打标；反向 marker 解析；扩 `delegatedTaskRecord`。
- `cmd/slave-agent/main.go` + `cmd/driver-agent/main.go`（`agentbackend.New` 第二参注入 `CODEX_HOME`）；派发桥 + `internal/dispatch/dispatch.go` 解析 marker 进 `Task`、进 prompt 前剥离。
- `deploy/` + `tests/prod_test/` configs — 每个 agent 指定 `CODEX_HOME`（或等价 `cfg.Agent.CodexHome`）。

### 测试

- **隔离**：两个临时 `CODEX_HOME` 各跑独立 scanner，互不见对方 session；codex 子进程确实写进指定 `CODEX_HOME/sessions`；**`worker_mode: app_server` 开启时**，app-server 子进程也写同一 `CODEX_HOME`（隔离一致性，不被 app-server 旁路）。
- **Scanner**：fixture rollout + sidecar 对 → `Origin=agent_task`、`ParentID`/`ParentAgentID` 非空；无 sidecar → 无 parent（不失败）；坏 sidecar → 跳过；sidecar mtime 变 → cache 失效；reaper 删孤儿。
- **Executor**：`thread.started` 后写正确字段 sidecar；`CODEX_HOME` 注入生效；best-effort 写失败不破 run；marker 解析进 `Task` 且从 prompt 剥离。
- **传播**：driver 打 `SystemContext`；slave 解析 + 剥离；JSON-prompt skill 仍能解析（`tools.go:506` 保护保持绿）。
- **反向链路**：`sessionIDFromMarker` 取出 child session + agent_id；`driver-tasks.jsonl` 多出 child 字段。
- **Commander**：observer 解析跨 daemon parent；远程 child 带 badge 嵌在 parent 下、从 home 根列表省略；离线 parent 渲染 `parent offline`（带 display_name）；本地 subagent 嵌套不变；一机多 agent 各自 CODEX_HOME 互不串扰。Playwright：desktop 树显示 `driver → remote task · on slave-02`。
- **回归**：`go test ./... -race -count=1`；`npm test` / `npm run build`（提交的 `assets/dist` "build 后无 diff" 检查，按 UI-redesign spec）。

### 验收标准（loom #24）

- driver 起的 exec session 暴露非空 `parent_id`（来源 driver session）与 `parent_agent_id`。
- slave 起的 exec session 暴露非空 `parent_id`（派发的 driver session）与 `parent_agent_id`。
- 每个 agent 用自己的 `CODEX_HOME`，一机多 agent 的 session 列表互不串扰、不重复。
- Commander session 树默认把这些远程 task session 嵌在 parent session 下（跨 daemon、带 `remote` badge + display_name），且现有本地 subagent 嵌套不变。

### 风险

- **既有 `~/.codex` session 迁移**：切到 per-agent `CODEX_HOME` 后，老 session 留在 `~/.codex` 不再被归属/显示。Mitigation：scanner fallback 读 `$HOME/.codex`（向后兼容，仅当 `CODEX_HOME` 未设）；已切的 agent 视老 session 为未标注根行；迁移期可接受。
- **Sidecar 孤儿**：codex rollout 删了但 sidecar 还在 → reaper + scanner 跳过无对应 thread id 的条目。
- **parent 离线**：跨 daemon parent 可能在断连的 daemon 上 → 渲染为带 `parent offline`（+ parent display_name，来自 sidecar denormalized 字段）的根行，绝不丢 child。
- **`agent_id`(ShortID) 身份漂移**：若 config 被清 / 强制重注册，`ShortID` 会变，旧链路悬空 → 可接受；与系统其余路由所依赖的同一身份模型一致。
- **`display_name` 非唯一**：操作员可能给多个 agent 起同名 display_name → 仅作标签，**绝不用作 key**；解析一律用 `agent_id`(ShortID)。
- **标记漏进 prompt**：`<loom_origin>` 必须在进 codex prompt 前剥离（沿用已有 manifest 剥离纪律），否则 JSON-prompt skill 会破。
- **codex 不认 `CODEX_HOME`**：若某 codex 版本不认该 env → 预飞 probe 确认；不认则按版本用对应 config flag。
- **codex rollout 路径随版本变**：sidecar 以 thread id 为 key，不依赖日期型 rollout 路径，能扛 codex 布局变更。

### ShortID 唯一性证明（备查，不需要重新推导）

- `agentserver/internal/shortid/shortid.go`：`Generate()` = 8 字符、36 字母表、`crypto/rand`（熵 36⁸ ≈ 2.8e12，~41.4 bit），**非主机名派生**。
- `internal/db/migrations/001_initial.sql:100`：`CREATE UNIQUE INDEX idx_sandboxes_short_id ON sandboxes (LOWER(short_id)) WHERE short_id IS NOT NULL;` —— **全局**唯一（无 workspace_id 限定）。
- `internal/server/agent_register.go:94-102`：注册时 collision → `Generate()` 重试最多 3 次。

### Pre-flight probes（写 P1/P2 代码前）

- 确认目标 codex 版本**认 `CODEX_HOME`** 且 session 落在 `<CODEX_HOME>/sessions/`（`codex --help` / 实跑一个 exec 看 rollout 落点）。
- 确认 driver 与 slave 的 prod_test config（`tests/prod_test/`）`ShortID` 非空、重启持久；确认一机多 agent 时各自 `ShortID` 不同、各自 `CODEX_HOME` 不同。
- 确认 codex backend executor 是 driver 与 slave 上创建 `codex_exec` rollout 的唯一路径（grep `codex exec` / `agentbackend.New`）。
- 确认 driver 在 MCP tool-call 时能拿到当前 codex thread id（turn 执行上下文），以便 `submit_task` 打标。
- 定 `$LOOM_HOME` / `CODEX_HOME` 解析（复用 `internal/driver/slave_file_tools.go:36` 与 `~/.cache/multi-agent/<short_id>/` 的既有 home/cache 辅助）。
