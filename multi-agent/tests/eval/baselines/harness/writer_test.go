package harness

import (
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriter_HeaderAndSingleRow — plan #5. Locks the 2-line invariant
// used by the 15-run smoke `wc -l` assertion + the stable column-order
// contract with the (future) D1 writer.
func TestWriter_HeaderAndSingleRow(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "row.csv")

	row := BaselineRunRow{
		RunID:                      "brun-1234-cross-device-code-mod-abcd",
		WorkloadID:                 "cross-device-code-mod",
		BaselineOrAblation:         "manual_ssh",
		StartedAtUnix:              1_720_000_000,
		FinishedAtUnix:             1_720_000_005,
		DurationMS:                 5000,
		Passed:                     true,
		OracleExitCode:             0,
		OracleDetailsJSON:          `{"patch":"ok"}`,
		OracleMetricsJSON:          `{"patch_bytes":42}`,
		DryRun:                     true,
		MetricsBaselineWallTimeMS:  10,
		MetricsBaselineAPICalls:    0,
		MetricsBaselineUploadBytes: 0,
		TempdirKept:                false,
	}

	if err := WriteCSV(out, row); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	// Assert file has header + 1 data row = 2 records via encoding/csv.
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("csv read: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (header + row), got %d: %v", len(recs), recs)
	}

	// Locked header — reviewers must update csvHeader when they add a
	// column, not silently rely on struct field order.
	wantHeader := CSVHeader()
	for i, col := range wantHeader {
		if recs[0][i] != col {
			t.Errorf("header column %d: got %q, want %q", i, recs[0][i], col)
		}
	}
	// Sanity: known columns in the data row.
	if recs[1][0] != row.RunID {
		t.Errorf("run_id column: got %q, want %q", recs[1][0], row.RunID)
	}
	if recs[1][2] != row.BaselineOrAblation {
		t.Errorf("baseline_or_ablation column: got %q, want %q", recs[1][2], row.BaselineOrAblation)
	}
	if recs[1][10] != "true" { // dry_run
		t.Errorf("dry_run column: got %q, want 'true'", recs[1][10])
	}
	// Also lock the raw byte layout: 2 lines by line count (the 15-run
	// smoke uses `wc -l`).
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := strings.Count(string(raw), "\n"); n != 2 {
		t.Errorf("expected 2 newlines (header + row), got %d in:\n%s", n, string(raw))
	}
}

// TestWriter_RefuseIfExists — plan #6. `--out` accidents must be a hard
// error, not a silent append.
func TestWriter_RefuseIfExists(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "row.csv")

	if err := WriteCSV(out, BaselineRunRow{BaselineOrAblation: "manual_ssh"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := WriteCSV(out, BaselineRunRow{BaselineOrAblation: "manual_ssh"})
	if !errors.Is(err, ErrCSVAlreadyExists) {
		t.Fatalf("second write: want ErrCSVAlreadyExists, got %v", err)
	}
}
