# opencode Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add anomalyco/opencode as a 3rd agent backend. Slave-side: full `agentbackend.Backend` implementation (`Run`/`RunResume`/`LLM`/`Permissions`/`Detect`) following the codex package as the closest reference. Driver-side: deploy templates so `opencode.json` (shared by opencode CLI + desktop) gets a `driver` MCP server pointing at our `driver-agent serve-mcp`.

**Architecture:** PR #18 (issue #15) already factored the backend registry so adding a kind = one new sub-package + two `import _` lines + one `Kind` constant. This is the first PR exercising that promise. opencode CLI is invoked as `opencode run [prompt] --format json` (nd-JSON event stream); humanloop MCP is injected by writing a temp `opencode.json` and pointing `OPENCODE_CONFIG` env at it (opencode-specific — different from claude's `--mcp-config` flag and codex's `-c mcp_servers.X` inline overrides). Driver-side: a single `opencode.json` template lands at `~/.config/opencode/opencode.json` (or `%APPDATA%\opencode\opencode.json`), consumed by both CLI and desktop.

**Tech Stack:** Go stdlib + existing `pkg/agentbackend` registry from PR #18 + existing `internal/humanloop` IPC. No new Go deps.

**Spec:** `docs/superpowers/specs/2026-06-14-opencode-backend-design.md` (committed `19be4bf`).

**Branch:** `worktree-add-opencode-backend`. Worktree: `/root/multi-agent/.claude/worktrees/add-opencode-backend/multi-agent/`.

**Master-path freeze:** Same as PR #18 — `cmd/master-agent/`, `internal/orchestrator/`, `internal/orchestration/` untouched. Verify after Task 8:

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
# expected: empty
```

---

## Implementer pre-flight (do BEFORE Task 4)

The spec flagged two unknowns. Resolve them **once** at the top of Task 4 — they unblock executor.go:

1. **Capture opencode event nd-JSON schema**

   ```bash
   npm i -g opencode-ai
   opencode --version    # confirm install
   opencode run --format json "say hi" > /tmp/opencode-run.ndjson 2>&1
   cat /tmp/opencode-run.ndjson | head -50
   ```

   Look for:
   - The event marking final assistant text (codex uses `{"type":"item.completed","item":{"type":"agent_message","text":"…"}}`; opencode's name will differ)
   - The event carrying `session_id` (if any) — needed for RunResume

   **Save the sample** into the worktree at `pkg/agentbackend/opencode/testdata/opencode_run.ndjson` (mirroring `pkg/agentbackend/codex/testdata/codex_exec.ndjson` which the codex `TestExecutorReplaysFixture` uses).

   Then edit Task 4's `opencodeEvent` struct + the parse loop to match what you saw. The spec's struct field names (`type`/`session_id`/etc.) are best-effort guesses; **use the actual schema you captured.**

2. **Verify opencode desktop config path**

   On the machine, install opencode desktop (one platform is enough — Linux .deb if available) and check whether it reads `~/.config/opencode/opencode.json` or something else (e.g. `~/.local/share/opencode/`). Document the result in Task 7's commit message. If different from CLI, file a follow-up issue and proceed with the CLI path — desktop integration becomes part of the follow-up.

   If install requires GUI / can't run headless: skip this step, document that desktop path is unverified, and rely on the spec's XDG assumption. The follow-up issue still gets filed.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `pkg/agentbackend/backend.go` | Modify | Add `KindOpencode Kind = "opencode"` constant alongside existing `KindClaude`/`KindCodex`. |
| `pkg/agentbackend/opencode/backend.go` | Create | `Backend` struct + `New(cfg, env)` + `Kind()`/`Run`/`RunResume`/`LLM`/`Permissions`/`Detect` methods + `init()` that calls `agentbackend.RegisterBuilder`. |
| `pkg/agentbackend/opencode/backend_test.go` | Create | `TestOpencodeBackend_AutoRegisters`, `TestOpencodeNew_DefaultsBin`, `TestOpencodeNew_KeepsExplicitBin`. |
| `pkg/agentbackend/opencode/llm.go` | Create | `llmRunner` implementing `agentbackend.LLMRunner`. Spawns `opencode run --dangerously-skip-permissions --format=json -`, reads stdout, extracts final text. |
| `pkg/agentbackend/opencode/llm_test.go` | Create | Fake bin via Go-built helper (mirror `buildFakeCodex`) returns canned stdout; assertions on parsed final text + error wrapping with stderr tail. |
| `pkg/agentbackend/opencode/permissions.go` | Create | NoOp `Store` — `Get` returns `State{Backend: KindOpencode, Mode: "ask"}`, `Patch` accepts `Mode`/`Presets` but persists nothing (opencode lacks an on-disk permissions schema). Reject `AllowAdd/Remove/DenyAdd/Remove` per `pkg/agentbackend.PermissionsStore` convention. |
| `pkg/agentbackend/opencode/permissions_test.go` | Create | Get default; Patch round-trips Mode/Presets; rejects claude-only fields. |
| `pkg/agentbackend/opencode/detect.go` | Create | `detect(ctx, bin) error` — `exec.LookPath(bin)`; bin defaults to `"opencode"` when empty. |
| `pkg/agentbackend/opencode/detect_test.go` | Create | Missing → err; existing fake bin → ok. |
| `pkg/agentbackend/opencode/executor.go` | Create | `executor` struct + `Run`/`RunResume` + `runWithArgv` (mirror codex shape) + `writeOpencodeHumanloopConfig` helper that writes the tmp `opencode.json` consumed via `OPENCODE_CONFIG` env. |
| `pkg/agentbackend/opencode/executor_test.go` | Create | Fixture replay using `testdata/opencode_run.ndjson` (captured in pre-flight); humanloop MCP injection test (verify tmp `opencode.json` content + `OPENCODE_CONFIG` env); `RunResume` argv test; context-cancel grace test. |
| `pkg/agentbackend/opencode/testdata/opencode_run.ndjson` | Create | Captured during pre-flight from real `opencode run --format json "say hi"`. |
| `cmd/driver-agent/main.go` | Modify | Add `_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"` to the existing backend-registry import block. |
| `cmd/slave-agent/main.go` | Modify | Same one-line import addition. |
| `internal/driver/config_test.go` | Modify | Add `TestLoadConfig_AcceptsOpencodeKind` proving the new kind passes `isRegisteredKind` when opencode is imported. |
| `internal/config/config_test.go` | Modify | Same `TestSlaveLoad_AcceptsOpencodeKind`. |
| `deploy/linux/driver/opencode.json.template` | Create | Single `mcp.driver` entry with `type: "local"`, command pointing at driver-agent binary. |
| `deploy/windows/driver/opencode.json.template` | Create | Same content (Windows-rendered path quoting handled by `install.ps1`). |
| `deploy/linux/driver/install.sh` | Modify | Replace existing `if claude / else codex` MCP-emit block with `case` statement; add `opencode` branch writing to `${XDG_CONFIG_HOME:-$HOME/.config}/opencode/opencode.json` with backup-on-existing. |
| `deploy/windows/driver/install.ps1` | Modify | Same case-style branch writing to `$env:APPDATA\opencode\opencode.json`. |
| `docs/migration/2026-06-agent-config.md` | Modify | Append "opencode" example block (agent.kind: opencode). |
| `docs/superpowers/specs/2026-06-14-opencode-backend-evidence.md` | Create | Mirror `2026-06-14-agent-backend-unify-evidence.md` structure. |

---

## Task 1: `KindOpencode` constant + opencode backend skeleton

**Files:**
- Modify: `pkg/agentbackend/backend.go`
- Create: `pkg/agentbackend/opencode/backend.go`
- Create: `pkg/agentbackend/opencode/backend_test.go`

### Step 1.1: Add `KindOpencode` constant

- [ ] **Open `pkg/agentbackend/backend.go`, find the `Kind` const block**

Current:
```go
const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
)
```

Replace with:
```go
const (
	KindClaude   Kind = "claude"
	KindCodex    Kind = "codex"
	KindOpencode Kind = "opencode"
)
```

### Step 1.2: Verify other tests still build

```bash
go build ./pkg/agentbackend/...
```

Expected: clean (constant addition doesn't break anything).

### Step 1.3: Write failing backend skeleton tests

- [ ] **Create `pkg/agentbackend/opencode/backend_test.go`**

```go
package opencode

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestOpencodeBackend_AutoRegisters pins the §issue-15 "new backend = one
// new package + one import" promise: importing this package registers the
// opencode builder so agentbackend.RegisteredKinds() includes it.
func TestOpencodeBackend_AutoRegisters(t *testing.T) {
	var found bool
	for _, k := range agentbackend.RegisteredKinds() {
		if k == string(agentbackend.KindOpencode) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("opencode not in RegisteredKinds(); got %v", agentbackend.RegisteredKinds())
	}
}

// TestOpencodeNew_DefaultsBin pins factory default: empty cfg.Bin becomes
// "opencode" (the npm-installed binary name).
func TestOpencodeNew_DefaultsBin(t *testing.T) {
	b := New(agentbackend.Config{WorkDir: t.TempDir()}, nil)
	if b.cfg.Bin != "opencode" {
		t.Fatalf("Bin=%q want opencode", b.cfg.Bin)
	}
	if b.Kind() != agentbackend.KindOpencode {
		t.Fatalf("Kind()=%v want opencode", b.Kind())
	}
}

// TestOpencodeNew_KeepsExplicitBin pins that an operator-set bin is
// preserved (not overwritten by the default).
func TestOpencodeNew_KeepsExplicitBin(t *testing.T) {
	b := New(agentbackend.Config{Bin: "/usr/local/bin/opencode-custom", WorkDir: t.TempDir()}, nil)
	if b.cfg.Bin != "/usr/local/bin/opencode-custom" {
		t.Fatalf("Bin=%q want /usr/local/bin/opencode-custom", b.cfg.Bin)
	}
}

// TestOpencodeBackend_RegistryDispatchesNew exercises the agentbackend
// registry path: agentbackend.New(Config{Kind: KindOpencode, ...}) routes
// through our init() builder and returns *Backend.
func TestOpencodeBackend_RegistryDispatchesNew(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindOpencode,
		Bin:     "opencode",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindOpencode {
		t.Fatalf("Kind()=%v want opencode", b.Kind())
	}
	_ = context.Background() // silence import if not used below
}
```

### Step 1.4: Verify tests FAIL (build errors — package doesn't exist)

```bash
go test ./pkg/agentbackend/opencode/... -count=1
```

Expected: `package opencode: no Go files` or similar — fails because no `backend.go` exists yet.

### Step 1.5: Write `backend.go`

- [ ] **Create `pkg/agentbackend/opencode/backend.go`**

```go
package opencode

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Backend is the opencode implementation of agentbackend.Backend.
// Mirrors pkg/agentbackend/codex/backend.go in shape — opencode is also
// invoked as `<bin> <subcmd> --flag …` and reads PROMPT from stdin.
//
// Backend-specific fields (humanloop MCP injection mechanism, event
// schema) live in executor.go.
type Backend struct {
	cfg  agentbackend.Config
	exec *executor
	perm *Store
	llm  *llmRunner
}

// New returns a fully-assembled opencode Backend with executor / permissions
// store / LLM runner wired. Defaults cfg.Bin to "opencode" when empty (the
// npm install target). See pkg/agentbackend/opencode/backend_test.go.
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "opencode"
	}
	return &Backend{
		cfg:  cfg,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}

func (b *Backend) Kind() agentbackend.Kind { return agentbackend.KindOpencode }

func (b *Backend) Run(ctx context.Context, t agentbackend.Task, s agentbackend.Sink) (agentbackend.Result, error) {
	return b.exec.Run(ctx, t, s)
}

func (b *Backend) RunResume(ctx context.Context, sessionID, answer string, s agentbackend.Sink) (agentbackend.Result, error) {
	return b.exec.RunResume(ctx, sessionID, answer, s)
}

func (b *Backend) LLM() agentbackend.LLMRunner                { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore { return b.perm }
func (b *Backend) Detect(ctx context.Context) error           { return detect(ctx, b.cfg.Bin) }

// init registers the opencode builder with the agentbackend registry. The
// builder runs only when this package is imported; CLI mains
// (cmd/{driver,slave}-agent/main.go) add the side-effect import.
func init() {
	agentbackend.RegisterBuilder(agentbackend.KindOpencode, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
```

This file references `executor`/`llmRunner`/`Store`/`detect` which don't exist yet — compile will fail until Tasks 2-4 land them. **Stub the missing types now** with a single `stubs.go` so this commit compiles:

- [ ] **Create `pkg/agentbackend/opencode/stubs.go`** (temporary scaffolding, deleted by Task 4)

```go
package opencode

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// stubs.go holds placeholder types so backend.go compiles before Tasks 2-4
// land the real implementations. Deleted at the end of Task 4.

type executor struct{}

func newExecutor(_ agentbackend.Config, _ []string) *executor { return &executor{} }
func (e *executor) Run(context.Context, agentbackend.Task, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.Run not implemented (Task 4)")
}
func (e *executor) RunResume(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.RunResume not implemented (Task 5)")
}

type llmRunner struct{}

func newLLM(_ agentbackend.Config, _ []string) *llmRunner { return &llmRunner{} }
func (r *llmRunner) Run(context.Context, string) (string, error) {
	panic("opencode llmRunner.Run not implemented (Task 2)")
}

type Store struct{}

func NewStore(_ string) *Store { return &Store{} }
func (s *Store) Get(context.Context) (agentbackend.State, error) {
	panic("opencode Store.Get not implemented (Task 3)")
}
func (s *Store) Patch(context.Context, agentbackend.Patch) (agentbackend.State, error) {
	panic("opencode Store.Patch not implemented (Task 3)")
}

func detect(_ context.Context, _ string) error {
	panic("opencode detect not implemented (Task 3)")
}
```

### Step 1.6: Verify tests pass

```bash
go test ./pkg/agentbackend/opencode/... -count=1
go build ./...
```

Expected: PASS on the 4 new tests; build clean (panicky stubs only run if called, which the skeleton tests don't).

### Step 1.7: Commit

```bash
git add pkg/agentbackend/backend.go pkg/agentbackend/opencode/backend.go pkg/agentbackend/opencode/backend_test.go pkg/agentbackend/opencode/stubs.go
git commit -m "feat(agentbackend): opencode skeleton + KindOpencode + init registration (Task 1)

First PR exercising PR #18 (issue #15) 'add backend = new sub-package +
two import lines' promise.

pkg/agentbackend/backend.go: add KindOpencode constant.

pkg/agentbackend/opencode/backend.go: full Backend interface shape
mirroring codex package. Factory defaults bin to 'opencode' (npm install
target). init() registers the builder.

stubs.go is temporary scaffolding so this commit compiles before Tasks
2-4 land the real executor/llm/permissions/detect; it panics on call.
Deleted by Task 4.

4 backend_test.go tests pin: auto-registration via RegisteredKinds(),
bin default, bin override, registry dispatch through agentbackend.New.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `llm.go` + `llm_test.go`

**Files:**
- Create: `pkg/agentbackend/opencode/llm.go`
- Create: `pkg/agentbackend/opencode/llm_test.go`
- Modify: `pkg/agentbackend/opencode/stubs.go` (remove `llmRunner` stub)

### Step 2.1: Write failing test

- [ ] **Create `pkg/agentbackend/opencode/llm_test.go`**

```go
package opencode

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestLLMRunnerReturnsTrimmedStdout pins the LLMRunner contract: spawn
// the configured bin, feed prompt via stdin, return trimmed stdout.
// Mirrors pkg/agentbackend/codex/llm_test.go.
func TestLLMRunnerReturnsTrimmedStdout(t *testing.T) {
	fakeBin := buildFakeOpencode(t, `package main
import (
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Stdout.Write([]byte("   pong   \n\n"))
}
`)
	llm := newLLM(agentbackend.Config{Bin: fakeBin}, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if out != "pong" {
		t.Fatalf("out=%q want %q", out, "pong")
	}
}

// TestLLMRunner_SurfacesStderrTailOnExit pins that exec failure wraps
// stderr tail (operators need to see why opencode bailed).
func TestLLMRunner_SurfacesStderrTailOnExit(t *testing.T) {
	fakeBin := buildFakeOpencode(t, `package main
import (
	"fmt"
	"os"
)
func main() {
	fmt.Fprintln(os.Stderr, "OPENCODE_ERROR_MARKER: provider auth failed")
	os.Exit(2)
}
`)
	llm := newLLM(agentbackend.Config{Bin: fakeBin}, nil)
	_, err := llm.Run(context.Background(), "ping")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_ERROR_MARKER") {
		t.Fatalf("err=%v want stderr tail with marker", err)
	}
}

// TestLLMRunner_ExtraArgsAppended pins that cfg.ExtraArgs are appended
// AFTER the default flags (operator overrides survive the default
// --dangerously-skip-permissions injection).
func TestLLMRunner_ExtraArgsAppended(t *testing.T) {
	// Fake bin emits its argv as the only stdout line so we can inspect it.
	fakeBin := buildFakeOpencode(t, `package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Print(strings.Join(os.Args[1:], "|"))
}
`)
	llm := newLLM(agentbackend.Config{Bin: fakeBin, ExtraArgs: []string{"--model", "anthropic/claude-3"}}, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--dangerously-skip-permissions") {
		t.Fatalf("default permissions flag missing: %s", out)
	}
	if !strings.Contains(out, "--model|anthropic/claude-3") {
		t.Fatalf("extra args missing: %s", out)
	}
	// Order: defaults first, extras after
	defaultIdx := strings.Index(out, "--dangerously-skip-permissions")
	extraIdx := strings.Index(out, "--model")
	if defaultIdx > extraIdx {
		t.Fatalf("extras should come AFTER defaults: %s", out)
	}
	_ = fmt.Sprintf("") // silence unused import if any
}

// buildFakeOpencode compiles a Go source as a fake opencode bin and returns
// its path. Mirrors pkg/agentbackend/codex/executor_test.go:buildFakeCodex.
func buildFakeOpencode(t *testing.T, source string) string {
	t.Helper()
	return goBuildFake(t, source, "opencode")
}
```

The helper `goBuildFake` is shared across `llm_test.go`/`detect_test.go`/`executor_test.go` — define it once in `testhelpers_test.go`.

- [ ] **Create `pkg/agentbackend/opencode/testhelpers_test.go`**

```go
package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// goBuildFake compiles `source` into an executable named `name` (+.exe on
// Windows) under t.TempDir() and returns the absolute path. Used by
// fake-bin helpers in this package's tests. Mirrors the buildFakeCodex
// pattern at pkg/agentbackend/codex/executor_test.go:281.
func goBuildFake(t *testing.T, source, name string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake %s: %v\n%s", name, err, out)
	}
	return exe
}
```

### Step 2.2: Verify RED

```bash
go test ./pkg/agentbackend/opencode/... -run "TestLLMRunner" -count=1
```

Expected: tests fail with panic from stub (`opencode llmRunner.Run not implemented`).

### Step 2.3: Implement `llm.go`

- [ ] **Create `pkg/agentbackend/opencode/llm.go`**

```go
package opencode

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// llmRunner implements agentbackend.LLMRunner by spawning
// `opencode run --dangerously-skip-permissions --format=json -`
// and returning the trimmed stdout. Used by the planner and by
// journal capability-merge calls (see internal/journal after PR #18).
//
// --format=json emits an nd-JSON event stream; this LLMRunner only
// cares about the final assistant text, which appears as the last
// significant frame. We trim trailing whitespace and return — callers
// that need structured events go through Backend.Run instead.
//
// --dangerously-skip-permissions is injected by default because the
// slave is unattended; operator overrides go in cfg.ExtraArgs.
type llmRunner struct {
	cfg agentbackend.Config
	env []string
}

func newLLM(cfg agentbackend.Config, env []string) *llmRunner {
	return &llmRunner{cfg: cfg, env: env}
}

func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
	args := []string{"run", "--dangerously-skip-permissions", "--format=json", "-"}
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
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
		}
		return "", fmt.Errorf("opencode llm exit: %v: %s", err, tail)
	}
	return strings.TrimSpace(string(out)), nil
}
```

### Step 2.4: Remove stub

- [ ] **Edit `pkg/agentbackend/opencode/stubs.go` — delete the `llmRunner` block**

Result:
```go
package opencode

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// stubs.go holds placeholder types so backend.go compiles before Tasks 3-4
// land the real implementations. Deleted at the end of Task 4.

type executor struct{}

func newExecutor(_ agentbackend.Config, _ []string) *executor { return &executor{} }
func (e *executor) Run(context.Context, agentbackend.Task, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.Run not implemented (Task 4)")
}
func (e *executor) RunResume(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.RunResume not implemented (Task 5)")
}

type Store struct{}

func NewStore(_ string) *Store { return &Store{} }
func (s *Store) Get(context.Context) (agentbackend.State, error) {
	panic("opencode Store.Get not implemented (Task 3)")
}
func (s *Store) Patch(context.Context, agentbackend.Patch) (agentbackend.State, error) {
	panic("opencode Store.Patch not implemented (Task 3)")
}

func detect(_ context.Context, _ string) error {
	panic("opencode detect not implemented (Task 3)")
}
```

### Step 2.5: GREEN

```bash
go test ./pkg/agentbackend/opencode/... -count=1
go build ./...
```

### Step 2.6: Commit

```bash
git add pkg/agentbackend/opencode/llm.go pkg/agentbackend/opencode/llm_test.go pkg/agentbackend/opencode/testhelpers_test.go pkg/agentbackend/opencode/stubs.go
git commit -m "feat(agentbackend/opencode): llm runner (Task 2)

llm.go implements agentbackend.LLMRunner via 'opencode run
--dangerously-skip-permissions --format=json -'. Stdin carries the
prompt; trimmed stdout returned. stderr tail wrapped on non-zero exit.

cfg.ExtraArgs appended AFTER defaults so operator overrides survive
the default permissions-bypass flag injection. Pinned by
TestLLMRunner_ExtraArgsAppended.

testhelpers_test.go: shared goBuildFake() — compiles a fake binary
under t.TempDir() (mirrors buildFakeCodex pattern from the codex
package). Used by llm_test.go and detect_test.go (Task 3) and
executor_test.go (Task 4).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `detect.go` + `permissions.go` + their tests

**Files:**
- Create: `pkg/agentbackend/opencode/detect.go`
- Create: `pkg/agentbackend/opencode/detect_test.go`
- Create: `pkg/agentbackend/opencode/permissions.go`
- Create: `pkg/agentbackend/opencode/permissions_test.go`
- Modify: `pkg/agentbackend/opencode/stubs.go` (remove `detect` + `Store` stubs)

### Step 3.1: Write failing `detect_test.go`

- [ ] **Create `pkg/agentbackend/opencode/detect_test.go`**

```go
package opencode

import (
	"context"
	"path/filepath"
	"testing"
)

// TestDetectFailsWhenBinMissing pins Backend.Detect()'s contract: a
// missing binary surfaces an error operators can act on.
func TestDetectFailsWhenBinMissing(t *testing.T) {
	if err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such")); err == nil {
		t.Fatal("expected error")
	}
}

// TestDetectPassesWhenBinExists pins the happy path: any executable
// at the configured path satisfies Detect (we don't try to run it
// because some bins choke on flag-only invocations).
func TestDetectPassesWhenBinExists(t *testing.T) {
	bin := goBuildFake(t, `package main

func main() {}
`, "opencode")
	if err := detect(context.Background(), bin); err != nil {
		t.Fatal(err)
	}
}
```

### Step 3.2: Write failing `permissions_test.go`

- [ ] **Create `pkg/agentbackend/opencode/permissions_test.go`**

```go
package opencode

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestStore_GetReturnsDefault pins the NoOp contract: a fresh store
// returns State with Backend=KindOpencode and Mode="ask" (the
// conservative default — opencode lacks an on-disk permissions
// schema so we report the "needs operator approval" state).
func TestStore_GetReturnsDefault(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != agentbackend.KindOpencode {
		t.Errorf("Backend=%v want opencode", got.Backend)
	}
	if got.Mode != "ask" {
		t.Errorf("Mode=%q want ask", got.Mode)
	}
}

// TestStore_PatchAcceptsModeAndPresets pins that Patch surfaces a
// new Mode (either explicit or via Presets) without trying to
// persist (opencode lacks the on-disk schema; mode lives only in
// the in-memory backend state for this turn).
func TestStore_PatchAcceptsMode(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.Patch(context.Background(), agentbackend.Patch{Mode: "workspace-write"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "workspace-write" {
		t.Errorf("Mode=%q want workspace-write", got.Mode)
	}
	// Subsequent Get returns "ask" again — NoOp does NOT persist.
	got2, _ := s.Get(context.Background())
	if got2.Mode != "ask" {
		t.Errorf("after Get: Mode=%q want ask (NoOp does not persist)", got2.Mode)
	}
}

// TestStore_PatchRejectsClaudeOnlyFields pins that Allow/Deny lists
// are claude-only and produce an explicit error rather than silent
// drop. Mirrors codex Store behaviour.
func TestStore_PatchRejectsClaudeOnlyFields(t *testing.T) {
	s := NewStore(t.TempDir())
	cases := []agentbackend.Patch{
		{AllowAdd: []string{"Read(./*)"}},
		{AllowRemove: []string{"Read(./*)"}},
		{DenyAdd: []string{"Write(/etc/**)"}},
		{DenyRemove: []string{"Write(/etc/**)"}},
	}
	for i, p := range cases {
		_, err := s.Patch(context.Background(), p)
		if err == nil {
			t.Errorf("case %d: expected error for claude-only fields", i)
		}
	}
}
```

### Step 3.3: Verify RED

```bash
go test ./pkg/agentbackend/opencode/... -run "TestDetect|TestStore" -count=1
```

Expected: panic from stubs.

### Step 3.4: Implement `detect.go`

- [ ] **Create `pkg/agentbackend/opencode/detect.go`**

```go
package opencode

import (
	"context"
	"fmt"
	"os/exec"
)

// detect resolves bin via PATH lookup. We don't shell out (some bins
// choke on flag-only invocations during health probes); a successful
// LookPath is enough for "the binary exists and is executable".
// Mirrors pkg/agentbackend/codex/detect.go.
func detect(ctx context.Context, bin string) error {
	if bin == "" {
		bin = "opencode"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("opencode bin %q not on PATH: %w", bin, err)
	}
	_ = ctx
	return nil
}
```

### Step 3.5: Implement `permissions.go`

- [ ] **Create `pkg/agentbackend/opencode/permissions.go`**

```go
package opencode

import (
	"context"
	"fmt"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Store is a NoOp PermissionsStore for opencode. opencode does not have
// an on-disk permissions schema analogous to claude's settings.json or
// codex's config.toml sandbox_mode — operators control opencode access
// via `opencode auth login` (provider keys) and the
// --dangerously-skip-permissions flag the slave-agent injects by default.
//
// Get always returns Mode="ask" so the surface looks like the other
// backends. Patch echoes a new Mode back without persisting (the value
// is meaningful only for THIS call, not future ones) and rejects the
// Allow/Deny list fields which are claude-only conventions.
//
// Mirrors codex/permissions.go's rejection list but skips the TOML
// round-trip because there's nothing to persist into.
type Store struct{ workdir string }

func NewStore(workdir string) *Store { return &Store{workdir: workdir} }

func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
	return agentbackend.State{
		Backend: agentbackend.KindOpencode,
		Path:    "", // no on-disk file
		Mode:    "ask",
	}, nil
}

func (s *Store) Patch(_ context.Context, p agentbackend.Patch) (agentbackend.State, error) {
	if len(p.AllowAdd) > 0 || len(p.AllowRemove) > 0 ||
		len(p.DenyAdd) > 0 || len(p.DenyRemove) > 0 {
		return agentbackend.State{}, fmt.Errorf(
			"opencode backend does not accept Patch.AllowAdd/AllowRemove/DenyAdd/DenyRemove (claude-only); use Mode")
	}
	mode := "ask"
	if p.Mode != "" {
		mode = p.Mode
	} else if len(p.Presets) > 0 {
		// Preset "workspace_write" / "full_access" map to obvious modes;
		// anything else is unknown and rejected. Same shape codex uses.
		switch p.Presets[len(p.Presets)-1] {
		case "workspace_write":
			mode = "workspace-write"
		case "full_access":
			mode = "full-access"
		case "ask":
			mode = "ask"
		default:
			return agentbackend.State{}, fmt.Errorf("opencode: unknown permissions preset %q", p.Presets[len(p.Presets)-1])
		}
	}
	return agentbackend.State{
		Backend: agentbackend.KindOpencode,
		Path:    "",
		Mode:    mode,
	}, nil
}
```

### Step 3.6: Remove stubs

- [ ] **Edit `pkg/agentbackend/opencode/stubs.go` — delete `Store` + `detect` blocks**

Result:
```go
package opencode

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// stubs.go: temporary scaffold for executor. Deleted by Task 4.

type executor struct{}

func newExecutor(_ agentbackend.Config, _ []string) *executor { return &executor{} }
func (e *executor) Run(context.Context, agentbackend.Task, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.Run not implemented (Task 4)")
}
func (e *executor) RunResume(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.RunResume not implemented (Task 5)")
}
```

### Step 3.7: GREEN

```bash
go test ./pkg/agentbackend/opencode/... -count=1
go build ./...
```

### Step 3.8: Commit

```bash
git add pkg/agentbackend/opencode/detect.go pkg/agentbackend/opencode/detect_test.go pkg/agentbackend/opencode/permissions.go pkg/agentbackend/opencode/permissions_test.go pkg/agentbackend/opencode/stubs.go
git commit -m "feat(agentbackend/opencode): detect + permissions Store (Task 3)

detect.go: PATH lookup of cfg.Bin (defaults to 'opencode'). Mirrors
codex/detect.go — no shell-out, just LookPath because some bins are
hostile to flag-only health probes.

permissions.go: NoOp PermissionsStore. opencode has no on-disk
schema analogous to claude settings.json or codex sandbox_mode, so
Get always returns Mode='ask' and Patch echoes a requested Mode
without persisting. Allow/Deny lists are claude-only and explicitly
rejected.

stubs.go shrinks to just the executor placeholder; Task 4 lands the
real one.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: `executor.go` Run() + humanloop MCP injection

**Files:**
- Modify: `pkg/agentbackend/opencode/stubs.go` (delete entirely at end)
- Create: `pkg/agentbackend/opencode/executor.go`
- Create: `pkg/agentbackend/opencode/executor_test.go`
- Create: `pkg/agentbackend/opencode/testdata/opencode_run.ndjson`

### Step 4.0: PRE-FLIGHT — capture real opencode event schema

- [ ] **Install opencode and capture nd-JSON sample**

```bash
npm i -g opencode-ai
opencode --version
# Set OPENAI_API_KEY or another provider key opencode supports (opencode auth login first)
opencode run --format json --dangerously-skip-permissions "say hi" > /tmp/opencode-run.ndjson 2>&1
cat /tmp/opencode-run.ndjson
```

Identify two things from the output:

1. **Final assistant text event** — the JSON event marking the message the model sent back. Note the `"type"` field value and the JSON path to the text (e.g. `type==="message.completed"` with `data.parts[0].text`, or whatever opencode emits).
2. **Session id event / field** — the event carrying a session_id (used by RunResume to continue the session). If opencode prints session id elsewhere (e.g. to stderr or as the first line), note that too.

Save the captured stream verbatim into `pkg/agentbackend/opencode/testdata/opencode_run.ndjson`. The test fixture in Step 4.5 will replay this exact stream through a fake bin.

Document both findings as the next two checklist items so reviewers can see what you concluded:

- [ ] **Document captured schema as inline test fixture comment**

Add a header comment to `testdata/opencode_run.ndjson`:

```
# Captured 2026-MM-DD via:
#   opencode run --format json --dangerously-skip-permissions "say hi"
# Final assistant text event: type="<value>", text path: <jq path>
# Session id event/path: type="<value>" / field <path>
```

### Step 4.1: Write failing executor test using the captured fixture

- [ ] **Create `pkg/agentbackend/opencode/executor_test.go`**

```go
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
	chunks []string
	closed bool
}

func (c *captureSink) Write(_, text string) { c.chunks = append(c.chunks, text) }
func (c *captureSink) Close()               { c.closed = true }

// TestExecutor_ReplaysCapturedFixture exercises Run() against the
// real-opencode event stream captured in pre-flight (Step 4.0). A
// fake bin echoes the fixture; the executor parses it and emits at
// least one sink chunk + a non-empty Result.Summary.
func TestExecutor_ReplaysCapturedFixture(t *testing.T) {
	fix, err := os.ReadFile("testdata/opencode_run.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	framesPath := filepath.Join(dir, "frames.ndjson")
	if err := os.WriteFile(framesPath, fix, 0o600); err != nil {
		t.Fatal(err)
	}

	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	body, err := os.ReadFile(%q)
	if err != nil {
		panic(err)
	}
	fmt.Print(string(body))
}
`, framesPath), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored — fake bin replays fixture"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	if res.Summary == "" {
		t.Fatalf("empty summary; sink chunks=%v", sink.chunks)
	}
}

// TestExecutor_InjectsHumanloopMCPViaTempConfig pins how opencode
// gets the humanloop MCP server: a temp opencode.json is written
// alongside a unix socket, the file lists loom_humanloop as a local
// MCP server with our binSelf as the command, and OPENCODE_CONFIG
// env is set to the file path. Different from claude's
// --mcp-config flag and codex's `-c mcp_servers.X` inline overrides —
// opencode uses a config-file-only mechanism.
func TestExecutor_InjectsHumanloopMCPViaTempConfig(t *testing.T) {
	// A fake bin that captures its OPENCODE_CONFIG env var contents
	// into a sentinel file in cwd so the test can inspect what we wrote.
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "captured-mcp-config.json")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	cfgPath := os.Getenv("OPENCODE_CONFIG")
	if cfgPath == "" {
		os.Exit(2)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(%q, data, 0o600); err != nil {
		os.Exit(4)
	}
}
`, sentinel), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	_, _ = b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("opencode bin did not capture OPENCODE_CONFIG contents: %v", err)
	}
	var cfg struct {
		MCP map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("captured config not JSON: %v\n%s", err, data)
	}
	hl, ok := cfg.MCP["loom_humanloop"]
	if !ok {
		t.Fatalf("loom_humanloop not in captured MCP map: %v", cfg.MCP)
	}
	if hl.Type != "local" {
		t.Errorf("loom_humanloop.type=%q want local", hl.Type)
	}
	if len(hl.Command) < 2 {
		t.Fatalf("loom_humanloop.command too short: %v", hl.Command)
	}
	if !strings.HasSuffix(hl.Command[0], "opencode.test") && !strings.Contains(hl.Command[0], "/T/") {
		// The binSelf default in tests is os.Args[0] which is the test binary —
		// just assert it's a non-empty absolute path; precise match isn't useful.
		if hl.Command[0] == "" {
			t.Errorf("command[0] empty")
		}
	}
	if hl.Command[1] != "humanloop-mcp" {
		t.Errorf("command[1]=%q want humanloop-mcp", hl.Command[1])
	}
	_ = sink
}
```

### Step 4.2: Verify RED

```bash
go test ./pkg/agentbackend/opencode/... -run "TestExecutor_" -count=1
```

Expected: panic from `stubs.go` executor.

### Step 4.3: Delete `stubs.go` entirely

```bash
rm pkg/agentbackend/opencode/stubs.go
```

(After Task 4, all stubs are gone — Task 2/3 already deleted theirs.)

### Step 4.4: Implement `executor.go`

- [ ] **Create `pkg/agentbackend/opencode/executor.go`**

```go
package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	executorpkg "github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/internal/platform"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// executor spawns `opencode run` non-interactively. Closest reference
// is pkg/agentbackend/codex/executor.go — both bins read PROMPT from
// stdin (trailing `-` arg), emit nd-JSON events on stdout, and exit
// when stdin closes. The opencode-specific bit is humanloop MCP
// injection: opencode reads its MCP config from a JSON file pointed
// at by OPENCODE_CONFIG (env var), not from a CLI flag.
type executor struct {
	cfg agentbackend.Config
	env []string

	binSelf          string
	maxQuestions     int
	shutdownGraceSec int

	socketHookForTest func(string)
}

func newExecutor(cfg agentbackend.Config, env []string) *executor {
	return &executor{
		cfg:              cfg,
		env:              env,
		binSelf:          os.Args[0],
		maxQuestions:     5,
		shutdownGraceSec: 10,
	}
}

// opencodeEvent matches the nd-JSON events emitted by
// `opencode run --format json …`. Schema captured in pre-flight
// (see pkg/agentbackend/opencode/testdata/opencode_run.ndjson header).
//
// IMPORTANT: implementer must adjust this struct to match the captured
// real schema. The field names below are placeholders — replace with
// what `opencode run --format json` actually emits. Look for the event
// marking final assistant text and the event carrying session_id.
type opencodeEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	prompt := t.Prompt + agentbackend.CapabilityEpilogue
	if t.SystemContext != "" {
		prompt = t.SystemContext + "\n\n" + prompt
	}
	args := append([]string{
		"run",
		"--format=json",
		"--dangerously-skip-permissions",
	}, e.cfg.ExtraArgs...)
	return e.runWithArgv(ctx, args, prompt, sink)
}

// runWithArgv: shared pipeline. argvHead is "run ..." or
// "run --session <id> --continue ...".  Appends `-` (stdin-prompt
// sentinel) and OPENCODE_CONFIG env pointing at a temp config with
// the humanloop MCP server.
func (e *executor) runWithArgv(ctx context.Context, argvHead []string, prompt string, sink agentbackend.Sink) (agentbackend.Result, error) {
	sockDir, err := os.MkdirTemp("", "humanloop-")
	if err != nil {
		return agentbackend.Result{}, err
	}
	defer os.RemoveAll(sockDir)

	srv, ep, err := humanloop.ListenIPC(sockDir)
	if err != nil {
		return agentbackend.Result{}, err
	}
	defer srv.Close()
	if e.socketHookForTest != nil {
		go e.socketHookForTest(humanloop.EndpointArg(ep))
	}

	cfgPath := filepath.Join(sockDir, "opencode.json")
	if err := writeOpencodeHumanloopConfig(cfgPath, e.binSelf, ep, e.maxQuestions); err != nil {
		return agentbackend.Result{}, err
	}

	args := append([]string{}, argvHead...)
	args = append(args, "--dir", e.cfg.WorkDir, "-") // -- dir + stdin sentinel

	cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
	cmd.Dir = e.cfg.WorkDir
	cmd.Env = append(cmd.Environ(), e.env...)
	cmd.Env = append(cmd.Env, "OPENCODE_CONFIG="+cfgPath)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agentbackend.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agentbackend.Result{}, err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return agentbackend.Result{}, err
	}

	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		_, _ = io.WriteString(stdin, prompt)
		_ = stdin.Close()
	}()

	var awaiting *executorpkg.AskUserPayload
	pauseCh := make(chan struct{})
	go func() {
		defer close(pauseCh)
		p, err := srv.Receive()
		if err != nil {
			return
		}
		awaiting = &executorpkg.AskUserPayload{
			Kind:     p.Kind,
			Question: p.Question,
			Options:  p.Options,
			Context:  p.Context,
			Intent:   p.Intent,
			Target:   p.Target,
			Reason:   p.Reason,
		}
		<-promptDone
		_ = stdin.Close()
	}()

	var lastText strings.Builder
	var sessionID string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		var ev opencodeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		// IMPLEMENTER: replace the two if-blocks below with the actual
		// event type names you captured in Step 4.0.
		if ev.SessionID != "" && sessionID == "" {
			sessionID = ev.SessionID
		}
		if text := extractAssistantText(ev); text != "" {
			sink.Write("chunk", text)
			lastText.WriteString(text)
		}
	}

	// Wait with shutdown grace (copy of codex pattern).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	killed := false
	select {
	case err := <-done:
		if awaiting == nil && err != nil {
			sink.Close()
			tail := stderrBuf.String()
			if len(tail) > 4096 {
				tail = tail[len(tail)-4096:]
			}
			if ctx.Err() == context.DeadlineExceeded {
				return agentbackend.Result{}, fmt.Errorf("timeout")
			}
			return agentbackend.Result{}, fmt.Errorf("opencode exit: %v: %s", err, tail)
		}
	case <-time.After(time.Duration(e.shutdownGraceSec) * time.Second):
		killed = true
		_ = platform.TerminateProcess(cmd.Process)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}

	select {
	case <-pauseCh:
	default:
		_ = srv.Close()
		<-pauseCh
	}

	full := lastText.String()
	summary, change := agentbackend.SplitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	if awaiting != nil && sessionID == "" {
		sink.Close()
		return agentbackend.Result{}, fmt.Errorf("backend never emitted session_id; cannot resume")
	}
	if killed {
		sink.Close()
		return agentbackend.Result{}, fmt.Errorf("opencode did not exit within %ds grace window after stdin close; graceful termination/forced kill applied", e.shutdownGraceSec)
	}
	sink.Close()
	return agentbackend.Result{
		Summary:          summary,
		CapabilityChange: change,
		SessionID:        sessionID,
		AwaitingUser:     awaiting,
	}, nil
}

// extractAssistantText pulls assistant text out of an opencode event.
// IMPLEMENTER: replace body with the actual schema you captured.
// Placeholder treats event Data as {"text": "..."}.
func extractAssistantText(ev opencodeEvent) string {
	// IMPLEMENTER: per the captured fixture comment in
	// testdata/opencode_run.ndjson header, fill this with the right
	// {type, path} extraction.
	var d struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(ev.Data, &d)
	return d.Text
}

// writeOpencodeHumanloopConfig writes a temp opencode.json with a single
// loom_humanloop MCP server. opencode reads MCP config from OPENCODE_CONFIG
// env (see https://opencode.ai/docs/mcp-servers/). type="local" means
// opencode spawns the command as a subprocess; we point command[0] at
// binSelf (slave-agent) with the humanloop-mcp subcommand + endpoint args
// — slave-agent already knows how to serve that path (see
// internal/humanloop/server.go).
//
// Why a config file (and not a CLI flag like claude/codex): opencode
// doesn't expose CLI overrides for MCP — only the config file and the
// OPENCODE_CONFIG env. A temp file per Run() is self-contained and
// doesn't pollute the user's ~/.config/opencode/opencode.json.
func writeOpencodeHumanloopConfig(path, binSelf string, ep humanloop.Endpoint, max int) error {
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			"loom_humanloop": map[string]any{
				"type":    "local",
				"command": []string{binSelf, "humanloop-mcp", humanloop.EndpointArg(ep), strconv.Itoa(max)},
				"enabled": true,
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

### Step 4.5: GREEN

```bash
go test ./pkg/agentbackend/opencode/... -count=1
```

Expected: all tests pass. If `TestExecutor_ReplaysCapturedFixture` fails because the captured schema differs from what `extractAssistantText` expects, **that's the implementer's signal to adjust the placeholder `opencodeEvent` struct + `extractAssistantText` to match the real schema captured in Step 4.0**. Iterate until green.

```bash
go build ./...
go vet ./...
```

### Step 4.6: Commit

```bash
git add pkg/agentbackend/opencode/executor.go pkg/agentbackend/opencode/executor_test.go pkg/agentbackend/opencode/testdata/
git rm pkg/agentbackend/opencode/stubs.go 2>/dev/null || true
git commit -m "feat(agentbackend/opencode): Run() + humanloop MCP via OPENCODE_CONFIG (Task 4)

Run() spawns 'opencode run --format=json --dangerously-skip-permissions
--dir <workdir> -' with prompt on stdin, parses nd-JSON event stream.
Mirrors codex executor pipeline (pause-via-IPC + grace shutdown).

Humanloop MCP injection is opencode-specific: opencode reads MCP config
ONLY from a JSON file pointed at by OPENCODE_CONFIG env. We write a
temp opencode.json per Run() containing the loom_humanloop server
(command[0]=binSelf, args=[humanloop-mcp, endpoint, max]). Different
from claude's --mcp-config flag and codex's '-c mcp_servers.X' inline
TOML overrides.

opencodeEvent struct + extractAssistantText match the captured event
schema in testdata/opencode_run.ndjson (header comment documents the
opencode run --format json output captured in pre-flight). If opencode
changes the schema, those two are the only places to update.

stubs.go deleted — all sub-types now real.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: `executor.go` RunResume()

**Files:**
- Modify: `pkg/agentbackend/opencode/executor.go`
- Modify: `pkg/agentbackend/opencode/executor_test.go`

### Step 5.1: Write failing test

- [ ] **Append to `executor_test.go`**

```go
// TestExecutor_RunResume_UsesSessionFlag pins the resume protocol:
// `opencode run --session <id> --continue` with the new answer as
// the prompt (rendered as "User answered: <answer>" for clarity).
// Mirrors codex/executor_test.go for `exec resume`.
func TestExecutor_RunResume_UsesSessionFlag(t *testing.T) {
	// Fake bin captures its argv and stdin into sentinel files.
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
	"strings"
)
func main() {
	argv := strings.Join(os.Args[1:], "|")
	_ = os.WriteFile(%q, []byte(argv), 0o600)
	body, _ := io.ReadAll(os.Stdin)
	_ = os.WriteFile(%q, body, 0o600)
}
`, argvPath, stdinPath), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	_, _ = b.RunResume(context.Background(), "sess-abc", "yes please proceed", sink)

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--session|sess-abc") {
		t.Errorf("argv missing --session sess-abc: %s", argv)
	}
	if !strings.Contains(string(argv), "--continue") {
		t.Errorf("argv missing --continue: %s", argv)
	}

	stdinBody, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdinBody), "User answered: yes please proceed") {
		t.Errorf("stdin missing user-answered prompt: %s", stdinBody)
	}
}
```

### Step 5.2: Verify RED

```bash
go test ./pkg/agentbackend/opencode/... -run "TestExecutor_RunResume" -count=1
```

Expected: fail — `runWithArgv` is wired through Run() but RunResume isn't implemented yet (it's currently dispatched through the stubbed path; the real `runWithArgv` exists from Task 4 but RunResume's specific argv is missing). Actually `RunResume` would have been auto-implemented as part of Task 4's `executor.go` if you followed the codex shape verbatim — verify by reading. If RunResume already exists from Task 4, this test should pass without changes; skip to Step 5.3.

### Step 5.3: Implement RunResume (if not already done in Task 4)

- [ ] **Add to `executor.go` after Run()** (if missing)

```go
// RunResume re-invokes opencode with `run --session <id> --continue`
// so the model continues the conversation it paused on a humanloop
// ask. Prompt is rendered "User answered: <answer>" — the model sees
// a natural user-turn responding to its prior request. Like Run(),
// humanloop MCP is injected so the model can pause AGAIN
// (multi-round Q&A is supported).
func (e *executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	args := append([]string{
		"run",
		"--session", sessionID,
		"--continue",
		"--format=json",
		"--dangerously-skip-permissions",
	}, e.cfg.ExtraArgs...)
	prompt := "User answered: " + answer
	return e.runWithArgv(ctx, args, prompt, sink)
}
```

### Step 5.4: GREEN

```bash
go test ./pkg/agentbackend/opencode/... -count=1
go build ./...
```

### Step 5.5: Commit

```bash
git add pkg/agentbackend/opencode/executor.go pkg/agentbackend/opencode/executor_test.go
git commit -m "feat(agentbackend/opencode): RunResume via --session + --continue (Task 5)

RunResume passes 'run --session <id> --continue --format=json
--dangerously-skip-permissions' to runWithArgv, prompt body
\"User answered: <answer>\". Humanloop MCP re-injected so the
model can pause AGAIN. Mirrors codex/executor.go RunResume.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: CLI imports + LoadConfig acceptance tests

**Files:**
- Modify: `cmd/driver-agent/main.go`
- Modify: `cmd/slave-agent/main.go`
- Modify: `internal/driver/config_test.go`
- Modify: `internal/config/config_test.go`

### Step 6.1: Write failing acceptance tests

- [ ] **Append to `internal/driver/config_test.go`**

```go
// TestLoadConfig_AcceptsOpencodeKind pins issue-15's "add backend
// only via import" promise: agent.kind: opencode loads after the
// driver-agent main imports pkg/agentbackend/opencode for side
// effects. The test relies on the side-effect imports config_test.go
// already has for claude+codex; add opencode to that block too.
func TestLoadConfig_AcceptsOpencodeKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: opencode
  workdir: /tmp/proj
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "opencode" {
		t.Fatalf("Agent.Kind=%q want opencode", c.Agent.Kind)
	}
	if c.Agent.Bin != "opencode" {
		t.Fatalf("Agent.Bin=%q want opencode (factory default)", c.Agent.Bin)
	}
}
```

- [ ] **Append to `internal/config/config_test.go`**

```go
// TestSlaveLoad_AcceptsOpencodeKind — mirror of the driver-side test.
func TestSlaveLoad_AcceptsOpencodeKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: opencode
  workdir: /tmp/proj
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "opencode" {
		t.Fatalf("Agent.Kind=%q want opencode", c.Agent.Kind)
	}
}
```

### Step 6.2: Verify RED (unknown kind because import missing)

```bash
go test ./internal/driver/ -run TestLoadConfig_AcceptsOpencodeKind -count=1
go test ./internal/config/ -run TestSlaveLoad_AcceptsOpencodeKind -count=1
```

Expected: fail with "unknown agent.kind \"opencode\"" because the test file's `_ "..."` imports don't yet pull opencode.

### Step 6.3: Add side-effect import in test files

- [ ] **`internal/driver/config_test.go`** — find the `_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"` import block, add the line:

```go
_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
```

- [ ] **`internal/config/config_test.go`** — same addition in its side-effect import block.

### Step 6.4: Add side-effect import in CLI mains

- [ ] **`cmd/driver-agent/main.go`** — find the backend-registry import block (next to existing `_ "...claude"` and `_ "...codex"`), add:

```go
_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
```

- [ ] **`cmd/slave-agent/main.go`** — same one-line addition.

### Step 6.5: GREEN

```bash
go test ./internal/driver/... ./internal/config/... ./cmd/driver-agent/... ./cmd/slave-agent/... -count=1
go build ./...
go vet ./...
```

Expected: PASS + clean.

### Step 6.6: Commit

```bash
git add cmd/driver-agent/main.go cmd/slave-agent/main.go internal/driver/config_test.go internal/config/config_test.go
git commit -m "feat(cmd,config): wire opencode into driver+slave registries (Task 6)

cmd/{driver,slave}-agent/main.go each gain one side-effect import:
  _ \"github.com/yourorg/multi-agent/pkg/agentbackend/opencode\"

Same line added to internal/{driver,config}/config_test.go so unit
tests see opencode in agentbackend.RegisteredKinds().

Two acceptance tests confirm agent.kind: opencode loads cleanly and
factory defaults Bin to 'opencode'. The 'add backend = 2 import
lines' promise from issue #15 is now mechanically verified end-to-end.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Deploy templates + install scripts

**Files:**
- Create: `deploy/linux/driver/opencode.json.template`
- Create: `deploy/windows/driver/opencode.json.template`
- Modify: `deploy/linux/driver/install.sh`
- Modify: `deploy/windows/driver/install.ps1`

### Step 7.1: Audit current install scripts

- [ ] **Read existing claude/codex emission blocks**

```bash
grep -n "MCP\|\.mcp\.json\|codex-mcp\|claude.*template" deploy/linux/driver/install.sh
sed -n '/AGENT/,/fi$/p' deploy/linux/driver/install.sh | head -50
grep -n "Replace\|opencode\|claude\|codex" deploy/windows/driver/install.ps1 | head -30
```

Note the exact shape of the existing claude vs codex if/else so you can convert it to `case` cleanly.

### Step 7.2: Create linux template

- [ ] **Create `deploy/linux/driver/opencode.json.template`**

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "driver": {
      "type": "local",
      "command": ["__DRIVER_AGENT__", "serve-mcp", "--config", "__CONFIG__"],
      "enabled": true
    }
  }
}
```

### Step 7.3: Create windows template

- [ ] **Create `deploy/windows/driver/opencode.json.template`**

Same content as Linux — the install.ps1 will Windows-quote the paths.

### Step 7.4: Modify linux install.sh

- [ ] **Replace the existing `if AGENT == claude / else codex` MCP-emission block**

Find code matching this shape (exact line numbers will differ — find the block by grepping for `\.mcp\.json` or `codex-mcp\.toml`):

```bash
if [[ "$AGENT" == "claude" ]]; then
  sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/.mcp.json.template" > "$PROJECT_ABS/.mcp.json"
else
  mkdir -p "$PROJECT_ABS/.codex"
  sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/codex-mcp.toml.template" > "$PROJECT_ABS/.codex/config.toml"
fi
```

Replace with:

```bash
case "$AGENT" in
  claude)
    sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/.mcp.json.template" > "$PROJECT_ABS/.mcp.json"
    ;;
  codex)
    mkdir -p "$PROJECT_ABS/.codex"
    sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/codex-mcp.toml.template" > "$PROJECT_ABS/.codex/config.toml"
    ;;
  opencode)
    # opencode CLI + desktop share the same MCP config file
    # (~/.config/opencode/opencode.json on Linux). Writing here means
    # both consume the driver server. If a config already exists, back
    # it up rather than merge JSON (merge is a follow-up if anyone
    # actually maintains a custom opencode.json alongside loom).
    OPENCODE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode"
    OPENCODE_CFG="$OPENCODE_DIR/opencode.json"
    mkdir -p "$OPENCODE_DIR"
    if [[ -f "$OPENCODE_CFG" ]]; then
      cp -f "$OPENCODE_CFG" "$OPENCODE_CFG.bak.$(date +%Y%m%d-%H%M%S)"
      echo "==> backed up existing opencode.json (loom install will overwrite)"
    fi
    sed \
      -e "s|__DRIVER_AGENT__|$PROJECT_ABS/driver-agent|g" \
      -e "s|__CONFIG__|$PROJECT_ABS/config.yaml|g" \
      "$HERE/opencode.json.template" > "$OPENCODE_CFG"
    echo "==> wrote $OPENCODE_CFG (consumed by both opencode CLI and desktop)"
    ;;
  *)
    echo "unsupported agent: $AGENT (expected claude|codex|opencode)" >&2
    exit 1
    ;;
esac
```

### Step 7.5: Modify windows install.ps1

- [ ] **Find the equivalent if/else block in install.ps1 and convert to switch**

```bash
grep -n "Agent.*-eq" deploy/windows/driver/install.ps1
```

Find a block like:

```powershell
if ($Agent -eq "claude") {
    # ... render .mcp.json ...
} elseif ($Agent -eq "codex") {
    # ... render .codex/config.toml ...
}
```

Replace with:

```powershell
switch ($Agent) {
  "claude" {
    # ... existing claude emit (untouched) ...
  }
  "codex" {
    # ... existing codex emit (untouched) ...
  }
  "opencode" {
    # opencode CLI + desktop share %APPDATA%\opencode\opencode.json
    $opencodeDir = Join-Path $env:APPDATA "opencode"
    $opencodeCfg = Join-Path $opencodeDir "opencode.json"
    New-Item -ItemType Directory -Force -Path $opencodeDir | Out-Null
    if (Test-Path $opencodeCfg) {
      $ts = Get-Date -Format "yyyyMMdd-HHmmss"
      Copy-Item $opencodeCfg "$opencodeCfg.bak.$ts" -Force
      Write-Host "==> backed up existing opencode.json (loom install will overwrite)"
    }
    $tmpl = Get-Content -LiteralPath (Join-Path $here "opencode.json.template") -Raw
    $tmpl = $tmpl.Replace("__DRIVER_AGENT__", (ConvertTo-YamlQuoted $driverExe))
    $tmpl = $tmpl.Replace("__CONFIG__",       (ConvertTo-YamlQuoted $configPath))
    Set-Content -LiteralPath $opencodeCfg -Value $tmpl -Encoding utf8
    Write-Host "==> wrote $opencodeCfg (consumed by both opencode CLI and desktop)"
  }
  default {
    throw "unsupported agent: $Agent (expected claude|codex|opencode)"
  }
}
```

(Adjust `$here` / `$driverExe` / `$configPath` / `ConvertTo-YamlQuoted` to whatever names the existing install.ps1 uses — read the surrounding context.)

### Step 7.6: Smoke render

```bash
mkdir -p /tmp/opencode-install-smoke
cd /tmp/opencode-install-smoke
HERE=/root/multi-agent/.claude/worktrees/add-opencode-backend/multi-agent/deploy/linux/driver
PROJECT_ABS=/tmp/opencode-install-smoke/project
mkdir -p "$PROJECT_ABS"
sed \
  -e "s|__DRIVER_AGENT__|$PROJECT_ABS/driver-agent|g" \
  -e "s|__CONFIG__|$PROJECT_ABS/config.yaml|g" \
  "$HERE/opencode.json.template" > rendered-opencode.json
cat rendered-opencode.json
```

Expected output (single block, no `__*__` placeholders):

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "driver": {
      "type": "local",
      "command": ["/tmp/opencode-install-smoke/project/driver-agent", "serve-mcp", "--config", "/tmp/opencode-install-smoke/project/config.yaml"],
      "enabled": true
    }
  }
}
```

### Step 7.7: Commit

```bash
git add deploy/
git commit -m "deploy: opencode driver MCP template + install branches (Task 7)

deploy/{linux,windows}/driver/opencode.json.template: single
mcp.driver entry. opencode CLI and desktop share the same config
file location so one template + one render covers both clients.

install.sh + install.ps1 switch on AGENT and gain an opencode branch
that:
  - writes to \${XDG_CONFIG_HOME:-~/.config}/opencode/opencode.json
    (Linux) or %APPDATA%\\opencode\\opencode.json (Windows)
  - backs up an existing opencode.json with timestamp suffix instead
    of merging JSON (YAGNI; merge is a follow-up if anyone actually
    runs a custom config alongside loom)
  - rejects unknown agent kinds with a clear error

Smoke-rendered the linux template: __DRIVER_AGENT__ / __CONFIG__ /
__PROJECT_DIR__ all substitute cleanly, output is the documented
opencode.json shape.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Migration doc + regression + evidence + PR

**Files:**
- Modify: `docs/migration/2026-06-agent-config.md`
- Create: `docs/superpowers/specs/2026-06-14-opencode-backend-evidence.md`

### Step 8.1: Append opencode example to migration doc

- [ ] **Edit `docs/migration/2026-06-agent-config.md`** — append section:

```markdown
## opencode (added later)

opencode is the 3rd supported kind. Config is identical in shape:

\`\`\`yaml
agent:
  kind: opencode
  bin: opencode        # optional; factory default
  workdir: /loom/proj
  extra_args: []
\`\`\`

Driver-side install writes `~/.config/opencode/opencode.json` (Linux)
or `%APPDATA%\opencode\opencode.json` (Windows). opencode CLI and
desktop share this file — both will see the driver MCP server.

Operators must authenticate their opencode provider separately:

\`\`\`bash
opencode auth login
\`\`\`

The slave-agent does not pass credentials through; it just spawns
the opencode binary which reads its own auth.json.
```

### Step 8.2: Full regression

- [ ] **Run full repo race**

```bash
go test ./... -race -count=1 2>&1 | tail -40
```

Expected: all `ok`, no FAIL. If anything in master-frozen paths regresses (unlikely for an additive PR), STOP and report.

### Step 8.3: Binary smoke

```bash
mkdir -p /tmp/bin-smoke-opencode
go build -o /tmp/bin-smoke-opencode/driver-agent ./cmd/driver-agent
go build -o /tmp/bin-smoke-opencode/slave-agent ./cmd/slave-agent
/tmp/bin-smoke-opencode/driver-agent 2>&1 | head -5
```

Expected: both build clean, help text appears.

### Step 8.4: Master-path freeze check

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
```

Expected: empty.

### Step 8.5: Write evidence doc

- [ ] **Create `docs/superpowers/specs/2026-06-14-opencode-backend-evidence.md`**

Mirror the structure of `docs/superpowers/specs/2026-06-14-agent-backend-unify-evidence.md`. Sections:

```markdown
# opencode Backend — 测试证据

- **日期**：2026-MM-DD
- **分支**：worktree-add-opencode-backend @ HEAD <SHA>
- **范围**：anomalyco/opencode 作为 3rd backend；slave 端 backend 包 + driver 端 deploy 模板
- **不在范围**：opencode `acp` 子命令、auth pass-through、master 路径、host-local e2e（留 follow-up — slave 端 opencode 真起任务的 e2e 是后续 issue）

## 为什么 unit + binary smoke 足够

- backend 是纯本地子进程调用（与 codex 同形状，codex backend 已经 e2e 验证过相同形状）
- humanloop MCP 注入由 unit test 验证（assert tmp opencode.json content + OPENCODE_CONFIG env 传递）
- 真起 opencode 端到端跑任务需要：装 opencode + 配 provider auth + 网络可达——属于 host-local e2e 工作量，与本 PR 解耦。Issue 跟进。

## 提交序列

\`\`\`
<git log --oneline master..HEAD>
\`\`\`

## 任务覆盖

| Task | Commit | 内容 |
|---|---|---|
| 1 | <SHA> | KindOpencode + Backend skeleton + init registration |
| 2 | <SHA> | llmRunner |
| 3 | <SHA> | detect + NoOp permissions Store |
| 4 | <SHA> | executor Run() + humanloop MCP via OPENCODE_CONFIG |
| 5 | <SHA> | RunResume |
| 6 | <SHA> | CLI imports + LoadConfig acceptance |
| 7 | <SHA> | deploy templates + install branches |
| 8 | (this) | migration doc + evidence + PR |

## 测试

**新 packages**: `pkg/agentbackend/opencode` — N 个测试（list them by name + what they pin）.

**Acceptance**: `internal/{driver,config}/config_test.go` 各 +1 `TestLoadConfig_AcceptsOpencodeKind` / `TestSlaveLoad_AcceptsOpencodeKind`.

**Full repo race**: `go test ./... -race -count=1` 全过.

## 二进制 sanity

`go build ./...` clean; `driver-agent` / `slave-agent` help text 正常.

## 实测的 opencode event schema

记录 Step 4.0 pre-flight 抓的 `opencode run --format json` 输出 schema（哪些 event type、final assistant text 在哪、session_id 在哪），引 `testdata/opencode_run.ndjson` header comment。

## Desktop config path verification

记 Step 4.0 第二项：实际测试 opencode desktop 是否读 `~/.config/opencode/opencode.json`。如果不是，filed issue link + follow-up scope。

## Master-path freeze

\`\`\`
$ git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
(empty)
\`\`\`

## Follow-up

- host-local e2e: slave 端真起一个 opencode 任务，验证 humanloop pause/resume
- desktop discovery 路径如与 CLI 不同，单独 issue
- opencode acp 子命令（v2 性能优化）
```

### Step 8.6: Commit migration + evidence

```bash
git add docs/migration/2026-06-agent-config.md docs/superpowers/specs/2026-06-14-opencode-backend-evidence.md
git commit -m "docs: migration update + opencode backend evidence (Task 8)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

### Step 8.7: Push + open PR

```bash
git push -u origin worktree-add-opencode-backend

gh pr create --base master --head worktree-add-opencode-backend \
  --title "feat(agentbackend): add opencode backend (slave + driver deploy)" \
  --body "$(cat <<'EOF'
First PR to exercise [PR #18 (issue #15)](https://github.com/agentserver/loom/pull/18)'s "add backend = new sub-package + 2 import lines" promise. Adds [anomalyco/opencode](https://github.com/sst/opencode) as a 3rd agent backend alongside claude and codex.

## Scope

| Area | What |
|---|---|
| `pkg/agentbackend/backend.go` | New `KindOpencode` constant |
| `pkg/agentbackend/opencode/` | Full Backend implementation — Run / RunResume / LLM / Permissions / Detect, mirrors codex shape |
| Humanloop integration | opencode-specific: temp `opencode.json` written + `OPENCODE_CONFIG` env (opencode lacks claude's `--mcp-config` flag and codex's `-c mcp_servers.X` inline override) |
| `cmd/{driver,slave}-agent/main.go` | One `_ "..."` import each |
| `deploy/{linux,windows}/driver/` | New `opencode.json.template` + install.sh/ps1 opencode branch writes to user-level XDG/APPDATA location (CLI + desktop share it) |
| `docs/migration/2026-06-agent-config.md` | Append opencode example |

## Why

PR #18 promised a new backend = one new package + two import lines + one Kind constant. This PR is the proof. Touch count:
- `pkg/agentbackend/opencode/` (new package, 5 .go files + 1 testdata)
- `pkg/agentbackend/backend.go` (1 line: new constant)
- `cmd/driver-agent/main.go` + `cmd/slave-agent/main.go` (1 line each)
- `deploy/{linux,windows}/driver/` (1 new template + ~20 lines of case-branch in install scripts)

## Driver CLI + desktop

opencode CLI and opencode desktop share the same MCP config file (`~/.config/opencode/opencode.json` on Linux, `%APPDATA%\opencode\opencode.json` on Windows). One template, one install branch, both clients see the driver server.

## Master-path freeze (still honored)

`git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'` → empty.

## Testing

- New unit tests for each opencode sub-component (backend skeleton, llm, detect, permissions, executor with fixture replay + humanloop injection check + RunResume argv).
- Acceptance: `internal/{driver,config}/config_test.go` each get one `TestLoadConfig_AcceptsOpencodeKind` verifying the registry path end-to-end.
- Full repo race: `go test ./... -race -count=1` all green.
- Binary smoke: driver-agent / slave-agent build clean.
- Captured real `opencode run --format json` output as test fixture (`pkg/agentbackend/opencode/testdata/opencode_run.ndjson`).

## Out of scope (intentional)

- opencode `acp` subcommand (run --format json is enough for v1)
- opencode auth pass-through (operator runs `opencode auth login` once)
- Master path
- Host-local e2e (slave actually spawning opencode against a real task) — follow-up issue; codex backend's e2e in PR #18 covered the same shape.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage:**

| Spec invariant | Implementing task |
|---|---|
| (1) `agent.kind: opencode` accepted | Task 6 (acceptance tests + main imports) |
| (2) Bin defaults to "opencode" | Task 1 + Task 3 (in New() and detect()) |
| (3) `--dangerously-skip-permissions` default | Task 2 (llm) + Task 4 (executor) |
| (4) Humanloop MCP via tmp opencode.json + OPENCODE_CONFIG | Task 4 (`writeOpencodeHumanloopConfig` + executor env) |
| (5) `backend.LLM().Run()` works | Task 2 |
| (6) `RunResume` with `--session <id> --continue` | Task 5 |
| (7) `Detect()` via PATH | Task 3 |
| (8) Driver opencode.json template (CLI + desktop) | Task 7 (templates + install branches) |
| (9) install.sh/ps1 opencode branch + OS-conventional path | Task 7 |

**Placeholder scan:** No `TODO`/`TBD`. The only `IMPLEMENTER:` comments are in `executor.go`'s `opencodeEvent` struct + `extractAssistantText` — those are explicitly required because the real schema must be captured in pre-flight (Step 4.0) and pasted in. Reviewers expect implementer notes here; this is not a plan failure.

**Type / signature consistency:**
- `New(cfg agentbackend.Config, env []string) *Backend` — Task 1, used by Task 6 imports + Task 4 executor tests
- `agentbackend.KindOpencode` — Task 1, used by Tasks 2/3/4/6 in `Get` state + register
- `newExecutor(cfg, env) *executor`, `newLLM(cfg, env) *llmRunner`, `NewStore(workdir) *Store`, `detect(ctx, bin) error` — all defined Task 1 stubs, replaced in Tasks 2/3/4 with same signatures
- `executor.Run`/`RunResume`/`runWithArgv` — Task 4 (Run + runWithArgv), Task 5 (RunResume)
- `writeOpencodeHumanloopConfig(path, binSelf, ep, max)` — Task 4
- `extractAssistantText(ev opencodeEvent) string` — Task 4 (placeholder; implementer adjusts)
- `goBuildFake(t, source, name)` — Task 2 testhelpers, reused by Tasks 3+4

**Risk acknowledgements baked in:**
- Pre-flight Step 4.0 captures opencode schema BEFORE writing parsing
- Pre-flight Step 4.0 verifies desktop path BEFORE Task 7 commits to XDG assumption
- stubs.go pattern keeps every commit compileable + each task self-contained
- Master-path freeze re-verified after Task 7 (the only one touching deploy scripts that could accidentally drift)
