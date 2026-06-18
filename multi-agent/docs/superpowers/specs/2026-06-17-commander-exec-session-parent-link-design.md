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
- **scanner root 是 backend 实例状态**（`b.effectiveCodexHome()`），**不是进程级 env**；`resolveCodexHome(cfg, env)` 同时看 `cfg.CodexHome`、传入的 `env` slice、进程 env（防 caller 传 `[]string{"CODEX_HOME=…"}` 被漏掉）；scanner 不直接读 env。
- **`CodexHome` vs `LoomHome` 位置**：`agentbackend.Config` **只**加 `CodexHome`（backend 只需要最终路径，**不**需要 `ShortID`/`LoomHome`）；`<loom_state_dir>/<short_id>/.codex` 的 short_id 默认**只在 launcher 解析**（launcher 知道 `cfg.Credentials.ShortID`），解析后把绝对路径填进 `cfg.Agent.CodexHome` → `agentbackend.Config.CodexHome`。`loom_home` 是 deploy 级输入，加到 `internal/{config,driver}.AgentConfig`（**不**进 `agentbackend.Config`）。
- **`effectiveCodexHome(cfg, env)`** = `resolveCodexHome(cfg, env)`，为空时回退 `$HOME/.codex`；仅当 `os.UserHomeDir()` 也失败时返回 `""`（极罕见，scanner no-op、sidecar writer skip，不静默写 temp）。scanner 与 sidecar writer **都用它**。子进程 env：`New` 用**原始** cfg/env 先 resolve 出最终值（**不能先删 env slice**），再从 env slice **和** `os.Environ()` 全量去重既有 `CODEX_HOME`（大小写不敏感），仅当最终值 ≠ 默认时插入一条；executor/llm/app-server 共用此 resolved env（`mergeEnv` 覆盖 `os.Environ` 同键），避免重复 key。
- **app-server env 贯通**：`New(cfg, env)` 把 `withCodexHome` 解析后的 env **存为 backend 实例状态**；`workerBackend` 用这个 **resolved env**（不是原始 `env`）建 `newAppServerManager`，确保 exec 与 app-server 两路径同 `CODEX_HOME`（修当前 `backend.go:86` 传原始 env 的漏）。
- **持久化 = sidecar**（`<effectiveCodexHome>/loom-meta/<thread_id>.json`），codex executor 捕获 thread id 后写，scanner 合并；校验 `schema==1 && kind=="codex" && origin=="agent_task" && session_id==文件名`（见 §6）。
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
- **CODEX_HOME 一致性**：app-server design 的"Runtime context parity"已要求 worker 继承与 exec 路径相同的 `CODEX_HOME`，并纳入 worker context fingerprint。`newAppServerManager(cfg, env)`（`appserver_manager.go:426`）与 exec executor **应共用同一份 resolved env** —— 本 spec 要求 `New` 把 `withCodexHome` 解析后的 env 存为实例状态，`workerBackend` 用它建 manager（修当前 `backend.go:86` 传原始 env 的漏，见 §1）。

### 1. CODEX_HOME 解析（解决 review #2/#3/#4 + blocker #1/#2）

**分层：launcher 解析 short_id 默认，backend 只收最终 `CodexHome`（blocker #1）。** `agentbackend.Config` **不加** `ShortID`/`LoomHome`（backend 不该知道身份/部署路径）；它**只**加 `CodexHome`（最终绝对路径）。`<loom_state_dir>/<short_id>/.codex` 的 short_id 默认**只在 launcher**（driver/slave main，它有 `cfg.Credentials.ShortID`）解析，结果填进 `cfg.Agent.CodexHome` → `agentbackend.Config.CodexHome`。

**`loom_home` 位置（blocker #2）：** `loom_home` 是 deploy 级输入，加到 `internal/config/config.go` 与 `internal/driver/config.go` 的 `AgentConfig`（**不**进 `agentbackend.Config`）。`loom_state_dir` = `cfg.Agent.LoomHome` → `$LOOM_HOME` env → `$HOME/.cache/multi-agent`（沿用 `internal/driver/slave_file_tools.go:238` 既有约定）。`LOOM_HOME` 当前仅注释出现（`chat_resume.go:25`），本 spec 扶正为可选 env。

**launcher 侧解析（short_id 默认在这里发生；driver/slave 时序不同，分开写）**：

```text
launcherCodexHome(cfg):
  if cfg.Agent.CodexHome != ""           → 用它
  else if short_id 已知                   → <loom_state_dir>/<short_id>/.codex
  else                                   → ""（交由 backend fallback $HOME/.codex）
```

- **首选**：deploy 模板显式设 `agent.codex_home`（首次启动即隔离，不依赖 short_id）。
- **slave**（`cmd/slave-agent/main.go`）：`agentbackend.New` 在 `:188`、`EnsureRegistered` 在 `:213`、`ShortID` 在 `:221` 才填 → **P2 把 `agentbackend.New` 移到 `EnsureRegistered` 之后**，再用已知 short_id 解析 `codex_home` 填 `cfg.Agent.CodexHome`。
- **driver**（`cmd/driver-agent/main.go`）：`register` 是**独立子命令**，早已把 `ShortID` 持久化进 config；`serve-mcp`（`:193`）与 `serve-daemon`（`:330`）启动时从 config 载入 `ShortID`，**无 `EnsureRegistered` 流程可重排**。故 driver 启动时：若 `cfg.Credentials.ShortID` 非空 → 用它解析 short_id 默认；若为空（未 register 过）→ **报配置错误**（"先跑 register"）或 fallback `$HOME/.codex`，**不要**写"driver 也移到 EnsureRegistered 后"。
- 首次启动 + 无显式配置 + slave 未重排：backend fallback `$HOME/.codex`（隔离待首次注册+重启生效）—— 可接受过渡，文档写明。

**backend 侧解析（纯函数，不看 short_id）**：

```go
// resolveCodexHome: cfg.CodexHome → env slice 里的 CODEX_HOME → 进程 env → ""
// env slice 必须看：caller 传 []string{"CODEX_HOME=..."} 时 os.Getenv 会漏（major）。
func resolveCodexHome(cfg agentbackend.Config, env []string) string {
    if cfg.CodexHome != "" { return cfg.CodexHome }
    if v := envValue(env, "CODEX_HOME"); v != "" { return v }
    return os.Getenv("CODEX_HOME")
}

// effectiveCodexHome: resolveCodexHome，为空回退 $HOME/.codex。
// scanner 与 sidecar writer 都用它（major：fallback 对 sidecar 闭环）。
// 失败行为（minor）：若 resolve 为空且 os.UserHomeDir() 也失败（极罕见），
// 返回 "" —— scanner 走 no-op（ListSessions 返空、不报错），sidecar writer
// best-effort skip（不写、不影响 codex run）。不 fallback 到 temp dir（避免
// 静默写到意外位置）。
func effectiveCodexHome(cfg, env) string {
    if r := resolveCodexHome(cfg, env); r != "" { return r }
    home, err := os.UserHomeDir()
    if err != nil || home == "" { return "" }
    return filepath.Join(home, ".codex")
}
```

**scanner root = backend 实例状态（review #4）**：

- `sessionsRoot` 改 `*Backend` 方法：`func (b *Backend) sessionsRoot() string { base := b.effectiveCodexHome(); if base == "" { return "" /* no-op */ }; return filepath.Join(base, "sessions") }`。**不读 `os.Getenv`**（env 已在 `New` 时解析进实例状态）。
- `loomMetaDir(base)` / `loomMetaPath(base, id)` 接受解析后 base（不读 env）。scanner 传 `b.effectiveCodexHome()`，executor 传 `effectiveCodexHome(e.cfg, e.env)`；`base==""` 时 loom-meta 写入 skip。
- 子进程 env 构造（major，**resolve-then-strip 顺序 + 全量去重**）：
  - 当前 executor/llm/app-server 都是 `cmd.Env = append(cmd.Environ(), <env>...)`（`executor.go:128`、`llm.go:27`）——若进程环境已有 `CODEX_HOME=/old` 而 resolved env 给 `/new`，子进程会有**重复 key**（OS 取最后一条，脆弱且跨平台不清）。
  - `New` 先用**原始** `cfg, env` 调 `resolveCodexHome` 得最终值 `final`（**不能先删 env slice**——否则 `env=[]string{"CODEX_HOME=/x"}` + `cfg` 空时会被删成空、fallback `$HOME/.codex`，丢了 caller 传入的值）。
  - 再构造 resolved env：从传入 `env` slice **和** `cmd.Environ()`/`os.Environ()` 里**都**移除既有的 `CODEX_HOME=…`（大小写不敏感：`Codex_Home`/`CODEX_HOME` 同键），然后仅当 `final != 默认 $HOME/.codex` 时 append 一条 `CODEX_HOME=<final>`。
  - 落地方式：`New` 把 resolved env **存为 `b.env`**；executor/llm 改为 `cmd.Env = mergeEnv(os.Environ(), b.env)`（`mergeEnv` 去重：b.env 的键覆盖 os.Environ 同键，大小写不敏感），而不是裸 `append(cmd.Environ(), env...)`。app-server manager 同样用 `b.env`。
  - 结果：子进程恰好一条 `CODEX_HOME`（或零条，当 `final` == 默认）；exec / app-server / llm 三路径一致。
- **app-server env 贯通（major）**：`workerBackend` 用 `b.env`（**不是**原始 `env`）建 `newAppServerManager(b.cfg, b.env)`，修当前 `backend.go:86` 传原始 env 的漏。
- 测试：设 `cfg.CodexHome = t.TempDir()` 即可，**无需 `t.Setenv`**，不污染进程 env、不冲突同进程多 backend；并测 `mergeEnv` 全量去重（`os.Environ()` 含 `CODEX_HOME=/old` + `b.env` 含 `CODEX_HOME=/new` → 子进程 env 只有一条 `CODEX_HOME=/new`；大小写变体 `Codex_Home` 也被覆盖；`final==默认` 时零条）；测 resolve-then-strip 顺序（`env=["CODEX_HOME=/x"]` + `cfg` 空 → `final=/x`，不被误删）。

**配置层贯通（review #3）**：`codex_home` + `loom_home` 加到：`internal/config/config.go` 的 `AgentConfig`、`internal/driver/config.go` 的 `AgentConfig`、driver/slave YAML schema、`deploy/{linux,windows}/{driver,slave}/config.yaml.template`，launcher 据其填 `agentbackend.Config.CodexHome`（short_id 默认）或直传 `codex_home`。**`agentbackend.Config` 只加 `CodexHome`，不加 `LoomHome`/`ShortID`。** 此贯通属 P2（wiring），config 字段定义可与 P1 同期。

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

codex executor 在 `Run`（新建 session 路径）捕获新 `sessionID`（`executor.go:191`）后写 sidecar：

- 路径：`<effectiveCodexHome>/loom-meta/<thread_id>.json`（base = `effectiveCodexHome(e.cfg, e.env)`；`base==""` 时 skip 写入）。
- **只在新 session 路径写，RunResume 绝不写（major）**：`Run` 与 `RunResume` 共享 `runWithArgv`（`executor.go:106`），两者都会收 `thread.started`。sidecar 写入**必须放在 `Run` 专属逻辑里**（或 runWithArgv 接收一个 `newSession bool` 参数，仅 `Run` 传 true），**不能**放在共享的 thread-capture 分支里——否则 resume 的交互 session 会被误标成 `agent_task`。测试钉死：`RunResume` 后无 loom-meta 生成/修改。
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
  - `parent_*` 取自 `Task` 字段；`parent_display_name` 故意 denormalized（parent 离线兜底）。owner 由扫该 `CODEX_HOME` 的 daemon 隐式决定，**不写 `owner_*`**。
  - **`created_at`（review #8）**：`thread.started` 事件 timestamp（若该事件带 ts）→ 否则 `time.Now().UTC().Format(time.RFC3339Nano)`。Go 代码用 `time.Now()` 无约束。
  - **写入校验（review #6）**：写前断言 `m.SessionID == sessionID`、`m.Schema == 1`、`m.Kind == "codex"`、`m.Origin == "agent_task"`；不符则不写（防御）。
- 写入 best-effort：失败降级（session 照常列出，无 parent 链路），绝不阻塞 codex run。
- **reaper（review #7）**：scanner 全量 List 后跑：删 (a) 无对应 rollout 的孤儿，(b) 文件 mtime 早于 `now - loomMetaMaxAge`（`loomMetaMaxAge = 30*24*time.Hour`，常量，按 mtime 因 `created_at` 可能空；P1 不开配置项）。

### 6. Scanner：为 agent_task session 恢复 parent（含校验，review #4/#6）

`pkg/agentbackend/codex/sessions.go`：

- `sessionsRoot` 改 `*Backend` 方法，用 `b.effectiveCodexHome()`（**不读 env**，见 §1）。
- `scanCodexSession` 构出 descriptor 后，合并 sidecar（base = `b.effectiveCodexHome()`）：
  - **校验**：仅当 sidecar `schema==1` && `kind=="codex"` && `origin=="agent_task"` && `sidecar.session_id == sess.ID` 时才合并；否则 skip（坏/陈旧 sidecar 不影响）。
  - **origin 规则**：sidecar **只补 parent 字段，不改 `Origin`**。`Origin` 由 `applyCodexSessionMeta`（codex 原生 `parent_thread_id`/`originator`）决定。仅当 `sess.Origin == agent_task`（rollout 确为 codex_exec）时才把 sidecar 的 `ParentID`/`ParentAgentID`/`ParentDisplayName` 叠上（仅填空位，不覆盖 codex 原生 subagent 的 `ParentID`）。→ **sidecar 永远不会把 user session 重标成 agent_task**。
- 现有 `b.list`/`Prune` 文件缓存（`sessions.go:82,91`；`internal/sessioncache/file_cache.go:29,55`）：`Get(path, info, scan)` 与 `Prune(seen)` 用**同一个 key**。若把 sidecar mtime 拼进 `Get` 的 key，**`seen` 也必须用同一 composite key**，否则 `Prune` 每次扫都把该条目裁掉（major，见 `file_cache.go:55`）。两种等价实现任选其一并加测试：(a) `Get` 与 `seen` 都用 `path + "|" + sidecarMtime` 作 key；(b) 扩展 `FileCache` 支持 salt（`Get(path, salt, info, scan)` + `Prune(seen)` where seen key = `path|salt`）。sidecar 重写 → mtime 变 → key 变 → row 失效。

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

1. **P1 — 后端记录 + scanner + 隔离（无 UI、无传播；测试手填 sidecar）。** `Session.ParentAgentID`/`ParentDisplayName`；`agentbackend.Config.CodexHome`（**不**含 `LoomHome`/`ShortID`）；**`internal/{config,driver}.AgentConfig` 只加 `codex_home`/`loom_home` struct 字段（定义，不接线）**；codex executor **仅 `Run`（非 `RunResume`）**写 sidecar（含校验/created_at fallback）+ resolve-then-strip + 全量去重（env slice + `os.Environ`，大小写不敏感）注入 `CODEX_HOME` + `b.env` 贯通 exec/llm/app-server；codex scanner 用 `b.effectiveCodexHome()`（实例状态）合并 sidecar（含校验）+ cache key（`Get`/`seen` 一致）+ reaper（30d）；`Task` parent 字段。
2. **P2 — 传播 + launcher wiring（driver/slave 分开）。** register→DaemonInfo→SessionRow 带 `ShortID`/`DisplayName`/`ParentDisplayName`；driver 当前 session-id 接线 + `<loom_origin>` 打标；slave 解析进 `Task`；反向 marker + `driver-tasks.jsonl` child 字段；**slave**：`EnsureRegistered` 后用 short_id 解析 `codex_home` 并把 `agentbackend.New` 移到注册之后；**driver**：启动时从已持久化 config 读 `ShortID` 解析（无 `EnsureRegistered` 可重排，`ShortID` 空则报"先 register"或 fallback）；YAML schema + `deploy/` 模板 + `dev/configs` 填 `codex_home`/`loom_home` 并传入 `agentbackend.Config.CodexHome`。
3. **P3 — Commander 嵌套。** observer 全局 parent 索引；前端跨 daemon `buildSessionNodes` 重写、`remote`/`parent offline` badge（display_name）、默认折叠；Playwright。

### 关键文件

- `pkg/agentbackend/backend.go` — 加 `ParentAgentID`/`ParentDisplayName`。
- `pkg/agentbackend/config.go` — `Config` **只**加 `CodexHome`（与 `WorkerMode` 同 carrier，`:17`）；**不加** `LoomHome`/`ShortID`（blocker #1/#2）。
- `pkg/agentbackend/codex/backend.go` — `New(cfg, env)` 先 `env = withCodexHome(cfg, env)` 存 `b.env` 实例字段；`resolveCodexHome(cfg, env)`/`effectiveCodexHome(cfg, env)`；`workerBackend` 用 `b.env` 建 manager（修 `:86`）。
- `pkg/agentbackend/codex/executor.go` — 用 `effectiveCodexHome(e.cfg, e.env)` 算 sidecar base；`sessionID` 捕获后写 sidecar（校验 + created_at fallback）；从 `Task` 读 parent。
- `pkg/agentbackend/codex/appserver_manager.go` — 由 `workerBackend` 传 backend 的 **resolved `b.env`**（`:426`），确保 app-server 与 exec 同 `CODEX_HOME`；不改协议。
- `pkg/agentbackend/codex/sessions.go` — `sessionsRoot` 改 `*Backend` 方法用 `b.effectiveCodexHome()`（不读 env）；List/Get 合并 sidecar（校验 + origin 规则）；cache key 纳 sidecar mtime；reaper（孤儿 + 30d mtime）。
- `pkg/agentbackend/codex/loommeta.go` — **新**：`loomMeta` 类型、`loomMetaDir(base)`/`loomMetaPath(base,id)`、`writeLoomMeta`/`readLoomMeta`/`reaper`（均接受 base，不读 env）。
- `internal/executor/executor.go` — `Task` 加 `ParentSessionID`/`ParentAgentID`/`ParentDisplayName`。
- `internal/config/config.go` + `internal/driver/config.go` — `AgentConfig` 加 `codex_home`/`loom_home`（P1 定义字段，P2 贯通）。
- `internal/commander/protocol.go` — `RegisterPayload` 加 `ShortID`。
- `internal/commanderhub/registry.go` + `hub.go` + `tree.go` — 贯通 `ShortID`/`ParentAgentID`/`ParentDisplayName`；`agent_id → daemonConn` 映射；全局 parent 索引；`SessionRow.OwnerAgentID` 上报填自身。
- `internal/commanderhub/webapp/src/api/types.ts` + `components/DaemonSessionTree.tsx` — 跨 daemon 嵌套 + badge（display_name）。
- `internal/driver/tools.go`（含 `contract_tools.go`/`slave_tools.go`/`register_mcp_tool.go`/`slave_file_tools.go`）— current-session 接线 + `<loom_origin>` 打标；反向 marker 解析；扩 `delegatedTaskRecord`。
- `cmd/slave-agent/main.go`（P2）— `agentbackend.New` 移到 `EnsureRegistered` 之后，用已知 short_id 解析 `codex_home` 传入 `Config.CodexHome`。
- `cmd/driver-agent/main.go`（P2）— driver **无** `EnsureRegistered` 流程（`register` 是独立子命令、ShortID 已持久化进 config）；`serve-mcp`/`serve-daemon` 启动时从 config 读 `ShortID` 解析 `codex_home`，空则报"先跑 register"或 fallback `$HOME/.codex`。**不**移 `New` 到注册之后。
- `deploy/{linux,windows}/{driver,slave}/config.yaml.template` + `dev/configs/*.example.yaml` — 加 `codex_home`（P2）。

### 测试

- **隔离**：两个 `cfg.CodexHome` 各跑独立 `*Backend` scanner，互不见对方 session；codex 子进程写进指定 `CODEX_HOME/sessions`；app-server 路径同 `CODEX_HOME`（隔离一致性，不被旁路）。**不依赖 `t.Setenv`**。
- **scanner 实例状态（review #4）**：同进程内两个 `Backend`（不同 `cfg.CodexHome`）各自 `ListSessions` 互不串扰（验证不读进程 env）。
- **Sidecar 校验（review #6）**：坏 sidecar（schema/kind/origin/session_id 不符）→ skip，session 不受影响；sidecar 不把 `user` session 重标 `agent_task`；`agent_task` session 才叠 parent；codex 原生 subagent 的 `ParentID` 不被 sidecar 覆盖。
- **env-slice 解析（major）**：`resolveCodexHome(cfg, []string{"CODEX_HOME=/x"})` 返回 `/x`（验证不漏 caller 传入的 env）；`effectiveCodexHome` 为空时回退 `$HOME/.codex`（sidecar 有落点）。
- **`withCodexHome` 去重（major）**：env slice 已有 `CODEX_HOME=/old` 且 `cfg.CodexHome=/new` → resolved env 只含一条 `CODEX_HOME=/new`（无重复）；大小写变体（`Codex_Home`）也只保留一条；解析值 == 默认 `$HOME/.codex` 时不注入。
- **Scanner**：fixture rollout + sidecar → `Origin=agent_task`、`ParentID`/`ParentAgentID`/`ParentDisplayName` 非空；无 sidecar → parent 空（不失败）；sidecar mtime 变 → cache 失效；reaper 删孤儿 + 删 30d 前。**cache key 一致性（major）**：`Get` 与 `seen`/`Prune` 用同一 composite key，连续两次 `ListSessions` 第二次命中 cache（不被 `Prune` 误删）。
- **Executor**：`thread.started` 后写正确字段 sidecar（parent 字段取自 `Task`；owner 由扫该 `CODEX_HOME` 的 daemon 隐式决定）；`created_at` 在事件无 ts 时 fallback `time.Now()`；best-effort 写失败不破 run；marker 解析进 `Task` 且从 prompt 剥离。
- **RunResume 不写 sidecar（major）**：`RunResume` 后 `loom-meta/<id>.json` 不生成、不被修改（钉死 sidecar 写入只在 `Run` 新建路径，不落共享 `runWithArgv` 的 thread-capture 分支）。
- **传播**：driver 打 `SystemContext`；slave 解析 + 剥离；JSON-prompt skill 仍解析（`tools.go:506` 保护保持绿）。
- **反向链路**：`sessionIDFromMarker` 取 child session + agent_id；`driver-tasks.jsonl` 多出 child 字段。
- **ParentDisplayName 传输（review #1）**：sidecar `parent_display_name` → `Session.ParentDisplayName` → `SessionRow` → 前端；parent daemon 离线时 badge 仍显示该名。
- **Commander**：observer 解析跨 daemon parent；远程 child 带 badge 嵌在 parent 下、从 home 根列表省略；离线 parent 渲染 `parent offline`（带 display_name）；本地 subagent 嵌套不变。Playwright：`driver → remote task · on slave-02`。
- **回归**：`go test ./... -race -count=1`；`npm test`/`npm run build`（`assets/dist` build 后无 diff）。

### 验收标准（按阶段，review #5）

**P1 验收（无传播、无 UI；手填 sidecar 可达）**

- `Session` 有 `ParentAgentID`/`ParentDisplayName`；`agentbackend.Config` 有 `CodexHome`（**不**含 `LoomHome`/`ShortID`）；`internal/{config,driver}.AgentConfig` 有 `codex_home`/`loom_home`；`Task` 有 parent 三字段。
- scanner 用 `b.effectiveCodexHome()`（实例状态，不读 env）读 `<base>/sessions`；合并校验过的 sidecar 设 `ParentID`/`ParentAgentID`/`ParentDisplayName`（仅 agent_task session）；cache 随 sidecar mtime 失效；reaper 删孤儿 + 30d。
- codex executor **仅 `Run`**（非 `RunResume`）写 sidecar（校验 + created_at fallback）；`RunResume` 不创建/不修改 loom-meta。子进程 env 经 resolve-then-strip + 全量去重（env slice + `os.Environ`，大小写不敏感）注入恰好一条 `CODEX_HOME`（或默认时零条）；exec/llm/app-server 共用 `b.env`。
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
- **确认 `Backend.Run` 产生的 rollout 被 scanner 识别为 `OriginAgentTask`（minor，关键前提）**：实跑一次 `codex exec`，看 `session_meta.originator` 是否为 `codex_exec`（或 `source.kind=="exec"`），使 `applyCodexSessionMeta` 归类 `agent_task`。否则 sidecar 即使存在也不会合并 parent（合并门控 `sess.Origin==agent_task`）。若该版本 codex 不写 `originator`，P1 需先补这个识别，否则整个链路落空。
- 确认 `thread.started` 事件是否带 timestamp（决定 `created_at` 是否需 `time.Now()` fallback）。
- 确认 driver/slave prod_test config `ShortID` 非空、重启持久；一机多 agent 各自 `ShortID` 不同。
- 确认 codex backend executor 是 driver/slave 上创建 `codex_exec` rollout 的唯一路径。
- 确认 driver 在 MCP tool-call 时能拿到当前 codex thread id。
- 确认 `EnsureRegistered` 后 `cfg.Credentials.ShortID` 已填、可据其解析 `codex_home`（P2 重排 `agentbackend.New` 顺序的可行性）。
