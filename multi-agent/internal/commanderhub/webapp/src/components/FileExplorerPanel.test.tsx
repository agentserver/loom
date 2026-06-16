import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { FileExplorerPanel, FilePreview } from './FileExplorerPanel';

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

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

test('shows too-large file metadata instead of content', () => {
  render(<FilePreview preview={{ path: 'large.log', size: 3_000_000, too_large: true }} />);
  expect(screen.getByText('large.log')).toBeInTheDocument();
  expect(screen.getByText(/2MB/)).toBeInTheDocument();
});

test('ignores stale file preview responses after a newer click', async () => {
  const oldPreview = deferred<Response>();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://commander.test');
      if (url.pathname.endsWith('/files')) {
        return jsonResponse({
          root: '/repo',
          path: '.',
          entries: [
            { name: 'old.log', path: 'old.log', kind: 'file', size: 3 },
            { name: 'new.log', path: 'new.log', kind: 'file', size: 3 },
          ],
        });
      }
      if (url.searchParams.get('path') === 'old.log') {
        return oldPreview.promise;
      }
      return jsonResponse({ path: 'new.log', size: 3, content: 'new content' });
    }),
  );

  render(<FileExplorerPanel daemonID="d1" sessionID="s1" />);
  fireEvent.click(await screen.findByText('old.log'));
  fireEvent.click(await screen.findByText('new.log'));
  expect(await screen.findByText('new content')).toBeInTheDocument();

  await act(async () => {
    oldPreview.resolve(jsonResponse({ path: 'old.log', size: 3, content: 'old content' }));
    await oldPreview.promise;
  });

  expect(screen.getByText('new content')).toBeInTheDocument();
  expect(screen.queryByText('old content')).not.toBeInTheDocument();
});
