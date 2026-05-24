package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectFailsWhenBinMissing(t *testing.T) {
	if err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such")); err == nil {
		t.Fatal("expected error")
	}
}
func TestDetectPassesWhenBinExists(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex")
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if err := detect(context.Background(), bin); err != nil {
		t.Fatal(err)
	}
}
