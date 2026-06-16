import { useEffect, useState } from 'react';
import { apiGet } from './api/client';
import type { CommanderTree } from './api/types';
import { ChatWorkspace } from './components/ChatWorkspace';
import { DaemonSessionTree } from './components/DaemonSessionTree';
import { FileExplorerPanel } from './components/FileExplorerPanel';

export function CommanderApp() {
  const [tree, setTree] = useState<CommanderTree | null>(null);
  const [error, setError] = useState<string>('');
  const [selected, setSelected] = useState<{ daemonID: string; sessionID: string } | null>(null);

  useEffect(() => {
    apiGet<CommanderTree>('/api/commander/tree')
      .then(setTree)
      .catch((err: Error) => setError(err.message));
  }, []);

  if (error === 'unauthorized') return <div className="login-shell">用 agentserver 登录</div>;
  if (error) return <div className="login-shell">加载失败: {error}</div>;
  if (!tree) return <div className="login-shell">加载中</div>;
  return (
    <div className="commander-shell">
      <DaemonSessionTree
        daemons={tree.daemons}
        selected={selected}
        onSelect={(daemonID, sessionID) => setSelected({ daemonID, sessionID })}
      />
      <ChatWorkspace
        daemonID={selected?.daemonID || ''}
        sessionID={selected?.sessionID || ''}
        session={null}
        turnState="idle"
        onSend={async () => {}}
      />
      <FileExplorerPanel />
    </div>
  );
}
