# Commander Exec Session Parent Link — P3 (Commander cross-daemon nesting) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render remote (driver/slave) `codex_exec` agent-task sessions nested under their originating session in the Commander tree — across daemons — with `remote`/`parent offline` badges, default-collapsed, without breaking local subagent nesting.

**Architecture:** P2 puts `owner_agent_id`/`parent_agent_id`/`parent_display_name` on `SessionRow` (and `short_id` on `DaemonInfo`). P3 is **frontend-only**: rewrite `DaemonSessionTree`'s tree builder to be **cross-daemon** — a global `(owner_agent_id, session_id)` index resolves a child's parent even when it lives in another daemon group. A remote child renders nested under its parent (primary location) and is omitted from its home daemon's root list; an unresolved parent renders the child as a root with a `parent offline` note. No observer Go changes beyond what P2 landed.

**Tech Stack:** React + TypeScript + Vite, Vitest component tests, Playwright visual checks. Go is untouched (verify with `go test`).

**Spec:** `multi-agent/docs/superpowers/specs/2026-06-17-commander-exec-session-parent-link-design.md` §8.

**Branch:** `commander-parent-link-p2p3`.

---

## File Structure

- `internal/commanderhub/webapp/src/api/types.ts` — add `owner_agent_id`/`parent_agent_id`/`parent_display_name` to `SessionRow`; `short_id` to `DaemonTree`.
- `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx` — cross-daemon builder + remote/parent-offline rendering.
- `internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx` — extend with cross-daemon nesting cases.
- `internal/commanderhub/webapp/e2e/parent-nesting.spec.ts` — **NEW** Playwright visual (optional/CI-gated).

---

## Task 1: Extend `SessionRow` / `DaemonTree` types

**Files:**
- Modify: `internal/commanderhub/webapp/src/api/types.ts`

- [ ] **Step 1: Add fields**

In `SessionRow`, after `parent_id`:

```ts
  owner_agent_id?: string;
  parent_agent_id?: string;
  parent_display_name?: string;
```

In `DaemonTree`, after `display_name`:

```ts
  short_id?: string;
```

- [ ] **Step 2: Typecheck**

Run: `cd internal/commanderhub/webapp && npm run build` (or `tsc --noEmit`)
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/commanderhub/webapp/src/api/types.ts
git commit -m "feat(commander): add owner/parent agent fields to frontend types (#24 P3)"
```

---

## Task 2: Cross-daemon tree builder

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`
- Test: `internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx`

- [ ] **Step 1: Write the failing tests**

Append to `DaemonSessionTree.test.tsx`. Key behaviors: children are **default-collapsed** (assert hidden, then expand, then badge visible); a remote child is **omitted from its home daemon's root list** (real DOM-scope assertion, not a comment); local subagents with only `parent_id` (no `parent_agent_id`) still nest; clicking a remote child selects the **child's** daemon.

```ts
import { fireEvent, render, screen, within } from '@testing-library/react';
import { expect, test, vi } from 'vitest';
import { DaemonSessionTree } from './DaemonSessionTree';
import type { DaemonTree, SessionRow } from '../api/types';

const row = (over: Partial<SessionRow>): SessionRow => ({
  daemon_id: 'd', session_id: 's', kind: 'codex', title: 't',
  turn_state: 'idle', active_worker: false, awaiting_approval: false, ...over,
});

test('nests a remote agent_task child under a parent in another daemon (default-collapsed)', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [row({ daemon_id: 'drv', session_id: 'parent-s', owner_agent_id: 'drv-1', origin: 'user', title: 'parent-s' })] },
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'child-s', owner_agent_id: 'slv-2', title: 'child-s',
        origin: 'agent_task', parent_id: 'parent-s', parent_agent_id: 'drv-1', parent_display_name: 'prod-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);

  // Default-collapsed: child is NOT visible until the parent is expanded.
  expect(screen.queryByText(/remote task · on slave-02/)).toBeNull();
  // The child must NOT appear as a root in its home (slave) daemon group.
  // Assert via the slave group's scope: the only session-title in the slave
  // group is none (child-s is nested under the driver parent, not a slave root).
  const slaveGroup = screen.getByText('slave-02').closest('section')!;
  expect(slaveGroup.querySelector('[data-testid="root-session"]')?.textContent ?? '').not.toContain('child-s');

  // Expand the parent; now the remote child + badge appear.
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: parent-s/));
  expect(screen.getByText(/remote task · on slave-02/)).toBeInTheDocument();
});

test('renders parent-offline note when parent is not in any daemon', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'orphan-s', owner_agent_id: 'slv-2',
        origin: 'agent_task', parent_id: 'gone-s', parent_agent_id: 'drv-gone', parent_display_name: 'old-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  // orphan renders as a root (visible without expansion) with the note.
  expect(screen.getByText(/parent offline/i)).toBeInTheDocument();
  expect(screen.getByText(/old-driver/)).toBeInTheDocument();
});

test('still nests local subagents that have only parent_id (no parent_agent_id)', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [
        row({ daemon_id: 'drv', session_id: 'u-s', owner_agent_id: 'drv-1', origin: 'user', title: 'u-s' }),
        row({ daemon_id: 'drv', session_id: 'sub-s', owner_agent_id: 'drv-1', origin: 'subagent', parent_id: 'u-s', title: 'sub-s' }),
      ] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  // sub-s must NOT be a root (it nests under u-s); expand u-s and find the subagent label.
  const drvGroup = screen.getByText('prod-driver').closest('section')!;
  expect(drvGroup.textContent).not.toMatch(/sub-s.*sub-s/); // not duplicated as a root
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: u-s/));
  expect(screen.getByText(/subagent ·/)).toBeInTheDocument();
});

test('clicking a remote child selects the child home daemon, not the parent daemon', () => {
  const onSelect = vi.fn();
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [row({ daemon_id: 'drv', session_id: 'parent-s', owner_agent_id: 'drv-1', origin: 'user', title: 'parent-s' })] },
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'child-s', owner_agent_id: 'slv-2', title: 'child-s',
        origin: 'agent_task', parent_id: 'parent-s', parent_agent_id: 'drv-1', parent_display_name: 'prod-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={onSelect} />);
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: parent-s/));
  fireEvent.click(screen.getByText('child-s'));
  expect(onSelect).toHaveBeenCalledWith('slv', 'child-s'); // child's daemon, not 'drv'
});
```

(Tests use `data-testid="root-session"` on root session rows and `aria-label` on the toggle — add these attributes in Step 3.)

- [ ] **Step 2: Run to verify they fail**

Run: `cd internal/commanderhub/webapp && npm test -- DaemonSessionTree`
Expected: FAIL — current builder is per-daemon and nests only `subagent + parent_id` within one daemon.

- [ ] **Step 3: Rewrite the builder to be cross-daemon (owner-aware keys)**

Replace `buildSessionNodes` and the render mapping in `DaemonSessionTree.tsx`. **All node/child/home keys are owner-aware** `(owner_agent_id, session_id)` — never `session_id` alone — so two daemons/backends sharing a session id can't collide (#4). **Local subagents** with only `parent_id` (no `parent_agent_id`) resolve their parent under the child's own `owner_agent_id` (#3) — so they keep nesting.

```ts
type SessionNode = {
  session: SessionRow;
  children: SessionNode[];
  remote: boolean;
  parentOffline: boolean; // has parent_id but parent not found in any daemon
};

// ownerKey accepts string | undefined (SessionRow.owner_agent_id is optional;
// tsconfig is strict — a `string` param would fail to compile). Coerces to ''.
function ownerKey(ownerAgentID: string | undefined, sessionID: string): string {
  return `${ownerAgentID ?? ''}\0${sessionID}`;
}

// parentOwnerFor returns the owner that owns a child's parent. For remote
// agent_task children, parent_agent_id is set explicitly. For local subagents
// (P1 leaves ParentAgentID empty) the parent lives in the SAME owner, so fall
// back to the child's own owner_agent_id.
function parentOwnerFor(s: SessionRow): string | undefined {
  return s.parent_agent_id ?? s.owner_agent_id;
}

function buildCrossDaemonTree(daemons: DaemonTree[]) {
  const all = daemons.flatMap(d => d.sessions ?? []);
  // Every map keyed by ownerKey (owner_agent_id, session_id) — never session_id alone.
  const byOwnerKey = new Map<string, SessionNode>();
  for (const s of all) {
    byOwnerKey.set(ownerKey(s.owner_agent_id, s.session_id),
      { session: s, children: [], remote: false, parentOffline: false });
  }
  const isChildKey = new Set<string>(); // ownerKey of resolved children
  for (const s of all) {
    if (s.origin !== 'subagent' && s.origin !== 'agent_task') continue;
    if (!s.parent_id) continue;
    const parentKey = ownerKey(parentOwnerFor(s), s.parent_id);
    const parent = byOwnerKey.get(parentKey);
    const childNode = byOwnerKey.get(ownerKey(s.owner_agent_id, s.session_id))!;
    if (!parent) {
      // parent offline → child stays a root, flagged for the offline note.
      childNode.parentOffline = true;
      continue;
    }
    parent.children.push(childNode);
    isChildKey.add(ownerKey(s.owner_agent_id, s.session_id));
  }
  // Roots per daemon = that daemon's sessions whose ownerKey is NOT a resolved child.
  const rootsByDaemon = new Map<string, SessionNode[]>();
  for (const d of daemons) {
    rootsByDaemon.set(d.daemon_id, (d.sessions ?? [])
      .filter(s => !isChildKey.has(ownerKey(s.owner_agent_id, s.session_id)))
      .map(s => byOwnerKey.get(ownerKey(s.owner_agent_id, s.session_id))!));
  }
  // Mark remote: child's home daemon != parent's home daemon (by ownerKey lookup).
  const daemonOfOwnerKey = new Map<string, string>();
  for (const s of all) daemonOfOwnerKey.set(ownerKey(s.owner_agent_id, s.session_id), s.daemon_id);
  for (const parent of byOwnerKey.values()) {
    const parentDaemon = daemonOfOwnerKey.get(ownerKey(parent.session.owner_agent_id, parent.session.session_id));
    for (const child of parent.children) {
      child.remote = daemonOfOwnerKey.get(ownerKey(child.session.owner_agent_id, child.session.session_id)) !== parentDaemon;
    }
  }
  return { rootsByDaemon, byOwnerKey };
}
```

**Render rules (fix #5, #7, #9):**
- For each daemon group, render its `rootsByDaemon`; mark each root row with `data-testid="root-session"` (for the dedup test).
- Under each root, recursively render `node.children`, default-collapsed (existing `expanded` state, keyed by parent's `(daemon_id, session_id)`). The toggle keeps its existing `aria-label` (`展开/收起 subagent sessions: <title>`).
- **A child button calls `onSelect(child.session.daemon_id, child.session.session_id)`** — the child's home daemon, NOT the parent group's daemon. (A remote child lives visually under the driver parent but selecting it must open the slave daemon's session.)
- **Remote child meta (unified text, fix #9):** `remote task · on <home display_name>` everywhere (badge text, Task 3, and the test assertion must match this exact string). Home display_name from a `daemon_id → display_name` map built from `daemons`.
- **Parent-offline root (fix #7):** a root node with `parentOffline === true` renders meta `parent offline · <parent_display_name>` (muted). This is concrete — the builder sets the flag; the renderer checks it. No "actually it IS a root" hand-waving.
- A root with `parent_id` that wasn't resolved (parent offline) — i.e. its ownerKey is not in `byOwnerKey` as someone's... actually it IS a root and has `parent_id` set but no resolved parent — render meta `parent offline · <parent_display_name>`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd internal/commanderhub/webapp && npm test -- DaemonSessionTree`
Expected: PASS (cross-daemon nest, parent-offline, local subagent).

- [ ] **Step 5: Commit**

```bash
git add internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx
git commit -m "feat(commander): cross-daemon session nesting with remote/parent-offline (#24 P3)"
```

---

## Task 3: Badges + default-collapsed rendering polish

**Files:**
- Modify: `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`

- [ ] **Step 1: Render badges**

- Remote child row meta: `remote task · on <home display_name>` (was `agent task · <working_dir>`).
- Parent-offline root row meta: `parent offline · <parent_display_name>` (muted/grey via existing class).
- Local subagent: unchanged (`subagent · <name>`).
- Default-collapsed: the existing `expanded` state already defaults to collapsed; ensure the remote-children sublist also starts collapsed (it reuses the same toggle per parent).

- [ ] **Step 2: Component test for badge text**

Add an assertion (covered by Task 2 tests) that `remote task · on slave-02` and `parent offline` appear.

- [ ] **Step 3: Commit**

```bash
git add internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx
git commit -m "feat(commander): remote/parent-offline badges in session tree (#24 P3)"
```

---

## Task 4: Build artifact + Go regression

- [ ] **Step 1: Rebuild the embedded frontend**

Run: `cd internal/commanderhub/webapp && npm run build`
Then verify `assets/dist` is updated and committed (the repo commits the build artifact; CI checks no drift):

```bash
git add internal/commanderhub/assets/dist
git commit -m "chore(commanderhub): rebuild embedded commander app (#24 P3)"
```

- [ ] **Step 2: Go regression (frontend embed + commanderhub)**

Run: `go build ./... && go test ./internal/commanderhub/ -race -count=1`
Expected: PASS (the embed serves the new bundle; commanderhub tests unaffected).

- [ ] **Step 3: gofmt + vet**

Run: `gofmt -w . && go vet ./... && git diff --exit-code`
Expected: clean.

---

## Task 5 (optional, CI-gated): Playwright visual

**Files:**
- Create: `internal/commanderhub/webapp/e2e/parent-nesting.spec.ts`

- [ ] **Step 1:** A Playwright test that loads a mocked Commander tree (driver session + a remote slave child + an offline-parent orphan), asserts the child renders nested under the driver session with a `remote task · on slave-02` badge and the orphan shows `parent offline`. Gate on Codex/commander availability like existing e2e.

- [ ] **Step 2:** Run (CI-gated): `npx playwright test parent-nesting`

- [ ] **Step 3: Commit**

```bash
git add internal/commanderhub/webapp/e2e/parent-nesting.spec.ts
git commit -m "test(commander): playwright visual for cross-daemon nesting (#24 P3)"
```

---

## Acceptance for P3

- `SessionRow`/`DaemonTree` frontend types carry `owner_agent_id`/`parent_agent_id`/`parent_display_name`/`short_id`.
- `DaemonSessionTree` nests `agent_task` (and `subagent`) children under their parent **across daemons**; a remote child is omitted from its home daemon's root list and shown nested under the parent with a `remote task · on <display_name>` badge.
- Unresolved parent → child renders as a root with a `parent offline · <parent_display_name>` note (no child dropped).
- Local subagent nesting unchanged; default-collapsed preserved.
- Embedded `assets/dist` rebuilt and committed; `go test ./internal/commanderhub/ -race` green.

## Out of scope

- Observer Go changes (P2 landed the needed `SessionRow` fields).
- Per-session `current` marker concurrency (P2 accepted caveat).
- web humanloop approval UI (still out of scope per the UI-redesign spec).

## Implementation notes

- **Branch:** implement P3 on its own branch (e.g. `commander-parent-link-p3`) off `origin/master` (after P2 lands). The `commander-parent-link-p2p3` branch holds plan docs only.
- **Windows:** P3 is frontend-only (no Go path changes), so Windows verification is not required here; but the embedded `assets/dist` must rebuild cleanly on the CI Node version.
