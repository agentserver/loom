# Observer 作为 Workspace Control Plane，解除 AgentServer 依赖

**日期：** 2026-07-16
**状态：** 设计已确认，尚未实施
**相关：** Driver / Planner Runtime 重构；能力空间与节点级 provisioning 设计

## 1. 结论

Observer 成为本项目唯一的 workspace control plane：它负责用户认证后的授权、workspace 与成员管理、agent 注册与可见性、以及可靠的任务派发。

AgentServer 不再是身份、workspace、agent 或 task 的权威。迁移期间它可以作为一个可替换的 transport adapter，为旧 agent 提供兼容；完成迁移后项目不依赖它。

这会显著简化认证拓扑，但不是简单删除认证功能：当前分散在 AgentServer 和 Observer 的身份、令牌与 workspace 映射，改为由 Observer 以一个明确的模型集中管理。

## 2. 目标与非目标

### 2.1 目标

- 一个用户可在 Observer 中直接创建、查看和管理其全部 workspace 及其中的 agent。
- Observer 是 application authorization 的唯一来源：workspace ownership、成员关系、角色、agent 归属和任务访问均以它保存的状态为准。
- 用户浏览器、Driver 和 Slave 均不需要向 AgentServer 请求身份或 workspace 权限。
- 已就绪的 DAG 节点由 Observer 持久化派发，并具备 assignment、lease、ack、heartbeat、取消、重试和失联回收语义。
- agent 的在线状态、能力快照和 workspace 可见性由 Observer 的 agent registry 权威维护。
- 个人能力空间、一次性 capability grant、provisioning 与节点执行使用同一 workspace / user 授权模型。

### 2.2 非目标

- 不把自然语言规划或 DAG 语义交给 Observer 的任务总线。
- 不在第一阶段实现自研密码、社交登录或完整 OIDC provider；Observer 可以消费外部 OIDC 身份断言，但 application user、workspace 和授权仍由 Observer 管理。
- 不要求旧 AgentServer agent 一次性下线；兼容 adapter 可以暂时存在。
- 不复用当前 observer 的 event 聚合表作为队列实现。

## 3. 权责边界

```text
用户 / 浏览器
   │  登录、workspace 管理、RBAC 管理
   ▼
Observer Control Plane
   ├─ Identity & Workspace: user、membership、session、agent enrollment
   ├─ Agent Registry: 可见性、心跳、能力快照、draining / revoke
   ├─ Task Bus: assignment、lease、attempt、结果、取消、回收
   ├─ Event / Artifact / Capability Registry
   └─ 管理 API 与审计日志
          ▲                         │ worker session / pull lease
          │ ready-node dispatch      ▼
Driver Runtime ────────────────── Slave / Worker
   ├─ LLM-led DAG patch
   ├─ policy、依赖、并发预算
   └─ 判定节点 READY

AgentServer（迁移期）：仅实现 ControlPlane / WorkerGateway adapter；不保存权威业务状态。
```

### 3.1 Driver Runtime

Driver Runtime 仍是 DAG 的确定性执行面：验证 plan patch、维护依赖关系、检查 policy / concurrency budget、把 node 从 `PENDING` 变为 `READY`，并请求派发。

它**不**再直接调用 AgentServer SDK。它只依赖项目内定义的窄接口，例如 `ControlPlane` 和 `WorkerGateway`。

### 3.2 Observer Task Bus

Observer 接收一个已经 `READY` 的 node 后，负责：

1. 按 workspace、target 约束、能力、在线状态与配额选择可执行 agent；
2. 创建不可重复的 dispatch 和 attempt；
3. 向 worker 的出站 session 提供工作，并发放短时 lease；
4. 接收 ack、heartbeat、progress、artifact reference、结果与错误；
5. 在 lease 过期或 worker 失联时以幂等规则 reclaim / retry / fail；
6. 将终态和事件回馈 Driver Runtime 与用户界面。

Task Bus 不重新推断 DAG 依赖，也不能擅自扩大 parent contract 的授权、并发或 retry budget。

## 4. 统一身份与授权模型

### 4.1 人类身份

- Observer 验证登录会话；外部 OIDC 若使用，只提供稳定 subject，不能决定 Observer 内的 workspace 权限。
- Observer 保存 `users`、`workspaces`、`workspace_memberships` 与角色 / ACL。
- 所有管理 API 均从该用户的 membership 计算访问权；不能用“知道 workspace_id”替代授权。

### 4.2 Agent 身份

- workspace owner 或有权限的成员生成一次性、短时、可撤销的 enrollment grant。
- agent 用 grant 注册，获得仅属于该 agent installation / workspace 的可轮换 credential。
- worker credential 只可打开 worker session、上报自身状态、领取已分配 work 和提交该 work 的结果；不得成为用户级或跨 workspace 的万能 token。
- revoke、rotation、draining 和删除 agent 立即由 Observer 生效，并使尚未开始的 lease 不可领取。

### 4.3 Node 授权

- Task Bus 仅为已派发 attempt 签发 node-scoped lease / grant。
- grant 绑定 `workspace_id`、`run_id`、`node_id`、`attempt_id`、目标 agent、权限范围、过期时间与 capability digest。
- personal capability space 的访问使用额外的一次性 grant，绑定 package digest、run、node、agent 和到期时间；它不能由 child contract 自行扩大。

这一模型替代当前“AgentServer proxy token → Observer whoami 反查 / 缓存 → Observer workspace 映射”以及 Observer 自有 agent token 并存的双重权威链。

## 5. Agent Registry 与可见性

Observer 的 agent registry 是 live worker registry，而不是仅由 telemetry 反推的展示列表。每个 agent 至少包含：

- agent installation identity、workspace binding、display name、owner / enrollment provenance；
- 状态：`offline`、`connecting`、`ready`、`busy`、`draining`、`revoked`；
- 心跳、session generation、最后活动时间、健康原因；
- capability snapshot（技能、工具、版本、平台、资源、签名 / digest）及其生成时间；
- 可执行约束和当前 reserved capacity。

用户只可查看其有 membership 的 workspace 中的 agent。跨 workspace 共享一台机器时，默认使用独立 enrollment / credential；以后若支持共享 installation，也必须通过显式 binding 和逐 workspace 授权实现，不能复用一个无限作用域 token。

## 6. 持久任务派发协议

当前 Observer 的 `events`、`tasks` 和 `subtasks` 是观测投影。新的任务总线应使用独立的持久模型，至少包含：

- `dispatches`：一个 ready node 的派发意图、target constraints、优先级和 idempotency key；
- `assignments`：某次选择的目标 agent；
- `leases`：短时、可续约的领取权；
- `attempts`：执行开始、终止、失败原因、结果和 artifact references；
- `worker_sessions` / `heartbeats`：连接代次与活跃度；
- `cancellations`：用户或 Runtime 请求的取消意图与确认状态。

推荐 worker 以出站 long-poll 或 WebSocket session 从 Observer 领取工作，避免 Observer 主动连入位于 NAT、边缘网络或用户内网中的 Slave。

典型状态流：

```text
Driver: node READY
  → Observer: dispatch pending
  → assignment selected
  → leased
  → worker ACK / running
  → completed | failed | cancelled

lease expired / worker disconnected
  → reclaimed
  → retry（仅在 parent policy 的 retry lease 内）| failed
```

重复提交、重复 ack、重复完成和连接重放必须以 `dispatch_id + attempt_id + lease_generation` 幂等处理。能力 provisioning 失败独立记录为 `ProvisionAttempt`，不消耗原业务节点的 retry 预算。

## 7. API 与代码边界

Driver、orchestrator、slave 等包应依赖本项目定义的类型，而不是泄漏 `agentsdk.AgentCard`、`agentsdk.DelegateTaskRequest` 或 AgentServer task id。

初始接口应覆盖以下语义，而非复制旧 SDK 的 HTTP 形状：

- `QueryAgents` / `QueryCapabilities`；
- `SubmitReadyNode`、`CancelDispatch`、`GetDispatch`；
- `OpenWorkerSession`、`ClaimLease`、`AckLease`、`HeartbeatLease`；
- `CompleteAttempt`、`FailAttempt`、`EmitProgress`；
- `IssueEnrollmentGrant`、`RegisterAgent`、`RevokeAgentCredential`。

迁移期的 `AgentServerAdapter` 可以实现这些接口，把旧的 `DiscoverAgents` / `DelegateTask` / `GetTask` / peer transport 转换为兼容调用。它不可写入或成为 Observer 状态的第二权威来源。

## 8. 迁移顺序

1. **抽象边界。** 在项目内引入 `ControlPlane` / `WorkerGateway` 类型和 fake，实现替换所有直接 `agentsdk` 依赖。
2. **建立 Observer authority。** 增加 user、workspace membership、agent enrollment、credential rotation、registry 和管理 API；保持旧 telemetry 读取兼容。
3. **建立 Task Bus。** 实现 dispatch、lease、attempt、worker session 和故障回收；以 fake worker 覆盖协议。
4. **接入新 Driver / Slave。** Driver 用 Observer 提交 ready node；Slave 直接连接 Observer 领取、续约并提交结果。
5. **兼容旧节点。** 仅通过 AgentServerAdapter 承载无法立即升级的 worker，明确标记 legacy transport。
6. **切换并删除依赖。** 所有 workspace、agent 和 task 的权威读写均来自 Observer 后，删除 AgentServer identity resolver、external identity 映射和直接 SDK 依赖。

每个阶段都必须避免“双写、双授权、双调度”。若 adapter 和 Observer 同时可派发，Observer 的 dispatch id 与 lease 仍必须是唯一权威；adapter 只能运输命令。

## 9. 现有 AgentServer Stub 的定位

现有 `tests/k8s_commander/mock-agentserver` 仅实现 device OAuth、token 轮询、`GET /api/agent/whoami` 和 health check。它可继续作为旧身份 adapter 的 fixture。

它没有 agent 注册、agent discovery、durable queue、assignment / lease、worker polling、task cancel 或 task result 查询，因此不是新控制面的实现起点。新 Task Bus 应由 Observer 自己实现并以独立 fake worker 测试。

## 10. 验收条件

- 用户可在 Observer 中管理多个 workspace、成员和 agent，而无需 AgentServer 账户或 workspace API。
- 一个 agent 只凭 Observer enrollment credential 即可注册、宣告能力、领取任务、续约和提交结果。
- Driver 不再导入 AgentServer SDK 类型；切换 adapter 不改变 DAG / contract 行为。
- agent 失联、重复消息、lease 过期、取消和 retry 不产生重复业务执行或越过 parent contract 预算。
- workspace 隔离覆盖管理 API、worker API、artifact、event、capability grant 和任务结果。
- 关闭 AgentServer 后，新 Driver / Slave 路径仍可完成 run；仅明确标记的 legacy worker 受影响。
