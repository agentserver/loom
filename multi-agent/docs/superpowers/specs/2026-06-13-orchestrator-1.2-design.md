# Orchestrator 不可恢复 + 重复派发修复 (审查报告 §1.2) — 设计文档

- **日期**：2026-06-13
- **来源**：`docs/review-2026-06-13.md` §1.2（CRITICAL #5–#8）
- **范围**：4 个编排/调度协议契约破裂点的最小修复
- **不在范围**：抽 `RunScheduler` 统一 fanout 与 driver_runner（§2 债，单独 PR）；server-side lease/heartbeat（agentserver 侧改）；observer 协议；SDK 接口

## 目标不变量

修复后下列四条必须始终成立：

1. **driver 重启后**：parent 任务在 `running` 状态 → 从 `sub_tasks` rebuild Scheduler，续跑未完节点。已 completed 节点保留结果不重派；assigned/running 节点视作"远端在跑"调 `WaitForTask` 续等；只有 pending 节点会被新 dispatch。
2. **同 task ID 被 poller 重投** → slave 进 `dispatch.Run` 只跑一个 executor；后到者立刻 idempotent 返回（不会启第二个 claude 子进程，消灭 MEMORY `jetson_outage_modes` mode B 的另一根因）。
3. **任何 fanout goroutine panic** → 不崩 driver；panic 转为该节点 `Status=failed, Error=panic: ...` 进 `doneCh`，Scheduler 继续处理。
4. **mcp validation replan** 顺序正确：原节点先 `MarkSuperseded`，新节点再 `Append`；`Append` 时未知 dep 立即报错，不允许"silently 永远不 ready"。

## 变更摘要

### 1. `internal/store/store.go` 新增 `InsertIfAbsent`

```go
// InsertIfAbsent inserts a task row if no row with this ID exists, returning
// (true, nil). On primary-key conflict returns (false, nil) — the caller can
// then look up the existing row to decide whether to replay (completed) or
// silently skip (running/assigned).
func (s *Store) InsertIfAbsent(t Task) (bool, error) {
    res, err := s.db.Exec(
        `INSERT OR IGNORE INTO tasks(id,skill,prompt,status,created_at) VALUES(?,?,?,?,?)`,
        t.ID, t.Skill, t.Prompt, "assigned", nowUTC(),
    )
    if err != nil {
        return false, err
    }
    n, _ := res.RowsAffected()
    return n > 0, nil
}
```

旧 `Insert` 保留，行为不变。

### 2. `internal/dispatch/dispatch.go:50–54` — slave 端 idempotent

```go
inserted, err := d.store.InsertIfAbsent(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt})
if err != nil {
    return executor.Result{}, err
}
if !inserted {
    return d.replayExistingTask(ctx, t)
}
if err := d.store.MarkRunning(t.ID); err != nil {
    return executor.Result{}, err
}
// ... existing emit + envelope strip + executor.Run ...
```

新 helper `replayExistingTask(ctx, t executor.Task) (executor.Result, error)`：
- 读 `tasks` 行 → 按 status 决定：
  - `completed` → 反解 stored output（chat skill 走 wrapper 解包逻辑 — 已有）→ 返回 `Result{Summary: ...}`，emit `EventSlaveTaskCompleted`（state=replay）
  - `running` / `assigned` → 返回 `Result{}, nil` 不 emit；log warning。poller 会按 reason 决定如何 ack（pending_acks 表里如果 task 已完成的话，poller 自己会下次 poll 拿到 final state）
  - `failed` → 返回原 error
- **关键**：不重启 executor，确保不会有第二个 claude 子进程

测试 (Bug #6) 包括"重 dispatch 时 spy executor 只被 Run 一次"。

### 3. `internal/orchestrator/orchestrator.go:72-128` — master 端 idempotent + resume

```go
func (o *Orchestrator) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
    inserted, err := o.store.InsertIfAbsent(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt})
    if err != nil {
        return executor.Result{}, err
    }
    if !inserted {
        return o.resumeOrReplay(ctx, t)
    }
    // ... existing path: MarkRunning + emit MasterTaskReceived + skill switch
}
```

`resumeOrReplay`：
- task 已 `completed` → 返回 stored output
- task 已 `failed` → 返回原 error
- task 仍 `running`/`assigned`：
  - `o.store.ListSubTasks(t.ID)` → 若空 → 继续走原 Run path（前次 plan 没写库就崩了；可以重 plan）
  - 若非空 → emit 新事件 `EventMasterTaskResumed`（payload: counts of completed/in-flight/pending），调 `runFanoutResume(ctx, t, rows)`（共享 runFanout 大部分逻辑，只是初始化 Scheduler 从 rows）

### 4. `internal/orchestrator/fanout.go` — 抽 rebuild + 改 runFanout 接受可选 rows

```go
// rebuildSchedulerFromRows reconstructs Scheduler + outputs + in-flight node
// IDs from persisted sub_tasks rows. Used on driver restart so we don't lose
// DAG state. Skipped rows (status='skipped') are recorded as finished:skipped.
func rebuildSchedulerFromRows(
    rows []store.SubTaskRow,
    maxConc int,
) (sched *Scheduler, plan []planner.Node, outputs map[string]string, inFlightIDs []string) {
    plan = nodesFromRows(rows)
    sched = NewScheduler(plan, maxConc)
    outputs = map[string]string{}
    for _, r := range rows {
        switch r.Status {
        case "completed":
            sched.MarkDispatched(r.NodeID)
            sched.Report(r.NodeID, "completed", r.Output, "")
            outputs[r.NodeID] = r.Output
        case "skipped", "failed":
            sched.MarkDispatched(r.NodeID)
            sched.Report(r.NodeID, r.Status, "", r.Error)
        case "assigned":
            sched.MarkDispatched(r.NodeID)
            inFlightIDs = append(inFlightIDs, r.NodeID)
            // child_task_id is on the row; caller will spawn WaitForTask
        case "pending", "":
            // stays in pending
        }
    }
    return
}
```

`runFanout` 增加 `resumeFromRows []store.SubTaskRow` 参数（nil 表示首次跑）。如果非 nil：
- 跳过 `o.planner.Plan(...)`、`InsertSubTasks`、`EventMasterPlanCreated`
- 用 `rebuildSchedulerFromRows` 初始化
- 对每个 `inFlightIDs` 节点：从 `row.ChildTaskID` 拿任务 ID，spawn 同样的 `WaitForTask` goroutine（用 protectedGo），结果进 doneCh
- 继续走主循环

### 5. `internal/orchestrator/fanout.go` — protectedGo + panic 防护

```go
// protectedGo wraps a worker goroutine: any panic is recovered and turned
// into a 'failed' doneCh send so the scheduler can move on instead of
// crashing the driver.
func protectedGo(doneCh chan<- done, nodeID string, fn func()) {
    go func() {
        defer func() {
            if r := recover(); r != nil {
                doneCh <- done{FinishedNode: FinishedNode{
                    NodeID: nodeID,
                    Status: "failed",
                    Error:  fmt.Sprintf("panic: %v", r),
                }}
            }
        }()
        fn()
    }()
}
```

把 fanout.go 所有 `go func(...) {...}()` 改用它：约 6 处（dispatched、dispatchSynthetic 的 GetArtifact 分支、dispatchSynthetic 的 synthetic-write 分支、Render 失败分支、authorizeMCP 失败分支、validation-replan-exhausted 分支）。准确数以实施时 `grep -n "^\s*go func" fanout.go` 为准。

### 6. `internal/orchestration/dag.go:235` — Append 拒绝未知 dep + 改 fanout 顺序

dag.go：

```go
func (s *Scheduler) Append(nodes []planner.Node) error {
    // existing dup check
    appended := make(map[string]bool, len(nodes))
    for _, n := range nodes {
        if _, exists := s.nodeByID[n.ID]; exists {
            return fmt.Errorf("Scheduler.Append: duplicate id %q", n.ID)
        }
        appended[n.ID] = true
    }
    // new dep-known check
    for _, n := range nodes {
        for _, dep := range n.DependsOn {
            if !appended[dep] {
                if _, known := s.nodeByID[dep]; !known {
                    return fmt.Errorf("Scheduler.Append: node %q depends on unknown %q", n.ID, dep)
                }
            }
        }
    }
    // existing append loop
}
```

fanout.go:579-591 改顺序：

```go
// 修复后顺序：先 supersede 旧节点（让它进 finished:skipped），再 Append 新节点；
// 否则若新节点 depends_on 旧 ID，Append 时旧节点还在 inFlight，
// Scheduler 判定 dep 未 completed → 新节点永远不进 pending。
supersededFn := sched.MarkSuperseded(n.ID, "superseded by mcp validation replan")
for _, skipped := range supersededFn {
    optionalByID[skipped.NodeID] = true
    recordSkippedDone(skipped)
}
if err := sched.Append(newPlan); err != nil {
    cancelAll()
    return executor.Result{}, fmt.Errorf("append mcp validation replan: %w", err)
}
allNodes = append(allNodes, newPlan...)
for _, appended := range newPlan {
    optionalByID[appended.ID] = appended.Optional
}
appendSubTaskRows(o.store, t.ID, newPlan)
```

mcp_tool_set replan 路径（fanout 后段相同模式）同改。

## 测试策略

| # | 测试 | 断言 | 包 |
|---|---|---|---|
| 1 | `TestStore_InsertIfAbsent` (table-driven: 第一次/第二次/不同 ID) | 第一次 (true, nil)，second (false, nil)，error path 正确 | store |
| 2 | `TestDispatch_DuplicateInsertSkipsExecutor` | spy executor 验证 Run 只调一次；第二次 Run 立即返回 | dispatch |
| 3 | `TestDispatch_DuplicateInsertReplayCompletedOutput` | 第一次跑完后第二次 Run 返回原 stored output（含 chat wrapper 解包） | dispatch |
| 4 | `TestDispatch_DuplicateInsertRunningTaskReturnsEmpty` | running 状态下第二次 Run 返回 Result{}, nil | dispatch |
| 5 | `TestScheduler_Append_RejectsUnknownDep` | Append node dep 不在已知集 → error | orchestration |
| 6 | `TestScheduler_Append_AllowsCrossAppendDep` | 同批 nodes 互相 dep OK | orchestration |
| 7 | `TestFanout_GoroutinePanicTurnsIntoFailedNode` | inject panic 的 SDK fake；driver 不崩；Scheduler 看到 failed 节点 | orchestrator |
| 8 | `TestFanout_ReplanSupersedeBeforeAppend` | 用 spy SDK 让节点验证失败触发 replan；断言原节点在 Append 之前进 finished:skipped | orchestrator |
| 9 | `TestOrchestrator_ResumeFromExistingSubTasks` | 预填 parent (running) + 3 行 sub_tasks (completed/assigned/pending)；Run → 验证 only pending 被新 dispatch、assigned 走 WaitForTask、completed 不重派 | orchestrator |
| 10 | `TestOrchestrator_DuplicateRunCompletedReturnsOutput` | parent completed 时第二次 Run 返回 stored output 不重跑 | orchestrator |

回归：`go test ./internal/store/... ./internal/dispatch/... ./internal/orchestrator/... ./internal/orchestration/... -race -count=1`。

## 兼容性

| 变更 | 影响 |
|---|---|
| 新 `store.InsertIfAbsent` 方法 | 加新；旧 `Insert` 保留 |
| `dispatch.Run` 重复 insert 不报错 | 行为变更：以前撞主键 error；现 idempotent。poller 端：之前重投会得到 error 触发重试，现在 silent OK，不影响 ack 决策（poller 看 task status）|
| `orchestrator.Run` 重复 task ID 走 resume | 行为变更：同上。增益是真崩溃恢复 |
| `fanout.go` goroutine 包 protectedGo | 内部 helper，无 API 变 |
| `Scheduler.Append` 拒未知 dep | 严格化：之前"侥幸"通过的 replan（带未知 dep）现在显式 fail，比 60s 后 `scheduler stuck` 好 |
| replan 顺序：先 supersede 后 Append | 修复路径，行为更正 |
| 新 observer event `EventMasterTaskResumed` | 加 const；消费方忽略未知事件无破坏 |

## 不变项 / 反目标

- 不抽 `RunScheduler` 把 fanout 与 driver_runner 调度循环统一（§2 债，单独 PR）。
- 不引入 server-side lease / heartbeat（agentserver 侧改，超范围）。
- 不动 observer 端协议（只加客户端 event 常量）。
- 不动 SDK 接口。
- 不引入新表（sub_tasks 已经在写，问题只是没有人读）。
- 不重 plan resumed 任务（只续跑未完节点）。
