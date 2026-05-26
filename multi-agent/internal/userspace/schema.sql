-- userspace tables. Namespace prefix: userspace_*.
-- Shares the SQLite file with observerstore but owns its own table family.

CREATE TABLE IF NOT EXISTS userspace_packages (
    slug         TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    tags_json    TEXT NOT NULL DEFAULT '[]',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_blobs (
    sha256       TEXT PRIMARY KEY,
    size_bytes   INTEGER NOT NULL,
    blob_path    TEXT NOT NULL,
    refcount     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_package_versions (
    slug                  TEXT NOT NULL REFERENCES userspace_packages(slug),
    version               TEXT NOT NULL,
    created_in_workspace  TEXT NOT NULL,
    created_by_agent_id   TEXT NOT NULL,
    manifest_json         TEXT NOT NULL,
    spec_json             TEXT,
    card_md               TEXT NOT NULL,
    tarball_sha256        TEXT NOT NULL,
    blob_sha256           TEXT NOT NULL REFERENCES userspace_blobs(sha256),
    status                TEXT NOT NULL DEFAULT 'ready',
    created_at            TEXT NOT NULL,
    PRIMARY KEY (slug, version)
);
CREATE INDEX IF NOT EXISTS idx_uspv_workspace ON userspace_package_versions(created_in_workspace);

CREATE TABLE IF NOT EXISTS userspace_workspace_installations (
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    slug                  TEXT NOT NULL REFERENCES userspace_packages(slug),
    installed_version     TEXT NOT NULL,
    installed_at          TEXT NOT NULL,
    installed_by_agent_id TEXT NOT NULL,
    PRIMARY KEY (workspace_id, slug),
    FOREIGN KEY (slug, installed_version) REFERENCES userspace_package_versions(slug, version)
);

CREATE VIRTUAL TABLE IF NOT EXISTS userspace_pkg_fts USING fts5(
    slug, description, card_md
);
