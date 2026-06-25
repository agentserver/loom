# Task 1 Report: ChatWorkspace `composerLocked` + `composerNote` props

## What Was Implemented

Extended `ChatWorkspace.tsx` with two new optional, backward-compatible props:
- `composerLocked?: boolean` — when `true`, forces `textarea` + send button `disabled`, independent of `turnState`. The `disabled` predicate is now: `empty === true || composerLocked === true || ['queued', 'answering', 'awaiting_approval'].includes(turnState)`.
- `composerNote?: string` — when set, renders `<p className="composer-note">{composerNote}</p>` immediately before the `<form className="composer">` element. When unset, no `.composer-note` element is added to the DOM.

Both props default `undefined`. All 8 pre-existing tests pass without modification.

## TDD Evidence

### RED Phase

Command: `npm test -- src/components/ChatWorkspace.test.tsx`

```
 ❯ src/components/ChatWorkspace.test.tsx (10 tests | 2 failed) 288ms
   × composerLocked=true forces textarea + send button disabled regardless of turnState 7ms
   × composerNote="..." renders .composer-note above composer; omitted means no .composer-note 6ms

 Test Files  1 failed (1)
      Tests  2 failed | 8 passed (10)
```

Both new tests failed as expected — `composerLocked` had no effect (textarea was enabled) and `.composer-note` element was absent from the DOM.

### GREEN Phase

Command: `npm test -- src/components/ChatWorkspace.test.tsx`

```
 Test Files  1 passed (1)
      Tests  10 passed (10)
   Start at  01:16:48
   Duration  1.28s
```

All 10 tests pass: 8 pre-existing + 2 new.

## Full Test Suite

```
npm test

 Test Files  11 passed (11)
      Tests  67 passed (67)
   Start at  01:16:56
   Duration  1.66s
```

No regressions. Full suite was 65 tests before Task 1; now 67 (2 added).

## Files Changed

- `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx` — added `composerLocked?` and `composerNote?` to destructured props and type literal; updated `disabled` predicate; inserted `{composerNote ? <p className="composer-note">{composerNote}</p> : null}` before the composer form.
- `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx` — appended 2 new test cases at the bottom.

## Self-Review Findings

None. The implementation exactly matches the plan spec:
- Prop names, type shapes, and default values are correct.
- Disabled predicate order is correct: `empty === true || composerLocked === true || turnState-check`.
- `.composer-note` uses `<p>` tag (not `<div>`) as specified.
- The `composerNote` null-check uses the same idiomatic `condition ? <element> : null` pattern already in the file.
- Backward-compatible: all pre-existing tests untouched and green.

## Concerns

None.
