package commanderhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
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

// TestPGTurnStore_RekeyValidSQL: verifies that the rekey path issues a BEGIN
// transaction and uses rekeyCheckSQL + rekeyUpdateSQL (never the old invalid
// `UPDATE … ON CONFLICT DO NOTHING` form).
func TestPGTurnStore_RekeyValidSQL(t *testing.T) {
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

	// Expect: BEGIN, check new key (not found → ErrNoRows), update old→new, COMMIT.
	mock.ExpectBegin()
	mock.ExpectQuery(rekeyCheckSQL).
		WithArgs("alice", "W1", "agent-A", "sess-real").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(rekeyUpdateSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1", "alice", "W1", "agent-A", "sess-real").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, s.rekey(context.Background(), oldKey, newKey))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPGTurnStore_RekeyExistingTarget: when newKey already exists, rekey must
// DELETE old (not UPDATE) and commit — leaving the existing newKey row intact.
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

	// Expect: BEGIN, check new key (found), delete old, COMMIT.
	mock.ExpectBegin()
	rows := sqlmock.NewRows([]string{"1"}).AddRow(1)
	mock.ExpectQuery(rekeyCheckSQL).
		WithArgs("alice", "W1", "agent-A", "sess-real").
		WillReturnRows(rows)
	mock.ExpectExec(rekeyDeleteOldSQL).
		WithArgs("alice", "W1", "agent-A", "sess-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

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
