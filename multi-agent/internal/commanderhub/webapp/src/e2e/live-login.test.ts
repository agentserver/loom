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
