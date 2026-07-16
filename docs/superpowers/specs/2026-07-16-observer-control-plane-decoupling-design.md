# Observer 作为 Workspace Control Plane，解除 AgentServer 依赖

**日期：** 2026-07-16
**状态：** 设计已确认，尚未实施
**相关：** Driver / Planner Runtime 重构；能力空间与节点级 provisioning 设计

## 1. 结论

Observer 成为本项目唯一的 workspace control plane：它负责用户认证后的授权、workspace 与成员管理、agent 注册与可见性、面向用户的 Driver 聊天网页 / 网关，以及可靠的任务派发。

聊天网页不使 Observer 变成对话引擎。真实的 `Conversation`、`Message`、LLM backend session 与会话管理由被选中的 Driver 本地持久化并提供；Observer 只保存可授权、可路由的会话绑定与审计元数据，通过 Driver 已认证的 worker session 转发请求和流式响应。

AgentServer 不再是身份、workspace、agent 或 task 的权威。迁移期间它可以作为一个可替换的 transport adapter，为旧 agent 提供兼容；完成迁移后项目不依赖它。

这会显著简化认证拓扑，但不是简单删除认证功能：当前分散在 AgentServer 和 Observer 的身份、令牌与 workspace 映射，改为由 Observer 以一个明确的模型集中管理。

## 2. 目标与非目标

### 2.1 目标

- 一个用户可在 Observer 中直接创建、查看和管理其全部 workspace 及其中的 agent。
- Observer 是 application authorization 的唯一来源：workspace ownership、成员关系、角色、agent 归属和任务访问均以它保存的状态为准。
- 用户浏览器、Driver 和 Slave 均不需要向 AgentServer 请求身份或 workspace 权限。
- 已就绪的 DAG 节点由 Observer 持久化派发，并具备 assignment、attempt、ack、heartbeat、取消、重试和失联回收语义。
- agent 的在线状态、能力快照和 workspace 可见性由 Observer 的 agent registry 权威维护。
- 用户可在 Observer 网页中选择可见的 Driver，新建、列出、重命名、归档、删除和继续与该 Driver 的对话；这些动作由 Observer 鉴权 / 路由、由 Driver 实际执行和保存。
- 个人能力空间、provisioning 与节点执行使用同一 workspace / user 授权模型；权限由已验证 token 身份与 Observer ACL / policy 判定，不另发能力 bearer grant。

### 2.2 非目标

- 不把自然语言规划或 DAG 语义交给 Observer 的任务总线。
- 不把 Observer 设计成第二个 Conversation / Message / LLM context store；除明确的绑定、标题 / 状态索引和审计元数据外，它不默认持久化完整聊天正文，也不在 Driver 离线时伪造回复。
- 不在第一阶段实现自研密码、社交登录或完整 OIDC provider；Observer 可以消费外部 OIDC 身份断言，但 application user、workspace 和授权仍由 Observer 管理。
- 不要求旧 AgentServer agent 一次性下线；兼容 adapter 可以暂时存在。
- 不复用当前 observer 的 event 聚合表作为队列实现。

## 3. 权责边界

```text
用户 / 浏览器
   │  登录、workspace 管理、RBAC 管理
   ▼
Observer Control Plane
   ├─ Identity & Workspace: user、membership、user token、device-code、agent token
   ├─ Chat Web & Gateway: Driver 选择、会话绑定、授权、路由与流式中继
   ├─ Agent Registry: 可见性、心跳、能力快照、draining / revoke
   ├─ Task Bus: assignment、attempt、结果、取消、回收
   ├─ Event / Artifact / Capability Registry
   └─ 管理 API 与审计日志
          ▲                         │ authenticated worker session / receive dispatch
          │ ready-node dispatch      ▼
Driver Runtime ────────────────── Slave / Worker
   ├─ LLM-led DAG patch
   ├─ policy、依赖、并发预算
   └─ 判定节点 READY

AgentServer（迁移期）：仅实现 ControlPlane / WorkerGateway adapter；不保存权威业务状态。
```

浏览器只向 Observer 发送 user token；它绝不接触 Driver 的 agent token。Observer 先以
`(user_id, workspace_id, driver_agent_id)` 校验 membership、agent role、可见性和在线状态，
随后复用该 Driver 已建立的、以 agent token 认证的 worker session 转发会话 RPC / 流。这里没有
第三类浏览器 token，也没有把 user token 交换成 agent token。

### 3.1 Driver Conversation Service

Driver 是实际对话的权威：它在本地 durable store 保存 `Conversation(conversation_id)`、
`Message(message_id, sequence)` 和可选 `LLMBackendSession(backend_session_id)`，并把相应内容送入
自己的 LLM backend。一个会话可以创建多个 run，但聊天记录本身不是 DAG 或 Task Bus 状态机。

Driver 必须在其已认证的 worker session 上提供内部会话协议：
`CreateConversation`、`ListConversations`、`GetConversation`、`RenameConversation`、
`ArchiveConversation`、`DeleteConversation`、`SendMessage` 与 `StreamConversation`。浏览器不能直接调用
这些 API；Observer 只在完成用户 / workspace / Driver 可见性检查后中继它们。Driver 返回的稳定
`conversation_id` 同时用作 Observer 会话绑定的键。

### 3.2 Driver Runtime

Driver Runtime 仍是 DAG 的确定性执行面：验证 plan patch、维护依赖关系、检查 policy / concurrency budget、把 node 从 `PENDING` 变为 `READY`，并请求派发。

它**不**再直接调用 AgentServer SDK。它只依赖项目内定义的窄接口，例如 `ControlPlane` 和 `WorkerGateway`。

### 3.3 Observer Chat Web 与 Gateway

Observer 的 Chat Web 是用户的唯一浏览器入口。它提供 workspace / Driver 选择、会话列表入口、
新建和管理动作、消息输入及流式显示；这些都是受 user token 与 membership 保护的管理 / 协作界面。

Observer 只保存 `ConversationBinding` / index：`conversation_id`、`user_id`、`workspace_id`、
`driver_agent_id`、显示标题、状态、时间戳、关联的 `ContractSubmission` / audit reference。它不保存
Driver 的完整 `Message` 正文、LLM private context 或 backend session。聊天字节在已鉴权的请求中暂态
中继；若 Driver 不在 `ready` 状态或 session 中断，网页显示不可用 / 可重连状态，不能改由 Observer
产生 LLM 回答。

### 3.4 Observer Task Bus

Observer 接收一个已经 `READY` 的 node 后，负责：

1. 按 workspace、target 约束、能力、在线状态与配额选择可执行 agent；
2. 创建不可重复的 dispatch 和 attempt；
3. 向已认证 worker 的 session 投递工作，并记录内部 execution epoch；
4. 接收 ack、heartbeat、progress、artifact reference、结果与错误；
5. 在 worker 失联或内部 execution claim 失效时以幂等规则 reclaim / retry / fail；
6. 将终态和事件回馈 Driver Runtime 与用户界面。

Task Bus 不重新推断 DAG 依赖，也不能擅自扩大 parent contract 的授权、并发或 retry budget。

## 4. 统一身份与授权模型

### 4.1 仅有两类长期身份 token

- **user token**：网页账号密码登录后由 Observer 签发，带 `type=user` 与 `user_id`；仅用于浏览器管理 API，绝不下发给机器。
- **agent token**：由 Observer 签发，带 `type=agent`、`user_id`、`workspace_id` 与 `agent_id`；Driver / Slave 只用它建立 worker session、派发 / 接收同 workspace 工作、传输 artifact 和回报结果。
- 两种 token 都必须带 issuer、audience、签名、到期和可轮换 token id / version 等验证字段；这些字段不引入新的授权主体。

Observer 保存 `users`、`workspaces`、`workspace_memberships`、agent registry 与角色 / ACL。token 是请求方提供的唯一身份来源；服务端授权只使用 token 的已验证身份字段和 Observer 自己的权威状态，绝不信任请求 body、URL 或 LLM 声称的 user / workspace / agent。

例如 user token 只含 `user_id`：用户访问 workspace `W` 时，Observer 以 `(user_id, W)` 查询 membership。只靠 URL 中的 `W` 不能获得任何权限。

### 4.2 保留 device-code，但 issuer 改为 Observer

新 agent 沿用现有的 device-code 交互，而不是保存 user token 或使用 API-key bootstrap：

```text
machine → POST /api/oauth2/device/auth → {device_code, user_code, verification_uri}
user browser（user token）→ verification_uri → 选择 workspace、确认 agent 信息
machine → POST /api/oauth2/token（轮询 device_code）→ agent token
```

- device code / user code 是短时、一次性注册事务，不是第三类长期身份 token。
- 浏览器中的 user token 决定可选择哪些 workspace；machine 提交的 agent name / role / workspace 仅是建议，Observer 必须以用户身份和自身 policy 最终裁决。
- machine 将 agent token 安全持久化；以后可用尚有效的 agent token 轮换自身 token。已撤销或过期的 agent 重新走 device-code。
- AgentServer 的 device-code、Register、proxy token 和 `whoami` 不再参与新路径。

### 4.3 常规任务不签发 node token

- Driver 用自己的 agent token 提交 child contract；Observer 验证 Driver、目标 Slave 与任务都在同一 workspace，并检查 registry 状态和 contract policy。
- Slave 用自己的 agent token 接收、执行和回报该 dispatch；没有 node-scoped bearer grant，也不需要把短时 lease 当作权限凭证。
- `dispatch_id`、`attempt_no` 与可选的内部 `execution_epoch` 只是幂等 / stale-result 防护字段，不是 token 或新授权。
- personal capability、secret、artifact 等资源仍由 Observer 的 ACL / policy 根据 user_id、workspace_id、agent_id 决定；若权限不足，发出 `authorization_required` 事件并由用户在网页管理面调整 policy，而不是另发一种 bearer grant。

这一模型替代当前“AgentServer device-code / Register / proxy token → Observer whoami 反查 / 缓存”的双重权威链。

## 5. Agent Registry 与可见性

Observer 的 agent registry 是 live worker registry，而不是仅由 telemetry 反推的展示列表。每个 agent 至少包含：

- agent installation identity、workspace binding、display name、owner / device-authorization provenance；
- 状态：`offline`、`connecting`、`ready`、`busy`、`draining`、`revoked`；
- 心跳、session generation、最后活动时间、健康原因；
- capability snapshot（技能、工具、版本、平台、资源、签名 / digest）及其生成时间；
- 可执行约束和当前 reserved capacity。

用户只可查看其有 membership 的 workspace 中的 agent。跨 workspace 共享一台机器时，默认使用独立 device authorization / agent token；以后若支持共享 installation，也必须通过显式 binding 和逐 workspace 授权实现，不能复用一个无限作用域 token。

## 6. 持久任务派发协议

当前 Observer 的 `events`、`tasks` 和 `subtasks` 是观测投影。新的任务总线应使用独立的持久模型，至少包含：

- `dispatches`：一个 ready node 的派发意图、target constraints、优先级和 idempotency key；
- `assignments`：某次选择的目标 agent；
- `attempts`：执行开始、终止、失败原因、结果和 artifact references；
- `worker_sessions` / `heartbeats`：连接代次与活跃度；
- `cancellations`：用户或 Runtime 请求的取消意图与确认状态。

若采用 worker-pull 或自动 reclaim，可额外保存短时 `execution_claims` / `execution_epoch`；它们是 Observer 内部的调度记录，不是 bearer token、权限 grant 或新的对外认证机制。

推荐 worker 以已认证的出站 long-poll 或 WebSocket session 接收工作，避免 Observer 主动连入位于 NAT、边缘网络或用户内网中的 Slave。

典型状态流：

```text
Driver: node READY
  → Observer: dispatch pending
  → assignment selected
  → worker ACK / running
  → completed | failed | cancelled

worker disconnected / stale execution epoch
  → Observer marks attempt stale
  → retry（仅在 parent contract retry budget 内）| failed
```

重复提交、重复 ack、重复完成和连接重放必须以 `dispatch_id + attempt_no + execution_epoch` 幂等处理。能力 provisioning 失败独立记录为 `ProvisionAttempt`，不消耗原业务节点的 retry 预算。

## 7. API 与代码边界

Driver、orchestrator、slave 等包应依赖本项目定义的类型，而不是泄漏 `agentsdk.AgentCard`、`agentsdk.DelegateTaskRequest` 或 AgentServer task id。

初始接口应覆盖以下语义，而非复制旧 SDK 的 HTTP 形状：

- **Observer Chat Web / Gateway：**`ListVisibleDrivers`、`BindConversation`、`RouteConversationCommand` 与 `RelayConversationStream`。它们只接受 user token，检查 membership / Driver visibility，并将请求转交给已经认证的 Driver worker session；不把 user token 下发给 Driver。
- **Driver Conversation Service（仅经已认证 worker session）：**`CreateConversation`、`ListConversations`、`GetConversation`、`RenameConversation`、`ArchiveConversation`、`DeleteConversation`、`SendMessage`、`StreamConversation`。Driver 对会话与消息的持久化和 LLM backend session 负责，Observer 对绑定、授权、路由和审计负责。
- `QueryAgents` / `QueryCapabilities`；
- `SubmitReadyNode`、`CancelDispatch`、`GetDispatch`；
- `OpenWorkerSession`、`ReceiveDispatch`、`AckDispatch`、`Heartbeat`；
- `CompleteAttempt`、`FailAttempt`、`EmitProgress`；
- `StartDeviceAuthorization`、`ApproveDeviceAuthorization`、`PollDeviceToken`、`RotateAgentToken`、`RevokeAgentToken`。

迁移期的 `AgentServerAdapter` 可以实现这些接口，把旧的 `DiscoverAgents` / `DelegateTask` / `GetTask` / peer transport 转换为兼容调用。它不可写入或成为 Observer 状态的第二权威来源。

## 8. 迁移顺序

1. **抽象边界。** 在项目内引入 `ControlPlane` / `WorkerGateway` 类型和 fake，实现替换所有直接 `agentsdk` 依赖。
2. **建立 Observer authority 与 Chat Web。** 增加账号密码网页登录、user token、workspace membership、Observer device-code、agent token rotation、registry、Driver 可见性和聊天网页 / gateway；保持旧 telemetry 读取兼容。
3. **接入 Driver Conversation Service。** 在 Driver local durable store 实现会话、消息和 LLM backend session；经既有 authenticated worker session 暴露会话管理 / 流式协议。Observer 只保存 ConversationBinding / index，浏览器不接触 agent token。
4. **建立 Task Bus。** 实现 dispatch、assignment、attempt、worker session 和故障回收；以 fake worker 覆盖协议。内部 execution claim 不成为对外 token。
5. **接入新 Driver / Slave。** 将现有 device-code 客户端改指向 Observer；Driver 用 agent token 提交 ready node；Slave 用 agent token 直接连接 Observer 并提交结果。
6. **兼容旧节点。** 仅通过 AgentServerAdapter 承载无法立即升级的 worker，明确标记 legacy transport。
7. **切换并删除依赖。** 所有 workspace、agent 和 task 的权威读写均来自 Observer 后，删除 AgentServer identity resolver、external identity 映射和直接 SDK 依赖。

每个阶段都必须避免“双写、双授权、双调度”。若 adapter 和 Observer 同时可派发，Observer 的 dispatch id / execution epoch 仍必须是唯一权威；adapter 只能运输命令。

## 9. 现有 AgentServer Stub 的定位

现有 `tests/k8s_commander/mock-agentserver` 实现 device OAuth、token 轮询、`GET /api/agent/whoami` 和 health check。其 device OAuth 交互是迁往 Observer 时可复用的行为契约；`whoami` 只保留为旧路径 fixture。

它没有 agent registry、agent discovery、durable queue、assignment / attempt / execution-epoch、worker polling、task cancel 或 task result 查询，因此不是新控制面的实现起点。新 Task Bus 应由 Observer 自己实现并以独立 fake worker 测试。

## 10. 验收条件

- 用户可在 Observer 中管理多个 workspace、成员和 agent，而无需 AgentServer 账户或 workspace API。
- 用户只在网页端以账号密码取得 user token；机器通过 Observer device-code 取得 agent token，机器上不保存 user token。
- 用户能在 Observer 网页选择其可见且在线的 Driver，新建、列出、重命名、归档、删除与继续对话；真实 Conversation / Message / LLM session 只由该 Driver 保存，Observer 只保存 binding / index / audit metadata。
- 浏览器不会持有 agent token；聊天请求只能经 Observer 的 user-token 鉴权和已认证的 Driver worker session 到达 Driver。Driver 离线时网页报告不可用，不由 Observer 伪造对话结果。
- 一个 agent 只凭 Observer agent token 即可宣告能力、接收同 workspace 任务和提交结果；普通任务不产生 node token。
- Driver 不再导入 AgentServer SDK 类型；切换 adapter 不改变 DAG / contract 行为。
- agent 失联、重复消息、stale execution epoch、取消和 retry 不产生重复业务执行或越过 parent contract 预算。
- workspace 隔离覆盖管理 API、worker API、artifact、event、capability access policy 和任务结果。
- 关闭 AgentServer 后，新 Driver / Slave 路径仍可完成 run；仅明确标记的 legacy worker 受影响。
