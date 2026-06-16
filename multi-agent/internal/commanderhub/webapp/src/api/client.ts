export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(path, { credentials: 'include' });
  if (res.status === 401) throw new Error('unauthorized');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return (await res.json()) as T;
}

export function sessionPath(daemonID: string, sessionID: string) {
  return `/api/commander/daemons/${encodeURIComponent(daemonID)}/sessions/${encodeURIComponent(sessionID)}`;
}

export function filesPath(daemonID: string, sessionID: string, path: string) {
  return `${sessionPath(daemonID, sessionID)}/files?path=${encodeURIComponent(path)}`;
}

export function fileContentPath(daemonID: string, sessionID: string, path: string) {
  return `${sessionPath(daemonID, sessionID)}/files/content?path=${encodeURIComponent(path)}`;
}

export function parseSSEBlock(block: string): { event: string; data: unknown } {
  let event = 'message';
  const dataLines: string[] = [];

  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith('event:')) {
      event = line.slice('event:'.length).trim();
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice('data:'.length).trimStart());
    }
  }

  const dataText = dataLines.join('\n');
  if (!dataText) return { event, data: null };

  try {
    return { event, data: JSON.parse(dataText) as unknown };
  } catch {
    return { event, data: dataText };
  }
}

export async function postTurn(
  daemonID: string,
  sessionID: string,
  prompt: string,
  onEvent: (event: string, data: unknown) => void,
): Promise<void> {
  const res = await fetch(`${sessionPath(daemonID, sessionID)}/turn`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });
  if (res.status === 409) throw new Error('turn already in flight');
  if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  for (;;) {
    const { value, done } = await reader.read();
    buffer = (buffer + decoder.decode(value, { stream: !done })).replace(/\r\n/g, '\n');

    let boundary = buffer.indexOf('\n\n');
    while (boundary >= 0) {
      const block = buffer.slice(0, boundary).trim();
      buffer = buffer.slice(boundary + 2);
      if (block) {
        const parsed = parseSSEBlock(block);
        onEvent(parsed.event, parsed.data);
      }
      boundary = buffer.indexOf('\n\n');
    }

    if (done) break;
  }

  const trailing = buffer.trim();
  if (trailing) {
    const parsed = parseSSEBlock(trailing);
    onEvent(parsed.event, parsed.data);
  }
}
