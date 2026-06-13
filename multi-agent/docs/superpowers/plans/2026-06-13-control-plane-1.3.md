# Control Plane §1.3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 control plane 5 个 CRITICAL bug（observerclient 启动死循环 / observerweb 上传半失败 / register 静默 takeover / 401 cooldown 丢事件 / BlobStore 并发 TOCTOU），让 driver/slave/observer-server 在 observer 抖动、并发 register、并发 push 这些常态下保持协议契约一致性。

**Architecture:** observerclient 加 bootstrap 超时 + degraded mode；observerweb 把"对象上传 vs DB 标记"半失败 explicit log + 502 状态；register 加 5min duplicate guard + `force` opt-in；observerclient 401 cooldown 期间 run() loop 暂停 dequeue；userspace BlobStore.Put 改 `INSERT OR IGNORE` + tx；ObjectBlobStore 去 hasBlobPath 双写。所有改动局限在 5 个文件 + 各自测试。

**Tech Stack:** Go stdlib（含 `sync`, `database/sql`, `context`, `net/http`）+ 现有 sqlite driver。无新依赖。

**Spec:** `docs/superpowers/specs/2026-06-13-control-plane-1.3-design.md`

**Worktree:** `/root/multi-agent/.worktrees/fix-control-plane-1.3/multi-agent/`，分支 `worktree-fix-control-plane-1.3`，baseline `go test ./internal/observerclient/... ./internal/observerweb/... ./internal/observerstore/... ./internal/userspace/...` 通过。

---

## 文件结构

- 修改：`internal/observerclient/client.go` — `Config.BootstrapTimeout` 字段 + New() 超时降级 + cooldown 字段 + `inCooldown/setCooldown/clearCooldown` helpers + run() 主循环 cooldown gate
- 修改：`internal/observerclient/bootstrap.go` — handle401 包 setCooldown/clearCooldown
- 修改：`internal/observerstore/store.go` — 加 `ErrArtifactNotFound` / `ErrWriteNotFound` sentinels；`AgentLastActiveAt` 新方法
- 修改：`internal/observerstore/types.go` — 接口加 `AgentLastActiveAt(workspaceID, agentID string) (time.Time, bool, error)`
- 修改：`internal/observerweb/server.go` — artifact/write put 失败显式 log + 502；register 加 `Force` + 5min duplicate guard
- 修改：`internal/userspace/blob.go` — Put 改 INSERT OR IGNORE + tx；upsertObjectBlob 去 hasBlobPath 写入分支
- 新增/扩展测试文件：6 个

---

## Task 1: observerstore sentinel errors + AgentLastActiveAt

**Files:**
- Modify: `internal/observerstore/store.go` — 加 sentinels；改 MarkArtifactAvailable/MarkWriteCompleted 返 sentinel；加 AgentLastActiveAt
- Modify: `internal/observerstore/types.go` — 接口加 AgentLastActiveAt
- Modify: `internal/observerstore/store_interface_test.go` — fake 加该方法
- Test: `internal/observerstore/store_test.go` 追加

- [ ] **Step 1: 写失败测试**

`internal/observerstore/store_test.go` 末尾追加：

```go
// TestMarkArtifactAvailableReturnsErrArtifactNotFound pins the sentinel
// observerweb uses to distinguish "row missing" (404) from "DB error" (502).
func TestMarkArtifactAvailableReturnsErrArtifactNotFound(t *testing.T) {
    s := newTestSQLiteStore(t)
    err := s.MarkArtifactAvailable("ws", "agent", "art-missing", "text/plain", "sha", "key", 1)
    if !errors.Is(err, ErrArtifactNotFound) {
        t.Fatalf("expected ErrArtifactNotFound, got %v", err)
    }
}

// TestMarkWriteCompletedReturnsErrWriteNotFound mirrors the above for writes.
func TestMarkWriteCompletedReturnsErrWriteNotFound(t *testing.T) {
    s := newTestSQLiteStore(t)
    err := s.MarkWriteCompleted("ws", "agent", "wr-missing", "text/plain", "sha", "key", 1)
    if !errors.Is(err, ErrWriteNotFound) {
        t.Fatalf("expected ErrWriteNotFound, got %v", err)
    }
}

// TestAgentLastActiveAt verifies the lookup used by register's
// duplicate-takeover guard.
func TestAgentLastActiveAt_Unknown(t *testing.T) {
    s := newTestSQLiteStore(t)
    _, found, err := s.AgentLastActiveAt("ws", "ghost")
    if err != nil {
        t.Fatalf("err: %v", err)
    }
    if found {
        t.Fatal("expected found=false for unknown agent")
    }
}

func TestAgentLastActiveAt_Known(t *testing.T) {
    s := newTestSQLiteStore(t)
    if err := s.UpsertWorkspaceLazy("ws", "Workspace", "key-1"); err != nil {
        t.Fatalf("upsert ws: %v", err)
    }
    a := Agent{WorkspaceID: "ws", ID: "driver-1", Role: "driver", DisplayName: "driver-1"}
    if err := s.UpsertAgent(a, "tok-1", "key-1"); err != nil {
        t.Fatalf("upsert agent: %v", err)
    }
    ts, found, err := s.AgentLastActiveAt("ws", "driver-1")
    if err != nil {
        t.Fatalf("err: %v", err)
    }
    if !found {
        t.Fatal("expected found=true")
    }
    if time.Since(ts) > 5*time.Second {
        t.Fatalf("last_seen_at too old: %v", ts)
    }
}
```

如 `newTestSQLiteStore` 名称不同，按现有 store_test 顶部 helper。如缺 `errors` / `time` import，加上。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/observerstore/ -run "TestMarkArtifactAvailableReturnsErrArtifactNotFound|TestMarkWriteCompletedReturnsErrWriteNotFound|TestAgentLastActiveAt" -v -count=1`
Expected: FAIL（sentinels + AgentLastActiveAt 未定义）

- [ ] **Step 3: 加 sentinels + 改 MarkArtifactAvailable/MarkWriteCompleted**

`internal/observerstore/store.go` 顶部 (在 `import` 块之后) 加：

```go
// ErrArtifactNotFound is returned by MarkArtifactAvailable when no artifact
// row matches the (workspace, id, owner) key. observerweb maps this to 404;
// other errors (DB unreachable, constraint violation) map to 502 so the
// uploading client can distinguish "wrong target" from "server problem".
var ErrArtifactNotFound = errors.New("observerstore: artifact not found")

// ErrWriteNotFound is the write equivalent of ErrArtifactNotFound.
var ErrWriteNotFound = errors.New("observerstore: write not found or already completed")
```

`MarkArtifactAvailable` (现有约 line 879) 把：

```go
if n == 0 {
    return fmt.Errorf("artifact not found")
}
```

改成：

```go
if n == 0 {
    return ErrArtifactNotFound
}
```

`MarkWriteCompleted` (约 line 962) 把：

```go
if n == 0 {
    return fmt.Errorf("write not found or already completed")
}
```

改成：

```go
if n == 0 {
    return ErrWriteNotFound
}
```

- [ ] **Step 4: 加 AgentLastActiveAt**

`internal/observerstore/store.go` 找到 `AgentBoundWorkspace` (约 line 334)，**在它下面**加：

```go
// AgentLastActiveAt returns the last_seen_at timestamp for the given
// (workspace, agent_id). Used by observerweb's register handler to detect
// "agent_id already in use" before silently rotating its token out from
// under the live process. Returns (zero, false, nil) when the agent is
// unknown — that's a fresh registration.
func (s *SQLiteStore) AgentLastActiveAt(workspaceID, agentID string) (time.Time, bool, error) {
    var lastSeen string
    err := s.db.QueryRow(`SELECT last_seen_at FROM agents WHERE workspace_id=? AND id=?`,
        workspaceID, agentID).Scan(&lastSeen)
    if err == sql.ErrNoRows {
        return time.Time{}, false, nil
    }
    if err != nil {
        return time.Time{}, false, err
    }
    t, perr := time.Parse(time.RFC3339Nano, lastSeen)
    if perr != nil {
        // Best-effort: malformed timestamp on disk means we can't enforce the
        // duplicate guard for this row. Return found=false so register goes
        // through (better than blocking on a corrupt timestamp).
        return time.Time{}, false, nil
    }
    return t, true, nil
}
```

确保 `time` import 已在 store.go（用 `grep -c '"time"' internal/observerstore/store.go` 校验）。

- [ ] **Step 5: 接口扩 + fake 扩**

`internal/observerstore/types.go` 找到 `Store` interface 里 `AgentBoundWorkspace` 那一行，**之后**加：

```go
AgentLastActiveAt(workspaceID, agentID string) (time.Time, bool, error)
```

确保 types.go 已 import `"time"`；如无，加上。

`internal/observerstore/store_interface_test.go` 里那个 testStore（或同等命名）fake 实现，加：

```go
func (f *fakeStore) AgentLastActiveAt(workspaceID, agentID string) (time.Time, bool, error) {
    return f.lastSeen[workspaceID+"|"+agentID], f.lastSeenFound[workspaceID+"|"+agentID], nil
}
```

如果 fake 没有这两个 map 字段，加上：

```go
lastSeen      map[string]time.Time
lastSeenFound map[string]bool
```

并在 fakeStore 构造点初始化（看 NewFakeStore / 0-value 用法）。

实际命名按现有 store_interface_test.go 风格。

- [ ] **Step 6: 跑测试看绿**

Run: `go test ./internal/observerstore/ -run "TestMarkArtifactAvailableReturnsErrArtifactNotFound|TestMarkWriteCompletedReturnsErrWriteNotFound|TestAgentLastActiveAt" -v -count=1`
Expected: 4 PASS

- [ ] **Step 7: 全包回归 + 跨包 vet**

Run: `go vet ./... && go test ./internal/observerstore/... -race -count=1`
Expected: 全 PASS（接口扩可能 break observerweb 编译 — 容后修；纯 observerstore 包先 PASS）

如果 vet 报 observerweb 没实现新方法的 fake，先放着 — Task 5 会改 observerweb，那里同步修 fake。

如果 vet 报错让本任务无法编译，最小修复：在 observerweb 的 fakeStore 加上 stub `AgentLastActiveAt(...) (time.Time, bool, error) { return time.Time{}, false, nil }`，让编译过。

- [ ] **Step 8: 提交**

```bash
git add internal/observerstore/store.go internal/observerstore/types.go internal/observerstore/store_test.go internal/observerstore/store_interface_test.go
# 若 observerweb 的 fake 也补了 stub:
# git add internal/observerweb/<fake_file>
git commit -m "feat(observerstore): ErrArtifact/Write sentinels + AgentLastActiveAt

Adds two sentinel errors so callers (observerweb) can distinguish
'row not found' (→ 404) from 'DB error' (→ 502). Adds AgentLastActiveAt
for register's duplicate-takeover guard (returns last_seen_at).

Spec: docs/superpowers/specs/2026-06-13-control-plane-1.3-design.md

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: observerclient bootstrap timeout + degraded mode (Bug #9)

**Files:**
- Modify: `internal/observerclient/client.go` — Config 加 `BootstrapTimeout`；New() 超时降级
- Test: `internal/observerclient/client_test.go`

- [ ] **Step 1: 写失败测试**

`internal/observerclient/client_test.go` 末尾追加：

```go
// TestNewBootstrapTimeoutDegradesToEmptyToken pins the §1.3 #9 invariant:
// if the observer is unreachable at New() time, we MUST NOT block forever.
// Returning a degraded Client (token="" but enabled=true) lets the process
// start; the first Emit hits 401 and handle401 takes over once observer
// recovers. This kills the jetson/HPC startup-deadlock.
func TestNewBootstrapTimeoutDegradesToEmptyToken(t *testing.T) {
    // Black-hole TCP listener: accepts the connection but never writes a
    // response, so register's HTTP roundtrip would hang indefinitely
    // without the bootstrap timeout.
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatalf("listen: %v", err)
    }
    defer ln.Close()
    // We don't even Accept(); register's Dial succeeds then read hangs.

    dir := t.TempDir()
    cfg := Config{
        Enabled:          true,
        TelemetryEnabled: false,
        URL:              "http://" + ln.Addr().String(),
        WorkspaceID:      "ws-1",
        AgentID:          "agent-1",
        AgentRole:        "driver",
        APIKey:           "ak-1",
        TokenStatePath:   filepath.Join(dir, "tok"),
        BootstrapTimeout: 200 * time.Millisecond,
    }
    start := time.Now()
    c, err := New(cfg)
    elapsed := time.Since(start)
    if err != nil {
        t.Fatalf("New must NOT return error in degraded mode: %v", err)
    }
    if elapsed > 2*time.Second {
        t.Fatalf("New took %v; bootstrap timeout did not fire", elapsed)
    }
    if c.Token() != "" {
        t.Fatalf("expected empty token in degraded mode, got %q", c.Token())
    }
    if !c.Enabled() {
        t.Fatal("client must stay enabled so handle401 can recover later")
    }
}

// TestNewBootstrapTimeoutDefaultsTo5s verifies BootstrapTimeout=0 picks up
// the 5s default rather than meaning "no timeout".
func TestNewBootstrapTimeoutDefaultsTo5s(t *testing.T) {
    ln, _ := net.Listen("tcp", "127.0.0.1:0")
    defer ln.Close()
    dir := t.TempDir()
    cfg := Config{
        Enabled:        true,
        URL:            "http://" + ln.Addr().String(),
        WorkspaceID:    "ws-1",
        AgentID:        "agent-1",
        AgentRole:      "driver",
        APIKey:         "ak-1",
        TokenStatePath: filepath.Join(dir, "tok"),
        // BootstrapTimeout not set → default 5s
    }
    // Test wraps in its OWN deadline so a regression here (timeout
    // not taking effect) doesn't hang go test for minutes.
    done := make(chan struct{})
    go func() { _, _ = New(cfg); close(done) }()
    select {
    case <-done:
        // good
    case <-time.After(8 * time.Second):
        t.Fatal("New blocked > 8s; default bootstrap timeout missing")
    }
}
```

Imports needed: `"net"`, `"path/filepath"`, `"time"`. Check existing imports first.

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/observerclient/ -run TestNewBootstrap -v -count=1 -timeout 30s`
Expected: TestNewBootstrapTimeoutDegradesToEmptyToken FAIL (current New blocks then returns err), TestNewBootstrapTimeoutDefaultsTo5s FAIL (similar)

- [ ] **Step 3: 改 Config + New()**

`internal/observerclient/client.go` 找到 `type Config struct {` (约 line 24)，在结尾加一行：

```go
    // BootstrapTimeout caps how long New() will wait on the initial
    // loadOrRegister roundtrip. Zero → default 5s. Negative → no timeout
    // (legacy blocking behavior). On timeout, New() returns a degraded
    // Client (enabled=true, token="") so the process starts up; the first
    // Emit hits 401 and handle401 acquires a token when observer recovers.
    BootstrapTimeout time.Duration
```

找到 `func New(cfg Config) (*Client, error) {` (约 line 60)，把现有的：

```go
    tok, err := c.loadOrRegister(context.Background())
    if err != nil {
        return nil, err
    }
    c.token = tok
```

替换为：

```go
    bt := cfg.BootstrapTimeout
    if bt == 0 {
        bt = 5 * time.Second
    }
    var bootstrapCtx context.Context
    var bootstrapCancel context.CancelFunc
    if bt > 0 {
        bootstrapCtx, bootstrapCancel = context.WithTimeout(context.Background(), bt)
    } else {
        bootstrapCtx, bootstrapCancel = context.WithCancel(context.Background())
    }
    defer bootstrapCancel()
    tok, err := c.loadOrRegister(bootstrapCtx)
    if err != nil {
        // Degraded mode: process starts; first Emit triggers handle401 which
        // re-registers once observer is reachable. Far better than hard-fail
        // on transient observer outage where there's no systemd restart
        // (jetson, HPC login nodes).
        fmt.Fprintf(os.Stderr,
            "observerclient: bootstrap failed (%v); entering degraded mode — "+
                "events will queue and post once token is acquired\n", err)
        c.token = ""
    } else {
        c.token = tok
    }
```

Check that `"fmt"`, `"os"`, `"context"`, `"time"` are imported.

- [ ] **Step 4: 跑新测试看绿**

Run: `go test ./internal/observerclient/ -run TestNewBootstrap -v -count=1 -timeout 30s`
Expected: 2 PASS

- [ ] **Step 5: 跑既有相关测试**

Run: `go test ./internal/observerclient/ -run "TestNew" -v -count=1`
Expected: all PASS. One specific worry: `TestNewRegisterFailureReturnsError` (line 264 of client_test.go) — that test previously expected New() to return error on register failure. After this change, register failure NO longer returns error (it enters degraded mode). Update that test to assert degraded mode instead:

```go
// Find TestNewRegisterFailureReturnsError. Old shape (rough):
//   _, err := New(cfg)
//   require.Error(t, err)
// New shape:
//   c, err := New(cfg)
//   require.NoError(t, err)
//   require.Equal(t, "", c.Token())
//   require.True(t, c.Enabled())
```

Read the actual test (`internal/observerclient/client_test.go` lines 264-300 area) to see the exact stub setup; preserve setup, replace assertions.

Run again:

Run: `go test ./internal/observerclient/ -run "TestNew" -v -count=1`
Expected: PASS

- [ ] **Step 6: 全包 race**

Run: `go test ./internal/observerclient/... -race -count=1`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/observerclient/client.go internal/observerclient/client_test.go
git commit -m "fix(observerclient): bootstrap timeout + degraded-mode startup

Previously, New() called loadOrRegister(context.Background()) with no
timeout. An unreachable observer at startup blocked the entire process
forever — fatal on jetson/HPC where there's no systemd 'Restart=on-failure'
to rescue from a hard exit.

New behavior: New() applies a 5s default bootstrap timeout (configurable
via Config.BootstrapTimeout; negative disables for legacy callers). On
timeout, New() returns a degraded Client (enabled=true, token=\"\"); the
first Emit hits 401, handle401 re-registers, and the client recovers
without restarting the process.

Fixes §1.3 #9 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: observerclient 401 cooldown pauses dequeue (Bug #12)

**Files:**
- Modify: `internal/observerclient/client.go` — Client struct 加 cooldown 字段；run() 主循环加 gate
- Modify: `internal/observerclient/bootstrap.go` — handle401 包 setCooldown/clearCooldown
- Test: `internal/observerclient/client_test.go`

- [ ] **Step 1: 写失败测试**

`internal/observerclient/client_test.go` 末尾追加：

```go
// TestClient_CooldownPausesDequeueAndResumesAfter401Recovery pins the §1.3
// #12 invariant. During the 401 re-register cooldown window:
//   1. run() must NOT drain events from the queue (else they hit 401
//      again and silently drop after server fails).
//   2. After handle401 succeeds, run() must resume dequeue immediately,
//      not wait out the full cooldown.
func TestClient_CooldownPausesDequeueAndResumesAfter401Recovery(t *testing.T) {
    var posted int32 // increments per successful event POST
    var registerCalls int32
    var got401 atomic.Bool

    // observer that 401s first batch of events, then 200s after a successful
    // /api/agents/register call.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/api/agents/register":
            atomic.AddInt32(&registerCalls, 1)
            got401.Store(false)
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"token":"new-tok","workspace_id":"ws","agent_id":"a","role":"driver","display_name":"a"}`))
        case "/api/events":
            if !got401.Load() {
                // first event after register → succeed
                atomic.AddInt32(&posted, 1)
                w.WriteHeader(http.StatusOK)
                return
            }
            w.WriteHeader(http.StatusUnauthorized)
        default:
            t.Fatalf("unexpected: %s", r.URL.Path)
        }
    }))
    defer srv.Close()

    dir := t.TempDir()
    cfg := Config{
        Enabled:          true,
        TelemetryEnabled: true,
        URL:              srv.URL,
        WorkspaceID:      "ws",
        AgentID:          "a",
        AgentRole:        "driver",
        APIKey:           "ak",
        TokenStatePath:   filepath.Join(dir, "tok"),
        BootstrapTimeout: 2 * time.Second,
    }
    // Pre-write a stale token so bootstrap doesn't re-register before we
    // trip the 401.
    require.NoError(t, os.WriteFile(cfg.TokenStatePath, []byte("stale-tok"), 0o600))
    got401.Store(true) // first /api/events will 401

    c, err := New(cfg)
    require.NoError(t, err)
    defer c.Close()

    // Emit several events. The first triggers 401 → handle401 → register
    // (200) → cooldown cleared → subsequent posts succeed.
    for i := 0; i < 5; i++ {
        c.Emit(observer.Event{Type: "test", TaskID: fmt.Sprintf("e-%d", i)})
    }

    // Wait for posts to land.
    deadline := time.Now().Add(3 * time.Second)
    for time.Now().Before(deadline) && atomic.LoadInt32(&posted) < 5 {
        time.Sleep(50 * time.Millisecond)
    }
    if atomic.LoadInt32(&posted) < 5 {
        t.Fatalf("expected ≥5 successful posts after 401 recovery; got %d, registerCalls=%d",
            atomic.LoadInt32(&posted), atomic.LoadInt32(&registerCalls))
    }
    if atomic.LoadInt32(&registerCalls) == 0 {
        t.Fatal("handle401 should have re-registered at least once")
    }
}
```

Imports needed: `"net/http"`, `"net/http/httptest"`, `"sync/atomic"`, `"github.com/yourorg/multi-agent/internal/observer"`, `"github.com/stretchr/testify/require"`. Add if missing.

Note: `Emit` payload uses `observer.Event{Type, TaskID}`; if Emit only takes typed events, see existing TestNewColdStartRegistersAndEmits for the exact shape.

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/observerclient/ -run TestClient_CooldownPausesDequeue -v -count=1 -timeout 30s`
Expected: FAIL — currently cooldown drops events, posted < 5.

- [ ] **Step 3: 改 Client struct + helpers**

`internal/observerclient/client.go` 找到 `type Client struct {` (约 line 38)，在结尾加（在 mu / queue 等字段之后）：

```go
    cooldownMu    sync.Mutex
    cooldownUntil time.Time
```

在文件靠末尾位置（其它 helper 如 emit 旁边）加：

```go
// inCooldown returns the remaining cooldown duration, or 0 if not in
// cooldown. Used by run() to gate dequeue during a 401 re-register window.
func (c *Client) inCooldown() time.Duration {
    c.cooldownMu.Lock()
    defer c.cooldownMu.Unlock()
    if c.cooldownUntil.IsZero() {
        return 0
    }
    rem := time.Until(c.cooldownUntil)
    if rem <= 0 {
        c.cooldownUntil = time.Time{}
        return 0
    }
    return rem
}

// setCooldown puts run() into a quiet window where it does not dequeue
// events (so they don't waste post attempts that would 401 again while
// handle401 is mid-flight). Called at the start of handle401.
func (c *Client) setCooldown(d time.Duration) {
    c.cooldownMu.Lock()
    c.cooldownUntil = time.Now().Add(d)
    c.cooldownMu.Unlock()
}

// clearCooldown is called by handle401 on successful re-register so run()
// resumes dequeue immediately rather than waiting out the full window.
func (c *Client) clearCooldown() {
    c.cooldownMu.Lock()
    c.cooldownUntil = time.Time{}
    c.cooldownMu.Unlock()
}
```

确保 `sync` import 已在文件（用 `grep '"sync"' internal/observerclient/client.go` 查）。

- [ ] **Step 4: 改 run() 主循环**

`internal/observerclient/client.go` 找到 `func (c *Client) run() {`（grep `func (c \*Client) run`）。它通常长这样：

```go
for {
    select {
    case ev := <-c.queue:
        c.post(ctx, ev)
    case <-c.done:
        return
    }
}
```

在 `for {` 之后、`select` 之前加 cooldown gate：

```go
for {
    if rem := c.inCooldown(); rem > 0 {
        // 401 re-register in progress; do not dequeue (would 401 again
        // and silently drop the event). Wait out the window, then re-check.
        select {
        case <-time.After(rem):
        case <-c.done:
            return
        }
        continue
    }
    select {
    case ev := <-c.queue:
        c.post(ctx, ev)
    case <-c.done:
        return
    }
}
```

读 run() 的现有真实形状再嵌入；上面是模板。如有 flush channel / batching，同样保留。

- [ ] **Step 5: 改 handle401 加 setCooldown/clearCooldown**

`internal/observerclient/bootstrap.go` 找到 `func (c *Client) handle401(ctx context.Context) {` (约 line 146)。在函数顶部、`if c.proxyTokenMode { return }` 之后加：

```go
    // Pause run()'s dequeue while we re-register; otherwise queued events
    // get popped, hit 401 again, and silently drop after the server fails.
    c.setCooldown(reRegisterCoolDur)
```

在函数末尾（成功路径，`fmt.Fprintln(os.Stderr, "observerclient: ingest 401 → re-registered successfully")` 之前）加：

```go
    // Re-register succeeded — resume dequeue immediately rather than
    // waiting out the full cooldown window.
    c.clearCooldown()
```

Note: cooldown 失败路径（register 仍然 err）保留 cooldown 直到自然到期，下次 post 来时 inCooldown() 返 0 才再次尝试。

- [ ] **Step 6: 跑新测试看绿**

Run: `go test ./internal/observerclient/ -run TestClient_CooldownPausesDequeue -v -count=1 -timeout 30s`
Expected: PASS

- [ ] **Step 7: 全包回归 + race**

Run: `go test ./internal/observerclient/... -race -count=1 -timeout 60s`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/observerclient/client.go internal/observerclient/bootstrap.go internal/observerclient/client_test.go
git commit -m "fix(observerclient): cooldown pauses dequeue, resumes on register success

Previously, during the 60s re-register cooldown after a 401, the run()
loop kept dequeuing events — each one then hit 401 again, was logged as
cooldown-blocked, and silently dropped. A long batch of events at 401
time was lost wholesale.

Now run() checks inCooldown() before reading the queue; if positive,
sleeps the remaining window (interruptible via done) then re-checks.
handle401 sets the cooldown at the top and clears it on successful
re-register, so recovery resumes dequeue immediately rather than
waiting out the full window.

Fixes §1.3 #12 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: observerweb artifact/write half-failure logging + 502 (Bug #10)

**Files:**
- Modify: `internal/observerweb/server.go` — artifact PUT block (around line 480-498), write PUT block (around 660-682)
- Test: `internal/observerweb/server_test.go`

- [ ] **Step 1: 写失败测试**

`internal/observerweb/server_test.go` 末尾追加：

```go
// TestPutArtifact_DBFailureRollsBackObjectAnd502 pins the §1.3 #10
// invariant: when the DB MarkArtifactAvailable fails (not "row missing"
// but real DB error), the uploaded object MUST be rolled back AND the
// response MUST be 502 (not the misleading 404 we used to return).
func TestPutArtifact_DBFailureRollsBackObjectAnd502(t *testing.T) {
    // ... uses a fake store whose MarkArtifactAvailable always returns a
    // generic error (NOT ErrArtifactNotFound), and a fake objects.Store
    // that records all Put + Delete calls. Asserts response.StatusCode == 502
    // AND fakeObjects.deleted contains the key Put used.

    t.Skip("TODO: implementer fills in using existing observerweb test scaffolding " +
        "(see TestUploadArtifactContent / fakeObjects pattern)")
}

// TestPutArtifact_RowNotFoundStill404 verifies the existing 404 path is
// preserved when the failure is "wrong target" not "DB problem".
func TestPutArtifact_RowNotFoundStill404(t *testing.T) {
    t.Skip("TODO: implementer mirrors above but uses ErrArtifactNotFound")
}

// TestPutWrite_DBFailureRollsBackObjectAnd502 mirror for writes.
func TestPutWrite_DBFailureRollsBackObjectAnd502(t *testing.T) {
    t.Skip("TODO: implementer mirrors the artifact test for the write PUT path")
}
```

Implementer note: existing tests around `TestRegister*` show the httptest+fake-store pattern. Replace `t.Skip` once you know the fakes' shape. Acceptance criteria:
- DB error (NOT ErrArtifactNotFound/ErrWriteNotFound) → response status 502 + objects.Delete called for the key
- ErrArtifactNotFound/ErrWriteNotFound → response status 404 + objects.Delete called (same rollback) + body mentions "not found"
- Happy path → 200 + no Delete

- [ ] **Step 2: 跑红 (skip 不算红 — 真正的红要等 implementer 填完测试)**

Run: `go test ./internal/observerweb/ -run "TestPutArtifact_DBFailure|TestPutWrite_DBFailure|TestPutArtifact_RowNotFoundStill404" -v -count=1`
Expected: SKIP (placeholder)。实施者必须真填测试 — 见 Step 5 后再次跑应该 PASS。

- [ ] **Step 3: 改 PUT artifact 块**

`internal/observerweb/server.go` 找到 artifact PUT 块（约 line 486-498）：

```go
info, err := h.objects.Put(r.Context(), objectKey, r.Header.Get("Content-Type"), body)
if err != nil {
    _ = h.objects.Delete(r.Context(), objectKey)
    writeObjectProxyError(w, err)
    return
}
if err := h.s.MarkArtifactAvailable(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), info.SHA256, objectKey, info.Bytes); err != nil {
    _ = h.objects.Delete(r.Context(), objectKey)
    http.Error(w, err.Error(), http.StatusNotFound)
    return
}
```

替换 MarkArtifactAvailable 块为：

```go
if err := h.s.MarkArtifactAvailable(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), info.SHA256, objectKey, info.Bytes); err != nil {
    delErr := h.objects.Delete(r.Context(), objectKey)
    if delErr != nil {
        log.Printf("observer: ORPHAN OBJECT %s after DB MarkArtifactAvailable failed: db_err=%v delete_err=%v",
            objectKey, err, delErr)
    } else {
        log.Printf("observer: rolled back object %s after DB MarkArtifactAvailable failed: %v",
            objectKey, err)
    }
    if errors.Is(err, observerstore.ErrArtifactNotFound) {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    http.Error(w, err.Error(), http.StatusBadGateway)
    return
}
```

确保 `"errors"`, `"log"` 已 import.

- [ ] **Step 4: 改 PUT write 块**

同文件 line 660-682 的 write PUT 块对称改：

```go
if err := h.s.MarkWriteCompleted(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), info.SHA256, objectKey, info.Bytes); err != nil {
    delErr := h.objects.Delete(r.Context(), objectKey)
    if delErr != nil {
        log.Printf("observer: ORPHAN OBJECT %s after DB MarkWriteCompleted failed: db_err=%v delete_err=%v",
            objectKey, err, delErr)
    } else {
        log.Printf("observer: rolled back object %s after DB MarkWriteCompleted failed: %v",
            objectKey, err)
    }
    if errors.Is(err, observerstore.ErrWriteNotFound) {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    http.Error(w, err.Error(), http.StatusBadGateway)
    return
}
```

- [ ] **Step 5: 实施者填充 Step 1 的 placeholder 测试**

参考 observerweb 现有 fake-store / fake-objects 模式（搜 `fakeStore`, `fakeObjects` 在 server_test.go）。把 3 个 t.Skip 改成真测试。每个测试断言：response status code + fakeObjects.Delete 调用记录。

Run: `go test ./internal/observerweb/ -run "TestPutArtifact_DBFailure|TestPutWrite_DBFailure|TestPutArtifact_RowNotFoundStill404" -v -count=1`
Expected: 3 PASS

- [ ] **Step 6: 全包回归**

Run: `go test ./internal/observerweb/... -race -count=1`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/observerweb/server.go internal/observerweb/server_test.go
git commit -m "fix(observerweb): artifact/write upload half-failure is loud + returns 502

Previously: when objects.Put succeeded but DB MarkArtifactAvailable /
MarkWriteCompleted failed, code silently swallowed the objects.Delete
error and returned 404 — even if the DB was unreachable, not the row
missing. Two consequences: (1) orphan objects piled up in storage with
no audit trail; (2) the uploading client thought it had aimed at a
non-existent ID and never retried.

Now: object Delete failures are logged with the original DB error
context ('ORPHAN OBJECT' vs 'rolled back'); DB errors that are NOT
ErrArtifactNotFound/ErrWriteNotFound return 502 so clients distinguish
'wrong target' from 'server problem' and retry accordingly.

Fixes §1.3 #10 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: register duplicate guard + force opt-in (Bug #11)

**Files:**
- Modify: `internal/observerweb/server.go` — registerRequest struct + register handler (around line 1027-1140)
- Modify: `internal/observerclient/bootstrap.go` — register() pass force flag from Config
- Modify: `internal/observerclient/client.go` — Config 加 `ForceRegister bool`
- Test: `internal/observerweb/server_test.go`

- [ ] **Step 1: 写失败测试**

`internal/observerweb/server_test.go` 末尾追加：

```go
// TestRegister_RejectsRecentDuplicateWithoutForce pins the §1.3 #11 invariant:
// same agent_id within 5 minutes → 409 unless caller passes "force":true.
// This stops the double-driver-same-id mutual-eviction loop where a stray
// second process knocks the first off observer's auth without warning.
func TestRegister_RejectsRecentDuplicateWithoutForce(t *testing.T) {
    // Use the standard test server scaffolding (newTestServer or
    // newRegisterTestEnv — whichever observerweb's existing register tests
    // use; see TestRegisterSuccessAndIssuedTokenIngests for the pattern).
    //
    // 1) Register agent-1, get 200 + token-A.
    // 2) Immediately register the same agent-1 with NO force flag.
    //    Expect 409 + body mentions "force" and "recently".
    // 3) token-A must still validate (not rotated).

    t.Skip("TODO: implementer adapts using newTestServer pattern from TestRegisterSuccessAndIssuedTokenIngests")
}

// TestRegister_AcceptsRecentDuplicateWithForce verifies the explicit opt-in.
func TestRegister_AcceptsRecentDuplicateWithForce(t *testing.T) {
    // 1) Register agent-1, get 200 + token-A.
    // 2) Register agent-1 with {"force":true}, get 200 + token-B (rotated).
    // 3) token-A must NOT validate anymore (UpsertAgent overwrites token_hash).

    t.Skip("TODO: implementer adapts using newTestServer pattern")
}
```

Implementer: see `TestRegisterReissueInvalidatesOldToken` for the pattern that asserts the rotation behavior.

- [ ] **Step 2: 跑 skip (placeholder)**

Run: `go test ./internal/observerweb/ -run "TestRegister_RejectsRecentDuplicateWithoutForce|TestRegister_AcceptsRecentDuplicateWithForce" -v -count=1`
Expected: SKIP

- [ ] **Step 3: 改 registerRequest + register handler**

`internal/observerweb/server.go` 找到 `type registerRequest struct {` (约 line 1027)：

```go
type registerRequest struct {
    AgentID       string `json:"agent_id"`
    Role          string `json:"role"`
    DisplayName   string `json:"display_name"`
    WorkspaceID   string `json:"workspace_id"`
    WorkspaceName string `json:"workspace_name,omitempty"`
}
```

加一行：

```go
    Force         bool   `json:"force,omitempty"`
```

找到 register handler 里 `if existing, found, err := h.s.AgentBoundWorkspace(req.AgentID); err != nil {` 块（约 line 1102），**在那个 if 块之后、`token, err := mintAgentToken()` 之前**插入：

```go
    // Duplicate-takeover guard: refuse to rotate the token of an agent_id
    // that's still actively talking to observer, unless caller explicitly
    // opts in. Stops the double-driver-same-id mutual-eviction loop where
    // a stray second process silently knocks the first off observer's auth.
    if !req.Force {
        lastActive, hasLastActive, err := h.s.AgentLastActiveAt(req.WorkspaceID, req.AgentID)
        if err != nil {
            log.Printf("observer: AgentLastActiveAt error: %v", err)
            http.Error(w, "internal", http.StatusInternalServerError)
            return
        }
        if hasLastActive && time.Since(lastActive) < 5*time.Minute {
            http.Error(w,
                fmt.Sprintf("agent %s already registered recently (last activity %s ago); pass {\"force\":true} to take over",
                    req.AgentID, time.Since(lastActive).Round(time.Second)),
                http.StatusConflict)
            return
        }
    }
```

Check `"time"`, `"fmt"`, `"log"` already imported (most likely yes).

- [ ] **Step 4: 改 observerclient Config + register call**

`internal/observerclient/client.go` `type Config struct {` 加：

```go
    // ForceRegister tells the observer to rotate the token of an agent_id
    // that's still active. Default false → observer 409s on same-id within
    // 5min. Set true when the caller knows the prior process is dead /
    // intentionally being replaced.
    ForceRegister bool
```

`internal/observerclient/bootstrap.go` 找到 register() 调用（在 New() 路径下，搜 `register(...)`）。register() 函数定义大致是：

```go
func register(ctx context.Context, httpc *http.Client, url, apiKey, agentID, role, displayName, workspaceID, workspaceName string) (string, ..., error)
```

加一个 `force bool` 参数（最后一个或合理位置）。在 register() 内部 marshal body 时加入：

```go
body := registerRequestBody{
    AgentID:       agentID,
    Role:          role,
    DisplayName:   displayName,
    WorkspaceID:   workspaceID,
    WorkspaceName: workspaceName,
    Force:         force,
}
```

如果 `registerRequestBody` 是 inline anonymous struct，加 `Force bool \`json:"force,omitempty"\``.

调用点（loadOrRegister, handle401）传入 `cfg.ForceRegister`。

- [ ] **Step 5: 实施者填充 Step 1 的 placeholder 测试**

参考 `TestRegisterSuccessAndIssuedTokenIngests` (server_test.go:1116) 拿到 register 端点的 URL，POST 一个 request body，断言 status。

Run: `go test ./internal/observerweb/ -run "TestRegister_Rejects|TestRegister_Accepts" -v -count=1`
Expected: 2 PASS

- [ ] **Step 6: 既有 register 测试**

Run: `go test ./internal/observerweb/ -run "TestRegister" -v -count=1`
Expected: 既有所有 PASS。可能会 break：
- `TestRegisterReissueInvalidatesOldToken` (server_test.go:1231) — 测试连续两次 register 同 id，旧 token 失效。新行为：第二次会 409。需要修改这个测试改用 `Force: true`。
- `TestRegister_TokenRotation` (server_test.go:1325) — 同上。
- `TestRegister_NameStickyOnSecondRegister` (server_test.go:1302) — 同上。

逐个改：在第二次 register 的 body 里加 `"force": true`。

Run again expect all PASS.

- [ ] **Step 7: 全包 + observerclient 跨包测试**

Run: `go test ./internal/observerweb/... ./internal/observerclient/... -race -count=1 -timeout 60s`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/observerweb/server.go internal/observerweb/server_test.go internal/observerclient/client.go internal/observerclient/bootstrap.go
git commit -m "fix(observerweb): register requires force=true to take over recent agent_id

Previously, /api/agents/register let any caller with a valid api_key
rotate any agent_id's token silently. A stray second process — same
config copy-pasted onto a second VM, a CI runner that started a stale
slave-agent against ws-prod, etc. — would steal the auth from the live
one without warning. The live process then 401d, entered 60s cooldown,
re-registered, stole it back, and the two mutually evicted at ~1Hz.

Now: register defaults to 409 if the agent_id has activity in the last
5 minutes. To intentionally take over, the caller passes 'force':true
(via observerclient Config.ForceRegister). The honest pattern at
restart time is force=true (you know you replaced the prior process);
the dishonest mutual-eviction pattern is rejected by default.

Existing TokenRotation/Reissue/NameSticky tests updated to pass force.

Fixes §1.3 #11 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: BlobStore.Put atomic + ObjectBlobStore drop hasBlobPath (Bug #13)

**Files:**
- Modify: `internal/userspace/blob.go` — Put 改 tx + INSERT OR IGNORE；upsertObjectBlob 去 hasBlobPath 写分支
- Test: `internal/userspace/blob_test.go`（可能需新建；查 `ls internal/userspace/*_test.go`）

- [ ] **Step 1: 查 blob test 文件**

Run: `ls internal/userspace/*_test.go && grep -l "BlobStore" internal/userspace/*_test.go`

如果没有 blob_test.go，按下一步新建。

- [ ] **Step 2: 写失败测试**

把以下内容 append 到现有 `internal/userspace/blob_test.go`（若存在）或写成新文件：

```go
package userspace

import (
    "sync"
    "sync/atomic"
    "testing"
)

// TestBlobStore_ConcurrentPutSameSha_NoDoubleWrite pins the §1.3 #13(a)
// invariant: N goroutines Put(same content) must produce ONE blob file
// on disk and a final refcount == N.
func TestBlobStore_ConcurrentPutSameSha_NoDoubleWrite(t *testing.T) {
    db := newTestDB(t)
    if err := Migrate(db); err != nil {
        t.Fatalf("migrate: %v", err)
    }
    bs := NewBlobStore(db, t.TempDir())
    content := []byte("hello-world-blob-content")
    const N = 20
    var wg sync.WaitGroup
    var failures int32
    var hexsum string
    var hexsumMu sync.Mutex
    for i := 0; i < N; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            h, err := bs.Put(content)
            if err != nil {
                atomic.AddInt32(&failures, 1)
                t.Logf("put err: %v", err)
                return
            }
            hexsumMu.Lock()
            hexsum = h
            hexsumMu.Unlock()
        }()
    }
    wg.Wait()
    if failures > 0 {
        t.Fatalf("concurrent Put failures: %d (must be 0)", failures)
    }
    // Final refcount must equal N.
    var rc int
    if err := db.QueryRow(
        `SELECT refcount FROM userspace_blobs WHERE sha256=?`, hexsum).Scan(&rc); err != nil {
        t.Fatalf("refcount query: %v", err)
    }
    if rc != N {
        t.Fatalf("expected refcount=%d, got %d (TOCTOU: concurrent Put didn't atomically bump)", N, rc)
    }
}
```

Imports for the test: `"sync"`, `"sync/atomic"`. The test relies on `newTestDB`, `Migrate`, `NewBlobStore` existing (they do — newTestDB at migrate_test.go:11).

If `NewBlobStore` requires different args, check `internal/userspace/blob.go` for the constructor signature.

- [ ] **Step 3: 跑红**

Run: `go test ./internal/userspace/ -run TestBlobStore_ConcurrentPutSameSha -v -count=1 -race`
Expected: FAIL — either failures > 0 OR refcount != N (TOCTOU manifesting under -race)

- [ ] **Step 4: 改 BlobStore.Put**

`internal/userspace/blob.go` `func (b *BlobStore) Put(content []byte) (string, error) {` 整段替换为：

```go
// Put writes content if it's not already stored, otherwise bumps the
// existing row's refcount. Atomic against concurrent Put(same content):
// INSERT OR IGNORE inside a tx picks exactly one winner that writes the
// file; losers UPDATE refcount instead.
func (b *BlobStore) Put(content []byte) (string, error) {
    sum := sha256.Sum256(content)
    hexsum := hex.EncodeToString(sum[:])
    path := b.pathFor(hexsum)

    tx, err := b.db.Begin()
    if err != nil {
        return "", err
    }
    defer tx.Rollback()

    res, err := tx.Exec(`
        INSERT OR IGNORE INTO userspace_blobs(sha256, size_bytes, blob_path, refcount, created_at)
        VALUES(?, ?, ?, 1, ?)`,
        hexsum, len(content), filepath.Join(blobShard(hexsum), hexsum), nowUTC())
    if err != nil {
        return "", err
    }
    inserted, _ := res.RowsAffected()
    if inserted == 0 {
        // Loser: row already existed. Bump refcount.
        if _, err := tx.Exec(
            `UPDATE userspace_blobs SET refcount = refcount + 1 WHERE sha256=?`, hexsum); err != nil {
            return "", err
        }
        return hexsum, tx.Commit()
    }
    // Winner: we own this row, write the content file. WriteFile sits
    // before Commit so a write failure rolls back the row (defer Rollback
    // takes care of it via the early return path).
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return "", err
    }
    if err := os.WriteFile(path, content, 0o644); err != nil {
        return "", err
    }
    if err := tx.Commit(); err != nil {
        _ = os.Remove(path)
        return "", err
    }
    return hexsum, nil
}
```

- [ ] **Step 5: 改 upsertObjectBlob 去 hasBlobPath 双写**

同文件找到 `func (b *ObjectBlobStore) upsertObjectBlob(...)` (约 line 327)。替换为：

```go
func (b *ObjectBlobStore) upsertObjectBlob(hexsum string, sizeBytes int, key string) error {
    // Canonical write path uses object_key only. The legacy hasBlobPath
    // dual-write was an unfinished migration that materialized duplicate
    // columns into user data; new writes drop it. Reads still tolerate
    // the legacy blob_path column (see Open).
    _, err := b.db.Exec(`
        INSERT INTO userspace_blobs(sha256, size_bytes, object_key, refcount, created_at)
        VALUES($1, $2, $3, 1, $4)
        ON CONFLICT(sha256) DO UPDATE SET
            size_bytes = excluded.size_bytes,
            object_key = excluded.object_key,
            refcount = CASE
                WHEN userspace_blobs.refcount > 0 THEN userspace_blobs.refcount + 1
                ELSE 1
            END`,
        hexsum, sizeBytes, key, nowUTC())
    return err
}
```

`b.hasBlobPath` field 保留（read path 仍参考），但不再驱动写 schema。

- [ ] **Step 6: 跑新测试看绿**

Run: `go test ./internal/userspace/ -run TestBlobStore_ConcurrentPutSameSha -v -count=1 -race -timeout 30s`
Expected: PASS, refcount == 20

- [ ] **Step 7: 全包回归**

Run: `go test ./internal/userspace/... -race -count=1 -timeout 60s`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/userspace/blob.go internal/userspace/blob_test.go
git commit -m "fix(userspace): BlobStore.Put atomic; ObjectBlobStore drops dual-write

(a) BlobStore.Put used SELECT → branch → INSERT/UPDATE with no tx. Two
goroutines Put(same content) both hit ErrNoRows, both WriteFile (same
sha so same path — second clobbers first concurrently, possibly
mid-write), both INSERT, one fails UNIQUE. The win is that under -race
the test now manifests the race; the fix is INSERT OR IGNORE + tx so
exactly one winner WriteFile's, losers UPDATE refcount.

(b) ObjectBlobStore.upsertObjectBlob's hasBlobPath branch wrote \$3 into
BOTH object_key and blob_path on every insert — an unfinished schema
migration silently materializing duplicate columns into production data.
Drop the dual-write; the read path's hasBlobPath check still tolerates
the legacy column.

Fixes §1.3 #13 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: 最终全量回归 + 跨包构建 + e2e 准备

- [ ] **Step 1: 全模块构建**

Run: `go build ./...`
Expected: clean

- [ ] **Step 2: 全模块 vet**

Run: `go vet ./...`
Expected: clean

- [ ] **Step 3: 全模块测试 + race**

Run: `go test ./... -race -count=1 -timeout 600s`
Expected: PASS

- [ ] **Step 4: 对照 spec 覆盖**

| Spec § | Plan task | Commit |
|---|---|---|
| §1.3 #9 bootstrap timeout | Task 2 | (sha) |
| §1.3 #10 artifact/write 502 + log | Task 4 | (sha) |
| §1.3 #11 register force | Task 5 | (sha) |
| §1.3 #12 cooldown gate | Task 3 | (sha) |
| §1.3 #13 BlobStore + ObjectBlob | Task 6 | (sha) |
| 基础（sentinels + AgentLastActiveAt） | Task 1 | (sha) |

确认无遗漏。

- [ ] **Step 5: `git log --oneline master..HEAD`**

应有 1 docs (spec, 已 commit) + 6 fix commits = 7 commits 在 worktree-fix-control-plane-1.3 上。

---

## 验证清单

- [ ] `go build ./...` 成功
- [ ] `go vet ./...` 干净
- [ ] `go test ./... -race -count=1` 全绿
- [ ] 新增测试均 PASS：
  - `TestMarkArtifactAvailableReturnsErrArtifactNotFound`
  - `TestMarkWriteCompletedReturnsErrWriteNotFound`
  - `TestAgentLastActiveAt_Unknown` / `_Known`
  - `TestNewBootstrapTimeoutDegradesToEmptyToken`
  - `TestNewBootstrapTimeoutDefaultsTo5s`
  - `TestClient_CooldownPausesDequeueAndResumesAfter401Recovery`
  - `TestPutArtifact_DBFailureRollsBackObjectAnd502`
  - `TestPutWrite_DBFailureRollsBackObjectAnd502`
  - `TestPutArtifact_RowNotFoundStill404`
  - `TestRegister_RejectsRecentDuplicateWithoutForce`
  - `TestRegister_AcceptsRecentDuplicateWithForce`
  - `TestBlobStore_ConcurrentPutSameSha_NoDoubleWrite`
- [ ] 既有 register 测试（TokenRotation/Reissue/NameSticky）适配 `force=true` 后仍 PASS
- [ ] e2e（按 `[[e2e_prod_test_codex_local]]` runbook）：起 driver+slave，跑基础 4 步；额外验证 (a) observer kill 后 driver 仍能起；(b) register 同 id 直接 force 重启可工作
