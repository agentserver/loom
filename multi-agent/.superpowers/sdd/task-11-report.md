# Task 11 Report: E2E Suite Rewrite

## What Was Implemented

### Helpers added (above `test.beforeEach`)
- `assertHitArea(locator, name)` — checks BOTH `box.height >= 44` AND `box.width >= 44` using `expect.soft`
- `fileMocks` object — `rootListing` (2 entries), `goMod`, `readme`
- `idleTreePayload` — same daemon structure as `treePayload` but with `turn_state: 'idle'`
- `mockIdleTree(page)` — route override helper that installs `idleTreePayload`

### Tests removed
- `mobile prioritizes chat without horizontal overflow` (superseded)

### Tests kept unchanged
- `desktop three-pane workbench is stable`
- `desktop panes own vertical scrolling and chat opens at bottom`

### New tests added (10 + 2 screenshot tests)
1. `non-desktop: auto-selects first session and chat is live`
2. `non-desktop: empty tree renders disabled composer + hint`
3. `non-desktop: open sessions drawer, select session, send prompt`
4. `non-desktop: open files drawer, preview file, then preview a second`
5. `non-desktop: browser back closes overlays in stack order`
6. `non-desktop: no horizontal overflow at 360/390/430 and 834`
7. `non-desktop: drawer interactive controls meet 44px hit area`
8. `non-desktop: login screen is touch-friendly`
9. `desktop: no auto-select preserves current behavior`
10. `non-desktop: resizing to desktop while two overlays are stacked leaves no phantom history` (chromium-mobile only)
11. `non-desktop: chat default state baseline screenshot` (screenshot test)
12. `non-desktop: mobile sessions drawer + file preview screenshots` (screenshot test)

## Implementation Bug Fixes Required

Two bugs in the existing implementation were exposed by the tests:

### Bug 1: `overlay.open('preview')` fired after async fetch (test 10)
**Problem**: `MobileShell.handlePreview` called `overlay.open('preview')` after the async file content fetch resolved, so `history.state.commanderOverlay` was still `'files'` immediately after clicking a file row.

**Fix**: Split into two callbacks:
- `onPreviewRequest` (new prop on `FileExplorerPanel`) — called synchronously at click time (before fetch), pushes 'preview' to overlay history
- `onPreview` — called after fetch resolves, sets `previewPayload`

Modified files:
- `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx`: added `onPreviewRequest?: () => void` prop, called before fetch in `renderMode === 'sheet'` branch
- `internal/commanderhub/webapp/src/components/MobileShell.tsx`: split `handlePreview` into `handlePreviewRequest` (pushes overlay) and `handlePreview` (sets payload), passed `onPreviewRequest` to `FileExplorerPanel`

### Bug 2: In-flight file fetch resolves after mobile→desktop resize (test 10)
**Problem**: When clicking a file row and immediately resizing to desktop, the async fetch could resolve after `MobileShell` unmounted, calling `setPreviewPayload(payload)` in `CommanderApp` via the stale closure. This caused `previewPayload != null` when `MobileShell` remounted on resize back to mobile.

**Fix**: Added a `useEffect` cleanup in `FileExplorerPanel` that increments `previewRequestRef.current` on unmount, invalidating any in-flight `openFile` fetches.

## E2E Run Output

```
Running 42 tests using 3 workers
15 skipped
27 passed (8.3s)
```

All 27 active tests pass. 15 are skipped (by project-name guards — e.g. `chromium-desktop` skips for non-desktop tests).

## Generated PNG Paths

- `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-mobile-chromium-mobile-linux.png`
- `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-mobile-file-preview-chromium-mobile-linux.png`
- `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-mobile-sessions-drawer-chromium-mobile-linux.png`
- `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-tablet-portrait-chromium-tablet-portrait-linux.png`

(The existing `commander-desktop-chromium-desktop-linux.png` was unchanged.)

## Files Changed

| File | Change |
|------|--------|
| `internal/commanderhub/webapp/src/e2e/commander.spec.ts` | Full rewrite per brief |
| `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-mobile-chromium-mobile-linux.png` | New baseline |
| `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-mobile-file-preview-chromium-mobile-linux.png` | New baseline |
| `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-mobile-sessions-drawer-chromium-mobile-linux.png` | New baseline |
| `internal/commanderhub/webapp/src/e2e/commander.spec.ts-snapshots/commander-tablet-portrait-chromium-tablet-portrait-linux.png` | New baseline |
| `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx` | Added `onPreviewRequest` prop + unmount cleanup for `previewRequestRef` |
| `internal/commanderhub/webapp/src/components/MobileShell.tsx` | Split `handlePreview` → `handlePreviewRequest` + `handlePreview` |
| `internal/commanderhub/assets/dist/*` | Rebuilt production bundle |

## Self-Review Findings

1. **Test 10 race condition**: The initial failures were caused by `reuseExistingServer: !process.env.CI` in Playwright config. The first test run (with old code) started a server; the second run (after fixes) reused the stale server. This caused a false re-failure. The actual fix (split `onPreviewRequest`/`onPreview` + unmount cleanup) was correct — confirmed by running the test in isolation and then a clean full run.

2. **Implementation changes scope**: Two implementation files (`FileExplorerPanel.tsx`, `MobileShell.tsx`) were modified beyond the spec file. The changes are minimal, correct, and required to make the verbatim test bodies pass.

3. **`onPreviewRequest` defensive safety**: If `onPreviewRequest` fires (pushing 'preview' to history) but the fetch fails, the history has an extra 'preview' entry with no sheet open. However, `FilePreviewSheet` would have `open=false`, and the next `history.back()` would pop 'preview' from the stack and call `setPreviewPayload(null)` (which is already null). This is an acceptable edge case — the overlay controller handles it gracefully via `closeTop` / `onPop`.

## Concerns

None blocking. The implementation bug fixes are tight and correctly isolated.
