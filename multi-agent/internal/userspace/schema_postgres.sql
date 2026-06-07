CREATE TABLE IF NOT EXISTS userspace_packages (
  slug text PRIMARY KEY,
  kind text NOT NULL,
  description text NOT NULL DEFAULT '',
  tags_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_blobs (
  sha256 text PRIMARY KEY,
  size_bytes bigint NOT NULL,
  object_key text NOT NULL,
  refcount bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_package_versions (
  slug text NOT NULL REFERENCES userspace_packages(slug),
  version text NOT NULL,
  created_in_workspace text NOT NULL,
  created_by_agent_id text NOT NULL,
  manifest_json jsonb NOT NULL,
  spec_json jsonb,
  card_md text NOT NULL,
  tarball_sha256 text NOT NULL,
  blob_sha256 text NOT NULL REFERENCES userspace_blobs(sha256),
  status text NOT NULL DEFAULT 'ready',
  created_at timestamptz NOT NULL,
  PRIMARY KEY (slug, version)
);
CREATE INDEX IF NOT EXISTS idx_uspv_workspace ON userspace_package_versions(created_in_workspace);

CREATE TABLE IF NOT EXISTS userspace_workspace_installations (
  workspace_id text NOT NULL REFERENCES workspaces(id),
  slug text NOT NULL REFERENCES userspace_packages(slug),
  installed_version text NOT NULL,
  installed_at timestamptz NOT NULL,
  installed_by_agent_id text NOT NULL,
  PRIMARY KEY (workspace_id, slug),
  FOREIGN KEY (slug, installed_version) REFERENCES userspace_package_versions(slug, version)
);

CREATE INDEX IF NOT EXISTS idx_userspace_packages_search
ON userspace_packages USING gin (to_tsvector('simple', slug || ' ' || description || ' ' || tags_json::text));
