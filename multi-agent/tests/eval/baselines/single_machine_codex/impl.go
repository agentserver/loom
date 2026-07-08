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
	"regexp"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

// bearerRE augments internal/secretscrub.Sanitize with a `Bearer <token>`
// pattern that the shared scrubber does not cover today. Spec §5 lists
// `Bearer` alongside `sk-*` / `ghp_*` / etc as a stderr leak shape that
// MUST be redacted before it reaches res.Err / row.OracleDetailsJSON.
// Modifying internal/secretscrub is out-of-scope for a rename PR; we
// pre-scrub Bearer locally, then let Sanitize handle the rest.
//
// Regex: `Bearer` (case-insensitive) followed by ANY separator run of
// whitespace / `:` / `=` (HTTP header dumps use `Authorization: Bearer …`
// while curl-style dumps use `Bearer=…`, and JSON dumps use
// `"Bearer <token>"`) followed by 8+ chars from the standard OAuth 2
// Bearer token character class ([A-Za-z0-9._~+/-]+ with optional
// trailing `=` per RFC 6750 §2.1). 8+ is loose enough to catch
// test-shaped values while keeping false positives cheap (redaction
// to [REDACTED] is idempotent so a false positive is a harmless
// cosmetic swap).
//
// Fresh-review P1: earlier `Bearer\s+` form missed `Bearer=…` and
// `Bearer:…` diagnostics — now covers all three separator styles.
var bearerRE = regexp.MustCompile(`(?i)Bearer[\s:=]+[A-Za-z0-9._~+/\-]{8,}=*`)

func scrubStderr(s string) string {
	// Pre-scrub Bearer prefix (secretscrub.Sanitize does not),
	// then delegate the token-family regexes it does cover.
	s = bearerRE.ReplaceAllString(s, "[REDACTED]")
	return secretscrub.Sanitize(s)
}

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
		// Scrub stderr before embedding — codex may emit token-shaped
		// bytes (auth errors, config dumps). Runs on the nonzero-exit
		// leak path (spec §5 TestExecuteAgent_ScrubsStderr).
		scrubbed := scrubStderr(stderr.String())
		return harness.ExecuteMetrics{
			WallTimeMS: time.Since(start).Milliseconds(),
		}, fmt.Errorf("single_machine_codex: codex CLI failed for %s: %w; stderr=%s", s.workloadID, err, scrubbed)
	}
	// Verify codex produced the expected outputs.
	for _, outName := range prompt.ExpectedOutputs {
		if _, err := os.Stat(filepath.Join(ws.Root, outName)); err != nil {
			// Success (exit 0) but expected output missing. Still
			// scrub stderr — stderr may contain diagnostic output
			// with token-shaped bytes.
			scrubbed := scrubStderr(stderr.String())
			return harness.ExecuteMetrics{
					WallTimeMS: time.Since(start).Milliseconds(),
				},
				fmt.Errorf("single_machine_codex: codex did not produce %q for %s: %w; stderr=%s", outName, s.workloadID, err, scrubbed)
		}
	}
	return harness.ExecuteMetrics{
		WallTimeMS: time.Since(start).Milliseconds(),
	}, nil
}
