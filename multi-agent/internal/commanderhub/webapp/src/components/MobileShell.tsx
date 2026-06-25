import { useEffect } from 'react';
import { Menu, FolderOpen } from 'lucide-react';
import type { DaemonTree, SessionDetail, TurnState } from '../api/types';
import type { OverlayController } from '../hooks/useOverlayHistory';
import { ChatWorkspace } from './ChatWorkspace';
import { DaemonSessionTree } from './DaemonSessionTree';
import { FileExplorerPanel } from './FileExplorerPanel';
import { MobileDrawer } from './MobileDrawer';
import { FilePreviewSheet, type FilePreviewPayload } from './FilePreviewSheet';

export function MobileShell({
  daemons,
  selected,
  onSelect,
  sessionDetail,
  turnState,
  onSend,
  overlay,
  sessionsOpen,
  setSessionsOpen,
  filesOpen,
  setFilesOpen,
  previewPayload,
  setPreviewPayload,
  pendingSession,
  onCreateSession,
  onDiscardSession,
  composerLocked,
  composerNote,
  disableFiles,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
  sessionDetail: SessionDetail | null;
  turnState: TurnState;
  onSend: (prompt: string) => Promise<void>;
  overlay: OverlayController;
  sessionsOpen: boolean;
  setSessionsOpen: (next: boolean) => void;
  filesOpen: boolean;
  setFilesOpen: (next: boolean) => void;
  previewPayload: FilePreviewPayload | null;
  setPreviewPayload: (next: FilePreviewPayload | null) => void;
  pendingSession?: { daemonID: string; sessionID: string; phase: 'draft' | 'submitting' } | null;
  onCreateSession?: (daemonID: string) => void;
  onDiscardSession?: (sessionID: string) => void;
  composerLocked?: boolean;
  composerNote?: string;
  disableFiles?: boolean;
}) {
  useEffect(() => {
    const unsubscribe = overlay.onPop((id) => {
      if (id === 'sessions') setSessionsOpen(false);
      else if (id === 'files') setFilesOpen(false);
      else if (id === 'preview') setPreviewPayload(null);
    });
    return unsubscribe;
  }, [overlay, setSessionsOpen, setFilesOpen, setPreviewPayload]);

  function openSessions() {
    overlay.open('sessions');
    setSessionsOpen(true);
  }
  function openFiles() {
    overlay.open('files');
    setFilesOpen(true);
  }

  // Generic "close this overlay" — if the controller's stack has this id on
  // top, go through history.back() so the back stack stays consistent. If the
  // stack is empty or out of sync (defensive: SSR remount, double-fire), the
  // shell closes the React state directly so the user is never stuck with a
  // visible overlay that has no way to dismiss it (spec §Closing via UI).
  function closeOverlay(
    id: 'sessions' | 'files' | 'preview',
    setOpen: (next: boolean) => void,
  ) {
    const stack = overlay.stackSnapshot();
    if (stack.length > 0 && stack[stack.length - 1] === id) {
      overlay.closeTop(id);
    } else {
      setOpen(false);
    }
  }

  function closePreview() {
    const stack = overlay.stackSnapshot();
    if (stack.length > 0 && stack[stack.length - 1] === 'preview') {
      overlay.closeTop('preview');
    } else {
      setPreviewPayload(null);
    }
  }

  function handleSelectSession(daemonID: string, sessionID: string) {
    onSelect(daemonID, sessionID);
    closeOverlay('sessions', setSessionsOpen);
  }

  function handleCreate(daemonID: string) {
    if (!onCreateSession) return;
    onCreateSession(daemonID);
    closeOverlay('sessions', setSessionsOpen);
  }

  function handlePreviewRequest() {
    // Dedupe: if a previous tap's preview entry is still on top (its
    // fetch hasn't resolved, or the user is replacing one open preview
    // with another), reuse it instead of pushing another invisible
    // entry. Without this, rapid taps stack multiple preview entries —
    // the older ones never show a sheet (FileExplorerPanel's request
    // bump drops their late responses) but still consume Back presses.
    const stack = overlay.stackSnapshot();
    if (stack.length > 0 && stack[stack.length - 1] === 'preview') return;
    overlay.open('preview');
  }

  function handlePreview(payload: FilePreviewPayload) {
    // Drop late fetch responses whose 'preview' entry was already
    // popped (user pressed Back while the content request was
    // in-flight). Opening the sheet now would leave it visible with
    // no matching back-stack entry — the next Back would close the
    // Files drawer behind it while the preview stayed up.
    const stack = overlay.stackSnapshot();
    if (stack.length === 0 || stack[stack.length - 1] !== 'preview') return;
    setPreviewPayload(payload);
  }

  const sessionsBtn = (
    <button
      type="button"
      className="chat-header-trigger"
      aria-label="Sessions"
      onClick={openSessions}
    >
      <Menu size={18} /> Sessions
    </button>
  );
  const filesBtn = (
    <button
      type="button"
      className="chat-header-trigger"
      aria-label="Files"
      onClick={openFiles}
    >
      <FolderOpen size={18} /> Files
    </button>
  );

  return (
    <div className="commander-shell commander-shell-mobile" data-testid="commander-shell">
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={sessionDetail}
        turnState={turnState}
        onSend={onSend}
        mobileLeading={sessionsBtn}
        mobileTrailing={filesBtn}
        empty={selected == null}
        composerLocked={composerLocked}
        composerNote={composerNote}
      />
      <MobileDrawer
        open={sessionsOpen}
        onOpenChange={(next) => {
          if (!next) closeOverlay('sessions', setSessionsOpen);
          else openSessions();
        }}
        side="left"
        title="Sessions"
      >
        <DaemonSessionTree
          daemons={daemons}
          selected={selected}
          onSelect={handleSelectSession}
          pendingSession={pendingSession}
          onCreateSession={onCreateSession ? handleCreate : undefined}
          onDiscardSession={onDiscardSession}
        />
      </MobileDrawer>
      <MobileDrawer
        open={filesOpen}
        onOpenChange={(next) => {
          if (!next) closeOverlay('files', setFilesOpen);
          else openFiles();
        }}
        side="right"
        title="Files"
      >
        <FileExplorerPanel
          daemonID={disableFiles ? '' : (selected?.daemonID || '')}
          sessionID={disableFiles ? '' : (selected?.sessionID || '')}
          renderMode="sheet"
          onPreviewRequest={handlePreviewRequest}
          onPreview={handlePreview}
          onPreviewDismiss={closePreview}
        />
      </MobileDrawer>
      <FilePreviewSheet
        open={previewPayload != null}
        onOpenChange={(next) => {
          if (!next) closePreview();
        }}
        payload={previewPayload}
      />
    </div>
  );
}
