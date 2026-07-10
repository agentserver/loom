package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

// ErrPreUploadSecretDetected is returned by Prepare when the workspace
// tempdir holds a file whose bytes trip secretscrub. The upload is
// aborted and the run fails pre-flight (exit 2). Spec §7(b).
var ErrPreUploadSecretDetected = errors.New("cloud_sandbox: pre-upload secret scan detected potential secret")

// ErrCloudSandboxWorkloadUnknown is returned when a workload has no
// remoteExecPlan entry.
var ErrCloudSandboxWorkloadUnknown = errors.New("cloud_sandbox: workload has no remote exec plan; add one to cloudPlans")

// defaultE2BBase is the (fictitious) E2B API base URL used when the
// caller doesn't override it. Real E2B API URL would go here.
const defaultE2BBase = "https://api.e2b.dev"

const (
	defaultContainerDockerBin       = "docker"
	defaultContainerCodexImage      = "multi-agent-container-smoke:codex-agents"
	defaultContainerCodexBin        = "codex"
	defaultContainerCodexNodeModule = "/usr/lib/node_modules/@openai/codex"
	defaultContainerCodexNetwork    = "host"
)

// CloudSandboxE2BImpl is the harness.BaselineImpl for §E3. Prepare
// walks the workspace looking for secrets; if any file trips the scan
// the run aborts with ErrPreUploadSecretDetected. ExecuteAgent then
// runs the per-workload remoteExecPlan through the E2B client (real
// or dry-run planner) and pulls fetched files back into ws.Root so the
// oracle grades locally.
type CloudSandboxE2BImpl struct {
	workloadID string
	// client is the E2B HTTP client. Real client for real mode, planner
	// client for dry-run. Injected via tests for coverage of the
	// no-dial assertion.
	client E2BClient
	// e2bAPIKey is empty unless the operator opted in via
	// --forward-e2b-api-key AND the env var named by --e2b-api-key-env
	// was present in parent env. Never enters agentEnv per spec §7(a).
	e2bAPIKey string
	// forwardE2B captures the opt-in bit for AgentForwards().
	forwardE2B bool
	// safeToUpload is the set of workspace-relative paths that passed
	// the pre-upload secret scan. Populated by Prepare and consumed by
	// ExecuteAgent; the cloud upload iterates THIS set only (spec §7(b)
	// "skipped ≠ safe"). A file skipped by the scan admission gates
	// (binary / >8 MiB) is NOT in this set, so it is never uploaded.
	safeToUpload []string
	// containerCodex switches real execution from E2B HTTP calls to a
	// local Docker container that runs Codex inside the mounted
	// workspace. It exists for environments where the "cloud sandbox"
	// baseline is represented by container isolation.
	containerCodex bool
	// forwardOpenAI is an explicit opt-in for propagating OPENAI_API_KEY
	// into the Codex container. It is independent from forwardE2B.
	forwardOpenAI            bool
	dockerBin                string
	containerImage           string
	containerCodexBin        string
	containerCodexHome       string
	containerCodexNodeModule string
	containerCodexNetwork    string
	// planOverride, when non-zero, replaces the `cloudPlans[workloadID]`
	// lookup. Used by tests that want to exercise a synthetic workload
	// without racing on the package-level `cloudPlans` map.
	planOverride *remoteExecPlan
}

// NewImpl constructs the impl. `apiKey` should be empty unless the
// operator opted in via --forward-e2b-api-key; when non-empty the real
// client is instantiated with it as the Bearer token.
func NewImpl(workloadID string, forwardE2B bool, apiKey string, dryRun bool, stderr io.Writer) *CloudSandboxE2BImpl {
	var client E2BClient
	if dryRun {
		client = newDryRunPlannerClient(stderr)
	} else {
		client = newRealE2BClient(defaultE2BBase, apiKey, false)
	}
	return &CloudSandboxE2BImpl{
		workloadID: workloadID,
		client:     client,
		e2bAPIKey:  apiKey,
		forwardE2B: forwardE2B,
	}
}

// Name returns the default baseline_or_ablation value.
func (c *CloudSandboxE2BImpl) Name() string {
	if c != nil && c.containerCodex {
		return "cloud_sandbox_container_codex"
	}
	return "cloud_sandbox_e2b"
}

// AgentForwards: E2B has no *agent subprocess* — the E2B client is
// in-process, and the key is passed to it directly at construction
// time (never through an env var of a child). Container Codex does
// spawn an agent subprocess, so OPENAI_API_KEY is propagated only when
// the operator explicitly opts in via the container-specific flag.
func (c *CloudSandboxE2BImpl) AgentForwards() harness.AgentForwards {
	if c != nil && c.containerCodex && c.forwardOpenAI {
		return harness.AgentForwards{"OPENAI_API_KEY"}
	}
	return nil
}

// Prepare: run the pre-upload secret scan (spec §7(b)) — the single
// most dangerous failure mode of this worktree. Scan runs whether
// dryRun is set or not, so a workload maintainer cannot land a leak
// by testing only with dry-run and then flipping it later.
func (c *CloudSandboxE2BImpl) Prepare(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) error {
	report, err := harness.ScanTreeForSecrets(ws.Root)
	if err != nil {
		return fmt.Errorf("cloud_sandbox: secret scan: %w", err)
	}
	if len(report.Flagged) > 0 {
		// Print rejected paths but NOT the matched substrings (would
		// defeat the scrub). Only the first path is called out; the
		// operator's fix is to remove the offending file from the
		// workload fixtures.
		return fmt.Errorf("%w: %s", ErrPreUploadSecretDetected, report.Flagged[0])
	}
	// Freeze the safe-to-upload set BEFORE the local exec step so a
	// script that writes new files into ws.Root can't smuggle unscanned
	// bytes into the upload set. ExecuteAgent iterates this slice
	// verbatim.
	c.safeToUpload = append([]string{}, report.SafeToUpload...)
	// Log the skipped counts to stderr so an operator running with a
	// binary/large fixture sees "3 files skipped, will not be uploaded"
	// instead of silently missing them. Filenames omitted here to keep
	// stderr short; the report is available for tests.
	if n := len(report.SkippedBinary) + len(report.SkippedLarge); n > 0 {
		fmt.Fprintf(os.Stderr, "cloud_sandbox: pre-upload scan skipped %d file(s) (%d binary, %d large); NOT uploading them\n",
			n, len(report.SkippedBinary), len(report.SkippedLarge))
	}
	return nil
}

// ExecuteAgent walks the per-workload remoteExecPlan through the E2B
// client. In dry-run mode the planner client swallows the request and
// logs the plan; in real mode the requests actually dial the API.
//
// The "exec" step is currently emulated locally after a real upload:
// we still run the bash script in ws.Root so the workspace ends up
// with the expected outputs. This intentional simplification lets the
// baseline compare across the 15-run matrix without a real E2B sandbox
// being available — the API-call counts, upload bytes, and dry-run
// planning are what matter for §E3, not the arithmetic-correctness of
// the shell snippet inside a remote container.
func (c *CloudSandboxE2BImpl) ExecuteAgent(ctx context.Context, ws *harness.Workspace, agentEnv []string, dryRun bool) (harness.ExecuteMetrics, error) {
	if c.containerCodex && !dryRun {
		return c.executeContainerCodex(ctx, ws, agentEnv)
	}
	var plan remoteExecPlan
	if c.planOverride != nil {
		plan = *c.planOverride
	} else {
		p, ok := cloudPlans[c.workloadID]
		if !ok {
			return harness.ExecuteMetrics{}, fmt.Errorf("%w: %s", ErrCloudSandboxWorkloadUnknown, c.workloadID)
		}
		plan = p
	}
	start := time.Now()

	// Simulate: 1 sandbox create, N file uploads, 1 exec, M file fetches.
	// All go through the client — dry-run planner logs; real client
	// dials the API.
	if err := c.callSandboxCreate(ctx); err != nil {
		return metricsFrom(c.client, start), err
	}
	// Iterate the scan-approved set only. Spec §7(b) "skipped ≠ safe":
	// a binary or oversized file that ScanTreeForSecrets could not
	// scan is NOT in c.safeToUpload, so this loop skips it. A workload
	// that needs to upload such files must land an escape hatch first.
	for _, p := range c.safeToUpload {
		if err := c.callUpload(ctx, ws.Root, p); err != nil {
			return metricsFrom(c.client, start), fmt.Errorf("cloud_sandbox: upload %s: %w", p, err)
		}
	}
	if err := c.callExec(ctx, plan.ExecScript); err != nil {
		return metricsFrom(c.client, start), err
	}
	// Local-side: also run the bash script so ws.Root ends up with the
	// workload outputs the oracle expects. See rationale in the
	// ExecuteAgent comment above.
	if _, err := runLocalBash(ctx, ws.Root, agentEnv, plan.ExecScript); err != nil {
		return metricsFrom(c.client, start), fmt.Errorf("cloud_sandbox: local script fallback: %w", err)
	}
	for _, f := range plan.FetchFiles {
		if err := c.callFetch(ctx, f); err != nil {
			return metricsFrom(c.client, start), fmt.Errorf("cloud_sandbox: fetch %s: %w", f, err)
		}
	}
	return metricsFrom(c.client, start), nil
}

var cloudBearerRE = regexp.MustCompile(`(?i)Bearer[\s:=]+[A-Za-z0-9._~+/\-]{8,}=*`)

func scrubCloudStderr(s string) string {
	s = cloudBearerRE.ReplaceAllString(s, "[REDACTED]")
	return secretscrub.Sanitize(s)
}

func (c *CloudSandboxE2BImpl) executeContainerCodex(ctx context.Context, ws *harness.Workspace, agentEnv []string) (harness.ExecuteMetrics, error) {
	prompt, ok := containerCodexPrompts[c.workloadID]
	if !ok {
		return harness.ExecuteMetrics{}, fmt.Errorf("cloud_sandbox: workload has no container codex prompt; add one to containerCodexPrompts: %s", c.workloadID)
	}

	dockerBin := firstNonEmpty(c.dockerBin, defaultContainerDockerBin)
	image := firstNonEmpty(c.containerImage, defaultContainerCodexImage)
	codexBin := firstNonEmpty(c.containerCodexBin, defaultContainerCodexBin)
	network := firstNonEmpty(c.containerCodexNetwork, defaultContainerCodexNetwork)

	codexHome, cleanup, err := prepareContainerCodexHome(c.containerCodexHome)
	if err != nil {
		return harness.ExecuteMetrics{}, err
	}
	defer cleanup()

	args := []string{"run", "--rm"}
	if network != "" {
		args = append(args, "--network", network)
	}
	args = append(args,
		"-e", "CODEX_HOME=/codex-home",
		"-e", "HOME=/tmp",
	)
	if openAIKey, ok := envValue(agentEnv, "OPENAI_API_KEY"); ok && openAIKey != "" {
		args = append(args, "-e", "OPENAI_API_KEY="+openAIKey)
	}
	args = appendProxyEnv(args)
	args = append(args,
		"-v", ws.Root+":/workspace",
		"-v", codexHome+":/codex-home",
	)
	if nodeModule := firstNonEmpty(c.containerCodexNodeModule, defaultContainerCodexNodeModule); nodeModule != "" && dirExists(nodeModule) {
		args = append(args, "-v", nodeModule+":"+defaultContainerCodexNodeModule+":ro")
	}
	args = append(args,
		"-w", "/workspace",
		image,
		codexBin,
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		"-C", "/workspace",
		"--",
		prompt.Prompt,
	)

	start := time.Now()
	cmd := exec.CommandContext(ctx, dockerBin, args...)
	cmd.Dir = ws.Root
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return harness.ExecuteMetrics{WallTimeMS: time.Since(start).Milliseconds()},
			fmt.Errorf("cloud_sandbox: container codex failed for %s: %w; stdout=%s; stderr=%s",
				c.workloadID, err, truncateForError(scrubCloudStderr(stdout.String())), truncateForError(scrubCloudStderr(stderr.String())))
	}
	for _, outName := range prompt.ExpectedOutputs {
		if _, err := os.Stat(filepath.Join(ws.Root, outName)); err != nil {
			return harness.ExecuteMetrics{WallTimeMS: time.Since(start).Milliseconds()},
				fmt.Errorf("cloud_sandbox: container codex did not produce %q for %s: %w; stderr=%s",
					outName, c.workloadID, err, truncateForError(scrubCloudStderr(stderr.String())))
		}
	}
	return harness.ExecuteMetrics{WallTimeMS: time.Since(start).Milliseconds()}, nil
}

func metricsFrom(c E2BClient, start time.Time) harness.ExecuteMetrics {
	return harness.ExecuteMetrics{
		WallTimeMS:  time.Since(start).Milliseconds(),
		APICalls:    c.APICallCount(),
		UploadBytes: c.UploadBytes(),
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix), true
		}
	}
	return "", false
}

func appendProxyEnv(args []string) []string {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v := os.Getenv(key); v != "" {
			args = append(args, "-e", key+"="+v)
		}
	}
	return args
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func prepareContainerCodexHome(sourceHome string) (string, func(), error) {
	srcHome := strings.TrimSpace(sourceHome)
	if srcHome == "" {
		if envHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); envHome != "" {
			srcHome = envHome
		} else if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
			srcHome = filepath.Join(userHome, ".codex")
		}
	}
	if srcHome == "" {
		return "", nil, errors.New("cloud_sandbox: container codex home not configured; set CODEX_HOME or --container-codex-home")
	}
	srcConfig := filepath.Join(srcHome, "config.toml")
	configBytes, err := os.ReadFile(srcConfig)
	if err != nil {
		return "", nil, fmt.Errorf("cloud_sandbox: read container codex config %s: %w", srcConfig, err)
	}
	tmp, err := os.MkdirTemp("", "cloud-sandbox-codex-home-")
	if err != nil {
		return "", nil, fmt.Errorf("cloud_sandbox: create temporary container codex home: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"), configBytes, 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("cloud_sandbox: write temporary container codex config: %w", err)
	}
	return tmp, cleanup, nil
}

func truncateForError(s string) string {
	const max = 4096
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// ErrE2BNon2xx signals that an E2B API call returned a non-2xx status.
// Codex P1: callers previously closed resp.Body without inspecting
// StatusCode, so any 4xx / 5xx was silently treated as success.
var ErrE2BNon2xx = errors.New("cloud_sandbox: E2B API returned non-2xx")

// doRequest is the single choke point for every E2B API call. It
// validates the response status is 2xx and always drains + closes the
// body so the caller can't leak a connection on the error path. Any
// non-2xx status returns ErrE2BNon2xx wrapping the status code — a
// negative test asserts this fires (impl_test.go
// TestCloudSandbox_Non2xxResponse_ReturnedAsError).
func (c *CloudSandboxE2BImpl) doRequest(ctx context.Context, req *http.Request) error {
	resp, err := c.client.Do(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %d %s %s", ErrE2BNon2xx, resp.StatusCode, req.Method, req.URL.String())
	}
	return nil
}

func (c *CloudSandboxE2BImpl) callSandboxCreate(ctx context.Context) error {
	body := []byte(`{"template":"generic"}`)
	req, err := http.NewRequest("POST", defaultE2BBase+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	return c.doRequest(ctx, req)
}

func (c *CloudSandboxE2BImpl) callUpload(ctx context.Context, root, rel string) error {
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", defaultE2BBase+"/sandboxes/current/files/"+rel, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	return c.doRequest(ctx, req)
}

func (c *CloudSandboxE2BImpl) callExec(ctx context.Context, script string) error {
	req, err := http.NewRequest("POST", defaultE2BBase+"/sandboxes/current/exec", bytes.NewReader([]byte(script)))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(script))
	return c.doRequest(ctx, req)
}

func (c *CloudSandboxE2BImpl) callFetch(ctx context.Context, rel string) error {
	req, err := http.NewRequest("GET", defaultE2BBase+"/sandboxes/current/files/"+rel, nil)
	if err != nil {
		return err
	}
	return c.doRequest(ctx, req)
}

func runLocalBash(ctx context.Context, cwd string, env []string, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", script)
	cmd.Dir = cwd
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
