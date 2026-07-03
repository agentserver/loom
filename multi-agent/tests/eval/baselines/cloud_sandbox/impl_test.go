package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yourorg/multi-agent/tests/eval/baselines/harness"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
}

// noDialClient is an E2BClient that fails the test if Do is ever
// invoked. Used to prove the dry-run branch never dials.
type noDialClient struct {
	t     *testing.T
	calls int32
}

func (c *noDialClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	c.t.Errorf("noDialClient.Do called with %s %s — dry-run must not dial", req.Method, req.URL.String())
	atomic.AddInt32(&c.calls, 1)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(nopReader{}), Request: req}, nil
}
func (c *noDialClient) APICallCount() int  { return int(atomic.LoadInt32(&c.calls)) }
func (c *noDialClient) UploadBytes() int64 { return 0 }

// countingClient is an E2BClient that counts and answers 200 without
// dialing anything. Used to assert metrics_baseline_api_calls populates.
type countingClient struct {
	calls   int32
	uploads int64
}

func (c *countingClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&c.calls, 1)
	if req.ContentLength > 0 {
		atomic.AddInt64(&c.uploads, req.ContentLength)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(nopReader{}), Request: req}, nil
}
func (c *countingClient) APICallCount() int  { return int(atomic.LoadInt32(&c.calls)) }
func (c *countingClient) UploadBytes() int64 { return atomic.LoadInt64(&c.uploads) }

// TestCloudSandbox_PreUploadSecret_Rejected — plan #28. A workspace
// with an sk-... in a fixture MUST be refused pre-flight; ZERO API
// calls issued; exit 2. Spec §7(b), the worktree's single most
// dangerous failure mode.
func TestCloudSandbox_PreUploadSecret_Rejected(t *testing.T) {
	// Set up a fake workload dir under a tempdir; the harness's
	// SetupWorkspace copies its fixtures/ into ws.Root.
	dir := t.TempDir()
	workloadDir := filepath.Join(dir, "workloads")
	wl := filepath.Join(workloadDir, "secret-leak-test")
	if err := os.MkdirAll(filepath.Join(wl, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wl, "spec.yaml"),
		[]byte("id: secret-leak-test\nsuccess_oracle: ./oracle.sh\ntimeout_seconds: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wl, "oracle.sh"),
		[]byte("#!/usr/bin/env bash\nprintf '%s\\n' '{\"passed\":true,\"details\":{},\"metrics\":{}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fixture with the sk- token — the scan must reject.
	if err := os.WriteFile(filepath.Join(wl, "fixtures", "leaky.py"),
		[]byte("key = 'sk-testonly-secret-1234567890AB'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Inject a no-dial client so a bug that skipped the scan would
	// surface as "test fails because Do was called".
	client := &noDialClient{t: t}
	impl := &CloudSandboxE2BImpl{
		workloadID: "secret-leak-test",
		client:     client,
		forwardE2B: false,
	}
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "secret-leak-test",
		WorkloadDir: workloadDir,
		OutCSV:      out,
		DryRun:      false, // scan MUST reject even without dry-run
	}, impl, io.Discard)

	if res.ExitCode != 2 {
		t.Fatalf("want exit 2 (pre-flight abort), got %d; err=%v", res.ExitCode, res.Err)
	}
	if !errors.Is(res.Err, ErrPreUploadSecretDetected) {
		t.Errorf("want ErrPreUploadSecretDetected, got %v", res.Err)
	}
	if client.APICallCount() != 0 {
		t.Errorf("secret scan must halt BEFORE any API call; got %d calls", client.APICallCount())
	}
	// CSV MUST NOT exist (pre-flight abort per spec exit-code table).
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("CSV should not exist on pre-flight fail; stat err=%v", err)
	}
}

// TestCloudSandbox_DryRun_NoDial — plan #29. With --dry-run, the E2B
// client is the planner variant that logs and doesn't dial; the real
// client's Do would panic. Prove via the planner that no dial occurred.
func TestCloudSandbox_DryRun_NoDial(t *testing.T) {
	// Full dry-run happy path: real workload, mock_workspace projection.
	out := filepath.Join(t.TempDir(), "row.csv")
	var stderr strings.Builder
	impl := NewImpl("cross-device-code-mod", false, "", true, &stderr)
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      true,
	}, impl, io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("want exit 0, got %d; err=%v", res.ExitCode, res.Err)
	}
	// Planner logs at least the sandbox create, uploads, exec, fetches.
	if !strings.Contains(stderr.String(), "[DRY-RUN]") {
		t.Errorf("expected planner log lines; stderr=%q", stderr.String())
	}
}

// TestCloudSandbox_RealClient_PanicsInDryRun — the real client is
// forbidden from dialing when dryRun=true; the panic-based invariant
// per spec §7(f).
func TestCloudSandbox_RealClient_PanicsInDryRun(t *testing.T) {
	c := newRealE2BClient("https://example.invalid", "key", true)
	req, _ := http.NewRequest("GET", "https://example.invalid/x", nil)
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic when dry-run reaches real client")
		}
	}()
	_, _ = c.Do(context.Background(), req)
}

// TestCloudSandbox_CIRunRequiresDryRun — plan #30. run.sh refuses when
// CI=true and no --dry-run.
func TestCloudSandbox_CIRunRequiresDryRun(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	sh := filepath.Join(filepath.Dir(self), "run.sh")
	// Invoke `bash run.sh --workload x --out y` with CI=true, no
	// --dry-run. The shell script must reject with exit 2.
	cmd := exec.Command("bash", sh, "--workload", "cross-device-code-mod", "--out", filepath.Join(t.TempDir(), "row.csv"))
	cmd.Env = append(os.Environ(), "CI=true")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("run.sh should have exited non-zero with CI=true and no --dry-run; output=%s", string(out))
	}
	if !strings.Contains(string(out), "§7(h)") {
		t.Errorf("expected §7(h) message; got %q", string(out))
	}
}

// TestCloudSandbox_RealMode_CountsAPICalls — plan #31. A countingClient
// records the number of Do calls; the ExecuteMetrics values feed the
// CSV columns. Confirm the row column matches.
func TestCloudSandbox_RealMode_CountsAPICalls(t *testing.T) {
	client := &countingClient{}
	impl := &CloudSandboxE2BImpl{
		workloadID: "cross-device-code-mod",
		client:     client,
		forwardE2B: false,
	}
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(moduleRoot(t), "tests/eval/workloads"),
		OutCSV:      out,
		DryRun:      false, // real mode with fake client
	}, impl, io.Discard)
	if res.ExitCode != 0 && res.ExitCode != 1 {
		t.Fatalf("want exit 0/1, got %d; err=%v", res.ExitCode, res.Err)
	}
	if res.Row.MetricsBaselineAPICalls == 0 {
		t.Errorf("expected non-zero api_calls; got 0. client.APICallCount=%d", client.APICallCount())
	}
	if res.Row.MetricsBaselineAPICalls != client.APICallCount() {
		t.Errorf("api_calls mismatch: row=%d client=%d", res.Row.MetricsBaselineAPICalls, client.APICallCount())
	}
}

// TestCloudSandbox_ForwardFlag_ControlsKeyPropagation — plan #27c.
// When --forward-e2b-api-key=false, real client is constructed with
// empty apiKey (Authorization header omitted); when true, key propagates
// to the client only.
func TestCloudSandbox_ForwardFlag_ControlsKeyPropagation(t *testing.T) {
	// Case 1: opt-out (default) — apiKey empty.
	implOff := NewImpl("cross-device-code-mod", false, "", false, io.Discard)
	if real, ok := implOff.client.(*realE2BClient); ok {
		if real.apiKey != "" {
			t.Errorf("opt-out: real client apiKey should be empty; got %q", real.apiKey)
		}
	} else {
		t.Fatalf("expected realE2BClient in real mode; got %T", implOff.client)
	}
	// Case 2: opt-in — apiKey passed to client.
	implOn := NewImpl("cross-device-code-mod", true, "e2b-test-key", false, io.Discard)
	if real, ok := implOn.client.(*realE2BClient); ok {
		if real.apiKey != "e2b-test-key" {
			t.Errorf("opt-in: real client apiKey should propagate; got %q", real.apiKey)
		}
	} else {
		t.Fatalf("expected realE2BClient in real mode; got %T", implOn.client)
	}
	// AgentForwards is always nil — the key never enters any subprocess env.
	if got := implOn.AgentForwards(); got != nil {
		t.Errorf("AgentForwards must be nil (key never in subprocess env); got %v", got)
	}
}

// erroringClient always returns a non-2xx.
type erroringClient struct {
	status int
	calls  int32
}

func (c *erroringClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&c.calls, 1)
	return &http.Response{
		StatusCode: c.status,
		Body:       io.NopCloser(nopReader{}),
		Request:    req,
	}, nil
}
func (c *erroringClient) APICallCount() int  { return int(atomic.LoadInt32(&c.calls)) }
func (c *erroringClient) UploadBytes() int64 { return 0 }

// TestCloudSandbox_Non2xxResponse_ReturnedAsError — Codex Stage 3 P1
// negative test. If E2B returns a 4xx/5xx, the client's Do returns
// nil error (the request went through), so the impl must inspect
// StatusCode itself. Prior code closed the body without checking; a
// 500 was silently treated as success.
func TestCloudSandbox_Non2xxResponse_ReturnedAsError(t *testing.T) {
	client := &erroringClient{status: 500}
	impl := &CloudSandboxE2BImpl{
		workloadID: "cross-device-code-mod",
		client:     client,
		forwardE2B: false,
		// Empty safeToUpload — Prepare would populate it, but this test
		// exercises callSandboxCreate which is the first API call.
	}
	// Directly exercise the request path.
	err := impl.callSandboxCreate(context.Background())
	if !errors.Is(err, ErrE2BNon2xx) {
		t.Fatalf("want ErrE2BNon2xx on 500 status, got %v", err)
	}
}

// TestCloudSandbox_UnscannedBinary_NotUploaded — Codex Stage 3 P0
// negative test. Confirms the "skipped ≠ safe" invariant: a binary
// file in the workspace lands in SkippedBinary and is NOT sent to the
// cloud client.
func TestCloudSandbox_UnscannedBinary_NotUploaded(t *testing.T) {
	// Build a workload dir + fixtures with a binary file whose NUL
	// bytes make ScanTreeForSecrets skip it. The impl must NOT upload
	// it.
	dir := t.TempDir()
	workloadDir := filepath.Join(dir, "workloads")
	wl := filepath.Join(workloadDir, "binary-fixture-test")
	if err := os.MkdirAll(filepath.Join(wl, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wl, "spec.yaml"),
		[]byte("id: binary-fixture-test\nsuccess_oracle: ./oracle.sh\ntimeout_seconds: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wl, "oracle.sh"),
		[]byte("#!/usr/bin/env bash\nprintf '%s\\n' '{\"passed\":true,\"details\":{},\"metrics\":{}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Binary fixture — NUL in first byte, no secret pattern.
	if err := os.WriteFile(filepath.Join(wl, "fixtures", "opaque.bin"),
		append([]byte{0, 0, 0, 0}, []byte("random data")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plain text fixture — this should be uploaded.
	if err := os.WriteFile(filepath.Join(wl, "fixtures", "readme.txt"),
		[]byte("plain text\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Track which paths get uploaded. Use a client that records the URL
	// of every request that looks like an upload.
	recordingC := &recordingClient{}
	// Use planOverride instead of mutating the package-level cloudPlans
	// map — avoids a race if this test is ever run under t.Parallel().
	impl := &CloudSandboxE2BImpl{
		workloadID: "binary-fixture-test",
		client:     recordingC,
		forwardE2B: false,
		planOverride: &remoteExecPlan{
			ExecScript: "echo ok > out.txt",
			FetchFiles: []string{"out.txt"},
		},
	}
	out := filepath.Join(t.TempDir(), "row.csv")
	res := harness.Run(context.Background(), harness.Opts{
		WorkloadID:  "binary-fixture-test",
		WorkloadDir: workloadDir,
		OutCSV:      out,
		DryRun:      false,
	}, impl, io.Discard)
	if res.ExitCode != 0 && res.ExitCode != 1 {
		t.Fatalf("unexpected exit %d; err=%v", res.ExitCode, res.Err)
	}
	// Assert opaque.bin NEVER appeared in an upload URL.
	for _, u := range recordingC.uploadURLs {
		if strings.HasSuffix(u, "opaque.bin") {
			t.Errorf("binary file was uploaded: %s (P0 leak path)", u)
		}
	}
	// And confirm the plain-text fixture WAS uploaded (positive control).
	found := false
	for _, u := range recordingC.uploadURLs {
		if strings.HasSuffix(u, "readme.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("plain text file should have been uploaded; got URLs %v", recordingC.uploadURLs)
	}
}

// recordingClient collects the URL of every PUT-shaped request so a
// test can verify "was file X uploaded?".
type recordingClient struct {
	calls      int32
	uploads    int64
	uploadURLs []string
}

func (c *recordingClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&c.calls, 1)
	if req.Method == "PUT" {
		c.uploadURLs = append(c.uploadURLs, req.URL.String())
		if req.ContentLength > 0 {
			atomic.AddInt64(&c.uploads, req.ContentLength)
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(nopReader{}), Request: req}, nil
}
func (c *recordingClient) APICallCount() int  { return int(atomic.LoadInt32(&c.calls)) }
func (c *recordingClient) UploadBytes() int64 { return atomic.LoadInt64(&c.uploads) }

// TestCloudSandbox_BuildsBinary.
func TestCloudSandbox_BuildsBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in -short")
	}
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	bin := filepath.Join(t.TempDir(), "cloud_sandbox")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
}
