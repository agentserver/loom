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

Append to `DaemonSessionTree.test.tsx` (cases: cross-daemon child nests under parent + is omitted from home root; parent-offline child renders as root with note; local subagent still nests):

```ts
import { render, screen } from '@testing-library/react';
import { DaemonSessionTree } from './DaemonSessionTree';
import type { DaemonTree, SessionRow } from '../api/types';

const row = (over: Partial<SessionRow>): SessionRow => ({
  daemon_id: 'd', session_id: 's', kind: 'codex', title: 't',
  turn_state: 'idle', active_worker: false, awaiting_approval: false, ...over,
});

it('nests a remote agent_task child under a parent in another daemon', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [row({ daemon_id: 'drv', session_id: 'parent-s', owner_agent_id: 'drv-1', origin: 'user' })] },
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'child-s', owner_agent_id: 'slv-2',
        origin: 'agent_task', parent_id: 'parent-s', parent_agent_id: 'drv-1', parent_display_name: 'prod-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  // child renders under the parent (parent's session node), with a remote badge
  expect(screen.getByText(/remote · on slave-02/)).toBeInTheDocument();
  // child is NOT a root in its home daemon group
  // (assert absence of a second top-level session-title "child-s" outside the parent)
});

it('renders parent-offline note when parent is not in any daemon', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'orphan-s', owner_agent_id: 'slv-2',
        origin: 'agent_task', parent_id: 'gone-s', parent_agent_id: 'drv-gone', parent_display_name: 'old-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  expect(screen.getByText(/parent offline/i)).toBeInTheDocument();
  expect(screen.getByText(/old-driver/)).toBeInTheDocument();
});

it('still nests local subagents within their daemon', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [
        row({ daemon_id: 'drv', session_id: 'u-s', owner_agent_id: 'drv-1', origin: 'user' }),
        row({ daemon_id: 'drv', session_id: 'sub-s', owner_agent_id: 'drv-1', origin: 'subagent', parent_id: 'u-s' }),
      ] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  expect(screen.getByText(/subagent ·/)).toBeInTheDocument();
});
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd internal/commanderhub/webapp && npm test -- DaemonSessionTree`
Expected: FAIL — current builder is per-daemon and nests only `subagent + parent_id` within one daemon.

- [ ] **Step 3: Rewrite the builder to be cross-daemon**

Replace `buildSessionNodes` and the render mapping in `DaemonSessionTree.tsx`. Core logic:

```ts
type SessionNode = { session: SessionRow; children: SessionNode[]; remote: boolean };

function ownerKey(ownerAgentID: string, sessionID: string) {
  return `${ownerAgentID}\0${sessionID}`;
}

// Build a global tree across all daemons. Returns:
//   rootsByDaemon: per daemon_id, the root nodes (sessions not nested elsewhere
//                  under a resolved parent) — remote children are omitted here.
//   A child with an unresolved parent is a root in its home daemon (parent-offline).
function buildCrossDaemonTree(daemons: DaemonTree[]) {
  const all = daemons.flatMap(d => d.sessions ?? []);
  const byOwnerKey = new Map<string, SessionNode>();
  const nodeBySession = new Map<string, SessionNode>();
  for (const s of all) {
    const node: SessionNode = { session: s, children: [], remote: false };
    byOwnerKey.set(ownerKey(s.owner_agent_id ?? '', s.session_id), node);
    nodeBySession.set(s.session_id, node);
  }
  const isChild = new Set<string>();
  for (const s of all) {
    if (s.origin !== 'subagent' && s.origin !== 'agent_task') continue;
    if (!s.parent_id || !s.parent_agent_id) continue;
    const parent = byOwnerKey.get(ownerKey(s.parent_agent_id, s.parent_id));
    if (!parent) continue; // parent offline → stays a root
    parent.children.push(nodeBySession.get(s.session_id)!);
    isChild.add(s.session_id);
  }
  // Roots per daemon = that daemon's sessions that are not a resolved child.
  const rootsByDaemon = new Map<string, SessionNode[]>();
  for (const d of daemons) {
    rootsByDaemon.set(d.daemon_id, (d.sessions ?? [])
      .filter(s => !isChild.has(s.session_id))
      .map(s => nodeBySession.get(s.session_id)!));
  }
  // Mark remote children (child whose home daemon != parent's home daemon).
  const homeDaemon = new Map<string, string>(); // session_id -> daemon_id
  for (const s of all) homeDaemon.set(s.session_id, s.daemon_id);
  for (const node of byOwnerKey.values()) {
    for (const child of node.children) {
      const parentDaemon = homeDaemon.get(node.session.session_id);
      child.remote = parentDaemon !== homeDaemon.get(child.session.session_id);
    }
  }
  return { rootsByDaemon };
}
```

Render: for each daemon group, render `rootsByDaemon.get(daemon_id)`; under each root, recursively render `node.children` (default-collapsed, as today). A `remote` child shows a `remote · on <home daemon display_name>` meta; a root that has `parent_agent_id` but wasn't resolved (parent offline) shows a `parent offline · <parent_display_name>` meta. Look up home-daemon display_name via a `short_id → display_name` map built from `daemons` (and the child's own `daemon_id`).

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

Add an assertion (covered by Task 2 tests) that `remote · on slave-02` and `parent offline` appear.

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

- [ ] **Step 1:** A Playwright test that loads a mocked Commander tree (driver session + a remote slave child + an offline-parent orphan), asserts the child renders nested under the driver session with a `remote · on slave-02` badge and the orphan shows `parent offline`. Gate on Codex/commander availability like existing e2e.

- [ ] **Step 2:** Run (CI-gated): `npx playwright test parent-nesting`

- [ ] **Step 3: Commit**

```bash
git add internal/commanderhub/webapp/e2e/parent-nesting.spec.ts
git commit -m "test(commander): playwright visual for cross-daemon nesting (#24 P3)"
```

---

## Acceptance for P3

- `SessionRow`/`DaemonTree` frontend types carry `owner_agent_id`/`parent_agent_id`/`parent_display_name`/`short_id`.
- `DaemonSessionTree` nests `agent_task` (and `subagent`) children under their parent **across daemons**; a remote child is omitted from its home daemon's root list and shown nested under the parent with a `remote · on <display_name>` badge.
- Unresolved parent → child renders as a root with a `parent offline · <parent_display_name>` note (no child dropped).
- Local subagent nesting unchanged; default-collapsed preserved.
- Embedded `assets/dist` rebuilt and committed; `go test ./internal/commanderhub/ -race` green.

## Out of scope

- Observer Go changes (P2 landed the needed `SessionRow` fields).
- Per-session `current` marker concurrency (P2 accepted caveat).
- web humanloop approval UI (still out of scope per the UI-redesign spec).
