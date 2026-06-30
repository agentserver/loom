package commanderhub

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
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
