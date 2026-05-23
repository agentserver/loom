# Codex backend support (pluggable coding-agent layer)

**Date:** 2026-05-23
**Status:** draft, awaiting user review
**Scope:** driver + slave + planner + deploy templates + docs

## Goal

Make the multi-agent system genuinely **mixed-fleet**: a single observer / workspace
can host driver and slave agents that are launched / powered by either
**Claude Code** *or* **OpenAI Codex CLI** (and, in the future, additional
coding-agent CLIs). Today, the project is hard-wired to Claude Code in three
places — slave's `chat` executor, slave's `claude_permissions` executor, and
driver's planner — and the deploy templates assume `claude` everywhere.

The user's request: add Codex as a first-class second backend; unify
permissions across backends; update deploy + docs.

## Non-goals

- Removing Claude Code support or breaking existing slave/driver configs.
- Adding any third backend (gemini-cli, aider, etc.) — the interface should
  *allow* it later, but no code or template ships in this change.
- Mixed-backend skills in a single process (one slave = one backend; want both?
  run two slave processes, distinct names).
- Translating Claude's fine-grained `Bash(rm *)`-style permission patterns
  into Codex's coarse sandbox model. Codex permissions stay at sandbox-mode +
  approval-policy granularity; native Claude allow/deny strings sent to a
  Codex backend are rejected with a clear error.

## Background — where Claude is hard-coded today

| Site | Behavior |
|---|---|
| `internal/executor/claude.go` | `chat` skill spawns `claude --print --output-format=stream-json --verbose`, parses Anthropic NDJSON, extracts `=== CAPABILITY ===` epilogue |
| `internal/executor/claude_permissions.go` + `internal/claudeperm/` | `claude_permissions` skill reads/patches `<workdir>/.claude/settings.local.json` `permissions.allow`/`deny` arrays, with preset expansion (`file_write`, `curl`, etc.) |
| `internal/planner/planner.go` | Driver planner (`Route`/`Plan`/`Reduce`) spawns `claude --print` with prompt on stdin, expects text on stdout |
| `internal/config/config.go` defaults | `cfg.Claude.Bin = "claude"`; `cfg.Planner.Bin` defaults to `cfg.Claude.Bin` |
| `internal/capabilitydoc/doc.go` | `which` probe lists `claude` |
| `deploy/linux/{driver,slave}/bootstrap.sh` + `install.sh` | `npm i -g @anthropic-ai/claude-code`; writes `.mcp.json`; ships `.claude/skills/` tarball; `planner.bin: claude` in template |
| `cmd/slave-agent/main.go` | Wires `chat` + `claude_permissions` executors using `cfg.Claude.*` |

## Design

### 1. New package `pkg/agentbackend/`

A backend is a single object that owns everything coding-agent-specific.
Slave and driver consume only this interface; no direct references to
`claude` or `codex` outside the backend subpackages.

```go
package agentbackend

import (
    "context"
    "github.com/yourorg/multi-agent/internal/executor"   // for Task / Sink / Result
)

type Kind string

const (
    KindClaude Kind = "claude"
    KindCodex  Kind = "codex"
)

type Backend interface {
    Kind() Kind

    // Run = chat/exec skill. Same contract as today's executor.Executor.
    Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error)

    // LLM = planner-side text-in/text-out. Used by driver's planner only.
    LLM() LLMRunner

    // Permissions = permissions skill (formerly claude_permissions).
    Permissions() PermissionsStore

    // Detect verifies the binary is on PATH and (best-effort) the user is
    // authenticated. Used by capabilitydoc.
    Detect(ctx context.Context) error
}

type LLMRunner interface {
    // Run executes a single prompt non-interactively and returns trimmed text.
    Run(ctx context.Context, prompt string) (string, error)
}

type PermissionsStore interface {
    Get(ctx context.Context) (State, error)
    Patch(ctx context.Context, p Patch) (State, error)
}

type State struct {
    Backend Kind     `json:"backend"`
    Path    string   `json:"path"`            // settings file path
    Mode    string   `json:"mode,omitempty"`  // codex: "ask" | "workspace-write" | "full-access"
    Allow   []string `json:"allow,omitempty"` // claude-native; codex leaves empty
    Deny    []string `json:"deny,omitempty"`
}

type Patch struct {
    // Uniform vocabulary — both backends translate these.
    Presets []string `json:"presets,omitempty"`
    // Claude-native passthrough. Codex returns an error if any are set.
    AllowAdd, AllowRemove []string `json:",omitempty"`
    DenyAdd,  DenyRemove  []string `json:",omitempty"`
    // Codex-native sandbox mode. Sending this to a Claude backend returns an error.
    Mode string `json:"mode,omitempty"`
}
```

#### Preset vocabulary (uniform)

| Preset | Claude expansion | Codex expansion |
|---|---|---|
| `file_write` | `Read`, `Write`, `Edit` | `sandbox_mode = "workspace-write"` |
| `curl` | `Bash(curl *)` | bumps `sandbox_mode` to ≥ `workspace-write` (Codex has no per-tool allowlist; documented limitation) |
| `python` | `Bash(python *)`, `Bash(python3 *)`, `Bash(pip *)`, `Bash(pip3 *)`, `Bash(python -m pip *)`, `Bash(python3 -m pip *)` | same as `curl` — bumps `sandbox_mode` to `workspace-write` |
| `full_access` | adds `Bash(*)` | `approval_policy = "never"` + `sandbox_mode = "danger-full-access"` |

Codex's coarser model means `curl`/`python` presets are no-ops if sandbox is
already ≥ `workspace-write`. Returned `State.Mode` reflects truth on disk.

### 2. Backend implementations

#### `pkg/agentbackend/claude/`

- `executor.go` — current `internal/executor/claude.go` body, moved here as
  `backend.Run` impl.
- `llm.go` — current `internal/planner/planner.go::runClaude` body, moved
  here behind `LLMRunner`. Reuses `claude --print` form.
- `permissions.go` — current `internal/claudeperm/store.go` body, moved
  here behind `PermissionsStore`. Preset expansion logic preserved.
- `detect.go` — `which claude` + (best-effort) check `~/.claude/.credentials.json`
  presence.

#### `pkg/agentbackend/codex/`

- `executor.go` — new `CodexExecutor`. Spawns `codex exec --json --full-access -`
  with prompt on stdin; reads NDJSON events from stdout. Event schema is
  not yet finalized in the published docs; **the first implementation task
  is to run `codex exec --json "hello"` and pin the event names** (likely
  `agent_message` / `final_response` / `delta`). Extracts assistant text;
  reuses `splitCapability` from a shared helper for the capability epilogue.
- `llm.go` — invokes `codex exec --full-access -` (no `--json`), returns
  trimmed stdout text. Same `LLMRunner` contract.
- `permissions.go` — reads/writes `<workdir>/.codex/config.toml` using
  `github.com/BurntSushi/toml` (add to go.mod). Honors uniform presets;
  rejects Claude-native `AllowAdd`/etc. with `error`. Returns `State` with
  `Mode` filled.
- `detect.go` — `which codex` + check `~/.codex/auth.json` or `OPENAI_API_KEY`.

#### Factory

```go
package agentbackend

func New(cfg Config) (Backend, error) {
    switch Kind(cfg.Kind) {
    case "", KindClaude:
        return claude.New(cfg.Claude), nil
    case KindCodex:
        return codex.New(cfg.Codex), nil
    default:
        return nil, fmt.Errorf("unknown agent.kind %q", cfg.Kind)
    }
}
```

### 3. Config schema

#### Slave `config.yaml`

```yaml
agent:
  kind: claude              # "claude" | "codex"; default claude

claude:                     # honored when kind=claude
  bin: claude
  workdir: ~/.loom/<NAME>
  extra_args: []

codex:                      # honored when kind=codex
  bin: codex
  workdir: ~/.loom/<NAME>
  extra_args: []

planner:
  bin: ""                   # empty → falls back to backend's LLM binary
  timeout_sec: 300
  extra_args: []

discovery:
  skills:
    - chat
    - permissions           # renamed (was claude_permissions)
    - bash
    - file
    - register_mcp
```

#### Driver `config.yaml`

Same `agent.*` / `claude.*` / `codex.*` blocks at top level. `planner.bin`
defaults from backend kind. No other change.

#### Defaulting

`internal/config/config.go` and `internal/driver/config.go`:

- Empty `agent.kind` → `"claude"` (matches today's behavior).
- Empty `planner.bin` → backend's binary (`claude` or `codex`).
- Old top-level `claude:` block with no `agent.kind` → still loads, treated
  as claude-backend config. Strict back-compat.

### 4. Skill renaming

| Old skill name | New skill name | Behavior |
|---|---|---|
| `chat` | `chat` | unchanged externally; routes through `backend.Run` |
| `claude_permissions` | `permissions` | unchanged JSON shape (`{op: get|patch, ...}`); routes through `backend.Permissions()` |

**Back-compat path:** the slave skill router accepts `claude_permissions` as
an alias for `permissions` and logs a one-shot deprecation warning per
process. The bootstrap templates emit `permissions` (not the legacy name).
`discovery.skills` in old configs containing `claude_permissions` is
silently rewritten to `permissions` at load with the same deprecation log.

Drivers asking the observer for a slave that supports `permissions` will not
match legacy slaves that still advertise only `claude_permissions`; the
deprecation window for operators to roll forward is **one minor release**
(planned cutoff with the next observer release).

### 5. Wiring changes

| File | Change |
|---|---|
| `cmd/slave-agent/main.go` | Build `backend, err := agentbackend.New(cfg.Agent)`; register `chat = backend.Run`; register `permissions = backend.Permissions()`-wrapping executor; skip `claude_permissions` legacy ctor |
| `cmd/driver-agent/main.go` | Build backend; `planner.New(cfg.Planner, backend.LLM())`; planner uses injected `LLMRunner` |
| `internal/planner/planner.go` | Replace inline `runClaude` with calls to injected `LLMRunner`. Prompts unchanged. |
| `internal/capabilitydoc/doc.go` | Replace `claude` probe with `backend.Detect`-driven list |
| `internal/executor/claude.go` | **Deleted** (logic moved to `pkg/agentbackend/claude/`) |
| `internal/executor/claude_permissions.go` | **Deleted** |
| `internal/claudeperm/` | **Deleted** (logic moved to `pkg/agentbackend/claude/`) |
| `internal/config/config.go` | New `Agent`, `Codex` structs; preserve old `Claude` struct; defaults as above |
| `internal/driver/config.go` | Same as slave: new fields, BC defaults |

No thin shims for the deleted files. Repo-internal callers are updated in
the same change.

### 6. Deploy templates

#### Bootstrap / install flags

Both `deploy/linux/driver/{bootstrap,install}.sh` and
`deploy/linux/slave/{bootstrap,install}.sh` gain:

```
--agent claude|codex      # default: claude (matches today)
```

`bootstrap-observer.sh` and the observer install are unaffected (observer
has no coding-agent dependency).

#### Per-backend behavior

| Step | `--agent claude` | `--agent codex` |
|---|---|---|
| Package install | `npm i -g @anthropic-ai/claude-code` | `npm i -g @openai/codex` (warn if Node < 22) |
| Skills / prompts bundle | `driver-skills.tar.gz` → `.claude/skills/{multiagent,mcp-acceptance,scaffold-mcp-server}/` | `driver-codex-prompts.tar.gz` → `AGENTS.md` (root) + `.codex/prompts/{scaffold-mcp,mcp-acceptance}.md` |
| Driver MCP registration | write `.mcp.json` (project root) | write **project-scoped** `.codex/config.toml` with `[mcp_servers.driver]` table |
| `config.yaml` rendered | `agent.kind: claude` + `claude:` block | `agent.kind: codex` + `codex:` block |
| Auth hint at the end | `claude login` or `ANTHROPIC_API_KEY` | `codex login` or `OPENAI_API_KEY`; reminder to `codex` once in the project dir to mark it trusted (project-scoped `.codex/config.toml` only loads in trusted dirs) |
| Launch line | `claude` | `codex` |
| systemd template (slave) | unchanged | unchanged (codex-mode slave still runs `slave-agent`; its child process is `codex exec`) |

#### New release assets

- `driver-codex-prompts.tar.gz` — built from a new
  `deploy/linux/driver/prompts-codex/` source dir containing `AGENTS.md` +
  prompt files transcribed from the existing skills.
- No new binary assets; same `driver-agent.linux-{amd64,arm64}` and
  `slave-agent.linux-{amd64,arm64}` work for both backends.

#### Project-scoped Codex config

Codex CLI loads `.codex/config.toml` from a project only if the directory
is in its trusted list (added via `codex` interactive prompt on first
run). This is acceptable because:

- Avoids polluting `~/.codex/config.toml` with one entry per driver project.
- Lets multiple driver projects coexist on one host without `mcp_servers`
  name collisions.
- The first-run trust prompt is a one-time UX cost, mirrored by Claude
  Code's first-run MCP-approval prompt.

The bootstrap output explicitly tells the user "run `codex` once inside
this directory and approve the trust prompt before issuing tasks."

### 7. Docs updates

- `deploy/README.md` — top section gains parallel one-liner blocks for
  Claude Code and Codex on each role; the role description rows are
  reworded to drop the "Claude Code project" framing.
- `deploy/linux/driver/README.md` — `--agent` flag, Codex auth notes,
  trusted-dir caveat, layout diff (`.codex/config.toml` vs `.mcp.json`).
- `deploy/linux/slave/README.md` — `--agent` flag, mentions backend
  choice is per-slave; runs Codex `chat` if `agent.kind: codex`.
- Top-level `README.md` + `README.en.md` — opening paragraph clarifies
  drivers and slaves can each independently run on Claude Code *or* Codex;
  the existing one-liner section gains Codex variants.
- New file `deploy/agent-backends.md` (双语) — comparison table, sample
  `permissions` skill JSON request/response for each backend, listing
  presets and which backend supports which native fields.

### 8. Tests

- `pkg/agentbackend/claude/permissions_test.go` — port of existing
  `internal/claudeperm/store_test.go`; same golden outputs.
- `pkg/agentbackend/codex/permissions_test.go` — new: preset → TOML
  diff fixtures; rejection of native `AllowAdd`; idempotency under repeated
  patch.
- `pkg/agentbackend/codex/executor_test.go` — fixture-driven NDJSON
  parser tests using a captured `codex exec --json` transcript (pinned
  during implementation).
- `internal/planner/planner_test.go` — refactor to inject a fake
  `LLMRunner`; existing prompt-shape assertions preserved.
- `deploy/linux/compose-test/` — add `driver-codex` service; the
  end-to-end smoke test asserts a codex-driver can `list_agents` and
  dispatch one `chat` task to a claude-slave. (Single happy-path; not
  exhaustive.)

### 9. Migration / rollout

- Existing slave + driver configs continue to work with zero edits: empty
  `agent.kind` → claude, `claude_permissions` skill alias → `permissions`.
- The bootstrap one-liners default to `--agent claude` so existing
  documented commands keep working.
- Operators who want Codex add `--agent codex` to the bootstrap; nothing
  else differs from their POV.
- Deprecation: `claude_permissions` skill name supported for one minor
  release, then removed.

## Open items (verify during implementation, not blocking design)

1. Exact `codex exec --json` event schema — pin in PR #1 by running it.
2. Whether `codex exec` exits non-zero on tool-call failure inside the
   sandbox; current Claude executor returns the wrapped stderr tail —
   Codex impl should match.
3. Whether Codex's project-scoped trust prompt can be pre-seeded
   programmatically (so bootstrap can avoid the manual approval step).
   If yes, fold it into the install script; if no, the install script
   prints the one-line instruction.
4. Whether `~/.codex/auth.json` exists when only `OPENAI_API_KEY` is set
   (`Detect` fallback ordering).

## File-touch summary

**Created:**
- `pkg/agentbackend/{backend.go,config.go,factory.go}`
- `pkg/agentbackend/claude/{executor.go,llm.go,permissions.go,detect.go,*_test.go}`
- `pkg/agentbackend/codex/{executor.go,llm.go,permissions.go,detect.go,*_test.go}`
- `deploy/linux/driver/prompts-codex/AGENTS.md` (+ optional prompt files)
- `deploy/agent-backends.md`
- `docs/superpowers/specs/2026-05-23-codex-backend-design.md` (this file)

**Modified:**
- `internal/config/config.go`, `internal/driver/config.go`
- `internal/planner/planner.go` (+ test)
- `internal/capabilitydoc/doc.go`
- `cmd/slave-agent/main.go`, `cmd/driver-agent/main.go`
- `deploy/linux/{driver,slave}/{bootstrap.sh,install.sh,config.yaml.template}`
- `deploy/linux/driver/.mcp.json.template` → keep but only used in claude mode; add sibling `codex-mcp.toml.template`
- `deploy/README.md`, `deploy/linux/{driver,slave}/README.md`
- top-level `README.md`, `README.en.md`
- `go.mod` (+ `github.com/BurntSushi/toml`)
- `deploy/linux/compose-test/{docker-compose.yml,entrypoint-driver.sh,README.md}`

**Deleted:**
- `internal/executor/claude.go`
- `internal/executor/claude_permissions.go`
- `internal/claudeperm/` (whole package)
