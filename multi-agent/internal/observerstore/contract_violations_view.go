// Package observerstore: WT-2-runtime-audit — writer + read helpers for
// the audit_events table and the contract_violations view.
//
// This file owns the ONLY writer path to audit_events (WriteAuditEvent).
// Any other file INSERT-ing into audit_events is a spec §7 (f) violation
// caught by TestOnlyOneAuditEventsWriter in the sibling test file.
//
// The contract_violations view is queried via ViolationsQuery; there is
// no writer path for the view — spec §7 (e) enforces this via
// TestContractViolationsView_NoTrigger_Static and
// TestContractViolationsView_NoWritePaths_Static.
package observerstore

import (
	"context"
	"database/sql"
	"fmt"
)

// AuditEventRow is the storage-typed shape of one audit_events row.
// Field set mirrors journal.AuditEvent 1:1 but strings are pre-formatted
// (in particular, TS is the fixed-width RFC3339Nano wire form so
// lexicographic sort matches chronological order — see
// evalrun.formatRFC3339NanoUTC for the same rationale).
type AuditEventRow struct {
	EventID        string
	WorkspaceID    string
	ConversationID string
	RunID          string
	Kind           string
	Target         string
	SizeBytes      int64
	Hash           string
	TS             string
}

// AuditWriter persists AuditEventRows to the audit_events table. This is
// the sole writer path (spec §7 (f)); every SQL string in the
// implementation is a compile-time constant with ? placeholders.
type AuditWriter interface {
	WriteAuditEvent(ctx context.Context, row AuditEventRow) error
}

type auditEventsWriter struct{ db *sql.DB }

// NewAuditWriter returns an AuditWriter backed by *sql.DB. A nil db
// returns a nil-safe nop implementation so a caller that has not yet
// wired a DB does not need to guard every call — matches
// journal.NewSQLRecorder posture.
func NewAuditWriter(db *sql.DB) AuditWriter {
	if db == nil {
		return nopAuditWriter{}
	}
	return &auditEventsWriter{db: db}
}

type nopAuditWriter struct{}

func (nopAuditWriter) WriteAuditEvent(context.Context, AuditEventRow) error { return nil }

// insertAuditEventSQL is a const so a reviewer can grep for the ONE and
// ONLY SQL string that mutates audit_events. If a caller wants to add
// UPDATE / DELETE / a second INSERT path, spec §7 (f) requires them to
// go through this file (and the corresponding grep test).
const insertAuditEventSQL = `INSERT INTO audit_events
    (event_id, workspace_id, conversation_id, run_id, kind, target,
     size_bytes, hash, ts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

// WriteAuditEvent inserts one row. Duplicate event_id surfaces as a
// UNIQUE-constraint error from the SQLite driver (spec §7 (f) test 16 —
// no silent upsert).
func (w *auditEventsWriter) WriteAuditEvent(ctx context.Context, row AuditEventRow) error {
	if _, err := w.db.ExecContext(ctx, insertAuditEventSQL,
		row.EventID,
		row.WorkspaceID,
		row.ConversationID,
		row.RunID,
		row.Kind,
		row.Target,
		row.SizeBytes,
		row.Hash,
		row.TS,
	); err != nil {
		return fmt.Errorf("observerstore: insert audit_events event_id=%s: %w", row.EventID, err)
	}
	return nil
}

// ContractViolationRow is one row of the contract_violations view.
// Column list is fixed at spec §4; any addition needs the DDL and this
// struct in lockstep.
type ContractViolationRow struct {
	RunID          string
	ConversationID string
	ViolationKind  string
	Target         string
	Expected       sql.NullString // JSON array; NULL when no contract in force
	Observed       string
	TS             string
}

// ViolationsQuery is the read side of the view. §D2 metric-extract
// selects through this interface so the SQL is not duplicated at every
// caller.
type ViolationsQuery interface {
	ByRunID(ctx context.Context, runID string) ([]ContractViolationRow, error)
}

type violationsQuery struct{ db *sql.DB }

// NewViolationsQuery returns a ViolationsQuery over *sql.DB. Nil db
// panics — a nil query source is a programmer error at wiring time (no
// nop path here because there is no scenario in which the caller
// legitimately queries a non-existent DB).
func NewViolationsQuery(db *sql.DB) ViolationsQuery {
	if db == nil {
		panic("observerstore: NewViolationsQuery requires non-nil *sql.DB")
	}
	return &violationsQuery{db: db}
}

// selectByRunIDSQL selects every violation row for a run_id, ordered
// by ts ascending (the view already sorts; the outer ORDER BY is
// belt-and-braces so a downstream JOIN cannot un-sort).
const selectByRunIDSQL = `SELECT run_id, conversation_id, violation_kind,
       target, expected, observed, ts
FROM contract_violations
WHERE run_id = ?
ORDER BY ts ASC`

// ByRunID returns every contract-violation row for the given run.
// Empty result → returns an empty slice (never nil) so callers do not
// need to guard against range-over-nil (matches Verifier.Verify).
func (q *violationsQuery) ByRunID(ctx context.Context, runID string) ([]ContractViolationRow, error) {
	rows, err := q.db.QueryContext(ctx, selectByRunIDSQL, runID)
	if err != nil {
		return nil, fmt.Errorf("observerstore: select contract_violations by run_id=%s: %w", runID, err)
	}
	defer rows.Close()
	out := make([]ContractViolationRow, 0)
	for rows.Next() {
		var r ContractViolationRow
		if err := rows.Scan(&r.RunID, &r.ConversationID, &r.ViolationKind,
			&r.Target, &r.Expected, &r.Observed, &r.TS); err != nil {
			return nil, fmt.Errorf("observerstore: scan contract_violations row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observerstore: iterate contract_violations rows: %w", err)
	}
	return out, nil
}
