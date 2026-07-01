package observerweb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// newTestPGLimiter creates a sqlmock-backed pgTelemetryLimiter with
// QueryMatcherEqual so tests match the exact telemetryUpsertSQL constant.
func newTestPGLimiter(t *testing.T) (*pgTelemetryLimiter, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return newPGTelemetryLimiter(db, 60, 120), mock
}

var testKey = telemetryKey{
	WorkspaceID:    "ws-1",
	AgentID:        "agent-1",
	TelemetryKeyID: "key-1",
}

var testNow = time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

// TestPGTelemetryLimiter_SatisfiesInterface ensures *pgTelemetryLimiter
// implements telemetryAllower without importing extra packages.
func TestPGTelemetryLimiter_SatisfiesInterface(t *testing.T) {
	var _ telemetryAllower = newPGTelemetryLimiter(nil, 60, 120)
}

// TestPGTelemetryLimiter_AllowFirstCall_BucketCreated checks that the first
// call (INSERT path) allows the request and returns (true, nil).
func TestPGTelemetryLimiter_AllowFirstCall_BucketCreated(t *testing.T) {
	limiter, mock := newTestPGLimiter(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout = '100ms'`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// RETURNING tokens = burst - 1 = 119
	rows := sqlmock.NewRows([]string{"tokens"}).AddRow(119.0)
	mock.ExpectQuery(telemetryUpsertSQL).
		WithArgs(
			testKey.WorkspaceID, testKey.AgentID, testKey.TelemetryKeyID,
			float64(120), float64(60), testNow, testNow,
		).
		WillReturnRows(rows)
	mock.ExpectCommit()

	allowed, err := limiter.allow(context.Background(), testKey, testNow)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTelemetryLimiter_AllowSecondCall_BucketDecremented verifies a second
// call (UPDATE path) also allows and returns tokens decremented by 1.
func TestPGTelemetryLimiter_AllowSecondCall_BucketDecremented(t *testing.T) {
	limiter, mock := newTestPGLimiter(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout = '100ms'`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	rows := sqlmock.NewRows([]string{"tokens"}).AddRow(118.0)
	mock.ExpectQuery(telemetryUpsertSQL).
		WithArgs(
			testKey.WorkspaceID, testKey.AgentID, testKey.TelemetryKeyID,
			float64(120), float64(60), testNow, testNow,
		).
		WillReturnRows(rows)
	mock.ExpectCommit()

	allowed, err := limiter.allow(context.Background(), testKey, testNow)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTelemetryLimiter_BucketExhausted_Returns429False verifies that when
// the UPSERT WHERE clause blocks the update (0 rows returned, sql.ErrNoRows),
// allow returns (false, nil) so the handler responds with 429.
func TestPGTelemetryLimiter_BucketExhausted_Returns429False(t *testing.T) {
	limiter, mock := newTestPGLimiter(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout = '100ms'`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(telemetryUpsertSQL).
		WithArgs(
			testKey.WorkspaceID, testKey.AgentID, testKey.TelemetryKeyID,
			float64(120), float64(60), testNow, testNow,
		).
		WillReturnError(sql.ErrNoRows)
	// The ErrNoRows branch commits the no-op transaction. Deferred Rollback
	// is a no-op after Commit (sql package marks tx as done).
	mock.ExpectCommit()

	allowed, err := limiter.allow(context.Background(), testKey, testNow)
	require.NoError(t, err)
	require.False(t, allowed)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTelemetryLimiter_PGUnavailable_ReturnsErr verifies that a BeginTx
// failure propagates as (false, err) so the handler responds with 503.
func TestPGTelemetryLimiter_PGUnavailable_ReturnsErr(t *testing.T) {
	limiter, mock := newTestPGLimiter(t)

	dbErr := errors.New("connection refused")
	mock.ExpectBegin().WillReturnError(dbErr)

	allowed, err := limiter.allow(context.Background(), testKey, testNow)
	require.ErrorIs(t, err, dbErr)
	require.False(t, allowed)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTelemetryLimiter_LockTimeout_55P03_ReturnsErr verifies that a
// PostgreSQL lock_timeout error (code 55P03) is surfaced as (false, err)
// so the handler maps it to 503 (not silently dropped).
func TestPGTelemetryLimiter_LockTimeout_55P03_ReturnsErr(t *testing.T) {
	limiter, mock := newTestPGLimiter(t)

	lockErr := &pgconn.PgError{
		Code:    "55P03",
		Message: "canceling statement due to lock timeout",
	}
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout = '100ms'`).
		WillReturnError(lockErr)
	mock.ExpectRollback()

	allowed, err := limiter.allow(context.Background(), testKey, testNow)
	require.Error(t, err)
	require.True(t, isPGLockTimeout(err), "expected 55P03 lock timeout error, got: %v", err)
	require.False(t, allowed)
	require.NoError(t, mock.ExpectationsWereMet())
}
