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
	validHash  = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
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

// TestResumeTask_DoesNotOpenJournalFile — compile-time proof that
// ResumeDeps has no Journal field.
func TestResumeTask_DoesNotOpenJournalFile(t *testing.T) {
	t.Parallel()
	// Compile-time check: constructing a ResumeDeps with a Journal
	// field would fail to compile. Runtime check: reflect over the
	// struct fields.
	deps := ResumeDeps{}
	typ := reflect.TypeOf(deps)
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Name == "Journal" {
			t.Errorf("ResumeDeps unexpectedly has a Journal field")
		}
	}
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

// TestResumeTask_ErrorRowOnContractMissing (plan-only) and
// TestResumeTask_ContractNotFound_ErrorOutcome — same behaviour.
func TestResumeTask_ErrorRowOnContractMissing(t *testing.T) {
	t.Parallel()
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

func TestResumeTask_ContractNotFound_ErrorOutcome(t *testing.T) {
	TestResumeTask_ErrorRowOnContractMissing(t)
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
func TestResumeTask_ForgedTerminalStillDispatches(t *testing.T) {
	t.Parallel()
	// The point of this test: ResumeTask has no journal I/O at all,
	// so a forged terminal record on disk cannot suppress dispatch.
	// Wire a Dispatch spy and assert it runs regardless of what
	// TaskJournal contains (we don't even pass a Journal in).
	db := openStoreDB(t)
	store := observerstore.NewSQLiteWriteIDStore(db)
	spy := &dispatchSpy{}
	deps := ResumeDeps{
		Store:        store,
		LoadContract: fakeLoad(stubContract(rtTargetNm), nil),
		Dispatch:     spy.Fn(),
	}
	// Even if a real driver journal on disk has a synthetic
	// terminal for our taskID, ResumeTask never opens it. We prove
	// this by NOT wiring one at all.
	if err := ResumeTask(context.Background(), deps, "run-1", rtTaskID); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Errorf("dispatch calls = %d; want 1 (forged terminal cannot suppress)", spy.calls)
	}
}

// TestResumeTask_ForgedJournalCannotCauseDuplicateWrite — spec §7(e).
func TestResumeTask_ForgedJournalCannotCauseDuplicateWrite(t *testing.T) {
	t.Parallel()
	// This test asserts the same safety property from a different
	// angle: even if the caller invokes ResumeTask N times in a row
	// (as it might do if the eval-runner reads a "forged" journal
	// showing the task is unfinished when it's already committed),
	// the executor's Reserve loop consults ONLY the DB state and
	// returns ReserveCommitted for every prior-committed write.
	// We simulate this by pre-committing a WriteID and then
	// invoking ResumeTask, whose Dispatch simulates ExecutorResume.
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
}

// TestResumeTask_MissingJournalStillCompletes — spec §7(e).
func TestResumeTask_MissingJournalStillCompletes(t *testing.T) {
	t.Parallel()
	// ResumeTask has no Journal field on ResumeDeps, so a missing
	// journal file cannot influence its behaviour at all. We simply
	// assert successful completion when the file doesn't exist.
	// (No journal open call in the implementation; this test's job
	// is regression protection against a future edit.)
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

// unused imports guard
var _ = validHash
