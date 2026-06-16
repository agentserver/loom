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
          { Role: 'user', Text: '为什么每次 list session 都这么卡？' },
          { Role: 'assistant', Text: '```go\nfunc cache() {}\n```' },
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

test('mobile prioritizes chat without horizontal overflow', async ({ page }, testInfo) => {
  await page.goto('/commander/');

  if (testInfo.project.name.includes('mobile')) {
    await expect(page.getByTestId('chat-workspace')).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow).toBe(false);
  }
});
