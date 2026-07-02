package observerstore

import (
	"context"
	"database/sql"
	"fmt"
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

type registryLookupSamplesWriter struct{ db *sql.DB }

// NewRegistryLookupSamplesWriter wraps *sql.DB into the writer. The
// schema migration is applied by OpenSQLite via schema.sql.
func NewRegistryLookupSamplesWriter(db *sql.DB) RegistryLookupSamplesWriter {
	return &registryLookupSamplesWriter{db: db}
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
