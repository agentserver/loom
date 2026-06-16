import { useEffect, useRef, useState } from 'react';
import { apiGet, isTurnInFlightError, postTurn, sessionPath } from './api/client';
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

function turnKey(selection: { daemonID: string; sessionID: string }) {
  return `${selection.daemonID}\0${selection.sessionID}`;
}

export function CommanderApp() {
  const [tree, setTree] = useState<CommanderTree | null>(null);
  const [error, setError] = useState<string>('');
  const [selected, setSelected] = useState<{ daemonID: string; sessionID: string } | null>(null);
  const [sessionDetail, setSessionDetail] = useState<SessionDetail | null>(null);
  const [turnState, setTurnState] = useState<TurnState>('idle');
  const selectedRef = useRef<typeof selected>(null);
  const turnRequestsRef = useRef(new Map<string, number>());

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
    const submitted = selectedRef.current;
    if (!submitted || !text) return;

    const key = turnKey(submitted);
    const previousRequestID = turnRequestsRef.current.get(key) || 0;
    const requestID = previousRequestID + 1;
    turnRequestsRef.current.set(key, requestID);
    const isCurrentTurn = () =>
      turnRequestsRef.current.get(key) === requestID &&
      selectedRef.current?.daemonID === submitted.daemonID &&
      selectedRef.current?.sessionID === submitted.sessionID;
    const setCurrentTurnState = (state: TurnState) => {
      if (isCurrentTurn()) setTurnState(state);
    };

    setCurrentTurnState('queued');
    let turnError: Error | null = null;
    try {
      await postTurn(submitted.daemonID, submitted.sessionID, text, (event, data) => {
        if (!isCurrentTurn()) return;
        if (event === 'status') {
          const statusText = isRecord(data) && typeof data.text === 'string' ? data.text : '';
          setCurrentTurnState(statusTurnState(statusText));
        } else if (event === 'chunk') {
          setCurrentTurnState('answering');
        } else if (event === 'done') {
          setCurrentTurnState(doneTurnState(data));
        } else if (event === 'error') {
          setCurrentTurnState('error');
          turnError = new Error(errorMessage(data));
        }
      });
      if (turnError) throw turnError;
      if (!isCurrentTurn()) return;
      const detail = await apiGet<SessionDetail>(sessionPath(submitted.daemonID, submitted.sessionID));
      if (isCurrentTurn()) setSessionDetail(detail);
    } catch (err) {
      if (isTurnInFlightError(err)) {
        if (turnRequestsRef.current.get(key) === requestID) {
          if (previousRequestID === 0) {
            turnRequestsRef.current.delete(key);
          } else {
            turnRequestsRef.current.set(key, previousRequestID);
          }
        }
        if (
          selectedRef.current?.daemonID === submitted.daemonID &&
          selectedRef.current?.sessionID === submitted.sessionID
        ) {
          setTurnState('queued');
        }
        throw err;
      }
      setCurrentTurnState('error');
      throw err;
    }
  }

  function selectSession(daemonID: string, sessionID: string) {
    const next = { daemonID, sessionID };
    selectedRef.current = next;
    setSelected(next);
  }

  if (error === 'unauthorized') return <div className="login-shell">用 agentserver 登录</div>;
  if (error) return <div className="login-shell">加载失败: {error}</div>;
  if (!tree) return <div className="login-shell">加载中</div>;
  return (
    <div className="commander-shell">
      <DaemonSessionTree
        daemons={tree.daemons}
        selected={selected}
        onSelect={selectSession}
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
