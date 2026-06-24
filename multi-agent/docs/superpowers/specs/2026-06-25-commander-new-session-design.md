# Commander "New Session" Button — Design

- Status: Draft (brainstorming)
- Date: 2026-06-25
- Owner: yzs15

## Problem

The Commander webapp at `/commander/` today lists every session a slave already
has, but provides no way for a user to start a fresh one. Today the only path
is to invoke the codex CLI on the slave host directly. Users want a "+" button
in the webapp that creates a new session against a specific daemon and drops
them straight into the chat composer.

## Goals

- Add a per-daemon `+` button that creates a new session and immediately
  selects it.
- Zero protocol changes: reuse the existing implicit-create behavior of the
  `POST /api/commander/daemons/<d>/sessions/<sid>/turn` endpoint
  (`internal/commander/handler.go:105` — `workerBackend.NewSessionWorker`
  fires on first reference to an unseen session ID).
- Preserve desktop and mobile experience equally.

## Non-goals

- No per-session `working_dir` / `title` / `kind` customization. The slave
  uses its configured `agent.workdir` as the cwd; backends auto-derive the
  title from the first prompt. Custom metadata requires protocol expansion
  and is out of scope.
- No "create empty session server-side" RPC. Empty sessions live only in
  client state until the first turn lands.
- No active "abandon" / "delete" UX for a pending session beyond reloading
  the page.
- No subagent-row `+` button. The `+` is per-daemon only; subagents are
  spawned by the parent session, not created from the UI.

## Architecture

### Protocol

`POST /api/commander/daemons/<d>/sessions/<uuid>/turn` (existing).
Client picks a fresh UUID v4, submits the first turn, backend implicitly
creates the session. `list_sessions` surfaces it on the next tree refresh.

### Pending session state

Because the backend has no record of a fresh session until the first turn
lands, the frontend models a `pendingSession` placeholder:

```ts
type PendingSession = { daemonID: string; sessionID: string };
```

This lives in `CommanderApp` state alongside `selected`, `tree`,
`sessionDetail`. At most one pending session exists at a time across the
whole app (creating a second `+` while a first pending is unsubmitted
replaces the first — covered in §Edge cases).

### Visibility rule

- Daemon `+` button visible only when `daemon.status === 'ok'`. Offline /
  error daemons show their existing status text instead.
- When a `pendingSession` is set on **any** daemon, every other daemon's
  `+` button is rendered with `disabled` + `title="先发送或离开当前新会话"`
  (only the daemon that owns the current pending session can still take
  a click, and clicking it on that daemon is a no-op since selection
  already points there). This guarantees an unsubmitted draft can never
  be silently overwritten by a second `+` tap.

## Component Changes

(See §Files for the full per-file change list; below describes each component in detail.)


### `DaemonSessionTree.tsx`

- New prop `pendingSession?: { daemonID: string; sessionID: string } | null`.
- New prop `onCreateSession?: (daemonID: string) => void`.
- For each daemon row: when `daemon.status === 'ok'` AND `onCreateSession`
  is provided, render a `<button class="daemon-new-session-btn">` with a
  lucide `Plus` icon at the position currently occupied by the
  `.daemon-status` text. Aria-label: `` `新建 session: ${daemon.display_name || daemon.daemon_id}` ``.
- The button is rendered `disabled` when `pendingSession != null && pendingSession.daemonID !== daemon.daemon_id`.
  When disabled it sets `title="先发送或离开当前新会话"` and a CSS `cursor: not-allowed`. Clicking is a no-op.
- The owning-daemon's button (where `pendingSession.daemonID === daemon.daemon_id`)
  remains enabled but clicking it is a no-op (selection already points at
  the existing pending session — re-invoking `onCreateSession` would mint
  a fresh UUID and discard the draft, which the disabled-rule is meant to
  prevent).
- When `daemon.status !== 'ok'`, render the existing `.daemon-status` span
  unchanged (no `+` button — independent of `pendingSession`).
- Tree-building: if `pendingSession?.daemonID === daemon.daemon_id` and no
  session with `pendingSession.sessionID` exists in `daemon.sessions`,
  prepend a virtual row to that daemon's session list. The virtual row uses
  `title: "新建会话(待提交)"`, `origin: 'user'`, `turn_state: 'idle'`,
  empty preview, no status badge. Selection / click behavior is the same as
  any other row (forwards to `onSelect`).

### `CommanderApp.tsx`

- New state: `const [pendingSession, setPendingSession] = useState<PendingSession | null>(null)`.
- New helper `createPendingSession(daemonID: string)`:
  1. If `pendingSessionRef.current != null && pendingSessionRef.current.daemonID !== daemonID`, return early — guard mirrors the disabled-button rule defensively in case the call sneaks past the UI.
  2. If `pendingSessionRef.current != null && pendingSessionRef.current.daemonID === daemonID`, just re-select the existing pending session and return (no fresh UUID, no draft loss).
  3. Otherwise: `const sid = crypto.randomUUID()`; `setPendingSession({ daemonID, sessionID: sid })`; `selectSession(daemonID, sid)`.
- Pass `pendingSession` and `createPendingSession` to both desktop
  `<DaemonSessionTree>` and `<MobileShell>` (which forwards to its embedded
  `<DaemonSessionTree>`).
- Compute `pendingDaemonOffline = pendingSession != null && (tree?.daemons.find(d => d.daemon_id === pendingSession.daemonID)?.status ?? 'offline') !== 'ok'`.
  When the currently-`selected` session matches `pendingSession`, pass
  `composerLocked={pendingDaemonOffline}` and
  `composerNote={pendingDaemonOffline ? 'daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话' : undefined}`
  to `ChatWorkspace`.
- `useEffect` that loads `sessionDetail` for `selected`: skip the
  `apiGet(sessionPath(...))` call when `selected.sessionID === pendingSession?.sessionID`.
  Set `sessionDetail = { session: { ID: sid, Title: '新建会话' }, messages: [] }`
  directly so `ChatWorkspace` renders an empty conversation with an active
  composer.
- `sendPrompt`: after a turn completes successfully (`done` state — the
  branch that calls `apiGet(sessionPath(...))` to refresh detail), check
  whether `submitted.sessionID === pendingSessionRef.current?.sessionID`.
  If so: trigger `void loadTree()` but **do not** clear `pendingSession`
  yet. Use a ref mirror `pendingSessionRef` (same pattern as
  `selectedRef`) so the closure inside `sendPrompt` sees the latest value.
- On `loadTree` resolution: if `pendingSession` is set AND the resolved
  tree contains a session with that UUID under the same daemon, clear
  `pendingSession` then. This is the **only** path that clears pending
  on success. If `loadTree` fails or returns without the row, the
  placeholder stays visible — the user can see their session is still
  on screen even when the refresh races. (Backend wins races where it
  already has the row before our turn — same code path.)

### `ChatWorkspace.tsx`

- Two new optional props:
  - `composerLocked?: boolean` — when true, force `disabled` on textarea +
    send button (same shape as the existing `empty?: boolean` flag). The
    `disabled` predicate becomes
    `empty === true || composerLocked === true || ['queued', 'answering', 'awaiting_approval'].includes(turnState)`.
  - `composerNote?: string` — when set, render a single
    `<div class="composer-note">` above the composer with the text.
    Otherwise no extra DOM. Used to surface the "daemon offline" reason.
- Both props default `undefined` and are backward-compatible.

### `MobileShell.tsx`

- New props on `MobileShell`: `pendingSession`, `onCreateSession`,
  `composerLocked?`, `composerNote?`. The first two are forwarded into the
  wrapped `<DaemonSessionTree>`. The last two are forwarded into the
  wrapped `<ChatWorkspace>`.
- The Sessions drawer's `onSelect` already routes through
  `closeOverlay('sessions', setSessionsOpen)` — `onCreateSession` is
  wrapped by a local `handleCreate(daemonID)` that calls the prop and
  then calls `closeOverlay('sessions', setSessionsOpen)` (mirroring
  `handleSelectSession`). Closing the drawer is the right UX because
  the user explicitly tapped `+` from inside the drawer and now needs
  to see the chat composer.

### `styles.css`

```css
.daemon-new-session-btn {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid #d9e1ec;
  border-radius: 6px;
  background: #fff;
  color: #1e7894;
}
.daemon-new-session-btn:hover { background: #f4f7fb; }
@media (max-width: 1023px) {
  .daemon-new-session-btn {
    width: 44px;
    height: 44px;
    min-width: 44px;
    min-height: 44px;
  }
}
```

(Pending-row styling reuses `.session-row` — no new selector needed; the
existing ellipsis + selected state work as-is.)

## Edge Cases

1. **User taps `+` twice on the same daemon**: the `+` button is rendered
   `disabled` while any pending session exists (see Visibility rule), so
   the second tap is a no-op. The first pending UUID and the user's
   composer draft are preserved.
2. **User taps `+` on daemon A, then `+` on daemon B**: daemon B's `+`
   is `disabled` (per Visibility rule). User must submit or navigate
   away from the pending session first. Daemon A's virtual row stays
   intact; the existing draft in the composer is preserved.
3. **User taps `+`, types a prompt, the turn fails (network / 5xx)**:
   `pendingSession` stays set, the row stays visible, the composer retains
   the draft (existing `ChatWorkspace` already does this). User can retry.
4. **User taps `+`, navigates to another session before sending**:
   `selected` changes, but `pendingSession` stays set. The virtual row
   persists in the tree. User can tap it to return.
5. **User taps `+` on an `ok` daemon, daemon goes `offline` before
   submit**: `+` button on that daemon disappears (status changed). The
   virtual row stays visible AND the chat header carries a banner
   `daemon offline — 无法提交,等待 daemon 上线或选择其它会话`. The
   composer is **forced disabled** while the daemon owning the pending
   session is non-`ok` — same shape as `ChatWorkspace`'s existing `empty`
   path. Implementation: `CommanderApp` computes
   `pendingDaemonOffline = pendingSession != null && tree.daemons.find(d => d.daemon_id === pendingSession.daemonID)?.status !== 'ok'`
   and passes `composerLocked={pendingDaemonOffline}` plus a
   `composerNote` string to `ChatWorkspace`. (New optional props on
   `ChatWorkspace` — backward-compatible.) When the daemon flips back
   to `ok`, both unset; composer becomes usable again, user can submit
   the existing draft.
6. **Tree refresh returns a real row with the pending UUID** (extremely
   rare race): `loadTree` effect clears `pendingSession` to avoid double
   rendering.
7. **Backend assigns a different ID** (codex backend may regenerate?):
   Per `internal/commander/handler.go:96-101`, the backend uses the
   client-supplied `sess.ID` verbatim (`if sess.ID == "" { sess.ID = id }`).
   So the UUID we send is the UUID the backend stores. No remapping needed.

## Files

| File | Change |
|---|---|
| `src/CommanderApp.tsx` | `pendingSession` state + `createPendingSession` helper + `pendingDaemonOffline` derive + `composerLocked`/`composerNote` plumbing + `sendPrompt` post-done refresh + `loadTree`-clears-pending effect |
| `src/components/DaemonSessionTree.tsx` | `pendingSession` + `onCreateSession` props; `+` button (with disabled-when-other-pending rule); virtual pending row at top of owning daemon's list |
| `src/components/ChatWorkspace.tsx` | `composerLocked?` + `composerNote?` optional props; disabled predicate updated; `.composer-note` element |
| `src/components/MobileShell.tsx` | thread `pendingSession`/`onCreateSession`/`composerLocked`/`composerNote` through; `handleCreate` wrapper that closes Sessions drawer |
| `src/styles.css` | `.daemon-new-session-btn` (desktop 32px + mobile 44px); `.composer-note` (single line, muted) |
| `src/e2e/commander.spec.ts` | 2 new tests (desktop + non-desktop); extend test 7 to include `.daemon-new-session-btn` |
| `src/components/DaemonSessionTree.test.tsx` | new cases per Test Strategy |
| `src/components/ChatWorkspace.test.tsx` | new cases per Test Strategy |
| `src/CommanderApp.mobile.test.tsx` | new cases per Test Strategy |

## Test Strategy

### Vitest unit

- `DaemonSessionTree.test.tsx` — add cases:
  - `+` button renders on `ok` daemon when `onCreateSession` provided.
  - `+` button hidden on non-`ok` daemon (status text shown instead).
  - `+` button on **other** daemons is `disabled` when `pendingSession`
    is set elsewhere; clicking the disabled button does not call
    `onCreateSession`.
  - `pendingSession` matching a daemon inserts a virtual row at top of
    that daemon's session list.
  - clicking the virtual row calls `onSelect(daemonID, pendingSession.sessionID)`.
  - clicking the `+` calls `onCreateSession(daemonID)`.

- `ChatWorkspace.test.tsx` — add cases:
  - `composerLocked=true` forces textarea + send button `disabled`
    independent of `turnState`.
  - `composerNote="..."` renders a `.composer-note` element above the
    composer with that text; omitted means no `.composer-note` in DOM.

- `CommanderApp.mobile.test.tsx` — add cases:
  - mobile shell + open Sessions drawer + click `+` → `selected` updates
    to a new UUID, `pendingSession` set, drawer closes, `ChatWorkspace`
    renders empty messages + active composer (no detail fetch issued).
  - re-clicking the same daemon's `+` while pending exists does NOT mint
    a fresh UUID (selectedRef stays equal).
  - daemon-status change to non-`ok` while pending → composer becomes
    `disabled` and `.composer-note` appears with the offline text.

### Playwright e2e (`commander.spec.ts`)

Two new tests:

1. **`desktop: create new session and send first prompt`** (chromium-desktop only)
   - Mock tree returns one daemon with one existing session.
   - Click `+` on the daemon row.
   - Assert chat header shows "新建会话" or similar; composer enabled.
   - Type prompt + submit → mock the `/turn` endpoint to return a done SSE.
   - Assert tree refresh triggered (route called twice) and chat header
     transitions to the new session's real title from the second
     tree-fetch mock response.

2. **`non-desktop: create new session via Sessions drawer, send prompt`** (chromium-mobile + tablet-portrait)
   - Open Sessions drawer.
   - Click `+` on daemon row inside drawer.
   - Assert drawer closes, chat workspace visible with empty list +
     active composer.
   - Submit prompt → same assertions as desktop test.

### Hit area

The existing test 7 (`drawer interactive controls meet 44px hit area`)
already iterates `.session-row` / `.session-toggle` / `.file-row` /
`.file-copy-button` — extend it to also include `.daemon-new-session-btn`
inside the Sessions drawer.

## Acceptance Criteria

| Requirement | Implementation | Coverage |
|---|---|---|
| Per-daemon `+` button (status-conditioned) | `DaemonSessionTree.tsx` | unit + visual e2e |
| Click `+` creates pending session + selects it + opens composer | `CommanderApp.createPendingSession` | unit + e2e |
| Empty `ChatWorkspace` rendered for pending session, no 404 fetch | `CommanderApp` detail-load short-circuit | unit + e2e |
| First turn POST creates real session server-side, tree refresh swaps placeholder (placeholder NOT cleared before tree confirms the row) | `sendPrompt` post-done branch + `loadTree` resolution clear | e2e flow test |
| Second `+` while pending exists is blocked (no draft loss) | DaemonSessionTree `disabled` rule + CommanderApp.createPendingSession guard | unit |
| Pending-daemon-goes-offline locks composer + shows note | CommanderApp computes `pendingDaemonOffline`, passes `composerLocked` + `composerNote` to ChatWorkspace | unit |
| Mobile: tap `+` in drawer closes drawer, reveals chat | `MobileShell.handleCreate` wrapper | mobile e2e |
| All interactive controls on `< 1024px` retain ≥44×44 hit areas | CSS + extended test 7 | hit-area e2e |

## Risks

- **Pending state survives mid-session refresh** if `loadTree` polling is
  added later: the §loadTree effect already clears `pendingSession` when
  the real row appears, so this is forward-safe.
- **Backend regenerates the session ID** (theoretical, contradicted by
  current code): would orphan the placeholder. If observed in testing,
  treat as a bug in the backend, not the frontend.
- **Two browser tabs racing** on the same daemon: both push the same
  intent, two different UUIDs are submitted, two real sessions appear.
  Acceptable; matches how concurrent codex CLI invocations work today.

## Out-of-scope follow-ups

- Per-session working_dir picker (requires protocol change to add
  `WorkingDir` to `SessionTurnArgs` and slave handler honoring it).
- Per-session title rename (requires backend `rename_session` command).
- Pending-session "X" / cancel button.
- Multi-pending support (one per daemon vs one global).
