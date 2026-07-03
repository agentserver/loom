// Package observerstore extension: WT-2-task-resume primitives.
// Owns WriteID / WriteIDStore / PayloadStager / SQLite
// implementations. See docs/specs/wt2-task-resume.spec.md.
package observerstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ------------------------------------------------------------------
// Regexes (§3, §7(a), §7(d))
// ------------------------------------------------------------------

var (
	// contentHashPattern requires exactly 64 lowercase hex chars
	// (SHA-256 hex encoding). Defends against placeholder / uppercase
	// / short / non-hex inputs (§7(a) test coverage).
	contentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// taskIDPattern enforces the §7(d) task_id regex. Compiled once
	// at init so a later edit cannot loosen it accidentally without
	// breaking tests. Consumers OUTSIDE observerstore must call
	// ValidateTaskID rather than compiling their own regex, so the
	// definition stays single-sourced (F8).
	taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

// ValidateTaskID returns nil if taskID matches
// ^[A-Za-z0-9_-]{8,128}$; ErrInvalidTaskIDForm otherwise. Single
// source of truth for the §7(d) regex — driver.ResumeTask calls
// this rather than compiling its own copy.
func ValidateTaskID(taskID string) error {
	if !taskIDPattern.MatchString(taskID) {
		return ErrInvalidTaskIDForm
	}
	return nil
}

// ------------------------------------------------------------------
// Sentinel errors (spec §9)
// ------------------------------------------------------------------

// ErrEmptyWriteIDComponent is returned by NewWriteID when any of the
// five inputs is the empty string.
var ErrEmptyWriteIDComponent = errors.New("observerstore: empty WriteID component")

// ErrInvalidContentHash is returned by NewWriteID when contentHash
// fails ^[0-9a-f]{64}$.
var ErrInvalidContentHash = errors.New("observerstore: contentHash must be 64 lowercase hex chars")

// ErrInvalidTaskIDForm is returned by NewWriteID when taskID fails
// ^[A-Za-z0-9_-]{8,128}$. The driver package re-exports this as
// driver.ErrInvalidTaskID (plain var alias) so errors.Is works from
// either side.
var ErrInvalidTaskIDForm = errors.New("observerstore: taskID must match [A-Za-z0-9_-]{8,128}")

// ErrEmptyReserveField is returned by Reserve / Commit when any
// required ReserveRequest / CommitRequest field is empty.
var ErrEmptyReserveField = errors.New("observerstore: empty ReserveRequest/CommitRequest field")

// ErrPayloadUnavailable is returned by PayloadStager.Load when no
// row exists for the given WriteID. Not fatal in the executor
// pipeline (caller must Stage first); is fatal in the driver-side
// ReconstructSteps when no fallback bytes are available.
var ErrPayloadUnavailable = errors.New("observerstore: payload unavailable")

// ErrConcurrentLease is returned to callers when Reserve returned
// ReserveInFlight and the caller wants an error value rather than
// polling on the state.
var ErrConcurrentLease = errors.New("observerstore: concurrent lease holder")

// ErrLeaseLost is returned by Commit when the write_ids row's
// lease_owner does not match req.WorkerID (i.e. another worker
// stole the lease after ours expired). The write may or may not
// have happened; the caller MUST surface this as an error rather
// than silently succeeding.
var ErrLeaseLost = errors.New("observerstore: lease lost to another worker")

// ErrNoReservation is returned by Commit when no write_ids row
// exists for the requested id at all (never Reserved, or purged
// by Vacuum before Commit). Distinct from ErrLeaseLost, which
// means the row exists but is held by another worker.
var ErrNoReservation = errors.New("observerstore: no reservation for id")

// ErrInvalidLeaseTTL is returned by Reserve when req.LeaseTTL <= 0.
// Distinct from ErrEmptyReserveField so callers can classify
// programmer bugs vs missing fields.
var ErrInvalidLeaseTTL = errors.New("observerstore: LeaseTTL must be > 0")

// ErrJournalChainBroken is a retained sentinel for a future
// hardening worktree that adds per-record hash chaining to the
// task journal. This worktree never raises it in normal operation.
// driver re-exports as driver.ErrJournalChainBroken (plain alias).
var ErrJournalChainBroken = errors.New("observerstore: journal chain broken")

// ------------------------------------------------------------------
// WriteID type + derivation (§3)
// ------------------------------------------------------------------

// WriteID is the idempotency key gated by WriteIDStore.Reserve.
type WriteID string

// NewWriteID derives an idempotency key from five inputs. See
// docs/specs/wt2-task-resume.spec.md §3 for the derivation rule.
// Length-prefixed (uvarint LEB128) concatenation makes the
// derivation injective regardless of what bytes any input contains.
func NewWriteID(taskID, conversationID, stepID, targetPath, contentHash string) (WriteID, error) {
	if taskID == "" || conversationID == "" || stepID == "" || targetPath == "" || contentHash == "" {
		return "", ErrEmptyWriteIDComponent
	}
	if !contentHashPattern.MatchString(contentHash) {
		return "", ErrInvalidContentHash
	}
	if err := ValidateTaskID(taskID); err != nil {
		return "", err
	}
	h := sha256.New()
	writeLP(h, taskID)
	writeLP(h, conversationID)
	writeLP(h, stepID)
	writeLP(h, targetPath)
	writeLP(h, contentHash)
	sum := h.Sum(nil)
	return WriteID(hex.EncodeToString(sum)), nil
}

// writeLP writes uvarint(len(s)) || s into the hasher.
func writeLP(h interface{ Write([]byte) (int, error) }, s string) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(s)))
	_, _ = h.Write(buf[:n])
	_, _ = h.Write([]byte(s))
}

// ------------------------------------------------------------------
// Reserve state machine + request types (§4)
// ------------------------------------------------------------------

// ReserveState is the four-way outcome of Reserve. See §4.
type ReserveState int

const (
	// ReserveFresh — no prior row for this id; caller MUST perform
	// the write and then call Commit.
	ReserveFresh ReserveState = iota
	// ReserveUncommitted — a prior Reserve for this id succeeded but
	// no Commit ever landed AND the prior lease has expired (OR the
	// prior lease belongs to req.WorkerID). Caller MUST retry the
	// write and then Commit. Safety comes from the content-hash-derived
	// WriteID: the retry's Payload bytes are byte-identical to the
	// earlier attempt's.
	ReserveUncommitted
	// ReserveInFlight — a prior Reserve for this id is still active
	// under a different WorkerID. Caller MUST NOT write.
	ReserveInFlight
	// ReserveCommitted — the write for this id already durably
	// completed. Caller MUST skip.
	ReserveCommitted
)

// String returns the audit-row outcome value for this state.
func (s ReserveState) String() string {
	switch s {
	case ReserveFresh:
		return "fresh"
	case ReserveUncommitted:
		return "uncommitted"
	case ReserveInFlight:
		return "inflight"
	case ReserveCommitted:
		return "committed"
	default:
		return "unknown"
	}
}

// ReserveRequest bundles inputs to Reserve. All fields required.
type ReserveRequest struct {
	ID             WriteID
	RunID          string
	TaskID         string
	ConversationID string
	StepID         string
	WorkerID       string
	LeaseTTL       time.Duration
}

func (r ReserveRequest) validate() error {
	if r.ID == "" || r.RunID == "" || r.TaskID == "" || r.ConversationID == "" ||
		r.StepID == "" || r.WorkerID == "" {
		return ErrEmptyReserveField
	}
	if r.LeaseTTL <= 0 {
		return ErrInvalidLeaseTTL
	}
	return nil
}

// CommitRequest bundles inputs to Commit. All fields required.
type CommitRequest struct {
	ID       WriteID
	RunID    string
	TaskID   string
	WorkerID string
}

func (r CommitRequest) validate() error {
	if r.ID == "" || r.RunID == "" || r.TaskID == "" || r.WorkerID == "" {
		return ErrEmptyReserveField
	}
	return nil
}

// PayloadRef is one row of PayloadStager.ListForStep results.
type PayloadRef struct {
	ID          WriteID
	SHA256      string
	StagedAt    time.Time
	CommittedAt *time.Time // nil when the associated write_ids row's committed_at IS NULL
}

// ------------------------------------------------------------------
// Interfaces (§4, §6.2a)
// ------------------------------------------------------------------

// WriteIDStore mediates access to the write_ids ledger + audit rows.
// Implementations are safe for concurrent use.
type WriteIDStore interface {
	Reserve(ctx context.Context, req ReserveRequest) (ReserveState, error)
	Commit(ctx context.Context, req CommitRequest) error
	Vacuum(ctx context.Context, cutoff time.Time) (int64, error)
	RecordResumeAttempt(ctx context.Context, runID, taskID, outcome, errorKind string) error
}

// PayloadStager persists pre-write payload bytes so a resumed
// ExecutorResume can retry byte-identically after a crash.
type PayloadStager interface {
	Stage(ctx context.Context, id WriteID, taskID, stepID string, payload []byte) error
	Load(ctx context.Context, id WriteID) ([]byte, error)
	ListForStep(ctx context.Context, taskID, stepID string) ([]PayloadRef, error)
}

// ------------------------------------------------------------------
// Metrics collaborator (§8)
// ------------------------------------------------------------------

// Metrics is an optional collaborator that receives counter
// increments from the store. The no-op default (nopMetrics) is used
// when WithMetrics is not called.
type Metrics interface {
	IncCounter(name string, labels map[string]string)
}

type nopMetrics struct{}

func (nopMetrics) IncCounter(string, map[string]string) {}

// ------------------------------------------------------------------
// nowFn — overridable for lease-expiry tests
// ------------------------------------------------------------------

// nowFn is the DEFAULT clock: each new SQLiteWriteIDStore /
// SQLitePayloadStager copies it into its own `now` struct field, so
// WithWriteIDClock / WithPayloadStagerClock swaps take effect on
// individual instances without leaking across parallel tests. Kept
// as a package-level var so external callers who want the same
// default can reference it.
var nowFn = func() time.Time { return time.Now().UTC() }

// ------------------------------------------------------------------
// SQLiteWriteIDStore (§4.1)
// ------------------------------------------------------------------

// SQLiteWriteIDStore is the SQLite implementation of WriteIDStore.
type SQLiteWriteIDStore struct {
	db      *sql.DB
	metrics Metrics
	now     func() time.Time
}

// SQLiteWriteIDStoreOption is the functional-option knob for the
// constructor.
type SQLiteWriteIDStoreOption func(*SQLiteWriteIDStore)

// WithWriteIDMetrics wires a Metrics collaborator. Default is a
// no-op.
func WithWriteIDMetrics(m Metrics) SQLiteWriteIDStoreOption {
	return func(s *SQLiteWriteIDStore) {
		if m != nil {
			s.metrics = m
		}
	}
}

// WithWriteIDClock replaces the wall clock with a test-controllable
// function. Two stores with different clocks do NOT clobber each
// other, so parallel tests can each hold their own clock.
func WithWriteIDClock(fn func() time.Time) SQLiteWriteIDStoreOption {
	return func(s *SQLiteWriteIDStore) {
		if fn != nil {
			s.now = fn
		}
	}
}

// NewSQLiteWriteIDStore constructs a store backed by db. db must be
// a *sql.DB opened via observerstore.OpenSQLite so the DDL is
// already applied.
func NewSQLiteWriteIDStore(db *sql.DB, opts ...SQLiteWriteIDStoreOption) *SQLiteWriteIDStore {
	s := &SQLiteWriteIDStore{db: db, metrics: nopMetrics{}, now: nowFn}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

const (
	sqlReserveInsert = `
INSERT OR IGNORE INTO write_ids
    (id, task_id, conversation_id, step_id, reserved_at,
     lease_owner, lease_expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);`

	sqlReserveSteal = `
UPDATE write_ids
   SET lease_owner = ?, lease_expires_at = ?
 WHERE id = ?
   AND committed_at IS NULL
   AND lease_expires_at < ?;`

	sqlReserveRead = `
SELECT committed_at, lease_owner, lease_expires_at
  FROM write_ids
 WHERE id = ?;`

	sqlReserveEvent = `
INSERT INTO write_id_reserve_events
    (event_id, id, run_id, task_id, outcome, occurred_at)
VALUES (?, ?, ?, ?, ?, ?);`

	sqlCommitUpdate = `
UPDATE write_ids
   SET committed_at = ?, lease_owner = '', lease_expires_at = NULL
 WHERE id = ?
   AND committed_at IS NULL
   AND lease_owner = ?;`

	// sqlReserveExtend refreshes lease_expires_at when a worker
	// re-Reserves its own live-lease row (F5).
	sqlReserveExtend = `
UPDATE write_ids
   SET lease_expires_at = ?
 WHERE id = ?
   AND lease_owner = ?
   AND committed_at IS NULL;`
)

// rfc3339Nano is the timestamp format used across every text column
// in this file. Fixed 9-digit nanosecond suffix so lexicographic
// order matches chronological order (mirrors evalrun's
// formatRFC3339NanoUTC).
const rfc3339NanoFmt = "2006-01-02T15:04:05.000000000Z07:00"

func formatTS(t time.Time) string { return t.UTC().Format(rfc3339NanoFmt) }

// Reserve implements WriteIDStore.
func (s *SQLiteWriteIDStore) Reserve(ctx context.Context, req ReserveRequest) (ReserveState, error) {
	if err := req.validate(); err != nil {
		return 0, err
	}
	nowT := s.now()
	nowStr := formatTS(nowT)
	leaseExp := formatTS(nowT.Add(req.LeaseTTL))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Attempt fresh reservation.
	res, err := tx.ExecContext(ctx, sqlReserveInsert,
		string(req.ID), req.TaskID, req.ConversationID, req.StepID,
		nowStr, req.WorkerID, leaseExp)
	if err != nil {
		return 0, fmt.Errorf("observerstore: reserve insert: %w", err)
	}
	inserted, _ := res.RowsAffected()

	stateStolen := false
	if inserted == 0 {
		// 2. Try to steal an expired lease.
		res2, err := tx.ExecContext(ctx, sqlReserveSteal,
			req.WorkerID, leaseExp, string(req.ID), nowStr)
		if err != nil {
			return 0, fmt.Errorf("observerstore: reserve steal: %w", err)
		}
		if aff, _ := res2.RowsAffected(); aff == 1 {
			stateStolen = true
		}
	}

	// 3. Read back committed_at + lease.
	var committedAt sql.NullString
	var leaseOwner string
	var leaseExpiresAt sql.NullString
	if err := tx.QueryRowContext(ctx, sqlReserveRead, string(req.ID)).
		Scan(&committedAt, &leaseOwner, &leaseExpiresAt); err != nil {
		return 0, fmt.Errorf("observerstore: reserve read: %w", err)
	}

	// Derive state.
	var state ReserveState
	switch {
	case inserted == 1:
		state = ReserveFresh
	case committedAt.Valid:
		state = ReserveCommitted
	case stateStolen:
		state = ReserveUncommitted
	case leaseOwner == req.WorkerID:
		// This worker's own lease is still live — treat as
		// uncommitted retry (spec §4.1 classifier case 4).
		// F5: extend the lease. Without this, a slow same-worker
		// retry after a partial TTL burn could see its own lease
		// expire mid-write, allowing a foreign worker to steal
		// and run WriteStep concurrently.
		if _, err := tx.ExecContext(ctx, sqlReserveExtend,
			leaseExp, string(req.ID), req.WorkerID); err != nil {
			return 0, fmt.Errorf("observerstore: reserve extend: %w", err)
		}
		state = ReserveUncommitted
	default:
		state = ReserveInFlight
	}

	// 4. Emit audit row. run_id is included in the derivation so
	// two runs Reserving the same id at the same fake-clock instant
	// with the same outcome don't collide on the audit PK (F7).
	outcome := state.String()
	eventID := deriveEventID(req.ID, req.RunID, nowStr, outcome)
	if _, err := tx.ExecContext(ctx, sqlReserveEvent,
		eventID, string(req.ID), req.RunID, req.TaskID, outcome, nowStr); err != nil {
		return 0, fmt.Errorf("observerstore: reserve audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("observerstore: reserve commit tx: %w", err)
	}
	s.metrics.IncCounter("resume_write_id_reserve_total",
		map[string]string{"outcome": outcome})
	return state, nil
}

// Commit implements WriteIDStore.
func (s *SQLiteWriteIDStore) Commit(ctx context.Context, req CommitRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	nowStr := formatTS(s.now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, sqlCommitUpdate,
		nowStr, string(req.ID), req.WorkerID)
	if err != nil {
		return fmt.Errorf("observerstore: commit update: %w", err)
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		// Determine whether the row was already committed (idempotent
		// no-op) or whether the lease was stolen. Both cases require
		// a follow-up SELECT.
		var committedAt sql.NullString
		var leaseOwner string
		if err := tx.QueryRowContext(ctx, sqlReserveRead, string(req.ID)).
			Scan(&committedAt, &leaseOwner, new(sql.NullString)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// F6: distinct from ErrLeaseLost — no row ever
				// existed OR Vacuum purged it before Commit ran.
				return fmt.Errorf("observerstore: commit id=%s: %w", req.ID, ErrNoReservation)
			}
			return fmt.Errorf("observerstore: commit read: %w", err)
		}
		if committedAt.Valid {
			// Idempotent no-op. No audit row.
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("observerstore: commit tx (noop): %w", err)
			}
			s.metrics.IncCounter("resume_write_id_commit_total",
				map[string]string{"outcome": "noop"})
			return nil
		}
		// Row exists, uncommitted, but leaseOwner != our WorkerID.
		return ErrLeaseLost
	}

	// Emit audit row. run_id included in derivation (F7).
	outcome := "commit"
	eventID := deriveEventID(req.ID, req.RunID, nowStr, outcome)
	if _, err := tx.ExecContext(ctx, sqlReserveEvent,
		eventID, string(req.ID), req.RunID, req.TaskID, outcome, nowStr); err != nil {
		return fmt.Errorf("observerstore: commit audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("observerstore: commit tx: %w", err)
	}
	s.metrics.IncCounter("resume_write_id_commit_total",
		map[string]string{"outcome": "applied"})
	return nil
}

// deriveEventID computes event_id = hex(sha256(id || run_id ||
// occurred_at || outcome)). Deterministic under retries but PRIMARY
// KEY prevents accidental double insert. Includes run_id (F7) so
// two runs Reserving the same id at the same wall-clock instant
// with the same outcome don't collide on the audit PK — this
// matters under fake-clock tests and is defence-in-depth in prod
// where the sub-nanosecond clock resolution makes collisions
// astronomically unlikely.
func deriveEventID(id WriteID, runID, occurredAt, outcome string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(id))
	_, _ = h.Write([]byte("\x1e"))
	_, _ = h.Write([]byte(runID))
	_, _ = h.Write([]byte("\x1e"))
	_, _ = h.Write([]byte(occurredAt))
	_, _ = h.Write([]byte("\x1e"))
	_, _ = h.Write([]byte(outcome))
	return hex.EncodeToString(h.Sum(nil))
}

// Vacuum implements WriteIDStore. Removes committed rows older than
// cutoff AND their associated write_id_payloads.
func (s *SQLiteWriteIDStore) Vacuum(ctx context.Context, cutoff time.Time) (int64, error) {
	cut := formatTS(cutoff)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// First delete the payloads (child rows) so the write_ids delete
	// doesn't leave orphans.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM write_id_payloads
 WHERE id IN (
   SELECT id FROM write_ids
    WHERE committed_at IS NOT NULL AND committed_at < ?
 );`, cut); err != nil {
		return 0, fmt.Errorf("observerstore: vacuum payloads: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
DELETE FROM write_ids
 WHERE committed_at IS NOT NULL AND committed_at < ?;`, cut)
	if err != nil {
		return 0, fmt.Errorf("observerstore: vacuum write_ids: %w", err)
	}
	aff, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("observerstore: vacuum tx: %w", err)
	}
	return aff, nil
}

// RecordResumeAttempt UPSERTs a row in resume_task_attempts keyed by
// (run_id, task_id). See spec §5.3 for the outcome transition rules.
func (s *SQLiteWriteIDStore) RecordResumeAttempt(ctx context.Context,
	runID, taskID, outcome, errorKind string) error {
	if runID == "" || taskID == "" || outcome == "" {
		return ErrEmptyReserveField
	}
	nowStr := formatTS(s.now())
	// INSERT sets both started_at and updated_at; the ON CONFLICT
	// branch overwrites outcome + error_kind + updated_at only,
	// preserving started_at from the earlier row.
	const sqlUpsert = `
INSERT INTO resume_task_attempts
    (run_id, task_id, outcome, error_kind, started_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id, task_id) DO UPDATE SET
    outcome    = excluded.outcome,
    error_kind = excluded.error_kind,
    updated_at = excluded.updated_at;`
	if _, err := s.db.ExecContext(ctx, sqlUpsert,
		runID, taskID, outcome, errorKind, nowStr, nowStr); err != nil {
		return fmt.Errorf("observerstore: record resume attempt: %w", err)
	}
	s.metrics.IncCounter("resume_task_completed_total",
		map[string]string{"outcome": outcome})
	return nil
}

// ------------------------------------------------------------------
// SQLitePayloadStager (§6.2a)
// ------------------------------------------------------------------

// SQLitePayloadStager is the SQLite implementation of PayloadStager.
type SQLitePayloadStager struct {
	db  *sql.DB
	now func() time.Time
}

// SQLitePayloadStagerOption is the option knob for the stager
// constructor.
type SQLitePayloadStagerOption func(*SQLitePayloadStager)

// WithPayloadStagerClock replaces the wall clock.
func WithPayloadStagerClock(fn func() time.Time) SQLitePayloadStagerOption {
	return func(p *SQLitePayloadStager) {
		if fn != nil {
			p.now = fn
		}
	}
}

// NewSQLitePayloadStager constructs a stager backed by db.
func NewSQLitePayloadStager(db *sql.DB, opts ...SQLitePayloadStagerOption) *SQLitePayloadStager {
	p := &SQLitePayloadStager{db: db, now: nowFn}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Stage implements PayloadStager. INSERT OR IGNORE keeps repeat
// stages of the same (id, payload) tuple a no-op. Because id is
// derived from sha256(payload), a "different payload for same id"
// requires a SHA-256 collision — impossible in practice.
func (p *SQLitePayloadStager) Stage(ctx context.Context, id WriteID,
	taskID, stepID string, payload []byte) error {
	if id == "" || taskID == "" || stepID == "" {
		return ErrEmptyReserveField
	}
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])
	nowStr := formatTS(p.now())
	const sqlInsert = `
INSERT OR IGNORE INTO write_id_payloads
    (id, task_id, step_id, payload, sha256, staged_at)
VALUES (?, ?, ?, ?, ?, ?);`
	if _, err := p.db.ExecContext(ctx, sqlInsert,
		string(id), taskID, stepID, payload, sumHex, nowStr); err != nil {
		return fmt.Errorf("observerstore: stage payload: %w", err)
	}
	return nil
}

// Load implements PayloadStager.
func (p *SQLitePayloadStager) Load(ctx context.Context, id WriteID) ([]byte, error) {
	if id == "" {
		return nil, ErrEmptyReserveField
	}
	var payload []byte
	err := p.db.QueryRowContext(ctx,
		`SELECT payload FROM write_id_payloads WHERE id = ?;`, string(id)).
		Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPayloadUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("observerstore: load payload: %w", err)
	}
	return payload, nil
}

// ListForStep implements PayloadStager. Reverse-chronological (most
// recent first). Populates CommittedAt via a LEFT JOIN on write_ids.
func (p *SQLitePayloadStager) ListForStep(ctx context.Context,
	taskID, stepID string) ([]PayloadRef, error) {
	if taskID == "" || stepID == "" {
		return nil, ErrEmptyReserveField
	}
	// Secondary sort by wp.id (F10) so two rows staged in the same
	// nanosecond (fake-clock tests, or a burst-write race in prod)
	// produce a deterministic order — ReconstructSteps's
	// "most-recent committed / uncommitted" pick becomes stable
	// across shuffled test runs.
	const sqlList = `
SELECT wp.id, wp.sha256, wp.staged_at, wi.committed_at
  FROM write_id_payloads wp
  LEFT JOIN write_ids wi ON wi.id = wp.id
 WHERE wp.task_id = ? AND wp.step_id = ?
 ORDER BY wp.staged_at DESC, wp.id DESC;`
	rows, err := p.db.QueryContext(ctx, sqlList, taskID, stepID)
	if err != nil {
		return nil, fmt.Errorf("observerstore: list payloads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PayloadRef
	for rows.Next() {
		var (
			idStr      string
			sumHex     string
			stagedAtS  string
			committedS sql.NullString
		)
		if err := rows.Scan(&idStr, &sumHex, &stagedAtS, &committedS); err != nil {
			return nil, fmt.Errorf("observerstore: scan payload ref: %w", err)
		}
		stagedAt, perr := time.Parse(rfc3339NanoFmt, stagedAtS)
		if perr != nil {
			return nil, fmt.Errorf("observerstore: parse staged_at: %w", perr)
		}
		ref := PayloadRef{
			ID:       WriteID(idStr),
			SHA256:   sumHex,
			StagedAt: stagedAt,
		}
		if committedS.Valid {
			ct, perr := time.Parse(rfc3339NanoFmt, committedS.String)
			if perr != nil {
				return nil, fmt.Errorf("observerstore: parse committed_at: %w", perr)
			}
			ref.CommittedAt = &ct
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observerstore: iterate payloads: %w", err)
	}
	return out, nil
}
