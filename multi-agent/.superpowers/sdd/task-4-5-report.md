# Task 4+5 Implementation Report

## What Was Implemented

### CommanderApp.tsx

**4 new state items:**
- `pendingSession: PendingSession | null` (useState)
- `pendingSessionRef: useRef<PendingSession | null>(null)` — kept in sync via `useLayoutEffect`
- `selectedIsPendingDraft` — derived boolean: pending non-null + sessionID matches + phase === 'draft'
- `composerLocked`, `composerNote` — derived from `pendingDaemonOffline && selectedIsPendingDraft`

**createPendingSession(daemonID):**
- Same-daemon idempotency: if pending exists for same daemon, re-selects existing without minting new UUID
- Different-daemon guard: returns early if pending exists for a different daemon
- New path: mints `crypto.randomUUID()`, sets draft phase, calls `selectSession`

**discardPendingSession():**
- Clears `pendingSessionRef` and `pendingSession` state
- If `selectedRef.current.sessionID === prev.sessionID`, also clears `selected`

**Detail-fetch effect short-circuit:**
- When `pendingSession.phase === 'draft' && pendingSession.sessionID === selected.sessionID`, returns synthetic `{ session: { ID, Title: '新建会话' }, messages: [] }` without issuing any API call
- Dep array includes `pendingSession` so re-fires on draft→submitting phase flip

**sendPrompt pending branch (before isCurrentTurn guard):**
- After `if (turnError) throw turnError`, checks `pendingSessionRef.current`
- If match: flips phase to 'submitting' via functional setState, calls `void loadTree()`, returns early
- Does NOT gate on `isCurrentTurn()` — server-side session was created regardless of navigation

**loadTree-clears-pending-on-real-row:**
- After `setTree(nextTree)`, scans `nextTree.daemons` for `pendingSessionRef.current`
- If real row found: clears both `pendingSessionRef` and `pendingSession` state

**fileDaemonID / fileSessionID:**
- When `selectedIsPendingDraft` → empty strings to suppress FileExplorerPanel's /files fetch

**MobileShell props forwarding:**
- `pendingSession`, `onCreateSession`, `onDiscardSession`, `composerLocked`, `composerNote`, `disableFiles` — all 6 passed to `<MobileShell>`

**Desktop DaemonSessionTree + ChatWorkspace + FileExplorerPanel:**
- `DaemonSessionTree` receives `pendingSession`, `onCreateSession`, `onDiscardSession`
- `ChatWorkspace` receives `composerLocked`, `composerNote`
- `FileExplorerPanel` receives `fileDaemonID`, `fileSessionID`

### MobileShell.tsx

**6 new props added (all optional):**
- `pendingSession`, `onCreateSession`, `onDiscardSession`, `composerLocked`, `composerNote`, `disableFiles`

**handleCreate wrapper:**
- Calls `onCreateSession(daemonID)` then `closeOverlay('sessions', setSessionsOpen)` — mirrors `handleSelectSession` pattern

**DaemonSessionTree inside Sessions drawer:**
- Now passes `pendingSession`, `onCreateSession ? handleCreate : undefined`, `onDiscardSession`

**ChatWorkspace:**
- Passes `composerLocked` and `composerNote` (other props unchanged)

**FileExplorerPanel:**
- `daemonID={disableFiles ? '' : (selected?.daemonID || '')}`
- `sessionID={disableFiles ? '' : (selected?.sessionID || '')}`
- `onPreviewRequest={handlePreviewRequest}` and `onPreview={handlePreview}` PRESERVED exactly as before

### CommanderApp.mobile.test.tsx

- Added `fireEvent` to testing-library import
- Appended 3 new mobile tests:
  1. Click + creates pending draft, drawer closes, composer enabled, no detail fetch issued
  2. Re-clicking + on same daemon does NOT mint a fresh UUID (idempotency)
  3. × discard clears pendingSession and selected

## TDD Evidence

**RED (before MobileShell edit):**
```
npm test -- src/CommanderApp.mobile.test.tsx
Tests  3 failed | 7 passed (10)
```
Failures at line 309 — `getByRole('button', { name: /新建 session: prod-codex/ })` not found because `onCreateSession` was not yet wired into the drawer's `<DaemonSessionTree>`.

**GREEN (after MobileShell edit):**
```
npm test -- src/CommanderApp.mobile.test.tsx
Tests  10 passed (10)
```

## Test Results

- `npm test -- src/CommanderApp.test.tsx`: 5 passed (desktop suite, no regressions)
- `npm test -- src/CommanderApp.mobile.test.tsx`: 10 passed (7 existing + 3 new)
- `npm test` (full suite): **77 passed across 11 test files**

## Build Result

`npm run build`: tsc + vite build succeeded, 408.55 kB bundle, 0 errors or warnings.

## Files Changed

- `internal/commanderhub/webapp/src/CommanderApp.tsx`
- `internal/commanderhub/webapp/src/CommanderApp.mobile.test.tsx`
- `internal/commanderhub/webapp/src/components/MobileShell.tsx`

## Self-Review Findings

None. All spec requirements confirmed implemented:
- Single-slot pending invariant enforced in both phases
- `sendPrompt` phase flip + `loadTree` runs before `isCurrentTurn()` short-circuit
- `pendingSession` in detail-fetch dep array
- `onPreviewRequest` / `onPreview` preserved in MobileShell's FileExplorerPanel

## Concerns

None.
