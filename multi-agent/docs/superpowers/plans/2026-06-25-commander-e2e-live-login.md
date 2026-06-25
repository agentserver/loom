# Commander Playwright e2e against live observer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one Playwright e2e test that drives the live commander UI through the agentserver OAuth login (printing the verification URL on first run, reusing the cookie afterwards) and asserts the pr33 fresh-id rebind end-to-end against the running observer + driver-codex.

**Architecture:** A separate `playwright.live.config.ts` co-located with the existing mocked `playwright.config.ts`. A `globalSetup` helper (`src/e2e/live-login.ts`) handles cached-or-live login + daemon readiness; the spec (`src/e2e/commander-live.spec.ts`) captures the placeholder UUID, intercepts the SSE `done` frame for the real codex thread ID, and asserts the daemon-tree row plus selected-session detail GET both pin to that real ID. One frontend hook (`data-session-id` on session rows) is the only production change.

**Tech Stack:** Playwright 1.61 (already installed), Vitest 4 (for one unit test of the path const), TypeScript 6 (ESM, `"type": "module"`).

## Global Constraints

- Observer URL: `http://127.0.0.1:18091`; driver URL: `http://127.0.0.1:18092` — both run as host processes, NOT docker, started manually by the user.
- StorageState cache: `<repo>/multi-agent/tests/prod_test/.playwright/observer-session.json`. The parent `tests/prod_test/` directory is already gitignored at `multi-agent/.gitignore:line listing prod_test/` — **no new .gitignore entry needed**.
- Webapp `package.json` has `"type": "module"`. `__dirname`/`__filename` are undefined; all path resolution uses `fileURLToPath(import.meta.url) + dirname`.
- Playwright config is evaluated BEFORE `globalSetup` runs. `use.storageState` cannot read `process.env` values written inside globalSetup — it must reference a deterministic const imported at module-load time.
- `testMatch: 'commander-live.spec.ts'` — the new config shares `testDir: './src/e2e'` with the existing mocked suite; without `testMatch`, the existing `commander.spec.ts` would be picked up too and would 401 against the real backend.
- Single browser project: `chromium-desktop` at 1440x960. No mobile / tablet — coverage there stays in the existing mocked suite.
- `retries: 0` — flaky live e2e should fail loudly, not retry-mask a real regression.
- Spec path: `docs/superpowers/specs/2026-06-25-commander-e2e-live-login-design.md` is authoritative; this plan implements it.

---

### Task 1: Frontend hook — `data-session-id` on session-row buttons

The test pins to the exact backend session ID. Tree rows currently expose
no stable hook for that ID. Add `data-session-id` to both the real and
pending session-row `<button>` elements in `DaemonSessionTree.tsx`.
`data-testid="daemon-tree"` already exists at line 240 — no work there.

**Files:**
- Modify: `multi-agent/internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`
- Test: `multi-agent/internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx`

**Interfaces:**
- Consumes: nothing — pure DOM addition.
- Produces: real session row `<button>` carries `data-session-id={session.session_id}`; pending session row `<button>` carries `data-session-id={pendingSession.sessionID}`. Read by the live spec via `page.locator('[data-testid="daemon-tree"] button[data-session-id]')`.

- [ ] **Step 1: Find or create a vitest file for `DaemonSessionTree` and write a failing test**

If `DaemonSessionTree.test.tsx` already exists, append to it. Otherwise create. The test renders the tree with one daemon + one session, plus a pending draft session, and asserts both rows expose `data-session-id`.

```tsx
import { render, screen } from '@testing-library/react';
import { describe, expect, test } from 'vitest';
import { DaemonSessionTree } from './DaemonSessionTree';

describe('DaemonSessionTree data-session-id', () => {
  test('real session row and pending row both expose data-session-id', () => {
    const tree = {
      daemons: [
        {
          daemon_id: 'd1',
          display_name: 'driver-codex',
          kind: 'codex',
          status: 'ok',
          sessions: [
            {
              daemon_id: 'd1',
              session_id: 'real-uuid-abc',
              kind: 'codex',
              title: 't',
              working_dir: '/tmp',
              updated_at: '2026-06-25T00:00:00Z',
              message_count: 0,
              preview: '',
              turn_state: 'idle',
              active_worker: false,
              awaiting_approval: false,
            },
          ],
        },
      ],
    };
    render(
      <DaemonSessionTree
        tree={tree as never}
        selected={null}
        pendingSession={{ daemonID: 'd1', sessionID: 'placeholder-uuid-xyz', phase: 'draft' }}
        onSelect={() => {}}
        onCreateSession={() => {}}
        onDiscardSession={() => {}}
      />,
    );
    const real = screen.getByRole('button', { name: /^t/ });
    expect(real.getAttribute('data-session-id')).toBe('real-uuid-abc');
    const pending = screen.getByRole('button', { name: /新建会话/ });
    expect(pending.getAttribute('data-session-id')).toBe('placeholder-uuid-xyz');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd multi-agent/internal/commanderhub/webapp
npm run test -- --run DaemonSessionTree
```

Expected: FAIL — `data-session-id` is `null` on both rows.

- [ ] **Step 3: Add `data-session-id` to the real session row button**

In `DaemonSessionTree.tsx` around line 213, the real-session row button:

```tsx
<button
  className={rowClass}
  onClick={() => onSelect(session.daemon_id, session.session_id)}
```

Add `data-session-id={session.session_id}`:

```tsx
<button
  className={rowClass}
  data-session-id={session.session_id}
  onClick={() => onSelect(session.daemon_id, session.session_id)}
```

- [ ] **Step 4: Add `data-session-id` to the pending session row button**

In `DaemonSessionTree.tsx` around line 282, the pending row button:

```tsx
<button
  type="button"
  className={`session-row${selected?.sessionID === pendingSession.sessionID ? ' selected' : ''}`}
  onClick={() => onSelect(daemon.daemon_id, pendingSession.sessionID)}
>
```

Add `data-session-id={pendingSession.sessionID}`:

```tsx
<button
  type="button"
  className={`session-row${selected?.sessionID === pendingSession.sessionID ? ' selected' : ''}`}
  data-session-id={pendingSession.sessionID}
  onClick={() => onSelect(daemon.daemon_id, pendingSession.sessionID)}
>
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd multi-agent/internal/commanderhub/webapp
npm run test -- --run DaemonSessionTree
```

Expected: PASS.

- [ ] **Step 6: Run the full vitest suite to confirm no regressions**

```bash
npm run test -- --run
```

Expected: ALL PASS (83+ tests). Adding a single attribute should not break any existing assertion.

- [ ] **Step 7: Commit**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login
git add multi-agent/internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx \
        multi-agent/internal/commanderhub/webapp/src/components/DaemonSessionTree.test.tsx
git commit -m "feat(commander): data-session-id on session-row buttons

Stable DOM hook for live Playwright e2e to pin assertions to the
exact backend session ID. Real-session rows carry session.session_id;
pending rows carry pendingSession.sessionID. Pure DOM addition — no
behavior change."
```

---

### Task 2: globalSetup helper — `src/e2e/live-login.ts`

The single module that owns: the `STORAGE_STATE_PATH` const (imported
by both the config and the spec), TCP-probe + cookie-validate logic, the
headed-browser login dance with the printed banner, atomic storageState
write, and the post-auth daemon readiness poll. Exports
`STORAGE_STATE_PATH` and the default `globalSetup` function.

**Files:**
- Create: `multi-agent/internal/commanderhub/webapp/src/e2e/live-login.ts`
- Test: `multi-agent/internal/commanderhub/webapp/src/e2e/live-login.test.ts` (unit test only for the path const)

**Interfaces:**
- Consumes: `@playwright/test`, `node:fs`, `node:net`, `node:path`, `node:url`.
- Produces:
  - `export const STORAGE_STATE_PATH: string` — absolute path to `<repo>/multi-agent/tests/prod_test/.playwright/observer-session.json`.
  - `export const OBSERVER_BASE = 'http://127.0.0.1:18091'`.
  - `export const DRIVER_BASE = 'http://127.0.0.1:18092'`.
  - `export default async function globalSetup(): Promise<void>` — the function Playwright invokes once before the test suite.

- [ ] **Step 1: Write the failing path-const unit test**

Create `multi-agent/internal/commanderhub/webapp/src/e2e/live-login.test.ts`:

```ts
import { existsSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, test } from 'vitest';
import { STORAGE_STATE_PATH } from './live-login';

const HERE = dirname(fileURLToPath(import.meta.url));
// Webapp root: src/e2e → src → webapp
const WEBAPP_ROOT = resolve(HERE, '../..');
// Repo root: webapp → commanderhub → internal → multi-agent
const MULTI_AGENT_ROOT = resolve(WEBAPP_ROOT, '../../..');
const EXPECTED = resolve(
  MULTI_AGENT_ROOT,
  'tests/prod_test/.playwright/observer-session.json',
);

describe('STORAGE_STATE_PATH', () => {
  test('resolves to multi-agent/tests/prod_test/.playwright/observer-session.json', () => {
    expect(STORAGE_STATE_PATH).toBe(EXPECTED);
  });

  test('parent directory tests/prod_test/ exists in repo', () => {
    // tests/prod_test is a checked-in directory; the .playwright/ subdir
    // is gitignored but its parent must exist for mkdirSync to work.
    expect(existsSync(resolve(MULTI_AGENT_ROOT, 'tests/prod_test'))).toBe(true);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd multi-agent/internal/commanderhub/webapp
npm run test -- --run live-login
```

Expected: FAIL — `Cannot find module './live-login'`.

- [ ] **Step 3: Create `live-login.ts` with the const + helpers**

Create `multi-agent/internal/commanderhub/webapp/src/e2e/live-login.ts`:

```ts
import { chromium, request } from '@playwright/test';
import { existsSync, mkdirSync, renameSync } from 'node:fs';
import { createConnection } from 'node:net';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
// src/e2e → src → webapp → commanderhub → internal → multi-agent
export const STORAGE_STATE_PATH = resolve(
  HERE, '../../../../../tests/prod_test/.playwright/observer-session.json',
);

export const OBSERVER_BASE = 'http://127.0.0.1:18091';
export const DRIVER_BASE = 'http://127.0.0.1:18092';

const LOGIN_TIMEOUT_MS = 10 * 60 * 1000;
const DAEMON_READY_TIMEOUT_MS = 30 * 1000;

function tcpProbe(host: string, port: number, timeoutMs = 2000): Promise<void> {
  return new Promise((res, rej) => {
    const sock = createConnection({ host, port });
    const fail = (err: Error) => { sock.destroy(); rej(err); };
    const timer = setTimeout(() => fail(new Error(`timeout ${host}:${port}`)), timeoutMs);
    sock.once('connect', () => { clearTimeout(timer); sock.end(); res(); });
    sock.once('error', (err) => { clearTimeout(timer); fail(err); });
  });
}

async function cookieStillValid(): Promise<boolean> {
  if (!existsSync(STORAGE_STATE_PATH)) return false;
  let ctx;
  try {
    ctx = await request.newContext({ storageState: STORAGE_STATE_PATH, baseURL: OBSERVER_BASE });
    const res = await ctx.get('/api/commander/tree');
    return res.status() === 200;
  } catch {
    // Parse error / corrupt JSON / network error → treat as miss.
    return false;
  } finally {
    if (ctx) await ctx.dispose();
  }
}

function printLoginBanner(verifyURL: string): void {
  const line = '═'.repeat(64);
  // eslint-disable-next-line no-console
  console.log(`
╔${line}╗
║  OPEN THIS URL TO AUTHORIZE COMMANDER:                          ║
║                                                                  ║
║  ${verifyURL.padEnd(62)}║
║                                                                  ║
║  Waiting up to 10 minutes...                                    ║
╚${line}╝
`);
}

async function runLiveLogin(): Promise<void> {
  const browser = await chromium.launch({ headless: false });
  try {
    const ctx = await browser.newContext({ baseURL: OBSERVER_BASE });
    const page = await ctx.newPage();
    await page.goto('/commander/');

    const [loginResp] = await Promise.all([
      page.waitForResponse('**/api/commander/login'),
      page.getByRole('button', { name: '用 agentserver 登录' }).click(),
    ]);
    const body = (await loginResp.json()) as { verification_uri_complete: string };
    printLoginBanner(body.verification_uri_complete);

    await page.getByTestId('daemon-tree').waitFor({ timeout: LOGIN_TIMEOUT_MS });

    // Atomic write: storageState → temp file → rename.
    mkdirSync(dirname(STORAGE_STATE_PATH), { recursive: true });
    const tmp = STORAGE_STATE_PATH + '.tmp';
    await ctx.storageState({ path: tmp });
    renameSync(tmp, STORAGE_STATE_PATH);

    await ctx.close();
  } finally {
    await browser.close();
  }
}

async function waitForCodexDaemonReady(): Promise<void> {
  const deadline = Date.now() + DAEMON_READY_TIMEOUT_MS;
  const ctx = await request.newContext({ storageState: STORAGE_STATE_PATH, baseURL: OBSERVER_BASE });
  try {
    while (Date.now() < deadline) {
      const res = await ctx.get('/api/commander/tree');
      if (res.status() === 200) {
        const tree = (await res.json()) as { daemons?: Array<{ kind?: string; status?: string }> };
        const ok = (tree.daemons || []).some((d) => d.kind === 'codex' && d.status === 'ok');
        if (ok) return;
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
    throw new Error('driver-codex daemon not registered with observer — check driver logs');
  } finally {
    await ctx.dispose();
  }
}

export default async function globalSetup(): Promise<void> {
  try {
    await tcpProbe('127.0.0.1', 18091);
  } catch {
    throw new Error('observer-server not listening on 127.0.0.1:18091 — start it first (see tests/prod_test/observer-local/)');
  }
  try {
    await tcpProbe('127.0.0.1', 18092);
  } catch {
    throw new Error('driver-agent not listening on 127.0.0.1:18092 — start it first (see tests/prod_test/driver-codex-local/)');
  }
  if (!(await cookieStillValid())) {
    await runLiveLogin();
  }
  await waitForCodexDaemonReady();
}
```

- [ ] **Step 4: Run the unit test to verify it passes**

```bash
cd multi-agent/internal/commanderhub/webapp
npm run test -- --run live-login
```

Expected: PASS — `STORAGE_STATE_PATH` resolves to the correct repo-relative path.

- [ ] **Step 5: TypeScript build check**

```bash
cd multi-agent/internal/commanderhub/webapp
npx tsc --noEmit
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login
git add multi-agent/internal/commanderhub/webapp/src/e2e/live-login.ts \
        multi-agent/internal/commanderhub/webapp/src/e2e/live-login.test.ts
git commit -m "feat(commander): globalSetup helper for live e2e

Exports STORAGE_STATE_PATH (the deterministic cache file location
shared by the live config and the spec), OBSERVER_BASE, DRIVER_BASE,
and a default globalSetup that:
- TCP-probes :18091 + :18092 (fast-fail on missing processes)
- validates any cached cookie against /api/commander/tree
- on miss: headed-browser login, prints the verification URL banner,
  waits up to 10 min for daemon-tree visible, atomic-writes storageState
- after auth: polls /api/commander/tree until driver-codex is status:ok

ESM (\"type\": \"module\"), so uses fileURLToPath(import.meta.url)
instead of __dirname. Vitest covers the path const."
```

---

### Task 3: `playwright.live.config.ts` + npm script

Live-target Playwright config. No `webServer` block. `testMatch` pinned
to the new spec so the existing mocked `commander.spec.ts` is excluded.

**Files:**
- Create: `multi-agent/internal/commanderhub/webapp/playwright.live.config.ts`
- Modify: `multi-agent/internal/commanderhub/webapp/package.json` (add `test:e2e:live` script)

**Interfaces:**
- Consumes: `STORAGE_STATE_PATH` from `./src/e2e/live-login.ts`.
- Produces: a `test:e2e:live` npm script + a config that loads exactly one test file.

- [ ] **Step 1: Create the config**

Create `multi-agent/internal/commanderhub/webapp/playwright.live.config.ts`:

```ts
import { defineConfig, devices } from '@playwright/test';
import { STORAGE_STATE_PATH } from './src/e2e/live-login';

export default defineConfig({
  testDir: './src/e2e',
  testMatch: 'commander-live.spec.ts',
  timeout: 120_000,
  expect: { timeout: 10_000 },
  retries: 0,
  fullyParallel: false,
  workers: 1,
  globalSetup: './src/e2e/live-login.ts',
  use: {
    baseURL: 'http://127.0.0.1:18091',
    storageState: STORAGE_STATE_PATH,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium-desktop',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 960 } },
    },
  ],
});
```

- [ ] **Step 2: Add the npm script**

In `multi-agent/internal/commanderhub/webapp/package.json`, add to `"scripts"`:

```json
"test:e2e:live": "playwright test --config=playwright.live.config.ts"
```

Resulting `"scripts"` block looks like:

```json
"scripts": {
  "dev": "vite",
  "build": "tsc -b && vite build",
  "test": "vitest run",
  "e2e": "playwright test",
  "test:e2e:live": "playwright test --config=playwright.live.config.ts",
  "preview": "vite preview --host 127.0.0.1"
}
```

- [ ] **Step 3: Verify config loads + matches exactly one spec**

```bash
cd multi-agent/internal/commanderhub/webapp
npx playwright test --config=playwright.live.config.ts --list 2>&1 | tail -20
```

Expected: error message saying the test file `commander-live.spec.ts` doesn't exist yet — but NOT a "config error" / "cannot resolve testMatch" message. The "no tests found" / "no matching file" error is expected and acceptable at this point; it proves the config itself parsed correctly.

If the output shows other test files (`commander.spec.ts`), `testMatch` is wrong — investigate and fix before continuing.

- [ ] **Step 4: Commit**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login
git add multi-agent/internal/commanderhub/webapp/playwright.live.config.ts \
        multi-agent/internal/commanderhub/webapp/package.json
git commit -m "feat(commander): playwright.live.config.ts + test:e2e:live script

Live-target config sharing testDir with the mocked suite but
pinning testMatch to commander-live.spec.ts. No webServer block
(observer must be running). globalSetup wired to the live-login
helper; storageState resolved from STORAGE_STATE_PATH at config
load time (Playwright evaluates config BEFORE globalSetup runs, so
we use an imported const, not env round-trip)."
```

---

### Task 4: `commander-live.spec.ts` — the fresh-id assertion

The single test. Assumes globalSetup has run and `use.storageState` has
loaded a valid cookie.

**Files:**
- Create: `multi-agent/internal/commanderhub/webapp/src/e2e/commander-live.spec.ts`

**Interfaces:**
- Consumes: `OBSERVER_BASE`, `DRIVER_BASE` from `./live-login` (the latter unused but imported for clarity).
- Produces: a single test that, on success, proves the pr33 fresh-id rebind path is healthy end-to-end.

- [ ] **Step 1: Create the spec file**

Create `multi-agent/internal/commanderhub/webapp/src/e2e/commander-live.spec.ts`:

```ts
import { expect, test } from '@playwright/test';

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

test('fresh + button creates session and rebinds to real codex thread ID', async ({ page }) => {
  // 1. Navigate. storageState already populated by globalSetup.
  await page.goto('/commander/');
  await expect(page.getByTestId('daemon-tree')).toBeVisible();

  // 2. Find the codex daemon group + click its + button.
  // The daemon-new-session-btn lives inside the daemon-group whose
  // header text contains the daemon's display name. We grab the first
  // codex daemon by querying for the codex kind label.
  const codexDaemonGroup = page.locator('.daemon-group', { has: page.locator('text=/codex/i') }).first();
  await expect(codexDaemonGroup).toBeVisible();
  await codexDaemonGroup.locator('.daemon-new-session-btn').click();

  // 3. The pending row appears with a placeholder data-session-id (UUID).
  const pendingRow = codexDaemonGroup.locator('[data-testid="pending-session-row"] button[data-session-id]');
  await expect(pendingRow).toBeVisible();
  const placeholderID = await pendingRow.getAttribute('data-session-id');
  expect(placeholderID).toMatch(UUID_RE);

  // 4. Capture daemonID from the same group's existing rows OR from the
  // POST URL when it fires. Easier: query the daemon-group's data-* if
  // any; otherwise capture from the request URL below.

  // 5. Type a short prompt and pre-attach POST waiters BEFORE clicking
  // send so we don't race the request.
  await page.getByPlaceholder(/输入消息|message/i).fill('say hi');

  const turnRequestPromise = page.waitForRequest(
    (req) => /\/api\/commander\/daemons\/[^/]+\/sessions\/[^/]+\/turn$/.test(req.url())
      && req.method() === 'POST',
  );
  const turnResponsePromise = page.waitForResponse(
    (resp) => /\/api\/commander\/daemons\/[^/]+\/sessions\/[^/]+\/turn$/.test(resp.url())
      && resp.request().method() === 'POST',
  );

  await page.getByRole('button', { name: /发送|send/i }).click();

  // 6. Resolve POST request. Assert body.fresh === true.
  const turnReq = await turnRequestPromise;
  const reqBody = JSON.parse(turnReq.postData() || '{}') as { prompt: string; fresh?: boolean };
  expect(reqBody.fresh, 'fresh flag must be true on first turn of draft pending').toBe(true);

  // Extract daemonID from the URL for the detail-fetch assertion later.
  const urlMatch = turnReq.url().match(/\/daemons\/([^/]+)\/sessions\/([^/]+)\/turn$/);
  expect(urlMatch).not.toBeNull();
  const daemonID = urlMatch![1];
  const submittedID = urlMatch![2];
  expect(submittedID, 'submitted ID must equal placeholder').toBe(placeholderID);

  // 7. Resolve SSE response. The body is an SSE stream; the terminal
  // frame is event: done\ndata: {...}\n\n. Read all bytes and parse.
  const turnResp = await turnResponsePromise;
  const sseBody = await turnResp.text();
  const doneMatch = sseBody.match(/event:\s*done\s*\ndata:\s*(.+)\n/);
  expect(doneMatch, 'SSE stream must contain a done frame').not.toBeNull();
  const donePayload = JSON.parse(doneMatch![1]) as { result?: { session_id?: string } };
  const realID = donePayload.result?.session_id;
  expect(realID, 'done payload must contain result.session_id').toBeDefined();
  expect(realID, 'realID must be UUID-shaped').toMatch(UUID_RE);
  expect(realID, 'realID must differ from placeholder').not.toBe(placeholderID);

  // 8. PRE-ARM the detail-fetch waiter BEFORE asserting tree state.
  // The frontend's [selected, tree, pendingSession] effect can fire the
  // GET immediately after rebind — attaching the waiter later races.
  const detailPromise = page.waitForResponse(
    (resp) =>
      resp.url().endsWith(`/api/commander/daemons/${daemonID}/sessions/${realID}`)
      && resp.request().method() === 'GET'
      && resp.status() === 200,
    { timeout: 30_000 },
  );

  // 9. Tree must show a row with data-session-id === realID.
  const rebornRow = page.locator(`[data-testid="daemon-tree"] button[data-session-id="${realID}"]`);
  await expect(rebornRow).toBeVisible({ timeout: 30_000 });

  // 10. The placeholder row must be gone (rebound, not stacked).
  const placeholderRow = page.locator(`[data-testid="daemon-tree"] button[data-session-id="${placeholderID}"]`);
  await expect(placeholderRow).toHaveCount(0);

  // 11. await the pre-armed detail waiter — proves selection followed.
  await detailPromise;
});
```

- [ ] **Step 2: TypeScript build check**

```bash
cd multi-agent/internal/commanderhub/webapp
npx tsc --noEmit
```

Expected: no errors.

- [ ] **Step 3: Confirm config picks up exactly this one spec**

```bash
cd multi-agent/internal/commanderhub/webapp
npx playwright test --config=playwright.live.config.ts --list 2>&1 | tail -10
```

Expected: 1 test listed:
```
[chromium-desktop] › commander-live.spec.ts:N › fresh + button creates session and rebinds to real codex thread ID
Total: 1 test in 1 file
```

If MORE than 1 test is listed (e.g., the mocked `commander.spec.ts` got picked up), Task 3's `testMatch` is wrong — STOP and fix Task 3.

- [ ] **Step 4: Commit**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login
git add multi-agent/internal/commanderhub/webapp/src/e2e/commander-live.spec.ts
git commit -m "test(commander): live e2e for fresh-id rebind

Single test against the running observer + driver-codex. Clicks +,
captures the placeholder UUID, types a prompt, intercepts the POST
.../turn to assert body.fresh === true, parses the terminal SSE done
frame for result.session_id (the real codex-minted ID), then asserts
the daemon-tree row pins to that exact realID and the placeholder
row is gone. Finally awaits the pre-armed GET /sessions/<realID>
detail response to prove selection followed the rebind.

If any of these fail, pr33's fresh-id contract has regressed."
```

---

### Task 5: End-to-end smoke against live backend

Manual verification step. Not automatable — requires real OAuth.

**Files:** none

**Interfaces:** none

- [ ] **Step 1: Confirm observer + driver are running**

```bash
ps -ef | grep -E "observer-server|driver-agent" | grep -v grep
```

Expected: both processes listed. If not:

```bash
cd /root/multi-agent/multi-agent/tests/prod_test/observer-local
nohup ../bin/observer-server.linux-amd64 -config observer.yaml > /tmp/observer.log 2>&1 &
cd /root/multi-agent/multi-agent/tests/prod_test/driver-codex-local
nohup ../bin/driver-agent.linux-amd64 serve-daemon --config ./config.yaml --listen 127.0.0.1:18092 > /tmp/driver.log 2>&1 &
```

- [ ] **Step 2: Delete any stale cookie cache so the OAuth path runs**

```bash
rm -f /root/multi-agent/multi-agent/tests/prod_test/.playwright/observer-session.json
```

- [ ] **Step 3: Run the live e2e**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login/multi-agent/internal/commanderhub/webapp
npm run test:e2e:live
```

Expected output starts with the headed browser opening, then the banner appears in the terminal:

```
╔════════════════════════════════════════════════════════════════╗
║  OPEN THIS URL TO AUTHORIZE COMMANDER:                          ║
║                                                                  ║
║  https://agent.cs.ac.cn/oauth2/device?user_code=XXXX            ║
║                                                                  ║
║  Waiting up to 10 minutes...                                    ║
╚════════════════════════════════════════════════════════════════╝
```

Open the URL in any browser, authorize. The headed browser should auto-close once the daemon tree loads. The test proceeds through the fresh-id flow and exits with code 0.

Expected: `1 passed`.

- [ ] **Step 4: Verify the cookie cache was created**

```bash
ls -la /root/multi-agent/multi-agent/tests/prod_test/.playwright/observer-session.json
```

Expected: file exists, recent mtime, non-empty.

- [ ] **Step 5: Re-run to verify the cache-hit fast path**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login/multi-agent/internal/commanderhub/webapp
npm run test:e2e:live
```

Expected: no banner printed; no headed browser; test starts immediately and passes. globalSetup should complete in under 2 seconds.

- [ ] **Step 6: Confirm clean state**

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login
git status
```

Expected: no uncommitted changes (the cookie file is gitignored under `tests/prod_test/`).

- [ ] **Step 7: Smoke the existing mocked suite — confirm we didn't break it**

```bash
cd multi-agent/internal/commanderhub/webapp
npm run e2e
```

Expected: existing mocked suite still passes. The new config is a separate file; the new spec was excluded from the default config via the existing `testMatch` (or lack thereof — verify the default config still picks up only `commander.spec.ts`).

If the default config DOES try to run `commander-live.spec.ts`, add a `testIgnore: 'commander-live.spec.ts'` to the existing `playwright.config.ts`.

- [ ] **Step 8: Commit any followup adjustments + push**

If steps 5-7 surfaced issues, commit fixes; otherwise this step is a no-op.

```bash
cd /root/multi-agent/.claude/worktrees/e2e-live-login
git push -u origin worktree-e2e-live-login
```
