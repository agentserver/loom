import { expect, test } from 'vitest';
import { parseSSEBlock } from './client';

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
