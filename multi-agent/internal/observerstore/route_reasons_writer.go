package observerstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
)

// RouteReasonRow is the data shape persisted by NewRouteWriter. It mirrors
// the dispatch.RouteDecision fields but is defined locally so observerstore
// does not import dispatch (would create an import cycle through executor).
type RouteReasonRow struct {
	DecisionID         string
	ConversationID     string
	SelectedAgentID    string
	ReasonCode         string
	ReasonText         string
	Candidates         []RouteCandidate
	DecisionStartedAt  time.Time
	DecisionEndedAt    time.Time
	DecisionDurationNs int64
}

// RouteCandidate is one entry in RouteReasonRow.Candidates. The JSON tags are
// the authoritative serialization for the route_reasons.candidates_json
// column; the set MUST stay exactly {agent_id, score, reason} — adding a
// field here without auditing dispatch's security spec §6 (b) widens the
// disclosure surface.
type RouteCandidate struct {
	AgentID string  `json:"agent_id"`
	Score   float64 `json:"score"`
	Reason  string  `json:"reason"`
}

// RouteWriter writes one RouteReasonRow per call. Implementations must be
// goroutine-safe.
type RouteWriter interface {
	WriteRouteReason(ctx context.Context, r RouteReasonRow) error
}

type routeReasonsWriter struct{ db *sql.DB }

// NewRouteWriter returns a RouteWriter backed by the provided *sql.DB. The
// schema migration (CREATE TABLE IF NOT EXISTS route_reasons) is applied by
// OpenSQLite via the embedded schema.sql.
//
// **SQLite-only in this WT.** The INSERT statement below uses `?`
// placeholders and sends candidates_json as a plain string. Both are
// SQLite-native forms; pgx/v5/stdlib (the driver used by
// observerstore/postgres) does not rewrite `?` to `$N` and will not
// auto-cast a plain string into a jsonb column. A pg-native writer is
// deferred to a follow-up WT that ships the pg schema alongside a
// $N-flavor INSERT with an explicit `::jsonb` cast. Passing a pg *sql.DB
// here today will error every call with `syntax error at or near "?"`,
// which is exactly the silent-drop class the wiring is supposed to
// close — DO NOT WIRE against pg until the follow-up lands.
func NewRouteWriter(db *sql.DB) RouteWriter { return &routeReasonsWriter{db: db} }

func (w *routeReasonsWriter) WriteRouteReason(ctx context.Context, r RouteReasonRow) error {
	// Defense-in-depth: scrub the two free-form text columns at the writer
	// boundary so the route_reasons table can never store a raw secret
	// even if a caller bypassed dispatch.FinalizeAndEmit. The
	// dispatch-side finalize gate already ran for the WrapRouteWriter
	// path; this second pass is cheap (idempotent on already-redacted
	// strings) and closes the writer-direct hole the spec acknowledges
	// as a follow-up risk. Both call sites delegate to
	// secretscrub.Sanitize so the blacklist cannot drift — see
	// internal/secretscrub/scrub_test.go for the authoritative pattern
	// matrix.
	r.ReasonText = secretscrub.Sanitize(r.ReasonText)
	r.ConversationID = secretscrub.Sanitize(r.ConversationID)
	payload, err := json.Marshal(r.Candidates)
	if err != nil {
		return err
	}
	if len(r.Candidates) == 0 {
		payload = []byte("[]")
	}
	_, err = w.db.ExecContext(ctx,
		`INSERT INTO route_reasons(
            decision_id, conversation_id, selected_agent_id,
            reason_code, reason_text, candidates_json,
            decision_started_at, decision_ended_at, decision_duration_ns)
         VALUES(?,?,?,?,?,?,?,?,?)
         ON CONFLICT(decision_id) DO NOTHING`,
		r.DecisionID, r.ConversationID, r.SelectedAgentID,
		r.ReasonCode, r.ReasonText, string(payload),
		r.DecisionStartedAt.UTC().Format(time.RFC3339Nano),
		r.DecisionEndedAt.UTC().Format(time.RFC3339Nano),
		r.DecisionDurationNs,
	)
	return err
}
