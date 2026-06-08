package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockRejectsConcurrentHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	first, err := TryLock(path)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	defer first.Unlock()

	second, err := TryLock(path)
	if err == nil {
		second.Unlock()
		t.Fatal("second TryLock succeeded, want locked error")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock error = %v, want ErrLocked", err)
	}
}

func TestTryLockCanReacquireAfterUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.lock")
	first, err := TryLock(path)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}

	second, err := TryLock(path)
	if err != nil {
		t.Fatalf("second TryLock after unlock: %v", err)
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
}

func TestFileLockWriteStringWritesThroughLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holder.lock")
	lock, err := TryLock(path)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if err := lock.WriteString("123\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "123\n" {
		t.Fatalf("lock file content = %q, want %q", got, "123\n")
	}
}
