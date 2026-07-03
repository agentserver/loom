package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: manual_ssh run --workload <id> --out <csv> [--dry-run] [--baseline-name <name>] [--workload-dir <path>]")
		os.Exit(2)
	}
	os.Exit(runMain(os.Args[2:]))
}

func runMain(args []string) int {
	opts, fs := harness.NewFlagSet("manual_ssh run", os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, harness.ErrFlagUsage) {
			return 2
		}
		return 2
	}
	if opts.BaselineName == "" {
		opts.BaselineName = (&ManualSSHImpl{}).Name()
	}
	if code := harness.ValidateOpts(opts, os.Stderr); code != 0 {
		return code
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	res := harness.Run(ctx, *opts, NewImpl(opts.WorkloadID), os.Stderr)
	return res.ExitCode
}
