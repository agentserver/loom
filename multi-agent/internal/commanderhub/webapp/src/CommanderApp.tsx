import { useEffect, useState } from 'react';
import { apiGet, postTurn, sessionPath } from './api/client';
import type { CommanderTree, SessionDetail, TurnState } from './api/types';
import { ChatWorkspace } from './components/ChatWorkspace';
import { DaemonSessionTree } from './components/DaemonSessionTree';
import { FileExplorerPanel } from './components/FileExplorerPanel';

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function statusTurnState(text: string): TurnState {
  if (text === 'starting codex') return 'starting';
  if (text === 'codex running') return 'answering';
  return 'queued';
}

function doneTurnState(data: unknown): TurnState {
  if (!isRecord(data) || !isRecord(data.result)) return 'done';
  return data.result.awaiting_user == null ? 'done' : 'awaiting_approval';
}

function errorMessage(data: unknown): string {
  if (!isRecord(data) || typeof data.message !== 'string') return 'turn failed';
  return data.message;
}

export function CommanderApp() {
  const [tree, setTree] = useState<CommanderTree | null>(null);
  const [error, setError] = useState<string>('');
  const [selected, setSelected] = useState<{ daemonID: string; sessionID: string } | null>(null);
  const [sessionDetail, setSessionDetail] = useState<SessionDetail | null>(null);
  const [turnState, setTurnState] = useState<TurnState>('idle');

  useEffect(() => {
    apiGet<CommanderTree>('/api/commander/tree')
      .then(setTree)
      .catch((err: Error) => setError(err.message));
  }, []);

  useEffect(() => {
    let cancelled = false;
    setSessionDetail(null);

    if (!selected) {
      setTurnState('idle');
      return;
    }

    const row = tree?.daemons
      .find((daemon) => daemon.daemon_id === selected.daemonID)
      ?.sessions?.find((session) => session.session_id === selected.sessionID);
    setTurnState(row?.turn_state || 'idle');

    apiGet<SessionDetail>(sessionPath(selected.daemonID, selected.sessionID))
      .then((detail) => {
        if (!cancelled) setSessionDetail(detail);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      });

    return () => {
      cancelled = true;
    };
  }, [selected, tree]);

  async function sendPrompt(prompt: string) {
    const text = prompt.trim();
    if (!selected || !text) return;

    setTurnState('queued');
    let turnError: Error | null = null;
    try {
      await postTurn(selected.daemonID, selected.sessionID, text, (event, data) => {
        if (event === 'status') {
          const statusText = isRecord(data) && typeof data.text === 'string' ? data.text : '';
          setTurnState(statusTurnState(statusText));
        } else if (event === 'chunk') {
          setTurnState('answering');
        } else if (event === 'done') {
          setTurnState(doneTurnState(data));
        } else if (event === 'error') {
          setTurnState('error');
          turnError = new Error(errorMessage(data));
        }
      });
      if (turnError) throw turnError;
      const detail = await apiGet<SessionDetail>(sessionPath(selected.daemonID, selected.sessionID));
      setSessionDetail(detail);
    } catch (err) {
      setTurnState('error');
      throw err;
    }
  }

  if (error === 'unauthorized') return <div className="login-shell">用 agentserver 登录</div>;
  if (error) return <div className="login-shell">加载失败: {error}</div>;
  if (!tree) return <div className="login-shell">加载中</div>;
  return (
    <div className="commander-shell">
      <DaemonSessionTree
        daemons={tree.daemons}
        selected={selected}
        onSelect={(daemonID, sessionID) => setSelected({ daemonID, sessionID })}
      />
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={sessionDetail}
        turnState={turnState}
        onSend={sendPrompt}
      />
      <FileExplorerPanel daemonID={selected?.daemonID || ''} sessionID={selected?.sessionID || ''} />
    </div>
  );
}
