# Driver 协议契约修复 (审查报告 §1.1) — 设计文档

- **日期**：2026-06-13
- **来源**：`docs/review-2026-06-13.md` §1.1（CRITICAL #1–#4）
- **范围**：driver 包内 4 个协议契约破裂点的最小修复
- **不在范围**：SDK `CancelTask`、observer relay retry/backoff 策略、`tools.go` god file 拆分、`files_handler` write 路径、其它子系统

## 目标不变量

修复后下列两条必须始终成立：

1. **DelegateTask 成功 ⇒ Claude 一定能拿到 `task_id`**。任何辅助步骤失败一律降级为 `warnings`，不让 Claude 误以为派发失败而重发或放弃（slave 已经在跑）。
2. **driver 关停（ctx cancel）⇒ `wait_task` 长轮询能在合理时间内退出**，不再依赖 stdin EOF；stdout broken pipe 不再静默吞掉。

并附带：
- observer relay 错误链路不再 silently 丢；单 upload 失败不中断整批。
- `wait_task` / `get_task` 不再接受空 `task_id`，registry 调用使用与 SDK / observer 一致的 `task_id` 来源。

## 变更摘要

### 1. `internal/driver/tools.go:521-529` — submit_task 半成功降级

**问题**：`DelegateTask`（行 492）已成功，slave 在跑；后续 `UpdateWriteTask`（行 525）或 `recordDelegatedTask`（行 501）任一失败都 `return err`，Claude 拿到"派发失败"会重发或放弃，**slave 端其实已经在跑**。

**修复**：辅助步骤失败收集到 `warnings []string`，response 增加 `warnings`（omitempty）字段。每条 warning 同时写 stderr + audit。

伪代码：

```go
var warnings []string
for _, tok := range writeTokens {
    s.t.reg.RebindWriteTokenTaskID(tok, resp.TaskID)
}
for _, writeID := range observerWriteIDs {
    if err := s.t.observerRelay().UpdateWriteTask(ctx, writeID, resp.TaskID); err != nil {
        warnings = append(warnings, fmt.Sprintf("observer update write %s: %v", writeID, err))
        s.t.logRelayErr("update_write_task", err)
    }
}
s.t.reg.TrackTask(resp.TaskID, writeTokens)
```

`recordDelegatedTask`（行 501）失败同样降级为 warning（driver 本地 journal，丢一条不该让 Claude 重发整个任务）。

### 2. `internal/driver/observer_relay.go:268, 303-320` — relay 错误可见

**问题**：
- `ServePendingLoop`（303）整段 `_ = ServePendingOnce(...)`，observer 401 / 网络 / JSON 错全静默。
- `ServePendingOnce`（255-269）单个 upload 失败就 `return err`，后面的 pending 永远不被处理。

**修复**：
- `ServePendingOnce` 把循环里的 `return err` 改成 `errs = append(errs, err); continue`，循环结束后 `errors.Join` 返回。
- `ServePendingLoop` 调用失败时 `fmt.Fprintf(os.Stderr, ...)` + 若 audit 非空也写 audit。
- 新增 helper `Tools.logRelayErr(op string, err error)`，集中 stderr + audit（同时给 §1 用）。

不引入 retry/backoff —— 那是 P0 后续单独修。

### 3. `internal/driver/mcp_server.go` — ctx 串起 + writeLine 错误

**问题**：
- `Serve`（69-91）用 `context.Background()` 派发所有 tool call，driver 关停时 `wait_task` 长轮询不会被 cancel。
- `writeLine`（191-203）直接 `w.Write` 丢 error，broken pipe 时 JSON-RPC 帧损坏不可见。

**修复**：

```go
// 改签名
func (s *MCPServer) Serve(ctx context.Context, r io.Reader, w io.Writer) error
// dispatch 用传入的 ctx
s.dispatch(ctx, w, &req, &wg)

// MCPServer 增加 broken int32 字段
// writeLine：
if _, err := w.Write(b); err != nil {
    fmt.Fprintf(os.Stderr, "driver: mcp write: %v\n", err)
    if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) {
        atomic.StoreInt32(&s.broken, 1)
    }
    return
}
// 同样处理换行写入

// Serve 主循环每轮 scan 前检查
if atomic.LoadInt32(&s.broken) == 1 {
    return errors.New("mcp stdout broken")
}
```

`cmd/driver-agent/main.go:206` 改成 `mcpSrv.Serve(ctx, os.Stdin, os.Stdout)`，把已有的 driver 根 ctx 传进去。

`wait_task` 行 752-756 的 `select { case <-ctx.Done(): ... }` 已经存在，ctx 串起后自然生效，不需要额外改 wait_task。

### 4. `internal/driver/tools.go:653-758` — wait_task task_id 统一

**问题**：`SyncWrites(ctx, taskID, ...)` 用对了 `taskID`（info.TaskID 优先），但下面 `WrittenFiles(args.TaskID)` 和 `ForgetTask(args.TaskID)` 用了原始 `args.TaskID`。在 server 给出别名（info.TaskID != args.TaskID）时丢关联。空字符串还能 `WrittenFiles("")` + `ForgetTask("")` 误删一整桶。

**修复**：

```go
if strings.TrimSpace(args.TaskID) == "" {
    return nil, &MCPToolError{Message: "task_id is required"}
}
// ...
// 所有 reg 调用统一用 args.TaskID（reg 的 key 就是 submit_task 时的 resp.TaskID
// = 用户后续 wait_task 时传进来的 args.TaskID；info.TaskID 只是 server 回声用于 emit）
written := w.t.reg.WrittenFiles(args.TaskID)
w.t.reg.ForgetTask(args.TaskID)
// SyncWrites 也用 args.TaskID（observer write 也是按 resp.TaskID 绑定）
```

`get_task`（行 554）入口加同样的空校验。

**注意**：reviewer 提到的"server 给别名时丢关联"，实际语义是 `args.TaskID` 才是 driver 自己存的 key，`info.TaskID` 仅用于 emit 显示。修复方向是 **`reg` 调用统一用 `args.TaskID`**，而不是改成 `taskID`（与原 review 修复方向措辞不同，但语义等价且更安全）。`SyncWrites` 也同时改用 `args.TaskID` 以保持一致。

## 测试策略

每个 bug 一个测试，按 TDD 先写红、再实现、看绿：

| # | 测试名 | 断言 |
|---|---|---|
| 1 | `TestSubmitTask_DegradesObserverUpdateFailureToWarning` | 用 stub relay 让 `UpdateWriteTask` 返回 err；断言 `task_id` 仍返回、`warnings` 含一条、`reg.TrackTask` 仍被调 |
| 2 | `TestServePendingOnce_ContinuesPastSingleFailure` | stub 3 个 pending，第 2 个 upload fail；断言第 1、3 都 upload，返回 joined err |
| 3 | `TestMCPServerServe_StopsOnContextCancel` | 启 Serve + 一个永远阻塞的 tool call，cancel ctx；断言 Serve 在 ≤1s 内返回 |
| 4 | `TestMCPServerWriteLine_EPIPETriggersStop` | 用 closed pipe 当 writer，发 init；断言 Serve 收 EPIPE 后退出 |
| 5 | `TestWaitTask_RejectsEmptyTaskID` / `TestGetTask_RejectsEmptyTaskID` | 传 `{"task_id":""}`，断言 MCPToolError |
| 6 | `TestWaitTask_UsesArgsTaskIDForRegistry` | stub SDK 让 `info.TaskID != args.TaskID`；断言 reg 调用用 `args.TaskID`，`emit` 用 `info.TaskID` |

回归：`go test ./internal/driver/... ./cmd/driver-agent/...` + `go build ./...`。

## 兼容性

| 变更 | 影响 |
|---|---|
| `MCPServer.Serve` 签名加 ctx | 仓库内 main.go + 6 处测试，一次性更新 |
| submit_task response 多 `warnings` 字段（omitempty） | 老 Claude 客户端忽略未知字段，无破坏 |
| `wait_task` / `get_task` 拒空 task_id | 此前传空会"silently 误删"，没有合理调用方依赖空值 |
| `ServePendingOnce` 返回 joined err | 调用方只有 `ServePendingLoop`，本身就在 log，不做差别处理 |

## Helper-failure 可见性契约（两条平行路径）

post-DelegateTask helper failures（`recordDelegatedTask`、observer relay 的辅助步骤）的"降级面向谁可见"取决于响应 shape：

1. **响应里有 `warnings []string` 字段的工具**（`submit_task`、`submit_contract_task`）—
   helper 失败 → `warnings = append(...)` **并且** `logHelperErr(category, op, err)`。
   Claude（调用方）能直接读到 `warnings`；运维同时拿到 stderr + audit。**两路都走。**
2. **响应里没有 `warnings` 字段的工具**（`register_slave_mcp`、`unregister_slave_mcp`、
   `resume_task`、`read_slave_file`、`write_slave_file`、`stat_slave_file`、
   `run_slave_bash` / shell 系、`delegatePermissionTask`，多数 wait=true 后用
   `waitDelegatedTask` 包装的工具）—
   helper 失败 → **只**走 `logHelperErr(category, op, err)`，**不**改响应 shape。
   Claude 只看到正常 task 结果；运维通过 stderr + audit 看到失败。**只走运维路径。**

不强行给所有响应加 `warnings` 字段，因为 (a) `waitDelegatedTask` 包装的 TaskInfo
shape 是协议级 contract，加字段是破坏性变更；(b) 这些工具的 helper 失败
（journal append 失败）通常是 driver 本地磁盘问题，Claude 端无任何可操作的补救
动作 —— 让它知道也无法决策不同的下一步。如果未来发现 Claude 实际需要这个信号，
单独立项扩展 wait 返回 shape。

`logHelperErr(category, op, err)` 的 category 必须如实分类，避免 PR #10 P2
讨论里指出的"observer relay 误分类 journal 失败"：
- `category="observer_relay"` 用于 observer relay 路径（`update_write_task`、
  `serve_pending`、`save_task_contract` 等）→ audit event `observer_relay_error`
- `category="driver_journal"` 用于 driver 本地 `taskJournal.Append` 失败（op
  `record_delegated_task`）→ audit event `driver_journal_error`

## 不变项 / 反目标

- 不引入 SDK `CancelTask`。
- 不动 retry/backoff（属 P0 后续 token 工作）。
- 不重构 `tools.go` god file。
- 不修 `files_handler` write 路径。
- 不改 observer 端协议。
- 不给 wait=true 工具的响应加 `warnings` 字段（见 "Helper-failure 可见性契约"）。
