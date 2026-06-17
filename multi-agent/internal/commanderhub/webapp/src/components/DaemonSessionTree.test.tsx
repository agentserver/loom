import { fireEvent, render, screen, within } from '@testing-library/react';
import { expect, test, vi } from 'vitest';
import type { DaemonTree } from '../api/types';
import { DaemonSessionTree } from './DaemonSessionTree';

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
