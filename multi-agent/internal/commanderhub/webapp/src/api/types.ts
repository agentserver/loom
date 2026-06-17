export type TurnState =
  | 'idle'
  | 'queued'
  | 'answering'
  | 'done'
  | 'error'
  | 'awaiting_approval'
  | 'disconnected';

export interface SessionRow {
  daemon_id: string;
  session_id: string;
  kind: string;
  title: string;
  origin?: 'user' | 'subagent' | 'agent_task' | 'unknown' | string;
  parent_id?: string;
  agent_name?: string;
  agent_role?: string;
  working_dir?: string;
  updated_at?: string;
  message_count?: number;
  preview?: string;
  turn_state: TurnState;
  active_worker: boolean;
  awaiting_approval: boolean;
}

export interface DaemonTree {
  daemon_id: string;
  display_name: string;
  kind: string;
  driver_version?: string;
  capabilities?: string[];
  status: string;
  error?: string;
  sessions?: SessionRow[];
}

export interface CommanderTree {
  daemons: DaemonTree[];
}

export interface SessionMessage {
  role: string;
  text: string;
  ts?: string;
}

export interface SessionDetail {
  session: Record<string, unknown>;
  messages: SessionMessage[];
}

export interface FileEntry {
  name: string;
  path: string;
  kind: 'file' | 'dir' | string;
  size?: number;
  mod_time?: string;
}

export interface FileListResult {
  root: string;
  path: string;
  entries: FileEntry[];
}

export interface FileReadResult {
  path: string;
  size: number;
  mime?: string;
  binary?: boolean;
  too_large?: boolean;
  content?: string;
}
