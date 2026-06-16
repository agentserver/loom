import { render, screen } from '@testing-library/react';
import { expect, test } from 'vitest';
import { DaemonSessionTree } from './DaemonSessionTree';

test('groups sessions under daemon rows', () => {
  render(
    <DaemonSessionTree
      daemons={[
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
              title: 'Fix session cache',
              turn_state: 'answering',
              active_worker: false,
              awaiting_approval: false,
            },
          ],
        },
      ]}
      selected={{ daemonID: 'd1', sessionID: 's1' }}
      onSelect={() => {}}
    />,
  );
  expect(screen.getByText('prod-codex')).toBeInTheDocument();
  expect(screen.getByText('Fix session cache')).toBeInTheDocument();
  expect(screen.getByText('answering')).toBeInTheDocument();
});
