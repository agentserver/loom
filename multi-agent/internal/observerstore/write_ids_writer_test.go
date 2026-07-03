// Tests for WT-2-task-resume primitives.
// See docs/specs/wt2-task-resume.plan.md §3 for the test matrix.
//
// NOTE: tests that swap the package-level `nowFn` MUST NOT call
// t.Parallel(); they share global state. Each such test uses
// t.Cleanup to restore the original clock.
package observerstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// -------------------------------------------------------------------
// Fixtures
// -------------------------------------------------------------------

const validTaskID = "task_ab12"           // 9 chars, satisfies regex
const validTaskID2 = "task-cd34"          // distinct valid task_id
const validConvID = "conv-x"
const validStepID = "write-0-foo"
const validTargetPath = "artifact:foo"
const validHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
const altHash = "1111111111111111111111111111111111111111111111111111111111111111"

func mustNewWriteID(t *testing.T, taskID, cid, sid, tp, ch string) WriteID {
	t.Helper()
	id, err := NewWriteID(taskID, cid, sid, tp, ch)
	if err != nil {
		t.Fatalf("NewWriteID: %v", err)
	}
	return id
}

// openStoreDB opens a fresh on-disk SQLite for a test and returns
// its *sql.DB (plus a t.Cleanup to close it). Uses t.TempDir so the
// file goes away automatically.
func openStoreDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observer.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = store.db.Close()
		_ = os.Remove(path)
	})
	return store.db
}

func newTestStore(t *testing.T) *SQLiteWriteIDStore {
	t.Helper()
	return NewSQLiteWriteIDStore(openStoreDB(t))
}

// fakeClock is a per-store clock that can be advanced by tests.
// Safe for concurrent use by store methods reading its time.
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

// newTestStoreAt returns a store whose clock is `clk.Now`. Also
// returns a paired PayloadStager sharing the same clock — many
// tests need both.
func newTestStoreAt(t *testing.T, clk *fakeClock) (*SQLiteWriteIDStore, *SQLitePayloadStager) {
	t.Helper()
	db := openStoreDB(t)
	store := NewSQLiteWriteIDStore(db, WithWriteIDClock(clk.Now))
	ps := NewSQLitePayloadStager(db, WithPayloadStagerClock(clk.Now))
	return store, ps
}

// countRows returns the total row count of a table.
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// -------------------------------------------------------------------
// §3.1 WriteID derivation
// -------------------------------------------------------------------

// TestNewWriteID_HappyPath (plan-only) — matches the reference
// derivation formula.
func TestNewWriteID_HappyPath(t *testing.T) {
	t.Parallel()
	id, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if err != nil {
		t.Fatalf("NewWriteID: %v", err)
	}
	// Re-derive in the test to guard the formula, not a magic constant.
	h := sha256.New()
	writeLPTest(h, validTaskID)
	writeLPTest(h, validConvID)
	writeLPTest(h, validStepID)
	writeLPTest(h, validTargetPath)
	writeLPTest(h, validHash)
	want := hex.EncodeToString(h.Sum(nil))
	if string(id) != want {
		t.Errorf("id = %s; want %s", id, want)
	}
}

func writeLPTest(h interface{ Write([]byte) (int, error) }, s string) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(s)))
	_, _ = h.Write(buf[:n])
	_, _ = h.Write([]byte(s))
}

func TestNewWriteID_ContentChangeYieldsDistinctID(t *testing.T) {
	t.Parallel()
	a := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	b := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, altHash)
	if a == b {
		t.Errorf("content change did not yield distinct WriteID: %s", a)
	}
}

// TestNewWriteID_TaskIDChangeYieldsDistinctID (plan-only).
func TestNewWriteID_TaskIDChangeYieldsDistinctID(t *testing.T) {
	t.Parallel()
	a := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	b := mustNewWriteID(t, validTaskID2, validConvID, validStepID, validTargetPath, validHash)
	if a == b {
		t.Errorf("task_id change did not yield distinct WriteID: %s", a)
	}
}

func TestNewWriteID_EmptyContentHashRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, ""); !errors.Is(err, ErrEmptyWriteIDComponent) {
		t.Errorf("err = %v; want ErrEmptyWriteIDComponent", err)
	}
}

func TestNewWriteID_PlaceholderContentHashRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, "placeholder"); !errors.Is(err, ErrInvalidContentHash) {
		t.Errorf("err = %v; want ErrInvalidContentHash", err)
	}
}

func TestNewWriteID_ShortContentHashRejected(t *testing.T) {
	t.Parallel()
	short := validHash[:63]
	if _, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, short); !errors.Is(err, ErrInvalidContentHash) {
		t.Errorf("err = %v; want ErrInvalidContentHash", err)
	}
}

func TestNewWriteID_UppercaseContentHashRejected(t *testing.T) {
	t.Parallel()
	up := strings.ToUpper(validHash)
	if _, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, up); !errors.Is(err, ErrInvalidContentHash) {
		t.Errorf("err = %v; want ErrInvalidContentHash", err)
	}
}

func TestNewWriteID_NonHexContentHashRejected(t *testing.T) {
	t.Parallel()
	bad := "g" + validHash[1:]
	if _, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, bad); !errors.Is(err, ErrInvalidContentHash) {
		t.Errorf("err = %v; want ErrInvalidContentHash", err)
	}
}

// TestNewWriteID_EmptyTaskIDRejected (plan-only) — empty string
// short-circuits into ErrEmptyWriteIDComponent (spec §3
// "any of the five inputs empty").
func TestNewWriteID_EmptyTaskIDRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewWriteID("", validConvID, validStepID, validTargetPath, validHash); !errors.Is(err, ErrEmptyWriteIDComponent) {
		t.Errorf("err = %v; want ErrEmptyWriteIDComponent", err)
	}
}

// TestNewWriteID_InvalidTaskIDFormRejected (plan-only).
func TestNewWriteID_InvalidTaskIDFormRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		taskID  string
		wantErr error
	}{
		{"too_short", "abcd", ErrInvalidTaskIDForm},
		{"too_long", strings.Repeat("a", 129), ErrInvalidTaskIDForm},
		{"sql_meta", "abc'def12", ErrInvalidTaskIDForm},
		{"path_traversal", "../abc/def", ErrInvalidTaskIDForm},
		{"nul", "abcdef\x00gh", ErrInvalidTaskIDForm},
		{"unicode", "café_task", ErrInvalidTaskIDForm},
		{"space", "abc def 12", ErrInvalidTaskIDForm},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewWriteID(tc.taskID, validConvID, validStepID, validTargetPath, validHash)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v; want %v", err, tc.wantErr)
			}
		})
	}
}

// TestNewWriteID_LengthPrefixInjectivityRegression (plan-only).
func TestNewWriteID_LengthPrefixInjectivityRegression(t *testing.T) {
	t.Parallel()
	a := mustNewWriteID(t, validTaskID, "aa", "bb", "cc", validHash)
	b := mustNewWriteID(t, validTaskID, "aab", "b", "cc", validHash)
	if a == b {
		t.Errorf("length-prefix scheme is not injective: %s", a)
	}
}

// -------------------------------------------------------------------
// §3.2 Reserve state machine + audit rows
// -------------------------------------------------------------------

func makeReq(id WriteID, workerID string) ReserveRequest {
	return ReserveRequest{
		ID:             id,
		RunID:          "run-1",
		TaskID:         validTaskID,
		ConversationID: validConvID,
		StepID:         validStepID,
		WorkerID:       workerID,
		LeaseTTL:       30 * time.Second,
	}
}

func TestReserve_FreshOnFirstCall(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	st, err := store.Reserve(ctx, makeReq(id, "w1"))
	if err != nil || st != ReserveFresh {
		t.Fatalf("Reserve: (%v, %v); want (fresh, nil)", st, err)
	}
	if got := countRows(t, store.db, "write_ids"); got != 1 {
		t.Errorf("write_ids rows = %d; want 1", got)
	}
	if got := countRows(t, store.db, "write_id_reserve_events"); got != 1 {
		t.Errorf("write_id_reserve_events rows = %d; want 1", got)
	}
}

// TestReserve_UncommittedAfterCrash — parallel-safe via per-store clock.
func TestReserve_UncommittedAfterCrash(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)

	if st, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil || st != ReserveFresh {
		t.Fatalf("first Reserve: (%v, %v); want (fresh, nil)", st, err)
	}
	clk.Advance(2 * time.Minute)
	st, err := store.Reserve(ctx, makeReq(id, "w2"))
	if err != nil || st != ReserveUncommitted {
		t.Fatalf("second Reserve: (%v, %v); want (uncommitted, nil)", st, err)
	}
}

// TestReserve_UncommittedForSameWorkerBeforeTTL (plan-only) —
// classifier case 4.
func TestReserve_UncommittedForSameWorkerBeforeTTL(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if st, err := store.Reserve(ctx, makeReq(id, "w-same")); err != nil || st != ReserveFresh {
		t.Fatalf("first: %v %v", st, err)
	}
	st, err := store.Reserve(ctx, makeReq(id, "w-same"))
	if err != nil || st != ReserveUncommitted {
		t.Errorf("second: (%v, %v); want (uncommitted, nil)", st, err)
	}
}

// TestReserve_InflightForForeignLive (plan-only).
func TestReserve_InflightForForeignLive(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatalf("first: %v", err)
	}
	st, err := store.Reserve(ctx, makeReq(id, "w2"))
	if err != nil || st != ReserveInFlight {
		t.Errorf("second: (%v, %v); want (inflight, nil)", st, err)
	}
}

func TestReserve_CommittedAfterHappyPath(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	st, err := store.Reserve(ctx, makeReq(id, "w2"))
	if err != nil || st != ReserveCommitted {
		t.Errorf("second reserve: (%v, %v); want (committed, nil)", st, err)
	}
}

// TestReserve_AtomicUnderConcurrency — spec §10 criterion 3
// primitive level.
func TestReserve_AtomicUnderConcurrency(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)

	const N = 8
	var wg sync.WaitGroup
	var freshCount int32
	var inflightCount int32
	var otherCount int32
	var errCount int32
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			workerID := fmt.Sprintf("w%d", i)
			st, err := store.Reserve(ctx, makeReq(id, workerID))
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				return
			}
			switch st {
			case ReserveFresh:
				atomic.AddInt32(&freshCount, 1)
			case ReserveInFlight:
				atomic.AddInt32(&inflightCount, 1)
			default:
				atomic.AddInt32(&otherCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if freshCount != 1 {
		t.Errorf("freshCount = %d; want 1", freshCount)
	}
	if freshCount+inflightCount+otherCount+errCount != N {
		t.Errorf("total mismatch")
	}
	if otherCount != 0 {
		t.Errorf("otherCount = %d; want 0 (concurrent live workers must never see uncommitted/committed)", otherCount)
	}
}

// TestReserve_ConcurrentLiveLeaseYieldsInflight — spec-required
// variant of the concurrency test.
func TestReserve_ConcurrentLiveLeaseYieldsInflight(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)

	if _, err := store.Reserve(ctx, makeReq(id, "wA")); err != nil {
		t.Fatalf("wA: %v", err)
	}
	// wB tries while wA's lease is live.
	st, err := store.Reserve(ctx, makeReq(id, "wB"))
	if err != nil || st != ReserveInFlight {
		t.Errorf("wB: (%v, %v); want (inflight, nil)", st, err)
	}
}

// TestReserve_ExpiredLeaseCanBeStolenOnce.
func TestReserve_ExpiredLeaseCanBeStolenOnce(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "wA")); err != nil {
		t.Fatalf("wA: %v", err)
	}
	clk.Advance(2 * time.Minute)
	// Concurrent steal attempts.
	var wg sync.WaitGroup
	var uncommittedCount int32
	var inflightCount int32
	const N = 4
	start := make(chan struct{})
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			st, err := store.Reserve(ctx, makeReq(id, fmt.Sprintf("w-steal-%d", i)))
			if err != nil {
				return
			}
			if st == ReserveUncommitted {
				atomic.AddInt32(&uncommittedCount, 1)
			} else if st == ReserveInFlight {
				atomic.AddInt32(&inflightCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if uncommittedCount != 1 {
		t.Errorf("exactly one stealer should succeed; got %d", uncommittedCount)
	}
}

// checkAuditFailureRollsBackAll asserts spec §7(g): when the audit
// INSERT inside Reserve's transaction fails (via pre-seeded PK
// conflict on the deterministic event_id), the whole transaction
// rolls back — no write_ids row appears, only the pre-seeded audit
// row remains. Shared between TestReserve_AllStatementsRollBackOnAuditFailure
// and TestReserveAndAuditRollBackTogetherOnDBError so both spec-named
// tests exercise the invariant via their OWN testing.T (avoiding the
// F3 anti-pattern of calling one Test* from another).
func checkAuditFailureRollsBackAll(t *testing.T) {
	t.Helper()
	fixed := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(fixed)
	store, _ := newTestStoreAt(t, clk)
	db := store.db
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	nowStr := formatTS(fixed)
	req := makeReq(id, "w1")
	eventID := deriveEventID(id, req.RunID, nowStr, "fresh")

	// Pre-seed the audit table with a row that will PK-conflict on
	// the exact event_id Reserve is about to compute.
	if _, err := db.ExecContext(ctx, `
INSERT INTO write_id_reserve_events (event_id, id, run_id, task_id, outcome, occurred_at)
VALUES (?, ?, ?, ?, 'fresh', ?);`,
		eventID, string(id), req.RunID, validTaskID, nowStr); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := store.Reserve(ctx, req)
	if err == nil {
		t.Fatal("expected error from audit insert PK conflict")
	}
	if got := countRows(t, db, "write_ids"); got != 0 {
		t.Errorf("write_ids rows = %d; want 0 (rolled back)", got)
	}
	if got := countRows(t, db, "write_id_reserve_events"); got != 1 {
		t.Errorf("write_id_reserve_events rows = %d; want 1 (the seed)", got)
	}
}

// TestReserve_AllStatementsRollBackOnAuditFailure — plan-only wrapper.
func TestReserve_AllStatementsRollBackOnAuditFailure(t *testing.T) {
	t.Parallel()
	checkAuditFailureRollsBackAll(t)
}

// TestReserve_SQLMetaCharactersRoundTrip.
func TestReserve_SQLMetaCharactersRoundTrip(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	req := makeReq(id, "w1")
	req.ConversationID = "'; DROP TABLE write_ids; --"
	if _, err := store.Reserve(ctx, req); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Table must still exist and hold the hostile string verbatim.
	var stored string
	if err := store.db.QueryRow("SELECT conversation_id FROM write_ids WHERE id = ?", string(id)).Scan(&stored); err != nil {
		t.Fatalf("select: %v", err)
	}
	if stored != req.ConversationID {
		t.Errorf("stored = %q; want %q", stored, req.ConversationID)
	}
}

// TestReserve_EmitsAuditRowForInflight.
func TestReserve_EmitsAuditRowForInflight(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "wA")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, makeReq(id, "wB")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'inflight'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("inflight audit rows = %d; want 1", n)
	}
}

// TestReserve_InflightRowsCounted_ByDenominator — asserts spec §5.3
// SQL against a seeded run.
func TestReserve_InflightRowsCounted_ByDenominator(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()

	// Setup: 4 different ids to independently trigger fresh /
	// uncommitted / inflight / committed outcomes.
	ids := make([]WriteID, 4)
	for i := range ids {
		payload := fmt.Sprintf("%d", i)
		sum := sha256.Sum256([]byte(payload))
		ids[i] = mustNewWriteID(t, validTaskID, validConvID,
			fmt.Sprintf("step-%d", i), validTargetPath,
			hex.EncodeToString(sum[:]))
	}

	// Fresh (id[0]).
	if _, err := store.Reserve(ctx, req(t, ids[0], "w1", "step-0")); err != nil {
		t.Fatal(err)
	}
	// Uncommitted (id[1]) — first Reserve, expire lease, second Reserve.
	if _, err := store.Reserve(ctx, req(t, ids[1], "w1", "step-1")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := store.Reserve(ctx, req(t, ids[1], "w2", "step-1")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(-2 * time.Minute) // reset for id[2] setup
	// Inflight (id[2]) — first live worker, then second while live.
	if _, err := store.Reserve(ctx, req(t, ids[2], "w1", "step-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, req(t, ids[2], "w2", "step-2")); err != nil {
		t.Fatal(err)
	}
	// Committed (id[3]) — reserve + commit + reserve.
	if _, err := store.Reserve(ctx, req(t, ids[3], "w1", "step-3")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: ids[3], RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, req(t, ids[3], "w2", "step-3")); err != nil {
		t.Fatal(err)
	}

	// The D4 SQL from spec §5.3:
	const dupSQL = `
SELECT
  CAST(SUM(CASE WHEN outcome IN ('uncommitted','committed')
                THEN 1 ELSE 0 END) AS REAL)
  / NULLIF(SUM(CASE WHEN outcome IN ('fresh','uncommitted','inflight','committed')
                    THEN 1 ELSE 0 END), 0)
FROM write_id_reserve_events
WHERE run_id = ?;`
	var ratio sql.NullFloat64
	if err := store.db.QueryRow(dupSQL, "run-1").Scan(&ratio); err != nil {
		t.Fatalf("dup sql: %v", err)
	}
	if !ratio.Valid {
		t.Fatalf("ratio was NULL")
	}
	// numerator = 2 (uncommitted+committed), denominator = 5
	// (fresh + uncommitted + inflight + committed + the extra
	// initial reserve on id[1]). Actually let's just require > 0
	// and < 1 — the exact ratio depends on the count of Reserve
	// calls. Precisely: 5 Reserve calls with outcomes
	//   step-0: [fresh]
	//   step-1: [fresh, uncommitted]
	//   step-2: [fresh, inflight]
	//   step-3: [fresh, committed] (+ commit event, filtered out)
	// Reserve-outcome denominator = 6 (5 above; step-0 fresh + 4
	// second-attempt Reserves). Let me recount:
	//   step-0: 1 Reserve (fresh)         → 1
	//   step-1: 2 Reserves (fresh, uncomm)→ 2
	//   step-2: 2 Reserves (fresh, inflig)→ 2
	//   step-3: 2 Reserves (fresh, comm)  → 2
	// total = 7 Reserve events. numerator = uncommitted(1) +
	// committed(1) = 2. denominator = 7. ratio = 2/7.
	want := 2.0 / 7.0
	// Tolerate floating-point noise.
	if diff := ratio.Float64 - want; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("ratio = %v; want ≈ %v", ratio.Float64, want)
	}
}

func req(t *testing.T, id WriteID, workerID, stepID string) ReserveRequest {
	t.Helper()
	return ReserveRequest{
		ID:             id,
		RunID:          "run-1",
		TaskID:         validTaskID,
		ConversationID: validConvID,
		StepID:         stepID,
		WorkerID:       workerID,
		LeaseTTL:       30 * time.Second,
	}
}

// TestReserve_EventIDDeterministicallyDerived (plan-only) — spec §5.3.
func TestReserve_EventIDDeterministicallyDerived(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(fixed)
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	nowStr := formatTS(fixed)
	req := makeReq(id, "w1")
	wantEventID := deriveEventID(id, req.RunID, nowStr, "fresh")
	var gotEventID string
	if err := store.db.QueryRow(
		"SELECT event_id FROM write_id_reserve_events WHERE id = ? AND outcome = 'fresh'",
		string(id)).Scan(&gotEventID); err != nil {
		t.Fatal(err)
	}
	if gotEventID != wantEventID {
		t.Errorf("event_id = %s; want %s", gotEventID, wantEventID)
	}
	// PK enforces uniqueness: a manual duplicate insert must fail.
	_, err := store.db.ExecContext(ctx,
		`INSERT INTO write_id_reserve_events (event_id, id, run_id, task_id, outcome, occurred_at)
                 VALUES (?, ?, 'run-2', ?, 'fresh', ?)`,
		wantEventID, string(id), validTaskID, nowStr)
	if err == nil {
		t.Errorf("duplicate PK insert should have failed")
	}
}

// TestReserveEmitsAuditRowFresh — spec-named.
func TestReserveEmitsAuditRowFresh(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'fresh'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("fresh audit rows = %d; want 1", count)
	}
}

// TestReserveEmitsAuditRowUncommitted — spec-named.
func TestReserveEmitsAuditRowUncommitted(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := store.Reserve(ctx, makeReq(id, "w2")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'uncommitted'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("uncommitted audit rows = %d; want 1", count)
	}
}

// TestReserveEmitsAuditRowCommitted — spec-named.
func TestReserveEmitsAuditRowCommitted(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, makeReq(id, "w2")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'committed'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("committed audit rows = %d; want 1", count)
	}
}

// TestCommitEmitsAuditRow — spec-named.
func TestCommitEmitsAuditRow(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'commit'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("commit audit rows = %d; want 1", count)
	}
}

// TestReserveAndAuditRollBackTogetherOnDBError — spec-named.
// Independent Test wrapper (not a function-call alias, per fresh-review
// F3) so t.Run, t.Parallel, t.Cleanup scoping stays correct.
func TestReserveAndAuditRollBackTogetherOnDBError(t *testing.T) {
	t.Parallel()
	checkAuditFailureRollsBackAll(t)
}

// -------------------------------------------------------------------
// §3.3 Commit
// -------------------------------------------------------------------

// TestCommit_MarksRowAndEmitsAuditRow (plan-only).
func TestCommit_MarksRowAndEmitsAuditRow(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}
	var committedAt sql.NullString
	if err := store.db.QueryRow(
		"SELECT committed_at FROM write_ids WHERE id = ?", string(id)).Scan(&committedAt); err != nil {
		t.Fatal(err)
	}
	if !committedAt.Valid || committedAt.String == "" {
		t.Errorf("committed_at not set")
	}
}

// TestCommit_IdempotentOnSecondCall (plan-only).
func TestCommit_IdempotentOnSecondCall(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	cr := CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}
	if err := store.Commit(ctx, cr); err != nil {
		t.Fatal(err)
	}
	// Second call — must be a no-op, no error, no new audit row.
	if err := store.Commit(ctx, cr); err != nil {
		t.Errorf("second commit: %v", err)
	}
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM write_id_reserve_events WHERE outcome = 'commit'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("commit audit rows = %d; want 1 (idempotent)", count)
	}
}

// TestCommit_ErrLeaseLost (plan-only).
func TestCommit_ErrLeaseLost(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "wA")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := store.Reserve(ctx, makeReq(id, "wB")); err != nil {
		t.Fatal(err)
	}
	// wA tries to Commit but the lease belongs to wB.
	err := store.Commit(ctx, CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "wA"})
	if !errors.Is(err, ErrLeaseLost) {
		t.Errorf("err = %v; want ErrLeaseLost", err)
	}
}

// -------------------------------------------------------------------
// §3.4 Vacuum + retention
// -------------------------------------------------------------------

func TestVacuum_DropsCommittedRowsOlderThanCutoff(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(base.Add(-31 * 24 * time.Hour))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()

	// Row 1: committed 31 days ago.
	id1 := mustNewWriteID(t, validTaskID, validConvID, "step-1", validTargetPath, validHash)
	if _, err := store.Reserve(ctx, req(t, id1, "w1", "step-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id1, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}
	// Row 2: committed 1 day ago.
	clk.Advance(30 * 24 * time.Hour) // now at base - 24h
	id2 := mustNewWriteID(t, validTaskID, validConvID, "step-2", validTargetPath, altHash)
	if _, err := store.Reserve(ctx, req(t, id2, "w1", "step-2")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id2, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}

	clk.Advance(24 * time.Hour) // now at base
	cutoff := base.Add(-30 * 24 * time.Hour)
	n, err := store.Vacuum(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("vacuumed = %d; want 1", n)
	}
	remaining := countRows(t, store.db, "write_ids")
	if remaining != 1 {
		t.Errorf("write_ids remaining = %d; want 1", remaining)
	}
}

func TestVacuum_KeepsUncommittedRegardlessOfAge(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(base.Add(-60 * 24 * time.Hour))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(60 * 24 * time.Hour) // now at base
	n, err := store.Vacuum(ctx, base.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("vacuumed = %d; want 0", n)
	}
}

// TestVacuum_ReturnsRowsAffectedCount (plan-only) — covered by
// the Drop test's count assertion above; explicit tiny test here.
func TestVacuum_ReturnsRowsAffectedCount(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	n, err := store.Vacuum(ctx, time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty vacuum = %d; want 0", n)
	}
}

// -------------------------------------------------------------------
// §3.5 RecordResumeAttempt
// -------------------------------------------------------------------

// TestRecordResumeAttempt_InitialStartedRow (plan-only).
func TestRecordResumeAttempt_InitialStartedRow(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", ""); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "resume_task_attempts"); got != 1 {
		t.Errorf("rows = %d; want 1", got)
	}
}

// TestRecordResumeAttempt_TransitionToReplayed (plan-only).
func TestRecordResumeAttempt_TransitionToReplayed(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	store, _ := newTestStoreAt(t, clk)
	ctx := context.Background()
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", ""); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "replayed", ""); err != nil {
		t.Fatal(err)
	}
	var outcome, startedAt, updatedAt string
	if err := store.db.QueryRow(
		`SELECT outcome, started_at, updated_at FROM resume_task_attempts
                 WHERE run_id = 'run-1' AND task_id = ?`, validTaskID).
		Scan(&outcome, &startedAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if outcome != "replayed" {
		t.Errorf("outcome = %s; want replayed", outcome)
	}
	if startedAt == updatedAt {
		t.Errorf("started_at should differ from updated_at after transition")
	}
	if got := countRows(t, store.db, "resume_task_attempts"); got != 1 {
		t.Errorf("rows = %d; want 1", got)
	}
}

// TestRecordResumeAttempt_TransitionToError (plan-only).
func TestRecordResumeAttempt_TransitionToError(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "error", "ErrContractNotFound"); err != nil {
		t.Fatal(err)
	}
	var outcome, errKind string
	if err := store.db.QueryRow(
		`SELECT outcome, error_kind FROM resume_task_attempts
                 WHERE run_id = 'run-1' AND task_id = ?`, validTaskID).
		Scan(&outcome, &errKind); err != nil {
		t.Fatal(err)
	}
	if outcome != "error" || errKind != "ErrContractNotFound" {
		t.Errorf("outcome/errKind = %s/%s", outcome, errKind)
	}
}

// TestRecordResumeAttempt_ReinvocationResetsOutcomeToStarted (plan-only).
func TestRecordResumeAttempt_ReinvocationResetsOutcomeToStarted(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "replayed", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", ""); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := store.db.QueryRow(
		`SELECT outcome FROM resume_task_attempts WHERE run_id = 'run-1' AND task_id = ?`,
		validTaskID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "started" {
		t.Errorf("outcome = %s; want started", outcome)
	}
}

// TestRecordResumeAttempt_PrimaryKeyPreventsDuplicates (plan-only).
func TestRecordResumeAttempt_PrimaryKeyPreventsDuplicates(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	const N = 8
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			_ = store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", "")
		}()
	}
	close(start)
	wg.Wait()
	if got := countRows(t, store.db, "resume_task_attempts"); got != 1 {
		t.Errorf("rows = %d; want 1", got)
	}
}

// TestRecordResumeAttemptRowsShapeCorrect — spec-named.
func TestRecordResumeAttemptRowsShapeCorrect(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "started", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordResumeAttempt(ctx, "run-1", validTaskID, "replayed", ""); err != nil {
		t.Fatal(err)
	}
	// Six columns declared: run_id, task_id, outcome, error_kind,
	// started_at, updated_at.
	var (
		runID, taskID, outcome, errKind, startedAt, updatedAt string
	)
	if err := store.db.QueryRow(
		`SELECT run_id, task_id, outcome, error_kind, started_at, updated_at
                 FROM resume_task_attempts WHERE run_id = 'run-1'`).
		Scan(&runID, &taskID, &outcome, &errKind, &startedAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if runID != "run-1" || taskID != validTaskID || outcome != "replayed" ||
		errKind != "" || startedAt == "" || updatedAt == "" {
		t.Errorf("row shape mismatch: %s/%s/%s/%s/%s/%s",
			runID, taskID, outcome, errKind, startedAt, updatedAt)
	}
}

// -------------------------------------------------------------------
// §3.6 PayloadStager
// -------------------------------------------------------------------

// TestStage_HappyPath (plan-only).
func TestStage_HappyPath(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	ps := NewSQLitePayloadStager(db)
	ctx := context.Background()
	payload := []byte("hello world")
	sum := sha256.Sum256(payload)
	id, err := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath,
		hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.Stage(ctx, id, validTaskID, validStepID, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ps.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload = %q; want %q", got, payload)
	}
}

// TestStage_IdempotentSameID (plan-only).
func TestStage_IdempotentSameID(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	ps := NewSQLitePayloadStager(db)
	ctx := context.Background()
	payload := []byte("data")
	sum := sha256.Sum256(payload)
	id, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, hex.EncodeToString(sum[:]))
	for i := 0; i < 3; i++ {
		if err := ps.Stage(ctx, id, validTaskID, validStepID, payload); err != nil {
			t.Fatal(err)
		}
	}
	if got := countRows(t, db, "write_id_payloads"); got != 1 {
		t.Errorf("rows = %d; want 1", got)
	}
}

// TestStage_ContentChangeUnderSameStepIDCoexist (plan-only).
func TestStage_ContentChangeUnderSameStepIDCoexist(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	ps := NewSQLitePayloadStager(db)
	ctx := context.Background()
	pA, pB := []byte("A"), []byte("B")
	sA, sB := sha256.Sum256(pA), sha256.Sum256(pB)
	idA, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, hex.EncodeToString(sA[:]))
	idB, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, hex.EncodeToString(sB[:]))
	if err := ps.Stage(ctx, idA, validTaskID, validStepID, pA); err != nil {
		t.Fatal(err)
	}
	if err := ps.Stage(ctx, idB, validTaskID, validStepID, pB); err != nil {
		t.Fatal(err)
	}
	refs, err := ps.ListForStep(ctx, validTaskID, validStepID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Errorf("refs = %d; want 2", len(refs))
	}
}

// TestLoad_ReturnsErrPayloadUnavailableForUnknownID (plan-only).
func TestLoad_ReturnsErrPayloadUnavailableForUnknownID(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	ps := NewSQLitePayloadStager(db)
	ctx := context.Background()
	fake := WriteID(strings.Repeat("0", 64))
	_, err := ps.Load(ctx, fake)
	if !errors.Is(err, ErrPayloadUnavailable) {
		t.Errorf("err = %v; want ErrPayloadUnavailable", err)
	}
}

// TestListForStep_ReversedByStagedAt (plan-only).
func TestListForStep_ReversedByStagedAt(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	db := openStoreDB(t)
	ps := NewSQLitePayloadStager(db, WithPayloadStagerClock(clk.Now))
	ctx := context.Background()

	idA, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if err := ps.Stage(ctx, idA, validTaskID, validStepID, []byte("early")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(1 * time.Minute)
	idB, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, altHash)
	if err := ps.Stage(ctx, idB, validTaskID, validStepID, []byte("late")); err != nil {
		t.Fatal(err)
	}
	refs, err := ps.ListForStep(ctx, validTaskID, validStepID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].ID != idB || refs[1].ID != idA {
		t.Errorf("refs order wrong: %+v", refs)
	}
}

// TestListForStep_PopulatesCommittedAt (plan-only).
func TestListForStep_PopulatesCommittedAt(t *testing.T) {
	t.Parallel()
	clk := newFakeClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	db := openStoreDB(t)
	store := NewSQLiteWriteIDStore(db, WithWriteIDClock(clk.Now))
	ps := NewSQLitePayloadStager(db, WithPayloadStagerClock(clk.Now))
	ctx := context.Background()

	payload := []byte("committed_bytes")
	sum := sha256.Sum256(payload)
	idA, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, hex.EncodeToString(sum[:]))
	if err := ps.Stage(ctx, idA, validTaskID, validStepID, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, makeReq(idA, "w1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: idA, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}

	// An uncommitted second attempt on same step.
	clk.Advance(1 * time.Minute)
	idB, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, altHash)
	if err := ps.Stage(ctx, idB, validTaskID, validStepID, []byte("uncommitted_bytes")); err != nil {
		t.Fatal(err)
	}

	refs, err := ps.ListForStep(ctx, validTaskID, validStepID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %d; want 2", len(refs))
	}
	// Latest first: idB uncommitted, then idA committed.
	if refs[0].ID != idB || refs[0].CommittedAt != nil {
		t.Errorf("refs[0]: %+v", refs[0])
	}
	if refs[1].ID != idA || refs[1].CommittedAt == nil {
		t.Errorf("refs[1]: %+v", refs[1])
	}
}

// TestListForStep_EmptyReturnsNil (plan-only).
func TestListForStep_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	ps := NewSQLitePayloadStager(db)
	ctx := context.Background()
	refs, err := ps.ListForStep(ctx, validTaskID, "no-such-step")
	if err != nil {
		t.Fatal(err)
	}
	if refs != nil {
		t.Errorf("refs = %+v; want nil", refs)
	}
}

// -------------------------------------------------------------------
// §3.7 Retention hits payload table
// -------------------------------------------------------------------

// TestVacuum_PayloadsCleanedForOldCommits (plan-only).
func TestVacuum_PayloadsCleanedForOldCommits(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(base.Add(-31 * 24 * time.Hour))
	db := openStoreDB(t)
	store := NewSQLiteWriteIDStore(db, WithWriteIDClock(clk.Now))
	ps := NewSQLitePayloadStager(db, WithPayloadStagerClock(clk.Now))
	ctx := context.Background()

	payload := []byte("old")
	sum := sha256.Sum256(payload)
	id, _ := NewWriteID(validTaskID, validConvID, validStepID, validTargetPath, hex.EncodeToString(sum[:]))
	if err := ps.Stage(ctx, id, validTaskID, validStepID, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}

	clk.Advance(31 * 24 * time.Hour) // now at base
	if _, err := store.Vacuum(ctx, base.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "write_id_payloads"); got != 0 {
		t.Errorf("payloads remaining = %d; want 0", got)
	}
}

// -------------------------------------------------------------------
// §3.8 Metrics collaborator
// -------------------------------------------------------------------

type recordingMetrics struct {
	mu     sync.Mutex
	counts map[string]int
	labels []map[string]string
}

func (r *recordingMetrics) IncCounter(name string, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	key := name
	if o, ok := labels["outcome"]; ok {
		key = name + "|" + o
	}
	r.counts[key]++
	r.labels = append(r.labels, labels)
}

// TestMetrics_ReserveEmitsCounters (plan-only).
func TestMetrics_ReserveEmitsCounters(t *testing.T) {
	t.Parallel()
	m := &recordingMetrics{}
	store := NewSQLiteWriteIDStore(openStoreDB(t), WithWriteIDMetrics(m))
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	if m.counts["resume_write_id_reserve_total|fresh"] != 1 {
		t.Errorf("counter not emitted: %+v", m.counts)
	}
}

// TestMetrics_CommitEmitsCounters (plan-only).
func TestMetrics_CommitEmitsCounters(t *testing.T) {
	t.Parallel()
	m := &recordingMetrics{}
	store := NewSQLiteWriteIDStore(openStoreDB(t), WithWriteIDMetrics(m))
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
	cr := CommitRequest{ID: id, RunID: "run-1", TaskID: validTaskID, WorkerID: "w1"}
	if err := store.Commit(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if m.counts["resume_write_id_commit_total|applied"] != 1 {
		t.Errorf("applied counter: %+v", m.counts)
	}
	if m.counts["resume_write_id_commit_total|noop"] != 1 {
		t.Errorf("noop counter: %+v", m.counts)
	}
}

// TestMetrics_NopDefaultDoesNotPanic (plan-only).
func TestMetrics_NopDefaultDoesNotPanic(t *testing.T) {
	t.Parallel()
	store := newTestStore(t) // no WithWriteIDMetrics
	ctx := context.Background()
	id := mustNewWriteID(t, validTaskID, validConvID, validStepID, validTargetPath, validHash)
	if _, err := store.Reserve(ctx, makeReq(id, "w1")); err != nil {
		t.Fatal(err)
	}
}

// -------------------------------------------------------------------
// §3.9 Perf assertion (CI-guarded)
// -------------------------------------------------------------------

// TestReserve_ThroughputSmoke (plan-only) — 1000 Reserves in <1s.
func TestReserve_ThroughputSmoke(t *testing.T) {
	if testing.Short() || os.Getenv("CI") != "" {
		t.Skip("perf assertion skipped in CI / -short")
	}
	store := newTestStore(t)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		payload := fmt.Sprintf("payload-%d", i)
		sum := sha256.Sum256([]byte(payload))
		id, err := NewWriteID(validTaskID, validConvID,
			fmt.Sprintf("step-%d", i), validTargetPath,
			hex.EncodeToString(sum[:]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Reserve(ctx, req(t, id, "w1", fmt.Sprintf("step-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("1000 Reserves took %v; want <5s", d)
	}
}

// -------------------------------------------------------------------
// §5.1 OpenSQLite loads the new tables
// -------------------------------------------------------------------

// TestOpenSQLite_LoadsNewTables (plan-only).
func TestOpenSQLite_LoadsNewTables(t *testing.T) {
	t.Parallel()
	db := openStoreDB(t)
	want := []string{
		"write_ids", "write_id_reserve_events",
		"resume_task_attempts", "task_run_bindings",
		"write_id_payloads",
	}
	for _, name := range want {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).
			Scan(&got)
		if err != nil {
			t.Errorf("missing table %s: %v", name, err)
		}
	}
	wantIndices := []string{
		"idx_write_ids_conv_step", "idx_write_ids_task_id",
		"idx_write_ids_committed_at",
		"idx_write_id_reserve_events_run", "idx_write_id_reserve_events_id",
		"idx_resume_task_attempts_run", "idx_task_run_bindings_run",
		"idx_write_id_payloads_task_step",
	}
	for _, name := range wantIndices {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).
			Scan(&got)
		if err != nil {
			t.Errorf("missing index %s: %v", name, err)
		}
	}
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------
// (fakeClock and newTestStoreAt live near the top of the file.)
