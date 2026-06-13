# Control Plane §1.3 修复 — E2E 证据

- **日期**：2026-06-13
- **分支**：`worktree-fix-control-plane-1.3` @ HEAD `252c3b5`
- **位置**：`tests/prod_test/.e2e-2026-06-13/`（沿用 PR #10/#11 e2e 工作目录）
- **拓扑**：host-local codex CLI → driver-agent (stdio MCP) → agent.cs.ac.cn (生产 ws-prod) → slave-agent → 本地 observer :18091
- **依据**：memory `e2e_required_for_features_and_fixes`

## 环境

| 角色 | binary | sandbox_id | short_id |
|---|---|---|---|
| driver | `bin/driver-agent.linux-amd64` (build @ 252c3b5) | 4220eca2-... | pqbefqqu |
| slave  | `bin/slave-agent.linux-amd64`  (build @ 252c3b5) | 4fe08dd5-... | wvwpv37q |
| observer | `bin/observer-server.linux-amd64` (build @ 252c3b5) | — | — |

凭据复用 PR #10 device-code OAuth 注册的 ws-prod workspace `6f55e9fe-...`。Observer DB fresh start（清掉 db/wal/shm + token files），所以 driver/slave 首次 register 走 fresh path，§1.3 #11 的 5min duplicate guard 不触发（无前序 registration in DB）。

## 测试 prompt

5-step：list_agents → submit_task A → wait_task A → submit_task B → wait_task B。完整 prompt 见 `tests/prod_test/.e2e-2026-06-13/e2e-prompt.txt`（沿用 PR #11 同一份）。

## 结果（5/5 PASS）

codex 5 步全过，driver_tasks.jsonl 记录两个 task，slave data.db 显示两个 task 都 completed：

```
=== driver-tasks.jsonl ===
{"ts":"2026-06-13T12:31:16","event":"delegate_task","task_id":"task_08b9f460-...","skill":"chat","status":"pending"}
{"ts":"2026-06-13T12:31:46","event":"delegate_task","task_id":"task_c10ebfce-...","skill":"chat","status":"pending"}

=== slave tasks ===
task_08b9f460-f40b-4c09-8daf-9507f50f404c|chat|completed|91
task_c10ebfce-439b-4115-b898-d6d46614e1d7|chat|completed|91
```

codex final report："E2E DONE" — `Step 1 returned slave-codex-local`, `TASK_ID_A` 完成, `TASK_ID_B` 完成。

## §1.3 修复在本次 e2e 的真实路径覆盖

| Spec § | Plan task | E2E 验证 |
|---|---|---|
| §1.3 #9 bootstrap timeout + degraded | Task 2 | ✅ driver+slave 启动成功（observer relay 即使持续 401 也不阻塞 startup）|
| §1.3 #10 artifact/write 502 + log | Task 4 | ✅ audit.log 含 33 条 `observer_relay_error` (op=serve_pending 持续 401)。修复前是 `_ = err` 静默；修复后每条都有 stderr + audit 日志，操作员可见 |
| §1.3 #11 register force | Task 5 | ⚠️ 未触发（fresh observer DB，第一次注册）— unit test 覆盖 `TestRegister_RejectsRecentDuplicateWithoutForce` / `TestRegister_AcceptsRecentDuplicateWithForce` |
| §1.3 #12 cooldown gate | Task 3 | ✅ 间接验证（两个 wait_task 都返回 completed，cooldown 期间没事件丢失）— unit test `TestClient_CooldownGateRetainsEventsWhenRegisterFails` 直接验证 |
| §1.3 #13 BlobStore tx | Task 6 | ⚠️ 未触发（happy path 无并发 push）— unit test `TestBlobStore_ConcurrentPutSameSha_NoDoubleWrite` race-clean |

## 防回归 (driver_journal_error count = 0 in happy path)

`driver_journal_error` 计数 0 — PR #11 (Task 1) 加的 driver_journal 分类没误用，happy path 不触发。

## 已知现象：slave tunnel EOF flap

`slave.log` 显示 slave-agent 反复 1-16s 内重连（"tunnel disconnected: EOF" → "reconnecting in Ns")，sandbox_id 相同。这是 MEMORY `jetson_outage_modes` mode B "双进程互踢"的早期 ITERATION 模式。**但任务最终都 completed**，没影响 e2e 结果。

可能根因：之前的 prod_test slave 实例（PID 173263 等）没 100% 清掉，留下 tunnel 抢占。**不属于 §1.3 范围**——PR #11 §1.2 #6 已经修过 dispatch idempotency，本次 PR 不应处理。Follow-up 需要单独诊断。

## 关停

```bash
pkill -9 -f "tests/prod_test/bin/"
```

`.e2e-2026-06-13/` 保留（gitignored），作下次重放快照。
