package observerstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"time"
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
func NewRouteWriter(db *sql.DB) RouteWriter { return &routeReasonsWriter{db: db} }

// reasonTextSecretRE is a defense-in-depth mirror of
// dispatch.secretBlacklistRE. dispatch.FinalizeAndEmit already sanitizes
// ReasonText before WriteRouteReason is called via the WrapRouteWriter
// adapter, but WriteRouteReason is a public API: future callers (the
// spec'd follow-up observer HTTP handler, a hand-built RouteReasonRow
// from another package, etc.) might wire rows directly without going
// through dispatch. We re-apply the same blacklist here so the boundary
// invariant "no raw secret in route_reasons.reason_text" cannot be
// broken by a single missing sanitize call upstream. Patterns are kept
// in sync with dispatch.secretBlacklistRE by the test
// TestWriteRouteReason_SanitizesReasonText.
var reasonTextSecretRE = regexp.MustCompile(
	`sk-[A-Za-z0-9_\-]{8,}|` +
		`eyJ[A-Za-z0-9_\-\.]{16,}|` +
		`AKIA[A-Z0-9]{12,}|` +
		`gh[opsruA-Z]_[A-Za-z0-9]{20,}|` +
		`github_pat_[A-Za-z0-9_]{20,}|` +
		`glpat-[A-Za-z0-9_\-]{20,}|` +
		`AIza[A-Za-z0-9_\-]{20,}|` +
		`xox[baprs]-[A-Za-z0-9-]{8,}|` +
		`-----BEGIN [A-Z ]*PRIVATE KEY-----`,
)

func sanitizeReasonText(s string) string {
	if s == "" {
		return s
	}
	out := reasonTextSecretRE.ReplaceAllString(s, "[REDACTED]")
	runes := []rune(out)
	if len(runes) > 256 {
		return string(runes[:256]) + "...[truncated]"
	}
	return out
}

func (w *routeReasonsWriter) WriteRouteReason(ctx context.Context, r RouteReasonRow) error {
	// Defense-in-depth: scrub ReasonText again at the writer boundary so
	// the route_reasons table can never store a raw secret even if a
	// caller bypassed dispatch.FinalizeAndEmit. The dispatch-side
	// sanitize already ran for the WrapRouteWriter path; this second
	// pass is cheap (idempotent on already-redacted strings) and closes
	// the writer-direct hole the spec acknowledges as a follow-up risk.
	r.ReasonText = sanitizeReasonText(r.ReasonText)
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
