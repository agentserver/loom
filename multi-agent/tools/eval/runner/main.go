// Command eval-runner executes one E1 macrobenchmark workload end-to-end —
// agentserver-stub up, oracle invocation, commit_meta collection, redacted
// run row to CSV. See docs/specs/wt1-eval-runner-skeleton.spec.md.
//
// ⚠️  NOT FOR PRODUCTION. Bypasses OAuth via agentserver-stub.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = `eval-runner — Phase 1 evaluation harness skeleton.

Usage:
  eval-runner run --workload <id> --stub-listen <host:port> --out <csv> [flags]

Run "eval-runner run -h" for the full flag list.`

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	exitCode := runMain(os.Args[2:])
	os.Exit(exitCode)
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		workload    = fs.String("workload", "", "workload id (e.g. cross-device-code-mod)")
		workloadDir = fs.String("workload-dir", "multi-agent/tests/eval/workloads", "directory containing <workload>/spec.yaml")
		stubListen  = fs.String("stub-listen", "127.0.0.1:18080", "agentserver-stub --listen address; MUST be loopback")
		observerDB  = fs.String("observer-db", "", "SQLite DB for run schema; empty = NoopWriter")
		codexConfig = fs.String("codex-config", "", "path to codex config.toml (passed through; recorded only)")
		runID       = fs.String("run-id", "", "explicit run id; default = derived")
		timeout     = fs.Duration("timeout", 0, "override spec.timeout_seconds")
		outCSV      = fs.String("out", "", "output CSV path; required")
		keep        = fs.Bool("keep-tempdir", false, "do not delete tempdir at exit (debug)")
	)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *workload == "" {
		fmt.Fprintln(os.Stderr, "eval-runner: --workload is required")
		return 2
	}
	if *outCSV == "" {
		fmt.Fprintln(os.Stderr, "eval-runner: --out is required")
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	res := Run(ctx, Opts{
		WorkloadID:      *workload,
		WorkloadDir:     *workloadDir,
		StubListen:      *stubListen,
		ObserverDB:      *observerDB,
		CodexConfigPath: *codexConfig,
		RunID:           *runID,
		Timeout:         *timeout,
		OutCSV:          *outCSV,
		KeepTempdir:     *keep,
	})
	return res.ExitCode
}
