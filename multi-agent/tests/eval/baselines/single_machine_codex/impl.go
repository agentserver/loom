// Package main is the single_machine_codex baseline binary. Real mode
// invokes the OpenAI Codex CLI (`codex exec`); dry-run mode projects
// mock_workspace and never touches the network.
package main

import (
	"context"
	"errors"

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

// ExecuteAgent is a stub in Task 1; concrete argv + scrub added in
// Task 3.
func (s *SingleMachineCodexImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	return harness.ExecuteMetrics{}, errors.New("not implemented; see Task 3")
}
