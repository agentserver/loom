package main

import (
	"bytes"
	"encoding/csv"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Test #38 — two-round output: both rows present; off_p50 > on_p50
// (real writer is more expensive than the noop; correctness gate, not
// perf assertion — the DELTA is guaranteed by construction).
func TestObserverBench_TwoRounds_NoObserverOnOff(t *testing.T) {
	if testing.Short() {
		t.Skip("bench takes ~seconds; skipped in short mode")
	}
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "obs.db")
	var out bytes.Buffer
	code := run([]string{
		"--sqlite-path", dbPath,
		"--warmup", "100", "--samples", "500",
		"--conv-id", "conv-obsbench",
	}, &out, &out)
	if code != 0 {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	// Parse CSV.
	rd := csv.NewReader(strings.NewReader(out.String()))
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want header + 2 data rows, got %d rows: %v", len(rows), rows)
	}
	byLabel := map[string]int64{}
	for _, r := range rows[1:] {
		v, err := strconv.ParseInt(r[1], 10, 64)
		if err != nil {
			t.Fatalf("parse p50 %q: %v", r[1], err)
		}
		byLabel[r[0]] = v
	}
	if _, ok := byLabel["true"]; !ok {
		t.Errorf("missing NoObserver=true row")
	}
	if _, ok := byLabel["false"]; !ok {
		t.Errorf("missing NoObserver=false row")
	}
	// Correctness gate: the real writer's p50 must be >= the noop's p50.
	// We don't assert a specific factor — CI machines vary — only sign.
	if byLabel["false"] < byLabel["true"] {
		t.Errorf("NoObserver=false p50 (%d) should be ≥ NoObserver=true p50 (%d)",
			byLabel["false"], byLabel["true"])
	}
}

// Test #39 — --dry-run does not open a SQLite file (or write anything).
func TestObserverBench_DryRun_NoSQLiteOpen(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before := countFiles(t, tmp)
	var out bytes.Buffer
	code := run([]string{"--dry-run"}, &out, &out)
	if code != 0 {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "observer_bench plan") {
		t.Errorf("dry-run stdout missing plan header: %q", out.String())
	}
	after := countFiles(t, tmp)
	if after != before {
		t.Errorf("TMPDIR file count changed during --dry-run: before=%d after=%d", before, after)
	}
}

// Test #40 — the bench honours the injected noObserverCurrent accessor.
// Point noObserverCurrent at a sentinel and assert the writer picked in
// round A is the noop (identity == observerstore.CurrentProbeWriter()
// after SetProbeWriter(nil)).
func TestObserverBench_ReadsAblationRegistry(t *testing.T) {
	prev := noObserverCurrent
	t.Cleanup(func() { noObserverCurrent = prev })
	noObserverCurrent = func() bool { return true }
	got := chooseWriter(nil) // real writer irrelevant when NoObserver=true
	if got == nil {
		t.Fatalf("chooseWriter returned nil")
	}
	if !got.IsNoop() {
		t.Errorf("chooseWriter under NoObserver=true returned non-noop writer")
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	_ = os.Stat // silence unused import if trimmed
	return n
}
