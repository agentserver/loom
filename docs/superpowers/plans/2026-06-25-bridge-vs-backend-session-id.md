# Bridge vs backend-native session ID disambiguation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace bare `string` session ids in driver + agentbackend layers with a typed `SessionRef` so the compiler distinguishes bridge ids (`cse_…`, agentserver) from backend-native ids (codex thread / claude session / opencode session), and fix the `resume_task` fallback bug that silently passes a bridge id into a backend resume.

**Architecture:** Introduce `agentbackend.SessionRef{Backend, Bridge, Kind, AgentID}` (zero JSON methods on the type — containing structs own flatten). Two-PR landing: P1 adds the type + driver-side migration + journal type swap + the bug-fix test (operationally important). P2 promotes `Backend.RunResume` interface to take `SessionRef` and migrates all implementations / test stubs / call-site wrappers (mechanical hardening). `internal/executor.ResumeBackend` permanently stays bare `string` to avoid an `agentbackend → executor → agentbackend` import cycle; the `cmd/slave-agent` `resumeAdapter` is the wrap seam.

**Tech Stack:** Go 1.22+, `pkg/agentbackend` (new file), `internal/driver/tools.go`, `internal/driver/task_journal.go`, `internal/executor`, `internal/commander`, `cmd/slave-agent`, three backend implementations (codex/claude/opencode) — production + tests.

## Global Constraints

- **Wire format additive-only, one documented exception.** No existing JSON / YAML / file-format field is renamed, retyped, or removed. `session_id` / `session` / `child_session_id` in markers / sidecars keep their meanings unchanged. **Exception:** driver journal `session_id` and `wait_task`/`get_task` response `session_id` flip from `firstNonEmpty(marker, bridge)` to "backend only, empty otherwise". New optional `bridge_session_id` / `child_bridge_session_id` fields are added with `omitempty` to driver-owned writers (TaskJournal, `submit_task` / `wait_task` / `get_task` responses).
- **No new wire fields in formats we don't own.** loom-meta sidecar / `loom_origin` marker / kind marker carry only `session_id` / `session`.
- **`submit_task.session_id` is the permanent exception:** keeps the bridge id forever (backend id not known synchronously); `bridge_session_id` is the new explicit alias.
- **Read backward compat.** `TaskJournal.Record.UnmarshalJSON` accepts legacy single-field shape AND uses `^cse_` prefix classifier to route legacy strings to `Backend` or `Bridge`.
- **SessionRef has NO JSON methods.** Containing structs own flatten. `Marshal/UnmarshalJSON` on the inner type would emit nested JSON, not sibling fields.
- **Bridge ≠ Backend invariant.** Constructors enforce: `NewBackend` only takes backend id; `NewBridgeOnly` only takes bridge id. `WithBackend` is the only pairing path.
- **Driver-side `SessionRef.Kind` is empty.** Driver has no backend-kind source — `agentsdk.AgentCard` has no `Kind` field, discovery card body carries no backend kind. Driver-side refs are constructed with `kind=""`. Slave-side wraps populate `Kind` via `a.b.Kind()` (Backend interface exposes it).
- **`Backend` is required for any backend-facing operation.** `RunResume`, `chat_resume` delegation, sidecar writes, cross-daemon nesting all require `Backend != ""`. If only `Bridge` is known, caller MUST error rather than guess.
- **Two-PR landing, strict order.** P1 (Tasks 1–8) lands first. P2 (Tasks 9–13) depends on P1's `SessionRef` type and must rebase on master after P1 lands.
- **Tests stay green at every commit.** Each task ends with `go test ./internal/driver/... && go vet ./...` (P1) or `go test ./... && go vet ./...` (P2) passing.
- **`executor.ResumeBackend` permanently stays bare `string`.** `pkg/agentbackend/backend.go:8` imports `internal/executor` for `Task`/`Sink`/`Result` aliases; reverse import would form `agentbackend → executor → agentbackend` cycle. The `cmd/slave-agent/main.go::resumeAdapter` wraps the string into `SessionRef` at the seam.

---

## File Structure

### Created in P1
- `multi-agent/pkg/agentbackend/sessionref.go` — `SessionRef` type, constructors, predicates. ~80 LOC.
- `multi-agent/pkg/agentbackend/sessionref_test.go` — constructor + predicate tests. ~150 LOC.

### Modified in P1
- `multi-agent/internal/driver/task_journal.go` — `TaskRecord` Go fields change from `SessionID string` / `ChildSessionID string` to `SessionRef SessionRef` / `ChildSessionRef SessionRef`. Add custom `MarshalJSON` / `UnmarshalJSON` on `TaskRecord` flattening to existing wire fields + new `bridge_session_id` / `child_bridge_session_id` siblings, with `^cse_` legacy classifier on read.
- `multi-agent/internal/driver/task_journal_test.go` — new flatten / legacy-read / round-trip tests.
- `multi-agent/internal/driver/tools.go` — five touch sites:
  - `delegatedTaskRecord` struct: replace `Response *agentsdk.DelegateTaskResponse` (still kept for other fields) by adding a `SessionRef` field that holds the bridge wrap; `recordDelegatedTask` writes `rec.SessionRef` into `TaskRecord.SessionRef` instead of `rec.Response.SessionID`.
  - `submit_task` response (~line 701): emit both `session_id` (bridge for back-compat) and `bridge_session_id` (new sibling).
  - `wait_task` response (~line 781): split `firstNonEmpty` into `markerBackendID` (→ `session_id`, empty when marker absent) + `bridgeID` (→ `bridge_session_id`).
  - `get_task` response (~line 900): same as wait_task.
  - `resume_task` (~line 1290): replace `sessionID := firstNonEmpty(kw.SessionID, info.SessionID)` with empty-check on `kw.SessionID` (error if empty) + `agentbackend.NewBackend("", slaveShortID, kw.SessionID)`. Use `ref.Backend` for the agentsdk JSON body (still a string on the wire).
- `multi-agent/internal/driver/tools_test.go` — migrate fixtures to use constructors; new `TestResumeTask_RefusesEmptyMarker`; new `TestWaitTask_BridgeAndBackendBothInResponse`.

### Modified in P2
- `multi-agent/pkg/agentbackend/backend.go` — `Backend.RunResume` interface signature changes from `(ctx, sessionID, answer string, sink Sink) (Result, error)` to `(ctx context.Context, ref SessionRef, answer string, sink Sink) (Result, error)`.
- `multi-agent/pkg/agentbackend/backend_test.go` — `nilBackend.RunResume` signature update.
- `multi-agent/pkg/agentbackend/codex/executor.go` `*Executor.RunResume` — signature + `!ref.HasBackend()` guard.
- `multi-agent/pkg/agentbackend/codex/backend.go` `*Backend.RunResume` — signature pass-through.
- `multi-agent/pkg/agentbackend/codex/appserver_worker.go` — fallback caller wraps as `SessionRef`.
- `multi-agent/pkg/agentbackend/claude/executor.go` `*Executor.RunResume` — signature + guard.
- `multi-agent/pkg/agentbackend/claude/backend.go` `*Backend.RunResume` — signature pass-through.
- `multi-agent/pkg/agentbackend/opencode/executor.go` `*Executor.RunResume` — signature + guard.
- `multi-agent/pkg/agentbackend/opencode/backend.go` `*Backend.RunResume` — signature pass-through.
- `multi-agent/internal/commander/handler.go` — both call sites validate `id != ""` then wrap as `NewBackend(h.Backend.Kind(), "", id)`.
- `multi-agent/cmd/slave-agent/main.go` — `resumeAdapter.RunResume` (signature unchanged) body validates `sid != ""` then wraps as `NewBackend(a.b.Kind(), "", sid)`.
- Test stubs / assertions: `internal/commander/handler_test.go`, `internal/commanderhub/proxy_test.go`, `pkg/agentbackend/codex/{backend_resume_test.go, executor_test.go, appserver_worker_test.go}`, `pkg/agentbackend/claude/{backend_resume_test.go, executor_test.go}`, `pkg/agentbackend/opencode/{backend_resume_test.go, executor_test.go}`.

### Untouched (intentionally)
- `multi-agent/pkg/agentbackend/loomorigin.go` `ParentLink.SessionID` — stays bare string. Driver populates it with `ref.Backend`.
- `multi-agent/pkg/agentbackend/codex/loommeta.go` — slave-side, stays bare string.
- `multi-agent/internal/executor/chat_resume.go` `ResumeBackend` interface — permanently bare string (import cycle).
- `agentsdk` SDK (any version) — wrap at driver seam, do not modify.
- `multi-agent/internal/driver/agent_card.go` — driver-side `parsedAgentCard` not extended with backend kind (out of scope).

---

## P1 — Type + Driver Boundary (fixes #29 bug)

### Task 1: Introduce `SessionRef` type and constructors

**Files:**
- Create: `multi-agent/pkg/agentbackend/sessionref.go`
- Create: `multi-agent/pkg/agentbackend/sessionref_test.go`

**Interfaces:**
- Consumes: `pkg/agentbackend.Kind` (existing named type `type Kind string`).
- Produces:
  - `type SessionRef struct { Backend string; Bridge string; Kind Kind; AgentID string }`
  - `func (r SessionRef) IsZero() bool`
  - `func (r SessionRef) HasBackend() bool`
  - `func (r SessionRef) String() string`
  - `func NewBackend(kind Kind, agentID, backendID string) SessionRef` — panics if `backendID == ""`.
  - `func NewBridgeOnly(kind Kind, agentID, bridgeID string) SessionRef` — panics if `bridgeID == ""`.
  - `func (r SessionRef) WithBackend(backendID string) SessionRef` — panics if `backendID == ""` OR `r.Bridge == ""` OR `r.Backend != ""`. Inherits `r.Kind` / `r.AgentID`.

- [ ] **Step 1: Write the failing test file**

Create `multi-agent/pkg/agentbackend/sessionref_test.go`:

```go
package agentbackend

import (
	"testing"
)

func TestSessionRef_IsZero(t *testing.T) {
	cases := []struct {
		name string
		ref  SessionRef
		want bool
	}{
		{"empty struct", SessionRef{}, true},
		{"only kind", SessionRef{Kind: "codex"}, true},
		{"only agentID", SessionRef{AgentID: "ag-1"}, true},
		{"backend set", SessionRef{Backend: "thr-1"}, false},
		{"bridge set", SessionRef{Bridge: "cse_1"}, false},
		{"both set", SessionRef{Backend: "thr-1", Bridge: "cse_1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.IsZero(); got != tc.want {
				t.Errorf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionRef_HasBackend(t *testing.T) {
	if (SessionRef{}).HasBackend() {
		t.Error("zero ref should not HasBackend")
	}
	if (SessionRef{Bridge: "cse_1"}).HasBackend() {
		t.Error("bridge-only ref should not HasBackend")
	}
	if !(SessionRef{Backend: "thr-1"}).HasBackend() {
		t.Error("backend ref should HasBackend")
	}
}

func TestSessionRef_String(t *testing.T) {
	cases := []struct {
		name string
		ref  SessionRef
		want string
	}{
		{"empty", SessionRef{}, "SessionRef{}"},
		{"backend only", SessionRef{Backend: "thr-1"}, "SessionRef{backend=thr-1}"},
		{"bridge only", SessionRef{Bridge: "cse_1"}, "SessionRef{bridge=cse_1}"},
		{"both", SessionRef{Backend: "thr-1", Bridge: "cse_1"}, "SessionRef{backend=thr-1 (bridge=cse_1)}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewBackend_Success(t *testing.T) {
	// Empty kind and empty agentID are both legitimate (driver-side construction).
	cases := []struct {
		name      string
		kind      Kind
		agentID   string
		backendID string
	}{
		{"all set", "codex", "ag-1", "thr-1"},
		{"empty kind", "", "ag-1", "thr-1"},
		{"empty agentID", "codex", "", "thr-1"},
		{"empty kind and agentID", "", "", "thr-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := NewBackend(tc.kind, tc.agentID, tc.backendID)
			if ref.Backend != tc.backendID {
				t.Errorf("Backend = %q, want %q", ref.Backend, tc.backendID)
			}
			if ref.Bridge != "" {
				t.Errorf("Bridge should be empty, got %q", ref.Bridge)
			}
			if ref.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", ref.Kind, tc.kind)
			}
			if ref.AgentID != tc.agentID {
				t.Errorf("AgentID = %q, want %q", ref.AgentID, tc.agentID)
			}
		})
	}
}

func TestNewBackend_EmptyBackendIDPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewBackend with empty backendID should panic")
		}
	}()
	NewBackend("codex", "ag-1", "")
}

func TestNewBridgeOnly_Success(t *testing.T) {
	ref := NewBridgeOnly("codex", "ag-1", "cse_1")
	if ref.Bridge != "cse_1" {
		t.Errorf("Bridge = %q, want cse_1", ref.Bridge)
	}
	if ref.Backend != "" {
		t.Errorf("Backend should be empty, got %q", ref.Backend)
	}
	// Empty kind is allowed.
	ref2 := NewBridgeOnly("", "", "cse_2")
	if ref2.Bridge != "cse_2" {
		t.Errorf("Bridge = %q, want cse_2", ref2.Bridge)
	}
}

func TestNewBridgeOnly_EmptyBridgeIDPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewBridgeOnly with empty bridgeID should panic")
		}
	}()
	NewBridgeOnly("codex", "ag-1", "")
}

func TestWithBackend_Success(t *testing.T) {
	base := NewBridgeOnly("codex", "ag-1", "cse_1")
	paired := base.WithBackend("thr-1")
	if paired.Backend != "thr-1" {
		t.Errorf("Backend = %q, want thr-1", paired.Backend)
	}
	if paired.Bridge != "cse_1" {
		t.Errorf("Bridge = %q, want cse_1", paired.Bridge)
	}
	if paired.Kind != "codex" || paired.AgentID != "ag-1" {
		t.Errorf("Kind/AgentID lost: %+v", paired)
	}
	// Original unchanged (value type).
	if base.Backend != "" {
		t.Errorf("base.Backend mutated: %+v", base)
	}
}

func TestWithBackend_PreconditionPanics(t *testing.T) {
	cases := []struct {
		name string
		base SessionRef
		arg  string
	}{
		{"empty backendID", NewBridgeOnly("codex", "ag-1", "cse_1"), ""},
		{"already paired", SessionRef{Backend: "thr-old", Bridge: "cse_1"}, "thr-new"},
		{"no bridge base", SessionRef{Kind: "codex"}, "thr-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("WithBackend should panic for %s", tc.name)
				}
			}()
			tc.base.WithBackend(tc.arg)
		})
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run from `multi-agent/`:
```
go test ./pkg/agentbackend/ -run TestSessionRef -count=1
```
Expected: FAIL with `undefined: SessionRef` / `undefined: NewBackend` / etc.

- [ ] **Step 3: Create the implementation file**

Create `multi-agent/pkg/agentbackend/sessionref.go`:

```go
package agentbackend

import "fmt"

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
// See internal/driver/task_journal.go for the canonical example.
//
// Driver-side construction sites legitimately leave Kind and AgentID empty
// (no backend-kind source on the driver). Slave-side wraps populate Kind
// via the Backend interface's Kind() method.
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
func (r SessionRef) String() string {
	switch {
	case r.Backend != "" && r.Bridge != "":
		return fmt.Sprintf("SessionRef{backend=%s (bridge=%s)}", r.Backend, r.Bridge)
	case r.Backend != "":
		return fmt.Sprintf("SessionRef{backend=%s}", r.Backend)
	case r.Bridge != "":
		return fmt.Sprintf("SessionRef{bridge=%s}", r.Bridge)
	default:
		return "SessionRef{}"
	}
}

// NewBackend builds a ref from a known backend-native id (kind marker,
// loom_origin marker, executor.Result, sidecar read, slave's own session id).
// Panics if backendID is empty — this is for internal invariant enforcement
// where the caller has already validated the id. For external/user-input
// paths (e.g. commander.Handler.SessionTurn) callers MUST validate first
// and return an error themselves, not catch the panic.
//
// agentID may be empty when no cross-agent identity is needed; kind may be
// empty when no backend-kind source is available (driver-side construction).
func NewBackend(kind Kind, agentID, backendID string) SessionRef {
	if backendID == "" {
		panic("agentbackend.NewBackend: empty backendID")
	}
	return SessionRef{Backend: backendID, Kind: kind, AgentID: agentID}
}

// NewBridgeOnly wraps an agentsdk response that has only the bridge id.
// Used at the driver↔agentsdk seam; downstream code that needs Backend
// must error if it sees !HasBackend(). Panics if bridgeID is empty.
func NewBridgeOnly(kind Kind, agentID, bridgeID string) SessionRef {
	if bridgeID == "" {
		panic("agentbackend.NewBridgeOnly: empty bridgeID")
	}
	return SessionRef{Bridge: bridgeID, Kind: kind, AgentID: agentID}
}

// WithBackend returns a copy of r with Backend filled. The only legitimate
// pairing path: take a bridge-only ref returned by NewBridgeOnly, look up
// the backend id (e.g. from the slave's kind marker), and pair them.
// Panics if backendID == "" OR r.Bridge == "" OR r.Backend != "".
func (r SessionRef) WithBackend(backendID string) SessionRef {
	if backendID == "" {
		panic("agentbackend.SessionRef.WithBackend: empty backendID")
	}
	if r.Bridge == "" {
		panic("agentbackend.SessionRef.WithBackend: base has no Bridge; only meaningful on bridge-only refs")
	}
	if r.Backend != "" {
		panic("agentbackend.SessionRef.WithBackend: base already has Backend; refuse to overwrite")
	}
	r.Backend = backendID
	return r
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```
go test ./pkg/agentbackend/ -run TestSessionRef -count=1
go test ./pkg/agentbackend/ -run TestNewBackend -count=1
go test ./pkg/agentbackend/ -run TestNewBridgeOnly -count=1
go test ./pkg/agentbackend/ -run TestWithBackend -count=1
```
Expected: all PASS.

- [ ] **Step 5: Confirm full package + downstream still compile**

```
go vet ./...
go test ./pkg/agentbackend/... -count=1
```
Expected: PASS (no callers yet — additive change).

- [ ] **Step 6: Commit**

```
git add multi-agent/pkg/agentbackend/sessionref.go multi-agent/pkg/agentbackend/sessionref_test.go
git commit -m "feat(agentbackend): introduce SessionRef typed bridge/backend session ref (#29 P1.1)

Adds pkg/agentbackend.SessionRef: a typed reference that distinguishes
backend-native session ids from agentserver bridge ids at the type
level. No JSON methods on the type itself — containing structs (added
in later tasks) own the flatten so the wire stays additive.

Constructors:
- NewBackend(kind, agentID, backendID) for known backend ids
- NewBridgeOnly(kind, agentID, bridgeID) for agentsdk wraps
- WithBackend(backendID) for pairing a bridge-only ref

All constructors panic on empty required id; empty Kind / AgentID are
allowed (driver-side construction has no backend-kind source).

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 2: Migrate `TaskJournal.Record` to `SessionRef` with custom JSON

**Files:**
- Modify: `multi-agent/internal/driver/task_journal.go` (struct + add `MarshalJSON` / `UnmarshalJSON`)
- Modify: `multi-agent/internal/driver/task_journal_test.go` (new tests)

**Interfaces:**
- Consumes: `agentbackend.SessionRef`, `agentbackend.NewBackend` (from Task 1).
- Produces:
  - `TaskRecord` now has `SessionRef agentbackend.SessionRef` (replaces `SessionID string`) and `ChildSessionRef agentbackend.SessionRef` (replaces `ChildSessionID string`).
  - JSON shape (output): existing fields unchanged + new optional `bridge_session_id` / `child_bridge_session_id`.
  - JSON shape (input): same fields tolerated + legacy single-field rows classified by `^cse_` prefix → Bridge, else Backend.
  - `TaskRecord` has Go methods `MarshalJSON` and `UnmarshalJSON` exported (required by encoding/json receiver lookup).

- [ ] **Step 1: Write failing tests**

Append to `multi-agent/internal/driver/task_journal_test.go` (create if file does not exist; otherwise add at end):

```go
func TestRecord_MarshalFlattensSessionRefIntoSiblings(t *testing.T) {
	r := TaskRecord{
		TS:    "2026-06-25T00:00:00Z",
		Event: "delegate_task",
		Tool:  "submit_task",
		TaskID: "task_1",
		SessionRef: agentbackend.SessionRef{
			Backend: "thr-1",
			Bridge:  "cse_1",
		},
		ChildSessionRef: agentbackend.SessionRef{
			Backend: "thr-child",
			Bridge:  "cse_child",
		},
		ChildAgentID: "ag-child",
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Decode into a generic map so we can verify the JSON is flat (no nested
	// session_id objects).
	var got map[string]interface{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal generic: %v", err)
	}
	if v, ok := got["session_id"]; !ok || v != "thr-1" {
		t.Errorf("session_id flat = %v (ok=%v), want \"thr-1\"", v, ok)
	}
	if v, ok := got["bridge_session_id"]; !ok || v != "cse_1" {
		t.Errorf("bridge_session_id = %v (ok=%v), want \"cse_1\"", v, ok)
	}
	if v, ok := got["child_session_id"]; !ok || v != "thr-child" {
		t.Errorf("child_session_id = %v (ok=%v), want \"thr-child\"", v, ok)
	}
	if v, ok := got["child_bridge_session_id"]; !ok || v != "cse_child" {
		t.Errorf("child_bridge_session_id = %v (ok=%v), want \"cse_child\"", v, ok)
	}
	if v, ok := got["child_agent_id"]; !ok || v != "ag-child" {
		t.Errorf("child_agent_id = %v (ok=%v), want \"ag-child\"", v, ok)
	}
	// Ensure session_id is a string, not a nested object.
	if _, isObj := got["session_id"].(map[string]interface{}); isObj {
		t.Error("session_id should be a flat string, not nested object")
	}
}

func TestRecord_UnmarshalLegacyBridgeSessionID(t *testing.T) {
	// Pre-refactor row: only session_id, value is a cse_… bridge id.
	raw := `{"ts":"2026-06-23T08:00:00Z","event":"delegate_task","tool":"submit_task","task_id":"t1","session_id":"cse_legacy"}`
	var r TaskRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.SessionRef.Bridge != "cse_legacy" {
		t.Errorf("SessionRef.Bridge = %q, want \"cse_legacy\"", r.SessionRef.Bridge)
	}
	if r.SessionRef.Backend != "" {
		t.Errorf("SessionRef.Backend should be empty for legacy bridge row, got %q", r.SessionRef.Backend)
	}
}

func TestRecord_UnmarshalLegacyBackendSessionID(t *testing.T) {
	// Pre-refactor row that already carried a backend id (less common — but
	// driver did sometimes write backend ids in journal terminal records).
	raw := `{"ts":"2026-06-23T08:00:00Z","event":"delegate_task","tool":"submit_task","task_id":"t1","session_id":"019ef428-d06b-77b0-bdfe-20118f4cbe7d"}`
	var r TaskRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.SessionRef.Backend != "019ef428-d06b-77b0-bdfe-20118f4cbe7d" {
		t.Errorf("SessionRef.Backend = %q, want the uuid", r.SessionRef.Backend)
	}
	if r.SessionRef.Bridge != "" {
		t.Errorf("SessionRef.Bridge should be empty for legacy backend row, got %q", r.SessionRef.Bridge)
	}
}

func TestRecord_UnmarshalLegacyChildSessionID(t *testing.T) {
	// child_session_id classifier — same rules.
	raw := `{"ts":"2026-06-23T08:00:00Z","event":"delegate_task","tool":"submit_task","task_id":"t1","child_session_id":"cse_child_legacy","child_agent_id":"ag-1"}`
	var r TaskRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.ChildSessionRef.Bridge != "cse_child_legacy" {
		t.Errorf("ChildSessionRef.Bridge = %q, want \"cse_child_legacy\"", r.ChildSessionRef.Bridge)
	}
	if r.ChildAgentID != "ag-1" {
		t.Errorf("ChildAgentID = %q, want \"ag-1\"", r.ChildAgentID)
	}
}

func TestRecord_RoundTripModernRow(t *testing.T) {
	// Modern (post-refactor) row: explicit bridge_session_id sibling means
	// classifier is bypassed; both fields land in their explicit targets.
	orig := TaskRecord{
		TS:    "2026-06-25T00:00:00Z",
		Event: "delegate_task",
		Tool:  "submit_task",
		TaskID: "task_2",
		SessionRef: agentbackend.SessionRef{
			Backend: "thr-modern",
			Bridge:  "cse_modern",
		},
		ChildSessionRef: agentbackend.SessionRef{
			Backend: "thr-child-modern",
			Bridge:  "cse_child_modern",
		},
		ChildAgentID: "ag-child",
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got TaskRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Backend, Bridge, AgentID survive round-trip. Kind is empty on both sides
	// (driver-side refs never have a Kind source).
	if got.SessionRef.Backend != orig.SessionRef.Backend {
		t.Errorf("SessionRef.Backend = %q, want %q", got.SessionRef.Backend, orig.SessionRef.Backend)
	}
	if got.SessionRef.Bridge != orig.SessionRef.Bridge {
		t.Errorf("SessionRef.Bridge = %q, want %q", got.SessionRef.Bridge, orig.SessionRef.Bridge)
	}
	if got.SessionRef.Kind != "" {
		t.Errorf("SessionRef.Kind should be empty, got %q", got.SessionRef.Kind)
	}
	if got.ChildSessionRef.Backend != orig.ChildSessionRef.Backend {
		t.Errorf("ChildSessionRef.Backend = %q, want %q", got.ChildSessionRef.Backend, orig.ChildSessionRef.Backend)
	}
	if got.ChildSessionRef.Bridge != orig.ChildSessionRef.Bridge {
		t.Errorf("ChildSessionRef.Bridge = %q, want %q", got.ChildSessionRef.Bridge, orig.ChildSessionRef.Bridge)
	}
	if got.ChildSessionRef.AgentID != "ag-child" {
		// ChildAgentID flows back into ChildSessionRef.AgentID during unmarshal
		// because the journal carries it as a separate field.
		t.Errorf("ChildSessionRef.AgentID = %q, want \"ag-child\"", got.ChildSessionRef.AgentID)
	}
	if got.ChildAgentID != orig.ChildAgentID {
		t.Errorf("ChildAgentID = %q, want %q", got.ChildAgentID, orig.ChildAgentID)
	}
}
```

If the test file already exists, ensure these imports are present at top:
```go
import (
	"encoding/json"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)
```

- [ ] **Step 2: Run tests to confirm they fail**

```
go test ./internal/driver/ -run TestRecord_ -count=1
```
Expected: FAIL with `undefined: TaskRecord.SessionRef` / `undefined: TaskRecord.ChildSessionRef` / compile errors.

- [ ] **Step 3: Replace `TaskRecord` struct + add Marshal/UnmarshalJSON**

In `multi-agent/internal/driver/task_journal.go`, replace the existing `TaskRecord` definition:

```go
type TaskRecord struct {
	TS                string                  `json:"-"`
	Event             string                  `json:"-"`
	Tool              string                  `json:"-"`
	TaskID            string                  `json:"-"`
	TargetID          string                  `json:"-"`
	TargetDisplayName string                  `json:"-"`
	Skill             string                  `json:"-"`
	Status            string                  `json:"-"`
	Wait              bool                    `json:"-"`
	TimeoutSec        int                     `json:"-"`
	ChildAgentID      string                  `json:"-"`
	Terminal          bool                    `json:"-"`
	SessionRef        agentbackend.SessionRef `json:"-"`
	ChildSessionRef   agentbackend.SessionRef `json:"-"`
}

// recordWire is the on-disk JSON shape. SessionRef and ChildSessionRef are
// flattened to sibling fields (session_id + bridge_session_id +
// child_session_id + child_bridge_session_id) — encoding/json does not
// flatten nested struct fields into siblings on its own, so the marshal
// path explicitly maps SessionRef.Backend / .Bridge to the wire keys.
//
// Read path uses the bridge-id prefix classifier ("^cse_" → Bridge, else
// Backend) when ONLY the legacy single field is present. Modern rows that
// carry the explicit bridge_session_id sibling bypass the classifier.
type recordWire struct {
	TS                   string `json:"ts"`
	Event                string `json:"event"`
	Tool                 string `json:"tool"`
	TaskID               string `json:"task_id"`
	SessionID            string `json:"session_id,omitempty"`
	BridgeSessionID      string `json:"bridge_session_id,omitempty"`
	TargetID             string `json:"target_id,omitempty"`
	TargetDisplayName    string `json:"target_display_name,omitempty"`
	Skill                string `json:"skill,omitempty"`
	Status               string `json:"status,omitempty"`
	Wait                 bool   `json:"wait"`
	TimeoutSec           int    `json:"timeout_sec,omitempty"`
	ChildSessionID       string `json:"child_session_id,omitempty"`
	ChildBridgeSessionID string `json:"child_bridge_session_id,omitempty"`
	ChildAgentID         string `json:"child_agent_id,omitempty"`
	Terminal             bool   `json:"terminal,omitempty"`
}

// MarshalJSON flattens SessionRef.Backend/.Bridge as sibling JSON fields.
func (r TaskRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(recordWire{
		TS:                   r.TS,
		Event:                r.Event,
		Tool:                 r.Tool,
		TaskID:               r.TaskID,
		SessionID:            r.SessionRef.Backend,
		BridgeSessionID:      r.SessionRef.Bridge,
		TargetID:             r.TargetID,
		TargetDisplayName:    r.TargetDisplayName,
		Skill:                r.Skill,
		Status:               r.Status,
		Wait:                 r.Wait,
		TimeoutSec:           r.TimeoutSec,
		ChildSessionID:       r.ChildSessionRef.Backend,
		ChildBridgeSessionID: r.ChildSessionRef.Bridge,
		ChildAgentID:         r.ChildAgentID,
		Terminal:             r.Terminal,
	})
}

// UnmarshalJSON inflates the wire shape and reconstructs SessionRef values.
// Modern rows: explicit bridge_session_id is present → fields go to their
// explicit targets. Legacy rows: only session_id is present → classifier
// routes ^cse_ to Bridge, anything else to Backend.
func (r *TaskRecord) UnmarshalJSON(data []byte) error {
	var w recordWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	r.TS = w.TS
	r.Event = w.Event
	r.Tool = w.Tool
	r.TaskID = w.TaskID
	r.TargetID = w.TargetID
	r.TargetDisplayName = w.TargetDisplayName
	r.Skill = w.Skill
	r.Status = w.Status
	r.Wait = w.Wait
	r.TimeoutSec = w.TimeoutSec
	r.ChildAgentID = w.ChildAgentID
	r.Terminal = w.Terminal
	r.SessionRef = classifyLegacyID(w.SessionID, w.BridgeSessionID)
	r.ChildSessionRef = classifyLegacyID(w.ChildSessionID, w.ChildBridgeSessionID)
	r.ChildSessionRef.AgentID = w.ChildAgentID
	return nil
}

// classifyLegacyID routes a journal row's id pair into a SessionRef.
//   - If bridge is non-empty, this is a modern row: backend → Backend, bridge → Bridge.
//   - If only id is set: ^cse_ prefix → Bridge, else → Backend (legacy classifier).
//   - If both empty: zero ref.
func classifyLegacyID(id, bridge string) agentbackend.SessionRef {
	if bridge != "" {
		// Modern row: explicit fields take precedence.
		return agentbackend.SessionRef{Backend: id, Bridge: bridge}
	}
	if id == "" {
		return agentbackend.SessionRef{}
	}
	if strings.HasPrefix(id, "cse_") {
		return agentbackend.SessionRef{Bridge: id}
	}
	return agentbackend.SessionRef{Backend: id}
}
```

Add to imports at top of `task_journal.go`:
```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)
```

(`strings` and `agentbackend` are the additions; the others already exist.)

- [ ] **Step 4: Run journal tests**

```
go test ./internal/driver/ -run TestRecord_ -count=1
```
Expected: PASS for all 5 tests.

- [ ] **Step 5: Run full driver suite to catch compile errors in other files that touched `TaskRecord.SessionID` / `.ChildSessionID`**

```
go test ./internal/driver/... -count=1 2>&1 | head -80
```
Expected: COMPILE ERRORS in `tools.go` and `tools_test.go` referencing the old field names. **That is the next task's signal.** Note all the failing file:line references — Task 3 fixes them.

- [ ] **Step 6: Commit**

```
git add multi-agent/internal/driver/task_journal.go multi-agent/internal/driver/task_journal_test.go
git commit -m "feat(driver): typed SessionRef on TaskJournal.Record with legacy classifier (#29 P1.2)

TaskRecord.SessionID / ChildSessionID (bare string) are replaced by
typed SessionRef. JSON wire shape is unchanged on the happy path:
session_id + child_session_id continue to carry the backend id;
new optional bridge_session_id + child_bridge_session_id siblings
carry the bridge id (omitempty).

Legacy reader: rows that have only session_id are classified by
^cse_ prefix — values starting with cse_ go to Bridge, otherwise
Backend. This handles the pre-refactor reality where the driver
wrote bridge ids (resp.SessionID from agentsdk) into session_id.

NOTE: this commit deliberately breaks the build for tools.go and
tools_test.go — those files still reference the removed field
names. They are fixed in the next commit (atomic P1 migration).

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 3: Migrate `internal/driver/tools.go` to `SessionRef` (production-side)

**Files:**
- Modify: `multi-agent/internal/driver/tools.go`

**Interfaces:**
- Consumes: `agentbackend.SessionRef`, `agentbackend.NewBackend`, `agentbackend.NewBridgeOnly`, `TaskRecord.SessionRef`, `TaskRecord.ChildSessionRef`.
- Produces:
  - `delegatedTaskRecord` struct gains `SessionRef agentbackend.SessionRef`.
  - `recordDelegatedTask` writes `rec.SessionRef` into the new `TaskRecord.SessionRef` field.
  - `recordTerminalChild` writes the slave's reported backend id into `rec.ChildSessionRef.Backend` (was a bare string into `ChildSessionID`).
  - `submit_task` response carries both `session_id` (bridge for back-compat) and `bridge_session_id` (new explicit alias).
  - `wait_task` / `get_task` responses split into `session_id` (backend, may be empty) + `bridge_session_id` (bridge, may be empty).
  - `resume_task` validates `kw.SessionID != ""` and returns an actionable `MCPToolError` otherwise; no longer falls back to `info.SessionID`.

- [ ] **Step 1: Update `delegatedTaskRecord` struct and `recordDelegatedTask`**

In `multi-agent/internal/driver/tools.go`, find the existing `delegatedTaskRecord` struct (currently around line 140) and replace:

```go
type delegatedTaskRecord struct {
	Tool              string
	Response          *agentsdk.DelegateTaskResponse
	TargetID          string
	TargetDisplayName string
	ChildAgentID      string
	Skill             string
	Wait              bool
	TimeoutSec        int
	// SessionRef wraps Response.SessionID (the agentserver bridge id) into a
	// typed SessionRef so downstream journal writes preserve the bridge/backend
	// distinction. Driver-side construction: Kind is always empty (no source).
	SessionRef agentbackend.SessionRef
}
```

Find `recordDelegatedTask` (currently ~line 152) and replace the body of the `t.taskJournal.Append(TaskRecord{...})` call to use the typed ref instead of the bare string:

```go
func (t *Tools) recordDelegatedTask(rec delegatedTaskRecord) error {
	if t.taskJournal == nil || rec.Response == nil || rec.Response.TaskID == "" {
		return nil
	}
	if err := t.taskJournal.Append(TaskRecord{
		Tool:              rec.Tool,
		TaskID:            rec.Response.TaskID,
		SessionRef:        rec.SessionRef,
		TargetID:          rec.TargetID,
		TargetDisplayName: rec.TargetDisplayName,
		ChildAgentID:      rec.ChildAgentID,
		Skill:             rec.Skill,
		Status:            rec.Response.Status,
		Wait:              rec.Wait,
		TimeoutSec:        rec.TimeoutSec,
	}); err != nil {
		return &MCPToolError{Message: fmt.Sprintf("task %s was created but driver failed to record it in driver-tasks.jsonl: %v", rec.Response.TaskID, err)}
	}
	return nil
}
```

Now find every place that constructs a `delegatedTaskRecord` (search for `delegatedTaskRecord{`):

```
git grep -n "delegatedTaskRecord{" multi-agent/internal/driver/
```

At each construction site, add a `SessionRef: agentbackend.NewBridgeOnly("", <targetShortID>, resp.SessionID)` line. Example pattern (submit_task site, ~line 670):

```go
if err := s.t.recordDelegatedTask(delegatedTaskRecord{
	Tool:              s.Name(),
	Response:          resp,
	TargetID:          targetID,
	TargetDisplayName: targetName,
	ChildAgentID:      targetShortID,
	Skill:             skill,
	Wait:              false,
	TimeoutSec:        timeout,
	SessionRef:        agentbackend.NewBridgeOnly("", targetShortID, resp.SessionID),
}); err != nil { ... }
```

Apply the same `SessionRef:` line at every `delegatedTaskRecord{` site — they all have `resp` and `targetShortID` in scope at that point. If a call site happens to receive a `resp.SessionID == ""` (which `NewBridgeOnly` would panic on), wrap the call in a guard:

```go
var sessRef agentbackend.SessionRef
if resp.SessionID != "" {
	sessRef = agentbackend.NewBridgeOnly("", targetShortID, resp.SessionID)
}
// ... use sessRef in the struct literal
```

Use the guarded form only at sites where `resp.SessionID` is genuinely optional (none expected today, but defensive).

- [ ] **Step 2: Update `recordTerminalChild`**

Find `recordTerminalChild` (~line 183). Today's signature:
```go
func (t *Tools) recordTerminalChild(taskID, childSessionID, status string)
```

Body sets `rec.ChildSessionID = childSessionID`. Change to set `rec.ChildSessionRef`:

```go
func (t *Tools) recordTerminalChild(taskID, childSessionID, status string) {
	if t.taskJournal == nil || taskID == "" || childSessionID == "" {
		return
	}
	rec, ok := t.taskJournal.LatestByTaskID(taskID)
	if !ok {
		return
	}
	// childSessionID at this site is the BACKEND-NATIVE id reported by the
	// slave's kind marker (sessionIDFromMarker output). Never a bridge id.
	newChildRef := agentbackend.NewBackend("", rec.ChildAgentID, childSessionID)
	if rec.Terminal && rec.ChildSessionRef.Backend == newChildRef.Backend && rec.Status == status {
		return // already recorded this terminal state; idempotent no-op
	}
	rec.ChildSessionRef = newChildRef
	rec.Terminal = true
	rec.TS = "" // let Append stamp a fresh timestamp
	if status != "" {
		rec.Status = status
	}
	if err := t.taskJournal.Append(rec); err != nil {
		t.logHelperErr("driver_journal", "record_terminal_child", err)
	}
}
```

Note the idempotency check now compares `rec.ChildSessionRef.Backend` (was `rec.ChildSessionID`).

- [ ] **Step 3: Fix `submit_task` response (~line 700)**

Find the response builder around line 701. Today's pattern is roughly:
```go
"session_id":          resp.SessionID,
```

Change to:
```go
// session_id keeps the bridge id permanently for back-compat consumers;
// bridge_session_id is the new explicit alias. submit_task is the
// permanent exception to "session_id always means backend" — no backend
// id exists synchronously at dispatch time. Spec §submit_task contract.
"session_id":          resp.SessionID,
"bridge_session_id":   resp.SessionID,
```

Both fields carry the same bridge value.

- [ ] **Step 4: Fix `wait_task` response (~line 781) and `get_task` response (~line 900)**

Find the line:
```go
SessionID:     firstNonEmpty(markerSessionID, info.SessionID),
```

(`markerSessionID` is the local around line 757 of `wait_task`; `sessionIDFromMarker(...)` is called directly in `get_task` at line 900.)

For `wait_task`, replace:
```go
SessionID:     firstNonEmpty(markerSessionID, info.SessionID),
```
with:
```go
SessionID:       markerSessionID,        // backend (empty if marker was absent)
BridgeSessionID: info.SessionID,         // bridge from agentsdk
```

The containing response struct (search for the struct definition around line 770 — declared inline as `var resp struct { ... }`) needs a new field. Find the inline declaration:

```go
var resp struct {
	Status        string          `json:"status"`
	IsFinal       bool            `json:"is_final"`
	Output        string          `json:"output,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	TargetID      string          `json:"target_id,omitempty"`
	// ... other fields ...
}
```

Add the new field next to `SessionID`:
```go
	SessionID       string          `json:"session_id,omitempty"`
	BridgeSessionID string          `json:"bridge_session_id,omitempty"`
```

Repeat the same change in `get_task` response (~line 900) — the inline struct should also be extended.

- [ ] **Step 5: Rewrite `resume_task` (~line 1290)**

Find:
```go
// kw.SessionID is the slave codex thread id (what chat_resume must target);
// info.SessionID is agentserver's task-bridge `cse_<uuid>` and would resume
// the wrong session. Prefer the marker; info.SessionID is only a defensive
// fallback for environments where the wrapper was missing (e.g. observer
// disabled AND the slave's TaskInfo.Output also lost it).
sessionID := firstNonEmpty(kw.SessionID, info.SessionID)
if sessionID == "" {
	return nil, &MCPToolError{Message: "missing session_id; cannot resume"}
}
```

Replace with:
```go
// kw.SessionID is the slave's backend-native id (codex thread / claude
// session / opencode session) extracted from the slave's kind marker.
// Falling back to info.SessionID would silently delegate chat_resume with
// the agentserver bridge id, which the backend cannot resume against —
// this was the issue #29 bug. The fix: error out instead of guessing.
if kw.SessionID == "" {
	return nil, &MCPToolError{Message: "resume failed: slave never reported a backend session id; bridge id alone cannot resume a codex/claude conversation"}
}
slaveShortID := ""
if prev, ok := r.t.taskJournal.LatestByTaskID(args.LastTaskID); ok {
	slaveShortID = prev.ChildAgentID
}
// Driver has no backend-kind source (agentsdk.AgentCard carries no Kind);
// SessionRef.Kind stays empty. NewBackend only takes the backend id, so
// this construction site cannot accidentally use info.SessionID (bridge).
ref := agentbackend.NewBackend("", slaveShortID, kw.SessionID)
sessionID := ref.Backend
_ = ref // SessionRef constructed for type-discipline; wire JSON carries the plain string below.
```

(The local `sessionID` variable is preserved so the existing `json.Marshal(map[string]string{...})` body below stays unchanged. The `_ = ref` is intentional — codify the type discipline even though the agentsdk JSON crosses the wire as a string.)

If the linter complains about `_ = ref`, drop that line and just leave the construction effect: the value of `ref.Backend` is what gets used; `ref` itself does not need to be referenced again at runtime.

- [ ] **Step 6: Build the package**

```
cd multi-agent
go vet ./internal/driver/
```
Expected: no compile errors in `tools.go`. Test file (`tools_test.go`) will still error — fixed in Task 4.

- [ ] **Step 7: Commit**

```
git add multi-agent/internal/driver/tools.go
git commit -m "feat(driver): migrate tools.go to typed SessionRef (#29 P1.3)

Five touch sites:
- delegatedTaskRecord.SessionRef holds the bridge wrap from agentsdk
  (NewBridgeOnly), persisted into TaskRecord.SessionRef.
- recordTerminalChild writes child backend id into
  TaskRecord.ChildSessionRef.Backend (was ChildSessionID string).
- submit_task response emits both session_id (bridge, back-compat)
  and bridge_session_id (new explicit alias).
- wait_task / get_task responses split: session_id (backend, may be
  empty) + bridge_session_id (bridge, may be empty). No more
  firstNonEmpty(marker, bridge) fallback.
- resume_task validates kw.SessionID != \"\" with actionable error;
  no longer falls back to info.SessionID (bridge id) — this is the
  #29 bug fix.

tools_test.go still errors against this change; fixed in next commit
(planned migration to atomic deliverable).

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 4: Migrate `internal/driver/tools_test.go` fixtures to typed `SessionRef`

**Files:**
- Modify: `multi-agent/internal/driver/tools_test.go`

**Interfaces:**
- Consumes: `TaskRecord.SessionRef`, `TaskRecord.ChildSessionRef`, `delegatedTaskRecord.SessionRef`, response builders with the new dual `session_id` + `bridge_session_id` shape.
- Produces: green `go test ./internal/driver/...`.

- [ ] **Step 1: Identify the compile errors**

Run:
```
cd multi-agent
go test ./internal/driver/... -count=1 2>&1 | grep -E "(undefined|cannot use|unknown field)" | head -40
```
Expected output: list of lines referencing `TaskRecord{SessionID:`, `TaskRecord{ChildSessionID:`, `.SessionID`, `.ChildSessionID` on TaskRecord values, and any direct struct construction with the old field names. Make a checklist.

- [ ] **Step 2: Mechanical migration of fixtures**

For each compile error pointing into `tools_test.go`:

1. **`TaskRecord{SessionID: "x"}`** → **`TaskRecord{SessionRef: agentbackend.SessionRef{Backend: "x"}}`** (or `Bridge: "x"` if the original test data was a `cse_…` value).
2. **`TaskRecord{ChildSessionID: "x"}`** → **`TaskRecord{ChildSessionRef: agentbackend.SessionRef{Backend: "x"}}`**.
3. **`record.SessionID`** (read) → **`record.SessionRef.Backend`** (or `.Bridge` based on test intent).
4. **`record.ChildSessionID`** (read) → **`record.ChildSessionRef.Backend`**.

Add `agentbackend` to imports if missing:
```go
import (
	// existing imports
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)
```

- [ ] **Step 3: Update response assertions for `submit_task` / `wait_task` / `get_task`**

For tests that decode `submit_task` / `wait_task` / `get_task` responses and assert on `session_id`:

- `submit_task`: today the test likely asserts `session_id == resp.SessionID`. That assertion is still valid (back-compat). If the test only asserts `session_id`, it stays unchanged.
- `wait_task` / `get_task`: if a test asserts `session_id == <expected_bridge>` when the marker was absent in the fixture, change the assertion to either:
  - Expect `session_id == ""` and `bridge_session_id == <expected_bridge>`, OR
  - If the test was checking the actual marker session id, expect `session_id == <marker_value>` (unchanged behavior).
  Use the test setup (whether a kind marker fixture was emitted) as the cue.

- [ ] **Step 4: Build + run tests**

```
go vet ./internal/driver/
go test ./internal/driver/... -count=1
```
Expected: PASS for all existing tests.

If tests fail because the assertion expected the old `firstNonEmpty` behavior, the test was encoding the bug. Fix the assertion to match the new wire contract (per the global-constraints "Wire format additive-only" exception list).

- [ ] **Step 5: Commit**

```
git add multi-agent/internal/driver/tools_test.go
git commit -m "test(driver): migrate tools_test.go fixtures to typed SessionRef (#29 P1.4)

Mechanical migration of existing fixtures:
- TaskRecord literals use SessionRef / ChildSessionRef instead of
  removed SessionID / ChildSessionID fields.
- Reads of record.SessionID become record.SessionRef.Backend (or
  .Bridge where the original test intent was a cse_ value).
- Response assertions for wait_task / get_task updated where the
  test had encoded the firstNonEmpty fallback semantics — those
  fixtures previously asserted bridge-on-marker-absent; now the
  response splits session_id (backend, may be empty) from
  bridge_session_id (explicit bridge alias).

go test ./internal/driver/... is green at this commit.

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 5: Bug-fix proof test — `TestResumeTask_RefusesEmptyMarker`

**Files:**
- Modify: `multi-agent/internal/driver/tools_test.go`

**Interfaces:**
- Consumes: existing test helpers for constructing a fake `Tools`, `fakeSDK`, etc.
- Produces: a regression test that fails in the pre-P1 codebase and passes in the post-P1 codebase. Codifies the #29 bug fix.

- [ ] **Step 1: Write the test**

Append to `multi-agent/internal/driver/tools_test.go`:

```go
// TestResumeTask_RefusesEmptyMarker codifies the #29 fix: when the slave's
// kind marker did NOT carry a backend session id, resume_task must NOT fall
// back to info.SessionID (the agentserver bridge cse_ id) — that would
// delegate chat_resume against a bridge id, which the backend cannot match.
// Instead it returns an actionable error so the operator sees the failure
// at the source, not buried inside the slave's "session not found" later.
func TestResumeTask_RefusesEmptyMarker(t *testing.T) {
	const (
		taskID    = "task_test_29"
		bridgeID  = "cse_fake_bridge_id"
		slaveID   = "ag-slave"
		targetID  = "sandbox-target"
	)

	// Set up: prior task exists in agentserver, but its TaskInfo.Output /
	// .Result lacks a kind marker session_id. info.SessionID is the bridge.
	sdk := &fakeSDK{
		getTaskByID: map[string]*agentsdk.TaskInfo{
			taskID: {
				TaskID:    taskID,
				Status:    "completed",
				SessionID: bridgeID, // bridge id — must NOT be used for resume
				TargetID:  targetID,
				// Result is a valid awaiting_user payload with NO session_id.
				Result: json.RawMessage(`{"kind":"awaiting_user","question":{"kind":"ask_user"}}`),
			},
		},
	}
	tools := newTestTools(t, sdk)
	// Pre-populate the journal so resume_task can recover slaveShortID.
	_ = tools.taskJournal.Append(TaskRecord{
		Tool:         "submit_task",
		TaskID:       taskID,
		TargetID:     targetID,
		ChildAgentID: slaveID,
	})
	// Driver must be bound for resume_task to pass the bind guard.
	tools.parentThread.Store(strPtr("019ef000-0000-0000-0000-000000000000"))

	r := &resumeTaskTool{t: tools}
	argsRaw := json.RawMessage(`{"last_task_id":"task_test_29","answer":"y"}`)
	_, err := r.Call(context.Background(), argsRaw)
	if err == nil {
		t.Fatal("expected error when slave marker has no backend session id; got nil")
	}
	if mce, ok := err.(*MCPToolError); !ok || !strings.Contains(mce.Message, "slave never reported a backend session id") {
		t.Fatalf("expected actionable bridge-fallback error, got: %v", err)
	}

	// CRITICAL: assert sdk.DelegateTask was NOT called with chat_resume +
	// the bridge id (the pre-PR buggy behavior).
	for _, call := range sdk.delegateCalls {
		if call.Skill == "chat_resume" {
			t.Errorf("expected zero chat_resume delegations; got one with session_id in prompt body. Prompt: %s", call.Prompt)
		}
	}
}

func strPtr(s string) *string { return &s }
```

Notes:
- `fakeSDK.getTaskByID` and `delegateCalls` — verify they exist in the existing test fixture (`tools_test.go`). If they don't, adapt to whatever fields the existing fixture exposes (e.g. `fakeSDK.getTaskFn func(id string) (*agentsdk.TaskInfo, error)` and a slice of captured `DelegateTaskRequest`).
- `tools.parentThread` is the bind-thread state from PR #31; tests in this file already set it via similar patterns — match the existing convention.
- `strPtr` is a local helper because `atomic.Pointer[string].Store` takes a `*string`.

If `fakeSDK` shape in this repo doesn't match the names above, look at the most recent existing test (e.g. `TestResumeTaskHappy`) to see the right pattern; rewrite the test using the same helpers but with the empty-marker fixture.

- [ ] **Step 2: Run the test**

```
cd multi-agent
go test ./internal/driver/ -run TestResumeTask_RefusesEmptyMarker -count=1 -v
```
Expected: PASS.

- [ ] **Step 3: Sanity check — temporarily revert the fix and re-run**

To prove the test catches the pre-fix bug:
1. In `tools.go::resumeTaskTool.Call`, temporarily revert the rewrite to `sessionID := firstNonEmpty(kw.SessionID, info.SessionID)`.
2. Run `go test ./internal/driver/ -run TestResumeTask_RefusesEmptyMarker -count=1 -v`.
3. Expected: FAIL — the test catches the bug.
4. Revert your revert (re-apply the fix).
5. Re-run: PASS.

Do NOT commit the revert. This step is a sanity check only.

- [ ] **Step 4: Commit**

```
git add multi-agent/internal/driver/tools_test.go
git commit -m "test(driver): TestResumeTask_RefusesEmptyMarker — proves #29 fix (#29 P1.5)

Constructs a TaskInfo with a marker payload that lacks session_id
and info.SessionID = \"cse_fake_bridge_id\". Pre-PR the resume_task
firstNonEmpty fallback would delegate chat_resume with the bridge id,
which the backend cannot resume. Post-PR (Task 3 rewrite) resume_task
returns an actionable error and does NOT call DelegateTask.

Sanity-checked: reverting the Task 3 rewrite locally makes this test
fail; re-applying makes it pass.

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 6: New test — `TestWaitTask_BridgeAndBackendBothInResponse`

**Files:**
- Modify: `multi-agent/internal/driver/tools_test.go`

**Interfaces:**
- Consumes: existing wait_task test helpers.
- Produces: asserts the new sibling `bridge_session_id` field is present in `wait_task` responses alongside the existing `session_id`.

- [ ] **Step 1: Write the test**

Append to `multi-agent/internal/driver/tools_test.go`:

```go
// TestWaitTask_BridgeAndBackendBothInResponse asserts that wait_task emits
// both session_id (backend, from the kind marker) and bridge_session_id
// (from agentserver's TaskInfo) as sibling fields. Pre-PR the response
// carried only session_id with firstNonEmpty(marker, bridge) semantics —
// consumers couldn't distinguish which they got. This test pins the new
// explicit two-field shape.
func TestWaitTask_BridgeAndBackendBothInResponse(t *testing.T) {
	const (
		taskID    = "task_test_wait"
		bridgeID  = "cse_bridge_for_wait"
		backendID = "019ef000-0000-0000-0000-000000000abc"
	)
	sdk := &fakeSDK{
		getTaskByID: map[string]*agentsdk.TaskInfo{
			taskID: {
				TaskID:    taskID,
				Status:    "completed",
				SessionID: bridgeID,
				Output:    `{"kind":"final","session_id":"` + backendID + `","summary":"done"}`,
			},
		},
	}
	tools := newTestTools(t, sdk)
	w := &waitTaskTool{t: tools}
	argsRaw := json.RawMessage(`{"task_id":"task_test_wait","timeout_sec":1}`)
	resp, err := w.Call(context.Background(), argsRaw)
	if err != nil {
		t.Fatalf("wait_task: %v", err)
	}
	var decoded struct {
		SessionID       string `json:"session_id"`
		BridgeSessionID string `json:"bridge_session_id"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		t.Fatalf("decode response: %v\nraw: %s", err, resp)
	}
	if decoded.SessionID != backendID {
		t.Errorf("session_id = %q, want %q (backend from kind marker)", decoded.SessionID, backendID)
	}
	if decoded.BridgeSessionID != bridgeID {
		t.Errorf("bridge_session_id = %q, want %q (bridge from agentsdk)", decoded.BridgeSessionID, bridgeID)
	}
}
```

(Adapt `fakeSDK.getTaskByID` and helper names to match the actual fixture in this repo; pattern same as Task 5.)

- [ ] **Step 2: Run the test**

```
cd multi-agent
go test ./internal/driver/ -run TestWaitTask_BridgeAndBackendBothInResponse -count=1 -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```
git add multi-agent/internal/driver/tools_test.go
git commit -m "test(driver): wait_task carries both session_id and bridge_session_id (#29 P1.6)

Asserts the new explicit two-field shape. Pre-PR a single session_id
was populated via firstNonEmpty(marker, bridge); post-PR session_id
is the backend id (may be empty), bridge_session_id is the bridge id.

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 7: Full repo test + commit P1 wrap

**Files:** none (verification + commit)

- [ ] **Step 1: Run the full repo test suite**

```
cd multi-agent
go vet ./...
go test ./... -count=1
```
Expected: ALL PASS. If a test outside `internal/driver/` fails because it constructed a `TaskRecord` with the old field names (unlikely — only `internal/driver/` uses it directly), fix that test too with the same migration pattern from Task 4 and amend the relevant prior commit.

- [ ] **Step 2: Push the branch and open P1 PR**

```
cd multi-agent
git push -u origin worktree-bridge-vs-backend-session-id
```

Open PR titled: `feat: SessionRef typed bridge/backend session ref + driver migration (P1, #29)`. Body should include:
- Link to spec `docs/superpowers/specs/2026-06-24-bridge-vs-backend-session-id-design.md`.
- Reproducer for the #29 bug: pre-PR `TestResumeTask_RefusesEmptyMarker` fails because resume_task uses bridge id; post-PR it passes.
- Note: this is P1; P2 (signature change on `Backend.RunResume`) is a follow-up PR that depends on this one.

P1 is operationally complete after this PR merges. P2 (Tasks 8–13) is a follow-up.

---

## P2 — Promote `SessionRef` into `Backend.RunResume`

> **Strict ordering:** P2 depends on the `SessionRef` type from P1 having merged. Open the P2 PR only after P1 lands on master, OR open in parallel and rebase before requesting CI green.

### Task 8: Change `Backend.RunResume` interface + ripple to all backend implementations

**Files:**
- Modify: `multi-agent/pkg/agentbackend/backend.go` (the `Backend` interface)
- Modify: `multi-agent/pkg/agentbackend/backend_test.go` (`nilBackend` test stub)
- Modify: `multi-agent/pkg/agentbackend/codex/executor.go`, `backend.go`, `appserver_worker.go` (signature + guard)
- Modify: `multi-agent/pkg/agentbackend/claude/executor.go`, `backend.go` (signature + guard)
- Modify: `multi-agent/pkg/agentbackend/opencode/executor.go`, `backend.go` (signature + guard)

**Interfaces:**
- Consumes: `SessionRef`, `SessionRef.HasBackend()`, `SessionRef.Backend`.
- Produces: new interface
  ```go
  RunResume(ctx context.Context, ref SessionRef, answer string, sink Sink) (Result, error)
  ```
  Every implementation MUST start with `if !ref.HasBackend() { return Result{}, fmt.Errorf("RunResume: …") }`.

- [ ] **Step 1: Update the `Backend` interface**

In `multi-agent/pkg/agentbackend/backend.go`, find the `RunResume` line in the `Backend` interface (currently `~line 38`):

```go
// Before:
RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
// After:
RunResume(ctx context.Context, ref SessionRef, answer string, sink Sink) (Result, error)
```

- [ ] **Step 2: Update `nilBackend` test stub**

In `multi-agent/pkg/agentbackend/backend_test.go`, find the `nilBackend.RunResume` definition:
```go
func (nilBackend) RunResume(_ context.Context, _, _ string, _ Sink) (Result, error) { ... }
```
Change to:
```go
func (nilBackend) RunResume(_ context.Context, _ SessionRef, _ string, _ Sink) (Result, error) { ... }
```

- [ ] **Step 3: Update each backend implementation (codex / claude / opencode)**

For each backend, two files: `executor.go` (the real impl) and `backend.go` (a thin pass-through wrapper). Pattern (codex shown):

`multi-agent/pkg/agentbackend/codex/executor.go::*Executor.RunResume`:

```go
// Before:
func (e *Executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	// ... body using sessionID
}

// After:
func (e *Executor) RunResume(ctx context.Context, ref agentbackend.SessionRef, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	if !ref.HasBackend() {
		return agentbackend.Result{}, fmt.Errorf("codex.RunResume: SessionRef has no backend id (Bridge=%q); cannot resume backend session", ref.Bridge)
	}
	sessionID := ref.Backend
	// ... rest of body unchanged
}
```

`multi-agent/pkg/agentbackend/codex/backend.go::*Backend.RunResume`:

```go
// Before:
func (b *Backend) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	workDir := /* existing logic */
	return b.executorForWorkDir(workDir).RunResume(ctx, sessionID, answer, sink)
}

// After:
func (b *Backend) RunResume(ctx context.Context, ref agentbackend.SessionRef, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	if !ref.HasBackend() {
		return agentbackend.Result{}, fmt.Errorf("codex.Backend.RunResume: SessionRef has no backend id (Bridge=%q); cannot resume", ref.Bridge)
	}
	workDir := /* existing logic */
	return b.executorForWorkDir(workDir).RunResume(ctx, ref, answer, sink)
}
```

`multi-agent/pkg/agentbackend/codex/appserver_worker.go` — fallback caller wraps to `SessionRef`. Find the call to `RunResume`:

```go
// Before:
return e.RunResume(ctx, sessionID, answer, sink)
// After (sessionID at this call site IS the backend id already — it came from
// app-server's own state):
return e.RunResume(ctx, agentbackend.NewBackend(e.kind, "", sessionID), answer, sink)
```

(Use `e.kind` if the executor has it; otherwise use the package constant `agentbackend.KindCodex`.)

Repeat for `claude/` and `opencode/` (no `appserver_worker.go` equivalent for those — only `executor.go` + `backend.go`).

- [ ] **Step 4: Build + ensure package compiles in isolation**

```
cd multi-agent
go vet ./pkg/agentbackend/...
go build ./pkg/agentbackend/...
```
Expected: PASS. (Test files within `pkg/agentbackend/*/*_test.go` may still error — Task 9 fixes them; the build verifies production code is consistent.)

- [ ] **Step 5: Commit**

```
git add multi-agent/pkg/agentbackend/backend.go multi-agent/pkg/agentbackend/backend_test.go multi-agent/pkg/agentbackend/codex/executor.go multi-agent/pkg/agentbackend/codex/backend.go multi-agent/pkg/agentbackend/codex/appserver_worker.go multi-agent/pkg/agentbackend/claude/executor.go multi-agent/pkg/agentbackend/claude/backend.go multi-agent/pkg/agentbackend/opencode/executor.go multi-agent/pkg/agentbackend/opencode/backend.go
git commit -m "feat(agentbackend): Backend.RunResume takes SessionRef (#29 P2.1)

Promotes the typed reference into the interface so every backend
implementation gets the bridge-vs-backend distinction enforced by
the compiler. Every implementation now starts with a HasBackend()
guard that returns an actionable error if the caller passes a
bridge-only ref. nilBackend test stub signature updated to match.

Test files still error against this signature change — fixed in
the next commit (Task 9).

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 9: Update backend test stubs/assertions in lockstep

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/executor_test.go` (~4 call sites)
- Modify: `multi-agent/pkg/agentbackend/codex/backend_resume_test.go`
- Modify: `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go` (assertions)
- Modify: `multi-agent/pkg/agentbackend/claude/executor_test.go`
- Modify: `multi-agent/pkg/agentbackend/claude/backend_resume_test.go`
- Modify: `multi-agent/pkg/agentbackend/opencode/executor_test.go`
- Modify: `multi-agent/pkg/agentbackend/opencode/backend_resume_test.go` (~2 call sites)
- Modify: `multi-agent/internal/commanderhub/proxy_test.go` (fake backend stub)

**Interfaces:**
- Consumes: new `Backend.RunResume(ctx, ref, answer, sink)` signature.
- Produces: green `go test ./pkg/agentbackend/...` + `go test ./internal/commanderhub/...`.

- [ ] **Step 1: Inventory remaining test compile errors**

```
cd multi-agent
go test ./pkg/agentbackend/... ./internal/commanderhub/... -count=1 2>&1 | grep -E "(cannot use|too many arguments|too few arguments)" | head -30
```
Note every file:line.

- [ ] **Step 2: Update test call sites**

For each `ex.RunResume(ctx, "literal-id", ...)` call:

```go
// Before:
_, err := ex.RunResume(context.Background(), "thr-1", "continue", &captureSink{})
// After:
_, err := ex.RunResume(context.Background(), agentbackend.NewBackend(agentbackend.KindCodex, "", "thr-1"), "continue", &captureSink{})
```

Use the package's own `Kind` constant — `agentbackend.KindCodex`, `agentbackend.KindClaude`, `agentbackend.KindOpencode`. If those constants don't exist (verify via grep), use the package's literal: `"codex"` / `"claude"` / `"opencode"`.

For `b.RunResume(...)` (backend-level test):
```go
_, err := b.RunResume(context.Background(), agentbackend.NewBackend(agentbackend.KindCodex, "", id), "continue", &captureSink{})
```

For `appserver_worker_test.go` assertions of the form `if id, answer := …, …; id != …`:
```go
// Before:
if id, answer := <call args>; id != "thr-1" { ... }
// After (the parameter slot is now ref):
if ref, answer := <call args>; ref.Backend != "thr-1" { ... }
```

For test stubs that implement `Backend` (fake/mock types in `internal/commanderhub/proxy_test.go`):
```go
// Before:
func (f *fakeBackend) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) { ... }
// After:
func (f *fakeBackend) RunResume(ctx context.Context, ref agentbackend.SessionRef, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	// In the body, use ref.Backend wherever sessionID was used.
	...
}
```

- [ ] **Step 3: Add negative-coverage tests for the `!ref.HasBackend()` guard**

For each backend (codex / claude / opencode), add ONE test to `*_test.go` that proves the guard returns an error and does NOT touch the underlying CLI. Pattern (codex shown):

```go
// TestExecutor_RunResume_RejectsBridgeOnlyRef proves the !ref.HasBackend()
// guard returns an actionable error when called with a bridge-only ref,
// without launching the codex CLI. Per spec §RunResume implementations
// MUST reject bridge-only / zero refs.
func TestExecutor_RunResume_RejectsBridgeOnlyRef(t *testing.T) {
	ex := newTestExecutor(t) // existing helper in this file
	bridgeOnly := agentbackend.NewBridgeOnly(agentbackend.KindCodex, "ag-1", "cse_test_bridge")
	_, err := ex.RunResume(context.Background(), bridgeOnly, "answer", &captureSink{})
	if err == nil {
		t.Fatal("expected error when ref has no Backend; got nil")
	}
	if !strings.Contains(err.Error(), "has no backend id") {
		t.Errorf("error should explain missing backend id, got: %v", err)
	}
}
```

If the existing tests have a helper like `newTestExecutor` use it; otherwise adapt to whatever the package's existing executor-construction pattern is.

Add the symmetric test in `*_test.go` for each backend.

- [ ] **Step 4: Run the suites**

```
cd multi-agent
go vet ./...
go test ./pkg/agentbackend/... -count=1
go test ./internal/commanderhub/... -count=1
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add multi-agent/pkg/agentbackend/codex/executor_test.go multi-agent/pkg/agentbackend/codex/backend_resume_test.go multi-agent/pkg/agentbackend/codex/appserver_worker_test.go multi-agent/pkg/agentbackend/claude/executor_test.go multi-agent/pkg/agentbackend/claude/backend_resume_test.go multi-agent/pkg/agentbackend/opencode/executor_test.go multi-agent/pkg/agentbackend/opencode/backend_resume_test.go multi-agent/internal/commanderhub/proxy_test.go
git commit -m "test(agentbackend): migrate RunResume call sites to SessionRef (#29 P2.2)

Updates every test that constructs SessionRef literals or implements
the Backend interface stub. Each backend gets a new negative-coverage
test (TestExecutor_RunResume_RejectsBridgeOnlyRef) that proves the
!ref.HasBackend() guard returns an actionable error without launching
the CLI.

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 10: Wrap at `cmd/slave-agent/resumeAdapter` seam

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go` (the `resumeAdapter` adapter)

**Interfaces:**
- Consumes: `executor.ResumeBackend` (kept bare-string), `agentbackend.Backend.RunResume(SessionRef, ...)` (new signature).
- Produces: `resumeAdapter.RunResume(ctx, sid string, ans string, s executor.Sink)` (signature unchanged) wraps `sid` into a `SessionRef` and calls `a.b.RunResume(ctx, ref, ans, s)`. Validates `sid != ""` before wrapping.

- [ ] **Step 1: Locate and patch `resumeAdapter`**

In `multi-agent/cmd/slave-agent/main.go`, find the `resumeAdapter` declaration (around line 466):

```go
// Before:
type resumeAdapter struct{ b agentbackend.Backend }

func (a resumeAdapter) RunResume(ctx context.Context, sid, ans string, s executor.Sink) (executor.Result, error) {
	return a.b.RunResume(ctx, sid, ans, s)
}
```

Replace the body:

```go
type resumeAdapter struct{ b agentbackend.Backend }

// RunResume is the seam between internal/executor.ResumeBackend (bare string,
// permanently — see spec §"Why ResumeBackend stays string") and the typed
// agentbackend.Backend.RunResume(SessionRef) above the seam. The slave's
// ChatResumeExecutor passes the slave's own backend-native session id (sourced
// from the slave's kind marker output), so the wrap can populate
// SessionRef.Backend with confidence; Kind comes from the Backend interface,
// AgentID is empty (single-backend seam, no cross-agent disambiguation).
func (a resumeAdapter) RunResume(ctx context.Context, sid, ans string, s executor.Sink) (executor.Result, error) {
	if sid == "" {
		return executor.Result{}, fmt.Errorf("resumeAdapter: empty session id; cannot resume")
	}
	ref := agentbackend.NewBackend(a.b.Kind(), "", sid)
	return a.b.RunResume(ctx, ref, ans, s)
}
```

Ensure `fmt` is imported at the top of `main.go` (likely already is).

- [ ] **Step 2: Build the binary**

```
cd multi-agent
go build ./cmd/slave-agent/
```
Expected: success.

- [ ] **Step 3: Run the slave-agent suite (if any)**

```
go test ./cmd/slave-agent/... -count=1
```
Expected: PASS (no tests in this dir likely; just verifies compile).

- [ ] **Step 4: Commit**

```
git add multi-agent/cmd/slave-agent/main.go
git commit -m "fix(slave-agent): resumeAdapter wraps string→SessionRef at backend seam (#29 P2.3)

The seam between internal/executor.ResumeBackend (string, stays this
way to avoid an import cycle) and the now-typed
agentbackend.Backend.RunResume(SessionRef). Validates sid != \"\"
before wrapping (returning an error rather than letting NewBackend
panic on empty input from a higher-level caller).

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 11: Wrap at `internal/commander/handler.go` call sites

**Files:**
- Modify: `multi-agent/internal/commander/handler.go` (~lines 61 + 65)
- Modify: `multi-agent/internal/commander/handler_test.go` (any tests calling RunResume directly)

**Interfaces:**
- Consumes: `agentbackend.NewBackend`, `agentbackend.Backend.Kind()`.
- Produces: `commander.Handler.SessionTurn` validates `id != ""` and wraps as `NewBackend(h.Backend.Kind(), "", id)`.

- [ ] **Step 1: Patch the two call sites**

In `multi-agent/internal/commander/handler.go`, find the two `h.Backend.RunResume(ctx, id, prompt, sink)` calls (around lines 61 + 65). For each:

```go
// Before:
return h.Backend.RunResume(ctx, id, prompt, sink)

// After:
if id == "" {
	return executor.Result{}, fmt.Errorf("commander: empty session id; cannot resume")
}
return h.Backend.RunResume(ctx, agentbackend.NewBackend(h.Backend.Kind(), "", id), prompt, sink)
```

`agentbackend` likely already imported in this file (the `Backend` field type). `executor` (for `Result`) likely already imported too. Verify by reading the imports block.

- [ ] **Step 2: Update handler tests**

In `handler_test.go`, find any test that asserts `mockBackend.RunResume(ctx, "id", ...)` was called with a specific session id. Change to:
```go
// Before:
if gotID != "expected-id" { ... }
// After:
if gotRef.Backend != "expected-id" { ... }
```

And update any `fakeBackend.RunResume` stub signature to take `SessionRef`.

- [ ] **Step 3: Run the suite**

```
cd multi-agent
go test ./internal/commander/... -count=1
```
Expected: PASS.

- [ ] **Step 4: Commit**

```
git add multi-agent/internal/commander/handler.go multi-agent/internal/commander/handler_test.go
git commit -m "fix(commander): SessionTurn validates+wraps id into SessionRef (#29 P2.4)

Both Backend.RunResume call sites in handler.go now validate id != \"\"
and wrap into NewBackend(h.Backend.Kind(), \"\", id) before calling
the typed interface. Empty AgentID is intentional — Handler serves
only its own agent's sessions; no cross-agent disambiguation needed.
Test stubs updated to match.

Issue #29.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
"
```

---

### Task 12: Full repo regression test + commit P2 wrap

**Files:** none (verification + commit)

- [ ] **Step 1: Run the full suite**

```
cd multi-agent
go vet ./...
go test ./... -count=1
```
Expected: ALL PASS. Investigate and fix any failures inline (signature mismatches in stubs/mocks the inventory may have missed; the spec's round-2 inventory list is comprehensive but a `grep` rescan won't hurt).

- [ ] **Step 2: Final `grep` audit — anywhere else still calling `RunResume(ctx, <string>, ...)`?**

```
cd multi-agent
git grep -nE "\.RunResume\(ctx[^)]*, ?\"" -- '*.go'
```
Expected: zero hits. If anything turns up, it's an overlooked stub — fix it with the same wrap pattern.

- [ ] **Step 3: Push branch**

```
git push origin worktree-bridge-vs-backend-session-id
```

Open PR titled: `feat: SessionRef in Backend.RunResume interface (P2, #29)`. Body must call out:
- Depends on P1 having merged.
- Backend.RunResume signature change — breaking for any external implementor (none expected).
- Negative-test coverage for the `!ref.HasBackend()` guard in each backend.

P2 complete.

---

## Self-Review

### Spec coverage

- ✅ `SessionRef` type introduced (Task 1).
- ✅ JSON ownership on `TaskRecord` not on `SessionRef` (Task 2).
- ✅ Legacy `^cse_` classifier on read (Task 2 test 2 + 3 + 4).
- ✅ Driver-side `Kind=""` everywhere (Tasks 3 + 5: `NewBackend("", ...)` literally typed).
- ✅ `submit_task.session_id` permanent exception + new `bridge_session_id` sibling (Task 3 Step 3).
- ✅ `wait_task` / `get_task` split (Task 3 Step 4).
- ✅ `resume_task` bug fix — actionable error, no bridge fallback (Task 3 Step 5).
- ✅ Bug-fix proof test (Task 5).
- ✅ Two-field response test (Task 6).
- ✅ `Backend.RunResume(SessionRef)` interface change (Task 8).
- ✅ `!ref.HasBackend()` guard in every impl + wrapper (Task 8 Step 3, with negative-test coverage in Task 9 Step 3).
- ✅ `executor.ResumeBackend` stays bare string; `resumeAdapter` is the seam (Task 10).
- ✅ Commander validates+wraps (Task 11).
- ✅ Test stub migration for all 23 inventory sites (Tasks 9 + 11).

### Placeholder scan

- No "TBD" / "TODO" / "implement later".
- Every step has either a code block or an exact command + expected output.
- Each test step has the actual test code, not "write tests for…".

### Type consistency

- `SessionRef` field names (`Backend`, `Bridge`, `Kind`, `AgentID`) used consistently across all tasks.
- `NewBackend(kind Kind, agentID, backendID string)` parameter order matches between Task 1 declaration, Task 3 caller, Task 5 test, Task 8 impl, Task 10 caller, Task 11 caller.
- `TaskRecord.SessionRef` field name (not `Session` or `Ref`) consistent between Task 2 declaration and Task 3 / 4 usage.
- `TaskRecord.ChildSessionRef` similarly consistent.
- `MarshalJSON` / `UnmarshalJSON` exist on `TaskRecord` only (Task 2); never claimed on `SessionRef`.
- `executor.ResumeBackend` (string) and `agentbackend.Backend.RunResume(SessionRef)` are correctly differentiated wherever both are mentioned.
