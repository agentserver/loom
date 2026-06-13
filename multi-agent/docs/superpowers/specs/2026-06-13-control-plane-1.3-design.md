# Control Plane 一致性修复 (审查报告 §1.3) — 设计文档

- **日期**：2026-06-13
- **来源**：`docs/review-2026-06-13.md` §1.3（CRITICAL #9–#13）
- **范围**：observerclient / observerweb / observerstore / userspace.blob 5 个 control-plane bug
- **不在范围**：master 路径（[[master_path_frozen]]）；server-side token push channel；完整 outbox pattern

## 目标不变量

修复后下列五条必须始终成立：

1. **observer 抖动不让 driver/slave 启动死循环** — bootstrap 超时降级，token 后续 401 自动恢复（不让 jetson/HPC 等无 systemd 兜底的部署陷死循环）
2. **observer 端 artifact/write 上传失败不留静默孤儿** — object Delete 必尝试，失败显式 log + audit；DB 真挂时返 5xx 而非误导的 404
3. **同 agent_id 重复 register 不静默抢占** — 默认 5min 内重复 → 409 拒绝；要 takeover 必须显式 `force=true`，杜绝双 driver 同 id 互踢
4. **observerclient 401 cooldown 期间不丢事件** — handle401 期间 run() 暂缓 dequeue，重 register 成功后恢复消费
5. **userspace BlobStore 并发 Put 同 sha 不重复写** — INSERT OR IGNORE + 事务 + ON CONFLICT；Object 路径移除 hasBlobPath 双写

## 变更摘要

### Bug #9 — observerclient.New 启动阻塞 → 5s timeout 降级

**位置**：`internal/observerclient/client.go:60-88`

**问题**：New() 调 `loadOrRegister(context.Background())` 同步无超时；observer 抖一下整个 slave/driver 起不来。jetson/HPC 无 systemd `Restart=on-failure` 兜底 → 死循环。

**修复**：

```go
type Config struct {
    // ... existing fields ...
    BootstrapTimeout time.Duration // default 5s; 0 → use default; <0 → wait forever (legacy)
}

func New(cfg Config) (*Client, error) {
    // ... existing fields setup ...
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
        // Degraded mode: keep enabled=true so Emit() still queues; first Emit
        // hits 401 → handle401 path takes over and re-registers when observer
        // recovers. This is far better than failing process startup on a
        // transient observer outage on jetson/HPC where there's no systemd
        // restart to recover from a hard exit.
        fmt.Fprintf(os.Stderr,
            "observerclient: bootstrap failed (%v); entering degraded mode — "+
                "events will queue and post once token is acquired\n", err)
        c.token = ""
    } else {
        c.token = tok
    }
    // ... rest unchanged ...
}
```

**注意**：`post()` 在 token 为空时直接 401（observer 端要求 Bearer token），触发 `handle401` 路径，已经在 cooldown 内有限重试。Degraded mode 不破坏现有恢复路径。

### Bug #10 — observerweb artifact/write 上传半失败

**位置**：`internal/observerweb/server.go:480-498, 660-682`

**问题**：先 `objects.Put` 成功 → `MarkArtifactAvailable/MarkWriteCompleted` 失败 → `_ = h.objects.Delete(...)` 静默吞错，DB 又返 404（误导，实际是 DB 错）。

**修复**（最小代价 — 不引入 outbox/pending state）：

```go
if err := h.s.MarkArtifactAvailable(agent.WorkspaceID, agent.ID, id, ...); err != nil {
    delErr := h.objects.Delete(r.Context(), objectKey)
    if delErr != nil {
        log.Printf("observer: ORPHAN OBJECT %s after DB MarkArtifactAvailable failed: db_err=%v delete_err=%v",
            objectKey, err, delErr)
    } else {
        log.Printf("observer: rolled back object %s after DB MarkArtifactAvailable failed: %v",
            objectKey, err)
    }
    // If MarkArtifactAvailable failed because the row doesn't exist (race or
    // wrong agent), return 404. Otherwise the error is server-side (DB
    // unreachable, constraint violation) — return 502 so the client doesn't
    // silently retry under the wrong error class.
    if errors.Is(err, observerstore.ErrArtifactNotFound) {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    http.Error(w, err.Error(), http.StatusBadGateway)
    return
}
```

Write path (lines 660-682) 同改。需要：
- `observerstore.ErrArtifactNotFound` / `ErrWriteNotFound` sentinel 新增
- `MarkArtifactAvailable` / `MarkWriteCompleted` 在 row 不存在时返这两个 sentinel（rows_affected==0）

### Bug #11 — register 同 id 重复 upsert 不撤旧 token

**位置**：`internal/observerweb/server.go:1100-1140` + `internal/observerstore/store.go:245`

**问题**：`/api/agents/register` 允许同 workspace 同 id 反复 upsert；前一个 token 被覆盖即失效，但**老进程仍在用旧 token Emit → 401 cooldown 死循环**。配合 #12，事件丢光。

**修复**：register 默认拒绝最近 active 的 agent_id；用户须显式 `force=true` takeover。

```go
type registerRequest struct {
    AgentID       string `json:"agent_id"`
    Role          string `json:"role"`
    DisplayName   string `json:"display_name"`
    WorkspaceID   string `json:"workspace_id"`
    WorkspaceName string `json:"workspace_name,omitempty"`
    Force         bool   `json:"force,omitempty"`  // NEW: opt-in takeover
}

// In register handler, after AgentBoundWorkspace check:
if !req.Force {
    lastActive, found, err := h.s.AgentLastActiveAt(req.WorkspaceID, req.AgentID)
    if err != nil {
        log.Printf("observer: AgentLastActiveAt error: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    if found && time.Since(lastActive) < 5*time.Minute {
        http.Error(w,
            fmt.Sprintf("agent %s already registered recently (last activity %s ago); pass {\"force\":true} to take over",
                req.AgentID, time.Since(lastActive).Round(time.Second)),
            http.StatusConflict)
        return
    }
}
// ... existing UpsertWorkspaceLazy + UpsertAgent ...
```

需要：
- `observerstore.AgentLastActiveAt(workspaceID, agentID) (time.Time, bool, error)` — 查 `agents.last_seen_at` 或近似列；如果 schema 没有，用 `agents.updated_at` 退化，或 `events.MAX(ts) WHERE agent_id=?` 取近似。
- 若 schema 没有可用列，**简化**：用 `agents.created_at < 5min ago` 拒绝（粗糙但够用）。具体实现见 plan task。

observerclient side: bootstrap.go `register()` 拿到 409 时打印 stderr `observerclient: bootstrap rejected — agent recently active; if you intend to take over, set Config.ForceRegister=true` → 加 `Config.ForceRegister bool` 透传到 register call。

### Bug #12 — 401 cooldown 期间 run() 继续 dequeue 丢事件

**位置**：`internal/observerclient/client.go` (`run` 函数) + `bootstrap.go:handle401`

**问题**：401 cooldown 内 post() 继续被调，继续 401，继续被 cooldown 拦截 → 事件 silently drop。

**修复**：

```go
// Client struct add:
type Client struct {
    // ... existing ...
    cooldownMu    sync.Mutex
    cooldownUntil time.Time
}

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

func (c *Client) setCooldown(d time.Duration) {
    c.cooldownMu.Lock()
    c.cooldownUntil = time.Now().Add(d)
    c.cooldownMu.Unlock()
}

func (c *Client) clearCooldown() {
    c.cooldownMu.Lock()
    c.cooldownUntil = time.Time{}
    c.cooldownMu.Unlock()
}

// In run() main loop, BEFORE attempting to read from queue:
for {
    if rem := c.inCooldown(); rem > 0 {
        select {
        case <-time.After(rem):
        case <-c.done:
            return
        }
        continue
    }
    // existing select on c.queue, c.flush, c.done ...
}

// In handle401, after detecting 401 (or at start):
c.setCooldown(reRegisterCoolDur)
// ... existing register call ...
if err == nil {
    c.clearCooldown()  // success → resume dequeue immediately
}
// failure → cooldown stays, expires naturally, next post() retries
```

效果：cooldown 期间事件停留在队列；handle401 成功立刻 resume；失败 cooldown 自然到期后 retry。**关键**：cooldown 期间 queue 满会 drop（现有 buffer 128 行为），不会变更——但 cooldown 缩短到 register attempt 实际耗时，比之前"5s drop 整个 cooldown"明显好。

### Bug #13 — BlobStore 并发 Put TOCTOU + ObjectBlobStore hasBlobPath 双写

**位置**：`internal/userspace/blob.go:51-81, 315-360`

**问题 (a)** SQLite BlobStore.Put: SELECT → 分支 → INSERT/UPDATE 不在事务里。并发同 sha 都进 ErrNoRows 分支 → 都 WriteFile → 都 INSERT 撞 UNIQUE。第二个 INSERT 失败 + 文件已被它覆盖。

**问题 (b)** ObjectBlobStore.upsertObjectBlob.hasBlobPath 路径把 `$3` 同时写进 `object_key` 和 `blob_path`，schema 迁移没收尾。

**修复 (a)**：用 `INSERT OR IGNORE` + 事务：

```go
func (b *BlobStore) Put(content []byte) (string, error) {
    sum := sha256.Sum256(content)
    hexsum := hex.EncodeToString(sum[:])
    path := b.pathFor(hexsum)

    tx, err := b.db.Begin()
    if err != nil {
        return "", err
    }
    defer tx.Rollback()

    // Atomic claim. If we win, rowsAffected==1 → we write the file. Otherwise
    // someone else inserted the row first; we just bump their refcount.
    res, err := tx.Exec(`
        INSERT OR IGNORE INTO userspace_blobs(sha256, size_bytes, blob_path, refcount, created_at)
        VALUES(?, ?, ?, 1, ?)`,
        hexsum, len(content), filepath.Join(blobShard(hexsum), hexsum), nowUTC())
    if err != nil {
        return "", err
    }
    inserted, _ := res.RowsAffected()
    if inserted == 0 {
        // Already exists; bump refcount.
        if _, err := tx.Exec(
            `UPDATE userspace_blobs SET refcount = refcount + 1 WHERE sha256=?`, hexsum); err != nil {
            return "", err
        }
        return hexsum, tx.Commit()
    }
    // We won the insert; write the file outside the tx-Commit window to keep
    // tx short, but inside tx-Rollback semantics (if writeFile fails we rollback).
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

**修复 (b)**：ObjectBlobStore.upsertObjectBlob 移除 hasBlobPath 双写分支。读取路径 (Open) 仍兼容 hasBlobPath schema（向后兼容旧 DB），但**写入只用 object_key**：

```go
func (b *ObjectBlobStore) upsertObjectBlob(hexsum string, sizeBytes int, key string) error {
    // Single canonical write path: object_key only. The legacy hasBlobPath
    // dual-write was an unfinished migration that silently materialized as
    // duplicate columns in user data; new writes drop it. Reads still
    // tolerate the legacy column (see Open).
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

注意：`b.hasBlobPath` 字段保留但不再驱动写路径；可在 follow-up PR 加 explicit `RemoveBlobPathColumn()` migration（不在本 PR 范围）。

## 测试策略

| # | 测试 | 包 | 类型 |
|---|---|---|---|
| 1 | `TestObserverClient_New_BootstrapTimeoutDegradesToEmptyToken` | observerclient | unit |
| 2 | `TestObserverClient_New_BootstrapTimeoutConfigurable` | observerclient | unit |
| 3 | `TestObserverClient_DegradedModeRecoversAfterRegister` | observerclient | unit (fake observer) |
| 4 | `TestObserverWeb_PutArtifactDBFailureRollsBackObject` | observerweb | unit (fake objects.Store) |
| 5 | `TestObserverWeb_PutWriteDBFailureRollsBackObject` | observerweb | unit |
| 6 | `TestObserverWeb_RegisterRejectsRecentDuplicateWithoutForce` | observerweb | unit |
| 7 | `TestObserverWeb_RegisterAcceptsRecentDuplicateWithForce` | observerweb | unit |
| 8 | `TestObserverClient_401CooldownPausesDequeue` | observerclient | unit (timing + counter) |
| 9 | `TestBlobStore_ConcurrentPutSameSha256_NoDoubleWrite` | userspace | unit (sync.WaitGroup) |
| 10 | `TestObjectBlobStore_UpsertOnlyWritesObjectKey` | userspace | unit |

回归：`go test ./internal/observerclient/... ./internal/observerweb/... ./internal/observerstore/... ./internal/userspace/... -race -count=1`

## 兼容性

| 变更 | 影响 |
|---|---|
| `observerclient.Config.BootstrapTimeout` 新字段 | 加新（zero 用 5s 默认）；老 caller 不变 |
| `Config.ForceRegister` 新字段 | 加新（默认 false → safer）；显式 takeover 需 opt-in |
| New() 不在 bootstrap 失败时返 err | 行为变化：以前 caller `if err != nil { log.Fatal }` 会触发；现在不返 err → 不 Fatal。**正是修复意图**。caller 看 stderr 警告知道 degraded |
| `registerRequest.Force` 新字段 | 默认 false → 行为变化：以前同 id < 5min 静默 takeover；现在 409 拒绝 |
| `MarkArtifactAvailable`/`MarkWriteCompleted` 返回 sentinel 区分 | 加新 ErrArtifactNotFound/ErrWriteNotFound；旧 caller 不变（仍可用 generic err） |
| 失败 HTTP 状态码 404 → 502（DB 错时） | 行为变化：observerclient/uploader 看到 502 应触发重试；404 仍是"row 真不存在" |
| `BlobStore.Put` 改事务 + INSERT OR IGNORE | 语义更严，老 caller 不变 |
| `ObjectBlobStore.upsertObjectBlob` 去掉 hasBlobPath 分支 | 老 DB 仍可读（Open 还认 blob_path）；新写不再填该列 |

## 不变项 / 反目标

- 不引入 server-side push channel for token revocation
- 不实现完整 outbox pattern for #10（仅"失败显式可见"）
- 不动 schema（不加 column, 不 ALTER）
- 不动 tunnel / poller
- 不改 observer 端协议 envelope shape
- 不动 master 相关路径（cmd/master-agent / internal/orchestrator / internal/orchestration），符合 [[master_path_frozen]]
