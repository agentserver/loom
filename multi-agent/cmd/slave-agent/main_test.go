package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/platform"
)

func TestHasSkill(t *testing.T) {
	if !hasSkill([]string{"chat", "bash"}, "bash") {
		t.Fatal("expected bash skill")
	}
	if !hasSkill([]string{"chat", "claude_permissions"}, "claude_permissions") {
		t.Fatal("expected claude_permissions skill")
	}
	if hasSkill([]string{"chat"}, "bash") {
		t.Fatal("did not expect bash skill")
	}
}

func TestHasSkill_File(t *testing.T) {
	if !hasSkill([]string{"chat", "file"}, "file") {
		t.Fatal("expected file skill")
	}
	if hasSkill([]string{"chat"}, "file") {
		t.Fatal("did not expect file skill")
	}
}

func TestAcquireInstanceLockManualSecondRefuses(t *testing.T) {
	withTempWorkDir(t)
	t.Setenv("INVOCATION_ID", "")

	first, err := acquireInstanceLock()
	if err != nil {
		t.Fatalf("first acquireInstanceLock: %v", err)
	}
	defer first.Unlock()

	second, err := acquireInstanceLock()
	if err == nil {
		second.Unlock()
		t.Fatal("second acquireInstanceLock succeeded, want already running error")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquireInstanceLock error = %v, want already running", err)
	}
}

func TestAcquireInstanceLockManagedUnknownHolderReturnsHeldLockAndUpdatesPID(t *testing.T) {
	dir := withTempWorkDir(t)
	t.Setenv("INVOCATION_ID", "test")
	lockPath := filepath.Join(dir, "slave-agent.lock")

	blocker, err := platform.TryLock(lockPath)
	if err != nil {
		t.Fatalf("blocker TryLock: %v", err)
	}
	if err := blocker.WriteString("not-a-pid\n"); err != nil {
		t.Fatalf("blocker WriteString: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = blocker.Unlock()
		close(released)
	}()

	lock, err := acquireInstanceLock()
	if err != nil {
		t.Fatalf("managed acquireInstanceLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	<-released

	contender, err := platform.TryLock(lockPath)
	if err == nil {
		contender.Unlock()
		t.Fatal("managed acquireInstanceLock returned without holding lock")
	}
	if !errors.Is(err, platform.ErrLocked) {
		t.Fatalf("contender TryLock error = %v, want ErrLocked", err)
	}

	if err := lock.Unlock(); err != nil {
		t.Fatalf("managed lock Unlock: %v", err)
	}
	got, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := fmt.Sprintf("%d\n", os.Getpid())
	if string(got) != want {
		t.Fatalf("lock file content = %q, want %q", got, want)
	}
}

func TestTakeOverLockReturnsNonLockTryLockErrorImmediately(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "not-dir")
	if err := os.WriteFile(notDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	start := time.Now()
	lock, err := takeOverLock(filepath.Join(notDir, "slave-agent.lock"), 0)
	if lock != nil {
		lock.Unlock()
		t.Fatal("takeOverLock returned lock, want error")
	}
	if err == nil {
		t.Fatal("takeOverLock returned nil error, want non-lock error")
	}
	if errors.Is(err, platform.ErrLocked) {
		t.Fatalf("takeOverLock error = %v, want non-lock error", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("takeOverLock took %s for non-lock error, want immediate return", elapsed)
	}
	if !strings.Contains(err.Error(), "try lock") {
		t.Fatalf("takeOverLock error = %v, want try lock context", err)
	}
}

func withTempWorkDir(t *testing.T) string {
	t.Helper()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})
	return dir
}
