// Package main is the cloud_sandbox_e2b baseline binary. See
// docs/specs/wt2-baselines.spec.md §E3. This file holds the tiny E2B
// HTTP client + the fake used by tests.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// E2BClient is the interface Impl uses to talk to E2B. Real client
// wraps net/http; tests inject fakeE2BClient. Kept minimal — only the
// three verbs (sandbox create, upload file, exec command, download
// file) the baseline actually needs.
type E2BClient interface {
	// Do issues an HTTP request. `dryRun` is enforced INSIDE the real
	// client: on dryRun=true, Do panics — the panic-recovery layer in
	// harness.Run maps it to a hard exit 3, which is a loud failure any
	// test asserting "dry-run must not dial" can rely on.
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
	// APICallCount returns how many Do calls have completed so far. The
	// baseline row's metrics_baseline_api_calls is populated from this
	// after ExecuteAgent returns.
	APICallCount() int
	// UploadBytes returns the cumulative bytes actually uploaded (sum
	// of request Content-Length for successful PUT / POST calls). Feeds
	// row column 14.
	UploadBytes() int64
}

// realE2BClient is the production client. `dryRun` is stored so any
// stray Do call panics loudly — the invariant per spec §7(f).
type realE2BClient struct {
	base    string
	apiKey  string
	http    *http.Client
	dryRun  bool
	calls   int
	uploads int64
}

// newRealE2BClient constructs the real client. When `dryRun` is true
// the client is still constructed (for parity of code paths), but any
// Do invocation panics.
func newRealE2BClient(base, apiKey string, dryRun bool) *realE2BClient {
	return &realE2BClient{
		base:   base,
		apiKey: apiKey,
		http:   &http.Client{},
		dryRun: dryRun,
	}
}

func (c *realE2BClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if c.dryRun {
		// Loud failure — a test that lets dry-run reach here should
		// crash rather than silently dial. Panic is caught by the
		// harness's exit-3 recovery.
		panic(fmt.Sprintf("cloud_sandbox: dry-run mode reached real E2B client Do(%s %s); this is a spec §7(f) violation", req.Method, req.URL.String()))
	}
	req = req.WithContext(ctx)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	c.calls++
	if err == nil && resp != nil && req.ContentLength > 0 {
		c.uploads += req.ContentLength
	}
	return resp, err
}

func (c *realE2BClient) APICallCount() int  { return c.calls }
func (c *realE2BClient) UploadBytes() int64 { return c.uploads }

// dryRunPlannerClient is the client used when --dry-run is set. It
// does NOT dial anything — it captures the intended API call plan
// (method + path + Content-Length) and prints one line per call to
// stderr. All Do calls return a synthetic 200 response so the calling
// code can keep going through its state machine without special-casing.
type dryRunPlannerClient struct {
	stderr io.Writer
	calls  int
}

func newDryRunPlannerClient(stderr io.Writer) *dryRunPlannerClient {
	return &dryRunPlannerClient{stderr: stderr}
}

func (c *dryRunPlannerClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	fmt.Fprintf(c.stderr, "cloud_sandbox: [DRY-RUN] %s %s content-length=%d\n", req.Method, req.URL.String(), req.ContentLength)
	// Synthetic empty 200 so downstream code doesn't NPE on nil body.
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(nopReader{}),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (c *dryRunPlannerClient) APICallCount() int  { return c.calls }
func (c *dryRunPlannerClient) UploadBytes() int64 { return 0 }

// nopReader is a zero-byte io.Reader used to fill dry-run response
// bodies without allocating a bytes.Buffer.
type nopReader struct{}

func (nopReader) Read(p []byte) (int, error) { return 0, io.EOF }
