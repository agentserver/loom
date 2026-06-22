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
- `pkg/agentbackend/codex/loommeta.go` — add `writeCurrentSession`/`readCurrentSession` (the `current` marker; not a sidecar).
- `pkg/agentbackend/codex/loommeta_test.go` — marker tests.
- `pkg/agentbackend/codex/executor.go` — write `current` marker on `thread.started` (Run **and** RunResume).
- `internal/commander/protocol.go` — `RegisterPayload` += `ShortID`.
- `internal/commanderhub/registry.go` + `hub.go` + `tree.go` — carry `ShortID`; `SessionRow` += `OwnerAgentID`/`ParentAgentID`/`ParentDisplayName`; `agent_id → daemonConn` map; `sessionRowFromBackend` copies parent fields.
- `cmd/driver-agent/main.go` — populate `ShortID` in register; resolve `codex_home` from persisted ShortID; pass `cfg.Agent.CodexHome`.
- `cmd/slave-agent/main.go` — **new `serve-daemon` subcommand** (mirror driver); move `agentbackend.New` past `EnsureRegistered`; resolve `codex_home`; populate `ShortID`.
- `internal/driver/tools.go` ( + `contract_tools.go`/`slave_tools.go`/`register_mcp_tool.go`/`slave_file_tools.go`) — stamp `<loom_origin>` on delegation; reverse link into `driver-tasks.jsonl`.
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

Create `pkg/agentbackend/loomorigin.go`:

```go
package agentbackend

import (
	"encoding/xml"
	"strings"
)

// ParentLink is the origin tuple carried by the <loom_origin> marker.
type ParentLink struct {
	SessionID   string
	AgentID     string
	DisplayName string
}

// BuildLoomOrigin renders a <loom_origin .../> marker line that carries the
// parent link through DelegateTaskRequest.SystemContext. Values are XML-escaped
// so a display name containing "</loom_origin>" cannot break the boundary.
// The marker is a single self-closing tag on its own line.
func BuildLoomOrigin(agentID, displayName, sessionID string) string {
	type tag struct {
		XMLName xml.Name `xml:"loom_origin"`
		Agent   string   `xml:"agent,attr"`
		Name    string   `xml:"name,attr"`
		Session string   `xml:"session,attr"`
	}
	var b strings.Builder
	b.WriteString("<loom_origin")
	writeAttr(&b, "agent", agentID)
	writeAttr(&b, "name", displayName)
	writeAttr(&b, "session", sessionID)
	b.WriteString(" />\n")
	_ = tag{} // keep xml import referenced for future structured form
	return b.String()
}

func writeAttr(b *strings.Builder, k, v string) {
	b.WriteByte(' ')
	b.WriteString(k)
	b.WriteString(`="`)
	xml.EscapeText(b, []byte(v))
	b.WriteByte('"')
}

// ParseLoomOrigin extracts the parent link from a SystemContext string and
// returns the context with the marker line removed. ok is false when no
// well-formed marker is present.
func ParseLoomOrigin(systemContext string) (ParentLink, string, bool) {
	start := strings.Index(systemContext, "<loom_origin")
	if start < 0 {
		return ParentLink{}, systemContext, false
	}
	end := strings.Index(systemContext[start:], "/>")
	if end < 0 {
		return ParentLink{}, systemContext, false
	}
	end += start + len("/>")
	marker := systemContext[start:end]
	var p ParentLink
	p.AgentID = attrVal(marker, "agent")
	p.DisplayName = attrVal(marker, "name")
	p.SessionID = attrVal(marker, "session")
	if p.SessionID == "" && p.AgentID == "" {
		return ParentLink{}, systemContext, false
	}
	// Remove the marker line (and its trailing newline) from the context.
	cleaned := systemContext[:start] + systemContext[end:]
	cleaned = strings.TrimPrefix(cleaned, "\n")
	// Collapse the gap left where the line was.
	cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	return p, cleaned, true
}

func attrVal(marker, key string) string {
	needle := key + `="`
	i := strings.Index(marker, needle)
	if i < 0 {
		return ""
	}
	i += len(needle)
	j := strings.IndexByte(marker[i:], '"')
	if j < 0 {
		return ""
	}
	// Unescape XML entities produced by xml.EscapeText.
	raw := marker[i : i+j]
	raw = strings.ReplaceAll(raw, "&lt;", "<")
	raw = strings.ReplaceAll(raw, "&gt;", ">")
	raw = strings.ReplaceAll(raw, "&quot;", `"`)
	raw = strings.ReplaceAll(raw, "&#39;", "'")
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	return raw
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/ -run LoomOrigin -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

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
	if got := readCurrentSession(base); got != "thread-now" {
		t.Fatalf("readCurrentSession = %q, want thread-now", got)
	}
}

func TestReadCurrentSessionMissing(t *testing.T) {
	if got := readCurrentSession(t.TempDir()); got != "" {
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
	if got := readCurrentSession(home); got != "thr-cur" {
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
	if got := readCurrentSession(home); got != "thr-res" {
		t.Fatalf("current marker = %q, want thr-res (RunResume updates marker)", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/agentbackend/codex/ -run 'CurrentSession|WritesCurrentSession' -v`
Expected: FAIL — `writeCurrentSession`/`readCurrentSession` undefined; marker not written.

- [ ] **Step 3: Add marker helpers to `loommeta.go`**

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

// readCurrentSession returns the thread id last written by writeCurrentSession,
// or "" if absent/unreadable.
func readCurrentSession(base string) string {
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

- [ ] **Step 4: Write the marker in the executor's thread.started block**

In `pkg/agentbackend/codex/executor.go`, the `thread.started` block (around line 202) currently writes the sidecar only when `newSession`. Add the marker write **outside** the `if newSession` (so RunResume also writes it):

```go
		if ev.Type == "thread.started" && sessionID == "" && ev.ThreadID != "" {
			sessionID = ev.ThreadID
			// current-session marker: written on BOTH Run and RunResume so
			// serve-mcp can learn the parent session during any turn. Best-effort.
			_ = writeCurrentSession(effectiveCodexHome(e.cfg, e.env), sessionID)
			if newSession {
				// ... existing sidecar write (unchanged) ...
			}
			continue
		}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/agentbackend/codex/ -run 'CurrentSession|WritesCurrentSession' -v -race`
Expected: PASS (Run writes marker; RunResume writes marker but no sidecar).

- [ ] **Step 6: Full codex package + race**

Run: `go test ./pkg/agentbackend/codex/ -race -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/agentbackend/codex/loommeta.go pkg/agentbackend/codex/loommeta_test.go pkg/agentbackend/codex/executor.go pkg/agentbackend/codex/executor_test.go
git commit -m "feat(codex): write current-session marker on thread.started (#24 P2)"
```

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

Add a `runServeDaemon` (mirror driver's): load config; require `proxy_token` + `observer.url`; resolve `codex_home` (see Task 8) and pass into `agentbackend.New`; build `commander.Handler{Backend: backend, WorkerMax: cfg.Daemon.WorkerMax}`; `commander.NewDaemon` with `RegisterPayload` including `ShortID: cfg.Credentials.ShortID`, `Kind: cfg.Agent.Kind`, `DisplayName: cfg.Discovery.DisplayName`; run until signal.

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

## Task 6: Driver stamps `<loom_origin>` on delegation

**Files:**
- Modify: `internal/driver/tools.go` (`submit_task`, ~line 511) + `contract_tools.go:100`, `slave_tools.go:150`, `register_mcp_tool.go:59`, `slave_file_tools.go:128/349/431`
- Test: `internal/driver/tools_test.go`

- [ ] **Step 1: Add a stamping helper on `Tools`**

In `internal/driver/tools.go`, add a helper that reads the current-session marker and the driver's own identity, returning the `<loom_origin>` marker to prepend to `SystemContext`:

```go
// loomOriginMarker returns the <loom_origin> marker for the current driver
// session, read from the current-session marker file written by the codex
// executor. Empty session when the marker is absent (no active turn).
func (t *Tools) loomOriginMarker() string {
	sess := readCurrentSessionForCfg(t.cfg) // resolves CODEX_HOME via the same effectiveCodexHome logic
	return agentbackend.BuildLoomOrigin(t.cfg.Credentials.ShortID, t.cfg.Discovery.DisplayName, sess)
}
```

`readCurrentSessionForCfg` resolves the effective codex home from `cfg.Agent` (codex_home → loom_home/short_id → $HOME/.codex) and calls `readCurrentSession`. Implement in a small helper in `internal/driver` (or call into a shared resolver). Keep it best-effort: absent marker → empty session → marker still carries agent+name (parent_session_id empty; slave sidecar gets empty parent session).

- [ ] **Step 2: Stamp `SystemContext` in `submit_task`**

In `submit_task` (tools.go ~line 511), where `DelegateTaskRequest` is built, prepend the marker to `SystemContext`:

```go
	systemContext := args.SystemContext
	if m := s.t.loomOriginMarker(); m != "" {
		systemContext = m + systemContext
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:      targetID,
		Skill:         skill,
		Prompt:        finalPrompt,
		SystemContext: systemContext,
		TimeoutSeconds: timeout,
	})
```

Repeat the `SystemContext` stamp in the other delegation handlers (`contract_tools.go:100`, `slave_tools.go:150/279`, `register_mcp_tool.go:59`, `unregister_mcp_tool.go:54`, `slave_file_tools.go:128/349/431`). For tools that don't accept `SystemContext` (file tools), skip — they don't create codex exec sessions.

- [ ] **Step 3: Test**

`internal/driver/tools_test.go` — with a fake SDK + a temp CODEX_HOME + a written `current` marker, assert `submit_task`'s `DelegateTaskRequest.SystemContext` starts with `<loom_origin agent="<shortID>" name="<displayName>" session="<marker>" />`.

- [ ] **Step 4: Build + test + race**

Run: `go build ./... && go test ./internal/driver/ -run SubmitTask -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driver/tools.go internal/driver/contract_tools.go internal/driver/slave_tools.go internal/driver/register_mcp_tool.go internal/driver/tools_test.go
git commit -m "feat(driver): stamp <loom_origin> on task delegation (#24 P2)"
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

## Task 8: Reverse link — `driver-tasks.jsonl` records child session + agent

**Files:**
- Modify: `internal/driver/tools.go` (`delegatedTaskRecord`, `sessionIDFromMarker` callers ~line 619-636)
- Test: `internal/driver/tools_test.go`

- [ ] **Step 1: Extend the journal record**

`delegatedTaskRecord` (tools.go ~line 79) += `ChildSessionID string` + `ChildAgentID string`. The driver already extracts the child session via `sessionIDFromMarker` (tools.go:821) from the task result, and knows the child agent via `resolveTarget`'s `shortID` (tools.go:165-211). Populate both when the result marker arrives.

- [ ] **Step 2: Test**

Assert a delegated task whose result carries `{"session_id":"child-sess"}` and target shortID `slave-2` produces a journal record with `child_session_id=child-sess`, `child_agent_id=slave-2`.

- [ ] **Step 3: Build + test**

Run: `go build ./... && go test ./internal/driver/ -run TaskJournal -race -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/driver/tools.go internal/driver/tools_test.go
git commit -m "feat(driver): record child session+agent in task journal (#24 P2)"
```

---

## Task 9: Launcher + config wiring (resolve `codex_home`, order `New` past registration)

**Files:**
- Modify: `cmd/slave-agent/main.go` (non-daemon run: move `agentbackend.New` past `EnsureRegistered`; resolve codex_home)
- Modify: `cmd/driver-agent/main.go` (`newAgentBackend`: resolve codex_home from persisted ShortID)
- Modify: `deploy/linux/driver/config.yaml.template`, `deploy/linux/slave/config.yaml.template`, `dev/configs/*.example.yaml`

- [ ] **Step 1: Shared resolver**

Add a helper (in `internal/driver` or `internal/config`) `ResolveCodexHome(cfg) string`: `cfg.Agent.CodexHome` → `<loom_state_dir>/<short_id>/.codex` (loom_state_dir = `cfg.Agent.LoomHome` → `$LOOM_HOME` → `$HOME/.cache/multi-agent`) when `short_id` known → `""`.

- [ ] **Step 2: Driver**

`newAgentBackend` (driver main.go:275): set `CodexHome: ResolveCodexHome(cfg)` on the `agentbackend.Config`. Driver reads persisted `cfg.Credentials.ShortID` (no `EnsureRegistered` to reorder; empty ⇒ warn + fallback `$HOME/.codex`).

- [ ] **Step 3: Slave (non-daemon run)**

`cmd/slave-agent/main.go`: move `agentbackend.New` **after** `tn.EnsureRegistered(ctx)` (currently `:188` before `:213`), so `cfg.Credentials.ShortID` is populated. Set `CodexHome: ResolveCodexHome(cfg)` before `New`. (Task 5's `serve-daemon` already resolves; share the helper.)

- [ ] **Step 4: Deploy templates**

Add `codex_home:` (commented example) and `loom_home:` to `deploy/linux/driver/config.yaml.template`, `deploy/linux/slave/config.yaml.template`, `deploy/windows/{driver,slave}/config.yaml.template`, and `dev/configs/{driver,slave-a,slave-b}.example.yaml`.

- [ ] **Step 5: Build + test + race**

Run: `go build ./... && go test ./... -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/slave-agent/main.go cmd/driver-agent/main.go internal/driver/*.go internal/config/*.go deploy/ dev/configs/
git commit -m "feat(launcher): wire codex_home + order New past registration (#24 P2)"
```

---

## Task 10: Whole-repo regression + gofmt

- [ ] **Step 1**

Run: `go build ./... && go test ./... -race -count=1 && go vet ./...`
Expected: PASS, vet clean.

- [ ] **Step 2**

Run: `gofmt -w . && git diff --exit-code` (commit formatting-only if any).

---

## Acceptance for P2

- Codex executor writes `<CODEX_HOME>/loom-meta/current` on `thread.started` (Run + RunResume); RunResume still writes no sidecar.
- Driver `submit_task` (and other delegation handlers) stamp `<loom_origin agent name session/>` into `DelegateTaskRequest.SystemContext`, reading the marker + `ShortID`/`DisplayName`.
- Slave `poller` parses the marker into `Task.Parent*` and strips it; the resulting codex exec session's loom-meta sidecar (P1) carries the parent link.
- Slave `serve-daemon` registers with the observer (`ShortID` populated); slave codex sessions are listable by Commander.
- `RegisterPayload`/`DaemonInfo`/`SessionRow` carry `ShortID`/`OwnerAgentID`/`ParentAgentID`/`ParentDisplayName`.
- Driver `driver-tasks.jsonl` records `child_session_id` + `child_agent_id`.
- Launcher resolves per-agent `codex_home` (slave `New` moved past `EnsureRegistered`; driver reads persisted ShortID); deploy templates document `codex_home`/`loom_home`.
- `go test ./... -race` green.

## Out of scope

- **P3 — Commander nesting:** observer global `(owner_agent_id, session_id)` parent index; frontend cross-daemon `buildSessionNodes`; `remote`/`parent offline` badges. Separate plan.
- Concurrency-perfect parent attribution (single `current` marker; last-writer-wins) — accepted caveat.
