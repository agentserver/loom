// Tests for WT-2-task-resume executor pipeline.
// See docs/specs/wt2-task-resume.plan.md §4 for the test matrix.
package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

const (
	testTaskID   = "task_ab12"
	testConvID   = "conv-x"
	testTargetNm = "artifact:foo"
)

// -------------------------------------------------------------------
// Fixtures
// -------------------------------------------------------------------

type resumeFixture struct {
	db      *sql.DB
	store   *observerstore.SQLiteWriteIDStore
	stager  *observerstore.SQLitePayloadStager
	clk     *fakeClock
	targets []contract.WriteTarget
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(at time.Time) *fakeClock { return &fakeClock{t: at.UTC()} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observer.db")
	store, err := observerstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	db := store.DB()
	t.Cleanup(func() {
		_ = db.Close()
		_ = os.Remove(path)
	})
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	return &resumeFixture{
		db:     db,
		store:  observerstore.NewSQLiteWriteIDStore(db, observerstore.WithWriteIDClock(clk.Now)),
		stager: observerstore.NewSQLitePayloadStager(db, observerstore.WithPayloadStagerClock(clk.Now)),
		clk:    clk,
		targets: []contract.WriteTarget{
			{Type: "artifact", Kind: "code", Name: testTargetNm},
		},
	}
}

// spyWriteStep records invocations for later assertions.
type spyWriteStep struct {
	mu    sync.Mutex
	calls []Step
}

func (s *spyWriteStep) Fn() func(context.Context, Step) error {
	return func(_ context.Context, step Step) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, step)
		return nil
	}
}

func (s *spyWriteStep) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func failingWriteStep(err error) func(context.Context, Step) error {
	return func(context.Context, Step) error { return err }
}

func noopFreshExecute(context.Context, Step) error { return nil }

func buildDeps(fx *resumeFixture, workerID string,
	writeStep func(context.Context, Step) error,
	freshExecute func(context.Context, Step) error) ExecutorDeps {
	return ExecutorDeps{
		Store:         fx.store,
		PayloadStager: fx.stager,
		WorkerID:      workerID,
		LeaseTTL:      30 * time.Second,
		WriteStep:     writeStep,
		FreshExecute:  freshExecute,
	}
}

func makeRequest(fx *resumeFixture, steps []Step) ResumeRequest {
	return ResumeRequest{
		TaskID:         testTaskID,
		RunID:          "run-1",
		ConversationID: testConvID,
		Contract: contract.TaskContract{
			Version:        contract.Version,
			ConversationID: testConvID,
			DataContract: contract.DataContract{
				WriteTargets: fx.targets,
			},
		},
		Steps: steps,
	}
}

// -------------------------------------------------------------------
// §4 Test matrix
// -------------------------------------------------------------------

// TestExecutorResume_HappyPathFresh (plan-only).
func TestExecutorResume_HappyPathFresh(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	spy := &spyWriteStep{}
	deps := buildDeps(fx, "w1", spy.Fn(), noopFreshExecute)
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("hello")},
	})
	if err := ExecutorResume(context.Background(), deps, req); err != nil {
		t.Fatalf("ExecutorResume: %v", err)
	}
	if spy.Count() != 1 {
		t.Errorf("WriteStep calls = %d; want 1", spy.Count())
	}
	// One write_ids row + committed_at populated.
	var committedAt sql.NullString
	if err := fx.db.QueryRow(`SELECT committed_at FROM write_ids`).Scan(&committedAt); err != nil {
		t.Fatal(err)
	}
	if !committedAt.Valid {
		t.Errorf("committed_at not set")
	}
}

// TestExecutorResume_CrashAfterReserveReplaysFromStagedPayload.
func TestExecutorResume_CrashAfterReserveReplaysFromStagedPayload(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	// First attempt: WriteStep returns error so no Commit happens.
	spy1 := &spyWriteStep{}
	deps1 := buildDeps(fx, "w1-crashed",
		func(ctx context.Context, s Step) error {
			spy1.mu.Lock()
			spy1.calls = append(spy1.calls, s)
			spy1.mu.Unlock()
			return errors.New("simulated crash before commit")
		}, noopFreshExecute)
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("payload-A")},
	})
	if err := ExecutorResume(context.Background(), deps1, req); err == nil {
		t.Fatal("first ExecutorResume: expected error")
	}
	if spy1.Count() != 1 {
		t.Fatalf("first spy: %d; want 1", spy1.Count())
	}
	// Fast-forward past lease TTL so a fresh worker can steal.
	fx.clk.Advance(2 * time.Minute)
	// Second attempt: same Step.Payload, fresh WorkerID, WriteStep OK.
	spy2 := &spyWriteStep{}
	deps2 := buildDeps(fx, "w2-recovered", spy2.Fn(), noopFreshExecute)
	if err := ExecutorResume(context.Background(), deps2, req); err != nil {
		t.Fatalf("second ExecutorResume: %v", err)
	}
	if spy2.Count() != 1 {
		t.Errorf("second spy: %d; want 1", spy2.Count())
	}
	// The write_ids row should be committed now.
	var committedAt sql.NullString
	if err := fx.db.QueryRow(`SELECT committed_at FROM write_ids`).Scan(&committedAt); err != nil {
		t.Fatal(err)
	}
	if !committedAt.Valid {
		t.Errorf("committed_at not set after recovery")
	}
}

// TestExecutorResume_ContentChangeYieldsFreshWriteID.
func TestExecutorResume_ContentChangeYieldsFreshWriteID(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)

	// First attempt: crashes with payload A.
	spyFail := &spyWriteStep{}
	depsFail := buildDeps(fx, "w1",
		func(ctx context.Context, s Step) error {
			spyFail.mu.Lock()
			spyFail.calls = append(spyFail.calls, s)
			spyFail.mu.Unlock()
			return errors.New("crash")
		}, noopFreshExecute)
	reqA := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("A")},
	})
	if err := ExecutorResume(context.Background(), depsFail, reqA); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	fx.clk.Advance(2 * time.Minute)

	// Second attempt: DIFFERENT payload B → distinct WriteID → fresh.
	spyOK := &spyWriteStep{}
	depsOK := buildDeps(fx, "w2", spyOK.Fn(), noopFreshExecute)
	reqB := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("B")},
	})
	if err := ExecutorResume(context.Background(), depsOK, reqB); err != nil {
		t.Fatal(err)
	}
	if spyOK.Count() != 1 {
		t.Errorf("spyOK count: %d; want 1", spyOK.Count())
	}

	// write_ids should now have 2 rows: uncommitted A + committed B.
	var count int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM write_ids`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("write_ids count = %d; want 2", count)
	}
}

// TestExecutorResume_SkipsWhenReserveCommitted.
func TestExecutorResume_SkipsWhenReserveCommitted(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	// Seed: first attempt commits.
	spy1 := &spyWriteStep{}
	deps1 := buildDeps(fx, "w1", spy1.Fn(), noopFreshExecute)
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("done")},
	})
	if err := ExecutorResume(context.Background(), deps1, req); err != nil {
		t.Fatal(err)
	}
	// Second invocation with byte-identical payload — should skip.
	spy2 := &spyWriteStep{}
	deps2 := buildDeps(fx, "w2", spy2.Fn(), noopFreshExecute)
	if err := ExecutorResume(context.Background(), deps2, req); err != nil {
		t.Fatal(err)
	}
	if spy2.Count() != 0 {
		t.Errorf("second WriteStep calls = %d; want 0 (already committed)", spy2.Count())
	}
}

// TestExecutorResume_ReserveInFlightReturnsErrConcurrentLease.
func TestExecutorResume_ReserveInFlightReturnsErrConcurrentLease(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)

	// wA reserves and blocks; wB tries to run — should get inflight.
	sum := sha256.Sum256([]byte("payload"))
	id, err := observerstore.NewWriteID(testTaskID, testConvID, "write-0-"+testTargetNm, testTargetNm, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.stager.Stage(context.Background(), id, testTaskID, "write-0-"+testTargetNm, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.store.Reserve(context.Background(), observerstore.ReserveRequest{
		ID:             id,
		RunID:          "run-1",
		TaskID:         testTaskID,
		ConversationID: testConvID,
		StepID:         "write-0-" + testTargetNm,
		WorkerID:       "wA-live",
		LeaseTTL:       30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	// wB tries via ExecutorResume.
	spyB := &spyWriteStep{}
	depsB := buildDeps(fx, "wB", spyB.Fn(), noopFreshExecute)
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("payload")},
	})
	err = ExecutorResume(context.Background(), depsB, req)
	if !errors.Is(err, ErrConcurrentLease) {
		t.Errorf("err = %v; want ErrConcurrentLease", err)
	}
	if spyB.Count() != 0 {
		t.Errorf("WriteStep should not have run; count = %d", spyB.Count())
	}
}

// TestExecutorResume_NilPayloadFallsThroughToFreshExecution.
func TestExecutorResume_NilPayloadFallsThroughToFreshExecution(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	var freshCount atomic.Int32
	freshFn := func(ctx context.Context, s Step) error {
		freshCount.Add(1)
		return nil
	}
	spy := &spyWriteStep{}
	deps := buildDeps(fx, "w1", spy.Fn(), freshFn)
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: nil},
	})
	if err := ExecutorResume(context.Background(), deps, req); err != nil {
		t.Fatal(err)
	}
	if freshCount.Load() != 1 {
		t.Errorf("FreshExecute calls = %d; want 1", freshCount.Load())
	}
	if spy.Count() != 0 {
		t.Errorf("WriteStep should not have run; count = %d", spy.Count())
	}
	// No write_ids row because FreshExecute doesn't touch the store in
	// this fake.
	var count int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM write_ids`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("write_ids rows = %d; want 0", count)
	}
}

// TestExecutorResume_ContentHashComputedBeforeStage (plan-only).
func TestExecutorResume_ContentHashComputedBeforeStage(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	spy := &spyWriteStep{}
	deps := buildDeps(fx, "w1", spy.Fn(), noopFreshExecute)
	payload := []byte("hash-check")
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: payload},
	})
	if err := ExecutorResume(context.Background(), deps, req); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	wantID, err := observerstore.NewWriteID(testTaskID, testConvID,
		"write-0-"+testTargetNm, testTargetNm, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	var gotID string
	if err := fx.db.QueryRow(`SELECT id FROM write_ids`).Scan(&gotID); err != nil {
		t.Fatal(err)
	}
	if gotID != string(wantID) {
		t.Errorf("stored id = %s; want %s (WriteID matches sha256(payload))", gotID, wantID)
	}
}

// TestExecutorResume_MultipleStepsOrdered (plan-only).
func TestExecutorResume_MultipleStepsOrdered(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	targets := []contract.WriteTarget{
		{Type: "artifact", Kind: "code", Name: "a.txt"},
		{Type: "artifact", Kind: "code", Name: "b.txt"},
		{Type: "artifact", Kind: "code", Name: "c.txt"},
	}
	fx.targets = targets
	spy := &spyWriteStep{}
	deps := buildDeps(fx, "w1", spy.Fn(), noopFreshExecute)
	steps := []Step{
		{Index: 0, Target: targets[0], Payload: []byte("A")},
		{Index: 1, Target: targets[1], Payload: []byte("B")},
		{Index: 2, Target: targets[2], Payload: []byte("C")},
	}
	req := makeRequest(fx, steps)
	if err := ExecutorResume(context.Background(), deps, req); err != nil {
		t.Fatal(err)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.calls) != 3 {
		t.Fatalf("calls = %d; want 3", len(spy.calls))
	}
	for i, c := range spy.calls {
		if c.Index != i {
			t.Errorf("call[%d].Index = %d; want %d", i, c.Index, i)
		}
	}
}

// TestExecutorResume_EmptyStepsReturnsNil (plan-only).
func TestExecutorResume_EmptyStepsReturnsNil(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	spy := &spyWriteStep{}
	deps := buildDeps(fx, "w1", spy.Fn(), noopFreshExecute)
	req := makeRequest(fx, nil)
	if err := ExecutorResume(context.Background(), deps, req); err != nil {
		t.Fatal(err)
	}
	if spy.Count() != 0 {
		t.Errorf("WriteStep calls = %d; want 0", spy.Count())
	}
}

// TestFirstRunWriteGoesThroughGate — spec §6.1c named test.
func TestFirstRunWriteGoesThroughGate(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	spy := &spyWriteStep{}
	deps := buildDeps(fx, "w1", spy.Fn(), noopFreshExecute)
	payload := []byte("first-run-bytes")
	req := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: payload},
	})
	if err := ExecutorResume(context.Background(), deps, req); err != nil {
		t.Fatal(err)
	}
	// Assert the pipeline traversed: Stage → Reserve → WriteStep → Commit.
	// Stage: write_id_payloads row exists.
	var payloadCount int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM write_id_payloads`).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 1 {
		t.Errorf("write_id_payloads = %d; want 1", payloadCount)
	}
	// Reserve emitted one 'fresh' audit row.
	var freshCount int
	if err := fx.db.QueryRow(
		`SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'fresh'`).Scan(&freshCount); err != nil {
		t.Fatal(err)
	}
	if freshCount != 1 {
		t.Errorf("fresh audit rows = %d; want 1", freshCount)
	}
	// WriteStep ran once.
	if spy.Count() != 1 {
		t.Errorf("WriteStep count = %d; want 1", spy.Count())
	}
	// Commit emitted one audit row + committed_at populated.
	var commitCount int
	if err := fx.db.QueryRow(
		`SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'commit'`).Scan(&commitCount); err != nil {
		t.Fatal(err)
	}
	if commitCount != 1 {
		t.Errorf("commit audit rows = %d; want 1", commitCount)
	}
}

// TestExecutorResume_ContentChangeRewrites — spec §7(a) named test.
func TestExecutorResume_ContentChangeRewrites(t *testing.T) {
	t.Parallel()
	fx := newResumeFixture(t)
	// First iteration: Payload=A → commits under WriteID_A.
	spyA := &spyWriteStep{}
	depsA := buildDeps(fx, "wA", spyA.Fn(), noopFreshExecute)
	reqA := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("A")},
	})
	if err := ExecutorResume(context.Background(), depsA, reqA); err != nil {
		t.Fatal(err)
	}
	// Second iteration: Payload=B, fresh — different WriteID → fresh
	// reserve, WriteStep runs, committed.
	spyB := &spyWriteStep{}
	depsB := buildDeps(fx, "wB", spyB.Fn(), noopFreshExecute)
	reqB := makeRequest(fx, []Step{
		{Index: 0, Target: fx.targets[0], Payload: []byte("B")},
	})
	if err := ExecutorResume(context.Background(), depsB, reqB); err != nil {
		t.Fatal(err)
	}
	if spyA.Count() != 1 || spyB.Count() != 1 {
		t.Errorf("spyA/B = %d/%d; want 1/1", spyA.Count(), spyB.Count())
	}
	// Two write_ids rows, both committed.
	var rows int
	if err := fx.db.QueryRow(
		`SELECT COUNT(*) FROM write_ids WHERE committed_at IS NOT NULL`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("committed write_ids = %d; want 2", rows)
	}
}

// TestExecutorResume_ConcurrentLiveWorkersRunWriteStepOnce (plan-only).
func TestExecutorResume_ConcurrentLiveWorkersRunWriteStepOnce(t *testing.T) {
	t.Run("committed_wins", func(t *testing.T) {
		t.Parallel()
		fx := newResumeFixture(t)
		payload := []byte("shared")
		// WriteStep serialises via a channel so we can control who
		// "wins" the race for the first attempt.
		hold := make(chan struct{})
		release := make(chan struct{})
		var slowSpy spyWriteStep
		slowFn := func(ctx context.Context, s Step) error {
			slowSpy.mu.Lock()
			slowSpy.calls = append(slowSpy.calls, s)
			slowSpy.mu.Unlock()
			close(hold)
			<-release
			return nil
		}
		var fastSpy spyWriteStep
		depsA := buildDeps(fx, "wA-slow", slowFn, noopFreshExecute)
		depsB := buildDeps(fx, "wB-fast", fastSpy.Fn(), noopFreshExecute)
		req := makeRequest(fx, []Step{
			{Index: 0, Target: fx.targets[0], Payload: payload},
		})

		errA := make(chan error, 1)
		errB := make(chan error, 1)
		go func() { errA <- ExecutorResume(context.Background(), depsA, req) }()
		<-hold // wA is inside WriteStep
		go func() { errB <- ExecutorResume(context.Background(), depsB, req) }()
		// Give wB a moment to hit the Reserve→InFlight path.
		time.Sleep(50 * time.Millisecond)
		close(release) // let wA commit
		if err := <-errA; err != nil {
			t.Errorf("wA: %v", err)
		}
		bErr := <-errB
		if !errors.Is(bErr, ErrConcurrentLease) {
			t.Errorf("wB: %v; want ErrConcurrentLease", bErr)
		}
		if slowSpy.Count() != 1 {
			t.Errorf("slowSpy: %d; want 1", slowSpy.Count())
		}
		if fastSpy.Count() != 0 {
			t.Errorf("fastSpy: %d; want 0 (loser must not WriteStep)", fastSpy.Count())
		}
		// Third invocation after both return — should skip.
		spyC := &spyWriteStep{}
		depsC := buildDeps(fx, "wC", spyC.Fn(), noopFreshExecute)
		if err := ExecutorResume(context.Background(), depsC, req); err != nil {
			t.Fatal(err)
		}
		if spyC.Count() != 0 {
			t.Errorf("wC (post-commit): %d; want 0", spyC.Count())
		}
	})
	t.Run("winner_crashed_before_commit", func(t *testing.T) {
		t.Parallel()
		fx := newResumeFixture(t)
		payload := []byte("crashed-payload")
		spy1 := &spyWriteStep{}
		deps1 := buildDeps(fx, "w1-crash",
			func(ctx context.Context, s Step) error {
				spy1.mu.Lock()
				spy1.calls = append(spy1.calls, s)
				spy1.mu.Unlock()
				return errors.New("simulated crash")
			}, noopFreshExecute)
		req := makeRequest(fx, []Step{
			{Index: 0, Target: fx.targets[0], Payload: payload},
		})
		if err := ExecutorResume(context.Background(), deps1, req); err == nil {
			t.Fatal("expected crash error")
		}
		// Advance past lease.
		fx.clk.Advance(2 * time.Minute)
		// Fresh worker.
		spy2 := &spyWriteStep{}
		deps2 := buildDeps(fx, "w2-recover", spy2.Fn(), noopFreshExecute)
		if err := ExecutorResume(context.Background(), deps2, req); err != nil {
			t.Fatal(err)
		}
		if spy2.Count() != 1 {
			t.Errorf("recovery WriteStep count = %d; want 1", spy2.Count())
		}
	})
}

// -------------------------------------------------------------------
// Constructor validation
// -------------------------------------------------------------------

func TestExecutorResume_RejectsNilDeps(t *testing.T) {
	t.Parallel()
	cases := []ExecutorDeps{
		{}, // everything nil/zero
	}
	for i, deps := range cases {
		if err := ExecutorResume(context.Background(), deps, ResumeRequest{
			TaskID: testTaskID, RunID: "r", ConversationID: testConvID,
		}); !errors.Is(err, ErrInvalidExecutorDeps) {
			t.Errorf("case %d: err = %v; want ErrInvalidExecutorDeps", i, err)
		}
	}
}

// -------------------------------------------------------------------
// unused: silence "declared but not used" for fmt if none imported
// -------------------------------------------------------------------
var _ = fmt.Sprintf
