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

  function handlePreviewRequest() {
    overlay.open('preview');
  }

  function handlePreview(payload: FilePreviewPayload) {
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
        <DaemonSessionTree daemons={daemons} selected={selected} onSelect={handleSelectSession} />
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
          daemonID={selected?.daemonID || ''}
          sessionID={selected?.sessionID || ''}
          renderMode="sheet"
          onPreviewRequest={handlePreviewRequest}
          onPreview={handlePreview}
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
