import { useState } from 'react';
import { Plus, X } from 'lucide-react';
import type { DaemonTree } from '../api/types';
import type { SessionRow } from '../api/types';
import { effectiveOwner, ownerKey, parentOwnerFor } from '../api/ownerKey';
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
  pendingSession,
  onCreateSession,
  onDiscardSession,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
  pendingSession?: {
    daemonID: string;
    sessionID: string;
    phase: 'draft' | 'submitting';
  } | null;
  onCreateSession?: (daemonID: string) => void;
  onDiscardSession?: (sessionID: string) => void;
}) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  function toggle(daemonID: string, sessionID: string) {
    const key = sessionTreeKey(daemonID, sessionID);
    setExpanded((current) => ({ ...current, [key]: !current[key] }));
  }

  const rootsByDaemon = buildCrossDaemonTree(daemons);
  const daemonByID = new Map(daemons.map(d => [d.daemon_id, d.display_name || d.daemon_id]));

  function isPendingRowVisible(daemonID: string): boolean {
    if (!pendingSession || pendingSession.daemonID !== daemonID) return false;
    const daemon = daemons.find((d) => d.daemon_id === daemonID);
    const sessions = daemon?.sessions ?? [];
    return !sessions.some((s) => s.session_id === pendingSession.sessionID);
  }

  function renderNode(node: SessionNode, depth: number) {
    const { session } = node;
    // expanded state is keyed by (home_daemon_id, session_id) so a remote
    // child uses the SAME key whether it's surfaced as a root in its home
    // daemon (impossible — it's always nested under its parent) or nested
    // here under a different daemon's parent. The toggle is consistent.
    const key = sessionTreeKey(session.daemon_id, session.session_id);
    const isExpanded = !!expanded[key];
    const hasChildren = node.children.length > 0;
    const isSelected = selected?.daemonID === session.daemon_id && selected.sessionID === session.session_id;
    const isRoot = depth === 0;
    const isSubagent = session.origin === 'subagent';

    let metaText: string;
    let metaClass = 'session-meta';
    if (node.parentOffline && session.origin === 'agent_task') {
      const displayName = session.parent_display_name ?? session.parent_id ?? '';
      metaText = `parent offline · ${displayName}`;
      metaClass += ' session-meta-muted';
    } else if (node.remote && session.origin === 'agent_task') {
      const homeName = daemonByID.get(session.daemon_id) ?? session.daemon_id;
      metaText = `remote task · on ${homeName}`;
    } else if (isSubagent) {
      metaText = subagentMeta(session);
    } else {
      metaText = sessionMeta(session, false);
    }

    // Roots and nested rows share the same structure so descendants of a
    // remote agent_task (e.g. the slave's own subagents under a remote
    // child) recurse correctly. Without this, P2's most common shape —
    // driver → slave agent_task → slave subagents — silently dropped
    // the subagents (filtered from the slave's root list AND never
    // rendered under their agent_task parent). #24 P3 review.
    const rowClass = isRoot
      ? (isSelected ? 'session-row selected' : 'session-row')
      : `${isSelected ? 'session-row selected' : 'session-row'} subagent-row`;
    const rowLineProps = isRoot ? { 'data-testid': 'root-session' } : {};

    return (
      <div className="session-node" key={ownerKey(effectiveOwner(session), session.session_id)}>
        <div className="session-row-line" {...rowLineProps}>
          {hasChildren ? (
            <button
              aria-label={`${isExpanded ? '收起' : '展开'} subagent sessions: ${session.title}`}
              className="session-toggle"
              onClick={() => toggle(session.daemon_id, session.session_id)}
              type="button"
            >
              {isExpanded ? '▾' : '▸'}
            </button>
          ) : (
            <span className="session-toggle-spacer" />
          )}
          <button
            className={rowClass}
            onClick={() => onSelect(session.daemon_id, session.session_id)}
            type="button"
          >
            <span className="session-title">{session.title}</span>
            <span className={metaClass}>{metaText}</span>
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
            {node.children.map((child) => renderNode(child, depth + 1))}
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
            {daemon.status === 'ok' && onCreateSession ? (() => {
              const otherDaemonPending = pendingSession != null && pendingSession.daemonID !== daemon.daemon_id;
              const disabledTitle = pendingSession?.phase === 'submitting'
                ? '等待新会话出现在列表中'
                : '先发送或丢弃当前草稿';
              return (
                <button
                  type="button"
                  className="daemon-new-session-btn"
                  aria-label={`新建 session: ${daemon.display_name || daemon.daemon_id}`}
                  disabled={otherDaemonPending}
                  title={otherDaemonPending ? disabledTitle : undefined}
                  onClick={() => onCreateSession(daemon.daemon_id)}
                >
                  <Plus size={16} />
                </button>
              );
            })() : (
              <span className="daemon-status">{daemon.status}</span>
            )}
          </div>
          {daemon.error ? <p className="daemon-error">{daemon.error}</p> : null}
          <div className="session-list">
            {isPendingRowVisible(daemon.daemon_id) && pendingSession ? (
              <div className="session-row-line session-row-line-pending" data-testid="pending-session-row">
                <span className="session-toggle-spacer" />
                <button
                  type="button"
                  className={`session-row${selected?.sessionID === pendingSession.sessionID ? ' selected' : ''}`}
                  onClick={() => onSelect(daemon.daemon_id, pendingSession.sessionID)}
                >
                  <span className="session-title">
                    {pendingSession.phase === 'submitting' ? '新建会话(同步中…)' : '新建会话(待提交)'}
                  </span>
                  <span className="session-meta">{daemon.display_name || daemon.daemon_id}</span>
                </button>
                {pendingSession.phase === 'draft' && onDiscardSession ? (
                  <button
                    type="button"
                    className="session-discard-btn"
                    aria-label="丢弃草稿"
                    onClick={(event) => {
                      event.stopPropagation();
                      onDiscardSession(pendingSession.sessionID);
                    }}
                  >
                    <X size={14} />
                  </button>
                ) : null}
              </div>
            ) : null}
            {(rootsByDaemon.get(daemon.daemon_id) ?? []).map((node) =>
              renderNode(node, 0)
            )}
          </div>
        </section>
      ))}
    </aside>
  );
}
