package platform

import (
	"os"
	"testing"
)

func TestProcessExistsCurrentPID(t *testing.T) {
	if !ProcessExists(os.Getpid()) {
		t.Fatalf("ProcessExists(%d) = false, want true", os.Getpid())
	}
}

func TestProcessExistsInvalidPID(t *testing.T) {
	if ProcessExists(-1) {
		t.Fatal("ProcessExists(-1) = true, want false")
	}
}

func TestShutdownSignalsIsNonEmpty(t *testing.T) {
	if len(ShutdownSignals()) == 0 {
		t.Fatal("ShutdownSignals returned no signals")
	}
}
