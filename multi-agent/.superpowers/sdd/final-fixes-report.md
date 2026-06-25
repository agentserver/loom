# Final Fixes Report — Commander Mobile doc-only comments (2026-06-25)

## Comments Added

### Finding 1: `pendingDaemonOffline` / `!tree` guard (line ~483-487)

Inserted immediately above `const selectedIsPendingDraft = ...` in `CommanderApp.tsx`:

```tsx
  // Note: `pendingDaemonOffline` uses `?? 'offline'` as a defensive default
  // when the daemon row isn't in tree. The `!tree` early-return at line ~474
  // (`if (!tree) return ...`) ensures the JSX consuming these derived values
  // is never rendered while `tree` is null, so the default never user-facing
  // flashes the composer lock during initial load.
  const selectedIsPendingDraft = pendingSession != null
    && selected?.sessionID === pendingSession.sessionID
    && pendingSession.phase === 'draft';
  const pendingDaemonOffline = pendingSession?.phase === 'draft'
    && (tree?.daemons.find((d) => d.daemon_id === pendingSession.daemonID)?.status ?? 'offline') !== 'ok';
```

### Finding 2: `createPendingSession` ordering invariant (line ~439-448)

Inserted immediately above `pendingSessionRef.current = next;` in `CommanderApp.tsx`:

```tsx
    // Ordering: ref first so any synchronous reader (e.g. a re-entrant
    // call from a render-path effect) sees the new pending; then state
    // batched. selectSession last so the effect at line ~308 reads BOTH
    // the new `selected` AND the new `pendingSession` in the next commit
    // and takes the draft short-circuit branch (no apiGet on the fake
    // UUID). The detail-fetch effect's dep array MUST include
    // `pendingSession` for this to hold — see line ~334.
    pendingSessionRef.current = next;
    setPendingSession(next);
    selectSession(daemonID, sid);
```

## npm test result

```
 Test Files  11 passed (11)
      Tests  77 passed (77)
   Duration  1.99s
```

All 77 tests passed (comment-only change, no behavior change).

## npm run build result

```
vite v8.0.16 building client environment for production...
✓ 2082 modules transformed.
../assets/dist/index.html                   0.41 kB │ gzip:   0.27 kB
../assets/dist/assets/index-BRV5GcmA.css    9.72 kB │ gzip:   2.39 kB
../assets/dist/assets/index-Bf0_Bbd2.js   408.55 kB │ gzip: 127.14 kB
✓ built in 221ms
```

Clean build, no errors or warnings.

## New head SHA

`983452e`
