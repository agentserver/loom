export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(path, { credentials: 'include' });
  if (res.status === 401) throw new Error('unauthorized');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return (await res.json()) as T;
}
