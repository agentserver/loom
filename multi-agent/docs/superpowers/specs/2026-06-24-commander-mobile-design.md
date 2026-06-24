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
  and tablet portrait widths (768, 834).
- Preserve the existing three-pane desktop experience on tablet landscape
  (1024–1180) and larger.
- Replace `display: none` for critical navigation with touch-accessible
  controls (drawers + sheet) so every desktop action remains reachable.
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
- `composer` adds `padding-bottom: max(10px, env(safe-area-inset-bottom))`
  and the textarea uses `font-size: 16px` on mobile to prevent iOS auto-zoom.

## Component Structure

### New components (under `internal/commanderhub/webapp/src/components/`)

1. **`MobileShell.tsx`** — wraps `<ChatWorkspace>` for `< 1024px` viewports.
   - Renders a 48px top "nav bar" with `[≡ Sessions]` (left), current session
     title (center, single-line truncation, fallback "Commander"), and
     `[≡ Files]` (right). Both buttons are at least 44×44px touch targets.
   - Hosts two `<MobileDrawer>` instances (Sessions left, Files right) and a
     single `<FilePreviewSheet>`.
   - Receives the same props that `CommanderApp` currently passes to the three
     panes, plus `selectedSession` for the header label.

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
     `FileExplorerPanel.tsx`, with `max-height: calc(100vh - 56px)`.
   - Closing returns to chat. Users reopen via `[Files]`.

### Modified components

4. **`FileExplorerPanel.tsx`** — add a `renderMode: 'inline' | 'sheet'` prop
   (default `'inline'` so desktop behavior is preserved) plus an
   `onPreview?: (preview: FileReadResult) => void` callback.
   - `inline` mode: current behavior. Selecting a file calls the internal
     state-based preview.
   - `sheet` mode: file tree only (no inline preview). Selecting a file
     invokes `onPreview(result)` instead of setting local preview state.
   - The `FilePreview` function stays exported and is consumed by both
     `FileExplorerPanel` (inline) and `FilePreviewSheet` (sheet).

5. **`CommanderApp.tsx`** — branch on the media query.
   - Uses `useMediaQuery('(max-width: 1023px)')` (a ~10-line inline hook).
   - When true: render `<MobileShell>` with the daemon tree, chat workspace,
     and file panel pieces it needs; otherwise render the existing three-pane
     `<div class="commander-shell">` JSX unchanged.
   - Owns drawer open/close state for Sessions and Files, plus the current
     `FilePreviewSheet` payload.

### Unchanged components

- `DaemonSessionTree`, `ChatWorkspace`, `StatusBadge`, `MessageRenderer`,
  the api client, and all server-side code are unaffected.

## Dependency

- Add `@radix-ui/react-dialog` (~14kb gzip). Chosen for accessibility, focus
  trap, and being unstyled (so existing plain-CSS aesthetic is preserved).
- Do not add `vaul`, Tailwind, or a CSS framework.

## Interaction Details (Mobile / Tablet portrait)

### Sessions drawer (left)
- Opens via `[Sessions]` header button.
- Header inside drawer: title "Sessions" + close (×) button.
- Body: full `<DaemonSessionTree>`, same props as desktop.
- Selecting a session calls `selectSession` and immediately closes the drawer
  (`setSessionsOpen(false)`), revealing chat with the new session loaded.

### Files drawer (right)
- Opens via `[Files]` header button.
- Header: title "Files" + close (×) button.
- Body: `<FileExplorerPanel renderMode="sheet" onPreview={…}>`. Only the file
  tree and any error message render here; selecting a directory expands in
  place, selecting a file:
  1. Triggers `onPreview(readResult)`.
  2. Closes the Files drawer (`setFilesOpen(false)`).
  3. Opens `<FilePreviewSheet>`.
  This avoids stacking two overlays.
- Copy-path button works as today and continues to use the existing
  clipboard logic.

### File preview sheet (full-viewport)
- Top bar (56px): `<` close button, truncated file path, "Copy path".
- Body: `<FilePreview preview={...} />` with mobile-tuned `max-height`.
- Close returns the user to chat. To browse another file, the user reopens
  the Files drawer via the header button. No FAB.

### Composer
- `padding: 10px 12px` and `padding-bottom: max(10px, env(safe-area-inset-bottom))`
  on `< 1024px`.
- `textarea { font-size: 16px }` on `< 1024px` to suppress iOS auto-zoom.
- No JavaScript visualViewport handling.

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

Add three new tests, each guarded so they run on `chromium-mobile` and
`chromium-tablet-portrait` only:

1. **`non-desktop: open sessions drawer, select session, send prompt`**
   - Default state: chat workspace visible; daemon-tree either absent or
     `aria-hidden`.
   - Click `[Sessions]` → drawer opens; daemon-tree visible inside drawer.
   - Click a session → drawer auto-closes; chat header shows the session
     title.
   - Type into composer and submit → fetch mock for the turn endpoint is
     called.

2. **`non-desktop: open files drawer, preview file, copy path`**
   - Click `[Files]` → file panel renders inside drawer; `go.mod` visible.
   - Click `go.mod` → Files drawer closes; file-preview-sheet opens with
     file content.
   - Click "Copy path" inside the sheet → clipboard contains the full path.
   - Click sheet close → returns to chat (no overlay left in DOM).

3. **`non-desktop: no horizontal overflow at 360/390/430`**
   - Loop `page.setViewportSize` over the three widths.
   - Assert `documentElement.scrollWidth <= clientWidth`.
   - Assert `[Sessions]` and `[Files]` header buttons have rendered hit
     areas ≥ 44×44px.

### Screenshots (snapshot tests)
- `commander-desktop.png` — kept (existing).
- `commander-mobile.png` — new, chromium-mobile, chat default state.
- `commander-tablet-portrait.png` — new, chromium-tablet-portrait, chat
  default state.
- Drawer / sheet open states are covered by DOM assertions (no extra
  snapshots to maintain).

### Vitest unit tests
- `MobileShell.test.tsx` (new): mock `matchMedia`, assert MobileShell renders
  at 1023px and desktop grid renders at 1024px.
- `MobileDrawer.test.tsx` (new): controlled open/close, ESC closes,
  overlay-click closes, `aria-modal="true"` present.
- `FileExplorerPanel.test.tsx` (extend): add a case that asserts
  `renderMode='sheet'` calls `onPreview` and skips the inline preview node.
- `DaemonSessionTree.test.tsx`, `ChatWorkspace.test.tsx`, existing
  `FileExplorerPanel.test.tsx` cases: unchanged.

## Acceptance Criteria Mapping

| Issue criterion                                                                 | Implementation                                                                  | Coverage                                                       |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| Usable at 360, 390, 430                                                         | Single-column chat + drawers                                                    | Viewport-loop e2e test + chromium-mobile screenshot @ 412      |
| Usable at 768, 834                                                              | tablet-portrait Playwright project                                              | Full mobile flows + screenshot @ 768                           |
| Usable at 1024–1180                                                             | Existing 3-pane grid (breakpoint moved to 1024)                                 | Existing desktop project remains green                         |
| Login, daemon/session navigation, chat, file access/copy without desktop view   | Sessions drawer, Files drawer, FilePreviewSheet                                 | Two new mobile flow tests                                       |
| Critical panes not `display: none`                                              | React-state-controlled Radix dialogs                                            | DOM assertions in flow tests + MobileDrawer unit test          |
| Playwright phone + tablet coverage                                              | New `chromium-tablet-portrait` project, retained `chromium-mobile`              | playwright.config diff                                          |
| Screenshot coverage for phone + tablet                                          | Two new baseline screenshots                                                    | Screenshot snapshot suite                                      |

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
- **Snapshot churn on different Playwright runners**: existing CI uses Linux
  Chromium; new snapshots are also Linux Chromium, matching the existing
  `*-chromium-desktop-linux.png` naming pattern.

## Out-of-scope follow-ups

- Native gesture (`swipe-to-dismiss`) on drawers/sheets.
- Persistent "recently opened files" or multi-file preview.
- Reduced-motion alternative animations beyond disabling the transform.
- Dark-mode tuning.
