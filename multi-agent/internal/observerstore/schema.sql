CREATE TABLE IF NOT EXISTS workspaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    workspace_id TEXT NOT NULL,
    id TEXT NOT NULL,
    role TEXT NOT NULL,
    display_name TEXT NOT NULL,
    token_hash TEXT NOT NULL,
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
