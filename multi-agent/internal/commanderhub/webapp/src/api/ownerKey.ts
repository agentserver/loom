import type { SessionRow } from './types';

// effectiveOwner returns a stable owner namespace for a session. For P2+
// daemons it's the ShortID (owner_agent_id). For pre-P2 daemons that don't
// carry owner_agent_id yet, fall back to `daemon:<daemon_id>` so two old
// daemons exporting the same session_id can't collide in the global map.
export function effectiveOwner(s: SessionRow): string {
  return s.owner_agent_id ?? `daemon:${s.daemon_id}`;
}

// ownerKey is the global node identity. NEVER session_id alone — two daemons
// can both report a "user-1" session id otherwise.
export function ownerKey(owner: string, sessionID: string): string {
  return `${owner}\0${sessionID}`;
}

// parentOwnerFor returns the namespace under which a child's parent should be
// resolved. For remote agent_task children, parent_agent_id is set explicitly
// (P2 ships this). For local subagents (P1 leaves ParentAgentID empty) the
// parent lives in the SAME owner namespace, so fall back to the child's own
// effectiveOwner — that's still `daemon:<id>` for pre-P2 daemons, keeping
// intra-daemon parent resolution intact.
export function parentOwnerFor(s: SessionRow): string {
  return s.parent_agent_id ?? effectiveOwner(s);
}
