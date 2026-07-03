package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// WorkloadSpec is the trimmed view of a workload's spec.yaml the
// baseline harness needs. Field names mirror 13 号 §1.2 exactly; unused
// fields (required_contexts, allowed_contexts, ...) are decoded and
// ignored so a future harness upgrade doesn't need a yaml re-parse.
type WorkloadSpec struct {
	ID             string `yaml:"id"`
	SuccessOracle  string `yaml:"success_oracle"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	Description    string `yaml:"description"`
	// SpecPath is filled in after load so callers can resolve
	// success_oracle relative to the spec.yaml file.
	SpecPath string `yaml:"-"`
}

// BaselineImpl is the per-baseline seam. All non-trivial work lives in
// an implementation of this interface; the harness owns the surrounding
// pipeline (workspace, env whitelist, oracle, CSV emit).
//
// The `agentEnv` slice passed to Prepare / ExecuteAgent is the env
// intended for the AGENT subprocess only. Implementations must never
// touch the oracle env (which is constructed independently by the
// harness at pipeline step 9 and passed straight to runOracle).
type BaselineImpl interface {
	// Name is the default baseline_or_ablation value used when
	// --baseline-name is empty. Must match the ValidateBaselineName
	// regex.
	Name() string

	// AgentForwards is the per-baseline credential opt-in list. Only
	// returns non-nil when the operator has opted in via a
	// baseline-specific CLI flag (e.g. --forward-anthropic-api-key).
	// Empty by default; per spec §7(a) baseline name choice never
	// implies credential forwarding.
	AgentForwards() AgentForwards

	// Prepare runs after the workspace tempdir is populated with the
	// workload's fixtures. Cloud baselines upload here; manual_ssh and
	// single_machine are no-op. A non-nil error aborts the whole run
	// with exit 2 (pre-flight failure).
	Prepare(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) error

	// ExecuteAgent is called after Prepare and must arrange that
	// `ws.Root` contains the workload's `outputs.write_targets`
	// artefacts by the time it returns. On dryRun=true implementations
	// must never contact an external service; the mock_workspace has
	// already been projected into ws.Root by the harness, so a
	// zero-work implementation is legal in that branch.
	ExecuteAgent(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) (ExecuteMetrics, error)
}

// ExecuteMetrics feeds row columns 12–14 (metrics_baseline_*).
type ExecuteMetrics struct {
	WallTimeMS  int64
	APICalls    int
	UploadBytes int64
}

// Result is what Run returns. ExitCode is the exit status main() should
// pass to os.Exit; Row is the emitted BaselineRunRow (whether or not
// CSV emission was attempted); Err carries the top-level failure.
type Result struct {
	Row      BaselineRunRow
	ExitCode int
	Err      error
}

// Run is the shared orchestrator. Per spec §4 it walks steps 1–10 with
// the two BaselineImpl seams (Prepare + ExecuteAgent) filling in the
// per-baseline behaviour.
func Run(ctx context.Context, opts Opts, impl BaselineImpl, stderr io.Writer) (result Result) {
	if stderr == nil {
		stderr = os.Stderr
	}
	// Spec §7(f) contract: a real E2B client that a bug reaches with
	// dryRun=true panics ("dry-run mode reached real E2B client Do");
	// the harness catches that panic and maps it to exit 3 so the
	// smoke matrix + CI fail loudly rather than dumping a stack.
	// Applies to any unexpected panic inside an impl too.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "harness: panic recovered in impl: %v\n", r)
			// Row is deliberately zero-valued: the named return `result`
			// is never assigned before a panic (all non-panic exit
			// points build a fresh Result{...} and rely on the assign-
			// at-return semantics). Setting Row would just re-echo the
			// zero value and mislead a maintainer into thinking a
			// partial row was preserved.
			result = Result{ExitCode: 3, Err: fmt.Errorf("harness: panic in impl: %v", r)}
		}
	}()
	if opts.BaselineName == "" {
		opts.BaselineName = impl.Name()
	}
	if err := ValidateBaselineName(opts.BaselineName); err != nil {
		fmt.Fprintln(stderr, err)
		return Result{ExitCode: 2, Err: err}
	}

	startedAt := time.Now().Unix()
	runID := opts.RunID
	if runID == "" {
		runID = deriveRunID(opts.BaselineName, opts.WorkloadID)
	}

	row := BaselineRunRow{
		RunID:              runID,
		WorkloadID:         opts.WorkloadID,
		BaselineOrAblation: opts.BaselineName,
		StartedAtUnix:      startedAt,
		DryRun:             opts.DryRun,
		TempdirKept:        opts.KeepTempdir,
	}

	spec, err := loadWorkloadSpec(opts.WorkloadDir, opts.WorkloadID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		row.FinishedAtUnix = time.Now().Unix()
		row.DurationMS = (row.FinishedAtUnix - row.StartedAtUnix) * 1000
		return Result{Row: row, ExitCode: 2, Err: err}
	}

	// Resolve the success_oracle path relative to spec.yaml before
	// we hand it to RunOracle (which requires an absolute path).
	oraclePath := spec.SuccessOracle
	if !filepath.IsAbs(oraclePath) {
		oraclePath = filepath.Join(filepath.Dir(spec.SpecPath), oraclePath)
	}
	oraclePath, err = filepath.Abs(oraclePath)
	if err != nil {
		row.FinishedAtUnix = time.Now().Unix()
		return Result{Row: row, ExitCode: 2, Err: err}
	}

	timeout := opts.Timeout
	if timeout <= 0 && spec.TimeoutSeconds > 0 {
		timeout = time.Duration(spec.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	fixturesDir := filepath.Join(filepath.Dir(spec.SpecPath), "fixtures")
	if _, statErr := os.Stat(fixturesDir); os.IsNotExist(statErr) {
		fixturesDir = ""
	}

	ws, err := SetupWorkspace(fixturesDir, opts.KeepTempdir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		row.FinishedAtUnix = time.Now().Unix()
		return Result{Row: row, ExitCode: 2, Err: err}
	}
	defer ws.Cleanup()

	agentEnv := WhitelistEnvForAgent(os.Environ(), opts.WorkloadID, impl.AgentForwards(), nil)
	oracleEnv := WhitelistEnvForOracle(os.Environ(), opts.WorkloadID, nil)

	if err := impl.Prepare(ctx, ws, agentEnv, opts.DryRun); err != nil {
		fmt.Fprintln(stderr, err)
		row.FinishedAtUnix = time.Now().Unix()
		row.DurationMS = (row.FinishedAtUnix - row.StartedAtUnix) * 1000
		return Result{Row: row, ExitCode: 2, Err: err}
	}

	metrics, err := impl.ExecuteAgent(ctx, ws, agentEnv, opts.DryRun)
	if err != nil {
		fmt.Fprintln(stderr, err)
		row.FinishedAtUnix = time.Now().Unix()
		row.DurationMS = (row.FinishedAtUnix - row.StartedAtUnix) * 1000
		row.MetricsBaselineWallTimeMS = metrics.WallTimeMS
		row.MetricsBaselineAPICalls = metrics.APICalls
		row.MetricsBaselineUploadBytes = metrics.UploadBytes
		// Best-effort: still write CSV so a partial row lands (exit 1,
		// not 2 — the baseline itself decided this was a runtime not
		// pre-flight failure).
		//
		// Use json.Marshal (NOT fmt.Sprintf %q) so control bytes / ANSI
		// escapes / non-ASCII runes in the subprocess stderr survive as
		// valid JSON — %q emits Go-quoted strings (`\x1b`, `\a`), which
		// json.Unmarshal rejects with "invalid character in string
		// escape code", silently corrupting the downstream D1 fold-in
		// (spec §5 promises oracle_details_json is valid JSON).
		if payload, jerr := json.Marshal(map[string]string{"agent_error": err.Error()}); jerr == nil {
			row.OracleDetailsJSON = string(payload)
		} else {
			// Very unlikely (map[string]string always marshals) — fall
			// back to a fixed-shape sentinel so the column stays valid.
			row.OracleDetailsJSON = `{"agent_error":"<unencodable>"}`
		}
		row.OracleMetricsJSON = "{}"
		_ = WriteCSV(opts.OutCSV, row)
		return Result{Row: row, ExitCode: 1, Err: err}
	}
	row.MetricsBaselineWallTimeMS = metrics.WallTimeMS
	row.MetricsBaselineAPICalls = metrics.APICalls
	row.MetricsBaselineUploadBytes = metrics.UploadBytes

	oracleRes, oracleErr := RunOracle(ctx, oraclePath, ws.Root, oracleEnv, timeout)
	row.OracleExitCode = oracleRes.ExitCode
	row.OracleDetailsJSON = oracleRes.DetailsJSON
	row.OracleMetricsJSON = oracleRes.MetricsJSON
	row.Passed = oracleRes.Passed
	row.FinishedAtUnix = time.Now().Unix()
	row.DurationMS = (row.FinishedAtUnix - row.StartedAtUnix) * 1000

	// Bad JSON or extra stdout lines: still emit a row (passed=false),
	// exit 1. The extra-line case is a workload bug per 13 号 §1.3;
	// the harness records the failure but keeps the CSV pipeline moving.
	if errors.Is(oracleErr, ErrOracleStdoutNotJSON) || errors.Is(oracleErr, ErrOracleStdoutHasExtraLines) {
		fmt.Fprintln(stderr, oracleErr)
		if errors.Is(oracleErr, ErrOracleStdoutHasExtraLines) {
			// Contract violation: force the row's Passed to false so a
			// well-formed but extra-line oracle can't sneak past.
			row.Passed = false
		}
		if row.OracleDetailsJSON == "" {
			row.OracleDetailsJSON = "{}"
		}
		if row.OracleMetricsJSON == "" {
			row.OracleMetricsJSON = "{}"
		}
		if err := WriteCSV(opts.OutCSV, row); err != nil {
			fmt.Fprintln(stderr, err)
			return Result{Row: row, ExitCode: 2, Err: err}
		}
		return Result{Row: row, ExitCode: 1, Err: oracleErr}
	}
	// Oversized stdout: pre-flight abort, no CSV.
	if errors.Is(oracleErr, ErrOracleOutputTooLarge) {
		fmt.Fprintln(stderr, oracleErr)
		return Result{Row: row, ExitCode: 2, Err: oracleErr}
	}
	if oracleErr != nil {
		fmt.Fprintln(stderr, oracleErr)
		return Result{Row: row, ExitCode: 3, Err: oracleErr}
	}
	if row.OracleDetailsJSON == "" {
		row.OracleDetailsJSON = "{}"
	}
	if row.OracleMetricsJSON == "" {
		row.OracleMetricsJSON = "{}"
	}

	if err := WriteCSV(opts.OutCSV, row); err != nil {
		fmt.Fprintln(stderr, err)
		return Result{Row: row, ExitCode: 2, Err: err}
	}

	if row.Passed {
		return Result{Row: row, ExitCode: 0}
	}
	return Result{Row: row, ExitCode: 1}
}

// ErrWorkloadIDPathTraversal is returned by loadWorkloadSpec when the
// caller passed an id containing a path separator or `..`. The id
// segment must be a bare directory name — accepting arbitrary paths
// would let `--workload ../../etc/whatever` read files outside the
// intended workloads root, exposing existence oracles on the host
// (the spec.id equality check downstream would catch the mismatch,
// but the stat has already happened).
var ErrWorkloadIDPathTraversal = errors.New("harness: workload id must be a bare directory name (no path separators, no ..)")

// loadWorkloadSpec parses <workloadDir>/<id>/spec.yaml into a
// WorkloadSpec plus the fully-resolved spec path.
func loadWorkloadSpec(dir, id string) (*WorkloadSpec, error) {
	if id == "" {
		return nil, errors.New("harness: workload id is empty")
	}
	// Path-traversal guard: `..`, `/`, `\`, or a `Clean` that changes
	// the id (e.g. `.`, empty segments) is rejected. This runs BEFORE
	// filepath.Join so we never read a file outside the workloads root.
	if id == "." || id == ".." ||
		strings.ContainsAny(id, `/\`) ||
		strings.Contains(id, "..") ||
		filepath.Clean(id) != id {
		return nil, fmt.Errorf("%w: %q", ErrWorkloadIDPathTraversal, id)
	}
	specPath := filepath.Join(dir, id, "spec.yaml")
	abs, err := filepath.Abs(specPath)
	if err != nil {
		return nil, fmt.Errorf("harness: abs spec path: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("harness: read spec: %w", err)
	}
	var spec WorkloadSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("harness: parse spec: %w", err)
	}
	if spec.ID != id {
		return nil, fmt.Errorf("harness: spec.id %q does not match dir name %q", spec.ID, id)
	}
	if spec.SuccessOracle == "" {
		return nil, errors.New("harness: spec.success_oracle is empty")
	}
	spec.SpecPath = abs
	return &spec, nil
}

// deriveRunID produces "brun-<unix>-<baseline>-<workload>-<hex>". The
// baseline / workload segments already pass the name regex; hex adds 6
// bytes of collision resistance.
func deriveRunID(baseline, workload string) string {
	var buf [3]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("brun-%d-%s-%s-%s", time.Now().Unix(), baseline, workload, hex.EncodeToString(buf[:]))
}
