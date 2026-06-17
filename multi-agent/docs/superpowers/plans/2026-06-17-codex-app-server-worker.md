# Codex App-Server Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in Codex hot session worker backed by `codex app-server`, while preserving the current `codex exec resume` fallback and runtime context.

**Architecture:** First harden the existing exec-resume path and backend-neutral status protocol, then add an opt-in Codex app-server wrapper that implements `agentbackend.SessionWorkerBackend` only when enabled. The app-server path uses stdio JSON-RPC, resumes the same Codex thread ID, preserves cwd/env/config/MCP parity, emits backend-neutral status codes, and falls back to `RunResume` only before a turn is accepted.

**Tech Stack:** Go, existing Commander WebSocket/SSE protocol, `codex app-server` JSON-RPC over stdio, existing `humanloop` MCP IPC, table/fake-server tests.

---

## File Structure

- Create `multi-agent/pkg/agentbackend/status.go`
  - New backend-neutral status helper and constants.
- Modify `multi-agent/pkg/agentbackend/config.go`
  - Add generic `WorkerMode string` to the flat backend config carrier.
- Modify `multi-agent/internal/config/config.go`
  - Add `worker_mode` to the slave config `AgentConfig`.
- Modify `multi-agent/internal/driver/config.go`
  - Add `worker_mode` to the driver agent config and daemon cache fields to the existing `DaemonConfig`.
- Modify `multi-agent/cmd/driver-agent/main.go`
  - Pass `WorkerMode`, `WorkerMax`, and `WorkerIdleTimeout` into backend/handler construction.
  - Add `humanloop-mcp` subcommand support.
- Modify `multi-agent/cmd/slave-agent/main.go`
  - Continue passing `WorkerMode`.
- Create `multi-agent/cmd/driver-agent/humanloop_subcmd.go`
  - Mirror the slave-agent humanloop MCP entrypoint.
- Modify `multi-agent/internal/commander/protocol.go`
  - Add `status_code` to `EventPayload` without removing `Extra`.
- Modify `multi-agent/internal/commander/sink.go`
  - Implement `agentbackend.StatusSink` for WS and SSE sinks.
- Modify `multi-agent/internal/commander/http.go`
  - Emit accepted status with `agentbackend.WriteStatus`.
- Modify `multi-agent/internal/commander/wsclient.go`
  - Emit queued status with `agentbackend.WriteStatus`.
- Modify `multi-agent/internal/commander/handler.go`
  - Emit fallback status with `agentbackend.WriteStatus`.
- Modify `multi-agent/internal/commanderhub/http.go`
  - Prefer `status_code` when updating turn state.
- Modify `multi-agent/pkg/agentbackend/codex/executor.go`
  - Use `agentbackend.WriteStatus`.
- Modify `multi-agent/pkg/agentbackend/codex/backend.go`
  - Return an enabled wrapper only when `worker_mode: app_server`.
- Create `multi-agent/pkg/agentbackend/codex/appserver_rpc.go`
  - Minimal line-oriented JSON-RPC client.
- Create `multi-agent/pkg/agentbackend/codex/appserver_manager.go`
  - Owns the app-server process, initialization, probes, thread resume, and shutdown.
- Create `multi-agent/pkg/agentbackend/codex/appserver_worker.go`
  - Implements `SessionWorker` and `HealthySessionWorker`.
- Create tests beside each changed package:
  - `multi-agent/pkg/agentbackend/status_test.go`
  - `multi-agent/internal/commander/sink_test.go`
  - `multi-agent/internal/commander/protocol_test.go`
  - `multi-agent/internal/commander/handler_test.go`
  - `multi-agent/internal/commanderhub/http_test.go`
  - `multi-agent/internal/driver/config_test.go`
  - `multi-agent/internal/config/config_test.go`
  - `multi-agent/cmd/driver-agent/main_test.go`
  - `multi-agent/pkg/agentbackend/codex/executor_test.go`
  - `multi-agent/pkg/agentbackend/codex/backend_resume_test.go`
  - `multi-agent/pkg/agentbackend/codex/appserver_rpc_test.go`
  - `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`

---

### Task 1: Backend-Neutral Status Codes

**Files:**
- Create: `multi-agent/pkg/agentbackend/status.go`
- Test: `multi-agent/pkg/agentbackend/status_test.go`
- Modify: `multi-agent/internal/commander/protocol.go`
- Modify: `multi-agent/internal/commander/sink.go`
- Modify: `multi-agent/internal/commander/http.go`
- Modify: `multi-agent/internal/commander/wsclient.go`
- Modify: `multi-agent/internal/commander/handler.go`
- Modify: `multi-agent/internal/commanderhub/http.go`
- Modify: `multi-agent/pkg/agentbackend/codex/executor.go`
- Test: `multi-agent/internal/commander/protocol_test.go`
- Test: `multi-agent/internal/commander/sink_test.go`
- Test: `multi-agent/internal/commanderhub/http_test.go`

- [ ] **Step 1: Write failing tests for status helper fallback and structured status**

Add to `multi-agent/pkg/agentbackend/status_test.go`:

```go
package agentbackend

import "testing"

type statusCaptureSink struct {
	events []struct {
		kind string
		text string
	}
	statuses []struct {
		code string
		text string
	}
}

func (s *statusCaptureSink) Write(kind, text string) {
	s.events = append(s.events, struct {
		kind string
		text string
	}{kind: kind, text: text})
}

func (s *statusCaptureSink) Close() {}

func (s *statusCaptureSink) WriteStatus(code, text string) {
	s.statuses = append(s.statuses, struct {
		code string
		text string
	}{code: code, text: text})
}

type plainCaptureSink struct {
	kind string
	text string
}

func (s *plainCaptureSink) Write(kind, text string) { s.kind, s.text = kind, text }
func (s *plainCaptureSink) Close()                  {}

func TestWriteStatusUsesStructuredSink(t *testing.T) {
	sink := &statusCaptureSink{}
	WriteStatus(sink, StatusStarting, "starting codex")
	if len(sink.statuses) != 1 {
		t.Fatalf("statuses=%+v", sink.statuses)
	}
	if sink.statuses[0].code != StatusStarting || sink.statuses[0].text != "starting codex" {
		t.Fatalf("status=%+v", sink.statuses[0])
	}
	if len(sink.events) != 0 {
		t.Fatalf("plain events=%+v", sink.events)
	}
}

func TestWriteStatusFallsBackToPlainStatusEvent(t *testing.T) {
	sink := &plainCaptureSink{}
	WriteStatus(sink, StatusAnswering, "codex running")
	if sink.kind != "status" || sink.text != "codex running" {
		t.Fatalf("plain status=(%q,%q)", sink.kind, sink.text)
	}
}
```

Add to `multi-agent/internal/commander/protocol_test.go`:

```go
func TestEventPayloadStatusCodePreservesExtra(t *testing.T) {
	extra := json.RawMessage(`{"source":"test"}`)
	payload := mustMarshal(t, EventPayload{
		EventKind:  "status",
		Text:       "starting codex",
		Extra:      extra,
		StatusCode: "starting",
	})
	assertJSONKeys(t, payload, "event_kind", "text", "extra", "status_code")

	var got EventPayload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.EventKind != "status" || got.Text != "starting codex" || got.StatusCode != "starting" || string(got.Extra) != string(extra) {
		t.Fatalf("payload=%+v extra=%s", got, got.Extra)
	}
}
```

Add to `multi-agent/internal/commander/sink_test.go`:

```go
func TestWSSinkWriteStatusIncludesStatusCode(t *testing.T) {
	var sent Envelope
	sink := newWSSink("cmd-1", func(env Envelope) error {
		sent = env
		return nil
	})
	sink.WriteStatus(agentbackend.StatusStarting, "starting codex")

	if sent.Type != "event" || sent.ID != "cmd-1" {
		t.Fatalf("envelope=%+v", sent)
	}
	var payload EventPayload
	if err := json.Unmarshal(sent.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.EventKind != "status" || payload.Text != "starting codex" || payload.StatusCode != agentbackend.StatusStarting {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestSSESinkWriteStatusIncludesStatusCode(t *testing.T) {
	var b strings.Builder
	sink := newSSESink(&b)
	sink.WriteStatus(agentbackend.StatusAnswering, "codex running")
	got := b.String()
	if !strings.Contains(got, "event: status") || !strings.Contains(got, `"status_code":"answering"`) {
		t.Fatalf("sse=%s", got)
	}
}
```

Add to `multi-agent/internal/commanderhub/http_test.go`:

```go
func TestUpdateTurnStatePrefersStatusCode(t *testing.T) {
	hub := NewHub(&fakeResolver{})
	ch := &commanderHandlers{hub: hub}
	key := turnKey{owner: owner{userID: "u", workspaceID: "w"}, daemonID: "d", commandID: "cmd"}
	hub.turns.begin(key)

	payload, err := json.Marshal(commander.EventPayload{
		EventKind:  "status",
		Text:       "backend-specific text",
		StatusCode: agentbackend.StatusStarting,
	})
	require.NoError(t, err)
	ch.updateTurnStateFromEnvelope(key, commander.Envelope{Type: "event", Payload: payload})

	state := hub.turns.get(key)
	require.Equal(t, turnStateQueued, state.State)
	require.True(t, state.InFlight)

	payload, err = json.Marshal(commander.EventPayload{
		EventKind:  "status",
		Text:       "backend-specific text",
		StatusCode: agentbackend.StatusAnswering,
	})
	require.NoError(t, err)
	ch.updateTurnStateFromEnvelope(key, commander.Envelope{Type: "event", Payload: payload})

	state = hub.turns.get(key)
	require.Equal(t, turnStateAnswering, state.State)
	require.True(t, state.InFlight)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/agentbackend ./internal/commander ./internal/commanderhub
```

Expected: FAIL because `StatusSink`, `WriteStatus`, `StatusCode`, and sink `WriteStatus` do not exist yet.

- [ ] **Step 3: Add status helper and protocol field**

Create `multi-agent/pkg/agentbackend/status.go`:

```go
package agentbackend

const (
	StatusQueued           = "queued"
	StatusStarting         = "starting"
	StatusAnswering        = "answering"
	StatusAwaitingApproval = "awaiting_approval"
	StatusDone             = "done"
	StatusError            = "error"
)

type StatusSink interface {
	WriteStatus(statusCode, text string)
}

func WriteStatus(s Sink, statusCode, text string) {
	if ss, ok := s.(StatusSink); ok {
		ss.WriteStatus(statusCode, text)
		return
	}
	s.Write("status", text)
}
```

Modify `multi-agent/internal/commander/protocol.go`:

```go
type EventPayload struct {
	EventKind  string          `json:"event_kind"`
	Text       string          `json:"text,omitempty"`
	Extra      json.RawMessage `json:"extra,omitempty"`
	StatusCode string          `json:"status_code,omitempty"`
}
```

- [ ] **Step 4: Add structured status to WS/SSE sinks**

Modify `multi-agent/internal/commander/sink.go`:

```go
func (s *sseSink) WriteStatus(statusCode, text string) {
	payload, _ := json.Marshal(struct {
		Text       string `json:"text,omitempty"`
		StatusCode string `json:"status_code,omitempty"`
	}{Text: text, StatusCode: statusCode})
	fmt.Fprintf(s.w, "event: status\ndata: %s\n\n", payload)
	s.written = true
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *wsSink) WriteStatus(statusCode, text string) {
	payload, _ := json.Marshal(EventPayload{EventKind: "status", Text: text, StatusCode: statusCode})
	_ = s.send(Envelope{Type: "event", ID: s.cmdID, Payload: payload})
}
```

Add compile-time assertions:

```go
var _ agentbackend.StatusSink = (*sseSink)(nil)
var _ agentbackend.StatusSink = (*wsSink)(nil)
```

- [ ] **Step 5: Convert status producers to the helper**

Modify these call sites:

```go
agentbackend.WriteStatus(sink, agentbackend.StatusQueued, "accepted by daemon")
agentbackend.WriteStatus(sink, agentbackend.StatusQueued, "queued on daemon")
agentbackend.WriteStatus(sink, agentbackend.StatusStarting, "hot worker unavailable; falling back to resume")
agentbackend.WriteStatus(sink, agentbackend.StatusStarting, "starting codex")
agentbackend.WriteStatus(sink, agentbackend.StatusAnswering, "codex running")
```

The exact files are:

- `multi-agent/internal/commander/http.go`
- `multi-agent/internal/commander/wsclient.go`
- `multi-agent/internal/commander/handler.go`
- `multi-agent/pkg/agentbackend/codex/executor.go`

- [ ] **Step 6: Prefer status_code in observer state updates**

Modify `multi-agent/internal/commanderhub/http.go`:

```go
case "status":
	switch ep.StatusCode {
	case agentbackend.StatusQueued:
		ch.hub.turns.set(key, turnStateQueued)
	case agentbackend.StatusStarting:
		ch.hub.turns.set(key, turnStateQueued)
	case agentbackend.StatusAnswering:
		ch.hub.turns.set(key, turnStateAnswering)
	case agentbackend.StatusAwaitingApproval:
		ch.hub.turns.finish(key, turnStateAwaitingApproval)
	case agentbackend.StatusDone:
		ch.hub.turns.finish(key, turnStateDone)
	case agentbackend.StatusError:
		ch.hub.turns.fail(key, ep.Text)
	default:
		switch ep.Text {
		case "queued on daemon", "queued-on-daemon", "accepted by daemon":
			ch.hub.turns.set(key, turnStateQueued)
		case "starting codex":
			ch.hub.turns.set(key, turnStateQueued)
		case "codex running":
			ch.hub.turns.set(key, turnStateAnswering)
		}
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run:

```bash
go test ./pkg/agentbackend ./internal/commander ./internal/commanderhub ./pkg/agentbackend/codex
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add multi-agent/pkg/agentbackend/status.go \
  multi-agent/pkg/agentbackend/status_test.go \
  multi-agent/internal/commander/protocol.go \
  multi-agent/internal/commander/protocol_test.go \
  multi-agent/internal/commander/sink.go \
  multi-agent/internal/commander/sink_test.go \
  multi-agent/internal/commander/http.go \
  multi-agent/internal/commander/wsclient.go \
  multi-agent/internal/commander/handler.go \
  multi-agent/internal/commanderhub/http.go \
  multi-agent/internal/commanderhub/http_test.go \
  multi-agent/pkg/agentbackend/codex/executor.go
git commit -m "feat(commander): add backend-neutral turn status codes"
```

---

### Task 2: Preserve Exec-Resume Context and Humanloop MCP

**Files:**
- Modify: `multi-agent/cmd/driver-agent/main.go`
- Create: `multi-agent/cmd/driver-agent/humanloop_subcmd.go`
- Modify: `multi-agent/pkg/agentbackend/codex/executor_test.go`
- Modify: `multi-agent/pkg/agentbackend/codex/backend_resume_test.go`
- Test: `multi-agent/cmd/driver-agent/main_test.go`

- [ ] **Step 1: Write failing tests for driver-agent humanloop subcommand**

Add to `multi-agent/cmd/driver-agent/main_test.go`:

```go
func TestDriverAgentSupportsHumanloopMCPSubcommand(t *testing.T) {
	args := []string{`{"network":"","address":""}`, "5"}
	err := runHumanloopMCP(args)
	if err == nil {
		t.Fatal("runHumanloopMCP unexpectedly succeeded with invalid endpoint")
	}
	if !strings.Contains(err.Error(), "humanloop") && !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("error=%v", err)
	}
}
```

Add `strings` to that file's import block if it is not already present.

This intentionally calls the subcommand helper directly; it proves `driver-agent` has the same helper symbol as `slave-agent` without starting a long-lived stdio MCP server.

- [ ] **Step 2: Write failing tests for RunResume parity**

Extend `multi-agent/pkg/agentbackend/codex/executor_test.go` with:

```go
func TestCodexExecutorRunResumePreservesExtraArgsEnvAndHumanloopMCP(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	envPath := filepath.Join(dir, "env.txt")
	script := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	_ = os.WriteFile(%q, []byte(strings.Join(os.Args[1:], "\n")), 0600)
	_ = os.WriteFile(%q, []byte(os.Getenv("CODEX_HOME")+"|"+os.Getenv("LOOM_TEST_ENV")), 0600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(`+"`"+`{"type":"thread.started","thread_id":"thr-resumed"}`+"`"+`)
	fmt.Println(`+"`"+`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`+"`"+`)
}
`, argsPath, envPath))

	codexHome := t.TempDir()
	ex := newExecutor(agentbackend.Config{
		Bin:       script,
		WorkDir:   t.TempDir(),
		ExtraArgs: []string{"--profile", "loom-test"},
	}, []string{"CODEX_HOME=" + codexHome, "LOOM_TEST_ENV=present"})
	_, err := ex.RunResume(context.Background(), "thr-1", "continue", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}

	argsRaw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsRaw)
	for _, want := range []string{
		"exec",
		"resume",
		"thr-1",
		"--profile",
		"loom-test",
		"mcp_servers.loom_humanloop.command",
		"humanloop-mcp",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("argv missing %q:\n%s", want, args)
		}
	}

	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(envRaw), codexHome+"|present"; got != want {
		t.Fatalf("env=%q want %q", got, want)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./cmd/driver-agent ./pkg/agentbackend/codex
```

Expected: FAIL because `cmd/driver-agent` does not expose `runHumanloopMCP` yet if this test is added before implementation.

- [ ] **Step 4: Add driver-agent humanloop MCP subcommand**

Create `multi-agent/cmd/driver-agent/humanloop_subcmd.go`:

```go
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/yourorg/multi-agent/internal/humanloop"
)

func runHumanloopMCP(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: driver-agent humanloop-mcp ENDPOINT_JSON_OR_SOCKET_PATH MAX_QUESTIONS")
	}
	endpointArg := args[0]
	max, err := strconv.Atoi(args[1])
	if err != nil || max <= 0 {
		return fmt.Errorf("max-questions must be a positive integer, got %q", args[1])
	}
	return humanloop.ServeStdio(os.Stdin, os.Stdout, endpointArg, max)
}
```

Modify `multi-agent/cmd/driver-agent/main.go`:

```go
case "humanloop-mcp":
	if err := runHumanloopMCP(os.Args[2:]); err != nil {
		log.Fatalf("driver_agent humanloop-mcp: %v", err)
	}
```

Add the usage line:

```text
  driver-agent humanloop-mcp ENDPOINT_JSON_OR_SOCKET_PATH MAX_QUESTIONS
```

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
go test ./cmd/driver-agent ./pkg/agentbackend/codex
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/cmd/driver-agent/main.go \
  multi-agent/cmd/driver-agent/humanloop_subcmd.go \
  multi-agent/cmd/driver-agent/main_test.go \
  multi-agent/pkg/agentbackend/codex/executor_test.go \
  multi-agent/pkg/agentbackend/codex/backend_resume_test.go
git commit -m "fix(codex): preserve resume runtime context"
```

---

### Task 3: Worker Mode Configuration and Off-Mode Behavior

**Files:**
- Modify: `multi-agent/pkg/agentbackend/config.go`
- Modify: `multi-agent/internal/driver/config.go`
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/cmd/driver-agent/main.go`
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Modify: `multi-agent/internal/commander/handler_test.go`
- Test: `multi-agent/internal/driver/config_test.go`
- Test: `multi-agent/internal/config/config_test.go`

- [ ] **Step 1: Write failing config tests**

Add to `multi-agent/internal/driver/config_test.go`:

```go
func TestLoadConfigAgentWorkerMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: "http://observer", name: "test"}
credentials: {proxy_token: "tok"}
agent:
  kind: codex
  bin: codex
  workdir: /tmp/repo
  worker_mode: app_server
discovery: {display_name: "daemon", description: "test", skills: []}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.WorkerMode != "app_server" {
		t.Fatalf("worker_mode=%q", cfg.Agent.WorkerMode)
	}
}
```

`multi-agent/internal/driver/config_test.go` already imports `os` and
`path/filepath`; keep using those helpers instead of adding a new config writer.

Add to `multi-agent/internal/config/config_test.go`:

```go
func TestLoadAgentWorkerMode(t *testing.T) {
	cfg, err := loadFromString(t, `
server: {url: "http://observer", name: "test"}
agent:
  kind: codex
  bin: codex
  workdir: /tmp/repo
  worker_mode: app_server
discovery: {display_name: "daemon", description: "test", skills: []}
`)
	require.NoError(t, err)
	require.Equal(t, "app_server", cfg.Agent.WorkerMode)
}
```

- [ ] **Step 2: Write failing off-mode no-extra-session-read test**

Add to `multi-agent/internal/commander/handler_test.go`:

```go
type resumeOnlyBackend struct {
	getFn    func(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error)
	resumeFn func(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error)
}

func (b *resumeOnlyBackend) Kind() agentbackend.Kind { return agentbackend.KindCodex }
func (b *resumeOnlyBackend) Run(context.Context, executor.Task, executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (b *resumeOnlyBackend) RunResume(ctx context.Context, id, answer string, sink executor.Sink) (executor.Result, error) {
	return b.resumeFn(ctx, id, answer, sink)
}
func (b *resumeOnlyBackend) LLM() agentbackend.LLMRunner                { return nil }
func (b *resumeOnlyBackend) Permissions() agentbackend.PermissionsStore { return nil }
func (b *resumeOnlyBackend) Detect(context.Context) error               { return nil }
func (b *resumeOnlyBackend) ListSessions(context.Context) ([]agentbackend.Session, error) {
	return nil, nil
}
func (b *resumeOnlyBackend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return b.getFn(ctx, id)
}

func TestHandler_OffModeBackendDoesNotFetchSessionBeforeResume(t *testing.T) {
	var getCalls atomic.Int32
	var resumeCalls atomic.Int32
	h := &Handler{Backend: &resumeOnlyBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			getCalls.Add(1)
			return agentbackend.Session{}, nil, errors.New("unexpected GetSession")
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			resumeCalls.Add(1)
			return executor.Result{Summary: "fallback"}, nil
		},
	}}

	_, err := h.SessionTurn(context.Background(), "s1", "prompt", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}
	if got := getCalls.Load(); got != 0 {
		t.Fatalf("GetSession calls=%d want 0", got)
	}
	if got := resumeCalls.Load(); got != 1 {
		t.Fatalf("RunResume calls=%d want 1", got)
	}
}
```

- [ ] **Step 3: Run tests to verify config tests fail**

Run:

```bash
go test ./internal/driver ./internal/config ./internal/commander
```

Expected: config tests FAIL until `WorkerMode` exists. The handler off-mode test should already pass and will guard the wrapper approach.

- [ ] **Step 4: Add WorkerMode fields and pass them through**

Modify `multi-agent/pkg/agentbackend/config.go`:

```go
type Config struct {
	Kind       Kind     `yaml:"kind"`
	Bin        string   `yaml:"bin"`
	WorkDir    string   `yaml:"workdir"`
	ExtraArgs  []string `yaml:"extra_args"`
	WorkerMode string   `yaml:"worker_mode"`
}
```

Modify both config shapes:

```go
type AgentConfig struct {
	Kind       string   `yaml:"kind"`
	Bin        string   `yaml:"bin"`
	WorkDir    string   `yaml:"workdir"`
	ExtraArgs  []string `yaml:"extra_args"`
	WorkerMode string   `yaml:"worker_mode"`
}
```

Modify `newAgentBackend` in `multi-agent/cmd/driver-agent/main.go` and backend construction in `multi-agent/cmd/slave-agent/main.go`:

```go
WorkerMode: cfg.Agent.WorkerMode,
```

- [ ] **Step 5: Add daemon worker cache config fields**

Modify `multi-agent/internal/driver/config.go`:

```go
type DaemonConfig struct {
	// Listen is the HTTP bind for the daemon's debug API.
	Listen               string `yaml:"listen,omitempty"`
	// WSPath is appended to Observer.URL when dialing the observer hub.
	WSPath               string `yaml:"ws_path,omitempty"`
	// HeartbeatIntervalSec is the daemon-to-observer heartbeat cadence.
	HeartbeatIntervalSec int    `yaml:"heartbeat_interval_sec,omitempty"`
	// InitialBackoffMs and MaxBackoffMs bound WS reconnect backoff.
	InitialBackoffMs     int    `yaml:"initial_backoff_ms,omitempty"`
	MaxBackoffMs         int    `yaml:"max_backoff_ms,omitempty"`
	// WorkerMax caps hot session workers kept by commander. Zero uses the handler default.
	WorkerMax            int    `yaml:"worker_max,omitempty"`
	// WorkerIdleTimeoutSec controls idle hot-worker lifetime. Zero uses the handler default.
	WorkerIdleTimeoutSec int    `yaml:"worker_idle_timeout_sec,omitempty"`
}
```

Preserve the existing fields and defaults in `LoadConfig`; only add the two worker fields. Modify `runServeDaemon` handler construction in `multi-agent/cmd/driver-agent/main.go` so `commander.Handler` receives the daemon cache settings:

Do not add nonzero defaults for `WorkerMax` or `WorkerIdleTimeoutSec` in `LoadConfig`; zero intentionally flows through to the Commander handler's existing defaults.

```go
handler := &commander.Handler{Backend: backend, WorkerMax: cfg.Daemon.WorkerMax}
if cfg.Daemon.WorkerIdleTimeoutSec > 0 {
	handler.WorkerIdleTimeout = time.Duration(cfg.Daemon.WorkerIdleTimeoutSec) * time.Second
}
```

Use `handler` in the daemon config:

```go
Handler: handler,
```

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/driver ./internal/config ./cmd/driver-agent ./cmd/slave-agent ./internal/commander
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/pkg/agentbackend/config.go \
  multi-agent/internal/driver/config.go \
  multi-agent/internal/driver/config_test.go \
  multi-agent/internal/config/config.go \
  multi-agent/internal/config/config_test.go \
  multi-agent/cmd/driver-agent/main.go \
  multi-agent/cmd/slave-agent/main.go \
  multi-agent/internal/commander/handler_test.go
git commit -m "feat(codex): add worker mode configuration"
```

---

### Task 4: Codex App-Server JSON-RPC Client

**Files:**
- Create: `multi-agent/pkg/agentbackend/codex/appserver_rpc.go`
- Test: `multi-agent/pkg/agentbackend/codex/appserver_rpc_test.go`

- [ ] **Step 1: Write failing JSON-RPC client tests**

Create `multi-agent/pkg/agentbackend/codex/appserver_rpc_test.go`:

```go
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestAppServerRPCRequestResponse(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	c := newAppServerRPC(clientReader, clientWriter)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(serverReader)
		if !sc.Scan() {
			errCh <- sc.Err()
			return
		}
		var req appServerRPCMessage
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			errCh <- err
			return
		}
		if req.Method != "thread/resume" || req.ID == nil {
			errCh <- errAppServerTest("unexpected request")
			return
		}
		_, err := serverWriter.Write([]byte(`{"id":` + string(*req.ID) + `,"result":{"ok":true}}` + "\n"))
		errCh <- err
	}()

	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.call(context.Background(), "thread/resume", map[string]string{"threadId": "thr-1"}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerRPCDispatchesNotifications(t *testing.T) {
	clientReader := io.NopCloser(strings.NewReader(`{"method":"item/agentMessage/delta","params":{"threadId":"thr","turnId":"turn","itemId":"item","delta":"hi"}}` + "\n"))
	c := newAppServerRPC(clientReader, io.Discard)
	ch := make(chan appServerRPCMessage, 1)
	c.onNotification = func(msg appServerRPCMessage) { ch <- msg }

	if err := c.readLoop(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg := <-ch
	if msg.Method != "item/agentMessage/delta" {
		t.Fatalf("method=%q", msg.Method)
	}
}

func TestAppServerRPCDropsUnknownResponses(t *testing.T) {
	clientReader := io.NopCloser(strings.NewReader(`{"id":99,"result":{"ignored":true}}` + "\n"))
	c := newAppServerRPC(clientReader, io.Discard)
	called := false
	c.onNotification = func(appServerRPCMessage) { called = true }

	if err := c.readLoop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("unknown response was dispatched as notification")
	}
}

type errAppServerTest string

func (e errAppServerTest) Error() string { return string(e) }
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/agentbackend/codex -run 'TestAppServerRPC'
```

Expected: FAIL because `newAppServerRPC` and related types do not exist.

- [ ] **Step 3: Implement minimal RPC client**

Create `multi-agent/pkg/agentbackend/codex/appserver_rpc.go`:

```go
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type appServerRPC struct {
	r io.Reader
	w io.Writer

	nextID atomic.Int64
	mu     sync.Mutex
	wait   map[int64]chan appServerRPCMessage

	onNotification func(appServerRPCMessage)
}

type appServerRPCMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *appServerError  `json:"error,omitempty"`
}

type appServerError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newAppServerRPC(r io.Reader, w io.Writer) *appServerRPC {
	return &appServerRPC{r: r, w: w, wait: map[int64]chan appServerRPCMessage{}}
}

func (c *appServerRPC) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	rawID := json.RawMessage(fmt.Sprintf("%d", id))
	ch := make(chan appServerRPCMessage, 1)
	c.mu.Lock()
	c.wait[id] = ch
	req := appServerRPCMessage{ID: &rawID, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		req.Params = b
	}
	b, err := json.Marshal(req)
	if err == nil {
		b = append(b, '\n')
		_, err = c.w.Write(b)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("app-server %s: %s", method, resp.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

func (c *appServerRPC) notify(method string, params any) error {
	msg := appServerRPCMessage{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = b
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(b)
	return err
}

func (c *appServerRPC) readLoop(ctx context.Context) error {
	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var msg appServerRPCMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			return err
		}
		if msg.ID != nil && msg.Method == "" {
			var id int64
			if err := json.Unmarshal(*msg.ID, &id); err == nil {
				c.mu.Lock()
				ch := c.wait[id]
				delete(c.wait, id)
				c.mu.Unlock()
				if ch != nil {
					ch <- msg
					continue
				}
				continue
			}
		}
		if c.onNotification != nil {
			c.onNotification(msg)
		}
	}
	return sc.Err()
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./pkg/agentbackend/codex -run 'TestAppServerRPC'
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/appserver_rpc.go \
  multi-agent/pkg/agentbackend/codex/appserver_rpc_test.go
git commit -m "feat(codex): add app-server rpc client"
```

---

### Task 5: App-Server Manager, Opt-In Wrapper, and Protocol Probe

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/backend.go`
- Create: `multi-agent/pkg/agentbackend/codex/appserver_manager.go`
- Test: `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`

- [ ] **Step 1: Write failing tests for opt-in wrapper**

Add to `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`:

```go
package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestCodexBackendDoesNotImplementSessionWorkerByDefault(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(agentbackend.SessionWorkerBackend); ok {
		t.Fatal("default codex backend unexpectedly implements SessionWorkerBackend")
	}
}

func TestCodexBackendImplementsSessionWorkerWhenAppServerModeEnabled(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(agentbackend.SessionWorkerBackend); !ok {
		t.Fatal("app_server codex backend does not implement SessionWorkerBackend")
	}
}

func TestCodexWorkerBackendFallsBackWhenProbeFails(t *testing.T) {
	b := New(agentbackend.Config{Bin: "definitely-missing-codex", WorkDir: t.TempDir()}, nil)
	wb := &workerBackend{Backend: b, manager: newAppServerManager(b.cfg, nil)}
	_, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{ID: "thr-1"})
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("err=%v want ErrSessionWorkerUnavailable", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/agentbackend/codex -run 'TestCodexBackend|TestCodexWorkerBackend'
```

Expected: FAIL because `WorkerMode`, `workerBackend`, and `newAppServerManager` are not fully wired.

- [ ] **Step 3: Add opt-in wrapper**

Modify `multi-agent/pkg/agentbackend/codex/backend.go`:

```go
type workerBackend struct {
	*Backend
	manager *appServerManager
}

func init() {
	agentbackend.RegisterBuilder(agentbackend.KindCodex, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		b := New(cfg, env)
		if cfg.WorkerMode == "app_server" {
			return &workerBackend{Backend: b, manager: newAppServerManager(cfg, env)}, nil
		}
		return b, nil
	})
}
```

Remove the old `init` body that always returns `New(cfg, env)`.

- [ ] **Step 4: Add manager skeleton with unavailable fallback**

Create `multi-agent/pkg/agentbackend/codex/appserver_manager.go`:

```go
package codex

import (
	"context"
	"errors"
	"os/exec"
	"sync"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type appServerManager struct {
	cfg agentbackend.Config
	env []string

	mu      sync.Mutex
	started bool
	rpc     *appServerRPC
	cmd     *exec.Cmd
	err     error
}

func newAppServerManager(cfg agentbackend.Config, env []string) *appServerManager {
	return &appServerManager{cfg: cfg, env: env}
}

func (m *appServerManager) ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.started {
		return nil
	}
	m.err = agentbackend.ErrSessionWorkerUnavailable
	return m.err
}

func (m *appServerManager) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		return m.cmd.Process.Kill()
	}
	return nil
}

func (b *workerBackend) NewSessionWorker(ctx context.Context, session agentbackend.Session) (agentbackend.SessionWorker, error) {
	if session.ID == "" {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	if err := b.manager.ensure(ctx); err != nil {
		if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
			return nil, err
		}
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	return nil, agentbackend.ErrSessionWorkerUnavailable
}
```

This step intentionally keeps app-server unavailable until the manager is implemented in the next task. It locks in the no-extra-off-mode invariant.

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./pkg/agentbackend/codex ./internal/commander
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/backend.go \
  multi-agent/pkg/agentbackend/codex/appserver_manager.go \
  multi-agent/pkg/agentbackend/codex/appserver_worker_test.go
git commit -m "feat(codex): add opt-in app-server worker wrapper"
```

---

### Task 6: App-Server Worker Event Mapping

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/appserver_manager.go`
- Create: `multi-agent/pkg/agentbackend/codex/appserver_worker.go`
- Test: `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`

- [ ] **Step 1: Write failing worker mapping tests**

Add to `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`:

```go
type appServerTestSink struct {
	events []appServerTestEvent
	closed bool
}

type appServerTestEvent struct {
	kind string
	data string
}

func (s *appServerTestSink) Write(kind, data string) {
	s.events = append(s.events, appServerTestEvent{kind: kind, data: data})
}

func (s *appServerTestSink) Close() {
	s.closed = true
}

func TestCodexSessionWorkerStreamsDeltasAndCapability(t *testing.T) {
	sink := &appServerTestSink{}
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(ctx context.Context, prompt string, emit func(appServerRPCMessage)) error {
			emit(appServerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"hello"}`))
			emit(appServerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"\n=== CAPABILITY ===\nnew cap"}`))
			emit(appServerNotification("turn/completed", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}`))
			return nil
		},
	}

	res, err := w.Run(context.Background(), "prompt", sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "hello" || res.CapabilityChange != "new cap" || res.SessionID != "thr-1" {
		t.Fatalf("result=%+v", res)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	if !sinkContains(sink, "chunk", "hello") || !sinkContains(sink, "capability", "new cap") {
		t.Fatalf("events=%+v", sink.events)
	}
}

func appServerNotification(method string, params string) appServerRPCMessage {
	return appServerRPCMessage{Method: method, Params: json.RawMessage(params)}
}

func sinkContains(sink *appServerTestSink, kind, data string) bool {
	for _, ev := range sink.events {
		if ev.kind == kind && ev.data == data {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./pkg/agentbackend/codex -run TestCodexSessionWorkerStreamsDeltasAndCapability
```

Expected: FAIL because `codexSessionWorker` does not exist.

- [ ] **Step 3: Implement worker event mapping**

Create `multi-agent/pkg/agentbackend/codex/appserver_worker.go`:

```go
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type codexSessionWorker struct {
	sessionID string
	workDir   string
	healthy   atomic.Bool

	runTurn func(ctx context.Context, prompt string, emit func(appServerRPCMessage)) error
	closeFn func() error
}

func (w *codexSessionWorker) Run(ctx context.Context, prompt string, sink agentbackend.Sink) (agentbackend.Result, error) {
	agentbackend.WriteStatus(sink, agentbackend.StatusStarting, "starting codex app-server")
	var accepted bool
	var text strings.Builder
	var runErr error
	runErr = w.runTurn(ctx, prompt, func(msg appServerRPCMessage) {
		switch msg.Method {
		case "turn/started":
			accepted = true
			agentbackend.WriteStatus(sink, agentbackend.StatusAnswering, "codex app-server running")
		case "item/agentMessage/delta":
			var p struct {
				ThreadID string `json:"threadId"`
				Delta    string `json:"delta"`
			}
			if err := json.Unmarshal(msg.Params, &p); err == nil && p.ThreadID == w.sessionID && p.Delta != "" {
				accepted = true
				sink.Write("chunk", p.Delta)
				text.WriteString(p.Delta)
			}
		case "turn/completed":
			accepted = true
		case "error":
			runErr = fmt.Errorf("app-server error: %s", string(msg.Params))
		}
	})
	full := text.String()
	summary, change := agentbackend.SplitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	sink.Close()
	if runErr != nil {
		if accepted {
			return agentbackend.Result{Summary: summary, CapabilityChange: change, SessionID: w.sessionID}, runErr
		}
		return agentbackend.Result{}, agentbackend.ErrSessionWorkerUnavailable
	}
	return agentbackend.Result{Summary: summary, CapabilityChange: change, SessionID: w.sessionID}, nil
}

func (w *codexSessionWorker) Close() error {
	w.healthy.Store(false)
	if w.closeFn != nil {
		return w.closeFn()
	}
	return nil
}

func (w *codexSessionWorker) Healthy() bool {
	return w.healthy.Load()
}
```

- [ ] **Step 4: Run worker tests**

Run:

```bash
go test ./pkg/agentbackend/codex -run 'TestCodexSessionWorker'
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/appserver_worker.go \
  multi-agent/pkg/agentbackend/codex/appserver_worker_test.go
git commit -m "feat(codex): map app-server worker events"
```

---

### Task 7: Wire Real App-Server Process and Humanloop MCP

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/appserver_manager.go`
- Modify: `multi-agent/pkg/agentbackend/codex/appserver_worker.go`
- Test: `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`

- [ ] **Step 1: Write failing manager request tests with fake process transport**

Add tests that assert the manager sends these JSON-RPC messages in order:

```json
{"method":"initialize","params":{"clientInfo":{"name":"loom_driver_daemon","title":"Loom Driver Daemon","version":"v0.0.0"},"capabilities":{"experimentalApi":true}}}
{"method":"initialized","params":{}}
{"method":"thread/resume","params":{"threadId":"thr-1","cwd":"/repo","config":{"mcp_servers":{"loom_humanloop":{"command":"/path/to/driver-agent","args":["humanloop-mcp","ENDPOINT_JSON","5"]}}}}}
{"method":"turn/start","params":{"threadId":"thr-1","cwd":"/repo","input":[{"type":"text","text":"User answered: continue"}]}}
```

Use a fake transport rather than spawning Codex:

```go
func TestAppServerManagerSendsResumeAndTurnWithContext(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	gotMethods := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(serverReader)
		for sc.Scan() {
			var req appServerRPCMessage
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				return
			}
			gotMethods <- req.Method
			if req.ID == nil {
				continue
			}
			_, _ = serverWriter.Write([]byte(`{"id":` + string(*req.ID) + `,"result":{"thread":{"id":"thr-1"},"turn":{"id":"turn-1","status":"running"}}}` + "\n"))
			if req.Method == "turn/start" {
				_, _ = serverWriter.Write([]byte(`{"method":"turn/started","params":{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}}` + "\n"))
				_, _ = serverWriter.Write([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"ok"}}` + "\n"))
				_, _ = serverWriter.Write([]byte(`{"method":"turn/completed","params":{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}}` + "\n"))
			}
		}
	}()

	m := newAppServerManager(agentbackend.Config{Bin: "codex", WorkDir: "/repo"}, nil)
	m.rpc = newAppServerRPC(clientReader, clientWriter)
	m.started = true
	wb := &workerBackend{Backend: New(agentbackend.Config{Bin: "codex", WorkDir: "/repo"}, nil), manager: m}

	worker, err := wb.NewSessionWorker(context.Background(), agentbackend.Session{ID: "thr-1", WorkingDir: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.Run(context.Background(), "continue", &appServerTestSink{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"thread/resume", "turn/start"}
	for _, method := range want {
		select {
		case got := <-gotMethods:
			if got != method {
				t.Fatalf("method=%q want %q", got, method)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", method)
		}
	}
}
```

This fake-transport test verifies the JSON-RPC shape without depending on a local Codex binary. The `cwd` fields on `thread/resume` and `turn/start` come from the Codex CLI 0.140.0 generated schemas and must be rechecked by the opt-in smoke test before enabling outside `tests/prod_test`.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/agentbackend/codex -run TestAppServerManagerSendsResumeAndTurnWithContext
```

Expected: FAIL until real manager methods exist.

- [ ] **Step 3: Implement process startup and initialization**

In `appserver_manager.go`, implement:

```go
func (m *appServerManager) ensure(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	cmd := exec.CommandContext(ctx, m.cfg.Bin, "app-server", "--listen", "stdio://")
	cmd.Dir = m.cfg.WorkDir
	cmd.Env = append(cmd.Environ(), m.env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.mu.Unlock()
		return agentbackend.ErrSessionWorkerUnavailable
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.mu.Unlock()
		return agentbackend.ErrSessionWorkerUnavailable
	}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return agentbackend.ErrSessionWorkerUnavailable
	}
	rpc := newAppServerRPC(stdout, stdin)
	m.cmd = cmd
	m.rpc = rpc
	m.started = true
	m.mu.Unlock()

	go func() { _ = rpc.readLoop(context.Background()) }()

	var initResp map[string]any
	if err := rpc.call(ctx, "initialize", initializeParams(), &initResp); err != nil {
		_ = m.close()
		return agentbackend.ErrSessionWorkerUnavailable
	}
	if err := rpc.notify("initialized", map[string]any{}); err != nil {
		_ = m.close()
		return agentbackend.ErrSessionWorkerUnavailable
	}
	return nil
}
```

Implement `initializeParams`:

```go
func initializeParams() map[string]any {
	return map[string]any{
		"clientInfo": map[string]any{
			"name":    "loom_driver_daemon",
			"title":   "Loom Driver Daemon",
			"version": "v0.0.0",
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}
}
```

- [ ] **Step 4: Implement per-worker humanloop IPC and thread resume**

In `NewSessionWorker`, create a worker-specific humanloop socket directory and server:

```go
sockDir, err := os.MkdirTemp("", "loom-codex-appserver-humanloop-")
if err != nil {
	return nil, agentbackend.ErrSessionWorkerUnavailable
}
srv, ep, err := humanloop.ListenIPC(sockDir)
if err != nil {
	os.RemoveAll(sockDir)
	return nil, agentbackend.ErrSessionWorkerUnavailable
}
```

Send `thread/resume` with:

```go
params := map[string]any{
	"threadId": session.ID,
	"cwd":      session.WorkingDir,
	"config": map[string]any{
		"mcp_servers": map[string]any{
			"loom_humanloop": map[string]any{
				"command": b.exec.binSelf,
				"args": []string{
					"humanloop-mcp",
					humanloop.EndpointArg(ep),
					strconv.Itoa(b.exec.maxQuestions),
				},
			},
		},
	},
}
```

If `thread/resume` rejects this config, close the server, remove the temp dir, and return `agentbackend.ErrSessionWorkerUnavailable` before any turn starts.

- [ ] **Step 5: Implement turn/start**

The worker `runTurn` closure should call:

```go
params := map[string]any{
	"threadId": w.sessionID,
	"cwd":      w.workDir,
	"input": []map[string]string{{
		"type": "text",
		"text": "User answered: " + prompt,
	}},
}
```

Important: the prompt must match the existing `RunResume` user-turn semantics.

- [ ] **Step 6: Route app-server human input requests**

For the first implementation, support humanloop through the MCP server above and wait for one IPC payload per turn in parallel with app-server notifications. Convert payloads to `agentbackend.Result.AwaitingUser` exactly like `executor.RunResume` does:

```go
awaiting = &executorpkg.AskUserPayload{
	Kind:     p.Kind,
	Question: p.Question,
	Options:  p.Options,
	Context:  p.Context,
	Intent:   p.Intent,
	Target:   p.Target,
	Reason:   p.Reason,
}
```

If app-server exposes first-class approval requests in notifications before the MCP bridge is proven, do not map them as a replacement for humanloop in this PR. Keep the MCP parity invariant first.

Concurrency constraint for this first implementation: app-server workers may run concurrently for different sessions, but humanloop MCP parity is only considered proven when the app-server routes MCP calls through the per-thread `thread/resume` config used to create that worker. If a local smoke test shows the single app-server process treats `mcp_servers.loom_humanloop` as process-global rather than thread-scoped, keep `NewSessionWorker` returning `ErrSessionWorkerUnavailable` for app-server mode and continue using `RunResume`. Do not ship a hot-worker path that can route an approval from one session to another worker.

- [ ] **Step 7: Run tests**

Run:

```bash
go test ./pkg/agentbackend/codex
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/appserver_manager.go \
  multi-agent/pkg/agentbackend/codex/appserver_worker.go \
  multi-agent/pkg/agentbackend/codex/appserver_worker_test.go
git commit -m "feat(codex): wire app-server session workers"
```

---

### Task 8: Fallback and No-Duplicate-Execution Tests

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`
- Modify: `multi-agent/internal/commander/handler_test.go`

- [ ] **Step 1: Write failing tests for accepted-turn errors**

Add to `multi-agent/pkg/agentbackend/codex/appserver_worker_test.go`:

```go
func TestCodexSessionWorkerDoesNotReturnUnavailableAfterAcceptedTurn(t *testing.T) {
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(ctx context.Context, prompt string, emit func(appServerRPCMessage)) error {
			emit(appServerNotification("turn/started", `{"threadId":"thr-1","turn":{"id":"turn-1","status":"running"}}`))
			emit(appServerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return errors.New("stream disconnected")
		},
	}
	_, err := w.Run(context.Background(), "prompt", &appServerTestSink{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("accepted turn error must not be unavailable: %v", err)
	}
}

func TestCodexSessionWorkerDeltaMeansAccepted(t *testing.T) {
	w := &codexSessionWorker{
		sessionID: "thr-1",
		workDir:   t.TempDir(),
		runTurn: func(ctx context.Context, prompt string, emit func(appServerRPCMessage)) error {
			emit(appServerNotification("item/agentMessage/delta", `{"threadId":"thr-1","turnId":"turn-1","itemId":"i1","delta":"partial"}`))
			return errors.New("stream disconnected after delta")
		},
	}
	_, err := w.Run(context.Background(), "prompt", &appServerTestSink{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("delta proves execution started; must not return unavailable: %v", err)
	}
}
```

Add to `multi-agent/internal/commander/handler_test.go`:

```go
func TestHandler_DoesNotFallbackAfterWorkerStartedExecution(t *testing.T) {
	var resumeCalls atomic.Int32
	worker := &fakeSessionWorker{healthy: true}
	worker.runFn = func(context.Context, string, executor.Sink) (executor.Result, error) {
		return executor.Result{}, errors.New("accepted turn failed")
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", Kind: agentbackend.KindClaude}, nil, nil
		},
		workerFn: func(context.Context, agentbackend.Session) (agentbackend.SessionWorker, error) {
			return worker, nil
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			resumeCalls.Add(1)
			return executor.Result{}, nil
		},
	}}
	_, err := h.SessionTurn(context.Background(), "s1", "prompt", &captureSink{})
	if err == nil {
		t.Fatal("expected worker error")
	}
	if resumeCalls.Load() != 0 {
		t.Fatalf("RunResume calls=%d want 0", resumeCalls.Load())
	}
}
```

- [ ] **Step 2: Run tests**

Run:

```bash
go test ./pkg/agentbackend/codex ./internal/commander
```

Expected: PASS after Task 6/7 behavior is correct.

- [ ] **Step 3: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/appserver_worker_test.go \
  multi-agent/internal/commander/handler_test.go
git commit -m "test(codex): lock worker fallback invariants"
```

---

### Task 9: Codex App-Server Smoke Test

**Files:**
- Create: `multi-agent/pkg/agentbackend/codex/appserver_smoke_test.go`

- [ ] **Step 1: Add opt-in smoke test**

Create `multi-agent/pkg/agentbackend/codex/appserver_smoke_test.go`:

```go
package codex

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestCodexAppServerSmoke(t *testing.T) {
	if os.Getenv("LOOM_CODEX_APPSERVER_SMOKE") != "1" {
		t.Skip("set LOOM_CODEX_APPSERVER_SMOKE=1 to run against local codex app-server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wb := b.(agentbackend.SessionWorkerBackend)
	_, err = wb.NewSessionWorker(ctx, agentbackend.Session{ID: "non-existent-smoke-thread", WorkingDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected unavailable or resume failure for synthetic thread")
	}
}
```

This smoke test proves startup/probe behavior without requiring a real persisted thread. A follow-up prod test can create a real Codex thread and resume it when the test environment has authenticated Codex state.

- [ ] **Step 2: Run normal tests**

Run:

```bash
go test ./pkg/agentbackend/codex
```

Expected: PASS with smoke skipped.

- [ ] **Step 3: Run optional smoke locally**

Run only on a machine with working Codex auth:

```bash
LOOM_CODEX_APPSERVER_SMOKE=1 go test ./pkg/agentbackend/codex -run TestCodexAppServerSmoke -count=1 -v
```

Expected: PASS if Codex app-server starts and returns a controlled unavailable/resume error, not a process crash or hang.

- [ ] **Step 4: Run optional humanloop routing smoke before enabling hot workers**

After a real thread-based smoke test exists in `tests/prod_test`, run two concurrent app-server worker turns that both trigger `ask_user` and assert each `AwaitingUser` payload returns to the matching session ID. If routing cannot be proven, leave app-server worker unavailable and keep falling back to `RunResume`.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/appserver_smoke_test.go
git commit -m "test(codex): add opt-in app-server smoke test"
```

---

### Task 10: Final Verification

**Files:**
- No code files unless earlier tasks expose failures.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./pkg/agentbackend ./pkg/agentbackend/codex ./internal/commander ./internal/commanderhub ./internal/driver ./internal/config ./cmd/driver-agent ./cmd/slave-agent
```

Expected: PASS.

- [ ] **Step 2: Run full repo tests**

Run:

```bash
go test ./...
```

Expected: PASS. If existing unrelated tests fail, capture the failing package, test name, and error text before deciding whether to fix or report.

- [ ] **Step 3: Verify no generated schema artifacts were accidentally added**

Run:

```bash
git status --short
git diff --stat
```

Expected: only intentional source/test files are modified. `/tmp/loom-codex-schema-plan` must not be committed.

- [ ] **Step 4: Commit any final fixups**

If `git status --short` shows no changes after Task 9, skip this step. If a verification fix changed files, add only those files and commit with:

```bash
git commit -m "fix(codex): polish app-server worker integration"
```

---

## Self-Review

- Spec coverage:
  - Runtime context parity: Tasks 2, 3, 7.
  - Existing `RunResume` consistency: Task 2.
  - Backend-neutral `status_code`: Task 1.
  - Off-mode unchanged behavior: Task 3.
  - App-server stdio JSON-RPC and protocol names: Tasks 4, 5, 7, 9.
  - No duplicate execution fallback invariant: Tasks 6, 8.
  - Humanloop MCP parity: Tasks 2, 7, and Task 9's routing smoke gate.
  - Capability parsing parity: Task 6.
  - Healthy worker interface and cache semantics: Tasks 5, 6, 8.
  - `starting` status code maps to existing queued state because the current turn-state model has no separate `starting` state.
- Placeholder scan: no placeholder markers or underspecified steps remain.
- Type consistency:
  - `WorkerMode` is a `string` field on both config carriers.
  - `StatusSink.WriteStatus(statusCode, text string)` is structural and does not require commander to import codex.
  - `codexSessionWorker` implements `Run`, `Close`, and `Healthy`.
  - `workerBackend` is the only Codex type implementing `SessionWorkerBackend` in app-server mode.
