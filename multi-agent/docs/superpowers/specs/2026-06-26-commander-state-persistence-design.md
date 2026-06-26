# Commander login + session state persistence (Postgres)

**Date:** 2026-06-26  
**Status:** Draft  
**Stage:** Spec (Stage 1 of Spec / Plan / Code)

## 1. Problem

`https://loom.nj.cs.ac.cn:10062/commander` 上点「用 agentserver 登录」绝大概率返回 `登录失败: HTTP 404`。

请求链路:

```
Browser → POST /api/commander/login         → Service round-robin → Pod A
                                              Pod A.logins[lid] = {DeviceCode, ...}
                                              Pod A 起 pollLogin goroutine
                                            ← 200 {login_id, ...}

Browser (≈1.5 s 后) → GET /login/poll?id=lid → Service round-robin → Pod B
                                               Pod B.logins[lid] 不存在
                                             ← 404 "unknown login"

前端 CommanderApp.tsx:249  if (!res.ok) throw new Error(`HTTP ${res.status}`)
→ setLogin({phase:'error', error:'HTTP 404'})
→ 页面显示「登录失败: HTTP 404」
```

### 根本原因

`internal/commanderhub/auth.go` 的 `Authenticator` 把两份状态完全放在进程内存里:

- `logins map[string]*loginState` —— 待授权与已授权的 device-flow 登录
- `sessions map[string]*session` —— 已登录用户的 cookie → token / identity

生产部署:

- `deploy/charts/observer/values-production.example.yaml:1` → `replicaCount: 3`
- `deploy/charts/observer/templates/service.yaml` 无 `sessionAffinity` / `ClientIP`
- HTTPRoute 平台托管,无 cookie 粘性
- `grep -rn 'sessionAffinity|consistentHash|sticky' deploy/` → 空

3 副本下 `/poll` 命中正确 pod 概率 1/3。后续 `/api/commander/tree` 等鉴权请求每次又是 1/3 概率,所以即便登录侥幸成功也会立即被另一个 pod 当成无 cookie 弹回登录页。

`auth.go:330–389` 的 `ServeLoginPoll` handler 本身没有 bug;`auth.go:361` 输出的 `"unknown login"` 体即为我们看到的 404 body —— `curl https://loom.nj.cs.ac.cn:10062/api/commander/login/poll?id=does-not-exist-test` 实测返回 `404 unknown login`,说明 handler 在跑,**路由没问题,问题是状态在错误的 pod**。

### 不修则会怎样

- 用户每次登录 1/3 概率成功;成功后每次请求 1/3 概率掉 session
- 任意 pod 滚动重启 → 所有该 pod 上的用户被踢
- `pollLogin` 后台 goroutine 跟着 pod 走,pod 挂 → login 永远 pending

## 2. Goals

1. **任意 observer-server pod 都能服务任意 commander 请求**(POST /login、GET /poll、Set-Cookie 后的 /api/commander/*)。
2. **Pod 滚动重启不强制用户重登。** 已登录 session 跨重启存活,直到 `sessionTTL = 12h` 自然过期。
3. **保持现有 one-shot 语义和 TTL 行为不变** —— 前端代码零修改。
4. **不引入新部署组件** —— 复用已有 Postgres(observer-server 已经在用)。
5. **Dev / sqlite 模式继续工作**(单 pod,无需 DB schema)。
6. **测试保持快**:单元测试无需 Postgres,集成测试可选。

### 非目标

- 不做 commander 的多用户并发优化
- 不做 SLO/指标看板(只加最简日志)
- 不重写 device-flow 协议(沿用 agentserver `RequestDeviceCode` + `PollToken` 的子集)
- 不动前端
- 不解决 observer-server 缺少 graceful shutdown 的现状(本变更不让它变得更差)

## 3. 决策矩阵(已敲定)

| 决策点 | 选择 | 替代 / 理由 |
|---|---|---|
| `pollLogin` 后台 goroutine | **删除**,/poll handler 同步调一次 `PollOnce` | 替代:DB lease + SKIP LOCKED 续 poll、leader 选举。前端本就 1.5 s 节流,同步拉天然多副本友好。**节流由 store 侧 `next_poll_at` 强制**(§6),避免暴击 agentserver |
| POST /login 先 reserve 再 RequestCode | **保留**今天的"先占位后上游"模型(`internal/commanderhub/auth.go:224` 注释解释为什么)。在 Postgres 版用一行 `device_code = ''` 的 reservation 行 + 一次 `UPDATE` 填回字段;失败则 `DELETE` 释放 | 替代:`SELECT count() ; INSERT` 有 TOCTOU 漏洞,会把 cap 击穿。reservation 行的 `device_code` 列允许空字符串作为哨兵 |
| Store 接口分包 | 新包 `internal/commanderhub/authstore/`,语义化 `Store` 接口 | 仿 `internal/userspace/` 现成范式,避免污染 commanderhub 主包 |
| Store 实现数量 | **2 个**:`postgresStore`(生产/集成测) + `inmemoryStore`(单测、dev/sqlite 退化) | **不写 sqliteStore** —— 用户明确不愿维护双 SQL 方言一致性。代价:dev/sqlite 仍是单 pod 语义(跟今天一样),开机 log 显式提示 |
| One-shot 消费 | `DELETE … RETURNING *`(Postgres) / map lock+delete(inmemory) | 与今天 in-memory 1:1 等价,不引入 `consumed_at` 软删列 |
| Session 存储:cookie 明文 / DB 存哈希 | DB 列 `session_id_hash = sha256_hex(sid)`,cookie 仍下发明文 `sid`;`GetSession(sid)` 走 `WHERE session_id_hash = $1`。**`access_token` 不入 DB**(commander 本身只需要 identity,access_token 在登录闭环外没人用) | 替代:明文 sid 入库 → DBA / 备份 / 慢查询日志直接拿到 cookie 等价物。哈希后即便泄露也无法用作 cookie |
| TTL 清扫 | 写路径懒扫 + 每 pod `1h` `time.Ticker` 兜底 `DELETE WHERE expires_at < now()` | 多 pod 重复执行无害 |
| `MarkLoginDone` 防孤儿 | **强一致**:一个事务里 `UPDATE commander_logins SET session_id_hash=… WHERE login_id=$1 AND session_id_hash IS NULL AND failure IS NULL AND expires_at>now()`,RowsAffected=0 → 整个事务 rollback,session 不写,返回 `ErrNotFound`。**§7 的"接受孤儿"段落删除** | 替代:接受孤儿。Codex Stage 1 审 blocker #4 指出语义自相矛盾;选最干净的语义 |
| **[C1] 同步 Set-Cookie**(废弃 ★ 不变式) | [C1] 成功 → 立即 Set-Cookie + 返回 `{"status":"ok"}`。[B] 分支只服务 (a) failure → 401 (b) done(说明客户端没收到上次 [C1] 的 200 响应 / 或在另一 pod 走完 [C1])→ **404 `{"status":"error","error":"login already completed"}`,客户端按"重新点登录"处理** | Codex Stage 1 R2 blocker #1。 一致性来源是 `MarkLoginDone` 的 UPDATE WHERE pending(任何并发 [C1] 只有一个能赢得 UPDATE);明文 sid 完全不必跨 pod 传递,DB 列继续只存 hash。罕见的"[C1] done 后客户端断网"窗口 < 1%,UX 同 OAuth device flow 正常重新发起,可接受 |
| 服务端节流 `next_poll_at` + interval 动态升级 | `commander_logins.next_poll_at` + `interval_seconds` 列。[C] 进入前 `if rec.NextPollAt > now: return pending`;`PollOnce` 后:`retryable` → `next_poll_at = now + max(5s, interval_seconds)`;`slow_down` → `interval_seconds += 5` 且 `next_poll_at` 增量;一次性方法 `SetPollThrottle(ctx, lid, intervalSeconds, nextPollAt)` 同时更新两列 | agentserver `slow_down`/速率防护;一次写避免分两个 SQL 出现"interval 升了 next_poll 没升"的中间态 |
| Schema 字段 | 行内列(`user_id`、`workspace_id`、`role`、`source`),不存 JSON、**不存 access_token、不存明文 sid**。`logins` 只持久化 `device_code`、`code_expires_at`、`interval_seconds`、`next_poll_at`、`session_id_hash`、`failure` | 紧凑、可索引、易运维 |
| Failure 文本入库前必须净化 | `SanitizeFailure(err) string` **只输出枚举集合**:`"authorization denied"` / `"authorization expired"` / `"upstream timeout"` / `"device flow error"` / `"id token invalid"` / `"store unavailable"`。**不接收 raw 字符串、不返回 raw 字符串。** store 接口标注"failure 必须是枚举之一" | Codex Stage 1 R2:regex scrubbing 总会有漏网。enum 是安全的 fail-closed:任何未识别的错误降级为 `"device flow error"` |
| Schema 迁移 | 同 `userspace.MigratePostgres` 套路:`schema_postgres.sql` 嵌入 + `db.Exec()`;接入 `observer-server --migrate-only`,跟随 helm `migration-job.yaml` 运行 | 不动 helm chart yaml |
| 测试拓扑 | 1) `authstore_test` 包(`_test.go`)里 `RunConformanceTests(t, factory)` 用同一组断言驱动 inmemory + postgres;2) postgres-specific SQL 方言测试用 recording driver;3) Authenticator 测试用 inmemory store。集成测沿用 `OBSERVER_POSTGRES_TEST_DSN`,空则 skip | 不写双 SQL 方言一致性测,把"两实现行为一致"由 conformance 顶住。conformance 在 `_test.go` 里(Codex Stage 1 nit) |
| 进程生命周期 | sweep goroutine 直接 `go auth.runSweep(time.Hour)`,跟随进程死(observer-server 无 graceful shutdown) | 不为此引入 ctx 参数 |
| Production postgres 没 store 时启动行为 | `cfg.Store.Driver == "postgres"` 且 `cfg.Identity.Agentserver.URL != ""` 时,observer-server 启动 panic;**不**静默退到 inmemory | 替代:inmemory 静默后备。Codex Stage 1 设计点指出生产回退到内存 = 静默回到 bug 状态。fail-fast 更安全 |

## 4. 架构

```
                                  ┌──────────────────────────────────┐
                                  │  Postgres (existing observer DB) │
                                  │  + commander_logins              │
                                  │  + commander_sessions            │
                                  └──────────────┬───────────────────┘
                                                 │ database/sql (existing pool)
                  ┌──────────────────────────────┼──────────────────────────────┐
                  │                              │                              │
            ┌─────┴────────┐               ┌─────┴────────┐               ┌─────┴────────┐
            │ observer-pod │               │ observer-pod │               │ observer-pod │
            │      A       │               │      B       │               │      C       │
            │              │               │              │               │              │
            │ Authenticator│               │ Authenticator│               │ Authenticator│
            │  ─→ Store ──┘               │  ─→ Store ──┘               │  ─→ Store ──┘
            │              │               │              │               │              │
            │ sweep@1h     │               │ sweep@1h     │               │ sweep@1h     │
            └──────────────┘               └──────────────┘               └──────────────┘
                  ▲                              ▲                              ▲
                  └──────── round-robin ─────────┴──────────────────────────────┘
                                                 │
                                       browser /api/commander/*
```

```
internal/commanderhub/
  auth.go                            // Authenticator,删 logins/sessions 内存 map,
                                     // 持 authstore.Store。无任何 cross-pod 内存状态
  http.go / hub.go / web.go / ...    // 不动
  authstore/                         // 新包
    store.go                         // Store 接口 + LoginRecord/SessionRecord
    failure.go                       // SanitizeFailure (enum-only) + Failure 类型
    inmemory.go                      // inmemoryStore(map + sync.Mutex)
    postgres.go                      // postgresStore(*sql.DB)
    schema_postgres.sql              // 嵌入
    migrate.go                       // MigratePostgres(db *sql.DB)
    conformance_test.go              // 导出 RunConformanceTests,suffix _test.go(Codex Stage 1 nit)
    inmemory_test.go                 // RunConformanceTests + 纯逻辑
    postgres_test.go                 // RunConformanceTests + SQL 方言 + DSN-gated 集成
    sql_dialect_test.go              // recordingSQLDB 套路,无需 DSN
    failure_test.go                  // SanitizeFailure 枚举性验证

cmd/observer-server/main.go
  - 启动时,如果 driver=postgres,authstore.MigratePostgres(st.DB())
  - 构造 authstore.NewPostgresStore(st.DB()) 或 authstore.NewInMemoryStore()
  - 通过 observerweb.Options.AuthStore 透传

internal/observerweb/server.go
  - Options 加 AuthStore 字段
  - 传给 commanderhub.MountAll(mux, resolver, agentserverURL, store)
```

## 5. Store 接口

```go
package authstore

import (
    "context"
    "errors"
    "time"

    "github.com/yourorg/multi-agent/internal/identity"
)

// ErrNotFound: lookup miss(sentinel)。
// ErrCapped:   POST /login cap 满,提示调用方回 429。
// 任何其它返回错被视为 DB 故障 → handler 应回 502。
var (
    ErrNotFound = errors.New("authstore: not found")
    ErrCapped   = errors.New("authstore: pending logins cap reached")
)

// LoginRecord:commander_logins 行的语义化表示。
//
// 状态机:
//   reserved: DeviceCode == "" && Failure == "" && SessionIDHash == ""
//             (POST /login reserve 占位,RequestCode 尚未返回)
//   pending:  DeviceCode != "" && Failure == "" && SessionIDHash == ""
//   failed:   Failure != ""(终态)
//   done:     SessionIDHash != ""(终态)
//
// failed 与 done 互斥。store 实现侧用 CHECK 约束保证。
type LoginRecord struct {
    LoginID         string
    DeviceCode      string    // "" 在 reserved 态
    CodeExpiresAt   time.Time // agentserver device-code 死线
    IntervalSeconds int       // PollOnce 的最小节流间隔, 由 RequestCode 返回
    NextPollAt      time.Time // 服务端节流:在此时间前 PollOnce 不应被调用
    ExpiresAt       time.Time // loginTTL(10 min)死线
    SessionIDHash   string    // terminal:done。hex(sha256(明文 sid))
    Failure         string    // terminal:failed。MUST be sanitized by caller (§见 sanitizeFailure)
}

// SessionRecord:commander_sessions 行 + identity。
//
// PlaintextSessionID 仅在 InsertSession / MarkLoginDone 入参里出现,
// store 实现侧立即 hash 后写入 session_id_hash 列,不持久化明文。
// GetSession 同样收明文 sid,内部 hash 后查询。
type SessionRecord struct {
    PlaintextSessionID string // 仅 in-flight 用; store 不持久化
    Identity           identity.Identity
    ExpiresAt          time.Time
}

// Store 是 Authenticator 持久化抽象。所有方法必须并发安全。
type Store interface {
    // -- logins --

    // ReserveLogin 原子地:
    //   1) 删 expires_at < now 的过期行(防止僵尸占着 cap 名额)
    //   2) 检查 cap;>= 1024 返回 ErrCapped(不消费 cap 名额)
    //   3) 插入 reservation 行 (DeviceCode="", ExpiresAt = now + loginTTL)
    //
    // 必须在一个事务/单 SQL 内完成(否则 cap 有 TOCTOU)。
    // 完成后调用方调 RequestCode,成功后 FinalizeReservedLogin 填字段;
    // 失败则 DeleteLogin 释放名额。
    ReserveLogin(ctx context.Context, loginID string, now time.Time, ttl time.Duration) error

    // FinalizeReservedLogin 把 RequestCode 拿到的字段写回 reservation 行。
    // 必须 WHERE login_id = $lid AND device_code = '' (reserved 状态)。
    // 行不在 reserved 态(并发 sweep 把它清了) → ErrNotFound。
    FinalizeReservedLogin(ctx context.Context, loginID string,
        deviceCode string, codeExpiresAt time.Time, intervalSeconds int) error

    // DeleteLogin 释放 reservation 占着的 cap 名额。
    // 不存在返回 nil(幂等)。Authenticator 仅在 RequestCode 失败时调。
    DeleteLogin(ctx context.Context, loginID string) error

    // GetLogin 读取当前状态,不修改。ErrNotFound = 行不存在。
    // 调用方负责判 ExpiresAt < now 视为过期。
    GetLogin(ctx context.Context, loginID string) (LoginRecord, error)

    // SetPollThrottle 单 SQL 同时更新 interval_seconds 与 next_poll_at。
    // 幂等;不存在的 lid 返回 nil(本次 /poll 节流失效不破 SLA)。
    // intervalSeconds 必须 > 0(store 实现侧用 CHECK 守住)。
    SetPollThrottle(ctx context.Context, loginID string, intervalSeconds int, nextPollAt time.Time) error

    // MarkLoginDone 单事务原子地:
    //   1) UPDATE commander_logins
    //          SET session_id_hash=$hash, finalized_at=now()
    //        WHERE login_id=$lid
    //          AND session_id_hash IS NULL AND failure IS NULL
    //          AND device_code != '' AND expires_at > now
    //   2) RowsAffected = 0 → ROLLBACK,返回 ErrNotFound
    //   3) RowsAffected = 1 → INSERT INTO commander_sessions ... COMMIT
    //
    // 必须置 finalized_at,否则 commander_logins_finalized_iff_terminal CHECK 失败。
    // 输入 session.PlaintextSessionID 由实现侧 hash 后写;调用方持有明文用于 Set-Cookie。
    // 输入 ctx 不应在写入路径上被取消(由 Authenticator 用 context.WithoutCancel 包好)。
    MarkLoginDone(ctx context.Context, loginID string, session SessionRecord) error

    // MarkLoginFailed 设 failure 字段(input MUST be a SanitizeFailure enum)。
    // 单事务原子地置 failure + finalized_at,WHERE session_id_hash IS NULL
    // AND failure IS NULL AND expires_at > now。
    // 仅在 pending 或 reserved 态成功;终态 / 不存在 / 过期 → ErrNotFound。
    // 输入 sanitizedFailure 必须是 SanitizeFailure 输出枚举之一,store 侧不再二次过滤;
    // CHECK 约束 length <= 256 兜底误用。
    MarkLoginFailed(ctx context.Context, loginID string, sanitizedFailure Failure) error

    // ConsumeLogin: 原子 SELECT + DELETE,one-shot 语义的核心。
    // Postgres: DELETE FROM commander_logins WHERE login_id=$1 RETURNING ...
    // inmemory: lock + map lookup + delete + return
    // 返回 ErrNotFound 表示别的 pod 已经消费,或 login 本就不存在。
    // 调用方负责只在终态时调用 —— Authenticator 状态机 §6[B] 已守住此契约;
    // 实现侧 NOT 做"只允许终态消费"的额外守护(因为 [A3] 也要消费过期 pending)。
    ConsumeLogin(ctx context.Context, loginID string) (LoginRecord, error)

    // -- sessions --

    // GetSession 必须按 expires_at > now() + session_id_hash = sha256_hex(plaintext) 过滤。
    // 行存在但已过期 → ErrNotFound。store 实现侧 hash 入参,不让明文 sid 落入 SQL 参数。
    GetSession(ctx context.Context, plaintextSessionID string) (SessionRecord, error)

    // DeleteSession logout 路径。不存在返回 nil(幂等)。
    // 入参明文 sid;实现侧 hash 后 DELETE WHERE session_id_hash = $hash。
    DeleteSession(ctx context.Context, plaintextSessionID string) error

    // -- sweep --

    // SweepExpired 删两张表 expires_at < now 的行。
    // 多 pod 并发执行无害。返回各自删除行数与首个错误。
    SweepExpired(ctx context.Context) (loginsDeleted int64, sessionsDeleted int64, err error)
}
```

### Bounded background contexts for unkillable writes

任何 `context.WithoutCancel(ctx)` 包装的 DB 写,在调用 store 之前**必须**再叠一层 `context.WithTimeout(bgCtx, storeWriteTimeout)`,`storeWriteTimeout = 5*time.Second`(常量)。这避免 Postgres 或连接池阻塞时,handler / sweep goroutine 永久挂起、把连接池吃干(Codex Stage 1 R3 blocker)。规范化的模式:

```go
bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeWriteTimeout)
defer cancel()
err := store.MarkLoginDone(bgCtx, lid, sess)
```

Authenticator 应封一个 helper `(a *Authenticator) writeCtx(ctx context.Context) (context.Context, context.CancelFunc)` 复用。

适用方法:`FinalizeReservedLogin`、`DeleteLogin`(post-reserve cleanup)、`MarkLoginDone`、`MarkLoginFailed`、`SetPollThrottle`、`ConsumeLogin`(在 [B]/[A3])、`DeleteSession`(在 ServeLogout)。`ReserveLogin` 不需要(它跑在 r.Context 上,client cancel 时安全;失败就让 cap 名额随上下文清,Reserve 之前没有副作用)。`SweepExpired` 用 sweeper-owned 30s 上下文,沿用 §8 已有写法,与本节一致。

`§6 POST /login` 和 `/poll` 状态机里的所有 `WithoutCancel` 调用点都按上述模式叠 `WithTimeout`。Stage 2 plan 把这条作为单独一项工程检查。

### SanitizeFailure(err error) Failure  (`internal/commanderhub/authstore/failure.go`)

```go
// Failure 是一个 string newtype,只有枚举常量构造合法实例。
// MarkLoginFailed 的 sanitizedFailure 参数类型即 Failure,编译期阻止 raw string 入库。
type Failure string

const (
    FailureAuthorizationDenied  Failure = "authorization denied"
    FailureAuthorizationExpired Failure = "authorization expired"
    FailureUpstreamTimeout      Failure = "upstream timeout"
    FailureIDTokenInvalid       Failure = "id token invalid"
    FailureDeviceFlow           Failure = "device flow error"
    FailureStoreUnavailable     Failure = "store unavailable"
)

// SanitizeFailure 是上游错误的唯一出口,fail-closed:
// 任何未明确识别的错误降级为 FailureDeviceFlow。
// 永远不返回 raw err.Error() 文本。
func SanitizeFailure(err error) Failure {
    if err == nil {
        return FailureDeviceFlow // defensive; shouldn't be called with nil
    }
    if errors.Is(err, context.DeadlineExceeded) {
        return FailureUpstreamTimeout
    }
    if errors.Is(err, errAuthorizationDenied) {
        return FailureAuthorizationDenied
    }
    if errors.Is(err, errAuthorizationExpired) {
        return FailureAuthorizationExpired
    }
    if errors.Is(err, errIDTokenInvalid) {
        return FailureIDTokenInvalid
    }
    return FailureDeviceFlow
}
```

`deviceFlow.PollOnce` 内部在感知 `access_denied` / `expired_token` / `slow_down` / `authorization_pending` 后,**返回 sentinel error**(`errAuthorizationDenied` 等), 不返回 raw HTTP body 字符串。Authenticator 收到 `perr` 直接 `SanitizeFailure(perr)`。

**DB 端枚举执法**:`commander_logins.failure` 列 CHECK `failure IS NULL OR failure IN ('authorization denied','authorization expired','upstream timeout','id token invalid','device flow error','store unavailable')`。这把"枚举性"从 Go 层强化到 DB 层 —— 即便有人将来 `authstore.Failure(err.Error())` 强转写入,SQL 也会拒绝(Codex Stage 1 R3 安全条:`Failure` newtype 不是 unforgeable)。CHECK 列表跟 `failure.go` 常量定义必须同步;Stage 2 plan 加一项"failure.go 改动必须同步 schema_postgres.sql + migration"。

`commander_logins.failure` 列还附带 `length(failure) <= 256` CHECK 作为长度兜底。

### 设计要点

- 接口 11 个方法,粒度语义化;Authenticator 不写一行 SQL
- **没有 InsertLogin 单步写入** —— 入门一定走 ReserveLogin + FinalizeReservedLogin 的两步,防 cap TOCTOU
- **`MarkLoginDone` 由 store 实现侧守住"only-pending-can-win"** —— 输的 caller 会拿 ErrNotFound,session row 通过事务 rollback 不入表(消除孤儿)
- **`MarkLoginFailed` 同样守住"only-pending-can-fail"** —— 防覆盖已 done 的 login
- `MarkLoginDone` 的 ctx 由 caller 用 `context.WithoutCancel`(Go 1.21+) 包,使客户端断开不杀写入路径
- `GetSession` / `DeleteSession` 输入明文 sid,store 内部 hash;明文 sid 永不写参数化 SQL 之外的任何地方
- `commander_sessions` 表无 `access_token` 列(commander 走 cookie session 即可,不需要原 OAuth token)
- 无 `Close()` —— DB 生命周期归 observer-server 主进程

## 6. /poll + /login handler 状态机

### deviceFlow seam

`agentsdkDeviceFlow.PollToken`(死循环)在本次变更中**移除**,改为同步 `PollOnce`。

```go
type deviceFlow interface {
    // RequestCode 包含 Interval 字段供调用方持久化做服务端节流。
    RequestCode(ctx context.Context) (DeviceCode, error)

    // PollOnce 跑一次 agentserver /api/oauth2/token,返回三态:
    //   tokenReady=true                  → 拿到 token, tok 有效
    //   tokenReady=false retryable=true  → "authorization_pending"/"slow_down"/transient HTTP error;
    //                                       slowDown=true → 调用方应增加下次 next_poll_at 步长
    //   tokenReady=false retryable=false → terminal failure (access_denied / expired_token …)
    //
    // err 已 sanitize(authstore.SanitizeFailure 已应用),可直接 MarkLoginFailed 入库。
    PollOnce(ctx context.Context, code DeviceCode) (tok loginToken, tokenReady, retryable, slowDown bool, err error)
}
```

### POST /api/commander/login (ServeLogin)

```
POST /api/commander/login

1. lid := randomID()
2. err := store.ReserveLogin(ctx, lid, now, loginTTL)
     ErrCapped  → 429 "too many pending logins"
     err != nil → 502 "store unavailable"
     OK         → 继续

3. // 一旦 reserve 成功,后续清理必须 unkillable —— 否则 client cancel
   //  会留下占位行直到 loginTTL,unauth 客户端可循环填满 cap。
   bgCtx := context.WithoutCancel(ctx)

4. // RequestCode 仍用 r.Context() 以便客户端真的不要时取消上游往返;
   //  失败路径用 bgCtx 释放占位。
   dc, err := flow.RequestCode(ctx)
   if err != nil:
       store.DeleteLogin(bgCtx, lid)                          // ★ unkillable cleanup
       return 502 "device flow: " + SanitizeFailure(err)
   if ctx.Err() != nil:                                       // client 在 RequestCode 后断开
       store.DeleteLogin(bgCtx, lid)
       return                                                 // ResponseWriter 已无意义

5. err := store.FinalizeReservedLogin(bgCtx, lid,             // ★ unkillable write
            dc.Code, time.Now().Add(dc.ExpiresIn), int(dc.Interval/time.Second))
   if err == ErrNotFound:
       return 502 "login expired during init"                 (极罕见:sweep 抢先)
   if err != nil:
       store.DeleteLogin(bgCtx, lid)
       return 502 "store unavailable"

6. 200 {"verification_uri_complete": dc.VerificationURIComplete, "login_id": lid, "expires_in": ...}
```

Reservation 模式 + 全程 `WithoutCancel` 清理保证 cap 不被 TOCTOU 击穿,也保证 client cancel 不能囤积 reservation。

### GET /api/commander/login/poll (ServeLoginPoll)

新版本**废弃**之前的 ★ "[B] 唯一终态出口" 不变式。终态在 [C1] / [C3] 现场返回。[B] 只服务"读到已存在的终态"(主要是 failure)或"二次访问已 done"(异常路径)。

```
GET /api/commander/login/poll?id=<lid>

[A] rec, err := store.GetLogin(ctx, lid)
    [A1] ErrNotFound       → 404 "unknown login"
    [A2] 其它 err           → 502 "store unavailable"
    [A3] rec.ExpiresAt<now  → store.ConsumeLogin(WithoutCancel(ctx)) best-effort
                              → 404 "unknown login"
    [A4] rec.DeviceCode==""  (reserved 但 RequestCode 还没返回 / fail):
                            → 200 {"status":"pending"}      (下一跳由前端 1.5s 节流)

[B] rec.SessionIDHash != "" OR rec.Failure != "" (已是终态):
    bgCtx := context.WithoutCancel(ctx)
    consumed, err := store.ConsumeLogin(bgCtx, lid)
    err==ErrNotFound  → 404 "unknown login"
    err!=nil          → 502 "store unavailable"
    if consumed.Failure != "":
        → 401 {"status":"error","error": string(consumed.Failure)}
    if consumed.SessionIDHash != "":
        // [C1] 在某个 pod 成功了,但客户端没收到那次响应(网络抖、断开、跨 pod):
        // 没有明文 sid 可发,只能让前端重新发起登录流程。
        → 401 {"status":"error","error":"authorization expired"}
        // 注:用枚举字符串避免暴露内部信息;前端逻辑跟其它 401 一致。

[C] pending (rec.DeviceCode != ""):
    [C-throttle] if rec.NextPollAt > now → 200 {"status":"pending"}

    pollCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
    defer cancel()
    dc := DeviceCode{Code: rec.DeviceCode, ExpiresIn: time.Until(rec.CodeExpiresAt),
                     Interval: time.Duration(rec.IntervalSeconds)*time.Second}
    tok, ready, retryable, slowDown, perr := flow.PollOnce(pollCtx, dc)
    bgCtx := context.WithoutCancel(ctx)                       // 写路径用

    [C1] ready==true:
        ident, err := identityFromIDToken(tok.IDToken, time.Now())
        if err != nil:
            store.MarkLoginFailed(bgCtx, lid, SanitizeFailure(err))    // best-effort
            return 401 {"status":"error","error": string(FailureIDTokenInvalid)}

        sid := randomID()
        err = store.MarkLoginDone(bgCtx, lid, SessionRecord{
            PlaintextSessionID: sid,                          // store 内部 hash
            Identity:           ident,
            ExpiresAt:          time.Now().Add(sessionTTL),
        })
        if err == ErrNotFound:
            // 另一 pod 已经赢了。我们 sid 没人知道,丢弃即可。
            return 401 {"status":"error","error":"authorization expired"}
        if err != nil:
            return 502 "store unavailable"

        // ★ NEW 不变式:[C1] 同步发 cookie + ok
        Set-Cookie commander_sess=<sid>;Path=/;HttpOnly;SameSite=Lax;Secure(if TLS)
        return 200 {"status":"ok"}

    [C2] retryable==true:
        intervalSeconds := rec.IntervalSeconds
        if slowDown:
            intervalSeconds += 5                              // §3 决策
        if intervalSeconds < 5: intervalSeconds = 5
        nextPollAt := time.Now().Add(time.Duration(intervalSeconds)*time.Second)
        store.SetPollThrottle(bgCtx, lid, intervalSeconds, nextPollAt) // best-effort
        return 200 {"status":"pending"}

    [C3] retryable==false:
        store.MarkLoginFailed(bgCtx, lid, SanitizeFailure(perr))       // best-effort
        return 401 {"status":"error","error": string(SanitizeFailure(perr))}
```

### 关键不变式(新)

> **终态响应在产生终态的同一次 /poll 调用里直接返回。** [B] 只服务"另一 pod 已写下终态,本 pod 读到了" 的少数异常路径,且统一退化为"请重登"。

这意味着 cookie 一旦下发,客户端就拥有完整 session。跨 pod 不必传递明文。DB 永远只存 hash。

### 客户端断开

- `PollOnce` 用 `r.Context()` —— 客户端断开取消上游往返,无副作用,下次 /poll 重做
- 一切写路径(`DeleteLogin`/`FinalizeReservedLogin`/`MarkLoginDone`/`MarkLoginFailed`/`SetPollThrottle`/`ConsumeLogin`/`DeleteSession`)用 `context.WithoutCancel(ctx)` —— 客户端断开不破坏一致性
- 在写完 `MarkLoginDone` 但响应未发出之间客户端断开:DB 标 done,客户端没拿到 sid,下次 /poll 走 [B] 拿 401 → 用户重登。比"sid 串号"安全得多

### 5 秒 PollOnce 超时

agentserver 在 LAN,p99 远小于 5 s。客户端 fetch 默认 60 s,留足重试。

## 7. Schema

```sql
-- internal/commanderhub/authstore/schema_postgres.sql

CREATE TABLE IF NOT EXISTS commander_logins (
    login_id          text        PRIMARY KEY,
    device_code       text        NOT NULL DEFAULT '',  -- reservation: '', filled by FinalizeReservedLogin
    code_expires_at   timestamptz,                       -- NULL until FinalizeReservedLogin
    interval_seconds  integer     NOT NULL DEFAULT 5,    -- 服务端节流下限
    next_poll_at      timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    session_id_hash   text,                              -- hex(sha256(plaintext sid)); terminal=done
    failure           text,                              -- 必经 SanitizeFailure; terminal=failed
    finalized_at      timestamptz,                       -- terminal 转入时刻

    -- 终态互斥:done XOR failed,且终态必有 finalized_at
    CONSTRAINT commander_logins_terminal_xor CHECK (
        (session_id_hash IS NULL OR failure IS NULL)
    ),
    CONSTRAINT commander_logins_finalized_iff_terminal CHECK (
        (finalized_at IS NULL) =
        (session_id_hash IS NULL AND failure IS NULL)
    ),
    CONSTRAINT commander_logins_failure_len CHECK (
        failure IS NULL OR length(failure) <= 256
    ),
    CONSTRAINT commander_logins_failure_enum CHECK (
        failure IS NULL OR failure IN (
            'authorization denied',
            'authorization expired',
            'upstream timeout',
            'id token invalid',
            'device flow error',
            'store unavailable'
        )
    ),
    CONSTRAINT commander_logins_login_id_nonempty CHECK (length(login_id) > 0),
    -- reserved (device_code = '') 行必须 code_expires_at IS NULL;
    -- 非 reserved 行必须 code_expires_at IS NOT NULL。完整性兜底。
    CONSTRAINT commander_logins_code_expires_iff_devcode CHECK (
        (device_code = '' AND code_expires_at IS NULL)
        OR
        (device_code <> '' AND code_expires_at IS NOT NULL)
    ),
    CONSTRAINT commander_logins_interval_positive CHECK (interval_seconds > 0)
);
CREATE INDEX IF NOT EXISTS commander_logins_expires_idx
    ON commander_logins (expires_at);

CREATE TABLE IF NOT EXISTS commander_sessions (
    session_id_hash text        PRIMARY KEY,             -- hex(sha256(plaintext sid)); 明文永不入库
    user_id         text        NOT NULL,
    workspace_id    text        NOT NULL,
    role            text        NOT NULL DEFAULT '',
    source          text        NOT NULL,                -- identity.Source enum
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT commander_sessions_user_id_nonempty     CHECK (length(user_id) > 0),
    CONSTRAINT commander_sessions_workspace_id_nonempty CHECK (length(workspace_id) > 0),
    CONSTRAINT commander_sessions_source_nonempty       CHECK (length(source) > 0)
);
CREATE INDEX IF NOT EXISTS commander_sessions_expires_idx
    ON commander_sessions (expires_at);
```

行内列 + 只索引 `expires_at` + CHECK 约束兜底完整性 —— 所有查询都按 PK / sweep 范围。

### 安全字段决策

| 列 | 决定 | 理由 |
|---|---|---|
| `commander_sessions.session_id_hash` (sha256 hex) | **存哈希,不存明文** | DBA / 备份 / 慢查询日志 / SQL injection (即便发生) 无法直接拿到 cookie 等价物 |
| `commander_sessions.access_token` | **删除** | commander 走 cookie/identity 即可。access_token 在登录闭环外没人用(`auth.go:197` 的 cookie 路径返回 cached identity);仅在 Bearer fallback 时由 resolver 重新校验,不需要这里持有 |
| `commander_logins.device_code` | 存明文(10 min 自动失效) | 这是 PollOnce 唯一需要的输入。哈希后无法用,跟今天 in-memory 同等敏感 |
| `commander_logins.failure` | 256 字节 CHECK + 必经 SanitizeFailure | 防 OAuth raw body / token 字符串入库 |
| `commander_logins.login_id` | 明文(它在 URL 里,客户端持有) | 哈希无收益:谁拿到 login_id 谁就能 poll,跟 cookie 不同 |

### 已知留痕

`MarkLoginDone` 强一致后,**无孤儿 session**。两个 pod 都走完 [C1]:第一个 UPDATE 成功 → INSERT session;第二个 UPDATE WHERE 不命中 → 事务回滚 → session 不入表 → 调用方拿 ErrNotFound。

唯一留痕:[C1] 写 session 到 [B] 消费 cookie 之间客户端断开,session 在 DB 占 12 h 后被 sweep 清。`sid` 16 字节 / 128 bit / `crypto/rand`,客户端从未拿到 → 穷举攻击不可行。可接受。

## 8. Sweep + cap

每个 pod 在 `MountAll` 末尾 `go auth.runSweep(time.Hour)`,跟进程同生死。

```go
func (a *Authenticator) runSweep(interval time.Duration) {
    t := time.NewTicker(interval)
    defer t.Stop()
    for range t.C {
        sweepCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        loginsDel, sessionsDel, err := a.store.SweepExpired(sweepCtx)
        cancel()
        if err != nil {
            log.Printf("commanderhub: sweep error: %v", err)
            continue
        }
        if loginsDel > 0 || sessionsDel > 0 {
            log.Printf("commanderhub: sweep removed %d logins, %d sessions",
                loginsDel, sessionsDel)
        }
    }
}
```

POST /login 的 cap **必须**用强一致路径(Codex Stage 1 R2 blocker #2:RC 下并发可超 cap 任意多)。选 `pg_advisory_xact_lock`:**事务期间持一个全局常量锁**,所有 ReserveLogin 串行化。`commander_logins.cap` 操作 ≤ 数毫秒,1024 是安全的并发上限。

```sql
-- ReserveLogin (Postgres, BEGIN/COMMIT 在 store 代码里):
BEGIN;
SELECT pg_advisory_xact_lock(8442987421341);          -- arbitrary const, scoped 到 commander_logins
DELETE FROM commander_logins WHERE expires_at < now();
INSERT INTO commander_logins (login_id, expires_at)
SELECT $1, $2
WHERE (SELECT count(*) FROM commander_logins) < 1024
RETURNING login_id;
COMMIT;
```

`RETURNING` 行数 = 0 → 返回 `ErrCapped`(不报 SQL 错;由 store 实现侧把"未插入"翻译成 `ErrCapped`)。`pg_advisory_xact_lock` 在事务提交/回滚时自动释放,不会泄漏。

inmemory 实现:`sync.Mutex` + `len(map) < 1024`(同样严格保证)。

advisory lock 常量 `8442987421341` 应集中定义在 `authstore/postgres.go` 一个 const,加注释解释它的 namespace。其他 Postgres 表如有 advisory lock 协同也用同一文件 const 区段。

**已知:已完成的 `[C1]` 行(`session_id_hash IS NOT NULL`)仍占 cap 直到 `loginTTL = 10 min` 过期**,因为新版本不在 [C1] 同步 ConsumeLogin。1024 上限下不构成实际限制(每分钟峰值 ≤ 100 次成功登录就远低于 cap 周转率)。如果将来上线发现 cap 被堆挤,简单对策是 [C1] 成功后调度一次 `WithoutCancel + 5s timeout` 的 `ConsumeLogin`(本 pod 自己最先看到 done,所以一致性不变)—— Stage 2 plan 评估是否本次就做。

inmemory `ReserveLogin` 实现:`mu.Lock` → sweep expired → check len → insert → unlock。

## 9. 进程接线

`cmd/observer-server/main.go`:

```go
// 在 buildIdentityResolver 之后、observerweb.NewWithResolverOptions 之前

var authStore authstore.Store
switch cfg.Store.Driver {
case "postgres":
    if err := authstore.MigratePostgres(st.DB()); err != nil {
        log.Fatalf("commanderhub authstore migrate: %v", err)
    }
    authStore = authstore.NewPostgresStore(st.DB())
case "sqlite", "":
    // dev 单 pod。生产路径绝不会走到这里 —— validateConfig 已经守住
    // production && sqlite 这种组合(main.go 现有 validateConfig 用
    // allow_sqlite_in_production gate)。
    log.Printf("commanderhub: using in-memory store (driver=%q is single-pod only)", cfg.Store.Driver)
    authStore = authstore.NewInMemoryStore()
default:
    log.Fatalf("commanderhub: unsupported store.driver %q", cfg.Store.Driver)
}

opts := observerWebOptions(cfg, objects)
opts.AuthStore = authStore

app := observerweb.NewWithResolverOptions(st, usHandler, resolver, opts)
```

`runMigrationsOnly` 同步加上:

```go
if cfg.Store.Driver == "postgres" {
    if err := authstore.MigratePostgres(st.DB()); err != nil {
        return fmt.Errorf("commanderhub authstore migrate: %w", err)
    }
}
```

`internal/observerweb/server.go`:

```go
type Options struct {
    ...
    AgentserverURL string
    AuthStore      authstore.Store
}

// NewWithResolverOptions:
if opts.AgentserverURL != "" {
    store := opts.AuthStore
    if store == nil {
        // 不静默退到 inmemory。Codex Stage 1 设计点:静默 fallback 是生产
        // 回到 in-memory bug 状态的最大风险源。要求 main.go 必须显式注入,
        // 单元测试若需要 commander 路由,自己注 NewInMemoryStore()。
        panic("observerweb: AuthStore is required when AgentserverURL is set")
    }
    commanderhub.MountAll(mux, resolver, opts.AgentserverURL, store)
}
```

`internal/commanderhub/wiring.go`:

```go
func MountAll(mux *http.ServeMux, resolver identity.Resolver,
              agentserverURL string, store authstore.Store) {
    hub := NewHub(resolver)
    auth := NewAuthenticator(resolver, agentserverURL, store)
    mux.Handle("/api/daemon-link", hub)
    Mount(mux, hub, auth)
    MountWeb(mux)
    go auth.runSweep(time.Hour)
}
```

## 10. 测试

### authstore 包

- **conformance_test.go** (suffix `_test.go`,Codex Stage 1 nit):导出 `RunConformanceTests(t *testing.T, factory func(t *testing.T) Store)`,覆盖:
  - ReserveLogin OK → GetLogin 看到 reserved (DeviceCode="")
  - ReserveLogin 满 cap → ErrCapped;sweep expired 后又能入
  - FinalizeReservedLogin OK → GetLogin 看到字段全
  - FinalizeReservedLogin 二次调用 → ErrNotFound(不能覆盖)
  - DeleteLogin 释放名额 → cap 重新可入
  - GetLogin / GetSession 未存在 → ErrNotFound
  - MarkLoginDone 单线程成功 → session 表有行(按 hash 查),login 标 done
  - **MarkLoginDone 并发:N 个 goroutine 各自不同 sid → 恰 1 个赢,其余 ErrNotFound,session 表恰 1 行**(强一致)
  - MarkLoginDone 在 failed/done/expired 上 → ErrNotFound,session 不写
  - MarkLoginFailed 在 pending 上 OK;在 done/failed/expired 上 → ErrNotFound
  - MarkLoginFailed 输入超 256 字符 → store 实现侧拒绝或截断(契约:caller 已 sanitize,但 store 也守底线 = CHECK 约束)
  - ConsumeLogin reserved / pending / done / failed 四态都能拿出 + 删
  - ConsumeLogin one-shot:第二次拿 ErrNotFound
  - SetPollThrottle 写完 GetLogin 验 interval_seconds + next_poll_at;不存在 lid 返 nil
  - GetSession 用明文 sid 查 → hash 命中;用错的 sid → ErrNotFound
  - GetSession 过期 → ErrNotFound(SQL/逻辑层过滤)
  - DeleteSession 幂等,DeleteSession 后 GetSession ErrNotFound
  - SweepExpired 不动未过期、删过期、count 正确、空表 0,0,nil
  - 并发 ConsumeLogin:N 个 goroutine 同 lid → 恰 1 拿 row,其余 ErrNotFound
- **inmemory_test.go**:`func TestInMemoryStore(t *testing.T) { RunConformanceTests(t, func(t) Store { return NewInMemoryStore() }) }`
- **postgres_test.go**:`func TestPostgresStore(t *testing.T) { RunConformanceTests(t, openPgFromDSN) }`,`OBSERVER_POSTGRES_TEST_DSN` 空则 `t.Skip`。每用例 `TRUNCATE commander_logins, commander_sessions` 隔离;同 isolation level 在 CI 复现
- **sql_dialect_test.go**:仿 `internal/userspace/store_postgres_test.go:21`,recording `*sql.DB` 截 SQL 字符串,正则验证:
  - 无 `?` 占位符(必须 `$1`)
  - 无 `INSERT OR REPLACE` / `AUTOINCREMENT` / `PRAGMA`
  - **每个动态 SQL 都有 args 列表(参数化)** —— 用 `strings.Contains(query, "%s")` 或类似启发式标错
  - 无需 DSN
- **sanitize_test.go**:`SanitizeFailure` 单元测试
  - 截断 257 字符 → 256
  - 输入包含 `Bearer xxx` / `access_token=xxx` / `id_token=xxx` / 类 JWT (三段 base64) → 输出不含
  - 已知错误模式映射成枚举
  - `nil` 输入 → `FailureDeviceFlow`(与 SanitizeFailure 实现一致;原 nit 已修)

### Authenticator 层

`auth_test.go` 改造:
- `newAuthenticatorWithFlow(resolver, flow)` → `newAuthenticatorWithFlow(resolver, flow, authstore.NewInMemoryStore())`
- 新的 fake `deviceFlow` 实现 `PollOnce` 四返回(`tokenReady, retryable, slowDown, err`)
- 现有 ServeLogin / ServeLoginPoll / putSession / CommanderIdentity 测试调通
- 新增覆盖 §6 状态机:
  - POST /login:reservation 成功 + cap 触发 429 + RequestCode 错误释放名额
  - [A1]/[A2]/[A3]/[A4 reserved]
  - [B failed]/[B done one-shot]/[B 二次访问 → 404 one-shot]
  - [B done] 来自另一 pod 的写入 → 401 "authorization expired"(因本 pod 无明文 sid 不发 cookie)
  - [C-throttle] next_poll_at > now → pending(没有 PollOnce 调用)
  - [C1] OK → 单次响应 Set-Cookie + 200 ok(新不变式)
  - [C1] **id_token 解析失败 → 入库的 failure 是 SanitizeFailure 枚举之一**(断言完全等于 `string(FailureIDTokenInvalid)`,**不含**任何原始错误文本 / token / device_code)
  - [C2] retryable + slowDown → SetPollThrottle 调用,interval_seconds 增长 5,next_poll_at 推后
  - [C3] retryable=false → 入库 SanitizeFailure 枚举,响应 401
  - [C1] MarkLoginDone 返 ErrNotFound (另一 pod 抢先) → 401 "authorization expired"
  - **客户端在 [C1] PollOnce 完成、MarkLoginDone 前断开**:虽然 client cancel 了 ctx,WithoutCancel 包好的写路径仍执行,store 看到 done;下一跳 /poll 走 [B] 返回 401 让用户重登(不可能既"客户端拿到 cookie"又"DB 没写")
- Cookie 属性断言:`HttpOnly`、`SameSite=Lax`、`Secure` 在 r.TLS / X-Forwarded-Proto=https 下置 true
- Logout 路径:`DeleteSession` 后另一个 Authenticator 实例(同 store)`GetSession` 拿 ErrNotFound

### CommanderIdentity 故障语义测试

新增:
- `GetSession` 返回 `errors.New("db down")` → CommanderIdentity 返 `ok=false`,**不**走 Bearer fallback。理由:store 故障期间允许 Bearer 等于扩大攻击面;让 /api/commander/* 返 401,前端弹错重试。如果是 ErrNotFound,继续走 Bearer fallback。
- 这个语义在 spec § 5 也要写明确

### 跨 pod 集成测

`internal/commanderhub/integration_test.go`(新),DSN-gated:
- 同一个 `*sql.DB` 起两个 Authenticator 实例(模拟 pod A / pod B)
- 子用例 1: pod A `POST /login` → pod B `GET /poll` (pending) → pod B `GET /poll` 拉到 token → Set-Cookie + 200 ok(任意 pod 都能完成 [C1])
- 子用例 2: pod A `POST /login` → pod A 完成 [C1] 拿 cookie → pod B 拿 cookie `GET /api/commander/tree` → 通过(session 跨 pod)
- 子用例 3: pod A logout → pod B 同 cookie → 401(失效跨 pod)
- 子用例 4: pod A 与 pod B 同时 `MarkLoginDone` 同 lid 不同 sid → 恰 1 个 session,输的调用拿 ErrNotFound,/poll 返回 401(强一致)
- 子用例 5: pod A `POST /login` → pod A 完成 [C1] (上次 response 因模拟客户端断开未达) → 客户端重发 `GET /poll` → pod B 收 [B] 路径,看到 done → 401 "authorization expired"(可重新发起 POST /login)
- 子用例 6: 1100 并发 `POST /login` → 恰 1024 个 200(强 cap),其余 429,且只有 1024 次 RequestCode 被调用(用 fake flow counter 验)

### 前端

零修改。轮询循环 / 错误路径完全不动。Stage 3 完成后人工跑一次 e2e 验"§6 ★ 跨 pod 401 → 重登成功" 行为符合预期。

## 11. 安全审视

| 面向 | 风险 | 处理 |
|---|---|---|
| Session cookie DB 泄露(DBA / 备份 / 慢日志 / SQL injection) | 直接拿到 cookie 等价物 | **`commander_sessions.session_id_hash = sha256_hex(plaintext sid)`**;DB 行无 cookie 等价物 |
| `access_token` 落库 | 扩大 token 持有面 | **不持久化**。commander 走 cookie/identity 即可;Bearer 路径让 resolver 当场校验 |
| `commander_logins.device_code` | 10 min 内有效的 device-flow secret | 必须存(PollOnce 的唯一输入);10 min TTL 自然失效;`commander_logins_failure_len` CHECK 约束防泄漏入 failure |
| SQL 注入 | 字符串拼接动态 SQL | 所有 SQL 走 `database/sql` 参数化(`$1`/`$2`);**`sql_dialect_test` 启发式扫拼接** |
| 上游 OAuth body / token / device_code 进入 failure 列与前端 | `MarkLoginFailed` 输入未净化 | **`authstore.SanitizeFailure` 是唯一入口**;`sanitize_test.go` 覆盖已知 token shape;`failure` 列 256 字符 CHECK + store 实现侧二次截断 |
| Session fixation | sid 由 `crypto/rand` 16 字节 hex,Set-Cookie 时才下发 | 不变 |
| Cookie 安全属性 | 沿用今天 `HttpOnly`、`SameSite=Lax`、`Secure`(基于 r.TLS / X-Forwarded-Proto) | 不变;**新增显式 cookie-attribute 测试** |
| Replay one-shot | 并发两个 /poll 同 lid,只许一个拿 cookie | `ConsumeLogin` 由 `DELETE … RETURNING` 实现;**并发 conformance 测固化** |
| `MarkLoginDone` 双赢者 → session 表孤儿 | 强一致:`UPDATE WHERE session_id_hash IS NULL` + 0 rows → 事务回滚 → 输的 caller 拿 ErrNotFound | **conformance 测固化"恰一个 session"** |
| 客户端断开导致 token 丢失 (Stage 1 设计点) | r.Context 取消会杀写路径 | 写路径用 `context.WithoutCancel(ctx)` |
| 上游限流 (slow_down) 被无视 (Stage 1 设计点) | 前端 1.5s × N → agentserver 抖 | `commander_logins.next_poll_at` 持久化节流;[C-throttle] 直接 pending 不调 PollOnce |
| 跨 workspace 越权 | `commander_sessions` 无 workspace_id scoping | sid → identity.WorkspaceID,commanderhub 路由内部按 owner 过滤(不变;§6 ServeLogin 链上不直接处理 owner 隔离) |
| Login 风暴 → agentserver | POST /login 暴打 RequestDeviceCode | `ReserveLogin` 走 `pg_advisory_xact_lock` 串行化(Codex Stage 1 R2),cap=1024 严格;**RequestCode 在 cap 名额持有后才打**;cleanup 路径全部 `WithoutCancel` 防 client cancel 囤积 reservation |
| pod 之间 session 串号 | sid 全局 128-bit | randomID `crypto/rand` 16 bytes |
| login_id URL 泄漏 → 攻击者 race 取 cookie | `login_id` 在 query string,可能进 access log / ingress trace | **本次不修(对应 frontend 改造);spec 显式记录** —— Stage 2/3 评估是否把 login_id 改到 cookie / POST body,或要求 ingress 配 query string scrubbing。生产 Helm chart values 中加 ingress 注释建议 |
| logout 后旧 cookie 在其它 pod 仍能用 | 今天的隐患 | **本变更顺带修**:DeleteSession 在 DB 即刻生效,所有 pod 下次 GetSession ErrNotFound |
| Store DB 故障期间走 Bearer fallback 扩大攻击面 | GetSession 故障时 fallthrough 到 Bearer | **§10 决策**:GetSession 返非 ErrNotFound → CommanderIdentity 返 false,不 fallback;只有 ErrNotFound (no session) 才尝试 Bearer |
| AuthStore 注入回退到 inmemory | 生产可能因配置错误悄悄退回 bug 状态 | **§9 决策**:observerweb 在 `AgentserverURL != ""` 且 `AuthStore == nil` 时 panic |
| Session id 哈希算法升级 | sha256 后续若需要升级 (HKDF / pepper) | 本次 sha256-no-salt,够用;后续可加 `algo` 列做 lazy migration。**Stage 2 plan 评估是否本次就上 pepper(env-supplied 全局秘密)** |

### 隐含修复亮点

1. **logout 后只在该 pod 失效,其它 pod 还认 cookie** — 跨 pod 失效现已生效
2. **客户端断开杀写路径** — `WithoutCancel` 之后,成功换到 token 一定能入库
3. **DB 哈希存 sid** — 即便 DB 泄露,cookie 不可重放

## 12. 迁移 + 上线

1. Code merge → CI build observer image
2. helm upgrade → `migration-job.yaml` 跑 `observer-server --migrate-only` → 建 `commander_logins` + `commander_sessions`
3. Pod 滚动重启 → 新版本接管
4. **冷启动效应**:旧 in-memory state 清零,所有已登录用户被踢一次;之后跨重启不再发生
5. 回滚:`helm rollback`。旧版根本不读 DB,新表静坐无副作用,无需 drop schema

### 不动

- helm chart yaml 文件
- values.yaml 配置 schema
- 现有 `OBSERVER_POSTGRES_TEST_DSN` CI 走法
- 前端
- `cookieName` / TTL 常量
- `identity.Identity` 结构

## 13. Out-of-scope follow-ups

- `login_id` 改用 cookie 或 POST body(消除 query-string 泄漏路径)+ 前端配合
- `commander_sessions.session_id_hash` 加 pepper(env-supplied 全局秘密),lazy migration 列 `algo`
- `commanderhub_pending_logins{}` / `commanderhub_active_sessions{}` Prometheus 指标
- `sessions` 表加 `last_seen_at` 做空转回收(空闲 4h 主动失效)
- observer-server graceful shutdown(其它 ticker 也受益)
- 把 commander 跟 observer 拆 deployment(本变更让这件事变得显然可行,但不在本次)
- 用 `commander_logins.consumed_at` 软删替代 `DELETE RETURNING`,以便对完成的 logins 做事后审计
