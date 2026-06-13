package driver

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr swaps os.Stderr for a pipe; returns the captured bytes after
// the closure runs and restores stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}

func TestLogHelperErr_ObserverRelayCategory(t *testing.T) {
	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()
	tools := &Tools{audit: audit}

	stderr := captureStderr(t, func() {
		tools.logHelperErr("observer_relay", "update_write_task", errors.New("boom"))
	})

	if !strings.Contains(stderr, "driver: observer_relay update_write_task: boom") {
		t.Fatalf("stderr missing message: %q", stderr)
	}

	body, err := os.ReadFile(dir + "/audit.log")
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(string(body), `"event":"observer_relay_error"`) ||
		!strings.Contains(string(body), `"op":"update_write_task"`) ||
		!strings.Contains(string(body), `"error":"boom"`) {
		t.Fatalf("audit missing fields: %s", body)
	}
}

// TestLogHelperErr_DriverJournalCategory pins the category separation that
// fixes PR #10 P2: journal-append failures must NOT be classified as observer
// relay failures (was the bug pre-fix — see record_delegated_task sites).
func TestLogHelperErr_DriverJournalCategory(t *testing.T) {
	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()
	tools := &Tools{audit: audit}

	stderr := captureStderr(t, func() {
		tools.logHelperErr("driver_journal", "record_delegated_task", errors.New("disk full"))
	})

	if !strings.Contains(stderr, "driver: driver_journal record_delegated_task: disk full") {
		t.Fatalf("stderr missing message: %q", stderr)
	}
	if strings.Contains(stderr, "observer relay") || strings.Contains(stderr, "observer_relay") {
		t.Fatalf("journal failure leaked into observer relay namespace: %q", stderr)
	}

	body, err := os.ReadFile(dir + "/audit.log")
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(string(body), `"event":"driver_journal_error"`) ||
		!strings.Contains(string(body), `"op":"record_delegated_task"`) ||
		!strings.Contains(string(body), `"error":"disk full"`) {
		t.Fatalf("audit missing fields: %s", body)
	}
	if strings.Contains(string(body), `"event":"observer_relay_error"`) {
		t.Fatalf("journal failure misfiled as observer_relay_error: %s", body)
	}
}

func TestLogHelperErr_NilErrorIsNoop(t *testing.T) {
	tools := &Tools{}
	stderr := captureStderr(t, func() {
		tools.logHelperErr("observer_relay", "x", nil)
	})
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
}
