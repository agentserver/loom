package observerstore

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPromoteCandidatesWriter_NoObserverAblation — PR #71 round-2
// review P1-B. When the injected `disabled` predicate returns true,
// Insert / UpdateDecision / Expire all skip the SQL exec and log
// `[ablation] NoObserver: dropped promote_candidates ...`. Matches
// promotionaudit.SQLiteWriter's NoObserver semantics so all three
// observer-store writers behave symmetrically under the ablation.
func TestPromoteCandidatesWriter_NoObserverAblation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	var disabled bool
	w := NewPromoteCandidatesWriterWithAblation(s.DB(), func() bool { return disabled })

	// Capture logs.
	var buf bytes.Buffer
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prev); log.SetFlags(prevFlags) }()

	// Insert one row with ablation OFF — lands in DB.
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID: "cand_ok0001",
		Family:      "fam",
		SurfacedAt:  "2026-07-02T12:00:00.000000000Z",
		SurfacedBy:  "user_hint",
	}))
	var n int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM promote_candidates`).Scan(&n))
	require.Equal(t, 1, n)

	// Flip ablation ON — subsequent Insert drops with log line.
	disabled = true
	buf.Reset()
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID: "cand_drop0001",
		Family:      "fam",
		SurfacedAt:  "2026-07-02T13:00:00.000000000Z",
		SurfacedBy:  "user_hint",
	}))
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM promote_candidates`).Scan(&n))
	require.Equal(t, 1, n, "ablated Insert must NOT persist")
	require.Contains(t, buf.String(), "[ablation] NoObserver: dropped promote_candidates row_id=promcand_")
	require.Contains(t, buf.String(), "candidate_id=cand_drop0001")

	// UpdateDecision under ablation also drops.
	buf.Reset()
	require.NoError(t, w.UpdatePromoteCandidateDecision(context.Background(), "cand_ok0001", "promoted", "2026-07-02T14:00:00.000000000Z"))
	require.Contains(t, buf.String(), "[ablation] NoObserver: dropped promote_candidates decision update")
	var dec string
	require.NoError(t, s.DB().QueryRow(`SELECT decision FROM promote_candidates WHERE candidate_id='cand_ok0001'`).Scan(&dec))
	require.Equal(t, "", dec, "ablated UpdateDecision must NOT touch DB")

	// Expire under ablation also drops.
	buf.Reset()
	nExp, err := w.ExpirePromoteCandidates(context.Background(), "2026-07-03T00:00:00.000000000Z")
	require.NoError(t, err)
	require.Equal(t, 0, nExp)
	require.Contains(t, buf.String(), "[ablation] NoObserver: dropped promote_candidates expiry sweep")
}

func TestSchema_PromoteCandidatesExists(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	cols := tableColumns(t, s, "promote_candidates")
	for _, want := range []string{
		"row_id", "candidate_id", "family", "source_task_ids",
		"surfaced_at", "decision", "decision_at", "surfaced_by",
		"workspace_id", "run_id",
	} {
		if !cols[want] {
			t.Errorf("promote_candidates missing column %q", want)
		}
	}
}

func TestSchema_PromoteCandidatesRejectsBadEnumViaCheck(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	_, err = s.db.Exec(`INSERT INTO promote_candidates
	    (row_id, candidate_id, family, surfaced_at, decision, surfaced_by)
	    VALUES ('r1','c1','fam','2026-07-02T00:00:00Z','bogus','user_hint')`)
	if err == nil {
		t.Fatal("expected CHECK rejection for bad decision")
	}
	_, err = s.db.Exec(`INSERT INTO promote_candidates
	    (row_id, candidate_id, family, surfaced_at, decision, surfaced_by)
	    VALUES ('r2','c2','fam','2026-07-02T00:00:00Z','','bogus')`)
	if err == nil {
		t.Fatal("expected CHECK rejection for bad surfaced_by")
	}
}

func TestPromoteCandidatesWriter_InsertRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	w := NewPromoteCandidatesWriter(s.DB())
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID:   "cand_abcd0123",
		Family:        "csv-profiler",
		SourceTaskIDs: `["task_11111111","task_22222222"]`,
		SurfacedAt:    "2026-07-02T12:00:00.000000000Z",
		SurfacedBy:    "similarity_signal",
		WorkspaceID:   "ws-abc12345",
		RunID:         "run-testxyz001",
	}))
	var name, fam, run string
	err = s.DB().QueryRow(`SELECT candidate_id, family, run_id FROM promote_candidates`).Scan(&name, &fam, &run)
	require.NoError(t, err)
	require.Equal(t, "cand_abcd0123", name)
	require.Equal(t, "csv-profiler", fam)
	require.Equal(t, "run-testxyz001", run)
}

func TestPromoteCandidatesWriter_InsertOrIgnoreOnDuplicate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	w := NewPromoteCandidatesWriter(s.DB())
	row := PromoteCandidateRow{
		CandidateID: "cand_dupe0001",
		Family:      "fam",
		SurfacedAt:  "2026-07-02T12:00:00.000000000Z",
		SurfacedBy:  "user_hint",
		RunID:       "run-testxyz001",
	}
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), row))
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), row))
	var n int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM promote_candidates WHERE candidate_id='cand_dupe0001'`).Scan(&n))
	require.Equal(t, 1, n, "same (run_id, candidate_id) must dedup")

	// Different run_id → NEW row per spec §2.1.
	row.RunID = "run-other0002"
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), row))
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM promote_candidates WHERE candidate_id='cand_dupe0001'`).Scan(&n))
	require.Equal(t, 2, n, "different run_id must produce distinct rows")
}

func TestPromoteCandidatesWriter_UpdateDecision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	w := NewPromoteCandidatesWriter(s.DB())
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID: "cand_dec00001",
		Family:      "fam",
		SurfacedAt:  "2026-07-02T12:00:00.000000000Z",
		SurfacedBy:  "user_hint",
	}))
	require.NoError(t, w.UpdatePromoteCandidateDecision(context.Background(), "cand_dec00001", "promoted", "2026-07-02T13:00:00.000000000Z"))
	var dec, at string
	require.NoError(t, s.DB().QueryRow(`SELECT decision, decision_at FROM promote_candidates WHERE candidate_id='cand_dec00001'`).Scan(&dec, &at))
	require.Equal(t, "promoted", dec)
	require.Equal(t, "2026-07-02T13:00:00.000000000Z", at)

	// Second update with same decision — no-op (WHERE decision='' filters).
	require.NoError(t, w.UpdatePromoteCandidateDecision(context.Background(), "cand_dec00001", "declined", "2026-07-02T14:00:00.000000000Z"))
	require.NoError(t, s.DB().QueryRow(`SELECT decision FROM promote_candidates WHERE candidate_id='cand_dec00001'`).Scan(&dec))
	require.Equal(t, "promoted", dec, "second update must be a no-op")
}

func TestPromoteCandidatesWriter_Expire_LeavesTerminalDecisionsAlone(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	w := NewPromoteCandidatesWriter(s.DB())

	// Row 1: pending, old enough to expire.
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID: "cand_old0001",
		Family:      "fam",
		SurfacedAt:  "2026-07-01T00:00:00.000000000Z",
		SurfacedBy:  "user_hint",
	}))
	// Row 2: already promoted, old.
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID: "cand_prom0001",
		Family:      "fam",
		SurfacedAt:  "2026-07-01T00:00:00.000000000Z",
		SurfacedBy:  "user_hint",
	}))
	require.NoError(t, w.UpdatePromoteCandidateDecision(context.Background(), "cand_prom0001", "promoted", "2026-07-01T01:00:00.000000000Z"))

	// Row 3: pending, recent.
	require.NoError(t, w.InsertPromoteCandidate(context.Background(), PromoteCandidateRow{
		CandidateID: "cand_new00001",
		Family:      "fam",
		SurfacedAt:  "2026-07-02T12:00:00.000000000Z",
		SurfacedBy:  "user_hint",
	}))

	// Expire everything older than cutoff.
	var buf bytes.Buffer
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prev); log.SetFlags(prevFlags) }()

	n, err := w.ExpirePromoteCandidates(context.Background(), "2026-07-02T00:00:00.000000000Z")
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the old-pending row should expire; the promoted one is terminal, the recent one is under cutoff")

	// Log includes the count.
	require.Contains(t, buf.String(), "[expiry] promote_candidates expired=1")

	// Assert states.
	var dec1, dec2, dec3, decAt1 string
	require.NoError(t, s.DB().QueryRow(`SELECT decision, decision_at FROM promote_candidates WHERE candidate_id='cand_old0001'`).Scan(&dec1, &decAt1))
	require.NoError(t, s.DB().QueryRow(`SELECT decision FROM promote_candidates WHERE candidate_id='cand_prom0001'`).Scan(&dec2))
	require.NoError(t, s.DB().QueryRow(`SELECT decision FROM promote_candidates WHERE candidate_id='cand_new00001'`).Scan(&dec3))
	require.Equal(t, "expired", dec1)
	require.Equal(t, "promoted", dec2)
	require.Equal(t, "", dec3)
	// PR #71 review: decision_at MUST be a fresh timestamp (NOT the
	// 24h-ago cutoff). Concrete guard: decision_at is not the cutoff.
	require.NotEqual(t, "2026-07-02T00:00:00.000000000Z", decAt1,
		"decision_at must be current time, not the cutoff")

	// Second expiry with same cutoff: nothing to do (0 rows).
	buf.Reset()
	n2, err := w.ExpirePromoteCandidates(context.Background(), "2026-07-02T00:00:00.000000000Z")
	require.NoError(t, err)
	require.Equal(t, 0, n2)
	require.True(t, strings.Contains(buf.String(), "expired=0"))
}
