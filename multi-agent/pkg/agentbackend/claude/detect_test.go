package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectFailsWhenBinMissing(t *testing.T) {
	err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such-bin"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDetectPassesWhenBinExists(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := detect(context.Background(), bin); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
