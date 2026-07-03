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

// ErrClaudeCLIUnavailable is returned by real-mode ExecuteAgent when
// the `claude` binary is not on $PATH. Spec §E2: fail fast rather than
// silently degrading to the dry-run stub — an operator asking for a
// real run must see the missing dependency, not a suspiciously-clean
// mock_workspace projection.
var ErrClaudeCLIUnavailable = errors.New("single_machine: `claude` binary not on $PATH; real mode requires Claude Code CLI")

// ErrSingleMachineWorkloadUnknown flags a workload id without a prompt
// entry in claudePrompts.
var ErrSingleMachineWorkloadUnknown = errors.New("single_machine: workload has no prompt; add one to claudePrompts")

// SingleMachineClaudeCodeImpl is the harness.BaselineImpl for §E2. Real
// mode `claude -p <prompt>` in the workspace tempdir; dry-run mode
// projects mock_workspace only.
type SingleMachineClaudeCodeImpl struct {
	workloadID       string
	forwardAnthropic bool
	// claudeBin is the path to the `claude` binary. Empty ⇒ resolved
	// via `exec.LookPath("claude")` at ExecuteAgent time. Tests inject
	// a fake path here.
	claudeBin string
}

// NewImpl constructs the impl. `forwardAnthropic` reflects the
// operator's opt-in via --forward-anthropic-api-key (spec §7(a)).
func NewImpl(workloadID string, forwardAnthropic bool) *SingleMachineClaudeCodeImpl {
	return &SingleMachineClaudeCodeImpl{workloadID: workloadID, forwardAnthropic: forwardAnthropic}
}

// Name returns the default baseline_or_ablation value.
func (*SingleMachineClaudeCodeImpl) Name() string { return "single_machine_claude_code" }

// AgentForwards is either empty or [ANTHROPIC_API_KEY] depending on
// whether the operator passed --forward-anthropic-api-key. Spec §7(a):
// no implicit-by-baseline forwarding.
func (s *SingleMachineClaudeCodeImpl) AgentForwards() harness.AgentForwards {
	if s.forwardAnthropic {
		return harness.AgentForwards{"ANTHROPIC_API_KEY"}
	}
	return nil
}

// Prepare is a no-op — this baseline has no external side effects to
// arrange up-front.
func (*SingleMachineClaudeCodeImpl) Prepare(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) error {
	return nil
}

// ExecuteAgent invokes the `claude` CLI in the workspace tempdir with
// the per-workload prompt. Dry-run mode short-circuits before any
// external invocation.
func (s *SingleMachineClaudeCodeImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	if dryRun {
		fmt.Fprintln(os.Stderr, "single_machine: [DRY-RUN] skipping `claude` invocation; using mock_workspace projection")
		return harness.ExecuteMetrics{WallTimeMS: 0, APICalls: 0, UploadBytes: 0}, nil
	}
	prompt, ok := claudePrompts[s.workloadID]
	if !ok {
		return harness.ExecuteMetrics{}, fmt.Errorf("%w: %s", ErrSingleMachineWorkloadUnknown, s.workloadID)
	}
	bin := s.claudeBin
	if bin == "" {
		resolved, err := exec.LookPath("claude")
		if err != nil {
			return harness.ExecuteMetrics{}, fmt.Errorf("%w: %v", ErrClaudeCLIUnavailable, err)
		}
		bin = resolved
	}

	// Guard: real mode without a forwarded key would still shell out
	// and let `claude` fail its own auth check. That's the intended
	// behaviour — spec §7(a): "the CLI errors out visibly; the operator
	// learns 'I asked for a real run without opting in to key
	// forwarding' rather than silently getting a leak-shaped
	// configuration". Do NOT auto-forward here.

	start := time.Now()
	// Non-interactive prompt mode: `-p <prompt>` is the current CLI
	// contract. Response is echoed to stdout; the CLI is expected to
	// perform file writes into cwd via its tool loop.
	cmd := exec.CommandContext(ctx, bin, "-p", prompt.Prompt)
	cmd.Dir = ws.Root
	cmd.Env = agentEnv
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("single_machine: claude CLI failed for %s: %w; stderr=%s", s.workloadID, err, stderr.String())
	}
	// Verify claude actually produced the outputs the workload's oracle
	// will look for. If any are missing, surface as a runtime failure so
	// the CSV lands with details showing "which output missing" rather
	// than the opaque oracle "file missing" error later.
	for _, out := range prompt.ExpectedOutputs {
		if _, err := os.Stat(filepath.Join(ws.Root, out)); err != nil {
			return harness.ExecuteMetrics{
					WallTimeMS: time.Since(start).Milliseconds(),
				},
				fmt.Errorf("single_machine: claude did not produce %q for %s: %w", out, s.workloadID, err)
		}
	}
	return harness.ExecuteMetrics{
		WallTimeMS: time.Since(start).Milliseconds(),
	}, nil
}
