# Design — Wire `observerstore.WriteSnapshot` into `driver.dry_run_contract` via observer HTTP relay

**Date**: 2026-07-07
**Owner**: paper/v3/fix-81-writesnap worktree
**Base**: multi-agent `origin/paper/v3-integration` @ `86bc2fd`
**Fixes**: [agentserver/loom#81](https://github.com/agentserver/loom/issues/81)
**Revision**: v2 — 应 codex round-1 审核修 3 P0 + 1 P1；scope 扩到 observer-server 侧 HTTP 端点 + relay 方法。

---

## 1. Problem statement

`internal/observerstore.WriteSnapshot`（`capability_snapshots_writer.go:80`）+ 附带的 `capability_snapshot_usages` upsert 逻辑齐备且被单测覆盖，但**整个 loom 仓库没有 non-test 调用者**。因此 `capability_snapshots` / `capability_snapshot_usages` 两表在 real deploy 里恒空。

`internal/driver/capability_tools.go:275` 的 `dryRunContractTool.Call` 计算了 `snapHash = capability.ComputeHash(snap)`（**注意**：这个 hash 只存进 `DryRunBlockRow.CapabilitySnapshotHash` 字段——`dryRunReport` 本身不 expose；只在有 block 时通过 `dry_run_blocks` 表间接落盘）—— 但 `Snapshot` body 与 `capability_snapshot_usages` 均**未持久化**。

**下游影响面**：
- D2 metric-extract (PR #68) 从 `capability_snapshots` 抽 hash 分布 → 0 rows
- Phase 3 real-run worktree 无法从 observer DB 计算 per-agent capability 复用率
- Phase 1 close-out memo (`docs/intermediate/14_phase1_closeout.md`) 声称 per-agent attribution 生效——只在单测层成立

## 2. Non-goals

1. **不重写** `WriteSnapshot`——它已有 secret-scan / ablation guard / canonical JSON hash / dedup / cross-agent attribution，通过完整单测。
2. **不改** `Snapshot` schema、`ComputeHash`、`schema.sql` 表定义。
3. **不修** `inspect_capabilities`——它 write 的是 `contract.ResourceSnapshot` 到 `resource_snapshots` 表，不同 shape。
4. **不修** issue #82（slave dispatch chain）—— 独立 worktree。

## 3. Security-first considerations

用户明确要求安全优先。逐条：

| 面 | 措施 |
|---|---|
| **secret 泄漏** | `WriteSnapshot` 已内含 `capability.JSONContainsRawToken(body)`；observer-side handler 在收到 `ErrSnapshotContainsSecret` 时**返 422 Unprocessable Entity**（不 400，因为语义是"合法 JSON 但含 secret"），driver 侧 relay 把 422 归到 warning 且**redact 原 error text**（固定文案 `"observer save capability snapshot: rejected (secret scan)"`）。 |
| **ablation bypass** | **Double guard (v4+)**: 分裂 deploy 场景（driver flag=disabled 而 observer flag=enabled）会 bypass 单点 guard。因此**必须**在 driver-side（发出 relay 前，`capability.IsUploadDisabled()`）**和** observer-side（`WriteSnapshot` 内置 short-circuit）**都**做 guard。两处都是 primary（不可省略）—— defense in depth for cross-process flag drift。详见 §4.4 ablation interplay 段与 §7 threat model。 |
| **未鉴权持久化** | observer-side handler 复用 `dryRunBlocks` 的 `h.authenticate(w, r) → agent.Role in (Driver|Master)` gate；**attribution key 强制**用 `agent.ID + agent.WorkspaceID`（来自 observer 侧 authenticate 得到的可信身份），**不接受**请求 body 里 caller-provided `agent_id/workspace_id`（防伪造）。 |
| **body size DoS** | 沿用 dryRunBlocks 的 `http.MaxBytesReader(w, r.Body, h.maxEventBodyBytes)`（default 256 KiB, configurable via Options）；同时 driver-relay 侧 pre-check `len(snap.CanonicalJSON) < 256 KiB - safety_margin`，防不合法大 snapshot 发出去后被 observer 拒。**cardinality DoS**：每 unique snapshot 落一行；防线：Phase 3 real-run 每 driver-invocation < 100 unique snapshots，SQLite `capability_snapshots` 表 hash PK dedup。**不加行数上限**（会破坏 attribution 时间线）；文档 §8 记录 Limitation。 |
| **persist raw error text** | observer-side handler 沿用 dryRunBlocks 的做法：任何 write 错误 log 到 server log + 返固定文案 `"failed to persist capability_snapshot"`；**不**把 driver / sqlite 内部 error 拼到 HTTP body。 |
| **持锁调 DB** | driver 侧的 write 是通过 `http.Client` (relay) 完成，异步不持锁；observer 侧 `WriteSnapshot` 内部事务无外部锁。 |
| **error 升级** | 现有 `dryRunWriter.WriteDryRunBlock` 失败**降级为 warning**（`capability_tools.go` 保持返 200）。新的 `capabilitySnapshotWriter.WriteCapabilitySnapshot` 走**同款语义**：任何 relay 错误 → append 到 `warnings []string`，落 `t.logHelperErr("observer_snapshot", ...)`，dry-run 主返回值不变。 |

## 4. Architecture

### 4.1 三层新增

| 层 | 加什么 | 文件 |
|---|---|---|
| **observer-server HTTP** | `POST /api/capability-snapshots` handler | `internal/observerweb/server.go` (+~80 行，mirror `dryRunBlocks`) |
| **driver → observer relay** | `WriteCapabilitySnapshot(ctx, snap capability.Snapshot) error` 方法 | `internal/driver/observer_relay.go` (+~50 行，mirror `WriteDryRunBlock`) |
| **driver call site** | `dryRunContractTool.Call` 里 hash 后调 relay 方法 | `internal/driver/capability_tools.go` (~+25 行) |

### 4.2 observer-side handler (§4.1 first row)

```go
// internal/observerweb/server.go — new route
mux.HandleFunc("/api/capability-snapshots", h.capabilitySnapshots)

func (h *handler) capabilitySnapshots(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    agent, ok := h.authenticate(w, r)
    if !ok { return }
    if agent.Role != observer.RoleDriver && agent.Role != observer.RoleMaster {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }
    // Backend discrimination — same pattern as dryRunBlocks (line 1036)
    // Postgres: nothing wired yet for capability_snapshots (future work); reject 503.
    managed, ok := h.s.(observerstore.ManagedStore)
    if !ok {
        http.Error(w, "capability_snapshots endpoint requires ManagedStore-backed store", http.StatusServiceUnavailable)
        return
    }
    db := managed.DB()

    r.Body = http.MaxBytesReader(w, r.Body, h.maxEventBodyBytes)
    var req struct {
        Snapshot json.RawMessage `json:"snapshot"`  // driver marshals capability.Snapshot into here
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        var maxBytesErr *http.MaxBytesError
        if errors.As(err, &maxBytesErr) {
            http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
            return
        }
        http.Error(w, "bad json", http.StatusBadRequest)
        return
    }
    if len(req.Snapshot) == 0 {
        http.Error(w, "snapshot field required", http.StatusBadRequest)
        return
    }
    var snap capability.Snapshot
    if err := json.Unmarshal(req.Snapshot, &snap); err != nil {
        http.Error(w, "invalid snapshot json", http.StatusBadRequest)
        return
    }
    // Reconstruct through NewSnapshot to canonicalise + validate — same
    // guarantees `dryRunContractTool` gets before hashing.
    canon, err := capability.NewSnapshot(snap)
    if err != nil {
        // Snapshot shape invariants violated. NEVER log err.Error() with %v —
        // NewSnapshot echoes attacker-controlled field values (e.g. bad OS
        // string) that may contain raw tokens (fix v4 P1-1 secret-leak).
        // Log only a fixed classifier; the exact fields are recoverable from
        // client-side traces if needed.
        log.Printf("[capability_snapshots] NewSnapshot rejected snapshot from agent=%s ws=%s (shape invariant)", agent.ID, agent.WorkspaceID)
        http.Error(w, "invalid snapshot shape", http.StatusUnprocessableEntity)
        return
    }
    // Attribution comes from AUTHENTICATED agent (spec §3 security);
    // caller-provided fields in `snap` cannot forge attribution.
    if err := observerstore.WriteSnapshot(r.Context(), db, agent.ID, agent.WorkspaceID, canon); err != nil {
        if errors.Is(err, observerstore.ErrSnapshotContainsSecret) {
            // 422 — request understood, contents refused. Fixed text.
            http.Error(w, "snapshot contains raw token; rejected by secret scan", http.StatusUnprocessableEntity)
            return
        }
        log.Printf("[capability_snapshots] write failed: %v", err)
        http.Error(w, "failed to persist capability_snapshot", http.StatusInternalServerError)
        return
    }
    w.WriteHeader(http.StatusNoContent)
}
```

### 4.3 driver-side relay (§4.1 second row)

```go
// internal/driver/observer_relay.go — new method + wire type

// WriteCapabilitySnapshot POSTs a canonical capability.Snapshot to
// observer-server's /api/capability-snapshots endpoint. nil relay ⇒
// silent no-op (matches SaveResourceSnapshot / WriteDryRunBlock contract).
//
// Attribution (agent_id / workspace_id) is derived by observer-side
// authenticate; caller has no way to forge it (spec §3 security).
func (r *ObserverRelay) WriteCapabilitySnapshot(ctx context.Context, snap capability.Snapshot) error {
    if r == nil {
        return nil
    }
    // Pre-check size to fail fast; observer would 413 otherwise.
    body, err := capability.CanonicalJSON(snap)
    if err != nil {
        return fmt.Errorf("canonicalize snapshot: %w", err)
    }
    const observerBodyCap = 256 * 1024
    if len(body) > observerBodyCap {
        return fmt.Errorf("snapshot body %d bytes exceeds observer cap %d", len(body), observerBodyCap)
    }
    payload, _ := json.Marshal(struct {
        Snapshot json.RawMessage `json:"snapshot"`
    }{Snapshot: body})

    req, err := http.NewRequestWithContext(ctx, http.MethodPost,
        r.baseURL+"/api/capability-snapshots", bytes.NewReader(payload))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")
    if err := r.attach(ctx, req); err != nil {  // same auth attach helper existing methods use
        return err
    }
    resp, err := r.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusNoContent {
        return nil
    }
    b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
    return fmt.Errorf("observer capability_snapshots status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}
```

⚠️ Plan phase: verify actual helper names (`r.attach`, `r.client`, `r.baseURL`) match the current `observer_relay.go`; adapt to existing patterns.

### 4.4 driver call site (§4.1 third row)

**关键**：位置在 `capability_tools.go:275` `snapHash = capability.ComputeHash(snap)` 后、`report.Blocks = blocks` 前，**必须**在 §7(d) ablation short-circuit **之后**（`validator.IsDryRunDisabled()` 分支不写；对齐现有 `dryRunWriter` 位置，见 `capability_tools.go:287`）：

```go
    snap, err := capability.NewSnapshot(snapSpec)
    if err != nil {
        // Fix v6 P1-1 (secret-leak, complete): NewSnapshot echoes
        // attacker-controlled field values (OS / Network / Files[].KindDetail)
        // that may embed raw tokens. Redact BOTH surfaces:
        //   (1) MCP wire response — fixed classifier only
        //   (2) driver-side log/audit — do NOT pass err through
        //       logHelperErr (its implementation writes err.Error() into
        //       audit log). Instead log ONLY the error's type name.
        errTypeName := fmt.Sprintf("%T", err)
        log.Printf("[observer_snapshot] new_snapshot rejected snapshot; err type=%s", errTypeName)
        return nil, &MCPToolError{Message: "capability_snapshot: shape invariant rejected", Category: observerstore.FailContractViolation}
    }
    blocks = validator.New().Check(ctx, tc, snap)
    snapHash = capability.ComputeHash(snap)

    // ── NEW (fix #81): persist canonical snapshot for per-agent attribution ─
    //
    // Position: OUTSIDE the `NoDryRun` ablation short-circuit (which returns
    // early above at `validator.IsDryRunDisabled()` — validated by test L12).
    //
    // Ablation split-deploy defence (fix v4 P1-2): driver checks
    // `capability.IsUploadDisabled()` LOCALLY here in addition to the
    // observer-side check inside `WriteSnapshot`. This ensures a driver
    // running with `NoCapabilityDiscovery=true` does NOT relay to an
    // observer that has the default flag state. Both guards are required
    // because they cover different failure modes:
    //   - driver-local guard: split deploy, observer flag misconfig
    //   - observer-side guard: single-process deploy, single source of truth
    // The driver-local log line and the observer-side log line are both
    // emitted when both flags are set (double-log is intentional; each
    // has an operator-owned inspection surface).
    if capability.IsUploadDisabled() {
        log.Printf("[ablation] NoCapabilityDiscovery: driver skipped WriteCapabilitySnapshot for conversation=%q hash=%s", tc.ConversationID, snapHash)
    } else if err := d.t.observerRelay().WriteCapabilitySnapshot(ctx, snap); err != nil {
        // Security-critical: don't leak raw error text for secret-scan
        // verdict; other errors carry no sensitive info (observer-side
        // errors are already redacted at the HTTP boundary in §4.2).
        // ErrSnapshotContainsSecret is surfaced by observer as
        // "snapshot contains raw token; rejected by secret scan" (422);
        // detect via status code marker in the error message.
        msg := "observer save capability snapshot: " + err.Error()
        if strings.Contains(err.Error(), "status 422") {
            msg = "observer save capability snapshot: rejected (secret scan)"
        }
        // Append to report.Warnings directly — do NOT rely on a local
        // `warnings` variable existing at this point in the function
        // (fix v4 P0). Ensure the slice is initialized if this is the
        // first append.
        if report.Warnings == nil {
            report.Warnings = []string{}
        }
        report.Warnings = append(report.Warnings, msg)
        d.t.logHelperErr("observer_snapshot", "write_snapshot", err)
    }
    // ────────────────────────────────────────────────────────────────

    report.Blocks = blocks
```

⚠️ **`warnings` 变量存在吗**？grep：现有 `Warnings []string` 在 `dryRunReport` 里没有。→ **加 field**：`Warnings []string \`json:"warnings,omitempty"\``. plan 阶段一并加。

**`d.t.observerRelay()` 已存在**（`capability_tools.go` 里现有 `dryRunWriter`/`SaveResourceSnapshot` 都通过它；`tools.go:relay()` accessor）——free 复用。

⚠️ **ablation 与 §7(d) NoDryRun 的正确 interplay（v6 明确更新，obsoletes 早期"driver 不感知"表述）**：
- `NoDryRun=true` 时，`dryRunContractTool.Call` 在 `IsDryRunDisabled()` 分支 return early（`capability_tools.go:280` 附近），**不会**跑到 hash 计算 → snapshot 也不写（符合 spec §7(d)：NoDryRun 关闭 pre-execution checks + 其副作用）。
- `NoCapabilityDiscovery=true` 时：**double guard**（v4/v5 修改）：
  - **driver-side (primary)**：`capability.IsUploadDisabled()` 在**发出 relay 请求之前**检查；true 时**不发出** HTTP 请求，log `[ablation] NoCapabilityDiscovery: driver skipped ...`
  - **observer-side (defense-in-depth)**：即便 driver 侧 guard 失效（bug/split deploy 配置漂移），observer `WriteSnapshot` 内部 short-circuit 依然拒绝写入并 log。
  - **这是 defense-in-depth，不是冗余**：driver 与 observer 可能在不同进程/机器，flag state 可能不同步。

**测试**：`TestDryRunContract_NoDryRunAblation_NoSnapshotWrite` (L12) + `TestDryRunContract_NoCapabilityDiscoveryAblation_NoRelayCall` (L13, driver-side) + `TestCapabilitySnapshots_NoCapabilityDiscoveryAblation_ObserverSideDefenseInDepth` (L13b, observer-side)。

### 4.5 main.go wiring

无需改。`driver.NewObserverRelay(cfg, obs)` 已在 `cmd/driver-agent/main.go:215` 存在；`observerRelay()` accessor 已就位。observer-server 侧的新 handler 在 mux 注册后**自动**接收 driver 请求；对齐已有 `dryRunBlocks` 模式。

## 5. Acceptance criteria

| # | 层 | 命令 / 观测 | 通过标准 |
|---|---|---|---|
| L1 | 新单测 observer-side：`TestCapabilitySnapshots_HappyPath` | POST 一个合法 canonical snapshot + driver auth | 204；SELECT count == 1；`capability_snapshot_usages` 一行 |
| L2 | 新单测 observer-side：`TestCapabilitySnapshots_SecretScanRejected_422` | POST snapshot 含 raw token | 422；body = `"snapshot contains raw token; rejected by secret scan"`；表内 0 rows |
| L3 | 新单测 observer-side：`TestCapabilitySnapshots_TooLarge_413` | POST > `maxEventBodyBytes` body | 413 |
| L4 | 新单测 observer-side：`TestCapabilitySnapshots_AuthNotDriver_403` | authenticated as `slave` role | 403 |
| L5 | 新单测 observer-side：`TestCapabilitySnapshots_MethodNotPost_405` | GET/PUT/DELETE | 405 |
| L6 | 新单测 observer-side：`TestCapabilitySnapshots_AttributionFromAuth_NotBody` | POST snapshot；authenticated agent 的 `Agent.ID` = "drv-001"；then SELECT `capability_snapshot_usages`.`agent_id` | 等于 "drv-001"（对应 observer `agents.id`，与 identity.Identity.AgentID 对齐）；handler 忽略任何 body-level `agent_id` 尝试 |
| L7 | 新单测 driver-side relay：`TestObserverRelay_WriteCapabilitySnapshot_HappyPath` | fake HTTP server 接收 → assert body shape + endpoint | 200 |
| L8 | 新单测 driver-side relay：`TestObserverRelay_WriteCapabilitySnapshot_LocalSizeCap` | 构造超大 snapshot | 返 error 而不发 request |
| L9 | 新单测 driver-side call site：`TestDryRunContract_WriteSnapshotFailure_DegradedToWarning` | inject failing relay | dry-run 返 200；warnings 含 fixed message；主路径未 kill |
| L10 | 新单测 driver-side call site：`TestDryRunContract_SecretScanFailure_RedactedWarning` | inject 422 status | warning = `"observer save capability snapshot: rejected (secret scan)"` |
| L11 | 新单测 driver-side call site：`TestDryRunContract_NilRelay_SilentSkip` | Tools 无 relay | dry-run 返 200；无 warnings |
| L12 | 新单测 driver-side call site：`TestDryRunContract_NoDryRunAblation_NoSnapshotWrite` | ablation.NoDryRun = true | validator.IsDryRunDisabled → early return；无 relay call；无 write |
| L13 | 新单测 driver-side call site：`TestDryRunContract_NoCapabilityDiscoveryAblation_NoRelayCall` | ablation.NoCapabilityDiscovery = true | **relay call 未发出**（driver-side guard 生效，v4 fix）；DB 无新行；driver 侧 ablation log line `[ablation] NoCapabilityDiscovery: driver skipped ...` 存在 |
| L13b | 新单测 observer-side：`TestCapabilitySnapshots_NoCapabilityDiscoveryAblation_ObserverSideDefenseInDepth` | observer 侧 ablation.NoCapabilityDiscovery = true；driver 侧 disabled；模拟"漏了 driver 侧 guard"场景 | observer 侧 WriteSnapshot short-circuit；DB 无新行；observer 侧 ablation log line 存在。**Defense in depth**：v4 double-guard 的下半层 |
| L14 | 现有测试全绿 | `go test ./internal/driver/... ./internal/observerweb/... -race` | 无回归 |
| L15 | `go vet` | 全 module | clean |
| L16 | smoke rerun (issue #78 L4 场景) | build stack → codex `dry_run_contract` → `sqlite3 $LOOM_HOME/observer/observer.db 'SELECT count(distinct hash) FROM capability_snapshots'` | count ≥ 1 |

## 6. Files touched

| File | Change |
|---|---|
| `internal/observerweb/server.go` | +80: handler `capabilitySnapshots` + `mux.HandleFunc` route |
| `internal/observerweb/server_test.go` (or new `capability_snapshots_test.go`) | 6 tests (L1-L6) |
| `internal/driver/observer_relay.go` | +50: `WriteCapabilitySnapshot(ctx, snap) error` + import `capability` |
| `internal/driver/observer_relay_test.go` | 2 tests (L7-L8) |
| `internal/driver/capability_tools.go` | +25 call site block; +1 field `Warnings []string \`json:"warnings,omitempty"\`` on `dryRunReport`; **imports**: add `fmt` (for `fmt.Sprintf("%T", err)` in v6 secret-redaction), `strings` if not already there (for `strings.Contains(err.Error(), "status 422")`), and `github.com/yourorg/multi-agent/internal/capability` if not already imported (for `capability.IsUploadDisabled()`) — plan phase must verify current imports and add missing ones |
| `internal/driver/capability_tools_test.go` | 5 tests (L9-L13) |

**No changes to** `observerstore/capability_snapshots_writer.go`, `internal/capability/snapshot.go`, `internal/observerstore/schema.sql`, `cmd/driver-agent/main.go`, `cmd/observer-server/main.go`.

## 7. Threat model recap

| Threat | Mitigation |
|---|---|
| caller forges attribution `agent_id` | observer-side handler ignores body-level `agent_id`; uses `authenticate()` return |
| caller poisons observer with secret-containing snapshot | `WriteSnapshot` secret-scan → 422; driver-relay redacts to fixed warning; snapshot not persisted |
| caller sends 1 GB snapshot to OOM observer | driver-relay pre-cap 256 KiB; observer `MaxBytesReader` cap |
| caller sends millions of unique snapshots to bloat DB | Accepted risk (Phase 3 real-run cadence ~ tens/hour); doc §8 note |
| ablation bypass | **Double guard** (fix v4): (1) driver-side `capability.IsUploadDisabled()` before relay call — covers split-deploy where observer flag differs; (2) observer-side `observerstore.WriteSnapshot` internal guard — covers single-process deploy + belt-and-suspenders |
| error text leaks sqlite internals | observer handler returns fixed `"failed to persist capability_snapshot"`; secret-scan verdict returns fixed 422 text |
| ablation flag drift | Test L12 (`NoDryRun`) + L13 (`NoCapabilityDiscovery`) both explicitly assert no write; regressions caught at CI |

## 8. Limitations (not fixed by this PR)

1. **`capability_snapshots` unbounded cardinality**: no TTL / no per-agent quota. Phase 3 real-run may want a retention job for old `capability_snapshot_usages` rows; separate follow-up.
2. **Postgres backend**: observer-side handler currently only handles `ManagedStore` (SQLite); postgres users get 503. Adding `postgres.WriteCapabilitySnapshot` is a separate PR — `dryRunBlocks` handler has the same limitation today (`server.go:1036-1043`).
3. **No batch API**: each `dry_run_contract` triggers one HTTP roundtrip. At Phase 3 cadence this is fine (<1 QPS); revisit if metric-extract needs bulk import.
4. **Warning stability**: the exact warning text is a public contract now (metric extractors may grep). Change requires bumping the log field version.

## 9. 三阶段循环审的执行计划

- **阶段 1（本文）** — spec 交 codex 审 → parse P0/P1 → 修 → 再审 → 到零 P0/P1 (P2/P3 not gating)
- **阶段 2** — plan.md → codex 审（`codex exec resume 019f3a89-c7d1-7271-8eef-5c9a14582a80`）→ 到零 P0/P1
- **阶段 3** — 按 plan 写代码 → codex 审 → 到零 P0/P1；P2/P3 记录不修
- 三阶段共享 codex session id `019f3a89-c7d1-7271-8eef-5c9a14582a80`
- 单阶段防死循环：>=5 轮不收敛，停下问用户

## 10. Revision log

- **v8 (2026-07-07 after codex round 6)** — 应审核补 1 P0：
  - r6 P0 (build-break: `fmt` import missing): §6 files-touched table 明列 `capability_tools.go` 新增 imports `fmt` / `strings` / `capability`；plan 阶段落实

- **v7 (2026-07-07 after codex round 5)** — 应审核补 1 P1：
  - r5 P1 (§3 security table stale text): §3 ablation-bypass 行改为 double-guard 语义，与 §4.4/§7 statement 对齐

- **v6 (2026-07-07 after codex round 4)** — 应审核补 2 P1：
  - r4 P1-1 (logHelperErr 仍泄漏 err.Error()): §4.4 改为**不**用 logHelperErr(err)；只 log `errTypeName := fmt.Sprintf("%T", err)`；err 不进任何 log/audit
  - r4 P1-2 (§4.4 stale text 与 L13 冲突): §4.4 ablation interplay 段完全重写，明写 double guard 语义

- **v5 (2026-07-07 after codex round 3)** — 应审核补 2 P1：
  - r3 P1-1 (NewSnapshot err leaks token to MCP caller): §4.4 上游 `NewSnapshot(snapSpec)` err 改为 log 完整、只返 `"capability_snapshot: shape invariant rejected"`；err.Error() 全 redact
  - r3 P1-2 (L13 与新 driver-side guard 冲突): L13 改成 `_NoRelayCall`（断言 relay 未发出）；加 L13b `_ObserverSideDefenseInDepth` 覆盖 double-guard 下半层；§7 threat model 明说双 guard

- **v4 (2026-07-07 after codex round 2)** — 应审核补 1 P0 + 2 P1：
  - r2 P0 (call-site `warnings` undefined): §4.4 改为直接 `report.Warnings = append(...)`，加 nil-init guard；不依赖不存在的 local slice
  - r2 P1-1 (NewSnapshot err log secret leak): §4.2 handler 改为**不**用 `%v` 打 err；固定 classifier + agent.ID + workspace 上下文（agent 已 auth）
  - r2 P1-2 (ablation split-deploy bypass): §4.4 driver-side 加 `capability.IsUploadDisabled()` 本地 guard；避免 driver=disabled+observer=enabled 时仍持久化。双 guard 是刻意，覆盖不同故障模式。

- **v3 (2026-07-07 pre-r2 fix)** — self-caught during r2 review (codex timed out mid-inspection while grepping identity resolver): observer-side handler was using `agent.SandboxID`, but `observerstore.Agent` exposes `ID`（stable observer-internal id）and `ExternalSandboxID`（agentserver-issued）separately. `capability_snapshot_usages.agent_id` schema semantics match `Agent.ID` (see existing writer tests use `"agent-1"` string, which is opaque + observer-internal). Changed §4.2 handler + §5 L6 to use `agent.ID`.

- **v2 (2026-07-07 after codex round 1)** — 应审核补 3 P0：
  - r1 P0-1 (Security-DoS body cap): 驱动 pre-cap 256 KiB + observer MaxBytesReader；限制说明写进 §3 / §7
  - r1 P0-2 (main.go store.DB() undefined): 完全放弃 driver-local sqlite 路径，改走 observer-server HTTP endpoint + relay；main.go 无需改
  - r1 P0-3 (DryRunReport.CapabilitySnapshotHash nonexistent): §1 语义澄清，指明该字段实际只在 `DryRunBlockRow` 里
  - r1 P1-1 (missing NoDryRun ablation test): §4.4 明写位置约定；§5 加 L12 覆盖
  - r1 P2-1 / P2-2 (per user policy P2 not fixed): 记录如下——
    - P2-1: nil relay 语义（helper-error log vs silent skip）——v2 统一为**silent skip**（L11 断言无 warnings），既有 `SaveResourceSnapshot`/`WriteDryRunBlock` 均此语义
    - P2-2: warning 文案漂移——v2 统一为 `"observer save capability snapshot: rejected (secret scan)"`（L10 断言，与实现 §4.4 同）

- **v1 (2026-07-07 initial)** — 初稿（driver-local sqlite 方案）
