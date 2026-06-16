import { MessageRenderer } from './MessageRenderer';
import type { SessionMessage, TurnState } from '../api/types';

export interface SessionDetail {
  session: {
    ID?: string;
    Title?: string;
    WorkingDir?: string;
    id?: string;
    title?: string;
    working_dir?: string;
  };
  messages: SessionMessage[];
}

function displayTurnState(state: TurnState | string) {
  if (state === 'starting') return '正在启动 Codex';
  if (state === 'answering') return 'Codex 正在回答';
  if (state === 'awaiting_approval') return '需人工审批';
  if (state === 'done') return '已回答完毕';
  if (state === 'error') return '出错';
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
  const title = session?.session.Title || session?.session.title || 'Session';
  const cwd = session?.session.WorkingDir || session?.session.working_dir || '';
  const disabled = ['queued', 'starting', 'answering', 'awaiting_approval'].includes(turnState);

  return (
    <main className="chat-workspace">
      <header className="chat-header">
        <div>
          <h1>{title}</h1>
          <p>{cwd}</p>
        </div>
        <span data-testid="turn-status" className="turn-status">
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
        onSubmit={(event) => {
          event.preventDefault();
          const input = event.currentTarget.elements.namedItem('prompt') as HTMLTextAreaElement;
          void onSend(input.value);
          input.value = '';
        }}
      >
        <textarea name="prompt" disabled={disabled} />
        <button type="submit" disabled={disabled}>
          发送
        </button>
      </form>
    </main>
  );
}
