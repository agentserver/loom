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

func TestLogRelayErr_WritesToStderrAndAudit(t *testing.T) {
	dir := t.TempDir()
	audit, err := NewAuditLog(dir + "/audit.log")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()
	tools := &Tools{audit: audit}

	stderr := captureStderr(t, func() {
		tools.logRelayErr("update_write_task", errors.New("boom"))
	})

	if !strings.Contains(stderr, "driver: observer relay update_write_task: boom") {
		t.Fatalf("stderr missing message: %q", stderr)
	}

	// audit should contain an entry tagged observer_relay_error with the op
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

func TestLogRelayErr_NilErrorIsNoop(t *testing.T) {
	tools := &Tools{}
	stderr := captureStderr(t, func() {
		tools.logRelayErr("x", nil)
	})
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
}
