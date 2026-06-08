//go:build !windows

package platform

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUnixLockErrorMapsContentionToErrLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	for _, err := range []error{syscall.EWOULDBLOCK, syscall.EAGAIN} {
		got := lockError(path, err)
		if !errors.Is(got, ErrLocked) {
			t.Fatalf("lockError(%v) = %v, want ErrLocked", err, got)
		}
	}
}

func TestUnixLockErrorPreservesNonContentionError(t *testing.T) {
	err := syscall.EINVAL
	got := lockError(filepath.Join(t.TempDir(), "session.lock"), err)
	if errors.Is(got, ErrLocked) {
		t.Fatalf("lockError(%v) = %v, want non-ErrLocked", err, got)
	}
	if !errors.Is(got, err) {
		t.Fatalf("lockError(%v) = %v, want original error preserved", err, got)
	}
}
