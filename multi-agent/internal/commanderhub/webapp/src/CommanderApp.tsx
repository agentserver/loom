import { useEffect, useState } from 'react';
import { apiGet } from './api/client';
import type { CommanderTree } from './api/types';

export function CommanderApp() {
  const [tree, setTree] = useState<CommanderTree | null>(null);
  const [error, setError] = useState<string>('');

  useEffect(() => {
    apiGet<CommanderTree>('/api/commander/tree')
      .then(setTree)
      .catch((err: Error) => setError(err.message));
  }, []);

  if (error === 'unauthorized') return <div className="login-shell">用 agentserver 登录</div>;
  if (error) return <div className="login-shell">加载失败: {error}</div>;
  if (!tree) return <div className="login-shell">加载中</div>;
  return <div className="commander-shell">{tree.daemons.length} daemons</div>;
}
