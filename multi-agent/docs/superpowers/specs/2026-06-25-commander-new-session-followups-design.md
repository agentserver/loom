# Commander New-Session Follow-ups — Design

- Status: Draft
- Date: 2026-06-25
- Owner: yzs15
- Parent: `2026-06-25-commander-new-session-design.md` + `2026-06-24-commander-mobile-design.md`

Three small changes after the initial new-session ship + the deployed prod_test loop:

1. `+` regression — after the first session is created, `+` does not work any more.
2. Add per-daemon collapse so the daemon's session list can fold to clear screen real estate.
3. `codex_home` per-agent default lives in `~/.cache/multi-agent/<short_id>/.codex`, which is far from the project files and mingles per-session state across projects. Make the default `<agent.workdir>/.codex` instead (explicit `agent.codex_home` still wins).

## Goals

- Re-clicking `+` after a session was created creates a fresh draft (no permanent lockout).
- Each daemon row gets a `▾` / `▸` chevron that collapses its session list (incl. virtual pending row and the daemon-error line).
- Sessions on one agent keep their codex state alongside the project files by default.

## Non-goals

- Per-session working_dir or per-session CODEX_HOME (still single home per agent).
- Persisting collapse state across page reloads (in-memory only; matches existing session-toggle expand state).
- Re-architecting the single-slot pending invariant (still single slot; but expanded to allow new drafts when prior pending is `'submitting'`).

## Architecture

### 1. `+` regression — relax single-slot for `'submitting'`

Today `createPendingSession(daemonID)` and the DaemonSessionTree `disabled` rule both block ALL new drafts whenever ANY `pendingSession` exists, in BOTH `'draft'` and `'submitting'` phases. The intent was to keep the single placeholder row visible until `loadTree()` confirms the real row.

In practice: after the first turn lands, the spec's `loadTree()` is called once. If the slave's `list_sessions` response on that single call does not yet contain the new session (the worker exists in-process but the backend's `list_sessions` is eventually-consistent), `pendingSession` stays in `'submitting'` forever, and every `+` button on the page becomes permanently disabled. The user's "+ has no effect" report matches this exact failure mode.

**Fix**: differentiate the two phases:

- `'draft'` continues to block ALL other-daemon `+` (preserves draft-loss protection).
- `'submitting'` no longer blocks `+`. The committed session is already on the server — the placeholder is a presentational nicety, not a guard. Creating a fresh draft evicts the `'submitting'` placeholder. The just-committed session will surface on the next `loadTree()` regardless of whether the placeholder lived or not.

Concretely:
- `createPendingSession(daemonID)`:
  - If `current?.phase === 'draft' && current.daemonID !== daemonID` → return (single-draft guard).
  - If `current?.phase === 'draft' && current.daemonID === daemonID` → re-select existing (no fresh UUID).
  - Else (no pending, or pending is `'submitting'`) → mint UUID, set new `'draft'` pending, select.
- `DaemonSessionTree` `+` disabled rule: only when `pendingSession?.phase === 'draft' && pendingSession.daemonID !== daemon.daemon_id`. The owning daemon's `+` is enabled (re-clicking it re-selects). In `'submitting'`, NO `+` is disabled.
- The disabled-button `title` text simplifies: only the draft title `先发送或丢弃当前草稿` is reachable.

**Submitting-phase 404 handling + bounded retry**: when pending phase is `'submitting'`, the detail-fetch effect is allowed to call `apiGet(sessionPath(...))` — but the codex backend's `GetSession` reads the same sessions file that `list_sessions` does, so if `loadTree()` didn't yet see the row, the detail fetch will 404 too. Today CommanderApp's detail effect catches and surfaces the error via `setError(err.message)` → the whole page renders `加载失败: HTTP 404`.

A bare swallow isn't enough: the app only calls `loadTree()` on init / login / post-turn. Without an extra trigger, a `submitting` session whose row hasn't been visible on the post-turn refresh would stay as the empty `新建会话` placeholder forever.

Fix has two parts:

1. **404 catch swallowing** — when `pendingSession?.sessionID === selected.sessionID && pendingSession.phase === 'submitting'` AND the detail fetch fails with `HTTP 404`, do NOT call `setError`. Keep the placeholder `sessionDetail = { session: { ID: sid, Title: '新建会话(同步中…)' }, messages: [] }` (use the submitting-phase title text so the user sees the "syncing" state, not the draft placeholder).

2. **Bounded retry** — when the catch above fires (or whenever `pendingSession.phase === 'submitting'`), schedule a `loadTree()` retry on a fixed 500 ms tick, capped at 5 retries within the same submitting cycle (overall ~2.5s). The retry counter resets when `pendingSession` clears (i.e. real row arrived) or when phase changes back to `'draft'` (user evicted via new `+`). If all 5 retries miss, leave the placeholder visible and stop retrying — operator can refresh the page if needed.

   Implementation: add a `submittingRetryRef = useRef({sessionID: '', attempt: 0})` so the retry schedule survives effect re-runs but is keyed on the pending session ID (resets on `pendingSession` change). When the `[selected, tree, pendingSession]` effect fires AND pending is `'submitting'` AND no real row in `tree`, increment the attempt; if `< 5`, `setTimeout(loadTree, 500)`. Otherwise no-op.

Practical shape inside the catch block:

```ts
.catch((err: Error) => {
  if (cancelled) return;
  const isSubmittingPending =
    pendingSession?.sessionID === selected.sessionID
    && pendingSession.phase === 'submitting';
  if (isSubmittingPending && /HTTP 404$/.test(err.message)) {
    setSessionDetail({ session: { ID: selected.sessionID, Title: '新建会话(同步中…)' }, messages: [] });
    return;  // bounded retry is scheduled by the effect below, not here
  }
  setError(err.message);
});
```

And the retry scheduler (inside the same effect, after the detail fetch is fired):

```ts
useEffect(() => {
  if (!pendingSession || pendingSession.phase !== 'submitting') {
    submittingRetryRef.current = { sessionID: '', attempt: 0 };
    return;
  }
  if (submittingRetryRef.current.sessionID !== pendingSession.sessionID) {
    submittingRetryRef.current = { sessionID: pendingSession.sessionID, attempt: 0 };
  }
  // Real row already visible? loadTree's success path will clear pending.
  const realRow = tree?.daemons
    .find((d) => d.daemon_id === pendingSession.daemonID)
    ?.sessions?.find((s) => s.session_id === pendingSession.sessionID);
  if (realRow) return;
  if (submittingRetryRef.current.attempt >= 5) return;  // bound
  submittingRetryRef.current.attempt += 1;
  const t = window.setTimeout(() => { void loadTree(); }, 500);
  return () => window.clearTimeout(t);
}, [pendingSession, tree, loadTree]);
```

The brief still preserves the existing virtual row + `loadTree`-clears-pending logic. The only weakened invariant is "single placeholder row across both phases" → "single DRAFT placeholder; `'submitting'` placeholder is opportunistic".

### 2. Per-daemon collapse

Add an in-memory collapsed-daemons set in `DaemonSessionTree`:

```ts
const [collapsedDaemons, setCollapsedDaemons] = useState<Set<string>>(new Set());
```

Each daemon row gets a leading chevron button (`Chevron▾` when expanded, `Chevron▸` when collapsed). Click toggles the daemon's `daemon_id` in the set. When collapsed:
- Real session rows are hidden.
- Daemon error line (`<p className="daemon-error">`) is hidden.
- The `+` button remains visible and clickable (creates a draft and auto-expands the daemon).

**Draft visibility invariant** — the daemon currently holding a `'draft'` pending session MUST remain visually expanded so the user can always reach the `×` discard button. Two enforcement points:

1. The chevron toggle handler refuses to collapse a daemon when `pendingSession?.phase === 'draft' && pendingSession.daemonID === daemon.daemon_id`: it just no-ops (or, equivalently, the chevron button is rendered with `disabled` + `title="当前草稿正在此 daemon 下,先发送或丢弃"`).
2. The render path treats the daemon as expanded whenever it owns a `'draft'` pending — i.e. `const isCollapsed = collapsedDaemons.has(daemon.daemon_id) && !(pendingSession?.phase === 'draft' && pendingSession.daemonID === daemon.daemon_id)`. This guarantees that even if the daemon was collapsed BEFORE the user created the draft (e.g. via another tab racing), the virtual row + `×` button are still visible.

The virtual pending row itself (real or `'submitting'` placeholder) is rendered iff `!isCollapsed`. `'submitting'` placeholders DO hide on collapse — losing the placeholder is fine because the session is already on the server and `loadTree()` will surface it next refresh.

Default: all daemons start expanded (existing behavior).

`onCreateSession` is wrapped locally: clicking `+` calls `setCollapsedDaemons(prev => { const next = new Set(prev); next.delete(daemonID); return next; })` before invoking the prop. Guarantees the virtual row is visible after the user clicks `+`.

Mobile: the chevron button is a 44×44 hit area on `< 1024px` (consistent with other touch targets). Sits at the left of the daemon row, before the daemon name.

### 3. `codex_home` default → `<workdir>/.codex`

Change `pkg/agentbackend/codexhome.go` `ResolveCodexHome` signature + logic:

```go
func ResolveCodexHome(codexHome, loomHome, shortID, workDir string) string {
    if codexHome != "" {
        return codexHome
    }
    if workDir != "" {
        // Workdir may arrive relative (e.g. config "workdir: ./"). The codex
        // subprocess runs with cmd.Dir = workDir, so a relative CODEX_HOME
        // would resolve differently in the child vs. the parent — sessions
        // appear to vanish. Always absolutize before joining .codex; fall
        // through to the loomHome path if absolutization fails.
        if abs, err := filepath.Abs(workDir); err == nil && abs != "" {
            return filepath.Join(abs, ".codex")
        }
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

Behavior:
- Explicit `agent.codex_home` still wins (no behavior change for users who set it).
- If `agent.workdir` is set (the slave/driver configs always set it), default home becomes `<workdir>/.codex`. Project-local, easy to find.
- The legacy `<loomHome>/<shortID>/.codex` path stays as the fallback for callers that don't supply a workdir (back-compat for any test/CLI that calls `ResolveCodexHome("", "", shortID)`).

**Migration**: existing `<loomHome>/<shortID>/.codex` dirs are NOT migrated. After this change, the slave/driver point at `<workdir>/.codex` and any prior codex state in `<loomHome>/<shortID>/.codex` is effectively abandoned. Spec calls this out — operators with active state in the old location must either copy it over or set `agent.codex_home` to the old path explicitly. The prod_test setup has nothing of value in those dirs (verified empty `j672zair`, `pamei5gr` per `ls /root/.cache/multi-agent/`).

All call sites of `ResolveCodexHome` get updated to pass `cfg.Agent.WorkDir` (or the equivalent resolved workdir for that call site) as the new 4th arg. The current production call sites are:

- `cmd/slave-agent/main.go:210` — main slave registration path
- `cmd/slave-agent/main.go:569` — slave `serve-daemon` path (separate code path; easy to miss)
- `cmd/driver-agent/main.go:276` — driver serve-daemon

Plus the in-tree test files. All four sites must be updated together — a compile error catches any miss because the function signature now takes 4 args.

**Workdir normalization**: `agent.workdir` is not guaranteed by the config loader to be absolute (the slave example allows `workdir: ./`). If a relative workdir is passed to `ResolveCodexHome`, the parent process reads `repo/.codex` while the codex subprocess (which has `cmd.Dir = workDir`) interprets a relative `CODEX_HOME=repo/.codex` as `repo/repo/.codex` — read/write split, sessions vanish. To prevent this, `ResolveCodexHome` MUST call `filepath.Abs(workDir)` (or equivalent) before joining `.codex`. If `filepath.Abs` fails (cwd unreadable), fall through to the legacy `<loomHome>/<shortID>/.codex` path rather than returning a broken relative result. The codex_home_test must include a relative-workdir case that asserts the result is absolute.

## Files

| File | Change |
|---|---|
| `internal/commanderhub/webapp/src/CommanderApp.tsx` | Relax `createPendingSession` guard to only single-DRAFT (allow new drafts when prior pending is `'submitting'`). Also relax `pendingDaemonOffline` / `composerLocked` / detail-short-circuit if they incidentally check `phase !== 'draft'` — verify. |
| `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx` | Add `collapsedDaemons` state + chevron button; collapse hides session list + daemon-error; relax `disabled` rule on `+` to `pendingSession?.phase === 'draft' && pendingSession.daemonID !== daemon.daemon_id`; wrap `onCreateSession` to auto-expand owning daemon. |
| `internal/commanderhub/webapp/src/styles.css` | `.daemon-collapse-btn` selector (desktop 24px, mobile 44px). |
| `pkg/agentbackend/codexhome.go` | Add `workDir` arg; prefer `<workDir>/.codex` when set, before the loomHome fallback. |
| Call sites of `ResolveCodexHome` (3 production sites: `cmd/slave-agent/main.go:210`, `cmd/slave-agent/main.go:569`, `cmd/driver-agent/main.go:276`; plus in-tree test files) | Pass the resolved workdir as the new 4th arg. |
| `pkg/agentbackend/codexhome_test.go` | Add cases for: `workDir` set + `codexHome` empty → `<workDir>/.codex`; `workDir` empty + `shortID` set → loomHome fallback (existing); explicit `codexHome` overrides everything (existing). |
| `internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx` | New cases: chevron toggles visibility; click `+` auto-expands; `+` enabled when only `'submitting'` pending exists. |
| `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx` | New case: after first turn flips pending to `'submitting'`, `+` is enabled and clicking it creates a fresh draft. |

## Test Strategy

### Vitest unit

- `DaemonSessionTree.test.tsx` adds:
  - chevron button visible per daemon; click toggles collapsed (session list hidden, daemon-error hidden, `+` still visible).
  - Clicking `+` on a collapsed daemon re-expands it before invoking `onCreateSession`.
  - `+` on other daemons is ENABLED when the only pending is `'submitting'` (was disabled previously — explicit regression cover).
  - `+` on other daemons is DISABLED when pending is `'draft'` (unchanged from current behavior).
  - Clicking the chevron on the daemon that owns a `'draft'` pending is a no-op (daemon stays expanded; virtual row + `×` discard remain visible).
  - Render-path: even if `collapsedDaemons` contains the draft-owning daemon (e.g. stale state from before the draft was created), the daemon is forced expanded.

- `CommanderApp.mobile.test.tsx` adds:
  - "after first turn, pending phase is `'submitting'` and `+` button on the owning daemon is enabled — clicking it mints a fresh UUID and evicts the prior placeholder row."
  - "submitting-phase detail fetch 404 keeps the placeholder sessionDetail and does NOT route to the page-wide `setError` (page stays on chat, not `加载失败: HTTP 404`)."
  - "submitting-phase post-turn `loadTree()` miss → bounded retry: fake timers advance 500 ms × N; second tree mock returns the real row; pending clears and detail succeeds within the 5-attempt bound."

- `pkg/agentbackend/codexhome_test.go` extends:
  - `workDir` set, `codexHome` empty → returns `<workDir>/.codex`.
  - `workDir` empty, `shortID` set → returns legacy `<loomHome>/<shortID>/.codex`.
  - Explicit `codexHome` always wins (preserves existing).

### Playwright e2e

The existing tests in `commander.spec.ts` don't need new cases — the existing "+ POST → tree refresh swaps placeholder" e2e already covers the success-then-refresh path; the relaxed `'submitting'` guard doesn't change the placeholder→real-row swap behavior, only the lock semantics. Adding one more e2e case would over-engineer.

## Acceptance

| Requirement | Implementation | Coverage |
|---|---|---|
| `+` keeps working after a session is created (no permanent lockout) | createPendingSession + disabled rule relaxed for `'submitting'` | unit |
| Per-daemon collapse toggle in tree | DaemonSessionTree `collapsedDaemons` state + chevron | unit |
| Click `+` auto-expands the daemon | wrapper around `onCreateSession` in DaemonSessionTree | unit |
| codex_home defaults to `<workdir>/.codex` when not explicitly set | `ResolveCodexHome` 4th arg + workDir preference | unit |
| Explicit `agent.codex_home` still wins (back-compat) | `ResolveCodexHome` `codexHome != ""` short-circuit kept | unit |
| Old call sites get the new 4th arg without breaking | callers updated; existing tests still green | unit |

## Risks

- **Evicting a `'submitting'` placeholder mid-refresh**: if a new draft is created while `loadTree()` is still in-flight, the new draft replaces the placeholder. The just-committed session still shows up in the tree refresh (it has its real UUID). Net effect: one session created on the server appears as a real row; one new draft is open. Acceptable.
- **`<workdir>/.codex` may live inside a git repo**: codex will write per-session state, locks, and history there. If the project has a `.gitignore` that doesn't already exclude `.codex/`, those files get tracked. The spec does NOT auto-edit gitignores. Operators with strict repos should set `agent.codex_home` explicitly to point elsewhere.
- **Collapse state lost on refresh**: in-memory only. Per spec, persisting is out of scope.
