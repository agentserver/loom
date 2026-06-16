export function StatusBadge({ state }: { state: string }) {
  return <span className={`status-badge status-${state}`}>{state}</span>;
}
