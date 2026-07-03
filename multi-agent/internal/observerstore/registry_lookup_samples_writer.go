package observerstore

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// RegistryLookupSampleRow is the data shape persisted by
// NewRegistryLookupSamplesWriter. Field order mirrors the DDL in
// schema.sql; a downstream code path that reorders these MUST update
// the insertSQL constant below in lock-step.
type RegistryLookupSampleRow struct {
	TS              time.Time
	RunID           string // may be "" for ad-hoc / interactive driver sessions
	WorkspaceID     string
	QueryHashPrefix string
	HitCount        int
	RegistryHits    int
	UserspaceHits   int
	TopScore        float64
}

// RegistryLookupSamplesWriter persists one RegistryLookupSampleRow
// per call. Kept as an interface so tests can inject a mock without
// a real *sql.DB. See docs/specs/wt2-driver-promotion-chain-B4.spec.md §5.
type RegistryLookupSamplesWriter interface {
	WriteRegistryLookupSample(ctx context.Context, r RegistryLookupSampleRow) error
}

type registryLookupSamplesWriter struct {
	db       *sql.DB
	disabled telemetryDisabledFn
}

// NewRegistryLookupSamplesWriter wraps *sql.DB into the writer with
// no ablation gating. Kept for tests that don't care about the
// ablation.
func NewRegistryLookupSamplesWriter(db *sql.DB) RegistryLookupSamplesWriter {
	return &registryLookupSamplesWriter{db: db}
}

// NewRegistryLookupSamplesWriterWithAblation is the production
// constructor. `disabled` is called before every mutation; when it
// returns true the mutation is DROPPED with an ablation log line.
// Matches promotionaudit.SQLiteWriter NoObserver semantics. PR #71
// round-2 review P1-B.
func NewRegistryLookupSamplesWriterWithAblation(db *sql.DB, disabled telemetryDisabledFn) RegistryLookupSamplesWriter {
	return &registryLookupSamplesWriter{db: db, disabled: disabled}
}

func (w *registryLookupSamplesWriter) telemetryDropped() bool {
	return w.disabled != nil && w.disabled()
}

// insertRegistryLookupSampleSQL — compile-time const with `?`
// placeholders only.
const insertRegistryLookupSampleSQL = `INSERT INTO registry_lookup_samples (
    row_id, ts, run_id, workspace_id, query_hash_prefix,
    hit_count, registry_hits, userspace_hits, top_score
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (w *registryLookupSamplesWriter) WriteRegistryLookupSample(ctx context.Context, r RegistryLookupSampleRow) error {
	rowID, err := PrefixedID("regslot")
	if err != nil {
		return fmt.Errorf("observerstore: registry_lookup_samples row_id: %w", err)
	}
	if w.telemetryDropped() {
		// PR #71 round-2 review P1-B: matches promotionaudit
		// NoObserver semantics. Hash prefix is safe to log; raw
		// query text is not stored in this row (§5).
		log.Printf("[ablation] NoObserver: dropped registry_lookup_samples row_id=%s run_id=%s query_hash=%s",
			rowID, r.RunID, r.QueryHashPrefix)
		return nil
	}
	tsStr := r.TS.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	if r.TS.IsZero() {
		tsStr = NowUTC()
	}
	if _, err := w.db.ExecContext(ctx, insertRegistryLookupSampleSQL,
		rowID,
		tsStr,
		r.RunID,
		r.WorkspaceID,
		r.QueryHashPrefix,
		r.HitCount,
		r.RegistryHits,
		r.UserspaceHits,
		r.TopScore,
	); err != nil {
		return fmt.Errorf("observerstore: registry_lookup_samples insert: %w", err)
	}
	return nil
}
