import { MessageRenderer } from './MessageRenderer';
import type { SessionDetail, TurnState } from '../api/types';

const turnStateLabels: Record<TurnState, string> = {
  idle: '',
  queued: '已排队',
  starting: '正在启动 Codex',
  answering: 'Codex 正在回答',
  awaiting_approval: '需人工审批',
  done: '已回答完毕',
  error: '出错',
  disconnected: '已断开',
};

function displayTurnState(state: TurnState | string) {
  if (Object.prototype.hasOwnProperty.call(turnStateLabels, state)) {
    return turnStateLabels[state as TurnState];
  }
  return '';
}

function sessionString(session: Record<string, unknown> | undefined, ...keys: string[]) {
  for (const key of keys) {
    const value = session?.[key];
    if (typeof value === 'string') return value;
  }
  return '';
}

export function ChatWorkspace({
  session,
  turnState,
  onSend,
}: {
  daemonID: string;
  sessionID: string;
  session: SessionDetail | null;
  turnState: TurnState | string;
  onSend: (prompt: string) => Promise<void>;
}) {
  const title = sessionString(session?.session, 'Title', 'title') || 'Session';
  const cwd = sessionString(session?.session, 'WorkingDir', 'working_dir');
  const disabled = ['queued', 'starting', 'answering', 'awaiting_approval'].includes(turnState);

  return (
    <main className="chat-workspace" data-testid="chat-workspace">
      <header className="chat-header">
        <div>
          <h1>{title}</h1>
          <p>{cwd}</p>
        </div>
        <span data-testid="turn-status" className="turn-status" role="status" aria-live="polite">
          {displayTurnState(turnState)}
        </span>
      </header>
      <div data-testid="message-list" className="message-list">
        {(session?.messages || []).map((msg, index) => {
          const role = msg.Role || msg.role || 'assistant';
          const text = msg.Text || msg.text || '';
          return (
            <article key={index} className={`message message-${role}`}>
              <MessageRenderer text={text} />
            </article>
          );
        })}
      </div>
      <form
        className="composer"
        onSubmit={async (event) => {
          event.preventDefault();
          const input = event.currentTarget.elements.namedItem('prompt') as HTMLTextAreaElement;
          const prompt = input.value.trim();
          if (!prompt) return;
          try {
            await onSend(prompt);
            input.value = '';
          } catch {
            // Keep the draft in place so the user can retry or edit it.
          }
        }}
      >
        <textarea aria-label="输入提示词" name="prompt" disabled={disabled} />
        <button type="submit" disabled={disabled}>
          发送
        </button>
      </form>
    </main>
  );
}
