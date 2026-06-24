import { expect, test } from '@playwright/test';

const treePayload = {
  daemons: [
    {
      daemon_id: 'd1',
      display_name: 'prod-codex',
      kind: 'codex',
      driver_version: 'v0.1.0',
      status: 'ok',
      sessions: [
        {
          daemon_id: 'd1',
          session_id: 's1',
          kind: 'codex',
          title: 'Fix commander session cache latency with a long title that must not overflow',
          working_dir: '/root/multi-agent/multi-agent/tests/prod_test/driver-codex',
          updated_at: '2026-06-16T12:00:00Z',
          message_count: 18,
          preview: 'I will add cache invalidation.',
          turn_state: 'answering',
          active_worker: false,
          awaiting_approval: false,
        },
      ],
    },
  ],
};

async function assertHitArea(locator: import('@playwright/test').Locator, name: string) {
  const box = await locator.boundingBox();
  if (!box) throw new Error(`${name} has no bounding box`);
  expect.soft(box.height, `${name}.height`).toBeGreaterThanOrEqual(44);
  expect.soft(box.width, `${name}.width`).toBeGreaterThanOrEqual(44);
}

const fileMocks = {
  rootListing: {
    root: '/root/project',
    path: '.',
    entries: [
      { name: 'go.mod', path: 'go.mod', kind: 'file', size: 40 },
      { name: 'README.md', path: 'README.md', kind: 'file', size: 80 },
    ],
  },
  goMod: { path: 'go.mod', size: 40, content: 'module x' },
  readme: { path: 'README.md', size: 80, content: '# project' },
};

// The default treePayload at the top of this file uses
// turn_state: 'answering' so the existing desktop test exercises an
// in-flight turn. Mobile tests below want an idle composer; install
// this fixture by overriding the tree route at the top of each mobile
// test that needs to type into the composer.
const idleTreePayload = {
  daemons: [
    {
      ...treePayload.daemons[0],
      sessions: [
        { ...treePayload.daemons[0].sessions[0], turn_state: 'idle' },
      ],
    },
  ],
};

async function mockIdleTree(page: import('@playwright/test').Page) {
  await page.route('**/api/commander/tree', (route) => route.fulfill({ json: idleTreePayload }));
}

test.beforeEach(async ({ page }) => {
  await page.route('**/api/commander/tree', async (route) => {
    await route.fulfill({ json: treePayload });
  });
  await page.route('**/api/commander/daemons/d1/sessions/s1', async (route) => {
    await route.fulfill({
      json: {
        session: {
          ID: 's1',
          Title: treePayload.daemons[0].sessions[0].title,
          WorkingDir: treePayload.daemons[0].sessions[0].working_dir,
        },
        messages: [
          { role: 'user', text: '为什么每次 list session 都这么卡？' },
          { role: 'assistant', text: '```go\nfunc cache() {}\n```' },
        ],
      },
    });
  });
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', async (route) => {
    await route.fulfill({
      json: {
        root: '/root/project',
        path: '.',
        entries: [{ name: 'go.mod', path: 'go.mod', kind: 'file', size: 40 }],
      },
    });
  });
});

test('desktop three-pane workbench is stable', async ({ page }, testInfo) => {
  await page.goto('/commander/');
  await expect(page.getByTestId('commander-shell')).toBeVisible();

  if (testInfo.project.name.includes('desktop')) {
    await page.getByRole('button', { name: /Fix commander session cache latency/ }).click();
    await expect(page.getByTestId('daemon-tree')).toBeVisible();
    await expect(page.getByTestId('chat-workspace')).toBeVisible();
    await expect(page.getByTestId('file-panel')).toBeVisible();
    await expect(page.getByText('func cache')).toBeVisible();
    await expect(page.getByText('go.mod')).toBeVisible();
    await expect(page).toHaveScreenshot('commander-desktop.png', { fullPage: true });
  }
});

test('desktop panes own vertical scrolling and chat opens at bottom', async ({ page }, testInfo) => {
  if (!testInfo.project.name.includes('desktop')) return;

  const sessions = Array.from({ length: 80 }, (_, index) => ({
    ...treePayload.daemons[0].sessions[0],
    session_id: `s${index + 1}`,
    title: `Session ${index + 1} with enough text to fill the sidebar`,
  }));
  await page.route('**/api/commander/tree', async (route) => {
    await route.fulfill({
      json: {
        daemons: [{ ...treePayload.daemons[0], sessions }],
      },
    });
  });
  await page.route('**/api/commander/daemons/d1/sessions/s1', async (route) => {
    await route.fulfill({
      json: {
        session: {
          ID: 's1',
          Title: sessions[0].title,
          WorkingDir: sessions[0].working_dir,
        },
        messages: Array.from({ length: 70 }, (_, index) => ({
          role: index % 2 === 0 ? 'user' : 'assistant',
          text: `message ${index + 1}\n\n` + 'content line\n'.repeat(5),
        })),
      },
    });
  });
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', async (route) => {
    await route.fulfill({
      json: {
        root: '/root/project',
        path: '.',
        entries: Array.from({ length: 90 }, (_, index) => ({
          name: `file-${String(index + 1).padStart(2, '0')}.txt`,
          path: `file-${String(index + 1).padStart(2, '0')}.txt`,
          kind: 'file',
          size: 40,
        })),
      },
    });
  });

  await page.goto('/commander/');
  await page.getByRole('button', { name: /Session 1 with enough text/ }).first().click();
  await expect(page.getByText('message 70')).toBeVisible();

  const metrics = await page.evaluate(() => {
    const daemonTree = document.querySelector('[data-testid="daemon-tree"]') as HTMLElement;
    const messages = document.querySelector('[data-testid="message-list"]') as HTMLElement;
    const files = document.querySelector('[data-testid="file-panel"]') as HTMLElement;
    const html = document.documentElement;
    return {
      pageScrolls: html.scrollHeight > html.clientHeight,
      daemonScrollable: daemonTree.scrollHeight > daemonTree.clientHeight,
      messagesScrollable: messages.scrollHeight > messages.clientHeight,
      filesScrollable: files.scrollHeight > files.clientHeight,
      messagesAtBottom: messages.scrollTop + messages.clientHeight >= messages.scrollHeight - 2,
    };
  });

  expect(metrics).toEqual({
    pageScrolls: false,
    daemonScrollable: true,
    messagesScrollable: true,
    filesScrollable: true,
    messagesAtBottom: true,
  });
});

test('non-desktop: auto-selects first session and chat is live', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.goto('/commander/');
  await expect(page.getByRole('heading', { level: 1, name: /Fix commander session cache latency/ })).toBeVisible();
  await expect(page.getByLabel('输入提示词')).toBeEnabled();
});

test('non-desktop: empty tree renders disabled composer + hint', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await page.route('**/api/commander/tree', (route) => route.fulfill({ json: { daemons: [] } }));
  await page.goto('/commander/');
  await expect(page.getByLabel('输入提示词')).toBeDisabled();
  await expect(page.getByRole('button', { name: '发送' })).toBeDisabled();
  await expect(
    page.getByText('No sessions yet — open Sessions to pick one once a daemon appears'),
  ).toBeVisible();
});

test('non-desktop: open sessions drawer, select session, send prompt', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  let turnCalled = false;
  await page.route('**/api/commander/daemons/d1/sessions/s1/turn', async (route) => {
    turnCalled = true;
    await route.fulfill({ body: 'event: done\ndata: {"result":{}}\n\n', headers: { 'content-type': 'text/event-stream' } });
  });
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Sessions' }).click();
  const sessionsDrawer = page.getByTestId('drawer-left');
  await expect(sessionsDrawer).toBeVisible();
  await expect(sessionsDrawer.getByTestId('daemon-tree')).toBeVisible();
  await sessionsDrawer.getByRole('button', { name: /Fix commander session cache latency/ }).click();
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);
  await page.getByLabel('输入提示词').fill('hi');
  await page.getByRole('button', { name: '发送' }).click();
  await expect.poll(() => turnCalled).toBe(true);
});

test('non-desktop: open files drawer, preview file, then preview a second', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=README.md', (route) => route.fulfill({ json: fileMocks.readme }));
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Files' }).click();
  await expect(page.getByTestId('drawer-right').getByText('go.mod')).toBeVisible();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  await expect(page.getByTestId('file-preview-sheet')).toBeVisible();
  await page.getByRole('button', { name: /Copy path/i }).click();
  const clip1 = await page.evaluate(() => navigator.clipboard.readText());
  expect(clip1).toBe('/root/project/go.mod');
  await page.getByRole('button', { name: '关闭预览' }).click();
  await expect(page.getByTestId('file-preview-sheet')).toHaveCount(0);
  await expect(page.getByTestId('drawer-right').getByText('go.mod')).toBeVisible();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 README.md/ }).click();
  await expect(page.getByTestId('file-preview-sheet').getByText('# project')).toBeVisible();
});

test('non-desktop: browser back closes overlays in stack order', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('about:blank');
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Sessions' }).click();
  await expect(page.getByTestId('drawer-left')).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);

  await page.getByRole('button', { name: 'Files' }).click();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  await expect(page.getByTestId('file-preview-sheet')).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId('file-preview-sheet')).toHaveCount(0);
  await expect(page.getByTestId('drawer-right')).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId('drawer-right')).toHaveCount(0);
});

test('non-desktop: no horizontal overflow at 360/390/430 and 834', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.goto('/commander/');
  for (const w of [360, 390, 430, 834]) {
    await page.setViewportSize({ width: w, height: 844 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect.soft(overflow, `viewport ${w}px`).toBe(false);
    await assertHitArea(page.getByRole('button', { name: 'Sessions' }), `Sessions@${w}`);
    await assertHitArea(page.getByRole('button', { name: 'Files' }), `Files@${w}`);
  }
});

test('non-desktop: drawer interactive controls meet 44px hit area', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  // Override the tree so Sessions drawer renders a parent + a subagent
  // child; this exposes a real `.session-toggle` to assert against. The
  // mockIdleTree helper covers the single-session default; for this test
  // we install a richer fixture.
  await page.route('**/api/commander/tree', (route) => route.fulfill({ json: {
    daemons: [
      {
        ...idleTreePayload.daemons[0],
        sessions: [
          { ...idleTreePayload.daemons[0].sessions[0] },
          {
            ...idleTreePayload.daemons[0].sessions[0],
            session_id: 'child-1',
            title: 'Child subagent session',
            origin: 'subagent',
            parent_id: 's1',
          },
        ],
      },
    ],
  }}));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('/commander/');

  await page.getByRole('button', { name: 'Sessions' }).click();
  const left = page.getByTestId('drawer-left');
  // Wait for the session list to actually render so .all() does not silently
  // return an empty array and turn the assertHitArea loop into a no-op.
  await expect(left.locator('.session-row').first()).toBeVisible();
  await expect(left.locator('.session-toggle').first()).toBeVisible();
  const sessionRows = await left.locator('.session-row').all();
  expect(sessionRows.length).toBeGreaterThan(0);
  for (const row of sessionRows) await assertHitArea(row, '.session-row');
  const sessionToggles = await left.locator('.session-toggle').all();
  expect(sessionToggles.length).toBeGreaterThan(0);
  for (const toggle of sessionToggles) await assertHitArea(toggle, '.session-toggle');
  await assertHitArea(left.locator('.mobile-drawer-close'), 'drawer-left close');

  // New-session hit area: + button inside the Sessions drawer
  await assertHitArea(left.locator('.daemon-new-session-btn'), '.daemon-new-session-btn');
  // Click + to materialize a draft virtual row; handleCreate closes the drawer.
  await left.locator('.daemon-new-session-btn').click();
  // Drawer closes after + is clicked.
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);
  // Re-open Sessions drawer to access the pending virtual row and its × button.
  await page.getByRole('button', { name: 'Sessions' }).click();
  const drawerWithPending = page.getByTestId('drawer-left');
  await expect(drawerWithPending.locator('[data-testid="pending-session-row"]')).toBeVisible();
  await assertHitArea(drawerWithPending.locator('.session-discard-btn'), '.session-discard-btn');
  // Discard the draft — restores the no-pending state.
  await drawerWithPending.locator('.session-discard-btn').click();
  await expect(drawerWithPending.locator('[data-testid="pending-session-row"]')).toHaveCount(0);
  // Re-select s1 by clicking its session-row button so handleSelectSession
  // closes the drawer. Use .session-row (not the toggle button) to avoid the
  // strict-mode violation — the s1 toggle aria-label also matches the regex.
  await drawerWithPending.locator('.session-row').filter({ hasText: /Fix commander session cache latency/ }).click();
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);

  await page.getByRole('button', { name: 'Files' }).click();
  const right = page.getByTestId('drawer-right');
  await expect(right.getByText('go.mod')).toBeVisible();
  const fileRows = await right.locator('.file-row').all();
  expect(fileRows.length).toBeGreaterThan(0);
  for (const row of fileRows) await assertHitArea(row, '.file-row');
  const copyButtons = await right.locator('.file-copy-button').all();
  expect(copyButtons.length).toBeGreaterThan(0);
  for (const cp of copyButtons) await assertHitArea(cp, '.file-copy-button');
  await right.getByRole('button', { name: /打开文件 go.mod/ }).click();
  const sheet = page.getByTestId('file-preview-sheet');
  await expect(sheet).toBeVisible();
  await assertHitArea(sheet.locator('.file-preview-sheet-close'), 'sheet close');
  await assertHitArea(sheet.locator('.file-preview-sheet-copy'), 'sheet copy');
});

test('non-desktop: login screen is touch-friendly', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await page.route('**/api/commander/tree', (route) => route.fulfill({ status: 401, body: 'unauthorized' }));
  await page.route('**/api/commander/login', (route) => route.fulfill({ json: { login_id: 'id', verification_uri_complete: 'https://example.test/verify' } }));
  await page.route('**/api/commander/login/poll**', (route) => route.fulfill({ json: { status: 'pending' } }));
  await page.goto('/commander/');
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  expect(overflow).toBe(false);
  const loginBtn = page.getByRole('button', { name: '用 agentserver 登录' });
  await assertHitArea(loginBtn, 'login button');
  await loginBtn.click();
  const verifyLink = page.getByRole('link', { name: '打开授权页面' });
  await expect(verifyLink).toBeVisible();
  await assertHitArea(verifyLink, 'verify link');
});

test('desktop: no auto-select preserves current behavior', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-desktop') test.skip();
  // Return the same title the existing top-of-file detail mock uses so the
  // post-click heading assertion is unambiguous regardless of route ordering.
  // detailFetched is the real desktop-no-auto-select gate.
  let detailFetched = false;
  await page.route('**/api/commander/daemons/d1/sessions/s1', (route) => {
    detailFetched = true;
    return route.fulfill({
      json: {
        session: {
          ID: 's1',
          Title: treePayload.daemons[0].sessions[0].title,
          WorkingDir: treePayload.daemons[0].sessions[0].working_dir,
        },
        messages: [],
      },
    });
  });
  await page.goto('/commander/');
  await expect(page.getByRole('heading', { level: 1, name: 'Session' })).toBeVisible();
  expect(detailFetched).toBe(false);
  await page.getByRole('button', { name: /Fix commander session cache latency/ }).click();
  await expect(page.getByRole('heading', { level: 1, name: /Fix commander session cache latency/ })).toBeVisible();
  expect(detailFetched).toBe(true);
});

test('non-desktop: resizing to desktop while two overlays are stacked leaves no phantom history', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-mobile') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('about:blank');
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Files' }).click();
  expect(await page.evaluate(() => (history.state as { commanderOverlay?: string } | null)?.commanderOverlay)).toBe('files');
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  expect(await page.evaluate(() => (history.state as { commanderOverlay?: string } | null)?.commanderOverlay)).toBe('preview');
  await page.setViewportSize({ width: 1280, height: 900 });
  // Wait for the shell swap: the desktop shell has the bare
  // `.commander-shell` class (no `commander-shell-mobile`) and renders the
  // file panel inline. `.commander-shell` alone matches BOTH shells
  // (mobile carries `commander-shell commander-shell-mobile`), so wait on
  // a desktop-only locator instead.
  await expect(page.locator('.commander-shell:not(.commander-shell-mobile)')).toBeVisible();
  await expect(page.getByTestId('file-panel')).toBeVisible();
  // history.go(-2) is asynchronous; poll until the controller's payload is
  // drained so the test does not race the matchMedia handler.
  await expect.poll(async () =>
    page.evaluate(() => (history.state as { commanderOverlay?: string } | null)?.commanderOverlay ?? null),
  ).toBeNull();

  await page.setViewportSize({ width: 390, height: 844 });
  // Wait for MobileShell to remount before asserting overlays are absent.
  await expect(page.locator('.commander-shell-mobile')).toBeVisible();
  await expect(page.getByTestId('file-preview-sheet')).toHaveCount(0);
  await expect(page.getByTestId('drawer-right')).toHaveCount(0);
  const before = page.url();
  await page.goBack();
  expect(page.url()).not.toBe(before);
  expect(page.url()).toBe('about:blank');
});

test('non-desktop: chat default state baseline screenshot', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();
  await mockIdleTree(page);
  await page.goto('/commander/');
  await expect(page.getByLabel('输入提示词')).toBeEnabled();
  const name = testInfo.project.name === 'chromium-mobile' ? 'commander-mobile.png' : 'commander-tablet-portrait.png';
  await expect(page).toHaveScreenshot(name, { fullPage: true });
});

test('non-desktop: mobile sessions drawer + file preview screenshots', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-mobile') test.skip();
  await mockIdleTree(page);
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', (route) => route.fulfill({ json: fileMocks.rootListing }));
  await page.route('**/api/commander/daemons/d1/sessions/s1/files/content?path=go.mod', (route) => route.fulfill({ json: fileMocks.goMod }));
  await page.goto('/commander/');
  await page.getByRole('button', { name: 'Sessions' }).click();
  await expect(page.getByTestId('drawer-left')).toBeVisible();
  await expect(page).toHaveScreenshot('commander-mobile-sessions-drawer.png');
  await page.goBack();
  await page.getByRole('button', { name: 'Files' }).click();
  await page.getByTestId('drawer-right').getByRole('button', { name: /打开文件 go.mod/ }).click();
  await expect(page.getByTestId('file-preview-sheet')).toBeVisible();
  await expect(page).toHaveScreenshot('commander-mobile-file-preview.png');
});

test('desktop: + button creates pending row, turn POSTs, tree refresh swaps placeholder with real row', async ({ page }, testInfo) => {
  if (testInfo.project.name !== 'chromium-desktop') test.skip();

  const newSessionTitle = 'Real title from backend';

  // The app generates a random UUID for the new session via crypto.randomUUID().
  // Capture it from the first turn request so the tree mock can include the real row.
  let capturedSID: string | null = null;
  let treeRequestCount = 0;
  await page.route('**/api/commander/tree', (route) => {
    treeRequestCount++;
    const sessions = [{ ...treePayload.daemons[0].sessions[0], turn_state: 'idle' as const }];
    // Once we have the real session ID (captured from the turn request), include
    // it in subsequent tree responses so the virtual row is replaced.
    if (capturedSID != null) {
      sessions.push({
        ...treePayload.daemons[0].sessions[0],
        session_id: capturedSID,
        title: newSessionTitle,
        turn_state: 'idle' as const,
      });
    }
    return route.fulfill({ json: { daemons: [{ ...treePayload.daemons[0], sessions }] } });
  });

  let turnCalled = false;
  // Wildcard detail mock: handles the detail fetch for the new UUID session
  // after turn completes. The glob `sessions/*` matches single-segment IDs
  // (e.g. /sessions/s1, /sessions/<uuid>) but NOT /sessions/*/turn or
  // /sessions/*/files (those have an extra path segment).
  await page.route('**/api/commander/daemons/d1/sessions/*', (route) => {
    const url = route.request().url();
    const sid = url.match(/\/sessions\/([^/?]+)(?:\?|$)/)?.[1] ?? '';
    return route.fulfill({
      json: { session: { ID: sid, Title: newSessionTitle, WorkingDir: '/root/project' }, messages: [] },
    });
  });
  // Turn route registered AFTER the wildcard so it takes precedence (LIFO).
  await page.route('**/api/commander/daemons/d1/sessions/*/turn', async (route) => {
    // Capture the UUID from the URL for use in the tree mock above.
    const url = route.request().url();
    const match = url.match(/\/sessions\/([^/]+)\/turn/);
    if (match) capturedSID = match[1];
    turnCalled = true;
    await route.fulfill({ body: 'event: done\ndata: {"result":{}}\n\n', headers: { 'content-type': 'text/event-stream' } });
  });

  await page.goto('/commander/');
  const treesBeforeClick = treeRequestCount;

  // Click + to create a draft virtual row.
  const newSessionBtn = page.locator('.daemon-new-session-btn');
  await expect(newSessionBtn).toBeVisible();
  await newSessionBtn.click();

  // Virtual row with '待提交' appears in the tree.
  const daemonTree = page.getByTestId('daemon-tree');
  await expect(daemonTree.locator('[data-testid="pending-session-row"]')).toBeVisible();
  await expect(daemonTree.getByText('待提交', { exact: false })).toBeVisible();

  // Send a prompt — this fires the turn POST against the random UUID.
  await page.getByLabel('输入提示词').fill('bootstrap this session');
  await page.getByRole('button', { name: '发送' }).click();
  await expect.poll(() => turnCalled).toBe(true);

  // After the turn, phase flips to 'submitting' → text briefly shows '同步中…'.
  // loadTree is then called; subsequent tree responses include the real row.
  // Poll until tree refresh resolved the pending session.
  await expect.poll(() => treeRequestCount).toBeGreaterThanOrEqual(treesBeforeClick + 1);
  await expect(daemonTree.getByText('待提交', { exact: false })).toHaveCount(0);
  await expect(daemonTree.getByText('同步中…', { exact: false })).toHaveCount(0);

  // The real backend title must now appear in the tree.
  await expect(daemonTree.getByRole('button', { name: new RegExp(newSessionTitle) })).toBeVisible();
});

test('non-desktop: + in Sessions drawer creates pending, drawer closes, turn → tree refresh swaps placeholder with real row', async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-desktop') test.skip();

  const newSessionTitle = 'Real title from backend';

  // The app generates a random UUID for the pending session; capture it from
  // the turn request so the tree mock can include the real row afterwards.
  let capturedSID: string | null = null;
  let treeRequestCount = 0;
  await page.route('**/api/commander/tree', (route) => {
    treeRequestCount++;
    const sessions = [{ ...treePayload.daemons[0].sessions[0], turn_state: 'idle' as const }];
    if (capturedSID != null) {
      sessions.push({
        ...treePayload.daemons[0].sessions[0],
        session_id: capturedSID,
        title: newSessionTitle,
        turn_state: 'idle' as const,
      });
    }
    return route.fulfill({ json: { daemons: [{ ...treePayload.daemons[0], sessions }] } });
  });

  let turnCalled = false;
  // Wildcard detail mock for any session ID (LIFO: registered before turn handler).
  await page.route('**/api/commander/daemons/d1/sessions/*', (route) => {
    const url = route.request().url();
    const sid = url.match(/\/sessions\/([^/?]+)(?:\?|$)/)?.[1] ?? '';
    return route.fulfill({
      json: { session: { ID: sid, Title: newSessionTitle, WorkingDir: '/root/project' }, messages: [] },
    });
  });
  // Turn route registered AFTER wildcard detail so it has higher priority (LIFO).
  await page.route('**/api/commander/daemons/d1/sessions/*/turn', async (route) => {
    const url = route.request().url();
    const match = url.match(/\/sessions\/([^/]+)\/turn/);
    if (match) capturedSID = match[1];
    turnCalled = true;
    await route.fulfill({ body: 'event: done\ndata: {"result":{}}\n\n', headers: { 'content-type': 'text/event-stream' } });
  });

  await page.goto('/commander/');
  const treesBeforeClick = treeRequestCount;

  // Open Sessions drawer and click the + button inside it.
  await page.getByRole('button', { name: 'Sessions' }).click();
  const sessionsDrawer = page.getByTestId('drawer-left');
  await expect(sessionsDrawer).toBeVisible();
  await expect(sessionsDrawer.locator('.daemon-new-session-btn')).toBeVisible();
  await sessionsDrawer.locator('.daemon-new-session-btn').click();

  // handleCreate closes the drawer after calling onCreateSession.
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);

  // Re-open Sessions drawer to verify the virtual pending row is present.
  await page.getByRole('button', { name: 'Sessions' }).click();
  const drawerWithPending = page.getByTestId('drawer-left');
  await expect(drawerWithPending.locator('[data-testid="pending-session-row"]')).toBeVisible();
  await expect(drawerWithPending.getByText('待提交', { exact: false })).toBeVisible();
  // Close drawer before sending prompt.
  // page.goBack() is NOT used here — at this point only one overlay entry has
  // been pushed (the re-open of the drawer after + was clicked). A goBack()
  // would pop that entry correctly in isolation but the previous goBack() that
  // closed the drawer after clicking + already consumed the first entry; if a
  // future path adjustment changes the push count this comment documents why
  // we use an explicit close instead of goBack(). See the brief's note: goBack()
  // after overlays in this test risks navigating out of /commander/.
  await page.getByTestId('drawer-left').getByRole('button', { name: /^关闭/ }).click();
  await expect(page.getByTestId('drawer-left')).toHaveCount(0);

  // Send a prompt using the composer (focused on the new draft session).
  await page.getByLabel('输入提示词').fill('hello from mobile new session');
  await page.getByRole('button', { name: '发送' }).click();
  await expect.poll(() => turnCalled).toBe(true);

  // Poll until tree refresh resolved the pending session.
  await expect.poll(() => treeRequestCount).toBeGreaterThanOrEqual(treesBeforeClick + 1);

  // Re-open Sessions drawer to confirm the virtual row is gone and real title appears.
  await page.getByRole('button', { name: 'Sessions' }).click();
  const drawerAfter = page.getByTestId('drawer-left');
  await expect(drawerAfter.getByText('待提交', { exact: false })).toHaveCount(0);
  await expect(drawerAfter.getByText('同步中…', { exact: false })).toHaveCount(0);
  await expect(drawerAfter.getByRole('button', { name: new RegExp(newSessionTitle) })).toBeVisible();
});
