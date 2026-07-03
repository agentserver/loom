package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// TestServePendingOnce_ContinuesPastSingleFailure verifies that when one
// upload fails, the loop still attempts the remaining requests instead of
// bailing out (which silently strands the rest forever).
// Fixes §1.1 #2 of docs/review-2026-06-13.md.
func TestServePendingOnce_ContinuesPastSingleFailure(t *testing.T) {
	tmp := t.TempDir()
	mkFile := func(name, body string) string {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	pA := mkFile("a.txt", "AAA")
	pB := mkFile("b.txt", "BBB")
	pC := mkFile("c.txt", "CCC")

	var uploaded sync.Map // artifactID -> true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/artifact-requests" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"requests":[
				{"request_id":"r1","artifact_id":"art-a","kind":"file","path":"` + pA + `","state":"pending"},
				{"request_id":"r2","artifact_id":"art-b","kind":"file","path":"` + pB + `","state":"pending"},
				{"request_id":"r3","artifact_id":"art-c","kind":"file","path":"` + pC + `","state":"pending"}
			]}`))
			return
		}
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/artifacts/") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"), "/content")
			if id == "art-b" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			uploaded.Store(id, true)
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Fatalf("unexpected req: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("t"))
	require := func(cond bool, msg string) {
		t.Helper()
		if !cond {
			t.Fatalf("%s", msg)
		}
	}
	require(relay != nil, "relay must be constructable")

	reg := NewFileRegistry(256)
	reg.RegisterObserverArtifact("art-a", pA, "file")
	reg.RegisterObserverArtifact("art-b", pB, "file")
	reg.RegisterObserverArtifact("art-c", pC, "file")

	err := relay.ServePendingOnce(context.Background(), reg, nil)
	require(err != nil, "expected aggregated error from failing upload")
	require(strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "500"),
		"err should reference upstream failure, got "+err.Error())
	_, okA := uploaded.Load("art-a")
	_, okC := uploaded.Load("art-c")
	require(okA, "a should have been uploaded BEFORE the failing b")
	require(okC, "c should have been uploaded AFTER the failing b — fix forgot to continue")
}

// TestServePendingLoop_LogsErrorsToStderrAndAudit confirms that the loop no
// longer silently swallows ServePendingOnce errors.
func TestServePendingLoop_LogsErrorsToStderrAndAudit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("t"))

	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		stderr := captureStderr(t, func() {
			relay.ServePendingLoop(ctx, NewFileRegistry(16), audit, 20*time.Millisecond)
		})
		done <- stderr
	}()
	// Let one tick happen, then cancel.
	time.Sleep(80 * time.Millisecond)
	cancel()

	var stderr string
	select {
	case stderr = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServePendingLoop did not exit after ctx cancel")
	}
	if !strings.Contains(stderr, "driver: observer relay serve pending:") {
		t.Fatalf("stderr missing log: %q", stderr)
	}
	body, _ := os.ReadFile(dir + "/audit.log")
	if !strings.Contains(string(body), `"event":"observer_relay_error"`) ||
		!strings.Contains(string(body), `"op":"serve_pending"`) {
		t.Fatalf("audit missing: %s", body)
	}
}

// TestServePendingLoop_SuppressesShutdownNoise verifies that errors caused by
// ctx cancellation during shutdown are NOT logged (would be spammy noise that
// looks like real failures during clean driver shutdowns).
func TestServePendingLoop_SuppressesShutdownNoise(t *testing.T) {
	// server blocks until ctx-cancel of the in-flight request returns ctx.Canceled
	hold := make(chan struct{})
	defer close(hold)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("t"))

	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		stderr := captureStderr(t, func() {
			relay.ServePendingLoop(ctx, NewFileRegistry(16), audit, 20*time.Millisecond)
		})
		done <- stderr
	}()
	// Let the first tick fire and block on the server, then cancel.
	time.Sleep(80 * time.Millisecond)
	cancel()

	var stderr string
	select {
	case stderr = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServePendingLoop did not exit after ctx cancel")
	}
	if strings.Contains(stderr, "driver: observer relay serve pending:") {
		t.Fatalf("shutdown noise leaked to stderr: %q", stderr)
	}
	body, _ := os.ReadFile(dir + "/audit.log")
	if strings.Contains(string(body), `"event":"observer_relay_error"`) {
		t.Fatalf("shutdown noise leaked to audit: %s", body)
	}
}

// writesServer returns an httptest server that responds to
// GET /api/writes with a payload built from `writes`. If `pad` > 0, the
// JSON response is padded with that many extra bytes (added inside an
// ignored `"pad"` string key) — used to push the response over the body
// cap without actually allocating a giant Content slice.
func writesServer(t *testing.T, writes []map[string]any, pad int) *httptest.Server {
	t.Helper()
	payload := map[string]any{"writes": writes}
	if pad > 0 {
		payload["pad"] = strings.Repeat("x", pad)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/writes") {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

// newSyncWritesRelay builds an ObserverRelay pointed at server with a
// caller-controlled maxWriteBytes cap. Both observer_lazy hardening tests
// use this so we don't have to allocate 1 GiB just to exercise the limiter.
func newSyncWritesRelay(server *httptest.Server, maxWriteBytes int64) *ObserverRelay {
	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("t"))
	if maxWriteBytes > 0 {
		relay.maxWriteBytes = maxWriteBytes
	}
	return relay
}

// b64 returns a base64-encoded form of data, matching how encoding/json
// emits []byte fields in the observer write payload.
func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// TestSyncWrites_HappyPath_WritesFileWithSecureModeAndCleansTmp pins the
// happy path: small Content lands at target, mode reflects umask but tmp is
// not left behind, and the returned record matches the on-disk content.
// Part of the PR #14 P1 follow-up: observer_lazy must match handlePut's
// invariants.
func TestSyncWrites_HappyPath_WritesFileWithSecureModeAndCleansTmp(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "out.txt")
	content := []byte("hello observer")

	server := writesServer(t, []map[string]any{{
		"write_id":  "w1",
		"path":      target,
		"overwrite": false,
		"bytes":     int64(len(content)),
		"content":   b64(content),
	}}, 0)
	defer server.Close()
	relay := newSyncWritesRelay(server, 0)

	got, err := relay.SyncWrites(context.Background(), "task-1", true, NewFileRegistry(8))
	if err != nil {
		t.Fatalf("SyncWrites: %v", err)
	}
	if len(got) != 1 || got[0].Path != target || got[0].Bytes != int64(len(content)) {
		t.Fatalf("unexpected written: %#v", got)
	}
	on, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(on) != string(content) {
		t.Fatalf("content mismatch: %q vs %q", on, content)
	}
	// No leftover tmp files in parent directory.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("tmp leak: %s", e.Name())
		}
	}
}

// TestSyncWrites_ResponseBodyCap rejects an oversized JSON response — the
// observer_lazy transport must not be exploitable as an OOM channel. Mirrors
// /files/put's body cap from Bug #18.
func TestSyncWrites_ResponseBodyCap(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "ok.txt")
	server := writesServer(t, []map[string]any{{
		"write_id":  "w1",
		"path":      target,
		"overwrite": true,
		"content":   b64([]byte("small")),
	}}, 4096) // pad far past the 256-byte cap below
	defer server.Close()

	relay := newSyncWritesRelay(server, 256)
	_, err := relay.SyncWrites(context.Background(), "task-1", true, NewFileRegistry(8))
	if err == nil {
		t.Fatalf("expected error from oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected body-cap error, got %v", err)
	}
	// Target must not be created when body is rejected upstream of decode.
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("target should not exist after rejection: %v", err)
	}
}

// TestSyncWrites_OverwriteFalse_RejectsExisting confirms overwrite=false
// refuses to clobber a pre-existing target, and leaves the original content
// untouched. Matches handlePut overwrite=false semantics.
func TestSyncWrites_OverwriteFalse_RejectsExisting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "exists.txt")
	if err := os.WriteFile(target, []byte("OLD"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	server := writesServer(t, []map[string]any{{
		"write_id":  "w1",
		"path":      target,
		"overwrite": false,
		"content":   b64([]byte("NEW")),
	}}, 0)
	defer server.Close()

	relay := newSyncWritesRelay(server, 0)
	_, err := relay.SyncWrites(context.Background(), "task-1", true, NewFileRegistry(8))
	if err == nil {
		t.Fatalf("expected error when overwrite=false and target exists")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Fatalf("expected exists error, got %v", err)
	}
	on, _ := os.ReadFile(target)
	if string(on) != "OLD" {
		t.Fatalf("target was clobbered: %q", on)
	}
	// No tmp leak in parent.
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("tmp leak: %s", e.Name())
		}
	}
}

// TestSyncWrites_OverwriteTrue_ReplacesExisting confirms overwrite=true
// atomically replaces the prior content.
func TestSyncWrites_OverwriteTrue_ReplacesExisting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "existing.txt")
	if err := os.WriteFile(target, []byte("OLD"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	server := writesServer(t, []map[string]any{{
		"write_id":  "w1",
		"path":      target,
		"overwrite": true,
		"content":   b64([]byte("NEW")),
	}}, 0)
	defer server.Close()

	relay := newSyncWritesRelay(server, 0)
	if _, err := relay.SyncWrites(context.Background(), "task-1", true, NewFileRegistry(8)); err != nil {
		t.Fatalf("SyncWrites: %v", err)
	}
	on, _ := os.ReadFile(target)
	if string(on) != "NEW" {
		t.Fatalf("expected NEW, got %q", on)
	}
}

// TestObserverRelay_WriteDryRunBlock exercises the driver→observer HTTP
// path end-to-end (WT-2-dry-run-validator §4.3). Confirms:
//   - POST /api/dry-run-blocks with correct wire body
//   - bearer-token header attached
//   - success on 2xx status; error on non-2xx
//   - nil relay is a silent no-op
func TestObserverRelay_WriteDryRunBlock(t *testing.T) {
	var lastMethod, lastPath, lastAuth string
	var lastBody []byte
	status := http.StatusCreated

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastMethod, lastPath = r.Method, r.URL.Path
		lastAuth = r.Header.Get("Authorization")
		lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	defer server.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = server.URL
	relay := NewObserverRelay(cfg, stubTokenSource("bearer-xyz"))
	if relay == nil {
		t.Fatal("relay must be constructable")
	}

	row := observerstore.DryRunBlockRow{
		BlockID: "blk-relay-1", AttemptID: "att-relay-1", ConversationID: "conv-relay-1",
		ExperimentID: "exp-1", ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "policy_violation",
		Field:     "execution_policy.required_reach",
		Expected:  "internet",
		Actual:    "intranet",
		Detail:    "execution_policy.required_reach: expected internet, actual intranet",
		BlockedAt: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC),
	}
	if err := relay.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatalf("WriteDryRunBlock: %v", err)
	}
	if lastMethod != http.MethodPost || lastPath != "/api/dry-run-blocks" {
		t.Errorf("wire method+path: got %s %s want POST /api/dry-run-blocks", lastMethod, lastPath)
	}
	if lastAuth != "Bearer bearer-xyz" {
		t.Errorf("Authorization header: got %q want %q", lastAuth, "Bearer bearer-xyz")
	}
	// Validate the JSON body carries all row fields.
	var got observerDryRunBlockSave
	if err := json.Unmarshal(lastBody, &got); err != nil {
		t.Fatalf("body unmarshal: %v; body=%s", err, string(lastBody))
	}
	// Assert every field carried across the wire — a silent drop of
	// (e.g.) contract_hash would defeat the §6.1 consumer SELECT
	// without any test noticing.
	if got.BlockID != row.BlockID {
		t.Errorf("BlockID: got %q want %q", got.BlockID, row.BlockID)
	}
	if got.AttemptID != row.AttemptID {
		t.Errorf("AttemptID: got %q want %q", got.AttemptID, row.AttemptID)
	}
	if got.ConversationID != row.ConversationID {
		t.Errorf("ConversationID: got %q want %q", got.ConversationID, row.ConversationID)
	}
	if got.ExperimentID != row.ExperimentID {
		t.Errorf("ExperimentID: got %q want %q", got.ExperimentID, row.ExperimentID)
	}
	if got.ContractHash != row.ContractHash {
		t.Errorf("ContractHash: got %q want %q", got.ContractHash, row.ContractHash)
	}
	if got.CapabilitySnapshotHash != row.CapabilitySnapshotHash {
		t.Errorf("CapabilitySnapshotHash: got %q want %q", got.CapabilitySnapshotHash, row.CapabilitySnapshotHash)
	}
	if got.BlockKind != row.BlockKind {
		t.Errorf("BlockKind: got %q want %q", got.BlockKind, row.BlockKind)
	}
	if got.Field != row.Field {
		t.Errorf("Field: got %q want %q", got.Field, row.Field)
	}
	if got.Expected != row.Expected {
		t.Errorf("Expected: got %q want %q", got.Expected, row.Expected)
	}
	if got.Actual != row.Actual {
		t.Errorf("Actual: got %q want %q", got.Actual, row.Actual)
	}
	if got.Detail != row.Detail {
		t.Errorf("Detail: got %q want %q", got.Detail, row.Detail)
	}
	if got.BlockedAt != row.BlockedAt.UTC().Format(time.RFC3339Nano) {
		t.Errorf("BlockedAt: got %q want %q", got.BlockedAt, row.BlockedAt.UTC().Format(time.RFC3339Nano))
	}

	// Non-2xx surfaces as an error.
	status = http.StatusInternalServerError
	if err := relay.WriteDryRunBlock(context.Background(), row); err == nil {
		t.Error("expected error on 500 response; got nil")
	}

	// nil relay is a silent no-op.
	var nilRelay *ObserverRelay
	if err := nilRelay.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Errorf("nil relay must be a silent no-op; got %v", err)
	}
}
