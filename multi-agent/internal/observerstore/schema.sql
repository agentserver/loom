CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,
    key_hash    TEXT NOT NULL UNIQUE,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS telemetry_api_keys (
    id           TEXT PRIMARY KEY,
    key_hash     TEXT NOT NULL UNIQUE,
    note         TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL DEFAULT '*',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL DEFAULT '',
    created_by_api_key_id  TEXT NOT NULL REFERENCES api_keys(id),
    created_at             TEXT NOT NULL,
    last_seen_at           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    workspace_id           TEXT NOT NULL,
    id                     TEXT NOT NULL,
    role                   TEXT NOT NULL,
    display_name           TEXT NOT NULL,
    token_hash             TEXT NOT NULL,
    created_by_api_key_id  TEXT NOT NULL REFERENCES api_keys(id),
    PRIMARY KEY (workspace_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_token_hash ON agents(token_hash);

CREATE TABLE IF NOT EXISTS events (
    event_id TEXT PRIMARY KEY,
    ts TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    agent_role TEXT NOT NULL,
    type TEXT NOT NULL,
    task_id TEXT NOT NULL,
    parent_task_id TEXT,
    subtask_id TEXT,
    child_task_id TEXT,
    summary TEXT,
    subtask_summary TEXT,
    status TEXT,
    target_agent_id TEXT,
    target_role TEXT,
    mcp_server_name TEXT,
    mcp_tools TEXT,
    payload TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    workspace_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    driver_agent_id TEXT,
    master_agent_id TEXT,
    slave_agent_id TEXT,
    summary TEXT NOT NULL,
    status TEXT NOT NULL,
    has_mcp INTEGER NOT NULL DEFAULT 0,
    mcp_status TEXT NOT NULL DEFAULT '',
    latest_progress TEXT NOT NULL DEFAULT '',
    latest_progress_phase TEXT NOT NULL DEFAULT '',
    latest_progress_at TEXT NOT NULL DEFAULT '',
    final_output TEXT NOT NULL DEFAULT '',
    is_final INTEGER NOT NULL DEFAULT 0,
    output TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, task_id)
);

CREATE TABLE IF NOT EXISTS subtasks (
    workspace_id TEXT NOT NULL,
    parent_task_id TEXT NOT NULL,
    subtask_id TEXT NOT NULL,
    child_task_id TEXT,
    master_agent_id TEXT,
    slave_agent_id TEXT,
    summary TEXT NOT NULL,
    display_label TEXT NOT NULL,
    status TEXT NOT NULL,
    mcp_status TEXT NOT NULL DEFAULT '',
    latest_progress TEXT NOT NULL DEFAULT '',
    latest_progress_phase TEXT NOT NULL DEFAULT '',
    latest_progress_at TEXT NOT NULL DEFAULT '',
    output TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, parent_task_id, subtask_id)
);

CREATE TABLE IF NOT EXISTS mcp_servers (
    workspace_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    parent_task_id TEXT,
    slave_agent_id TEXT NOT NULL,
    name TEXT NOT NULL,
    tools TEXT NOT NULL,
    tool_descriptors TEXT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, task_id, name)
);

CREATE TABLE IF NOT EXISTS artifacts (
    workspace_id TEXT NOT NULL,
    id TEXT NOT NULL,
    owner_agent_id TEXT NOT NULL,
    path TEXT NOT NULL,
    kind TEXT NOT NULL,
    mime TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    object_key TEXT NOT NULL DEFAULT '',
    content BLOB,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, id)
);

CREATE TABLE IF NOT EXISTS artifact_requests (
    workspace_id TEXT NOT NULL,
    id TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    requester_agent_id TEXT NOT NULL,
    owner_agent_id TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_artifact_requests_owner_state ON artifact_requests(workspace_id, owner_agent_id, state);

CREATE TABLE IF NOT EXISTS writes (
    workspace_id TEXT NOT NULL,
    id TEXT NOT NULL,
    owner_agent_id TEXT NOT NULL,
    writer_agent_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL,
    path TEXT NOT NULL,
    overwrite INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    mime TEXT NOT NULL DEFAULT '',
    bytes INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    object_key TEXT NOT NULL DEFAULT '',
    content BLOB,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_writes_owner_task_state ON writes(workspace_id, owner_agent_id, task_id, state);

CREATE TABLE IF NOT EXISTS task_contracts (
  workspace_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
);

CREATE INDEX IF NOT EXISTS idx_task_contracts_conversation
ON task_contracts(workspace_id, conversation_id, updated_at);

CREATE TABLE IF NOT EXISTS resource_snapshots (
  workspace_id TEXT NOT NULL,
  snapshot_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, snapshot_id)
);

CREATE INDEX IF NOT EXISTS idx_resource_snapshots_latest
ON resource_snapshots(workspace_id, created_at);
