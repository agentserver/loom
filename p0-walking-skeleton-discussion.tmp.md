# Loom 核心重写 P0 讨论快照

> **状态：临时讨论记录，不是最终设计规范或实施计划。**
>
> 本文件只冻结截至 2026-08-14 已由用户逐项确认的 P0 决策。错误处理、验证矩阵、eval harness 细节、精确 API/schema 和实施任务仍需继续评审。评审完成后，正式设计写入 `docs/superpowers/specs/`，活的分层设计写入 `docs/design/`，实施计划写入 `docs/superpowers/plans/`。

## 1. 权威输入与总体目标

P0 以以下文件为权威输入：

1. `docs/superpowers/specs/2026-08-13-loom-core-rewrite-roadmap-design.md`
2. `refactored-driver-lifecycle-spec.html`
3. `platform-release-governance-spec.html`

若旧的 Markdown lifecycle design 与 HTML 冲突，以 HTML 为准。当前 HTML 已冻结为 v1 单成员 Workspace、creator-only Conversation 的口径，并包含 `deactivate_conversation` 等较新的生命周期决定。

Roadmap 的总目标是按 lifecycle spec **近乎重写 Loom 的核心**：

- 重写 `TaskContract -> child nodes -> runs -> RunEvent/RunProjection` 执行链；
- 重写 Driver daemon 与隔离 coding agent 的边界；
- 令 Observer 成为唯一 control plane 和 Task Bus；
- 旧 `multi-agent/internal/` 只作参考、对照 oracle 和 baseline，不要求继续可部署。

P0 是极薄 walking skeleton，不是 P1 DAG 核心的提前实现。

## 2. 已确认的总体落地策略

### 2.1 新旧系统物理隔离

新核心建立在仓库根级独立 Go module `loom/` 中：

```text
loom/
├── go.mod
├── cmd/
│   ├── loom-observer/
│   ├── loom-driver/
│   ├── loom-slave/
│   └── loom-fake-coding-agent/
├── internal/
│   ├── conversation/
│   ├── driverruntime/
│   ├── release/
│   ├── runstore/
│   ├── taskbus/
│   └── slaveruntime/
├── migrations/
│   ├── observer/
│   └── driver/
└── tests/
    └── p0/
```

约束：

- 新 `loom/` module 不得 import `multi-agent/internal/*`；
- 旧 `multi-agent/` 保持为 baseline/oracle；
- 同一 eval harness 后续能够分别启动旧系统和新系统并评分；
- 旧系统既有存储不因 P0 被迁移。

### 2.2 P0 使用真实多进程边界

P0 运行以下独立进程：

- `loom-observer`
- `loom-driver`
- `loom-fake-coding-agent`
- `loom-slave`

P0 使用真实 HTTP/worker-session/本机 IPC 边界，但用确定性替身控制范围：

- fake coding agent 固定形成单节点 echo contract，不调用真实 LLM；
- Slave 只执行 echo；
- 不把 coding agent 写成 Driver 内的普通函数；
- P0 验证真实控制链、进程隔离和持久化边界，而非 LLM 语义质量。

## 3. P0 范围

### 3.1 唯一端到端 happy path

```text
prompt
  -> Observer session
  -> Driver
  -> isolated fake coding agent
  -> one-node trivial TaskContract
  -> Observer admission
  -> PostgreSQL transactional outbox Task Bus
  -> Slave echo execution
  -> structured Result
  -> ordered RunEvent + RunProjection
  -> terminal Run
  -> explicit close_run
  -> Driver assistant Message
  -> Observer SSE relay
  -> test client
```

### 3.2 P0 明确不实现

- 多节点或条件 DAG；
- plan revision、lineage、supersede、reducer；
- retry、崩溃恢复、reconciliation、租约；
- approval、撤销和复杂授权；
- capability snapshot、按需能力查询、动态 MCP；
- object store 大结果；
- OIDC、device-code、用户管理和多成员生命周期；
- Chat Web UI；
- release promotion、rollback、operator、签名/KMS 和完整 genesis 治理；
- 真实 LLM/coding-agent backend。

## 4. 会话入口与回显协议

### 4.1 P0 身份 fixture

P0 预置唯一 user、workspace、driver 和 slave 身份，不实现外围身份流程。即使是 fixture，Observer 仍在每个入口校验 creator、workspace、agent token 和 worker session 的绑定关系。

### 4.2 Conversation 是两个状态转换

P0 真实实现：

```text
create_conversation -> activate_conversation
```

- `create_conversation` 只创建 inactive Conversation；
- `activate_conversation` 才占用唯一 workspace slot 并启动 runtime；
- 只有 active Conversation 才能接收 prompt、启动隔离 coding agent 和 `open_run`；
- P0 由 API/测试客户端驱动，不实现 Chat Web。

### 4.3 异步 prompt 与 SSE 回显

- 测试客户端提交 prompt 后，Observer 只有在 Driver durable 接纳 Message 后才返回 `202 Accepted + message_id`；
- Driver 的 fake coding agent 运行任务、等待 terminal、显式 `close_run`，再生成确定性 assistant Message；
- Conversation/Message 正文只存在于 Driver PostgreSQL；
- Observer 只中继正文，不保存正文；
- 回复通过 SSE 到达测试客户端；
- Run 状态由 Observer 的 RunProjection API 独立查询，它不是会话回复；
- SSE 断开不影响 Run 执行，P0 不实现断线续传。

## 5. 总体架构与权威边界

```text
test client
   | HTTP + SSE
   v
loom-observer ---------------- Observer PostgreSQL
   ^    |                       control plane, Run, events,
   |    |                       projections, release registry, outbox
   |    |
   |    +-- Driver worker session --> loom-driver
   |    |                                  |
   |    |                                  +-- Driver PostgreSQL
   |    |                                  |   Conversation, Message,
   |    |                                  |   RunScope, runtime metadata
   |    |                                  |
   |    |                                  +-- isolated local process
   |    |                                      loom-fake-coding-agent
   |    |
   |    +-- Slave worker session ----> loom-slave
   |                                       +-- echo executor
   |
   +-- structured result and conversation relay
```

### 5.1 `loom-observer`

- 唯一 control plane 和 Task Bus；
- 保存预置单成员 Workspace、Conversation binding/slot、worker session、release registry、Run、单节点、RunEvent、RunProjection 和 transactional outbox；
- 认证测试用户、Driver 和 Slave；
- 只中继 Message/回复正文，不保存正文；
- 不替 Driver/coding agent 做语义规划。

### 5.2 `loom-driver`

- 保存 Conversation、Message、RunScope 和 coding-agent runtime 的本地权威数据；
- 每个 active Conversation 启动一个独立 fake coding-agent 子进程；
- 暴露 P0 版受限 Driver tool/JSON-RPC：`open_run`、`submit_trivial_plan`、`get_run_status`、`close_run`；
- Runtime 不调用 LLM，也不替 coding agent 生成 contract。

### 5.3 `loom-fake-coding-agent`

- 是确定性隔离进程；
- 收到 prompt 后生成固定形态的单节点 echo contract；
- 调用 Driver 工具完成 open、提交、等待、close；
- terminal 后生成确定性回复；
- 后续可被真实 Codex/app-server 替换，而不改变 Observer 协议。

### 5.4 `loom-slave`

- 通过 agent-token worker session 从 Observer 获取 dispatch；
- P0 只支持 `kind=echo`；
- 返回带 `dispatch_id`、`assignment_id`、`attempt_id`、输出和成功状态的结构化结果。

### 5.5 通信边界

- test client -> Observer：HTTP + SSE；
- Observer <-> Driver/Slave：agent-token 鉴权的 HTTP long-poll worker session；
- Driver <-> fake coding agent：本机 Unix socket 上的 JSON-RPC，不暴露公网 HTTP；
- Observer 与 Driver PostgreSQL 使用不同 database/schema 和凭据。

## 6. PostgreSQL 决策

新核心从 P0 起所有关系型持久化统一使用 **PostgreSQL 16**：

- 不提供 SQLite 实现；
- 不用内存 store 替代需要验证的 PostgreSQL 事务语义；
- 测试使用 `postgres:16-alpine`，与仓库现有 CI 一致；
- schema 通过版本化前向 migration 管理；
- Observer 和 Driver 使用两个隔离的持久化域；
- Slave P0 不设数据库；以后若需要 durable local state，也使用独立 PostgreSQL。

Observer PostgreSQL 持有控制面和永久 Run 事实。Driver PostgreSQL 持有私有 Conversation/runtime 事实。Driver 私有正文不得集中写入 Observer。

## 7. Release registry 的 P0 边界

P0 采用“真实 registry 读取、静态 genesis fixture”：

- 启动时通过受控 bootstrap fixture 写入唯一 `RunControlSchemaRelease` 并设为 `active-default`；
- `open_run` 必须从 Observer release registry 实时读取并固定 `{version, digest}`；
- registry 为空、存在多个 active-default 或 release 无效时，`open_run` 失败且不得写入任何部分 Run 事实；
- P0 不实现 promotion/rollback 的状态机；
- release pointer 变化不写入用户 RunEvent；
- P0 的 fixture 不被描述为完整生产 genesis 治理。

## 8. 跨数据库事务原则

Observer PostgreSQL 与 Driver PostgreSQL 之间不使用或模拟两阶段提交。跨域操作统一采用：

```text
local transaction -> durable receipt -> remote transaction
```

### 8.1 `create_conversation`

1. Observer 验证预置 creator 和 Driver worker session。
2. Driver 事务创建本地 Conversation placeholder，生成 `ConversationRef`。
3. Driver 返回 durable receipt。
4. Observer 事务创建 `ConversationBinding(state=inactive)`。
5. Driver 未 durable 成功时，Observer 不发布 binding。

### 8.2 `activate_conversation`

1. Observer 事务将唯一 workspace slot 从 `empty` 改为 `activating`。
2. Driver 启动 Conversation 独占的 fake coding-agent 子进程，并持久化 runtime identity。
3. Driver 返回 ready receipt。
4. Observer 事务将 binding 和 slot 改为 `active`。
5. 启动失败则释放 slot，Conversation 保持 `inactive`。

### 8.3 `submit_prompt`

1. Observer 验证 creator、active binding 和当前 Driver session。
2. Driver 事务写入不可变 Message 与 append-only delivery attempt。
3. Driver durable 接纳后，Observer 才返回 `202 + message_id`。
4. Prompt 正文不进入 Observer PostgreSQL、日志或 RunEvent。

## 9. P0 Run 执行事务

1. fake coding agent 从 prompt 构造单节点 echo ParentContract，调用 Driver `open_run`。
2. Driver 在本地 PostgreSQL 写入 `opening` metadata。
3. Observer 在一个事务中：
   - 验证 Conversation active；
   - 验证 creator、ParentContract owner 和 agent token user 相等；
   - 读取唯一 active-default release；
   - 写入 ContractSubmission、ParentContract 和 Run；
   - 固定 release `{version,digest}`；
   - 追加 `run_opened`；
   - 创建初始 RunProjection。
4. Driver 收到成功响应后，持久化 pin 和 active RunScope。
5. fake coding agent 调用 `submit_trivial_plan`，明确指定预置 Slave。
6. Observer 在一个事务中：
   - 验证恰好一个 `kind=echo` node；
   - 保存 committed plan snapshot 和 digest；
   - 创建 Node、Dispatch、Assignment 和 Attempt；
   - 追加对应 RunEvent；
   - 推进 RunProjection；
   - 写入 Task Bus outbox。
7. Task Bus worker 使用 PostgreSQL transactional outbox 和 `FOR UPDATE SKIP LOCKED` 领取工作。
8. Slave 通过 worker session 获取 dispatch，执行 `output=input`。
9. Slave 提交 `{dispatch_id, assignment_id, attempt_id, output}`。
10. Observer 在一个事务中：
    - 校验 ID 链和 attempt 状态；
    - 写入结构化 Result；
    - 将 attempt、node 和 run 推进到 `completed`；
    - 追加有序 RunEvent；
    - 更新 terminal RunProjection。
11. Driver 观察 terminal projection，fake coding agent 调用 `close_run`。
12. Observer 追加 `run_closed` 并设置 `closed_at`。
13. Driver 保存确定性 assistant Message，Observer 通过 SSE 中继正文。

P0 不实现 outbox 重试、租约或崩溃恢复，但保留 `dispatch_id`、`assignment_id`、`attempt_id`、幂等键和 outbox 边界，供 P1 精化。

## 10. 最小数据模型

### 10.1 Observer 表组

```text
users
workspaces
workspace_memberships
agents
worker_sessions

conversation_bindings
workspace_conversation_slots
conversation_operations

run_control_schema_releases
contract_submissions
parent_contracts
runs
committed_plan_revisions
run_nodes

dispatches
assignments
attempts
results
task_bus_outbox

run_events
run_projections
```

### 10.2 Driver 表组

```text
conversations
messages
message_delivery_attempts
coding_agent_runtimes
driver_run_metadata
run_scopes
operation_receipts
```

### 10.3 已确认的数据库不变量

- 每个 workspace 最多一个 active/activating Conversation slot；
- 每个平台恰好一个 `active-default` release；
- `(driver_agent_id, run_id)` 唯一；
- `(run_id, sequence)` 唯一；
- `(run_id, event_id)` 唯一；
- P0 每个 Run 恰好一个 committed node；
- `dispatch_id`、`assignment_id`、`attempt_id` 全局唯一；
- 一个 Attempt 最多接受一个 terminal Result；
- RunProjection 的 `projection_version` 等于最后应用的 event sequence；
- `run_closed` 只能追加到 terminal Run。

RunProjection 不是独立真相源。P0 提供 rebuild 工具，在空 projection 表中按 RunEvent 顺序重放，并验证重建结果与在线 projection 完全一致。

## 11. PlusCal/TLA+ 与分层文档

### 11.1 P0 形式化方法

P0 使用一个多进程 PlusCal L0 模型：

- User、Observer、Driver、TaskBus、Slave 作为独立 process；
- 所有状态转换、角色行为和并发流程用 PlusCal 编写；
- PlusCal 嵌入 `.tla` 文件，并提交翻译生成的 TLA+ 区段；
- 不变量与 temporal properties 用 TLA+ 公式声明；
- TLC 参数放在 `.cfg`；
- 验证先检查 PlusCal translation 无漂移，再运行 TLC。

P0 不为每个组件建立独立 PlusCal/L1 模型。P1 才在核心组件目录加入 `run-store.tla`、`task-bus.tla` 等 PlusCal 模型，并证明它们精化 P0 L0。

### 11.2 P0 L0 路径

```text
Conversation inactive
  -> active
  -> prompt accepted
  -> trivial contract formed
  -> run opened with active-default pin
  -> one node admitted
  -> dispatch created
  -> echo executed
  -> structured result accepted
  -> RunEvent + RunProjection atomically advanced
  -> terminal observed
  -> run explicitly closed
  -> response emitted
```

### 11.3 P0 L0 属性

- inactive Conversation 不能接收 prompt 或 `open_run`；
- 每个新 Run 必须绑定唯一 active-default release 的 version 和 digest；
- P0 Run 恰好包含一个 child node；
- 未经 Observer 准入不能派发；
- 每次已提交状态变化同时追加有序 RunEvent 并更新 RunProjection；
- projection 可以从事件序列重建；
- Run 不会同时处于两个终态；
- 只有 terminal Run 才能回显并执行 `close_run`；
- 在公平性假设下，已准入节点最终被派发，已派发 echo 最终收敛到 terminal。

### 11.4 P0 不建模

- LLM 语义决策；
- 条件 DAG、revision 和 lineage；
- retry、崩溃恢复和 reconciliation；
- approval、撤销和复杂授权；
- capability snapshot 和动态 MCP；
- 多成员生命周期；
- release promotion/rollback 全状态机。

### 11.5 文档树

讨论完成后应形成：

```text
docs/
├── superpowers/specs/
│   └── 2026-08-14-loom-core-p0-walking-skeleton-design.md
│
└── design/
    ├── flows/
    │   ├── index.md
    │   ├── submit-run.md
    │   ├── system.tla
    │   └── system.cfg
    │
    └── components/
        ├── conversation/
        ├── driver-runtime/
        ├── run-store/
        ├── task-bus/
        ├── slave-runtime/
        └── release-registry/
```

每个 P0 实际涉及的组件目录先包含：

```text
contract.md
internal/index.md
```

规则：

- flow 只引用组件 `contract.md`；
- `contract.md` 是稳定对外契约；
- `internal/index.md` 是 P0 内部状态和事务设计；
- 实现开始时才按需增加 `detail/index.md`、schema 或 error-paths 子文档；
- 不提前创建 P1/P2 的组件目录；
- 最终 design spec 是日期化快照，`docs/design/` 是随代码演进的权威活文档。

## 12. 尚未完成的设计评审

以下内容尚未逐项提交用户确认，因此本快照不对它们作最终决定：

- API、JSON-RPC、SSE envelope 和 DomainErrorEnvelope 的精确 schema；
- worker-session handshake、long-poll 和 ACK 的精确协议；
- P0 错误分类、补偿动作和超时；
- PostgreSQL migration 工具、事务 isolation level 和 lock 顺序；
- PlusCal 变量、process、fairness 和 TLC state bounds 的精确定义；
- PlusCal/TLA+ 工具版本和可复现安装方式；
- 单元、集成、contract、conformance 和端到端测试矩阵；
- eval harness 的 1-2 个 P0 case、旧系统 baseline adapter 和评分结果格式；
- CI job、compose 拓扑、配置和开发命令；
- P0 明确的完成门槛；
- 最终文件清单与 task-by-task 实施计划。

这些内容在后续 brainstorming 评审完成后，才写入正式 P0 design spec，并转入 `writing-plans`。
