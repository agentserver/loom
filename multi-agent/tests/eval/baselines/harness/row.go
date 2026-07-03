// Package harness is the shared code all three baselines under
// tests/eval/baselines/{manual_ssh,single_machine,cloud_sandbox} depend
// on. It owns everything that is not baseline-specific: flag parsing,
// workspace tempdir setup, env whitelist, pre-upload secret scan, oracle
// invocation, CSV row emission.
//
// See docs/specs/wt2-baselines.spec.md for the design of this package
// and the BaselineImpl seam.
package harness

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrBaselineNameInvalid is returned by ValidateBaselineName when the
// candidate string does not match the regex documented in spec §7(d).
// The regex is the only sanitiser we run on baseline_or_ablation before
// persisting; downstream storage layers trust the value verbatim, so
// this must fail hard on any suspicious input rather than trimming or
// normalising.
var ErrBaselineNameInvalid = errors.New("harness: baseline_or_ablation must match ^[a-z][a-z0-9_-]{2,63}$")

// baselineNameRE is the compiled form of spec §7(d)'s validator. The
// leading `[a-z]` prevents leading digits (so the value is a valid
// identifier fragment for scripts that grep on it) and the
// `[a-z0-9_-]{2,63}` tail restricts the total length to [3, 64] runes.
var baselineNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)

// ValidateBaselineName rejects any string that would be unsafe to land
// in the D1 `runs.baseline_or_ablation` column or the CSV output. Empty
// input is rejected the same as any other invalid value — callers must
// supply a name (the per-baseline binary defaults it before validation
// is called, so an empty string here signals a bug not a "no default").
func ValidateBaselineName(s string) error {
	if !baselineNameRE.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrBaselineNameInvalid, s)
	}
	return nil
}

// BaselineRunRow is the persistence shape emitted by every baseline run.
// Field order matches spec §5 columns 1–15 exactly; the CSV writer walks
// this struct via reflection-free direct access so the header + row are
// guaranteed to stay in step.
//
// When the D1 `runs` table lands (WT-1-run-schema follow-up), columns
// 1–10 + 15 map 1:1 into runs; column 3 → runs.baseline_or_ablation;
// columns 11–14 fold into runs.artifact_hashes_json.baseline_metrics.
type BaselineRunRow struct {
	// 1
	RunID string
	// 2
	WorkloadID string
	// 3 — validated against baselineNameRE before assignment.
	BaselineOrAblation string
	// 4
	StartedAtUnix int64
	// 5
	FinishedAtUnix int64
	// 6
	DurationMS int64
	// 7
	Passed bool
	// 8
	OracleExitCode int
	// 9 — the oracle's `details` sub-object serialized back to JSON.
	OracleDetailsJSON string
	// 10 — the oracle's `metrics` sub-object serialized back to JSON.
	OracleMetricsJSON string
	// 11
	DryRun bool
	// 12–14 — cost accounting per spec §7(g). All three are always
	// populated; for non-cloud baselines APICalls and UploadBytes are 0.
	MetricsBaselineWallTimeMS  int64
	MetricsBaselineAPICalls    int
	MetricsBaselineUploadBytes int64
	// 15
	TempdirKept bool
}
