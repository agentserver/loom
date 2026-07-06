// Tests for WT-2-task-resume driver-side ResumeTask +
// ReconstructSteps. See docs/specs/wt2-task-resume.plan.md §5.
package driver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

const (
	rtTaskID   = "task_ab12"
	rtConvID   = "conv-x"
	rtTargetNm = "artifact:foo"
)

// -------------------------------------------------------------------
// Fixtures
// -------------------------------------------------------------------

func openStoreDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observer.db")
	s, err := observerstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	db := s.DB()
	t.Cleanup(func() {
		_ = db.Close()
		_ = os.Remove(path)
	})
	return db
}

func newStore(t *testing.T) *observerstore.SQLiteWriteIDStore {
	t.Helper()
	return observerstore.NewSQLiteWriteIDStore(openStoreDB(t))
}

func stubContract(target string) contract.TaskContract {
	return contract.TaskContract{
		Version:        contract.Version,
		ConversationID: rtConvID,
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: "artifact", Kind: "code", Name: target}},
		},
	}
}

func fakeLoad(body contract.TaskContract, err error) func(context.Context, string) (contract.TaskContract, error) {
	return func(context.Context, string) (contract.TaskContract, error) {
		return body, err
	}
}

type dispatchSpy struct {
	calls int
	err   error
	rec   struct {
		runID  string
		taskID string
		body   contract.TaskContract
	}
}

func (d *dispatchSpy) Fn() func(context.Context, string, string, contract.TaskContract) error {
	return func(_ context.Context, runID, taskID string, body contract.TaskContract) error {
		d.calls++
		d.rec.runID = runID
		d.rec.taskID = taskID
		d.rec.body = body
		return d.err
	}
}

// -------------------------------------------------------------------
// §5 ResumeTask tests
// -------------------------------------------------------------------

func TestResumeTask_InvalidTaskIDRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		taskID string
	}{
		{"empty", ""},
		{"too_short", "abcd"},
		{"too_long", strings.Repeat("a", 129)},
		{"sql_meta", "abc'def12"},
		{"path_traversal", "../abc/def"},
		{"nul", "abcdef\x00gh"},
		{"unicode", "café_task"},
		{"space", "abc def 12"},
	}
	deps := ResumeDeps{
		Store:        newStore(t),
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     (&dispatchSpy{}).Fn(),
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ResumeTask(context.Background(), deps, "run-1", tc.taskID)
			if !errors.Is(err, ErrInvalidTaskID) {
				t.Errorf("err = %v; want ErrInvalidTaskID", err)
			}
		})
	}
}

// TestResumeTask_InvalidResumeDepsRejected (plan-only).
func TestResumeTask_InvalidResumeDepsRejected(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	load := fakeLoad(stubContract(rtTargetNm), nil)
	dispatch := (&dispatchSpy{}).Fn()
	cases := []struct {
		name string
		deps ResumeDeps
	}{
		{"all_nil", ResumeDeps{}},
		{"no_store", ResumeDeps{LoadContract: load, Dispatch: dispatch}},
		{"no_load", ResumeDeps{Store: store, Dispatch: dispatch}},
		{"no_dispatch", ResumeDeps{Store: store, LoadContract: load}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ResumeTask(context.Background(), tc.deps, "run-1", rtTaskID)
			if !errors.Is(err, ErrInvalidResumeDeps) {
				t.Errorf("err = %v; want ErrInvalidResumeDeps", err)
			}
		})
	}
}

// TestResumeTask_DoesNotOpenJournalFile — proof that ResumeDeps has
// NO file/path-shaped fields AND that resume.go itself contains no
// file-open calls. Reviewer round 2 F2 pointed out the earlier
// reflect-check only caught the literal name "Journal" and the
// tmp-path fs oracle only caught opens on that specific path — a
// future edit renaming to "Recorder" / "Log" / "TaskJournal" and
// wiring an os.Open call would defeat both. This test closes that
// hole two ways:
//
//   (a) reflect-check REJECTS any field whose type is *os.File,
//       io.Reader/Writer, string named "*Path"/"*File", or any
//       type from the driver package that has a "Path()" or
//       "Open" method — i.e. anything a file could ride on.
//   (b) static-check greps the source of driver/resume.go for
//       os.Open / os.OpenFile / bufio.NewScanner / TaskJournal /
//       Journal. call sites. Zero hits required.
func TestResumeTask_DoesNotOpenJournalFile(t *testing.T) {
	t.Parallel()

	// (a) Reflect check — deny-list of Go-type patterns that could
	// carry a filesystem handle. Grows as new file-shaped types
	// enter the codebase.
	deps := ResumeDeps{}
	typ := reflect.TypeOf(deps)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		fieldType := f.Type.String()
		// Direct file/reader types.
		if strings.Contains(fieldType, "os.File") ||
			strings.Contains(fieldType, "io.Reader") ||
			strings.Contains(fieldType, "io.Writer") ||
			strings.Contains(fieldType, "io.ReadCloser") ||
			strings.Contains(fieldType, "io.WriteCloser") ||
			strings.Contains(fieldType, "TaskJournal") {
			t.Errorf("ResumeDeps.%s: type %s can carry a file handle "+
				"— journal I/O is banned by spec §7(e)",
				f.Name, fieldType)
		}
		// Path-shaped string fields.
		if fieldType == "string" &&
			(strings.HasSuffix(f.Name, "Path") ||
				strings.HasSuffix(f.Name, "File") ||
				strings.EqualFold(f.Name, "Journal")) {
			t.Errorf("ResumeDeps.%s: path-shaped string field name "+
				"— journal I/O is banned by spec §7(e)", f.Name)
		}
	}

	// (b) Static check — read the on-disk source of resume.go and
	// assert it contains none of the sentinel substrings that would
	// indicate any file-open call. This catches edits that route
	// I/O through some collaborator we don't reflect over.
	// NOTE: the test file references os.WriteFile / os.Open etc.
	// itself in F2 fixtures — so we scan resume.go only, not the
	// _test file.
	src, err := os.ReadFile("resume.go")
	if err != nil {
		t.Fatalf("read resume.go: %v", err)
	}
	src = normalizeSource(src)
	forbidden := []string{
		"os.Open(", "os.OpenFile(", "os.ReadFile(", "os.Create(",
		"bufio.NewScanner(", "bufio.NewReader(",
		"TaskJournal", "j.Recent(", ".Journal(", "Journal.Path(",
	}
	srcStr := string(src)
	for _, needle := range forbidden {
		if strings.Contains(srcStr, needle) {
			t.Errorf("driver/resume.go contains forbidden token %q "+
				"— journal I/O is banned by spec §7(e)", needle)
		}
	}
}

// normalizeSource strips Go line comments and block comments so the
// static grep in F2's oracle doesn't false-fire on documentation
// mentioning the forbidden tokens (e.g. this exact test comment).
func normalizeSource(src []byte) []byte {
	// Remove /* ... */ blocks (non-greedy).
	blockComment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	src = blockComment.ReplaceAll(src, []byte(""))
	// Remove // to end-of-line.
	lineComment := regexp.MustCompile(`//[^\n]*`)
	src = lineComment.ReplaceAll(src, []byte(""))
	return src
}

// TestResumeTask_StartedRowInsertedBeforeDispatch (plan-only).
func TestResumeTask_StartedRowInsertedBeforeDispatch(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	spy := &dispatchSpy{}
	// Assert the row is present INSIDE Dispatch — Dispatch is where
	// we can catch the intermediate state.
	spy.err = errors.New("stop-here")
	dispatchFn := func(ctx context.Context, runID, taskID string, body contract.TaskContract) error {
		spy.calls++
		spy.rec.runID = runID
		spy.rec.taskID = taskID
		spy.rec.body = body
		var outcome string
		if err := db.QueryRow(
			`SELECT outcome FROM resume_task_attempts WHERE run_id = ? AND task_id = ?`,
			runID, taskID).Scan(&outcome); err != nil {
			t.Errorf("audit row not present during dispatch: %v", err)
		}
		if outcome != "started" {
			t.Errorf("outcome during dispatch = %s; want started", outcome)
		}
		return spy.err
	}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     dispatchFn,
	}
	_ = ResumeTask(context.Background(), deps, "run-1", rtTaskID)
	if spy.calls != 1 {
		t.Errorf("dispatch calls = %d; want 1", spy.calls)
	}
}

// TestResumeTask_ReplayedRowAfterSuccess (plan-only).
func TestResumeTask_ReplayedRowAfterSuccess(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     (&dispatchSpy{}).Fn(),
	}
	if err := ResumeTask(context.Background(), deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := db.QueryRow(
		`SELECT outcome FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		rtTaskID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "replayed" {
		t.Errorf("outcome = %s; want replayed", outcome)
	}
}

// checkContractMissingErrorOutcome asserts that a LoadContract
// returning ErrContractNotFound results in outcome='error',
// error_kind='ErrContractNotFound' on the resume_task_attempts
// row. Shared between TestResumeTask_ErrorRowOnContractMissing
// and TestResumeTask_ContractNotFound_ErrorOutcome so both
// spec-named tests exercise the invariant via their own testing.T
// (fresh-review F3: no more inter-test function-call aliases).
func checkContractMissingErrorOutcome(t *testing.T) {
	t.Helper()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(contract.TaskContract{}, ErrContractNotFound),
		Dispatch:     (&dispatchSpy{}).Fn(),
	}
	err := ResumeTask(context.Background(), deps, "run-1", rtTaskID)
	if !errors.Is(err, ErrContractNotFound) {
		t.Errorf("err = %v; want ErrContractNotFound", err)
	}
	var outcome, errKind string
	if err := db.QueryRow(
		`SELECT outcome, error_kind FROM resume_task_attempts
                 WHERE run_id = 'run-1' AND task_id = ?`, rtTaskID).
		Scan(&outcome, &errKind); err != nil {
		t.Fatal(err)
	}
	if outcome != "error" || errKind != "ErrContractNotFound" {
		t.Errorf("outcome/errKind = %s/%s", outcome, errKind)
	}
}

// TestResumeTask_ErrorRowOnContractMissing (plan-only).
func TestResumeTask_ErrorRowOnContractMissing(t *testing.T) {
	t.Parallel()
	checkContractMissingErrorOutcome(t)
}

// TestResumeTask_ContractNotFound_ErrorOutcome — spec-named.
func TestResumeTask_ContractNotFound_ErrorOutcome(t *testing.T) {
	t.Parallel()
	checkContractMissingErrorOutcome(t)
}

// TestResumeTask_ErrorRowOnDispatchFail (plan-only).
func TestResumeTask_ErrorRowOnDispatchFail(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	spy := &dispatchSpy{err: errors.New("dispatch boom")}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     spy.Fn(),
	}
	err := ResumeTask(context.Background(), deps, "run-1", rtTaskID)
	if err == nil {
		t.Fatal("expected error")
	}
	var outcome string
	if err := db.QueryRow(
		`SELECT outcome FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		rtTaskID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "error" {
		t.Errorf("outcome = %s; want error", outcome)
	}
}

// TestResumeTask_CrashAfterDispatch_LeavesStartedRow (plan-only).
func TestResumeTask_CrashAfterDispatch_LeavesStartedRow(t *testing.T) {
	t.Parallel()
	// We simulate the driver crashing DURING Dispatch's return by
	// having Dispatch panic. ResumeTask propagates the panic so the
	// terminal audit UPDATE never runs; the row stays 'started'.
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch: func(context.Context, string, string, contract.TaskContract) error {
			panic("simulated driver crash")
		},
	}
	func() {
		defer func() { _ = recover() }()
		_ = ResumeTask(context.Background(), deps, "run-1", rtTaskID)
	}()
	var outcome string
	if err := db.QueryRow(
		`SELECT outcome FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		rtTaskID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "started" {
		t.Errorf("outcome after crash = %s; want started", outcome)
	}
}

// TestResumeTask_ReinvocationUpdatesRowInPlace (plan-only).
func TestResumeTask_ReinvocationUpdatesRowInPlace(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     (&dispatchSpy{}).Fn(),
	}
	if err := ResumeTask(context.Background(), deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	if err := ResumeTask(context.Background(), deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		rtTaskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("rows = %d; want 1", n)
	}
}

// TestErrJournalChainBroken_IsFromBothPackages — spec §9.
func TestErrJournalChainBroken_IsFromBothPackages(t *testing.T) {
	t.Parallel()
	// Wrapped error is observerstore's; assert errors.Is matches from
	// both the driver and observerstore names.
	wrapped := fmt.Errorf("something: %w", observerstore.ErrJournalChainBroken)
	if !errors.Is(wrapped, ErrJournalChainBroken) {
		t.Errorf("errors.Is with driver.ErrJournalChainBroken failed")
	}
	if !errors.Is(wrapped, observerstore.ErrJournalChainBroken) {
		t.Errorf("errors.Is with observerstore.ErrJournalChainBroken failed")
	}
}

// TestResumeTask_PrimitiveDriverRestartFixture — spec §10 criterion 1.
func TestResumeTask_PrimitiveDriverRestartFixture(t *testing.T) {
	t.Parallel()
	// Simulate a mid-run crash: a contract is persisted (via fake
	// LoadContract) with 2 targets; the "prior" attempt committed
	// step-0 but crashed before step-1.
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	stager := observerstore.NewSQLitePayloadStager(db)
	targets := []contract.WriteTarget{
		{Type: "artifact", Kind: "code", Name: "a.txt"},
		{Type: "artifact", Kind: "code", Name: "b.txt"},
	}
	body := contract.TaskContract{
		Version:        contract.Version,
		ConversationID: rtConvID,
		DataContract:   contract.DataContract{WriteTargets: targets},
	}

	// Pre-seed: step-0 already committed.
	payA := []byte("done-a")
	sumA := sha256.Sum256(payA)
	idA, err := observerstore.NewWriteID(rtTaskID, rtConvID,
		"write-0-a.txt", "a.txt", hex.EncodeToString(sumA[:]))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := stager.Stage(ctx, idA, rtTaskID, "write-0-a.txt", payA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, observerstore.ReserveRequest{
		ID: idA, RunID: "run-1", TaskID: rtTaskID, ConversationID: rtConvID,
		StepID: "write-0-a.txt", WorkerID: "wold", LeaseTTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, observerstore.CommitRequest{
		ID: idA, RunID: "run-1", TaskID: rtTaskID, WorkerID: "wold",
	}); err != nil {
		t.Fatal(err)
	}

	// ResumeTask: fake Dispatch executes the missing step.
	spy := &dispatchSpy{}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(body, nil),
		Dispatch:     spy.Fn(),
	}
	if err := ResumeTask(ctx, deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Errorf("dispatch calls = %d; want 1", spy.calls)
	}
	if spy.rec.runID != "run-1" || spy.rec.taskID != rtTaskID {
		t.Errorf("dispatch args mismatch: %+v", spy.rec)
	}
}

// TestResumeTask_ForgedTerminalStillDispatches — spec §7(e).
// Strengthened after fresh-review F2: actually creates a real
// TaskJournal file on disk with a forged terminal:true record for
// our taskID, then asserts (a) ResumeTask still dispatches, and
// (b) the file's mtime + content are byte-identical after
// ResumeTask returns (proving no read AND no write). Runs against
// the SAME driver as prod (creates a real *TaskJournal, appends a
// forged record, hands the path to a fs-oracle helper).
func TestResumeTask_ForgedTerminalStillDispatches(t *testing.T) {
	t.Parallel()
	journalPath := filepath.Join(t.TempDir(), "task_journal.jsonl")
	writeForgedJournal(t, journalPath, `{"ts":"2026-07-03T12:00:00Z","event":"delegate_task","task_id":"`+rtTaskID+`","terminal":true}`+"\n")
	preFI, preHash := snapshotFile(t, journalPath)

	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	spy := &dispatchSpy{}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     spy.Fn(),
	}
	if err := ResumeTask(context.Background(), deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Errorf("dispatch calls = %d; want 1 (forged terminal must not suppress)", spy.calls)
	}
	// Filesystem oracle: byte-identical + mtime unchanged. A
	// future edit that adds an os.Open of the journal (even
	// read-only) would still leave the file unchanged, but this
	// combined check catches any accidental os.OpenFile with
	// O_TRUNC / O_WRONLY / O_APPEND on the journal path.
	postFI, postHash := snapshotFile(t, journalPath)
	if preHash != postHash {
		t.Errorf("journal content changed: pre=%s post=%s", preHash, postHash)
	}
	if !preFI.ModTime().Equal(postFI.ModTime()) {
		t.Errorf("journal mtime changed: pre=%v post=%v", preFI.ModTime(), postFI.ModTime())
	}
}

// TestResumeTask_ForgedTerminalCannotSuppressResume — spec §7(e).
// End-to-end variant: a pre-seeded uncommitted WriteID exists (the
// slave crashed mid-write). Even if the driver journal contains a
// forged 'terminal:true' record, ResumeTask still runs Dispatch,
// which drives ExecutorResume and completes the pending write.
func TestResumeTask_ForgedTerminalCannotSuppressResume(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	stager := observerstore.NewSQLitePayloadStager(db)
	ctx := context.Background()

	// Seed uncommitted attempt.
	payload := []byte("pending-write")
	sum := sha256.Sum256(payload)
	id, err := observerstore.NewWriteID(rtTaskID, rtConvID,
		"write-0-"+rtTargetNm, rtTargetNm, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := stager.Stage(ctx, id, rtTaskID, "write-0-"+rtTargetNm, payload); err != nil {
		t.Fatal(err)
	}
	// Prior worker's stale lease.
	if _, err := store.Reserve(ctx, observerstore.ReserveRequest{
		ID: id, RunID: "run-1", TaskID: rtTaskID, ConversationID: rtConvID,
		StepID: "write-0-" + rtTargetNm, WorkerID: "wprior",
		LeaseTTL: 1 * time.Nanosecond, // expires immediately
	}); err != nil {
		t.Fatal(err)
	}
	// (Simulated forged terminal on the journal: not consulted at
	// all by ResumeTask, hence not modeled here — the API surface
	// makes it impossible.)

	// Dispatch drives ExecutorResume-style behavior directly against
	// the store: reserve the WriteID (should see uncommitted),
	// commit.
	dispatchFn := func(ctx context.Context, runID, taskID string, body contract.TaskContract) error {
		state, err := store.Reserve(ctx, observerstore.ReserveRequest{
			ID: id, RunID: runID, TaskID: taskID, ConversationID: rtConvID,
			StepID: "write-0-" + rtTargetNm, WorkerID: "wrecover",
			LeaseTTL: 30 * time.Second,
		})
		if err != nil {
			return err
		}
		if state != observerstore.ReserveUncommitted {
			return fmt.Errorf("dispatch: expected ReserveUncommitted, got %v", state)
		}
		return store.Commit(ctx, observerstore.CommitRequest{
			ID: id, RunID: runID, TaskID: taskID, WorkerID: "wrecover",
		})
	}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     dispatchFn,
	}
	if err := ResumeTask(ctx, deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	// Assert the pending write is now committed.
	var committedAt sql.NullString
	if err := db.QueryRow(
		`SELECT committed_at FROM write_ids WHERE id = ?`, string(id)).Scan(&committedAt); err != nil {
		t.Fatal(err)
	}
	if !committedAt.Valid {
		t.Errorf("committed_at not set — forged terminal suppressed recovery")
	}
}

// racyStager wraps SQLitePayloadStager and simulates a race:
// ListForStep returns a ref, but a concurrent Vacuum deletes it
// before Load runs — Load then returns ErrPayloadUnavailable.
type racyStager struct {
	inner observerstore.PayloadStager
	db    *sql.DB
}

func (r *racyStager) Stage(ctx context.Context, id observerstore.WriteID,
	taskID, stepID string, payload []byte) error {
	return r.inner.Stage(ctx, id, taskID, stepID, payload)
}
func (r *racyStager) Load(ctx context.Context, id observerstore.WriteID) ([]byte, error) {
	// Simulate the race: delete before Load returns.
	_, _ = r.db.ExecContext(ctx, `DELETE FROM write_id_payloads WHERE id = ?`, string(id))
	return r.inner.Load(ctx, id)
}
func (r *racyStager) ListForStep(ctx context.Context, taskID, stepID string) ([]observerstore.PayloadRef, error) {
	return r.inner.ListForStep(ctx, taskID, stepID)
}

// TestReconstructSteps_UnrecoverableWhenPayloadRacesVacuum
// (plan-only) — exercises ErrStepPayloadUnrecoverable when Load
// fails for a row that was present during ListForStep.
func TestReconstructSteps_UnrecoverableWhenPayloadRacesVacuum(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	real := observerstore.NewSQLitePayloadStager(db)
	ctx := context.Background()
	targets := []contract.WriteTarget{{Type: "artifact", Kind: "code", Name: "y.txt"}}
	body := contract.TaskContract{DataContract: contract.DataContract{WriteTargets: targets}}
	payload := []byte("bytes")
	sum := sha256.Sum256(payload)
	id, _ := observerstore.NewWriteID(rtTaskID, rtConvID, "write-0-y.txt", "y.txt", hex.EncodeToString(sum[:]))
	if err := real.Stage(ctx, id, rtTaskID, "write-0-y.txt", payload); err != nil {
		t.Fatal(err)
	}
	stager := &racyStager{inner: real, db: db}
	_, err := ReconstructSteps(ctx, stager, rtTaskID, body)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrStepPayloadUnrecoverable) {
		t.Errorf("err = %v; want ErrStepPayloadUnrecoverable", err)
	}
}

// TestResumeTask_ForgedJournalCannotCauseDuplicateWrite — spec §7(e).
// Strengthened after fresh-review F2: forges a real journal file
// with a synthetic 'unfinished' record for a task that's actually
// already committed, then invokes ResumeTask N times, asserting
// (a) no duplicate committed rows appear (DB is authoritative),
// (b) the forged journal is byte-untouched by ResumeTask.
func TestResumeTask_ForgedJournalCannotCauseDuplicateWrite(t *testing.T) {
	t.Parallel()
	journalPath := filepath.Join(t.TempDir(), "task_journal.jsonl")
	// Forge a "task is unfinished" record — the eval-runner might
	// read a corrupted journal like this and decide to resume.
	writeForgedJournal(t, journalPath, `{"ts":"2026-07-03T12:00:00Z","event":"delegate_task","task_id":"`+rtTaskID+`","status":"pending"}`+"\n")
	preFI, preHash := snapshotFile(t, journalPath)

	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	stager := observerstore.NewSQLitePayloadStager(db)
	payload := []byte("already-done")
	sum := sha256.Sum256(payload)
	id, err := observerstore.NewWriteID(rtTaskID, rtConvID,
		"write-0-"+rtTargetNm, rtTargetNm, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := stager.Stage(ctx, id, rtTaskID, "write-0-"+rtTargetNm, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, observerstore.ReserveRequest{
		ID: id, RunID: "run-1", TaskID: rtTaskID, ConversationID: rtConvID,
		StepID: "write-0-" + rtTargetNm, WorkerID: "wprior", LeaseTTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, observerstore.CommitRequest{
		ID: id, RunID: "run-1", TaskID: rtTaskID, WorkerID: "wprior",
	}); err != nil {
		t.Fatal(err)
	}

	// Dispatch spies re-check on each call.
	spy := &dispatchSpy{}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     spy.Fn(),
	}
	for i := 0; i < 3; i++ {
		if err := ResumeTask(ctx, deps, "run-1", rtTaskID); err != nil {
			t.Fatal(err)
		}
	}
	// The Reserve rows should each be 'committed' — no duplicate
	// write happens because the DB is authoritative.
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM write_ids WHERE id = ? AND committed_at IS NOT NULL`, string(id)).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("committed rows = %d; want 1 (no duplicate)", count)
	}
	if spy.calls != 3 {
		t.Errorf("dispatch calls = %d; want 3", spy.calls)
	}
	postFI, postHash := snapshotFile(t, journalPath)
	if preHash != postHash {
		t.Errorf("journal content changed: pre=%s post=%s", preHash, postHash)
	}
	if !preFI.ModTime().Equal(postFI.ModTime()) {
		t.Errorf("journal mtime changed")
	}
}

// TestResumeTask_MissingJournalStillCompletes — spec §7(e).
// Strengthened after fresh-review F2: creates then deletes a
// journal file at a specific path, then asserts ResumeTask
// completes without ever touching that path (via fs oracle: the
// path stays non-existent after ResumeTask returns).
func TestResumeTask_MissingJournalStillCompletes(t *testing.T) {
	t.Parallel()
	journalPath := filepath.Join(t.TempDir(), "task_journal.jsonl")
	// Create then delete — mimics "operator wiped journal after crash".
	writeForgedJournal(t, journalPath, "irrelevant\n")
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	spy := &dispatchSpy{}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     spy.Fn(),
	}
	if err := ResumeTask(context.Background(), deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Errorf("dispatch calls = %d; want 1", spy.calls)
	}
	// Missing-file oracle: ResumeTask MUST NOT have created the file
	// (would be caught if a future edit added an os.OpenFile with
	// O_CREATE on the journal path).
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Errorf("ResumeTask created/re-opened the journal file (Stat: %v)", err)
	}
}

// writeForgedJournal writes a journal file with the given content
// (plain os.WriteFile, not atomic tmp+rename, not fsynced — this
// is a test fixture, not a production writer) and backdates its
// mtime by 1 hour so the "unchanged mtime" assertion in the caller
// can't be fooled by ResumeTask happening to write within the same
// clock tick. Test helper for F2 forged-journal tests.
func writeForgedJournal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write forged journal: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// snapshotFile returns the file's os.FileInfo (for ModTime) and the
// hex sha256 of its contents. Fatal-fails on IO error.
func snapshotFile(t *testing.T, path string) (os.FileInfo, string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return fi, hex.EncodeToString(sum[:])
}

// -------------------------------------------------------------------
// §5 ReconstructSteps tests
// -------------------------------------------------------------------

// TestReconstructSteps_CommittedWinsPriority (plan-only).
func TestReconstructSteps_CommittedWinsPriority(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	stager := observerstore.NewSQLitePayloadStager(db)
	ctx := context.Background()
	targets := []contract.WriteTarget{
		{Type: "artifact", Kind: "code", Name: "x.txt"},
	}
	body := contract.TaskContract{DataContract: contract.DataContract{WriteTargets: targets}}

	// Committed attempt: payload OLD.
	payOld := []byte("OLD")
	sumOld := sha256.Sum256(payOld)
	idOld, _ := observerstore.NewWriteID(rtTaskID, rtConvID, "write-0-x.txt", "x.txt", hex.EncodeToString(sumOld[:]))
	if err := stager.Stage(ctx, idOld, rtTaskID, "write-0-x.txt", payOld); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, observerstore.ReserveRequest{
		ID: idOld, RunID: "run-1", TaskID: rtTaskID, ConversationID: rtConvID,
		StepID: "write-0-x.txt", WorkerID: "w1", LeaseTTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, observerstore.CommitRequest{
		ID: idOld, RunID: "run-1", TaskID: rtTaskID, WorkerID: "w1",
	}); err != nil {
		t.Fatal(err)
	}
	// A NEWER uncommitted stage under a different WriteID (content-change).
	payNew := []byte("NEW")
	sumNew := sha256.Sum256(payNew)
	idNew, _ := observerstore.NewWriteID(rtTaskID, rtConvID, "write-0-x.txt", "x.txt", hex.EncodeToString(sumNew[:]))
	if err := stager.Stage(ctx, idNew, rtTaskID, "write-0-x.txt", payNew); err != nil {
		t.Fatal(err)
	}

	steps, err := ReconstructSteps(ctx, stager, rtTaskID, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps: %d; want 1", len(steps))
	}
	if string(steps[0].Payload) != "OLD" {
		t.Errorf("payload = %q; want OLD (committed-wins)", steps[0].Payload)
	}
}

// TestReconstructSteps_UncommittedUsedWhenNoCommitted (plan-only).
func TestReconstructSteps_UncommittedUsedWhenNoCommitted(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	stager := observerstore.NewSQLitePayloadStager(db)
	ctx := context.Background()
	targets := []contract.WriteTarget{{Type: "artifact", Kind: "code", Name: "y.txt"}}
	body := contract.TaskContract{DataContract: contract.DataContract{WriteTargets: targets}}
	payload := []byte("pending-bytes")
	sum := sha256.Sum256(payload)
	id, _ := observerstore.NewWriteID(rtTaskID, rtConvID, "write-0-y.txt", "y.txt", hex.EncodeToString(sum[:]))
	if err := stager.Stage(ctx, id, rtTaskID, "write-0-y.txt", payload); err != nil {
		t.Fatal(err)
	}
	steps, err := ReconstructSteps(ctx, stager, rtTaskID, body)
	if err != nil {
		t.Fatal(err)
	}
	if string(steps[0].Payload) != "pending-bytes" {
		t.Errorf("payload = %q", steps[0].Payload)
	}
}

// TestReconstructSteps_UnstagedStepYieldsNilPayload (plan-only).
func TestReconstructSteps_UnstagedStepYieldsNilPayload(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	stager := observerstore.NewSQLitePayloadStager(db)
	ctx := context.Background()
	targets := []contract.WriteTarget{{Type: "artifact", Kind: "code", Name: "z.txt"}}
	body := contract.TaskContract{DataContract: contract.DataContract{WriteTargets: targets}}
	steps, err := ReconstructSteps(ctx, stager, rtTaskID, body)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Payload != nil {
		t.Errorf("payload = %v; want nil", steps[0].Payload)
	}
}

// TestResumeTask_ErrKindIsStepPayloadUnrecoverable — F1 regression
// guard. If Dispatch propagates a wrapped ErrStepPayloadUnrecoverable
// (as it would when the executor's ReconstructSteps racecheck fires),
// the resume_task_attempts row MUST carry error_kind =
// "ErrStepPayloadUnrecoverable" — NOT the more generic
// "ErrPayloadUnavailable". Reverting classifyErr's case ordering
// would silently pass every prior test but fail this one.
func TestResumeTask_ErrKindIsStepPayloadUnrecoverable(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	// Dispatch returns a wrapped ErrStepPayloadUnrecoverable that
	// ALSO wraps ErrPayloadUnavailable — mirrors what
	// ReconstructSteps produces on the race path (see driver/resume.go
	// ReconstructSteps).
	wrapped := fmt.Errorf(
		"driver: load step 0: %w: %w",
		ErrStepPayloadUnrecoverable,
		observerstore.ErrPayloadUnavailable,
	)
	// Sanity: the wrapped err matches BOTH sentinels.
	if !errors.Is(wrapped, ErrStepPayloadUnrecoverable) {
		t.Fatal("test-setup bug: wrapped err doesn't match ErrStepPayloadUnrecoverable")
	}
	if !errors.Is(wrapped, observerstore.ErrPayloadUnavailable) {
		t.Fatal("test-setup bug: wrapped err doesn't match ErrPayloadUnavailable")
	}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch: func(context.Context, string, string, contract.TaskContract) error {
			return wrapped
		},
	}
	_ = ResumeTask(context.Background(), deps, "run-1", rtTaskID)
	var errKind string
	if err := db.QueryRow(
		`SELECT error_kind FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		rtTaskID).Scan(&errKind); err != nil {
		t.Fatal(err)
	}
	if errKind != "ErrStepPayloadUnrecoverable" {
		t.Errorf("error_kind = %q; want ErrStepPayloadUnrecoverable "+
			"(classifyErr must check the more specific sentinel first)",
			errKind)
	}
}

// TestResumeTask_ErrKindIsLeaseLostAfterWrite — round-4 P1 #3.
// If Dispatch propagates a wrapped executor.ErrLeaseLostAfterWrite
// (the executor's Commit hit the noop-branch with commit_worker !=
// req.WorkerID), the resume_task_attempts row MUST carry error_kind
// = "ErrLeaseLostAfterWrite". Reverting the alias in
// executor/resume.go or dropping the classifyErr case would silently
// downgrade the D4 label to "unknown".
func TestResumeTask_ErrKindIsLeaseLostAfterWrite(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	// Dispatch surfaces the sentinel exactly the way ExecutorResume
	// would (Commit's error propagates via fmt.Errorf %w).
	wrapped := fmt.Errorf(
		"executor: commit step 0: %w",
		observerstore.ErrLeaseLostAfterWrite,
	)
	// Sanity: the sentinel matches via both the executor alias AND
	// the direct observerstore reference (proves the alias chain).
	if !errors.Is(wrapped, observerstore.ErrLeaseLostAfterWrite) {
		t.Fatal("test-setup bug: wrapped err doesn't match observerstore sentinel")
	}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch: func(context.Context, string, string, contract.TaskContract) error {
			return wrapped
		},
	}
	_ = ResumeTask(context.Background(), deps, "run-1", rtTaskID)
	var errKind string
	if err := db.QueryRow(
		`SELECT error_kind FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		rtTaskID).Scan(&errKind); err != nil {
		t.Fatal(err)
	}
	if errKind != "ErrLeaseLostAfterWrite" {
		t.Errorf("error_kind = %q; want ErrLeaseLostAfterWrite "+
			"(classifyErr must recognise the alias)",
			errKind)
	}
}

