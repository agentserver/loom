# Explicit driver thread binding + sidecar-confirms-exec origin

**Date:** 2026-06-23
**Branch / PR:** PR #31 (commander-parent-link-p3) — folds two follow-up fixes into the open P3 PR per the operator decision.
**Issue:** #24 (P3 follow-ups discovered in live e2e). Touches the bridge-vs-backend-id naming concerns from #29.

## Problem

P3 of #24 shipped the cross-daemon Commander tree: slave `agent_task` sessions can render nested under their originating driver session via `(effectiveOwner, parent_id)` keys. The piping works in unit tests. The live e2e revealed two gaps that prevent the feature from being visible end to end:

1. **Q1 — no nesting in the UI.** The slave sidecar's `parent_session_id` field is empty in real runs, so the frontend's `if (!s.parent_id) continue` early-exit skips the nest step and every slave session renders as a root in its own daemon column. Root cause: the driver MCP's `loomOriginMarker()` reads `<CODEX_HOME>/loom-meta/current` to fill `parent_session_id`, but that file is only written by the P2 codex executor (slave path). The driver's parent codex is the user's interactive `codex` CLI, which never touches that path → file does not exist → marker carries the empty string → slave sidecar inherits empty → frontend bails.
2. **Q2 — driver's own sessions show as `agent_task`.** Sessions launched via `codex exec ...` are classified as `agent_task` (because `pkg/agentbackend/codex/sessions.go::applyCodexSessionMeta` maps `originator=codex_exec || source=exec` to `SessionOriginAgentTask` unconditionally). The intent of "agent_task" is "spawned by another loom agent" — that signal lives in our loom-meta sidecar, not in codex's own metadata.

Both gaps are mechanical: Q1 needs the driver to know its parent codex thread_id; Q2 needs the origin classifier to defer to the sidecar for the agent_task signal.

## Goals

- **Q1**: The driver-agent has a deterministic, explicitly-set parent thread_id (Codex `thread_id`, backend-native per #29). Parent-link submissions (`submit_task`/`submit_contract_task` with skill ∈ {chat, chat_resume, fanout, fanout_strict, route}, plus `resume_task` which is always parent-link) stamp it into the `loom_origin` SystemContext, slave's P2 executor copies it into the sidecar, P3 frontend nests the slave session under it. Non-parent-link submissions (bash, powershell, etc.) continue to send no SystemContext stamp and do not require bind.
- **Q2**: Origin classification:
  - Codex-native `subagent` stays authoritative; no sidecar can demote or relabel it.
  - `agent_task` is granted only when both (a) the rollout came from exec mode AND (b) a loom-meta sidecar with `origin=agent_task` exists. Other exec-mode rollouts default to `user`.
- After both fixes, a live e2e with `driver codex exec → submit_task → wait_task` shows: driver session origin = `user`, slave sessions nested under driver in Commander UI.
- **Fail explicitly, never silently mis-attribute.** If a parent-link submission is attempted without a bound thread, it returns an actionable error rather than stamping an empty / wrong parent. (Non-parent-link submissions are unaffected — they continue working without bind.) Discovery uses only signals that uniquely identify the parent codex process — no `cwd`-based "newest thread" heuristic, no time-based tiebreaks.
- **Platform support, v1**:
  - **Linux**: fully supported. Both env-source and /proc/fd-source paths in `discover-thread.sh` (bash).
  - **macOS**: env-source path works (bash ships; script's env branch runs and succeeds if CODEX_THREAD_ID is in the shell's env). /proc/fd path no-ops gracefully (script `[[ -d /proc/$PPID/fd ]]` guard returns 1 with the operator-actionable stderr message). Operator falls back to manual `/status` paste.
  - **Windows (WSL / Git Bash)**: same as macOS — env path works, /proc path no-ops, manual `/status` fallback otherwise.
  - **Windows (native cmd / PowerShell, no bash)**: **NOT auto-supported in v1**. The heredoc-bash invocation in SKILL.md won't run. Operator must always bind manually: call `driver.bind_thread(thread_id="<from /status>")` directly. SKILL.md `## Initialization` block calls this out explicitly so the model knows to skip the script step and go straight to "ask the user for /status thread_id" on native Windows.
  - A `discover-thread.ps1` for native Windows is **out of scope for v1** and tracked as a follow-up.
- No upstream codex changes required. No new Go dependencies.

## Non-goals

- Replacing P2's existing loom-meta machinery on the slave side. The slave path stays exactly as P2 ships it; we only fix the discovery hole on the driver side.
- Any UX changes to how users invoke codex. `codex` and `codex exec` continue to work as today; no wrapper.
- Automatic / silent discovery of the parent thread inside driver-agent (the historic approach). Discovery is the skill's job; driver-agent only stores what it's told.

## Design

The architecture splits cleanly:

- **driver-agent** holds a single mutable field — the bound parent thread_id — and a new MCP tool `bind_thread` to set it. **Parent-link submission paths** (`submit_task` / `submit_contract_task` with skill ∈ {chat, chat_resume, fanout, fanout_strict, route}, plus `resume_task` which is always parent-link) refuse to operate until it's bound. Non-parent-link paths (bash, powershell, read/write/stat slave file, etc.) are unaffected.
- **multiagent skill** ships a deterministic discovery script (`scripts/discover-thread.sh`) and an explicit initialization step in `SKILL.md` that mandates calling `bind_thread` once at session start.

This puts the platform-specific discovery logic in a shell script — easy to update, easy to test, easy to make platform-specific — while keeping driver-agent's Go code dumb and portable.

### Q1: explicit parent thread binding

#### New MCP tool: `bind_thread`

The driver MCP server exposes tools via the `Tool` interface in `internal/driver/mcp_server.go:18` (`Name`/`Description`/`InputSchema`/`Call`). To make `bind_thread` callable from codex (`tools/list` discovers it, `tools/call` invokes it), we add a new `bindThreadTool` struct AND register it in `Tools.All()` at `internal/driver/tools.go:176`. Adding only a `BindThread(...)` method on `Tools` would compile but not be reachable via MCP — the explicit registration is mandatory.

Implementation comes in TWO layers — a `BindThread(...)` method on `Tools` that unit tests call directly, and an MCP-facing `bindThreadTool` that delegates to it. The split lets us unit-test bind semantics without going through MCP serialization while keeping the tool reachable via `tools/list`.

```go
// internal/driver/tools.go — add to Tools struct, methods, and All()

type Tools struct {
    ...
    parentThread atomic.Pointer[string]  // nil = not yet bound
}

var (
    validThreadIDPattern = `^[A-Za-z0-9._-]{1,128}$`
    validThreadID        = regexp.MustCompile(validThreadIDPattern)
)

// BindResult is the structured response returned to the MCP caller and
// directly to unit tests. Marshaled by bindThreadTool.Call().
type BindResult struct {
    Bound       bool   `json:"bound"`
    ThreadID    string `json:"thread_id"`
    AgentID     string `json:"agent_id"`
    DisplayName string `json:"display_name"`
}

// BindThread validates and stores the parent thread_id. Tests call this
// directly (no MCP serialization needed). The MCP tool below delegates
// here so wire-protocol surface and direct-call surface stay in lockstep.
func (t *Tools) BindThread(ctx context.Context, threadID string) (BindResult, error) {
    id := strings.TrimSpace(threadID)
    if !validThreadID.MatchString(id) {
        return BindResult{}, fmt.Errorf("invalid thread_id format: must match %s", validThreadIDPattern)
    }
    t.parentThread.Store(&id)
    return BindResult{
        Bound:       true,
        ThreadID:    id,
        AgentID:     t.cfg.Credentials.ShortID,
        DisplayName: t.cfg.Discovery.DisplayName,
    }, nil
}

func (t *Tools) All() []Tool {
    return []Tool{
        // ... existing tools unchanged ...
        &bindThreadTool{t},  // ← NEW, placed near submit_task for discoverability
        &submitTaskTool{t},
        // ... rest unchanged
    }
}
```

```go
// internal/driver/bind_thread_tool.go (new file)

type bindThreadTool struct{ t *Tools }

func (b *bindThreadTool) Name() string { return "bind_thread" }

func (b *bindThreadTool) Description() string {
    return "Bind the driver MCP to its parent Codex thread_id. Must be " +
        "called once per session before any parent-link submission " +
        "(submit_task/submit_contract_task with skill chat/chat_resume/" +
        "fanout/fanout_strict/route, or resume_task). The multiagent " +
        "skill runs the discover-thread.sh script and passes its output " +
        "here. Calling with a new thread_id replaces the bound value " +
        "(supports /resume mid-session)."
}

func (b *bindThreadTool) InputSchema() json.RawMessage {
    return json.RawMessage(`{
        "type":"object",
        "properties":{
            "thread_id":{"type":"string","description":"Backend-native thread/session id of the parent agent. Codex's value is a UUIDv7 like 019ef3bd-42c8-7731-85b7-7177ae747389; the validator accepts any opaque id matching ^[A-Za-z0-9._-]{1,128}$, so other backends or tests can pass non-UUID strings (e.g. 'thr-parent') as well.","pattern":"^[A-Za-z0-9._-]{1,128}$"}
        },
        "required":["thread_id"]
    }`)
}

func (b *bindThreadTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
    var args struct {
        ThreadID string `json:"thread_id"`
    }
    if err := json.Unmarshal(raw, &args); err != nil {
        return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
    }
    result, err := b.t.BindThread(ctx, args.ThreadID)
    if err != nil {
        return nil, &MCPToolError{Message: err.Error()}
    }
    return json.Marshal(result)
}
```

The split means:
- `tools.BindThread(ctx, id)` is the test-callable Go API; returns `(BindResult, error)`.
- `bindThreadTool.Call(...)` is the MCP-callable wrapper; wraps any non-MCP error into `&MCPToolError{...}` so codex sees a clean JSON-RPC -32000 error.

`Semantics`:
- **Idempotent**: calling again with a NEW value replaces the bound id (handles `/resume`). Calling with the SAME value is a no-op (atomic Store of the same string).
- **Single source of truth**: NO automatic discovery in driver-agent. If the skill doesn't call `bind_thread`, parent-link submissions fail loudly.
- **Validator**: rejects unexpanded interpolation placeholders (`${VAR}`), shell metachars, whitespace-only strings — anything that would persist as a broken link in loom-meta sidecars. Permissive enough to accept any 8-4-4-4-12 hex UUID, including the UUIDv7s Codex emits (`019ef3bd-42c8-7731-85b7-7177ae747389`) and any reasonable opaque id within 128 chars of `[A-Za-z0-9._-]`.

#### Tool guards on submit/resume/contract

The guard is **narrowed to the skills that actually stamp loom_origin** — `isParentLinkDelegation(skill)` returns true for `""`, `chat`, `chat_resume`, `fanout`, `fanout_strict`, `route` (per `internal/driver/tools.go:68`). Other skills (`bash`, `powershell`, etc.) don't produce nested codex sessions on the slave, so they don't need a parent_session_id and shouldn't require bind. This dramatically narrows the test-migration blast radius (~53 tests touch `SubmitTask`/`SubmitContractTask`/`ResumeTask`, but only those exercising parent-link skills need to call BindThread).

```go
// internal/driver/tools.go
//
// Capture-and-pass pattern (not a re-Load in loomOriginMarker) — single
// tool call MUST see a consistent thread_id even if a concurrent
// bind_thread replaces it mid-flight. MCP server executes tool calls
// concurrently (internal/driver/mcp_server.go:79,237), so a Load-then-act
// pattern would race.
func (t *Tools) requireBoundThread() (string, error) {
    p := t.parentThread.Load()
    if p == nil {
        return "", fmt.Errorf(
            "driver not bound to a codex thread; call `bind_thread` first " +
                "(the multiagent skill runs scripts/discover-thread.sh as " +
                "its first init step)")
    }
    return *p, nil
}
```

**Two changes in `(s *submitTaskTool) Call`**: capture `parentThreadID` EARLY (before any side effects), then use it later when building `systemContext`.

```go
// Change 1 — fail-fast guard, placed RIGHT AFTER skill defaulting and
// the jsonPromptSkill validation (~line 461), BEFORE manifest
// construction / RegisterFile / RegisterWrite / observer artifact
// creation / audit writes. Goal: an unbound parent-link submit MUST
// NOT leak read/write tokens, audit log entries, or observer artifacts.

skill := args.Skill
if skill == "" {
    skill = "fanout"
}
if jsonPromptSkill(skill) && (len(args.ReadPaths) > 0 || len(args.WritePaths) > 0) {
    return nil, &MCPToolError{Message: "..."}
}

// NEW: capture bound thread before any side effects.
var parentThreadID string
if isParentLinkDelegation(skill) {
    pid, err := s.t.requireBoundThread()
    if err != nil {
        return nil, &MCPToolError{Message: err.Error()}
    }
    parentThreadID = pid
}

// ... existing manifest / token / observer / target-resolution code
// runs unchanged from here ...

// Change 2 — at the existing systemContext block (~line 569), replace
// the loomOriginMarker call with BuildLoomOrigin using the
// already-captured parentThreadID. No second Load.

// Before:
//   systemContext := ""
//   if isParentLinkDelegation(skill) {
//       if m := s.t.loomOriginMarker(); m != "" {
//           systemContext = m
//       }
//   }
// After:
systemContext := ""
if isParentLinkDelegation(skill) {
    systemContext = agentbackend.BuildLoomOrigin(
        s.t.cfg.Credentials.ShortID,
        s.t.cfg.Discovery.DisplayName,
        parentThreadID,  // captured above; never re-Loaded
    )
}
// systemContext is "" for non-parent-link skills (bash, powershell,
// etc.) — matches existing behavior; those submissions don't carry a
// loom_origin marker and don't require bind.

resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
    TargetID:       targetID,
    Skill:          skill,
    Prompt:         finalPrompt,
    SystemContext:  systemContext,
    TimeoutSeconds: timeout,
})
```

Same pattern in `(s *resumeTaskTool) Call` and the two paths in `(s *submitContractTaskTool) Call`.

**Specific ordering for `submitContractTaskTool.Call` (`contract_tools.go`)** — the existing flow has several pre-bind-guard side effects that need to be reordered. Allowed-before-guard operations are pure (validation + read-only discovery + in-memory analysis); side-effecting operations MUST move after the guard:

```
Allowed BEFORE the bind guard (pure / read-only — no observable mutation):
  - Unmarshal args
  - tc.ApplyDefaults() + tc.Validate()
  - sdk.DiscoverAgents(ctx)                   // read-only RPC #1
  - contract.NewResourceSnapshot(...)         // in-memory
  - json.Marshal(snapshot)                    // in-memory
  - analyzeContractCapabilities(...)          // in-memory; produces `report`
  - contract.EncodeEnvelope(...)              // in-memory
  - Compute `needsBind` from `report.RecommendedRoute` and the
    skillOverride: see below. Route inference is in-memory at this point.

NOTE on Path-B route resolution: the full selectTarget call at
`contract_tools.go:163` invokes `t.resolveTarget`, which internally
makes a SECOND read-only DiscoverAgents call (tools.go:224). That's
also pre-bind-safe (still read-only), but the spec recommends
refactoring selectTarget to accept the already-discovered `cards`
slice from the call above, eliminating the redundant RPC. If the
refactor is not done in this PR (it touches code beyond the bind
guard's scope), the second DiscoverAgents stays — explicitly
acknowledged as a duplicate read-only call, not a side effect.

MOVED until AFTER the bind guard (was at contract_tools.go:62):
  - observerRelay().SaveResourceSnapshot(...) ← observer-visible side effect

The guard itself:
  var parentThreadID string
  needsBind := (args.TargetDisplayName == "" && report.RecommendedRoute == routeDriverFanout) ||
               isParentLinkDelegation(<resolved skill — for early Path A,
                                       inferred from contract; for Path B
                                       known after selectTarget>)
  if needsBind {
      pid, err := s.t.requireBoundThread()
      if err != nil {
          return nil, &MCPToolError{Message: err.Error()}
      }
      parentThreadID = pid
  }

For Path B, the implementer has two options:
  (a) Compute the skill from the contract + override BEFORE selectTarget
      runs (mirroring the `skillOverride` defaulting in selectTarget at
      contract_tools.go:175-184), pass it through the guard, then call
      selectTarget. Cleanest — keeps the guard at the top and avoids
      duplicating route logic.
  (b) Call selectTarget first to learn the resolved skill, THEN run the
      guard. Costs the second DiscoverAgents RPC but reuses existing
      logic. Side-effect-free because the only mutation
      (SaveResourceSnapshot) is still after the guard.
Pick (a) when feasible; either way, SaveResourceSnapshot stays after
the guard.

After guard, in this order:
  - observerRelay().SaveResourceSnapshot(...)
  - Path A (driver_fanout early return):
        marker := agentbackend.BuildLoomOrigin(shortID, displayName, parentThreadID)
        result, err := t.contractRunner.Run(ctx, finalPrompt, marker)
  - Path B (normal selectTarget): existing flow with the same
    capture-once-then-BuildLoomOrigin pattern as SubmitTask, using
    `parentThreadID` (do not re-Load).
```

Rationale: an unbound parent-link contract submission must NOT leave an observer ResourceSnapshot artifact behind (currently it would — observer save happens at `contract_tools.go:62`, well before any bind-aware code). Reordering keeps the failure side-effect-free at the boundary, matching the SubmitTask refactor's invariant.

The route-vs-skill check for `needsBind` covers both contract-routing modes: Path A (driver_fanout, no target picked) always needs bind because contractRunner spawns nested codex on the driver; Path B inherits the same skill-aware rule as SubmitTask.

(The detailed `submitContractTaskTool.Call` ordering — including Path A driver_fanout and Path B selectTarget — is fully specified in the block above; do not split it across multiple summaries. `ResumeTask` follows the same guard-early / capture-once / pass-through pattern; resume is always parent-link, so the guard is unconditional.)

Note: `loomOriginMarker()` as a method on `Tools` is **deleted**. Its callers (including the `contract_tools.go:75` driver_fanout path) now build the marker locally with the captured `parentThreadID`. This removes the implicit Load on every marker-building path that would have re-introduced the race.

The error message is deliberately long: when the model sees it, it should know the failure mode AND the next step to recover (run discover-thread.sh and call bind_thread).

#### `loomOriginMarker()` removed

The old method `(t *Tools) loomOriginMarker() string` is removed entirely. Its callers now construct the marker inline using the `parentThreadID` captured by `requireBoundThread()` at the top of the same call (see the SubmitTask example above). This makes the race-free design explicit at every call site.

After this change `codex.ReadCurrentSession` has no production callers on the driver path. `writeCurrentSession` is still called by the slave-side P2 executor (`pkg/agentbackend/codex/executor.go:207`) — the slave's executor owns its CODEX_HOME and uses the file for its own resume tracking; that path is untouched. Removing `ReadCurrentSession` entirely is a follow-up cleanup, not in scope here.

### Multiagent skill side

#### `skills/multiagent/scripts/discover-thread.sh` (new)

The script accepts **only signals that uniquely identify the parent codex process** — no `cwd`-based "newest thread" heuristic, no time ordering. Two sources, both deterministic:

1. **`CODEX_THREAD_ID` env in the discovery process's environment**: the discover-thread.sh script is run as a heredoc-bash shell tool invocation by the model (`bash <<'DISCOVER_EOF' ... DISCOVER_EOF`), so its environment is **inherited from the codex shell-tool subprocess that's executing it**. That shell-tool subprocess is itself spawned by codex; per the codex binary strings analysis (literal `CODEX_THREAD_ID` adjacent to `core/src/shell_snapshot.rs`), codex sets `CODEX_THREAD_ID` in the env of its shell-tool subprocesses. So the env path works whenever the discovery script runs as a codex shell tool — which is exactly the SKILL.md heredoc invocation pattern.

   This is **distinct from** codex's `[mcp_servers.<X>] env_vars = ["CODEX_THREAD_ID"]` config field, which controls what env reaches the *MCP child process* (driver-agent, in our case) — driver-agent's own env is irrelevant to discover-thread.sh because the script runs in the model-executed shell, not inside driver-agent. The MCP-child env path would only matter if driver-agent itself did env-based discovery internally, which we're explicitly NOT doing (the bind_thread design moves discovery entirely into the skill).
2. **Linux `/proc/<ppid>/fd` exact-UUID scan**: walk up the parent chain, look for an open rollout file `*-<UUID>.jsonl` whose UUID portion is matched by the strict UUID regex (8-4-4-4-12 hex; matches any UUID, including the v7s Codex emits) (same pattern Go uses in `pkg/agentbackend/codex/sessions.go:26`). The parent/child kinship is kernel-enforced, so the matched id IS the parent codex's current thread.

If neither resolves, the script exits non-zero. The skill instructions then tell the model to ask the user for the value via `/status`. No silent fallback.

```bash
#!/usr/bin/env bash
# discover-thread.sh — print the current codex thread_id to stdout, or
# exit non-zero with an explanation on stderr. Deterministic; no heuristics.
#
# Signals allowed:
#   1. $CODEX_THREAD_ID (codex itself wrote it, or a forwarder did)
#   2. /proc/<ppid>/fd open rollout file under $CODEX_HOME/sessions/
#      with exact UUID suffix (Linux)
#
# Cwd-based or "newest thread" sqlite lookup is INTENTIONALLY OMITTED —
# the same cwd routinely hosts multiple codex threads, and "newest" can
# silently mis-attribute to the wrong one.
#
# /proc/<ppid>/fd matches MUST be:
#   - under $CODEX_HOME/sessions/ (canonicalized prefix)
#   - basename ends with -<UUID>.jsonl
# Across the parent-chain walk we collect UNIQUE thread_id candidates;
# exactly 1 → emit it; 0 → fall to manual fallback; >1 → fail loud and
# print the candidates so operators can see the invariant violation.

set -euo pipefail

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
valid() { [[ "$1" =~ $UUID_RE ]]; }

# Source 1: env
if [[ -n "${CODEX_THREAD_ID:-}" ]] && valid "$CODEX_THREAD_ID"; then
    echo "$CODEX_THREAD_ID"
    exit 0
fi

# Source 2: Linux /proc/<pid>/fd walk
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
# Canonicalize so symlinked CODEX_HOME doesn't false-negative the prefix
# check (gopsutil-side fd readlinks return realpath).
SESSIONS_ROOT=$(cd "$CODEX_HOME/sessions" 2>/dev/null && pwd -P || true)

if [[ -n "$SESSIONS_ROOT" && -d "/proc/$PPID/fd" ]]; then
    declare -A seen=()
    pid=$PPID
    for _ in 1 2 3 4 5; do
        [[ "$pid" -gt 1 ]] || break
        if [[ -d "/proc/$pid/fd" ]]; then
            for fd in /proc/$pid/fd/*; do
                target=$(readlink -f "$fd" 2>/dev/null) || continue
                # Reject anything not under sessions root
                [[ "$target" == "$SESSIONS_ROOT"/* ]] || continue
                # Extract trailing UUID (validate strict shape)
                cand=$(printf '%s\n' "$target" | sed -nE \
                    's|.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$|\1|p')
                if [[ -n "$cand" ]] && valid "$cand"; then
                    seen["$cand"]=1
                fi
            done
        fi
        pid=$(awk '/^PPid:/{print $2}' "/proc/$pid/status" 2>/dev/null || echo 1)
    done

    case "${#seen[@]}" in
        1)
            for k in "${!seen[@]}"; do echo "$k"; done
            exit 0
            ;;
        0)
            : # fall through to manual-fallback message below
            ;;
        *)
            {
                echo "discover-thread: parent codex appears to hold MORE THAN ONE"
                echo "open rollout fd under $SESSIONS_ROOT. Refusing to guess."
                echo "Candidate thread_ids:"
                for k in "${!seen[@]}"; do echo "  - $k"; done
                echo "Next: use /status in codex to identify the correct id"
                echo "and call driver.bind_thread(thread_id=...) directly."
            } >&2
            exit 2
            ;;
    esac
fi

# All sources failed — explicit fail with operator-actionable message.
{
    echo "discover-thread: could not determine the parent codex thread_id."
    echo ""
    echo "Tried:"
    echo "  1. \$CODEX_THREAD_ID env (unset or malformed)"
    if [[ -d "/proc/$PPID/fd" ]]; then
        if [[ -z "${SESSIONS_ROOT:-}" ]]; then
            echo "  2. /proc/<ppid>/fd scan (skipped — \$CODEX_HOME/sessions missing)"
        else
            echo "  2. /proc/<ppid>/fd scan under $SESSIONS_ROOT (no match)"
        fi
    else
        echo "  2. /proc/<ppid>/fd scan (skipped — not on Linux)"
    fi
    echo ""
    echo "Next steps:"
    echo "  - In your codex session, type /status. Find the line"
    echo "    starting with 'thread_id:' and copy the UUID."
    echo "  - Then call driver.bind_thread(thread_id=<that uuid>) manually."
} >&2
exit 1
```

Exit codes:
- `0` — `stdout` has the thread_id; operator/caller binds it.
- `1` — no source resolved a value. Use `/status` fallback.
- `2` — multi-candidate (invariant violation). Use `/status` fallback; stderr lists candidates so the operator can pick or investigate.

Notes on cross-platform:

- **Linux**: both sources available; `/proc/<ppid>/fd` works without extra tools.
- **macOS** (and **Windows under WSL/Git Bash**): script runs (bash + sed available). Source 1 (env) works if `CODEX_THREAD_ID` is set; source 2's `[[ -d /proc/$PPID/fd ]]` guard returns 1 (no /proc), script falls through to the "all sources failed" stderr block. Operator pastes `/status` thread_id manually. A future upgrade could swap `/proc` for `lsof -p <ppid>` on macOS to enable source 2; not in v1.
- **Native Windows (cmd / PowerShell, no bash)**: script does not run at all. Per Goals above, native Windows v1 = manual `/status` bind only; SKILL.md instructs the model to skip the script step on this platform.

The script makes no `sqlite3` / `sort -V` / GNU-coreutils assumptions, so the macOS BSD-tools variant works as-is. A native-Windows `.ps1` sibling is a separate follow-up.

#### `skills/multiagent/SKILL.md` change

There is no standard env var across codex / claude / cursor hosts that exposes the skill's install root, and the repo-relative path `skills/multiagent/scripts/...` only works when cwd is the repo root. To avoid this fragility, **the discovery script is embedded inline in SKILL.md as a copy-pasteable bash heredoc** — the model runs it via a single `bash <<'DISCOVER_EOF' ... DISCOVER_EOF` shell invocation. The file at `skills/multiagent/scripts/discover-thread.sh` is the canonical source of truth (used by CI tests); SKILL.md inlines a copy, and a CI check asserts the heredoc body and the file are byte-identical.

Add a new top section before "Default Workflow":

````markdown
## Initialization (REQUIRED before any submit_task)

You MUST bind the driver to its parent codex thread once per session,
BEFORE calling any parent-link skill (submit_task / resume_task /
submit_contract_task with skill ∈ {chat, chat_resume, fanout,
fanout_strict, route} — i.e. anything that spawns a nested codex on a
slave). Bash-only / powershell-only submissions don't need this.

Step 1 — run this discovery script as a shell tool call. **Pipe the script body into bash via a quoted heredoc** (`bash <<'DISCOVER_EOF' ... DISCOVER_EOF`) rather than `bash -c '...'`. The script contains single-quoted regex literals (`UUID_RE='^[0-9a-fA-F]{8}-...$'`) that would conflict with single quotes around a `-c` argument. The quoted heredoc form (`<<'DISCOVER_EOF'`) disables variable / command expansion AND preserves the script byte-for-byte, satisfying the CI drift check below.

**Critical**: do NOT wrap the invocation in `THREAD_ID=$(...)` or any other shell-variable assignment. Shell-tool calls do not persist environment between invocations — a variable set in one tool call vanishes before the next. The script must print the thread_id to stdout so the model sees it directly in the tool-call response. The model then copies that value verbatim into the `bind_thread` MCP call.

```
bash <<'DISCOVER_EOF'
#!/usr/bin/env bash
# [INLINE FULL SCRIPT BODY HERE — must be byte-for-byte identical to
#  skills/multiagent/scripts/discover-thread.sh, including the shebang,
#  comment header, set flags, UUID_RE, valid(), Source 1 (env), Source 2
#  (/proc walk + canonicalized SESSIONS_ROOT + multi-candidate fail), and
#  final "all sources failed" stderr block. CI test
#  `skill_md_inline_in_sync_test.sh` enforces byte-identical match.]
DISCOVER_EOF
```

(In this spec the inline body is elided for brevity — the implementation MUST paste the complete script verbatim; the CI drift test below would otherwise fail immediately. See [discover-thread.sh][script] for the source of truth.)

It prints the thread_id to stdout, or exits non-zero with an actionable
message on stderr. Exit codes: 0 = success, 1 = no source resolved,
2 = multi-candidate invariant violation.

Step 2 — read the UUID from Step 1's stdout, then call `bind_thread` with that value as a literal argument. Expect `{ "bound": true, "thread_id": "..." }` in the response:

```
driver.bind_thread(thread_id="<paste the UUID from Step 1's stdout here>")
```

If Step 1 exits non-zero (typical on macOS / Windows without `CODEX_THREAD_ID` set, or in containers without /proc access), or if running on native Windows (cmd / PowerShell, no bash — skip Step 1 entirely), ask the user:

> "I need your current Codex thread id to set up parent linkage.
>  In your codex session, run `/status` and paste the UUID after
>  `thread_id:` here."

Then call `bind_thread` with that value.

[script]: ../../skills/multiagent/scripts/discover-thread.sh

If you skip this step entirely, every parent-link submission will fail
with: "driver not bound to a codex thread; call `bind_thread` first ..."

Re-binding mid-session (e.g. after `/resume` switches to a different
codex thread) is supported: just re-run discovery and call
`bind_thread` again — the new value replaces the old one.
````

Why inline rather than `bash skills/multiagent/scripts/discover-thread.sh`:

- No assumption about cwd or install location.
- Works identically regardless of how the host distributes skills (codex `~/.codex/skills/`, claude `~/.claude/skills/`, repo-local checkout, etc.).
- Model already has the SKILL.md text in context — pasting the heredoc block into a single shell tool call resolves no path.
- File version exists for unit tests and source-of-truth; CI check keeps the two copies in lockstep.

### Q2: sidecar-confirms-exec origin classification

(Unchanged from the previous design rounds — Q2 is orthogonal to Q1's rearchitecture.)

The current classifier in `applyCodexSessionMeta` blanket-maps `originator=codex_exec || source=exec → SessionOriginAgentTask`. After Q2, `agent_task` requires BOTH the rollout to be exec-mode AND a corresponding loom-meta sidecar with `origin=agent_task`. Codex-native `subagent` classification is untouched and continues to take priority over any sidecar.

#### `pkg/agentbackend/codex/sessions.go::applyCodexSessionMeta` — remove the codex_exec branch

```go
func applyCodexSessionMeta(sess *agentbackend.Session, p codexMetaPayload) {
    spawn := p.Source.Subagent.ThreadSpawn
    parentID := firstNonEmpty(p.ParentThreadID, spawn.ParentThreadID)
    if p.ThreadSource == "subagent" || parentID != "" || p.AgentNickname != "" || spawn.AgentNickname != "" {
        sess.Origin = agentbackend.SessionOriginSubagent
        sess.ParentID = parentID
        sess.AgentName = firstNonEmpty(p.AgentNickname, spawn.AgentNickname)
        sess.AgentRole = firstNonEmpty(p.AgentRole, spawn.AgentRole)
        return
    }
    // REMOVED in Q2: codex_exec / source==exec → SessionOriginAgentTask.
    // agent_task is now granted exclusively by applyLoomMeta when a sidecar
    // confirms an exec-mode rollout originated from a loom agent.
    if sess.Origin == "" {
        sess.Origin = agentbackend.SessionOriginUser
    }
}
```

#### `pkg/agentbackend/codex/sessions.go::applyLoomMeta` — sidecar-confirms-exec

```go
// Signature change: takes rolloutIsExec so it can apply both gates
// (subagent priority and exec-mode requirement) without needing
// transient fields on Session.
func applyLoomMeta(base string, sess *agentbackend.Session, rolloutIsExec bool) {
    if base == "" || sess == nil { return }
    m, ok := readLoomMeta(base, sess.ID)
    if !ok { return }
    if m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID != sess.ID {
        return
    }
    // Gate 1: codex-native subagent classification is authoritative.
    if sess.Origin == agentbackend.SessionOriginSubagent { return }
    // Gate 2: only exec-mode rollouts are eligible for sidecar upgrade.
    if !rolloutIsExec { return }

    sess.Origin = agentbackend.SessionOriginAgentTask
    if m.ParentSessionID != "" && sess.ParentID == "" {
        sess.ParentID = m.ParentSessionID
    }
    if m.ParentAgentID != "" && sess.ParentAgentID == "" {
        sess.ParentAgentID = m.ParentAgentID
    }
    if m.ParentDisplayName != "" && sess.ParentDisplayName == "" {
        sess.ParentDisplayName = m.ParentDisplayName
    }
}
```

#### Call-site plumbing in `scanCodexSession`

`p` is currently scoped inside the `case "session_meta":` block (`sessions.go:270`); `applyLoomMeta` is invoked after the loop (`sessions.go:322`). Hoist a boolean local out of the loop, OR-accumulator semantics so a later session_meta record that lacks exec fields cannot demote an earlier true:

```go
// inside scanCodexSession, before the loop:
var rolloutIsExec bool

// inside case "session_meta":, after unmarshalling p:
rolloutIsExec = rolloutIsExec || p.Originator == "codex_exec" || p.Source.Kind == "exec"
applyCodexSessionMeta(&res.session, p)

// after the loop:
applyLoomMeta(base, &res.session, rolloutIsExec)
```

#### Classification matrix after the change

| Rollout signal | Sidecar? | Before | After |
|---|---|---|---|
| Codex-native `thread_source=subagent` / `parent_thread_id` set | n/a | Subagent | Subagent (untouched, gate 1) |
| `source=cli` (interactive) | absent | User | User |
| `source=cli` (interactive) | present (stale/corrupted) | User (existing contract) | User (gate 2 blocks) |
| `source=exec, originator=codex_exec` | absent | AgentTask | **User** (Q2 fix) |
| `source=exec, originator=codex_exec` | present (origin=agent_task) | AgentTask + parent fields merged | AgentTask + parent fields merged |
| Pre-P1 historical exec rollouts | absent | AgentTask | **User** (acceptable semantic shift) |

## Testing

### Unit tests

**`internal/driver/tools_bind_thread_test.go`** (new):
- `TestBindThread_SetsAndStores` — call `Tools.BindThread` with valid id, assert parentThread is set and returns BindResult with id + agent_id + display_name.
- **`TestBindThreadTool_RegisteredInAll`** (mandatory, non-gated) — call `newTestTools(t, nil).All()`, scan for an entry whose `Name() == "bind_thread"`, fail if not present. Then unmarshal the entry's `InputSchema()` and assert it has a `thread_id` required property. Finally invoke the entry's `Call(ctx, json.RawMessage(\`{"thread_id":"019ef3bd-42c8-7731-85b7-7177ae747389"}\`))` and assert the JSON response decodes to `{bound:true, thread_id:"019ef3bd-...", agent_id:..., display_name:...}`. **This test catches the "BindThread method exists on Tools but bindThreadTool is missing from All()" failure mode** — the env-gated integration test alone is insufficient because most CI runs skip it.
- `TestBindThread_RejectsInvalidFormat` — empty, whitespace-only, contains `$`, `{`, `}`, `/`, newline, length > 128 — each returns error and does NOT mutate parentThread.
- `TestBindThread_Idempotent_SameValue` — call twice with same id, second call no-op.
- `TestBindThread_Replaces_DifferentValue` — call with id1 then id2, parentThread == id2.

**`internal/driver/tools_test.go`** (modifications):

Two migration categories, audited from the test file:

1. **Loom-origin stamping tests** (use `writeLoomOriginCurrentFile`) — migrate to BindThread:

  ```go
  tools := newLoomTestTools(t, sdk, /*codexHome*/ "", "drv-X", "driver-X")
  _, err := tools.BindThread(ctx, "thr-X")
  require.NoError(t, err)
  ```

  Affected (from `grep -n writeLoomOriginCurrentFile internal/driver/tools_test.go`):
   - `TestSubmitTaskDefaultStampsLoomOrigin` (`:2265`) — add `BindThread("thr-parent")` before submit, assert ParentLink.SessionID == "thr-parent". Skill defaults to `fanout`, which IS parent-link, so bind required.
   - `TestSubmitTaskChatStampsLoomOrigin` (`:2316`) — same pattern with `"thr-chat"`.
   - `TestSubmitTaskBashDoesNotStampLoomOrigin` (`:2346`) — **different**: remove the `writeLoomOriginCurrentFile` call but do NOT add `BindThread`. New semantic: bash is NOT a parent-link skill, so SubmitTask runs without bind AND systemContext stays empty. Assertion changes from "marker present pointing at thr-bash" to "marker absent / SystemContext is empty string". The test's intent (bash doesn't stamp) is preserved and now more directly captured.
   - `TestSubmitContractTaskStampsLoomOrigin` (`:2375`) — same pattern with `"thr-contract"`. This test exercises the normal selectTarget contract route.
   - **`TestSubmitContractTaskDriverFanoutCarriesLoomOrigin` (`:2706`)** — **does not currently use `writeLoomOriginCurrentFile`** but DOES depend on `loomOriginMarker()` returning something. After Q1 it will fail (`requireBoundThread` returns an error). Migration: add `_, err := tools.BindThread(context.Background(), "thr-fanout"); require.NoError(t, err)` before `submitContractToolForTest(t, tools).Call(...)`. Existing assertions on `p.AgentID == "drv-5"` / `p.DisplayName == "driver-5"` stay; ADD assertion `p.SessionID == "thr-fanout"` to verify the captured-thread plumbing reaches the contract runner.
   - **`TestSubmitContractTaskUsesDriverFanoutWhenRecommended` (`:285`)** — route lands on driver_fanout (two slaves match). Currently passes because `loomOriginMarker()` returns a non-empty marker even without a bound id. After Q1 the bind guard fires before route execution. Migration: add `tools.BindThread(ctx, "thr-fanout-uses")` to the test setup. No assertion change needed (the test asserts route selection, not marker contents).
   - **`TestSubmitContractTaskDriverFanoutRequiresConfiguredRunner` (`:322`)** — also routes to driver_fanout; designed to assert the "no runner" error. After Q1 the bind guard fires BEFORE the runner-presence check, so without migration the test would assert the wrong error string. Migration: add `tools.BindThread(ctx, "thr-fanout-norunner")` so the test reaches the actual no-runner code path and the existing "no driver contract runner configured" assertion holds.
   - **New** `TestSubmitContractTaskDriverFanoutFailsWithoutBind` — same fixture as above, but DO NOT call BindThread. Expect `submitContractToolForTest(t, tools).Call(...)` to return an error containing "driver not bound to a codex thread". Codifies that Path A (driver_fanout early return at `contract_tools.go:75`) is guarded.
   - **New** `TestSubmitContractTaskDriverFanoutFailWithoutBind_NoObserverSideEffect` — same fixture, but configure `tools.observerRelay()` to point at an `httptest.NewServer` that records every request. Call submitContractToolForTest; assert (a) it returns the bind error, AND (b) the server received ZERO `POST /api/resource-snapshots` (or whatever the SaveResourceSnapshot endpoint is). This test would catch an implementation that leaves `SaveResourceSnapshot` in its current pre-guard position at `contract_tools.go:62` — the "guard first, save second" reorder above MUST hold. Without this assertion the default test fixture's `observerRelay()` is nil, so the wrong implementation could still pass the bind-error assertion while silently leaking the snapshot in production.
   - `TestResumeTaskStampsLoomOrigin` (`:2408`) — same pattern with `"thr-resume"`. Resume is always parent-link.
   - `writeLoomOriginCurrentFile` helper (`:2240`) — **delete** after all callers migrated.

2. **Non-loom tests that happen to invoke a parent-link skill** — `~53` tests reference `SubmitTask` / `ResumeTask` / `SubmitContractTask` (`grep -n SubmitTask\|ResumeTask\|SubmitContractTask`). Most use bash / non-parent-link skills and are NOT affected (the guard short-circuits on `isParentLinkDelegation == false`). The remaining tests that exercise chat / chat_resume / fanout / fanout_strict / route paths MUST call BindThread before the submission.

  **Audit protocol** (do this during implementation, not in spec):

  ```
  # Pre-implementation: list candidate tests
  rg -nE 'SubmitTask|ResumeTask|SubmitContractTask' internal/driver/tools_test.go

  # For each, check whether it exercises a parent-link skill:
  # - Inspect the Args / fakeSDK fixture's Skill field
  # - If Skill ∈ {"", "chat", "chat_resume", "fanout", "fanout_strict", "route"},
  #   add a BindThread call to the test setup (one line via
  #   newTestTools().BindThread(...) helper).
  # - Otherwise, no change.
  ```

  The narrowed guard (`isParentLinkDelegation`) is the reason this isn't a wholesale rewrite. Examples of likely-affected tests (need manual audit, line numbers from the reviewer's finding): `tools_test.go:1045`, `:1911`, `:2622` — plus the loom-origin tests above.

  **New**: `TestSubmitTask_ChatSkill_FailsWithoutBindThread` and `TestSubmitTask_BashSkill_SucceedsWithoutBindThread` (and parallels for resume/contract) — assert the narrowed-guard semantics explicitly.

**`pkg/agentbackend/codex/sessions_test.go`** (Q2 modifications + additions):
- `TestGetSession_CodexExecManifestIsAgentTaskAndTitleSkipsManifest` (`:305`) — add a loom-meta sidecar with `origin=agent_task` to the fixture; rename to `TestGetSession_CodexExecWithSidecarIsAgentTaskAndTitleSkipsManifest`.
- `TestListSessionsSidecarDoesNotRelabelUserSession` (`:390`) — **keep as-is** (new gate 2 in `applyLoomMeta` upholds the contract).
- `TestListSessionsLoomMetaMergesPartialParentLink` — **keep as-is**.
- **New**: `TestApplyCodexSessionMeta_ExecRunWithoutSidecarIsUser`, `TestApplyLoomMeta_PreservesSubagentClassification`, `TestApplyLoomMeta_RequiresExecModeRollout`, `TestApplyLoomMeta_UpgradesExecModeWithSidecar`, `TestScanCodexSession_MultiSessionMeta_ExecFlagAccumulates` (OR-accumulator test for the loop-outer bool).

### Script tests

**`skills/multiagent/scripts/discover-thread_test.sh`** (new bats / shellspec / bash-test suite, run as part of CI):

Fixtures use a valid UUID (a UUIDv7, to match what Codex emits) like `019ef3bd-42c8-7731-85b7-7177ae747389` for the env / fd paths. Tests:

**Test-isolation invariant (env-invalid / all-sources-fail tests only)**: tests that expect the env path to fall through or all sources to fail MUST set `CODEX_HOME="$tmpdir"` to a directory that does NOT contain `sessions/`. Without this, the env-fall-through tests would race against whatever the host shell's parent codex (if any) currently holds open in `/proc/$PPID/fd` — a stray rollout fd from the CI runner's outer codex session would let the test "succeed" via the /proc path and never exercise the intended exit-1 behavior. With it, the /proc walk's `[[ -n "$SESSIONS_ROOT" ]]` guard returns false-y (sessions root missing), the walk is skipped, and the test deterministically reaches the "all sources failed" exit 1 branch. The `/proc` positive cases below DO require a populated `$CODEX_HOME/sessions/` and their own rollout fixtures; the invariant doesn't apply there.

- `env_valid_uuid_returns_immediately` — `CODEX_THREAD_ID=019ef3bd-42c8-7731-85b7-7177ae747389 CODEX_HOME=$tmpdir ./discover-thread.sh` → stdout has the UUID, exit 0; never touches /proc.
- `env_invalid_format_falls_through` — `CODEX_THREAD_ID='thr-from-env' CODEX_HOME=$tmpdir ./discover-thread.sh` (where `$tmpdir/sessions` does not exist) → validator rejects env, /proc walk skipped, exit 1.
- `env_unexpanded_placeholder_falls_through` — `CODEX_THREAD_ID='${CODEX_THREAD_ID}' CODEX_HOME=$tmpdir ./discover-thread.sh` → exit 1 (same isolation).
- `env_whitespace_falls_through` — `CODEX_THREAD_ID='   ' CODEX_HOME=$tmpdir ./discover-thread.sh` → exit 1 (same isolation).
- `proc_fd_single_match_returns_uuid` (Linux only, `[ -d /proc ]` skip elsewhere) — set up `$tmpdir/sessions/2026/06/23/`, create a real `rollout-...-019ef3bd-42c8-7731-85b7-7177ae747389.jsonl`, then exercise the **parent-holds-fd, script-runs-as-child** topology:

  ```bash
  # The test harness explicitly passes ROLLOUT_PATH and SCRIPT through
  # env, then opens the rollout on fd 9 (parent shell holds it) and runs
  # the script as a CHILD process. Discover-thread.sh's $PPID therefore
  # points at the harness, which holds the rollout fd in /proc/$PPID/fd/9.
  #
  # SCRIPT is resolved relative to THIS TEST FILE's location (not $(pwd)),
  # so the test passes regardless of cwd (CI vs developer-laptop vs
  # nested invocations don't differ).
  TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
  SCRIPT="$TEST_DIR/discover-thread.sh"   # test lives next to script
  ROLLOUT_PATH="$tmpdir/sessions/2026/06/23/rollout-2026-06-23T17-08-20-019ef3bd-42c8-7731-85b7-7177ae747389.jsonl"
  CODEX_HOME="$tmpdir" \
  ROLLOUT_PATH="$ROLLOUT_PATH" \
  SCRIPT="$SCRIPT" \
      bash -c '
          exec 9< "$ROLLOUT_PATH"
          # script runs as child of this bash; its $PPID is our PID;
          # SCRIPT is absolute so cwd does not matter.
          bash "$SCRIPT"
      '
  # Assert: stdout is "019ef3bd-42c8-7731-85b7-7177ae747389", exit 0.
  ```

  PPID is readonly in the child shell — you cannot set it. The trick is to make the harness shell BE the parent (open fd 9 in the harness; run the script as its child). Same technique for the multi-candidate (`exec 9< rollout1; exec 8< rollout2`) and dedupe variants below; always pass `ROLLOUT_PATH` and `SCRIPT` explicitly through env, and resolve `SCRIPT` via `${BASH_SOURCE[0]}` so the test is location-independent.
- `proc_fd_rejects_path_outside_sessions_root` — open a UUID-shaped jsonl file OUTSIDE `$CODEX_HOME/sessions/` → not picked up → exit 1.
- `proc_fd_rejects_non_uuid_basename` — create a file like `rollout-2026-06-23T17-08-20-not-a-uuid.jsonl` and open it → script doesn't extract anything → exit 1.
- `proc_fd_multi_candidate_fails_loud` — open TWO rollout files with different UUIDs (simulates invariant violation) → exit 2, stderr lists both candidates.
- `proc_fd_dedupe_same_uuid_across_walk` — same UUID open multiple times / inherited via shell wrapper → counts as one candidate → exit 0 (not 2).
- `all_sources_fail_explicit_message` — env unset, no /proc match → exit 1 with the operator-actionable stderr block.

Exit code asserts are part of every case.

No sqlite, no `state_<N>.sqlite`, no `sort -V`. The earlier round's sqlite tests were stale and have been removed.

**`skills/multiagent/scripts/skill_md_inline_in_sync_test.sh`** (new, CI gate):

The skill ships the discovery script in two places — the canonical file at `skills/multiagent/scripts/discover-thread.sh` AND an inlined copy in `skills/multiagent/SKILL.md` (so the model can paste it without path resolution). The two must stay byte-identical or the inline path silently drifts.

Test:
- Extract the heredoc body between `bash <<'DISCOVER_EOF'` and `DISCOVER_EOF` inside the `## Initialization` section of `skills/multiagent/SKILL.md` (e.g. `awk '/^## Initialization/{p=1} p && /<<'\''DISCOVER_EOF'\''/{flag=1; next} flag && /^DISCOVER_EOF$/{exit} flag' SKILL.md`).
- `diff` the extracted body against `skills/multiagent/scripts/discover-thread.sh`.
- Test fails if they differ. CI runs this on every PR touching either file.

Implementation note: keeping the two in sync is a small operational tax (touch both files when updating the script) but eliminates the entire class of "path resolution failed in production" failures. The drift-detection test makes the tax visible.

### Integration tests

**`internal/driver/bind_thread_integration_test.go`** (new, env-skip gated by `LOOM_BIND_THREAD_INTEGRATION=1`):

- `TestIntegration_DriverBindFlow`: drive a real driver-agent via stdio MCP, send a `bind_thread` request, then a `submit_task` request. Assert:
  1. bind_thread returns `{bound: true, thread_id: ...}`.
  2. submit_task succeeds and the captured DelegateTaskRequest's SystemContext contains a `loom_origin` marker with `parent_session_id` equal to the bound id.
  3. submit_task BEFORE bind_thread returns the "not bound" error.

### E2E

Existing PR #31 prod_test stack: update the driver-side codex skill instructions to call `bind_thread` first (this becomes part of the test fixture). Then `driver codex exec → submit_task slave-A → submit_task slave-B → wait_task`. Acceptance:

1. driver session displays `origin=user` (Q2).
2. Both slave sessions' sidecars contain a non-empty `parent_session_id` equal to the driver's current thread_id (Q1).
3. Commander UI shows the two slave sessions nested under the driver session, default-collapsed.

## Rollout

- Three commits land in PR #31:
  - `feat(driver): bind_thread MCP tool + submit/resume/contract guards (#24)`
  - `feat(skills): discover-thread.sh + SKILL.md init step for multiagent (#24)`
  - `fix(codex): loom-meta sidecar confirms agent_task only for exec rollouts (#24)`
- All commits include their tests.
- No new Go dependencies. (Earlier drafts proposed `github.com/shirou/gopsutil/v3`; with bind_thread the Go side does not introspect processes.)

## Decisions and open questions

- **Why explicit bind_thread instead of automatic discovery**: the previous design rounds explored env vars (only set by codex in certain variants/modes), parent-process fd introspection (cross-platform issues, gopsutil dep, multi-fd corner cases), and various heuristics. Each had a "silent-degrade-to-empty" failure mode that obscured operator misconfiguration. Explicit binding makes the failure visible at the boundary: the skill either succeeds at discovery (and binds) or fails loudly (and the user knows to investigate).
- **Why a shell script instead of Go discovery code**: the discovery logic is inherently platform-and-environment specific (env var name, sqlite path layout, /proc layout). A shell script (with `.ps1` sibling for Windows) is easier to update, easier to test, and decouples the discovery from driver-agent's release cycle.
- **`/resume` mid-session**: if the user switches threads with `/resume` inside an interactive codex, the skill should re-run `discover-thread.sh` and call `bind_thread` again. The tool's "calling with a new value replaces" semantic makes this safe. SKILL.md should mention this.
- **Sidecar gates 1 and 2 are deliberately strict**: gate 1 preserves Codex-native subagent priority (existing contract, `TestListSessionsSidecarDoesNotRelabelUserSession` test enshrines it); gate 2 (exec-mode requirement) prevents stray sidecars from upgrading interactive sessions.
- **Pre-P1 historical exec rollouts shift from AgentTask → User**: accepted as a semantic correction. A human-typed `codex exec` from a shell was never an "agent task" in the loom sense — there was no agent.
- **Naming**: per #29, in new code we say "Codex thread_id" or "backend-native session id", reserve `cse_…` discussion for "agentserver bridge session id". The `bind_thread` tool's `thread_id` parameter name explicitly carries the backend-native semantic.
