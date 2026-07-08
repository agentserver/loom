package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/yourorg/multi-agent/tools/eval/runner/probes"
)

// Compile-time interface check: *RunRow satisfies probes.RunRowLike
// so probes.MergeIntoRow can write into it without importing package
// main (which would be a cycle).
var _ probes.RunRowLike = (*RunRow)(nil)

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

	// BaselineOrAblation is the D1 runs.baseline_or_ablation label
	// (08 号 line 260) derived by Run via
	// ComputeBaselineOrAblation(opts.AblationFlags, opts.BaselineName).
	// Callers MUST NOT set this directly — Run always overwrites it
	// with the derived value to prevent label forgery (WT-2-flag-integration
	// spec §7(a.3)).
	BaselineOrAblation string

	// Probe fields (WT-2-e1e6-probes spec §4). Pointer types encode
	// "unavailable" as nil; the CSV serialiser emits "" for nil.
	ProbeTaskSuccessRate            *bool
	ProbeLifecycleClosureRate       *bool
	ProbeTimeToCompletionNs         *int64
	ProbeHumanContextSelectionCount *int
	ProbeWrongContextFailureRate    *bool
	ProbeArtifactCorrectnessRate    *bool
	ProbeManualSetupStepCount       *int
	ProbeConfigTouchCount           *int
	ProbeNotesJSON                  string // "{}" when empty; MergeIntoRow always sets this

	// Codex CLI token usage. Zero means either no usage JSONL was
	// supplied or Codex reported zero tokens for that side.
	ModelInputTokens  int
	ModelOutputTokens int
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
		// WT-2-e1e6-probes §4 (append-only):
		"probe_task_success_rate",
		"probe_lifecycle_closure_rate",
		"probe_time_to_completion_ns",
		"probe_human_context_selection_count",
		"probe_wrong_context_failure_rate",
		"probe_artifact_correctness_rate",
		"probe_manual_setup_step_count",
		"probe_config_touch_count",
		"probe_notes_json",
		// WT-2-flag-integration §2.4 (append-only):
		"baseline_or_ablation",
		// Codex CLI token usage (append-only):
		"model_input_tokens",
		"model_output_tokens",
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
		nilOrBool(r.ProbeTaskSuccessRate),
		nilOrBool(r.ProbeLifecycleClosureRate),
		nilOrInt64(r.ProbeTimeToCompletionNs),
		nilOrInt(r.ProbeHumanContextSelectionCount),
		nilOrBool(r.ProbeWrongContextFailureRate),
		nilOrBool(r.ProbeArtifactCorrectnessRate),
		nilOrInt(r.ProbeManualSetupStepCount),
		nilOrInt(r.ProbeConfigTouchCount),
		probeNotesOrDefault(r.ProbeNotesJSON),
		r.BaselineOrAblation,
		strconv.Itoa(r.ModelInputTokens),
		strconv.Itoa(r.ModelOutputTokens),
	}
}

// nilOrBool returns "" for nil pointer inputs and "true"/"false"
// otherwise. Matches spec §4 nil encoding for probe columns.
func nilOrBool(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

func nilOrInt(i *int) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(*i)
}

func nilOrInt64(i *int64) string {
	if i == nil {
		return ""
	}
	return strconv.FormatInt(*i, 10)
}

func probeNotesOrDefault(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// SetProbe implements probes.RunRowLike. Last write wins per spec §5.2.
func (r *RunRow) SetProbe(metric probes.MetricKey, value any) {
	switch metric {
	case probes.MetricTaskSuccessRate:
		r.ProbeTaskSuccessRate = boolPtrOrNil(value)
	case probes.MetricLifecycleClosureRate:
		r.ProbeLifecycleClosureRate = boolPtrOrNil(value)
	case probes.MetricTimeToCompletion:
		r.ProbeTimeToCompletionNs = int64PtrOrNil(value)
	case probes.MetricHumanContextSelectionCount:
		r.ProbeHumanContextSelectionCount = intPtrOrNil(value)
	case probes.MetricWrongContextFailureRate:
		r.ProbeWrongContextFailureRate = boolPtrOrNil(value)
	case probes.MetricArtifactCorrectnessRate:
		r.ProbeArtifactCorrectnessRate = boolPtrOrNil(value)
	case probes.MetricManualSetupStepCount:
		r.ProbeManualSetupStepCount = intPtrOrNil(value)
	case probes.MetricConfigTouchCount:
		r.ProbeConfigTouchCount = intPtrOrNil(value)
	}
}

// SetProbeNotes implements probes.RunRowLike.
func (r *RunRow) SetProbeNotes(j string) { r.ProbeNotesJSON = j }

func boolPtrOrNil(v any) *bool {
	if v == nil {
		return nil
	}
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

func intPtrOrNil(v any) *int {
	if v == nil {
		return nil
	}
	if i, ok := v.(int); ok {
		return &i
	}
	return nil
}

func int64PtrOrNil(v any) *int64 {
	if v == nil {
		return nil
	}
	if i, ok := v.(int64); ok {
		return &i
	}
	return nil
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
