# Commander State Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-06-26-commander-state-persistence-design.md`

**Goal:** Move commander login + session state from in-memory maps to Postgres so any of the 3 observer-server replicas can serve any commander request, and pod rolling restart no longer kicks logged-in users. Resolves the `登录失败: HTTP 404` symptom at `https://loom.nj.cs.ac.cn:10062/commander`.

**Architecture (from spec):**
- New package `internal/commanderhub/authstore` with `Store` interface + `postgresStore` + `inmemoryStore` + `Failure` enum + `SanitizeFailure`.
- New tables `commander_logins` + `commander_sessions` with hash-stored session IDs, `pg_advisory_xact_lock`-serialized cap, enum-CHECK failure column, terminal-state CHECK invariants.
- `Authenticator` loses both in-memory maps and the `pollLogin` background goroutine. `/poll` is now synchronous: `[C1]` Set-Cookie+200 in one response. `[B]` only handles "another pod wrote terminal" → 401 "authorization expired".
- All unkillable DB writes use `Authenticator.writeCtx(ctx)` = `WithTimeout(WithoutCancel(ctx), 5s)`.
- New `observer-server` wiring: postgres driver → `authstore.NewPostgresStore`, sqlite/empty → `inmemoryStore`, anything else → fatal.
- `observerweb.Options.AuthStore` required if `AgentserverURL != ""` (panic if nil — no silent in-memory fallback in production).
- 1h ticker per pod for `SweepExpired`.

**Tech Stack:** Go 1.26 (see `go.mod`), `database/sql`, `pgx/v5` stdlib, `crypto/sha256`, `crypto/rand`. Postgres 16+ for the prod path (`pg_advisory_xact_lock`, `CHECK`, `RETURNING`). Tests via standard `testing`, `github.com/stretchr/testify/require`, `OBSERVER_POSTGRES_TEST_DSN` for the DSN-gated integration suite. Frontend unchanged.

## Global Constraints

The following come from the spec and bind every task:

- **No frontend changes.** The login UX, polling cadence, and error rendering stay identical. Stage 3 manual e2e verifies the cross-pod 401 → user-retry path works.
- **No new deployment components.** Reuse `st.DB()` from observer-server's existing pool. No new helm chart yaml.
- **DB-only state.** `Authenticator` holds no `sync.Mutex` over login/session state. Sweep ticker is the only goroutine.
- **`context.WithoutCancel(ctx) + WithTimeout(5s)`** wraps every write that must survive client disconnect. Pattern is `Authenticator.writeCtx(ctx)` helper. Direct `WithoutCancel(ctx)` without `WithTimeout` is a code-review-blocking smell.
- **Plaintext sid never enters DB or parameterized SQL.** Hash with `sha256_hex` in `authstore` before any `database/sql.QueryContext` call. The plaintext sid lives on the wire (cookie) and inside `Authenticator.ServeLoginPoll` stack only.
- **`access_token` is NOT persisted.** Commander uses cookie/identity. Bearer fallback re-resolves on each request via the existing `resolver.Resolve` path.
- **Failure column accepts only enum values.** Compiler guard via `authstore.Failure` newtype + DB CHECK `failure IN ('authorization denied', ...)`. `failure.go` constants and `schema_postgres.sql` CHECK list MUST move together (Task 8 self-check).
- **No `CountActiveLogins`-then-`InsertLogin` pattern.** Only `ReserveLogin` (single SQL inside advisory-lock-tx) creates new login rows.
- **All Go SQL goes through `database/sql` parameterized args (`$1, $2, ...`).** No string concatenation of dynamic values. Task 6 explicitly verifies via a `recordingSQLDB` test.
- **Conformance test suite shared by both store implementations.** Behavior divergence between `inmemoryStore` and `postgresStore` is a Stage 3 blocker, caught by `authstore.RunConformanceTests`.
- **Working directory:** `/root/multi-agent/.claude/worktrees/commander-state-persistence/multi-agent`. All `go test` commands run from there. Use `go test ./internal/commanderhub/...` for scoped runs, `go test ./...` before declaring Stage 3 done.
- **TDD where the test is cheap to write first** (failure.go, sanitize, store conformance, dialect). For wiring-heavy steps (observerweb/main.go), write the code then add the small targeted test — these are integration shims with tiny behavioral surface.
- **Codex review at end of Stage 2** (this plan) AND end of Stage 3 (the code). Iterate until codex returns APPROVED.

---

## File Structure

**New files:**

```
internal/commanderhub/authstore/
  store.go                  # Store interface + LoginRecord + SessionRecord + sentinels
  failure.go                # Failure newtype + enum constants + SanitizeFailure +
                            # PollOnce sentinel errors (errAuthorizationDenied etc.)
  inmemory.go               # inmemoryStore (sync.Mutex + maps)
  postgres.go               # postgresStore (*sql.DB) + advisory-lock const
  schema_postgres.sql       # embedded
  migrate.go                # MigratePostgres(db *sql.DB)

  conformance_test.go       # exported RunConformanceTests(t, factory)
  inmemory_test.go          # RunConformanceTests(NewInMemoryStore)
  postgres_test.go          # RunConformanceTests(openPgFromDSN)
  sql_dialect_test.go       # recordingSQLDB, no DSN
  failure_test.go           # SanitizeFailure enum coverage
```

**Modified files:**

```
internal/commanderhub/
  auth.go                   # heavy: removed maps + pollLogin; added writeCtx + new state machine
  auth_test.go              # rewritten fake deviceFlow; new test cases for state machine
  wiring.go                 # MountAll signature gains store + ctx for sweeper; starts ticker
  http.go                   # unchanged (commander handlers unaffected)
  integration_test.go       # NEW: cross-pod integration tests (DSN-gated)

internal/observerweb/
  server.go                 # Options.AuthStore + panic guard

cmd/observer-server/
  main.go                   # store construction + Migrate call in both main + runMigrationsOnly
  main_test.go              # if existing tests broke, fix narrowly
```

**Untouched:**
- All other `internal/commanderhub/*.go`
- `internal/commanderhub/webapp/`
- `deploy/charts/observer/`
- `internal/observerstore/*`
- `cmd/{driver,master,slave}-agent/`

---

## Task Ordering

Tasks ordered so each step is green-bar before the next. The `authstore` package goes first (bottom-up) so `Authenticator` can be reworked on top of a known-good store. Wiring lands last.

1. **Task 1:** `authstore.Failure` + `SanitizeFailure` + sentinel errors  (no DB; pure)
2. **Task 2:** `authstore.Store` interface + `LoginRecord` + `SessionRecord` + sentinels  (just types)
3. **Task 3:** `authstore.NewInMemoryStore` implementation  (proves the interface)
4. **Task 4:** `authstore.RunConformanceTests` suite  (drives Task 3 & later Task 6)
5. **Task 5:** `authstore.MigratePostgres` + `schema_postgres.sql`  (DDL only; idempotent)
6. **Task 6:** `authstore.NewPostgresStore` implementation  (passes conformance + dialect + DSN-gated)
7. **Task 7+8 (combined commit):** rewrite `deviceFlow` seam (`PollOnce` + sentinel errors) **and** `Authenticator` (Store + new state machine + `writeCtx`). They MUST land together because today's `pollLogin` goroutine calls `PollToken` — replacing one alone breaks the build. Detailed sections below keep the Task 7 / Task 8 split for clarity but the workflow ships them in one commit, e.g. via the worker doing both task lists end-to-end before running `go test`.
9. **Task 9:** `internal/commanderhub/wiring.go` — `MountAll` signature change + sweep ticker
10. **Task 10:** `internal/observerweb/server.go` — `Options.AuthStore` + panic guard
11. **Task 11:** `cmd/observer-server/main.go` — store construction + `MigratePostgres` in main & `--migrate-only`
12. **Task 12:** Cross-pod integration test (`commanderhub/integration_test.go`, DSN-gated)
13. **Task 13:** End-to-end verification + Stage 3 codex review prep

---

### Task 1: `authstore.Failure` + `SanitizeFailure` + sentinel errors

**Files:**
- Create: `internal/commanderhub/authstore/failure.go`
- Create: `internal/commanderhub/authstore/failure_test.go`

**Interfaces:**
- Exports: `Failure` (string newtype), constants `FailureAuthorizationDenied`, `FailureAuthorizationExpired`, `FailureUpstreamTimeout`, `FailureIDTokenInvalid`, `FailureDeviceFlow`, `FailureStoreUnavailable`.
- Exports: `SanitizeFailure(err error) Failure`.
- Exports: `ValidFailure(f Failure) bool`. Whitelist of the six constants above.
- Exports: sentinel errors `ErrAuthorizationDenied`, `ErrAuthorizationExpired`, `ErrIDTokenInvalid`, `ErrInvalidFailure`.

- [ ] **Step 1: Write failing tests in `failure_test.go`**

```go
package authstore

import (
    "context"
    "errors"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestSanitizeFailure_EnumOnly(t *testing.T) {
    cases := []struct {
        name string
        err  error
        want Failure
    }{
        {"nil → DeviceFlow defensive", nil, FailureDeviceFlow},
        {"ErrAuthorizationDenied", ErrAuthorizationDenied, FailureAuthorizationDenied},
        {"ErrAuthorizationDenied wrapped", &wrappedErr{ErrAuthorizationDenied}, FailureAuthorizationDenied},
        {"ErrAuthorizationExpired", ErrAuthorizationExpired, FailureAuthorizationExpired},
        {"ErrIDTokenInvalid", ErrIDTokenInvalid, FailureIDTokenInvalid},
        {"context.DeadlineExceeded", context.DeadlineExceeded, FailureUpstreamTimeout},
        {"random unknown error containing token shape",
            errors.New("upstream returned access_token=eyJxxx.yyy.zzz and Bearer abc123"),
            FailureDeviceFlow},
        {"raw JSON body unknown error",
            errors.New(`{"error":"slow_down","raw_token":"super-secret"}`),
            FailureDeviceFlow},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := SanitizeFailure(tc.err)
            require.Equal(t, tc.want, got)
            // Belt-and-suspenders: result MUST be one of the declared enum values.
            require.Contains(t, allFailureValues, got)
        })
    }
}

func TestFailureEnumLengthSanity(t *testing.T) {
    // schema CHECK requires <= 256
    for _, f := range allFailureValues {
        require.LessOrEqual(t, len(string(f)), 256)
    }
}

func TestValidFailure_RejectsRawString(t *testing.T) {
    require.False(t, ValidFailure(Failure("anything custom")), "unforged enum values must be rejected")
    require.False(t, ValidFailure(Failure("")), "empty must be rejected")
    require.False(t, ValidFailure(Failure("authorization-denied")), "near-miss must be rejected")
    for _, f := range allFailureValues {
        require.True(t, ValidFailure(f), "%q is in allowlist but ValidFailure said false", f)
    }
}

// helper for "all valid enum values" — change here when adding a constant
var allFailureValues = []Failure{
    FailureAuthorizationDenied,
    FailureAuthorizationExpired,
    FailureUpstreamTimeout,
    FailureIDTokenInvalid,
    FailureDeviceFlow,
    FailureStoreUnavailable,
}

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }
```

- [ ] **Step 2: Run `go test ./internal/commanderhub/authstore/ -run TestSanitizeFailure` — confirm it fails to compile (no package yet).**

- [ ] **Step 3: Implement `failure.go`**

```go
package authstore

import (
    "context"
    "errors"
)

// Failure is the only string type accepted into commander_logins.failure.
// The DB enforces `failure IN (...enum values...)`. SanitizeFailure is the
// only blessed constructor.
type Failure string

const (
    FailureAuthorizationDenied  Failure = "authorization denied"
    FailureAuthorizationExpired Failure = "authorization expired"
    FailureUpstreamTimeout      Failure = "upstream timeout"
    FailureIDTokenInvalid       Failure = "id token invalid"
    FailureDeviceFlow           Failure = "device flow error"
    FailureStoreUnavailable     Failure = "store unavailable"
)

// Sentinel errors the deviceFlow.PollOnce path returns. Authenticator wraps
// upstream responses (access_denied, expired_token, ...) and id-token parse
// failures into one of these — never propagating raw HTTP body or token text.
var (
    ErrAuthorizationDenied  = errors.New("authstore: authorization denied")
    ErrAuthorizationExpired = errors.New("authstore: authorization expired")
    ErrIDTokenInvalid       = errors.New("authstore: id token invalid")
)

// SanitizeFailure maps an upstream / id-token / context error into one of the
// six enum Failure constants. Fail-closed: unknown errors degrade to
// FailureDeviceFlow rather than echoing the original text.
func SanitizeFailure(err error) Failure {
    switch {
    case err == nil:
        return FailureDeviceFlow
    case errors.Is(err, context.DeadlineExceeded):
        return FailureUpstreamTimeout
    case errors.Is(err, ErrAuthorizationDenied):
        return FailureAuthorizationDenied
    case errors.Is(err, ErrAuthorizationExpired):
        return FailureAuthorizationExpired
    case errors.Is(err, ErrIDTokenInvalid):
        return FailureIDTokenInvalid
    default:
        return FailureDeviceFlow
    }
}

// ValidFailure is the runtime allowlist for Failure values. The Go type
// system cannot prevent `Failure(rawErr)` strong conversion; ValidFailure +
// store enforcement guard the persistence boundary. Postgres CHECK is the
// last line of defense.
func ValidFailure(f Failure) bool {
    switch f {
    case FailureAuthorizationDenied, FailureAuthorizationExpired,
         FailureUpstreamTimeout, FailureIDTokenInvalid,
         FailureDeviceFlow, FailureStoreUnavailable:
        return true
    }
    return false
}

// ErrInvalidFailure is returned by Store.MarkLoginFailed when the input
// Failure value is not in the allowlist.
var ErrInvalidFailure = errors.New("authstore: invalid failure value")
```

- [ ] **Step 4: Run `go test ./internal/commanderhub/authstore/...` — both tests pass.**

- [ ] **Step 5: Sanity scan: `git grep '"authorization denied"' internal/commanderhub/authstore` should be 1 hit in `failure.go`. Plan Task 5 will add a second hit in `schema_postgres.sql`.**

---

### Task 2: `authstore.Store` interface + record types + sentinels

**Files:**
- Create: `internal/commanderhub/authstore/store.go`

**Interfaces:** (exactly the spec § 5 interface; reproduced here as source of truth)

- [ ] **Step 1: Create `store.go` with full interface**

```go
package authstore

import (
    "context"
    "errors"
    "time"

    "github.com/yourorg/multi-agent/internal/identity"
)

// Sentinels returned by Store methods.
var (
    // ErrNotFound: lookup miss. Authenticator translates this to 404 / 401 /
    // "another pod won" depending on the call site.
    ErrNotFound = errors.New("authstore: not found")

    // ErrCapped: ReserveLogin refused because >= 1024 unexpired logins exist.
    // Authenticator translates to HTTP 429.
    ErrCapped = errors.New("authstore: pending logins cap reached")
)

// LoginRecord is the semantic view of a commander_logins row.
//
// State machine:
//   reserved: DeviceCode == "" && Failure == "" && SessionIDHash == ""
//   pending:  DeviceCode != "" && Failure == "" && SessionIDHash == ""
//   failed:   Failure != "" (terminal)
//   done:     SessionIDHash != "" (terminal)
//
// Mutual exclusion enforced by commander_logins_terminal_xor CHECK in
// schema_postgres.sql. Sweep removes rows with expires_at < now() regardless
// of state.
type LoginRecord struct {
    LoginID         string
    DeviceCode      string    // "" while reserved
    CodeExpiresAt   time.Time // zero while reserved
    IntervalSeconds int       // > 0 once finalized
    NextPollAt      time.Time
    ExpiresAt       time.Time
    SessionIDHash   string    // hex(sha256(plaintext sid)); terminal=done
    Failure         Failure   // terminal=failed
}

// SessionRecord is the semantic view of a commander_sessions row plus
// PlaintextSessionID (used only by InsertSession / GetSession entry; never
// persisted).
type SessionRecord struct {
    PlaintextSessionID string // in-flight only; store hashes before write
    Identity           identity.Identity
    ExpiresAt          time.Time
}

// Store persists commander login + session state across all observer-server
// replicas. All methods must be safe for concurrent use.
type Store interface {
    // -- logins --

    // ReserveLogin atomically:
    //   1. sweep expired rows (preventing zombies from stealing cap slots),
    //   2. check cap (>= 1024 → ErrCapped),
    //   3. insert reservation row (DeviceCode="", ExpiresAt = now+ttl).
    //
    // Postgres implementation uses pg_advisory_xact_lock for strict serialization.
    // inmemory implementation uses sync.Mutex.
    ReserveLogin(ctx context.Context, loginID string, now time.Time, ttl time.Duration) error

    // FinalizeReservedLogin fills RequestCode's fields onto a reservation row.
    // Targets WHERE login_id=$lid AND device_code = ''. If the row is not in
    // reserved state (sweep raced, double-call, …) returns ErrNotFound.
    FinalizeReservedLogin(ctx context.Context, loginID string,
        deviceCode string, codeExpiresAt time.Time, intervalSeconds int) error

    // DeleteLogin releases a reservation slot. Idempotent: missing → nil.
    // Called only on the post-Reserve failure path (RequestCode err, or
    // client cancelled before Finalize completed).
    DeleteLogin(ctx context.Context, loginID string) error

    // GetLogin returns the current row unchanged. ErrNotFound for missing.
    // Caller decides whether ExpiresAt < now means "expired".
    GetLogin(ctx context.Context, loginID string) (LoginRecord, error)

    // SetPollThrottle updates both interval_seconds and next_poll_at in one
    // SQL. Idempotent: missing lid → nil (best-effort throttle).
    // intervalSeconds must be > 0 (CHECK constraint backs this).
    SetPollThrottle(ctx context.Context, loginID string,
        intervalSeconds int, nextPollAt time.Time) error

    // MarkLoginDone is a single tx:
    //   1) UPDATE commander_logins SET session_id_hash=$hash, finalized_at=now()
    //        WHERE login_id=$lid
    //          AND session_id_hash IS NULL AND failure IS NULL
    //          AND device_code != '' AND expires_at > now()
    //   2) RowsAffected = 0 → ROLLBACK, return ErrNotFound
    //   3) INSERT INTO commander_sessions (session_id_hash, ...) ...
    //   4) COMMIT
    //
    // session.PlaintextSessionID is hashed by the store; the caller keeps the
    // plaintext to Set-Cookie. ctx is expected to be Authenticator.writeCtx (i.e.
    // WithoutCancel + 5s timeout) so a client disconnect cannot leave a
    // session row without its login row (or vice versa).
    MarkLoginDone(ctx context.Context, loginID string, session SessionRecord) error

    // MarkLoginFailed sets failure + finalized_at in one statement
    // WHERE session_id_hash IS NULL AND failure IS NULL AND expires_at > now().
    // Terminal / missing / expired → ErrNotFound.
    // sanitizedFailure MUST be the output of SanitizeFailure; the type system
    // and DB CHECK both enforce this.
    MarkLoginFailed(ctx context.Context, loginID string, sanitizedFailure Failure) error

    // ConsumeLogin: atomic SELECT + DELETE. One-shot semantics.
    // Postgres: DELETE FROM commander_logins WHERE login_id=$1 RETURNING …
    // inmemory: lock + map lookup + delete + return.
    // ErrNotFound means another pod already consumed, or the row never existed.
    // Caller (ServeLoginPoll [B] / [A3]) decides per-state HTTP response.
    ConsumeLogin(ctx context.Context, loginID string) (LoginRecord, error)

    // -- sessions --

    // GetSession looks up by sha256_hex(plaintextSessionID) WHERE expires_at > now().
    // The store hashes internally; plaintext sid is never written to a SQL parameter.
    // Expired or missing → ErrNotFound.
    GetSession(ctx context.Context, plaintextSessionID string) (SessionRecord, error)

    // DeleteSession hashes the plaintext and DELETEs that row. Idempotent.
    DeleteSession(ctx context.Context, plaintextSessionID string) error

    // -- sweep --

    // SweepExpired DELETEs rows with expires_at < now() from both tables.
    // Safe to run concurrently across pods (each statement is atomic).
    // Returns per-table deletion counts and the first error encountered.
    SweepExpired(ctx context.Context) (loginsDeleted, sessionsDeleted int64, err error)
}
```

- [ ] **Step 2: `go build ./internal/commanderhub/authstore/...` succeeds (no consumers yet, just type-checks).**

- [ ] **Step 3: No tests yet — that's Task 4's RunConformanceTests.**

---

### Task 3: `authstore.NewInMemoryStore`

**Files:**
- Create: `internal/commanderhub/authstore/inmemory.go`

**Interfaces:**
- Exports: `NewInMemoryStore() Store`

- [ ] **Step 1: Implement `inmemory.go`.** Use one `sync.Mutex` guarding two maps (`logins map[string]*loginRow` + `sessions map[string]*sessionRow`). All methods acquire the mutex once at entry. The map values are private struct types mirroring DB columns.

Key design points:
- `hashSID(plaintext string) string` — `hex.EncodeToString(sha256.Sum256([]byte(plaintext))[:])`. Use this in `MarkLoginDone` / `GetSession` / `DeleteSession` so behavior matches postgres.
- `ReserveLogin`: lock → range sweep `expires_at < now` → `len(logins) >= 1024` returns `ErrCapped` → insert empty row.
- `FinalizeReservedLogin`: lock → lookup → if not in reserved state (DeviceCode!="" or terminal) return `ErrNotFound` → update.
- `MarkLoginDone`: lock → lookup → if not pending (terminal, expired, or reserved) return `ErrNotFound` → set hash + finalized_at + insert session row. **Both writes under one lock = atomic.**
- `MarkLoginFailed`: lock → lookup → similar guard → set failure + finalized_at.
- `ConsumeLogin`: lock → lookup → delete → return snapshot.
- `GetSession`: lock → hash → lookup → if expired return ErrNotFound + DELETE the expired row (cheap-and-clean) → return snapshot.
- `SweepExpired`: lock → range each map → count deletions → return.
- No goroutines.

- [ ] **Step 2: Quick smoke test in `inmemory_test.go`:**

```go
package authstore

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

func TestInMemoryStore_BasicSmoke(t *testing.T) {
    s := NewInMemoryStore()
    ctx := context.Background()
    now := time.Now()

    require.NoError(t, s.ReserveLogin(ctx, "lid1", now, 10*time.Minute))
    rec, err := s.GetLogin(ctx, "lid1")
    require.NoError(t, err)
    require.Equal(t, "", rec.DeviceCode)
    require.Equal(t, "lid1", rec.LoginID)
}
```

- [ ] **Step 3: `go test ./internal/commanderhub/authstore/...` — passes.**

The fuller behavioral coverage comes from Task 4's conformance suite.

---

### Task 4: `authstore.RunConformanceTests`

**Files:**
- Create: `internal/commanderhub/authstore/conformance_test.go`

**Interfaces:**
- Exports (within the `authstore_test` package — no, keep in `authstore` package so `RunConformanceTests` can be called from `inmemory_test.go` + `postgres_test.go`):
  - `func RunConformanceTests(t *testing.T, newStore func(t *testing.T) Store)`
  - Helper: `newStore(t)` returns a fresh Store; subtests rely on isolation. For postgres, the factory does `TRUNCATE commander_logins, commander_sessions` first.

- [ ] **Step 1: Write the conformance suite** as `t.Run`-style sub-tests covering every contract bullet from the spec § 10 list. Reproducing essentials:

```go
package authstore

import (
    "context"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

// RunConformanceTests drives all Store contract assertions. Both inmemoryStore
// and postgresStore must pass it.
func RunConformanceTests(t *testing.T, newStore func(t *testing.T) Store) {
    t.Run("ReserveLogin_basic", func(t *testing.T) {
        s := newStore(t)
        ctx := context.Background()
        require.NoError(t, s.ReserveLogin(ctx, "lid1", time.Now(), 10*time.Minute))
        rec, err := s.GetLogin(ctx, "lid1")
        require.NoError(t, err)
        require.Equal(t, "", rec.DeviceCode)
        require.WithinDuration(t, time.Now().Add(10*time.Minute), rec.ExpiresAt, 5*time.Second)
    })

    t.Run("ReserveLogin_capped_then_sweep_releases", func(t *testing.T) {
        s := newStore(t)
        ctx := context.Background()
        // Fill cap with TINY TTL so they all expire shortly.
        for i := 0; i < 1024; i++ {
            require.NoError(t, s.ReserveLogin(ctx, fmt.Sprintf("lid%d", i),
                time.Now(), 50*time.Millisecond))
        }
        // Confirm cap is hit.
        err := s.ReserveLogin(ctx, "overflow", time.Now(), 10*time.Minute)
        require.ErrorIs(t, err, ErrCapped)

        // Wait past expiry so ReserveLogin's internal sweep ("DELETE WHERE
        // expires_at < now()") will reclaim slots on the next call.
        time.Sleep(150 * time.Millisecond)

        // Now a fresh reserve must succeed via lazy sweep.
        require.NoError(t, s.ReserveLogin(ctx, "after-sweep", time.Now(), 10*time.Minute))
    })

    t.Run("FinalizeReservedLogin_OK_then_double_call_NotFound", func(t *testing.T) { /* ... */ })
    t.Run("FinalizeReservedLogin_intervalSeconds_below_5_is_clamped_by_store", func(t *testing.T) { /* see Task 6 design point */ })
    t.Run("DeleteLogin_idempotent", func(t *testing.T) { /* exists / missing both nil */ })
    t.Run("DeleteLogin_frees_cap_slot", func(t *testing.T) { /* fill, delete one, reserve OK */ })
    t.Run("GetLogin_missing_NotFound", func(t *testing.T) { /* */ })
    t.Run("SetPollThrottle_writes_both_fields", func(t *testing.T) {
        // ReserveLogin + FinalizeReservedLogin first; SetPollThrottle(60, future);
        // GetLogin verifies IntervalSeconds=60, NextPollAt≈future ±1s.
    })
    t.Run("SetPollThrottle_missing_lid_returns_nil", func(t *testing.T) { /* */ })
    t.Run("MarkLoginDone_terminal_existing_NotFound", func(t *testing.T) {
        // 1st MarkLoginDone wins; 2nd MarkLoginDone with different sid → ErrNotFound, session table unchanged
    })
    t.Run("MarkLoginDone_on_expired_login_NotFound_no_session_insert", func(t *testing.T) {
        // Reserve + FinalizeReservedLogin so the login is in 'pending' state
        // (NOT reserved — otherwise MarkLoginDone would return ErrNotFound
        // for the WRONG reason: device_code='' guard rather than expiry).
        // Then wait past expiry, then MarkLoginDone → ErrNotFound, sessions empty.
        ctx := context.Background()
        require.NoError(t, s.ReserveLogin(ctx, "expired-pending", time.Now(), 50*time.Millisecond))
        require.NoError(t, s.FinalizeReservedLogin(ctx, "expired-pending",
            "dc-x", time.Now().Add(5*time.Minute), 5))
        time.Sleep(150 * time.Millisecond)
        err := s.MarkLoginDone(ctx, "expired-pending", SessionRecord{
            PlaintextSessionID: "should-not-stick",
            Identity:           identity.Identity{UserID: "u", WorkspaceID: "w", Source: identity.SourceAgentserver},
            ExpiresAt:          time.Now().Add(12 * time.Hour),
        })
        require.ErrorIs(t, err, ErrNotFound, "expired login row must reject MarkLoginDone")
        _, err = s.GetSession(ctx, "should-not-stick")
        require.ErrorIs(t, err, ErrNotFound, "no session row may exist when MarkLoginDone fails")
    })
    t.Run("MarkLoginDone_on_failed_login_NotFound", func(t *testing.T) {
        // reserve+finalize+MarkLoginFailed → MarkLoginDone returns ErrNotFound
    })
    t.Run("MarkLoginDone_on_reserved_login_NotFound", func(t *testing.T) {
        // reserve (no finalize) → MarkLoginDone returns ErrNotFound (device_code = '' guards it)
    })
    t.Run("MarkLoginDone_strong_consistency_concurrent", func(t *testing.T) { /* see Step 2 */ })
    t.Run("MarkLoginFailed_OK_then_double_call_NotFound", func(t *testing.T) { /* */ })
    t.Run("MarkLoginFailed_with_invalid_Failure_value_rejected", func(t *testing.T) {
        // The Failure newtype can be constructed via `authstore.Failure("custom raw error")`.
        // Both stores must reject any value not in ValidFailure() and return ErrInvalidFailure
        // BEFORE writing anything. Row state stays as 'pending'.
        ctx := context.Background()
        require.NoError(t, s.ReserveLogin(ctx, "bad-fail", time.Now(), 10*time.Minute))
        require.NoError(t, s.FinalizeReservedLogin(ctx, "bad-fail",
            "dc-y", time.Now().Add(5*time.Minute), 5))
        err := s.MarkLoginFailed(ctx, "bad-fail", Failure("custom raw error not in enum"))
        require.ErrorIs(t, err, ErrInvalidFailure, "non-enum Failure must be rejected")
        rec, err := s.GetLogin(ctx, "bad-fail")
        require.NoError(t, err)
        require.Empty(t, string(rec.Failure), "row failure column must remain untouched")
        require.Equal(t, "", rec.SessionIDHash, "row session_id_hash must remain untouched")
    })
    t.Run("ConsumeLogin_reserved_pending_done_failed_all_consumable", func(t *testing.T) { /* 4 subcases */ })
    t.Run("ConsumeLogin_oneshot_concurrent", func(t *testing.T) { /* see Step 3 */ })
    t.Run("GetSession_hash_lookup_works", func(t *testing.T) {
        // Reserve+Finalize+MarkLoginDone with PlaintextSessionID="P"; GetSession("P") hits;
        // GetSession("Q") (different plaintext) misses.
    })
    t.Run("GetSession_expired_NotFound", func(t *testing.T) {
        // MarkLoginDone with ExpiresAt = now-1s → GetSession ErrNotFound.
    })
    t.Run("DeleteSession_then_GetSession_NotFound", func(t *testing.T) { /* */ })
    t.Run("DeleteSession_missing_idempotent_nil", func(t *testing.T) { /* */ })
    t.Run("SweepExpired_deletes_expired_only_correct_counts", func(t *testing.T) {
        // Seed: 3 expired logins + 2 fresh logins + 4 expired sessions + 1 fresh session
        // SweepExpired → (3, 4, nil); remaining rows verified by GetLogin/GetSession scans.
    })
    t.Run("SweepExpired_empty_tables_returns_zero", func(t *testing.T) { /* (0, 0, nil) */ })

    // Note: FailureEnumMatchesSchema drift guard lives in Task 5's migrate_test.go,
    // NOT inside this conformance suite — it depends on schema_postgres.sql which
    // doesn't exist until Task 5. See Task 5 Step 5.
}
```

- [ ] **Step 2: The concurrent MarkLoginDone test is critical** — write it carefully:

```go
    t.Run("MarkLoginDone_concurrent_strong_consistency", func(t *testing.T) {
        s := newStore(t)
        ctx := context.Background()
        lid := "concurrent-lid"
        require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
        require.NoError(t, s.FinalizeReservedLogin(ctx, lid, "dc-1",
            time.Now().Add(5*time.Minute), 5))

        const N = 20
        var wg sync.WaitGroup
        wg.Add(N)
        results := make([]error, N)
        sids := make([]string, N)
        start := make(chan struct{})
        for i := 0; i < N; i++ {
            sids[i] = fmt.Sprintf("plain-sid-%02d", i)
            go func(i int) {
                defer wg.Done()
                <-start
                results[i] = s.MarkLoginDone(ctx, lid, SessionRecord{
                    PlaintextSessionID: sids[i],
                    Identity:           identity.Identity{UserID: "u", WorkspaceID: "w", Source: identity.SourceAgentserver},
                    ExpiresAt:          time.Now().Add(12 * time.Hour),
                })
            }(i)
        }
        close(start)
        wg.Wait()

        wins := 0
        for _, r := range results {
            if r == nil {
                wins++
            } else {
                require.ErrorIs(t, r, ErrNotFound)
            }
        }
        require.Equal(t, 1, wins, "exactly one MarkLoginDone must succeed")

        // Exactly one session row exists. Try every sid; exactly one resolves.
        hits := 0
        for _, sid := range sids {
            _, err := s.GetSession(ctx, sid)
            if err == nil {
                hits++
            } else {
                require.ErrorIs(t, err, ErrNotFound)
            }
        }
        require.Equal(t, 1, hits, "no orphan sessions left in DB")
    })
```

- [ ] **Step 3: Concurrent ConsumeLogin one-shot:**

```go
    t.Run("ConsumeLogin_concurrent_oneshot", func(t *testing.T) {
        s := newStore(t)
        ctx := context.Background()
        lid := "oneshot-lid"
        require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
        require.NoError(t, s.FinalizeReservedLogin(ctx, lid, "dc-x",
            time.Now().Add(5*time.Minute), 5))

        const N = 10
        var wg sync.WaitGroup
        wg.Add(N)
        observed := make([]error, N)
        start := make(chan struct{})
        for i := 0; i < N; i++ {
            go func(i int) {
                defer wg.Done()
                <-start
                _, observed[i] = s.ConsumeLogin(ctx, lid)
            }(i)
        }
        close(start)
        wg.Wait()

        wins := 0
        for _, e := range observed {
            if e == nil {
                wins++
            } else {
                require.ErrorIs(t, e, ErrNotFound)
            }
        }
        require.Equal(t, 1, wins)
    })
```

- [ ] **Step 4: Wire to `inmemory_test.go`:**

```go
func TestInMemoryStore_Conformance(t *testing.T) {
    RunConformanceTests(t, func(t *testing.T) Store {
        return NewInMemoryStore()
    })
}
```

- [ ] **Step 5: Run `go test ./internal/commanderhub/authstore/...`. inmemory passes all subtests; Postgres absent for now.**

- [ ] **Step 6: If a subtest reveals an inmemory bug, fix `inmemory.go` and re-run. Don't write postgresStore until inmemory is conformance-green.**

---

### Task 5: `MigratePostgres` + `schema_postgres.sql`

**Files:**
- Create: `internal/commanderhub/authstore/schema_postgres.sql`
- Create: `internal/commanderhub/authstore/migrate.go`

**Interfaces:**
- Exports: `MigratePostgres(db *sql.DB) error`. Idempotent (`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`, etc.). Safe to call on every startup.

- [ ] **Step 1: Create `schema_postgres.sql` with the spec § 7 schema verbatim.** Specifically, copy ALL CHECK constraints:
  - `commander_logins_terminal_xor`
  - `commander_logins_finalized_iff_terminal`
  - `commander_logins_failure_len`
  - `commander_logins_failure_enum` (the 6-value IN list)
  - `commander_logins_login_id_nonempty`
  - `commander_logins_code_expires_iff_devcode`
  - `commander_logins_interval_positive`
  - `commander_sessions_user_id_nonempty`
  - `commander_sessions_workspace_id_nonempty`
  - `commander_sessions_source_nonempty`

**SANITY:** the `failure_enum` CHECK list MUST match `failure.go` constants 1:1. Add a sentinel comment:

```sql
-- WHEN ADDING TO failure.go's Failure const block, ALSO ADD HERE.
-- Mismatch = INSERT failure on legitimate enum values. Reverse mismatch =
-- store silently accepts a stale enum, defeating the security guard.
```

- [ ] **Step 2: Create `migrate.go`:**

```go
package authstore

import (
    "database/sql"
    _ "embed"
)

//go:embed schema_postgres.sql
var schemaPostgresSQL string

// MigratePostgres creates commander tables + constraints + indexes if missing.
// Idempotent — every observer-server startup re-runs it; helm migration-job
// also runs it via --migrate-only.
func MigratePostgres(db *sql.DB) error {
    _, err := db.Exec(schemaPostgresSQL)
    return err
}
```

- [ ] **Step 3: Sanity test (DSN-gated):**

```go
// internal/commanderhub/authstore/migrate_test.go
package authstore

import (
    "database/sql"
    "os"
    "testing"

    _ "github.com/jackc/pgx/v5/stdlib"
    "github.com/stretchr/testify/require"
)

func TestMigratePostgres_Idempotent(t *testing.T) {
    dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
    if dsn == "" {
        t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
    }
    db, err := sql.Open("pgx", dsn)
    require.NoError(t, err)
    defer db.Close()
    require.NoError(t, MigratePostgres(db))
    require.NoError(t, MigratePostgres(db)) // re-run, must not error
}
```

- [ ] **Step 4: With DSN set, run `go test ./internal/commanderhub/authstore/ -run TestMigratePostgres`. Passes.**

- [ ] **Step 5: Add `FailureEnumMatchesSchema` drift-guard test (no-DSN)** in `migrate_test.go` (or `failure_drift_test.go` if you prefer to keep migrate_test.go DSN-gated):

```go
package authstore

import (
    "regexp"
    "sort"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestFailureEnumMatchesSchema(t *testing.T) {
    // Extract the `failure IN ('a', 'b', ...)` list from schemaPostgresSQL
    // and assert it equals the Go enum allowlist.
    re := regexp.MustCompile(`(?s)commander_logins_failure_enum CHECK \(\s*failure IS NULL OR failure IN \(([^)]+)\)`)
    match := re.FindStringSubmatch(schemaPostgresSQL)
    require.NotNil(t, match, "schema_postgres.sql missing commander_logins_failure_enum CHECK")

    raw := match[1]
    var schemaList []string
    for _, lit := range strings.Split(raw, ",") {
        lit = strings.TrimSpace(lit)
        lit = strings.Trim(lit, "'")
        if lit != "" {
            schemaList = append(schemaList, lit)
        }
    }
    sort.Strings(schemaList)

    var goList []string
    for _, f := range allFailureValues {
        goList = append(goList, string(f))
    }
    sort.Strings(goList)

    require.Equal(t, goList, schemaList,
        "Go Failure enum and SQL CHECK list have drifted; sync both.")
}
```

Runnable without DSN since `schemaPostgresSQL` is the embedded string. **`allFailureValues` is defined in `failure_test.go` from Task 1; this test reuses it by being in the same package.**

---

### Task 6: `authstore.NewPostgresStore`

**Files:**
- Create: `internal/commanderhub/authstore/postgres.go`
- Create: `internal/commanderhub/authstore/postgres_test.go`
- Create: `internal/commanderhub/authstore/sql_dialect_test.go`

**Interfaces:**
- Exports: `NewPostgresStore(db *sql.DB) Store`.
- `const advisoryLockKeyCommanderLogins int64 = 8442987421341` (single source of truth; document namespace).

- [ ] **Step 1: Implement `postgres.go`.** All methods take `ctx`. SQL writes use `db.ExecContext` / `db.QueryRowContext`. Transactions for `MarkLoginDone` and `ReserveLogin`. Use `database/sql` parameterized `$N` placeholders exclusively.

Key SQL for the tricky methods (literal):

**ReserveLogin:**

```go
func (s *postgresStore) ReserveLogin(ctx context.Context, loginID string,
    now time.Time, ttl time.Duration) error {

    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback() // safe no-op after Commit

    if _, err := tx.ExecContext(ctx,
        `SELECT pg_advisory_xact_lock($1)`, advisoryLockKeyCommanderLogins); err != nil {
        return err
    }
    if _, err := tx.ExecContext(ctx,
        `DELETE FROM commander_logins WHERE expires_at < now()`); err != nil {
        return err
    }

    res, err := tx.ExecContext(ctx, `
        INSERT INTO commander_logins (login_id, expires_at)
        SELECT $1::text, $2::timestamptz
        WHERE (SELECT count(*) FROM commander_logins) < 1024
    `, loginID, now.Add(ttl))
    if err != nil {
        return err
    }
    n, err := res.RowsAffected()
    if err != nil {
        return err
    }
    if n == 0 {
        return ErrCapped
    }
    return tx.Commit()
}
```

**MarkLoginDone:**

```go
func (s *postgresStore) MarkLoginDone(ctx context.Context, loginID string,
    sess SessionRecord) error {

    hash := hashSID(sess.PlaintextSessionID)

    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback()

    res, err := tx.ExecContext(ctx, `
        UPDATE commander_logins
           SET session_id_hash = $1, finalized_at = now()
         WHERE login_id = $2
           AND session_id_hash IS NULL
           AND failure IS NULL
           AND device_code <> ''
           AND expires_at > now()
    `, hash, loginID)
    if err != nil {
        return err
    }
    n, err := res.RowsAffected()
    if err != nil {
        return err
    }
    if n == 0 {
        return ErrNotFound
    }

    if _, err := tx.ExecContext(ctx, `
        INSERT INTO commander_sessions (
            session_id_hash, user_id, workspace_id, role, source, expires_at
        ) VALUES ($1, $2, $3, $4, $5, $6)
    `, hash, sess.Identity.UserID, sess.Identity.WorkspaceID,
        sess.Identity.Role, string(sess.Identity.Source), sess.ExpiresAt); err != nil {
        return err
    }
    return tx.Commit()
}
```

**ConsumeLogin:**

```go
func (s *postgresStore) ConsumeLogin(ctx context.Context, loginID string) (LoginRecord, error) {
    row := s.db.QueryRowContext(ctx, `
        DELETE FROM commander_logins
              WHERE login_id = $1
        RETURNING login_id, device_code, code_expires_at, interval_seconds,
                  next_poll_at, expires_at, session_id_hash, failure
    `, loginID)
    return scanLoginRecord(row)
}

func scanLoginRecord(row interface{ Scan(...any) error }) (LoginRecord, error) {
    var rec LoginRecord
    var codeExpiresAt, nextPollAt, expiresAt sql.NullTime
    var sidHash, failure sql.NullString
    err := row.Scan(&rec.LoginID, &rec.DeviceCode, &codeExpiresAt, &rec.IntervalSeconds,
        &nextPollAt, &expiresAt, &sidHash, &failure)
    if err == sql.ErrNoRows {
        return LoginRecord{}, ErrNotFound
    }
    if err != nil {
        return LoginRecord{}, err
    }
    if codeExpiresAt.Valid {
        rec.CodeExpiresAt = codeExpiresAt.Time
    }
    if nextPollAt.Valid {
        rec.NextPollAt = nextPollAt.Time
    }
    if expiresAt.Valid {
        rec.ExpiresAt = expiresAt.Time
    }
    if sidHash.Valid {
        rec.SessionIDHash = sidHash.String
    }
    if failure.Valid {
        rec.Failure = Failure(failure.String)
    }
    return rec, nil
}
```

Implement remaining methods in the same style. `MarkLoginFailed` uses a single UPDATE+WHERE; `SetPollThrottle` single UPDATE; `GetSession` single SELECT WHERE session_id_hash + expires_at; `DeleteSession`/`DeleteLogin` single DELETE; `SweepExpired` two DELETEs (one tx OK or two separate calls — separate is simpler and equally safe).

**Design point — clamp `intervalSeconds` to >= 5** in `FinalizeReservedLogin` and `SetPollThrottle` BEFORE writing. Reason: agentserver upstream can return `Interval=0` (Codex Stage 2 design concern), which would violate `commander_logins_interval_positive` CHECK and fail the whole login. Helper:

```go
func clampIntervalSeconds(n int) int {
    if n < 5 {
        return 5
    }
    return n
}
```

Both store implementations apply this clamp; conformance test "FinalizeReservedLogin_intervalSeconds_below_5_is_clamped_by_store" verifies behavior is consistent.

**Failure value validation** — both store implementations check `if !ValidFailure(f) { return ErrInvalidFailure }` as the very first statement in `MarkLoginFailed`. `ValidFailure` and `ErrInvalidFailure` are defined in Task 1's `failure.go` (moved earlier per Codex Stage 2 R2 ordering blocker). Postgres DB CHECK `commander_logins_failure_enum` is the second line of defense.

`hashSID`:

```go
func hashSID(plaintext string) string {
    sum := sha256.Sum256([]byte(plaintext))
    return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 2: Wire conformance suite in `postgres_test.go`:**

```go
package authstore

import (
    "database/sql"
    "os"
    "testing"

    _ "github.com/jackc/pgx/v5/stdlib"
    "github.com/stretchr/testify/require"
)

func TestPostgresStore_Conformance(t *testing.T) {
    dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
    if dsn == "" {
        t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
    }
    db, err := sql.Open("pgx", dsn)
    require.NoError(t, err)
    require.NoError(t, MigratePostgres(db))

    RunConformanceTests(t, func(t *testing.T) Store {
        _, err := db.ExecContext(context.Background(),
            `TRUNCATE commander_logins, commander_sessions`)
        require.NoError(t, err)
        return NewPostgresStore(db)
    })
}
```

- [ ] **Step 3: Write `sql_dialect_test.go`** modeled after `internal/userspace/store_postgres_test.go:21` — a recording `driver.Driver` capturing **both** the query string AND the named/positional args of every `ExecContext`/`QueryContext`/`QueryRowContext`:
  - All `?` placeholders absent (must use `$1`)
  - No `INSERT OR REPLACE` / `AUTOINCREMENT` / `PRAGMA`
  - No SQL line contains `fmt.Sprintf`-style `%s` after parameter substitution (best-effort regex against captured queries)
  - **Plaintext-sid leak check**: drive each store method that takes a plaintext sid (`MarkLoginDone`, `GetSession`, `DeleteSession`) with a known sentinel `plaintext := "DIALECT_TEST_PLAINTEXT_SID"` and assert that NEITHER the captured queries NOR any captured arg contains this literal — only its sha256_hex hash is permitted to appear. This is the structural test that plaintext never reaches SQL parameters.
  - No DSN required

Use the existing `recordingSQLDB` helper pattern from userspace; if it's not exported, copy-adapt into a small in-test recorder. Reference: `git grep -n 'newRecordingSQLDB' internal/userspace/store_postgres_test.go`.

- [ ] **Step 4: With DSN set, run `go test ./internal/commanderhub/authstore/...`. All conformance + migrate + dialect passes.**

- [ ] **Step 5: Without DSN, run `go test ./internal/commanderhub/authstore/...`. Migrate/postgres conformance skip; inmemory conformance + failure + dialect all pass.**

---

### Task 7: `deviceFlow.PollOnce` + sentinel errors

**Files:**
- Modify: `internal/commanderhub/auth.go` (specifically the `deviceFlow` interface and `agentsdkDeviceFlow` impl)

**Interfaces:**
- Replace the `deviceFlow.PollToken` death-loop with `PollOnce`. New signature:

```go
type deviceFlow interface {
    RequestCode(ctx context.Context) (DeviceCode, error)
    PollOnce(ctx context.Context, code DeviceCode) (tok loginToken,
        tokenReady, retryable, slowDown bool, err error)
}
```

- `agentsdkDeviceFlow.PollOnce` body lifted from today's `PollToken` loop body:
  - `200 OK` → unmarshal → `tokenReady=true`
  - `authorization_pending` → `retryable=true, slowDown=false, err=nil`
  - `slow_down` → `retryable=true, slowDown=true, err=nil`
  - `access_denied` → `err = ErrAuthorizationDenied`
  - `expired_token` → `err = ErrAuthorizationExpired`
  - HTTP network error (dial/timeout/EOF) → `retryable=true, err=nil` (let Authenticator retry on the next /poll tick)
  - HTTP 5xx response (502/503/504/...) → `retryable=true, err=nil`. **Do NOT** include the response body in any return path. Per spec § 11 + Codex Stage 2 security note, 502 bodies may contain token-shaped content — silently retry.
  - HTTP 4xx response other than the listed OAuth errors → terminal: `retryable=false`, return `errors.New("device flow: unknown 4xx")` (no body interpolation; `SanitizeFailure` maps to `FailureDeviceFlow`).
  - JSON parse failure (200 body) → terminal: `retryable=false`, return `errors.New("device flow: bad token response")` (no body interpolation).

- [ ] **Step 1: Update interface + impl, delete old `PollToken` death loop.**

- [ ] **Step 2: Smoke test in `auth_test.go`** (or new `device_flow_test.go`):

```go
func TestAgentsdkDeviceFlow_PollOnce_StateMapping(t *testing.T) {
    // Stand up httptest.Server emulating /api/oauth2/token responses for each case.
    // Verify: tokenReady, retryable, slowDown, err mapping.
    // Required cases: 200 ok, authorization_pending, slow_down, access_denied,
    //   expired_token, 500 internal, network refused (server closed).
}
```

- [ ] **Step 3: DO NOT run `go test ./internal/commanderhub/...` yet.** Authenticator (Task 8) still calls the removed `PollToken` — compilation will fail. Tasks 7 + 8 share a green checkpoint at the END of Task 8. Move directly to Task 8.

---

### Task 8: rewrite `Authenticator` to hold `Store` + new state machine + `writeCtx` helper

**Files:**
- Modify: `internal/commanderhub/auth.go`
- Modify: `internal/commanderhub/auth_test.go`

**Interfaces:**
- `NewAuthenticator(resolver identity.Resolver, agentserverURL string, store authstore.Store) *Authenticator`
- `Authenticator.CommanderIdentity(r *http.Request) (identity.Identity, bool)` — semantics from spec § 11: cookie hits store.GetSession; store non-NotFound error → return false (no Bearer fallback); ErrNotFound → fall through to Bearer.
- `Authenticator.ServeLogin / ServeLoginPoll / ServeLogout` — state machines from spec § 6.
- `Authenticator.writeCtx(ctx context.Context) (context.Context, context.CancelFunc)` — `WithTimeout(WithoutCancel(ctx), storeWriteTimeout)`. `storeWriteTimeout = 5*time.Second` const.
- `Authenticator.runSweep(interval time.Duration)` — sweep ticker (called from `MountAll`).

- [ ] **Step 1: Strip `loginMu`, `logins`, `sessMu`, `sessions`, `pollLogin`. Add `store authstore.Store`. Remove `putSession` (tests no longer need it — replaced by direct `store.MarkLoginDone` calls in fixtures).**

- [ ] **Step 2: Implement `writeCtx`:**

```go
const storeWriteTimeout = 5 * time.Second

func (a *Authenticator) writeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
    return context.WithTimeout(context.WithoutCancel(ctx), storeWriteTimeout)
}
```

- [ ] **Step 3: Rewrite `ServeLogin` per spec § 6.** Steps:
  1. `lid := randomID()`
  2. `if err := a.store.ReserveLogin(r.Context(), lid, time.Now(), loginTTL); err != nil { ... }` (translate ErrCapped → 429, other → 502)
  3. `dc, err := a.flow.RequestCode(r.Context())` — if err, do **fresh** cleanup ctx: `cleanCtx, cancel := a.writeCtx(context.Background()); defer cancel(); a.store.DeleteLogin(cleanCtx, lid)`. Return 502. **Always derive cleanup from `context.Background()`, NOT from a possibly-already-failed/timed-out `r.Context()`** (Codex Stage 2 design concern: the same ctx whose timeout caused the failure would be useless for cleanup).
  4. `if err := r.Context().Err(); err != nil` — same fresh cleanup; bail (response is already moot, just call `return` without writing).
  5. `writeCtx, cancel := a.writeCtx(r.Context()); defer cancel()`
     `if err := a.store.FinalizeReservedLogin(writeCtx, lid, dc.Code, time.Now().Add(dc.ExpiresIn), clampIntervalSeconds(int(dc.Interval/time.Second))); err != nil {`
     `    cleanCtx, c2 := a.writeCtx(context.Background()); a.store.DeleteLogin(cleanCtx, lid); c2();`
     `    if err == authstore.ErrNotFound { return 502 "login expired during init" } else { return 502 "store unavailable" }`
     `}`
  6. `writeJSON(w, map[string]any{"verification_uri_complete": dc.VerificationURIComplete, "login_id": lid, "expires_in": int(dc.ExpiresIn/time.Second)})`

  Note: spec § 5 nit said §6 pseudo-code shows raw `WithoutCancel` not the helper. Use the helper consistently in code.

  Helper rule: `a.writeCtx(parent)` should accept either `r.Context()` (when we want to keep the request's deadlines/values where applicable) or `context.Background()` (for cleanup that must outlive any request failure):

  ```go
  func (a *Authenticator) writeCtx(parent context.Context) (context.Context, context.CancelFunc) {
      // WithoutCancel preserves values from `parent` (trace IDs, etc.) while
      // dropping its cancel/deadline. The 5s timeout is fresh.
      return context.WithTimeout(context.WithoutCancel(parent), storeWriteTimeout)
  }
  ```

  This way `a.writeCtx(context.Background())` is still a fine call — WithoutCancel of Background returns Background, then WithTimeout adds the 5s budget.

- [ ] **Step 4: Rewrite `ServeLoginPoll` per spec § 6 state machine.** Implement [A1]–[A4], [B], [C-throttle], [C1]–[C3] exactly. All writes wrapped with `writeCtx`.

  Key: in [C1] success, do `MarkLoginDone` (with writeCtx), then immediately `http.SetCookie(...)` + `writeJSON(... status: ok ...)`. No sidByLoginID map. No deferred consume.

  Spec § 5 nit: §3 decision table previously said `[B] done` returns 404. State machine says 401 "authorization expired". Plan implements 401 (state-machine wins). When committing, ensure the spec is also corrected for the residual nit (Task 13 wraps it up).

- [ ] **Step 5: Rewrite `ServeLogout`:** `cookie → DeleteSession(writeCtx(r.Context()), sid)`; clear cookie; ok.

- [ ] **Step 6: Rewrite `CommanderIdentity` per spec § 11:**

```go
func (a *Authenticator) CommanderIdentity(r *http.Request) (identity.Identity, bool) {
    if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
        sess, err := a.store.GetSession(r.Context(), c.Value)
        switch {
        case err == nil:
            return sess.Identity, true
        case errors.Is(err, authstore.ErrNotFound):
            // fall through to Bearer fallback below
        default:
            // store unhealthy → fail closed; do NOT widen attack surface via Bearer
            log.Printf("commanderhub: GetSession error: %v", err)
            return identity.Identity{}, false
        }
    }
    if tok, ok := bearerToken(r.Header.Get("Authorization")); ok {
        ident, err := a.resolver.Resolve(r.Context(), tok)
        if err == nil {
            return ident, true
        }
    }
    return identity.Identity{}, false
}
```

- [ ] **Step 7: Implement `runSweep` exactly as spec § 8.**

- [ ] **Step 8: Rewrite `auth_test.go`:**
  - New `fakeFlow` struct implementing `PollOnce` (records calls; returns programmable triples)
  - Adapt `newAuthenticatorWithFlow(resolver, flow)` → take a `store authstore.Store` parameter. Default in tests: `authstore.NewInMemoryStore()`.
  - Add tests for the cases enumerated in spec § 10 ("Authenticator 层") and "CommanderIdentity 故障语义测试". Each state machine branch gets one positive + one negative case.
  - Cookie attribute test: assert `HttpOnly`, `SameSite=Lax`, `Secure` (with X-Forwarded-Proto=https), MaxAge correct.
  - **WithoutCancel test (refined per Codex Stage 2)**:
    - **Positive case**: `fakeFlow.PollOnce` returns `tokenReady=true` after a small (deterministic, not timing-dependent) signal, but the test cancels `r.Context()` AFTER PollOnce returns and BEFORE MarkLoginDone is observed by the store. Use an `instrumentedStore` wrapping `inmemoryStore` that signals on MarkLoginDone entry. Assert: (a) MarkLoginDone called, (b) Session row exists when probed via `GetSession`. (i.e. WithoutCancel actually causes the write to land.)
    - **Negative case**: cancel BEFORE `PollOnce` returns. Assert no MarkLoginDone is called and the login row is still in `pending` state. (i.e. cancellation during PollOnce is honored cleanly.)
  - **PollOnce 502 token-leak test** (deviceFlow test, augments Task 7): httptest server returns `502` with body `{"raw_token":"super-secret","access_token":"Bearer abc"}`. Assert: `retryable=true`, `err=nil`, and any subsequent SanitizeFailure-of-err call returns FailureDeviceFlow (not body-derived). Pair with a direct grep of the captured PollOnce return values: `string(captured)` must not contain `super-secret` or `Bearer abc`.

- [ ] **Step 9: Run `go test ./internal/commanderhub/...` — passes.**

---

### Task 9: `wiring.go` — `MountAll` signature + sweep

**Files:**
- Modify: `internal/commanderhub/wiring.go`
- Modify: `internal/commanderhub/wiring_test.go`

**Interfaces:**
- `func MountAll(mux *http.ServeMux, resolver identity.Resolver, agentserverURL string, store authstore.Store)`

- [ ] **Step 1: Update `MountAll`:**

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

- [ ] **Step 2: Update `wiring_test.go`** to pass a store argument:

```go
MountAll(mux, resolver, "https://agent.example/", authstore.NewInMemoryStore())
```

- [ ] **Step 3: Run `go test ./internal/commanderhub/...` — passes.**

---

### Task 10: `observerweb/server.go` — `Options.AuthStore` + panic guard

**Files:**
- Modify: `internal/observerweb/server.go`
- Modify: `internal/observerweb/server_test.go` (if any tests instantiate `NewWithResolverOptions` with `AgentserverURL`)

**Interfaces:**
- Adds `Options.AuthStore authstore.Store`.

- [ ] **Step 1: Update `Options`:**

```go
type Options struct {
    // ... existing ...
    AgentserverURL string
    AuthStore      authstore.Store
}
```

- [ ] **Step 2: Update `NewWithResolverOptions`:**

```go
if opts.AgentserverURL != "" {
    if opts.AuthStore == nil {
        panic("observerweb: AuthStore is required when AgentserverURL is set (see commanderhub/authstore)")
    }
    commanderhub.MountAll(mux, resolver, opts.AgentserverURL, opts.AuthStore)
}
```

- [ ] **Step 3: Verify no current call site passes `AgentserverURL != ""` without `AuthStore`.** Grep:

```
git grep -n 'AgentserverURL' internal/ cmd/
```

- [ ] **Step 4: Add a small targeted test** (`server_authstore_test.go`):

```go
func TestNewWithResolverOptions_PanicsWithoutAuthStore(t *testing.T) {
    defer func() {
        if r := recover(); r == nil {
            t.Fatal("expected panic")
        }
    }()
    s := testStore(t)
    _ = NewWithResolverOptions(s, nil, static.New(s), Options{
        AgentserverURL: "https://agent.example/",
        // AuthStore intentionally absent
    })
}
```

- [ ] **Step 5: Run `go test ./internal/observerweb/...` — passes.**

---

### Task 11: `cmd/observer-server/main.go` — construct store + migrate

**Files:**
- Modify: `cmd/observer-server/main.go`
- Modify: `cmd/observer-server/main_test.go` (fix any tests broken by the new wiring)

**Interfaces:** wiring only.

- [ ] **Step 1: Add store construction block before `observerweb.NewWithResolverOptions`:**

```go
var authStore authstore.Store
switch cfg.Store.Driver {
case "postgres":
    if err := authstore.MigratePostgres(st.DB()); err != nil {
        log.Fatalf("commanderhub authstore migrate: %v", err)
    }
    authStore = authstore.NewPostgresStore(st.DB())
case "sqlite", "":
    log.Printf("commanderhub: using in-memory store (driver=%q is single-pod only)", cfg.Store.Driver)
    authStore = authstore.NewInMemoryStore()
default:
    log.Fatalf("commanderhub: unsupported store.driver %q", cfg.Store.Driver)
}

opts := observerWebOptions(cfg, objects)
opts.AuthStore = authStore
```

- [ ] **Step 2: Update `runMigrationsOnly`:**

```go
if cfg.Store.Driver == "postgres" {
    if err := authstore.MigratePostgres(st.DB()); err != nil {
        return fmt.Errorf("commanderhub authstore migrate: %w", err)
    }
}
```

(Place after the userspace migrate call.)

- [ ] **Step 3: Verify `main_test.go` still compiles + passes.** If a test wires `observerweb.NewWithResolverOptions` with `AgentserverURL` set, pass `Options.AuthStore = authstore.NewInMemoryStore()`.

- [ ] **Step 4: Run `go test ./cmd/observer-server/...` — passes.**

- [ ] **Step 5: Run `go build ./...` — entire repo builds.**

---

### Task 12: Cross-pod integration test

**Files:**
- Create: `internal/commanderhub/integration_test.go`

**Interfaces:** test-only.

- [ ] **Step 1: Build the harness:** opens a single `*sql.DB` from `OBSERVER_POSTGRES_TEST_DSN`, runs `MigratePostgres`, TRUNCATEs the two tables, constructs two independent `Authenticator` instances ("pod A" / "pod B") sharing the DB. Each pod gets its own `http.ServeMux` via `Mount` (no MountAll — we want fine control without ticker).

- [ ] **Step 2: Implement the 6 subcases enumerated in spec § 10.** Use `httptest.NewServer(pod.mux).URL` for each pod. fake `deviceFlow` shared between pods (records counts). Carry the cookie between pod requests manually (httptest doesn't follow cookies by default).

  Specifically for **subcase 5** (lost-response simulation, per Codex Stage 2): after pod A completes [C1] and returns 200, the test code **must extract the cookie from the response, then explicitly discard it** before sending the next `GET /poll?id=lid` to pod B. The next request goes out WITHOUT a `Cookie:` header. This models the user's browser never having received the Set-Cookie response. Assert: pod B's response is 401 `"authorization expired"` and the user can POST a fresh /login to retry.

- [ ] **Step 3: Subcase 6 (cap stress)** spawns 1100 goroutines doing `POST /login` against pod A's URL, asserts exactly 1024 succeed with 200, the rest 429, and the fake `RequestCode` was called exactly 1024 times.

- [ ] **Step 4: With DSN set, `go test ./internal/commanderhub/ -run TestCrossPod` passes.**

- [ ] **Step 5: Without DSN, all six subcases skip — `go test ./internal/commanderhub/` is green.**

---

### Task 13: End-to-end verification + spec/plan polish

- [ ] **Step 1: Fix Stage 1 R4 residual nits in the spec:**
  - §6 pseudo-code: replace `context.WithoutCancel(ctx)` with `a.writeCtx(ctx)` helper invocations.
  - §3 decision table `[B] done` → 401 (match the state machine).
  - §6 `PollOnce` comment about sanitization — clarify that PollOnce returns sentinel errors and Authenticator calls `SanitizeFailure` at the boundary.

- [ ] **Step 2: Run `go vet ./...` — clean.**

- [ ] **Step 3: Run `go test ./...` — full repo green. If `OBSERVER_POSTGRES_TEST_DSN` is set, postgres + cross-pod integration runs too.**

- [ ] **Step 4: `git log --oneline` should show one commit per task minimum. Squash optional later.**

- [ ] **Step 5: Manual e2e (deferred to Stage 3 wrap-up, but plan it here):**
  - Build observer-server binary
  - Stand up two instances locally pointing at a shared local Postgres (compose-test or ad-hoc)
  - Curl `POST /api/commander/login` against instance A, then `GET /poll?id=X` against instance B (round-robin via a tiny `nc` shim or just alternating `curl --resolve`)
  - Expected: cookie issued by either pod is usable on either pod; logout on one invalidates everywhere

- [ ] **Step 6: Trigger Stage 3 final codex review (handled by orchestrator at /tmp/codex-review/stage3-prompt.md).**

---

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| pg_advisory_xact_lock collides with future commander tables using the same key | Low | Medium | Const namespaced in `postgres.go`; document key registry comment |
| Postgres pool exhaustion under sweep-storm | Low | High | 30s timeout per sweep call + bounded ticker (1h) |
| `MarkLoginDone` ROLLBACK leaks an advisory lock | Zero | n/a | `pg_advisory_xact_lock` released at tx end regardless of commit/rollback |
| `failure.go` enum and DB CHECK drift | Medium (forgettable) | High (writes fail in prod) | Task 5 sentinel comment + Task 13 grep for both files; CI lint follow-up |
| Inmemory store's "MarkLoginDone first-writer-wins" semantically differs from postgres | High without test | Medium | Conformance Task 4 step 2 concrete test (N=20 goroutines, expect exactly 1 win) |
| `WithoutCancel` accidentally not paired with `WithTimeout` | Medium | High (goroutine leak) | `writeCtx` helper centralizes; code review checks for `WithoutCancel` not wrapped |
| Dev `sqlite` users surprised by inmemory mode | Low | Low | Startup log line + spec § 9 documentation |

## Self-Review

After completing all tasks:

- [ ] All new SQL is parameterized (`$1`)
- [ ] All `WithoutCancel` wraps have `WithTimeout`
- [ ] No plaintext sid in any SQL parameter except the `hashSID` callsites (sql_dialect_test asserts)
- [ ] `failure.go` enum constants match `commander_logins_failure_enum` CHECK list exactly (`FailureEnumMatchesSchema` test asserts)
- [ ] `MarkLoginFailed` rejects values outside the enum (`ErrInvalidFailure`); both stores
- [ ] `FinalizeReservedLogin` and `SetPollThrottle` clamp `intervalSeconds >= 5`
- [ ] `FailureStoreUnavailable` either used somewhere or removed from `failure.go` (Codex Stage 2 nit; if no caller, delete the constant)
- [ ] All post-`ReserveLogin` cleanup paths use `a.writeCtx(context.Background())`, never the request ctx
- [ ] No `pollLogin` goroutine remains
- [ ] No `Authenticator.{logins,sessions,loginMu,sessMu}` fields remain
- [ ] `MountAll` callers all pass an `authstore.Store`
- [ ] `observerweb.Options.AuthStore` panic-guards production accidental misconfiguration
- [ ] Conformance suite covers concurrent MarkLoginDone (exactly-1-winner) and concurrent ConsumeLogin (exactly-1-observer)
- [ ] Cross-pod integration test exercises subcases 1–6 from spec § 10
- [ ] `go vet ./...` clean
- [ ] `go test ./...` green; with DSN set, postgres + cross-pod also green
