import { render, screen } from '@testing-library/react';
import { expect, test } from 'vitest';
import { ChatWorkspace } from './ChatWorkspace';

test('renders turn state outside assistant message body', () => {
  render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={{
        session: { ID: 's1', Title: 'Fix cache', WorkingDir: '/repo' },
        messages: [{ Role: 'assistant', Text: 'real codex answer' }],
      }}
      turnState="answering"
      onSend={async () => {}}
    />,
  );
  expect(screen.getByTestId('turn-status')).toHaveTextContent('Codex 正在回答');
  expect(screen.getByTestId('message-list')).toHaveTextContent('real codex answer');
  expect(screen.getByTestId('message-list')).not.toHaveTextContent('Codex 正在回答');
});
