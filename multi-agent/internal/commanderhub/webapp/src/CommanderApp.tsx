import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { apiGet, isTurnInFlightError, postTurn, sessionPath } from './api/client';
import { effectiveOwner, ownerKey, parentOwnerFor } from './api/ownerKey';
import type { CommanderTree, SessionDetail, SessionRow, TurnState } from './api/types';
import { ChatWorkspace } from './components/ChatWorkspace';
import { DaemonSessionTree } from './components/DaemonSessionTree';
import { FileExplorerPanel } from './components/FileExplorerPanel';
import { MobileShell } from './components/MobileShell';
import type { FilePreviewPayload } from './components/FilePreviewSheet';
import { useMediaQuery } from './hooks/useMediaQuery';
import { useOverlayHistory } from './hooks/useOverlayHistory';

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function isQueuedStatusText(text: string) {
  return text === 'queued on daemon' || text === 'queued-on-daemon' || text === 'accepted by daemon';
}

function legacyStatusTextTurnState(text: string): TurnState | null {
  if (isQueuedStatusText(text) || text === 'starting codex') return 'queued';
  if (text === 'codex running') return 'answering';
  return null;
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


// pickAutoSession mirrors DaemonSessionTree.buildCrossDaemonTree to find the
// first "root" session (not a resolved child) from all daemons.
// Returns { daemonID, sessionID } of the first root, or null if none.
function pickAutoSession(
  tree: CommanderTree,
): { daemonID: string; sessionID: string } | null {
  const daemons = tree.daemons;
  const all = daemons.flatMap((d) => d.sessions ?? []);
  if (all.length === 0) return null;

  // Build owner-keyed set so we can find resolved children (same logic as
  // DaemonSessionTree.buildCrossDaemonTree — never use a flat Set<session_id>
  // because cross-daemon session_id collisions would falsely link sessions).
  const isChildKey = new Set<string>();
  for (const s of all) {
    if (s.origin !== 'subagent' && s.origin !== 'agent_task') continue;
    if (!s.parent_id) continue;
    const parentKey = ownerKey(parentOwnerFor(s), s.parent_id);
    // Only mark as child if the parent exists in our set
    const parentExists = all.some(
      (p) => ownerKey(effectiveOwner(p), p.session_id) === parentKey,
    );
    if (parentExists) {
      isChildKey.add(ownerKey(effectiveOwner(s), s.session_id));
    }
  }

  // Return first root session from any daemon
  for (const d of daemons) {
    for (const s of d.sessions ?? []) {
      if (!isChildKey.has(ownerKey(effectiveOwner(s), s.session_id))) {
        return { daemonID: s.daemon_id, sessionID: s.session_id };
      }
    }
  }
  return null;
}

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

  // Mobile overlay state — hoisted here so CommanderApp owns it
  const [sessionsOpen, setSessionsOpen] = useState(false);
  const [filesOpen, setFilesOpen] = useState(false);
  const [previewPayload, setPreviewPayload] = useState<FilePreviewPayload | null>(null);

  type PendingSession = { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' };
  const [pendingSession, setPendingSession] = useState<PendingSession | null>(null);
  const pendingSessionRef = useRef<PendingSession | null>(null);
  useLayoutEffect(() => {
    pendingSessionRef.current = pendingSession;
  });

  // Overlay history controller — one instance per CommanderApp lifetime
  const overlay = useOverlayHistory();

  // One-shot auto-select guard: set to true after the first auto-select,
  // reset only on full logout (authRequired false → true).
  const hasAutoSelectedRef = useRef(false);
  const prevAuthRequiredRef = useRef(authRequired);

  // Reset the auto-select ref on full logout (authRequired false → true).
  useEffect(() => {
    if (!prevAuthRequiredRef.current && authRequired) {
      hasAutoSelectedRef.current = false;
    }
    prevAuthRequiredRef.current = authRequired;
  }, [authRequired]);

  // matchMedia onChange fires BEFORE setMatches — use it to drain overlay
  // history synchronously when transitioning desktop (matches becomes false).
  const isNonDesktop = useMediaQuery('(max-width: 1023px)', {
    onChange(matches) {
      if (!matches) {
        // Transitioning to desktop: drain pushed history entries, then flush
        // overlay UI flags — order matters per spec.
        overlay.drainForBreakpoint();
        setSessionsOpen(false);
        setFilesOpen(false);
        setPreviewPayload(null);
      }
    },
  });

  // Full unmount cleanup — detaches the popstate listener only.
  useEffect(() => () => overlay.reset(), [overlay]);

  // tryAutoSelect: one-shot auto-select for mobile. Reads latest state via
  // refs so it can be called from both the loadTree .then callback and the
  // useEffect below without stale-closure issues.
  function tryAutoSelect(nextTree: CommanderTree) {
    if (hasAutoSelectedRef.current) return;
    if (!isNonDesktop) return;
    if (selectedRef.current != null) return;
    const pick = pickAutoSession(nextTree);
    if (pick) {
      hasAutoSelectedRef.current = true;
      selectedRef.current = pick;
      setSelected(pick);
    }
  }

  // Keep a ref to tryAutoSelect so loadTree's useCallback([]) can call the
  // latest version without capturing stale closures.
  const tryAutoSelectRef = useRef(tryAutoSelect);
  useLayoutEffect(() => {
    tryAutoSelectRef.current = tryAutoSelect;
  });

  // Auto-select effect: fires when isNonDesktop or tree changes.
  // Path (b): covers desktop→mobile rotation while tree is already loaded.
  useEffect(() => {
    if (!tree) return;
    tryAutoSelectRef.current(tree);
  }, [isNonDesktop, tree]);

  const loadTree = useCallback(() => {
    setError('');
    return apiGet<CommanderTree>('/api/commander/tree')
      .then((nextTree) => {
        setTree(nextTree);
        setAuthRequired(false);
        // Path (a): one-shot auto-select right after the tree arrives,
        // before React flushes the state update. Path (b) useEffect above
        // also covers this case for rotation while tree is loaded.
        tryAutoSelectRef.current(nextTree);
        // If a pending session's real row has arrived, clear pending so
        // the virtual row is replaced by the real one.
        const p = pendingSessionRef.current;
        if (p != null) {
          const realRow = nextTree.daemons
            .find((d) => d.daemon_id === p.daemonID)
            ?.sessions?.find((s) => s.session_id === p.sessionID);
          if (realRow) {
            pendingSessionRef.current = null;
            setPendingSession(null);
          }
        }
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

    // Draft pending — backend has no row yet; render an empty placeholder.
    if (
      pendingSession != null
      && pendingSession.sessionID === selected.sessionID
      && pendingSession.phase === 'draft'
    ) {
      setSessionDetail({
        session: { ID: selected.sessionID, Title: '新建会话' },
        messages: [],
      });
      return;
    }

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
  }, [selected, tree, pendingSession]);

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
          } else {
            const legacyState = legacyStatusTextTurnState(statusText);
            if (legacyState) setCurrentTurnState(legacyState);
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
      // pending phase flip + loadTree MUST run independent of isCurrentTurn():
      // the server-side session was created regardless of whether the user has
      // since navigated to a different session. If we gated this on
      // isCurrentTurn(), a quick navigation away would leave the virtual row
      // visible forever and lock other daemons' + buttons.
      const pendingNow = pendingSessionRef.current;
      if (
        pendingNow != null
        && pendingNow.sessionID === submitted.sessionID
        && pendingNow.phase === 'draft'
      ) {
        const flipped: PendingSession = { ...pendingNow, phase: 'submitting' };
        pendingSessionRef.current = flipped;
        setPendingSession(flipped);
        void loadTree();
        // Detail fetch is handled by the [selected, tree, pendingSession]
        // effect when it re-runs on the phase change. We don't issue one here.
        return;
      }
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

  function createPendingSession(daemonID: string) {
    const current = pendingSessionRef.current;
    if (current != null && current.daemonID !== daemonID) return;
    if (current != null && current.daemonID === daemonID) {
      // Re-select existing; no fresh UUID.
      selectSession(current.daemonID, current.sessionID);
      return;
    }
    const sid = crypto.randomUUID();
    const next: PendingSession = { daemonID, sessionID: sid, phase: 'draft' };
    pendingSessionRef.current = next;
    setPendingSession(next);
    selectSession(daemonID, sid);
  }

  function discardPendingSession() {
    const prev = pendingSessionRef.current;
    pendingSessionRef.current = null;
    setPendingSession(null);
    if (prev != null && selectedRef.current?.sessionID === prev.sessionID) {
      selectedRef.current = null;
      setSelected(null);
    }
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

  const selectedIsPendingDraft = pendingSession != null
    && selected?.sessionID === pendingSession.sessionID
    && pendingSession.phase === 'draft';
  const pendingDaemonOffline = pendingSession?.phase === 'draft'
    && (tree?.daemons.find((d) => d.daemon_id === pendingSession.daemonID)?.status ?? 'offline') !== 'ok';
  const composerLocked = selectedIsPendingDraft && pendingDaemonOffline;
  const composerNote = composerLocked
    ? 'daemon 离线 — 无法提交,等待 daemon 上线或选择其它会话'
    : undefined;
  // Suppress FileExplorerPanel fetches when selected is a draft pending
  // session — the backend has no row for it yet, so /files?path=. would
  // 404 and surface a misleading error. Passing an empty sessionID
  // short-circuits the panel's effect (see FileExplorerPanel.tsx — the
  // useEffect bails when !daemonID || !sessionID).
  const fileSessionID = selectedIsPendingDraft ? '' : (selected?.sessionID || '');
  const fileDaemonID = selectedIsPendingDraft ? '' : (selected?.daemonID || '');

  if (isNonDesktop) {
    return (
      <MobileShell
        daemons={tree.daemons}
        selected={selected}
        onSelect={selectSession}
        sessionDetail={sessionDetail}
        turnState={turnState}
        onSend={sendPrompt}
        overlay={overlay}
        sessionsOpen={sessionsOpen}
        setSessionsOpen={setSessionsOpen}
        filesOpen={filesOpen}
        setFilesOpen={setFilesOpen}
        previewPayload={previewPayload}
        setPreviewPayload={setPreviewPayload}
        pendingSession={pendingSession}
        onCreateSession={createPendingSession}
        onDiscardSession={discardPendingSession}
        composerLocked={composerLocked}
        composerNote={composerNote}
        disableFiles={selectedIsPendingDraft}
      />
    );
  }

  return (
    <div className="commander-shell" data-testid="commander-shell">
      <DaemonSessionTree
        daemons={tree.daemons}
        selected={selected}
        onSelect={selectSession}
        pendingSession={pendingSession}
        onCreateSession={createPendingSession}
        onDiscardSession={discardPendingSession}
      />
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={sessionDetail}
        turnState={turnState}
        onSend={sendPrompt}
        composerLocked={composerLocked}
        composerNote={composerNote}
      />
      <FileExplorerPanel daemonID={fileDaemonID} sessionID={fileSessionID} />
    </div>
  );
}
