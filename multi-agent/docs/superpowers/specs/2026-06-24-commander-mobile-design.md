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
  (1024–1180) and larger.
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

- On mount, after `loadTree` resolves and on every tree refresh while
  `selected` is null, `CommanderApp` auto-selects the first available session
  it can find (first daemon's first non-subagent session).
- If no daemon or no session is available, mobile shell renders an empty
  state inside the chat area: a single line ("No sessions yet — open Sessions
  to pick one once a daemon appears") plus the Sessions drawer trigger; the
  composer renders disabled.
- `ChatWorkspace` gains an `empty?: boolean` prop. When `empty` is true the
  composer textarea and send button are forced `disabled` regardless of
  `turnState`. Desktop benefits from the same guard.

### Browser back as drawer/sheet close

- When a drawer or the file preview sheet opens, `MobileShell` calls
  `history.pushState({ commanderOverlay: <id> }, '')` and listens for
  `popstate`. A `popstate` whose old state was a commander overlay closes the
  matching overlay; explicit close (close button, overlay click, ESC) calls
  `history.back()` to keep the stack consistent.
- Only one overlay-back entry is pushed per overlay open. The file preview
  sheet sitting on top of the Files drawer pushes a second entry, so back
  pops the sheet first, then the drawer.

## Component Structure

### New components (under `internal/commanderhub/webapp/src/components/`)

1. **`MobileShell.tsx`** — wraps `<ChatWorkspace>` for `< 1024px` viewports.
   - **No separate nav bar.** Sessions / Files triggers are injected into the
     existing chat header so the screen retains a single header band.
   - Renders `<ChatWorkspace mobileLeading={<SessionsButton/>}
     mobileTrailing={<FilesButton/>} empty={hasNoSelection}>`.
   - Hosts two `<MobileDrawer>` instances (Sessions left, Files right) and a
     single `<FilePreviewSheet>` stacked over the Files drawer.
   - Owns the `history.pushState` / `popstate` bridging for back-to-close.

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
   file on mobile.
   - Top bar: back affordance, file path (single-line truncation), "Copy path"
     button.
   - Body: reuses the existing `FilePreview` function from
     `FileExplorerPanel.tsx`, with `max-height: calc(100dvh - 56px)`.
   - Closing returns to chat. Users reopen via `[Files]`.

### Modified components

4. **`FileExplorerPanel.tsx`** — add a `renderMode: 'inline' | 'sheet'` prop
   (default `'inline'` so desktop behavior is preserved) plus an
   `onPreview?: (preview: FileReadResult) => void` callback.
   - `inline` mode: current behavior. Selecting a file calls the internal
     state-based preview.
   - `sheet` mode: file tree only (no inline preview). Selecting a file
     invokes `onPreview(result)` while leaving the tree's `directories` /
     scroll state untouched (state lives in the same component instance, so
     it persists across overlay open/close).
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
   and auto-select.
   - Uses `useMediaQuery('(max-width: 1023px)')` (a ~10-line inline hook).
   - After each `loadTree` resolution, if `selected` is null and any session
     exists, calls `selectSession` with the first daemon's first non-subagent
     session. Honors a user-cleared selection (`null` after they manually
     navigate) only on initial load; subsequent refreshes leave the explicit
     selection alone.
   - When `useMediaQuery` is true: render `<MobileShell>`; otherwise render
     the existing three-pane `<div class="commander-shell">` JSX unchanged.
   - Owns drawer open/close state for Sessions and Files, plus the current
     `FilePreviewSheet` payload.

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
  place. Selecting a file calls `onPreview(readResult)` which opens
  `<FilePreviewSheet>` **stacked on top of the Files drawer**; the drawer
  stays mounted so its expanded directories and scroll position survive.
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
   - Click `[Files]` → file panel renders inside drawer; `go.mod` visible.
   - Click `go.mod` → preview sheet opens stacked over drawer.
   - Click "Copy path" inside the sheet → clipboard contains the full path.
   - Close sheet → Files drawer is still open, still showing the file list.
   - Click a second file (`README.md`) → preview sheet opens with that
     file's content, no extra drawer reopen needed.

5. **`non-desktop: browser back closes overlays in stack order`**
   - Open Sessions drawer → `page.goBack()` → drawer closed, chat visible,
     no overlay in DOM.
   - Open Files drawer, then a file → both overlays mounted; `page.goBack()`
     closes preview sheet but leaves Files drawer; second `goBack` closes
     Files drawer.

6. **`non-desktop: no horizontal overflow at 360/390/430`**
   - Loop `page.setViewportSize` over the three widths.
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
  at 1023px and desktop grid renders at 1024px; auto-select-first-session
  behavior; empty-tree state disables composer.
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
| Usable at 768, 834                                                              | tablet-portrait Playwright project                                              | Full mobile flows + screenshot @ 768                           |
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
