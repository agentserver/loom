import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import type { DaemonTree, SessionRow } from '../api/types';
import { DaemonSessionTree } from './DaemonSessionTree';

afterEach(cleanup);

test('nests subagent sessions under their parent and keeps them collapsed by default', () => {
  const onSelect = vi.fn();
  const daemons: DaemonTree[] = [
    {
      daemon_id: 'd1',
      display_name: 'prod-codex',
      kind: 'codex',
      status: 'ok',
      sessions: [
        {
          daemon_id: 'd1',
          session_id: 'parent-1',
          kind: 'codex',
          title: 'Implement feature',
          origin: 'user',
          turn_state: 'idle',
          active_worker: false,
          awaiting_approval: false,
        },
        {
          daemon_id: 'd1',
          session_id: 'child-1',
          kind: 'codex',
          title: 'Review implementation',
          origin: 'subagent',
          parent_id: 'parent-1',
          agent_name: 'Lovelace',
          turn_state: 'idle',
          active_worker: false,
          awaiting_approval: false,
        },
      ],
    },
  ];

  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={onSelect} />);

  expect(screen.getByText('Implement feature')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Review implementation/ })).toBeNull();

  fireEvent.click(screen.getByRole('button', { name: '展开 subagent sessions: Implement feature' }));

  const child = screen.getByRole('button', { name: /Review implementation/ });
  expect(child).toBeInTheDocument();
  expect(within(child).getByText('subagent · Lovelace')).toBeInTheDocument();

  fireEvent.click(child);
  expect(onSelect).toHaveBeenCalledWith('d1', 'child-1');
});

test('labels codex exec sessions as agent tasks', () => {
  const daemons: DaemonTree[] = [
    {
      daemon_id: 'd1',
      display_name: 'prod-codex',
      kind: 'codex',
      status: 'ok',
      sessions: [
        {
          daemon_id: 'd1',
          session_id: 'task-1',
          kind: 'codex',
          title: 'ack',
          origin: 'agent_task',
          working_dir: '/tmp/slave-workdir',
          turn_state: 'idle',
          active_worker: false,
          awaiting_approval: false,
        },
      ],
    },
  ];

  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={vi.fn()} />);

  const task = screen.getByRole('button', { name: /ack/ });
  expect(within(task).getByText('agent task · /tmp/slave-workdir')).toBeInTheDocument();
});

test('marks sessions with active workers separately from turn state', () => {
  const daemons: DaemonTree[] = [
    {
      daemon_id: 'd1',
      display_name: 'prod-codex',
      kind: 'codex',
      status: 'ok',
      sessions: [
        {
          daemon_id: 'd1',
          session_id: 'hot-1',
          kind: 'codex',
          title: 'Resume work',
          origin: 'user',
          turn_state: 'idle',
          active_worker: true,
          awaiting_approval: false,
        },
      ],
    },
  ];

  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={vi.fn()} />);

  const row = screen.getByRole('button', { name: /Resume work/ });
  expect(within(row).getByText('active')).toHaveAttribute(
    'title',
    'Daemon has a hot worker cached for this session',
  );
  expect(within(row).getByText('idle')).toBeInTheDocument();
});

const row = (over: Partial<SessionRow>): SessionRow => ({
  daemon_id: 'd', session_id: 's', kind: 'codex', title: 't',
  turn_state: 'idle', active_worker: false, awaiting_approval: false, ...over,
});

test('nests a remote agent_task child under a parent in another daemon (default-collapsed)', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [row({ daemon_id: 'drv', session_id: 'parent-s', owner_agent_id: 'drv-1', origin: 'user', title: 'parent-s' })] },
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'child-s', owner_agent_id: 'slv-2', title: 'child-s',
        origin: 'agent_task', parent_id: 'parent-s', parent_agent_id: 'drv-1', parent_display_name: 'prod-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);

  // Default-collapsed: child is NOT visible until the parent is expanded.
  expect(screen.queryByText(/remote task · on slave-02/)).toBeNull();
  // The child must NOT appear as a root in its home (slave) daemon group.
  const slaveGroup = within(screen.getByText('slave-02').closest('section')!);
  const slaveRoots = slaveGroup.queryAllByTestId('root-session');
  expect(slaveRoots).toHaveLength(0);

  // Sanity: the driver group has exactly one root, the parent.
  const driverGroup = within(screen.getByText('prod-driver').closest('section')!);
  const driverRoots = driverGroup.queryAllByTestId('root-session');
  expect(driverRoots).toHaveLength(1);
  expect(driverRoots[0].textContent).toContain('parent-s');
  expect(driverRoots.some(r => (r.textContent ?? '').includes('child-s'))).toBe(false);

  // Expand the parent; now the remote child + badge appear.
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: parent-s/));
  expect(screen.getByText(/remote task · on slave-02/)).toBeInTheDocument();
});

test('renders parent-offline note when parent is not in any daemon', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'orphan-s', owner_agent_id: 'slv-2',
        origin: 'agent_task', parent_id: 'gone-s', parent_agent_id: 'drv-gone', parent_display_name: 'old-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  // orphan renders as a root (visible without expansion) with the note.
  const offlineText = screen.getByText(/parent offline/i);
  expect(offlineText).toBeInTheDocument();
  expect(screen.getByText(/old-driver/)).toBeInTheDocument();
  // The muted CSS class is the user-facing signal distinguishing a stale
  // parent-offline root from a normal root — pin it so a future refactor
  // can't silently drop the styling branch.
  expect(offlineText.className).toContain('session-meta-muted');
});

test('still nests local subagents that have only parent_id (no parent_agent_id)', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [
        row({ daemon_id: 'drv', session_id: 'u-s', owner_agent_id: 'drv-1', origin: 'user', title: 'u-s' }),
        row({ daemon_id: 'drv', session_id: 'sub-s', owner_agent_id: 'drv-1', origin: 'subagent', parent_id: 'u-s', title: 'sub-s' }),
      ] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  // sub-s must NOT be a root (it nests under u-s); expand u-s and find the subagent label.
  const drvGroup = screen.getByText('prod-driver').closest('section')!;
  expect(drvGroup.textContent).not.toMatch(/sub-s.*sub-s/); // not duplicated as a root
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: u-s/));
  expect(screen.getByText(/subagent ·/)).toBeInTheDocument();
});

test('clicking a remote child selects the child home daemon, not the parent daemon', () => {
  const onSelect = vi.fn();
  const daemons: DaemonTree[] = [
    { daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [row({ daemon_id: 'drv', session_id: 'parent-s', owner_agent_id: 'drv-1', origin: 'user', title: 'parent-s' })] },
    { daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [row({ daemon_id: 'slv', session_id: 'child-s', owner_agent_id: 'slv-2', title: 'child-s',
        origin: 'agent_task', parent_id: 'parent-s', parent_agent_id: 'drv-1', parent_display_name: 'prod-driver' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={onSelect} />);
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: parent-s/));
  fireEvent.click(screen.getByText('child-s'));
  expect(onSelect).toHaveBeenCalledWith('slv', 'child-s'); // child's daemon, not 'drv'
});

// Backward-compat: two pre-P2 daemons reporting the same session_id with NO
// owner_agent_id must not collide in the global (effectiveOwner, session_id) map.
test('two daemons with the same session_id and no owner_agent_id render independently', () => {
  const daemons: DaemonTree[] = [
    { daemon_id: 'd1', display_name: 'old-codex-1', kind: 'codex', status: 'ok',
      sessions: [row({ daemon_id: 'd1', session_id: 's', origin: 'user', title: 'in d1' })] },
    { daemon_id: 'd2', display_name: 'old-codex-2', kind: 'codex', status: 'ok',
      sessions: [row({ daemon_id: 'd2', session_id: 's', origin: 'user', title: 'in d2' })] },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);
  // Both sessions must render as roots in their own group, no collision.
  const d1Group = within(screen.getByText('old-codex-1').closest('section')!);
  const d2Group = within(screen.getByText('old-codex-2').closest('section')!);
  expect(d1Group.queryAllByTestId('root-session')).toHaveLength(1);
  expect(d2Group.queryAllByTestId('root-session')).toHaveLength(1);
  expect(d1Group.getByText(/in d1/)).toBeInTheDocument();
  expect(d2Group.getByText(/in d2/)).toBeInTheDocument();
});

const oneOkDaemon: DaemonTree[] = [
  {
    daemon_id: 'd1',
    display_name: 'prod-codex',
    kind: 'codex',
    status: 'ok',
    sessions: [
      {
        daemon_id: 'd1',
        session_id: 's1',
        kind: 'codex',
        title: 'Real session',
        origin: 'user',
        turn_state: 'idle',
        active_worker: false,
        awaiting_approval: false,
      },
    ],
  },
];

const twoOkDaemons: DaemonTree[] = [
  ...oneOkDaemon,
  {
    daemon_id: 'd2',
    display_name: 'other',
    kind: 'codex',
    status: 'ok',
    sessions: [],
  },
];

test('+ button rendered on ok daemon when onCreateSession provided', () => {
  const onCreateSession = vi.fn();
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={onCreateSession}
    />,
  );
  const btn = screen.getByRole('button', { name: /新建 session: prod-codex/ });
  expect(btn).toBeEnabled();
  fireEvent.click(btn);
  expect(onCreateSession).toHaveBeenCalledWith('d1');
});

test('+ button hidden on non-ok daemon (status text shown instead)', () => {
  const offline: DaemonTree[] = [
    { ...oneOkDaemon[0], status: 'offline' },
  ];
  render(
    <DaemonSessionTree
      daemons={offline}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={vi.fn()}
    />,
  );
  expect(screen.queryByRole('button', { name: /新建 session/ })).not.toBeInTheDocument();
  expect(screen.getByText('offline')).toBeInTheDocument();
});

test('+ button on other daemon disabled when draft pending exists; title says 先发送或丢弃当前草稿', () => {
  const onCreateSession = vi.fn();
  render(
    <DaemonSessionTree
      daemons={twoOkDaemons}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={onCreateSession}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'draft' }}
    />,
  );
  const otherBtn = screen.getByRole('button', { name: /新建 session: other/ });
  expect(otherBtn).toBeDisabled();
  expect(otherBtn).toHaveAttribute('title', '先发送或丢弃当前草稿');
  fireEvent.click(otherBtn);
  expect(onCreateSession).not.toHaveBeenCalled();
});

test('+ button on other daemon disabled when submitting pending exists; title says 等待新会话出现在列表中', () => {
  render(
    <DaemonSessionTree
      daemons={twoOkDaemons}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={vi.fn()}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'submitting' }}
    />,
  );
  const otherBtn = screen.getByRole('button', { name: /新建 session: other/ });
  expect(otherBtn).toBeDisabled();
  expect(otherBtn).toHaveAttribute('title', '等待新会话出现在列表中');
});

test('pendingSession matching a daemon inserts a virtual row at top with draft title', () => {
  const onSelect = vi.fn();
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={onSelect}
      onCreateSession={vi.fn()}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'draft' }}
    />,
  );
  // Virtual row is first
  const buttons = screen.getAllByRole('button', { name: /会话/ });
  // The first .session-row button should be the pending virtual row.
  expect(within(buttons[0]).getByText('新建会话(待提交)')).toBeInTheDocument();
  fireEvent.click(buttons[0]);
  expect(onSelect).toHaveBeenCalledWith('d1', 'pending-uuid');
});

test('submitting phase virtual row uses 新建会话(同步中…) and no × button', () => {
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={vi.fn()}
      onCreateSession={vi.fn()}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'submitting' }}
    />,
  );
  expect(screen.getByText('新建会话(同步中…)')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '丢弃草稿' })).not.toBeInTheDocument();
});

test('× discard button on draft virtual row calls onDiscardSession and does NOT call onSelect', () => {
  const onSelect = vi.fn();
  const onDiscardSession = vi.fn();
  render(
    <DaemonSessionTree
      daemons={oneOkDaemon}
      selected={null}
      onSelect={onSelect}
      onCreateSession={vi.fn()}
      onDiscardSession={onDiscardSession}
      pendingSession={{ daemonID: 'd1', sessionID: 'pending-uuid', phase: 'draft' }}
    />,
  );
  fireEvent.click(screen.getByRole('button', { name: '丢弃草稿' }));
  expect(onDiscardSession).toHaveBeenCalledWith('pending-uuid');
  expect(onSelect).not.toHaveBeenCalled();
});

test('renders descendants of a remote agent_task (slave subagent under driver→remote→subagent chain)', () => {
  // The P2-shipped shape: driver session → submit_task delegates to slave
  // (creates a remote agent_task on slave) → that codex_exec spawns its
  // own subagent on the slave. Three-level tree across two daemons. The
  // slave subagent gets filtered from the slave's root list (it's a
  // resolved child) AND must render under its agent_task parent, which
  // itself is nested under the driver root. Without recursive rendering,
  // the subagent disappears entirely from the UI. #24 P3 review.
  const daemons: DaemonTree[] = [
    {
      daemon_id: 'drv', display_name: 'prod-driver', kind: 'codex', status: 'ok', short_id: 'drv-1',
      sessions: [row({
        daemon_id: 'drv', session_id: 'driver-s', owner_agent_id: 'drv-1',
        origin: 'user', title: 'driver-s',
      })],
    },
    {
      daemon_id: 'slv', display_name: 'slave-02', kind: 'codex', status: 'ok', short_id: 'slv-2',
      sessions: [
        row({
          daemon_id: 'slv', session_id: 'task-s', owner_agent_id: 'slv-2', title: 'task-s',
          origin: 'agent_task', parent_id: 'driver-s', parent_agent_id: 'drv-1', parent_display_name: 'prod-driver',
        }),
        row({
          daemon_id: 'slv', session_id: 'sub-s', owner_agent_id: 'slv-2',
          origin: 'subagent', parent_id: 'task-s', agent_name: 'Lovelace', title: 'sub-s',
        }),
      ],
    },
  ];
  render(<DaemonSessionTree daemons={daemons} selected={null} onSelect={() => {}} />);

  // Neither nested session appears as a slave-group root.
  const slaveGroup = within(screen.getByText('slave-02').closest('section')!);
  expect(slaveGroup.queryAllByTestId('root-session')).toHaveLength(0);

  // Driver root exists; nothing visible from the subtree until we expand.
  expect(screen.queryByText(/remote task · on slave-02/)).toBeNull();
  expect(screen.queryByText(/subagent · Lovelace/)).toBeNull();

  // Expand the driver root → remote agent_task surfaces.
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: driver-s/));
  expect(screen.getByText(/remote task · on slave-02/)).toBeInTheDocument();
  // Still default-collapsed at the next level — the slave subagent is not
  // visible until its agent_task parent is expanded.
  expect(screen.queryByText(/subagent · Lovelace/)).toBeNull();

  // Expand the remote agent_task → its slave subagent finally shows.
  fireEvent.click(screen.getByLabelText(/展开 subagent sessions: task-s/));
  expect(screen.getByText(/subagent · Lovelace/)).toBeInTheDocument();
});
