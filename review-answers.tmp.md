# Refactored Spec Review Decisions

本文件记录逐项评审问题、建议修正及用户确认结果。

## 1. Critical DeploymentSafetyAdvisory 的停止与唤醒

**用户决定：采用最小可用生产建议。**

- 新增 deployment control-plane 的 `SafetyStopOperation`，绑定 advisory、受影响 build / behavior bundle digest、停止状态和 termination / resolution evidence。
- `must_yield` 只表示结束当前受影响 coding-agent generation，不自动创建下一 turn。
- advisory 未 resolved 前，受影响 bundle 不得创建或恢复 turn，不得静默切换 profile / model / behavior bundle / Conversation。
- Driver 只保存无正文的 stop / wake receipt；UI 显示安全阻断状态。
- 恢复只经 maintenance resolution evidence 或新建不受影响的 Conversation。
- 需要从原 spec 删除“下一 turn 自动读取 advisory”的表述，并保留 advisory 不写入用户 RunEvent 的边界。

## 2. Public fixture 的执行隔离

**用户决定：采用最小可用生产建议。**

- public candidate 认证的唯一生产执行模式改为 `isolated_fixture_v1`。
- 不再使用 `local_trusted + network=allowed + resource_limits=none` 执行普通用户 candidate。
- 每次 fixture execution 使用临时隔离实例、低权限身份、默认拒绝网络、固定资源和时间上限。
- 实例不得挂载平台数据库、KMS/Vault、host credential、用户 Workspace、Artifact、Conversation 或其他用户资源。
- 执行结束必须停止完整进程树并销毁实例；输出只能进入受控 verification report。
- `PlatformFixtureTarget` 固定 sandbox、resource-limit、egress-policy 与 baseline digest。
- 若部署不具备该隔离能力，candidate submit 返回 `feature_unavailable`，不接受后再“尽力隔离”。
- conformance 必须验证宿主访问、横向访问、资源耗尽、越界网络、daemonize、残留进程 / 文件和销毁失败。
- 原 spec 中关于 `local_trusted`、无 sandbox、无资源上限的 public fixture 文字需要删除或改为非生产测试模式。

## 3. Secret 注入与输出泄露边界

**用户意见：不接受强制所有 secret executor 运行在 `isolated_secret_executor_v1`。**

当前项保持开放，待确认较弱的生产方案。暂定方向：

- 继续允许符合已声明 capability contract 的 `local_trusted` executor 使用 SecretUseBinding；
- 不把“secret 永不进入 Result / Artifact”表述成固定 Runtime 可以绝对保证的事实；
- 区分“平台控制面不主动记录 secret”和“executor 自身不得把 secret 输出到普通结果面”；
- 不立即引入全局 DLP 或强制 sandbox；通过 descriptor、secret slot、输出契约和 conformance 约束 executor；
- 若未来需要防恶意 executor、输出检测或隔离执行，再作为独立 execution profile 增加。

**用户决定：采用上述较弱但可实现的方案。**

- `local_trusted` executor 仍可使用 SecretUseBinding，只要 owner、descriptor slot、policy、approval、attempt/epoch 等既有校验通过。
- v1 不强制 sandbox，不实现全局 DLP，也不扫描所有输出以猜测 secret 洩漏。
- 平台保证 Observer、Resolver、Worker channel、日志、trace、RunEvent 与 LLM context 不主动记录或复制 secret plaintext。
- executor 是否把 secret 写入 Result / Artifact 属于 capability contract 与 conformance 责任；违规视为 executor/package conformance defect。
- 删除 spec 中“Result / Artifact / ValidationEvidence 永不接收 secret 正文”的绝对可证明表述，改为上述控制面边界。
- 外部发送、付费、发布等动作继续使用既有 ApprovalRequest；secret 不变成新的审批对象。

## 4. Fixture 环境故障与 candidate 认证结果

**用户决定：采用最小可用生产建议。**

- 将 candidate assertion failure 与 fixture environment unavailable 分离。
- fixture 正常运行但断言失败：submission=`rejected`，生成 failure notice。
- 平台无法可靠判定：execution=`inconclusive`，submission=`verification_retryable`，记录 retry_after、safe reason 和 evidence。
- 仅平台 verifier 可重试，最多 3 次；每次创建新的 execution，不覆盖旧 execution。
- 重试仍无法判定时保持 retryable，进入平台 maintenance / operator attention，不伪造 candidate failure。
- 环境故障不改变 candidate digest、owner approval 或 package bytes；只有候选内容或授权绑定变化才需要新 submission。
- User disable 仍优先转既有 withdrawal 路径。

## 5. Capability key 与 conformance profile 分离

**用户决定：采用最小可用生产建议。**

- `public_capability_key` 只代表稳定的接口语义身份。
- `capability_interface_digest / semantic_contract_digest` 固定操作、schema、前后条件、权限、副作用和 target compatibility。
- 新增 `ConformanceProfileRevision`，单独固定 fixture set、runner、environment、verifier 与资源/网络限制。
- 接口语义不变而 fixture、runner 或 verifier 变化时，只创建新的 profile revision，不创建新 capability key。
- `CapabilityPublicationAttestation` 同时 pin key/interface digest、profile revision、fixture execution digest 与 VerificationReport。
- interface digest 变化才强制新 key；profile retired 只阻止新的 publication/recommendation，不改写旧 CapabilityLock。
- 安全撤销仍通过既有 package revoke / CapabilityRevocation / advisory 路径处理。

## 6. Maintenance 期间的安全收敛路由

**用户决定：采用最小可用生产建议。**

- 新增闭合的 `MaintenanceRouteMatrix`，逐 route 固定准入、actor、是否可创建领域对象、既有 ID 绑定和 retry class。
- maintenance 期间允许健康/审计读取、既有 Attempt 的 heartbeat/progress/terminal report、既有 cancellation/stop/cleanup 重试与首次安全收敛命令、restore reconciliation、verifier、audit seal，以及精确 digest 的 emergency revoke/advisory。
- 允许的首次 stop/fence/cleanup 必须由已有安全事实触发，并绑定已有 Run/Attempt/Assignment/Conversation；不能携带新 DAG、prompt、Artifact 或普通业务参数。
- maintenance 期间拒绝新 Conversation、Message、Run、PlanCommit、Dispatch、Assignment、Attempt、普通 provision/upload/publish、普通 policy 修改和普通 release promotion。
- 删除或改写原 spec 中“拒绝所有新的控制命令”的宽泛表述，改为“拒绝新的普通业务控制命令；安全收敛命令按 MaintenanceRouteMatrix 放行”。
- 所有放行 route 继续使用 durable due-work/outbox、幂等键和 CAS。

## 7. User disable 的安全总闸与 fan-out

**用户决定：采用最小可用生产建议。**

- User disable 原子事务只更新 `User.status=disabled`、递增 `user_security_epoch`、写入 `UserDisableOperation` 和 durable outbox。
- 所有 user/agent token、WorkerSession、device-code、approval consume、secret resolve、artifact/capability、Conversation/Run 写入口都必须复核 active status 与 epoch。
- agent revoke、session close、Run cancellation、candidate withdrawal、approval/device-code invalidation、runtime cleanup 由 due-work/outbox 分批幂等执行。
- 每个 fan-out 子动作使用既有 durable ID、可恢复重试并记录审计。
- disable API 在安全总闸提交后即可返回 disabled；可另返回 `fanout_state=pending|converged|attention_required`，但不影响 User disabled 权威状态。
- 修改原 spec 中可能暗示“所有对象在同一大事务内完成”的文字，改为“即时安全阻断 + 最终 fan-out 收敛”。

## 8. Conversation create pending 操作的可见性

**用户决定：采用最小可用生产建议。**

- 增加 owner-only `get_create_operation(operation_ref)`。
- 增加 `list_pending_create_operations(workspace_id)`。
- 只返回 operation 状态、时间、safe error code、retry 时间和已绑定的 ConversationRef 摘要；不返回 raw idempotency key、Prompt、Message、路径或 LLM context。
- 对长期无进展 operation 提供 stalled/attention_required 的只读投影和运维告警。
- 暂不提供用户取消创建，不新增 create retirement 状态机。
- 永久保留原有 idempotency replay record，防止迟到重放创建第二个 Conversation。

## 9. PlatformOperator 更换与自锁死防护

**用户决定：采用最小可用生产建议。**

- 新增 `PlatformOperatorAssignmentChangeOperation`，支持普通 replace 和 emergency_replace。
- replacement 必须绑定 candidate User/identity、expected assignment version、外部治理批准和独立 evidence。
- Observer 以 CAS 事务同时激活新 assignment、撤销旧 assignment、递增 assignment generation 并写审计。
- 硬不变量：任意时刻 `count(active PlatformOperatorAssignment) == 1`。
- 当前 operator 不能直接撤销自己；必须先完成 replacement。
- 当前 operator 不可用时，只允许外部治理的 emergency replacement，不能由 User token、Driver 或 Workspace 管理员旁路。
- assignment generation 变化后，旧 release/catalog approval 按既有规则失效，必须新建 approval。

## 10. User enable 的权威入口与并发

**用户决定：采用最小可用生产建议。**

- 新增统一的 `UserStatusChangeOperation(action=disable|enable)`，带 expected status、reason code、operator/authn evidence、幂等引用和状态。
- 只有 active PlatformOperator 可执行；enable 重新验证原 ExternalIdentityLink、issuer allowlist 和 active security cause。
- enable 只恢复 User=status=active 并递增 security epoch；不恢复旧 user/agent token、WorkerSession、device-code、Run、Conversation 或 candidate。
- agent 必须重新完成 device-code；旧 disable fan-out work 携带目标 epoch，发现 epoch 不匹配即 obsolete，不得撤销新 session。
- disable fan-out 尚未完成时允许安全 enable。
- 同一状态的重复 enable/disable 幂等；并发状态变更用 status-version CAS，冲突者返回 conflicted 并重新读取。
- 每次状态变更写不可变审计。

## 11. SigningKey 轮换与精确引用

**用户决定：采用最小可用生产建议。**

- 每个 typed usage 新增 `SigningKeyUsagePointer(current_key_id, generation, verifier_profile_digest)`。
- 新增不可变 `SigningOperation`，固定 object digest、实际 key、verifier profile、签发状态和 evidence。
- 轮换先准备并验证新 KMS/HSM key，再通过 rotation CAS 原子切换 current pointer、递增 generation、将旧 key 置为 retiring。
- 已开始的签发继续使用已 pin 的 key；retiring key 不接收新签发。
- overlap/retention 满足后才可退役私钥或 KMS handle；公开验证材料、算法和 verifier profile 永久保留。
- 新增 key 到对象的反向引用，供 compromise assessment 和精确处置。
- current key 被 revoke 时暂停该 usage 新签发；必须先激活替代 key，不自动重签历史对象；无替代 key 时进入 maintenance。

## 12. Deployment/configuration staged activation

**用户决定：采用最小可用生产建议。**

- 新增 `DeploymentChangeOperation`，绑定候选 build set、configuration digest、manifest、SystemConformanceEvidence 和 expected deployment generation。
- pipeline 先登记候选；所有必需组件提交 exact readiness report 后，Observer 验证 bundle、DB schema range、基础设施 identity、policy digest 与 conformance evidence。
- 通过后以 CAS 原子切换 current deployment build set、current configuration digest 并递增 deployment generation。
- 验证失败保持旧 current，不把候选当 active。
- 混合版本只允许 manifest 明确支持的 startup/health/restore/report route；contract/destructive migration 继续要求 maintenance 和 restore checkpoint。
- RunControl active-default 只能在 deployment current ready 后切换。
- 该对象只处理 deployment 级切换，不引入 Agent rollout 编排。

## 13. Platform policy 的 authority 分层

**用户决定：采用最小可用生产建议。**

- 平台管理员/PlatformOperator 唯一决定平台硬上限、安全阈值和策略生效语义。
- Workspace owner 可在平台 envelope 内选择更严格的 workspace policy，不得放宽平台上限或安全下限。
- ParentContract/ChildContract 可为单次 Run/Node 进一步收紧 deadline、retry、并发等限制；不得提高平台 ceiling。
- Driver-local model budget 由 Driver 管理，但不得超过平台 ceiling；owner 可在 ceiling 内调整后续预算。
- Worker 只报告实际 capacity/health/liveness，不能提高平台限额或自行判定授权。
- 新增 `PlatformPolicyRevision(policy_key, version, digest, body)` 与 per-key active pointer；策略值变化创建新 revision，应用语义变化仍需新的 deployment contract/conformance。

## 14. Release approval consume-time TOCTOU 复核

**用户决定：采用最小可用生产建议。**

- `PlatformReleaseChangeApproval` 增加 immutable `consume_preflight_digest_set`。
- 集合至少包含 deployment/configuration、InterfaceContractBundle、Extension/Policy matrix、DependencyHealthPolicy、candidate manifest/handler/generator、candidate signing key/verifier profile、PromotionValidationEvidence 与 critical advisory 状态。
- 消费 CAS 时同一事务重验 operator/User/TTL、expected-current、全部 digest、bundle 可读性、key 可签发性、evidence pass/binding、advisory 和 maintenance gate。
- 任一变化将 approval 置为 `invalidated(reason=approval_snapshot_stale)`，pointer 不变。
- 旧 approval 不可重新批准或复用；必须创建新的 approval ID 和新的 evidence。

## 15. AuditCheckpoint 链与未封存尾部

**用户决定：采用最小可用生产建议。**

- `AuditCheckpoint` 增加 `prev_checkpoint_id`、覆盖范围、`ledger_head_digest`、`seal_watermark`、签名 key/verifier profile 和 WORM ref。
- checkpoint 必须链式连续；leader failover 通过 durable due-work/outbox 重放同一 checkpoint ID。
- 将封存时间拆为三层阈值：`seal_target_interval=5m`（目标，不是失败条件）、`seal_warning_after=10m`（告警并继续有限重试）、`seal_hard_deadline=30m`（持续超时才进入 maintenance）。
- release promotion/rollback、maintenance exit、operator assignment change、key rotation 等高风险操作前必须封存并验证相关 audit range。
- 封存失败先拒绝该高风险操作，不自动触发全局 maintenance。
- `AuditSealPolicy` 负责版本化保存 target interval、warning/hard deadline、retry backoff 与高风险操作 freshness 要求。
- 恢复时校验 WORM head 与 DB ledger；DB 落后则重建操作副本，DB 超出 WORM head 的部分只能标记为 unsealed tail。
- 断档、无法解释或签名/root 不一致时进入 maintenance，不自动重签、补洞或删除。

## 16. 永久留存与物理存储分层

**用户决定：采用最小可用生产建议。**

- 逻辑 Run、RunEvent、AuditRecord、AuditCheckpoint、release、tombstone、必要 package/evidence 事实仍永久保留。
- 新增可验证 `StorageCompactionOperation`，支持 hot/cold/WORM 物理分层。
- 热层保存当前 projection、最近事件、活跃 due-work、查询索引；冷层保存终态 Run 压缩 segment、旧 release handler 和历史 evidence；WORM 保存 checkpoint、必要审计 root、tombstone 和长期验证材料。
- 迁移先复制/压缩并校验 target digest，成功后才释放 source；失败保留 source，任何时刻至少有一个可验证副本。
- 逻辑 API、ID、digest、引用关系和 cursor 语义不变；cold 读取可有明确延迟，但不能伪造不存在。
- 存储故障进入 maintenance；不猜测、不重建、不删除永久事实。
- capacity reserve 继续优先保护 cancel、terminal、cleanup、fence、outbox 和 audit；普通新工作先拒绝，compaction 由 due-work 调度。
- conformance 覆盖迁移 crash/retry、digest/引用稳定、tombstone non-resurrection、cursor 连续性和容量耗尽时的安全收敛写入。

## 后续维护触发风险复查

以下问题是在确认 16 项后，对两份 spec 的 maintenance 触发条件再次扫描发现的，按顺序继续确认。

### 17. 短暂依赖故障是否会误触发 deployment maintenance

**用户决定：采用最小可用生产建议。**

- 新增 `RouteDegradedState`，与 deployment-level `DeploymentMaintenanceRecord` 分离。
- transient outage 只打开受影响 route 的 circuit breaker，不直接进入全局 maintenance。
- 只有 DB/ledger 权威性、AuditCheckpoint continuity、configuration identity、schema/contract、KMS/signing trust 或长期且影响安全收敛 reserve 的故障，才允许升级到 deployment maintenance。
- 依赖升级必须满足连续失败次数、最短持续时间和版本化 escalation policy；单次 health-check failure 不得触发全局 gate。
- 恢复同样采用连续成功 probe 迟滞；全部 active maintenance cause 各自 verifier pass 后才退出 maintenance。
- MaintenanceRecord 增加 scope、cause class、failure count、first/escalated/probe timestamps 和 escalation policy digest。
- MaintenanceRouteMatrix 继续决定 maintenance 期间允许哪些安全收敛 route。

### 18. 存储容量压力与安全 reserve 耗尽

**用户决定：采用最小可用生产建议。**

- 新增按 storage pool / usage scope 派生的 `StoragePressureState`，至少区分 `normal|pressured|reserve_at_risk|reserve_exhausted|accounting_unknown|integrity_failed`。
- owner Artifact quota、staging quota 或普通容量耗尽只拒绝受影响的 upload / provision / publish 等普通 route，不进入 deployment maintenance。
- `reserve_at_risk` 关闭普通业务写入，但继续放行 cancel、terminal、cleanup、fence、outbox、audit 和 recovery 等安全收敛写入。
- 新增不可被普通业务占用的 `control_plane_emergency_reserve`，并以有界最坏情况计算其容量。
- `reserve_exhausted` 时，若 emergency reserve 仍可保证安全收敛，则保持 route degraded / operator attention；只有连安全写入也无法保证时才追加 deployment maintenance cause。
- 容量账本无法判定，或 digest、tombstone、WORM、ledger / object reference 完整性失败时，进入 maintenance，不得猜测。
- 平台不得通过自动删除、驱逐或覆盖用户 Artifact 或永久事实恢复容量；只能经 owner 删除或已定义的 `StorageCompactionOperation` 回收。
- 恢复准入要求容量回到版本化 `StorageCapacityPolicy` 的安全水位、连续成功 probe，且 accounting、digest、WORM 和 outbox 验证通过；不因瞬时释放少量容量立即恢复。
- 收窄“存储故障进入 maintenance”的表述：只有安全 reserve 无法保证、容量账本不可判定或存储完整性失败才能升级为 deployment maintenance。
### 19. Restore verification 中单个资源缺失是否会永久阻断整个 deployment

**用户决定：采用最小可用生产建议。**

- 新增 `RestoreVerificationOperation`、`RestoreInventoryManifest` 和 `ResourceIntegrityIncident`。
- Restore verification 分为权威根验证和资源完整性验证两阶段；权威根或影响边界无法判定时仍保持 deployment maintenance。
- 普通 Artifact、历史 package 或 evidence 缺失若能通过备份 inventory 完整枚举，则建立不可变 `ResourceIntegrityIncident`，只隔离受影响资源和 route，不阻断整个 deployment。
- Artifact 标记 `integrity_unavailable` 后拒绝 read / materialize / execute；非终态 Run 生成 `artifact_integrity_unavailable` decision；终态 Run 保持 outcome 并在 ResultView 显示交付物不可用。
- 当前 release、active runtime 或验证路径依赖的 package / evidence 缺失时关闭受影响 route；无法限定影响范围、以及 tombstone、ledger 或 audit root 缺失时，仍保持 maintenance。
- `RestoreInventoryManifest` 固定 snapshot / backup ID、资源范围、metadata/index Merkle root、object inventory root、tombstone root、ledger/checkpoint watermark、签名与 configuration identity；恢复先比较 root，再枚举差异。
- 退出全局 maintenance 前必须证明权威根通过、缺失集合可完整枚举、每个缺失均有 durable incident 和访问 fence、无 tombstone resurrection 风险，且未受影响范围仍可信。
- 不得从日志 / Worker 内存重建对象，不得以空文件、重新执行或新 digest 替代旧对象，不得改写 Run outcome 或假装 owner delete。对象找回后必须校验原 digest / length 并追加恢复 evidence。
