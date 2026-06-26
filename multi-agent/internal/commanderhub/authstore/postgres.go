package authstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yourorg/multi-agent/internal/identity"
)

// advisoryLockKeyCommanderLogins is the pg_advisory_xact_lock key namespace
// for the commander_logins cap.
//
// Registry: pick a random int64 unique to this table; document new entries
// here when other tables in this repo start using pg_advisory_xact_lock.
//
//	commander_logins → 8442987421341
const advisoryLockKeyCommanderLogins int64 = 8442987421341

// postgresStore is the production Store implementation. It uses a single
// shared *sql.DB (observerstore's pool) and serializes the cap check inside
// ReserveLogin via pg_advisory_xact_lock so concurrent /login requests
// across all pods cannot exceed MaxActiveLogins.
type postgresStore struct {
	db *sql.DB
}

// NewPostgresStore returns a Store backed by the given *sql.DB. Caller is
// responsible for having called MigratePostgres(db) once at startup (or via
// the helm migration-job for prod).
func NewPostgresStore(db *sql.DB) Store {
	return &postgresStore{db: db}
}

// skipCapConformance opts the postgresStore out of the slow MaxActiveLogins
// strict-cap conformance subtest — see the comment in
// conformance_test.go::ReserveLogin_capped_then_sweep_releases. Real-cap
// behavior under concurrency is covered by the k8s e2e (subcase 6).
func (s *postgresStore) skipCapConformance() {}

func (s *postgresStore) ReserveLogin(ctx context.Context, loginID string, now time.Time, ttl time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Rollback is a no-op after Commit; safe in all paths.
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock($1)`, advisoryLockKeyCommanderLogins); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM commander_logins WHERE expires_at < now()`); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `
        INSERT INTO commander_logins (login_id, expires_at)
        SELECT $1::text, $2::timestamptz
        WHERE (SELECT count(*) FROM commander_logins) < $3
    `, loginID, now.Add(ttl), MaxActiveLogins)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrCapped
	}
	return tx.Commit()
}

func (s *postgresStore) FinalizeReservedLogin(ctx context.Context, loginID string,
	deviceCode string, codeExpiresAt time.Time, intervalSeconds int) error {

	intervalSeconds = ClampIntervalSeconds(intervalSeconds)
	// next_poll_at honours the agentserver-derived interval from the very
	// first /poll. Without this, ServeLogin returns, the frontend polls
	// 1.5 s later, and we'd call agentserver again well below its
	// advertised throttle.
	//
	// expires_at > now() guard: a very slow RequestCode could leave the
	// reservation row past loginTTL by the time we get here. Finalizing it
	// anyway would hand the client a login_id whose first /poll immediately
	// expires (404). Refuse instead so ServeLogin's cleanup releases the slot.
	res, err := s.db.ExecContext(ctx, `
        UPDATE commander_logins
           SET device_code      = $1,
               code_expires_at  = $2,
               interval_seconds = $3::int,
               next_poll_at     = now() + ($3::int * interval '1 second')
         WHERE login_id    = $4
           AND device_code = ''
           AND expires_at  > now()
    `, deviceCode, codeExpiresAt, intervalSeconds, loginID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *postgresStore) DeleteLogin(ctx context.Context, loginID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM commander_logins WHERE login_id = $1`, loginID)
	return err
}

func (s *postgresStore) GetLogin(ctx context.Context, loginID string) (LoginRecord, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT login_id, device_code, code_expires_at, interval_seconds,
               next_poll_at, expires_at, session_id_hash, failure
          FROM commander_logins
         WHERE login_id = $1
    `, loginID)
	return scanLoginRecord(row)
}

func (s *postgresStore) SetPollThrottle(ctx context.Context, loginID string,
	intervalSeconds int, nextPollAt time.Time) error {

	intervalSeconds = ClampIntervalSeconds(intervalSeconds)
	_, err := s.db.ExecContext(ctx, `
        UPDATE commander_logins
           SET interval_seconds = $1,
               next_poll_at     = $2
         WHERE login_id = $3
    `, intervalSeconds, nextPollAt, loginID)
	return err
}

func (s *postgresStore) MarkLoginDone(ctx context.Context, loginID string, sess SessionRecord) error {
	hash := hashSID(sess.PlaintextSessionID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
        UPDATE commander_logins
           SET session_id_hash = $1, finalized_at = now()
         WHERE login_id = $2
           AND session_id_hash IS NULL
           AND failure IS NULL
           AND device_code <> ''
           AND expires_at > now()
    `, hash, loginID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO commander_sessions (
            session_id_hash, user_id, workspace_id, role, source, expires_at
        ) VALUES ($1, $2, $3, $4, $5, $6)
    `, hash,
		sess.Identity.UserID,
		sess.Identity.WorkspaceID,
		sess.Identity.Role,
		sess.Identity.Source,
		sess.ExpiresAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresStore) MarkLoginFailed(ctx context.Context, loginID string, sanitized Failure) error {
	if !ValidFailure(sanitized) {
		return ErrInvalidFailure
	}
	res, err := s.db.ExecContext(ctx, `
        UPDATE commander_logins
           SET failure      = $1,
               finalized_at = now()
         WHERE login_id = $2
           AND session_id_hash IS NULL
           AND failure IS NULL
           AND expires_at > now()
    `, string(sanitized), loginID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *postgresStore) ConsumeLogin(ctx context.Context, loginID string) (LoginRecord, error) {
	row := s.db.QueryRowContext(ctx, `
        DELETE FROM commander_logins
              WHERE login_id = $1
        RETURNING login_id, device_code, code_expires_at, interval_seconds,
                  next_poll_at, expires_at, session_id_hash, failure
    `, loginID)
	return scanLoginRecord(row)
}

func (s *postgresStore) GetSession(ctx context.Context, plaintext string) (SessionRecord, error) {
	hash := hashSID(plaintext)
	row := s.db.QueryRowContext(ctx, `
        SELECT user_id, workspace_id, role, source, expires_at
          FROM commander_sessions
         WHERE session_id_hash = $1
           AND expires_at > now()
    `, hash)

	var sess SessionRecord
	var userID, workspaceID, role, source string
	var expiresAt time.Time
	err := row.Scan(&userID, &workspaceID, &role, &source, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRecord{}, ErrNotFound
	}
	if err != nil {
		return SessionRecord{}, err
	}
	sess.Identity = identity.Identity{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Role:        role,
		Source:      source,
	}
	sess.ExpiresAt = expiresAt
	// PlaintextSessionID intentionally left zero: store side never re-emits it.
	return sess, nil
}

func (s *postgresStore) DeleteSession(ctx context.Context, plaintext string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM commander_sessions WHERE session_id_hash = $1`,
		hashSID(plaintext))
	return err
}

func (s *postgresStore) SweepExpired(ctx context.Context) (int64, int64, error) {
	resL, err := s.db.ExecContext(ctx,
		`DELETE FROM commander_logins WHERE expires_at < now()`)
	if err != nil {
		return 0, 0, err
	}
	logins, err := resL.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	resS, err := s.db.ExecContext(ctx,
		`DELETE FROM commander_sessions WHERE expires_at < now()`)
	if err != nil {
		return logins, 0, err
	}
	sessions, err := resS.RowsAffected()
	if err != nil {
		return logins, 0, err
	}
	return logins, sessions, nil
}

func scanLoginRecord(row interface{ Scan(...any) error }) (LoginRecord, error) {
	var rec LoginRecord
	var (
		codeExpiresAt sql.NullTime
		sidHash       sql.NullString
		failure       sql.NullString
	)
	err := row.Scan(
		&rec.LoginID,
		&rec.DeviceCode,
		&codeExpiresAt,
		&rec.IntervalSeconds,
		&rec.NextPollAt,
		&rec.ExpiresAt,
		&sidHash,
		&failure,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginRecord{}, ErrNotFound
	}
	if err != nil {
		return LoginRecord{}, err
	}
	if codeExpiresAt.Valid {
		rec.CodeExpiresAt = codeExpiresAt.Time
	}
	if sidHash.Valid {
		rec.SessionIDHash = sidHash.String
	}
	if failure.Valid {
		rec.Failure = Failure(failure.String)
	}
	return rec, nil
}
