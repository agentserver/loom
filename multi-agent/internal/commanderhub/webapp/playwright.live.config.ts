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
