-- commander state persistence schema.
--
-- Two tables: commander_logins (in-flight + terminal device-flow login state)
-- and commander_sessions (cookie-bound identity, hash-keyed).
--
-- WHEN ADDING TO failure.go's Failure const block, ALSO ADD HERE to the
-- commander_logins_failure_enum CHECK. Mismatch = INSERT failure on legitimate
-- enum values. Reverse mismatch = store silently accepts a stale enum,
-- defeating the security guard. TestFailureEnumMatchesSchema in
-- migrate_test.go enforces this without requiring a live Postgres.

CREATE TABLE IF NOT EXISTS commander_logins (
    login_id          text        PRIMARY KEY,
    device_code       text        NOT NULL DEFAULT '',
    code_expires_at   timestamptz,
    interval_seconds  integer     NOT NULL DEFAULT 5,
    next_poll_at      timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    session_id_hash   text,
    failure           text,
    finalized_at      timestamptz,

    CONSTRAINT commander_logins_terminal_xor CHECK (
        (session_id_hash IS NULL OR failure IS NULL)
    ),
    CONSTRAINT commander_logins_finalized_iff_terminal CHECK (
        (finalized_at IS NULL) =
        (session_id_hash IS NULL AND failure IS NULL)
    ),
    CONSTRAINT commander_logins_failure_len CHECK (
        failure IS NULL OR length(failure) <= 256
    ),
    CONSTRAINT commander_logins_failure_enum CHECK (
        failure IS NULL OR failure IN (
            'authorization denied',
            'authorization expired',
            'upstream timeout',
            'id token invalid',
            'device flow error',
            'store unavailable'
        )
    ),
    CONSTRAINT commander_logins_login_id_nonempty CHECK (length(login_id) > 0),
    CONSTRAINT commander_logins_code_expires_iff_devcode CHECK (
        (device_code = '' AND code_expires_at IS NULL)
        OR
        (device_code <> '' AND code_expires_at IS NOT NULL)
    ),
    CONSTRAINT commander_logins_interval_positive CHECK (interval_seconds > 0)
);

CREATE INDEX IF NOT EXISTS commander_logins_expires_idx
    ON commander_logins (expires_at);

CREATE TABLE IF NOT EXISTS commander_sessions (
    session_id_hash text        PRIMARY KEY,
    user_id         text        NOT NULL,
    workspace_id    text        NOT NULL,
    role            text        NOT NULL DEFAULT '',
    source          text        NOT NULL,
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT commander_sessions_user_id_nonempty     CHECK (length(user_id) > 0),
    CONSTRAINT commander_sessions_workspace_id_nonempty CHECK (length(workspace_id) > 0),
    CONSTRAINT commander_sessions_source_nonempty       CHECK (length(source) > 0)
);

CREATE INDEX IF NOT EXISTS commander_sessions_expires_idx
    ON commander_sessions (expires_at);

CREATE TABLE IF NOT EXISTS commander_daemons (
    user_id              text        NOT NULL,
    workspace_id         text        NOT NULL,
    short_id             text        NOT NULL,
    connection_id        text        NOT NULL,
    display_name         text        NOT NULL DEFAULT '',
    kind                 text        NOT NULL DEFAULT '',
    driver_version       text        NOT NULL DEFAULT '',
    capabilities         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    owning_instance_url  text        NOT NULL,
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, workspace_id, short_id),
    CONSTRAINT commander_daemons_user_id_nonempty       CHECK (length(user_id) > 0),
    CONSTRAINT commander_daemons_workspace_id_nonempty  CHECK (length(workspace_id) > 0),
    CONSTRAINT commander_daemons_short_id_nonempty      CHECK (length(short_id) > 0),
    CONSTRAINT commander_daemons_conn_id_nonempty       CHECK (length(connection_id) > 0),
    CONSTRAINT commander_daemons_owning_url_nonempty    CHECK (length(owning_instance_url) > 0)
);
CREATE INDEX IF NOT EXISTS commander_daemons_owner_idx
    ON commander_daemons (user_id, workspace_id);
CREATE INDEX IF NOT EXISTS commander_daemons_last_seen_idx
    ON commander_daemons (last_seen_at);

CREATE TABLE IF NOT EXISTS commander_turns (
    user_id            text        NOT NULL,
    workspace_id       text        NOT NULL,
    short_id           text        NOT NULL,
    session_id         text        NOT NULL,
    state              text        NOT NULL,
    awaiting_approval  boolean     NOT NULL DEFAULT false,
    active_worker      boolean     NOT NULL DEFAULT false,
    message            text        NOT NULL DEFAULT '',
    updated_at         timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, workspace_id, short_id, session_id),
    CONSTRAINT commander_turns_state_enum CHECK (
        state IN ('idle','queued','answering','awaiting_approval','done','error','disconnected')
    )
);
CREATE INDEX IF NOT EXISTS commander_turns_owner_idx
    ON commander_turns (user_id, workspace_id, short_id);
CREATE INDEX IF NOT EXISTS commander_turns_updated_idx
    ON commander_turns (updated_at);

CREATE TABLE IF NOT EXISTS commander_forward_nonces (
    nonce       text        PRIMARY KEY,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS commander_forward_nonces_received_idx
    ON commander_forward_nonces (received_at);

CREATE TABLE IF NOT EXISTS commander_telemetry_buckets (
    workspace_id      text             NOT NULL,
    agent_id          text             NOT NULL,
    telemetry_key_id  text             NOT NULL,
    tokens            double precision NOT NULL,
    last_refilled     timestamptz      NOT NULL DEFAULT now(),
    updated_at        timestamptz      NOT NULL DEFAULT now(),

    PRIMARY KEY (workspace_id, agent_id, telemetry_key_id)
);
CREATE INDEX IF NOT EXISTS commander_telemetry_buckets_updated_idx
    ON commander_telemetry_buckets (updated_at);
