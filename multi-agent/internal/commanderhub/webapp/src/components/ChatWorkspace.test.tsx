import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { ChatWorkspace } from './ChatWorkspace';

afterEach(cleanup);

function renderWorkspace(props: Partial<Parameters<typeof ChatWorkspace>[0]> = {}) {
  return render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={{
        session: { ID: 's1', Title: 'Fix cache', WorkingDir: '/repo' },
        messages: [{ role: 'assistant', text: 'real codex answer' }],
      }}
      turnState="idle"
      onSend={async () => {}}
      {...props}
    />,
  );
}

test('renders turn state outside assistant message body', () => {
  renderWorkspace({ turnState: 'answering' });
  expect(screen.getByTestId('turn-status')).toHaveTextContent('Codex 正在回答');
  expect(screen.getByTestId('message-list')).toHaveTextContent('real codex answer');
  expect(screen.getByTestId('message-list')).not.toHaveTextContent('Codex 正在回答');
});

test('renders queued turn state as a live status', () => {
  renderWorkspace({ turnState: 'queued' });
  const status = screen.getByRole('status');
  expect(status).toHaveAttribute('aria-live', 'polite');
  expect(status).toHaveTextContent('已排队');
});

test('does not send or clear whitespace prompts', () => {
  const onSend = vi.fn(async () => {});
  renderWorkspace({ onSend });
  const textarea = screen.getByRole('textbox', { name: '输入提示词' }) as HTMLTextAreaElement;

  fireEvent.change(textarea, { target: { value: '   ' } });
  fireEvent.submit(textarea.form!);

  expect(onSend).not.toHaveBeenCalled();
  expect(textarea.value).toBe('   ');
});

test('trims prompt and clears after successful send resolves', async () => {
  let resolveSend: () => void = () => {};
  const onSend = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        resolveSend = resolve;
      }),
  );
  renderWorkspace({ onSend });
  const textarea = screen.getByRole('textbox', { name: '输入提示词' }) as HTMLTextAreaElement;

  fireEvent.change(textarea, { target: { value: '  run tests  ' } });
  fireEvent.submit(textarea.form!);

  expect(onSend).toHaveBeenCalledWith('run tests');
  expect(textarea.value).toBe('  run tests  ');
  resolveSend();
  await waitFor(() => expect(textarea.value).toBe(''));
});

test('keeps prompt when send fails', async () => {
  const onSend = vi.fn(async () => {
    throw new Error('send failed');
  });
  renderWorkspace({ onSend });
  const textarea = screen.getByRole('textbox', { name: '输入提示词' }) as HTMLTextAreaElement;

  fireEvent.change(textarea, { target: { value: 'please retry' } });
  fireEvent.submit(textarea.form!);

  await waitFor(() => expect(onSend).toHaveBeenCalledWith('please retry'));
  expect(textarea.value).toBe('please retry');
});
