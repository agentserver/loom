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
    external_user_id       TEXT NOT NULL DEFAULT '',
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
    external_sandbox_id    TEXT NOT NULL DEFAULT '',
    external_user_id       TEXT NOT NULL DEFAULT '',
    last_seen_at           TEXT NOT NULL DEFAULT '',
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

-- WT-1-run-schema: per-run D1 evaluation rows (24 columns matching
-- /root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md lines 256-279).
CREATE TABLE IF NOT EXISTS runs (
    run_id                    TEXT PRIMARY KEY,
    workload_id               TEXT NOT NULL,
    claim_id                  TEXT NOT NULL,
    experiment_id             TEXT NOT NULL,
    baseline_or_ablation      TEXT NOT NULL,
    loom_commit               TEXT NOT NULL,
    agentserver_commit        TEXT NOT NULL,
    modelserver_commit        TEXT NOT NULL,
    app_commit                TEXT NOT NULL,
    machine_topology          TEXT NOT NULL,
    context_ground_truth      TEXT NOT NULL,
    capability_snapshot_hash  TEXT NOT NULL DEFAULT '',
    task_contract_hash        TEXT NOT NULL DEFAULT '',
    dynamic_mcp_registry_hash TEXT NOT NULL DEFAULT '',
    selected_context          TEXT NOT NULL,
    ground_truth_context      TEXT NOT NULL,
    start_time                TEXT NOT NULL,
    end_time                  TEXT NOT NULL,
    success_oracle_result     TEXT NOT NULL CHECK(success_oracle_result IN ('pass','fail','timeout')),
    failure_category          TEXT NOT NULL DEFAULT '',
    human_intervention_count  INTEGER NOT NULL DEFAULT 0,
    artifact_hashes           TEXT NOT NULL DEFAULT '[]',
    observer_trace_path       TEXT NOT NULL DEFAULT '',
    model_trace_id            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_runs_experiment ON runs(experiment_id, workload_id);

-- WT-1-routing-trace: per-Dispatch decision trace.
-- Captures why one agent was selected over the others; consumed by
-- RoutingLatencyP50P95 (decision_duration_ns) and routing-correctness audits.
CREATE TABLE IF NOT EXISTS route_reasons (
    decision_id           TEXT PRIMARY KEY,
    conversation_id       TEXT NOT NULL,
    selected_agent_id     TEXT NOT NULL DEFAULT '',
    reason_code           TEXT NOT NULL,
    reason_text           TEXT NOT NULL DEFAULT '',
    candidates_json       TEXT NOT NULL DEFAULT '[]',
    decision_started_at   TEXT NOT NULL,
    decision_ended_at     TEXT NOT NULL,
    decision_duration_ns  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_route_reasons_conv
    ON route_reasons(conversation_id, decision_started_at);

-- WT-1-capability-snapshot: A1 capability snapshot persistence
-- (spec: docs/specs/wt1-capability-snapshot.spec.md §5.1). Two tables:
-- the content-addressed dedup store for the JSON blob, and an
-- insert-always attribution log so the eval runner can trace which
-- agents observed which capability set at which instant.
CREATE TABLE IF NOT EXISTS capability_snapshots (
  hash          TEXT PRIMARY KEY,
  snapshot_json TEXT NOT NULL,
  first_seen_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS capability_snapshot_usages (
  workspace_id TEXT NOT NULL,
  agent_id     TEXT NOT NULL,
  hash         TEXT NOT NULL,
  used_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_id, agent_id, hash, used_at)
);
CREATE INDEX IF NOT EXISTS idx_capability_snapshot_usages_agent
ON capability_snapshot_usages(workspace_id, agent_id, used_at);
CREATE INDEX IF NOT EXISTS idx_capability_snapshot_usages_hash
ON capability_snapshot_usages(hash);

-- WT-2-driver-promotion-chain B6: per-register/unregister/install audit row.
-- Populated by internal/promotionaudit.SQLiteWriter via the register /
-- unregister driver tools and the mcp-userspace install CLI. Reserved
-- columns `stage` / `stage_result` are filled by sub-B2 (acceptance
-- pipeline) which reuses this table for per-stage rows. See spec
-- docs/specs/wt2-driver-promotion-chain-B6.spec.md §3.2 and §7 for the
-- Security invariants; the CHECK constraints belt-and-suspenders the
-- Go-side promotionaudit.Validate enums.
CREATE TABLE IF NOT EXISTS promotion_audit (
    row_id                    TEXT PRIMARY KEY,
    ts                        TEXT NOT NULL,
    workspace_id              TEXT NOT NULL DEFAULT '',
    mcp_name                  TEXT NOT NULL,
    action                    TEXT NOT NULL CHECK(action IN ('register','unregister','install')),
    promoted_by_user_id       TEXT NOT NULL,
    driver_thread_id          TEXT NOT NULL,
    promotion_reason          TEXT NOT NULL CHECK(promotion_reason IN (
        'explicit_user_request','driver_agent_inferred','batch_import','ci_seed'
    )),
    candidate_source_task_id  TEXT NOT NULL,
    registry_hash_after       TEXT NOT NULL DEFAULT '',
    stage                     TEXT NOT NULL DEFAULT '',
    stage_result              TEXT NOT NULL DEFAULT '',
    stage_note                TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_promotion_audit_mcp
    ON promotion_audit(mcp_name, ts);
CREATE INDEX IF NOT EXISTS idx_promotion_audit_user
    ON promotion_audit(promoted_by_user_id, ts);
CREATE INDEX IF NOT EXISTS idx_promotion_audit_thread
    ON promotion_audit(driver_thread_id, ts);

-- WT-2-driver-promotion-chain B4: one row per non-ablated
-- driver.Lookup call, keyed by run_id so D2 can aggregate
-- RegistryLookupHitRate under Phase 3 parallel runs unambiguously.
-- Stores only query_hash_prefix (8-hex) — not the raw user intent
-- text — because the sanitizer can't strip alphanumeric
-- secret-shaped material. See spec
-- docs/specs/wt2-driver-promotion-chain-B4.spec.md §5.
CREATE TABLE IF NOT EXISTS registry_lookup_samples (
    row_id             TEXT PRIMARY KEY,
    ts                 TEXT NOT NULL,
    run_id             TEXT NOT NULL DEFAULT '',
    workspace_id       TEXT NOT NULL DEFAULT '',
    query_hash_prefix  TEXT NOT NULL DEFAULT '',
    hit_count          INTEGER NOT NULL DEFAULT 0,
    registry_hits      INTEGER NOT NULL DEFAULT 0,
    userspace_hits     INTEGER NOT NULL DEFAULT 0,
    top_score          REAL NOT NULL DEFAULT 0.0
);
CREATE INDEX IF NOT EXISTS idx_registry_lookup_samples_run
    ON registry_lookup_samples(run_id, ts);

-- WT-2-driver-promotion-chain B1: promote-candidate surfacing rows.
-- Populated by driver.SurfacePromoteCandidate; consumed by
-- PromotionCandidateSurfacingRate / PromotionAdoptionRate /
-- TimeFromUserDecisionToRegisteredMCP metrics. Keyed by
-- UNIQUE(run_id, candidate_id) so parallel Phase 3 runs stay
-- unambiguous; the JOIN to promotion_audit uses candidate_id (which
-- itself embeds run_id — see spec §4.1). See spec
-- docs/specs/wt2-driver-promotion-chain-B1.spec.md §2.
CREATE TABLE IF NOT EXISTS promote_candidates (
    row_id            TEXT PRIMARY KEY,
    candidate_id      TEXT NOT NULL,
    family            TEXT NOT NULL,
    source_task_ids   TEXT NOT NULL DEFAULT '[]',
    surfaced_at       TEXT NOT NULL,
    decision          TEXT NOT NULL DEFAULT '' CHECK(decision IN ('','promoted','declined','expired')),
    decision_at       TEXT NOT NULL DEFAULT '',
    surfaced_by       TEXT NOT NULL DEFAULT '' CHECK(surfaced_by IN ('','user_hint','driver_inferred','similarity_signal')),
    workspace_id      TEXT NOT NULL DEFAULT '',
    run_id            TEXT NOT NULL DEFAULT '',
    UNIQUE(run_id, candidate_id)
);
CREATE INDEX IF NOT EXISTS idx_promote_candidates_family
    ON promote_candidates(family, surfaced_at);
CREATE INDEX IF NOT EXISTS idx_promote_candidates_candidate
    ON promote_candidates(candidate_id);
CREATE INDEX IF NOT EXISTS idx_promote_candidates_run
    ON promote_candidates(run_id, surfaced_at);
