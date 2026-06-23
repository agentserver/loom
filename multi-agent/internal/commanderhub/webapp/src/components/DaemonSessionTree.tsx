import { useState } from 'react';
import type { DaemonTree } from '../api/types';
import type { SessionRow } from '../api/types';
import { StatusBadge } from './StatusBadge';

type SessionNode = {
  session: SessionRow;
  children: SessionNode[];
  remote: boolean;
  parentOffline: boolean; // has parent_id but parent not found in any daemon
};

function sessionTreeKey(daemonID: string, sessionID: string) {
  return `${daemonID}\0${sessionID}`;
}

// effectiveOwner returns a stable owner namespace for a session. For P2+
// daemons it's the ShortID (owner_agent_id). For pre-P2 daemons that don't
// carry owner_agent_id yet, fall back to `daemon:<daemon_id>` so two old
// daemons exporting the same session_id can't collide in the global map.
function effectiveOwner(s: SessionRow): string {
  return s.owner_agent_id ?? `daemon:${s.daemon_id}`;
}

// ownerKey is the global node identity. NEVER session_id alone — two daemons
// can both report a "user-1" session id otherwise.
function ownerKey(owner: string, sessionID: string): string {
  return `${owner}\0${sessionID}`;
}

// parentOwnerFor returns the namespace under which a child's parent should be
// resolved. For remote agent_task children, parent_agent_id is set explicitly
// (P2 ships this). For local subagents (P1 leaves ParentAgentID empty) the
// parent lives in the SAME owner namespace, so fall back to the child's own
// effectiveOwner — that's still `daemon:<id>` for pre-P2 daemons, keeping
// intra-daemon parent resolution intact.
function parentOwnerFor(s: SessionRow): string {
  return s.parent_agent_id ?? effectiveOwner(s);
}

function buildCrossDaemonTree(daemons: DaemonTree[]) {
  const all = daemons.flatMap(d => d.sessions ?? []);
  // Every map keyed by ownerKey (effectiveOwner, session_id) — never session_id alone.
  const byOwnerKey = new Map<string, SessionNode>();
  for (const s of all) {
    byOwnerKey.set(ownerKey(effectiveOwner(s), s.session_id),
      { session: s, children: [], remote: false, parentOffline: false });
  }
  const isChildKey = new Set<string>(); // ownerKey of resolved children
  for (const s of all) {
    if (s.origin !== 'subagent' && s.origin !== 'agent_task') continue;
    if (!s.parent_id) continue;
    const parentKey = ownerKey(parentOwnerFor(s), s.parent_id);
    const parent = byOwnerKey.get(parentKey);
    const childKey = ownerKey(effectiveOwner(s), s.session_id);
    const childNode = byOwnerKey.get(childKey)!;
    if (!parent) {
      // parent offline → child stays a root, flagged for the offline note.
      childNode.parentOffline = true;
      continue;
    }
    parent.children.push(childNode);
    isChildKey.add(childKey);
  }
  // Roots per daemon = that daemon's sessions whose ownerKey is NOT a resolved child.
  const rootsByDaemon = new Map<string, SessionNode[]>();
  for (const d of daemons) {
    rootsByDaemon.set(d.daemon_id, (d.sessions ?? [])
      .filter(s => !isChildKey.has(ownerKey(effectiveOwner(s), s.session_id)))
      .map(s => byOwnerKey.get(ownerKey(effectiveOwner(s), s.session_id))!));
  }
  // Mark remote: child's home daemon != parent's home daemon.
  const daemonOfOwnerKey = new Map<string, string>();
  for (const s of all) daemonOfOwnerKey.set(ownerKey(effectiveOwner(s), s.session_id), s.daemon_id);
  for (const parent of byOwnerKey.values()) {
    const parentDaemon = daemonOfOwnerKey.get(ownerKey(effectiveOwner(parent.session), parent.session.session_id));
    for (const child of parent.children) {
      child.remote = daemonOfOwnerKey.get(ownerKey(effectiveOwner(child.session), child.session.session_id)) !== parentDaemon;
    }
  }
  return rootsByDaemon;
}

function subagentMeta(session: SessionRow) {
  const label = session.agent_name || session.agent_role || session.parent_id || 'subagent';
  return `subagent · ${label}`;
}

function sessionMeta(session: SessionRow, isSubagent: boolean) {
  if (isSubagent) return subagentMeta(session);
  if (session.origin === 'agent_task') {
    return `agent task${session.working_dir ? ` · ${session.working_dir}` : ''}`;
  }
  return session.working_dir || '';
}

export function DaemonSessionTree({
  daemons,
  selected,
  onSelect,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
}) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  function toggle(daemonID: string, sessionID: string) {
    const key = sessionTreeKey(daemonID, sessionID);
    setExpanded((current) => ({ ...current, [key]: !current[key] }));
  }

  const rootsByDaemon = buildCrossDaemonTree(daemons);
  const daemonByID = new Map(daemons.map(d => [d.daemon_id, d.display_name || d.daemon_id]));

  function renderChildNode(node: SessionNode) {
    const { session } = node;
    const isSelected = selected?.daemonID === session.daemon_id && selected.sessionID === session.session_id;
    const isSubagent = session.origin === 'subagent';

    let metaText: string;
    if (node.remote && session.origin === 'agent_task') {
      const homeName = daemonByID.get(session.daemon_id) ?? session.daemon_id;
      metaText = `remote task · on ${homeName}`;
    } else if (isSubagent) {
      metaText = subagentMeta(session);
    } else {
      metaText = sessionMeta(session, false);
    }

    return (
      <button
        key={ownerKey(effectiveOwner(session), session.session_id)}
        className={`${isSelected ? 'session-row selected' : 'session-row'} subagent-row`}
        onClick={() => onSelect(session.daemon_id, session.session_id)}
        type="button"
      >
        <span className="session-title">{session.title}</span>
        <span className="session-meta">{metaText}</span>
        <span className="session-badges">
          {session.active_worker ? (
            <span className="active-worker-badge" title="Daemon has a hot worker cached for this session">
              active
            </span>
          ) : null}
          <StatusBadge state={session.turn_state} />
        </span>
      </button>
    );
  }

  function renderRootNode(node: SessionNode, daemonID: string) {
    const { session } = node;
    const key = sessionTreeKey(daemonID, session.session_id);
    const isExpanded = !!expanded[key];
    const hasChildren = node.children.length > 0;
    const isSelected = selected?.daemonID === session.daemon_id && selected.sessionID === session.session_id;

    let metaText: string;
    if (node.parentOffline && session.origin === 'agent_task') {
      const displayName = session.parent_display_name ?? session.parent_id ?? '';
      metaText = `parent offline · ${displayName}`;
    } else {
      const isSubagent = session.origin === 'subagent';
      metaText = sessionMeta(session, isSubagent);
    }

    return (
      <div className="session-node" key={ownerKey(effectiveOwner(session), session.session_id)}>
        <div className="session-row-line" data-testid="root-session">
          {hasChildren ? (
            <button
              aria-label={`${isExpanded ? '收起' : '展开'} subagent sessions: ${session.title}`}
              className="session-toggle"
              onClick={() => toggle(daemonID, session.session_id)}
              type="button"
            >
              {isExpanded ? '▾' : '▸'}
            </button>
          ) : (
            <span className="session-toggle-spacer" />
          )}
          <button
            className={`${isSelected ? 'session-row selected' : 'session-row'}`}
            onClick={() => onSelect(session.daemon_id, session.session_id)}
            type="button"
          >
            <span className="session-title">{session.title}</span>
            <span className={`session-meta${node.parentOffline ? ' session-meta-muted' : ''}`}>{metaText}</span>
            <span className="session-badges">
              {session.active_worker ? (
                <span className="active-worker-badge" title="Daemon has a hot worker cached for this session">
                  active
                </span>
              ) : null}
              <StatusBadge state={session.turn_state} />
            </span>
          </button>
        </div>
        {hasChildren && isExpanded ? (
          <div className="session-children">
            {node.children.map((child) => renderChildNode(child))}
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <aside className="daemon-tree" data-testid="daemon-tree">
      {daemons.map((daemon) => (
        <section className="daemon-group" key={daemon.daemon_id}>
          <div className={`daemon-row daemon-${daemon.status}`}>
            <span className={`online-dot online-dot-${daemon.status}`} />
            <strong>{daemon.display_name || daemon.daemon_id}</strong>
            <span>{daemon.kind}</span>
            <span className="daemon-status">{daemon.status}</span>
          </div>
          {daemon.error ? <p className="daemon-error">{daemon.error}</p> : null}
          <div className="session-list">
            {(rootsByDaemon.get(daemon.daemon_id) ?? []).map((node) =>
              renderRootNode(node, daemon.daemon_id)
            )}
          </div>
        </section>
      ))}
    </aside>
  );
}
