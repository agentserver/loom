package commanderhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// SQL statements as package-level consts so unit tests can assert the exact
// shape via sqlmock.QueryMatcherEqual.

const beginTurnSQL = `INSERT INTO commander_turns (user_id, workspace_id, short_id, session_id, state, updated_at) VALUES ($1, $2, $3, $4, 'queued', now()) ON CONFLICT (user_id, workspace_id, short_id, session_id) DO UPDATE SET state='queued', awaiting_approval=false, active_worker=false, message='', updated_at=now() WHERE commander_turns.state IN ('idle','done','error','awaiting_approval','disconnected') RETURNING (xmax = 0) AS inserted`

const setTurnSQL = `UPDATE commander_turns SET state=$5, updated_at=now() WHERE user_id=$1 AND workspace_id=$2 AND short_id=$3 AND session_id=$4`

const finishTurnSQL = `UPDATE commander_turns SET state=$5, updated_at=now() WHERE user_id=$1 AND workspace_id=$2 AND short_id=$3 AND session_id=$4`

const failTurnSQL = `UPDATE commander_turns SET state='error', message=$5, updated_at=now() WHERE user_id=$1 AND workspace_id=$2 AND short_id=$3 AND session_id=$4`

const rekeyTurnSQL = `UPDATE commander_turns SET user_id=$5, workspace_id=$6, short_id=$7, session_id=$8 WHERE user_id=$1 AND workspace_id=$2 AND short_id=$3 AND session_id=$4 ON CONFLICT DO NOTHING`

const getTurnSQL = `SELECT state, awaiting_approval, active_worker, message, updated_at FROM commander_turns WHERE user_id=$1 AND workspace_id=$2 AND short_id=$3 AND session_id=$4`

const cleanupTurnsSQL = `UPDATE commander_turns SET state='disconnected', updated_at=now() WHERE state IN ('queued','answering') AND updated_at < now() - $1::interval`

// pgTurnStore is a PostgreSQL-backed implementation of turnStateBackend.
// It persists turn state in the commander_turns table so state survives
// pod restarts and is visible across pods in a cluster deployment.
type pgTurnStore struct {
	db *sql.DB
}

func newPGTurnStore(db *sql.DB) *pgTurnStore {
	return &pgTurnStore{db: db}
}

// begin attempts to atomically start a new turn for key. Returns (true, nil)
// when the turn was started (fresh insert or replacement of a terminal row).
// Returns (false, nil) when a turn is already in flight (queued or answering).
//
// The RETURNING clause yields (xmax = 0) AS inserted:
//   - xmax=0 → fresh INSERT; inserted=true.
//   - xmax!=0 → ON CONFLICT UPDATE replaced a terminal row; inserted=false.
//
// Both cases indicate a successful begin — one row was RETURNED. When the
// WHERE clause on the ON CONFLICT blocks the update (state is queued/answering),
// no row is returned and QueryRowContext yields sql.ErrNoRows.
func (s *pgTurnStore) begin(ctx context.Context, key turnKey) (bool, error) {
	var inserted bool
	row := s.db.QueryRowContext(ctx, beginTurnSQL,
		key.owner.userID, key.owner.workspaceID, key.shortID, key.sessionID)
	if err := row.Scan(&inserted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// ON CONFLICT WHERE blocked the update (state was queued/answering).
			return false, nil
		}
		return false, err
	}
	// One row was returned (either fresh insert or terminal replacement) → begin succeeded.
	return true, nil
}

// set updates the state of an existing turn entry. If the row does not
// exist yet (race during forwarding), this is a silent no-op.
func (s *pgTurnStore) set(ctx context.Context, key turnKey, state turnState) error {
	_, err := s.db.ExecContext(ctx, setTurnSQL,
		key.owner.userID, key.owner.workspaceID, key.shortID, key.sessionID,
		string(state))
	return err
}

// finish updates the state of a turn to a terminal state. If the row does
// not exist, this is a silent no-op.
func (s *pgTurnStore) finish(ctx context.Context, key turnKey, state turnState) error {
	_, err := s.db.ExecContext(ctx, finishTurnSQL,
		key.owner.userID, key.owner.workspaceID, key.shortID, key.sessionID,
		string(state))
	return err
}

// fail sets the turn state to 'error' with an explanatory message.
func (s *pgTurnStore) fail(ctx context.Context, key turnKey, msg string) error {
	_, err := s.db.ExecContext(ctx, failTurnSQL,
		key.owner.userID, key.owner.workspaceID, key.shortID, key.sessionID,
		msg)
	return err
}

// rekey migrates a turn entry from oldKey to newKey, used when the
// fresh-session protocol returns the real backend session ID. When newKey
// already exists, ON CONFLICT DO NOTHING preserves the existing entry.
func (s *pgTurnStore) rekey(ctx context.Context, oldKey, newKey turnKey) error {
	if oldKey == newKey {
		return nil
	}
	_, err := s.db.ExecContext(ctx, rekeyTurnSQL,
		oldKey.owner.userID, oldKey.owner.workspaceID, oldKey.shortID, oldKey.sessionID,
		newKey.owner.userID, newKey.owner.workspaceID, newKey.shortID, newKey.sessionID)
	return err
}

// get returns the current snapshot for key. On sql.ErrNoRows (key doesn't
// exist), returns a zero-value snapshot with State=idle and nil error.
func (s *pgTurnStore) get(ctx context.Context, key turnKey) (turnSnapshot, error) {
	var snap turnSnapshot
	var state string
	var updatedAt time.Time
	err := s.db.QueryRowContext(ctx, getTurnSQL,
		key.owner.userID, key.owner.workspaceID, key.shortID, key.sessionID).
		Scan(&state, &snap.AwaitingApproval, &snap.ActiveWorker, &snap.Message, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return turnSnapshot{State: turnStateIdle}, nil
	}
	if err != nil {
		return turnSnapshot{}, err
	}
	snap.State = turnState(state)
	snap.InFlight = snap.State == turnStateQueued || snap.State == turnStateAnswering
	snap.updatedAt = updatedAt
	return snap, nil
}

// updateFromEnvelope translates envelope-derived state changes into
// persistent SQL updates, mirroring the logic in http.go::updateTurnStateFromEnvelope.
func (s *pgTurnStore) updateFromEnvelope(ctx context.Context, key turnKey, command string, env commander.Envelope) error {
	switch env.Type {
	case "event":
		var ep commander.EventPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			return nil
		}
		switch ep.EventKind {
		case "status":
			switch ep.StatusCode {
			case agentbackend.StatusQueued, agentbackend.StatusStarting:
				return s.set(ctx, key, turnStateQueued)
			case agentbackend.StatusAnswering:
				return s.set(ctx, key, turnStateAnswering)
			case agentbackend.StatusAwaitingApproval:
				return s.finish(ctx, key, turnStateAwaitingApproval)
			case agentbackend.StatusDone:
				return s.finish(ctx, key, turnStateDone)
			case agentbackend.StatusError:
				return s.fail(ctx, key, ep.Text)
			default:
				switch ep.Text {
				case "queued on daemon", "queued-on-daemon", "accepted by daemon", "starting codex":
					return s.set(ctx, key, turnStateQueued)
				case "codex running":
					return s.set(ctx, key, turnStateAnswering)
				}
			}
		case "chunk":
			return s.set(ctx, key, turnStateAnswering)
		}
	case "command_result":
		if payloadAwaitingUser(env.Payload) {
			return s.finish(ctx, key, turnStateAwaitingApproval)
		}
		return s.finish(ctx, key, turnStateDone)
	case "error":
		return s.fail(ctx, key, errorMessage(env.Payload))
	}
	return nil
}

// cleanupOrphans marks turns stuck in queued or answering state for longer
// than older as disconnected. Called by the periodic sweeper.
func (s *pgTurnStore) cleanupOrphans(ctx context.Context, older time.Duration) error {
	_, err := s.db.ExecContext(ctx, cleanupTurnsSQL, older.String())
	return err
}
