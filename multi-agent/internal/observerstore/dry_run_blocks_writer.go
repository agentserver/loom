package observerstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
)

// DryRunBlockMaxDetailBytes bounds the detail column at the writer
// boundary — defense-in-depth against a caller that skipped the
// validator's maxDetailBytes cap. See wt2-dry-run-validator.spec.md §7(c).
// Exported so alternate backends (see postgres/store.go
// WriteDryRunBlock) share one truncation cap and can't drift.
const DryRunBlockMaxDetailBytes = 8192

// DryRunBlockDetailTruncSentinel is the marker appended when detail
// exceeds the cap. Exported alongside DryRunBlockMaxDetailBytes.
const DryRunBlockDetailTruncSentinel = "<...truncated>"

// Package-local aliases preserve the pre-round-4 identifier names for
// the SQLite writer body below.
const (
	dryRunBlockMaxDetailBytes      = DryRunBlockMaxDetailBytes
	dryRunBlockDetailTruncSentinel = DryRunBlockDetailTruncSentinel
)

// DryRunBlockRow is the data shape persisted by NewDryRunBlockWriter.
// Mirrors validator.Block plus the identity + hash columns the
// consumer-view SELECT (spec §6.1) needs.
type DryRunBlockRow struct {
	BlockID                string
	AttemptID              string
	ConversationID         string
	ExperimentID           string
	ContractHash           string
	CapabilitySnapshotHash string
	BlockKind              string
	Field                  string
	Expected               string
	Actual                 string
	Detail                 string
	BlockedAt              time.Time
}

// DryRunBlockWriter writes one DryRunBlockRow per call. Implementations
// must be goroutine-safe.
type DryRunBlockWriter interface {
	WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error
}

type dryRunBlocksWriter struct{ db *sql.DB }

// NewDryRunBlockWriter returns a DryRunBlockWriter backed by db. The
// schema migration (CREATE TABLE IF NOT EXISTS dry_run_blocks) is
// applied by OpenSQLite via the embedded schema.sql.
//
// SQLite-only: uses `?` placeholders. See route_reasons_writer.go for
// the pg-follow-up-required rationale.
func NewDryRunBlockWriter(db *sql.DB) DryRunBlockWriter {
	return &dryRunBlocksWriter{db: db}
}

// insertDryRunBlockSQL is a constant, parameterized INSERT.
// ON CONFLICT(block_id) DO NOTHING supports idempotent retries by
// the driver-side best-effort writer.
const insertDryRunBlockSQL = `
INSERT INTO dry_run_blocks(
    block_id, attempt_id, conversation_id, experiment_id,
    contract_hash, capability_snapshot_hash, block_kind,
    field, expected, actual, detail, blocked_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(block_id) DO NOTHING;
`

func (w *dryRunBlocksWriter) WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error {
	// Defense-in-depth (§7(c)): truncate detail at the writer boundary
	// regardless of upstream. maxDetailBytes in validator.newBlock is
	// the primary cap; this is the last line of defence.
	if len(r.Detail) > dryRunBlockMaxDetailBytes {
		cut := dryRunBlockMaxDetailBytes - len(dryRunBlockDetailTruncSentinel)
		if cut < 0 {
			cut = 0
		}
		r.Detail = r.Detail[:cut] + dryRunBlockDetailTruncSentinel
	}
	// Scrub caller-controlled free-text columns. Mirrors
	// route_reasons_writer's defense: even though validator.newBlock
	// constrains inputs, a caller bypassing the validator (e.g. a
	// direct writer call) must not land raw secrets in the table.
	r.Field = secretscrub.Sanitize(r.Field)
	r.Expected = secretscrub.Sanitize(r.Expected)
	r.Actual = secretscrub.Sanitize(r.Actual)
	r.Detail = secretscrub.Sanitize(r.Detail)
	r.ConversationID = secretscrub.Sanitize(r.ConversationID)

	_, err := w.db.ExecContext(ctx, insertDryRunBlockSQL,
		r.BlockID, r.AttemptID, r.ConversationID, r.ExperimentID,
		r.ContractHash, r.CapabilitySnapshotHash, r.BlockKind,
		r.Field, r.Expected, r.Actual, r.Detail,
		r.BlockedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}
