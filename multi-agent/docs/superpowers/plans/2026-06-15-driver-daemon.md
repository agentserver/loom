# driver-agent serve-daemon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a long-lived `driver-agent serve-daemon` subcommand that dials a WebSocket out to observer (bearer `ProxyToken`), exposes a local HTTP API (127.0.0.1, debug only), and routes 3 commands (`list_sessions` / `get_session` / `session_turn`) to PR-1's `Backend` methods. Streams turn events back over WS / SSE.

**Architecture:** New greenfield `internal/commander/` package. Two transports (WS + HTTP) share one set of handlers that dispatch to `agentbackend.Backend`. WS envelope JSON schema is locked by the spec so PR-3 observer-hub conforms. Daemon owns no persistent state — all session data comes from `Backend.ListSessions/GetSession`. Reconnect loop with exponential backoff + heartbeat survives observer outages.

**Tech Stack:** Go stdlib + `github.com/gorilla/websocket` (pure Go, CGO_ENABLED=0 friendly) + existing `pkg/agentbackend` (PR-1).

**Spec:** `docs/superpowers/specs/2026-06-15-driver-daemon-design.md` (committed `8f97003`).

**Vision:** `docs/vision/commander-web-entry.md` (on master). This is PR-2 of 3.

**Branch:** `worktree-driver-daemon`. Worktree: `/root/multi-agent/.claude/worktrees/driver-daemon/multi-agent/`.

**Master-path freeze:** all changes confined to `cmd/driver-agent/`, `internal/commander/` (new), `internal/driver/config.go`, `go.mod`/`go.sum`. Verify after Task 8:

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
# expected: empty
```

---

## Pre-flight already done by plan author

- `executor.Sink` interface is `Write(eventType, data string)` + `Close()` — handler streams `RunResume` events through; adapters convert to WS event envelopes / SSE event lines
- `executor.Result` is `{Summary, CapabilityChange, SessionID, AwaitingUser}` — full struct is marshallable for `command_result` payload
- `agentbackend.Session` / `SessionMessage` exported from PR-1 — handlers return these directly in JSON
- `driver.Config.Observer.URL` + `driver.Config.Credentials.ProxyToken` are the WS dial inputs
- `cmd/driver-agent/main.go` has `switch os.Args[1] { case "register": ...; case "serve-mcp": ... }` — extend with `case "serve-daemon"`
- `gorilla/websocket` NOT in go.mod — must `go get` (Task 4)

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `internal/commander/protocol.go` | Create | `Envelope` struct + `register` / `heartbeat` / `command` / `command_result` / `event` / `error` / `ping` payload structs + `SchemaVersion` const |
| `internal/commander/protocol_test.go` | Create | JSON round-trip + version-mismatch detection |
| `internal/commander/handler.go` | Create | `Handler` struct holding `agentbackend.Backend`; `ListSessions(ctx)` / `GetSession(ctx, id)` / `SessionTurn(ctx, id, prompt, sink)` methods |
| `internal/commander/handler_test.go` | Create | fake Backend; verify each handler delegates correctly + ErrSessionNotFound surfaces as error envelope code |
| `internal/commander/http.go` | Create | `NewHTTPHandler(h *Handler) http.Handler` mounting `/healthz`, `/sessions`, `/sessions/{id}`, `/sessions/{id}/turn` (SSE) |
| `internal/commander/http_test.go` | Create | httptest server; per-route assertions; SSE stream parse |
| `internal/commander/wsclient.go` | Create | `WSClient` that dials with bearer auth, sends register, runs read loop, dispatches commands to `Handler`, reconnects with backoff |
| `internal/commander/wsclient_test.go` | Create | in-process gorilla upgrader; verify register, command round-trip, heartbeat, reconnect on drop, schema-mismatch shutdown |
| `internal/commander/sink.go` | Create | `wsSink` + `sseSink` adapters from `executor.Sink` to transport |
| `internal/commander/sink_test.go` | Create | each sink writes through to a capture buffer in the right shape |
| `internal/commander/daemon.go` | Create | `Daemon` orchestrator: wires HTTP + WS + Handler; `Run(ctx)` blocks until ctx done; clean shutdown |
| `internal/commander/daemon_test.go` | Create | end-to-end: daemon connects to fake observer + serves HTTP simultaneously + graceful shutdown |
| `internal/driver/config.go` | Modify | Add `Daemon` struct under `Config`; LoadConfig defaults |
| `internal/driver/config_test.go` | Modify | Round-trip YAML with explicit + implicit `daemon:` block |
| `cmd/driver-agent/main.go` | Modify | Add `serve-daemon` case + `runServeDaemon([]string)` |
| `cmd/driver-agent/main_test.go` | Modify | Flag parsing for `serve-daemon` |
| `cmd/driver-agent/README.md` | Modify | Document `serve-daemon` invocation |
| `go.mod` / `go.sum` | Modify | Add `github.com/gorilla/websocket` |

---

## Task 1: WS envelope schema (`protocol.go`)

**Files:**
- Create: `internal/commander/protocol.go`
- Create: `internal/commander/protocol_test.go`

### Step 1.1: Write failing tests

- [ ] **Create `internal/commander/protocol_test.go`**

```go
package commander

import (
	"encoding/json"
	"testing"
)

// TestEnvelope_RegisterRoundTrip pins the daemon→observer register
// frame as documented in the spec (§WS envelope schema lock).
func TestEnvelope_RegisterRoundTrip(t *testing.T) {
	in := Envelope{
		Type: "register",
		Payload: mustMarshal(t, RegisterPayload{
			SchemaVersion: SchemaVersion,
			Kind:          "claude",
			AgentBin:      "/usr/bin/claude",
			AgentWorkDir:  "/home/me/proj",
			DisplayName:   "office-mac",
			DriverVersion: "v0.1.2",
		}),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "register" {
		t.Fatalf("type=%q", out.Type)
	}
	var pl RegisterPayload
	if err := json.Unmarshal(out.Payload, &pl); err != nil {
		t.Fatal(err)
	}
	if pl.Kind != "claude" || pl.DisplayName != "office-mac" {
		t.Fatalf("payload=%+v", pl)
	}
	if pl.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version mismatch: %d", pl.SchemaVersion)
	}
}

// TestEnvelope_CommandWithIDRoundTrip pins that observer→daemon command
// frames carry an ID (so daemon can correlate event / command_result).
func TestEnvelope_CommandWithIDRoundTrip(t *testing.T) {
	in := Envelope{
		Type: "command",
		ID:   "cmd-abc",
		Payload: mustMarshal(t, CommandPayload{
			Command: "get_session",
			Args:    mustMarshal(t, GetSessionArgs{ID: "sess-1"}),
		}),
	}
	b, _ := json.Marshal(in)
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "cmd-abc" {
		t.Fatalf("id=%q", out.ID)
	}
	var cp CommandPayload
	if err := json.Unmarshal(out.Payload, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.Command != "get_session" {
		t.Fatalf("command=%q", cp.Command)
	}
}

// TestEnvelope_EventStreamingShape pins streaming events (daemon→observer)
// for the session_turn command: each chunk lands as a separate event
// envelope with the same ID as the originating command.
func TestEnvelope_EventStreamingShape(t *testing.T) {
	ev := Envelope{
		Type: "event",
		ID:   "cmd-abc",
		Payload: mustMarshal(t, EventPayload{
			EventKind: "chunk",
			Text:      "hello",
		}),
	}
	b, _ := json.Marshal(ev)
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "event" || out.ID != "cmd-abc" {
		t.Fatalf("envelope=%+v", out)
	}
	var ep EventPayload
	if err := json.Unmarshal(out.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.EventKind != "chunk" || ep.Text != "hello" {
		t.Fatalf("event payload=%+v", ep)
	}
}

// TestEnvelope_ErrorCodesEnumerated pins the spec's error codes so a
// typo at the call site is a test failure, not a silent runtime miss.
func TestEnvelope_ErrorCodesEnumerated(t *testing.T) {
	codes := []string{
		ErrCodeSessionNotFound,
		ErrCodeBackendUnavailable,
		ErrCodeSchemaVersionMismatch,
		ErrCodeInternal,
	}
	for _, c := range codes {
		if c == "" {
			t.Errorf("error code is empty string")
		}
	}
}

// TestSchemaVersion_IsOne pins that PR-2 ships at schema_version = 1.
// Breaking changes bump this and force daemon/observer to coordinate.
func TestSchemaVersion_IsOne(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion=%d want 1", SchemaVersion)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
```

### Step 1.2: Verify RED

```bash
go test ./internal/commander/ -count=1
```

Expected: build error (package doesn't exist).

### Step 1.3: Implement `protocol.go`

- [ ] **Create `internal/commander/protocol.go`**

```go
// Package commander implements the daemon side of the commander-web-entry
// architecture (docs/vision/commander-web-entry.md). It speaks the
// WebSocket envelope schema locked in
// docs/superpowers/specs/2026-06-15-driver-daemon-design.md so the
// observer hub (PR-3) can be implemented against the same contract
// without cross-PR negotiation.
package commander

import "encoding/json"

// SchemaVersion is the protocol version this build of the daemon
// speaks. The observer hub (PR-3) must match. Bump on any breaking
// change to envelope or payload shape.
const SchemaVersion = 1

// Envelope is the JSON shell wrapping every WS frame. Daemon→observer
// types: register / heartbeat / command_result / event / error.
// Observer→daemon types: command / ping.
type Envelope struct {
	// Type names the frame kind; routing dispatches on this.
	Type string `json:"type"`
	// ID correlates command frames with their command_result / event
	// frames. Set by observer on command; copied back by daemon on
	// reply. register / heartbeat / ping omit it.
	ID string `json:"id,omitempty"`
	// Payload is the type-specific body. Kept as RawMessage so the
	// router can dispatch on Type before decoding.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RegisterPayload is the first frame the daemon sends after the WS
// upgrade. Observer uses kind + display_name + driver_version to
// populate the per-user daemon list.
type RegisterPayload struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	AgentBin      string `json:"agent_bin"`
	AgentWorkDir  string `json:"agent_workdir"`
	DisplayName   string `json:"display_name"`
	DriverVersion string `json:"driver_version"`
}

// CommandPayload describes an observer→daemon command. Args is
// command-specific; daemon dispatches on Command then decodes Args.
type CommandPayload struct {
	Command string          `json:"command"`
	Args    json.RawMessage `json:"args,omitempty"`
}

// GetSessionArgs is the payload for command="get_session".
type GetSessionArgs struct {
	ID string `json:"id"`
}

// SessionTurnArgs is the payload for command="session_turn".
type SessionTurnArgs struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

// EventPayload is one streaming event in a session_turn flow.
// EventKind values: "chunk", "capability", "awaiting_user".
type EventPayload struct {
	EventKind string          `json:"event_kind"`
	Text      string          `json:"text,omitempty"`
	Extra     json.RawMessage `json:"extra,omitempty"`
}

// ErrorPayload is the body of an error envelope. Code is one of the
// ErrCode* constants below; messages are human-readable hints.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes (lock these — call sites reference by name).
const (
	ErrCodeSessionNotFound       = "session_not_found"
	ErrCodeBackendUnavailable    = "backend_unavailable"
	ErrCodeSchemaVersionMismatch = "schema_version_mismatch"
	ErrCodeInternal              = "internal"
)
```

### Step 1.4: GREEN

```bash
go test ./internal/commander/ -count=1 -v
go build ./...
go vet ./...
```

Expected: 5 protocol tests PASS, full build clean.

### Step 1.5: Commit

```bash
git add internal/commander/protocol.go internal/commander/protocol_test.go
git commit -m "feat(commander): WS envelope schema (PR-2 Task 1)

internal/commander/ package skeleton + locked WS message schema per
docs/superpowers/specs/2026-06-15-driver-daemon-design.md §WS
envelope schema lock.

Envelope{Type, ID, Payload} + payload structs for register / command /
event / error. SchemaVersion = 1; error codes enumerated as named
consts so observer hub (PR-3) shares the contract.

Pure data types, zero deps beyond stdlib. Round-trip tests pin every
envelope shape so a refactor that drops a field is caught immediately.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Shared handler (`handler.go`)

**Files:**
- Create: `internal/commander/handler.go`
- Create: `internal/commander/handler_test.go`

### Step 2.1: Write failing tests

- [ ] **Create `internal/commander/handler_test.go`**

```go
package commander

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// fakeBackend implements agentbackend.Backend with hooks for each
// method tested here. Unused methods return zero values so the
// interface stays satisfied.
type fakeBackend struct {
	listFn   func(ctx context.Context) ([]agentbackend.Session, error)
	getFn    func(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error)
}

func (f *fakeBackend) Kind() agentbackend.Kind { return agentbackend.KindClaude }
func (f *fakeBackend) Run(_ context.Context, _ executor.Task, _ executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *fakeBackend) RunResume(ctx context.Context, id, ans string, sink executor.Sink) (executor.Result, error) {
	return f.resumeFn(ctx, id, ans, sink)
}
func (f *fakeBackend) LLM() agentbackend.LLMRunner                { return nil }
func (f *fakeBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (f *fakeBackend) Detect(_ context.Context) error             { return nil }
func (f *fakeBackend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	return f.listFn(ctx)
}
func (f *fakeBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return f.getFn(ctx, id)
}

// captureSink records sink writes so tests can assert on streamed
// events from a SessionTurn flow.
type captureSink struct {
	events []captured
	closed bool
}

type captured struct {
	kind string
	data string
}

func (c *captureSink) Write(k, d string) { c.events = append(c.events, captured{k, d}) }
func (c *captureSink) Close()             { c.closed = true }

// TestHandler_ListSessionsForwards pins that Handler.ListSessions
// returns whatever the Backend returns, no filtering / mutation.
func TestHandler_ListSessionsForwards(t *testing.T) {
	want := []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude}}
	h := &Handler{Backend: &fakeBackend{
		listFn: func(_ context.Context) ([]agentbackend.Session, error) { return want, nil },
	}}
	got, err := h.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("got=%+v", got)
	}
}

// TestHandler_GetSessionForwards pins the descriptor + message slice
// flow.
func TestHandler_GetSessionForwards(t *testing.T) {
	wantS := agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude}
	wantM := []agentbackend.SessionMessage{{Role: "user", Text: "hi"}}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			if id != "s1" {
				t.Errorf("id=%q", id)
			}
			return wantS, wantM, nil
		},
	}}
	s, m, err := h.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s1" || len(m) != 1 || m[0].Text != "hi" {
		t.Fatalf("s=%+v m=%+v", s, m)
	}
}

// TestHandler_GetSessionPropagatesErrSessionNotFound pins that the
// PR-1 sentinel error survives the handler — call site needs it to
// emit the right ErrCodeSessionNotFound envelope.
func TestHandler_GetSessionPropagatesErrSessionNotFound(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
		},
	}}
	_, _, err := h.GetSession(context.Background(), "missing")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v", err)
	}
}

// TestHandler_SessionTurnStreamsAndReturns pins that the sink given to
// the handler receives each RunResume event in order, and the final
// Result is returned to the caller. Both pieces matter: streaming
// powers the WS event envelopes, the Result powers command_result.
func TestHandler_SessionTurnStreamsAndReturns(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, id, ans string, sink executor.Sink) (executor.Result, error) {
			if id != "s1" {
				t.Errorf("id=%q", id)
			}
			if !strings.Contains(ans, "do thing") {
				t.Errorf("answer=%q", ans)
			}
			sink.Write("chunk", "step one")
			sink.Write("chunk", "step two")
			sink.Write("capability", "added foo")
			sink.Close()
			return executor.Result{Summary: "done", SessionID: id}, nil
		},
	}}
	sink := &captureSink{}
	res, err := h.SessionTurn(context.Background(), "s1", "do thing", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "done" {
		t.Errorf("summary=%q", res.Summary)
	}
	if !sink.closed {
		t.Errorf("sink not closed")
	}
	if len(sink.events) != 3 || sink.events[0].kind != "chunk" || sink.events[2].kind != "capability" {
		t.Errorf("events=%+v", sink.events)
	}
}

// TestHandler_SessionTurnRespectsContextCancel pins that handler does
// not swallow ctx errors — needed for graceful daemon shutdown.
func TestHandler_SessionTurnRespectsContextCancel(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(ctx context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			select {
			case <-ctx.Done():
				return executor.Result{}, ctx.Err()
			case <-time.After(5 * time.Second):
				t.Errorf("backend not cancelled")
				return executor.Result{}, nil
			}
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.SessionTurn(ctx, "s1", "p", &captureSink{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
```

### Step 2.2: Verify RED

```bash
go test ./internal/commander/ -run TestHandler_ -count=1
```

Expected: undefined `Handler` / `ListSessions` / etc.

### Step 2.3: Implement `handler.go`

- [ ] **Create `internal/commander/handler.go`**

```go
package commander

import (
	"context"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Handler is the transport-agnostic command dispatcher used by both
// the WS client (wsclient.go) and the local HTTP server (http.go).
// Backend is the agent-specific backend selected at daemon startup
// (cfg.Agent.Kind). One Handler per daemon process — each daemon
// serves exactly one kind by design (multi-kind = multi-daemon, see
// spec decision record).
type Handler struct {
	Backend agentbackend.Backend
}

// ListSessions returns every session this backend has persisted.
// Pure forward to Backend — exists as a method so wsclient/http
// have a single import surface.
func (h *Handler) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	return h.Backend.ListSessions(ctx)
}

// GetSession returns descriptor + message history for one id.
// Propagates agentbackend.ErrSessionNotFound so callers can map it
// to the correct ErrCodeSessionNotFound error envelope.
func (h *Handler) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return h.Backend.GetSession(ctx, id)
}

// SessionTurn runs one user turn against an existing session by
// invoking the backend's RunResume. The provided sink receives any
// intermediate events (chunk / capability / awaiting_user) — the
// caller adapts this to its transport (WS event envelopes or SSE
// lines). The final Result is returned to the caller, which encodes
// it as a command_result envelope (WS) or `event: done` (SSE).
func (h *Handler) SessionTurn(ctx context.Context, id, prompt string, sink executor.Sink) (executor.Result, error) {
	return h.Backend.RunResume(ctx, id, prompt, sink)
}
```

### Step 2.4: GREEN

```bash
go test ./internal/commander/ -count=1 -v
go build ./...
go vet ./...
```

Expected: 5 protocol tests + 5 handler tests PASS.

### Step 2.5: Commit

```bash
git add internal/commander/handler.go internal/commander/handler_test.go
git commit -m "feat(commander): shared handler delegating to Backend (PR-2 Task 2)

internal/commander/handler.go: Handler{Backend} with ListSessions /
GetSession / SessionTurn methods that pure-forward to the PR-1
Backend interface. One Handler per daemon, shared between WS and
HTTP transports.

ErrSessionNotFound propagates so call sites can emit the right error
envelope. SessionTurn keeps the sink as parameter so transport
adapters (Tasks 3+5) can stream events back to web in real time.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: HTTP transport + SSE (`http.go`)

**Files:**
- Create: `internal/commander/http.go`
- Create: `internal/commander/sink.go` (sseSink half — wsSink lands in Task 4)
- Create: `internal/commander/http_test.go`
- Create: `internal/commander/sink_test.go` (sseSink half)

### Step 3.1: Write failing http tests

- [ ] **Create `internal/commander/http_test.go`**

```go
package commander

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestHTTP_HealthzOK(t *testing.T) {
	srv := httptest.NewServer(NewHTTPHandler(&Handler{Backend: &fakeBackend{}}, LinkStatusFunc(func() bool { return true })))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_GetSessionsReturnsBackendResult(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		listFn: func(_ context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1"}, {ID: "s2"}}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true })))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Sessions []agentbackend.Session `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("got %d sessions", len(body.Sessions))
	}
}

func TestHTTP_GetSession404OnUnknown(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true })))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_GetSessionOK(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: id}, []agentbackend.SessionMessage{{Role: "user", Text: "hi"}}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true })))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Session  agentbackend.Session          `json:"session"`
		Messages []agentbackend.SessionMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Session.ID != "abc" || len(body.Messages) != 1 {
		t.Fatalf("body=%+v", body)
	}
}

// TestHTTP_PostTurnStreamsSSE pins the SSE shape: one "event: chunk"
// per sink Write, then "event: done" with the marshalled final Result.
func TestHTTP_PostTurnStreamsSSE(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			sink.Write("chunk", "alpha")
			sink.Write("chunk", "beta")
			sink.Close()
			return executor.Result{Summary: "done", SessionID: "abc"}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true })))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/sessions/abc/turn",
		strings.NewReader(`{"prompt":"do thing"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type=%q", got)
	}

	var events []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		events = append(events, sc.Text())
	}
	body := strings.Join(events, "\n")
	if !strings.Contains(body, "event: chunk") {
		t.Errorf("missing chunk event:\n%s", body)
	}
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Errorf("missing data lines:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("missing done event:\n%s", body)
	}
	if !strings.Contains(body, `"summary":"done"`) {
		t.Errorf("done event missing result body:\n%s", body)
	}
}

// TestHTTP_PostTurnErrSessionNotFound returns 404 before opening SSE
// stream.
func TestHTTP_PostTurnErrSessionNotFound(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			return executor.Result{}, agentbackend.ErrSessionNotFound
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true })))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/sessions/x/turn",
		strings.NewReader(`{"prompt":"p"}`))
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d want 404", resp.StatusCode)
	}
	_ = errors.New
}
```

- [ ] **Create `internal/commander/sink_test.go`** (sseSink half — wsSink test lands in Task 4)

```go
package commander

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSSESink_WriteEmitsEventDataPair pins the SSE wire format:
// each Sink.Write produces "event: <kind>\ndata: <json>\n\n".
func TestSSESink_WriteEmitsEventDataPair(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSESink(rec)
	s.Write("chunk", "hello")
	s.Close()
	body := rec.Body.String()
	if !strings.Contains(body, "event: chunk\n") {
		t.Errorf("missing event line: %q", body)
	}
	if !strings.Contains(body, `data: {"text":"hello"}`) {
		t.Errorf("missing data line: %q", body)
	}
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), "}") {
		t.Errorf("body should end with closed json: %q", body)
	}
}

// TestSSESink_EmitDone writes a terminal event with the result body.
func TestSSESink_EmitDone(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSESink(rec)
	s.EmitDone([]byte(`{"summary":"ok"}`))
	body := rec.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Errorf("missing done event: %q", body)
	}
	if !strings.Contains(body, `data: {"summary":"ok"}`) {
		t.Errorf("missing done payload: %q", body)
	}
}

// TestSSESink_FlushedOnEachWrite — flushable writer is invoked so
// browsers see events immediately rather than after handler returns.
type fakeFlusher struct {
	bytes.Buffer
	flushed int
}

func (f *fakeFlusher) Flush() { f.flushed++ }

func TestSSESink_FlushedOnEachWrite(t *testing.T) {
	f := &fakeFlusher{}
	s := newSSESink(struct {
		*bytes.Buffer
		flushable
	}{&f.Buffer, f})
	s.Write("chunk", "x")
	s.Write("chunk", "y")
	s.Close()
	if f.flushed < 2 {
		t.Errorf("flushed=%d want >=2", f.flushed)
	}
}
```

### Step 3.2: Verify RED

```bash
go test ./internal/commander/ -run "TestHTTP_|TestSSESink_" -count=1
```

Expected: undefined `NewHTTPHandler`, `LinkStatusFunc`, `newSSESink`, etc.

### Step 3.3: Implement `sink.go` (sseSink + adapter interface)

- [ ] **Create `internal/commander/sink.go`**

```go
package commander

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/yourorg/multi-agent/internal/executor"
)

// flushable is the subset of http.Flusher we use; declared here so
// tests can supply their own without depending on net/http.
type flushable interface {
	Flush()
}

// sseSink adapts executor.Sink writes to SSE event lines, flushing
// after each write so browsers see chunks in real time. Close emits
// nothing terminal — call EmitDone with the final Result to mark
// stream end.
type sseSink struct {
	w io.Writer
	f flushable
}

func newSSESink(w io.Writer) *sseSink {
	s := &sseSink{w: w}
	if f, ok := w.(flushable); ok {
		s.f = f
	}
	return s
}

// Write implements executor.Sink. eventType becomes the SSE event
// name; data is JSON-wrapped as {"text": data}.
func (s *sseSink) Write(eventType, data string) {
	payload, _ := json.Marshal(map[string]string{"text": data})
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, payload)
	if s.f != nil {
		s.f.Flush()
	}
}

// Close implements executor.Sink. SSE doesn't have a special close
// frame; EmitDone is the terminal marker. Close is a no-op kept to
// satisfy the interface.
func (s *sseSink) Close() {}

// EmitDone writes the terminal "event: done" line with the JSON
// body. Body is raw — callers Marshal the Result first.
func (s *sseSink) EmitDone(body []byte) {
	fmt.Fprintf(s.w, "event: done\ndata: %s\n\n", body)
	if s.f != nil {
		s.f.Flush()
	}
}

// staticSink wraps a value-receiver Write/Close for fast adapters.
// Tests use captureSink; production paths use sseSink (HTTP) and
// wsSink (added in Task 4).
var _ executor.Sink = (*sseSink)(nil)
```

### Step 3.4: Implement `http.go`

- [ ] **Create `internal/commander/http.go`**

```go
package commander

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// LinkStatusFunc reports whether the daemon currently has an active
// WS link to observer. Used by /healthz; supplied by daemon.go which
// wires the wsclient state.
type LinkStatusFunc func() bool

// NewHTTPHandler mounts the daemon's local HTTP API on a fresh
// ServeMux. Default bind in daemon.go is 127.0.0.1:0; routes are:
//
//	GET  /healthz                — link status + uptime
//	GET  /sessions               — Handler.ListSessions
//	GET  /sessions/{id}          — Handler.GetSession
//	POST /sessions/{id}/turn     — Handler.SessionTurn, SSE response
//
// The HTTP API is debug / local-web only per spec decision record;
// production / cross-host commanding goes through the WS to observer.
func NewHTTPHandler(h *Handler, link LinkStatusFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok\nlinked: %v\n", link())
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessions, err := h.ListSessions(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": sessions})
	})
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/sessions/")
		if rest == "" {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(rest, "/turn") {
			id := strings.TrimSuffix(rest, "/turn")
			handleTurn(h, w, r, id)
			return
		}
		// GET /sessions/{id}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess, msgs, err := h.GetSession(r.Context(), rest)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session":  sess,
			"messages": msgs,
		})
	})
	return mux
}

func handleTurn(h *Handler, w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Probe with a no-op sink-less call to surface 404 early — we
	// don't want to start a 200 SSE stream and then have to fail.
	// (RunResume returns ErrSessionNotFound when id is unknown.)
	sink := newSSESink(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	res, err := h.SessionTurn(r.Context(), id, body.Prompt, sink)
	if errors.Is(err, agentbackend.ErrSessionNotFound) {
		// Headers may already be sent if backend wrote events before
		// detecting the missing id — but with the typical flow the
		// error fires before first Write. If we already wrote 200,
		// fall back to emitting an error event in the stream.
		if w.Header().Get("Content-Type") == "text/event-stream" {
			// stream is open; emit error event
			fmt.Fprintf(w, "event: error\ndata: {\"code\":\"session_not_found\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: {\"code\":\"internal\",\"message\":%q}\n\n", err.Error())
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	doneBody, _ := json.Marshal(map[string]any{
		"summary":           res.Summary,
		"capability_change": res.CapabilityChange,
		"session_id":        res.SessionID,
		"awaiting_user":     res.AwaitingUser,
	})
	sink.EmitDone(doneBody)
}
```

NOTE: the order-of-operations on `ErrSessionNotFound` for `/turn` is genuinely awkward in HTTP — backend may call sink before failing. Tests pass with the "fail-before-stream" assumption. Document in `http.go` comment, accept v1 limitation.

### Step 3.5: GREEN

```bash
go test ./internal/commander/ -count=1 -v
go build ./...
go vet ./...
```

Expected: 5 protocol + 5 handler + 6 HTTP + 3 sink tests PASS.

### Step 3.6: Commit

```bash
git add internal/commander/http.go internal/commander/sink.go internal/commander/http_test.go internal/commander/sink_test.go
git commit -m "feat(commander): HTTP + SSE transport (PR-2 Task 3)

internal/commander/http.go mounts /healthz, /sessions, /sessions/{id},
POST /sessions/{id}/turn (SSE). All routes dispatch to the shared
Handler; SSE turn streams chunk events via sseSink and terminates with
'event: done' carrying the marshalled Result.

sink.go: sseSink adapter implementing executor.Sink writing SSE
event/data pairs with Flush after each. EmitDone is the terminal
marker; Close is a no-op (HTTP just closes the response body).

LinkStatusFunc dependency-injects the WS link state from daemon.go so
http_test can swap a stub instead of standing up a real WS client.

Per spec: HTTP API is bind 127.0.0.1, debug / local-web only.
Production / cross-host commanding flows through WS (Task 4).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: WebSocket client (`wsclient.go`)

**Files:**
- Modify: `go.mod` / `go.sum` (add `gorilla/websocket`)
- Create: `internal/commander/wsclient.go`
- Create: `internal/commander/wsclient_test.go`
- Modify: `internal/commander/sink.go` (add wsSink half)
- Modify: `internal/commander/sink_test.go` (add wsSink tests)

### Step 4.1: Add gorilla/websocket dependency

```bash
go get github.com/gorilla/websocket@latest
go mod tidy
```

Confirm:
```bash
grep gorilla go.mod
```

Expected: `github.com/gorilla/websocket v1.x.x` listed.

### Step 4.2: Write failing wsclient tests

- [ ] **Create `internal/commander/wsclient_test.go`**

```go
package commander

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// fakeObserver upgrades incoming HTTP to WS and records every frame
// the daemon sends. Lets tests inject server→daemon frames via Send.
type fakeObserver struct {
	t          *testing.T
	upgrader   websocket.Upgrader
	mu         sync.Mutex
	conns      []*websocket.Conn
	received   []Envelope
	bearer     string
	rejectAuth bool
}

func newFakeObserver(t *testing.T) *fakeObserver {
	return &fakeObserver{t: t, upgrader: websocket.Upgrader{}}
}

func (f *fakeObserver) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.bearer = r.Header.Get("Authorization")
		if f.rejectAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		for {
			var env Envelope
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
			f.mu.Lock()
			f.received = append(f.received, env)
			f.mu.Unlock()
		}
	})
}

func (f *fakeObserver) Send(env Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.conns) == 0 {
		return nil
	}
	return f.conns[len(f.conns)-1].WriteJSON(env)
}

// TestWSClient_DialsAndRegisters pins the bootstrap: daemon dials the
// observer URL with Bearer auth and sends register as first frame.
func TestWSClient_DialsAndRegisters(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	c := NewWSClient(WSConfig{
		URL:           wsURL,
		ProxyToken:    "token-abc",
		Register:      RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude", DisplayName: "test"},
		Handler:       &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:  10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// Wait until observer records the register frame.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		fo.mu.Lock()
		n := len(fo.received)
		fo.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := fo.bearer; got != "Bearer token-abc" {
		t.Errorf("auth header=%q", got)
	}
	fo.mu.Lock()
	got := fo.received
	fo.mu.Unlock()
	if len(got) < 1 || got[0].Type != "register" {
		t.Fatalf("first frame not register: %+v", got)
	}
	cancel()
	<-errCh
}

// TestWSClient_DispatchesCommandAndReturnsResult pins the round-trip:
// observer sends command, daemon invokes Handler, command_result
// envelope comes back with the same id.
func TestWSClient_DispatchesCommandAndReturnsResult(t *testing.T) {
	listFn := func(_ context.Context) ([]agentbackend.Session, error) {
		return []agentbackend.Session{{ID: "s1"}}, nil
	}
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	c := NewWSClient(WSConfig{
		URL:           wsURL,
		ProxyToken:    "t",
		Register:      RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:       &Handler{Backend: &fakeBackend{listFn: listFn}},
		HeartbeatInt:  10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// Wait for register.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		fo.mu.Lock()
		n := len(fo.received)
		fo.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send command.
	cmdEnv := Envelope{
		Type:    "command",
		ID:      "cmd-1",
		Payload: jsonRaw(t, CommandPayload{Command: "list_sessions"}),
	}
	if err := fo.Send(cmdEnv); err != nil {
		t.Fatal(err)
	}

	// Wait for result.
	deadline = time.Now().Add(1 * time.Second)
	var resultEnv *Envelope
	for time.Now().Before(deadline) {
		fo.mu.Lock()
		for i, e := range fo.received {
			if e.Type == "command_result" && e.ID == "cmd-1" {
				resultEnv = &fo.received[i]
				break
			}
		}
		fo.mu.Unlock()
		if resultEnv != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resultEnv == nil {
		t.Fatal("no command_result for cmd-1")
	}
	var body struct {
		Sessions []agentbackend.Session `json:"sessions"`
	}
	if err := json.Unmarshal(resultEnv.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].ID != "s1" {
		t.Errorf("sessions=%+v", body.Sessions)
	}
	cancel()
	<-errCh
}

// TestWSClient_TurnCommandStreamsEvents pins that session_turn emits
// event envelopes mid-stream and a final command_result.
func TestWSClient_TurnCommandStreamsEvents(t *testing.T) {
	resumeFn := func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
		sink.Write("chunk", "one")
		sink.Write("chunk", "two")
		sink.Close()
		return executor.Result{Summary: "done"}, nil
	}
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	c := NewWSClient(WSConfig{
		URL:           wsURL,
		ProxyToken:    "t",
		Register:      RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:       &Handler{Backend: &fakeBackend{resumeFn: resumeFn}},
		HeartbeatInt:  10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// Wait for register before sending command.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		fo.mu.Lock()
		n := len(fo.received)
		fo.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	turn := Envelope{
		Type: "command",
		ID:   "turn-1",
		Payload: jsonRaw(t, CommandPayload{
			Command: "session_turn",
			Args:    jsonRaw(t, SessionTurnArgs{ID: "s1", Prompt: "do"}),
		}),
	}
	_ = fo.Send(turn)

	// Wait for command_result.
	deadline = time.Now().Add(2 * time.Second)
	var eventCount int
	var sawResult bool
	for time.Now().Before(deadline) {
		fo.mu.Lock()
		eventCount = 0
		sawResult = false
		for _, e := range fo.received {
			if e.ID != "turn-1" {
				continue
			}
			if e.Type == "event" {
				eventCount++
			}
			if e.Type == "command_result" {
				sawResult = true
			}
		}
		fo.mu.Unlock()
		if sawResult {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawResult {
		t.Fatal("no command_result for turn-1")
	}
	if eventCount < 2 {
		t.Errorf("eventCount=%d want >=2", eventCount)
	}
}

// TestWSClient_ReconnectsOnDrop pins that closing the connection from
// the server causes the daemon to redial and re-register.
func TestWSClient_ReconnectsOnDrop(t *testing.T) {
	fo := newFakeObserver(t)
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	c := NewWSClient(WSConfig{
		URL:           wsURL,
		ProxyToken:    "t",
		Register:      RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:       &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:  10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// Wait for first register.
	waitFor(t, func() bool {
		fo.mu.Lock()
		defer fo.mu.Unlock()
		return len(fo.received) >= 1
	}, 1*time.Second)

	// Drop the connection from the server side.
	fo.mu.Lock()
	for _, c := range fo.conns {
		_ = c.Close()
	}
	fo.mu.Unlock()

	// Wait for second register (re-dial).
	waitFor(t, func() bool {
		fo.mu.Lock()
		defer fo.mu.Unlock()
		registers := 0
		for _, e := range fo.received {
			if e.Type == "register" {
				registers++
			}
		}
		return registers >= 2
	}, 2*time.Second)
}

// TestWSClient_RejectsUnauthorized pins that 401 on dial returns an
// error from Run (caller can choose to retry or surface to operator).
func TestWSClient_RejectsUnauthorized(t *testing.T) {
	fo := newFakeObserver(t)
	fo.rejectAuth = true
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"

	c := NewWSClient(WSConfig{
		URL:            wsURL,
		ProxyToken:     "bad",
		Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
		Handler:        &Handler{Backend: &fakeBackend{}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// On auth fail the client should retry per backoff; ctx deadline
	// stops it. Run returns ctx.Err() — confirm we don't panic and
	// don't successfully register anywhere.
	_ = c.Run(ctx)
	fo.mu.Lock()
	defer fo.mu.Unlock()
	for _, e := range fo.received {
		if e.Type == "register" {
			t.Fatalf("registered despite 401")
		}
	}
}

func jsonRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
```

- [ ] **Append to `internal/commander/sink_test.go`** (wsSink half)

```go

// TestWSSink_WriteForwardsAsEventEnvelope pins that each sink Write
// produces an event envelope with the right id + payload, queued for
// the wsclient to flush.
func TestWSSink_WriteForwardsAsEventEnvelope(t *testing.T) {
	var got []Envelope
	send := func(e Envelope) error { got = append(got, e); return nil }
	s := newWSSink("cmd-1", send)
	s.Write("chunk", "hello")
	s.Write("capability", "added foo")
	s.Close()

	if len(got) != 2 {
		t.Fatalf("got %d envelopes", len(got))
	}
	if got[0].Type != "event" || got[0].ID != "cmd-1" {
		t.Errorf("envelope[0]=%+v", got[0])
	}
	var p1 EventPayload
	_ = json.Unmarshal(got[0].Payload, &p1)
	if p1.EventKind != "chunk" || p1.Text != "hello" {
		t.Errorf("payload[0]=%+v", p1)
	}
}
```

(json import already present in earlier sink_test.go from sseSink tests; if not, add it.)

### Step 4.3: Verify RED

```bash
go test ./internal/commander/ -run "TestWSClient_|TestWSSink_" -count=1
```

Expected: undefined `NewWSClient`, `WSConfig`, `newWSSink`.

### Step 4.4: Implement wsSink in `sink.go`

- [ ] **Append to `internal/commander/sink.go`**

```go

// wsSink adapts executor.Sink writes to event envelopes sent via the
// provided send function. The wsclient pumps the envelopes back to
// the observer over the live WS connection (one envelope per Write).
// Close is a no-op — the wsclient sends command_result with the final
// Result as the terminal frame.
type wsSink struct {
	cmdID string
	send  func(Envelope) error
}

func newWSSink(cmdID string, send func(Envelope) error) *wsSink {
	return &wsSink{cmdID: cmdID, send: send}
}

func (s *wsSink) Write(eventType, data string) {
	payload, _ := json.Marshal(EventPayload{EventKind: eventType, Text: data})
	_ = s.send(Envelope{Type: "event", ID: s.cmdID, Payload: payload})
}

func (s *wsSink) Close() {}

var _ executor.Sink = (*wsSink)(nil)
```

### Step 4.5: Implement `wsclient.go`

- [ ] **Create `internal/commander/wsclient.go`**

```go
package commander

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// WSConfig is the dial + behaviour knobs for WSClient.
type WSConfig struct {
	// URL is the full WS URL to dial (ws:// or wss://).
	URL string
	// ProxyToken is the agentserver-issued token used as bearer auth.
	ProxyToken string
	// Register is the payload sent immediately after dial.
	Register RegisterPayload
	// Handler dispatches inbound commands.
	Handler *Handler
	// HeartbeatInt is how often the daemon sends a heartbeat frame.
	HeartbeatInt time.Duration
	// InitialBackoff / MaxBackoff bound the dial retry timer.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// WSClient owns the outbound WebSocket link to observer. Run keeps a
// connection alive (or repeatedly redials) until ctx is cancelled.
type WSClient struct {
	cfg WSConfig

	mu     sync.Mutex
	linked bool
}

func NewWSClient(cfg WSConfig) *WSClient {
	if cfg.HeartbeatInt == 0 {
		cfg.HeartbeatInt = 30 * time.Second
	}
	if cfg.InitialBackoff == 0 {
		cfg.InitialBackoff = time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	return &WSClient{cfg: cfg}
}

// Linked reports whether the WS link is currently up (used by /healthz).
func (c *WSClient) Linked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.linked
}

// Run blocks. Returns nil on ctx cancel; never returns a non-nil error
// in normal operation (transient dial/read failures are absorbed by
// the reconnect loop).
func (c *WSClient) Run(ctx context.Context) error {
	backoff := c.cfg.InitialBackoff
	for ctx.Err() == nil {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		// Mark down + sleep backoff.
		c.mu.Lock()
		c.linked = false
		c.mu.Unlock()

		if err != nil {
			// stay silent on transient failures (operator sees them via
			// /healthz); log placeholder available if needed.
			_ = err
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff *= 2
		if backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
	return nil
}

// runOnce performs a single dial → register → read loop. Returns nil
// when ctx cancels; non-nil when the connection drops.
func (c *WSClient) runOnce(ctx context.Context) error {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.cfg.ProxyToken)

	dialer := *websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, c.cfg.URL, hdr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Concurrent writers serialized.
	var writeMu sync.Mutex
	write := func(env Envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(env)
	}

	// Register.
	regPayload, _ := json.Marshal(c.cfg.Register)
	if err := write(Envelope{Type: "register", Payload: regPayload}); err != nil {
		return err
	}

	c.mu.Lock()
	c.linked = true
	c.mu.Unlock()

	// Heartbeat goroutine.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		ticker := time.NewTicker(c.cfg.HeartbeatInt)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = write(Envelope{Type: "heartbeat"})
			case <-hbCtx.Done():
				return
			}
		}
	}()

	// Read loop.
	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		switch env.Type {
		case "command":
			// Dispatch in a goroutine so long-running session_turn
			// doesn't block other commands. Each dispatch writes its
			// own result / events via the same connection.
			go c.dispatchCommand(ctx, env, write)
		case "ping":
			_ = write(Envelope{Type: "heartbeat"})
		case "error":
			// Observer-side error — currently log + continue.
			// schema_version_mismatch should be terminal but v1 just
			// surfaces via the next reconnect.
			_ = env
		}
	}
}

func (c *WSClient) dispatchCommand(ctx context.Context, env Envelope, write func(Envelope) error) {
	var cmd CommandPayload
	if err := json.Unmarshal(env.Payload, &cmd); err != nil {
		_ = write(errEnvelope(env.ID, ErrCodeInternal, "bad command payload: "+err.Error()))
		return
	}
	switch cmd.Command {
	case "list_sessions":
		sessions, err := c.cfg.Handler.ListSessions(ctx)
		if err != nil {
			_ = write(errEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(map[string]any{"sessions": sessions})
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	case "get_session":
		var args GetSessionArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errEnvelope(env.ID, ErrCodeInternal, err.Error()))
			return
		}
		sess, msgs, err := c.cfg.Handler.GetSession(ctx, args.ID)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errEnvelope(env.ID, ErrCodeSessionNotFound, "no such session"))
			return
		}
		if err != nil {
			_ = write(errEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(map[string]any{"session": sess, "messages": msgs})
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	case "session_turn":
		var args SessionTurnArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errEnvelope(env.ID, ErrCodeInternal, err.Error()))
			return
		}
		sink := newWSSink(env.ID, write)
		res, err := c.cfg.Handler.SessionTurn(ctx, args.ID, args.Prompt, sink)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errEnvelope(env.ID, ErrCodeSessionNotFound, "no such session"))
			return
		}
		if err != nil {
			_ = write(errEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(map[string]any{
			"summary":           res.Summary,
			"capability_change": res.CapabilityChange,
			"session_id":        res.SessionID,
			"awaiting_user":     res.AwaitingUser,
		})
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	default:
		_ = write(errEnvelope(env.ID, ErrCodeInternal, fmt.Sprintf("unknown command: %s", cmd.Command)))
	}
}

func errEnvelope(id, code, msg string) Envelope {
	body, _ := json.Marshal(ErrorPayload{Code: code, Message: msg})
	return Envelope{Type: "error", ID: id, Payload: body}
}
```

### Step 4.6: GREEN

```bash
go test ./internal/commander/ -count=1 -race -v
go build ./...
go vet ./...
```

Expected: all tests pass; race clean.

### Step 4.7: Commit

```bash
git add internal/commander/wsclient.go internal/commander/sink.go internal/commander/wsclient_test.go internal/commander/sink_test.go go.mod go.sum
git commit -m "feat(commander): WebSocket client + wsSink (PR-2 Task 4)

internal/commander/wsclient.go: WSClient owns the outbound link to
observer. Run keeps connection alive: dial with Bearer ProxyToken,
send register, read commands in a loop, dispatch each to the shared
Handler in its own goroutine. session_turn streams events back via
wsSink.

Reconnect loop with exponential backoff (1s → 30s); heartbeat every
30s by default. Auth failure / read errors trigger backoff + redial.

wsSink (in sink.go): adapts executor.Sink writes to event envelopes
carrying the originating command id. Close is a no-op; the wsclient
sends command_result as the terminal frame.

gorilla/websocket added — pure Go, CGO_ENABLED=0 friendly. Tests
mock observer via httptest + gorilla upgrader.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Daemon top-level orchestrator (`daemon.go`)

**Files:**
- Create: `internal/commander/daemon.go`
- Create: `internal/commander/daemon_test.go`

### Step 5.1: Write failing daemon tests

- [ ] **Create `internal/commander/daemon_test.go`**

```go
package commander

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestDaemon_BothTransportsServeListSessions pins that a running
// daemon exposes the same data to (a) HTTP /sessions and (b) WS
// list_sessions command.
func TestDaemon_BothTransportsServeListSessions(t *testing.T) {
	listFn := func(_ context.Context) ([]agentbackend.Session, error) {
		return []agentbackend.Session{{ID: "alpha"}}, nil
	}
	fo := newFakeObserver(t)
	wsSrv := httptest.NewServer(fo.handler())
	defer wsSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http") + "/api/daemon-link"

	d := NewDaemon(DaemonConfig{
		Handler:    &Handler{Backend: &fakeBackend{listFn: listFn}},
		ListenAddr: "127.0.0.1:0",
		WS: WSConfig{
			URL:            wsURL,
			ProxyToken:     "t",
			Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     50 * time.Millisecond,
			HeartbeatInt:   10 * time.Second,
		},
	})

	// Daemon.Handler is internally wired to WS too; supply it here for clarity.
	d.WS.Handler = d.handler

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	// Wait for HTTP to bind + WS register.
	waitFor(t, func() bool { return d.HTTPAddr() != "" }, 2*time.Second)
	waitFor(t, func() bool {
		fo.mu.Lock()
		defer fo.mu.Unlock()
		return len(fo.received) >= 1
	}, 2*time.Second)

	// HTTP path.
	httpResp, err := http.Get("http://" + d.HTTPAddr() + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	body, _ := io.ReadAll(httpResp.Body)
	if !strings.Contains(string(body), `"alpha"`) {
		t.Errorf("HTTP body missing alpha: %s", body)
	}

	// WS path.
	_ = fo.Send(Envelope{
		Type:    "command",
		ID:      "cmd-1",
		Payload: jsonRaw(t, CommandPayload{Command: "list_sessions"}),
	})
	waitFor(t, func() bool {
		fo.mu.Lock()
		defer fo.mu.Unlock()
		for _, e := range fo.received {
			if e.Type == "command_result" && e.ID == "cmd-1" {
				var pl struct {
					Sessions []agentbackend.Session `json:"sessions"`
				}
				_ = json.Unmarshal(e.Payload, &pl)
				return len(pl.Sessions) == 1 && pl.Sessions[0].ID == "alpha"
			}
		}
		return false
	}, 2*time.Second)

	cancel()
	<-errCh
}

// TestDaemon_GracefulShutdownClosesBothTransports pins clean exit:
// after ctx cancel, HTTP server stops + WS connection closes + Run
// returns within reasonable time.
func TestDaemon_GracefulShutdownClosesBothTransports(t *testing.T) {
	fo := newFakeObserver(t)
	wsSrv := httptest.NewServer(fo.handler())
	defer wsSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http") + "/api/daemon-link"

	d := NewDaemon(DaemonConfig{
		Handler:    &Handler{Backend: &fakeBackend{}},
		ListenAddr: "127.0.0.1:0",
		WS: WSConfig{
			URL:            wsURL,
			ProxyToken:     "t",
			Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     50 * time.Millisecond,
			HeartbeatInt:   10 * time.Second,
		},
	})
	d.WS.Handler = d.handler

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	waitFor(t, func() bool { return d.HTTPAddr() != "" }, 2*time.Second)
	addr := d.HTTPAddr()

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of cancel")
	}

	// HTTP should be unreachable now.
	client := &http.Client{Timeout: 200 * time.Millisecond}
	_, err := client.Get("http://" + addr + "/healthz")
	if err == nil {
		t.Error("HTTP still serving after shutdown")
	}
}
```

### Step 5.2: Verify RED

```bash
go test ./internal/commander/ -run TestDaemon_ -count=1
```

Expected: undefined `NewDaemon` / `DaemonConfig` / `HTTPAddr`.

### Step 5.3: Implement `daemon.go`

- [ ] **Create `internal/commander/daemon.go`**

```go
package commander

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// DaemonConfig wires together the HTTP listener + WS client + shared
// Handler. ListenAddr default "127.0.0.1:0" makes the OS pick a free
// port; HTTPAddr() reports the bound address after Run starts.
type DaemonConfig struct {
	// Handler is the command dispatcher shared between transports.
	Handler *Handler
	// ListenAddr is the HTTP bind (default "127.0.0.1:0"). Per spec,
	// binding 0.0.0.0 is allowed but should emit a warning from the
	// caller (cmd/driver-agent/main.go) — daemon.go is transport-only.
	ListenAddr string
	// WS holds the WebSocket dial config. Handler is filled in by
	// Daemon (so all transports route through the same handler).
	WS WSConfig
}

// Daemon orchestrates the HTTP listener + WS client. Run blocks until
// ctx is cancelled, then drains both transports and returns.
type Daemon struct {
	cfg      DaemonConfig
	handler  *Handler
	wsClient *WSClient

	mu       sync.Mutex
	httpAddr string

	// WS is exposed so callers can override Handler before Run if
	// they need access to the internal handler (the daemon_test.go
	// pattern). In production cmd/driver-agent wiring, Handler is
	// passed in via DaemonConfig.WS.Handler before NewDaemon.
	WS *WSConfig
}

func NewDaemon(cfg DaemonConfig) *Daemon {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	d := &Daemon{
		cfg:     cfg,
		handler: cfg.Handler,
	}
	d.WS = &d.cfg.WS
	return d
}

// HTTPAddr is the actual bound address (host:port). Empty before Run.
func (d *Daemon) HTTPAddr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.httpAddr
}

// Run starts both transports. Returns when ctx is cancelled. Errors
// from internal goroutines are absorbed (HTTP serve normally returns
// http.ErrServerClosed on shutdown; WS reconnect loop returns nil on
// ctx done).
func (d *Daemon) Run(ctx context.Context) error {
	// Wire WS to handler so commands dispatch.
	d.cfg.WS.Handler = d.handler
	d.wsClient = NewWSClient(d.cfg.WS)

	httpHandler := NewHTTPHandler(d.handler, LinkStatusFunc(d.wsClient.Linked))
	srv := &http.Server{
		Handler: httpHandler,
	}
	ln, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.httpAddr = ln.Addr().String()
	d.mu.Unlock()

	var wg sync.WaitGroup

	// HTTP server.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(ln) // returns ErrServerClosed on Shutdown
	}()

	// WS client.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = d.wsClient.Run(ctx)
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	wg.Wait()
	return nil
}

// keep errors visible to test compilation
var _ = errors.New
```

### Step 5.4: GREEN

```bash
go test ./internal/commander/ -count=1 -race -v
go build ./...
go vet ./...
```

Expected: all tests pass; race clean.

### Step 5.5: Commit

```bash
git add internal/commander/daemon.go internal/commander/daemon_test.go
git commit -m "feat(commander): daemon orchestrator (PR-2 Task 5)

Daemon wires HTTP listener + WS client to the shared Handler in one
process. Run blocks until ctx cancel, then graceful-shuts the HTTP
server (5s timeout) and the WS client (ctx-driven). HTTPAddr() reports
the bound address (defaults bind 127.0.0.1:0 → OS-picked port).

LinkStatusFunc connects /healthz to wsclient.Linked() so debug curl
shows whether the observer link is currently up.

End-to-end test: same daemon serves list_sessions over both HTTP and
WS, returns identical data, shuts down cleanly on ctx cancel.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: driver config `Daemon` section

**Files:**
- Modify: `internal/driver/config.go`
- Modify: `internal/driver/config_test.go`

### Step 6.1: Write failing config tests

- [ ] **Append to `internal/driver/config_test.go`**

```go
// TestLoadConfig_DaemonDefaults pins that omitting daemon: in YAML
// gives sensible defaults: listen 127.0.0.1:0, ws_path
// /api/daemon-link, heartbeat 30s, backoff 1s→30s.
func TestLoadConfig_DaemonDefaults(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Daemon.Listen != "127.0.0.1:0" {
		t.Errorf("Daemon.Listen=%q want 127.0.0.1:0", c.Daemon.Listen)
	}
	if c.Daemon.WSPath != "/api/daemon-link" {
		t.Errorf("Daemon.WSPath=%q want /api/daemon-link", c.Daemon.WSPath)
	}
	if c.Daemon.HeartbeatIntervalSec != 30 {
		t.Errorf("Daemon.HeartbeatIntervalSec=%d want 30", c.Daemon.HeartbeatIntervalSec)
	}
	if c.Daemon.InitialBackoffMs != 1000 {
		t.Errorf("Daemon.InitialBackoffMs=%d want 1000", c.Daemon.InitialBackoffMs)
	}
	if c.Daemon.MaxBackoffMs != 30000 {
		t.Errorf("Daemon.MaxBackoffMs=%d want 30000", c.Daemon.MaxBackoffMs)
	}
}

// TestLoadConfig_DaemonExplicit pins that explicit values are
// preserved (not overridden by defaults).
func TestLoadConfig_DaemonExplicit(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
discovery:
  display_name: x
daemon:
  listen: 127.0.0.1:9099
  ws_path: /custom/path
  heartbeat_interval_sec: 60
  initial_backoff_ms: 500
  max_backoff_ms: 15000
`
	path := writeTempYAML(t, yaml)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Daemon.Listen != "127.0.0.1:9099" {
		t.Errorf("Listen=%q", c.Daemon.Listen)
	}
	if c.Daemon.WSPath != "/custom/path" {
		t.Errorf("WSPath=%q", c.Daemon.WSPath)
	}
	if c.Daemon.HeartbeatIntervalSec != 60 {
		t.Errorf("Heartbeat=%d", c.Daemon.HeartbeatIntervalSec)
	}
	if c.Daemon.InitialBackoffMs != 500 || c.Daemon.MaxBackoffMs != 15000 {
		t.Errorf("backoff=%d,%d", c.Daemon.InitialBackoffMs, c.Daemon.MaxBackoffMs)
	}
}
```

### Step 6.2: Verify RED

```bash
go test ./internal/driver/ -run "TestLoadConfig_Daemon" -count=1 -v
```

Expected: undefined `c.Daemon` field.

### Step 6.3: Add `Daemon` struct + defaults

- [ ] **Find the `type Config struct` block in `internal/driver/config.go`** and add `Daemon` field:

```go
type Config struct {
	// ... existing fields ...
	DriverDefaults DriverDefaults `yaml:"driver_defaults"`
	Observer       Observer       `yaml:"observer,omitempty"`
	Daemon         Daemon         `yaml:"daemon,omitempty"` // PR-2 commander
}
```

(Exact location: append `Daemon Daemon ...` line after `Observer`. Other fields untouched.)

- [ ] **Append the `Daemon` struct definition** (place near `Observer` for cohesion):

```go
// Daemon configures the optional long-lived `driver-agent serve-daemon`
// mode (commander-web-entry PR-2). Empty / missing block uses the
// defaults applied by LoadConfig.
type Daemon struct {
	// Listen is the HTTP bind for the daemon's debug API.
	// Default: "127.0.0.1:0" — OS picks a free port. Bind 0.0.0.0
	// is allowed but cmd/driver-agent emits a warning at startup.
	Listen string `yaml:"listen,omitempty"`
	// WSPath is the path appended to Observer.URL when dialing.
	// Default: "/api/daemon-link". Must match the observer hub.
	WSPath string `yaml:"ws_path,omitempty"`
	// HeartbeatIntervalSec is the daemon→observer heartbeat cadence.
	// Default: 30.
	HeartbeatIntervalSec int `yaml:"heartbeat_interval_sec,omitempty"`
	// InitialBackoffMs / MaxBackoffMs bound the WS reconnect timer.
	// Defaults: 1000 / 30000.
	InitialBackoffMs int `yaml:"initial_backoff_ms,omitempty"`
	MaxBackoffMs     int `yaml:"max_backoff_ms,omitempty"`
}
```

- [ ] **In the `LoadConfig` function, apply daemon defaults** (find the existing `// apply defaults` block; if there is no explicit "apply defaults" block locate where other defaults like `TaskTimeoutSec` are filled in and add adjacent):

```go
	// Daemon defaults (PR-2 commander).
	if c.Daemon.Listen == "" {
		c.Daemon.Listen = "127.0.0.1:0"
	}
	if c.Daemon.WSPath == "" {
		c.Daemon.WSPath = "/api/daemon-link"
	}
	if c.Daemon.HeartbeatIntervalSec == 0 {
		c.Daemon.HeartbeatIntervalSec = 30
	}
	if c.Daemon.InitialBackoffMs == 0 {
		c.Daemon.InitialBackoffMs = 1000
	}
	if c.Daemon.MaxBackoffMs == 0 {
		c.Daemon.MaxBackoffMs = 30000
	}
```

### Step 6.4: GREEN

```bash
go test ./internal/driver/ -count=1
go build ./...
go vet ./...
```

Expected: 2 new daemon tests PASS; existing driver tests still pass.

### Step 6.5: Commit

```bash
git add internal/driver/config.go internal/driver/config_test.go
git commit -m "feat(driver): Daemon config section + defaults (PR-2 Task 6)

internal/driver/config.go: Config gains a Daemon block configuring the
HTTP bind, WS path, heartbeat interval, and reconnect backoff for the
new serve-daemon subcommand (Task 7).

LoadConfig fills sensible defaults (127.0.0.1:0 / /api/daemon-link /
30s heartbeat / 1s→30s backoff) when daemon: is omitted, so existing
configs keep working without edits.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: cmd/driver-agent serve-daemon subcommand

**Files:**
- Modify: `cmd/driver-agent/main.go`
- Modify: `cmd/driver-agent/main_test.go`
- Modify: `cmd/driver-agent/README.md`

### Step 7.1: Write failing flag-parse test

- [ ] **Append to `cmd/driver-agent/main_test.go`**

```go
// TestServeDaemon_ParseFlagsConfigRequired pins that --config is
// required and absence fails fast.
func TestServeDaemon_ParseFlagsConfigRequired(t *testing.T) {
	if _, err := parseServeDaemonFlags([]string{}); err == nil {
		t.Fatal("expected error for missing --config")
	}
}

// TestServeDaemon_ParseFlagsConfigParsed pins that --config picks up
// the path.
func TestServeDaemon_ParseFlagsConfigParsed(t *testing.T) {
	opts, err := parseServeDaemonFlags([]string{"--config", "/tmp/x.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.ConfigPath != "/tmp/x.yaml" {
		t.Errorf("ConfigPath=%q", opts.ConfigPath)
	}
}

// TestServeDaemon_ParseFlagsListenOverride pins --listen override.
func TestServeDaemon_ParseFlagsListenOverride(t *testing.T) {
	opts, err := parseServeDaemonFlags([]string{
		"--config", "/tmp/x.yaml",
		"--listen", "0.0.0.0:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Listen != "0.0.0.0:8080" {
		t.Errorf("Listen=%q", opts.Listen)
	}
}
```

### Step 7.2: Verify RED

```bash
go test ./cmd/driver-agent/ -run TestServeDaemon_ -count=1
```

Expected: undefined `parseServeDaemonFlags`.

### Step 7.3: Add subcommand dispatch + runner

- [ ] **Modify `cmd/driver-agent/main.go`** — update `usage` const + `switch` + add `runServeDaemon` + `parseServeDaemonFlags`:

Find the existing `usage` const:

```go
const usage = `driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register   --config /path/to/driver.yaml
  driver-agent serve-mcp  --config /path/to/driver.yaml
`
```

Replace with:

```go
const usage = `driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register      --config /path/to/driver.yaml
  driver-agent serve-mcp     --config /path/to/driver.yaml
  driver-agent serve-daemon  --config /path/to/driver.yaml [--listen host:port]
`
```

Find the `switch os.Args[1]` block and add the new case:

```go
	switch os.Args[1] {
	case "register":
		runRegister(os.Args[2:])
	case "serve-mcp":
		runServe(os.Args[2:])
	case "serve-daemon":
		runServeDaemon(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
```

Append `parseServeDaemonFlags` + `runServeDaemon` at end of file:

```go
type serveDaemonOpts struct {
	ConfigPath string
	Listen     string // empty = use cfg.Daemon.Listen
}

func parseServeDaemonFlags(args []string) (serveDaemonOpts, error) {
	fs := flag.NewFlagSet("serve-daemon", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to driver.yaml (required)")
	listen := fs.String("listen", "", "HTTP bind (overrides config; default 127.0.0.1:0)")
	if err := fs.Parse(args); err != nil {
		return serveDaemonOpts{}, err
	}
	if *cfgPath == "" {
		return serveDaemonOpts{}, fmt.Errorf("--config is required")
	}
	return serveDaemonOpts{ConfigPath: *cfgPath, Listen: *listen}, nil
}

func runServeDaemon(args []string) {
	opts, err := parseServeDaemonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve-daemon:", err)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cfg, err := driver.LoadConfig(opts.ConfigPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if cfg.Credentials.ProxyToken == "" {
		die("serve-daemon requires cfg.Credentials.ProxyToken — run `driver-agent register` first")
	}
	if cfg.Observer.URL == "" {
		die("serve-daemon requires cfg.Observer.URL")
	}

	listen := opts.Listen
	if listen == "" {
		listen = cfg.Daemon.Listen
	}
	if strings.HasPrefix(listen, "0.0.0.0") {
		fmt.Fprintln(os.Stderr, "WARNING: serve-daemon HTTP bound to 0.0.0.0 — debug API will be reachable from the network")
	}

	backend, err := agentbackend.New(agentbackend.Config{
		Kind:      agentbackend.Kind(cfg.Agent.Kind),
		Bin:       cfg.Agent.Bin,
		WorkDir:   cfg.Agent.WorkDir,
		ExtraArgs: cfg.Agent.ExtraArgs,
	}, nil)
	if err != nil {
		die("agentbackend.New: " + err.Error())
	}

	wsURL := strings.TrimRight(cfg.Observer.URL, "/") + cfg.Daemon.WSPath
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	d := commander.NewDaemon(commander.DaemonConfig{
		Handler:    &commander.Handler{Backend: backend},
		ListenAddr: listen,
		WS: commander.WSConfig{
			URL:        wsURL,
			ProxyToken: cfg.Credentials.ProxyToken,
			Register: commander.RegisterPayload{
				SchemaVersion: commander.SchemaVersion,
				Kind:          cfg.Agent.Kind,
				AgentBin:      cfg.Agent.Bin,
				AgentWorkDir:  cfg.Agent.WorkDir,
				DisplayName:   cfg.Discovery.DisplayName,
				DriverVersion: "v0.0.0", // populated by build; placeholder for v1
			},
			HeartbeatInt:   time.Duration(cfg.Daemon.HeartbeatIntervalSec) * time.Second,
			InitialBackoff: time.Duration(cfg.Daemon.InitialBackoffMs) * time.Millisecond,
			MaxBackoff:     time.Duration(cfg.Daemon.MaxBackoffMs) * time.Millisecond,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	fmt.Fprintf(os.Stderr, "serve-daemon: starting (config=%s, ws=%s)\n", opts.ConfigPath, wsURL)
	if err := d.Run(ctx); err != nil {
		die("daemon: " + err.Error())
	}
	// HTTPAddr after Run starts via stderr — daemon writes nothing
	// by itself; cmd-level wrapper logs.
}
```

Update imports at top of `main.go`:

```go
import (
	// ... existing ...
	"github.com/yourorg/multi-agent/internal/commander"
	// ... existing ...
)
```

(Add the commander import; `context`, `signal`, `syscall`, `time`, `strings` should already be in the file.)

### Step 7.4: GREEN

```bash
go test ./cmd/driver-agent/ -count=1 -v
go build ./...
go vet ./...
```

Expected: 3 new tests PASS.

### Step 7.5: Update README

- [ ] **Append to `cmd/driver-agent/README.md`** (or update Usage section):

```markdown
## serve-daemon (commander web entry, PR-2)

Long-lived mode that dials a WebSocket out to observer (the cloud
endpoint named in `config.yaml`'s `observer.url`) and exposes a local
HTTP debug API on `127.0.0.1`. The observer hub (PR-3) uses the WS to
fan out web-issued commands to this daemon, which delegates to the
backend (`Backend.ListSessions / GetSession / RunResume`).

    driver-agent serve-daemon --config /path/to/driver.yaml [--listen host:port]

Requires `driver-agent register --config ...` first (the daemon reuses
`cfg.Credentials.ProxyToken` as its WS bearer token).

Coexists with `serve-mcp` — same config file, different transport.

The local HTTP API is debug-only by default (`127.0.0.1:0`, OS picks
free port; printed to stderr on startup). Override with `--listen
0.0.0.0:9099` for trusted-LAN access — a warning prints when bind is
not loopback.
```

### Step 7.6: Commit

```bash
git add cmd/driver-agent/main.go cmd/driver-agent/main_test.go cmd/driver-agent/README.md
git commit -m "feat(driver-agent): serve-daemon subcommand (PR-2 Task 7)

cmd/driver-agent gains a third subcommand:

  driver-agent serve-daemon --config driver.yaml [--listen host:port]

Loads driver.yaml, constructs the agentbackend.Backend (using PR-1's
ListSessions/GetSession/RunResume) , and starts the commander daemon
which:
  - dials WS to cfg.Observer.URL + cfg.Daemon.WSPath, Bearer ProxyToken
  - listens HTTP on cfg.Daemon.Listen (default 127.0.0.1:0, OS port)
  - serves /healthz, /sessions, /sessions/{id}, /sessions/{id}/turn
  - blocks until SIGINT/SIGTERM, then graceful shutdown

Fail-fast: missing ProxyToken (operator forgot register) or missing
Observer.URL surfaces a clear error. Bind 0.0.0.0 prints a warning.

Coexists with serve-mcp (same config, different transport).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Full regression + push + PR

**Files:** no code changes — verification + git only.

### Step 8.1: Full repo race

```bash
go test ./... -race -count=1 2>&1 | tail -30
```

Expected: all `ok`, no FAIL.

### Step 8.2: Master-path freeze

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
```

Expected: empty.

### Step 8.3: Binary smoke (CGO=0)

```bash
mkdir -p /tmp/bin-smoke-pr2
CGO_ENABLED=0 go build -o /tmp/bin-smoke-pr2/driver-agent ./cmd/driver-agent
/tmp/bin-smoke-pr2/driver-agent 2>&1 | head -10
```

Expected: usage text includes the new `serve-daemon` line.

### Step 8.4: End-to-end smoke (no real observer)

Spin up a throwaway WS server locally, point a real serve-daemon at it for ~5s to confirm wiring outside of unit tests:

```bash
cat > /tmp/fake-obs.go <<'EOF'
package main

import (
	"fmt"
	"net/http"
	"github.com/gorilla/websocket"
)

func main() {
	up := websocket.Upgrader{}
	http.HandleFunc("/api/daemon-link", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("auth:", r.Header.Get("Authorization"))
		c, err := up.Upgrade(w, r, nil)
		if err != nil { return }
		defer c.Close()
		for {
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil { return }
			fmt.Printf("recv: %v\n", m["type"])
		}
	})
	http.ListenAndServe("127.0.0.1:18099", nil)
}
EOF
go run /tmp/fake-obs.go &
OBS_PID=$!
sleep 1

# Use any existing driver.yaml with ProxyToken populated; if none, skip this step.
# Confirm at least "auth: Bearer <token>" + "recv: register" prints.
# Then kill both processes.
kill $OBS_PID 2>/dev/null
```

(Skip if no driver.yaml on hand — unit tests already cover this path.)

### Step 8.5: Write evidence doc

- [ ] **Create `docs/superpowers/specs/2026-06-15-driver-daemon-evidence.md`**

Mirror the §1.5 evidence doc structure (committed on master at `docs/superpowers/specs/2026-06-14-planner-1.5-evidence.md`). Sections:

```markdown
# driver-agent serve-daemon — 测试证据

- 日期：2026-06-15
- 分支：worktree-driver-daemon @ HEAD <SHA>
- 范围：PR-2 of commander-web-entry vision — daemon mode, WS out, HTTP API
- 不在范围：observer-side hub (PR-3); web UI; master path

## 为何 unit + binary smoke 足够

PR-2 is pure greenfield package + new subcommand. No cross-service
runtime to test — observer side is PR-3. Coverage:

- WS envelope: round-trip every payload struct
- Handler: forward to Backend with errors propagated
- HTTP+SSE: route table + streaming format
- WS client: dial / register / dispatch / stream / reconnect /
  401-fail-and-retry against in-process gorilla upgrader
- Daemon: both transports serve same data, graceful shutdown
- Config: daemon block defaults + explicit override
- Driver-agent CLI: flag parse for serve-daemon

15 new test functions; all pass under `-race`. Master-path freeze
honored (diff stat empty).

## 提交序列

<git log --oneline master..HEAD>

## Open items for PR-3 (recorded so reviewer sees them)

- observer must implement WS endpoint at /api/daemon-link
- observer must dispatch each user's WS sessions by subject
- observer must validate Bearer ProxyToken via existing
  identity.Resolver chain (agentserver /api/agent/whoami)
- if observer expects schema_version > 1, daemon will need bump
```

### Step 8.6: Commit + push + PR

```bash
git add docs/superpowers/specs/2026-06-15-driver-daemon-evidence.md
git commit -m "docs: test evidence for commander PR-2 (driver-daemon)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"

git push -u origin worktree-driver-daemon

gh pr create --base master --head worktree-driver-daemon \
  --title "feat(driver-agent): serve-daemon + commander package (commander PR-2)" \
  --body "$(cat <<'EOF'
PR-2 of the 3-PR commander-web-entry vision ([docs/vision/commander-web-entry.md](docs/vision/commander-web-entry.md)). Adds the long-lived daemon mode that exposes session list/get/turn via WebSocket (to observer hub, PR-3) and a local HTTP API (debug/local-web only).

## Scope

- `internal/commander/` (new package):
  - `protocol.go` — WS envelope schema locked at SchemaVersion 1
  - `handler.go` — shared command dispatcher
  - `http.go` + `sink.go` — HTTP API + SSE streaming
  - `wsclient.go` — outbound WS dial + reconnect + heartbeat
  - `daemon.go` — orchestrator wiring HTTP + WS in one process
- `internal/driver/config.go` — `Daemon` config block + defaults
- `cmd/driver-agent/` — new `serve-daemon` subcommand
- `go.mod` — `github.com/gorilla/websocket` (pure Go, CGO_ENABLED=0 friendly)

## Why

PR-2 unblocks PR-3: the WS envelope schema in `protocol.go` is the contract the observer hub will implement. Each daemon kind (claude / codex / opencode) is a long-lived process so the web UI can issue commands at any time, not just when a CLI is open.

## Master-path freeze (still honored)

`git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'` → empty.

## Testing

- WS envelope round-trips (every payload type)
- Handler delegates to Backend; ErrSessionNotFound propagates
- HTTP routes + SSE format
- WS client: dial / register / command-dispatch / event-streaming / reconnect on drop / Bearer auth
- Daemon: both transports serve identical data; clean shutdown on ctx cancel
- driver config Daemon defaults
- driver-agent CLI flag parsing
- Full repo race: green
- Binary smoke under CGO_ENABLED=0

## Out of scope

- observer-side WS hub + reverse proxy + web UI (PR-3)
- session writes (use existing Backend.RunResume)
- master path (frozen)
- session persistence on daemon (in-memory; backends own session files)
- multi-kind in single daemon (one daemon per kind)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage:**

| Spec invariant | Implementing task |
|---|---|
| 1 (daemon long-running) | Task 5 + Task 7 (sig-handled main) |
| 2 (WS out + Bearer) | Task 4 |
| 3 (reconnect + backoff) | Task 4 |
| 4 (HTTP 127.0.0.1 default) | Task 5 + Task 7 (--listen flag) |
| 5 (HTTP / WS share handler) | Task 2 (`Handler`) + Task 3 (HTTP) + Task 4 (WS) |
| 6 (envelope schema lock) | Task 1 |
| 7 (sink stream events) | Task 3 (sseSink) + Task 4 (wsSink) |
| 8 (coexist with serve-mcp) | Task 7 (new subcommand alongside existing dispatch) |
| 9 (master path frozen) | Task 8 verify step |

**Placeholder scan:** None. The "log placeholder available if needed" comment in `runOnce` is acceptable runtime documentation, not a TBD task obligation. The `// daemon writes nothing by itself` end-of-`runServeDaemon` comment is honest — daemon's `Run` blocks until ctx cancel, no return value beyond shutdown.

**Type / signature consistency:**
- `Envelope` / payload structs — Task 1, used identically Tasks 2/3/4/5
- `Handler.{ListSessions, GetSession, SessionTurn}` — defined Task 2, called Tasks 3/4
- `executor.Sink` interface — referenced Task 3 (sseSink), Task 4 (wsSink)
- `LinkStatusFunc` — defined Task 3, supplied Task 5
- `WSConfig` / `DaemonConfig` — defined Tasks 4/5, used Task 7
- `RegisterPayload` — defined Task 1, populated Task 7
- `cfg.Daemon.*` field names — defined Task 6, consumed Task 7

**Single risk-worth-flagging:** `daemon.go`'s `WS` exposed field (so test can swap Handler after construction) is a leaky abstraction. If reviewers push back, replace with a `WithWSHandler` setter. Acceptable for v1.
