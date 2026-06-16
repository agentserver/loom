import type { DaemonTree } from '../api/types';
import { StatusBadge } from './StatusBadge';

export function DaemonSessionTree({
  daemons,
  selected,
  onSelect,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
}) {
  return (
    <aside className="daemon-tree">
      {daemons.map((daemon) => (
        <section className="daemon-group" key={daemon.daemon_id}>
          <div className="daemon-row">
            <span className="online-dot" />
            <strong>{daemon.display_name || daemon.daemon_id}</strong>
            <span>{daemon.kind}</span>
          </div>
          <div className="session-list">
            {(daemon.sessions || []).map((session) => {
              const isSelected =
                selected?.daemonID === daemon.daemon_id && selected.sessionID === session.session_id;
              return (
                <button
                  key={session.session_id}
                  className={isSelected ? 'session-row selected' : 'session-row'}
                  onClick={() => onSelect(daemon.daemon_id, session.session_id)}
                  type="button"
                >
                  <span className="session-title">{session.title}</span>
                  <span className="session-meta">{session.working_dir || ''}</span>
                  <StatusBadge state={session.turn_state} />
                </button>
              );
            })}
          </div>
        </section>
      ))}
    </aside>
  );
}
