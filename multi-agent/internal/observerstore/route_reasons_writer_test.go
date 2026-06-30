package observerstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTestObserverStore(t *testing.T) (*SQLiteStore, *sql.DB) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.db")
	st, err := OpenSQLite(p)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return st, st.DB()
}

func TestRouteReasonsWriter_RoundTrip(t *testing.T) {
	_, db := openTestObserverStore(t)
	w := NewRouteWriter(db)

	row := RouteReasonRow{
		DecisionID:      "dec-1",
		ConversationID:  "conv-1",
		SelectedAgentID: "slave-A",
		ReasonCode:      "capability_match",
		ReasonText:      "matched skill chat",
		Candidates: []RouteCandidate{
			{AgentID: "slave-A", Score: 1, Reason: "capability_match"},
			{AgentID: "slave-B", Score: 0, Reason: "no_capability_match"},
		},
		DecisionStartedAt:  time.Unix(1700000000, 0).UTC(),
		DecisionEndedAt:    time.Unix(1700000000, 12345).UTC(),
		DecisionDurationNs: 12345,
	}
	require.NoError(t, w.WriteRouteReason(context.Background(), row))

	var got RouteReasonRow
	var candsJSON string
	var startedAt, endedAt string
	require.NoError(t, db.QueryRow(
		`SELECT decision_id, conversation_id, selected_agent_id, reason_code,
                reason_text, candidates_json, decision_started_at,
                decision_ended_at, decision_duration_ns FROM route_reasons WHERE decision_id=?`,
		"dec-1").Scan(
		&got.DecisionID, &got.ConversationID, &got.SelectedAgentID, &got.ReasonCode,
		&got.ReasonText, &candsJSON, &startedAt, &endedAt, &got.DecisionDurationNs,
	))
	require.Equal(t, row.DecisionID, got.DecisionID)
	require.Equal(t, row.ConversationID, got.ConversationID)
	require.Equal(t, row.SelectedAgentID, got.SelectedAgentID)
	require.Equal(t, row.ReasonCode, got.ReasonCode)
	require.Equal(t, row.ReasonText, got.ReasonText)
	require.Equal(t, row.DecisionDurationNs, got.DecisionDurationNs)
	require.Equal(t, row.DecisionStartedAt.UTC().Format(time.RFC3339Nano), startedAt)
	require.Equal(t, row.DecisionEndedAt.UTC().Format(time.RFC3339Nano), endedAt)
	var cands []RouteCandidate
	require.NoError(t, json.Unmarshal([]byte(candsJSON), &cands))
	require.Equal(t, row.Candidates, cands)
}

func TestRouteReasonsWriter_OnConflictDoNothing(t *testing.T) {
	_, db := openTestObserverStore(t)
	w := NewRouteWriter(db)
	row := RouteReasonRow{
		DecisionID: "dup", ConversationID: "c", ReasonCode: "capability_match",
		DecisionStartedAt: time.Unix(1, 0), DecisionEndedAt: time.Unix(2, 0),
	}
	require.NoError(t, w.WriteRouteReason(context.Background(), row))
	require.NoError(t, w.WriteRouteReason(context.Background(), row))
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM route_reasons`).Scan(&n))
	require.Equal(t, 1, n)
}

// TestWriteRouteReason_SanitizesReasonText covers the writer-direct
// defense-in-depth path: if a caller hands a RouteReasonRow with a raw
// secret in ReasonText straight to WriteRouteReason (bypassing
// dispatch.FinalizeAndEmit), the writer MUST still redact it. This guards
// against the spec's acknowledged silent-drop class where a follow-up
// HTTP handler forwards rows from a remote dispatch process and forgets
// to call sanitize again.
func TestWriteRouteReason_SanitizesReasonText(t *testing.T) {
	_, db := openTestObserverStore(t)
	w := NewRouteWriter(db)
	row := RouteReasonRow{
		DecisionID: "dn-1", ConversationID: "c", ReasonCode: "capability_match",
		ReasonText:        "leaked sk-abcdefghijklmnopqrstuv inside",
		DecisionStartedAt: time.Unix(1, 0), DecisionEndedAt: time.Unix(2, 0),
	}
	require.NoError(t, w.WriteRouteReason(context.Background(), row))
	var stored string
	require.NoError(t, db.QueryRow(
		`SELECT reason_text FROM route_reasons WHERE decision_id=?`, "dn-1",
	).Scan(&stored))
	require.NotContains(t, stored, "sk-abcdefghij")
	require.Contains(t, stored, "[REDACTED]")
}

// TestWriteRouteReason_SanitizesConversationID asserts the writer-side
// defense-in-depth covers ConversationID as well as ReasonText (added in
// round-3 review). A direct caller that bypassed dispatch and handed a
// secret-shaped conversation_id must NOT land that value in the column.
func TestWriteRouteReason_SanitizesConversationID(t *testing.T) {
	_, db := openTestObserverStore(t)
	w := NewRouteWriter(db)
	row := RouteReasonRow{
		DecisionID: "dn-conv", ConversationID: "leaked-sk-abcdefghijklmnopqr",
		ReasonCode:        "capability_match",
		DecisionStartedAt: time.Unix(1, 0), DecisionEndedAt: time.Unix(2, 0),
	}
	require.NoError(t, w.WriteRouteReason(context.Background(), row))
	var stored string
	require.NoError(t, db.QueryRow(
		`SELECT conversation_id FROM route_reasons WHERE decision_id=?`, "dn-conv",
	).Scan(&stored))
	require.NotContains(t, stored, "sk-abcdefghij")
	require.Contains(t, stored, "[REDACTED]")
}

func TestRouteReasonsWriter_SQLInjection(t *testing.T) {
	_, db := openTestObserverStore(t)
	w := NewRouteWriter(db)
	malicious := `x'); DROP TABLE route_reasons;--`
	row := RouteReasonRow{
		DecisionID: "inj", ConversationID: malicious, ReasonCode: "capability_match",
		DecisionStartedAt: time.Unix(1, 0), DecisionEndedAt: time.Unix(2, 0),
	}
	require.NoError(t, w.WriteRouteReason(context.Background(), row))
	var stored string
	require.NoError(t, db.QueryRow(`SELECT conversation_id FROM route_reasons WHERE decision_id=?`, "inj").Scan(&stored))
	require.Equal(t, malicious, stored)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM route_reasons`).Scan(&n))
	require.Equal(t, 1, n)
}
