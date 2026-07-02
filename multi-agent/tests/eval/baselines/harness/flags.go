package harness

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

// ErrFlagUsage is returned by ParseFlags when flag parsing itself fails
// (bad arg, unknown flag). Callers map this to exit 2 without further
// action; the flag package has already printed usage to Stderr.
var ErrFlagUsage = errors.New("harness: flag parse failed")

// Opts is the shared per-invocation configuration for every baseline.
// Cloud-specific extras live in the baseline binary's own main.go and
// are threaded into BaselineImpl via constructor args.
type Opts struct {
	WorkloadID   string
	WorkloadDir  string
	BaselineName string
	RunID        string
	Timeout      time.Duration
	OutCSV       string
	DryRun       bool
	KeepTempdir  bool
}

// NewFlagSet builds the shared flag set + Opts destination. The caller
// registers any baseline-specific flags on the returned FlagSet, then
// calls fs.Parse(args); Opts is populated in place.
//
// Note: Opts is returned by pointer so the pointers the FlagSet binds
// remain valid after this function returns.
func NewFlagSet(name string, out io.Writer) (*Opts, *flag.FlagSet) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)

	opts := &Opts{}
	fs.StringVar(&opts.WorkloadID, "workload", "", "workload id (e.g. cross-device-code-mod)")
	fs.StringVar(&opts.WorkloadDir, "workload-dir", "tests/eval/workloads", "directory holding <workload>/spec.yaml")
	fs.StringVar(&opts.BaselineName, "baseline-name", "", "override the default baseline_or_ablation value; must match [a-z][a-z0-9_-]{2,63}")
	fs.StringVar(&opts.RunID, "run-id", "", "explicit run id; default derived")
	fs.DurationVar(&opts.Timeout, "timeout", 0, "override spec.timeout_seconds")
	fs.StringVar(&opts.OutCSV, "out", "", "CSV output path; required")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "skip external side effects (claude CLI / cloud API); mock_workspace is projected instead")
	fs.BoolVar(&opts.KeepTempdir, "keep-tempdir", false, "retain the workspace tempdir on exit (debug)")

	return opts, fs
}

// ValidateOpts checks the post-parse Opts for the fields ParseFlags
// cannot enforce (mandatory strings, name regex). Returns exit-2 code on
// failure with a message on `out`.
func ValidateOpts(opts *Opts, out io.Writer) int {
	if opts.WorkloadID == "" {
		fmt.Fprintln(out, "harness: --workload is required")
		return 2
	}
	if opts.OutCSV == "" {
		fmt.Fprintln(out, "harness: --out is required")
		return 2
	}
	if err := ValidateBaselineName(opts.BaselineName); err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 2
	}
	return 0
}
