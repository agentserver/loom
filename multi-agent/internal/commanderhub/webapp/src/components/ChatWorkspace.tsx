import { useLayoutEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { MessageRenderer } from './MessageRenderer';
import type { SessionDetail, TurnState } from '../api/types';

const turnStateLabels: Record<TurnState, string> = {
  idle: '',
  queued: '已排队',
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
  mobileLeading,
  mobileTrailing,
  empty,
}: {
  daemonID: string;
  sessionID: string;
  session: SessionDetail | null;
  turnState: TurnState | string;
  onSend: (prompt: string) => Promise<void>;
  mobileLeading?: ReactNode;
  mobileTrailing?: ReactNode;
  empty?: boolean;
}) {
  const title = sessionString(session?.session, 'Title', 'title') || 'Session';
  const cwd = sessionString(session?.session, 'WorkingDir', 'working_dir');
  const disabled =
    empty === true ||
    ['queued', 'answering', 'awaiting_approval'].includes(turnState);
  const messages = session?.messages || [];
  const messageListRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const list = messageListRef.current;
    if (!list) return;
    list.scrollTop = list.scrollHeight;
  }, [sessionString(session?.session, 'ID', 'id'), messages.length]);

  return (
    <main className="chat-workspace" data-testid="chat-workspace">
      <header className="chat-header">
        {mobileLeading ? <div className="chat-header-slot">{mobileLeading}</div> : null}
        <div className="chat-header-title">
          <h1>{title}</h1>
          <p>{cwd}</p>
        </div>
        <span data-testid="turn-status" className="turn-status" role="status" aria-live="polite">
          {displayTurnState(turnState)}
        </span>
        {mobileTrailing ? <div className="chat-header-slot">{mobileTrailing}</div> : null}
      </header>
      <div data-testid="message-list" className="message-list" ref={messageListRef}>
        {empty ? (
          <p className="message-list-empty">
            No sessions yet — open Sessions to pick one once a daemon appears
          </p>
        ) : (
          messages.map((msg, index) => {
            const role = msg.role || 'assistant';
            const text = msg.text || '';
            return (
              <article key={index} className={`message message-${role}`}>
                <MessageRenderer text={text} />
              </article>
            );
          })
        )}
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
