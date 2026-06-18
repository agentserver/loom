# Commander Exec Session Parent Link — P1 (Backend record + scanner + CODEX_HOME isolation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist a parent link for `codex_exec` agent-task sessions in a per-agent-`CODEX_HOME` sidecar, and have the Codex session scanner merge it into `agentbackend.Session` so that downstream (P2 propagation, P3 Commander nesting) can render remote tasks under their originating session.

**Architecture:** Each agent runs its own `CODEX_HOME`. The codex executor writes a `$CODEX_HOME/loom-meta/<thread_id>.json` sidecar right after it captures the new codex thread id. The codex scanner reads `CODEX_HOME` (was hard-coded to `$HOME/.codex`) and merges any matching sidecar into the scanned `Session`, setting `Origin=agent_task` + `ParentID` + `ParentAgentID`. No driver/slave wiring or UI here — propagation (P2) and nesting (P3) are separate follow-on plans. P1 is fully testable with hand-seeded fixtures.

**Tech Stack:** Go (stdlib only — `encoding/json`, `os`, `path/filepath`, `time`), table-driven tests, `go test -race`.

**Spec:** `multi-agent/docs/superpowers/specs/2026-06-17-commander-exec-session-parent-link-design.md`

---

## File Structure

- `pkg/agentbackend/backend.go` — add `ParentAgentID` to `Session`; (no new file).
- `pkg/agentbackend/config.go` — add `CodexHome` field to `Config` (flat carrier, alongside `WorkerMode`).
- `pkg/agentbackend/codex/sessions.go` — `sessionsRoot()` honors `CODEX_HOME`; merge sidecar in List/Get; cache key includes sidecar mtime.
- `pkg/agentbackend/codex/loommeta.go` — **NEW**: sidecar type, path resolver, read helper, writer, reaper.
- `pkg/agentbackend/codex/loommeta_test.go` — **NEW**: unit tests for the sidecar helper.
- `pkg/agentbackend/codex/sessions_test.go` — add sidecar-merge + `CODEX_HOME` tests.
- `pkg/agentbackend/codex/executor.go` — inject `CODEX_HOME` into `e.env`; write sidecar after thread capture.
- `pkg/agentbackend/codex/backend.go` — resolve `CodexHome` from cfg/env, pass to executor + scanner.
- `internal/executor/executor.go` — add `ParentSessionID`/`ParentAgentID`/`ParentDisplayName` to `Task`.

---

## Task 1: Add `ParentAgentID` to the `Session` model

**Files:**
- Modify: `pkg/agentbackend/backend.go`

- [ ] **Step 1: Add the field**

In `pkg/agentbackend/backend.go`, inside `type Session struct`, directly after the existing `ParentID` field (around line 117):

```go
	// ParentAgentID is the ShortID of the agent instance (daemon) that owns
	// ParentID. Empty when ParentID is empty. Lets the Commander observer
	// resolve a parent across reconnects: daemon_id is ephemeral, agent_id
	// (ShortID) is stable. See loom #24 spec.
	ParentAgentID string
```

- [ ] **Step 2: Build & vet**

Run: `go build ./pkg/agentbackend/...`
Expected: succeeds (additive field; zero value is empty).

- [ ] **Step 3: Commit**

```bash
git add pkg/agentbackend/backend.go
git commit -m "feat(agentbackend): add Session.ParentAgentID for parent linkage (#24)"
```

---

## Task 2: Add `CodexHome` to `Config`

**Files:**
- Modify: `pkg/agentbackend/config.go`

- [ ] **Step 1: Add the field**

In `pkg/agentbackend/config.go`, inside `type Config struct`, after `WorkerMode`:

```go
	// CodexHome overrides the codex data directory passed as CODEX_HOME to
	// the codex subprocess and read by the session scanner. Per-agent
	// isolation (loom #24). Empty = resolve CODEX_HOME from env / fall back
	// to $HOME/.codex.
	CodexHome string `yaml:"codex_home"`
```

- [ ] **Step 2: Build**

Run: `go build ./pkg/agentbackend/...`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add pkg/agentbackend/config.go
git commit -m "feat(agentbackend): add Config.CodexHome for per-agent isolation (#24)"
```

---

## Task 3: Sidecar helper — `loommeta.go` (type, path, read, reaper)

**Files:**
- Create: `pkg/agentbackend/codex/loommeta.go`
- Create: `pkg/agentbackend/codex/loommeta_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/agentbackend/codex/loommeta_test.go`:

```go
package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoomMetaDir(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/codex-home-xyz")
	if got, want := loomMetaDir(), filepath.Join("/tmp/codex-home-xyz", "loom-meta"); got != want {
		t.Fatalf("loomMetaDir() = %q, want %q", got, want)
	}
}

func TestLoomMetaDir_FallbackHome(t *testing.T) {
	// Unset CODEX_HOME so resolver falls back to $HOME/.codex.
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := loomMetaDir(), filepath.Join(home, ".codex", "loom-meta"); got != want {
		t.Fatalf("loomMetaDir() = %q, want %q", got, want)
	}
}

func TestLoomMetaPath(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/codex-home-xyz")
	if got, want := loomMetaPath("thread-123"), filepath.Join("/tmp/codex-home-xyz", "loom-meta", "thread-123.json"); got != want {
		t.Fatalf("loomMetaPath() = %q, want %q", got, want)
	}
}

func TestWriteLoomMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	in := loomMeta{
		Schema:            loomMetaSchema,
		SessionID:        "thread-1",
		ParentSessionID:  "parent-thread",
		ParentAgentID:    "drv-abc",
		ParentDisplayName: "prod-driver",
		Origin:           "agent_task",
		Kind:             "codex",
		CreatedAt:        "2026-06-17T00:00:00Z",
	}
	if err := writeLoomMeta(in); err != nil {
		t.Fatalf("writeLoomMeta: %v", err)
	}
	out, ok := readLoomMeta("thread-1")
	if !ok {
		t.Fatal("readLoomMeta: not found")
	}
	if out.ParentAgentID != "drv-abc" || out.ParentDisplayName != "prod-driver" || out.Origin != "agent_task" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestReadLoomMeta_Missing(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, ok := readLoomMeta("nope"); ok {
		t.Fatal("expected not found")
	}
}

func TestReadLoomMeta_CorruptSkipped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	_ = os.MkdirAll(loomMetaDir(), 0o755)
	_ = os.WriteFile(loomMetaPath("bad"), []byte("{not json"), 0o600)
	if _, ok := readLoomMeta("bad"); ok {
		t.Fatal("corrupt sidecar should be skipped, not returned")
	}
}

func TestReaper_RemovesOrphans(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	_ = os.MkdirAll(loomMetaDir(), 0o755)
	live := loomMeta{Schema: loomMetaSchema, SessionID: "live-thread", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T00:00:00Z"}
	dead := loomMeta{Schema: loomMetaSchema, SessionID: "dead-thread", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T00:00:00Z"}
	_ = writeLoomMeta(live)
	_ = writeLoomMeta(dead)
	reaper(dir, []string{"live-thread"}) // only live-thread still has a rollout
	if _, ok := readLoomMeta("live-thread"); !ok {
		t.Fatal("live sidecar must survive reaper")
	}
	if _, ok := readLoomMeta("dead-thread"); ok {
		t.Fatal("orphaned sidecar must be removed")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'LoomMeta|Reaper' -v`
Expected: FAIL / build error — `loomMeta`, `loomMetaDir`, etc. undefined.

- [ ] **Step 3: Implement `loommeta.go`**

Create `pkg/agentbackend/codex/loommeta.go`:

```go
package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// loomMetaSchema is the sidecar schema version. Bump only on incompatible
// shape changes.
const loomMetaSchema = 1

// loomMeta is the on-disk sidecar written next to a codex exec session. It
// records the parent link that codex itself does not know (the originating
// driver/slave agent + session). Read back by the session scanner so the
// Session descriptor carries ParentID / ParentAgentID.
//
// Path: $CODEX_HOME/loom-meta/<thread_id>.json  (see loom #24 spec §4).
type loomMeta struct {
	Schema            string `json:"schema"`
	SessionID         string `json:"session_id"`
	ParentSessionID   string `json:"parent_session_id,omitempty"`
	ParentAgentID     string `json:"parent_agent_id,omitempty"`
	ParentDisplayName string `json:"parent_display_name,omitempty"`
	Origin            string `json:"origin"`
	Kind              string `json:"kind"`
	CreatedAt         string `json:"created_at"`
}

// loomMetaDir returns the directory holding all sidecars, honoring CODEX_HOME
// (the same root the codex subprocess writes sessions under). Falls back to
// $HOME/.codex when CODEX_HOME is unset (backward compat).
func loomMetaDir() string {
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".codex")
	}
	return filepath.Join(base, "loom-meta")
}

func loomMetaPath(threadID string) string {
	return filepath.Join(loomMetaDir(), threadID+".json")
}

// writeLoomMeta writes one sidecar, best-effort: missing dir is created.
// Returns an error only on filesystem failure; callers MUST treat failure as
// non-fatal (session still lists; just without the parent link).
func writeLoomMeta(m loomMeta) error {
	dir := loomMetaDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(loomMetaPath(m.SessionID), b, 0o600)
}

// readLoomMeta returns the sidecar for threadID. ok is false when absent or
// unparseable (corrupt entries are skipped silently, mirroring the scanner's
// own skip-on-error policy).
func readLoomMeta(threadID string) (loomMeta, bool) {
	var m loomMeta
	b, err := os.ReadFile(loomMetaPath(threadID))
	if err != nil {
		return m, false
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, false
	}
	return m, true
}

// reaper removes sidecars whose thread id is not in liveThreadIDs. Called by
// the scanner after a full List (which already enumerates live thread ids).
func reaper(root string, liveThreadIDs []string) {
	dir := loomMetaDir()
	if dir == "" {
		return
	}
	live := make(map[string]struct{}, len(liveThreadIDs))
	for _, id := range liveThreadIDs {
		live[id] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		id, ok := threadIDFromLoomMetaName(name)
		if !ok {
			continue
		}
		if _, ok := live[id]; ok {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// threadIDFromLoomMetaName turns "thread-1.json" -> ("thread-1", true).
func threadIDFromLoomMetaName(name string) (string, bool) {
	const suffix = ".json"
	if len(name) <= len(suffix) || name[len(name)-len(suffix):] != suffix {
		return "", false
	}
	return name[:len(name)-len(suffix)], true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'LoomMeta|Reaper' -v -race`
Expected: PASS (all six tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/codex/loommeta.go pkg/agentbackend/codex/loommeta_test.go
git commit -m "feat(codex): add loom-meta sidecar helper (#24)"
```

---

## Task 4: Scanner honors `CODEX_HOME`

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go` (`sessionsRoot`)
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/agentbackend/codex/sessions_test.go` (a new test function):

```go
func TestSessionsRoot_HonorsCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/codex-home-zzz")
	root := sessionsRoot()
	if want := filepath.Join("/tmp/codex-home-zzz", "sessions"); root != want {
		t.Fatalf("sessionsRoot() = %q, want %q", root, want)
	}
}

func TestSessionsRoot_FallbackHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := sessionsRoot()
	if want := filepath.Join(home, ".codex", "sessions"); root != want {
		t.Fatalf("sessionsRoot() = %q, want %q", root, want)
	}
}
```

If the test file does not already import `path/filepath`, add it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'SessionsRoot' -v`
Expected: FAIL — current `sessionsRoot()` ignores `CODEX_HOME`, returns `$HOME/.codex/sessions`.

- [ ] **Step 3: Update `sessionsRoot()`**

In `pkg/agentbackend/codex/sessions.go`, replace the existing `sessionsRoot()` function (currently lines 34-40):

```go
func sessionsRoot() string {
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".codex")
	}
	return filepath.Join(base, "sessions")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'SessionsRoot' -v -race`
Expected: PASS.

- [ ] **Step 5: Run the full codex package to confirm no regression**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS (existing scanner tests still pass — they redirect `HOME`, and `CODEX_HOME` unset falls back to the old behavior).

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "fix(codex): sessions scanner honors CODEX_HOME (#24)"
```

---

## Task 5: Scanner merges sidecar into the scanned `Session`

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go` (`ListSessions`, `scanCodexSession`)
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/agentbackend/codex/sessions_test.go`. This test seeds a rollout + sidecar under a temp `CODEX_HOME` and asserts the parent link is merged.

```go
func TestListSessions_MergesLoomMetaSidecar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)

	// Minimal valid codex rollout: one session_meta line.
	dayDir := filepath.Join(home, "sessions", "2026", "06", "17")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "deadbeef-0000-0000-0000-000000000001"
	rollout := filepath.Join(dayDir, "rollout-2026-06-17T10-00-00Z-"+threadID+".jsonl")
	meta := `{"timestamp":"2026-06-17T10:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/proj","originator":"codex_exec"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}

	// Sidecar with a parent link.
	if err := writeLoomMeta(loomMeta{
		Schema:            loomMetaSchema,
		SessionID:         threadID,
		ParentSessionID:   "parent-thread-aaa",
		ParentAgentID:     "drv-abc",
		ParentDisplayName: "prod-driver",
		Origin:            "agent_task",
		Kind:              "codex",
		CreatedAt:         "2026-06-17T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	b := &Backend{list: sessioncache.NewFileCache()}
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d (%+v)", len(got), got)
	}
	s := got[0]
	if s.Origin != agentbackend.SessionOriginAgentTask {
		t.Errorf("Origin = %q, want agent_task", s.Origin)
	}
	if s.ParentID != "parent-thread-aaa" {
		t.Errorf("ParentID = %q, want parent-thread-aaa", s.ParentID)
	}
	if s.ParentAgentID != "drv-abc" {
		t.Errorf("ParentAgentID = %q, want drv-abc", s.ParentAgentID)
	}
}
```

Ensure the test file imports `"context"` and `"github.com/yourorg/multi-agent/pkg/agentbackend"`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListSessions_MergesLoomMetaSidecar' -v`
Expected: FAIL — `Origin` is `agent_task` (scanner already classifies `codex_exec`) but `ParentID`/`ParentAgentID` are empty (no merge yet).

- [ ] **Step 3: Merge the sidecar in the scan path**

In `pkg/agentbackend/codex/sessions.go`, add a helper near `applyCodexSessionMeta`:

```go
// applyLoomMeta overlays sidecar parent/origin data onto a session. A missing
// sidecar is a no-op (the session lists without a parent link). Only non-empty
// fields overwrite, so a sidecar never erases codex-native subagent linkage.
func applyLoomMeta(sess *agentbackend.Session) {
	m, ok := readLoomMeta(sess.ID)
	if !ok {
		return
	}
	if m.Origin == string(agentbackend.SessionOriginAgentTask) && sess.Origin == "" {
		sess.Origin = agentbackend.SessionOriginAgentTask
	}
	if m.Origin != "" && sess.Origin != agentbackend.SessionOriginSubagent {
		// codex-native subagent classification wins; otherwise trust sidecar.
		if m.Origin == string(agentbackend.SessionOriginAgentTask) {
			sess.Origin = agentbackend.SessionOriginAgentTask
		}
	}
	if sess.ParentID == "" {
		sess.ParentID = m.ParentSessionID
	}
	if sess.ParentAgentID == "" {
		sess.ParentAgentID = m.ParentAgentID
	}
}
```

Then call it from `scanCodexSession`, right before `return res` at the end of the function (after the read loop, after `Preview` is set). Find the existing tail:

```go
	if lastAssistantText != "" {
		res.session.Preview = truncatePreview(lastAssistantText)
	}
	return res
}
```

Replace with:

```go
	if lastAssistantText != "" {
		res.session.Preview = truncatePreview(lastAssistantText)
	}
	applyLoomMeta(&res.session)
	return res
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListSessions_MergesLoomMetaSidecar' -v -race`
Expected: PASS.

- [ ] **Step 5: Add a "no sidecar = no parent" regression test**

Append:

```go
func TestListSessions_NoSidecarLeavesParentEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dayDir := filepath.Join(home, "sessions", "2026", "06", "17")
	_ = os.MkdirAll(dayDir, 0o755)
	threadID := "deadbeef-0000-0000-0000-000000000002"
	rollout := filepath.Join(dayDir, "rollout-2026-06-17T11-00-00Z-"+threadID+".jsonl")
	meta := `{"timestamp":"2026-06-17T11:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/proj","originator":"codex_exec"}}` + "\n"
	_ = os.WriteFile(rollout, []byte(meta), 0o600)

	b := &Backend{list: sessioncache.NewFileCache()}
	got, _ := b.ListSessions(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	if got[0].ParentID != "" || got[0].ParentAgentID != "" {
		t.Errorf("no sidecar should leave parent empty: %+v", got[0])
	}
	if got[0].Origin != agentbackend.SessionOriginAgentTask {
		t.Errorf("Origin = %q, still agent_task without sidecar", got[0].Origin)
	}
}
```

- [ ] **Step 6: Run the full codex package**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "feat(codex): merge loom-meta sidecar into scanned sessions (#24)"
```

---

## Task 6: Scanner cache key includes sidecar mtime

**Why:** `Backend.list` (`sessioncache.FileCache`) caches descriptors keyed on rollout `(path,size,mtime)`. A sidecar written after the rollout would not invalidate the row. Include the sidecar's mtime in the lookup so a rewritten sidecar refreshes the descriptor.

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go` (`ListSessions`)
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Inspect the cache helper signature**

Run: `grep -n 'func.*Get\|func.*NewFileCache' pkg/agentbackend/internal/sessioncache/*.go internal/sessioncache/*.go 2>/dev/null`
Expected: the `Get(path string, info fs.FileInfo, build func() V) V` signature. (Confirm the exact parameter list before editing.)

> If `FileCache.Get` keys only on `(path, size, modtime)` and has no overload to add a salt, the simplest correct change is to fold the sidecar mtime into the **path** key passed to `Get` (it is opaque to the cache) OR to invalidate after a sidecar write. Choose the smaller diff; the test below pins behavior, not implementation.

- [ ] **Step 2: Write the failing test**

Append:

```go
func TestListCache_InvalidatedBySidecarRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dayDir := filepath.Join(home, "sessions", "2026", "06", "17")
	_ = os.MkdirAll(dayDir, 0o755)
	threadID := "deadbeef-0000-0000-0000-000000000003"
	rollout := filepath.Join(dayDir, "rollout-2026-06-17T12-00-00Z-"+threadID+".jsonl")
	_ = os.WriteFile(rollout, []byte(`{"timestamp":"2026-06-17T12:00:00Z","type":"session_meta","payload":{"id":"`+threadID+`","cwd":"/p","originator":"codex_exec"}}`+"\n"), 0o600)

	b := &Backend{list: sessioncache.NewFileCache()}
	first, _ := b.ListSessions(context.Background())
	if first[0].ParentAgentID != "" {
		t.Fatalf("precondition: no sidecar yet, got %q", first[0].ParentAgentID)
	}

	// Write sidecar, then re-list — cache must reflect the new parent.
	_ = writeLoomMeta(loomMeta{Schema: loomMetaSchema, SessionID: threadID, ParentAgentID: "drv-xyz", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T12:00:00Z"})
	second, _ := b.ListSessions(context.Background())
	if second[0].ParentAgentID != "drv-xyz" {
		t.Fatalf("cache not invalidated by sidecar write: ParentAgentID=%q", second[0].ParentAgentID)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListCache_InvalidatedBySidecarRewrite' -v`
Expected: FAIL — second read still returns the cached (parent-less) descriptor.

- [ ] **Step 4: Implement**

In `ListSessions`, when computing the key for each `.jsonl`, also stat the sidecar and incorporate its mtime into the cache key passed to `b.list.Get`. Concretely, in the `WalkDir` body, change the cache lookup to key on a composite string when a sidecar exists. Minimal approach — before the existing `session := b.list.Get(path, info, ...)` call, append the sidecar mtime to `path` only for the cache key (the build closure still reads the real rollout path):

```go
		id := sessionIDFromFilename(entry.Name())
		if id == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		seen[path] = struct{}{}
		cacheKey := path
		if si, err := os.Stat(loomMetaPath(id)); err == nil {
			cacheKey = cacheKey + "|" + si.ModTime().Format(time.RFC3339Nano)
		}
		session := b.list.Get(cacheKey, info, func() agentbackend.Session {
			return scanCodexSession(path, id, false).session
		})
		out = append(out, session)
```

Add `"time"` to the imports if not present. Run `gofmt -w` on the file.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListCache_InvalidatedBySidecarRewrite' -v -race`
Expected: PASS.

- [ ] **Step 6: Full package + race**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "fix(codex): invalidate session cache on sidecar rewrite (#24)"
```

---

## Task 7: Scanner runs the reaper after a full `ListSessions`

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go` (`ListSessions`)
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestListSessions_ReapsOrphanSidecars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	// Sidecar with NO matching rollout -> orphan.
	_ = writeLoomMeta(loomMeta{Schema: loomMetaSchema, SessionID: "orphan-thread", ParentAgentID: "drv", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T00:00:00Z"})

	b := &Backend{list: sessioncache.NewFileCache()}
	_, _ = b.ListSessions(context.Background())

	if _, ok := readLoomMeta("orphan-thread"); ok {
		t.Fatal("orphan sidecar should be reaped after ListSessions")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListSessions_ReapsOrphanSidecars' -v`
Expected: FAIL — orphan still present.

- [ ] **Step 3: Call the reaper**

In `ListSessions`, after the `WalkDir` completes and before `b.list.Prune(seen)`, collect the live thread ids (they're already enumerated — build the slice from `seen` is wrong since `seen` is keyed by path; instead accumulate thread ids during the walk). Add a local accumulator:

At the top of `ListSessions`, alongside `seen`:

```go
	var out []agentbackend.Session
	seen := map[string]struct{}{}
	liveThreadIDs := make([]string, 0)
```

Inside the walk body, where `id` is known (right after `seen[path] = struct{}{}`), also:

```go
		liveThreadIDs = append(liveThreadIDs, id)
```

Then, before `b.list.Prune(seen)`, prune sidecars:

```go
	reaper(root, liveThreadIDs)
	b.list.Prune(seen)
```

(Do the same live-id accumulation in `GetSession`'s walk if needed for parity; `GetSession` targets a single id, so it does not need the reaper.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListSessions_ReapsOrphanSidecars|Reaper' -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "feat(codex): reap orphan loom-meta sidecars during scan (#24)"
```

---

## Task 8: Add parent fields to `executor.Task`

**Files:**
- Modify: `internal/executor/executor.go`

- [ ] **Step 1: Add the fields**

In `internal/executor/executor.go`, inside `type Task struct` (lines 5-11), append after `TimeoutSec`:

```go
	// ParentSessionID / ParentAgentID / ParentDisplayName carry the origin
	// link for a codex exec session spawned by a driver/slave. Empty for
	// sessions with no parent (direct interactive). Read by the codex
	// backend executor to write the loom-meta sidecar. See loom #24.
	ParentSessionID   string
	ParentAgentID     string
	ParentDisplayName string
```

- [ ] **Step 2: Build**

Run: `go build ./internal/executor/... ./pkg/agentbackend/...`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/executor/executor.go
git commit -m "feat(executor): add parent link fields to Task (#24)"
```

---

## Task 9: Codex executor writes the sidecar after thread capture

**Files:**
- Modify: `pkg/agentbackend/codex/executor.go`
- Test: `pkg/agentbackend/codex/executor_test.go`

This plan reuses the existing fake-codex harness already in `executor_test.go`: `writeFakeCodex(t, []string{...})` (emits stream-json frames and exits 0), `captureSink` (`Write`/`Close`), and `newExecutor(cfg, env)`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/agentbackend/codex/executor_test.go`:

```go
func TestCodexExecutorWritesLoomMetaSidecar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-sidecar"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil)
	res, err := ex.Run(context.Background(), agentbackend.Task{
		Prompt:            "hi",
		ParentSessionID:   "parent-t",
		ParentAgentID:     "drv-1",
		ParentDisplayName: "prod-driver",
	}, &captureSink{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionID != "thr-sidecar" {
		t.Fatalf("SessionID = %q, want thr-sidecar", res.SessionID)
	}
	m, ok := readLoomMeta("thr-sidecar")
	if !ok {
		t.Fatal("sidecar not written after thread capture")
	}
	if m.SessionID != "thr-sidecar" || m.ParentAgentID != "drv-1" || m.ParentDisplayName != "prod-driver" {
		t.Fatalf("sidecar mismatch: %+v", m)
	}
	if m.ParentSessionID != "parent-t" {
		t.Errorf("ParentSessionID = %q, want parent-t", m.ParentSessionID)
	}
	if m.Origin != "agent_task" {
		t.Errorf("Origin = %q, want agent_task", m.Origin)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'TestCodexExecutorWritesLoomMetaSidecar' -v`
Expected: FAIL — `readLoomMeta` returns `ok=false` (no sidecar written today).

- [ ] **Step 3: Capture a timestamp and write the sidecar**

In `pkg/agentbackend/codex/executor.go`, inside `runWithArgv`, the thread id is captured at:

```go
		if ev.Type == "thread.started" && sessionID == "" && ev.ThreadID != "" {
			sessionID = ev.ThreadID
			continue
		}
```

First, declare a local timestamp variable at the top of the scan loop (so the sidecar gets a deterministic `created_at` from the event, not `time.Now()`). Above `for sc.Scan() {`, add:

```go
	var lastLineTimestamp string
```

Then, inside the loop where `ts := parseTimestamp(ln.Timestamp)` is computed (the line is currently in the `switch`/scan body), also record the raw timestamp string. Add right before or after the `ts :=` assignment:

```go
			if ln.Timestamp != "" {
				lastLineTimestamp = ln.Timestamp
			}
```

Now change the thread-capture block to write the sidecar:

```go
		if ev.Type == "thread.started" && sessionID == "" && ev.ThreadID != "" {
			sessionID = ev.ThreadID
			// Best-effort parent-link sidecar. Failure is non-fatal: the
			// session still lists; it just lacks a parent link. created_at
			// comes from the captured event timestamp, not time.Now(), so
			// the write is deterministic.
			_ = writeLoomMeta(loomMeta{
				Schema:            loomMetaSchema,
				SessionID:         sessionID,
				ParentSessionID:   t.ParentSessionID,
				ParentAgentID:     t.ParentAgentID,
				ParentDisplayName: t.ParentDisplayName,
				Origin:            string(agentbackend.SessionOriginAgentTask),
				Kind:              "codex",
				CreatedAt:         lastLineTimestamp,
			})
			continue
		}
```

> If `ts := parseTimestamp(ln.Timestamp)` is computed inside the `switch ln.Type` only, hoist the `lastLineTimestamp = ln.Timestamp` capture to the top of the `for` body (after the `json.Unmarshal` of `ln`) so it always runs regardless of event type. Read the surrounding code first and place it consistently.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/agentbackend/codex/ -run 'TestCodexExecutorWritesLoomMetaSidecar' -v -race`
Expected: PASS.

- [ ] **Step 5: Full package**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/executor.go pkg/agentbackend/codex/executor_test.go
git commit -m "feat(codex): write loom-meta sidecar after thread capture (#24)"
```

---

## Task 10: Resolve & inject `CODEX_HOME` from cfg/env into the codex subprocess

**Why:** `executor_test.go` (`TestCodexExecutorRunResumePreservesExtraArgsEnvAndHumanloopMCP`, lines 287-345) already proves that a `CODEX_HOME=…` entry passed via `newExecutor(cfg, env)` reaches the codex subprocess (`e.env` → `cmd.Env`). What's missing is the *resolution* of `cfg.CodexHome` → `CODEX_HOME` env in `New`, so callers set one config field instead of hand-building env. The app-server manager shares the same `env` (`newAppServerManager(cfg, env)`), so it inherits the same `CODEX_HOME` for free.

**Files:**
- Modify: `pkg/agentbackend/codex/backend.go` (`New`, add `resolveCodexHome`/`withCodexHome`/`codexHome`)
- Test: `pkg/agentbackend/codex/backend_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/agentbackend/codex/backend_test.go` (create the file if absent):

```go
package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestResolveCodexHome_FromConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", "") // ensure not already isolated by env
	t.Setenv("LOOM_HOME", "")
	t.Setenv("LOOM_AGENT_SHORT_ID", "")
	got := resolveCodexHome(agentbackend.Config{CodexHome: "/tmp/codex-home-cfg"})
	if got != "/tmp/codex-home-cfg" {
		t.Fatalf("resolveCodexHome(cfg.CodexHome) = %q, want /tmp/codex-home-cfg", got)
	}
}

func TestResolveCodexHome_FromLoomHomeShortID(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("LOOM_HOME", "/tmp/loom")
	t.Setenv("LOOM_AGENT_SHORT_ID", "drv-1")
	got := resolveCodexHome(agentbackend.Config{})
	if want := filepath.Join("/tmp/loom", "drv-1", ".codex"); got != want {
		t.Fatalf("resolveCodexHome() = %q, want %q", got, want)
	}
}

func TestResolveCodexHome_EmptyWhenUnset(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("LOOM_HOME", "")
	t.Setenv("LOOM_AGENT_SHORT_ID", "")
	if got := resolveCodexHome(agentbackend.Config{}); got != "" {
		t.Fatalf("resolveCodexHome() = %q, want empty (fall back to $HOME/.codex in scanner)", got)
	}
}

func TestNew_InjectsCodexHomeIntoSubprocess(t *testing.T) {
	codexHome := t.TempDir()
	// Reuse the resume env-probe fake (executor_test.go) pattern: a fake
	// codex that writes its CODEX_HOME env to a marker file.
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	bin := buildFakeCodex(t, fmt.Sprintf(`package main
import ("io";"os")
func main() {
	_ = os.WriteFile(%q, []byte(os.Getenv("CODEX_HOME")), 0600)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
`, envPath))
	b := New(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: codexHome}, nil)
	if got := b.codexHome(); got != codexHome {
		t.Fatalf("b.codexHome() = %q, want %q", got, codexHome)
	}
	if _, err := b.RunResume(context.Background(), "thr-1", "ok", &captureSink{}); err != nil {
		// RunResume's codex resume path also inherits e.env.
	}
	got, _ := os.ReadFile(envPath)
	if string(got) != codexHome {
		t.Fatalf("subprocess CODEX_HOME = %q, want %q", string(got), codexHome)
	}
}
```

(`buildFakeCodex`, `captureSink`, and `writeFakeCodex` live in `executor_test.go`, same package, so they're directly callable.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'ResolveCodexHome|New_InjectsCodexHome' -v`
Expected: FAIL — `resolveCodexHome` / `b.codexHome()` undefined.

- [ ] **Step 3: Implement resolution + injection in `New`**

In `pkg/agentbackend/codex/backend.go`, modify `New` and add the helpers:

```go
package codex

import (
	"context"
	"os"
	"path/filepath"

	"github.com/yourorg/multi-agent/internal/sessioncache"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// ... existing code ...

// New returns a fully-assembled Codex Backend. (Replaces the throwaway
// `New(...) *executor` stub — see the original doc comment.)
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "codex"
	}
	env = withCodexHome(cfg, env)
	return &Backend{
		cfg:  cfg,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
		list: sessioncache.NewFileCache(),
	}
}

// withCodexHome ensures the codex subprocess (exec AND app-server, both of
// which consume the same env) and the in-process scanner all see one
// CODEX_HOME. Resolution order: cfg.CodexHome -> existing CODEX_HOME env ->
// $LOOM_HOME/<short_id>/.codex -> "" (scanner then falls back to $HOME/.codex).
// Returns env unchanged when nothing resolves, preserving current behavior.
func withCodexHome(cfg agentbackend.Config, env []string) []string {
	resolved := resolveCodexHome(cfg)
	if resolved == "" {
		return env
	}
	out := append([]string(nil), env...)
	out = append(out, "CODEX_HOME="+resolved)
	return out
}

func resolveCodexHome(cfg agentbackend.Config) string {
	if cfg.CodexHome != "" {
		return cfg.CodexHome
	}
	if existing := os.Getenv("CODEX_HOME"); existing != "" {
		return existing
	}
	if loom := os.Getenv("LOOM_HOME"); loom != "" {
		if sid := os.Getenv("LOOM_AGENT_SHORT_ID"); sid != "" {
			return filepath.Join(loom, sid, ".codex")
		}
	}
	return ""
}

// codexHome returns the effective CODEX_HOME this backend resolved (for tests).
func (b *Backend) codexHome() string { return resolveCodexHome(b.cfg) }
```

(`"path/filepath"` is a new import for `backend.go`.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'ResolveCodexHome|New_InjectsCodexHome' -v -race`
Expected: PASS.

- [ ] **Step 5: Full package + vet**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1 && go vet ./pkg/agentbackend/...`
Expected: PASS, no vet warnings.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/backend.go pkg/agentbackend/codex/backend_test.go
git commit -m "feat(codex): resolve and inject CODEX_HOME per agent (#24)"
```

---

## Task 11: Whole-repo regression

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 2: Race the full suite**

Run: `go test ./... -race -count=1`
Expected: PASS (no regressions in driver/slave/commander; the new fields are additive).

- [ ] **Step 3: Commit any final formatting**

Run: `gofmt -w pkg/agentbackend/codex/ internal/executor/executor.go && git diff --exit-code`
If `git diff` shows formatting-only changes:

```bash
git add -u
git commit -m "chore: gofmt loom-meta additions"
```

---

## Acceptance for P1

- `agentbackend.Session` has `ParentAgentID`; `Config` has `CodexHome`; `executor.Task` has the three parent fields.
- The codex scanner reads `CODEX_HOME` (fallback `$HOME/.codex`); merges a `$CODEX_HOME/loom-meta/<thread_id>.json` sidecar to set `Origin=agent_task` + `ParentID` + `ParentAgentID`; cache invalidates on sidecar rewrite; orphans reaped.
- The codex executor writes the sidecar right after capturing the thread id, using `Task` parent fields; `CODEX_HOME` resolved from cfg/env is injected into the codex subprocess env.
- `go test ./... -race` green.

## Out of scope (follow-on plans)

- **P2 — propagation + agent-id plumbing:** driver current-session-id wiring, `<loom_origin>` marker in `DelegateTaskRequest.SystemContext`, slave parse → `Task`, reverse marker → `driver-tasks.jsonl`, `ShortID` in `register` → `DaemonInfo` → `SessionRow`, deploy configs setting per-agent `CODEX_HOME`.
- **P3 — Commander nesting:** observer global `(parent_agent_id, session_id)` index, frontend cross-daemon `buildSessionNodes`, `remote`/`parent offline` badges with `display_name`.
