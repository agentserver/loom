import type { TurnState } from '../api/types';

export function StatusBadge({ state }: { state: TurnState | string }) {
  return <span className={`status-badge status-${state}`}>{state}</span>;
}
