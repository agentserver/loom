package observerweb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// telemetryUpsertSQL is the atomic token-bucket UPSERT for the PG rate limiter.
// Parameters:
//
//	$1 = workspace_id  (string)
//	$2 = agent_id      (string)
//	$3 = telemetry_key_id (string)
//	$4 = burst         (float64)  — used as initial tokens (burst-1) on INSERT
//	$5 = per_minute    (float64)
//	$6 = now           (time.Time) — first occurrence: in VALUES and WHERE/SET
//	$7 = now           (time.Time) — second occurrence: required because pgx stdlib
//	                                  placeholders are positional; same value as $6
//
// The WHERE clause in ON CONFLICT DO UPDATE filters out exhausted buckets: if
// (refilled tokens) < 1, no UPDATE is emitted and RETURNING returns 0 rows,
// which the caller maps to (false, nil).
const telemetryUpsertSQL = `INSERT INTO commander_telemetry_buckets AS b
  (workspace_id, agent_id, telemetry_key_id, tokens, last_refilled, updated_at)
VALUES ($1, $2, $3, $4::double precision - 1, $6, $6)
ON CONFLICT (workspace_id, agent_id, telemetry_key_id) DO UPDATE
  SET tokens = LEAST(
                 b.tokens + (EXTRACT(EPOCH FROM ($7 - b.last_refilled)) / 60.0) * $5,
                 $4::double precision
               ) - 1,
      last_refilled = $7,
      updated_at    = $7
  WHERE LEAST(
          b.tokens + (EXTRACT(EPOCH FROM ($7 - b.last_refilled)) / 60.0) * $5,
          $4::double precision
        ) >= 1
RETURNING tokens`

// pgTelemetryLimiter implements telemetryAllower using an atomic UPSERT into
// commander_telemetry_buckets. Each call opens a short transaction with a
// lock_timeout so a stuck lock causes a fast 503 rather than a goroutine leak.
type pgTelemetryLimiter struct {
	db        *sql.DB
	perMinute int
	burst     int
}

func newPGTelemetryLimiter(db *sql.DB, perMinute, burst int) *pgTelemetryLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = perMinute
	}
	if burst < 1 {
		burst = 1
	}
	return &pgTelemetryLimiter{
		db:        db,
		perMinute: perMinute,
		burst:     burst,
	}
}

// SetPGTelemetryLimiter configures opts to use the Postgres-backed token-bucket
// rate limiter. Called by the observer-server wiring layer when
// store.driver == "postgres" and telemetry.enabled are both true.
// This keeps telemetryAllower (an unexported interface) out of the public API
// while still allowing external callers to plug the PG implementation.
func SetPGTelemetryLimiter(opts *Options, db *sql.DB, perMinute, burst int) {
	opts.TelemetryLimiter = newPGTelemetryLimiter(db, perMinute, burst)
}

// allow checks whether the given key is within its rate limit using an atomic
// Postgres UPSERT.
//
// Returns (true, nil) when the request is allowed.
// Returns (false, nil) when the bucket is exhausted (caller should 429).
// Returns (false, err) when the database is unavailable (caller should 503).
func (l *pgTelemetryLimiter) allow(ctx context.Context, key telemetryKey, now time.Time) (bool, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '100ms'`); err != nil {
		return false, err
	}

	var tokens float64
	err = tx.QueryRowContext(ctx, telemetryUpsertSQL,
		key.WorkspaceID,        // $1
		key.AgentID,            // $2
		key.TelemetryKeyID,     // $3
		float64(l.burst),       // $4
		float64(l.perMinute),   // $5
		now,                    // $6
		now,                    // $7
	).Scan(&tokens)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// WHERE clause blocked the UPDATE (bucket exhausted). Commit the no-op.
		return false, tx.Commit()
	case err != nil:
		return false, err
	}
	return true, tx.Commit()
}

// isPGLockTimeout returns true if err is a PostgreSQL lock_timeout error (55P03).
func isPGLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}
