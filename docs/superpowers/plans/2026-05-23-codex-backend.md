# Codex backend support — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce `pkg/agentbackend` as a pluggable coding-agent layer with Claude (existing behavior) and Codex (new) implementations, rename `claude_permissions` skill to `permissions`, and surface `--agent claude|codex` in driver/slave bootstrap + install scripts and docs.

**Architecture:** Refactor first (Phase 1, zero behavior change, ports the existing Claude logic into the new package), then add the Codex backend (Phase 2), then wire deploy templates (Phase 3), then end-to-end via docker-compose (Phase 4), then docs (Phase 5).

**Tech Stack:** Go 1.22, gopkg.in/yaml.v3 (existing), `github.com/BurntSushi/toml` (new dep, Codex `.codex/config.toml`), bash bootstrap/install scripts, docker-compose smoke tests, markdown docs.

**Spec:** [docs/superpowers/specs/2026-05-23-codex-backend-design.md](../specs/2026-05-23-codex-backend-design.md)

## File Structure

**Created (Phase 1 – abstraction):**
- `pkg/agentbackend/backend.go` — `Backend`, `LLMRunner`, `PermissionsStore` interfaces, `State`, `Patch`, `Task`/`Sink`/`Result` re-exports
- `pkg/agentbackend/config.go` — top-level `Config { Kind, Claude, Codex }` plus `Claude`/`Codex` structs
- `pkg/agentbackend/factory.go` — `New(Config) (Backend, error)` dispatch
- `pkg/agentbackend/capability.go` — `splitCapability` helper (shared by both backends)
- `pkg/agentbackend/claude/executor.go` — moved from `internal/executor/claude.go`
- `pkg/agentbackend/claude/llm.go` — moved from `internal/planner/planner.go::runClaude`
- `pkg/agentbackend/claude/permissions.go` — moved from `internal/claudeperm/store.go`
- `pkg/agentbackend/claude/presets.go` — preset expansion table (was in claudeperm)
- `pkg/agentbackend/claude/detect.go` — `Detect` (which + creds presence)
- `pkg/agentbackend/claude/*_test.go` — moved/ported tests

**Created (Phase 2 – codex):**
- `pkg/agentbackend/codex/executor.go` — `CodexExecutor` (parses `codex exec --json` NDJSON)
- `pkg/agentbackend/codex/llm.go` — `codex exec` text wrapper
- `pkg/agentbackend/codex/permissions.go` — TOML read/write of `<workdir>/.codex/config.toml`
- `pkg/agentbackend/codex/presets.go` — preset → TOML transform
- `pkg/agentbackend/codex/detect.go` — `which codex` + auth probe
- `pkg/agentbackend/codex/*_test.go` — table-driven tests, NDJSON fixture
- `pkg/agentbackend/codex/testdata/codex_exec.ndjson` — captured `codex exec --json` transcript

**Created (Phase 3 – deploy):**
- `deploy/linux/driver/codex-mcp.toml.template` — sibling to `.mcp.json.template`
- `deploy/linux/driver/prompts-codex/AGENTS.md` — Codex equivalent of multiagent skill instructions
- `deploy/linux/driver/prompts-codex/scaffold-mcp.md`, `mcp-acceptance.md` — optional Codex prompt files

**Created (Phase 5 – docs):**
- `deploy/agent-backends.md` — 双语 backend comparison + permissions JSON examples

**Modified:**
- `internal/config/config.go` — add `Agent`, `Codex`; preserve `Claude`; new defaults
- `internal/driver/config.go` — same
- `internal/planner/planner.go` — drop `runClaude`, accept injected `LLMRunner`
- `internal/capabilitydoc/doc.go` — `which` list driven by backend kind
- `cmd/slave-agent/main.go` — build backend, route `chat` + `permissions` through it; alias old skill name
- `cmd/driver-agent/main.go` — build backend, inject `backend.LLM()` into planner
- `deploy/linux/{driver,slave}/bootstrap.sh` — `--agent` flag, per-backend asset download + config render
- `deploy/linux/{driver,slave}/install.sh` — same
- `deploy/linux/{driver,slave}/config.yaml.template` — `agent.kind` + `codex:` block
- `deploy/linux/compose-test/docker-compose.yml` — new `driver-codex` service
- `deploy/linux/compose-test/entrypoint-driver.sh` — split into claude/codex variants
- `deploy/README.md`, `deploy/linux/{driver,slave}/README.md` — `--agent` docs
- `README.md`, `README.en.md` — opening framing + parallel one-liners
- `go.mod` — add `github.com/BurntSushi/toml`

**Deleted:**
- `internal/executor/claude.go`
- `internal/executor/claude_permissions.go`
- `internal/claudeperm/` (package)

---

## Phase 1 — Land the abstraction (claude-only, zero behavior change)

Phase exit: full test suite passes, `cmd/slave-agent` and `cmd/driver-agent` build, smoke-test deploy still works, no production behavior changed. **Do not start Phase 2 until Phase 1 is green.**

### Task 1: Create empty `pkg/agentbackend` skeleton with interfaces

**Files:**
- Create: `multi-agent/pkg/agentbackend/backend.go`
- Create: `multi-agent/pkg/agentbackend/config.go`
- Create: `multi-agent/pkg/agentbackend/backend_test.go`

- [ ] **Step 1: Write compile-test verifying interfaces are referenceable**

```go
// multi-agent/pkg/agentbackend/backend_test.go
package agentbackend

import "testing"

func TestInterfacesCompile(t *testing.T) {
    var _ Backend       = (*nilBackend)(nil)
    var _ LLMRunner     = (*nilLLM)(nil)
    var _ PermissionsStore = (*nilPerm)(nil)
}

type nilBackend struct{}
func (nilBackend) Kind() Kind                                                          { return KindClaude }
func (nilBackend) Run(_ ctxArg, _ Task, _ Sink) (Result, error)                        { return Result{}, nil }
func (nilBackend) LLM() LLMRunner                                                      { return nilLLM{} }
func (nilBackend) Permissions() PermissionsStore                                       { return nilPerm{} }
func (nilBackend) Detect(_ ctxArg) error                                               { return nil }

type nilLLM  struct{}
func (nilLLM) Run(_ ctxArg, _ string) (string, error) { return "", nil }

type nilPerm struct{}
func (nilPerm) Get(_ ctxArg)            (State, error) { return State{}, nil }
func (nilPerm) Patch(_ ctxArg, _ Patch) (State, error) { return State{}, nil }

// ctxArg is a local alias so this test file doesn't need a context import while the
// real backend.go below pulls context.Context in directly.
type ctxArg = interface{}
```

- [ ] **Step 2: Run test to verify it fails (no types exist yet)**

```
cd multi-agent && go test ./pkg/agentbackend/...
```

Expected: FAIL with "undefined: Backend" / "undefined: Kind" etc.

- [ ] **Step 3: Implement `backend.go`**

```go
// multi-agent/pkg/agentbackend/backend.go
package agentbackend

import (
    "context"

    "github.com/yourorg/multi-agent/internal/executor"
)

type Kind string

const (
    KindClaude Kind = "claude"
    KindCodex  Kind = "codex"
)

// Re-exports so callers can depend on agentbackend only.
type (
    Task   = executor.Task
    Sink   = executor.Sink
    Result = executor.Result
)

type Backend interface {
    Kind() Kind
    Run(ctx context.Context, t Task, sink Sink) (Result, error)
    LLM() LLMRunner
    Permissions() PermissionsStore
    Detect(ctx context.Context) error
}

type LLMRunner interface {
    Run(ctx context.Context, prompt string) (string, error)
}

type PermissionsStore interface {
    Get(ctx context.Context) (State, error)
    Patch(ctx context.Context, p Patch) (State, error)
}

type State struct {
    Backend Kind     `json:"backend"`
    Path    string   `json:"path"`
    Mode    string   `json:"mode,omitempty"`
    Allow   []string `json:"allow,omitempty"`
    Deny    []string `json:"deny,omitempty"`
}

type Patch struct {
    Presets     []string `json:"presets,omitempty"`
    AllowAdd    []string `json:"allow_add,omitempty"`
    AllowRemove []string `json:"allow_remove,omitempty"`
    DenyAdd     []string `json:"deny_add,omitempty"`
    DenyRemove  []string `json:"deny_remove,omitempty"`
    Mode        string   `json:"mode,omitempty"`
}
```

- [ ] **Step 4: Implement `config.go`**

```go
// multi-agent/pkg/agentbackend/config.go
package agentbackend

type Config struct {
    Kind   Kind         `yaml:"kind"`
    Claude ClaudeConfig `yaml:"-"`
    Codex  CodexConfig  `yaml:"-"`
}

type ClaudeConfig struct {
    Bin       string
    WorkDir   string
    ExtraArgs []string
}

type CodexConfig struct {
    Bin       string
    WorkDir   string
    ExtraArgs []string
}
```

- [ ] **Step 5: Fix backend_test.go to use real context, run, expect PASS**

Replace `ctxArg = interface{}` with `context.Context` import in the test, run:

```
cd multi-agent && go test ./pkg/agentbackend/...
```

Expected: PASS (one test).

- [ ] **Step 6: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/
git commit -m "feat(agentbackend): introduce Backend interface skeleton"
```

### Task 2: Move `internal/claudeperm` into `pkg/agentbackend/claude`

**Files:**
- Create: `multi-agent/pkg/agentbackend/claude/permissions.go`
- Create: `multi-agent/pkg/agentbackend/claude/presets.go`
- Create: `multi-agent/pkg/agentbackend/claude/permissions_test.go`
- Delete (after migration): `multi-agent/internal/claudeperm/`

- [ ] **Step 1: Copy `internal/claudeperm/store.go` to `pkg/agentbackend/claude/permissions.go`, rename package**

Open `multi-agent/internal/claudeperm/store.go`, copy entire contents to `multi-agent/pkg/agentbackend/claude/permissions.go`, change `package claudeperm` → `package claude`. Move the preset-expansion table (`presets` map and `expandPreset` if present, or the inline switch in `Patch`) into a sibling `presets.go`.

The exported types `Store`, `State`, `Patch` keep their names (now `claude.Store`, `claude.State`, `claude.Patch`).

- [ ] **Step 2: Add `*Store` adapter satisfying `agentbackend.PermissionsStore`**

Append to `permissions.go`:

```go
import (
    "context"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
    st, err := s.Read()
    if err != nil {
        return agentbackend.State{}, err
    }
    return agentbackend.State{
        Backend: agentbackend.KindClaude,
        Path:    st.Path,
        Allow:   st.Allow,
        Deny:    st.Deny,
    }, nil
}

func (s *Store) Patch(_ context.Context, p agentbackend.Patch) (agentbackend.State, error) {
    if p.Mode != "" {
        return agentbackend.State{}, fmt.Errorf("claude backend does not accept Patch.Mode (codex-only)")
    }
    native := Patch{
        AllowPresets: p.Presets,
        AllowAdd:     p.AllowAdd,
        AllowRemove:  p.AllowRemove,
        DenyAdd:      p.DenyAdd,
        DenyRemove:   p.DenyRemove,
    }
    st, err := s.PatchNative(native)
    if err != nil {
        return agentbackend.State{}, err
    }
    return agentbackend.State{
        Backend: agentbackend.KindClaude,
        Path:    st.Path,
        Allow:   st.Allow,
        Deny:    st.Deny,
    }, nil
}
```

Rename the original `Patch` receiver method to `PatchNative` to avoid signature clash with the new `Patch(ctx, agentbackend.Patch)`.

- [ ] **Step 3: Copy `internal/claudeperm/store_test.go` to `permissions_test.go`, change package, update receivers**

Tests should reference `Store.PatchNative` for the existing behavior tests and add **one new test** verifying the new `Patch(ctx, agentbackend.Patch)` adapter:

```go
func TestStorePatchAdapterRejectsModeField(t *testing.T) {
    store := NewStore(t.TempDir())
    _, err := store.Patch(context.Background(), agentbackend.Patch{Mode: "full-access"})
    if err == nil || !strings.Contains(err.Error(), "Mode") {
        t.Fatalf("expected Mode-rejection error, got %v", err)
    }
}

func TestStorePatchAdapterPassesPresets(t *testing.T) {
    store := NewStore(t.TempDir())
    st, err := store.Patch(context.Background(), agentbackend.Patch{Presets: []string{"file_write"}})
    if err != nil {
        t.Fatal(err)
    }
    if !equal(st.Allow, []string{"Edit", "Read", "Write"}) {
        t.Fatalf("allow=%v", st.Allow)
    }
    if st.Backend != agentbackend.KindClaude {
        t.Fatalf("backend=%v", st.Backend)
    }
}
```

- [ ] **Step 4: Run tests — expect PASS**

```
cd multi-agent && go test ./pkg/agentbackend/claude/...
```

Expected: existing tests pass (renamed to `PatchNative`) + 2 new adapter tests pass.

- [ ] **Step 5: Repoint the one consumer of `internal/claudeperm`**

```
cd multi-agent && grep -rn "internal/claudeperm" .
```

Should list `internal/executor/claude_permissions.go` (which we'll delete in Task 8). Don't repoint yet — `claude_permissions.go` is doomed; we'll let it keep its import until then. (If grep finds any other consumer, that's a surprise — stop and report.)

- [ ] **Step 6: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/claude/
git commit -m "feat(agentbackend/claude): port claudeperm into backend package"
```

### Task 3: Move `internal/executor/claude.go` into `pkg/agentbackend/claude`

**Files:**
- Create: `multi-agent/pkg/agentbackend/claude/executor.go`
- Create: `multi-agent/pkg/agentbackend/capability.go`
- Create: `multi-agent/pkg/agentbackend/claude/executor_test.go`

- [ ] **Step 1: Extract `splitCapability` into shared file**

Create `multi-agent/pkg/agentbackend/capability.go`:

```go
package agentbackend

import "strings"

// SplitCapability extracts the trailing "=== CAPABILITY ===" epilogue.
// Returns the summary (everything before the marker) and the change description
// (the trimmed text after the marker), with "NO_CAPABILITY_CHANGE" normalized
// to empty.
func SplitCapability(s string) (summary, change string) {
    const sep = "=== CAPABILITY ==="
    i := strings.LastIndex(s, sep)
    if i < 0 {
        return strings.TrimSpace(s), ""
    }
    summary = strings.TrimSpace(s[:i])
    change = strings.TrimSpace(s[i+len(sep):])
    if change == "NO_CAPABILITY_CHANGE" {
        change = ""
    }
    return
}

// CapabilityEpilogue is the system-prompt suffix that asks the agent to print
// the marker + a one-paragraph capability change description (or
// NO_CAPABILITY_CHANGE) on completion.
const CapabilityEpilogue = "\n\nWhen you finish, append a line `=== CAPABILITY ===` then 1-3 lines describing any persistent capability change to yourself. If none, write `NO_CAPABILITY_CHANGE`."
```

- [ ] **Step 2: Copy `internal/executor/claude.go` to `pkg/agentbackend/claude/executor.go`, refactor**

```go
// multi-agent/pkg/agentbackend/claude/executor.go
package claude

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os/exec"
    "strings"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type executor struct {
    cfg agentbackend.ClaudeConfig
    env []string
}

func newExecutor(cfg agentbackend.ClaudeConfig, env []string) *executor {
    return &executor{cfg: cfg, env: env}
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
    args := append([]string{
        "--print",
        "--output-format=stream-json",
        "--verbose",
        "--append-system-prompt", agentbackend.CapabilityEpilogue,
    }, e.cfg.ExtraArgs...)

    cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
    cmd.Dir = e.cfg.WorkDir
    cmd.Env = append(cmd.Environ(), e.env...)

    stdin, err := cmd.StdinPipe()
    if err != nil { return agentbackend.Result{}, err }
    stdout, err := cmd.StdoutPipe()
    if err != nil { return agentbackend.Result{}, err }
    var stderrBuf strings.Builder
    cmd.Stderr = &stderrBuf

    if err := cmd.Start(); err != nil { return agentbackend.Result{}, err }

    go func() {
        defer stdin.Close()
        if t.SystemContext != "" {
            io.WriteString(stdin, t.SystemContext+"\n\n")
        }
        io.WriteString(stdin, t.Prompt)
    }()

    var lastText strings.Builder
    sc := bufio.NewScanner(stdout)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
    for sc.Scan() {
        line := sc.Bytes()
        var msg struct {
            Type    string `json:"type"`
            Message struct {
                Content []struct {
                    Type string `json:"type"`
                    Text string `json:"text"`
                } `json:"content"`
            } `json:"message"`
        }
        if err := json.Unmarshal(line, &msg); err != nil {
            continue
        }
        if msg.Type != "assistant" { continue }
        for _, c := range msg.Message.Content {
            if c.Type != "text" { continue }
            sink.Write("chunk", c.Text)
            lastText.WriteString(c.Text)
        }
    }

    if err := cmd.Wait(); err != nil {
        defer sink.Close()
        if ctx.Err() == context.DeadlineExceeded {
            return agentbackend.Result{}, fmt.Errorf("timeout")
        }
        tail := stderrBuf.String()
        if len(tail) > 4096 { tail = tail[len(tail)-4096:] }
        return agentbackend.Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
    }

    full := lastText.String()
    summary, change := agentbackend.SplitCapability(full)
    if change != "" { sink.Write("capability", change) }
    sink.Close()
    return agentbackend.Result{Summary: summary, CapabilityChange: change}, nil
}
```

- [ ] **Step 3: Add a Backend impl that wires executor + LLM + permissions**

Create `multi-agent/pkg/agentbackend/claude/backend.go`:

```go
package claude

import (
    "context"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type Backend struct {
    cfg      agentbackend.ClaudeConfig
    env      []string
    exec     *executor
    perm     *Store
    llm      *llmRunner
}

func New(cfg agentbackend.ClaudeConfig, env []string) *Backend {
    return &Backend{
        cfg:  cfg,
        env:  env,
        exec: newExecutor(cfg, env),
        perm: NewStore(cfg.WorkDir),
        llm:  newLLM(cfg, env),
    }
}

func (b *Backend) Kind() Kind                                         { return agentbackend.KindClaude }
func (b *Backend) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
    return b.exec.Run(ctx, t, sink)
}
func (b *Backend) LLM() agentbackend.LLMRunner                        { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore         { return b.perm }
func (b *Backend) Detect(ctx context.Context) error                   { return detect(ctx, b.cfg.Bin) }

type Kind = agentbackend.Kind
```

Note: `llm.go` and `detect.go` come in the next two tasks; until then `claude/backend.go` won't compile. That's expected — Task 4 lands before we attempt to build.

- [ ] **Step 4: Port the test fixture**

Create `multi-agent/pkg/agentbackend/claude/executor_test.go` mirroring whatever exists in `internal/executor/claude_test.go` (if present) or a fresh minimal happy-path test using a fake `claude` binary on PATH (a shell script written to `t.TempDir()` that emits a single `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` line then exits 0).

```go
package claude

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
    chunks  []string
    closed  bool
}
func (c *captureSink) Write(kind, text string) { c.chunks = append(c.chunks, kind+":"+text) }
func (c *captureSink) Close()                  { c.closed = true }

func TestExecutorParsesAssistantText(t *testing.T) {
    dir := t.TempDir()
    fakeBin := filepath.Join(dir, "claude")
    script := "#!/usr/bin/env bash\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello world\"}]}}'\n"
    if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
        t.Fatal(err)
    }
    b := New(agentbackend.ClaudeConfig{Bin: fakeBin, WorkDir: dir}, nil)
    sink := &captureSink{}
    res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
    if err != nil { t.Fatal(err) }
    if res.Summary != "hello world" {
        t.Fatalf("summary=%q want %q", res.Summary, "hello world")
    }
    if !sink.closed { t.Fatal("sink not closed") }
}
```

- [ ] **Step 5: Repoint `internal/executor/claude.go` consumers — leave the file alone for now**

`internal/executor/claude.go` stays in place this task; we'll delete it in Task 8 after the slave wiring is rerouted. We just need it to keep compiling. `grep -rn "executor.NewClaudeExecutor" multi-agent` should list at most `cmd/slave-agent/main.go`.

- [ ] **Step 6: Don't run tests yet — wait until LLM lands in Task 4**

(`claude/backend.go` references `newLLM` and `detect` which don't exist yet. Build will fail; that's OK — Task 4 and Task 5 finish the package.)

- [ ] **Step 7: Commit (broken-build commit on a feature branch is acceptable mid-refactor)**

```bash
cd multi-agent && git add pkg/agentbackend/
git commit -m "feat(agentbackend/claude): port chat executor + capability helper"
```

### Task 4: Port planner's `runClaude` into `pkg/agentbackend/claude/llm.go`

**Files:**
- Create: `multi-agent/pkg/agentbackend/claude/llm.go`
- Create: `multi-agent/pkg/agentbackend/claude/llm_test.go`

- [ ] **Step 1: Write the failing test first**

```go
// multi-agent/pkg/agentbackend/claude/llm_test.go
package claude

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestLLMRunnerEchoesStdin(t *testing.T) {
    dir := t.TempDir()
    fakeBin := filepath.Join(dir, "claude")
    script := "#!/usr/bin/env bash\nread line\nprintf '%s\\n' \"$line\"\n"
    if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
        t.Fatal(err)
    }
    cfg := agentbackend.ClaudeConfig{Bin: fakeBin, ExtraArgs: nil}
    llm := newLLM(cfg, nil)
    out, err := llm.Run(context.Background(), "ping")
    if err != nil { t.Fatal(err) }
    if out != "ping" {
        t.Fatalf("out=%q want %q", out, "ping")
    }
}
```

- [ ] **Step 2: Run to verify it fails (newLLM undefined)**

```
cd multi-agent && go test ./pkg/agentbackend/claude/...
```

Expected: FAIL with "undefined: newLLM"

- [ ] **Step 3: Implement llm.go (port of planner.runClaude)**

```go
// multi-agent/pkg/agentbackend/claude/llm.go
package claude

import (
    "context"
    "fmt"
    "os/exec"
    "strings"
    "time"

    "github.com/yourorg/multi-agent/internal/progress"
    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

const llmIdleTimeout = 90 * time.Second

type llmRunner struct {
    cfg agentbackend.ClaudeConfig
    env []string
}

func newLLM(cfg agentbackend.ClaudeConfig, env []string) *llmRunner {
    return &llmRunner{cfg: cfg, env: env}
}

// Run honors a deadline carried in ctx; if none, defaults to 60s.
func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
    var stderrBuf strings.Builder
    var out []byte
    commandDone := make(chan struct{})
    timeout := 60 * time.Second
    if d, ok := ctx.Deadline(); ok {
        if rem := time.Until(d); rem > 0 { timeout = rem }
    }
    err := progress.RunWithHeartbeat(ctx, progress.Config{
        Interval:    15 * time.Second,
        IdleTimeout: llmIdleTimeout,
        HardTimeout: timeout,
        Message:     "claude llm still running",
    }, func(runCtx context.Context) error {
        defer close(commandDone)
        args := append([]string{"--print"}, r.cfg.ExtraArgs...)
        cmd := exec.CommandContext(runCtx, r.cfg.Bin, args...)
        cmd.Env = append(cmd.Environ(), r.env...)
        cmd.Stdin = strings.NewReader(stdinPrompt)
        cmd.Stderr = &stderrBuf
        var err error
        out, err = cmd.Output()
        return err
    })
    <-commandDone
    if err != nil {
        if strings.Contains(err.Error(), "hard timeout") {
            return "", fmt.Errorf("planner timeout after %s", timeout)
        }
        tail := stderrBuf.String()
        if len(tail) > 4096 { tail = tail[len(tail)-4096:] }
        return "", fmt.Errorf("claude llm exit: %v: %s", err, tail)
    }
    return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
cd multi-agent && go test ./pkg/agentbackend/claude/...
```

Expected: PASS (executor + llm + permissions tests).

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/claude/llm.go pkg/agentbackend/claude/llm_test.go
git commit -m "feat(agentbackend/claude): port planner runClaude into backend"
```

### Task 5: Implement `detect.go`

**Files:**
- Create: `multi-agent/pkg/agentbackend/claude/detect.go`
- Create: `multi-agent/pkg/agentbackend/claude/detect_test.go`

- [ ] **Step 1: Write the test**

```go
// multi-agent/pkg/agentbackend/claude/detect_test.go
package claude

import (
    "context"
    "os"
    "path/filepath"
    "testing"
)

func TestDetectFailsWhenBinMissing(t *testing.T) {
    err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such-bin"))
    if err == nil { t.Fatal("expected error") }
}

func TestDetectPassesWhenBinExists(t *testing.T) {
    dir := t.TempDir()
    bin := filepath.Join(dir, "claude")
    if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := detect(context.Background(), bin); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}
```

- [ ] **Step 2: Run, expect FAIL**

```
cd multi-agent && go test ./pkg/agentbackend/claude/...
```

Expected: FAIL with "undefined: detect".

- [ ] **Step 3: Implement**

```go
// multi-agent/pkg/agentbackend/claude/detect.go
package claude

import (
    "context"
    "fmt"
    "os/exec"
)

func detect(ctx context.Context, bin string) error {
    if bin == "" { bin = "claude" }
    if _, err := exec.LookPath(bin); err != nil {
        return fmt.Errorf("claude bin %q not on PATH: %w", bin, err)
    }
    // Best-effort: a `claude --version` should exit 0 quickly. Don't gate on
    // login state — `claude login` may not have been run yet.
    _ = ctx
    return nil
}
```

If the bin is an absolute path, `exec.LookPath` returns it; if relative, it must be on PATH. The function explicitly accepts both because tests pass absolute temp paths.

- [ ] **Step 4: Run tests, expect PASS**

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/claude/detect.go pkg/agentbackend/claude/detect_test.go
git commit -m "feat(agentbackend/claude): add Detect probe"
```

### Task 6: Wire factory

**Files:**
- Create: `multi-agent/pkg/agentbackend/factory.go`
- Create: `multi-agent/pkg/agentbackend/factory_test.go`

- [ ] **Step 1: Write the test**

```go
// multi-agent/pkg/agentbackend/factory_test.go
package agentbackend_test

import (
    "testing"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestNewDefaultsToClaude(t *testing.T) {
    b, err := agentbackend.New(agentbackend.Config{Claude: agentbackend.ClaudeConfig{Bin: "claude"}}, nil)
    if err != nil { t.Fatal(err) }
    if b.Kind() != agentbackend.KindClaude {
        t.Fatalf("kind=%v want claude", b.Kind())
    }
}

func TestNewCodexNotYetImplemented(t *testing.T) {
    _, err := agentbackend.New(agentbackend.Config{Kind: agentbackend.KindCodex}, nil)
    if err == nil {
        t.Fatal("expected codex-not-implemented error")
    }
}

func TestNewRejectsUnknownKind(t *testing.T) {
    _, err := agentbackend.New(agentbackend.Config{Kind: "frobnitz"}, nil)
    if err == nil { t.Fatal("expected error") }
}
```

- [ ] **Step 2: Run, expect FAIL**

```
cd multi-agent && go test ./pkg/agentbackend/...
```

Expected: FAIL with "undefined: New".

- [ ] **Step 3: Implement factory.go**

```go
// multi-agent/pkg/agentbackend/factory.go
package agentbackend

import (
    "fmt"

    "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
)

func New(cfg Config, env []string) (Backend, error) {
    kind := cfg.Kind
    if kind == "" { kind = KindClaude }
    switch kind {
    case KindClaude:
        return claude.New(cfg.Claude, env), nil
    case KindCodex:
        return nil, fmt.Errorf("agentbackend: codex backend not yet implemented (Phase 2)")
    default:
        return nil, fmt.Errorf("agentbackend: unknown kind %q", kind)
    }
}
```

- [ ] **Step 4: Run, expect PASS**

```
cd multi-agent && go test ./pkg/agentbackend/...
```

Expected: PASS (all factory + claude tests).

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/factory.go pkg/agentbackend/factory_test.go
git commit -m "feat(agentbackend): factory dispatch (codex stub)"
```

### Task 7: Add `Agent` + `Codex` config fields, BC defaults

**Files:**
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/internal/driver/config.go`
- Modify: `multi-agent/internal/config/config_test.go` (if exists) or create

- [ ] **Step 1: Write the BC test**

Open `multi-agent/internal/config/config_test.go` (create if missing). Add:

```go
func TestLoadDefaultsAgentKindToClaude(t *testing.T) {
    path := filepath.Join(t.TempDir(), "c.yaml")
    os.WriteFile(path, []byte(`
server: { url: x, name: y }
credentials: {}
claude: { bin: claude }
discovery: { display_name: a }
`), 0o600)
    c, err := Load(path)
    if err != nil { t.Fatal(err) }
    if c.Agent.Kind != "claude" {
        t.Fatalf("Agent.Kind=%q want claude", c.Agent.Kind)
    }
    if c.Codex.Bin != "codex" {
        t.Fatalf("Codex.Bin default=%q want codex", c.Codex.Bin)
    }
}

func TestLoadAgentKindCodexFillsCodexDefaults(t *testing.T) {
    path := filepath.Join(t.TempDir(), "c.yaml")
    os.WriteFile(path, []byte(`
server: { url: x, name: y }
credentials: {}
agent: { kind: codex }
codex: { workdir: /tmp/cx }
discovery: { display_name: a }
`), 0o600)
    c, err := Load(path)
    if err != nil { t.Fatal(err) }
    if c.Agent.Kind != "codex" { t.Fatalf("kind=%q", c.Agent.Kind) }
    if c.Codex.Bin  != "codex" { t.Fatalf("bin=%q",  c.Codex.Bin)  }
    if c.Planner.Bin != "codex" { t.Fatalf("planner.bin=%q (should follow codex)", c.Planner.Bin) }
}
```

- [ ] **Step 2: Run, expect FAIL**

```
cd multi-agent && go test ./internal/config/...
```

Expected: FAIL with "undefined: Agent" / "Codex"

- [ ] **Step 3: Add types + defaults**

In `multi-agent/internal/config/config.go`:

```go
type Config struct {
    Server      Server               `yaml:"server"`
    Credentials Credentials          `yaml:"credentials"`
    Agent       Agent                `yaml:"agent"`        // NEW
    Claude      Claude               `yaml:"claude"`
    Codex       Codex                `yaml:"codex"`        // NEW
    MCPServers  map[string]MCPServer `yaml:"mcp_servers"`
    Discovery   Discovery            `yaml:"discovery"`
    Planner     Planner              `yaml:"planner"`
    Fanout      Fanout               `yaml:"fanout"`
    Resources   *Resources           `yaml:"resources,omitempty"`
    Observer    Observer             `yaml:"observer,omitempty"`
}

type Agent struct {
    Kind string `yaml:"kind"` // "claude" | "codex"; default claude
}

type Codex struct {
    Bin     string   `yaml:"bin"`
    WorkDir string   `yaml:"workdir"`
    Args    []string `yaml:"extra_args"`
}
```

In `Load` (after `c.Claude.Bin == ""` block):

```go
if c.Agent.Kind == "" { c.Agent.Kind = "claude" }
if c.Codex.Bin == ""  { c.Codex.Bin = "codex"  }
if c.Codex.WorkDir == "" { c.Codex.WorkDir = c.Claude.WorkDir }
// Planner.Bin fallback driven by agent kind:
if c.Planner.Bin == "" {
    switch c.Agent.Kind {
    case "codex": c.Planner.Bin = c.Codex.Bin
    default:      c.Planner.Bin = c.Claude.Bin
    }
}
```

Apply the analogous additions in `multi-agent/internal/driver/config.go`.

- [ ] **Step 4: Run, expect PASS**

```
cd multi-agent && go test ./internal/config/... ./internal/driver/...
```

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add internal/config/ internal/driver/
git commit -m "feat(config): add agent.kind + codex block, BC defaults"
```

### Task 8: Rewire slave + driver to use backend; rename skill; delete old paths

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Modify: `multi-agent/cmd/driver-agent/main.go`
- Modify: `multi-agent/internal/planner/planner.go`
- Modify: `multi-agent/internal/capabilitydoc/doc.go`
- Delete: `multi-agent/internal/executor/claude.go`
- Delete: `multi-agent/internal/executor/claude_permissions.go`
- Delete: `multi-agent/internal/claudeperm/` (whole dir)

- [ ] **Step 1: Refactor planner.go to accept injected LLM**

```go
// multi-agent/internal/planner/planner.go
package planner

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"
    "time"

    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/multi-agent/internal/config"
    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type ProgressFunc func(ctx context.Context, phase, message string, elapsed time.Duration)

type Planner struct {
    cfg      config.Planner
    llm      agentbackend.LLMRunner
    progress ProgressFunc
}

func New(cfg config.Planner, llm agentbackend.LLMRunner) *Planner {
    return &Planner{cfg: cfg, llm: llm}
}

func (p *Planner) WithProgress(fn ProgressFunc) *Planner {
    cp := *p
    cp.progress = fn
    return &cp
}

// Route / Plan / Reduce — change every `p.runClaude(ctx, X)` call to `p.runLLM(ctx, X)`.
func (p *Planner) runLLM(ctx context.Context, stdinPrompt string) (string, error) {
    timeout := time.Duration(p.cfg.TimeoutSec) * time.Second
    if timeout <= 0 { timeout = 60 * time.Second }
    runCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    return p.llm.Run(runCtx, stdinPrompt)
}
```

Delete the old `runClaude` body and the `os/exec`/`progress` imports it required. `progress` lives in the LLM impl now.

- [ ] **Step 2: Update planner tests**

Replace any test that built a Planner with `planner.New(cfg)` to use `planner.New(cfg, fakeLLM{})` where `fakeLLM` implements `agentbackend.LLMRunner` returning canned outputs.

- [ ] **Step 3: Refactor `cmd/slave-agent/main.go`**

Find the existing chunk:

```go
claudeExec := executor.NewClaudeExecutor(executor.ClaudeConfig{
    Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, Args: cfg.Claude.Args,
})
```

Replace with:

```go
backend, err := agentbackend.New(agentbackend.Config{
    Kind: agentbackend.Kind(cfg.Agent.Kind),
    Claude: agentbackend.ClaudeConfig{Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, ExtraArgs: cfg.Claude.Args},
    Codex:  agentbackend.CodexConfig{Bin: cfg.Codex.Bin,  WorkDir: cfg.Codex.WorkDir,  ExtraArgs: cfg.Codex.Args},
}, nil)
if err != nil { log.Fatalf("agentbackend: %v", err) }
```

Update `routes[""] = claudeExec` to `routes[""] = executorAdapter(backend)` where `executorAdapter` is a tiny inline closure satisfying `executor.Executor` by delegating to `backend.Run`.

Find the existing `if hasSkill(... "claude_permissions") { routes["claude_permissions"] = NewClaudePermissionsExecutor(...) }` block. Replace with:

```go
if hasSkill(cfg.Discovery.Skills, "permissions") || hasSkill(cfg.Discovery.Skills, "claude_permissions") {
    if hasSkill(cfg.Discovery.Skills, "claude_permissions") {
        log.Printf("WARN: 'claude_permissions' skill name is deprecated; rename to 'permissions' in discovery.skills")
    }
    permExec := newPermissionsExecutor(backend.Permissions(), func(ctx context.Context, reason string) error {
        refreshCapabilities(ctx, reason)
        return tn.PublishCard(ctx)
    })
    routes["permissions"]         = permExec
    routes["claude_permissions"]  = permExec // BC alias
}
```

`newPermissionsExecutor` is a new helper in the slave-agent package (next step).

- [ ] **Step 4: Add `permissions_executor.go` adapter in `cmd/slave-agent/`**

```go
// multi-agent/cmd/slave-agent/permissions_executor.go
package main

import (
    "context"
    "encoding/json"
    "fmt"

    "github.com/yourorg/multi-agent/internal/executor"
    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type permissionsExecutor struct {
    store   agentbackend.PermissionsStore
    refresh func(context.Context, string) error
}

func newPermissionsExecutor(s agentbackend.PermissionsStore, refresh func(context.Context, string) error) *permissionsExecutor {
    return &permissionsExecutor{store: s, refresh: refresh}
}

type permRequest struct {
    Op string `json:"op"`
    agentbackend.Patch
}

func (e *permissionsExecutor) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
    defer sink.Close()
    var req permRequest
    if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
        return executor.Result{}, fmt.Errorf("permissions prompt must be JSON: %w", err)
    }
    var (
        state agentbackend.State
        err   error
    )
    switch req.Op {
    case "get":
        state, err = e.store.Get(ctx)
    case "patch":
        state, err = e.store.Patch(ctx, req.Patch)
        if err == nil && e.refresh != nil {
            err = e.refresh(ctx, "permission update")
        }
    default:
        return executor.Result{}, fmt.Errorf("unsupported permissions op %q", req.Op)
    }
    if err != nil { return executor.Result{}, err }
    body, _ := json.Marshal(state)
    sink.Write("chunk", string(body))
    return executor.Result{Summary: string(body)}, nil
}
```

- [ ] **Step 5: Refactor `cmd/driver-agent/main.go`**

First grep for the exact construction site:

```bash
grep -n "planner.New" multi-agent/cmd/driver-agent/main.go
```

There should be exactly one call. Above it, insert backend construction; replace the call to inject the LLM. Final shape:

```go
backend, err := agentbackend.New(agentbackend.Config{
    Kind:   agentbackend.Kind(cfg.Agent.Kind),
    Claude: agentbackend.ClaudeConfig{Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, ExtraArgs: cfg.Claude.Args},
    Codex:  agentbackend.CodexConfig{Bin: cfg.Codex.Bin,  WorkDir: cfg.Codex.WorkDir,  ExtraArgs: cfg.Codex.Args},
}, nil)
if err != nil { log.Fatalf("agentbackend: %v", err) }
p := planner.New(cfg.Planner, backend.LLM())
```

Driver config shape: `multi-agent/internal/driver/config.go` re-exports the same `agentconfig.Claude`/`agentconfig.Codex`/`agentconfig.Agent` types added in Task 7. If the field accessor differs (e.g., `cfg.AgentCfg` instead of `cfg.Agent`), use whatever name Task 7 chose — the names introduced in Task 7 are the source of truth.

- [ ] **Step 6: Update `internal/capabilitydoc/doc.go`**

Find:

```go
names := []string{"claude", "python3", "node", "npm", "go", "docker"}
if cfg != nil && cfg.Claude.Bin != "" {
    names = append(names, cfg.Claude.Bin)
}
```

Replace with:

```go
names := []string{"python3", "node", "npm", "go", "docker"}
if cfg != nil {
    switch cfg.Agent.Kind {
    case "codex":
        names = append(names, "codex")
        if cfg.Codex.Bin != "" && cfg.Codex.Bin != "codex" {
            names = append(names, cfg.Codex.Bin)
        }
    default:
        names = append(names, "claude")
        if cfg.Claude.Bin != "" && cfg.Claude.Bin != "claude" {
            names = append(names, cfg.Claude.Bin)
        }
    }
}
```

- [ ] **Step 7: Delete the old files**

```bash
cd multi-agent && rm -rf internal/executor/claude.go internal/executor/claude_permissions.go internal/claudeperm/
```

- [ ] **Step 8: Build and run full test suite**

```bash
cd multi-agent && go build ./...
cd multi-agent && go test ./...
```

Expected: all green. If a test referenced `executor.NewClaudeExecutor` or `claudeperm` types directly, update it to use the backend equivalent. If a test in `internal/planner/` constructed Planner with one arg, fix it to pass a fake `LLMRunner`.

- [ ] **Step 9: Local smoke — slave still chats**

```bash
cd multi-agent && go build -o /tmp/slave-agent ./cmd/slave-agent
LOOM_OBSERVER_URL=... LOOM_API_KEY=... /tmp/slave-agent /path/to/existing/slave/config.yaml &
# from a separate driver session, dispatch a `chat` task — expect same behavior as before
```

If you don't have an observer handy, skip this and rely on Phase 4 compose-test.

- [ ] **Step 10: Commit**

```bash
cd multi-agent && git add -A
git commit -m "refactor: route slave + driver through agentbackend; rename claude_permissions skill to permissions"
```

### Task 9: Run full suite one more time, tag Phase 1 done

- [ ] **Step 1: Full test + vet**

```bash
cd multi-agent && go vet ./... && go test ./...
```

Expected: all green.

- [ ] **Step 2: Commit any vet fixes (likely none)**

If green and clean, no commit needed. Move on to Phase 2.

---

## Phase 2 — Codex backend

Phase exit: `pkg/agentbackend/codex/...` is fully implemented and tested; factory accepts `Kind=codex`; `cmd/slave-agent` and `cmd/driver-agent` boot under `agent.kind: codex` without crashing (no e2e yet — that's Phase 4).

### Task 10: Pin `codex exec --json` event schema, capture fixture

**Files:**
- Create: `multi-agent/pkg/agentbackend/codex/testdata/codex_exec.ndjson`
- Create: `multi-agent/pkg/agentbackend/codex/testdata/README.md`

- [ ] **Step 1: Run codex against a trivial prompt, capture stream**

```bash
# Requires: codex installed and authenticated (codex login) on the dev box.
codex exec --json --full-access 'reply with the single word: pong' \
  > multi-agent/pkg/agentbackend/codex/testdata/codex_exec.ndjson
```

If the version available doesn't accept `--full-access` here, use `--ask-for-approval never --sandbox danger-full-access`. Whatever flags make `codex exec` non-interactive on your version.

- [ ] **Step 2: Sanity-check the capture**

```bash
head -3 multi-agent/pkg/agentbackend/codex/testdata/codex_exec.ndjson
wc -l  multi-agent/pkg/agentbackend/codex/testdata/codex_exec.ndjson
```

You should see structured NDJSON events. Note the exact `type` (or `event`) field name and the path to the assistant text. Common shapes:
- `{"type":"agent_message","message":{"content":[{"type":"text","text":"pong"}]}}`
- `{"event":"response.completed","item":{"role":"assistant","content":[{"type":"output_text","text":"pong"}]}}`

- [ ] **Step 3: Document the schema you observed**

Create `multi-agent/pkg/agentbackend/codex/testdata/README.md`:

```markdown
# codex testdata

Captured from `codex exec --json --full-access 'reply with the single word: pong'`
on <DATE>, codex version `<output of `codex --version`>`.

Event of interest: lines with `type == "<actual value>"` carry the assistant
text at `<actual JSON path>`. The executor's NDJSON parser keys off this.

If a future codex version changes the schema, re-capture and update the parser.
```

Fill in `<actual value>` / `<actual JSON path>` / `<DATE>` based on what you observed.

- [ ] **Step 4: Commit the fixture + notes**

```bash
cd multi-agent && git add pkg/agentbackend/codex/testdata/
git commit -m "test(agentbackend/codex): capture codex exec --json fixture"
```

### Task 11: Implement Codex executor (chat skill)

**Files:**
- Create: `multi-agent/pkg/agentbackend/codex/executor.go`
- Create: `multi-agent/pkg/agentbackend/codex/executor_test.go`

- [ ] **Step 1: Write the test driving off the fixture**

```go
// multi-agent/pkg/agentbackend/codex/executor_test.go
package codex

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct{ chunks []string; closed bool }
func (c *captureSink) Write(_, text string) { c.chunks = append(c.chunks, text) }
func (c *captureSink) Close()               { c.closed = true }

func TestExecutorReplaysFixture(t *testing.T) {
    fix, err := os.ReadFile("testdata/codex_exec.ndjson")
    if err != nil { t.Fatal(err) }

    dir := t.TempDir()
    fakeBin := filepath.Join(dir, "codex")
    script := "#!/usr/bin/env bash\ncat >/dev/null\ncat <<'EOF'\n" + string(fix) + "\nEOF\n"
    if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
        t.Fatal(err)
    }
    b := New(agentbackend.CodexConfig{Bin: fakeBin, WorkDir: dir}, nil)
    sink := &captureSink{}
    res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
    if err != nil { t.Fatal(err) }
    if !sink.closed { t.Fatal("sink not closed") }
    if res.Summary == "" { t.Fatal("empty summary") }
    // The fixture asks for "pong"; assert we extracted it (substring match in case
    // the model prefixed/suffixed something).
    if !contains(res.Summary, "pong") {
        t.Fatalf("summary %q does not contain pong", res.Summary)
    }
}

func contains(s, sub string) bool {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub { return true }
    }
    return false
}
```

- [ ] **Step 2: Run, expect FAIL (executor doesn't exist)**

```
cd multi-agent && go test ./pkg/agentbackend/codex/...
```

- [ ] **Step 3: Implement executor.go**

```go
// multi-agent/pkg/agentbackend/codex/executor.go
package codex

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os/exec"
    "strings"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type executor struct {
    cfg agentbackend.CodexConfig
    env []string
}

func newExecutor(cfg agentbackend.CodexConfig, env []string) *executor {
    return &executor{cfg: cfg, env: env}
}

// Match the event shape pinned in Task 10. If your codex version emits
// {"event":"...","item":...} instead of {"type":"...","message":...},
// adjust this struct and the type/event check below.
type codexEvent struct {
    Type    string `json:"type"`
    Message struct {
        Content []struct {
            Type string `json:"type"`
            Text string `json:"text"`
        } `json:"content"`
    } `json:"message"`
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
    args := []string{"exec", "--json", "--full-access", "-"}
    if len(e.cfg.ExtraArgs) > 0 {
        args = append(args, e.cfg.ExtraArgs...)
    }

    cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
    cmd.Dir = e.cfg.WorkDir
    cmd.Env = append(cmd.Environ(), e.env...)

    stdin, err  := cmd.StdinPipe()
    if err != nil { return agentbackend.Result{}, err }
    stdout, err := cmd.StdoutPipe()
    if err != nil { return agentbackend.Result{}, err }
    var stderrBuf strings.Builder
    cmd.Stderr = &stderrBuf

    if err := cmd.Start(); err != nil { return agentbackend.Result{}, err }

    go func() {
        defer stdin.Close()
        prompt := t.Prompt + agentbackend.CapabilityEpilogue
        if t.SystemContext != "" {
            io.WriteString(stdin, t.SystemContext+"\n\n")
        }
        io.WriteString(stdin, prompt)
    }()

    var lastText strings.Builder
    sc := bufio.NewScanner(stdout)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
    for sc.Scan() {
        line := sc.Bytes()
        var ev codexEvent
        if err := json.Unmarshal(line, &ev); err != nil { continue }
        // Schema-pin: change "agent_message" to whatever Task 10 observed.
        if ev.Type != "agent_message" { continue }
        for _, c := range ev.Message.Content {
            if c.Type != "text" { continue }
            sink.Write("chunk", c.Text)
            lastText.WriteString(c.Text)
        }
    }

    if err := cmd.Wait(); err != nil {
        defer sink.Close()
        if ctx.Err() == context.DeadlineExceeded {
            return agentbackend.Result{}, fmt.Errorf("timeout")
        }
        tail := stderrBuf.String()
        if len(tail) > 4096 { tail = tail[len(tail)-4096:] }
        return agentbackend.Result{}, fmt.Errorf("codex exit: %v: %s", err, tail)
    }

    full := lastText.String()
    summary, change := agentbackend.SplitCapability(full)
    if change != "" { sink.Write("capability", change) }
    sink.Close()
    return agentbackend.Result{Summary: summary, CapabilityChange: change}, nil
}
```

- [ ] **Step 4: Add stub `New(cfg, env)` exported constructor in codex package**

`backend.go` comes in Task 14. For now, drop a temporary one so the test compiles:

```go
// multi-agent/pkg/agentbackend/codex/backend_stub.go (delete in Task 14)
package codex

import "github.com/yourorg/multi-agent/pkg/agentbackend"

func New(cfg agentbackend.CodexConfig, env []string) *executor { return newExecutor(cfg, env) }
```

(The test calls `New(...).Run(...)`. After Task 14 we'll rename to a real `Backend` type and delete this stub file.)

- [ ] **Step 5: Run, expect PASS**

```
cd multi-agent && go test ./pkg/agentbackend/codex/...
```

If FAIL because the `type` field is something other than `agent_message`, update both `codexEvent` and the type check to match the README.md observation from Task 10.

- [ ] **Step 6: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/codex/
git commit -m "feat(agentbackend/codex): chat executor parsing exec --json NDJSON"
```

### Task 12: Implement Codex LLM runner (planner)

**Files:**
- Create: `multi-agent/pkg/agentbackend/codex/llm.go`
- Create: `multi-agent/pkg/agentbackend/codex/llm_test.go`

- [ ] **Step 1: Write the test**

```go
// multi-agent/pkg/agentbackend/codex/llm_test.go
package codex

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestLLMRunnerReturnsTrimmedStdout(t *testing.T) {
    dir := t.TempDir()
    fakeBin := filepath.Join(dir, "codex")
    script := "#!/usr/bin/env bash\ncat >/dev/null\nprintf '   pong   \\n\\n'\n"
    if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
        t.Fatal(err)
    }
    llm := newLLM(agentbackend.CodexConfig{Bin: fakeBin}, nil)
    out, err := llm.Run(context.Background(), "ping")
    if err != nil { t.Fatal(err) }
    if out != "pong" {
        t.Fatalf("out=%q want %q", out, "pong")
    }
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

```go
// multi-agent/pkg/agentbackend/codex/llm.go
package codex

import (
    "context"
    "fmt"
    "os/exec"
    "strings"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type llmRunner struct {
    cfg agentbackend.CodexConfig
    env []string
}

func newLLM(cfg agentbackend.CodexConfig, env []string) *llmRunner {
    return &llmRunner{cfg: cfg, env: env}
}

func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
    args := []string{"exec", "--full-access", "-"}
    if len(r.cfg.ExtraArgs) > 0 {
        args = append(args, r.cfg.ExtraArgs...)
    }
    cmd := exec.CommandContext(ctx, r.cfg.Bin, args...)
    cmd.Env = append(cmd.Environ(), r.env...)
    cmd.Stdin = strings.NewReader(stdinPrompt)
    var stderrBuf strings.Builder
    cmd.Stderr = &stderrBuf
    out, err := cmd.Output()
    if err != nil {
        tail := stderrBuf.String()
        if len(tail) > 4096 { tail = tail[len(tail)-4096:] }
        return "", fmt.Errorf("codex llm exit: %v: %s", err, tail)
    }
    return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/codex/llm.go pkg/agentbackend/codex/llm_test.go
git commit -m "feat(agentbackend/codex): LLM runner for planner"
```

### Task 13: Implement Codex permissions (TOML read/write)

**Files:**
- Modify: `multi-agent/go.mod`, `multi-agent/go.sum`
- Create: `multi-agent/pkg/agentbackend/codex/permissions.go`
- Create: `multi-agent/pkg/agentbackend/codex/presets.go`
- Create: `multi-agent/pkg/agentbackend/codex/permissions_test.go`

- [ ] **Step 1: Add toml dep**

```
cd multi-agent && go get github.com/BurntSushi/toml@v1.4.0
```

- [ ] **Step 2: Write the tests first**

```go
// multi-agent/pkg/agentbackend/codex/permissions_test.go
package codex

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestPermissionsGetMissingReturnsAskMode(t *testing.T) {
    s := NewStore(t.TempDir())
    st, err := s.Get(context.Background())
    if err != nil { t.Fatal(err) }
    if st.Mode != "ask" {
        t.Fatalf("Mode=%q want ask", st.Mode)
    }
    if st.Backend != agentbackend.KindCodex {
        t.Fatalf("Backend=%v", st.Backend)
    }
}

func TestPermissionsPatchFullAccess(t *testing.T) {
    dir := t.TempDir()
    s := NewStore(dir)
    st, err := s.Patch(context.Background(), agentbackend.Patch{Presets: []string{"full_access"}})
    if err != nil { t.Fatal(err) }
    if st.Mode != "full-access" {
        t.Fatalf("Mode=%q want full-access", st.Mode)
    }
    data, _ := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
    if !strings.Contains(string(data), `approval_policy = "never"`) {
        t.Fatalf("config.toml missing approval_policy line:\n%s", data)
    }
    if !strings.Contains(string(data), `sandbox_mode = "danger-full-access"`) {
        t.Fatalf("config.toml missing sandbox_mode line:\n%s", data)
    }
}

func TestPermissionsPatchFileWriteSetsWorkspaceWrite(t *testing.T) {
    s := NewStore(t.TempDir())
    st, err := s.Patch(context.Background(), agentbackend.Patch{Presets: []string{"file_write"}})
    if err != nil { t.Fatal(err) }
    if st.Mode != "workspace-write" {
        t.Fatalf("Mode=%q want workspace-write", st.Mode)
    }
}

func TestPermissionsPatchRejectsAllowAdd(t *testing.T) {
    s := NewStore(t.TempDir())
    _, err := s.Patch(context.Background(), agentbackend.Patch{AllowAdd: []string{"Bash(rm *)"}})
    if err == nil || !strings.Contains(err.Error(), "AllowAdd") {
        t.Fatalf("expected AllowAdd-rejection, got %v", err)
    }
}

func TestPermissionsPatchModePassthrough(t *testing.T) {
    s := NewStore(t.TempDir())
    st, err := s.Patch(context.Background(), agentbackend.Patch{Mode: "workspace-write"})
    if err != nil { t.Fatal(err) }
    if st.Mode != "workspace-write" {
        t.Fatalf("Mode=%q want workspace-write", st.Mode)
    }
}

func TestPermissionsPatchIdempotent(t *testing.T) {
    s := NewStore(t.TempDir())
    p := agentbackend.Patch{Presets: []string{"full_access"}}
    a, err := s.Patch(context.Background(), p); if err != nil { t.Fatal(err) }
    b, err := s.Patch(context.Background(), p); if err != nil { t.Fatal(err) }
    if a.Mode != b.Mode { t.Fatalf("not idempotent: %v vs %v", a, b) }
}
```

- [ ] **Step 3: Run, expect FAIL**

```
cd multi-agent && go test ./pkg/agentbackend/codex/...
```

- [ ] **Step 4: Implement permissions.go + presets.go**

```go
// multi-agent/pkg/agentbackend/codex/presets.go
package codex

// presetToMode picks the strongest mode any of the named presets implies.
// "ask" < "workspace-write" < "full-access".
func presetToMode(presets []string, current string) (mode string, ok bool) {
    rank := map[string]int{"ask": 0, "workspace-write": 1, "full-access": 2}
    cur := rank[current]
    if current == "" { cur = 0 }
    best := cur
    for _, p := range presets {
        switch p {
        case "file_write", "curl", "python":
            if rank["workspace-write"] > best { best = rank["workspace-write"] }
        case "full_access":
            if rank["full-access"] > best { best = rank["full-access"] }
        default:
            // unknown preset — caller will warn
        }
    }
    if best == cur {
        if current == "" { return "ask", true }
        return current, true
    }
    for name, r := range rank {
        if r == best { return name, true }
    }
    return "ask", false
}
```

```go
// multi-agent/pkg/agentbackend/codex/permissions.go
package codex

import (
    "context"
    "fmt"
    "os"
    "path/filepath"

    "github.com/BurntSushi/toml"
    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type Store struct{ workdir string }

func NewStore(workdir string) *Store { return &Store{workdir: workdir} }

func (s *Store) path() string { return filepath.Join(s.workdir, ".codex", "config.toml") }

type rawConfig struct {
    ApprovalPolicy string `toml:"approval_policy,omitempty"`
    SandboxMode    string `toml:"sandbox_mode,omitempty"`
    // Plus whatever else lives in the file; we only touch our two keys.
}

func (s *Store) read() (rawConfig, error) {
    var c rawConfig
    data, err := os.ReadFile(s.path())
    if errors.Is(err, os.ErrNotExist) { return c, nil }
    if err != nil { return c, err }
    if err := toml.Unmarshal(data, &c); err != nil { return c, err }
    return c, nil
}

func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
    c, err := s.read()
    if err != nil { return agentbackend.State{}, err }
    mode := codexMode(c)
    return agentbackend.State{
        Backend: agentbackend.KindCodex,
        Path:    s.path(),
        Mode:    mode,
    }, nil
}

func codexMode(c rawConfig) string {
    switch c.SandboxMode {
    case "danger-full-access":
        return "full-access"
    case "workspace-write":
        return "workspace-write"
    default:
        return "ask"
    }
}

func (s *Store) Patch(ctx context.Context, p agentbackend.Patch) (agentbackend.State, error) {
    if len(p.AllowAdd) > 0 || len(p.AllowRemove) > 0 ||
       len(p.DenyAdd)  > 0 || len(p.DenyRemove)  > 0 {
        return agentbackend.State{}, fmt.Errorf("codex backend does not accept Patch.AllowAdd/AllowRemove/DenyAdd/DenyRemove (claude-only); use Presets or Mode")
    }
    c, err := s.read()
    if err != nil { return agentbackend.State{}, err }

    cur := codexMode(c)
    var newMode string
    switch {
    case p.Mode != "":
        newMode = p.Mode
    case len(p.Presets) > 0:
        m, _ := presetToMode(p.Presets, cur)
        newMode = m
    default:
        newMode = cur
    }

    switch newMode {
    case "ask":
        c.SandboxMode    = ""
        c.ApprovalPolicy = ""
    case "workspace-write":
        c.SandboxMode    = "workspace-write"
        c.ApprovalPolicy = "on-request"
    case "full-access":
        c.SandboxMode    = "danger-full-access"
        c.ApprovalPolicy = "never"
    default:
        return agentbackend.State{}, fmt.Errorf("unknown codex mode %q", newMode)
    }

    if err := os.MkdirAll(filepath.Dir(s.path()), 0o700); err != nil {
        return agentbackend.State{}, err
    }
    f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
    if err != nil { return agentbackend.State{}, err }
    defer f.Close()
    if err := toml.NewEncoder(f).Encode(c); err != nil {
        return agentbackend.State{}, err
    }
    return s.Get(ctx)
}
```

Add the missing `import "errors"` at the top of permissions.go.

- [ ] **Step 5: Run, expect PASS**

```
cd multi-agent && go test ./pkg/agentbackend/codex/...
```

- [ ] **Step 6: Commit**

```bash
cd multi-agent && git add pkg/agentbackend/codex/ go.mod go.sum
git commit -m "feat(agentbackend/codex): permissions store backed by .codex/config.toml"
```

### Task 14: Implement Codex Detect + Backend assembly

**Files:**
- Create: `multi-agent/pkg/agentbackend/codex/detect.go`
- Create: `multi-agent/pkg/agentbackend/codex/backend.go`
- Delete: `multi-agent/pkg/agentbackend/codex/backend_stub.go`
- Modify: `multi-agent/pkg/agentbackend/factory.go`
- Create: `multi-agent/pkg/agentbackend/codex/detect_test.go`

- [ ] **Step 1: Write detect test**

```go
// multi-agent/pkg/agentbackend/codex/detect_test.go
package codex

import (
    "context"
    "os"
    "path/filepath"
    "testing"
)

func TestDetectFailsWhenBinMissing(t *testing.T) {
    if err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such")); err == nil {
        t.Fatal("expected error")
    }
}
func TestDetectPassesWhenBinExists(t *testing.T) {
    bin := filepath.Join(t.TempDir(), "codex")
    os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
    if err := detect(context.Background(), bin); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Implement detect.go**

```go
// multi-agent/pkg/agentbackend/codex/detect.go
package codex

import (
    "context"
    "fmt"
    "os/exec"
)

func detect(ctx context.Context, bin string) error {
    if bin == "" { bin = "codex" }
    if _, err := exec.LookPath(bin); err != nil {
        return fmt.Errorf("codex bin %q not on PATH: %w", bin, err)
    }
    _ = ctx
    return nil
}
```

- [ ] **Step 3: Implement backend.go**

```go
// multi-agent/pkg/agentbackend/codex/backend.go
package codex

import (
    "context"

    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type Backend struct {
    cfg  agentbackend.CodexConfig
    exec *executor
    perm *Store
    llm  *llmRunner
}

// New returns a fully-assembled Codex Backend. (Replaces the throwaway
// `New(...) *executor` stub from Task 11 — the executor_test.go calls
// still resolve to this new symbol because the signature returns a type
// that implements agentbackend.Backend; the test only calls .Run on it,
// which both types satisfy with the same shape.)
func New(cfg agentbackend.CodexConfig, env []string) *Backend {
    return &Backend{
        cfg:  cfg,
        exec: newExecutor(cfg, env),
        perm: NewStore(cfg.WorkDir),
        llm:  newLLM(cfg, env),
    }
}

func (b *Backend) Kind() agentbackend.Kind                                                            { return agentbackend.KindCodex }
func (b *Backend) Run(ctx context.Context, t agentbackend.Task, s agentbackend.Sink) (agentbackend.Result, error) {
    return b.exec.Run(ctx, t, s)
}
func (b *Backend) LLM() agentbackend.LLMRunner                                                        { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore                                         { return b.perm }
func (b *Backend) Detect(ctx context.Context) error                                                   { return detect(ctx, b.cfg.Bin) }
```

- [ ] **Step 4: Delete the stub**

```bash
rm multi-agent/pkg/agentbackend/codex/backend_stub.go
```

`executor_test.go` doesn't need to change — it already calls `New(cfg, env)`, which now resolves to the real Backend constructor (the stub is gone, so no symbol collision).

- [ ] **Step 5: Wire codex into factory**

In `pkg/agentbackend/factory.go`, replace the codex stub branch:

```go
import (
    ...
    "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)
...
case KindCodex:
    return codex.New(cfg.Codex, env), nil
```

Update `factory_test.go::TestNewCodexNotYetImplemented` → `TestNewCodexBuilds`:

```go
func TestNewCodexBuilds(t *testing.T) {
    b, err := agentbackend.New(agentbackend.Config{
        Kind:  agentbackend.KindCodex,
        Codex: agentbackend.CodexConfig{Bin: "codex"},
    }, nil)
    if err != nil { t.Fatal(err) }
    if b.Kind() != agentbackend.KindCodex { t.Fatalf("kind=%v", b.Kind()) }
}
```

- [ ] **Step 6: Run full backend tests**

```
cd multi-agent && go test ./pkg/agentbackend/...
```

Expected: all green.

- [ ] **Step 7: Boot a codex-mode slave (build only, no runtime)**

```bash
cd multi-agent && go build ./...
```

Expected: clean. If you have a codex-authenticated environment, you can render a minimal `agent.kind: codex` slave config.yaml and try `slave-agent /path/to/config.yaml` — it should at least come up to the observer-register step.

- [ ] **Step 8: Commit**

```bash
cd multi-agent && git add -A
git commit -m "feat(agentbackend/codex): assemble Backend + wire factory"
```

---

## Phase 3 — Deploy templates

Phase exit: bootstrap & install scripts accept `--agent codex` and lay down a working project / slave directory. Verified by hand (or by Phase 4's compose-test).

### Task 15: Slave `--agent` flag in bootstrap + install

**Files:**
- Modify: `multi-agent/deploy/linux/slave/bootstrap.sh`
- Modify: `multi-agent/deploy/linux/slave/install.sh`
- Modify: `multi-agent/deploy/linux/slave/config.yaml.template`

- [ ] **Step 1: Add `--agent` flag parsing in bootstrap.sh**

In the arg-parsing while loop, add:

```bash
    --agent)         AGENT="$2"; shift 2 ;;
```

Set default near the other env defaults:

```bash
AGENT="${LOOM_AGENT_KIND:-claude}"
```

Validate after the loop:

```bash
case "$AGENT" in claude|codex) ;; *) echo "ERROR: --agent must be claude or codex" >&2; exit 2 ;; esac
```

- [ ] **Step 2: Branch the npm install step**

Replace the `if ! command -v claude` block with:

```bash
if [[ "$AGENT" == "claude" ]]; then
  if ! command -v claude >/dev/null 2>&1; then
    if command -v npm >/dev/null 2>&1; then
      echo "==> installing claude code CLI (npm i -g @anthropic-ai/claude-code)"
      npm install -g @anthropic-ai/claude-code
    else
      echo "WARN: 'claude' not in PATH and 'npm' unavailable — install Node + 'npm i -g @anthropic-ai/claude-code'"
    fi
  fi
else
  if ! command -v codex >/dev/null 2>&1; then
    if command -v npm >/dev/null 2>&1; then
      echo "==> installing openai codex CLI (npm i -g @openai/codex)"
      npm install -g @openai/codex || echo "WARN: codex install failed (requires Node >= 22); install manually"
    else
      echo "WARN: 'codex' not in PATH and 'npm' unavailable — install Node >= 22 + 'npm i -g @openai/codex'"
    fi
  fi
fi
```

- [ ] **Step 3: Branch the rendered config.yaml**

Replace the existing `cat > "$LOOM_HOME/config.yaml" <<EOF` block with:

```bash
if [[ "$AGENT" == "claude" ]]; then
  AGENT_BLOCK=$(cat <<YAML
agent:
  kind: claude

claude:
  bin: claude
  workdir: $LOOM_HOME
  extra_args: []
YAML
)
  PERMISSIONS_SKILL_LINE="    - permissions"
else
  AGENT_BLOCK=$(cat <<YAML
agent:
  kind: codex

codex:
  bin: codex
  workdir: $LOOM_HOME
  extra_args: []
YAML
)
  PERMISSIONS_SKILL_LINE="    - permissions"
fi

cat > "$LOOM_HOME/config.yaml" <<EOF
server:
  url: https://agent.cs.ac.cn
  name: $NAME

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  short_id: ""

$AGENT_BLOCK

mcp_servers: {}

discovery:
  display_name: $NAME
  description: $DESC
  skills:
    - chat
    - bash
$PERMISSIONS_SKILL_LINE
    - register_mcp
    - file

planner:
  bin: ""
  timeout_sec: 300
  extra_args: []

fanout:
  max_concurrency: 1
  default_policy: best_effort
  policy_by_skill: {}

resources:
  cpu:
    cores: $CPU_CORES
    arch: $CPU_ARCH
  memory_gb: $MEMORY_GB
  tags:
${tag_lines%$'\n'}

observer:
  enabled: true
  url: $OBSERVER_URL
  workspace_id: $WORKSPACE_ID
  agent_id: $NAME
  api_key: "$API_KEY"
  token_state_path: $LOOM_HOME/observer.token
EOF
chmod 0600 "$LOOM_HOME/config.yaml"
```

Notice we always emit `permissions` (not `claude_permissions`). `planner.bin: ""` so the Go code defaults from agent.kind.

- [ ] **Step 4: Mirror changes in install.sh**

Apply the same three changes to `multi-agent/deploy/linux/slave/install.sh` (flag, dep install, config render). Reuse the same templating snippet.

Also update `multi-agent/deploy/linux/slave/config.yaml.template` (if `install.sh` uses sed-replace rather than heredoc) — add `__AGENT_KIND__`, `__AGENT_BLOCK__` placeholders and corresponding sed lines.

- [ ] **Step 5: Smoke render**

```bash
cd multi-agent/deploy/linux/slave
LOOM_OBSERVER_URL=http://x:8090 LOOM_API_KEY=test bash bootstrap.sh \
  --name slave-test-claude --release v0.0.1 || true
# inspect ~/.loom/slave-test-claude/config.yaml — should have agent.kind: claude

LOOM_OBSERVER_URL=http://x:8090 LOOM_API_KEY=test bash bootstrap.sh \
  --name slave-test-codex --agent codex --release v0.0.1 || true
# inspect ~/.loom/slave-test-codex/config.yaml — should have agent.kind: codex + codex: block
```

(The downloads will likely fail without a real release; that's fine — we're verifying the config render path.)

- [ ] **Step 6: Commit**

```bash
cd multi-agent && git add deploy/linux/slave/
git commit -m "feat(deploy/slave): --agent claude|codex flag in bootstrap + install"
```

### Task 16: Driver `--agent` flag + Codex MCP TOML template + AGENTS.md bundle

**Files:**
- Modify: `multi-agent/deploy/linux/driver/bootstrap.sh`
- Modify: `multi-agent/deploy/linux/driver/install.sh`
- Modify: `multi-agent/deploy/linux/driver/config.yaml.template`
- Create: `multi-agent/deploy/linux/driver/codex-mcp.toml.template`
- Create: `multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md`

- [ ] **Step 1: Create the TOML template**

```toml
# multi-agent/deploy/linux/driver/codex-mcp.toml.template
# Codex CLI MCP registration for the driver-agent.
# Codex loads project-scoped .codex/config.toml only in trusted directories;
# the first `codex` invocation in this dir will prompt to trust it.

[mcp_servers.driver]
command = "__PROJECT_DIR__/driver-agent"
args    = ["serve-mcp", "--config", "__PROJECT_DIR__/config.yaml"]
startup_timeout_sec = 30
tool_timeout_sec    = 120
enabled             = true
```

- [ ] **Step 2: Create AGENTS.md**

Translate the multiagent skill instructions from `tests/prod_test/driver/.claude/skills/multiagent/SKILL.md` (or wherever the source lives) into a single `AGENTS.md`. Codex reads `AGENTS.md` from the project root automatically.

```markdown
# multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md
# Multi-Agent Driver — Codex notes

This project hosts the **driver MCP server** registered as `mcp_servers.driver`
in `.codex/config.toml`. The MCP exposes one set of tools for routing tasks
to a fleet of slave agents on a shared observer.

## Core tools

- `mcp__driver__list_agents()` — list slave agents in the workspace
- `mcp__driver__dispatch(target_id, prompt, skill="chat")` — send a single task
- `mcp__driver__plan(prompt)` — let the planner produce a multi-node plan, then
  `mcp__driver__execute(plan_id)` to fan out

## When you start

1. Run `mcp__driver__list_agents` first — verify slaves are reachable.
2. If listing returns empty, the slaves haven't registered yet (or the
   observer api-key is wrong). Don't dispatch.

## Permissions skill

Slaves expose a `permissions` skill — use `mcp__driver__dispatch(target_id,
JSON_string, skill="permissions")` where the JSON is:

  - `{"op":"get"}` to read
  - `{"op":"patch","presets":["file_write"]}` to grant a preset
  - `{"op":"patch","mode":"workspace-write"}` (codex slaves) to set sandbox mode

Codex slaves reject `allow_add`/`deny_add` arrays (claude-only); claude slaves
reject `mode` (codex-only).
```

(Mirror the same content into separate `scaffold-mcp.md` / `mcp-acceptance.md` prompt files if you have time — optional in v1.)

- [ ] **Step 3: Add `--agent` parsing to bootstrap.sh and install.sh**

Same flag-parsing pattern as Task 15. Default `AGENT="${LOOM_AGENT_KIND:-claude}"`.

- [ ] **Step 4: Branch MCP registration in bootstrap.sh**

Replace the `cat > "$PROJECT/.mcp.json" <<EOF` block with:

```bash
if [[ "$AGENT" == "claude" ]]; then
  cat > "$PROJECT/.mcp.json" <<EOF
{
  "mcpServers": {
    "driver": {
      "command": "$PROJECT/driver-agent",
      "args": ["serve-mcp", "--config", "$PROJECT/config.yaml"]
    }
  }
}
EOF
else
  mkdir -p "$PROJECT/.codex"
  cat > "$PROJECT/.codex/config.toml" <<EOF
[mcp_servers.driver]
command = "$PROJECT/driver-agent"
args    = ["serve-mcp", "--config", "$PROJECT/config.yaml"]
startup_timeout_sec = 30
tool_timeout_sec    = 120
enabled             = true
EOF
fi
```

- [ ] **Step 5: Branch the skill / prompts bundle download**

```bash
if [[ "$AGENT" == "claude" ]]; then
  echo "==> downloading driver-skills.tar.gz"
  mkdir -p "$PROJECT/.claude/skills"
  tmp_tar="$(mktemp)"
  curl -fL --progress-bar -o "$tmp_tar" "$RELEASE_BASE/driver-skills.tar.gz"
  tar -xzf "$tmp_tar" -C "$PROJECT/.claude/skills/"
  rm -f "$tmp_tar"
else
  echo "==> downloading driver-codex-prompts.tar.gz"
  tmp_tar="$(mktemp)"
  curl -fL --progress-bar -o "$tmp_tar" "$RELEASE_BASE/driver-codex-prompts.tar.gz"
  tar -xzf "$tmp_tar" -C "$PROJECT/"
  rm -f "$tmp_tar"
fi
```

The codex tarball contains `AGENTS.md` and optional `.codex/prompts/*.md`. The release-build script (out of scope here — add a TODO note in `deploy/README.md`) tars `deploy/linux/driver/prompts-codex/` into the asset.

- [ ] **Step 6: Branch the rendered config.yaml**

Same pattern as Task 15 step 3, applied to driver config. Driver's `agent`+`claude`/`codex` blocks; `planner.bin: ""`.

- [ ] **Step 7: Branch the auth + launch hint at the bottom**

```bash
if [[ "$AGENT" == "claude" ]]; then
  cat <<EOF
==> launch:
      cd $PROJECT
      claude                  # approve the 'driver' MCP server on first prompt
EOF
else
  cat <<EOF
==> launch (Codex):
      cd $PROJECT
      codex                   # first run will prompt to trust this directory
                              # — required for project-scoped .codex/config.toml
      # then inside codex:    mcp__driver__list_agents
      # auth: \`codex login\` (chat subscription) or export OPENAI_API_KEY
EOF
fi
```

- [ ] **Step 8: Mirror in install.sh**

Apply Steps 4–7 analogously to `multi-agent/deploy/linux/driver/install.sh`. The `--skill-bundle` flag now takes a directory of skills (claude) or a single `prompts-codex/` directory (codex); detect by `--agent`.

- [ ] **Step 9: Smoke render**

```bash
cd /tmp && mkdir -p driver-codex-test && cd driver-codex-test
bash /root/multi-agent/multi-agent/deploy/linux/driver/install.sh \
  --project . \
  --name driver-test \
  --observer-url http://x:8090 \
  --api-key test \
  --agent codex \
  --bin /tmp/fake-driver-agent  # absent → expect missing-bin error, that's fine
```

Verify `.codex/config.toml` and `AGENTS.md` (from `--skill-bundle` autodetect or template copy) exist.

- [ ] **Step 10: Commit**

```bash
cd multi-agent && git add deploy/linux/driver/
git commit -m "feat(deploy/driver): --agent flag + codex MCP toml + AGENTS.md bundle"
```

---

## Phase 4 — Compose-test end-to-end

Phase exit: `docker-compose up` in `deploy/linux/compose-test/` brings up observer + claude-driver + codex-driver + one claude-slave, and `list_agents` from either driver returns the slave. The codex-driver path may be marked "manual verification" if codex auth in CI is impractical.

### Task 17: Add `driver-codex` service to docker-compose

**Files:**
- Modify: `multi-agent/deploy/linux/compose-test/docker-compose.yml`
- Modify: `multi-agent/deploy/linux/compose-test/entrypoint-driver.sh` (or create `entrypoint-driver-codex.sh`)
- Modify: `multi-agent/deploy/linux/compose-test/README.md`

- [ ] **Step 1: Add a sibling service**

In `docker-compose.yml`, copy the existing `driver` service definition and rename to `driver-codex`. Change:
- `command`: invoke `entrypoint-driver.sh --agent codex` (passed via env var or arg).
- `environment`: add `LOOM_AGENT_KIND=codex` and `OPENAI_API_KEY=${OPENAI_API_KEY:-skip}`.
- Don't make it a hard `depends_on` for the test if codex isn't authenticated — gate behind a `profiles: [codex]` so `docker compose up` skips it by default and `docker compose --profile codex up` runs it.

- [ ] **Step 2: Update entrypoint to honor `LOOM_AGENT_KIND`**

In `entrypoint-driver.sh`, change the install.sh invocation to pass `--agent "${LOOM_AGENT_KIND:-claude}"`.

- [ ] **Step 3: Document in README**

In `deploy/linux/compose-test/README.md`, add a section:

```markdown
## Codex driver (optional)

The `driver-codex` service is gated behind the `codex` profile. To run it:

    export OPENAI_API_KEY=sk-...      # or set up codex login in a mounted volume
    docker compose --profile codex up driver-codex

Without auth, codex exec returns an error; the service will crash-loop. That's
expected — the goal of this profile is to verify the deploy templates lay the
right files, not to make a paid API call in CI.
```

- [ ] **Step 4: Sanity run claude-only path**

```bash
cd multi-agent/deploy/linux/compose-test
docker compose up --build --abort-on-container-exit
```

Expected: same behavior as before (driver-claude + slave register; `list_agents` returns the slave). The new `driver-codex` should not start (no profile).

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add deploy/linux/compose-test/
git commit -m "test(compose): add codex-driver profile to e2e harness"
```

---

## Phase 5 — Docs

### Task 18: Deploy READMEs and top-level README

**Files:**
- Modify: `multi-agent/deploy/README.md`
- Modify: `multi-agent/deploy/linux/driver/README.md`
- Modify: `multi-agent/deploy/linux/slave/README.md`
- Modify: `README.md` (repo root)
- Modify: `README.en.md` (repo root)

- [ ] **Step 1: `deploy/README.md` — add codex one-liners**

In the Driver section, add a "Codex" subheader after "Driver" with the parallel bash invocation, and similarly for Slave. The opening paragraph should note that **drivers and slaves can each run on Claude Code or Codex CLI**, picked per-agent with `--agent claude|codex`. The "Path" table row for `linux/driver` should drop the "Claude Code project dir" framing in favor of "coding-agent project dir".

Use this exact snippet for the Driver Codex one-liner (EN, then 中文):

````markdown
EN — Codex variant — drop the same driver project but write `.codex/config.toml`
instead of `.mcp.json`, and lay an `AGENTS.md` Codex can read on first launch:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
  --name driver-myhost --agent codex
```

中文 —— Codex 版本，写入 `.codex/config.toml` 并附 `AGENTS.md`：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
  --name driver-myhost --agent codex
```

启动前：`codex login`（订阅）或 `export OPENAI_API_KEY=...`；首次 `cd` 进项目
后跑一次 `codex` 让它把这个目录加入 trust list（否则项目级 `.codex/config.toml`
不会加载）。然后 `./driver-agent register --config ./config.yaml` 走 device-code。
````

Slave: same pattern, but the auth note is `codex login` or `OPENAI_API_KEY` and the launch is whatever the user normally does (foreground / systemd).

- [ ] **Step 2: `deploy/linux/driver/README.md` — add `--agent` to the flag reference**

Insert a new row in the Flag reference table:

```markdown
| `--agent CLI` | `claude` | `claude` or `codex`. Codex mode writes `.codex/config.toml` instead of `.mcp.json`, drops `AGENTS.md` + optional `.codex/prompts/`, and renders `agent.kind: codex` in `config.yaml`. |
```

Add a "Codex notes" subsection after "Project layout after install":

```markdown
## Codex notes

- Codex loads project-scoped `.codex/config.toml` **only in trusted
  directories**. The first `codex` invocation in the project dir prompts
  to trust it — approve once.
- Codex CLI auth: `codex login` (with a ChatGPT subscription) or
  `export OPENAI_API_KEY=...`. The driver-agent itself still does its
  own observer device-code OAuth via `./driver-agent register`.
- Codex's permissions model is coarser than Claude's. The `permissions`
  skill exposes presets (`file_write`, `full_access`, ...) and the
  three sandbox modes (`ask`, `workspace-write`, `full-access`).
```

- [ ] **Step 3: `deploy/linux/slave/README.md` — `--agent` flag + backend choice**

Same `--agent` row in the flag reference. Note that the choice is per-slave (one slave process = one backend) and that under `--agent codex` the chat skill spawns `codex exec --json` instead of `claude --print --output-format=stream-json`.

- [ ] **Step 4: Top-level `README.md` (中文) + `README.en.md`**

Opening paragraph: clarify that the system supports **mixed-fleet** deployment — each driver and slave independently runs under Claude Code or Codex CLI; agents from both backends share one observer / workspace.

In the "One-line deploy" section, add `--agent codex` examples next to the existing claude ones (mirror the deploy/README.md snippet, kept brief).

- [ ] **Step 5: Commit**

```bash
cd multi-agent && git add deploy/README.md deploy/linux/driver/README.md deploy/linux/slave/README.md
cd .. && git add README.md README.en.md
git commit -m "docs: surface --agent claude|codex in deploy and top-level READMEs"
```

### Task 19: Write `deploy/agent-backends.md`

**Files:**
- Create: `multi-agent/deploy/agent-backends.md`

- [ ] **Step 1: Author the comparison + JSON examples**

```markdown
# Agent backends — Claude Code vs Codex CLI

This project hosts a pluggable coding-agent layer at `pkg/agentbackend/`.
Driver and slave processes pick a backend via `agent.kind` in their
`config.yaml` (default `claude`). Both backends implement the same three
interfaces: `Run` (chat skill), `LLMRunner` (planner), `PermissionsStore`
(permissions skill).

## Side-by-side

| Aspect | claude | codex |
|---|---|---|
| Binary | `claude` (`npm i -g @anthropic-ai/claude-code`) | `codex` (`npm i -g @openai/codex`, Node ≥ 22) |
| Auth | `claude login` or `ANTHROPIC_API_KEY` | `codex login` (subscription) or `OPENAI_API_KEY` |
| Chat invocation | `claude --print --output-format=stream-json` | `codex exec --json --full-access` |
| Driver MCP wiring | `.mcp.json` (project root) | `.codex/config.toml` (project root, trusted-dir only) |
| Skill / prompt bundle | `.claude/skills/{multiagent,...}/` | `AGENTS.md` (+ optional `.codex/prompts/*.md`) |
| Permissions store | `<workdir>/.claude/settings.local.json` (`allow`/`deny` arrays) | `<workdir>/.codex/config.toml` (`sandbox_mode` + `approval_policy`) |
| Permissions grain | per-tool strings like `Bash(curl *)` | three sandbox modes: ask / workspace-write / full-access |

## The `permissions` skill

Both backends accept the same JSON envelope. Patches carry uniform `presets`
plus backend-specific overrides; sending the wrong override returns an error
with the backend name.

### Get current state

Request (any backend):

```json
{"op": "get"}
```

Response (claude):

```json
{
  "backend": "claude",
  "path": "/home/x/.loom/slave-myhost/.claude/settings.local.json",
  "allow": ["Edit", "Read", "Write"],
  "deny": ["Bash(rm *)"]
}
```

Response (codex):

```json
{
  "backend": "codex",
  "path": "/home/x/.loom/slave-myhost/.codex/config.toml",
  "mode": "workspace-write"
}
```

### Patch with a uniform preset

Both backends honor `presets`:

```json
{"op": "patch", "presets": ["file_write"]}
```

- claude → adds `Read`, `Write`, `Edit` to `allow`
- codex → bumps `sandbox_mode` to `workspace-write`

### Backend-specific patches

Claude-native (rejected by codex):

```json
{"op": "patch", "allow_add": ["Bash(curl *)"], "deny_add": ["Bash(rm *)"]}
```

Codex-native (rejected by claude):

```json
{"op": "patch", "mode": "full-access"}
```

## Mixing backends in one fleet

A single observer / workspace hosts agents of either backend. Driver-A on
claude can dispatch to slave-B on codex (and vice versa) — the chat output
is plain text either way, and the permissions skill exposes a uniform
preset vocabulary.

One process is one backend. To run both on the same host, run two slave
processes with distinct `--name` and distinct `LOOM_HOME` directories.

## See also

- Design spec: `docs/superpowers/specs/2026-05-23-codex-backend-design.md`
- Bootstrap one-liners: `deploy/README.md`
```

中文段（如果你的双语风格是中文在同一文件，把上面表格 + 关键段落翻译过来追加；
或单独建 `deploy/agent-backends.zh.md`）。

- [ ] **Step 2: Commit**

```bash
cd multi-agent && git add deploy/agent-backends.md
git commit -m "docs: agent-backends comparison + permissions JSON examples"
```

---

## Done

Final check:

- [ ] `cd multi-agent && go vet ./... && go test ./...` — all green
- [ ] `cd multi-agent/deploy/linux/compose-test && docker compose up --build --abort-on-container-exit` — claude path passes
- [ ] `docker compose --profile codex up driver-codex` — at least starts up + reaches register step (skip if no `OPENAI_API_KEY` in CI)
- [ ] Top-level `README.md` opening paragraph mentions Codex
- [ ] `deploy/README.md` has Codex one-liner for both driver and slave
- [ ] `claude_permissions` skill name still works but logs deprecation warning
- [ ] `internal/executor/claude.go`, `internal/executor/claude_permissions.go`, `internal/claudeperm/` are gone
