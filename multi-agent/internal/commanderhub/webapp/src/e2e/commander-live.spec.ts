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
  // The textarea uses aria-label="输入提示词" (not a placeholder attribute).
  await page.getByLabel('输入提示词').fill('say hi');

  const turnRequestPromise = page.waitForRequest(
    (req) => /\/api\/commander\/daemons\/[^/]+\/sessions\/[^/]+\/turn$/.test(req.url())
      && req.method() === 'POST',
  );
  const turnResponsePromise = page.waitForResponse(
    (resp) => /\/api\/commander\/daemons\/[^/]+\/sessions\/[^/]+\/turn$/.test(resp.url())
      && resp.request().method() === 'POST',
  );

  // Send button text is 发送 (Chinese only, no English variant).
  await page.getByRole('button', { name: '发送' }).click();

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
