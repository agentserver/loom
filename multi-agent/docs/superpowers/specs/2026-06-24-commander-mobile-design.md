# Commander Mobile & Tablet Layout — Design

- Issue: [agentserver/loom#30 — Commander: make /commander usable on phones and tablets](https://github.com/agentserver/loom/issues/30)
- Status: Approved (brainstorming)
- Date: 2026-06-24
- Owner: yzs15

## Problem

The `/commander` webapp ships a three-column desktop grid in
`internal/commanderhub/webapp/src/styles.css`:

```css
.commander-shell {
  grid-template-columns: minmax(280px, 360px) minmax(420px, 1fr) minmax(280px, 380px);
}
```

At `@media (max-width: 900px)` the layout collapses to a single column and
hides the daemon/session tree and the file panel with `display: none`. Existing
Playwright coverage only asserts that chat is visible and that there is no
horizontal overflow, so phone and common tablet portrait widths can render
without overflow but lose the daemon/session navigation and the file explorer.
The full commander workflow is therefore not usable on mobile or tablet.

## Goals

- Make `/commander` usable end-to-end on common phone widths (360, 390, 430)
  and tablet portrait widths (768, 834), including the login flow.
- Preserve the existing three-pane desktop experience on tablet landscape
  (1024–1180) and larger, including the current "no session selected by
  default" behavior on desktop.
- Replace `display: none` for critical navigation with touch-accessible
  controls (drawers + sheet) so every desktop action remains reachable, with
  ≥44×44px hit areas for every interactive control on mobile/tablet portrait.
- Avoid a first-screen dead end on mobile: never present an empty chat with
  no path to selection.
- Cover phone and tablet portrait with Playwright projects, screenshots, and
  flow assertions; harden the no-horizontal-overflow guard across multiple
  widths.

## Non-goals

- No changes to the underlying REST API or to daemon/session/file data models.
- No new CSS framework. Existing plain-CSS aesthetic is preserved.
- No visualViewport / JS-side keyboard handling beyond `font-size: 16px` on
  textarea and `env(safe-area-inset-bottom)` padding.
- No floating action button or persistent secondary navigation; the
  header-mounted segmented controls cover every needed entry point.

## Breakpoints & Layout Strategy

| Width            | Mode                              | Notes                                                                 |
| ---------------- | --------------------------------- | --------------------------------------------------------------------- |
| `< 1024px`       | Single-column chat + drawers      | Phone & tablet portrait. Sessions/Files exposed via header buttons.   |
| `>= 1024px`      | Existing three-pane grid          | Tablet landscape and desktop. Behavior unchanged.                     |

Concrete changes:

- Delete the current `@media (max-width: 900px) { display: none }` rules and
  replace with a single `1024px` breakpoint that swaps between `<MobileShell>`
  and the current desktop `<div class="commander-shell">` JSX.
- The active shell is chosen in `CommanderApp` via a small `useMediaQuery`
  hook (`(max-width: 1023px)`); selection / session detail / turn state remain
  hoisted in `CommanderApp` so switching shells does not lose state.
- Critical panels are never `display: none`; they are conditionally rendered
  by the active shell. The mobile shell exposes the same panels through Radix
  Dialog instances.
- `commander-shell`, drawer content, and file preview sheet use `100dvh`
  (not `100vh`) so mobile browser chrome resize does not clip content. The
  `login-shell` likewise uses `100dvh`.
- Drawer and sheet content boxes add `padding-top: env(safe-area-inset-top)`
  and `padding-bottom: env(safe-area-inset-bottom)`; the composer keeps its
  existing `padding-bottom: max(10px, env(safe-area-inset-bottom))`.
- Textarea uses `font-size: 16px` on `< 1024px` to prevent iOS auto-zoom.

### First-screen behavior (no empty-chat dead end)

- **Mobile / tablet portrait only.** Desktop keeps its current "no session
  selected by default" behavior.
- On the first `loadTree` resolution after mount, if and only if
  `isNonDesktop && selected == null`, `CommanderApp` auto-selects the
  first visible session it finds by scanning `tree.daemons` in order and,
  for each daemon, scanning `daemon.sessions` in order. A session is
  considered selectable when `DaemonSessionTree` would render it as a
  top-level (non-subagent) row — i.e. `session.origin !== 'subagent'` or
  the session has no resolvable `parent_id` inside this tree. If every
  daemon's top-level sessions are exhausted, the scan falls back to the
  first subagent session it sees so the user still lands on chat content.
  Only if no session of any kind exists does the empty state render.
- Subsequent `loadTree` refreshes do not auto-select again: an explicit
  user selection (or an explicit user clear) is never overridden. A
  `hasAutoSelectedRef` flag in `CommanderApp` guards this.
- If `isNonDesktop` and no daemon or no session is available, the mobile
  shell renders an empty state inside the chat area: a single line
  ("No sessions yet — open Sessions to pick one once a daemon appears")
  plus the Sessions drawer trigger; the composer renders disabled.
- `ChatWorkspace` gains an `empty?: boolean` prop. When `empty` is true the
  composer textarea and send button are forced `disabled` regardless of
  `turnState`. Mobile shell sets this when `selected == null`; desktop
  callers leave the prop unset, preserving today's behavior (composer is a
  visible textarea but submits are no-ops without a session — unchanged).

### Browser back as drawer/sheet close

The overlay history controller is **owned by `CommanderApp`**, not
`MobileShell`. This keeps a single source of truth that survives the
mobile↔desktop shell swap, and lets the matchMedia handler reach the stack
without prop-drilling refs.

`CommanderApp` instantiates one controller via a custom hook
`useOverlayHistory()` that returns:

```
{
  open(id: OverlayID): void,    // push history + record in stack
  closeTop(id: OverlayID): void,// history.back() if id is on top; no-op otherwise
  reset(): void,                // detach listener, clear stack — no history changes
  drainForBreakpoint(): void,   // breakpoint-only: history.go(-len) then clear
  onPop(handler): unsubscribe,  // subscribe to overlay-driven popstate events
}
```

Internals: a closure-scoped `stack: OverlayID[]` (not a React ref, so the
matchMedia handler reads the live array without needing a ref forwarded
across shell components), one `popstate` listener attached on first
`open()` and detached by `reset()`, and the same `{ commanderOverlay: id }`
payload on every `history.pushState`.

`MobileShell` calls `useOverlayHistory()` indirectly via props
(`openOverlay`, `closeOverlay`, and a subscription to `onPop` that flips
`sessionsOpen` / `filesOpen` / `previewPayload`).

**Opening an overlay**
1. `controller.open(id)` pushes `id` onto `stack`.
2. Calls `history.pushState({ commanderOverlay: id }, '')`. Each open
   pushes exactly one history entry (preview stacked on Files therefore
   pushes a second entry).
3. The shell flips the matching React state to render the overlay.

**Closing via UI (close button, overlay click, ESC, session selection)**
- `controller.closeTop(id)`: if `stack.at(-1) === id`, call
  `history.back()` and let the `popstate` handler do the React state
  update. This keeps the back stack in sync.
- If `stack` is empty (defensive: e.g. SSR-style remount), the shell
  closes the React state directly without touching history.

**Closing via browser Back**
- The single `popstate` listener pops the top of `stack` and emits an
  `onPop(id)` event so the shell flips the matching React state to
  closed. It does **not** inspect the new history state — the local stack
  is the source of truth for which overlay is on top.
- If the local stack is empty when `popstate` fires, the event is a
  non-overlay navigation; do nothing (the browser leaves commander).

**Edge cases**
- Page reload: history entries the controller pushed remain in browser
  history but the in-memory stack is empty on mount; the `popstate`
  handler ignores them per the rule above. Reload also means no listener
  is attached until the user opens an overlay again.
- Programmatic navigation away (logout link, address bar): no
  `history.go(...)` cleanup. `CommanderApp` calls `controller.reset()` on
  full unmount, which only detaches the listener and clears the stack;
  user-initiated navigation completes normally.
- Breakpoint crossing (`< 1024px → ≥ 1024px`, e.g. tablet rotated to
  landscape) is the **only** case that actively consumes pushed history
  entries. The matchMedia change handler in `CommanderApp` calls
  `controller.drainForBreakpoint()` **before** swapping shells: it
  captures `len = stack.length`, clears the stack, then calls
  `window.history.go(-len)` once if `len > 0`. Only after that does it
  flip the local `isNonDesktop` state, which triggers `MobileShell`
  unmount + desktop shell mount. The user never has to press Back to
  consume phantom entries.

## Component Structure

### New components (under `internal/commanderhub/webapp/src/components/`)

1. **`MobileShell.tsx`** — wraps `<ChatWorkspace>` for `< 1024px` viewports.
   - **No separate nav bar.** Sessions / Files triggers are injected into the
     existing chat header so the screen retains a single header band.
   - Renders `<ChatWorkspace mobileLeading={<SessionsButton/>}
     mobileTrailing={<FilesButton/>} empty={hasNoSelection}>`.
   - Hosts two `<MobileDrawer>` instances (Sessions left, Files right) and a
     single `<FilePreviewSheet>` stacked over the Files drawer.
   - **Does not own** the overlay history controller; receives
     `openOverlay`, `closeOverlay`, and a subscription to `onPop` as props
     from `CommanderApp` so the controller survives the shell swap on
     breakpoint crossings.

2. **`MobileDrawer.tsx`** — controlled Radix Dialog wrapper.
   - Props: `side: 'left' | 'right'`, `open`, `onOpenChange`, `title`,
     `children`.
   - Renders `Dialog.Root` + `Dialog.Portal` + `Dialog.Overlay` +
     `Dialog.Content`. Content uses `transform: translateX(...)` with a
     200ms `ease-out` transition and respects
     `@media (prefers-reduced-motion: reduce)`.
   - Width: `min(320px, 88vw)` (left) / `min(360px, 92vw)` (right).
   - Provides ESC, overlay-click, focus trap, and `aria-modal` via Radix.

3. **`FilePreviewSheet.tsx`** — full-viewport Radix Dialog for previewing one
   file on mobile, stacked over the Files drawer.
   - Props: `open`, `onOpenChange`, `payload: { preview: FileReadResult,
     fullPath: string, displayPath: string } | null`.
   - Top bar: back/close affordance (44×44), `displayPath` shown
     single-line-truncated, "Copy path" button (44×44) that writes
     `payload.fullPath` to the clipboard.
   - Body: reuses the existing `FilePreview` function from
     `FileExplorerPanel.tsx`, with `max-height: calc(100dvh - 56px)`.
   - Closing (button, ESC, overlay click, browser back) returns to the
     **Files drawer**, which remains mounted underneath with its expanded
     directories and scroll position preserved.

### Modified components

4. **`FileExplorerPanel.tsx`** — add a `renderMode: 'inline' | 'sheet'` prop
   (default `'inline'` so desktop behavior is preserved) plus an
   `onPreview?: (payload: { preview: FileReadResult, fullPath: string,
   displayPath: string }) => void` callback.
   - `inline` mode: current behavior. Selecting a file calls the internal
     state-based preview.
   - `sheet` mode: file tree only (no inline preview). Selecting a file
     invokes `onPreview({ preview, fullPath, displayPath })`, computing
     `fullPath` via the existing `fullPath(root, entry.path)` helper and
     using `entry.path` for `displayPath`. The tree's `directories` /
     scroll state is left untouched (state lives in the same component
     instance, so it persists across overlay open/close).
   - The `FilePreview` function stays exported and is consumed by both
     `FileExplorerPanel` (inline) and `FilePreviewSheet` (sheet).

5. **`ChatWorkspace.tsx`** — add optional mobile slots and empty state.
   - New optional props: `mobileLeading?: ReactNode`,
     `mobileTrailing?: ReactNode`, `empty?: boolean`.
   - When `mobileLeading` / `mobileTrailing` are provided, they render inside
     the existing `.chat-header` as flex children flanking title/status —
     no second header.
   - When `empty` is true, the message list shows a centered placeholder and
     the composer textarea + send button are forced `disabled`.

6. **`CommanderApp.tsx`** — branch on the media query, hoist drawer state,
   and auto-select on mobile only.
   - Uses `useMediaQuery('(max-width: 1023px)')` (a ~10-line inline hook).
   - Owns `useOverlayHistory()` (see Browser Back section). The
     `useMediaQuery` matchMedia change handler calls
     `controller.drainForBreakpoint()` before flipping `isNonDesktop`
     from true to false, so the desktop shell never inherits phantom
     history entries. Full-unmount paths call `controller.reset()`
     instead, which leaves history untouched.
   - Auto-select rule (mobile-only, one-shot): keep a
     `hasAutoSelectedRef = useRef(false)`. After `loadTree` resolves, if
     `!hasAutoSelectedRef.current && isNonDesktop && selected == null`,
     scan `tree.daemons` per the First-screen behavior section to find the
     first selectable session (top-level first, subagent as fallback). If
     one is found, call `selectSession(...)` and set the ref to true.
     Resetting the ref happens only on full logout (`authRequired`
     transition from false → true), so a deliberate clear is respected.
   - When `useMediaQuery` is true: render `<MobileShell>`; otherwise render
     the existing three-pane `<div class="commander-shell">` JSX unchanged.
   - Owns drawer open/close state for Sessions and Files, plus the current
     `FilePreviewSheet` payload (the `{ preview, fullPath, displayPath }`
     object).

### Unchanged components

- `DaemonSessionTree`, `StatusBadge`, `MessageRenderer`, the api client, and
  all server-side code are unaffected.

## Dependency

- Add `@radix-ui/react-dialog` (~14kb gzip). Chosen for accessibility, focus
  trap, and being unstyled (so existing plain-CSS aesthetic is preserved).
- Do not add `vaul`, Tailwind, or a CSS framework.

## Interaction Details (Mobile / Tablet portrait)

### Header (single, embedded in chat-header)
- Layout: `[Sessions ≡] · title/status (flex 1) · [Files ≡]`.
- Touch targets: every interactive control on `< 1024px` is at least
  44×44px (Sessions / Files triggers, drawer close buttons, drawer
  session/file rows, session-toggle, file-copy-button, sheet close).
- Existing `chat-header` ellipsis on title and working dir is preserved.

### Sessions drawer (left)
- Opens via `[Sessions]` header button.
- Header inside drawer: title "Sessions" + close (×) button (44×44).
- Body: full `<DaemonSessionTree>`, same props as desktop.
- Selecting a session calls `selectSession` and immediately closes the drawer
  (via `history.back()` so the back stack stays consistent), revealing chat
  with the new session loaded.

### Files drawer (right)
- Opens via `[Files]` header button.
- Header: title "Files" + close (×) button (44×44).
- Body: `<FileExplorerPanel renderMode="sheet" onPreview={…}>`. Only the file
  tree and any error message render here; selecting a directory expands in
  place. Selecting a file calls
  `onPreview({ preview, fullPath, displayPath })` (signature defined in the
  Component Structure section) which opens `<FilePreviewSheet>` **stacked on
  top of the Files drawer**; the drawer stays mounted so its expanded
  directories and scroll position survive.
- Closing the preview sheet returns the user to the Files drawer in its
  prior state, ready to pick another file.
- Copy-path button works as today and continues to use the existing
  clipboard logic.

### File preview sheet (full-viewport)
- Top bar (56px): `<` close button (44×44), truncated file path, "Copy path"
  (44×44).
- Body: `<FilePreview preview={...} />` with `max-height: calc(100dvh - 56px)`.
- Close (button, ESC, browser back) returns to the Files drawer (still
  mounted underneath).

### Composer
- `padding: 10px 12px` and `padding-bottom: max(10px, env(safe-area-inset-bottom))`
  on `< 1024px`.
- `textarea { font-size: 16px; min-height: 44px }` on `< 1024px` to suppress
  iOS auto-zoom and meet touch-target height.
- Send button: `min-height: 44px; min-width: 44px` on `< 1024px`.
- No JavaScript visualViewport handling.

### Login (auth required) on mobile
- `login-shell` uses `100dvh` instead of `100vh` so mobile browser chrome
  resize does not clip.
- On `< 1024px`, login button and the verification-URL link bump
  `min-height` from 38px to 44px and add explicit `min-width: 44px`.
- The login panel keeps its existing `width: min(360px, calc(100vw - 32px))`
  rule.
- No structural rewrite of the login flow — the device-code "open
  authorization page" link still opens in a new tab; the polling logic is
  unchanged.

### Touch target rules (mobile / tablet portrait)
A single CSS block under `@media (max-width: 1023px)` brings every
interactive control to ≥44×44px:
- `.session-toggle`, `.session-toggle-spacer { width: 44px; min-width: 44px;
  height: 44px }` (previously 24px).
- `.session-row { min-height: 44px }`.
- `.file-row { min-height: 44px }`.
- `.file-copy-button { width: 44px; min-width: 44px; height: 44px }`
  (previously 30px).
- All drawer / sheet close buttons explicitly sized 44×44.

### Horizontal-overflow guard
- Existing ellipsis rules on session/daemon titles, working dir, file names,
  and chat header remain.
- E2E asserts `documentElement.scrollWidth <= clientWidth` at 360 / 390 / 430
  (loop in a single test) and within mobile + tablet-portrait projects.

## Test Strategy

### Playwright `playwright.config.ts`
Replace the current two projects with three:

| Project name              | Viewport                          |
| ------------------------- | --------------------------------- |
| `chromium-desktop`        | 1440×960 (unchanged)              |
| `chromium-tablet-portrait`| 768×1024 (new)                    |
| `chromium-mobile`         | Pixel 7 device descriptor (412×915, unchanged) |

### E2E tests `src/e2e/commander.spec.ts`

Keep desktop-only tests intact:
- `desktop three-pane workbench is stable` — unchanged.
- `desktop panes own vertical scrolling and chat opens at bottom` — unchanged.

Remove:
- `mobile prioritizes chat without horizontal overflow` — superseded.

Add new tests, each guarded so they run on `chromium-mobile` and
`chromium-tablet-portrait` only unless stated otherwise:

1. **`non-desktop: auto-selects first session and chat is live`**
   - Tree mock returns one daemon with one session.
   - Assert chat header shows that session's title without any user click.
   - Composer textarea is enabled (turnState idle).

2. **`non-desktop: empty tree renders disabled composer + hint`**
   - Tree mock returns `{ daemons: [] }`.
   - Assert empty-state hint text visible inside chat area.
   - Assert composer textarea + send button both have the `disabled`
     attribute.

3. **`non-desktop: open sessions drawer, select session, send prompt`**
   - Click `[Sessions]` → drawer opens; daemon-tree visible inside drawer.
   - Click a session → drawer auto-closes; chat header shows the session
     title.
   - Type into composer and submit → fetch mock for the turn endpoint is
     called.

4. **`non-desktop: open files drawer, preview file, then preview a second`**
   - Tree/file mocks return `root: '/root/project'` and `go.mod`,
     `README.md` entries.
   - Click `[Files]` → file panel renders inside drawer; `go.mod` visible.
   - Click `go.mod` → preview sheet opens stacked over drawer.
   - Click "Copy path" inside the sheet → assert clipboard value equals
     `/root/project/go.mod` exactly (proves fullPath, not bare filename, is
     copied).
   - Close sheet → Files drawer is still open, still showing the file list.
   - Click a second file (`README.md`) → preview sheet opens with that
     file's content, no extra drawer reopen needed.

5. **`non-desktop: browser back closes overlays in stack order`**
   - Open Sessions drawer → `page.goBack()` → drawer closed, chat visible,
     no overlay in DOM.
   - Open Files drawer, then a file → both overlays mounted; `page.goBack()`
     closes preview sheet but leaves Files drawer; second `goBack` closes
     Files drawer.

6. **`non-desktop: no horizontal overflow at 360/390/430 and 834`**
   - Loop `page.setViewportSize` over `[360, 390, 430, 834]` (phone trio
     + the 834 tablet portrait width called out in acceptance criteria).
   - Assert `documentElement.scrollWidth <= clientWidth`.
   - Assert `[Sessions]` and `[Files]` header buttons have rendered hit
     areas ≥ 44×44px.

7. **`non-desktop: drawer interactive controls meet 44px hit area`**
   - Open Sessions drawer; for each `.session-row`, `.session-toggle`, and
     close (×) button, assert `boundingBox().height >= 44`.
   - Open Files drawer; for each `.file-row` and `.file-copy-button`,
     assert `boundingBox().height >= 44 && width >= 44`.

8. **`non-desktop: login screen is touch-friendly`** (auth-required path)
   - Mock `/api/commander/tree` to return 401 so `authRequired` is true.
   - Assert login button and verify-URL anchor both have
     `boundingBox().height >= 44`.
   - Assert `documentElement.scrollWidth <= clientWidth` at the project
     viewport.
   - Mock `POST /api/commander/login` to return a fake `login_id` +
     `verification_uri_complete`; click button; assert the verify-URL link
     becomes visible. (No real OAuth round-trip.)

9. **`desktop: no auto-select preserves current behavior`** (chromium-desktop only)
   - Tree mock returns one daemon with one session. Spy on
     `GET /api/commander/daemons/d1/sessions/s1`.
   - Assert chat header renders with its current no-selection fallback
     (title text `Session`, working-dir line empty) — matches today's
     desktop default.
   - Assert the session detail endpoint has **not** been requested.
   - Click the session in the daemon tree → chat header now shows the
     mock session title and the detail endpoint has been requested.

10. **`non-desktop: resizing to desktop while two overlays are stacked
    leaves no phantom history`** (chromium-mobile only; uses
    `page.setViewportSize`)
    - Setup: `page.goto('about:blank')` first to establish a known
      previous entry, then `page.goto('/commander/')`. This guarantees
      the browser has a non-commander entry to navigate back to.
    - Auto-selected first session is visible.
    - Click `[Files]` → Files drawer open. Assert
      `history.state?.commanderOverlay === 'files'` (controller push
      observed).
    - Click `go.mod` in the file tree → preview sheet opens stacked over
      Files. Assert `history.state?.commanderOverlay === 'preview'` (a
      second push).
    - **Why this pair of overlays:** Sessions and Files share the same
      header trigger row; opening one closes / blocks the other (modal
      focus trap), so a real user can never reach a two-entry stack
      that way. The legitimate two-entry stack is `files` → `preview`.
    - Resize viewport to `1280×900` → matchMedia change handler runs
      `drainForBreakpoint()` (one `history.go(-2)`) before
      `MobileShell` unmounts. Assert
      `history.state?.commanderOverlay == null` (no overlay token
      lives on the current entry; the controller drained the stack).
    - Trigger one `page.goBack()` → assert `page.url() === 'about:blank'`,
      proving Back reaches the sentinel page in a single step rather
      than first popping invisible overlay entries.

### Screenshots (snapshot tests)
- `commander-desktop.png` — kept (existing).
- `commander-mobile.png` — new, chromium-mobile, chat default state with
  auto-selected first session.
- `commander-tablet-portrait.png` — new, chromium-tablet-portrait, chat
  default state.
- `commander-mobile-sessions-drawer.png` — new, chromium-mobile, Sessions
  drawer open.
- `commander-mobile-file-preview.png` — new, chromium-mobile, file preview
  sheet open (stacked over Files drawer).
- Files-drawer-only state on mobile and per-state screenshots on
  tablet-portrait are skipped: Files drawer mirrors Sessions drawer
  structurally, and DOM assertions plus the two mobile drawer/sheet
  snapshots already cover the highest-risk visual regressions.

### Vitest unit tests
- `MobileShell.test.tsx` (new): mock `matchMedia`, assert MobileShell renders
  at 1023px and desktop grid renders at 1024px; auto-select scans across
  daemons (case A: first daemon has no sessions, second daemon has one →
  second daemon's session is selected; case B: only subagent sessions exist
  → fallback selects the first subagent; case C: nothing → empty state);
  empty-tree state disables composer.
- `MobileDrawer.test.tsx` (new): controlled open/close, ESC closes,
  overlay-click closes, `aria-modal="true"` present; `history.pushState`
  is called on open and `popstate` triggers close.
- `FileExplorerPanel.test.tsx` (extend): add a case that asserts
  `renderMode='sheet'` calls `onPreview` and skips the inline preview node;
  add a case that asserts the directory expansion state is preserved across
  open/close of an external sheet (re-render).
- `ChatWorkspace.test.tsx` (extend): `mobileLeading` / `mobileTrailing`
  slots render where expected; `empty` prop forces composer disabled.
- `DaemonSessionTree.test.tsx`, existing `FileExplorerPanel.test.tsx`
  cases: unchanged.

## Acceptance Criteria Mapping

| Issue criterion                                                                 | Implementation                                                                  | Coverage                                                       |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| Usable at 360, 390, 430                                                         | Single-column chat + drawers                                                    | Viewport-loop e2e test + chromium-mobile screenshot @ 412      |
| Usable at 768, 834                                                              | tablet-portrait Playwright project (768 baseline) + 834 width covered by viewport-loop in test 6 | Full mobile flows + screenshot @ 768 + overflow/hit-area assertion @ 834 |
| Usable at 1024–1180                                                             | Existing 3-pane grid (breakpoint moved to 1024)                                 | Existing desktop project remains green                         |
| Login, daemon/session navigation, chat, file access/copy without desktop view   | Auth-required mobile path; Sessions drawer; Files drawer + persistent state; FilePreviewSheet | Login mobile flow (test 8) + Sessions flow (test 3) + Files multi-file flow (test 4) |
| Critical panes not `display: none`                                              | React-state-controlled Radix dialogs                                            | DOM assertions in flow tests + MobileDrawer unit test          |
| Touch-friendly controls (pane switching, session selection, send, copy/open)    | 44×44px hit-area rule for header triggers, drawer rows, copy buttons, login    | Test 7 (drawer hit areas) + test 6 (header hit areas) + test 8 (login hit areas) |
| Composer usable with software keyboard, no horizontal overflow                  | `100dvh`, safe-area padding, `font-size: 16px`, ellipsis preserved              | Test 6 viewport-loop overflow at 360/390/430                  |
| Playwright phone + tablet coverage                                              | New `chromium-tablet-portrait` project, retained `chromium-mobile`              | playwright.config diff                                          |
| Screenshot coverage for phone + tablet                                          | Four new baseline screenshots (chat default × 2, mobile sessions drawer, mobile file preview sheet) | Screenshot snapshot suite                                      |

## Risks

- **Radix focus trap** can clash with chat composer focus. Mitigation: ensure
  the composer is not mounted inside the drawer; drawers are siblings of the
  composer, so focus returns to the trigger on close.
- **State persistence on viewport rotation**: switching shells must not drop
  `selected` or `sessionDetail`. Mitigation: state lives in `CommanderApp`;
  the media query only swaps shells, not data.
- **iOS keyboard behavior**: relying on the browser's default viewport resize
  is intentional. If reports surface, follow up with a visualViewport hook
  later. Out of scope here.
- **Browser back history pollution**: pushing history entries for each
  overlay can confuse users who use back to leave commander. Mitigation:
  only push when an overlay opens; pop on overlay close; on hard navigation
  (page unload) skip; ensure tests assert the exact stack length so the
  invariant doesn't drift.
- **Auto-select first session surprises returning users**: a user who
  intentionally has no selection may be jumped into one. Mitigation: only
  auto-select on the first `loadTree` after mount and only if `selected` is
  null; subsequent refreshes do not override an explicit clear.
- **Snapshot churn on different Playwright runners**: existing CI uses Linux
  Chromium; new snapshots are also Linux Chromium, matching the existing
  `*-chromium-desktop-linux.png` naming pattern.

## Out-of-scope follow-ups

- Native gesture (`swipe-to-dismiss`) on drawers/sheets.
- Persistent "recently opened files" or multi-file preview.
- Reduced-motion alternative animations beyond disabling the transform.
- Dark-mode tuning.
