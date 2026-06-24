# Bridge vs backend-native session ID disambiguation — design

**Date:** 2026-06-24
**Issue:** [#29 — Clarify bridge session IDs vs backend-native conversation IDs](https://github.com/agentserver/loom/issues/29)
**Branch / PR:** TBD (worktree `worktree-bridge-vs-backend-session-id`)

## Problem

The code currently uses the generic name `session_id` for two different concepts, and treats both as bare `string`. The compiler cannot tell them apart, and at least one production code path mixes them (the `resume_task` fallback).

### The two concepts

1. **Bridge session id** — agentserver's task bridge session, shaped `cse_<uuid>`.
   - Produced by `agentsdk.DelegateTaskResponse.SessionID` and `agentsdk.TaskInfo.SessionID`.
   - Used by agentserver for task SSE / event aggregation / proxy.
   - **Not a backend conversation id.** Codex/Claude/opencode have no way to resume against it.
2. **Backend-native session id** — the conversation id the agent CLI uses to persist + resume.
   - Codex: `thread_id` from `thread.started`.
   - Claude: session uuid.
   - Opencode: session id.
   - Exposed as `agentbackend.Session.ID` and `executor.Result.SessionID`.
   - The id that `Backend.RunResume` / `chat_resume` requires.
   - In cross-daemon flows it is what gets stamped into the loom-meta sidecar (`parent_session_id`) and the `loom_origin` marker (`session` field).

### The concrete bug

`internal/driver/tools.go::resumeTaskTool.Call` (lines ~1283-1290 today) extracts the marker session id from the slave's prior chat output, then falls back to `info.SessionID` if the marker is absent:

```go
sessionID := firstNonEmpty(kw.SessionID, info.SessionID)
```

`kw.SessionID` is the backend-native id (slave's codex thread id from its kind marker). `info.SessionID` is the bridge id (`cse_…`). When the slave's response is missing or malformed and the fallback fires, the driver delegates `chat_resume` with a bridge id, which the slave's codex backend cannot match → resume fails or attaches to the wrong session.

Two more places use the same `firstNonEmpty(marker, bridge)` fallback for `session_id` propagation: `wait_task` (~line 781) and `get_task` (~line 900) — though those write the result into a response shape rather than back into a `RunResume` call.

Mirror problem in the journal: `TaskJournal.Record.SessionID` and `.ChildSessionID` are bare `string` typed; nothing in the journal write/read path enforces which kind of id they hold.

## Goal

Replace the bare `string` in the driver/agentbackend layers with a typed Go value (`SessionRef`) that:

- distinguishes backend-native id from bridge id at the type level;
- carries the backend `Kind` and the owning `AgentID`, so downstream consumers can reason about origin without re-deriving from siblings;
- serializes back to the **existing** wire shapes used by loom-meta sidecars, kind markers, agentsdk JSON, driver journal — no wire-format breakage, no schema bump;
- makes the `resume_task` fallback impossible to misuse: `RunResume` is called with `ref.Backend` and that's it; there is no `firstNonEmpty(marker, bridge)` available to write because `Bridge` and `Backend` are different struct fields.

The refactor is split into two PRs, P1 and P2 (defined below). P1 alone fixes the observed bug in `resume_task`; P2 promotes the type up into the `Backend` interface so future contributors can't reintroduce the same class of bug.

## Non-goals

- Renaming wire-format fields (`session_id` in markers, `session` in `loom_origin`, JSON keys in agentsdk, `session_id` / `child_session_id` in driver journal). Preserves compatibility with deployed sidecars, in-flight slave processes, and replayed journal files.
- Refactoring `commanderhub` / `internal/commander` / `orchestration` / `observerstore`. Their `SessionID` fields refer to commander UI sessions, not the bridge↔backend distinction.
- Changing agentserver's `agentsdk` SDK. We do not push the type into the SDK; we wrap SDK return values at the driver boundary.
- Renaming or restructuring `agentbackend.Session` struct itself. `.ID` keeps its meaning ("backend-native session id"); only callers learn to wrap it.

## Global constraints

- **Wire format additive-only, with one documented exception.** No existing JSON / YAML / file-format field is renamed, retyped, or removed by this refactor. The `session_id` / `session` / `child_session_id` field names in markers / sidecars keep their meaning unchanged.
  - **Driver journal `session_id`** has historically carried the bridge id (see "Legacy journal data" below). P1 changes its semantics: from this refactor onward `session_id` is the **backend-native** id; the bridge id moves to a new optional `bridge_session_id` sibling field. Legacy rows are read with a per-row classifier (see "Legacy journal data").
  - **`submit_task` response** today returns `"session_id": resp.SessionID` which is the **bridge** id (backend id is not known yet at dispatch time). P1 keeps `submit_task.session_id` populated with the bridge id for backward compatibility AND adds a new sibling `bridge_session_id` field with the same value — so consumers can opt into the new explicit name on their own schedule. `submit_task.session_id` is the single documented exception to the "session_id always means backend" rule, and the `bridge_session_id` field is the migration path.
  - **`wait_task` / `get_task` response** today returns `session_id` as `firstNonEmpty(markerSessionID, info.SessionID)` — meaning backend when the marker resolved, bridge when it didn't. P1 changes this to always be the backend id when known (empty otherwise), and emits `bridge_session_id` as a separate sibling.
  - New `bridge_session_id` / `child_bridge_session_id` fields are added with `omitempty` and only to driver-owned writers (TaskJournal rows; `submit_task` / `wait_task` / `get_task` response bodies).
- **No new wire fields added to formats we don't own.** loom-meta sidecar (slave codex_home, read by slave/Commander), `loom_origin` marker (driver → slave system context), kind marker (slave executor → driver output) — **none** of these gain a `bridge_session_id` field. They only ever needed backend ids and continue to write only `session_id` / `session`.
- **Read backward compatibility.** SessionRef.UnmarshalJSON must accept the old single-field shape (`{"session_id": "<id>"}`) and route the id into `Backend`, so loading a sidecar / journal row written by an older binary continues to work.
- **Bridge field never carries a backend id, and vice versa.** Construction sites enforce this: when wrapping an agentsdk response, set `Bridge` only; when reading from a kind marker / loom_origin / slave executor result, set `Backend` only. A SessionRef may have both set when the driver has paired them (e.g. journal records of a delegation it issued).
- **`Backend` is required for any backend-facing operation.** `RunResume`, `chat_resume` delegation, sidecar writes, and cross-daemon nesting all require `Backend != ""`. If only `Bridge` is known, the caller must error rather than guess.
- **Two-PR landing.** P1 lands without changing the `Backend` interface. P2 lands the interface change in a separate PR so each is reviewable on its own.
- **Tests stay green at every commit.** No "skip tests until P2" markers — the in-PR test suite plus the existing CI workflow must pass on P1 alone and on P2 alone.

## Design

### The `SessionRef` type

Lives in `pkg/agentbackend/sessionref.go` (new file in same package as `Session`, `Result`, `Kind`).

```go
// SessionRef is a typed reference to an agent conversation. It distinguishes
// the backend-native session id (what the CLI actually persists and resumes
// against) from the agentserver task bridge id (what agentserver uses for
// task SSE/proxy). The compiler enforces the distinction at use sites that
// previously took bare strings.
//
// Backend MUST be set for any operation that reaches a backend (RunResume,
// chat_resume delegation, sidecar writes, cross-daemon nesting). Bridge is
// optional and only meaningful inside the driver/agentsdk seam.
//
// SessionRef does NOT carry its own JSON marshaler. JSON I/O happens at the
// containing-struct level (TaskJournal.Record, response builders), which
// flatten Backend → "session_id" and Bridge → "bridge_session_id" into the
// parent JSON object using explicit fields rather than a nested object.
// See the journal / response sections below.
type SessionRef struct {
    Backend string // backend-native conversation id (codex thread uuid, claude session uuid, opencode session id)
    Bridge  string // agentserver task bridge id (cse_<uuid>); optional; non-empty only when wrapped from agentsdk
    Kind    Kind   // codex / claude / opencode; matches the backend kind that owns Backend
    AgentID string // sandbox short_id of the agent that holds this session
}

// IsZero reports whether the ref carries no usable id.
func (r SessionRef) IsZero() bool { return r.Backend == "" && r.Bridge == "" }

// HasBackend reports whether Backend is set (the field required for resume/nesting).
func (r SessionRef) HasBackend() bool { return r.Backend != "" }

// String renders a compact, log-safe representation. Backend takes priority; bridge is parenthesized.
func (r SessionRef) String() string
```

**Why no `Marshal/UnmarshalJSON` on SessionRef itself:** a custom marshaler on the inner type would produce nested JSON like `"session_id":{"session_id":"thr-1","bridge_session_id":"cse-2"}` when used as a field of a parent struct tagged `json:"session_id"`. Go's encoding/json does not flatten nested objects into sibling fields; only `json:"...,inline"` (struct embedding) does that, and embedding would conflict with the existing `session_id` field name on the parent. The clean alternative — which this spec adopts — is for **the containing struct's marshal/unmarshal** to explicitly map `r.Backend → "session_id"` and `r.Bridge → "bridge_session_id"` as two sibling fields. The `TaskJournal.Record` and response-builder sections below specify these explicitly.

#### Wire-format mapping (read + write)

| Source | Wire field(s) | SessionRef field(s) | Mapping owner |
|---|---|---|---|
| `kind marker` (slave executor → driver) | `session_id` (string) | `Backend` | inline JSON parser in `internal/driver/tools.go` |
| `loom_origin` marker (driver → slave) | `session` (string) | `Backend` | `pkg/agentbackend/loomorigin.go` BuildLoomOrigin |
| `loom-meta` sidecar (slave codex_home) | `session_id` (string) | `Backend` | `pkg/agentbackend/codex/loommeta.go` (unchanged — slave-side, bare string fine) |
| `agentsdk.TaskInfo` | `session_id` (string, always `cse_…`) | `Bridge` | wrap with `NewBridgeOnly(...)` at SDK boundary |
| `agentsdk.DelegateTaskResponse` | `session_id` (string, always `cse_…`) | `Bridge` | wrap with `NewBridgeOnly(...)` at SDK boundary |
| driver `TaskJournal.Record` (new rows, write) | `session_id` (Backend, omitempty), `bridge_session_id` (Bridge, omitempty), `child_session_id` (ChildBackend), `child_bridge_session_id` (ChildBridge) | both fields populated as two sibling JSON keys | `internal/driver/task_journal.go` MarshalJSON on `Record` |
| driver `TaskJournal.Record` (legacy rows, read) | `session_id` (string only) | classifier (see below) | `internal/driver/task_journal.go` UnmarshalJSON on `Record` |
| `submit_task` response | `session_id` (Bridge — back-compat), `bridge_session_id` (Bridge — new explicit) | both populated with the same SDK bridge id; no Backend at dispatch time | `internal/driver/tools.go` submitTaskTool.Call response |
| `wait_task` / `get_task` response | `session_id` (Backend, may be empty), `bridge_session_id` (Bridge, may be empty) | both fields, explicit | `internal/driver/tools.go` response builders |

**Per-field marshal/unmarshal happens on the containing struct**, not on `SessionRef`. Concrete shape for `TaskJournal.Record`:

```go
type Record struct {
    TS                  string `json:"ts"`
    Event               string `json:"event"`
    Tool                string `json:"tool"`
    // … other unchanged fields …
    SessionRef          SessionRef `json:"-"`        // Go field; NOT directly serialized
    ChildSessionRef     SessionRef `json:"-"`        // Go field; NOT directly serialized
}

// MarshalJSON flattens SessionRef.Backend + SessionRef.Bridge as siblings.
func (r Record) MarshalJSON() ([]byte, error) {
    type rawRecord struct {
        TS                       string `json:"ts"`
        Event                    string `json:"event"`
        // … other unchanged fields …
        SessionID                string `json:"session_id,omitempty"`
        BridgeSessionID          string `json:"bridge_session_id,omitempty"`
        ChildSessionID           string `json:"child_session_id,omitempty"`
        ChildBridgeSessionID     string `json:"child_bridge_session_id,omitempty"`
    }
    return json.Marshal(rawRecord{
        TS: r.TS, Event: r.Event, /* ... */,
        SessionID:            r.SessionRef.Backend,
        BridgeSessionID:      r.SessionRef.Bridge,
        ChildSessionID:       r.ChildSessionRef.Backend,
        ChildBridgeSessionID: r.ChildSessionRef.Bridge,
    })
}

// UnmarshalJSON decodes the flat shape and reconstructs SessionRef values
// using the legacy classifier described below.
func (r *Record) UnmarshalJSON(data []byte) error { /* mirror */ }
```

**Legacy journal data — bridge id classifier.** Today's `internal/driver/tools.go:159` writes `delegatedTaskRecord.Response.SessionID` into `TaskRecord.SessionID`, and `Response.SessionID` is always the agentserver bridge id (`cse_…`). So pre-refactor journal rows have **`cse_…` strings stored under `session_id`**. The unmarshal path MUST NOT shove these into `Backend` (would violate the bridge/backend invariant). Decision: legacy rows whose `session_id` matches `^cse_` (case-sensitive prefix used by agentserver SDK) are classified as bridge and routed to `Bridge`; rows whose `session_id` does NOT start with `cse_` are routed to `Backend`. Same rule for `child_session_id` → `ChildSessionRef`. Modern (post-refactor) rows carry both `session_id` and `bridge_session_id`, and the classifier is bypassed — fields go to their respective targets.

This makes a one-time read-time correction: a deployment that runs the new binary against an old journal will see its old `cse_…` entries as Bridge-only refs (correct), and the next write through that record will start populating both fields.

**No new wire fields anywhere we don't own.** Per the global constraint, `loom-meta` sidecar / `loom_origin` marker / `kind marker` continue writing only their existing single field; they always meant backend-native.

#### Constructors

```go
// NewBackend builds a ref from a known backend-native id (kind marker,
// loom_origin marker, executor.Result, sidecar read).
// Panics if backendID is empty (caller bug — checked at construction time).
func NewBackend(kind Kind, agentID, backendID string) SessionRef

// NewBridgeOnly wraps an agentsdk response that has only the bridge id.
// Used at the driver↔agentsdk seam; downstream code that needs Backend
// must error if it sees !HasBackend().
// Panics if bridgeID is empty (caller bug).
func NewBridgeOnly(kind Kind, agentID, bridgeID string) SessionRef

// WithBackend returns a copy of r with Backend filled. The only legitimate
// pairing path: take a bridge-only ref returned by NewBridgeOnly, look up
// the backend id (e.g. from the slave's kind marker), and pair them.
// Preconditions (panic if violated — caller bug):
//   - backendID != ""               // can't pair to an empty id
//   - r.Bridge != ""                // only meaningful on a bridge-only base
//   - r.Backend == ""               // refuse to overwrite an existing pairing
// Kind and AgentID are inherited from r unchanged. Pairing across different
// agents would itself be a bug; the caller is responsible for verifying
// r.AgentID matches the agent that produced backendID.
func (r SessionRef) WithBackend(backendID string) SessionRef
```

There is intentionally **no** `NewBoth` constructor that takes both at once — pairing happens via `WithBackend` on an existing bridge-only ref, so the code path that does the pairing is also the one that has to look up the backend id.

### PR 1 — types + driver boundary (fixes #29 bug)

Changes:

1. **`pkg/agentbackend/sessionref.go`** — new file, the type + constructors + JSON I/O + tests.
2. **`pkg/agentbackend/sessionref_test.go`** — new file, table-driven tests for IsZero/HasBackend/String/Marshal/Unmarshal/round-trip/legacy-read.
3. **`internal/driver/tools.go`** — every line touching `SessionID` / `session_id` / `ChildSessionID` migrates to `SessionRef`. Concretely:
   - `delegatedTaskRecord.Response` was used for `Response.SessionID` (bridge); wrap into `NewBridgeOnly(kind, agentShortID, resp.SessionID)` at the agentsdk seam, stash as `SessionRef` on the record.
   - The two response-builder paths in `wait_task` / `get_task` that today do `firstNonEmpty(sessionIDFromMarker(...), info.SessionID)`: split — extract `markerBackendID = sessionIDFromMarker(...)` and `bridgeID = info.SessionID` separately, build a `SessionRef{Backend: markerBackendID, Bridge: bridgeID, ...}`, then emit both as sibling JSON fields. **Wire change**: `session_id` becomes empty (rather than fall back to bridge) when the marker was absent; `bridge_session_id` is the new explicit sibling. The "Wire format additive-only" constraint above documents this exception and the migration path for consumers.
   - **`submit_task` response**: today emits `"session_id": resp.SessionID` (bridge). P1 keeps this for back-compat (any current consumer reading it gets the same value) AND adds `"bridge_session_id": resp.SessionID` (same value, explicit name). No Backend is known at dispatch time. This is the documented `submit_task.session_id` exception.
   - `resume_task` reads the prior task via `g.t.sdk.GetTask(...)`, extracts the slave's kind-marker session id (`kw.SessionID`), then today falls back to `info.SessionID`. Rewrite:
     ```go
     // Old:
     // sessionID := firstNonEmpty(kw.SessionID, info.SessionID)
     // New:
     if kw.SessionID == "" {
         return nil, &MCPToolError{Message: "resume failed: slave never reported a backend session id; bridge id alone cannot resume a codex/claude conversation"}
     }
     ref := agentbackend.NewBackend(kind, slaveShortID, kw.SessionID)
     // Pass ref.Backend to RunResume (P1 still unwraps); never touch info.SessionID for resume.
     ```
   - `sessionIDFromMarker` helper stays (parses kind marker JSON), but its return value is always treated as a backend id.
4. **`internal/driver/task_journal.go`** — `Record` struct fields `SessionID` / `ChildSessionID` change Go type from `string` to `SessionRef` (unexported JSON via `json:"-"`). Add custom `MarshalJSON` / `UnmarshalJSON` on `Record` that:
   - On write, flattens `r.SessionRef.Backend` → `session_id`, `r.SessionRef.Bridge` → `bridge_session_id` (and similarly for `ChildSessionRef`), with `omitempty`.
   - On read, uses the legacy bridge-id classifier: if `bridge_session_id` field is present, treat as modern row (Backend / Bridge each load from their explicit fields); if only `session_id` is present, classify by prefix — `^cse_` → `Bridge`, else → `Backend`.
5. **`internal/driver/tools_test.go`** — adjust tests that construct fake responses to use the constructors; rebuild fixtures that previously asserted on `info.SessionID` fallback semantics (those fixtures encode the bug and need updating).
6. **`internal/driver/task_journal_test.go`** — new tests for the journal:
   - `TestRecord_MarshalFlattensSessionRefIntoSiblings` proves the marshal produces two top-level JSON fields, NOT a nested object.
   - `TestRecord_UnmarshalLegacyBridgeSessionID` writes a row with only `"session_id":"cse_legacy"` (no `bridge_session_id`), reads it back, asserts `r.SessionRef.Bridge == "cse_legacy"` and `r.SessionRef.Backend == ""`.
   - `TestRecord_UnmarshalLegacyBackendSessionID` writes a row with only `"session_id":"019ef…"` (uuid shape), reads, asserts `r.SessionRef.Backend == "019ef…"` and `Bridge == ""`.
   - `TestRecord_RoundTripModernRow` writes a Record with both fields, reads, asserts equal.
7. **Test for the actual #29 bug**: new test `TestResumeTask_RefusesEmptyMarker` that constructs a `TaskInfo` where the slave kind marker is missing and `info.SessionID = "cse_fake_bridge"`. Pre-PR this would have called `chat_resume` with `"cse_fake_bridge"`. Post-PR it returns the actionable error from the rewrite above. (This is the proof-of-fix for the spec.)

What does NOT change in P1:
- `Backend.RunResume(ctx, sessionID string, …)` signature — driver still unwraps `ref.Backend` and passes the string.
- `pkg/agentbackend/codex/*`, `claude/*`, `opencode/*` — completely untouched.
- `pkg/agentbackend/loomorigin.go` — `ParentLink.SessionID` (the `session` field in the marker) stays bare `string`. Driver construction of the marker writes `ref.Backend` into it.
- `pkg/agentbackend/codex/loommeta.go` — sidecar struct stays bare `string`.
- `internal/executor/chat_resume.go` ResumeBackend interface — stays bare `string` (will move in P2 along with its single implementor).
- `internal/commander/handler.go` RunResume call sites — stay bare `string` (will move in P2).
- `cmd/slave-agent/main.go` backendExecutor.RunResume adapter — stays bare `string` (will move in P2).

### PR 2 — promote `SessionRef` into `Backend.RunResume`

**Full call-site inventory** (verified via `git grep -nE '\.RunResume\('`):

| Site | Role | Change |
|---|---|---|
| `pkg/agentbackend/backend.go` | the `Backend` interface | signature change |
| `pkg/agentbackend/backend_test.go` `nilBackend` | stub | signature change |
| `pkg/agentbackend/codex/executor.go` `*Executor.RunResume` | impl | signature change; body reads `ref.Backend` |
| `pkg/agentbackend/codex/backend.go` `executorForWorkDir(...).RunResume(...)` | thin pass-through wrapper | signature change |
| `pkg/agentbackend/codex/appserver_worker.go` | app-server fallback caller | wrap as `SessionRef` before pass-through |
| `pkg/agentbackend/codex/appserver_worker_test.go` | tests asserting `RunResume(id, ...)` was called | change assertions to `ref.Backend == ...` |
| `pkg/agentbackend/claude/executor.go` `*Executor.RunResume` | impl | signature change |
| `pkg/agentbackend/claude/backend.go` `executorForWorkDir(...).RunResume(...)` | wrapper | signature change |
| `pkg/agentbackend/opencode/executor.go` `*Executor.RunResume` | impl | signature change |
| `pkg/agentbackend/opencode/backend.go` `executorForWorkDir(...).RunResume(...)` | wrapper | signature change |
| `internal/executor/chat_resume.go` `ResumeBackend` interface + caller | local interface alias of Backend.RunResume | **must move alongside** — interface signature changes from `(ctx, string, ...)` to `(ctx, SessionRef, ...)`; ChatResumeExecutor caller wraps `body.SessionID` (which is the slave's marker session id — backend-native) into `NewBackend(...)` before calling |
| `internal/executor/chat_resume_test.go` `fakeResumeBackend` stub | test stub | signature change |
| `internal/commander/handler.go` two call sites (lines 61 + 65) | commander session-turn handler fallback | wrap bare string `id` into `NewBackend(...)` before call. The commander handler's `Backend.RunResume(ctx, id, prompt, sink)` calls use an `id` value originating from the commander session id (which the surrounding code treats as a backend-native id; it's the session id of the agent's prior conversation, not a bridge id). Document this in the call site with a comment so future readers know why this is a `NewBackend` not a `NewBridgeOnly`. |
| `cmd/slave-agent/main.go` `backendExecutor.RunResume(ctx, sid, ans, s)` | the slave-side adapter implementing `executor.ResumeBackend` | signature change to match the new `ResumeBackend` interface above. Body wraps `sid` (the slave's own backend session id) into `NewBackend(...)` before calling the underlying `agentbackend.Backend.RunResume`. |
| `internal/driver/tools.go::resumeTaskTool.Call` | the driver caller | stops unwrapping `ref.Backend` to string; passes `ref` directly. |

Total: **13 files** touched. The `internal/executor` + `internal/commander` + `cmd/slave-agent` inclusions are the corrections from the codex round-1 review — without them the build does not compile.

**No import-cycle risk** — `pkg/agentbackend` is already imported by `internal/executor`, `internal/commander`, `cmd/slave-agent`, and `internal/driver`. Adding `SessionRef` to the existing imported package does not introduce a new cycle.

**Why these stay bare `string` in P1 but move in P2:** they implement or call the `Backend.RunResume` interface, so once that interface signature changes, ALL implementors and callers MUST change in the same PR — Go interface implementation is by signature match, not nominal. We can't migrate them piecemeal. This is exactly the "everything in P2, all at once" property that justifies splitting from P1.

What does NOT change in P2:
- Wire format. Still none.
- `Session`, `Result` struct shapes.
- Loom-origin marker / loommeta sidecar / kind marker / driver journal JSON.

### Why this split

- P1 alone fixes the observed bug (`resume_task` cannot fall back to bridge id), which is the operationally important outcome.
- P1's change is contained inside `internal/driver` + one new file in `pkg/agentbackend`. Reviewers don't need to evaluate the three backend implementations.
- P2 is mechanical signature propagation. Easy to review independently; if it has a bug, only `RunResume` paths are affected, not the resume-path-shape bug from P1.
- If P2 stalls in review, P1's bug fix still ships on time.

### Observable behavior change

Only one operationally observable behavior changes:

- **`resume_task` with a malformed/missing slave marker now errors** with `"resume failed: slave never reported a backend session id; …"` instead of silently delegating `chat_resume` against a bridge id (which would then fail later on the slave side with a less actionable error). This is intentional — it's the bug-fix half of the issue.

All other paths are equivalent in observable behavior: same wire output, same JSON shapes, same backend invocations.

### Testing

| Layer | Tests added/changed |
|---|---|
| `pkg/agentbackend/sessionref_test.go` (new) | IsZero, HasBackend, String formatting, Marshal+Unmarshal round-trip, Unmarshal of legacy single-field JSON, Unmarshal of full new shape, constructors set fields correctly. |
| `internal/driver/tools_test.go` | Existing TaskInfo-construction fixtures updated; new `TestResumeTask_RefusesBridgeOnlyFallback`; new `TestWaitTask_BridgeAndBackendBothInResponse` that asserts the new `bridge_session_id` field appears alongside the existing `session_id` field. |
| `internal/driver/task_journal_test.go` | New `TestJournal_BackwardCompatReadLegacyRow` that writes a row with only `session_id` (no `bridge_session_id`) and verifies `SessionRef.UnmarshalJSON` puts it into Backend. New `TestJournal_RoundTripSessionRef` that writes a SessionRef with both fields and reads it back. |
| `pkg/agentbackend/backend_test.go` (P2) | `nilBackend.RunResume` signature update. |
| `pkg/agentbackend/codex/appserver_worker_test.go` (P2) | The two `RunResume` assertion sites: `if id, answer := …, …; id != …` becomes `if ref.Backend, answer := …, …; ref.Backend != …`. |
| E2E | No new e2e required. The prod_test stack exercises the resume path; PR runs go test ./... and exits green is sufficient. |

### Migration / rollout

- P1 and P2 are master-bound commits via PRs against the `master` branch. No feature flag, no kill switch.
- **Wire-format backward-read compatibility means:** rolling out P1 to production, then later P2, then later possibly downgrading to a pre-P1 binary, never produces an unreadable sidecar / journal / API response. New `bridge_session_id` fields are ignored by old readers; legacy `session_id`-only rows are correctly classified by P1's bridge-id classifier.
- **PR ordering:** P2 depends on P1 having merged first. P1 introduces the `SessionRef` type that P2's interface signature change consumes. So the merge order is **strictly P1 → P2**; P2 cannot be reviewed or built standalone. The two PRs can be opened in parallel for early review, but P2 must rebase on `master` after P1 lands before it can pass CI.

## Open questions

None at design time. If something surfaces during plan or implementation, escalate to the human.

## Review history

### Codex round 1 (2026-06-24)

Four P1 (blocking) + two P3 findings from `codex exec resume 019ef428…` against commit `212fb92`. Resolutions inline above; key changes:

- **P1 (Go JSON nesting):** `SessionRef.MarshalJSON` removed from the design. Containing structs (`TaskJournal.Record`, response builders) own the flatten — explicit `session_id` + `bridge_session_id` sibling fields, not nested.
- **P1 (legacy journal carries bridge ids):** Added a `^cse_` prefix classifier for legacy journal rows so old `cse_…` strings end up in `Bridge`, not `Backend`. Specified explicit tests for both legacy shapes.
- **P1 (submit_task semantics):** Documented `submit_task.session_id` as a one-line exception to the additive-only rule. `session_id` keeps the bridge id for back-compat; new `bridge_session_id` sibling carries the same value with the explicit name.
- **P1 (P2 RunResume call sites):** Expanded the P2 call-site inventory from 8 sites to 13. Added `internal/executor/chat_resume.go`, `internal/commander/handler.go`, `cmd/slave-agent/main.go`. Explained why these must move with the interface signature change.
- **P3 (WithBackend preconditions):** Added explicit panic preconditions (`backendID != ""`, `r.Bridge != ""`, `r.Backend == ""`).
- **P3 (rollout topology):** Clarified that PR merge order is **strictly P1 → P2**.

## Out of scope (deliberately tracked here so the next refactor doesn't re-litigate)

- **Renaming wire-format fields** to e.g. `backend_session_id` for clarity. Considered and rejected because it would force a coordinated migration with all deployed slaves' loom-meta sidecars and with agentserver SDK consumers. The new typed Go layer is sufficient to enforce the discipline going forward.
- **Pushing `SessionRef` into `agentsdk`.** Requires an agentsdk SDK version bump, which couples agentserver and multi-agent release cycles. We wrap SDK return values at the driver boundary instead.
- **A `SessionResolver` service layer** that hides RunResume entirely. Heavier; not justified by the bug we're fixing.
