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
		fmt.Fprintln(os.Stderr, "usage: cloud_sandbox run --workload <id> --out <csv> [--dry-run] [--forward-e2b-api-key] [--e2b-api-key-env NAME] [--container-codex] [--workload-dir <path>]")
		os.Exit(2)
	}
	os.Exit(runMain(os.Args[2:]))
}

func runMain(args []string) int {
	opts, fs := harness.NewFlagSet("cloud_sandbox run", os.Stderr)
	forwardE2B := fs.Bool("forward-e2b-api-key", false, "opt-in: read the env var named by --e2b-api-key-env and pass its value to the E2B HTTP client. Never enters any subprocess env.")
	apiKeyEnv := fs.String("e2b-api-key-env", "E2B_API_KEY", "env var name the harness reads the E2B API key from when --forward-e2b-api-key is set.")
	containerCodex := fs.Bool("container-codex", false, "run the cloud sandbox baseline through a local Docker container containing Codex instead of E2B.")
	containerCodexDocker := fs.String("container-codex-docker", defaultContainerDockerBin, "docker binary used by --container-codex.")
	containerCodexImage := fs.String("container-codex-image", defaultContainerCodexImage, "container image used by --container-codex.")
	containerCodexBin := fs.String("container-codex-bin", defaultContainerCodexBin, "Codex executable inside the --container-codex image.")
	containerCodexHome := fs.String("container-codex-home", "", "source Codex home directory whose config.toml is copied into the container; defaults to CODEX_HOME or ~/.codex.")
	containerCodexNodeModule := fs.String("container-codex-node-module", defaultContainerCodexNodeModule, "optional host @openai/codex node module directory mounted read-only into the container.")
	containerCodexNetwork := fs.String("container-codex-network", defaultContainerCodexNetwork, "docker network used by --container-codex; empty disables --network.")
	forwardOpenAI := fs.Bool("forward-openai-api-key", false, "opt-in: forward OPENAI_API_KEY to the Codex container when --container-codex is set.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, harness.ErrFlagUsage) {
			return 2
		}
		return 2
	}
	if opts.BaselineName == "" {
		opts.BaselineName = (&CloudSandboxE2BImpl{containerCodex: *containerCodex}).Name()
	}
	if code := harness.ValidateOpts(opts, os.Stderr); code != 0 {
		return code
	}
	// §7(h) CI must skip real cloud calls. When CI=true in env AND
	// --dry-run is NOT set, refuse the run unless the operator selected
	// local container isolation instead of E2B.
	if os.Getenv("CI") == "true" && !opts.DryRun && !*containerCodex {
		fmt.Fprintln(os.Stderr, "cloud_sandbox: CI=true detected but neither --dry-run nor --container-codex is set; refusing (spec §7(h)). Set --dry-run, set --container-codex, or unset CI to override.")
		return 2
	}

	// Read the E2B API key from the operator-nominated env var ONLY
	// when the operator opted in. Spec §7(a): no implicit forwarding.
	var apiKey string
	if *forwardE2B {
		apiKey = os.Getenv(*apiKeyEnv)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	impl := NewImpl(opts.WorkloadID, *forwardE2B, apiKey, opts.DryRun, os.Stderr)
	impl.containerCodex = *containerCodex
	impl.forwardOpenAI = *forwardOpenAI
	impl.dockerBin = *containerCodexDocker
	impl.containerImage = *containerCodexImage
	impl.containerCodexBin = *containerCodexBin
	impl.containerCodexHome = *containerCodexHome
	impl.containerCodexNodeModule = *containerCodexNodeModule
	impl.containerCodexNetwork = *containerCodexNetwork
	res := harness.Run(ctx, *opts, impl, os.Stderr)
	return res.ExitCode
}
