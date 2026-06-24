# Commander "New Session" Button Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-daemon `+` button in commander that creates a new chat session, drops the user into the composer, and gracefully handles the round-trip from local placeholder to real backend row.

**Architecture:** Zero protocol changes. Client mints a UUID v4, renders a virtual "pending" tree row + opens the composer for that session, and POSTs the first turn to `/api/commander/daemons/<d>/sessions/<uuid>/turn`. The slave's commander handler creates the backend session implicitly (`internal/commander/handler.go:105`). Pending state moves through `'draft'` → `'submitting'`, then is cleared by the next `loadTree()` that confirms the real row. A discard `×` button releases the draft lock without submitting.

**Tech Stack:** React 19, TypeScript 6, Vite 8, Vitest 4, Playwright 1.61, `@radix-ui/react-dialog` (already a dep), lucide-react icons. Plain CSS in `internal/commanderhub/webapp/src/styles.css`. All work frontend only — no Go changes.

## Global Constraints

The following come from the spec and bind every task:

- Single pending session across the whole app, in EITHER phase. Other-daemon `+` buttons are disabled with phase-appropriate `title` until `loadTree` confirms the real row and clears `pendingSession`.
- Pending phases:
  - `'draft'` — no turn submitted; skip detail fetch (would 404); composer locked if owning daemon is non-`ok`; virtual row shows `×` discard button.
  - `'submitting'` — first turn done; detail fetch is live (real backend transcript); virtual row stays, no `×` (session exists on server).
- `+` button visible only when `daemon.status === 'ok'`.
- Empty-state placeholder copy (verbatim, em dash, not hyphen, single quotes around "Sessions" are NOT in the copy): `新建会话` and `新建会话(待提交)` / `新建会话(同步中…)`.
- Composer offline note copy (verbatim): `daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话`.
- Disabled-button titles: `先发送或丢弃当前草稿` (draft) / `等待新会话出现在列表中` (submitting).
- Discard button aria-label: `丢弃草稿`.
- `+` button aria-label: `` `新建 session: ${daemon.display_name || daemon.daemon_id}` ``.
- All interactive controls on `< 1024px` retain ≥44×44 hit areas.
- TDD: write the failing test, run to confirm it fails, then implement.
- Same workdir as the rest of the worktree: `/root/multi-agent/.claude/worktrees/issue-30-commander-mobile/multi-agent`. All `npm` commands run from `internal/commanderhub/webapp/`.

---

## File Structure

**Modified files:**

```
internal/commanderhub/webapp/src/
  CommanderApp.tsx              # pendingSession state + helpers + plumbing
  CommanderApp.mobile.test.tsx  # +3 cases
  components/
    ChatWorkspace.tsx           # composerLocked? + composerNote? props
    ChatWorkspace.test.tsx      # +2 cases
    DaemonSessionTree.tsx       # +/X buttons + virtual row + props
    DaemonSessionTree.test.tsx  # +7 cases
    MobileShell.tsx             # thread new props through
  styles.css                    # 3 new selectors + mobile .daemon-row min-height bump
  e2e/
    commander.spec.ts           # +2 e2e tests, extend test 7
```

**No new files.** All changes are additive props on existing components; behavior changes go to the host component (`CommanderApp.tsx`).

---

## Task Ordering

Tasks ordered so each step is green-bar before the next:

1. `ChatWorkspace` props (`composerLocked` + `composerNote`) — leaf change, additive, no consumers yet
2. `DaemonSessionTree` props (`pendingSession` + `onCreateSession` + `onDiscardSession`) — leaf change, additive
3. CSS: `.daemon-new-session-btn`, `.session-discard-btn`, `.composer-note`
4. `CommanderApp` state + helpers + plumbing — wires the whole thing
5. `MobileShell` forwarding — threads the 5 new props
6. E2E suite + extended test 7

---

### Task 1: `ChatWorkspace` `composerLocked` + `composerNote` props

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`
- Modify: `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx`

**Interfaces:**
- Consumes: nothing new.
- Produces: extended `ChatWorkspace` props:
  ```ts
  composerLocked?: boolean;  // when true, disables textarea + send button
  composerNote?: string;     // when set, renders above composer in .composer-note
  ```
  Disabled predicate becomes: `empty === true || composerLocked === true || ['queued', 'answering', 'awaiting_approval'].includes(turnState)`. Both props default `undefined` and are backward-compatible.

- [ ] **Step 1: Add failing tests**

Append the following to `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx` (keep all existing tests). `screen`, `vi` should already be imported from earlier tests.

```tsx
test('composerLocked=true forces textarea + send button disabled regardless of turnState', () => {
  render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={null}
      turnState="idle"
      onSend={vi.fn()}
      composerLocked
    />,
  );
  expect(screen.getByLabelText('输入提示词')).toBeDisabled();
  expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
});

test('composerNote="..." renders .composer-note above composer; omitted means no .composer-note', () => {
  const { rerender, container } = render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={null}
      turnState="idle"
      onSend={vi.fn()}
      composerNote="daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话"
    />,
  );
  expect(screen.getByText('daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话')).toBeInTheDocument();
  expect(container.querySelector('.composer-note')).not.toBeNull();

  rerender(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={null}
      turnState="idle"
      onSend={vi.fn()}
    />,
  );
  expect(container.querySelector('.composer-note')).toBeNull();
});
```

- [ ] **Step 2: Run tests to verify failure**

Run from `internal/commanderhub/webapp/`:
```
npm test -- src/components/ChatWorkspace.test.tsx
```
Expected: FAIL on both new tests — `composerLocked` does nothing (composer enabled), `composerNote` element not in DOM.

- [ ] **Step 3: Update `ChatWorkspace.tsx`**

Edit `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`. Add the two props to the destructured signature and the type literal, update the disabled predicate, and render the optional `.composer-note` div between the message list and the composer form. Apply ONLY these edits, leave everything else untouched.

Replace the function signature block:

```tsx
export function ChatWorkspace({
  session,
  turnState,
  onSend,
  mobileLeading,
  mobileTrailing,
  empty,
  composerLocked,
  composerNote,
}: {
  daemonID: string;
  sessionID: string;
  session: SessionDetail | null;
  turnState: TurnState | string;
  onSend: (prompt: string) => Promise<void>;
  mobileLeading?: ReactNode;
  mobileTrailing?: ReactNode;
  empty?: boolean;
  composerLocked?: boolean;
  composerNote?: string;
}) {
```

Replace the disabled computation line:

```tsx
  const disabled =
    empty === true ||
    composerLocked === true ||
    ['queued', 'answering', 'awaiting_approval'].includes(turnState);
```

Insert the note element immediately before the `<form className="composer"...>` line, inside the `<main>`:

```tsx
      {composerNote ? <p className="composer-note">{composerNote}</p> : null}
```

- [ ] **Step 4: Run tests to verify pass**

Run from `internal/commanderhub/webapp/`:
```
npm test -- src/components/ChatWorkspace.test.tsx
```
Expected: PASS for all tests (existing + 2 new).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/components/ChatWorkspace.tsx \
        internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx
git commit -m "feat(commander): add composerLocked + composerNote props to ChatWorkspace

Both optional + backward-compatible. composerLocked forces textarea +
send disabled regardless of turnState (lifts the existing 'empty'
shape). composerNote renders a single <p class='composer-note'>
above the composer when set. Used by CommanderApp to lock + explain
when the daemon owning a pending session is offline.

Refs: #30 follow-up"
```

---

### Task 2: `DaemonSessionTree` `+` button, virtual pending row, `×` discard

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`
- Modify: `internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx`

**Interfaces:**
- Consumes: `lucide-react` (already a dep) for `Plus` and `X` icons. `SessionRow` from `../api/types` (already imported in DaemonSessionTree).
- Produces: extended `DaemonSessionTree` props:
  ```ts
  pendingSession?: {
    daemonID: string;
    sessionID: string;
    phase: 'draft' | 'submitting';
  } | null;
  onCreateSession?: (daemonID: string) => void;
  onDiscardSession?: (sessionID: string) => void;
  ```
  Rules (per spec):
  - `+` button rendered when `daemon.status === 'ok' && onCreateSession` provided. Aria-label: `` `新建 session: ${daemon.display_name || daemon.daemon_id}` ``.
  - `+` disabled when `pendingSession != null && pendingSession.daemonID !== daemon.daemon_id`. Title: `先发送或丢弃当前草稿` for draft phase, `等待新会话出现在列表中` for submitting.
  - Virtual row prepended to owning daemon's session list when `pendingSession?.daemonID === daemon.daemon_id` AND no real session with that ID exists. Title: `新建会话(待提交)` for draft, `新建会话(同步中…)` for submitting. `origin: 'user'`, `turn_state: 'idle'`, `active_worker: false`, `awaiting_approval: false`, empty preview.
  - `×` discard button on the virtual row (draft phase only). Aria-label: `丢弃草稿`. `event.stopPropagation()` on click so it doesn't also fire the row's `onSelect`.

- [ ] **Step 1: Read the existing test file**

Open `internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx`. Note the existing pattern: each test builds an inline `DaemonTree[]` array, renders the component with `onSelect`, asserts via `screen.getByRole/getByText`. Reuse that pattern for all new tests below.

- [ ] **Step 2: Add failing tests**

Append the following at the bottom of `internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx`. Keep all existing tests intact.

```tsx
const oneOkDaemon: DaemonTree[] = [
  {
    daemon_id: 'd1',
    display_name: 'prod-codex',
    kind: 'codex',
    status: 'ok',
    sessions: [
      {
        daemon_id: 'd1',
        session_id: 's1',
        kind: 'codex',
        title: 'Real session',
        origin: 'user',
        turn_state: 'idle',
        active_worker: false,
        awaiting_approval: false,
      },
    ],
  },
];

const twoOkDaemons: DaemonTree[] = [
  ...oneOkDaemon,
  {
    daemon_id: 'd2',
    display_name: 'other',
    kind: 'codex',
    status: 'ok',
    sessions: [],
  },
];

test('+ button rendered on ok daemon when onCreateSession provided', () => {
  const onCreateSession = vi.fn();
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={onCreateSession}
    />,
  );
  const btn = screen.getByRole('button', { name: /新建 session: prod-codex/ });
  expect(btn).toBeEnabled();
  fireEvent.click(btn);
  expect(onCreateSession).toHaveBeenCalledWith('d1');
});

test('+ button hidden on non-ok daemon (status text shown instead)', () => {
  const offline: DaemonTree[] = [
    { ...oneOkDaemon[0], status: 'offline' },
  ];
  render(
    <DaemonSessionTree
      daemons={offline}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={vi.fn()}
    />,
  );
  expect(screen.queryByRole('button', { name: /新建 session/ })).not.toBeInTheDocument();
  expect(screen.getByText('offline')).toBeInTheDocument();
});

test('+ button on other daemon disabled when draft pending exists; title says 先发送或丢弃当前草稿', () => {
  const onCreateSession = vi.fn();
  render(
    <DaemonSessionTree
      daemons={twoOkDaemons}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={onCreateSession}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'draft' }}
    />,
  );
  const otherBtn = screen.getByRole('button', { name: /新建 session: other/ });
  expect(otherBtn).toBeDisabled();
  expect(otherBtn).toHaveAttribute('title', '先发送或丢弃当前草稿');
  fireEvent.click(otherBtn);
  expect(onCreateSession).not.toHaveBeenCalled();
});

test('+ button on other daemon disabled when submitting pending exists; title says 等待新会话出现在列表中', () => {
  render(
    <DaemonSessionTree
      daemons={twoOkDaemons}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={vi.fn()}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'submitting' }}
    />,
  );
  const otherBtn = screen.getByRole('button', { name: /新建 session: other/ });
  expect(otherBtn).toBeDisabled();
  expect(otherBtn).toHaveAttribute('title', '等待新会话出现在列表中');
});

test('pendingSession matching a daemon inserts a virtual row at top with draft title', () => {
  const onSelect = vi.fn();
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={onSelect}
      onCreateSession={vi.fn()}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'draft' }}
    />,
  );
  // Virtual row is first
  const buttons = screen.getAllByRole('button', { name: /会话/ });
  // The first .session-row button should be the pending virtual row.
  expect(within(buttons[0]).getByText('新建会话(待提交)')).toBeInTheDocument();
  fireEvent.click(buttons[0]);
  expect(onSelect).toHaveBeenCalledWith('d1', 'pending-uuid');
});

test('submitting phase virtual row uses 新建会话(同步中…) and no × button', () => {
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={vi.fn()}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'submitting' }}
    />,
  );
  expect(screen.getByText('新建会话(同步中…)')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '丢弃草稿' })).not.toBeInTheDocument();
});

test('× discard button on draft virtual row calls onDiscardSession and does NOT call onSelect', () => {
  const onSelect = vi.fn();
  const onDiscardSession = vi.fn();
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={onSelect}
      onCreateSession={vi.fn()}
      onDiscardSession={onDiscardSession}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'draft' }}
    />,
  );
  fireEvent.click(screen.getByRole('button', { name: '丢弃草稿' }));
  expect(onDiscardSession).toHaveBeenCalledWith('pending-uuid');
  expect(onSelect).not.toHaveBeenCalled();
});
```

If `within` is not already imported at the top of the file, add it: change `import { cleanup, fireEvent, render, screen } from '@testing-library/react';` to `import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';`.

- [ ] **Step 3: Run tests to verify failure**

Run from `internal/commanderhub/webapp/`:
```
npm test -- src/components/DaemonSessionTree.test.tsx
```
Expected: FAIL on all 7 new tests — the props don't exist, no `+` / `×` buttons, no virtual row.

- [ ] **Step 4: Update `DaemonSessionTree.tsx`**

Edit `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`. Apply these edits:

1) Add the new icons to the lucide-react import (find the existing import; if there's no lucide-react import, add the next line at the top with the other imports):

```tsx
import { Plus, X } from 'lucide-react';
```

2) Add the three new props to the function signature. Find the existing destructured props block (around the `export function DaemonSessionTree({...})` line) and extend it:

```tsx
export function DaemonSessionTree({
  daemons,
  selected,
  onSelect,
  pendingSession,
  onCreateSession,
  onDiscardSession,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
  pendingSession?: {
    daemonID: string;
    sessionID: string;
    phase: 'draft' | 'submitting';
  } | null;
  onCreateSession?: (daemonID: string) => void;
  onDiscardSession?: (sessionID: string) => void;
}) {
```

3) Inside the daemon-row JSX (find `<div className={\`daemon-row daemon-${daemon.status}\`}>`), replace the existing `<span className="daemon-status">{daemon.status}</span>` line with:

```tsx
          {daemon.status === 'ok' && onCreateSession ? (() => {
            const otherDaemonPending = pendingSession != null && pendingSession.daemonID !== daemon.daemon_id;
            const disabledTitle = pendingSession?.phase === 'submitting'
              ? '等待新会话出现在列表中'
              : '先发送或丢弃当前草稿';
            return (
              <button
                type="button"
                className="daemon-new-session-btn"
                aria-label={`新建 session: ${daemon.display_name || daemon.daemon_id}`}
                disabled={otherDaemonPending}
                title={otherDaemonPending ? disabledTitle : undefined}
                onClick={() => onCreateSession(daemon.daemon_id)}
              >
                <Plus size={16} />
              </button>
            );
          })() : (
            <span className="daemon-status">{daemon.status}</span>
          )}
```

4) Insert the virtual pending row at the top of the owning daemon's session list. Find the JSX block that maps `daemon.sessions` (or wherever the per-daemon session rows are rendered — typically inside the daemon section JSX). Locate the line that renders sessions for the daemon. Right before mapping the real sessions, render the virtual row when this daemon owns the pending session AND no real session with that UUID exists. The exact insertion depends on the existing structure; add a `pendingRow` helper inline:

Add the following helper near the top of the component body (before the `return`):

```tsx
  function isPendingRowVisible(daemonID: string): boolean {
    if (!pendingSession || pendingSession.daemonID !== daemonID) return false;
    const daemon = daemons.find((d) => d.daemon_id === daemonID);
    const sessions = daemon?.sessions ?? [];
    return !sessions.some((s) => s.session_id === pendingSession.sessionID);
  }
```

Then inside the per-daemon JSX block (right before the existing `(daemon.sessions ?? []).map(...)` or equivalent — find the spot that renders sessions for a single daemon), prepend:

```tsx
              {isPendingRowVisible(daemon.daemon_id) && pendingSession ? (
                <div className="session-row-line session-row-line-pending" data-testid="pending-session-row">
                  <span className="session-toggle-spacer" />
                  <button
                    type="button"
                    className={`session-row${selected?.sessionID === pendingSession.sessionID ? ' selected' : ''}`}
                    onClick={() => onSelect(daemon.daemon_id, pendingSession.sessionID)}
                  >
                    <span className="session-title">
                      {pendingSession.phase === 'submitting' ? '新建会话(同步中…)' : '新建会话(待提交)'}
                    </span>
                    <span className="session-meta">{daemon.display_name || daemon.daemon_id}</span>
                  </button>
                  {pendingSession.phase === 'draft' && onDiscardSession ? (
                    <button
                      type="button"
                      className="session-discard-btn"
                      aria-label="丢弃草稿"
                      onClick={(event) => {
                        event.stopPropagation();
                        onDiscardSession(pendingSession.sessionID);
                      }}
                    >
                      <X size={14} />
                    </button>
                  ) : null}
                </div>
              ) : null}
```

(If the existing session list uses a different wrapper structure than `.session-row-line`, follow the existing pattern; the key invariants are: virtual row appears first, has `.session-row` class, selection highlight on UUID match, and the `×` button uses `event.stopPropagation()`.)

- [ ] **Step 5: Run tests to verify pass**

Run from `internal/commanderhub/webapp/`:
```
npm test -- src/components/DaemonSessionTree.test.tsx
```
Expected: PASS on all 7 new tests + all existing tests.

- [ ] **Step 6: Run the full suite once to catch regressions**

```
npm test
```
Expected: full suite green (no regressions in MobileShell / FileExplorerPanel / etc.).

- [ ] **Step 7: Commit**

```bash
git add internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx \
        internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx
git commit -m "feat(commander): add + / × buttons + pending virtual row to DaemonSessionTree

Per spec: + button visible on ok daemons; disabled (with phase-aware
title) when another daemon owns the current pending session; virtual
row at top of owning daemon's session list with phase-aware title
(draft: 新建会话(待提交), submitting: 新建会话(同步中…)); × discard
button on draft rows uses event.stopPropagation so clicking it
doesn't also fire onSelect.

Refs: #30 follow-up"
```

---

### Task 3: CSS for `+`, `×`, and `.composer-note`

**Files:**
- Modify: `internal/commanderhub/webapp/src/styles.css`

**Interfaces:**
- Consumes: nothing.
- Produces: three new selectors used by Task 1 and Task 2 components.

- [ ] **Step 1: Append the new rules to `styles.css`**

Append to the end of `internal/commanderhub/webapp/src/styles.css`:

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
  cursor: pointer;
}
.daemon-new-session-btn:hover { background: #f4f7fb; }
.daemon-new-session-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
  background: #fff;
}

/* Pending virtual row has 3 columns (toggle-spacer, row, × discard) instead
   of the 2-column .session-row-line default. Override grid template here. */
.session-row-line-pending {
  grid-template-columns: 24px minmax(0, 1fr) 28px;
  align-items: center;
}

.session-discard-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: 1px solid #d9e1ec;
  border-radius: 6px;
  background: #fff;
  color: #69768a;
  cursor: pointer;
}
.session-discard-btn:hover { background: #f4f7fb; color: #a33b3b; }

.composer-note {
  margin: 0;
  padding: 8px 18px;
  border-top: 1px solid #d9e1ec;
  background: #fff8e6;
  color: #8d5b12;
  font-size: 12px;
}

@media (max-width: 1023px) {
  /* daemon-row default height is 32px (set in styles.css line ~116). A 44px
     + button would overflow and visually collide with the first session
     row. Raise the row's min-height so the button sits inside the row. */
  .daemon-row { min-height: 44px; height: auto; }
  .daemon-new-session-btn {
    width: 44px;
    height: 44px;
    min-width: 44px;
    min-height: 44px;
  }
  .session-discard-btn {
    width: 44px;
    height: 44px;
    min-width: 44px;
    min-height: 44px;
  }
  /* Widen the pending row's × column to match the 44px discard button. */
  .session-row-line-pending {
    grid-template-columns: 44px minmax(0, 1fr) 44px;
  }
}
```

- [ ] **Step 2: Verify the build still produces clean CSS**

Run from `internal/commanderhub/webapp/`:
```
npm run build
```
Expected: succeeds; no PostCSS warnings.

- [ ] **Step 3: Commit**

```bash
git add internal/commanderhub/webapp/src/styles.css
git commit -m "style(commander): + / × buttons (desktop 32/28, mobile 44) + .composer-note

New selectors back the per-daemon + button, the draft-row × discard
button, and the offline-daemon composer note. Mobile breakpoint
(< 1024px) bumps both buttons to 44x44 to satisfy the existing hit-
area rule.

Refs: #30 follow-up"
```

---

### Task 4: `CommanderApp` — pendingSession state, helpers, phase transitions

**Files:**
- Modify: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- Modify: `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx`

**Interfaces:**
- Consumes: Task 1's `ChatWorkspace` props (`composerLocked`, `composerNote`), Task 2's `DaemonSessionTree` props (`pendingSession`, `onCreateSession`, `onDiscardSession`).
- Produces: `CommanderApp` that owns the single `pendingSession` slot and wires it into both desktop and mobile shells. Helper signatures consumed by Task 5:
  ```ts
  type PendingSession = { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' };
  // available inside CommanderApp scope:
  const [pendingSession, setPendingSession] = useState<PendingSession | null>(null);
  function createPendingSession(daemonID: string): void;
  function discardPendingSession(): void;
  ```
  The mobile shell receives `pendingSession`, `createPendingSession`, `discardPendingSession`, `composerLocked`, `composerNote` as new props.

Behavior (all per spec):
- `createPendingSession(d)`:
  1. If `pendingSessionRef.current != null && pendingSessionRef.current.daemonID !== d`, no-op.
  2. If `pendingSessionRef.current != null && pendingSessionRef.current.daemonID === d`, re-select existing (no fresh UUID).
  3. Else: mint `crypto.randomUUID()`, set `{daemonID: d, sessionID: sid, phase: 'draft'}`, then `selectSession(d, sid)`.
- `discardPendingSession()`: clear `pendingSession`; if `selectedRef.current?.sessionID === discardedSid`, also clear `selected`.
- Detail-fetch effect: when `selected?.sessionID === pendingSession?.sessionID && pendingSession.phase === 'draft'`, skip `apiGet(sessionPath(...))` and instead set `sessionDetail = { session: { ID: sid, Title: '新建会话' }, messages: [] }`. Effect deps must include the pending phase so re-fires on flip.
- `sendPrompt` post-`done` branch: if `submitted.sessionID === pendingSessionRef.current?.sessionID && pendingSessionRef.current.phase === 'draft'`, transition phase to `'submitting'` via functional setState, then call `void loadTree()`.
- `loadTree.then`: after `setTree(nextTree)`, if `pendingSessionRef.current != null` and `nextTree.daemons` contains a session with that UUID under the matching daemon, `setPendingSession(null)` and clear the ref.
- Compute `pendingDaemonOffline = pendingSession?.phase === 'draft' && pendingSession != null && (tree?.daemons.find(d => d.daemon_id === pendingSession.daemonID)?.status ?? 'offline') !== 'ok'`. Pass `composerLocked={pendingDaemonOffline}` + `composerNote={pendingDaemonOffline ? 'daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话' : undefined}` to `ChatWorkspace` only when selected matches pending and phase is draft; otherwise pass both undefined.

- [ ] **Step 1: Add failing mobile tests (and the `fireEvent` import they need)**

Open `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx`. Add `fireEvent` to the testing-library import on line 1:

```tsx
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
```

Then append three new tests at the bottom of the file. They reuse the existing `installMatchMedia` and `treeWith` / `stubFetch` helpers (defined at the top of the file). Some tests need to inspect the fetch mock's call history, so build the fetch mock inline instead of going through `stubFetch`:

```tsx
test('mobile: click + on daemon creates pending session in draft phase, drawer closes, composer enabled with empty messages, no detail fetch issued', async () => {
  installMatchMedia(true);
  const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://commander.test');
    if (url.pathname === '/api/commander/tree') {
      return treeWith([{ session_id: 'a', title: 'Session A' }]);
    }
    if (url.pathname.endsWith('/files')) {
      return jsonResponse({ root: '/repo', path: '.', entries: [] });
    }
    if (/\/sessions\/[^/]+$/.test(url.pathname)) {
      return jsonResponse({ session: { ID: 'a', Title: 'Session A' }, messages: [] });
    }
    return jsonResponse({});
  });
  vi.stubGlobal('fetch', fetchFn);
  render(<CommanderApp />);
  await screen.findByText('Session A');
  fireEvent.click(screen.getByRole('button', { name: 'Sessions' }));
  const isDetailURL = (input: unknown) =>
    /\/api\/commander\/daemons\/[^/]+\/sessions\/[^/]+$/.test(String(input));
  const detailsBefore = fetchFn.mock.calls.filter(([url]) => isDetailURL(url)).length;
  fireEvent.click(screen.getByRole('button', { name: /新建 session: prod-codex/ }));
  await waitFor(() => expect(screen.queryByTestId('drawer-left')).not.toBeInTheDocument());
  expect(screen.getByLabelText('输入提示词')).toBeEnabled();
  // The draft path must short-circuit detail fetch — no new detail call.
  const detailsAfter = fetchFn.mock.calls.filter(([url]) => isDetailURL(url)).length;
  expect(detailsAfter).toBe(detailsBefore);
});

test('mobile: re-clicking + on same daemon while draft pending exists does NOT mint a fresh UUID', async () => {
  installMatchMedia(true);
  stubFetch([{ session_id: 'a', title: 'Session A' }]);
  render(<CommanderApp />);
  await screen.findByText('Session A');
  fireEvent.click(screen.getByRole('button', { name: 'Sessions' }));
  fireEvent.click(screen.getByRole('button', { name: /新建 session: prod-codex/ }));
  await waitFor(() => expect(screen.queryByTestId('drawer-left')).not.toBeInTheDocument());
  fireEvent.click(screen.getByRole('button', { name: 'Sessions' }));
  expect(screen.getByText('新建会话(待提交)')).toBeInTheDocument();
  // Re-click + on the same daemon — should NOT add a second virtual row.
  fireEvent.click(screen.getByRole('button', { name: /新建 session: prod-codex/ }));
  await waitFor(() => expect(screen.queryByTestId('drawer-left')).not.toBeInTheDocument());
  fireEvent.click(screen.getByRole('button', { name: 'Sessions' }));
  expect(screen.getAllByText('新建会话(待提交)')).toHaveLength(1);
});

test('mobile: × discard on draft row clears pendingSession + selected', async () => {
  installMatchMedia(true);
  stubFetch([{ session_id: 'a', title: 'Session A' }]);
  render(<CommanderApp />);
  await screen.findByText('Session A');
  fireEvent.click(screen.getByRole('button', { name: 'Sessions' }));
  fireEvent.click(screen.getByRole('button', { name: /新建 session: prod-codex/ }));
  await waitFor(() => expect(screen.queryByTestId('drawer-left')).not.toBeInTheDocument());
  fireEvent.click(screen.getByRole('button', { name: 'Sessions' }));
  expect(screen.getByText('新建会话(待提交)')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '丢弃草稿' }));
  expect(screen.queryByText('新建会话(待提交)')).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run tests to verify failure**

Run from `internal/commanderhub/webapp/`:
```
npm test -- src/CommanderApp.mobile.test.tsx
```
Expected: FAIL on all 3 new tests — no `+` button is rendered yet because `CommanderApp` doesn't pass `onCreateSession` to `MobileShell`.

- [ ] **Step 3: Edit `CommanderApp.tsx` — state, refs, helpers, plumbing**

Open `internal/commanderhub/webapp/src/CommanderApp.tsx`. Apply the edits in this order:

3a) Below the existing `useState` declarations (after `setPreviewPayload` and the other hoisted overlay state), add the pending state + ref:

```tsx
  type PendingSession = { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' };
  const [pendingSession, setPendingSession] = useState<PendingSession | null>(null);
  const pendingSessionRef = useRef<PendingSession | null>(null);
  useLayoutEffect(() => {
    pendingSessionRef.current = pendingSession;
  });
```

3b) Below the `selectSession` function (around line 372 in the current file), add two new helpers:

```tsx
  function createPendingSession(daemonID: string) {
    const current = pendingSessionRef.current;
    if (current != null && current.daemonID !== daemonID) return;
    if (current != null && current.daemonID === daemonID) {
      // Re-select existing; no fresh UUID.
      selectSession(current.daemonID, current.sessionID);
      return;
    }
    const sid = crypto.randomUUID();
    const next: PendingSession = { daemonID, sessionID: sid, phase: 'draft' };
    pendingSessionRef.current = next;
    setPendingSession(next);
    selectSession(daemonID, sid);
  }

  function discardPendingSession() {
    const prev = pendingSessionRef.current;
    pendingSessionRef.current = null;
    setPendingSession(null);
    if (prev != null && selectedRef.current?.sessionID === prev.sessionID) {
      selectedRef.current = null;
      setSelected(null);
    }
  }
```

3c) Replace the existing detail-fetch `useEffect` (around lines 277-302 — the effect that loads `sessionDetail` for `selected`). Replace it with:

```tsx
  useEffect(() => {
    let cancelled = false;
    setSessionDetail(null);

    if (!selected) {
      setTurnState('idle');
      return;
    }

    const row = tree?.daemons
      .find((daemon) => daemon.daemon_id === selected.daemonID)
      ?.sessions?.find((session) => session.session_id === selected.sessionID);
    setTurnState(row?.turn_state || 'idle');

    // Draft pending — backend has no row yet; render an empty placeholder.
    if (
      pendingSession != null
      && pendingSession.sessionID === selected.sessionID
      && pendingSession.phase === 'draft'
    ) {
      setSessionDetail({
        session: { ID: selected.sessionID, Title: '新建会话' },
        messages: [],
      });
      return;
    }

    apiGet<SessionDetail>(sessionPath(selected.daemonID, selected.sessionID))
      .then((detail) => {
        if (!cancelled) setSessionDetail(detail);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      });

    return () => {
      cancelled = true;
    };
  }, [selected, tree, pendingSession]);
```

3d) Modify `sendPrompt` (around line 304). Find the post-`done` branch — the line `const detail = await apiGet<SessionDetail>(sessionPath(submitted.daemonID, submitted.sessionID));` (around line 348). Replace from `if (turnError) throw turnError;` through `if (isCurrentTurn()) setSessionDetail(detail);` with:

```tsx
      if (turnError) throw turnError;
      // pending phase flip + loadTree MUST run independent of isCurrentTurn():
      // the server-side session was created regardless of whether the user has
      // since navigated to a different session. If we gated this on
      // isCurrentTurn(), a quick navigation away would leave the virtual row
      // visible forever and lock other daemons' + buttons.
      const pendingNow = pendingSessionRef.current;
      if (
        pendingNow != null
        && pendingNow.sessionID === submitted.sessionID
        && pendingNow.phase === 'draft'
      ) {
        const flipped: PendingSession = { ...pendingNow, phase: 'submitting' };
        pendingSessionRef.current = flipped;
        setPendingSession(flipped);
        void loadTree();
        // Detail fetch is handled by the [selected, tree, pendingSession]
        // effect when it re-runs on the phase change. We don't issue one here.
        return;
      }
      if (!isCurrentTurn()) return;
      const detail = await apiGet<SessionDetail>(sessionPath(submitted.daemonID, submitted.sessionID));
      if (isCurrentTurn()) setSessionDetail(detail);
```

3e) Update `loadTree` to clear pending when the real row appears. Replace the `.then((nextTree) => {...})` block (around lines 197-204) with:

```tsx
      .then((nextTree) => {
        setTree(nextTree);
        setAuthRequired(false);
        // Path (a): one-shot auto-select right after the tree arrives,
        // before React flushes the state update. Path (b) useEffect above
        // also covers this case for rotation while tree is loaded.
        tryAutoSelectRef.current(nextTree);
        // If a pending session's real row has arrived, clear pending so
        // the virtual row is replaced by the real one.
        const p = pendingSessionRef.current;
        if (p != null) {
          const realRow = nextTree.daemons
            .find((d) => d.daemon_id === p.daemonID)
            ?.sessions?.find((s) => s.session_id === p.sessionID);
          if (realRow) {
            pendingSessionRef.current = null;
            setPendingSession(null);
          }
        }
      })
```

3f) Compute derived state used by BOTH the mobile and desktop branches. Place these `const` declarations between the `if (!tree) return ...` line and the `if (isNonDesktop) {` block:

```tsx
  const selectedIsPendingDraft = pendingSession != null
    && selected?.sessionID === pendingSession.sessionID
    && pendingSession.phase === 'draft';
  const pendingDaemonOffline = pendingSession?.phase === 'draft'
    && (tree?.daemons.find((d) => d.daemon_id === pendingSession.daemonID)?.status ?? 'offline') !== 'ok';
  const composerLocked = selectedIsPendingDraft && pendingDaemonOffline;
  const composerNote = composerLocked
    ? 'daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话'
    : undefined;
  // Suppress FileExplorerPanel fetches when selected is a draft pending
  // session — the backend has no row for it yet, so /files?path=. would
  // 404 and surface a misleading error. Passing an empty sessionID
  // short-circuits the panel's effect (see FileExplorerPanel.tsx — the
  // useEffect bails when !daemonID || !sessionID).
  const fileSessionID = selectedIsPendingDraft ? '' : (selected?.sessionID || '');
  const fileDaemonID = selectedIsPendingDraft ? '' : (selected?.daemonID || '');
```

3g) Update the mobile branch's `<MobileShell>` call to forward the new props. Note `disableFiles={selectedIsPendingDraft}`: when true, MobileShell will pass empty daemonID/sessionID into its inner FileExplorerPanel to suppress the 404 fetch (Task 5 wires this).

```tsx
  if (isNonDesktop) {
    return (
      <MobileShell
        daemons={tree.daemons}
        selected={selected}
        onSelect={selectSession}
        sessionDetail={sessionDetail}
        turnState={turnState}
        onSend={sendPrompt}
        overlay={overlay}
        sessionsOpen={sessionsOpen}
        setSessionsOpen={setSessionsOpen}
        filesOpen={filesOpen}
        setFilesOpen={setFilesOpen}
        previewPayload={previewPayload}
        setPreviewPayload={setPreviewPayload}
        pendingSession={pendingSession}
        onCreateSession={createPendingSession}
        onDiscardSession={discardPendingSession}
        composerLocked={composerLocked}
        composerNote={composerNote}
        disableFiles={selectedIsPendingDraft}
      />
    );
  }
```

3h) Update the desktop render branch (the `return (<div className="commander-shell" ...>...</div>)` at the end of the component):

```tsx
  return (
    <div className="commander-shell" data-testid="commander-shell">
      <DaemonSessionTree
        daemons={tree.daemons}
        selected={selected}
        onSelect={selectSession}
        pendingSession={pendingSession}
        onCreateSession={createPendingSession}
        onDiscardSession={discardPendingSession}
      />
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={sessionDetail}
        turnState={turnState}
        onSend={sendPrompt}
        composerLocked={composerLocked}
        composerNote={composerNote}
      />
      <FileExplorerPanel daemonID={fileDaemonID} sessionID={fileSessionID} />
    </div>
  );
```

- [ ] **Step 4: Run desktop unit tests to confirm no regression (do NOT run `npm run build` yet)**

The 3 new mobile tests in this Task depend on Task 5 (`MobileShell` plumbing) being done before the `+` button is reachable inside the drawer. They will keep failing until Step 1 of Task 5 lands. Similarly, **`npm run build` would fail at this point** because the new props passed to `<MobileShell>` are not yet declared in `MobileShell`'s prop type — TypeScript would reject the JSX. We defer the build to Task 5 Step 4 once `MobileShell` has been updated.

Run only the desktop unit suite:

```
npm test -- src/CommanderApp.test.tsx
```
Expected: PASS (no regressions). Do **not** run `npm run build` or `npm test` (the whole suite) here.

Do **NOT** commit yet. Proceed straight to Task 5 — the commit at the end of Task 5 covers both Task 4's source edits and Task 5's MobileShell forwarding, so the resulting commit is green (no failing tests + build clean).

---

### Task 5: `MobileShell` — thread the five new props through, then commit Tasks 4+5 together

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/MobileShell.tsx`

**Interfaces:**
- Consumes: Task 4's helpers + state (passed in as props). Task 2's `DaemonSessionTree` props.
- Produces: `MobileShell` extended with 6 new optional-but-actively-used props:
  ```ts
  pendingSession?: { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' } | null;
  onCreateSession?: (daemonID: string) => void;
  onDiscardSession?: (sessionID: string) => void;
  composerLocked?: boolean;
  composerNote?: string;
  disableFiles?: boolean;  // when true, pass empty IDs into FileExplorerPanel to suppress fetches
  ```
  The first three flow into `<DaemonSessionTree>` (with `onCreateSession` wrapped to also close the Sessions drawer). `composerLocked` + `composerNote` flow into `<ChatWorkspace>`. `disableFiles` short-circuits the wrapped `<FileExplorerPanel>` by passing empty IDs into it (the panel's effect bails when `!daemonID || !sessionID`).

- [ ] **Step 1: Edit `MobileShell.tsx`**

Open `internal/commanderhub/webapp/src/components/MobileShell.tsx`. Apply these edits:

1a) Extend the destructured props and type literal. Find the existing destructuring (it currently lists `daemons`, `selected`, `onSelect`, `sessionDetail`, `turnState`, `onSend`, `overlay`, `sessionsOpen`, `setSessionsOpen`, `filesOpen`, `setFilesOpen`, `previewPayload`, `setPreviewPayload`). Add the 5 new fields:

```tsx
export function MobileShell({
  daemons,
  selected,
  onSelect,
  sessionDetail,
  turnState,
  onSend,
  overlay,
  sessionsOpen,
  setSessionsOpen,
  filesOpen,
  setFilesOpen,
  previewPayload,
  setPreviewPayload,
  pendingSession,
  onCreateSession,
  onDiscardSession,
  composerLocked,
  composerNote,
  disableFiles,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
  sessionDetail: SessionDetail | null;
  turnState: TurnState;
  onSend: (prompt: string) => Promise<void>;
  overlay: OverlayController;
  sessionsOpen: boolean;
  setSessionsOpen: (open: boolean) => void;
  filesOpen: boolean;
  setFilesOpen: (open: boolean) => void;
  previewPayload: FilePreviewPayload | null;
  setPreviewPayload: (payload: FilePreviewPayload | null) => void;
  pendingSession?: { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' } | null;
  onCreateSession?: (daemonID: string) => void;
  onDiscardSession?: (sessionID: string) => void;
  composerLocked?: boolean;
  composerNote?: string;
  disableFiles?: boolean;
}) {
```

(The existing types for `selected`, `setSessionsOpen`, `setFilesOpen`, `setPreviewPayload` might differ slightly in your local file — leave them as they currently are; only add the six new lines.)

1b) Add a wrapper that closes the Sessions drawer after invoking `onCreateSession`. Add this near the existing `handleSelectSession` helper inside the component body:

```tsx
  function handleCreate(daemonID: string) {
    if (!onCreateSession) return;
    onCreateSession(daemonID);
    closeOverlay('sessions', setSessionsOpen);
  }
```

1c) Find the `<DaemonSessionTree>` invocation inside the Sessions drawer. Pass the new props (use `handleCreate` instead of `onCreateSession` directly so the drawer closes):

```tsx
        <DaemonSessionTree
          daemons={daemons}
          selected={selected}
          onSelect={handleSelectSession}
          pendingSession={pendingSession}
          onCreateSession={onCreateSession ? handleCreate : undefined}
          onDiscardSession={onDiscardSession}
        />
```

1d) Find the `<ChatWorkspace>` invocation in the main render. Pass `composerLocked` and `composerNote`:

```tsx
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={sessionDetail}
        turnState={turnState}
        onSend={onSend}
        mobileLeading={sessionsBtn}
        mobileTrailing={filesBtn}
        empty={selected == null}
        composerLocked={composerLocked}
        composerNote={composerNote}
      />
```

(Leave the other props — `mobileLeading`, `mobileTrailing`, `empty` — as they are; just add the two new lines.)

1e) Find the `<FileExplorerPanel>` invocation in the Files drawer. Apply the `disableFiles` suppression by passing empty IDs when set, to short-circuit the panel's eager `/files?path=.` fetch:

```tsx
        <FileExplorerPanel
          daemonID={disableFiles ? '' : (selected?.daemonID || '')}
          sessionID={disableFiles ? '' : (selected?.sessionID || '')}
          renderMode="sheet"
          onPreview={(payload) => { setPreviewPayload(payload); overlay.open('preview'); }}
        />
```

(Leave any other props on `<FileExplorerPanel>` as they are; only the `daemonID` and `sessionID` lines change to use the `disableFiles` ternary.)

- [ ] **Step 2: Run mobile tests to verify pass**

Run from `internal/commanderhub/webapp/`:
```
npm test -- src/CommanderApp.mobile.test.tsx
```
Expected: PASS, including the 3 new tests from Task 4.

- [ ] **Step 3: Run full suite to confirm no regressions**

```
npm test
```
Expected: full suite green.

- [ ] **Step 4: Build to confirm TypeScript is happy**

```
npm run build
```
Expected: tsc + vite build succeed.

- [ ] **Step 5: Commit Tasks 4 + 5 together**

```bash
git add internal/commanderhub/webapp/src/CommanderApp.tsx \
        internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx \
        internal/commanderhub/webapp/src/components/MobileShell.tsx
git commit -m "feat(commander): hoist pendingSession + forward through MobileShell

CommanderApp owns a single pendingSession slot in 'draft' |
'submitting' phase. createPendingSession (with idempotent same-daemon
re-select), discardPendingSession, draft-aware detail-fetch
short-circuit, sendPrompt post-done phase flip (NOT gated by
isCurrentTurn — server-side session was created regardless of where
the user navigated), loadTree-clears-pending-on-real-row, and
composerLocked/composerNote computed for the daemon-offline case.
MobileShell threads pending / onCreateSession / onDiscardSession /
composerLocked / composerNote into its wrapped DaemonSessionTree +
ChatWorkspace. handleCreate wrapper closes the Sessions drawer after
+ (mirrors handleSelectSession).

Tasks 4 and 5 commit together so the working tree stays green — the
3 new mobile tests pass only once MobileShell forwards the props.

Refs: #30 follow-up"
```

---

### Task 6: E2E tests + extend test 7 hit-area coverage

**Files:**
- Modify: `internal/commanderhub/webapp/src/e2e/commander.spec.ts`

**Interfaces:**
- Consumes: existing `idleTreePayload`, `mockIdleTree(page)`, `assertHitArea(locator, name)` helpers at the top of the file.
- Produces: 2 new e2e tests + an extension to the existing hit-area test.

- [ ] **Step 1: Add the desktop test for new session flow**

Open `internal/commanderhub/webapp/src/e2e/commander.spec.ts`. Find the existing `desktop:` test block. After the last desktop test (or in any sensible location among the existing desktop tests), append:

```ts
test('desktop: + button creates pending row, turn POSTs, tree refresh swaps placeholder with real row', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-desktop') test.skip();
  let turnRequestCount = 0;
  let treeRequestCount = 0;
  let createdSessionUUID = '';
  // Tree-route surfaces the new session ONLY after a turn was POSTed.
  await page.route('**/api/commander/tree', async (route) => {
    treeRequestCount += 1;
    const base = idleTreePayload.daemons[0];
    const sessions = [...base.sessions];
    if (turnRequestCount > 0 && createdSessionUUID) {
      sessions.unshift({
        ...base.sessions[0],
        session_id: createdSessionUUID,
        title: 'Real title from backend',
        turn_state: 'idle',
      });
    }
    await route.fulfill({ json: { daemons: [{ ...base, sessions }] } });
  });
  await page.route('**/api/commander/daemons/d1/sessions/*/turn', async (route, request) => {
    turnRequestCount += 1;
    const match = String(request.url()).match(/\/sessions\/([^/]+)\/turn$/);
    if (match) createdSessionUUID = decodeURIComponent(match[1]);
    await route.fulfill({
      body: 'event: done\ndata: {"result":{}}\n\n',
      headers: { 'content-type': 'text/event-stream' },
    });
  });
  await page.route('**/api/commander/daemons/d1/sessions/*', async (route, request) => {
    const match = String(request.url()).match(/\/sessions\/([^/]+)$/);
    const sid = match ? decodeURIComponent(match[1]) : 'unknown';
    await route.fulfill({
      json: { session: { ID: sid, Title: 'Real title from backend' }, messages: [] },
    });
  });
  await page.goto('/commander/');
  await expect.poll(() => treeRequestCount).toBeGreaterThanOrEqual(1);
  const treesBeforeClick = treeRequestCount;
  // Click + on the daemon row
  await page.getByRole('button', { name: /新建 session: prod/ }).click();
  // Placeholder visible: virtual row in tree + draft title in chat header + composer enabled
  await expect(page.locator('.daemon-tree').getByText('新建会话(待提交)')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: '新建会话' })).toBeVisible();
  await expect(page.getByLabel('输入提示词')).toBeEnabled();
  // Submit a prompt — POST the first turn
  await page.getByLabel('输入提示词').fill('hi');
  await page.getByRole('button', { name: '发送' }).click();
  await expect.poll(() => turnRequestCount).toBeGreaterThanOrEqual(1);
  // A second tree-fetch MUST happen after the turn (loadTree post-done).
  await expect.poll(() => treeRequestCount).toBeGreaterThanOrEqual(treesBeforeClick + 1);
  // Virtual row disappears (pendingSession cleared by loadTree-saw-real-row)
  await expect(page.locator('.daemon-tree').getByText('新建会话(待提交)')).toHaveCount(0);
  await expect(page.locator('.daemon-tree').getByText('新建会话(同步中…)')).toHaveCount(0);
  // Real row from backend appears in the tree
  await expect(page.locator('.daemon-tree').getByText('Real title from backend')).toBeVisible();
  // Chat header transitions to the real backend title (no longer shows draft placeholder)
  await expect(page.getByRole('heading', { level: 1, name: '新建会话' })).toHaveCount(0);
  await expect(page.getByRole('heading', { level: 1, name: 'Real title from backend' })).toBeVisible();
});
```

- [ ] **Step 2: Add the mobile / tablet-portrait test**

Append (skip-guard for desktop):

```ts
test('non-desktop: + in Sessions drawer creates pending, drawer closes, turn → tree refresh swaps placeholder with real row', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  let turnRequestCount = 0;
  let treeRequestCount = 0;
  let createdSessionUUID = '';
  // Drive the tree directly so we can surface the new session after the turn,
  // instead of using mockIdleTree which returns a fixed body.
  await page.route('**/api/commander/tree', async (route) => {
    treeRequestCount += 1;
    const base = idleTreePayload.daemons[0];
    const sessions = [...base.sessions];
    if (turnRequestCount > 0 && createdSessionUUID) {
      sessions.unshift({
        ...base.sessions[0],
        session_id: createdSessionUUID,
        title: 'Real title from backend',
        turn_state: 'idle',
      });
    }
    await route.fulfill({ json: { daemons: [{ ...base, sessions }] } });
  });
  await page.route('**/api/commander/daemons/d1/sessions/*/turn', async (route, request) => {
    turnRequestCount += 1;
    const match = String(request.url()).match(/\/sessions\/([^/]+)\/turn$/);
    if (match) createdSessionUUID = decodeURIComponent(match[1]);
    await route.fulfill({
      body: 'event: done\ndata: {"result":{}}\n\n',
      headers: { 'content-type': 'text/event-stream' },
    });
  });
  await page.route('**/api/commander/daemons/d1/sessions/*', async (route, request) => {
    const match = String(request.url()).match(/\/sessions\/([^/]+)$/);
    const sid = match ? decodeURIComponent(match[1]) : 'unknown';
    await route.fulfill({
      json: { session: { ID: sid, Title: 'Real title from backend' }, messages: [] },
    });
  });
  await page.goto('/commander/');
  await expect.poll(() => treeRequestCount).toBeGreaterThanOrEqual(1);
  const treesBeforeClick = treeRequestCount;
  await page.getByRole('button', { name: 'Sessions' }).click();
  await expect(page.getByTestId('drawer-left')).toBeVisible();
  await page.getByTestId('drawer-left').getByRole('button', { name: /新建 session: prod/ }).click();
  // Drawer closes; placeholder header + active composer
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);
  await expect(page.getByRole('heading', { level: 1, name: '新建会话' })).toBeVisible();
  await expect(page.getByLabel('输入提示词')).toBeEnabled();
  // Submit prompt
  await page.getByLabel('输入提示词').fill('hello');
  await page.getByRole('button', { name: '发送' }).click();
  await expect.poll(() => turnRequestCount).toBeGreaterThanOrEqual(1);
  await expect.poll(() => treeRequestCount).toBeGreaterThanOrEqual(treesBeforeClick + 1);
  // Open the drawer again to inspect the tree
  await page.getByRole('button', { name: 'Sessions' }).click();
  const drawer2 = page.getByTestId('drawer-left');
  await expect(drawer2.getByText('新建会话(待提交)')).toHaveCount(0);
  await expect(drawer2.getByText('新建会话(同步中…)')).toHaveCount(0);
  await expect(drawer2.getByText('Real title from backend')).toBeVisible();
});
```

- [ ] **Step 3: Extend the existing test 7 (`drawer interactive controls meet 44px hit area`) to cover `.daemon-new-session-btn` AND `.session-discard-btn`**

Find the existing test by searching for the string `drawer interactive controls meet 44px hit area`. After the existing assertions that loop `.session-row`, `.session-toggle`, etc. and before the line `await page.goBack();` (which closes the Sessions drawer to open Files next), insert the new assertions AND **REPLACE** that `await page.goBack();` line with our own deterministic close.

The `.daemon-new-session-btn` is always rendered (when daemon is `ok`), so the loop just needs to be added after the existing session-row block. The `.session-discard-btn` only renders on a draft virtual row, so the test must first click `+` to create one. The whole block also takes responsibility for closing the Sessions drawer at the end (clicking the drawer's own close button) so the test does NOT call `page.goBack()` afterward — `goBack()` after our re-select would have no overlay history to pop and would navigate the test out of `/commander/`, breaking the subsequent Files drawer assertions.

Apply this edit verbatim:
1. Add the `.daemon-new-session-btn` and `.session-discard-btn` assertions before the existing `await page.goBack();` line.
2. **Delete the `await page.goBack();` line** — the snippet below ends by clicking the drawer's own close button instead.

```ts
  // New + button must also meet the 44x44 rule on mobile.
  for (const plus of await left.locator('.daemon-new-session-btn').all()) {
    await assertHitArea(plus, '.daemon-new-session-btn');
  }
  // Click + to materialize a pending draft row, then verify × discard hit
  // area, then click × so the test's subsequent steps (Files drawer + go.mod
  // assertion) still target the original session s1 — not the pending UUID
  // for which no /files route was mocked.
  const firstPlus = left.locator('.daemon-new-session-btn').first();
  if (await firstPlus.isEnabled()) {
    await firstPlus.click();
    // The drawer closes after +; reopen to inspect the virtual row.
    await page.getByRole('button', { name: 'Sessions' }).click();
    const reopened = page.getByTestId('drawer-left');
    await expect(reopened).toBeVisible();
    const discard = reopened.locator('.session-discard-btn').first();
    await expect(discard).toBeVisible();
    await assertHitArea(discard, '.session-discard-btn');
    // Discard the draft so the rest of the test goes back to selected=s1
    // and the existing /files?path=. mock keeps matching.
    await discard.click();
    await expect(reopened.locator('.session-discard-btn')).toHaveCount(0);
    // Re-select s1 explicitly: the discard cleared selected, and on mobile
    // a fresh auto-select would re-pick s1 anyway, but make it deterministic.
    // This click also closes the drawer (handleSelectSession routes through
    // closeOverlay), so we do NOT need (and MUST NOT use) page.goBack() to
    // close it — that would have no overlay history to pop and would
    // navigate the test out of /commander/.
    await reopened.getByRole('button', { name: /Fix commander session cache latency/ }).click();
    await expect(page.getByTestId('drawer-left')).toHaveCount(0);
  } else {
    // + is disabled (some other pending exists in the fixture). Just close
    // the drawer the same way the original test did.
    await page.goBack();
  }
```

- [ ] **Step 4: Run e2e with the build server**

Run from `internal/commanderhub/webapp/`:
```
npm run e2e
```
Expected: all e2e tests pass across all three Playwright projects (desktop, mobile, tablet-portrait). The 2 new tests are added; test 7 now also covers `.daemon-new-session-btn`.

- [ ] **Step 5: Rebuild the dist + commit**

```
npm run build
```
Expected: succeeds. New asset hash in `internal/commanderhub/assets/dist/` (it's vendored — commit it).

```bash
git add internal/commanderhub/webapp/src/e2e/commander.spec.ts \
        internal/commanderhub/assets/dist/
git commit -m "test(commander): add e2e for + new-session flow + extend hit-area coverage

Two new e2e tests (desktop + non-desktop) cover the full + → pending
draft → first turn → real-row swap flow. Existing test 7 also asserts
.daemon-new-session-btn meets the 44x44 hit-area rule on mobile.
Rebuilds the embedded dist so the deployed observer-server picks up
the new commander bundle.

Refs: #30 follow-up"
```

---

## Self-Review

**Spec coverage:**

| Spec requirement | Task |
| --- | --- |
| Pending state with `'draft'` / `'submitting'` phases | 4 |
| Single-slot pending; other-daemon `+` disabled in both phases | 2 (UI) + 4 (createPendingSession guard) |
| Phase-aware disabled titles | 2 |
| `+` only on `ok` daemons | 2 |
| Virtual row in owning daemon's session list, phase-aware title | 2 |
| `×` discard on draft row only, `stopPropagation` | 2 |
| `createPendingSession` idempotency on same-daemon re-click | 4 |
| Detail-fetch short-circuit only in `'draft'` phase | 4 (detail-effect rewrite) |
| `sendPrompt` post-done phase flip + `loadTree()` | 4 |
| `loadTree.then` clears pending when real row appears | 4 |
| `pendingDaemonOffline` → `composerLocked` + `composerNote` | 4 |
| `ChatWorkspace.composerLocked` + `composerNote` | 1 |
| MobileShell forwards pending/discard/lock/note | 5 |
| MobileShell `handleCreate` closes Sessions drawer on `+` | 5 |
| CSS for `+`/`×`/note, 44px hit areas on mobile | 3 |
| E2E: full + → turn → real-row swap | 6 |
| Test 7 extension for `.daemon-new-session-btn` hit area | 6 |
| Dist rebuild so deployed observer-server picks up the new bundle | 6 |

No spec requirement is unmapped.

**Placeholder scan:** No "TBD", "TODO", "implement later", "similar to task N", or unspecified hand-waving remain. Every code step shows the exact code; every test step has the literal test body; every commit step has the exact `git commit -m` text.

**Type consistency:**
- `PendingSession = { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' }` — same shape in Task 2 (DaemonSessionTree prop), Task 4 (CommanderApp state), Task 5 (MobileShell prop).
- `createPendingSession(daemonID: string) => void` — Task 4 defines it, Tasks 5 and 6 consume it via `onCreateSession`.
- `discardPendingSession() => void` — Task 4 defines, but DaemonSessionTree's prop is `onDiscardSession?: (sessionID: string) => void` (sessionID arg). Task 4's `discardPendingSession` ignores the arg (reads from ref instead) — accepted because the discard target is always the single pending session. Task 5 forwards `onDiscardSession={discardPendingSession}` directly; TypeScript accepts the wider-arg adapter because functions with fewer parameters than the prop signature are valid React handlers.
- `ChatWorkspace.composerLocked?: boolean` + `composerNote?: string` — Task 1 defines, Tasks 4 + 5 consume.
- Disabled-title strings used in Task 2 implementation match the strings asserted in the Task 2 test cases.
- Copy strings (`新建会话`, `新建会话(待提交)`, `新建会话(同步中…)`, `daemon 离线 — 无法提交,...`, `丢弃草稿`, `先发送或丢弃当前草稿`, `等待新会话出现在列表中`) — identical in Task 1/2/4/6 tests and Task 1/2/4 implementations.
