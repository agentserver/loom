CREATE TABLE IF NOT EXISTS tasks (
    id           TEXT PRIMARY KEY,
    skill        TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    status       TEXT NOT NULL,
    output       TEXT,
    error        TEXT,
    created_at   TEXT NOT NULL,
    started_at   TEXT,
    finished_at  TEXT
);

CREATE TABLE IF NOT EXISTS task_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    ts          TEXT NOT NULL,
    type        TEXT NOT NULL,
    data        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_chunks_task ON task_chunks(task_id, id);

CREATE TABLE IF NOT EXISTS pending_acks (
    task_id      TEXT PRIMARY KEY REFERENCES tasks(id),
    status       TEXT NOT NULL,
    enqueued_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sub_tasks (
    parent_id     TEXT NOT NULL REFERENCES tasks(id),
    node_id       TEXT NOT NULL,
    target_id     TEXT NOT NULL,
    child_task_id TEXT,
    prompt        TEXT NOT NULL,
    depends_on    TEXT NOT NULL,
    status        TEXT NOT NULL,
    output        TEXT,
    error         TEXT,
    created_at    TEXT NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    PRIMARY KEY (parent_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_subtasks_parent ON sub_tasks(parent_id);
