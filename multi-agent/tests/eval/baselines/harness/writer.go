package harness

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ErrCSVAlreadyExists is returned by WriteCSV when the target file is
// already on disk. The writer refuses to append or overwrite — a caller
// that lands two baseline runs at the same `--out` path is almost
// certainly a bug (accidental clobber of a previous smoke result).
var ErrCSVAlreadyExists = errors.New("harness: --out file already exists; refusing to overwrite")

// csvHeader is the exact stable header written to every baseline CSV. It
// must stay 1:1 with the field order in BaselineRunRow so a follow-up
// D1 writer can zip rows without reshuffling.
var csvHeader = []string{
	"run_id",
	"workload_id",
	"baseline_or_ablation",
	"started_at_unix",
	"finished_at_unix",
	"duration_ms",
	"passed",
	"oracle_exit_code",
	"oracle_details_json",
	"oracle_metrics_json",
	"dry_run",
	"metrics_baseline_wall_time_ms",
	"metrics_baseline_api_calls",
	"metrics_baseline_upload_bytes",
	"tempdir_kept",
}

// WriteCSV writes one row to `path`, creating the file with a header +
// one data line. Fails hard if the file already exists (spec §5's "no
// accidental append" rule). Errors are wrapped with context so a caller
// invoking the runner in a for-loop can log per-row failures cleanly.
func WriteCSV(path string, row BaselineRunRow) error {
	// O_EXCL is the atomic "create or fail" primitive; O_CREATE|O_EXCL
	// gives us the refuse-if-exists guarantee without a race between
	// stat() and open().
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrCSVAlreadyExists, path)
		}
		return fmt.Errorf("harness: open %s: %w", path, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write(csvHeader); err != nil {
		return fmt.Errorf("harness: write header: %w", err)
	}
	if err := w.Write(rowToCSVRecord(row)); err != nil {
		return fmt.Errorf("harness: write data row: %w", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("harness: csv flush: %w", err)
	}
	return nil
}

// rowToCSVRecord serialises a BaselineRunRow into the exact stable
// column order encoded in csvHeader. Direct field access (no reflection)
// keeps this file and row.go in lock-step; adding a column requires
// touching both the struct and this list at once, which the writer test
// enforces.
func rowToCSVRecord(row BaselineRunRow) []string {
	return []string{
		row.RunID,
		row.WorkloadID,
		row.BaselineOrAblation,
		strconv.FormatInt(row.StartedAtUnix, 10),
		strconv.FormatInt(row.FinishedAtUnix, 10),
		strconv.FormatInt(row.DurationMS, 10),
		strconv.FormatBool(row.Passed),
		strconv.Itoa(row.OracleExitCode),
		row.OracleDetailsJSON,
		row.OracleMetricsJSON,
		strconv.FormatBool(row.DryRun),
		strconv.FormatInt(row.MetricsBaselineWallTimeMS, 10),
		strconv.Itoa(row.MetricsBaselineAPICalls),
		strconv.FormatInt(row.MetricsBaselineUploadBytes, 10),
		strconv.FormatBool(row.TempdirKept),
	}
}

// CSVHeader returns a copy of the stable CSV header for tests / external
// code that need to know the column order without importing the
// unexported literal.
func CSVHeader() []string {
	out := make([]string, len(csvHeader))
	copy(out, csvHeader)
	return out
}
