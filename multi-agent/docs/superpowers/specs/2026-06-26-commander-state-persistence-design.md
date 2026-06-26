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
| `MarkLoginDone` 防孤儿 | **强一致**:一个事务里 `UPDATE commander_logins SET session_id=… WHERE login_id=$1 AND session_id IS NULL AND failure IS NULL AND expires_at>now()`,RowsAffected=0 → 整个事务 rollback,session 不写,返回 `ErrNotFound`。**§7 的"接受孤儿"段落删除** | 替代:接受孤儿。Codex Stage 1 审 blocker #4 指出语义自相矛盾;选最干净的语义 |
| 服务端节流 `next_poll_at` | `commander_logins.next_poll_at` 列。[C] 分支进入前 `if rec.NextPollAt > now: return pending`;`PollOnce` 后根据返回:`retryable` → `next_poll_at = now + max(5s, Interval)`、`slow_down` → 当前 interval + 5s 持久化 | agentserver `slow_down`/速率防护;不再让前端 1.5 s 转化为后端真的 1.5 s × N 用户 |
| Schema 字段 | 行内列(`user_id`、`workspace_id`、`role`、`source`),不存 JSON、**不存 access_token、不存明文 sid**。`logins` 只持久化 `device_code`、`code_expires_at`、`interval_seconds`、`next_poll_at`、`session_id_hash`、`failure` | 紧凑、可索引、易运维 |
| Failure 文本入库前必须净化 | `sanitizeFailure(err) string`:截断 ≤ 256 字节、剥离上游 raw body / token / device_code / id_token 等 token-shape 子串、统一映射为 `authorization denied` / `authorization expired` / `device flow error`。store 接口标注"failure 必须已 sanitize" | 防 OAuth raw body / token 字符串泄露到 DB 和前端 |
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
  auth.go                            // Authenticator,删 map,持 authstore.Store
  http.go / hub.go / web.go / ...    // 不动
  authstore/                         // 新包
    store.go                         // Store 接口 + LoginRecord/SessionRecord
    inmemory.go                      // inmemoryStore(map + sync.Mutex)
    postgres.go                      // postgresStore(*sql.DB)
    schema_postgres.sql              // 嵌入
    migrate.go                       // MigratePostgres(db *sql.DB)
    conformance.go                   // 公共契约测套件(non-_test.go,可复用)
    inmemory_test.go                 // RunConformanceTests + 纯逻辑
    postgres_test.go                 // RunConformanceTests + SQL 方言 + DSN-gated 集成
    sql_dialect_test.go              // recordingSQLDB 套路,无需 DSN

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

    // SetNextPollAt 持久化服务端节流时刻。幂等;不存在的 lid 返回 nil(本次 /poll 失效不破 SLA)。
    SetNextPollAt(ctx context.Context, loginID string, nextPollAt time.Time) error

    // MarkLoginDone 单事务原子地:
    //   1) UPDATE commander_logins SET session_id_hash=$hash WHERE login_id=$lid
    //        AND session_id_hash IS NULL AND failure IS NULL
    //        AND device_code != '' AND expires_at > now
    //   2) RowsAffected = 0 → ROLLBACK,返回 ErrNotFound
    //   3) RowsAffected = 1 → INSERT INTO commander_sessions ... COMMIT
    //
    // 输入 session.PlaintextSessionID 由实现侧 hash 后写;调用方持有明文用于 Set-Cookie。
    // 输入 ctx 不应在写入路径上被取消(由 Authenticator 用 context.WithoutCancel 包好)。
    MarkLoginDone(ctx context.Context, loginID string, session SessionRecord) error

    // MarkLoginFailed 设 failure 字段(input MUST be sanitized)。
    // 仅在 pending 或 reserved 态成功;终态 / 不存在 / 过期 → ErrNotFound。
    // 由实现侧用 WHERE session_id_hash IS NULL AND failure IS NULL AND expires_at > now 守住。
    MarkLoginFailed(ctx context.Context, loginID, sanitizedFailure string) error

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

### sanitizeFailure(err error) string

集中放在 `internal/commanderhub/authstore/sanitize.go`(纯函数,无 DB 依赖,store 实现侧用,Authenticator 也用)。规则:

1. 长度截断 ≤ 256 字节
2. 把已知错误模式映射成枚举字符串:
   - `errors.Is(err, errAuthorizationDenied)` → `"authorization denied"`
   - `errors.Is(err, errAuthorizationExpired)` → `"authorization expired"`
   - context.DeadlineExceeded → `"upstream timeout"`
   - 其它 → `"device flow error"`
3. 不让 raw HTTP body、access_token、id_token、device_code 字符串进入返回值
4. 是 `PollOnce` / `identityFromIDToken` / 任何上游错误的统一出口;入库前必经

`commander_logins.failure` 列也加 `CHECK (length(failure) <= 256)`。

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
3. dc, err := flow.RequestCode(ctx)
     err != nil:
        store.DeleteLogin(ctx, lid)   // best-effort 释放占位
        return 502 "device flow: <sanitized>"
4. err := store.FinalizeReservedLogin(ctx, lid,
            dc.Code, time.Now().Add(dc.ExpiresIn), int(dc.Interval/time.Second))
     ErrNotFound → 502 "login expired during init"       (极罕见:sweep 抢先)
     err != nil  → 502 "store unavailable"
5. 返回 200 {"verification_uri_complete": dc.VerificationURIComplete, "login_id": lid, "expires_in": ...}
```

Reservation 模式保证 cap 不被 TOCTOU 击穿:无论多少并发请求,Reserve 必先消费 cap 名额。

### GET /api/commander/login/poll (ServeLoginPoll)

```
GET /api/commander/login/poll?id=<lid>

[A] rec, err := store.GetLogin(ctx, lid)
    [A1] ErrNotFound       → 404 "unknown login"
    [A2] 其它 err           → 502 "store unavailable"
    [A3] rec.ExpiresAt<now  → store.ConsumeLogin best-effort, 404 "unknown login"
    [A4] rec.DeviceCode==""  (reserved 但 RequestCode 还没返回 / fail):
                            → 200 {"status":"pending"}     (下一跳由前端 1.5s 节流)

[B] rec.SessionIDHash != "" OR rec.Failure != "" (terminal):
    consumed, err := store.ConsumeLogin(ctx, lid)
    err==ErrNotFound        → 404 "unknown login"          (并发 /poll 抢先,one-shot)
    err!=nil                → 502 "store unavailable"
    consumed.Failure != ""  → 401 {"status":"error","error": consumed.Failure}
                              (Failure 已 sanitized;直接回前端)
    consumed.SessionIDHash != "":
        // 此处需要明文 sid 给客户端做 cookie。Hash 入库的方案要求 MarkLoginDone
        // 调用方持有明文。但 ConsumeLogin 返回的只有 hash,明文已被丢弃。
        // 解法:见下 "★ 明文 sid 怎么从 [C1] 流到 [B]"
        Set-Cookie commander_sess=<plaintext>, 200 {"status":"ok"}

[C] pending (rec.DeviceCode != ""):
    [C-throttle] if rec.NextPollAt > now → 200 {"status":"pending"}   // 服务端节流

    pollCtx := context.WithTimeout(r.Context(), 5*time.Second)
    dc := DeviceCode{Code: rec.DeviceCode, ExpiresIn: time.Until(rec.CodeExpiresAt),
                     Interval: time.Duration(rec.IntervalSeconds)*time.Second}
    tok, ready, retryable, slowDown, perr := flow.PollOnce(pollCtx, dc)

    [C1] ready==true:
        ident, err := identityFromIDToken(tok.IDToken, time.Now())
        if err != nil:
            writeCtx := context.WithoutCancel(ctx)                    // ★ 不被客户端断开打断
            store.MarkLoginFailed(writeCtx, lid, authstore.SanitizeFailure(err))
            return 200 {"status":"pending"}                           // 下一跳走 [B]
        sid := randomID()
        writeCtx := context.WithoutCancel(ctx)                        // ★
        err = store.MarkLoginDone(writeCtx, lid, SessionRecord{
            PlaintextSessionID: sid,                                  // store 内部 hash
            Identity:           ident,
            ExpiresAt:          time.Now().Add(sessionTTL),
        })
        if err == ErrNotFound:
            return 404 "unknown login"
        if err != nil:
            return 502 "store unavailable"

        // ★ 明文 sid 暂存(见下方)
        ourSidByLoginID.Put(lid, sid, ttl=loginTTL)
        return 200 {"status":"pending"}                               // 不本次发 cookie

    [C2] retryable==true:
        delta := time.Duration(rec.IntervalSeconds)*time.Second
        if slowDown: delta += 5*time.Second
        if delta < 5*time.Second: delta = 5*time.Second
        nextPollAt := time.Now().Add(delta)
        if slowDown:
            // 持久化 interval 增长,后续所有 pod 都尊重
            store.FinalizeReservedLogin? — 不,改用 store.SetNextPollAt(ctx, lid, nextPollAt)
        else:
            store.SetNextPollAt(ctx, lid, nextPollAt)                 // best-effort
        return 200 {"status":"pending"}

    [C3] retryable==false:
        writeCtx := context.WithoutCancel(ctx)
        store.MarkLoginFailed(writeCtx, lid, authstore.SanitizeFailure(perr))
        return 200 {"status":"pending"}                               // 下一跳走 [B] 返回 401
```

### ★ 明文 sid 怎么从 [C1] 流到 [B]

挑战:`MarkLoginDone` 把 hash 入库,明文丢失;下一跳 /poll 走 [B] 时 `ConsumeLogin` 只能拿回 hash,不能下发 cookie。

解法:`Authenticator` 持一个内存 `sidByLoginID map[string]string`(per-pod,key=login_id, value=明文 sid)。当 [C1] 写完 hash 即写本 map(TTL=loginTTL)。[B] 分支命中 `consumed.SessionIDHash != ""` 时:

1. 优先从 `sidByLoginID[lid]` 取明文。**有 → Set-Cookie 直接下发**(本 pod 自己刚刚写的)
2. 没有(说明 [C1] 发生在另一 pod):跨 pod 拿不回明文,**这是接受的代价** —— 返回 401 `{"status":"error","error":"login completed on another pod; please retry"}`,前端弹错让用户点重登

代价合理性:
- 单 pod 部署:0 跨 pod 跳,永远命中
- 3 pod round-robin:[C1] 在 pod X、[B] 在 pod Y 概率 ≈ 2/3。**但 [B] 在 [C1] 之后才发生(前端 1.5s 后下一跳),命中同 pod 概率 1/3** —— 期望约 33% 的登录第一次拿 401,前端会有"登录失败:..."提示,**点登录按钮再试一次**(那次的 cap 名额仍在,reservation 已用)。重试期望 1.5 跳成功

**这个跨 pod 跳不命中的概率是设计代价**。可接受替代:把 sid plaintext 缓存到 Postgres(那就违背了哈希存储的初衷)。Stage 2 时如果用户嫌弃,可引入"sid 短窗 + 自动重试"前端逻辑;**Stage 1 spec 锁这个语义**。

### 关键不变式

> **所有终态(`ok`/`error`)HTTP 响应,都从 [B] 分支的 `ConsumeLogin RETURNING` 出。**

[C1] / [C3] 只"写终态",[B] 才"消费终态"。一次 /poll 只做一件事。代价:用户看到终态最多延迟 1.5 s。

### 客户端断开

`pollCtx` 派生自 `r.Context()`,用于 `PollOnce`(网络往返,可取消)。
**写路径(MarkLoginDone / MarkLoginFailed)用 `context.WithoutCancel(ctx)`** 包,客户端断开不打断 DB 写入 —— Codex Stage 1 设计点:成功换到 token 后绝不能因为客户端断开丢失,否则下次 /poll 又会去 agentserver 再换一次,而 agentserver 早已把 device_code 标 used。

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
    CONSTRAINT commander_logins_login_id_nonempty CHECK (length(login_id) > 0)
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

POST /login 的 cap **不再**通过分立的 `count() + insert`(TOCTOU 已在 § 5 决策 + § 6 ServeLogin 步骤里说明)。`store.ReserveLogin` 单 SQL 原子完成"sweep expired + count + insert reservation",cap = 1024。Postgres 实现示例:

```sql
WITH
  swept AS (
    DELETE FROM commander_logins WHERE expires_at < now()
  ),
  current AS (
    SELECT count(*) AS n FROM commander_logins
  ),
  inserted AS (
    INSERT INTO commander_logins (login_id, expires_at)
    SELECT $1, $2 FROM current WHERE current.n < 1024
    RETURNING login_id
  )
SELECT (SELECT count(*) FROM inserted) AS inserted_rows;
```

`inserted_rows = 0` → 返回 ErrCapped。多 pod 并发:每个事务里 CTE `current` 拿到的是 MVCC snapshot,但 `INSERT` 受唯一性约束 + isolation level,**正确的强保证需要 REPEATABLE READ 或 SERIALIZABLE**。Stage 2 plan 段评估两个方案:
- 方案 A:用 `SERIALIZABLE` + retry
- 方案 B:用 advisory lock (`SELECT pg_advisory_xact_lock(<const>)`),用一个全局 1024 闸门
- 方案 C:`INSERT ... SELECT ... WHERE (SELECT count(*) FROM commander_logins) < 1024` 在 RC 下也能跑,小概率超 cap 几个 ≤ 副本数,可接受

**默认方案 C**:简单,小幅超 cap (≤ 3 行,集群副本数级) 无关紧要,后续要严格再加 advisory lock。inmemory 实现用 `sync.Mutex` + `len(map) < 1024` 严格保证。

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
  - SetNextPollAt OK + 不存在 lid 返 nil
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
  - `nil` 输入 → 空串

### Authenticator 层

`auth_test.go` 改造:
- `newAuthenticatorWithFlow(resolver, flow)` → `newAuthenticatorWithFlow(resolver, flow, authstore.NewInMemoryStore())`
- 新的 fake `deviceFlow` 实现 `PollOnce` 四返回(`tokenReady, retryable, slowDown, err`)
- 现有 ServeLogin / ServeLoginPoll / putSession / CommanderIdentity 测试调通
- 新增覆盖 §6 状态机:
  - POST /login:reservation 成功 + cap 触发 429 + RequestCode 错误释放名额
  - [A1]/[A2]/[A3]/[A4 reserved]
  - [B done]/[B failed]/[B one-shot] —— 第一次 cookie OK,第二次拿 404
  - [B done] **明文 sid 已在 sidByLoginID** vs **不在(模拟跨 pod)→ 401**(覆盖 §6 ★ 段落)
  - [C-throttle] next_poll_at > now → pending(没有 PollOnce 调用)
  - [C1] OK → 写 sid 到 sidByLoginID;下一跳 /poll cookie
  - [C1] **id_token 解析失败 → 入库的 failure 是 sanitized 串**(断言不含 token)
  - [C2] retryable + slowDown → next_poll_at 增量
  - [C3] retryable=false → 入库 sanitized failure
  - [C1] 写 done 时 store 返 ErrNotFound (sweep 抢先) → 404
  - **客户端在 [C1] PollOnce 完成、MarkLoginDone 前断开 → MarkLoginDone 仍执行(WithoutCancel)**
- Cookie 属性断言:`HttpOnly`、`SameSite=Lax`、`Secure` 在 r.TLS / X-Forwarded-Proto=https 下置 true
- Logout 路径:`DeleteSession` 后另一个 Authenticator 实例(同 store)`GetSession` 拿 ErrNotFound

### CommanderIdentity 故障语义测试

新增:
- `GetSession` 返回 `errors.New("db down")` → CommanderIdentity 返 `ok=false`,**不**走 Bearer fallback。理由:store 故障期间允许 Bearer 等于扩大攻击面;让 /api/commander/* 返 401,前端弹错重试。如果是 ErrNotFound,继续走 Bearer fallback。
- 这个语义在 spec § 5 也要写明确

### 跨 pod 集成测

`internal/commanderhub/integration_test.go`(新),DSN-gated:
- 同一个 `*sql.DB` 起两个 Authenticator 实例(模拟 pod A / pod B)
- 子用例 1: pod A `POST /login` → pod B `GET /poll` (pending) → pod B `GET /poll` ([C1] 在 pod B,sid 进 B 的 sidByLoginID) → pod B `GET /poll` 拿 cookie ([B] 同 pod, 成功)
- 子用例 2: pod A `POST /login` → pod B `GET /poll` (pending) → pod A `GET /poll` ([C1] 在 pod A,sid 进 A 的 sidByLoginID) → pod B `GET /poll` ([B] 在 pod B 拿不到明文 → 401)
- 子用例 3: pod A 登入拿 cookie → pod B `/api/commander/tree` mock CommanderTree → 通过
- 子用例 4: pod A logout → pod B 同 cookie → 401
- 子用例 5: pod A `MarkLoginDone` 进行中,pod B 同时 `MarkLoginDone` → 恰 1 个 session,一个调用拿 ErrNotFound

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
| Login 风暴 → agentserver | POST /login 暴打 RequestDeviceCode | `ReserveLogin` 单 SQL 原子 sweep+count+insert,cap=1024;**RequestCode 在 cap 名额持有后才打** |
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
- `MarkLoginDone` 改 `pg_advisory_xact_lock` 严格防 cap 超额(目前 ≤ 副本数轻微超额)
