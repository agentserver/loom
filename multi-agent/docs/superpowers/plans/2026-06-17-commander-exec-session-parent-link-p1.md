# Commander Exec Session Parent Link — P1 (Backend record + scanner + CODEX_HOME isolation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist a parent link for `codex_exec` agent-task sessions in a per-agent-`CODEX_HOME` sidecar, and have the Codex scanner merge it into `agentbackend.Session` so downstream (P2 propagation, P3 Commander nesting) can render remote tasks under their originating session.

**Architecture:** Each agent runs its own `CODEX_HOME`. The codex executor writes a `$CODEX_HOME/loom-meta/<thread_id>.json` sidecar **only on the `Run` (new-session) path, never `RunResume`**, right after it captures the new codex thread id. The codex scanner derives its root from backend-instance state (`effectiveCodexHome`, not process env) and merges any matching sidecar into the scanned `Session`, setting `ParentID` + `ParentAgentID` + `ParentDisplayName` (only for `agent_task` sessions). `CODEX_HOME` is resolved once in `New` (resolve-then-strip + full dedup of env slice AND `os.Environ`, case-insensitive) and shared by exec/llm/app-server. No driver/slave wiring or UI here — propagation (P2) and nesting (P3) are separate follow-on plans. P1 is fully testable with hand-seeded fixtures.

**Tech Stack:** Go (stdlib only — `encoding/json`, `os`, `path/filepath`, `strings`, `time`), table-driven tests, `go test -race`.

**Spec:** `multi-agent/docs/superpowers/specs/2026-06-17-commander-exec-session-parent-link-design.md` (revision `5926e9c`)

---

## File Structure

- `pkg/agentbackend/backend.go` — add `ParentAgentID` + `ParentDisplayName` to `Session`.
- `pkg/agentbackend/config.go` — add `CodexHome` to `Config` (NOT `LoomHome`/`ShortID`).
- `internal/executor/executor.go` — add `ParentSessionID`/`ParentAgentID`/`ParentDisplayName` to `Task`.
- `pkg/agentbackend/codex/loommeta.go` — **NEW**: `loomMeta` type, `loomMetaDir(base)`/`loomMetaPath(base,id)`, `writeLoomMeta`/`readLoomMeta`/`reaper` (all take resolved base; no env reads).
- `pkg/agentbackend/codex/loommeta_test.go` — **NEW**: sidecar helper tests.
- `pkg/agentbackend/codex/codexenv.go` — **NEW**: `resolveCodexHome`/`effectiveCodexHome`/`mergeEnv`/`envValue`.
- `pkg/agentbackend/codex/codexenv_test.go` — **NEW**: resolution + dedup tests.
- `pkg/agentbackend/codex/backend.go` — `New` resolves env once into `b.env`; `b.effectiveCodexHome()`; `workerBackend` uses `b.env`.
- `pkg/agentbackend/codex/executor.go` — `runWithArgv` gains `newSession bool`; writes sidecar only when `newSession`; uses `mergeEnv(os.Environ(), e.env)`.
- `pkg/agentbackend/codex/llm.go` — switch to `mergeEnv(os.Environ(), r.env)`.
- `pkg/agentbackend/codex/sessions.go` — `sessionsRoot` → `*Backend` method using `b.effectiveCodexHome()`; merge sidecar (validated) in List/Get; cache key = `Get`/`seen` consistent composite; reaper (orphan + 30d mtime).
- `pkg/agentbackend/codex/sessions_test.go` — sidecar-merge + instance-state + cache-consistency tests.
- `internal/config/config.go` + `internal/driver/config.go` — `AgentConfig` adds `codex_home`/`loom_home` **struct fields only** (no wiring; wiring is P2).

---

## Task 1: Add `ParentAgentID` + `ParentDisplayName` to `Session`

**Files:**
- Modify: `pkg/agentbackend/backend.go`

- [ ] **Step 1: Add the fields**

In `pkg/agentbackend/backend.go`, inside `type Session struct`, directly after the existing `ParentID` field (around line 117):

```go
	// ParentAgentID is the ShortID of the agent instance (daemon) that owns
	// ParentID. Empty when ParentID is empty. Lets the Commander observer
	// resolve a parent across reconnects: daemon_id is ephemeral, agent_id
	// (ShortID) is stable. See loom #24 spec.
	ParentAgentID string

	// ParentDisplayName is the display name of the parent agent, denormalized
	// from the sidecar so the UI can label a parent even when the parent
	// daemon is offline (observer cannot live-resolve its display name then).
	ParentDisplayName string
```

- [ ] **Step 2: Build & vet**

Run: `go build ./pkg/agentbackend/...`
Expected: succeeds (additive fields; zero values are empty).

- [ ] **Step 3: Commit**

```bash
git add pkg/agentbackend/backend.go
git commit -m "feat(agentbackend): add Session.ParentAgentID/ParentDisplayName (#24)"
```

---

## Task 2: Add `CodexHome` to `Config`; parent fields to `Task`

**Files:**
- Modify: `pkg/agentbackend/config.go`
- Modify: `internal/executor/executor.go`
- Modify: `internal/config/config.go`, `internal/driver/config.go` (struct fields only)

- [ ] **Step 1: `Config.CodexHome`**

In `pkg/agentbackend/config.go`, inside `type Config struct`, after `WorkerMode`:

```go
	// CodexHome overrides the codex data directory passed as CODEX_HOME to
	// the codex subprocess and read by the session scanner. Per-agent
	// isolation (loom #24). Empty = resolve CODEX_HOME from env / fall back
	// to $HOME/.codex. The short_id-based default is resolved by the
	// LAUNCHER (which has ShortID), not the backend.
	CodexHome string `yaml:"codex_home"`
```

- [ ] **Step 2: `Task` parent fields**

In `internal/executor/executor.go`, inside `type Task struct`, after `TimeoutSec`:

```go
	// ParentSessionID / ParentAgentID / ParentDisplayName carry the origin
	// link for a codex exec session spawned by a driver/slave. Empty for
	// sessions with no parent (direct interactive). Read by the codex
	// backend executor to write the loom-meta sidecar. See loom #24.
	ParentSessionID   string
	ParentAgentID     string
	ParentDisplayName string
```

- [ ] **Step 3: deploy-level struct fields (definition only, no wiring)**

In `internal/config/config.go` `AgentConfig` (around line 39) and `internal/driver/config.go` `AgentConfig` (around line 36), after `ExtraArgs`, add:

```go
	CodexHome string `yaml:"codex_home,omitempty"`
	LoomHome  string `yaml:"loom_home,omitempty"`
```

(No code reads these yet — P2 wires them. This task only defines the fields so YAML parses.)

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/config.go internal/executor/executor.go internal/config/config.go internal/driver/config.go
git commit -m "feat(config): add CodexHome/loom_home fields + Task parent fields (#24)"
```

---

## Task 3: Env resolution + dedup helpers (`codexenv.go`)

**Files:**
- Create: `pkg/agentbackend/codex/codexenv.go`
- Create: `pkg/agentbackend/codex/codexenv_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/agentbackend/codex/codexenv_test.go`:

```go
package codex

import (
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestResolveCodexHome_CfgWins(t *testing.T) {
	if got := resolveCodexHome(agentbackend.Config{CodexHome: "/cfg"}, []string{"CODEX_HOME=/env"}); got != "/cfg" {
		t.Fatalf("got %q, want /cfg", got)
	}
}

func TestResolveCodexHome_EnvSliceBeforeProcessEnv(t *testing.T) {
	t.Setenv("CODEX_HOME", "/proc")
	if got := resolveCodexHome(agentbackend.Config{}, []string{"CODEX_HOME=/env"}); got != "/env" {
		t.Fatalf("got %q, want /env (env slice beats process env)", got)
	}
}

func TestResolveCodexHome_ProcessEnvFallback(t *testing.T) {
	t.Setenv("CODEX_HOME", "/proc")
	if got := resolveCodexHome(agentbackend.Config{}, nil); got != "/proc" {
		t.Fatalf("got %q, want /proc", got)
	}
}

func TestResolveCodexHome_Empty(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	if got := resolveCodexHome(agentbackend.Config{}, nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestEffectiveCodexHome_FallbackHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := effectiveCodexHome(agentbackend.Config{}, nil); got != home+"/.codex" {
		t.Fatalf("got %q, want %s/.codex", got, home)
	}
}

func TestMergeEnv_OverridesCaseInsensitive(t *testing.T) {
	// Process env has CODEX_HOME=/old (and a case variant); override slice has CODEX_HOME=/new.
	merged := mergeEnv([]string{"CODEX_HOME=/old", "Codex_Home=/old2", "PATH=/bin"}, []string{"CODEX_HOME=/new", "FOO=bar"})
	got := map[string]string{}
	for _, kv := range merged {
		k, v, ok := splitEnv(kv)
		if !ok {
			continue
		}
		got[strings.ToLower(k)] = v
	}
	if got["codex_home"] != "/new" {
		t.Fatalf("codex_home = %q, want /new (single, overridden)", got["codex_home"])
	}
	if got["path"] != "/bin" {
		t.Fatalf("path = %q, want /bin (preserved)", got["path"])
	}
	if got["foo"] != "bar" {
		t.Fatalf("foo = %q, want bar", got["foo"])
	}
	// Exactly one CODEX_HOME entry (case-insensitive dedup).
	count := 0
	for _, kv := range merged {
		if k, _, ok := splitEnv(kv); ok && strings.EqualFold(k, "CODEX_HOME") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("CODEX_HOME appears %d times, want 1", count)
	}
}
```

Add `"strings"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'ResolveCodexHome|EffectiveCodexHome|MergeEnv' -v`
Expected: FAIL / build error — undefined symbols.

- [ ] **Step 3: Implement `codexenv.go`**

Create `pkg/agentbackend/codex/codexenv.go`:

```go
package codex

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// envValue returns the value of key in the env slice (e.g.
// []string{"CODEX_HOME=/x"}), case-insensitive, or "" if absent.
func envValue(env []string, key string) string {
	prefix := strings.ToUpper(key) + "="
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// resolveCodexHome resolves the effective codex data dir from cfg then env
// slice then process env. Returns "" when none is set (caller falls back to
// $HOME/.codex via effectiveCodexHome). Does NOT read ShortID — the
// short_id-based default is the launcher's job.
func resolveCodexHome(cfg agentbackend.Config, env []string) string {
	if cfg.CodexHome != "" {
		return cfg.CodexHome
	}
	if v := envValue(env, "CODEX_HOME"); v != "" {
		return v
	}
	return os.Getenv("CODEX_HOME")
}

// effectiveCodexHome returns resolveCodexHome, or $HOME/.codex when unresolved.
// Returns "" only when both resolve and os.UserHomeDir fail (extremely rare):
// callers treat "" as no-op (scanner returns empty; sidecar writer skips).
func effectiveCodexHome(cfg agentbackend.Config, env []string) string {
	if r := resolveCodexHome(cfg, env); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// splitEnv splits "KEY=VALUE" into (key, value, true). Returns ok=false for
// malformed entries (no '=').
func splitEnv(kv string) (string, string, bool) {
	i := strings.IndexByte(kv, '=')
	if i < 0 {
		return "", "", false
	}
	return kv[:i], kv[i+1:], true
}

// mergeEnv combines a base env (typically os.Environ()) with an override env,
// case-insensitive on keys: an override key replaces any base key with the
// same name (case-insensitive), and override entries not in base are appended.
// Each key appears exactly once. Used to build subprocess env so CODEX_HOME
// is never duplicated across cmd.Environ() and the resolved env slice.
func mergeEnv(base, override []string) []string {
	seen := make(map[string]int) // lowercased key -> index in out
	out := make([]string, 0, len(base)+len(override))
	for _, kv := range base {
		k, v, ok := splitEnv(kv)
		if !ok {
			out = append(out, kv)
			continue
		}
		lk := strings.ToLower(k)
		if idx, exists := seen[lk]; exists {
			out[idx] = k + "=" + v
			continue
		}
		seen[lk] = len(out)
		out = append(out, kv)
	}
	for _, kv := range override {
		k, v, ok := splitEnv(kv)
		if !ok {
			out = append(out, kv)
			continue
		}
		lk := strings.ToLower(k)
		if idx, exists := seen[lk]; exists {
			out[idx] = k + "=" + v
			continue
		}
		seen[lk] = len(out)
		out = append(out, kv)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'ResolveCodexHome|EffectiveCodexHome|MergeEnv' -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/codex/codexenv.go pkg/agentbackend/codex/codexenv_test.go
git commit -m "feat(codex): add codex-home resolution + case-insensitive env merge (#24)"
```

---

## Task 4: Sidecar helper (`loommeta.go`)

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

func TestLoomMetaPath(t *testing.T) {
	if got, want := loomMetaPath("/tmp/codex-home", "thread-1"), filepath.Join("/tmp/codex-home", "loom-meta", "thread-1.json"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteLoomMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := loomMeta{
		Schema:            loomMetaSchema,
		SessionID:         "thread-1",
		ParentSessionID:   "parent-thread",
		ParentAgentID:     "drv-abc",
		ParentDisplayName: "prod-driver",
		Origin:            "agent_task",
		Kind:              "codex",
		CreatedAt:         "2026-06-17T00:00:00Z",
	}
	if err := writeLoomMeta(dir, in); err != nil {
		t.Fatalf("writeLoomMeta: %v", err)
	}
	out, ok := readLoomMeta(dir, "thread-1")
	if !ok {
		t.Fatal("readLoomMeta: not found")
	}
	if out.ParentAgentID != "drv-abc" || out.ParentDisplayName != "prod-driver" || out.Origin != "agent_task" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestReadLoomMeta_Missing(t *testing.T) {
	if _, ok := readLoomMeta(t.TempDir(), "nope"); ok {
		t.Fatal("expected not found")
	}
}

func TestReadLoomMeta_CorruptSkipped(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(loomMetaDir(dir), 0o755)
	_ = os.WriteFile(loomMetaPath(dir, "bad"), []byte("{not json"), 0o600)
	if _, ok := readLoomMeta(dir, "bad"); ok {
		t.Fatal("corrupt sidecar should be skipped")
	}
}

func TestReaper_RemovesOrphansAndAged(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(loomMetaDir(dir), 0o755)
	live := loomMeta{Schema: loomMetaSchema, SessionID: "live", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T00:00:00Z"}
	dead := loomMeta{Schema: loomMetaSchema, SessionID: "dead", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T00:00:00Z"}
	_ = writeLoomMeta(dir, live)
	_ = writeLoomMeta(dir, dead)
	// Backdate the "dead" file's mtime beyond loomMetaMaxAge.
	old := loomMetaPath(dir, "dead")
	past := timeNow().Add(-(loomMetaMaxAge + time.Hour))
	_ = os.Chtimes(old, past, past)

	reaper(dir, []string{"live"}) // only "live" still has a rollout
	if _, ok := readLoomMeta(dir, "live"); !ok {
		t.Fatal("live sidecar must survive")
	}
	if _, ok := readLoomMeta(dir, "dead"); ok {
		t.Fatal("orphaned+aged sidecar must be removed")
	}
}
```

Add `"time"` to the test imports (`timeNow` is a thin wrapper added in Step 3 to keep tests deterministic; see below).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'LoomMeta|Reaper' -v`
Expected: FAIL / build error — undefined symbols.

- [ ] **Step 3: Implement `loommeta.go`**

Create `pkg/agentbackend/codex/loommeta.go`:

```go
package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const loomMetaSchema = 1 // JSON number, per spec — NOT a string

var loomMetaMaxAge = 30 * 24 * time.Hour

// timeNow is a package indirection so tests can backdate via os.Chtimes
// without freezing the clock; production uses time.Now.
func timeNow() time.Time { return time.Now() }

// loomMeta is the on-disk sidecar written next to a codex exec session. It
// records the parent link that codex itself does not know (the originating
// driver/slave agent + session). Read back by the session scanner so the
// Session descriptor carries ParentID / ParentAgentID / ParentDisplayName.
//
// Path: <effectiveCodexHome>/loom-meta/<thread_id>.json  (loom #24 spec §5).
// Schema is a JSON number (schema: 1), matching the spec's on-disk contract.
type loomMeta struct {
	Schema            int    `json:"schema"`
	SessionID         string `json:"session_id"`
	ParentSessionID   string `json:"parent_session_id,omitempty"`
	ParentAgentID     string `json:"parent_agent_id,omitempty"`
	ParentDisplayName string `json:"parent_display_name,omitempty"`
	Origin            string `json:"origin"`
	Kind              string `json:"kind"`
	CreatedAt         string `json:"created_at"`
}

func loomMetaDir(base string) string  { return filepath.Join(base, "loom-meta") }
func loomMetaPath(base, threadID string) string {
	return filepath.Join(loomMetaDir(base), threadID+".json")
}

// writeLoomMeta writes one sidecar under base, best-effort. Validates the
// record before writing (schema/kind/origin/session_id). Returns an error on
// filesystem failure; callers MUST treat failure as non-fatal.
func writeLoomMeta(base string, m loomMeta) error {
	if base == "" {
		return nil // no effective home resolved; skip
	}
	if m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID == "" {
		return nil // defensive: refuse to write an invalid sidecar
	}
	if err := os.MkdirAll(loomMetaDir(base), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(loomMetaPath(base, m.SessionID), b, 0o600)
}

// readLoomMeta returns the sidecar for threadID. ok is false when absent or
// unparseable (corrupt entries skipped silently, mirroring scanner policy).
func readLoomMeta(base, threadID string) (loomMeta, bool) {
	var m loomMeta
	b, err := os.ReadFile(loomMetaPath(base, threadID))
	if err != nil {
		return m, false
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, false
	}
	return m, true
}

// reaper removes sidecars whose thread id is not in liveThreadIDs (orphans),
// and sidecars whose file mtime is older than loomMetaMaxAge.
func reaper(base string, liveThreadIDs []string) {
	dir := loomMetaDir(base)
	if dir == "" || base == "" {
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
	cutoff := timeNow().Add(-loomMetaMaxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		id, ok := threadIDFromLoomMetaName(name)
		if !ok {
			continue
		}
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		if _, ok := live[id]; !ok {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

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
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/codex/loommeta.go pkg/agentbackend/codex/loommeta_test.go
git commit -m "feat(codex): add loom-meta sidecar helper + reaper (#24)"
```

---

## Task 5: `New` resolves env once into `b.env`; `b.effectiveCodexHome()`

**Files:**
- Modify: `pkg/agentbackend/codex/backend.go`
- Test: `pkg/agentbackend/codex/backend_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create/append `pkg/agentbackend/codex/backend_test.go`:

```go
package codex

import (
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestBackend_EffectiveCodexHomeFromConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	b := New(agentbackend.Config{CodexHome: "/cfg-home"}, nil)
	if got := b.effectiveCodexHome(); got != "/cfg-home" {
		t.Fatalf("effectiveCodexHome = %q, want /cfg-home", got)
	}
}

func TestBackend_EffectiveCodexHomeFromEnvSlice(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	b := New(agentbackend.Config{}, []string{"CODEX_HOME=/env-home"})
	if got := b.effectiveCodexHome(); got != "/env-home" {
		t.Fatalf("effectiveCodexHome = %q, want /env-home (env slice)", got)
	}
}

func TestNew_ResolvedEnvHasSingleCodexHome(t *testing.T) {
	// Process env already has CODEX_HOME=/old; cfg wants /new. b.env must
	// contain exactly one CODEX_HOME=/new.
	t.Setenv("CODEX_HOME", "/old")
	b := New(agentbackend.Config{CodexHome: "/new"}, nil)
	count := 0
	for _, kv := range b.env {
		if k, v, ok := splitEnv(kv); ok && k == "CODEX_HOME" {
			count++
			if v != "/new" {
				t.Errorf("CODEX_HOME value = %q, want /new", v)
			}
		}
	}
	// b.env is the override slice passed to mergeEnv; it should contain
	// CODEX_HOME=/new exactly once (dedup within the slice).
	if count != 1 {
		t.Fatalf("b.env has %d CODEX_HOME entries, want 1", count)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'Backend_EffectiveCodexHome|New_ResolvedEnv' -v`
Expected: FAIL — `b.env`/`b.effectiveCodexHome` undefined.

- [ ] **Step 3: Implement in `backend.go`**

In `pkg/agentbackend/codex/backend.go`, add an `env` field to `Backend` and resolve in `New`:

```go
type Backend struct {
	cfg  agentbackend.Config
	env  []string // resolved env (CODEX_HOME injected/deduped) shared by exec/llm/app-server
	exec *executor
	perm *Store
	llm  *llmRunner
	list *sessioncache.FileCache
}

func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "codex"
	}
	env = withCodexHome(cfg, env)
	return &Backend{
		cfg:  cfg,
		env:  env,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
		list: sessioncache.NewFileCache(),
	}
}

// withCodexHome resolves the final CODEX_HOME from the ORIGINAL cfg/env (do
// NOT strip the env slice before resolving), then returns an env slice with
// any existing CODEX_HOME removed (case-insensitive) and the final value
// ALWAYS inserted when non-empty — including when it equals the default
// $HOME/.codex. Injecting even the default is required so that mergeEnv at
// subprocess spawn overrides a stale CODEX_HOME in os.Environ(); otherwise
// the codex subprocess would write to the stale dir while the scanner reads
// the default dir. The caller merges this with os.Environ() via mergeEnv.
func withCodexHome(cfg agentbackend.Config, env []string) []string {
	final := resolveCodexHome(cfg, env)
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, ok := splitEnv(kv)
		if ok && strings.EqualFold(k, "CODEX_HOME") {
			continue // drop existing (case-insensitive)
		}
		out = append(out, kv)
	}
	if final == "" {
		return out // nothing resolved; scanner/subprocess fall back to $HOME/.codex
	}
	out = append(out, "CODEX_HOME="+final)
	return out
}

// effectiveCodexHome returns the resolved codex home for this backend instance
// (never reads os.Getenv directly; uses cfg + the resolved env).
func (b *Backend) effectiveCodexHome() string {
	return effectiveCodexHome(b.cfg, b.env)
}
```

Add `"os"`, `"path/filepath"`, `"strings"` to imports if missing.

Update the builder so `workerBackend` uses `b.env` (not raw `env`):

```go
func init() {
	agentbackend.RegisterBuilder(agentbackend.KindCodex, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		b := New(cfg, env)
		if cfg.WorkerMode == "app_server" && os.Getenv(appServerUnsafeHumanloopRoutingEnv) == "1" {
			return &workerBackend{
				Backend: b,
				manager: newAppServerManager(b.cfg, b.env), // resolved env, not raw
			}, nil
		}
		return b, nil
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'Backend_EffectiveCodexHome|New_ResolvedEnv' -v -race`
Expected: PASS.

- [ ] **Step 5: Full package + vet**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1 && go vet ./pkg/agentbackend/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/backend.go pkg/agentbackend/codex/backend_test.go
git commit -m "feat(codex): resolve CODEX_HOME once into b.env; share with app-server (#24)"
```

---

## Task 6: Scanner root from `b.effectiveCodexHome()` (instance state, no env)

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go`
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/agentbackend/codex/sessions_test.go`:

```go
func TestSessionsRoot_InstanceScopedNotEnv(t *testing.T) {
	// Two backends in one process with different CodexHome must see different
	// roots (proves the scanner does NOT read os.Getenv).
	t.Setenv("CODEX_HOME", "")
	b1 := New(agentbackend.Config{CodexHome: "/h1"}, nil)
	b2 := New(agentbackend.Config{CodexHome: "/h2"}, nil)
	if b1.sessionsRoot() == b2.sessionsRoot() {
		t.Fatalf("two backends share root %q — scanner is reading process env", b1.sessionsRoot())
	}
	if want := "/h1/sessions"; b1.sessionsRoot() != want {
		t.Fatalf("b1 root = %q, want %q", b1.sessionsRoot(), want)
	}
}

func TestSessionsRoot_FallbackHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	b := New(agentbackend.Config{}, nil)
	if want := filepath.Join(home, ".codex", "sessions"); b.sessionsRoot() != want {
		t.Fatalf("root = %q, want %q", b.sessionsRoot(), want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'SessionsRoot' -v`
Expected: FAIL — `sessionsRoot` is a package func reading `os.Getenv`, not a `*Backend` method.

- [ ] **Step 3: Convert `sessionsRoot` to a `*Backend` method**

In `pkg/agentbackend/codex/sessions.go`, replace the package-level `sessionsRoot()` (lines 34-40) with:

```go
func (b *Backend) sessionsRoot() string {
	base := b.effectiveCodexHome()
	if base == "" {
		return "" // no home resolvable; ListSessions no-ops
	}
	return filepath.Join(base, "sessions")
}
```

Update all callers within the file (`ListSessions`, `GetSession`, `sessionWorkingDir`) from `sessionsRoot()` to `b.sessionsRoot()`. (These are methods on `*Backend`, so `b` is in scope.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'SessionsRoot' -v -race`
Expected: PASS.

- [ ] **Step 5: Full codex package (existing tests redirect HOME, so fallback path still works)**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "fix(codex): scanner root is backend-instance state, not process env (#24)"
```

---

## Task 7: Scanner merges sidecar (validated) into `agent_task` sessions

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go`
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestListSessions_MergesLoomMetaSidecar(t *testing.T) {
	home := t.TempDir()
	b := New(agentbackend.Config{CodexHome: home}, nil)

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

	if err := writeLoomMeta(home, loomMeta{
		Schema: loomMetaSchema, SessionID: threadID,
		ParentSessionID: "parent-aaa", ParentAgentID: "drv-abc", ParentDisplayName: "prod-driver",
		Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	s := got[0]
	if s.Origin != agentbackend.SessionOriginAgentTask {
		t.Errorf("Origin = %q, want agent_task", s.Origin)
	}
	if s.ParentID != "parent-aaa" || s.ParentAgentID != "drv-abc" || s.ParentDisplayName != "prod-driver" {
		t.Errorf("parent fields mismatch: %+v", s)
	}
}

func TestListSessions_SidecarDoesNotRelabelUserSession(t *testing.T) {
	home := t.TempDir()
	b := New(agentbackend.Config{CodexHome: home}, nil)
	dayDir := filepath.Join(home, "sessions", "2026", "06", "17")
	_ = os.MkdirAll(dayDir, 0o755)
	threadID := "deadbeef-0000-0000-0000-000000000002"
	// rollout WITHOUT originator=codex_exec → classified user (not agent_task).
	rollout := filepath.Join(dayDir, "rollout-2026-06-17T11-00-00Z-"+threadID+".jsonl")
	_ = os.WriteFile(rollout, []byte(`{"timestamp":"2026-06-17T11:00:00Z","type":"session_meta","payload":{"id":"`+threadID+`","cwd":"/proj"}}`+"\n"), 0o600)
	_ = writeLoomMeta(home, loomMeta{Schema: loomMetaSchema, SessionID: threadID, ParentAgentID: "drv", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T00:00:00Z"})

	got, _ := b.ListSessions(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].Origin == agentbackend.SessionOriginAgentTask {
		t.Errorf("sidecar must not relabel a non-codex_exec session as agent_task")
	}
	if got[0].ParentID != "" || got[0].ParentAgentID != "" {
		t.Errorf("parent fields must stay empty for non-agent_task session: %+v", got[0])
	}
}

func TestListSessions_CorruptSidecarSkipped(t *testing.T) {
	home := t.TempDir()
	b := New(agentbackend.Config{CodexHome: home}, nil)
	dayDir := filepath.Join(home, "sessions", "2026", "06", "17")
	_ = os.MkdirAll(dayDir, 0o755)
	threadID := "deadbeef-0000-0000-0000-000000000003"
	rollout := filepath.Join(dayDir, "rollout-2026-06-17T12-00-00Z-"+threadID+".jsonl")
	_ = os.WriteFile(rollout, []byte(`{"timestamp":"2026-06-17T12:00:00Z","type":"session_meta","payload":{"id":"`+threadID+`","cwd":"/p","originator":"codex_exec"}}`+"\n"), 0o600)
	_ = os.MkdirAll(loomMetaDir(home), 0o755)
	_ = os.WriteFile(loomMetaPath(home, threadID), []byte("{not json"), 0o600)

	got, _ := b.ListSessions(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].ParentAgentID != "" {
		t.Errorf("corrupt sidecar must be skipped, got ParentAgentID=%q", got[0].ParentAgentID)
	}
}
```

Ensure the test file imports `"context"` and `"github.com/yourorg/multi-agent/pkg/agentbackend"`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListSessions_MergesLoomMetaSidecar|ListSessions_SidecarDoesNotRelabel|ListSessions_CorruptSidecar' -v`
Expected: FAIL — no merge yet (parent fields empty).

- [ ] **Step 3: Add `applyLoomMeta` and call it from `scanCodexSession`**

In `pkg/agentbackend/codex/sessions.go`, add near `applyCodexSessionMeta`:

```go
// applyLoomMeta overlays a validated sidecar's parent fields onto a session.
// A missing/corrupt sidecar is a no-op. The sidecar only enriches; it never
// sets Origin (Origin is codex-native) and only applies when the session is
// already classified agent_task — so a stale sidecar cannot relabel a user
// session. codex-native subagent ParentID is never overwritten.
func applyLoomMeta(base string, sess *agentbackend.Session) {
	if base == "" {
		return
	}
	m, ok := readLoomMeta(base, sess.ID)
	if !ok {
		return
	}
	if m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID != sess.ID {
		return
	}
	if sess.Origin != agentbackend.SessionOriginAgentTask {
		return // only enrich genuine agent_task sessions
	}
	// Parent link is a tuple: only apply when the sidecar has a parent
	// session id. A sidecar with ParentAgentID but no ParentSessionID is
	// malformed — set nothing (matches spec: ParentAgentID is empty when
	// ParentID is empty). Never overwrite codex-native subagent ParentID.
	if m.ParentSessionID == "" {
		return
	}
	if sess.ParentID == "" {
		sess.ParentID = m.ParentSessionID
		sess.ParentAgentID = m.ParentAgentID
		sess.ParentDisplayName = m.ParentDisplayName
	}
}
```

`scanCodexSession` is a package func (no `b` receiver). It needs the base. Change its signature to accept the base, OR have callers pass it. Minimal: add a `base string` parameter to `scanCodexSession` and call `applyLoomMeta(base, &res.session)` before `return res`. Update the two callers (`ListSessions` and `GetSession` in the walk) to pass `b.effectiveCodexHome()`.

In `scanCodexSession`, change the signature from `func scanCodexSession(path, fallbackID string, withMessages bool) codexScanResult` to `func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult`, and before the final `return res`:

```go
	applyLoomMeta(base, &res.session)
	return res
```

Update callers in `ListSessions` and `GetSession`:

```go
	return scanCodexSession(path, id, false, b.effectiveCodexHome()).session
	// and in GetSession:
	res := scanCodexSession(found, id, true, b.effectiveCodexHome())
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListSessions_MergesLoomMetaSidecar|ListSessions_SidecarDoesNotRelabel|ListSessions_CorruptSidecar' -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "feat(codex): merge validated loom-meta sidecar into agent_task sessions (#24)"
```

---

## Task 8: Cache key = `Get`/`seen` consistency (sidecar mtime invalidation)

**Files:**
- Modify: `pkg/agentbackend/codex/sessions.go` (`ListSessions`)
- Test: `pkg/agentbackend/codex/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestListCache_InvalidatedBySidecarRewrite(t *testing.T) {
	home := t.TempDir()
	b := New(agentbackend.Config{CodexHome: home}, nil)
	dayDir := filepath.Join(home, "sessions", "2026", "06", "17")
	_ = os.MkdirAll(dayDir, 0o755)
	threadID := "deadbeef-0000-0000-0000-000000000004"
	rollout := filepath.Join(dayDir, "rollout-2026-06-17T13-00-00Z-"+threadID+".jsonl")
	_ = os.WriteFile(rollout, []byte(`{"timestamp":"2026-06-17T13:00:00Z","type":"session_meta","payload":{"id":"`+threadID+`","cwd":"/p","originator":"codex_exec"}}`+"\n"), 0o600)

	first, _ := b.ListSessions(context.Background())
	if first[0].ParentID != "" || first[0].ParentAgentID != "" {
		t.Fatalf("precondition: no sidecar yet, got ParentID=%q ParentAgentID=%q", first[0].ParentID, first[0].ParentAgentID)
	}
	// Sidecar carries the full parent tuple (ParentSessionID + agent + name).
	_ = writeLoomMeta(home, loomMeta{Schema: loomMetaSchema, SessionID: threadID, ParentSessionID: "parent-xyz", ParentAgentID: "drv-xyz", ParentDisplayName: "prod-driver", Origin: "agent_task", Kind: "codex", CreatedAt: "2026-06-17T13:00:00Z"})
	second, _ := b.ListSessions(context.Background())
	if second[0].ParentID != "parent-xyz" || second[0].ParentAgentID != "drv-xyz" {
		t.Fatalf("cache not invalidated by sidecar write: ParentID=%q ParentAgentID=%q", second[0].ParentID, second[0].ParentAgentID)
	}
	// Third call must hit cache (not re-scan) and still see the parent tuple.
	third, _ := b.ListSessions(context.Background())
	if third[0].ParentID != "parent-xyz" || third[0].ParentAgentID != "drv-xyz" {
		t.Fatalf("cache lost the row on re-scan (Prune key mismatch?): ParentID=%q", third[0].ParentID)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListCache_InvalidatedBySidecarRewrite' -v`
Expected: FAIL — either second read returns stale (no invalidation) or third read loses the row (Prune evicts because `seen` key ≠ `Get` key).

- [ ] **Step 3: Make `Get` and `seen` use the same composite key**

In `ListSessions`, build a composite key from rollout path + sidecar mtime, and use it for BOTH `b.list.Get` and the `seen` map. In the `WalkDir` body:

```go
		id := sessionIDFromFilename(entry.Name())
		if id == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		sidecarMtime := ""
		if si, err := os.Stat(loomMetaPath(b.effectiveCodexHome(), id)); err == nil {
			sidecarMtime = si.ModTime().Format(time.RFC3339Nano)
		}
		cacheKey := path + "|" + sidecarMtime
		seen[cacheKey] = struct{}{}
		liveThreadIDs = append(liveThreadIDs, id)
		session := b.list.Get(cacheKey, info, func() agentbackend.Session {
			return scanCodexSession(path, id, false, b.effectiveCodexHome()).session
		})
		out = append(out, session)
```

Declare `liveThreadIDs := make([]string, 0)` alongside `seen` at the top of `ListSessions`, and call `reaper(b.effectiveCodexHome(), liveThreadIDs)` before `b.list.Prune(seen)`. Add `"time"` to imports if absent; run `gofmt -w`.

> The `seen` map now keys on `cacheKey` (path + sidecar mtime), matching `Get` — so `Prune(seen)` keeps exactly the entries `Get` populated. A sidecar rewrite changes `sidecarMtime` → changes `cacheKey` → `Get` misses (re-scans) and the old key is pruned.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/agentbackend/codex/ -run 'ListCache_InvalidatedBySidecarRewrite' -v -race`
Expected: PASS.

- [ ] **Step 5: Full package + race**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go
git commit -m "fix(codex): align FileCache Get/seen keys; invalidate on sidecar rewrite (#24)"
```

---

## Task 9: Executor writes sidecar **only on `Run`**, never `RunResume`

**Files:**
- Modify: `pkg/agentbackend/codex/executor.go`
- Test: `pkg/agentbackend/codex/executor_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/agentbackend/codex/executor_test.go` (reuses `writeFakeCodex`, `captureSink`, `buildFakeCodex`):

```go
func TestCodexExecutorRunWritesLoomMetaSidecar(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-new","timestamp":"2026-06-17T10:00:00Z"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, nil)
	res, err := ex.Run(context.Background(), agentbackend.Task{
		Prompt: "hi", ParentSessionID: "p", ParentAgentID: "drv-1", ParentDisplayName: "prod-driver",
	}, &captureSink{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionID != "thr-new" {
		t.Fatalf("SessionID = %q", res.SessionID)
	}
	m, ok := readLoomMeta(home, "thr-new")
	if !ok {
		t.Fatal("sidecar not written on Run")
	}
	if m.ParentAgentID != "drv-1" || m.ParentDisplayName != "prod-driver" || m.Origin != "agent_task" {
		t.Fatalf("sidecar mismatch: %+v", m)
	}
}

func TestCodexExecutorRunResumeDoesNotWriteSidecar(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-resume","timestamp":"2026-06-17T10:00:00Z"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, nil)
	if _, err := ex.RunResume(context.Background(), "thr-resume", "continue", &captureSink{}); err != nil {
		t.Fatalf("RunResume: %v", err)
	}
	if _, ok := readLoomMeta(home, "thr-resume"); ok {
		t.Fatal("RunResume must NOT write a sidecar (would mislabel interactive resume as agent_task)")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'RunWritesLoomMetaSidecar|RunResumeDoesNotWriteSidecar' -v`
Expected: FAIL — no sidecar written on Run yet (first test fails); once you add unconditional writing in `runWithArgv`, the RunResume test will fail (proving the hazard).

- [ ] **Step 3: Add `Timestamp` to `codexEvent`; add `newSession bool` + `parent parentLink` to `runWithArgv`**

First, give `codexEvent` a `Timestamp` field (the loop variable is `ev`, not `ln`; codex stream-json events carry a top-level `timestamp`). In `pkg/agentbackend/codex/executor.go`:

```go
type codexEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	ThreadID  string `json:"thread_id"`
	Item      struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}
```

Add a small bundle type near `executor`:

```go
// parentLink carries the origin fields from a Task into runWithArgv so the
// sidecar can be written on the new-session path. Zero-valued for RunResume
// (which never writes a sidecar).
type parentLink struct {
	sessionID, agentID, displayName string
}
```

Change `runWithArgv`'s signature to accept `newSession bool` and `parent parentLink`:

```go
func (e *executor) runWithArgv(ctx context.Context, argvHead []string, prompt string, sink agentbackend.Sink, newSession bool, parent parentLink) (agentbackend.Result, error) {
```

Update the two callers:
- `Run` (line 81): `return e.runWithArgv(ctx, args, prompt, sink, true, parentLink{t.ParentSessionID, t.ParentAgentID, t.ParentDisplayName})`
- `RunResume` (line 101): `return e.runWithArgv(ctx, args, prompt, sink, false, parentLink{})`

- [ ] **Step 4: Capture the event timestamp and write the sidecar only when `newSession`**

The thread-capture block (loop variable is `ev`) writes the sidecar only when `newSession`. Use the **thread.started event's own** `ev.Timestamp` for `created_at` (not a "last seen event" timestamp — if codex ever emits a timestamped event before thread.started, a last-seen value would misattribute the time); fall back to `time.Now()` only when the thread.started event has no timestamp:

```go
		if ev.Type == "thread.started" && sessionID == "" && ev.ThreadID != "" {
			sessionID = ev.ThreadID
			if newSession {
				// Best-effort parent-link sidecar. Only on the new-session
				// path (Run), NEVER RunResume — a resume emits thread.started
				// too and must not be mislabeled agent_task. Failure is
				// non-fatal: the session still lists, just without a parent.
				created := ev.Timestamp // thread.started's own timestamp
				if created == "" {
					created = timeNow().UTC().Format(time.RFC3339Nano)
				}
				_ = writeLoomMeta(effectiveCodexHome(e.cfg, e.env), loomMeta{
					Schema:            loomMetaSchema,
					SessionID:         sessionID,
					ParentSessionID:   parent.sessionID,
					ParentAgentID:     parent.agentID,
					ParentDisplayName: parent.displayName,
					Origin:            string(agentbackend.SessionOriginAgentTask),
					Kind:              "codex",
					CreatedAt:         created,
				})
			}
			continue
		}
```

Add `"time"` to the executor's imports (for `time.RFC3339Nano`).

- [ ] **Step 5: Add a no-timestamp-fallback test**

Append to `executor_test.go`:

```go
func TestCodexExecutorSidecarCreatedAteFallback(t *testing.T) {
	home := t.TempDir()
	// thread.started event with NO timestamp field → created_at must fall
	// back to time.Now() (non-empty RFC3339Nano), not stay empty.
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-nots"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, nil)
	if _, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi", ParentAgentID: "drv"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m, ok := readLoomMeta(home, "thr-nots")
	if !ok {
		t.Fatal("sidecar not written")
	}
	if m.CreatedAt == "" {
		t.Fatal("CreatedAt empty — expected time.Now() fallback when thread.started has no timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		t.Fatalf("CreatedAt %q is not RFC3339Nano: %v", m.CreatedAt, err)
	}
}
```

Add `"time"` to the test file imports if absent.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'RunWritesLoomMetaSidecar|RunResumeDoesNotWriteSidecar|SidecarCreatedAteFallback' -v -race`
Expected: PASS (Run writes; RunResume does not; no-timestamp event still yields a valid `created_at`).

- [ ] **Step 7: Full package + race**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/agentbackend/codex/executor.go pkg/agentbackend/codex/executor_test.go
git commit -m "feat(codex): write loom-meta sidecar only on Run, never RunResume (#24)"
```

---

## Task 10: Subprocess env via `mergeEnv` (full dedup across `os.Environ`) — exec + llm + app-server

**Files:**
- Modify: `pkg/agentbackend/codex/executor.go` (`runWithArgv`)
- Modify: `pkg/agentbackend/codex/llm.go`
- Modify: `pkg/agentbackend/codex/appserver_manager.go` (`startAppServerProcess`, line ~776)
- Test: `pkg/agentbackend/codex/executor_test.go`

> **Why all three:** `withCodexHome` runs in `New` and stores the resolved env in `b.env`; `b.env` is what each subprocess must receive. Today executor (`:128`), llm (`:27`), AND app-server (`:776`) all do `append(cmd.Environ(), env...)`, so a stale `CODEX_HOME` in the process env survives alongside the resolved one. All three must switch to `mergeEnv(os.Environ(), <resolved env>)`.

- [ ] **Step 1: Write the failing test (uses `New`, not `newExecutor(..., nil)`)**

> The test MUST go through `New` so `withCodexHome` actually populates `b.env` with `CODEX_HOME=<home>`. Calling `newExecutor(cfg, nil)` directly leaves `e.env` nil and the subprocess never sees `cfg.CodexHome`.

Append:

```go
func TestCodexExecutorSubprocessEnvHasSingleCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "/stale-from-process") // process env already has a stale value
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	bin := buildFakeCodex(t, fmt.Sprintf(`package main
import ("fmt";"io";"os";"strings")
func main() {
	var lines []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CODEX_HOME=") || strings.HasPrefix(e, "Codex_Home=") {
			lines = append(lines, e)
		}
	}
	_ = os.WriteFile(%q, []byte(strings.Join(lines, "\n")), 0600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(%q)
}
`, envPath, `{"type":"thread.started","thread_id":"thr-env","timestamp":"2026-06-17T10:00:00Z"}`))
	// Go through New so withCodexHome resolves CODEX_HOME=<home> into b.env.
	b := New(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, nil)
	if _, err := b.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(envPath)
	// Must contain exactly one CODEX_HOME line, value == home (not the stale process value).
	lines := strings.Split(string(got), "\n")
	count := 0
	for _, l := range lines {
		if l == "CODEX_HOME="+home {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("subprocess env CODEX_HOME lines = %q, want exactly one CODEX_HOME=%s", string(got), home)
	}
}

// TestCodexExecutorSubprocessEnvDefaultOverridesStale: cfg.CodexHome unset,
// process env has CODEX_HOME=/stale. resolveCodexHome falls back to
// $HOME/.codex; withCodexHome MUST inject CODEX_HOME=<HOME>/.codex so that
// mergeEnv overrides /stale (not leave /stale in place). Otherwise the
// subprocess writes to /stale while the scanner reads <HOME>/.codex.
func TestCodexExecutorSubprocessEnvDefaultOverridesStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)                       // resolveCodexHome falls back to home/.codex
	t.Setenv("CODEX_HOME", "/stale-from-process") // stale value that must be overridden
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	bin := buildFakeCodex(t, fmt.Sprintf(`package main
import ("fmt";"io";"os";"strings")
func main() {
	var lines []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CODEX_HOME=") {
			lines = append(lines, e)
		}
	}
	_ = os.WriteFile(%q, []byte(strings.Join(lines, "\n")), 0600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(%q)
}
`, envPath, `{"type":"thread.started","thread_id":"thr-def","timestamp":"2026-06-17T10:00:00Z"}`))
	// cfg.CodexHome unset → final resolves to home/.codex; withCodexHome injects it.
	b := New(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil)
	if _, err := b.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(envPath)
	want := "CODEX_HOME=" + filepath.Join(home, ".codex")
	count := 0
	for _, l := range strings.Split(string(got), "\n") {
		if l == want {
			count++
		}
		if l == "CODEX_HOME=/stale-from-process" {
			t.Fatalf("stale CODEX_HOME survived in subprocess env: %s", got)
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one %q, got %q", want, got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/codex/ -run 'SubprocessEnvHasSingleCodexHome|SubprocessEnvDefaultOverridesStale' -v`
Expected: FAIL — `runWithArgv` does `append(cmd.Environ(), e.env...)`. For the first test, two CODEX_HOME entries; for the second (before withCodexHome injects the default), the stale `/stale-from-process` survives.

- [ ] **Step 3: Switch executor + llm + app-server to `mergeEnv`**

In `pkg/agentbackend/codex/executor.go` `runWithArgv`, replace:

```go
	cmd.Env = append(cmd.Environ(), e.env...)
```

with:

```go
	cmd.Env = mergeEnv(os.Environ(), e.env)
```

In `pkg/agentbackend/codex/llm.go` (line 27), replace `cmd.Env = append(cmd.Environ(), r.env...)` with `cmd.Env = mergeEnv(os.Environ(), r.env)`.

In `pkg/agentbackend/codex/appserver_manager.go` `startAppServerProcess` (line ~776), replace:

```go
	cmd.Env = append(cmd.Environ(), env...)
```

with:

```go
	cmd.Env = mergeEnv(os.Environ(), env)
```

(The app-server manager receives the **resolved** `b.env` from `workerBackend` per Task 5, so `env` here already carries the single `CODEX_HOME=<resolved>`; `mergeEnv` ensures the process's own stale `CODEX_HOME` (if any) is overridden rather than duplicated.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'SubprocessEnvHasSingleCodexHome|SubprocessEnvDefaultOverridesStale' -v -race`
Expected: PASS — exactly one `CODEX_HOME=<home>`; default case overrides `/stale-from-process`.

- [ ] **Step 5: Full package + race**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codex/executor.go pkg/agentbackend/codex/llm.go pkg/agentbackend/codex/appserver_manager.go pkg/agentbackend/codex/executor_test.go
git commit -m "fix(codex): mergeEnv for subprocess env — single CODEX_HOME, no dup (#24)"
```

---

## Task 11: Whole-repo regression

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 2: Race the full suite**

Run: `go test ./... -race -count=1`
Expected: PASS (additive fields; scanner/exec changes scoped to codex backend).

- [ ] **Step 3: gofmt**

Run: `gofmt -w pkg/agentbackend/codex/ internal/executor/executor.go internal/config/config.go internal/driver/config.go && git diff --exit-code`
If formatting-only changes appear:

```bash
git add -u
git commit -m "chore: gofmt loom-meta additions"
```

---

## Acceptance for P1

- `agentbackend.Session` has `ParentAgentID` + `ParentDisplayName`; `agentbackend.Config` has `CodexHome` (no `LoomHome`/`ShortID`); `internal/{config,driver}.AgentConfig` have `codex_home`/`loom_home` (defined, unwired); `executor.Task` has the three parent fields.
- `New` resolves `CODEX_HOME` once (resolve-then-strip + full dedup of env slice AND `os.Environ`, case-insensitive) into `b.env`, shared by exec/llm/app-server (`workerBackend` uses `b.env`).
- Scanner root is backend-instance state (`b.effectiveCodexHome()`, no `os.Getenv`); merges a validated `$CODEX_HOME/loom-meta/<thread_id>.json` sidecar to set `ParentID`/`ParentAgentID`/`ParentDisplayName` **only for `agent_task` sessions** (never relabels user sessions; never overwrites codex-native subagent `ParentID`).
- Cache `Get`/`seen` use the same composite key (path + sidecar mtime); sidecar rewrite invalidates; orphans + 30d-aged sidecars reaped.
- Codex executor writes the sidecar **only on `Run`** (never `RunResume`); subprocess env has exactly one `CODEX_HOME` (or zero when == default).
- `go test ./... -race` green.

## Out of scope (follow-on plans)

- **P2 — propagation + launcher wiring (driver/slave separate):** driver current-session-id wiring, `<loom_origin>` marker in `DelegateTaskRequest.SystemContext`, slave parse → `Task`, reverse marker → `driver-tasks.jsonl`, `ShortID` in `register` → `DaemonInfo` → `SessionRow` (with `ParentDisplayName`). **slave**: move `agentbackend.New` past `EnsureRegistered`, resolve `codex_home` from short_id. **driver**: read `ShortID` from persisted config at startup (no `EnsureRegistered` to reorder; empty ⇒ "run register first" or fallback). YAML schema + `deploy/` templates + `dev/configs` set `codex_home`/`loom_home`.
- **P3 — Commander nesting:** observer global `(parent_agent_id, session_id)` index, frontend cross-daemon `buildSessionNodes`, `remote`/`parent offline` badges with `display_name`.
