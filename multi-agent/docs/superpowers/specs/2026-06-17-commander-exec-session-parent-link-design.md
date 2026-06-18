# Commander: 链接 driver/slave exec session 到来源 session — 设计文档

- **Issue**: agentserver/loom#24 — *Commander: link driver/slave exec sessions back to originating workspace session*
- **日期**: 2026-06-17（2026-06-18 依 review 修订）
- **状态**: design spec, 待 user review → writing-plans
- **来源**: `/commander` 现状、`docs/superpowers/specs/2026-06-16-commander-ui-redesign-design.md`、`2026-06-14-backend-sessions-design.md`、`2026-06-15-commander-observer-hub-design.md`、`2026-06-15-driver-daemon-design.md`、`2026-06-12-driver-task-journal-design.md`、`2026-06-17-codex-app-server-worker-design.md`、全栈审查 `multi-agent/docs/review-2026-06-13.md`

## Context（为什么做这件事）

Commander 现在已经能区分直连 user session、本地 subagent、以及 `codex_exec` agent task，并能把本地 subagent 按来源 session 嵌套。但 driver / slave 创建的 `codex_exec` session **不带 `parent_id`**，所以在 session 树里显示成断开的根行——即使从 workspace 视角看，这个远程任务是某个 driver session 派发出去的。Issue #24 要求给这些远程 exec session 显式的父级链路，让 Commander 默认把它们嵌在来源 session 下，保住跨实例的因果结构。

**目标产出**：driver 的交互 codex session 能清晰"拥有"它派发出去的远程任务（在本地 daemon 或 slave daemon 上），且从任一端都能导航到另一端，并展示 slave/driver 的 display name（含 parent 离线时的兜底标签）。

## 关键概念：是"哪个 agent 实例启动了 session"，不是"哪台机器"

**`ShortID` 是 per-agent-instance 的，不是 per-machine 的**：一台机器上可跑多个 agent，每个各自 `Register` 得到不同 `ShortID`（`agentserver@v0.48.1/internal/shortid/shortid.go` 8 字符 crypto/rand；`internal/db/migrations/001_initial.sql:100` 全局唯一索引；`internal/server/agent_register.go:94-102` collision 重试）。因此 **`machine_id` 是错的抽象**。正确的关联单元是 **agent 实例（= 一个 daemon）**，稳定唯一 id 是 **`ShortID`**（下称 `agent_id`），给人看的标签是 **`display_name`**。

**隔离机制 = 每个 agent 实例用自己的 `CODEX_HOME`**。codex 只写自己的 `CODEX_HOME/sessions`，scanner 也只扫自己的 → 归属天然隐式、无需去重。跨实例的 **父级链路** 仍须显式：codex 不知道是谁派发了它，父级信息由 sidecar 在创建时写入。

## 现状（带 file:line）

- **Session 模型**：`agentbackend.Session` 有 `Origin`/`ParentID`/`AgentName`/`AgentRole`（`pkg/agentbackend/backend.go:111-122`），**无** `ParentAgentID`/`ParentDisplayName`（本 spec 新增）。
- **Codex scanner**：`sessionsRoot()` 当前读 `os.Getenv`/`$HOME/.codex/sessions`（`pkg/agentbackend/codex/sessions.go:34-40`）——**进程级 env，非 backend 实例状态**（见 §1 修正）。`applyCodexSessionMeta`（`sessions.go:313-330`）只为 codex 原生 subagent 设 `ParentID`；对 `originator == "codex_exec"` 只设 `Origin=agent_task`，**从不设 `ParentID`**。核心缺口。
- **Codex 启动**：`runWithArgv`（`pkg/agentbackend/codex/executor.go:107`），`cmd.Env = append(cmd.Environ(), e.env…)`（`executor.go:128`）——注入 `CODEX_HOME` 给子进程的点；捕获 thread id（`sessionID = ev.ThreadID`，`executor.go:191`）——写 sidecar 的点。
- **slave 新建 codex exec**：`routes[""] = backendExecutor{backend}` → `backend.Run`（`cmd/slave-agent/main.go:242-245`）。`agentbackend.New(Config{…}, nil)` 第二参 `env` 当前 nil。
- **后端创建早于注册**：slave `agentbackend.New` 在 `:188`，`EnsureRegistered` 在 `:213`，`ShortID` 在 `:221` 才填 —— 首次启动时 backend 创建前 `short_id` 不可用（见 §1）。
- **Commander tree row** 已带 `Origin`/`ParentID`/`AgentName`/`AgentRole`（`internal/commanderhub/tree.go:26-44`），无 `ParentAgentID`/`ParentDisplayName`。
- **前端嵌套**：`DaemonSessionTree.tsx` `buildSessionNodes` 只嵌 `origin==='subagent' && parent_id`、仅单 daemon 内（`DaemonSessionTree.tsx:15-34`）。
- **派发契约冻结**：`agentsdk.DelegateTaskRequest`（外部 module）带 `Prompt`/`SystemContext`/`DelegationChain`；observer-hub spec 禁止改 agentserver → 来源链路搭 `SystemContext`。
- **配置层缺字段**：`internal/config/config.go:39` 与 `internal/driver/config.go:36` 的 `AgentConfig` 均无 `codex_home`。
- **稳定 agent 实例 id**：`Credentials.ShortID`（`internal/config/config.go:53`），`Register` 时分配（`internal/tunnel/tunnel.go:117`）、持久化到 config（`tunnel.go:113-119`）、已被用作 `Observer.AgentID`/缓存 key/peer 路由。`daemonID` 短暂（重连即变），不能当 key。

## 决策（已与用户确认 + review 修订）

- **隔离 = 每个 agent 实例用自己的 `CODEX_HOME`**（默认 `<loom_state_dir>/<short_id>/.codex`，可显式配 `codex_home`；fallback `$HOME/.codex`）。codex session/sidecar 落该实例私有目录 → 归属隐式、无需去重。
- **`agent_id` = `ShortID`**（per-agent-instance、稳定、唯一；非 machine 维度）。标签用 `display_name`。
- **scanner root 是 backend 实例状态**（`b.codexHome()`），**不是进程级 env**；`os.Getenv("CODEX_HOME")` 仅作 `resolveCodexHome` 的一个输入，scanner 不直接读 env（防测试污染/同进程多 backend 冲突）。
- **持久化 = sidecar**（`<CODEX_HOME>/loom-meta/<thread_id>.json`），codex executor 捕获 thread id 后写，scanner 合并；schema/origin/session_id 校验（见 §6）。
- **`ParentDisplayName` 必须进 `Session` 并贯通到前端**（parent 离线时 observer 无法 live 回查，只能靠 sidecar denormalized 字段）。
- **跨 daemon 嵌套 = 全量**，按 `(parent_agent_id, parent_session_id)` 解析。
- **传播载体 = `SystemContext`** 结构化标记，不改 agentserver。
- **双向链路**：driver 派发带 `(parent_agent_id, parent_session_id, parent_display_name)`；slave 把父级 + 自己的 `(child_agent_id, child_session_id)` 记进 sidecar；slave 输出 marker 返回 child session；driver 把 child 记进 task journal。
- **reaper**：删孤儿（无对应 rollout）+ 删 mtime 超 30 天的条目（常量 `loomMetaMaxAge = 30*24h`，按文件 mtime；P1 不开配置项）。
- **`created_at` fallback**：thread.started 事件 timestamp → `time.Now().UTC().Format(time.RFC3339Nano)`（Go 代码用 `time.Now()` 没问题；之前"热路径不用 Date.now"是 workflow 脚本约束，不适用 Go）。

## 设计

### 链路 tuple

```
(parent_agent_id, parent_session_id) ↔ (child_agent_id, child_session_id)
+ parent/child 各自的 display_name（标签，非 key）
```

- `agent_id` = 该 agent 实例（daemon）的 `ShortID`。
- **归属**：隐式 —— child 归属它所在 `CODEX_HOME` 的 owner agent（= 扫它的那个 daemon）。无需 sidecar 记 owner。
- **父级**：显式 —— `parent_agent_id`/`parent_session_id`/`parent_display_name` 由 sidecar 记。
- `display_name` 仅为给人看的标签；**解析一律用 `agent_id`**。`parent_display_name` denormalized 进 sidecar，因 parent daemon 离线时 observer 无法 live 回查。

### 0. 与 codex app-server hot worker 的关系（issue-23，已落 master）

master 已合入 codex app-server hot worker（`pkg/agentbackend/codex/appserver_manager.go` + `agentbackend.SessionWorkerBackend`/`HealthySessionWorker`，`backend.go:154-169`；`agentbackend.Config.WorkerMode`，`config.go:17`；`Handler.SessionTurn` 先查 `SessionWorkerBackend` → `NewSessionWorker`，否则 `RunResume`，`internal/commander/handler.go:85-105,137`）。本 spec 与它**正交**：

- app-server worker 只"恢复"已存在交互 session（`thread/resume`），**不创建** `codex_exec` agent_task session。issue #24 目标的 agent_task session 仍由 `Backend.Run` → executor `codex exec` 创建 —— sidecar 写入点不变。
- parent（交互）session 经 app-server 或 exec-resume 跑都行，其 codex thread id 就是 `parent_session_id`。
- **CODEX_HOME 一致性**：app-server design 的"Runtime context parity"已要求 worker 继承与 exec 路径相同的 `CODEX_HOME`，并纳入 worker context fingerprint。`newAppServerManager(cfg, env)`（`appserver_manager.go:426`）与 exec executor 共用 `env` —— 把 `CODEX_HOME` 注入 `agentbackend.New(cfg, env)` 会自动同时到达两条路径。

### 1. CODEX_HOME 解析（解决 review #2/#3/#4）

**loom state dir（权威来源）**：`cfg.LoomHome`（新可选配置）→ `$LOOM_HOME` env → `$HOME/.cache/multi-agent`（沿用 `internal/driver/slave_file_tools.go:238` 既有约定）。`LOOM_HOME` 当前仅出现在注释（`chat_resume.go:25`），本 spec 把它扶正为可选 env，并加 `cfg.LoomHome` 配置项。

**`resolveCodexHome(cfg)` 解析顺序**（纯函数，scanner 与 subprocess env 共用）：

1. `cfg.CodexHome`（显式配置，最高优先）。
2. `$CODEX_HOME` env（launcher 已隔离时）。
3. `<loom_state_dir>/<short_id>/.codex`，**仅当 `short_id` 已知**（`cfg.Credentials.ShortID` 非空，即已持久化）。
4. `""` → scanner/subprocess fallback `$HOME/.codex`。

**short_id 时序（review #2）**：`short_id` 在 `EnsureRegistered` 后才写入 `cfg.Credentials.ShortID` 并持久化；首次启动时 backend 创建前未知。因此：

- **首选**：deploy 模板显式设 `agent.codex_home`（首次启动即隔离，不依赖 short_id）。
- **次选**：launcher（driver/slave main）在 `EnsureRegistered` **之后**解析 `codex_home`（此时 short_id 已知），再 `agentbackend.New(cfg{CodexHome: resolved})`。**这要求把 `agentbackend.New` 移到 `EnsureRegistered` 之后**（slave 当前 `:188` 创建早于 `:213` 注册 —— P2 调整此顺序；driver 的 `register` 是独立子命令、daemon 启动时 short_id 已从 config 载入，不受影响）。
- 首次启动 + 无显式配置 + 未重排：fallback `$HOME/.codex`（隔离待首次注册+重启后生效）—— 可接受的过渡，文档写明。

**scanner root = backend 实例状态（review #4）**：

- `sessionsRoot` 改为 `*Backend` 方法：`func (b *Backend) sessionsRoot() string { base := b.codexHome(); if base=="" { base = defaultCodexHome() /* $HOME/.codex */ }; return filepath.Join(base, "sessions") }`。**不读 `os.Getenv`**。
- `loomMetaDir(base string)` / `loomMetaPath(base, threadID)` 接受解析后的 base（不读 env）。scanner 传 `b.codexHome()`，executor 传 `resolveCodexHome(e.cfg)`。
- 子进程 env：`withCodexHome(cfg, env)` append `CODEX_HOME=<resolveCodexHome(cfg)>`（子进程只能走 env）。
- 测试：设 `cfg.CodexHome = t.TempDir()` 即可，**无需 `t.Setenv`**，不污染进程 env、不冲突同进程多 backend。

**配置层贯通（review #3）**：`codex_home`（+ 可选 `loom_home`）需加到：`internal/config/config.go` 的 `AgentConfig`、`internal/driver/config.go` 的 `AgentConfig`、driver/slave YAML schema、`deploy/{linux,windows}/{driver,slave}/config.yaml.template`，并由 launcher 填入 `agentbackend.Config.CodexHome`。仅改 `agentbackend.Config` carrier 不够。此贯通属 P2（wiring），但 config 字段定义可与 P1 同期。

### 2. 把 agent 实例身份 + parent 标签暴露给 Commander

把 `ShortID`（=`agent_id`）与 `DisplayName` 贯通：

- `commander.RegisterPayload`（`internal/commander/protocol.go:35`）→ 加 `ShortID string`。`DisplayName` 已在。
- `commanderhub.daemonConn` + `DaemonInfo`（`internal/commanderhub/registry.go:23,38`）→ 加 `ShortID`；从 `RegisterPayload` 填（`hub.go:111-113`）。
- observer 维护 `agent_id(ShortID) → 当前 daemonConn` 映射：重连后 daemon_id 变、ShortID 不变，映射按 ShortID 重建；供按 `agent_id` 解析 parent、live 回查 display_name。
- `SessionRow`（`internal/commanderhub/tree.go:26`）→ 加 `OwnerAgentID`（daemon 上报时填自身 ShortID）、`ParentAgentID`、`ParentDisplayName`（后两者来自 Session）、`OwnerDisplayName?`（observer live 回查 owner daemon）。**`ParentDisplayName` 必须在 row 上**（parent 离线兜底）。
- 前端 `SessionRow`（`internal/commanderhub/webapp/src/api/types.ts:10`）→ 加对应字段。

**不需要 bump `commander.SchemaVersion`**：`ShortID` additive；observer 容忍老 daemon 缺该字段。

### 3. Session 模型：带上 parent 的 agent + display name（review #1）

扩展 `agentbackend.Session`（`pkg/agentbackend/backend.go:94`）：

- `ParentAgentID string` —— 拥有 `ParentID`（父 session）的 agent 实例 `ShortID`。`ParentID` 为空时为空。来自 sidecar。
- `ParentDisplayName string` —— parent 的 `display_name`，来自 sidecar denormalized 字段。**parent daemon 离线时 observer 无法 live 回查，必须靠此字段**。

保留 `ParentID` = 父级 session id（语义不变）。**不**加 `OwnerAgentID` —— 归属隐式（由扫它的 daemon 决定），`SessionRow.OwnerAgentID` 上报时填自身 ShortID。

### 4. 传播：driver → slave（正向链路）

- driver 一轮 turn 中知道自己当前 codex thread id + `agent_id`/`display_name`。接进 MCP tool-call 上下文（`Tools` handler 设 current-session 值，turn 结束清）。
- `submit_task`/shell/contract 派发 handler（`internal/driver/tools.go:511`、`contract_tools.go:100`、`slave_tools.go:150` 等）给 `DelegateTaskRequest.SystemContext` 打标记：`<loom_origin agent="<driver ShortID>" name="<driver display_name>" session="<当前 thread_id>" />`（boundary 标签 + escape，沿用 `codex/sessions.go:446` 纪律）。
- `executor.Task`（`internal/executor/executor.go:5`）加：`ParentSessionID`/`ParentAgentID`/`ParentDisplayName`。
- slave 派发桥（`cmd/slave-agent`/`internal/dispatch`）从 `SystemContext` 解析 `<loom_origin>` 填进 `Task` 字段，并**剥离标记**（维持 `tools.go:506-510` JSON-prompt 保护）。

### 5. Sidecar 持久化（含校验，review #6/#7/#8）

codex executor 捕获新 `sessionID`（`executor.go:191`）后写 sidecar：

- 路径：`<CODEX_HOME>/loom-meta/<thread_id>.json`（base = `resolveCodexHome(e.cfg)`）。
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
    "created_at": "<RFC3339Nano>"
  }
  ```
  - `parent_*` 来自 `Task` 字段。`parent_display_name` 故意 denormalized（parent 离线兜底）。不写 `owner_*`（归属隐式）。
  - **`created_at`（review #8）**：`thread.started` 事件 timestamp（若该事件带 ts）→ 否则 `time.Now().UTC().Format(time.RFC3339Nano)`。Go 代码用 `time.Now()` 无约束。
  - **写入校验（review #6）**：写前断言 `m.SessionID == sessionID`、`m.Schema == 1`、`m.Kind == "codex"`；不符则不写（防御）。
- 写入 best-effort：失败降级（session 照常列出，无 parent 链路），绝不阻塞 codex run。
- **reaper（review #7）**：scanner 全量 List 后跑：删 (a) 无对应 rollout 的孤儿，(b) 文件 mtime 早于 `now - loomMetaMaxAge`（`loomMetaMaxAge = 30*24*time.Hour`，常量，按 mtime 因 `created_at` 可能空；P1 不开配置项）。

### 6. Scanner：为 agent_task session 恢复 parent（含校验，review #4/#6）

`pkg/agentbackend/codex/sessions.go`：

- `sessionsRoot` 改 `*Backend` 方法，用 `b.codexHome()`（**不读 env**，见 §1）。
- `scanCodexSession` 构出 descriptor 后，合并 sidecar（base = `b.codexHome()`）：
  - **校验**：仅当 sidecar `schema==1` && `kind=="codex"` && `sidecar.session_id == sess.ID` 时才合并；否则 skip（坏/陈旧 sidecar 不影响）。
  - **origin 规则**：sidecar **只补 parent 字段，不改 `Origin`**。`Origin` 由 `applyCodexSessionMeta`（codex 原生 `parent_thread_id`/`originator`）决定。仅当 `sess.Origin == agent_task`（rollout 确为 codex_exec）时才把 sidecar 的 `ParentID`/`ParentAgentID`/`ParentDisplayName` 叠上（仅填空位，不覆盖 codex 原生 subagent 的 `ParentID`）。→ **sidecar 永远不会把 user session 重标成 agent_task**。
- 现有 `b.list`/`Prune` 文件缓存（`sessions.go:82,91`）以 rollout (path,size,mtime) 为 key，**把 sidecar mtime 并入 cache key**，sidecar 重写即让该 row 失效。

### 7. 反向链路：slave 结果 → driver journal

- slave chat 输出带 `session_id` kind marker，driver 经 `sessionIDFromMarker`（`internal/driver/tools.go:619-636,821-833,1101-1140`）解析。扩展 marker 带 `agent_id`（slave ShortID；driver 通常已从 `AgentCard` 知道，`tools.go:165-211`）。
- 扩展 `driver-tasks.jsonl` 记录（`tools.go:79` `delegatedTaskRecord`）加 `child_session_id` + `child_agent_id`，task 结果 marker 到达时写。反向链路：driver session 的 children 索引，持久在 driver 本地。

### 8. Observer + 前端：全量跨 daemon 嵌套（无需去重）

§1 per-agent `CODEX_HOME` 隔离 → 每个 session 只被唯一一个 daemon 扫到上报，**无重复**，observer 无需去重。

Observer（`internal/commanderhub/tree.go`）：

- 跨 daemon 嵌套建全局 parent 索引：key `(owner_agent_id, session_id)` → node，跨所有 daemon；带 `parent_id`+`parent_agent_id` 的 child 挂到 `(parent_agent_id, parent_id)` 下（即便 parent 在另一 daemon 分组）。
- 渲染：远程 child 嵌在 parent session 下（主位置），带 `remote` badge 标 display_name（如 `remote · on slave-02`）。child 从其归属 daemon 根列表省略（归在 parent 下）；`session_count` 仍计。

前端（`DaemonSessionTree.tsx` + `api/types.ts`）：

- `SessionRow` 加 `owner_agent_id`/`parent_agent_id`/`owner_display_name?`/`parent_display_name?`。
- 跨 daemon builder 替换单 daemon `buildSessionNodes`：聚合所有 session，建全局 `(owner_agent_id, session_id)` map，把 `origin==='subagent'||'agent_task'` 且带 `parent_id` 的 child 嵌到解析出的 parent node 下，默认折叠。
- 离线/缺失 parent：无节点匹配 `(parent_agent_id, parent_id)` → child 当根行渲染并附 `parent offline`（带 `parent_display_name`，来自 sidecar denormalized）灰色注记。
- Badge：远程 `agent_task` 行 `remote task · on <owner display_name>`；本地 subagent 保留 `subagent · <name>`。

### 分阶段（3 PR）

1. **P1 — 后端记录 + scanner + 隔离（无 UI、无传播；测试手填 sidecar）。** `Session.ParentAgentID`/`ParentDisplayName`；`Config.CodexHome`（+ `LoomHome`）；codex executor 写 sidecar（含校验/created_at fallback）+ 注入 `CODEX_HOME`；codex scanner 用 `b.codexHome()`（实例状态）合并 sidecar（含校验）+ cache key + reaper（30d）；`Task` parent 字段；config struct 字段定义。
2. **P2 — 传播 + 配置贯通。** register→DaemonInfo→SessionRow 带 `ShortID`/`DisplayName`/`ParentDisplayName`；driver 当前 session-id 接线 + `<loom_origin>` 打标；slave 解析进 `Task`；反向 marker + `driver-tasks.jsonl` child 字段；**launcher 在 `EnsureRegistered` 后解析 `codex_home` 并移 `agentbackend.New` 到注册之后**；`internal/config`/`internal/driver`/YAML/deploy 模板加 `codex_home` 并传入。
3. **P3 — Commander 嵌套。** observer 全局 parent 索引；前端跨 daemon `buildSessionNodes` 重写、`remote`/`parent offline` badge（display_name）、默认折叠；Playwright。

### 关键文件

- `pkg/agentbackend/backend.go` — 加 `ParentAgentID`/`ParentDisplayName`。
- `pkg/agentbackend/config.go` — `Config` 加 `CodexHome`/`LoomHome`（与 `WorkerMode` 同 carrier，`:17`）。
- `pkg/agentbackend/codex/executor.go` — 注入 `CODEX_HOME` 进 `e.env`；`sessionID` 捕获后写 sidecar（校验 + created_at fallback）；从 `Task` 读 parent。
- `pkg/agentbackend/codex/appserver_manager.go` — 复用同 `env`（`:426`），确保 app-server 路径与 exec 同 `CODEX_HOME`；不改协议。
- `pkg/agentbackend/codex/sessions.go` — `sessionsRoot` 改 `*Backend` 方法用 `b.codexHome()`（不读 env）；List/Get 合并 sidecar（校验 + origin 规则）；cache key 纳 sidecar mtime；reaper（孤儿 + 30d mtime）。
- `pkg/agentbackend/codex/loommeta.go` — **新**：`loomMeta` 类型、`loomMetaDir(base)`/`loomMetaPath(base,id)`、`writeLoomMeta`/`readLoomMeta`/`reaper`（均接受 base，不读 env）。
- `internal/executor/executor.go` — `Task` 加 `ParentSessionID`/`ParentAgentID`/`ParentDisplayName`。
- `internal/config/config.go` + `internal/driver/config.go` — `AgentConfig` 加 `codex_home`/`loom_home`（P1 定义字段，P2 贯通）。
- `internal/commander/protocol.go` — `RegisterPayload` 加 `ShortID`。
- `internal/commanderhub/registry.go` + `hub.go` + `tree.go` — 贯通 `ShortID`/`ParentAgentID`/`ParentDisplayName`；`agent_id → daemonConn` 映射；全局 parent 索引；`SessionRow.OwnerAgentID` 上报填自身。
- `internal/commanderhub/webapp/src/api/types.ts` + `components/DaemonSessionTree.tsx` — 跨 daemon 嵌套 + badge（display_name）。
- `internal/driver/tools.go`（含 `contract_tools.go`/`slave_tools.go`/`register_mcp_tool.go`/`slave_file_tools.go`）— current-session 接线 + `<loom_origin>` 打标；反向 marker 解析；扩 `delegatedTaskRecord`。
- `cmd/slave-agent/main.go` + `cmd/driver-agent/main.go` — `agentbackend.New` 移到 `EnsureRegistered` 之后；解析 `codex_home` 传入 `Config.CodexHome`（P2）。
- `deploy/{linux,windows}/{driver,slave}/config.yaml.template` + `dev/configs/*.example.yaml` — 加 `codex_home`（P2）。

### 测试

- **隔离**：两个 `cfg.CodexHome` 各跑独立 `*Backend` scanner，互不见对方 session；codex 子进程写进指定 `CODEX_HOME/sessions`；app-server 路径同 `CODEX_HOME`（隔离一致性，不被旁路）。**不依赖 `t.Setenv`**。
- **scanner 实例状态（review #4）**：同进程内两个 `Backend`（不同 `cfg.CodexHome`）各自 `ListSessions` 互不串扰（验证不读进程 env）。
- **Sidecar 校验（review #6）**：坏 sidecar（schema/kind/session_id 不符）→ skip，session 不受影响；sidecar 不把 `user` session 重标 `agent_task`；`agent_task` session 才叠 parent；codex 原生 subagent 的 `ParentID` 不被 sidecar 覆盖。
- **Scanner**：fixture rollout + sidecar → `Origin=agent_task`、`ParentID`/`ParentAgentID`/`ParentDisplayName` 非空；无 sidecar → parent 空（不失败）；sidecar mtime 变 → cache 失效；reaper 删孤儿 + 删 30d 前。
- **Executor**：`thread.started` 后写正确字段 sidecar（owner 取自本进程 cfg）；`created_at` 在事件无 ts 时 fallback `time.Now()`；best-effort 写失败不破 run；marker 解析进 `Task` 且从 prompt 剥离。
- **传播**：driver 打 `SystemContext`；slave 解析 + 剥离；JSON-prompt skill 仍解析（`tools.go:506` 保护保持绿）。
- **反向链路**：`sessionIDFromMarker` 取 child session + agent_id；`driver-tasks.jsonl` 多出 child 字段。
- **ParentDisplayName 传输（review #1）**：sidecar `parent_display_name` → `Session.ParentDisplayName` → `SessionRow` → 前端；parent daemon 离线时 badge 仍显示该名。
- **Commander**：observer 解析跨 daemon parent；远程 child 带 badge 嵌在 parent 下、从 home 根列表省略；离线 parent 渲染 `parent offline`（带 display_name）；本地 subagent 嵌套不变。Playwright：`driver → remote task · on slave-02`。
- **回归**：`go test ./... -race -count=1`；`npm test`/`npm run build`（`assets/dist` build 后无 diff）。

### 验收标准（按阶段，review #5）

**P1 验收（无传播、无 UI；手填 sidecar 可达）**

- `Session` 有 `ParentAgentID`/`ParentDisplayName`；`Config` 有 `CodexHome`/`LoomHome`；`Task` 有 parent 三字段。
- scanner 用 `b.codexHome()`（实例状态，不读 env）读 `CODEX_HOME/sessions`；合并校验过的 sidecar 设 `ParentID`/`ParentAgentID`/`ParentDisplayName`（仅 agent_task session）；cache 随 sidecar mtime 失效；reaper 删孤儿 + 30d。
- codex executor 写 sidecar（校验 + created_at fallback）并注入 `CODEX_HOME` 给子进程。
- `go test ./... -race` 绿；隔离/校验/实例状态测试通过。

**P2 验收（传播 + 配置贯通）**

- driver 起的 exec session 暴露非空 `parent_id`/`parent_agent_id`/`parent_display_name`（来源 driver session）。
- slave 起的 exec session 暴露非空 `parent_id`/`parent_agent_id`/`parent_display_name`（派发的 driver session）。
- 每个 agent 用自己的 `CODEX_HOME`（launcher 注册后解析、`agentbackend.New` 移到注册后）；`internal/config`/`internal/driver`/YAML/deploy 贯通 `codex_home`。
- `register`→`DaemonInfo`→`SessionRow` 带 `ShortID`/`DisplayName`/`ParentDisplayName`；`driver-tasks.jsonl` 记 child。

**P3 验收（= issue #24 最终）**

- Commander session 树默认把远程 task session 嵌在 parent session 下（跨 daemon、带 `remote` badge + display_name），parent 离线显示 `parent offline` + display_name。
- 一机多 agent 各自 `CODEX_HOME` 互不串扰、不重复。
- 现有本地 subagent 嵌套不变。

### 风险

- **既有 `~/.codex` session 迁移**：切 per-agent `CODEX_HOME` 后老 session 不再归属/显示。Mitigation：scanner fallback `$HOME/.codex`（`CODEX_HOME` 未设时）；已切 agent 视老 session 为未标注根行。
- **首次启动 short_id 未知**：无显式 `codex_home` + 未重排 `New` 时 fallback `$HOME/.codex`，隔离待首次注册+重启。Mitigation：deploy 模板显式设 `codex_home`。
- **Sidecar 孤儿/陈旧**：reaper 删无 rollout 的 + 30d 前；scanner skip 校验不符的。
- **parent 离线**：渲染 `parent offline`（+ parent display_name，来自 sidecar denormalized），绝不丢 child。
- **`agent_id`(ShortID) 漂移**：config 清/强制重注册 → `ShortID` 变、旧链路悬空；可接受（与系统其余路由同模型）。
- **`display_name` 非唯一**：仅标签，绝不作 key；解析一律 `agent_id`。
- **标记漏进 prompt**：`<loom_origin>` 进 codex prompt 前必剥离，否则 JSON-prompt skill 破。
- **codex 不认 `CODEX_HOME`**：预飞 probe 确认；不认则按版本用 config flag。
- **codex rollout 路径随版本变**：sidecar 以 thread id 为 key，不依赖日期型 rollout 路径。

### ShortID 唯一性证明（备查）

- `agentserver/internal/shortid/shortid.go`：`Generate()` = 8 字符、36 字母表、`crypto/rand`（熵 36⁸ ≈ 2.8e12，~41.4 bit），非主机名派生。
- `internal/db/migrations/001_initial.sql:100`：`CREATE UNIQUE INDEX idx_sandboxes_short_id ON sandboxes (LOWER(short_id)) WHERE short_id IS NOT NULL;` —— 全局唯一。
- `internal/server/agent_register.go:94-102`：collision → `Generate()` 重试最多 3 次。

### Pre-flight probes（写 P1/P2 代码前）

- 确认目标 codex 版本认 `CODEX_HOME` 且 session 落 `<CODEX_HOME>/sessions/`（`codex --help` / 实跑 exec 看 rollout 落点）。
- 确认 `thread.started` 事件是否带 timestamp（决定 `created_at` 是否需 `time.Now()` fallback）。
- 确认 driver/slave prod_test config `ShortID` 非空、重启持久；一机多 agent 各自 `ShortID` 不同。
- 确认 codex backend executor 是 driver/slave 上创建 `codex_exec` rollout 的唯一路径。
- 确认 driver 在 MCP tool-call 时能拿到当前 codex thread id。
- 确认 `EnsureRegistered` 后 `cfg.Credentials.ShortID` 已填、可据其解析 `codex_home`（P2 重排 `agentbackend.New` 顺序的可行性）。
