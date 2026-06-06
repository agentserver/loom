CREATE TABLE IF NOT EXISTS api_keys (
  id text PRIMARY KEY,
  key_hash text NOT NULL UNIQUE,
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
  id text PRIMARY KEY,
  name text NOT NULL DEFAULT '',
  created_by_api_key_id text NOT NULL REFERENCES api_keys(id),
  created_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  workspace_id text NOT NULL,
  id text NOT NULL,
  role text NOT NULL,
  display_name text NOT NULL,
  token_hash text NOT NULL,
  created_by_api_key_id text NOT NULL REFERENCES api_keys(id),
  PRIMARY KEY (workspace_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_token_hash ON agents(token_hash);

CREATE TABLE IF NOT EXISTS telemetry_api_keys (
  id text PRIMARY KEY,
  key_hash text NOT NULL UNIQUE,
  note text NOT NULL DEFAULT '',
  workspace_id text NOT NULL DEFAULT '*',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  event_id text PRIMARY KEY,
  ts timestamptz NOT NULL,
  workspace_id text NOT NULL,
  agent_id text NOT NULL,
  agent_role text NOT NULL,
  type text NOT NULL,
  task_id text NOT NULL DEFAULT '',
  parent_task_id text,
  subtask_id text,
  child_task_id text,
  summary text,
  subtask_summary text,
  status text,
  target_agent_id text,
  target_role text,
  mcp_server_name text,
  mcp_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
  payload jsonb
);
CREATE INDEX IF NOT EXISTS idx_events_workspace_ts ON events(workspace_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_workspace_task_ts ON events(workspace_id, task_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_workspace_agent_ts ON events(workspace_id, agent_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_workspace_type_ts ON events(workspace_id, type, ts);

CREATE TABLE IF NOT EXISTS tasks (
  workspace_id text NOT NULL,
  task_id text NOT NULL,
  driver_agent_id text,
  master_agent_id text,
  slave_agent_id text,
  summary text NOT NULL,
  status text NOT NULL,
  has_mcp boolean NOT NULL DEFAULT false,
  mcp_status text NOT NULL DEFAULT '',
  latest_progress text NOT NULL DEFAULT '',
  latest_progress_phase text NOT NULL DEFAULT '',
  latest_progress_at text NOT NULL DEFAULT '',
  final_output text NOT NULL DEFAULT '',
  is_final boolean NOT NULL DEFAULT false,
  output text,
  error text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_workspace_updated ON tasks(workspace_id, updated_at);

CREATE TABLE IF NOT EXISTS subtasks (
  workspace_id text NOT NULL,
  parent_task_id text NOT NULL,
  subtask_id text NOT NULL,
  child_task_id text,
  master_agent_id text,
  slave_agent_id text,
  summary text NOT NULL,
  display_label text NOT NULL,
  status text NOT NULL,
  mcp_status text NOT NULL DEFAULT '',
  latest_progress text NOT NULL DEFAULT '',
  latest_progress_phase text NOT NULL DEFAULT '',
  latest_progress_at text NOT NULL DEFAULT '',
  output text,
  error text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, parent_task_id, subtask_id)
);
CREATE INDEX IF NOT EXISTS idx_subtasks_child ON subtasks(workspace_id, child_task_id);

CREATE TABLE IF NOT EXISTS mcp_servers (
  workspace_id text NOT NULL,
  task_id text NOT NULL,
  parent_task_id text,
  slave_agent_id text NOT NULL,
  name text NOT NULL,
  tools jsonb NOT NULL DEFAULT '[]'::jsonb,
  tool_descriptors jsonb,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, task_id, name)
);

CREATE TABLE IF NOT EXISTS artifacts (
  workspace_id text NOT NULL,
  id text NOT NULL,
  owner_agent_id text NOT NULL,
  path text NOT NULL,
  kind text NOT NULL,
  mime text NOT NULL DEFAULT '',
  state text NOT NULL,
  bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  object_key text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_owner_state ON artifacts(workspace_id, owner_agent_id, state);

CREATE TABLE IF NOT EXISTS artifact_requests (
  workspace_id text NOT NULL,
  id text NOT NULL,
  artifact_id text NOT NULL,
  requester_agent_id text NOT NULL,
  owner_agent_id text NOT NULL,
  state text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_artifact_requests_owner_state ON artifact_requests(workspace_id, owner_agent_id, state);

CREATE TABLE IF NOT EXISTS writes (
  workspace_id text NOT NULL,
  id text NOT NULL,
  owner_agent_id text NOT NULL,
  writer_agent_id text NOT NULL DEFAULT '',
  task_id text NOT NULL,
  path text NOT NULL,
  overwrite boolean NOT NULL DEFAULT false,
  state text NOT NULL,
  mime text NOT NULL DEFAULT '',
  bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  object_key text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_writes_owner_task_state ON writes(workspace_id, owner_agent_id, task_id, state);

CREATE TABLE IF NOT EXISTS task_contracts (
  workspace_id text NOT NULL,
  task_id text NOT NULL,
  conversation_id text NOT NULL,
  owner_agent_id text NOT NULL,
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_task_contracts_conversation ON task_contracts(workspace_id, conversation_id, updated_at);

CREATE TABLE IF NOT EXISTS resource_snapshots (
  workspace_id text NOT NULL,
  snapshot_id text NOT NULL,
  owner_agent_id text NOT NULL,
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, snapshot_id)
);
CREATE INDEX IF NOT EXISTS idx_resource_snapshots_latest ON resource_snapshots(workspace_id, created_at);
