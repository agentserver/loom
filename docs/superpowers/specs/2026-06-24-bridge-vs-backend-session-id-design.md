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
  - **`submit_task` response** today returns `"session_id": resp.SessionID` which is the **bridge** id (backend id is not known synchronously at dispatch time). P1 keeps `submit_task.session_id` populated with the bridge id **permanently** for backward compatibility AND adds a new sibling `bridge_session_id` field with the same value. `submit_task.session_id` is the single permanent exception to "session_id always means backend"; see "submit_task contract" in "Observable behavior change" below for the full rationale and why this is not a planned migration.
  - **`wait_task` / `get_task` response** today returns `session_id` as `firstNonEmpty(markerSessionID, info.SessionID)` — meaning backend when the marker resolved, bridge when it didn't. P1 changes this to always be the backend id when known (empty otherwise), and emits `bridge_session_id` as a separate sibling.
  - New `bridge_session_id` / `child_bridge_session_id` fields are added with `omitempty` and only to driver-owned writers (TaskJournal rows; `submit_task` / `wait_task` / `get_task` response bodies).
- **No new wire fields added to formats we don't own.** loom-meta sidecar (slave codex_home, read by slave/Commander), `loom_origin` marker (driver → slave system context), kind marker (slave executor → driver output) — **none** of these gain a `bridge_session_id` field. They only ever needed backend ids and continue to write only `session_id` / `session`.
- **Read backward compatibility.** Read paths that produce a `SessionRef` from on-disk JSON (only `TaskJournal.Record.UnmarshalJSON` after this refactor — `SessionRef` itself has no JSON methods) must accept the old single-field shape and use the bridge-id prefix classifier (see "Legacy journal data" below) to route legacy values to `Backend` or `Bridge`. Loom-meta sidecars, kind markers, and `loom_origin` markers always meant backend-native and continue to load directly into `Backend` without classifier logic.
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
    ChildAgentID        string     `json:"child_agent_id,omitempty"`  // already present today
    SessionRef          SessionRef `json:"-"`                         // Go field; NOT directly serialized
    ChildSessionRef     SessionRef `json:"-"`                         // Go field; NOT directly serialized
}

// MarshalJSON flattens SessionRef.Backend + SessionRef.Bridge as siblings
// and continues to write ChildAgentID as a top-level field the way it
// exists today. No new ChildKind field — see "Driver-side SessionRef has
// empty Kind" above.
func (r Record) MarshalJSON() ([]byte, error) {
    type rawRecord struct {
        TS                       string `json:"ts"`
        Event                    string `json:"event"`
        // … other unchanged fields …
        ChildAgentID             string `json:"child_agent_id,omitempty"`
        SessionID                string `json:"session_id,omitempty"`
        BridgeSessionID          string `json:"bridge_session_id,omitempty"`
        ChildSessionID           string `json:"child_session_id,omitempty"`
        ChildBridgeSessionID     string `json:"child_bridge_session_id,omitempty"`
    }
    return json.Marshal(rawRecord{
        TS: r.TS, Event: r.Event, /* ... */,
        ChildAgentID:         r.ChildAgentID,
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

**Journal stays unchanged in wire-shape.** The journal continues to carry exactly the same set of fields it does today — no new `child_kind`. On `UnmarshalJSON`, `SessionRef.Kind` is left empty (driver-side refs never had a Kind source). `SessionRef.AgentID` is reconstructed from the existing `child_agent_id` field for child refs; for the parent's own SessionRef there is no AgentID source in the journal either, and it stays empty too. This is consistent with the round-5 finding that driver-side SessionRef instances legitimately have empty Kind.

Round-trip test `TestRecord_RoundTripModernRow` asserts `Backend`, `Bridge`, and `AgentID` (for child ref) equal across the boundary. `Kind` is asserted empty (matches what the driver actually populates).

**Legacy journal data — bridge id classifier.** Today's `internal/driver/tools.go:159` writes `delegatedTaskRecord.Response.SessionID` into `TaskRecord.SessionID`, and `Response.SessionID` is always the agentserver bridge id (`cse_…`). So pre-refactor journal rows have **`cse_…` strings stored under `session_id`**. The unmarshal path MUST NOT shove these into `Backend` (would violate the bridge/backend invariant). Decision: legacy rows whose `session_id` matches `^cse_` (case-sensitive prefix used by agentserver SDK) are classified as bridge and routed to `Bridge`; rows whose `session_id` does NOT start with `cse_` are routed to `Backend`. Same rule for `child_session_id` → `ChildSessionRef`. Modern (post-refactor) rows carry both `session_id` and `bridge_session_id`, and the classifier is bypassed — fields go to their respective targets.

This makes a one-time read-time correction: a deployment that runs the new binary against an old journal will see its old `cse_…` entries as Bridge-only refs (correct), and the next write through that record will start populating both fields.

**No new wire fields anywhere we don't own.** Per the global constraint, `loom-meta` sidecar / `loom_origin` marker / `kind marker` continue writing only their existing single field; they always meant backend-native.

#### Constructors

```go
// NewBackend builds a ref from a known backend-native id (kind marker,
// loom_origin marker, executor.Result, sidecar read, slave's own session id).
// Panics if backendID is empty — this is for internal invariant enforcement
// where the caller has already validated the id. For external/user-input
// paths (e.g. commander.Handler.SessionTurn) callers MUST validate first
// and return an error themselves, not catch the panic.
//
// agentID may be empty when no cross-agent identity is needed (e.g. when
// wrapping at a local backend seam where there is exactly one Backend
// instance and the ref's "owning agent" is implicit). All driver-side
// callers MUST populate agentID; only seams below the driver may leave
// it empty (slave's resumeAdapter, internal commander handler).
func NewBackend(kind Kind, agentID, backendID string) SessionRef

// NewBridgeOnly wraps an agentsdk response that has only the bridge id.
// Used at the driver↔agentsdk seam; downstream code that needs Backend
// must error if it sees !HasBackend().
// Panics if bridgeID is empty (caller bug — agentsdk always returns one
// or returns an error before reaching this constructor).
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

**Driver-side `SessionRef` has empty `Kind`.** Verified by reading the code: `agentsdk.AgentCard` has no `Kind` field; driver-side `parsedAgentCard.HasCommandKind(...)` matches command interface kind (`bash`/`powershell`), not backend kind; published discovery card body carries no backend-kind field. The driver does NOT have a generally-available source for slave's backend kind.

Critically, the driver also does NOT call `agentbackend.Backend.RunResume` directly. `resumeTaskTool.Call` calls `r.t.sdk.DelegateTask(... Skill: "chat_resume", ...)`; the agentsdk JSON crosses the wire to the slave; the slave-side `ChatResumeExecutor` calls `resumeAdapter.RunResume(sid string, ...)`. The seam where `Kind` actually matters is **slave-side**, where `cfg.Agent.Kind` is locally known.

So driver-side `SessionRef` instances **leave `Kind` empty**:

- `NewBridgeOnly(...)` at `submitTaskTool.Call` agentsdk seam (P1): pass empty kind (or omit `kind` param entirely — see constructor revision below).
- `NewBackend(...)` at `resumeTaskTool.Call` (P1): pass empty kind.

The slave-side `resumeAdapter.RunResume` wraps with `NewBackend(a.b.Kind(), "", sid)` because the slave's `agentbackend.Backend` interface DOES expose `Kind() Kind` (verified in `pkg/agentbackend/backend.go:36`).

`Kind` is a "best-effort tag" on driver-side refs — informational. The bridge/backend distinction (the bug-prevention goal) is unaffected by empty `Kind`. Tests that pass a driver-side ref to a backend interface for unit-testing purposes can rely on `RunResume` being called only at the slave seam, where `Kind` IS populated.

**Constructor revision (no `kind != ""` precondition).** Because driver-side legitimately constructs refs with empty kind, the round-4 "kind != \"\" panic" precondition is **rolled back**. Constructors accept empty Kind; only `Backend` (for `NewBackend`) and `Bridge` (for `NewBridgeOnly`) remain required. Test mandate at `sessionref_test.go` updated: empty-kind constructors do NOT panic; only empty backendID / empty bridgeID do.

resumeTaskTool.Call pseudo-code:

```go
slaveShortID := ""
if prev, ok := r.t.taskJournal.LatestByTaskID(args.LastTaskID); ok {
    slaveShortID = prev.ChildAgentID
}
if kw.SessionID == "" {
    return nil, &MCPToolError{Message: "resume failed: slave never reported a backend session id; bridge id alone cannot resume a codex/claude conversation"}
}
// Kind left empty — driver does not have a backend-kind source; it's informational only.
// agentsdk JSON body carries kw.SessionID as a string (wire format unchanged).
ref := agentbackend.NewBackend("", slaveShortID, kw.SessionID)
body, _ := json.Marshal(map[string]string{
    "session_id": ref.Backend,  // stays a bare string in wire JSON
    "answer":     args.Answer,
    "kind":       kw.Question.Kind,
})
resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
    TargetID: info.TargetID, Skill: "chat_resume", Prompt: string(body), ...,
})
```

The `ref` is used here for **type-discipline at the construction site** — code that grabbed `kw.SessionID` cannot mistake it for a bridge id, because `NewBackend` only takes the backend slot. The actual wire JSON (sent to agentsdk) is unchanged: a string in `session_id`.

**ChildKind in TaskRecord is rolled back too.** The round-4 `ChildKind agentbackend.Kind` field added to TaskRecord is removed — there is no source to populate it at delegation time. The journal continues to carry only `ChildAgentID` (which is sufficient to reconstruct `SessionRef.AgentID`). On unmarshal, `SessionRef.Kind` stays empty for driver-side refs. The "Journal Kind persistence" subsection below is rewritten accordingly.

**`agentID` source at each P2 wrap site:**

| Site | `kind` source | `agentID` source |
|---|---|---|
| `internal/driver/tools.go` `resumeTaskTool.Call` (P1 site) | **resolved via `s.t.sdk.DiscoverAgents(ctx)` then matched by `card.AgentID == info.TargetID`** — see "resume_task identity resolution" below | `prev.ChildAgentID` from the prior journal record (already recovered today via `taskJournal.LatestByTaskID(args.LastTaskID)` — tools.go:1327-1330) |
| `internal/driver/tools.go` `submitTaskTool.Call` agentsdk seam (P1 site, `NewBridgeOnly`) | `targetCard.Kind()` (the agent card was just resolved via `s.t.resolveTarget`) | `targetShortID` (computed in same block, tools.go:670 already passes it as ChildAgentID into the journal) |
| `internal/driver/tools.go` `waitTaskTool.Call` / `getTaskTool.Call` response builders | from the journal `LatestByTaskID(taskID)` record AND from the slave's kind marker. The journal carries `ChildAgentID`; backend kind comes from a new persisted field — see "Journal Kind persistence" below | from journal `ChildAgentID` |
| `internal/driver/tools.go` `submit_task` agentsdk seam (P1 site, `NewBridgeOnly`) | same | same |
| `cmd/slave-agent/main.go` `resumeAdapter.RunResume` (P2 seam) | `a.b.Kind()` (Backend interface already exposes Kind()) | **empty string** — see note below |
| `internal/commander/handler.go` `SessionTurn` (P2) | `h.Backend.Kind()` | **empty string** — see note below |
| `pkg/agentbackend/*/executor_test.go` (P2 test fixtures) | the kind constant for that package's test (`KindCodex` / `KindClaude` / `KindOpencode`) | empty in tests — fixtures don't model cross-agent identity |

**Why empty `agentID` is OK at slave / commander / executor seams:** the `AgentID` field on `SessionRef` is a **disambiguator across agents**, used when the same `Backend` value type might serve multiple agents (driver does — it routes to N slaves; each `SessionRef` carries which slave's session it is). Slave-side seams have exactly **one** backend instance owned by exactly **one** agent process; there is nothing to disambiguate. Same for `commander.Handler` which is constructed once per agent process and serves only that agent's sessions. Plumbing an `AgentID` through these seams would be ceremonial without changing the type-safety guarantee that `Backend` ≠ `Bridge` (which is the actual bug-prevention goal).

**Validate-then-wrap at user-input seams.** `commander.Handler.SessionTurn` accepts `id string` from a higher-level caller. Before `NewBackend(kind, "", id)`, the handler MUST check `id != ""` and return an error if empty — propagating the constructor panic to a panic-mid-request is wrong. Spec-mandated pseudo-code at both call sites (lines 61 + 65 of `internal/commander/handler.go`):

```go
if id == "" {
    return executor.Result{}, fmt.Errorf("commander: empty session id; cannot resume")
}
return h.Backend.RunResume(ctx, agentbackend.NewBackend(h.Backend.Kind(), "", id), prompt, sink)
```

(`commander.Handler.SessionTurn` already returns `executor.Result`, NOT `result.Result` — that was a typo in an earlier spec draft.)

P1's driver `resume_task` already does this validation (the `if kw.SessionID == ""` check above). The slave's `resumeAdapter.RunResume` body should add a symmetric guard before wrapping.

**RunResume implementations MUST reject bridge-only / zero refs.** Even though P1's typed `SessionRef` makes "pass bridge id to backend" harder, the type alone does not enforce it — a future caller could still pass `NewBridgeOnly(...)` to `RunResume` since the parameter accepts any `SessionRef`. P2's `Backend.RunResume` interface contract therefore REQUIRES every implementation (codex / claude / opencode `Executor.RunResume`, `*.Backend.RunResume` wrappers) to start with:

```go
if !ref.HasBackend() {
    return Result{}, fmt.Errorf("RunResume: SessionRef has no backend id (Bridge=%q); cannot resume backend session", ref.Bridge)
}
```

This guard is mandatory for all 3 backend executor implementations and their thin `*.Backend.RunResume` pass-through wrappers. The error becomes the actionable surface when a future driver-side bug delegates the wrong ref shape; without it, the backends would silently call their CLI with an empty session id.

**`Kind` validity in constructors.** `Kind` is the `agentbackend.Kind` named type (`type Kind string`); its zero value is `""`. Constructor preconditions:

- `NewBackend(kind, agentID, backendID)`: panics if `backendID == ""`. Empty `kind` and `agentID` are ALLOWED (see "Driver-side SessionRef has empty Kind"). Driver-side construction has no kind source and constructs refs with `kind=""` legitimately.
- `NewBridgeOnly(kind, agentID, bridgeID)`: panics if `bridgeID == ""`. Empty `kind` and `agentID` allowed for the same reason.
- `WithBackend(backendID)`: inherits `r.Kind` unchanged; no separate kind validation.

This adds these assertion sites to `sessionref_test.go`: `NewBackend("", "ag", "")` panics (empty backendID); `NewBridgeOnly("", "ag", "")` panics (empty bridgeID); both with kind="" and kind="codex" pass when the required id is non-empty (proves kind is NOT validated).

There is intentionally **no** `NewBoth` constructor that takes both at once — pairing happens via `WithBackend` on an existing bridge-only ref, so the code path that does the pairing is also the one that has to look up the backend id.

### PR 1 — types + driver boundary (fixes #29 bug)

Changes:

1. **`pkg/agentbackend/sessionref.go`** — new file: the type, constructors (`NewBackend`, `NewBridgeOnly`, `WithBackend`), and predicates (`IsZero`, `HasBackend`, `String`). **No JSON methods on SessionRef.** JSON ownership lives on containing structs (`TaskJournal.Record`, response builders) per the rationale above.
2. **`pkg/agentbackend/sessionref_test.go`** — new file: table-driven tests for `IsZero`, `HasBackend`, `String`, and the constructors. **No marshal/unmarshal tests on SessionRef** (no JSON methods to test). Constructor panic-preconditions covered here: `NewBackend(<any kind>, <any agentID>, "")` panics on empty backendID; `NewBridgeOnly(<any kind>, <any agentID>, "")` panics on empty bridgeID; `WithBackend("")` panics; `WithBackend(...)` on a ref with `Backend != ""` panics; `WithBackend(...)` on a ref with `Bridge == ""` panics. Positive coverage: empty `kind` and empty `agentID` are explicitly accepted (driver-side legitimate construction).
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
     // Resolve kind + agent id (see "resume_task identity resolution" below)
     slaveShortID := ""
     if prev, ok := r.t.taskJournal.LatestByTaskID(args.LastTaskID); ok {
         slaveShortID = prev.ChildAgentID
     }
     kind, err := r.t.resolveKindByAgentID(ctx, info.TargetID)
     if err != nil {
         return nil, &MCPToolError{Message: "resume failed: cannot resolve agent kind: " + err.Error()}
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
- `internal/executor/chat_resume.go` `ResumeBackend` interface — **permanently** stays bare `string` to avoid the import cycle `agentbackend → executor → agentbackend` (see P2 "Why ResumeBackend stays string" below). Its single implementor in `cmd/slave-agent` is the wrap seam.
- `internal/commander/handler.go` `Backend.RunResume` call sites — stay bare `string` in P1; in P2 the **call sites** wrap into `SessionRef` (validate `id != ""` first, then `NewBackend(h.Backend.Kind(), "", id)`), but no interface in the commander package itself changes signature.
- `cmd/slave-agent/main.go` `resumeAdapter.RunResume` (NOT `backendExecutor` — the correct type is `resumeAdapter struct{ b agentbackend.Backend }` at line ~466) — its signature stays string in both P1 and P2 (it implements `executor.ResumeBackend`). In P2 its body changes to wrap the string into a SessionRef before calling `a.b.RunResume(ref, ...)`.

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
| `internal/executor/chat_resume.go` `ResumeBackend` interface + caller | local interface alias of Backend.RunResume | **STAYS bare `string`** — see "Why ResumeBackend stays string" below |
| `internal/executor/chat_resume_test.go` `fakeResumeBackend` stub | test stub | **STAYS bare `string`** (same reason) |
| `internal/commander/handler.go` two call sites (lines 61 + 65) | commander session-turn handler fallback | wrap bare string `id` into `NewBackend(h.Backend.Kind(), "", id)` before call. The `id` value originates from the commander session id (which the surrounding code treats as a backend-native id — the session id of the agent's prior conversation, not a bridge id). Validate `id != ""` first and return an error if empty (NewBackend would otherwise panic, which is wrong for a user-input seam). Empty `AgentID` is intentional — Handler serves only its own agent's sessions, no cross-agent disambiguation needed. Document in the call-site comment so future readers know why this is `NewBackend` not `NewBridgeOnly`, and why AgentID is empty. |
| `internal/commander/handler_test.go` | test stubs / assertions | wrap test ids with `NewBackend(...)`; assertions on `RunResume(ctx, "id", ...)` become `RunResume(ctx, ref, ...)` with `ref.Backend == "id"` |
| `internal/commanderhub/proxy_test.go` | test stub for fake Backend | signature change on the stub |
| `cmd/slave-agent/main.go` `resumeAdapter.RunResume(ctx, sid, ans, s)` (correct type name; not `backendExecutor` which is the *task* executor adapter) | the slave-side adapter implementing `executor.ResumeBackend` (string signature) AND forwarding into `agentbackend.Backend.RunResume` (new SessionRef signature) | the adapter is **the seam**: takes a string `sid` from `executor.ResumeBackend`, wraps as `NewBackend(a.b.Kind(), "", sid)` (empty AgentID — see "agentID source" table) before calling the agentbackend layer. Body MUST first validate `sid != ""` and return an error if empty, instead of letting NewBackend panic. No signature change to `resumeAdapter.RunResume` itself — only its body changes. |
| `pkg/agentbackend/codex/backend_resume_test.go` | direct `b.RunResume(ctx, id, ...)` call | wrap id as SessionRef in test |
| `pkg/agentbackend/codex/executor_test.go` | 4 sites calling `ex.RunResume(ctx, "thr-...", ...)` | wrap each as `NewBackend(KindCodex, "", "thr-...")` |
| `pkg/agentbackend/claude/backend_resume_test.go` | `b.RunResume(ctx, id, ...)` call | wrap as SessionRef |
| `pkg/agentbackend/claude/executor_test.go` | `ex.RunResume(ctx, "sess-1", ...)` | wrap as SessionRef |
| `pkg/agentbackend/opencode/backend_resume_test.go` | 2 sites calling `b.RunResume(ctx, id, ...)` | wrap as SessionRef |
| `pkg/agentbackend/opencode/executor_test.go` | `b.RunResume(ctx, "sess-abc", ...)` | wrap as SessionRef |
| `internal/driver/tools.go::resumeTaskTool.Call` | NOT a `Backend.RunResume` caller (it uses `agentsdk.DelegateTask` with `Skill: "chat_resume"`) | **Nothing changes in P2.** Driver-side code path is established by P1 (constructs a SessionRef with empty Kind, writes `ref.Backend` as a string into agentsdk JSON body). The cross-wire body remains a bare string `session_id`. This row exists in the table only to record that the inventory was checked and the driver intentionally does not move in P2. |

Total: **23 unique files** in the P2 inventory (verified by counting rows in the table above and de-duplicating). This includes 12 production files + 11 test files. `internal/driver/tools.go` appears once though it has only one P2 site (resumeTaskTool.Call already changed in P1; in P2 it stops unwrapping `ref.Backend` to string). The expansion from the round-1 spec catches (a) the import-cycle constraint and (b) the test-stub surface that must change in lockstep with the interface signature. Without all of these, `go test ./...` does not compile.

**Why `executor.ResumeBackend` stays `string` (NOT SessionRef):** `pkg/agentbackend/backend.go:8` imports `internal/executor` for the `Task` / `Sink` / `Result` type aliases. The reverse import (`internal/executor` → `pkg/agentbackend`) does NOT exist today. Promoting `executor.ResumeBackend` to take a `SessionRef` would require importing `pkg/agentbackend.SessionRef` into `internal/executor` — which forms `agentbackend → executor → agentbackend`, a Go-rejected import cycle. So `executor.ResumeBackend` keeps `(ctx, sessionID string, ...)`; the `cmd/slave-agent` adapter is the seam that wraps that string into a `SessionRef` before calling the underlying `agentbackend.Backend.RunResume(ref, ...)`. Below the seam, the type is enforced; above the seam (only one direct caller, `ChatResumeExecutor`), the contract is that `sessionID` is the slave's own backend-native id — already enforced by where it's sourced from (kind marker JSON).

**Why these signature changes must land together in P2:** Go interface satisfaction is structural — once `agentbackend.Backend.RunResume` changes its parameter from `string` to `SessionRef`, every implementor (codex/claude/opencode `Executor.RunResume`, `*.Backend.RunResume` wrappers, `nilBackend` test stub, `commanderhub` proxy test stub) MUST update in the same commit or the build fails. Test files calling `b.RunResume(ctx, "literal-id", ...)` also fail to compile until they wrap the literal. The `executor.ResumeBackend` interface, by contrast, is independent — it has its own concrete signature — and isolates the seam at the slave-agent adapter.

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

The refactor changes four observable behaviors, all intentional:

1. **`resume_task` with a malformed/missing slave marker now errors** with `"resume failed: slave never reported a backend session id; …"` instead of silently delegating `chat_resume` against a bridge id (which would then fail later on the slave side with a less actionable error). This is the bug-fix half of the issue.
2. **`wait_task` / `get_task` response `session_id`** is empty when the slave's marker was absent (previously fell back to the bridge id). Consumers relying on the field always being non-empty must read `bridge_session_id` instead. The driver journal carries both fields too.
3. **`submit_task` response gains a `bridge_session_id` field** with the same value as `session_id` (both bridge — backend is not known at dispatch). `session_id` retains its bridge value indefinitely (see "submit_task contract" below); the new field is the explicit alias.
4. **Driver journal** new rows carry both `session_id` (backend) and `bridge_session_id` (bridge) as siblings. Old rows are read through a `^cse_` prefix classifier.

#### `submit_task` contract (permanent)

`submit_task.session_id` is the **permanent** exception to the "session_id always means backend" rule. The bridge id is the only id that exists at dispatch time (the slave's codex/claude/opencode CLI has not yet started, so no backend session id has been minted). `submit_task` cannot synchronously return a backend id without blocking on slave startup; we will not introduce that block. So `session_id` here stays the bridge id forever, deprecated for new consumers in favor of `bridge_session_id`. The reverse is not in scope: there is no plan to populate `submit_task.session_id` with a backend id, because doing so synchronously is architecturally impossible.

Consumers that need the backend id post-dispatch already use `wait_task` / `get_task` (which return it once the slave reports it via kind marker). Those endpoints follow the standard rule: `session_id` is backend, `bridge_session_id` is bridge.

All other paths (kind marker / loom_origin marker / loom-meta sidecar / `Backend.Run*` invocations / agentsdk traffic) are equivalent in observable behavior.

### Testing

| Layer | Tests added/changed |
|---|---|
| `pkg/agentbackend/sessionref_test.go` (new) | IsZero, HasBackend, String formatting; constructors (`NewBackend`/`NewBridgeOnly`/`WithBackend`) set fields correctly; constructor panic preconditions on the **id** parameters (Backend / Bridge / pairing) — empty Kind / AgentID explicitly do NOT panic. **No JSON tests** — SessionRef has no JSON methods; JSON shape lives on Record + response builders. |
| `internal/driver/tools_test.go` | Existing TaskInfo-construction fixtures updated; new `TestResumeTask_RefusesBridgeOnlyFallback`; new `TestWaitTask_BridgeAndBackendBothInResponse` that asserts the new `bridge_session_id` field appears alongside the existing `session_id` field. |
| `internal/driver/task_journal_test.go` | New `TestRecord_MarshalFlattensSessionRefIntoSiblings` (proves flat output, not nested), `TestRecord_UnmarshalLegacyBridgeSessionID` (cse_ prefix → Bridge), `TestRecord_UnmarshalLegacyBackendSessionID` (uuid shape → Backend), `TestRecord_RoundTripModernRow` (both fields populated). All flatten/unmarshal/classifier logic lives on `Record`, not on `SessionRef`. |
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

### Codex round 2 (2026-06-24)

Three P1 (blocking) + two P2 findings against commit `9d2f4f7`. All real and reproducible. Resolutions:

- **P1 (stale "SessionRef JSON I/O" requirements):** swept the spec — removed all references to `SessionRef.MarshalJSON` / `UnmarshalJSON`. The constraint at line 65 now points to `TaskJournal.Record.UnmarshalJSON` and the journal's bridge-id classifier; the `sessionref.go` and `sessionref_test.go` plan items dropped "JSON I/O" language and now explicitly say "no JSON methods on SessionRef".
- **P1 (import cycle `agentbackend → executor → agentbackend`):** verified by `grep -nE "internal/executor" pkg/agentbackend/*.go` showing `backend.go:8` does import `internal/executor`, while `grep -nE "pkg/agentbackend" internal/executor/*.go` is empty. Spec P2 rewrite: `executor.ResumeBackend` interface **stays bare `string`** (no signature change); the `cmd/slave-agent/main.go` adapter is the seam that wraps the string into a `SessionRef` before calling the (newly typed) `agentbackend.Backend.RunResume`. Above the seam (only one caller: `ChatResumeExecutor`), the contract that `sessionID` is a backend-native id is enforced by where it's sourced — the slave's own kind marker JSON. Added a dedicated "Why ResumeBackend stays string" paragraph.
- **P1 (P2 call-site inventory incomplete for `go test ./...`):** expanded from 13 sites to 18. Added every test file that calls `b.RunResume(ctx, "literal-id", ...)`: `internal/commander/handler_test.go`, `internal/commanderhub/proxy_test.go`, `pkg/agentbackend/codex/backend_resume_test.go`, `pkg/agentbackend/codex/executor_test.go` (4 sites), `pkg/agentbackend/claude/backend_resume_test.go`, `pkg/agentbackend/claude/executor_test.go`, `pkg/agentbackend/opencode/backend_resume_test.go` (2 sites), `pkg/agentbackend/opencode/executor_test.go`.
- **P2 (observable behavior section contradicted the journal/response changes):** expanded "Observable behavior change" from one bullet to four — now lists `resume_task` error, `wait_task`/`get_task` session_id-can-be-empty, `submit_task` adds `bridge_session_id`, journal new-row shape change.
- **P2 (submit_task contract ambiguity):** added explicit "submit_task contract (permanent)" subsection making clear `submit_task.session_id` is permanently the bridge id — backend cannot be produced synchronously — and consumers needing backend should use `wait_task`/`get_task`.

### Codex round 3 (2026-06-24)

One P1 + three P2 + one P3 against commit `65e76e5`. All real. Resolutions:

- **P1 (Testing table stale "Marshal+Unmarshal" on sessionref_test):** updated the testing-table row for `sessionref_test.go` to drop all JSON test claims and replace with "constructor panic preconditions". Symmetric with lines 202-203 / 343 saying SessionRef has no JSON methods.
- **P2 (agentID source undefined at wrap sites):** added a new "agentID source at each P2 wrap site" table to the spec, documenting kind+agentID source per site. **`AgentID` is intentionally empty** at `resumeAdapter` / `commander.Handler` / executor test fixtures because those seams own exactly one backend instance — disambiguation is meaningless. Driver-side construction (P1) keeps populating `AgentID` from the discovered slave `short_id`. Added a "Why empty `agentID` is OK" paragraph documenting the rationale.
- **P2 (NewBackend panic dangerous at user-input seams):** updated `NewBackend` doc to say it's "for internal invariant enforcement" and that external/user-input paths MUST validate first. Added explicit "Validate-then-wrap at user-input seams" paragraph with pseudo-code for `commander.Handler.SessionTurn`. Both `resumeAdapter` and commander handler now MUST guard `id != ""` before calling `NewBackend`.
- **P2 ("will move in P2" stale + wrong adapter name):** rewrote the P1 "What does NOT change" bullets for `internal/executor/chat_resume.go`, `commander/handler.go`, and `cmd/slave-agent/main.go` to no longer claim they "move". Corrected `backendExecutor` to `resumeAdapter` (verified by reading line 466 of `cmd/slave-agent/main.go`).
- **P3 (inventory count inconsistent):** verified actual unique-file count via `awk` extraction of table rows. Changed "Total: 13 files" → "Total: 23 unique files" (12 production + 11 test). Marked the entry shape clearly.

### Codex round 4 (2026-06-24)

One P1 + two P2 + two P3 against commit `976bc1e`. All real; verified each by reading actual code. Resolutions:

- **P1 (`resume_task` lacks Kind / slave short_id source):** verified by reading `tools.go:1252-1290` — resume_task has only `info.TargetID`, no agent card. Spec was wrong to claim `targetCard.Kind()` was available. Fix: add a new `ChildKind agentbackend.Kind` field to `TaskRecord` (populated at delegation time from `targetCard.Kind()`), recover it via `taskJournal.LatestByTaskID`. Fallback when journal rotated: `DiscoverAgents` + match on `card.AgentID == info.TargetID`. Added "resume_task identity resolution" subsection with pseudo-code.
- **P2 (journal flattening drops Kind/AgentID):** added new `ChildAgentID` (already existed) + new `ChildKind` top-level fields to Record's MarshalJSON output. `UnmarshalJSON` can now reconstruct full `SessionRef{Backend, Bridge, Kind, AgentID}` from journal rows. Updated `TestRecord_RoundTripModernRow` to assert all four SessionRef fields, not just Backend / Bridge.
- **P2 (`RunResume(SessionRef)` impls not required to reject bridge-only refs):** added explicit "RunResume implementations MUST reject bridge-only / zero refs" paragraph mandating a `!ref.HasBackend()` guard in every codex / claude / opencode `Executor.RunResume` implementation and their `*.Backend.RunResume` wrappers. Without it, future callers could pass a bridge-only ref and silently call the CLI with empty session id.
- **P3 (NewBackend kind validity):** added explicit constructor preconditions for `Kind`: `NewBackend("", agentID, backendID)` panics; same for `NewBridgeOnly`. Added unit test mandate.
- **P3 (commander pseudo-code wrong result type):** changed `result.Result{}` → `executor.Result{}` and added clarifying parenthetical.

### Codex round 5 (2026-06-25)

Two P1 + one P2 + two P3 against commit `a7054ee`. Both P1 verified real and material:

- **P1 (`child_kind` has no source):** confirmed by reading `agentsdk.AgentCard` (no `Kind` field) and `internal/driver/agent_card.go` (`parsedAgentCard.HasCommandKind` matches command-interface kind, not backend kind). The discovery card body carries no backend kind. So the round-4 plan to persist `ChildKind` at delegation time was unimplementable. **Resolution: rolled back** the `ChildKind` field and the constructor `kind != ""` precondition. Driver-side SessionRef instances are explicitly allowed to have empty `Kind`. The bug-prevention goal (`Backend ≠ Bridge`) is unaffected. Slave-side wraps still populate `Kind` from `a.b.Kind()` because the slave's `Backend` interface DOES expose it.
- **P1 (driver does NOT call Backend.RunResume directly):** confirmed by reading `tools.go:1313` — `resumeTaskTool.Call` issues `r.t.sdk.DelegateTask(... Skill: "chat_resume", ...)` over agentsdk JSON. There is no direct driver→backend RunResume call. P2 inventory's claim that "`resumeTaskTool.Call` stops unwrapping ref.Backend to string; passes ref directly" was incoherent. **Resolution:** the inventory row now explicitly states "nothing changes in P2 for the driver path"; the wire JSON body keeps `session_id` as a bare string carrying `ref.Backend`. SessionRef type-discipline at the construction site is still the value driver gets; it just doesn't propagate over the wire.

**P2 + P3 deferred to plan/implementation phase** (per operator decision):

- **P2 (RunResume bridge-only guard lacks negative test coverage):** the spec mandates the `!ref.HasBackend()` guard exists; adding the specific negative test cases (one per backend impl + wrapper, 6 total sites) is implementation detail to write into the plan, not a spec-level decision.
- **P3 (constructor panic test wording was stale):** addressed inline (rolled back as part of P1 above — `kind != ""` precondition removed entirely).
- **P3 (journal round-trip wording permits losing Kind/AgentID):** spec now says journal round-trip asserts `Backend` + `Bridge` + `AgentID` and that `Kind` stays empty (matches driver-side reality after the P1#1 rollback). Plan will define the exact test cases.

Net result: the spec correctly characterizes driver-side and slave-side roles for `SessionRef.Kind`; the `agentbackend.Kind` discovery card field (which would be needed to populate driver-side `Kind`) is **explicitly out of scope** for this refactor — adding it is a separate spec / agentsdk concern.

## Out of scope (deliberately tracked here so the next refactor doesn't re-litigate)

- **Renaming wire-format fields** to e.g. `backend_session_id` for clarity. Considered and rejected because it would force a coordinated migration with all deployed slaves' loom-meta sidecars and with agentserver SDK consumers. The new typed Go layer is sufficient to enforce the discipline going forward.
- **Pushing `SessionRef` into `agentsdk`.** Requires an agentsdk SDK version bump, which couples agentserver and multi-agent release cycles. We wrap SDK return values at the driver boundary instead.
- **A `SessionResolver` service layer** that hides RunResume entirely. Heavier; not justified by the bug we're fixing.
- **Adding a `backend_kind` field to the published discovery card** (`internal/driver/agent_card.go::parsedAgentCard`, `agentsdk.AgentCard`, and the slave-side card publish at registration). Would let driver-side `SessionRef` populate `Kind` for completeness. Considered and rejected for this PR: agentsdk SDK surface change couples release cycles; the `Kind` field on driver-side refs is informational only (bug-prevention goal is `Backend ≠ Bridge`, achieved without it). If a future refactor needs driver-side `Kind` for routing or display, file that as its own spec.
