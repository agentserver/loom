package commanderhub

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

func TestSharedRegistry_ConnectUpsertSQL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id:            "conn-1",
		shortID:       "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac",
		kind:          "claude",
		driverVersion: "0.0.10",
	}

	mock.ExpectExec(connectUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.connectUpsert(context.Background(), dc))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_HeartbeatStillOwn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id: "conn-1", shortID: "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac", kind: "claude", driverVersion: "0.0.10",
	}

	// 9 args: user, workspace, short_id, conn_id, display, kind, driver, caps_json, owning_url
	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 1))

	stillOwn, err := s.heartbeatUpsert(context.Background(), dc)
	require.NoError(t, err)
	require.True(t, stillOwn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_HeartbeatOwnershipLost(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id: "conn-1", shortID: "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac", kind: "claude", driverVersion: "0.0.10",
	}

	// 0 rows affected => sibling owns the row (ownership-guarded WHERE blocked SET).
	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 0))

	stillOwn, err := s.heartbeatUpsert(context.Background(), dc)
	require.NoError(t, err)
	require.False(t, stillOwn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_RemoveGuardsConnectionID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	mock.ExpectExec(removeSQL).
		WithArgs("alice", "W1", "agent-A", "http://10.0.0.42:8091", "conn-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.remove(context.Background(), o, "agent-A", "conn-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_LookupRemoteSkipsSelfOwned(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	// Row exists, owned by THIS pod => ok=false (no peer URL).
	rows := sqlmock.NewRows([]string{"owning_instance_url", "short_id", "display_name", "kind", "driver_version", "capabilities", "last_seen_at"}).
		AddRow("http://10.0.0.42:8091", "agent-A", "alice-mac", "claude", "0.0.10", `[]`, time.Now())
	mock.ExpectQuery(lookupRemoteSQL).
		WithArgs("alice", "W1", "agent-A", sqlmock.AnyArg()).
		WillReturnRows(rows)

	_, _, ok, err := s.lookupRemote(context.Background(), o, "agent-A")
	require.NoError(t, err)
	require.False(t, ok, "self-owned row must not be returned as remote")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_LookupRemotePeerOwned(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	rows := sqlmock.NewRows([]string{"owning_instance_url", "short_id", "display_name", "kind", "driver_version", "capabilities", "last_seen_at"}).
		AddRow("http://10.0.1.99:8091", "agent-A", "alice-mac", "claude", "0.0.10", `["sessions","turn"]`, time.Now())
	mock.ExpectQuery(lookupRemoteSQL).
		WithArgs("alice", "W1", "agent-A", sqlmock.AnyArg()).
		WillReturnRows(rows)

	peer, info, ok, err := s.lookupRemote(context.Background(), o, "agent-A")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "http://10.0.1.99:8091", peer)
	require.Equal(t, "agent-A", info.DaemonID)
	require.Equal(t, "alice-mac", info.DisplayName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_ListAllFreshOnly(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	rows := sqlmock.NewRows([]string{"short_id", "display_name", "kind", "driver_version", "capabilities", "last_seen_at", "owning_instance_url"}).
		AddRow("agent-A", "alice-mac", "claude", "0.0.10", `["sessions"]`, time.Now(), "http://10.0.0.42:8091").
		AddRow("agent-B", "alice-laptop", "codex", "0.0.10", `["sessions"]`, time.Now(), "http://10.0.1.99:8091")
	mock.ExpectQuery(listAllSQL).
		WithArgs("alice", "W1", sqlmock.AnyArg()).
		WillReturnRows(rows)

	got, err := s.listAll(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "agent-A", got[0].DaemonID)
	require.Equal(t, "agent-B", got[1].DaemonID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// To avoid timer-based race conditions, the production runHeartbeat is
// factored to expose runHeartbeatOnce(ctx, dc) which executes EXACTLY
// one tick body. Tests call it directly; runHeartbeat is just the for-
// loop wrapper.

func TestSharedRegistry_HeartbeatOnce_StillOwn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id: "conn-1", shortID: "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac", kind: "claude", driverVersion: "0.0.10",
	}

	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 1))

	keepRunning := s.runHeartbeatOnce(context.Background(), dc)
	require.True(t, keepRunning, "stillOwn should let the loop continue")
	require.False(t, dc.ownershipLost.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_HeartbeatOnce_ForceClosesOnOwnershipLoss(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := newOwnershipTestDaemonConn(t, "conn-1", "agent-A", owner{userID: "alice", workspaceID: "W1"})

	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 0))

	keepRunning := s.runHeartbeatOnce(context.Background(), dc)
	require.False(t, keepRunning, "ownership loss must signal stop")
	require.True(t, dc.ownershipLost.Load(), "ownershipLost must be sticky-true")
	require.True(t, ownershipTestConnIsClosed(dc), "WS conn must be force-closed on ownership loss")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Sweep tests use sqlmock + the runSweepOnce helper (NO timer flakes).

func TestSharedRegistry_Sweep_DeletesStale(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")

	mock.ExpectExec(sweepDaemonsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))

	err = s.sweep(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_SweepNonces_DeletesStale(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")

	mock.ExpectExec(sweepNoncesSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))

	err = s.sweepNonces(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_SweepTelemetryBuckets_DeletesStale(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")

	mock.ExpectExec(sweepTelemetryBucketsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))

	err = s.sweepTelemetryBuckets(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_SweepOnce_CallsAllThree(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")

	// Expect all three sweep SQL statements in order
	mock.ExpectExec(sweepDaemonsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(sweepNoncesSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(sweepTelemetryBucketsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))

	s.runSweepOnce(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_SweepOnce_ContinuesOnError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")

	// First sweep fails, but subsequent sweeps should still execute
	mock.ExpectExec(sweepDaemonsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectExec(sweepNoncesSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(sweepTelemetryBucketsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))

	s.runSweepOnce(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSharedRegistry_ConfiguredTimingReachesGoroutines verifies that timing
// values passed via SharedRegistryConfig are applied to the sharedRegistry fields
// and thereby used by the heartbeat and sweep goroutines. This is the Finding-6
// fix: previously config values were parsed but never propagated.
func TestSharedRegistry_ConfiguredTimingReachesGoroutines(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	cfg := SharedRegistryConfig{
		HeartbeatEvery: 7 * time.Second,
		SweepEvery:     13 * time.Second,
		OnlineTTL:      25 * time.Second,
		DeleteAfter:    2 * time.Minute,
		NonceTTL:       90 * time.Second,
	}
	sr := newSharedRegistryWithConfig(db, "http://10.0.0.42:8091", cfg)

	require.Equal(t, 7*time.Second, sr.heartbeatEvery, "heartbeatEvery must use configured value")
	require.Equal(t, 13*time.Second, sr.sweepEvery, "sweepEvery must use configured value")
	require.Equal(t, 25*time.Second, sr.onlineTTL, "onlineTTL must use configured value")
	require.Equal(t, 2*time.Minute, sr.deleteAfter, "deleteAfter must use configured value")
	require.Equal(t, 90*time.Second, sr.nonceTTL, "nonceTTL must use configured value")
}

// TestSharedRegistry_ZeroConfigFallsBackToDefaults ensures that zero-valued config
// fields leave the package defaults intact.
func TestSharedRegistry_ZeroConfigFallsBackToDefaults(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := newSharedRegistryWithConfig(db, "http://10.0.0.42:8091", SharedRegistryConfig{})

	require.Equal(t, defaultHeartbeatEvery, sr.heartbeatEvery, "zero config must keep default heartbeat")
	require.Equal(t, defaultSweepEvery, sr.sweepEvery, "zero config must keep default sweep")
	require.Equal(t, defaultOnlineTTL, sr.onlineTTL, "zero config must keep default onlineTTL")
	require.Equal(t, defaultDeleteAfter, sr.deleteAfter, "zero config must keep default deleteAfter")
	require.Equal(t, defaultNonceTTL, sr.nonceTTL, "zero config must keep default nonceTTL")
}

// fakeTurnStore is a minimal turnStateBackend used only in sweep tests.
// It records cleanupOrphans calls and returns a preconfigured error.
type fakeTurnStore struct {
	cleanupCalls int
	cleanupArg   time.Duration
	cleanupErr   error
}

func (f *fakeTurnStore) begin(_ context.Context, _ turnKey) (bool, error)                          { return false, nil }
func (f *fakeTurnStore) set(_ context.Context, _ turnKey, _ turnState) error                       { return nil }
func (f *fakeTurnStore) finish(_ context.Context, _ turnKey, _ turnState) error                    { return nil }
func (f *fakeTurnStore) fail(_ context.Context, _ turnKey, _ string) error                         { return nil }
func (f *fakeTurnStore) rekey(_ context.Context, _, _ turnKey) error                               { return nil }
func (f *fakeTurnStore) get(_ context.Context, _ turnKey) (turnSnapshot, error)                    { return turnSnapshot{}, nil }
func (f *fakeTurnStore) updateFromEnvelope(_ context.Context, _ turnKey, _ string, _ commander.Envelope) error { return nil }
func (f *fakeTurnStore) cleanupOrphans(_ context.Context, older time.Duration) error {
	f.cleanupCalls++
	f.cleanupArg = older
	return f.cleanupErr
}

// TestRunSweepOnce_CallsCleanupOrphans verifies that runSweepOnce invokes
// cleanupOrphans on each tick when a turns backend is attached, and that a
// transient error from cleanupOrphans does not abort the sweep cycle
// (i.e. the turn error counter increments but no panic/early-return occurs).
// This exercises the final-fix1 finding-3 fix.
func TestRunSweepOnce_CallsCleanupOrphans(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	sr := newSharedRegistry(db, "http://10.0.0.42:8091")

	fts := &fakeTurnStore{}
	const wantTimeout = 10 * time.Minute
	sr.attachTurns(fts, wantTimeout)

	// Expect all three SQL sweeps to proceed (cleanupOrphans uses the fake, not SQL).
	mock.ExpectExec(sweepDaemonsSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sweepNoncesSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sweepTelemetryBucketsSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))

	sr.runSweepOnce(context.Background())

	require.Equal(t, 1, fts.cleanupCalls, "cleanupOrphans must be called once per sweep tick")
	require.Equal(t, wantTimeout, fts.cleanupArg, "cleanupOrphans must receive hub.TurnTimeout")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRunSweepOnce_CleanupOrphansErrorDoesNotAbort verifies that a transient
// error from cleanupOrphans does not prevent the three SQL sweeps from running.
func TestRunSweepOnce_CleanupOrphansErrorDoesNotAbort(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	sr := newSharedRegistry(db, "http://10.0.0.42:8091")

	fts := &fakeTurnStore{cleanupErr: sql.ErrConnDone}
	sr.attachTurns(fts, 10*time.Minute)

	mock.ExpectExec(sweepDaemonsSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sweepNoncesSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sweepTelemetryBucketsSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))

	// Must not panic or early-return despite cleanupOrphans returning an error.
	sr.runSweepOnce(context.Background())

	require.Equal(t, 1, fts.cleanupCalls, "cleanupOrphans must still be called despite prior sweep errors")
	require.Equal(t, int32(1), sr.sweepTurnsErrCount, "sweepTurnsErrCount must be incremented on error")
	require.NoError(t, mock.ExpectationsWereMet())
}
