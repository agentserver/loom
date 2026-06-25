# Task 8 Report: CommanderApp Wiring

## What Was Implemented

### Files Changed
- **Modified**: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- **Created**: `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx`

### 7 Key Effects/Helpers

1. **`pickAutoSession(tree)`** — Mirrors `DaemonSessionTree.buildCrossDaemonTree` using `effectiveOwner`/`parentOwnerFor`/`ownerKey` helpers (copied from DaemonSessionTree). Builds a `isChildKey` Set keyed by owner-namespaced keys (never flat `Set<session_id>`). Returns `{ daemonID, sessionID }` of the first non-child root session, or null.

2. **`useMediaQuery('(max-width: 1023px)', { onChange })`** — The `onChange` callback fires synchronously BEFORE `setMatches` per the hook's design. When `matches` becomes `false` (desktop transition), it executes in required order: `overlay.drainForBreakpoint()` then `setSessionsOpen(false); setFilesOpen(false); setPreviewPayload(null)`.

3. **Hoisted overlay state** — `[sessionsOpen, setSessionsOpen]`, `[filesOpen, setFilesOpen]`, `[previewPayload, setPreviewPayload]` as `useState` in CommanderApp. Passed as all 13 props to `<MobileShell>`.

4. **`useOverlayHistory()` controller** — One instance per CommanderApp lifetime via `const overlay = useOverlayHistory()`.

5. **Auto-select `useEffect([isNonDesktop, tree, selected])`** — Guards: `hasAutoSelectedRef.current`, `!isNonDesktop`, `selected != null`, `!tree`. On success sets ref to true and calls `setSelected(pick)`. Covers desktop→mobile rotation while tree is loaded.

6. **Logout reset effect** — Tracks `prevAuthRequiredRef` and resets `hasAutoSelectedRef.current = false` on `authRequired` false→true transition (full logout).

7. **Unmount cleanup `useEffect(() => () => overlay.reset(), [overlay])`** — Detaches the popstate listener only; no history mutation.

### onChange Ordering Invariant
Per spec: `onChange` sees `false` (desktop) → calls `overlay.drainForBreakpoint()` FIRST (history.go(-len)), THEN flushes React state flags. This runs before `setMatches` so React's next render observes the post-drain world. A `useEffect([isNonDesktop])` would be too late.

### Import Edits
- Added `SessionRow` to type import from `./api/types`
- Added `MobileShell` import from `./components/MobileShell`
- Added `type FilePreviewPayload` import from `./components/FilePreviewSheet`
- Added `useMediaQuery` from `./hooks/useMediaQuery`
- Added `useOverlayHistory` from `./hooks/useOverlayHistory`

### Render Branch
`if (isNonDesktop) return <MobileShell {...all 13 props} />;` — existing three-pane JSX unchanged in the `else` path.

## TDD Evidence

### RED Phase (7 mobile tests fail before implementation)
```
Tests  5 failed | 2 passed (7)
- ✓ desktop viewport renders three-pane layout, not MobileShell (already passing)
- × mobile viewport renders MobileShell with commander-shell-mobile class
- × auto-selects the first session on mobile mount
- × auto-select is one-shot and does not fire again on subsequent tree loads
- × rotating from desktop to mobile auto-selects the first session
- × drainForBreakpoint is called when transitioning from mobile to desktop
- ✓ no session is auto-selected on desktop viewport (already passing)
```

### GREEN Phase (all tests pass after implementation)
```
Tests  7 passed (7)
- ✓ desktop viewport renders three-pane layout, not MobileShell
- ✓ mobile viewport renders MobileShell with commander-shell-mobile class
- ✓ auto-selects the first session on mobile mount
- ✓ auto-select is one-shot and does not fire again on subsequent tree loads
- ✓ rotating from desktop to mobile auto-selects the first session
- ✓ drainForBreakpoint is called when transitioning from mobile to desktop
- ✓ no session is auto-selected on desktop viewport
```

## Test Results

### `npm test -- src/CommanderApp.test.tsx src/CommanderApp.mobile.test.tsx`
```
Test Files  2 passed (2)
     Tests  12 passed (12)
  Duration  1.54s
```

### `npm run build`
```
✓ built in 214ms
../assets/dist/index.html                   0.41 kB │ gzip:   0.27 kB
../assets/dist/assets/index-4w_A8AF5.css    5.78 kB │ gzip:   1.70 kB
../assets/dist/assets/index-BVf7--rc.js   405.28 kB │ gzip: 126.07 kB
```

### Wider suite: `npm test`
```
Test Files  11 passed (11)
     Tests  65 passed (65)
  Duration  1.68s
```

## Files Changed
- Modified: `internal/commanderhub/webapp/src/CommanderApp.tsx` (+140 lines, additions)
- Created: `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx` (7 tests)
- Updated dist: `internal/commanderhub/assets/dist/` (build artifact)

## Self-Review Findings

1. The `loadTree().then` path in the task brief mentions running auto-select inside `then` as path (a), but since React batches state updates, the `useEffect([isNonDesktop, tree, selected])` (path b) handles both cases correctly. The `then` callback sets `setTree(nextTree)`, which triggers the effect on the next render. No explicit auto-select code was needed inside `then` because the effect covers it. The comment in `loadTree` notes this.

2. `pickAutoSession` checks `parentExists` to match DaemonSessionTree's exact behavior — only marks as child if the parent actually exists in the all-sessions list. This matches the `if (!parent) { childNode.parentOffline = true; continue; }` branch in DaemonSessionTree.

3. Desktop tests (5) pass unchanged — no matchMedia stub installed in existing tests, so `useMediaQuery` returns `false` (no `window.matchMedia` function) → desktop path renders.

## Concerns
None. All tests green, build clean.

---

## Fix-Wave: Path (a) Implementation (2026-06-24)

### Problem
The `loadTree().then` block had a misleading comment claiming path (a) existed but `tryAutoSelect` was never actually called there. Only path (b) (the `useEffect` keyed on `[isNonDesktop, tree]`) was real.

### What Changed

**`internal/commanderhub/webapp/src/CommanderApp.tsx`**

1. Added `useLayoutEffect` to the React import.

2. Extracted the auto-select logic from the inline `useEffect` into a named `tryAutoSelect(nextTree)` function inside the component. The function reads `hasAutoSelectedRef.current`, `isNonDesktop` (from render closure), and `selectedRef.current` (always fresh ref) — no stale-closure risk for the ref reads.

3. Added `tryAutoSelectRef` + `useLayoutEffect` (no-deps) to keep it current each render without adding deps to `loadTree`'s `useCallback([])`:
   ```ts
   const tryAutoSelectRef = useRef(tryAutoSelect);
   useLayoutEffect(() => {
     tryAutoSelectRef.current = tryAutoSelect;
   });
   ```

4. Path (b) `useEffect` now calls `tryAutoSelectRef.current(tree)` instead of inline logic:
   ```ts
   useEffect(() => {
     if (!tree) return;
     tryAutoSelectRef.current(tree);
   }, [isNonDesktop, tree]);
   ```
   Note: `selected` removed from deps — the guard now uses `selectedRef.current` (always current, no render-cycle lag).

5. Path (a) wired into `loadTree`'s `.then`:
   ```ts
   .then((nextTree) => {
     setTree(nextTree);
     setAuthRequired(false);
     // Path (a): one-shot auto-select right after the tree arrives,
     // before React flushes the state update.
     tryAutoSelectRef.current(nextTree);
   })
   ```
   `loadTree`'s `useCallback([])` deps are unchanged — `tryAutoSelectRef` is a stable ref object.

6. Removed the misleading comment that claimed path (a) existed (replaced with accurate comment).

### Covering Test Runs

#### `npm test -- src/CommanderApp.test.tsx src/CommanderApp.mobile.test.tsx`
```
Test Files  2 passed (2)
     Tests  12 passed (12)
  Duration  1.54s
```

#### `npm run build`
```
✓ built in 218ms
../assets/dist/index.html                   0.41 kB │ gzip:   0.27 kB
../assets/dist/assets/index-4w_A8AF5.css    5.78 kB │ gzip:   1.70 kB
../assets/dist/assets/index-Du0C4ynQ.js   405.39 kB │ gzip: 126.10 kB
```

#### `npm test` (full suite)
```
Test Files  11 passed (11)
     Tests  65 passed (65)
  Duration  1.63s
```

### New Head SHA
`a09bc1d`
