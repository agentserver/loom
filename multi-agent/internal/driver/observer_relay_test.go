package driver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
