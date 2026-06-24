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

test('ignores stale file preview responses after session changes', async () => {
  const oldPreview = deferred<Response>();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://commander.test');
      if (url.pathname.endsWith('/files')) {
        return jsonResponse({
          root: '/repo',
          path: '.',
          entries: [{ name: 'old.log', path: 'old.log', kind: 'file', size: 3 }],
        });
      }
      if (url.searchParams.get('path') === 'old.log') {
        return oldPreview.promise;
      }
      return jsonResponse({ path: 'other.log', size: 3, content: 'other content' });
    }),
  );

  const { rerender } = render(<FileExplorerPanel daemonID="d1" sessionID="s1" />);
  fireEvent.click(await screen.findByText('old.log'));
  rerender(<FileExplorerPanel daemonID="d1" sessionID="s2" />);

  await act(async () => {
    oldPreview.resolve(jsonResponse({ path: 'old.log', size: 3, content: 'old content' }));
    await oldPreview.promise;
  });

  expect(screen.queryByText('old content')).not.toBeInTheDocument();
});

test('expands directories lazily', async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://commander.test');
    if (url.pathname.endsWith('/files') && url.searchParams.get('path') === '.') {
      return jsonResponse({
        root: '/repo',
        path: '.',
        entries: [{ name: 'src', path: 'src', kind: 'dir', size: 0 }],
      });
    }
    if (url.pathname.endsWith('/files') && url.searchParams.get('path') === 'src') {
      return jsonResponse({
        root: '/repo',
        path: 'src',
        entries: [{ name: 'main.go', path: 'src/main.go', kind: 'file', size: 12 }],
      });
    }
    return jsonResponse({ path: 'src/main.go', size: 12, content: 'package main' });
  });
  vi.stubGlobal('fetch', fetchMock);

  render(<FileExplorerPanel daemonID="d1" sessionID="s1" />);

  fireEvent.click(await screen.findByRole('button', { name: /展开目录 src/ }));

  expect(await screen.findByText('main.go')).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledWith(
    '/api/commander/daemons/d1/sessions/s1/files?path=src',
    expect.objectContaining({ credentials: 'include' }),
  );
});

test('copies file and directory paths', async () => {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://commander.test');
      if (url.pathname.endsWith('/files')) {
        return jsonResponse({
          root: '/repo',
          path: '.',
          entries: [
            { name: 'src', path: 'src', kind: 'dir', size: 0 },
            { name: 'README.md', path: 'README.md', kind: 'file', size: 8 },
          ],
        });
      }
      return jsonResponse({ path: 'README.md', size: 8, content: 'readme' });
    }),
  );

  render(<FileExplorerPanel daemonID="d1" sessionID="s1" />);

  fireEvent.click(await screen.findByRole('button', { name: '复制路径 src' }));
  fireEvent.click(await screen.findByRole('button', { name: '复制路径 README.md' }));

  expect(writeText).toHaveBeenNthCalledWith(1, '/repo/src');
  expect(writeText).toHaveBeenNthCalledWith(2, '/repo/README.md');
});

test('copies full paths for windows roots', async () => {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      jsonResponse({
        root: 'C:\\repo',
        path: '.',
        entries: [{ name: 'main.go', path: 'src/main.go', kind: 'file', size: 8 }],
      }),
    ),
  );

  render(<FileExplorerPanel daemonID="d1" sessionID="s1" />);

  fireEvent.click(await screen.findByRole('button', { name: '复制路径 src/main.go' }));

  expect(writeText).toHaveBeenCalledWith('C:\\repo\\src\\main.go');
});

test('renderMode="sheet" calls onPreview with { preview, fullPath, displayPath } and omits inline preview', async () => {
  const onPreview = vi.fn();
  const fetchMock = vi.fn(async (input: RequestInfo) => {
    const url = typeof input === 'string' ? input : input.url;
    if (url.includes('/files?path=.')) {
      return new Response(JSON.stringify({
        root: '/root/project',
        path: '.',
        entries: [{ name: 'go.mod', path: 'go.mod', kind: 'file', size: 12 }],
      }), { status: 200 });
    }
    if (url.includes('/files/content?path=go.mod')) {
      return new Response(JSON.stringify({ path: 'go.mod', size: 12, content: 'module x' }), { status: 200 });
    }
    return new Response('not found', { status: 404 });
  });
  vi.stubGlobal('fetch', fetchMock);

  render(
    <FileExplorerPanel
      daemonID="d1"
      sessionID="s1"
      renderMode="sheet"
      onPreview={onPreview}
    />,
  );

  await screen.findByText('go.mod');
  // Sheet mode must NOT render the inline preview region.
  expect(screen.queryByText('No file selected')).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: /打开文件 go.mod/ }));
  await vi.waitFor(() => expect(onPreview).toHaveBeenCalledTimes(1));
  expect(onPreview.mock.calls[0][0]).toEqual({
    preview: expect.objectContaining({ path: 'go.mod', content: 'module x' }),
    fullPath: '/root/project/go.mod',
    displayPath: 'go.mod',
  });
});

test('renderMode="sheet" preserves directory expansion across external rerender (sheet open/close cycle)', async () => {
  const fetchMock = vi.fn(async (input: RequestInfo) => {
    const url = typeof input === 'string' ? input : input.url;
    if (url.includes('/files?path=.')) {
      return new Response(JSON.stringify({
        root: '/root/project',
        path: '.',
        entries: [
          { name: 'cmd', path: 'cmd', kind: 'dir' },
          { name: 'go.mod', path: 'go.mod', kind: 'file', size: 12 },
        ],
      }), { status: 200 });
    }
    if (url.includes('/files?path=cmd')) {
      return new Response(JSON.stringify({
        root: '/root/project',
        path: 'cmd',
        entries: [{ name: 'main.go', path: 'cmd/main.go', kind: 'file', size: 4 }],
      }), { status: 200 });
    }
    return new Response('not found', { status: 404 });
  });
  vi.stubGlobal('fetch', fetchMock);

  const { rerender } = render(
    <FileExplorerPanel daemonID="d1" sessionID="s1" renderMode="sheet" onPreview={vi.fn()} />,
  );
  await screen.findByText('cmd');
  fireEvent.click(screen.getByRole('button', { name: /展开目录 cmd/ }));
  await screen.findByText('main.go');

  // Simulate the parent rerendering the same component instance
  // (e.g. because the file-preview sheet opened/closed elsewhere).
  rerender(<FileExplorerPanel daemonID="d1" sessionID="s1" renderMode="sheet" onPreview={vi.fn()} />);
  expect(screen.getByText('main.go')).toBeInTheDocument();
});
