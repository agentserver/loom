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
    let done = false;
    const sock = createConnection({ host, port });
    const fail = (err: Error) => { if (done) return; done = true; sock.destroy(); rej(err); };
    const timer = setTimeout(() => fail(new Error(`timeout ${host}:${port}`)), timeoutMs);
    sock.once('connect', () => { if (done) return; done = true; clearTimeout(timer); sock.end(); res(); });
    sock.once('error', (err: Error) => { clearTimeout(timer); fail(err); });
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
  const headless = !process.env.DISPLAY;
  const browser = await chromium.launch({ headless });
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
