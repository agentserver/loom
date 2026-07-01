package commanderhub

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

func TestRegistry_AddLookupRemove(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// shortID is empty → routingID() falls back to dc.id ("d1").
	dc := &daemonConn{id: "d1", shortID: "", owner: o, displayName: "mac", kind: "claude", driverVersion: "v1"}

	r.add(dc)

	got, ok := r.lookup(o, "d1")
	require.True(t, ok)
	require.Equal(t, "mac", got.displayName)

	// 跨用户不可见
	_, ok = r.lookup(owner{userID: "bob", workspaceID: "W1"}, "d1")
	require.False(t, ok)

	// 同用户跨 workspace 不可见
	_, ok = r.lookup(owner{userID: "alice", workspaceID: "W2"}, "d1")
	require.False(t, ok)
}

func TestRegistry_DaemonsSnapshot(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// shortID empty → routingID() == dc.id → keyed as "d1", "d2"
	r.add(&daemonConn{id: "d1", shortID: "", owner: o, displayName: "mac", kind: "claude", driverVersion: "v1"})
	r.add(&daemonConn{id: "d2", shortID: "", owner: o, displayName: "linux", kind: "codex", driverVersion: "v2"})

	infos := r.daemons(o)
	require.Len(t, infos, 2)
	got := map[string]DaemonInfo{}
	for _, di := range infos {
		got[di.DaemonID] = di
	}
	require.Equal(t, "claude", got["d1"].Kind)
	require.Equal(t, "codex", got["d2"].Kind)

	// 别人的 owner 快照为空
	require.Empty(t, r.daemons(owner{userID: "bob", workspaceID: "W1"}))
}

func TestRegistryDaemonInfoIncludesCapabilities(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	r.add(&daemonConn{
		id:           "d1",
		shortID:      "d1",
		owner:        o,
		displayName:  "prod-codex",
		kind:         "codex",
		capabilities: map[string]bool{commander.CapabilityFiles: true},
	})

	got := r.daemons(o)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Capabilities, commander.CapabilityFiles)
}

func TestRegistry_RemoveCleansEmptyOwner(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// shortID empty → routingID() == "d1"
	r.add(&daemonConn{id: "d1", shortID: "", owner: o})
	r.remove(o, "d1")

	_, ok := r.lookup(o, "d1")
	require.False(t, ok)
	require.Empty(t, r.daemons(o))
}

// TestDaemonConn_ConfirmOwnership_SinglePodReturnsTrue verifies that when
// sharedReg is nil OR hub is nil, confirmOwnership returns true without
// touching PG.
func TestDaemonConn_ConfirmOwnership_SinglePodReturnsTrue(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	// Test 1: hub is nil (single-pod mode, no shared registry)
	dc := &daemonConn{
		id:      "conn-1",
		owner:   o,
		shortID: "daemon-1",
		hub:     nil,
	}
	result := dc.confirmOwnership(context.Background())
	require.True(t, result, "single-pod mode (hub=nil) should return true")

	// Test 2: hub is not nil but sharedReg is nil (single-pod mode with hub)
	hub := &Hub{}
	dc2 := &daemonConn{
		id:      "conn-2",
		owner:   o,
		shortID: "daemon-2",
		hub:     hub,
		// hub.sharedReg is nil by default
	}
	result = dc2.confirmOwnership(context.Background())
	require.True(t, result, "single-pod mode (sharedReg=nil) should return true")
}

// TestDaemonConn_ConfirmOwnership_SharedPodOwns verifies that confirmOwnership
// returns true when the row matches the current pod and connection.
func TestDaemonConn_ConfirmOwnership_SharedPodOwns(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// Expect the query to return the current pod and connection.
	mock.ExpectQuery(`SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = \$1 AND workspace_id = \$2 AND short_id = \$3`).
		WithArgs("alice", "W1", "daemon-1").
		WillReturnRows(sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}).
			AddRow("pod-1.example.com", "conn-abc"))

	result := dc.confirmOwnership(context.Background())
	require.True(t, result, "confirmOwnership should return true when ownership is still ours")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDaemonConn_ConfirmOwnership_SharedPodLostOwnership verifies that
// confirmOwnership returns false and sets ownershipLost when the row is owned
// by a different pod.
func TestDaemonConn_ConfirmOwnership_SharedPodLostOwnership(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// Expect the query to return a different pod URL.
	mock.ExpectQuery(`SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = \$1 AND workspace_id = \$2 AND short_id = \$3`).
		WithArgs("alice", "W1", "daemon-1").
		WillReturnRows(sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}).
			AddRow("pod-2.example.com", "conn-xyz"))

	result := dc.confirmOwnership(context.Background())
	require.False(t, result, "confirmOwnership should return false when ownership is lost")
	require.True(t, dc.ownershipLost.Load(), "ownershipLost flag should be set")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDaemonConn_ConfirmOwnership_SharedPodDifferentConnection verifies that
// confirmOwnership returns false and sets ownershipLost when the connection_id
// differs (same pod, different connection).
func TestDaemonConn_ConfirmOwnership_SharedPodDifferentConnection(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// Expect the query to return the same pod but a different connection.
	mock.ExpectQuery(`SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = \$1 AND workspace_id = \$2 AND short_id = \$3`).
		WithArgs("alice", "W1", "daemon-1").
		WillReturnRows(sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}).
			AddRow("pod-1.example.com", "conn-xyz"))

	result := dc.confirmOwnership(context.Background())
	require.False(t, result, "confirmOwnership should return false when connection_id differs")
	require.True(t, dc.ownershipLost.Load(), "ownershipLost flag should be set")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDaemonConn_ConfirmOwnership_SharedPodRowDeleted verifies that
// confirmOwnership returns false when the row is missing (sql.ErrNoRows)
// but does NOT sticky-set ownershipLost (codex Phase-B r3 MAJOR #1).
// The heartbeat goroutine's self-heal UPSERT re-inserts the row on its
// next tick; sticky-poisoning here would brick a daemon the cluster
// considers healthy.
func TestDaemonConn_ConfirmOwnership_SharedPodRowDeleted(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// Expect the query to return no rows (row was deleted). Empty
	// sqlmock.NewRows makes Scan return sql.ErrNoRows — the definitive
	// "row absent" signal that DOES sticky-set ownershipLost.
	mock.ExpectQuery(`SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = \$1 AND workspace_id = \$2 AND short_id = \$3`).
		WithArgs("alice", "W1", "daemon-1").
		WillReturnRows(sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}))

	result := dc.confirmOwnership(context.Background())
	require.False(t, result, "confirmOwnership should return false when row is missing")
	require.False(t, dc.ownershipLost.Load(), "row-missing must NOT sticky-set ownershipLost; heartbeat self-heal reclaims it")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDaemonConn_ConfirmOwnership_TransientPGErrorDoesNotPoison verifies
// that a transient PG error (caller ctx cancel, query timeout, PG
// unreachable) returns false for this call but does NOT sticky-set
// ownershipLost — otherwise a single cancelled HTTP request would brick
// the WS for the rest of its life (codex Phase-B r2 MAJOR #1).
func TestDaemonConn_ConfirmOwnership_TransientPGErrorDoesNotPoison(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// Expect the query to fail with a transient PG error.
	mock.ExpectQuery(`SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = \$1 AND workspace_id = \$2 AND short_id = \$3`).
		WithArgs("alice", "W1", "daemon-1").
		WillReturnError(context.DeadlineExceeded)

	result := dc.confirmOwnership(context.Background())
	require.False(t, result, "confirmOwnership returns false on transient PG error")
	require.False(t, dc.ownershipLost.Load(), "transient PG error must NOT sticky-set ownershipLost")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDaemonConn_ConfirmOwnership_CallerCancelDoesNotPoison verifies
// that a caller ctx cancel does NOT sticky-set ownershipLost. The next
// call should be able to re-query and succeed.
func TestDaemonConn_ConfirmOwnership_CallerCancelDoesNotPoison(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// First call: caller ctx already cancelled. database/sql short-circuits
	// at QueryRowContext entry when ctx is cancelled and never reaches the
	// driver — sqlmock sees no query. confirmOwnership's Scan returns
	// context.Canceled.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, dc.confirmOwnership(cancelledCtx))
	require.False(t, dc.ownershipLost.Load(), "caller cancel must NOT sticky-set ownershipLost")

	// Second call (fresh ctx): we still own → returns true. Proves the
	// transient failure didn't poison the conn.
	rows := sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}).
		AddRow("pod-1.example.com", "conn-abc")
	mock.ExpectQuery(`SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = \$1 AND workspace_id = \$2 AND short_id = \$3`).
		WithArgs("alice", "W1", "daemon-1").
		WillReturnRows(rows)
	require.True(t, dc.confirmOwnership(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDaemonConn_ConfirmOwnership_StickyOwnershipLost verifies that once
// ownershipLost is set, subsequent calls return false without querying PG
// (fast path).
func TestDaemonConn_ConfirmOwnership_StickyOwnershipLost(t *testing.T) {
	o := owner{userID: "alice", workspaceID: "W1"}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	sr := &sharedRegistry{
		db:           db,
		advertiseURL: "pod-1.example.com",
	}
	hub := &Hub{sharedReg: sr}

	dc := &daemonConn{
		id:      "conn-abc",
		owner:   o,
		shortID: "daemon-1",
		hub:     hub,
	}

	// Pre-set ownershipLost to true.
	dc.ownershipLost.Store(true)

	// Don't expect any query — should return false immediately.
	result := dc.confirmOwnership(context.Background())
	require.False(t, result, "confirmOwnership should return false when ownershipLost is already set")
	require.NoError(t, mock.ExpectationsWereMet())
}
