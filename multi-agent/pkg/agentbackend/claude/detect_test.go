package claude

import (
	"context"
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
	bin := buildFakeClaude(t, `package main

func main() {}
`)
	if err := detect(context.Background(), bin); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
