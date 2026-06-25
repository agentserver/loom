# Task 4 Report: FileExplorerPanel renderMode + onPreview

## What Was Implemented

Extended `FileExplorerPanel.tsx` with two new props:
- `renderMode?: 'inline' | 'sheet'` — defaults to `'inline'`, preserving all existing desktop behavior bit-for-bit
- `onPreview?: (payload: { preview: FileReadResult; fullPath: string; displayPath: string }) => void`

Key behavioral changes (sheet mode only):
- The inline `<FilePreview>` block is conditionally rendered: `{renderMode === 'inline' ? <FilePreview preview={preview} /> : null}`
- `openFile()` branches: in `sheet` mode, calls `onPreview({ preview: result, fullPath: fullPath(root, entry.path), displayPath: entry.path })` then returns without calling `setPreview`
- In `sheet` mode, `setPreview(null)` is skipped on file open (no state mutation needed)
- `isAbsolutePath` and `fullPath` helpers are now **exported** for reuse by `FilePreviewSheet` (Task 6)

API URLs used in mocks:
- `/files?path=...` — directory listing via `filesPath()` helper → `/api/commander/daemons/{daemonID}/sessions/{sessionID}/files?path=...`
- `/files/content?path=...` — file content via `fileContentPath()` helper → `/api/commander/daemons/{daemonID}/sessions/{sessionID}/files/content?path=...`

## TDD Evidence

### RED Phase
```
npm test -- src/components/FileExplorerPanel.test.tsx

 ❯ src/components/FileExplorerPanel.test.tsx (8 tests | 1 failed) 289ms
   × renderMode="sheet" calls onPreview with { preview, fullPath, displayPath } and omits inline preview 12ms

FAIL  src/components/FileExplorerPanel.test.tsx > renderMode="sheet" calls onPreview with { preview, fullPath, displayPath } and omits inline preview
Error: expected document not to contain element, found <div class="file-preview-empty">No file selected</div> instead
 ❯ src/components/FileExplorerPanel.test.tsx:219:54

 Test Files  1 failed (1)
      Tests  1 failed | 7 passed (8)
```

(The second new test passed vacuously because the old component ignores unknown props in the JSX test context; only the inline-preview absence check failed.)

### GREEN Phase
```
npm test -- src/components/FileExplorerPanel.test.tsx

 RUN  v4.1.9

 Test Files  1 passed (1)
      Tests  8 passed (8)
   Start at  20:06:14
   Duration  1.32s
```

All 8 tests pass: 6 pre-existing + 2 new.

## Files Changed

- `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx` — rewritten (exported `isAbsolutePath`, `fullPath`; added `renderMode`/`onPreview` props; conditional `<FilePreview>` rendering; `openFile` branching)
- `internal/commanderhub/webapp/src/components/FileExplorerPanel.test.tsx` — appended 2 new test cases

## Self-Review Findings

None. The implementation exactly matches the brief. The stale-request guard (`previewRequestRef`) correctly works in both modes: in `sheet` mode the early return after calling `onPreview` is inside the `if (previewRequestRef.current !== requestID) return;` check.

## Concerns

None.
