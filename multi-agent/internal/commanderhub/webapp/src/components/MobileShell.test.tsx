import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { createOverlayController } from '../hooks/useOverlayHistory';
import { MobileShell } from './MobileShell';
import type { DaemonTree } from '../api/types';

afterEach(cleanup);
beforeEach(() => window.history.replaceState(null, '', window.location.pathname));

const daemons: DaemonTree[] = [
  {
    daemon_id: 'd1',
    display_name: 'prod',
    kind: 'codex',
    status: 'ok',
    sessions: [
      {
        daemon_id: 'd1',
        session_id: 's1',
        kind: 'codex',
        title: 'Session one',
        origin: 'user',
        turn_state: 'idle',
        active_worker: false,
        awaiting_approval: false,
      },
    ],
  },
];

type RenderShellState = {
  sessionsOpen: boolean;
  filesOpen: boolean;
  previewPayload: null;
};

function renderShell(overrides: Partial<Parameters<typeof MobileShell>[0]> = {}) {
  const overlay = createOverlayController();
  const onSelect = vi.fn();
  const setSessionsOpen = vi.fn();
  const setFilesOpen = vi.fn();
  const setPreviewPayload = vi.fn();
  const props = {
    daemons,
    selected: { daemonID: 'd1', sessionID: 's1' },
    onSelect,
    sessionDetail: null,
    turnState: 'idle' as const,
    onSend: vi.fn(),
    overlay,
    sessionsOpen: false,
    setSessionsOpen,
    filesOpen: false,
    setFilesOpen,
    previewPayload: null,
    setPreviewPayload,
    ...overrides,
  };
  const utils = render(<MobileShell {...props} />);
  return { overlay, onSelect, setSessionsOpen, setFilesOpen, setPreviewPayload, ...utils };
}

test('renders chat workspace with Sessions and Files trigger buttons in chat-header', () => {
  renderShell();
  const header = document.querySelector('.chat-header') as HTMLElement;
  expect(within(header).getByRole('button', { name: /Sessions/ })).toBeInTheDocument();
  expect(within(header).getByRole('button', { name: /Files/ })).toBeInTheDocument();
});

test('clicking Sessions calls overlay.open + setSessionsOpen(true); drawer renders when prop is true', () => {
  const { setSessionsOpen, overlay, rerender } = renderShell();
  const openSpy = vi.spyOn(overlay, 'open');
  fireEvent.click(screen.getByRole('button', { name: /Sessions/ }));
  expect(openSpy).toHaveBeenCalledWith('sessions');
  expect(setSessionsOpen).toHaveBeenCalledWith(true);

  // Re-render with sessionsOpen=true (simulating CommanderApp setState).
  rerender(
    <MobileShell
      daemons={daemons}
      selected={{ daemonID: 'd1', sessionID: 's1' }}
      onSelect={vi.fn()}
      sessionDetail={null}
      turnState="idle"
      onSend={vi.fn()}
      overlay={overlay}
      sessionsOpen
      setSessionsOpen={setSessionsOpen}
      filesOpen={false}
      setFilesOpen={vi.fn()}
      previewPayload={null}
      setPreviewPayload={vi.fn()}
    />,
  );
  const drawer = screen.getByTestId('drawer-left');
  expect(within(drawer).getByTestId('daemon-tree')).toBeInTheDocument();
});

test('selecting a session in the drawer forwards onSelect and asks overlay.closeTop("sessions")', () => {
  const { overlay, setSessionsOpen, onSelect, rerender } = renderShell();
  // Simulate CommanderApp's open path: push 'sessions' onto the controller's
  // stack so handleSelectSession's closeOverlay helper takes the "top
  // matches" branch and exercises closeTop instead of the empty-stack
  // fallback covered by the next test.
  overlay.open('sessions');
  rerender(
    <MobileShell
      daemons={daemons}
      selected={{ daemonID: 'd1', sessionID: 's1' }}
      onSelect={onSelect}
      sessionDetail={null}
      turnState="idle"
      onSend={vi.fn()}
      overlay={overlay}
      sessionsOpen
      setSessionsOpen={setSessionsOpen}
      filesOpen={false}
      setFilesOpen={vi.fn()}
      previewPayload={null}
      setPreviewPayload={vi.fn()}
    />,
  );
  const drawer = screen.getByTestId('drawer-left');
  const closeSpy = vi.spyOn(overlay, 'closeTop').mockImplementation(() => {});
  fireEvent.click(within(drawer).getByRole('button', { name: /Session one/ }));
  expect(onSelect).toHaveBeenCalledWith('d1', 's1');
  expect(closeSpy).toHaveBeenCalledWith('sessions');
});

test('overlay.onPop("sessions") triggers setSessionsOpen(false)', () => {
  const { overlay, setSessionsOpen } = renderShell({ sessionsOpen: true });
  // simulate browser back popping the top of the controller's stack
  // The hook attaches the popstate listener internally; just dispatch the event
  // after pushing the same id into the controller so the stack is non-empty.
  overlay.open('sessions');
  window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  expect(setSessionsOpen).toHaveBeenCalledWith(false);
});

test('closing a drawer with empty overlay stack falls back to setOpen(false), never gets stuck', () => {
  // Defensive case: a remount / double-close could leave the controller's
  // stack empty while the React state still says sessionsOpen=true.
  // Clicking close (or pressing ESC, which Radix routes through onOpenChange)
  // must close the drawer instead of no-op'ing via closeTop.
  const { overlay, setSessionsOpen } = renderShell({ sessionsOpen: true });
  expect(overlay.stackSnapshot()).toEqual([]); // pre-condition
  const closeBtn = screen.getByRole('button', { name: /关闭 Sessions/ });
  fireEvent.click(closeBtn);
  expect(setSessionsOpen).toHaveBeenCalledWith(false);
});

test('empty=true when selected == null (composer disabled via ChatWorkspace)', () => {
  renderShell({ selected: null });
  expect(screen.getByLabelText('输入提示词')).toBeDisabled();
});
