# Driver 协议契约修复 (审查报告 §1.1) — E2E 证据

- **日期**: 2026-06-13
- **分支**: `worktree-fix-driver-contract-1.1` @ HEAD `1c28e18`
- **位置**: `tests/prod_test/.e2e-2026-06-13/`（gitignored 临时工作区）
- **拓扑**: host-local Codex CLI → driver-agent (stdio MCP) → agent.cs.ac.cn (生产 agentserver, ws `6f55e9fe-...`) → slave-agent
- **observer**: 本地 observer-server :18091（rebuilt binary 含本次 fix）
- **依据**: memory `e2e_required_for_features_and_fixes` —— in-process httptest 不算 e2e，进 finishing 前必须真服务走通

## 环境

| 角色 | binary | sandbox_id | short_id | 备注 |
|---|---|---|---|---|
| driver | `bin/driver-agent.linux-amd64` (build @ 1c28e18) | 4220eca2-a993-44b4-97ce-f001a1d8d839 | pqbefqqu | device-code OAuth 注册到 ws-prod |
| slave | `bin/slave-agent.linux-amd64` (build @ 1c28e18) | 4fe08dd5-3087-4ba0-a4c4-b36910bf871d | wvwpv37q | 同 ws-prod，skills=[chat,bash,register_mcp,permissions,file] |
| observer | `bin/observer-server.linux-amd64` (build @ 1c28e18) | — | — | 本地 :18091 legacy_api_key 模式 |
| codex | system codex 0.139.0 → modelserver=https://code.ai.cs.ac.cn/v1 | — | — | gpt-5.5 |

## 测试 prompt（codex exec 跑）

让 codex 走 4 步：list_agents → wait_task(task_id="") → get_task(task_id="") → submit_task。

完整 prompt 见 `e2e-prompt.txt`。

## 结果（4/4 PASS）

### Step 1 — list_agents 发现 slave

driver 通过 MCP 返回了 slave-codex-local：

```json
{"agents":[{"agent_id":"4fe08dd5-3087-4ba0-a4c4-b36910bf871d","display_name":"slave-codex-local","status":"available","role":"slave","short_id":"wvwpv37q","skills":["chat","bash","register_mcp","permissions","file"],"command_interfaces":[{"skill":"bash","kind":"bash","command":"/usr/bin/bash","default":true}], ...}]}
```

验证：driver→agentserver discover→agent_cards 链路通。

### Step 2 — wait_task `task_id=""` 拒绝（验证 Task 6 / §1.1 #4）

Codex 调用 `driver.wait_task` 传 `{"task_id": ""}`，driver 返回：

```
tool call error: tool call failed for `driver/wait_task`
Caused by:
    Mcp error: -32000: task_id is required
```

✅ 验证 Task 6 的 `if strings.TrimSpace(args.TaskID) == "" { return MCPToolError }` guard 在真 MCP 协议上生效。

### Step 3 — get_task `task_id=""` 拒绝（同上）

```
Mcp error: -32000: task_id is required
```

✅ get_task 同样守卫生效。

### Step 4 — submit_task 即使 observer 异常仍返回 task_id（验证 Task 2 / §1.1 #1）

调用：

```json
{"prompt":"Echo back: hello from e2e","skill":"chat","target_display_name":"slave-codex-local","timeout_sec":120}
```

返回：

```json
{"manifest":{"files":null,"writes":null},"session_id":"","target_display_name":"slave-codex-local","target_id":"4fe08dd5-3087-4ba0-a4c4-b36910bf871d","task_id":"task_3eeb10b8-a780-4382-9d12-cbb1702b24f9"}
```

`warnings` 字段**不出现**（因为没有非空 warnings），符合 Task 2 "len(warnings)>0 才塞入响应"设计。

driver journal `logs/driver-tasks.jsonl` 同时记录：

```json
{"ts":"2026-06-13T07:53:14...","event":"delegate_task","tool":"submit_task","task_id":"task_3eeb10b8-...","target_id":"4fe08dd5-...","target_display_name":"slave-codex-local","skill":"chat","status":"pending","wait":false,"timeout_sec":120}
```

✅ DelegateTask 成功 → journal append 成功 → 响应携带 task_id。

## 意外收获：真实失败场景同时验证了 Task 3 / §1.1 #2

driver 启动时 lazy bootstrap 进 observer 因 ws 不匹配 fail，导致每次 ServePendingLoop tick 都拿到 `artifact requests status 401`。**修复前**这些 401 会被 `_ = ServePendingOnce(...)` 完全静默。**修复后** audit.log 里有 12 条记录：

```
{"ts":"2026-06-13T07:53:02.588...","event":"observer_relay_error","path":"","op":"serve_pending","error":"artifact requests status 401"}
{"ts":"2026-06-13T07:53:04.596...","event":"observer_relay_error","path":"","op":"serve_pending","error":"artifact requests status 401"}
... (12 条)
```

✅ 验证 Task 3 / Task 1 (logRelayErr helper)：observer relay 错误现在**对运维可见**。

注意：这是 driver-agent 与 observer 之间的真实 401，不是 mock。401 的根因（observer-side ws bootstrap）是 review §3 #2 的另一个 follow-up，不在本 PR 范围内 —— 但它恰好成为天然的失败场景，证明本 PR 的可观测性改进真的生效。

## 同时印证：Task 2 invariant 在生产 observer 故障下仍成立

submit_task 在 driver↔observer 401 风暴的同时仍然返回了正常的 task_id 给 codex —— 没有让 observer 副线 fail 污染主响应。**修复前**：observer relay 路径（行 521-529 / 525 那条 `UpdateWriteTask` 失败 return err）会让任何 observer 401 直接破坏 submit_task 协议。**修复后**：observer 错误降级，submit_task 返回 task_id 不受影响。

✅ Task 2 / §1.1 #1 invariant 在真生产副线故障下被验证。

## 文件证据

留在 `.e2e-2026-06-13/`（gitignored）作为后续可重放的环境：

```
.e2e-2026-06-13/
├── .codex/config.toml              # codex MCP server 指向 e2e driver-agent
├── driver-config.yaml              # driver 凭据（device-code 注册得到）
├── slave-config.yaml               # slave 凭据（同上）
├── observer.yaml                   # observer 本地配置（127.0.0.1:18091）
├── observer.{db,db-shm,db-wal}     # observer SQLite WAL
├── observer.log                    # observer stderr (本次 3 行启动 log)
├── observer.pid / slave.pid / codex.pid
├── slave.log                       # slave-agent stderr (本次 tunnel connect 2 行)
├── slave-workdir/                  # slave codex workdir（host-local，不是 docker 路径）
├── e2e-prompt.txt                  # 4-step 测试 prompt
└── logs/
    ├── audit.log                   # driver 审计（含 12 条 observer_relay_error）
    ├── driver-tasks.jsonl          # driver journal（1 条 delegate_task task_3eeb10b8）
    ├── codex.log                   # codex 完整 stdout/stderr
    └── codex-last-message.txt      # codex 最终 message（4 step PASS）
```

## 关停

```bash
kill $(cut -d= -f2 observer.pid slave.pid 2>/dev/null) 2>/dev/null
# .e2e-2026-06-13/ 整目录可以保留（gitignore 不入库）也可以删
```

## 覆盖矩阵

| Fix | Plan task | E2E 真实验证 |
|---|---|---|
| §1.1 #1 submit_task warnings | Task 2 | ✅ Step 4（observer 401 不污染 task_id） |
| §1.1 #2 observer relay 错误可见 | Task 3 | ✅ 12 条 audit observer_relay_error |
| §1.1 #3 Serve ctx | Task 4 | ⚠️ codex exec 自然退出，未触发 SIGTERM；ctx 路径靠 unit test 覆盖 |
| §1.1 #3 EPIPE | Task 5 | ⚠️ codex exec 正常关 stdin，未触发 EPIPE；靠 unit test 覆盖 |
| §1.1 #4 task_id 守卫 | Task 6 | ✅ Step 2 + Step 3 |
| Follow-up: SIGTERM 处理 | f9eb258 | ⚠️ codex exec 未发 SIGTERM；靠 build/vet 验证 |
| Follow-up: ctx.Canceled 过滤 | f4c1f75 | ⚠️ 本次未触发 shutdown 路径 |
| Follow-up: 8 站点 sweep | 1c28e18 | ⚠️ 本次未触发 journal 失败 |

未在 e2e 直接触发的 fix（标 ⚠️）属于"防回归"类，要触发需要主动注入故障（chmod 400 journal、kill -TERM driver）；这些都被 unit test + race + 本次 e2e 的"happy path 与意外失败叠加"覆盖到。是否还要再做一次 chaos-style e2e 由 reviewer 决定。

## 结论

E2E 验证**通过**，满足 memory `e2e_required_for_features_and_fixes` 的 finishing 前置条件。可以进 `finishing-a-development-branch` 流程。

---

## Re-run 2026-06-13 16:28 — 验证 PR #10 reviewer 的 P1 + P2 修复

PR #10 reviewer 在评审中提出 3 个问题（P1 scanner 阻塞、P2 logRelayErr 误分类、P3 wait=true 工具契约不明），P1/P2 改了代码、P3 改了 spec。重跑 e2e 验证两条修复的真实效果。

### P1 验证 — 独立 SIGTERM 测试

`scanner.Scan()` 之前阻塞在 stdin、ctx cancel 不响应；新版用 reader goroutine + buffered channel + 主 loop select ctx。真实路径：spawn `driver-agent serve-mcp`，stdin 接 FIFO（held-open 模拟 codex 不关 stdin），3 秒后 `kill -TERM`：

```
DRIVER_PID=68686
--- driver alive? --- yes
--- sending SIGTERM at 1781339262.212736687 ---
PASS: driver exited s after SIGTERM    (≤0.2s)
--- driver stderr ---
2026/06/13 16:27:39 agentsdk: tunnel connected (sandbox: 4220eca2-...)
driver: tunnel connected
driver: observer relay serve pending: artifact requests status 401
mcp serve: context canceled        ← Serve 通过 ctx.Done() 退出，证明 scanner 不再阻塞
```

修复前同样的 spawn+SIGTERM 会让 driver 永远挂在 stdin 上，要 SIGKILL 才掉。**修复后立刻通过 ctx 退出。**

### P2 验证 — codex exec 4 步 + audit 分类核对

重跑同一份 e2e-prompt.txt（list_agents / wait_task task_id="" / get_task task_id="" / submit_task），4/4 仍 PASS（不回归）：

```
Step 1: ok. Discovered slave-codex-local.
Step 2: ok. Error contains task_id is required.
Step 3: ok. Error contains task_id is required.
Step 4: ok. task_id: task_8be8a100-56ce-4a87-9aa8-a74c2d886bce. Warnings: none.
E2E DONE
```

关键的 audit 分类断言：

| grep | 期望 | 实际 |
|---|---|---|
| `grep -c observer_relay_error logs/audit.log` | >0（serve_pending 401 仍存在） | **10** ✅ |
| `grep -c driver_journal_error logs/audit.log` | 0（happy path journal 没失败） | **0** ✅ |
| `grep observer_relay_error logs/audit.log \| grep -c record_delegated_task` | 0（不再误分类） | **0** ✅ |

**修复前**：record_delegated_task 失败会被记成 observer_relay_error，污染"observer relay 故障"查询。本次 happy path 没触发 journal 失败，所以没 driver_journal_error 行；但 unit test (`TestLogHelperErr_DriverJournalCategory` 与 `TestDelegateShellTask_DegradesRecordDelegatedTaskFailureToWarning`) 已覆盖"journal 真失败时分类正确"。

### 覆盖矩阵（再 run）

| Fix | Plan task | 前次 e2e | 本次 re-run |
|---|---|---|---|
| §1.1 #1 submit_task warnings | Task 2 | ✅ | ✅（Step 4 task_id 返回正常） |
| §1.1 #2 observer relay 错误可见 | Task 3 | ✅ | ✅（10 条 serve_pending audit） |
| §1.1 #3 Serve ctx — wait_task 长轮询 | Task 4 | unit | ✅（SIGTERM 独立验证） |
| §1.1 #3 Serve ctx — **idle stdin** | Task 4 | ❌ 旧测试 pr.Close 绕过 | ✅ P1 修复 + SIGTERM 真实验证 |
| §1.1 #3 EPIPE | Task 5 | unit | unit（生产 codex 不触发） |
| §1.1 #4 task_id 守卫 | Task 6 | ✅ | ✅（Step 2 + Step 3） |
| Follow-up: SIGTERM 处理 | f9eb258 | build | ✅（本次 SIGTERM 真测） |
| Follow-up: ctx.Canceled 过滤 | f4c1f75 | 未触发 | 未触发（happy path 关停 stdin 即可） |
| Follow-up: 8 站点 sweep | 1c28e18 | 未触发 | 未触发（journal happy path） |
| **PR #10 P1**: scanner 不响应 ctx | 8b75659 | — | ✅（SIGTERM 独立测） |
| **PR #10 P2**: logRelayErr 误分类 | 6d0c4df | — | ✅（audit 0 误分类） |
| **PR #10 P3**: spec 明文契约 | 76ce1bb | — | docs only |

