import { useCallback, useEffect, useRef, useState } from 'react';
import { apiGet, isTurnInFlightError, postTurn, sessionPath } from './api/client';
import type { CommanderTree, SessionDetail, TurnState } from './api/types';
import { ChatWorkspace } from './components/ChatWorkspace';
import { DaemonSessionTree } from './components/DaemonSessionTree';
import { FileExplorerPanel } from './components/FileExplorerPanel';

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function isQueuedStatusText(text: string) {
  return text === 'queued on daemon' || text === 'queued-on-daemon' || text === 'accepted by daemon';
}

function statusCodeTurnState(code: string): TurnState | null {
  switch (code) {
    case 'queued':
    case 'starting':
      return 'queued';
    case 'answering':
      return 'answering';
    case 'awaiting_approval':
      return 'awaiting_approval';
    case 'done':
      return 'done';
    case 'error':
      return 'error';
    default:
      return null;
  }
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

type LoginState = {
  phase: 'idle' | 'starting' | 'pending' | 'error';
  loginID?: string;
  verifyURL?: string;
  error?: string;
};

type LoginResponse = {
  login_id: string;
  verification_uri_complete: string;
};

type LoginPollResponse = {
  status: 'pending' | 'ok' | 'error';
  error?: string;
};

export function CommanderApp() {
  const [tree, setTree] = useState<CommanderTree | null>(null);
  const [error, setError] = useState<string>('');
  const [authRequired, setAuthRequired] = useState(false);
  const [login, setLogin] = useState<LoginState>({ phase: 'idle' });
  const [selected, setSelected] = useState<{ daemonID: string; sessionID: string } | null>(null);
  const [sessionDetail, setSessionDetail] = useState<SessionDetail | null>(null);
  const [turnState, setTurnState] = useState<TurnState>('idle');
  const selectedRef = useRef<typeof selected>(null);
  const turnRequestsRef = useRef(new Map<string, number>());

  const loadTree = useCallback(() => {
    setError('');
    return apiGet<CommanderTree>('/api/commander/tree')
      .then((nextTree) => {
        setTree(nextTree);
        setAuthRequired(false);
      })
      .catch((err: Error) => {
        if (err.message === 'unauthorized') {
          setAuthRequired(true);
          setTree(null);
          return;
        }
        setError(err.message);
      });
  }, []);

  useEffect(() => {
    void loadTree();
  }, [loadTree]);

  useEffect(() => {
    if (login.phase !== 'pending' || !login.loginID) return;

    let cancelled = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const res = await fetch(`/api/commander/login/poll?id=${encodeURIComponent(login.loginID || '')}`, {
          credentials: 'include',
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const body = (await res.json()) as LoginPollResponse;
        if (body.status === 'pending') {
          if (!cancelled) timer = window.setTimeout(poll, 1500);
          return;
        }
        if (body.status === 'ok') {
          if (!cancelled) {
            setLogin({ phase: 'idle' });
            void loadTree();
          }
          return;
        }
        throw new Error(body.error || 'login failed');
      } catch (err) {
        if (!cancelled) {
          setLogin({
            phase: 'error',
            verifyURL: login.verifyURL,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }
    };

    void poll();
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [loadTree, login.loginID, login.phase, login.verifyURL]);

  async function startLogin() {
    setLogin({ phase: 'starting' });
    try {
      const res = await fetch('/api/commander/login', { method: 'POST', credentials: 'include' });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const body = (await res.json()) as LoginResponse;
      setLogin({
        phase: 'pending',
        loginID: body.login_id,
        verifyURL: body.verification_uri_complete,
      });
    } catch (err) {
      setLogin({ phase: 'error', error: err instanceof Error ? err.message : String(err) });
    }
  }

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
          const statusCode = isRecord(data) && typeof data.status_code === 'string' ? data.status_code : '';
          const statusState = statusCodeTurnState(statusCode);
          if (statusState) {
            setCurrentTurnState(statusState);
            if (statusState === 'error') turnError = new Error(statusText || 'turn failed');
          } else if (isQueuedStatusText(statusText)) {
            setCurrentTurnState('queued');
          }
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

  if (authRequired) {
    return (
      <div className="login-shell">
        <section className="login-panel">
          <h1>Commander</h1>
          <button type="button" onClick={() => void startLogin()} disabled={login.phase === 'starting'}>
            用 agentserver 登录
          </button>
          {login.verifyURL ? (
            <a href={login.verifyURL} target="_blank" rel="noreferrer">
              打开授权页面
            </a>
          ) : null}
          {login.phase === 'pending' ? <p>授权完成后会自动进入 Commander。</p> : null}
          {login.phase === 'error' ? <p className="login-error">登录失败: {login.error}</p> : null}
        </section>
      </div>
    );
  }
  if (error) return <div className="login-shell">加载失败: {error}</div>;
  if (!tree) return <div className="login-shell">加载中</div>;
  return (
    <div className="commander-shell" data-testid="commander-shell">
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
