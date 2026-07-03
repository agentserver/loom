package observerstore

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// PromoteCandidateRow is the row shape persisted by
// PromoteCandidatesWriter. Field order mirrors the DDL.
type PromoteCandidateRow struct {
	CandidateID   string
	Family        string
	SourceTaskIDs string // JSON array — caller marshals
	SurfacedAt    string // RFC3339Nano UTC
	SurfacedBy    string
	WorkspaceID   string
	RunID         string
}

// PromoteCandidatesWriter is the driver-facing interface for the
// promote_candidates table. See docs/specs/wt2-driver-promotion-chain-B1.spec.md §3.
type PromoteCandidatesWriter interface {
	InsertPromoteCandidate(ctx context.Context, row PromoteCandidateRow) error
	UpdatePromoteCandidateDecision(ctx context.Context, candidateID, decision, decisionAt string) error
	ExpirePromoteCandidates(ctx context.Context, cutoffRFC3339 string) (int, error)
}

type promoteCandidatesWriter struct{ db *sql.DB }

// NewPromoteCandidatesWriter wraps *sql.DB into the writer.
func NewPromoteCandidatesWriter(db *sql.DB) PromoteCandidatesWriter {
	return &promoteCandidatesWriter{db: db}
}

const insertPromoteCandidateSQL = `INSERT OR IGNORE INTO promote_candidates (
    row_id, candidate_id, family, source_task_ids, surfaced_at,
    decision, decision_at, surfaced_by, workspace_id, run_id
) VALUES (?, ?, ?, ?, ?, '', '', ?, ?, ?)`

func (w *promoteCandidatesWriter) InsertPromoteCandidate(ctx context.Context, r PromoteCandidateRow) error {
	rowID, err := PrefixedID("promcand")
	if err != nil {
		return fmt.Errorf("observerstore: promote_candidates row_id: %w", err)
	}
	if _, err := w.db.ExecContext(ctx, insertPromoteCandidateSQL,
		rowID,
		r.CandidateID,
		r.Family,
		r.SourceTaskIDs,
		r.SurfacedAt,
		r.SurfacedBy,
		r.WorkspaceID,
		r.RunID,
	); err != nil {
		return fmt.Errorf("observerstore: promote_candidates insert: %w", err)
	}
	return nil
}

const updatePromoteCandidateDecisionSQL = `UPDATE promote_candidates
    SET decision = ?, decision_at = ?
    WHERE candidate_id = ? AND decision = ''`

func (w *promoteCandidatesWriter) UpdatePromoteCandidateDecision(ctx context.Context, candidateID, decision, decisionAt string) error {
	if _, err := w.db.ExecContext(ctx, updatePromoteCandidateDecisionSQL,
		decision, decisionAt, candidateID); err != nil {
		return fmt.Errorf("observerstore: promote_candidates update: %w", err)
	}
	return nil
}

const expirePromoteCandidatesSQL = `UPDATE promote_candidates
    SET decision = 'expired', decision_at = ?
    WHERE decision = '' AND surfaced_at < ?`

func (w *promoteCandidatesWriter) ExpirePromoteCandidates(ctx context.Context, cutoffRFC3339 string) (int, error) {
	// decision_at gets the CURRENT time (not the cutoff — an earlier
	// version bound cutoffRFC3339 to both ?s, stamping expired rows
	// 24h in the past which broke
	// TimeFromUserDecisionToRegisteredMCP's decision-timestamp
	// assumptions). See PR #71 review P2 finding on this function.
	decisionAt := NowUTC()
	res, err := w.db.ExecContext(ctx, expirePromoteCandidatesSQL, decisionAt, cutoffRFC3339)
	if err != nil {
		return 0, fmt.Errorf("observerstore: promote_candidates expire: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("observerstore: promote_candidates expire rows: %w", err)
	}
	// §7 (d) audit log — silent expiry would break the paper's
	// PromotionCandidateSurfacingRate denominator story.
	log.Printf("[expiry] promote_candidates expired=%d cutoff=%s decision_at=%s", n, cutoffRFC3339, decisionAt)
	return int(n), nil
}
