# Agent Backend Unify (issue #15) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse `cfg.Claude.*` / `cfg.Codex.*` into `cfg.Agent.*` across driver + slave; flatten `agentbackend.Config{Kind, Claude, Codex}` to `agentbackend.Config{Kind, Bin, WorkDir, ExtraArgs}` so adding a new backend only touches `pkg/agentbackend/<name>/` + two `_ "..."` imports; eliminate every `switch cfg.Agent.Kind` outside the registry; fix 3 bug consequences (bash/powershell hardcoded `cfg.Claude.WorkDir`, `journal.ClaudeBin` calling missing binary on codex slaves, PR #14 P1 fallback band-aid).

**Architecture:** `pkg/agentbackend` already has `RegisterBuilder` + `New` (Go-stdlib-style self-registration via `init()` in claude/codex sub-packages). This PR is **rename + flatten**, not greenfield. `Config` collapses to one carrier struct; each backend's `init()` builder reads the flat fields. CLI mains and config loaders lose every kind-specific branch — only `cfg.Agent.Kind` reaches `agentbackend.New`, the registry handles dispatch.

**Tech Stack:** Go stdlib + existing yaml.v3 (no new deps). Existing testify pattern throughout.

**Spec:** `docs/superpowers/specs/2026-06-14-agent-backend-unify-design.md` (committed `9a8cb20`).

**Branch:** `worktree-unify-agent-backend`. Worktree: `/root/multi-agent/.claude/worktrees/unify-agent-backend/multi-agent/`.

**Master-path freeze:** all changes confined to `pkg/agentbackend/{claude,codex}`, `pkg/agentbackend/*.go` (NOT inside `internal/orchestrator/` / `internal/orchestration/` / `cmd/master-agent/`), `internal/driver/`, `internal/config/`, `internal/journal/`, `internal/capabilitydoc/`, `cmd/{driver,slave}-agent/`, `deploy/`, `docs/`. Verify after Task 8:
```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
# expected: empty
```

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `pkg/agentbackend/config.go` | Modify | Collapse 3 structs (`Config{Kind, Claude, Codex}` + `ClaudeConfig` + `CodexConfig`) into flat `Config{Kind, Bin, WorkDir, ExtraArgs}`. Delete `ClaudeConfig`/`CodexConfig` types. |
| `pkg/agentbackend/factory.go` | Modify | `New(cfg Config, env []string)` — remove `if kind == "" { kind = KindClaude }` implicit default (kind now required); error message includes `RegisteredKinds()` list. |
| `pkg/agentbackend/factory_test.go` | Modify | Drop `TestNewDefaultsToClaude`; add `TestNewRequiresKind`, `TestNewUnknownKindListsRegistered`, `TestRegisterBuilder_DuplicatePanics`, `TestRegisteredKinds`. |
| `pkg/agentbackend/claude/backend.go` | Modify | `New(cfg agentbackend.ClaudeConfig, env)` → `New(cfg agentbackend.Config, env)`. `init()` builder passes `cfg` straight through. Apply factory default `if cfg.Bin == "" { cfg.Bin = "claude" }`. |
| `pkg/agentbackend/codex/backend.go` | Modify | Same as claude with `"codex"` default. |
| `internal/driver/config.go` | Modify | Delete `ClaudeConfig` + `CodexConfig` types and `Config.Claude` + `Config.Codex` fields. Replace `AgentConfig{Kind}` with `AgentConfig{Kind, Bin, WorkDir, ExtraArgs}`. Delete `driverDefaultWorkDir` helper (PR #14 P1 band-aid). Delete `Planner.Bin` kind-switch defaulting (`Planner.Bin` now defaults to `Agent.Bin`). Add `LoadConfig` legacy-key peek + required-field validation. |
| `internal/driver/config_test.go` | Modify | Rewrite fixture YAMLs: delete `claude:`/`codex:` blocks; YAMLs now contain `agent: { kind, bin, workdir, extra_args }`. Delete `TestLoadConfig_DriverDefaultsWorkDir*` series (PR #14 P1 tests for the deleted band-aid). Add `TestLoadConfig_RequiresAgentKind`, `_RequiresAgentWorkDir`, `_RejectsUnknownAgentKind`, `_RejectsLegacyClaudeKey`, `_RejectsLegacyCodexKey`, `_AgentBinDefaultsViaFactory`. |
| `internal/config/config.go` | Modify | Same flattening as driver: delete `Claude`/`Codex` fields and types; replace `Agent{Kind}` with `Agent{Kind, Bin, WorkDir, ExtraArgs}`. Delete bidirectional `Claude.WorkDir`↔`Codex.WorkDir` mirroring. Delete `Planner.Bin` kind switch. Add legacy peek + required-field validation. |
| `internal/config/config_test.go` | Modify | Same pattern as driver: rewrite fixtures, replace `TestLoad_CodexWorkDirMirrorsToClaude` + `_ClaudeWorkDirMirrorsToCodex` (both pin deleted band-aid) with negative legacy-key tests. |
| `internal/journal/journal.go` | Modify | Rename `Config.ClaudeBin` → `Config.AgentBin`. Update internal use at line 60 (`exec.CommandContext(..., j.cfg.ClaudeBin, ...)`). |
| `internal/journal/journal_test.go` | Modify | Rename `ClaudeBin:` to `AgentBin:` at lines 25, 42. |
| `internal/capabilitydoc/doc.go` | Modify | `scanCommands` (line 410-425): replace `switch cfg.Agent.Kind` with `cfg.Agent.Bin`-based logic; one branch only. |
| `cmd/driver-agent/main.go` | Modify | Replace `agentbackend.Config{Kind, Claude, Codex}` builder call with flat `Config{Kind, Bin, WorkDir, ExtraArgs}` from `cfg.Agent.*`. |
| `cmd/slave-agent/main.go` | Modify | Same flat builder. Replace `journal.Config{ClaudeBin: cfg.Claude.Bin}` with `journal.Config{AgentBin: cfg.Agent.Bin}`. Delete the `if cfg.Agent.Kind == "codex" { fileWorkDir = cfg.Codex.WorkDir }` switch — `fileWorkDir = cfg.Agent.WorkDir`. |
| `cmd/slave-agent/capabilities.go` | Modify | bash/powershell executor workdir: `cfg.Claude.WorkDir` → `cfg.Agent.WorkDir`. |
| `cmd/slave-agent/capabilities_test.go` | Create | New file: `TestBashExecutorUsesAgentWorkDir`, `TestPowerShellExecutorUsesAgentWorkDir`. |
| `deploy/linux/driver/config.yaml.template` | Modify | Replace `agent: { kind }` + separate `claude:` / `codex:` blocks with single `agent: { kind, bin, workdir, extra_args }`. Drop `__AGENT_BLOCK__` placeholder. |
| `deploy/linux/slave/config.yaml.template` | Modify | Same as driver template. |
| `deploy/windows/driver/config.yaml.template` | Modify | Same. |
| `deploy/windows/slave/config.yaml.template` | Modify | Same. |
| `deploy/linux/driver/install.sh` | Modify | Delete `AGENT_BLOCK` shell var + python3 substitution. Add `__AGENT_KIND__` / `__AGENT_BIN__` to sed pass. |
| `deploy/linux/slave/install.sh` | Modify | Same. |
| `deploy/windows/driver/install.ps1` | Modify | Add `$config.Replace("__AGENT_KIND__", ...)` + `__AGENT_BIN__`. Drop old per-kind YAML emission. |
| `deploy/windows/slave/install.ps1` | Modify | Same. |
| `docs/migration/2026-06-agent-config.md` | Create | Before/after YAML diff + reason + pointer to spec. |
| `docs/superpowers/specs/2026-06-14-agent-backend-unify-evidence.md` | Create | Mirror `2026-06-14-planner-1.5-evidence.md` structure. |

---

## Task 1: Flatten `pkg/agentbackend.Config` + tighten `New`

**Files:**
- Modify: `pkg/agentbackend/config.go`
- Modify: `pkg/agentbackend/factory.go`
- Modify: `pkg/agentbackend/factory_test.go`
- Modify: `pkg/agentbackend/claude/backend.go`
- Modify: `pkg/agentbackend/codex/backend.go`

### Step 1.1: Write failing factory tests

- [ ] **Replace `pkg/agentbackend/factory_test.go` contents entirely**

```go
package agentbackend_test

import (
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

// TestNewRequiresKind pins the §issue-15 contract: agent.kind is
// required, no implicit default. Empty kind must error rather than
// silently routing to claude.
func TestNewRequiresKind(t *testing.T) {
	_, err := agentbackend.New(agentbackend.Config{Bin: "claude", WorkDir: "/tmp"}, nil)
	if err == nil {
		t.Fatal("expected error for empty kind")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("err should mention 'kind'; got %v", err)
	}
}

// TestNewUnknownKindListsRegistered pins that the error message
// names every registered kind so the operator can see which import
// they're missing — rather than a bare "unknown kind".
func TestNewUnknownKindListsRegistered(t *testing.T) {
	_, err := agentbackend.New(agentbackend.Config{Kind: "frobnitz", WorkDir: "/tmp"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"frobnitz", "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err missing %q: %v", want, err)
		}
	}
}

// TestNewDispatchesToFactory exercises the registered claude path
// using the flat Config — proves the rename + flatten path works
// end-to-end.
func TestNewDispatchesClaude(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindClaude,
		Bin:     "claude",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindClaude {
		t.Fatalf("kind=%v", b.Kind())
	}
}

func TestNewDispatchesCodex(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindCodex,
		Bin:     "codex",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindCodex {
		t.Fatalf("kind=%v", b.Kind())
	}
}

// TestRegisteredKinds is the introspection helper used by the LoadConfig
// validators (so the error message there can also list available kinds).
func TestRegisteredKinds(t *testing.T) {
	kinds := agentbackend.RegisteredKinds()
	if len(kinds) < 2 {
		t.Fatalf("expected at least claude+codex registered; got %v", kinds)
	}
	var hasClaude, hasCodex bool
	for _, k := range kinds {
		if k == string(agentbackend.KindClaude) {
			hasClaude = true
		}
		if k == string(agentbackend.KindCodex) {
			hasCodex = true
		}
	}
	if !hasClaude || !hasCodex {
		t.Fatalf("missing builtin kinds; got %v", kinds)
	}
}
```

### Step 1.2: Run tests — expect compile errors

- [ ] **Verify RED**

```bash
go test ./pkg/agentbackend/... -run "TestNew|TestRegistered" -count=1 -v
```

Expected: compile error — `agentbackend.Config{Bin,WorkDir}` doesn't have those fields yet, and `agentbackend.RegisteredKinds` doesn't exist.

### Step 1.3: Flatten `pkg/agentbackend/config.go`

- [ ] **Replace `pkg/agentbackend/config.go` entirely**

```go
package agentbackend

// Config is the single per-backend carrier shape consumed by every
// builder registered with RegisterBuilder. Driver and slave both
// build this from cfg.Agent.* (see internal/{driver,config}/config.go).
//
// Backend-specific fields previously lived in separate ClaudeConfig /
// CodexConfig structs; that arrangement multiplied schema work each
// time a backend was added. The flat shape means a new backend
// (e.g. opencode) only adds a sub-package — no Config edit here.
// Fixes issue #15.
type Config struct {
	Kind      Kind     `yaml:"kind"`
	Bin       string   `yaml:"bin"`
	WorkDir   string   `yaml:"workdir"`
	ExtraArgs []string `yaml:"extra_args"`
}
```

(Note: `ClaudeConfig` and `CodexConfig` types **deleted**. The yaml tags here are informational only — driver/slave configs don't decode Config directly; they build it from their own `AgentConfig` struct.)

### Step 1.4: Update `pkg/agentbackend/factory.go`

- [ ] **Replace `factory.go` entirely**

```go
package agentbackend

import (
	"fmt"
	"sort"
)

// Builder constructs a Backend from a flat Config. Backend packages
// register a Builder via RegisterBuilder() in their init().
type Builder func(Config, []string) (Backend, error)

var builders = make(map[Kind]Builder)

// RegisterBuilder installs a Builder for kind. Duplicate registration
// for the same kind panics — that almost always means two backend
// packages tried to claim the same name during init().
func RegisterBuilder(kind Kind, b Builder) {
	if _, dup := builders[kind]; dup {
		panic("agentbackend: duplicate RegisterBuilder for " + string(kind))
	}
	builders[kind] = b
}

// RegisteredKinds returns sorted names of every kind currently
// registered. LoadConfig in driver+slave use this to surface a
// helpful error when YAML specifies an unknown agent.kind.
func RegisteredKinds() []string {
	out := make([]string, 0, len(builders))
	for k := range builders {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// New builds the Backend for cfg.Kind. Empty kind is rejected (no
// implicit default — see §issue-15). Unknown kind reports the
// available set so the operator can spot a missing import.
func New(cfg Config, env []string) (Backend, error) {
	if cfg.Kind == "" {
		return nil, fmt.Errorf("agentbackend: kind is required; one of %v", RegisteredKinds())
	}
	b, ok := builders[cfg.Kind]
	if !ok {
		return nil, fmt.Errorf("agentbackend: unknown kind %q; registered: %v", cfg.Kind, RegisteredKinds())
	}
	return b(cfg, env)
}
```

### Step 1.5: Update `pkg/agentbackend/claude/backend.go`

- [ ] **Replace `New` signature + `init()` builder body**

Find:
```go
func New(cfg agentbackend.ClaudeConfig, env []string) *Backend {
	return &Backend{
		cfg:  cfg,
		env:  env,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}
```

Replace with:
```go
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "claude"
	}
	return &Backend{
		cfg:  cfg,
		env:  env,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}
```

Change the struct's `cfg` field type to match:
```go
type Backend struct {
	cfg  agentbackend.Config   // was: agentbackend.ClaudeConfig
	env  []string
	exec *executor
	perm *Store
	llm  *llmRunner
}
```

Replace the `init()` block:
```go
func init() {
	agentbackend.RegisterBuilder(agentbackend.KindClaude, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
```

### Step 1.6: Cascade type change through claude package

- [ ] **`pkg/agentbackend/claude/executor.go` + `llm.go` + `permissions.go` etc.**

Anywhere that uses `agentbackend.ClaudeConfig` as a parameter type, replace with `agentbackend.Config`. The fields `Bin`, `WorkDir`, `ExtraArgs` exist on the new flat type unchanged.

Run a search to surface all sites:
```bash
grep -rn "agentbackend.ClaudeConfig" pkg/agentbackend/claude/
```

For each match, replace `agentbackend.ClaudeConfig` with `agentbackend.Config`.

Also: `newExecutorWithSocketHook(cfg agentbackend.ClaudeConfig, env, hook)` at executor.go:46 — same rename.

### Step 1.7: Update `pkg/agentbackend/codex/backend.go`

- [ ] **Mirror the claude changes**

```go
func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "codex"
	}
	return &Backend{
		cfg:  cfg,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}
```

```go
type Backend struct {
	cfg  agentbackend.Config   // was: agentbackend.CodexConfig
	exec *executor
	perm *Store
	llm  *llmRunner
}
```

```go
func init() {
	agentbackend.RegisterBuilder(agentbackend.KindCodex, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
```

### Step 1.8: Cascade type change through codex package

- [ ] **Same as Step 1.6 for codex**

```bash
grep -rn "agentbackend.CodexConfig" pkg/agentbackend/codex/
```

Replace each match with `agentbackend.Config`.

### Step 1.9: Update internal callers in `cmd/{driver,slave}-agent/main.go` to compile

The plan converts these fully in Tasks 5 + 6; for now do the **minimum** to keep the tree compiling between commits — replace the per-kind sub-config build with the flat one, but keep reading from the old `cfg.Claude` / `cfg.Codex` for now.

- [ ] **`cmd/driver-agent/main.go` — change the agentbackend builder call (around line 178-182)**

Find:
```go
backend, err := agentbackend.New(agentbackend.Config{
	Kind:   cfg.Agent.Kind,
	Claude: agentbackend.ClaudeConfig{Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, ExtraArgs: cfg.Claude.Args},
	Codex:  agentbackend.CodexConfig{Bin: cfg.Codex.Bin, WorkDir: cfg.Codex.WorkDir, ExtraArgs: cfg.Codex.Args},
}, env)
```

Replace with **interim code** that picks bin/workdir/args from the per-kind config so we keep behaviour identical until Task 3-5 lands the schema change:
```go
agentCfg := agentbackend.Config{Kind: agentbackend.Kind(cfg.Agent.Kind)}
switch cfg.Agent.Kind {
case "claude":
	agentCfg.Bin = cfg.Claude.Bin
	agentCfg.WorkDir = cfg.Claude.WorkDir
	agentCfg.ExtraArgs = cfg.Claude.Args
case "codex":
	agentCfg.Bin = cfg.Codex.Bin
	agentCfg.WorkDir = cfg.Codex.WorkDir
	agentCfg.ExtraArgs = cfg.Codex.Args
}
backend, err := agentbackend.New(agentCfg, env)
```

This is **temporary** — it goes away in Task 5 when `cfg.Agent.*` carries the fields directly. Comment with `// TODO(issue-15 Task 5): replace switch with direct cfg.Agent.{Bin,WorkDir,ExtraArgs}`.

- [ ] **`cmd/slave-agent/main.go` — same surgery at the agentbackend.New call (around line 191-195)**

Same shape:
```go
agentCfg := agentbackend.Config{Kind: agentbackend.Kind(cfg.Agent.Kind)}
switch cfg.Agent.Kind {
case "claude":
	agentCfg.Bin = cfg.Claude.Bin
	agentCfg.WorkDir = cfg.Claude.WorkDir
	agentCfg.ExtraArgs = cfg.Claude.Args
case "codex":
	agentCfg.Bin = cfg.Codex.Bin
	agentCfg.WorkDir = cfg.Codex.WorkDir
	agentCfg.ExtraArgs = cfg.Codex.Args
}
backend, err := agentbackend.New(agentCfg, env)
```

Same `// TODO(issue-15 Task 5)` comment.

### Step 1.10: GREEN

- [ ] **Run focused tests + full build**

```bash
go build ./...
go vet ./...
go test ./pkg/agentbackend/... -count=1 -v
```

Expected: builds clean, all 5 new factory tests PASS, claude/codex package tests still PASS.

### Step 1.11: Commit

- [ ] **Commit Task 1**

```bash
git add pkg/agentbackend/config.go pkg/agentbackend/factory.go pkg/agentbackend/factory_test.go pkg/agentbackend/claude/ pkg/agentbackend/codex/ cmd/driver-agent/main.go cmd/slave-agent/main.go
git commit -m "refactor(agentbackend): flatten Config + require explicit kind (issue #15 Task 1)

Collapse Config{Kind, Claude, Codex} + ClaudeConfig + CodexConfig into
one flat Config{Kind, Bin, WorkDir, ExtraArgs}. Backend builders read
the same shape — adding a new backend (opencode, etc.) is now a single
new sub-package, no schema edit here.

New() now requires cfg.Kind explicitly (the old implicit 'default to
claude' was technical debt). Unknown kind error names every registered
kind so a missing 'import _' is obvious to the operator.

RegisteredKinds() exposes the registry for LoadConfig validators
(internal/{driver,config}/config.go in Task 3).

Interim TODO comments in cmd/{driver,slave}-agent/main.go bridge to
Tasks 3-5 — the switch on cfg.Agent.Kind goes away when Agent gains
the Bin/WorkDir/ExtraArgs fields.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `internal/journal` rename `ClaudeBin` → `AgentBin`

**Files:**
- Modify: `internal/journal/journal.go`
- Modify: `internal/journal/journal_test.go`
- Modify: `cmd/slave-agent/main.go`

### Step 2.1: Update test to use new field name (RED)

- [ ] **`internal/journal/journal_test.go` — replace `ClaudeBin:` references at lines 25 and 42**

Find:
```go
j, err := New(Config{Dir: dir, ClaudeBin: fakeTextClaude(t)})
```
Replace:
```go
j, err := New(Config{Dir: dir, AgentBin: fakeTextClaude(t)})
```

Find:
```go
j, _ := New(Config{Dir: dir, ClaudeBin: fakeTextClaude(t), Env: []string{"FAKE_CLAUDE_TEXT_MODE=fail"}})
```
Replace:
```go
j, _ := New(Config{Dir: dir, AgentBin: fakeTextClaude(t), Env: []string{"FAKE_CLAUDE_TEXT_MODE=fail"}})
```

### Step 2.2: Confirm RED

- [ ] **Run journal tests**

```bash
go test ./internal/journal/... -count=1
```

Expected: compile error — `Config.AgentBin` undefined.

### Step 2.3: Implement rename in `journal.go`

- [ ] **`internal/journal/journal.go:19` — rename field**

Find:
```go
type Config struct {
	Dir       string
	ClaudeBin string
	Env       []string
}
```
Replace:
```go
type Config struct {
	Dir string
	// AgentBin is the path to the agent CLI binary used to merge
	// capability-change events into CURRENT_STATE.md. Renamed from
	// ClaudeBin in issue #15 — on codex slaves the field used to
	// silently point at a non-existent 'claude' binary because the
	// caller hardcoded cfg.Claude.Bin. Caller now passes
	// cfg.Agent.Bin so it matches whichever backend is configured.
	AgentBin string
	Env      []string
}
```

- [ ] **`internal/journal/journal.go:60` — update internal use**

Find:
```go
cmd := exec.CommandContext(ctx, j.cfg.ClaudeBin, "--print")
```
Replace:
```go
cmd := exec.CommandContext(ctx, j.cfg.AgentBin, "--print")
```

### Step 2.4: Update caller in `cmd/slave-agent/main.go`

- [ ] **Line 167 — pass cfg.Agent.Bin (still reads old struct until Task 3)**

Find:
```go
j, err := journal.New(journal.Config{Dir: journalDir, ClaudeBin: cfg.Claude.Bin})
```

Replace with **interim** logic mirroring Task 1's switch (will simplify in Task 5):
```go
// Interim: pick the right bin by kind until Task 5 unifies cfg.Agent.Bin.
journalBin := cfg.Claude.Bin
if cfg.Agent.Kind == "codex" {
	journalBin = cfg.Codex.Bin
}
j, err := journal.New(journal.Config{Dir: journalDir, AgentBin: journalBin})
```

Note: this also **fixes bug #2** (codex slave used to call non-existent claude binary). The interim block goes away in Task 5.

### Step 2.5: GREEN

- [ ] **Run journal + slave-agent**

```bash
go test ./internal/journal/... ./cmd/slave-agent/... -count=1
go build ./...
```

Expected: builds clean, journal tests PASS, slave-agent tests PASS.

### Step 2.6: Commit

```bash
git add internal/journal/journal.go internal/journal/journal_test.go cmd/slave-agent/main.go
git commit -m "fix(journal): rename ClaudeBin to AgentBin and pick by kind (issue #15 Task 2 + bug)

journal.Config.ClaudeBin was hardcoded as the claude binary even on
codex slaves where cfg.Claude.Bin is the wrong/missing executable.
The merge path silently dropped capability-change events.

Rename the field to AgentBin (semantic accuracy — it's the agent CLI,
not 'the claude CLI'). Slave-agent caller now picks by cfg.Agent.Kind
(switch removed in Task 5 once cfg.Agent.Bin is the single source).

Fixes one of the three bugs flagged in issue #15 surfacing.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Flatten `internal/driver/config` schema

**Files:**
- Modify: `internal/driver/config.go`
- Modify: `internal/driver/config_test.go`

### Step 3.1: Write failing config tests first

- [ ] **Append to `internal/driver/config_test.go`** (preserve existing TestLoadConfig_FullRoundTrip etc.; the band-aid tests are removed in Step 3.4)

```go
// TestLoadConfig_RequiresAgentKind pins issue #15: agent.kind is
// mandatory; no implicit default.
func TestLoadConfig_RequiresAgentKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  workdir: /tmp/proj
  bin: claude
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing agent.kind")
	}
	if !strings.Contains(err.Error(), "agent.kind") {
		t.Fatalf("err should mention agent.kind; got %v", err)
	}
}

// TestLoadConfig_RequiresAgentWorkDir pins issue #15: agent.workdir
// is mandatory; no fallback to cwd (PR #14 P1 band-aid removed).
func TestLoadConfig_RequiresAgentWorkDir(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  bin: claude
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing agent.workdir")
	}
	if !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should mention agent.workdir; got %v", err)
	}
}

// TestLoadConfig_RejectsUnknownAgentKind names the registered set so
// operators can see what import they're missing.
func TestLoadConfig_RejectsUnknownAgentKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: opencode
  workdir: /tmp/proj
  bin: opencode
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown agent.kind")
	}
	for _, want := range []string{"opencode", "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err missing %q: %v", want, err)
		}
	}
}

// TestLoadConfig_RejectsLegacyClaudeKey gives operators an actionable
// migration error instead of yaml's generic "unknown field" noise.
func TestLoadConfig_RejectsLegacyClaudeKey(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
claude:
  workdir: /tmp/old
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for legacy claude: key")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should name 'claude' legacy key and point at agent.workdir; got %v", err)
	}
}

// TestLoadConfig_RejectsLegacyCodexKey same for codex.
func TestLoadConfig_RejectsLegacyCodexKey(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: codex
  workdir: /tmp/proj
  bin: codex
codex:
  workdir: /tmp/old
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for legacy codex: key")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err should name 'codex' legacy key; got %v", err)
	}
}

// TestLoadConfig_HappyPathClaude pins the new YAML shape.
func TestLoadConfig_HappyPathClaude(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
  extra_args:
    - --foo
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "claude" {
		t.Errorf("kind=%q", c.Agent.Kind)
	}
	if c.Agent.WorkDir != "/tmp/proj" {
		t.Errorf("workdir=%q", c.Agent.WorkDir)
	}
	if c.Agent.Bin != "claude" {
		t.Errorf("bin=%q", c.Agent.Bin)
	}
	if len(c.Agent.ExtraArgs) != 1 || c.Agent.ExtraArgs[0] != "--foo" {
		t.Errorf("extra_args=%v", c.Agent.ExtraArgs)
	}
}

// writeTempYAML is a tiny helper used by the tests above. If
// internal/driver/config_test.go already has it (it does today for
// existing TestLoadConfig_FullRoundTrip), reuse it — otherwise add.
func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
```

Confirm the imports include `os`, `path/filepath`, `strings`. If the file already has `writeTempYAML` (it does), don't duplicate — drop the helper from the new tests.

### Step 3.2: Verify RED

```bash
go test ./internal/driver/ -run "TestLoadConfig_Requires|TestLoadConfig_RejectsUnknown|TestLoadConfig_RejectsLegacy|TestLoadConfig_HappyPathClaude" -count=1 -v
```

Expected: tests FAIL because (a) `Agent` struct has only `Kind` today, (b) no legacy-peek, (c) no validation.

### Step 3.3: Flatten `internal/driver/config.go` schema

- [ ] **Replace `AgentConfig` struct definition (around line 31-33)**

Find:
```go
type AgentConfig struct {
	Kind string `yaml:"kind"` // "claude" | "codex"; default claude
}
```

Replace:
```go
// AgentConfig is the single per-backend descriptor consumed by both
// the agent runtime (agentbackend.New) and the driver-only paths
// (jail roots, planner bin default). Previously this was split
// across claude:/codex: top-level YAML blocks plus a tiny
// AgentConfig{Kind} stub; collapsed in issue #15.
type AgentConfig struct {
	Kind      string   `yaml:"kind"`
	Bin       string   `yaml:"bin"`
	WorkDir   string   `yaml:"workdir"`
	ExtraArgs []string `yaml:"extra_args"`
}
```

- [ ] **Delete `ClaudeConfig` and `CodexConfig` types entirely (around lines 35-45)**

- [ ] **Update `Config` struct (around line 17-29)**

Find:
```go
type Config struct {
	Server         ServerConfig        `yaml:"server"`
	Credentials    Credentials         `yaml:"credentials"`
	Agent          AgentConfig         `yaml:"agent"`
	Claude         ClaudeConfig        `yaml:"claude"`
	Codex          CodexConfig         `yaml:"codex"`
	Discovery      Discovery           `yaml:"discovery"`
	...
}
```

Remove the `Claude` and `Codex` lines:
```go
type Config struct {
	Server         ServerConfig        `yaml:"server"`
	Credentials    Credentials         `yaml:"credentials"`
	Agent          AgentConfig         `yaml:"agent"`
	Discovery      Discovery           `yaml:"discovery"`
	ListenAddr     string              `yaml:"listen_addr"`
	Planner        agentconfig.Planner `yaml:"planner"`
	Fanout         agentconfig.Fanout  `yaml:"fanout"`
	DriverDefaults DriverDefaults      `yaml:"driver_defaults"`
	Observer       Observer            `yaml:"observer,omitempty"`
}
```

### Step 3.4: Rewrite `LoadConfig` validation block

Currently around lines 119-170. Find the **whole block** from where `dec.Decode(&c)` succeeds through where DriverDefaults.WorkDir gets set, and replace with:

- [ ] **Replace LoadConfig validation block**

```go
// Legacy-key peek: produce a friendly migration error before the
// unknown-fields decoder buries it as a generic message. Probe
// without DisallowUnknownFields so we can recognise the old shape.
type legacyProbe struct {
	Claude any `yaml:"claude"`
	Codex  any `yaml:"codex"`
}
var probe legacyProbe
_ = yaml.Unmarshal(b, &probe)
var legacy []string
if probe.Claude != nil {
	legacy = append(legacy, "claude")
}
if probe.Codex != nil {
	legacy = append(legacy, "codex")
}
if len(legacy) > 0 {
	return nil, fmt.Errorf("config %s: legacy top-level key(s) %v are no longer supported; consolidate into agent: { kind, bin, workdir, extra_args }. See docs/migration/2026-06-agent-config.md", path, legacy)
}

// Required-field validation (no implicit defaults — see issue #15).
if c.Agent.Kind == "" {
	return nil, fmt.Errorf("config %s: agent.kind is required (one of %v)", path, agentbackend.RegisteredKinds())
}
if c.Agent.WorkDir == "" {
	return nil, fmt.Errorf("config %s: agent.workdir is required", path)
}
if !isRegisteredKind(c.Agent.Kind) {
	return nil, fmt.Errorf("config %s: unknown agent.kind %q; registered: %v", path, c.Agent.Kind, agentbackend.RegisteredKinds())
}

// Defaults from the registered factory (Bin only — WorkDir is
// required, ExtraArgs default-empty).
if c.Agent.Bin == "" {
	c.Agent.Bin = c.Agent.Kind // factory will recognise "" too, but explicit is clearer
}

if c.DriverDefaults.TaskTimeoutSec == 0 {
	c.DriverDefaults.TaskTimeoutSec = 600
}
if c.DriverDefaults.MaxDirCacheEntries == 0 {
	c.DriverDefaults.MaxDirCacheEntries = 50000
}
if c.DriverDefaults.ArtifactTransport == "" {
	c.DriverDefaults.ArtifactTransport = ArtifactTransportPeerProxy
}
if c.Planner.Bin == "" {
	c.Planner.Bin = c.Agent.Bin
}
if c.Planner.TimeoutSec == 0 {
	c.Planner.TimeoutSec = 60
}
if c.Fanout.MaxConcurrency == 0 {
	c.Fanout.MaxConcurrency = 4
}
if c.Fanout.SubTaskDefaults.TimeoutSec == 0 {
	c.Fanout.SubTaskDefaults.TimeoutSec = c.DriverDefaults.TaskTimeoutSec
}
// driver_defaults.workdir defaults to agent.workdir (jail root).
if c.DriverDefaults.WorkDir == "" {
	c.DriverDefaults.WorkDir = c.Agent.WorkDir
}
```

Add a helper at file end:
```go
// isRegisteredKind asks the agentbackend registry whether kind is
// claimed by some imported backend package. Wrapped so we can mock
// in unit tests if needed (none today; left as a private helper for
// future stub-friendliness).
func isRegisteredKind(kind string) bool {
	for _, k := range agentbackend.RegisteredKinds() {
		if k == kind {
			return true
		}
	}
	return false
}
```

Add `"github.com/yourorg/multi-agent/pkg/agentbackend"` to imports.

Also **delete**:
- `driverDefaultWorkDir(c *Config)` function (around line 248-264) — band-aid no longer needed
- the old `if c.Codex.WorkDir == "" { c.Codex.WorkDir = c.Claude.WorkDir }` mirror at lines 146-148
- the old `if c.Claude.WorkDir == "" { c.Claude.WorkDir = c.Codex.WorkDir }` reverse mirror
- the old `if c.Agent.Kind == "" { c.Agent.Kind = "claude" }` implicit default at lines 137-139
- the old `if c.Claude.Bin == "" { c.Claude.Bin = "claude" }` blocks at lines 140-145
- the old `switch c.Agent.Kind` Planner.Bin block at lines 157-165 (replaced by `c.Planner.Bin = c.Agent.Bin`)

### Step 3.5: Remove obsolete tests

- [ ] **Delete `TestLoadConfig_DriverDefaultsWorkDir*` series** in `internal/driver/config_test.go` (4 tests added in PR #14 P1 — they pinned the deleted `driverDefaultWorkDir` band-aid)

Find each `func TestLoadConfig_DriverDefaultsWorkDir...` test and delete its full body. There are 4: `DefaultsToClaudeWorkdir`, `CodexAgentUsesCodexWorkdir`, `CwdFallback`, `ExplicitWins`. The semantics they pinned (`driver_defaults.workdir` mirrors `agent.workdir`) is now covered by the LoadConfig assignment at the end of Step 3.3.

Also rewrite any existing test that uses `claude:` / `codex:` YAML blocks (e.g. `TestLoadConfig_FullRoundTrip`, `TestLoadConfig_DefaultsApplied`, `TestLoadConfig_ObserverForceRegisterRoundTrip`, etc.) to use the new `agent:` block shape.

Search for them:
```bash
grep -n "claude:\|codex:" internal/driver/config_test.go
```

Replace those YAML lines. The agent block format:
```yaml
agent:
  kind: claude
  bin: claude
  workdir: /tmp/proj
  extra_args: []
```

### Step 3.6: GREEN

```bash
go test ./internal/driver/... -count=1 -v
```

Expected: all PASS (new validation tests + rewritten existing tests).

If `cmd/driver-agent/main.go` now fails to compile because it references `cfg.Claude.Bin` / `cfg.Codex.Bin` etc. — those are intentionally left for Task 5. Stage the file change there. Until then, the **temporary fix** is to also update the `switch cfg.Agent.Kind` interim block from Task 1 step 1.9 to read from the new `cfg.Agent.*` instead — since `cfg.Claude` and `cfg.Codex` no longer exist:

```go
// Updated interim — Task 5 will collapse this to a one-liner.
agentCfg := agentbackend.Config{
	Kind:      agentbackend.Kind(cfg.Agent.Kind),
	Bin:       cfg.Agent.Bin,
	WorkDir:   cfg.Agent.WorkDir,
	ExtraArgs: cfg.Agent.ExtraArgs,
}
backend, err := agentbackend.New(agentCfg, env)
```

The "interim" comment / TODO is now redundant — strip it; the line is the final shape. Same for `cmd/slave-agent/main.go` 's agentbackend.New call. **And the journal interim block from Task 2 step 2.4** is now `journalBin := cfg.Agent.Bin`.

```bash
go build ./...
go vet ./...
```

Expected: clean compile.

### Step 3.7: Commit

```bash
git add internal/driver/config.go internal/driver/config_test.go cmd/driver-agent/main.go cmd/slave-agent/main.go
git commit -m "refactor(driver,slave-agent): flatten config to agent.* + legacy peek (issue #15 Task 3)

internal/driver/config.go drops top-level claude: / codex: YAML blocks
and the ClaudeConfig + CodexConfig types. AgentConfig gains Bin,
WorkDir, ExtraArgs — the single source of per-backend config.

LoadConfig validates agent.kind required, agent.workdir required,
kind in the agentbackend.RegisteredKinds() set. A legacy-key peek
before the unknown-fields decoder reports old YAML with an actionable
migration message pointing at docs/migration/2026-06-agent-config.md.

Removed PR #14 P1 band-aid: driverDefaultWorkDir, claude/codex
WorkDir bidirectional mirror, AgentConfig.Kind implicit default.
Tests that pinned the band-aid (TestLoadConfig_DriverDefaultsWorkDir*)
are deleted; semantics now covered by DriverDefaults.WorkDir =
Agent.WorkDir assignment.

cmd/{driver,slave}-agent/main.go updated to build the flat
agentbackend.Config directly from cfg.Agent.*; Task 1's interim
switch goes away. journal.AgentBin is now cfg.Agent.Bin.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Flatten `internal/config` (slave) schema

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

The slave config mirror of Task 3. The shape is identical; the differences are:
- Slave has no `DriverDefaults.WorkDir` (that's driver-only)
- Slave's `Planner.Bin` default is identical to driver
- Existing tests `TestLoad_CodexWorkDirMirrorsToClaude` + `TestLoad_ClaudeWorkDirMirrorsToCodex` (PR #14 P1 band-aid) are also deleted here

### Step 4.1: Failing tests

- [ ] **Append to `internal/config/config_test.go`** — same 6 tests as driver Step 3.1 but with slave-specific yaml fragments (use `slave:` discovery shape if it differs, but the surface is identical for the Agent block)

Use this template (adapt YAML to slave's required server/discovery shape):

```go
func TestSlaveLoad_RequiresAgentKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  workdir: /tmp/proj
  bin: claude
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatalf("expected error; got %+v", c)
	}
	if !strings.Contains(err.Error(), "agent.kind") {
		t.Fatalf("err should mention agent.kind; got %v", err)
	}
}

func TestSlaveLoad_RequiresAgentWorkDir(t *testing.T) { /* ... same pattern ... */ }
func TestSlaveLoad_RejectsUnknownAgentKind(t *testing.T) { /* ... */ }
func TestSlaveLoad_RejectsLegacyClaudeKey(t *testing.T) { /* ... */ }
func TestSlaveLoad_RejectsLegacyCodexKey(t *testing.T) { /* ... */ }
func TestSlaveLoad_HappyPathCodex(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: codex
  workdir: /tmp/proj
  bin: codex
  extra_args:
    - --foo
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err != nil { t.Fatal(err) }
	if c.Agent.Kind != "codex" { t.Errorf("kind=%q", c.Agent.Kind) }
	if c.Agent.WorkDir != "/tmp/proj" { t.Errorf("workdir=%q", c.Agent.WorkDir) }
	if c.Agent.Bin != "codex" { t.Errorf("bin=%q", c.Agent.Bin) }
}
```

If `loadFromString(t, yaml)` doesn't exist yet, add it:
```go
func loadFromString(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil { t.Fatal(err) }
	return Load(p)
}
```

(Slave's loader function may be named differently — `Load`, `LoadConfig`, etc. — check the package and adapt.)

### Step 4.2: RED

```bash
go test ./internal/config/ -run "TestSlaveLoad_" -count=1 -v
```

Expected: tests FAIL.

### Step 4.3: Apply the same schema changes to `internal/config/config.go`

Mirror Task 3 step 3.3-3.4:
- Replace `Agent{Kind}` struct with `Agent{Kind, Bin, WorkDir, ExtraArgs}`
- Delete top-level `Claude` and `Codex` fields + types
- Add legacy peek + required-field validation + RegisteredKinds check
- Remove the bidirectional WorkDir mirror (lines 140-145)
- Remove the implicit `Agent.Kind = "claude"` default (line 128-130)
- Replace `switch c.Agent.Kind` Planner.Bin block with `if c.Planner.Bin == "" { c.Planner.Bin = c.Agent.Bin }`

Add `"github.com/yourorg/multi-agent/pkg/agentbackend"` to imports.

### Step 4.4: Delete obsolete tests

- [ ] **Find and delete in `internal/config/config_test.go`**:
- `TestLoad_CodexWorkDirMirrorsToClaude`
- `TestLoad_ClaudeWorkDirMirrorsToCodex`

Also rewrite YAML fixtures in existing tests to use the `agent:` block:
```bash
grep -n "claude:\|codex:" internal/config/config_test.go
```

### Step 4.5: GREEN

```bash
go test ./internal/config/... ./cmd/slave-agent/... -count=1
go build ./...
go vet ./...
```

Expected: clean. cmd/slave-agent/main.go's agentbackend.New call was already moved to `cfg.Agent.*` in Step 3.6 — no further bridge needed.

### Step 4.6: Commit

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "refactor(slave config): flatten to agent.* + legacy peek (issue #15 Task 4)

Mirror of the driver-side schema flattening (Task 3) applied to
internal/config/config.go (slave). Same legacy-key migration error,
same required-field validation, same removal of PR #14 P1
bidirectional WorkDir mirror.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Slave bash/powershell workdir + delete fileWorkDir switch

**Files:**
- Modify: `cmd/slave-agent/main.go`
- Modify: `cmd/slave-agent/capabilities.go`
- Create: `cmd/slave-agent/capabilities_test.go`

### Step 5.1: Write failing tests for capabilities

- [ ] **Create `cmd/slave-agent/capabilities_test.go`**

```go
package main

import (
	"testing"

	"github.com/yourorg/multi-agent/internal/config"
)

// TestBashExecutorUsesAgentWorkDir pins the issue #15 surfacing
// bug: bash executor previously hardcoded cfg.Claude.WorkDir, which
// is missing on codex slaves. Now it reads cfg.Agent.WorkDir.
func TestBashExecutorUsesAgentWorkDir(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Kind = "codex"
	cfg.Agent.WorkDir = "/expected/agent/workdir"

	wd := bashExecutorWorkDir(cfg)
	if wd != "/expected/agent/workdir" {
		t.Errorf("bash workdir=%q want /expected/agent/workdir", wd)
	}
}

// TestPowerShellExecutorUsesAgentWorkDir same defence for the
// powershell capability path.
func TestPowerShellExecutorUsesAgentWorkDir(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Kind = "codex"
	cfg.Agent.WorkDir = "/expected/agent/workdir"

	wd := powerShellExecutorWorkDir(cfg)
	if wd != "/expected/agent/workdir" {
		t.Errorf("powershell workdir=%q want /expected/agent/workdir", wd)
	}
}
```

The helpers `bashExecutorWorkDir` and `powerShellExecutorWorkDir` don't exist yet — they're 1-line extractions in the next step. This is **test-then-extract** for testability.

### Step 5.2: Verify RED

```bash
go test ./cmd/slave-agent/ -run "TestBashExecutor|TestPowerShellExecutor" -count=1 -v
```

Expected: compile error — helpers don't exist.

### Step 5.3: Extract the helpers + fix the bug

- [ ] **`cmd/slave-agent/capabilities.go:30,33` — read first to confirm current shape**

Look for the two sites that currently use `cfg.Claude.WorkDir`:
```bash
grep -n "Claude.WorkDir\|Codex.WorkDir" cmd/slave-agent/capabilities.go
```

For each, extract a small helper at the top of the file (preserves the existing wiring shape so the call sites change minimally):

```go
// bashExecutorWorkDir returns the working directory for the bash
// executor. Reads cfg.Agent.WorkDir — previously hardcoded
// cfg.Claude.WorkDir, which was wrong on codex slaves.
// Fixes one of the bugs surfaced by issue #15.
func bashExecutorWorkDir(cfg *config.Config) string {
	return cfg.Agent.WorkDir
}

// powerShellExecutorWorkDir same as bash, for the PowerShell capability.
func powerShellExecutorWorkDir(cfg *config.Config) string {
	return cfg.Agent.WorkDir
}
```

Then replace the call sites:
- Where bash executor is constructed, replace `cfg.Claude.WorkDir` with `bashExecutorWorkDir(cfg)`
- Where powershell executor is constructed, replace `cfg.Claude.WorkDir` with `powerShellExecutorWorkDir(cfg)`

### Step 5.4: Delete the fileWorkDir switch in main.go

- [ ] **`cmd/slave-agent/main.go` — around line 254-256**

Find:
```go
fileWorkDir := cfg.Claude.WorkDir
if cfg.Agent.Kind == "codex" {
	fileWorkDir = cfg.Codex.WorkDir
}
```

Replace with:
```go
fileWorkDir := cfg.Agent.WorkDir
```

### Step 5.5: GREEN

```bash
go test ./cmd/slave-agent/... -count=1
go build ./...
go vet ./...
```

Expected: all PASS, build clean.

### Step 5.6: Commit

```bash
git add cmd/slave-agent/main.go cmd/slave-agent/capabilities.go cmd/slave-agent/capabilities_test.go
git commit -m "fix(slave-agent): bash/powershell/file workdir from cfg.Agent.* (issue #15 Task 5 + bugs)

cmd/slave-agent/capabilities.go previously read cfg.Claude.WorkDir
hardcoded for both bash and powershell capabilities — on codex
slaves these capabilities silently ran in the wrong directory
(or the empty string, depending on YAML).

cmd/slave-agent/main.go had a 'if cfg.Agent.Kind == \"codex\"'
switch around file executor workdir (PR #14 P1 band-aid). Now
that cfg.Agent.WorkDir is the single source of truth, the switch
collapses to one line.

Fixes two of the three bugs surfaced in issue #15.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `internal/capabilitydoc` kind switch removal

**Files:**
- Modify: `internal/capabilitydoc/doc.go`

### Step 6.1: Read existing tests (if any) for `scanCommands`

```bash
grep -n "scanCommands\|TestScanCommands" internal/capabilitydoc/
```

If a test pins the kind switch, update its expectation in Step 6.3. Otherwise, no new test is added (this is a 4-line refactor with no behaviour change — the binary search just enumerates `cfg.Agent.Bin`).

### Step 6.2: Replace the switch

- [ ] **`internal/capabilitydoc/doc.go:410-425`**

Find:
```go
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

Replace:
```go
if cfg != nil {
	// Always probe the configured agent kind's bin (the kind name
	// is also the conventional default binary name) plus any
	// operator-overridden bin path.
	names = append(names, cfg.Agent.Kind)
	if cfg.Agent.Bin != "" && cfg.Agent.Bin != cfg.Agent.Kind {
		names = append(names, cfg.Agent.Bin)
	}
}
```

### Step 6.3: GREEN

```bash
go test ./internal/capabilitydoc/... -count=1
go build ./...
go vet ./...
```

Expected: PASS. If a test was pinning the old switch, update it to assert on `cfg.Agent.Bin` instead of `cfg.Claude.Bin` / `cfg.Codex.Bin`.

### Step 6.4: Commit

```bash
git add internal/capabilitydoc/doc.go
git commit -m "refactor(capabilitydoc): scanCommands reads cfg.Agent.* (issue #15 Task 6)

Last remaining 'switch cfg.Agent.Kind' outside the agentbackend
registry. Collapsed to a single Bin probe — same behaviour, smaller
surface.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Deploy templates + install scripts

**Files:**
- Modify: `deploy/linux/driver/config.yaml.template`
- Modify: `deploy/linux/slave/config.yaml.template`
- Modify: `deploy/windows/driver/config.yaml.template`
- Modify: `deploy/windows/slave/config.yaml.template`
- Modify: `deploy/linux/driver/install.sh`
- Modify: `deploy/linux/slave/install.sh`
- Modify: `deploy/windows/driver/install.ps1`
- Modify: `deploy/windows/slave/install.ps1`

### Step 7.1: Audit current templates

- [ ] **Read each template + installer to see what they currently emit**

```bash
for f in deploy/linux/driver/config.yaml.template deploy/linux/slave/config.yaml.template deploy/windows/driver/config.yaml.template deploy/windows/slave/config.yaml.template deploy/linux/driver/install.sh deploy/linux/slave/install.sh deploy/windows/driver/install.ps1 deploy/windows/slave/install.ps1; do
  echo "=== $f ==="
  cat "$f"
done
```

Expect to see `agent:` + separate `claude:` / `codex:` blocks emitted via `__AGENT_BLOCK__` placeholder and `AGENT_BLOCK` shell variable.

### Step 7.2: Rewrite linux driver template

- [ ] **`deploy/linux/driver/config.yaml.template`**

Replace whatever currently sits in/around the `agent:` block with a single block:
```yaml
agent:
  kind: __AGENT_KIND__
  bin: __AGENT_BIN__
  workdir: __PROJECT_DIR__
  extra_args: []
```

Delete the old `__AGENT_BLOCK__` placeholder line entirely. Make sure `driver_defaults:` still has `workdir: __PROJECT_DIR__` (PR #14 P1 follow-up substitution is preserved).

### Step 7.3: Rewrite linux slave template

- [ ] **`deploy/linux/slave/config.yaml.template`** — same shape as driver. The slave doesn't have `driver_defaults`, but `agent:` is identical.

### Step 7.4: Rewrite windows templates (driver + slave)

- [ ] **`deploy/windows/driver/config.yaml.template`** + **`deploy/windows/slave/config.yaml.template`** — same shape as their linux counterparts.

### Step 7.5: Rewrite linux installers

- [ ] **`deploy/linux/driver/install.sh`**

Look for the `AGENT_BLOCK="..."` shell variable construction (and the subsequent `python3` substitution block). Replace with sed pass additions:

Find:
```bash
if [[ "$AGENT" == "claude" ]]; then
  AGENT_BLOCK="agent:\n  kind: claude\n\nclaude:\n  bin: claude\n  workdir: $PROJECT_ABS\n  extra_args: []"
else
  AGENT_BLOCK="agent:\n  kind: codex\n\ncodex:\n  bin: codex\n  workdir: $PROJECT_ABS\n  extra_args: []"
fi
```

Delete entirely. Update the sed pass that renders config.yaml to include the new placeholders:

Find:
```bash
sed \
  -e "s|__AGENT_NAME__|$NAME|g" \
  -e "s|__DESCRIPTION__|$DESC|g" \
  -e "s|__LOOM_HOME__|$TOKEN_DIR|g" \
  -e "s|__OBSERVER_URL__|$OBSERVER_URL|g" \
  -e "s|__WORKSPACE_ID__|$WORKSPACE_ID|g" \
  -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
  "$HERE/config.yaml.template" > "$PROJECT_ABS/config.yaml"
```

Add two `-e` lines:
```bash
sed \
  -e "s|__AGENT_NAME__|$NAME|g" \
  -e "s|__DESCRIPTION__|$DESC|g" \
  -e "s|__LOOM_HOME__|$TOKEN_DIR|g" \
  -e "s|__OBSERVER_URL__|$OBSERVER_URL|g" \
  -e "s|__WORKSPACE_ID__|$WORKSPACE_ID|g" \
  -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
  -e "s|__AGENT_KIND__|$AGENT|g" \
  -e "s|__AGENT_BIN__|$AGENT|g" \
  "$HERE/config.yaml.template" > "$PROJECT_ABS/config.yaml"
```

Then delete the entire `python3 - "$PROJECT_ABS/config.yaml" "$AGENT_BLOCK" <<'PY' ... PY` block right after.

- [ ] **`deploy/linux/slave/install.sh`** — same `AGENT_BLOCK` removal + same sed additions

Search for `AGENT_BLOCK` in slave install:
```bash
grep -n "AGENT_BLOCK" deploy/linux/slave/install.sh
```

Same surgery as driver.

### Step 7.6: Rewrite windows installers

- [ ] **`deploy/windows/driver/install.ps1`** — find the equivalent agent-block emission (likely uses `Get-Content` + `Replace`)

```bash
grep -n "AGENT_BLOCK\|AGENT_KIND\|claude:\|codex:" deploy/windows/driver/install.ps1
```

Look for any per-kind `if ($Agent -eq "claude") { ... }` block and either replace it with a `$AgentBin = $Agent` extraction followed by `$config.Replace("__AGENT_KIND__", $Agent)` + `$config.Replace("__AGENT_BIN__", $AgentBin)`.

Existing code (line ~96-105 per spec):
```powershell
$config = Get-Content -LiteralPath (Join-Path $here "config.yaml.template") -Raw
$config = $config.Replace("__AGENT_NAME__", (ConvertTo-YamlQuoted $Name))
$config = $config.Replace("__AGENT_KIND__", (ConvertTo-YamlQuoted $Agent))
$config = $config.Replace("__DESCRIPTION__", (ConvertTo-YamlQuoted $description))
$config = $config.Replace("__PROJECT_DIR__", (ConvertTo-YamlQuoted $projectDir))
...
```

Already has `__AGENT_KIND__` — add `__AGENT_BIN__` next to it:
```powershell
$config = $config.Replace("__AGENT_BIN__", (ConvertTo-YamlQuoted $Agent))
```

Then delete the now-redundant per-kind block emission if present.

- [ ] **`deploy/windows/slave/install.ps1`** — same

### Step 7.7: Smoke-render each template locally

- [ ] **Reproduce the install rendering for one combination**

```bash
cd /tmp && rm -rf install-smoke && mkdir install-smoke && cd install-smoke
HERE=/root/multi-agent/.claude/worktrees/unify-agent-backend/multi-agent/deploy/linux/driver
PROJECT_ABS=/tmp/install-smoke/project
NAME=smoke DESC=smoke TOKEN_DIR=/tmp/smoke-token OBSERVER_URL=https://obs.invalid WORKSPACE_ID=ws-xyz AGENT=claude
mkdir -p $PROJECT_ABS
sed \
  -e "s|__AGENT_NAME__|$NAME|g" \
  -e "s|__DESCRIPTION__|$DESC|g" \
  -e "s|__LOOM_HOME__|$TOKEN_DIR|g" \
  -e "s|__OBSERVER_URL__|$OBSERVER_URL|g" \
  -e "s|__WORKSPACE_ID__|$WORKSPACE_ID|g" \
  -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
  -e "s|__AGENT_KIND__|$AGENT|g" \
  -e "s|__AGENT_BIN__|$AGENT|g" \
  "$HERE/config.yaml.template" > rendered.yaml
cat rendered.yaml
```

Confirm:
- No `__*__` placeholders remain
- No `claude:` / `codex:` top-level keys
- Single `agent:` block with `kind: claude`, `bin: claude`, `workdir: /tmp/install-smoke/project`

Repeat for AGENT=codex.

### Step 7.8: Commit

```bash
git add deploy/
git commit -m "deploy: templates + installers emit unified agent: block (issue #15 Task 7)

deploy/{linux,windows}/{driver,slave}/config.yaml.template no longer
contains separate claude: / codex: top-level blocks. Single agent:
block with kind, bin, workdir, extra_args.

deploy/linux/{driver,slave}/install.sh: deleted the AGENT_BLOCK shell
var + python3 substitution block. sed pass now substitutes
__AGENT_KIND__ + __AGENT_BIN__ directly.

deploy/windows/{driver,slave}/install.ps1: added __AGENT_BIN__ Replace
call alongside the existing __AGENT_KIND__.

Smoke-rendered claude and codex for linux driver — single agent block,
no legacy keys, no leftover placeholders.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Migration doc + regression + evidence + push + PR

**Files:**
- Create: `docs/migration/2026-06-agent-config.md`
- Create: `docs/superpowers/specs/2026-06-14-agent-backend-unify-evidence.md`

### Step 8.1: Write migration doc

- [ ] **Create `docs/migration/2026-06-agent-config.md`**

```markdown
# Agent Config Migration (June 2026)

`internal/driver/config.go` and `internal/config/config.go` (slave)
collapse the per-backend `claude:` / `codex:` top-level YAML blocks
into a single `agent:` block. Linked: [issue #15](https://github.com/agentserver/loom/issues/15).

## Before

```yaml
agent:
  kind: claude

claude:
  bin: claude
  workdir: /loom/project
  extra_args: []

# (codex slaves used the codex: block instead)
```

## After

```yaml
agent:
  kind: claude        # required (no implicit default)
  bin: claude         # optional; backend factory defaults to the kind name
  workdir: /loom/proj # required (no cwd fallback)
  extra_args: []
```

For codex: `agent.kind: codex`, `agent.bin: codex`. Same for any
future backend (e.g. opencode) — only the strings change.

## Migration

Edit your driver and slave config YAML(s):

1. Delete the top-level `claude:` (or `codex:`) block entirely.
2. Move its `bin`, `workdir`, and `extra_args` keys into the `agent:` block.
3. Make sure `agent.kind` is set (no default any more).

The loader emits a friendly error if it sees a legacy `claude:` or
`codex:` top-level key:

```
config /path/to/config.yaml: legacy top-level key(s) [claude] are no
longer supported; consolidate into agent: { kind, bin, workdir,
extra_args }. See docs/migration/2026-06-agent-config.md
```

## Why

`pkg/agentbackend` had per-backend sub-structs and the CLI mains had
`switch cfg.Agent.Kind { case "claude": ...; case "codex": ... }`
peppered around. Adding a new backend required ~12 file edits. After
this PR a new backend lives in `pkg/agentbackend/<name>/` and two
`_ "..."` imports — no schema changes.

## Master config

Master's `cmd/master-agent/config.go` keeps the old shape for now; it
is on the [frozen list](https://github.com/agentserver/loom/issues/15)
and will be unified in a follow-up PR.

## Deploy templates

`deploy/{linux,windows}/{driver,slave}/install.{sh,ps1}` already
render the new schema — operators using `install.sh` next time will
get the new YAML automatically.
```

### Step 8.2: Run full regression with race

- [ ] **Full ./... race**

```bash
go test ./... -race -count=1 2>&1 | tail -40
```

Expected: all `ok`. If any failure surfaces in `internal/orchestrator/` (master-path-frozen consumer that may depend on the old `cfg.Claude.*` shape), check whether the dependency is real or just a stale test fixture. If real semantics regression → file a follow-up issue and **leave it**. If stale fixture, same pattern as §1.4/§1.5: rewrite the test in this PR with a comment citing PR #14's `TestFileExecutor_ReadAbsolutePath` precedent.

### Step 8.3: Binary smoke

```bash
mkdir -p /tmp/bin-smoke-15
go build -o /tmp/bin-smoke-15/driver-agent ./cmd/driver-agent
go build -o /tmp/bin-smoke-15/slave-agent ./cmd/slave-agent
/tmp/bin-smoke-15/driver-agent 2>&1 | head -5
```

Expected: builds clean, help text appears.

### Step 8.4: Master-path freeze check

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
```

Expected: empty output. If non-empty, investigate per Step 8.2 reasoning.

### Step 8.5: Evidence doc

- [ ] **Create `docs/superpowers/specs/2026-06-14-agent-backend-unify-evidence.md`**

Mirror the §1.5 evidence doc structure (read `docs/superpowers/specs/2026-06-14-planner-1.5-evidence.md` for the template). Capture:

- Header: date, branch, scope (issue #15 + 3 bugs), not-in-scope (opencode real adoption, master config, master path frozen)
- Why unit-only e2e is sufficient: pure refactor; the registry / config / installer changes are exercised by adversarial unit tests + binary smoke; no transport / cross-service change
- Commit list (`git log --oneline master..HEAD`)
- Per-task coverage: each commit, what it changed, what the tests pin
- Full repo race result summary
- Binary smoke
- Master-path freeze confirmation (diff stat empty)
- Bugs fixed (3 of them) called out explicitly

### Step 8.6: Commit migration + evidence

```bash
git add docs/migration/2026-06-agent-config.md docs/superpowers/specs/2026-06-14-agent-backend-unify-evidence.md
git commit -m "docs: migration guide + test evidence for agent backend unify (issue #15)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

### Step 8.7: Push + open PR

```bash
git push -u origin worktree-unify-agent-backend

gh pr create --base master --head worktree-unify-agent-backend \
  --title "refactor: unify agent backend config (issue #15)" \
  --body "$(cat <<'EOF'
Fixes [issue #15](https://github.com/agentserver/loom/issues/15) — agent config split-brain — and the three bugs surfaced while exploring it.

## Scope

| Area | What |
|---|---|
| `pkg/agentbackend` | Flatten `Config{Kind, Claude, Codex}` + `ClaudeConfig` + `CodexConfig` → flat `Config{Kind, Bin, WorkDir, ExtraArgs}`. Require explicit kind. `RegisteredKinds()` for friendly errors. |
| `internal/{driver,config}/config.go` | Drop top-level `claude:` / `codex:` YAML blocks. New `agent:` block has `{ kind, bin, workdir, extra_args }`. Legacy-key peek → migration error. Required-field validation (kind, workdir). Removed PR #14 P1 band-aid: `driverDefaultWorkDir`, bidirectional WorkDir mirror, implicit `kind = "claude"` default. |
| `internal/journal` | `Config.ClaudeBin` → `AgentBin`. **Bug fix**: codex slaves silently dropped capability-change events. |
| `cmd/slave-agent/capabilities.go` | bash + powershell executor workdir from `cfg.Agent.WorkDir` (was hardcoded `cfg.Claude.WorkDir`). **Bug fix**: codex slaves ran shell capabilities in the wrong directory. |
| `cmd/{driver,slave}-agent/main.go` | Single `agentbackend.New(agentbackend.Config{...cfg.Agent.*})` call; every `switch cfg.Agent.Kind` removed. |
| `internal/capabilitydoc/doc.go` | Last `switch cfg.Agent.Kind` collapsed to one Bin probe. |
| `deploy/{linux,windows}/{driver,slave}/{config.yaml.template, install.{sh,ps1}}` | Templates emit single `agent:` block; installers substitute `__AGENT_KIND__` + `__AGENT_BIN__` via sed/Replace. |
| `docs/migration/2026-06-agent-config.md` | Before/after + why + master-config-frozen note. |

## Why

Adding a new backend (opencode etc.) used to require ~12 file edits across config structs, main.go switches, installers, and capabilitydoc. After this PR a new backend lives in `pkg/agentbackend/<name>/` plus two `_ \"...\"` imports — nothing else.

The 3 bugs we found while mapping the surface (`journal.ClaudeBin` hardcoded, bash/powershell workdir hardcoded, `agent.workdir` silent cwd fallback) are all corollaries of the split — they vanish for free once the schema is unified.

## Breaking change

YAML configs from prior versions stop loading with an actionable error pointing at `docs/migration/2026-06-agent-config.md`. Operators must consolidate their `claude:` / `codex:` block into `agent:`. Per chat: opted into this rather than carrying deprecated-key forwarders.

## Master-path freeze (still honored)

`git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'` → **empty**. Per memory `master_path_frozen`, master's own config schema is on a separate workstream.

## Testing

- Adversarial unit tests for registry (`TestNewRequiresKind`, `TestNewUnknownKindListsRegistered`, `TestRegisteredKinds`), driver/slave loaders (legacy-peek + required-field + unknown-kind + happy-path), journal bin field, bash/powershell workdir.
- Full repo race: `go test ./... -race -count=1` all green.
- Binary smoke: driver-agent / slave-agent build clean.
- Installer smoke-render: claude and codex variants of linux driver template produce single `agent:` block, no legacy keys.
- Evidence doc: `docs/superpowers/specs/2026-06-14-agent-backend-unify-evidence.md`.

## Out of scope (intentional)

- Real opencode backend (this PR just makes adding it trivial — actual integration needs separate research on opencode CLI session/streaming).
- Master config schema (frozen, follow-up issue).
- `agentbackend.Backend` interface methods (unchanged).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage:** Every section of the spec maps to a task — flatten Config (Task 1), `init()` self-registration retrofit (Task 1, builders already exist), driver schema (Task 3), slave schema (Task 4), 3 bug fixes (Tasks 2 + 5 + the `cfg.Agent.WorkDir` use that survives Task 3 means PR #14 band-aid is gone), main.go switch removal (Tasks 3 + 5), capabilitydoc (Task 6), templates + installers (Task 7), migration doc (Task 8), evidence + PR (Task 8). ✓

**Placeholder scan:** No TBD/TODO inside step bodies. Every commit step has a complete commit message. Every code step shows the actual code. Comments mention prior PRs (`PR #14 P1`, `§1.4`, `§1.5`) by name for traceability — those are concrete references, not placeholders.

**Type / signature consistency:**
- `agentbackend.Config{Kind, Bin, WorkDir, ExtraArgs}` — defined Task 1, used identically in Tasks 1.5/1.7 (claude/codex builders), Task 3.6 main.go interim, Task 4 slave-loader, Task 6 capabilitydoc.
- `agentbackend.RegisteredKinds() []string` — defined Task 1.4, called Task 3.3 (driver loader), Task 4.3 (slave loader), Task 1.1 (factory tests).
- `agentbackend.RegisterBuilder(Kind, Builder)` — preserved name (existing API). Spec called it `Register`; plan keeps the existing name to minimise churn. Reason documented in plan's Task 1 header.
- `internal/journal/Config.AgentBin` — defined Task 2.3, used Task 2.4 and (implicitly) Task 3.6 slave main.
- `bashExecutorWorkDir(cfg *config.Config) string` / `powerShellExecutorWorkDir(cfg *config.Config) string` — defined Task 5.3, asserted Task 5.1.
- `cfg.Agent.Kind` / `cfg.Agent.Bin` / `cfg.Agent.WorkDir` / `cfg.Agent.ExtraArgs` — schema defined Task 3.3 (driver) + Task 4.3 (slave), consumed Tasks 3.6/4/5/6.
- `__AGENT_KIND__` / `__AGENT_BIN__` placeholders — introduced Task 7.2-7.6, substituted Task 7.5-7.6, smoke-tested Task 7.7.

**One internal note:** Task 3.6 has the engineer modify `cmd/driver-agent/main.go` and `cmd/slave-agent/main.go` along with config_test changes. The commit message in Task 3.7 mentions both; the `git add` line in 3.7 explicitly lists both files. The Task 1.9 interim block they wrote in Task 1 is what gets simplified in Task 3.6 — same files, sequential edits, single commit at Task 3.7.
