# WT-1-routing-trace — Spec

> Scope: `multi-agent/internal/dispatch/` produces a structured `RouteDecision`
> trace at every dispatch; trace rows land in observer SQLite via a new
> `route_reasons` table. Drives the `RoutingLatencyP50P95` metric consumed by
> WT-2-overhead-probes.
>
> Out of scope: no changes to driver / executor business logic; no new ablation
> flags (those land via WT-1-ablation-registry).

## 1. Background

Today `dispatch.Dispatcher.Run` resolves an executor by skill string and runs
it. **Why** one executor was chosen over the others is invisible — there is no
record of which candidates were considered, what their scores were, or why
non-winners lost. This blocks:

* Routing-correctness debugging ("we expected `slave-A`, got `slave-B`; why?").
* `RoutingLatencyP50P95` from D7 (no decision timestamps recorded today).
* Post-hoc fairness / load-balance audits.

This spec adds an out-of-band trace: every `Dispatcher.Run` decision produces a
`RouteDecision`, persists it via a pluggable writer, and **does not change** the
existing `Run(...) (executor.Result, error)` signature.

## 2. Data structures

All public types live in a new file `internal/dispatch/route_decision.go`.

```go
type ReasonCode string

const (
    ReasonCapabilityMatch   ReasonCode = "capability_match"
    ReasonVersionTooOld     ReasonCode = "version_too_old"
    ReasonForbiddenCred     ReasonCode = "forbidden_cred"
    ReasonNotReachable      ReasonCode = "not_reachable"
    ReasonLoadTooHigh       ReasonCode = "load_too_high"
    ReasonNoCapabilityMatch ReasonCode = "no_capability_match"
    ReasonUnknown           ReasonCode = "unknown"
)

type Candidate struct {
    AgentID string     `json:"agent_id"`
    Score   float64    `json:"score"`  // 0..1; higher = better
    Reason  ReasonCode `json:"reason"` // why this candidate scored as it did
}

type RouteDecision struct {
    // Caller-mutable fields (populated between NewDecision and FinalizeAndEmit).
    Candidates      []Candidate
    SelectedAgentID string      // skill key ("" is the fallback executor's id; use
                                // SelectedNone to mean "no candidate matched", see below)
    SelectedNone    bool        // true iff no executor matched at all; disambiguates
                                // SelectedAgentID == "" (legitimate fallback) from
                                // "lookup failure" — readers MUST check this flag, not
                                // SelectedAgentID == "".
    ReasonCode      ReasonCode  // selection reason (or failure reason)
    ReasonText      string      // human-readable; sanitized (see §6 (a))

    // Read-only mirrors of the unexported canonical seed (§2.1).
    // Any caller mutation to these is unconditionally OVERWRITTEN inside
    // FinalizeAndEmit from the unexported seed pair below. Exported only
    // because the writer in another package serializes them.
    ConversationID     string
    DecisionID         string
    DecisionStartedAt  time.Time   // monotonic; sourced from time.Now in NewDecision
    DecisionEndedAt    time.Time   // monotonic; sourced from time.Now in FinalizeAndEmit
    DecisionDurationNs int64       // EndedAt.Sub(StartedAt) in ns — written by FinalizeAndEmit

    // Unexported canonical seed: ONLY NewDecision writes; FinalizeAndEmit
    // reads to overwrite the exported mirrors. Because struct literals in
    // another package cannot set unexported fields, callers outside the
    // dispatch package cannot construct a forged seed.
    seedConv    string
    seedStarted time.Time
    seedNonce   uint64    // ensures unique DecisionID even on same-nanosecond calls
}
```

### 2.1 DecisionID derivation (§6 (f)) and timestamp anchoring (§6 (c))

`NewDecision(conversationID string) *RouteDecision` is the only constructor.
It writes:

```
d.seedConv    = conversationID
d.seedStarted = time.Now()                  // monotonic reading retained
d.seedNonce   = decisionNonce.Add(1)   // process-local monotonic counter, var decisionNonce atomic.Uint64
d.ConversationID    = d.seedConv
d.DecisionStartedAt = d.seedStarted
d.DecisionID        = deriveID(d.seedConv, d.seedStarted, d.seedNonce)

// deriveID:
sum := sha256.Sum256([]byte(
    conv + "|" + strconv.FormatInt(t.UnixNano(), 10) + "|" + strconv.FormatUint(n, 10),
))
return hex.EncodeToString(sum[:16])   // 32 hex chars
```

The `seedNonce` (process-local `atomic.Uint64` counter starting at 1) is
mixed into the hash so that two `NewDecision` calls landing on the same
nanosecond timestamp (possible on coarse-resolution clocks or under heavy
load) still produce distinct `DecisionID`s. `seedNonce` is also an
unexported field that callers in other packages cannot set via struct
literal — same forgery shield as `seedConv` / `seedStarted`. Test
`TestDecisionID_UniquePerCall` constructs 10 000 decisions with the same
conversationID in a tight loop and asserts |distinct IDs| == 10000.

`FinalizeAndEmit` then **always** re-applies the seed before serialization:

```
end := time.Now()
// DecisionID derives from the RAW seedConv (sha256 fingerprint hides any
// underlying secret); ConversationID is then ALSO sanitized before write +
// log emission — defense-in-depth (round-3 review): conversation_id
// values come from caller-supplied envelope text via peekConversationID,
// so even though they're typically UUID-shaped we'd rather redact a
// stray secret than let it land in route_reasons.conversation_id.
d.DecisionStartedAt  = d.seedStarted
d.DecisionEndedAt    = end
d.DecisionDurationNs = end.Sub(d.seedStarted).Nanoseconds()   // monotonic-preserving
d.DecisionID         = deriveID(d.seedConv, d.seedStarted, d.seedNonce)
d.ConversationID     = SanitizeReasonText(d.seedConv)
d.ReasonText         = SanitizeReasonText(d.ReasonText)
```

Because the seed pair is unexported, code outside `internal/dispatch` cannot
forge an alternate `(seedConv, seedStarted)` via struct literal — and even if
they mutate the exported mirrors, those mutations are wiped here. Tests
covering this gate: `TestForgery_ConversationID_OverwrittenOnFinalize`,
`TestForgery_StartedAt_OverwrittenOnFinalize`,
`TestForgery_DecisionID_OverwrittenOnFinalize` (each constructs a
`RouteDecision` via `NewDecision`, mutates one exported mirror, calls
`FinalizeAndEmit`, and asserts the writer received the seed value).

`DecisionDurationNs` is the **authoritative** routing latency value;
`RoutingLatencyP50P95` consumers use this column rather than
`decision_ended_at - decision_started_at` of RFC3339 strings (which would lose
the monotonic reading once the timestamps round-trip through wall-clock
formatting). Schema §4 carries a `decision_duration_ns INTEGER` column for
exactly this reason.

### 2.2 Writer interface and thread-safety

```go
type Writer interface {
    Write(ctx context.Context, d RouteDecision) error
}
```

The package keeps a single writer behind an `atomic.Value` so `SetWriter` and
`Dispatcher.Run` (running on many goroutines) never race. To avoid the
`atomic.Value` "inconsistent type" panic (storing a concrete `noopWriter{}`
first then a concrete `*routeReasonsWriter` later trips the same-concrete-
type invariant), we store a **fixed wrapper type** that always carries an
interface value:

```go
type writerBox struct{ w Writer }   // fixed concrete type, w may be any Writer

var activeWriter atomic.Value // always holds writerBox

func init()                                  { activeWriter.Store(writerBox{w: noopWriter{}}) }
func SetWriter(w Writer) {
    if w == nil { w = noopWriter{} }
    activeWriter.Store(writerBox{w: w})
}
func currentWriter() Writer { return activeWriter.Load().(writerBox).w }
```

Default writer is `noopWriter{}` (returns nil from Write).

**Wiring location is out of scope for this worktree.** This WT delivers the
infrastructure: the `Writer` interface, the in-process SQL writer
(`observerstore.NewRouteWriter`), the schema migration, the dispatch hook,
and the `IsNoopWriter` guard helper. `Dispatcher` runs in
`cmd/slave-agent` while observer's `*sql.DB` lives in
`cmd/observer-server` — bridging the two requires a remote-writer adapter
(slave-side `observerclient.NewRouteWriter` + a `POST /api/route-trace`
handler on observer-server). The task brief restricts this WT's file
domain to `internal/dispatch/` and `internal/observerstore/`; the cmd
wiring lands in a follow-up WT (WT-2-overhead-probes is the natural
consumer of `RoutingLatencyP50P95` and will own the slave→observer
adapter). Until that lands, slave dispatch traces are emitted to the noop
writer — **the design exposes `IsNoopWriter()` precisely so the
follow-up's main can `log.Fatal` if it forgets to call SetWriter.**

Tests in the dispatch package use `SetWriter(&captureWriter{})` plus
`t.Cleanup(func(){ SetWriter(nil) })` to assert what the writer received.
Observerstore writer tests exercise the in-process SQL writer end-to-end
against a real SQLite file (verifying the §5 SQL is exercised).

Acknowledged limitation: until the follow-up cmd-wiring lands, the
`RoutingLatencyP50P95` data path is "primed but unfed". That is intentional
— it lets P2 and P1 land in parallel without entangling file domains.

## 3. Dispatch hook

`Dispatcher.Run` is amended **without changing its signature or return value**.

### 3.1 Candidate model for the current routes map

`Dispatcher.routes` is `map[string]executor.Executor` — the key is a skill
string ("", "mcp", "bash", "chat", "chat_resume", "register_mcp",
"unregister_mcp"). The dispatcher does **not** today resolve "which slave
agent" — it resolves "which executor handles this skill". For this trace's
purposes:

* `Candidate.AgentID` is set to the skill key (`""`, `"mcp"`, …). When a real
  multi-agent router lands (post-Phase-1) the field's semantic shifts to slave
  agent id without breaking the schema.
* The candidate list is built by iterating `d.routes` in sorted key order
  (stable test output). The chosen candidate gets `Reason =
  ReasonCapabilityMatch` and `Score = 1.0`. Non-chosen candidates get `Reason
  = ReasonNoCapabilityMatch` and `Score = 0.0`.
* The fallback executor (`d.routes[""]`) is reported with its own row
  (`AgentID = ""`); when it is the actual winner because the explicit skill
  was absent, it's still tagged `ReasonCapabilityMatch`.
* When the lookup fails (no entry for the skill and no fallback),
  `SelectedAgentID = ""`, `SelectedNone = true`, and `ReasonCode =
  ReasonNoCapabilityMatch`. The candidate list still enumerates all routes
  so an audit can confirm "yes, these are the executors that existed; none
  matched."
* When the fallback executor `d.routes[""]` wins because the explicit skill
  was absent, `SelectedAgentID = ""` and `SelectedNone = false`. Readers
  distinguish "lookup failure" from "fallback selected" via `SelectedNone`,
  not via `SelectedAgentID == ""`.

The persisted row carries `selected_agent_id` exactly as set; the
`SelectedNone` boolean is encoded by storing the literal string `"<none>"`
in `selected_agent_id` when `SelectedNone == true`. This keeps the schema
column TEXT NOT NULL DEFAULT '' (no new column) while making downstream
queries unambiguous. Test `TestPersistedRow_SelectedNoneSerialization`
covers both serializations.

### 3.2 Lifecycle (every return path covered)

1. **First** thing in `Dispatcher.Run`, before `InsertIfAbsent`, decode-error
   handling, or any other return-bearing call, capture the conversation_id
   and construct the decision:

   ```go
   func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
       conv := peekConversationID(t.Prompt)
       if conv == "" {
           conv = t.ID                        // never persist an empty conversation_id
       }
       dec := NewDecision(conv)
       defer FinalizeAndEmit(ctx, dec)        // covers EVERY return path, including the early
                                              //   ones at InsertIfAbsent, malformed envelope,
                                              //   duplicate-running sentinel, no-executor, and
                                              //   exec.Run error.
       // ... existing body ...
   }
   ```

   `peekConversationID(prompt string) string` is a lightweight in-package
   helper that scans for the `TASK_CONTRACT` start marker and extracts only
   the `conversation_id` JSON field via a small regex. It never returns an
   error; on any parse failure it returns `""`. The two-line fallback above
   substitutes `t.ID` so the persisted `conversation_id` column is always
   non-empty (the DDL declares it `NOT NULL`, but we want a meaningful
   value, not an empty string). This avoids the double-decode foot-gun and
   keeps the change scoped to the `dispatch` package —
   `contract.DecodeEnvelope`'s signature is left untouched. Test
   `TestPeekConversationID` covers: present, absent, malformed, escaped
   quotes. Test `TestDispatch_ConversationIDFallback_UsesTaskID` verifies
   the fallback substitution and that the persisted row's
   `conversation_id` equals `t.ID`.

   **Post-success persistence failure** (added after code-review round 2):
   the success path runs `exec.Run` and then `d.store.Complete(t.ID,
   stored)`. If `Complete` fails after `exec.Run` already succeeded, the
   route decision itself was sound — `ReasonCode` stays
   `ReasonCapabilityMatch` — but `ReasonText` must be amended to record
   the downstream persistence failure, otherwise the audit row reads as a
   clean success and obscures the incident:

       dec.ReasonText = "matched skill " + t.Skill + "; store.Complete failed: " + err.Error()

   `FinalizeAndEmit` then sanitizes the concatenated text (including any
   tainted substring inside `err.Error()`) via
   `secretscrub.Sanitize`, so this concatenation cannot widen the
   blast radius established by §6(a). Test
   `TestDispatch_StoreCompleteFails_TraceRecordsFailure` covers it.

   With this placement the `defer` fires for **all** early-return paths
   (envelope-decode failure, duplicate-task replay, no-executor lookup miss,
   executor-error, timeout, success). Each early branch that knows the
   reason populates `dec.SelectedAgentID` / `dec.ReasonCode` /
   `dec.ReasonText` before returning; `FinalizeAndEmit` then writes the row.

2. Populate the candidate list and decide winner per §3.1 immediately after
   executor lookup. If the lookup fails, populate failure code/text *now* —
   before any early `return executor.Result{}, err`.

3. `FinalizeAndEmit(parentCtx context.Context, dec *RouteDecision)` does:

   * Overwrites the four exported mirror fields from the unexported seed
     pair (see §2.1) — wipes any caller mutation.
   * Stamps `dec.DecisionEndedAt = time.Now()` and
     `dec.DecisionDurationNs = end.Sub(d.seedStarted).Nanoseconds()`.
   * Re-derives `dec.DecisionID = deriveID(d.seedConv, d.seedStarted, d.seedNonce)`.
   * Sanitizes `dec.ReasonText` (§6 (a)).
   * **Detaches the parent context for the writer call** to a bounded
     timeout so a cancelled / shutdown-triggered parent context does not
     also cancel trace persistence:

     ```go
     writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
     defer cancel()
     err := currentWriter().Write(writeCtx, *dec)
     ```

     Rationale: the trace is an audit artifact, not a request-scoped
     side-effect. If the slave is being shut down (parent ctx cancelled),
     we still want the routing decision recorded — that's exactly when
     incident-reconstruction needs it most. The 2 s ceiling caps shutdown
     latency. The detached context is `context.Background()` derived, so
     it does not inherit cancellation, but it is bounded so a wedged
     writer cannot leak goroutines either.

   * Logs `[route-trace] write failed: <err> conv=<conv> decision=<id>` on
     a non-nil error (§6 (d)). The error is **not** propagated; the
     business `Run` result is preserved untouched. Silent drops are
     forbidden — the log line must always fire on `err != nil`.

### 3.3 What MUST NOT change

* `Dispatcher.Run` return type and value remain `(executor.Result, error)`.
* The `executor.Executor.Run` interface is untouched.
* `JournalRecorder.Record` is not called more / fewer times.
* Observer event emission lifecycle is identical.
* `contract.DecodeEnvelope` is **not** amended. The conversation_id capture
  uses the in-package `peekConversationID` helper (§3.2) — zero blast
  radius on the `contract` package's other callers.

## 4. DDL (append to `internal/observerstore/schema.sql`)

```sql
-- WT-1-routing-trace: per-Dispatch decision trace.
-- Captures why one agent was selected over the others; consumed by
-- RoutingLatencyP50P95 (decision timestamps) and routing-correctness audits.
CREATE TABLE IF NOT EXISTS route_reasons (
    decision_id           TEXT PRIMARY KEY,
    conversation_id       TEXT NOT NULL,
    selected_agent_id     TEXT NOT NULL DEFAULT '',
    reason_code           TEXT NOT NULL,
    reason_text           TEXT NOT NULL DEFAULT '',
    candidates_json       TEXT NOT NULL DEFAULT '[]',
    decision_started_at   TEXT NOT NULL,                 -- RFC3339Nano, audit/debug only
    decision_ended_at     TEXT NOT NULL,                 -- RFC3339Nano, audit/debug only
    decision_duration_ns  INTEGER NOT NULL DEFAULT 0     -- AUTHORITATIVE for p50/p95 latency
);
CREATE INDEX IF NOT EXISTS idx_route_reasons_conv
    ON route_reasons(conversation_id, decision_started_at);
```

* Appended at the **end** of `schema.sql` (avoids merge conflicts with
  capability-snapshot / run-schema worktrees that own the prior tail).
* `candidates_json` is a JSON-encoded array of `{agent_id, score, reason}`
  objects — **no capability snapshot copy** (§6 (b)).
* All timestamps stored as `RFC3339Nano` strings (matches other observer
  tables).

## 5. Writer (new file `internal/observerstore/route_reasons_writer.go`)

### 5.1 Import-cycle constraint

`dispatch` imports `executor`, and many `executor` files import
`observerstore` (`bash.go`, `chat_resume.go`, etc.). Therefore
**`observerstore` cannot import `dispatch`** — that would close a cycle.

The writer therefore takes a **plain-Go-typed input struct local to
`observerstore`**, and the dispatch hook converts `RouteDecision` →
`observerstore.RouteReasonRow` at the call site (one direction only;
`observerstore` has no reverse dependency on `dispatch`).

### 5.2 Plain-input writer

```go
package observerstore

import (
    "context"
    "database/sql"
    "encoding/json"
    "time"

    "github.com/yourorg/multi-agent/internal/secretscrub"
)

// RouteReasonRow is the data shape persisted by NewRouteWriter. It mirrors
// the dispatch.RouteDecision fields but is defined locally so observerstore
// does not import dispatch (would create an import cycle through executor).
type RouteReasonRow struct {
    DecisionID         string
    ConversationID     string
    SelectedAgentID    string
    ReasonCode         string
    ReasonText         string
    Candidates         []RouteCandidate
    DecisionStartedAt  time.Time
    DecisionEndedAt    time.Time
    DecisionDurationNs int64
}

type RouteCandidate struct {
    AgentID string  `json:"agent_id"`
    Score   float64 `json:"score"`
    Reason  string  `json:"reason"`
}

// RouteWriter writes one RouteReasonRow per call. Implementations must be
// goroutine-safe.
type RouteWriter interface {
    WriteRouteReason(ctx context.Context, r RouteReasonRow) error
}

type routeReasonsWriter struct{ db *sql.DB }

// NewRouteWriter returns a RouteWriter backed by the provided *sql.DB.
// The schema migration (CREATE TABLE IF NOT EXISTS route_reasons) is
// applied by observerstore.OpenSQLite via the embedded schema.sql.
//
// SQLite-only in this WT: the INSERT uses `?` placeholders and sends
// candidates_json as a plain string. pgx/v5/stdlib does not rewrite `?`
// nor auto-cast string→jsonb, so wiring this writer against pg will
// fail every call with `syntax error at or near "?"`. A pg-native
// writer (with `$N` placeholders + `::jsonb` cast, alongside a matching
// pg DDL block) is deferred to the follow-up WT that wires
// cmd/slave-agent → observer-server → observerstore.
func NewRouteWriter(db *sql.DB) RouteWriter { return &routeReasonsWriter{db: db} }

func (w *routeReasonsWriter) WriteRouteReason(ctx context.Context, r RouteReasonRow) error {
    // Defense-in-depth: scrub the two free-form text columns at the writer
    // boundary so a future caller bypassing dispatch.FinalizeAndEmit (e.g.
    // the spec'd follow-up observer HTTP handler) cannot land a raw secret
    // here. Idempotent on the WrapRouteWriter path (dispatch already
    // redacted; the [REDACTED] literal does not re-match the regex).
    r.ReasonText     = secretscrub.Sanitize(r.ReasonText)
    r.ConversationID = secretscrub.Sanitize(r.ConversationID)
    payload, err := json.Marshal(r.Candidates)
    if err != nil {
        return err
    }
    if len(r.Candidates) == 0 {
        payload = []byte("[]")
    }
    _, err = w.db.ExecContext(ctx,
        `INSERT INTO route_reasons(
            decision_id, conversation_id, selected_agent_id,
            reason_code, reason_text, candidates_json,
            decision_started_at, decision_ended_at, decision_duration_ns)
         VALUES(?,?,?,?,?,?,?,?,?)
         ON CONFLICT(decision_id) DO NOTHING`,
        r.DecisionID, r.ConversationID, r.SelectedAgentID,
        r.ReasonCode, r.ReasonText, string(payload),
        r.DecisionStartedAt.UTC().Format(time.RFC3339Nano),
        r.DecisionEndedAt.UTC().Format(time.RFC3339Nano),
        r.DecisionDurationNs,
    )
    return err
}
```

### 5.3 Bridge: `observerstore.RouteWriter` → `dispatch.Writer`

The conversion lives in `internal/dispatch/route_decision.go` (where
`dispatch.Writer` is already defined), because the conversion direction is
`dispatch.RouteDecision` → `observerstore.RouteReasonRow` and `dispatch`
already imports nothing reverse-cyclic by adding `observerstore` to its
own imports — **does it?** Cross-check: `executor` imports `observerstore`,
`dispatch` imports `executor`, so transitively `dispatch` already pulls
`observerstore` in. Adding a direct `observerstore` import to
`internal/dispatch/route_decision.go` is therefore safe (it does not
create a new cycle because none of those packages imports `dispatch`).

```go
// in internal/dispatch/route_decision.go
import "github.com/yourorg/multi-agent/internal/observerstore"

// WrapRouteWriter adapts an observerstore.RouteWriter into a dispatch.Writer.
// Slave-agent boot calls dispatch.SetWriter(dispatch.WrapRouteWriter(observerstore.NewRouteWriter(db)))
// — that wiring lives in cmd/slave-agent (out of this WT's file domain, see §6 (d)).
func WrapRouteWriter(w observerstore.RouteWriter) Writer {
    return &observerWriterAdapter{w: w}
}

type observerWriterAdapter struct{ w observerstore.RouteWriter }

func (a *observerWriterAdapter) Write(ctx context.Context, d RouteDecision) error {
    cands := make([]observerstore.RouteCandidate, len(d.Candidates))
    for i, c := range d.Candidates {
        cands[i] = observerstore.RouteCandidate{AgentID: c.AgentID, Score: c.Score, Reason: string(c.Reason)}
    }
    selected := d.SelectedAgentID
    if d.SelectedNone {
        selected = "<none>"   // sentinel; see §3.1 disambiguation
    }
    return a.w.WriteRouteReason(ctx, observerstore.RouteReasonRow{
        DecisionID:         d.DecisionID,
        ConversationID:     d.ConversationID,
        SelectedAgentID:    selected,
        ReasonCode:         string(d.ReasonCode),
        ReasonText:         d.ReasonText,
        Candidates:         cands,
        DecisionStartedAt:  d.DecisionStartedAt,
        DecisionEndedAt:    d.DecisionEndedAt,
        DecisionDurationNs: d.DecisionDurationNs,
    })
}
```

Test `TestWrapRouteWriter_PassesAllFields` constructs a captureWriter that
records the `RouteReasonRow` it received and asserts every field round-trips.

`ON CONFLICT(decision_id) DO NOTHING` makes the writer idempotent against the
theoretical case of two finalize calls colliding on the same `DecisionID`.
That doesn't suppress duplicate-delivered tasks (those produce different
`DecisionID`s because the second `Run` stamps a new `DecisionStartedAt`) —
that's by design: a duplicate delivery IS a separate routing decision that
the audit table should record on its own row, not silently coalesce. The
ON CONFLICT is defense against a `FinalizeAndEmit` somehow being invoked
twice on the same `*RouteDecision` (e.g., a future refactor that adds an
explicit call in addition to the defer).

## 6. Security

The blast radius of getting this wrong is observer-side credential exposure +
clock-skew-driven p50/p95 lies. Mitigations are **mandatory**, not aspirational.

### (a) `ReasonText` raw-secret sanitize

The dispatcher composes `ReasonText` from internal facts (skill name, candidate
list size) — but it may include capability snippets, model names, or other
caller-influenced fragments. Before the field hits the writer it MUST be run
through a shared sanitize helper.

**Single source of truth**: the regex blacklist, the truncation rule, and the
`route_reason_redacted_total` `expvar.Int` all live in
`internal/secretscrub` (one stdlib-only package, depended on by both
`internal/dispatch` and `internal/observerstore`). Splitting the blacklist
into a third package means the dispatch finalize gate and the observerstore
writer boundary cannot drift — adding a new pattern is a one-line change
that automatically benefits both call sites.

1. Apply a regex blacklist that matches common secret prefixes and replaces
   each match with `[REDACTED]`. Current pattern list (live in
   `internal/secretscrub/scrub.go`):

   ```
   regexp.MustCompile(
     `sk-[A-Za-z0-9_\-]{8,}|` +              // OpenAI / Anthropic (sk-, sk-ant-)
     `eyJ[A-Za-z0-9_\-\.]{16,}|` +            // JWT
     `AKIA[A-Z0-9]{12,}|` +                   // AWS access key
     `gh[opsruA-Z]_[A-Za-z0-9]{20,}|` +       // GitHub gh{p,o,s,r,u}_ tokens
     `github_pat_[A-Za-z0-9_]{20,}|` +        // GitHub fine-grained PATs
     `glpat-[A-Za-z0-9_\-]{20,}|` +           // GitLab PATs
     `AIza[A-Za-z0-9_\-]{20,}|` +             // Google API keys
     `xox[baprs]-[A-Za-z0-9-]{8,}|` +         // Slack
     `-----BEGIN [A-Z ]*PRIVATE KEY-----`,    // PEM private keys
   )
   ```

2. After redaction, truncate to ≤ 256 runes; on truncation append the literal
   suffix `...[truncated]` (so the total is at most `256 + len(suffix)` runes).

3. On any redaction (one or more pattern hits within a single call),
   increment `route_reason_redacted_total` by one. Multiple hits per call
   still bump the counter only once — it counts CALLS-with-redaction, not
   redaction occurrences. Idempotent: a second `Sanitize` on an
   already-redacted string returns the same string and does NOT bump.

The sanitize function is exported by the secretscrub package as
`Sanitize(s string) string`. `internal/dispatch` re-exports it as
`var SanitizeReasonText = secretscrub.Sanitize` (function-pointer alias,
not a wrapper) so the test
`TestSanitizeReasonText_DelegatesToSecretscrub` can assert literal
function identity (`reflect.ValueOf(...).Pointer()`) and structurally
catch a future refactor that inlines a stripped-down regex. The mutable
`var` is reassignable inside the dispatch package — that is an
intentional tradeoff for identity-testability and is acceptable because
no production or test code in the package reassigns it.

`FinalizeAndEmit` calls `SanitizeReasonText` unconditionally on every
write path for BOTH fields that carry caller-influenced text:

* `d.ReasonText` (primary leak surface — composed inside dispatch).
* `d.ConversationID` (defense-in-depth — sourced from caller envelope
  text via `peekConversationID`). The raw `seedConv` is still used
  internally for `deriveID` because a sha256 fingerprint does not leak
  the underlying value, but the serialized `conversation_id` column AND
  the writer-fail log line `conv=%s` both see the sanitized version.

`internal/observerstore.WriteRouteReason` calls `secretscrub.Sanitize`
again at the writer boundary on both `r.ReasonText` AND `r.ConversationID`
as defense-in-depth, so a future caller bypassing dispatch (e.g. the
spec'd follow-up observer HTTP handler that reconstructs a
`RouteReasonRow` from a remote slave's POST body) cannot land a raw
secret in either column. The second pass is idempotent on the
WrapRouteWriter path (dispatch already redacted; `[REDACTED]` does not
re-match the regex) so the counter does not double-count.

Tests covering ConversationID sanitization:
`TestFinalizeAndEmit_SanitizesConversationID` (dispatch side),
`TestWriteRouteReason_SanitizesConversationID` (writer boundary).

### (b) `candidates_json` minimum-field rule

The writer's serialization of `Candidate` includes **only** `agent_id`,
`score`, and `reason`. Capability snapshots, version strings, and credential
aliases of candidates are forbidden in this column — they already live in
`capability_snapshots` (sibling worktree). Enforcing one source of truth
prevents redundant disclosure and divergence.

Test `TestCandidatesJSON_NoCapabilitySnapshot` asserts the unmarshalled column
has exactly the field set `{agent_id, score, reason}`.

### (c) Timestamps are monotonic-clock-sourced

`DecisionStartedAt` / `DecisionEndedAt` are both stamped via `time.Now()`
inside this package (Go's `time.Now` carries a monotonic reading). The public
constructors **do not accept** an externally-supplied `time.Time`, and the
seed pair `(seedConv, seedStarted)` is unexported so no caller in another
package can construct a forged `RouteDecision` even via a struct literal.

The RFC3339-string timestamps stored in `decision_started_at` /
`decision_ended_at` are for human audit only — they lose the Go monotonic
reading once formatted. The authoritative latency is the
`decision_duration_ns INTEGER` column computed inside `FinalizeAndEmit` as
`end.Sub(d.seedStarted).Nanoseconds()` while both values still carry their
monotonic readings. `RoutingLatencyP50P95` queries `decision_duration_ns`,
not the timestamp subtraction.

### (d) Writer failure logged, never silent

`FinalizeAndEmit` logs `[route-trace] write failed: ... conv=<id>
decision=<id>` via `log.Printf` on any non-nil writer error. The error is
**not** propagated; the business `Run` result is preserved untouched. The log
line MUST always fire on `err != nil` — silently swallowing the error is
forbidden because an attacker who can make observer's DB return errors could
otherwise erase their tracks.

There is one orthogonal silent-drop path that this mitigation does not
itself close: the default writer is `noopWriter{}`, so if **the
slave-agent process** (which owns `dispatch.Dispatcher`) forgets to call
`dispatch.SetWriter(...)` at boot, every trace is dropped at the call site
with no error. The exported `dispatch.IsNoopWriter() bool` helper (§2.2) is
provided so the follow-up WT that adds the cmd-side wiring (in
`cmd/slave-agent/main.go`, with a `dispatch.Writer` adapter that forwards
to observer-server over the existing observerclient HTTP channel) can
`log.Fatal` when `SetWriter` was never called. Until that follow-up lands,
slave-side dispatch traces are observed only by in-process tests; the
infrastructure-only delivery of this WT (interface, in-process SQL writer,
schema migration, finalize gate) is fully covered by unit and writer
tests. Tests assert `IsNoopWriter()` returns `true` before `SetWriter` and
`false` after.

**Explicitly NOT in this WT's file domain:** `cmd/slave-agent/main.go`,
`cmd/observer-server/main.go`, and any observerclient changes. The
fail-loud guard is a property the slave-agent cmd MUST adopt when it wires
the writer; this WT exports the helper that makes the guard a one-liner.

### (e) Fully parameterized SQL

Every `Write` statement uses `?` placeholders. No `fmt.Sprintf` /
`strings.Replace` on user-influenced fields. The writer is the only path that
inserts into `route_reasons`.

### (f) `DecisionID` is derived, never accepted

`NewDecision(conversationID)` is the only way to produce a `RouteDecision`
with a non-zero `DecisionID`. There is no setter and no field-tag accepting a
caller-provided id. The seed is `(conversationID, time.Now(), atomic-counter
nonce)` — all three unexported and immutable post-construction. Result: a
caller cannot forge a duplicate id to overwrite an existing trace, and two
genuine `NewDecision` calls cannot collide (even at nanosecond resolution)
because the nonce is monotonic and process-unique.

## 7. Acceptance

* Unit test: driver picks one of two candidate executors; `RouteDecision`
  exposes both candidates, the winner with `Reason = capability_match` and the
  loser with `Reason = no_capability_match` (or, when the relevant ablation is
  flipped, `load_too_high`).
* `Dispatcher.Run` signature byte-identical before/after this change.
* `go test ./internal/dispatch/... ./internal/observerstore/... -race -shuffle=on`
  passes.
* `go vet ./...` and `gofmt -l` clean.

## 8. Open questions

None at spec time. Implementation notes (env-driven knob for sample rate,
multi-writer fan-out, OpenTelemetry export) are deferred — YAGNI for P1.
