// randomUUID returns a RFC4122 v4 UUID. Prefers crypto.randomUUID() when
// available (secure contexts only), otherwise assembles a v4 from
// crypto.getRandomValues which is available in every browser regardless of
// secure-context status — including the common case of accessing commander
// over http://<lan-ip>:<port>/.
export function randomUUID(): string {
  const g = globalThis as { crypto?: Crypto };
  if (g.crypto && typeof g.crypto.randomUUID === 'function') {
    return g.crypto.randomUUID();
  }
  if (!g.crypto || typeof g.crypto.getRandomValues !== 'function') {
    throw new Error('randomUUID: no crypto.getRandomValues available');
  }
  const bytes = new Uint8Array(16);
  g.crypto.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant 10xx
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`;
}
