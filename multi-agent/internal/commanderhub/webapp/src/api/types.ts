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
  /**
   * Stable ShortID of the daemon that owns this session, populated by the
   * observer from RegisterPayload.short_id (#24 P2). Used together with
   * session_id as the owner-aware key for cross-daemon parent resolution.
   */
  owner_agent_id?: string;
  /**
   * For agent_task sessions, the ShortID of the originating driver/master
   * (carried on the loom_origin marker from #24 P2). Lets the frontend
   * nest a remote child under its parent even when the parent lives in a
   * different daemon group.
   */
  parent_agent_id?: string;
  /**
   * Denormalised display name of the parent's daemon, so the parent-offline
   * badge keeps a human-readable label after the parent's daemon
   * disconnects.
   */
  parent_display_name?: string;
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
  /**
   * Stable agent-instance ShortID (#24 P2). Optional because older
   * daemons predate the field; consumers fall back to daemon_id-namespaced
   * keys via DaemonSessionTree's effectiveOwner helper.
   */
  short_id?: string;
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
