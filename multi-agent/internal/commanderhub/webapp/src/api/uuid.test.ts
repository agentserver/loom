import { afterEach, describe, expect, test, vi } from 'vitest';
import { randomUUID } from './uuid';

const UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('randomUUID', () => {
  test('returns a valid v4 UUID via crypto.randomUUID when available', () => {
    const id = randomUUID();
    expect(id).toMatch(UUID_V4_RE);
  });

  test('falls back to crypto.getRandomValues when crypto.randomUUID is missing (insecure-context simulation)', () => {
    const real = globalThis.crypto;
    const fake = {
      getRandomValues: real.getRandomValues.bind(real),
    } as unknown as Crypto;
    vi.stubGlobal('crypto', fake);
    const id = randomUUID();
    expect(id).toMatch(UUID_V4_RE);
  });

  test('throws when no crypto support at all', () => {
    vi.stubGlobal('crypto', undefined as unknown as Crypto);
    expect(() => randomUUID()).toThrow(/no crypto.getRandomValues/);
  });
});
