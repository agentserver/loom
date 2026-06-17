import { useState } from 'react';
import type { DaemonTree } from '../api/types';
import type { SessionRow } from '../api/types';
import { StatusBadge } from './StatusBadge';

type SessionNode = {
  session: SessionRow;
  children: SessionRow[];
};

function sessionTreeKey(daemonID: string, sessionID: string) {
  return `${daemonID}\0${sessionID}`;
}

function buildSessionNodes(sessions: SessionRow[]): SessionNode[] {
  const byID = new Map<string, SessionNode>();
  for (const session of sessions) {
    byID.set(session.session_id, { session, children: [] });
  }

  const roots: SessionNode[] = [];
  for (const session of sessions) {
    const node = byID.get(session.session_id)!;
    if (session.origin === 'subagent' && session.parent_id) {
      const parent = byID.get(session.parent_id);
      if (parent) {
        parent.children.push(session);
        continue;
      }
    }
    roots.push(node);
  }
  return roots;
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

  function renderSessionButton(daemonID: string, session: SessionRow, isSubagent = false) {
    const isSelected = selected?.daemonID === daemonID && selected.sessionID === session.session_id;
    return (
      <button
        key={session.session_id}
        className={`${isSelected ? 'session-row selected' : 'session-row'}${isSubagent ? ' subagent-row' : ''}`}
        onClick={() => onSelect(daemonID, session.session_id)}
        type="button"
      >
        <span className="session-title">{session.title}</span>
        <span className="session-meta">{sessionMeta(session, isSubagent)}</span>
        <StatusBadge state={session.turn_state} />
      </button>
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
            {buildSessionNodes(daemon.sessions || []).map(({ session, children }) => {
              const key = sessionTreeKey(daemon.daemon_id, session.session_id);
              const isExpanded = !!expanded[key];
              const hasChildren = children.length > 0;
              return (
                <div className="session-node" key={session.session_id}>
                  <div className="session-row-line">
                    {hasChildren ? (
                      <button
                        aria-label={`${isExpanded ? '收起' : '展开'} subagent sessions: ${session.title}`}
                        className="session-toggle"
                        onClick={() => toggle(daemon.daemon_id, session.session_id)}
                        type="button"
                      >
                        {isExpanded ? '▾' : '▸'}
                      </button>
                    ) : (
                      <span className="session-toggle-spacer" />
                    )}
                    {renderSessionButton(daemon.daemon_id, session)}
                  </div>
                  {hasChildren && isExpanded ? (
                    <div className="session-children">
                      {children.map((child) => renderSessionButton(daemon.daemon_id, child, true))}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
        </section>
      ))}
    </aside>
  );
}
