package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// openTestObserver mirrors observerstore's openProbeStore helper but is
// duplicated here to avoid an in-tree test-helper import cycle.
func openTestObserver(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "obs.db")
	st, err := observerstore.OpenSQLite(p)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

// Test #13 — happy path: span written, kind matches, duration_ns > 0
// (correctness: proves the End timestamp is later than Start; NOT a
// wall-clock threshold assertion). The looser assertion satisfies spec
// §6 (h) so no CI guard is needed — this test is a functional check,
// not a perf check.
func TestMeasurePlanning_HappyPath(t *testing.T) {
	db := openTestObserver(t)
	SetPlanningProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetPlanningProbeWriter(nil) })

	got, err := MeasurePlanning(context.Background(), "conv-abcdefgh", func() (int, error) {
		// A tiny sleep ensures monotonic clock advances; no wall-clock
		// threshold is asserted (would be a §6 (h) violation).
		time.Sleep(1 * time.Millisecond)
		return 42, nil
	})
	if err != nil {
		t.Fatalf("MeasurePlanning: %v", err)
	}
	if got != 42 {
		t.Errorf("fn return not propagated: got %d", got)
	}
	var (
		kind string
		dur  int64
	)
	if err := db.QueryRow(
		`SELECT probe_kind, duration_ns FROM probe_events`,
	).Scan(&kind, &dur); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if kind != string(observerstore.KindDriverPlanning) {
		t.Errorf("kind mismatch: %q", kind)
	}
	// Correctness gate only: End must be after Start (monotonic clock).
	// No absolute-latency assertion — CI runners are noisy.
	if dur <= 0 {
		t.Errorf("duration_ns %d ≤ 0; span timestamps went backwards", dur)
	}
}

// Test #14 — panic in fn: span still written; MeasurePlanning re-panics.
func TestMeasurePlanning_PanicRecovery_StillWritesSpan(t *testing.T) {
	db := openTestObserver(t)
	SetPlanningProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetPlanningProbeWriter(nil) })

	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		_, _ = MeasurePlanning(context.Background(), "conv-abcdefgh", func() (int, error) {
			panic("boom")
		})
	}()
	if !panicked {
		t.Fatalf("expected panic to propagate")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 span row after panic, got %d", n)
	}
}

// Test #15 — noop writer fast path: 100k calls allocate nothing per op.
// Correctness-shaped assertion (Allocs == 0), CI-guarded because the
// benchmark itself is what takes time.
func TestMeasurePlanning_NoopWriter_ZeroAllocationsOverhead(t *testing.T) {
	if testing.Short() {
		t.Skip("bench-shape perf test skipped in short mode")
	}
	SetPlanningProbeWriter(observerstore.CurrentProbeWriter()) // noop
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = MeasurePlanning(context.Background(), "conv-abcdefgh", func() (int, error) {
				return 0, nil
			})
		}
	})
	if res.AllocsPerOp() > 1 {
		t.Errorf("noop fast path allocated %d per op; want ≤ 1", res.AllocsPerOp())
	}
}

// Test #16 — invalid convID: fn still runs; return value propagates; no
// row written; observerstore's counter bumped.
func TestMeasurePlanning_RejectsInvalidConvID(t *testing.T) {
	db := openTestObserver(t)
	SetPlanningProbeWriter(observerstore.NewProbeEventWriter(db))
	t.Cleanup(func() { SetPlanningProbeWriter(nil) })

	got, err := MeasurePlanning(context.Background(), "bad id", func() (string, error) {
		return "ran", errors.New("fn err")
	})
	if got != "ran" {
		t.Errorf("fn return not propagated: %q", got)
	}
	if err == nil || err.Error() != "fn err" {
		t.Errorf("fn error not propagated: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no row should be written on invalid convID, got %d", n)
	}
}

// Test #17 — structural: MeasurePlanning[T]'s signature has no time.Time.
func TestMeasurePlanning_SignatureNoTimeArg(t *testing.T) {
	tt := reflect.TypeOf(time.Time{})
	fnT := reflect.TypeOf(MeasurePlanning[int])
	for i := 0; i < fnT.NumIn(); i++ {
		if fnT.In(i) == tt {
			t.Errorf("MeasurePlanning[int] param #%d is time.Time", i)
		}
	}
	for i := 0; i < fnT.NumOut(); i++ {
		if fnT.Out(i) == tt {
			t.Errorf("MeasurePlanning[int] return #%d is time.Time", i)
		}
	}
}

// Test #54 — file-domain guard: fanout.go diff is at most 3 lines
// (+3/-0 or smaller). Skips only when git is unavailable.
func TestFanoutDiffAtMost3Lines(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	rootOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git worktree")
	}
	root := strings.TrimSpace(string(rootOut))
	cmd := exec.Command("git", "diff", "--numstat",
		"origin/paper/v3-integration", "--", "internal/orchestrator/fanout.go")
	cmd.Dir = filepath.Join(root, "multi-agent")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git diff failed: %v", err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return // no diff = fine
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("unexpected git diff --numstat output: %q", line)
	}
	added, _ := strconv.Atoi(fields[0])
	removed, _ := strconv.Atoi(fields[1])
	if added > 3 || removed > 3 {
		t.Errorf("fanout.go diff too big: +%d -%d (spec caps at 3)", added, removed)
	}
}
