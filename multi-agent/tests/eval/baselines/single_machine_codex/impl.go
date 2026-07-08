// Package main is the single_machine_codex baseline binary. Real mode
// invokes the OpenAI Codex CLI (`codex exec`) with the pinned §4.1
// argv; dry-run mode projects mock_workspace and never touches the
// network.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

// ErrCodexCLIUnavailable is returned by real-mode ExecuteAgent when
// the `codex` binary is not on $PATH. Symmetric to the
// single_machine (Claude) ErrClaudeCLIUnavailable; spec §3 fail-fast.
var ErrCodexCLIUnavailable = errors.New("single_machine_codex: `codex` binary not on $PATH; real mode requires OpenAI Codex CLI")

// ErrSingleMachineCodexWorkloadUnknown flags a workload id without a
// prompt entry in codexPrompts.
var ErrSingleMachineCodexWorkloadUnknown = errors.New("single_machine_codex: workload has no prompt; add one to codexPrompts")

// SingleMachineCodexImpl is the harness.BaselineImpl for the codex
// single-machine baseline. Real mode invokes the LOCKED §4.1 argv;
// dry-run mode projects mock_workspace only.
type SingleMachineCodexImpl struct {
	workloadID    string
	forwardOpenAI bool
	// codexBin is the path to the `codex` binary. Empty ⇒ resolved
	// via exec.LookPath("codex") at ExecuteAgent time. Tests inject
	// a fake path here.
	codexBin string
}

// NewImpl constructs the impl. `forwardOpenAI` reflects the operator's
// opt-in via --forward-openai-api-key (spec §3).
func NewImpl(workloadID string, forwardOpenAI bool) *SingleMachineCodexImpl {
	return &SingleMachineCodexImpl{workloadID: workloadID, forwardOpenAI: forwardOpenAI}
}

// Name returns the default baseline_or_ablation value.
func (*SingleMachineCodexImpl) Name() string { return "single_machine_codex" }

// AgentForwards is either empty or [OPENAI_API_KEY] depending on
// whether the operator passed --forward-openai-api-key. Spec §3:
// no implicit-by-baseline forwarding.
func (s *SingleMachineCodexImpl) AgentForwards() harness.AgentForwards {
	if s.forwardOpenAI {
		return harness.AgentForwards{"OPENAI_API_KEY"}
	}
	return nil
}

// Prepare is a no-op — this baseline has no external side effects to
// arrange up-front.
func (*SingleMachineCodexImpl) Prepare(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) error {
	return nil
}

// ExecuteAgent invokes the LOCKED §4.1 codex exec argv in the workspace
// tempdir with the per-workload prompt. Dry-run mode short-circuits
// before any external invocation. Stderr scrub added in Task 4.
func (s *SingleMachineCodexImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	if dryRun {
		fmt.Fprintln(os.Stderr, "single_machine_codex: [DRY-RUN] skipping `codex exec` invocation; using mock_workspace projection")
		return harness.ExecuteMetrics{WallTimeMS: 0, APICalls: 0, UploadBytes: 0}, nil
	}
	prompt, ok := codexPrompts[s.workloadID]
	if !ok {
		return harness.ExecuteMetrics{}, fmt.Errorf("%w: %s", ErrSingleMachineCodexWorkloadUnknown, s.workloadID)
	}
	bin := s.codexBin
	if bin == "" {
		resolved, err := exec.LookPath("codex")
		if err != nil {
			return harness.ExecuteMetrics{}, fmt.Errorf("%w: %v", ErrCodexCLIUnavailable, err)
		}
		bin = resolved
	}

	// LOCKED argv (spec §4.1 + Global Constraints). Do not add or
	// remove flags without updating TestSingleMachineCodex_UsesPinnedArgv.
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin,
		"exec",
		"--sandbox", "workspace-write",
		"--ephemeral",
		"--skip-git-repo-check",
		"--json",
		"-C", ws.Root,
		"--",
		prompt.Prompt,
	)
	cmd.Dir = ws.Root
	cmd.Env = agentEnv
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("single_machine_codex: codex CLI failed for %s: %w; stderr=%s", s.workloadID, err, stderr.String())
	}
	// Verify codex produced the expected outputs.
	for _, outName := range prompt.ExpectedOutputs {
		if _, err := os.Stat(filepath.Join(ws.Root, outName)); err != nil {
			return harness.ExecuteMetrics{
					WallTimeMS: time.Since(start).Milliseconds(),
				},
				fmt.Errorf("single_machine_codex: codex did not produce %q for %s: %w", outName, s.workloadID, err)
		}
	}
	return harness.ExecuteMetrics{
		WallTimeMS: time.Since(start).Milliseconds(),
	}, nil
}
