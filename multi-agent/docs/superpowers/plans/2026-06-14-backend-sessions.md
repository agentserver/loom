# Backend.ListSessions / GetSession Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add read-side session API to `agentbackend.Backend` (`ListSessions` + `GetSession`) and implement it for claude (jsonl), codex (jsonl under year/month/day), and opencode (sqlite). This is PR-1 of the 3-PR commander-web-entry vision and unblocks daemon (PR-2) which needs a single interface across all backends.

**Architecture:** Each backend reads its CLI's own session persistence directly — **no subprocess invocation, no network**. claude scans `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`. codex scans `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl`. opencode queries `~/.local/share/opencode/opencode.db` (sqlite). Returns vendor-agnostic `Session` / `[]SessionMessage` structs.

**Tech Stack:** Go stdlib + `modernc.org/sqlite` (already in go.mod or needs adding — verify; if needs adding, use it pure-Go to keep CGO_ENABLED=0 builds working).

**Spec:** `docs/superpowers/specs/2026-06-14-backend-sessions-design.md` (committed `987c36d`).

**Vision:** `docs/vision/commander-web-entry.md` (committed `3501189`). This PR is PR-1 of 3.

**Branch:** `worktree-backend-sessions`. Worktree: `/root/multi-agent/.claude/worktrees/backend-sessions/multi-agent/`.

**Master-path freeze:** all changes confined to `pkg/agentbackend/`. Verify after Task 6:
```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
# expected: empty
```

---

## Pre-flight findings (already done by plan author)

Implementer does NOT need to re-probe — these were captured on this host on 2026-06-15.

### claude

- Storage: `~/.claude/projects/<encoded_cwd>/<session_uuid>.jsonl`
- `<encoded_cwd>` rule: replace each `/` in cwd with `-`. Example: `/root/multi-agent` → `-root-multi-agent`.
- Each `.jsonl` file = one session, lines are objects of various `type`:
  - `{"type":"last-prompt","sessionId":"<uuid>","leafUuid":"…"}`
  - `{"type":"permission-mode","permissionMode":"auto","sessionId":"<uuid>"}`
  - `{"parentUuid":…,"type":"…","attachment":{…}}`
  - `{"type":"user","message":{"role":"user","content":"…"}}`
  - `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"…"}]}}`
- Filename uuid IS the session id.
- Timestamps: `"timestamp"` field on most lines, ISO 8601.
- cwd: not stored explicitly per session; **infer from the parent directory name** (decode `-` back to `/`).

### codex

- Storage: `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-<iso-with-dashes>-<thread-uuid>.jsonl`
- Filename example: `rollout-2026-05-26T11-10-29-019e6243-2a01-7661-af39-964535611170.jsonl`
- Thread uuid (last 5 dash-separated segments) IS the session id.
- First line of each file is `{"timestamp":"…","type":"session_meta","payload":{"id":"<uuid>","timestamp":"…","cwd":"/…","originator":"codex_exec","cli_version":"0.133.0",…}}` — gives cwd + start time.
- Subsequent lines have `type` in `{"user_input","model_output","tool_call","tool_result",…}` (TBC per implementer's actual sampling — but for PR-1 only the meta + `model_output` text and `user_input` text are needed; other types skipped).
- No `codex session list` subcommand exists — implementer must walk the filesystem.

### opencode

- Storage: sqlite at `~/.local/share/opencode/opencode.db`.
- Tables (relevant):
  - `session` — columns: `id`, `project_id`, `directory`, `title`, `time_created` (or `metadata`/etc — verify with `PRAGMA table_info(session)` first), `version`, …
  - `session_message` — `id`, `session_id`, `type`, `seq`, `time_created`, `time_updated`, `data` (JSON blob)
  - `part` — `id`, `message_id`, `session_id`, `time_created`, `data` (JSON blob like `{"type":"text","text":"…"}`)
- `opencode session list` CLI exists but we still read sqlite directly (no subprocess).
- Need `modernc.org/sqlite` pure-Go driver. Check existing `go.mod` — likely not present yet; add it.

### Mock backends (no other places to update)

`grep -rn "agentbackend\.Backend\b" --include="*.go" | grep -v pkg/agentbackend` returned only:
- `cmd/slave-agent/main.go` — `backendExecutor` and `resumeAdapter` hold a `Backend` field but only call `Run`/`RunResume`. Interface extension does NOT break them (still satisfied; they just don't proactively call the new methods).
- `internal/executor/chat_resume.go` — defines its own `ResumeBackend` mini-interface with only `Run`/`RunResume`. Untouched.

The only file that needs updating to keep satisfying the extended `Backend` interface is `pkg/agentbackend/backend_test.go`'s `nilBackend` mock.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `pkg/agentbackend/backend.go` | Modify | Add `Session`, `SessionMessage` structs, `ErrSessionNotFound` sentinel, `sessionPreviewMaxBytes` const, extend `Backend` interface with `ListSessions` + `GetSession` |
| `pkg/agentbackend/backend_test.go` | Modify | Add `ListSessions` + `GetSession` to `nilBackend` so it still satisfies `Backend` |
| `pkg/agentbackend/sessions_test.go` | Create | Top-level cross-backend test: empty HOME → every registered backend returns nil, empty list |
| `pkg/agentbackend/claude/sessions.go` | Create | Read `~/.claude/projects/*/*.jsonl`; extract session id from filename, infer cwd from parent dir |
| `pkg/agentbackend/claude/sessions_test.go` | Create | Tests with fixture files |
| `pkg/agentbackend/claude/testdata/sessions/` | Create | Fixture HOME tree: `-tmp-myproj/<uuid>.jsonl` happy + corrupt + empty |
| `pkg/agentbackend/codex/sessions.go` | Create | Walk `~/.codex/sessions/<y>/<m>/<d>/rollout-*.jsonl`; first line = session_meta with id+cwd |
| `pkg/agentbackend/codex/sessions_test.go` | Create | Tests with fixture files |
| `pkg/agentbackend/codex/testdata/sessions/` | Create | Fixture HOME tree: `2026/01/15/rollout-...jsonl` happy + corrupt + meta-only |
| `pkg/agentbackend/opencode/sessions.go` | Create | Open sqlite at `~/.local/share/opencode/opencode.db` read-only; SELECT from session + session_message + part |
| `pkg/agentbackend/opencode/sessions_test.go` | Create | Tests with fixture sqlite DB built in TestMain (or per-test) |
| `pkg/agentbackend/opencode/testdata/` (existing) | (maybe Modify) | Add a builder helper to create a known sqlite fixture |
| `go.mod` / `go.sum` | Modify | Add `modernc.org/sqlite` if not present |

---

## Task 1: Backend interface extension + nilBackend update

**Files:**
- Modify: `pkg/agentbackend/backend.go`
- Modify: `pkg/agentbackend/backend_test.go`

### Step 1.1: Add types + const + sentinel to `backend.go`

- [ ] **Find the existing `Backend` interface block (around lines 24-32) in `pkg/agentbackend/backend.go`**

The existing top of file:
```go
package agentbackend

import (
	"context"

	"github.com/yourorg/multi-agent/internal/executor"
)

type Kind string

const (
	KindClaude   Kind = "claude"
	KindCodex    Kind = "codex"
	KindOpencode Kind = "opencode"
)
```

### Step 1.2: Add imports + new exports

- [ ] **Replace the imports block** at the top of `pkg/agentbackend/backend.go`:

```go
import (
	"context"
	"errors"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
)
```

### Step 1.3: Append new exports after the existing `Patch` struct

- [ ] **Append to end of `pkg/agentbackend/backend.go`** (after the `type Patch struct {…}` block):

```go
// sessionPreviewMaxBytes caps Session.Preview so a session-list UI can
// render one line per row without unbounded growth from a verbose
// assistant message. 256 bytes ≈ one display row.
const sessionPreviewMaxBytes = 256

// Session is a backend-agnostic descriptor of a conversation thread
// persisted by an agent CLI (claude / codex / opencode). Authoritative
// storage lives in the backend's own files; this struct is the
// interchange shape consumed by daemon / web layers via
// Backend.ListSessions / GetSession.
//
// See docs/vision/commander-web-entry.md §3.3 for the broader picture
// of why session data isn't duplicated in Loom's own storage.
type Session struct {
	// ID is the backend-native session identifier (claude session
	// uuid, codex thread uuid, opencode session id). Stable across
	// reads; used directly by RunResume.
	ID string

	// Kind names the backend that owns this session (mirrors
	// Backend.Kind()).
	Kind Kind

	// WorkingDir is the cwd the session was originally created with,
	// as recorded by the backend itself. May be empty when the backend
	// doesn't record it; callers should not depend on it being present.
	WorkingDir string

	// StartedAt is when the first message in the session was recorded.
	// Zero value means unknown.
	StartedAt time.Time

	// UpdatedAt is when the most recent message was appended.
	// Zero value means unknown.
	UpdatedAt time.Time

	// MessageCount is the total messages in the session (any role).
	MessageCount int

	// Preview is a short snippet from the most recent assistant message,
	// suitable for a session-list UI. Empty when no assistant message
	// has been written. Length-capped at sessionPreviewMaxBytes.
	Preview string
}

// SessionMessage is one turn in a session. Roles map to claude / codex
// / opencode conventions: "user", "assistant", "system", "tool".
type SessionMessage struct {
	Role string
	Text string
	Ts   time.Time
}

// ErrSessionNotFound signals GetSession was called with an id that does
// not exist in this backend's persistence. Distinct from other read
// errors (e.g. permission denied) so callers can return 404 cleanly.
var ErrSessionNotFound = errors.New("agentbackend: session not found")
```

### Step 1.4: Extend `Backend` interface

- [ ] **Replace the existing `Backend` interface definition** (currently at lines 24-32 of `pkg/agentbackend/backend.go`):

Find:
```go
type Backend interface {
	Kind() Kind
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
	RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
	LLM() LLMRunner
	Permissions() PermissionsStore
	Detect(ctx context.Context) error
}
```

Replace with:
```go
type Backend interface {
	Kind() Kind
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
	RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
	LLM() LLMRunner
	Permissions() PermissionsStore
	Detect(ctx context.Context) error

	// ListSessions returns descriptors for every session this backend
	// has persisted on disk. Empty list (with nil error) when the
	// backend has no session storage directory or it is empty.
	// Implementations must not shell out to the backend CLI.
	// Individual unparseable session entries are skipped silently;
	// a hard error is returned only when the storage location itself
	// can't be read (e.g. permission denied on a directory we expected).
	ListSessions(ctx context.Context) ([]Session, error)

	// GetSession returns the descriptor plus full message history of
	// one session. Returns ErrSessionNotFound when id is unknown to
	// this backend. Like ListSessions, no subprocess invocation.
	GetSession(ctx context.Context, id string) (Session, []SessionMessage, error)
}
```

### Step 1.5: Update `nilBackend` mock so it still satisfies `Backend`

- [ ] **Append to `pkg/agentbackend/backend_test.go`** (after the existing `nilPerm` methods, before EOF):

```go

func (nilBackend) ListSessions(_ context.Context) ([]Session, error) { return nil, nil }
func (nilBackend) GetSession(_ context.Context, _ string) (Session, []SessionMessage, error) {
	return Session{}, nil, ErrSessionNotFound
}
```

### Step 1.6: Verify package compiles + existing tests still pass

- [ ] **Run**

```bash
go build ./pkg/agentbackend/...
go test ./pkg/agentbackend/... -count=1
```

Expected: clean. `TestInterfacesCompile` still passes (proves `nilBackend` satisfies new `Backend`).

Now verify the package's downstream consumers — the 3 backend packages — still compile (they shouldn't break yet because they will fail to satisfy `Backend` interface at the `init()` `RegisterBuilder` call site that hands `*Backend` back, which still satisfies via the methods they DO have plus the methods they DON'T — wait, the missing methods will cause the assignment to fail at compile time).

```bash
go build ./pkg/agentbackend/claude/...
```

Expected: compile error like `*Backend does not implement agentbackend.Backend (missing method GetSession)`.

This is the **intentional cascade** — claude/codex/opencode each fail until their Tasks 3/4/5 land. We'll fix this by adding stubs in Task 1 too.

### Step 1.7: Add satisfaction stubs to claude/codex/opencode

To keep the tree compileable between Task 1 and Tasks 3-5, add **stub methods** to each backend's `Backend` struct that panic on call (Tasks 3-5 will replace).

- [ ] **Append to `pkg/agentbackend/claude/backend.go`**:

```go

// ListSessions / GetSession stubs — implemented in sessions.go (Task 3).
// Panic ensures we'd notice if a caller invoked these before Task 3
// lands. Bare interface satisfaction only.
func (b *Backend) ListSessions(_ context.Context) ([]agentbackend.Session, error) {
	panic("claude.ListSessions not implemented (Task 3)")
}
func (b *Backend) GetSession(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	panic("claude.GetSession not implemented (Task 3)")
}
```

- [ ] **Append to `pkg/agentbackend/codex/backend.go`** (replace `claude` with `codex` and "Task 3" with "Task 4"):

```go

// ListSessions / GetSession stubs — implemented in sessions.go (Task 4).
func (b *Backend) ListSessions(_ context.Context) ([]agentbackend.Session, error) {
	panic("codex.ListSessions not implemented (Task 4)")
}
func (b *Backend) GetSession(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	panic("codex.GetSession not implemented (Task 4)")
}
```

- [ ] **Append to `pkg/agentbackend/opencode/backend.go`** (Task 5):

```go

// ListSessions / GetSession stubs — implemented in sessions.go (Task 5).
func (b *Backend) ListSessions(_ context.Context) ([]agentbackend.Session, error) {
	panic("opencode.ListSessions not implemented (Task 5)")
}
func (b *Backend) GetSession(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	panic("opencode.GetSession not implemented (Task 5)")
}
```

### Step 1.8: Verify whole repo compiles + tests pass

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

Expected: clean. The stubs panic only if called; existing tests don't call them.

### Step 1.9: Commit

```bash
git add pkg/agentbackend/backend.go pkg/agentbackend/backend_test.go pkg/agentbackend/claude/backend.go pkg/agentbackend/codex/backend.go pkg/agentbackend/opencode/backend.go
git commit -m "feat(agentbackend): extend Backend interface with ListSessions / GetSession (PR-1 Task 1)

PR-1 of commander-web-entry vision (docs/vision/commander-web-entry.md).
Adds the read-side session API that daemon (PR-2) and observer UI
(PR-3) will consume.

Backend interface gains two methods + Session/SessionMessage structs +
ErrSessionNotFound sentinel + sessionPreviewMaxBytes constant.

claude/codex/opencode each get panicking stubs so the package keeps
compiling between this commit and Tasks 3-5 which replace them with
real implementations reading the backend's own on-disk session store.

nilBackend mock updated to satisfy the extended interface.

Spec: docs/superpowers/specs/2026-06-14-backend-sessions-design.md
Vision: docs/vision/commander-web-entry.md PR-1

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Top-level empty-dir cross-backend test

**Files:**
- Create: `pkg/agentbackend/sessions_test.go`

This test pins invariant 3 from the spec (empty-dir tolerance) at the package level: every registered backend returns nil error + empty slice when its session storage is missing. Catches a backend that crashes on a fresh install.

### Step 2.1: Write the test

- [ ] **Create `pkg/agentbackend/sessions_test.go`**:

```go
package agentbackend_test

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

// TestListSessions_EmptyHomeEveryBackend pins spec invariant 3:
// every registered backend tolerates a fresh HOME with no session
// storage at all — returns (nil, nil), not a crash, not an error.
// Catches a backend that assumes its storage dir always exists.
func TestListSessions_EmptyHomeEveryBackend(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", emptyHome+"/.config")
	t.Setenv("XDG_DATA_HOME", emptyHome+"/.local/share")

	for _, k := range agentbackend.RegisteredKinds() {
		t.Run(k, func(t *testing.T) {
			b, err := agentbackend.New(agentbackend.Config{
				Kind:    agentbackend.Kind(k),
				Bin:     k, // factory will default to this anyway
				WorkDir: emptyHome,
			}, nil)
			if err != nil {
				t.Fatalf("New(%s): %v", k, err)
			}
			sessions, err := b.ListSessions(context.Background())
			if err != nil {
				t.Fatalf("ListSessions(%s) on empty home: %v", k, err)
			}
			if len(sessions) != 0 {
				t.Fatalf("ListSessions(%s) on empty home: got %d sessions, want 0", k, len(sessions))
			}
		})
	}
}

// TestGetSession_UnknownIDEveryBackend pins invariant 5: unknown id
// returns ErrSessionNotFound (via errors.Is) on every backend.
func TestGetSession_UnknownIDEveryBackend(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", emptyHome+"/.config")
	t.Setenv("XDG_DATA_HOME", emptyHome+"/.local/share")

	for _, k := range agentbackend.RegisteredKinds() {
		t.Run(k, func(t *testing.T) {
			b, err := agentbackend.New(agentbackend.Config{
				Kind: agentbackend.Kind(k), Bin: k, WorkDir: emptyHome,
			}, nil)
			if err != nil {
				t.Fatalf("New(%s): %v", k, err)
			}
			_, _, err = b.GetSession(context.Background(), "no-such-session-id")
			if !errorsIs(err, agentbackend.ErrSessionNotFound) {
				t.Fatalf("GetSession(%s) unknown id: got err=%v, want ErrSessionNotFound", k, err)
			}
		})
	}
}

// errorsIs is a local shorthand to keep the test file import surface
// tiny. Equivalent to errors.Is from the stdlib.
func errorsIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	for {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
		if err == nil {
			return false
		}
	}
}
```

### Step 2.2: Verify it fails — the panicking stubs will trigger

```bash
go test ./pkg/agentbackend/ -run "TestListSessions_EmptyHomeEveryBackend|TestGetSession_UnknownIDEveryBackend" -count=1 -v
```

Expected: all subtests panic from the stubs (claude/codex/opencode `ListSessions not implemented`).

Don't commit yet — this test will land green only after Task 5 (last backend impl). Either:

**(a)** Commit it now and accept that the package is RED until Task 5 — that's fine because the per-backend Tasks 3/4/5 each turn one subtest green.

**(b)** Hold the file in working tree, commit in Task 6 with the final regression.

Plan picks **(a)** — TDD purist: the failing umbrella test motivates each backend impl. Implementer just needs to know subsequent task commits will land the failing subtests green.

### Step 2.3: Commit

```bash
git add pkg/agentbackend/sessions_test.go
git commit -m "test(agentbackend): umbrella tests for ListSessions empty-dir + GetSession unknown-id (PR-1 Task 2)

Two cross-backend tests pin spec invariants 3 (empty-dir → nil, nil)
and 5 (unknown id → ErrSessionNotFound) for every kind registered
with the agentbackend registry.

RED for all 3 backends until Tasks 3/4/5 replace the stubs.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: claude `sessions.go` + tests + fixtures

**Files:**
- Create: `pkg/agentbackend/claude/sessions.go`
- Create: `pkg/agentbackend/claude/sessions_test.go`
- Create: `pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj/aaaa1111-bbbb-2222-cccc-333333333333.jsonl` (happy fixture)
- Create: `pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj/bbbb2222-cccc-3333-dddd-444444444444.jsonl` (corrupt fixture — invalid JSON on line 2)
- Create: `pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj/cccc3333-dddd-4444-eeee-555555555555.jsonl` (empty-history fixture — only `last-prompt` + `permission-mode`)
- Modify: `pkg/agentbackend/claude/backend.go` (delete the panicking stubs added in Task 1)

### Step 3.1: Create the fixture files

The fixture dir is the **HOME** the test will set, not just a session dir. Inside it sits `.claude/projects/<encoded-cwd>/<uuid>.jsonl`.

- [ ] **Create fixture dir tree**

```bash
mkdir -p pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj
```

- [ ] **Create happy fixture** `pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj/aaaa1111-bbbb-2222-cccc-333333333333.jsonl`:

```
{"type":"last-prompt","leafUuid":"leaf-1","sessionId":"aaaa1111-bbbb-2222-cccc-333333333333"}
{"type":"permission-mode","permissionMode":"auto","sessionId":"aaaa1111-bbbb-2222-cccc-333333333333"}
{"type":"user","timestamp":"2026-06-14T10:00:00Z","sessionId":"aaaa1111-bbbb-2222-cccc-333333333333","message":{"role":"user","content":"hello, claude"}}
{"type":"assistant","timestamp":"2026-06-14T10:00:01Z","sessionId":"aaaa1111-bbbb-2222-cccc-333333333333","message":{"role":"assistant","content":[{"type":"text","text":"hi! how can I help?"}]}}
{"type":"user","timestamp":"2026-06-14T10:00:05Z","sessionId":"aaaa1111-bbbb-2222-cccc-333333333333","message":{"role":"user","content":"what is 2+2?"}}
{"type":"assistant","timestamp":"2026-06-14T10:00:06Z","sessionId":"aaaa1111-bbbb-2222-cccc-333333333333","message":{"role":"assistant","content":[{"type":"text","text":"4"}]}}
```

- [ ] **Create corrupt fixture** `pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj/bbbb2222-cccc-3333-dddd-444444444444.jsonl`:

```
{"type":"last-prompt","sessionId":"bbbb2222-cccc-3333-dddd-444444444444"}
this line is not valid json {{ broken
{"type":"user","timestamp":"2026-06-14T11:00:00Z","sessionId":"bbbb2222-cccc-3333-dddd-444444444444","message":{"role":"user","content":"survives"}}
```

- [ ] **Create empty-history fixture** `pkg/agentbackend/claude/testdata/sessions/.claude/projects/-tmp-myproj/cccc3333-dddd-4444-eeee-555555555555.jsonl`:

```
{"type":"last-prompt","sessionId":"cccc3333-dddd-4444-eeee-555555555555"}
{"type":"permission-mode","permissionMode":"auto","sessionId":"cccc3333-dddd-4444-eeee-555555555555"}
```

### Step 3.2: Write failing tests

- [ ] **Create `pkg/agentbackend/claude/sessions_test.go`**:

```go
package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// copyFixtureToHOME copies the prebuilt fixture tree under
// testdata/sessions/ into a temporary HOME, returning that HOME path.
// The tree includes .claude/projects/-tmp-myproj/*.jsonl that the
// claude backend scans.
func copyFixtureToHOME(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "sessions")
	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o600)
	})
}

// TestListSessions_ReturnsKnownSessions pins that the 3 fixture
// sessions are enumerated with correct IDs + cwd inferred from
// the encoded parent dir.
func TestListSessions_ReturnsKnownSessions(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3 (ids=%v)", len(got), sessionIDs(got))
	}
	want := map[string]bool{
		"aaaa1111-bbbb-2222-cccc-333333333333": false,
		"bbbb2222-cccc-3333-dddd-444444444444": false,
		"cccc3333-dddd-4444-eeee-555555555555": false,
	}
	for _, s := range got {
		if _, ok := want[s.ID]; !ok {
			t.Errorf("unexpected session id %q", s.ID)
		}
		want[s.ID] = true
		if s.Kind != agentbackend.KindClaude {
			t.Errorf("session %s: kind=%v want claude", s.ID, s.Kind)
		}
		if s.WorkingDir != "/tmp/myproj" {
			t.Errorf("session %s: WorkingDir=%q want /tmp/myproj", s.ID, s.WorkingDir)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing session %s", id)
		}
	}
}

func sessionIDs(ss []agentbackend.Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

// TestListSessions_TolertaesCorruptFile pins invariant 4: a corrupt
// jsonl in the dir doesn't fail the whole listing — the corrupt
// file's session is still reported (the filename uuid alone tells us
// it exists) with whatever was parseable.
func TestListSessions_TolertaesCorruptFile(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions errored even though one file is corrupt: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions even with corrupt file; got %d", len(got))
	}
}

// TestGetSession_ReturnsMessages pins that GetSession returns the
// happy session's full message history.
func TestGetSession_ReturnsMessages(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), "aaaa1111-bbbb-2222-cccc-333333333333")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "aaaa1111-bbbb-2222-cccc-333333333333" {
		t.Errorf("sess.ID=%q", sess.ID)
	}
	if sess.MessageCount != 4 {
		t.Errorf("MessageCount=%d want 4 (2 user + 2 assistant)", sess.MessageCount)
	}
	if sess.Preview == "" {
		t.Errorf("Preview empty; want last assistant message")
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "hello, claude" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[3].Role != "assistant" || msgs[3].Text != "4" {
		t.Errorf("msgs[3]=%+v", msgs[3])
	}
}

// TestGetSession_UnknownIDReturnsErrSessionNotFound pins invariant 5.
func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v, want ErrSessionNotFound", err)
	}
}
```

### Step 3.3: Verify tests FAIL (panicking stubs)

```bash
go test ./pkg/agentbackend/claude/ -run "TestListSessions|TestGetSession" -count=1 -v
```

Expected: panic from stubs.

### Step 3.4: Implement `sessions.go`

- [ ] **Delete the panicking stubs from `pkg/agentbackend/claude/backend.go`** (the two methods added in Task 1.7) — `sessions.go` will provide the real ones.

- [ ] **Create `pkg/agentbackend/claude/sessions.go`**:

```go
// Package claude — session storage reader.
//
// Storage layout (claude code 0.5.x, captured 2026-06-15):
//
//	$HOME/.claude/projects/<encoded_cwd>/<session_uuid>.jsonl
//
// <encoded_cwd> is the absolute cwd with each '/' replaced by '-'.
// Example: /root/multi-agent → -root-multi-agent
//
// Each .jsonl file is one session. Lines are JSON objects of various
// `type`. Schema relevant to this reader:
//
//   {"type":"last-prompt","sessionId":"<uuid>",…}        — first line typical
//   {"type":"permission-mode","permissionMode":"…",…}
//   {"type":"user","timestamp":"…","message":{"role":"user","content":"…"}}
//   {"type":"assistant","timestamp":"…","message":{"role":"assistant","content":[{"type":"text","text":"…"}]}}
//
// Other types (tool_use, summary, attachment) are skipped — UI only
// shows user/assistant turns.
//
// The reader does NOT spawn claude code. All data is read from disk.

package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// sessionsRoot returns $HOME/.claude/projects, or empty string if HOME
// is unset.
func sessionsRoot() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// decodeCwd reverses claude's '/' → '-' encoding of project paths.
// Example: -root-multi-agent → /root/multi-agent
func decodeCwd(encoded string) string {
	return "/" + strings.ReplaceAll(strings.TrimPrefix(encoded, "-"), "-", "/")
}

// ListSessions walks $HOME/.claude/projects/*/*.jsonl and returns one
// Session per file. Missing root → (nil, nil). Files that fail to open
// are skipped; files with corrupt lines still return a Session (other
// metadata may be from the filename + the parseable lines).
func (b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	root := sessionsRoot()
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []agentbackend.Session
	for _, projDir := range entries {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if !projDir.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, projDir.Name())
		cwd := decodeCwd(projDir.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue // unreadable subdir = skip, don't fail entire list
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			s := scanSession(filepath.Join(dirPath, f.Name()), id, cwd)
			out = append(out, s)
		}
	}
	return out, nil
}

// GetSession returns full message history for one session id, locating
// the file by scanning subdirs (claude doesn't index sessions
// elsewhere). Returns ErrSessionNotFound if not present.
func (b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	root := sessionsRoot()
	if root == "" {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	for _, projDir := range entries {
		if !projDir.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, projDir.Name())
		path := filepath.Join(dirPath, id+".jsonl")
		if _, statErr := os.Stat(path); statErr == nil {
			cwd := decodeCwd(projDir.Name())
			sess, msgs := loadSession(path, id, cwd)
			return sess, msgs, nil
		}
	}
	return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
}

// claudeJSONLLine is the minimal shape we decode from each jsonl line.
// Unknown fields are ignored; unparseable lines are skipped.
type claudeJSONLLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	SessionID string          `json:"sessionId"`
	Message   *claudeJSONLMsg `json:"message,omitempty"`
}

type claudeJSONLMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// scanSession reads the file and returns only the Session descriptor.
// Used by ListSessions where messages aren't needed.
func scanSession(path, id, cwd string) agentbackend.Session {
	s, _ := loadSessionImpl(path, id, cwd, false)
	return s
}

// loadSession reads the file and returns both descriptor + messages.
// Used by GetSession.
func loadSession(path, id, cwd string) (agentbackend.Session, []agentbackend.SessionMessage) {
	return loadSessionImpl(path, id, cwd, true)
}

func loadSessionImpl(path, id, cwd string, withMessages bool) (agentbackend.Session, []agentbackend.SessionMessage) {
	sess := agentbackend.Session{
		ID:         id,
		Kind:       agentbackend.KindClaude,
		WorkingDir: cwd,
	}
	f, err := os.Open(path)
	if err != nil {
		return sess, nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Lines can be large (system_prompt etc); raise the limit.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var msgs []agentbackend.SessionMessage
	var lastAssistantText string
	for sc.Scan() {
		var ln claudeJSONLLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue // skip corrupt line
		}
		if ln.Message == nil {
			continue
		}
		text := extractText(ln.Message.Content)
		if text == "" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, ln.Timestamp)
		if sess.StartedAt.IsZero() {
			sess.StartedAt = ts
		}
		sess.UpdatedAt = ts
		sess.MessageCount++
		if ln.Message.Role == "assistant" {
			lastAssistantText = text
		}
		if withMessages {
			msgs = append(msgs, agentbackend.SessionMessage{
				Role: ln.Message.Role,
				Text: text,
				Ts:   ts,
			})
		}
	}
	if lastAssistantText != "" {
		sess.Preview = truncatePreview(lastAssistantText)
	}
	return sess, msgs
}

// extractText handles the two content shapes claude uses:
//   - string (older / user role often): "hello"
//   - array of typed parts (assistant): [{"type":"text","text":"…"}]
//
// Returns the concatenation of all text parts; other types skipped.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try plain string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of parts.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// truncatePreview returns at most sessionPreviewMaxBytes UTF-8 bytes,
// cut on a rune boundary.
func truncatePreview(s string) string {
	const cap = 256
	if len(s) <= cap {
		return s
	}
	out := s[:cap]
	// Walk back to a valid utf8 rune start.
	for len(out) > 0 && (out[len(out)-1]&0xC0) == 0x80 {
		out = out[:len(out)-1]
	}
	return out + "…"
}
```

NOTE: `sessionPreviewMaxBytes` in `backend.go` is **unexported** — so `truncatePreview` uses a local const literal `256` instead. If you'd rather not duplicate, exporting the cap is fine, but the const is invariant + tied to the truncatePreview function's contract.

### Step 3.5: Verify tests GREEN

```bash
go test ./pkg/agentbackend/claude/ -count=1 -v
go test ./pkg/agentbackend/ -run "TestListSessions_EmptyHomeEveryBackend/claude|TestGetSession_UnknownIDEveryBackend/claude" -count=1 -v
```

Expected: all 4 claude tests PASS + the 2 umbrella subtests for claude PASS.

```bash
go build ./...
go vet ./...
```

Expected: clean.

### Step 3.6: Commit

```bash
git add pkg/agentbackend/claude/sessions.go pkg/agentbackend/claude/sessions_test.go pkg/agentbackend/claude/testdata/ pkg/agentbackend/claude/backend.go
git commit -m "feat(agentbackend/claude): ListSessions / GetSession via ~/.claude/projects (PR-1 Task 3)

Reads claude code's on-disk session storage directly (no subprocess).
Layout: $HOME/.claude/projects/<encoded_cwd>/<session_uuid>.jsonl,
where <encoded_cwd> reverses '/' to '-' (e.g. -root-multi-agent).

Per-line schema captured from claude code 0.5.x:
  {type:'user'|'assistant', timestamp, message:{role, content}}
content is either a plain string or an array of {type,text} parts.
Other line types (last-prompt, permission-mode, tool_use, summary,
attachment) are silently skipped — UI shows user/assistant turns only.

Corrupt lines do not fail ListSessions — Session is returned with
whatever parseable data was found. Unknown id returns ErrSessionNotFound.

Stubs from Task 1's backend.go removed.

4 unit tests + 2 umbrella subtests in agentbackend/sessions_test.go
turn green. codex + opencode subtests still RED until Tasks 4/5.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: codex `sessions.go` + tests + fixtures

**Files:**
- Create: `pkg/agentbackend/codex/sessions.go`
- Create: `pkg/agentbackend/codex/sessions_test.go`
- Create: `pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15/rollout-2026-01-15T10-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl` (happy)
- Create: `pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15/rollout-2026-01-15T11-00-00-ffffffff-1111-2222-3333-444444444444.jsonl` (corrupt — invalid first line)
- Create: `pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15/rollout-2026-01-15T12-00-00-99999999-aaaa-bbbb-cccc-dddddddddddd.jsonl` (meta-only — no messages)
- Modify: `pkg/agentbackend/codex/backend.go` (delete the panicking stubs from Task 1)

### Step 4.1: Create fixture files

```bash
mkdir -p pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15
```

- [ ] **Happy fixture** `pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15/rollout-2026-01-15T10-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl`:

```
{"timestamp":"2026-01-15T10:00:00.000Z","type":"session_meta","payload":{"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-01-15T10:00:00.000Z","cwd":"/tmp/codex-proj","originator":"codex_exec","cli_version":"0.139.0"}}
{"timestamp":"2026-01-15T10:00:05.000Z","type":"user_input","payload":{"text":"sum 2 and 3"}}
{"timestamp":"2026-01-15T10:00:07.000Z","type":"model_output","payload":{"text":"5"}}
{"timestamp":"2026-01-15T10:00:15.000Z","type":"user_input","payload":{"text":"now divide by 0"}}
{"timestamp":"2026-01-15T10:00:18.000Z","type":"model_output","payload":{"text":"division by zero is undefined"}}
```

- [ ] **Corrupt fixture** `pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15/rollout-2026-01-15T11-00-00-ffffffff-1111-2222-3333-444444444444.jsonl`:

```
not valid json at all {{{
{"timestamp":"2026-01-15T11:00:05.000Z","type":"user_input","payload":{"text":"hello after broken first line"}}
```

(The first-line corruption means session_meta is missing — implementer falls back to filename-derived id.)

- [ ] **Meta-only fixture** `pkg/agentbackend/codex/testdata/sessions/.codex/sessions/2026/01/15/rollout-2026-01-15T12-00-00-99999999-aaaa-bbbb-cccc-dddddddddddd.jsonl`:

```
{"timestamp":"2026-01-15T12:00:00.000Z","type":"session_meta","payload":{"id":"99999999-aaaa-bbbb-cccc-dddddddddddd","timestamp":"2026-01-15T12:00:00.000Z","cwd":"/tmp/empty-codex","originator":"codex_exec","cli_version":"0.139.0"}}
```

### Step 4.2: Write failing tests

- [ ] **Create `pkg/agentbackend/codex/sessions_test.go`**:

```go
package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func copyFixtureToHOME(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "sessions")
	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o600)
	})
}

func TestListSessions_ReturnsKnownSessions(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	wantIDs := map[string]string{
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee": "/tmp/codex-proj",
		"ffffffff-1111-2222-3333-444444444444": "", // session_meta missing, cwd unknown
		"99999999-aaaa-bbbb-cccc-dddddddddddd": "/tmp/empty-codex",
	}
	for _, s := range got {
		wantCwd, ok := wantIDs[s.ID]
		if !ok {
			t.Errorf("unexpected id %q", s.ID)
			continue
		}
		if s.Kind != agentbackend.KindCodex {
			t.Errorf("session %s: kind=%v want codex", s.ID, s.Kind)
		}
		if s.WorkingDir != wantCwd {
			t.Errorf("session %s: cwd=%q want %q", s.ID, s.WorkingDir, wantCwd)
		}
	}
}

func TestListSessions_TolertaesCorruptFile(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions errored with corrupt file: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions even with corrupt file; got %d", len(got))
	}
}

func TestGetSession_ReturnsMessages(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/codex-proj" {
		t.Errorf("WorkingDir=%q", sess.WorkingDir)
	}
	if sess.MessageCount != 4 {
		t.Errorf("MessageCount=%d want 4", sess.MessageCount)
	}
	if sess.Preview == "" {
		t.Errorf("Preview empty")
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs)=%d want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "sum 2 and 3" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[3].Role != "assistant" || msgs[3].Text != "division by zero is undefined" {
		t.Errorf("msgs[3]=%+v", msgs[3])
	}
}

func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v want ErrSessionNotFound", err)
	}
}
```

### Step 4.3: Verify RED

```bash
go test ./pkg/agentbackend/codex/ -run "TestListSessions|TestGetSession" -count=1 -v
```

Expected: panic from stubs.

### Step 4.4: Implement `sessions.go`

- [ ] **Delete the panicking stubs from `pkg/agentbackend/codex/backend.go`** (Task 1.7 additions).

- [ ] **Create `pkg/agentbackend/codex/sessions.go`**:

```go
// Package codex — session storage reader.
//
// Storage layout (codex 0.139.0, captured 2026-06-15):
//
//	$HOME/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-<iso>-<thread-uuid>.jsonl
//
// Filename example:
//	rollout-2026-05-26T11-10-29-019e6243-2a01-7661-af39-964535611170.jsonl
//
// Thread uuid (last 5 dash-separated segments after "rollout-<iso>-") IS
// the session id. The .jsonl's first line is typically:
//	{"timestamp":"…","type":"session_meta","payload":{"id":"<uuid>","cwd":"/…","cli_version":"…"}}
// Subsequent lines:
//	{"timestamp":"…","type":"user_input"|"model_output"|…,"payload":{"text":"…"}}
//
// Other types (tool_call, tool_result, etc.) are skipped — only
// user/assistant text turns reach the UI.
//
// The reader does NOT spawn codex. All data is read from disk.

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func sessionsRoot() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// filenameUUIDRe matches the trailing thread uuid in a rollout filename.
// The uuid is 5 dash-separated groups: 8-4-4-4-12 chars.
var filenameUUIDRe = regexp.MustCompile(`-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

func sessionIDFromFilename(name string) string {
	m := filenameUUIDRe.FindStringSubmatch(name)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// ListSessions walks $HOME/.codex/sessions/<y>/<m>/<d>/rollout-*.jsonl.
func (b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	root := sessionsRoot()
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []agentbackend.Session
	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable subtree
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		id := sessionIDFromFilename(info.Name())
		if id == "" {
			return nil
		}
		s := scanCodexSession(p, id, false)
		out = append(out, s.session)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	root := sessionsRoot()
	if root == "" {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	var found string
	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		if sessionIDFromFilename(info.Name()) == id {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return agentbackend.Session{}, nil, err
	}
	if found == "" {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	res := scanCodexSession(found, id, true)
	return res.session, res.messages, nil
}

type codexScanResult struct {
	session  agentbackend.Session
	messages []agentbackend.SessionMessage
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexMetaPayload struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

type codexTextPayload struct {
	Text string `json:"text"`
}

// scanCodexSession reads one rollout jsonl file. withMessages=false
// for ListSessions (descriptor only); true for GetSession.
func scanCodexSession(path, fallbackID string, withMessages bool) codexScanResult {
	res := codexScanResult{session: agentbackend.Session{
		ID:   fallbackID,
		Kind: agentbackend.KindCodex,
	}}
	f, err := os.Open(path)
	if err != nil {
		return res
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var lastAssistantText string
	for sc.Scan() {
		var ln codexLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, ln.Timestamp)
		switch ln.Type {
		case "session_meta":
			var p codexMetaPayload
			if json.Unmarshal(ln.Payload, &p) == nil {
				if p.ID != "" {
					res.session.ID = p.ID
				}
				if p.Cwd != "" {
					res.session.WorkingDir = p.Cwd
				}
				if res.session.StartedAt.IsZero() {
					res.session.StartedAt = ts
				}
			}
		case "user_input":
			var p codexTextPayload
			if json.Unmarshal(ln.Payload, &p) != nil || p.Text == "" {
				continue
			}
			res.session.MessageCount++
			if res.session.StartedAt.IsZero() {
				res.session.StartedAt = ts
			}
			res.session.UpdatedAt = ts
			if withMessages {
				res.messages = append(res.messages, agentbackend.SessionMessage{
					Role: "user", Text: p.Text, Ts: ts,
				})
			}
		case "model_output":
			var p codexTextPayload
			if json.Unmarshal(ln.Payload, &p) != nil || p.Text == "" {
				continue
			}
			res.session.MessageCount++
			if res.session.StartedAt.IsZero() {
				res.session.StartedAt = ts
			}
			res.session.UpdatedAt = ts
			lastAssistantText = p.Text
			if withMessages {
				res.messages = append(res.messages, agentbackend.SessionMessage{
					Role: "assistant", Text: p.Text, Ts: ts,
				})
			}
		default:
			continue
		}
	}
	if lastAssistantText != "" {
		res.session.Preview = truncatePreview(lastAssistantText)
	}
	return res
}

// truncatePreview is a local copy of the helper in claude/sessions.go.
// Both implementations need it; copying avoids a fan-out via a tiny
// shared package.
func truncatePreview(s string) string {
	const cap = 256
	if len(s) <= cap {
		return s
	}
	out := s[:cap]
	for len(out) > 0 && (out[len(out)-1]&0xC0) == 0x80 {
		out = out[:len(out)-1]
	}
	return out + "…"
}
```

### Step 4.5: Verify GREEN

```bash
go test ./pkg/agentbackend/codex/ -count=1 -v
go test ./pkg/agentbackend/ -run "TestListSessions_EmptyHomeEveryBackend/codex|TestGetSession_UnknownIDEveryBackend/codex" -count=1 -v
go build ./...
go vet ./...
```

Expected: codex tests + 2 umbrella subtests PASS; build/vet clean.

### Step 4.6: Commit

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go pkg/agentbackend/codex/testdata/ pkg/agentbackend/codex/backend.go
git commit -m "feat(agentbackend/codex): ListSessions / GetSession via ~/.codex/sessions/ (PR-1 Task 4)

Reads codex's on-disk rollout files directly (no subprocess). Layout:
  \$HOME/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-<iso>-<thread-uuid>.jsonl

Thread uuid (last 5 dash-segments before .jsonl) is the session id.
First line is typically session_meta with id+cwd+cli_version; later
lines are user_input/model_output/tool_call/etc. Only user_input and
model_output text turns reach the UI.

Fallback: if session_meta is corrupt/missing, the uuid from the
filename still seeds Session.ID — list and get still work, just with
unknown cwd.

Stubs from Task 1's backend.go removed.

4 unit tests + 2 umbrella subtests turn green. opencode still RED.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: opencode `sessions.go` + tests + fixtures + go.mod sqlite

**Files:**
- Modify: `go.mod` / `go.sum` (add `modernc.org/sqlite` if absent)
- Create: `pkg/agentbackend/opencode/sessions.go`
- Create: `pkg/agentbackend/opencode/sessions_test.go`
- Create: `pkg/agentbackend/opencode/testdata/sessions_fixture.go` (helper that builds the fixture DB at test time — simpler than checking in a binary .db)
- Modify: `pkg/agentbackend/opencode/backend.go` (delete the panicking stubs from Task 1)

### Step 5.1: Add sqlite driver if absent

- [ ] **Check go.mod**

```bash
grep -E "modernc.org/sqlite|mattn/go-sqlite3" go.mod
```

If `modernc.org/sqlite` is present, skip 5.1.

If only `mattn/go-sqlite3` is present, use it (CGO required — but the repo build expects CGO_ENABLED=0 in some paths; verify with `git grep CGO_ENABLED`).

If neither is present:

```bash
go get modernc.org/sqlite
go mod tidy
```

The `modernc.org/sqlite` driver is pure-Go and works under `CGO_ENABLED=0`, which matches PR #19's binary smoke pattern.

### Step 5.2: Create the fixture-DB builder

opencode's sqlite schema is documented in the spec/pre-flight section. Building a real sqlite at test time is simpler than checking in a binary file.

- [ ] **Create `pkg/agentbackend/opencode/sessions_fixture_test.go`** (note: `_test.go` so it isn't in the production binary):

```go
package opencode

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// buildFixtureDB writes a minimal opencode.db at
// $HOME/.local/share/opencode/opencode.db (HOME = the returned dir),
// populated with the schema + rows needed by sessions_test.go:
//
//   session ses_a — happy, 4 messages (2 user + 2 assistant)
//   session ses_b — corrupt (one part row has unparseable data field)
//   session ses_c — meta only, 0 messages
//
// Returns the HOME path the test should t.Setenv("HOME", ...) with.
func buildFixtureDB(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dbDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stmts := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL DEFAULT '', time_created INTEGER NOT NULL DEFAULT 0, time_updated INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,

		// ses_a happy
		`INSERT INTO session VALUES ('ses_a','/tmp/opencode-a','first session','1.17.6',1781442100000,1781442200000)`,
		`INSERT INTO session_message VALUES ('msg_a1','ses_a','user',1,1781442101000,1781442101000,'{}')`,
		`INSERT INTO part VALUES ('prt_a1','msg_a1','ses_a',1781442101000,1781442101000,'{"type":"text","text":"hello from a"}')`,
		`INSERT INTO session_message VALUES ('msg_a2','ses_a','assistant',2,1781442102000,1781442102000,'{}')`,
		`INSERT INTO part VALUES ('prt_a2','msg_a2','ses_a',1781442102000,1781442102000,'{"type":"text","text":"hi back"}')`,
		`INSERT INTO session_message VALUES ('msg_a3','ses_a','user',3,1781442150000,1781442150000,'{}')`,
		`INSERT INTO part VALUES ('prt_a3','msg_a3','ses_a',1781442150000,1781442150000,'{"type":"text","text":"another turn"}')`,
		`INSERT INTO session_message VALUES ('msg_a4','ses_a','assistant',4,1781442200000,1781442200000,'{}')`,
		`INSERT INTO part VALUES ('prt_a4','msg_a4','ses_a',1781442200000,1781442200000,'{"type":"text","text":"final answer"}')`,

		// ses_b: one part has unparseable data — reader should skip that part
		`INSERT INTO session VALUES ('ses_b','/tmp/opencode-b','corrupt-mix','1.17.6',1781442300000,1781442400000)`,
		`INSERT INTO session_message VALUES ('msg_b1','ses_b','user',1,1781442301000,1781442301000,'{}')`,
		`INSERT INTO part VALUES ('prt_b1','msg_b1','ses_b',1781442301000,1781442301000,'this is not valid json {{{')`,

		// ses_c: meta only, no messages
		`INSERT INTO session VALUES ('ses_c','/tmp/opencode-c','empty','1.17.6',1781442500000,1781442500000)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("fixture exec %q: %v", q, err)
		}
	}
	return home
}
```

### Step 5.3: Write failing tests

- [ ] **Create `pkg/agentbackend/opencode/sessions_test.go`**:

```go
package opencode

import (
	"context"
	"errors"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestListSessions_ReturnsKnownSessions(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	wantIDs := map[string]string{
		"ses_a": "/tmp/opencode-a",
		"ses_b": "/tmp/opencode-b",
		"ses_c": "/tmp/opencode-c",
	}
	for _, s := range got {
		wantCwd, ok := wantIDs[s.ID]
		if !ok {
			t.Errorf("unexpected id %q", s.ID)
			continue
		}
		if s.Kind != agentbackend.KindOpencode {
			t.Errorf("session %s: kind=%v want opencode", s.ID, s.Kind)
		}
		if s.WorkingDir != wantCwd {
			t.Errorf("session %s: cwd=%q want %q", s.ID, s.WorkingDir, wantCwd)
		}
	}
}

func TestListSessions_TolertaesCorruptParts(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions errored with corrupt part: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions even with corrupt part; got %d", len(got))
	}
}

func TestGetSession_ReturnsMessages(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/opencode-a" {
		t.Errorf("WorkingDir=%q", sess.WorkingDir)
	}
	if sess.MessageCount != 4 {
		t.Errorf("MessageCount=%d want 4", sess.MessageCount)
	}
	if sess.Preview == "" {
		t.Errorf("Preview empty")
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs)=%d want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "hello from a" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[3].Role != "assistant" || msgs[3].Text != "final answer" {
		t.Errorf("msgs[3]=%+v", msgs[3])
	}
}

func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v want ErrSessionNotFound", err)
	}
}
```

### Step 5.4: Verify RED

```bash
go test ./pkg/agentbackend/opencode/ -run "TestListSessions|TestGetSession" -count=1 -v
```

Expected: panic from stubs.

### Step 5.5: Implement `sessions.go`

- [ ] **Delete the panicking stubs from `pkg/agentbackend/opencode/backend.go`** (Task 1.7 additions).

- [ ] **Create `pkg/agentbackend/opencode/sessions.go`**:

```go
// Package opencode — session storage reader.
//
// Storage: sqlite at $HOME/.local/share/opencode/opencode.db
// (anomalyco/opencode 1.17.6, captured 2026-06-15).
//
// Relevant tables:
//	session         — id, directory, title, version, time_created, time_updated
//	session_message — id, session_id, type ('user'|'assistant'|…), seq, time_created
//	part            — id, message_id, session_id, time_created, data (JSON {type,text})
//
// The reader opens the DB read-only and uses parameterized SELECTs.
// It does NOT spawn opencode.

package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func sessionsDBPath() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "opencode", "opencode.db")
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// openDB opens the opencode sqlite DB in read-only mode + foreign keys
// off (we don't write). Empty path or missing file returns nil DB +
// nil error so callers can treat both as "no sessions".
func openDB() (*sql.DB, error) {
	p := sessionsDBPath()
	if p == "" {
		return nil, nil
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	// read-only + immutable to avoid creating a WAL file we don't need.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", url.PathEscape(p))
	return sql.Open("sqlite", dsn)
}

func (b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	db, err := openDB()
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, nil
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT id, directory, time_created, time_updated FROM session`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	type rec struct {
		id, dir          string
		created, updated int64
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.dir, &r.created, &r.updated); err != nil {
			continue
		}
		ids = append(ids, r.id)
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch message-count + last assistant text per session in batch.
	counts, lastAsst := loadAggregates(ctx, db, ids)

	out := make([]agentbackend.Session, 0, len(recs))
	for _, r := range recs {
		s := agentbackend.Session{
			ID:           r.id,
			Kind:         agentbackend.KindOpencode,
			WorkingDir:   r.dir,
			StartedAt:    msToTime(r.created),
			UpdatedAt:    msToTime(r.updated),
			MessageCount: counts[r.id],
		}
		if last, ok := lastAsst[r.id]; ok && last != "" {
			s.Preview = truncatePreview(last)
		}
		out = append(out, s)
	}
	// Stable order by UpdatedAt descending for deterministic test output.
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	db, err := openDB()
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	if db == nil {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	defer db.Close()

	var dir string
	var created, updated int64
	err = db.QueryRowContext(ctx,
		`SELECT directory, time_created, time_updated FROM session WHERE id = ?`, id).
		Scan(&dir, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	if err != nil {
		return agentbackend.Session{}, nil, err
	}

	// Join session_message with the message's part rows to get text in seq order.
	rows, err := db.QueryContext(ctx, `
		SELECT m.type, p.data, m.time_created
		FROM session_message m
		JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.seq, p.time_created`, id)
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	defer rows.Close()

	var msgs []agentbackend.SessionMessage
	var lastAsst string
	count := 0
	for rows.Next() {
		var mType, partData string
		var ts int64
		if err := rows.Scan(&mType, &partData, &ts); err != nil {
			continue
		}
		text := extractPartText(partData)
		if text == "" {
			continue
		}
		role := normalizeRole(mType)
		msgs = append(msgs, agentbackend.SessionMessage{
			Role: role, Text: text, Ts: msToTime(ts),
		})
		count++
		if role == "assistant" {
			lastAsst = text
		}
	}

	sess := agentbackend.Session{
		ID:           id,
		Kind:         agentbackend.KindOpencode,
		WorkingDir:   dir,
		StartedAt:    msToTime(created),
		UpdatedAt:    msToTime(updated),
		MessageCount: count,
	}
	if lastAsst != "" {
		sess.Preview = truncatePreview(lastAsst)
	}
	return sess, msgs, nil
}

// loadAggregates batches one COUNT + one "last assistant" query so
// ListSessions is O(2) queries regardless of session count.
func loadAggregates(ctx context.Context, db *sql.DB, ids []string) (counts map[string]int, lastAsst map[string]string) {
	counts = map[string]int{}
	lastAsst = map[string]string{}
	if len(ids) == 0 {
		return
	}
	// Count parts (the part table is what actually has text rows).
	rows, err := db.QueryContext(ctx, `
		SELECT m.session_id, COUNT(*)
		FROM session_message m
		JOIN part p ON p.message_id = m.id
		GROUP BY m.session_id`)
	if err == nil {
		for rows.Next() {
			var sid string
			var n int
			if err := rows.Scan(&sid, &n); err == nil {
				counts[sid] = n
			}
		}
		rows.Close()
	}

	// Last assistant text per session: get the latest assistant message
	// per session_id (by seq) + its first part's text.
	q := `
		SELECT m.session_id, p.data
		FROM session_message m
		JOIN part p ON p.message_id = m.id
		WHERE m.type = 'assistant'
		ORDER BY m.session_id, m.seq, p.time_created`
	rows2, err := db.QueryContext(ctx, q)
	if err != nil {
		return
	}
	defer rows2.Close()
	for rows2.Next() {
		var sid, data string
		if err := rows2.Scan(&sid, &data); err != nil {
			continue
		}
		text := extractPartText(data)
		if text == "" {
			continue
		}
		lastAsst[sid] = text // later rows overwrite earlier → last assistant wins
	}
	return
}

// extractPartText decodes the part.data JSON blob, returning the text
// when type=="text", else "".
func extractPartText(data string) string {
	var p struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(data), &p) != nil {
		return ""
	}
	if p.Type != "text" {
		return ""
	}
	return p.Text
}

// normalizeRole maps opencode's session_message.type to the
// SessionMessage.Role values we promise callers.
func normalizeRole(t string) string {
	switch t {
	case "user", "assistant", "system", "tool":
		return t
	default:
		return t // unknown types pass through verbatim
	}
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func truncatePreview(s string) string {
	const cap = 256
	if len(s) <= cap {
		return s
	}
	out := s[:cap]
	for len(out) > 0 && (out[len(out)-1]&0xC0) == 0x80 {
		out = out[:len(out)-1]
	}
	return out + "…"
}
```

### Step 5.6: Verify GREEN

```bash
go test ./pkg/agentbackend/opencode/ -count=1 -v
go test ./pkg/agentbackend/ -run "TestListSessions_EmptyHomeEveryBackend/opencode|TestGetSession_UnknownIDEveryBackend/opencode" -count=1 -v
go build ./...
go vet ./...
```

Expected: opencode tests + 2 umbrella subtests PASS; build/vet clean.

### Step 5.7: Commit

```bash
git add go.mod go.sum pkg/agentbackend/opencode/sessions.go pkg/agentbackend/opencode/sessions_test.go pkg/agentbackend/opencode/sessions_fixture_test.go pkg/agentbackend/opencode/backend.go
git commit -m "feat(agentbackend/opencode): ListSessions / GetSession via sqlite (PR-1 Task 5)

Reads opencode's on-disk sqlite store at
\$HOME/.local/share/opencode/opencode.db (anomalyco/opencode 1.17.6).
Relevant tables: session, session_message, part. Opens read-only via
modernc.org/sqlite (pure-Go, CGO_ENABLED=0 friendly).

ListSessions: one query per session-row + two aggregate queries for
message count + last assistant text. O(3) queries regardless of N.

GetSession: joins session_message with part ordered by (seq, time)
to reconstruct turn-by-turn history. Unknown id → ErrSessionNotFound.

Corrupt part.data JSON is skipped silently; the session still appears
in the list (with whatever was parseable).

Test fixture is built at test-time by sessions_fixture_test.go —
simpler than checking in a binary .db; same schema as real opencode.

Stubs from Task 1's backend.go removed.

4 unit tests + 2 umbrella subtests turn green. All cross-backend tests
now pass.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Full regression + push + PR

**Files:** none modified beyond verification.

### Step 6.1: Full race regression

```bash
go test ./... -race -count=1 2>&1 | tail -30
```

Expected: all `ok`, no FAIL.

### Step 6.2: Master-path freeze re-verify

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
```

Expected: empty.

### Step 6.3: Binary smoke

```bash
mkdir -p /tmp/bin-smoke-pr1
CGO_ENABLED=0 go build -o /tmp/bin-smoke-pr1/driver-agent ./cmd/driver-agent
CGO_ENABLED=0 go build -o /tmp/bin-smoke-pr1/slave-agent ./cmd/slave-agent
/tmp/bin-smoke-pr1/driver-agent 2>&1 | head -5
```

Expected: builds clean (proves `modernc.org/sqlite` works under CGO_ENABLED=0), help text appears.

### Step 6.4: Probe a real session list on this host (smoke proof)

This isn't strictly required for the PR, but it's a 2-minute sanity check that the readers work on real-world data, not just fixtures.

```bash
cat > /tmp/sessions-smoke.go <<'EOF'
package main

import (
	"context"
	"fmt"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

func main() {
	for _, k := range agentbackend.RegisteredKinds() {
		b, err := agentbackend.New(agentbackend.Config{Kind: agentbackend.Kind(k), Bin: k, WorkDir: "/tmp"}, nil)
		if err != nil {
			fmt.Printf("%s: New err: %v\n", k, err)
			continue
		}
		ss, err := b.ListSessions(context.Background())
		fmt.Printf("%s: %d sessions, err=%v\n", k, len(ss), err)
		for i, s := range ss {
			if i >= 3 {
				break
			}
			fmt.Printf("  %s  cwd=%s  msgs=%d  preview=%q\n", s.ID, s.WorkingDir, s.MessageCount, s.Preview)
		}
	}
}
EOF
go run /tmp/sessions-smoke.go
```

Expected: each kind reports its real session count from this host. Document the counts in the PR description.

### Step 6.5: Push + open PR

```bash
git push -u origin worktree-backend-sessions

gh pr create --base master --head worktree-backend-sessions \
  --title "feat(agentbackend): ListSessions / GetSession across 3 backends (commander PR-1)" \
  --body "$(cat <<'EOF'
PR-1 of the 3-PR commander-web-entry vision ([docs/vision/commander-web-entry.md](docs/vision/commander-web-entry.md)). Adds the read-side session API that a future daemon (PR-2) and observer UI (PR-3) consume.

## Scope

- `pkg/agentbackend/backend.go`: gain `Session`, `SessionMessage`, `ErrSessionNotFound`, `sessionPreviewMaxBytes`; extend `Backend` interface with `ListSessions` + `GetSession`.
- `pkg/agentbackend/claude/sessions.go`: scan `$HOME/.claude/projects/<encoded_cwd>/*.jsonl`.
- `pkg/agentbackend/codex/sessions.go`: walk `$HOME/.codex/sessions/<y>/<m>/<d>/rollout-*.jsonl`.
- `pkg/agentbackend/opencode/sessions.go`: query `$HOME/.local/share/opencode/opencode.db` (sqlite, pure-Go `modernc.org/sqlite`).

No subprocess invocation. No network. Empty storage → empty list + nil err. Corrupt entries → skipped silently. Unknown id → `ErrSessionNotFound`.

## Why

[commander-web-entry vision](docs/vision/commander-web-entry.md) §3.3 fixes session ownership in the backend's own persistence — daemon / observer / web all read through this single interface. This PR is the read-only foundation; daemon (PR-2) and observer UI (PR-3) add the transport + UI on top.

## Master-path freeze (honored)

`git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'` → **empty**.

## Testing

- Per-backend: list+get with fixture HOME (claude jsonl, codex jsonl walk, opencode sqlite built at test-time)
- Cross-backend umbrella: empty-HOME tolerance + unknown-id → ErrSessionNotFound for every registered backend
- Full repo: `go test ./... -race -count=1` all green
- Binary smoke under `CGO_ENABLED=0` (proves `modernc.org/sqlite` is pure-Go)
- Real-host smoke: ran ListSessions against this host's actual claude/codex/opencode sessions, found N/M/K (see commit body)

## Out of scope (intentional)

- daemon mode (PR-2)
- observer reverse proxy + web UI (PR-3)
- session write APIs beyond what `Backend.RunResume` already provides
- session delete / rename
- backend CLI subprocess invocation
- master path (frozen)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage:**
- Spec invariant 1 (interface contract) → Task 1
- Invariant 2 (zero network / no subprocess) → Tasks 3/4/5 (each impl uses only file I/O or in-process sqlite)
- Invariant 3 (empty-dir → nil) → Tasks 3/4/5 + Task 2 umbrella
- Invariant 4 (corrupt entries skipped) → each backend's Test*TolertaesCorruptFile
- Invariant 5 (ErrSessionNotFound) → each backend + Task 2 umbrella
- Invariant 6 (WorkingDir from session-recorded cwd, not file path) → claude decodes parent dir; codex reads session_meta; opencode reads `session.directory`
- Invariant 7 (existing interface methods unchanged) → Task 1 explicitly preserves them in the replacement block

**Placeholder scan:** No TBD/TODO. The "implementer may need to PRAGMA table_info(session) first" hint in the pre-flight section is genuine information, not a placeholder — opencode column names are documented in pre-flight findings but a quick PRAGMA at the top of Task 5 is cheap insurance.

**Type / signature consistency:**
- `Session` / `SessionMessage` / `ErrSessionNotFound` — defined Task 1, used identically in Tasks 2/3/4/5
- `(b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error)` — same signature in all 3 backends + stubs + nilBackend
- `(b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error)` — same signature in all 3 backends + stubs + nilBackend
- `truncatePreview` helper — copied (verbatim) into each backend's sessions.go (intentional small duplication to avoid creating a shared sub-package for 8 lines)
- `copyFixtureToHOME` + `copyTree` — duplicated in claude/codex test files; opencode uses `buildFixtureDB` instead since its fixture is a sqlite DB

**One risk worth noting in advance:** the test-only `_ "modernc.org/sqlite"` import in Task 5's fixture file pulls the driver into the test binary. The same import in production `sessions.go` (Task 5 step 5.5) pulls it into the daemon binary. If someone later wants the opencode package without sqlite (e.g. a slim cross-compile), they can't easily; that's an acceptable PR-1 tradeoff because the spec mandates `opencode` enumerate sessions and the only source is sqlite.
