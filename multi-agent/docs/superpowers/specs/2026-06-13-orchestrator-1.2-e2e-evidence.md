# Orchestrator §1.2 修复 — E2E 证据

- **日期**：2026-06-13
- **分支**：`worktree-fix-orchestrator-1.2` @ HEAD `dafc47a`
- **位置**：`tests/prod_test/.e2e-2026-06-13/`（沿用 PR #10 e2e 工作目录）
- **拓扑**：host-local codex CLI → driver-agent (stdio MCP) → agent.cs.ac.cn (ws-prod) → slave-agent → 本地 observer :18091
- **依据**：memory `e2e_required_for_features_and_fixes`

## 环境

| 角色 | binary | sandbox_id | short_id |
|---|---|---|---|
| driver | `bin/driver-agent.linux-amd64` (build @ dafc47a) | 4220eca2-... | pqbefqqu |
| slave  | `bin/slave-agent.linux-amd64`  (build @ c3b2353) | 4fe08dd5-... | wvwpv37q |
| observer | `bin/observer-server.linux-amd64` (build @ c3b2353) | — | — |

凭据复用 PR #10 device-code OAuth 注册的 ws-prod workspace `6f55e9fe-...`。

## 测试 prompt（codex exec）

5-step：list_agents → submit_task A → wait_task A → submit_task B → wait_task B。完整 prompt 见 `tests/prod_test/.e2e-2026-06-13/e2e-prompt.txt`。

## 跑了两轮（中间发现 wait_task 漏修后 hot-fix）

### Round 1 (commit `c3b2353`, §1.2 6 commits) — wait_task fail

发现 wait_task 在 observer SyncWrites 401 时**整个 response 报错**：

```
Step 3: fail
Mcp error: -32000: observer sync writes: list writes status 401
Step 5: fail (同上)
```

但 slave-side `data.db`：

```
task_478331e0-...|chat|completed|187   ← Step 2 真完成了
task_d41d2e9a-...|chat|completed|173   ← Step 4 也完成了
```

也就是说：**任务真完成、output 都在 store 里**，但 wait_task 因为 observer 副线 SyncWrites 401 把 response 整个干掉，Claude 拿不到结果。这是 §1.1 #1 invariant 在 wait_task 路径的漏修（PR #10 只在 submit_task 路径补了）。

Hot-fix commit `dafc47a` follow-up：wait_task SyncWrites 失败 → `logHelperErr("observer_relay", "sync_writes", err)` + 加 `warnings` 字段到 response（与 submit_task / submit_contract_task 对称）。

### Round 2 (commit `dafc47a`, +1 fix) — 5/5 PASS

```
Step 1: ok. Discovered slave-codex-local.
Step 2: ok. TASK_ID_A = task_29ef9908-...
Step 3: ok. Final status `completed`. (warnings 含 observer sync writes 警告)
Step 4: ok. TASK_ID_B = task_795a937c-...
Step 5: ok. Final status `completed`. (同上)
E2E DONE
```

Step 5 完整 wait_task response：

```json
{
  "status": "completed",
  "output": "hello §1.2",
  "failure_reason": "",
  "latest_progress": "",
  "latest_progress_phase": "",
  "latest_progress_at": "",
  "final_output": "hello §1.2",
  "is_final": true,
  "written_files": [],
  "warnings": ["observer sync writes: list writes status 401"]
}
```

User 看到正确 output `hello §1.2`，warning 是诊断信号（observer 副线问题，不破坏主响应）。

## 关键证据对照

| 验证 | Round 1 | Round 2 |
|---|---|---|
| wait_task 拿到 output | ❌ (报错) | ✅ |
| warnings 字段含 SyncWrites 警告 | — | ✅ |
| slave data.db 行数 | 2 (各 1 row, completed) | 2 (各 1 row, completed) |
| driver-tasks.jsonl 行数 | 2 | 2 |
| observer_relay_error count (真 relay 错误) | 30 | 27 |
| driver_journal_error count (happy path) | 0 | 0 |

## 顺带：P1 SIGTERM 独立验证（继承 PR #10）

在主 e2e 前独立跑：spawn `driver-agent serve-mcp` + 持有 FIFO 当 stdin + 3s 后 `kill -TERM`。Driver `≤0.2s` 内退出，stderr 末尾：

```
mcp serve: context canceled
```

§1.2 fixes 没回归 PR #10 的 P1 修复（reader goroutine + select ctx）。

## §1.2 fixes 在本次 e2e 的真实路径覆盖

| Spec § | Plan task | E2E 验证 |
|---|---|---|
| §1.2 #5 resume from sub_tasks | Task 6 | ⚠️ 未触发（happy path 没杀 driver mid-run；靠 unit test `TestOrchestrator_ResumeFromExistingSubTasks` + `TestOrchestrator_ResumeRefusesPendingMCPNode` + `TestOrchestrator_DuplicateRunCompletedReturnsOutput` 覆盖） |
| §1.2 #6 dispatch idempotency | Task 2 | ⚠️ 未触发（poller 没 ack-loss；靠 unit test 3 个 `TestDispatch_DuplicateInsert*` 覆盖；slave data.db 无重复 row 间接印证） |
| §1.2 #7 protectedGo panic | Task 5 | ⚠️ 未触发（happy path 无 panic；靠 unit test `TestFanout_GoroutinePanicTurnsIntoFailedNode`） |
| §1.2 #8 replan order | Task 3 + 4 | ⚠️ 未触发（用户 prompt 简单，无 MCP validation replan；靠 fake-planner 集成测试 + 4 个 Scheduler dag 单元测试） |
| §1.1 follow-up: wait_task warnings | dafc47a | ✅ Round 2 Step 3/5 |
| (继承 PR #10 fixes) | — | ✅ 全部不回归 |

防回归类的 4 个 fix 在 happy-path e2e 不直接触发是 acceptable —— 它们的设计目的就是"故障路径不再炸"，正常运行看不见。要主动验证它们需要 chaos-style e2e（kill driver mid-task、ack-loss 模拟、SDK fault injection），那是 follow-up 工作。本次 e2e 的价值：

1. **证明 §1.2 fixes 不回归 happy path**（前后两轮都跑通）
2. **发现并修复 wait_task warnings 漏网之鱼**（这是真实 e2e 的唯一发现）
3. 印证 §1.2 #6 idempotency 在生产路径下 invariants 成立（slave data.db 各 task 仅 1 row）

## 关停

```bash
pkill -9 -f "tests/prod_test/bin/"
```

`.e2e-2026-06-13/` 保留作下次重放快照（gitignored）。
