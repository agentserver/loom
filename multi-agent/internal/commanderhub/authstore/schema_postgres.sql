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
