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
  eval-runner --list-ablations

Run "eval-runner run -h" for the full flag list.`

func main() {
	// WT-2-flag-integration §2.3 + §7(a.2): scrub any inherited
	// LOOM_ABLATION_* env vars FIRST, before flag parsing, so a
	// parent process's stray value cannot silently activate an
	// ablation the operator did not request. Idempotent no-op
	// when no bridge vars are present.
	ScrubAmbientAblationEnv()

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	// --list-ablations is a top-level verb: print the registered
	// flag names + one-line descriptions, exit 0. Mutually
	// exclusive with `run`.
	if os.Args[1] == "--list-ablations" {
		fmt.Print(ListAblationsText())
		os.Exit(0)
	}
	if os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	exitCode := runMain(os.Args[2:])
	os.Exit(exitCode)
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		workload        = fs.String("workload", "", "workload id (e.g. cross-device-code-mod)")
		workloadDir     = fs.String("workload-dir", "multi-agent/tests/eval/workloads", "directory containing <workload>/spec.yaml")
		stubListen      = fs.String("stub-listen", "127.0.0.1:18080", "agentserver-stub --listen address; MUST be loopback")
		observerDB      = fs.String("observer-db", "", "SQLite DB for run schema; empty = NoopWriter")
		codexConfig     = fs.String("codex-config", "", "path to codex config.toml (passed through; recorded only)")
		codexConfigPath = fs.String("codex-config-path", "", "WT-2: filesystem path to a codex config.toml; validated against --codex-config-mode; must resolve under the repo or /tmp")
		codexConfigMode = fs.String("codex-config-mode", "", "WT-2: \"a\" = local-proxy (experimental_bearer_token) or \"b\" = upstream-direct (env_key=OPENAI_API_KEY); enforces the auth-field / env-var preconditions")
		codexUsageJSONL = fs.String("codex-usage-jsonl", "", "path to Codex CLI JSONL usage events; records model_input_tokens/model_output_tokens")
		runID           = fs.String("run-id", "", "explicit run id; default = derived")
		timeout         = fs.Duration("timeout", 0, "override spec.timeout_seconds")
		outCSV          = fs.String("out", "", "output CSV path; required")
		keep            = fs.Bool("keep-tempdir", false, "do not delete tempdir at exit (debug)")
		baselineName    = fs.String("baseline-name", DefaultBaselineName, "value stamped into runs.baseline_or_ablation when no --ablation is passed; must match ^[a-z][a-z0-9_-]{2,63}$")
	)
	// WT-2-flag-integration §2.3: --ablation is a repeatable
	// flag.Value; each --ablation value may be one name or a
	// comma-joined list. Rejects unknown names at parse time.
	var ablationList AblationList
	fs.Var(&ablationList, "ablation", "ablation flag name; repeat or comma-join for multiple; see --list-ablations")
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

	// --codex-config-path wins over --codex-config for the recorded
	// path when both are supplied (WT-2 spec §2.2). Callers that only
	// pass --codex-config keep PR #53 semantics.
	recordedCodexPath := *codexConfig
	if *codexConfigPath != "" {
		recordedCodexPath = *codexConfigPath
	}

	res := Run(ctx, Opts{
		WorkloadID:      *workload,
		WorkloadDir:     *workloadDir,
		StubListen:      *stubListen,
		ObserverDB:      *observerDB,
		CodexConfigPath: recordedCodexPath,
		CodexConfigMode: *codexConfigMode,
		CodexUsageJSONL: *codexUsageJSONL,
		RunID:           *runID,
		Timeout:         *timeout,
		OutCSV:          *outCSV,
		KeepTempdir:     *keep,
		AblationFlags:   ablationList.Values(),
		BaselineName:    *baselineName,
	})
	return res.ExitCode
}
