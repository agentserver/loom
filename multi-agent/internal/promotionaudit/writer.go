package promotionaudit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/evalrun"
)

// Writer is the per-audit sink for promotion_audit rows. Alternative
// implementations (in-memory mocks) MUST run Validate before recording
// or silently dropping so ablation cannot mask schema violations —
// matching the invariant enforced by evalrun.Writer.
type Writer interface {
	Write(ctx context.Context, f AuditFields) error
	Close() error
}

// SQLiteWriter writes rows to the promotion_audit table of an *sql.DB.
// It respects the SHARED NoObserver ablation via evalrun.DisableTelemetry
// (that package owns the ablation.Default.Register call — only one
// target per FlagName is allowed, so ours reads the same flag rather
// than registering a duplicate). See spec §7 (i).
type SQLiteWriter struct {
	db *sql.DB
}

// NewSQLiteWriter wraps *sql.DB into a Writer that targets the
// promotion_audit table. It does NOT run DDL — that lives in
// observerstore/schema.sql and is applied by the observer init path.
func NewSQLiteWriter(db *sql.DB) *SQLiteWriter {
	return &SQLiteWriter{db: db}
}

// insertSQL is a compile-time-constant SQL template with `?`
// placeholders only. Column list and placeholder order match §3.2 of
// the spec 1:1. Changing this constant requires updating the DDL in
// observerstore/schema.sql AND the Args slice below in lock-step.
const insertSQL = `INSERT INTO promotion_audit (
    row_id, ts, workspace_id, mcp_name, action,
    promoted_by_user_id, driver_thread_id, promotion_reason,
    candidate_source_task_id, registry_hash_after, stage, stage_result
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// nowUTC is overridable in tests for deterministic timestamps. Tests
// that swap this MUST be serial (no t.Parallel) and restore via
// t.Cleanup — the global has no internal lock.
var nowUTC = func() time.Time { return time.Now().UTC() }

// randID generates a 12-byte random hex suffix for row_id. Extracted
// to a package-level var so tests can inject a deterministic sequence.
var randID = func() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// generateRowID returns "promaud_" + 24 hex chars. Never returns "".
func generateRowID() (string, error) {
	suffix, err := randID()
	if err != nil {
		return "", fmt.Errorf("promotionaudit: rand: %w", err)
	}
	return "promaud_" + suffix, nil
}

// Write validates f then either writes one row to promotion_audit OR —
// when evalrun.DisableTelemetry is true — logs a single
// `[ablation] NoObserver: dropped promotion_audit row_id=<id>
// mcp_name=<name>` line and returns nil. Validation runs in BOTH modes
// so ablation cannot mask schema violations (§7 (i)).
//
// If f.TS is zero the writer stamps time.Now().UTC() (matches
// observerstore's NowUTC pattern). Any non-zero caller value is
// normalised to UTC + serialised with a fixed 9-digit nanosecond
// suffix so lexicographic string sort matches chronological order —
// see evalrun.formatRFC3339NanoUTC rationale.
func (w *SQLiteWriter) Write(ctx context.Context, f AuditFields) error {
	if err := Validate(f); err != nil {
		return err
	}
	rowID, err := generateRowID()
	if err != nil {
		return err
	}
	if f.TS.IsZero() {
		f.TS = nowUTC()
	}
	tsStr := f.TS.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")

	if evalrun.DisableTelemetry {
		// Audit pointer per §7 (i) — must include row_id AND mcp_name so
		// a forensic operator can grep both by identity and by MCP.
		log.Printf("[ablation] NoObserver: dropped promotion_audit row_id=%s mcp_name=%s",
			rowID, f.MCPName)
		return nil
	}

	if _, err := w.db.ExecContext(ctx, insertSQL,
		rowID,
		tsStr,
		f.WorkspaceID,
		f.MCPName,
		string(f.Action),
		f.PromotedByUserID,
		f.DriverThreadID,
		string(f.PromotionReason),
		f.CandidateSourceTaskID,
		f.RegistryHashAfter,
		f.Stage,
		string(f.StageResult),
	); err != nil {
		return fmt.Errorf("promotionaudit: insert row_id=%s: %w", rowID, err)
	}
	return nil
}

// Close is a no-op; the caller owns the *sql.DB lifetime (matches
// evalrun.SQLWriter.Close). Kept on the interface so alternative
// writers with real cleanup can implement it.
func (w *SQLiteWriter) Close() error { return nil }

// --- Testing hooks. Not part of the exported API. -------------------

// setTestNowUTC is used by tests only; kept unexported so production
// callers cannot flip it. Returns the previous fn so tests restore in
// t.Cleanup.
func setTestNowUTC(fn func() time.Time) func() time.Time {
	testHookMu.Lock()
	defer testHookMu.Unlock()
	prev := nowUTC
	nowUTC = fn
	return prev
}

// setTestRandID is used by tests only.
func setTestRandID(fn func() (string, error)) func() (string, error) {
	testHookMu.Lock()
	defer testHookMu.Unlock()
	prev := randID
	randID = fn
	return prev
}

var testHookMu sync.Mutex
