# Orchestrator §1.2 修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 `internal/orchestrator/` 和 `internal/dispatch/` 里 4 个让"driver/slave 重启 = 任务丢失或被双跑"、"任何 goroutine panic = driver 崩溃"、"mcp replan 偶发挂死"不成立的协议契约破裂点。

**Architecture:** 复用现有 `sub_tasks` 表（已经实时写）做 DAG checkpoint，driver 重启时从中 rebuild Scheduler 续跑；slave 端 `INSERT OR IGNORE` + `replayExistingTask` 路径杀掉重投双跑；fanout goroutine 包 `protectedGo` helper 捕获 panic 转 doneCh；replan 顺序改 `MarkSuperseded → Append` + `Append` 拒未知 dep。

**Tech Stack:** Go stdlib + sqlite (existing). 无新依赖。

**Spec:** `docs/superpowers/specs/2026-06-13-orchestrator-1.2-design.md`

**Worktree:** `/root/multi-agent/.claude/worktrees/fix-orchestrator-1.2/multi-agent/`，分支 `worktree-fix-orchestrator-1.2`，baseline `go test ./internal/orchestrator/... ./internal/orchestration/... ./internal/dispatch/...` 通过。

---

## 文件结构

- 修改：`internal/store/store.go` — 加 `InsertIfAbsent`
- 修改：`internal/store/store_test.go` — 加测试
- 修改：`internal/dispatch/dispatch.go` — 用 InsertIfAbsent + replayExistingTask
- 修改：`internal/dispatch/dispatch_test.go` — 加 idempotency 测试
- 修改：`internal/orchestrator/orchestrator.go` — 用 InsertIfAbsent + resumeOrReplay
- 修改：`internal/orchestrator/fanout.go` — protectedGo helper + 6 处 goroutine + replan 顺序 + 可选 rebuildSchedulerFromRows + runFanout 接受 resume rows
- 修改：`internal/orchestrator/fanout_test.go` — 加 panic / replan-order / resume 测试
- 修改：`internal/orchestration/dag.go` — `Append` 拒未知 dep
- 修改：`internal/orchestration/dag_test.go` — 加测试
- 修改：`internal/observer/event.go` — 加 `EventMasterTaskResumed` 常量

---

## Task 1: `store.InsertIfAbsent` (基础设施)

**Files:**
- Modify: `internal/store/store.go` — 在 `Insert` 下方加新方法
- Test: `internal/store/store_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/store/store_test.go` 找到任何现有 Insert 测试附近，追加：

```go
func TestStore_InsertIfAbsent_FirstInsertReturnsTrue(t *testing.T) {
    s := newTestStore(t)
    inserted, err := s.InsertIfAbsent(Task{ID: "t-1", Skill: "chat", Prompt: "hi"})
    if err != nil {
        t.Fatalf("InsertIfAbsent: %v", err)
    }
    if !inserted {
        t.Fatalf("expected inserted=true for fresh row")
    }
}

func TestStore_InsertIfAbsent_DuplicateReturnsFalse(t *testing.T) {
    s := newTestStore(t)
    if _, err := s.InsertIfAbsent(Task{ID: "t-1", Skill: "chat", Prompt: "hi"}); err != nil {
        t.Fatalf("first: %v", err)
    }
    inserted, err := s.InsertIfAbsent(Task{ID: "t-1", Skill: "chat", Prompt: "hi-different"})
    if err != nil {
        t.Fatalf("second: %v", err)
    }
    if inserted {
        t.Fatalf("expected inserted=false for duplicate id")
    }
    // The original prompt must be preserved (INSERT OR IGNORE).
    row, _, err := s.GetTaskWithChunks("t-1")
    if err != nil {
        t.Fatalf("get: %v", err)
    }
    if row.Prompt != "hi" {
        t.Fatalf("prompt overwritten on duplicate: %q", row.Prompt)
    }
}
```

`newTestStore` 应已存在；如果叫别名 (`newStoreForTest` 等)，按现有惯例。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/store/ -run TestStore_InsertIfAbsent -v -count=1`
Expected: FAIL（`InsertIfAbsent` undefined）

- [ ] **Step 3: 实现 InsertIfAbsent**

在 `internal/store/store.go` `func (s *Store) Insert(t Task) error {` **下方**，新加：

```go
// InsertIfAbsent inserts a task row if no row with this ID exists, returning
// (true, nil). On primary-key conflict returns (false, nil) — the caller can
// then look up the existing row to decide whether to replay (completed) or
// silently skip (running/assigned). Used by master + slave dispatch entrypoints
// to make task delivery idempotent: a re-delivered task ID never spawns a
// second executor.
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

- [ ] **Step 4: 跑测试看绿**

Run: `go test ./internal/store/ -run TestStore_InsertIfAbsent -v -count=1`
Expected: 两个 PASS。

- [ ] **Step 5: 全包回归**

Run: `go test ./internal/store/... -race -count=1`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): add InsertIfAbsent for idempotent task delivery

INSERT OR IGNORE-based variant of Insert returning (inserted bool, err).
Caller can detect duplicate task IDs (poller re-delivery, driver restart)
and replay/resume instead of spawning a second executor or losing state.

Spec: docs/superpowers/specs/2026-06-13-orchestrator-1.2-design.md

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `dispatch.Run` 使用 InsertIfAbsent + replayExistingTask（Bug #6）

**Files:**
- Modify: `internal/dispatch/dispatch.go`
- Test: `internal/dispatch/dispatch_test.go`

- [ ] **Step 1: 写失败测试**

`internal/dispatch/dispatch_test.go` 末尾追加（按现有 testStore / spy executor 风格，如发现现有 helpers 名字不同请按现有命名）：

```go
// TestDispatch_DuplicateInsertSkipsExecutor verifies that when the same task
// is delivered twice (poller ack lost → re-delivery), only ONE executor.Run
// is invoked. Fixes §1.2 #6 of docs/review-2026-06-13.md.
func TestDispatch_DuplicateInsertSkipsExecutor(t *testing.T) {
    s := newTestStore(t)
    var runs int32
    exec := executorFunc(func(ctx context.Context, t executor.Task, sink store.Sink) (executor.Result, error) {
        atomic.AddInt32(&runs, 1)
        return executor.Result{Summary: "ok"}, nil
    })
    d := New(map[string]executor.Executor{"chat": exec}, &noopJournal{}, s, nil)

    task := executor.Task{ID: "task-dup", Skill: "chat", Prompt: "hello"}

    res1, err1 := d.Run(context.Background(), task)
    if err1 != nil {
        t.Fatalf("first Run: %v", err1)
    }
    if res1.Summary != "ok" {
        t.Fatalf("first res: %+v", res1)
    }

    res2, err2 := d.Run(context.Background(), task)
    if err2 != nil {
        t.Fatalf("second Run (re-delivery) must not error: %v", err2)
    }

    if got := atomic.LoadInt32(&runs); got != 1 {
        t.Fatalf("executor.Run was invoked %d times; expected exactly 1 (re-delivery must not spawn second executor)", got)
    }

    // For chat skill, the stored row holds a JSON wrapper; replay must
    // surface the same Summary so the caller still sees the result.
    if res2.Summary == "" {
        t.Fatalf("second Run must replay stored output; got empty summary")
    }
}
```

如 `executorFunc`/`noopJournal` 不在测试包，则在该文件内 inline 定义（最小实现）；如已有不同 helpers，按其命名。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/dispatch/ -run TestDispatch_DuplicateInsertSkipsExecutor -v -count=1`
Expected: FAIL（第二次 Run 会撞主键报错）

- [ ] **Step 3: 改 `dispatch.Run` + 加 `replayExistingTask`**

`internal/dispatch/dispatch.go` 把 `func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error)` 顶部段落改成：

```go
func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
    summary := observer.SummarizePrompt(t.Prompt, 80)
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
    d.emit(observer.Event{
        Type:    observer.EventSlaveTaskStarted,
        TaskID:  t.ID,
        Summary: summary,
        Status:  "running",
    })
    // ... rest of existing function unchanged (envelope strip + executor.Run + Complete + emit) ...
```

并在文件底部加：

```go
// replayExistingTask is called when InsertIfAbsent sees a duplicate task ID.
// The caller (poller re-delivery, driver restart) must get a sensible result
// without spawning a second executor.
//   - completed: surface the stored output (chat skill output is a JSON
//     wrapper; we forward it as-is — the driver-side wait_task already
//     unwraps it via unwrapKindMarker).
//   - running/assigned: another executor is still running; return empty result
//     with no error so the poller will simply re-poll until the original run
//     finishes and Complete is called.
//   - failed: surface the stored error.
func (d *Dispatcher) replayExistingTask(ctx context.Context, t executor.Task) (executor.Result, error) {
    row, _, err := d.store.GetTaskWithChunks(t.ID)
    if err != nil {
        return executor.Result{}, fmt.Errorf("replay task %s: %w", t.ID, err)
    }
    switch row.Status {
    case "completed":
        return executor.Result{Summary: row.Output}, nil
    case "failed":
        if row.Error == "" {
            return executor.Result{}, fmt.Errorf("task %s previously failed", t.ID)
        }
        return executor.Result{}, fmt.Errorf("%s", row.Error)
    default: // assigned, running
        return executor.Result{}, nil
    }
}
```

- [ ] **Step 4: 跑新测试看绿 + dispatch 包回归**

Run: `go test ./internal/dispatch/... -race -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/dispatch/dispatch.go internal/dispatch/dispatch_test.go
git commit -m "fix(dispatch): idempotent task delivery; replay duplicates instead of double-run

InsertIfAbsent + replayExistingTask kills the slave-side double-executor
bug: poller ack loss used to redeliver the same task ID, which racing
INSERT INTO tasks would error on — but only AFTER the first run had
already started a second claude subprocess for chat skills. This is the
other root cause of MEMORY jetson_outage_modes mode B (one-second EOF
flap), beyond the acquireInstanceLock fix.

Replay semantics:
- completed → surface stored output (chat wrapper passes through; driver
  side already unwraps via unwrapKindMarker)
- failed    → surface stored error
- running   → return empty Result, no error (poller will re-poll)

Fixes §1.2 #6 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `Scheduler.Append` 拒未知 dep（Bug #8 part 1）

**Files:**
- Modify: `internal/orchestration/dag.go:235`
- Test: `internal/orchestration/dag_test.go`

- [ ] **Step 1: 写失败测试**

`internal/orchestration/dag_test.go` 末尾追加：

```go
// TestScheduler_Append_RejectsUnknownDep prevents the silent "node never
// becomes ready" failure that produces a 60s scheduler-stuck false positive.
// Fixes §1.2 #8 (part 1) of docs/review-2026-06-13.md.
func TestScheduler_Append_RejectsUnknownDep(t *testing.T) {
    s := NewScheduler([]planner.Node{{ID: "a"}}, 1)
    err := s.Append([]planner.Node{{ID: "b", DependsOn: []string{"ghost"}}})
    if err == nil {
        t.Fatal("expected error for unknown dep")
    }
    if !strings.Contains(err.Error(), "ghost") {
        t.Fatalf("error should reference unknown dep: %v", err)
    }
}

func TestScheduler_Append_AllowsCrossAppendDep(t *testing.T) {
    s := NewScheduler([]planner.Node{{ID: "a"}}, 1)
    // x depends on y, both inside the same Append batch — must NOT error.
    err := s.Append([]planner.Node{
        {ID: "y"},
        {ID: "x", DependsOn: []string{"y"}},
    })
    if err != nil {
        t.Fatalf("cross-append dep should be allowed: %v", err)
    }
}

func TestScheduler_Append_AllowsDepOnExisting(t *testing.T) {
    s := NewScheduler([]planner.Node{{ID: "a"}}, 1)
    err := s.Append([]planner.Node{{ID: "b", DependsOn: []string{"a"}}})
    if err != nil {
        t.Fatalf("dep on existing scheduler node should be allowed: %v", err)
    }
}
```

如果 `strings` 没 import，加上。`planner.Node` 已在该文件用到。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/orchestration/ -run "TestScheduler_Append_" -v -count=1`
Expected: 第 1 个 FAIL（无 dep 校验），2/3 通过（已支持）

- [ ] **Step 3: 改 `Scheduler.Append`**

`internal/orchestration/dag.go` 把 `func (s *Scheduler) Append(nodes []planner.Node) error {` 整体替换为：

```go
func (s *Scheduler) Append(nodes []planner.Node) error {
    // Phase 1: dup check + collect new ids
    appended := make(map[string]bool, len(nodes))
    for _, n := range nodes {
        if _, exists := s.nodeByID[n.ID]; exists {
            return fmt.Errorf("Scheduler.Append: duplicate id %q", n.ID)
        }
        appended[n.ID] = true
    }
    // Phase 2: every depends_on must be either an existing scheduler node
    // (incl. completed/skipped) or another node in this same Append batch.
    // Unknown deps used to silently keep the node out of pending forever,
    // surfacing as a 60s scheduler-stuck error with no actionable signal.
    for _, n := range nodes {
        for _, dep := range n.DependsOn {
            if appended[dep] {
                continue
            }
            if _, known := s.nodeByID[dep]; !known {
                return fmt.Errorf("Scheduler.Append: node %q depends on unknown %q", n.ID, dep)
            }
        }
    }
    // Phase 3: commit
    for _, n := range nodes {
        s.nodes = append(s.nodes, n)
        s.nodeByID[n.ID] = n
        for _, d := range n.DependsOn {
            s.rev[d] = append(s.rev[d], n.ID)
        }
        ready := true
        for _, dep := range n.DependsOn {
            f, ok := s.finished[dep]
            if !ok || f.Status != "completed" {
                ready = false
                break
            }
        }
        if ready {
            s.pending[n.ID] = true
        }
    }
    return nil
}
```

- [ ] **Step 4: 跑测试看绿**

Run: `go test ./internal/orchestration/ -run "TestScheduler_Append_" -v -count=1`
Expected: 三个 PASS

- [ ] **Step 5: 包回归**

Run: `go test ./internal/orchestration/... -race -count=1`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/orchestration/dag.go internal/orchestration/dag_test.go
git commit -m "fix(orchestration): Scheduler.Append rejects unknown depends_on

Previously, appending a node with an unknown depends_on silently kept it
out of pending forever (since the dep would never reach 'completed'
status), surfacing 60s later as 'scheduler stuck: no progress in 60s'
with no actionable signal. Now Append returns a clear error so the
caller (replan paths) sees the bug immediately.

Cross-batch deps are still allowed: a node in the same Append batch can
depend on another node in that batch.

Fixes §1.2 #8 (part 1) of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: fanout replan 顺序（Bug #8 part 2）

**Files:**
- Modify: `internal/orchestrator/fanout.go` — replan 路径 (around lines 578–591) — 顺序改 `MarkSuperseded → Append`；mcp_tool_set replan 同改
- Test: `internal/orchestrator/fanout_test.go`

- [ ] **Step 1: 找两处 replan**

Run: `grep -n "sched.Append(newPlan)\|MarkSuperseded(n.ID" internal/orchestrator/fanout.go`
Expected: 看到 2 个 `Append(newPlan)` 调用（mcp validation 与 mcp_tool_set），各自附近一个 `MarkSuperseded`。两处都要按同样顺序改。

- [ ] **Step 2: 写失败测试**

`internal/orchestrator/fanout_test.go` 末尾追加：

```go
// TestFanout_ReplanSupersedeBeforeAppend pins the order: MarkSuperseded must
// run BEFORE Append for the new plan. Otherwise a new node that depends on
// the original (renamed) node would never become ready (dep still in
// inFlight / not yet skipped), surfacing 60s later as scheduler-stuck.
// Fixes §1.2 #8 (part 2) of docs/review-2026-06-13.md.
func TestFanout_ReplanSupersedeBeforeAppend(t *testing.T) {
    // This is a regression test that triggers an mcp validation replan
    // (forces fanout into the replan branch) and asserts the original node
    // is in 'skipped' state by the time Append runs. Concrete setup mirrors
    // the existing TestFanout_MCPValidationReplan in this file — extend that
    // pattern, then add a spy-Append-callback assertion. If no such pattern
    // exists yet, add the simplest possible: fake SDK returns a node whose
    // first validation fails; planner mock returns a replan whose new node
    // depends on the (original) node id; assert the replan succeeds (= no
    // 'scheduler stuck' error after up to 5s).

    // Pseudocode (adapt to existing fanout test infra in this file):
    //   sdk := &fakeSDK{ ... }
    //   plan := []planner.Node{{ID: "n1", Skill: "mcp", TargetID: "slave-a", ...}}
    //   replan := []planner.Node{{ID: "n1.v2", Skill: "mcp", TargetID: "slave-a",
    //                              DependsOn: []string{}}}  // must NOT dep n1
    //   ...
    //   res, err := o.runFanout(ctx, parentTask)
    //   require.NoError(t, err, "replan must not deadlock the scheduler")
    //   require.Contains(t, res.Summary, "n1.v2 output")
    t.Skip("TODO: adapt to existing fanout_test.go scaffolding (see comment above); subagent will fill in real assertions")
}
```

**Note for implementer:** the existing `internal/orchestrator/fanout_test.go` has a richer infra for spinning up a real `runFanout` with fake SDK/planner. The skip is a placeholder — the implementer **must** unskip and adapt the test to the existing infra. Acceptance: with the OLD order (Append before MarkSuperseded) the test fails; with the NEW order it passes.

- [ ] **Step 3: 跑测试看现状**

Run: `go test ./internal/orchestrator/ -run TestFanout_ReplanSupersedeBeforeAppend -v -count=1`
Expected: SKIP (placeholder)

- [ ] **Step 4: 改 fanout.go 两处 replan 顺序**

`internal/orchestrator/fanout.go` 找到 `if err := sched.Append(newPlan); err != nil {` (mcp validation 分支，约 579 行)，把整块：

```go
if err := sched.Append(newPlan); err != nil {
    cancelAll()
    return executor.Result{}, fmt.Errorf("append mcp validation replan: %w", err)
}
allNodes = append(allNodes, newPlan...)
for _, appended := range newPlan {
    optionalByID[appended.ID] = appended.Optional
}
appendSubTaskRows(o.store, t.ID, newPlan)
for _, skipped := range sched.MarkSuperseded(n.ID, "superseded by mcp validation replan") {
    optionalByID[skipped.NodeID] = true
    recordSkippedDone(skipped)
}
```

改成：

```go
// Supersede the old node FIRST so the new plan can depend on (or coexist
// with) it; otherwise Append's depends_on check would either reject the
// new node (if old id is referenced) or queue it forever (since old dep
// would not be 'completed'). See §1.2 #8 of the 2026-06-13 review.
for _, skipped := range sched.MarkSuperseded(n.ID, "superseded by mcp validation replan") {
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

第二处 mcp_tool_set replan（grep 上面会显示，找 `sched.Append(newPlan)` 第二处）同样改。

- [ ] **Step 5: 实施者把 placeholder 测试 fill in**

实施者参考 `internal/orchestrator/fanout_test.go` 现有 `TestFanout_*` 用例的 fake SDK / planner / store 模式，把 placeholder 测试改成真测试：
- fake planner 第一次返回 plan = `[{ID:"n1",Skill:"mcp",...}]`
- 让 `validateMCPNode(n1, ...)` 失败一次 → fanout 进 replan 分支
- fake planner 第二次返回 replan = `[{ID:"n1.v2",Skill:"chat",TargetID:"slave-a"}]`（无 dep n1）
- 跑 fanout：替换前死锁/60s 报 stuck；替换后正常完成

Run: `go test ./internal/orchestrator/ -run TestFanout_ReplanSupersedeBeforeAppend -v -count=1 -timeout 60s`
Expected: PASS（≤30s 内完成）

- [ ] **Step 6: 包回归**

Run: `go test ./internal/orchestrator/... -race -count=1 -timeout 120s`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go
git commit -m "fix(orchestrator): replan does MarkSuperseded before Append

Old order (Append → MarkSuperseded) meant that if the new plan's node
referenced or depended on the original node, the original node was still
in inFlight when Append ran, so the new node was added but never moved
into pending. 60s later this surfaced as 'scheduler stuck: no progress
in 60s' with no signal about WHY. Reversing the order lets Append's
new depends_on validation (Task 3) see the original node as
finished:skipped (or fail loudly if the new plan references an unknown
node — better than silent deadlock).

Both replan branches (mcp validation, mcp_tool_set) updated.

Fixes §1.2 #8 (part 2) of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: `protectedGo` helper + fanout panic 防护（Bug #7）

**Files:**
- Modify: `internal/orchestrator/fanout.go` — 加 helper + 改 6 处 `go func` 用法
- Test: `internal/orchestrator/fanout_test.go`

- [ ] **Step 1: 写失败测试**

`internal/orchestrator/fanout_test.go` 末尾追加：

```go
// TestFanout_GoroutinePanicTurnsIntoFailedNode verifies that a panic inside
// a fanout worker goroutine is recovered and reported as a failed node
// (rather than crashing the whole driver process).
// Fixes §1.2 #7 of docs/review-2026-06-13.md.
func TestFanout_GoroutinePanicTurnsIntoFailedNode(t *testing.T) {
    // SDK whose DelegateTask panics on first call.
    sdk := &fakeSDK{ /* fill in: see existing fanout_test fakeSDK pattern */ }
    // Configure plan with one node that the panicking SDK will be asked to delegate.
    //
    // Expected: o.runFanout returns NORMALLY (driver does not crash);
    // the returned result reflects the failed node; the test process
    // does not panic out.

    t.Skip("TODO: adapt to existing fanout_test.go fakeSDK; subagent will fill in real panic injection")
}
```

**Note for implementer:** same as Task 4 placeholder — adapt to existing fakeSDK infrastructure in this file. Acceptance: pre-fix (no protectedGo), running this test panics the test process / `-race` reports the unrecovered panic; post-fix the test runs cleanly with the node marked failed.

- [ ] **Step 2: 加 `protectedGo` helper**

在 `internal/orchestrator/fanout.go` 顶部、`runFanout` 函数之前或文件最末（看现有 helper 放哪），新加：

```go
// protectedGo wraps a worker goroutine: any panic is recovered and turned
// into a 'failed' doneCh send so the scheduler can move on instead of
// crashing the driver. nodeID identifies which fanout node the failure
// should be attributed to.
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

**Note**: `done` 是 `runFanout` 内部 type；如果 `protectedGo` 放函数外，要么把 `done` 提到 package level，要么 inline 在 `runFanout` 里。推荐放函数外 + 把 `done` 类型也提到 package level（其它 helper 也可能用）。

- [ ] **Step 3: 改 6 处 `go func` 使用 protectedGo**

Run: `grep -n "^\s*go func" internal/orchestrator/fanout.go`
列出 6 处。对每一处：

模板：

```go
// BEFORE
go func(n planner.Node, prompt string) {
    // body using n, prompt
    doneCh <- done{FinishedNode: ...}
}(n, prompt)

// AFTER
protectedGo(doneCh, n.ID, func() {
    n := n        // capture in closure (or pass via closure scope)
    prompt := prompt
    // same body
    doneCh <- done{FinishedNode: ...}
})
```

对 `n.ID` 不可用的两处（已经在 closure scope），就直接读外层变量。

注意：保留每个 goroutine 内原有的 doneCh send 不变。protectedGo 只处理 panic 路径。

- [ ] **Step 4: 实施者把 placeholder fill in**

实施者参考 fanout_test 现有 fakeSDK，注入 panic（例如 `delegateFunc: func(...) { panic("boom") }`），断言 `runFanout` 返回 nil error / failed node summary。

Run: `go test ./internal/orchestrator/ -run TestFanout_GoroutinePanicTurnsIntoFailedNode -v -count=1`
Expected: PASS

- [ ] **Step 5: 包回归 + race**

Run: `go test ./internal/orchestrator/... -race -count=1 -timeout 120s`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go
git commit -m "fix(orchestrator): protectedGo wraps fanout goroutines with panic recovery

Any panic in an SDK call, Render, or artifact resolver inside a worker
goroutine used to take down the whole driver process. protectedGo
recovers, converts the panic into a 'failed' FinishedNode for that
node, and lets the Scheduler continue. All 6 'go func' sites in fanout.go
now use it.

Fixes §1.2 #7 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `orchestrator.Run` idempotency + resume from sub_tasks（Bug #5）

**Files:**
- Modify: `internal/observer/event.go` — 加常量
- Modify: `internal/orchestrator/orchestrator.go` — Run 入口 + resumeOrReplay
- Modify: `internal/orchestrator/fanout.go` — runFanout 接 resume rows + rebuildSchedulerFromRows helper
- Test: `internal/orchestrator/orchestrator_test.go` (or fanout_test.go)

- [ ] **Step 1: 加 event 常量**

`internal/observer/event.go` 在 `EventMasterTaskFailed` 那块加：

```go
EventMasterTaskResumed = "master_task_resumed"
```

- [ ] **Step 2: 写失败测试**

新建 `internal/orchestrator/orchestrator_resume_test.go`（如果包内已有 orchestrator_test.go 就追加到那里）：

```go
package orchestrator

import (
    "context"
    "testing"
    // ... + planner, store, executor 等按现有测试 import
)

// TestOrchestrator_DuplicateRunCompletedReturnsOutput verifies that delivering
// the same parent task ID twice (driver restart, poller redelivery) returns
// the original stored output without re-running anything.
// Fixes §1.2 #5 of docs/review-2026-06-13.md.
func TestOrchestrator_DuplicateRunCompletedReturnsOutput(t *testing.T) {
    s := newOrchTestStore(t)
    // Pre-seed completed task in DB
    if _, err := s.InsertIfAbsent(store.Task{ID: "p-1", Skill: "fanout", Prompt: "go"}); err != nil {
        t.Fatalf("seed: %v", err)
    }
    _ = s.MarkRunning("p-1")
    _ = s.Complete("p-1", "previously-finished-summary")

    o := newOrchForTest(t, s) // helper: build Orchestrator with fakes
    res, err := o.Run(context.Background(), executor.Task{ID: "p-1", Skill: "fanout", Prompt: "go"})
    if err != nil {
        t.Fatalf("duplicate Run on completed task: %v", err)
    }
    if res.Summary != "previously-finished-summary" {
        t.Fatalf("expected stored output replay; got %q", res.Summary)
    }
}

// TestOrchestrator_ResumeFromExistingSubTasks verifies that on restart, a
// parent task with status='running' and existing sub_tasks rows is resumed:
// completed nodes are not re-dispatched, assigned nodes are awaited via
// WaitForTask, only pending nodes are newly dispatched.
func TestOrchestrator_ResumeFromExistingSubTasks(t *testing.T) {
    s := newOrchTestStore(t)
    // Seed parent in running state
    _, _ = s.InsertIfAbsent(store.Task{ID: "p-2", Skill: "fanout", Prompt: "x"})
    _ = s.MarkRunning("p-2")
    // Seed 3 sub_tasks: one completed, one assigned (child task in flight), one pending
    _ = s.InsertSubTasks("p-2", []store.SubTaskRow{
        {ParentID: "p-2", NodeID: "a", TargetID: "slave-a", Prompt: "do a", Status: "completed", Output: "a-result"},
        {ParentID: "p-2", NodeID: "b", TargetID: "slave-a", Prompt: "do b", Status: "assigned", ChildTaskID: "child-b"},
        {ParentID: "p-2", NodeID: "c", TargetID: "slave-a", Prompt: "do c", Status: "pending", DependsOn: []string{"a"}},
    })

    // Fakes: planner must NOT be called (resume path), SDK WaitForTask on
    // child-b returns completed, DelegateTask only called for node c
    var delegateCalls []string
    var waitCalls []string
    sdk := &fakeSDK{
        delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
            delegateCalls = append(delegateCalls, req.Prompt)
            return &agentsdk.DelegateTaskResponse{TaskID: "child-c"}, nil
        },
        waitFunc: func(taskID string) (*agentsdk.TaskInfo, error) {
            waitCalls = append(waitCalls, taskID)
            return &agentsdk.TaskInfo{Status: "completed", Output: "b-result-or-c-result"}, nil
        },
    }
    o := newOrchForTestWithSDK(t, s, sdk)

    _, err := o.Run(context.Background(), executor.Task{ID: "p-2", Skill: "fanout", Prompt: "x"})
    if err != nil {
        t.Fatalf("resume Run: %v", err)
    }
    // Assertion 1: completed node 'a' must NOT be re-dispatched
    for _, p := range delegateCalls {
        if p == "do a" {
            t.Fatalf("completed node 'a' was re-dispatched: %v", delegateCalls)
        }
    }
    // Assertion 2: assigned node 'b' must trigger WaitForTask on its child id
    if len(waitCalls) == 0 || waitCalls[0] != "child-b" {
        t.Fatalf("assigned node 'b' must trigger WaitForTask(child-b); waitCalls=%v", waitCalls)
    }
}
```

`newOrchTestStore`, `newOrchForTest`, `fakeSDK` 等用现有 fanout_test.go 里的同款 helpers。

- [ ] **Step 3: 跑测试看红**

Run: `go test ./internal/orchestrator/ -run "TestOrchestrator_DuplicateRunCompletedReturnsOutput|TestOrchestrator_ResumeFromExistingSubTasks" -v -count=1`
Expected: FAIL（撞主键 / 重新 plan 而非 resume）

- [ ] **Step 4: 改 orchestrator.Run**

`internal/orchestrator/orchestrator.go` 把 `func (o *Orchestrator) Run` 改成：

```go
func (o *Orchestrator) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
    summary := observer.SummarizePrompt(t.Prompt, 80)
    inserted, err := o.store.InsertIfAbsent(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt})
    if err != nil {
        return executor.Result{}, err
    }
    if !inserted {
        return o.resumeOrReplay(ctx, t, summary)
    }
    if err := o.store.MarkRunning(t.ID); err != nil {
        return executor.Result{}, err
    }
    o.emit(observer.Event{
        Type:    observer.EventMasterTaskReceived,
        TaskID:  t.ID,
        Summary: summary,
        Status:  "running",
    })
    // ... existing switch on t.Skill (route / fanout) and error/complete tails ...
}
```

加 helper 同文件底：

```go
// resumeOrReplay is called when InsertIfAbsent sees the parent task ID
// already exists (driver restart, poller redelivery). Returns the stored
// result for terminal tasks and continues a fanout DAG from sub_tasks rows
// for in-flight ones.
func (o *Orchestrator) resumeOrReplay(ctx context.Context, t executor.Task, summary string) (executor.Result, error) {
    row, _, err := o.store.GetTaskWithChunks(t.ID)
    if err != nil {
        return executor.Result{}, fmt.Errorf("resume %s: %w", t.ID, err)
    }
    switch row.Status {
    case "completed":
        return executor.Result{Summary: row.Output}, nil
    case "failed":
        if row.Error == "" {
            return executor.Result{}, fmt.Errorf("task %s previously failed", t.ID)
        }
        return executor.Result{}, fmt.Errorf("%s", row.Error)
    }
    // running / assigned: try to resume from sub_tasks
    rows, lerr := o.store.ListSubTasks(t.ID)
    if lerr != nil {
        return executor.Result{}, fmt.Errorf("list sub_tasks %s: %w", t.ID, lerr)
    }
    if len(rows) == 0 {
        // No DAG state yet — fall through to a fresh plan on the same task id
        // (the first attempt died before InsertSubTasks).
        if t.Skill == "fanout" {
            return o.runFanout(ctx, t)
        }
        if t.Skill == "route" {
            return o.runRoute(ctx, t)
        }
        return executor.Result{}, fmt.Errorf("resume %s: unknown skill %q", t.ID, t.Skill)
    }
    if t.Skill != "fanout" {
        return executor.Result{}, fmt.Errorf("resume %s: only fanout supported (skill=%s)", t.ID, t.Skill)
    }
    o.emit(observer.Event{
        Type:    observer.EventMasterTaskResumed,
        TaskID:  t.ID,
        Summary: summary,
        Status:  "running",
        Payload: observerPayload(resumeStats(rows)),
    })
    return o.runFanoutResume(ctx, t, rows)
}

func resumeStats(rows []store.SubTaskRow) map[string]int {
    out := map[string]int{}
    for _, r := range rows {
        out[r.Status]++
    }
    return out
}
```

- [ ] **Step 5: 在 fanout.go 加 `rebuildSchedulerFromRows` + `runFanoutResume`**

`internal/orchestrator/fanout.go` 加：

```go
// rebuildSchedulerFromRows reconstructs Scheduler + outputs map + in-flight
// node descriptors from persisted sub_tasks rows. Used by runFanoutResume
// after a driver restart so we don't lose DAG state.
func rebuildSchedulerFromRows(
    rows []store.SubTaskRow,
    maxConc int,
) (sched *Scheduler, plan []planner.Node, outputs map[string]string, inFlight []store.SubTaskRow) {
    plan = make([]planner.Node, 0, len(rows))
    for _, r := range rows {
        plan = append(plan, planner.Node{
            ID:        r.NodeID,
            TargetID:  r.TargetID,
            Prompt:    r.Prompt,
            DependsOn: r.DependsOn,
            // Skill / Optional / SystemContext not persisted; resume path is
            // limited to nodes whose only progress signal is child_task_id +
            // status. If the new run needs more, extend SubTaskRow.
        })
    }
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
        case "assigned", "running":
            sched.MarkDispatched(r.NodeID)
            inFlight = append(inFlight, r)
        case "pending", "":
            // already in pending
        }
    }
    return
}

// runFanoutResume continues a fanout DAG using state rebuilt from sub_tasks.
// In-flight nodes (status='assigned' with child_task_id) are awaited via
// WaitForTask without re-dispatch; pending nodes are dispatched normally.
func (o *Orchestrator) runFanoutResume(ctx context.Context, t executor.Task, rows []store.SubTaskRow) (executor.Result, error) {
    maxConc := effectiveFanoutConcurrency(o.cfg.MaxConcurrency, contract.ExecutionPolicy{}, false)
    sched, plan, outputs, inFlight := rebuildSchedulerFromRows(rows, maxConc)
    // TODO(implementer): integrate with runFanout's existing main loop. The
    // cleanest approach is to refactor runFanout's body into a helper that
    // takes (sched, plan, outputs, alreadyInFlightRows) and have both the
    // fresh path and the resume path call it. For now: implement a minimal
    // resume that spawns WaitForTask goroutines for each `inFlight` row,
    // then drives the same main loop as runFanout.
    _ = plan
    _ = outputs
    _ = sched
    _ = inFlight
    return executor.Result{}, fmt.Errorf("runFanoutResume not yet implemented; rows=%d", len(rows))
}
```

**Important note for implementer:** the full integration of `runFanoutResume` with `runFanout`'s ~400-line main loop is significant. Two options:
1. **(Preferred)** Extract runFanout's main loop into `runFanoutLoop(ctx, t, plan, sched, outputs, initialInFlight)` and have both fresh + resume paths call it. This is real refactoring; budget extra time.
2. **(Minimum viable)** Implement resume by issuing `WaitForTask` synchronously for each in-flight row to collect their final state, then call `runFanout` with the surviving (still-pending) plan. Loses parallelism during resume; trivial to implement.

If option 1 is too large for this task's scope, **switch to option 2 and explicitly note the limitation in the commit message**. The integration test `TestOrchestrator_ResumeFromExistingSubTasks` will pass either way.

- [ ] **Step 6: 跑测试看绿**

Run: `go test ./internal/orchestrator/ -run "TestOrchestrator_DuplicateRunCompletedReturnsOutput|TestOrchestrator_ResumeFromExistingSubTasks" -v -count=1 -timeout 60s`
Expected: PASS

- [ ] **Step 7: 包回归 + race**

Run: `go test ./internal/orchestrator/... ./internal/orchestration/... ./internal/store/... ./internal/dispatch/... -race -count=1 -timeout 300s`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/observer/event.go internal/orchestrator/orchestrator.go internal/orchestrator/fanout.go internal/orchestrator/orchestrator_resume_test.go
git commit -m "fix(orchestrator): resume fanout from sub_tasks on restart

orchestrator.Run is now idempotent against duplicate task IDs (driver
restart, poller redelivery). When the parent task already exists:
- completed → replay stored output
- failed    → replay stored error
- running/assigned with sub_tasks rows → rebuild Scheduler from rows,
  await any in-flight nodes via WaitForTask without re-dispatch, and
  continue dispatching pending nodes
- running/assigned with no rows → fall through to a fresh plan (the
  first attempt died before InsertSubTasks)

Emits new EventMasterTaskResumed with a {pending,assigned,completed,...}
count payload so observer-side replay can distinguish first-run from
resume.

Fixes §1.2 #5 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: 最终全量回归 + 跨包 build + e2e 准备

- [ ] **Step 1: 全模块构建**

Run: `go build ./...`
Expected: clean

- [ ] **Step 2: 全模块测试 + race**

Run: `go test ./... -race -count=1 -timeout 600s`
Expected: PASS

- [ ] **Step 3: 对照 spec 覆盖**

| Spec § | Plan task | Commit |
|---|---|---|
| §1.2 #5 resume | Task 6 | (sha) |
| §1.2 #6 dispatch dedup | Task 2 | (sha) |
| §1.2 #7 panic recover | Task 5 | (sha) |
| §1.2 #8 replan order | Task 4 (+3) | (sha) |

确认无遗漏。

- [ ] **Step 4: `git log --oneline master..HEAD`**

Expected: 1 docs (spec) + 6 commits (Task 1-6) = 7 commits。

---

## 验证清单（implementation 完成后由 verification-before-completion 复核）

- [ ] `go build ./...` clean
- [ ] `go test ./... -race -count=1` 全绿
- [ ] 新增测试全部 PASS：
  - `TestStore_InsertIfAbsent_*` (2)
  - `TestDispatch_DuplicateInsert*` (≥1)
  - `TestScheduler_Append_*` (3)
  - `TestFanout_ReplanSupersedeBeforeAppend`
  - `TestFanout_GoroutinePanicTurnsIntoFailedNode`
  - `TestOrchestrator_DuplicateRunCompletedReturnsOutput`
  - `TestOrchestrator_ResumeFromExistingSubTasks`
- [ ] Spec 的 4 条 §1.2 CRITICAL bug 全部映射到 plan task
- [ ] e2e（本机灰度）：按 `[[e2e_prod_test_codex_local]]` runbook 跑通基础 4 步 + 一个 resume scenario（kill driver 后重启，验证不重派完成节点）
