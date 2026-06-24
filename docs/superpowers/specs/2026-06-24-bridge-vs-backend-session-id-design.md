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

- **Wire format additive-only.** No existing JSON / YAML / file-format field is renamed, retyped, or removed by this refactor. The `session_id` / `session` / `child_session_id` field names in markers / sidecars / journal / API responses keep their meaning and continue to carry the backend-native id (i.e. `Backend` post-refactor). New optional `bridge_session_id` / `child_bridge_session_id` fields are added with `omitempty` to driver-owned writers (TaskJournal rows; wait_task / get_task response bodies) — these are backward-compatible additions: older consumers ignore unknown fields and continue to see today's `session_id` semantics.
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
// JSON shape preserves existing wire fields for backward compatibility:
//   {"session_id": "<backend>", "bridge_session_id": "<bridge>", "kind": "...", "agent_id": "..."}
// Empty fields are omitted. Reading legacy data (bridge-only fallback was
// the old wire shape in some places) is handled by UnmarshalJSON below.
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

// JSON I/O. Writes use the existing wire field names; reads accept the same
// plus legacy aliases so older sidecars and journal rows keep parsing.
func (r SessionRef) MarshalJSON() ([]byte, error)
func (r *SessionRef) UnmarshalJSON(data []byte) error
```

#### Wire-format mapping (read + write)

| Source | Wire field | SessionRef field |
|---|---|---|
| `kind marker` (slave executor → driver) | `session_id` (string) | `Backend` |
| `loom_origin` marker (driver → slave) | `session` (string) | `Backend` |
| `loom-meta` sidecar (slave codex_home) | `session_id` (string) | `Backend` |
| `agentsdk.TaskInfo` | `session_id` (string, `cse_…`) | `Bridge` |
| `agentsdk.DelegateTaskResponse` | `session_id` (string, `cse_…`) | `Bridge` |
| driver `TaskJournal` rows (read) | `session_id` (string) AND optional `bridge_session_id` | both, as written |
| driver `TaskJournal` rows (write) | `session_id` (Backend) + `bridge_session_id` (Bridge if set) | both populated |

`MarshalJSON` always writes both fields when populated. `UnmarshalJSON` accepts:

- New shape: `{"session_id": "<backend>", "bridge_session_id": "<bridge>", "kind": "...", "agent_id": "..."}`
- Legacy shape: `{"session_id": "<id>"}` — id goes into `Backend` (matches today's marker / sidecar semantic where `session_id` always meant backend-native).
- The agentsdk-shape `cse_*` id never flows through `SessionRef.UnmarshalJSON`; it enters the system via Go constructor `NewBridgeOnly` (below), not via JSON.

#### Constructors

```go
// NewBackend builds a ref from a known backend-native id (kind marker,
// loom_origin marker, executor.Result, sidecar read).
func NewBackend(kind Kind, agentID, backendID string) SessionRef

// NewBridgeOnly wraps an agentsdk response that has only the bridge id.
// Used at the driver↔agentsdk seam; downstream code that needs Backend
// must error if it sees IsZero() || !HasBackend().
func NewBridgeOnly(kind Kind, agentID, bridgeID string) SessionRef

// WithBackend returns a copy of r with Backend filled. Used by the driver
// once it has paired a bridge response with the slave's marker output.
func (r SessionRef) WithBackend(backendID string) SessionRef
```

There is intentionally **no** `NewBoth` constructor that takes both at once — pairing happens via `WithBackend` on an existing bridge-only ref, so the code path that does the pairing is also the one that has to look up the backend id.

### PR 1 — types + driver boundary (fixes #29 bug)

Changes:

1. **`pkg/agentbackend/sessionref.go`** — new file, the type + constructors + JSON I/O + tests.
2. **`pkg/agentbackend/sessionref_test.go`** — new file, table-driven tests for IsZero/HasBackend/String/Marshal/Unmarshal/round-trip/legacy-read.
3. **`internal/driver/tools.go`** — every line touching `SessionID` / `session_id` / `ChildSessionID` migrates to `SessionRef`. Concretely:
   - `delegatedTaskRecord.Response` was used for `Response.SessionID` (bridge); wrap into `NewBridgeOnly(kind, agentShortID, resp.SessionID)` at the agentsdk seam, stash as `SessionRef` on the record.
   - The two response-builder paths in `wait_task` / `get_task` that today do `firstNonEmpty(sessionIDFromMarker(...), info.SessionID)`: split — extract `markerBackendID` and `bridgeID = info.SessionID` separately, build a `SessionRef` with both populated, then marshal back to the existing JSON shape (which already carries them as one `session_id` field today; that field becomes `Backend` write-source going forward, and `bridge_session_id` becomes a new optional sibling field). **No breaking JSON change in the response** — the existing `session_id` field still holds the backend-native id (which is what existing consumers actually wanted); we just add `bridge_session_id` alongside.
   - `resume_task` reads the prior task via `g.t.sdk.GetTask(...)`, extracts the slave's kind-marker session id (`kw.SessionID`), then today falls back to `info.SessionID`. Rewrite:
     ```go
     // Old:
     // sessionID := firstNonEmpty(kw.SessionID, info.SessionID)
     // New:
     ref := agentbackend.NewBackend(kind, slaveShortID, kw.SessionID)
     if !ref.HasBackend() {
         return nil, &MCPToolError{Message: "resume failed: slave never reported a backend session id; bridge id alone cannot resume a codex/claude conversation"}
     }
     // Pass ref.Backend to RunResume (P1 still unwraps); never touch info.SessionID for resume.
     ```
   - `sessionIDFromMarker` helper stays (parses kind marker JSON), but its return value is always treated as a backend id.
4. **`internal/driver/task_journal.go`** — `Record` struct fields `SessionID` / `ChildSessionID` change Go type from `string` to `SessionRef`. JSON tags stay as `session_id` / `child_session_id` (Backend goes into those); new sibling fields `bridge_session_id` / `child_bridge_session_id` get added with `omitempty` for the Bridge when present. Read path uses `SessionRef.UnmarshalJSON` which accepts the legacy single-field shape (read backward compat).
5. **`internal/driver/tools_test.go`** — adjust tests that construct fake responses to use the constructors; rebuild fixtures that referenced `info.SessionID` as if it were a backend id (the test fixtures themselves were arguably buggy — they sometimes shoved a fake `cse_` into a kind marker, which `SessionRef.UnmarshalJSON` plus the new typed callers will catch).
6. **Test for the actual #29 bug**: new test `TestResumeTask_RefusesBridgeOnlyFallback` that constructs a `TaskInfo` where the slave kind marker is missing and `info.SessionID = "cse_fake_bridge"`. Pre-PR this would have called `chat_resume` with `"cse_fake_bridge"`. Post-PR it returns the actionable error from the rewrite above. (This is the proof-of-fix for the spec.)

What does NOT change in P1:
- `Backend.RunResume(ctx, sessionID string, …)` signature — driver still unwraps `ref.Backend` and passes the string.
- `pkg/agentbackend/codex/*`, `claude/*`, `opencode/*` — completely untouched.
- `pkg/agentbackend/loomorigin.go` — `ParentLink.SessionID` (the `session` field in the marker) stays bare `string`. Driver construction of the marker writes `ref.Backend` into it.
- `pkg/agentbackend/codex/loommeta.go` — sidecar struct stays bare `string`.

### PR 2 — promote `SessionRef` into `Backend.RunResume`

Changes:

1. **`pkg/agentbackend/backend.go`** — change `RunResume`:
   ```go
   // Before
   RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
   // After
   RunResume(ctx context.Context, ref SessionRef, answer string, sink Sink) (Result, error)
   ```
   Plus a note that `ref.Backend` is the required field; implementations may panic / return error if `ref.HasBackend()` is false (matching today's "empty sessionID" behavior).
2. **`pkg/agentbackend/codex/executor.go`** — `RunResume` reads `ref.Backend`. The internal `sessionID` local variable just becomes `ref.Backend`. No behavior change.
3. **`pkg/agentbackend/claude/executor.go`** — same.
4. **`pkg/agentbackend/opencode/executor.go`** — same.
5. **`pkg/agentbackend/codex/appserver_worker.go`** — the `RunResume` fallback path in the app-server worker passes the string `sessionID` through; switch to `SessionRef`.
6. **`internal/driver/tools.go::resumeTaskTool.Call`** — stops unwrapping `ref.Backend` to string; passes `ref` directly.
7. **`pkg/agentbackend/backend_test.go`** — the `nilBackend` test stub's `RunResume` signature updates; same for the test stubs in the three executor test files.
8. **`pkg/agentbackend/codex/appserver_worker_test.go`** — ~3 test sites that assert `RunResume` was called with a specific string; adjust to assert `ref.Backend == "thr-1"`.

What does NOT change in P2:
- Wire format. Still none.
- `Session`, `Result` struct shapes.
- Loom-origin marker / loommeta sidecar / kind marker / journal JSON.

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
- Wire-format backward-read compatibility means: rolling out P1 first, then later P2, then later possibly downgrading P1 only, never produces an unreadable sidecar / journal / API response.
- The two PRs do NOT have to be merged in order; P2 depends on P1 only for the constructors and the type definition.

## Open questions

None at design time. If something surfaces during plan or implementation, escalate to the human.

## Out of scope (deliberately tracked here so the next refactor doesn't re-litigate)

- **Renaming wire-format fields** to e.g. `backend_session_id` for clarity. Considered and rejected because it would force a coordinated migration with all deployed slaves' loom-meta sidecars and with agentserver SDK consumers. The new typed Go layer is sufficient to enforce the discipline going forward.
- **Pushing `SessionRef` into `agentsdk`.** Requires an agentsdk SDK version bump, which couples agentserver and multi-agent release cycles. We wrap SDK return values at the driver boundary instead.
- **A `SessionResolver` service layer** that hides RunResume entirely. Heavier; not justified by the bug we're fixing.
