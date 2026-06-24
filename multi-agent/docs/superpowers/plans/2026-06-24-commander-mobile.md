# Commander Mobile & Tablet Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/commander` usable end-to-end on common phone (360 / 390 / 430) and tablet portrait (768 / 834) widths while preserving the existing three-pane desktop experience at ≥1024px.

**Architecture:** Replace the `@media (max-width: 900px) { display: none }` mobile fallback with a single `1024px` breakpoint that swaps the existing three-pane shell for a new mobile shell. The mobile shell keeps `ChatWorkspace` as the primary surface and exposes daemon/session navigation and the file explorer via Radix Dialog drawers (Sessions on the left, Files on the right) plus a full-viewport file-preview sheet that stacks over the Files drawer. A `useOverlayHistory()` hook owned by `CommanderApp` bridges browser Back to drawer/sheet close and drains pushed history entries on breakpoint crossings.

**Tech Stack:** React 19, TypeScript 6, Vite 8, Vitest 4, Playwright 1.61, `@radix-ui/react-dialog` (new). Plain CSS (`internal/commanderhub/webapp/src/styles.css`). Existing `lucide-react`, `react-markdown`, `rehype-sanitize`, `remark-gfm`. No CSS framework added.

## Global Constraints

The following constraints come from the spec and apply to every task — never violate them, never ask the user to relax them mid-task:

- Single breakpoint: **`max-width: 1023px`** = non-desktop (mobile + tablet portrait); **`min-width: 1024px`** = desktop three-pane (unchanged). The existing `@media (max-width: 900px)` block must be deleted, not extended.
- Critical panels (daemon tree, file panel) must never be hidden via `display: none` on `< 1024px`. They render inside Radix Dialog overlays.
- Every interactive control on `< 1024px` must have a hit area with **both** `height >= 44` **and** `width >= 44`. Height-only assertions are forbidden in tests.
- Use **`100dvh`**, not `100vh`, in `.commander-shell`, `.login-shell`, drawer content, and the preview sheet.
- Add **`@radix-ui/react-dialog`** only. Do not add `vaul`, Tailwind, or any CSS framework.
- All new chat-area, drawer, and sheet copy must remain consistent with the existing Chinese / English mix in the webapp (the spec quotes the exact empty-state copy below).
- Desktop default behavior — "no session selected on first paint" — must be preserved. Auto-select runs **only** when `isNonDesktop && selected == null` and only one-shot per mount (or on full logout reset).
- Composer `textarea` font-size must be `16px` on `< 1024px` (iOS auto-zoom suppression). Send button must be `min-height: 44px; min-width: 44px` on `< 1024px`.
- All file paths below are relative to the repo root unless prefixed `./`.
- Tests live alongside source: `*.test.tsx` next to component, e2e in `internal/commanderhub/webapp/src/e2e/`.

## File Structure

**New files:**

```
internal/commanderhub/webapp/src/
  hooks/
    useMediaQuery.ts                 # matchMedia React hook
    useMediaQuery.test.ts            # vitest for the hook
    useOverlayHistory.ts             # stable controller + factory
    useOverlayHistory.test.ts        # vitest for the controller
  components/
    MobileShell.tsx                  # < 1024px shell
    MobileShell.test.tsx
    MobileDrawer.tsx                 # Radix Dialog wrapper for left/right drawer
    MobileDrawer.test.tsx
    FilePreviewSheet.tsx             # full-viewport Radix Dialog
    FilePreviewSheet.test.tsx
  CommanderApp.mobile.test.tsx       # mobile-branch vitest (task 8)
```

**Modified files:**

```
internal/commanderhub/webapp/
  package.json                       # add @radix-ui/react-dialog
  playwright.config.ts               # add chromium-tablet-portrait project
  src/
    CommanderApp.tsx                 # branch on media query, hoist overlay
                                     # controller, auto-select, drawer state
    components/
      ChatWorkspace.tsx              # mobileLeading / mobileTrailing / empty
      ChatWorkspace.test.tsx         # +slots + empty assertion
      FileExplorerPanel.tsx          # renderMode prop, onPreview payload
      FileExplorerPanel.test.tsx     # +sheet mode + persistence assertion
    styles.css                       # 1024px breakpoint, drawer/sheet CSS,
                                     # 44px hit areas, 100dvh, login bumps
    e2e/commander.spec.ts            # new tests 1–10 + assertHitArea helper
```

**Unchanged files:** `DaemonSessionTree.tsx`, `StatusBadge.tsx`, `MessageRenderer.tsx`, `api/*`, all server-side Go code.

## Task ordering

Tasks below are ordered to land green-bar after each step:

1. Dependency + media-query hook (foundation, no UI change)
2. `useOverlayHistory` hook (pure logic, unit-tested alone)
3. `ChatWorkspace` slots + `empty` (additive props)
4. `FileExplorerPanel` `renderMode` + `onPreview` (additive prop)
5. `MobileDrawer` (Radix wrapper)
6. `FilePreviewSheet` (Radix wrapper)
7. `MobileShell` (composes all the above)
8. `CommanderApp` wiring (auto-select + media-query branch + controller)
9. CSS: `1024px` breakpoint replacement, 44px hit areas, `100dvh`, login bumps
10. Playwright config: add tablet-portrait project
11. E2E test suite (tests 1–10) + screenshots

---

### Task 1: Add `@radix-ui/react-dialog` and the `useMediaQuery` hook

**Files:**
- Modify: `internal/commanderhub/webapp/package.json`
- Modify: `internal/commanderhub/webapp/package-lock.json` (regenerated)
- Create: `internal/commanderhub/webapp/src/hooks/useMediaQuery.ts`
- Create: `internal/commanderhub/webapp/src/hooks/useMediaQuery.test.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `@radix-ui/react-dialog` available for import as `* as Dialog`.
  - `useMediaQuery(query: string, options?: { onChange?: (matches: boolean) => void }): boolean` — re-renders the caller when the match flips. SSR-safe: returns `false` on first render if `window` is undefined. The optional `onChange` fires **synchronously inside the matchMedia change handler, before** the hook calls `setMatches` (so the caller can run side-effects — for example `history.go(...)` — before React commits the new render). `onChange` is captured in a ref so the hook does not re-attach the listener whenever the callback identity changes.

- [ ] **Step 1: Write the failing hook test**

Create `internal/commanderhub/webapp/src/hooks/useMediaQuery.test.ts`:

```ts
import { act, renderHook } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { useMediaQuery } from './useMediaQuery';

type MQLListener = (ev: MediaQueryListEvent) => void;

function installMatchMedia(initialMatches: boolean) {
  const listeners = new Set<MQLListener>();
  let matches = initialMatches;
  const mql = {
    get matches() {
      return matches;
    },
    media: '',
    addEventListener: (_: 'change', l: MQLListener) => listeners.add(l),
    removeEventListener: (_: 'change', l: MQLListener) => listeners.delete(l),
    dispatchEvent: () => true,
    onchange: null,
  } as unknown as MediaQueryList;
  window.matchMedia = vi.fn().mockReturnValue(mql);
  return {
    flip(next: boolean) {
      matches = next;
      for (const l of listeners) l({ matches } as MediaQueryListEvent);
    },
  };
}

afterEach(() => {
  // restore matchMedia between tests
  // @ts-expect-error allow reset
  delete window.matchMedia;
});

test('returns the initial match value', () => {
  installMatchMedia(true);
  const { result } = renderHook(() => useMediaQuery('(max-width: 1023px)'));
  expect(result.current).toBe(true);
});

test('re-renders when the media query flips', () => {
  const ctrl = installMatchMedia(false);
  const { result } = renderHook(() => useMediaQuery('(max-width: 1023px)'));
  expect(result.current).toBe(false);
  act(() => ctrl.flip(true));
  expect(result.current).toBe(true);
});

test('invokes onChange synchronously before the new render', () => {
  const ctrl = installMatchMedia(true);
  const order: string[] = [];
  const onChange = vi.fn((next: boolean) => {
    // At the moment onChange runs, the hook has not yet called setMatches,
    // so any side-effect (history.go, state reset) runs before React commits.
    order.push(`onChange:${next}`);
  });
  const { result } = renderHook(() => useMediaQuery('(max-width: 1023px)', { onChange }));
  order.push(`initial:${result.current}`);
  act(() => ctrl.flip(false));
  order.push(`after:${result.current}`);
  expect(onChange).toHaveBeenCalledWith(false);
  // The synchronous-before-commit ordering: onChange precedes the post-flip
  // render observation.
  expect(order).toEqual(['initial:true', 'onChange:false', 'after:false']);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run from `internal/commanderhub/webapp/`: `npm test -- src/hooks/useMediaQuery.test.ts`
Expected: FAIL — `Cannot find module './useMediaQuery'`.

- [ ] **Step 3: Implement the hook**

Create `internal/commanderhub/webapp/src/hooks/useMediaQuery.ts`:

```ts
import { useEffect, useRef, useState } from 'react';

export function useMediaQuery(
  query: string,
  options?: { onChange?: (matches: boolean) => void },
): boolean {
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return false;
    }
    return window.matchMedia(query).matches;
  });

  // Keep the latest onChange in a ref so changing identity does not re-attach
  // the listener, but the synchronous call inside the handler still uses the
  // most-recently-passed callback.
  const onChangeRef = useRef(options?.onChange);
  useEffect(() => {
    onChangeRef.current = options?.onChange;
  });

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return;
    }
    const mql = window.matchMedia(query);
    setMatches(mql.matches);
    const handler = (event: MediaQueryListEvent) => {
      // Run the consumer's side-effects (e.g. history.go) BEFORE updating
      // state, so React's next render observes the post-side-effect world.
      onChangeRef.current?.(event.matches);
      setMatches(event.matches);
    };
    mql.addEventListener('change', handler);
    return () => mql.removeEventListener('change', handler);
  }, [query]);

  return matches;
}
```

- [ ] **Step 4: Add the Radix dependency**

Run from `internal/commanderhub/webapp/`: `npm install @radix-ui/react-dialog@^1.1.0 --save-exact=false`
Expected: `package.json` `dependencies` gains `@radix-ui/react-dialog`; `package-lock.json` updates.

- [ ] **Step 5: Run hook test + typecheck**

Run from `internal/commanderhub/webapp/`:
- `npm test -- src/hooks/useMediaQuery.test.ts` → expected: PASS (3 tests).
- `npm run build` → expected: tsc + vite build both succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/webapp/package.json \
        internal/commanderhub/webapp/package-lock.json \
        internal/commanderhub/webapp/src/hooks/useMediaQuery.ts \
        internal/commanderhub/webapp/src/hooks/useMediaQuery.test.ts
git commit -m "feat(commander): add useMediaQuery hook + @radix-ui/react-dialog dep

Foundation for the mobile/tablet shell work (issue #30). The hook is
SSR-safe and re-renders the caller when the underlying matchMedia
result flips, which CommanderApp will use to swap shells on rotation /
window resize.

Refs: #30"
```

---

### Task 2: `useOverlayHistory` hook with stable controller

**Files:**
- Create: `internal/commanderhub/webapp/src/hooks/useOverlayHistory.ts`
- Create: `internal/commanderhub/webapp/src/hooks/useOverlayHistory.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces (exports from `useOverlayHistory.ts`):
  - `type OverlayID = 'sessions' | 'files' | 'preview'`
  - `type OverlayController = { open(id: OverlayID): void; closeTop(id: OverlayID): void; reset(): void; drainForBreakpoint(): void; onPop(handler: (id: OverlayID) => void): () => void; stackSnapshot(): readonly OverlayID[]; }`
  - `function createOverlayController(): OverlayController` — plain factory; no React.
  - `function useOverlayHistory(): OverlayController` — returns a stable instance via `useRef`.

Behavior pinned by the spec (Browser Back section):
- `open(id)`: pushes `id` onto an internal `stack`, calls `history.pushState({ commanderOverlay: id }, '')`; attaches the `popstate` listener on first open.
- `closeTop(id)`: if `stack.at(-1) === id`, calls `history.back()` (the `popstate` handler does the rest); otherwise no-op.
- `popstate` listener pops the top of the stack and emits `onPop(id)` to subscribers. If the stack is empty when `popstate` fires, do nothing.
- `reset()`: detaches the listener, clears the stack, clears subscribers; **never** touches history.
- `drainForBreakpoint()`: snapshots `len = stack.length`, clears the stack, then `if (len > 0) window.history.go(-len)`. Does not emit `onPop` (consumers reset their own UI separately).

- [ ] **Step 1: Write failing tests for the factory contract**

Create `internal/commanderhub/webapp/src/hooks/useOverlayHistory.test.ts`:

```ts
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { createOverlayController } from './useOverlayHistory';

beforeEach(() => {
  // Reset history state between tests.
  window.history.replaceState(null, '', window.location.pathname);
});

afterEach(() => {
  vi.restoreAllMocks();
});

test('open(id) pushes a history entry tagged with commanderOverlay', () => {
  const controller = createOverlayController();
  controller.open('sessions');
  expect(window.history.state).toEqual({ commanderOverlay: 'sessions' });
  expect(controller.stackSnapshot()).toEqual(['sessions']);
});

test('closeTop calls history.back when id matches the top', () => {
  const controller = createOverlayController();
  const back = vi.spyOn(window.history, 'back').mockImplementation(() => {});
  controller.open('files');
  controller.closeTop('files');
  expect(back).toHaveBeenCalledTimes(1);
});

test('closeTop is a no-op when id does not match the top', () => {
  const controller = createOverlayController();
  const back = vi.spyOn(window.history, 'back').mockImplementation(() => {});
  controller.open('files');
  controller.closeTop('sessions');
  expect(back).not.toHaveBeenCalled();
});

test('popstate pops the stack and notifies onPop subscribers', () => {
  const controller = createOverlayController();
  const handler = vi.fn();
  controller.onPop(handler);
  controller.open('files');
  controller.open('preview');
  window.dispatchEvent(new PopStateEvent('popstate', { state: { commanderOverlay: 'files' } }));
  expect(handler).toHaveBeenCalledWith('preview');
  expect(controller.stackSnapshot()).toEqual(['files']);
});

test('popstate with empty stack is ignored', () => {
  const controller = createOverlayController();
  const handler = vi.fn();
  controller.onPop(handler);
  window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  expect(handler).not.toHaveBeenCalled();
});

test('reset detaches listener and never touches history', () => {
  const controller = createOverlayController();
  const handler = vi.fn();
  controller.onPop(handler);
  const back = vi.spyOn(window.history, 'back').mockImplementation(() => {});
  controller.open('sessions');
  controller.reset();
  window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  expect(handler).not.toHaveBeenCalled();
  expect(back).not.toHaveBeenCalled();
  expect(controller.stackSnapshot()).toEqual([]);
});

test('drainForBreakpoint calls history.go(-len) once and clears the stack', () => {
  const controller = createOverlayController();
  const go = vi.spyOn(window.history, 'go').mockImplementation(() => {});
  controller.open('files');
  controller.open('preview');
  controller.drainForBreakpoint();
  expect(go).toHaveBeenCalledExactlyOnceWith(-2);
  expect(controller.stackSnapshot()).toEqual([]);
});

test('drainForBreakpoint with empty stack does not call history.go', () => {
  const controller = createOverlayController();
  const go = vi.spyOn(window.history, 'go').mockImplementation(() => {});
  controller.drainForBreakpoint();
  expect(go).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run from `internal/commanderhub/webapp/`: `npm test -- src/hooks/useOverlayHistory.test.ts`
Expected: FAIL — `Cannot find module './useOverlayHistory'`.

- [ ] **Step 3: Implement the factory + hook**

Create `internal/commanderhub/webapp/src/hooks/useOverlayHistory.ts`:

```ts
import { useRef } from 'react';

export type OverlayID = 'sessions' | 'files' | 'preview';

export interface OverlayController {
  open(id: OverlayID): void;
  closeTop(id: OverlayID): void;
  reset(): void;
  drainForBreakpoint(): void;
  onPop(handler: (id: OverlayID) => void): () => void;
  stackSnapshot(): readonly OverlayID[];
}

export function createOverlayController(): OverlayController {
  const stack: OverlayID[] = [];
  const subscribers = new Set<(id: OverlayID) => void>();
  let listener: ((event: PopStateEvent) => void) | null = null;

  function ensureListener() {
    if (listener || typeof window === 'undefined') return;
    listener = () => {
      const popped = stack.pop();
      if (!popped) return;
      for (const handler of subscribers) handler(popped);
    };
    window.addEventListener('popstate', listener);
  }

  function detachListener() {
    if (!listener || typeof window === 'undefined') return;
    window.removeEventListener('popstate', listener);
    listener = null;
  }

  return {
    open(id) {
      ensureListener();
      stack.push(id);
      if (typeof window !== 'undefined') {
        window.history.pushState({ commanderOverlay: id }, '');
      }
    },
    closeTop(id) {
      if (stack[stack.length - 1] !== id) return;
      if (typeof window !== 'undefined') window.history.back();
    },
    reset() {
      detachListener();
      stack.length = 0;
      subscribers.clear();
    },
    drainForBreakpoint() {
      const len = stack.length;
      stack.length = 0;
      if (len > 0 && typeof window !== 'undefined') {
        window.history.go(-len);
      }
    },
    onPop(handler) {
      subscribers.add(handler);
      return () => subscribers.delete(handler);
    },
    stackSnapshot() {
      return stack.slice();
    },
  };
}

export function useOverlayHistory(): OverlayController {
  const ref = useRef<OverlayController | null>(null);
  if (ref.current === null) ref.current = createOverlayController();
  return ref.current;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run from `internal/commanderhub/webapp/`: `npm test -- src/hooks/useOverlayHistory.test.ts`
Expected: PASS (8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/hooks/useOverlayHistory.ts \
        internal/commanderhub/webapp/src/hooks/useOverlayHistory.test.ts
git commit -m "feat(commander): add useOverlayHistory controller for mobile drawer back-stack

Stable closure-owned controller (issue #30). open/closeTop/reset/drain
operate on an internal stack; popstate is the single source of truth
for which overlay is on top. drainForBreakpoint is the only path that
mutates history outside of user-initiated open/close, and is reserved
for the matchMedia mobile->desktop transition.

Refs: #30"
```

---

### Task 3: Extend `ChatWorkspace` with `mobileLeading` / `mobileTrailing` / `empty`

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`
- Modify: `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx`

**Interfaces:**
- Consumes: nothing new.
- Produces — extended `ChatWorkspace` props:
  ```ts
  mobileLeading?: ReactNode;   // rendered as first flex child of .chat-header
  mobileTrailing?: ReactNode;  // rendered as last flex child of .chat-header
  empty?: boolean;             // when true, composer textarea + button forced
                               // disabled AND the message list shows the
                               // centered hint copy below in place of messages.
  ```
  When `empty` is true, the message list renders a single `<p>` with the
  literal text `No sessions yet — open Sessions to pick one once a daemon
  appears` (spec §First-screen behavior) inside a `.message-list-empty`
  wrapper. Default (`mobileLeading`/`mobileTrailing` unset, `empty` unset)
  preserves today's behavior exactly.

- [ ] **Step 1: Read the existing test file to preserve style**

Read `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx` — note the helper that builds a `SessionDetail`. Reuse it in the new cases below.

- [ ] **Step 2: Add failing tests for the three new behaviors**

Append the following at the bottom of `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx` (keep all existing tests intact):

```tsx
test('renders mobileLeading and mobileTrailing slots inside chat-header', () => {
  render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={null}
      turnState="idle"
      onSend={vi.fn()}
      mobileLeading={<button type="button">L</button>}
      mobileTrailing={<button type="button">R</button>}
    />,
  );
  const header = screen.getByTestId('chat-workspace').querySelector('.chat-header') as HTMLElement | null;
  expect(header).not.toBeNull();
  expect(within(header as HTMLElement).getByRole('button', { name: 'L' })).toBeInTheDocument();
  expect(within(header as HTMLElement).getByRole('button', { name: 'R' })).toBeInTheDocument();
});

test('empty=true forces composer disabled and shows the no-sessions hint', () => {
  render(
    <ChatWorkspace
      daemonID=""
      sessionID=""
      session={null}
      turnState="idle"
      onSend={vi.fn()}
      empty
    />,
  );
  expect(screen.getByLabelText('输入提示词')).toBeDisabled();
  expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
  expect(
    screen.getByText('No sessions yet — open Sessions to pick one once a daemon appears'),
  ).toBeInTheDocument();
});

test('empty=false (default) keeps composer enabled at turnState idle', () => {
  render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={null}
      turnState="idle"
      onSend={vi.fn()}
    />,
  );
  expect(screen.getByLabelText('输入提示词')).toBeEnabled();
  expect(screen.getByRole('button', { name: '发送' })).toBeEnabled();
});
```

Also add `screen, within` to the imports at the top if they are not already present, and `vi` from `vitest`.

- [ ] **Step 3: Run tests to verify the three new tests fail**

Run from `internal/commanderhub/webapp/`: `npm test -- src/components/ChatWorkspace.test.tsx`
Expected: FAIL on the new tests — props not accepted / disabled assertions break.

- [ ] **Step 4: Update `ChatWorkspace.tsx`**

Edit `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`. Replace the destructured props block and the header JSX so the new props are honored:

```tsx
import { useLayoutEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { MessageRenderer } from './MessageRenderer';
import type { SessionDetail, TurnState } from '../api/types';

// turnStateLabels / displayTurnState / sessionString unchanged

export function ChatWorkspace({
  session,
  turnState,
  onSend,
  mobileLeading,
  mobileTrailing,
  empty,
}: {
  daemonID: string;
  sessionID: string;
  session: SessionDetail | null;
  turnState: TurnState | string;
  onSend: (prompt: string) => Promise<void>;
  mobileLeading?: ReactNode;
  mobileTrailing?: ReactNode;
  empty?: boolean;
}) {
  const title = sessionString(session?.session, 'Title', 'title') || 'Session';
  const cwd = sessionString(session?.session, 'WorkingDir', 'working_dir');
  const disabled =
    empty === true ||
    ['queued', 'answering', 'awaiting_approval'].includes(turnState);
  const messages = session?.messages || [];
  const messageListRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const list = messageListRef.current;
    if (!list) return;
    list.scrollTop = list.scrollHeight;
  }, [sessionString(session?.session, 'ID', 'id'), messages.length]);

  return (
    <main className="chat-workspace" data-testid="chat-workspace">
      <header className="chat-header">
        {mobileLeading ? <div className="chat-header-slot">{mobileLeading}</div> : null}
        <div className="chat-header-title">
          <h1>{title}</h1>
          <p>{cwd}</p>
        </div>
        <span data-testid="turn-status" className="turn-status" role="status" aria-live="polite">
          {displayTurnState(turnState)}
        </span>
        {mobileTrailing ? <div className="chat-header-slot">{mobileTrailing}</div> : null}
      </header>
      {/* message-list and composer unchanged */}
      <div data-testid="message-list" className="message-list" ref={messageListRef}>
        {empty ? (
          <p className="message-list-empty">
            No sessions yet — open Sessions to pick one once a daemon appears
          </p>
        ) : (
          messages.map((msg, index) => {
            const role = msg.role || 'assistant';
            const text = msg.text || '';
            return (
              <article key={index} className={`message message-${role}`}>
                <MessageRenderer text={text} />
              </article>
            );
          })
        )}
      </div>
      <form
        className="composer"
        onSubmit={async (event) => {
          event.preventDefault();
          const input = event.currentTarget.elements.namedItem('prompt') as HTMLTextAreaElement;
          const prompt = input.value.trim();
          if (!prompt) return;
          try {
            await onSend(prompt);
            input.value = '';
          } catch {
            // Keep the draft in place so the user can retry or edit it.
          }
        }}
      >
        <textarea aria-label="输入提示词" name="prompt" disabled={disabled} />
        <button type="submit" disabled={disabled}>
          发送
        </button>
      </form>
    </main>
  );
}
```

Note: `chat-header-slot` and `chat-header-title` classes are styled in task 9.

- [ ] **Step 5: Run all ChatWorkspace tests to verify they pass**

Run from `internal/commanderhub/webapp/`: `npm test -- src/components/ChatWorkspace.test.tsx`
Expected: PASS (all existing + 3 new).

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/webapp/src/components/ChatWorkspace.tsx \
        internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx
git commit -m "feat(commander): add mobileLeading/mobileTrailing/empty to ChatWorkspace

Slots let MobileShell inject Sessions/Files triggers into the existing
chat-header so the screen retains a single header band (issue #30).
The empty prop forces the composer disabled when no session is
selected on mobile, preventing the no-op submit dead-end.

Refs: #30"
```

---

### Task 4: Extend `FileExplorerPanel` with `renderMode` + `onPreview`

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx`
- Modify: `internal/commanderhub/webapp/src/components/FileExplorerPanel.test.tsx`

**Interfaces:**
- Consumes: existing `fullPath(root, path)` helper already in this file (kept exported).
- Produces — `FileExplorerPanel` props become:
  ```ts
  daemonID: string;
  sessionID: string;
  renderMode?: 'inline' | 'sheet'; // default 'inline' = today's behavior
  onPreview?: (payload: {
    preview: FileReadResult;
    fullPath: string;
    displayPath: string;
  }) => void;
  ```
  In `'sheet'` mode the panel renders only the file tree (no `<FilePreview>` block) and invokes `onPreview` when a file is read successfully. In `'inline'` mode (default) the existing behavior is preserved bit-for-bit.

- [ ] **Step 1: Add failing tests**

Append to `internal/commanderhub/webapp/src/components/FileExplorerPanel.test.tsx`:

```tsx
test('renderMode="sheet" calls onPreview with { preview, fullPath, displayPath } and omits inline preview', async () => {
  const onPreview = vi.fn();
  const fetchMock = vi.fn(async (input: RequestInfo) => {
    const url = typeof input === 'string' ? input : input.url;
    if (url.includes('/files?path=.')) {
      return new Response(JSON.stringify({
        root: '/root/project',
        path: '.',
        entries: [{ name: 'go.mod', path: 'go.mod', kind: 'file', size: 12 }],
      }), { status: 200 });
    }
    if (url.includes('/files/content?path=go.mod')) {
      return new Response(JSON.stringify({ path: 'go.mod', size: 12, content: 'module x' }), { status: 200 });
    }
    return new Response('not found', { status: 404 });
  });
  vi.stubGlobal('fetch', fetchMock);

  render(
    <FileExplorerPanel
      daemonID="d1"
      sessionID="s1"
      renderMode="sheet"
      onPreview={onPreview}
    />,
  );

  await screen.findByText('go.mod');
  // Sheet mode must NOT render the inline preview region.
  expect(screen.queryByText('No file selected')).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: /打开文件 go.mod/ }));
  await vi.waitFor(() => expect(onPreview).toHaveBeenCalledTimes(1));
  expect(onPreview.mock.calls[0][0]).toEqual({
    preview: expect.objectContaining({ path: 'go.mod', content: 'module x' }),
    fullPath: '/root/project/go.mod',
    displayPath: 'go.mod',
  });
});

test('renderMode="sheet" preserves directory expansion across external rerender (sheet open/close cycle)', async () => {
  const fetchMock = vi.fn(async (input: RequestInfo) => {
    const url = typeof input === 'string' ? input : input.url;
    if (url.includes('/files?path=.')) {
      return new Response(JSON.stringify({
        root: '/root/project',
        path: '.',
        entries: [
          { name: 'cmd', path: 'cmd', kind: 'dir' },
          { name: 'go.mod', path: 'go.mod', kind: 'file', size: 12 },
        ],
      }), { status: 200 });
    }
    if (url.includes('/files?path=cmd')) {
      return new Response(JSON.stringify({
        root: '/root/project',
        path: 'cmd',
        entries: [{ name: 'main.go', path: 'cmd/main.go', kind: 'file', size: 4 }],
      }), { status: 200 });
    }
    return new Response('not found', { status: 404 });
  });
  vi.stubGlobal('fetch', fetchMock);

  const { rerender } = render(
    <FileExplorerPanel daemonID="d1" sessionID="s1" renderMode="sheet" onPreview={vi.fn()} />,
  );
  await screen.findByText('cmd');
  fireEvent.click(screen.getByRole('button', { name: /展开目录 cmd/ }));
  await screen.findByText('main.go');

  // Simulate the parent rerendering the same component instance
  // (e.g. because the file-preview sheet opened/closed elsewhere).
  rerender(<FileExplorerPanel daemonID="d1" sessionID="s1" renderMode="sheet" onPreview={vi.fn()} />);
  expect(screen.getByText('main.go')).toBeInTheDocument();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run from `internal/commanderhub/webapp/`: `npm test -- src/components/FileExplorerPanel.test.tsx`
Expected: FAIL — `renderMode` / `onPreview` not accepted; `expect(screen.queryByText('No file selected'))` triggers because today the inline preview always renders.

- [ ] **Step 3: Rewrite `FileExplorerPanel.tsx`**

Replace the entire contents of `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx` with:

```tsx
import { useEffect, useRef, useState } from 'react';
import type { CSSProperties, ReactNode } from 'react';
import { ChevronDown, ChevronRight, Copy } from 'lucide-react';
import { apiGet, fileContentPath, filesPath } from '../api/client';
import type { FileEntry, FileListResult, FileReadResult } from '../api/types';

export function FilePreview({ preview }: { preview: FileReadResult | null }) {
  if (!preview) return <div className="file-preview-empty">No file selected</div>;
  if (preview.too_large) {
    return (
      <div className="file-preview">
        <strong>{preview.path}</strong>
        <p>文件超过 2MB, 不预览。</p>
      </div>
    );
  }
  if (preview.binary) {
    return (
      <div className="file-preview">
        <strong>{preview.path}</strong>
        <p>二进制文件 · {preview.size} bytes</p>
      </div>
    );
  }
  return (
    <pre className="file-preview">
      <code>{preview.content || ''}</code>
    </pre>
  );
}

type DirectoryNode = {
  expanded: boolean;
  entries?: FileEntry[];
  loading?: boolean;
};

export function isAbsolutePath(path: string) {
  return path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith('\\\\');
}

export function fullPath(root: string, path: string) {
  if (!root || path === '.' || isAbsolutePath(path)) return path;
  const separator = root.includes('\\') ? '\\' : '/';
  const cleanRoot = root.replace(/[\\/]+$/, '');
  const cleanPath = path.replace(/^[\\/]+/, '').replace(/[\\/]+/g, separator);
  return `${cleanRoot}${separator}${cleanPath}`;
}

export function FileExplorerPanel({
  daemonID,
  sessionID,
  renderMode = 'inline',
  onPreview,
}: {
  daemonID: string;
  sessionID: string;
  renderMode?: 'inline' | 'sheet';
  onPreview?: (payload: {
    preview: FileReadResult;
    fullPath: string;
    displayPath: string;
  }) => void;
}) {
  const [root, setRoot] = useState('');
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [directories, setDirectories] = useState<Record<string, DirectoryNode>>({});
  const [preview, setPreview] = useState<FileReadResult | null>(null);
  const [error, setError] = useState('');
  const previewRequestRef = useRef(0);
  const listingRequestRef = useRef(0);

  useEffect(() => {
    let cancelled = false;
    previewRequestRef.current += 1;
    listingRequestRef.current += 1;
    const requestID = listingRequestRef.current;
    setRoot('');
    setEntries([]);
    setDirectories({});
    setPreview(null);
    setError('');

    if (!daemonID || !sessionID) return;

    apiGet<FileListResult>(filesPath(daemonID, sessionID, '.'))
      .then((result) => {
        if (!cancelled && listingRequestRef.current === requestID) {
          setRoot(result.root || '');
          setEntries(result.entries || []);
        }
      })
      .catch((err: Error) => {
        if (!cancelled && listingRequestRef.current === requestID) setError(err.message);
      });

    return () => {
      cancelled = true;
    };
  }, [daemonID, sessionID]);

  async function openFile(entry: FileEntry) {
    if (entry.kind !== 'file' || !daemonID || !sessionID) return;
    const requestID = previewRequestRef.current + 1;
    previewRequestRef.current = requestID;
    if (renderMode === 'inline') setPreview(null);
    setError('');
    try {
      const result = await apiGet<FileReadResult>(fileContentPath(daemonID, sessionID, entry.path));
      if (previewRequestRef.current !== requestID) return;
      if (renderMode === 'sheet') {
        onPreview?.({
          preview: result,
          fullPath: fullPath(root, entry.path),
          displayPath: entry.path,
        });
        return;
      }
      setPreview(result);
    } catch (err) {
      if (previewRequestRef.current === requestID) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  }

  async function toggleDirectory(entry: FileEntry) {
    if (entry.kind !== 'dir' || !daemonID || !sessionID) return;

    const current = directories[entry.path];
    if (current?.expanded) {
      setDirectories((prev) => ({
        ...prev,
        [entry.path]: { ...prev[entry.path], expanded: false },
      }));
      return;
    }

    setDirectories((prev) => ({
      ...prev,
      [entry.path]: { ...(prev[entry.path] || {}), expanded: true, loading: !prev[entry.path]?.entries },
    }));
    if (current?.entries) return;

    const requestID = listingRequestRef.current;
    try {
      const result = await apiGet<FileListResult>(filesPath(daemonID, sessionID, entry.path));
      if (listingRequestRef.current !== requestID) return;
      setRoot((current) => current || result.root || '');
      setDirectories((prev) => ({
        ...prev,
        [entry.path]: { expanded: true, entries: result.entries || [], loading: false },
      }));
    } catch (err) {
      if (listingRequestRef.current !== requestID) return;
      setDirectories((prev) => ({
        ...prev,
        [entry.path]: { ...(prev[entry.path] || {}), expanded: false, loading: false },
      }));
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function copyPath(path: string) {
    try {
      await navigator.clipboard.writeText(path);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function renderEntries(items: FileEntry[], depth = 0): ReactNode {
    return items.map((entry) => {
      const isDir = entry.kind === 'dir';
      const dir = directories[entry.path];
      const isExpanded = !!dir?.expanded;
      return (
        <div className="file-node" key={entry.path}>
          <div className="file-row-line" style={{ '--depth': depth } as CSSProperties}>
            <button
              aria-label={isDir ? `${isExpanded ? '收起' : '展开'}目录 ${entry.name}` : `打开文件 ${entry.name}`}
              className="file-row"
              onClick={() => (isDir ? void toggleDirectory(entry) : void openFile(entry))}
              type="button"
            >
              <span className="file-kind">
                {isDir ? (isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />) : 'FILE'}
              </span>
              <span className="file-name">{entry.name}</span>
            </button>
            <button
              aria-label={`复制路径 ${entry.path}`}
              className="file-copy-button"
              onClick={() => void copyPath(fullPath(root, entry.path))}
              title="复制路径"
              type="button"
            >
              <Copy size={14} />
            </button>
          </div>
          {isDir && isExpanded ? (
            <div className="file-children">
              {dir?.loading ? <div className="file-loading">Loading</div> : renderEntries(dir?.entries || [], depth + 1)}
            </div>
          ) : null}
        </div>
      );
    });
  }

  return (
    <aside className="file-panel" data-testid="file-panel">
      <div className="file-list">{renderEntries(entries)}</div>
      {error ? <div className="file-error">{error}</div> : null}
      {renderMode === 'inline' ? <FilePreview preview={preview} /> : null}
    </aside>
  );
}
```

The only behavioral change vs today is:
1. `renderMode` / `onPreview` are added to the props (default `'inline'` preserves desktop behavior).
2. `openFile` branches: when `renderMode === 'sheet'` it invokes `onPreview({ preview, fullPath, displayPath })` and skips the local `setPreview` call.
3. The trailing `<FilePreview>` is only rendered in `inline` mode.
4. `isAbsolutePath` and `fullPath` are now exported so `FilePreviewSheet` can reuse `fullPath` if needed — current implementation does the `fullPath` computation inside `FileExplorerPanel` before invoking `onPreview`, so re-exporting is forward compatibility only.

- [ ] **Step 4: Run tests to verify they pass**

Run from `internal/commanderhub/webapp/`: `npm test -- src/components/FileExplorerPanel.test.tsx`
Expected: PASS (all existing + 2 new).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx \
        internal/commanderhub/webapp/src/components/FileExplorerPanel.test.tsx
git commit -m "feat(commander): add renderMode + onPreview to FileExplorerPanel

renderMode='sheet' (issue #30) renders only the file tree and hands a
{ preview, fullPath, displayPath } payload to the caller via onPreview.
Directory expansion lives in the same component instance, so opening
and closing an external preview sheet does not collapse the tree.
Default renderMode='inline' preserves desktop behavior bit-for-bit.

Refs: #30"
```

---

### Task 5: `MobileDrawer` Radix wrapper

**Files:**
- Create: `internal/commanderhub/webapp/src/components/MobileDrawer.tsx`
- Create: `internal/commanderhub/webapp/src/components/MobileDrawer.test.tsx`

**Interfaces:**
- Consumes: `@radix-ui/react-dialog`.
- Produces:
  ```ts
  export function MobileDrawer(props: {
    open: boolean;
    onOpenChange: (next: boolean) => void; // called by Radix on ESC/overlay-click and on close-button
    side: 'left' | 'right';
    title: string;
    children: ReactNode;
  }): JSX.Element;
  ```
  - Renders `Dialog.Root`/`Portal`/`Overlay`/`Content` with `role="dialog"`, `aria-modal="true"` (provided by Radix), `data-testid={'drawer-' + side}`.
  - Content classes: `mobile-drawer mobile-drawer-${side}`; close button: `.mobile-drawer-close` (44×44; styled in task 9).

- [ ] **Step 1: Write failing tests**

Create `internal/commanderhub/webapp/src/components/MobileDrawer.test.tsx`:

```tsx
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { MobileDrawer } from './MobileDrawer';

afterEach(cleanup);

test('renders children when open=true and hides them when open=false', () => {
  const { rerender } = render(
    <MobileDrawer open={false} onOpenChange={vi.fn()} side="left" title="Sessions">
      <p>inside</p>
    </MobileDrawer>,
  );
  expect(screen.queryByText('inside')).not.toBeInTheDocument();
  rerender(
    <MobileDrawer open={true} onOpenChange={vi.fn()} side="left" title="Sessions">
      <p>inside</p>
    </MobileDrawer>,
  );
  expect(screen.getByText('inside')).toBeInTheDocument();
  expect(screen.getByRole('dialog')).toHaveAttribute('aria-modal', 'true');
});

test('clicking the close button invokes onOpenChange(false)', () => {
  const onOpenChange = vi.fn();
  render(
    <MobileDrawer open={true} onOpenChange={onOpenChange} side="right" title="Files">
      <p>inside</p>
    </MobileDrawer>,
  );
  fireEvent.click(screen.getByRole('button', { name: /关闭 Files/ }));
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test('pressing ESC invokes onOpenChange(false) (Radix wires this)', () => {
  const onOpenChange = vi.fn();
  render(
    <MobileDrawer open={true} onOpenChange={onOpenChange} side="left" title="Sessions">
      <p>inside</p>
    </MobileDrawer>,
  );
  fireEvent.keyDown(document.activeElement || document.body, { key: 'Escape' });
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test('content carries side-specific testid and class', () => {
  render(
    <MobileDrawer open={true} onOpenChange={vi.fn()} side="right" title="Files">
      <p>inside</p>
    </MobileDrawer>,
  );
  const content = screen.getByTestId('drawer-right');
  expect(content.classList.contains('mobile-drawer-right')).toBe(true);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run from `internal/commanderhub/webapp/`: `npm test -- src/components/MobileDrawer.test.tsx`
Expected: FAIL — `Cannot find module './MobileDrawer'`.

- [ ] **Step 3: Implement `MobileDrawer.tsx`**

```tsx
import type { ReactNode } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { X } from 'lucide-react';

export function MobileDrawer({
  open,
  onOpenChange,
  side,
  title,
  children,
}: {
  open: boolean;
  onOpenChange: (next: boolean) => void;
  side: 'left' | 'right';
  title: string;
  children: ReactNode;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="mobile-overlay" />
        <Dialog.Content
          className={`mobile-drawer mobile-drawer-${side}`}
          data-testid={`drawer-${side}`}
        >
          <header className="mobile-drawer-header">
            <Dialog.Title className="mobile-drawer-title">{title}</Dialog.Title>
            <Dialog.Close asChild>
              <button
                className="mobile-drawer-close"
                type="button"
                aria-label={`关闭 ${title}`}
              >
                <X size={20} />
              </button>
            </Dialog.Close>
          </header>
          <div className="mobile-drawer-body">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run from `internal/commanderhub/webapp/`: `npm test -- src/components/MobileDrawer.test.tsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/components/MobileDrawer.tsx \
        internal/commanderhub/webapp/src/components/MobileDrawer.test.tsx
git commit -m "feat(commander): add MobileDrawer Radix Dialog wrapper

Left/right side drawer used by MobileShell (issue #30). Radix provides
focus trap, ESC, overlay click, and aria-modal; the wrapper adds a
44x44 close button and side-specific testids/classes for styling.

Refs: #30"
```

---

### Task 6: `FilePreviewSheet`

**Files:**
- Create: `internal/commanderhub/webapp/src/components/FilePreviewSheet.tsx`
- Create: `internal/commanderhub/webapp/src/components/FilePreviewSheet.test.tsx`

**Interfaces:**
- Consumes: `FilePreview` exported from `FileExplorerPanel.tsx` (task 4).
- Produces:
  ```ts
  export type FilePreviewPayload = {
    preview: FileReadResult;
    fullPath: string;
    displayPath: string;
  };
  export function FilePreviewSheet(props: {
    open: boolean;
    onOpenChange: (next: boolean) => void;
    payload: FilePreviewPayload | null;
  }): JSX.Element;
  ```
  Renders a full-viewport Radix Dialog with a 56px top bar: close (×), truncated `displayPath`, "Copy path" button that writes `payload.fullPath` to clipboard. Body is `<FilePreview preview={payload.preview}>` with `max-height: calc(100dvh - 56px)`.

- [ ] **Step 1: Write failing tests**

Create `internal/commanderhub/webapp/src/components/FilePreviewSheet.test.tsx`:

```tsx
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import type { FileReadResult } from '../api/types';
import { FilePreviewSheet } from './FilePreviewSheet';

afterEach(cleanup);

const payload = {
  preview: { path: 'go.mod', size: 8, content: 'module x' } as FileReadResult,
  fullPath: '/root/project/go.mod',
  displayPath: 'go.mod',
};

test('renders preview content + display path when open with payload', () => {
  render(<FilePreviewSheet open={true} onOpenChange={vi.fn()} payload={payload} />);
  expect(screen.getByText('module x')).toBeInTheDocument();
  expect(screen.getByText('go.mod')).toBeInTheDocument();
});

test('Copy path writes fullPath, not displayPath, to clipboard', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
  render(<FilePreviewSheet open={true} onOpenChange={vi.fn()} payload={payload} />);
  fireEvent.click(screen.getByRole('button', { name: /Copy path/i }));
  expect(writeText).toHaveBeenCalledWith('/root/project/go.mod');
});

test('close button invokes onOpenChange(false)', () => {
  const onOpenChange = vi.fn();
  render(<FilePreviewSheet open={true} onOpenChange={onOpenChange} payload={payload} />);
  fireEvent.click(screen.getByRole('button', { name: /关闭预览/ }));
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test('renders nothing visible when open=false', () => {
  render(<FilePreviewSheet open={false} onOpenChange={vi.fn()} payload={payload} />);
  expect(screen.queryByText('module x')).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm test -- src/components/FilePreviewSheet.test.tsx`
Expected: FAIL — `Cannot find module`.

- [ ] **Step 3: Implement `FilePreviewSheet.tsx`**

```tsx
import * as Dialog from '@radix-ui/react-dialog';
import { Copy, X } from 'lucide-react';
import type { FileReadResult } from '../api/types';
import { FilePreview } from './FileExplorerPanel';

export type FilePreviewPayload = {
  preview: FileReadResult;
  fullPath: string;
  displayPath: string;
};

export function FilePreviewSheet({
  open,
  onOpenChange,
  payload,
}: {
  open: boolean;
  onOpenChange: (next: boolean) => void;
  payload: FilePreviewPayload | null;
}) {
  const fullPath = payload?.fullPath ?? '';
  const displayPath = payload?.displayPath ?? '';
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="mobile-overlay" />
        <Dialog.Content
          className="file-preview-sheet"
          data-testid="file-preview-sheet"
        >
          <header className="file-preview-sheet-header">
            <Dialog.Close asChild>
              <button
                type="button"
                className="file-preview-sheet-close"
                aria-label="关闭预览"
              >
                <X size={20} />
              </button>
            </Dialog.Close>
            <Dialog.Title asChild>
              <span className="file-preview-sheet-path" title={displayPath}>
                {displayPath}
              </span>
            </Dialog.Title>
            <button
              type="button"
              className="file-preview-sheet-copy"
              aria-label="Copy path"
              onClick={() => {
                if (fullPath) void navigator.clipboard?.writeText(fullPath);
              }}
            >
              <Copy size={16} /> Copy path
            </button>
          </header>
          <div className="file-preview-sheet-body">
            {payload ? <FilePreview preview={payload.preview} /> : null}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm test -- src/components/FilePreviewSheet.test.tsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/components/FilePreviewSheet.tsx \
        internal/commanderhub/webapp/src/components/FilePreviewSheet.test.tsx
git commit -m "feat(commander): add FilePreviewSheet (full-viewport Radix Dialog)

Stacked over the Files drawer on mobile (issue #30). 56px top bar
hosts a 44x44 close button, the displayPath (relative, ellipsis), and
a 'Copy path' button that writes the fullPath payload to clipboard.
Body reuses FilePreview from FileExplorerPanel so binary/too-large
formatting stays identical to desktop.

Refs: #30"
```

---

### Task 7: `MobileShell`

**Files:**
- Create: `internal/commanderhub/webapp/src/components/MobileShell.tsx`
- Create: `internal/commanderhub/webapp/src/components/MobileShell.test.tsx`

**Interfaces:**
- Consumes: `ChatWorkspace`, `DaemonSessionTree`, `FileExplorerPanel`, `MobileDrawer`, `FilePreviewSheet`. The overlay state itself lives in `CommanderApp` (per spec §Component Structure #6, "Owns drawer open/close state for Sessions and Files, plus the current FilePreviewSheet payload"). `MobileShell` is a pure rendering layer that receives the open flags and setters as props so the matchMedia breakpoint-cross handler in `CommanderApp` can reset them without prop-drilling refs back upward.
- Produces:
  ```ts
  export function MobileShell(props: {
    daemons: DaemonTree[];
    selected: { daemonID: string; sessionID: string } | null;
    onSelect: (daemonID: string, sessionID: string) => void;
    sessionDetail: SessionDetail | null;
    turnState: TurnState;
    onSend: (prompt: string) => Promise<void>;
    overlay: OverlayController;
    sessionsOpen: boolean;
    setSessionsOpen: (next: boolean) => void;
    filesOpen: boolean;
    setFilesOpen: (next: boolean) => void;
    previewPayload: FilePreviewPayload | null;
    setPreviewPayload: (next: FilePreviewPayload | null) => void;
  }): JSX.Element;
  ```
  - Renders `<ChatWorkspace mobileLeading={<SessionsButton/>} mobileTrailing={<FilesButton/>} empty={selected == null} ... />`.
  - On mount, subscribes to `overlay.onPop` to flip the appropriate setter in response to browser Back. Unsubscribes on unmount.
  - `[Sessions]` click → `overlay.open('sessions'); setSessionsOpen(true)`.
  - `[Files]` click → `overlay.open('files'); setFilesOpen(true)`.
  - Sessions drawer's `<DaemonSessionTree onSelect>` wraps the prop callback so it also calls `overlay.closeTop('sessions')` (which triggers popstate → `setSessionsOpen(false)`).
  - Files drawer's `<FileExplorerPanel renderMode="sheet" onPreview={p => { setPreviewPayload(p); overlay.open('preview'); }}>`.
  - Preview sheet `onOpenChange(false)` → `overlay.closeTop('preview')`.

- [ ] **Step 1: Write failing tests**

Create `internal/commanderhub/webapp/src/components/MobileShell.test.tsx`:

```tsx
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { createOverlayController } from '../hooks/useOverlayHistory';
import { MobileShell } from './MobileShell';
import type { DaemonTree } from '../api/types';

afterEach(cleanup);
beforeEach(() => window.history.replaceState(null, '', window.location.pathname));

const daemons: DaemonTree[] = [
  {
    daemon_id: 'd1',
    display_name: 'prod',
    kind: 'codex',
    status: 'ok',
    sessions: [
      {
        daemon_id: 'd1',
        session_id: 's1',
        kind: 'codex',
        title: 'Session one',
        origin: 'user',
        turn_state: 'idle',
        active_worker: false,
        awaiting_approval: false,
      },
    ],
  },
];

type RenderShellState = {
  sessionsOpen: boolean;
  filesOpen: boolean;
  previewPayload: null;
};

function renderShell(overrides: Partial<Parameters<typeof MobileShell>[0]> = {}) {
  const overlay = createOverlayController();
  const onSelect = vi.fn();
  const setSessionsOpen = vi.fn();
  const setFilesOpen = vi.fn();
  const setPreviewPayload = vi.fn();
  const props = {
    daemons,
    selected: { daemonID: 'd1', sessionID: 's1' },
    onSelect,
    sessionDetail: null,
    turnState: 'idle' as const,
    onSend: vi.fn(),
    overlay,
    sessionsOpen: false,
    setSessionsOpen,
    filesOpen: false,
    setFilesOpen,
    previewPayload: null,
    setPreviewPayload,
    ...overrides,
  };
  const utils = render(<MobileShell {...props} />);
  return { overlay, onSelect, setSessionsOpen, setFilesOpen, setPreviewPayload, ...utils };
}

test('renders chat workspace with Sessions and Files trigger buttons in chat-header', () => {
  renderShell();
  const header = document.querySelector('.chat-header') as HTMLElement;
  expect(within(header).getByRole('button', { name: /Sessions/ })).toBeInTheDocument();
  expect(within(header).getByRole('button', { name: /Files/ })).toBeInTheDocument();
});

test('clicking Sessions calls overlay.open + setSessionsOpen(true); drawer renders when prop is true', () => {
  const { setSessionsOpen, overlay, rerender } = renderShell();
  const openSpy = vi.spyOn(overlay, 'open');
  fireEvent.click(screen.getByRole('button', { name: /Sessions/ }));
  expect(openSpy).toHaveBeenCalledWith('sessions');
  expect(setSessionsOpen).toHaveBeenCalledWith(true);

  // Re-render with sessionsOpen=true (simulating CommanderApp setState).
  rerender(
    <MobileShell
      daemons={daemons}
      selected={{ daemonID: 'd1', sessionID: 's1' }}
      onSelect={vi.fn()}
      sessionDetail={null}
      turnState="idle"
      onSend={vi.fn()}
      overlay={overlay}
      sessionsOpen
      setSessionsOpen={setSessionsOpen}
      filesOpen={false}
      setFilesOpen={vi.fn()}
      previewPayload={null}
      setPreviewPayload={vi.fn()}
    />,
  );
  const drawer = screen.getByTestId('drawer-left');
  expect(within(drawer).getByTestId('daemon-tree')).toBeInTheDocument();
});

test('selecting a session in the drawer forwards onSelect and asks overlay.closeTop("sessions")', () => {
  const { overlay, setSessionsOpen, onSelect, rerender } = renderShell();
  // Simulate CommanderApp's open path: push 'sessions' onto the controller's
  // stack so handleSelectSession's closeOverlay helper takes the "top
  // matches" branch and exercises closeTop instead of the empty-stack
  // fallback covered by the next test.
  overlay.open('sessions');
  rerender(
    <MobileShell
      daemons={daemons}
      selected={{ daemonID: 'd1', sessionID: 's1' }}
      onSelect={onSelect}
      sessionDetail={null}
      turnState="idle"
      onSend={vi.fn()}
      overlay={overlay}
      sessionsOpen
      setSessionsOpen={setSessionsOpen}
      filesOpen={false}
      setFilesOpen={vi.fn()}
      previewPayload={null}
      setPreviewPayload={vi.fn()}
    />,
  );
  const drawer = screen.getByTestId('drawer-left');
  const closeSpy = vi.spyOn(overlay, 'closeTop').mockImplementation(() => {});
  fireEvent.click(within(drawer).getByRole('button', { name: /Session one/ }));
  expect(onSelect).toHaveBeenCalledWith('d1', 's1');
  expect(closeSpy).toHaveBeenCalledWith('sessions');
});

test('overlay.onPop("sessions") triggers setSessionsOpen(false)', () => {
  const { overlay, setSessionsOpen } = renderShell({ sessionsOpen: true });
  // simulate browser back popping the top of the controller's stack
  // The hook attaches the popstate listener internally; just dispatch the event
  // after pushing the same id into the controller so the stack is non-empty.
  overlay.open('sessions');
  window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  expect(setSessionsOpen).toHaveBeenCalledWith(false);
});

test('closing a drawer with empty overlay stack falls back to setOpen(false), never gets stuck', () => {
  // Defensive case: a remount / double-close could leave the controller's
  // stack empty while the React state still says sessionsOpen=true.
  // Clicking close (or pressing ESC, which Radix routes through onOpenChange)
  // must close the drawer instead of no-op'ing via closeTop.
  const { overlay, setSessionsOpen } = renderShell({ sessionsOpen: true });
  expect(overlay.stackSnapshot()).toEqual([]); // pre-condition
  const closeBtn = screen.getByRole('button', { name: /关闭 Sessions/ });
  fireEvent.click(closeBtn);
  expect(setSessionsOpen).toHaveBeenCalledWith(false);
});

test('empty=true when selected == null (composer disabled via ChatWorkspace)', () => {
  renderShell({ selected: null });
  expect(screen.getByLabelText('输入提示词')).toBeDisabled();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm test -- src/components/MobileShell.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement `MobileShell.tsx`**

```tsx
import { useEffect } from 'react';
import { Menu, FolderOpen } from 'lucide-react';
import type { DaemonTree, SessionDetail, TurnState } from '../api/types';
import type { OverlayController } from '../hooks/useOverlayHistory';
import { ChatWorkspace } from './ChatWorkspace';
import { DaemonSessionTree } from './DaemonSessionTree';
import { FileExplorerPanel } from './FileExplorerPanel';
import { MobileDrawer } from './MobileDrawer';
import { FilePreviewSheet, type FilePreviewPayload } from './FilePreviewSheet';

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
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
  sessionDetail: SessionDetail | null;
  turnState: TurnState;
  onSend: (prompt: string) => Promise<void>;
  overlay: OverlayController;
  sessionsOpen: boolean;
  setSessionsOpen: (next: boolean) => void;
  filesOpen: boolean;
  setFilesOpen: (next: boolean) => void;
  previewPayload: FilePreviewPayload | null;
  setPreviewPayload: (next: FilePreviewPayload | null) => void;
}) {
  useEffect(() => {
    const unsubscribe = overlay.onPop((id) => {
      if (id === 'sessions') setSessionsOpen(false);
      else if (id === 'files') setFilesOpen(false);
      else if (id === 'preview') setPreviewPayload(null);
    });
    return unsubscribe;
  }, [overlay, setSessionsOpen, setFilesOpen, setPreviewPayload]);

  function openSessions() {
    overlay.open('sessions');
    setSessionsOpen(true);
  }
  function openFiles() {
    overlay.open('files');
    setFilesOpen(true);
  }

  // Generic "close this overlay" — if the controller's stack has this id on
  // top, go through history.back() so the back stack stays consistent. If the
  // stack is empty or out of sync (defensive: SSR remount, double-fire), the
  // shell closes the React state directly so the user is never stuck with a
  // visible overlay that has no way to dismiss it (spec §Closing via UI).
  function closeOverlay(
    id: 'sessions' | 'files' | 'preview',
    setOpen: (next: boolean) => void,
  ) {
    const stack = overlay.stackSnapshot();
    if (stack.length > 0 && stack[stack.length - 1] === id) {
      overlay.closeTop(id);
    } else {
      setOpen(false);
    }
  }

  function closePreview() {
    const stack = overlay.stackSnapshot();
    if (stack.length > 0 && stack[stack.length - 1] === 'preview') {
      overlay.closeTop('preview');
    } else {
      setPreviewPayload(null);
    }
  }

  function handleSelectSession(daemonID: string, sessionID: string) {
    onSelect(daemonID, sessionID);
    closeOverlay('sessions', setSessionsOpen);
  }

  function handlePreview(payload: FilePreviewPayload) {
    setPreviewPayload(payload);
    overlay.open('preview');
  }

  const sessionsBtn = (
    <button
      type="button"
      className="chat-header-trigger"
      aria-label="Sessions"
      onClick={openSessions}
    >
      <Menu size={18} /> Sessions
    </button>
  );
  const filesBtn = (
    <button
      type="button"
      className="chat-header-trigger"
      aria-label="Files"
      onClick={openFiles}
    >
      <FolderOpen size={18} /> Files
    </button>
  );

  return (
    <div className="commander-shell commander-shell-mobile" data-testid="commander-shell">
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={sessionDetail}
        turnState={turnState}
        onSend={onSend}
        mobileLeading={sessionsBtn}
        mobileTrailing={filesBtn}
        empty={selected == null}
      />
      <MobileDrawer
        open={sessionsOpen}
        onOpenChange={(next) => {
          if (!next) closeOverlay('sessions', setSessionsOpen);
          else openSessions();
        }}
        side="left"
        title="Sessions"
      >
        <DaemonSessionTree daemons={daemons} selected={selected} onSelect={handleSelectSession} />
      </MobileDrawer>
      <MobileDrawer
        open={filesOpen}
        onOpenChange={(next) => {
          if (!next) closeOverlay('files', setFilesOpen);
          else openFiles();
        }}
        side="right"
        title="Files"
      >
        <FileExplorerPanel
          daemonID={selected?.daemonID || ''}
          sessionID={selected?.sessionID || ''}
          renderMode="sheet"
          onPreview={handlePreview}
        />
      </MobileDrawer>
      <FilePreviewSheet
        open={previewPayload != null}
        onOpenChange={(next) => {
          if (!next) closePreview();
        }}
        payload={previewPayload}
      />
    </div>
  );
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm test -- src/components/MobileShell.test.tsx`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/components/MobileShell.tsx \
        internal/commanderhub/webapp/src/components/MobileShell.test.tsx
git commit -m "feat(commander): add MobileShell composing chat + drawers + preview sheet

Single-column chat with Sessions/Files triggers in the chat-header,
left/right drawers via MobileDrawer, and a stacked preview sheet via
FilePreviewSheet (issue #30). MobileShell is a pure rendering layer:
sessionsOpen / filesOpen / previewPayload are received as props from
CommanderApp so the matchMedia breakpoint-cross handler can reset
them in place. The component still subscribes to overlay.onPop so
browser Back closes the topmost overlay deterministically.

Refs: #30"
```

---

### Task 8: Wire `CommanderApp` — media-query branch, auto-select, controller drain

**Files:**
- Modify: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- Create: `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx`

(Existing `CommanderApp.test.tsx` is unchanged.)

**Interfaces:**
- Consumes: `useMediaQuery`, `useOverlayHistory` (tasks 1 & 2), `MobileShell` (task 7), `FilePreviewPayload` type from `FilePreviewSheet` (task 6).
- Produces: `CommanderApp` that, when `useMediaQuery('(max-width: 1023px)')` is true, renders `<MobileShell>` (passing the hoisted overlay state down) and runs the mobile-only auto-select logic per spec. When false, renders the existing three-pane JSX unchanged. The component owns one `OverlayController` instance for its lifetime **and** the Sessions / Files / preview overlay React state (per spec §Component Structure #6).

Overlay state hoisting (spec §Component Structure #6):
- `CommanderApp` holds `[sessionsOpen, setSessionsOpen]`, `[filesOpen, setFilesOpen]`, and `[previewPayload, setPreviewPayload]` as `useState`. They are passed into `<MobileShell>` as props.
- The matchMedia mobile→desktop transition handler (below) calls all three setters with their closed/null values **before** flipping the local `isNonDesktop` view state, so the desktop shell never inherits stale overlay flags and a rotation back to mobile starts from a clean state.

The auto-select rule (spec §First-screen behavior):
- Helper `pickAutoSession(tree)` mirrors `DaemonSessionTree.buildCrossDaemonTree`: it scans `tree.daemons` in order; for each daemon scans `daemon.sessions` in order, returning the first session whose `origin` is **neither** `'subagent'` **nor** `'agent_task'` with a `parent_id` that resolves to another session in the whole tree. If every session is a resolvable child, fall back to the first session of any kind so the user lands on chat content. Returns `null` only when no session of any kind exists.
- A `hasAutoSelectedRef` guards one-shot behavior; it resets only when `authRequired` flips false → true.
- `tryAutoSelect(tree)`: when `!hasAutoSelectedRef.current && isNonDesktop && selected == null`, call `pickAutoSession(tree)`; if non-null, call `selectSession(...)` and set the ref.
- Invoke `tryAutoSelect` (a) inside `loadTree().then` after `setTree(nextTree)` and (b) from a `useEffect` keyed on `[isNonDesktop, tree]`.
- The matchMedia `onChange` callback (passed into `useMediaQuery`) is the **first** thing to run on a breakpoint transition. When it observes a `true → false` change (mobile → desktop) it runs, synchronously, in this exact order:
  1. `overlay.drainForBreakpoint()` (history.go(-len)).
  2. `setSessionsOpen(false)`, `setFilesOpen(false)`, `setPreviewPayload(null)` to flush overlay UI state.
  3. The hook then calls its own `setMatches(false)`.
  Because all three React `setState` calls happen inside the same microtask before React commits, the desktop shell never renders with stale overlay flags or phantom history entries — they're cleared in the same render cycle as the shell swap. Using `useEffect([isNonDesktop])` would be too late (React would have already committed the desktop shell once).
- A `useEffect(() => () => overlay.reset(), [overlay])` runs on full unmount (route change, logout) and only detaches the listener (no history mutation).

- [ ] **Step 1: Add failing mobile-branch tests**

Create `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx`:

```tsx
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { CommanderApp } from './CommanderApp';
import type { CommanderTree } from './api/types';

type MQLListener = (ev: MediaQueryListEvent) => void;

function installMatchMedia(initialMatches: boolean) {
  const listeners = new Set<MQLListener>();
  let matches = initialMatches;
  const mql = {
    get matches() { return matches; },
    media: '',
    addEventListener: (_: 'change', l: MQLListener) => listeners.add(l),
    removeEventListener: (_: 'change', l: MQLListener) => listeners.delete(l),
    dispatchEvent: () => true,
    onchange: null,
  } as unknown as MediaQueryList;
  window.matchMedia = vi.fn().mockReturnValue(mql);
  return {
    flip(next: boolean) {
      matches = next;
      for (const l of listeners) l({ matches } as MediaQueryListEvent);
    },
  };
}

function mockTreeFetch(tree: CommanderTree) {
  // Build a (daemonID, sessionID) -> title map so each detail response
  // carries the right title even when two daemons share a session_id.
  // Otherwise the owner-key regression test (which uses 's1' in both
  // daemons) would race-overwrite the map entry and assert against the
  // wrong title.
  const titleByOwner = new Map<string, string>();
  const detailKey = (daemonID: string, sessionID: string) =>
    `${daemonID}\0${sessionID}`;
  for (const daemon of tree.daemons) {
    for (const s of daemon.sessions || []) {
      titleByOwner.set(detailKey(daemon.daemon_id, s.session_id), s.title);
    }
  }
  return vi.fn(async (input: RequestInfo) => {
    const url = typeof input === 'string' ? input : input.url;
    if (url.includes('/api/commander/tree')) {
      return new Response(JSON.stringify(tree), { status: 200 });
    }
    const detailMatch = url.match(
      /\/api\/commander\/daemons\/([^/]+)\/sessions\/([^/?]+)$/,
    );
    if (detailMatch) {
      const daemonID = decodeURIComponent(detailMatch[1]);
      const sessionID = decodeURIComponent(detailMatch[2]);
      const title = titleByOwner.get(detailKey(daemonID, sessionID)) ?? sessionID;
      return new Response(
        JSON.stringify({ session: { ID: sessionID, Title: title }, messages: [] }),
        { status: 200 },
      );
    }
    return new Response('not found', { status: 404 });
  });
}

afterEach(() => {
  cleanup();
  // @ts-expect-error reset
  delete window.matchMedia;
  vi.restoreAllMocks();
});

beforeEach(() => window.history.replaceState(null, '', window.location.pathname));

const oneSessionTree: CommanderTree = {
  daemons: [
    {
      daemon_id: 'd1', display_name: 'prod', kind: 'codex', status: 'ok',
      sessions: [{ daemon_id: 'd1', session_id: 's1', kind: 'codex', title: 'Session one', origin: 'user', turn_state: 'idle', active_worker: false, awaiting_approval: false }],
    },
  ],
};

test('non-desktop: auto-selects the first session and renders MobileShell', async () => {
  installMatchMedia(true);
  vi.stubGlobal('fetch', mockTreeFetch(oneSessionTree));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('Session one')).toBeInTheDocument());
  expect(document.querySelector('.commander-shell-mobile')).not.toBeNull();
});

test('non-desktop: empty tree shows ChatWorkspace with disabled composer', async () => {
  installMatchMedia(true);
  vi.stubGlobal('fetch', mockTreeFetch({ daemons: [] }));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByLabelText('输入提示词')).toBeDisabled());
});

test('desktop: does not auto-select; chat header shows fallback "Session"', async () => {
  installMatchMedia(false);
  const fetchMock = mockTreeFetch(oneSessionTree);
  vi.stubGlobal('fetch', fetchMock);
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('Session', { selector: 'h1' })).toBeInTheDocument());
  const detailRequests = fetchMock.mock.calls.filter(([url]) => String(url).includes('/api/commander/daemons/'));
  expect(detailRequests).toHaveLength(0);
});

test('desktop→mobile transition triggers a single auto-select', async () => {
  const ctrl = installMatchMedia(false);
  vi.stubGlobal('fetch', mockTreeFetch(oneSessionTree));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('Session', { selector: 'h1' })).toBeInTheDocument());
  act(() => ctrl.flip(true));
  await waitFor(() => expect(screen.getByText('Session one')).toBeInTheDocument());
});

test('auto-select picks the first selectable session across multiple daemons', async () => {
  installMatchMedia(true);
  vi.stubGlobal('fetch', mockTreeFetch({
    daemons: [
      { daemon_id: 'd0', display_name: 'empty', kind: 'codex', status: 'ok', sessions: [] },
      { daemon_id: 'd1', display_name: 'prod', kind: 'codex', status: 'ok', sessions: oneSessionTree.daemons[0].sessions },
    ],
  }));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('Session one')).toBeInTheDocument());
});

test('auto-select falls back to subagent when only subagent sessions exist', async () => {
  installMatchMedia(true);
  vi.stubGlobal('fetch', mockTreeFetch({
    daemons: [
      { daemon_id: 'd1', display_name: 'prod', kind: 'codex', status: 'ok', sessions: [
        { daemon_id: 'd1', session_id: 'sub-1', kind: 'codex', title: 'Subagent only', origin: 'subagent', parent_id: 'absent', turn_state: 'idle', active_worker: false, awaiting_approval: false },
      ]},
    ],
  }));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('Subagent only')).toBeInTheDocument());
});

test('auto-select skips agent_task whose parent_id resolves in the tree', async () => {
  installMatchMedia(true);
  vi.stubGlobal('fetch', mockTreeFetch({
    daemons: [
      {
        daemon_id: 'd1', display_name: 'prod', kind: 'codex', status: 'ok',
        sessions: [
          // agent_task with a resolvable parent — DaemonSessionTree renders
          // this nested under parent-1, so it must NOT be auto-selected.
          { daemon_id: 'd1', session_id: 'task-1', kind: 'codex', title: 'Nested task', origin: 'agent_task', parent_id: 'parent-1', turn_state: 'idle', active_worker: false, awaiting_approval: false },
          // The resolvable parent — this is the legitimate top-level row.
          { daemon_id: 'd1', session_id: 'parent-1', kind: 'codex', title: 'Parent root', origin: 'user', turn_state: 'idle', active_worker: false, awaiting_approval: false },
        ],
      },
    ],
  }));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('Parent root')).toBeInTheDocument());
});

test('auto-select uses owner-keyed parent lookup so same session_id across daemons does not falsely link', async () => {
  // Two daemons both report session_id 's1'. The second daemon's 's1' is an
  // agent_task whose parent_id 's1' could be confused with the first daemon's
  // 's1' if the lookup used the global session_id set. With owner-keyed
  // namespacing, the second 's1' has no resolvable parent in its OWN owner
  // namespace and stays a root — so the first daemon's 's1' (scanned first)
  // is the auto-selected session.
  installMatchMedia(true);
  vi.stubGlobal('fetch', mockTreeFetch({
    daemons: [
      { daemon_id: 'd1', display_name: 'prod-a', kind: 'codex', status: 'ok', sessions: [
        { daemon_id: 'd1', session_id: 's1', kind: 'codex', title: 'First daemon root', origin: 'user', turn_state: 'idle', active_worker: false, awaiting_approval: false },
      ]},
      { daemon_id: 'd2', display_name: 'prod-b', kind: 'codex', status: 'ok', sessions: [
        // Same session_id, different daemon, no owner_agent_id => effectiveOwner is daemon:d2.
        { daemon_id: 'd2', session_id: 's1', kind: 'codex', title: 'Second daemon root', origin: 'agent_task', parent_id: 's1', turn_state: 'idle', active_worker: false, awaiting_approval: false },
      ]},
    ],
  }));
  render(<CommanderApp />);
  await waitFor(() => expect(screen.getByText('First daemon root')).toBeInTheDocument());
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm test -- src/CommanderApp.mobile.test.tsx`
Expected: FAIL — `MobileShell` not rendered, auto-select not implemented.

- [ ] **Step 3: Edit `CommanderApp.tsx`**

At the top of the file, replace the existing api/types import and add the new hook/component imports:

```tsx
// Replace the existing line
//   import type { CommanderTree, SessionDetail, TurnState } from './api/types';
// with the line below (adds SessionRow + FilePreviewPayload-relevant types):
import type { CommanderTree, SessionDetail, SessionRow, TurnState } from './api/types';

// New imports below the existing api/client + components imports:
import { useMediaQuery } from './hooks/useMediaQuery';
import { useOverlayHistory } from './hooks/useOverlayHistory';
import { MobileShell } from './components/MobileShell';
import type { FilePreviewPayload } from './components/FilePreviewSheet';
```

Add the helper near other top-level functions (above `CommanderApp`).
It mirrors `DaemonSessionTree.buildCrossDaemonTree` by using the same
owner-key namespacing (`effectiveOwner` + `parentOwnerFor`) so cross-daemon
sessions that happen to share a `session_id` are not mistaken for
parent/child:

```tsx
function effectiveOwner(s: SessionRow): string {
  return s.owner_agent_id ?? `daemon:${s.daemon_id}`;
}

function parentOwnerFor(s: SessionRow): string {
  return s.parent_agent_id ?? effectiveOwner(s);
}

function ownerKey(owner: string, sessionID: string): string {
  return `${owner}\0${sessionID}`;
}

function pickAutoSession(tree: CommanderTree | null) {
  if (!tree) return null;
  // Build the set of resolved-child owner keys with the same logic
  // DaemonSessionTree uses, so a session_id collision across daemons does
  // not cause us to skip a real top-level row.
  const presentOwnerKeys = new Set<string>();
  for (const daemon of tree.daemons) {
    for (const s of daemon.sessions || []) {
      presentOwnerKeys.add(ownerKey(effectiveOwner(s), s.session_id));
    }
  }
  const isChild = (s: SessionRow) =>
    (s.origin === 'subagent' || s.origin === 'agent_task') &&
    !!s.parent_id &&
    presentOwnerKeys.has(ownerKey(parentOwnerFor(s), s.parent_id));
  for (const daemon of tree.daemons) {
    for (const s of daemon.sessions || []) {
      if (!isChild(s)) return { daemonID: daemon.daemon_id, sessionID: s.session_id };
    }
  }
  // Fall back to the first session of any kind so mobile never lands in the
  // empty state when sessions actually exist.
  for (const daemon of tree.daemons) {
    for (const s of daemon.sessions || []) {
      return { daemonID: daemon.daemon_id, sessionID: s.session_id };
    }
  }
  return null;
}
```

Inside `CommanderApp` add (place after the existing `useState` hooks for `tree`/`error`/`authRequired`/`login`/`selected`/`sessionDetail`/`turnState`):

```tsx
const overlay = useOverlayHistory();
const hasAutoSelectedRef = useRef(false);

// Hoisted overlay UI state (spec §Component Structure #6).
const [sessionsOpen, setSessionsOpen] = useState(false);
const [filesOpen, setFilesOpen] = useState(false);
const [previewPayload, setPreviewPayload] = useState<FilePreviewPayload | null>(null);

// matchMedia onChange runs BEFORE the hook's setState commits the new
// isNonDesktop value, so we can drain history + reset overlay flags in the
// same microtask as the shell swap. See spec §Browser back as drawer/sheet
// close → Edge cases (breakpoint crossing).
const isNonDesktop = useMediaQuery('(max-width: 1023px)', {
  onChange: (nextIsNonDesktop) => {
    if (!nextIsNonDesktop) {
      // mobile -> desktop transition: drain pushed history AND reset overlay
      // UI state so a rotation back to mobile does not auto-reopen an overlay
      // whose backing history entries were already consumed.
      overlay.drainForBreakpoint();
      setSessionsOpen(false);
      setFilesOpen(false);
      setPreviewPayload(null);
    }
  },
});

useEffect(() => {
  // Reset the auto-select guard on full logout (false -> true).
  if (authRequired) hasAutoSelectedRef.current = false;
}, [authRequired]);

useEffect(() => () => overlay.reset(), [overlay]);

function tryAutoSelect(nextTree: CommanderTree | null) {
  if (hasAutoSelectedRef.current) return;
  if (!isNonDesktop) return;
  if (selectedRef.current != null) return;
  const pick = pickAutoSession(nextTree);
  if (!pick) return;
  hasAutoSelectedRef.current = true;
  selectSession(pick.daemonID, pick.sessionID);
}

useEffect(() => {
  tryAutoSelect(tree);
  // eslint-disable-next-line react-hooks/exhaustive-deps
}, [isNonDesktop, tree]);
```

Update the existing `loadTree` `.then` block so it also invokes `tryAutoSelect`. The existing `.catch` handler is unchanged; the only line you add is `tryAutoSelect(nextTree);` after `setAuthRequired(false);`:

```tsx
return apiGet<CommanderTree>('/api/commander/tree')
  .then((nextTree) => {
    setTree(nextTree);
    setAuthRequired(false);
    tryAutoSelect(nextTree);
  })
  .catch((err: Error) => {
    if (err.message === 'unauthorized') {
      setAuthRequired(true);
      setTree(null);
      return;
    }
    setError(err.message);
  });
```

Replace the final return block (after `authRequired` / `error` / `!tree` early returns) so it branches on `isNonDesktop`:

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
    />
  );
}
return (
  <div className="commander-shell" data-testid="commander-shell">
    <DaemonSessionTree daemons={tree.daemons} selected={selected} onSelect={selectSession} />
    <ChatWorkspace
      daemonID={selected?.daemonID || ''}
      sessionID={selected?.sessionID || ''}
      session={sessionDetail}
      turnState={turnState}
      onSend={sendPrompt}
    />
    <FileExplorerPanel daemonID={selected?.daemonID || ''} sessionID={selected?.sessionID || ''} />
  </div>
);
```

Note `loadTree` was declared with `useCallback([])`. Because `tryAutoSelect` reads the latest `isNonDesktop` / `selectedRef`, keep `loadTree`'s deps empty and rely on the `[isNonDesktop, tree]` effect for the breakpoint-change trigger.

- [ ] **Step 4: Run all CommanderApp tests + ensure the desktop test still passes**

Run from `internal/commanderhub/webapp/`:
- `npm test -- src/CommanderApp.test.tsx src/CommanderApp.mobile.test.tsx`
- `npm run build`

Expected: PASS for both files; build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/CommanderApp.tsx \
        internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx
git commit -m "feat(commander): branch CommanderApp on media query + auto-select on mobile

useMediaQuery('(max-width: 1023px)') swaps in MobileShell while
keeping desktop's three-pane JSX intact. CommanderApp owns the
useOverlayHistory controller AND the Sessions/Files/preview overlay
React state (per spec). The matchMedia mobile->desktop handler runs
drainForBreakpoint() then closes every overlay flag before flipping
isNonDesktop, so a rotation back to mobile starts from a clean
state. Auto-select runs one-shot on the first loadTree or when the
breakpoint flips to non-desktop with no selection; desktop never
auto-selects.

Refs: #30"
```

---

### Task 9: Replace CSS — 1024 breakpoint, drawer/sheet, 44px hit areas, 100dvh

**Files:**
- Modify: `internal/commanderhub/webapp/src/styles.css`

**Interfaces:**
- Consumes: class names from tasks 5–7 (`.mobile-overlay`, `.mobile-drawer*`, `.file-preview-sheet*`, `.chat-header-slot`, `.chat-header-title`, `.chat-header-trigger`, `.commander-shell-mobile`).
- Produces: CSS that satisfies every spec rule in §Touch target rules, §Composer, §Login, §Browser back as drawer/sheet close (via animation only), and the §Horizontal-overflow guard.

- [ ] **Step 1: Delete the existing mobile fallback block**

In `internal/commanderhub/webapp/src/styles.css`, remove the entire block:

```css
@media (max-width: 900px) {
  .commander-shell { grid-template-columns: 1fr; }
  .daemon-tree, .file-panel { display: none; }
}
```

- [ ] **Step 2: Replace `100vh` with `100dvh` in shells**

Change:
```css
.commander-shell { height: 100vh; ... }
.login-shell    { min-height: 100vh; ... }
body            { min-height: 100vh; }
```
to:
```css
.commander-shell { height: 100dvh; ... }
.login-shell    { min-height: 100dvh; ... }
body            { min-height: 100dvh; }
```

- [ ] **Step 3: Add chat-header slot CSS (always-on, no media query)**

Append:

```css
.chat-header-slot { display: flex; align-items: center; }
.chat-header-title { min-width: 0; flex: 1; }
.chat-header-trigger {
  display: inline-flex; align-items: center; gap: 6px;
  min-width: 44px; min-height: 44px;
  padding: 0 10px;
  border: 1px solid #d9e1ec; border-radius: 8px;
  background: #fff; color: #26364d;
}
.chat-header-trigger:hover { background: #f4f7fb; }

.message-list-empty {
  margin: auto;
  padding: 24px 16px;
  text-align: center;
  color: #69768a;
  font-size: 14px;
}
```

- [ ] **Step 4: Append the mobile / tablet-portrait stylesheet block**

Append:

```css
@media (max-width: 1023px) {
  .commander-shell,
  .commander-shell-mobile {
    grid-template-columns: 1fr;
  }

  /* Single-header chat: hide turn-status when too narrow only via ellipsis already in place. */
  .composer { padding: 10px 12px; padding-bottom: max(10px, env(safe-area-inset-bottom)); }
  .composer textarea { font-size: 16px; min-height: 44px; }
  .composer button { min-height: 44px; min-width: 44px; }

  .login-panel button, .login-panel a { min-height: 44px; min-width: 44px; }

  /* Hit-area bumps */
  .session-toggle, .session-toggle-spacer {
    width: 44px; min-width: 44px; height: 44px;
  }
  .session-row-line { grid-template-columns: 44px minmax(0, 1fr); }
  .session-row { min-height: 44px; }
  .file-row { min-height: 44px; }
  .file-copy-button { width: 44px; min-width: 44px; height: 44px; }

  /* Drawer + sheet — overlay shared across both */
  .mobile-overlay {
    position: fixed; inset: 0;
    background: rgba(15, 23, 42, 0.45);
    z-index: 20;
  }
  .mobile-drawer {
    position: fixed; top: 0; bottom: 0;
    background: #fff; z-index: 21;
    display: flex; flex-direction: column;
    padding-top: env(safe-area-inset-top);
    padding-bottom: env(safe-area-inset-bottom);
    box-shadow: 0 4px 24px rgba(15, 23, 42, 0.18);
    transition: transform 200ms ease-out;
    height: 100dvh;
  }
  .mobile-drawer-left  { left: 0;  width: min(320px, 88vw); transform: translateX(0); }
  .mobile-drawer-right { right: 0; width: min(360px, 92vw); transform: translateX(0); }
  .mobile-drawer[data-state="closed"].mobile-drawer-left  { transform: translateX(-100%); }
  .mobile-drawer[data-state="closed"].mobile-drawer-right { transform: translateX(100%); }
  @media (prefers-reduced-motion: reduce) {
    .mobile-drawer { transition: none; }
  }
  .mobile-drawer-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 12px; border-bottom: 1px solid #d9e1ec;
  }
  .mobile-drawer-title { margin: 0; font-size: 16px; color: #253348; }
  .mobile-drawer-close {
    width: 44px; height: 44px;
    display: inline-flex; align-items: center; justify-content: center;
    border: 0; background: transparent; color: #506074;
  }
  .mobile-drawer-body { flex: 1; overflow-y: auto; overflow-x: hidden; }

  .file-preview-sheet {
    position: fixed; inset: 0; z-index: 22;
    background: #fff;
    display: flex; flex-direction: column;
    padding-top: env(safe-area-inset-top);
    padding-bottom: env(safe-area-inset-bottom);
    height: 100dvh;
  }
  .file-preview-sheet-header {
    display: grid;
    grid-template-columns: 44px 1fr auto;
    align-items: center;
    gap: 8px;
    height: 56px;
    padding: 0 12px;
    border-bottom: 1px solid #d9e1ec;
  }
  .file-preview-sheet-close {
    width: 44px; height: 44px;
    display: inline-flex; align-items: center; justify-content: center;
    border: 0; background: transparent; color: #506074;
  }
  .file-preview-sheet-path {
    min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    color: #253348;
  }
  .file-preview-sheet-copy {
    display: inline-flex; align-items: center; gap: 6px;
    min-height: 44px; min-width: 44px;
    padding: 0 10px;
    border: 1px solid #d9e1ec; border-radius: 8px;
    background: #fff; color: #26364d;
  }
  .file-preview-sheet-body {
    flex: 1; overflow: auto; max-height: calc(100dvh - 56px);
  }
}
```

- [ ] **Step 5: Verify the build still produces CSS without warnings**

Run from `internal/commanderhub/webapp/`: `npm run build`
Expected: succeeds; no `Could not resolve` warnings.

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/webapp/src/styles.css
git commit -m "style(commander): 1024px breakpoint, drawer/sheet CSS, 44px hit areas, 100dvh

Deletes the legacy @media (max-width: 900px) display:none fallback and
replaces it with a single max-width:1023px breakpoint (issue #30).
Adds CSS for the new chat-header slot triggers, MobileDrawer left/right
positioning, file-preview-sheet header, and bumps session-toggle /
file-copy-button / session-row / file-row / composer / login controls
to >= 44x44 hit areas. Replaces 100vh with 100dvh on shells and uses
env(safe-area-inset-*) padding in drawers and the preview sheet.

Refs: #30"
```

---

### Task 10: Add the tablet-portrait Playwright project

**Files:**
- Modify: `internal/commanderhub/webapp/playwright.config.ts`

**Interfaces:**
- Consumes: existing Playwright config.
- Produces: a `chromium-tablet-portrait` project at 768×1024 alongside the existing `chromium-desktop` and `chromium-mobile` projects.

- [ ] **Step 1: Edit `playwright.config.ts`**

Replace the `projects` array:

```ts
projects: [
  {
    name: 'chromium-desktop',
    use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 960 } },
  },
  {
    name: 'chromium-tablet-portrait',
    use: { ...devices['Desktop Chrome'], viewport: { width: 768, height: 1024 } },
  },
  {
    name: 'chromium-mobile',
    use: { ...devices['Pixel 7'] },
  },
],
```

- [ ] **Step 2: Smoke the playwright config without running tests**

Run from `internal/commanderhub/webapp/`: `npx playwright test --list 2>&1 | head -20`
Expected: lists tests under all three projects.

- [ ] **Step 3: Commit**

```bash
git add internal/commanderhub/webapp/playwright.config.ts
git commit -m "test(commander): add chromium-tablet-portrait Playwright project at 768x1024

Required by issue #30 acceptance criterion 'usable at 768, 834'. The
new project will run the new non-desktop flows + a baseline screenshot."
```

---

### Task 11: Rewrite the E2E suite

**Files:**
- Modify: `internal/commanderhub/webapp/src/e2e/commander.spec.ts`

**Interfaces:**
- Consumes: the running webapp at `/commander/` plus the mocked endpoints already used by today's tests.
- Produces: all 10 e2e tests pinned by the spec, the `assertHitArea` helper, and four new screenshot baselines.

- [ ] **Step 1: Read the current spec file and identify the kept tests**

Open `internal/commanderhub/webapp/src/e2e/commander.spec.ts`. Keep:
- `desktop three-pane workbench is stable` (unchanged)
- `desktop panes own vertical scrolling and chat opens at bottom` (unchanged)

Remove:
- `mobile prioritizes chat without horizontal overflow`

- [ ] **Step 2: Add a top-of-file helper + utilities**

Above the existing `test.beforeEach` add:

```ts
async function assertHitArea(locator: import('@playwright/test').Locator, name: string) {
  const box = await locator.boundingBox();
  if (!box) throw new Error(`${name} has no bounding box`);
  expect.soft(box.height, `${name}.height`).toBeGreaterThanOrEqual(44);
  expect.soft(box.width, `${name}.width`).toBeGreaterThanOrEqual(44);
}

const fileMocks = {
  rootListing: {
    root: '/root/project',
    path: '.',
    entries: [
      { name: 'go.mod', path: 'go.mod', kind: 'file', size: 40 },
      { name: 'README.md', path: 'README.md', kind: 'file', size: 80 },
    ],
  },
  goMod: { path: 'go.mod', size: 40, content: 'module x' },
  readme: { path: 'README.md', size: 80, content: '# project' },
};

// The default treePayload at the top of this file uses
// turn_state: 'answering' so the existing desktop test exercises an
// in-flight turn. Mobile tests below want an idle composer; install
// this fixture by overriding the tree route at the top of each mobile
// test that needs to type into the composer.
const idleTreePayload = {
  daemons: [
    {
      ...treePayload.daemons[0],
      sessions: [
        { ...treePayload.daemons[0].sessions[0], turn_state: 'idle' },
      ],
    },
  ],
};

async function mockIdleTree(page: import('@playwright/test').Page) {
  await page.route('**/api/commander/tree', (route) => route.fulfill({ json: idleTreePayload }));
}
```

- [ ] **Step 3: Add tests 1–10**

Append (verbatim — copy below into the file; do not paraphrase):

```ts
test('non-desktop: auto-selects first session and chat is live', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.goto('/commander/');
  await expect(page.getByRole('heading', { level: 1, name: /Fix commander session cache latency/ })).toBeVisible();
  await expect(page.getByLabel('输入提示词')).toBeEnabled();
});

test('non-desktop: empty tree renders disabled composer + hint', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await page.route('**/api/commander/tree', (route) => route.fulfill({ json: { daemons: [] } }));
  await page.goto('/commander/');
  await expect(page.getByLabel('输入提示词')).toBeDisabled();
  await expect(page.getByRole('button', { name: '发送' })).toBeDisabled();
  await expect(
    page.getByText('No sessions yet — open Sessions to pick one once a daemon appears'),
  ).toBeVisible();
});

test('non-desktop: open sessions drawer, select session, send prompt', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  let turnCalled = false;
  await page.route('**/api/commander/daemons/d1/sessions/s1/turn', async (route) => {
    turnCalled = true;
    await route.fulfill({ body: 'event: done\ndata: {"result":{}}\n\n', headers: { 'content-type': 'text/event-stream' } });
  });
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Sessions' }).click();
  const sessionsDrawer = page.getByTestId('drawer-left');
  await expect(sessionsDrawer).toBeVisible();
  await expect(sessionsDrawer.getByTestId('daemon-tree')).toBeVisible();
  await sessionsDrawer.getByRole('button', { name: /Fix commander session cache latency/ }).click();
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);
  await page.getByLabel('输入提示词').fill('hi');
  await page.getByRole('button', { name: '发送' }).click();
  await expect.poll(() => turnCalled).toBe(true);
});

test('non-desktop: open files drawer, preview file, then preview a second', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=README.md', (route) => route.fulfill({ json: fileMocks.readme }));
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Files' }).click();
  await expect(page.getByTestId('drawer-right').getByText('go.mod')).toBeVisible();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  await expect(page.getByTestId('file-preview-sheet')).toBeVisible();
  await page.getByRole('button', { name: /Copy path/i }).click();
  const clip1 = await page.evaluate(() => navigator.clipboard.readText());
  expect(clip1).toBe('/root/project/go.mod');
  await page.getByRole('button', { name: '关闭预览' }).click();
  await expect(page.getByTestId('file-preview-sheet')).toHaveCount(0);
  await expect(page.getByTestId('drawer-right').getByText('go.mod')).toBeVisible();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 README.md/ }).click();
  await expect(page.getByTestId('file-preview-sheet').getByText('# project')).toBeVisible();
});

test('non-desktop: browser back closes overlays in stack order', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('about:blank');
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Sessions' }).click();
  await expect(page.getByTestId('drawer-left')).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);

  await page.getByRole('button', { name: 'Files' }).click();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  await expect(page.getByTestId('file-preview-sheet')).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId('file-preview-sheet')).toHaveCount(0);
  await expect(page.getByTestId('drawer-right')).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId('drawer-right')).toHaveCount(0);
});

test('non-desktop: no horizontal overflow at 360/390/430 and 834', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.goto('/commander/');
  for (const w of [360, 390, 430, 834]) {
    await page.setViewportSize({ width: w, height: 844 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect.soft(overflow, `viewport ${w}px`).toBe(false);
    await assertHitArea(page.getByRole('button', { name: 'Sessions' }), `Sessions@${w}`);
    await assertHitArea(page.getByRole('button', { name: 'Files' }), `Files@${w}`);
  }
});

test('non-desktop: drawer interactive controls meet 44px hit area', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  // Override the tree so Sessions drawer renders a parent + a subagent
  // child; this exposes a real `.session-toggle` to assert against. The
  // mockIdleTree helper covers the single-session default; for this test
  // we install a richer fixture.
  await page.route('**/api/commander/tree', (route) => route.fulfill({ json: {
    daemons: [
      {
        ...idleTreePayload.daemons[0],
        sessions: [
          { ...idleTreePayload.daemons[0].sessions[0] },
          {
            ...idleTreePayload.daemons[0].sessions[0],
            session_id: 'child-1',
            title: 'Child subagent session',
            origin: 'subagent',
            parent_id: 's1',
          },
        ],
      },
    ],
  }}));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('/commander/');

  await page.getByRole('button', { name: 'Sessions' }).click();
  const left = page.getByTestId('drawer-left');
  // Wait for the session list to actually render so .all() does not silently
  // return an empty array and turn the assertHitArea loop into a no-op.
  await expect(left.locator('.session-row').first()).toBeVisible();
  await expect(left.locator('.session-toggle').first()).toBeVisible();
  const sessionRows = await left.locator('.session-row').all();
  expect(sessionRows.length).toBeGreaterThan(0);
  for (const row of sessionRows) await assertHitArea(row, '.session-row');
  const sessionToggles = await left.locator('.session-toggle').all();
  expect(sessionToggles.length).toBeGreaterThan(0);
  for (const toggle of sessionToggles) await assertHitArea(toggle, '.session-toggle');
  await assertHitArea(left.locator('.mobile-drawer-close'), 'drawer-left close');
  await page.goBack();

  await page.getByRole('button', { name: 'Files' }).click();
  const right = page.getByTestId('drawer-right');
  await expect(right.getByText('go.mod')).toBeVisible();
  const fileRows = await right.locator('.file-row').all();
  expect(fileRows.length).toBeGreaterThan(0);
  for (const row of fileRows) await assertHitArea(row, '.file-row');
  const copyButtons = await right.locator('.file-copy-button').all();
  expect(copyButtons.length).toBeGreaterThan(0);
  for (const cp of copyButtons) await assertHitArea(cp, '.file-copy-button');
  await right.getByRole('button', { name: /打开文件 go.mod/ }).click();
  const sheet = page.getByTestId('file-preview-sheet');
  await expect(sheet).toBeVisible();
  await assertHitArea(sheet.locator('.file-preview-sheet-close'), 'sheet close');
  await assertHitArea(sheet.locator('.file-preview-sheet-copy'), 'sheet copy');
});

test('non-desktop: login screen is touch-friendly', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await page.route('**/api/commander/tree', (route) => route.fulfill({ status: 401, body: 'unauthorized' }));
  await page.route('**/api/commander/login', (route) => route.fulfill({ json: { login_id: 'id', verification_uri_complete: 'https://example.test/verify' } }));
  await page.route('**/api/commander/login/poll**', (route) => route.fulfill({ json: { status: 'pending' } }));
  await page.goto('/commander/');
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  expect(overflow).toBe(false);
  const loginBtn = page.getByRole('button', { name: '用 agentserver 登录' });
  await assertHitArea(loginBtn, 'login button');
  await loginBtn.click();
  const verifyLink = page.getByRole('link', { name: '打开授权页面' });
  await expect(verifyLink).toBeVisible();
  await assertHitArea(verifyLink, 'verify link');
});

test('desktop: no auto-select preserves current behavior', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-desktop') test.skip();
  let detailFetched = false;
  await page.route('**/api/commander/daemons/d1/sessions/s1', (route) => {
    detailFetched = true;
    return route.fulfill({ json: { session: { ID: 's1', Title: 'mocked' }, messages: [] } });
  });
  await page.goto('/commander/');
  await expect(page.getByRole('heading', { level: 1, name: 'Session' })).toBeVisible();
  expect(detailFetched).toBe(false);
  await page.getByRole('button', { name: /Fix commander session cache latency/ }).click();
  await expect(page.getByRole('heading', { level: 1, name: /Fix commander session cache latency/ })).toBeVisible();
  expect(detailFetched).toBe(true);
});

test('non-desktop: resizing to desktop while two overlays are stacked leaves no phantom history', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-mobile') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('about:blank');
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Files' }).click();
  expect(await page.evaluate(() => (history.state as { commanderOverlay?: string } | null)?.commanderOverlay)).toBe('files');
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  expect(await page.evaluate(() => (history.state as { commanderOverlay?: string } | null)?.commanderOverlay)).toBe('preview');
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(page.locator('.commander-shell')).toBeVisible();
  expect(await page.evaluate(() => (history.state as { commanderOverlay?: string } | null)?.commanderOverlay)).toBeFalsy();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByTestId('file-preview-sheet')).toHaveCount(0);
  await expect(page.getByTestId('drawer-right')).toHaveCount(0);
  const before = page.url();
  await page.goBack();
  expect(page.url()).not.toBe(before);
  expect(page.url()).toBe('about:blank');
});
```

- [ ] **Step 4: Add screenshot snapshots**

In the `desktop three-pane workbench is stable` test, leave the existing `commander-desktop.png` snapshot.

Add a new test at the end of the file:

```ts
test('non-desktop: chat default state baseline screenshot', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.goto('/commander/');
  await expect(page.getByLabel('输入提示词')).toBeEnabled();
  const name = testInfo.project.name === 'chromium-mobile' ? 'commander-mobile.png' : 'commander-tablet-portrait.png';
  await expect(page).toHaveScreenshot(name, { fullPage: true });
});

test('non-desktop: mobile sessions drawer + file preview screenshots', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-mobile') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Sessions' }).click();
  await expect(page.getByTestId('drawer-left')).toBeVisible();
  await expect(page).toHaveScreenshot('commander-mobile-sessions-drawer.png');
  await page.goBack();
  await page.getByRole('button', { name: 'Files' }).click();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  await expect(page.getByTestId('file-preview-sheet')).toBeVisible();
  await expect(page).toHaveScreenshot('commander-mobile-file-preview.png');
});
```

- [ ] **Step 5: Generate snapshots on first run**

Run from `internal/commanderhub/webapp/`:
- `npm run e2e -- --update-snapshots` (this should be done once locally so the baseline `.png`s land in `commander.spec.ts-snapshots/`).
- Stage the resulting `.png` files alongside the spec changes.

Expected: all tests pass, four new `*.png` snapshots produced.

- [ ] **Step 6: Final commit**

```bash
git add internal/commanderhub/webapp/src/e2e/commander.spec.ts \
        internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots
git commit -m "test(commander): add mobile + tablet-portrait e2e suite for issue #30

Adds tests 1-10 plus four screenshot baselines (mobile chat, tablet-
portrait chat, mobile sessions drawer, mobile file preview sheet).
A single assertHitArea helper enforces both height >= 44 and width
>= 44 for every interactive control checked in tests 6, 7, and 8.
Desktop tests are preserved unchanged.

Refs: #30"
```

---

## Self-Review (filled in by the plan author)

**Spec coverage:**

| Spec section / requirement                                | Task |
| --------------------------------------------------------- | ---- |
| Breakpoint = 1024px; delete old 900px block               | 9    |
| `100dvh` everywhere; safe-area padding                    | 9    |
| First-screen auto-select (loadTree + breakpoint trigger)  | 8    |
| Empty state when no session                               | 7, 8 |
| Browser back as drawer/sheet close + drainForBreakpoint   | 2, 7, 8 |
| Sessions drawer + DaemonSessionTree                       | 5, 7 |
| Files drawer + FileExplorerPanel `renderMode='sheet'`     | 4, 5, 7 |
| File preview sheet (stacked over Files)                   | 6, 7 |
| Single chat-header with mobile slots                      | 3, 7, 9 |
| Composer: 16px font, 44px min, safe-area padding          | 9    |
| Login mobile (100dvh, 44px button/link)                   | 9    |
| Touch-target rules for `.session-toggle`/etc.             | 9    |
| Playwright `chromium-tablet-portrait`                     | 10   |
| E2E tests 1–10 + `assertHitArea` helper                   | 11   |
| Four screenshot baselines                                 | 11   |
| Desktop preservation (no auto-select; existing tests)     | 8, 11 |

No spec requirement is unmapped.

**Placeholder scan:** No "TBD", "TODO", "implement later", or "similar to task N" placeholders remain — every step shows the exact code or command.

**Type consistency:** `OverlayID = 'sessions' | 'files' | 'preview'` used identically in tasks 2, 7, 8. `onPreview` payload type `{ preview, fullPath, displayPath }` matches in tasks 4, 6, 7. `mobileLeading` / `mobileTrailing` / `empty` props match between task 3 (definition) and task 7 (usage). `OverlayController` exposes `open`/`closeTop`/`reset`/`drainForBreakpoint`/`onPop`/`stackSnapshot` everywhere they are referenced.
