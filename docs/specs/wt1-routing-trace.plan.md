# WT-1-routing-trace — Plan

> Drives [wt1-routing-trace.spec.md](wt1-routing-trace.spec.md). Pure TDD;
> every test maps to a spec section or a Security mitigation (a–f).

## Global Constraints (copied verbatim from spec)

- File domain: `multi-agent/internal/dispatch/` and
  `multi-agent/internal/observerstore/` ONLY. **Do not** touch `cmd/*`,
  `internal/executor/*`, `internal/contract`, or any other package.
- `Dispatcher.Run`'s signature is `(executor.Result, error)` — unchanged.
- `contract.DecodeEnvelope`'s signature is unchanged.
- Go version: 1.26.x (module declares `go 1.26.2`).
- **All `go test` / `go vet` / `gofmt` commands shown below assume cwd =
  `multi-agent/.worktrees/p1-routing-trace/multi-agent/`** (the Go module
  root). From the worktree root, run `cd multi-agent` first.
- Test command: `go test ./internal/dispatch/... ./internal/observerstore/...
  -count=1 -shuffle=on -race`
- Lint: `go vet ./...` && `gofmt -l internal/dispatch internal/observerstore`
- Each `git commit` ends with `Co-Authored-By: Claude Opus 4.8 (1M context)
  <noreply@anthropic.com>`. **Do not** push.

---

## File Map

| Path | Action | Responsibility |
|---|---|---|
| `multi-agent/internal/dispatch/route_decision.go` | **CREATE** | `RouteDecision`/`Candidate`/`ReasonCode` types, `NewDecision`, `FinalizeAndEmit`, `Writer`/`SetWriter`/`IsNoopWriter`/`currentWriter`, `peekConversationID`, `SanitizeReasonText`, `deriveID`, `WrapRouteWriter` adapter, `decisionNonce` atomic counter, `route_reason_redacted_total` expvar. |
| `multi-agent/internal/dispatch/route_decision_test.go` | **CREATE** | All type-level + sanitize + nonce + writer-wiring + forgery tests. |
| `multi-agent/internal/dispatch/dispatch.go` | **MODIFY** | Insert `peekConversationID` + `NewDecision` + `defer FinalizeAndEmit` as first 4 lines of `Run`. Populate `SelectedAgentID`/`SelectedNone`/`ReasonCode`/`ReasonText`/`Candidates` on each branch (success, no-executor, exec error, duplicate). |
| `multi-agent/internal/dispatch/dispatch_test.go` | **MODIFY** | Append integration tests covering the dispatch hook end-to-end (the 7 tests #1, #2, #7, #14, #16, #23, #24). |
| `multi-agent/internal/observerstore/schema.sql` | **MODIFY (APPEND)** | Append `route_reasons` table DDL + index, **at the end** of the file. |
| `multi-agent/internal/observerstore/route_reasons_writer.go` | **CREATE** | `RouteReasonRow`, `RouteCandidate`, `RouteWriter` interface, `routeReasonsWriter` struct, `NewRouteWriter`. |
| `multi-agent/internal/observerstore/route_reasons_writer_test.go` | **CREATE** | SQL injection guard, round-trip, ON CONFLICT DO NOTHING. |

## Test Matrix (each row links a test to spec / security item)

| # | Test | Verifies | Spec / Security |
|---|---|---|---|
| 1 | `TestDispatch_TwoCandidates_TraceWhyChosen` | 2 candidate executors; winner has `Reason=capability_match`, loser has `Reason=no_capability_match`. | §7 acceptance |
| 2 | `TestDispatch_NoCandidates_TraceFailure` | 0 executors registered; `SelectedAgentID=""`, `SelectedNone=true`, `ReasonCode=no_capability_match`. | §3.1 / §3.2 |
| 3 | `TestPersistedRow_ReasonText_Redacted` | `ReasonText="my key is sk-abc123abc123"` → persisted `"my key is [REDACTED]"`; expvar `route_reason_redacted_total` incremented. End-to-end through the real SQL writer. | Security (a) |
| 4 | `TestReasonText_LongerThan256_Truncated` | 1 KB ReasonText → truncated to 256 runes + `...[truncated]`. | Security (a) |
| 5 | `TestCandidatesJSON_NoCapabilitySnapshot` | Decoded `candidates_json` has exactly `{agent_id, score, reason}` fields, never `capability_snapshot`. | Security (b) |
| 6 | `TestTimestamp_FromMonotonic_NoExternalInject` | (a) `NewDecision` rejects any external `time.Time` (compile-test via `nm -P` not feasible — instead assert the constructor signature has no `time.Time` param via reflect). (b) Two `NewDecision` calls in a tight loop yield strictly monotonic `seedStarted` (Go `time.Now` guarantees monotonic-clock). | Security (c) |
| 7 | `TestWriter_FailLogged_DispatchContinues` | Writer returns `errors.New("boom")` → `Run` returns normal result; capture `log` output and assert it contains `[route-trace] write failed:` and `conv=<id>` and `decision=<id>`. | Security (d) |
| 8 | `TestWriter_Parameterized_SQLInjection` | `conv_id = "x'); DROP TABLE route_reasons;--"` writes through the real SQL writer; `route_reasons` table still exists, row count == 1, stored `conversation_id` exact-matches the malicious string. | Security (e) |
| 9 | `TestDecisionID_DerivedNotProvided` | `NewDecision` has no `DecisionID` parameter (reflect); two `NewDecision` calls with the same conversation_id yield distinct `DecisionID`s (nonce diff). | Security (f) |
| 10 | `TestForgery_ConversationID_OverwrittenOnFinalize` | Mutate exported `ConversationID` between `NewDecision` and `FinalizeAndEmit`; capture writer sees the seed value, not the mutation. | Security (c)+(f) |
| 11 | `TestForgery_DecisionStartedAt_OverwrittenOnFinalize` | Mutate exported `DecisionStartedAt`; same expectation. | Security (c) |
| 12 | `TestForgery_DecisionID_OverwrittenOnFinalize` | Mutate exported `DecisionID`; same expectation. | Security (f) |
| 13 | `TestDecisionID_UniquePerCall` | 10 000 `NewDecision` calls with same conversationID → 10 000 distinct `DecisionID`s. | Security (f) |
| 14 | `TestDispatch_FinalizeAndEmit_DeferCoversEarlyReturns` | For each of: malformed envelope, no executor, executor error, duplicate-running sentinel — assert the capture writer received exactly one row. | §3.2 |
| 15 | `TestPeekConversationID` | (a) present envelope → returns the value; (b) absent → ""; (c) malformed → ""; (d) escaped quotes inside JSON → still returns correct value. | §3.2 |
| 16 | `TestDispatch_ConversationIDFallback_UsesTaskID` | Absent envelope → persisted `conversation_id == t.ID`. | §3.2 |
| 17 | `TestSetWriter_AtomicValueNoPanic` | `SetWriter(noop)` then `SetWriter(real)` then `SetWriter(noop)` — never panics. | §2.2 |
| 18 | `TestIsNoopWriter` | True before `SetWriter`; false after `SetWriter(real)`; true after `SetWriter(nil)`. | §6 (d) |
| 19 | `TestRouteReasonsWriter_RoundTrip` | Write 1 row via real `*sql.DB` (in-memory SQLite), read back via `SELECT *`, assert every column matches. | §5 |
| 20 | `TestRouteReasonsWriter_OnConflictDoNothing` | Write the same `RouteReasonRow` twice; second call returns no error, table has 1 row. | §5 |
| 21 | `TestWrapRouteWriter_PassesAllFields` | Adapter converts `RouteDecision` → `RouteReasonRow`; capture every field round-trips, including `SelectedNone → "<none>"`. | §5.3 |
| 22 | `TestPersistedRow_SelectedNoneSerialization` | `SelectedNone=true` persists `selected_agent_id="<none>"`; `SelectedNone=false, SelectedAgentID=""` persists empty string. | §3.1 |
| 23 | `TestDispatch_TimestampDetached_PreservesTraceOnCtxCancel` | Pass a `ctx` that is already cancelled; assert the writer still received the row (FinalizeAndEmit uses `context.WithTimeout(context.Background(),2s)`). | §3.2 |
| 24 | `TestDispatch_Run_SignatureUnchanged` | `reflect.TypeOf((*Dispatcher).Run).String()` matches the recorded baseline `func(*Dispatcher, context.Context, executor.Task) (executor.Result, error)`. | §3.3 |
| 25 | `TestDispatch_FallbackExecutor_SelectedAsCapabilityMatch` | When `d.routes[""]` is the only entry and the task carries an unknown skill, the trace records `SelectedAgentID=""`, `SelectedNone=false`, `ReasonCode=capability_match`. Verifies §3.1's "fallback selected vs no-match" disambiguation. | §3.1 |

## Test → File Placement (authoritative)

| File | Tests written |
|---|---|
| `internal/observerstore/route_reasons_writer_test.go` (Task 1) | #8, #19, #20 |
| `internal/dispatch/route_decision_test.go` (Task 2) | #3, #4, #5, #6, #9, #10, #11, #12, #13, #15, #17, #18, #21, #22 |
| `internal/dispatch/dispatch_test.go` (Task 3) | #1, #2, #7, #14, #16, #23, #24, #25 |

Tests #3, #5, #22 are end-to-end through the real `*sql.DB` and use the
`openSQLiteForDispatchTest` helper defined in `route_decision_test.go`.
They live in Task 2's file but exercise the full
`NewDecision → FinalizeAndEmit → WrapRouteWriter → observerstore.NewRouteWriter`
path, so they require Task 1 to be merged first (the DDL must exist).
**Authoring order:** Task 1 → Task 2 (which depends on Task 1's writer +
DDL) → Task 3. Within Task 2, tests that only need the dispatch package
are written BEFORE the end-to-end ones, and within each red-green cycle
the tests run via the appropriate `-run` regex shown in each step.

## Tasks

### Task 1: writer types + DDL + writer test (pure observerstore)

**Files:**
- Create: `multi-agent/internal/observerstore/route_reasons_writer.go`
- Create: `multi-agent/internal/observerstore/route_reasons_writer_test.go`
- Modify: `multi-agent/internal/observerstore/schema.sql` — append at the very end.

**Interfaces:**
- Produces: `observerstore.RouteReasonRow`, `observerstore.RouteCandidate`,
  `observerstore.RouteWriter` interface, `observerstore.NewRouteWriter(db *sql.DB) RouteWriter`.

- [ ] **Step 1.1: Write the failing writer test FIRST** (tests #19, #20, #8). The schema is NOT yet appended — at this point `OpenSQLite` opens fine but `INSERT INTO route_reasons` returns `no such table: route_reasons`, which is the expected RED.

```go
package observerstore

import (
    "context"
    "database/sql"
    "encoding/json"
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

func openTestObserverStore(t *testing.T) (*SQLiteStore, *sql.DB) {
    t.Helper()
    p := filepath.Join(t.TempDir(), "x.db")
    st, err := OpenSQLite(p)
    require.NoError(t, err)
    t.Cleanup(func() { st.Close() })
    return st, st.DB()
}

func TestRouteReasonsWriter_RoundTrip(t *testing.T) {
    _, db := openTestObserverStore(t)
    w := NewRouteWriter(db)

    row := RouteReasonRow{
        DecisionID:        "dec-1",
        ConversationID:    "conv-1",
        SelectedAgentID:   "slave-A",
        ReasonCode:        "capability_match",
        ReasonText:        "matched skill chat",
        Candidates:        []RouteCandidate{{AgentID: "slave-A", Score: 1, Reason: "capability_match"}, {AgentID: "slave-B", Score: 0, Reason: "no_capability_match"}},
        DecisionStartedAt: time.Unix(1700000000, 0),
        DecisionEndedAt:   time.Unix(1700000000, 12345),
        DecisionDurationNs: 12345,
    }
    require.NoError(t, w.WriteRouteReason(context.Background(), row))

    var got RouteReasonRow
    var candsJSON string
    var startedAt, endedAt string
    require.NoError(t, db.QueryRow(
        `SELECT decision_id, conversation_id, selected_agent_id, reason_code,
                reason_text, candidates_json, decision_started_at,
                decision_ended_at, decision_duration_ns FROM route_reasons WHERE decision_id=?`,
        "dec-1").Scan(
        &got.DecisionID, &got.ConversationID, &got.SelectedAgentID, &got.ReasonCode,
        &got.ReasonText, &candsJSON, &startedAt, &endedAt, &got.DecisionDurationNs,
    ))
    require.Equal(t, row.DecisionID, got.DecisionID)
    require.Equal(t, row.ConversationID, got.ConversationID)
    require.Equal(t, row.SelectedAgentID, got.SelectedAgentID)
    require.Equal(t, row.ReasonCode, got.ReasonCode)
    require.Equal(t, row.ReasonText, got.ReasonText)
    require.Equal(t, row.DecisionDurationNs, got.DecisionDurationNs)
    require.Equal(t, row.DecisionStartedAt.UTC().Format(time.RFC3339Nano), startedAt)
    require.Equal(t, row.DecisionEndedAt.UTC().Format(time.RFC3339Nano), endedAt)
    var cands []RouteCandidate
    require.NoError(t, json.Unmarshal([]byte(candsJSON), &cands))
    require.Equal(t, row.Candidates, cands)
}

func TestRouteReasonsWriter_OnConflictDoNothing(t *testing.T) {
    _, db := openTestObserverStore(t)
    w := NewRouteWriter(db)
    row := RouteReasonRow{
        DecisionID: "dup", ConversationID: "c", ReasonCode: "capability_match",
        DecisionStartedAt: time.Unix(1, 0), DecisionEndedAt: time.Unix(2, 0),
    }
    require.NoError(t, w.WriteRouteReason(context.Background(), row))
    require.NoError(t, w.WriteRouteReason(context.Background(), row))
    var n int
    require.NoError(t, db.QueryRow(`SELECT count(*) FROM route_reasons`).Scan(&n))
    require.Equal(t, 1, n)
}

func TestRouteReasonsWriter_SQLInjection(t *testing.T) {
    _, db := openTestObserverStore(t)
    w := NewRouteWriter(db)
    malicious := `x'); DROP TABLE route_reasons;--`
    row := RouteReasonRow{
        DecisionID: "inj", ConversationID: malicious, ReasonCode: "capability_match",
        DecisionStartedAt: time.Unix(1, 0), DecisionEndedAt: time.Unix(2, 0),
    }
    require.NoError(t, w.WriteRouteReason(context.Background(), row))
    var stored string
    require.NoError(t, db.QueryRow(`SELECT conversation_id FROM route_reasons WHERE decision_id=?`, "inj").Scan(&stored))
    require.Equal(t, malicious, stored)
    // The table must still exist:
    var n int
    require.NoError(t, db.QueryRow(`SELECT count(*) FROM route_reasons`).Scan(&n))
    require.Equal(t, 1, n)
}
```

- [ ] **Step 1.2: Run the tests to see them fail**

```
go test ./internal/observerstore/... -run TestRouteReasons -count=1 -race
```

Expected: compile errors on `RouteReasonRow`/`RouteCandidate`/`NewRouteWriter` — types don't exist yet.

- [ ] **Step 1.3: Implement `route_reasons_writer.go`** (verbatim from spec §5.2).

- [ ] **Step 1.4: Re-run the tests; now they fail at runtime with `no such table: route_reasons` because the schema has not yet been appended.**

```
go test ./internal/observerstore/... -run TestRouteReasons -count=1 -race
```

Expected: assertions fail with `SQL logic error: no such table: route_reasons`.

- [ ] **Step 1.5: Append DDL to `schema.sql`** at the END of the file (the embedded schema is applied on `OpenSQLite`):

```sql

-- WT-1-routing-trace: per-Dispatch decision trace.
-- Captures why one agent was selected over the others; consumed by
-- RoutingLatencyP50P95 (decision_duration_ns) and routing-correctness audits.
CREATE TABLE IF NOT EXISTS route_reasons (
    decision_id           TEXT PRIMARY KEY,
    conversation_id       TEXT NOT NULL,
    selected_agent_id     TEXT NOT NULL DEFAULT '',
    reason_code           TEXT NOT NULL,
    reason_text           TEXT NOT NULL DEFAULT '',
    candidates_json       TEXT NOT NULL DEFAULT '[]',
    decision_started_at   TEXT NOT NULL,
    decision_ended_at     TEXT NOT NULL,
    decision_duration_ns  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_route_reasons_conv
    ON route_reasons(conversation_id, decision_started_at);
```

- [ ] **Step 1.6: Re-run; tests pass.**

```
go test ./internal/observerstore/... -run TestRouteReasons -count=1 -race
```

- [ ] **Step 1.7: Commit**

```
git add multi-agent/internal/observerstore/schema.sql \
        multi-agent/internal/observerstore/route_reasons_writer.go \
        multi-agent/internal/observerstore/route_reasons_writer_test.go
git commit -m "feat(observerstore): add route_reasons table + writer for WT-1-routing-trace

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: dispatch types & helpers (route_decision.go, no dispatch.go change yet)

**Files:**
- Create: `multi-agent/internal/dispatch/route_decision.go`
- Create: `multi-agent/internal/dispatch/route_decision_test.go`

**Interfaces:**
- Consumes: `observerstore.RouteWriter`, `observerstore.RouteReasonRow`,
  `observerstore.RouteCandidate` from Task 1.
- Produces: `dispatch.RouteDecision`, `dispatch.Candidate`,
  `dispatch.ReasonCode` constants, `dispatch.NewDecision`,
  `dispatch.FinalizeAndEmit`, `dispatch.SetWriter`, `dispatch.IsNoopWriter`,
  `dispatch.WrapRouteWriter`, `dispatch.SanitizeReasonText`,
  `dispatch.peekConversationID` (unexported).

- [ ] **Step 2.1: Write the failing tests first** — tests #3, #4, #5, #6, #9, #10–13, #15, #17, #18, #21, #22.

```go
package dispatch

import (
    "context"
    "database/sql"
    "encoding/json"
    "errors"
    "path/filepath"
    "reflect"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/multi-agent/internal/contract"
    "github.com/yourorg/multi-agent/internal/observerstore"
)

// openSQLiteForDispatchTest opens a fresh observerstore SQLite file (with the
// embedded schema applied, including the route_reasons table appended in
// Task 1) and returns its *sql.DB. Cleanup closes the store.
func openSQLiteForDispatchTest(t *testing.T) *sql.DB {
    t.Helper()
    p := filepath.Join(t.TempDir(), "obs.db")
    st, err := observerstore.OpenSQLite(p)
    require.NoError(t, err)
    t.Cleanup(func() { st.Close() })
    return st.DB()
}

// capture is a Writer that stores the last RouteDecision it received.
type capture struct {
    mu  sync.Mutex
    got []RouteDecision
    err error
}

func (c *capture) Write(_ context.Context, d RouteDecision) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.got = append(c.got, d)
    return c.err
}

func (c *capture) last() RouteDecision {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.got[len(c.got)-1]
}

func TestSanitize_RawSecret_Redacted(t *testing.T) {
    in := "leaked: sk-abcdefghijklmnopqrstuv"
    out := SanitizeReasonText(in)
    require.NotContains(t, out, "sk-abcdefghij")
    require.Contains(t, out, "[REDACTED]")
}

func TestSanitize_LongerThan256_Truncated(t *testing.T) {
    in := strings.Repeat("x", 1024)
    out := SanitizeReasonText(in)
    require.True(t, strings.HasSuffix(out, "...[truncated]"))
    // Truncated portion is exactly 256 runes:
    body := strings.TrimSuffix(out, "...[truncated]")
    require.Equal(t, 256, len([]rune(body)))
}

func TestDecisionID_NoParameter(t *testing.T) {
    rt := reflect.TypeOf(NewDecision)
    require.Equal(t, 1, rt.NumIn())
    require.Equal(t, "string", rt.In(0).String())
}

func TestTimestamp_FromMonotonic_NoExternalInject(t *testing.T) {
    // (a) NewDecision must NOT accept a time.Time parameter.
    rt := reflect.TypeOf(NewDecision)
    for i := 0; i < rt.NumIn(); i++ {
        require.NotEqual(t, "time.Time", rt.In(i).String(),
            "NewDecision must NOT accept an externally-provided time.Time (would break §6 (c))")
    }
    // (b) Two consecutive NewDecision calls yield strictly monotonic seeds.
    a := NewDecision("c")
    b := NewDecision("c")
    require.True(t, !b.DecisionStartedAt.Before(a.DecisionStartedAt),
        "second NewDecision must have >= first's StartedAt (monotonic clock)")
    // (c) seedStarted carries a monotonic reading — Sub returns a non-negative
    // duration even if wall clock jumps backward. We can verify monotonic
    // preservation indirectly: the duration computed by FinalizeAndEmit must
    // be > 0 even for instantaneous decisions, because time.Now's monotonic
    // reading advances on every call.
    SetWriter(&capture{})
    t.Cleanup(func() { SetWriter(nil) })
    d := NewDecision("c")
    FinalizeAndEmit(context.Background(), d)
    require.GreaterOrEqual(t, d.DecisionDurationNs, int64(0),
        "DecisionDurationNs must be non-negative (monotonic-clock guarantee)")
}

func TestDecisionID_UniquePerCall(t *testing.T) {
    seen := make(map[string]struct{}, 10000)
    for i := 0; i < 10000; i++ {
        d := NewDecision("same-conv")
        if _, dup := seen[d.DecisionID]; dup {
            t.Fatalf("duplicate DecisionID after %d iterations", i)
        }
        seen[d.DecisionID] = struct{}{}
    }
}

func TestForgery_ConversationID_OverwrittenOnFinalize(t *testing.T) {
    cap := &capture{}
    SetWriter(cap)
    t.Cleanup(func() { SetWriter(nil) })

    d := NewDecision("real-conv")
    d.ConversationID = "FORGED"
    FinalizeAndEmit(context.Background(), d)
    require.Equal(t, "real-conv", cap.last().ConversationID)
}

func TestForgery_StartedAt_OverwrittenOnFinalize(t *testing.T) {
    cap := &capture{}
    SetWriter(cap)
    t.Cleanup(func() { SetWriter(nil) })

    d := NewDecision("c")
    real := d.DecisionStartedAt
    d.DecisionStartedAt = time.Time{}
    FinalizeAndEmit(context.Background(), d)
    require.True(t, cap.last().DecisionStartedAt.Equal(real))
}

func TestForgery_DecisionID_OverwrittenOnFinalize(t *testing.T) {
    cap := &capture{}
    SetWriter(cap)
    t.Cleanup(func() { SetWriter(nil) })

    d := NewDecision("c")
    real := d.DecisionID
    d.DecisionID = "FORGED-ID"
    FinalizeAndEmit(context.Background(), d)
    require.Equal(t, real, cap.last().DecisionID)
}

func TestPeekConversationID(t *testing.T) {
    // Use the real markers from internal/contract so the regex stays in sync
    // with the actual envelope format.
    start := contract.EnvelopeStart   // "<TASK_CONTRACT version=1>"
    end   := contract.EnvelopeEnd     // "</TASK_CONTRACT>"
    cases := []struct{ name, in, want string }{
        {"absent", "hello", ""},
        {"malformed", start + "{bogus}" + end, ""},
        {"present", start + "\n{\"conversation_id\":\"abc-123\",\"version\":1}\n" + end + "\nbody", "abc-123"},
        {"escaped-quotes", start + "\n{\"conversation_id\":\"a\\\"b\"}\n" + end, "a\"b"},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            require.Equal(t, c.want, peekConversationID(c.in))
        })
    }
}

func TestSetWriter_AtomicValueNoPanic(t *testing.T) {
    SetWriter(&capture{})
    SetWriter(&capture{})
    SetWriter(nil)
    require.True(t, IsNoopWriter())
}

func TestIsNoopWriter(t *testing.T) {
    SetWriter(nil)
    require.True(t, IsNoopWriter())
    SetWriter(&capture{})
    t.Cleanup(func() { SetWriter(nil) })
    require.False(t, IsNoopWriter())
}

// TestPersistedRow_ReasonText_Redacted: end-to-end through the real
// observerstore SQL writer — asserts a ReasonText containing a raw secret
// lands in route_reasons.reason_text as "[REDACTED]" and that
// route_reason_redacted_total expvar was incremented. Complements
// TestSanitize_RawSecret_Redacted by verifying FinalizeAndEmit actually
// runs sanitize on the round-trip path before WriteRouteReason is called.
func TestPersistedRow_ReasonText_Redacted(t *testing.T) {
    db := openSQLiteForDispatchTest(t)
    SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(db)))
    t.Cleanup(func() { SetWriter(nil) })

    before := routeReasonRedactedTotal.Value()
    d := NewDecision("conv-leak")
    d.SelectedAgentID = "agent-X"
    d.ReasonCode = ReasonCapabilityMatch
    d.ReasonText = "matched; token=sk-abcdefghijklmnop in capability"
    FinalizeAndEmit(context.Background(), d)

    var stored string
    require.NoError(t, db.QueryRow(
        `SELECT reason_text FROM route_reasons WHERE conversation_id=?`, "conv-leak",
    ).Scan(&stored))
    require.NotContains(t, stored, "sk-abcdefghij")
    require.Contains(t, stored, "[REDACTED]")
    require.Greater(t, routeReasonRedactedTotal.Value(), before,
        "route_reason_redacted_total expvar must be incremented")
}

// TestCandidatesJSON_NoCapabilitySnapshot asserts the DECODED candidates_json
// column in route_reasons contains exactly {agent_id, score, reason} per
// candidate — never any additional fields (capability_snapshot, version,
// credential alias, etc.). Tested via the real observerstore SQL writer so
// any extra columns in the DB schema or upstream Candidate struct mutation
// would surface here, not just at the dispatch unit-test boundary.
func TestCandidatesJSON_NoCapabilitySnapshot(t *testing.T) {
    db := openSQLiteForDispatchTest(t) // helper opens *sql.DB + applies schema
    SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(db)))
    t.Cleanup(func() { SetWriter(nil) })

    d := NewDecision("conv-cand")
    d.Candidates = []Candidate{
        {AgentID: "x", Score: 0.5, Reason: ReasonCapabilityMatch},
        {AgentID: "y", Score: 0.0, Reason: ReasonLoadTooHigh},
    }
    d.SelectedAgentID = "x"
    d.ReasonCode = ReasonCapabilityMatch
    FinalizeAndEmit(context.Background(), d)

    var raw string
    require.NoError(t, db.QueryRow(
        `SELECT candidates_json FROM route_reasons WHERE conversation_id=?`, "conv-cand",
    ).Scan(&raw))
    var arr []map[string]any
    require.NoError(t, json.Unmarshal([]byte(raw), &arr))
    require.Len(t, arr, 2)
    for _, c := range arr {
        keys := make([]string, 0, len(c))
        for k := range c { keys = append(keys, k) }
        require.ElementsMatch(t, []string{"agent_id", "score", "reason"}, keys,
            "candidates_json must contain ONLY agent_id, score, reason — no capability snapshot")
    }
}

func TestWrapRouteWriter_PassesAllFields(t *testing.T) {
    var got observerstore.RouteReasonRow
    w := WrapRouteWriter(rwFunc(func(_ context.Context, r observerstore.RouteReasonRow) error {
        got = r
        return nil
    }))
    d := NewDecision("conv-xyz")
    d.SelectedAgentID = "agent-X"
    d.SelectedNone = false
    d.ReasonCode = ReasonCapabilityMatch
    d.ReasonText = "all-fields-must-round-trip"
    d.Candidates = []Candidate{
        {AgentID: "agent-X", Score: 1.0, Reason: ReasonCapabilityMatch},
        {AgentID: "agent-Y", Score: 0.25, Reason: ReasonLoadTooHigh},
    }
    FinalizeAndEmit(context.Background(), d)
    require.NoError(t, w.Write(context.Background(), *d))

    require.Equal(t, d.DecisionID, got.DecisionID)
    require.Equal(t, d.ConversationID, got.ConversationID)
    require.Equal(t, d.SelectedAgentID, got.SelectedAgentID)
    require.Equal(t, string(d.ReasonCode), got.ReasonCode)
    require.Equal(t, d.ReasonText, got.ReasonText)
    require.Equal(t, len(d.Candidates), len(got.Candidates))
    for i := range d.Candidates {
        require.Equal(t, d.Candidates[i].AgentID, got.Candidates[i].AgentID)
        require.Equal(t, d.Candidates[i].Score, got.Candidates[i].Score)
        require.Equal(t, string(d.Candidates[i].Reason), got.Candidates[i].Reason)
    }
    require.True(t, d.DecisionStartedAt.Equal(got.DecisionStartedAt))
    require.True(t, d.DecisionEndedAt.Equal(got.DecisionEndedAt))
    require.Equal(t, d.DecisionDurationNs, got.DecisionDurationNs)
}

// rwFunc is a function-type adapter implementing observerstore.RouteWriter.
type rwFunc func(context.Context, observerstore.RouteReasonRow) error

func (f rwFunc) WriteRouteReason(ctx context.Context, r observerstore.RouteReasonRow) error {
    return f(ctx, r)
}

func TestWrapRouteWriter_SelectedNoneSentinel(t *testing.T) {
    var got observerstore.RouteReasonRow
    w := WrapRouteWriter(rwFunc(func(_ context.Context, r observerstore.RouteReasonRow) error {
        got = r
        return nil
    }))
    d := NewDecision("c")
    d.SelectedNone = true
    d.SelectedAgentID = "" // would be ambiguous without SelectedNone
    FinalizeAndEmit(context.Background(), d)
    require.NoError(t, w.Write(context.Background(), *d))
    require.Equal(t, "<none>", got.SelectedAgentID)
}

func TestPersistedRow_SelectedNoneSerialization(t *testing.T) {
    // (a) SelectedNone=true → persisted "<none>".
    dbA := openSQLiteForDispatchTest(t)
    SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(dbA)))
    t.Cleanup(func() { SetWriter(nil) })

    da := NewDecision("conv-none")
    da.SelectedNone = true
    da.ReasonCode = ReasonNoCapabilityMatch
    FinalizeAndEmit(context.Background(), da)
    var got string
    require.NoError(t, dbA.QueryRow(
        `SELECT selected_agent_id FROM route_reasons WHERE conversation_id=?`, "conv-none",
    ).Scan(&got))
    require.Equal(t, "<none>", got)

    // (b) SelectedNone=false, SelectedAgentID="" (fallback executor selected) → persisted "".
    dbB := openSQLiteForDispatchTest(t)
    SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(dbB)))
    dec := NewDecision("conv-fallback")
    dec.SelectedNone = false
    dec.SelectedAgentID = ""
    dec.ReasonCode = ReasonCapabilityMatch
    FinalizeAndEmit(context.Background(), dec)
    var got2 string
    require.NoError(t, dbB.QueryRow(
        `SELECT selected_agent_id FROM route_reasons WHERE conversation_id=?`, "conv-fallback",
    ).Scan(&got2))
    require.Equal(t, "", got2)
}

func TestWriter_Errors_AreReturnedNotPanicked(t *testing.T) {
    cap := &capture{err: errors.New("boom")}
    SetWriter(cap)
    t.Cleanup(func() { SetWriter(nil) })
    d := NewDecision("c")
    // FinalizeAndEmit must not panic on writer error
    FinalizeAndEmit(context.Background(), d)
}
```

- [ ] **Step 2.2: Run; expect compile failures** (types not defined).

- [ ] **Step 2.3: Implement `route_decision.go`** with:

  - `ReasonCode` constants per spec §2.
  - `Candidate`, `RouteDecision` (with unexported `seedConv`,
    `seedStarted`, `seedNonce`).
  - `var decisionNonce atomic.Uint64`.
  - `NewDecision(conv string) *RouteDecision` — stamps seed pair + nonce,
    sets exported mirrors, computes initial `DecisionID`.
  - `deriveID(conv string, t time.Time, n uint64) string`.
  - `FinalizeAndEmit(parentCtx, *RouteDecision)` — overwrites mirrors from
    seed; stamps end + duration; sanitizes ReasonText; detaches ctx (2s
    timeout context.Background()); calls writer; logs on error.
  - `Writer` interface + `writerBox` wrapper + `activeWriter atomic.Value`
    + `SetWriter` + `currentWriter` + `IsNoopWriter` + `noopWriter`.
  - `peekConversationID(string) string` — regex extract.
  - `SanitizeReasonText(string) string` — regex blacklist + truncate +
    expvar increment.
  - `WrapRouteWriter(observerstore.RouteWriter) Writer` + adapter.
  - `var routeReasonRedactedTotal = expvar.NewInt("route_reason_redacted_total")`.

- [ ] **Step 2.4: Run; tests pass.**

```
go test ./internal/dispatch/... -run 'Sanitize|Decision|Forgery|Peek|SetWriter|IsNoop|Candidates|WrapRoute|Writer_Errors|Timestamp|PersistedRow' -count=1 -race -shuffle=on
```

- [ ] **Step 2.5: gofmt + vet clean.**

```
gofmt -l internal/dispatch internal/observerstore
go vet ./internal/dispatch/... ./internal/observerstore/...
```

- [ ] **Step 2.6: Commit**

```
git add multi-agent/internal/dispatch/route_decision.go \
        multi-agent/internal/dispatch/route_decision_test.go
git commit -m "feat(dispatch): RouteDecision types, finalize gate, writer wiring

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: dispatch.go hook + integration tests

**Files:**
- Modify: `multi-agent/internal/dispatch/dispatch.go` — insert four lines at top of `Run`, and per-branch population of `dec.SelectedAgentID`/`SelectedNone`/`ReasonCode`/`ReasonText`/`Candidates`.
- Modify: `multi-agent/internal/dispatch/dispatch_test.go` — append tests #1, #2, #7, #14, #16, #23, #24, #25.

**Interfaces:**
- Consumes: everything from Task 2.

- [ ] **Step 3.1: Write the failing integration tests** (append to `dispatch_test.go`) — tests #1, #2, #7, #14, #16, #23, #24, #25:

```go
// at top of the helpers section, add a shared capture writer hook:

func withCaptureWriter(t *testing.T) *capture {
    t.Helper()
    c := &capture{}
    SetWriter(c)
    t.Cleanup(func() { SetWriter(nil) })
    return c
}

func TestDispatch_TwoCandidates_TraceWhyChosen(t *testing.T) {
    cap := withCaptureWriter(t)
    bashExec := &stubExec{res: executor.Result{Summary: "bash-ok"}}
    chatExec := &stubExec{res: executor.Result{Summary: "chat-ok"}}
    d := New(map[string]executor.Executor{"bash": bashExec, "chat": chatExec}, &stubJournal{}, newStore(t), nil)
    _, err := d.Run(context.Background(), executor.Task{ID: "T", Skill: "chat", Prompt: "hi"})
    require.NoError(t, err)
    require.Len(t, cap.got, 1)
    dec := cap.got[0]
    require.Equal(t, "chat", dec.SelectedAgentID)
    require.False(t, dec.SelectedNone)
    require.Equal(t, ReasonCapabilityMatch, dec.ReasonCode)
    // Both candidates listed
    require.Len(t, dec.Candidates, 2)
    var bashCand, chatCand Candidate
    for _, c := range dec.Candidates {
        if c.AgentID == "bash" { bashCand = c } else { chatCand = c }
    }
    require.Equal(t, ReasonNoCapabilityMatch, bashCand.Reason)
    require.Equal(t, ReasonCapabilityMatch, chatCand.Reason)
}

func TestDispatch_NoCandidates_TraceFailure(t *testing.T) {
    cap := withCaptureWriter(t)
    d := New(map[string]executor.Executor{}, &stubJournal{}, newStore(t), nil)
    _, err := d.Run(context.Background(), executor.Task{ID: "T", Skill: "unknown", Prompt: "hi"})
    require.Error(t, err)
    require.Len(t, cap.got, 1)
    require.True(t, cap.got[0].SelectedNone)
    require.Equal(t, "", cap.got[0].SelectedAgentID)
    require.Equal(t, ReasonNoCapabilityMatch, cap.got[0].ReasonCode)
}

// (Test #3 TestPersistedRow_ReasonText_Redacted, test #5
// TestCandidatesJSON_NoCapabilitySnapshot, and test #22
// TestPersistedRow_SelectedNoneSerialization live in route_decision_test.go
// from Task 2 — see the Test → File Placement table.)

func TestDispatch_FinalizeAndEmit_DeferCoversEarlyReturns(t *testing.T) {
    t.Run("malformed-envelope", func(t *testing.T) {
        cap := withCaptureWriter(t)
        d := New(map[string]executor.Executor{"": &stubExec{}}, &stubJournal{}, newStore(t), nil)
        malformed := contract.EnvelopeStart + "\n{\"version\":1}\n"
        _, err := d.Run(context.Background(), executor.Task{ID: "TM", Skill: "chat", Prompt: malformed})
        require.Error(t, err)
        require.Len(t, cap.got, 1)
    })
    t.Run("no-executor", func(t *testing.T) {
        cap := withCaptureWriter(t)
        d := New(map[string]executor.Executor{}, &stubJournal{}, newStore(t), nil)
        _, err := d.Run(context.Background(), executor.Task{ID: "TN", Skill: "x"})
        require.Error(t, err)
        require.Len(t, cap.got, 1)
    })
    t.Run("executor-error", func(t *testing.T) {
        cap := withCaptureWriter(t)
        d := New(map[string]executor.Executor{"": &stubExec{err: errors.New("oops")}}, &stubJournal{}, newStore(t), nil)
        _, err := d.Run(context.Background(), executor.Task{ID: "TE"})
        require.Error(t, err)
        require.Len(t, cap.got, 1)
    })
    t.Run("duplicate-running-sentinel", func(t *testing.T) {
        cap := withCaptureWriter(t)
        s := newStore(t)
        ok, err := s.InsertIfAbsent(store.Task{ID: "TD", Skill: "chat"})
        require.NoError(t, err)
        require.True(t, ok)
        require.NoError(t, s.MarkRunning("TD"))
        d := New(map[string]executor.Executor{"chat": &stubExec{}}, &stubJournal{}, s, nil)
        _, err = d.Run(context.Background(), executor.Task{ID: "TD", Skill: "chat"})
        require.ErrorIs(t, err, ErrDuplicateTaskRunning)
        require.Len(t, cap.got, 1)
    })
}

func TestDispatch_FallbackExecutor_SelectedAsCapabilityMatch(t *testing.T) {
    // When the explicit skill ("xyzzy") is absent and the fallback executor
    // d.routes[""] handles the task, the trace must reflect:
    //   * SelectedAgentID == "" (the fallback key)
    //   * SelectedNone == false (this is NOT a lookup failure)
    //   * ReasonCode == capability_match (the fallback WAS selected on purpose)
    //   * Candidates contains a row for "" with Reason=capability_match
    cap := withCaptureWriter(t)
    fallback := &stubExec{res: executor.Result{Summary: "fallback-ok"}}
    d := New(map[string]executor.Executor{"": fallback}, &stubJournal{}, newStore(t), nil)
    _, err := d.Run(context.Background(), executor.Task{ID: "T-fb", Skill: "xyzzy", Prompt: "hi"})
    require.NoError(t, err)
    require.Len(t, cap.got, 1)
    require.Equal(t, "", cap.got[0].SelectedAgentID)
    require.False(t, cap.got[0].SelectedNone, "fallback selected is NOT a lookup failure")
    require.Equal(t, ReasonCapabilityMatch, cap.got[0].ReasonCode)
    require.Len(t, cap.got[0].Candidates, 1)
    require.Equal(t, "", cap.got[0].Candidates[0].AgentID)
    require.Equal(t, ReasonCapabilityMatch, cap.got[0].Candidates[0].Reason)
}

func TestDispatch_ConversationIDFallback_UsesTaskID(t *testing.T) {
    cap := withCaptureWriter(t)
    d := New(map[string]executor.Executor{"": &stubExec{res: executor.Result{Summary: "ok"}}}, &stubJournal{}, newStore(t), nil)
    _, err := d.Run(context.Background(), executor.Task{ID: "fallback-tid", Prompt: "plain"})
    require.NoError(t, err)
    require.Equal(t, "fallback-tid", cap.got[0].ConversationID)
}

func TestWriter_FailLogged_DispatchContinues(t *testing.T) {
    cap := &capture{err: errors.New("kaboom")}
    SetWriter(cap)
    t.Cleanup(func() { SetWriter(nil) })

    var buf strings.Builder
    log.SetOutput(&buf)
    t.Cleanup(func() { log.SetOutput(os.Stderr) })

    d := New(map[string]executor.Executor{"": &stubExec{res: executor.Result{Summary: "ok"}}}, &stubJournal{}, newStore(t), nil)
    res, err := d.Run(context.Background(), executor.Task{ID: "T-log"})
    require.NoError(t, err, "dispatch must NOT propagate writer error")
    require.Equal(t, "ok", res.Summary)
    require.Contains(t, buf.String(), "[route-trace] write failed:")
    require.Contains(t, buf.String(), "kaboom")
    require.Contains(t, buf.String(), "conv=T-log")
    // §6 (d) requires decision=<id> in the log line so an attacker who
    // forced observer errors cannot then claim "no incident; no trace id".
    require.Regexp(t, `decision=[a-f0-9]{32}`, buf.String(),
        "log line must include decision=<id> so the incident can be traced")
}

// (TestPersistedRow_SelectedNoneSerialization is shown in Task 2 step 2.1
// since it does not exercise Dispatcher.Run — it asserts the
// FinalizeAndEmit→WrapRouteWriter→SQL path directly.)

func TestDispatch_TimestampDetached_PreservesTraceOnCtxCancel(t *testing.T) {
    cap := withCaptureWriter(t)
    d := New(map[string]executor.Executor{"": &stubExec{res: executor.Result{Summary: "ok"}}}, &stubJournal{}, newStore(t), nil)
    ctx, cancel := context.WithCancel(context.Background())
    cancel() // already cancelled
    _, _ = d.Run(ctx, executor.Task{ID: "T-cancel"})
    require.Len(t, cap.got, 1, "trace must still be written even when parent ctx was cancelled")
}

func TestDispatch_Run_SignatureUnchanged(t *testing.T) {
    rt := reflect.TypeOf((*Dispatcher)(nil)).Method(methodIndex(t, "Run")).Type
    require.Equal(t, "func(*dispatch.Dispatcher, context.Context, executor.Task) (executor.Result, error)", rt.String())
}

func methodIndex(t *testing.T, name string) int {
    t.Helper()
    rt := reflect.TypeOf((*Dispatcher)(nil))
    for i := 0; i < rt.NumMethod(); i++ {
        if rt.Method(i).Name == name {
            return i
        }
    }
    t.Fatalf("method %q not found", name)
    return -1
}
```

(Adds these imports to `dispatch_test.go`: `log`, `os`, `reflect`, `strings`, `time`.)

- [ ] **Step 3.2: Run; expect failures** (`dispatch.go` does not yet emit traces).

- [ ] **Step 3.3: Modify `dispatch.go` `Run`** — insert at the top:

```go
func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
    conv := peekConversationID(t.Prompt)
    if conv == "" {
        conv = t.ID
    }
    dec := NewDecision(conv)
    defer FinalizeAndEmit(ctx, dec)

    // Populate candidate list from d.routes (sorted skill keys for determinism).
    skills := make([]string, 0, len(d.routes))
    for k := range d.routes {
        skills = append(skills, k)
    }
    sort.Strings(skills)

    // ... existing body, with the following branch-specific writes to dec:
    //   * envelope decode err: dec.ReasonCode=ReasonUnknown, dec.ReasonText="malformed envelope"
    //   * exec lookup fails:   dec.SelectedNone=true; dec.ReasonCode=ReasonNoCapabilityMatch;
    //                          for _, s := range skills { dec.Candidates = append(dec.Candidates, Candidate{AgentID:s, Score:0, Reason:ReasonNoCapabilityMatch}) }
    //   * exec found:          matchedKey is t.Skill when d.routes[t.Skill] != nil,
    //                          else "" (fallback executor). Set:
    //                          dec.SelectedAgentID = matchedKey;
    //                          dec.SelectedNone = false;
    //                          dec.ReasonCode=ReasonCapabilityMatch; dec.ReasonText="matched skill " + t.Skill;
    //                          for _, s := range skills {
    //                            r := ReasonNoCapabilityMatch; score := 0.0
    //                            if s == matchedKey { r = ReasonCapabilityMatch; score = 1.0 }
    //                            dec.Candidates = append(dec.Candidates, Candidate{AgentID:s, Score:score, Reason:r})
    //                          }
    //   * duplicate-running:   dec.ReasonCode=ReasonUnknown; dec.ReasonText="duplicate task running"
    //   * exec error:          dec.ReasonCode=ReasonUnknown; dec.ReasonText="executor returned error"
    //                          (do NOT include the executor's actual error string; sanitize would catch
    //                          a secret leak but principle of minimum disclosure applies)
    // ... return as before
}
```

(Also import `sort` if not present.)

- [ ] **Step 3.4: Re-run integration tests.**

```
go test ./internal/dispatch/... -count=1 -race -shuffle=on
```

- [ ] **Step 3.5: Final full sweep + lint.**

```
go test ./internal/dispatch/... ./internal/observerstore/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l internal/dispatch internal/observerstore
```

- [ ] **Step 3.6: Commit**

```
git add multi-agent/internal/dispatch/dispatch.go \
        multi-agent/internal/dispatch/dispatch_test.go
git commit -m "feat(dispatch): emit RouteDecision trace from Dispatcher.Run

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

* Spec §2 (types) → Tasks 2 + tests #9–13, #21.
* Spec §3 (dispatch hook) → Task 3 + tests #1, #2, #14, #16, #23, #24.
* Spec §4 (DDL) → Task 1 step 1.1 + test #19.
* Spec §5 (writer) → Task 1 + tests #19, #20, #8, #21, #22.
* Spec §6 (a) sanitize → Tests #3, #4.
* Spec §6 (b) candidates_json → Test #5.
* Spec §6 (c) monotonic → Test #6 + #11.
* Spec §6 (d) writer-fail logged → Test #7.
* Spec §6 (e) parameterized SQL → Test #8.
* Spec §6 (f) DecisionID derived → Tests #9, #12, #13.
* Spec §3.2 defer covers early returns → Test #14.
* Spec §2.2 atomic writer + IsNoopWriter → Tests #17, #18.
* Spec §3.3 signature unchanged → Test #24.

No placeholder steps. No "similar to". All file paths absolute below
`multi-agent/.worktrees/p1-routing-trace/`. Commit messages include the
mandatory trailer.
