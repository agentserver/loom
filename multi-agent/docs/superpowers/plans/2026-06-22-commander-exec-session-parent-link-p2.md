# Commander Exec Session Parent Link — P2 (Propagation + agent-id plumbing + slave daemon) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make real driver/slave `codex_exec` sessions carry a parent link end-to-end, expose stable agent identity (`ShortID`) through Commander, and make slave sessions visible in Commander (slave becomes a daemon) — so P3 can nest remote tasks under their originating session.

**Architecture:** Two design corrections from the original spec, both confirmed:
1. **Parent-session propagation via a marker file.** The driver's MCP tool handler runs in `driver-agent serve-mcp`, a stdio **child** of codex (`codex-mcp.toml.template`), so it cannot see codex's thread id. The codex executor writes `<CODEX_HOME>/loom-meta/current` on `thread.started` (Run + RunResume); `serve-mcp` reads it as `parent_session_id` and stamps a `<loom_origin agent name session/>` marker into `DelegateTaskRequest.SystemContext`. The slave `poller` parses the marker into `Task.Parent*` and strips it before codex sees the prompt. (P1's executor already writes the loom-meta sidecar from `Task.Parent*`.)
2. **Slave becomes a Commander daemon.** `slave-agent` has no `serve-daemon` path today, so slave codex sessions are invisible in Commander. Add a `serve-daemon` subcommand mirroring the driver's, so slave sessions are reported via `/api/daemon-link` and P3 can nest them.

**Tech Stack:** Go (stdlib), table-driven tests, `go test -race`.

**Spec:** `multi-agent/docs/superpowers/specs/2026-06-17-commander-exec-session-parent-link-design.md` (P2 section revised in `a1bb691`)

**Branch:** `commander-parent-link-p2p3` (off `origin/master` with P1 merged).

---

## File Structure

- `pkg/agentbackend/loomorigin.go` — **NEW**: `BuildLoomOrigin`/`ParseLoomOrigin` (shared by driver stamp + slave parse; both import agentbackend).
- `pkg/agentbackend/loomorigin_test.go` — **NEW**.
- `pkg/agentbackend/codexhome.go` — **NEW**: `ResolveCodexHome(codexHome, loomHome, shortID)` (Task 9). Lives in the low-level `pkg/agentbackend` package so both driver and slave can call it without coupling.
- `pkg/agentbackend/codexhome_test.go` — **NEW**.
- `pkg/agentbackend/codex/loommeta.go` — add `writeCurrentSession`/`ReadCurrentSession` (the `current` marker; not a sidecar).
- `pkg/agentbackend/codex/loommeta_test.go` — marker tests.
- `pkg/agentbackend/codex/executor.go` — write `current` marker on `thread.started` (Run **and** RunResume).
- `internal/commander/protocol.go` — `RegisterPayload` += `ShortID`.
- `internal/commanderhub/registry.go` + `hub.go` + `tree.go` — carry `ShortID`; `SessionRow` += `OwnerAgentID`/`ParentAgentID`/`ParentDisplayName`; `sessionRowFromBackend` copies parent fields. (No observer `agent_id → daemonConn` map is built — P3 resolves cross-daemon parents in the frontend from the `SessionRow` fields; offline parents use the denormalized `parent_display_name`.)
- `cmd/driver-agent/main.go` — populate `ShortID` in register; resolve `codex_home` from persisted ShortID; pass `cfg.Agent.CodexHome`.
- `cmd/slave-agent/main.go` — **new `serve-daemon` subcommand** (mirror driver); move `agentbackend.New` past `EnsureRegistered`; resolve `codex_home`; populate `ShortID`.
- `internal/driver/tools.go` + `contract_tools.go` — stamp `<loom_origin>` on parent-link delegations (`submit_task` default + chat skills, `submit_contract_task`, `resume_task`); `submit_contract_task.selectTarget` returns `targetShortID` so the journal carries `ChildAgentID` on the direct-match branch; `resume_task` recovers `ChildAgentID` via `TaskJournal.LatestByTaskID`; terminal child-link record into `driver-tasks.jsonl`. Shell/file/mcp handlers untouched.
- `internal/orchestrator/fanout.go` + `internal/orchestration/driver_runner.go` — propagate outer `executor.Task.SystemContext` (carries loom_origin marker) into each child `DelegateTaskRequest` so fanout-routed chat children inherit the parent link (Task 6b).
- `internal/driver/task_journal.go` — `TaskRecord` += child fields + `Terminal`; `Recent` dedups by task_id preferring terminal.
- `internal/poller/poller.go` — parse `<loom_origin>` → `Task.Parent*`, strip from `SystemContext`.
- `internal/config/config.go`, `internal/driver/config.go` — (fields added in P1; P2 wires them).
- `deploy/{linux,windows}/{driver,slave}/config.yaml.template` + `dev/configs/*.example.yaml` — set `codex_home`/`loom_home`.

---

## Task 1: `<loom_origin>` marker helper

**Files:**
- Create: `pkg/agentbackend/loomorigin.go`
- Create: `pkg/agentbackend/loomorigin_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/agentbackend/loomorigin_test.go`:

```go
package agentbackend

import (
	"strings"
	"testing"
)

func TestBuildAndParseLoomOriginRoundTrip(t *testing.T) {
	sc := BuildLoomOrigin("drv-1", "prod-driver", "thread-abc")
	got, cleaned, ok := ParseLoomOrigin(sc)
	if !ok {
		t.Fatalf("ParseLoomOrigin ok=false for %q", sc)
	}
	if got.SessionID != "thread-abc" || got.AgentID != "drv-1" || got.DisplayName != "prod-driver" {
		t.Fatalf("parsed = %+v", got)
	}
	if strings.Contains(cleaned, "loom_origin") {
		t.Fatalf("marker not stripped from cleaned context: %q", cleaned)
	}
}

func TestParseLoomOriginAbsent(t *testing.T) {
	_, _, ok := ParseLoomOrigin("just a normal system context")
	if ok {
		t.Fatal("ParseLoomOrigin should return ok=false when no marker")
	}
}

func TestBuildLoomOriginEscapesBoundary(t *testing.T) {
	// A display_name containing the closing tag must not break parsing.
	sc := BuildLoomOrigin("drv", "evil</loom_origin>", "t")
	// The marker must still parse to the (escaped) value and strip cleanly.
	got, _, ok := ParseLoomOrigin(sc)
	if !ok {
		t.Fatalf("escaped marker did not parse: %q", sc)
	}
	if got.DisplayName != "evil</loom_origin>" {
		t.Fatalf("display name lost: %q", got.DisplayName)
	}
}

func TestParseLoomOriginPreservesSurroundingContext(t *testing.T) {
	sc := "preamble\n" + BuildLoomOrigin("drv", "d", "t") + "\nepilogue"
	_, cleaned, ok := ParseLoomOrigin(sc)
	if !ok {
		t.Fatal("parse failed")
	}
	if !strings.Contains(cleaned, "preamble") || !strings.Contains(cleaned, "epilogue") {
		t.Fatalf("surrounding context lost: %q", cleaned)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/agentbackend/ -run LoomOrigin -v`
Expected: FAIL — `BuildLoomOrigin`/`ParseLoomOrigin` undefined.

- [ ] **Step 3: Implement `loomorigin.go`**

Use a **single-line JSON marker** (robust against any value — `/>`, quotes, `<` — via `encoding/json` escaping; no hand-rolled XML parsing). One line, prefixed so it's cheap to find and strip:

```go
package agentbackend

import (
	"encoding/json"
	"strings"
)

// ParentLink is the origin tuple carried by the loom_origin marker. JSON tags
// fix the on-disk field names (agent/name/session) independent of Go field
// order, so callers can unmarshal without depending on marshal ordering.
type ParentLink struct {
	SessionID   string `json:"session"`
	AgentID     string `json:"agent"`
	DisplayName string `json:"name"`
}

type loomOriginMarker struct {
	LoomOrigin ParentLink `json:"loom_origin"`
}

const loomOriginPrefix = `{"loom_origin":`

// BuildLoomOrigin renders a single-line JSON marker carrying the parent link
// through DelegateTaskRequest.SystemContext. Values are JSON-escaped, so any
// display name (incl. ones containing "/>", quotes, or "<") is safe. The
// marker is one line ending with "\n".
func BuildLoomOrigin(agentID, displayName, sessionID string) string {
	b, _ := json.Marshal(loomOriginMarker{LoomOrigin: ParentLink{
		AgentID: agentID, DisplayName: displayName, SessionID: sessionID,
	}})
	return string(b) + "\n"
}

// ParseLoomOrigin extracts the parent link from a SystemContext string and
// returns the context with the marker line removed. ok is false when no
// well-formed marker is present. Robust: uses encoding/json, so values
// containing "/>", quotes, or "<" parse correctly.
func ParseLoomOrigin(systemContext string) (ParentLink, string, bool) {
	var found ParentLink
	markerLine := ""
	rest := make([]string, 0, 4)
	for _, line := range strings.Split(systemContext, "\n") {
		trimmed := strings.TrimSpace(line)
		if markerLine == "" && strings.HasPrefix(trimmed, loomOriginPrefix) {
			var m loomOriginMarker
			if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
				found = m.LoomOrigin
				markerLine = line
				continue // drop this line from cleaned output
			}
		}
		rest = append(rest, line)
	}
	if markerLine == "" {
		return ParentLink{}, systemContext, false
	}
	if found.SessionID == "" && found.AgentID == "" {
		return ParentLink{}, systemContext, false
	}
	cleaned := strings.Join(rest, "\n")
	// Trim a leading blank line left where the marker was, if it was first.
	cleaned = strings.TrimPrefix(cleaned, "\n")
	return found, cleaned, true
}
```

- [ ] **Step 4: Add adversarial parser tests**

Append to `loomorigin_test.go` (cover the cases the prior hand-rolled parser broke on):

```go
func TestParseLoomOriginHandlesAdversarialValues(t *testing.T) {
	for _, name := range []string{`evil"/>`, `has "quotes" and <tags>`, `back\slash`, `multi\nline`} {
		sc := BuildLoomOrigin("drv", name, "t")
		got, cleaned, ok := ParseLoomOrigin(sc)
		if !ok {
			t.Fatalf("name %q: parse failed for %q", name, sc)
		}
		if got.DisplayName != name {
			t.Fatalf("name %q: round-trip got %q", name, got.DisplayName)
		}
		if strings.Contains(cleaned, "loom_origin") {
			t.Fatalf("name %q: marker not stripped: %q", name, cleaned)
		}
	}
}

func TestParseLoomOriginMultipleMarkersUsesFirst(t *testing.T) {
	sc := BuildLoomOrigin("drv", "d1", "t1") + BuildLoomOrigin("drv", "d2", "t2")
	got, _, ok := ParseLoomOrigin(sc)
	if !ok || got.SessionID != "t1" {
		t.Fatalf("want first marker t1, got %+v ok=%v", got, ok)
	}
}

func TestParseLoomOriginMalformedMarkerSkipped(t *testing.T) {
	sc := `{"loom_origin": not-json}` // malformed
	_, _, ok := ParseLoomOrigin(sc)
	if ok {
		t.Fatal("malformed marker should return ok=false")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/ -run LoomOrigin -v -race`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/loomorigin.go pkg/agentbackend/loomorigin_test.go
git commit -m "feat(agentbackend): add loom_origin marker build/parse helper (#24 P2)"
```

---

## Task 2: `current` session marker (executor writes on thread.started)

**Files:**
- Modify: `pkg/agentbackend/codex/loommeta.go`
- Modify: `pkg/agentbackend/codex/loommeta_test.go`
- Modify: `pkg/agentbackend/codex/executor.go`

- [ ] **Step 1: Write the failing tests**

Append to `loommeta_test.go`:

```go
func TestWriteReadCurrentSession(t *testing.T) {
	base := t.TempDir()
	if err := writeCurrentSession(base, "thread-now"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}
	if got := ReadCurrentSession(base); got != "thread-now" {
		t.Fatalf("readCurrentSession = %q, want thread-now", got)
	}
}

func TestReadCurrentSessionMissing(t *testing.T) {
	if got := ReadCurrentSession(t.TempDir()); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
```

Append to `executor_test.go`:

```go
func TestCodexExecutorWritesCurrentSessionMarkerOnRun(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-cur","timestamp":"2026-06-22T10:00:00Z"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, []string{"CODEX_HOME=" + home})
	if _, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ReadCurrentSession(home); got != "thr-cur" {
		t.Fatalf("current marker = %q, want thr-cur", got)
	}
}

func TestCodexExecutorWritesCurrentSessionMarkerOnResume(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-res","timestamp":"2026-06-22T10:00:00Z"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, []string{"CODEX_HOME=" + home})
	if _, err := ex.RunResume(context.Background(), "thr-res", "continue", &captureSink{}); err != nil {
		t.Fatalf("RunResume: %v", err)
	}
	// RunResume must NOT write a sidecar (P1) but MUST write the current marker.
	if _, ok := readLoomMeta(home, "thr-res"); ok {
		t.Fatal("RunResume must not write a sidecar")
	}
	if got := ReadCurrentSession(home); got != "thr-res" {
		t.Fatalf("current marker = %q, want thr-res (RunResume updates marker)", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'CurrentSession|WritesCurrentSession' -v`
Expected: FAIL — `writeCurrentSession`/`ReadCurrentSession` undefined; marker not written.

- [ ] **Step 3: Add marker helpers to `loommeta.go`**

`ReadCurrentSession` is **exported** (internal/driver reads it — it cannot call unexported codex funcs). `writeCurrentSession` stays unexported (only the codex executor writes).

```go
// writeCurrentSession writes a transient pointer to the currently-active
// codex thread id, read by serve-mcp to learn the parent session when codex
// calls submit_task (codex and serve-mcp are separate processes sharing
// CODEX_HOME). Written on every thread.started (Run + RunResume). NOT a
// sidecar; does not participate in parent-link merge or reaping. best-effort.
func writeCurrentSession(base, threadID string) error {
	if base == "" || threadID == "" {
		return nil
	}
	if err := os.MkdirAll(loomMetaDir(base), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(loomMetaDir(base), "current"), []byte(threadID), 0o600)
}

// ReadCurrentSession returns the thread id last written by writeCurrentSession,
// or "" if absent/unreadable. Exported so internal/driver (serve-mcp) can read
// the parent session marker; it cannot call codex's unexported helpers.
func ReadCurrentSession(base string) string {
	if base == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(loomMetaDir(base), "current"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
```

Also **export `EffectiveCodexHome`** in `codexenv.go` (rename `effectiveCodexHome` → `EffectiveCodexHome`; update internal callers `b.effectiveCodexHome()` → `b.EffectiveCodexHome()` and the executor). internal/driver needs it to resolve the marker base from `cfg.Agent.CodexHome` (with the `$HOME/.codex` fallback when unset). The launcher (Task 9) resolves `cfg.Agent.CodexHome` from short_id; serve-mcp then calls `codex.EffectiveCodexHome(agentbackend.Config{CodexHome: cfg.Agent.CodexHome}, nil)` → `codex.ReadCurrentSession(base)`.

- [ ] **Step 4: Write the marker in the executor's thread.started block**

In `pkg/agentbackend/codex/executor.go`, the `thread.started` block (around line 202) currently writes the sidecar only when `newSession`. Add the marker write **outside** the `if newSession` (so RunResume also writes it):

```go
		if ev.Type == "thread.started" && sessionID == "" && ev.ThreadID != "" {
			sessionID = ev.ThreadID
			// current-session marker: written on BOTH Run and RunResume so
			// serve-mcp can learn the parent session during any turn. Best-effort.
			// Uses the EXPORTED EffectiveCodexHome (renamed in Step 3).
			_ = writeCurrentSession(EffectiveCodexHome(e.cfg, e.env), sessionID)
			if newSession {
				// ... existing sidecar write (unchanged; it also calls
				// EffectiveCodexHome(e.cfg, e.env) for the sidecar base) ...
			}
			continue
		}
```

(Step 3 renamed `effectiveCodexHome` → `EffectiveCodexHome` everywhere: the package func, `b.effectiveCodexHome()` → `b.EffectiveCodexHome()`, and the executor's existing sidecar-write call. Verify with `grep -n effectiveCodexHome pkg/agentbackend/codex` returning nothing after the rename.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'CurrentSession|WritesCurrentSession' -v -race`
Expected: PASS (Run writes marker; RunResume writes marker but no sidecar).

- [ ] **Step 6: Full codex package + race**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/agentbackend/codex/loommeta.go pkg/agentbackend/codex/loommeta_test.go pkg/agentbackend/codex/codexenv.go pkg/agentbackend/codex/codexenv_test.go pkg/agentbackend/codex/executor.go pkg/agentbackend/codex/executor_test.go pkg/agentbackend/codex/backend.go
git commit -m "feat(codex): current-session marker + export EffectiveCodexHome/ReadCurrentSession (#24 P2)"
```

(`codexenv.go` for the `effectiveCodexHome` → `EffectiveCodexHome` rename + export; `backend.go` for the `b.effectiveCodexHome()` → `b.EffectiveCodexHome()` method rename.)

---

## Task 3: `RegisterPayload` carries `ShortID`; daemon + driver/slave populate it

**Files:**
- Modify: `internal/commander/protocol.go`
- Modify: `cmd/driver-agent/main.go` (register payload in `runServeDaemon`)
- Test: `internal/commander/protocol_test.go`

- [ ] **Step 1: Add the field**

In `internal/commander/protocol.go`, add to `RegisterPayload` (after `DriverVersion`):

```go
	// ShortID is the stable agent-instance id (agentserver-assigned, persisted).
	// Lets the observer resolve a parent across reconnects (daemon_id is
	// ephemeral). Empty for old daemons.
	ShortID string `json:"short_id,omitempty"`
```

- [ ] **Step 2: Driver populate**

In `cmd/driver-agent/main.go` `runServeDaemon`, the `RegisterPayload` (around line 352) — add:

```go
				ShortID:       cfg.Credentials.ShortID,
```

- [ ] **Step 3: Test round-trip**

Append to `internal/commander/protocol_test.go`:

```go
func TestRegisterPayloadShortIDRoundTrip(t *testing.T) {
	in := RegisterPayload{SchemaVersion: SchemaVersion, Kind: "codex", ShortID: "drv-1"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out RegisterPayload
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ShortID != "drv-1" {
		t.Fatalf("ShortID = %q, want drv-1", out.ShortID)
	}
}
```

- [ ] **Step 4: Build + test**

Run: `go build ./... && go test ./internal/commander/ -run RegisterPayload -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/commander/protocol.go internal/commander/protocol_test.go cmd/driver-agent/main.go
git commit -m "feat(commander): carry ShortID in register payload (#24 P2)"
```

---

## Task 4: commanderhub carries `ShortID` + parent fields on `SessionRow`

**Files:**
- Modify: `internal/commanderhub/registry.go`, `hub.go`, `tree.go`
- Test: `internal/commanderhub/tree_test.go` (or `registry_test.go`)

- [ ] **Step 1: Add fields**

`registry.go` `daemonConn` += `shortID string`; `DaemonInfo` += `ShortID string` (json `short_id`). Populate from `RegisterPayload.ShortID` in `hub.go` where `displayName`/`driverVersion` are set (around line 111-113).

`tree.go` `SessionRow` +=:

```go
	OwnerAgentID     string `json:"owner_agent_id,omitempty"`
	ParentAgentID    string `json:"parent_agent_id,omitempty"`
	ParentDisplayName string `json:"parent_display_name,omitempty"`
```

`sessionRowFromBackend` (tree.go ~line 84): copy `ParentAgentID`/`ParentDisplayName` from `sess`; set `OwnerAgentID` from the daemon's ShortID (passed in — change the signature to take `ownerAgentID string`, or look it up; simplest: pass `info.DaemonInfo`/ShortID into `sessionRowFromBackend`).

- [ ] **Step 2: Wire OwnerAgentID**

`sessionRowFromBackend(daemonID, ownerAgentID string, sess, snap)` — add `ownerAgentID` param; set `OwnerAgentID: ownerAgentID`. Update callers in `refreshSessionRows`/`mergeCurrentTurnState` to pass `info.ShortID`.

- [ ] **Step 3: Test**

Append a test asserting a `Session` with `ParentAgentID`/`ParentDisplayName` flows into `SessionRow`, and `OwnerAgentID` = the daemon's ShortID.

```go
func TestSessionRowCarriesParentAndOwner(t *testing.T) {
	sess := agentbackend.Session{ID: "s1", Origin: agentbackend.SessionOriginAgentTask,
		ParentAgentID: "drv-1", ParentDisplayName: "prod-driver"}
	row := sessionRowFromBackend("d1", "slave-2", sess, turnSnapshot{})
	if row.OwnerAgentID != "slave-2" || row.ParentAgentID != "drv-1" || row.ParentDisplayName != "prod-driver" {
		t.Fatalf("row = %+v", row)
	}
}
```

- [ ] **Step 4: Build + test + race**

Run: `go build ./... && go test ./internal/commanderhub/ -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/registry.go internal/commanderhub/hub.go internal/commanderhub/tree.go internal/commanderhub/*_test.go
git commit -m "feat(commanderhub): carry ShortID + parent fields on SessionRow (#24 P2)"
```

---

## Task 5: Slave `serve-daemon` subcommand

**Files:**
- Modify: `cmd/slave-agent/main.go`
- Test: `cmd/slave-agent/main_test.go`

Mirror `cmd/driver-agent/main.go runServeDaemon` (lines 303-388). The slave already loads `cfg` via `config.LoadConfig`, has `cfg.Credentials.ProxyToken`, `cfg.Observer.URL`, `cfg.Agent.*`, `cfg.Credentials.ShortID`, and a backend (built in its main run).

- [ ] **Step 1: Add `serve-daemon` case + runner**

In `cmd/slave-agent/main.go` switch (near the top, alongside `humanloop-mcp`):

```go
	case "serve-daemon":
		runServeDaemon(os.Args[2:])
```

Add a `runServeDaemon` (mirror driver's): load config; require `proxy_token` + `observer.url`; resolve `codex_home` via `agentbackend.ResolveCodexHome` (see Task 9) and pass into `agentbackend.New`; build `commander.Handler{Backend: backend, WorkerMax: cfg.Daemon.WorkerMax}`; `commander.NewDaemon` with `RegisterPayload` including `ShortID: cfg.Credentials.ShortID`, `Kind: cfg.Agent.Kind`, `DisplayName: cfg.Discovery.DisplayName`; run until signal.

- [ ] **Step 2: Resolve codex_home before backend creation**

The slave's main run currently creates `backend` at `:188` **before** `EnsureRegistered` (`:213`). For `serve-daemon`, resolve `codex_home` from persisted `cfg.Credentials.ShortID` (driver-style; no `EnsureRegistered` in serve-daemon) and set `cfg.Agent.CodexHome` before `agentbackend.New`. (Task 8 handles the non-daemon slave run's ordering.)

- [ ] **Step 3: Test**

`cmd/slave-agent/main_test.go` — add `TestServeDaemonParseFlags` (mirrors driver's) and a smoke test that `serve-daemon` builds a daemon with `ShortID` in the register payload (use a fake observer WS like the driver's daemon test).

- [ ] **Step 4: Build + test**

Run: `go build ./... && go test ./cmd/slave-agent/ -run ServeDaemon -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/slave-agent/main.go cmd/slave-agent/main_test.go
git commit -m "feat(slave-agent): add serve-daemon subcommand (#24 P2)"
```

---

## Task 6: Driver stamps `<loom_origin>` on chat-capable AND fanout delegations

**Scope (which handlers stamp):** every delegation that can transitively produce a codex exec session on a slave — that means chat-capable skills *and* `fanout`/`route`/`fanout_strict`/`""` (which the master orchestrator fans out into one-or-more chat children — see `dispatch.go:138`, `tools.go:235`, and the orchestrator propagation added in Task 6b below). Shell/file/mcp skills still don't stamp.

- **STAMP** (these build a `DelegateTaskRequest` whose `SystemContext` will reach a codex exec session — either directly, or after fanout propagation):
  - `submit_task` — `internal/driver/tools.go:511` (default skill is `fanout` per `tools.go:398`)
  - `submit_contract_task` — `internal/driver/contract_tools.go:100`
  - `resume_task` — `internal/driver/tools.go:1140`
- **DO NOT STAMP** (terminal non-codex skills; leave untouched):
  - `run_slave_bash` / `run_slave_powershell` / `run_slave_shell` — `internal/driver/slave_tools.go:150/279`
  - `read_slave_file` / `write_slave_file` / `stat_slave_file` — `internal/driver/slave_file_tools.go:128/349/431` (file skills, no `SystemContext`)
  - `register_slave_mcp` / `unregister_slave_mcp` — `internal/driver/register_mcp_tool.go:59`, `unregister_mcp_tool.go:54` (mcp skills, no codex session)

**Files:**
- Modify: `internal/driver/tools.go` (`submit_task` ~511, `resume_task` ~1140), `internal/driver/contract_tools.go` (`submit_contract_task` ~100)
- Test: `internal/driver/tools_test.go`

- [ ] **Step 1: Add a stamping helper on `Tools`**

In `internal/driver/tools.go`, add a helper that resolves the effective codex home (via the **exported** `codex.EffectiveCodexHome` from Task 2) and reads the current-session marker (via **exported** `codex.ReadCurrentSession`):

```go
import (
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	"github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

// loomOriginMarker returns the loom_origin marker for the current driver
// session. parent_session_id comes from the current-session marker written by
// the codex executor (shared CODEX_HOME); agent_id/display_name from cfg.
// Best-effort: absent marker → empty session (marker still carries agent+name).
func (t *Tools) loomOriginMarker() string {
	base := codex.EffectiveCodexHome(agentbackend.Config{CodexHome: t.cfg.Agent.CodexHome}, nil)
	sess := codex.ReadCurrentSession(base)
	return agentbackend.BuildLoomOrigin(t.cfg.Credentials.ShortID, t.cfg.Discovery.DisplayName, sess)
}
```

(This requires `cfg.Agent.CodexHome` to be resolved at launcher startup — Task 9 sets it from short_id for both serve-mcp and serve-daemon.)

- [ ] **Step 2: Stamp `SystemContext` in the three delegation handlers (predicate-gated)**

The predicate must include **fanout-routed skills** because the *default* `submit_task` skill is `"fanout"` (`tools.go:398`), and a fanout master then re-delegates to chat-capable slaves (Task 6b propagates the marker through that fanout). Without `fanout` in the predicate the most common code path — bare `submit_task` — gets no parent link at all.

```go
// isParentLinkDelegation reports whether the delegated skill can transitively
// produce a codex exec session on a slave (directly via chat, or after master
// fanout). Stamps the loom_origin marker so the child's session inherits the
// originating driver session. Matches dispatch.go:138 + tools.go:235.
func isParentLinkDelegation(skill string) bool {
	switch skill {
	case "", "chat", "chat_resume", "fanout", "fanout_strict", "route":
		return true
	}
	return false
}
```

In `submit_task` (tools.go ~511), where `DelegateTaskRequest` is built, prepend the marker **only when `isParentLinkDelegation(skill)`**. `submit_task` does not currently pass `SystemContext` to `DelegateTask` (see `tools.go:511`); add the field at the same time:

```go
	systemContext := ""
	if isParentLinkDelegation(skill) {
		if m := s.t.loomOriginMarker(); m != "" {
			systemContext = m
		}
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         finalPrompt,
		SystemContext:  systemContext,
		TimeoutSeconds: timeout,
	})
```

Apply the identical predicate-gated stamp in `submit_contract_task` (`contract_tools.go:100`) and `resume_task` (`tools.go:1140`). Do **not** touch shell/file/mcp handlers. The fanout-propagation half (so slave chat children inherit the marker) is Task 6b below.

- [ ] **Step 3: Test (unmarshal-based; cover default + chat + non-chat)**

`internal/driver/tools_test.go` — with a fake SDK + a temp `CODEX_HOME` (set `cfg.Agent.CodexHome`) + a written `current` marker:

```go
// submit_task with default skill (empty → "fanout") stamps the marker.
// This is the most common path; if it stops stamping, P2 fails its goal.
reqDefault := fakeSDK.lastDelegateRequest
p, cleaned, ok := agentbackend.ParseLoomOrigin(reqDefault.SystemContext)
if !ok || p.AgentID != shortID || p.DisplayName != displayName || p.SessionID != markerSession {
	t.Fatalf("default (fanout) delegation not stamped correctly: %+v", p)
}
if reqDefault.Skill != "fanout" {
	t.Fatalf("default skill expected fanout, got %q", reqDefault.Skill)
}
if strings.Contains(cleaned, "loom_origin") {
	t.Fatalf("marker not stripped-cleanable: %q", cleaned)
}

// submit_task with skill="chat" also stamps.
reqChat := fakeSDK.lastDelegateRequestForChat
if _, _, ok := agentbackend.ParseLoomOrigin(reqChat.SystemContext); !ok {
	t.Fatal("chat delegation must be stamped")
}

// submit_task with skill="bash" (terminal non-codex) does NOT stamp.
reqBash := fakeSDK.lastDelegateRequestForBash
if _, _, ok := agentbackend.ParseLoomOrigin(reqBash.SystemContext); ok {
	t.Fatal("bash delegation must not be stamped")
}
```

- [ ] **Step 4: Build + test + race**

Run: `go build ./... && go test ./internal/driver/ -run 'SubmitTask|SlaveBash' -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driver/tools.go internal/driver/contract_tools.go internal/driver/tools_test.go
git commit -m "feat(driver): stamp <loom_origin> on parent-link delegations (#24 P2)"
```

---

## Task 6b: Fanout/contract pipelines propagate `SystemContext` to chat children

**Problem:** Task 6 stamps the marker on the driver's `submit_task`, but four hops drop it before any slave chat child sees it:

1. **`internal/orchestrator/fanout.go:474`** (master `runFanout`) — uses only `n.SystemContext`; `planner.Node.SystemContext` is `json:"-"` (`internal/planner/planner.go:50`) and never receives the outer task's value.
2. **`internal/orchestrator/fanout.go:1091`** (`runFanoutResume` re-dispatcher) — same shape.
3. **`internal/orchestration/driver_runner.go:160-166`** (`DriverRunner.runNode`) — receives only `(ctx, n, outputs, agents)`; outer `SystemContext` is not threaded in. `DriverRunner.Run` currently has signature `Run(ctx, prompt string)` (`driver_runner.go:46`) — no `SystemContext` parameter at all.
4. **`internal/driver/contract_tools.go:79`** — `s.t.contractRunner.Run(ctx, finalPrompt)` calls the `ContractRunner` interface (`internal/driver/tools.go:33-35`), which today is `Run(ctx context.Context, prompt string) (orchestration.RunnerResult, error)` — also no `SystemContext` slot. The `driver_fanout` route (the contract-tool equivalent of fanout) silently strips the marker because the interface has nowhere to put it.

**Fix:** widen the `Run` signatures end-to-end, and route the loom_origin marker through them. Plain `string` second arg keeps the change minimal and avoids exposing a new opts struct just for this one field.

- Modify: `internal/orchestration/driver_runner.go` — change `Run(ctx, prompt)` → `Run(ctx, prompt, systemContext string)`; thread `systemContext` into `runPreparedPlan` → `runNode` and merge with `n.SystemContext` before the `DelegateTask` call (`:160-166`).
- Modify: `internal/driver/tools.go` — change `ContractRunner.Run` to `Run(ctx, prompt, systemContext string)` so the interface matches.
- Modify: `internal/driver/contract_tools.go:79` — pass the loom_origin marker (built via `t.loomOriginMarker()`, Task 6) as the `systemContext` arg on the `driver_fanout` route (the route delegates to a driver-local fanout runner, so the same predicate from Task 6 applies — only stamp when `isParentLinkDelegation(skill)` would have stamped a delegated request).
- Modify: `internal/orchestrator/fanout.go` (`runFanout`, `runFanoutResume`) — merge `t.SystemContext` (the outer `executor.Task.SystemContext` already in scope) with `n.SystemContext` at each `DelegateTask` call.
- Modify: callers of `DriverRunner.Run` and `ContractRunner.Run` — pass `""` (or a recovered marker) where appropriate; tests added below cover the parent-link cases.

**Helper placement (avoid the cross-package pitfall):** put the merge helper in **`pkg/agentbackend`** (alongside `BuildLoomOrigin`/`ParseLoomOrigin`) and **export** it:

```go
// MergeSystemContext returns outer+inner with a single separating newline when
// both are non-empty. Outer comes first so its loom_origin marker wins the
// first-match rule in ParseLoomOrigin (one effective marker per context).
// BuildLoomOrigin always ends with "\n" — trim trailing newlines on outer
// before joining so we don't synthesize a blank line every hop.
func MergeSystemContext(outer, inner string) string {
	outer = strings.TrimRight(outer, "\n")
	switch {
	case outer == "" && inner == "":
		return ""
	case outer == "":
		return inner
	case inner == "":
		return outer + "\n"
	default:
		return outer + "\n" + inner
	}
}
```

Both `internal/orchestrator` and `internal/orchestration` already import `pkg/agentbackend` for `BuildLoomOrigin`/`ParseLoomOrigin`, so this adds no new dependency edges. (A package-local helper in `internal/orchestrator/fanout.go` would be invisible from `internal/orchestration/driver_runner.go` — that's the package-boundary bug being fixed.)

**Files:**
- Modify: `pkg/agentbackend/loomorigin.go` (new exported `MergeSystemContext` + tests in `loomorigin_test.go`)
- Modify: `internal/orchestrator/fanout.go`, `internal/orchestration/driver_runner.go`, `internal/driver/tools.go`, `internal/driver/contract_tools.go`
- Test: `internal/orchestrator/fanout_test.go` (new), `internal/orchestration/driver_runner_test.go` (new), `internal/driver/contract_tools_test.go` (new case for `driver_fanout` route)

- [ ] **Step 1: Write the failing tests**

`pkg/agentbackend/loomorigin_test.go`:

```go
func TestMergeSystemContextNoBlankLineFromMarker(t *testing.T) {
	marker := BuildLoomOrigin("drv", "d", "t") // ends with "\n"
	merged := MergeSystemContext(marker, "child preamble")
	if strings.Contains(merged, "\n\n") {
		t.Fatalf("merge produced double newline: %q", merged)
	}
	if _, cleaned, ok := ParseLoomOrigin(merged); !ok || !strings.Contains(cleaned, "child preamble") {
		t.Fatalf("merged context lost child preamble or marker: %q (ok=%v cleaned=%q)", merged, ok, cleaned)
	}
}

func TestMergeSystemContextEmptyArgs(t *testing.T) {
	if got := MergeSystemContext("", ""); got != "" {
		t.Fatalf("empty,empty = %q", got)
	}
	if got := MergeSystemContext("", "x"); got != "x" {
		t.Fatalf("empty,x = %q", got)
	}
	if got := MergeSystemContext("x\n", ""); got != "x\n" {
		t.Fatalf("x\\n,empty = %q", got)
	}
}
```

`internal/orchestrator/fanout_test.go`: outer `executor.Task{SystemContext: agentbackend.BuildLoomOrigin("drv-1","prod-driver","thr-1")}` + planner emitting one chat node with empty `SystemContext`. Capture the fake SDK's `DelegateTaskRequest`; assert `ParseLoomOrigin(req.SystemContext)` returns `(drv-1, prod-driver, thr-1)` and that the request body has no double newline. Second case: child node has its own preamble — assert preamble survives merge and the outer marker wins first-match.

`internal/orchestration/driver_runner_test.go`: analogous test, calling `r.Run(ctx, prompt, agentbackend.BuildLoomOrigin(...))`.

`internal/driver/contract_tools_test.go`: a `submit_contract_task` whose contract routes to `driver_fanout` — assert the test `ContractRunner` (fake) sees the loom_origin marker in its second arg.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/agentbackend/ ./internal/orchestrator/ ./internal/orchestration/ ./internal/driver/ -run 'MergeSystemContext|Fanout.*SystemContext|DriverRunner.*SystemContext|DriverFanout.*Marker' -v`
Expected: FAIL — outer `SystemContext` isn't forwarded.

- [ ] **Step 3: Widen signatures and merge in every dispatcher**

1. **`pkg/agentbackend/loomorigin.go`** — add `MergeSystemContext` above.
2. **`internal/orchestrator/fanout.go:474`** and **`:1091`** — replace `SystemContext: n.SystemContext` with `SystemContext: agentbackend.MergeSystemContext(t.SystemContext, n.SystemContext)` (`t` is the outer `executor.Task` already in scope in both `runFanout` and `runFanoutResume`).
3. **`internal/orchestration/driver_runner.go`** — change `func (r *DriverRunner) Run(ctx, prompt string)` → `func (r *DriverRunner) Run(ctx, prompt, systemContext string)`. Thread `systemContext` through `runPreparedPlan(ctx, prompt, systemContext, nodes, agents)` → `runNode(ctx, n, outerCtx, outputs, agents)`. In `runNode` set `SystemContext: agentbackend.MergeSystemContext(outerCtx, n.SystemContext)` on the `DelegateTaskRequest` (`:160-166`).
4. **`internal/driver/tools.go:33-35`** — widen the `ContractRunner` interface to `Run(ctx context.Context, prompt, systemContext string) (orchestration.RunnerResult, error)`.
5. **`internal/driver/contract_tools.go:79`** — the `driver_fanout` route is, by definition, a fanout (the driver itself plays master and re-delegates to slaves), so it's always parent-link. No predicate needed. Replace the existing call:

    ```go
    result, err := s.t.contractRunner.Run(ctx, finalPrompt, s.t.loomOriginMarker())
    ```

    `loomOriginMarker()` returns `""` if the marker isn't available; `MergeSystemContext` inside the runner then no-ops the prepend and the existing code path is unaffected.
6. **All other callers** of `DriverRunner.Run` and `ContractRunner.Run` (find via `grep -rn "ContractRunner\|\.Run(ctx" cmd/ internal/`) — pass `""` as the new third arg (only the `driver_fanout` and reserve paths need a non-empty marker).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/ ./internal/orchestrator/ ./internal/orchestration/ ./internal/driver/ -race -count=1`
Expected: PASS — chat child carries the outer marker; pre-existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/agentbackend/loomorigin.go pkg/agentbackend/loomorigin_test.go internal/orchestrator/fanout.go internal/orchestrator/fanout_test.go internal/orchestration/driver_runner.go internal/orchestration/driver_runner_test.go internal/driver/tools.go internal/driver/contract_tools.go internal/driver/contract_tools_test.go
git commit -m "feat(orchestrator): propagate parent SystemContext through fanout/contract runners (#24 P2)"
```

---

## Task 7: Slave parses `<loom_origin>` → `Task.Parent*`

**Files:**
- Modify: `internal/poller/poller.go` (line 157)
- Test: `internal/poller/poller_test.go`

- [ ] **Step 1: Write the failing test**

Append to `poller_test.go`: a task whose `SystemContext` carries a `<loom_origin>` marker reaches the dispatcher with `Task.ParentSessionID/ParentAgentID/ParentDisplayName` set and the marker stripped from `SystemContext`. Use a fake executor that records the `Task` it receives.

- [ ] **Step 2: Parse in the poller**

In `internal/poller/poller.go` (line 157), where `executor.Task` is built:

```go
	systemContext := t.SystemContext
	var parent agentbackend.ParentLink
	if p, cleaned, ok := agentbackend.ParseLoomOrigin(systemContext); ok {
		parent = p
		systemContext = cleaned
	}
	res, err := p.disp.Run(ctx, executor.Task{
		ID:            t.TaskID,
		Skill:         t.Skill,
		Prompt:        t.Prompt,
		SystemContext: systemContext,
		TimeoutSec:    t.TimeoutSeconds,
		ParentSessionID:   parent.SessionID,
		ParentAgentID:     parent.AgentID,
		ParentDisplayName: parent.DisplayName,
	})
```

(Receiver name `p` shadows — rename the parsed `parent` var or the receiver; use `pl` for the parsed link if the receiver is `p`.)

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./internal/poller/ -run LoomOrigin -v -race`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/poller/poller.go internal/poller/poller_test.go
git commit -m "feat(poller): parse <loom_origin> into Task parent fields (#24 P2)"
```

---

## Task 8: Reverse link — append a terminal journal record at result time

**Timing problem (review #2):** `recordDelegatedTask` (`tools.go:72`) calls `taskJournal.Append` immediately after `DelegateTask` returns (`tools.go:528`) — **before** the slave has run, so the child session id is unknown. The journal is **append-only** (`task_journal.go` has only `Append`/`Recent`/`RecentWithWarnings`, no `Update`). The child session id only becomes known when the result marker arrives, in `get_task` (`tools.go:619`, `sessionIDFromMarker`) / `wait_task` (`tools.go:748`).

**Fix:** keep the initial `recordDelegatedTask` append as-is (delegation-time, no child fields), and **append a second, terminal record** when the result marker arrives. The journal stays append-only; `Recent`/`RecentWithWarnings` already return newest-first and the reader correlates by `task_id` (keep the terminal record; or dedup by task_id keeping the one with `child_session_id`).

**Files:**
- Modify: `internal/driver/task_journal.go` (`TaskRecord` += `ChildSessionID`/`ChildAgentID` + `Terminal bool`; `Recent` dedups by task_id keeping terminal when present)
- Modify: `internal/driver/tools.go` (in `get_task` ~619 and `wait_task` ~748, where `sessionIDFromMarker` resolves the child session, append a terminal record)
- Test: `internal/driver/task_journal_test.go`, `internal/driver/tools_test.go`

- [ ] **Step 1: Extend `TaskRecord`; populate `ChildAgentID` at delegation; add `LatestByTaskID`**

`task_journal.go`:

```go
type TaskRecord struct {
	// ... existing fields ...
	ChildSessionID string `json:"child_session_id,omitempty"`
	ChildAgentID   string `json:"child_agent_id,omitempty"` // populated at delegation (target shortID) — for resume_task, recovered from LatestByTaskID(original_task_id)
	Terminal       bool   `json:"terminal,omitempty"`       // result-time record carrying child link
}

// LatestByTaskID returns the most recent record for taskID (newest-first scan),
// or ok=false if none. Used at result time to read the delegation-time
// ChildAgentID/Tool/TargetID/Skill when appending the terminal record.
func (j *TaskJournal) LatestByTaskID(taskID string) (TaskRecord, bool) {
	recs, err := j.Recent(500, taskID)
	if err != nil || len(recs) == 0 {
		return TaskRecord{}, false
	}
	return recs[0], true // Recent is newest-first
}
```

`Recent`/`RecentWithWarnings` semantics (**carefully scoped — do not break existing multi-non-terminal behavior**): only when a record with `Terminal=true` exists for a given `task_id`, hide the **delegation-time, non-terminal** record(s) for that same `task_id` from the result. Records that are not part of the delegation→terminal pair (e.g. multiple non-terminal `resume_task` rows, or any record on a task that has no terminal counterpart) MUST still appear. Concretely: build `terminalTaskIDs := {task_id : exists rec where rec.Terminal && rec.TaskID == task_id}`; on the final pass, drop a record iff `!rec.Terminal && terminalTaskIDs[rec.TaskID]`. Add a regression test that two non-terminal `resume_task` rows for the same `task_id` (no terminal record) are still both returned newest-first — preserving the current contract exercised by `TestListDriverTasksFiltersTaskID` and friends in `internal/driver/task_journal_test.go`.

`recordDelegatedTask` (`tools.go:72`): add `ChildAgentID` to `delegatedTaskRecord`. **Three delegation paths must set it consistently** — `resolveTarget` already returns `shortID` (used by `submit_task`), but `submit_contract_task`'s `selectTarget` direct-match branch (`contract_tools.go:147-153`) returns only `agentID/displayName/skill/route` and `resume_task` (`tools.go:1140-1145`) reuses `info.TargetID` from `GetTask`. Fix:

- `internal/driver/contract_tools.go:144` — change `selectTarget`'s return to `(targetID, targetName, targetShortID, skill, route string, err error)`. The direct-match branch reads `matches[0]` via `cardShortID(matches[0])` (helper already exists, `tools.go:250`); the fallback branch calls `s.t.resolveTarget(...)` and forwards its existing `shortID` return (currently discarded with `_`).
- `internal/driver/tools.go:1140` `resume_task` — `info.TargetID` is an agent_id only. **Recover the child shortID** by reading the journal: `prev, ok := r.t.taskJournal.LatestByTaskID(args.LastTaskID); childAgent := prev.ChildAgentID` (the original `submit_task`/`submit_contract_task` already persisted it at delegation time). If `prev` is missing (e.g. journal rotated), fall through with `ChildAgentID == ""` — the terminal record still carries `ChildSessionID`, and the parent index degrades to session-only for that one task; document this acceptable degraded mode.

(Reverse-marker expansion — i.e. teaching the slave to embed `agent_id` in the `session_id` kind marker, so the driver could parse it from `sessionIDFromMarker` — was considered but rejected: it requires slave-side codex backend changes and an envelope version bump, while the journal-recovery approach is local to the driver and uses already-persisted state.)

- [ ] **Step 2: Append terminal record at result time — idempotent, status-faithful**

In `get_task` (`tools.go:619`) and `wait_task` (`tools.go:715-758` — the `switch info.Status { case "completed", "failed", "cancelled":` branch), where `sessionIDFromMarker` already extracts the child session, append a terminal record. Both tools can be called many times for the same completed task (deliberately — clients re-poll after timeouts); without an idempotency guard you would append a new terminal row on every call.

Pass the actual terminal status through (`wait_task` already knows it from `info.Status`; `get_task` reads `info.Status` at `tools.go:619`):

```go
	if childSess := sessionIDFromMarker(progress.FinalOutput, info.Output, string(info.Result)); childSess != "" {
		t.recordTerminalChild(info.TaskID, childSess, info.Status)
	}
```

```go
// recordTerminalChild appends a terminal journal record for a finished
// delegation (status ∈ {completed, failed, cancelled}), carrying the child
// session link.
//   - status is forwarded verbatim from agentsdk.TaskInfo.Status; the Recent
//     dedup rule only acts on Terminal=true presence, so failure/cancellation
//     reasons survive into the journal for downstream readers.
//   - Idempotent: if the most recent journal record for taskID is already a
//     Terminal=true row with the same (child_session_id, status), return
//     without appending. Without this, repeated get_task/wait_task polls —
//     the normal client pattern — would append a new row each call, and
//     Recent's dedup (which hides only the NON-terminal delegation row) would
//     leave every duplicate terminal row visible.
//   - Requires a prior delegation-time record (carrying ChildAgentID, Tool,
//     TargetID, Skill). If LatestByTaskID returns nothing — e.g. the journal
//     was rotated between delegation and result — we have nothing to enrich
//     and return without writing. The parent-link reverse path degrades for
//     that one task to "no journal evidence" (the slave's sidecar still
//     carries the link in the forward direction).
//   - Best-effort: append failure is logged, not fatal.
func (t *Tools) recordTerminalChild(taskID, childSessionID, status string) {
	if t.taskJournal == nil || taskID == "" || childSessionID == "" {
		return
	}
	rec, ok := t.taskJournal.LatestByTaskID(taskID)
	if !ok {
		return
	}
	if rec.Terminal && rec.ChildSessionID == childSessionID && rec.Status == status {
		return // already recorded this terminal state; idempotent no-op
	}
	rec.ChildSessionID = childSessionID
	rec.Terminal = true
	if status != "" {
		rec.Status = status
	}
	if err := t.taskJournal.Append(rec); err != nil {
		t.logHelperErr("driver_journal", "record_terminal_child", err)
	}
}
```

(`logHelperErr` already exists on `Tools` for degraded journal errors — `tools.go:538`.)

**Status transitions:** if `wait_task` first sees `failed` then a later `get_task` re-runs and `info.Status` is still `failed`, the idempotency check matches and no second row is written. If status genuinely changes between calls (shouldn't happen for terminal states in agentserver, but defensive), a new row is appended — preserving the audit trail. The dedup rule in `Recent` (Task 8 step 1) still picks the newest terminal row.

- [ ] **Step 3: Test**

`task_journal_test.go` — four cases:
1. Append a delegation record (with `ChildAgentID="slave-2"`) then a terminal record for the same task_id (with `ChildSessionID="child-sess"`, `Status="completed"`); `Recent` returns **one** row (the terminal) with both `child_session_id=child-sess` and `child_agent_id=slave-2`. `LatestByTaskID` returns the terminal (newest).
2. **Regression for existing semantics**: two non-terminal `resume_task` rows for the same task_id (no terminal record) — `Recent` returns **both**, newest-first. This pins down the "dedup only when terminal exists" rule and prevents the change from breaking `TestListDriverTasksFiltersTaskID`.
3. A mix: terminal task_id `A` (one delegation + one terminal) plus task_id `B` (two non-terminal `resume_task` records) — `Recent` returns three rows (1 for A, 2 for B), newest-first.
4. **Status fidelity**: terminal record with `Status="failed"` is preserved; `Recent` returns it with `failed` (not silently rewritten to `completed`).

`tools_test.go` — four cases:
- A `get_task` whose result carries `{"session_id":"child-sess"}` and `info.Status="completed"` calls `recordTerminalChild`, producing a terminal record with `child_session_id=child-sess`, `child_agent_id=slave-2`, `status=completed`.
- A `wait_task` that observes `info.Status="failed"` with a child session marker writes a terminal row with `status=failed` (not `completed`).
- **Idempotency**: calling `get_task` three times on the same completed task with the same marker results in **exactly one** terminal row in the journal (verify via raw line count, not just `Recent`).
- **Status change between polls** (defensive): if a hypothetical second poll reports `info.Status="cancelled"` after a `failed` row exists, a second terminal row is appended, and `Recent` returns the newest (`cancelled`). This documents the chosen behavior.

`contract_tools_test.go` (or `tools_test.go` if shared fixtures are easier): assert `submit_contract_task` direct-match branch persists the chosen target's shortID into `delegatedTaskRecord.ChildAgentID` (and the eventual terminal record).

`tools_test.go`: assert `resume_task` reads the prior `submit_task` journal record via `LatestByTaskID`, copies its `ChildAgentID` into the new delegation record, and the terminal record for the resumed task ends up with both `child_agent_id` (recovered from prior record) and `child_session_id` (from result marker).

- [ ] **Step 4: Build + test + race**

Run: `go build ./... && go test ./internal/driver/ -run 'TaskJournal|GetTask' -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driver/task_journal.go internal/driver/task_journal_test.go internal/driver/tools.go internal/driver/tools_test.go internal/driver/contract_tools.go internal/driver/contract_tools_test.go
git commit -m "feat(driver): append terminal child-link record to task journal (#24 P2)"
```

---

## Task 9: Launcher + config wiring (resolve `codex_home`, order `New` past registration)

**Files:**
- Modify: `cmd/slave-agent/main.go` (non-daemon run: move `agentbackend.New` past `EnsureRegistered`; resolve codex_home)
- Modify: `cmd/driver-agent/main.go` (`newAgentBackend`: resolve codex_home from persisted ShortID)
- Modify: `deploy/linux/driver/config.yaml.template`, `deploy/linux/slave/config.yaml.template`, `dev/configs/*.example.yaml`

- [ ] **Step 1: Shared resolver (primitive args, no config-type coupling)**

Create **`pkg/agentbackend/codexhome.go`** (low-level; both driver and slave already import `pkg/agentbackend`, so neither slave→driver nor duplicate impl) and `pkg/agentbackend/codexhome_test.go` covering: explicit `codexHome` wins; `shortID==""` returns `""`; `loomHome` arg overrides `LOOM_HOME` env which overrides `$HOME/.cache/multi-agent`; final path is `<base>/<shortID>/.codex`; unresolvable home returns `""`.

```go
// ResolveCodexHome resolves the per-agent codex data dir from deploy inputs.
// Order: codexHome (explicit) → <loomStateDir>/<shortID>/.codex when shortID
// is known → "" (caller falls back to $HOME/.codex via EffectiveCodexHome).
// loomStateDir = loomHome → $LOOM_HOME env → $HOME/.cache/multi-agent.
func ResolveCodexHome(codexHome, loomHome, shortID string) string {
	if codexHome != "" {
		return codexHome
	}
	if shortID == "" {
		return ""
	}
	base := loomHome
	if base == "" {
		base = os.Getenv("LOOM_HOME")
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".cache", "multi-agent")
	}
	return filepath.Join(base, shortID, ".codex")
}
```

Both driver (`cfg.Agent.CodexHome`/`LoomHome` + `cfg.Credentials.ShortID`) and slave (same fields on its config type) call `agentbackend.ResolveCodexHome(cfg.Agent.CodexHome, cfg.Agent.LoomHome, cfg.Credentials.ShortID)` — no driver/slave package coupling, no duplicate logic.

- [ ] **Step 2: Driver**

`newAgentBackend` (driver main.go:275): set `CodexHome: agentbackend.ResolveCodexHome(cfg.Agent.CodexHome, cfg.Agent.LoomHome, cfg.Credentials.ShortID)` on the `agentbackend.Config`, AND set `cfg.Agent.CodexHome` to the same value so serve-mcp's `Tools` (Task 6) reads it. Driver reads persisted `cfg.Credentials.ShortID` (no `EnsureRegistered` to reorder; empty ⇒ warn + fallback `$HOME/.codex` via `EffectiveCodexHome`).

- [ ] **Step 3: Slave (non-daemon run)**

`cmd/slave-agent/main.go`: move `agentbackend.New` **after** `tn.EnsureRegistered(ctx)` (currently `:188` before `:213`), so `cfg.Credentials.ShortID` is populated. Set `cfg.Agent.CodexHome = agentbackend.ResolveCodexHome(cfg.Agent.CodexHome, cfg.Agent.LoomHome, cfg.Credentials.ShortID)` before `New`. (Task 5's `serve-daemon` uses the same helper.)

- [ ] **Step 4: Deploy templates**

Add `codex_home:` (commented example) and `loom_home:` to `deploy/linux/driver/config.yaml.template`, `deploy/linux/slave/config.yaml.template`, `deploy/windows/{driver,slave}/config.yaml.template`, and `dev/configs/{driver,slave-a,slave-b}.example.yaml`.

- [ ] **Step 5: Build + test + race**

Run: `go build ./... && go test ./... -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/codexhome.go pkg/agentbackend/codexhome_test.go cmd/slave-agent/main.go cmd/driver-agent/main.go internal/driver/*.go internal/config/*.go deploy/ dev/configs/
git commit -m "feat(launcher): wire codex_home + order New past registration (#24 P2)"
```

---

## Task 10: Whole-repo regression + gofmt + Windows verification

- [ ] **Step 1: Linux race + vet**

Run: `go build ./... && go test ./... -race -count=1 && go vet ./...`
Expected: PASS, vet clean.

- [ ] **Step 2: gofmt**

Run: `gofmt -w . && git diff --exit-code` (commit formatting-only if any).

- [ ] **Step 3: Windows cross-compile (mandatory; not optional)**

The launcher / `codex_home` resolution touched in Task 9 calls `os.UserHomeDir` and `filepath.Join`, both of which behave differently on Windows (`%USERPROFILE%` vs `$HOME`, backslash separators). Linux `go test` does NOT exercise these — past PRs have broken Windows here. Verify before requesting review:

```bash
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
```

Expected: both pass with no output.

- [ ] **Step 4: Windows runtime smoke (mandatory if SSH access available)**

The cross-compile catches type/import bugs but not runtime path bugs. Run the touched unit tests on a real Windows box:

```bash
ssh Administrator@9.0.16.110 'cd C:\path\to\multi-agent && go test ./pkg/agentbackend/... ./internal/driver/... ./internal/poller/... ./internal/orchestrator/... ./internal/orchestration/... -count=1'
```

(If the Windows host is unavailable, document that here and request the human reviewer run the smoke before merge — but do NOT silently skip.)

Expected: PASS. Failure modes to watch for: `os.UserHomeDir` returning empty (no `$HOME` fallback hit), backslash-vs-forward-slash mismatches in `codex_home` keys, sidecar paths with mixed separators.

- [ ] **Step 5: Capture Windows result**

Add the Windows command output (or the documented unavailability + reviewer hand-off) to the PR description. Without it, P2 is not review-ready.

---

## Acceptance for P2

- Codex executor writes `<CODEX_HOME>/loom-meta/current` on `thread.started` (Run + RunResume); RunResume still writes no sidecar.
- Driver `submit_task` (default skill `fanout` AND `chat`/`chat_resume`), `submit_contract_task`, `resume_task` stamp `<loom_origin agent name session/>` into `DelegateTaskRequest.SystemContext`, reading the marker + `ShortID`/`DisplayName`.
- The master orchestrator (`internal/orchestrator/fanout.go`, `internal/orchestration/driver_runner.go`) merges the outer task's `SystemContext` into each fanout child's `DelegateTaskRequest.SystemContext`, so slave chat children dispatched from default `submit_task` inherit the parent link end-to-end.
- Slave `poller` parses the marker into `Task.Parent*` and strips it; the resulting codex exec session's loom-meta sidecar (P1) carries the parent link.
- Slave `serve-daemon` registers with the observer (`ShortID` populated); slave codex sessions are listable by Commander.
- `RegisterPayload`/`DaemonInfo`/`SessionRow` carry `ShortID`/`OwnerAgentID`/`ParentAgentID`/`ParentDisplayName`.
- Driver `driver-tasks.jsonl` records `child_session_id` + `child_agent_id` on **all three** delegation paths (`submit_task` via `resolveTarget`, `submit_contract_task` via the widened `selectTarget` return, `resume_task` via `LatestByTaskID` recovery).
- `TaskJournal.Recent` dedup is **scoped**: only the delegation-time record is hidden when a terminal record exists for the same `task_id`; multiple non-terminal records for the same `task_id` (e.g. several `resume_task` rows) are preserved (regression coverage required).
- Launcher resolves per-agent `codex_home` via the new shared `pkg/agentbackend.ResolveCodexHome` helper (slave `New` moved past `EnsureRegistered`; driver reads persisted ShortID); deploy templates document `codex_home`/`loom_home`.
- End-to-end test (fake SDK + planner emitting one chat child) confirms default-skill `submit_task` → fanout → slave chat carries a non-empty loom_origin all the way to the slave's `Task.Parent*` fields.
- `go test ./... -race` green.

## Out of scope

- **P3 — Commander nesting (frontend-only):** frontend cross-daemon `buildSessionNodes` keyed on `(owner_agent_id, session_id)`; `remote`/`parent offline` badges; Playwright. **No observer-side parent index** (spec §8 decision); P2 ships the `SessionRow` fields and observer transparently passes them through. Separate plan.
- Concurrency-perfect parent attribution (single `current` marker; last-writer-wins) — accepted caveat.

## Implementation notes

- **Branch:** these plan docs live on `commander-parent-link-p2p3` (planning only). **Implement P2 on its own branch** (e.g. `commander-parent-link-p2`) off `origin/master`; P3 separately. Don't bundle P2+P3 into one PR.
- **Windows:** P2 changes launcher/config/templates and `codex_home` resolution. `go test ./... -race` on Linux does **not** cover real Windows `os.UserHomeDir`/path behavior. Add a Windows CI job (or a `GOOS=windows go build` + a path-resolution unit test with a fake home) before merging P2.
