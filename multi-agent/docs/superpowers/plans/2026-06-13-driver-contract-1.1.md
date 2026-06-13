# Driver 协议契约修复 (审查报告 §1.1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 `internal/driver/` 里 4 个让"DelegateTask 成功 ⇒ Claude 拿到 task_id"和"driver 关停 ⇒ 长轮询能退出"不成立的协议契约破裂点。

**Architecture:** 半成功降级（warnings 数组而非 error）+ ctx 串起 MCPServer.Serve + writeLine 错误可见 + registry key 统一。改动局限在 `internal/driver/` 与一个 `cmd/driver-agent/main.go` 调用点。

**Tech Stack:** Go 1.x、内置 `context` / `errors` / `sync/atomic`、`testify/require`、`httptest` stub。无新依赖。

**Spec:** `docs/superpowers/specs/2026-06-13-driver-contract-1.1-design.md`

**Worktree:** `/root/multi-agent/.claude/worktrees/fix-driver-contract-1.1/multi-agent/`，分支 `worktree-fix-driver-contract-1.1`，baseline `go test ./internal/driver/...` 通过。

---

## 文件结构

- 修改：`internal/driver/tools.go` — submit_task 半成功降级；wait_task / get_task task_id 守卫；新增 `logRelayErr` helper
- 修改：`internal/driver/observer_relay.go` — `ServePendingOnce` 不再单失败中断；`ServePendingLoop` 可见错误
- 修改：`internal/driver/mcp_server.go` — `Serve(ctx, r, w)` 签名；`writeLine` 错误检测；broken pipe 退出
- 修改：`cmd/driver-agent/main.go:206` — 传 ctx 进 Serve
- 新增测试（同包）：
  - `internal/driver/mcp_server_test.go` 追加 ctx-cancel 与 broken-pipe 用例
  - `internal/driver/tools_test.go` 追加 submit_task warning、wait_task / get_task 守卫、reg key 别名用例
  - `internal/driver/observer_relay_test.go`（**新文件**）覆盖 ServePendingOnce 多失败聚合

---

## Task 1: 新 helper `logRelayErr`（基础设施，后续任务依赖）

**Files:**
- Modify: `internal/driver/tools.go` — 在 `func (t *Tools) emit` 附近（约 92 行）下方加新方法

- [ ] **Step 1: 写失败测试**

新建 `internal/driver/tools_log_relay_err_test.go`：

```go
package driver

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr swaps os.Stderr for a pipe; returns the captured bytes after
// the closure runs and restores stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}

func TestLogRelayErr_WritesToStderrAndAudit(t *testing.T) {
	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()
	tools := &Tools{audit: audit}

	stderr := captureStderr(t, func() {
		tools.logRelayErr("update_write_task", errors.New("boom"))
	})

	if !strings.Contains(stderr, "driver: observer relay update_write_task: boom") {
		t.Fatalf("stderr missing message: %q", stderr)
	}

	// audit should contain an entry tagged observer_relay_error with the op
	body, err := os.ReadFile(dir + "/audit.log")
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(string(body), `"event":"observer_relay_error"`) ||
		!strings.Contains(string(body), `"op":"update_write_task"`) ||
		!strings.Contains(string(body), `"error":"boom"`) {
		t.Fatalf("audit missing fields: %s", body)
	}
}

func TestLogRelayErr_NilErrorIsNoop(t *testing.T) {
	tools := &Tools{}
	stderr := captureStderr(t, func() {
		tools.logRelayErr("x", nil)
	})
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
}
```

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/driver/ -run TestLogRelayErr -v`
Expected: FAIL（`tools.logRelayErr` undefined）

- [ ] **Step 3: 实现 helper**

在 `internal/driver/tools.go` 找到 `func (t *Tools) emit(ev observer.Event) {` 这块，**在它下方** 插入：

```go
// logRelayErr surfaces an observer-relay operation error in two places:
// stderr (so it shows up in driver-agent's log) and the audit log
// (so it's queryable later). Used by callers that intentionally degrade
// relay failures to warnings instead of aborting the request.
func (t *Tools) logRelayErr(op string, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "driver: observer relay %s: %v\n", op, err)
	if t.audit != nil {
		t.audit.Log(AuditEvent{Event: "observer_relay_error", Op: op, Error: err.Error()})
	}
}
```

如果 `tools.go` 顶部 import 未引入 `"os"`，加上。

- [ ] **Step 4: 给 AuditEvent 补字段**

`internal/driver/audit.go` 搜索 `type AuditEvent struct`，在末尾添加（保持 JSON 兼容，omitempty）：

```go
	Op    string `json:"op,omitempty"`
	Error string `json:"error,omitempty"`
```

- [ ] **Step 5: 跑测试看绿**

Run: `go test ./internal/driver/ -run TestLogRelayErr -v`
Expected: PASS（两个）

- [ ] **Step 6: 整包回归**

Run: `go test ./internal/driver/...`
Expected: PASS（不应破坏任何旧测试）

- [ ] **Step 7: 提交**

```bash
git add internal/driver/tools.go internal/driver/audit.go internal/driver/tools_log_relay_err_test.go
git commit -m "feat(driver): add logRelayErr helper (stderr + audit)

Centralizes 'observer relay op failed' logging used by the upcoming
submit_task and ServePendingLoop degradations. Extends AuditEvent with
op/error fields tagged observer_relay_error.

Spec: docs/superpowers/specs/2026-06-13-driver-contract-1.1-design.md

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: submit_task 半成功降级为 warnings (Bug §1.1 #1)

**Files:**
- Modify: `internal/driver/tools.go:492-538` — submit_task 响应增加 `warnings`，UpdateWriteTask 失败不再 return err
- Modify: `internal/driver/tools.go` — `recordDelegatedTask` 错误处理改为可降级（具体见 Step 3）
- Test: `internal/driver/tools_test.go` 追加

- [ ] **Step 1: 写失败测试**

在 `internal/driver/tools_test.go` 末尾追加：

```go
// TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning verifies that when
// DelegateTask succeeds (slave is already running the task) but observer
// UpdateWriteTask fails, submit_task still returns task_id and surfaces the
// failure as a warning. This is the §1.1 #1 invariant: "DelegateTask success
// ⇒ Claude always gets a task_id".
func TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/write-tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"write_id":"w-1","put_url":"http://example/put"}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/writes/"):
			http.Error(w, "store unavailable", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected observer request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-77", SessionID: "sess-77", Status: "assigned"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.DriverDefaults.ArtifactTransport = ArtifactTransportObserverLazy
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))

	tmp := t.TempDir()
	args, _ := json.Marshal(map[string]any{
		"prompt":              "do work",
		"skill":               "chat",
		"target_display_name": "slave-a",
		"write_paths":         []map[string]any{{"path": tmp + "/out.txt", "overwrite": true}},
	})

	out, err := toolByName(t, tools, "submit_task").Call(context.Background(), args)
	require.NoError(t, err, "submit_task must NOT return error; DelegateTask already succeeded")
	require.Contains(t, string(out), `"task_id":"task-77"`)
	require.Contains(t, string(out), `"warnings"`)
	require.Contains(t, string(out), "update_write_task")

	// reg.TrackTask must still have been called so that wait_task can later
	// find the write tokens for this task.
	written := tools.reg.WrittenFiles("task-77")
	require.NotNil(t, written, "TrackTask should have been called even after warning")
}
```

如 `tools_test.go` 顶部缺少 `"net/http"` / `"net/http/httptest"` / `"strings"` 任一 import，请加上。`stubTokenSource` 与 `toolByName` / `newTestTools` 在同包已存在，直接复用。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/driver/ -run TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning -v`
Expected: FAIL（当前 `UpdateWriteTask` 失败会 `return err`，require.NoError 触发）

- [ ] **Step 3: 改 submit_task 把 UpdateWriteTask + recordDelegatedTask 失败降级**

`internal/driver/tools.go` 找到 submit_task 的 Call 方法，定位 492-537 行这段（DelegateTask 之后到 return json.Marshal 之前）。**完整替换**为下面的代码：

```go
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}

	// From this point on, the task is running on the slave. Any helper-step
	// failure (journal, write-token rebind, observer UpdateWriteTask) is
	// degraded to a warning rather than returned as error — otherwise Claude
	// would think the task failed to dispatch and either re-submit (double
	// run) or abandon it. See §1.1 #1 of the 2026-06-13 review.
	var warnings []string

	if err := s.t.recordDelegatedTask(delegatedTaskRecord{
		Tool:              s.Name(),
		Response:          resp,
		TargetID:          targetID,
		TargetDisplayName: targetName,
		Skill:             skill,
		Wait:              false,
		TimeoutSec:        timeout,
	}); err != nil {
		warnings = append(warnings, "record delegated task: "+err.Error())
		s.t.logRelayErr("record_delegated_task", err)
	}

	s.t.emit(observer.Event{
		Type:          observer.EventDriverTaskSubmitted,
		TaskID:        resp.TaskID,
		Summary:       observer.SummarizePrompt(args.Prompt, 80),
		Status:        "assigned",
		TargetAgentID: targetID,
		TargetRole:    targetRole,
	})

	for _, tok := range writeTokens {
		s.t.reg.RebindWriteTokenTaskID(tok, resp.TaskID)
	}
	for _, writeID := range observerWriteIDs {
		if err := s.t.observerRelay().UpdateWriteTask(ctx, writeID, resp.TaskID); err != nil {
			warnings = append(warnings, fmt.Sprintf("observer update_write_task %s: %v", writeID, err))
			s.t.logRelayErr("update_write_task", err)
		}
	}
	s.t.reg.TrackTask(resp.TaskID, writeTokens)

	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"session_id":          resp.SessionID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"manifest":            manifest,
		"warnings":            warnings,
	})
}
```

注意：`recordDelegatedTask` 当前在错误路径返回 `&MCPToolError{...}`，调用方拿到时类型是 `error`，上面 `warnings = append(...)` 会调它的 `.Error()`，行为正确。

`warnings` 字段 marshal 时若为 nil 会输出 `"warnings":null`。希望省略时用 `omitempty`，但 map 类型不支持 omitempty —— 改成显式只在非空时塞入：

把上面 `return json.Marshal(...)` 那段改成：

```go
	respMap := map[string]interface{}{
		"task_id":             resp.TaskID,
		"session_id":          resp.SessionID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"manifest":            manifest,
	}
	if len(warnings) > 0 {
		respMap["warnings"] = warnings
	}
	return json.Marshal(respMap)
}
```

- [ ] **Step 4: 跑新测试看绿**

Run: `go test ./internal/driver/ -run TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning -v`
Expected: PASS

- [ ] **Step 5: 跑 submit_task 相关旧测试看是否回归**

Run: `go test ./internal/driver/ -run "TestSubmit" -v`
Expected: 全部 PASS（旧测试不应被打破——新逻辑只在 happy path 加了 emit 与 reg.TrackTask 的相对顺序保持不变，且 warnings 字段只在非空时输出）

如有失败，逐个看是哪条 assertion，多半是其它测试断言"response 不含 warnings"——把断言改成 `require.NotContains(string(out), "warnings")` 仍能通过。

- [ ] **Step 6: 整包回归**

Run: `go test ./internal/driver/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/driver/tools.go internal/driver/tools_test.go
git commit -m "fix(driver): degrade submit_task post-DelegateTask failures to warnings

DelegateTask success means the slave is already running. Returning an
error for any subsequent helper failure (recordDelegatedTask,
UpdateWriteTask) made Claude believe the task wasn't dispatched and
either re-submit or abandon it. Collect those failures into a 'warnings'
field on the response instead.

Fixes §1.1 #1 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: observer_relay 错误可见 + 单失败不中断 (Bug §1.1 #2)

**Files:**
- Modify: `internal/driver/observer_relay.go:255-320` — `ServePendingOnce` 改 errors.Join；`ServePendingLoop` 写 stderr + audit
- Test: 新建 `internal/driver/observer_relay_test.go`

- [ ] **Step 1: 写失败测试**

新建 `internal/driver/observer_relay_test.go`：

```go
package driver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestServePendingOnce_ContinuesPastSingleFailure verifies that when one
// upload fails, the loop still attempts the remaining requests instead of
// bailing out (which silently strands the rest forever).
// Fixes §1.1 #2 of docs/review-2026-06-13.md.
func TestServePendingOnce_ContinuesPastSingleFailure(t *testing.T) {
	tmp := t.TempDir()
	mkFile := func(name, body string) string {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	pA := mkFile("a.txt", "AAA")
	pB := mkFile("b.txt", "BBB")
	pC := mkFile("c.txt", "CCC")

	var uploaded sync.Map // artifactID -> true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/artifact-requests" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"requests":[
				{"request_id":"r1","artifact_id":"art-a","kind":"file","path":"` + pA + `","state":"pending"},
				{"request_id":"r2","artifact_id":"art-b","kind":"file","path":"` + pB + `","state":"pending"},
				{"request_id":"r3","artifact_id":"art-c","kind":"file","path":"` + pC + `","state":"pending"}
			]}`))
			return
		}
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/artifacts/") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"), "/content")
			if id == "art-b" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			uploaded.Store(id, true)
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Fatalf("unexpected req: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("t"))
	require := func(cond bool, msg string) {
		t.Helper()
		if !cond {
			t.Fatalf("%s", msg)
		}
	}
	require(relay != nil, "relay must be constructable")

	reg := NewFileRegistry(256)
	reg.RegisterObserverArtifact("art-a", pA, "file")
	reg.RegisterObserverArtifact("art-b", pB, "file")
	reg.RegisterObserverArtifact("art-c", pC, "file")

	err := relay.ServePendingOnce(context.Background(), reg, nil)
	require(err != nil, "expected aggregated error from failing upload")
	require(strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "500"),
		"err should reference upstream failure, got "+err.Error())
	_, okA := uploaded.Load("art-a")
	_, okC := uploaded.Load("art-c")
	require(okA, "a should have been uploaded BEFORE the failing b")
	require(okC, "c should have been uploaded AFTER the failing b — fix forgot to continue")
}
```

`stubTokenSource` 已存在；`NewFileRegistry` / `RegisterObserverArtifact` 都已存在（registry.go）。

补上 sync import：在测试文件顶 imports 加 `"sync"`。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/driver/ -run TestServePendingOnce_ContinuesPastSingleFailure -v`
Expected: FAIL（当前 art-b 失败后 art-c 不会被上传，所以 `okC` false）

- [ ] **Step 3: 修 ServePendingOnce 改聚合**

`internal/driver/observer_relay.go` 把 `ServePendingOnce` 整段替换为：

```go
func (r *ObserverRelay) ServePendingOnce(ctx context.Context, reg *FileRegistry, audit *AuditLog) error {
	if r == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/api/artifact-requests", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("artifact requests status %d", resp.StatusCode)
	}
	var listed observerArtifactRequestsResponse
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		return err
	}
	var errs []error
	for _, pending := range listed.Requests {
		path, kind, ok := reg.LookupObserverArtifact(pending.ArtifactID)
		if !ok {
			continue
		}
		if kind != "file" {
			continue
		}
		if err := AssertNoSymlinkLeaf(path); err != nil {
			errs = append(errs, fmt.Errorf("artifact %s: %w", pending.ArtifactID, err))
			continue
		}
		if err := r.uploadFile(ctx, pending.ArtifactID, path, audit); err != nil {
			errs = append(errs, fmt.Errorf("artifact %s: %w", pending.ArtifactID, err))
			continue
		}
	}
	return errors.Join(errs...)
}
```

确保 `internal/driver/observer_relay.go` 顶部 import 已含 `"errors"`（当前应已有；如缺，加上）。

- [ ] **Step 4: 跑新测试看绿**

Run: `go test ./internal/driver/ -run TestServePendingOnce_ContinuesPastSingleFailure -v`
Expected: PASS

- [ ] **Step 5: 修 ServePendingLoop 写 stderr + audit**

`internal/driver/observer_relay.go` 找到 `func (r *ObserverRelay) ServePendingLoop(...)`，把循环体内 `_ = r.ServePendingOnce(ctx, reg, audit)` 改成：

```go
		if err := r.ServePendingOnce(ctx, reg, audit); err != nil {
			fmt.Fprintf(os.Stderr, "driver: observer relay serve pending: %v\n", err)
			if audit != nil {
				audit.Log(AuditEvent{Event: "observer_relay_error", Op: "serve_pending", Error: err.Error()})
			}
		}
```

如顶部缺 `"os"` import，加上。

- [ ] **Step 6: 给 ServePendingLoop 加一个 stderr-capture 集成测试**

继续追加到 `internal/driver/observer_relay_test.go`：

```go
// TestServePendingLoop_LogsErrorsToStderrAndAudit confirms that the loop no
// longer silently swallows ServePendingOnce errors.
func TestServePendingLoop_LogsErrorsToStderrAndAudit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// always fail listing → triggers the error path on the first tick
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("t"))

	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		stderr := captureStderr(t, func() {
			relay.ServePendingLoop(ctx, NewFileRegistry(16), audit, 20*time.Millisecond)
		})
		done <- stderr
	}()
	// Let one tick happen, then cancel.
	time.Sleep(80 * time.Millisecond)
	cancel()

	var stderr string
	select {
	case stderr = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServePendingLoop did not exit after ctx cancel")
	}
	if !strings.Contains(stderr, "driver: observer relay serve pending:") {
		t.Fatalf("stderr missing log: %q", stderr)
	}
	body, _ := os.ReadFile(dir + "/audit.log")
	if !strings.Contains(string(body), `"event":"observer_relay_error"`) ||
		!strings.Contains(string(body), `"op":"serve_pending"`) {
		t.Fatalf("audit missing: %s", body)
	}
}

func init() { _ = atomic.LoadInt32 } // keep sync/atomic import alive if unused
```

补 import：`"time"`。如果 `atomic` 已不需要则删除 import 与最后那行 init。

- [ ] **Step 7: 跑该测试看绿**

Run: `go test ./internal/driver/ -run TestServePendingLoop_LogsErrorsToStderrAndAudit -v -count=1`
Expected: PASS

- [ ] **Step 8: 整包回归**

Run: `go test ./internal/driver/...`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add internal/driver/observer_relay.go internal/driver/observer_relay_test.go
git commit -m "fix(driver): observer relay errors no longer silent

ServePendingOnce: aggregate per-artifact errors via errors.Join instead
of bailing on the first failure (which stranded the rest forever).
ServePendingLoop: log to stderr + audit on each failed tick.

Fixes §1.1 #2 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: MCPServer.Serve 接受 ctx (Bug §1.1 #3 第一半)

**Files:**
- Modify: `internal/driver/mcp_server.go:69-91` — Serve 签名加 ctx；dispatch 用传入 ctx
- Modify: `cmd/driver-agent/main.go:206` — 传 ctx
- Modify: `internal/driver/mcp_server_test.go` 全部 `srv.Serve(in, &out)` → `srv.Serve(context.Background(), in, &out)`
- Test: 在 mcp_server_test.go 追加 ctx-cancel 用例

- [ ] **Step 1: 写失败测试**

在 `internal/driver/mcp_server_test.go` 末尾追加：

```go
// TestMCPServerServe_StopsOnContextCancel verifies that cancelling the ctx
// passed into Serve drains in-flight long-running tool calls and returns from
// Serve in bounded time, instead of waiting on stdin EOF. Fixes §1.1 #3 of
// docs/review-2026-06-13.md.
func TestMCPServerServe_StopsOnContextCancel(t *testing.T) {
	blocking := &mockTool{
		name: "blocker",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	// pipe reader stays open (no EOF) so the ONLY way Serve can return is via
	// ctx cancel propagating through the in-flight tool call + Serve loop.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"blocker","arguments":{}}}` + "\n",
		))
		// intentionally do not close pw — Serve should exit anyway
	}()
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{blocking})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, pr, &out) }()

	// Give the tool a moment to be dispatched, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	_ = pr.Close() // unblock scanner so loop can exit

	select {
	case <-done:
		// expected within 1s; the deferred wg.Wait inside Serve must also
		// observe the cancelled ctx
	case <-time.After(2 * time.Second):
		t.Fatalf("Serve did not return after ctx cancel; out=%s", out.String())
	}
}
```

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/driver/ -run TestMCPServerServe_StopsOnContextCancel -v`
Expected: FAIL — 编译错误 `too many arguments to srv.Serve`（Serve 签名当前是 `Serve(r, w)`）

- [ ] **Step 3: 修 Serve 签名 + dispatch 用 ctx**

`internal/driver/mcp_server.go` 把 `func (s *MCPServer) Serve(r io.Reader, w io.Writer) error { ... }` 整段替换为：

```go
// Serve reads one JSON-RPC message per line from r and writes responses to w.
// Tool calls run concurrently so a long-running tool cannot block later
// requests; response ids let JSON-RPC clients match out-of-order replies.
// Returns when r reaches EOF, ctx is cancelled, or an "exit" notification is
// received. The ctx is propagated into every tool Call so callers that wait
// (e.g. wait_task long-polling) can unwind when the driver shuts down.
func (s *MCPServer) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(w, json.RawMessage(`null`), -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "exit" {
			return nil
		}
		s.dispatch(ctx, w, &req, &wg)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err() // nil if not cancelled
}
```

- [ ] **Step 4: 修主调用点**

`cmd/driver-agent/main.go` 找 `if err := mcpSrv.Serve(os.Stdin, os.Stdout); err != nil {` 改成：

```go
	if err := mcpSrv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
```

- [ ] **Step 5: 修旧测试**

`internal/driver/mcp_server_test.go` 中所有 `srv.Serve(in, &out)` 与 `srv.Serve(in, outW)`，前面加 `context.Background()`。

Run: `grep -n "srv.Serve(" internal/driver/mcp_server_test.go`
逐一改。当前共约 6 处（init / tools list / dispatch / error code / concurrent / unknown method 等）。

- [ ] **Step 6: 跑新测试看绿**

Run: `go test ./internal/driver/ -run TestMCPServerServe_StopsOnContextCancel -v -timeout 30s`
Expected: PASS（≤2s 内返回）

- [ ] **Step 7: 整包回归**

Run: `go test ./internal/driver/... ./cmd/driver-agent/...`
Expected: PASS

如有任何 build 失败，多半是漏改一处 `srv.Serve(in, ...)`，按编译报错位置补 `context.Background()`。

- [ ] **Step 8: 提交**

```bash
git add internal/driver/mcp_server.go internal/driver/mcp_server_test.go cmd/driver-agent/main.go
git commit -m "fix(driver): thread root ctx through MCPServer.Serve

Serve used context.Background() for all tool dispatches, so driver
shutdown couldn't cancel wait_task long-polling — Serve sat until stdin
EOF. Add ctx parameter to Serve, propagate it into dispatch, and pass
driver's root ctx from cmd/driver-agent/main.go.

Fixes §1.1 #3 (first half) of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: writeLine EPIPE 退出 (Bug §1.1 #3 第二半)

**Files:**
- Modify: `internal/driver/mcp_server.go:29-43, 69-91, 191-203` — MCPServer 加 `broken int32`；writeLine 检 err；Serve 主循环检 broken
- Test: `internal/driver/mcp_server_test.go` 追加

- [ ] **Step 1: 写失败测试**

在 `internal/driver/mcp_server_test.go` 末尾追加：

```go
// TestMCPServerWriteLine_EPIPETriggersStop verifies that when stdout is
// closed (e.g. parent Claude Code process died), Serve detects the broken
// pipe on the next write and exits instead of silently looping on a dead
// channel. Fixes §1.1 #3 (second half) of docs/review-2026-06-13.md.
func TestMCPServerWriteLine_EPIPETriggersStop(t *testing.T) {
	tool := &mockTool{
		name: "ping",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	// reader side closed → any Write returns ErrClosedPipe
	pr, pw := io.Pipe()
	_ = pr.Close()

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping","arguments":{}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}` + "\n",
	)
	srv := NewMCPServer([]Tool{tool})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), in, pw) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil; expected broken-pipe error")
		}
		if !strings.Contains(err.Error(), "broken") && !strings.Contains(err.Error(), "closed pipe") {
			t.Fatalf("Serve err = %v; expected broken-pipe", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not exit on broken pipe")
	}
}
```

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/driver/ -run TestMCPServerWriteLine_EPIPETriggersStop -v -timeout 30s`
Expected: FAIL（当前 writeLine 吞 err，Serve 不会因此退出，最终超时）

- [ ] **Step 3: 改 MCPServer struct 加 broken**

`internal/driver/mcp_server.go` 把 `type MCPServer struct { ... }` 段改成：

```go
type MCPServer struct {
	tools     map[string]Tool
	toolOrder []string
	writeMu   sync.Mutex
	linesOut  int64
	broken    int32 // set to 1 by writeLine when the writer returns EPIPE/closed-pipe
}
```

- [ ] **Step 4: 改 writeLine 检 err**

把 `func (s *MCPServer) writeLine(w io.Writer, v interface{})` 整段替换为：

```go
func (s *MCPServer) writeLine(w io.Writer, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := w.Write(b); err != nil {
		s.handleWriteErr(err)
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		s.handleWriteErr(err)
		return
	}
	if f, ok := w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	atomic.AddInt64(&s.linesOut, 1)
}

func (s *MCPServer) handleWriteErr(err error) {
	fmt.Fprintf(os.Stderr, "driver: mcp write: %v\n", err)
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) {
		atomic.StoreInt32(&s.broken, 1)
	}
}
```

补 imports：`"errors"`、`"fmt"`、`"os"`、`"syscall"`。

- [ ] **Step 5: Serve 主循环检 broken**

把 Task 4 Step 3 写的 Serve `for scanner.Scan() { ... }` 的 `if ctx.Err() != nil { return ctx.Err() }` 之后立即加：

```go
		if atomic.LoadInt32(&s.broken) == 1 {
			return errors.New("mcp stdout broken pipe")
		}
```

最终 Serve 主循环形如：

```go
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if atomic.LoadInt32(&s.broken) == 1 {
			return errors.New("mcp stdout broken pipe")
		}
		line := scanner.Bytes()
		...
```

确保 Serve 函数所在文件已 import `"errors"`。

- [ ] **Step 6: 跑新测试看绿**

Run: `go test ./internal/driver/ -run TestMCPServerWriteLine_EPIPETriggersStop -v -timeout 30s`
Expected: PASS

- [ ] **Step 7: 整包回归 + 跑 race 检测**

Run: `go test ./internal/driver/... -race -count=1`
Expected: PASS（broken 字段用 atomic，无 race；其它原来就 ok）

- [ ] **Step 8: 提交**

```bash
git add internal/driver/mcp_server.go internal/driver/mcp_server_test.go
git commit -m "fix(driver): MCPServer detects broken stdout pipe and exits

writeLine used to ignore Write errors entirely; EPIPE/closed-pipe meant
silent garbage JSON-RPC frames and a Serve loop that kept pulling
requests it could never respond to. Log all write errors to stderr; on
EPIPE/closed-pipe set an atomic flag that Serve checks each loop
iteration, returning a 'broken pipe' error.

Fixes §1.1 #3 (second half) of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: wait_task / get_task task_id 守卫与 registry key 统一 (Bug §1.1 #4)

**Files:**
- Modify: `internal/driver/tools.go` — wait_task (约 653-758) 与 get_task (约 554-634) 入口校验 + reg 调用统一用 `args.TaskID`
- Test: `internal/driver/tools_test.go` 追加

- [ ] **Step 1: 写失败测试**

在 `internal/driver/tools_test.go` 末尾追加：

```go
// TestWaitTask_RejectsEmptyTaskID prevents WrittenFiles("")+ForgetTask("")
// from silently nuking an unrelated zero-key registry entry.
// Fixes §1.1 #4 of docs/review-2026-06-13.md.
func TestWaitTask_RejectsEmptyTaskID(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	_, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":""}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "task_id is required")
}

func TestGetTask_RejectsEmptyTaskID(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	_, err := toolByName(t, tools, "get_task").Call(context.Background(),
		json.RawMessage(`{"task_id":""}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "task_id is required")
}

// TestWaitTask_UsesArgsTaskIDForRegistry verifies the registry key is the
// task_id the caller submitted (= the same id we stored in reg.TrackTask),
// even when the SDK echoes a different info.TaskID. The emit event still
// uses info.TaskID for human-facing display.
func TestWaitTask_UsesArgsTaskIDForRegistry(t *testing.T) {
	tmp := t.TempDir()
	target := tmp + "/out.txt"
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	sdk := &fakeSDK{
		getTaskFunc: func(id string) (*agentsdk.TaskInfo, error) {
			// Server returns DIFFERENT TaskID (an alias) — registry must
			// still use the args.TaskID we originally tracked.
			return &agentsdk.TaskInfo{
				TaskID: "server-alias-99",
				Status: "completed",
			}, nil
		},
	}
	tools := newTestTools(t, sdk)

	// Manually populate registry as if submit_task had been called with id=client-1.
	tok := tools.reg.RegisterWrite(target, true, "")
	tools.reg.RebindWriteTokenTaskID(tok, "client-1")
	tools.reg.RecordWritten("client-1", WrittenFile{Path: target, Bytes: 5, SHA256: "abc"})
	tools.reg.TrackTask("client-1", []string{tok})

	out, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"client-1","poll_interval_sec":1,"timeout_sec":5}`))
	require.NoError(t, err)
	require.Contains(t, string(out), `"written_files"`)
	require.Contains(t, string(out), target,
		"wait_task should have looked up writes under args.TaskID=client-1, not server-alias-99")

	// emit should have been called with info.TaskID for display.
	obs, ok := tools.observer.(*fakeObserver)
	require.True(t, ok)
	var sawAlias bool
	for _, ev := range obs.events {
		if ev.TaskID == "server-alias-99" {
			sawAlias = true
		}
	}
	require.True(t, sawAlias, "emit should still surface server-side alias for display")

	// Subsequent wait_task with the same client-1 must find empty
	// written_files because ForgetTask("client-1") cleared the entry.
	out2, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"client-1","poll_interval_sec":1,"timeout_sec":5}`))
	require.NoError(t, err)
	require.NotContains(t, string(out2), target,
		"after wait_task, ForgetTask(args.TaskID) should have cleared the entry")
}
```

需要给 `fakeSDK` 加 `getTaskFunc` 字段（如果尚未支持）。查一下：

Run: `grep -n "getTaskFunc\|GetTask(ctx" internal/driver/tools_test.go | head -5`

如未支持，把 fakeSDK 的 `GetTask` 改成可注入：

```go
type fakeSDK struct {
	discoverFunc func() ([]agentsdk.AgentCard, error)
	delegateFunc func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	getTaskFunc  func(id string) (*agentsdk.TaskInfo, error) // NEW
}

func (f *fakeSDK) GetTask(ctx context.Context, id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
	if f.getTaskFunc != nil {
		return f.getTaskFunc(id)
	}
	return &agentsdk.TaskInfo{TaskID: id, Status: "completed"}, nil
}
```

确保 fakeObserver 有 `events []observer.Event` 公开字段（应已存在；如名字不同按实际改）。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/driver/ -run "TestWaitTask_RejectsEmptyTaskID|TestGetTask_RejectsEmptyTaskID|TestWaitTask_UsesArgsTaskIDForRegistry" -v -timeout 30s`
Expected: FAIL（空 task_id 不报错；alias 测试现在用 `args.TaskID` 也会通过——所以更准确的红/绿判断在 Step 5 后回头看）

实际上当前实现 `WrittenFiles(args.TaskID)` 已经用的就是 args.TaskID（与目标一致），但 `SyncWrites` 上面行 720 用的是 `taskID`（info.TaskID 优先）。**当前 alias 测试可能意外通过**——这是因为我们的修复目标是"统一用 args.TaskID"，而 reg 已经统一用了 args.TaskID。需要让 SyncWrites 也用 args.TaskID。我们在 Step 4 处理。

空校验那 2 个测试当前一定红。

- [ ] **Step 3: 改 wait_task 入口守卫**

`internal/driver/tools.go` 找到 `func (w *waitTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {` 在 `if err := json.Unmarshal(...)` 之后加：

```go
	if strings.TrimSpace(args.TaskID) == "" {
		return nil, &MCPToolError{Message: "task_id is required"}
	}
```

- [ ] **Step 4: 改 wait_task 内部 SyncWrites 用 args.TaskID**

找到行 720：

```go
		if w.t.useObserverRelay() {
			if _, err := w.t.observerRelay().SyncWrites(ctx, taskID, w.t.cfg.DriverDefaults.DisableUIDCheck, w.t.reg); err != nil {
				return nil, &MCPToolError{Message: "observer sync writes: " + err.Error()}
			}
		}
		written := w.t.reg.WrittenFiles(args.TaskID)
		w.t.reg.ForgetTask(args.TaskID)
```

把第二行的 `taskID` 改成 `args.TaskID`：

```go
		if w.t.useObserverRelay() {
			if _, err := w.t.observerRelay().SyncWrites(ctx, args.TaskID, w.t.cfg.DriverDefaults.DisableUIDCheck, w.t.reg); err != nil {
				return nil, &MCPToolError{Message: "observer sync writes: " + err.Error()}
			}
		}
		written := w.t.reg.WrittenFiles(args.TaskID)
		w.t.reg.ForgetTask(args.TaskID)
```

`emit` 与 `progress`/响应中的 `taskID`（info.TaskID 优先）保持不变 —— 那是给人看的；reg / observer write 的 key 保持 args.TaskID。

- [ ] **Step 5: 改 get_task 入口守卫**

`internal/driver/tools.go` 找到 `func (g *getTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {` 在 `if err := json.Unmarshal(...)` 之后加：

```go
	if strings.TrimSpace(args.TaskID) == "" {
		return nil, &MCPToolError{Message: "task_id is required"}
	}
```

`get_task` 不动 registry，只展示状态，所以不需要改其它部分。

- [ ] **Step 6: 跑新测试看绿**

Run: `go test ./internal/driver/ -run "TestWaitTask_RejectsEmptyTaskID|TestGetTask_RejectsEmptyTaskID|TestWaitTask_UsesArgsTaskIDForRegistry" -v -timeout 30s`
Expected: 三个均 PASS

- [ ] **Step 7: 整包回归**

Run: `go test ./internal/driver/... -race -count=1`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/driver/tools.go internal/driver/tools_test.go
git commit -m "fix(driver): wait_task/get_task reject empty task_id; reg key unified

Empty task_id used to silently invoke WrittenFiles(\"\") + ForgetTask(\"\"),
nuking any zero-key registry entry. SyncWrites used info.TaskID (server
alias) while WrittenFiles/ForgetTask used args.TaskID — split key
sources. Now all registry and observer-write calls use args.TaskID
(= the id stored at submit_task time). info.TaskID is reserved for the
human-facing emit event.

Fixes §1.1 #4 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: 最终全量回归 + 跨包构建

- [ ] **Step 1: 全模块构建**

Run: `go build ./...`
Expected: 无输出（成功）

- [ ] **Step 2: 全模块测试 + race**

Run: `go test ./... -race -count=1 -timeout 300s`
Expected: PASS

如某些 prod_test / E2E 包要求真实 observer，可单独 skip 或限定到 internal/、cmd/。

- [ ] **Step 3: 整理 todo / 验证 spec 覆盖**

把本 plan 的 6 个 task 与 spec 的 4 个 bug 对照：

| Spec § | Plan task |
|---|---|
| Bug 1 (tools.go:521) | Task 1 + Task 2 |
| Bug 2 (observer_relay.go:303) | Task 1 + Task 3 |
| Bug 3 (mcp_server.go ctx) | Task 4 |
| Bug 3 (writeLine EPIPE) | Task 5 |
| Bug 4 (wait_task task_id) | Task 6 |

确认无遗漏。

- [ ] **Step 4: 复制 commit log 给用户**

Run: `git log --oneline master..HEAD`
Expected: 7 个 commit（docs + 6 个 fix）。

---

## 验证清单（implementation 完成后由 verification-before-completion 复核）

- [ ] `go build ./...` 成功
- [ ] `go test ./internal/driver/... -race -count=1` 成功
- [ ] 新增 6+ 个测试（log_relay_err×2 / submit_task warning / serve_pending×2 / mcp_serve ctx-cancel / mcp_writeLine epipe / wait_task empty / get_task empty / wait_task alias）全部 PASS
- [ ] submit_task response 在 happy path 不含 `warnings` 字段；UpdateWriteTask 失败时含
- [ ] `git log --oneline master..HEAD` 含 1 docs + 6 fix commit
- [ ] Spec 的 4 条 CRITICAL bug 全部映射到 plan task（见上表）
