package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// RunRow is the row schema for a single eval run. The column order in
// CSVColumns() and the field set here mirror docs/specs/wt1-eval-runner-skeleton.spec.md
// §5; WT-1-run-schema's SQLiteWriter implementation must accept this struct
// without remapping.
type RunRow struct {
	RunID              string
	WorkloadID         string
	StartedAtUnix      int64
	FinishedAtUnix     int64
	DurationMs         int64
	Passed             bool
	OracleExitCode     int
	OracleDetailsJSON  string
	OracleMetricsJSON  string
	LoomCommit         string
	AgentserverCommit  string
	ModelserverCommit  string
	AppCommit          string
	OSKernel           string
	OSDistro           string
	OSArch             string
	MachineHostname    string
	AuthorEmailSHA8    string
	CommitterEmailSHA8 string
	CodexConfigPath    string
	StubListen         string
	TempdirKept        bool
}

// RunWriter is the seam between this worktree and WT-1-run-schema. Skeleton
// hands out NoopWriter; the run-schema worktree will land a SQLite-backed
// implementation behind the same contract.
type RunWriter interface {
	Insert(ctx context.Context, row RunRow) error
}

// NoopWriter discards rows. Used whenever --observer-db is unset and as the
// stand-in until WT-1-run-schema lands.
type NoopWriter struct{}

// Insert satisfies RunWriter without persisting anything.
func (NoopWriter) Insert(context.Context, RunRow) error { return nil }

// CSVColumns is the stable header order. Changing this list is a schema
// migration — append-only.
func CSVColumns() []string {
	return []string{
		"run_id",
		"workload_id",
		"started_at_unix",
		"finished_at_unix",
		"duration_ms",
		"passed",
		"oracle_exit_code",
		"oracle_details_json",
		"oracle_metrics_json",
		"loom_commit",
		"agentserver_commit",
		"modelserver_commit",
		"app_commit",
		"os_kernel",
		"os_distro",
		"os_arch",
		"machine_hostname",
		"author_email_sha8",
		"committer_email_sha8",
		"codex_config_path",
		"stub_listen",
		"tempdir_kept",
	}
}

// rowAsCSVRecord converts the typed row to the encoder's wire format. The
// order matches CSVColumns() one-for-one.
func rowAsCSVRecord(r RunRow) []string {
	return []string{
		r.RunID,
		r.WorkloadID,
		strconv.FormatInt(r.StartedAtUnix, 10),
		strconv.FormatInt(r.FinishedAtUnix, 10),
		strconv.FormatInt(r.DurationMs, 10),
		strconv.FormatBool(r.Passed),
		strconv.Itoa(r.OracleExitCode),
		r.OracleDetailsJSON,
		r.OracleMetricsJSON,
		r.LoomCommit,
		r.AgentserverCommit,
		r.ModelserverCommit,
		r.AppCommit,
		r.OSKernel,
		r.OSDistro,
		r.OSArch,
		r.MachineHostname,
		r.AuthorEmailSHA8,
		r.CommitterEmailSHA8,
		r.CodexConfigPath,
		r.StubListen,
		strconv.FormatBool(r.TempdirKept),
	}
}

// WriteCSVRow writes a fresh CSV file at path containing the header row plus
// the supplied data row. The file must NOT already exist — accidental
// re-runs that would silently corrupt a previous result are rejected with
// ErrCSVExists. Use a fresh --out path per run, or `rm` the previous file.
func WriteCSVRow(path string, row RunRow) error {
	if path == "" {
		return fmt.Errorf("eval-runner: --out is required")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrCSVExists, path)
		}
		return fmt.Errorf("eval-runner: open --out %q: %w", path, err)
	}
	defer f.Close()
	if err := writeCSV(f, row); err != nil {
		return fmt.Errorf("eval-runner: write --out %q: %w", path, err)
	}
	return f.Sync()
}

func writeCSV(w io.Writer, row RunRow) error {
	c := csv.NewWriter(w)
	if err := c.Write(CSVColumns()); err != nil {
		return err
	}
	if err := c.Write(rowAsCSVRecord(row)); err != nil {
		return err
	}
	c.Flush()
	return c.Error()
}

// ErrCSVExists signals that the requested --out file is already present.
// Refusing rather than appending matches the operator expectation that one
// run produces a freshly-named CSV; appending to an existing file would
// silently change its schema-counted row count.
var ErrCSVExists = errors.New("eval-runner: --out file already exists")
