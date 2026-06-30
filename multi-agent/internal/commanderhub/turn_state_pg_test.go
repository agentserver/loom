package commanderhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// helper to build a turnKey for tests.
func testTurnKey() turnKey {
	return turnKey{
		owner:     owner{userID: "alice", workspaceID: "W1"},
		shortID:   "agent-A",
		sessionID: "sess-1",
	}
}

func TestPGTurnStore_SatisfiesInterface(t *testing.T) {
	var _ turnStateBackend = newPGTurnStore(nil)
}

func TestPGTurnStore_BeginFirstInsert(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	// xmax = 0 means this was a fresh INSERT (not an UPDATE of an existing row).
	rows := sqlmock.NewRows([]string{"inserted"}).AddRow(true)
	mock.ExpectQuery(beginTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1").
		WillReturnRows(rows)

	ok, err := s.begin(context.Background(), key)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_BeginReplaceTerminal(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	// xmax != 0 means the ON CONFLICT ... DO UPDATE ran (old state was terminal).
	rows := sqlmock.NewRows([]string{"inserted"}).AddRow(false)
	mock.ExpectQuery(beginTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1").
		WillReturnRows(rows)

	ok, err := s.begin(context.Background(), key)
	require.NoError(t, err)
	// Still returns true — replacement of a terminal turn is a successful begin.
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_BeginConflictInflight(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	// 0 rows: the WHERE clause on the ON CONFLICT blocked the update because
	// state is 'queued' or 'answering'.
	mock.ExpectQuery(beginTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1").
		WillReturnError(sql.ErrNoRows)

	ok, err := s.begin(context.Background(), key)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_SetFinishFailUpdate(t *testing.T) {
	cases := []struct {
		name string
		run  func(s *pgTurnStore, ctx context.Context, key turnKey, mock sqlmock.Sqlmock) error
	}{
		{
			name: "set_answering",
			run: func(s *pgTurnStore, ctx context.Context, key turnKey, mock sqlmock.Sqlmock) error {
				mock.ExpectExec(setTurnSQL).
					WithArgs("alice", "W1", "agent-A", "sess-1", "answering").
					WillReturnResult(sqlmock.NewResult(0, 1))
				return s.set(ctx, key, turnStateAnswering)
			},
		},
		{
			name: "finish_done",
			run: func(s *pgTurnStore, ctx context.Context, key turnKey, mock sqlmock.Sqlmock) error {
				mock.ExpectExec(finishTurnSQL).
					WithArgs("alice", "W1", "agent-A", "sess-1", "done").
					WillReturnResult(sqlmock.NewResult(0, 1))
				return s.finish(ctx, key, turnStateDone)
			},
		},
		{
			name: "finish_disconnected",
			run: func(s *pgTurnStore, ctx context.Context, key turnKey, mock sqlmock.Sqlmock) error {
				mock.ExpectExec(finishTurnSQL).
					WithArgs("alice", "W1", "agent-A", "sess-1", "disconnected").
					WillReturnResult(sqlmock.NewResult(0, 1))
				return s.finish(ctx, key, turnStateDisconnected)
			},
		},
		{
			name: "fail_with_msg",
			run: func(s *pgTurnStore, ctx context.Context, key turnKey, mock sqlmock.Sqlmock) error {
				mock.ExpectExec(failTurnSQL).
					WithArgs("alice", "W1", "agent-A", "sess-1", "something went wrong").
					WillReturnResult(sqlmock.NewResult(0, 1))
				return s.fail(ctx, key, "something went wrong")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			defer db.Close()

			s := newPGTurnStore(db)
			key := testTurnKey()
			require.NoError(t, tc.run(s, context.Background(), key, mock))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPGTurnStore_GetMissing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	mock.ExpectQuery(getTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1").
		WillReturnError(sql.ErrNoRows)

	snap, err := s.get(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, turnStateIdle, snap.State)
	require.False(t, snap.InFlight)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_GetExisting(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()
	now := time.Now().UTC().Truncate(time.Second)

	rows := sqlmock.NewRows([]string{"state", "awaiting_approval", "active_worker", "message", "updated_at"}).
		AddRow("answering", false, true, "", now)
	mock.ExpectQuery(getTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1").
		WillReturnRows(rows)

	snap, err := s.get(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, turnStateAnswering, snap.State)
	require.True(t, snap.InFlight)
	require.True(t, snap.ActiveWorker)
	require.False(t, snap.AwaitingApproval)
	require.Equal(t, now, snap.updatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTurnStore_RekeyAtomicCTE: verifies that the rekey path issues the atomic
// CTE statement (rekeySQL) — a single Exec with 8 arguments covering both
// oldKey and newKey. The previous multi-statement transaction (BEGIN +
// SELECT FOR UPDATE + UPDATE/DELETE + COMMIT) could not lock a non-existent row,
// causing a PK violation race when two rekeys raced on the same old→new pair.
func TestPGTurnStore_RekeyAtomicCTE(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	oldKey := testTurnKey()
	newKey := turnKey{
		owner:     owner{userID: "alice", workspaceID: "W1"},
		shortID:   "agent-A",
		sessionID: "sess-real",
	}

	// Expect a single Exec (the atomic CTE) — no BEGIN/COMMIT, no SELECT FOR UPDATE.
	mock.ExpectExec(rekeySQL).
		WithArgs(
			"alice", "W1", "agent-A", "sess-1", // oldKey
			"alice", "W1", "agent-A", "sess-real", // newKey
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.rekey(context.Background(), oldKey, newKey))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTurnStore_RekeyExistingTarget: when newKey already exists, rekey must
// still succeed (the ON CONFLICT DO NOTHING branch is transparent to the caller).
// The CTE handles both cases in a single statement; we just verify it is issued.
func TestPGTurnStore_RekeyExistingTarget(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	oldKey := testTurnKey()
	newKey := turnKey{
		owner:     owner{userID: "alice", workspaceID: "W1"},
		shortID:   "agent-A",
		sessionID: "sess-real",
	}

	// Same CTE regardless of whether newKey already exists — ON CONFLICT DO NOTHING handles it.
	mock.ExpectExec(rekeySQL).
		WithArgs(
			"alice", "W1", "agent-A", "sess-1", // oldKey
			"alice", "W1", "agent-A", "sess-real", // newKey
		).
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows = ON CONFLICT path

	require.NoError(t, s.rekey(context.Background(), oldKey, newKey))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_RekeyNoop(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	// oldKey == newKey — no SQL should be issued.
	require.NoError(t, s.rekey(context.Background(), key, key))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_CleanupOrphans(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	d := 5 * time.Minute

	mock.ExpectExec(cleanupTurnsSQL).
		WithArgs(d.String()).
		WillReturnResult(sqlmock.NewResult(0, 3))

	require.NoError(t, s.cleanupOrphans(context.Background(), d))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_UpdateFromEnvelope_TerminalDone(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	// command_result with no awaiting_user → finish(done)
	payload, _ := json.Marshal(map[string]any{"result": map[string]any{"session_id": "sess-1"}})
	env := commander.Envelope{Type: "command_result", Payload: payload}

	mock.ExpectExec(finishTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1", "done").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.updateFromEnvelope(context.Background(), key, "session_turn", env))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGTurnStore_UpdateFromEnvelope_StatusAnswering(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newPGTurnStore(db)
	key := testTurnKey()

	ep := commander.EventPayload{EventKind: "status", StatusCode: agentbackend.StatusAnswering, Text: "running"}
	payload, _ := json.Marshal(ep)
	env := commander.Envelope{Type: "event", Payload: payload}

	mock.ExpectExec(setTurnSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1", "answering").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.updateFromEnvelope(context.Background(), key, "session_turn", env))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTurnStore_RekeyConurrentNoPKViolation spawns 16 goroutines that all
// concurrently call rekey on the same old→new pair against a real Postgres
// database. The atomic CTE guarantees exactly one row ends up at newKey and
// zero PK violations occur.
//
// Env-gated: set OBSERVER_POSTGRES_TEST_DSN to run this test.
func TestPGTurnStore_RekeyConurrentNoPKViolation(t *testing.T) {
	dsn := os.Getenv(multiPodDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run postgres rekey concurrency test", multiPodDSNEnv)
	}

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(context.Background()))
	require.NoError(t, authstore.MigratePostgres(db), "MigratePostgres")

	// Use a unique session ID per test run to avoid interference with concurrent tests.
	runID := fmt.Sprintf("concurrent-rekey-%d", time.Now().UnixNano())
	oldKey := turnKey{
		owner:     owner{userID: "alice-concurrent", workspaceID: "W-concurrent"},
		shortID:   "agent-concurrent",
		sessionID: "old-" + runID,
	}
	newKey := turnKey{
		owner:     owner{userID: "alice-concurrent", workspaceID: "W-concurrent"},
		shortID:   "agent-concurrent",
		sessionID: "new-" + runID,
	}

	s := newPGTurnStore(db)
	ctx := context.Background()

	// Seed the old row so there is something to rekey.
	ok, err := s.begin(ctx, oldKey)
	require.NoError(t, err)
	require.True(t, ok, "begin should succeed for a fresh key")

	// 16 goroutines all call rekey(old→new) concurrently.
	const goroutines = 16
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // synchronised start
			errs[i] = s.rekey(ctx, oldKey, newKey)
		}(i)
	}
	close(start) // release all goroutines simultaneously
	wg.Wait()

	// None of the rekey calls should return an error.
	for i, e := range errs {
		require.NoError(t, e, "goroutine %d rekey error", i)
	}

	// Exactly one row at newKey should exist.
	snap, err := s.get(ctx, newKey)
	require.NoError(t, err)
	require.NotEqual(t, turnStateIdle, snap.State,
		"newKey must exist after concurrent rekeys (state=%s)", snap.State)

	// The old key must be gone.
	snapOld, err := s.get(ctx, oldKey)
	require.NoError(t, err)
	require.Equal(t, turnStateIdle, snapOld.State,
		"oldKey must not exist after rekey (got state=%s)", snapOld.State)

	// Cleanup.
	_, _ = db.ExecContext(ctx, `DELETE FROM commander_turns WHERE user_id=$1 AND workspace_id=$2`,
		"alice-concurrent", "W-concurrent")
}
