import { afterEach, expect, test, vi } from 'vitest';
import { isTurnInFlightError, parseSSEBlock, postTurn, TurnInFlightError } from './client';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

test('parses status SSE blocks', () => {
  expect(parseSSEBlock('event: status\ndata: {"text":"codex running"}')).toEqual({
    event: 'status',
    data: { text: 'codex running' },
  });
});

test('parses chunk SSE blocks', () => {
  expect(parseSSEBlock('event: chunk\ndata: {"text":"hi"}')).toEqual({
    event: 'chunk',
    data: { text: 'hi' },
  });
});

test('marks HTTP 409 turn responses as in-flight errors', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response('turn already in flight', { status: 409 })),
  );

  await expect(postTurn('d1', 's1', 'go', () => {})).rejects.toBeInstanceOf(TurnInFlightError);

  try {
    await postTurn('d1', 's1', 'go', () => {});
  } catch (err) {
    expect(isTurnInFlightError(err)).toBe(true);
  }
});
