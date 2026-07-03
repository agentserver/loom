package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

// bashPath is the ONLY subprocess binary this baseline ever invokes.
// Spec §7(c): manual_ssh must not shell out to ssh/scp/rsync/sftp. The
// constant lives at package scope so a code review (and the runtime
// test) can catch any drift; every ExecuteAgent path routes through it.
const bashPath = "/bin/bash"

// ErrManualSSHWorkloadUnknown is returned when the invoker asks for a
// workload id we don't have a hand-written bash script for. Adding
// support is a one-line addition to workloadScripts.
var ErrManualSSHWorkloadUnknown = errors.New("manual_ssh: workload has no bash script; add one to workloadScripts")

// ManualSSHImpl is the harness.BaselineImpl for the E1 manual_ssh
// baseline. In real mode ExecuteAgent runs a per-workload local bash
// snippet (workloadScripts). In dry-run mode ExecuteAgent is a no-op —
// the harness has already projected fixtures/mock_workspace into
// ws.Root, so the oracle sees identical bytes.
type ManualSSHImpl struct {
	// workloadID is captured so ExecuteAgent can look the script up
	// without re-parsing Opts.
	workloadID string
}

// NewImpl constructs a ManualSSHImpl. Passing the workload id in via
// constructor keeps the interface method surface stable (BaselineImpl
// does not carry Opts).
func NewImpl(workloadID string) *ManualSSHImpl { return &ManualSSHImpl{workloadID: workloadID} }

// Name returns the default baseline_or_ablation value.
func (*ManualSSHImpl) Name() string { return "manual_ssh" }

// AgentForwards for manual_ssh is always nil — this baseline has no
// third-party credential to forward. Spec §7(a) makes this explicit:
// manual_ssh registers NO --forward-*-api-key flag.
func (*ManualSSHImpl) AgentForwards() harness.AgentForwards { return nil }

// Prepare is a no-op for manual_ssh — this baseline has no external
// side effects to arrange up-front.
func (*ManualSSHImpl) Prepare(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) error {
	return nil
}

// ExecuteAgent runs the per-workload bash script under `/bin/bash -c`
// with `cwd = ws.Root` so writes land in the workspace tempdir.
//
// dryRun branch: skip the script. Harness has already flattened
// mock_workspace into ws.Root; that IS the "agent output" in dry-run.
// Logged `[DRY-RUN]` on stderr for the operator.
func (m *ManualSSHImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	if dryRun {
		fmt.Fprintln(os.Stderr, "manual_ssh: [DRY-RUN] skipping local script; using mock_workspace projection")
		return harness.ExecuteMetrics{WallTimeMS: 0, APICalls: 0, UploadBytes: 0}, nil
	}
	script, ok := workloadScripts[m.workloadID]
	if !ok {
		return harness.ExecuteMetrics{}, fmt.Errorf("%w: %s", ErrManualSSHWorkloadUnknown, m.workloadID)
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, bashPath, "-c", script)
	cmd.Dir = ws.Root
	cmd.Env = agentEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("manual_ssh: script failed for %s: %w; stderr=%s", m.workloadID, err, stderr.String())
	}
	return harness.ExecuteMetrics{
		WallTimeMS: time.Since(start).Milliseconds(),
	}, nil
}
