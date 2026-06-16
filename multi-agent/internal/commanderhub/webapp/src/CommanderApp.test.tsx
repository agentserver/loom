import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { CommanderApp } from './CommanderApp';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function streamResponse() {
  let controller: ReadableStreamDefaultController<Uint8Array> | null = null;
  const response = new Response(
    new ReadableStream<Uint8Array>({
      start(c) {
        controller = c;
      },
    }),
    { status: 200 },
  );
  return { response, get controller() { return controller; } };
}

test('keeps a session turn stream current after navigating away and back', async () => {
  const turn = streamResponse();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://commander.test');
      if (url.pathname === '/api/commander/tree') {
        return jsonResponse({
          daemons: [
            {
              daemon_id: 'd1',
              display_name: 'prod-codex',
              kind: 'codex',
              status: 'ok',
              sessions: [
                {
                  daemon_id: 'd1',
                  session_id: 'a',
                  kind: 'codex',
                  title: 'Session A',
                  turn_state: 'idle',
                  active_worker: false,
                  awaiting_approval: false,
                },
                {
                  daemon_id: 'd1',
                  session_id: 'b',
                  kind: 'codex',
                  title: 'Session B',
                  turn_state: 'idle',
                  active_worker: false,
                  awaiting_approval: false,
                },
              ],
            },
          ],
        });
      }
      if (url.pathname.endsWith('/files')) {
        return jsonResponse({ root: '/repo', path: '.', entries: [] });
      }
      if (url.pathname.endsWith('/turn') && init?.method === 'POST') {
        return turn.response;
      }
      if (url.pathname.endsWith('/sessions/a')) {
        return jsonResponse({ session: { ID: 'a', Title: 'Session A' }, messages: [] });
      }
      if (url.pathname.endsWith('/sessions/b')) {
        return jsonResponse({ session: { ID: 'b', Title: 'Session B' }, messages: [] });
      }
      return jsonResponse({});
    }),
  );

  render(<CommanderApp />);
  fireEvent.click(await screen.findByRole('button', { name: /Session A/ }));
  fireEvent.change(screen.getByRole('textbox', { name: '输入提示词' }), { target: { value: 'go' } });
  fireEvent.submit((screen.getByRole('textbox', { name: '输入提示词' }) as HTMLTextAreaElement).form!);
  await waitFor(() => expect(turn.controller).not.toBeNull());

  fireEvent.click(screen.getByRole('button', { name: /Session B/ }));
  fireEvent.click(screen.getByRole('button', { name: /Session A/ }));

  await act(async () => {
    turn.controller?.enqueue(new TextEncoder().encode('event: done\ndata: {"result":{}}\n\n'));
    turn.controller?.close();
  });

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('已回答完毕'));
});
